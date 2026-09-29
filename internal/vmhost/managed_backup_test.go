// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestClaimBackupRepositoryOwnerIsExclusive(t *testing.T) {
	root := t.TempDir()
	if err := ClaimBackupRepositoryOwner(root, "repo-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	if err := ClaimBackupRepositoryOwner(root, "repo-2", "pool-2"); err == nil {
		t.Fatal("second exclusive owner claim must fail")
	}
	repo, pool, err := ReadBackupRepositoryOwner(root)
	if err != nil || repo != "repo-1" || pool != "pool-1" {
		t.Fatalf("owner=%s pool=%s err=%v", repo, pool, err)
	}
}

func TestVerifyBackupRepositoryRejectsWrongMountedSource(t *testing.T) {
	oldRoot, oldRun := storageMountRoot, nativeRun
	storageMountRoot = t.TempDir()
	t.Cleanup(func() { storageMountRoot, nativeRun = oldRoot, oldRun })
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindNFS,
		Source:   protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
		Identity: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export/backups", FilesystemID: "0:42"},
	}
	repo.MountPath = remoteMountPath(backupStorageRegistration(repo))
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "findmnt" {
			t.Fatalf("unexpected native call: %s", name)
		}
		return []byte("other.example.test:/export/backups nfs4 rw,hard " + repo.MountPath + "\n"), nil
	}
	if err := VerifyBackupRepository(context.Background(), repo); err == nil {
		t.Fatal("backup repository must reject a different mounted NFS source")
	}
}

func TestSMBBackupRegistrationUsesSealedMountAndPrivateCredential(t *testing.T) {
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups", Directory: "pool-1"},
		SMBIdentity:   &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)},
		CredentialRef: "receipt-1",
	}
	registration := backupStorageRegistration(repo)
	if registration.Kind != protocol.StorageKindSMB || registration.Source.SMB != repo.SMBSource ||
		registration.Identity.SMB != repo.SMBIdentity || registration.CredentialReceipt != repo.CredentialRef ||
		registration.Source.NFS != nil {
		t.Fatalf("incorrect SMB backup storage registration: %+v", registration)
	}
	repo.SMBIdentity.Server = "other.example.test"
	repo.MountPath = remoteMountPath(registration)
	if err := VerifyBackupRepository(context.Background(), repo); err == nil {
		t.Fatal("backup repository must reject a changed SMB source before mount access")
	}
}

func TestBackupFilesystemPublicationProbe(t *testing.T) {
	root := t.TempDir()
	stale, recent := filepath.Join(root, ".nex-backup-probe-crashed"), filepath.Join(root, ".nex-backup-probe-running")
	for _, dir := range []string{stale, recent} {
		if err := os.MkdirAll(filepath.Join(dir, "artifact"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := probeBackupFilesystemPublication(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(recent) {
		t.Fatalf("publication probe left stale or removed live entries: %v, %v", entries, err)
	}
	if err := probeBackupFilesystemPublication(filepath.Join(root, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing repository root must fail: %v", err)
	}
}

// tempBackupSMBCredentialRoot keeps the sealed credential directory inside the test.
func tempBackupSMBCredentialRoot(t *testing.T) string {
	t.Helper()
	previous := backupSMBCredentialRoot
	backupSMBCredentialRoot = filepath.Join(t.TempDir(), "backup-smb-credentials")
	t.Cleanup(func() { backupSMBCredentialRoot = previous })
	return backupSMBCredentialRoot
}

func TestPrepareSMBBackupRefusesForeignOwnerBeforePublicationProbe(t *testing.T) {
	root := t.TempDir()
	if err := ClaimBackupRepositoryOwner(root, "foreign-repo", "foreign-pool"); err != nil {
		t.Fatal(err)
	}
	previousPrepare, previousProbe := prepareBackupSMB, probeBackupPublication
	t.Cleanup(func() { prepareBackupSMB, probeBackupPublication = previousPrepare, previousProbe })
	called := false
	prepareBackupSMB = func(context.Context, protocol.StorageRegistration, string) (storageAccess, error) {
		return storageAccess{Path: root, Observed: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{
			Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32),
		}}}, nil
	}
	probeBackupPublication = func(string) error { called = true; return nil }
	_, err := PrepareBackupRepository(context.Background(), protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		CredentialRef: "receipt-1",
	})
	if err == nil || called {
		t.Fatalf("foreign-owned repository was probed: err=%v called=%v", err, called)
	}
}

func TestPrepareSMBBackupRefusesExistingForeignOwnerBeforeNativeMountWork(t *testing.T) {
	previousRoot, previousRun := storageMountRoot, nativeRun
	storageMountRoot = t.TempDir()
	t.Cleanup(func() { storageMountRoot, nativeRun = previousRoot, previousRun })
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups", Directory: "selected"},
		CredentialRef: "receipt-1",
	}
	root := filepath.Join(remoteMountPath(backupStorageRegistration(repo)), repo.SMBSource.Directory)
	if err := ClaimBackupRepositoryOwner(root, "foreign-repo", "foreign-pool"); err != nil {
		t.Fatal(err)
	}
	nativeCalls := 0
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		nativeCalls++
		return nil, errors.New("native mount work should not begin")
	}
	credentialRoot := tempBackupSMBCredentialRoot(t)
	if _, err := PrepareBackupRepositorySMB(context.Background(), repo, protocol.StorageCredentials{Username: "backup", Secret: "private-example-secret"}); err == nil || nativeCalls != 0 {
		t.Fatalf("foreign owner was touched before refusal: err=%v native calls=%d", err, nativeCalls)
	}
	if _, err := os.Stat(filepath.Join(credentialRoot, repo.CredentialRef)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign owner caused credential materialization: %v", err)
	}
	ownerRepo, ownerPool, err := ReadBackupRepositoryOwner(root)
	if err != nil || ownerRepo != "foreign-repo" || ownerPool != "foreign-pool" {
		t.Fatalf("foreign owner changed: %q %q %v", ownerRepo, ownerPool, err)
	}
}

func TestPrepareSMBBackupRefusesForeignOwnerDiscoveredAfterMount(t *testing.T) {
	previousRun, previousMount, previousUnits := nativeRun, storageMountRoot, storageUnitRoot
	base := t.TempDir()
	storageMountRoot, storageUnitRoot = filepath.Join(base, "mounts"), filepath.Join(base, "units")
	t.Cleanup(func() { nativeRun, storageMountRoot, storageUnitRoot = previousRun, previousMount, previousUnits })
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups", Directory: "selected"},
		CredentialRef: "receipt-1",
	}
	mount := remoteMountPath(backupStorageRegistration(repo))
	root := filepath.Join(mount, repo.SMBSource.Directory)
	probeCalls := 0
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "id":
			return []byte("1000\n"), nil
		case "findmnt":
			return []byte("//smb.example.test/backups cifs rw,vers=3.1.1,seal,cache=strict,uid=1000,gid=1000,file_mode=0600,dir_mode=0700 " + mount + "\n"), nil
		case "systemctl":
			if len(args) > 0 && args[0] == "enable" {
				if err := ClaimBackupRepositoryOwner(root, "foreign-repo", "foreign-pool"); err != nil {
					t.Fatal(err)
				}
			}
			return nil, nil
		case "runuser":
			probeCalls++
			return nil, errors.New("QEMU probe must not touch a foreign repository")
		}
		return nil, errors.New("unexpected native command")
	}
	tempBackupSMBCredentialRoot(t)
	if _, err := PrepareBackupRepositorySMB(context.Background(), repo, protocol.StorageCredentials{Username: "backup", Secret: "private-example-secret"}); err == nil {
		t.Fatal("foreign owner discovered after mount was accepted")
	}
	if probeCalls != 0 {
		t.Fatalf("QEMU probed foreign repository %d times", probeCalls)
	}
	if _, err := os.Stat(filepath.Join(mount, smbMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("share marker was written before owner proof: %v", err)
	}
}

func TestVerifySMBBackupRefusesForeignOwnerBeforePublicationProbe(t *testing.T) {
	root := t.TempDir()
	if err := ClaimBackupRepositoryOwner(root, "foreign-repo", "foreign-pool"); err != nil {
		t.Fatal(err)
	}
	previousInspect, previousProbe := inspectBackupSMB, probeBackupPublication
	t.Cleanup(func() { inspectBackupSMB, probeBackupPublication = previousInspect, previousProbe })
	inspectBackupSMB = func(context.Context, protocol.StorageRegistration) (storageAccess, error) {
		return storageAccess{Path: root, Observed: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{
			Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32),
		}}}, nil
	}
	called := false
	probeBackupPublication = func(string) error { called = true; return nil }
	err := VerifyBackupRepository(context.Background(), protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		SMBIdentity:   &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)},
		CredentialRef: "receipt-1", MountPath: root,
	})
	if err == nil || called {
		t.Fatalf("foreign-owned repository was probed: err=%v called=%v", err, called)
	}
}

func TestVerifySMBBackupReadSkipsPublicationProbe(t *testing.T) {
	root := t.TempDir()
	if err := ClaimBackupRepositoryOwner(root, "repo-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	previousInspect, previousQEMU, previousProbe := inspectBackupSMB, probeBackupQEMUAccess, probeBackupPublication
	t.Cleanup(func() {
		inspectBackupSMB, probeBackupQEMUAccess, probeBackupPublication = previousInspect, previousQEMU, previousProbe
	})
	inspectBackupSMB = func(context.Context, protocol.StorageRegistration) (storageAccess, error) {
		return storageAccess{Path: root, Observed: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{
			Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32),
		}}}, nil
	}
	qemuCalls, publicationCalls := 0, 0
	probeBackupQEMUAccess = func(context.Context, string) error { qemuCalls++; return nil }
	probeBackupPublication = func(string) error { publicationCalls++; return errors.New("repository is read-only") }
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		SMBIdentity:   &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)},
		CredentialRef: "receipt-1", MountPath: root,
	}
	if err := VerifyBackupRepositoryRead(context.Background(), repo); err != nil {
		t.Fatalf("read verification: %v", err)
	}
	if qemuCalls != 0 || publicationCalls != 0 {
		t.Fatalf("read verification wrote to repository: QEMU=%d publication=%d", qemuCalls, publicationCalls)
	}
	if err := VerifyBackupRepository(context.Background(), repo); err == nil {
		t.Fatal("writable verification accepted denied publication")
	}
	if qemuCalls != 1 || publicationCalls != 1 {
		t.Fatalf("writable verification skipped probes: QEMU=%d publication=%d", qemuCalls, publicationCalls)
	}
	repo.SMBIdentity.MarkerID = strings.Repeat("b", 32)
	if err := VerifyBackupRepositoryRead(context.Background(), repo); err == nil {
		t.Fatal("read verification accepted wrong share marker")
	}
}

func TestPrepareBackupRepositorySMBSealsCredentialFile(t *testing.T) {
	root, credentialRoot := t.TempDir(), tempBackupSMBCredentialRoot(t)
	var seen string
	previousPrepare := prepareBackupSMB
	t.Cleanup(func() { prepareBackupSMB = previousPrepare })
	prepareBackupSMB = func(ctx context.Context, registration protocol.StorageRegistration, _ string) (storageAccess, error) {
		credential, err := credentialFile(ctx, registration)
		if err != nil {
			return storageAccess{}, err
		}
		info, err := os.Stat(credential)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("credential mode: %v, %v", info, err)
		}
		if data, err := os.ReadFile(credential); err != nil || string(data) != "sealed:username=backup\npassword=private-example-secret\ndomain=EXAMPLE\n" {
			t.Fatalf("sealed credential body: %q, %v", data, err)
		}
		seen = credential
		return storageAccess{Path: root, Observed: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{
			Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32),
		}}}, nil
	}
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"}, CredentialRef: "receipt-1",
	}
	creds := protocol.StorageCredentials{Username: "backup", Secret: "private-example-secret", Domain: "EXAMPLE"}
	for i := 0; i < 2; i++ {
		prepared, err := PrepareBackupRepositorySMB(context.Background(), repo, creds)
		if err != nil || prepared.MountPath != root || prepared.SMBIdentity == nil {
			t.Fatalf("prepare %d: %+v, %v", i, prepared, err)
		}
		if seen != filepath.Join(credentialRoot, repo.CredentialRef) {
			t.Fatalf("mount read %q, not the sealed credential file", seen)
		}
	}
	if err := DetachBackupRepository(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seen); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sealed credential survived detach: %v", err)
	}
	for _, domain := range []string{"EX,AMPLE", "EX=AMPLE", "EX\nAMPLE", "EX AMPLE"} {
		bad := creds
		bad.Domain = domain
		if _, err := PrepareBackupRepositorySMB(context.Background(), repo, bad); err == nil {
			t.Fatalf("domain %q accepted", domain)
		}
	}
}

// Regression for BUG-0751: preparing a mount that already uses the sealed file must not seal again.
func TestPrepareBackupRepositorySMBKeepsSealedFileOfMountedRepository(t *testing.T) {
	root, credentialRoot := t.TempDir(), tempBackupSMBCredentialRoot(t)
	previousUnits, previousPrepare := storageUnitRoot, prepareBackupSMB
	storageUnitRoot = t.TempDir()
	t.Cleanup(func() { storageUnitRoot, prepareBackupSMB = previousUnits, previousPrepare })
	prepareBackupSMB = func(context.Context, protocol.StorageRegistration, string) (storageAccess, error) {
		return storageAccess{Path: root, Observed: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{
			Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32),
		}}}, nil
	}
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"}, CredentialRef: "receipt-1",
	}
	registration := backupStorageRegistration(repo)
	_, unitPath := remoteUnitPath(registration)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0700); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(credentialRoot, repo.CredentialRef)
	unit := remoteUnit("//smb.example.test/backups", remoteMountPath(registration), "cifs", smbMountOptions("1000", "1000"), sealed)
	if err := os.WriteFile(unitPath, unit, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(credentialRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sealed, []byte("already sealed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareBackupRepositorySMB(context.Background(), repo, protocol.StorageCredentials{Username: "backup", Secret: "private-example-secret"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(sealed); err != nil || string(got) != "already sealed" {
		t.Fatalf("prepare resealed the file: %q %v", got, err)
	}
}

func TestPrepareBackupRepositorySMBRejectsCredentialRotationBeforeWritingMaterial(t *testing.T) {
	credentialRoot := tempBackupSMBCredentialRoot(t)
	previousUnits := storageUnitRoot
	storageUnitRoot = t.TempDir()
	t.Cleanup(func() { storageUnitRoot = previousUnits })
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		CredentialRef: "new-receipt",
	}
	registration := backupStorageRegistration(repo)
	_, unitPath := remoteUnitPath(registration)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0700); err != nil {
		t.Fatal(err)
	}
	oldUnit := remoteUnit("//smb.example.test/backups", remoteMountPath(registration), "cifs",
		smbMountOptions("1000", "1000"), filepath.Join(credentialRoot, "old-receipt"))
	if err := os.WriteFile(unitPath, oldUnit, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareBackupRepositorySMB(context.Background(), repo, protocol.StorageCredentials{Username: "backup", Secret: "new-secret"}); err == nil {
		t.Fatal("mounted repository accepted credential rotation without a dependency-safe remount")
	}
	if data, err := os.ReadFile(unitPath); err != nil || string(data) != string(oldUnit) {
		t.Fatalf("failed rotation changed the mount unit: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(credentialRoot, repo.CredentialRef)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed rotation materialized new credentials: %v", err)
	}
}

func TestPublishBackupRecoveryPointRequiresCompleteManifest(t *testing.T) {
	root := t.TempDir()
	if err := ClaimBackupRepositoryOwner(root, "repo-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join(t.TempDir(), "vda.qcow2")
	if err := os.WriteFile(diskPath, []byte("standalone-disk"), 0600); err != nil {
		t.Fatal(err)
	}
	sum, size, err := fileSHA256(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1",
		PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: size, SHA256: sum}},
		Complete: true, Success: 1,
	}
	digest, err := PublishBackupRecoveryPoint(root, manifest, map[string]string{"vda.qcow2": diskPath})
	if err != nil || len(digest) != 64 {
		t.Fatalf("publish: digest=%q err=%v", digest, err)
	}
	if _, err := os.Stat(filepath.Join(root, "point-1", backupManifestName)); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), backupStagingPrefix) {
			t.Fatalf("staging directory remained after publish: %s", entry.Name())
		}
	}
	incomplete := manifest
	incomplete.RecoveryPointID = "point-2"
	incomplete.Success = 0
	if _, err := PublishBackupRecoveryPoint(root, incomplete, map[string]string{"vda.qcow2": diskPath}); err == nil {
		t.Fatal("success=0 must never publish")
	}
	if _, err := os.Stat(filepath.Join(root, "point-2")); !os.IsNotExist(err) {
		t.Fatal("incomplete recovery point must not exist")
	}
}

func TestPublishBackupRecoveryPointExactRetrySettlesWithoutRepublish(t *testing.T) {
	root := t.TempDir()
	if err := ClaimBackupRepositoryOwner(root, "repo-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join(t.TempDir(), "vda.qcow2")
	if err := os.WriteFile(diskPath, []byte("standalone-disk"), 0600); err != nil {
		t.Fatal(err)
	}
	sum, size, err := fileSHA256(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-retry", RepositoryID: "repo-1",
		PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: size, SHA256: sum}},
		Complete: true, Success: 1,
	}
	first, err := PublishBackupRecoveryPoint(root, manifest, map[string]string{"vda.qcow2": diskPath})
	if err != nil || len(first) != 64 {
		t.Fatalf("first publish: digest=%q err=%v", first, err)
	}
	before, err := os.ReadFile(filepath.Join(root, "point-retry", backupManifestName))
	if err != nil {
		t.Fatal(err)
	}
	retried := manifest
	retried.CapturedAt = retried.CapturedAt.Add(time.Minute)
	again, err := PublishBackupRecoveryPoint(root, retried, map[string]string{"vda.qcow2": diskPath})
	if err != nil || again != first {
		t.Fatalf("exact retry must reuse published digest: got=%q want=%q err=%v", again, first, err)
	}
	after, err := os.ReadFile(filepath.Join(root, "point-retry", backupManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("exact retry must not rewrite the published manifest")
	}
	changed := manifest
	changed.SpecSHA256 = strings.Repeat("c", 64)
	if _, err := PublishBackupRecoveryPoint(root, changed, map[string]string{"vda.qcow2": diskPath}); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("different input against the same point id must conflict: %v", err)
	}
	changed = manifest
	changed.Disks = append([]protocol.BackupDiskArtifact(nil), manifest.Disks...)
	changed.Disks[0].SHA256 = strings.Repeat("c", 64)
	if _, err := PublishBackupRecoveryPoint(root, changed, map[string]string{"vda.qcow2": diskPath}); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("different artifact against the same point id must conflict: %v", err)
	}
}

func TestReadBackupManifestRefusesCorruptPayload(t *testing.T) {
	root := t.TempDir()
	point := filepath.Join(root, "point-1")
	if err := os.MkdirAll(point, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, backupManifestName), []byte(`{"version":1,"complete":true,"success":1`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBackupManifest(point); err == nil {
		t.Fatal("corrupt manifest must be refused")
	}
	bad := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1",
		PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: 1, SHA256: strings.Repeat("c", 64)}},
		Complete: true, Success: 0,
	}
	raw, _ := json.Marshal(bad)
	if err := os.WriteFile(filepath.Join(point, backupManifestName), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBackupManifest(point); err == nil {
		t.Fatal("success=0 manifest must be refused")
	}
}

func TestCopyBackupArtifactsLocalPreservesDigests(t *testing.T) {
	root := t.TempDir()
	if err := ClaimBackupRepositoryOwner(root, "repo-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join(t.TempDir(), "vda.qcow2")
	payload := []byte("restore-source")
	if err := os.WriteFile(diskPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1",
		PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:])}},
		Complete: true, Success: 1,
	}
	if _, err := PublishBackupRecoveryPoint(root, manifest, map[string]string{"vda.qcow2": diskPath}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "restore-local")
	if err := CopyBackupArtifactsLocal(context.Background(), filepath.Join(root, "point-1"), local, manifest); err != nil {
		t.Fatal(err)
	}
	got, size, err := fileSHA256(filepath.Join(local, "vda.qcow2"))
	if err != nil || got != hex.EncodeToString(sum[:]) || size != int64(len(payload)) {
		t.Fatalf("local copy digest=%s size=%d err=%v", got, size, err)
	}
	if err := CopyBackupArtifactsLocal(context.Background(), filepath.Join(root, "point-1"), local, manifest); err != nil {
		t.Fatalf("same-operation copy retry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "vda.qcow2"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CopyBackupArtifactsLocal(context.Background(), filepath.Join(root, "point-1"), local, manifest); err != nil {
		t.Fatalf("interrupted copy retry: %v", err)
	}
	got, size, err = fileSHA256(filepath.Join(local, "vda.qcow2"))
	if err != nil || got != hex.EncodeToString(sum[:]) || size != int64(len(payload)) {
		t.Fatalf("retried copy digest=%s size=%d err=%v", got, size, err)
	}
}

func TestReadBackupManifestChecksCapturedSpecDigest(t *testing.T) {
	vm := managedTestVM()
	vm.Boot.Firmware = "bios"
	vm.MediaID = ""
	vm.Boot.MediaBootOrder = 0
	specDigest, _, err := BackupSpecAndDiskDigests(vm, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildBackupManifest("point-1", "repo-1", "pool-1", vm.ID, vm.Name,
		protocol.BackupCaptureStopped, specDigest, strings.Repeat("b", 64), vm,
		[]protocol.BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", SHA256: strings.Repeat("a", 64), Size: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest.CapturedSpec.Name = "changed after capture"
	point := filepath.Join(t.TempDir(), "point-1")
	if err := os.Mkdir(point, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, backupManifestName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBackupManifest(point); err == nil {
		t.Fatal("changed captured spec must not restore")
	}
}

func TestRestoreSpecRejectsUnsafeOrWrongVMIdentity(t *testing.T) {
	vm := managedTestVM()
	vm.Boot.Firmware = "bios"
	vm.MediaID = ""
	vm.Boot.MediaBootOrder = 0
	vm.Disks[0].BootOrder = 1
	if _, err := BuildRestoreAsNewSpec(vm, protocol.RestoreAsNewRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: "../other-vm",
		NewVMName: "restored", StorageID: "storage-1",
	}, "pool-1", "host-1"); err == nil {
		t.Fatal("restore VM ID must not escape its storage directory")
	}
	if _, err := BuildRestoreAsNewSpec(vm, protocol.RestoreAsNewRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: "22222222-2222-4222-8222-222222222222",
		NewVMName: "restored", StorageID: "storage-1", HostID: "other-host",
	}, "pool-1", "host-1"); err == nil {
		t.Fatal("restore must not place on a different host than requested")
	}
	other := vm
	other.ID = "22222222-2222-4222-8222-222222222222"
	if _, err := BuildRestoreReplaceSpec(vm, other, protocol.RestoreReplaceRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", OriginalVMID: vm.ID,
		StorageID: "storage-1", ConfirmReplace: true,
	}); err == nil {
		t.Fatal("replace must reject another VM's recovery point")
	}
}

func TestRestoreFirmwareRequiresMatchingState(t *testing.T) {
	vm := managedTestVM()
	manifest := protocol.BackupManifest{CapturedSpec: &vm}
	if err := ValidateBackupFirmwareForRestore(manifest); err == nil {
		t.Fatal("UEFI restore must refuse missing NVRAM")
	}
	manifest.Firmware = []protocol.BackupFirmwareArtifact{{Kind: "nvram", Filename: "nvram", SHA256: strings.Repeat("a", 64), Size: 1}}
	if err := ValidateBackupFirmwareForRestore(manifest); err != nil {
		t.Fatalf("complete UEFI NVRAM should be supported: %v", err)
	}
	vm.Boot.Firmware = "bios"
	vm.Boot.TPM = protocol.TPM20
	if err := ValidateBackupFirmwareForRestore(manifest); err == nil {
		t.Fatal("TPM restore must refuse missing state")
	}
	manifest.Firmware = []protocol.BackupFirmwareArtifact{{Kind: "swtpm", Filename: "swtpm.tar", SHA256: strings.Repeat("b", 64), Size: 1}}
	if err := ValidateBackupFirmwareForRestore(manifest); err != nil {
		t.Fatalf("complete TPM artifact should pass profile validation: %v", err)
	}
	vm.Boot.TPM = protocol.TPMNone
	manifest.Firmware = nil
	if err := ValidateBackupFirmwareForRestore(manifest); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRestoreNVRAMUsesNewExclusivePathAndVerifiesDigest(t *testing.T) {
	oldDir, oldOwner := firmwareVariablesDir, backupNVRAMOwner
	firmwareVariablesDir = t.TempDir()
	backupNVRAMOwner = func(context.Context) (int, int, error) { return os.Getuid(), os.Getgid(), nil }
	t.Cleanup(func() { firmwareVariablesDir, backupNVRAMOwner = oldDir, oldOwner })
	localDir := t.TempDir()
	payload := []byte("captured-uefi-variables")
	if err := os.WriteFile(filepath.Join(localDir, "nvram"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	artifact := protocol.BackupFirmwareArtifact{Kind: "nvram", Filename: "nvram", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload))}
	vm := managedTestVM()
	path, err := installRestoreNVRAM(context.Background(), vm, "restore-1", localDir, []protocol.BackupFirmwareArtifact{artifact}, false, false)
	if err != nil || path != firmwareVariablesPath(vm.ID) {
		t.Fatalf("as-new NVRAM path=%q err=%v", path, err)
	}
	if err := verifyRestoreNVRAM(context.Background(), path, artifact); err != nil {
		t.Fatal(err)
	}
	if _, err := installRestoreNVRAM(context.Background(), vm, "other-operation", localDir, []protocol.BackupFirmwareArtifact{artifact}, false, false); err == nil {
		t.Fatal("a second operation must not overwrite the new VM's NVRAM")
	}
	replacement, err := installRestoreNVRAM(context.Background(), vm, "replace-1", localDir, []protocol.BackupFirmwareArtifact{artifact}, false, true)
	if err != nil || replacement == path {
		t.Fatalf("replace must retain original NVRAM: path=%q err=%v", replacement, err)
	}
	if err := os.WriteFile(replacement, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installRestoreNVRAM(context.Background(), vm, "replace-1", localDir, []protocol.BackupFirmwareArtifact{artifact}, true, true); err != nil {
		t.Fatalf("reserved NVRAM retry: %v", err)
	}
	if err := verifyRestoreNVRAM(context.Background(), replacement, artifact); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := installRestoreNVRAM(context.Background(), vm, "replace-1", localDir, []protocol.BackupFirmwareArtifact{artifact}, true, true); err == nil {
		t.Fatal("NVRAM retry must refuse a hard link to another VM's variables")
	}
}

func TestInstallRestoreTPMUsesReservedStateDirectory(t *testing.T) {
	oldOwner := backupTPMOwner
	oldRoot := backupTPMStateRoot
	backupTPMStateRoot = t.TempDir()
	backupTPMOwner = func() (int, int, error) { return os.Getuid(), os.Getgid(), nil }
	t.Cleanup(func() { backupTPMOwner = oldOwner; backupTPMStateRoot = oldRoot })
	localDir := t.TempDir()
	archivePath := filepath.Join(localDir, "swtpm.tar")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	w := tar.NewWriter(f)
	for _, name := range []string{"swtpm-tree/", "swtpm-tree/tpm2/"} {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0700, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	payload := []byte("sealed-tpm-state")
	if err := w.WriteHeader(&tar.Header{Name: "swtpm-tree/tpm2/tpm2-00.permall", Mode: 0600, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	sum, size, err := fileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	artifact := protocol.BackupFirmwareArtifact{Kind: "swtpm", Filename: "swtpm.tar", SHA256: sum, Size: size}
	vm := managedTestVM()
	vm.Boot.TPM = protocol.TPM20
	stale := filepath.Join(backupTPMStateRoot, vm.ID+".staging")
	if err := os.Mkdir(stale, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "unrelated"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	tpmPath, digest, err := installRestoreTPM(context.Background(), vm, "restore-1", localDir, []protocol.BackupFirmwareArtifact{artifact}, false, false, false)
	if err != nil || tpmPath != filepath.Join(backupTPMStateRoot, vm.ID, "tpm2") || digest == "" {
		t.Fatalf("TPM placement=%q digest=%q err=%v", tpmPath, digest, err)
	}
	if got, err := os.ReadFile(filepath.Join(stale, "unrelated")); err != nil || string(got) != "keep" {
		t.Fatalf("unrelated staging path changed: %q, %v", got, err)
	}
	original := filepath.Join(tpmPath, "tpm2-00.permall")
	if got, err := os.ReadFile(original); err != nil || string(got) != string(payload) {
		t.Fatalf("TPM payload=%q err=%v", got, err)
	}
	if root, err := os.Stat(filepath.Dir(tpmPath)); err != nil || root.Mode().Perm() != 0711 {
		t.Fatalf("restored TPM outer directory must be traversable like libvirt's native state root: %v, %v", root, err)
	}
	vm.NICs = nil
	domain, err := managedDomainXML(vm, nil, []string{"/tmp/disk.qcow2"}, []string{"vda"}, "sda", "", "sdb", "")
	if err != nil {
		t.Fatal(err)
	}
	domain, err = setRestoreFirmware(domain, "")
	if err != nil {
		t.Fatal(err)
	}
	var parsed managedDomain
	if err := xml.Unmarshal(domain, &parsed); err != nil || parsed.Devices.TPM.Backend.Source != nil {
		t.Fatalf("restored TPM must use libvirt UUID source: %v", err)
	}
	if _, _, err := installRestoreTPM(context.Background(), vm, "another-restore", localDir, []protocol.BackupFirmwareArtifact{artifact}, false, false, false); err == nil {
		t.Fatal("first restore must not overwrite preexisting UUID TPM state")
	}
	_, retryDigest, err := installRestoreTPM(context.Background(), vm, "restore-1", localDir, []protocol.BackupFirmwareArtifact{artifact}, true, false, false)
	if err != nil || retryDigest != digest {
		t.Fatalf("exact reserved TPM retry digest=%q err=%v", retryDigest, err)
	}
	if err := os.WriteFile(original, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := installRestoreTPM(context.Background(), vm, "restore-1", localDir, []protocol.BackupFirmwareArtifact{artifact}, true, false, false); err == nil {
		t.Fatal("changed TPM tree must not be deleted during retry")
	}
	if got, err := os.ReadFile(original); err != nil || string(got) != "partial" {
		t.Fatalf("changed TPM state was overwritten: %q err=%v", got, err)
	}
}

func TestManagedTPMStateSourceRequiresDefaultUUIDTree(t *testing.T) {
	oldRoot := backupTPMStateRoot
	backupTPMStateRoot = t.TempDir()
	t.Cleanup(func() { backupTPMStateRoot = oldRoot })
	vm := managedTestVM()
	domain := managedDomain{UUID: vm.ID}
	domain.Devices.TPM = emulatedTPM()
	defaultBase := filepath.Join(backupTPMStateRoot, vm.ID)
	for _, source := range []string{"", filepath.Join(defaultBase, "tpm2")} {
		if source == "" {
			domain.Devices.TPM.Backend.Source = nil
		} else {
			domain.Devices.TPM.Backend.Source = &managedTPMSource{Type: "dir", Path: source}
		}
		got, err := managedTPMStateSource(domain, vm.ID)
		if err != nil || got != defaultBase {
			t.Fatalf("source %q: got %q, want %q: %v", source, got, defaultBase, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(defaultBase, "tpm2"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultBase, "tpm2", "tpm2-00.permall"), []byte("state"), 0600); err != nil {
		t.Fatal(err)
	}
	domain.Devices.TPM.Backend.Source = &managedTPMSource{Type: "dir", Path: filepath.Join(defaultBase, "tpm2")}
	artifacts, err := snapshotFirmwareSources(context.Background(), domain, vm.ID, t.TempDir(), "backup")
	if err != nil || len(artifacts) != 1 || artifacts[0].SourcePath != defaultBase || !artifacts[0].SourceManifest.Exists {
		t.Fatalf("restored TPM capture source: %+v, %v", artifacts, err)
	}
	if _, err := copyManagedSnapshotFirmware(context.Background(), artifacts[0].SourcePath, filepath.Join(t.TempDir(), "swtpm-tree")); err != nil {
		t.Fatalf("capturing restored TPM tree: %v", err)
	}
	domain.Devices.TPM.Backend.Source.Path = filepath.Join(backupTPMStateRoot, "foreign", "tpm2")
	if _, err := managedTPMStateSource(domain, vm.ID); err == nil {
		t.Fatal("foreign TPM tree must be refused")
	}
}

func TestBackupTPMRootAccessibleCreatesOnlyMissingNativeRoot(t *testing.T) {
	oldRoot := backupTPMStateRoot
	parent := t.TempDir()
	backupTPMStateRoot = filepath.Join(parent, "swtpm")
	t.Cleanup(func() { backupTPMStateRoot = oldRoot })
	if err := backupTPMRootAccessible(os.Getuid(), os.Getgid(), false); err != nil {
		t.Fatalf("first TPM restore should create native state root: %v", err)
	}
	info, err := os.Lstat(backupTPMStateRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0711 {
		t.Fatalf("native state root must be a traversable directory: %v %v", info, err)
	}
	if err := os.Remove(backupTPMStateRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent, backupTPMStateRoot); err != nil {
		t.Fatal(err)
	}
	if err := backupTPMRootAccessible(os.Getuid(), os.Getgid(), false); err == nil {
		t.Fatal("symlinked TPM state root was accepted")
	}
	if err := os.Remove(backupTPMStateRoot); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{parent, filepath.Dir(parent)} {
		if err := os.Chmod(dir, 0711); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(backupTPMStateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := backupTPMRootAccessible(os.Getuid()+1, os.Getgid()+1, false); err == nil {
		t.Fatal("preexisting inaccessible TPM root was accepted without reserved retry")
	}
	if err := backupTPMRootAccessible(os.Getuid()+1, os.Getgid()+1, true); err != nil {
		t.Fatalf("exact interrupted creation should repair empty native root: %v", err)
	}
	if err := os.Chmod(backupTPMStateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupTPMStateRoot, "foreign"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := backupTPMRootAccessible(os.Getuid()+1, os.Getgid()+1, true); err == nil {
		t.Fatal("nonempty inaccessible TPM root was repaired")
	}
	foreign := t.TempDir()
	foreignRoot := filepath.Join(foreign, "swtpm")
	if err := os.Mkdir(foreignRoot, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "alias")
	if err := os.Symlink(foreign, link); err != nil {
		t.Fatal(err)
	}
	backupTPMStateRoot = filepath.Join(link, "swtpm")
	if err := backupTPMRootAccessible(os.Getuid()+1, os.Getgid()+1, true); err == nil {
		t.Fatal("symlinked ancestor was accepted for TPM root repair")
	}
	if info, err := os.Stat(foreignRoot); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("foreign TPM root was modified through symlink: %v %v", info, err)
	}
}

func TestRestoreTPMRootMustBeTraversableBySwtpm(t *testing.T) {
	oldRoot := backupTPMStateRoot
	backupTPMStateRoot = t.TempDir()
	t.Cleanup(func() { backupTPMStateRoot = oldRoot })
	if err := os.Chmod(backupTPMStateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := backupTPMRootAccessible(os.Getuid()+1, os.Getgid()+1, false); err == nil {
		t.Fatal("TPM root inaccessible to swtpm must be refused")
	}
}

func TestReplaceTPMExchangeRetainsOriginalAndRefusesReplay(t *testing.T) {
	oldRoot := backupTPMStateRoot
	backupTPMStateRoot = t.TempDir()
	t.Cleanup(func() { backupTPMStateRoot = oldRoot })
	vm := managedTestVM()
	vm.Revision = 1
	current := filepath.Join(backupTPMStateRoot, vm.ID)
	staged := restoreTPMPath(vm.ID, "restore-1")
	for path, value := range map[string]string{current: "old-state", staged: "restored-state"} {
		if err := os.MkdirAll(filepath.Join(path, "tpm2"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0711); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "tpm2", "tpm2-00.permall"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	oldManifest, err := managedSnapshotManifest(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	newManifest, err := managedSnapshotManifest(context.Background(), staged)
	if err != nil {
		t.Fatal(err)
	}
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	operationID := "restore-1"
	j := managedJournal{
		Command: protocol.Command{OperationID: operationID, Kind: "vm-update", VM: &vm},
		Result:  protocol.Result{OperationID: operationID, Status: "pending"}, Stage: "swapping-tpm",
		BackupTPMPreviousSHA256: oldManifest.SHA256, BackupTPMManifestSHA256: newManifest.SHA256,
	}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "virsh", `case "$3" in domstate) echo 'shut off';; *) exit 1;; esac`)
	if result, err := e.ReconcileExclusiveReplaceCutover(context.Background(), operationID); err != nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "did not land") {
		t.Fatalf("pre-exchange reconciliation: %+v, %v", result, err)
	}
	if err := exchangeReplaceTPM(context.Background(), vm.ID, "restore-1", oldManifest.SHA256, newManifest.SHA256); err != nil {
		t.Fatal(err)
	}
	if result, err := e.ReconcileExclusiveReplaceCutover(context.Background(), operationID); err != nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "landed") {
		t.Fatalf("post-exchange reconciliation must not replay definition: %+v, %v", result, err)
	}
	if got, _ := os.ReadFile(filepath.Join(current, "tpm2", "tpm2-00.permall")); string(got) != "restored-state" {
		t.Fatalf("default UUID tree did not receive restored state: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(staged, "tpm2", "tpm2-00.permall")); string(got) != "old-state" {
		t.Fatalf("old state not retained at operation path: %q", got)
	}
	if err := exchangeReplaceTPM(context.Background(), vm.ID, "restore-1", oldManifest.SHA256, newManifest.SHA256); err == nil {
		t.Fatal("ambiguous exchange replay must be refused")
	}
}

func TestUnpackRestoreTPMRejectsUnsafeArchiveEntries(t *testing.T) {
	for _, entry := range []tar.Header{
		{Name: "swtpm-tree/tpm2/../escape", Typeflag: tar.TypeReg, Size: 1},
		{Name: "swtpm-tree/tpm2/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	} {
		t.Run(entry.Name, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), "swtpm.tar")
			f, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			w := tar.NewWriter(f)
			if err := w.WriteHeader(&entry); err != nil {
				t.Fatal(err)
			}
			if entry.Size > 0 {
				_, _ = w.Write([]byte("x"))
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if err := unpackRestoreTPM(context.Background(), archivePath, t.TempDir(), os.Getuid(), os.Getgid()); err == nil {
				t.Fatal("unsafe TPM archive entry must be refused")
			}
		})
	}
}

func TestReadBackupManifestRefusesBasenameMismatch(t *testing.T) {
	root := t.TempDir()
	if err := ClaimBackupRepositoryOwner(root, "repo-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join(t.TempDir(), "vda.qcow2")
	if err := os.WriteFile(diskPath, []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	sum, size, err := fileSHA256(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-real", RepositoryID: "repo-1",
		PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: size, SHA256: sum}},
		Complete: true, Success: 1,
	}
	if _, err := PublishBackupRecoveryPoint(root, manifest, map[string]string{"vda.qcow2": diskPath}); err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(root, "point-alias")
	if err := os.Rename(filepath.Join(root, "point-real"), wrong); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadBackupManifest(wrong); err == nil {
		t.Fatal("basename mismatch must be refused")
	}
	scanned, err := ScanBackupRepositoryPoints(root, "repo-1", "pool-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) != 0 {
		t.Fatalf("mismatched directory must not enter catalog: %#v", scanned)
	}
}

func TestCopyAndHashRefuseSymlinkArtifacts(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.qcow2")
	if err := os.WriteFile(real, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.qcow2")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fileSHA256(link); err == nil {
		t.Fatal("hash must refuse symlink")
	}
	dest := filepath.Join(dir, "out.qcow2")
	if err := copyRegularFileExclusive(link, dest); err == nil {
		t.Fatal("copy must refuse symlink source")
	}
}

func TestBuildRestoreAsNewSpecUsesNewIdentityAndDisconnectedNICs(t *testing.T) {
	captured := managedTestVM()
	captured.Boot.Firmware = "bios"
	captured.MediaID = ""
	captured.Boot.MediaBootOrder = 0
	captured.Disks[0].BootOrder = 1
	req := protocol.RestoreAsNewRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1",
		NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored-guest", StorageID: "storage-restore",
	}
	got, err := BuildRestoreAsNewSpec(captured, req, "pool-x", "host-y")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != req.NewVMID || got.Name != req.NewVMName || got.HostID != "host-y" || got.PoolID != "pool-x" {
		t.Fatalf("identity: %#v", got)
	}
	if got.StartAfterCreate || got.Autostart || len(got.NICs) != 0 || got.MediaID != "" {
		t.Fatalf("must stay stopped and disconnected: %#v", got)
	}
	if got.Disks[0].StorageID != req.StorageID {
		t.Fatalf("storage remap: %#v", got.Disks)
	}
}

func TestBuildRestoreReplaceSpecKeepsIdentityAndDisconnectsNICs(t *testing.T) {
	current := managedTestVM()
	current.Revision = 3
	current.NICs = []protocol.VMNIC{{NetworkID: "n", Model: "virtio"}}
	captured := managedTestVM()
	captured.Boot.Firmware = "bios"
	captured.MediaID = ""
	captured.Boot.MediaBootOrder = 0
	captured.Disks[0].BootOrder = 1
	captured.Name = "captured-name"
	req := protocol.RestoreReplaceRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1",
		OriginalVMID: current.ID, StorageID: "storage-replace", ConfirmReplace: true,
	}
	got, err := BuildRestoreReplaceSpec(current, captured, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != current.ID || got.Name != current.Name || got.HostID != current.HostID || got.PoolID != current.PoolID {
		t.Fatalf("must keep original identity: %#v", got)
	}
	if got.Revision != 4 || len(got.NICs) != 0 || got.StartAfterCreate || got.Autostart || got.MediaID != "" {
		t.Fatalf("replace must bump revision and disconnect: %#v", got)
	}
	if got.Disks[0].StorageID != req.StorageID {
		t.Fatalf("storage remap: %#v", got.Disks)
	}
}

func TestApplyExclusiveReplaceCutoverPlacesNewDisksAndRetainsOriginals(t *testing.T) {
	storageRoot := t.TempDir()
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Boot.Firmware = "bios"
	vm.MediaID = ""
	vm.Boot.MediaBootOrder = 0
	vm.Disks = []protocol.VMDisk{{StorageID: "s", Name: "disk", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}
	vmDir := filepath.Join(storageRoot, "nex-vms", vm.ID)
	if err := os.MkdirAll(vmDir, 0711); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(vmDir, "disk-0.qcow2")
	if err := os.WriteFile(original, []byte("original-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Generation: 1, Path: storageRoot, Kind: protocol.StorageKindLocal}
	if err := e.save(managedJournal{
		Command:        protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &storage},
		AppliedStorage: &storage, Result: protocol.Result{OperationID: "storage-1", Status: "succeeded"},
	}); err != nil {
		t.Fatal(err)
	}
	targets, media, drivers := managedTargets(vm)
	if err := e.save(managedJournal{
		Command: protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &vm},
		Result:  protocol.Result{OperationID: "create-1", Status: "succeeded"},
		Stage:   "defined", Disks: []string{original}, DiskChains: [][]string{{original}},
		Targets: targets, MediaTarget: media, DriversTarget: drivers,
	}); err != nil {
		t.Fatal(err)
	}
	payload := []byte("restored-disk-bytes")
	sum := sha256.Sum256(payload)
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "vda.qcow2"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	artifacts := []protocol.BackupDiskArtifact{{
		Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: int64(len(payload)), SHA256: hex.EncodeToString(sum[:]),
	}}
	spec, err := BuildRestoreReplaceSpec(vm, vm, protocol.RestoreReplaceRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1",
		OriginalVMID: vm.ID, StorageID: "s", ConfirmReplace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, got protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		return storageAccess{Path: storageRoot, Identity: "local:" + storageRoot, Total: 1 << 40, Available: 1 << 40, Health: "healthy"}, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	previousCapacity := replaceCutoverCapacity
	replaceCutoverCapacity = func(context.Context, protocol.VMCreateSpec, []managedJournal) error { return nil }
	t.Cleanup(func() { replaceCutoverCapacity = previousCapacity })
	previousDirs := replaceCutoverPrepareDirs
	replaceCutoverPrepareDirs = func(_ context.Context, _ []managedJournal, _ string, path string) error {
		return os.MkdirAll(path, 0711)
	}
	t.Cleanup(func() { replaceCutoverPrepareDirs = previousDirs })
	e.observe = func(context.Context, managedJournal, string) error { return nil }
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
define) exit 0;;
autostart) exit 0;;
dominfo) echo 'Autostart: disable';;
*) exit 0;;
esac`)
	fakeCommand(t, "qemu-img", `case "$*" in
*info*) printf '{"format":"qcow2","virtual-size":1073741824}\n';;
*) exit 0;;
esac`)
	if err := e.ApplyExclusiveReplaceCutover(context.Background(), "replace-cutover-1", spec, localDir, artifacts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("original must remain retained: %v", err)
	}
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	cur, err := managedCurrent(js, vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Result.Status != "succeeded" || cur.Command.Kind != "vm-update" || cur.Command.VM.Revision != 1 {
		t.Fatalf("cutover journal: %#v", cur)
	}
	if len(cur.Disks) != 1 || cur.Disks[0] == original {
		t.Fatalf("cutover must switch to a new disk path: %#v", cur.Disks)
	}
	if _, err := os.Stat(cur.Disks[0]); err != nil {
		t.Fatalf("restored disk missing: %v", err)
	}
	// Simulate a crash during the reserved copy stage, including a truncated output.
	retry := cur
	retry.Stage = "allocating"
	retry.Result.Status = "pending"
	retry.Domain = nil
	if err := e.save(retry); err != nil {
		t.Fatal(err)
	}
	if result, err := e.ReconcileExclusiveReplaceCutover(context.Background(), "replace-cutover-1"); err != nil || result.Status != "needs_attention" {
		t.Fatalf("allocating cutover must not be replayed by reconciliation: result=%#v err=%v", result, err)
	}
	if err := os.WriteFile(cur.Disks[0], []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.ApplyExclusiveReplaceCutover(context.Background(), "replace-cutover-1", spec, localDir, artifacts); err != nil {
		t.Fatalf("reserved placement retry: %v", err)
	}
	got, size, err := fileSHA256(cur.Disks[0])
	if err != nil || got != artifacts[0].SHA256 || size != artifacts[0].Size {
		t.Fatalf("retried disk digest=%q size=%d err=%v", got, size, err)
	}
	if result, err := e.ReconcileExclusiveReplaceCutover(context.Background(), "replace-cutover-1"); err != nil || result.Status != "succeeded" {
		t.Fatalf("defined cutover reconciliation: result=%#v err=%v", result, err)
	}
	foundRetained := false
	for _, path := range cur.Retained {
		if path == original {
			foundRetained = true
		}
	}
	if !foundRetained {
		t.Fatalf("original must be listed as retained: %#v", cur.Retained)
	}
	if len(cur.Command.VM.NICs) != 0 {
		t.Fatalf("NICs must be disconnected: %#v", cur.Command.VM.NICs)
	}
}

func TestConvertStoppedDiskStandaloneRefusesBackingLink(t *testing.T) {
	fakeCommand(t, "qemu-img", `case "$*" in
*convert*) touch "$5";;
*--backing-chain*) printf '%s\n' '[{"filename":"'"$5"'","format":"qcow2","backing-filename":"base.qcow2","full-backing-filename":"/tmp/base.qcow2"},{"filename":"/tmp/base.qcow2","format":"qcow2"}]';;
*check*) exit 0;;
*) exit 1;;
esac`)
	source := filepath.Join(t.TempDir(), "source.qcow2")
	dest := filepath.Join(t.TempDir(), "dest.qcow2")
	if err := os.WriteFile(source, []byte("src"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ConvertStoppedDiskStandalone(context.Background(), source, dest); err == nil {
		t.Fatal("backing-linked convert must be refused")
	}
}

func TestRequireCompletedBackupSuccessRefusesFailureAndMissingStats(t *testing.T) {
	currentNone := "Job type: 0\n"
	okCompleted := "Job type: 3\nsuccess: 1\ndisk_processed: 1024\ndisk_remaining: 0\n"
	if err := RequireCompletedBackupSuccess(currentNone, okCompleted); err != nil {
		t.Fatal(err)
	}
	failed := "Job type: 4\nsuccess: 0\ndisk_processed: 100\ndisk_remaining: 900\n"
	if err := RequireCompletedBackupSuccess(currentNone, failed); !errors.Is(err, errBackupSuccess0) {
		t.Fatalf("success=0: %v", err)
	}
	if err := RequireCompletedBackupSuccess(currentNone, "Job type: 0\n"); !errors.Is(err, errBackupNoStats) {
		t.Fatalf("missing completed stats: %v", err)
	}
	if err := RequireCompletedBackupSuccess("Job type: 2\n", okCompleted); err == nil {
		t.Fatal("active job must be refused")
	}
}

func TestPublishBackupRecoveryPointRequiresValidatedSuccess(t *testing.T) {
	root := t.TempDir()
	if err := ClaimBackupRepositoryOwner(root, "repo-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join(t.TempDir(), "vda.qcow2")
	if err := os.WriteFile(diskPath, []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	sum, size, err := fileSHA256(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	base := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-ok", RepositoryID: "repo-1",
		PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureOnline,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: size, SHA256: sum}},
		Complete: true, Success: 1,
	}
	if _, err := PublishBackupRecoveryPoint(root, base, map[string]string{"vda.qcow2": diskPath}); err != nil {
		t.Fatal(err)
	}
	missingStats := base
	missingStats.RecoveryPointID = "point-fail"
	missingStats.Success = 0
	if _, err := PublishBackupRecoveryPoint(root, missingStats, map[string]string{"vda.qcow2": diskPath}); err == nil {
		t.Fatal("success=0 must not publish")
	}
	if _, err := os.Stat(filepath.Join(root, "point-fail")); !os.IsNotExist(err) {
		t.Fatal("failed online backup must leave no published point")
	}
}

func TestBackupDiskSourcesReadsManagedJournalHeads(t *testing.T) {
	dir := t.TempDir()
	engine := NewManaged(dir)
	vm := managedTestVM()
	vm.Boot.Firmware = "bios"
	vm.MediaID = ""
	disk := filepath.Join(dir, "disk-0.qcow2")
	if err := os.WriteFile(disk, []byte("qcow"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.save(managedJournal{
		Command: protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &vm},
		Result:  protocol.Result{OperationID: "create-1", Status: "succeeded"},
		Stage:   "defined",
		Disks:   []string{disk},
		Targets: []string{"vda"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := engine.BackupDiskSources(vm.ID)
	if err != nil || len(got) != 1 || got[0].Target != "vda" || got[0].Source != disk {
		t.Fatalf("disk sources: %#v err=%v", got, err)
	}
}

func TestRefuseOnlineBackupAdmissionUEFIAndQuiesced(t *testing.T) {
	vm := protocol.VMCreateSpec{Boot: protocol.VMBoot{Firmware: "uefi"}}
	if reason := RefuseOnlineBackupAdmission(protocol.BackupCaptureOnline, vm, "running"); reason == "" {
		t.Fatal("UEFI online must be refused")
	}
	vm.Boot.Firmware = "bios"
	if reason := RefuseOnlineBackupAdmission(protocol.BackupCaptureQuiesced, vm, "running"); reason == "" {
		t.Fatal("quiesced must be refused until proven")
	}
	if reason := RefuseOnlineBackupAdmission(protocol.BackupCaptureOnline, vm, "running"); reason != "" {
		t.Fatal(reason)
	}
}

func TestRegisterRestoredAsNewRefusesInvalidInputs(t *testing.T) {
	e := NewManaged(t.TempDir())
	spec := managedTestVM()
	spec.MediaID = ""
	spec.NICs = nil
	spec.Boot.Firmware = "bios"
	spec.Boot.MediaBootOrder = 0
	artifacts := []protocol.BackupDiskArtifact{{
		Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: 1, SHA256: strings.Repeat("a", 64),
	}}
	if err := e.RegisterRestoredAsNew(context.Background(), "bad/id", spec, t.TempDir(), strings.Repeat("a", 64), artifacts); err == nil {
		t.Fatal("invalid operation id must be refused")
	}
	if err := e.RegisterRestoredAsNew(context.Background(), "restore-valid-1", spec, "", strings.Repeat("a", 64), artifacts); err == nil {
		t.Fatal("empty local dir must be refused")
	}
	if err := e.RegisterRestoredAsNew(context.Background(), "restore-valid-1", spec, t.TempDir(), strings.Repeat("a", 64), nil); err == nil {
		t.Fatal("artifact count mismatch must be refused")
	}
}

func TestReconcileRestoredAsNewObservesRetainedIntentOnly(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Boot.Firmware = "bios"
	operationID := "restore-observe-1"
	manifestSHA := strings.Repeat("a", 64)
	artifacts := []protocol.BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: 1, SHA256: strings.Repeat("b", 64)}}
	j := managedJournal{
		Command: protocol.Command{OperationID: operationID, Kind: "vm-create", VM: &vm},
		Stage:   "allocating", Result: protocol.Result{OperationID: operationID, Status: "pending"},
		BackupRestore: true, BackupManifestSHA256: manifestSHA, BackupDisks: artifacts,
	}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	if allocating, err := e.RestoreAsNewAllocating(operationID, vm, manifestSHA, artifacts, nil); err != nil || !allocating {
		t.Fatalf("exact allocating restore should be resumable: allocating=%v err=%v", allocating, err)
	}
	wrong := vm
	wrong.Name = "other"
	if _, err := e.RestoreAsNewAllocating(operationID, wrong, manifestSHA, artifacts, nil); err == nil {
		t.Fatal("different restore allocation intent was accepted")
	}
	if _, err := e.RestoreAsNewAllocating("other-restore-1", vm, manifestSHA, artifacts, nil); !errors.Is(err, ErrRestoreAllocationJournalUnavailable) {
		t.Fatalf("missing native journal must be distinguishable: %v", err)
	}
	called := false
	e.observe = func(context.Context, managedJournal, string) error { called = true; return nil }
	result, err := e.ReconcileRestoredAsNew(context.Background(), operationID, vm)
	if err != nil || result.Status != "needs_attention" || called {
		t.Fatalf("allocating restore must not be observed: result=%#v err=%v called=%v", result, err, called)
	}
	j.Stage = "defining"
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	if allocating, err := e.RestoreAsNewAllocating(operationID, vm, manifestSHA, artifacts, nil); err != nil || allocating {
		t.Fatalf("defining restore must use observation only: allocating=%v err=%v", allocating, err)
	}
	if _, err := e.ReconcileRestoredAsNew(context.Background(), operationID, wrong); err == nil || called {
		t.Fatal("different restore intent must be refused before observation")
	}
	result, err = e.ReconcileRestoredAsNew(context.Background(), operationID, vm)
	if err != nil || result.Status != "succeeded" || !called {
		t.Fatalf("defined restore observation: result=%#v err=%v called=%v", result, err, called)
	}
}

func TestRestoreAsNewTargetAbsentRequiresNoNativeEffects(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	storageRoot := t.TempDir()
	vm := managedTestVM()
	vm.MediaID, vm.NICs = "", nil
	vm.Boot.MediaBootOrder, vm.Boot.TPM = 0, "2.0"
	vm.Disks = []protocol.VMDisk{{StorageID: "s", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Generation: 1, Path: storageRoot, Kind: protocol.StorageKindLocal}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &storage}, AppliedStorage: &storage, Result: protocol.Result{OperationID: "storage-1", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	oldNVRAM, oldTPM := firmwareVariablesDir, backupTPMStateRoot
	firmwareVariablesDir, backupTPMStateRoot = t.TempDir(), t.TempDir()
	t.Cleanup(func() { firmwareVariablesDir, backupTPMStateRoot = oldNVRAM, oldTPM })
	fakeCommand(t, "virsh", `case "$3" in list) exit 0;; *) exit 1;; esac`)
	check := func(want bool) {
		t.Helper()
		got, err := e.RestoreAsNewTargetAbsent(context.Background(), "restore-absent-1", vm)
		if err != nil || got != want {
			t.Fatalf("absence=%v want=%v err=%v", got, want, err)
		}
	}
	check(true)
	vmDir := filepath.Join(storageRoot, "nex-vms", vm.ID)
	if err := os.MkdirAll(vmDir, 0700); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := os.Remove(vmDir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{firmwareVariablesPath(vm.ID), filepath.Join(backupTPMStateRoot, vm.ID)} {
		if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
			t.Fatal(err)
		}
		check(false)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	fakeCommand(t, "virsh", `case "$3" in list) echo '`+vm.ID+`';; *) exit 1;; esac`)
	check(false)
	fakeCommand(t, "virsh", `case "$3" in list) exit 0;; *) exit 1;; esac`)
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "restore-absent-1", Kind: "vm-create", VM: &vm}, Stage: "allocating", Result: protocol.Result{OperationID: "restore-absent-1", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestCleanupTestRestoredVMKeepsCreatorBindingThroughDelete(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	vm := managedTestVM()
	vm.MediaID, vm.NICs = "", nil
	vm.Boot.Firmware, vm.Boot.MediaBootOrder = "bios", 0
	vm.Disks = []protocol.VMDisk{{StorageID: "s", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}
	disk := filepath.Join(root, "nex-vms", vm.ID, "disk-0.qcow2")
	if err := os.MkdirAll(filepath.Dir(disk), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("test-restore-disk"), 0600); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Path: root, Kind: protocol.StorageKindLocal}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &storage}, Result: protocol.Result{OperationID: "storage-1", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	creator := managedJournal{
		Command: protocol.Command{OperationID: "test-restore-1", Kind: "vm-create", VM: &vm},
		Result:  protocol.Result{OperationID: "test-restore-1", Status: "succeeded"}, Stage: "defined",
		BackupRestore: true, Disks: []string{disk}, DiskChains: [][]string{{disk}}, Targets: []string{"vda"}, MediaTarget: "sda",
	}
	if err := e.save(creator); err != nil {
		t.Fatal(err)
	}
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, _ protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		return storageAccess{Path: root, Total: 1 << 40, Available: 1 << 40}, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	fakeCommand(t, "virsh", `case "$3" in domstate) echo 'shut off';; list) ;; undefine) exit 0;; *) exit 1;; esac`)
	observed := 0
	e.observe = func(_ context.Context, j managedJournal, state string) error {
		observed++
		if j.Command.OperationID != "test-restore-1" || state != "shut off" {
			return errors.New("wrong restore definition observed")
		}
		return nil
	}
	e.observeChains = func(context.Context, []managedJournal, managedJournal) ([][]string, error) {
		return [][]string{{disk}}, nil
	}
	wrong := vm
	wrong.Name = "different"
	if result, err := e.CleanupTestRestoredVM(context.Background(), "test-restore-1", wrong, "test-clean-1"); err != nil || result.Status != "failed" || observed != 0 {
		t.Fatalf("wrong owner must be refused without native observation: %+v %v", result, err)
	}
	creator.BackupRestore = false
	if err := e.save(creator); err != nil {
		t.Fatal(err)
	}
	if result, err := e.CleanupTestRestoredVM(context.Background(), "test-restore-1", vm, "test-clean-1"); err != nil || result.Status != "failed" || observed != 0 {
		t.Fatalf("legacy creator must be refused: %+v %v", result, err)
	}
	creator.BackupRestore = true
	if err := e.save(creator); err != nil {
		t.Fatal(err)
	}
	result, err := e.CleanupTestRestoredVM(context.Background(), "test-restore-1", vm, "test-clean-1")
	if err != nil || result.Status != "succeeded" || observed != 1 {
		t.Fatalf("exact native cleanup failed: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Dir(disk)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test restore disk directory remains after cleanup: %v", err)
	}
	identity := protocol.VMCreateSpec{ID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID}
	result, err = e.CleanupTestRestoredVM(context.Background(), "test-restore-1", identity, "test-clean-1")
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("exact retry must prove absence: %+v %v", result, err)
	}
	wrongIdentity := identity
	wrongIdentity.HostID = "other-host"
	result, err = e.CleanupTestRestoredVM(context.Background(), "test-restore-1", wrongIdentity, "test-clean-1")
	if err != nil || result.Status != "failed" {
		t.Fatalf("different owner identity must block replay: %+v %v", result, err)
	}
	wrongIdentity = identity
	wrongIdentity.ID = "9b7a50d2-7df2-4990-afc3-58ce78371503"
	result, err = e.CleanupTestRestoredVM(context.Background(), "test-restore-1", wrongIdentity, "test-clean-1")
	if err != nil || result.Status != "failed" {
		t.Fatalf("different target identity must block replay: %+v %v", result, err)
	}
	fakeCommand(t, "virsh", `case "$3" in list) echo '`+vm.ID+`';; *) exit 1;; esac`)
	result, err = e.CleanupTestRestoredVM(context.Background(), "test-restore-1", identity, "test-clean-1")
	if err != nil || result.Status != "failed" {
		t.Fatalf("reappeared domain must block replay: %+v %v", result, err)
	}
}

func TestVerifyTestRestoredVMRequiresCurrentCreatorAndFirmware(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.MediaID, vm.NICs = "", nil
	vm.Boot.Firmware, vm.Boot.TPM, vm.Boot.MediaBootOrder = "uefi", "2.0", 0
	vm.Disks = []protocol.VMDisk{{StorageID: "s", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}
	creator := managedJournal{Command: protocol.Command{OperationID: "test-origin-1", Kind: "vm-create", VM: &vm}, Result: protocol.Result{OperationID: "test-origin-1", Status: "succeeded"}, Stage: "defined", BackupRestore: true}
	if err := e.save(creator); err != nil {
		t.Fatal(err)
	}
	oldNVRAM, oldTPM := firmwareVariablesDir, backupTPMStateRoot
	firmwareVariablesDir, backupTPMStateRoot = t.TempDir(), t.TempDir()
	t.Cleanup(func() { firmwareVariablesDir, backupTPMStateRoot = oldNVRAM, oldTPM })
	fakeCommand(t, "virsh", `case "$3" in domstate) echo 'shut off';; dumpxml) echo '<domain><devices><tpm model="tpm-crb"><backend type="emulator" version="2.0"/></tpm></devices></domain>';; *) exit 1;; esac`)
	observed := 0
	runningObserved := false
	e.observe = func(_ context.Context, _ managedJournal, state string) error {
		observed++
		if state == "running" {
			runningObserved = true
		}
		return nil
	}
	checkFails := func() {
		t.Helper()
		if err := e.VerifyTestRestoredVM(context.Background(), "test-origin-1", vm, true); err == nil {
			t.Fatal("unproven test restore was verified")
		}
	}
	checkFails()
	nvram := restoreNVRAMPath(vm, "test-origin-1", false)
	if err := os.WriteFile(nvram, []byte("current variables"), 0600); err != nil {
		t.Fatal(err)
	}
	checkFails()
	if err := os.MkdirAll(filepath.Join(backupTPMStateRoot, vm.ID, "tpm2"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupTPMStateRoot, vm.ID, "tpm2", "tpm2-00.permall"), []byte("changed after boot"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.VerifyTestRestoredVM(context.Background(), "test-origin-1", vm, true); err != nil {
		t.Fatalf("current stopped test restore should verify: %v", err)
	}
	fakeCommand(t, "virsh", `case "$3" in domstate) echo 'running';; dumpxml) echo '<domain><devices><tpm model="tpm-crb"><backend type="emulator" version="2.0"/></tpm></devices></domain>';; *) exit 1;; esac`)
	if err := e.VerifyTestRestoredVM(context.Background(), "test-origin-1", vm, true); err != nil || !runningObserved {
		t.Fatalf("current running test restore should verify: %v observed=%v", err, runningObserved)
	}
	wrong := vm
	wrong.HostID = "other-host"
	if err := e.VerifyTestRestoredVM(context.Background(), "test-origin-1", wrong, true); err == nil {
		t.Fatal("different owner verified")
	}
	creator.BackupRestore = false
	if err := e.save(creator); err != nil {
		t.Fatal(err)
	}
	checkFails()
	creator.BackupRestore = true
	if err := e.save(creator); err != nil {
		t.Fatal(err)
	}
	other := creator
	other.Command.OperationID = "foreign-create-1"
	if err := e.save(other); err != nil {
		t.Fatal(err)
	}
	checkFails()
	if observed != 4 {
		t.Fatalf("expected only exact creator observations, got %d", observed)
	}
}

func TestVerifyFailedTestRestoreKeepsOriginWhenDiskIsDamaged(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.MediaID, vm.NICs = "", nil
	vm.Boot.Firmware, vm.Boot.MediaBootOrder = "bios", 0
	vm.Disks = []protocol.VMDisk{{StorageID: "s", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}
	domain, err := testDomainXML(vm, nil, []string{"/missing/test-disk.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	definition := filepath.Join(t.TempDir(), "definition.xml")
	if err := os.WriteFile(definition, domain, 0600); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "virsh", `case "$3" in domstate) echo 'shut off';; dumpxml) cat '`+definition+`';; *) exit 1;; esac`)
	creator := managedJournal{Command: protocol.Command{OperationID: "test-damaged-1", Kind: "vm-create", VM: &vm}, Result: protocol.Result{OperationID: "test-damaged-1", Status: "succeeded"}, Stage: "defined", BackupRestore: true, Domain: domain}
	if err := e.save(creator); err != nil {
		t.Fatal(err)
	}
	e.observe = func(context.Context, managedJournal, string) error { return errors.New("disk is missing") }
	if err := e.VerifyTestRestoredVM(context.Background(), "test-damaged-1", vm, false); err != nil {
		t.Fatalf("failed verification should retain exact origin despite damage: %v", err)
	}
	if err := e.VerifyTestRestoredVM(context.Background(), "test-damaged-1", vm, true); err == nil {
		t.Fatal("passed verification accepted missing disk")
	}
	other := creator
	other.Command.OperationID = "foreign-create-1"
	if err := e.save(other); err != nil {
		t.Fatal(err)
	}
	if err := e.VerifyTestRestoredVM(context.Background(), "test-damaged-1", vm, false); err == nil {
		t.Fatal("failed verification accepted a reused UUID")
	}
}

func TestPrepareBackupSMBChecksMarkerBeforeClaimAndDropsNewUnitOnFailure(t *testing.T) {
	oldRun, oldMount, oldUnit, oldAccess, oldProbe := nativeRun, storageMountRoot, storageUnitRoot, smbPathAccess, probeBackupPublication
	t.Cleanup(func() {
		nativeRun, storageMountRoot, storageUnitRoot, smbPathAccess, probeBackupPublication = oldRun, oldMount, oldUnit, oldAccess, oldProbe
	})
	root := t.TempDir()
	storageMountRoot, storageUnitRoot = filepath.Join(root, "mounts"), filepath.Join(root, "units")
	credentials := filepath.Join(root, "credentials")
	if err := os.MkdirAll(credentials, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentials, "receipt-1"), []byte("username=backup\npassword=example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), storageCredentialRootKey{}, credentials)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"}, CredentialRef: "receipt-1"}
	path := remoteMountPath(backupStorageRegistration(repo))
	_, unitPath := remoteUnitPath(backupStorageRegistration(repo))
	// QEMU runs as this test's account, so everything after the publication probe succeeds.
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	var unit string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "id" && args[0] == "-u":
			return []byte(uid + "\n"), nil
		case name == "id":
			return []byte(gid + "\n"), nil
		case name == "findmnt":
			return []byte("//smb.example.test/backups cifs rw,vers=3.1.1,seal,cache=strict,uid=" + uid + ",gid=" + gid + ",file_mode=0600,dir_mode=0700 " + path + "\n"), nil
		case name == "systemctl" && args[0] == "enable":
			raw, _ := os.ReadFile(unitPath)
			unit = string(raw)
		}
		return nil, nil
	}
	smbPathAccess = func(path, _ string) (storageAccess, error) { return storageAccess{Path: path}, nil }
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	marker, err := smbMarker(path)
	if err != nil {
		t.Fatal(err)
	}

	repo.SMBIdentity = &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)}
	if _, err := PrepareBackupRepository(ctx, repo); err == nil {
		t.Fatal("a share with another marker was accepted")
	}
	if _, err := os.Lstat(filepath.Join(path, backupOwnerMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner was claimed on a share with another marker: %v", err)
	}
	if !strings.Contains(unit, "nosuid,nodev,noexec") {
		t.Fatalf("backup mount is not restricted to data: %s", unit)
	}

	repo.SMBIdentity.MarkerID = marker
	probeBackupPublication = func(string) error { return errors.New("rename is unsupported") }
	if _, err := PrepareBackupRepository(ctx, repo); err == nil {
		t.Fatal("repository that failed its publication probe was accepted")
	}
	if _, err := os.Lstat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed first registration left its mount unit: %v", err)
	}
}

// A stalled unprotect that read an older marker must not remove a newer one.
func TestStaleBackupUnprotectKeepsNewerMarker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, backupProtectMarkerName)
	if err := WriteBackupProtectMarker(dir, true); err != nil {
		t.Fatal(err)
	}
	stale, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBackupProtectMarker(dir, true); err != nil {
		t.Fatal(err)
	}
	newer, err := os.ReadFile(marker)
	if err != nil || bytes.Equal(stale, newer) {
		t.Fatalf("protect did not write a unique marker: %v", err)
	}
	if err := removeBackupProtectMarkerIfUnchanged(marker, stale); err == nil {
		t.Fatal("stale unprotect was not refused")
	}
	if got, err := os.ReadFile(marker); err != nil || !bytes.Equal(got, newer) || !ReadBackupProtectMarker(dir) {
		t.Fatalf("newer marker was lost: %q %v", got, err)
	}
	if err := WriteBackupProtectMarker(dir, false); err != nil || ReadBackupProtectMarker(dir) {
		t.Fatal("unprotect did not remove the marker:", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("unprotect left files: %v", entries)
	}
}

// Regression for BUG-0751: a marker that cannot be judged is put back, never deleted.
func TestBackupUnprotectKeepsUnreadableMarker(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, backupProtectMarkerName)
	if err := os.Mkdir(marker, 0700); err != nil { // reading a directory fails
		t.Fatal(err)
	}
	if err := removeBackupProtectMarkerIfUnchanged(marker, []byte("old")); err == nil {
		t.Fatal("unreadable marker was treated as unchanged")
	}
	if info, err := os.Lstat(marker); err != nil || !info.IsDir() {
		t.Fatalf("marker was lost: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

// A crash between rename-aside and restore leaves the only copy of a marker as .rm-*.
// Scan puts it back; unprotect removes a leftover only when it matches the current marker.
func TestBackupProtectMarkerSweepSettlesLeftovers(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, backupProtectMarkerName)
	if err := WriteBackupProtectMarker(dir, true); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(marker)
	if err := os.Rename(marker, marker+".rm-a"); err != nil {
		t.Fatal(err)
	}
	if ReadBackupProtectMarker(dir) {
		t.Fatal("test setup: point should look unprotected")
	}
	if err := sweepBackupProtectMarkers(dir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(marker); err != nil || !bytes.Equal(got, want) || !ReadBackupProtectMarker(dir) {
		t.Fatalf("marker not restored: %v", err)
	}
	// Identical leftover beside the marker is removed; a different one is kept.
	if err := os.WriteFile(marker+".rm-b", want, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker+".rm-c", []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteBackupProtectMarker(dir, false); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(marker + "*")
	if len(left) != 1 || left[0] != marker+".rm-c" {
		t.Fatalf("leftovers after unprotect: %v", left)
	}
}
