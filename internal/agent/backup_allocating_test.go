// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAllocatingRestoreResumesOnlyExactRetainedOperation(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", s.State.PoolID, "point-1")
	if _, err := ImportPoolBackupCatalog(s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "restore-allocating", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1", HostID: s.State.HostID}}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatal(err)
	}
	pointDir := filepath.Join(mount, "point-1")
	manifest, originalManifestSHA, err := vmhost.ReadBackupManifest(pointDir)
	if err != nil {
		t.Fatal(err)
	}
	originalManifest, err := os.ReadFile(filepath.Join(pointDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join(pointDir, manifest.Disks[0].Filename)
	originalDisk, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := vmhost.BuildRestoreAsNewSpec(*manifest.CapturedSpec, *cmd.Restore, s.State.PoolID, s.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{spec.ID: {
		PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID, CreateOperationID: cmd.OperationID,
		State: "defined", Spec: spec,
	}}}); err != nil {
		t.Fatal(err)
	}
	if got := attentionRestoreRun(s, cmd, time.Now().UTC(), "prior native allocation was interrupted"); got.Status != "needs_attention" {
		t.Fatalf("attention state: %+v", got)
	}
	previousStage, previousBind := restoreAsNewAllocating, bindLegacyRestoreAsNewAllocating
	previousRegister, previousObserve := registerBackupRestoredAsNew, reconcileRestoredAsNew
	t.Cleanup(func() {
		restoreAsNewAllocating, bindLegacyRestoreAsNewAllocating = previousStage, previousBind
		registerBackupRestoredAsNew, reconcileRestoredAsNew = previousRegister, previousObserve
	})
	var stageChecks, registrations, observations int
	legacy, legacyBound := false, false
	restoreAsNewAllocating = func(_ *vmhost.ManagedEngine, operationID string, gotSpec protocol.VMCreateSpec, manifestSHA string, disks []protocol.BackupDiskArtifact, _ []protocol.BackupFirmwareArtifact) (bool, error) {
		stageChecks++
		if operationID != cmd.OperationID || gotSpec.ID != spec.ID || len(disks) != 1 {
			t.Fatal("stage check used a different target")
		}
		if manifestSHA != originalManifestSHA || disks[0] != manifest.Disks[0] {
			return false, ErrBackupConflict
		}
		if legacy && !legacyBound {
			return false, vmhost.ErrRestoreManifestUnbound
		}
		return true, nil
	}
	bindLegacyRestoreAsNewAllocating = func(_ *vmhost.ManagedEngine, _ context.Context, operationID string, _ protocol.VMCreateSpec, localDir, manifestSHA string, _ []protocol.BackupDiskArtifact, _ []protocol.BackupFirmwareArtifact) error {
		if operationID != cmd.OperationID || manifestSHA != originalManifestSHA {
			t.Fatal("legacy binding changed the retained intent")
		}
		localDisk, err := os.ReadFile(filepath.Join(localDir, manifest.Disks[0].Filename))
		if err != nil || string(localDisk) != string(originalDisk) {
			t.Fatalf("legacy binding ran after local copy changed: %v", err)
		}
		legacyBound = true
		return nil
	}
	registerBackupRestoredAsNew = func(_ context.Context, _ *vmhost.ManagedEngine, operationID string, gotSpec protocol.VMCreateSpec, localDir, manifestSHA string, artifacts []protocol.BackupDiskArtifact, _ ...protocol.BackupFirmwareArtifact) error {
		registrations++
		if operationID != cmd.OperationID || gotSpec.ID != spec.ID || manifestSHA == "" || len(artifacts) != 1 {
			t.Fatal("registration changed the retained target")
		}
		if _, err := os.Stat(filepath.Join(localDir, artifacts[0].Filename)); err != nil {
			t.Fatalf("verified local copy missing before registration: %v", err)
		}
		if registrations == 1 {
			return errors.New("native retry still unavailable")
		}
		return nil
	}
	reconcileRestoredAsNew = func(_ *vmhost.ManagedEngine, _ context.Context, operationID string, gotSpec protocol.VMCreateSpec) (protocol.Result, error) {
		observations++
		if operationID != cmd.OperationID || gotSpec.ID != spec.ID {
			t.Fatal("observation changed the retained target")
		}
		return protocol.Result{OperationID: operationID, Status: "succeeded", ObservedAt: time.Now().UTC()}, nil
	}
	changed := cmd
	changedReq := *cmd.Restore
	changedReq.NewVMName = "other-name"
	changed.Restore = &changedReq
	if got, err := reconcileRestoreAsNewAttention(context.Background(), s.dir, s.State, changed); err != nil || got.Status != "needs_attention" || stageChecks != 0 {
		t.Fatalf("changed intent retried native placement: %+v %v", got, err)
	}
	if got, err := reconcileRestoreAsNewAttention(context.Background(), s.dir, s.State, cmd); err != nil || got.Status != "needs_attention" ||
		got.Error != "native retry still unavailable" || registrations != 1 || observations != 0 {
		t.Fatalf("failed exact resume did not stay uncertain: %+v %v; registrations=%d observations=%d", got, err, registrations, observations)
	}
	if run, err := loadPoolBackupRun(s, cmd.OperationID); err != nil || run.Status != "needs_attention" {
		t.Fatalf("failed resume settled run: %+v %v", run, err)
	}
	if pins, err := ActiveBackupRestorePins(s); err != nil || !pins["point-1"] {
		t.Fatalf("failed resume released pin: %+v %v", pins, err)
	}
	replacement := []byte("different-valid-disk-payload")
	sum := sha256.Sum256(replacement)
	changedManifest := manifest
	changedManifest.Disks = append([]protocol.BackupDiskArtifact(nil), manifest.Disks...)
	changedManifest.Disks[0].Size = int64(len(replacement))
	changedManifest.Disks[0].SHA256 = hex.EncodeToString(sum[:])
	changedRaw, err := json.Marshal(changedManifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diskPath, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pointDir, "manifest.json"), changedRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := reconcileRestoreAsNewAttention(context.Background(), s.dir, s.State, cmd); err != nil || got.Status != "needs_attention" ||
		got.Error != ErrBackupConflict.Error() || registrations != 1 {
		t.Fatalf("changed valid point reached native retry: %+v %v; registrations=%d", got, err, registrations)
	}
	if err := os.WriteFile(diskPath, originalDisk, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pointDir, "manifest.json"), originalManifest, 0600); err != nil {
		t.Fatal(err)
	}
	legacy = true
	if got, err := reconcileRestoreAsNewAttention(context.Background(), s.dir, s.State, cmd); err != nil || got.Status != "succeeded" || registrations != 2 || observations != 1 || !legacyBound {
		t.Fatalf("exact resume did not settle: %+v %v; registrations=%d observations=%d", got, err, registrations, observations)
	}
	if run, err := loadPoolBackupRun(s, cmd.OperationID); err != nil || run.Status != "succeeded" || run.Stage != "registered" {
		t.Fatalf("settled run: %+v %v", run, err)
	}
	if pins, err := ActiveBackupRestorePins(s); err != nil || pins["point-1"] {
		t.Fatalf("successful resume retained pin: %+v %v", pins, err)
	}
}
