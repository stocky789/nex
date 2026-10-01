// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// Releasing retained originals deletes files a settling restore may rely on, so it takes
// the owner admission VM delete takes: the pool master's record must show no unfinished work and no retained snapshots.
func TestPoolVMReleaseRetainedTakesOwnerAdmission(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	local, shared := autostartFixture(t, s, false, false)
	autostartVirsh(t, local.ID, shared.ID)
	if err := poolVMLocal(s, local.ID, "define", nil, true, &local, nil); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: "release-1", Kind: "vm-release-retained", ReleaseRetained: &protocol.VMReleaseRetainedRequest{
		VMID: local.ID, PoolID: s.State.PoolID, HostID: s.State.HostID, Confirmation: local.Name}}
	if _, err := AdmitPoolVMExecution(context.Background(), s, command); err != nil {
		t.Fatalf("release of an idle VM was refused: %v", err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[local.ID]
	reservation, err := poolVMRecoveryReservationFor(record, "retained-point", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	record.Snapshots = []protocol.PoolVMSnapshot{snapshotDescriptorForTest(reservation, "before-change", "")}
	state.VMs[local.ID] = record
	if err = savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitPoolVMExecution(context.Background(), s, command); err == nil {
		t.Fatal("release was not refused by the master's record of the VM")
	}
}

// Core refuses a release itself when the master's record holds pending recovery or an
// unfinished request for the VM is journaled here, whatever the Controller checked.
func TestAcceptReleaseRetainedRefusesUnfinishedWork(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	local, shared := autostartFixture(t, s, false, false)
	autostartVirsh(t, local.ID, shared.ID)
	if err := poolVMLocal(s, local.ID, "define", nil, true, &local, nil); err != nil {
		t.Fatal(err)
	}
	release := func(id string) BackupResponse {
		return acceptBackupRequest(context.Background(), s, BackupRequest{Command: &protocol.Command{OperationID: id, Kind: "vm-release-retained",
			ReleaseRetained: &protocol.VMReleaseRetainedRequest{VMID: local.ID, PoolID: "-", HostID: "-", Confirmation: local.Name}}})
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[local.ID]
	reservation := snapshotDeleteReservationForTest(t, &record, "op-delete", 1)
	record.PendingRecovery = &reservation
	state.VMs[local.ID] = record
	if err = savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	if got := release("release-1"); !errors.Is(got.Err, ErrBackupConflict) {
		t.Fatalf("pending recovery: %+v", got)
	}
	record.PendingRecovery, record.Snapshots = nil, nil
	state.VMs[local.ID] = record
	if err = savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	s.State.Journal["restore-1"] = JournalEntry{Status: "pending", Command: &protocol.Command{OperationID: "restore-1", Kind: protocol.BackupKindRestoreReplace,
		RestoreReplace: &protocol.RestoreReplaceRequest{OriginalVMID: local.ID}}}
	if got := release("release-2"); !errors.Is(got.Err, ErrBackupConflict) {
		t.Fatalf("queued restore: %+v", got)
	}
}
