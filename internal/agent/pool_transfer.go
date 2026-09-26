// SPDX-License-Identifier: AGPL-3.0-only
package agent

// A planned master change and a member eject are one durable, resumable state machine on
// Core, keyed by the Controller's operation ID. The Controller submits one command to the
// current master and displays what the participants report; it drives nothing, holds no
// job, and a transfer finishes across restarts with no Controller connection at all.
//
// Why no instant has two writers, in the order the code enforces it:
//
//  1. The outgoing master persists its freeze in pool-peer.json. Every authority write on
//     that host reads the freeze inside the same lock section that performs the write, so
//     after the freeze is durable no authority write can begin, and any that began has
//     already finished.
//  2. While frozen the authority generation is pinned: the master cannot number an N+1.
//  3. The replica is built after the freeze and takes the VM lock, so a write that slipped
//     in before the freeze is inside it. The target must acknowledge exactly generation N.
//  4. The master persists HandedOff before it issues the pool-CA-signed hand-off for N, and
//     HandedOff refuses every authority write for good, not merely while frozen.
//  5. The target activates only on that signed hand-off and only against a verified replica
//     at exactly N, and it activates at N+1, adopting the authority in its own durable
//     files before it records itself master.
//  6. Members and the outgoing master adopt the new master only from a pool-CA-signed
//     re-point notice at a strictly higher membership generation, never from a claim.
//  7. An abort is decided once, under the target's own lock: after the hand-off it succeeds
//     only if the target durably records that it never activated and never will for that
//     operation. So an abort and an activation cannot both succeed.
//
// A member eject runs the same machine with the master staying master: freeze, remove the
// membership row at the next generation, re-point the remaining members, and tell the
// ejected host to leave. Running guests are never touched by any of this: the freeze
// refuses new placements, ownership changes, starts, membership and registry changes, and
// never a stop, a shutdown or a power-off.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolTransferPath = "/api/v1/pool/transfer"
const poolAuthorityPath = "/api/v1/pool/authority"
const poolTransferCheck = "pool-authority-transfer"

// poolAuthorityFrozenRefusal is the one sentence every frozen authority write carries, so a
// Controller maps it to one retryable conflict without parsing several messages.
const poolAuthorityFrozenRefusal = "pool authority is frozen for a master change; retry when it completes"

// poolAuthorityHandedOffRefusal is permanent for this generation: this host signed its
// authority away and may never write it again.
const poolAuthorityHandedOffRefusal = "pool authority was handed to another host; this host is no longer the pool master"

// poolTransferMaxAttempts bounds retries of a stage, so an unreachable target ends as an
// honest needs_attention instead of a permanent pending.
// ponytail: fixed attempt bound; make it time-based if operators need a deadline.
const poolTransferMaxAttempts = 10

// poolTransferMu single-flights the machine per process: a command and a tick resume must
// not run it at once. It is never held while the stages take poolPeerMu.
var poolTransferMu sync.Mutex

// poolTransferFailSavePeerAfterVMAdopt is set by tests to force the peer-state write that
// completes activation to fail after VM adoption, so rollback is exercised.
var poolTransferFailSavePeerAfterVMAdopt func() error

func poolAuthorityVMSectionMatches(expected json.RawMessage, current map[string]poolVMRecord) (bool, error) {
	encoded, err := json.Marshal(current)
	if err != nil {
		return false, err
	}
	return bytes.Equal(expected, encoded), nil
}

// poolTransferRecord is the durable record in pool-peer.json. Stage names the step still to
// perform, as the staged join does, so a host that froze but has not replicated reports
// "replicate", which is the truth a caller needs.
type poolTransferRecord struct {
	OperationID, Action, Role, TargetHostID string
	Stage, Status, LastError                string
	AuthorityGeneration                     uint64
	MembershipGeneration                    uint64
	Automatic                               bool
	// Frozen refuses authority writes on this host; HandedOff refuses them for good.
	Frozen, HandedOff bool
	// Notice is the signed hand-off this host issued (source) or activated on (target).
	Notice *protocol.PoolAuthorityNotice
	// Fences are this promotion's own proofs that the hosts it excludes are powered off:
	// observed power-offs, each bound to this pool, that host, this operation and this
	// generation. They are durable so an interrupted promotion resumes on proofs that are
	// still fresh, and re-fences rather than assuming anything once they are not.
	Fences []protocol.StorageClusterFenceStatus
	// FenceAgreed is the exact host list the operator confirmed powering off. Core compares
	// it with what its own proof requires at execution time and refuses a mismatch.
	FenceAgreed []string
	// Materialized are the fence credential references this promotion wrote into this host's
	// own private store. A promotion that ends without this host becoming master retires
	// them again: a plaintext copy of another host's key does not outlive the operation that
	// needed it.
	Materialized []string
	// Repointed are the hosts that have acknowledged the new authority, so a resumed
	// re-point contacts only the ones that have not.
	Repointed []string
	Attempts  int
	UpdatedAt time.Time
}

func (r *poolTransferRecord) repointed(host string) bool {
	return slices.Contains(r.Repointed, host)
}

// poolAuthorityWritable is the guard, answered from an already-loaded record so a caller
// that holds the peer lock for its own read-modify-write checks the same bytes it writes.
func poolAuthorityWritable(state poolPeerState) error {
	if record := state.Transfer; record != nil {
		if record.HandedOff {
			return errors.New(poolAuthorityHandedOffRefusal)
		}
		if record.Frozen {
			return errors.New(poolAuthorityFrozenRefusal)
		}
	}
	// A host that cannot prove it holds this pool's current authority writes none of it.
	// This is checked last so a frozen or handed-over transfer still reports its own, more
	// specific sentence.
	if state.Restricted != nil {
		return errors.New(poolRestrictedReason(state.Restricted))
	}
	return nil
}

// requirePoolAuthorityWritable is the same guard for a caller that holds no lock yet. It
// must be called inside the lock section that performs the write, never before it.
func requirePoolAuthorityWritable(s *Store) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return errors.New("pool authority state is unavailable")
	}
	return poolAuthorityWritable(state)
}

// requirePoolVMAuthorityWritable is the guard for the durable VM ownership record. A frozen
// pool refuses exactly the writes that grant something new — a placement, an ownership
// transition and a start permit — and a host that has handed its authority away refuses
// every one of them. Running guests are untouched: stop, shutdown, force-stop and the
// transitions that settle work already admitted are never blocked here.
func requirePoolVMAuthorityWritable(s *Store, action string, create bool) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return errors.New("pool authority state is unavailable")
	}
	refusal := poolAuthorityWritable(state)
	if refusal == nil {
		return nil
	}
	if state.Transfer != nil && state.Transfer.HandedOff {
		return refusal
	}
	if action == "update-reserve" || action == "snapshot-reserve" || action == "snapshot-metadata" || action == "delete-check" || action == "start" || (action == "define" && create) {
		return refusal
	}
	return nil
}

// poolRegistryCommandWritable refuses a Controller-driven pool registry change while this
// host's authority is frozen or handed over. VM commands are not listed here on purpose:
// they are guarded inside the durable ownership record, so a stop, a shutdown or a
// force-stop is never blocked by a master change.
func poolRegistryCommandWritable(s *Store, cmd protocol.Command) error {
	switch cmd.Kind {
	case "storage-register", "storage-update", "storage-detach", "storage-credential-retire",
		"storage-zfs-provision", "network-register", "network-remove", "network-update",
		"media-remove", "iso-import":
		return requirePoolAuthorityWritable(s)
	}
	return nil
}

func loadPoolTransfer(s *Store) (*poolTransferRecord, error) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Transfer == nil {
		return nil, err
	}
	record := *state.Transfer
	return &record, nil
}

func savePoolTransfer(s *Store, record *poolTransferRecord) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	record.UpdatedAt = time.Now().UTC()
	stored := *record
	state.Transfer = &stored
	return savePoolPeer(s, state)
}

func poolTransferTerminal(stage string) bool { return stage == "done" || stage == "aborted" }

// poolTransferCancellable is the one cancellation predicate: cancelPoolTransfer enforces it
// and poolTransferStatus reports it, so what an operator is offered and what Core accepts
// cannot drift. A cancel is allowed only before the transfer's first irreversible durable
// step:
//
//   - master change: until the outgoing master has signed its authority away. In the window
//     where it has, the cancel is still attempted and succeeds only if the target proves,
//     under its own lock, that it never activated.
//   - eject: until the membership without that member is written at the next generation.
//     From the re-point on the removal is durable and the remaining hosts are converging on
//     it; undoing it here would leave an aborted record and a removed member at once.
//   - promotion: never. The candidate powers the old master off, and nothing undoes that.
func poolTransferCancellable(record *poolTransferRecord) bool {
	switch {
	case record == nil || poolTransferTerminal(record.Stage) || record.Role != "source":
		return false
	case record.Action == "eject":
		return record.Stage == "freeze" || record.Stage == "remove"
	default:
		return true
	}
}

// poolTransferForwardOnly is the sentence a cancel gets once the transfer has taken that
// step and can only go forward.
func poolTransferForwardOnly(record *poolTransferRecord) string {
	if record.Role == "candidate" {
		return "this pool promotion has powered the old pool master off; it cannot be cancelled and can only go forward"
	}
	if record.Action != "eject" {
		return "this pool master change has handed authority to another host; it cannot be cancelled and can only go forward"
	}
	return "host " + record.TargetHostID + " is already removed from this pool's membership; the eject can only go forward"
}

// poolTransferStatus is the honest projection of a stage. A host that handed its authority
// away but has not demoted is needs_attention: the transfer is neither undone nor complete.
func poolTransferStatus(record *poolTransferRecord) protocol.PoolTransferStatus {
	status := protocol.PoolTransferStatus{
		OperationID: record.OperationID, Action: record.Action, Role: record.Role,
		TargetHostID: record.TargetHostID, Stage: record.Stage, Error: record.LastError,
		AuthorityGeneration: record.AuthorityGeneration, Frozen: record.Frozen,
		Cancellable: poolTransferCancellable(record), HandedOff: record.HandedOff,
		Automatic:  record.Automatic,
		ObservedAt: time.Now().UTC(),
	}
	switch {
	case record.Stage == "done":
		status.Status = "succeeded"
	case record.Stage == "aborted":
		status.Status = "failed"
	case record.HandedOff || record.Attempts >= poolTransferMaxAttempts:
		status.Status = "needs_attention"
	case record.LastError != "" && (record.Stage == "freeze" || record.Stage == "preflight"):
		// A refusal at the freeze is a decision, not a transient error: nothing moved.
		status.Status = "failed"
	default:
		status.Status = "pending"
	}
	return status
}

// PoolTransferReport is this host's own part of a transfer, for inventory and for the
// host-local pool view. It carries no notice, no key material and no credential.
func PoolTransferReport(s *Store) *protocol.PoolTransferStatus {
	record, err := loadPoolTransfer(s)
	if err != nil || record == nil {
		return nil
	}
	status := poolTransferStatus(record)
	return &status
}

// PoolTransferCheck reports the current transfer state. A completed transfer emits an
// explicit healthy sample so inventory consumers replace any earlier failed or pending
// observation instead of retaining it when this host has recovered.
func PoolTransferCheck(s *Store) []protocol.Check {
	record, err := loadPoolTransfer(s)
	if err != nil {
		return nil
	}
	if record == nil {
		return []protocol.Check{{Name: poolTransferCheck, Status: "healthy", Freshness: "fresh", ObservedAt: time.Now().UTC()}}
	}
	status := poolTransferStatus(record)
	if status.Status == "succeeded" {
		return []protocol.Check{{Name: poolTransferCheck, Status: "healthy", Freshness: "fresh", ObservedAt: status.ObservedAt}}
	}
	check := protocol.Check{Name: poolTransferCheck, Status: "unknown", Freshness: "fresh", ObservedAt: status.ObservedAt, Error: status.Error}
	switch status.Status {
	case "failed":
		check.Status = "failed"
	case "needs_attention":
		check.Status = "failed"
		if check.Error == "" {
			check.Error = "a pool master change is incomplete on this host"
		}
	default:
		if check.Error == "" {
			check.Error = "a pool authority transfer is in progress"
		}
	}
	return []protocol.Check{check}
}

// signPoolAuthorityNotice signs with the pool CA key this host holds. Only a host that is
// the pool master holds it, which is what makes a notice evidence rather than a claim.
func signPoolAuthorityNotice(caKeyPEM string, notice *protocol.PoolAuthorityNotice) error {
	signer, err := parsePoolCAKey(caKeyPEM)
	if err != nil {
		return err
	}
	notice.Signature = ""
	sig, err := signPoolCADigest(signer, notice.SigningBytes)
	if err != nil {
		return err
	}
	notice.Signature = sig
	return nil
}

func verifyPoolAuthorityNotice(caPEM string, notice protocol.PoolAuthorityNotice) error {
	if err := notice.Valid(); err != nil {
		return err
	}
	return verifyPoolCASignature(caPEM, notice.Signature, notice.SigningBytes, "pool authority notice is not signed by this pool's CA")
}

func poolTransferError(err error) string {
	return boundPoolPeerText(240, "pool authority transfer refused", err.Error())
}

// nextPoolMembership advances a membership by one generation, keeping every identity,
// name, certificate and peer origin byte for byte. A master change moves exactly one
// field: which member is the master.
func nextPoolMembership(current protocol.PoolMembership, masterHostID string, eject string) protocol.PoolMembership {
	next := current
	next.MasterHostID = masterHostID
	next.Generation = current.Generation + 1
	next.Members = append([]protocol.PoolMember(nil), current.Members...)
	for i := range next.Members {
		next.Members[i].Generation = next.Generation
		if eject != "" && next.Members[i].HostID == eject {
			next.Members[i].Status = "ejected"
			next.Members[i].Maintenance, next.Members[i].MaintenanceSince = false, time.Time{}
		}
		if next.Members[i].HostID == masterHostID {
			next.Members[i].Status = "ready"
		}
	}
	return next
}

func poolMemberByID(membership protocol.PoolMembership, hostID string) (protocol.PoolMember, bool) {
	for _, member := range membership.Members {
		if member.HostID == hostID {
			return member, true
		}
	}
	return protocol.PoolMember{}, false
}

// poolTransferClient opens an mTLS connection to one named host. A member reaches its own
// master through the existing pinned client; every other direction pins the certificate to
// the exact membership record this host already holds, so a peer origin can never introduce
// a host, and a host whose row is marked ejected can still be told that it was.
func poolTransferClient(s *Store, origin, hostID string) (*http.Client, error) {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return nil, err
	}
	if state.Membership.Valid() != nil {
		return nil, errors.New("pool membership is unavailable")
	}
	if hostID == state.Membership.MasterHostID && hostID != s.State.HostID {
		return PoolPeerClient(s, origin)
	}
	if _, err = peerOrigin(origin); err != nil {
		return nil, err
	}
	member, ok := poolMemberByID(state.Membership, hostID)
	if !ok || member.CertificateSHA256 == "" {
		return nil, errors.New("peer is not a current membership record")
	}
	return poolMTLSClient(state, hostID, member.CertificateSHA256)
}

func poolTransferRPC(ctx context.Context, s *Store, origin, hostID string, request protocol.PoolTransferRequest, timeout time.Duration) (protocol.PoolTransferReply, error) {
	var reply protocol.PoolTransferReply
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client, err := poolTransferClient(s, origin, hostID)
	if err != nil {
		return reply, err
	}
	defer client.CloseIdleConnections()
	work, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(request)
	if err != nil {
		return reply, err
	}
	req, err := http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(origin, "/")+poolTransferPath, bytes.NewReader(body))
	if err != nil {
		return reply, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return reply, errors.New("pool peer " + hostID + " is unreachable for this transfer step")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return reply, errors.New("pool peer " + hostID + " does not support " + protocol.PoolAuthorityTransferCapability + "; upgrade it before changing the pool master")
	}
	if json.NewDecoder(io.LimitReader(response.Body, protocol.PoolTransferLimit)).Decode(&reply) != nil {
		return reply, errors.New("pool peer " + hostID + " did not answer this transfer step")
	}
	if !reply.Accepted {
		if reply.Error != "" {
			return reply, errors.New(reply.Error)
		}
		return reply, errors.New("pool peer " + hostID + " refused this transfer step")
	}
	return reply, nil
}

// probePoolAuthority is the read-only authority probe. It is the one call a host makes to
// learn where authority actually is: the replicate stage proves the target's generation
// with it, an abort proves the target never activated with it, and a returning or
// superseded host discovers the current master with it.
func probePoolAuthority(ctx context.Context, s *Store, origin, hostID string, timeout time.Duration) (protocol.PoolAuthorityStatus, error) {
	return probePoolAuthorityQuery(ctx, s, origin, hostID, timeout, "")
}

// probePoolAuthorityVMs additionally asks the host for its own view of the VMs it defines.
// It is the accepted-ownership evidence a promotion candidate needs from a reachable owner:
// that host's own record, not the candidate's replica repeated back to itself.
func probePoolAuthorityVMs(ctx context.Context, s *Store, origin, hostID string, timeout time.Duration) (protocol.PoolAuthorityStatus, error) {
	return probePoolAuthorityQuery(ctx, s, origin, hostID, timeout, "?vms=1")
}

func probePoolAuthorityQuery(ctx context.Context, s *Store, origin, hostID string, timeout time.Duration, query string) (protocol.PoolAuthorityStatus, error) {
	var status protocol.PoolAuthorityStatus
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client, err := poolTransferClient(s, origin, hostID)
	if err != nil {
		return status, err
	}
	defer client.CloseIdleConnections()
	work, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(work, http.MethodGet, strings.TrimRight(origin, "/")+poolAuthorityPath+query, nil)
	if err != nil {
		return status, err
	}
	response, err := client.Do(req)
	if err != nil {
		return status, errors.New("pool peer " + hostID + " is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return status, errors.New("pool peer " + hostID + " does not support " + protocol.PoolAuthorityTransferCapability + "; upgrade it before changing the pool master")
	}
	if response.StatusCode != http.StatusOK {
		return status, errors.New("pool peer " + hostID + " refused the authority probe")
	}
	if json.NewDecoder(io.LimitReader(response.Body, protocol.PoolTransferLimit)).Decode(&status) != nil {
		return status, errors.New("pool peer " + hostID + " did not report its authority")
	}
	if status.HostID != hostID {
		return status, errors.New("pool peer reported another host's authority")
	}
	return status, nil
}

// PoolAuthorityHandler answers the read-only probe to any holder of a certificate this
// pool's CA issued, including one this pool has ejected: that host must be able to learn it
// is no longer a member, and it can only learn that from the pool that ejected it.
func PoolAuthorityHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "pool peer certificate is required", http.StatusUnauthorized)
			return
		}
		caller := r.TLS.PeerCertificates[0].Subject.CommonName
		callerHash := fmt.Sprintf("%x", sha256.Sum256(r.TLS.PeerCertificates[0].Raw))
		poolPeerMu.Lock()
		state, err := loadPoolPeer(s)
		poolPeerMu.Unlock()
		if err != nil || state.Membership.Valid() != nil {
			http.Error(w, "pool membership is unavailable", http.StatusConflict)
			return
		}
		status := protocol.PoolAuthorityStatus{
			HostID: s.State.HostID, PoolID: state.Membership.PoolID, Role: "member",
			MasterHostID: state.Membership.MasterHostID, AuthorityGeneration: state.AuthorityGeneration,
			MembershipGeneration: state.Membership.Generation, ObservedAt: time.Now().UTC(),
		}
		if master, ok := poolMemberByID(state.Membership, state.Membership.MasterHostID); ok {
			status.MasterPeerOrigin = master.PeerOrigin
		}
		if state.Membership.MasterHostID == s.State.HostID {
			status.Role = "master"
		}
		if record := state.Transfer; record != nil {
			status.Frozen, status.HandedOff = record.Frozen, record.HandedOff
			reported := poolTransferStatus(record)
			status.Transfer = &reported
		}
		if replica, replicaErr := loadPoolReplica(s); replicaErr == nil && replica != nil {
			status.ReplicaGeneration, status.ReplicaVerified = replica.Replica.Generation, replica.Verified
		}
		// A restricted host says so to every peer that asks. A promotion candidate needs
		// it: a host that answers the probe but refuses to act on its own authority is not
		// evidence that authority is where it says it is.
		status.Restricted = poolRestrictedStatus(state.Restricted)
		if r.URL.Query().Get("vms") == "1" {
			status.VMs, status.VMsReported = localPoolVMOwnership(r.Context(), s)
		}
		// The caller's own membership status, decided from the certificate the transport
		// proved, not from a name in a body. This is how an ejected host learns it is no
		// longer a member: its certificate is no longer pinned to a ready record.
		if member, ok := poolMemberByID(state.Membership, caller); ok && member.CertificateSHA256 == callerHash {
			status.CallerStatus = member.Status
			// A master answers a member that is behind with a signed re-point notice, so
			// a host that missed the push converges from the master it can reach without
			// having to trust anything a stale host says.
			// A host that refuses to write its own authority also refuses to issue signed
			// statements that move other hosts: a restricted or handed-over master is
			// exactly the host whose view of the pool may be the stale one.
			handedOff := state.Transfer != nil && state.Transfer.HandedOff
			if status.Role == "master" && state.CAKeyPEM != "" && member.Status == "ready" && state.Restricted == nil && !handedOff {
				notice := protocol.PoolAuthorityNotice{
					PoolID: state.Membership.PoolID, OperationID: poolRepointOperation(state), Action: "promote",
					FromHostID: s.State.HostID, ToHostID: caller, TargetHostID: s.State.HostID,
					AuthorityGeneration: state.AuthorityGeneration, Membership: state.Membership, IssuedAt: time.Now().UTC(),
				}
				if notice.AuthorityGeneration == 0 {
					notice.AuthorityGeneration = 1
				}
				if signPoolAuthorityNotice(state.CAKeyPEM, &notice) == nil {
					status.Notice = &notice
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})
}

// poolRepointOperation names the notice a probe answers with. A converging host has no
// operation of its own to quote, and the notice is evidence because it is signed and
// higher, not because of its name.
func poolRepointOperation(state poolPeerState) string {
	if state.Transfer != nil && state.Transfer.OperationID != "" {
		return state.Transfer.OperationID
	}
	return "pool-authority-" + state.Membership.PoolID
}

// PoolTransferHandler performs one step of a transfer on the host it addresses. activate,
// abort and leave are accepted only from the current pinned master; repoint is accepted
// from any current member, because the host issuing it has just become the master and this
// host does not know that yet — which is exactly what the signed notice proves.
func PoolTransferHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request protocol.PoolTransferRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.PoolTransferLimit))
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Valid() != nil {
			http.Error(w, "invalid pool transfer request", http.StatusBadRequest)
			return
		}
		caller, err := authenticatePoolTransferPeer(s, r, request.Action)
		if err != nil {
			http.Error(w, "pool transfer peer rejected", http.StatusUnauthorized)
			return
		}
		var reply protocol.PoolTransferReply
		switch request.Action {
		case "activate":
			reply, err = activatePoolAuthority(s, caller, request)
		case "abort":
			reply, err = abortPoolAuthorityTarget(s, request)
		case "repoint":
			reply, err = applyPoolRepoint(s, caller, request)
		case "leave":
			reply, err = recordPoolLeave(s, caller, request)
		default:
			err = errors.New("invalid pool transfer action")
		}
		reply.HostID, reply.OperationID, reply.Action = s.State.HostID, request.OperationID, request.Action
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			reply.Accepted, reply.Error = false, poolTransferError(err)
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(reply)
			return
		}
		reply.Accepted = true
		_ = json.NewEncoder(w).Encode(reply)
	})
}

// authenticatePoolTransferPeer admits the pinned master for the master-issued actions and
// any current ready member for a re-point. Both end at the same place: the notice must be
// signed by the pool CA, and the handler checks that separately.
func authenticatePoolTransferPeer(s *Store, r *http.Request, action string) (protocol.PoolMember, error) {
	if action == "repoint" {
		if r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			return protocol.PoolMember{}, errors.New("pool peer client certificate is required")
		}
		poolPeerMu.Lock()
		state, err := loadPoolPeer(s)
		poolPeerMu.Unlock()
		if err != nil || state.Membership.Valid() != nil {
			return protocol.PoolMember{}, errors.New("pool membership is unavailable")
		}
		leaf := r.TLS.PeerCertificates[0]
		hash := fmt.Sprintf("%x", sha256.Sum256(leaf.Raw))
		member, ok := poolMemberByID(state.Membership, leaf.Subject.CommonName)
		if !ok || member.CertificateSHA256 != hash || member.Status != "ready" {
			return protocol.PoolMember{}, errors.New("pool peer is not an admitted member")
		}
		return member, nil
	}
	return AuthenticatePoolMaster(s, r)
}

// activatePoolAuthority is the target's step 4. It is idempotent by operation ID: a host
// that already adopted the authority answers the same way, so a retry after a lost reply
// never activates twice and never activates a second generation.
func activatePoolAuthority(s *Store, master protocol.PoolMember, request protocol.PoolTransferRequest) (protocol.PoolTransferReply, error) {
	var reply protocol.PoolTransferReply
	notice := request.Notice
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil || state.CAPEM == "" {
		return reply, errors.New("pool membership is unavailable")
	}
	if record := state.Transfer; record != nil && record.OperationID == request.OperationID && record.Role == "target" {
		if record.Stage == "aborted" {
			return reply, errors.New("this pool master change was aborted on this host")
		}
		if state.Membership.MasterHostID == s.State.HostID {
			// Already activated. Answer with the same signed re-point notice.
			repoint, noticeErr := poolRepointNotice(s, state, record)
			if noticeErr != nil {
				return reply, noticeErr
			}
			reply.Activated, reply.AuthorityGeneration = true, state.AuthorityGeneration
			reply.MembershipGeneration, reply.Notice = state.Membership.Generation, repoint
			return reply, nil
		}
	}
	if state.Membership.MasterHostID == s.State.HostID {
		return reply, errors.New("this host is already the master of its pool")
	}
	if notice.Action != "promote" || notice.PoolID != state.Membership.PoolID || notice.ToHostID != s.State.HostID || notice.TargetHostID != s.State.HostID {
		return reply, errors.New("pool master change notice does not address this host")
	}
	if notice.FromHostID != state.Membership.MasterHostID || notice.FromHostID != master.HostID {
		return reply, errors.New("pool master change notice is not from the current pool master")
	}
	if err = verifyPoolAuthorityNotice(state.CAPEM, notice); err != nil {
		return reply, err
	}
	// The authority this host adopts is the replica it already holds and verified, at
	// exactly the generation the outgoing master froze and signed away. A replica that is
	// missing, unverified or at another generation blocks the activation; nothing about
	// this step recomputes or invents authority.
	replica, err := loadPoolReplica(s)
	if err != nil || replica == nil {
		return reply, errors.New("this host holds no pool authority replica to activate")
	}
	if !replica.Verified || replica.Replica.Generation != notice.AuthorityGeneration {
		return reply, errors.New("this host's pool authority replica is not the generation being handed over")
	}
	if replica.Replica.PoolID != state.Membership.PoolID || replica.Replica.MasterHostID != notice.FromHostID || replica.Replica.MemberHostID != s.State.HostID {
		return reply, errors.New("this host's pool authority replica names another pool, master or member")
	}
	if err = verifyPoolAuthorityReplica(state.CAPEM, replica.Replica); err != nil {
		return reply, err
	}
	if err = replica.Replica.Valid(); err != nil {
		return reply, err
	}
	record := &poolTransferRecord{
		OperationID: request.OperationID, Action: "promote", Role: "target", TargetHostID: s.State.HostID,
		Stage: "activate", AuthorityGeneration: notice.AuthorityGeneration, Notice: &notice,
	}
	repoint, err := adoptPoolAuthorityFromReplica(s, replica.Replica, record)
	if err != nil {
		return reply, err
	}
	reply.Activated, reply.AuthorityGeneration = true, notice.AuthorityGeneration+1
	reply.MembershipGeneration, reply.Notice = record.MembershipGeneration, repoint
	return reply, nil
}

// adoptPoolAuthorityFromReplica is the one activation path, used by a planned master change
// and by a fence-verified promotion alike. It opens the pool CA key the replica sealed for
// this host, adopts the replicated VM ownership record, and only then makes this host the
// master at generation+1, in one atomic write. The caller has already established this
// host's right to do it: a signed hand-off from the outgoing master, or a verified fence
// proof that the outgoing master is excluded.
func adoptPoolAuthorityFromReplica(s *Store, replica protocol.PoolAuthorityReplica, record *poolTransferRecord) (*protocol.PoolAuthorityNotice, error) {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		return nil, errors.New("pool membership is unavailable")
	}
	caKeyPEM, err := unwrapPoolCAKey(state.KeyPEM, replica)
	if err != nil {
		return nil, err
	}
	if _, err = parsePoolCAKey(caKeyPEM); err != nil {
		return nil, errors.New("the wrapped pool CA key is unusable on this host")
	}
	membership := nextPoolMembership(replica.Membership, s.State.HostID, "")
	if record.Action == "promote" && record.Role == "candidate" {
		if record.TargetHostID != s.State.HostID || record.AuthorityGeneration != replica.Generation {
			return nil, errors.New("promotion fence proofs do not match the authority being adopted")
		}
		for _, hostID := range record.FenceAgreed {
			member, ok := poolMemberByID(replica.Membership, hostID)
			proof := poolPromotionProofFor(record.Fences, hostID)
			if !ok || protocol.PoolPromotionFenceVerified(proof, member, replica.PoolID, hostID, record.OperationID, replica.Generation, time.Now().UTC()) != nil {
				return nil, errors.New("promotion cannot exclude a member without its exact current fence proof")
			}
			for i := range membership.Members {
				if membership.Members[i].HostID == hostID {
					membership.Members[i].ReplicaAckExcluded = true
					break
				}
			}
		}
	}
	if err = membership.Valid(); err != nil {
		return nil, errors.New("the replicated membership cannot name this host as master")
	}
	record.AuthorityGeneration, record.MembershipGeneration = replica.Generation, membership.Generation
	// Recorded before anything is adopted, so a crash mid-activation resumes here instead
	// of leaving a host that cannot say what it was doing.
	if err = savePoolTransfer(s, record); err != nil {
		return nil, errors.New("the pool master change could not be recorded durably")
	}
	poolVMMu.Lock()
	vmSnapshot, snapErr := loadPoolVMs(s)
	poolVMMu.Unlock()
	if snapErr != nil {
		return nil, errors.New("this host's VM execution state is unavailable")
	}
	// The VM ownership record is adopted before this host records itself master, so it is
	// never master without knowing who owns which guest. Adoption is idempotent and the
	// published inventory snapshots are this host's own observations, which are kept.
	if err = adoptPoolVMAuthority(s, replica); err != nil {
		return nil, err
	}
	poolVMMu.Lock()
	poolPeerMu.Lock()
	state, err = loadPoolPeer(s)
	var rollbackResourceMutations func()
	if err == nil {
		state.CAKeyPEM, state.Membership = caKeyPEM, membership
		state.AuthorityGeneration, state.AuthorityDigest = replica.Generation+1, ""
		state.Offers = map[string]poolJoinOffer{}
		// This host has just proved, by the accepted hand-off or fence evidence, that it
		// holds this pool's authority. Bind that proof to this boot in the same write that
		// makes it master, so libvirt's hook cannot inherit a former member's marker.
		bootID, bootErr := poolBootAuthorityID()
		if bootErr != nil {
			err = bootErr
		} else {
			state.Restricted = nil
			state.BootAuthority = &poolBootAuthorityConfirmation{PoolID: membership.PoolID, HostID: s.State.HostID, BootID: bootID}
		}
		if err == nil {
			if self, ok := poolMemberByID(membership, s.State.HostID); ok && self.PeerOrigin != "" {
				state.MasterURL = self.PeerOrigin
			}
			record.Stage = "repoint"
			record.UpdatedAt = time.Now().UTC()
			stored := *record
			state.Transfer = &stored
			// One atomic write makes this host the master: the CA key, the membership and the
			// new generation land together or not at all.
			if poolTransferFailSavePeerAfterVMAdopt != nil {
				err = poolTransferFailSavePeerAfterVMAdopt()
			}
			if err == nil {
				rollbackResourceMutations, err = adoptPoolResourceMutations(s, replica.ResourceMutations)
			}
			if err == nil {
				err = savePoolPeer(s, state)
			}
		}
	}
	poolPeerMu.Unlock()
	poolVMMu.Unlock()
	if err != nil {
		if rollbackResourceMutations != nil {
			rollbackResourceMutations()
		}
		poolVMMu.Lock()
		restoreErr := savePoolVMs(s, vmSnapshot)
		poolVMMu.Unlock()
		if restoreErr != nil {
			return nil, errors.New("the pool master change could not be recorded durably and the VM ownership snapshot could not be restored")
		}
		return nil, errors.New("the pool master change could not be recorded durably")
	}
	return poolRepointNotice(s, state, record)
}

func adoptPoolResourceMutations(s *Store, mutations []protocol.PoolResourceMutation) (func(), error) {
	if err := protocol.ValidatePoolResourceMutations(mutations); err != nil {
		return nil, errors.New("replicated pool resource mutations are invalid")
	}
	poolStorageMu.Lock()
	state, err := loadPoolStorage(s)
	if err != nil {
		poolStorageMu.Unlock()
		return nil, errors.New("pool resource mutation state is unavailable")
	}
	previous := make(map[string]string)
	for key, operation := range state.Mutations {
		if strings.HasPrefix(key, poolResourceMutationPrefix) {
			previous[key] = operation
			delete(state.Mutations, key)
		}
	}
	for _, mutation := range mutations {
		state.Mutations[poolResourceMutationKey(mutation.Resource, mutation.ResourceID)] = mutation.OperationID
	}
	err = savePoolStorage(s, state)
	poolStorageMu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() {
		poolStorageMu.Lock()
		defer poolStorageMu.Unlock()
		current, loadErr := loadPoolStorage(s)
		if loadErr != nil {
			return
		}
		for key := range current.Mutations {
			if strings.HasPrefix(key, poolResourceMutationPrefix) {
				delete(current.Mutations, key)
			}
		}
		for key, operation := range previous {
			current.Mutations[key] = operation
		}
		_ = savePoolStorage(s, current)
	}, nil
}

// poolRepointNotice is the new master's signed statement of the new membership. Every other
// host adopts the master change from this and nothing else.
func poolRepointNotice(s *Store, state poolPeerState, record *poolTransferRecord) (*protocol.PoolAuthorityNotice, error) {
	if state.CAKeyPEM == "" || state.Membership.MasterHostID != s.State.HostID {
		return nil, errors.New("only the pool master signs a re-point notice")
	}
	notice := protocol.PoolAuthorityNotice{
		PoolID: state.Membership.PoolID, OperationID: record.OperationID, Action: record.Action,
		FromHostID: s.State.HostID, ToHostID: s.State.HostID, TargetHostID: record.TargetHostID,
		AuthorityGeneration: state.AuthorityGeneration, Membership: state.Membership, IssuedAt: time.Now().UTC(),
	}
	if err := signPoolAuthorityNotice(state.CAKeyPEM, &notice); err != nil {
		return nil, err
	}
	return &notice, nil
}

// adoptPoolVMAuthority writes the replicated VM ownership record into this host's own
// pool-vms.json, byte for byte: every VM ID, owner, epoch, spec revision and pending
// update is preserved. The per-member published inventory snapshots are observations this
// host collected and keeps; a promoted master re-collects the rest (BUG-0158).
func adoptPoolVMAuthority(s *Store, replica protocol.PoolAuthorityReplica) error {
	var vms map[string]poolVMRecord
	if json.Unmarshal(replica.VMs, &vms) != nil {
		return errors.New("the replicated VM ownership record is unreadable")
	}
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		return errors.New("this host's VM execution state is unavailable")
	}
	if state.VMs == nil {
		state.VMs = map[string]poolVMRecord{}
	}
	for id, record := range vms {
		state.VMs[id] = record
	}
	if err = savePoolVMs(s, state); err != nil {
		return errors.New("the replicated VM ownership record could not be stored")
	}
	return nil
}

// abortPoolAuthorityTarget is the target's half of the abort rule. It is one decision under
// this host's own lock: either it has already adopted the authority, and says so, or it
// records that it never will for this operation.
func abortPoolAuthorityTarget(s *Store, request protocol.PoolTransferRequest) (protocol.PoolTransferReply, error) {
	var reply protocol.PoolTransferReply
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil {
		return reply, errors.New("pool membership is unavailable")
	}
	if state.Membership.MasterHostID == s.State.HostID {
		reply.Activated, reply.AuthorityGeneration = true, state.AuthorityGeneration
		reply.MembershipGeneration = state.Membership.Generation
		return reply, errors.New("this host has already activated the pool authority; the master change can only go forward")
	}
	record := state.Transfer
	if record == nil || record.OperationID != request.OperationID {
		record = &poolTransferRecord{OperationID: request.OperationID, Action: "promote", Role: "target", TargetHostID: s.State.HostID}
	}
	record.Stage, record.Status, record.Frozen, record.HandedOff = "aborted", "failed", false, false
	record.UpdatedAt = time.Now().UTC()
	stored := *record
	state.Transfer = &stored
	if err = savePoolPeer(s, state); err != nil {
		return reply, errors.New("the abort could not be recorded durably")
	}
	reply.MembershipGeneration = state.Membership.Generation
	return reply, nil
}

// applyPoolRepoint adopts a new master. It is the only way a host changes which host it
// follows: a pool-CA-signed notice whose membership is at a strictly higher generation and
// names the caller as its master. A lower or equal generation is refused, so a stale master
// can never call a host back.
func applyPoolRepoint(s *Store, caller protocol.PoolMember, request protocol.PoolTransferRequest) (protocol.PoolTransferReply, error) {
	var reply protocol.PoolTransferReply
	notice := request.Notice
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil || state.CAPEM == "" {
		return reply, errors.New("pool membership is unavailable")
	}
	reply.MembershipGeneration = state.Membership.Generation
	// The notice is addressed either to this host or to its sender; the signature covers
	// that field either way, so neither form can be re-addressed in transit.
	if notice.PoolID != state.Membership.PoolID || (notice.ToHostID != s.State.HostID && notice.ToHostID != caller.HostID) {
		return reply, errors.New("pool re-point notice does not address this host")
	}
	if notice.Membership.Valid() != nil || notice.Membership.PoolID != state.Membership.PoolID || notice.Membership.MasterHostID != caller.HostID {
		return reply, errors.New("pool re-point notice does not name its sender as the pool master")
	}
	if err = verifyPoolAuthorityNotice(state.CAPEM, notice); err != nil {
		return reply, err
	}
	if notice.Membership.Generation <= state.Membership.Generation {
		if state.Membership.MasterHostID == caller.HostID {
			// Already following this master at this generation or later.
			reply.AuthorityGeneration = state.AuthorityGeneration
			return reply, nil
		}
		return reply, errors.New("pool re-point notice is not newer than this host's membership")
	}
	if _, ok := poolMemberByID(notice.Membership, s.State.HostID); !ok {
		return reply, errors.New("pool re-point notice does not name this host")
	}
	master, _ := poolMemberByID(notice.Membership, caller.HostID)
	if master.PeerOrigin == "" {
		return reply, errors.New("pool re-point notice carries no master peer origin")
	}
	if _, err = peerOrigin(master.PeerOrigin); err != nil {
		return reply, errors.New("pool re-point notice carries an invalid master peer origin")
	}
	demoted := state.Membership.MasterHostID == s.State.HostID
	// A master that handed its own authority over knows exactly what happened and is not
	// restricted by it. Any other demotion is a host discovering that the pool moved on
	// without it, which is the case that must start nothing until it has reconciled.
	handedOver := state.Transfer != nil && state.Transfer.Role == "source" && state.Transfer.HandedOff && state.Transfer.OperationID == notice.OperationID
	state.Membership, state.MasterURL = notice.Membership, master.PeerOrigin
	if demoted {
		// A demoted master keeps its identity, its certificate and its membership row and
		// gives up exactly one thing: the key that lets it sign this pool's authority.
		state.CAKeyPEM, state.Offers = "", map[string]poolJoinOffer{}
		state.AuthorityGeneration, state.AuthorityDigest = 0, ""
		if !handedOver {
			state.Restricted = supersededPoolRestriction(state.Restricted)
		}
	}
	if record := state.Transfer; record != nil && record.Role == "source" {
		record.Stage, record.Status, record.Frozen, record.HandedOff = "done", "succeeded", false, false
		record.MembershipGeneration, record.LastError = notice.Membership.Generation, ""
		record.UpdatedAt = time.Now().UTC()
	}
	if err = savePoolPeer(s, state); err != nil {
		return reply, errors.New("the new pool master could not be recorded durably")
	}
	reply.MembershipGeneration, reply.AuthorityGeneration = notice.Membership.Generation, state.AuthorityGeneration
	return reply, nil
}

// recordPoolLeave records an eject on the host being ejected. It performs nothing here: the
// re-initialization rewrites this Core's own pool identity, so it runs on the goroutine
// that owns the Store, exactly as a completed join does.
func recordPoolLeave(s *Store, master protocol.PoolMember, request protocol.PoolTransferRequest) (protocol.PoolTransferReply, error) {
	var reply protocol.PoolTransferReply
	notice := request.Notice
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil || state.CAPEM == "" {
		return reply, errors.New("pool membership is unavailable")
	}
	reply.MembershipGeneration = state.Membership.Generation
	if notice.Action != "eject" || notice.PoolID != state.Membership.PoolID || notice.TargetHostID != s.State.HostID || notice.ToHostID != s.State.HostID {
		return reply, errors.New("pool eject notice does not address this host")
	}
	if notice.FromHostID != state.Membership.MasterHostID || notice.FromHostID != master.HostID {
		return reply, errors.New("pool eject notice is not from the current pool master")
	}
	if err = verifyPoolAuthorityNotice(state.CAPEM, notice); err != nil {
		return reply, err
	}
	if notice.Membership.Valid() != nil || notice.Membership.Generation <= state.Membership.Generation {
		return reply, errors.New("pool eject notice is not newer than this host's membership")
	}
	if member, ok := poolMemberByID(notice.Membership, s.State.HostID); ok && member.Status == "ready" {
		return reply, errors.New("pool eject notice still names this host as a ready member")
	}
	if record := state.Transfer; record != nil && record.Role == "ejected" && record.OperationID == request.OperationID {
		return reply, nil
	}
	state.Transfer = &poolTransferRecord{
		OperationID: request.OperationID, Action: "eject", Role: "ejected", TargetHostID: s.State.HostID,
		Stage: "leave", Status: "pending", MembershipGeneration: notice.Membership.Generation,
		Notice: &notice, UpdatedAt: time.Now().UTC(),
	}
	if err = savePoolPeer(s, state); err != nil {
		return reply, errors.New("the eject could not be recorded durably")
	}
	return reply, nil
}

// ExecutePoolTransfer records the Controller's durable transfer command on the current
// master and drives it as far as it gets in this pass. Every later pass resumes it from the
// tick with no Controller at all.
func ExecutePoolTransfer(ctx context.Context, s *Store, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	v := cmd.Pool
	if v == nil || v.Valid() != nil || (v.Action != "master-change" && v.Action != "eject" && v.Action != "transfer-cancel") {
		result.Error = "pool transfer command carries no transfer request"
		return result
	}
	poolTransferMu.Lock()
	defer poolTransferMu.Unlock()
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		result.Error = "pool membership is unavailable"
		return result
	}
	// After activation the old master is deliberately demoted but keeps the exact durable
	// receipt. Return its irreversible result even when the Controller still carries the
	// pre-handoff generation; every cancellation that could still mutate stays behind the
	// normal authority and version guards below.
	if v.Action == "transfer-cancel" && irreversibleMasterChangeReceipt(state.Transfer, v.MemberHostID) {
		result.Error = "the target has already activated this pool authority; the master change can only go forward"
		return result
	}
	if state.Membership.MasterHostID != s.State.HostID || v.Membership.MasterHostID != s.State.HostID {
		result.Error = "only the pool master starts a master change or an eject"
		return result
	}
	if v.ExpectedVersion != 0 && v.ExpectedVersion != state.Membership.Generation {
		result.Error = "pool membership changed since this request was composed; review the pool and try again"
		return result
	}
	if v.Action == "transfer-cancel" {
		status, cancelErr := cancelPoolTransfer(ctx, s, cmd.OperationID, v.MemberHostID, v)
		if cancelErr != nil {
			result.Error = poolTransferError(cancelErr)
			return result
		}
		// The command the Controller asked for — cancel this transfer — succeeded; the
		// transfer itself is over and reports its own terminal stage in inventory. A refused
		// cancellation returns above, with Core's sentence and a failed status, so the two
		// are distinguishable without reading the reason.
		result.Status, result.Error = "succeeded", status.Error
		if status.Status == "pending" {
			result.Status = "needs_attention"
		}
		return result
	}
	record := state.Transfer
	if record != nil && record.OperationID != cmd.OperationID && !poolTransferTerminal(record.Stage) {
		result.Error = "another pool authority transfer is already in progress on this pool"
		return result
	}
	if record == nil || record.OperationID != cmd.OperationID {
		action := "promote"
		if v.Action == "eject" {
			action = "eject"
		}
		record = &poolTransferRecord{OperationID: cmd.OperationID, Action: action, Role: "source", TargetHostID: v.MemberHostID, Stage: "freeze", Status: "pending", Frozen: true}
		if err = savePoolTransfer(s, record); err != nil {
			result.Error = "the pool authority transfer could not be recorded durably"
			return result
		}
	}
	status := advancePoolTransfer(ctx, s, record, 20*time.Second)
	result.Status, result.Error, result.ObservedAt = status.Status, status.Error, status.ObservedAt
	if result.Status == "pending" {
		// The command is terminal for the Controller; the hosts keep working and report
		// their true stage in inventory rather than holding the operation open.
		result.Status = "needs_attention"
	}
	return result
}

func irreversibleMasterChangeReceipt(record *poolTransferRecord, targetHostID string) bool {
	return record != nil && record.Action == "promote" && record.TargetHostID == targetHostID && (record.Role == "target" || (record.Role == "source" && record.Stage == "done"))
}

// cancelPoolTransfer is the abort rule, gated by poolTransferCancellable. Before the
// hand-off a master change simply unfreezes and keeps the current master; in the hand-off
// window it is allowed only when the target proves, under its own lock, that it never
// activated and never will for this operation. An eject whose membership is already written
// and a promotion that has fenced are refused outright: they can only go forward.
func cancelPoolTransfer(ctx context.Context, s *Store, operationID, targetHostID string, fence *protocol.PoolCommand) (protocol.PoolTransferStatus, error) {
	record, err := loadPoolTransfer(s)
	if err != nil {
		return protocol.PoolTransferStatus{}, errors.New("pool authority transfer state is unavailable")
	}
	if record == nil || (record.Role != "source" && record.Role != "candidate" && record.Role != "target") {
		return protocol.PoolTransferStatus{}, errors.New("no pool authority transfer is in progress on this pool")
	}
	if targetHostID != "" && targetHostID != record.TargetHostID {
		return protocol.PoolTransferStatus{}, errors.New("the pool authority transfer in progress has another target")
	}
	if poolTransferTerminal(record.Stage) {
		return protocol.PoolTransferStatus{}, errors.New("no pool authority transfer is in progress on this pool")
	}
	if !poolTransferCancellable(record) {
		return protocol.PoolTransferStatus{}, errors.New(poolTransferForwardOnly(record))
	}
	if record.HandedOff {
		member, err := currentPoolMember(s, record.TargetHostID)
		if err != nil {
			return protocol.PoolTransferStatus{}, err
		}
		reply, err := poolTransferRPC(ctx, s, member.PeerOrigin, member.HostID, protocol.PoolTransferRequest{OperationID: record.OperationID, Action: "abort"}, 20*time.Second)
		if err != nil {
			if reply.Activated {
				return protocol.PoolTransferStatus{}, errors.New("the target has already activated this pool authority; the master change can only go forward")
			}
			// The target did not answer. An unanswered request is not evidence that it never
			// activated, so the only way forward is the evidence everything else here runs
			// on: an observed power-off of that host.
			if resumeErr := resumeAfterFencedPoolTarget(ctx, s, record, member, fence); resumeErr != nil {
				return protocol.PoolTransferStatus{}, resumeErr
			}
			return poolTransferStatus(record), nil
		}
		record.HandedOff = false
	}
	cancelled := "the pool master change was cancelled by an operator"
	if record.Action == "eject" {
		cancelled = "the pool member eject was cancelled by an operator"
	}
	record.Stage, record.Status, record.Frozen, record.LastError = "aborted", "failed", false, cancelled
	if err = savePoolTransfer(s, record); err != nil {
		return protocol.PoolTransferStatus{}, errors.New("the cancellation could not be recorded durably")
	}
	return poolTransferStatus(record), nil
}

// resumeAfterFencedPoolTarget is the one way out of a hand-off whose target is gone. It is
// the same evidence discipline as a promotion, read from the other side:
//
//   - the target must be unreachable and must be excluded by an observed power-off, bound to
//     this pool, that host, this operation and the generation this transfer froze;
//   - the operator must have named that exact host, as every destructive step here requires;
//   - this host must still be the recorded master, still hold the pool CA key, and not be
//     restricted — a host that was itself fenced and rebooted is restricted and refused here,
//     which is what stops a fenced host resuming over the one that fenced it.
//
// It then resumes two generations above the one it froze. That matters: if the target did
// activate before it died it minted exactly N+1 and membership M+1, so resuming at N+2/M+2
// supersedes it everywhere by the same strictly-higher rule every host already applies, and
// the target — which by construction has a recorded usable fence profile, since it was just
// fenced with it — comes back restricted, finds a higher authority naming another master and
// takes the superseded path instead of writing anything.
func resumeAfterFencedPoolTarget(ctx context.Context, s *Store, record *poolTransferRecord, target protocol.PoolMember, fence *protocol.PoolCommand) error {
	if fence == nil || !fence.FenceConfirmed || len(fence.FenceHostIDs) != 1 || fence.FenceHostIDs[0] != target.HostID {
		return errors.New("pool peer " + target.HostID + " did not answer, and this pool's authority stays with it until that host is powered off; confirm powering off " + target.HostID + " to resume this master change's source")
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		return errors.New("pool membership is unavailable")
	}
	if state.Restricted != nil {
		return errors.New(poolRestrictedReason(state.Restricted))
	}
	if state.Membership.MasterHostID != s.State.HostID || state.CAKeyPEM == "" {
		return errors.New("only the host that handed this pool's authority over can resume it")
	}
	if target.Fence == nil {
		return errors.New("pool host " + target.HostID + " has no recorded fence profile, so it cannot be excluded; this pool's authority stays with it")
	}
	// It must really be unreachable. A target that answers is cancelled by its own answer,
	// never by powering it off.
	if status, probeErr := probePoolAuthority(ctx, s, target.PeerOrigin, target.HostID, 10*time.Second); probeErr == nil {
		if status.Transfer != nil && status.Role == "master" {
			return errors.New("the target has already activated this pool authority; the master change can only go forward")
		}
		return errors.New("pool peer " + target.HostID + " is reachable; retry the cancellation rather than powering it off")
	}
	generation := record.AuthorityGeneration
	digest := protocol.PoolPromotionFenceDigest(state.Membership.PoolID, target.HostID, record.OperationID, generation)
	proof, fenceErr := poolFenceVerify(ctx, s.dir, target.Fence, target.SystemUUID, target.HostID, record.OperationID, digest, generation)
	record.Fences = append(poolPromotionProofsWithout(record.Fences, target.HostID), proof)
	if err = savePoolTransfer(s, record); err != nil {
		return errors.New("the fence proof could not be recorded durably")
	}
	if fenceErr != nil {
		return errors.New(poolFenceRefusal(proof, fenceErr))
	}
	if err = protocol.PoolPromotionFenceVerified(&proof, target, state.Membership.PoolID, target.HostID, record.OperationID, generation, time.Now().UTC()); err != nil {
		return errors.New("the fence of pool host " + target.HostID + " produced no usable proof: " + err.Error())
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err = loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil {
		return errors.New("pool membership is unavailable")
	}
	membership := nextPoolMembership(state.Membership, s.State.HostID, "")
	membership.Generation++
	for i := range membership.Members {
		membership.Members[i].Generation = membership.Generation
	}
	if err = membership.Valid(); err != nil {
		return errors.New("the pool membership would be invalid after resuming")
	}
	state.Membership = membership
	state.AuthorityGeneration, state.AuthorityDigest = generation+2, ""
	record.Stage, record.Status, record.Frozen, record.HandedOff = "aborted", "failed", false, false
	record.MembershipGeneration = membership.Generation
	record.LastError = "the target of this master change was powered off and could not take the pool's authority; this host resumed it"
	record.UpdatedAt = time.Now().UTC()
	stored := *record
	state.Transfer = &stored
	// One atomic write ends the hand-off and resumes this host above anything the target
	// could have minted.
	if err = savePoolPeer(s, state); err != nil {
		return errors.New("resuming this pool's authority could not be recorded durably")
	}
	return nil
}

func currentPoolMember(s *Store, hostID string) (protocol.PoolMember, error) {
	membership, err := Membership(s)
	if err != nil {
		return protocol.PoolMember{}, errors.New("pool membership is unavailable")
	}
	member, ok := poolMemberByID(membership, hostID)
	if !ok {
		return protocol.PoolMember{}, errors.New("no such member of this pool")
	}
	if member.PeerOrigin == "" {
		return protocol.PoolMember{}, errors.New("pool member " + hostID + " has not advertised a peer endpoint")
	}
	return member, nil
}

// advancePoolTransfer executes each remaining stage in order, persisting after every one,
// so a crash resumes at the stage that did not complete. Every stage is individually
// idempotent on both hosts.
func advancePoolTransfer(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) protocol.PoolTransferStatus {
	for !poolTransferTerminal(record.Stage) {
		var err error
		switch record.Role {
		case "source":
			err = advancePoolTransferSource(ctx, s, record, timeout)
		case "candidate":
			err = advancePoolPromotion(ctx, s, record, timeout)
		case "target":
			err = advancePoolTransferTarget(ctx, s, record, timeout)
		case "ejected":
			// The re-initialization rewrites this Core's pool identity and runs only on
			// the goroutine that owns the Store; the tick never performs it.
			return poolTransferStatus(record)
		default:
			err = errors.New("unknown pool authority transfer role")
		}
		if err != nil {
			record.Attempts++
			record.LastError = poolTransferError(err)
			break
		}
		record.Attempts, record.LastError = 0, ""
		if saveErr := savePoolTransfer(s, record); saveErr != nil {
			record.Attempts++
			record.LastError = poolTransferError(saveErr)
			break
		}
	}
	if record.Role == "candidate" && poolTransferTerminal(record.Stage) && record.Stage != "done" {
		retireAbandonedPoolFenceCredentials(s, record)
	}
	if (record.Stage == "freeze" || record.Stage == "preflight") && record.LastError != "" {
		// A refusal at the freeze, or at a promotion's preflight, is a decision and nothing
		// moved, so the transfer is over rather than in progress: the pool is not left
		// holding an operation that would block the next one.
		record.Stage, record.Frozen = "aborted", false
	}
	status := poolTransferStatus(record)
	record.Status = status.Status
	if err := savePoolTransfer(s, record); err != nil && status.Status == "succeeded" {
		status.Status, status.Error = "needs_attention", poolTransferError(err)
	}
	return status
}

func advancePoolTransferSource(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) error {
	switch record.Stage {
	case "freeze":
		return freezePoolAuthority(s, record)
	case "replicate":
		return replicatePoolAuthority(ctx, s, record, timeout)
	case "activate":
		return handOffPoolAuthority(ctx, s, record, timeout)
	case "remove":
		return removePoolMembership(s, record)
	case "repoint":
		return repointPoolMembers(ctx, s, record, timeout)
	case "leave":
		return tellPoolMemberToLeave(ctx, s, record, timeout)
	}
	return errors.New("unknown pool authority transfer stage")
}

func advancePoolTransferTarget(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) error {
	if record.Stage == "repoint" {
		return repointPoolMembers(ctx, s, record, timeout)
	}
	// An activation interrupted before this host recorded itself master is retried by the
	// outgoing master, which is the only host that may issue the hand-off.
	return errors.New("awaiting the outgoing pool master")
}

// freezePoolAuthority is step 1. The freeze is durable before anything else happens, and
// the authority generation is numbered and pinned with it, so the outgoing master can
// neither write nor renumber while the transfer runs.
func freezePoolAuthority(s *Store, record *poolTransferRecord) error {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return errors.New("only the pool master freezes pool authority")
	}
	target, ok := poolMemberByID(membership, record.TargetHostID)
	if !ok || record.TargetHostID == s.State.HostID {
		return errors.New("no such member of this pool")
	}
	if record.Action == "promote" && (target.Status != "ready" || target.PeerOrigin == "") {
		return errors.New("only a ready member with a peer endpoint can become the pool master")
	}
	if record.Action == "eject" {
		if err = poolEjectRefusal(s, membership, target); err != nil {
			return err
		}
	}
	// The master's own peer origin is what every member already uses to reach it, and what
	// a new master needs to reach it back. Record it before the authority is numbered, so
	// the number covers the content that is then frozen.
	if err = recordPoolMasterPeerOrigin(s); err != nil {
		return err
	}
	// Number the current authority before pinning it, so the generation the target must
	// acknowledge is the content that is actually frozen.
	digest := ""
	for attempt := 0; attempt < 3; attempt++ {
		var generation uint64
		var replica protocol.PoolAuthorityReplica
		replica, digest, err = buildPoolAuthorityReplica(s)
		if err != nil {
			return err
		}
		if generation, err = advancePoolAuthorityGeneration(s, digest); err != nil {
			return err
		}
		record.AuthorityGeneration = generation
		poolVMMu.Lock()
		vmState, vmErr := loadPoolVMs(s)
		matches, compareErr := poolAuthorityVMSectionMatches(replica.VMs, vmState.VMs)
		if vmErr != nil || compareErr != nil {
			poolVMMu.Unlock()
			return errors.New("pool VM authority is unavailable while freezing")
		}
		if !matches {
			poolVMMu.Unlock()
			continue
		}
		poolPeerMu.Lock()
		state, loadErr := loadPoolPeer(s)
		if loadErr != nil {
			poolPeerMu.Unlock()
			poolVMMu.Unlock()
			return loadErr
		}
		if state.AuthorityDigest != digest || state.AuthorityGeneration != generation {
			poolPeerMu.Unlock()
			poolVMMu.Unlock()
			continue
		}
		poolStorageMu.Lock()
		storage, storageErr := loadPoolStorage(s)
		currentMutations, mutationErr := poolResourceMutationsFromStorage(storage)
		if storageErr != nil || mutationErr != nil {
			poolStorageMu.Unlock()
			poolPeerMu.Unlock()
			poolVMMu.Unlock()
			return errors.New("pool resource mutation state is unavailable while freezing")
		}
		if poolResourceMutationPending(storage) {
			poolStorageMu.Unlock()
			poolPeerMu.Unlock()
			poolVMMu.Unlock()
			return errors.New("pool resource removal outcome is unresolved; finish or reconcile it before changing the master")
		}
		if !slices.Equal(currentMutations, replica.ResourceMutations) {
			poolStorageMu.Unlock()
			poolPeerMu.Unlock()
			poolVMMu.Unlock()
			continue
		}
		record.Frozen, record.MembershipGeneration = true, state.Membership.Generation
		record.Stage = "replicate"
		if record.Action == "eject" {
			record.Stage = "remove"
		}
		record.UpdatedAt = time.Now().UTC()
		stored := *record
		state.Transfer = &stored
		err = savePoolPeer(s, state)
		poolStorageMu.Unlock()
		poolPeerMu.Unlock()
		poolVMMu.Unlock()
		return err
	}
	return errors.New("pool authority changed while it was being frozen; retry the master change")
}

// recordPoolMasterPeerOrigin records the master's own peer origin in its membership row if
// it has none. Nothing advertises it today — the advertise call is member to master — and a
// new master cannot reach its predecessor without it. Like that advertise handler this is a
// recorded observation, not a membership change, so it does not move the generation.
func recordPoolMasterPeerOrigin(s *Store) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil || state.MasterURL == "" {
		return err
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID != s.State.HostID || state.Membership.Members[i].PeerOrigin != "" {
			continue
		}
		if _, originErr := peerOrigin(state.MasterURL); originErr != nil {
			return nil
		}
		state.Membership.Members[i].PeerOrigin = state.MasterURL
		return savePoolPeer(s, state)
	}
	return nil
}

// poolEjectRefusal holds every refusal an eject owes its operator, named from the master's
// own durable records. A host is never ejected while the pool still expects it to run or
// hold anything.
func poolEjectRefusal(s *Store, membership protocol.PoolMembership, target protocol.PoolMember) error {
	poolVMMu.Lock()
	state, err := loadPoolVMs(s)
	poolVMMu.Unlock()
	if err != nil {
		return errors.New("pool VM ownership state is unavailable")
	}
	if err = poolEjectOwnerRefusal(state, membership, target); err != nil {
		return err
	}
	poolStorageMu.Lock()
	storage, storageErr := loadPoolStorage(s)
	poolStorageMu.Unlock()
	if storageErr != nil {
		return errors.New("pool storage reservation state is unavailable")
	}
	if poolResourceMutationPending(storage) {
		return errors.New("pool resource removal outcome is unresolved; finish or reconcile it before ejecting a host")
	}
	for id, holders := range storage.Reservations {
		if holders[target.HostID] != "" {
			return errors.New("host " + target.HostID + " still holds a pool-shared storage reservation on " + id + "; let it finish before ejecting it")
		}
	}
	return poolEjectLocalDependencyRefusal(s, membership, state, target)
}

// poolEjectOwnerRefusal distinguishes a locally contained VM from one still owned by the
// old pool. A fresh target inventory is the proof for both cases: without it, a missing
// owner record could hide a running shared-disk guest. Local-only records are retired only
// after the authority freeze, immediately before the durable membership removal.
func poolEjectOwnerRefusal(state poolVMState, membership protocol.PoolMembership, target protocol.PoolMember) error {
	inventory, ok := state.Inventory[target.HostID]
	if !ok || inventory.HostID != target.HostID || inventory.Generation != membership.Generation {
		return errors.New("host " + target.HostID + " has not reported a current VM inventory; wait for it before ejecting it")
	}
	owned := []string{}
	recovering := []string{}
	snapshotted := []string{}
	for id, vm := range state.VMs {
		if vm.HostID != target.HostID {
			continue
		}
		if len(vm.Snapshots) != 0 {
			snapshotted = append(snapshotted, id)
		}
		if vm.PendingRecovery != nil {
			recovering = append(recovering, id)
			continue
		}
		current := findPoolVMInventorySpec(inventory, id)
		if !poolVMRecordMatchesSpec(vm, current) || vmUsesInventorySharedStorage(current, inventory.Storage) {
			owned = append(owned, id)
		}
	}
	if len(recovering) != 0 {
		slices.Sort(recovering)
		return errors.New("host " + target.HostID + " has unresolved snapshot recovery for pool VMs (" + strings.Join(recovering, ", ") + "); reconcile them before ejecting it")
	}
	if len(snapshotted) != 0 {
		slices.Sort(snapshotted)
		return errors.New("host " + target.HostID + " has VM snapshots that are not yet transferable (" + strings.Join(snapshotted, ", ") + "); delete the snapshots before ejecting it")
	}
	if len(owned) != 0 {
		names := strings.Join(owned, ", ")
		if len(names) > 120 {
			names = names[:120] + "…"
		}
		return errors.New("host " + target.HostID + " still owns pool VM records (" + names + "); move or delete them before ejecting it")
	}
	return nil
}

// poolEjectLocalDependencyRefusal names the local VMs on the target that depend on a resource
// the eject takes away. An ejected host keeps its local VMs, but a local VM whose NIC is on a
// pool VLAN, or whose CD drive holds a pool ISO, holds a registration the leaving host must
// tear down and will refuse to. Core never deletes that VM or edits its NICs to get past it,
// so the operator has to fix it — and the master, which already holds every member's own
// inventory, is the only place that can say so before the eject rather than after.
func poolEjectLocalDependencyRefusal(s *Store, membership protocol.PoolMembership, state poolVMState, target protocol.PoolMember) error {
	inventory, ok := state.Inventory[target.HostID]
	if !ok || inventory.HostID != target.HostID || inventory.Generation != membership.Generation {
		return errors.New("host " + target.HostID + " has not reported a current VM inventory; wait for it before ejecting it")
	}
	networks, err := poolReplicaNetworks(s)
	if err != nil {
		return errors.New("pool network records are unavailable")
	}
	pool := map[string]string{}
	for _, network := range networks {
		pool[network.ID] = "pool network " + network.ID
	}
	media, err := poolSharedMedia(s)
	if err != nil {
		return errors.New("pool ISO records are unavailable")
	}
	for _, m := range media {
		pool[m.ID] = "pool ISO " + m.ID
	}
	var held []string
	for _, vm := range inventory.VMs {
		for _, nic := range vm.NICs {
			if name, ok := pool[nic.NetworkID]; ok {
				held = append(held, "VM "+vm.ID+" uses "+name)
			}
		}
		for _, id := range []string{vm.MediaID, vm.DriversMediaID} {
			if name, ok := pool[id]; id != "" && ok {
				held = append(held, "VM "+vm.ID+" uses "+name)
			}
		}
	}
	if len(held) == 0 {
		return nil
	}
	slices.Sort(held)
	held = slices.Compact(held)
	names := strings.Join(held, "; ")
	if len(names) > 200 {
		names = names[:200] + "…"
	}
	return errors.New("host " + target.HostID + " has local VMs that depend on resources leaving with the pool (" + names + "); detach them from the pool network or ISO before ejecting it")
}

// replicatePoolAuthority is step 2. The target must acknowledge exactly the frozen
// generation; nothing else is proof that it can carry the authority.
func replicatePoolAuthority(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) error {
	member, err := currentPoolMember(s, record.TargetHostID)
	if err != nil {
		return err
	}
	// Every member is caught up here, not only the target: a member that is behind cannot
	// re-point later without a replica. An unreachable one does not block the transfer.
	_ = PushPoolAuthorityReplicas(ctx, s, timeout)
	status, err := probePoolAuthority(ctx, s, member.PeerOrigin, member.HostID, timeout)
	if err != nil {
		return err
	}
	if status.PoolID != s.State.PoolID {
		return errors.New("pool member " + member.HostID + " reports another pool")
	}
	if status.ReplicaGeneration != record.AuthorityGeneration || !status.ReplicaVerified {
		return errors.New("pool member " + member.HostID + " has not acknowledged the frozen pool authority generation")
	}
	record.Stage = "activate"
	return nil
}

// handOffPoolAuthority is steps 3 and 4. The hand-off is persisted before it is issued, and
// from that instant this host refuses every authority write for good: that ordering, not a
// timeout, is what makes a second writer impossible.
func handOffPoolAuthority(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) error {
	member, err := currentPoolMember(s, record.TargetHostID)
	if err != nil {
		return err
	}
	if record.Notice == nil || !record.HandedOff {
		poolPeerMu.Lock()
		state, loadErr := loadPoolPeer(s)
		if loadErr != nil {
			poolPeerMu.Unlock()
			return loadErr
		}
		if state.CAKeyPEM == "" || state.Membership.MasterHostID != s.State.HostID {
			poolPeerMu.Unlock()
			return errors.New("only the pool master hands its authority over")
		}
		notice := protocol.PoolAuthorityNotice{
			PoolID: state.Membership.PoolID, OperationID: record.OperationID, Action: "promote",
			FromHostID: s.State.HostID, ToHostID: member.HostID, TargetHostID: member.HostID,
			AuthorityGeneration: record.AuthorityGeneration, IssuedAt: time.Now().UTC(),
		}
		if err = signPoolAuthorityNotice(state.CAKeyPEM, &notice); err != nil {
			poolPeerMu.Unlock()
			return err
		}
		record.Notice, record.HandedOff = &notice, true
		record.UpdatedAt = time.Now().UTC()
		stored := *record
		state.Transfer = &stored
		err = savePoolPeer(s, state)
		poolPeerMu.Unlock()
		if err != nil {
			return errors.New("the pool authority hand-off could not be recorded durably")
		}
	}
	reply, err := poolTransferRPC(ctx, s, member.PeerOrigin, member.HostID, protocol.PoolTransferRequest{OperationID: record.OperationID, Action: "activate", Notice: *record.Notice}, timeout)
	if err != nil {
		return errors.New("pool authority was handed to " + member.HostID + " but activation did not complete; cancel the transfer or confirm powering off that host to resume authority on this host")
	}
	if !reply.Activated || reply.Notice == nil {
		return errors.New("pool member " + member.HostID + " did not activate the pool authority")
	}
	// The outgoing master demotes itself on the new master's signed re-point notice, not
	// on its own assumption: the same evidence every other host requires.
	if _, err = applyPoolRepoint(s, member, protocol.PoolTransferRequest{OperationID: record.OperationID, Action: "repoint", Notice: *reply.Notice}); err != nil {
		return err
	}
	record.Stage, record.Frozen, record.HandedOff = "done", false, false
	record.MembershipGeneration = reply.MembershipGeneration
	return nil
}

// removePoolMembership is the eject's durable membership change: the target's row is marked
// ejected at the next membership generation, so its certificate stops being an admitted
// peer everywhere the pool checks membership.
func removePoolMembership(s *Store, record *poolTransferRecord) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil {
		return errors.New("pool membership is unavailable")
	}
	if state.Membership.MasterHostID != s.State.HostID {
		return errors.New("only the pool master ejects a member")
	}
	target, ok := poolMemberByID(state.Membership, record.TargetHostID)
	if !ok {
		return errors.New("no such member of this pool")
	}
	if target.Status != "ejected" {
		poolPeerMu.Unlock()
		err = retireEjectedLocalPoolVMOwners(s, record.TargetHostID)
		poolPeerMu.Lock()
		if err != nil {
			return err
		}
		state, err = loadPoolPeer(s)
		if err != nil || state.Membership.Valid() != nil {
			return errors.New("pool membership is unavailable")
		}
		state.Membership = nextPoolMembership(state.Membership, s.State.HostID, record.TargetHostID)
		if err = state.Membership.Valid(); err != nil {
			return errors.New("the pool membership would be invalid after this eject")
		}
	}
	// The eject is durable from here, so nothing more is frozen: the remaining members and
	// the ejected host converge on the new membership, and the pool keeps working.
	record.Stage, record.Frozen = "repoint", false
	record.MembershipGeneration = state.Membership.Generation
	record.AuthorityGeneration = state.AuthorityGeneration
	record.UpdatedAt = time.Now().UTC()
	stored := *record
	state.Transfer = &stored
	return savePoolPeer(s, state)
}

func retireEjectedLocalPoolVMOwners(s *Store, hostID string) error {
	membership, err := Membership(s)
	if err != nil {
		return errors.New("pool membership is unavailable")
	}
	target, ok := poolMemberByID(membership, hostID)
	if !ok {
		return errors.New("no such member of this pool")
	}
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		return errors.New("pool VM ownership state is unavailable")
	}
	if err = poolEjectOwnerRefusal(state, membership, target); err != nil {
		return err
	}
	for id, vm := range state.VMs {
		if vm.HostID == hostID {
			delete(state.VMs, id)
		}
	}
	if err = savePoolVMs(s, state); err != nil {
		return errors.New("local VM owner records could not be retired before ejecting this host")
	}
	return nil
}

// repointPoolMembers is step 5. The new master tells every other host, in its own signed
// words, which host is the master now and at which membership generation. A host that is
// unreachable is not waited for and is not assumed to have failed: it is contacted again on
// every tick, and it can also converge by probing any host it can reach.
func repointPoolMembers(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		return errors.New("pool membership is unavailable")
	}
	if state.Membership.MasterHostID != s.State.HostID {
		return errors.New("only the pool master re-points its members")
	}
	notice, err := poolRepointNotice(s, state, record)
	if err != nil {
		return err
	}
	var failed error
	for _, member := range state.Membership.Members {
		if member.HostID == s.State.HostID || record.repointed(member.HostID) {
			continue
		}
		if member.Status != "ready" || member.PeerOrigin == "" {
			// An ejected or pending host is not re-pointed: the eject's own leave step
			// addresses the ejected host, and a pending one has no authority to follow.
			record.Repointed = append(record.Repointed, member.HostID)
			continue
		}
		if poolPromotionProofFor(record.Fences, member.HostID) != nil {
			// A host this promotion fenced is powered off, so it is not waited for and its
			// silence is not a failure. It keeps its membership row and learns what happened
			// from the authority probe when it comes back, which is the only evidence it
			// will accept anyway.
			record.Repointed = append(record.Repointed, member.HostID)
			continue
		}
		addressed := *notice
		addressed.ToHostID = member.HostID
		if err = signPoolAuthorityNotice(state.CAKeyPEM, &addressed); err != nil {
			return err
		}
		reply, rpcErr := poolTransferRPC(ctx, s, member.PeerOrigin, member.HostID, protocol.PoolTransferRequest{OperationID: record.OperationID, Action: "repoint", Notice: addressed}, timeout)
		if rpcErr != nil && reply.MembershipGeneration < state.Membership.Generation {
			failed = rpcErr
			continue
		}
		record.Repointed = append(record.Repointed, member.HostID)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if failed != nil {
		return failed
	}
	record.Stage = "done"
	if record.Action == "eject" {
		record.Stage = "leave"
	}
	// The new authority is signed and numbered; push it so every member holds a replica of
	// the pool it now follows.
	_ = PushPoolAuthorityReplicas(ctx, s, timeout)
	return nil
}

// tellPoolMemberToLeave is the eject's last step. An ejected host that cannot be reached is
// left alone: it is no longer an admitted peer anywhere in this pool, and it learns what
// happened from the authority probe when it comes back.
func tellPoolMemberToLeave(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil || state.CAKeyPEM == "" {
		return errors.New("pool membership is unavailable")
	}
	target, ok := poolMemberByID(state.Membership, record.TargetHostID)
	if !ok || target.Status != "ejected" {
		return errors.New("the ejected host is no longer recorded")
	}
	record.Stage = "done"
	if target.PeerOrigin == "" {
		return nil
	}
	notice := protocol.PoolAuthorityNotice{
		PoolID: state.Membership.PoolID, OperationID: record.OperationID, Action: "eject",
		FromHostID: s.State.HostID, ToHostID: target.HostID, TargetHostID: target.HostID,
		AuthorityGeneration: state.AuthorityGeneration, Membership: state.Membership, IssuedAt: time.Now().UTC(),
	}
	if err = signPoolAuthorityNotice(state.CAKeyPEM, &notice); err != nil {
		return err
	}
	if _, err = poolTransferRPC(ctx, s, target.PeerOrigin, target.HostID, protocol.PoolTransferRequest{OperationID: record.OperationID, Action: "leave", Notice: notice}, timeout); err != nil {
		// Not a failure of the eject: the membership change is durable either way.
		record.LastError = poolTransferError(err)
	}
	return nil
}

// AdvancePoolAuthorityTransfer resumes this host's own durable transfer from the probe
// tick. It is what makes every stage crash-resumable on both hosts with no Controller.
func AdvancePoolAuthorityTransfer(ctx context.Context, s *Store, timeout time.Duration) {
	if !poolTransferMu.TryLock() {
		return
	}
	defer poolTransferMu.Unlock()
	record, err := loadPoolTransfer(s)
	if err != nil || record == nil || poolTransferTerminal(record.Stage) || record.Role == "ejected" {
		return
	}
	if err := automaticPoolPromotionFenceEnabled(s, record); err != nil {
		record.Attempts++
		record.LastError = err.Error()
		_ = savePoolTransfer(s, record)
		return
	}
	if record.Role == "source" && record.HandedOff && record.Stage != "done" {
		// This host signed its authority away and has not yet learned the outcome. It
		// refuses every authority write meanwhile; it converges by asking the target.
		convergePoolAuthorityFrom(ctx, s, record, timeout)
		return
	}
	advancePoolTransfer(ctx, s, record, timeout)
}

// convergePoolAuthorityFrom asks the transfer's target where authority actually is. A host
// that handed its authority over demotes itself only on the new master's signed notice; a
// target that never activated leaves this host frozen and honest, never a second writer.
func convergePoolAuthorityFrom(ctx context.Context, s *Store, record *poolTransferRecord, timeout time.Duration) {
	member, err := currentPoolMember(s, record.TargetHostID)
	if err != nil {
		return
	}
	status, err := probePoolAuthority(ctx, s, member.PeerOrigin, member.HostID, timeout)
	if err != nil {
		record.Attempts, record.LastError = record.Attempts+1, poolTransferError(err)
		_ = savePoolTransfer(s, record)
		return
	}
	if status.Role != "master" || status.Notice == nil {
		record.LastError = "the host this pool's authority was handed to has not activated it"
		_ = savePoolTransfer(s, record)
		return
	}
	if _, err = applyPoolRepoint(s, member, protocol.PoolTransferRequest{OperationID: record.OperationID, Action: "repoint", Notice: *status.Notice}); err != nil {
		record.Attempts, record.LastError = record.Attempts+1, poolTransferError(err)
		_ = savePoolTransfer(s, record)
	}
}

// ConvergePoolAuthority is what a member does on every tick: one bounded probe of the host
// it believes is its master. It re-points on a signed notice at a higher generation, it
// follows a demoted master's answer to the host that is master now, and it records an eject
// when the pool it followed no longer admits its certificate. It never concludes anything
// from a timeout, and it changes nothing without a signature.
func ConvergePoolAuthority(ctx context.Context, s *Store, timeout time.Duration) {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil || state.Membership.MasterHostID == s.State.HostID {
		return
	}
	if state.Transfer != nil && state.Transfer.Role == "ejected" && !poolTransferTerminal(state.Transfer.Stage) {
		return
	}
	master, ok := poolMemberByID(state.Membership, state.Membership.MasterHostID)
	if !ok {
		return
	}
	// A member's own copy of the membership may predate the master recording its own peer
	// origin; the durable master URL it already pulls from is the same endpoint.
	origin := master.PeerOrigin
	if origin == "" {
		origin = state.MasterURL
	}
	if origin == "" {
		return
	}
	status, err := probePoolAuthority(ctx, s, origin, master.HostID, timeout)
	if err != nil {
		return
	}
	if status.Notice != nil {
		_, _ = applyPoolRepoint(s, master, protocol.PoolTransferRequest{OperationID: status.Notice.OperationID, Action: "repoint", Notice: *status.Notice})
		return
	}
	if status.CallerStatus == "" || status.CallerStatus == "ejected" {
		// The pool this host followed no longer admits its certificate, said by the master
		// this host has pinned, at a membership generation newer than its own. That is an
		// eject it missed, not an outage.
		if status.Role == "master" && status.MembershipGeneration > state.Membership.Generation {
			recordMissedPoolEject(s, status)
		}
		return
	}
	if status.Role == "member" && status.MasterHostID != "" && status.MasterHostID != master.HostID && status.MasterPeerOrigin != "" {
		// The host this one followed is a member now too. Ask the host it names; only a
		// signed notice from that host at a higher generation changes anything here.
		next, nextErr := probePoolAuthority(ctx, s, status.MasterPeerOrigin, status.MasterHostID, timeout)
		if nextErr != nil || next.Notice == nil {
			return
		}
		if member, found := poolMemberByID(state.Membership, status.MasterHostID); found {
			_, _ = applyPoolRepoint(s, member, protocol.PoolTransferRequest{OperationID: next.Notice.OperationID, Action: "repoint", Notice: *next.Notice})
		}
	}
}

func recordMissedPoolEject(s *Store, status protocol.PoolAuthorityStatus) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil || status.MembershipGeneration <= state.Membership.Generation {
		return
	}
	// A completed eject receipt survives a later rejoin as historical evidence. It must
	// not suppress a newer missed eject: the master is already reporting this certificate
	// ejected at a higher membership generation. An active leave still wins, so repeated
	// probes cannot replace an in-progress reinitialization.
	if state.Transfer != nil && state.Transfer.Role == "ejected" && !poolTransferTerminal(state.Transfer.Stage) {
		return
	}
	operation := status.HostID + "-eject-" + s.State.HostID
	state.Transfer = &poolTransferRecord{
		OperationID: operation, Action: "eject", Role: "ejected", TargetHostID: s.State.HostID,
		Stage: "leave", Status: "pending", MembershipGeneration: status.MembershipGeneration,
		LastError: "this host was ejected from its pool while it was unreachable", UpdatedAt: time.Now().UTC(),
	}
	_ = savePoolPeer(s, state)
}

// ResumePoolLeave completes an eject on the ejected host: it becomes the master of a new
// single-host pool of its own, keeping its host ID, its local storage and its local VMs. It
// must run on the goroutine that owns the Store, like a completed join, because it rewrites
// this Core's pool identity; it reports whether it did.
func ResumePoolLeave(ctx context.Context, s *Store) bool {
	poolTransferMu.Lock()
	defer poolTransferMu.Unlock()
	record, err := loadPoolTransfer(s)
	if err != nil || record == nil || record.Role != "ejected" || poolTransferTerminal(record.Stage) {
		return false
	}
	before := s.State.PoolID
	if err = reinitializeOwnPool(ctx, s, record); err != nil {
		record.Attempts, record.LastError = record.Attempts+1, poolTransferError(err)
		_ = savePoolTransfer(s, record)
		return s.State.PoolID != before
	}
	record.Stage, record.Status, record.LastError = "done", "succeeded", ""
	_ = savePoolTransfer(s, record)
	return s.State.PoolID != before
}

// reinitializeOwnPool turns an ejected host back into the master of its own single-host
// pool. Every step is idempotent, so an interrupted eject finishes on the next pass: the
// old pool's shared registrations are detached through the same orphan paths a member
// already uses, then this Core takes a new pool identity and a new pool CA. Its host ID,
// its local storage and its local VMs are untouched, and the old pool's files on shared
// storage are left exactly where they are: they belong to the pool, not to this host.
func reinitializeOwnPool(ctx context.Context, s *Store, record *poolTransferRecord) error {
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	if err := detachPoolSharedResources(ctx, s, engine, record); err != nil {
		return err
	}
	// The replica is another pool's authority, including its sealed CA key.
	if err := os.Remove(poolReplicaFile(s)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("the previous pool's authority replica could not be removed")
	}
	if s.State.HostID == "" {
		return errors.New("this host has no identity to re-initialize")
	}
	// A new pool identity exactly as an enrolling host takes one, named after this host: a
	// new pool ID is what keeps the pool it left unambiguous.
	name := s.State.PoolName
	if hostname, err := osHostname(); err == nil && hostname != "" {
		name = hostname
	}
	poolID := newUUID()
	s.State.PoolID, s.State.PoolName = poolID, name
	if err := s.Save(); err != nil {
		return persistenceError{err}
	}
	// A host that has never journalled anything has no scope to re-home.
	if err := vmhost.RehomeLeavingPoolScope(filepath.Join(s.dir, "managed-vms"), poolID, s.State.HostID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	poolVMMu.Lock()
	vms, err := loadPoolVMs(s)
	if err == nil && len(vms.VMs)+len(vms.Inventory) != 0 {
		vms.VMs, vms.Inventory = map[string]poolVMRecord{}, map[string]poolVMInventory{}
		err = savePoolVMs(s, vms)
	}
	poolVMMu.Unlock()
	if err != nil {
		return errors.New("the previous pool's VM ownership records could not be cleared")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	state.CAPEM, state.CAKeyPEM, state.CertificatePEM, state.KeyPEM = "", "", "", ""
	state.MasterURL, state.PendingPeerOrigin, state.PendingJoin, state.PendingRehome = "", "", nil, nil
	state.PendingKeyPEM, state.PendingCSRPEM = "", ""
	state.AuthorityGeneration, state.AuthorityDigest = 0, ""
	state.Membership = protocol.PoolMembership{}
	state.Offers = map[string]poolJoinOffer{}
	if err = initPoolMaster(s, &state); err != nil {
		return err
	}
	return savePoolPeer(s, state)
}

// detachPoolSharedResources removes every registration this host holds of the pool it is
// leaving, in dependency order: the adopted ISO records first — metadata only, the file stays
// on shared storage because it belongs to the pool, not to this host — then the pool-shared
// repositories those ISOs referenced, then the pool VLAN networks.
//
// Every step is prechecked exactly as a member's own orphan convergence pass prechecks its
// removals. A refusal — a local VM this host keeps whose NIC is on a pool VLAN, or whose CD
// drive holds a pool ISO — journals nothing at all, names the VM in Core's own words and is
// re-evaluated on the next tick, so fixing the cause converges without an operator touching
// the host's state. Nothing here ever deletes a VM or edits its NIC configuration: the leave
// stays in its stage and reports the blocker instead.
func detachPoolSharedResources(ctx context.Context, s *Store, engine *vmhost.ManagedEngine, record *poolTransferRecord) error {
	poolID, hostID, generation, signed := signedEjectCleanupProvenance(s, record)
	executeCleanup := func(cmd protocol.Command) error {
		var result protocol.Result
		var err error
		if signed {
			result, err = engine.ExecuteEjectCleanup(ctx, cmd, poolID, hostID, generation)
		} else {
			// An unreachable host learns ejection from authenticated status rather than a
			// retained notice. Preserve its old fail-closed cleanup behavior; it receives
			// no automatic rejoin grant without the signed notice provenance.
			result, err = engine.Execute(ctx, cmd, nil)
		}
		if err != nil {
			return err
		}
		if result.Status != "succeeded" {
			return errors.New(result.Error)
		}
		return nil
	}
	managed, _ := engine.Applied(ctx)
	if managed == nil {
		return errors.New("this host's managed state is unavailable")
	}
	shared := map[string]bool{}
	for _, storage := range managed.Storage {
		if protocol.IsPoolSharedStorage(storage) && !storage.Tombstone {
			shared[storage.ID] = true
		}
	}
	var blocked error
	for _, media := range managed.Media {
		if media.Tombstone || !shared[media.StorageID] {
			continue
		}
		if err := engine.CheckMediaRemoval(ctx, media.ID); err != nil {
			blocked = poolLeaveBlockedf("the previous pool's shared ISO %s cannot be released from this host: %s", media.ID, err)
			continue
		}
		cmd := protocol.Command{
			OperationID: poolLeaveCleanupOperation(record, "media-remove", media.ID, protocol.MediaGeneration(media)),
			Kind:        "media-remove",
			ResourceLifecycle: &protocol.ResourceLifecycleRequest{
				Action: "remove", Resource: "media", ResourceID: media.ID,
				PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: protocol.MediaGeneration(media),
			},
		}
		if err := executeCleanup(cmd); err != nil {
			blocked = poolLeaveBlockedf("the previous pool's shared ISO %s could not be released from this host: %s", media.ID, err)
		}
	}
	// A repository is only detachable once the ISOs on it are released, so a blocked ISO
	// stops this phase rather than minting a detach that is certain to be refused.
	if blocked == nil {
		for _, storage := range managed.Storage {
			if !shared[storage.ID] {
				continue
			}
			if err := engine.CheckStorageDetach(ctx, storage.ID); err != nil {
				blocked = poolLeaveBlockedf("the previous pool's repository %s cannot be detached from this host: %s", storage.ID, err)
				continue
			}
			cmd := protocol.Command{
				OperationID: poolLeaveCleanupOperation(record, "storage-detach", storage.ID, storage.Generation),
				Kind:        "storage-detach",
				StorageLifecycle: &protocol.StorageLifecycleRequest{
					Action: "detach", RepositoryID: storage.ID,
					PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: storage.Generation,
				},
			}
			if err := executeCleanup(cmd); err != nil {
				blocked = poolLeaveBlockedf("the previous pool's repository %s could not be detached from this host: %s", storage.ID, err)
			}
		}
	}
	// The VLAN networks depend on nothing above, so they are torn down even while an ISO or
	// a repository is still blocked: one stuck resource never strands the others.
	for _, network := range managed.Networks {
		if !protocol.IsPoolSharedNetwork(network) || network.Tombstone {
			continue
		}
		if err := engine.CheckNetworkRemoval(ctx, network.ID); err != nil {
			if blocked == nil {
				blocked = poolLeaveBlockedf("the previous pool's network %s cannot be released from this host: %s", network.ID, err)
			}
			continue
		}
		cmd := protocol.Command{
			OperationID: poolLeaveCleanupOperation(record, "network-remove", network.ID, protocol.NetworkGeneration(network)),
			Kind:        "network-remove",
			ResourceLifecycle: &protocol.ResourceLifecycleRequest{
				Action: "remove", Resource: "network", ResourceID: network.ID,
				PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: protocol.NetworkGeneration(network),
			},
		}
		if err := executeCleanup(cmd); err != nil {
			if blocked == nil {
				blocked = poolLeaveBlockedf("the previous pool's network %s could not be released from this host: %s", network.ID, err)
			}
		}
	}
	return blocked
}

// poolLeaveCleanupOperation is stable for retries of one durable leave, and distinct for a
// later signed eject even when the master's resource generation did not change.  A cleanup
// receipt is membership-scoped provenance, so replaying an earlier leave's successful ID
// would falsely report a new cleanup without recording its tombstone.
func poolLeaveCleanupOperation(record *poolTransferRecord, action, id string, resourceGeneration uint64) string {
	membershipGeneration := uint64(0)
	operation := ""
	if record != nil {
		membershipGeneration, operation = record.MembershipGeneration, record.OperationID
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte("pool-leave-cleanup\x00"+operation+"\x00"+strconv.FormatUint(membershipGeneration, 10)+"\x00"+action+"\x00"+id+"\x00"+strconv.FormatUint(resourceGeneration, 10))))
}

// signedEjectCleanupProvenance keeps the leave-only grant bound to the notice the member
// verified before entering its durable leave state. Status-only recovery has no such notice.
func signedEjectCleanupProvenance(s *Store, record *poolTransferRecord) (poolID, hostID string, generation uint64, ok bool) {
	if record == nil || record.Action != "eject" || record.Notice == nil || record.Notice.Membership.Generation != record.MembershipGeneration || record.Notice.PoolID != s.State.PoolID || record.Notice.TargetHostID != s.State.HostID {
		return "", "", 0, false
	}
	return s.State.PoolID, s.State.HostID, record.MembershipGeneration, true
}

// poolLeaveBlockedf is the leave's own refusal sentence. It carries Core's own words for the
// blocker, bounded, so the ejected host's reported transfer names what an operator must fix.
func poolLeaveBlockedf(format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	if len(detail) > 300 {
		detail = detail[:300]
	}
	return errors.New(detail)
}
