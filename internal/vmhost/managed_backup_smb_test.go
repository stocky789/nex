// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestPrepareBackupRepositorySMBReplacesMountOfAnotherCredential(t *testing.T) {
	repo, credentialRoot, _, active := backupSMBReplacementFixture(t, "")
	prepared, err := PrepareBackupRepositorySMB(context.Background(), repo, protocol.StorageCredentials{Username: "backup", Secret: "new-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if *active != repo.CredentialRef || prepared.MountPath != remoteMountPath(backupStorageRegistration(repo)) {
		t.Fatalf("replacement access = %q, repository = %+v", *active, prepared)
	}
	oldFile := filepath.Join(credentialRoot, "old-receipt")
	if got, err := os.ReadFile(oldFile); err != nil || string(got) != "username=backup\npassword=old-secret\n" {
		t.Fatalf("preparation discarded the credential needed for commit rollback: %q %v", got, err)
	}
	// The registration owner retires the old plaintext only after durable commit.
	if err := RemoveBackupSMBCredential("old-receipt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the superseded plaintext credential stayed after retirement: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(credentialRoot, repo.CredentialRef)); err != nil || string(got) != "username=backup\npassword=new-secret\n" {
		t.Fatalf("new credential file: %q %v", got, err)
	}
	if err := VerifyBackupRepositoryRead(context.Background(), prepared); err != nil {
		t.Fatalf("replacement lost access after old credential retirement: %v", err)
	}
}

func TestPrepareBackupRepositorySMBReplacementFailureRestoresPriorAccess(t *testing.T) {
	for _, stage := range []string{"mount", "publication", "qemu", "final-path"} {
		t.Run(stage, func(t *testing.T) {
			repo, credentialRoot, oldUnit, active := backupSMBReplacementFixture(t, stage)
			if stage == "final-path" {
				repo.MountPath = filepath.Join(t.TempDir(), "wrong-mount")
			}
			_, err := PrepareBackupRepositorySMB(context.Background(), repo, protocol.StorageCredentials{Username: "backup", Secret: "new-secret"})
			if err == nil || errors.Is(err, ErrStorageRollbackUncertain) {
				t.Fatalf("replacement error = %v, want definite failure with proven rollback", err)
			}
			_, unitPath := remoteUnitPath(backupStorageRegistration(repo))
			if got, err := os.ReadFile(unitPath); err != nil || string(got) != string(oldUnit) {
				t.Fatalf("prior unit not restored exactly: %q %v", got, err)
			}
			if got, err := os.ReadFile(filepath.Join(credentialRoot, "old-receipt")); err != nil || string(got) != "username=backup\npassword=old-secret\n" {
				t.Fatalf("prior credential changed: %q %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(credentialRoot, repo.CredentialRef)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed replacement left new plaintext behind: %v", err)
			}
			if *active != "old-receipt" {
				t.Fatalf("mounted credential = %q, want old-receipt", *active)
			}
			repo.CredentialRef = "old-receipt"
			repo.MountPath = remoteMountPath(backupStorageRegistration(repo))
			if err := VerifyBackupRepositoryRead(context.Background(), repo); err != nil {
				t.Fatalf("prior repository access not restored: %v", err)
			}
		})
	}
}

func TestPrepareBackupRepositorySMBReplacementRollbackFailureRetainsCredentials(t *testing.T) {
	repo, credentialRoot, oldUnit, _ := backupSMBReplacementFixture(t, "rollback")
	_, err := PrepareBackupRepositorySMB(context.Background(), repo, protocol.StorageCredentials{Username: "backup", Secret: "new-secret"})
	if !errors.Is(err, ErrStorageRollbackUncertain) {
		t.Fatalf("replacement error = %v, want visible uncertain rollback", err)
	}
	_, unitPath := remoteUnitPath(backupStorageRegistration(repo))
	if got, err := os.ReadFile(unitPath); err != nil || string(got) != string(oldUnit) {
		t.Fatalf("prior unit not retained for recovery: %q %v", got, err)
	}
	for reference, expected := range map[string]string{
		"old-receipt": "username=backup\npassword=old-secret\n",
		"new-receipt": "username=backup\npassword=new-secret\n",
	} {
		if got, err := os.ReadFile(filepath.Join(credentialRoot, reference)); err != nil || string(got) != expected {
			t.Fatalf("credential %q not retained for uncertain recovery: %q %v", reference, got, err)
		}
	}
}

func backupSMBReplacementFixture(t *testing.T, failure string) (protocol.BackupRepository, string, []byte, *string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("SMB mount ancestry validation requires a root-owned disposable fixture")
	}
	credentialRoot := tempBackupSMBCredentialRoot(t)
	previousUnits, previousMount, previousRun, previousProbe := storageUnitRoot, storageMountRoot, nativeRun, probeBackupPublication
	base, err := os.MkdirTemp("/var/lib", "nex-backup-smb-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	storageUnitRoot, storageMountRoot = filepath.Join(base, "units"), filepath.Join(base, "mounts")
	if err := os.MkdirAll(storageMountRoot, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		storageUnitRoot, storageMountRoot, nativeRun, probeBackupPublication = previousUnits, previousMount, previousRun, previousProbe
	})
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"}, CredentialRef: "new-receipt",
	}
	registration := backupStorageRegistration(repo)
	mount := remoteMountPath(registration)
	if err := ClaimBackupRepositoryOwner(mount, repo.ID, repo.PoolID); err != nil {
		t.Fatal(err)
	}
	marker, err := smbMarker(mount)
	if err != nil {
		t.Fatal(err)
	}
	repo.SMBIdentity = &protocol.SMBStorageIdentity{Server: repo.SMBSource.Server, Share: repo.SMBSource.Share, MarkerID: marker}
	_, unitPath := remoteUnitPath(registration)
	oldFile := filepath.Join(credentialRoot, "old-receipt")
	// Native QEMU commands are simulated; keep probe ownership valid in a single-UID namespace.
	qemuID := strconv.Itoa(os.Getuid())
	oldUnit := remoteUnit("//smb.example.test/backups", mount, "cifs", smbMountOptions(qemuID, qemuID, oldFile)+backupNFSHardeningOpts)
	for path, body := range map[string][]byte{unitPath: oldUnit, oldFile: []byte("username=backup\npassword=old-secret\n")} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	active := "old-receipt"
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "id":
			return []byte(qemuID + "\n"), nil
		case "systemctl":
			if len(args) > 0 && args[0] == "restart" {
				current, err := os.ReadFile(unitPath)
				if err != nil {
					return nil, err
				}
				reference := "old-receipt"
				if unitHasSMBCredential(current, filepath.Join(credentialRoot, repo.CredentialRef)) {
					reference = repo.CredentialRef
				}
				if _, err := os.Stat(filepath.Join(credentialRoot, reference)); err != nil {
					return nil, err
				}
				if reference == repo.CredentialRef && failure == "mount" {
					active = ""
					return nil, errors.New("replacement credentials rejected")
				}
				if reference == "old-receipt" && failure == "rollback" {
					active = ""
					return nil, errors.New("prior mount restart failed")
				}
				active = reference
			}
			return nil, nil
		case "findmnt":
			if active == "" {
				return nil, errors.New("mount unavailable")
			}
			return []byte("//smb.example.test/backups cifs rw,vers=3.1.1,seal,cache=strict,uid=" + qemuID + ",gid=" + qemuID + ",file_mode=0600,dir_mode=0700 " + mount + "\n"), nil
		case "runuser":
			if active == repo.CredentialRef && (failure == "qemu" || failure == "rollback") {
				return nil, errors.New("replacement QEMU access denied")
			}
			return nil, nil
		default:
			return nil, errors.New("unexpected native command: " + name + " " + strings.Join(args, " "))
		}
	}
	probeBackupPublication = func(root string) error {
		if active == repo.CredentialRef && failure == "publication" {
			return errors.New("replacement publication denied")
		}
		return previousProbe(root)
	}
	return repo, credentialRoot, oldUnit, &active
}

func TestNFSRootSquashRefusalIsExplained(t *testing.T) {
	if err := nfsRootSquash(os.ErrPermission); err == nil || !strings.Contains(err.Error(), "root_squash is not supported") {
		t.Fatalf("permission refusal is not explained: %v", err)
	}
	if err := nfsRootSquash(errors.New("connection refused")); err == nil || strings.Contains(err.Error(), "root_squash") {
		t.Fatalf("an unrelated failure was blamed on root_squash: %v", err)
	}
	if nfsRootSquash(nil) != nil {
		t.Fatal("no failure became a failure")
	}
}
