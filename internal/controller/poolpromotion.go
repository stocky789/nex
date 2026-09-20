// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"

	"nex.local/nex/internal/protocol"
)

// A promotion is what an operator does when the pool master is gone, so nothing here is
// addressed to the master and nothing here waits for it: the one durable command goes to
// the candidate, which proves replica integrity, replica supremacy, accepted ownership and
// a fence proof it produced itself before it moves anything. The Controller's view of the
// old master is never evidence in either direction — it neither refuses a promotion because
// the old master looks reachable nor allows one because it looks gone. Core probes the old
// master over the pool network and refuses when it answers.
//
// Owner decision OD-5: an opted-in pool whose master restarts while a member is permanently
// dead stays restricted. There is no escape action here, and none may be added: no force, no
// override, no "declare host lost", no timeout-based path.

// fenceProfileCredentialPlaceholder stands in for the Core-allocated reference while the
// submitted profile is validated, so a profile whose credential is still in flight is
// checked by exactly the same rule as one that names a reference already.
const fenceProfileCredentialPlaceholder = "credential"

type poolPromoteAPIRequest struct {
	RequestID string `json:"request_id"`
	HostID    string `json:"host_id"`
	// Confirmation is the pool's name, typed: a promotion powers the old pool master off.
	Confirmation string `json:"confirmation,omitempty"`
	// FenceConfirmed must be literally true. It is the operator's separate, explicit
	// acknowledgement of that power-off and a precondition only: Core produces and
	// verifies the fence proof itself, and an acknowledgement is never evidence.
	FenceConfirmed bool `json:"fence_confirmed,omitempty"`
	// FenceHostIDs is the set of hosts the operator agreed to power off, which is exactly
	// the set Core's own preflight named in verdict.fence_host_ids. The Controller never
	// computes, completes or corrects it: it carries the operator's confirmation to the
	// candidate verbatim, and Core re-computes the set when the promotion runs and refuses
	// a promotion whose confirmation no longer matches. It is a set, so it is sorted here
	// before it is recorded or sent and a confirmation typed in another order replays.
	FenceHostIDs []string `json:"fence_host_ids"`
}

// poolPromotePayload is the replay record: the client request plus the pool it addressed.
type poolPromotePayload struct {
	PoolID string `json:"pool_id"`
	poolPromoteAPIRequest
}

type poolPreflightAPIRequest struct {
	HostID string `json:"host_id"`
}

type poolFenceProfileAPIRequest struct {
	RequestID string `json:"request_id"`
	// Fence is the profile to record; nil clears it.
	Fence *protocol.StorageClusterFence `json:"fence"`
	// Credential never reaches the Controller database, the audit trail, the response or a
	// log: it is handed to Core over the existing one-time encrypted credential offer, and
	// only the opaque reference Core allocates is written into the command.
	Credential *protocol.StorageCredentials `json:"credential,omitempty"`
}

// poolFenceProfilePayload is the replay record. It deliberately carries the profile exactly
// as the client sent it — without the credential and without the Core-allocated reference —
// so an exact retry of the same request_id is byte-identical.
type poolFenceProfilePayload struct {
	PoolID    string                        `json:"pool_id"`
	HostID    string                        `json:"host_id"`
	RequestID string                        `json:"request_id"`
	Fence     *protocol.StorageClusterFence `json:"fence"`
}

// promotionPreflight implements POST /api/v1/pools/{id}/master/promote/preflight. It is one
// brokered report round trip to the candidate and stores nothing at all, so it is safe to
// repeat. The pool master is deliberately not required to be fresh: a preflight is exactly
// what an operator runs when it is gone.
func (c *Controller) promotionPreflight(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body poolPreflightAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(pool) || !nameValid(body.HostID) {
		fail(w, http.StatusBadRequest, "pool and the promotion candidate host_id are required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if status, candidateErr := c.poolPromotionCandidate(tx, pool, body.HostID, false); candidateErr != nil {
		fail(w, status, candidateErr.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	result, ok := c.relayPoolRequest(w, r, protocol.PoolRequest{RequestID: random(), PoolID: pool, HostID: body.HostID, Action: "promotion-preflight"})
	if !ok {
		return
	}
	if result.Status != "succeeded" || result.Promotion == nil {
		refusal := result.Error
		if refusal == "" {
			refusal = "the promotion candidate could not compute a promotion verdict"
		}
		fail(w, http.StatusConflict, refusal)
		return
	}
	write(w, http.StatusOK, map[string]any{"host_id": body.HostID, "promotion": result.Promotion})
}

// promotePoolMaster implements POST /api/v1/pools/{id}/master/promote: one durable
// pool-promote command, addressed to the candidate.
func (c *Controller) promotePoolMaster(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body poolPromoteAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(pool) || !nameValid(body.RequestID) || !nameValid(body.HostID) {
		fail(w, http.StatusBadRequest, "pool, stable request_id and the promotion candidate host_id are required")
		return
	}
	fenced, err := normalisedFenceHostIDs(body.FenceHostIDs, poolPromoteFenceHostsRequired)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	body.FenceHostIDs = fenced
	payload := poolPromotePayload{PoolID: pool, poolPromoteAPIRequest: body}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "pool-promote", payload) {
		return
	}
	var poolName string
	if err = tx.QueryRow("SELECT name FROM pools WHERE id=?", pool).Scan(&poolName); err != nil {
		fail(w, http.StatusNotFound, "pool not found")
		return
	}
	if !confirmedName(body.Confirmation, poolName) {
		fail(w, http.StatusBadRequest, fmt.Sprintf("the confirmation must be the pool name %q", poolName))
		return
	}
	// Core's own sentence: the destructive acknowledgement is separate from the typed name
	// because it is a different statement, about a host that is about to be powered off.
	if !body.FenceConfirmed {
		fail(w, http.StatusBadRequest, "promoting this host powers the old pool master off; confirm that before the promotion can run")
		return
	}
	if status, candidateErr := c.poolPromotionCandidate(tx, pool, body.HostID, true); candidateErr != nil {
		fail(w, status, candidateErr.Error())
		return
	}
	if status, fenceErr := mirroredFenceHostsOfPool(tx, pool, fenced); fenceErr != nil {
		fail(w, status, fenceErr.Error())
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "pool-promote", Pool: &protocol.PoolCommand{
		RequestID:      body.RequestID,
		Action:         "promote",
		MemberHostID:   body.HostID,
		FenceConfirmed: true,
		FenceHostIDs:   fenced,
	}}
	op, err := c.saveResource(tx, user, body.RequestID, pool, body.HostID, body.HostID, "pool-promote:"+body.HostID+":"+opID, cmd, payload)
	if err != nil || c.audit(tx, user, "pool.promote", pool) != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "pool promotion storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}

// poolPromoteFenceHostsRequired points the operator back at the one place the answer comes
// from. The set is Core's, not this Controller's, so a missing or malformed confirmation is
// never repaired here: it is refused, and the preflight is run again.
const poolPromoteFenceHostsRequired = "this promotion powers hosts off; confirm in fence_host_ids exactly the hosts the promotion preflight named in verdict.fence_host_ids, each listed once"

// normalisedFenceHostIDs sorts the operator's confirmed set and refuses an empty, blank or
// duplicated one. Sorting is the whole normalisation: the list is a set, so the replay
// record and the command are both built from the sorted copy and the same confirmation
// typed in another order is the same request rather than a second promotion.
func normalisedFenceHostIDs(hosts []string, refusal string) ([]string, error) {
	out := append([]string(nil), hosts...)
	slices.Sort(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("%s", refusal)
	}
	for i, id := range out {
		if !nameValid(id) || i > 0 && id == out[i-1] {
			return nil, fmt.Errorf("%s", refusal)
		}
	}
	return out, nil
}

// mirroredFenceHostsOfPool refuses a confirmed host this Controller mirrors in another
// pool, which is the one thing the mirror can honestly contradict. A host it has never
// heard of is accepted: the mirror is not the pool's record, a partitioned host may be
// known only to the pool, and Core re-computes the whole set before it powers anything off.
func mirroredFenceHostsOfPool(tx *sql.Tx, pool string, hosts []string) (int, error) {
	for _, id := range hosts {
		var known string
		err := tx.QueryRow("SELECT pool_id FROM hosts WHERE id=?", id).Scan(&known)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return http.StatusInternalServerError, fmt.Errorf("host lookup failed")
		}
		if known != pool {
			return http.StatusBadRequest, fmt.Errorf("host %s is not a host of this pool; confirm only the hosts this pool reports", id)
		}
	}
	return 0, nil
}

// poolPromotionCandidate refuses only what the candidate's own reports already make certain.
// Everything about the old pool master is left to Core, including whether it answers: the
// Controller's reachability of a host is not evidence about the pool network.
func (c *Controller) poolPromotionCandidate(tx *sql.Tx, pool, host string, promoting bool) (int, error) {
	var master string
	if err := tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil || master == "" {
		return http.StatusNotFound, fmt.Errorf("pool not found")
	}
	var known string
	if err := tx.QueryRow("SELECT id FROM hosts WHERE id=?", host).Scan(&known); err != nil {
		return http.StatusNotFound, fmt.Errorf("host not found")
	}
	if host == master {
		return http.StatusConflict, fmt.Errorf("this host is already the pool master")
	}
	purpose := "answer a preflight"
	if promoting {
		purpose = "be promoted"
	}
	if err := c.managedHost(tx, pool, host); err != nil {
		return http.StatusConflict, fmt.Errorf("the promotion candidate must be fresh and active to %s", purpose)
	}
	if !hostSupportsCapability(tx, host, protocol.PoolAuthorityTransferCapability) {
		return http.StatusConflict, fmt.Errorf("a fence-verified promotion needs %s on the promotion candidate; upgrade %s", protocol.PoolAuthorityTransferCapability, host)
	}
	if !promoting {
		return 0, nil
	}
	// The membership consulted here is the candidate's own copy, not the master's: the
	// master is the host that is gone. Core re-proves all of it from the signed replica.
	inv, err := hostInventory(tx, host)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("inventory unavailable")
	}
	membership := reportedMembership(inv)
	if membership == nil {
		return http.StatusConflict, fmt.Errorf("the promotion candidate has not reported its pool membership; it cannot be promoted yet")
	}
	if membership.MasterHostID == host {
		return http.StatusConflict, fmt.Errorf("this host is already the pool master")
	}
	member := maintenanceMember(membership, host)
	if membership.PoolID != pool || member == nil || member.Status != "ready" {
		return http.StatusConflict, fmt.Errorf("only a ready member of this pool can be promoted")
	}
	return 0, nil
}

// setPoolFenceProfile implements POST /api/v1/pools/{id}/members/{host_id}/fence-profile.
// The profile is a field of the membership record the master owns, so this one is relayed
// to the master and does require a fresh one.
func (c *Controller) setPoolFenceProfile(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool, host := r.PathValue("id"), r.PathValue("host_id")
	var body poolFenceProfileAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(pool) || !nameValid(host) || !nameValid(body.RequestID) {
		fail(w, http.StatusBadRequest, "pool, host and a stable request_id are required")
		return
	}
	kind, status, err := fenceProfileCredentialKind(body)
	if err != nil {
		fail(w, status, err.Error())
		return
	}
	payload := poolFenceProfilePayload{PoolID: pool, HostID: host, RequestID: body.RequestID, Fence: body.Fence}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "pool-fence-profile", payload) {
		return
	}
	master, status, err := c.poolFenceProfileTarget(tx, pool, host)
	if err != nil {
		fail(w, status, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	// The profile the command carries is a copy: the stored replay payload keeps the
	// client's own profile, so a retry compares equal whatever reference Core allocates.
	fence := body.Fence
	if kind != "" {
		resolved, credentialStatus, credentialErr := c.deliverFenceCredential(body, kind, pool, master, host)
		if credentialErr != nil {
			fail(w, credentialStatus, credentialErr.Error())
			return
		}
		fence = resolved
	}
	c.commandFenceProfile(w, user, pool, master, host, payload, fence)
}

// fenceProfileCredentialKind validates the submitted profile and names the credential kind
// its material would be delivered as, or "" when no credential accompanies it.
func fenceProfileCredentialKind(body poolFenceProfileAPIRequest) (string, int, error) {
	if body.Fence == nil {
		if body.Credential != nil {
			return "", http.StatusBadRequest, fmt.Errorf("a fence credential is delivered only with a fence profile")
		}
		return "", 0, nil
	}
	kind := ""
	if body.Credential != nil {
		switch body.Fence.Kind {
		case protocol.FenceVirsh:
			kind = "fence_ssh"
		case protocol.FenceRedfish, protocol.FenceIPMI:
			kind = "fence_bmc"
		}
		if kind == "" || body.Fence.CredentialRef != "" || body.Fence.SSHHostKeyRef != "" {
			return "", http.StatusBadRequest, fmt.Errorf("a fence credential is delivered only with a fence profile that takes one and names no reference of its own")
		}
		if !scopedCredentialValid(kind, *body.Credential) {
			return "", http.StatusBadRequest, fmt.Errorf("valid scoped fence credential required")
		}
	}
	fence := *body.Fence
	if kind != "" {
		fence.CredentialRef = fenceProfileCredentialPlaceholder
		if kind == "fence_ssh" {
			fence.SSHHostKeyRef = fenceProfileCredentialPlaceholder
		}
	}
	if err := fence.Valid(); err != nil {
		return "", http.StatusBadRequest, err
	}
	return kind, 0, nil
}

// deliverFenceCredential hands the material to Core over the existing one-time encrypted
// offer and returns the profile carrying the opaque reference Core allocated for it. The
// broker has no persistence, so nothing here is written to the Controller database, and the
// material is never logged, audited or echoed.
func (c *Controller) deliverFenceCredential(body poolFenceProfileAPIRequest, kind, pool, master, host string) (*protocol.StorageClusterFence, int, error) {
	binding := "cluster-credential:" + hex.EncodeToString([]byte(body.RequestID)) + ":" + kind + ":" + hex.EncodeToString([]byte(host))
	if _, err := c.credentials.offer(storageCredentialRequest{RequestID: body.RequestID, RegistrationRequestID: binding, PoolID: pool, HostID: master, Credentials: *body.Credential}); err != nil {
		return nil, http.StatusConflict, err
	}
	receipt, ok := c.credentials.wait(body.RequestID)
	if !ok || receipt.Receipt == "" {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("Core did not acknowledge fence credential delivery; retry with the same request_id")
	}
	fence := *body.Fence
	fence.CredentialRef = receipt.Receipt
	if kind == "fence_ssh" {
		fence.SSHHostKeyRef = receipt.Receipt
	}
	return &fence, 0, nil
}

// commandFenceProfile journals the one durable command to the master.
func (c *Controller) commandFenceProfile(w http.ResponseWriter, user, pool, master, host string, payload poolFenceProfilePayload, fence *protocol.StorageClusterFence) {
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, payload.RequestID, "pool-fence-profile", payload) {
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "pool-fence-profile", Pool: &protocol.PoolCommand{
		RequestID:    payload.RequestID,
		Action:       "fence-profile",
		Membership:   protocol.PoolMembership{MasterHostID: master},
		MemberHostID: host,
		Fence:        fence,
	}}
	op, err := c.saveResource(tx, user, payload.RequestID, pool, master, host, "pool-fence-profile:"+host+":"+opID, cmd, payload)
	if err != nil || c.audit(tx, user, "pool.fence-profile", pool) != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "fence profile storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}

// poolFenceProfileTarget returns the master the command must be addressed to.
func (c *Controller) poolFenceProfileTarget(tx *sql.Tx, pool, host string) (string, int, error) {
	if status, err := hostKnown(tx, host); err != nil {
		return "", status, err
	}
	master, status, err := c.freshPoolMaster(tx, pool, "record a fence profile")
	if err != nil {
		return "", status, err
	}
	if !hostSupportsCapability(tx, master, protocol.PoolAuthorityTransferCapability) {
		return "", http.StatusConflict, fmt.Errorf("recording a fence profile needs %s on the pool master; upgrade %s", protocol.PoolAuthorityTransferCapability, master)
	}
	inv, err := hostInventory(tx, master)
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("inventory unavailable")
	}
	membership := reportedMembership(inv)
	if membership == nil {
		return "", http.StatusConflict, fmt.Errorf("the pool master has not reported its membership; a fence profile cannot be recorded yet")
	}
	if maintenanceMember(membership, host) == nil {
		return "", http.StatusConflict, fmt.Errorf("no such member of this pool")
	}
	return master, 0, nil
}

// poolPromotionView is a candidate's own reported promotion, with the host that reported it.
// The Controller records no candidate: it shows the fresh host of this pool whose promotion
// report is newest, and keeps nothing.
type poolPromotionView struct {
	HostID string `json:"host_id"`
	protocol.PoolPromotionStatus
}

func reportedPromotion(hosts map[string]hostRow, pool string) *poolPromotionView {
	var out *poolPromotionView
	for id, host := range hosts {
		if host.poolID != pool || host.connection != "fresh" || host.inventory.Managed == nil || host.inventory.Managed.Promotion == nil {
			continue
		}
		reported := *host.inventory.Managed.Promotion
		if out != nil && !reported.ObservedAt.After(out.ObservedAt) && !(reported.ObservedAt.Equal(out.ObservedAt) && id < out.HostID) {
			continue
		}
		out = &poolPromotionView{HostID: id, PoolPromotionStatus: reported}
	}
	return out
}

// memberRestricted is this host's own Core-enforced refusal to act on pool authority. It is
// the member's own report, so it is shown whenever that host is reporting, whatever the pool
// master is doing: a restricted old master is exactly a host reporting for itself in a pool
// whose master has changed.
func memberRestricted(host hostRow) *protocol.PoolRestrictedStatus {
	if host.id == "" || host.connection != "fresh" || host.inventory.Managed == nil {
		return nil
	}
	return host.inventory.Managed.Restricted
}

// poolMemberFence is what the master's membership records about how a host would be
// excluded. The credential reference is never projected, and neither is anything it names.
// There is no "usable" field: no host reports whether a profile can be turned into a proof.
type poolMemberFence struct {
	Kind       string `json:"kind"`
	Endpoint   string `json:"endpoint,omitempty"`
	Configured bool   `json:"configured"`
	// CredentialVersion is the master's own counter for the private material behind the
	// profile, mirrored verbatim. The material itself never reaches this Controller; the
	// number is what tells an operator that a rotation has actually been replicated.
	CredentialVersion uint64 `json:"credential_version"`
}

func memberFence(member protocol.PoolMember) *poolMemberFence {
	if member.Fence == nil {
		return nil
	}
	return &poolMemberFence{Kind: member.Fence.Kind, Endpoint: member.Fence.Endpoint, Configured: true, CredentialVersion: member.FenceCredentialVersion}
}

// hostSupportsTransfer reports one host's own advertised transfer capability, so the UI can
// see that a promotion candidate or master-change target could not perform one before it
// offers the action. A host that is not reporting keeps its last advertised list.
func hostSupportsTransfer(host hostRow) bool {
	return slices.Contains(host.capabilities, protocol.PoolAuthorityTransferCapability)
}
