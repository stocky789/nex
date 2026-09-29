// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestPeerBackupCredentialScopeRotationAndPromotion(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}, Generation: 1}
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
	if _, _, err := stagePoolBackupAuthorityActivation(member, &protocol.PoolBackupAuthority{Repositories: []protocol.BackupRepository{
		func() protocol.BackupRepository { r := repo; r.CredentialRef = "missing"; return r }(),
	}}, 3); err == nil {
		t.Fatal("promotion accepted missing private receipt")
	}

	rotated := repo
	rotated.Generation++
	rotated.CredentialRef = peerTestBackupCredential(t, master, "credential-2", "register-2", "rotated-example-secret")
	newProjection, err := exportPeerBackupCredential(master, rotated, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if err := importPeerBackupCredential(member, newProjection); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: rotated}}); err != nil {
		t.Fatal(err)
	}
	if err := RetireBackupCredential(member, repo, rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBackupCredential(member, repo); err == nil {
		t.Fatal("old generation resolved after rotation")
	}
	if got, err := ResolveBackupCredential(member, rotated); err != nil || got.Secret != "rotated-example-secret" {
		t.Fatalf("rotated credential: %v", err)
	}
	if err := importPeerBackupCredential(member, projection); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("old projection replay: %v", err)
	}
}

func TestJoiningMemberReceivesExistingBackupCredential(t *testing.T) {
	master := seedBackupMaster(t)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}, Generation: 1}
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
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}, Generation: 1}
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
	if _, err := upsertPoolBackupRepositoryPrepared(master, missing, missingPrepared); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("missing master credential committed: %v", err)
	}
	if _, err := GetPoolBackupRepository(master, missing.ID); !errors.Is(err, ErrBackupRepoNotFound) {
		t.Fatalf("missing master credential left a repository: %v", err)
	}
	lab.drop(offline.State.HostID, "!"+poolBackupPeerPath+"/credential")
	saved, err := upsertPoolBackupRepositoryPrepared(master, repo, prepared)
	if err != nil || saved.Generation != 1 {
		t.Fatalf("offline member blocked master registration: %+v, %v", saved, err)
	}
	if _, err := localBackupCredential(offline, saved); err == nil {
		t.Fatal("offline member acknowledged a credential it never received")
	}
	if got, err := localBackupCredential(online, saved); err != nil || got.Credentials.Secret != "private-example-secret" {
		t.Fatalf("online member lost credential: %v", err)
	}
	if _, _, err := stagePoolBackupAuthorityActivation(offline, &protocol.PoolBackupAuthority{Repositories: []protocol.BackupRepository{saved}}, 3); err == nil || err.Error() != "promoted backup credential is unavailable" {
		t.Fatalf("unprepared member was promotable: %v", err)
	}
	if replay, err := upsertPoolBackupRepositoryPrepared(master, repo, prepared); err != nil || replay.Generation != 1 {
		t.Fatalf("exact registration retry changed identity: %+v, %v", replay, err)
	}
	lab.drop(offline.State.HostID, "")
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	if err := projectBackupCredentialToMembers(context.Background(), master, membership, saved); err != nil {
		t.Fatalf("reconnected member did not receive credential: %v", err)
	}
	if got, err := localBackupCredential(offline, saved); err != nil || got.Credentials.Secret != "private-example-secret" {
		t.Fatalf("reconnected member lost credential: %v", err)
	}
}

func TestPeerBackupCredentialExportNeedsWritableAuthority(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}, Generation: 1}
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

func TestIdleMemberBackupCredentialRotation(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	old := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backup"}, Generation: 1}
	old.CredentialRef = peerTestBackupCredential(t, master, "credential-old", "register-old", "old-example-secret")
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{old.ID: old}}); err != nil {
		t.Fatal(err)
	}
	projection, err := exportPeerBackupCredential(master, old, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if err := importPeerBackupCredential(member, projection); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{old.ID: old}}); err != nil {
		t.Fatal(err)
	}
	rotated := old
	rotated.Generation++
	rotated.CredentialRef = peerTestBackupCredential(t, master, "credential-new", "register-new", "new-example-secret")
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{rotated.ID: rotated}}); err != nil {
		t.Fatal(err)
	}
	if err := retireSupersededBackupCredentials(master, rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBackupCredentialForAdoption(master, old); err == nil {
		t.Fatal("master retained prior material")
	}
	future := rotated
	future.CredentialRef = peerTestBackupCredential(t, master, "credential-future", "register-future", "future-example-secret")
	if err := retireSupersededBackupCredentials(master, rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBackupCredentialForRegistration(master, future, "register-future"); err != nil {
		t.Fatalf("pending handoff was retired before registration: %v", err)
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	lab.drop(member.State.HostID, "!"+poolBackupPeerPath+"/credential")
	if err := projectBackupCredentialToMembers(context.Background(), master, membership, rotated); err == nil {
		t.Fatal("failed retirement delivery acknowledged")
	}
	if _, err := ResolveBackupCredentialForAdoption(member, old); err != nil {
		t.Fatalf("old member receipt changed without delivery: %v", err)
	}
	lab.drop(member.State.HostID, "")
	if err := projectBackupCredentialToMembers(context.Background(), master, membership, rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBackupCredentialForAdoption(member, old); err == nil {
		t.Fatal("idle member retained prior material")
	}
	if got, err := ResolveBackupCredentialForAdoption(member, rotated); err != nil || got.Secret != "new-example-secret" {
		t.Fatalf("new member receipt missing: %v", err)
	}
}

func TestJoinBackupCredentialSyncRetryAndLegacyNFS(t *testing.T) {
	for _, withSecret := range []bool{false, true} {
		name := "nfs-only"
		if withSecret {
			name = "secret-bearing"
		}
		t.Run(name, func(t *testing.T) {
			master := seedBackupMaster(t)
			repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup", Generation: 1}
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
			if withSecret {
				if err == nil || pending == nil || !pending.BackupCredentialSync || gets.Load() != 1 {
					t.Fatalf("missing durable sync retry: pending=%+v gets=%d err=%v", pending, gets.Load(), err)
				}
				blocked.Store(false)
				if err := RetryPoolPeerOrigin(context.Background(), member); err != nil {
					t.Fatal(err)
				}
				if peerState(t, member).PendingRehome != nil {
					t.Fatal("completed sync remained pending")
				}
				if _, err := localBackupCredential(member, repo); err != nil {
					t.Fatalf("retry lost credential: %v", err)
				}
			} else if err != nil || pending != nil || gets.Load() != 0 {
				t.Fatalf("NFS-only join called new route: pending=%+v gets=%d err=%v", pending, gets.Load(), err)
			}
		})
	}
}

func TestPendingBackupCredentialSyncPersistence(t *testing.T) {
	s := seedBackupMaster(t)
	state := peerState(t, s)
	state.PendingRehome = &poolRehome{PoolID: "pool-1", PoolName: "Pool", BackupCredentialSync: true}
	if err := savePoolPeer(s, state); err != nil {
		t.Fatal(err)
	}
	if loaded := peerState(t, s).PendingRehome; loaded == nil || !loaded.BackupCredentialSync {
		t.Fatal("pending sync did not survive reload")
	}
	var legacy poolRehome
	if err := json.Unmarshal([]byte(`{"PoolID":"pool-1","PoolName":"Pool"}`), &legacy); err != nil || legacy.BackupCredentialSync {
		t.Fatalf("legacy rehome did not default to no sync: %+v %v", legacy, err)
	}
}
