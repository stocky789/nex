// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// Retiring an ordinary storage receipt runs on the Run loop and must not wait for the
// pool backup authority.
func TestOrdinaryCredentialRetireDoesNotWaitForBackupAuthority(t *testing.T) {
	s := seedBackupMaster(t)
	poolChangeCredential(t, s, "credential-1", protocol.BackupRepoKindS3, "repo-1", "example-secret")
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	type outcome struct {
		handled bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		handled, err := RetireUnusedBackupCredential(s, "storage-receipt-1", s.State.PoolID, s.State.HostID)
		done <- outcome{handled, err}
	}()
	select {
	case got := <-done:
		if got.handled || got.err != nil {
			t.Fatalf("ordinary receipt = %t, %v", got.handled, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary receipt retirement waited for the pool backup authority")
	}
}

// A missing backup sealing key must not block ordinary storage credential retirement.
func TestOrdinaryCredentialRetireWithoutBackupSealingKey(t *testing.T) {
	s := seedBackupMaster(t)
	poolChangeCredential(t, s, "credential-1", protocol.BackupRepoKindS3, "repo-1", "example-secret")
	if err := os.RemoveAll(filepath.Join(s.dir, "backup-credential-key")); err != nil {
		t.Fatal(err)
	}
	handled, err := RetireUnusedBackupCredential(s, "storage-receipt-1", s.State.PoolID, s.State.HostID)
	if handled || err != nil {
		t.Fatalf("missing sealing key blocked ordinary retirement: %t, %v", handled, err)
	}
}

// An ejected host unmounts and forgets the repositories of the pool it left.
// Regression for BUG-0741: the unmount runs outside the pool backup lock, and a
// retry after a failed unmount keeps the new pool identity the first attempt took.
func TestEjectedHostDetachesFormerPoolBackupRepositories(t *testing.T) {
	lab := newTransferLab(t, 2)
	usePoolNetworkSysfs(t)
	member := lab.host("member-1")
	if err := os.MkdirAll(filepath.Join(member.dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	repo := protocol.BackupRepository{ID: "repo-smb", PoolID: member.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindSMB3, CredentialRef: "credential-smb"}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	var detached []string
	attempts, heldLock := 0, false
	previous := detachBackupRepositoryNative
	detachBackupRepositoryNative = func(_ context.Context, repo protocol.BackupRepository) error {
		if !poolBackupAuthorityMu.TryLock() {
			heldLock = true
		} else {
			poolBackupAuthorityMu.Unlock()
		}
		if attempts++; attempts == 1 {
			return errors.New("share busy")
		}
		detached = append(detached, repo.ID)
		return nil
	}
	t.Cleanup(func() { detachBackupRepositoryNative = previous })
	ejected := ejectMember(t, lab, "op-eject-repositories")
	ResumePoolLeave(context.Background(), ejected)
	firstPoolID := ejected.State.PoolID
	if record := peerState(t, ejected).Transfer; record == nil || record.Stage == "done" || firstPoolID == "" {
		t.Fatalf("a failed unmount finished the eject: %#v", record)
	}
	ResumePoolLeave(context.Background(), ejected)
	if record := peerState(t, ejected).Transfer; record == nil || record.Stage != "done" {
		t.Fatalf("the ejected host did not re-initialize: %#v", record)
	}
	if ejected.State.PoolID != firstPoolID || heldLock {
		t.Fatalf("retry pool ID %s after %s; unmount under the backup lock %v", ejected.State.PoolID, firstPoolID, heldLock)
	}
	state, err := loadPoolBackupRepositories(ejected)
	if err != nil || len(state.Repositories) != 0 || len(detached) != 1 || detached[0] != repo.ID {
		t.Fatalf("former pool repositories = %+v, %v; detached %v", state.Repositories, err, detached)
	}
}

// Only a credential this host cannot use waives the typed confirmation for replacing it; a
// failed read of the credential store does not.
func TestReplacementConfirmationIsWaivedOnlyForUnusableCredential(t *testing.T) {
	s := seedBackupMaster(t)
	receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	existing := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Cloud", Kind: protocol.BackupRepoKindS3,
		CredentialRef: receipt.Receipt, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups"}}
	requested := existing
	requested.CredentialRef = "credential-2"
	if err := backupReplacementError(s, existing, requested, false); !errors.Is(err, ErrBackupCredentialInUse) {
		t.Fatalf("a working credential was replaced without confirmation: %v", err)
	}
	dir, err := backupCredentialDir(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0300); err != nil {
		t.Fatal(err)
	}
	err = backupReplacementError(s, existing, requested, false)
	_ = os.Chmod(dir, 0700)
	if !errors.Is(err, ErrBackupCredentialInUse) {
		t.Fatalf("a failed read of the credential store waived the confirmation: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(s.dir, "backup-credential-key")); err != nil {
		t.Fatal(err)
	}
	if err := backupReplacementError(s, existing, requested, false); err != nil {
		t.Fatalf("a lost sealing key kept the credential from being replaced: %v", err)
	}
}
