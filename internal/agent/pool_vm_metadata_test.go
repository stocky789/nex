// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestApplyPoolVMSnapshotMetadata(t *testing.T) {
	created := time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC)
	snap := protocol.PoolVMSnapshot{
		ID: "snapshot-001", VMID: "11111111-1111-4111-8111-111111111111", Name: "before",
		Description: "old", CreatedAt: created, SpecRevision: 1,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
	}
	other := snap
	other.ID, other.Name = "snapshot-002", "other"
	record := poolVMRecord{
		PoolID: "pool-1", VMID: snap.VMID, HostID: "host-a", Spec: *poolVMTestSpec(snap.VMID, "host-a"),
		Snapshots: []protocol.PoolVMSnapshot{snap, other},
	}
	updated, err := applyPoolVMSnapshotMetadata(&record, protocol.VMSnapshotRequest{SnapshotID: snap.ID, Name: "after", Description: "new"})
	if err != nil || updated.Name != "after" || updated.Description != "new" || updated.ID != snap.ID ||
		updated.CreatedAt != created || updated.SpecSHA256 != snap.SpecSHA256 || record.Snapshots[0].Name != "after" {
		t.Fatalf("metadata update failed: %+v err=%v", updated, err)
	}
	if _, err := applyPoolVMSnapshotMetadata(&record, protocol.VMSnapshotRequest{SnapshotID: snap.ID, Name: "other"}); !errors.Is(err, ErrSnapshotMetadataConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := applyPoolVMSnapshotMetadata(&record, protocol.VMSnapshotRequest{SnapshotID: "missing-1", Name: "gone"}); !errors.Is(err, ErrSnapshotMetadataNotFound) {
		t.Fatalf("missing snapshot: %v", err)
	}
	record.PendingRecovery = &protocol.PoolVMRecoveryReservation{TargetSnapshotID: "unrelated", OperationID: "op-create"}
	pending := snap
	pending.ID, pending.Name = "op-create", "pending-create"
	record.PendingSnapshot = &pending
	if _, err := applyPoolVMSnapshotMetadata(&record, protocol.VMSnapshotRequest{SnapshotID: snap.ID, Name: "safe"}); !errors.Is(err, ErrSnapshotMetadataPending) {
		t.Fatalf("any pending recovery must block metadata: %v", err)
	}
	record.PendingRecovery = nil
	if _, err := applyPoolVMSnapshotMetadata(&record, protocol.VMSnapshotRequest{SnapshotID: snap.ID, Name: "pending-create"}); !errors.Is(err, ErrSnapshotMetadataConflict) {
		t.Fatalf("pending create name must stay reserved: %v", err)
	}
}

func TestUpdatePoolVMSnapshotMetadataRequiresReplicaConfirm(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	if err := poolVMLocal(s, vm.ID, "define", nil, true, vm, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[vm.ID]
	reservation, err := poolVMRecoveryReservationFor(record, "snapshot-001", "snapshot-create", strings.Repeat("a", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	snap := snapshotDescriptorForTest(reservation, "before", "old")
	record.Snapshots = []protocol.PoolVMSnapshot{snap}
	state.VMs[vm.ID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdatePoolVMSnapshotMetadata(context.Background(), s, vm.ID, snap.ID, "after", "new")
	if err != nil || updated.Name != "after" || updated.Description != "new" {
		t.Fatalf("single-host metadata confirm: %#v err=%v", updated, err)
	}
	poolReplicaMu.Lock()
	poolReplicaPushing[s.dir] = true
	poolReplicaMu.Unlock()
	t.Cleanup(func() {
		poolReplicaMu.Lock()
		delete(poolReplicaPushing, s.dir)
		poolReplicaMu.Unlock()
	})
	if _, err := UpdatePoolVMSnapshotMetadata(context.Background(), s, vm.ID, snap.ID, "blocked", "busy"); err == nil {
		t.Fatal("metadata succeeded while replica push was already in flight")
	}
}
