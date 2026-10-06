// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func seedPublishedPoint(t *testing.T, mount, repoID, poolID, pointID string, sourceVMID ...string) {
	t.Helper()
	vmID := "vm-1"
	if len(sourceVMID) > 0 {
		vmID = sourceVMID[0]
	}
	if err := vmhost.ClaimBackupRepositoryOwner(mount, repoID, poolID); err != nil {
		// Owner may already exist from a prior call in the same test.
		_ = err
	}
	diskPath := filepath.Join(t.TempDir(), "vda.qcow2")
	payload := []byte("disk-bytes-for-restore")
	if err := os.WriteFile(diskPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	captured := protocol.VMCreateSpec{
		ID: vmID, PoolID: poolID, HostID: "host-a", Name: "app", GuestOS: "linux",
		CPU: protocol.VMCPU{Count: 1, Model: "host-model"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: "storage-1", Name: "disk", SizeGiB: 1, Bus: "virtio", BootOrder: 1}},
		Boot:  protocol.VMBoot{Firmware: "bios"},
	}
	specDigest, _, err := vmhost.BackupSpecAndDiskDigests(captured, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: pointID, RepositoryID: repoID,
		PoolID: poolID, SourceVMID: vmID, SourceVMName: "app", CapturedAt: time.Now().UTC().Truncate(time.Second),
		Consistency: protocol.BackupCaptureStopped, SpecSHA256: specDigest,
		DiskIdentitySHA256: strings.Repeat("b", 64), CapturedSpec: &captured,
		Disks: []protocol.BackupDiskArtifact{{
			Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: int64(len(payload)), SHA256: digest,
		}},
		Complete: true, Success: 1,
	}
	if _, err := vmhost.PublishBackupRecoveryPoint(mount, manifest, map[string]string{"vda.qcow2": diskPath}); err != nil {
		t.Fatal(err)
	}
}

func replaceCatalogedPointWithValidManifest(t *testing.T, s *Store, repo protocol.BackupRepository, pointID string) {
	t.Helper()
	if _, err := importPoolBackupCatalog(context.Background(), s, repo.ID); err != nil {
		t.Fatal(err)
	}
	pointDir := filepath.Join(repo.MountPath, pointID)
	manifest, _, err := vmhost.ReadBackupManifest(pointDir)
	if err != nil {
		t.Fatal(err)
	}
	manifest.CapturedAt = manifest.CapturedAt.Add(time.Second)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := vmhost.AtomicWrite(filepath.Join(pointDir, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (filesystemBackupStorage{repo: repo}).StatPoint(context.Background(), pointID); err != nil {
		t.Fatalf("replacement manifest must remain fully valid: %v", err)
	}
}

func importTestBackupCatalog(t *testing.T, s *Store) {
	t.Helper()
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
}

func seedTestCatalogPoint(t *testing.T, s *Store) {
	t.Helper()
	if err := savePoolBackupCatalogEntry(s, protocol.BackupRecoveryPoint{
		ID: "point-1", RepositoryID: "repo-1", PoolID: s.State.PoolID, SourceVMID: "vm-1",
		CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreManifestMustMatchMasterCatalogDigest(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	replaceCatalogedPointWithValidManifest(t, s, repo, "point-1")
	point, err := getCatalogPoint(s, repo.ID, "point-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := validatedCatalogBackupManifest(context.Background(), s, repo, "point-1", point.ManifestSHA256); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("valid same-ID replacement must not satisfy master catalog: %v", err)
	}
	for _, cmd := range []protocol.Command{
		{OperationID: "restore-changed", Kind: protocol.BackupKindRestoreNew,
			Restore: &protocol.RestoreAsNewRequest{RepositoryID: repo.ID, RecoveryPointID: "point-1",
				NewVMID: "11111111-1111-4111-8111-111111111111", NewVMName: "Restored", StorageID: "storage-1"}},
		{OperationID: "test-changed", Kind: protocol.BackupKindTestRestore,
			TestRestore: &protocol.TestRestoreRequest{RepositoryID: repo.ID, RecoveryPointID: "point-1",
				NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "Test", StorageID: "storage-1"}},
	} {
		result, err := executeBackupCommand(context.Background(), s.dir, s.State, cmd)
		if err != nil || result.Status != "failed" {
			t.Fatalf("%s must refuse changed manifest before placement: %+v err=%v", cmd.Kind, result, err)
		}
		if _, err := os.Stat(filepath.Join(s.dir, "backup-restore", cmd.OperationID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s copied changed point: %v", cmd.Kind, err)
		}
	}
}

func TestRestoreRunWithoutManifestBindingRetainsAttentionAndPin(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	importTestBackupCatalog(t, s)
	cmd := protocol.Command{OperationID: "unbound-restore", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "Restored", StorageID: "storage-1", HostID: s.State.HostID}}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: s.State.PoolID,
		HostID: s.State.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: cmd.Restore.NewVMID,
		IntentSHA256: intent, Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	if err := reserveRestoreRunWithPin(s, run, protocol.BackupPinKindNew); err != nil {
		t.Fatal(err)
	}
	run.Status, run.Stage, run.Error = "needs_attention", "placement", "native outcome is uncertain"
	if err := SettlePoolBackupRun(s, run, nil); err != nil {
		t.Fatal(err)
	}
	// The retained native journal is what makes the outcome uncertain.
	if err := os.MkdirAll(filepath.Join(s.dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "managed-vms", cmd.OperationID+".json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := reconcileRestoreAsNewAttention(context.Background(), s.dir, s.State, cmd); err != nil ||
		result.Status != "needs_attention" || result.Error != ErrBackupConflict.Error() {
		t.Fatalf("unbound run resumed: %+v err=%v", result, err)
	}
	if result, err := executeRestoreAsNew(context.Background(), s, cmd, time.Now().UTC()); err != nil || result.Status != "needs_attention" {
		t.Fatalf("unbound run reexecuted: %+v err=%v", result, err)
	}
	if pins, err := ActiveBackupRestorePins(s); err != nil || !pins["point-1"] {
		t.Fatalf("unbound uncertainty lost its point pin: %+v err=%v", pins, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "backup-restore", cmd.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unbound run copied artifacts: %v", err)
	}
}

func stubReplacePoolOwner(t *testing.T) {
	t.Helper()
	reserve, commit := reserveReplacePoolOwner, commitReplacePoolOwner
	reserveReplacePoolOwner = func(context.Context, *Store, string, protocol.VMCreateSpec, protocol.VMCreateSpec) error { return nil }
	commitReplacePoolOwner = func(context.Context, *Store, *vmhost.ManagedEngine, string, string) error { return nil }
	t.Cleanup(func() { reserveReplacePoolOwner, commitReplacePoolOwner = reserve, commit })
}

func TestBackupRetentionRemovesPayloadAfterPublicationAndHonorsPin(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-a")
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-b")
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	tagCatalogPolicy(t, s, "repo-1", "policy-1", "point-a", "point-b")
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{
		RecoveryPointID: "point-a", OperationID: "restore-a", Kind: protocol.BackupPinKindNew, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := prunePublishedBackupPayloads(context.Background(), s, "repo-1", "vm-1", "policy-1", protocol.BackupRetention{KeepLatest: 0}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mount, "point-a")); err != nil {
		t.Fatalf("pinned payload was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mount, "point-b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unprotected payload was retained: %v", err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-b"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("pruned payload remains cataloged: %v", err)
	}
}

func TestBackupVerificationRequiresMatchingSuccessfulTestRun(t *testing.T) {
	s := seedBackupMaster(t)
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	point := protocol.BackupRecoveryPoint{
		ID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1", SourceVMID: "vm-1",
		CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: strings.Repeat("a", 64),
	}
	if err := savePoolBackupCatalogEntry(s, point); err != nil {
		t.Fatal(err)
	}
	req := protocol.TestRestoreVerifyRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: vm.ID, Result: protocol.BackupTestResultPassed,
	}
	if _, err := prepareTestRestoreVerification(s, req); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("verification without a matching restore passed: %v", err)
	}
	if err := savePoolBackupRun(s, protocol.BackupRun{
		OperationID: "test-1", Kind: protocol.BackupKindTestRestore, PoolID: "pool-1",
		RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: vm.ID, HostID: vm.HostID,
		Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, CreateOperationID: "test-1", State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareTestRestoreVerification(s, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecordTestRestoreVerification(s, plan); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("verification accepted without native proof: %v", err)
	}
	plan.VerifiedAt = time.Now().UTC()
	if _, err := RecordTestRestoreVerification(s, plan); err != nil {
		t.Fatal(err)
	}
	for _, creator := range []string{"other-create", ""} {
		state, err := loadPoolVMs(s)
		if err != nil {
			t.Fatal(err)
		}
		record := state.VMs[vm.ID]
		record.CreateOperationID = creator
		state.VMs[vm.ID] = record
		if err := savePoolVMs(s, state); err != nil {
			t.Fatal(err)
		}
		if _, err := RecordTestRestoreVerification(s, plan); !errors.Is(err, ErrBackupConflict) {
			t.Fatalf("verification accepted creator %q: %v", creator, err)
		}
		if _, err := prepareTestRestoreCleanup(s, protocol.TestRestoreCleanupRequest{
			RepositoryID: req.RepositoryID, RecoveryPointID: req.RecoveryPointID, TestVMID: req.TestVMID,
		}); !errors.Is(err, ErrBackupConflict) {
			t.Fatalf("cleanup accepted creator %q: %v", creator, err)
		}
	}
}

func TestMemberTestVerificationBindsNativeProofAndRun(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", member.State.HostID)
	run := protocol.BackupRun{OperationID: "test-restore-op", Kind: protocol.BackupKindTestRestore,
		PoolID: vm.PoolID, HostID: vm.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1",
		NewVMID: vm.ID, IntentSHA256: strings.Repeat("a", 64), Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC()}
	for _, store := range []*Store{master, member} {
		if err := savePoolBackupRun(store, run); err != nil {
			t.Fatal(err)
		}
	}
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, CreateOperationID: run.OperationID, State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	point := protocol.BackupRecoveryPoint{ID: "point-1", RepositoryID: "repo-1", PoolID: vm.PoolID,
		SourceVMID: "source-vm", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: strings.Repeat("b", 64)}
	if err := savePoolBackupCatalogEntry(master, point); err != nil {
		t.Fatal(err)
	}
	req := protocol.TestRestoreVerifyRequest{RepositoryID: run.RepositoryID, RecoveryPointID: run.RecoveryPointID,
		TestVMID: vm.ID, Result: protocol.BackupTestResultPassed}
	plan, err := prepareTestRestoreVerification(master, req)
	if err != nil {
		t.Fatal(err)
	}
	priorVerify := verifyTestRestoredVM
	verified := make(chan bool, 3)
	verifyTestRestoredVM = func(_ *vmhost.ManagedEngine, _ context.Context, op string, spec protocol.VMCreateSpec, passed bool) error {
		if op != run.OperationID || !reflect.DeepEqual(spec, vm) {
			return ErrBackupConflict
		}
		verified <- passed
		return nil
	}
	t.Cleanup(func() { verifyTestRestoredVM = priorVerify })
	if err := verifyTestRestoreNative(context.Background(), master, plan); err != nil {
		t.Fatalf("authenticated native proof: %v", err)
	}
	select {
	case passed := <-verified:
		if !passed {
			t.Fatal("passed result did not require full native proof")
		}
	case <-time.After(time.Minute):
		t.Fatal("member did not observe native test VM")
	}
	plan.VerifiedAt = time.Now().UTC()
	if _, err := RecordTestRestoreVerification(master, plan); err != nil {
		t.Fatal(err)
	}
	failedPlan := plan
	failedPlan.Request.Result = protocol.BackupTestResultFailed
	if err := verifyTestRestoreNative(context.Background(), master, failedPlan); err != nil {
		t.Fatalf("failed result native proof: %v", err)
	}
	select {
	case passed := <-verified:
		if passed {
			t.Fatal("failed result requested full native proof")
		}
	case <-time.After(time.Minute):
		t.Fatal("member did not observe failed test result")
	}
	changed := run
	changed.NewVMID = "22222222-2222-4222-8222-222222222222"
	if err := savePoolBackupRun(member, changed); err != nil {
		t.Fatal(err)
	}
	if err := verifyTestRestoreNative(context.Background(), master, plan); err == nil || len(verified) != 0 {
		t.Fatalf("mismatched member run supplied native proof: %v", err)
	}
}

func TestTestRestoreCleanupReconcilesLostNativeDeleteReply(t *testing.T) {
	s := seedBackupMaster(t)
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, CreateOperationID: "test-restore-op", State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: "test-restore-op", Kind: protocol.BackupKindTestRestore,
		PoolID: vm.PoolID, HostID: vm.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1",
		NewVMID: vm.ID, Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{
		RecoveryPointID: "point-1", OperationID: run.OperationID,
		Kind: protocol.BackupPinKindTest, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	req := protocol.TestRestoreCleanupRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: vm.ID}
	cmd := protocol.Command{OperationID: "cleanup-op", Kind: "backup-test-cleanup", TestRestoreClean: &req}
	if err := reserveBackupMetadataReceipt(s, cmd, false); err != nil {
		t.Fatal(err)
	}
	// Native exact-delete replay is tested by vmhost; Core must settle the
	// owner record and receipt even when the first success reply was lost.
	previousCleanup := cleanupTestRestoredVM
	cleanupTestRestoredVM = func(_ *vmhost.ManagedEngine, _ context.Context, createID string, spec protocol.VMCreateSpec, deleteID string) (protocol.Result, error) {
		if createID != run.OperationID || deleteID == "" || spec.ID != vm.ID || spec.PoolID != vm.PoolID ||
			spec.HostID != vm.HostID || spec.Name != "" && !reflect.DeepEqual(spec, vm) {
			return protocol.Result{}, ErrBackupConflict
		}
		return protocol.Result{OperationID: deleteID, Status: "succeeded", ObservedAt: time.Now().UTC()}, nil
	}
	t.Cleanup(func() { cleanupTestRestoredVM = previousCleanup })
	for i := 0; i < 2; i++ {
		plan, err := prepareTestRestoreCleanup(s, req)
		if err != nil {
			t.Fatalf("retry %d prepare: %v", i, err)
		}
		if err := executeTestRestoreCleanupNative(context.Background(), s.dir, plan); err != nil {
			t.Fatalf("retry %d native replay: %v", i, err)
		}
		if err := finishTestRestoreCleanup(s, plan); err != nil {
			t.Fatalf("retry %d owner settlement: %v", i, err)
		}
	}
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd,
		PreparedCleanup: &testRestoreCleanupPlan{Request: req, RunID: run.OperationID, VM: vm},
	}); response.Err != nil || response.Result == nil || response.Result.Status != "succeeded" {
		t.Fatalf("receipt settlement: %+v", response)
	}
	state, err := loadPoolVMs(s)
	if err != nil || len(state.VMs) != 0 {
		t.Fatalf("test VM remained in pool registry: %+v %v", state, err)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || pins[req.RecoveryPointID] {
		t.Fatalf("test restore pin remained after cleanup: %+v %v", pins, err)
	}
}

func TestCleanupOfFailedTestRestoreReleasesPin(t *testing.T) {
	s := seedBackupMaster(t)
	run := protocol.BackupRun{OperationID: "test-restore-op", Kind: protocol.BackupKindTestRestore, PoolID: "pool-1",
		HostID: "member-1", RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: "never-created-vm",
		Stage: "not_started", Status: "failed", Error: "member never started this operation for 10m0s: member unreachable", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{RecoveryPointID: "point-1", OperationID: run.OperationID,
		Kind: protocol.BackupPinKindTest, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareTestRestoreCleanup(s, protocol.TestRestoreCleanupRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: run.NewVMID})
	if err != nil {
		t.Fatal(err)
	}
	if err := executeTestRestoreCleanupNative(context.Background(), s.dir, plan); err != nil {
		t.Fatal(err)
	}
	if err := finishTestRestoreCleanup(s, plan); err != nil {
		t.Fatal(err)
	}
	if pins, err := ActiveBackupRestorePins(s); err != nil || pins["point-1"] {
		t.Fatalf("pin remained: %v %v", pins, err)
	}
}

func TestTestRestoreCleanupRefusesReusedPoolVMAtSettlement(t *testing.T) {
	s := seedBackupMaster(t)
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	run := protocol.BackupRun{OperationID: "test-restore-op", Kind: protocol.BackupKindTestRestore,
		PoolID: vm.PoolID, HostID: vm.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1",
		NewVMID: vm.ID, Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	state := poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, CreateOperationID: run.OperationID,
		State: "stopped", Spec: vm,
	}}}
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{
		RecoveryPointID: run.RecoveryPointID, OperationID: run.OperationID,
		Kind: protocol.BackupPinKindTest, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareTestRestoreCleanup(s, protocol.TestRestoreCleanupRequest{
		RepositoryID: run.RepositoryID, RecoveryPointID: run.RecoveryPointID, TestVMID: vm.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	reused := state.VMs[vm.ID]
	reused.CreateOperationID = "other-create"
	state.VMs[vm.ID] = reused
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	if err := finishTestRestoreCleanup(s, plan); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("settlement removed reused VM: %v", err)
	}
	got, err := loadPoolVMs(s)
	if err != nil || got.VMs[vm.ID].CreateOperationID != "other-create" {
		t.Fatalf("reused VM record changed: %+v %v", got, err)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || !pins[run.RecoveryPointID] {
		t.Fatalf("restore pin lost after refused cleanup: %+v %v", pins, err)
	}
}

func TestTestRestorePreEffectPlacementRefusalFailsAndReleasesPin(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	importTestBackupCatalog(t, s)
	cmd := protocol.Command{OperationID: "test-placement-op", Kind: protocol.BackupKindTestRestore,
		TestRestore: &protocol.TestRestoreRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "test-restore", StorageID: "storage-unavailable", HostID: s.State.HostID}}
	result, err := executeTestRestore(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "failed" {
		t.Fatalf("pre-effect placement refusal must fail: %+v %v", result, err)
	}
	run, err := loadPoolBackupRun(s, cmd.OperationID)
	if err != nil || run.Status != "failed" || run.Stage != "failed" || run.Error == "" {
		t.Fatalf("run lost placement refusal: %+v %v", run, err)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || pins["point-1"] {
		t.Fatalf("restore pin retained after proven refusal: %+v %v", pins, err)
	}
}

func TestRestoreAsNewPreEffectPlacementRefusalFailsAndReleasesPin(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	importTestBackupCatalog(t, s)
	cmd := protocol.Command{OperationID: "restore-placement-refused", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-unavailable", HostID: s.State.HostID}}
	result, err := executeRestoreAsNew(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "failed" || result.Error == "" {
		t.Fatalf("pre-effect restore placement refusal must fail with reason: %+v %v", result, err)
	}
	run, err := loadPoolBackupRun(s, cmd.OperationID)
	if err != nil || run.Status != "failed" || run.Stage != "failed" || run.Error != result.Error {
		t.Fatalf("run lost definite refusal: %+v %v", run, err)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || pins["point-1"] {
		t.Fatalf("pin remained after definite refusal: %+v %v", pins, err)
	}
}

func TestRestoreAsNewNativeCapacityRefusalReleasesExactOwner(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	importTestBackupCatalog(t, s)
	managedDir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(managedDir, 0700); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "storage-1", PoolID: s.State.PoolID, HostID: s.State.HostID,
		Kind: protocol.StorageKindLocal, Generation: 1, Path: t.TempDir()}
	storageJournal, err := json.Marshal(struct {
		Command        protocol.Command              `json:"command"`
		AppliedStorage *protocol.StorageRegistration `json:"applied_storage"`
		Result         protocol.Result               `json:"result"`
	}{protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &storage}, &storage,
		protocol.Result{OperationID: "storage-1", Status: "succeeded", ObservedAt: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managedDir, "storage-1.json"), storageJournal, 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\ncase \"$3\" in list) exit 0;; *) exit 1;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	previousPlacement, previousNative := requestBackupRestorePlacement, registerBackupRestoredAsNew
	markOwner := true
	requestBackupRestorePlacement = func(_ context.Context, store *Store, spec protocol.VMCreateSpec, operationID string) error {
		if !markOwner {
			operationID = ""
		}
		return savePoolVMs(store, poolVMState{VMs: map[string]poolVMRecord{spec.ID: {
			PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID, CreateOperationID: operationID,
			State: "defined", Epoch: 1, Spec: spec,
		}}, Inventory: map[string]poolVMInventory{}})
	}
	registerBackupRestoredAsNew = func(context.Context, *vmhost.ManagedEngine, string, protocol.VMCreateSpec, string, string, []protocol.BackupDiskArtifact, ...protocol.BackupFirmwareArtifact) error {
		return errors.New("dedicated memory exceeds what this host has left to promise")
	}
	t.Cleanup(func() { requestBackupRestorePlacement, registerBackupRestoredAsNew = previousPlacement, previousNative })
	cmd := protocol.Command{OperationID: "restore-capacity-refused", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1", HostID: s.State.HostID}}
	result, err := executeRestoreAsNew(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "dedicated memory") {
		t.Fatalf("definite native capacity refusal must fail with original reason: %+v %v", result, err)
	}
	state, err := loadPoolVMs(s)
	if err != nil || len(state.VMs) != 0 {
		t.Fatalf("failed restore kept its exact owner row: %+v %v", state, err)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || pins["point-1"] {
		t.Fatalf("pin remained after proven native refusal: %+v %v", pins, err)
	}
	markOwner = false
	unproven := protocol.Command{OperationID: "restore-capacity-unproven", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "33333333-3333-4333-8333-333333333333", NewVMName: "unproven-owner", StorageID: "storage-1", HostID: s.State.HostID}}
	result, err = executeRestoreAsNew(context.Background(), s, unproven, time.Now().UTC())
	if err != nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "dedicated memory") {
		t.Fatalf("unproven owner must retain uncertainty and cause: %+v %v", result, err)
	}
	pins, err = ActiveBackupRestorePins(s)
	if err != nil || !pins["point-1"] {
		t.Fatalf("unproven owner lost pin: %+v %v", pins, err)
	}
	reconciled, err := reconcileRestoreAsNewAttention(context.Background(), s.dir, s.State, unproven)
	if err != nil || reconciled.Status != "needs_attention" || !strings.Contains(reconciled.Error, "dedicated memory") {
		t.Fatalf("observation erased original capacity refusal: %+v %v", reconciled, err)
	}
}

func TestDistinctRestoreTargetsSharePointPinsAndSameTargetConflicts(t *testing.T) {
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	seedTestCatalogPoint(t, s)
	makeRestore := func(operationID, vmID string) protocol.Command {
		return protocol.Command{OperationID: operationID, Kind: protocol.BackupKindRestoreNew,
			Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
				NewVMID: vmID, NewVMName: operationID, StorageID: "storage-1", HostID: s.State.HostID}}
	}
	first := makeRestore("restore-first", "22222222-2222-4222-8222-222222222222")
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &first}); response.Err != nil {
		t.Fatalf("first restore admission: %+v", response)
	}
	run, err := loadPoolBackupRun(s, first.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Stage, run.Error = "needs_attention", "placement", "native placement unresolved"
	if err := SettlePoolBackupRun(s, run, nil); err != nil {
		t.Fatal(err)
	}
	entry := s.State.Journal[first.OperationID]
	entry.Status, entry.Error = "needs_attention", run.Error
	s.State.Journal[first.OperationID] = entry
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	// Promotion transfers the run and pin, while the new master's journal does
	// not contain the old master's execution command.
	delete(s.State.Journal, first.OperationID)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &first}); response.Err != nil || response.Result == nil || response.Result.Status != "needs_attention" {
		t.Fatalf("promoted master replayed retained restore: %+v", response)
	}
	if _, exists := s.State.Journal[first.OperationID]; exists {
		t.Fatal("exact retry recreated a native restore command after promotion")
	}
	changed := makeRestore(first.OperationID, "44444444-4444-4444-8444-444444444444")
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &changed}); !errors.Is(response.Err, ErrBackupConflict) {
		t.Fatalf("changed intent reused retained restore operation: %+v", response)
	}
	pin := protocol.BackupRestorePin{RecoveryPointID: "point-1", OperationID: first.OperationID,
		Kind: protocol.BackupPinKindNew, CreatedAt: time.Now().UTC()}
	if err := savePoolBackupPins(s, poolBackupPinsState{Pins: map[string]protocol.BackupRestorePin{first.OperationID: pin}}); err != nil {
		t.Fatal(err)
	}
	second := makeRestore("restore-second", "33333333-3333-4333-8333-333333333333")
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &second}); response.Err != nil {
		t.Fatalf("distinct target blocked by another restore pin: %+v", response)
	}
	state, err := loadPoolBackupPins(s)
	if err != nil || len(state.Pins) != 2 || state.Pins[first.OperationID].OperationID != first.OperationID ||
		state.Pins[second.OperationID].OperationID != second.OperationID {
		t.Fatalf("restore pins not retained separately: %+v %v", state, err)
	}
	conflict := makeRestore("restore-same-target", first.Restore.NewVMID)
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &conflict}); !errors.Is(response.Err, ErrBackupConflict) {
		t.Fatalf("same target was not fenced: %+v", response)
	}
	if err := clearBackupRestorePin(s, "point-1", first.OperationID); err != nil {
		t.Fatal(err)
	}
	if pins, err := ActiveBackupRestorePins(s); err != nil || !pins["point-1"] {
		t.Fatalf("clearing one restore dropped the other pin: %+v %v", pins, err)
	}
	if err := clearBackupRestorePin(s, "point-1", second.OperationID); err != nil {
		t.Fatal(err)
	}
	if pins, err := ActiveBackupRestorePins(s); err != nil || pins["point-1"] {
		t.Fatalf("last exact pin remained: %+v %v", pins, err)
	}
}

func TestBackupPinStoreRejectsCorruptKeysAndDuplicateOperations(t *testing.T) {
	s := seedBackupMaster(t)
	pin := protocol.BackupRestorePin{RecoveryPointID: "point-1", OperationID: "restore-1",
		Kind: protocol.BackupPinKindNew, CreatedAt: time.Now().UTC()}
	for name, pins := range map[string]map[string]protocol.BackupRestorePin{
		"mismatched key":      {"other-key": pin},
		"duplicate operation": {"point-1": pin, "restore-1": pin},
		"invalid pin":         {"restore-1": {RecoveryPointID: pin.RecoveryPointID, OperationID: pin.OperationID, Kind: pin.Kind}},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(poolBackupPinsState{Pins: pins})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(poolBackupPinsPath(s), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadPoolBackupPins(s); err == nil {
				t.Fatal("corrupt persisted restore pins were accepted")
			}
		})
	}
}

func TestTestRestoreInterruptedNativeDefineRetainsRunAndPin(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	importTestBackupCatalog(t, s)
	previousPlacement, previousNative := requestTestRestorePlacement, registerTestRestoredAsNew
	requestTestRestorePlacement = func(context.Context, *Store, protocol.VMCreateSpec, string) error { return nil }
	registerTestRestoredAsNew = func(context.Context, *vmhost.ManagedEngine, string, protocol.VMCreateSpec, string, string, []protocol.BackupDiskArtifact, ...protocol.BackupFirmwareArtifact) error {
		return vmhost.ErrRestorePlacementUncertain
	}
	t.Cleanup(func() { requestTestRestorePlacement, registerTestRestoredAsNew = previousPlacement, previousNative })
	cmd := protocol.Command{OperationID: "test-define-interrupted", Kind: protocol.BackupKindTestRestore,
		TestRestore: &protocol.TestRestoreRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "33333333-3333-4333-8333-333333333333", NewVMName: "test-restore", StorageID: "storage-1", HostID: s.State.HostID}}
	result, err := executeTestRestore(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "needs_attention" {
		t.Fatalf("interrupted native define must remain uncertain: %+v %v", result, err)
	}
	run, err := loadPoolBackupRun(s, cmd.OperationID)
	if err != nil || run.Status != "needs_attention" || run.Stage != "placement" {
		t.Fatalf("run lost interrupted define: %+v %v", run, err)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || !pins["point-1"] {
		t.Fatalf("pin cleared after interrupted native define: %+v %v", pins, err)
	}
}

func TestRestoreReservationLateErrorRetainsPin(t *testing.T) {
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	seedTestCatalogPoint(t, s)
	previous := reserveBackupRun
	reserveBackupRun = func(store *Store, run protocol.BackupRun) (protocol.BackupRun, error) {
		if _, err := previous(store, run); err != nil {
			return protocol.BackupRun{}, err
		}
		return protocol.BackupRun{}, errors.New("simulated failed durability acknowledgment")
	}
	t.Cleanup(func() { reserveBackupRun = previous })
	cmd := protocol.Command{OperationID: "restore-late-error", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1", HostID: s.State.HostID}}
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if response.Err == nil {
		t.Fatalf("expected simulated reservation failure: %+v", response)
	}
	run, err := loadPoolBackupRun(s, cmd.OperationID)
	if err != nil || run.Status != "pending" {
		t.Fatalf("durable run disappeared: %+v %v", run, err)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || !pins["point-1"] {
		t.Fatalf("pin cleared despite uncertain reservation: %+v %v", pins, err)
	}
}

func TestRestoreReservationRefusalDoesNotLeakPin(t *testing.T) {
	for _, collision := range []bool{false, true} {
		name := "write refused"
		if collision {
			name = "operation collision"
		}
		t.Run(name, func(t *testing.T) {
			s := seedBackupMaster(t)
			if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
				ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
				Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
			}); err != nil {
				t.Fatal(err)
			}
			seedTestCatalogPoint(t, s)
			cmd := protocol.Command{OperationID: "restore-reserve-refused", Kind: protocol.BackupKindRestoreNew,
				Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
					NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1", HostID: s.State.HostID}}
			if collision {
				if _, err := ReservePoolBackupRun(s, protocol.BackupRun{
					OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreNew, PoolID: s.State.PoolID,
					HostID: s.State.HostID, RepositoryID: "repo-1", RecoveryPointID: "other-point",
					NewVMID: cmd.Restore.NewVMID, IntentSHA256: strings.Repeat("a", 64),
					Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC(),
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				previous := reserveBackupRun
				reserveBackupRun = func(*Store, protocol.BackupRun) (protocol.BackupRun, error) {
					return protocol.BackupRun{}, errors.New("simulated prewrite refusal")
				}
				t.Cleanup(func() { reserveBackupRun = previous })
			}
			if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd}); response.Err == nil {
				t.Fatal("restore reservation refusal was accepted")
			}
			pins, err := ActiveBackupRestorePins(s)
			if err != nil || pins["point-1"] {
				t.Fatalf("refused restore leaked pin: %+v %v", pins, err)
			}
		})
	}
}

func TestBackupRestoreRefusesManifestPointAlias(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-real")
	if err := os.Symlink(filepath.Join(mount, "point-real"), filepath.Join(mount, "point-alias")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (filesystemBackupStorage{repo: repo}).StatPoint(context.Background(), "point-alias"); err == nil {
		t.Fatalf("alias must not select a different recovery point: %v", err)
	}
}

func TestRestoreReplaceRefusesUnsafeInPlace(t *testing.T) {
	if err := refuseInPlaceOverwrite("/var/lib/nex/vm/vda.qcow2", "/var/lib/nex/vm"); err == nil {
		t.Fatal("in-place overwrite must be refused")
	}
	if err := refuseInPlaceOverwrite("/var/lib/nex/vm/vda.qcow2", "/var/lib/nex/backup-restore/op-1"); err != nil {
		t.Fatalf("distinct restore dir must be allowed: %v", err)
	}
	req := protocol.RestoreReplaceRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", OriginalVMID: "vm-1",
		StorageID: "storage-1", ConfirmReplace: true,
	}
	if reason := protocol.PreflightRestoreReplaceReason(req, "running", true, false); reason == "" {
		t.Fatal("running guest must be refused")
	}
}

func TestRestoreAsNewDoesNotSucceedWithoutCapturedSpec(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPointWithoutSpec(t, mount, "repo-1", "pool-1", "point-nospec")
	importTestBackupCatalog(t, s)
	cmd := protocol.Command{
		OperationID: "restore-nospec", Kind: "backup-restore-as-new",
		Restore: &protocol.RestoreAsNewRequest{
			RepositoryID: "repo-1", RecoveryPointID: "point-nospec",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1",
		},
	}
	result, err := executeBackupCommand(context.Background(), s.dir, s.State, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status == "succeeded" {
		t.Fatalf("restore-as-new must not succeed without captured spec: %#v", result)
	}
	if result.Status != "failed" {
		t.Fatalf("want failed, got %#v", result)
	}
}

func seedPublishedPointWithoutSpec(t *testing.T, mount, repoID, poolID, pointID string) {
	t.Helper()
	if err := vmhost.ClaimBackupRepositoryOwner(mount, repoID, poolID); err != nil {
		_ = err
	}
	diskPath := filepath.Join(t.TempDir(), "vda.qcow2")
	payload := []byte("point-data")
	if err := os.WriteFile(diskPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: pointID, RepositoryID: repoID,
		PoolID: poolID, SourceVMID: "vm-1", SourceVMName: "app", CapturedAt: time.Now().UTC().Truncate(time.Second),
		Consistency: protocol.BackupCaptureStopped, SpecSHA256: strings.Repeat("a", 64),
		DiskIdentitySHA256: strings.Repeat("b", 64),
		Disks: []protocol.BackupDiskArtifact{{
			Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:]),
		}},
		Complete: true, Success: 1,
	}
	if _, err := vmhost.PublishBackupRecoveryPoint(mount, manifest, map[string]string{"vda.qcow2": diskPath}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreReplaceCutoverSettlesSucceeded(t *testing.T) {
	stubReplacePoolOwner(t)
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1", "11111111-1111-4111-8111-111111111111")
	importTestBackupCatalog(t, s)
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	vm.Disks[0].StorageID = "storage-1"
	original := filepath.Join(t.TempDir(), "disk-0.qcow2")
	if err := os.WriteFile(original, []byte("live-original"), 0600); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestVMJournalWithDisk(t, s, vm, original)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\necho 'shut off'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{
		RecoveryPointID: "point-1", OperationID: "replace-ok",
		Kind: protocol.BackupPinKindReplace, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	prev := applyExclusiveReplaceCutover
	applyExclusiveReplaceCutover = func(_ context.Context, _ *vmhost.ManagedEngine, operationID string, spec protocol.VMCreateSpec, localDir string, artifacts []protocol.BackupDiskArtifact, firmware ...protocol.BackupFirmwareArtifact) error {
		if operationID != "replace-ok" || spec.ID != vm.ID || len(spec.NICs) != 0 || len(artifacts) == 0 || localDir == "" {
			t.Fatalf("unexpected cutover args: op=%s spec=%#v local=%s artifacts=%#v", operationID, spec, localDir, artifacts)
		}
		return nil
	}
	t.Cleanup(func() { applyExclusiveReplaceCutover = prev })
	cmd := protocol.Command{
		OperationID: "replace-ok", Kind: protocol.BackupKindRestoreReplace,
		RestoreReplace: &protocol.RestoreReplaceRequest{
			RepositoryID: "repo-1", RecoveryPointID: "point-1",
			OriginalVMID: vm.ID, StorageID: "storage-1", ConfirmReplace: true,
		},
	}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatal(err)
	}
	result, err := executeBackupCommand(context.Background(), s.dir, s.State, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" {
		t.Fatalf("cutover success must settle succeeded: %#v", result)
	}
	run, loadErr := loadPoolBackupRun(s, "replace-ok")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if run.Stage != "replaced" || run.Status != "succeeded" {
		t.Fatalf("run must settle replaced/succeeded: %#v", run)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("original disk must remain for recover-original: %v", err)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || pins["point-1"] {
		t.Fatalf("restore pin must clear after succeeded cutover: %#v err=%v", pins, err)
	}
}

func TestRestoreReplacePreCutoverFailureKeepsOriginals(t *testing.T) {
	stubReplacePoolOwner(t)
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1", "11111111-1111-4111-8111-111111111111")
	importTestBackupCatalog(t, s)
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	vm.Disks[0].StorageID = "storage-1"
	original := filepath.Join(t.TempDir(), "disk-0.qcow2")
	if err := os.WriteFile(original, []byte("live-original"), 0600); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestVMJournalWithDisk(t, s, vm, original)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\necho 'shut off'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev := applyExclusiveReplaceCutover
	applyExclusiveReplaceCutover = func(context.Context, *vmhost.ManagedEngine, string, protocol.VMCreateSpec, string, []protocol.BackupDiskArtifact, ...protocol.BackupFirmwareArtifact) error {
		return errors.New("storage mount identity changed before replace cutover")
	}
	t.Cleanup(func() { applyExclusiveReplaceCutover = prev })
	cmd := protocol.Command{
		OperationID: "replace-pre", Kind: protocol.BackupKindRestoreReplace,
		RestoreReplace: &protocol.RestoreReplaceRequest{
			RepositoryID: "repo-1", RecoveryPointID: "point-1",
			OriginalVMID: vm.ID, StorageID: "storage-1", ConfirmReplace: true,
		},
	}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatal(err)
	}
	result, err := executeBackupCommand(context.Background(), s.dir, s.State, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "needs_attention" {
		t.Fatalf("reserved owner update must remain unresolved until native reconciliation: %#v", result)
	}
	got, err := os.ReadFile(original)
	if err != nil || string(got) != "live-original" {
		t.Fatalf("original disk must stay intact: %q err=%v", got, err)
	}
	run, loadErr := loadPoolBackupRun(s, "replace-pre")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if run.Status == "succeeded" || run.Stage == "replaced" {
		t.Fatalf("pre-cutover failure must not settle replaced: %#v", run)
	}
}

func TestRestoreReplaceRefusesRunningVM(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1", "11111111-1111-4111-8111-111111111111")
	importTestBackupCatalog(t, s)
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	disk := filepath.Join(t.TempDir(), "disk-0.qcow2")
	if err := os.WriteFile(disk, []byte("live"), 0600); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestVMJournalWithDisk(t, s, vm, disk)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\necho 'running'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	called := false
	prev := applyExclusiveReplaceCutover
	applyExclusiveReplaceCutover = func(context.Context, *vmhost.ManagedEngine, string, protocol.VMCreateSpec, string, []protocol.BackupDiskArtifact, ...protocol.BackupFirmwareArtifact) error {
		called = true
		return nil
	}
	t.Cleanup(func() { applyExclusiveReplaceCutover = prev })
	cmd := protocol.Command{
		OperationID: "replace-run", Kind: protocol.BackupKindRestoreReplace,
		RestoreReplace: &protocol.RestoreReplaceRequest{
			RepositoryID: "repo-1", RecoveryPointID: "point-1",
			OriginalVMID: vm.ID, StorageID: "storage-1", ConfirmReplace: true,
		},
	}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatal(err)
	}
	result, err := executeBackupCommand(context.Background(), s.dir, s.State, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !strings.Contains(result.Error, "shut off") {
		t.Fatalf("running VM replace must be refused: %#v", result)
	}
	if called {
		t.Fatal("exclusive cutover must not run for a powered guest")
	}
}

func TestRestoreReplaceRefusesChangedValidCatalogManifestBeforeCopy(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	vmID := "11111111-1111-4111-8111-111111111111"
	seedPublishedPoint(t, mount, repo.ID, s.State.PoolID, "point-1", vmID)
	if _, err := importPoolBackupCatalog(context.Background(), s, repo.ID); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec(vmID, "host-a")
	original := filepath.Join(t.TempDir(), "disk-0.qcow2")
	if err := os.WriteFile(original, []byte("live-original"), 0600); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestVMJournalWithDisk(t, s, vm, original)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\necho 'shut off'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := protocol.Command{OperationID: "replace-changed", Kind: protocol.BackupKindRestoreReplace,
		RestoreReplace: &protocol.RestoreReplaceRequest{RepositoryID: repo.ID, RecoveryPointID: "point-1",
			OriginalVMID: vmID, StorageID: "storage-1", ConfirmReplace: true}}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatal(err)
	}
	replaceCatalogedPointWithValidManifest(t, s, repo, "point-1")
	run, err := loadPoolBackupRun(s, cmd.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Stage, run.Error = "needs_attention", "cutover-ready", "retained cutover requires observation"
	if err := SettlePoolBackupRun(s, run, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeRetainedOriginal(s, retainedOriginalRecord{OperationID: cmd.OperationID, OriginalVMID: vmID,
		RecoveryPointID: "point-1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if observed, err := reconcileRestoreReplaceAttention(context.Background(), s.dir, s.State, cmd); err != nil ||
		observed.Status != "needs_attention" || observed.Error != ErrBackupConflict.Error() {
		t.Fatalf("replace reconciliation accepted changed manifest: %+v err=%v", observed, err)
	}
	result, err := executeRestoreReplace(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "needs_attention" || result.Error != ErrBackupConflict.Error() {
		t.Fatalf("replace accepted changed valid manifest: %+v err=%v", result, err)
	}
	if pins, err := ActiveBackupRestorePins(s); err != nil || !pins["point-1"] {
		t.Fatalf("changed manifest released uncertain replace pin: %+v err=%v", pins, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "backup-restore", cmd.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replace copied changed point: %v", err)
	}
	if contents, err := os.ReadFile(original); err != nil || string(contents) != "live-original" {
		t.Fatalf("replace touched original: %q err=%v", contents, err)
	}
}

func TestRestoreReplaceMidCutoverNeedsAttention(t *testing.T) {
	stubReplacePoolOwner(t)
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1", "11111111-1111-4111-8111-111111111111")
	importTestBackupCatalog(t, s)
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	vm.Disks[0].StorageID = "storage-1"
	original := filepath.Join(t.TempDir(), "disk-0.qcow2")
	if err := os.WriteFile(original, []byte("live-original"), 0600); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestVMJournalWithDisk(t, s, vm, original)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\necho 'shut off'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{
		RecoveryPointID: "point-1", OperationID: "replace-mid",
		Kind: protocol.BackupPinKindReplace, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	prev := applyExclusiveReplaceCutover
	applyExclusiveReplaceCutover = func(context.Context, *vmhost.ManagedEngine, string, protocol.VMCreateSpec, string, []protocol.BackupDiskArtifact, ...protocol.BackupFirmwareArtifact) error {
		return &vmhost.ReplaceCutoverError{Stage: "defining", Err: errors.New("VM definition outcome uncertain; observation required before recovery")}
	}
	t.Cleanup(func() { applyExclusiveReplaceCutover = prev })
	cmd := protocol.Command{
		OperationID: "replace-mid", Kind: protocol.BackupKindRestoreReplace,
		RestoreReplace: &protocol.RestoreReplaceRequest{
			RepositoryID: "repo-1", RecoveryPointID: "point-1",
			OriginalVMID: vm.ID, StorageID: "storage-1", ConfirmReplace: true,
		},
	}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatal(err)
	}
	result, err := executeBackupCommand(context.Background(), s.dir, s.State, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "needs_attention" || result.Status == "succeeded" {
		t.Fatalf("mid-cutover failure must be needs_attention, never succeeded: %#v", result)
	}
	run, loadErr := loadPoolBackupRun(s, "replace-mid")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if run.Stage != "defining" || run.Status != "needs_attention" {
		t.Fatalf("run must name defining stage: %#v", run)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || !pins["point-1"] {
		t.Fatalf("pin must remain while cutover needs attention: %#v err=%v", pins, err)
	}
}

func writeSnapshotTestVMJournalWithDisk(t *testing.T, s *Store, vm *protocol.VMCreateSpec, disk string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"command": protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: vm},
		"result":  protocol.Result{OperationID: "create-1", Status: "succeeded"},
		"stage":   "defined",
		"disks":   []string{disk},
		"targets": []string{"vda"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "create-1.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProtectBlocksPruneAndSurvivesRebuild(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-old")
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-new")
	now := time.Now().UTC()
	old := protocol.BackupRecoveryPoint{
		ID: "point-old", RepositoryID: "repo-1", PoolID: "pool-1", SourceVMID: "vm-1",
		CapturedAt: now.Add(-48 * time.Hour), Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: strings.Repeat("d", 64), Protected: true,
	}
	newer := protocol.BackupRecoveryPoint{
		ID: "point-new", RepositoryID: "repo-1", PoolID: "pool-1", SourceVMID: "vm-1",
		CapturedAt: now.Add(-time.Hour), Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: strings.Repeat("e", 64),
	}
	// Re-read real digests from published points for Valid().
	for _, id := range []string{"point-old", "point-new"} {
		_, digest, err := vmhost.ReadBackupManifest(filepath.Join(mount, id))
		if err != nil {
			t.Fatal(err)
		}
		if id == "point-old" {
			old.ManifestSHA256 = digest
		} else {
			newer.ManifestSHA256 = digest
		}
	}
	if err := savePoolBackupCatalogEntry(s, old); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupCatalogEntry(s, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-old", Protected: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{
		RecoveryPointID: "point-new", OperationID: "restore-1",
		Kind: protocol.BackupPinKindNew, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil || !pins["point-new"] {
		t.Fatalf("pin missing: %#v err=%v", pins, err)
	}
	// Clear catalog files and rebuild from manifests; protect marker + pin must remain.
	_ = os.Remove(filepath.Join(poolBackupCatalogPath(s), "point-old.json"))
	_ = os.Remove(filepath.Join(poolBackupCatalogPath(s), "point-new.json"))
	imported, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1")
	if err != nil || len(imported) < 2 {
		t.Fatalf("rebuild: %#v err=%v", imported, err)
	}
	point, err := getCatalogPoint(s, "repo-1", "point-old")
	if err != nil || !point.Protected {
		t.Fatalf("protect must survive rebuild: %#v err=%v", point, err)
	}
	pinsAfter, err := ActiveBackupRestorePins(s)
	if err != nil || !pinsAfter["point-new"] {
		t.Fatalf("pin must survive rebuild: %#v err=%v", pinsAfter, err)
	}
	if !vmhost.ReadBackupProtectMarker(filepath.Join(mount, "point-old")) {
		t.Fatal("repository protect marker missing after rebuild")
	}
}

func TestCatalogRebuildImportsValidatedManifestsOnly(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-ok")
	badDir := filepath.Join(mount, "point-bad")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "manifest.json"), []byte(`{"version":1,"complete":false}`), 0644); err != nil {
		t.Fatal(err)
	}
	imported, err := importPoolBackupCatalog(context.Background(), s, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0].ID != "point-ok" {
		t.Fatalf("only validated point must import: %#v", imported)
	}
	raw, _ := json.Marshal(imported[0])
	if !strings.Contains(string(raw), "point-ok") {
		t.Fatal("unexpected catalog payload")
	}
}

func TestCatalogRebuildRetriesPendingReceipt(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-ok")
	cmd := protocol.Command{OperationID: "rebuild-retry", Kind: "backup-catalog-rebuild",
		CatalogImport: &protocol.CatalogImportRequest{RepositoryID: "repo-1"}}
	reserved := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd, ReserveOnly: true})
	if reserved.Err != nil || reserved.Result == nil || reserved.Result.Status != "pending" {
		t.Fatalf("reserve rebuild: %+v", reserved)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	settleBackupMetadataResponse(s, cmd, receipt, BackupResponse{Err: ErrBackupUnavailable})
	status := acceptBackupRequestUnrecorded(context.Background(), s, BackupRequest{OperationID: cmd.OperationID})
	if status.Err != nil || status.Result == nil || status.Result.Status != "pending" || status.Result.Error == "" {
		t.Fatalf("reserved rebuild status: %+v", status)
	}
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if response.Err != nil || response.Result == nil || response.Result.Status != "succeeded" ||
		len(response.Catalog) != 1 || response.Catalog[0].ID != "point-ok" {
		t.Fatalf("pending rebuild did not resume: %+v", response)
	}
	replay := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if replay.Err != nil || replay.Result == nil || replay.Result.Status != "succeeded" {
		t.Fatalf("settled rebuild did not replay: %+v", replay)
	}
}

func TestCatalogRebuildLocalRetrySettlesControllerJournal(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-ok")
	cmd := protocol.Command{OperationID: "rebuild-controller-retry", Kind: "backup-catalog-rebuild",
		CatalogImport: &protocol.CatalogImportRequest{RepositoryID: "repo-1"}}
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd, ReserveOnly: true}); response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
		t.Fatalf("reserve: %+v", response)
	}
	s.State.Journal[cmd.OperationID] = JournalEntry{Status: "needs_attention", Error: "corrupt artifact",
		Command: &cmd, ObservedAt: time.Now().UTC()}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if response.Err != nil || response.Result == nil || response.Result.Status != "succeeded" {
		t.Fatalf("repaired Core-local retry: %+v", response)
	}
	entry := s.State.Journal[cmd.OperationID]
	if entry.Status != "succeeded" || entry.Error != "" {
		t.Fatalf("Controller journal did not settle: %+v", entry)
	}
	status := acceptBackupRequestUnrecorded(context.Background(), s, BackupRequest{OperationID: cmd.OperationID})
	if status.Err != nil || status.Result == nil || status.Result.Status != "succeeded" {
		t.Fatalf("Core operation status stayed stale: %+v", status)
	}
	outcomes := commandOutcomes(s.State.Journal, nil)
	if len(outcomes) != 1 || outcomes[0].OperationID != cmd.OperationID || outcomes[0].Status != "succeeded" {
		t.Fatalf("Core outcome projection: %+v", outcomes)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.dir)
	if err == nil {
		defer reopened.Close()
	}
	if err != nil || reopened.State.Journal[cmd.OperationID].Status != "succeeded" {
		t.Fatalf("durable Controller journal: %+v %v", reopened, err)
	}
}

func TestCatalogRebuildReceiptReconcilesStaleJournalAfterRestart(t *testing.T) {
	s := seedBackupMaster(t)
	cmd := protocol.Command{OperationID: "rebuild-crash-gap", Kind: "backup-catalog-rebuild",
		CatalogImport: &protocol.CatalogImportRequest{RepositoryID: "repo-1"}}
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd, ReserveOnly: true}); response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
		t.Fatalf("reserve: %+v", response)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status, receipt.Error, receipt.ObservedAt = "succeeded", "", time.Now().UTC()
	if err := savePoolBackupMetadataReceipt(s, receipt); err != nil {
		t.Fatal(err)
	}
	s.State.Journal[cmd.OperationID] = JournalEntry{Status: "needs_attention", Error: "interrupted before journal settlement", Command: &cmd}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// Only the writable pool authority may settle a journal from its receipt.
	transfer := &poolTransferRecord{OperationID: "freeze-journal", Action: "promote", Role: "source",
		Stage: "freeze", Status: "pending", TargetHostID: "member-1", Frozen: true}
	if err := savePoolTransfer(reopened, transfer); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCatalogMetadataJournal(reopened, cmd.OperationID); err == nil ||
		reopened.State.Journal[cmd.OperationID].Status != "needs_attention" {
		t.Fatalf("frozen authority settled journal: %+v %v", reopened.State.Journal[cmd.OperationID], err)
	}
	transfer.Stage, transfer.Status, transfer.Frozen = "aborted", "failed", false
	if err := savePoolTransfer(reopened, transfer); err != nil {
		t.Fatal(err)
	}
	if err := reconcileCatalogMetadataJournal(reopened, cmd.OperationID); err != nil {
		t.Fatal(err)
	}
	if reopened.State.Journal[cmd.OperationID].Status != "succeeded" {
		t.Fatal("durable receipt did not settle stale journal")
	}
	entry := reopened.State.Journal[cmd.OperationID]
	entry.Status = "failed"
	entry.Error = "terminal result"
	reopened.State.Journal[cmd.OperationID] = entry
	if err := reconcileCatalogMetadataJournal(reopened, cmd.OperationID); err != nil || reopened.State.Journal[cmd.OperationID].Status != "failed" {
		t.Fatalf("terminal result was changed: %+v %v", reopened.State.Journal[cmd.OperationID], err)
	}
	entry.Status = "needs_attention"
	wrong := cmd
	wrong.CatalogImport = &protocol.CatalogImportRequest{RepositoryID: "repo-other"}
	entry.Command = &wrong
	reopened.State.Journal[cmd.OperationID] = entry
	if err := reconcileCatalogMetadataJournal(reopened, cmd.OperationID); !errors.Is(err, ErrBackupConflict) ||
		reopened.State.Journal[cmd.OperationID].Status != "needs_attention" {
		t.Fatalf("mismatched intent settled: %+v %v", reopened.State.Journal[cmd.OperationID], err)
	}
}

// A receipt settled outside the Run loop, such as by a worker that lost its
// reply, reaches the Controller-visible journal on a later backup tick.
func TestRunLoopSettlesCatalogJournalFromLaterReceipt(t *testing.T) {
	s := seedBackupMaster(t)
	cmd := protocol.Command{OperationID: "rebuild-run-loop", Kind: "backup-catalog-rebuild",
		CatalogImport: &protocol.CatalogImportRequest{RepositoryID: "repo-1"}}
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd, ReserveOnly: true}); response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
		t.Fatalf("reserve: %+v", response)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	s.State.Journal[cmd.OperationID] = JournalEntry{Status: "needs_attention", Error: "scan reply lost", Command: &cmd}
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	worker := &Store{dir: s.dir, State: copyState(s.State)}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: time.Hour, ProbeInterval: 100 * time.Millisecond, ProbeTimeout: 20 * time.Millisecond})
	}()
	// Any answered request proves Run finished its startup reconciliation.
	runLoopPeerRequest(t, ctx, s, done, BackupRequest{})
	receipt.Status, receipt.ObservedAt = "succeeded", time.Now().UTC()
	poolBackupAuthorityMu.Lock()
	err = savePoolBackupMetadataReceipt(worker, receipt)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for {
		raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
		var observed State
		if err == nil && json.Unmarshal(raw, &observed) == nil && observed.Journal[cmd.OperationID].Status == "succeeded" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Core Run stopped before journal settlement: %v", err)
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Core Run left the catalog journal needing attention after its receipt settled")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Core Run stopped with error: %v", err)
	}
}

func TestCatalogRebuildReceiptWriteFailureIsPersistenceError(t *testing.T) {
	for _, failedScan := range []bool{false, true} {
		name := "success"
		if failedScan {
			name = "failed-scan"
		}
		t.Run(name, func(t *testing.T) {
			s := seedBackupMaster(t)
			cmd := protocol.Command{OperationID: "rebuild-receipt-failure", Kind: "backup-catalog-rebuild",
				CatalogImport: &protocol.CatalogImportRequest{RepositoryID: "repo-1"}}
			if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd, ReserveOnly: true}); response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
				t.Fatalf("reserve: %+v", response)
			}
			receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			previous := writeBackupMetadataReceipt
			writeBackupMetadataReceipt = func(string, []byte, os.FileMode) error {
				return errors.New("injected receipt write failure")
			}
			t.Cleanup(func() { writeBackupMetadataReceipt = previous })
			response := BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()}}
			if failedScan {
				response = BackupResponse{Err: ErrBackupUnavailable}
			}
			got := settleBackupMetadataResponse(s, cmd, receipt, response)
			var persistence persistenceError
			if !errors.As(got.Err, &persistence) {
				t.Fatalf("receipt write failure was not fatal persistence error: %+v", got)
			}
		})
	}
}

func TestCatalogRebuildReceiptAdmissionRefusalIsNotPersistenceError(t *testing.T) {
	s := seedBackupMaster(t)
	cmd := protocol.Command{OperationID: "rebuild-admission-refusal", Kind: "backup-catalog-rebuild",
		CatalogImport: &protocol.CatalogImportRequest{RepositoryID: "repo-1"}}
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd, ReserveOnly: true}); response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
		t.Fatalf("reserve: %+v", response)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	// Settlement is never gated on member replicas; a frozen authority is the
	// admission refusal left for it.
	if err := savePoolTransfer(s, &poolTransferRecord{OperationID: "freeze-receipt", Action: "promote", Role: "source",
		Stage: "freeze", Status: "pending", TargetHostID: "member-1", Frozen: true}); err != nil {
		t.Fatal(err)
	}
	got := settleBackupMetadataResponse(s, cmd, receipt, BackupResponse{Result: &protocol.Result{
		OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC(),
	}})
	var persistence persistenceError
	if got.Err == nil || errors.As(got.Err, &persistence) {
		t.Fatalf("authority admission refusal was classified as storage failure: %+v", got)
	}
	stored, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil || stored.Status != "pending" {
		t.Fatalf("refused receipt changed: %+v %v", stored, err)
	}
}

func TestCatalogRebuildJournalWriteFailureRecoversFromReceipt(t *testing.T) {
	s := seedBackupMaster(t)
	cmd := protocol.Command{OperationID: "rebuild-journal-failure", Kind: "backup-catalog-rebuild",
		CatalogImport: &protocol.CatalogImportRequest{RepositoryID: "repo-1"}}
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd, ReserveOnly: true}); response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
		t.Fatalf("reserve: %+v", response)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	s.State.Journal[cmd.OperationID] = JournalEntry{Status: "needs_attention", Command: &cmd}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.beforePublish = func() error { return errors.New("injected journal write failure") }
	response := settleBackupMetadataResponse(s, cmd, receipt, BackupResponse{Result: &protocol.Result{
		OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC(),
	}})
	var persistence persistenceError
	if !errors.As(response.Err, &persistence) {
		t.Fatalf("journal write failure was not fatal: %+v", response)
	}
	s.beforePublish = nil
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.State.Journal[cmd.OperationID].Status != "needs_attention" {
		t.Fatal("injected failure unexpectedly persisted journal")
	}
	if err := reconcileCatalogMetadataJournal(reopened, cmd.OperationID); err != nil ||
		reopened.State.Journal[cmd.OperationID].Status != "succeeded" {
		t.Fatalf("receipt did not recover journal: %+v %v", reopened.State.Journal[cmd.OperationID], err)
	}
}

func TestCatalogRebuildRetainsProtectedAndPinnedMissingPoints(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"point-protected", "point-pinned", "point-unprotected"} {
		seedPublishedPoint(t, mount, "repo-1", "pool-1", id)
	}
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	protected, err := getCatalogPoint(s, "repo-1", "point-protected")
	if err != nil {
		t.Fatal(err)
	}
	protected.Protected = true
	if err := savePoolBackupCatalogEntry(s, protected); err != nil {
		t.Fatal(err)
	}
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{
		RecoveryPointID: "point-pinned", OperationID: "restore-op", Kind: protocol.BackupPinKindNew,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"point-protected", "point-pinned", "point-unprotected"} {
		if err := os.RemoveAll(filepath.Join(mount, id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"point-protected", "point-pinned"} {
		if _, err := getCatalogPoint(s, "repo-1", id); err != nil {
			t.Fatalf("missing safeguarded point %s: %v", id, err)
		}
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-unprotected"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("unprotected missing point was retained: %v", err)
	}
}

func TestCatalogImportReportsProtectMarkerFailure(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-protected")
	point, err := vmhost.ScanBackupRepositoryPoints(mount, "repo-1", "pool-1")
	if err != nil || len(point) != 1 {
		t.Fatalf("seed scan: %+v %v", point, err)
	}
	point[0].Protected = true
	if err := savePoolBackupCatalogEntry(s, point[0]); err != nil {
		t.Fatal(err)
	}
	previous := writeImportedBackupProtectMarker
	writeImportedBackupProtectMarker = func(string, bool) error { return errors.New("marker write failed") }
	t.Cleanup(func() { writeImportedBackupProtectMarker = previous })
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err == nil || err.Error() != "marker write failed" {
		t.Fatalf("marker failure was hidden: %v", err)
	}
}

func TestCatalogRebuildCorruptPointBlocksStaleDeletion(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-stale")
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(mount, "point-stale")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(mount, "point-corrupt"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1"); err == nil {
		t.Fatal("corrupt point was silently skipped during stale deletion")
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-stale"); err != nil {
		t.Fatalf("rebuild deleted prior catalog after corrupt scan: %v", err)
	}
}

func TestCatalogImportReportsPinDurabilityFailure(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	if err := os.WriteFile(poolBackupPinsPath(s), []byte("invalid pins"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err == nil {
		t.Fatal("pin durability failure was hidden")
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-1"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("catalog changed before pin validation: %v", err)
	}
}

// Catalog import must not hold poolBackupAuthorityMu across protect-marker I/O.
func TestCatalogImportReleasesLockDuringProtectMarker(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-protected")
	point, err := vmhost.ScanBackupRepositoryPoints(mount, "repo-1", "pool-1")
	if err != nil || len(point) != 1 {
		t.Fatalf("seed scan: %+v %v", point, err)
	}
	point[0].Protected = true
	if err := savePoolBackupCatalogEntry(s, point[0]); err != nil {
		t.Fatal(err)
	}
	previous := writeImportedBackupProtectMarker
	var heldLock bool
	writeImportedBackupProtectMarker = func(path string, protected bool) error {
		if !poolBackupAuthorityMu.TryLock() {
			heldLock = true
			return previous(path, protected)
		}
		poolBackupAuthorityMu.Unlock()
		return previous(path, protected)
	}
	t.Cleanup(func() { writeImportedBackupProtectMarker = previous })
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if heldLock {
		t.Fatal("import held poolBackupAuthorityMu during protect-marker write")
	}
}

// A cancelled import must not delete stale catalog entries that were not re-imported.
func TestCatalogRebuildCancelSkipsStaleDeletion(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-keep")
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-stale")
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(mount, "point-stale")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RebuildPoolBackupCatalogFromRepository(ctx, s, "repo-1"); err == nil {
		t.Fatal("cancelled rebuild reported success")
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-stale"); err != nil {
		t.Fatalf("cancelled rebuild deleted a not-yet-reimported point: %v", err)
	}
}
