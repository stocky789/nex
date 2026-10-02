// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Manual, operator-selected, fence-verified promotion after the permanent loss of a pool
// master, and the safe automatic rejoin of the host that was lost. Nothing here elects a
// master: an operator picks the surviving member, Core refuses unless it can prove every
// check below on its own, and a two-host pool never promotes by itself.
//
// The proof, all of which must hold on the candidate's own Core:
//
//  1. Replica integrity — the stored replica verifies against the pool CA this host pinned
//     at join, declares every section, names this pool and this host, and carries a pool CA
//     key this host can actually open. A missing or unverifiable replica blocks the
//     promotion; a fence can never substitute for it.
//  2. Replica supremacy — every host of the replica's membership is probed. Any reachable
//     host holding a higher authority or replica generation blocks the promotion and is
//     named as the better candidate. Unreachable hosts do not veto; they are listed, and
//     they force check 4.
//  3. Accepted ownership — every VM record of the replica is classified from evidence:
//     confirmed by its reachable owner's own record, fenced with its owner, or blocked. A
//     reachable owner that disagrees with the replica blocks the promotion outright, which
//     is ambiguous authority. A blocked VM does not block the pool; it stays visible and
//     unstartable until it is reconciled.
//  4. Old-master exclusion — a fence proof produced by this Core, through the existing
//     native fence mechanism, bound to this pool, this host, this operation and this
//     authority generation, with an independently observed power-off no older than
//     fifteen minutes. A reachable, cooperative old master is refused here and sent to the
//     planned master change instead. A fence request that returned success, a timeout, a
//     missed heartbeat and an operator assurance are none of them proof and cannot produce
//     this record.
//
// The rejoin half is the same rule read from the other side: a host that cannot prove it
// holds this pool's current authority starts nothing. That refusal is Core's, it is durable,
// and it holds with no Controller connected.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolRestrictedCheck = "pool-restricted"
const poolPromotionCheck = "pool-promotion"

// The two reasons a host restricts itself. They are whole sentences because they are what an
// operator reads, in Core's own words, in inventory and in the libvirt start refusal.
const poolRestrictedUnconfirmed = "unconfirmed"
const poolRestrictedSuperseded = "superseded"
const poolUnconfirmedReason = "this pool master has not confirmed its authority since it restarted; until every other member confirms it, it starts no shared-storage guest and writes no pool authority"
const poolSupersededReason = "this host's pool authority was superseded by another master; it starts no shared-storage guest and writes no pool authority until it has rejoined and reconciled"
const poolUnconfirmedUnreachableDetail = "unreachable pool hosts: "

// poolFenceVerify is the fencing seam. Production always calls the native verifier; a test
// replaces it so no test ever runs a real fence agent.
var poolFenceVerify = vmhost.VerifyPoolHostFence

type poolRestrictedRecord struct {
	Kind        string
	Reason      string
	Since       time.Time
	Conflicts   []string
	Unreachable []string
}

func poolRestrictedReason(record *poolRestrictedRecord) string {
	if record == nil {
		return ""
	}
	reason := record.Reason
	if len(record.Unreachable) != 0 {
		unreachable := append([]string(nil), record.Unreachable...)
		sort.Strings(unreachable)
		reason += "; " + poolUnconfirmedUnreachableDetail + strings.Join(unreachable, ", ")
	}
	if len(record.Conflicts) != 0 {
		return reason + " (VM ownership conflicts: " + strings.Join(record.Conflicts, ", ") + ")"
	}
	return reason
}

func poolRestrictedStatus(record *poolRestrictedRecord) *protocol.PoolRestrictedStatus {
	if record == nil {
		return nil
	}
	return &protocol.PoolRestrictedStatus{
		Kind: record.Kind, Reason: poolRestrictedReason(record), Since: record.Since,
		Conflicts: append([]string(nil), record.Conflicts...), ObservedAt: time.Now().UTC(),
	}
}

func supersededPoolRestriction(current *poolRestrictedRecord) *poolRestrictedRecord {
	since := time.Now().UTC()
	conflicts := []string(nil)
	if current != nil && current.Kind == poolRestrictedSuperseded {
		since, conflicts = current.Since, current.Conflicts
	}
	return &poolRestrictedRecord{Kind: poolRestrictedSuperseded, Reason: poolSupersededReason, Since: since, Conflicts: conflicts}
}

// PoolRestrictedReport is this host's own restriction, for inventory.
func PoolRestrictedReport(s *Store) *protocol.PoolRestrictedStatus {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return nil
	}
	return poolRestrictedStatus(state.Restricted)
}

// PoolRestrictedCheck reports Core's current restriction state. A clear restriction is an
// explicit fresh healthy sample so an inventory consumer can replace a previous failure
// instead of retaining it as an omitted, possibly stale check.
func PoolRestrictedCheck(s *Store) []protocol.Check {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return nil
	}
	if state.Restricted == nil {
		return []protocol.Check{{Name: poolRestrictedCheck, Status: "healthy", Freshness: "fresh", ObservedAt: time.Now().UTC()}}
	}
	return []protocol.Check{{Name: poolRestrictedCheck, Status: "failed", Freshness: "fresh", ObservedAt: time.Now().UTC(), Error: poolRestrictedReason(state.Restricted)}}
}

// EnsurePoolBootRestriction is the boot rule, and it runs before any start path is exposed.
//
// A master that was fenced and promoted away cannot know it at boot: it has no network yet,
// and the evidence that it is no longer the master exists only on other hosts. So a master
// that has other ready members assumes nothing. It restricts itself until it has re-proved,
// against those members, that it still holds the highest authority. A single-host pool has
// no one who could have promoted anything, so it is never restricted and its guests start as
// they always did.
var poolBootAuthorityID = func() (string, error) { return poolVMAutostartBootID() }

func EnsurePoolBootRestriction(s *Store) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil {
		return err
	}
	if state.Restricted != nil || state.Membership.MasterHostID != s.State.HostID {
		return nil
	}
	if len(poolConfirmationPeers(state.Membership, s.State.HostID)) == 0 {
		return nil
	}
	if !poolSelfFencePromotable(state, s.State.HostID) {
		// Durable evidence, not an assumption: a promotion over this host is only possible
		// with a fence proof, a proof needs either a fence profile for this host that some
		// member holds in its replica or an armed watchdog of its own, and this host has
		// never had either. Nothing could have taken its authority while it was down, so it
		// comes back as the master it was and its guests start. A planned hand-off would
		// have left HandedOff behind and is refused separately.
		return nil
	}
	state.Restricted = &poolRestrictedRecord{Kind: poolRestrictedUnconfirmed, Reason: poolUnconfirmedReason, Since: time.Now().UTC()}
	return savePoolPeer(s, state)
}

// poolSelfFencePromotable answers whether any host could still fence-promote over this host,
// from this host's own durable record alone. It is true while an exposure is recorded, and
// stays true after it is cleared until every ready member has acknowledged a replica built
// after the clearing: a member whose latest replica predates it still holds the record that
// replica carried, and could still fence-promote with it.
func poolSelfFencePromotable(state poolPeerState, self string) bool {
	if state.SelfFence != nil {
		return true
	}
	member, ok := poolMemberByID(state.Membership, self)
	return ok && poolSelfFenceExposed(member)
}

// poolSelfFenceExposed is the whole list of ways a member could be fence-promoted over: a
// usable fence profile some peer holds in its replica, or its own armed watchdog, which
// needs no profile and no credential on the promoting host at all.
func poolSelfFenceExposed(member protocol.PoolMember) bool {
	return vmhost.PoolFenceUsable(member.Fence, member.SystemUUID) == nil ||
		member.WatchdogArmed != nil && member.WatchdogArmed.Armed
}

// recordPoolSelfFenceExposure is called under the peer lock by the places that change what
// could fence this host — its fence profile and its own arming. The caller passes the
// membership it is about to write.
func recordPoolSelfFenceExposure(state *poolPeerState, self string, wasPromotable bool) error {
	member, ok := poolMemberByID(state.Membership, self)
	usable := ok && poolSelfFenceExposed(member)
	switch {
	case usable:
		state.SelfFence = &poolSelfFenceExposure{}
	case state.SelfFence != nil && state.SelfFence.ClearedAt == 0:
		// The next replica this master pushes is the first one without the profile, and a
		// push advances the generation by exactly one when the content changed — which
		// removing a replicated membership field does.
		state.SelfFence.ClearedAt = state.AuthorityGeneration + 1
	}
	// This is the one transition that can prove the current master was not fence-promoted
	// before this write: no usable self profile (including a retained SelfFence exposure)
	// existed in the previous durable record. The existing writable-authority guard ran
	// before this transition. Bind that proof to this boot in the same write that makes a
	// member able to fence-promote this master. Rotations and re-records have no equivalent
	// proof and deliberately retain their existing marker state.
	if !wasPromotable && poolSelfFencePromotable(*state, self) && state.Membership.MasterHostID == self && len(poolConfirmationPeers(state.Membership, self)) != 0 && state.Restricted == nil {
		bootID, err := poolBootAuthorityID()
		if err != nil {
			return err
		}
		state.BootAuthority = &poolBootAuthorityConfirmation{PoolID: state.Membership.PoolID, HostID: self, BootID: bootID}
	}
	return nil
}

// ReleasePoolSelfFenceExposure drops the exposure once every ready member has acknowledged a
// replica at or above the clearing generation. Acknowledgements are memory-only
// observations, so this only ever runs on a master that is actually pushing replicas, and a
// restart simply re-establishes them before the exposure can be dropped again.
func ReleasePoolSelfFenceExposure(s *Store) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.SelfFence == nil || state.SelfFence.ClearedAt == 0 {
		return
	}
	if state.Membership.Valid() != nil || state.Membership.MasterHostID != s.State.HostID {
		return
	}
	if member, ok := poolMemberByID(state.Membership, s.State.HostID); ok && vmhost.PoolFenceUsable(member.Fence, member.SystemUUID) == nil {
		return
	}
	poolReplicaMu.Lock()
	acks := poolReplicaAcks[s.dir]
	poolReplicaMu.Unlock()
	for _, member := range poolConfirmationPeers(state.Membership, s.State.HostID) {
		ack, ok := acks[member.HostID]
		if !ok || !ack.Verified || ack.Generation < state.SelfFence.ClearedAt {
			return
		}
	}
	state.SelfFence = nil
	_ = savePoolPeer(s, state)
}

// poolConfirmationPeers are the hosts whose answer a master needs after a restart: every
// other ready member, because only a ready member can ever have been promoted.
func poolConfirmationPeers(membership protocol.PoolMembership, self string) []protocol.PoolMember {
	out := []protocol.PoolMember{}
	for _, member := range membership.Members {
		if member.HostID != self && member.Status == "ready" {
			out = append(out, member)
		}
	}
	return out
}

// ReconcilePoolRestriction is the tick half: a restricted host tries, from evidence only, to
// establish where this pool's authority actually is. It never clears a restriction on a
// timeout, and it never guesses.
func ReconcilePoolRestriction(ctx context.Context, s *Store, timeout time.Duration) {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Restricted == nil || state.Membership.Valid() != nil {
		return
	}
	if state.Restricted.Kind == poolRestrictedUnconfirmed {
		confirmPoolAuthorityAfterRestart(ctx, s, state, timeout)
		return
	}
	reconcilePoolRejoin(ctx, s, state, timeout)
}

// confirmPoolAuthorityAfterRestart is the quorum-free, evidence-based rule a restarted master
// uses. Every other ready member must be reachable and must name this host as its master at
// no higher a generation. One member that cannot be reached leaves the host restricted: an
// unreachable member is exactly the host that could have been promoted while this one was
// off, and could not have re-pointed it afterwards. Silence is never taken as agreement.
func confirmPoolAuthorityAfterRestart(ctx context.Context, s *Store, state poolPeerState, timeout time.Duration) {
	self := s.State.HostID
	superseding := ""
	unreachable := []string{}
	for _, member := range poolConfirmationPeers(state.Membership, self) {
		origin := member.PeerOrigin
		if origin == "" {
			unreachable = append(unreachable, member.HostID)
			continue
		}
		status, err := probePoolAuthority(ctx, s, origin, member.HostID, timeout)
		if err != nil {
			unreachable = append(unreachable, member.HostID)
			continue
		}
		if status.MasterHostID != self || status.Role == "master" || status.AuthorityGeneration > state.AuthorityGeneration {
			superseding = status.MasterHostID
			if status.Role == "master" {
				superseding = status.HostID
			}
			break
		}
	}
	if superseding != "" && superseding != self {
		// This host is not the master any more. It converges on signed evidence alone.
		markPoolAuthoritySuperseded(s)
		convergeSupersededMaster(ctx, s, superseding, timeout)
		return
	}
	recordPoolRestrictionUnreachable(s, unreachable)
	if len(unreachable) != 0 && !poolRestartUnreachableExcluded(state, unreachable) {
		return
	}
	clearPoolRestriction(s, poolRestrictedUnconfirmed)
}

// recordPoolRestrictionUnreachable names the ready members whose silence still blocks
// clearance. It changes nothing else about the restriction.
func recordPoolRestrictionUnreachable(s *Store, unreachable []string) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Restricted == nil || state.Restricted.Kind != poolRestrictedUnconfirmed {
		return
	}
	sorted := append([]string(nil), unreachable...)
	sort.Strings(sorted)
	if slices.Equal(state.Restricted.Unreachable, sorted) {
		return
	}
	state.Restricted.Unreachable = sorted
	_ = savePoolPeer(s, state)
}

// poolRestartUnreachableExcluded is true only when this host's own durable transfer record
// already carries a fresh fence proof for every unreachable ready member, bound to this
// pool, that host, the recorded operation and this authority generation. Nothing here
// fences; it only consumes proofs an operator already confirmed elsewhere.
func poolRestartUnreachableExcluded(state poolPeerState, unreachable []string) bool {
	if len(unreachable) == 0 {
		return true
	}
	record := state.Transfer
	if record == nil || record.OperationID == "" || len(record.Fences) == 0 {
		return false
	}
	now := time.Now().UTC()
	for _, hostID := range unreachable {
		member, ok := poolMemberByID(state.Membership, hostID)
		if !ok {
			return false
		}
		proof := poolPromotionProofFor(record.Fences, hostID)
		if protocol.PoolPromotionFenceVerified(proof, member, state.Membership.PoolID, hostID, record.OperationID, state.AuthorityGeneration, now) != nil {
			return false
		}
	}
	return true
}

// markPoolAuthoritySuperseded records the second restriction durably before this host tries
// to converge, so an interrupted convergence still leaves a host that starts nothing.
func markPoolAuthoritySuperseded(s *Store) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || (state.Restricted != nil && state.Restricted.Kind == poolRestrictedSuperseded) {
		return
	}
	state.Restricted = supersededPoolRestriction(state.Restricted)
	_ = savePoolPeer(s, state)
}

// convergeSupersededMaster asks the host another member named and adopts its signed re-point
// notice. A name is a hint; only the signature at a strictly higher membership generation
// changes anything on this host.
func convergeSupersededMaster(ctx context.Context, s *Store, hostID string, timeout time.Duration) {
	member, err := currentPoolMember(s, hostID)
	if err != nil {
		return
	}
	status, err := probePoolAuthority(ctx, s, member.PeerOrigin, member.HostID, timeout)
	if err != nil || status.Notice == nil {
		return
	}
	_, _ = applyPoolRepoint(s, member, protocol.PoolTransferRequest{OperationID: status.Notice.OperationID, Action: "repoint", Notice: *status.Notice})
}

// reconcilePoolRejoin is the returning host's gate. The restriction clears only when this
// host has re-pointed to the current master, holds that master's current authority replica,
// has published a VM inventory the master accepted, and has no VM the master records as
// running somewhere else. A conflict leaves it restricted and names the VM: Core never
// starts a second copy of a guest that was recovered elsewhere, and it never deletes the
// local record of one either.
func reconcilePoolRejoin(ctx context.Context, s *Store, state poolPeerState, timeout time.Duration) {
	if state.Membership.MasterHostID == s.State.HostID {
		return // Still recorded as master: it has not re-pointed yet.
	}
	master, ok := poolMemberByID(state.Membership, state.Membership.MasterHostID)
	if !ok || master.PeerOrigin == "" {
		return
	}
	status, err := probePoolAuthority(ctx, s, master.PeerOrigin, master.HostID, timeout)
	if err != nil || status.Role != "master" || status.CallerStatus != "ready" {
		return
	}
	replica, err := loadPoolReplica(s)
	if err != nil || replica == nil || !replica.Verified || replica.Replica.MasterHostID != master.HostID {
		return // The current master has not replicated its authority to this host yet.
	}
	if replica.Replica.Membership.Generation < state.Membership.Generation {
		return
	}
	// Ownership first: a guest the current authority records elsewhere is the refusal an
	// operator must see, and it is decided from this host's own journal alone.
	conflicts := poolOwnershipConflicts(ctx, s, replica.Replica)
	if len(conflicts) != 0 {
		recordPoolRestrictionConflicts(s, conflicts)
		return
	}
	if err = PublishPoolVMInventory(ctx, s); err != nil {
		return // The master has not accepted this host's own record of its guests.
	}
	clearPoolRestriction(s, poolRestrictedSuperseded)
}

// poolOwnershipConflicts names every VM this host still defines on pool-shared storage that
// the current authority records as belonging to another host. The local definition is left
// exactly where it is — it is not deleted, and it is never started here.
func poolOwnershipConflicts(ctx context.Context, s *Store, replica protocol.PoolAuthorityReplica) []string {
	var vms map[string]poolVMRecord
	if json.Unmarshal(replica.VMs, &vms) != nil {
		return []string{"the current pool authority's VM ownership record is unreadable"}
	}
	managed, readable := localPoolManagedVMs(ctx, s)
	if !readable {
		return []string{"this host's own VM inventory is unavailable"}
	}
	shared := map[string]bool{}
	for _, storage := range managed.Storage {
		if protocol.IsPoolSharedStorage(storage) {
			shared[storage.ID] = true
		}
	}
	conflicts := []string{}
	for _, vm := range managed.VMs {
		onShared := false
		for _, disk := range vm.Disks {
			onShared = onShared || shared[disk.StorageID]
		}
		record, known := vms[vm.ID]
		if !onShared || !known || record.HostID == s.State.HostID {
			continue
		}
		conflicts = append(conflicts, vm.ID)
	}
	sort.Strings(conflicts)
	return conflicts
}

func recordPoolRestrictionConflicts(s *Store, conflicts []string) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Restricted == nil || slices.Equal(state.Restricted.Conflicts, conflicts) {
		return
	}
	state.Restricted.Conflicts = conflicts
	_ = savePoolPeer(s, state)
}

func clearPoolRestriction(s *Store, kind string) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Restricted == nil || state.Restricted.Kind != kind {
		return
	}
	bootID, err := poolBootAuthorityID()
	if err != nil {
		return // retain the restriction; a later tick can safely retry.
	}
	state.Restricted = nil
	state.BootAuthority = &poolBootAuthorityConfirmation{PoolID: state.Membership.PoolID, HostID: s.State.HostID, BootID: bootID}
	_ = savePoolPeer(s, state)
}

// requirePoolStartPath is the refusal every start, definition and placement path takes on the
// host that would run the guest. The master-side guards live inside the durable ownership
// record (requirePoolVMAuthorityWritable); this one is the same rule on the asking side, so a
// restricted host does not even request a permit it must not have.
func requirePoolStartPath(s *Store) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return errors.New("pool authority state is unavailable")
	}
	if state.Restricted == nil {
		return nil
	}
	return errors.New(poolRestrictedReason(state.Restricted))
}

// localPoolVMOwnership is this host's own statement about the VMs it defines, from its own
// managed journal. It is the evidence a promotion candidate asks a reachable owner for.
//
// It requires the journal to be readable whole and nothing else: which host owns a
// definition is a durable journal fact, while whether libvirt currently lists that domain is
// runtime state, and a host whose libvirt is down still owns exactly what it owned before.
// The second return separates "this host defines no VM" from "this host could not tell".
func localPoolVMOwnership(ctx context.Context, s *Store) ([]protocol.PoolVMOwnership, bool) {
	managed, readable := localPoolManagedVMs(ctx, s)
	if !readable {
		return nil, false
	}
	out := make([]protocol.PoolVMOwnership, 0, len(managed.VMs))
	for _, vm := range managed.VMs {
		out = append(out, protocol.PoolVMOwnership{VMID: vm.ID, HostID: vm.HostID, Revision: vm.Revision, PoolID: vm.PoolID})
	}
	slices.SortFunc(out, func(a, b protocol.PoolVMOwnership) int { return strings.Compare(a.VMID, b.VMID) })
	return out, true
}

// localPoolManagedVMs is this host's own durable managed state, gated on the journal being
// readable whole and on nothing else. Whether libvirt currently lists a domain is runtime
// state and a separate question from which host owns a definition.
func localPoolManagedVMs(ctx context.Context, s *Store) (*protocol.ManagedState, bool) {
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(work)
	for _, check := range checks {
		if check.Name == "managed-journal" && check.Status != "healthy" && check.Status != "passed" {
			return nil, false
		}
	}
	return managed, managed != nil
}

// PoolPromotionReport is the candidate's own promotion, for inventory.
func PoolPromotionReport(s *Store) *protocol.PoolPromotionStatus {
	record, err := loadPoolTransfer(s)
	if err != nil || record == nil || record.Role != "candidate" {
		return nil
	}
	status := poolTransferStatus(record)
	return &protocol.PoolPromotionStatus{
		OperationID: record.OperationID, Stage: record.Stage, Status: status.Status,
		Error: record.LastError, Cancellable: status.Cancellable, Automatic: record.Automatic, ObservedAt: status.ObservedAt,
	}
}

// PoolPromotionCheck reports the current candidate promotion state. A finished promotion
// emits an explicit healthy sample so inventory consumers replace any earlier failure rather
// than retaining it after this host has recovered.
func PoolPromotionCheck(s *Store) []protocol.Check {
	record, err := loadPoolTransfer(s)
	if err != nil {
		return nil
	}
	if record == nil || record.Role != "candidate" {
		return []protocol.Check{{Name: poolPromotionCheck, Status: "healthy", Freshness: "fresh", ObservedAt: time.Now().UTC()}}
	}
	status := poolTransferStatus(record)
	if status.Status == "succeeded" {
		return []protocol.Check{{Name: poolPromotionCheck, Status: "healthy", Freshness: "fresh", ObservedAt: status.ObservedAt}}
	}
	check := protocol.Check{Name: poolPromotionCheck, Status: "unknown", Freshness: "fresh", ObservedAt: status.ObservedAt, Error: status.Error}
	if status.Status == "failed" || status.Status == "needs_attention" {
		check.Status = "failed"
	}
	if check.Error == "" {
		check.Error = "a fence-verified pool master promotion is in progress on this host"
	}
	return []protocol.Check{check}
}

// PoolPromotionPreflight computes the whole verdict and changes nothing. It is the call the
// Controller makes so an operator sees, in Core's words, exactly what blocks a promotion
// before confirming one that powers a host off.
func PoolPromotionPreflight(ctx context.Context, s *Store, operationID string, timeout time.Duration) protocol.PoolPromotionVerdict {
	record, err := loadPoolTransfer(s)
	var proofs []protocol.StorageClusterFenceStatus
	if err == nil && record != nil && record.Role == "candidate" && (operationID == "" || record.OperationID == operationID) {
		proofs, operationID = record.Fences, record.OperationID
	}
	return evaluatePoolPromotion(ctx, s, operationID, proofs, timeout)
}

// ExecutePoolPromotionRequest answers the Controller's brokered preflight in one report round
// trip. Nothing about it is durable and nothing is stored by the Controller.
func ExecutePoolPromotionRequest(ctx context.Context, s *Store, request protocol.PoolRequest, timeout time.Duration) protocol.PoolResult {
	result := protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "failed", ObservedAt: time.Now().UTC()}
	if request.RequestID == "" || len(request.RequestID) > 128 || request.HostID != s.State.HostID || request.PoolID != s.State.PoolID {
		result.Error = "pool request does not address this host"
		return result
	}
	verdict := PoolPromotionPreflight(ctx, s, "", timeout)
	result.Status, result.Promotion = "succeeded", &verdict
	return result
}

func poolPromotionBlocked(verdict *protocol.PoolPromotionVerdict, name, status, reason string, hosts, vms []string) {
	verdict.Checks = append(verdict.Checks, protocol.PoolPromotionCheck{Name: name, Status: status, Reason: reason, Hosts: hosts, VMs: vms})
	if status != "passed" {
		verdict.Blocking = append(verdict.Blocking, reason)
	}
}

// ReconcileAutomaticPoolPromotion starts the supported two-host automatic path. Core owns
// selection and the durable operation; the normal promotion stage machine still performs
// the destructive fence and rechecks the proof before activation.
func ReconcileAutomaticPoolPromotion(ctx context.Context, s *Store, timeout time.Duration) bool {
	keepAutomaticPromotionHold := false
	defer func() {
		if !keepAutomaticPromotionHold {
			resetAutomaticPoolPromotionHold(s.State.HostID)
		}
	}()
	if !poolTransferMu.TryLock() {
		return false
	}
	defer poolTransferMu.Unlock()

	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil || state.CAPEM == "" || state.Restricted != nil {
		return false
	}
	if record, loadErr := loadPoolTransfer(s); loadErr != nil || record != nil && !poolTransferTerminal(record.Stage) {
		return false
	}
	stored, err := loadPoolReplica(s)
	if err != nil || stored == nil || !stored.Verified {
		return false
	}
	membership := stored.Replica.Membership
	// The automatic path must honor the pool-wide opt-out as well: when fencing is
	// disabled, boot restriction is not an authorization to take over automatically.
	if membership.Valid() != nil || membership.HostFencingDisabled || membership.MasterHostID != stored.Replica.MasterHostID || membership.MasterHostID == s.State.HostID {
		return false
	}
	ready := make([]protocol.PoolMember, 0, 2)
	for _, member := range membership.Members {
		if member.Status == "ready" {
			ready = append(ready, member)
		}
	}
	if len(ready) != 2 {
		return false
	}
	candidate, candidateOK := poolMemberByID(membership, s.State.HostID)
	oldMaster, oldMasterOK := poolMemberByID(membership, membership.MasterHostID)
	if !candidateOK || !oldMasterOK || candidate.Status != "ready" || oldMaster.Status != "ready" || candidate.HostID == oldMaster.HostID {
		return false
	}
	for _, member := range ready {
		if member.HostID != candidate.HostID && member.HostID != oldMaster.HostID {
			return false
		}
	}
	if stored.Replica.MasterHostID != oldMaster.HostID || stored.Replica.MemberHostID != candidate.HostID {
		return false
	}
	operationID := automaticPoolPromotionOperationID(membership.PoolID, candidate.HostID, stored.Replica.Generation)
	verdict := evaluatePoolPromotion(ctx, s, operationID, nil, timeout)
	if !automaticPoolPromotionPreflight(s.dir, verdict, oldMaster) {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	satisfied := automaticPoolPromotionHoldSatisfied(membership.PoolID, candidate.HostID, oldMaster.HostID, stored.Replica.Generation)
	if ctx.Err() != nil {
		return false
	}
	keepAutomaticPromotionHold = true
	if !satisfied {
		return false
	}
	record := &poolTransferRecord{
		OperationID: operationID, Action: "promote", Role: "candidate", TargetHostID: candidate.HostID,
		Stage: "preflight", Status: "pending", FenceAgreed: []string{oldMaster.HostID}, Automatic: true,
	}
	if savePoolTransfer(s, record) != nil {
		return false
	}
	advancePoolTransfer(ctx, s, record, timeout)
	return true
}

// Automatic fencing requires two qualifying observations at least this far apart. It
// delays action through a transient failed probe; it does not prove continuous downtime.
const automaticPoolPromotionHold = 15 * time.Second

var automaticPoolPromotionClock = time.Now

var automaticPoolPromotionHoldMu sync.Mutex
var automaticPoolPromotionHolds = map[string]automaticPoolPromotionHoldObservation{}

type automaticPoolPromotionHoldObservation struct {
	poolID, masterHostID string
	generation           uint64
	firstObserved        time.Time
}

func automaticPoolPromotionHoldSatisfied(poolID, candidateHostID, masterHostID string, generation uint64) bool {
	now := automaticPoolPromotionClock()
	automaticPoolPromotionHoldMu.Lock()
	defer automaticPoolPromotionHoldMu.Unlock()
	previous, ok := automaticPoolPromotionHolds[candidateHostID]
	if !ok || previous.poolID != poolID || previous.masterHostID != masterHostID || previous.generation != generation {
		automaticPoolPromotionHolds[candidateHostID] = automaticPoolPromotionHoldObservation{
			poolID: poolID, masterHostID: masterHostID, generation: generation, firstObserved: now,
		}
		return false
	}
	return now.Sub(previous.firstObserved) >= automaticPoolPromotionHold
}

func resetAutomaticPoolPromotionHold(candidateHostID string) {
	automaticPoolPromotionHoldMu.Lock()
	delete(automaticPoolPromotionHolds, candidateHostID)
	automaticPoolPromotionHoldMu.Unlock()
}

func automaticPoolPromotionPreflight(dir string, verdict protocol.PoolPromotionVerdict, oldMaster protocol.PoolMember) bool {
	if !verdict.ReadyToFence || len(verdict.FenceHostIDs) != 1 || verdict.FenceHostIDs[0] != oldMaster.HostID || !verdict.FenceRecorded || !verdict.FenceUsable {
		return false
	}
	for _, check := range verdict.Checks {
		if check.Name == "old-master-exclusion" {
			if check.Status != "unknown" {
				return false
			}
			continue
		}
		if check.Status != "passed" {
			return false
		}
	}
	oldMasterObservedUnreachable, oldMasterFenceKind := false, ""
	for _, host := range verdict.Hosts {
		if host.HostID == oldMaster.HostID {
			oldMasterObservedUnreachable, oldMasterFenceKind = !host.Reachable && host.Blocks, host.FenceKind
			break
		}
	}
	if !oldMasterObservedUnreachable {
		return false
	}
	if oldMaster.Fence == nil || oldMaster.Fence.Kind != protocol.FenceRedfish && oldMaster.Fence.Kind != protocol.FenceIPMI {
		return false
	}
	if oldMasterFenceKind != protocol.FenceRedfish && oldMasterFenceKind != protocol.FenceIPMI {
		return false
	}
	if !poolAutomaticBMCCheckFresh(dir, verdict.CandidateHostID, oldMaster) {
		return false
	}
	return true
}

func poolAutomaticBMCCheckFresh(dir, checker string, target protocol.PoolMember) bool {
	poolFenceCheckMu.Lock()
	check, ok := poolFenceChecks[dir][checker][target.HostID]
	poolFenceCheckMu.Unlock()
	return ok && check.Result == protocol.PoolFenceCheckOK && check.Profile == protocol.PoolFenceProfileStamp(target) && !check.ObservedAt.IsZero() && !check.ObservedAt.After(time.Now().Add(time.Minute)) && time.Since(check.ObservedAt) <= poolFenceCheckStaleAfter()
}

func automaticPoolPromotionOperationID(poolID, candidateHostID string, generation uint64) string {
	sum := sha256.Sum256([]byte("nex automatic pool promotion v1\x00" + poolID + "\x00" + candidateHostID + "\x00" + strconv.FormatUint(generation, 10)))
	return "auto-promotion-" + hex.EncodeToString(sum[:])
}

func automaticPoolPromotionFenceEnabled(s *Store, record *poolTransferRecord) error {
	if !record.Automatic {
		return nil
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		return errors.New("automatic pool promotion cannot confirm the Host fencing setting")
	}
	if state.Membership.HostFencingDisabled {
		return errors.New("automatic pool promotion is paused because Host fencing is off")
	}
	replica, err := loadPoolReplica(s)
	if err != nil || replica == nil || !replica.Verified {
		return errors.New("automatic pool promotion cannot confirm the Host fencing setting")
	}
	if replica.Replica.Membership.HostFencingDisabled {
		return errors.New("automatic pool promotion is paused because Host fencing is off")
	}
	return nil
}

// evaluatePoolPromotion runs the whole proof against live evidence. It is deliberately
// re-run immediately before the activation as well as at the preflight: a member that came
// back holding a higher generation between the two must stop the promotion.
func evaluatePoolPromotion(ctx context.Context, s *Store, operationID string, proofs []protocol.StorageClusterFenceStatus, timeout time.Duration) protocol.PoolPromotionVerdict {
	verdict := protocol.PoolPromotionVerdict{
		CandidateHostID: s.State.HostID, PoolID: s.State.PoolID, OperationID: operationID,
		FenceRequired: true, ObservedAt: time.Now().UTC(),
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil || state.CAPEM == "" {
		poolPromotionBlocked(&verdict, "replica-integrity", "blocked", "pool membership is unavailable", nil, nil)
		return verdict
	}
	verdict.PoolID, verdict.OldMasterHostID = state.Membership.PoolID, state.Membership.MasterHostID
	if state.Membership.MasterHostID == s.State.HostID {
		poolPromotionBlocked(&verdict, "replica-integrity", "blocked", "this host is already the master of its pool", nil, nil)
		return verdict
	}
	replica := verifiedPoolPromotionReplica(s, state, &verdict)
	if replica == nil {
		return verdict
	}
	verdict.AuthorityGeneration, verdict.OldMasterHostID = replica.Generation, replica.MasterHostID
	oldMaster, _ := poolMemberByID(replica.Membership, replica.MasterHostID)
	verdict.FenceRecorded, verdict.FenceUsable, _, verdict.FenceUnusableReason = poolPromotionFenceReadiness(s, state, *replica, oldMaster)
	if verdict.Fence = poolPromotionProofFor(proofs, replica.MasterHostID); verdict.Fence == nil && len(proofs) != 0 {
		verdict.Fence = &proofs[0]
	}
	// Which hosts are excluded already, from proofs this promotion produced itself. A host
	// counts as excluded only on a proof bound to this pool, that host, this operation and
	// this generation, observed inside the freshness window.
	excluded := map[string]bool{}
	for i := range proofs {
		member, ok := poolMemberByID(replica.Membership, proofs[i].HostID)
		if !ok {
			continue
		}
		if protocol.PoolPromotionFenceVerified(&proofs[i], member, replica.PoolID, proofs[i].HostID, operationID, replica.Generation, time.Now().UTC()) == nil {
			excluded[proofs[i].HostID] = true
		}
	}
	reachable := probePoolPromotionHosts(ctx, s, state, *replica, excluded, &verdict, timeout)
	checkPoolPromotionOwnership(ctx, s, *replica, reachable, excluded, &verdict)
	checkPoolPromotionExclusion(s, state, *replica, reachable, excluded, operationID, &verdict)
	verdict.CanPromote = len(verdict.Blocking) == 0
	// Ready to fence is the offer a caller may make: everything but the exclusion holds, the
	// exclusion is open only because nothing has been fenced yet, and every host this
	// promotion would power off can actually be fenced by this host. It is computed here so
	// no caller has to read Core's sentences.
	verdict.ReadyToFence = len(verdict.FenceHostIDs) != 0
	for _, host := range verdict.Hosts {
		// Every host that blocks must be one this promotion would power off, and one this
		// host could actually power off. A blocking host that cannot be fenced is a refusal
		// an operator has to resolve, never an offer.
		if host.Blocks && (!slices.Contains(verdict.FenceHostIDs, host.HostID) || !host.FenceUsable) {
			verdict.ReadyToFence = false
		}
	}
	for _, check := range verdict.Checks {
		switch check.Name {
		case "old-master-exclusion":
			verdict.ReadyToFence = verdict.ReadyToFence && check.Status == "unknown"
		case "replica-supremacy":
			// Unknown here means "hosts that are silent and fenceable", which is exactly
			// what confirming the fence resolves.
			verdict.ReadyToFence = verdict.ReadyToFence && (check.Status == "passed" || check.Status == "unknown")
		default:
			verdict.ReadyToFence = verdict.ReadyToFence && check.Status == "passed"
		}
	}
	return verdict
}

// poolPromotionProofFor finds this promotion's proof for one host.
func poolPromotionProofFor(proofs []protocol.StorageClusterFenceStatus, hostID string) *protocol.StorageClusterFenceStatus {
	if i := slices.IndexFunc(proofs, func(proof protocol.StorageClusterFenceStatus) bool { return proof.HostID == hostID }); i >= 0 {
		return &proofs[i]
	}
	return nil
}

// poolPromotionFenceReadiness answers, for one host, whether a profile is recorded and
// whether this candidate could actually turn it into a proof: the right kind of profile and a
// fence credential its own replica carried and it can open.
// It answers for both methods, in the order the plan gives them: the fence device where this
// host could use it, otherwise the target's own watchdog. kind names the method, so a caller
// shows the operator what is about to happen rather than only that something will.
func poolPromotionFenceReadiness(s *Store, state poolPeerState, replica protocol.PoolAuthorityReplica, member protocol.PoolMember) (recorded, usable bool, kind, reason string) {
	device := ""
	switch {
	case member.Fence == nil:
		device = "no fence profile is recorded for " + member.HostID
	default:
		kind = member.Fence.Kind
		if err := vmhost.PoolFenceUsable(member.Fence, member.SystemUUID); err != nil {
			device = err.Error()
		} else if err = poolPromotionCredentialReady(s, state, replica, member); err != nil {
			device = "this host holds no fence credential for " + member.HostID
		} else {
			return true, true, kind, ""
		}
	}
	// The other method: the target shut itself out. It needs no device and no credential
	// on this host — only the target's own armed report, in the durable record this host
	// holds, at the generation being promoted.
	if err := PoolWatchdogUsable(s.dir, member); err == nil {
		return true, true, protocol.PoolFenceWatchdog, ""
	}
	return member.Fence != nil, false, kind, poolPromotionReason(device)
}

// checkPoolPromotionExclusion proves check 4 over every host this promotion must exclude: the
// old master, and every other host that blocks it and can be fenced. A host that must be
// excluded and cannot be fenced is a refusal, not a fence.
func checkPoolPromotionExclusion(s *Store, state poolPeerState, replica protocol.PoolAuthorityReplica, reachable map[string]protocol.PoolAuthorityStatus, excluded map[string]bool, operationID string, verdict *protocol.PoolPromotionVerdict) {
	if status, ok := reachable[replica.MasterHostID]; ok && status.Role == "master" && !status.HandedOff && status.Restricted == nil {
		poolPromotionBlocked(verdict, "old-master-exclusion", "blocked", "the pool master "+replica.MasterHostID+" is reachable and holds this pool's authority; change the pool master with a planned master change instead of promoting", []string{replica.MasterHostID}, nil)
		return
	}
	// Every host that must be powered off for this promotion to be safe, named before any
	// of it happens: the old master, plus every host the supremacy check says blocks and
	// that a fence could exclude.
	required := []string{replica.MasterHostID}
	for _, host := range verdict.Hosts {
		if host.Blocks && host.HostID != replica.MasterHostID && host.FenceRecorded {
			required = append(required, host.HostID)
		}
	}
	slices.Sort(required)
	verdict.FenceHostIDs = slices.Compact(required)
	unfenceable, unfenceableReasons, pending, waits := []string{}, []string{}, []string{}, []string{}
	for _, hostID := range verdict.FenceHostIDs {
		member, ok := poolMemberByID(replica.Membership, hostID)
		recorded, usable, kind, reason := false, false, "", ""
		if ok {
			recorded, usable, kind, reason = poolPromotionFenceReadiness(s, state, replica, member)
		}
		if hostID == replica.MasterHostID {
			verdict.FenceRecorded, verdict.FenceUsable, verdict.FenceUnusableReason = recorded, usable, reason
		}
		if !recorded {
			poolPromotionBlocked(verdict, "old-master-exclusion", "blocked", "the pool master "+replica.MasterHostID+" has no recorded fence profile; record one before promoting", []string{hostID}, nil)
			return
		}
		if !usable {
			unfenceable = append(unfenceable, hostID)
			unfenceableReasons = append(unfenceableReasons, hostID+": "+reason)
			continue
		}
		if !excluded[hostID] {
			pending = append(pending, hostID)
			if kind == protocol.PoolFenceWatchdog {
				// Nobody powers this host off: it resets itself, and this host only
				// establishes that it must have. What the operator needs is the wait.
				waits = append(waits, hostID+": "+poolWatchdogWait(s, member))
			}
		}
	}
	switch {
	case len(unfenceable) != 0:
		poolPromotionBlocked(verdict, "old-master-exclusion", "blocked", "this host cannot fence "+strings.Join(unfenceableReasons, "; ")+", so it cannot be promoted while those hosts are unreachable", unfenceable, nil)
	case len(pending) != 0:
		reason := "pool host " + strings.Join(pending, ", ") + " has not been fenced for this promotion; Core fences each of them and observes the power-off when the promotion is confirmed"
		if len(waits) != 0 {
			reason += ". " + strings.Join(waits, "; ")
		}
		poolPromotionBlocked(verdict, "old-master-exclusion", "unknown", reason, pending, nil)
	default:
		poolPromotionBlocked(verdict, "old-master-exclusion", "passed", "", verdict.FenceHostIDs, nil)
	}
}

// poolPromotionReason bounds a reason that reaches a caller. Nothing private ever reaches
// one of these, and a long or multi-line one is replaced rather than passed on.
func poolPromotionReason(reason string) string {
	return boundPoolPeerText(200, "this host cannot fence the old pool master", reason)
}

// poolPromotionCredentialReady proves this host could actually fence the old master: the
// material is in the replica it holds, addressed to it, and opens. It opens the material to
// prove it and keeps none of it; only the promotion itself materializes it.
func poolPromotionCredentialReady(s *Store, state poolPeerState, replica protocol.PoolAuthorityReplica, oldMaster protocol.PoolMember) error {
	if oldMaster.Fence == nil {
		return errors.New("the old pool master has no recorded fence profile")
	}
	credential := poolFenceCredentialFor(replica, oldMaster.HostID)
	if credential == nil || credential.CredentialRef != oldMaster.Fence.CredentialRef {
		return errors.New("this host holds no fence credential for " + oldMaster.HostID)
	}
	_, err := unwrapPoolFenceCredential(state.KeyPEM, replica, *credential)
	return err
}

// materializePoolFenceCredential puts the old master's material where this host's own fence
// mechanism reads it, at the reference the profile already names, so the profile that was
// replicated is the profile that runs. It refuses to overwrite different material this host
// already holds under that reference rather than clobbering a credential of its own.
func materializePoolFenceCredential(s *Store, replica protocol.PoolAuthorityReplica, member protocol.PoolMember, record *poolTransferRecord) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return errors.New("pool peer state is unavailable")
	}
	if credErr := poolPromotionCredentialReady(s, state, replica, member); credErr != nil {
		return errors.New("this host holds no fence credential for " + member.HostID + "; it cannot fence it, so it cannot be promoted over it")
	}
	files, err := unwrapPoolFenceCredential(state.KeyPEM, replica, *poolFenceCredentialFor(replica, member.HostID))
	if err != nil {
		return err
	}
	if err = vmhost.WritePoolFenceCredential(s.dir, *member.Fence, files); err != nil {
		return err
	}
	if record != nil && !slices.Contains(record.Materialized, member.Fence.CredentialRef) {
		record.Materialized = append(record.Materialized, member.Fence.CredentialRef)
	}
	// From here this host owns that material: if it becomes the master it re-wraps it for
	// the members that remain, and it retires it when the profile is cleared or replaced.
	return nil
}

// retireAbandonedPoolFenceCredentials removes the material a promotion materialized when that
// promotion ends without this host becoming the master. Material this host's own membership
// still names is kept: it is then a profile this host owns, not a leftover.
func retireAbandonedPoolFenceCredentials(s *Store, record *poolTransferRecord) {
	if len(record.Materialized) == 0 {
		return
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return
	}
	kept := map[string]bool{}
	if state.Membership.MasterHostID == s.State.HostID {
		for _, member := range state.Membership.Members {
			if member.Fence != nil {
				kept[member.Fence.CredentialRef] = true
			}
		}
	}
	for _, ref := range record.Materialized {
		if !kept[ref] {
			_ = vmhost.RetirePoolFenceCredential(s.dir, ref)
		}
	}
	record.Materialized = nil
}

// verifiedPoolPromotionReplica proves check 1 and returns the replica the promotion would
// activate. Everything it checks is checked again by the activation itself.
func verifiedPoolPromotionReplica(s *Store, state poolPeerState, verdict *protocol.PoolPromotionVerdict) *protocol.PoolAuthorityReplica {
	stored, err := loadPoolReplica(s)
	if err != nil || stored == nil {
		poolPromotionBlocked(verdict, "replica-integrity", "blocked", "this host holds no pool authority replica; it cannot be promoted", nil, nil)
		return nil
	}
	replica := stored.Replica
	if !stored.Verified {
		poolPromotionBlocked(verdict, "replica-integrity", "blocked", "this host's pool authority replica was never verified", nil, nil)
		return nil
	}
	if replica.PoolID != state.Membership.PoolID || replica.MemberHostID != s.State.HostID {
		poolPromotionBlocked(verdict, "replica-integrity", "blocked", "this host's pool authority replica names another pool or another member", nil, nil)
		return nil
	}
	if err = replica.Valid(); err != nil {
		poolPromotionBlocked(verdict, "replica-integrity", "blocked", err.Error(), nil, nil)
		return nil
	}
	if err = verifyPoolAuthorityReplica(state.CAPEM, replica); err != nil {
		poolPromotionBlocked(verdict, "replica-integrity", "blocked", err.Error(), nil, nil)
		return nil
	}
	self, ok := poolMemberByID(replica.Membership, s.State.HostID)
	if !ok || self.Status != "ready" {
		poolPromotionBlocked(verdict, "replica-integrity", "blocked", "this host is not a ready member of the pool its replica describes", nil, nil)
		return nil
	}
	caKeyPEM, err := unwrapPoolCAKey(state.KeyPEM, replica)
	if err != nil {
		poolPromotionBlocked(verdict, "replica-integrity", "blocked", err.Error(), nil, nil)
		return nil
	}
	if _, err = parsePoolCAKey(caKeyPEM); err != nil {
		poolPromotionBlocked(verdict, "replica-integrity", "blocked", "the wrapped pool CA key is unusable on this host", nil, nil)
		return nil
	}
	poolPromotionBlocked(verdict, "replica-integrity", "passed", "", nil, nil)
	return &replica
}

// probePoolPromotionHosts proves check 2 and collects the live evidence check 3 needs. It
// returns the hosts that answered.
func probePoolPromotionHosts(ctx context.Context, s *Store, state poolPeerState, replica protocol.PoolAuthorityReplica, excluded map[string]bool, verdict *protocol.PoolPromotionVerdict, timeout time.Duration) map[string]protocol.PoolAuthorityStatus {
	answers := map[string]protocol.PoolAuthorityStatus{}
	better, silent, contending := []string{}, []string{}, []string{}
	for _, member := range replica.Membership.Members {
		if member.HostID == s.State.HostID || member.Status == "ejected" {
			continue
		}
		host := protocol.PoolPromotionHost{HostID: member.HostID}
		host.FenceRecorded, host.FenceUsable, host.FenceKind, host.FenceUnusableReason = poolPromotionFenceReadiness(s, state, replica, member)
		// Advisory only: this candidate's own last look at that fence device. It is
		// never an input to any check — the proof path is unchanged.
		host.FenceCheckWarning = poolFenceCheckWarning(s.dir, s.State.HostID, member.HostID)
		var status protocol.PoolAuthorityStatus
		err := errors.New("this host has never advertised a peer endpoint")
		if member.PeerOrigin != "" {
			status, err = probePoolAuthorityVMs(ctx, s, member.PeerOrigin, member.HostID, timeout)
		}
		if err != nil {
			host.Error = poolTransferError(err)
			// An unreachable host that is not the old master is the case this check exists
			// for: it is a ready member, so it holds the same replica and could be promoted
			// itself, and its silence is not evidence that it was not. It blocks unless this
			// promotion has excluded it by a verified fence proof of its own.
			if member.Status == "ready" && member.HostID != replica.MasterHostID && !excluded[member.HostID] {
				host.Blocks = true
				silent = append(silent, member.HostID)
			}
			if member.HostID == replica.MasterHostID && !excluded[member.HostID] {
				host.Blocks = true
			}
			verdict.Hosts = append(verdict.Hosts, host)
			continue
		}
		answers[member.HostID] = status
		host.Reachable, host.Role = true, status.Role
		host.AuthorityGeneration, host.ReplicaGeneration = status.AuthorityGeneration, status.ReplicaGeneration
		host.MembershipGeneration = status.MembershipGeneration
		switch {
		case status.AuthorityGeneration > replica.Generation || status.ReplicaGeneration > replica.Generation:
			host.Blocks = true
			better = append(better, member.HostID)
		case status.Role == "master" && member.HostID != replica.MasterHostID:
			host.Blocks = true
			better = append(better, member.HostID)
		case poolPromotionContending(status, s.State.HostID):
			// Another candidate is already performing a promotion of its own from this same
			// authority. Exactly one of them may proceed, and it is the one that recorded
			// its promotion first; this one refuses rather than racing it.
			host.Blocks = true
			contending = append(contending, member.HostID)
		case member.HostID == replica.MasterHostID && status.Role == "master" && !status.HandedOff && status.Restricted == nil:
			host.Blocks = true
		}
		verdict.Hosts = append(verdict.Hosts, host)
	}
	// This host's own row, so an operator sees the candidate beside the others.
	verdict.Hosts = append(verdict.Hosts, protocol.PoolPromotionHost{
		HostID: s.State.HostID, Reachable: true, Role: "member",
		ReplicaGeneration: replica.Generation, MembershipGeneration: replica.Membership.Generation,
	})
	switch {
	case len(better) != 0:
		poolPromotionBlocked(verdict, "replica-supremacy", "blocked", "pool host "+strings.Join(better, ", ")+" holds a newer pool authority than this host; promote it instead", better, nil)
	case len(contending) != 0:
		poolPromotionBlocked(verdict, "replica-supremacy", "blocked", "pool host "+strings.Join(contending, ", ")+" is already performing a promotion of this pool; let it finish or cancel it before promoting another host", contending, nil)
	case len(silent) != 0:
		// Unreachable surviving members do not veto quietly: they are named, and the
		// promotion may only proceed once each of them is excluded by its own fence proof.
		poolPromotionBlocked(verdict, "replica-supremacy", "unknown", "pool host "+strings.Join(silent, ", ")+" is unreachable and holds this pool's authority replica, so it could be promoted too; it must be fenced before this host can be promoted", silent, nil)
	default:
		poolPromotionBlocked(verdict, "replica-supremacy", "passed", "", nil, nil)
	}
	return answers
}

// poolPromotionContending reports that a reachable host is itself in the middle of a
// promotion of this pool, addressed to some host other than the one asking.
func poolPromotionContending(status protocol.PoolAuthorityStatus, self string) bool {
	transfer := status.Transfer
	if transfer == nil || transfer.Role != "candidate" || transfer.Action != "promote" {
		return false
	}
	if transfer.Status == "succeeded" || transfer.Status == "failed" {
		return false
	}
	return transfer.TargetHostID != self
}

// checkPoolPromotionOwnership proves check 3. A VM whose reachable owner disagrees with the
// replica is ambiguous authority and blocks the promotion; a VM whose owner is fenced or
// simply unreachable stays visible and unstartable, and blocks nothing.
func checkPoolPromotionOwnership(ctx context.Context, s *Store, replica protocol.PoolAuthorityReplica, reachable map[string]protocol.PoolAuthorityStatus, excluded map[string]bool, verdict *protocol.PoolPromotionVerdict) {
	var vms map[string]poolVMRecord
	if json.Unmarshal(replica.VMs, &vms) != nil {
		poolPromotionBlocked(verdict, "accepted-ownership", "blocked", "the replicated VM ownership record is unreadable", nil, nil)
		return
	}
	owned, reported := map[string][]protocol.PoolVMOwnership{}, map[string]bool{}
	owned[s.State.HostID], reported[s.State.HostID] = localPoolVMOwnership(ctx, s)
	for host, status := range reachable {
		owned[host], reported[host] = status.VMs, status.VMsReported
	}
	ids := make([]string, 0, len(vms))
	for id := range vms {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	mismatched := []string{}
	for _, id := range ids {
		record := vms[id]
		entry := protocol.PoolPromotionVM{VMID: id, HostID: record.HostID}
		_, ownerReachable := reachable[record.HostID]
		ownerReachable = ownerReachable || record.HostID == s.State.HostID
		switch {
		case record.PendingUpdate != nil:
			entry.State, entry.Reason = "blocked", "this VM has an unresolved definition transition and stays refused until it is reconciled"
		case ownerReachable && !reported[record.HostID]:
			entry.State, entry.Reason = "blocked", "its owner could not report its own VM inventory, so this VM's ownership is unresolved"
			mismatched = append(mismatched, id)
		case ownerReachable:
			if err := poolPromotionOwnerAgrees(record, owned[record.HostID]); err != nil {
				entry.State, entry.Reason = "blocked", err.Error()
				mismatched = append(mismatched, id)
			} else {
				entry.State = "confirmed"
			}
		case excluded[record.HostID]:
			entry.State, entry.Reason = "fenced", "its owner is excluded by this promotion's fence proof; the VM stays visible and is not startable until its disks are validated"
		default:
			entry.State, entry.Reason = "blocked", "its owner is unreachable, so this VM stays visible and is not startable until it is reconciled"
		}
		verdict.VMs = append(verdict.VMs, entry)
	}
	if len(mismatched) != 0 {
		poolPromotionBlocked(verdict, "accepted-ownership", "blocked", "pool VM "+strings.Join(mismatched, ", ")+" is recorded differently by the host that owns it; resolve that before promoting", nil, mismatched)
		return
	}
	poolPromotionBlocked(verdict, "accepted-ownership", "passed", "", nil, nil)
}

// poolPromotionOwnerAgrees compares one replica record with its owner's own statement. The
// execution epoch is deliberately not compared: it is the master's number and its owner has
// never held it, so it could only be repeated back rather than confirmed.
func poolPromotionOwnerAgrees(record poolVMRecord, reported []protocol.PoolVMOwnership) error {
	for _, vm := range reported {
		if vm.VMID != record.VMID {
			continue
		}
		if vm.HostID != record.HostID {
			return errors.New("its owner reports it on host " + vm.HostID)
		}
		if vm.Revision != record.Spec.Revision {
			return errors.New("its owner reports definition revision " + strconv.Itoa(vm.Revision) + ", not " + strconv.Itoa(record.Spec.Revision))
		}
		return nil
	}
	return errors.New("the host recorded as its owner does not define it")
}

// ExecutePoolPromotion records the Controller's durable promotion command on the candidate
// the operator selected and drives it as far as it gets in this pass. Every later pass
// resumes it from the tick, with no Controller connection at all.
func ExecutePoolPromotion(ctx context.Context, s *Store, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	v := cmd.Pool
	if v == nil || v.Valid() != nil || v.Action != "promote" {
		result.Error = "pool promotion command carries no promotion request"
		return result
	}
	if v.MemberHostID != s.State.HostID {
		result.Error = "a pool promotion is performed by the candidate itself"
		return result
	}
	if !v.FenceConfirmed || len(v.FenceHostIDs) == 0 {
		result.Error = "this promotion will power off other hosts; confirm the hosts this pool reports before it can run"
		return result
	}
	poolTransferMu.Lock()
	defer poolTransferMu.Unlock()
	record, err := loadPoolTransfer(s)
	if err != nil {
		result.Error = "pool authority transfer state is unavailable"
		return result
	}
	if record != nil && record.OperationID != cmd.OperationID && !poolTransferTerminal(record.Stage) {
		result.Error = "another pool authority transfer is already in progress on this pool"
		return result
	}
	if record == nil || record.OperationID != cmd.OperationID {
		record = &poolTransferRecord{OperationID: cmd.OperationID, Action: "promote", Role: "candidate", TargetHostID: s.State.HostID, Stage: "preflight", Status: "pending", FenceAgreed: append([]string(nil), v.FenceHostIDs...)}
		if err = savePoolTransfer(s, record); err != nil {
			result.Error = "the pool promotion could not be recorded durably"
			return result
		}
	}
	status := advancePoolTransfer(ctx, s, record, 20*time.Second)
	result.Status, result.Error, result.ObservedAt = status.Status, status.Error, status.ObservedAt
	if result.Status == "pending" {
		// The command is terminal for the Controller; this host keeps working and reports
		// its true stage in inventory rather than holding the operation open.
		result.Status = "needs_attention"
	}
	return result
}

// advancePoolPromotion is the candidate's stage machine: prove, fence, activate, re-point.
// Every stage persists before the next begins, so a crash at any point resumes at the stage
// that did not complete and the pool still ends with exactly one master.
func advancePoolPromotion(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) error {
	switch record.Stage {
	case "preflight":
		verdict := evaluatePoolPromotion(ctx, s, record.OperationID, record.Fences, timeout)
		if err := poolPromotionRefusal(verdict, "old-master-exclusion", "replica-supremacy"); err != nil {
			return err
		}
		// The operator's confirmation must name exactly the hosts Core says it must power
		// off, and it is checked before anything destructive happens.
		if err := poolPromotionFenceListAgreed(verdict, record); err != nil {
			return err
		}
		record.AuthorityGeneration = verdict.AuthorityGeneration
		record.TargetHostID = s.State.HostID
		record.Notice = nil
		record.Stage = "fence"
		if verdict.CanPromote {
			record.Stage = "activate"
		}
		return nil
	case "fence":
		return fencePoolExcludedHosts(ctx, s, record)
	case "activate":
		return activatePromotedPoolAuthority(ctx, s, record, timeout)
	case "catalog-rebuild":
		return reconcilePoolBackupCatalogAfterAuthorityAdopt(s, record)
	case "repoint":
		return repointPoolMembers(ctx, s, record, timeout)
	}
	return errors.New("unknown pool promotion stage")
}

// poolPromotionRefusal turns a verdict into the one sentence an operator reads. pending names
// the checks a later stage is allowed to satisfy, and only while they are "unknown" — which
// means "nothing has been fenced yet". A check that is *blocked* is a refusal now, whatever
// its name: that is what stops a promotion fencing a master that is reachable and healthy.
func poolPromotionRefusal(verdict protocol.PoolPromotionVerdict, pending ...string) error {
	for _, check := range verdict.Checks {
		if check.Status == "passed" || (check.Status == "unknown" && slices.Contains(pending, check.Name)) {
			continue
		}
		return errors.New(check.Reason)
	}
	return nil
}

// fencePoolExcludedHosts performs the destructive step for every host this promotion must
// exclude, through the existing native fence mechanism, and persists each proof before
// anything acts on it. A proof that is still fresh and still bound to this promotion is
// reused rather than fencing a host twice.
func fencePoolExcludedHosts(ctx context.Context, s *Store, record *poolTransferRecord) error {
	replica, err := loadPoolReplica(s)
	if err != nil || replica == nil {
		return errors.New("this host holds no pool authority replica; it cannot be promoted")
	}
	generation := replica.Replica.Generation
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return errors.New("pool peer state is unavailable")
	}
	verdict := evaluatePoolPromotion(ctx, s, record.OperationID, record.Fences, 20*time.Second)
	if err = poolPromotionFenceListAgreed(verdict, record); err != nil {
		return err
	}
	for _, hostID := range verdict.FenceHostIDs {
		member, ok := poolMemberByID(replica.Replica.Membership, hostID)
		if !ok {
			return errors.New("pool host " + hostID + " is not a member of this pool; it cannot be excluded")
		}
		// The same decider the operator's verdict was composed from, so the method that
		// runs here is the method readiness promised. Recomputing it from the profile
		// alone made a promotion the preflight approved by watchdog abort on the device
		// path when this host could not open the credential.
		recorded, usable, kind, reason := poolPromotionFenceReadiness(s, state, replica.Replica, member)
		if !recorded || !usable {
			return errors.New("pool host " + hostID + " cannot be excluded: " + reason)
		}
		digest := protocol.PoolPromotionFenceDigest(replica.Replica.PoolID, hostID, record.OperationID, generation)
		if existing := poolPromotionProofFor(record.Fences, hostID); existing != nil &&
			protocol.PoolPromotionFenceVerified(existing, member, replica.Replica.PoolID, hostID, record.OperationID, generation, time.Now().UTC()) == nil {
			continue
		}
		var proof protocol.StorageClusterFenceStatus
		var fenceErr error
		if kind == protocol.PoolFenceWatchdog {
			// The target shut itself out. Nothing is powered off here; this host
			// establishes that the target's own watchdog must have reset it, and is
			// refused — with the time remaining — until it certainly has.
			proof, fenceErr = poolWatchdogFenceVerify(ctx, s, member, record.OperationID, digest, generation)
		} else {
			// The material first: this host fences with the pool's own credential, sealed
			// for it while the master it must now exclude was still there to seal it.
			if err = materializePoolFenceCredential(s, replica.Replica, member, record); err != nil {
				return err
			}
			proof, fenceErr = poolFenceVerify(ctx, s.dir, member.Fence, member.SystemUUID, hostID, record.OperationID, digest, generation)
		}
		record.Fences = append(poolPromotionProofsWithout(record.Fences, hostID), proof)
		record.AuthorityGeneration = generation
		if err = savePoolTransfer(s, record); err != nil {
			return errors.New("the fence proof could not be recorded durably")
		}
		if fenceErr != nil {
			return errors.New(poolFenceRefusal(proof, fenceErr))
		}
		if err = protocol.PoolPromotionFenceVerified(&proof, member, replica.Replica.PoolID, hostID, record.OperationID, generation, time.Now().UTC()); err != nil {
			return errors.New("the fence of pool host " + hostID + " produced no usable proof: " + err.Error())
		}
	}
	record.Stage = "activate"
	return nil
}

// poolPromotionProofsWithout drops any earlier proof for one host, so a re-fence replaces it
// rather than leaving two answers for the same question.
func poolPromotionProofsWithout(proofs []protocol.StorageClusterFenceStatus, hostID string) []protocol.StorageClusterFenceStatus {
	out := proofs[:0:0]
	for _, proof := range proofs {
		if proof.HostID != hostID {
			out = append(out, proof)
		}
	}
	return out
}

// poolPromotionFenceListAgreed refuses to power off a host the operator was not shown. The
// comparison is against Core's own computed list at execution time, not against the list the
// command was composed from, so a pool that changed since the preflight stops the promotion
// instead of quietly fencing something new.
func poolPromotionFenceListAgreed(verdict protocol.PoolPromotionVerdict, record *poolTransferRecord) error {
	required, agreed := append([]string(nil), verdict.FenceHostIDs...), append([]string(nil), record.FenceAgreed...)
	slices.Sort(required)
	slices.Sort(agreed)
	if slices.Equal(required, agreed) {
		return nil
	}
	if len(required) == 0 {
		return errors.New("this promotion no longer needs to power off any host; review the pool and try again")
	}
	return errors.New("this promotion must power off " + strings.Join(required, ", ") +
		", which is not what was confirmed; confirm the hosts this pool reports and try again")
}

func poolFenceRefusal(proof protocol.StorageClusterFenceStatus, err error) string {
	if proof.Error != "" {
		return "the old pool master was not excluded: " + proof.Error
	}
	return "the old pool master was not excluded: " + poolTransferError(err)
}

// activatePromotedPoolAuthority re-proves the whole verdict against live evidence and only
// then adopts the authority at generation+1. The re-proof matters: between the fence and
// this step a member could have come back holding a newer authority, and promoting over it
// would be the second writer everything here exists to prevent.
func activatePromotedPoolAuthority(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) error {
	if err := automaticPoolPromotionFenceEnabled(s, record); err != nil {
		return err
	}
	verdict := evaluatePoolPromotion(ctx, s, record.OperationID, record.Fences, timeout)
	if !verdict.CanPromote {
		// A fence observation expires by design: it proves the old master was off then,
		// not that it remains off.  When that is the only failed proof, return to the
		// durable fence stage for this same operation.  Every other live check remains a
		// refusal; in particular a changed authority, ownership disagreement, or a
		// malformed proof must never cause another host to be fenced.
		if promotionExpiredFenceOnly(s, record, verdict) {
			record.Stage = "fence"
			return nil
		}
		if err := poolPromotionRefusal(verdict); err != nil {
			return err
		}
		return errors.New("this pool promotion is not proved")
	}
	stored, err := loadPoolReplica(s)
	if err != nil || stored == nil {
		return errors.New("this host holds no pool authority replica; it cannot be promoted")
	}
	if err = automaticPoolPromotionFenceEnabled(s, record); err != nil {
		return err
	}
	_, err = adoptPoolAuthorityFromReplica(s, stored.Replica, record)
	return err
}

// promotionExpiredFenceOnly recognizes precisely the retry case where a bound proof was
// valid when observed but has aged out. It deliberately validates the proof at its own
// observation time before allowing a re-fence, so a malformed or differently bound proof
// remains a refusal rather than becoming a reason to power off a host again.
func promotionExpiredFenceOnly(s *Store, record *poolTransferRecord, verdict protocol.PoolPromotionVerdict) bool {
	oldMasterUnknown := false
	for _, check := range verdict.Checks {
		switch check.Name {
		case "old-master-exclusion":
			oldMasterUnknown = check.Status == "unknown"
		case "replica-supremacy":
			// A silent member is eligible for the same already-confirmed fence list.
			// The proof loop below requires a valid or solely expired proof for every
			// such host, so an unproved newly silent member cannot trigger a fence.
			if check.Status != "passed" && check.Status != "unknown" {
				return false
			}
		default:
			if check.Status != "passed" {
				return false
			}
		}
	}
	if !oldMasterUnknown || len(verdict.FenceHostIDs) == 0 {
		return false
	}
	replica, err := loadPoolReplica(s)
	if err != nil || replica == nil {
		return false
	}
	now := time.Now().UTC()
	expired := false
	for _, hostID := range verdict.FenceHostIDs {
		// The host list remains the operator-confirmed destructive scope even on a
		// retry. fencePoolExcludedHosts checks this again immediately before acting;
		// keeping it here makes the activate-to-fence transition fail closed too.
		if !slices.Contains(record.FenceAgreed, hostID) {
			return false
		}
		member, ok := poolMemberByID(replica.Replica.Membership, hostID)
		if !ok {
			return false
		}
		proof := poolPromotionProofFor(record.Fences, hostID)
		if protocol.PoolPromotionFenceVerified(proof, member, replica.Replica.PoolID, hostID, record.OperationID, replica.Replica.Generation, now) == nil {
			continue
		}
		if proof == nil || proof.ObservedAt.IsZero() || now.Sub(proof.ObservedAt) <= protocol.PoolPromotionFenceWindow ||
			protocol.PoolPromotionFenceVerified(proof, member, replica.Replica.PoolID, hostID, record.OperationID, replica.Replica.Generation, proof.ObservedAt) != nil {
			return false
		}
		expired = true
	}
	return expired
}

// recordPoolFenceCredential proves the master holds the material for a profile it is about
// to record, bounds it, and moves that profile's replicated credential version only when the
// material actually changed. The digest is kept in this master's own private state; neither
// it nor the material is ever replicated, reported or logged.
func recordPoolFenceCredential(s *Store, state *poolPeerState, hostID string, fence protocol.StorageClusterFence) error {
	files, err := vmhost.ReadPoolFenceCredential(s.dir, fence)
	if err != nil {
		if strings.Contains(err.Error(), "too large") {
			return errors.New("this host's fence credential material for " + hostID + " is larger than this pool replicates; shorten it before recording the profile")
		}
		return errors.New("this host holds no fence credential for " + hostID + "; deliver it to this host before recording the profile")
	}
	digest := vmhost.PoolFenceCredentialDigest(files)
	if state.FenceCredentials == nil {
		state.FenceCredentials = map[string]poolFenceCredentialRecord{}
	}
	current, ok := state.FenceCredentials[hostID]
	if ok && current.Ref == fence.CredentialRef && current.Digest == digest {
		return nil // The same material, re-recorded: nothing in the pool changes.
	}
	state.FenceCredentials[hostID] = poolFenceCredentialRecord{Ref: fence.CredentialRef, Digest: digest, Version: current.Version + 1}
	return nil
}

// retirePoolFenceCredential removes the material a profile named, once nothing else on this
// host names it. A failure is not fatal to the record: the profile is gone from the pool
// either way, and a file left behind is inert rather than silently retried.
func retirePoolFenceCredential(s *Store, state poolPeerState, previous *protocol.StorageClusterFence) {
	if previous == nil || previous.CredentialRef == "" {
		return
	}
	for _, member := range state.Membership.Members {
		// Never retire material a profile that is still recorded uses. state carries the
		// membership as it will be written, so the cleared profile is already gone from it.
		if member.Fence != nil && member.Fence.CredentialRef == previous.CredentialRef {
			return
		}
	}
	// Clustered storage fences out of the same private store. A reference both features
	// name belongs to both, and retiring a pool profile must never disarm a storage
	// cluster's fencing.
	if clusters, err := loadStorageClusters(s); err == nil {
		for _, operation := range clusters.Operations {
			for _, node := range operation.Config.Nodes {
				if node.Fence.CredentialRef == previous.CredentialRef {
					return
				}
			}
		}
	} else {
		return // Unreadable cluster state is not evidence that nothing needs this material.
	}
	_ = vmhost.RetirePoolFenceCredential(s.dir, previous.CredentialRef)
}

// pinBMCFenceProfile captures the certificate the management controller presents now, pins
// the profile to it, and then proves over that pinned connection that the controller really
// manages this host's machine. Nothing is recorded unless both succeed, so a recorded BMC
// profile has been used once, against the right machine, before anyone depends on it.
func pinBMCFenceProfile(s *Store, fence protocol.StorageClusterFence, hostID string) (protocol.StorageClusterFence, error) {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return fence, errors.New("pool membership is unavailable")
	}
	// Only the master records a profile, so only the master has any business connecting to
	// a management controller and sending it a password. The record path checks this again
	// under the pool lock; checking it here is what keeps a host that is not the master
	// from making the network call at all.
	if state.Membership.MasterHostID != s.State.HostID {
		return fence, errors.New("only the pool master records a fence profile")
	}
	member, ok := poolMemberByID(state.Membership, hostID)
	if !ok {
		return fence, errors.New("no such member of this pool")
	}
	// The same predicate the record path applies, asked first so a profile that could never
	// work costs no network round trip and no operator wait.
	if err = vmhost.PoolFenceUsable(&fence, member.SystemUUID); err != nil {
		return fence, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if fence.Kind == protocol.FenceRedfish {
		// IPMI authenticates its own session and presents no certificate, so only a
		// Redfish controller has one to pin.
		if fence.TLSFingerprint, err = vmhost.BMCCertificateFingerprint(ctx, fence.Endpoint); err != nil {
			return fence, err
		}
	}
	if fence.Kind == protocol.FenceIPMI && fence.Cipher == 0 {
		// An operator is not asked which RMCP+ cipher suite their controller speaks, so
		// Core finds out now, while it is connecting anyway, and records the answer: 17
		// where the controller offers it, otherwise the suite every IPMI 2.0 controller
		// must implement. Suite 0 — no authentication at all — is never among them.
		for _, cipher := range []uint8{17, 3} {
			fence.Cipher = cipher
			if err = vmhost.VerifyBMCTargetIdentity(ctx, s.dir, fence, member.SystemUUID); err == nil {
				return fence, nil
			}
		}
		return fence, err
	}
	if err = vmhost.VerifyBMCTargetIdentity(ctx, s.dir, fence, member.SystemUUID); err != nil {
		return fence, err
	}
	return fence, nil
}

// releaseRefusedFenceCredential drops the material the credential handoff delivered for a
// profile this master then refused. Without it every refusal — and a refusal is the normal
// answer for a host that never reported its machine identity — would leave an orphan
// password in the private store with no profile naming it.
func releaseRefusedFenceCredential(s *Store, fence *protocol.StorageClusterFence) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	if state, err := loadPoolPeer(s); err == nil {
		retirePoolFenceCredential(s, state, fence)
	}
}

// ExecutePoolFenceProfile records one host's fence profile in the master's membership. Like
// maintenance it is deliberately outside the generation equality rule: recording how a host
// would be excluded is not a membership change, and bumping the generation for it would
// invalidate every in-flight placement and published inventory in the pool.
func ExecutePoolFenceProfile(s *Store, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	if cmd.Pool == nil {
		result.Error = "fence profile command carries no fence profile request"
		return result
	}
	// The command's PoolCommand is shared with the agent's journal entry for this operation, which a
	// Controller replay is compared against byte for byte. Pinning the certificate below rewrites the
	// fence, so work on a copy: mutating the shared request made the next replay of the same command
	// "reuse the ID with different input" and stopped the whole agent (pool lab 3).
	request := *cmd.Pool
	v := &request
	if v.Action != "fence-profile" {
		result.Error = "fence profile command carries no fence profile request"
		return result
	}
	if err := v.Valid(); err != nil {
		result.Error = err.Error()
		return result
	}
	// A management controller is pinned and proved before it is recorded, which needs the
	// network and must not happen under the pool lock. Refusing here rather than at the
	// moment somebody is about to power a host off is the whole point: an operator learns
	// now, while the master is still there to record a corrected profile.
	if v.Fence != nil && (v.Fence.Kind == protocol.FenceRedfish || v.Fence.Kind == protocol.FenceIPMI) {
		pinned, err := pinBMCFenceProfile(s, *v.Fence, v.MemberHostID)
		if err != nil {
			releaseRefusedFenceCredential(s, v.Fence)
			result.Error = "this fence profile cannot be verified by this host: " + err.Error()
			return result
		}
		v.Fence = &pinned
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil {
		result.Error = "pool membership is unavailable"
		return result
	}
	if state.Membership.MasterHostID != s.State.HostID || v.Membership.MasterHostID != s.State.HostID {
		result.Error = "only the pool master records a fence profile"
		return result
	}
	if err = poolAuthorityWritable(state); err != nil {
		result.Error = err.Error()
		return result
	}
	index := -1
	for i, member := range state.Membership.Members {
		if member.HostID == v.MemberHostID {
			index = i
		}
	}
	if index < 0 {
		result.Error = "no such member of this pool"
		return result
	}
	if state.Membership.Members[index].Status == "ejected" {
		result.Error = "an ejected host has no fence profile"
		return result
	}
	previous := state.Membership.Members[index].Fence
	wasSelfFencePromotable := v.MemberHostID == s.State.HostID && poolSelfFencePromotable(state, s.State.HostID)
	// What an operator entered by hand is what the operator meant. A host that configures
	// its own controller offers its profile on every pass, and without this it would take
	// over a device somebody chose deliberately.
	if v.Fence != nil && v.Fence.ProvisionedByCore && previous != nil && !previous.ProvisionedByCore {
		retirePoolFenceCredential(s, state, v.Fence)
		result.Error = "this host already has a fence device an operator set, so the one it configured for itself was not recorded"
		return result
	}
	if v.Fence != nil && v.Fence.ProvisionedByCore && state.Membership.Members[index].BMCProvisioningDisabled {
		retirePoolFenceCredential(s, state, v.Fence)
		result.Error = "an operator turned automatic provisioning off for this host, so the fence device it configured for itself was not recorded"
		return result
	}
	if v.Fence != nil {
		// A profile this build could never turn into a proof is refused rather than
		// recorded: recording it would promise a recovery path that does not exist, and
		// would restrict this master after every restart for nothing.
		if err = vmhost.PoolFenceUsable(v.Fence, state.Membership.Members[index].SystemUUID); err != nil {
			retirePoolFenceCredential(s, state, v.Fence)
			result.Error = "this fence profile cannot be verified by this host: " + err.Error()
			return result
		}
		// The master must hold the material itself before it promises its members a
		// recovery that depends on it: they receive it sealed in the next replica, and the
		// candidate that one day fences with it can get it from nowhere else.
		if err = recordPoolFenceCredential(s, &state, v.MemberHostID, *v.Fence); err != nil {
			retirePoolFenceCredential(s, state, v.Fence)
			result.Error = err.Error()
			return result
		}
	}
	state.Membership.Members[index].Fence = v.Fence
	if v.Fence == nil {
		// Cleared: the private digest goes with the profile, and the material this host
		// held only for it is retired. That is the whole lifecycle of a fence credential
		// bound to no repository — it exists while a profile names it and not after.
		delete(state.FenceCredentials, v.MemberHostID)
		state.Membership.Members[index].FenceCredentialVersion = 0
		retirePoolFenceCredential(s, state, previous)
		// Clearing a device Nex Core configured is one operator action with one meaning:
		// the account on that host's controller goes too, and the host stops creating
		// another, until the operator says otherwise. The host reads this from the
		// membership it already holds and removes the account itself; nothing else in the
		// pool may write it, so a host cannot withdraw its own fence device.
		if previous != nil && previous.ProvisionedByCore {
			state.Membership.Members[index].BMCProvisioningDisabled = true
		}
	} else {
		state.Membership.Members[index].FenceCredentialVersion = state.FenceCredentials[v.MemberHostID].Version
		if previous != nil && previous.CredentialRef != v.Fence.CredentialRef {
			// Replaced by a profile naming other material: the material the old reference
			// names is retired, the new record and its version stay.
			retirePoolFenceCredential(s, state, previous)
		}
	}
	if v.MemberHostID == s.State.HostID {
		if err = recordPoolSelfFenceExposure(&state, s.State.HostID, wasSelfFencePromotable); err != nil {
			result.Status, result.Error = "needs_attention", "the current boot identity is unavailable"
			return result
		}
	}
	if err = savePoolPeer(s, state); err != nil {
		result.Status, result.Error = "needs_attention", "the fence profile could not be recorded durably"
		return result
	}
	result.Status = "succeeded"
	return result
}
