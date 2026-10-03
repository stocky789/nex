// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
)

// A Controller-driven pool join is two Core calls and no Controller state: the master mints
// a single-use invitation over one report round trip, and the joining host redeems it by
// itself under one durable command. Everything shown by the projections below is what the
// hosts reported; a fresh Controller rebuilds all of it from their next reports.
type poolJoinPending struct {
	request    protocol.PoolRequest
	result     chan protocol.PoolResult
	dispatched bool
}
type poolJoinBroker struct {
	mu      sync.Mutex
	pending map[string]poolJoinPending
}

// poolJoinWait must comfortably exceed a host heartbeat: Core answers a pool request on its
// next report, not on the one that carried it. It is a variable only so tests can shorten it.
var poolJoinWait = 45 * time.Second

// poolBMCProbeWait covers the two minutes Core gives a management controller probe, plus the
// report that carries its answer back.
var poolBMCProbeWait = 3 * time.Minute

// poolJoinInvitationTTL matches the master's invitation lifetime. It bounds how long a
// dispatched command row may still carry the invitation before it is scrubbed.
const poolJoinInvitationTTL = 15 * time.Minute

// poolJoinInvitationSkew is how far ahead the Controller clock may be relative to the
// master when deciding whether an invitation answer is still collectable. Core remains
// authoritative for actual join-time expiry.
const poolJoinInvitationSkew = 2 * time.Minute

// statusClientClosedRequest is the conventional code for "the client went away first". It
// never reaches that client; it keeps an abandoned request out of the success range.
const statusClientClosedRequest = 499

func (c *Controller) joinBroker() *poolJoinBroker {
	c.poolJoinOnce.Do(func() { c.poolJoins.pending = map[string]poolJoinPending{} })
	return &c.poolJoins
}

func (c *Controller) takePoolRequests(host string) []protocol.PoolRequest {
	b := c.joinBroker()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []protocol.PoolRequest
	for id, pending := range b.pending {
		if pending.request.HostID != host || pending.dispatched {
			continue
		}
		out = append(out, pending.request)
		pending.dispatched = true
		b.pending[id] = pending
	}
	return out
}

func (c *Controller) completePoolResults(host string, results []protocol.PoolResult) {
	b := c.joinBroker()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, result := range results {
		pending, ok := b.pending[result.RequestID]
		if !ok || result.HostID != host || result.HostID != pending.request.HostID || result.PoolID != pending.request.PoolID || !poolResultValid(pending.request.Action, result) {
			continue
		}
		select {
		case pending.result <- result:
		default:
		}
		delete(b.pending, result.RequestID)
	}
}

// poolResultValid bounds what a host may put into a brokered answer, for the action that was
// asked: a succeeded invitation must carry a usable invitation, a succeeded promotion
// preflight must carry a verdict for the host that was asked, a management controller probe
// carries a well-formed answer (a failed one may still say what answered), and a failed
// result carries nothing else. Nothing in an answer is ever stored, so this is the whole of the trust boundary.
func poolResultValid(action string, v protocol.PoolResult) bool {
	if (v.Status != "succeeded" && v.Status != "failed") || len(v.Error) > 4096 || v.ObservedAt.IsZero() {
		return false
	}
	if v.Status == "failed" {
		return v.Invitation == nil && v.Promotion == nil && (v.BMCProbe == nil || action == "bmc-probe" && v.BMCProbe.Valid() == nil)
	}
	if action == "bmc-probe" {
		return v.Invitation == nil && v.Promotion == nil && v.BMCProbe != nil && v.BMCProbe.Valid() == nil
	}
	if v.BMCProbe != nil {
		return false
	}
	if action == "promotion-preflight" {
		return v.Invitation == nil && v.Promotion != nil && v.Promotion.CandidateHostID == v.HostID
	}
	if v.Promotion != nil {
		return false
	}
	i := v.Invitation
	return i != nil && bareHTTPSOrigin(i.MasterURL) && i.PoolCAPEM != "" && len(i.PoolCAPEM) <= 16384 && i.Invitation != "" && len(i.Invitation) <= 8192 && poolJoinInvitationCollectable(i.ExpiresAt)
}

func poolJoinInvitationCollectable(expiresAt time.Time) bool {
	return !expiresAt.IsZero() && time.Until(expiresAt) > -poolJoinInvitationSkew
}

// bareHTTPSOrigin accepts exactly "https://host[:port]", optionally with a single trailing
// "/": no other path, no query, fragment or userinfo. This is what the host-local API accepts
// (validHTTPSOrigin in internal/localcore/handoff.go), so the same string cannot be good
// enough for one end of the join and not the other.
func bareHTTPSOrigin(s string) bool { return normalizedHTTPSOrigin(s) != "" }

// normalizedHTTPSOrigin returns the canonical "https://host[:port]" form, or "" when s is not
// a bare origin. The Controller normalises before storing the replay payload and before
// forwarding, so "https://x:9444" and "https://x:9444/" are deliberately the *same* request
// for an exact retry: they address the same host, and a retry that only differs in a trailing
// slash is a retry, not a changed payload.
func normalizedHTTPSOrigin(s string) string {
	if s == "" || len(s) > 512 {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	origin := "https://" + u.Host
	if s != origin && s != origin+"/" {
		return ""
	}
	return origin
}

type poolMemberAPIRequest struct {
	RequestID  string `json:"request_id"`
	HostID     string `json:"host_id"`
	PeerOrigin string `json:"peer_origin"`
	// MasterURL optionally overrides the address the joining host will dial the master on.
	// Left empty, the master works it out itself; once the master has recorded one, that
	// reported value wins.
	MasterURL string `json:"master_url,omitempty"`
}

// poolJoinPayload is the replay record: the client request plus the pool it addressed, so a
// retry of the same request_id against another pool is a changed payload. It deliberately
// holds no invitation.
type poolJoinPayload struct {
	PoolID string `json:"pool_id"`
	poolMemberAPIRequest
}

// addPoolMember relays one join. The Controller validates only what it can see, asks the
// master for an invitation, and hands it to the joining host as a durable command; every
// refusal that matters is still Core's.
func (c *Controller) addPoolMember(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.scrubPoolJoinInvitations()
	pool := r.PathValue("id")
	var body poolMemberAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(pool) || !nameValid(body.RequestID) || !nameValid(body.HostID) || !bareHTTPSOrigin(body.PeerOrigin) || (body.MasterURL != "" && !bareHTTPSOrigin(body.MasterURL)) {
		fail(w, http.StatusBadRequest, "pool, stable request_id, host_id and a bare https peer origin are required")
		return
	}
	body.PeerOrigin, body.MasterURL = normalizedHTTPSOrigin(body.PeerOrigin), normalizedHTTPSOrigin(body.MasterURL)
	payload := poolJoinPayload{PoolID: pool, poolMemberAPIRequest: body}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "pool-join", payload) {
		return
	}
	master, masterURL, status, err := c.poolJoinTargets(tx, pool, body.HostID, body.MasterURL)
	if err != nil {
		fail(w, status, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	result, ok := c.relayPoolRequest(w, r, protocol.PoolRequest{RequestID: random(), PoolID: pool, HostID: master, Action: "invite", MemberHostID: body.HostID, MasterURL: masterURL})
	if !ok {
		return
	}
	if result.Status != "succeeded" {
		// Core's own words when it has any; a terse master must still not become an empty
		// refusal an operator cannot read.
		refusal := result.Error
		if refusal == "" {
			refusal = "the pool master refused to issue a join invitation"
		}
		fail(w, http.StatusConflict, refusal)
		return
	}
	c.commandPoolJoin(w, user, pool, payload, *result.Invitation)
}

// poolJoinTargets validates both ends of the join and returns the master and the URL the
// joining host will dial. The status is the code its error must be reported with.
func (c *Controller) poolJoinTargets(tx *sql.Tx, pool, host, requestedMaster string) (string, string, int, error) {
	var master, targetSite string
	if err := tx.QueryRow("SELECT master_host_id,site_id FROM pools WHERE id=?", pool).Scan(&master, &targetSite); err != nil || master == "" {
		return "", "", http.StatusNotFound, fmt.Errorf("pool not found")
	}
	var joiningPool, joiningSite string
	if err := tx.QueryRow("SELECT h.pool_id,p.site_id FROM hosts h JOIN pools p ON p.id=h.pool_id WHERE h.id=?", host).Scan(&joiningPool, &joiningSite); err != nil {
		return "", "", http.StatusNotFound, fmt.Errorf("host not found")
	}
	if joiningSite != targetSite {
		return "", "", http.StatusConflict, fmt.Errorf("the joining host must belong to the same site as the target pool")
	}
	if host == master || joiningPool == pool {
		return "", "", http.StatusConflict, fmt.Errorf("this host already belongs to the selected pool")
	}
	if err := c.managedHost(tx, pool, master); err != nil {
		return "", "", http.StatusConflict, fmt.Errorf("pool master must be fresh and active before a host can join")
	}
	if err := c.managedHost(tx, joiningPool, host); err != nil {
		return "", "", http.StatusConflict, fmt.Errorf("the joining host must be fresh and active")
	}
	// Only the master of its own single-host pool may join; Core refuses the rest again.
	masterInventory, err := hostInventory(tx, master)
	if err != nil {
		return "", "", http.StatusInternalServerError, fmt.Errorf("inventory unavailable")
	}
	joining, err := hostInventory(tx, host)
	if err != nil {
		return "", "", http.StatusInternalServerError, fmt.Errorf("inventory unavailable")
	}
	if reason := joinCandidateRefusal(tx, host, joiningPool, joining); reason != "" {
		return "", "", http.StatusConflict, fmt.Errorf("%s", reason)
	}
	if membership := reportedMembership(masterInventory); membership != nil {
		for _, member := range membership.Members {
			if member.HostID == host && member.Status != "ejected" {
				return "", "", http.StatusConflict, fmt.Errorf("this host already belongs to the selected pool")
			}
			if member.HostID == master && member.PeerOrigin != "" {
				requestedMaster = member.PeerOrigin
			}
		}
	}
	// With neither a recorded nor a supplied origin, the empty master_url asks the master to
	// work out its own peer address.
	// A reported peer_origin is canonicalised the same way the operator's master_url is.
	// Anything else a host reports is passed through unchanged for Core to refuse.
	if normalized := normalizedHTTPSOrigin(requestedMaster); normalized != "" {
		requestedMaster = normalized
	}
	return master, requestedMaster, 0, nil
}

// relayPoolRequest holds one report round trip. Nothing it carries is written or logged.
func (c *Controller) relayPoolRequest(w http.ResponseWriter, r *http.Request, request protocol.PoolRequest) (protocol.PoolResult, bool) {
	b := c.joinBroker()
	result := make(chan protocol.PoolResult, 1)
	b.mu.Lock()
	if _, exists := b.pending[request.RequestID]; exists {
		b.mu.Unlock()
		fail(w, http.StatusConflict, "pool request is already active")
		return protocol.PoolResult{}, false
	}
	b.pending[request.RequestID] = poolJoinPending{request: request, result: result}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, request.RequestID)
		b.mu.Unlock()
	}()
	wait := poolJoinWait
	if request.Action == "bmc-probe" {
		wait = poolBMCProbeWait
	}
	select {
	case found := <-result:
		return found, true
	case <-r.Context().Done():
		// Nobody is left to read this, but writing nothing makes net/http emit a bare 200,
		// which is the one answer an abandoned join must never produce. The deferred delete
		// above releases the broker entry, so an exact retry with the same request_id is
		// accepted rather than refused as already active.
		fail(w, statusClientClosedRequest, poolRequestAbandoned(request.Action))
		return protocol.PoolResult{}, false
	case <-time.After(wait):
		write(w, http.StatusGatewayTimeout, protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "failed", Error: poolRequestDeadline(request.Action), ObservedAt: time.Now().UTC()})
		return protocol.PoolResult{}, false
	}
}

type poolRequestCopy struct {
	abandoned, deadline string
}

var poolRequestCopyByAction = map[string]poolRequestCopy{
	"promotion-preflight": {
		abandoned: "the client disconnected before the promotion candidate answered; retry",
		deadline:  "the promotion candidate did not answer the promotion preflight before the deadline; retry",
	},
	"bmc-probe": {
		abandoned: "the client disconnected before the pool master finished probing the management controller; detect again",
		deadline:  "the pool master did not finish probing the management controller before the deadline; detect again",
	},
}

func poolRequestAbandoned(action string) string {
	if copy, ok := poolRequestCopyByAction[action]; ok {
		return copy.abandoned
	}
	return "the client disconnected before the pool master answered; retry with the same request_id"
}

func poolRequestDeadline(action string) string {
	if copy, ok := poolRequestCopyByAction[action]; ok {
		return copy.deadline
	}
	return "the pool master did not return a join invitation before the deadline; retry with the same request_id"
}

// commandPoolJoin journals the one durable command. The stored replay payload is the client
// request; the invitation rides only in the command that is delivered to the joining host.
func (c *Controller) commandPoolJoin(w http.ResponseWriter, user, pool string, payload poolJoinPayload, invitation protocol.PoolJoinInvitation) {
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, payload.RequestID, "pool-join", payload) {
		return
	}
	id, opID := random(), random()
	cmd := protocol.Command{OperationID: opID, Kind: "pool-join", Pool: &protocol.PoolCommand{RequestID: payload.RequestID, Action: "join", MemberHostID: payload.HostID, MasterURL: invitation.MasterURL, PoolCAPEM: invitation.PoolCAPEM, Invitation: invitation.Invitation, PeerOrigin: payload.PeerOrigin}}
	op, err := c.saveResource(tx, user, payload.RequestID, pool, payload.HostID, payload.HostID, id, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "pool join storage failed")
		return
	}
	// The invitation is delivery data, not Controller state: drop it from the stored command
	// as soon as it has been delivered, and in any case when it expires.
	time.AfterFunc(poolJoinInvitationTTL, c.scrubPoolJoinInvitations)
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}

// poolJoinInvitationExpired is what an operator is told about a join whose invitation ran out
// before the joining host ever collected the command: the credential is gone, so the command
// cannot be delivered and the join has to be started again.
const poolJoinInvitationExpired = "the join invitation expired before the host collected it; start the join again"

// poolJoinScrubInterval is how often the health tick sweeps for invitations left at rest. It
// is a sweep, not the lifetime: the TTL above is what decides whether a row is scrubbed.
const poolJoinScrubInterval = time.Minute

// scrubPoolJoinInvitations removes the invitation and pool CA from stored pool-join commands
// whose operation has left the queue, or which are older than the invitation lifetime. It
// runs on Controller start, on every pool-join route and on the health tick, so a Controller
// that is restarted inside an invitation's lifetime and then never asked about pools again
// still cannot leave one behind. An invitation that expired before the joining host collected
// its command also ends that operation: see poolJoinInvitationExpired.
func (c *Controller) scrubPoolJoinInvitations() {
	rows, err := c.db.Query("SELECT r.operation_id,r.command,o.status,o.created_at FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='pool-join'")
	if err != nil {
		return
	}
	scrub, expired := map[string]string{}, []string{}
	for rows.Next() {
		var opID, raw, status, created string
		if rows.Scan(&opID, &raw, &status, &created) != nil {
			break
		}
		var cmd protocol.Command
		if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Pool == nil || (cmd.Pool.Invitation == "" && cmd.Pool.PoolCAPEM == "") {
			continue
		}
		// An unreadable timestamp is not a reason to keep a secret: it is scrubbed, because
		// the only thing lost is a join the operator can start again.
		at, parseErr := time.Parse(time.RFC3339Nano, created)
		if status == "queued" && parseErr == nil && time.Since(at) < poolJoinInvitationTTL {
			continue
		}
		if status == "queued" {
			// Never collected, and now uncollectable: a command with no invitation would be
			// redelivered on every report and refused by the host, so it stops being one.
			expired = append(expired, opID)
		}
		cmd.Pool.Invitation, cmd.Pool.PoolCAPEM = "", ""
		if b, marshalErr := json.Marshal(cmd); marshalErr == nil {
			scrub[opID] = string(b)
		}
	}
	rows.Close()
	for opID, command := range scrub {
		c.db.Exec("UPDATE managed_resources SET command=? WHERE operation_id=?", command, opID)
	}
	for _, opID := range expired {
		c.db.Exec("UPDATE operations SET status='failed',error=?,updated_at=? WHERE id=? AND status='queued'", poolJoinInvitationExpired, stamp(), opID)
	}
}

func reportedMembership(inv protocol.Inventory) *protocol.PoolMembership {
	if inv.Managed == nil {
		return nil
	}
	return inv.Managed.Membership
}

// joinCandidateRefusal returns Core's own precheck reasons as far as the Controller can see
// them from reported inventory, so the operator is not told to wait for a refusal.
func joinCandidateRefusal(tx *sql.Tx, host, pool string, inv protocol.Inventory) string {
	if membership := reportedMembership(inv); membership != nil && (membership.MasterHostID != host || len(membership.Members) != 1) {
		return "only the master of its own single-host pool can join another pool"
	}
	var peers int
	if tx.QueryRow("SELECT count(*) FROM hosts WHERE pool_id=? AND id<>?", pool, host).Scan(&peers) == nil && peers > 0 {
		return "only the master of its own single-host pool can join another pool"
	}
	// Managed VMs and registrations are mirrored into managed_resources, not retained in the
	// stored inventory, so the candidate's own reported journal is read from there.
	var vms int
	if tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='vm-create' AND r.host_id=? AND o.status!='failed' AND r.id NOT LIKE 'failed-vm-create:%'", host).Scan(&vms) == nil && vms > 0 {
		return "joining a pool with managed VMs is not supported; migrate or remove workloads first"
	}
	rows, err := tx.Query("SELECT r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='storage-register' AND r.host_id=? AND o.status!='failed'", host)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var cmd protocol.Command
		if rows.Scan(&raw) != nil || json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Storage == nil {
			continue
		}
		// The same predicate Core's own precheck applies, so the preview and the host cannot
		// disagree. A detached repository has no mirror row left here and no live projection
		// there, which is the same answer from both sides by construction.
		if protocol.StorageBlocksPoolJoin(*cmd.Storage) {
			return "joining a pool with existing remote storage is not supported; detach it first"
		}
	}
	return ""
}

// connection mirrors the freshness the hosts view reports, for one host.
func (c *Controller) connection(host string, seen sql.NullString, revoked bool) string {
	if revoked {
		return "revoked"
	}
	c.mu.Lock()
	at, ok := c.seen[host]
	c.mu.Unlock()
	if ok && time.Since(at) <= c.cfg.StaleAfter {
		return "fresh"
	}
	if seen.Valid {
		return "stale"
	}
	return "unknown"
}

// hostSeenAt renders a hosts.last_seen stamp as RFC3339, or "" when the host has never
// reported. The column is written with RFC3339Nano (stamp()); the projection publishes
// seconds, as every other timestamp in this view does.
func hostSeenAt(seen string) string {
	at, err := time.Parse(time.RFC3339Nano, seen)
	if err != nil {
		return ""
	}
	return maintenanceSince(at)
}

type poolMemberCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}
type poolMemberAccessCounts struct {
	Healthy int `json:"healthy"`
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
	Stale   int `json:"stale"`
}
type poolMemberFailure struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}
type poolMemberView struct {
	HostID     string                   `json:"host_id"`
	Name       string                   `json:"name"`
	Role       string                   `json:"role"`
	Status     string                   `json:"status"`
	Error      string                   `json:"error"`
	PeerOrigin string                   `json:"peer_origin"`
	JoinedAt   string                   `json:"joined_at"`
	Connection string                   `json:"connection"`
	Checks     []poolMemberCheck        `json:"checks"`
	Access     poolMemberAccessCounts   `json:"access"`
	Failures   []poolMemberFailure      `json:"failures"`
	Join       *protocol.PoolJoinStatus `json:"join"`
	// Replica is nil on the master's row: the master holds the authority, not a copy of
	// it. Every field in it is a host report, mirrored for display and stored nowhere.
	Replica *poolMemberReplica `json:"replica"`
	// Maintenance is the master's durable drain flag. It is nil, not false, whenever the
	// master is not fresh: an unreported flag is unknown, never "not drained".
	Maintenance      *bool            `json:"maintenance"`
	MaintenanceSince string           `json:"maintenance_since"`
	Guests           poolMemberGuests `json:"guests"`
	LastMaintenance  *memberOperation `json:"last_maintenance"`
	// Transfer is this host's own report of the master change or eject it is part of. It
	// is nil whenever the host is not reporting now: a stage observed an hour ago is not
	// this host's present stage.
	Transfer *protocol.PoolTransferStatus `json:"transfer"`
	// Fence is what the master's membership records about how this host would be excluded.
	// It is master-derived, so it is nil while the master is not fresh, and it never
	// carries the credential reference or anything the reference names.
	Fence *poolMemberFence `json:"fence"`
	// Restricted is this host's own Core-enforced refusal to act on pool authority. It is
	// the host's own report, so it is shown whenever that host is reporting, whatever the
	// pool master is doing.
	Restricted *protocol.PoolRestrictedStatus `json:"restricted"`
}
type poolMembersView struct {
	PoolID           string `json:"pool_id"`
	PoolName         string `json:"pool_name"`
	MasterHostID     string `json:"master_host_id"`
	MasterConnection string `json:"master_connection"`
	// MasterSeenAt is when the master host last reported to this Controller (RFC3339),
	// so a stale-master banner can say since when. It is the mirror's own record of a
	// host report, never a pool fact, and it is "" when the master has never reported.
	MasterSeenAt  string `json:"master_seen_at"`
	Generation    uint64 `json:"generation"`
	Authoritative bool   `json:"authoritative"`
	// Fence is the pool's own fencing answer as the master reported it with its
	// membership: the count, the Host fencing setting and Core's prerequisite checklist.
	// Mirrored verbatim and stored nowhere; nil when the master is not fresh or has not
	// reported its membership, which reads as "not reported" and never as ready.
	Fence *protocol.PoolFenceSummary `json:"fence"`
	// Authority is the master's own reported authority generation and digest: the only
	// absolute number in the pool, and what a member's replica generation is compared
	// against. Nil when the master is not fresh or does not report one.
	Authority *protocol.PoolAuthorityRecord `json:"authority"`
	// Transfer is the master's own report of a transfer in progress; each participant's
	// own view is on its member row.
	Transfer *protocol.PoolTransferStatus `json:"transfer"`
	// LastTransfer is the newest pool-transfer command this Controller relayed for the
	// pool, so an asynchronous Core refusal is visible where the operator acted.
	LastTransfer *poolTransferOperation `json:"last_transfer"`
	// MasterConflict names hosts that claim this pool's mastery without newer evidence
	// than the recorded master. Normally empty; never acted on, only shown.
	MasterConflict []string `json:"master_conflict"`
	// Promotion is a candidate's own reported fence-verified promotion. Like every other
	// member-owned report it survives a stale master, which is the only time it matters.
	Promotion *poolPromotionView `json:"promotion"`
	// LastPromotion is the newest pool-promote command this Controller relayed for the
	// pool, so an asynchronous Core refusal is visible where the operator acted.
	LastPromotion *poolTransferOperation `json:"last_promotion"`
	Members       []poolMemberView       `json:"members"`
}

// poolMemberReplica pairs the member's own report of what it holds with the master's report
// of what it last acknowledged. Both numbers are shown because a single word cannot say
// which side is stale. The Controller computes nothing else and persists none of it.
type poolMemberReplica struct {
	Generation             uint64 `json:"generation"`
	Digest                 string `json:"digest"`
	Verified               bool   `json:"verified"`
	ReceivedAt             string `json:"received_at"`
	AcknowledgedGeneration uint64 `json:"acknowledged_generation"`
	AcknowledgedAt         string `json:"acknowledged_at"`
	Error                  string `json:"error"`
	State                  string `json:"state"`
}

// poolConvergeChecks is the fixed order every member row reports its checks in. A check the
// host does not report is projected as "unknown", never omitted, so the column cannot
// silently lose a row. Append only: the order is part of the client contract.
var poolConvergeChecks = []string{"pool-storage-reconciliation", "pool-network-reconciliation", "pool-media-reconciliation", "pool-authority-replica", "pool-restricted", "pool-promotion", "pool-authority-transfer"}

// poolMembers projects the master's reported membership. It stores nothing: when the master
// is not fresh the response says so and every master-derived field is unknown.
func (c *Controller) poolMembers(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.scrubPoolJoinInvitations()
	pool := r.PathValue("id")
	if !nameValid(pool) {
		fail(w, http.StatusBadRequest, "pool required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	out := poolMembersView{PoolID: pool, Members: []poolMemberView{}, MasterConflict: []string{}}
	if err = tx.QueryRow("SELECT name,master_host_id FROM pools WHERE id=?", pool).Scan(&out.PoolName, &out.MasterHostID); err != nil {
		fail(w, http.StatusNotFound, "pool not found")
		return
	}
	hosts, err := c.poolHosts(tx)
	if err != nil {
		fail(w, http.StatusInternalServerError, "host lookup failed")
		return
	}
	resources, err := poolResources(tx, pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "resource lookup failed")
		return
	}
	maintenance, err := lastMaintenanceOperations(tx, pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "operation lookup failed")
		return
	}
	master := hosts[out.MasterHostID]
	out.MasterConnection = "unknown"
	if master.id != "" {
		out.MasterConnection, out.MasterSeenAt = master.connection, hostSeenAt(master.seen)
	}
	out.Authoritative = out.MasterConnection == "fresh"
	if out.Authoritative && master.inventory.Managed != nil {
		out.Authority, out.Transfer = master.inventory.Managed.Authority, master.inventory.Managed.Transfer
	}
	if out.LastTransfer, err = lastPoolTransferOperation(tx, pool); err != nil {
		fail(w, http.StatusInternalServerError, "operation lookup failed")
		return
	}
	if out.LastPromotion, err = lastPoolPromotionOperation(tx, pool); err != nil {
		fail(w, http.StatusInternalServerError, "operation lookup failed")
		return
	}
	// A promotion is reported by the candidate, not by the master: it is the master that
	// is gone. Nothing about it is recorded here.
	out.Promotion = reportedPromotion(hosts, pool)
	membership := reportedMembership(master.inventory)
	if membership == nil && master.id == "" {
		// The recorded master never enrolled here, so the pool is shown as its enrolled
		// hosts last reported it. Nothing in it is authoritative.
		if membership, err = poolRecordNaming(tx, pool, out.MasterHostID); err != nil {
			fail(w, http.StatusInternalServerError, "membership lookup failed")
			return
		}
	}
	members := []protocol.PoolMember{}
	if membership != nil {
		out.Generation, members = membership.Generation, membership.Members
		if out.Authoritative {
			// The pool's fencing answer is the master's, computed over the membership
			// it owns. Nothing here counts rows or decides a prerequisite.
			out.Fence = membership.FenceSummary
		}
	} else if master.id != "" {
		members = []protocol.PoolMember{{HostID: master.id, Status: "unknown"}}
	}
	out.MasterConflict = masterClaimants(hosts, pool, out.MasterHostID, out.Generation)
	// The master's own authority generation is the absolute reference a member's replica
	// generation is measured against. Without it the ladder falls back to comparing
	// members with each other, exactly as it did before the master reported one.
	authority := uint64(0)
	if out.Authority != nil {
		authority = out.Authority.Generation
	}
	// The highest generation any fresh member is acknowledged to hold. No host reports the
	// master's authority generation, so this is the only evidence that a member lags: a
	// peer of the same pool demonstrably holds a newer one.
	replicaPeak := uint64(0)
	if out.Authoritative {
		for _, member := range members {
			if member.HostID != out.MasterHostID && hosts[member.HostID].connection == "fresh" && member.ReplicaGeneration > replicaPeak {
				replicaPeak = member.ReplicaGeneration
			}
		}
	}
	for _, member := range members {
		host := hosts[member.HostID]
		view := poolMemberView{HostID: member.HostID, Name: member.HostID, Role: "member", Status: "unknown", Connection: "unknown", Checks: []poolMemberCheck{}, Failures: []poolMemberFailure{}}
		if member.Hostname != "" {
			view.Name = member.Hostname
		}
		if member.HostID == out.MasterHostID {
			view.Role = "master"
		}
		if host.id != "" {
			view.Name, view.Connection = host.hostname, host.connection
			if host.inventory.Managed != nil {
				view.Join = host.inventory.Managed.Join
			}
		}
		// peer_origin and the join time are part of the master's durable membership record,
		// not observations of it, and a quiet master does not make them untrue. Everything
		// observational stays unknown.
		view.PeerOrigin, view.JoinedAt = member.PeerOrigin, maintenanceSince(member.ObservedAt)
		view.Guests = memberGuests(host)
		if last, ok := maintenance[member.HostID]; ok {
			found := last
			view.LastMaintenance = &found
		}
		// A host's own reports are its own: the freshness that decides them is that host's,
		// not the master's. Choosing a promotion candidate means reading each surviving
		// member's own replica, checks and restriction while the master is gone, so these
		// are deliberately outside the authoritative gate below. Everything the master
		// observes about a member stays unknown without a fresh master.
		view.Restricted = memberRestricted(host)
		if host.connection == "fresh" {
			view.Checks = memberChecks(host.inventory)
		}
		if out.Authoritative {
			drained := member.Maintenance
			view.Maintenance, view.MaintenanceSince = &drained, maintenanceSince(member.MaintenanceSince)
			view.Status, view.Error = member.Status, member.Error
			view.Fence = memberFence(member)
			// member_access is the master's observation of its members; it holds no rows
			// about the master itself, which owns these resources.
			if view.Role == "member" {
				view.Access, view.Failures = memberAccess(resources, member.HostID, c.cfg.StaleAfter)
			}
		}
		// Defect 3: a pool VLAN's uplink is validated against the master's NICs at
		// registration. Each member's own reported NICs decide whether it can ever
		// converge, so the projection reports that per member, whatever the master says.
		// Failure rows are observations, so like every other one they are withheld while
		// the master is not fresh: the resources they are about are the master's mirror.
		if out.Authoritative {
			view.Failures = append(view.Failures, uplinkFailures(resources, host, view.Failures)...)
		}
		if view.Role == "member" {
			view.Replica = memberReplica(out.Authoritative, host, member, replicaPeak, authority)
		}
		view.Transfer = memberTransfer(host)
		out.Members = append(out.Members, view)
	}
	write(w, http.StatusOK, out)
}

func memberChecks(inv protocol.Inventory) []poolMemberCheck {
	out := []poolMemberCheck{}
	for _, name := range poolConvergeChecks {
		check := poolMemberCheck{Name: name, Status: "unknown"}
		for _, reported := range inv.Checks {
			if reported.Name != name {
				continue
			}
			check.Detail = reported.Error
			if reported.Status == "healthy" || reported.Status == "failed" {
				check.Status = reported.Status
			}
		}
		out = append(out, check)
	}
	return out
}

// memberReplica projects one member's authority replica from the two hosts that report it:
// the member says what it holds, the master says what it last saw acknowledged. Neither
// number is derived, stored or reconciled here. A master or member that is not fresh makes
// the whole row unknown — a replica that was current an hour ago is not current now.
func memberReplica(authoritative bool, host hostRow, member protocol.PoolMember, peak, authority uint64) *poolMemberReplica {
	out := poolMemberReplica{State: "unknown"}
	if host.id == "" || host.connection != "fresh" {
		return &out
	}
	// What this member says it holds is its own fresh report, and it is the evidence an
	// operator needs most when the master is gone: a promotion candidate must hold a
	// verified replica. The master's observation of the same replica is separate, and
	// stays unknown without a fresh master.
	if host.inventory.Managed != nil && host.inventory.Managed.Replica != nil {
		reported := host.inventory.Managed.Replica
		out.Generation, out.Digest, out.Verified, out.ReceivedAt, out.Error = reported.Generation, reported.Digest, reported.Verified, maintenanceSince(reported.ReceivedAt), reported.Error
	}
	if authoritative {
		out.AcknowledgedGeneration, out.AcknowledgedAt = member.ReplicaGeneration, maintenanceSince(member.ReplicaObservedAt)
		if member.ReplicaError != "" {
			out.Error = member.ReplicaError
		}
	}
	switch {
	case out.Error != "":
		out.State = "refused"
	case out.Generation == 0 && out.AcknowledgedGeneration == 0:
		out.State = "awaiting"
	case out.Generation > 0 && !out.Verified:
		out.State = "unverified"
	case !authoritative:
		// current and behind are claims about the master's number. Without a fresh master
		// there is none, and a member's own copy cannot stand in for it.
		out.State = "unknown"
	case out.Generation != out.AcknowledgedGeneration || out.AcknowledgedGeneration < peak || (authority > 0 && (out.AcknowledgedGeneration < authority || out.Generation < authority)):
		// Either the two sides disagree on one generation — in which case one of them is
		// stale and the operator needs both numbers — the master reports an authority
		// generation one of them is below, or a peer holds a newer one.
		out.State = "behind"
	default:
		out.State = "current"
	}
	return &out
}

// poolResource is one master-owned resource a member must converge, with the master's
// reported observation of this pool's members.
type poolResource struct {
	kind, id, name, uplink string
	// storage is the repository an ISO lives on; it is a member obligation only when that
	// repository is pool-shared.
	storage string
	vlan    int
	access  map[string]struct {
		observed time.Time
		err      string
	}
}

// poolResources reads the mirrored master-owned registrations of a pool. Every field comes
// from a host report; the rows disappear when the master stops reporting them.
func poolResources(tx *sql.Tx, pool string) ([]poolResource, error) {
	rows, err := tx.Query("SELECT r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.pool_id=? AND o.status='succeeded' AND r.kind IN ('storage-register','network-register','iso-import') ORDER BY r.name,r.id", pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []poolResource
	shared := map[string]bool{}
	var media []poolResource
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var cmd protocol.Command
		if json.Unmarshal([]byte(raw), &cmd) != nil {
			continue
		}
		switch {
		case cmd.Storage != nil && protocol.IsPoolSharedStorage(*cmd.Storage):
			v := newPoolResource("storage", cmd.Storage.ID, cmd.Storage.Name)
			for _, access := range cmd.Storage.MemberAccess {
				v.observe(access.HostID, access.ObservedAt, access.Error)
			}
			shared[cmd.Storage.ID] = true
			out = append(out, v)
		case cmd.Network != nil && protocol.IsPoolSharedNetwork(*cmd.Network):
			v := newPoolResource("network", cmd.Network.ID, cmd.Network.Name)
			v.uplink, v.vlan = cmd.Network.Uplink, cmd.Network.VLAN
			for _, access := range cmd.Network.MemberAccess {
				v.observe(access.HostID, access.ObservedAt, access.Error)
			}
			out = append(out, v)
		case cmd.Media != nil:
			v := newPoolResource("media", cmd.Media.ID, cmd.Media.Name)
			v.storage = cmd.Media.StorageID
			for _, access := range cmd.Media.MemberAccess {
				v.observe(access.HostID, access.ObservedAt, access.Error)
			}
			media = append(media, v)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// An ISO is a member obligation only when it lives on shared storage.
	for _, v := range media {
		if shared[v.storage] {
			out = append(out, v)
		}
	}
	return out, nil
}

func newPoolResource(kind, id, name string) poolResource {
	return poolResource{kind: kind, id: id, name: name, access: map[string]struct {
		observed time.Time
		err      string
	}{}}
}
func (p poolResource) observe(host string, at time.Time, err string) {
	p.access[host] = struct {
		observed time.Time
		err      string
	}{observed: at, err: err}
}

// memberAccess counts the master's member_access rows for one host: an absent row is a
// member that has not reconciled yet, never a healthy one.
func memberAccess(resources []poolResource, host string, staleAfter time.Duration) (poolMemberAccessCounts, []poolMemberFailure) {
	counts, failures := poolMemberAccessCounts{}, []poolMemberFailure{}
	now := time.Now()
	for _, resource := range resources {
		access, ok := resource.access[host]
		switch {
		case !ok:
			counts.Pending++
		case access.err != "":
			counts.Failed++
			failures = append(failures, poolMemberFailure{Kind: resource.kind, ID: resource.id, Name: resource.name, Error: access.err})
		case access.observed.IsZero() || access.observed.After(now) || now.Sub(access.observed) > staleAfter:
			counts.Stale++
		default:
			counts.Healthy++
		}
	}
	return counts, failures
}

// uplinkFailures reports pool VLAN uplinks the member's own inventory does not have. The
// authoritative refusal stays on Core; this only stops an operator waiting for it.
func uplinkFailures(resources []poolResource, host hostRow, reported []poolMemberFailure) []poolMemberFailure {
	out := []poolMemberFailure{}
	if host.id == "" || len(host.inventory.NICs) == 0 {
		return out
	}
	for _, resource := range resources {
		if resource.kind != "network" || resource.uplink == "" {
			continue
		}
		if slices.ContainsFunc(reported, func(v poolMemberFailure) bool { return v.Kind == "network" && v.ID == resource.id }) {
			continue
		}
		if err := uplinkObserved(host.inventory, resource.uplink); err != nil {
			out = append(out, poolMemberFailure{Kind: "network", ID: resource.id, Name: resource.name, Error: err.Error()})
		}
	}
	return out
}

type joinCandidateRow struct {
	Check   string `json:"check"`
	Subject string `json:"subject"`
	Result  string `json:"result"`
	Detail  string `json:"detail"`
}
type joinCandidate struct {
	HostID     string             `json:"host_id"`
	Name       string             `json:"name"`
	Connection string             `json:"connection"`
	Eligible   bool               `json:"eligible"`
	Rows       []joinCandidateRow `json:"rows"`
	// PeerAddress is the host part of the peer origin the host reports for itself, or ""
	// when it reports none; PeerPort is that origin's port, else Core's default. The dialog
	// shows the operator only the address and composes the origin from the two.
	PeerAddress string `json:"peer_address"`
	PeerPort    int    `json:"peer_port"`
}

// poolJoinCandidates previews what the operator would be choosing. Every row is composed
// from what the two hosts already report; Core still decides every refusal.
func (c *Controller) poolJoinCandidates(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.scrubPoolJoinInvitations()
	pool := r.PathValue("id")
	if !nameValid(pool) {
		fail(w, http.StatusBadRequest, "pool required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var site, masterID string
	if err = tx.QueryRow("SELECT site_id,master_host_id FROM pools WHERE id=?", pool).Scan(&site, &masterID); err != nil {
		fail(w, http.StatusNotFound, "pool not found")
		return
	}
	hosts, err := c.poolHosts(tx)
	if err != nil {
		fail(w, http.StatusInternalServerError, "host lookup failed")
		return
	}
	resources, err := poolResources(tx, pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "resource lookup failed")
		return
	}
	sitePools := map[string]bool{}
	poolRows, err := tx.Query("SELECT id FROM pools WHERE site_id=?", site)
	if err != nil {
		fail(w, http.StatusInternalServerError, "pool lookup failed")
		return
	}
	for poolRows.Next() {
		var id string
		if poolRows.Scan(&id) == nil {
			sitePools[id] = true
		}
	}
	poolRows.Close()
	master := hosts[masterID]
	out := []joinCandidate{}
	for _, host := range hosts {
		if host.poolID == pool || !sitePools[host.poolID] || host.revoked {
			continue
		}
		out = append(out, candidatePreview(tx, host, master, resources))
	}
	slices.SortFunc(out, func(a, b joinCandidate) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return strings.Compare(a.HostID, b.HostID)
	})
	write(w, http.StatusOK, map[string]any{"candidates": out})
}

func candidatePreview(tx *sql.Tx, host, master hostRow, resources []poolResource) joinCandidate {
	v := joinCandidate{HostID: host.id, Name: host.hostname, Connection: host.connection, Eligible: true, PeerPort: protocol.DefaultPoolPeerPort}
	if host.inventory.Managed != nil {
		if u, err := url.Parse(normalizedHTTPSOrigin(host.inventory.Managed.PeerOrigin)); err == nil && u.Host != "" {
			v.PeerAddress = u.Hostname()
			if port, err := strconv.Atoi(u.Port()); err == nil {
				v.PeerPort = port
			}
		}
	}
	add := func(check, subject, result, detail string) {
		v.Rows = append(v.Rows, joinCandidateRow{Check: check, Subject: subject, Result: result, Detail: detail})
	}
	if host.connection == "fresh" {
		add("connection", host.hostname, "compatible", "reporting now")
	} else {
		add("connection", host.hostname, "refuses_join", "this host is "+host.connection+"; it cannot be given a join command")
	}
	if host.inventory.AgentVersion == master.inventory.AgentVersion {
		add("version", host.inventory.AgentVersion, "compatible", "same Nex Core version as the pool master")
	} else {
		add("version", host.inventory.AgentVersion, "will_not_converge", "the pool master reports "+master.inventory.AgentVersion)
	}
	if reason := joinCandidateRefusal(tx, host.id, host.poolID, host.inventory); reason != "" {
		check := "membership"
		switch {
		case strings.Contains(reason, "managed VMs"):
			check = "vms"
		case strings.Contains(reason, "remote storage"):
			check = "storage"
		}
		add(check, host.hostname, "refuses_join", reason)
	} else {
		add("membership", host.hostname, "compatible", "master of its own single-host pool")
	}
	for _, resource := range resources {
		if resource.kind != "network" || resource.uplink == "" {
			continue
		}
		subject := fmt.Sprintf("VLAN %d on %s", resource.vlan, resource.uplink)
		if err := uplinkObserved(host.inventory, resource.uplink); err != nil {
			add("uplink", subject, "will_not_converge", err.Error())
		} else {
			add("uplink", subject, "compatible", resource.name)
		}
	}
	if baseline := master.inventory.CPUBaseline; baseline != nil && baseline.Model != "" {
		if slices.Contains(host.inventory.CPUModels, baseline.Model) {
			add("cpu", baseline.Model, "compatible", "the pool CPU baseline is available on this host")
		} else {
			add("cpu", baseline.Model, "will_not_converge", "this host does not report the pool CPU baseline model")
		}
	}
	for _, row := range v.Rows {
		if row.Result == "refuses_join" {
			v.Eligible = false
		}
	}
	return v
}
