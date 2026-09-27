// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

var testBackupMounts sync.Map

func init() {
	verifyBackupRepository = func(ctx context.Context, repo protocol.BackupRepository) error {
		if _, ok := testBackupMounts.Load(repo.MountPath); ok {
			ownerRepo, ownerPool, err := vmhost.ReadBackupRepositoryOwner(repo.MountPath)
			if errors.Is(err, os.ErrNotExist) {
				return vmhost.ClaimBackupRepositoryOwner(repo.MountPath, repo.ID, repo.PoolID)
			}
			if err != nil || ownerRepo != repo.ID || ownerPool != repo.PoolID {
				return ErrBackupConflict
			}
			return nil
		}
		return vmhost.VerifyBackupRepository(ctx, repo)
	}
}

func upsertTestBackupRepository(s *Store, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	testBackupMounts.Store(repo.MountPath, true)
	prepared := repo
	prepared.Identity = &protocol.NFSStorageIdentity{
		Server: repo.Source.Server, Export: repo.Source.Export, FilesystemID: "test-nfs",
	}
	repo.MountPath = ""
	return upsertPoolBackupRepositoryPrepared(s, repo, prepared)
}

func seedBackupMaster(t *testing.T) *Store {
	t.Helper()
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBackupRepositoryCRUDAndInventoryFlags(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source:    protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
		MountPath: mount,
	}
	saved, err := upsertTestBackupRepository(s, repo)
	if err != nil || saved.Generation != 1 {
		t.Fatalf("upsert: %#v err=%v", saved, err)
	}
	listed, err := ListPoolBackupRepositories(s)
	if err != nil || len(listed) != 1 || listed[0].ID != "repo-1" {
		t.Fatalf("list: %#v err=%v", listed, err)
	}
	tested, err := TestPoolBackupRepositoryAccess(s, "repo-1")
	if err != nil || tested.Access.Status != "ok" {
		t.Fatalf("test access: %#v err=%v", tested, err)
	}
	repos, catalog, runs, _, reported, err := PoolBackupInventory(s)
	if err != nil || !reported || len(repos) != 1 || len(catalog) != 0 || len(runs) != 0 {
		t.Fatalf("inventory reported=%v repos=%d catalog=%v runs=%v err=%v", reported, len(repos), catalog, runs, err)
	}
}

func TestBackupAdmissionExactRetryAndChangedInputConflict(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	writeSnapshotTestVMJournal(t, s, vm)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\necho 'shut off'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	request := BackupRequest{Command: &protocol.Command{
		OperationID: "backup-1", Kind: "backup-now",
		VM: vm, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"},
	}}
	response := acceptBackupRequest(context.Background(), s, request)
	if response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
		t.Fatalf("admission: %#v", response)
	}
	run, err := loadPoolBackupRun(s, "backup-1")
	if err != nil || run.Status != "pending" || run.Stage != "reserved" {
		t.Fatalf("run reservation: %#v err=%v", run, err)
	}
	if got := acceptBackupRequest(context.Background(), s, request); got.Err != nil || got.Result.OperationID != "backup-1" {
		t.Fatalf("exact retry: %#v", got)
	}
	changed := request
	changed.Command = &protocol.Command{
		OperationID: "backup-1", Kind: "backup-now",
		VM: vm, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-other"},
	}
	if got := acceptBackupRequest(context.Background(), s, changed); !errors.Is(got.Err, ErrBackupConflict) {
		t.Fatalf("changed input: %#v", got)
	}
	lookup := BackupRequest{OperationID: "backup-1"}
	if got := acceptBackupRequest(context.Background(), s, lookup); got.Err != nil || got.Result.Status != "pending" {
		t.Fatalf("lookup: %#v", got)
	}
}

func TestBackupNowControllerPathReservesRunAndExactPublishRetrySettles(t *testing.T) {
	// Controller dispatches backup-now through the journal without acceptBackupRequest,
	// so execute must reserve pool-backup-runs before settle. A published point for the
	// same operation_id must settle succeeded without failing "already exists".
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if err := vmhost.ClaimBackupRepositoryOwner(mount, "repo-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	diskPath := filepath.Join(t.TempDir(), "vda.qcow2")
	if err := os.WriteFile(diskPath, []byte("disk-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("disk-bytes"))
	diskSHA := hex.EncodeToString(sum[:])
	specDigest, _, err := vmhost.BackupSpecAndDiskDigests(*vm, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := vmhost.BuildStoppedBackupManifest("backup-ctrl-1", "repo-1", "pool-1", vm.ID, vm.Name,
		specDigest, strings.Repeat("b", 64), *vm,
		[]protocol.BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: int64(len("disk-bytes")), SHA256: diskSHA}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publishedDigest, err := vmhost.PublishBackupRecoveryPoint(mount, manifest, map[string]string{"vda.qcow2": diskPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, poolBackupRunsDir, "backup-ctrl-1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("controller path must start without a reserved run: %v", err)
	}
	cmd := protocol.Command{
		OperationID: "backup-ctrl-1", Kind: "backup-now",
		VM: vm, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"},
	}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatalf("controller-path reserve: %v", err)
	}
	run, err := loadPoolBackupRun(s, "backup-ctrl-1")
	if err != nil || run.Status != "pending" {
		t.Fatalf("reserved run: %#v err=%v", run, err)
	}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	point := protocol.BackupRecoveryPoint{
		ID: "backup-ctrl-1", RepositoryID: "repo-1", PoolID: "pool-1", SourceVMID: vm.ID,
		SourceVMName: vm.Name, CapturedAt: manifest.CapturedAt, Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: publishedDigest,
	}
	if err := SettlePoolBackupRun(s, protocol.BackupRun{
		OperationID: "backup-ctrl-1", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1",
		VMID: vm.ID, RepositoryID: "repo-1", RecoveryPointID: "backup-ctrl-1", IntentSHA256: intent,
		Stage: "published", Status: "succeeded", CreatedAt: time.Now().UTC(),
	}, &point); err != nil {
		t.Fatalf("settle after publish: %v", err)
	}
	again, err := vmhost.PublishBackupRecoveryPoint(mount, manifest, map[string]string{"vda.qcow2": diskPath})
	if err != nil || again != publishedDigest {
		t.Fatalf("exact republish must succeed: digest=%q err=%v", again, err)
	}
	catalog, err := loadPoolBackupCatalog(s)
	if err != nil || len(catalog) != 1 || catalog[0].ID != "backup-ctrl-1" {
		t.Fatalf("catalog after settle: %#v err=%v", catalog, err)
	}
}

func TestRestoreAsNewControllerPathReservesMissingRunBeforeSettle(t *testing.T) {
	// Browser restore-as-new via Controller left needs_attention with
	// pool-backup-runs/{id}.json missing after the guest was created.
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-restore")
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportPoolBackupCatalog(s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{
		OperationID: "restore-ctrl-1", Kind: "backup-restore-as-new",
		Restore: &protocol.RestoreAsNewRequest{
			RepositoryID: "repo-1", RecoveryPointID: "point-restore",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored-ctrl", StorageID: "storage-1",
		},
	}
	if _, err := os.Stat(filepath.Join(s.dir, poolBackupRunsDir, "restore-ctrl-1.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want no run file before execute: %v", err)
	}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatalf("reserve on controller path: %v", err)
	}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := SettlePoolBackupRun(s, protocol.BackupRun{
		OperationID: "restore-ctrl-1", Kind: protocol.BackupKindRestoreNew, PoolID: "pool-1",
		RepositoryID: "repo-1", RecoveryPointID: "point-restore", NewVMID: cmd.Restore.NewVMID,
		IntentSHA256: intent, Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC(),
	}, nil); err != nil {
		t.Fatalf("settle after register must find reserved run: %v", err)
	}
	run, err := loadPoolBackupRun(s, "restore-ctrl-1")
	if err != nil || run.Status != "succeeded" || run.Stage != "registered" {
		t.Fatalf("restored run: %#v err=%v", run, err)
	}
	// Exact retry of reservation is idempotent.
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatalf("exact reserve retry: %v", err)
	}
	changed := cmd
	changed.Restore = &protocol.RestoreAsNewRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-restore",
		NewVMID: "33333333-3333-4333-8333-333333333333", NewVMName: "other", StorageID: "storage-1",
	}
	if err := ensureBackupRunReserved(s, changed); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("different input same operation_id: %v", err)
	}
}

func TestBackupAdmissionRefusesOnlineUEFIAndQuiesced(t *testing.T) {
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	writeSnapshotTestVMJournal(t, s, vm)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\necho running\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	quiesced := BackupRequest{Command: &protocol.Command{
		OperationID: "backup-q", Kind: "backup-now",
		VM: vm, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1", Consistency: protocol.BackupCaptureQuiesced},
	}}
	if got := acceptBackupRequest(context.Background(), s, quiesced); !errors.Is(got.Err, ErrBackupInvalid) {
		t.Fatalf("quiesced: %#v", got)
	}
	uefi := *vm
	uefi.Boot.Firmware = "uefi"
	writeSnapshotTestVMJournal(t, s, &uefi)
	online := BackupRequest{Command: &protocol.Command{
		OperationID: "backup-u", Kind: "backup-now",
		VM: &uefi, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1", Consistency: protocol.BackupCaptureOnline},
	}}
	if got := acceptBackupRequest(context.Background(), s, online); !errors.Is(got.Err, ErrBackupInvalid) {
		t.Fatalf("uefi online: %#v", got)
	}
}

func TestBackupIncompleteNeverCataloged(t *testing.T) {
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source:    protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
		MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	intent := strings.Repeat("a", 64)
	run := protocol.BackupRun{
		OperationID: "backup-fail", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1",
		VMID: "vm-1", RepositoryID: "repo-1", RecoveryPointID: "backup-fail",
		IntentSHA256: intent, Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC(),
	}
	if _, err := ReservePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	run.Stage, run.Status, run.Error = "failed", "failed", "convert failed"
	if err := SettlePoolBackupRun(s, run, nil); err != nil {
		t.Fatal(err)
	}
	points, err := loadPoolBackupCatalog(s)
	if err != nil || len(points) != 0 {
		t.Fatalf("failed run must not publish catalog entries: %#v err=%v", points, err)
	}
}

func TestBackupCatalogAdoptionAfterSuccess(t *testing.T) {
	s := seedBackupMaster(t)
	point := protocol.BackupRecoveryPoint{
		ID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1", SourceVMID: "vm-1",
		CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: strings.Repeat("c", 64),
	}
	if err := savePoolBackupCatalogEntry(s, point); err != nil {
		t.Fatal(err)
	}
	_, catalog, _, _, reported, err := PoolBackupInventory(s)
	if err != nil || !reported || len(catalog) != 1 || catalog[0].ID != "point-1" {
		t.Fatalf("catalog adoption: reported=%v catalog=%#v err=%v", reported, catalog, err)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, poolBackupCatalogDir, "point-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored protocol.BackupRecoveryPoint
	if err := json.Unmarshal(raw, &stored); err != nil || stored.Valid() != nil {
		t.Fatalf("durable catalog: %#v err=%v", stored, err)
	}
}

// Snapshot reservation holds poolPeerMu while sizing the authority replica. Calling
// Membership from PoolBackupAuthorityRefs re-enters that lock and hangs the Catalog suite.
func TestPoolBackupAuthorityRefsDoesNotReenterPeerLock(t *testing.T) {
	s := seedBackupMaster(t)
	vms, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	vms.VMs["vm-1"] = poolVMRecord{PoolID: "pool-1", VMID: "vm-1", HostID: "host-a", State: "stopped", Epoch: 1, Spec: *poolVMTestSpec("vm-1", "host-a")}
	if err := savePoolVMs(s, vms); err != nil {
		t.Fatal(err)
	}
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.NextDispatchAt = time.Time{}
	if _, err := UpsertPoolBackupPolicy(s, policy); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		poolPeerMu.Lock()
		defer poolPeerMu.Unlock()
		auth, err := PoolBackupAuthorityRefs(s)
		if err != nil {
			done <- err
			return
		}
		if auth == nil || len(auth.Policies) != 1 {
			done <- errors.New("expected compact policy refs under peer lock")
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PoolBackupAuthorityRefs deadlocked re-entering poolPeerMu (Membership)")
	}
}

func TestBackupMetadataReceiptBindsExactIntentAcrossRetry(t *testing.T) {
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
	cmd := protocol.Command{OperationID: "verify-1", Kind: "backup-test-verify", TestRestoreVerify: &protocol.TestRestoreVerifyRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: vm.ID, Result: protocol.BackupTestResultPassed,
	}}
	plan, err := prepareTestRestoreVerification(s, *cmd.TestRestoreVerify)
	if err != nil {
		t.Fatal(err)
	}
	plan.VerifiedAt = time.Now().UTC()
	first := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd, PreparedVerification: &plan})
	if first.Err != nil || first.Result == nil || first.Result.Status != "succeeded" {
		t.Fatalf("first metadata operation: %#v", first)
	}
	if retry := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd}); retry.Err != nil || retry.Result == nil || retry.Result.Status != "succeeded" {
		t.Fatalf("exact retry: %#v", retry)
	}
	changed := cmd
	copyReq := *cmd.TestRestoreVerify
	copyReq.Result = protocol.BackupTestResultFailed
	changed.TestRestoreVerify = &copyReq
	if retry := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &changed}); !errors.Is(retry.Err, ErrBackupConflict) {
		t.Fatalf("changed intent replay: %#v", retry)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, "verify-1")
	if err != nil || receipt.Status != "succeeded" || receipt.IntentSHA256 == "" {
		t.Fatalf("durable metadata receipt: %#v err=%v", receipt, err)
	}
}

func TestBackupMetadataInvalidRequestSettlesFailedReceipt(t *testing.T) {
	s := seedBackupMaster(t)
	cmd := protocol.Command{OperationID: "bad-register", Kind: "backup-repository-register"}
	first := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if !errors.Is(first.Err, ErrBackupInvalid) {
		t.Fatalf("invalid request: %#v", first)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil || receipt.Status != "failed" {
		t.Fatalf("invalid request left an unresolved receipt: %#v err=%v", receipt, err)
	}
	retry := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if retry.Err != nil || retry.Result == nil || retry.Result.Status != "failed" {
		t.Fatalf("invalid exact retry: %#v", retry)
	}
}
