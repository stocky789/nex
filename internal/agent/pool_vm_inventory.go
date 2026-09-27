// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

type poolVMInventory struct {
	DetectedOS map[string]protocol.DetectedOS      `json:"detected_os,omitempty"`
	HostID     string                              `json:"host_id"`
	Generation uint64                              `json:"generation"`
	VMs        []protocol.VMCreateSpec             `json:"vms"`
	Storage    map[string]protocol.StorageIdentity `json:"storage"`
	Observed   map[string]string                   `json:"observed"`
	// StartOutcomes contains exact failed start journal IDs. The master accepts one
	// only with matching fresh shutoff state and a pending admission match.
	StartOutcomes []poolVMStartOutcome `json:"start_outcomes,omitempty"`
	// Updates contains only exact, successful local managed-journal evidence
	// for a current revision. It lets the master settle a pre-existing pending
	// transition after a restart, never makes inventory itself authoritative.
	Updates map[string]vmhost.VMUpdateEvidence `json:"updates,omitempty"`
	// Aborted maps an operation ID to journal proof that this exact next-revision update
	// failed unapplied, so the master can release a reservation whose outcome it never saw.
	Aborted map[string]vmhost.VMUpdateEvidence `json:"aborted,omitempty"`
	// Recoveries contains terminal evidence constructed by this owner's managed journal.
	// An omitted operation remains reserved; inventory absence is never completion evidence.
	Recoveries map[string]protocol.PoolVMRecoveryEvidence `json:"recoveries,omitempty"` // operation ID to evidence
	// Deleted names the VMs this host durably deleted: succeeded vm-delete tombstones
	// from its own journal. It is the positive evidence the master prunes owner records
	// with; absence from VMs above is never read as a deletion.
	Deleted    []string  `json:"deleted,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type poolVMStartOutcome struct {
	VMID        string `json:"vm_id"`
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
}

var errPoolVMSnapshotTombstone = errors.New("VM delete tombstone conflicts with pending or retained snapshot state")

// PoolVMRecoveryInventory reports the master's unresolved snapshot reservations to a fresh
// Controller. The durable authority is pool-vms.json; members never publish this projection.
func PoolVMRecoveryInventory(s *Store) ([]protocol.PoolVMRecoveryReservation, bool, error) {
	membership, err := Membership(s)
	if err != nil {
		return nil, false, err
	}
	if membership.MasterHostID != s.State.HostID {
		return nil, false, nil
	}
	poolVMMu.Lock()
	state, err := loadPoolVMs(s)
	poolVMMu.Unlock()
	if err != nil {
		return nil, false, err
	}
	reservations := make([]protocol.PoolVMRecoveryReservation, 0)
	for _, record := range state.VMs {
		if record.PendingRecovery == nil {
			continue
		}
		want, bindingErr := poolVMRecoveryReservationFor(record, record.PendingRecovery.OperationID, record.PendingRecovery.Kind, record.PendingRecovery.IntentSHA256, record.PendingRecovery.OwnerOperationOrder)
		got := *record.PendingRecovery
		got.AdmissionRefusal = ""
		if got.Kind == "snapshot-revert" {
			want.TargetSnapshotID, want.TargetSnapshotSHA256, want.ResultSpecSHA256 = got.TargetSnapshotID, got.TargetSnapshotSHA256, got.ResultSpecSHA256
			if record.PendingRecoveryResultSpec == nil || poolVMResultSpecMatchesReservation(got, *record.PendingRecoveryResultSpec) != nil ||
				!poolVMHasSnapshotDescriptor(record.Snapshots, got.TargetSnapshotID, got.TargetSnapshotSHA256) {
				bindingErr = errors.New("pending snapshot revert result or target is invalid")
			}
		} else if got.Kind == "snapshot-delete" {
			want.TargetSnapshotID, want.TargetSnapshotSHA256 = got.TargetSnapshotID, got.TargetSnapshotSHA256
			if record.PendingRecoveryResultSpec != nil || !poolVMHasSnapshotDescriptor(record.Snapshots, got.TargetSnapshotID, got.TargetSnapshotSHA256) {
				bindingErr = errors.New("pending snapshot delete target is invalid")
			}
		} else if record.PendingRecoveryResultSpec != nil {
			bindingErr = errors.New("non-revert recovery has a pending result definition")
		}
		if err := record.PendingRecovery.Valid(); err != nil || bindingErr != nil || want != got || record.PendingRecovery.PoolID != membership.PoolID || record.PendingRecovery.VMID != record.VMID || record.PendingRecovery.OwnerHostID != record.HostID {
			return nil, false, errors.New("durable snapshot recovery reservation is invalid")
		}
		reservations = append(reservations, *record.PendingRecovery)
	}
	slices.SortFunc(reservations, func(a, b protocol.PoolVMRecoveryReservation) int {
		return strings.Compare(a.VMID, b.VMID)
	})
	b, err := json.Marshal(reservations)
	if err != nil || len(b) > protocol.PoolReplicaLimit {
		return nil, false, errors.New("snapshot recovery inventory exceeds the pool authority limit")
	}
	return reservations, true, nil
}

// PoolVMSnapshotInventory returns the catalog owned by the current pool master.
// Members report it as unknown rather than substituting local libvirt metadata.
func PoolVMSnapshotInventory(s *Store) ([]protocol.PoolVMSnapshot, bool, error) {
	membership, err := Membership(s)
	if err != nil || membership.PoolID != s.State.PoolID || membership.MasterHostID == "" {
		return nil, false, errors.New("pool snapshot catalog authority is unavailable")
	}
	if membership.MasterHostID != s.State.HostID {
		return nil, false, nil
	}
	poolVMMu.Lock()
	state, err := loadPoolVMs(s)
	poolVMMu.Unlock()
	if err != nil {
		return nil, false, err
	}
	snapshots := make([]protocol.PoolVMSnapshot, 0)
	for id, record := range state.VMs {
		if id != record.VMID || record.PoolID != membership.PoolID {
			return nil, false, errors.New("pool snapshot catalog contains an invalid VM record")
		}
		if err := validatePoolVMSnapshotCatalog(record); err != nil {
			return nil, false, err
		}
		snapshots = append(snapshots, record.Snapshots...)
	}
	slices.SortFunc(snapshots, func(a, b protocol.PoolVMSnapshot) int {
		if order := strings.Compare(a.VMID, b.VMID); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	encoded, err := json.Marshal(snapshots)
	if err != nil || len(encoded) > protocol.PoolReplicaLimit {
		return nil, false, errors.New("pool snapshot catalog exceeds the authority limit")
	}
	return snapshots, true, nil
}

type poolVMInventoryReply struct {
	Membership protocol.PoolMembership `json:"membership,omitempty"`
}

var errPoolVMInventoryIncomplete = errors.New("pool VM inventory is incomplete for current membership")

// errPoolVMOwnersDeferred reports that the snapshot is durable but the owner records it would
// settle were not written, because this host's pool authority is frozen or handed over. The
// projection is deferred, never dropped: the next publication after the transfer settles it on
// whichever host is master then.
var errPoolVMOwnersDeferred = errors.New("pool VM owner records are deferred while pool authority is frozen")
var errPoolVMInventoryStale = errors.New("stale pool VM inventory")

func settlePoolVMInventoryDefinition(record *poolVMRecord, owner string, observed protocol.VMCreateSpec, evidence *vmhost.VMUpdateEvidence) error {
	if record.Spec.ID == "" || reflect.DeepEqual(record.Spec, observed) {
		return nil
	}
	if evidence == nil {
		return errors.New("shared VM definition conflicts with durable inventory")
	}
	if record.PendingUpdate != nil {
		if commitPoolVMUpdate(record, owner, *evidence) != nil {
			return errors.New("shared VM update evidence conflicts with durable inventory")
		}
	} else if recoverPoolVMUpdate(record, owner, *evidence) != nil {
		return errors.New("shared VM definition conflicts with durable inventory")
	}
	if !reflect.DeepEqual(record.Spec, observed) {
		return errors.New("shared VM update evidence conflicts with durable inventory")
	}
	return nil
}

// PublishPoolVMInventory sends only durable local facts to the master.  The master
// rejects incomplete/current-generation snapshots rather than electing the first caller.
func PublishPoolVMInventory(ctx context.Context, s *Store) error {
	return publishPoolVMInventory(ctx, s, false)
}

func publishPoolVMInventory(ctx context.Context, s *Store, refreshed bool) error {
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	inv, err := localPoolVMInventory(ctx, s, membership.Generation)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		return recordPoolVMInventory(s, membership, inv)
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	b, err := json.Marshal(inv)
	if err != nil || len(b) > protocol.PoolReplicaLimit {
		return errors.New("pool VM inventory exceeds the peer body limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, master+poolVMPath+"inventory", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return nil // Accepted means master durably retained a current partial snapshot.
	}
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode == http.StatusConflict && !refreshed {
		var reply poolVMInventoryReply
		if json.NewDecoder(resp.Body).Decode(&reply) == nil && updatePoolVMMembership(s, reply.Membership) == nil {
			return publishPoolVMInventory(ctx, s, true)
		}
	}
	return errors.New("pool master refused VM inventory")
}

// SettlePoolVMSnapshot publishes owner-journal evidence and returns success only
// when the current master echoes the exact settled evidence receipt.
func SettlePoolVMSnapshot(ctx context.Context, s *Store, evidence protocol.PoolVMRecoveryEvidence) error {
	if evidence.Valid() != nil || evidence.Reservation.Kind != "snapshot-create" && evidence.Reservation.Kind != "snapshot-revert" && evidence.Reservation.Kind != "snapshot-delete" {
		return errors.New("invalid snapshot settlement evidence")
	}
	actual, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).PoolVMRecoveryEvidence(ctx, evidence.Reservation)
	if err != nil || !reflect.DeepEqual(actual, evidence) {
		return errors.New("local managed journal does not match the requested snapshot settlement")
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != evidence.Reservation.PoolID || membership.MasterHostID == "" {
		return errors.New("pool snapshot authority is unavailable")
	}
	if membership.MasterHostID == s.State.HostID {
		err := func() error {
			poolVMMu.Lock()
			defer poolVMMu.Unlock()
			poolPeerMu.Lock()
			defer poolPeerMu.Unlock()
			peer, err := loadPoolPeer(s)
			if err != nil || peer.Membership.Generation != membership.Generation || peer.Membership.MasterHostID != s.State.HostID {
				return errors.New("pool snapshot authority changed before settlement")
			}
			if err := poolAuthorityWritable(peer); err != nil {
				return err
			}
			state, err := loadPoolVMs(s)
			if err != nil {
				return err
			}
			return settlePoolVMSnapshotStateLocked(s, &state, peer.Membership, s.State.HostID, evidence)
		}()
		if err != nil {
			return err
		}
		return confirmPoolVMSnapshotReplica(ctx, s, nil, &evidence)
	}
	return poolVMSnapshotSettlementPeer(ctx, s, evidence)
}

// confirmPoolVMSnapshotReplica proves every ready member has the exact pending
// reservation or settled evidence before the owner may proceed or release its
// local journal fence. It drops all pool locks for peer I/O, then pins the same
// authority digest and exact VM record again before returning.
func confirmPoolVMSnapshotReplica(ctx context.Context, s *Store, request *poolVMRequest, evidence *protocol.PoolVMRecoveryEvidence) error {
	if (request == nil) == (evidence == nil) || request != nil && (request.Action != "snapshot-reserve" || request.SnapshotReservation == nil || request.Snapshot == nil) {
		return errors.New("snapshot replica acknowledgement requires one exact transition")
	}
	if err := PushPoolAuthorityReplicas(ctx, s, 20*time.Second); err != nil {
		return errors.New("snapshot authority replica is not acknowledged by every ready member")
	}
	replica, digest, err := buildPoolAuthorityReplica(s)
	if err != nil {
		return errors.New("snapshot authority replica is unavailable")
	}
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || peer.Membership.Valid() != nil || peer.Membership.MasterHostID != s.State.HostID ||
		peer.Membership.PoolID != s.State.PoolID || peer.AuthorityGeneration == 0 || peer.AuthorityDigest != digest || poolAuthorityWritable(peer) != nil {
		return errors.New("snapshot authority changed before member acknowledgement")
	}
	replica.Generation = peer.AuthorityGeneration
	for _, member := range peer.Membership.Members {
		if member.HostID == s.State.HostID || !poolAuthorityReplicaAckRequired(member) {
			continue
		}
		if member.PeerOrigin == "" || !poolReplicaPostAckMatches(s, member.HostID, replica.Generation, digest) {
			return errors.New("every ready pool member must acknowledge snapshot state before native work or settlement")
		}
	}

	poolVMMu.Lock()
	poolPeerMu.Lock()
	currentPeer, peerErr := loadPoolPeer(s)
	state, stateErr := loadPoolVMs(s)
	vmBytes, marshalErr := json.Marshal(state.VMs)
	validRecord := false
	if stateErr == nil && marshalErr == nil && string(vmBytes) == string(replica.VMs) {
		var record poolVMRecord
		var ok bool
		if request != nil {
			reservation := request.SnapshotReservation
			record, ok = state.VMs[reservation.VMID]
			if request.RecoveryKind == "snapshot-revert" {
				if ok && request.CapturedSpec != nil && record.PendingRecovery != nil && record.PendingSnapshot == nil && record.PendingRecoveryResultSpec != nil {
					result, resultErr := protocol.PoolVMSnapshotRevertSpec(*request.CapturedSpec, record.Spec)
					validRecord = reflect.DeepEqual(*record.PendingRecovery, *reservation) && resultErr == nil &&
						reflect.DeepEqual(*record.PendingRecoveryResultSpec, result) &&
						poolVMHasSnapshotDescriptor(record.Snapshots, reservation.TargetSnapshotID, reservation.TargetSnapshotSHA256)
				}
			} else if request.RecoveryKind == "snapshot-delete" {
				validRecord = ok && record.PendingRecovery != nil && record.PendingSnapshot == nil && record.PendingRecoveryResultSpec == nil &&
					reflect.DeepEqual(*record.PendingRecovery, *reservation) &&
					poolVMHasSnapshotDescriptor(record.Snapshots, reservation.TargetSnapshotID, reservation.TargetSnapshotSHA256)
			} else {
				derived, depErr := protocol.PoolVMSnapshotDependenciesForSpec(record.Spec)
				validRecord = ok && record.PendingRecovery != nil && record.PendingSnapshot != nil &&
					reflect.DeepEqual(*record.PendingRecovery, *reservation) && record.PendingSnapshot.ID == reservation.OperationID &&
					record.PendingSnapshot.VMID == request.VMID && record.PendingSnapshot.Name == request.Snapshot.Name &&
					record.PendingSnapshot.Description == request.Snapshot.Description && record.PendingSnapshot.CreatedAt.Equal(request.SnapshotCreatedAt) &&
					record.PendingSnapshot.SpecRevision == reservation.SpecRevision && record.PendingSnapshot.SpecSHA256 == reservation.SpecSHA256 &&
					record.PendingSnapshot.DiskIdentitySHA256 == reservation.DiskIdentitySHA256 &&
					depErr == nil && record.PendingSnapshot.Dependencies != nil && reflect.DeepEqual(record.PendingSnapshot.Dependencies, &derived)
			}
		} else {
			record, ok = state.VMs[evidence.Reservation.VMID]
			validRecord = ok && record.PendingRecovery == nil && record.PendingSnapshot == nil &&
				record.LastSettledRecovery != nil && reflect.DeepEqual(*record.LastSettledRecovery, *evidence)
		}
	}
	valid := peerErr == nil && stateErr == nil && marshalErr == nil && validRecord &&
		currentPeer.Membership.Valid() == nil && currentPeer.Membership.MasterHostID == s.State.HostID &&
		reflect.DeepEqual(currentPeer.Membership, peer.Membership) && currentPeer.AuthorityGeneration == replica.Generation &&
		currentPeer.AuthorityDigest == digest && poolAuthorityWritable(currentPeer) == nil
	poolPeerMu.Unlock()
	poolVMMu.Unlock()
	if !valid {
		return errors.New("snapshot authority changed after member acknowledgement")
	}
	return nil
}

// confirmPoolVMSnapshotMetadataReplica proves every ready member has the updated
// catalog descriptor before metadata success is returned to the caller.
func confirmPoolVMSnapshotMetadataReplica(ctx context.Context, s *Store, vmID string, updated protocol.PoolVMSnapshot) error {
	if vmID == "" || updated.Valid() != nil || updated.VMID != vmID {
		return errors.New("snapshot metadata replica acknowledgement requires one exact catalog entry")
	}
	if err := PushPoolAuthorityReplicas(ctx, s, 20*time.Second); err != nil {
		return errors.New("snapshot authority replica is not acknowledged by every ready member")
	}
	replica, digest, err := buildPoolAuthorityReplica(s)
	if err != nil {
		return errors.New("snapshot authority replica is unavailable")
	}
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || peer.Membership.Valid() != nil || peer.Membership.MasterHostID != s.State.HostID ||
		peer.Membership.PoolID != s.State.PoolID || peer.AuthorityGeneration == 0 || peer.AuthorityDigest != digest || poolAuthorityWritable(peer) != nil {
		return errors.New("snapshot authority changed before member acknowledgement")
	}
	replica.Generation = peer.AuthorityGeneration
	for _, member := range peer.Membership.Members {
		if member.HostID == s.State.HostID || !poolAuthorityReplicaAckRequired(member) {
			continue
		}
		if member.PeerOrigin == "" || !poolReplicaPostAckMatches(s, member.HostID, replica.Generation, digest) {
			return errors.New("every ready pool member must acknowledge snapshot state before metadata success")
		}
	}

	poolVMMu.Lock()
	poolPeerMu.Lock()
	currentPeer, peerErr := loadPoolPeer(s)
	state, stateErr := loadPoolVMs(s)
	vmBytes, marshalErr := json.Marshal(state.VMs)
	validRecord := false
	if stateErr == nil && marshalErr == nil && string(vmBytes) == string(replica.VMs) {
		record, ok := state.VMs[vmID]
		if ok {
			for _, snapshot := range record.Snapshots {
				if snapshot.ID == updated.ID {
					validRecord = reflect.DeepEqual(snapshot, updated) && record.PendingRecovery == nil
					break
				}
			}
		}
	}
	valid := peerErr == nil && stateErr == nil && marshalErr == nil && validRecord &&
		currentPeer.Membership.Valid() == nil && currentPeer.Membership.MasterHostID == s.State.HostID &&
		reflect.DeepEqual(currentPeer.Membership, peer.Membership) && currentPeer.AuthorityGeneration == replica.Generation &&
		currentPeer.AuthorityDigest == digest && poolAuthorityWritable(currentPeer) == nil
	poolPeerMu.Unlock()
	poolVMMu.Unlock()
	if !valid {
		return errors.New("snapshot authority changed after member acknowledgement")
	}
	return nil
}

func settlePoolVMSnapshotStateLocked(s *Store, state *poolVMState, membership protocol.PoolMembership, owner string, evidence protocol.PoolVMRecoveryEvidence) error {
	if membership.Valid() != nil || membership.PoolID != s.State.PoolID || membership.MasterHostID != s.State.HostID ||
		evidence.Valid() != nil || evidence.Reservation.PoolID != membership.PoolID || evidence.Reservation.OwnerHostID != owner {
		return errors.New("snapshot settlement evidence does not match current pool authority")
	}
	if _, err := settlePoolVMRecoveryInventory(state, owner, map[string]protocol.PoolVMRecoveryEvidence{evidence.Reservation.OperationID: evidence}); err != nil {
		return err
	}
	record, ok := state.VMs[evidence.Reservation.VMID]
	if !ok || record.LastSettledRecovery == nil || !reflect.DeepEqual(*record.LastSettledRecovery, evidence) {
		return errors.New("snapshot settlement has no matching pending master reservation")
	}
	vmBytes, err := json.Marshal(state.VMs)
	if err != nil || len(vmBytes) > protocol.PoolReplicaLimit {
		return errors.New("settled snapshot authority exceeds the replica limit")
	}
	peer, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	replica, _, err := buildPoolAuthorityReplicaForMembership(s, membership, vmBytes)
	if err != nil || poolAuthorityReplicaFitsAddressedBodies(s, replica, peer.CAKeyPEM) != nil {
		return errors.New("settled snapshot authority exceeds the peer body limit")
	}
	return savePoolVMs(s, *state)
}

func poolVMSnapshotSettlementPeer(ctx context.Context, s *Store, evidence protocol.PoolVMRecoveryEvidence) error {
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	request := poolVMRequest{VMID: evidence.Reservation.VMID, Action: "snapshot-settle", RecoveryEvidence: &evidence}
	body, err := json.Marshal(request)
	if err != nil || len(body) > 64<<10 {
		return errors.New("snapshot settlement exceeds the bounded peer request size")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+evidence.Reservation.VMID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("pool master refused snapshot settlement")
	}
	var ack poolVMRequest
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&ack) != nil || !validPoolVMSnapshotSettlementAck(ack, evidence) {
		return errors.New("pool master did not acknowledge the exact snapshot settlement")
	}
	return nil
}

func validPoolVMSnapshotSettlementAck(ack poolVMRequest, evidence protocol.PoolVMRecoveryEvidence) bool {
	return ack.VMID == evidence.Reservation.VMID && ack.Action == "snapshot-settle" && ack.SnapshotSettled &&
		ack.RecoveryEvidence != nil && reflect.DeepEqual(*ack.RecoveryEvidence, evidence)
}

func updatePoolVMMembership(s *Store, membership protocol.PoolMembership) error {
	if membership.Valid() != nil || membership.PoolID != s.State.PoolID {
		return errors.New("pool membership refresh is invalid")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	if state.Membership.Valid() != nil || membership.MasterHostID != state.Membership.MasterHostID || membership.Generation <= state.Membership.Generation {
		return errors.New("pool membership refresh is stale")
	}
	var current, updated protocol.PoolMember
	for _, member := range state.Membership.Members {
		if member.HostID == s.State.HostID {
			current = member
		}
	}
	for _, member := range membership.Members {
		if member.HostID == s.State.HostID {
			updated = member
		}
	}
	if current.HostID == "" || updated.HostID == "" || current.CertificateSHA256 != updated.CertificateSHA256 {
		return errors.New("pool membership refresh changes local peer identity")
	}
	state.Membership = membership
	return savePoolPeer(s, state)
}

func localPoolVMInventory(ctx context.Context, s *Store, generation uint64) (poolVMInventory, error) {
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	if !managedVMChecksReady(checks) {
		return poolVMInventory{}, errors.New("managed VM inventory is unavailable")
	}
	storage := map[string]protocol.StorageIdentity{}
	for _, r := range managed.Storage {
		if protocol.IsPoolSharedStorage(r) {
			storage[r.ID] = r.Identity
		}
	}
	observed := make(map[string]string, len(managed.VMs))
	startOutcomes := make([]poolVMStartOutcome, 0)
	updates := make(map[string]vmhost.VMUpdateEvidence)
	for _, vm := range managed.VMs {
		state, err := vmhost.ManagedVMState(ctx, vm.ID)
		if err != nil {
			return poolVMInventory{}, errors.New("managed VM runtime observation is unavailable")
		}
		observed[vm.ID] = state
		if state == "shut off" {
			// A locally confirmed owner stop invalidates any unconsumed admission;
			// ownership itself remains on the master record.
			_ = vmhost.RemovePoolVMStartPermit(s.dir, vm.ID)
		}
		// Every definition now has a master-owned placement record. The journal
		// evidence remains read-only and only settles an exact next revision.
		// A VM without exact evidence (for example an unresolved newer update) is
		// still published, without an Updates entry, at its highest succeeded
		// revision, which the master already records; that VM's pending-update
		// guards keep it blocked. If a published definition ever differed from the
		// master record without evidence, the master would still refuse the whole
		// publication, as before.
		if vm.Revision > 0 {
			evidence, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).SuccessfulVMUpdateEvidence(ctx, vm.ID)
			if err == nil && reflect.DeepEqual(evidence.Current, vm) {
				updates[vm.ID] = evidence
			}
		}
	}
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	for _, outcome := range managed.Outcomes {
		if (outcome.Status != "failed" && outcome.Status != "succeeded") || outcome.OperationID == "" {
			continue
		}
		command, ok := engine.JournaledCommand(outcome.OperationID)
		if !ok || command.OperationID != outcome.OperationID {
			continue
		}
		vmID, status := "", outcome.Status
		switch command.Kind {
		case "vm-power":
			if command.Power == nil || command.Power.Action != "start" || command.Power.PoolID != s.State.PoolID || command.Power.HostID != s.State.HostID {
				continue
			}
			vmID = command.Power.VMID
		case "vm-create":
			if command.VM == nil || !command.VM.StartAfterCreate || outcome.Status != "succeeded" ||
				command.VM.PoolID != s.State.PoolID || command.VM.HostID != s.State.HostID {
				continue
			}
			vmID = command.VM.ID
			start, found, err := engine.CreateStartOutcome(ctx, command.OperationID)
			if err != nil {
				return poolVMInventory{}, errors.New("managed VM automatic-start journal is unavailable")
			}
			if !found || start.OperationID != command.OperationID+":start" || start.Kind != "vm-power" || start.ID != vmID {
				continue
			}
			status = start.Status
		default:
			continue
		}
		if status != "failed" {
			continue
		}
		if outcome.ID != vmID || observed[vmID] != "shut off" {
			continue
		}
		startOutcomes = append(startOutcomes, poolVMStartOutcome{VMID: vmID, OperationID: outcome.OperationID, Status: status})
	}
	slices.SortFunc(startOutcomes, func(a, b poolVMStartOutcome) int {
		if order := strings.Compare(a.VMID, b.VMID); order != 0 {
			return order
		}
		if order := strings.Compare(a.OperationID, b.OperationID); order != 0 {
			return order
		}
		return strings.Compare(a.Status, b.Status)
	})
	aborted := make(map[string]vmhost.VMUpdateEvidence)
	for _, outcome := range managed.Outcomes {
		if outcome.Kind != "vm-update" || outcome.Status != "failed" {
			continue
		}
		cmd, ok := engine.JournaledCommand(outcome.OperationID)
		if !ok || cmd.VM == nil {
			continue
		}
		if evidence, err := engine.FailedVMUpdateEvidence(ctx, cmd.VM.ID, outcome.OperationID, *cmd.VM); err == nil {
			aborted[outcome.OperationID] = evidence
		}
	}
	deleted := make([]string, 0, len(managed.DeletedVMs))
	for _, tombstone := range managed.DeletedVMs {
		if tombstone.ID != "" && tombstone.HostID == s.State.HostID && tombstone.PoolID == s.State.PoolID {
			deleted = append(deleted, tombstone.ID)
		}
	}
	// A create proven never to have run here releases its admission record the same way.
	neverCreated, err := engine.NeverCreatedVMs()
	if err != nil {
		return poolVMInventory{}, errors.New("managed VM journal is unavailable")
	}
	deleted = append(deleted, neverCreated...)
	recoveries, err := poolVMRecoveryJournalEvidence(ctx, engine, s.State.PoolID, s.State.HostID, managed.VMs)
	if err != nil {
		return poolVMInventory{}, errors.New("managed snapshot recovery journal is unavailable")
	}
	detectedOS, err := retainLocalDetectedOS(s, managed.VMs, nil)
	if err != nil {
		return poolVMInventory{}, err
	}
	return poolVMInventory{DetectedOS: detectedOS, HostID: s.State.HostID, Generation: generation, VMs: managed.VMs, Storage: storage, Observed: observed, StartOutcomes: startOutcomes, Updates: updates, Aborted: aborted, Recoveries: recoveries, Deleted: deleted, ObservedAt: time.Now().UTC()}, nil
}

func poolVMRecoveryJournalEvidence(ctx context.Context, engine *vmhost.ManagedEngine, poolID, hostID string, vms []protocol.VMCreateSpec) (map[string]protocol.PoolVMRecoveryEvidence, error) {
	reservations, err := engine.PoolVMRecoveryReservations()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]protocol.VMCreateSpec, len(vms))
	for _, vm := range vms {
		byID[vm.ID] = vm
	}
	evidence := make(map[string]protocol.PoolVMRecoveryEvidence)
	for _, reservation := range reservations {
		vm, ok := byID[reservation.VMID]
		if !ok || reservation.PoolID != poolID || reservation.OwnerHostID != hostID {
			continue // Old journal receipts can outlive a VM deletion or pool transfer.
		}
		if reservation.Valid() != nil {
			return nil, errors.New("managed snapshot recovery reservation is invalid")
		}
		specDigest, diskDigest, err := poolVMRecoveryDigests(vm)
		if err != nil {
			return nil, err
		}
		if reservation.Kind == "snapshot-revert" {
			if vm.Revision != reservation.SpecRevision && vm.Revision != reservation.SpecRevision+1 {
				continue
			}
			if vm.Revision == reservation.SpecRevision && (reservation.SpecSHA256 != specDigest || reservation.DiskIdentitySHA256 != diskDigest) {
				continue
			}
		} else if reservation.Kind == "snapshot-delete" {
			if reservation.SpecRevision != vm.Revision || reservation.SpecSHA256 != specDigest || reservation.DiskIdentitySHA256 != diskDigest {
				continue
			}
		} else if reservation.SpecRevision != vm.Revision || reservation.SpecSHA256 != specDigest || reservation.DiskIdentitySHA256 != diskDigest {
			continue // A terminal operation may describe an older VM definition.
		}
		terminal, err := engine.PoolVMRecoveryEvidence(ctx, reservation)
		if err != nil {
			continue // In-progress, partial, or unknown remains held at the master.
		}
		if terminal.Valid() != nil || terminal.Reservation != reservation {
			return nil, errors.New("managed snapshot recovery journal evidence is invalid")
		}
		if reservation.Kind == "snapshot-revert" {
			if terminal.Outcome == "completed" && !poolVMRevertInventoryMatchesEvidence(reservation, vm, terminal, specDigest, diskDigest) {
				continue
			}
			if terminal.Outcome == "failed-before-effects" && (reservation.SpecRevision != vm.Revision || reservation.SpecSHA256 != specDigest || reservation.DiskIdentitySHA256 != diskDigest) {
				continue
			}
		}
		evidence[reservation.OperationID] = terminal
	}
	return evidence, nil
}

func poolVMRevertInventoryMatchesEvidence(reservation protocol.PoolVMRecoveryReservation, vm protocol.VMCreateSpec, evidence protocol.PoolVMRecoveryEvidence, specDigest, diskDigest string) bool {
	if evidence.ResultSpec == nil || evidence.Valid() != nil || evidence.Reservation != reservation {
		return false
	}
	if vm.Revision == reservation.SpecRevision {
		return specDigest == reservation.SpecSHA256 && diskDigest == reservation.DiskIdentitySHA256
	}
	return vm.Revision == reservation.SpecRevision+1 && reflect.DeepEqual(*evidence.ResultSpec, vm)
}

func settlePoolVMRecoveryInventory(state *poolVMState, owner string, recoveries map[string]protocol.PoolVMRecoveryEvidence) ([]protocol.PoolVMRecoveryEvidence, error) {
	settled := make([]protocol.PoolVMRecoveryEvidence, 0, len(recoveries))
	for operationID, evidence := range recoveries {
		if evidence.Reservation.OperationID != operationID || evidence.Valid() != nil {
			return nil, errors.New("snapshot recovery inventory contains invalid journal evidence")
		}
		vmID := evidence.Reservation.VMID
		record, ok := state.VMs[vmID]
		if !ok || record.HostID != owner {
			continue // A terminal receipt can outlive a deleted or transferred VM.
		}
		if record.PendingRecovery == nil {
			if record.LastSettledRecovery == nil || record.LastSettledRecovery.Reservation.OperationID != operationID {
				continue // Obsolete terminal history is not current master authority.
			}
			if err := settlePoolVMRecovery(&record, owner, evidence); err != nil {
				return nil, err
			}
			continue // Already terminal; no fresh replica confirm required.
		} else if record.PendingRecovery.OperationID != operationID {
			continue // Retained terminal history for an earlier operation.
		}
		if err := settlePoolVMRecovery(&record, owner, evidence); err != nil {
			return nil, err
		}
		state.VMs[vmID] = record
		settled = append(settled, evidence)
	}
	return settled, nil
}

func vmUsesInventorySharedStorage(vm protocol.VMCreateSpec, storage map[string]protocol.StorageIdentity) bool {
	for _, disk := range vm.Disks {
		if _, ok := storage[disk.StorageID]; ok {
			return true
		}
	}
	return false
}
func recordPoolVMInventory(s *Store, membership protocol.PoolMembership, inv poolVMInventory) error {
	if inv.HostID == "" || inv.Generation != membership.Generation {
		return errPoolVMInventoryStale
	}
	ready := false
	for _, m := range membership.Members {
		ready = ready || (m.HostID == inv.HostID && m.Status == "ready")
	}
	if !ready {
		return errors.New("VM inventory host is not ready")
	}
	if len(inv.DetectedOS) > len(inv.VMs) {
		return errors.New("invalid detected OS inventory")
	}
	byID := make(map[string]protocol.VMCreateSpec, len(inv.VMs))
	for _, vm := range inv.VMs {
		if previous, ok := byID[vm.ID]; ok && previous.ID != "" {
			byID[vm.ID] = protocol.VMCreateSpec{}
			continue
		}
		byID[vm.ID] = vm
	}
	for id, detected := range inv.DetectedOS {
		vm := byID[id]
		if vm.ID == "" || vm.HostID != inv.HostID || vm.PoolID != membership.PoolID || detected.Valid() != nil {
			return errors.New("invalid detected OS inventory")
		}
	}
	// The receiving master timestamps its own accepted snapshot; a member-provided
	// time cannot make an old generation appear current.
	inv.ObservedAt = time.Now().UTC()
	var settled []protocol.PoolVMRecoveryEvidence
	seedErr, err := func() (error, error) {
		poolVMMu.Lock()
		defer poolVMMu.Unlock()
		state, err := loadPoolVMs(s)
		if err != nil {
			return nil, err
		}
		for _, id := range inv.Deleted {
			record, exists := state.VMs[id]
			if exists && record.HostID == inv.HostID && poolVMHasSnapshotState(record) {
				return nil, errPoolVMSnapshotTombstone
			}
		}
		if state.Inventory == nil {
			state.Inventory = map[string]poolVMInventory{}
		}
		state.Inventory[inv.HostID] = inv
		// A host that is no longer part of the pool is no longer part of its picture. Its row
		// stays in membership for ever, so its snapshot is dropped here instead: otherwise it
		// keeps holding pool networks and ISOs against removal, and pool-vms.json grows without
		// bound. Dropping an observation removes no authority; owner records are untouched, and
		// the eject that produced the row already proved the host owned none.
		for host := range state.Inventory {
			member, known := poolMemberByID(membership, host)
			if !known || !poolVMInventoryMember(member) {
				delete(state.Inventory, host)
			}
		}
		// The snapshot above is an observation and is always stored; the owner records below are
		// the replicated `vms` section. Keep the peer lock through their commit so a freeze cannot
		// race between checking authority and settling an owner record.
		poolPeerMu.Lock()
		defer poolPeerMu.Unlock()
		peer, peerErr := loadPoolPeer(s)
		if peerErr != nil || peer.Membership.Valid() != nil || peer.Membership.MasterHostID != s.State.HostID ||
			peer.Membership.PoolID != membership.PoolID || peer.Membership.Generation != membership.Generation {
			clearPoolVMStartFailures(&state, inv.HostID)
			if err = savePoolVMs(s, state); err != nil {
				return nil, err
			}
			return nil, errPoolVMInventoryStale
		}
		if refusal := poolAuthorityWritable(peer); refusal != nil {
			clearPoolVMStartFailures(&state, inv.HostID)
			if err = savePoolVMs(s, state); err != nil {
				return nil, err
			}
			return nil, errPoolVMOwnersDeferred
		}
		// Owner records are pruned from this host's own delete tombstones before the seed
		// pass, so a deleted guest stops holding a pool fence and pool-vms.json stops growing
		// without bound. The pool-wide network/ISO dependency check keeps reading the
		// published inventory snapshots, which this never touches.
		prunePoolVMOwners(&state, inv.HostID)
		seedErr := seedPoolVMOwners(s, membership, &state, inv.HostID)
		if seedErr == nil {
			var settleErr error
			settled, settleErr = settlePoolVMRecoveryInventory(&state, inv.HostID, inv.Recoveries)
			if settleErr != nil {
				return nil, settleErr
			}
		}
		clearPoolVMStartFailures(&state, inv.HostID)
		// A partial current-generation snapshot is durable evidence, not an execution
		// admission.  Retain it so later ready members can complete the same generation;
		// owner records remain untouched until seedPoolVMOwners succeeds.
		if err = savePoolVMs(s, state); err != nil {
			return nil, err
		}
		return seedErr, nil
	}()
	if err != nil {
		return err
	}
	for i := range settled {
		evidence := settled[i]
		if confirmErr := confirmPoolVMSnapshotReplica(context.Background(), s, nil, &evidence); confirmErr != nil {
			return confirmErr
		}
	}
	return seedErr
}

func clearPoolVMStartFailures(state *poolVMState, hostID string) {
	if state == nil || state.Inventory == nil {
		return
	}
	inv, ok := state.Inventory[hostID]
	if !ok || len(inv.StartOutcomes) == 0 {
		return
	}
	inv.StartOutcomes = nil
	state.Inventory[hostID] = inv
}

// completePoolVMReplicaRejoin drops the ACK exclusion only after an excluded member has
// published an accepted current-generation inventory and acknowledged the exact current
// authority replica. The push is deliberately outside the VM and peer locks.
func completePoolVMReplicaRejoin(ctx context.Context, s *Store, hostID string) {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return
	}
	member, ok := poolMemberByID(membership, hostID)
	if !ok || !member.ReplicaAckExcluded || member.Status != "ready" || hostID == s.State.HostID {
		return
	}
	_ = PushPoolAuthorityReplicas(ctx, s, 20*time.Second)
	_, digest, err := buildPoolAuthorityReplica(s)
	if err != nil {
		return
	}

	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	peer, err := loadPoolPeer(s)
	state, stateErr := loadPoolVMs(s)
	if err != nil || stateErr != nil || peer.Membership.Valid() != nil {
		return
	}
	vmSection, err := json.Marshal(state.VMs)
	if err != nil {
		return
	}
	_, currentDigest, err := buildPoolAuthorityReplicaForMembership(s, peer.Membership, vmSection)
	if err != nil || !poolReplicaRejoinStillCurrent(peer, membership, digest, currentDigest, state, hostID, s.State.HostID) ||
		!poolReplicaPostAckMatches(s, hostID, peer.AuthorityGeneration, digest) {
		return
	}
	for i := range peer.Membership.Members {
		if peer.Membership.Members[i].HostID == hostID {
			peer.Membership.Members[i].ReplicaAckExcluded = false
			break
		}
	}
	// This authority-only field is outside membership generation. The changed signed
	// replica content advances AuthorityGeneration on the next push before any new ACK.
	_ = savePoolPeer(s, peer)
}

func poolReplicaRejoinStillCurrent(peer poolPeerState, captured protocol.PoolMembership, digest, currentDigest string, state poolVMState, hostID, self string) bool {
	member, ok := poolMemberByID(peer.Membership, hostID)
	return peer.Membership.Valid() == nil && peer.Membership.MasterHostID == self &&
		reflect.DeepEqual(peer.Membership, captured) && peer.AuthorityDigest == digest && currentDigest == digest &&
		ok && member.ReplicaAckExcluded && member.Status == "ready" &&
		state.Inventory[hostID].HostID == hostID && state.Inventory[hostID].Generation == peer.Membership.Generation
}

// poolVMInventoryMember reports whether a membership row can own or run a pool VM now, which
// is what the completeness rule is a question about. Every status but "ejected" counts: a
// pending or failed member may have been ready a moment ago and may still be running guests,
// so a picture without it is genuinely incomplete and nothing may be concluded from it. An
// ejected host is the one row that cannot hold a pool VM record by construction — the eject
// is refused from the master's own records while the target owns one (poolEjectRefusal) — and
// its row is kept for ever, so counting it made every inventory permanently incomplete and no
// shared VM could ever be settled as stopped or started again (BUG-0358).
func poolVMInventoryMember(member protocol.PoolMember) bool { return member.Status != "ejected" }

func seedPoolVMOwners(s *Store, membership protocol.PoolMembership, state *poolVMState, observedHost string) error {
	members := map[string]bool{}
	for _, member := range membership.Members {
		if !poolVMInventoryMember(member) {
			continue
		}
		members[member.HostID] = true
		if member.Status != "ready" || state.Inventory[member.HostID].Generation != membership.Generation {
			return fmt.Errorf("%w: host %s (%s) has not reported at generation %d", errPoolVMInventoryIncomplete, member.HostID, member.Status, membership.Generation)
		}
	}
	masterManaged, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(context.Background())
	if !managedVMChecksReady(checks) {
		return errors.New("master storage inventory is unavailable")
	}
	masterStorage := map[string]protocol.StorageRegistration{}
	for _, storage := range masterManaged.Storage {
		if protocol.IsPoolSharedStorage(storage) {
			masterStorage[storage.ID] = storage
		}
	}
	seen := map[string]string{}
	for host, inv := range state.Inventory {
		if !members[host] {
			continue // A host that left the pool seeds nothing; its VMs left with it.
		}
		for _, vm := range inv.VMs {
			if vm.ID == "" || vm.PoolID != s.State.PoolID || vm.HostID != host || protocol.ValidateVMSpec(vm) != nil {
				return errors.New("invalid VM definition in pool inventory")
			}
			shared := false
			for _, disk := range vm.Disks {
				if observed, ok := inv.Storage[disk.StorageID]; ok {
					expected, registered := masterStorage[disk.StorageID]
					if !registered || !vmhost.StorageIdentityMatchesExpected(expected.Identity, observed) {
						return errors.New("shared VM inventory storage identity does not match master registration")
					}
					shared = true
				}
			}
			old, exists := state.VMs[vm.ID]
			if shared && inv.Observed[vm.ID] != "running" && inv.Observed[vm.ID] != "shut off" {
				return errors.New("shared VM runtime observation is invalid")
			}
			if prior := seen[vm.ID]; prior != "" && prior != host {
				return errors.New("duplicate shared VM identity in pool inventory")
			}
			seen[vm.ID] = host
			if exists && old.HostID != host {
				return errors.New("shared VM owner conflicts with current inventory")
			}
			if !exists {
				vmState := "defined"
				if inv.Observed[vm.ID] == "running" {
					vmState = "running"
				} else if inv.Observed[vm.ID] == "shut off" {
					vmState = "stopped"
				}
				state.VMs[vm.ID] = poolVMRecord{PoolID: s.State.PoolID, VMID: vm.ID, HostID: host, State: vmState, Autostart: vm.Autostart, Epoch: 1, Spec: vm}
			} else if old.Spec.ID != "" && !reflect.DeepEqual(old.Spec, vm) {
				evidence, proven := inv.Updates[vm.ID]
				if !proven {
					return errors.New("shared VM definition conflicts with durable inventory")
				}
				if err := settlePoolVMInventoryDefinition(&old, host, vm, &evidence); err != nil {
					return err
				}
				state.VMs[vm.ID] = old
			} else if old.PendingUpdate != nil && host == observedHost && abortPoolVMUpdate(&old, host, inv.Aborted[old.PendingUpdate.OperationID]) == nil {
				// The owner proved the reserved update failed unapplied; release the reservation.
				state.VMs[vm.ID] = old
			} else if old.HostID == host && host == observedHost && old.State == "starting" && old.PendingStartOperationID != "" &&
				old.PendingUpdate == nil && old.PendingRecovery == nil {
				if inv.Observed[vm.ID] == "running" {
					// A fresh observation of the exact owner's currently running guest is
					// sufficient to record its present execution state; it cannot authorize
					// a second start. Shutoff still needs exact terminal failure evidence.
					old.State, old.PendingStartOperationID = "running", ""
					state.VMs[vm.ID] = old
				} else if inv.Observed[vm.ID] == "shut off" {
					for _, outcome := range inv.StartOutcomes {
						if outcome.VMID == vm.ID && outcome.OperationID == old.PendingStartOperationID && outcome.Status == "failed" {
							old.State, old.PendingStartOperationID = "stopped", ""
							state.VMs[vm.ID] = old
							break
						}
					}
				}
			} else if old.HostID == host && host == observedHost && old.State != "starting" {
				// Only the just-authenticated owner's fresh native observation may
				// reconcile execution state. Replaying another host's cached
				// inventory must never demote or promote this VM.
				switch inv.Observed[vm.ID] {
				case "running":
					if old.State != "running" {
						old.State = "running"
						state.VMs[vm.ID] = old
					}
				case "shut off":
					if old.State != "stopped" {
						old.State = "stopped"
						state.VMs[vm.ID] = old
					}
				}
			}
			if detected, ok := inv.DetectedOS[vm.ID]; ok && host == observedHost {
				record := state.VMs[vm.ID]
				if detected.Valid() != nil {
					return errors.New("invalid detected OS inventory")
				}
				if record.DetectedOS == nil || detected.ObservedAt.After(record.DetectedOS.ObservedAt) {
					record.DetectedOS = &detected
					state.VMs[vm.ID] = record
				}
			}
		}
	}
	return nil
}
