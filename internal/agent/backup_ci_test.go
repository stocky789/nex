// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// TestPhase6BackupHermeticCIRegression is the forge/github.com go-test entry point for
// Phase 6 S4 backup safety: NFS-or-temp repository registration, stopped publish,
// corrupt-manifest refusal, and restore-as-new durable registration records.
// Live libvirt define/storage ancestry remains disposable-lab gated.
func TestPhase6BackupHermeticCIRegression(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()

	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source:    protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
		MountPath: mount,
	}
	saved, err := upsertTestBackupRepository(s, repo)
	if err != nil || saved.ID != "repo-1" || saved.MountPath != mount {
		t.Fatalf("register NFS/temp repo: %#v err=%v", saved, err)
	}
	listed, err := ListPoolBackupRepositories(s)
	if err != nil || len(listed) != 1 || listed[0].Source.Server != "nfs.example.test" {
		t.Fatalf("list registered repo: %#v err=%v", listed, err)
	}

	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-ok")
	pointDir := filepath.Join(mount, "point-ok")
	manifest, digest, err := vmhost.ReadBackupManifest(pointDir)
	if err != nil || digest == "" || manifest.Consistency != protocol.BackupCaptureStopped || manifest.CapturedSpec == nil {
		t.Fatalf("stopped publish must be readable: digest=%q manifest=%#v err=%v", digest, manifest, err)
	}
	if err := savePoolBackupCatalogEntry(s, protocol.BackupRecoveryPoint{
		ID: manifest.RecoveryPointID, RepositoryID: manifest.RepositoryID, PoolID: manifest.PoolID,
		SourceVMID: manifest.SourceVMID, CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency,
		ManifestSHA256: digest,
	}); err != nil {
		t.Fatal(err)
	}

	badDir := filepath.Join(mount, "point-bad")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "manifest.json"), []byte(`{"version":1,"complete":true,"success":1`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := vmhost.ReadBackupManifest(badDir); err == nil {
		t.Fatal("corrupt manifest must be refused")
	}
	imported, err := ImportPoolBackupCatalog(s, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0].ID != "point-ok" {
		t.Fatalf("catalog rebuild must keep only validated points: %#v", imported)
	}

	localDir := filepath.Join(s.dir, "backup-restore", "restore-ci-1")
	if err := vmhost.CopyBackupArtifactsLocal(context.Background(), pointDir, localDir, manifest); err != nil {
		t.Fatal(err)
	}
	asNew := protocol.RestoreAsNewRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-ok",
		NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored-ci", StorageID: "storage-1",
	}
	spec, err := vmhost.BuildRestoreAsNewSpec(*manifest.CapturedSpec, asNew, s.State.PoolID, s.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != asNew.NewVMID || spec.Name != asNew.NewVMName || spec.StartAfterCreate || len(spec.NICs) != 0 {
		t.Fatalf("restore-as-new spec must be stopped and disconnected: %#v", spec)
	}
	payload, err := os.ReadFile(filepath.Join(localDir, "vda.qcow2"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != manifest.Disks[0].SHA256 {
		t.Fatal("local restore copy digest mismatch")
	}

	observed := time.Now().UTC()
	intent := strings.Repeat("d", 64)
	reserved, err := ReservePoolBackupRun(s, protocol.BackupRun{
		OperationID: "restore-ci-1", Kind: protocol.BackupKindRestoreNew, PoolID: s.State.PoolID,
		RepositoryID: "repo-1", RecoveryPointID: "point-ok", NewVMID: asNew.NewVMID,
		IntentSHA256: intent, Stage: "reserved", Status: "pending", CreatedAt: observed,
	})
	if err != nil || reserved.Status != "pending" {
		t.Fatalf("reserve restore run: %#v err=%v", reserved, err)
	}
	if err := SettlePoolBackupRun(s, protocol.BackupRun{
		OperationID: "restore-ci-1", Kind: protocol.BackupKindRestoreNew, PoolID: s.State.PoolID,
		RepositoryID: "repo-1", RecoveryPointID: "point-ok", NewVMID: asNew.NewVMID,
		IntentSHA256: intent, Stage: "registered", Status: "succeeded",
		CreatedAt: reserved.CreatedAt, UpdatedAt: observed,
	}, nil); err != nil {
		t.Fatal(err)
	}
	_, catalog, runs, _, reported, err := PoolBackupInventory(s)
	if err != nil || !reported || len(catalog) != 1 || len(runs) != 1 {
		t.Fatalf("inventory after restore registration: reported=%v catalog=%d runs=%d err=%v", reported, len(catalog), len(runs), err)
	}
	if runs[0].Stage != "registered" || runs[0].NewVMID != asNew.NewVMID || runs[0].Status != "succeeded" {
		t.Fatalf("restore-as-new registration run: %#v", runs[0])
	}

	// With a valid published point, restore reaches managed registration and fails without
	// host storage ancestry — proving publish/copy/spec before the lab-gated define step.
	cmd := protocol.Command{
		OperationID: "restore-ci-live", Kind: "backup-restore-as-new",
		Restore: &asNew,
	}
	result, err := executeBackupCommand(context.Background(), s.dir, s.State, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status == "succeeded" {
		t.Fatalf("restore-as-new must not succeed without managed storage: %#v", result)
	}
	if result.Status != "failed" || result.Error == "" {
		t.Fatalf("want failed pre-effect placement refusal, got %#v", result)
	}
	if _, statErr := os.Stat(filepath.Join(s.dir, "backup-restore", "restore-ci-live")); statErr != nil {
		t.Fatalf("restore must stage local copies before registration failure: %v", statErr)
	}
}
