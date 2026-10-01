// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// After the credential key is lost the repository is registered again with a new credential
// reference; the old mount and its file give way instead of blocking the registration.
func TestPrepareBackupRepositorySMBReplacesMountOfAnotherCredential(t *testing.T) {
	root, credentialRoot := t.TempDir(), tempBackupSMBCredentialRoot(t)
	previousUnits, previousPrepare, previousRun := storageUnitRoot, prepareBackupSMB, nativeRun
	storageUnitRoot = t.TempDir()
	t.Cleanup(func() { storageUnitRoot, prepareBackupSMB, nativeRun = previousUnits, previousPrepare, previousRun })
	nativeRun = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	prepareBackupSMB = func(context.Context, protocol.StorageRegistration, string) (storageAccess, error) {
		return storageAccess{Path: root, Observed: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{
			Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32),
		}}}, nil
	}
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Backups", Kind: protocol.BackupRepoKindSMB3,
		SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"}, CredentialRef: "new-receipt",
	}
	registration := backupStorageRegistration(repo)
	_, unitPath := remoteUnitPath(registration)
	oldFile := filepath.Join(credentialRoot, "old-receipt")
	for path, body := range map[string][]byte{
		unitPath: remoteUnit("//smb.example.test/backups", remoteMountPath(registration), "cifs", smbMountOptions("1000", "1000", oldFile)),
		oldFile:  []byte("username=backup\npassword=old-secret\n"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PrepareBackupRepositorySMB(context.Background(), repo, protocol.StorageCredentials{Username: "backup", Secret: "new-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old plaintext credential file stayed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(credentialRoot, "new-receipt")); err != nil || !strings.Contains(string(got), "new-secret") {
		t.Fatalf("new credential file: %q %v", got, err)
	}
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
