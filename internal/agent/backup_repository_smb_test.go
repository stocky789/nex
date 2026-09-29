// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestSMBBackupRegistrationRequiresOperationBoundCredentialAndExactSource(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:smb3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "backup", Secret: "private-example-secret"}}
	receipt, err := saveCredentialHandoff(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups", Directory: "pool-1"},
		CredentialRef: receipt.Receipt,
	}
	previous := prepareBackupRepositorySMBNative
	t.Cleanup(func() { prepareBackupRepositorySMBNative = previous })
	called := 0
	prepareBackupRepositorySMBNative = func(_ context.Context, got protocol.BackupRepository, stateDir string, creds protocol.StorageCredentials) (protocol.BackupRepository, error) {
		called++
		if got.ID != repo.ID || stateDir != s.dir || creds != handoff.Credentials {
			t.Fatalf("incorrect SMB prepare scope: %+v %q %+v", got, stateDir, creds)
		}
		got.MountPath = t.TempDir()
		got.SMBIdentity = &protocol.SMBStorageIdentity{Server: repo.SMBSource.Server, Share: repo.SMBSource.Share, MarkerID: strings.Repeat("a", 32)}
		return got, nil
	}
	if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, repo, "register-other"); err == nil || called != 0 {
		t.Fatalf("wrong registration operation reached mount: %v, calls=%d", err, called)
	}
	invalid := repo
	invalid.SMBSource = &protocol.SMBStorageSource{Server: "smb.example.test", Share: "../backups"}
	if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, invalid, "register-1"); err == nil || called != 0 {
		t.Fatalf("invalid SMB share reached mount: %v, calls=%d", err, called)
	}
	prepared, err := prepareBackupRepositoryForRegistration(context.Background(), s, repo, "register-1")
	if err != nil || called != 1 {
		t.Fatalf("prepare: %+v, %v, calls=%d", prepared, err, called)
	}
	changed := prepared
	changed.SMBIdentity = &protocol.SMBStorageIdentity{Server: "other.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)}
	if _, err := upsertPoolBackupRepositoryPrepared(s, repo, changed); err == nil {
		t.Fatal("changed prepared SMB identity was accepted")
	}
	saved, err := upsertPoolBackupRepositoryPrepared(s, repo, prepared)
	if err != nil || saved.Generation == 0 {
		t.Fatalf("durable SMB registration: %+v, %v", saved, err)
	}
	if creds, err := ResolveBackupCredential(s, saved); err != nil || creds != handoff.Credentials {
		t.Fatalf("strict credential lookup: %+v, %v", creds, err)
	}
	if _, err := openBackupRepositoryStorage(s, saved); err != nil {
		t.Fatalf("SMB backend unavailable after durable registration: %v", err)
	}
	stale := saved
	stale.CredentialRef = "stale-ref"
	if _, err := openBackupRepositoryStorage(s, stale); err == nil {
		t.Fatal("old SMB credential reference was accepted")
	}
}

func TestSMBPeerBackupRefusesMemberWithoutDestinationCapability(t *testing.T) {
	s := peerStore(t, "host-b", "pool-1")
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		CredentialRef: "receipt-1",
	}
	cmd := protocol.Command{Kind: protocol.BackupKindBackupNow, OperationID: "backup-1"}
	got := acceptBackupRequestUnrecorded(context.Background(), s, BackupRequest{Command: &cmd, PeerRepository: &repo})
	if !errors.Is(got.Err, ErrBackupInvalid) {
		t.Fatalf("unproven member accepted SMB backup dispatch: %v", got.Err)
	}
}
