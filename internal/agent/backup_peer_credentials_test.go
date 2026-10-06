// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func peerTestBackupCredential(t *testing.T, s *Store, requestID, operationID, secret string) string {
	t.Helper()
	receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
		Offer: protocol.StorageCredentialOffer{RequestID: requestID,
			RegistrationRequestID: "backup-credential:s3:" + operationID,
			PoolID:                s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute)},
		Credentials: protocol.StorageCredentials{Username: "example-key", Secret: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt.Receipt
}

func TestPeerBackupCredentialScopeAndPromotion(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}}
	repo.CredentialRef = peerTestBackupCredential(t, master, "credential-1", "register-1", "private-example-secret")
	projection, err := exportPeerBackupCredential(master, repo, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if err := importPeerBackupCredential(member, projection); err != nil {
		t.Fatal(err)
	}
	if err := importPeerBackupCredential(member, projection); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	for _, changed := range []peerBackupCredential{
		func() peerBackupCredential { p := projection; p.RecipientHostID = "other-member"; return p }(),
		func() peerBackupCredential { p := projection; p.Repository.PoolID = "other-pool"; return p }(),
		func() peerBackupCredential { p := projection; p.Repository.ID = "other-repo"; return p }(),
		func() peerBackupCredential { p := projection; p.Repository.Kind = "unsupported"; return p }(),
		func() peerBackupCredential { p := projection; p.Credentials.Secret = "changed"; return p }(),
	} {
		if err := importPeerBackupCredential(member, changed); err == nil {
			t.Fatalf("wrong scope or replay accepted: %+v", changed.Repository)
		}
	}
	if _, err := exportPeerBackupCredential(member, repo, master.State.HostID); err == nil {
		t.Fatal("member exported master-scoped receipt")
	}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveBackupCredential(member, repo); err != nil || got.Secret != "private-example-secret" {
		t.Fatalf("member credential: %v", err)
	}
	missing := repo
	missing.CredentialRef = "missing"
	root, _, release, err := stagePoolBackupAuthorityActivation(member, &protocol.PoolBackupAuthority{Repositories: []protocol.BackupRepository{missing}}, 3)
	if err != nil {
		t.Fatalf("missing private receipt blocked promotion: %v", err)
	}
	defer release()
	if adopted, err := loadPoolBackupRepositories(&Store{dir: filepath.Join(member.dir, root)}); err != nil || adopted.Repositories[repo.ID].Access.Status != "failed" {
		t.Fatalf("missing private receipt not recorded as failed access: %+v, %v", adopted.Repositories[repo.ID], err)
	}

	// The master is authoritative: a replacement credential for the same repository is accepted.
	changed := repo
	changed.CredentialRef = peerTestBackupCredential(t, master, "credential-2", "register-2", "changed-example-secret")
	changedProjection, err := exportPeerBackupCredential(master, changed, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if err := importPeerBackupCredential(member, changedProjection); err != nil {
		t.Fatalf("same-ID credential change: %v", err)
	}
}

// After the master loses its sealing key and re-registers the repository, members converge
// on the new credential and their stale one is retired.
func TestMemberConvergesOnReplacedBackupCredential(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}}
	repo.CredentialRef = peerTestBackupCredential(t, master, "credential-1", "register-1", "old-example-secret")
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	if failed := projectBackupCredentialToMembers(context.Background(), master, membership, repo); len(failed) != 0 {
		t.Fatal(failed)
	}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	replaced := repo
	replaced.CredentialRef = peerTestBackupCredential(t, master, "credential-2", "register-2", "new-example-secret")
	if failed := projectBackupCredentialToMembers(context.Background(), master, membership, replaced); len(failed) != 0 {
		t.Fatalf("member refused the replacement: %v", failed)
	}
	poolBackupAuthorityMu.Lock()
	err = adoptPeerBackupRepository(member, BackupRequest{}, replaced, replaced)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		t.Fatalf("member kept the old registration: %v", err)
	}
	if got, err := ResolveBackupCredential(member, replaced); err != nil || got.Secret != "new-example-secret" {
		t.Fatalf("member cannot read the replacement: %v", err)
	}
	if _, err := ResolveBackupCredential(member, repo); err == nil {
		t.Fatal("stale credential was not retired")
	}
}

func TestJoiningMemberReceivesExistingBackupCredential(t *testing.T) {
	master := seedBackupMaster(t)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}}
	repo.CredentialRef = peerTestBackupCredential(t, master, "credential-join", "register-join", "join-example-secret")
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	member := peerStore(t, "member-join", "solo-join")
	joinPeerMember(t, master, member)
	projection, err := localBackupCredential(member, repo)
	if err != nil || projection.Credentials.Secret != "join-example-secret" {
		t.Fatalf("joined member lacks private receipt: %v", err)
	}
}

func TestPeerBackupCredentialAmbiguousDeliveryRetries(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}}
	repo.CredentialRef = peerTestBackupCredential(t, master, "credential-delivery", "register-delivery", "delivery-example-secret")
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	peer, ok := poolMemberByID(membership, member.State.HostID)
	if !ok {
		t.Fatal("member absent")
	}
	lab.drop(member.State.HostID, "POST:"+poolBackupPeerPath+"/credential")
	if err := deliverPeerBackupCredential(context.Background(), master, peer, repo); err == nil {
		t.Fatal("lost reply was treated as delivery success")
	} else {
		var permanent permanentPeerBackupError
		if errors.As(err, &permanent) {
			t.Fatal("ambiguous delivery was classified permanent")
		}
	}
	if _, err := localBackupCredential(member, repo); err != nil {
		t.Fatalf("peer did not retain ambiguous delivery: %v", err)
	}
	lab.drop(member.State.HostID, "")
	if err := deliverPeerBackupCredential(context.Background(), master, peer, repo); err != nil {
		t.Fatalf("exact retry failed: %v", err)
	}
}

func TestBackupRepositoryRegistrationWithOfflineMember(t *testing.T) {
	lab := newTransferLab(t, 2)
	master, offline, online := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}}
	repo.CredentialRef = peerTestBackupCredential(t, master, "credential-offline", "register-offline", "private-example-secret")
	prepared := repo
	prepared.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)}
	missing := repo
	missing.ID, missing.CredentialRef = "repo-missing", "missing-receipt"
	missingPrepared := missing
	missingPrepared.RemoteIdentity = prepared.RemoteIdentity
	if _, err := upsertPoolBackupRepositoryPrepared(master, missing, missingPrepared, false, nil); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("missing master credential committed: %v", err)
	}
	if _, err := GetPoolBackupRepository(master, missing.ID); !errors.Is(err, ErrBackupRepoNotFound) {
		t.Fatalf("missing master credential left a repository: %v", err)
	}
	lab.drop(offline.State.HostID, "!"+poolBackupPeerPath+"/credential")
	saved, err := upsertPoolBackupRepositoryPrepared(master, repo, prepared, false, nil)
	if err != nil {
		t.Fatalf("offline member blocked master registration: %+v, %v", saved, err)
	}
	if _, err := localBackupCredential(offline, saved); err == nil {
		t.Fatal("offline member acknowledged a credential it never received")
	}
	if _, err := localBackupCredential(online, saved); err == nil {
		t.Fatal("owner commit synchronously delivered a member credential")
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	projectBackupCredentialToMembers(context.Background(), master, membership, saved)
	if got, err := localBackupCredential(online, saved); err != nil || got.Credentials.Secret != "private-example-secret" {
		t.Fatalf("background delivery lost online member credential: %v", err)
	}
	root, _, release, err := stagePoolBackupAuthorityActivation(offline, &protocol.PoolBackupAuthority{Repositories: []protocol.BackupRepository{saved}}, 3)
	if err != nil {
		t.Fatalf("an unreadable repository blocked promotion: %v", err)
	}
	defer release()
	adopted, err := loadPoolBackupRepositories(&Store{dir: filepath.Join(offline.dir, root)})
	if got := adopted.Repositories[saved.ID]; err != nil || got.Access.Status != "failed" || got.Access.Error == "" || got.Health.Status != "offline" {
		t.Fatalf("promotion did not record the unreachable repository: %+v, %v", got, err)
	}
	if replay, err := upsertPoolBackupRepositoryPrepared(master, repo, prepared, false, nil); err != nil || replay.CredentialRef != saved.CredentialRef || !replay.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("exact registration retry changed identity: %+v, %v", replay, err)
	}
	lab.drop(offline.State.HostID, "")
	membership, err = Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	if failed := projectBackupCredentialToMembers(context.Background(), master, membership, saved); len(failed) != 0 {
		t.Fatalf("reconnected member did not receive credential: %v", failed)
	}
	if got, err := localBackupCredential(offline, saved); err != nil || got.Credentials.Secret != "private-example-secret" {
		t.Fatalf("reconnected member lost credential: %v", err)
	}
}

func TestPeerBackupCredentialExportNeedsWritableAuthority(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}}
	repo.CredentialRef = peerTestBackupCredential(t, master, "credential-authority", "register-authority", "authority-example-secret")
	if _, err := exportPeerBackupCredential(master, repo, member.State.HostID); err != nil {
		t.Fatal(err)
	}
	state := peerState(t, master)
	state.Transfer = &poolTransferRecord{Frozen: true}
	if err := savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
	if _, err := exportPeerBackupCredential(master, repo, member.State.HostID); err == nil {
		t.Fatal("frozen master exported private receipt")
	}
}

// An abandoned registration attempt leaves an older receipt for the same repository ID;
// the committed registration retires it, but never a newer pending handoff.
func TestSupersededBackupCredentialRetirement(t *testing.T) {
	s := seedBackupMaster(t)
	var removed []string
	previous := removeBackupSMBCredentialNative
	t.Cleanup(func() { removeBackupSMBCredentialNative = previous })
	removeBackupSMBCredentialNative = func(reference string) error { removed = append(removed, reference); return nil }
	abandoned := poolChangeCredential(t, s, "credential-old", protocol.BackupRepoKindSMB3, "repo-1", "old-example-secret")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Backup", Kind: protocol.BackupRepoKindSMB3,
		SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"}}
	repo.CredentialRef = poolChangeCredential(t, s, "credential-new", protocol.BackupRepoKindSMB3, "repo-1", "new-example-secret")
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	future := repo
	future.CredentialRef = poolChangeCredential(t, s, "credential-future", protocol.BackupRepoKindSMB3, "repo-1", "future-example-secret")
	if err := retireSupersededBackupCredentials(s, repo); err != nil {
		t.Fatal(err)
	}
	old := repo
	old.CredentialRef = abandoned
	if _, err := ResolveBackupCredentialForAdoption(s, old); err == nil {
		t.Fatal("abandoned receipt kept its material")
	}
	if !slices.Equal(removed, []string{abandoned}) {
		t.Fatalf("sealed mount credentials removed: %v, want only the abandoned one", removed)
	}
	if got, err := ResolveBackupCredentialForAdoption(s, repo); err != nil || got.Secret != "new-example-secret" {
		t.Fatalf("registered receipt: %v", err)
	}
	if _, err := ResolveBackupCredentialForRegistration(s, future, "register-credential-future"); err != nil {
		t.Fatalf("pending handoff was retired before registration: %v", err)
	}
}

func TestJoinBackupCredentialSyncRetryAndNFSOnly(t *testing.T) {
	for _, withSecret := range []bool{false, true} {
		name := "nfs-only"
		if withSecret {
			name = "secret-bearing"
		}
		t.Run(name, func(t *testing.T) {
			master := seedBackupMaster(t)
			repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup"}
			if withSecret {
				repo.Kind = protocol.BackupRepoKindS3
				repo.S3Source = &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}
				repo.CredentialRef = peerTestBackupCredential(t, master, "credential-join", "register-join", "join-example-secret")
			} else {
				repo.Kind = protocol.BackupRepoKindNFS
				repo.Source = protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backup"}
			}
			if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
				t.Fatal(err)
			}
			member := peerStore(t, "member-join", "solo-join")
			csr, err := PoolJoinCSR(member)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
				t.Fatal(err)
			}
			config, err := PoolPeerTLSConfig(master)
			if err != nil {
				t.Fatal(err)
			}
			var blocked atomic.Bool
			blocked.Store(true)
			var gets atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == poolBackupPeerPath+"/credentials" {
					gets.Add(1)
					if blocked.Load() {
						http.NotFound(w, r)
						return
					}
				}
				PoolPeerHandler(master).ServeHTTP(w, r)
			}))
			server.TLS = config
			server.StartTLS()
			t.Cleanup(server.Close)
			ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
			if err != nil {
				t.Fatal(err)
			}
			err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer)
			pending := peerState(t, member).PendingRehome
			if err == nil || pending == nil || gets.Load() != 1 {
				t.Fatalf("missing durable sync retry: pending=%+v gets=%d err=%v", pending, gets.Load(), err)
			}
			blocked.Store(false)
			// The cluster worker must never rehome: it does not own the Store.
			if err := RetryPoolPeerOrigin(context.Background(), member); err != nil || peerState(t, member).PendingRehome == nil {
				t.Fatalf("cluster worker resumed the pending rehome: err=%v", err)
			}
			// The Run loop's pool-join resumption finishes it on the owning goroutine.
			if err := ResumePoolRehome(context.Background(), member); err != nil {
				t.Fatal(err)
			}
			if peerState(t, member).PendingRehome != nil {
				t.Fatal("completed sync remained pending")
			}
			if withSecret {
				if _, err := localBackupCredential(member, repo); err != nil {
					t.Fatalf("retry lost credential: %v", err)
				}
			}
		})
	}
}
