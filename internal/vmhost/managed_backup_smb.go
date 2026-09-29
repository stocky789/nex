// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"nex.local/nex/internal/protocol"
)

// PrepareBackupRepositorySMB materializes only a Core-resolved credential in
// private host state before the sealed CIFS mount reads it.
func PrepareBackupRepositorySMB(ctx context.Context, repo protocol.BackupRepository, stateDir string, credentials protocol.StorageCredentials) (protocol.BackupRepository, error) {
	if repo.Kind != protocol.BackupRepoKindSMB3 || repo.Valid() != nil || repo.CredentialRef == "" ||
		!filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir ||
		credentials.Username == "" || credentials.Secret == "" || strings.ContainsAny(credentials.Username+credentials.Secret, "\x00\r\n") {
		return protocol.BackupRepository{}, errors.New("backup repository SMB credential scope is invalid")
	}
	body, err := smbAuthFileBody(credentials.Username, credentials.Secret, "")
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	root := filepath.Join(stateDir, "backup-smb-credentials")
	registration := backupStorageRegistration(repo)
	if err := checkExistingBackupRepositoryOwner(filepath.Join(remoteMountPath(registration), repo.SMBSource.Directory), repo.ID, repo.PoolID); err != nil {
		return protocol.BackupRepository{}, err
	}
	_, unitPath := remoteUnitPath(registration)
	if unit, err := os.ReadFile(unitPath); err == nil {
		if !strings.HasPrefix(string(unit), nexMountMarker) || !unitHasSMBCredential(unit, filepath.Join(root, repo.CredentialRef)) {
			return protocol.BackupRepository{}, errors.New("backup repository SMB credential rotation requires the mount to be detached")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return protocol.BackupRepository{}, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return protocol.BackupRepository{}, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return protocol.BackupRepository{}, errors.New("backup repository SMB credential directory is not private")
	}
	path := filepath.Join(root, repo.CredentialRef)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return protocol.BackupRepository{}, errors.New("backup repository SMB credential file is not private")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(data, []byte(body)) {
			return protocol.BackupRepository{}, errors.New("backup repository SMB credential conflicts with private receipt")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := AtomicWrite(path, []byte(body), 0600); err != nil {
			return protocol.BackupRepository{}, err
		}
	} else {
		return protocol.BackupRepository{}, err
	}
	return PrepareBackupRepository(context.WithValue(ctx, storageCredentialRootKey{}, root), repo)
}

func unitHasSMBCredential(unit []byte, credentialPath string) bool {
	for _, line := range strings.Split(string(unit), "\n") {
		if !strings.HasPrefix(line, "Options=") {
			continue
		}
		for _, option := range strings.Split(strings.TrimPrefix(line, "Options="), ",") {
			if option == "credentials="+credentialPath {
				return true
			}
		}
	}
	return false
}

// RetireBackupRepositorySMBCredential removes a replaced mount credential only
// after the durable repository row has selected its successor.
func RetireBackupRepositorySMBCredential(stateDir, credentialRef string) error {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || !protocol.ValidSnapshotOperationID(credentialRef) {
		return errors.New("backup repository SMB credential scope is invalid")
	}
	root := filepath.Join(stateDir, "backup-smb-credentials")
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("backup repository SMB credential directory is not private")
	}
	path := filepath.Join(root, credentialRef)
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("backup repository SMB credential file is not private")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncManagedDir(root)
}

// probeBackupFilesystemPublication refuses SMB mounts that cannot perform the
// sync and directory rename sequence used to publish recovery points.
func probeBackupFilesystemPublication(root string) (err error) {
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
