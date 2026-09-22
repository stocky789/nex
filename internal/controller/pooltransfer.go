// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"nex.local/nex/internal/protocol"
)

// A planned master change and a member eject are one durable command to the current pool
// master and nothing else. Core runs the whole transfer by itself, so this Controller
// holds no stage, no timer and no generation: a Controller restarted, replaced or
// disconnected mid-transfer keeps nothing to recover and projects the transfer from the
// participants' next reports. The refusals below are only what the mirror already shows
// Core would refuse; Core refuses again, and it is the authority.

// poolAuthorityFrozen is Core's refusal while another master change holds the freeze. It
// is the one transfer refusal an operator retries unchanged, so it is marked retryable.
const poolAuthorityFrozen = "pool authority is frozen for a master change; retry when it completes"

// poolTransferConflicts are Core's refusals that arrive as a failed operation rather than
// an HTTP status, because the Controller could not see them when the request was made.
var poolTransferConflicts = []string{
	poolAuthorityFrozen,
	"pool authority was handed to another host; this host is no longer the pool master",
	"pool membership changed since this request was composed; review the pool and try again",
	"pool membership is unavailable",
	"another pool authority transfer is already in progress on this pool",
	"only the pool master starts a master change or an eject",
	"no such member of this pool",
	"only a ready member with a peer endpoint can become the pool master",
	"no pool authority transfer is in progress on this pool",
	"the pool authority transfer in progress has another target",
	"the target has already activated this pool authority; the master change can only go forward",
	"this pool promotion has powered the old pool master off; it cannot be cancelled and can only go forward",
}

// poolEjectForwardOnly is Core's refusal of a cancel whose eject is already durable. It
// names the removed host, so it is recognised by its tail rather than in full. It belongs
// with the conflicts above: a synchronous route would have answered 409, and like every
// other one it is not retryable — the eject completes, it does not come back.
const poolEjectForwardOnly = "; the eject can only go forward"

type poolTransferAPIRequest struct {
	RequestID       string `json:"request_id"`
	HostID          string `json:"host_id"`
	ExpectedVersion uint64 `json:"expected_version,omitempty"`
	Confirmation    string `json:"confirmation,omitempty"`
	// FenceConfirmed and FenceHostIDs belong to the cancel route alone, and are optional:
	// they are the operator's explicit agreement to power the transfer's target off, which
	// Core needs when a handed-off target stops answering and the pool's authority would
	// otherwise stay with a dead host. Core uses them only in that case and refuses them
	// while the target is reachable. The two are confirmed together or not at all.
	FenceConfirmed bool     `json:"fence_confirmed,omitempty"`
	FenceHostIDs   []string `json:"fence_host_ids,omitempty"`
}

// poolMasterChangeAPIRequest is the master-change body. It repeats the four fields that
// route accepts rather than reusing the carrier above, so the shared decoder still refuses
// a fence confirmation aimed at the wrong route with a 400.
type poolMasterChangeAPIRequest struct {
	RequestID       string `json:"request_id"`
	HostID          string `json:"host_id"`
	ExpectedVersion uint64 `json:"expected_version,omitempty"`
	Confirmation    string `json:"confirmation,omitempty"`
}

// poolEjectAPIRequest and poolCancelAPIRequest carry only the fields their route accepts:
// the ejected host is the path, and a cancel takes no typed confirmation. The shared
// decoder refuses unknown fields, so a body aimed at the wrong route is a 400, not a
// silently ignored value.
type poolEjectAPIRequest struct {
	RequestID       string `json:"request_id"`
	ExpectedVersion uint64 `json:"expected_version,omitempty"`
	Confirmation    string `json:"confirmation,omitempty"`
}
type poolCancelAPIRequest struct {
	RequestID       string   `json:"request_id"`
	HostID          string   `json:"host_id"`
	ExpectedVersion uint64   `json:"expected_version,omitempty"`
	FenceConfirmed  bool     `json:"fence_confirmed,omitempty"`
	FenceHostIDs    []string `json:"fence_host_ids,omitempty"`
}

// poolCancelFenceRequired is the cancel route's own refusal: the acknowledgement and the
// host it is about are one statement, so half of it is not a confirmation of anything.
const poolCancelFenceRequired = "powering the transfer target off is confirmed with both fence_confirmed and fence_host_ids naming that host once, or with neither"

// poolTransferPayload is the replay record: the client request plus the pool and action it
// addressed, so the same request_id aimed at another pool, host or action is a changed
// payload rather than a replay.
type poolTransferPayload struct {
	PoolID string `json:"pool_id"`
	Action string `json:"action"`
	poolTransferAPIRequest
}

// changePoolMaster implements POST /api/v1/pools/{id}/master.
func (c *Controller) changePoolMaster(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body poolMasterChangeAPIRequest
	if !decode(w, r, &body) {
		return
	}
	c.submitPoolTransfer(w, user, r.PathValue("id"), "master-change", poolTransferAPIRequest{RequestID: body.RequestID, HostID: body.HostID, ExpectedVersion: body.ExpectedVersion, Confirmation: body.Confirmation})
}

// ejectPoolMember implements DELETE /api/v1/pools/{id}/members/{host_id}.
func (c *Controller) ejectPoolMember(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body poolEjectAPIRequest
	if !decode(w, r, &body) {
		return
	}
	c.submitPoolTransfer(w, user, r.PathValue("id"), "eject", poolTransferAPIRequest{RequestID: body.RequestID, HostID: r.PathValue("host_id"), ExpectedVersion: body.ExpectedVersion, Confirmation: body.Confirmation})
}

// cancelPoolMasterChange implements POST /api/v1/pools/{id}/master/cancel. host_id is the
// transfer's target: Core refuses a cancel that names a different one, and after the
// hand-off it refuses the cancel itself, because a master change can only go forward.
func (c *Controller) cancelPoolMasterChange(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body poolCancelAPIRequest
	if !decode(w, r, &body) {
		return
	}
	request := poolTransferAPIRequest{RequestID: body.RequestID, HostID: body.HostID, ExpectedVersion: body.ExpectedVersion, FenceConfirmed: body.FenceConfirmed}
	if body.FenceConfirmed != (len(body.FenceHostIDs) != 0) {
		fail(w, http.StatusBadRequest, poolCancelFenceRequired)
		return
	}
	if body.FenceConfirmed {
		fenced, err := normalisedFenceHostIDs(body.FenceHostIDs, poolCancelFenceRequired)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		request.FenceHostIDs = fenced
	}
	c.submitPoolTransfer(w, user, r.PathValue("id"), "transfer-cancel", request)
}

// submitPoolTransfer journals exactly one pool-transfer command, addressed to the current
// master. Every route above ends here, so the replay, refusal and audit behavior cannot
// differ between them.
func (c *Controller) submitPoolTransfer(w http.ResponseWriter, user, pool, action string, body poolTransferAPIRequest) {
	if !nameValid(pool) || !nameValid(body.RequestID) || !nameValid(body.HostID) {
		fail(w, http.StatusBadRequest, poolTransferRequired(action))
		return
	}
	payload := poolTransferPayload{PoolID: pool, Action: action, poolTransferAPIRequest: body}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "pool-transfer", payload) {
		return
	}
	master, status, err := c.poolTransferTarget(tx, pool, body.HostID, action, body.ExpectedVersion, body.Confirmation)
	if err != nil {
		fail(w, status, err.Error())
		return
	}
	if status, fenceErr := mirroredFenceHostsOfPool(tx, pool, body.FenceHostIDs); fenceErr != nil {
		fail(w, status, fenceErr.Error())
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "pool-transfer", Pool: &protocol.PoolCommand{
		RequestID:       body.RequestID,
		Action:          action,
		Membership:      protocol.PoolMembership{MasterHostID: master},
		MemberHostID:    body.HostID,
		ExpectedVersion: body.ExpectedVersion,
		FenceConfirmed:  body.FenceConfirmed,
		FenceHostIDs:    body.FenceHostIDs,
	}}
	op, err := c.saveResource(tx, user, body.RequestID, pool, master, body.HostID, "pool-transfer:"+body.HostID+":"+opID, cmd, payload)
	if err != nil || c.audit(tx, user, "pool."+action, pool) != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "pool transfer storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}

type poolTransferRoute struct {
	required, purpose string
}

var poolTransferRoutes = map[string]poolTransferRoute{
	"eject":           {required: "pool, host and a stable request_id are required", purpose: "eject a member"},
	"transfer-cancel": {required: "pool, stable request_id and the transfer target host_id are required", purpose: "cancel a master change"},
	"master-change":   {required: "pool, stable request_id and the new master host_id are required", purpose: "change the pool master"},
}

func poolTransferRequired(action string) string {
	if route, ok := poolTransferRoutes[action]; ok {
		return route.required
	}
	return poolTransferRoutes["master-change"].required
}

func poolTransferPurpose(action string) string {
	if route, ok := poolTransferRoutes[action]; ok {
		return route.purpose
	}
	return poolTransferRoutes["master-change"].purpose
}

// poolTransferTarget returns the master the command must be addressed to, or the refusal
// the mirror already justifies with the status it must be reported with.
func (c *Controller) poolTransferTarget(tx *sql.Tx, pool, host, action string, expected uint64, confirmation string) (string, int, error) {
	var poolName, hostname string
	if status, err := hostKnown(tx, host); err != nil {
		return "", status, err
	}
	master, status, err := c.freshPoolMaster(tx, pool, poolTransferPurpose(action))
	if err != nil {
		return "", status, err
	}
	if err = tx.QueryRow("SELECT name FROM pools WHERE id=?", pool).Scan(&poolName); err != nil {
		return "", http.StatusNotFound, fmt.Errorf("pool not found")
	}
	if err = tx.QueryRow("SELECT hostname FROM hosts WHERE id=?", host).Scan(&hostname); err != nil {
		return "", http.StatusNotFound, fmt.Errorf("host not found")
	}
	// A master change freezes every pool authority write until it completes and an eject
	// removes a host from the pool, so both are typed by the operator: the pool's name for
	// the change, the ejected host's name for the eject.
	switch action {
	case "master-change":
		if !confirmedName(confirmation, poolName) {
			return "", http.StatusBadRequest, fmt.Errorf("the confirmation must be the pool name %q", poolName)
		}
	case "eject":
		if !confirmedName(confirmation, hostname) {
			return "", http.StatusBadRequest, fmt.Errorf("the confirmation must be the host name %q", hostname)
		}
	}
	inv, err := hostInventory(tx, master)
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("inventory unavailable")
	}
	membership := reportedMembership(inv)
	if missing, reason := c.poolTransferCapability(tx, pool, master, host, action, membership); missing != "" {
		return "", http.StatusConflict, fmt.Errorf("%s", reason)
	}
	if membership == nil {
		return "", http.StatusConflict, fmt.Errorf("the pool master has not reported its membership; %s", poolTransferUnreported(action))
	}
	member := maintenanceMember(membership, host)
	switch action {
	case "master-change":
		if host == master {
			return "", http.StatusConflict, fmt.Errorf("this host is already the pool master")
		}
		if member == nil {
			return "", http.StatusConflict, fmt.Errorf("no such member of this pool")
		}
		if member.Status != "ready" || member.PeerOrigin == "" {
			return "", http.StatusConflict, fmt.Errorf("only a ready member with a peer endpoint can become the pool master")
		}
		if err = c.managedHost(tx, pool, host); err != nil {
			return "", http.StatusConflict, fmt.Errorf("the selected host must be fresh and active to become the pool master")
		}
	case "eject":
		if host == master {
			return "", http.StatusConflict, fmt.Errorf("the pool master cannot be ejected; change the pool master first")
		}
		// The ejected host may be unreachable: Core ejects it and the host learns of it
		// when it returns, so neither its freshness nor its capability list is gated here.
		if member == nil {
			return "", http.StatusConflict, fmt.Errorf("no such member of this pool")
		}
	}
	if expected != 0 && expected != membership.Generation {
		return "", http.StatusConflict, fmt.Errorf("the pool master reports membership generation %d; reload the pool and retry", membership.Generation)
	}
	// A cancel is what ends a transfer in progress, so only the two that start one are
	// refused for it.
	if action != "transfer-cancel" && inv.Managed != nil && inv.Managed.Transfer != nil && inv.Managed.Transfer.Status == "pending" {
		return "", http.StatusConflict, fmt.Errorf("another pool authority transfer is already in progress on this pool")
	}
	return master, 0, nil
}

// confirmedName compares a typed confirmation with the name the operator was shown.
func confirmedName(confirmation, name string) bool {
	return name != "" && strings.EqualFold(strings.TrimSpace(confirmation), strings.TrimSpace(name))
}

func poolTransferUnreported(action string) string {
	switch action {
	case "eject":
		return "a member cannot be ejected yet"
	case "transfer-cancel":
		return "a master change cannot be cancelled yet"
	}
	return "a master change cannot be started yet"
}

// poolTransferCapability applies Core's version gate: the master, the target of a master
// change and every ready member must support the transfer, because a member that cannot
// re-point would keep following a host that is no longer the pool master. A member that is
// not fresh is not gated on: its capability list is a memory like anything else, and Core
// checks every participant again.
func (c *Controller) poolTransferCapability(tx *sql.Tx, pool, master, host, action string, membership *protocol.PoolMembership) (string, string) {
	missing := ""
	switch {
	case !hostSupportsCapability(tx, master, protocol.PoolAuthorityTransferCapability):
		missing = master
	case action == "master-change" && host != master && !hostSupportsCapability(tx, host, protocol.PoolAuthorityTransferCapability):
		missing = host
	case action != "transfer-cancel" && membership != nil:
		for _, member := range membership.Members {
			if member.Status != "ready" || member.HostID == master {
				continue
			}
			if !hostSupportsCapability(tx, member.HostID, protocol.PoolAuthorityTransferCapability) && c.managedHost(tx, pool, member.HostID) == nil {
				missing = member.HostID
				break
			}
		}
	}
	if missing == "" {
		return "", ""
	}
	if action == "eject" {
		return missing, fmt.Sprintf("ejecting a member needs %s on the pool master and every ready member; upgrade %s", protocol.PoolAuthorityTransferCapability, missing)
	}
	if action == "transfer-cancel" {
		return missing, fmt.Sprintf("cancelling a master change needs %s on the pool master; upgrade %s", protocol.PoolAuthorityTransferCapability, missing)
	}
	return missing, fmt.Sprintf("changing the pool master needs %s on the pool master, the selected host and every ready member; upgrade %s", protocol.PoolAuthorityTransferCapability, missing)
}

// poolTransferOperation is the newest pool-transfer command this Controller holds for a
// pool, so an asynchronous Core refusal is visible where the operator acted. Conflict
// marks refusals a synchronous route would have answered with 409; Retryable marks the one
// sentence an operator retries unchanged once the current transfer completes.
type poolTransferOperation struct {
	OperationID string `json:"operation_id"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	// Reason carries Core's sentence when the operation succeeded. A fence-backed cancel
	// is the case that needs it: Core reports the cancellation it performed as succeeded
	// and says in the same breath what it did, and that sentence is an outcome an operator
	// must read, not a failure. Error stays empty then, so no caller can show a successful
	// cancellation as a failed transfer.
	Reason    string `json:"reason"`
	Conflict  bool   `json:"conflict"`
	Retryable bool   `json:"retryable"`
}

// masterClaimants names hosts whose reported membership contradicts the recorded master
// without being evidence this Controller may adopt. The adoption rule (adoptReportedMaster)
// draws the line: a strictly higher generation reported by the very host that membership
// names as master IS the evidence, and is followed rather than shown as a conflict. What is
// left is exactly two cases, and only these are listed:
//
//   - an equal-generation rival: a host whose own membership names itself master at exactly
//     the generation mirrored for the recorded master. Neither side is newer, so the mirror
//     keeps its row and the conflict is shown instead of flapping.
//   - a newer membership naming a third host: a host reporting a strictly higher generation
//     for a master that is neither itself nor the recorded one. Adoption follows only the
//     named master's own report, so this cannot be adopted from here and the mirror is
//     knowingly behind.
//
// Everything else — a lower generation, a stale copy of the recorded master's own
// membership, a self-claim the adoption rule already followed — is not a conflict.
func masterClaimants(hosts map[string]hostRow, pool, master string, generation uint64) []string {
	out := []string{}
	for id, host := range hosts {
		if id == master {
			continue
		}
		claim := reportedMembership(host.inventory)
		if claim == nil || claim.Valid() != nil || claim.PoolID != pool {
			continue
		}
		rival := claim.MasterHostID == id && claim.Generation == generation
		thirdHost := claim.MasterHostID != id && claim.MasterHostID != master && claim.Generation > generation
		if !rival && !thirdHost {
			continue
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// memberTransfer is one participant's own report of the transfer it is part of. A host
// that is not reporting now says nothing: its last stage is not present truth.
func memberTransfer(host hostRow) *protocol.PoolTransferStatus {
	if host.id == "" || host.connection != "fresh" || host.inventory.Managed == nil {
		return nil
	}
	return host.inventory.Managed.Transfer
}

// adoptReportedMaster follows the hosts when they report a pool master this Controller
// does not have. The Controller owns no pool authority: it mirrors what the pool master
// reports, so after a master change its row, and the mirror rows that only the recorded
// master may refresh, must move to the host the pool now names.
//
// Evidence, matching Core's own rule: the report comes from the host its membership names
// as master, and the recorded master's own mirrored membership either is older (a strictly
// lower generation, which is a master change) or already names the claimant as the pool
// master (a reconnect, where the two agree at one generation). Two hosts claiming mastery at
// the same generation change nothing, and with no mirrored membership at all there is no
// evidence: the claim is refused, because a member's report is a payload it fully controls.
//
// A pool whose recorded master has never reported a membership is not a dead end. It is what
// a replacement Controller has when a member reconnects first: the member creates the pool
// row with itself recorded as master and then reports the membership naming the real master.
// That report disclaims the mastery this Controller assumed, so the row follows it at once,
// even to a master that has not enrolled here — it may be the host that is gone. Until that
// master enrolls, the evidence for it is the pool's own record as its enrolled hosts reported
// it (poolRecordNaming).
func (c *Controller) adoptReportedMaster(tx *sql.Tx, host string, inventory *protocol.Inventory) error {
	if inventory == nil || inventory.Managed == nil || inventory.Managed.Membership == nil {
		return nil
	}
	claim := inventory.Managed.Membership
	if claim.Valid() != nil {
		return nil
	}
	var site, current, master string
	err := tx.QueryRow("SELECT p.site_id,h.pool_id,p.master_host_id FROM hosts h JOIN pools p ON p.id=h.pool_id WHERE h.id=?", host).Scan(&site, &current, &master)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if claim.MasterHostID != host {
		// Only the recorded master can give the row away, and only within its own pool.
		if claim.PoolID != current || master != host {
			return nil
		}
		return c.moveRecordedMaster(tx, host, current, claim.MasterHostID)
	}
	if claim.PoolID != current {
		return c.adoptReinitializedPool(tx, host, site, master, claim)
	}
	if master == host {
		return nil
	}
	var mirrored *protocol.PoolMembership
	inv, invErr := hostInventory(tx, master)
	switch {
	case invErr == sql.ErrNoRows:
		if mirrored, invErr = poolRecordNaming(tx, current, master); invErr != nil {
			return invErr
		}
	case invErr != nil:
		return invErr
	default:
		mirrored = reportedMembership(inv)
	}
	if mirrored == nil || mirrored.PoolID != current {
		return nil
	}
	if claim.Generation <= mirrored.Generation && mirrored.MasterHostID != host {
		return nil
	}
	return c.moveRecordedMaster(tx, host, current, host)
}

// moveRecordedMaster records the master the pool names and, when that host is enrolled here,
// moves the pool's mirror rows to it. A master that is not enrolled cannot hold them; they
// move when it enrolls or when the pool names an enrolled master. The name comes out of a
// report its reporter fully controls, so a host enrolled in another pool is refused: it
// would take this pool's row and mirrors to a host that never refreshes them.
func (c *Controller) moveRecordedMaster(tx *sql.Tx, reporter, pool, master string) error {
	var enrolled string
	err := tx.QueryRow("SELECT pool_id FROM hosts WHERE id=?", master).Scan(&enrolled)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && enrolled != pool {
		return nil
	}
	if _, err = tx.Exec("UPDATE pools SET master_host_id=? WHERE id=?", master, pool); err != nil {
		return err
	}
	if enrolled == pool {
		if err = rekeyPoolMirrors(tx, pool, master); err != nil {
			return err
		}
	}
	return c.audit(tx, "host:"+reporter, "pool.master.adopt", pool)
}

// poolRecordNaming is the pool's record naming master, as the pool's enrolled hosts reported
// it. It stands in for the mirrored membership of a recorded master that has never enrolled
// with this Controller: a replacement Controller whose old master is gone. Each report is a
// payload its member controls, so the record is the conservative combination of them: the
// highest generation any of them claims, which a promotion must exceed, and only the hosts
// every one of them lists with the same status, so no single member can admit a host of its
// choosing.
func poolRecordNaming(tx *sql.Tx, pool, master string) (*protocol.PoolMembership, error) {
	rows, err := tx.Query("SELECT inventory FROM hosts WHERE pool_id=? AND revoked=0 AND inventory IS NOT NULL", pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*protocol.PoolMembership
	for rows.Next() {
		var raw string
		var inv protocol.Inventory
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &inv) != nil {
			continue
		}
		if m := reportedMembership(inv); m != nil && m.Valid() == nil && m.PoolID == pool && m.MasterHostID == master {
			records = append(records, m)
		}
	}
	if err = rows.Err(); err != nil || len(records) == 0 {
		return nil, err
	}
	newest := records[0]
	for _, record := range records {
		if record.Generation > newest.Generation {
			newest = record
		}
	}
	combined := *newest
	combined.Members = nil
	for _, member := range newest.Members {
		agreed := true
		for _, record := range records {
			if other := maintenanceMember(record, member.HostID); other == nil || other.Status != member.Status {
				agreed = false
			}
		}
		if agreed {
			combined.Members = append(combined.Members, member)
		}
	}
	return &combined, nil
}

// rekeyPoolMirrors points the pool's shared storage, network and media mirror rows at the
// recorded master, because those rows are refreshed and tombstoned only by the recorded master
// and would otherwise go stale on a host that does not own them. It moves no content: every
// field is still replaced by the master's next report.
func rekeyPoolMirrors(tx *sql.Tx, pool, to string) error {
	resources, err := poolResources(tx, pool)
	if err != nil {
		return err
	}
	kinds := map[string]string{"storage": "storage-register", "network": "network-register", "media": "iso-import"}
	for _, resource := range resources {
		if _, err = tx.Exec("UPDATE managed_resources SET host_id=? WHERE id=? AND kind=? AND pool_id=?", to, resource.id, kinds[resource.kind], pool); err != nil {
			return err
		}
	}
	return nil
}

// adoptReinitializedPool adopts the new single-host pool an ejected host re-initializes
// itself into. The host keeps its identity and its enrollment; only the pool it names is
// new, so the Controller records it under the same site the way enrollment records a new
// host's pool, with no operator action.
//
// Core re-initializes a host only after its pool ejected it, and the eject is the master's
// record, not the leaving host's. So the same evidence rule applies here: while the pool it
// is leaving still has another host recorded as master, that master's own mirrored
// membership must no longer hold this host as a member. Until it says so the Controller
// keeps the host where the pool says it is, and the next report adopts it once the master
// has reported the eject.
func (c *Controller) adoptReinitializedPool(tx *sql.Tx, host, site, master string, claim *protocol.PoolMembership) error {
	if len(claim.Members) != 1 || claim.Members[0].HostID != host {
		return nil
	}
	if master != host {
		inv, err := hostInventory(tx, master)
		if err != nil {
			return err
		}
		mirrored := reportedMembership(inv)
		if mirrored == nil || maintenanceMember(mirrored, host) != nil {
			return nil
		}
	}
	var existing int
	if err := tx.QueryRow("SELECT count(*) FROM pools WHERE id=?", claim.PoolID).Scan(&existing); err != nil || existing != 0 {
		return err
	}
	var hostname string
	if err := tx.QueryRow("SELECT hostname FROM hosts WHERE id=?", host).Scan(&hostname); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO pools(id,site_id,name,master_host_id) VALUES(?,?,?,?)", claim.PoolID, site, hostname, host); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE hosts SET pool_id=? WHERE id=?", claim.PoolID, host); err != nil {
		return err
	}
	return c.audit(tx, "host:"+host, "pool.create", claim.PoolID)
}
