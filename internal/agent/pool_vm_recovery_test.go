// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestPoolVMRecoveryReservationIsExactAndReplayable(t *testing.T) {
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: "host-a", Epoch: 7, State: "stopped", Spec: spec}
	reservation, err := poolVMRecoveryReservationFor(record, "operation-1", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		t.Fatal(err)
	}
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		t.Fatalf("exact operation replay refused: %v", err)
	}
	changed := reservation
	changed.Kind = "snapshot-revert"
	if err := reservePoolVMRecovery(&record, changed); err == nil {
		t.Fatal("different intent reused a pending operation ID")
	}
	changed = reservation
	changed.Epoch++
	if err := reservePoolVMRecovery(&record, changed); err == nil {
		t.Fatal("stale owner epoch was admitted")
	}
	changed = reservation
	changed.DiskIdentitySHA256 = strings.Repeat("b", 64)
	if err := reservePoolVMRecovery(&record, changed); err == nil {
		t.Fatal("different disk identity was admitted")
	}
	if record.PendingRecovery == nil || *record.PendingRecovery != reservation {
		t.Fatalf("failed replays changed the durable pending intent: %#v", record.PendingRecovery)
	}

	next := spec
	next.Revision++
	if err := reservePoolVMUpdate(&record, "host-a", "update-1", next); err == nil {
		t.Fatal("definition update crossed a pending snapshot reservation")
	}
}

func TestReservePoolVMSnapshotPersistsExactCatalogIntentAndBlocksStorageMutation(t *testing.T) {
	const vmID = "11111111-1111-4111-8111-111111111111"
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec(vmID, "host-a")
	if err := poolVMLocal(s, spec.ID, "define", nil, true, &spec, nil); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 9, 25, 12, 30, 0, 0, time.UTC)
	command := protocol.Command{
		OperationID: "snapshot_op-1", Kind: "snapshot-create", VM: &spec,
		Snapshot: &protocol.VMSnapshotRequest{Name: "before-upgrade", Description: "pre-upgrade"},
	}
	intent := strings.Repeat("c", 64)
	reservation, err := ReservePoolVMSnapshot(context.Background(), s, command, 1, intent, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.IntentSHA256 != intent || reservation.SpecRevision != spec.Revision || reservation.VMID != vmID {
		t.Fatalf("reservation lost exact request binding: %#v", reservation)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[vmID]
	if record.PendingRecovery == nil || *record.PendingRecovery != reservation || record.PendingSnapshot == nil ||
		record.PendingSnapshot.ID != command.OperationID || record.PendingSnapshot.Name != command.Snapshot.Name || !record.PendingSnapshot.CreatedAt.Equal(createdAt) {
		t.Fatalf("master did not persist exact pending snapshot intent: %#v", record)
	}
	wantDependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(spec)
	if err != nil || !reflect.DeepEqual(record.PendingSnapshot.Dependencies, &wantDependencies) {
		t.Fatalf("master did not persist exact snapshot resource dependencies: %#v (%v)", record.PendingSnapshot.Dependencies, err)
	}
	listed, err := ListPoolVMSnapshots(context.Background(), s, vmID)
	if err != nil || len(listed) != 0 {
		t.Fatalf("unsettled snapshot leaked into catalog: %#v (%v)", listed, err)
	}

	// Once a repository mutation is durable, a new snapshot reservation must refuse
	// before changing the VM record. Both paths use VM → storage lock order.
	if err := settlePoolVMRecovery(&record, "host-a", protocol.PoolVMRecoveryEvidence{
		Reservation: reservation, Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
	state.VMs[vmID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	storage := poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Reservations: map[string]map[string]string{}, Mutations: map[string]string{"repo-1": "storage-update-1"}}
	if err := savePoolStorage(s, storage); err != nil {
		t.Fatal(err)
	}
	command.OperationID = "snapshot_op-2"
	if _, err := ReservePoolVMSnapshot(context.Background(), s, command, 2, strings.Repeat("d", 64), createdAt.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "mutation pending") {
		t.Fatalf("snapshot admission crossed a pending storage mutation: %v", err)
	}
	state, err = loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if state.VMs[vmID].PendingRecovery != nil {
		t.Fatal("refused snapshot admission persisted a reservation")
	}
}

func TestSnapshotRevertReservationAndSettlementAreMasterDerivedAndReplayable(t *testing.T) {
	const vmID = "11111111-1111-4111-8111-111111111111"
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	captured := *poolVMTestSpec(vmID, "host-a")
	captured.Name = "captured name"
	captured.Description = "captured description"
	captured.Tags = []string{"captured"}
	captured.Autostart = true
	captured.CPU.Count = 2
	captured.Memory.SizeMiB = 1024
	captured.Disks = append([]protocol.VMDisk(nil), captured.Disks...)
	current := captured
	current.Disks = append([]protocol.VMDisk(nil), current.Disks...)
	current.Name = "operator current name"
	current.Description = "operator current description"
	current.Tags = []string{"current", "operator"}
	current.Autostart = false
	current.CPU.Count = 4
	current.Memory.SizeMiB = 2048
	current.Disks[0].StorageID = "repo-current"
	current.Revision = captured.Revision + 1
	if err := poolVMLocal(s, current.ID, "define", nil, true, &current, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[vmID]
	record.Spec, record.Autostart, record.State = current, current.Autostart, "stopped"
	capturedSHA, capturedDiskSHA, err := poolVMRecoveryDigests(captured)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(captured)
	if err != nil {
		t.Fatal(err)
	}
	target := protocol.PoolVMSnapshot{
		ID: "snapshot-point-a", VMID: vmID, Name: "renamed point", Description: "renamed later",
		CreatedAt: time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC), SpecRevision: captured.Revision,
		SpecSHA256: capturedSHA, DiskIdentitySHA256: capturedDiskSHA, Dependencies: &dependencies,
	}
	wrongRevision := target
	wrongRevision.SpecRevision++
	if _, _, err := poolVMRevertReservationFor(record, "revert-operation-1", strings.Repeat("a", 64), 10, wrongRevision, captured); err == nil {
		t.Fatal("snapshot descriptor revision was accepted despite matching captured spec hashes")
	}
	record.Snapshots = []protocol.PoolVMSnapshot{target}
	state.VMs[vmID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{
		OperationID: "revert-operation-1", Kind: "snapshot-revert", VM: &current,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: target.ID, Target: &target},
	}
	request := poolVMRequest{
		VMID: vmID, Action: "snapshot-reserve", RecoveryKind: "snapshot-revert", OperationID: command.OperationID,
		VM: &current, Snapshot: command.Snapshot, CapturedSpec: &captured, IntentSHA256: strings.Repeat("a", 64), OwnerOperationOrder: 10,
	}
	for _, storageID := range []string{"repo-1", "repo-current"} {
		storage := poolStorageState{
			Storage: map[string]protocol.StorageRegistration{}, Reservations: map[string]map[string]string{},
			Mutations: map[string]string{storageID: "storage-update-pending"},
		}
		if err := savePoolStorage(s, storage); err != nil {
			t.Fatal(err)
		}
		if got, err := loadPoolStorage(s); err != nil || got.Mutations[storageID] == "" {
			t.Fatalf("test mutation did not persist for %s: %#v (%v)", storageID, got.Mutations, err)
		}
		if _, err := reservePoolVMSnapshotLocked(s, "host-a", request); err == nil || !strings.Contains(err.Error(), "storage source has a mutation pending") {
			t.Fatalf("revert crossed a pending mutation for source %s (current=%#v captured=%#v): %v", storageID, current.Disks, captured.Disks, err)
		}
	}
	if err := savePoolStorage(s, poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Reservations: map[string]map[string]string{}, Mutations: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	runningRecord := record
	runningRecord.State = "running"
	state.VMs[vmID] = runningRecord
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	if _, err := reservePoolVMSnapshotLocked(s, "host-a", request); err == nil || !strings.Contains(err.Error(), "not eligible") {
		t.Fatalf("revert accepted a running VM: %v", err)
	}
	state.VMs[vmID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	ack, err := reservePoolVMSnapshotLocked(s, "host-a", request)
	if err != nil {
		t.Fatal(err)
	}
	reservation := *ack.SnapshotReservation
	if !validPoolVMSnapshotReservationAck(ack, request, "pool-1", "host-a") {
		t.Fatal("master revert reservation did not echo the exact target, captured spec, and expected result")
	}
	changedAck := ack
	changedReservation := reservation
	changedReservation.TargetSnapshotSHA256 = strings.Repeat("0", 64)
	changedAck.SnapshotReservation = &changedReservation
	if validPoolVMSnapshotReservationAck(changedAck, request, "pool-1", "host-a") {
		t.Fatal("changed target descriptor was accepted in the reservation acknowledgement")
	}
	expected, err := protocol.PoolVMSnapshotRevertSpec(captured, current)
	if err != nil {
		t.Fatal(err)
	}
	state, err = loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	reserved := state.VMs[vmID]
	if reserved.PendingRecovery == nil || *reserved.PendingRecovery != reservation {
		t.Fatalf("master did not persist the exact revert reservation: %#v", reserved.PendingRecovery)
	}
	if reserved.PendingRecoveryResultSpec == nil || !reflect.DeepEqual(*reserved.PendingRecoveryResultSpec, expected) {
		t.Fatalf("master derived result mismatch: got %#v, want %#v", reserved.PendingRecoveryResultSpec, expected)
	}
	if !reflect.DeepEqual(reserved.Spec, current) || len(reserved.Snapshots) != 1 || !reflect.DeepEqual(reserved.Snapshots[0], target) {
		t.Fatalf("master changed the VM or target catalog before effects: %#v", reserved)
	}
	if reservation.TargetSnapshotID != target.ID || reservation.TargetSnapshotSHA256 == "" || reservation.ResultSpecSHA256 == "" {
		t.Fatalf("reservation omitted target/result binding: %#v", reservation)
	}
	if replay, err := reservePoolVMSnapshotLocked(s, "host-a", request); err != nil || replay.SnapshotReservation == nil || *replay.SnapshotReservation != reservation {
		t.Fatalf("exact reservation replay failed: %#v (%v)", replay, err)
	}
	changedTarget := target
	changedTarget.Description = "stale catalog metadata"
	changed := request
	changed.Snapshot = &protocol.VMSnapshotRequest{SnapshotID: target.ID, Target: &changedTarget}
	if _, err := reservePoolVMSnapshotLocked(s, "host-a", changed); err == nil {
		t.Fatal("revert reservation accepted a stale target descriptor")
	}
	changed = request
	changedCaptured := captured
	changedCaptured.CPU.Count++
	changed.CapturedSpec = &changedCaptured
	if _, err := reservePoolVMSnapshotLocked(s, "host-a", changed); err == nil {
		t.Fatal("revert reservation accepted a captured spec with a different descriptor hash")
	}
	changed = request
	changed.OperationID = "different-operation"
	changed.Snapshot = &protocol.VMSnapshotRequest{SnapshotID: target.ID, Target: &target}
	if _, err := reservePoolVMSnapshotLocked(s, "host-a", changed); err == nil {
		t.Fatal("different operation was admitted while another revert was pending")
	}

	failed := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("b", 64)}
	if err := settlePoolVMRecovery(&reserved, "host-a", failed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reserved.Spec, current) || reserved.PendingRecovery != nil || reserved.PendingRecoveryResultSpec != nil || len(reserved.Snapshots) != 1 {
		t.Fatalf("failed-before-effects changed the VM or target catalog: %#v", reserved)
	}
	state.VMs[vmID] = reserved
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}

	// A new exact operation can now reserve and complete. Its result preserves
	// current identity/presentation fields while restoring captured execution data.
	request.OperationID = "revert-operation-2"
	request.OwnerOperationOrder = 11
	ack, err = reservePoolVMSnapshotLocked(s, "host-a", request)
	if err != nil {
		t.Fatal(err)
	}
	reservation = *ack.SnapshotReservation
	state, err = loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record = state.VMs[vmID]
	result := *request.VM
	result, err = protocol.PoolVMSnapshotRevertSpec(captured, *request.VM)
	if err != nil {
		t.Fatal(err)
	}
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("c", 64), ResultSpec: &result}
	encodedReplica, err := json.Marshal(state.VMs)
	if err != nil {
		t.Fatal(err)
	}
	var replicaVMs map[string]poolVMRecord
	if err := json.Unmarshal(encodedReplica, &replicaVMs); err != nil || replicaVMs[vmID].PendingRecoveryResultSpec == nil ||
		!reflect.DeepEqual(*replicaVMs[vmID].PendingRecoveryResultSpec, result) {
		t.Fatalf("replicated pending revert lost the master's expected result: %#v (%v)", replicaVMs[vmID], err)
	}
	promoted := poolVMState{VMs: replicaVMs}
	if _, err := settlePoolVMRecoveryInventory(&promoted, "host-a", map[string]protocol.PoolVMRecoveryEvidence{reservation.OperationID: evidence}); err != nil {
		t.Fatalf("promoted master could not settle exact revert evidence: %v", err)
	}
	if !reflect.DeepEqual(promoted.VMs[vmID].Spec, result) || promoted.VMs[vmID].PendingRecovery != nil {
		t.Fatalf("promoted master did not apply the reserved revert result: %#v", promoted.VMs[vmID])
	}
	if err := settlePoolVMRecovery(&record, "host-a", evidence); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.Spec, result) || record.Autostart != current.Autostart || record.Spec.Name != current.Name ||
		!reflect.DeepEqual(record.Spec.Tags, current.Tags) || record.Spec.Revision != current.Revision+1 ||
		len(record.Snapshots) != 1 || !reflect.DeepEqual(record.Snapshots[0], target) || record.PendingRecovery != nil || record.PendingRecoveryResultSpec != nil {
		t.Fatalf("completed revert did not atomically update only the master VM definition: %#v", record)
	}
	if err := settlePoolVMRecovery(&record, "host-a", evidence); err != nil {
		t.Fatalf("exact lost-ack settlement replay failed: %v", err)
	}
	changedEvidence := evidence
	changedResult := result
	changedResult.Name = "forged identity"
	changedEvidence.ResultSpec = &changedResult
	if err := settlePoolVMRecovery(&record, "host-a", changedEvidence); err == nil {
		t.Fatal("changed result reused an already settled revert operation")
	}
}

func TestSnapshotRevertReservationAcceptsDefinedNeverStartedVM(t *testing.T) {
	const vmID = "11111111-1111-4111-8111-111111111111"
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec(vmID, "host-a")
	if err := poolVMLocal(s, spec.ID, "define", nil, true, &spec, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	specSHA, diskSHA, err := poolVMRecoveryDigests(spec)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	target := protocol.PoolVMSnapshot{
		ID: "snapshot-point-defined", VMID: vmID, Name: "defined point",
		CreatedAt: time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC), SpecRevision: spec.Revision,
		SpecSHA256: specSHA, DiskIdentitySHA256: diskSHA, Dependencies: &dependencies,
	}
	record := state.VMs[vmID]
	record.Spec, record.Autostart, record.State = spec, spec.Autostart, "defined"
	record.Snapshots = []protocol.PoolVMSnapshot{target}
	state.VMs[vmID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	request := poolVMRequest{
		VMID: vmID, Action: "snapshot-reserve", RecoveryKind: "snapshot-revert", OperationID: "revert-defined-1",
		VM: &spec, Snapshot: &protocol.VMSnapshotRequest{SnapshotID: target.ID, Target: &target},
		CapturedSpec: &spec, IntentSHA256: strings.Repeat("b", 64), OwnerOperationOrder: 3,
	}
	if _, err := reservePoolVMSnapshotLocked(s, "host-a", request); err != nil {
		t.Fatalf("defined never-started VM must be eligible for stopped snapshot revert: %v", err)
	}
}

func TestPoolVMInventorySettlesCompletedRevertFromOwnerEvidence(t *testing.T) {
	const vmID = "11111111-1111-4111-8111-111111111111"
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	captured := *poolVMTestSpec(vmID, "host-a")
	captured.CPU.Count = 2
	current := *poolVMTestSpec(vmID, "host-a")
	current.Revision = captured.Revision + 1
	current.CPU.Count = 4
	if err := poolVMLocal(s, current.ID, "define", nil, true, &current, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[vmID]
	record.Spec, record.State = current, "stopped"
	capturedSHA, capturedDiskSHA, err := poolVMRecoveryDigests(captured)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(captured)
	if err != nil {
		t.Fatal(err)
	}
	target := protocol.PoolVMSnapshot{
		ID: "snapshot-point-a", VMID: vmID, Name: "target", CreatedAt: time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC),
		SpecRevision: captured.Revision, SpecSHA256: capturedSHA, DiskIdentitySHA256: capturedDiskSHA, Dependencies: &dependencies,
	}
	reservation, result, err := poolVMRevertReservationFor(record, "revert-operation-1", strings.Repeat("a", 64), 10, target, captured)
	if err != nil {
		t.Fatal(err)
	}
	record.Snapshots = []protocol.PoolVMSnapshot{target}
	record.PendingRecovery = &reservation
	record.PendingRecoveryResultSpec = &result
	state.VMs[vmID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	evidence := protocol.PoolVMRecoveryEvidence{
		Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("b", 64), ResultSpec: &result,
	}
	inventory := poolVMInventory{
		// Until the master acknowledges settlement, managedCurrent keeps exposing
		// the prior VM projection even though the native journal has terminal evidence.
		HostID: "host-a", Generation: membership.Generation, VMs: []protocol.VMCreateSpec{current},
		Storage: map[string]protocol.StorageIdentity{}, Observed: map[string]string{vmID: "shut off"},
		Recoveries: map[string]protocol.PoolVMRecoveryEvidence{reservation.OperationID: evidence},
	}
	if err := recordPoolVMInventory(s, membership, inventory); err != nil {
		t.Fatalf("completed revert inventory could not settle: %v", err)
	}
	state, err = loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	settled := state.VMs[vmID]
	if !reflect.DeepEqual(settled.Spec, result) || settled.PendingRecovery != nil || settled.PendingRecoveryResultSpec != nil ||
		settled.LastSettledRecovery == nil || !reflect.DeepEqual(*settled.LastSettledRecovery, evidence) {
		t.Fatalf("completed revert was not settled with the current-generation owner inventory: %#v", settled)
	}
}

func TestCompletedRevertInventoryEvidenceMatchesPreOrPostProjection(t *testing.T) {
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	record := poolVMRecord{PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID, Epoch: 1, State: "stopped", Spec: spec}
	reservation, result, _ := snapshotRevertReservationForTest(t, record, "revert-operation-1", 1)
	evidence := protocol.PoolVMRecoveryEvidence{
		Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("b", 64), ResultSpec: &result,
	}
	specSHA, diskSHA, err := poolVMRecoveryDigests(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !poolVMRevertInventoryMatchesEvidence(reservation, spec, evidence, specSHA, diskSHA) {
		t.Fatal("exact old projection with completed revert evidence was omitted")
	}
	resultSHA, resultDiskSHA, err := poolVMRecoveryDigests(result)
	if err != nil {
		t.Fatal(err)
	}
	if !poolVMRevertInventoryMatchesEvidence(reservation, result, evidence, resultSHA, resultDiskSHA) {
		t.Fatal("exact post-revert projection with completed evidence was omitted")
	}
	changedOld := spec
	changedOld.Name = "stale divergent projection"
	changedOldSHA, changedOldDiskSHA, err := poolVMRecoveryDigests(changedOld)
	if err != nil {
		t.Fatal(err)
	}
	if poolVMRevertInventoryMatchesEvidence(reservation, changedOld, evidence, changedOldSHA, changedOldDiskSHA) {
		t.Fatal("divergent old projection was accepted")
	}
	changedResult := result
	changedResult.Name = "different post-revert projection"
	if poolVMRevertInventoryMatchesEvidence(reservation, changedResult, evidence, "", "") {
		t.Fatal("divergent post-revert projection was accepted")
	}
}

func TestDeniedSnapshotAdmissionReplaysAndStaleOwnerOrderCannotReacquire(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	if err := poolVMLocal(s, spec.ID, "define", nil, true, &spec, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[spec.ID]
	prior, err := poolVMRecoveryReservationFor(record, "snapshot-existing", "snapshot-create", strings.Repeat("a", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	record.Snapshots = []protocol.PoolVMSnapshot{snapshotDescriptorForTest(prior, "before-upgrade", "")}
	state.VMs[spec.ID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{
		OperationID: "snapshot-denied", Kind: "snapshot-create", VM: &spec,
		Snapshot: &protocol.VMSnapshotRequest{Name: "BEFORE-UPGRADE"},
	}
	deniedCommand := command
	createdAt := time.Date(2026, 9, 25, 15, 0, 0, 0, time.UTC)
	first, err := ReservePoolVMSnapshot(context.Background(), s, command, 20, strings.Repeat("b", 64), createdAt)
	if err != nil || first.AdmissionRefusal != "name-in-use" {
		t.Fatalf("duplicate name did not return a durable exact denial: %#v (%v)", first, err)
	}
	// A lost reserve reply retries the same operation order and receives the same
	// denied reservation, never an executable acknowledgement.
	replayed, err := ReservePoolVMSnapshot(context.Background(), s, command, 20, strings.Repeat("b", 64), createdAt)
	if err != nil || replayed != first {
		t.Fatalf("lost denial acknowledgement was not replayed exactly: %#v / %#v (%v)", first, replayed, err)
	}
	state, err = loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record = state.VMs[spec.ID]
	if record.PendingRecovery == nil || *record.PendingRecovery != first || len(record.Snapshots) != 1 {
		t.Fatalf("denial did not remain reserved without changing catalog: %#v", record)
	}
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: first, Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("c", 64)}
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := settlePoolVMSnapshotStateLocked(s, &state, membership, s.State.HostID, evidence); err != nil {
		t.Fatalf("exact denied no-effects evidence was not settleable: %v", err)
	}
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	if state.VMs[spec.ID].PendingRecovery != nil || state.VMs[spec.ID].LastSettledRecovery == nil ||
		!reflect.DeepEqual(*state.VMs[spec.ID].LastSettledRecovery, evidence) || len(state.VMs[spec.ID].Snapshots) != 1 {
		t.Fatalf("denial settlement changed catalog or lost its receipt: %#v", state.VMs[spec.ID])
	}

	// A later successful operation advances the per-owner order watermark after
	// the bounded LastSettledRecovery receipt moves on.
	command = protocol.Command{
		OperationID: "snapshot-later", Kind: "snapshot-create", VM: &spec,
		Snapshot: &protocol.VMSnapshotRequest{Name: "after-upgrade"},
	}
	later, err := ReservePoolVMSnapshot(context.Background(), s, command, 21, strings.Repeat("d", 64), createdAt.Add(time.Minute))
	if err != nil || later.AdmissionRefusal != "" {
		t.Fatalf("later available snapshot was not admitted: %#v (%v)", later, err)
	}
	state, err = loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record = state.VMs[spec.ID]
	descriptor := *record.PendingSnapshot
	laterEvidence := protocol.PoolVMRecoveryEvidence{Reservation: later, Outcome: "completed", JournalSHA256: strings.Repeat("e", 64), Snapshot: &descriptor}
	if err := settlePoolVMSnapshotStateLocked(s, &state, membership, s.State.HostID, laterEvidence); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	// Owner epochs advance on ordinary start/stop transitions. The monotonic
	// journal order still fences old operations after that same owner advances.
	record = state.VMs[spec.ID]
	record.Epoch++
	state.VMs[spec.ID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	// A normal same-owner definition update must preserve the bounded order
	// watermark too. Use the new exact spec with the old order to isolate that
	// check from the ordinary spec-mismatch guard.
	record = state.VMs[spec.ID]
	updatedSpec := record.Spec
	updatedSpec.Revision++
	if err := reservePoolVMUpdate(&record, record.HostID, "update-after-snapshot", updatedSpec); err != nil {
		t.Fatal(err)
	}
	if err := commitPoolVMUpdate(&record, record.HostID, vmhost.VMUpdateEvidence{
		OperationID: "update-after-snapshot", Status: "succeeded", Previous: record.Spec, Current: updatedSpec,
	}); err != nil {
		t.Fatal(err)
	}
	state.VMs[spec.ID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	lateReplay := deniedCommand
	lateReplay.VM = &updatedSpec
	if _, err := ReservePoolVMSnapshot(context.Background(), s, lateReplay, 20, strings.Repeat("b", 64), createdAt); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("delayed duplicate denial reacquired after a later settlement: %v", err)
	}
}

func TestFullSnapshotCatalogDenialFitsFailedSettlementHeadroom(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	if err := poolVMLocal(s, spec.ID, "define", nil, true, &spec, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[spec.ID]
	for i := 0; i < poolVMSnapshotLimit; i++ {
		id := fmt.Sprintf("saved-snapshot-%02d", i)
		reservation, err := poolVMRecoveryReservationFor(record, id, "snapshot-create", strings.Repeat("a", 64), uint64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		record.Snapshots = append(record.Snapshots, snapshotDescriptorForTest(reservation, fmt.Sprintf("point-%02d", i), ""))
	}
	state.VMs[spec.ID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{
		OperationID: "snapshot-full-denial", Kind: "snapshot-create", VM: &spec,
		Snapshot: &protocol.VMSnapshotRequest{Name: "next-point"},
	}
	createdAt := time.Date(2026, 9, 25, 16, 0, 0, 0, time.UTC)
	reservation, err := ReservePoolVMSnapshot(context.Background(), s, command, 50, strings.Repeat("b", 64), createdAt)
	if err != nil || reservation.AdmissionRefusal != "catalog-full" {
		t.Fatalf("full catalog did not return its durable denial: %#v (%v)", reservation, err)
	}
	state, err = loadPoolVMs(s)
	if err != nil || state.VMs[spec.ID].PendingRecovery == nil || *state.VMs[spec.ID].PendingRecovery != reservation {
		t.Fatalf("full-catalog denial did not retain the exact pending reservation: %#v (%v)", state.VMs[spec.ID], err)
	}
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("c", 64)}
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := settlePoolVMSnapshotStateLocked(s, &state, membership, s.State.HostID, evidence); err != nil {
		t.Fatalf("full-catalog denial could not settle within reserved headroom: %v", err)
	}
	if len(state.VMs[spec.ID].Snapshots) != poolVMSnapshotLimit || state.VMs[spec.ID].PendingRecovery != nil {
		t.Fatalf("full-catalog denial changed entries or retained its fence: %#v", state.VMs[spec.ID])
	}
}

func TestPoolSnapshotCatalogPermanentlyRejectsReusedSnapshotID(t *testing.T) {
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: "host-a", Epoch: 3, State: "stopped", Spec: spec}
	first, err := poolVMRecoveryReservationFor(record, "snapshot_old", "snapshot-create", strings.Repeat("a", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := poolVMRecoveryReservationFor(record, "snapshot_new", "snapshot-create", strings.Repeat("b", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	record.LastSettledRecovery = &protocol.PoolVMRecoveryEvidence{Reservation: second, Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("c", 64)}
	record.Snapshots = []protocol.PoolVMSnapshot{snapshotDescriptorForTest(first, "older", "")}
	if err := reservePoolVMRecovery(&record, first); err == nil {
		t.Fatal("an older cataloged snapshot ID was reacquired after the bounded receipt moved on")
	}
}

func TestSnapshotReservationRechecksAuthorityAfterConcurrentFreeze(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	if err := poolVMLocal(s, spec.ID, "define", nil, true, &spec, nil); err != nil {
		t.Fatal(err)
	}
	req := poolVMRequest{
		VMID: spec.ID, Action: "snapshot-reserve", OperationID: "snapshot-race",
		VM: &spec, Snapshot: &protocol.VMSnapshotRequest{Name: "race-point"},
		SnapshotCreatedAt: time.Now().UTC(), IntentSHA256: strings.Repeat("a", 64), OwnerOperationOrder: 1,
	}

	// Hold the peer-state lock until the reservation has acquired the VM lock.
	// This models freezePoolAuthority's final locked freeze write racing an
	// otherwise-ready reservation after the freeze replica has been built.
	poolPeerMu.Lock()
	done := make(chan error, 1)
	go func() {
		poolVMMu.Lock()
		_, err := reservePoolVMSnapshotLocked(s, "host-a", req)
		poolVMMu.Unlock()
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if !poolVMMu.TryLock() {
			break
		}
		poolVMMu.Unlock()
		if time.Now().After(deadline) {
			poolPeerMu.Unlock()
			t.Fatal("snapshot reservation did not reach the peer-lock boundary")
		}
		runtime.Gosched()
	}
	peer, err := loadPoolPeer(s)
	if err != nil {
		poolPeerMu.Unlock()
		t.Fatal(err)
	}
	peer.Transfer = &poolTransferRecord{Action: "promote", Stage: "replicate", Frozen: true}
	if err := savePoolPeer(s, peer); err != nil {
		poolPeerMu.Unlock()
		t.Fatal(err)
	}
	poolPeerMu.Unlock()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("reservation crossed an authority freeze: %v", err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if state.VMs[spec.ID].PendingRecovery != nil {
		t.Fatal("refused reservation was persisted after authority freeze")
	}
}

func TestPoolVMRecoveryGuardsExistingMasterActionsButNotEmergencyStop(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	if err := poolVMLocal(s, spec.ID, "define", nil, true, &spec, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[spec.ID]
	record.State = "running"
	reservation, result, target := snapshotRevertReservationForTest(t, record, "operation-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	record.Snapshots = []protocol.PoolVMSnapshot{target}
	record.PendingRecoveryResultSpec = &result
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		t.Fatal(err)
	}
	state.VMs[spec.ID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocal(s, spec.ID, "start", nil, false, nil, nil); err == nil {
		t.Fatal("start crossed pending snapshot recovery")
	}
	if err := poolVMLocal(s, spec.ID, "reboot", nil, false, nil, nil); err == nil {
		t.Fatal("reboot crossed pending snapshot recovery")
	}
	if err := poolVMLocal(s, spec.ID, "media", nil, false, nil, nil); err == nil {
		t.Fatal("media change crossed pending snapshot recovery")
	}
	if err := poolVMLocal(s, spec.ID, "define", nil, false, &spec, nil); err == nil {
		t.Fatal("definition confirmation crossed pending snapshot recovery")
	}
	// Emergency reducing power stays available and changes no recovery receipt.
	if err := poolVMLocal(s, spec.ID, "stop", nil, false, nil, nil); err != nil {
		t.Fatalf("emergency stop was blocked: %v", err)
	}
	state, err = loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if state.VMs[spec.ID].PendingRecovery == nil || state.VMs[spec.ID].LastSettledRecovery != nil {
		t.Fatal("stop released or settled an unresolved recovery reservation")
	}
}

func TestPoolVMRecoverySettlementRequiresExactTerminalEvidenceAndKeepsReplay(t *testing.T) {
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: "host-a", Epoch: 7, State: "stopped", Spec: spec}
	reservation, err := poolVMRecoveryReservationFor(record, "operation-1", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		t.Fatal(err)
	}
	descriptor := snapshotDescriptorForTest(reservation, "before-upgrade", "pre-upgrade")
	record.PendingSnapshot = &descriptor
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("a", 64), Snapshot: &descriptor}
	wrong := evidence
	wrong.Reservation.OwnerHostID = "host-b"
	if err := settlePoolVMRecovery(&record, "host-a", wrong); err == nil {
		t.Fatal("evidence from a different owner was accepted")
	}
	unknown := evidence
	unknown.Outcome = "partial"
	if err := settlePoolVMRecovery(&record, "host-a", unknown); err == nil {
		t.Fatal("partial native result released the reservation")
	}
	wrongDependencies := evidence
	wrongDescriptor := descriptor
	wrongDescriptor.Dependencies = &protocol.PoolVMSnapshotDependencies{StorageIDs: []string{"other-repo"}, NetworkIDs: []string{}, MediaIDs: []string{}}
	wrongDependencies.Snapshot = &wrongDescriptor
	if err := settlePoolVMRecovery(&record, "host-a", wrongDependencies); err == nil {
		t.Fatal("descriptor with mismatched historical resource dependencies was accepted")
	}
	missingDependencies := evidence
	missingDescriptor := descriptor
	missingDescriptor.Dependencies = nil
	missingDependencies.Snapshot = &missingDescriptor
	if err := settlePoolVMRecovery(&record, "host-a", missingDependencies); err == nil {
		t.Fatal("new reservation accepted a completed descriptor with unknown dependencies")
	}
	if record.PendingRecovery == nil {
		t.Fatal("rejected evidence released the pending reservation")
	}
	if err := settlePoolVMRecovery(&record, "host-a", evidence); err != nil {
		t.Fatal(err)
	}
	if record.PendingRecovery != nil || record.LastSettledRecovery == nil {
		t.Fatalf("terminal evidence did not move to bounded replay receipt: %#v", record)
	}
	if len(record.Snapshots) != 1 || !reflect.DeepEqual(record.Snapshots[0], descriptor) {
		t.Fatalf("settled descriptor was not committed to the master catalog: %#v", record.Snapshots)
	}
	if err := settlePoolVMRecovery(&record, "host-a", evidence); err != nil {
		t.Fatalf("exact evidence replay was not idempotent: %v", err)
	}
	if err := reservePoolVMRecovery(&record, reservation); !errors.Is(err, errPoolVMRecoverySettled) {
		t.Fatalf("terminal operation ID was reused: %v", err)
	}
}

func TestSnapshotSettlementWithoutRecordedDependenciesStaysConservative(t *testing.T) {
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	record := poolVMRecord{PoolID: spec.PoolID, VMID: spec.ID, HostID: "host-a", Epoch: 7, State: "stopped", Spec: spec}
	reservation, err := poolVMRecoveryReservationFor(record, "undeclared-deps-snapshot-1", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		t.Fatal(err)
	}
	descriptor := snapshotDescriptorForTest(reservation, "before-dependency-field", "")
	descriptor.Dependencies = nil
	record.PendingSnapshot = &descriptor
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("a", 64), Snapshot: &descriptor}
	if err := settlePoolVMRecovery(&record, "host-a", evidence); err != nil {
		t.Fatalf("exact evidence without recorded dependencies could not settle: %v", err)
	}
	if len(record.Snapshots) != 1 || record.Snapshots[0].Dependencies != nil {
		t.Fatalf("descriptor was incorrectly upgraded to an invented empty dependency set: %#v", record.Snapshots)
	}
	if !poolVMSnapshotUsesResource(record, "network", "unrecorded-network") || !poolVMSnapshotUsesResource(record, "media", "unrecorded-media") || !poolVMSnapshotUsesResource(record, "storage", "unrecorded-storage") {
		t.Fatal("snapshot with unknown dependencies did not fail closed for all resource classes")
	}
}

func TestPendingRevertDeleteResourceUseGatesOnCapturedAndCurrentDeps(t *testing.T) {
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	record := poolVMRecord{PoolID: spec.PoolID, VMID: spec.ID, HostID: "host-a", Epoch: 1, State: "stopped", Spec: spec}
	reservation := snapshotDeleteReservationForTest(t, &record, "delete-op", 1)
	record.PendingRecovery = &reservation
	deps, err := protocol.PoolVMSnapshotDependenciesForSpec(spec)
	if err != nil || len(deps.StorageIDs) == 0 {
		t.Fatalf("fixture deps unavailable: %#v (%v)", deps, err)
	}
	used := deps.StorageIDs[0]
	if !poolVMSnapshotUsesResource(record, "storage", used) {
		t.Fatal("pending delete ignored captured/current storage dependency")
	}
	if poolVMSnapshotUsesResource(record, "storage", "unrelated-storage") || poolVMSnapshotUsesResource(record, "network", "unrelated-network") {
		t.Fatal("pending delete treated an unrelated resource as in use")
	}
}

func snapshotDeleteReservationForTest(t *testing.T, record *poolVMRecord, operationID string, ownerOrder uint64) protocol.PoolVMRecoveryReservation {
	t.Helper()
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(record.Spec)
	if err != nil {
		t.Fatal(err)
	}
	specSHA, diskSHA, err := poolVMRecoveryDigests(record.Spec)
	if err != nil {
		t.Fatal(err)
	}
	target := protocol.PoolVMSnapshot{
		ID: "snapshot-target-delete", VMID: record.VMID, Name: "delete-target", CreatedAt: time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC),
		SpecRevision: record.Spec.Revision, SpecSHA256: specSHA, DiskIdentitySHA256: diskSHA, Dependencies: &dependencies,
	}
	reservation, err := poolVMDeleteReservationFor(*record, operationID, strings.Repeat("d", 64), ownerOrder, target)
	if err != nil {
		t.Fatal(err)
	}
	record.Snapshots = append(append([]protocol.PoolVMSnapshot{}, record.Snapshots...), target)
	return reservation
}

func snapshotDescriptorForTest(reservation protocol.PoolVMRecoveryReservation, name, description string) protocol.PoolVMSnapshot {
	spec := poolVMTestSpec(reservation.VMID, reservation.OwnerHostID)
	dependencies, _ := protocol.PoolVMSnapshotDependenciesForSpec(*spec)
	return protocol.PoolVMSnapshot{
		ID: reservation.OperationID, VMID: reservation.VMID, Name: name, Description: description,
		CreatedAt: time.Now().UTC(), SpecRevision: reservation.SpecRevision,
		SpecSHA256: reservation.SpecSHA256, DiskIdentitySHA256: reservation.DiskIdentitySHA256,
		Dependencies: &dependencies,
	}
}

func snapshotRevertReservationForTest(t *testing.T, record poolVMRecord, operationID string, ownerOrder uint64) (protocol.PoolVMRecoveryReservation, protocol.VMCreateSpec, protocol.PoolVMSnapshot) {
	t.Helper()
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(record.Spec)
	if err != nil {
		t.Fatal(err)
	}
	specSHA, diskSHA, err := poolVMRecoveryDigests(record.Spec)
	if err != nil {
		t.Fatal(err)
	}
	target := protocol.PoolVMSnapshot{
		ID: "snapshot-target-1", VMID: record.VMID, Name: "target", CreatedAt: time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC),
		SpecRevision: record.Spec.Revision, SpecSHA256: specSHA, DiskIdentitySHA256: diskSHA, Dependencies: &dependencies,
	}
	reservation, err := poolVMRecoveryReservationFor(record, operationID, "snapshot-revert", strings.Repeat("d", 64), ownerOrder)
	if err != nil {
		t.Fatal(err)
	}
	result, err := protocol.PoolVMSnapshotRevertSpec(record.Spec, record.Spec)
	if err != nil {
		t.Fatal(err)
	}
	resultSHA, _, err := poolVMRecoveryDigests(result)
	if err != nil {
		t.Fatal(err)
	}
	targetSHA, err := protocol.PoolVMSnapshotDescriptorSHA256(target)
	if err != nil {
		t.Fatal(err)
	}
	reservation.TargetSnapshotID = target.ID
	reservation.TargetSnapshotSHA256 = targetSHA
	reservation.ResultSpecSHA256 = resultSHA
	return reservation, result, target
}

func TestPoolVMRecoveryInventoryIsMasterOnlyAndBounded(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{
		{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: "member-1", Hostname: "member-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"},
	}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "master-1")
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: "master-1", Epoch: 1, State: "stopped", Spec: spec}
	reservation, result, target := snapshotRevertReservationForTest(t, record, "operation-1", 1)
	record.PendingRecovery = &reservation
	record.PendingRecoveryResultSpec = &result
	record.Snapshots = []protocol.PoolVMSnapshot{target}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{spec.ID: record}}); err != nil {
		t.Fatal(err)
	}
	snapshots, reported, err := PoolVMSnapshotInventory(s)
	if err != nil || !reported || len(snapshots) != 1 || !reflect.DeepEqual(snapshots[0], record.Snapshots[0]) {
		t.Fatalf("master snapshot catalog = %#v, reported %v, err %v", snapshots, reported, err)
	}
	got, reported, err := PoolVMRecoveryInventory(s)
	if err != nil || !reported || len(got) != 1 || got[0] != reservation {
		t.Fatalf("master inventory = %#v, reported %v, err %v", got, reported, err)
	}
	membership.MasterHostID = "member-1"
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	snapshots, reported, err = PoolVMSnapshotInventory(s)
	if err != nil || reported || len(snapshots) != 0 {
		t.Fatalf("member reported master snapshot catalog: %#v, reported %v, err %v", snapshots, reported, err)
	}
	got, reported, err = PoolVMRecoveryInventory(s)
	if err != nil || reported || len(got) != 0 {
		t.Fatalf("member reported master authority: %#v, reported %v, err %v", got, reported, err)
	}

	// A master refuses an over-limit projection instead of truncating it.
	membership.MasterHostID = "master-1"
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	large := poolVMState{VMs: make(map[string]poolVMRecord)}
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1)
		itemSpec := *poolVMTestSpec(id, "master-1")
		item := poolVMRecord{PoolID: "pool-1", VMID: id, HostID: "master-1", Epoch: 1, State: "stopped", Spec: itemSpec}
		entry, err := poolVMRecoveryReservationFor(item, "operation-"+id, "snapshot-create", strings.Repeat("d", 64), 1)
		if err != nil {
			t.Fatal(err)
		}
		item.PendingRecovery = &entry
		descriptor := snapshotDescriptorForTest(entry, "pending-"+id, "")
		item.PendingSnapshot = &descriptor
		large.VMs[id] = item
	}
	largeBytes, err := json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poolVMFile(s), largeBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PoolVMRecoveryInventory(s); err == nil {
		t.Fatal("over-limit snapshot inventory was truncated or accepted")
	}
}

func TestPoolVMRecoveryStorageDependencyUsesReservedVMDisks(t *testing.T) {
	vm := poolVMRecord{Spec: protocol.VMCreateSpec{Disks: []protocol.VMDisk{{StorageID: "repo-a"}}}}
	if poolVMRecoveryUsesStorage(vm, "repo-a") {
		t.Fatal("VM without a recovery reservation was treated as a blocker")
	}
	vm.PendingRecovery = &protocol.PoolVMRecoveryReservation{}
	if !poolVMRecoveryUsesStorage(vm, "repo-a") {
		t.Fatal("pending snapshot did not retain its repository dependency")
	}
	if poolVMRecoveryUsesStorage(vm, "repo-b") {
		t.Fatal("unrelated repository was blocked")
	}
}

func TestRetainedSnapshotCatalogProtectsHistoricalPoolResources(t *testing.T) {
	master, member := joinedPair(t)
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", member.State.HostID)
	spec.NICs = []protocol.VMNIC{{NetworkID: "network-1", Model: "virtio"}}
	spec.MediaID = "media-1"
	record := poolVMRecord{PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID, Epoch: 1, State: "stopped", Spec: spec}
	reservation, err := poolVMRecoveryReservationFor(record, "snapshot-old", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := snapshotDescriptorForTest(reservation, "before-resource-edit", "")
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Dependencies = &dependencies
	record.Snapshots = []protocol.PoolVMSnapshot{descriptor}
	state, err := loadPoolVMs(master)
	if err != nil {
		t.Fatal(err)
	}
	state.VMs[spec.ID] = record
	state.Inventory[member.State.HostID] = poolVMInventory{HostID: member.State.HostID, Generation: membership.Generation}
	if err := savePoolVMs(master, state); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []protocol.Command{
		{OperationID: "remove-network", Kind: "network-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{Resource: "network", Action: "remove", ResourceID: "network-1", PoolID: spec.PoolID, HostID: master.State.HostID, ExpectedGeneration: 1}},
		{OperationID: "remove-media", Kind: "media-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{Resource: "media", Action: "remove", ResourceID: "media-1", PoolID: spec.PoolID, HostID: master.State.HostID, ExpectedGeneration: 1}},
	} {
		if _, err := BeginPoolResourceRemoval(context.Background(), master, cmd); err == nil || !strings.Contains(err.Error(), "retained VM snapshot") {
			t.Fatalf("historical %s dependency did not block removal: %v", cmd.ResourceLifecycle.Resource, err)
		}
	}
	state, err = loadPoolVMs(master)
	if err != nil || len(state.VMs[spec.ID].Snapshots) != 1 {
		t.Fatalf("refused resource removal changed the durable catalog: %#v (%v)", state.VMs[spec.ID], err)
	}
}

func TestPoolStorageDetachIsRefusedWhileVMRecoveryUsesRepository(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Name: "shared", Path: t.TempDir(), Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1}
	writeAppliedPoolStorage(t, s, storage)
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "master-1")
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: "master-1", Epoch: 1, State: "stopped", Spec: spec}
	reservation := snapshotDeleteReservationForTest(t, &record, "operation-1", 1)
	record.PendingRecovery = &reservation
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{spec.ID: record}}); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: "detach-1", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "repo-1", PoolID: "pool-1", HostID: "master-1", ExpectedGeneration: 1}}
	if finish, err := BeginPoolStorageMutation(context.Background(), s, command); err == nil {
		if finish != nil {
			finish(true)
		}
		t.Fatal("storage detach crossed a pending snapshot repository dependency")
	} else if !strings.Contains(err.Error(), "snapshot recovery") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	record.PendingRecovery = nil
	record.Snapshots = []protocol.PoolVMSnapshot{snapshotDescriptorForTest(reservation, "before-detach", "")}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	state.VMs[spec.ID] = record
	if err = savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	if finish, err := BeginPoolStorageMutation(context.Background(), s, command); err == nil {
		if finish != nil {
			finish(true)
		}
		t.Fatal("storage detach crossed a retained snapshot storage dependency")
	} else if !strings.Contains(err.Error(), "retained VM snapshot") {
		t.Fatalf("unexpected retained-snapshot refusal: %v", err)
	}
	credentialRotation := storage
	credentialRotation.CredentialReceipt = "credential-next"
	credentialRotation.CredentialPresent = true
	update := protocol.Command{OperationID: "credential-rotate-1", Kind: "storage-update", StorageLifecycle: &protocol.StorageLifecycleRequest{
		Action: "update", RepositoryID: storage.ID, PoolID: storage.PoolID, HostID: storage.HostID,
		ExpectedGeneration: storage.Generation, Registration: &credentialRotation,
	}}
	finish, err := BeginPoolStorageMutation(context.Background(), s, update)
	if err != nil {
		t.Fatalf("credential rotation was incorrectly blocked by retained snapshot identity: %v", err)
	}
	finish(true)
}

func TestPoolStorageDetachSnapshotHistoryBlocksOldRepositoryAfterCurrentDefinitionMoves(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	storageA := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Name: "snapshot source", Path: t.TempDir(), Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports/a"}}, Generation: 1}
	storageB := protocol.StorageRegistration{ID: "repo-b", PoolID: "pool-1", HostID: "master-1", Name: "current source", Path: t.TempDir(), Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports/b"}}, Generation: 1}
	writeAppliedPoolStorage(t, s, storageA)
	writeAppliedPoolStorage(t, s, storageB)

	// The retained descriptor is from the earlier A-backed definition. The current
	// master VM inventory now names B, so A can only be rejected here because of the
	// historical snapshot dependency. Public vm-update currently retains removed disk
	// files as well, so this test isolates the authority guard rather than claiming that
	// a complete native detach of the old disk is possible through today's lifecycle.
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "master-1")
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: spec.HostID, Epoch: 1, State: "stopped", Spec: spec}
	reservation, err := poolVMRecoveryReservationFor(record, "snapshot-old", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := snapshotDescriptorForTest(reservation, "before-storage-edit", "")
	record.Spec.Disks[0].StorageID = storageB.ID
	record.Spec.Revision++
	record.Snapshots = []protocol.PoolVMSnapshot{descriptor}
	if !poolVMSnapshotUsesResource(record, "storage", storageA.ID) || poolVMSnapshotUsesResource(record, "storage", storageB.ID) {
		t.Fatalf("fixture does not isolate the historical A dependency from current B: %#v", record.Snapshots[0].Dependencies)
	}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{spec.ID: record}}); err != nil {
		t.Fatal(err)
	}

	detachA := protocol.Command{OperationID: "detach-a", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: storageA.ID, PoolID: storageA.PoolID, HostID: storageA.HostID, ExpectedGeneration: storageA.Generation}}
	if finish, err := BeginPoolStorageMutation(context.Background(), s, detachA); err == nil {
		if finish != nil {
			finish(true)
		}
		t.Fatal("pool master allowed detaching A despite the retained snapshot")
	} else if err.Error() != "pool repository is required by a retained VM snapshot" {
		t.Fatalf("historical A dependency returned the wrong refusal: %v", err)
	}

	// B is not protected by this snapshot-specific reservation. This only verifies
	// the master history guard; the host-local disk-use guard still protects current B.
	detachB := protocol.Command{OperationID: "detach-b", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: storageB.ID, PoolID: storageB.PoolID, HostID: storageB.HostID, ExpectedGeneration: storageB.Generation}}
	finish, err := BeginPoolStorageMutation(context.Background(), s, detachB)
	if err != nil {
		t.Fatalf("snapshot on A blocked the unrelated B repository at the master history guard: %v", err)
	}
	finish(true)
}

func TestPoolVMRecoveryReservationReplicatesAndBlocksEject(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	maintenanceMembership(t, s, "master-1", maintenanceMember("master-1", false), maintenanceMember("member-1", false))
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "member-1")
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: "member-1", Epoch: 2, State: "stopped", Spec: spec}
	reservation := snapshotDeleteReservationForTest(t, &record, "operation-1", 1)
	record.PendingRecovery = &reservation
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{spec.ID: record}}); err != nil {
		t.Fatal(err)
	}
	raw, err := poolReplicaVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	var replicated map[string]poolVMRecord
	if err := json.Unmarshal(raw, &replicated); err != nil {
		t.Fatal(err)
	}
	if replicated[spec.ID].PendingRecovery == nil || *replicated[spec.ID].PendingRecovery != reservation {
		t.Fatalf("pending recovery was not included in the existing VM authority replica: %#v", replicated[spec.ID])
	}

	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 3, Members: []protocol.PoolMember{{HostID: "member-1", Status: "ready", Generation: 3}}}
	inventory := poolVMInventory{HostID: "member-1", Generation: 3}
	state := poolVMState{VMs: map[string]poolVMRecord{spec.ID: record}, Inventory: map[string]poolVMInventory{"member-1": inventory}}
	if err := poolEjectOwnerRefusal(state, membership, membership.Members[0]); err == nil || !strings.Contains(err.Error(), "snapshot recovery") {
		t.Fatalf("eject ignored unresolved snapshot recovery: %v", err)
	}
}

func TestPoolVMRecoveryEvidenceWireContractRejectsUnsupportedOutcomes(t *testing.T) {
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: "host-a", Epoch: 1, State: "defined", Spec: spec}
	reservation, err := poolVMRecoveryReservationFor(record, "operation-1", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"completed", "failed-before-effects"} {
		evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: outcome, JournalSHA256: strings.Repeat("a", 64)}
		if outcome == "completed" {
			descriptor := snapshotDescriptorForTest(reservation, "before-upgrade", "")
			evidence.Snapshot = &descriptor
		}
		if err := evidence.Valid(); err != nil {
			t.Fatalf("valid terminal outcome %q rejected: %v", outcome, err)
		}
	}
	for _, outcome := range []string{"partial", "unknown", "failed-after-effects", ""} {
		evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: outcome, JournalSHA256: strings.Repeat("a", 64)}
		if err := evidence.Valid(); err == nil {
			t.Fatalf("unsafe terminal outcome %q accepted", outcome)
		}
	}
}

func TestPoolVMSnapshotSettlementAckRequiresExactEvidence(t *testing.T) {
	reservation, err := poolVMRecoveryReservationFor(
		poolVMRecord{PoolID: "pool-1", VMID: "11111111-1111-4111-8111-111111111111", HostID: "host-a", Epoch: 1,
			Spec: *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")},
		"snapshot-one", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := snapshotDescriptorForTest(reservation, "before-upgrade", "")
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("a", 64), Snapshot: &descriptor}
	ack := poolVMRequest{VMID: reservation.VMID, Action: "snapshot-settle", SnapshotSettled: true, RecoveryEvidence: &evidence}
	if !validPoolVMSnapshotSettlementAck(ack, evidence) {
		t.Fatal("exact owner-settlement acknowledgement was rejected")
	}
	for _, rejected := range []poolVMRequest{{}, {VMID: reservation.VMID, Action: "snapshot-settle", RecoveryEvidence: &evidence}} {
		if validPoolVMSnapshotSettlementAck(rejected, evidence) {
			t.Fatalf("settlement accepted an empty or missing-positive-ack response: %#v", rejected)
		}
	}
	changed := evidence
	changed.JournalSHA256 = strings.Repeat("b", 64)
	ack.RecoveryEvidence = &changed
	if validPoolVMSnapshotSettlementAck(ack, evidence) {
		t.Fatal("settlement accepted an acknowledgement for different owner evidence")
	}
}

func TestPoolEjectRefusesVMWithRetainedSnapshotCatalog(t *testing.T) {
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "member-1")
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: "member-1", Epoch: 1, State: "stopped", Spec: spec}
	reservation, err := poolVMRecoveryReservationFor(record, "snapshot-one", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	record.Snapshots = []protocol.PoolVMSnapshot{snapshotDescriptorForTest(reservation, "before-upgrade", "")}
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 3, Members: []protocol.PoolMember{{HostID: "member-1", Status: "ready", Generation: 3}}}
	state := poolVMState{VMs: map[string]poolVMRecord{spec.ID: record}, Inventory: map[string]poolVMInventory{
		"member-1": {HostID: "member-1", Generation: 3, VMs: []protocol.VMCreateSpec{spec}, Observed: map[string]string{spec.ID: "shut off"}},
	}}
	if err := poolEjectOwnerRefusal(state, membership, membership.Members[0]); err == nil || !strings.Contains(err.Error(), "snapshots that are not yet transferable") {
		t.Fatalf("eject allowed retained snapshots to leave the master catalog: %v", err)
	}
}

func TestUnrelatedOwnerGrowthCannotConsumePendingSnapshotSettlementHeadroom(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	record := poolVMRecord{PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID, Epoch: 1, State: "stopped", Spec: spec}
	reservation, err := poolVMRecoveryReservationFor(record, "snapshot-one", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := snapshotDescriptorForTest(reservation, "before-upgrade", "")
	record.PendingRecovery, record.PendingSnapshot = &reservation, &descriptor
	original := poolVMState{VMs: map[string]poolVMRecord{spec.ID: record}}
	if err := savePoolVMs(s, original); err != nil {
		t.Fatalf("initial pending reservation did not fit: %v", err)
	}
	peer, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	currentMembership := peer.Membership
	peer.Membership = protocol.PoolMembership{}
	if err := savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(s, original); err == nil {
		t.Fatal("pending snapshot owner write proceeded with invalid pool authority")
	}
	peer.Membership = currentMembership
	if err := savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	peer, err = loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	prospective := poolVMState{VMs: map[string]poolVMRecord{spec.ID: record}}
	ownerFits := func(state poolVMState) bool {
		vmBytes, err := json.Marshal(state.VMs)
		if err != nil || len(vmBytes) > protocol.PoolReplicaLimit {
			return false
		}
		replica, _, err := buildPoolAuthorityReplicaForMembership(s, peer.Membership, vmBytes)
		return err == nil && poolAuthorityReplicaFitsAddressedBodies(s, replica, peer.CAKeyPEM) == nil
	}
	var fittingState poolVMState
	foundHeadroomBoundary := false
	for count := 1; count <= 300 && !foundHeadroomBoundary; count++ {
		id := fmt.Sprintf("22222222-2222-4222-8222-%012x", count)
		filler := *poolVMTestSpec(id, "host-a")
		filler.Description = strings.Repeat("x", 4096)
		prospective.VMs[id] = poolVMRecord{PoolID: "pool-1", VMID: id, HostID: "host-a", State: "defined", Spec: filler}
		if !ownerFits(prospective) {
			low, high := 0, 4095
			best := -1
			for low <= high {
				mid := low + (high-low)/2
				entry := prospective.VMs[id]
				entry.Spec.Description = strings.Repeat("x", mid)
				prospective.VMs[id] = entry
				if ownerFits(prospective) {
					best = mid
					low = mid + 1
				} else {
					high = mid - 1
				}
			}
			if best < 0 {
				break
			}
			entry := prospective.VMs[id]
			entry.Spec.Description = strings.Repeat("x", best)
			prospective.VMs[id] = entry
		}
		if !ownerFits(prospective) {
			break
		}
		if err := poolVMRecoverySettlementHeadroom(s, prospective); err != nil {
			fittingState = prospective
			foundHeadroomBoundary = true
		}
	}
	if !foundHeadroomBoundary {
		t.Fatal("test could not find valid owner growth that fits while pending but not after settlement")
	}
	actualBytes, err := json.Marshal(fittingState.VMs)
	if err != nil || len(actualBytes) > protocol.PoolReplicaLimit || !ownerFits(fittingState) {
		t.Fatalf("fixture does not fit the current owner body: bytes=%d err=%v", len(actualBytes), err)
	}
	if err := savePoolVMs(s, fittingState); err == nil {
		t.Fatal("unrelated owner growth consumed the capacity reserved for snapshot settlement")
	}
	stored, err := loadPoolVMs(s)
	if err != nil || len(stored.VMs) != 1 || stored.VMs[spec.ID].PendingRecovery == nil || *stored.VMs[spec.ID].PendingRecovery != reservation || stored.VMs[spec.ID].Snapshots != nil {
		t.Fatalf("rejected growth changed or lost the durable reservation: %#v (%v)", stored, err)
	}
}

func TestPoolVMRecoveryInventoryIgnoresObsoleteHistoryAndSettlesCurrentOperation(t *testing.T) {
	priorSpec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	priorRecord := poolVMRecord{PoolID: "pool-1", VMID: priorSpec.ID, HostID: "host-a", Epoch: 1, State: "stopped", Spec: priorSpec}
	prior, err := poolVMRecoveryReservationFor(priorRecord, "operation-old", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	currentSpec := priorSpec
	currentSpec.Revision = 1
	currentRecord := poolVMRecord{PoolID: "pool-1", VMID: currentSpec.ID, HostID: "host-a", Epoch: 2, State: "stopped", Spec: currentSpec}
	current := snapshotDeleteReservationForTest(t, &currentRecord, "operation-current", 2)
	currentRecord.PendingRecovery = &current
	state := poolVMState{VMs: map[string]poolVMRecord{currentSpec.ID: currentRecord}}
	priorDescriptor := snapshotDescriptorForTest(prior, "old", "")
	oldEvidence := protocol.PoolVMRecoveryEvidence{Reservation: prior, Outcome: "completed", JournalSHA256: strings.Repeat("a", 64), Snapshot: &priorDescriptor}
	currentEvidence := protocol.PoolVMRecoveryEvidence{Reservation: current, Outcome: "completed", JournalSHA256: strings.Repeat("b", 64)}
	deleted := prior
	deleted.VMID = "22222222-2222-4222-8222-222222222222"
	deletedEvidence := protocol.PoolVMRecoveryEvidence{Reservation: deleted, Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("c", 64)}
	recoveries := map[string]protocol.PoolVMRecoveryEvidence{
		prior.OperationID:   oldEvidence,
		current.OperationID: currentEvidence,
		deleted.OperationID: deletedEvidence,
	}
	if _, err := settlePoolVMRecoveryInventory(&state, "host-a", recoveries); err != nil {
		t.Fatal(err)
	}
	got := state.VMs[currentSpec.ID]
	if got.PendingRecovery != nil || got.LastSettledRecovery == nil || *got.LastSettledRecovery != currentEvidence {
		t.Fatalf("obsolete journal history blocked or replaced current settlement: %#v", got)
	}
	if len(got.Snapshots) != 0 {
		t.Fatalf("settled snapshot delete left catalog entries: %#v", got.Snapshots)
	}
}
