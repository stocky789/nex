// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
)

// backupSMBCredentialRoot holds one systemd-creds encrypted file per credential reference. The
// mount unit decrypts it itself, so the repository remounts after a reboot without Core.
var backupSMBCredentialRoot = "/var/lib/nex-core-agent/backup-smb-credentials"

var backupSMBPrepareMu sync.Mutex

// RemoveBackupSMBCredential deletes the sealed mount credential of a credential reference.
func RemoveBackupSMBCredential(reference string) error {
	if !protocol.ValidSnapshotOperationID(reference) {
		return nil
	}
	if err := os.Remove(filepath.Join(backupSMBCredentialRoot, reference)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PrepareBackupRepositorySMB seals the Core-resolved credential with systemd-creds before the
// CIFS mount unit that loads it is made; the plaintext never reaches disk.
func PrepareBackupRepositorySMB(ctx context.Context, repo protocol.BackupRepository, credentials protocol.StorageCredentials) (protocol.BackupRepository, error) {
	if repo.Kind != protocol.BackupRepoKindSMB3 || repo.Valid() != nil || repo.CredentialRef == "" || !protocol.ValidSnapshotOperationID(repo.CredentialRef) ||
		credentials.Username == "" || credentials.Secret == "" || !protocol.ValidSMBDomain(credentials.Domain) || strings.ContainsAny(credentials.Username+credentials.Secret, "\x00\r\n") {
		return protocol.BackupRepository{}, errors.New("backup repository SMB credential scope is invalid")
	}
	body, err := smbAuthFileBody(credentials.Username, credentials.Secret, credentials.Domain)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	registration := backupStorageRegistration(repo)
	if err := checkExistingBackupRepositoryOwner(filepath.Join(remoteMountPath(registration), repo.SMBSource.Directory), repo.ID, repo.PoolID); err != nil {
		return protocol.BackupRepository{}, err
	}
	root, path := backupSMBCredentialRoot, filepath.Join(backupSMBCredentialRoot, repo.CredentialRef)
	_, unitPath := remoteUnitPath(registration)
	mounted := false
	if unit, err := os.ReadFile(unitPath); err == nil {
		if !strings.HasPrefix(string(unit), nexMountMarker) || !unitHasSMBCredential(unit, path) {
			return protocol.BackupRepository{}, errors.New("backup repository SMB credential rotation requires the mount to be detached")
		}
		mounted = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return protocol.BackupRepository{}, err
	}
	backupSMBPrepareMu.Lock()
	defer backupSMBPrepareMu.Unlock()
	if err := os.MkdirAll(root, 0700); err != nil {
		return protocol.BackupRepository{}, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return protocol.BackupRepository{}, errors.New("backup repository SMB credential directory is not private")
	}
	// A reference never changes its secret, so a mount already using the file keeps it.
	if _, err := os.Lstat(path); !mounted || err != nil {
		if err := sealMountCredential(ctx, path, body); err != nil {
			return protocol.BackupRepository{}, err
		}
	}
	return PrepareBackupRepository(context.WithValue(ctx, storageCredentialRootKey{}, root), repo)
}

func unitHasSMBCredential(unit []byte, credentialPath string) bool {
	return strings.Contains(string(unit), "\nLoadCredentialEncrypted="+mountCredentialName+":"+credentialPath+"\n")
}

// probeBackupFilesystemPublication refuses SMB mounts that cannot perform the
// sync and directory rename sequence used to publish recovery points.
func probeBackupFilesystemPublication(root string) (err error) {
	// A crash between create and cleanup leaves a probe behind; clear stale ones.
	stale, _ := filepath.Glob(filepath.Join(root, ".nex-backup-probe-*"))
	for _, path := range stale {
		if info, statErr := os.Lstat(path); statErr == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.RemoveAll(path)
		}
	}
	staging, err := os.MkdirTemp(root, ".nex-backup-probe-")
	if err != nil {
		return err
	}
	published := staging + "-published"
	defer func() {
		if cleanupErr := os.RemoveAll(staging); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		if cleanupErr := os.RemoveAll(published); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	payload := []byte("nex-backup-publication-probe")
	path := filepath.Join(staging, "artifact")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(payload); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = syncManagedDir(staging); err != nil {
		return err
	}
	if err = os.Rename(staging, published); err != nil {
		return err
	}
	if err = syncManagedDir(root); err != nil {
		return err
	}
	got, err := os.ReadFile(filepath.Join(published, "artifact"))
	if err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return errors.New("backup repository publication probe readback differs")
	}
	return nil
}
