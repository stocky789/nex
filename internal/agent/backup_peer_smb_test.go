// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func testPeerSMBRepository(t *testing.T, master *Store) protocol.BackupRepository {
	t.Helper()
	receipt, err := saveBackupCredential(master, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "smb-credential-1", RegistrationRequestID: "backup-credential:smb3:register-1",
		PoolID: master.State.PoolID, HostID: master.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "backup", Secret: "private-example-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups", Directory: "pool-1"},
		SMBIdentity:   &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)},
		CredentialRef: receipt.Receipt, MountPath: t.TempDir()}
}

func TestPeerSMBPreflightRequiresPrivateReceiptExactIdentity(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerSMBRepository(t, master)
	portable := portablePeerRepository(repo)
	if _, err := preparePeerSMBRepository(context.Background(), member, portable, repo.SMBIdentity); err == nil {
		t.Fatal("member prepared SMB without its private receipt")
	}
	projection, err := exportPeerBackupCredential(master, repo, member.State.HostID)
	if err != nil || importPeerBackupCredential(member, projection) != nil {
		t.Fatalf("private projection: %v", err)
	}
	previous := prepareBackupRepositorySMBNative
	t.Cleanup(func() { prepareBackupRepositorySMBNative = previous })
	prepareBackupRepositorySMBNative = func(_ context.Context, got protocol.BackupRepository, credentials protocol.StorageCredentials) (protocol.BackupRepository, error) {
		if got.ID != repo.ID || credentials.Username != "backup" || credentials.Secret != "private-example-secret" {
			t.Fatalf("wrong member prepare scope: %+v %+v", got, credentials)
		}
		got.MountPath = t.TempDir()
		got.SMBIdentity = repo.SMBIdentity
		return got, nil
	}
	prepared, err := preparePeerSMBRepository(context.Background(), member, portable, repo.SMBIdentity)
	if err != nil || !samePeerSMBIdentity(prepared.SMBIdentity, repo.SMBIdentity) {
		t.Fatalf("member SMB capability: %+v, %v", prepared, err)
	}
	wrongIdentity := *repo.SMBIdentity
	wrongIdentity.MarkerID = strings.Repeat("b", 32)
	if _, err := preparePeerSMBRepository(context.Background(), member, portable, &wrongIdentity); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("wrong share marker accepted: %v", err)
	}
}

func TestPeerSMBPreflightRefusesOldMemberAndFencedMaster(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerSMBRepository(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	oldMember := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	}))
	defer oldMember.Close()
	work := pendingPeerBackup{store: master, repo: portablePeerRepository(repo), localRepo: repo,
		member: protocol.PoolMember{HostID: member.State.HostID, PeerOrigin: oldMember.URL}, client: oldMember.Client()}
	if err := preflightPeerSMBRepository(context.Background(), work); err == nil {
		t.Fatal("old member without SMB capability endpoint was accepted")
	} else {
		var permanent permanentPeerBackupError
		if !errors.As(err, &permanent) {
			t.Fatalf("old member refusal must settle without retry loop: %v", err)
		}
	}
	if requests != 1 {
		t.Fatalf("old member preflight requests = %d", requests)
	}
	state := peerState(t, master)
	state.Transfer = &poolTransferRecord{Frozen: true}
	if err := savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
	if err := preflightPeerSMBRepository(context.Background(), work); err == nil || requests != 1 {
		t.Fatalf("fenced master contacted member: %v, requests=%d", err, requests)
	}
}

func TestPeerSMBPreflightAuthenticatedLostReplyRetry(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerSMBRepository(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	peer, ok := poolMemberByID(membership, member.State.HostID)
	if !ok {
		t.Fatal("member missing")
	}
	if err := deliverPeerBackupCredential(context.Background(), master, peer, repo); err != nil {
		t.Fatal(err)
	}
	previous := prepareBackupRepositorySMBNative
	t.Cleanup(func() { prepareBackupRepositorySMBNative = previous })
	prepareCalls := 0
	prepareBackupRepositorySMBNative = func(_ context.Context, got protocol.BackupRepository, credentials protocol.StorageCredentials) (protocol.BackupRepository, error) {
		prepareCalls++
		if got.ID != repo.ID || credentials.Secret != "private-example-secret" {
			t.Fatalf("wrong SMB preflight scope: %+v %+v", got, credentials)
		}
		got.MountPath = t.TempDir()
		got.SMBIdentity = repo.SMBIdentity
		return got, nil
	}
	client, err := poolTransferClient(master, peer.PeerOrigin, peer.HostID)
	if err != nil {
		t.Fatal(err)
	}
	work := pendingPeerBackup{store: master, repo: portablePeerRepository(repo), localRepo: repo, member: peer, client: client}
	lab.drop(member.State.HostID, "POST:"+poolBackupPeerPath+"/capability/smb3")
	if err := preflightPeerSMBRepository(context.Background(), work); err == nil || prepareCalls != 1 {
		t.Fatalf("lost preflight reply was treated as success or skipped: %v, calls=%d", err, prepareCalls)
	}
	lab.drop(member.State.HostID, "")
	if err := preflightPeerSMBRepository(context.Background(), work); err != nil || prepareCalls != 2 {
		t.Fatalf("exact preflight retry failed: %v, calls=%d", err, prepareCalls)
	}
}
