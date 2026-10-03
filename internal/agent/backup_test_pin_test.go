// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"testing"
	"time"
)

func TestSuccessfulTestRestoreStaysPinnedUntilCleanup(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", s.State.PoolID, "point-1")
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	previousPlace, previousRegister := requestTestRestorePlacement, registerTestRestoredAsNew
	previousStage, previousObserve := restoreAsNewAllocating, reconcileRestoredAsNew
	t.Cleanup(func() {
		requestTestRestorePlacement, registerTestRestoredAsNew = previousPlace, previousRegister
		restoreAsNewAllocating, reconcileRestoredAsNew = previousStage, previousObserve
	})
	requestTestRestorePlacement = func(_ context.Context, store *Store, spec protocol.VMCreateSpec, operationID string) error {
		state, err := loadPoolVMs(store)
		if err != nil {
			return err
		}
		if state.VMs == nil {
			state.VMs = map[string]poolVMRecord{}
		}
		state.VMs[spec.ID] = poolVMRecord{PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID,
			CreateOperationID: operationID, State: "defined", Spec: spec}
		return savePoolVMs(store, state)
	}
	registerTestRestoredAsNew = func(context.Context, *vmhost.ManagedEngine, string, protocol.VMCreateSpec,
		string, string, []protocol.BackupDiskArtifact, ...protocol.BackupFirmwareArtifact) error {
		return nil
	}
	newCommand := func(id, vmID string) protocol.Command {
		return protocol.Command{OperationID: id, Kind: protocol.BackupKindTestRestore, TestRestore: &protocol.TestRestoreRequest{
			RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: vmID,
			NewVMName: id, StorageID: "storage-1", HostID: s.State.HostID}}
	}
	direct := newCommand("test-direct", "22222222-2222-4222-8222-222222222222")
	if result, err := executeTestRestore(context.Background(), s, direct, time.Now().UTC()); err != nil || result.Status != "succeeded" {
		t.Fatalf("direct test restore: %+v %v", result, err)
	}
	attention := newCommand("test-resume", "33333333-3333-4333-8333-333333333333")
	if err := ensureBackupRunReserved(s, attention); err != nil {
		t.Fatal(err)
	}
	repo, err := GetPoolBackupRepository(s, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	point, err := getCatalogPoint(s, "repo-1", "point-1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := validatedCatalogBackupManifest(context.Background(), s, repo, "point-1", point.ManifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
		NewVMID: attention.TestRestore.NewVMID, NewVMName: attention.TestRestore.NewVMName,
		StorageID: "storage-1", HostID: s.State.HostID}
	spec, err := vmhost.BuildRestoreAsNewSpec(*manifest.CapturedSpec, req, s.State.PoolID, s.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if err := requestTestRestorePlacement(context.Background(), s, spec, attention.OperationID); err != nil {
		t.Fatal(err)
	}
	if result := attentionTestRestoreRun(s, attention, time.Now().UTC(), "retained allocation"); result.Status != "needs_attention" {
		t.Fatalf("attention test restore: %+v", result)
	}
	restoreAsNewAllocating = func(*vmhost.ManagedEngine, string, protocol.VMCreateSpec, string,
		[]protocol.BackupDiskArtifact, []protocol.BackupFirmwareArtifact) (bool, error) {
		return true, nil
	}
	reconcileRestoredAsNew = func(_ *vmhost.ManagedEngine, _ context.Context, operationID string, _ protocol.VMCreateSpec) (protocol.Result, error) {
		return protocol.Result{OperationID: operationID, Status: "succeeded", ObservedAt: time.Now().UTC()}, nil
	}
	if result, err := reconcileTestRestoreAttention(context.Background(), s.dir, s.State, attention); err != nil || result.Status != "succeeded" {
		t.Fatalf("resumed test restore: %+v %v", result, err)
	}
	if err := reconcileTerminalBackupRestorePins(s); err != nil {
		t.Fatal(err)
	}
	pins, err := loadPoolBackupPins(s)
	if err != nil || len(pins.Pins) != 2 || pins.Pins[direct.OperationID].OperationID == "" || pins.Pins[attention.OperationID].OperationID == "" {
		t.Fatalf("successful test VM pins were removed before cleanup: %+v %v", pins, err)
	}
}

func TestMemberTestRestoreSettlementKeepsSuccessfulPin(t *testing.T) {
	for _, tc := range []struct {
		kind, status string
		clear        bool
	}{
		{protocol.BackupKindTestRestore, "succeeded", false},
		{protocol.BackupKindTestRestore, "needs_attention", false},
		{protocol.BackupKindTestRestore, "failed", true},
		{protocol.BackupKindRestoreNew, "succeeded", true},
		{protocol.BackupKindRestoreReplace, "succeeded", true},
		{protocol.BackupKindBackupNow, "succeeded", false},
	} {
		if got := clearPeerRestorePinAfterSettlement(tc.kind, tc.status); got != tc.clear {
			t.Fatalf("kind=%s status=%s: clear=%v, want %v", tc.kind, tc.status, got, tc.clear)
		}
	}
}
