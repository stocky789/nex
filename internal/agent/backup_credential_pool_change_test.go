// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func poolChangeCredential(t *testing.T, s *Store, requestID, kind, repoID, secret string) string {
	t.Helper()
	receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
		Offer: protocol.StorageCredentialOffer{RequestID: requestID, RegistrationRequestID: "backup-credential:" + kind + ":register-" + requestID,
			PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: repoID, ExpiresAt: time.Now().Add(time.Minute)},
		Credentials: protocol.StorageCredentials{Username: "example-user", Secret: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt.Receipt
}

func backupCredentialNames(t *testing.T, s *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.dir, clusterSecretDirectory, "backup"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// A former pool's receipt, a damaged file or a crashed write's temp file must not take down
// the listing or resolution of every other backup credential on the host.
func TestBackupCredentialReceiptsSkipForeignAndDamagedFiles(t *testing.T) {
	s := seedBackupMaster(t)
	poolChangeCredential(t, s, "credential-old", protocol.BackupRepoKindS3, "repo-old", "old-pool-secret")
	s.State.PoolID = "pool-2"
	ref := poolChangeCredential(t, s, "credential-new", protocol.BackupRepoKindS3, "repo-new", "new-pool-secret")
	dir, err := backupCredentialDir(s)
	if err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{"corrupt.json": 0600, "unsafe.json": 0640, ".write-123": 0600} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("username=x\npassword=plaintext"), mode); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := BackupCredentialReceipts(s)
	if !errors.Is(err, errBackupCredentialDamaged) || len(listed) != 1 || listed[0].Receipt != ref {
		t.Fatalf("listing = %+v, %v", listed, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".write-123")); !os.IsNotExist(err) {
		t.Fatalf("crashed write left plaintext temp file: %v", err)
	}
	repo := protocol.BackupRepository{ID: "repo-new", PoolID: s.State.PoolID, Kind: protocol.BackupRepoKindS3, CredentialRef: ref}
	if got, err := ResolveBackupCredentialForRegistration(s, repo, "register-credential-new"); err != nil || got.Secret != "new-pool-secret" {
		t.Fatalf("damaged file blocked resolution: %v", err)
	}
	if err := eraseForeignBackupCredentials(s); err != nil {
		t.Fatal(err)
	}
	if names := backupCredentialNames(t, s); len(names) != 3 || strings.Contains(strings.Join(names, ","), ".write-") {
		t.Fatalf("former pool receipt survived: %v", names)
	}
}

// The heartbeat's receipt listing must not wait behind a long pool backup authority holder.
func TestBackupCredentialReceiptsDoNotWaitForBackupAuthority(t *testing.T) {
	s := seedBackupMaster(t)
	poolChangeCredential(t, s, "credential-1", protocol.BackupRepoKindS3, "repo-1", "example-secret")
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := BackupCredentialReceipts(s)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receipt listing waited for the pool backup authority")
	}
}

func TestBackupCredentialScopeRefusesSMBUsernameWithEquals(t *testing.T) {
	s := seedBackupMaster(t)
	_, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
		Offer: protocol.StorageCredentialOffer{RequestID: "credential-1", RegistrationRequestID: "backup-credential:smb3:register-1",
			PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute)},
		Credentials: protocol.StorageCredentials{Username: "user=admin", Secret: "example-secret"},
	})
	if err == nil {
		t.Fatal("SMB username the mount credential file cannot hold was stored")
	}
	if names := backupCredentialNames(t, s); len(names) != 0 {
		t.Fatalf("refused credential left a receipt: %v", names)
	}
}

func TestRetiredBackupCredentialReplayReportsRetirement(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "example-secret"}}
	receipt, err := saveBackupCredential(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	if handled, err := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, s.State.HostID); !handled || err != nil {
		t.Fatalf("retire: %t, %v", handled, err)
	}
	if _, err := saveBackupCredential(s, handoff); err == nil || err.Error() != "backup credential receipt was retired" {
		t.Fatalf("retired replay = %v", err)
	}
}

// Joining another pool erases the solo pool's receipts, and a pull larger than the former
// 1 MiB cap still delivers every repository's credential.
func TestJoinErasesFormerPoolCredentialsAndPullsLargeSet(t *testing.T) {
	master := seedBackupMaster(t)
	state := poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{}}
	secret := strings.Repeat("s", 60<<10)
	for i := range 20 {
		id := fmt.Sprintf("repo-%d", i)
		repo := protocol.BackupRepository{ID: id, PoolID: master.State.PoolID, Name: "Backup",
			Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}}
		repo.CredentialRef = poolChangeCredential(t, master, "credential-"+id, protocol.BackupRepoKindS3, id, secret)
		state.Repositories[id] = repo
	}
	if err := savePoolBackupRepositories(master, state); err != nil {
		t.Fatal(err)
	}
	member := peerStore(t, "member-join", "solo-join")
	poolChangeCredential(t, member, "credential-solo", protocol.BackupRepoKindS3, "repo-solo", "solo-pool-secret")
	joinPeerMember(t, master, member)
	listed, err := BackupCredentialReceipts(member)
	if err != nil || len(listed) != len(state.Repositories) {
		t.Fatalf("joined member receipts = %d, %v", len(listed), err)
	}
	for _, repo := range state.Repositories {
		if _, err := localBackupCredential(member, repo); err != nil {
			t.Fatalf("joined member lacks %s: %v", repo.ID, err)
		}
	}
	if names := backupCredentialNames(t, member); len(names) != len(state.Repositories) {
		t.Fatalf("former pool receipt survived the join: %d files", len(names))
	}
}

// An ejected host becomes its own new pool and keeps no secret of the pool it left.
func TestEjectedHostErasesBackupCredentials(t *testing.T) {
	lab := newTransferLab(t, 2)
	usePoolNetworkSysfs(t)
	member := lab.host("member-1")
	if err := os.MkdirAll(filepath.Join(member.dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	poolChangeCredential(t, member, "credential-smb", protocol.BackupRepoKindSMB3, "repo-smb", "smb-secret")
	ejected := ejectMember(t, lab, "op-eject-credentials")
	if !ResumePoolLeave(context.Background(), ejected) {
		t.Fatalf("the ejected host did not re-initialize: %#v", peerState(t, ejected).Transfer)
	}
	if names := backupCredentialNames(t, ejected); len(names) != 0 {
		t.Fatalf("ejected host kept backup receipts: %v", names)
	}
}

// A former pool's receipt is wiped from readable sealed metadata even when the sealing key is
// gone and the ciphertext is damaged.
func TestEraseForeignBackupCredentialsWithoutDecrypt(t *testing.T) {
	s := seedBackupMaster(t)
	foreignRef := poolChangeCredential(t, s, "credential-old", protocol.BackupRepoKindSMB3, "repo-old", "old-pool-secret")
	s.State.PoolID = "pool-2"
	currentRef := poolChangeCredential(t, s, "credential-new", protocol.BackupRepoKindS3, "repo-new", "new-pool-secret")
	dir, err := backupCredentialDir(s)
	if err != nil {
		t.Fatal(err)
	}
	foreignPath := ""
	for _, name := range backupCredentialNames(t, s) {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var sealed sealedBackupCredential
		if err := json.Unmarshal(raw, &sealed); err != nil {
			t.Fatal(err)
		}
		if sealed.Reference != foreignRef {
			continue
		}
		foreignPath = path
		sealed.Ciphertext[0] ^= 0xff
		damaged, err := json.Marshal(sealed)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, damaged, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if foreignPath == "" {
		t.Fatal("foreign receipt missing")
	}
	if err := os.RemoveAll(filepath.Join(s.dir, "backup-credential-key")); err != nil {
		t.Fatal(err)
	}
	var removed []string
	previous := removeBackupSMBCredentialNative
	removeBackupSMBCredentialNative = func(reference string) error {
		removed = append(removed, reference)
		return nil
	}
	t.Cleanup(func() { removeBackupSMBCredentialNative = previous })
	if err := eraseForeignBackupCredentials(s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(foreignPath); !os.IsNotExist(err) {
		t.Fatalf("damaged foreign receipt survived without a key: %v", err)
	}
	if len(removed) != 1 || removed[0] != foreignRef {
		t.Fatalf("SMB credential file not erased: %v", removed)
	}
	listed, err := BackupCredentialReceipts(s)
	if !errors.Is(err, errBackupCredentialKeyMissing) {
		t.Fatalf("current receipt listing without key: %+v %v", listed, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, backupCredentialNames(t, s)[0]))
	if err != nil {
		t.Fatal(err)
	}
	var sealed sealedBackupCredential
	if err := json.Unmarshal(raw, &sealed); err != nil || sealed.Reference != currentRef {
		t.Fatalf("current-pool receipt was wiped: %+v %v", sealed, err)
	}
}
