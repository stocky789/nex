// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Pool VM execution is deliberately a small, master-owned fence.  Repository
// reservations protect a storage configuration; they cannot say which host may
// run one particular guest.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolVMPath = "/api/v1/pool/vms/"

var (
	ErrSnapshotMetadataNotFound = errors.New("pool VM snapshot not found")
	ErrSnapshotMetadataConflict = errors.New("snapshot catalog name is already in use")
	ErrSnapshotMetadataPending  = errors.New("snapshot metadata cannot change while recovery is reserved")
	errSnapshotMetadataOwner    = errors.New("snapshot metadata update does not match the authenticated VM owner")
)

type poolVMRecord struct {
	PoolID            string `json:"pool_id"`
	VMID              string `json:"vm_id"`
	HostID            string `json:"host_id"`
	CreateOperationID string `json:"create_operation_id,omitempty"`
	State             string `json:"state"` // defined, starting, running, stopped; never inferred from time.
	Autostart         bool   `json:"autostart"`
	Epoch             uint64 `json:"epoch"`
	// PendingStartOperationID binds the durable starting state to the exact
	// admitted owner command until a fresh native observation settles it.
	PendingStartOperationID string `json:"pending_start_operation_id,omitempty"`
	// Spec is the master-owned inventory copy used to re-adopt a pool VM after a
	// Controller replacement.  It is written only from a complete, generation
	// matched host inventory or a validated new vm-create request.
	Spec       protocol.VMCreateSpec `json:"spec"`
	DetectedOS *protocol.DetectedOS  `json:"detected_os,omitempty"`
	// PendingUpdate is a master-owned reservation for one exact next revision.
	// The committed Spec never changes until the owner proves the matching local
	// managed journal succeeded; a timeout therefore blocks rather than adopts
	// an arbitrary later inventory report.
	PendingUpdate *poolVMPendingUpdate `json:"pending_update,omitempty"`
	// PendingRecovery is the master-owned fence for one exact snapshot transition.
	// Native chains and progress stay in the executing Core's managed journal.
	PendingRecovery *protocol.PoolVMRecoveryReservation `json:"pending_recovery,omitempty"`
	PendingSnapshot *protocol.PoolVMSnapshot            `json:"pending_snapshot,omitempty"`
	// PendingRecoveryResultSpec is the master's independently derived next VM
	// definition for a reserved revert. It exists only until exact settlement.
	PendingRecoveryResultSpec *protocol.VMCreateSpec    `json:"pending_recovery_result_spec,omitempty"`
	Snapshots                 []protocol.PoolVMSnapshot `json:"snapshots,omitempty"`
	// LastSettledRecovery is one bounded exact replay receipt, like LastAbortedUpdate.
	LastSettledRecovery *protocol.PoolVMRecoveryEvidence `json:"last_settled_recovery,omitempty"`
	// LastAbortedUpdate is the single bounded replay receipt retained after an
	// exact failed update-abort response was lost.
	LastAbortedUpdate *poolVMPendingUpdate `json:"last_aborted_update,omitempty"`
}

func poolVMSnapshotUsesResource(vm poolVMRecord, resource, id string) bool {
	snapshots := append([]protocol.PoolVMSnapshot(nil), vm.Snapshots...)
	if vm.PendingSnapshot != nil {
		snapshots = append(snapshots, *vm.PendingSnapshot)
	}
	// Pending revert/delete has no PendingSnapshot: gate on catalog (captured) deps
	// plus current/result specs, not every resource in the pool.
	if vm.PendingRecovery != nil && vm.PendingSnapshot == nil {
		for _, spec := range []protocol.VMCreateSpec{vm.Spec} {
			deps, err := protocol.PoolVMSnapshotDependenciesForSpec(spec)
			if err != nil {
				return true
			}
			snapshots = append(snapshots, protocol.PoolVMSnapshot{Dependencies: &deps})
		}
		if vm.PendingRecoveryResultSpec != nil {
			deps, err := protocol.PoolVMSnapshotDependenciesForSpec(*vm.PendingRecoveryResultSpec)
			if err != nil {
				return true
			}
			snapshots = append(snapshots, protocol.PoolVMSnapshot{Dependencies: &deps})
		}
	}
	for _, snapshot := range snapshots {
		if snapshot.Dependencies == nil {
			return true
		}
		var ids []string
		switch resource {
		case "storage":
			ids = snapshot.Dependencies.StorageIDs
		case "network":
			ids = snapshot.Dependencies.NetworkIDs
		case "media":
			ids = snapshot.Dependencies.MediaIDs
		default:
			return true
		}
		if slices.Contains(ids, id) {
			return true
		}
	}
	return false
}

var errPoolVMRecoverySettled = errors.New("snapshot recovery operation is already terminal")

func poolVMRecoveryDigests(spec protocol.VMCreateSpec) (string, string, error) {
	if err := protocol.ValidateVMSpec(spec); err != nil {
		return "", "", err
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		return "", "", err
	}
	diskBytes, err := json.Marshal(spec.Disks)
	if err != nil {
		return "", "", err
	}
	specDigest := sha256.Sum256(specBytes)
	diskDigest := sha256.Sum256(diskBytes)
	return hex.EncodeToString(specDigest[:]), hex.EncodeToString(diskDigest[:]), nil
}

func poolVMRecoveryReservationFor(record poolVMRecord, operationID, kind, intentSHA256 string, ownerOperationOrder uint64) (protocol.PoolVMRecoveryReservation, error) {
	specDigest, diskDigest, err := poolVMRecoveryDigests(record.Spec)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, errors.New("snapshot recovery VM definition is unavailable")
	}
	reservation := protocol.PoolVMRecoveryReservation{
		OperationID: operationID, Kind: kind, OwnerOperationOrder: ownerOperationOrder, PoolID: record.PoolID, VMID: record.VMID,
		OwnerHostID: record.HostID, Epoch: record.Epoch, SpecRevision: record.Spec.Revision,
		SpecSHA256: specDigest, DiskIdentitySHA256: diskDigest, IntentSHA256: intentSHA256,
	}
	if kind != "snapshot-revert" && kind != "snapshot-delete" && err == nil {
		err = reservation.Valid()
	}
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	return reservation, nil
}

func reservePoolVMRecovery(record *poolVMRecord, reservation protocol.PoolVMRecoveryReservation) error {
	if err := reservation.Valid(); err != nil || record == nil || record.PoolID != reservation.PoolID ||
		record.VMID != reservation.VMID || record.HostID != reservation.OwnerHostID || record.Epoch != reservation.Epoch ||
		record.Spec.Revision != reservation.SpecRevision {
		return errors.New("snapshot recovery reservation does not match the durable pool VM owner")
	}
	want, err := poolVMRecoveryReservationFor(*record, reservation.OperationID, reservation.Kind, reservation.IntentSHA256, reservation.OwnerOperationOrder)
	got := reservation
	got.AdmissionRefusal = ""
	if reservation.Kind == "snapshot-revert" || reservation.Kind == "snapshot-delete" {
		got.TargetSnapshotID = ""
		got.TargetSnapshotSHA256 = ""
		got.ResultSpecSHA256 = ""
	}
	if err != nil || want != got {
		return errors.New("snapshot recovery reservation does not match the durable VM definition")
	}
	if reservation.Kind == "snapshot-revert" {
		if record.PendingSnapshot != nil || record.PendingRecoveryResultSpec == nil ||
			poolVMResultSpecMatchesReservation(reservation, *record.PendingRecoveryResultSpec) != nil ||
			!poolVMHasSnapshotDescriptor(record.Snapshots, reservation.TargetSnapshotID, reservation.TargetSnapshotSHA256) {
			return errors.New("snapshot revert reservation does not match its target or derived definition")
		}
	} else if reservation.Kind == "snapshot-delete" {
		if record.PendingSnapshot != nil || record.PendingRecoveryResultSpec != nil ||
			!poolVMHasSnapshotDescriptor(record.Snapshots, reservation.TargetSnapshotID, reservation.TargetSnapshotSHA256) {
			return errors.New("snapshot delete reservation does not match its target catalog descriptor")
		}
	} else if record.PendingRecoveryResultSpec != nil {
		return errors.New("unexpected pending snapshot revert definition")
	}
	if record.PendingRecovery != nil {
		if *record.PendingRecovery == reservation {
			return nil
		}
		return errors.New("another snapshot recovery operation is pending")
	}
	if record.LastSettledRecovery != nil {
		last := record.LastSettledRecovery.Reservation
		if last.OperationID == reservation.OperationID {
			last.AdmissionRefusal = ""
			if last == got {
				return errPoolVMRecoverySettled
			}
			return errors.New("snapshot recovery operation ID was already used for another intent")
		}
		if last.OwnerHostID == record.HostID && reservation.OwnerOperationOrder != 0 {
			if record.Epoch < last.Epoch {
				return errors.New("snapshot recovery owner epoch moved backwards")
			}
			if reservation.OwnerOperationOrder <= last.OwnerOperationOrder {
				return errors.New("snapshot recovery operation order is stale")
			}
		}
	}
	for _, snapshot := range record.Snapshots {
		if snapshot.ID == reservation.OperationID {
			return errors.New("snapshot operation ID is already in the master catalog")
		}
	}
	if record.PendingUpdate != nil {
		return errors.New("VM definition update is pending reconciliation")
	}
	if record.State != "defined" && record.State != "stopped" && record.State != "running" {
		return errors.New("VM execution state is not settled for snapshot recovery")
	}
	record.PendingRecovery = &reservation
	record.LastSettledRecovery = nil
	return nil
}

func poolVMResultSpecMatchesReservation(reservation protocol.PoolVMRecoveryReservation, spec protocol.VMCreateSpec) error {
	specSHA, _, err := poolVMRecoveryDigests(spec)
	if err != nil || reservation.Kind != "snapshot-revert" || spec.ID != reservation.VMID || spec.PoolID != reservation.PoolID ||
		spec.HostID != reservation.OwnerHostID || spec.Revision != reservation.SpecRevision+1 || specSHA != reservation.ResultSpecSHA256 {
		return errors.New("snapshot revert result definition does not match its reservation")
	}
	return nil
}

func poolVMRevertReservationMatchesTarget(reservation protocol.PoolVMRecoveryReservation, current, captured protocol.VMCreateSpec, target protocol.PoolVMSnapshot) (protocol.VMCreateSpec, error) {
	if reservation.Kind != "snapshot-revert" || reservation.TargetSnapshotID != target.ID || target.VMID != reservation.VMID ||
		!poolVMHasSnapshotDescriptor([]protocol.PoolVMSnapshot{target}, reservation.TargetSnapshotID, reservation.TargetSnapshotSHA256) {
		return protocol.VMCreateSpec{}, errors.New("snapshot revert reservation target does not match its descriptor")
	}
	capturedSpecSHA, capturedDiskSHA, err := poolVMRecoveryDigests(captured)
	dependencies, depErr := protocol.PoolVMSnapshotDependenciesForSpec(captured)
	if err != nil || depErr != nil || captured.Revision != target.SpecRevision || capturedSpecSHA != target.SpecSHA256 || capturedDiskSHA != target.DiskIdentitySHA256 ||
		target.Dependencies == nil || !reflect.DeepEqual(*target.Dependencies, dependencies) {
		return protocol.VMCreateSpec{}, errors.New("snapshot revert captured VM definition differs from its catalog descriptor")
	}
	next, err := protocol.PoolVMSnapshotRevertSpec(captured, current)
	if err != nil {
		return protocol.VMCreateSpec{}, err
	}
	resultSHA, _, err := poolVMRecoveryDigests(next)
	if err != nil || resultSHA != reservation.ResultSpecSHA256 {
		return protocol.VMCreateSpec{}, errors.New("snapshot revert result definition differs from its reservation")
	}
	return next, nil
}

func poolVMHasSnapshotDescriptor(snapshots []protocol.PoolVMSnapshot, id, digest string) bool {
	if id == "" || !lowerHexDigest(digest) {
		return false
	}
	for _, snapshot := range snapshots {
		if snapshot.ID != id {
			continue
		}
		actual, err := protocol.PoolVMSnapshotDescriptorSHA256(snapshot)
		return err == nil && actual == digest
	}
	return false
}

func settlePoolVMRecovery(record *poolVMRecord, owner string, evidence protocol.PoolVMRecoveryEvidence) error {
	if err := evidence.Valid(); err != nil || record == nil || evidence.Reservation.OwnerHostID != owner ||
		record.PoolID != evidence.Reservation.PoolID || record.VMID != evidence.Reservation.VMID || record.HostID != owner {
		return errors.New("snapshot recovery journal evidence does not match the durable pool VM owner")
	}
	if record.PendingRecovery == nil {
		if record.LastSettledRecovery != nil && reflect.DeepEqual(*record.LastSettledRecovery, evidence) {
			return nil
		}
		return errors.New("snapshot recovery journal evidence has no matching reservation")
	}
	if *record.PendingRecovery != evidence.Reservation {
		return errors.New("snapshot recovery journal evidence does not match the pending intent")
	}
	var revertedSpec *protocol.VMCreateSpec
	if evidence.Reservation.Kind == "snapshot-create" {
		if evidence.Outcome == "completed" {
			var dependencies *protocol.PoolVMSnapshotDependencies
			if record.PendingSnapshot != nil && record.PendingSnapshot.Dependencies != nil {
				derived, dependencyErr := protocol.PoolVMSnapshotDependenciesForSpec(record.Spec)
				if dependencyErr != nil {
					return errors.New("snapshot VM resource dependencies are invalid")
				}
				dependencies = &derived
			}
			if record.PendingSnapshot == nil || evidence.Snapshot == nil || evidence.Snapshot.VMID != record.VMID ||
				evidence.Snapshot.ID != record.PendingSnapshot.ID || evidence.Snapshot.Name != record.PendingSnapshot.Name ||
				evidence.Snapshot.Description != record.PendingSnapshot.Description || !evidence.Snapshot.CreatedAt.Equal(record.PendingSnapshot.CreatedAt) ||
				evidence.Snapshot.SpecRevision != record.PendingSnapshot.SpecRevision || evidence.Snapshot.SpecSHA256 != record.PendingSnapshot.SpecSHA256 ||
				evidence.Snapshot.DiskIdentitySHA256 != record.PendingSnapshot.DiskIdentitySHA256 ||
				!reflect.DeepEqual(record.PendingSnapshot.Dependencies, dependencies) || !reflect.DeepEqual(evidence.Snapshot.Dependencies, dependencies) {
				return errors.New("snapshot catalog descriptor does not match the durable reservation")
			}
			if len(record.Snapshots) >= poolVMSnapshotLimit || poolVMSnapshotNameExists(record.Snapshots, evidence.Snapshot.Name) {
				return errors.New("snapshot catalog is full or name is already in use")
			}
			record.Snapshots = append(record.Snapshots, *evidence.Snapshot)
		} else if evidence.Outcome != "failed-before-effects" || record.PendingSnapshot == nil {
			return errors.New("snapshot failure evidence does not match a pending create")
		}
		record.PendingSnapshot = nil
	} else if evidence.Reservation.Kind == "snapshot-revert" {
		reservation := evidence.Reservation
		if record.PendingSnapshot != nil || !poolVMHasSnapshotDescriptor(record.Snapshots, reservation.TargetSnapshotID, reservation.TargetSnapshotSHA256) {
			return errors.New("snapshot revert target differs from the current master catalog")
		}
		if evidence.Outcome == "completed" {
			if record.PendingRecoveryResultSpec == nil || evidence.ResultSpec == nil ||
				!reflect.DeepEqual(*evidence.ResultSpec, *record.PendingRecoveryResultSpec) ||
				poolVMResultSpecMatchesReservation(reservation, *evidence.ResultSpec) != nil {
				return errors.New("snapshot revert result differs from the master's reserved VM definition")
			}
			revertedSpec = evidence.ResultSpec
		} else if evidence.Outcome != "failed-before-effects" || evidence.ResultSpec != nil {
			return errors.New("snapshot revert failure evidence is invalid")
		}
		record.PendingRecoveryResultSpec = nil
	} else if evidence.Reservation.Kind == "snapshot-delete" {
		reservation := evidence.Reservation
		if record.PendingSnapshot != nil || record.PendingRecoveryResultSpec != nil ||
			!poolVMHasSnapshotDescriptor(record.Snapshots, reservation.TargetSnapshotID, reservation.TargetSnapshotSHA256) {
			return errors.New("snapshot delete target differs from the current master catalog")
		}
		if evidence.Outcome == "completed" {
			if evidence.Snapshot != nil || evidence.ResultSpec != nil {
				return errors.New("snapshot delete evidence must not carry catalog or VM definition payloads")
			}
			next := make([]protocol.PoolVMSnapshot, 0, len(record.Snapshots)-1)
			for _, snapshot := range record.Snapshots {
				if snapshot.ID == reservation.TargetSnapshotID {
					continue
				}
				next = append(next, snapshot)
			}
			record.Snapshots = next
		} else if evidence.Outcome != "failed-before-effects" || evidence.Snapshot != nil || evidence.ResultSpec != nil {
			return errors.New("snapshot delete failure evidence is invalid")
		}
	} else if record.PendingRecoveryResultSpec != nil {
		return errors.New("unexpected pending snapshot revert definition")
	}
	want, err := poolVMRecoveryReservationFor(*record, evidence.Reservation.OperationID, evidence.Reservation.Kind, evidence.Reservation.IntentSHA256, evidence.Reservation.OwnerOperationOrder)
	got := evidence.Reservation
	got.AdmissionRefusal = ""
	got.TargetSnapshotID = ""
	got.TargetSnapshotSHA256 = ""
	got.ResultSpecSHA256 = ""
	if err != nil || want != got {
		return errors.New("snapshot recovery journal evidence does not match the current VM definition")
	}
	if revertedSpec != nil {
		record.Spec = *revertedSpec
		record.Autostart = revertedSpec.Autostart
	}
	record.LastSettledRecovery = &evidence
	record.PendingRecovery = nil
	return nil
}

const poolVMSnapshotLimit = 32

func poolVMSnapshotNameExists(snapshots []protocol.PoolVMSnapshot, name string) bool {
	for _, snapshot := range snapshots {
		if strings.EqualFold(snapshot.Name, name) {
			return true
		}
	}
	return false
}

func ReservePoolVMSnapshot(ctx context.Context, s *Store, command protocol.Command, ownerOperationOrder uint64, intentSHA256 string, createdAt time.Time) (protocol.PoolVMRecoveryReservation, error) {
	if command.Kind != "snapshot-create" || command.VM == nil || command.Snapshot == nil ||
		!protocol.ValidSnapshotOperationID(command.OperationID) || command.Snapshot.Valid() != nil ||
		!lowerHexDigest(intentSHA256) || createdAt.IsZero() || ownerOperationOrder == 0 || protocol.ValidateVMSpec(*command.VM) != nil {
		return protocol.PoolVMRecoveryReservation{}, errors.New("invalid stopped VM snapshot request")
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != s.State.PoolID || membership.MasterHostID == "" {
		return protocol.PoolVMRecoveryReservation{}, errors.New("pool snapshot authority is unavailable")
	}
	request := poolVMRequest{
		VMID: command.VM.ID, Action: "snapshot-reserve", OperationID: command.OperationID,
		VM: command.VM, Snapshot: command.Snapshot, SnapshotCreatedAt: createdAt.UTC(), IntentSHA256: intentSHA256, OwnerOperationOrder: ownerOperationOrder,
	}
	if membership.MasterHostID == s.State.HostID {
		if err := requirePoolVMAuthorityWritable(s, request.Action, false); err != nil {
			return protocol.PoolVMRecoveryReservation{}, err
		}
		poolVMMu.Lock()
		if err := requirePoolVMAuthorityWritable(s, request.Action, false); err != nil {
			poolVMMu.Unlock()
			return protocol.PoolVMRecoveryReservation{}, err
		}
		ack, err := reservePoolVMSnapshotLocked(s, s.State.HostID, request)
		poolVMMu.Unlock()
		if err != nil {
			return protocol.PoolVMRecoveryReservation{}, err
		}
		if !validPoolVMSnapshotReservationAck(ack, request, s.State.PoolID, s.State.HostID) {
			return protocol.PoolVMRecoveryReservation{}, errors.New("pool master did not confirm snapshot reservation")
		}
		if err := confirmPoolVMSnapshotReplica(ctx, s, &ack, nil); err != nil {
			return protocol.PoolVMRecoveryReservation{}, reservationUnconfirmed{err}
		}
		return *ack.SnapshotReservation, nil
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	defer client.CloseIdleConnections()
	body, _ := json.Marshal(request)
	requestURL := strings.TrimRight(master, "/") + poolVMPath + command.VM.ID
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(string(body)))
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, reservationUnconfirmed{err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return protocol.PoolVMRecoveryReservation{}, snapshotReservationRefusal(response, "pool master refused snapshot reservation")
	}
	var ack poolVMRequest
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&ack) != nil || !validPoolVMSnapshotReservationAck(ack, request, s.State.PoolID, s.State.HostID) {
		return protocol.PoolVMRecoveryReservation{}, reservationUnconfirmed{errors.New("pool master did not return an exact snapshot reservation acknowledgement")}
	}
	return *ack.SnapshotReservation, nil
}

// ReservePoolVMSnapshotRevert reserves an exact catalog target and the master's
// derived resulting VM definition before the owner applies native revert effects.
func ReservePoolVMSnapshotRevert(ctx context.Context, s *Store, command protocol.Command, ownerOperationOrder uint64, intentSHA256 string, capturedSpec protocol.VMCreateSpec) (protocol.PoolVMRecoveryReservation, error) {
	if command.Kind != "snapshot-revert" || command.VM == nil || command.Snapshot == nil || command.Snapshot.ValidFor(command.Kind) != nil ||
		!protocol.ValidSnapshotOperationID(command.OperationID) || command.OperationID == command.Snapshot.SnapshotID || !lowerHexDigest(intentSHA256) || ownerOperationOrder == 0 ||
		protocol.ValidateVMSpec(*command.VM) != nil || protocol.ValidateVMSpec(capturedSpec) != nil ||
		capturedSpec.ID != command.VM.ID || capturedSpec.PoolID != command.VM.PoolID || capturedSpec.HostID != command.VM.HostID {
		return protocol.PoolVMRecoveryReservation{}, errors.New("invalid stopped VM snapshot revert request")
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != s.State.PoolID || membership.MasterHostID == "" {
		return protocol.PoolVMRecoveryReservation{}, errors.New("pool snapshot authority is unavailable")
	}
	request := poolVMRequest{
		VMID: command.VM.ID, Action: "snapshot-reserve", RecoveryKind: "snapshot-revert", OperationID: command.OperationID,
		VM: command.VM, Snapshot: command.Snapshot, CapturedSpec: &capturedSpec, IntentSHA256: intentSHA256, OwnerOperationOrder: ownerOperationOrder,
	}
	if membership.MasterHostID == s.State.HostID {
		if err := requirePoolVMAuthorityWritable(s, request.Action, false); err != nil {
			return protocol.PoolVMRecoveryReservation{}, err
		}
		poolVMMu.Lock()
		if err := requirePoolVMAuthorityWritable(s, request.Action, false); err != nil {
			poolVMMu.Unlock()
			return protocol.PoolVMRecoveryReservation{}, err
		}
		ack, err := reservePoolVMSnapshotLocked(s, s.State.HostID, request)
		poolVMMu.Unlock()
		if err != nil {
			return protocol.PoolVMRecoveryReservation{}, err
		}
		if !validPoolVMSnapshotReservationAck(ack, request, s.State.PoolID, s.State.HostID) {
			return protocol.PoolVMRecoveryReservation{}, errors.New("pool master did not confirm snapshot revert reservation")
		}
		if err := confirmPoolVMSnapshotReplica(ctx, s, &ack, nil); err != nil {
			return protocol.PoolVMRecoveryReservation{}, reservationUnconfirmed{err}
		}
		return *ack.SnapshotReservation, nil
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(request)
	if err != nil || len(body) > 64<<10 {
		return protocol.PoolVMRecoveryReservation{}, errors.New("snapshot revert reservation exceeds the bounded peer request size")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+command.VM.ID, strings.NewReader(string(body)))
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, reservationUnconfirmed{err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return protocol.PoolVMRecoveryReservation{}, snapshotReservationRefusal(response, "pool master refused snapshot revert reservation")
	}
	var ack poolVMRequest
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&ack) != nil || !validPoolVMSnapshotReservationAck(ack, request, s.State.PoolID, s.State.HostID) {
		return protocol.PoolVMRecoveryReservation{}, reservationUnconfirmed{errors.New("pool master did not return an exact snapshot revert reservation acknowledgement")}
	}
	return *ack.SnapshotReservation, nil
}

// ReservePoolVMSnapshotDelete reserves an exact catalog target before the owner
// applies native leaf-delete effects. It does not change the VM definition.
func ReservePoolVMSnapshotDelete(ctx context.Context, s *Store, command protocol.Command, ownerOperationOrder uint64, intentSHA256 string) (protocol.PoolVMRecoveryReservation, error) {
	if command.Kind != "snapshot-delete" || command.VM == nil || command.Snapshot == nil || command.Snapshot.ValidFor(command.Kind) != nil ||
		!protocol.ValidSnapshotOperationID(command.OperationID) || command.OperationID == command.Snapshot.SnapshotID || !lowerHexDigest(intentSHA256) || ownerOperationOrder == 0 ||
		protocol.ValidateVMSpec(*command.VM) != nil {
		return protocol.PoolVMRecoveryReservation{}, errors.New("invalid stopped VM snapshot delete request")
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != s.State.PoolID || membership.MasterHostID == "" {
		return protocol.PoolVMRecoveryReservation{}, errors.New("pool snapshot authority is unavailable")
	}
	request := poolVMRequest{
		VMID: command.VM.ID, Action: "snapshot-reserve", RecoveryKind: "snapshot-delete", OperationID: command.OperationID,
		VM: command.VM, Snapshot: command.Snapshot, IntentSHA256: intentSHA256, OwnerOperationOrder: ownerOperationOrder,
	}
	if membership.MasterHostID == s.State.HostID {
		if err := requirePoolVMAuthorityWritable(s, request.Action, false); err != nil {
			return protocol.PoolVMRecoveryReservation{}, err
		}
		poolVMMu.Lock()
		if err := requirePoolVMAuthorityWritable(s, request.Action, false); err != nil {
			poolVMMu.Unlock()
			return protocol.PoolVMRecoveryReservation{}, err
		}
		ack, err := reservePoolVMSnapshotLocked(s, s.State.HostID, request)
		poolVMMu.Unlock()
		if err != nil {
			return protocol.PoolVMRecoveryReservation{}, err
		}
		if !validPoolVMSnapshotReservationAck(ack, request, s.State.PoolID, s.State.HostID) {
			return protocol.PoolVMRecoveryReservation{}, errors.New("pool master did not confirm snapshot delete reservation")
		}
		if err := confirmPoolVMSnapshotReplica(ctx, s, &ack, nil); err != nil {
			return protocol.PoolVMRecoveryReservation{}, reservationUnconfirmed{err}
		}
		return *ack.SnapshotReservation, nil
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(request)
	if err != nil || len(body) > 64<<10 {
		return protocol.PoolVMRecoveryReservation{}, errors.New("snapshot delete reservation exceeds the bounded peer request size")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+command.VM.ID, strings.NewReader(string(body)))
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, reservationUnconfirmed{err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return protocol.PoolVMRecoveryReservation{}, snapshotReservationRefusal(response, "pool master refused snapshot delete reservation")
	}
	var ack poolVMRequest
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&ack) != nil || !validPoolVMSnapshotReservationAck(ack, request, s.State.PoolID, s.State.HostID) {
		return protocol.PoolVMRecoveryReservation{}, reservationUnconfirmed{errors.New("pool master did not return an exact snapshot delete reservation acknowledgement")}
	}
	return *ack.SnapshotReservation, nil
}

func lowerHexDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validPoolVMSnapshotReservationAck(ack, request poolVMRequest, poolID, owner string) bool {
	if ack.VMID != request.VMID || ack.Action != "snapshot-reserve" || ack.OperationID != request.OperationID ||
		ack.RecoveryKind != request.RecoveryKind || !reflect.DeepEqual(ack.Snapshot, request.Snapshot) ||
		!reflect.DeepEqual(ack.CapturedSpec, request.CapturedSpec) ||
		!ack.SnapshotCreatedAt.Equal(request.SnapshotCreatedAt) || ack.SnapshotReservation == nil {
		return false
	}
	reservation := *ack.SnapshotReservation
	kind := request.RecoveryKind
	if kind == "" {
		kind = "snapshot-create"
	}
	if reservation.Valid() != nil || reservation.OperationID != request.OperationID || reservation.Kind != kind || reservation.OwnerOperationOrder != request.OwnerOperationOrder ||
		reservation.PoolID != poolID || reservation.VMID != request.VMID || reservation.OwnerHostID != owner || reservation.IntentSHA256 != request.IntentSHA256 {
		return false
	}
	if ack.SnapshotAccepted != (reservation.AdmissionRefusal == "") {
		return false
	}
	if request.VM == nil || reservation.SpecRevision != request.VM.Revision || !poolVMRecoverySpecMatches(reservation, *request.VM) {
		return false
	}
	if kind == "snapshot-revert" {
		if request.Snapshot == nil || request.Snapshot.ValidFor(kind) != nil || request.CapturedSpec == nil ||
			reservation.TargetSnapshotID != request.Snapshot.SnapshotID {
			return false
		}
		targetDigest, err := protocol.PoolVMSnapshotDescriptorSHA256(*request.Snapshot.Target)
		expectedResult, resultErr := poolVMRevertReservationMatchesTarget(reservation, *request.VM, *request.CapturedSpec, *request.Snapshot.Target)
		resultSpecSHA, _, expectedErr := poolVMRecoveryDigests(expectedResult)
		return err == nil && resultErr == nil && expectedErr == nil && reservation.TargetSnapshotSHA256 == targetDigest && reservation.ResultSpecSHA256 == resultSpecSHA
	}
	if kind == "snapshot-delete" {
		if request.Snapshot == nil || request.Snapshot.ValidFor(kind) != nil || request.CapturedSpec != nil ||
			reservation.TargetSnapshotID != request.Snapshot.SnapshotID || reservation.ResultSpecSHA256 != "" {
			return false
		}
		targetDigest, err := protocol.PoolVMSnapshotDescriptorSHA256(*request.Snapshot.Target)
		return err == nil && reservation.TargetSnapshotSHA256 == targetDigest
	}
	return request.RecoveryKind == "" && request.Snapshot != nil && request.Snapshot.ValidFor(kind) == nil
}

func poolVMRecoverySpecMatches(reservation protocol.PoolVMRecoveryReservation, spec protocol.VMCreateSpec) bool {
	specSHA, diskSHA, err := poolVMRecoveryDigests(spec)
	return err == nil && reservation.SpecSHA256 == specSHA && reservation.DiskIdentitySHA256 == diskSHA
}

func reservePoolVMSnapshotLocked(s *Store, owner string, req poolVMRequest) (poolVMRequest, error) {
	if req.RecoveryKind == "snapshot-revert" {
		return reservePoolVMSnapshotRevertLocked(s, owner, req)
	}
	if req.RecoveryKind == "snapshot-delete" {
		return reservePoolVMSnapshotDeleteLocked(s, owner, req)
	}
	if req.Action != "snapshot-reserve" || req.RecoveryKind != "" || req.CapturedSpec != nil || req.VM == nil || req.Snapshot == nil || req.Snapshot.Valid() != nil ||
		!protocol.ValidSnapshotOperationID(req.OperationID) || !lowerHexDigest(req.IntentSHA256) || req.SnapshotCreatedAt.IsZero() ||
		req.VMID != req.VM.ID || req.VM.HostID != owner || req.VM.PoolID != s.State.PoolID || protocol.ValidateVMSpec(*req.VM) != nil {
		return poolVMRequest{}, errors.New("invalid snapshot reservation request")
	}
	// Keep the authority record locked from the writable check through the durable
	// reservation. Otherwise a concurrent transfer could freeze a replica digest
	// that omits a reservation accepted just after its replica was built.
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	peer, err := loadPoolPeer(s)
	if err != nil || peer.Membership.Valid() != nil || peer.Membership.PoolID != s.State.PoolID ||
		peer.Membership.MasterHostID != s.State.HostID || owner == "" {
		return poolVMRequest{}, errors.New("pool snapshot authority is unavailable")
	}
	if err := poolAuthorityWritable(peer); err != nil {
		return poolVMRequest{}, err
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return poolVMRequest{}, errors.New("pool VM recovery state is unavailable")
	}
	record, ok := state.VMs[req.VMID]
	if !ok || record.HostID != owner || record.PoolID != s.State.PoolID || !poolVMRecordMatchesSpec(record, *req.VM) {
		return poolVMRequest{}, errors.New("snapshot request does not match the durable pool VM owner")
	}
	if record.PendingRecovery != nil {
		pending := record.PendingSnapshot
		if record.PendingRecovery.OperationID == req.OperationID && record.PendingSnapshot != nil &&
			record.PendingRecovery.Kind == "snapshot-create" && record.PendingRecovery.IntentSHA256 == req.IntentSHA256 &&
			record.PendingRecovery.OwnerOperationOrder == req.OwnerOperationOrder &&
			pending.Name == req.Snapshot.Name && pending.Description == req.Snapshot.Description && pending.CreatedAt.Equal(req.SnapshotCreatedAt) {
			return snapshotReservationAck(req, *record.PendingRecovery), nil
		}
		return poolVMRequest{}, errors.New("another snapshot recovery operation is pending")
	}
	if record.LastSettledRecovery != nil && record.LastSettledRecovery.Reservation.OperationID == req.OperationID {
		return poolVMRequest{}, errors.New("snapshot operation ID was already settled")
	}
	if last := record.LastSettledRecovery; last != nil && last.Reservation.OwnerHostID == owner {
		if record.Epoch < last.Reservation.Epoch {
			return poolVMRequest{}, errors.New("snapshot recovery owner epoch moved backwards")
		}
		if req.OwnerOperationOrder <= last.Reservation.OwnerOperationOrder {
			return poolVMRequest{}, errors.New("snapshot operation order is stale")
		}
	}
	if record.PendingStartOperationID != "" {
		return poolVMRequest{}, errors.New("snapshot creation cannot start while a start is pending")
	}
	running := protocol.SnapshotModeRunning(req.Snapshot.Mode)
	switch {
	case running && record.State == "running":
		// Running crash/quiesced capture is admitted here; native firmware limits are enforced on the owner.
	case !running && (record.State == "defined" || record.State == "stopped"):
	default:
		if running {
			return poolVMRequest{}, errors.New("running snapshot creation requires a running VM")
		}
		return poolVMRequest{}, errors.New("stopped snapshot creation requires a defined or stopped VM")
	}
	if err := validatePoolVMSnapshotCatalog(record); err != nil {
		return poolVMRequest{}, errors.New("pool VM snapshot catalog is invalid")
	}
	if record.PendingUpdate != nil {
		return poolVMRequest{}, errors.New("VM definition update is pending")
	}
	reservation, err := poolVMRecoveryReservationFor(record, req.OperationID, "snapshot-create", req.IntentSHA256, req.OwnerOperationOrder)
	if err != nil {
		return poolVMRequest{}, err
	}
	if len(record.Snapshots) >= poolVMSnapshotLimit {
		reservation.AdmissionRefusal = "catalog-full"
	} else if poolVMSnapshotNameExists(record.Snapshots, req.Snapshot.Name) {
		reservation.AdmissionRefusal = "name-in-use"
	}
	descriptor := protocol.PoolVMSnapshot{
		ID: req.OperationID, VMID: req.VMID, Name: req.Snapshot.Name, Description: req.Snapshot.Description,
		CreatedAt: req.SnapshotCreatedAt.UTC(), SpecRevision: reservation.SpecRevision,
		SpecSHA256: reservation.SpecSHA256, DiskIdentitySHA256: reservation.DiskIdentitySHA256,
	}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(record.Spec)
	if err != nil {
		return poolVMRequest{}, errors.New("snapshot VM resource dependencies are invalid")
	}
	descriptor.Dependencies = &dependencies
	if descriptor.Valid() != nil {
		return poolVMRequest{}, errors.New("snapshot descriptor is invalid")
	}
	// Recovery admission and storage-source mutation share VM → storage lock order.
	// Once the reservation is saved, the mutation path sees it before changing a disk source.
	poolStorageMu.Lock()
	storage, err := loadPoolStorage(s)
	if err != nil {
		poolStorageMu.Unlock()
		return poolVMRequest{}, errors.New("pool storage mutation state is unavailable")
	}
	for _, disk := range record.Spec.Disks {
		if disk.StorageID != "" && storage.Mutations[disk.StorageID] != "" {
			poolStorageMu.Unlock()
			return poolVMRequest{}, errors.New("snapshot storage source has a mutation pending")
		}
	}
	if poolResourceMutationForSpec(storage, record.Spec) != "" {
		poolStorageMu.Unlock()
		return poolVMRequest{}, errors.New("snapshot network or media source has a removal pending")
	}
	poolStorageMu.Unlock()
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		return poolVMRequest{}, err
	}
	record.PendingSnapshot = &descriptor
	state.VMs[record.VMID] = record
	if err := poolVMReplicaFits(s, peer.Membership, state.VMs); err != nil {
		return poolVMRequest{}, err
	}
	// Reserve enough space for the largest terminal record before native effects
	// begin. pool-vms.json is replicated as one bounded authority section; a
	// successful capture must not outgrow the section needed to settle its receipt.
	prospective := record
	prospective.Snapshots = append([]protocol.PoolVMSnapshot(nil), record.Snapshots...)
	completion := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("0", 64), Snapshot: &descriptor}
	if reservation.AdmissionRefusal != "" {
		completion.Outcome, completion.Snapshot = "failed-before-effects", nil
	}
	if err := settlePoolVMRecovery(&prospective, owner, completion); err != nil {
		return poolVMRequest{}, errors.New("snapshot catalog cannot reserve its terminal receipt")
	}
	state.VMs[record.VMID] = prospective
	if err := poolVMReplicaFits(s, peer.Membership, state.VMs); err != nil {
		return poolVMRequest{}, err
	}
	state.VMs[record.VMID] = record
	if err := savePoolVMs(s, state); err != nil {
		return poolVMRequest{}, err
	}
	return snapshotReservationAck(req, reservation), nil
}

func poolVMReplicaFits(s *Store, membership protocol.PoolMembership, vms map[string]poolVMRecord) error {
	vmBytes, err := json.Marshal(vms)
	if err != nil || len(vmBytes) > protocol.PoolReplicaLimit {
		return errors.New("snapshot transition would exceed the pool authority limit")
	}
	peer, err := loadPoolPeer(s)
	if err != nil || peer.Membership.Generation != membership.Generation {
		return errors.New("snapshot transition membership changed during replica sizing")
	}
	replica, _, err := buildPoolAuthorityReplicaForMembership(s, membership, vmBytes)
	if err != nil || poolAuthorityReplicaFitsAddressedBodies(s, replica, peer.CAKeyPEM) != nil {
		return errors.New("snapshot transition would exceed the pool replica limit")
	}
	return nil
}

func poolVMRevertReservationFor(record poolVMRecord, operationID, intentSHA256 string, ownerOrder uint64, target protocol.PoolVMSnapshot, captured protocol.VMCreateSpec) (protocol.PoolVMRecoveryReservation, protocol.VMCreateSpec, error) {
	reservation, err := poolVMRecoveryReservationFor(record, operationID, "snapshot-revert", intentSHA256, ownerOrder)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, protocol.VMCreateSpec{}, err
	}
	capturedSpecSHA, capturedDiskSHA, err := poolVMRecoveryDigests(captured)
	if err != nil || target.VMID != record.VMID || target.SpecRevision != captured.Revision || target.SpecSHA256 != capturedSpecSHA || target.DiskIdentitySHA256 != capturedDiskSHA {
		return protocol.PoolVMRecoveryReservation{}, protocol.VMCreateSpec{}, errors.New("captured VM definition does not match the snapshot target")
	}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(captured)
	if err != nil || target.Dependencies == nil || !reflect.DeepEqual(*target.Dependencies, dependencies) {
		return protocol.PoolVMRecoveryReservation{}, protocol.VMCreateSpec{}, errors.New("snapshot target resource dependencies do not match its captured VM")
	}
	targetSHA, err := protocol.PoolVMSnapshotDescriptorSHA256(target)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, protocol.VMCreateSpec{}, err
	}
	result, err := protocol.PoolVMSnapshotRevertSpec(captured, record.Spec)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, protocol.VMCreateSpec{}, err
	}
	resultSHA, _, err := poolVMRecoveryDigests(result)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, protocol.VMCreateSpec{}, err
	}
	reservation.TargetSnapshotID = target.ID
	reservation.TargetSnapshotSHA256 = targetSHA
	reservation.ResultSpecSHA256 = resultSHA
	if err = reservation.Valid(); err != nil {
		return protocol.PoolVMRecoveryReservation{}, protocol.VMCreateSpec{}, err
	}
	return reservation, result, nil
}

func reservePoolVMSnapshotRevertLocked(s *Store, owner string, req poolVMRequest) (poolVMRequest, error) {
	if req.Action != "snapshot-reserve" || req.RecoveryKind != "snapshot-revert" || req.VM == nil || req.Snapshot == nil ||
		req.Snapshot.ValidFor("snapshot-revert") != nil || req.CapturedSpec == nil || !protocol.ValidSnapshotOperationID(req.OperationID) || req.OperationID == req.Snapshot.SnapshotID ||
		!lowerHexDigest(req.IntentSHA256) || req.OwnerOperationOrder == 0 || req.VMID != req.VM.ID || req.VM.HostID != owner ||
		req.VM.PoolID != s.State.PoolID || protocol.ValidateVMSpec(*req.VM) != nil || protocol.ValidateVMSpec(*req.CapturedSpec) != nil {
		return poolVMRequest{}, errors.New("invalid snapshot revert reservation request")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	peer, err := loadPoolPeer(s)
	if err != nil || peer.Membership.Valid() != nil || peer.Membership.PoolID != s.State.PoolID || peer.Membership.MasterHostID != s.State.HostID || owner == "" {
		return poolVMRequest{}, errors.New("pool snapshot authority is unavailable")
	}
	if err := poolAuthorityWritable(peer); err != nil {
		return poolVMRequest{}, err
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return poolVMRequest{}, errors.New("pool VM recovery state is unavailable")
	}
	record, ok := state.VMs[req.VMID]
	if !ok || record.HostID != owner || record.PoolID != s.State.PoolID || !poolVMRecordMatchesSpec(record, *req.VM) {
		return poolVMRequest{}, errors.New("snapshot revert request does not match the durable pool VM owner")
	}
	if record.PendingRecovery == nil && (record.PendingSnapshot != nil || record.PendingRecoveryResultSpec != nil) {
		return poolVMRequest{}, errors.New("pool VM has inconsistent pending snapshot recovery state")
	}
	if err := validatePoolVMSnapshotCatalog(record); err != nil {
		return poolVMRequest{}, errors.New("pool VM snapshot catalog is invalid")
	}
	var target *protocol.PoolVMSnapshot
	for i := range record.Snapshots {
		if record.Snapshots[i].ID == req.Snapshot.SnapshotID {
			target = &record.Snapshots[i]
			break
		}
	}
	if target == nil || !reflect.DeepEqual(*target, *req.Snapshot.Target) {
		return poolVMRequest{}, errors.New("snapshot revert target differs from the current master catalog")
	}
	if target.Dependencies == nil {
		return poolVMRequest{}, errors.New("snapshot revert target does not record complete resource dependencies")
	}
	reservation, resultSpec, err := poolVMRevertReservationFor(record, req.OperationID, req.IntentSHA256, req.OwnerOperationOrder, *target, *req.CapturedSpec)
	if err != nil {
		return poolVMRequest{}, err
	}
	if record.PendingRecovery != nil {
		if *record.PendingRecovery == reservation && record.PendingRecoveryResultSpec != nil && reflect.DeepEqual(*record.PendingRecoveryResultSpec, resultSpec) {
			return snapshotReservationAck(req, reservation), nil
		}
		return poolVMRequest{}, errors.New("another snapshot recovery operation is pending")
	}
	if record.LastSettledRecovery != nil && record.LastSettledRecovery.Reservation.OperationID == req.OperationID {
		return poolVMRequest{}, errors.New("snapshot operation ID was already settled")
	}
	if last := record.LastSettledRecovery; last != nil && last.Reservation.OwnerHostID == owner {
		if record.Epoch < last.Reservation.Epoch || req.OwnerOperationOrder <= last.Reservation.OwnerOperationOrder {
			return poolVMRequest{}, errors.New("snapshot operation order is stale")
		}
	}
	if (record.State != "defined" && record.State != "stopped") || record.PendingUpdate != nil || record.PendingStartOperationID != "" {
		return poolVMRequest{}, errors.New("snapshot revert is not eligible for the durable pool VM")
	}
	poolStorageMu.Lock()
	storage, err := loadPoolStorage(s)
	if err != nil {
		poolStorageMu.Unlock()
		return poolVMRequest{}, errors.New("snapshot storage mutation state is unavailable")
	}
	for _, spec := range []protocol.VMCreateSpec{record.Spec, resultSpec} {
		for _, disk := range spec.Disks {
			if disk.StorageID != "" && storage.Mutations[disk.StorageID] != "" {
				poolStorageMu.Unlock()
				return poolVMRequest{}, errors.New("snapshot revert storage source has a mutation pending")
			}
		}
		if poolResourceMutationForSpec(storage, spec) != "" {
			poolStorageMu.Unlock()
			return poolVMRequest{}, errors.New("snapshot revert network or media source has a removal pending")
		}
	}
	poolStorageMu.Unlock()
	record.PendingRecoveryResultSpec = &resultSpec
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		return poolVMRequest{}, err
	}
	state.VMs[record.VMID] = record
	if err := poolVMReplicaFits(s, peer.Membership, state.VMs); err != nil {
		return poolVMRequest{}, err
	}
	prospective := record
	resultCopy := resultSpec
	if err := settlePoolVMRecovery(&prospective, owner, protocol.PoolVMRecoveryEvidence{
		Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("0", 64), ResultSpec: &resultCopy,
	}); err != nil {
		return poolVMRequest{}, errors.New("snapshot revert cannot reserve its terminal receipt")
	}
	prospectiveVMs := make(map[string]poolVMRecord, len(state.VMs))
	for id, vm := range state.VMs {
		prospectiveVMs[id] = vm
	}
	prospectiveVMs[record.VMID] = prospective
	if err := poolVMReplicaFits(s, peer.Membership, prospectiveVMs); err != nil {
		return poolVMRequest{}, err
	}
	if err := savePoolVMs(s, state); err != nil {
		return poolVMRequest{}, err
	}
	return snapshotReservationAck(req, reservation), nil
}

func poolVMDeleteReservationFor(record poolVMRecord, operationID, intentSHA256 string, ownerOrder uint64, target protocol.PoolVMSnapshot) (protocol.PoolVMRecoveryReservation, error) {
	reservation, err := poolVMRecoveryReservationFor(record, operationID, "snapshot-delete", intentSHA256, ownerOrder)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	if target.VMID != record.VMID {
		return protocol.PoolVMRecoveryReservation{}, errors.New("snapshot delete target does not belong to this VM")
	}
	targetSHA, err := protocol.PoolVMSnapshotDescriptorSHA256(target)
	if err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	reservation.TargetSnapshotID = target.ID
	reservation.TargetSnapshotSHA256 = targetSHA
	if err = reservation.Valid(); err != nil {
		return protocol.PoolVMRecoveryReservation{}, err
	}
	return reservation, nil
}

func reservePoolVMSnapshotDeleteLocked(s *Store, owner string, req poolVMRequest) (poolVMRequest, error) {
	if req.Action != "snapshot-reserve" || req.RecoveryKind != "snapshot-delete" || req.VM == nil || req.Snapshot == nil ||
		req.Snapshot.ValidFor("snapshot-delete") != nil || req.CapturedSpec != nil || !protocol.ValidSnapshotOperationID(req.OperationID) || req.OperationID == req.Snapshot.SnapshotID ||
		!lowerHexDigest(req.IntentSHA256) || req.OwnerOperationOrder == 0 || req.VMID != req.VM.ID || req.VM.HostID != owner ||
		req.VM.PoolID != s.State.PoolID || protocol.ValidateVMSpec(*req.VM) != nil {
		return poolVMRequest{}, errors.New("invalid snapshot delete reservation request")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	peer, err := loadPoolPeer(s)
	if err != nil || peer.Membership.Valid() != nil || peer.Membership.PoolID != s.State.PoolID || peer.Membership.MasterHostID != s.State.HostID || owner == "" {
		return poolVMRequest{}, errors.New("pool snapshot authority is unavailable")
	}
	if err := poolAuthorityWritable(peer); err != nil {
		return poolVMRequest{}, err
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return poolVMRequest{}, errors.New("pool VM recovery state is unavailable")
	}
	record, ok := state.VMs[req.VMID]
	if !ok || record.HostID != owner || record.PoolID != s.State.PoolID || !poolVMRecordMatchesSpec(record, *req.VM) {
		return poolVMRequest{}, errors.New("snapshot delete request does not match the durable pool VM owner")
	}
	if record.PendingRecovery == nil && (record.PendingSnapshot != nil || record.PendingRecoveryResultSpec != nil) {
		return poolVMRequest{}, errors.New("pool VM has inconsistent pending snapshot recovery state")
	}
	if err := validatePoolVMSnapshotCatalog(record); err != nil {
		return poolVMRequest{}, errors.New("pool VM snapshot catalog is invalid")
	}
	var target *protocol.PoolVMSnapshot
	for i := range record.Snapshots {
		if record.Snapshots[i].ID == req.Snapshot.SnapshotID {
			target = &record.Snapshots[i]
			break
		}
	}
	if target == nil || !reflect.DeepEqual(*target, *req.Snapshot.Target) {
		return poolVMRequest{}, errors.New("snapshot delete target differs from the current master catalog")
	}
	reservation, err := poolVMDeleteReservationFor(record, req.OperationID, req.IntentSHA256, req.OwnerOperationOrder, *target)
	if err != nil {
		return poolVMRequest{}, err
	}
	if record.PendingRecovery != nil {
		if *record.PendingRecovery == reservation {
			return snapshotReservationAck(req, reservation), nil
		}
		return poolVMRequest{}, errors.New("another snapshot recovery operation is pending")
	}
	if record.LastSettledRecovery != nil && record.LastSettledRecovery.Reservation.OperationID == req.OperationID {
		return poolVMRequest{}, errors.New("snapshot operation ID was already settled")
	}
	if last := record.LastSettledRecovery; last != nil && last.Reservation.OwnerHostID == owner {
		if record.Epoch < last.Reservation.Epoch || req.OwnerOperationOrder <= last.Reservation.OwnerOperationOrder {
			return poolVMRequest{}, errors.New("snapshot operation order is stale")
		}
	}
	if (record.State != "defined" && record.State != "stopped") || record.PendingUpdate != nil || record.PendingStartOperationID != "" {
		return poolVMRequest{}, errors.New("snapshot delete is not eligible for the durable pool VM")
	}
	poolStorageMu.Lock()
	storage, err := loadPoolStorage(s)
	if err != nil {
		poolStorageMu.Unlock()
		return poolVMRequest{}, errors.New("snapshot storage mutation state is unavailable")
	}
	for _, disk := range record.Spec.Disks {
		if disk.StorageID != "" && storage.Mutations[disk.StorageID] != "" {
			poolStorageMu.Unlock()
			return poolVMRequest{}, errors.New("snapshot delete storage source has a mutation pending")
		}
	}
	if poolResourceMutationForSpec(storage, record.Spec) != "" {
		poolStorageMu.Unlock()
		return poolVMRequest{}, errors.New("snapshot delete network or media source has a removal pending")
	}
	poolStorageMu.Unlock()
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		return poolVMRequest{}, err
	}
	state.VMs[record.VMID] = record
	if err := poolVMReplicaFits(s, peer.Membership, state.VMs); err != nil {
		return poolVMRequest{}, err
	}
	prospective := record
	if err := settlePoolVMRecovery(&prospective, owner, protocol.PoolVMRecoveryEvidence{
		Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("0", 64),
	}); err != nil {
		return poolVMRequest{}, errors.New("snapshot delete cannot reserve its terminal receipt")
	}
	prospectiveVMs := make(map[string]poolVMRecord, len(state.VMs))
	for id, vm := range state.VMs {
		prospectiveVMs[id] = vm
	}
	prospectiveVMs[record.VMID] = prospective
	if err := poolVMReplicaFits(s, peer.Membership, prospectiveVMs); err != nil {
		return poolVMRequest{}, err
	}
	if err := savePoolVMs(s, state); err != nil {
		return poolVMRequest{}, err
	}
	return snapshotReservationAck(req, reservation), nil
}

// reservationUnconfirmed marks a reservation the master may have saved without the
// requester learning of it: a lost reply, a server error, or a master that could not
// get every ready member to acknowledge. The master keeps it for an exact retry, so the
// requester must keep its intent and retry rather than abandon it and strand it.
type reservationUnconfirmed struct{ error }

func (reservationUnconfirmed) Is(target error) bool {
	return target == vmhost.ErrSnapshotReservationUnconfirmed
}

func (e reservationUnconfirmed) Unwrap() error { return e.error }

// poolSnapshotSavedHeader marks a refusal that follows a saved reservation or settlement the
// master could not yet get every ready member to acknowledge. It is a header, not message
// text, so rewording the refusal cannot turn a retryable answer into an abandoned one.
const poolSnapshotSavedHeader = "X-Nex-Snapshot-Saved"

func snapshotReservationRefusal(response *http.Response, message string) error {
	if response.StatusCode >= 500 || response.Header.Get(poolSnapshotSavedHeader) != "" {
		return reservationUnconfirmed{errors.New(message)}
	}
	return errors.New(message)
}

func snapshotReservationAck(request poolVMRequest, reservation protocol.PoolVMRecoveryReservation) poolVMRequest {
	request.SnapshotAccepted = reservation.AdmissionRefusal == ""
	request.SnapshotReservation = &reservation
	return request
}

type poolVMSnapshotCatalog struct {
	VMID      string                    `json:"vm_id"`
	Snapshots []protocol.PoolVMSnapshot `json:"snapshots"`
}

func validatePoolVMSnapshotCatalog(record poolVMRecord) error {
	if len(record.Snapshots) > poolVMSnapshotLimit {
		return errors.New("snapshot catalog exceeds its bounded size")
	}
	ids := make(map[string]bool, len(record.Snapshots))
	names := make([]protocol.PoolVMSnapshot, 0, len(record.Snapshots))
	for _, snapshot := range record.Snapshots {
		if snapshot.Valid() != nil || snapshot.VMID != record.VMID || ids[snapshot.ID] || poolVMSnapshotNameExists(names, snapshot.Name) {
			return errors.New("snapshot catalog contains an invalid or duplicate descriptor")
		}
		ids[snapshot.ID] = true
		names = append(names, snapshot)
	}
	return nil
}

// ListPoolVMSnapshots reads the catalog from the current pool master. A member
// never substitutes its local libvirt snapshot list for missing master authority.
func ListPoolVMSnapshots(ctx context.Context, s *Store, vmID string) ([]protocol.PoolVMSnapshot, error) {
	if !uuidLike(vmID) {
		return nil, errors.New("invalid pool VM snapshot catalog request")
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != s.State.PoolID || membership.MasterHostID == "" {
		return nil, errors.New("pool VM snapshot authority is unavailable")
	}
	if membership.MasterHostID == s.State.HostID {
		poolVMMu.Lock()
		state, loadErr := loadPoolVMs(s)
		poolVMMu.Unlock()
		if loadErr != nil {
			return nil, loadErr
		}
		record, ok := state.VMs[vmID]
		if !ok || record.PoolID != membership.PoolID {
			return nil, errors.New("pool VM snapshot catalog not found")
		}
		if err := validatePoolVMSnapshotCatalog(record); err != nil {
			return nil, err
		}
		return append([]protocol.PoolVMSnapshot(nil), record.Snapshots...), nil
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return nil, err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(master, "/")+poolVMPath+vmID+"/snapshots", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("pool master refused VM snapshot catalog")
	}
	var catalog poolVMSnapshotCatalog
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&catalog) != nil || catalog.VMID != vmID {
		return nil, errors.New("pool master returned an invalid VM snapshot catalog")
	}
	record := poolVMRecord{VMID: vmID, Snapshots: catalog.Snapshots}
	if err := validatePoolVMSnapshotCatalog(record); err != nil {
		return nil, err
	}
	return append([]protocol.PoolVMSnapshot(nil), catalog.Snapshots...), nil
}

// UpdatePoolVMSnapshotMetadata renames or redescribes one settled catalog entry on
// the current pool master. Identity, digests and capture time stay fixed; later
// revert/delete bind the updated descriptor SHA.
func UpdatePoolVMSnapshotMetadata(ctx context.Context, s *Store, vmID, snapshotID, name, description string) (protocol.PoolVMSnapshot, error) {
	req := protocol.VMSnapshotRequest{SnapshotID: snapshotID, Name: name, Description: description}
	if !uuidLike(vmID) || req.ValidFor("snapshot-metadata") != nil {
		return protocol.PoolVMSnapshot{}, errors.New("invalid pool VM snapshot metadata update")
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != s.State.PoolID || membership.MasterHostID == "" {
		return protocol.PoolVMSnapshot{}, errors.New("pool VM snapshot authority is unavailable")
	}
	if membership.MasterHostID == s.State.HostID {
		return updatePoolVMSnapshotMetadataLocal(s, vmID, req)
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return protocol.PoolVMSnapshot{}, err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return protocol.PoolVMSnapshot{}, err
	}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(poolVMRequest{VMID: vmID, Action: "snapshot-metadata", Snapshot: &req})
	if err != nil {
		return protocol.PoolVMSnapshot{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+vmID, strings.NewReader(string(body)))
	if err != nil {
		return protocol.PoolVMSnapshot{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	response, err := client.Do(httpReq)
	if err != nil {
		return protocol.PoolVMSnapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return protocol.PoolVMSnapshot{}, errors.New("pool master refused VM snapshot metadata update")
	}
	var ack poolVMRequest
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&ack) != nil || ack.VMID != vmID || ack.Action != "snapshot-metadata" ||
		ack.Snapshot == nil || ack.Snapshot.SnapshotID != snapshotID || ack.Snapshot.Name != name || ack.Snapshot.Description != description ||
		ack.Snapshot.Target == nil || ack.Snapshot.Target.Valid() != nil || ack.Snapshot.Target.ID != snapshotID || ack.Snapshot.Target.VMID != vmID ||
		ack.Snapshot.Target.Name != name || ack.Snapshot.Target.Description != description {
		return protocol.PoolVMSnapshot{}, errors.New("pool master returned an invalid VM snapshot metadata update")
	}
	return *ack.Snapshot.Target, nil
}

func updatePoolVMSnapshotMetadataLocal(s *Store, vmID string, req protocol.VMSnapshotRequest) (protocol.PoolVMSnapshot, error) {
	updated, err := func() (protocol.PoolVMSnapshot, error) {
		poolVMMu.Lock()
		defer poolVMMu.Unlock()
		if err := requirePoolVMAuthorityWritable(s, "snapshot-metadata", false); err != nil {
			return protocol.PoolVMSnapshot{}, err
		}
		membership, err := Membership(s)
		if err != nil || membership.MasterHostID != s.State.HostID {
			return protocol.PoolVMSnapshot{}, errors.New("pool VM snapshot authority is unavailable")
		}
		state, err := loadPoolVMs(s)
		if err != nil {
			return protocol.PoolVMSnapshot{}, err
		}
		record, ok := state.VMs[vmID]
		if !ok || record.PoolID != membership.PoolID {
			return protocol.PoolVMSnapshot{}, errors.New("pool VM snapshot catalog not found")
		}
		if record.HostID != s.State.HostID {
			return protocol.PoolVMSnapshot{}, errors.New("snapshot metadata update does not address this Core host")
		}
		updated, err := applyPoolVMSnapshotMetadata(&record, req)
		if err != nil {
			return protocol.PoolVMSnapshot{}, err
		}
		state.VMs[vmID] = record
		if err := savePoolVMs(s, state); err != nil {
			return protocol.PoolVMSnapshot{}, err
		}
		return updated, nil
	}()
	if err != nil {
		return protocol.PoolVMSnapshot{}, err
	}
	if err := confirmPoolVMSnapshotMetadataReplica(context.Background(), s, vmID, updated); err != nil {
		return protocol.PoolVMSnapshot{}, err
	}
	return updated, nil
}

func applyPoolVMSnapshotMetadata(record *poolVMRecord, req protocol.VMSnapshotRequest) (protocol.PoolVMSnapshot, error) {
	if record == nil || req.ValidFor("snapshot-metadata") != nil {
		return protocol.PoolVMSnapshot{}, errors.New("invalid pool VM snapshot metadata update")
	}
	// Any pending recovery fences the catalog: renaming into a reserved create name
	// would make a completed create unsettleable.
	if record.PendingRecovery != nil {
		return protocol.PoolVMSnapshot{}, ErrSnapshotMetadataPending
	}
	if err := validatePoolVMSnapshotCatalog(*record); err != nil {
		return protocol.PoolVMSnapshot{}, err
	}
	index := -1
	for i, snapshot := range record.Snapshots {
		if snapshot.ID == req.SnapshotID {
			index = i
			break
		}
	}
	if index < 0 {
		return protocol.PoolVMSnapshot{}, ErrSnapshotMetadataNotFound
	}
	others := append(append([]protocol.PoolVMSnapshot(nil), record.Snapshots[:index]...), record.Snapshots[index+1:]...)
	if record.PendingSnapshot != nil {
		others = append(others, *record.PendingSnapshot)
	}
	if poolVMSnapshotNameExists(others, req.Name) {
		return protocol.PoolVMSnapshot{}, ErrSnapshotMetadataConflict
	}
	updated := record.Snapshots[index]
	updated.Name, updated.Description = req.Name, req.Description
	if updated.Valid() != nil {
		return protocol.PoolVMSnapshot{}, errors.New("pool VM snapshot metadata update is invalid")
	}
	record.Snapshots[index] = updated
	return updated, nil
}

type poolVMPendingUpdate struct {
	OperationID string                `json:"operation_id"`
	Next        protocol.VMCreateSpec `json:"next"`
}
type poolVMState struct {
	LocalDetectedOS map[string]protocol.DetectedOS `json:"local_detected_os,omitempty"`
	VMs             map[string]poolVMRecord        `json:"vms"`
	Inventory       map[string]poolVMInventory     `json:"inventory,omitempty"`
}
type poolVMRequest struct {
	VMID                string                              `json:"vm_id,omitempty"`
	Action              string                              `json:"action,omitempty"`
	Permit              string                              `json:"permit,omitempty"`
	Create              bool                                `json:"create,omitempty"`
	VM                  *protocol.VMCreateSpec              `json:"vm,omitempty"`
	Snapshot            *protocol.VMSnapshotRequest         `json:"snapshot,omitempty"`
	RecoveryKind        string                              `json:"recovery_kind,omitempty"`
	CapturedSpec        *protocol.VMCreateSpec              `json:"captured_spec,omitempty"`
	SnapshotCreatedAt   time.Time                           `json:"snapshot_created_at,omitempty"`
	IntentSHA256        string                              `json:"intent_sha256,omitempty"`
	OwnerOperationOrder uint64                              `json:"owner_operation_order,omitempty"`
	SnapshotReservation *protocol.PoolVMRecoveryReservation `json:"snapshot_reservation,omitempty"`
	RecoveryEvidence    *protocol.PoolVMRecoveryEvidence    `json:"recovery_evidence,omitempty"`
	// SpecMatch is an explicit master acknowledgement for recovery admission. It
	// is deliberately absent from older peers, which a new recovery client must
	// treat as unsafe rather than silently accepting a 200 response.
	SpecMatch   bool                     `json:"spec_match,omitempty"`
	Epoch       uint64                   `json:"epoch,omitempty"`
	OperationID string                   `json:"operation_id,omitempty"`
	Previous    *protocol.VMCreateSpec   `json:"previous,omitempty"`
	Update      *vmhost.VMUpdateEvidence `json:"update,omitempty"`
	// UpdateAccepted is set only by the master after its durable record was
	// saved.  It prevents a newer member from mistaking a legacy empty 200 for
	// a definition-transition acknowledgement.
	UpdateAccepted bool `json:"update_accepted,omitempty"`
	UpdateRevision int  `json:"update_revision,omitempty"`
	// PlacementAccepted is an explicit acknowledgement from a current master
	// after its fresh target-host preflight and durable definition reservation.
	PlacementAccepted bool `json:"placement_accepted,omitempty"`
	PlacementAbsent   bool `json:"placement_absent,omitempty"`
	PlacementReleased bool `json:"placement_released,omitempty"`
	SnapshotAccepted  bool `json:"snapshot_accepted,omitempty"`
	SnapshotSettled   bool `json:"snapshot_settled,omitempty"`
}

// poolVMRecordMatchesSpec binds an interrupted operation to the immutable
// master-owned definition rather than trusting the caller's copy. A later
// inventory observation may refresh a record, so recovery fails closed on any
// revision, disk, or other definition difference.
func poolVMRecordMatchesSpec(record poolVMRecord, want protocol.VMCreateSpec) bool {
	return record.PoolID == want.PoolID && record.VMID == want.ID && record.HostID == want.HostID && protocol.ValidateVMSpec(record.Spec) == nil && reflect.DeepEqual(record.Spec, want)
}

func validPoolVMUpdateOperation(id string) bool {
	return len(id) > 0 && len(id) <= 200 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "/\\\x00")
}

func poolVMUpdateEvidenceMatches(record poolVMRecord, pending poolVMPendingUpdate, evidence vmhost.VMUpdateEvidence) bool {
	return evidence.Status == "succeeded" && evidence.OperationID == pending.OperationID && reflect.DeepEqual(evidence.Previous, record.Spec) && reflect.DeepEqual(evidence.Current, pending.Next) && evidence.Current.ID == record.VMID && evidence.Current.PoolID == record.PoolID && evidence.Current.HostID == record.HostID && evidence.Current.Revision == record.Spec.Revision+1 && protocol.ValidateVMSpec(evidence.Current) == nil
}

func reservePoolVMUpdate(record *poolVMRecord, owner, operationID string, next protocol.VMCreateSpec) error {
	if record.PoolID == "" || record.HostID != owner || (record.State != "defined" && record.State != "stopped" && record.State != "running") || record.PendingRecovery != nil || record.PendingStartOperationID != "" || !validPoolVMUpdateOperation(operationID) || next.ID != record.VMID || next.PoolID != record.PoolID || next.HostID != owner || next.Revision != record.Spec.Revision+1 || protocol.ValidateVMSpec(next) != nil {
		return errors.New("VM update is not eligible for the durable pool owner record")
	}
	if record.PendingUpdate != nil {
		if record.PendingUpdate.OperationID == operationID && reflect.DeepEqual(record.PendingUpdate.Next, next) {
			return nil
		}
		return errors.New("VM update outcome is pending reconciliation")
	}
	record.PendingUpdate = &poolVMPendingUpdate{OperationID: operationID, Next: next}
	record.LastAbortedUpdate = nil
	return nil
}

// reserveOrSeedPoolVMUpdate creates a first pool owner record only from the
// authenticated member's current-generation inventory. The caller-supplied
// Previous is a comparison value, never an authority to adopt or transfer a
// local VM definition.
func reserveOrSeedPoolVMUpdate(state *poolVMState, membership protocol.PoolMembership, record *poolVMRecord, exists bool, owner, operationID string, previous, next *protocol.VMCreateSpec) error {
	if previous == nil || next == nil || previous.ID != next.ID || previous.PoolID != next.PoolID || previous.HostID != owner || next.HostID != owner || next.Revision != previous.Revision+1 || protocol.ValidateVMSpec(*previous) != nil {
		return errors.New("VM update previous definition is invalid")
	}
	if exists {
		if !poolVMRecordMatchesSpec(*record, *previous) {
			return errors.New("VM update previous definition does not match the durable pool owner record")
		}
		return reservePoolVMUpdate(record, owner, operationID, *next)
	}
	memberReady := false
	for _, member := range membership.Members {
		if member.HostID == owner && member.Status == "ready" && member.Generation == membership.Generation {
			memberReady = true
		}
	}
	inv, inventoryOK := state.Inventory[owner]
	if !memberReady || !inventoryOK || inv.Generation != membership.Generation || inv.HostID != owner || !reflect.DeepEqual(findPoolVMInventorySpec(inv, previous.ID), *previous) {
		return errors.New("VM update requires an exact current owner inventory")
	}
	observed := inv.Observed[previous.ID]
	if observed != "running" && observed != "shut off" {
		return errors.New("VM update owner runtime observation is unavailable")
	}
	stateName := "stopped"
	if observed == "running" {
		stateName = "running"
	}
	*record = poolVMRecord{PoolID: previous.PoolID, VMID: previous.ID, HostID: owner, State: stateName, Autostart: previous.Autostart, Epoch: 1, Spec: *previous}
	return reservePoolVMUpdate(record, owner, operationID, *next)
}

func findPoolVMInventorySpec(inv poolVMInventory, id string) protocol.VMCreateSpec {
	var found protocol.VMCreateSpec
	for _, vm := range inv.VMs {
		if vm.ID != id {
			continue
		}
		if found.ID != "" {
			return protocol.VMCreateSpec{}
		}
		found = vm
	}
	return found
}

func commitPoolVMUpdate(record *poolVMRecord, owner string, evidence vmhost.VMUpdateEvidence) error {
	if record.PoolID == "" || record.HostID != owner || record.PendingUpdate == nil || !poolVMUpdateEvidenceMatches(*record, *record.PendingUpdate, evidence) {
		return errors.New("VM update journal evidence does not match the durable pool owner record")
	}
	record.Spec, record.Autostart, record.PendingUpdate = evidence.Current, evidence.Current.Autostart, nil
	return nil
}

// abortPoolVMUpdate releases only a journal-proven failed, unapplied update.
// Timeouts, needs_attention, and any evidence of a newer definition deliberately
// leave the pending record in place: another execution could still have changed
// the guest and must be reconciled rather than retried as a new update.
func abortPoolVMUpdate(record *poolVMRecord, owner string, evidence vmhost.VMUpdateEvidence) error {
	if record.PoolID == "" || record.HostID != owner || evidence.Status != "failed" || !reflect.DeepEqual(evidence.Previous, record.Spec) || evidence.Current.ID != record.VMID || evidence.Current.PoolID != record.PoolID || evidence.Current.HostID != record.HostID || evidence.Current.Revision != record.Spec.Revision+1 || protocol.ValidateVMSpec(evidence.Current) != nil {
		return errors.New("VM update failure evidence does not match the durable pool owner record")
	}
	if record.PendingUpdate == nil {
		// Retrying an exact authenticated abort after its response was lost is a
		// read/compare acknowledgement. It cannot release a later reservation.
		if record.LastAbortedUpdate != nil && evidence.OperationID == record.LastAbortedUpdate.OperationID && reflect.DeepEqual(evidence.Current, record.LastAbortedUpdate.Next) {
			return nil
		}
		return errors.New("VM update failure evidence does not match the durable pool owner record")
	}
	if evidence.OperationID != record.PendingUpdate.OperationID || !reflect.DeepEqual(evidence.Current, record.PendingUpdate.Next) {
		return errors.New("VM update failure evidence does not match the durable pool owner record")
	}
	record.LastAbortedUpdate = record.PendingUpdate
	record.PendingUpdate = nil
	return nil
}

// recoverPoolVMUpdate accepts only a journal-backed, one-revision successor
// from the already recorded owner. It is the bounded recovery path for an
// update applied by an older Core before update reservations existed; inventory
// alone never reaches this function.
func recoverPoolVMUpdate(record *poolVMRecord, owner string, evidence vmhost.VMUpdateEvidence) error {
	if record.PoolID == "" || record.HostID != owner || record.PendingUpdate != nil || evidence.Status != "succeeded" || !validPoolVMUpdateOperation(evidence.OperationID) || !reflect.DeepEqual(evidence.Previous, record.Spec) || evidence.Current.ID != record.VMID || evidence.Current.PoolID != record.PoolID || evidence.Current.HostID != record.HostID || evidence.Current.Revision != record.Spec.Revision+1 || protocol.ValidateVMSpec(evidence.Current) != nil {
		return errors.New("VM update journal evidence does not match the durable pool owner record")
	}
	record.Spec, record.Autostart = evidence.Current, evidence.Current.Autostart
	return nil
}

var poolVMMu sync.Mutex

const poolVMAutostartAttemptLimit = 4 << 10

type poolVMAutostartAttempt struct {
	VMID   string `json:"vm_id"`
	BootID string `json:"boot_id"`
}

var poolVMAutostartMu sync.Mutex
var poolVMAutostartBootID = func() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	bootID := strings.TrimSpace(string(b))
	if err != nil || !validPoolVMAutostartID(bootID) {
		return "", errors.New("local boot identity is unavailable")
	}
	return bootID, nil
}
var poolVMAutostartOwner = func(uid uint32) bool {
	return uid == 0 || uid == uint32(os.Geteuid())
}

func validPoolVMAutostartID(id string) bool {
	return id == strings.ToLower(id) && uuidLike(id)
}

// ClaimPoolVMAutostartAttempt durably records one autostart reconciliation for
// vmID during the current OS boot. A failed claim is fail-closed: callers must
// not infer that a restart of the agent is permission to start a stopped VM.
func ClaimPoolVMAutostartAttempt(stateDir, vmID string) (bool, error) {
	if !validPoolVMAutostartID(vmID) {
		return false, errors.New("invalid pool VM identity")
	}
	bootID, err := poolVMAutostartBootID()
	if err != nil {
		return false, err
	}

	poolVMAutostartMu.Lock()
	defer poolVMAutostartMu.Unlock()
	dir, err := poolVMAutostartAttemptDir(stateDir)
	if err != nil {
		return false, err
	}
	path := filepath.Join(dir, vmID+"-"+bootID+".json")
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return poolVMAutostartAttemptExists(path, vmID, bootID)
		}
		return false, err
	}
	f := os.NewFile(uintptr(fd), path)
	b, err := json.Marshal(poolVMAutostartAttempt{VMID: vmID, BootID: bootID})
	if err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// Do not remove a partial claim: a later attempt must fail closed rather
		// than treat an interrupted durable write as permission to start.
		return false, err
	}
	d, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

func poolVMAutostartAttemptDir(stateDir string) (string, error) {
	if err := trustedPoolVMAutostartStateDir(stateDir); err != nil {
		return "", errors.New("Core state directory is unsafe")
	}
	dir := filepath.Join(stateDir, "pool-vm-autostart-attempts")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := trustedPoolVMAutostartDir(dir); err != nil {
		return "", errors.New("pool VM autostart state directory is unsafe")
	}
	return dir, nil
}

func poolVMAutostartAttemptExists(path, vmID, bootID string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 || !poolVMAutostartOwned(info) {
		return false, errors.New("pool VM autostart attempt is unsafe")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var stat syscall.Stat_t
	if err = syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0077 != 0 || !poolVMAutostartOwner(stat.Uid) {
		return false, errors.New("pool VM autostart attempt changed or is unsafe")
	}
	b, err := io.ReadAll(io.LimitReader(f, poolVMAutostartAttemptLimit+1))
	if err != nil || len(b) > poolVMAutostartAttemptLimit {
		return false, errors.New("pool VM autostart attempt is unreadable")
	}
	var attempt poolVMAutostartAttempt
	if json.Unmarshal(b, &attempt) != nil || attempt.VMID != vmID || attempt.BootID != bootID {
		return false, errors.New("pool VM autostart attempt is invalid")
	}
	return false, nil
}

func poolVMAutostartOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && poolVMAutostartOwner(stat.Uid)
}

func trustedPoolVMAutostartDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !poolVMAutostartOwned(info) {
		return errors.New("unsafe private directory")
	}
	return nil
}

func trustedPoolVMAutostartStateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("unsafe Core state directory path")
	}
	if err := trustedPoolVMAutostartDir(path); err != nil {
		return err
	}
	// A writable ancestor may rename the state directory between validation and
	// creation. Sticky parents protect an entry owned by this Core; all other
	// ancestors must not be writable by group or other users.
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe Core state parent")
		}
		if info.Mode().Perm()&0022 != 0 {
			if info.Mode()&os.ModeSticky == 0 || !poolVMAutostartOwned(info) {
				return errors.New("writable Core state parent is unsafe")
			}
		}
		if parent == "/" {
			return nil
		}
	}
}

// prunePoolVMOwners removes owner records for VMs their owner has provably deleted. The
// rule is positive evidence only, never absence: a record is removed when the host that
// just published its inventory is the record's owner, that inventory carries a succeeded
// vm-delete tombstone (or a create it proved failed before allocating) for the same pool
// and host, and no other host's stored inventory still defines that VM. A deleted VM ID
// can never be defined again on the same host, so the tombstone cannot prune a later VM,
// and a pending update for it can no longer take effect.
// Everything else is kept: an unreported, unreachable or mid-placement VM keeps its owner
// record, because a lost record is a lost fence. It returns the pruned IDs.
func prunePoolVMOwners(state *poolVMState, observedHost string) []string {
	inventory, ok := state.Inventory[observedHost]
	if !ok || len(inventory.Deleted) == 0 {
		return nil
	}
	defined := map[string]bool{}
	for host, inv := range state.Inventory {
		if host == observedHost {
			continue
		}
		for _, vm := range inv.VMs {
			defined[vm.ID] = true
		}
	}
	var pruned []string
	for _, id := range inventory.Deleted {
		record, exists := state.VMs[id]
		if !exists || record.HostID != observedHost || defined[id] {
			continue
		}
		// A tombstone cannot discard unresolved snapshot state. In particular,
		// keep the catalog if an older/foreign delete reached libvirt before the
		// owner check existed; inventory validation will report that conflict.
		if poolVMHasSnapshotState(record) {
			continue
		}
		delete(state.VMs, id)
		pruned = append(pruned, id)
	}
	return pruned
}

func poolVMHasSnapshotState(record poolVMRecord) bool {
	return record.PendingRecovery != nil || record.PendingSnapshot != nil || len(record.Snapshots) != 0
}

func poolVMFile(s *Store) string { return filepath.Join(s.dir, "pool-vms.json") }
func loadPoolVMs(s *Store) (poolVMState, error) {
	out := poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{}}
	b, err := os.ReadFile(poolVMFile(s))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if json.Unmarshal(b, &out) != nil || out.VMs == nil {
		return out, errors.New("invalid pool VM state")
	}
	if out.Inventory == nil {
		out.Inventory = map[string]poolVMInventory{}
	}
	return out, nil
}
func savePoolVMs(s *Store, state poolVMState) error {
	if err := poolVMRecoverySettlementHeadroom(s, state); err != nil {
		return err
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(poolVMFile(s), b, 0600)
}

// poolVMRecoverySettlementProjection reserves each pending VM's largest current,
// completed or no-effects-failure representation. Independent recoveries may settle
// in any order, so pending deletion shrinkage never offsets another VM's growth.
// It returns nil when no recovery is pending. Callers hold poolVMMu.
func poolVMRecoverySettlementProjection(state poolVMState) (json.RawMessage, error) {
	var prospective map[string]poolVMRecord
	for id, record := range state.VMs {
		if record.PendingRecovery == nil && record.PendingRecoveryResultSpec != nil {
			return nil, errors.New("snapshot revert result definition has no pending reservation")
		}
		if record.PendingRecovery == nil {
			continue
		}
		if prospective == nil {
			prospective = make(map[string]poolVMRecord, len(state.VMs))
			for otherID, other := range state.VMs {
				prospective[otherID] = other
			}
		}
		var descriptor *protocol.PoolVMSnapshot
		var resultSpec *protocol.VMCreateSpec
		outcome := "failed-before-effects"
		if record.PendingRecovery.Kind == "snapshot-create" && record.PendingRecovery.AdmissionRefusal == "" {
			if record.PendingSnapshot == nil {
				return nil, errors.New("pending snapshot create has no durable descriptor")
			}
			copy := *record.PendingSnapshot
			descriptor = &copy
			outcome = "completed"
		} else if record.PendingRecovery.Kind == "snapshot-revert" {
			if record.PendingRecoveryResultSpec == nil {
				return nil, errors.New("pending snapshot revert has no derived result definition")
			}
			copy := *record.PendingRecoveryResultSpec
			resultSpec = &copy
			outcome = "completed"
		} else if record.PendingRecovery.Kind == "snapshot-delete" {
			outcome = "completed"
		}
		evidence := protocol.PoolVMRecoveryEvidence{
			Reservation: *record.PendingRecovery, Outcome: outcome, JournalSHA256: strings.Repeat("0", 64), Snapshot: descriptor, ResultSpec: resultSpec,
		}
		current, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		largest, largestSize := record, len(current)
		failure := protocol.PoolVMRecoveryEvidence{Reservation: *record.PendingRecovery,
			Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("0", 64)}
		for i, terminal := range [...]protocol.PoolVMRecoveryEvidence{evidence, failure} {
			if i == 1 && outcome == "failed-before-effects" {
				break
			}
			candidate := record
			if err := settlePoolVMRecovery(&candidate, record.HostID, terminal); err != nil {
				return nil, errors.New("pending snapshot recovery has invalid terminal replica intent")
			}
			encoded, err := json.Marshal(candidate)
			if err != nil {
				return nil, err
			}
			if len(encoded) > largestSize {
				largest, largestSize = candidate, len(encoded)
			}
		}
		prospective[id] = largest
	}
	if prospective == nil {
		return nil, nil
	}
	vmBytes, err := json.Marshal(prospective)
	if err != nil || len(vmBytes) > protocol.PoolReplicaLimit {
		return nil, errors.New("pending snapshots would exceed the pool authority limit when settled")
	}
	return vmBytes, nil
}

// poolVMRecoverySettlementHeadroom prevents later owner writes from consuming the
// replica capacity already promised to a pending snapshot's terminal catalog entry.
func poolVMRecoverySettlementHeadroom(s *Store, state poolVMState) error {
	pending := false
	for _, record := range state.VMs {
		if record.PendingRecovery == nil && record.PendingRecoveryResultSpec != nil {
			return errors.New("snapshot revert result definition has no pending reservation")
		}
		pending = pending || record.PendingRecovery != nil
	}
	if !pending {
		return nil
	}
	peer, err := loadPoolPeer(s)
	if err != nil || peer.Membership.Valid() != nil || peer.Membership.PoolID != s.State.PoolID {
		return errors.New("pool authority is unavailable while a snapshot reservation is pending")
	}
	if peer.Membership.MasterHostID != s.State.HostID {
		return nil // Members do not write the master's pool-vms.json owner projection.
	}
	vmBytes, err := poolVMRecoverySettlementProjection(state)
	if err != nil {
		return err
	}
	replica, _, err := buildPoolAuthorityReplicaForMembership(s, peer.Membership, vmBytes)
	if err != nil || poolAuthorityReplicaFitsAddressedBodies(s, replica, peer.CAKeyPEM) != nil {
		return errors.New("pending snapshots would exceed the pool replica limit when settled")
	}
	return nil
}

func poolSharedVM(ctx context.Context, s *Store, cmd protocol.Command) (protocol.VMCreateSpec, bool, error) {
	var v protocol.VMCreateSpec
	if cmd.VM != nil {
		v = *cmd.VM
	} else if cmd.Power != nil {
		state, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
		if !managedVMChecksReady(checks) {
			return v, false, errors.New("managed VM state is unavailable")
		}
		for _, candidate := range state.VMs {
			if candidate.ID == cmd.Power.VMID {
				v = candidate
				break
			}
		}
	} else if cmd.Delete != nil || cmd.ReleaseRetained != nil {
		id := ""
		if cmd.Delete != nil {
			id = cmd.Delete.VMID
		} else {
			id = cmd.ReleaseRetained.VMID
		}
		state, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
		if !managedVMChecksReady(checks) {
			return v, false, errors.New("managed VM state is unavailable")
		}
		for _, candidate := range state.VMs {
			if candidate.ID == id {
				v = candidate
				break
			}
		}
	} else if cmd.VMMedia != nil {
		state, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
		if !managedVMChecksReady(checks) {
			return v, false, errors.New("managed VM state is unavailable")
		}
		for _, candidate := range state.VMs {
			if candidate.ID == cmd.VMMedia.VMID {
				v = candidate
				break
			}
		}
	}
	if v.ID == "" {
		return v, false, nil
	}
	state, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	if !managedVMChecksReady(checks) {
		return v, false, errors.New("managed storage state is unavailable")
	}
	definitions := []protocol.VMCreateSpec{v}
	if cmd.Kind == "vm-update" {
		for _, current := range state.VMs {
			if current.ID == v.ID {
				definitions = append(definitions, current)
				break
			}
		}
	}
	for _, definition := range definitions {
		for _, disk := range definition.Disks {
			for _, storage := range state.Storage {
				if storage.ID == disk.StorageID && isPoolSharedStorage(storage) {
					return v, true, nil
				}
			}
		}
	}
	// A VM which ever used a successfully registered pool-shared repository
	// remains pool-owned after an edit removes its final shared disk or after
	// that repository is detached. The proof is read-only managed history, not
	// a member policy cache.
	if cmd.Kind == "vm-update" || cmd.Power != nil || cmd.Delete != nil || cmd.ReleaseRetained != nil || cmd.VMMedia != nil {
		used, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).VMHasPoolSharedStorageHistory(ctx, v.ID)
		if err != nil {
			return v, false, errors.New("managed VM shared-storage history is unavailable")
		}
		if used {
			return v, true, nil
		}
	}
	return v, false, nil
}

func poolVMUpdatePrevious(ctx context.Context, s *Store, id string) (*protocol.VMCreateSpec, error) {
	state, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	if !managedVMChecksReady(checks) {
		return nil, errors.New("managed VM state is unavailable")
	}
	for _, candidate := range state.VMs {
		if candidate.ID == id {
			previous := candidate
			return &previous, nil
		}
	}
	return nil, errors.New("managed VM definition is unavailable")
}

// ManagedEngine always reports observational checks.  Healthy observations are
// positive evidence; only failed or unknown journal/libvirt state blocks a
// shared-disk execution admission.
func managedVMChecksReady(checks []protocol.Check) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if check.Status != "healthy" && check.Status != "passed" {
			return false
		}
	}
	return true
}

func isPoolSharedStorage(s protocol.StorageRegistration) bool {
	return protocol.IsPoolSharedStorage(s)
}

// poolVMExecutionContext binds a VM operation to the master-approved clustered
// storage profile.  It is deliberately derived from the local managed journal,
// never from the client command. ValidateClusteredISCSIAt is also a live native
// readiness gate before a start permit is written.
func poolVMExecutionContext(ctx context.Context, s *Store, v protocol.VMCreateSpec) (context.Context, error) {
	return poolVMExecutionContextWithLeaseResources(ctx, s, v, false)
}

func poolVMExecutionContextWithLeaseResources(ctx context.Context, s *Store, v protocol.VMCreateSpec, initializeVMResources bool) (context.Context, error) {
	storage, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedStorage()
	if !managedVMChecksReady(checks) {
		return nil, errors.New("managed storage state is unavailable")
	}
	byID := map[string]protocol.StorageRegistration{}
	for _, storage := range storage {
		byID[storage.ID] = storage
	}
	var selected protocol.StorageClusterConfig
	for _, disk := range v.Disks {
		storage, ok := byID[disk.StorageID]
		if !ok || !protocol.IsClusteredISCSI(storage) {
			continue
		}
		config, err := StorageClusterConfigForRepository(s, storage.ID)
		if err != nil || config.ValidFor(storage) != nil || !config.FencingVerified() {
			return nil, errors.New("clustered VM storage configuration is unavailable or unsafe")
		}
		membership, err := Membership(s)
		if err != nil || !poolVMClusterOperationActivated(s, membership, config) {
			return nil, errors.New("clustered VM storage has not completed safe initial activation for the current pool membership")
		}
		if selected.RepositoryID != "" && selected.RepositoryID != config.RepositoryID {
			return nil, errors.New("a VM may not use multiple clustered iSCSI repositories")
		}
		if _, err := vmhost.ValidateClusteredISCSIAt(ctx, s.dir, storage, config); err != nil {
			return nil, errors.New("clustered VM storage is not ready")
		}
		selected = config
	}
	if selected.RepositoryID != "" {
		ctx = vmhost.WithStorageClusterStateDir(vmhost.WithStorageClusterConfig(ctx, selected), s.dir)
	}
	leaseCtx, err := poolVMStorageLeaseContext(ctx, s, v, storage, initializeVMResources)
	if err != nil {
		return nil, err
	}
	if leaseCtx != nil {
		ctx = leaseCtx
	}
	return ctx, nil
}

func poolVMStorageLeaseContext(ctx context.Context, s *Store, vm protocol.VMCreateSpec, applied []protocol.StorageRegistration, initializeVMResources bool) (context.Context, error) {
	usesNFS := false
	byID := make(map[string]protocol.StorageRegistration, len(applied))
	for _, registration := range applied {
		byID[registration.ID] = registration
	}
	for _, disk := range vm.Disks {
		registration, ok := byID[disk.StorageID]
		if ok && registration.Kind == protocol.StorageKindNFS && protocol.IsPoolSharedStorage(registration) {
			usesNFS = true
		}
	}
	if !usesNFS {
		return nil, nil
	}
	local, err := Membership(s)
	if err != nil {
		return nil, err
	}
	membership := local
	if local.MasterHostID != s.State.HostID {
		replica, replicaErr := loadPoolReplica(s)
		if replicaErr != nil || replica == nil || replica.Replica.PoolID != local.PoolID || replica.Replica.MemberHostID != s.State.HostID {
			return nil, errors.New("pool storage-lease authority replica is unavailable")
		}
		membership = replica.Replica.Membership
	}
	lease := membership.StorageLease
	if lease == nil {
		return nil, nil
	}
	if lease.PreparedGeneration == 0 || lease.Valid(membership.PoolID) != nil {
		return nil, errors.New("shared NFS VM storage has not completed pool lease preparation")
	}
	self, ok := poolMemberByID(membership, s.State.HostID)
	if !ok || self.Status != "ready" || self.SanlockHostID == 0 {
		return nil, errors.New("this host has no prepared sanlock membership identity")
	}
	if err := requirePoolStorageLeaseReset(ctx, self); err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(membership.Members))
	for _, member := range membership.Members {
		if member.Status == "ready" {
			if member.SanlockHostID == 0 {
				return nil, errors.New("a ready pool member has no stable sanlock host ID")
			}
			ids = append(ids, member.SanlockHostID)
		}
	}
	slices.Sort(ids)
	joinCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := preparePoolStorageLeaseHost(joinCtx, s, membership, false); err != nil {
		return nil, err
	}
	joined := make([]vmhost.SanlockJoinedRepository, 0, len(lease.Repositories))
	for _, repository := range lease.Repositories {
		storage, ok := byID[repository.RepositoryID]
		if !ok || storage.Tombstone || storage.Generation != repository.RepositoryGeneration ||
			storage.Identity.String() != repository.RepositoryIdentity.String() || !poolNFSIdentity(storage.Identity) || !protocol.IsPoolSharedStorage(storage) {
			return nil, errors.New("shared NFS VM storage does not match the prepared repository identity")
		}
		storage, err = vmhost.ValidatePoolStorageAccess(joinCtx, storage)
		if err != nil || storage.Identity.String() != repository.RepositoryIdentity.String() {
			return nil, errors.New("shared NFS VM repository is not mounted with the prepared identity")
		}
		path, err := vmhost.SanlockLockspacePath(storage.Path)
		if err != nil {
			return nil, errors.New("shared NFS VM repository has no safe sanlock path")
		}
		status, err := vmhost.EnsureSanlockLockspace(joinCtx, vmhost.SanlockLockspaceConfig{
			Lockspace: repository.Lockspace, LeasePath: path, LocalHostID: self.SanlockHostID,
			HostIDs: ids, IsMaster: membership.MasterHostID == s.State.HostID, IOTimeout: uint32(lease.IOTimeoutSeconds),
		})
		if err != nil || status.HostID != self.SanlockHostID || status.HostState != "HOST_LIVE" {
			return nil, errors.New("this host has not joined every VM storage lockspace")
		}
		joined = append(joined, vmhost.SanlockJoinedRepository{
			RepositoryID: repository.RepositoryID, Lockspace: repository.Lockspace,
			HostID: status.HostID, Generation: status.Generation, HostState: status.HostState,
		})
	}
	return vmhost.WithPoolStorageLeaseConfig(ctx, membership.PoolID, *lease, self.SanlockHostID, ids, joined, initializeVMResources), nil
}

// poolVMClusterOperationActivated distinguishes an initial, all-member Core
// admission from later runtime health. The native validator that follows this
// check remains authoritative for current local quorum/fencing readiness.
func poolVMClusterOperationActivated(s *Store, membership protocol.PoolMembership, config protocol.StorageClusterConfig) bool {
	if !clusterCurrentMember(s.State.HostID, membership, config) {
		return false
	}
	for _, op := range StorageClusterStatus(s) {
		if clusterOperationActivated(op, membership, config) {
			return true
		}
	}
	return false
}

func poolVMCommandExecutionContext(ctx context.Context, s *Store, cmd protocol.Command) (context.Context, error) {
	if cmd.Kind == "storage-recheck" && cmd.StorageLifecycle != nil {
		managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
		if !managedVMChecksReady(checks) {
			return ctx, errors.New("managed storage state is unavailable")
		}
		for _, storage := range managed.Storage {
			if storage.ID != cmd.StorageLifecycle.RepositoryID || !protocol.IsClusteredISCSI(storage) {
				continue
			}
			config, err := StorageClusterConfigForRepository(s, storage.ID)
			if err != nil || config.ValidFor(storage) != nil || !config.FencingVerified() {
				return ctx, errors.New("clustered storage recheck configuration is unavailable or unsafe")
			}
			membership, err := Membership(s)
			if err != nil || !poolVMClusterOperationActivated(s, membership, config) {
				return ctx, errors.New("clustered storage recheck has not completed safe initial activation for the current pool membership")
			}
			return vmhost.WithStorageClusterStateDir(vmhost.WithStorageClusterConfig(ctx, config), s.dir), nil
		}
	}
	v, shared, err := poolSharedVM(ctx, s, cmd)
	if err != nil || !shared {
		return ctx, err
	}
	if err := poolVMStopOnly(s, cmd, v); err == nil {
		// A stop only reduces the locally observed execution set.  It neither
		// opens the clustered filesystem nor writes a start permit, so a lost
		// quorum must not prevent an operator from stopping the exact local
		// owner.  All state-changing admissions remain below.
		return ctx, nil
	} else if isPoolVMStopOnly(cmd) {
		return ctx, err
	}
	return poolVMExecutionContextWithLeaseResources(ctx, s, v, cmd.Kind == "vm-create" || cmd.Kind == "vm-update")
}

// isPoolVMStopOnly identifies the two reducing-only power requests.  Reboot and
// force-reboot deliberately remain execution admissions: they can resume a writable VM.
func isPoolVMStopOnly(cmd protocol.Command) bool {
	return cmd.Kind == "vm-power" && cmd.Power != nil && (cmd.Power.Action == "shutdown" || cmd.Power.Action == "force-stop")
}

// poolVMStartsExecution identifies the requests that begin a new execution on this host: a
// power start, the S3 wake AdmitPoolVMExecution turns a start into, and a create that asks
// for the guest to be started. A reboot or force-reboot needs a guest that is already
// running here, so it is not a new placement and maintenance never refuses it.
func poolVMStartsExecution(cmd protocol.Command) bool {
	if cmd.Kind == "vm-create" {
		return cmd.VM != nil && cmd.VM.StartAfterCreate
	}
	return cmd.Kind == "vm-power" && cmd.Power != nil && (cmd.Power.Action == "start" || cmd.Power.Action == "wake")
}

// poolVMStopOnly proves a stop request is for the exact locally journaled
// owner before allowing it to bypass live clustered-storage readiness.  It
// does not contact or alter the master record; a fresh authenticated inventory
// observation reconciles a confirmed stop after quorum returns.
func poolVMStopOnly(s *Store, cmd protocol.Command, v protocol.VMCreateSpec) error {
	if !isPoolVMStopOnly(cmd) {
		return errors.New("VM power action is not stop-only")
	}
	if err := vmhost.ValidateManagedCommand(cmd); err != nil {
		return err
	}
	p := cmd.Power
	if v.ID == "" || p.VMID != v.ID || p.PoolID != s.State.PoolID || p.HostID != s.State.HostID || v.PoolID != s.State.PoolID || v.HostID != s.State.HostID {
		return errors.New("stop request does not match the local pool VM owner")
	}
	return nil
}

func randomPermit() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func uuidLike(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil
}

// PoolVMHandler is mounted by PoolPeerHandler.  It accepts requests only from a ready member;
// the master writes the record before returning a start permit.  There is intentionally no
// expiry or timeout cleanup: unknown execution remains fenced.
func PoolVMHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		member, err := AuthenticatePoolPeer(s, r)
		if err != nil {
			http.Error(w, "peer rejected", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == poolVMPath+"inventory" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var inv poolVMInventory
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&inv) != nil || inv.HostID != member.HostID {
				http.Error(w, "invalid VM inventory", http.StatusBadRequest)
				return
			}
			membership, err := Membership(s)
			if err != nil {
				http.Error(w, "membership unavailable", http.StatusConflict)
				return
			}
			if err = recordPoolVMInventory(s, membership, inv); err != nil {
				if errors.Is(err, errPoolVMSnapshotTombstone) {
					http.Error(w, err.Error(), http.StatusConflict)
					return
				}
				if errors.Is(err, errPoolVMInventoryIncomplete) || errors.Is(err, errPoolVMOwnersDeferred) {
					// The snapshot is durable but cannot activate legacy owner
					// adoption until every current ready member has reported.
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if errors.Is(err, errPoolVMInventoryStale) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(poolVMInventoryReply{Membership: membership})
					return
				}
				http.Error(w, "VM inventory refused", http.StatusConflict)
				return
			}
			completePoolVMReplicaRejoin(r.Context(), s, inv.HostID)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(poolVMInventoryReply{})
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, poolVMPath) && strings.HasSuffix(r.URL.Path, "/snapshots") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, poolVMPath), "/snapshots")
			membership, membershipErr := Membership(s)
			if !uuidLike(id) || membershipErr != nil || membership.MasterHostID != s.State.HostID {
				http.Error(w, "snapshot catalog is unavailable on this host", http.StatusConflict)
				return
			}
			poolVMMu.Lock()
			state, stateErr := loadPoolVMs(s)
			poolVMMu.Unlock()
			if stateErr != nil {
				http.Error(w, "snapshot catalog is unavailable", http.StatusServiceUnavailable)
				return
			}
			record, ok := state.VMs[id]
			if !ok || record.PoolID != membership.PoolID {
				http.Error(w, "pool VM snapshot catalog not found", http.StatusNotFound)
				return
			}
			if err := validatePoolVMSnapshotCatalog(record); err != nil {
				http.Error(w, "pool VM snapshot catalog is invalid", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(poolVMSnapshotCatalog{VMID: id, Snapshots: record.Snapshots})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, poolVMPath)
		if id == "" || strings.ContainsAny(id, "/\\") {
			http.NotFound(w, r)
			return
		}
		var req poolVMRequest
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !uuidLike(id) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil || req.VMID != id {
			http.Error(w, "invalid VM execution request", http.StatusBadRequest)
			return
		}
		placementAbsent := req.PlacementAbsent
		req.PlacementAccepted, req.PlacementReleased, req.UpdateAccepted, req.SpecMatch, req.SnapshotAccepted, req.SnapshotSettled = false, false, false, false, false, false
		// A definition placement is decided by the master for every VM, including
		// local-storage guests.  Do the bounded target RPC before the durable VM
		// lock; locks must never span a peer request.
		var placement poolPlacementReply
		if req.Action == "define" && req.Create || req.Action == "update-reserve" {
			membership, membershipErr := Membership(s)
			cmdKind := "vm-create"
			if req.Action == "update-reserve" {
				cmdKind = "vm-update"
			}
			command := protocol.Command{OperationID: req.OperationID, Kind: cmdKind, VM: req.VM}
			if membershipErr != nil || req.OperationID == "" || req.VM == nil {
				http.Error(w, "VM placement preflight is unavailable", http.StatusConflict)
				return
			}
			placement, membershipErr = poolVMPlacement(r.Context(), s, membership, member, command)
			if membershipErr == nil {
				membershipErr = poolVMPlacementStorageMatches(r.Context(), s, placement, *req.VM)
			}
			if membershipErr != nil {
				http.Error(w, "VM placement preflight refused: "+placementError(membershipErr), http.StatusConflict)
				return
			}
		}
		poolVMMu.Lock()
		poolVMLocked := true
		defer func() {
			if poolVMLocked {
				poolVMMu.Unlock()
			}
		}()
		// A frozen or handed-over pool authority refuses new ownership and new start
		// permits here, inside the same lock section that would record them, which is what
		// makes the freeze an ordering guarantee rather than a hint.
		if err := requirePoolVMAuthorityWritable(s, req.Action, req.Create); err != nil {
			http.Error(w, placementError(err), http.StatusConflict)
			return
		}
		// All placement admission paths take the established VM → peer lock order.
		// Keep the peer lock through savePoolVMs so membership cannot change between
		// the authenticated preflight and durable owner/reservation record.
		placementMutation := (req.Action == "define" && req.Create) || req.Action == "update-reserve"
		recoveryMutation := req.Action == "snapshot-settle" || req.Action == "release-restore-placement"
		poolPeerLocked := false
		defer func() {
			if poolPeerLocked {
				poolPeerMu.Unlock()
			}
		}()
		var currentMembership protocol.PoolMembership
		if placementMutation || recoveryMutation {
			poolPeerMu.Lock()
			poolPeerLocked = true
			peer, peerErr := loadPoolPeer(s)
			if peerErr != nil || peer.Membership.Valid() != nil {
				http.Error(w, "VM placement membership is unavailable", http.StatusConflict)
				return
			}
			currentMembership = peer.Membership
			if recoveryMutation && !poolPeerMemberCurrent(currentMembership, member) {
				http.Error(w, "VM recovery owner is no longer a current pool member", http.StatusConflict)
				return
			}
			if recoveryMutation {
				if refusal := poolAuthorityWritable(peer); refusal != nil {
					http.Error(w, placementError(refusal), http.StatusConflict)
					return
				}
			}
		}
		state, err := loadPoolVMs(s)
		if err != nil {
			http.Error(w, "execution state unavailable", 500)
			return
		}
		if placementMutation && !placementMembershipMatches(s, currentMembership, placement.Generation, member) {
			http.Error(w, "VM placement membership changed before durable admission", http.StatusConflict)
			return
		}
		record, ok := state.VMs[id]
		if placement.Replay && (!ok || (req.Action == "update-reserve" && !placementReservationMatches(record, req.OperationID, req.VM))) {
			http.Error(w, "VM placement replay has no durable master record", http.StatusConflict)
			return
		}
		switch req.Action {
		case "release-restore-placement":
			if !placementAbsent || req.VM == nil || req.VM.HostID != member.HostID || req.OperationID == "" {
				http.Error(w, "restore placement release is invalid", http.StatusBadRequest)
				return
			}
			if err = releaseRestorePlacementOwnerLocked(s, &state, *req.VM, req.OperationID); err != nil {
				http.Error(w, placementError(err), http.StatusConflict)
				return
			}
			req.PlacementReleased = true
		case "snapshot-reserve":
			ack, reserveErr := reservePoolVMSnapshotLocked(s, member.HostID, req)
			if reserveErr != nil {
				http.Error(w, placementError(reserveErr), http.StatusConflict)
				return
			}
			req = ack
		case "snapshot-metadata":
			if req.Snapshot == nil || req.Snapshot.ValidFor("snapshot-metadata") != nil {
				http.Error(w, "invalid VM snapshot metadata update", http.StatusBadRequest)
				return
			}
			if !ok || record.HostID != member.HostID {
				http.Error(w, errSnapshotMetadataOwner.Error(), http.StatusConflict)
				return
			}
			updated, metaErr := applyPoolVMSnapshotMetadata(&record, *req.Snapshot)
			if metaErr != nil {
				http.Error(w, placementError(metaErr), http.StatusConflict)
				return
			}
			state.VMs[id] = record
			if err = savePoolVMs(s, state); err != nil {
				http.Error(w, "execution state unavailable", 500)
				return
			}
			req.Snapshot = &protocol.VMSnapshotRequest{SnapshotID: updated.ID, Name: updated.Name, Description: updated.Description, Target: &updated}
			req.Epoch = record.Epoch
			if poolPeerLocked {
				poolPeerMu.Unlock()
				poolPeerLocked = false
			}
			poolVMMu.Unlock()
			poolVMLocked = false
			if ackErr := confirmPoolVMSnapshotMetadataReplica(r.Context(), s, id, updated); ackErr != nil {
				http.Error(w, "snapshot metadata was saved but is not yet acknowledged by every ready member", http.StatusConflict)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(req)
			return
		case "snapshot-settle":
			if req.RecoveryEvidence == nil || req.RecoveryEvidence.Reservation.VMID != id || req.RecoveryEvidence.Reservation.OwnerHostID != member.HostID {
				http.Error(w, "snapshot settlement does not match the authenticated VM owner", http.StatusConflict)
				return
			}
			if err := settlePoolVMSnapshotStateLocked(s, &state, currentMembership, member.HostID, *req.RecoveryEvidence); err != nil {
				http.Error(w, placementError(err), http.StatusConflict)
				return
			}
			req.SnapshotSettled = true
		case "update-reserve":
			resourceErr := error(nil)
			if req.VM != nil {
				resourceErr = poolResourceMutationRefusal(s, *req.VM)
			}
			if req.VM == nil || !placementMutation || resourceErr != nil || vmhost.ValidateManagedCommand(protocol.Command{OperationID: req.OperationID, Kind: "vm-update", VM: req.VM}) != nil || reserveOrSeedPoolVMUpdate(&state, currentMembership, &record, ok, member.HostID, req.OperationID, req.Previous, req.VM) != nil {
				http.Error(w, "VM update is not eligible for the durable pool owner record", http.StatusConflict)
				return
			}
			state.VMs[id] = record
			err = savePoolVMs(s, state)
			if err == nil && record.PendingUpdate != nil {
				req.UpdateAccepted = true
				req.UpdateRevision = record.PendingUpdate.Next.Revision
				req.PlacementAccepted = true
			}
		case "update-commit":
			if !ok || req.Update == nil || commitPoolVMUpdate(&record, member.HostID, *req.Update) != nil {
				http.Error(w, "VM update journal evidence does not match the durable pool owner record", http.StatusConflict)
				return
			}
			state.VMs[id] = record
			err = savePoolVMs(s, state)
			if err == nil {
				req.UpdateAccepted = true
				req.UpdateRevision = record.Spec.Revision
			}
		case "update-abort":
			if !ok || req.Update == nil || abortPoolVMUpdate(&record, member.HostID, *req.Update) != nil {
				http.Error(w, "VM update failure evidence does not match the durable pool owner record", http.StatusConflict)
				return
			}
			state.VMs[id] = record
			err = savePoolVMs(s, state)
			if err == nil {
				req.UpdateAccepted = true
				req.UpdateRevision = req.Update.Current.Revision
			}
		case "define":
			if ok && record.PendingRecovery != nil {
				http.Error(w, "VM snapshot recovery is pending reconciliation", http.StatusConflict)
				return
			}
			if ok && (record.PendingUpdate != nil || record.PoolID != s.State.PoolID || record.HostID != member.HostID) {
				http.Error(w, "VM is owned by another host", http.StatusConflict)
				return
			}
			if ok && req.VM != nil && !poolVMRecordMatchesSpec(record, *req.VM) {
				http.Error(w, "VM definition does not match the durable pool owner record", http.StatusConflict)
				return
			}
			if !ok && !req.Create {
				http.Error(w, "VM has no durable pool execution record", http.StatusConflict)
				return
			}
			if !ok {
				if req.VM == nil || req.VM.ID != id || req.VM.PoolID != s.State.PoolID || req.VM.HostID != member.HostID || protocol.ValidateVMSpec(*req.VM) != nil {
					http.Error(w, "invalid VM definition", http.StatusBadRequest)
					return
				}
				if req.Create && poolResourceMutationRefusal(s, *req.VM) != nil {
					http.Error(w, "VM definition uses a network or media resource with removal pending", http.StatusConflict)
					return
				}
				record = poolVMRecord{PoolID: s.State.PoolID, VMID: id, HostID: member.HostID, CreateOperationID: req.OperationID, State: "defined", Autostart: req.VM.Autostart, Epoch: 1, Spec: *req.VM}
				state.VMs[id] = record
				err = savePoolVMs(s, state)
			}
			if req.VM != nil && err == nil && poolVMRecordMatchesSpec(record, *req.VM) {
				req.SpecMatch = true
				if req.Create {
					req.PlacementAccepted = true
				}
			}
		case "delete-check":
			if !ok || req.VM == nil || record.PendingUpdate != nil || record.PendingRecovery != nil || record.PendingStartOperationID != "" ||
				record.PoolID != s.State.PoolID || record.HostID != member.HostID || !poolVMRecordMatchesSpec(record, *req.VM) {
				http.Error(w, "VM delete does not match the durable pool owner record", http.StatusConflict)
				return
			}
			if err := validatePoolVMSnapshotCatalog(record); err != nil {
				http.Error(w, "VM snapshot catalog is invalid", http.StatusConflict)
				return
			}
			if len(record.Snapshots) != 0 {
				http.Error(w, "VM has retained snapshots; delete its snapshots before deleting the VM", http.StatusConflict)
				return
			}
			req.SpecMatch = true
		case "start":
			// A drained host starts nothing new. Stop and the transitions that follow a
			// start already under way are deliberately not refused here.
			if err := poolHostInMaintenance(s, member.HostID); err != nil {
				http.Error(w, placementError(err), http.StatusConflict)
				return
			}
			if !ok || !validPoolVMUpdateOperation(req.OperationID) || record.PendingUpdate != nil || record.PendingRecovery != nil || record.PendingStartOperationID != "" || record.PoolID != s.State.PoolID || record.HostID != member.HostID || (record.State != "defined" && record.State != "stopped") {
				http.Error(w, "VM execution is not eligible to start", http.StatusConflict)
				return
			}
			record.State, record.Epoch = "starting", record.Epoch+1
			record.PendingStartOperationID = req.OperationID
			state.VMs[id] = record
			err = savePoolVMs(s, state)
			if err == nil {
				req.Permit, err = randomPermit()
			}
		case "running":
			if !ok || record.PendingUpdate != nil || record.PendingRecovery != nil || record.HostID != member.HostID || record.State != "starting" || record.PendingStartOperationID == "" || req.OperationID != record.PendingStartOperationID || req.Epoch == 0 || req.Epoch != record.Epoch {
				http.Error(w, "VM execution transition refused", http.StatusConflict)
				return
			}
			record.State = "running"
			record.PendingStartOperationID = ""
			state.VMs[id] = record
			err = savePoolVMs(s, state)
		case "stop":
			if !ok || record.PendingUpdate != nil || record.HostID != member.HostID {
				http.Error(w, "VM execution owner mismatch", http.StatusConflict)
				return
			}
		case "stopped":
			// Only the recorded owner may prove a stop.  It retains placement; another host
			// cannot claim the VM merely because this host is quiet or unreachable.
			if !ok || record.PendingUpdate != nil || record.HostID != member.HostID {
				http.Error(w, "VM execution owner mismatch", http.StatusConflict)
				return
			}
			if record.State == "starting" {
				if record.PendingStartOperationID == "" || req.OperationID != record.PendingStartOperationID || req.Epoch == 0 || req.Epoch != record.Epoch || record.PendingRecovery != nil {
					http.Error(w, "VM start outcome does not match the pending owner operation", http.StatusConflict)
					return
				}
			} else if req.OperationID != "" || req.Epoch != 0 {
				http.Error(w, "stale VM start outcome", http.StatusConflict)
				return
			}
			record.State = "stopped"
			record.PendingStartOperationID = ""
			state.VMs[id] = record
			err = savePoolVMs(s, state)
		case "reboot", "force-reboot", "wake", "media":
			// wake resumes a guest that put itself to sleep (ACPI S3): the domain is
			// still this owner's running execution, so the record keeps state and epoch.
			// It is nevertheless an operator pressing Start, so a drained host refuses it;
			// a reboot of a guest already executing there is not a new placement and is
			// deliberately allowed.
			if req.Action == "wake" {
				if err := poolHostInMaintenance(s, member.HostID); err != nil {
					http.Error(w, placementError(err), http.StatusConflict)
					return
				}
			}
			if !ok || record.PendingUpdate != nil || record.PendingRecovery != nil || record.PendingStartOperationID != "" || record.HostID != member.HostID || (req.Action != "media" && record.State != "running") {
				http.Error(w, "VM execution is not eligible for this action", http.StatusConflict)
				return
			}
		default:
			http.Error(w, "unsupported VM execution action", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "execution state persistence failed", 500)
			return
		}
		if req.Action == "snapshot-reserve" || req.Action == "snapshot-settle" {
			if poolPeerLocked {
				poolPeerMu.Unlock()
				poolPeerLocked = false
			}
			poolVMMu.Unlock()
			poolVMLocked = false
			var ackErr error
			if req.Action == "snapshot-reserve" {
				ackErr = confirmPoolVMSnapshotReplica(r.Context(), s, &req, nil)
			} else if req.RecoveryEvidence != nil {
				ackErr = confirmPoolVMSnapshotReplica(r.Context(), s, nil, req.RecoveryEvidence)
			}
			if ackErr != nil {
				w.Header().Set(poolSnapshotSavedHeader, "1")
				http.Error(w, "snapshot state was saved but is not yet acknowledged by every ready member", http.StatusConflict)
				return
			}
		}
		req.Epoch = record.Epoch
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(req)
	})
}

// poolVMMediaChangesDefinition reports whether a media request would rewrite the durable VM
// definition the pool master owns.  Re-inserting the ISO that definition already names does
// not: it reconnects a tray, which is how an operator recovers a disc the guest ejected.
func poolVMMediaChangesDefinition(v protocol.VMCreateSpec, req protocol.VMMediaRequest) bool {
	return !reflect.DeepEqual(protocol.ApplyVMMedia(v, req.Action, req.Drive, req.MediaID), v)
}

// PoolVMMediaCommand expresses a shared VM's media change as the same durable prior→next
// definition transition vm-update uses, so the master reserves the exact next revision
// before this host applies it and its committed Spec cannot drift.  The operation keeps its
// identity, so the Controller still tracks the media task it queued. A media
// request that leaves the definition unchanged keeps the lighter vm-media path.
func PoolVMMediaCommand(ctx context.Context, s *Store, cmd protocol.Command) protocol.Command {
	if cmd.Kind != "vm-media" || cmd.VMMedia == nil {
		return cmd
	}
	// A redelivered operation is applied exactly as it was journaled.  The current
	// definition already carries the first attempt's change, so deriving the next revision
	// again would make one operation ID mean two different commands.
	if journaled, ok := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).JournaledCommand(cmd.OperationID); ok {
		if journaled.Kind == "vm-media" && reflect.DeepEqual(journaled, cmd) {
			return journaled
		}
		if journaled.Kind == "vm-update" && journaled.VM != nil && journaled.VM.ID == cmd.VMMedia.VMID && journaled.VM.PoolID == cmd.VMMedia.PoolID && journaled.VM.HostID == cmd.VMMedia.HostID && reflect.DeepEqual(protocol.ApplyVMMedia(*journaled.VM, cmd.VMMedia.Action, cmd.VMMedia.Drive, cmd.VMMedia.MediaID), *journaled.VM) {
			return journaled
		}
		return cmd
	}
	v, _, err := poolSharedVM(ctx, s, cmd)
	if err != nil || !poolVMMediaChangesDefinition(v, *cmd.VMMedia) {
		return cmd
	}
	next := protocol.ApplyVMMedia(v, cmd.VMMedia.Action, cmd.VMMedia.Drive, cmd.VMMedia.MediaID)
	next.Revision, next.StartAfterCreate = v.Revision+1, false
	update := protocol.Command{OperationID: cmd.OperationID, Kind: "vm-update", VM: &next}
	if vmhost.ValidateManagedCommand(update) != nil {
		return cmd
	}
	return update
}

// poolVMDeleteMatchesOwner reports whether a delete or release request names the VM v, this
// host and its pool. poolSharedVM prefers a command's VM definition, so the request's own VM
// identity is compared rather than assumed.
func poolVMDeleteMatchesOwner(s *Store, cmd protocol.Command, v protocol.VMCreateSpec) bool {
	id, pool, host := "", "", ""
	if cmd.Delete != nil {
		id, pool, host = cmd.Delete.VMID, cmd.Delete.PoolID, cmd.Delete.HostID
	} else if cmd.ReleaseRetained != nil {
		id, pool, host = cmd.ReleaseRetained.VMID, cmd.ReleaseRetained.PoolID, cmd.ReleaseRetained.HostID
	}
	return id == v.ID && pool == s.State.PoolID && host == s.State.HostID
}

// AdmitPoolVMExecution must wrap every managed create/start/stop.  Its returned callback
// records only a confirmed local observation; it never frees ownership after an error/timeout.
func AdmitPoolVMExecution(ctx context.Context, s *Store, cmd protocol.Command) (func(protocol.Result, error) error, error) {
	// A host that cannot prove it holds its pool's current authority starts no workload at
	// all and does not even ask its master for a permit. It is decided first, before any
	// storage or runtime observation, so a restricted host refuses in its own words rather
	// than failing for an unrelated reason, and it covers local-storage guests too: the
	// recovery decision is that a stale host blocks local and libvirt autostarts and other
	// workload starts until it has reconciled. Stop, shutdown and force-stop are never
	// blocked — a restricted host must always be able to put a guest down — and local
	// console and recovery access are untouched.
	if !isPoolVMStopOnly(cmd) {
		if err := requirePoolStartPath(s); err != nil {
			return nil, err
		}
	}
	// A drained host starts nothing new, for exactly the same reason and in exactly the
	// same place. Reboots and every reducing power request are deliberately not refused here.
	if poolVMStartsExecution(cmd) {
		if err := poolHostInMaintenance(s, s.State.HostID); err != nil {
			return nil, err
		}
	}
	v, shared, err := poolSharedVM(ctx, s, cmd)
	if err != nil {
		return nil, err
	}
	definition := cmd.Kind == "vm-create" || cmd.Kind == "vm-update"
	ownerAdmission := shared || definition
	if v.ID != "" && cmd.Kind == "vm-power" && cmd.Power != nil {
		switch cmd.Power.Action {
		case "start", "wake", "reboot", "force-reboot":
			ownerAdmission = true
		}
	}
	ownerAdmission = ownerAdmission || (v.ID != "" && cmd.Kind == "vm-media")
	// Every pool VM deletion is durable master state. Keep the check independent
	// of DeleteDisks: retaining or unlinking disks must not discard catalog points.
	// Releasing retained originals deletes files a settling restore may still rely on, so it
	// takes the same owner admission: the master refuses it while an update, restore or
	// snapshot recovery is pending for the VM.
	if (cmd.Kind == "vm-delete" || cmd.Kind == "vm-release-retained") && v.ID != "" {
		if !poolVMDeleteMatchesOwner(s, cmd, v) {
			return nil, errors.New("VM delete does not match the local pool owner")
		}
		if err := poolVMRequestPeer(ctx, s, v.ID, "delete-check", nil, false, &v, nil); err != nil {
			return nil, err
		}
	}
	// Placement ownership covers every VM definition. The shared-storage fence
	// below still covers only operations which could create a second writer. Local
	// execution-increasing and media operations still require owner admission so a
	// pending recovery cannot be bypassed through local-only storage.
	if !ownerAdmission {
		return func(protocol.Result, error) error { return nil }, nil
	}
	stopErr := poolVMStopOnly(s, cmd, v)
	if shared && stopErr == nil {
		// Do not turn loss of quorum into an inability to stop a known local
		// writer.  In particular, do not write a start permit, call an
		// unreachable master, or infer a durable "stopped" transition from a
		// request timeout.  The Engine journals and observes the native request;
		// later authenticated inventory is the only reconciliation path.
		return func(protocol.Result, error) error { return nil }, nil
	} else if shared && isPoolVMStopOnly(cmd) {
		return nil, stopErr
	}
	if shared {
		if _, err := poolVMExecutionContext(ctx, s, v); err != nil {
			return nil, err
		}
	}
	if (shared || definition) && cmd.Kind != "vm-create" {
		if err := PublishPoolVMInventory(ctx, s); err != nil {
			return nil, err
		}
	}
	action := ""
	switch cmd.Kind {
	case "vm-create":
		action = "define"
	case "vm-update":
		action = "update-reserve"
	case "vm-power":
		action = cmd.Power.Action
	case "vm-media":
		// A media change that rewrites the definition must arrive as the vm-update
		// transition PoolVMMediaCommand produces. The remaining operation only
		// reconnects the tray to the ISO the definition already names, and still
		// needs the owner admission every other execution action takes.
		if cmd.VMMedia == nil || poolVMMediaChangesDefinition(v, *cmd.VMMedia) {
			return nil, errors.New("VM media changes require a durable pool definition transition")
		}
		action = "media"
	default:
		return func(protocol.Result, error) error { return nil }, nil
	}
	if action != "define" && action != "update-reserve" && action != "start" && action != "shutdown" && action != "force-stop" && action != "reboot" && action != "force-reboot" && action != "media" {
		return func(protocol.Result, error) error { return nil }, nil
	}
	if cmd.Kind == "vm-power" && (action == "shutdown" || action == "force-stop") {
		action = "stop"
	}
	if action == "start" {
		// The engine wakes a guest asleep in ACPI S3 instead of starting it. The master
		// already records that execution as running, so it is asked for a wake, which
		// needs no permit; a second start permit would claim a second execution.
		asleep, err := vmhost.ManagedVMAsleep(ctx, v.ID)
		if err != nil {
			return nil, errors.New("managed VM runtime observation is unavailable")
		}
		if asleep {
			action = "wake"
		}
	}
	permit := ""
	epoch := uint64(0)
	startAdmitted := false
	if action == "update-reserve" {
		previous, priorErr := poolVMUpdatePrevious(ctx, s, v.ID)
		if cmd.VM == nil || priorErr != nil || vmhost.ValidateManagedCommand(cmd) != nil {
			return nil, errors.New("VM update definition is unavailable or invalid")
		}
		if err := poolVMUpdateRequestPeer(ctx, s, v.ID, action, cmd.OperationID, previous, cmd.VM, nil); err != nil {
			return nil, err
		}
	} else if action == "define" && cmd.Kind == "vm-create" {
		if err := poolVMDefineRequestPeer(ctx, s, *cmd.VM, cmd.OperationID); err != nil {
			return nil, err
		}
	} else if action != "start" {
		if err := poolVMRequestPeer(ctx, s, v.ID, action, nil, cmd.Kind == "vm-create", cmd.VM, nil); err != nil {
			return nil, err
		}
	}
	if cmd.Kind == "vm-create" && v.StartAfterCreate {
		if err := poolVMRequestPeerBound(ctx, s, v.ID, "start", cmd.OperationID, 0, &permit, false, nil, &epoch); err != nil {
			return nil, err
		}
		startAdmitted = true
	} else if action == "start" {
		if err := poolVMRequestPeerBound(ctx, s, v.ID, action, cmd.OperationID, 0, &permit, false, nil, &epoch); err != nil {
			return nil, err
		}
		startAdmitted = true
	}
	if permit != "" && shared {
		if err := vmhost.WritePoolVMStartPermitAdmission(s.dir, s.State.PoolID, s.State.HostID, v.ID, epoch, permit); err != nil {
			return nil, err
		}
	}
	return func(result protocol.Result, executeErr error) error {
		if cmd.Kind == "vm-update" && (executeErr != nil || result.Status != "succeeded") {
			settle, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			evidence, evidenceErr := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).FailedVMUpdateEvidence(settle, v.ID, cmd.OperationID, *cmd.VM)
			if evidenceErr != nil || poolVMUpdateRequestPeer(settle, s, v.ID, "update-abort", cmd.OperationID, nil, nil, &evidence) != nil {
				return errors.New("VM update outcome is pending durable failure reconciliation")
			}
			return nil
		}
		if executeErr != nil || result.Status != "succeeded" {
			if permit != "" {
				_ = vmhost.RemovePoolVMStartPermit(s.dir, v.ID)
			}
			if startAdmitted && executeErr == nil && result.Status == "failed" {
				if cmd.Kind == "vm-create" && v.StartAfterCreate {
					// Creation failure is not evidence about a separately admitted
					// automatic start. Keep its reservation until an exact start result
					// or owner observation can reconcile it.
					return nil
				}
				state, observeErr := vmhost.ManagedVMState(context.Background(), v.ID)
				if observeErr == nil && state == "shut off" {
					if err := poolVMRequestPeerBound(context.Background(), s, v.ID, "stopped", cmd.OperationID, epoch, nil, false, nil, nil); err == nil {
						return nil
					}
				}
				return errors.New("failed VM start remains pending durable reconciliation")
			}
			return nil
		}
		if cmd.Kind == "vm-update" {
			settle, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			evidence, evidenceErr := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).SuccessfulVMUpdateEvidence(settle, v.ID)
			if evidenceErr != nil || poolVMUpdateRequestPeer(settle, s, v.ID, "update-commit", cmd.OperationID, nil, nil, &evidence) != nil {
				return errors.New("local VM update succeeded but pool owner confirmation is pending")
			}
			return nil
		}
		if cmd.Kind == "vm-power" && (cmd.Power.Action == "reboot" || cmd.Power.Action == "force-reboot") {
			// Reboot has no stopped/running acknowledgement: an ambiguous guest
			// reboot must not be translated into a placement transition.
			return nil
		}
		if !ownerAdmission || (cmd.Kind == "vm-create" && !v.StartAfterCreate) {
			return nil
		}
		state, err := vmhost.ManagedVMState(context.Background(), v.ID)
		if err != nil {
			return nil
		}
		if state == "running" {
			if startAdmitted {
				_ = poolVMRequestPeerBound(context.Background(), s, v.ID, "running", cmd.OperationID, epoch, nil, false, nil, nil)
			} else {
				_ = poolVMRequestPeer(context.Background(), s, v.ID, "running", nil, false, nil, nil)
			}
		}
		if state == "shut off" {
			_ = vmhost.RemovePoolVMStartPermit(s.dir, v.ID)
			if startAdmitted {
				if cmd.Kind == "vm-create" && v.StartAfterCreate {
					start, found, startErr := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).CreateStartOutcome(context.Background(), cmd.OperationID)
					if startErr != nil || !found || start.OperationID != cmd.OperationID+":start" || start.Status != "failed" {
						return nil
					}
				}
				_ = poolVMRequestPeerBound(context.Background(), s, v.ID, "stopped", cmd.OperationID, epoch, nil, false, nil, nil)
			} else {
				_ = poolVMRequestPeer(context.Background(), s, v.ID, "stopped", nil, false, nil, nil)
			}
		}
		return nil
	}, nil
}

// poolVMPlacementRefusal is a definite master refusal (HTTP 409) of a VM
// placement, as opposed to a transport or server failure whose outcome is unknown.
type poolVMPlacementRefusal string

func (r poolVMPlacementRefusal) Error() string { return string(r) }

func poolVMDefineRequestPeer(ctx context.Context, s *Store, vm protocol.VMCreateSpec, operationID string) error {
	membership, err := Membership(s)
	if err != nil || operationID == "" {
		return errors.New("VM placement membership or operation is unavailable")
	}
	if membership.MasterHostID == s.State.HostID {
		member := protocol.PoolMember{HostID: s.State.HostID, Generation: membership.Generation, Status: "ready"}
		placement, err := poolVMPlacement(ctx, s, membership, member, protocol.Command{OperationID: operationID, Kind: "vm-create", VM: &vm})
		if err != nil {
			return err
		}
		if err := poolVMPlacementStorageMatches(ctx, s, placement, vm); err != nil {
			return err
		}
		return poolVMLocalPlacementBound(s, vm.ID, "define", operationID, 0, nil, true, &vm, nil, &placement)
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	body, _ := json.Marshal(poolVMRequest{VMID: vm.ID, Action: "define", Create: true, OperationID: operationID, VM: &vm})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+vm.ID, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		message := "pool master refused VM placement: " + placementError(errors.New(string(body)))
		if response.StatusCode == http.StatusConflict {
			return poolVMPlacementRefusal(message)
		}
		return errors.New(message)
	}
	var reply poolVMRequest
	if json.NewDecoder(response.Body).Decode(&reply) != nil || reply.VMID != vm.ID || reply.Action != "define" || reply.OperationID != operationID || !reply.PlacementAccepted || !reply.SpecMatch {
		return errors.New("pool master did not acknowledge VM placement")
	}
	return nil
}

// releaseProvenFailedRestorePlacement removes only the provisional owner that
// this restore created, after the executing host proves no native target exists.
func releaseProvenFailedRestorePlacement(ctx context.Context, s *Store, spec protocol.VMCreateSpec, operationID string) error {
	if !protocol.ValidSnapshotOperationID(operationID) || protocol.ValidateVMSpec(spec) != nil ||
		spec.PoolID != s.State.PoolID || spec.HostID != s.State.HostID {
		return errors.New("restore placement release is invalid")
	}
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	absent, err := engine.RestoreAsNewTargetAbsent(ctx, operationID, spec)
	if err != nil {
		return err
	}
	if !absent {
		return errors.New("restore target absence is unproven")
	}
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		poolVMMu.Lock()
		defer poolVMMu.Unlock()
		if err := requirePoolVMAuthorityWritable(s, "release-restore-placement", false); err != nil {
			return err
		}
		state, err := loadPoolVMs(s)
		if err != nil {
			return err
		}
		return releaseRestorePlacementOwnerLocked(s, &state, spec, operationID)
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	body, _ := json.Marshal(poolVMRequest{VMID: spec.ID, Action: "release-restore-placement", OperationID: operationID, VM: &spec, PlacementAbsent: true})
	work, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+spec.ID, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("pool master refused restore placement release")
	}
	var reply poolVMRequest
	if json.NewDecoder(response.Body).Decode(&reply) != nil || reply.VMID != spec.ID || reply.Action != "release-restore-placement" ||
		reply.OperationID != operationID || !reply.PlacementReleased {
		return errors.New("pool master did not acknowledge restore placement release")
	}
	return nil
}

func releaseRestorePlacementOwnerLocked(s *Store, state *poolVMState, spec protocol.VMCreateSpec, operationID string) error {
	if state == nil || !protocol.ValidSnapshotOperationID(operationID) || protocol.ValidateVMSpec(spec) != nil || spec.PoolID != s.State.PoolID {
		return errors.New("restore placement release is invalid")
	}
	run, err := loadPoolBackupRun(s, operationID)
	if err != nil || (run.Kind != protocol.BackupKindRestoreNew && run.Kind != protocol.BackupKindTestRestore) ||
		run.Status != "pending" || run.NewVMID != spec.ID || run.HostID != spec.HostID || run.PoolID != spec.PoolID {
		return errors.New("restore placement run does not match the owner")
	}
	for _, inventory := range state.Inventory {
		for _, vm := range inventory.VMs {
			if vm.ID == spec.ID {
				return errors.New("restore target has appeared in host inventory")
			}
		}
	}
	record, ok := state.VMs[spec.ID]
	if !ok {
		return nil // exact retry after a lost acknowledgement
	}
	if record.CreateOperationID != operationID || !poolVMRecordMatchesSpec(record, spec) || record.State != "defined" ||
		record.Epoch != 1 || record.PendingUpdate != nil || record.PendingStartOperationID != "" || poolVMHasSnapshotState(record) {
		return errors.New("restore placement owner has changed")
	}
	delete(state.VMs, spec.ID)
	return savePoolVMs(s, *state)
}

func poolVMRequestPeer(ctx context.Context, s *Store, id, action string, permit *string, create bool, vm *protocol.VMCreateSpec, epoch *uint64) error {
	return poolVMRequestPeerBound(ctx, s, id, action, "", 0, permit, create, vm, epoch)
}

func poolVMRequestPeerBound(ctx context.Context, s *Store, id, action, operationID string, expectedEpoch uint64, permit *string, create bool, vm *protocol.VMCreateSpec, epoch *uint64) error {
	if create {
		return errors.New("VM creation requires exact operation placement admission")
	}
	if action == "start" && !validPoolVMUpdateOperation(operationID) {
		return errors.New("VM start requires an exact owner operation ID")
	}
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		localPermit, localEpoch := "", uint64(0)
		if err := poolVMLocalBound(s, id, action, operationID, expectedEpoch, &localPermit, create, vm, &localEpoch); err != nil {
			return err
		}
		if action == "start" && (localPermit == "" || localEpoch == 0) {
			return errors.New("pool master did not issue an exact VM start admission")
		}
		if permit != nil {
			*permit = localPermit
		}
		if epoch != nil {
			*epoch = localEpoch
		}
		return nil
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
	body, _ := json.Marshal(poolVMRequest{VMID: id, Action: action, OperationID: operationID, Epoch: expectedEpoch, Create: create, VM: vm})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+id, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The master's own sentence, not a bare refusal: an operator whose start was
		// refused has to be told which check refused it and on which host.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if reason := placementError(errors.New(string(detail))); reason != "" {
			return errors.New("pool master refused VM execution: " + reason)
		}
		return errors.New("pool master refused VM execution")
	}
	var result poolVMRequest
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return errors.New("invalid pool VM execution response")
	}
	if action == "start" && (result.VMID != id || result.Action != action || result.OperationID != operationID || result.Permit == "" || result.Epoch == 0) {
		return errors.New("pool master did not issue an exact VM start admission")
	}
	if (action == "running" || action == "stopped") && operationID != "" && (result.VMID != id || result.Action != action || result.OperationID != operationID || result.Epoch != expectedEpoch) {
		return errors.New("pool master did not acknowledge the exact VM start outcome")
	}
	if action == "delete-check" && (result.VMID != id || result.Action != action || !result.SpecMatch) {
		return errors.New("pool master did not authorize the exact VM delete")
	}
	if action == "define" && !create && vm != nil && !result.SpecMatch {
		return errors.New("pool master did not confirm the durable VM definition")
	}
	if permit != nil {
		*permit = result.Permit
	}
	if epoch != nil {
		*epoch = result.Epoch
	}
	return nil
}

// poolVMUpdateRequestPeer is deliberately separate from execution permits: a
// definition revision has a two-step master record (reserve then journal-backed
// commit), while power permits are one-use runtime admissions.
func poolVMUpdateRequestPeer(ctx context.Context, s *Store, id, action, operationID string, previous, next *protocol.VMCreateSpec, evidence *vmhost.VMUpdateEvidence) error {
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		if action == "update-reserve" {
			if next == nil {
				return errors.New("VM placement definition is unavailable")
			}
			member := protocol.PoolMember{HostID: s.State.HostID, Generation: membership.Generation, Status: "ready"}
			placement, preflightErr := poolVMPlacement(ctx, s, membership, member, protocol.Command{OperationID: operationID, Kind: "vm-update", VM: next})
			if preflightErr != nil {
				return preflightErr
			}
			if err := poolVMPlacementStorageMatches(ctx, s, placement, *next); err != nil {
				return err
			}
			return poolVMLocalUpdatePlacement(s, id, action, operationID, previous, next, evidence, &placement)
		}
		return poolVMLocalUpdate(s, id, action, operationID, previous, next, evidence)
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
	body, _ := json.Marshal(poolVMRequest{VMID: id, Action: action, OperationID: operationID, Previous: previous, VM: next, Update: evidence})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+id, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return errors.New("pool master refused VM update transition: " + placementError(errors.New(string(body))))
	}
	var result poolVMRequest
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return errors.New("invalid pool VM update transition response")
	}
	if result.VMID != id || result.Action != action || result.OperationID != operationID || !result.UpdateAccepted || (action == "update-reserve" && !result.PlacementAccepted) {
		return errors.New("pool master did not confirm the durable VM update transition")
	}
	wantRevision := 0
	if next != nil {
		wantRevision = next.Revision
	} else if evidence != nil {
		wantRevision = evidence.Current.Revision
	}
	if wantRevision < 1 || result.UpdateRevision != wantRevision {
		return errors.New("pool master returned a mismatched durable VM update transition")
	}
	return nil
}

// placementMembershipMatches validates the final admission snapshot while the
// caller holds poolPeerMu through the durable VM record write.
func placementMembershipMatches(s *Store, membership protocol.PoolMembership, generation uint64, selected protocol.PoolMember) bool {
	if membership.Valid() != nil || membership.PoolID != s.State.PoolID || membership.MasterHostID != s.State.HostID || membership.Generation != generation {
		return false
	}
	for _, member := range membership.Members {
		if member.HostID == selected.HostID && member.Status == "ready" && member.Generation == generation {
			return selected.CertificateSHA256 == "" || member.CertificateSHA256 == selected.CertificateSHA256
		}
	}
	return false
}

func poolPeerMemberCurrent(membership protocol.PoolMembership, selected protocol.PoolMember) bool {
	if membership.Valid() != nil || membership.MasterHostID == "" || selected.HostID == "" {
		return false
	}
	for _, member := range membership.Members {
		if member.HostID == selected.HostID && member.Status == "ready" &&
			member.CertificateSHA256 == selected.CertificateSHA256 && member.Generation <= membership.Generation {
			return true
		}
	}
	return false
}

func placementReservationMatches(record poolVMRecord, operationID string, next *protocol.VMCreateSpec) bool {
	return next != nil && record.PendingUpdate != nil && record.PendingUpdate.OperationID == operationID && reflect.DeepEqual(record.PendingUpdate.Next, *next)
}

func poolVMLocalUpdate(s *Store, id, action, operationID string, previous, next *protocol.VMCreateSpec, evidence *vmhost.VMUpdateEvidence) error {
	return poolVMLocalUpdatePlacement(s, id, action, operationID, previous, next, evidence, nil)
}

func poolVMLocalUpdatePlacement(s *Store, id, action, operationID string, previous, next *protocol.VMCreateSpec, evidence *vmhost.VMUpdateEvidence, placement *poolPlacementReply) error {
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	if err := requirePoolVMAuthorityWritable(s, action, false); err != nil {
		return err
	}
	var membership protocol.PoolMembership
	if placement != nil {
		poolPeerMu.Lock()
		defer poolPeerMu.Unlock()
		peer, err := loadPoolPeer(s)
		membership = peer.Membership
		if err != nil || !placementMembershipMatches(s, membership, placement.Generation, protocol.PoolMember{HostID: s.State.HostID}) {
			return errors.New("pool membership changed before durable VM placement")
		}
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return err
	}
	record, ok := state.VMs[id]
	if placement != nil && placement.Replay && (!ok || !placementReservationMatches(record, operationID, next)) {
		return errors.New("VM placement replay has no matching durable master reservation")
	}
	if !ok && action != "update-reserve" {
		return errors.New("VM has no durable pool execution record")
	}
	switch action {
	case "update-reserve":
		valid := next != nil && vmhost.ValidateManagedCommand(protocol.Command{OperationID: operationID, Kind: "vm-update", VM: next}) == nil
		if valid && ok {
			valid = previous != nil && poolVMRecordMatchesSpec(record, *previous) && reservePoolVMUpdate(&record, s.State.HostID, operationID, *next) == nil
		} else if valid {
			var membershipErr error
			if placement == nil {
				membership, membershipErr = Membership(s)
			}
			valid = membershipErr == nil && reserveOrSeedPoolVMUpdate(&state, membership, &record, ok, s.State.HostID, operationID, previous, next) == nil
		}
		if !valid {
			return errors.New("VM update is not eligible for the durable pool owner record")
		}
	case "update-commit":
		if evidence == nil || commitPoolVMUpdate(&record, s.State.HostID, *evidence) != nil {
			return errors.New("VM update journal evidence does not match the durable pool owner record")
		}
	case "update-abort":
		if evidence == nil || abortPoolVMUpdate(&record, s.State.HostID, *evidence) != nil {
			return errors.New("VM update failure evidence does not match the durable pool owner record")
		}
	default:
		return errors.New("unsupported VM update transition")
	}
	state.VMs[id] = record
	return savePoolVMs(s, state)
}
func poolVMLocal(s *Store, id, action string, permit *string, create bool, vm *protocol.VMCreateSpec, epoch *uint64) error {
	return poolVMLocalBound(s, id, action, "", 0, permit, create, vm, epoch)
}

func poolVMLocalBound(s *Store, id, action, operationID string, expectedEpoch uint64, permit *string, create bool, vm *protocol.VMCreateSpec, epoch *uint64) error {
	return poolVMLocalPlacementBound(s, id, action, operationID, expectedEpoch, permit, create, vm, epoch, nil)
}

func poolVMLocalPlacement(s *Store, id, action string, permit *string, create bool, vm *protocol.VMCreateSpec, epoch *uint64, placement *poolPlacementReply) error {
	return poolVMLocalPlacementBound(s, id, action, "", 0, permit, create, vm, epoch, placement)
}

func poolVMLocalPlacementBound(s *Store, id, action, operationID string, expectedEpoch uint64, permit *string, create bool, vm *protocol.VMCreateSpec, epoch *uint64, placement *poolPlacementReply) error {
	// The master's own guests take this path instead of the peer handler. Read the drain
	// flag before any lock: a placement already carries its own refusal from
	// poolVMPlacement, and reading membership here would otherwise take the peer lock in
	// the wrong order behind the VM lock a placement already holds. A wake is a start an
	// operator asked for; a reboot of a guest already running here is not.
	if action == "start" || action == "wake" {
		if err := poolHostInMaintenance(s, s.State.HostID); err != nil {
			return err
		}
	}
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	if err := requirePoolVMAuthorityWritable(s, action, create); err != nil {
		return err
	}
	if placement != nil {
		poolPeerMu.Lock()
		defer poolPeerMu.Unlock()
		peer, err := loadPoolPeer(s)
		if err != nil || !placementMembershipMatches(s, peer.Membership, placement.Generation, protocol.PoolMember{HostID: s.State.HostID}) {
			return errors.New("pool membership changed before durable VM placement")
		}
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return err
	}
	r, ok := state.VMs[id]
	if placement != nil && placement.Replay && !ok {
		return errors.New("VM placement replay has no durable master record")
	}
	switch action {
	case "define":
		if ok && r.PendingRecovery != nil {
			return errors.New("VM snapshot recovery is pending reconciliation")
		}
		if ok && (r.PendingUpdate != nil || r.HostID != s.State.HostID) {
			return errors.New("VM is owned by another host")
		}
		if ok && vm != nil && !poolVMRecordMatchesSpec(r, *vm) {
			return errors.New("VM definition does not match the durable pool owner record")
		}
		if !ok && !create {
			return errors.New("VM has no durable pool execution record")
		}
		if !ok {
			if vm == nil || vm.ID != id || vm.PoolID != s.State.PoolID || vm.HostID != s.State.HostID || protocol.ValidateVMSpec(*vm) != nil {
				return errors.New("invalid VM definition")
			}
			r = poolVMRecord{PoolID: s.State.PoolID, VMID: id, HostID: s.State.HostID, CreateOperationID: operationID, State: "defined", Autostart: vm.Autostart, Epoch: 1, Spec: *vm}
		}
	case "delete-check":
		if !ok || vm == nil || r.PendingUpdate != nil || r.PendingRecovery != nil || r.PendingStartOperationID != "" ||
			r.PoolID != s.State.PoolID || r.HostID != s.State.HostID || !poolVMRecordMatchesSpec(r, *vm) {
			return errors.New("VM delete does not match the durable pool owner record")
		}
		if err := validatePoolVMSnapshotCatalog(r); err != nil {
			return errors.New("VM snapshot catalog is invalid")
		}
		if len(r.Snapshots) != 0 {
			return errors.New("VM has retained snapshots; delete its snapshots before deleting the VM")
		}
	case "start":
		if !ok || !validPoolVMUpdateOperation(operationID) || r.PendingUpdate != nil || r.PendingRecovery != nil || r.PendingStartOperationID != "" || r.HostID != s.State.HostID || (r.State != "defined" && r.State != "stopped") {
			return errors.New("VM execution is not eligible to start")
		}
		r.State, r.Epoch = "starting", r.Epoch+1
		r.PendingStartOperationID = operationID
		if permit != nil {
			*permit, err = randomPermit()
			if err != nil {
				return err
			}
		}
	case "running":
		if !ok || r.PendingUpdate != nil || r.PendingRecovery != nil || r.HostID != s.State.HostID || r.State != "starting" || r.PendingStartOperationID == "" || operationID != r.PendingStartOperationID || expectedEpoch == 0 || expectedEpoch != r.Epoch {
			return errors.New("VM execution transition refused")
		}
		r.State = "running"
		r.PendingStartOperationID = ""
	case "stop":
		if !ok || r.PendingUpdate != nil || r.HostID != s.State.HostID {
			return errors.New("VM execution owner mismatch")
		}
	case "stopped":
		if !ok || r.PendingUpdate != nil || r.HostID != s.State.HostID {
			return errors.New("VM execution owner mismatch")
		}
		if r.State == "starting" {
			if r.PendingStartOperationID == "" || operationID != r.PendingStartOperationID || expectedEpoch == 0 || expectedEpoch != r.Epoch || r.PendingRecovery != nil {
				return errors.New("VM start outcome does not match the pending owner operation")
			}
		} else if operationID != "" || expectedEpoch != 0 {
			return errors.New("stale VM start outcome")
		}
		r.State = "stopped"
		r.PendingStartOperationID = ""
	case "reboot", "force-reboot", "wake", "media":
		if !ok || r.PendingUpdate != nil || r.PendingRecovery != nil || r.PendingStartOperationID != "" || r.HostID != s.State.HostID || (action != "media" && r.State != "running") {
			return errors.New("VM execution is not eligible for this action")
		}
	default:
		return errors.New("unsupported VM execution action")
	}
	state.VMs[id] = r
	if err := savePoolVMs(s, state); err != nil {
		return err
	}
	if epoch != nil {
		*epoch = r.Epoch
	}
	return nil
}

// ReconcilePoolVMAutostart is called from the serialized managed worker.  It starts only the
// already-recorded owner and only after a fresh master permit; it is not HA or failover. It
// returns the reason the pass did nothing, or "" when it ran.
func ReconcilePoolVMAutostart(ctx context.Context, s *Store) string {
	// A drained host does not autostart pool guests on boot. Say so once for the pass: the
	// master would refuse each start anyway, and a silent skip reads like a failed start.
	// The operator-visible reason is the maintenance flag this host reports in inventory.
	drained := poolHostInMaintenance(s, s.State.HostID)
	// A restricted host autostarts no pool guest: this pass runs at boot, which is exactly
	// when a host that was fenced and promoted away would otherwise start a second copy of
	// a guest that is already running on the host that took over.
	restricted := requirePoolStartPath(s)
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	managed, checks := engine.Applied(ctx)
	// Guests an update shut down for a reboot return with the autostart ones, through the
	// same gates below. The record only counts on a boot after the one that wrote it.
	restart := pendingUpdateRestart(s)
	// libvirt starts enabled domains before this agent can confirm pool authority. Keep
	// native autostart disabled for every managed VM; the durable Core Autostart setting
	// below is the boot intent, and this pass starts it only after current master admission.
	//
	// The flag alone is not enough to bring a local-storage guest back, because libvirt's
	// one attempt happens before this host has confirmed its pool authority and the startup
	// hook refuses it then. The loop below is what actually starts those guests, once the
	// two gates above have passed.
	suppressionFailed := make(map[string]bool)
	for _, vm := range managed.VMs {
		if err := engine.SetManagedVMAutostart(ctx, vm); err != nil {
			suppressionFailed[vm.ID] = true
			log.Print("libvirt autostart could not be reconciled for a managed VM; inspect this host")
		}
	}
	if drained != nil {
		log.Print("pool VM autostart skipped: this host is in maintenance mode")
		return "pool VM autostart skipped: this host is in maintenance mode"
	}
	if restricted != nil {
		log.Print("pool VM autostart skipped: this host is restricted until its pool authority is reconciled")
		return "pool VM autostart skipped: this host is restricted until its pool authority is reconciled"
	}
	if !managedVMChecksReady(checks) {
		return "pool VM autostart skipped: managed VM checks are not ready"
	}
	for _, vm := range managed.VMs {
		if !(vm.Autostart || restart[vm.ID]) || suppressionFailed[vm.ID] {
			continue
		}
		shared := poolVMOnSharedStorage(managed, vm)
		// Autostart is an on-boot intent, not a keepalive policy. Claim this boot
		// before native observation or master admission, so an agent restart after
		// a manual stop cannot start the VM again.
		claimed, err := ClaimPoolVMAutostartAttempt(s.dir, vm.ID)
		if err != nil || !claimed {
			continue
		}
		state, err := vmhost.ManagedVMState(ctx, vm.ID)
		if err != nil || state != "shut off" {
			continue
		}
		_ = vmhost.RemovePoolVMStartPermit(s.dir, vm.ID)
		// Confirm the exact master-owned definition before either local or shared
		// autostart can consume a start permit.
		if poolVMRequestPeer(ctx, s, vm.ID, "define", nil, false, &vm, nil) != nil {
			continue
		}
		if poolVMRequestPeer(ctx, s, vm.ID, "stopped", nil, false, nil, nil) != nil {
			continue
		}
		permit := ""
		epoch := uint64(0)
		op, err := randomPermit()
		if err != nil {
			continue
		}
		executionCtx := ctx
		if shared {
			executionCtx, err = poolVMExecutionContext(ctx, s, vm)
			if err != nil {
				continue
			}
		}
		if poolVMRequestPeerBound(ctx, s, vm.ID, "start", op, 0, &permit, false, nil, &epoch) != nil {
			continue
		}
		if shared && vmhost.WritePoolVMStartPermitAdmission(s.dir, s.State.PoolID, s.State.HostID, vm.ID, epoch, permit) != nil {
			continue
		}
		result, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Execute(executionCtx, protocol.Command{OperationID: op, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "start"}}, nil)
		if err != nil || result.Status != "succeeded" {
			_ = vmhost.RemovePoolVMStartPermit(s.dir, vm.ID)
			if err == nil && result.Status == "failed" {
				if state, stateErr := vmhost.ManagedVMState(ctx, vm.ID); stateErr == nil && state == "shut off" {
					_ = poolVMRequestPeerBound(ctx, s, vm.ID, "stopped", op, epoch, nil, false, nil, nil)
				}
			}
			continue
		}
		if observed, err := vmhost.ManagedVMState(ctx, vm.ID); err == nil && observed == "running" {
			_ = poolVMRequestPeerBound(ctx, s, vm.ID, "running", op, epoch, nil, false, nil, nil)
		} else if err == nil && observed == "shut off" {
			_ = poolVMRequestPeerBound(ctx, s, vm.ID, "stopped", op, epoch, nil, false, nil, nil)
		}
	}
	// Every recorded guest has now had its one attempt this boot, started or refused.
	if restart != nil {
		finishUpdateRestart(s)
	}
	return ""
}

// poolVMOnSharedStorage answers whether any of this VM's disks sit on a repository the pool
// shares. Shared guests additionally need the repository execution context; every guest still
// receives the master's owner admission before autostart.
func poolVMOnSharedStorage(managed *protocol.ManagedState, vm protocol.VMCreateSpec) bool {
	for _, disk := range vm.Disks {
		for _, storage := range managed.Storage {
			if disk.StorageID == storage.ID && isPoolSharedStorage(storage) {
				return true
			}
		}
	}
	return false
}
