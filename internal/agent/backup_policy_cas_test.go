// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestBackupPolicyUpdateRequiresCurrentRevision(t *testing.T) {
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("vm-1", "host-a")
	otherVM := poolVMTestSpec("vm-2", "host-a")
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{
		vm.ID:      {PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: *vm},
		otherVM.ID: {PoolID: otherVM.PoolID, VMID: otherVM.ID, HostID: otherVM.HostID, State: "stopped", Spec: *otherVM},
	}}); err != nil {
		t.Fatal(err)
	}
	create := testPolicy()
	if _, err := UpsertPoolBackupPolicy(s, create); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertPoolBackupPolicy(s, create); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("update without revision: %v", err)
	}
	zero := uint64(0)
	create.ID, create.ScheduleID, create.ExpectedRevision = "pol-2", "sch-2", &zero
	if _, err := UpsertPoolBackupPolicy(s, create); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("create with expected revision: %v", err)
	}

	update := testPolicy()
	current := uint64(1)
	update.ExpectedRevision = &current
	update.Retention.KeepLatest = 9
	command := protocol.Command{OperationID: "policy-update-1", Kind: "backup-policy-upsert", BackupPolicy: &update}
	request := BackupRequest{Command: &command}
	first := acceptBackupRequest(context.Background(), s, request)
	if first.Err != nil || first.Result == nil || first.Result.Status != "succeeded" || first.Policy == nil || first.Policy.Revision != 2 {
		t.Fatalf("update: %+v", first)
	}
	replay := acceptBackupRequest(context.Background(), s, request)
	if replay.Err != nil || replay.Result == nil || replay.Result.Status != "succeeded" {
		t.Fatalf("exact replay: %+v", replay)
	}
	stored, err := GetPoolBackupPolicy(s, update.ID)
	if err != nil || stored.Revision != 2 || stored.Retention.KeepLatest != 9 || stored.ExpectedRevision != nil {
		t.Fatalf("stored policy after replay: %+v, %v", stored, err)
	}
	if _, err := SetPoolBackupPolicyPaused(s, update.ID, true); err != nil {
		t.Fatal(err)
	}
	stale := testPolicy()
	staleRevision := uint64(1)
	stale.ExpectedRevision = &staleRevision
	stale.VMIDs = []string{otherVM.ID}
	stale.Retention.KeepLatest = 3
	staleCommand := protocol.Command{OperationID: "policy-stale-1", Kind: "backup-policy-upsert", BackupPolicy: &stale}
	staleRequest := BackupRequest{Command: &staleCommand}
	if got := acceptBackupRequest(context.Background(), s, staleRequest); !errors.Is(got.Err, ErrBackupConflict) {
		t.Fatalf("stale update after pause: %+v", got)
	}
	if got := acceptBackupRequest(context.Background(), s, staleRequest); got.Err != nil || got.Result == nil || got.Result.Status != "failed" {
		t.Fatalf("stale exact replay: %+v", got)
	}
	stored, err = GetPoolBackupPolicy(s, update.ID)
	if err != nil || stored.Revision != 3 || !stored.Paused || len(stored.VMIDs) != 1 || stored.Retention.KeepLatest != 9 {
		t.Fatalf("stale update changed policy: %+v, %v", stored, err)
	}
	current = stored.Revision
	stale.ExpectedRevision = &current
	stale.Name = "Edited"
	if _, err := UpsertPoolBackupPolicy(s, stale); err != nil {
		t.Fatalf("current revision update: %v", err)
	}
	stale.Retention.KeepLatest = 1
	if _, err := UpsertPoolBackupPolicy(s, stale); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("stale update after VM/retention change: %v", err)
	}
	stored, err = GetPoolBackupPolicy(s, update.ID)
	if err != nil || stored.Revision != 4 || len(stored.VMIDs) != 1 || stored.VMIDs[0] != otherVM.ID || stored.Retention.KeepLatest != 3 {
		t.Fatalf("later stale update changed policy: %+v, %v", stored, err)
	}
}
