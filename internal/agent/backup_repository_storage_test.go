// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	if _, _, err := backend.ReadPoint(context.Background(), "point-incomplete"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete point was treated as absent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mount, "point-good", "vda.qcow2"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.ReadPoint(context.Background(), "point-good"); err == nil {
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
	if _, err := ImportPoolBackupCatalog(s, "repo-1"); err != nil {
		t.Fatal(err)
	}
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
	if err := prunePublishedBackupPayloads(s, "repo-1", "vm-1", protocol.BackupRetention{KeepLatest: 0}); err == nil {
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
	if err := prunePublishedBackupPayloads(s, "repo-1", "vm-1", protocol.BackupRetention{KeepLatest: 0}); err == nil {
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
	if _, err := RebuildPoolBackupCatalogFromRepository(s, "repo-1"); err == nil {
		t.Fatal("rebuild accepted interrupted deletion")
	}
	if err := prunePublishedBackupPayloads(s, "repo-1", "vm-1", protocol.BackupRetention{KeepLatest: 0}); err != nil {
		t.Fatalf("exact prune retry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mount, "point-old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partially deleted payload remained: %v", err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-old"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("deleted point remained cataloged: %v", err)
	}
	if _, err := RebuildPoolBackupCatalogFromRepository(s, "repo-1"); err != nil {
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
	if _, err := RebuildPoolBackupCatalogFromRepository(s, "repo-1"); err != nil {
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
	if _, err := RebuildPoolBackupCatalogFromRepository(s, "repo-1"); err != nil {
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
	if err := backend.(filesystemBackupStorage).AbortIncomplete(context.Background(), cmd.OperationID); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("abort removed a foreign staged directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(mount, ".staging-"+cmd.OperationID)); err != nil {
		t.Fatalf("foreign staged directory changed: %v", err)
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
