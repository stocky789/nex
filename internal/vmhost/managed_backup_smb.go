// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

// backupSMBCredentialRoot holds one root-only mount.cifs credentials file per credential reference. The
// mount unit reads it itself, so the repository remounts after a reboot without Core. The root
// is fixed, like the mount and unit roots the same unit names: it does not follow --state-dir.
var backupSMBCredentialRoot = "/var/lib/nex-core-agent/backup-smb-credentials"

// backupSMBPrepare serializes the credential-file and mount-unit sequence of one repository. The
// wait ends with the caller's context, and a stuck share holds up only its own repository.
var backupSMBPrepare keyedLock

// RemoveBackupSMBCredential deletes the mount credentials file of a credential reference.
func RemoveBackupSMBCredential(reference string) error {
	if !protocol.ValidSnapshotOperationID(reference) {
		return nil
	}
	if err := os.Remove(filepath.Join(backupSMBCredentialRoot, reference)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PrepareBackupRepositorySMB writes the Core-resolved credential to a root-only file before the
// CIFS mount unit that names it is made.
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
	unlock, err := backupSMBPrepare.lock(ctx, registration.ID)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	defer unlock()
	if err := checkExistingBackupRepositoryOwner(filepath.Join(remoteMountPath(registration), repo.SMBSource.Directory), repo.ID, repo.PoolID); err != nil {
		return protocol.BackupRepository{}, err
	}
	root, path := backupSMBCredentialRoot, filepath.Join(backupSMBCredentialRoot, repo.CredentialRef)
	unit, unitPath := remoteUnitPath(registration)
	oldUnit, err := os.ReadFile(unitPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return protocol.BackupRepository{}, err
	}
	replacing := err == nil && !unitHasSMBCredential(oldUnit, path)
	if err == nil && !strings.HasPrefix(string(oldUnit), nexMountMarker) {
		return protocol.BackupRepository{}, errors.New("refusing to replace an existing mount unit")
	}
	if replacing {
		_, rest, ok := strings.Cut(string(oldUnit), ",credentials=")
		oldPath, _, _ := strings.Cut(rest, ",")
		if !ok || filepath.Dir(oldPath) != root || !protocol.ValidSnapshotOperationID(filepath.Base(oldPath)) {
			return protocol.BackupRepository{}, errors.New("previous backup repository SMB credential scope is invalid")
		}
		previous := registration
		previous.CredentialReceipt = filepath.Base(oldPath)
		if _, err := credentialFile(context.WithValue(ctx, storageCredentialRootKey{}, root), previous); err != nil {
			return protocol.BackupRepository{}, err
		}
		// Registration admission has blocked repository dependencies. Keep the old
		// unit and credential available to the native remount rollback.
		ctx = allowStorageRemount(ctx)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return protocol.BackupRepository{}, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return protocol.BackupRepository{}, errors.New("backup repository SMB credential directory is not private")
	}
	// A credential reference is immutable, including across registration retries.
	_, credentialErr := os.Lstat(path)
	created := errors.Is(credentialErr, os.ErrNotExist)
	if credentialErr != nil && !created {
		return protocol.BackupRepository{}, credentialErr
	}
	if created {
		if err := atomicWrite(path, []byte(body)); err != nil {
			return protocol.BackupRepository{}, err
		}
	}
	ctx = context.WithValue(ctx, storageCredentialRootKey{}, root)
	prepared, prepareErr := PrepareBackupRepository(ctx, repo)
	if prepareErr != nil {
		if replacing {
			// Native preparation rolls back mount/probe failures. Cover failures in
			// the final repository checks as well, before handing back to registration.
			current, readErr := os.ReadFile(unitPath)
			if readErr != nil || !bytes.Equal(current, oldUnit) {
				cleanupCtx, cancel := context.WithTimeout(storageCleanupContext(ctx), 20*time.Second)
				defer cancel()
				rollbackErr := restoreManagedMountUnit(cleanupCtx, unitPath, oldUnit, unit, true)
				if rollbackErr == nil {
					_, rollbackErr = inspectSMB(cleanupCtx, registration)
				}
				if rollbackErr == nil {
					rollbackErr = qemuStorageAccess(cleanupCtx, filepath.Join(remoteMountPath(registration), repo.SMBSource.Directory))
				}
				if rollbackErr != nil {
					prepareErr = errors.Join(prepareErr, fmt.Errorf("%w: restore previous backup SMB mount: %v", ErrStorageRollbackUncertain, rollbackErr))
				}
			}
		}
		if created && !errors.Is(prepareErr, ErrStorageRollbackUncertain) {
			prepareErr = errors.Join(prepareErr, RemoveBackupSMBCredential(repo.CredentialRef))
		}
		return protocol.BackupRepository{}, prepareErr
	}
	// Registration removes the superseded credential only after its durable commit.
	return prepared, nil
}

func unitHasSMBCredential(unit []byte, credentialPath string) bool {
	return strings.Contains(string(unit), ",credentials="+credentialPath+",")
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
	if err = AtomicWrite(path, payload, 0600); err != nil {
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
