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
	prepareBackupRepositorySMBNative = func(_ context.Context, got protocol.BackupRepository, creds protocol.StorageCredentials) (protocol.BackupRepository, error) {
		called++
		if got.ID != repo.ID || creds != handoff.Credentials {
			t.Fatalf("incorrect SMB prepare scope: %+v %+v", got, creds)
		}
		got.MountPath = t.TempDir()
		got.SMBIdentity = &protocol.SMBStorageIdentity{Server: repo.SMBSource.Server, Share: repo.SMBSource.Share, MarkerID: strings.Repeat("a", 32)}
		return got, nil
	}
	if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, repo, "register-other", "", nil); err == nil || called != 0 {
		t.Fatalf("wrong registration operation reached mount: %v, calls=%d", err, called)
	}
	invalid := repo
	invalid.SMBSource = &protocol.SMBStorageSource{Server: "smb.example.test", Share: "../backups"}
	if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, invalid, "register-1", "", nil); err == nil || called != 0 {
		t.Fatalf("invalid SMB share reached mount: %v, calls=%d", err, called)
	}
	prepared, err := prepareBackupRepositoryForRegistration(context.Background(), s, repo, "register-1", "", nil)
	if err != nil || called != 1 {
		t.Fatalf("prepare: %+v, %v, calls=%d", prepared, err, called)
	}
	changed := prepared
	changed.SMBIdentity = &protocol.SMBStorageIdentity{Server: "other.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)}
	if _, err := upsertPoolBackupRepositoryPrepared(s, repo, changed, false, nil); err == nil {
		t.Fatal("changed prepared SMB identity was accepted")
	}
	saved, err := upsertPoolBackupRepositoryPrepared(s, repo, prepared, false, nil)
	if err != nil {
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
	backend, err := openBackupRepositoryStorage(s, stale)
	if err != nil {
		t.Fatalf("reads through the mounted SMB repository needed its credential: %v", err)
	}
	if err := backend.Verify(context.Background()); err == nil {
		t.Fatal("old SMB credential reference was accepted for writes")
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

func TestAdoptedSMBRepositoryVerifiesReplicatedMarker(t *testing.T) {
	s := seedBackupMaster(t)
	receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
		Offer: protocol.StorageCredentialOffer{RequestID: "credential-smb",
			RegistrationRequestID: "backup-credential:smb3:register-smb",
			PoolID:                s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute)},
		Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "example-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pool := &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)}
	replicated := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindSMB3,
		SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"}, SMBIdentity: pool,
		CredentialRef: receipt.Receipt, MountPath: "/old"}
	previous := prepareBackupRepositorySMBNative
	t.Cleanup(func() { prepareBackupRepositorySMBNative = previous })
	var expected *protocol.SMBStorageIdentity
	prepareBackupRepositorySMBNative = func(_ context.Context, r protocol.BackupRepository, _ protocol.StorageCredentials) (protocol.BackupRepository, error) {
		expected = r.SMBIdentity
		if r.SMBIdentity == nil || r.SMBIdentity.MarkerID != pool.MarkerID {
			return protocol.BackupRepository{}, errors.New("SMB repository marker does not match registration")
		}
		r.MountPath = t.TempDir()
		return r, nil
	}
	_, _, release, err := stagePoolBackupAuthorityActivation(s, &protocol.PoolBackupAuthority{Repositories: []protocol.BackupRepository{replicated}}, 5)
	if err != nil {
		t.Fatalf("promotion refused the replicated share: %v", err)
	}
	release()
	if !samePeerSMBIdentity(expected, pool) {
		t.Fatalf("promotion prepared the share without the pool marker: %+v", expected)
	}
	unexpected := portablePeerRepository(replicated)
	if _, err := prepareBackupRepositoryForAdoption(context.Background(), s, unexpected, nil); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("adoption without the pool marker could create a new one: %v", err)
	}
}
