// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestFilesystemBackupBoundaryRejectsIncompleteAndCorruptPoints(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-good")
	repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := backend.CaptureWorkDir("backup-op", true); got != filepath.Join(mount, ".nex-backup-work", "backup-op") {
		t.Fatalf("online capture path changed: %s", got)
	}
	if got := backend.CaptureWorkDir("backup-op", false); got != filepath.Join(s.dir, "backup-staging", "backup-op") {
		t.Fatalf("stopped capture path changed: %s", got)
	}
	if err := os.Mkdir(filepath.Join(mount, "point-incomplete"), 0700); err != nil {
		t.Fatal(err)
	}
	points, err := backend.Scan(context.Background())
	if err != nil || len(points) != 1 || points[0].ID != "point-good" {
		t.Fatalf("scan included incomplete point: %+v, %v", points, err)
	}
	if _, _, err := backend.StatPoint(context.Background(), "point-incomplete"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete point was treated as absent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mount, "point-good", "vda.qcow2"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.StatPoint(context.Background(), "point-good"); err == nil {
		t.Fatal("corrupt point was readable")
	}
	if err := backend.Delete(context.Background(), "point-good"); err == nil {
		t.Fatal("corrupt point was deleted")
	}
	if _, err := os.Stat(filepath.Join(mount, "point-good")); err != nil {
		t.Fatalf("corrupt point disappeared: %v", err)
	}
}

func TestFilesystemPruneResumesOnlyRecordedPartialDeletion(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	for _, id := range []string{"point-old", "point-protected"} {
		seedPublishedPoint(t, mount, "repo-1", "pool-1", id)
	}
	manifestPath := filepath.Join(mount, "point-old", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, formatted.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	tagCatalogPolicy(t, s, "repo-1", "policy-1", "point-old", "point-protected")
	if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-protected", Protected: true,
	}); err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join(mount, "point-old", "vda.qcow2")
	diskBytes, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(diskPath); err != nil {
		t.Fatal(err)
	}
	if err := prunePublishedBackupPayloads(context.Background(), s, "repo-1", "vm-1", "policy-1", protocol.BackupRetention{KeepLatest: 0}, ""); err == nil {
		t.Fatal("unrecorded missing artifact was treated as a prune")
	}
	if err := os.WriteFile(diskPath, diskBytes, 0600); err != nil {
		t.Fatal(err)
	}
	_, digest, err := vmhost.ReadBackupManifest(filepath.Join(mount, "point-old"))
	if err != nil {
		t.Fatal(err)
	}
	intent, err := json.Marshal(struct {
		Manifest []byte `json:"manifest"`
		Digest   string `json:"digest"`
	}{formatted.Bytes(), digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := vmhost.AtomicWrite(filepath.Join(mount, ".nex-delete-point-old"), intent, 0600); err != nil {
		t.Fatal(err)
	}
	var replacement protocol.BackupManifest
	if err := json.Unmarshal(formatted.Bytes(), &replacement); err != nil {
		t.Fatal(err)
	}
	replacement.SourceVMName = "replacement"
	replacementRaw, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, replacementRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := prunePublishedBackupPayloads(context.Background(), s, "repo-1", "vm-1", "policy-1", protocol.BackupRetention{KeepLatest: 0}, ""); err == nil {
		t.Fatal("delete intent removed a replacement manifest")
	}
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("replacement artifact was removed: %v", err)
	}
	if err := os.WriteFile(manifestPath, formatted.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(diskPath); err != nil {
		t.Fatal(err)
	}
	if err := prunePublishedBackupPayloads(context.Background(), s, "repo-1", "vm-1", "policy-1", protocol.BackupRetention{KeepLatest: 0}, ""); err != nil {
		t.Fatalf("exact prune retry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mount, "point-old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partially deleted payload remained: %v", err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-old"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("deleted point remained cataloged: %v", err)
	}
	if _, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1"); err != nil {
		t.Fatalf("rebuild after prune: %v", err)
	}
	if point, err := getCatalogPoint(s, "repo-1", "point-protected"); err != nil || !point.Protected {
		t.Fatalf("protected point changed: %+v, %v", point, err)
	}
}

func TestRemoteOnlineBackupUsesQEMUAccessibleWorkDir(t *testing.T) {
	stateDir := t.TempDir()
	backends := []backupRepositoryStorage{
		&s3BackupStorage{s: &Store{dir: stateDir}},
		sftpBackupStorage{stateDir: stateDir},
	}
	for _, backend := range backends {
		if got := backend.CaptureWorkDir("backup-op", true); got != filepath.Join(vmhost.DefaultStoragePath, ".nex-backup-work", "backup-op") {
			t.Fatalf("online capture path inaccessible to QEMU: %s", got)
		}
		if got := backend.CaptureWorkDir("backup-op", false); got != filepath.Join(stateDir, "backup-staging", "backup-op") {
			t.Fatalf("stopped capture path changed: %s", got)
		}
	}
}

func TestFailedBackupRemovesPartialCapture(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	disk := filepath.Join(t.TempDir(), "source.qcow2")
	if err := os.WriteFile(disk, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestVMJournalWithDisk(t, s, vm, disk)
	bin := t.TempDir()
	for name, script := range map[string]string{
		"virsh":    "#!/bin/sh\necho 'shut off'\n",
		"qemu-img": "#!/bin/sh\nprintf partial > \"$5\"\nexit 1\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := protocol.Command{OperationID: "failed-capture", Kind: protocol.BackupKindBackupNow,
		VM: vm, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	result, err := executeBackupNow(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "failed" {
		t.Fatalf("failed capture result: %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "backup-staging", cmd.OperationID)); !os.IsNotExist(err) {
		t.Fatalf("partial capture remained after terminal failure: %v", err)
	}
}

func TestFilesystemPostRenameErrorReconcilesPublishedPoint(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	disk := filepath.Join(t.TempDir(), "source.qcow2")
	if err := os.WriteFile(disk, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestVMJournalWithDisk(t, s, vm, disk)
	bin := t.TempDir()
	for name, script := range map[string]string{
		"virsh":    "#!/bin/sh\ncase \" $* \" in\n*' dumpxml '*) echo '<domain type=\"kvm\"><name>test</name></domain>';;\n*) echo 'shut off';;\nesac\n",
		"qemu-img": "#!/bin/sh\ncase \"$1\" in\nconvert) cp \"$4\" \"$5\";;\ninfo) printf '[{\"filename\":\"%s\",\"format\":\"qcow2\"}]' \"$5\";;\ncheck) exit 0;;\nesac\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	previous := publishFilesystemBackupPoint
	publishFilesystemBackupPoint = func(root string, manifest protocol.BackupManifest, files map[string]string) (string, error) {
		if _, err := vmhost.PublishBackupRecoveryPoint(root, manifest, files); err != nil {
			return "", err
		}
		return "", errors.Join(vmhost.ErrBackupPublishUncertain, errors.New("repository parent sync reply lost after publication"))
	}
	t.Cleanup(func() { publishFilesystemBackupPoint = previous })
	cmd := protocol.Command{OperationID: "post-rename-error", Kind: protocol.BackupKindBackupNow,
		VM: vm, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	result, err := executeBackupNow(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("published point reported failed after lost sync reply: %+v, %v", result, err)
	}
	if run, err := loadPoolBackupRun(s, cmd.OperationID); err != nil || run.Status != "succeeded" {
		t.Fatalf("published run was not settled: %+v, %v", run, err)
	}
	if _, err := getCatalogPoint(s, "repo-1", cmd.OperationID); err != nil {
		t.Fatalf("published point missing from catalog: %v", err)
	}
	publishFilesystemBackupPoint = func(root string, manifest protocol.BackupManifest, files map[string]string) (string, error) {
		for name := range files {
			files[name] = filepath.Join(t.TempDir(), "missing")
			break
		}
		return vmhost.PublishBackupRecoveryPoint(root, manifest, files)
	}
	cmd.OperationID = "pre-rename-error"
	result, err = executeBackupNow(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "failed" {
		t.Fatalf("prepublication error remained unsettled: %+v, %v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(mount, cmd.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepublication error left a point: %v", err)
	}
	if run, err := loadPoolBackupRun(s, cmd.OperationID); err != nil || run.Status != "failed" {
		t.Fatalf("prepublication run was not settled: %+v, %v", run, err)
	}
	publishFilesystemBackupPoint = func(string, protocol.BackupManifest, map[string]string) (string, error) {
		return "", errors.Join(vmhost.ErrBackupPublishUncertain, errors.New("rename reply lost before publication"))
	}
	cmd.OperationID = "uncertain-no-point"
	result, err = executeBackupNow(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "failed" {
		t.Fatalf("absent uncertain publication remained unsettled: %+v, %v", result, err)
	}
	if run, err := loadPoolBackupRun(s, cmd.OperationID); err != nil || run.Status != "failed" {
		t.Fatalf("absent publication run was not terminal: %+v, %v", run, err)
	}
	if _, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1"); err != nil {
		t.Fatalf("abort fence blocked catalog rebuild: %v", err)
	}
	if _, err := getCatalogPoint(s, "repo-1", cmd.OperationID); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("abort fence became a catalog point: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mount, cmd.OperationID), []byte("nex-backup-abort:"), 0600); err != nil {
		t.Fatal(err)
	}
	repo, err := GetPoolBackupRepository(s, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.(filesystemBackupStorage).AbortIncomplete(context.Background(), cmd.OperationID); err != nil {
		t.Fatalf("interrupted fence did not resume: %v", err)
	}
	if _, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1"); err != nil {
		t.Fatalf("resumed fence blocked catalog rebuild: %v", err)
	}
	if err := os.Mkdir(filepath.Join(mount, ".staging-"+cmd.OperationID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(mount, ".staging-"+cmd.OperationID), filepath.Join(mount, cmd.OperationID)); err == nil {
		t.Fatal("late publication replaced the abort fence")
	}
	if err := backend.(filesystemBackupStorage).AbortIncomplete(context.Background(), "post-rename-error"); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("abort accepted a cataloged published point: %v", err)
	}
	if err := backend.(filesystemBackupStorage).AbortIncomplete(context.Background(), cmd.OperationID); err != nil {
		t.Fatalf("abort retry refused its fenced staging directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(mount, ".staging-"+cmd.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fenced staging directory remained: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(mount, "foreign-fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := backend.(filesystemBackupStorage).AbortIncomplete(context.Background(), "foreign-fifo"); err == nil {
		t.Fatal("abort accepted a foreign FIFO")
	}
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(s)
	if err == nil {
		peer.Restricted = &poolRestrictedRecord{Kind: poolRestrictedUnconfirmed, Reason: poolUnconfirmedReason, Since: time.Now().UTC()}
		err = savePoolPeer(s, peer)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.(filesystemBackupStorage).AbortIncomplete(context.Background(), "restricted-abort"); err == nil || err.Error() != poolUnconfirmedReason {
		t.Fatalf("restricted host reached the repository before refusal: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(mount, "restricted-abort")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restricted host changed the repository: %v", err)
	}
}

func TestBackupHealthSaveMergesCurrentRecordAndRejectsChangedIdentity(t *testing.T) {
	s := seedBackupMaster(t)
	repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := repo
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		t.Fatal(err)
	}
	current := state.Repositories[repo.ID]
	current.Name = "Renamed"
	state.Repositories[repo.ID] = current
	if err := savePoolBackupRepositories(s, state); err != nil {
		t.Fatal(err)
	}
	stale.Health = protocol.BackupRepositoryHealth{Status: "offline", ObservedAt: time.Now().UTC()}
	if err := savePoolBackupRepositoryAccess(s, stale); err != nil {
		t.Fatal(err)
	}
	state, err = loadPoolBackupRepositories(s)
	if err != nil || state.Repositories[repo.ID].Name != "Renamed" || state.Repositories[repo.ID].Health.Status != "offline" {
		t.Fatalf("stale observation overwrote current registration: %+v, %v", state.Repositories[repo.ID], err)
	}
	current = state.Repositories[repo.ID]
	changed := *current.Identity
	changed.FilesystemID = "different"
	current.Identity = &changed
	state.Repositories[repo.ID] = current
	if err := savePoolBackupRepositories(s, state); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRepositoryAccess(s, stale); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("stale identity observation accepted: %v", err)
	}
	stale.Identity = current.Identity
	current.Health = protocol.BackupRepositoryHealth{Status: "online", ObservedAt: stale.Health.ObservedAt.Add(time.Second)}
	state.Repositories[repo.ID] = current
	if err := savePoolBackupRepositories(s, state); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRepositoryAccess(s, stale); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("older health observation accepted: %v", err)
	}
}

// Regression for BUG-0716: prune, protection and rebuild read only manifests and
// artifact sizes; restore reads still hash every artifact.
func TestFilesystemMetadataReadsSkipArtifactContent(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	// Same-size content change: only a hashing read can detect it.
	if err := os.WriteFile(filepath.Join(mount, "point-1", "vda.qcow2"), bytes.Repeat([]byte("x"), len("disk-bytes-for-restore")), 0600); err != nil {
		t.Fatal(err)
	}
	if points, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil || len(points) != 1 {
		t.Fatalf("rebuild hashed artifact content: %v %v", points, err)
	}
	repo, err := GetPoolBackupRepository(s, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyBackupPoint(t, backend, "point-1"); err == nil {
		t.Fatal("restore copy accepted changed artifact content")
	}
	if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1"}); err != nil {
		t.Fatalf("protection hashed artifact content: %v", err)
	}
	if err := backend.Delete(context.Background(), "point-1"); err != nil {
		t.Fatalf("prune hashed artifact content: %v", err)
	}
}

func TestFilesystemPendingDeleteIsHiddenAndFinishedAfterCatalogLoss(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	for _, id := range []string{"point-a", "point-b", "point-c"} {
		seedPublishedPoint(t, mount, "repo-1", "pool-1", id)
	}
	repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		t.Fatal(err)
	}
	// A prune that crashed right after recording its intent for point-a.
	raw, err := os.ReadFile(filepath.Join(mount, "point-a", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, err := vmhost.ReadBackupManifest(filepath.Join(mount, "point-a"))
	if err != nil {
		t.Fatal(err)
	}
	intent, err := json.Marshal(filesystemDeleteIntent{Manifest: raw, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := vmhost.AtomicWrite(filepath.Join(mount, ".nex-delete-point-a"), intent, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.StatPoint(context.Background(), "point-a"); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("half-deleted point was readable: %v", err)
	}
	if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-a", Protected: true,
	}); err == nil {
		t.Fatal("half-deleted point was protected")
	}
	points, err := backend.Scan(context.Background())
	if err != nil || len(points) != 2 {
		t.Fatalf("scan listed a half-deleted point or refused the repository: %+v, %v", points, err)
	}
	if _, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1"); err != nil {
		t.Fatalf("rebuild refused the repository: %v", err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-a"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("rebuild kept the half-deleted point: %v", err)
	}

	if err := os.WriteFile(filepath.Join(mount, "point-b", ".DS_Store"), []byte("finder"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := backend.Delete(context.Background(), "point-b"); err != nil {
		t.Fatalf("file-manager metadata blocked delete: %v", err)
	}
	for _, path := range []string{"point-a", ".nex-delete-point-a", "point-b"} {
		if _, err := os.Lstat(filepath.Join(mount, path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remained after delete: %v", path, err)
		}
	}
	if err := os.Mkdir(filepath.Join(mount, "point-c", "@eaDir"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := backend.Delete(context.Background(), "point-c"); !errors.Is(err, ErrBackupConflict) || !strings.Contains(err.Error(), "@eaDir") {
		t.Fatalf("unexpected entry was not reported by name: %v", err)
	}
}

func TestFilesystemAbortClearsStagingAndReportsPointAbsent(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-good")
	repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(mount, ".staging-point-lost")
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "vda.qcow2"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := backend.(filesystemBackupStorage).AbortIncomplete(context.Background(), "point-lost"); err != nil {
		t.Fatalf("abort refused a failed publish's staging: %v", err)
	}
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abort left the staging copy: %v", err)
	}
	if _, _, err := backend.StatPoint(context.Background(), "point-lost"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted point was not reported absent: %v", err)
	}
	if err := os.Rename(filepath.Join(mount, "point-good"), filepath.Join(mount, "point-lost")); err == nil {
		t.Fatal("a delayed publish rename replaced the abort fence")
	}
}

// copyBackupPoint copies a point's artifacts as restore does; the copy verifies every digest.
func copyBackupPoint(t *testing.T, backend backupRepositoryStorage, id string) error {
	manifest, _, err := backend.StatPoint(context.Background(), id)
	if err != nil {
		return err
	}
	return backend.CopyArtifacts(context.Background(), id, t.TempDir(), manifest)
}

// A catalog rebuilt from the repository alone recovers each point's creating
// policy from its manifest, so retention still prunes those points.
func TestCatalogRebuildRestoresPolicyIDForRetention(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s)
	now := time.Now().UTC().Truncate(time.Second)
	for id, policy := range map[string]string{"old": "policy-1", "new": "policy-1", "manual": ""} {
		seedPublishedPoint(t, mount, "repo-1", s.State.PoolID, id)
		name := filepath.Join(mount, id, "manifest.json")
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var manifest protocol.BackupManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.PolicyID = policy
		manifest.CapturedAt = now.Add(-time.Hour)
		if id == "new" {
			manifest.CapturedAt = now
		}
		if raw, err = json.Marshal(manifest); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"old": "policy-1", "new": "policy-1", "manual": ""} {
		if point, err := getCatalogPoint(s, "repo-1", id); err != nil || point.PolicyID != want {
			t.Fatalf("rebuilt %s policy %q: %v", id, point.PolicyID, err)
		}
	}
	if err := prunePublishedBackupPayloads(context.Background(), s, "repo-1", "vm-1", "policy-1", protocol.BackupRetention{KeepLatest: 1}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "old"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatal("rebuilt policy point was not pruned:", err)
	}
	for _, id := range []string{"new", "manual"} {
		if _, err := getCatalogPoint(s, "repo-1", id); err != nil {
			t.Fatalf("retention pruned %s: %v", id, err)
		}
	}
}

func restrictPoolAuthority(t *testing.T, s *Store) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	state.Restricted = &poolRestrictedRecord{Kind: poolRestrictedUnconfirmed, Reason: poolUnconfirmedReason, Since: time.Now().UTC()}
	if err := savePoolPeer(s, state); err != nil {
		t.Fatal(err)
	}
}

func restoreFilesystemTestBackend(t *testing.T, s *Store, mount string) filesystemBackupStorage {
	t.Helper()
	repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		t.Fatal(err)
	}
	return backend.(filesystemBackupStorage)
}

// A hung share must not hold the pool-wide authority lock: mount inspection and
// reads happen before it, and it covers only the commit.
func TestFilesystemDeleteAndAbortReachTheMountOutsidePoolPeerMu(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-a")
	backend := restoreFilesystemTestBackend(t, s, mount)
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	previous, previousRead := verifyBackupRepository, verifyBackupRepositoryRead
	t.Cleanup(func() { verifyBackupRepository, verifyBackupRepositoryRead = previous, previousRead })
	var calls, held int
	observe := func() {
		calls++
		if !poolPeerMu.TryLock() {
			held++
			return
		}
		poolPeerMu.Unlock()
	}
	verifyBackupRepository = func(ctx context.Context, repo protocol.BackupRepository) error {
		observe()
		return previous(ctx, repo)
	}
	verifyBackupRepositoryRead = func(ctx context.Context, repo protocol.BackupRepository) error {
		observe()
		return previousRead(ctx, repo)
	}
	if err := backend.AbortIncomplete(context.Background(), "point-lost"); err != nil {
		t.Fatal(err)
	}
	if err := backend.Delete(context.Background(), "point-a"); err != nil {
		t.Fatal(err)
	}
	if calls == 0 || held != 0 {
		t.Fatalf("mount checks: %d calls, %d ran under poolPeerMu", calls, held)
	}
}

func TestFilesystemRecordedDeleteWaitsForAuthority(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-a")
	backend := restoreFilesystemTestBackend(t, s, mount)
	raw, err := os.ReadFile(filepath.Join(mount, "point-a", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, err := vmhost.ReadBackupManifest(filepath.Join(mount, "point-a"))
	if err != nil {
		t.Fatal(err)
	}
	intent, err := json.Marshal(filesystemDeleteIntent{Manifest: raw, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if err := vmhost.AtomicWrite(filepath.Join(mount, ".nex-delete-point-a"), intent, 0600); err != nil {
		t.Fatal(err)
	}
	restrictPoolAuthority(t, s)
	backend.SweepDeleteMarkers(context.Background())
	if _, err := os.Lstat(filepath.Join(mount, "point-a", "manifest.json")); err != nil {
		t.Fatalf("a host without authority kept deleting: %v", err)
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	if err == nil {
		state.Restricted = nil
		err = savePoolPeer(s, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	backend.SweepDeleteMarkers(context.Background())
	if _, err := os.Lstat(filepath.Join(mount, "point-a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recorded delete was not finished once authority returned: %v", err)
	}
}

func TestFilesystemScanLeavesSetAsideProtectionMarkerAlone(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-a")
	backend := restoreFilesystemTestBackend(t, s, mount)
	marker := filepath.Join(mount, "point-a", ".nex-protected")
	leftover := marker + ".rm-crashed"
	if err := os.WriteFile(leftover, []byte(`{"protected":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	points, err := backend.Scan(context.Background())
	if err != nil || len(points) != 1 || !points[0].Protected {
		t.Fatalf("set-aside marker did not keep the point protected: %+v %v", points, err)
	}
	if _, err := os.Lstat(leftover); err != nil {
		t.Fatalf("scan moved the leftover: %v", err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scan restored the marker: %v", err)
	}
	if protected, err := backend.Protected(context.Background(), "point-a"); err != nil || !protected {
		t.Fatalf("Protected ignored the set-aside marker: %v %v", protected, err)
	}
	if err := vmhost.WriteBackupProtectMarker(filepath.Join(mount, "point-a"), false); err != nil {
		t.Fatal(err)
	}
	if protected, err := backend.Protected(context.Background(), "point-a"); err != nil || protected {
		t.Fatalf("unprotect did not settle the leftover: %v %v", protected, err)
	}
}

func TestFilesystemVerifyArtifactsHashesStoredContent(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-a")
	backend := restoreFilesystemTestBackend(t, s, mount)
	manifest, _, err := backend.StatPoint(context.Background(), "point-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.VerifyArtifacts(context.Background(), "point-a", manifest); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(mount, "point-a", manifest.Disks[0].Filename)
	raw, err := os.ReadFile(artifact)
	if err != nil || len(raw) == 0 {
		t.Fatalf("artifact unreadable: %v", err)
	}
	raw[0] ^= 0xff
	if err := os.WriteFile(artifact, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := backend.VerifyArtifacts(context.Background(), "point-a", manifest); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("same-size corruption not detected: %v", err)
	}
}

func TestReadBoundedBackupFileRefusesOversizeAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(file, bytes.Repeat([]byte("x"), 11), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedBackupFile(file, 10); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("oversize file read: %v", err)
	}
	if raw, err := readBoundedBackupFile(file, 11); err != nil || len(raw) != 11 {
		t.Fatalf("file at the limit refused: %v", err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedBackupFile(link, 100); err == nil {
		t.Fatal("symlink followed")
	}
}
