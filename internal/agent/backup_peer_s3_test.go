// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"nex.local/nex/internal/protocol"
)

// The HTTPS object server is a contract fake; native S3 durability and fencing need a lab gate.
func testPeerS3Repository(t *testing.T, master *Store) protocol.BackupRepository {
	repo, _ := testPeerS3RepositoryWithFake(t, master)
	return repo
}

func testPeerS3RepositoryWithFake(t *testing.T, master *Store) (protocol.BackupRepository, *fakeS3) {
	t.Helper()
	fake := &fakeS3{objects: map[string][]byte{}}
	server := httptest.NewTLSServer(fake)
	t.Cleanup(server.Close)
	previous := newBackupS3Client
	newBackupS3Client = func(options s3.Options) *s3.Client {
		options.HTTPClient = server.Client()
		return s3.New(options)
	}
	t.Cleanup(func() { newBackupS3Client = previous })
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Backup",
		Kind: protocol.BackupRepoKindS3, S3Source: &protocol.BackupS3Source{Endpoint: server.URL, Bucket: "backups", Prefix: "pool-1"}, Generation: 1}
	repo.CredentialRef = peerTestBackupCredential(t, master, "credential-s3", "register-s3", "private-example-secret")
	prepared, err := prepareS3BackupRepository(context.Background(), master, repo,
		protocol.StorageCredentials{Username: "example-key", Secret: "private-example-secret"})
	if err != nil {
		t.Fatal(err)
	}
	return prepared, fake
}

func TestPeerS3PreflightRequiresPrivateReceiptExactIdentityAndGeneration(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerS3Repository(t, master)
	if _, err := preparePeerS3Repository(context.Background(), member, repo); err == nil {
		t.Fatal("member prepared S3 without private receipt")
	}
	projection, err := exportPeerBackupCredential(master, repo, member.State.HostID)
	if err != nil || importPeerBackupCredential(member, projection) != nil {
		t.Fatalf("private projection: %v", err)
	}
	prepared, err := preparePeerS3Repository(context.Background(), member, repo)
	if err != nil || !preparedBackupRepositoryMatches(repo, prepared) {
		t.Fatalf("member S3 capability: %v", err)
	}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: prepared}}); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []protocol.BackupRepository{
		func() protocol.BackupRepository {
			r := repo
			r.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("b", 64)}
			return r
		}(),
		func() protocol.BackupRepository {
			r := repo
			r.S3Source = &protocol.BackupS3Source{Endpoint: repo.S3Source.Endpoint, Bucket: "other-bucket", Prefix: repo.S3Source.Prefix}
			return r
		}(),
		func() protocol.BackupRepository { r := repo; r.CredentialRef = "other-receipt"; return r }(),
	} {
		if _, err := preparePeerS3Repository(context.Background(), member, changed); !errors.Is(err, ErrBackupConflict) {
			t.Fatalf("changed identity or receipt accepted: %v", err)
		}
	}
	stale := repo
	stale.Generation++
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: stale}}); err != nil {
		t.Fatal(err)
	}
	if _, err := preparePeerS3Repository(context.Background(), member, repo); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("stale generation accepted: %v", err)
	}
}

func TestPeerS3PreflightAuthenticatedLostReplyAndOldMember(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerS3Repository(t, master)
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
	client, err := poolTransferClient(master, peer.PeerOrigin, peer.HostID)
	if err != nil {
		t.Fatal(err)
	}
	work := pendingPeerBackup{store: master, repo: repo, localRepo: repo, member: peer, client: client}
	lab.drop(member.State.HostID, "POST:"+poolBackupPeerPath+"/capability/s3")
	if err := preflightPeerS3Repository(context.Background(), work); err == nil {
		t.Fatal("lost capability reply accepted")
	}
	lab.drop(member.State.HostID, "")
	if err := preflightPeerS3Repository(context.Background(), work); err != nil {
		t.Fatalf("exact capability retry: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, poolBackupPeerPath+"/capability/s3", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	PoolBackupPeerHandler(member).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated capability status = %d", response.Code)
	}

	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	work.member.PeerOrigin, work.client = old.URL, old.Client()
	if err := preflightPeerS3Repository(context.Background(), work); err == nil {
		t.Fatal("old member without S3 capability accepted")
	} else {
		var permanent permanentPeerBackupError
		if !errors.As(err, &permanent) {
			t.Fatalf("old member refusal is retryable: %v", err)
		}
	}
	state := peerState(t, master)
	state.Transfer = &poolTransferRecord{Frozen: true}
	if err := savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
	if err := preflightPeerS3Repository(context.Background(), work); err == nil {
		t.Fatal("fenced master attempted S3 member preflight")
	}
}

func TestPeerS3CapabilityRejectsChangedResponse(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	repo := testPeerS3Repository(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(peerS3CapabilityResponse{Generation: repo.Generation, CredentialRef: repo.CredentialRef, Fingerprint: strings.Repeat("c", 64)})
	}))
	defer server.Close()
	work := pendingPeerBackup{store: master, repo: repo, localRepo: repo,
		member: protocol.PoolMember{PeerOrigin: server.URL}, client: server.Client()}
	if err := preflightPeerS3Repository(context.Background(), work); err == nil {
		t.Fatal("changed S3 identity accepted")
	} else {
		var permanent permanentPeerBackupError
		if !errors.As(err, &permanent) {
			t.Fatalf("changed identity was retryable: %v", err)
		}
	}
}

func TestPeerS3RestoreVerificationTimeoutCanRetry(t *testing.T) {
	master := seedBackupMaster(t)
	repo := testPeerS3Repository(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	work := pendingPeerBackup{store: master, localRepo: repo,
		command: protocol.Command{Kind: protocol.BackupKindRestoreNew},
		run:     protocol.BackupRun{RecoveryPointID: "point-1"},
		point:   &protocol.BackupRecoveryPoint{ManifestSHA256: strings.Repeat("a", 64)}}
	err := verifyPeerBackupPoint(ctx, work, poolBackupPeerResponse{Result: protocol.Result{Status: "succeeded"}})
	var permanent permanentPeerBackupError
	if !errors.Is(err, context.Canceled) || errors.As(err, &permanent) {
		t.Fatalf("temporary S3 verification failure cannot be permanent: %v", err)
	}
}

func TestPeerS3DispatchSendsPortableRepositoryAndReconcilesReplay(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerS3Repository(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("vm-1", member.State.HostID)
	cmd := protocol.Command{OperationID: "s3-peer-backup", Kind: protocol.BackupKindBackupNow, VM: &vm,
		Backup: &protocol.BackupNowRequest{RepositoryID: repo.ID, Consistency: protocol.BackupCaptureStopped}}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: master.State.PoolID,
		HostID: member.State.HostID, VMID: vm.ID, RepositoryID: repo.ID, RecoveryPointID: cmd.OperationID,
		IntentSHA256: intent, Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	seen := make(chan protocol.BackupRepository, 1)
	go func() {
		for i := 0; i < 3; i++ {
			request := <-queue
			if request.Command == nil {
				if i == 0 {
					request.Reply <- BackupResponse{Err: ErrBackupNotFound}
				} else {
					request.Reply <- BackupResponse{Result: &protocol.Result{OperationID: run.OperationID, Status: "pending"}}
				}
				continue
			}
			seen <- *request.PeerRepository
			if err := savePoolBackupRun(member, run); err != nil {
				request.Reply <- BackupResponse{Err: err}
				continue
			}
			request.Reply <- BackupResponse{Result: &protocol.Result{OperationID: run.OperationID, Status: "pending"}}
		}
	}()
	work, err := peerBackupWorkForCommand(master, cmd, run)
	if err != nil {
		t.Fatal(err)
	}
	first := callPeerBackup(context.Background(), work)
	if first.err != nil || first.reply.Result.Status != "pending" {
		t.Fatalf("S3 member dispatch: %v %+v", first.err, first)
	}
	wire := <-seen
	if wire.MountPath != "" || wire.Identity != nil || wire.SMBIdentity != nil || wire.Access.Status != "" ||
		wire.S3Source == nil || wire.CredentialRef != repo.CredentialRef || wire.RemoteIdentity == nil ||
		wire.RemoteIdentity.Fingerprint != repo.RemoteIdentity.Fingerprint {
		t.Fatalf("S3 peer payload lost identity or carried host-local state: %+v", wire)
	}
	second := callPeerBackup(context.Background(), work)
	if second.err != nil || second.reply.Result.Status != "pending" {
		t.Fatalf("exact operation replay: %+v", second)
	}
}

func TestS3PruneResumesInterruptedManifestDeletion(t *testing.T) {
	master := seedBackupMaster(t)
	repo, fake := testPeerS3RepositoryWithFake(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	backend, err := openBackupRepositoryStorage(master, repo)
	if err != nil {
		t.Fatal(err)
	}
	storage := backend.(*s3BackupStorage)
	payload := []byte("disk")
	manifest := testS3Manifest(payload)
	stage := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(stage, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Publish(context.Background(), manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		t.Fatal(err)
	}
	catalogS3Point(t, storage, manifest)
	fake.mu.Lock()
	fake.failManifestDelete = true
	fake.mu.Unlock()
	retention := protocol.BackupRetention{KeepLatest: 0}
	if err := prunePublishedBackupPayloads(master, repo.ID, manifest.SourceVMID, retention); err == nil {
		t.Fatal("lost manifest delete reply reported successful prune")
	}
	if _, err := getCatalogPoint(master, repo.ID, manifest.RecoveryPointID); err != nil {
		t.Fatalf("interrupted prune removed catalog evidence: %v", err)
	}
	fake.mu.Lock()
	_, tombstone := fake.objects[storage.pointKey(manifest.RecoveryPointID, s3BackupDeleteKey)]
	fake.failManifestDelete = false
	fake.mu.Unlock()
	if !tombstone {
		t.Fatal("interrupted prune lost durable S3 deletion record")
	}
	if err := prunePublishedBackupPayloads(master, repo.ID, manifest.SourceVMID, retention); err != nil {
		t.Fatalf("S3 prune resume: %v", err)
	}
	if _, err := getCatalogPoint(master, repo.ID, manifest.RecoveryPointID); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("pruned catalog point remains: %v", err)
	}
	if keys, err := storage.list(context.Background(), storage.pointKey(manifest.RecoveryPointID, "")); err != nil || len(keys) != 0 {
		t.Fatalf("pruned S3 objects remain: %v %v", keys, err)
	}
}
