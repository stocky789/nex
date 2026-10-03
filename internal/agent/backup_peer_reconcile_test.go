// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestPeerBackupObservationSettlesMissingOperationAsNotStarted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != poolBackupPeerPath+"/old-restore" {
			t.Errorf("unexpected peer request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "backup operation not found", http.StatusNotFound)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	work := pendingPeerBackup{
		run:    protocol.BackupRun{OperationID: "old-restore"},
		member: protocol.PoolMember{PeerOrigin: server.URL}, client: server.Client(), observe: true,
	}
	completion := callPeerBackup(ctx, work)
	if !peerBackupNotStarted(completion.err) || completion.run.OperationID != work.run.OperationID {
		t.Fatalf("missing exact operation must settle as never started without replay: %+v", completion)
	}
}

// An unanswered observation of a member restore must free the master's
// single backup slot for later work.
func TestUnreachableMemberRestoreObservationReleasesBackupSlot(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	if _, err := importPoolBackupCatalog(context.Background(), master, "repo-1"); err != nil {
		t.Fatal(err)
	}
	restore := protocol.Command{OperationID: "member-restore", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1", HostID: "member-1"}}
	if err := ensureBackupRunReserved(master, restore); err != nil {
		t.Fatal(err)
	}
	master.State.Journal[restore.OperationID] = JournalEntry{Command: &restore, Status: "needs_attention"}
	vm := *poolVMTestSpec("vm-local", master.State.HostID)
	later := protocol.Command{OperationID: "later-backup", Kind: protocol.BackupKindBackupNow, VM: &vm,
		Backup: &protocol.BackupNowRequest{RepositoryID: "repo-missing", Consistency: protocol.BackupCaptureStopped}}
	var deliver atomic.Bool
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := protocol.ReportResponse{}
		if deliver.Load() {
			response.Commands = []protocol.Command{later}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	master.State.Controller, master.State.TrustPEM = identity.Controller, identity.TrustPEM
	master.State.CertificatePEM, master.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := master.Save(); err != nil {
		t.Fatal(err)
	}
	observed := make(chan struct{}, 1)
	lab.intercept.Store(func(hostID string, r *http.Request) {
		if hostID == "member-1" && r.Method == http.MethodGet && r.URL.Path == poolBackupPeerPath+"/"+restore.OperationID {
			select {
			case observed <- struct{}{}:
			default:
			}
		}
	})
	lab.drop("member-1", "!*")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, master, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: 100 * time.Millisecond, ProbeTimeout: 20 * time.Millisecond})
	}()
	select {
	case <-observed:
	case err := <-done:
		t.Fatalf("Core Run stopped before observing the member: %v", err)
	}
	deliver.Store(true)
	for {
		if run, err := loadPoolBackupRun(master, later.OperationID); err == nil && run.Status == "failed" {
			break // The missing repository fails the later backup once it is dispatched.
		}
		select {
		case err := <-done:
			t.Fatalf("Core Run stopped: %v", err)
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("an unanswered member observation kept the backup slot")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Core Run stopped with error: %v", err)
	}
	if run, err := loadPoolBackupRun(master, restore.OperationID); err != nil || run.Status != "pending" {
		t.Fatalf("unanswered observation settled the member restore: %+v %v", run, err)
	}
	if pins, err := ActiveBackupRestorePins(master); err != nil || !pins["point-1"] {
		t.Fatalf("unanswered observation released the restore pin: %+v %v", pins, err)
	}
}

func TestPeerBackupMissingOperationReturnsNotFound(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	go func() {
		request := <-queue
		request.Reply <- acceptBackupRequest(context.Background(), member, request)
	}()
	client, err := poolTransferClient(master, lab.origin[member.State.HostID], member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	request, err := http.NewRequest(http.MethodGet, lab.origin[member.State.HostID]+poolBackupPeerPath+"/old-restore", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, status, err := doPeerBackup(client, request)
	if status != http.StatusNotFound || err == nil {
		t.Fatalf("authenticated missing operation: status=%d err=%v", status, err)
	}
}

type reachableBackupStorage struct{ backupRepositoryStorage }

func (reachableBackupStorage) Verify(context.Context) error { return nil }

type unreachableBackupStorage struct{ backupRepositoryStorage }

func (unreachableBackupStorage) Verify(context.Context) error {
	return errors.New("repository offline")
}

func TestPeerBackupVerificationRetriesOnlyUnreachableRepository(t *testing.T) {
	content := errors.New("published backup disk digest mismatch")
	var permanent permanentPeerBackupError
	if err := peerBackupVerificationError(context.Background(), reachableBackupStorage{}, content, "failed"); !errors.As(err, &permanent) {
		t.Fatalf("content failure in a reachable repository was retryable: %v", err)
	}
	if err := peerBackupVerificationError(context.Background(), unreachableBackupStorage{}, content, "failed"); errors.As(err, &permanent) {
		t.Fatalf("unreachable repository was permanent: %v", err)
	}
}

func TestPeerRestoreVerificationStatsPointAndRefusesMissingArtifact(t *testing.T) {
	master := seedBackupMaster(t)
	mount := t.TempDir()
	repo, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: master.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, repo.ID, repo.PoolID, "point-1")
	_, digest, err := vmhost.ReadBackupManifest(filepath.Join(mount, "point-1"))
	if err != nil {
		t.Fatal(err)
	}
	work := pendingPeerBackup{store: master, localRepo: repo,
		command: protocol.Command{Kind: protocol.BackupKindRestoreNew},
		run:     protocol.BackupRun{RecoveryPointID: "point-1"},
		point:   &protocol.BackupRecoveryPoint{ManifestSHA256: digest}}
	succeeded := poolBackupPeerResponse{Result: protocol.Result{Status: "succeeded"}}
	// The member restore already hashed every artifact; the master re-reads none of them.
	artifact := filepath.Join(mount, "point-1", "vda.qcow2")
	info, err := os.Stat(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, bytes.Repeat([]byte("x"), int(info.Size())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyPeerBackupPoint(context.Background(), work, succeeded); err != nil {
		t.Fatalf("master verification read artifact content: %v", err)
	}
	if err := os.Remove(artifact); err != nil {
		t.Fatal(err)
	}
	var permanent permanentPeerBackupError
	if err := verifyPeerBackupPoint(context.Background(), work, succeeded); !errors.As(err, &permanent) {
		t.Fatalf("missing artifact in a reachable repository must need attention, got %v", err)
	}
}

// memberRunLab reserves a scheduled occurrence for a member VM and returns the
// master's copy after it was settled needs_attention.
func memberRunLab(t *testing.T) (master, member *Store, run protocol.BackupRun, mount string) {
	lab := newTransferLab(t, 1)
	master, member = lab.host("master-1"), lab.host("member-1")
	mount = t.TempDir()
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("vm-1", member.State.HostID)
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertPoolBackupPolicy(master, testPolicy()); err != nil {
		t.Fatal(err)
	}
	run = protocol.BackupRun{OperationID: "schedule-1", Kind: protocol.BackupKindScheduled, PoolID: "pool-1", VMID: vm.ID,
		RepositoryID: "repo-1", RecoveryPointID: "schedule-1", PolicyID: "pol-1", ScheduleID: "sch-1", OccurrenceID: "schedule-1",
		PolicyRevision: 1, OccurrenceAt: time.Now().UTC(), CaptureMode: protocol.BackupCaptureStopped, Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	run, _, err := prepareScheduledBackupRun(master, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReservePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	run.Status, run.Stage, run.Error = "needs_attention", "publication", "member lost the publish reply"
	if err := SettlePoolBackupRun(master, run, nil); err != nil {
		t.Fatal(err)
	}
	run, err = loadPoolBackupRun(master, run.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	return master, member, run, mount
}

func TestNeedsAttentionMemberRunSettlesWhenMemberReconciles(t *testing.T) {
	master, member, run, mount := memberRunLab(t)
	seedPublishedPoint(t, mount, run.RepositoryID, run.PoolID, run.OperationID)
	manifest, digest, err := vmhost.ReadBackupManifest(filepath.Join(mount, run.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	memberRun := run
	memberRun.Kind, memberRun.Status, memberRun.Stage, memberRun.Error = protocol.BackupKindBackupNow, "succeeded", "published", ""
	if err := savePoolBackupRun(member, memberRun); err != nil {
		t.Fatal(err)
	}
	point := protocol.BackupRecoveryPoint{ID: run.OperationID, RepositoryID: run.RepositoryID, PoolID: run.PoolID, SourceVMID: run.VMID,
		CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency, ManifestSHA256: digest}
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	go func() {
		request := <-queue
		if request.Command != nil {
			t.Error("needs-attention observation started the backup again")
		}
		request.Reply <- BackupResponse{Result: &protocol.Result{OperationID: run.OperationID, Status: "succeeded"}, Point: &point}
	}()
	work, ok := nextPendingPeerBackup(master, peerBackoff{}, time.Now())
	if !ok || !work.observe || work.run.OperationID != run.OperationID {
		t.Fatalf("needs-attention member run was not re-observed: ok=%v %+v", ok, work.run)
	}
	settlePeerBackupCompletion(context.Background(), master, callPeerBackup(context.Background(), work))
	settled, err := loadPoolBackupRun(master, run.OperationID)
	if err != nil || settled.Status != "succeeded" {
		t.Fatalf("member reconciliation never reached the master: %+v, %v", settled, err)
	}
	if _, err := getCatalogPoint(master, run.RepositoryID, run.OperationID); err != nil {
		t.Fatalf("reconciled point missing from the master catalog: %v", err)
	}
}

func TestMemberSuccessAfterMasterRepositoryRecoverySettlesWithoutReplay(t *testing.T) {
	master, member, run, mount := memberRunLab(t)
	run.Status, run.Stage, run.Error = "pending", "reserved", ""
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, run.RepositoryID, run.PoolID, run.OperationID)
	manifest, digest, err := vmhost.ReadBackupManifest(filepath.Join(mount, run.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	memberRun := run
	memberRun.Kind, memberRun.Status, memberRun.Stage = protocol.BackupKindBackupNow, "succeeded", "published"
	if err := savePoolBackupRun(member, memberRun); err != nil {
		t.Fatal(err)
	}
	point := protocol.BackupRecoveryPoint{ID: run.OperationID, RepositoryID: run.RepositoryID, PoolID: run.PoolID,
		SourceVMID: run.VMID, CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency, ManifestSHA256: digest}
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	var replayed atomic.Int32
	go func() {
		for request := range queue {
			if request.Command != nil {
				replayed.Add(1)
			}
			request.Reply <- BackupResponse{Result: &protocol.Result{OperationID: run.OperationID, Status: "succeeded"}, Point: &point}
		}
	}()
	defer close(queue)
	var offline atomic.Bool
	offline.Store(true)
	previous := verifyBackupRepository
	verifyBackupRepository = func(ctx context.Context, repo protocol.BackupRepository) error {
		if repo.MountPath == mount && offline.Load() {
			return errors.New("master repository access is unavailable")
		}
		return previous(ctx, repo)
	}
	t.Cleanup(func() { verifyBackupRepository = previous })
	backoff := peerBackoff{}
	work, ok := nextPendingPeerBackup(master, backoff, time.Now())
	if !ok {
		t.Fatal("pending member run was not observed")
	}
	completion := callPeerBackup(context.Background(), work)
	if completion.err == nil || completion.reply.Result.Status != "succeeded" {
		t.Fatalf("member success did not expose master verification failure: %+v", completion)
	}
	settlePeerBackupCompletion(context.Background(), master, completion)
	notePeerBackupAnswer(master, backoff, completion)
	if saved, err := loadPoolBackupRun(master, run.OperationID); err != nil || saved.Status != "needs_attention" {
		t.Fatalf("unverified member success was terminalized: %+v, %v", saved, err)
	}
	// Even long silence must not turn an observed success into terminal failure.
	afterSilence := time.Now().Add(2 * peerBackupSilentLimit)
	work, ok = nextPendingPeerBackup(master, backoff, afterSilence)
	if !ok || !work.observe {
		t.Fatalf("unknown member outcome stopped observation: ok=%v, work=%+v", ok, work)
	}
	completion = callPeerBackup(context.Background(), work)
	settlePeerBackupCompletion(context.Background(), master, completion)
	if saved, err := loadPoolBackupRun(master, run.OperationID); err != nil || saved.Status != "needs_attention" {
		t.Fatalf("repository outage made outcome terminal: %+v, %v", saved, err)
	}
	offline.Store(false)
	work, ok = nextPendingPeerBackup(master, peerBackoff{}, afterSilence)
	if !ok || !work.observe {
		t.Fatal("recovered master did not resume observation-only settlement")
	}
	settlePeerBackupCompletion(context.Background(), master, callPeerBackup(context.Background(), work))
	if saved, err := loadPoolBackupRun(master, run.OperationID); err != nil || saved.Status != "succeeded" {
		t.Fatalf("late verified member success was lost: %+v, %v", saved, err)
	}
	if saved, err := getCatalogPoint(master, run.RepositoryID, run.OperationID); err != nil || saved.ManifestSHA256 != digest {
		t.Fatalf("recovered master did not catalog the exact published point: %+v, %v", saved, err)
	}
	if replayed.Load() != 0 {
		t.Fatalf("unknown outcome was replayed %d times", replayed.Load())
	}
}

func TestNeedsAttentionMemberRunWithoutMemberRecordSettlesFailed(t *testing.T) {
	master, member, run, _ := memberRunLab(t)
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	go func() {
		request := <-queue
		request.Reply <- BackupResponse{Err: ErrBackupNotFound}
	}()
	work, ok := nextPendingPeerBackup(master, peerBackoff{}, time.Now())
	if !ok {
		t.Fatal("needs-attention member run was not re-observed")
	}
	settlePeerBackupCompletion(context.Background(), master, callPeerBackup(context.Background(), work))
	if settled, err := loadPoolBackupRun(master, run.OperationID); err != nil || settled.Status != "failed" {
		t.Fatalf("never-started member run stayed unsettled: %+v, %v", settled, err)
	}
}

func TestPendingMemberRunForMovedVMSettlesFailedWithoutPost(t *testing.T) {
	master, member, run, _ := memberRunLab(t)
	run.Status, run.Stage, run.Error = "pending", "reserved", ""
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec(run.VMID, master.State.HostID)
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm}}}); err != nil {
		t.Fatal(err)
	}
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	go func() {
		request := <-queue
		if request.Command != nil {
			t.Error("moved VM's backup was posted to its former host")
		}
		request.Reply <- BackupResponse{Err: ErrBackupNotFound}
	}()
	work, ok := nextPendingPeerBackup(master, peerBackoff{}, time.Now())
	if !ok || work.member.HostID != member.State.HostID {
		t.Fatalf("pending run for a moved VM was skipped: ok=%v", ok)
	}
	settlePeerBackupCompletion(context.Background(), master, callPeerBackup(context.Background(), work))
	if settled, err := loadPoolBackupRun(master, run.OperationID); err != nil || settled.Status != "failed" {
		t.Fatalf("moved VM's pending run stayed unsettled: %+v, %v", settled, err)
	}
}

func TestPeerPreflightRefusalBeforeAdmissionSettlesFailed(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerS3Repository(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("vm-1", member.State.HostID)
	cmd := protocol.Command{OperationID: "s3-refused-backup", Kind: protocol.BackupKindBackupNow, VM: &vm,
		Backup: &protocol.BackupNowRequest{RepositoryID: repo.ID, Consistency: protocol.BackupCaptureStopped}}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: master.State.PoolID,
		HostID: member.State.HostID, VMID: vm.ID, RepositoryID: repo.ID, RecoveryPointID: cmd.OperationID,
		IntentSHA256: intent, Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	work, err := peerBackupWorkForCommand(master, cmd, run)
	if err != nil {
		t.Fatal(err)
	}
	// The registration changes after the work was built, so the preflight refuses it.
	changed := repo
	changed.CredentialRef = "other-receipt"
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: changed}}); err != nil {
		t.Fatal(err)
	}
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	go func() {
		request := <-queue
		if request.Command != nil {
			t.Error("refused preflight still posted the backup")
		}
		request.Reply <- BackupResponse{Err: ErrBackupNotFound}
	}()
	if completion := callPeerBackup(context.Background(), work); !peerBackupNotStarted(completion.err) {
		t.Fatalf("preflight refusal after an absent member record must settle as not started: %v", completion.err)
	}
}

func TestCredentialPullSkipsOneUnexportableRepository(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerS3Repository(t, master)
	broken := repo
	broken.ID, broken.CredentialRef = "repo-2", "credential-lost"
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{
		repo.ID: repo, broken.ID: broken}}); err != nil {
		t.Fatal(err)
	}
	if err := pullPeerBackupCredentials(context.Background(), member, lab.origin[master.State.HostID]); err != nil {
		t.Fatalf("one lost receipt blocked every credential: %v", err)
	}
	if _, err := localBackupCredential(member, repo); err != nil {
		t.Fatalf("exportable credential was not delivered: %v", err)
	}
}

func TestCredentialDeliveryDoesNotHoldBackupAuthority(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerS3Repository(t, master)
	config, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	var delivered, heldAuthority atomic.Bool
	handler := PoolPeerHandler(member)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == poolBackupPeerPath+"/credential" {
			delivered.Store(true)
			if poolBackupAuthorityMu.TryLock() {
				poolBackupAuthorityMu.Unlock()
			} else {
				heldAuthority.Store(true)
			}
		}
		handler.ServeHTTP(w, r)
	}))
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	setMemberPeerOrigin(t, master, member.State.HostID, server.URL)
	if _, err := upsertPoolBackupRepositoryPrepared(master, repo, repo, false, nil); err != nil {
		t.Fatal(err)
	}
	if delivered.Load() {
		t.Fatal("registration commit synchronously delivered a credential")
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	if failed := projectBackupCredentialToMembers(context.Background(), master, membership, repo); len(failed) != 0 {
		t.Fatalf("background credential delivery: %v", failed)
	}
	if !delivered.Load() || heldAuthority.Load() {
		t.Fatalf("credential delivery: delivered=%v under authority lock=%v", delivered.Load(), heldAuthority.Load())
	}
}

func TestPeerBackoffDoublesToCapAndResetsWhenAnswerChanges(t *testing.T) {
	b, now := peerBackoff{}, time.Now()
	for _, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute} {
		b.observed("op", "refused", now)
		if got := b["op"].next.Sub(now); got != want {
			t.Fatalf("delay = %v, want %v", got, want)
		}
	}
	for range 10 {
		b.observed("op", "refused", now)
	}
	if got := b["op"].next.Sub(now); got != peerBackupRetryMax {
		t.Fatalf("delay = %v, want the cap", got)
	}
	b.observed("op", "different refusal", now)
	if got := b["op"].next.Sub(now); got != peerBackupRetry {
		t.Fatalf("changed answer kept the old delay: %v", got)
	}
	b.observed("op", "different refusal", now)
	b.observed("op", "", now)
	if got := b["op"].next.Sub(now); got != peerBackupRetry {
		t.Fatalf("a normal answer kept the old delay: %v", got)
	}
	if !b.skipping(nil, now)["op"] || b.skipping(nil, now.Add(peerBackupRetry))["op"] {
		t.Fatal("the direct path must skip a run only while it backs off")
	}
}

func TestNeedsAttentionMemberRunIsReobservedOnlyAfterBackoff(t *testing.T) {
	master, _, run, _ := memberRunLab(t)
	backoff, now := peerBackoff{}, time.Now()
	if _, ok := nextPendingPeerBackup(master, backoff, now); !ok {
		t.Fatal("needs-attention member run was not observed")
	}
	backoff.observed(run.OperationID, "member refused", now)
	if _, ok := nextPendingPeerBackup(master, backoff, now.Add(peerBackupRetry-time.Second)); ok {
		t.Fatal("run observed again before its backoff expired")
	}
	if _, ok := nextPendingPeerBackup(master, backoff, now.Add(peerBackupRetry)); !ok {
		t.Fatal("run not observed after its backoff expired")
	}
	backoff.observed(run.OperationID, "member refused", now.Add(peerBackupRetry))
	if _, ok := nextPendingPeerBackup(master, backoff, now.Add(2*peerBackupRetry)); ok {
		t.Fatal("the same refusal did not double the wait")
	}
}

// Regression for BUG-0751: transport errors differ in ports and timings on every attempt,
// so they back off as one class.
func TestPeerBackoffTreatsUnreachableMemberAsOneShortCappedClass(t *testing.T) {
	first := peerBackupAnswer(peerBackupCompletion{err: errors.New("read tcp 192.0.2.1:43122->192.0.2.2:9443: i/o timeout")})
	second := peerBackupAnswer(peerBackupCompletion{err: errors.New("read tcp 192.0.2.1:50001->192.0.2.2:9443: i/o timeout")})
	if first != second || first != peerBackupUnreachable {
		t.Fatalf("transport errors are classified by their text: %q %q", first, second)
	}
	b, now := peerBackoff{}, time.Now()
	for range 12 {
		b.observed("op", first, now)
	}
	if got := b["op"].next.Sub(now); got != peerBackupRetryMax {
		t.Fatalf("unreachable delay = %v, want %v", got, peerBackupRetryMax)
	}
	permanent := peerBackupAnswer(peerBackupCompletion{err: permanentPeerBackupError{errors.New("member refused")}})
	if permanent != "member refused" {
		t.Fatalf("permanent answer = %q", permanent)
	}
}

// A member that refuses a backup before journaling it (for example a crash-consistent
// backup of a shut-off VM) must have its real reason reach the run, not just "never started".
func TestPeerBackupRefusalKeepsMemberReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, "backup operation not found", http.StatusNotFound)
			return
		}
		http.Error(w, "invalid backup request: online backup requires a running VM", http.StatusBadRequest)
	}))
	defer server.Close()
	vm := *poolVMTestSpec("vm-1", "member-1")
	work := pendingPeerBackup{
		run:     protocol.BackupRun{OperationID: "crash-off"},
		command: protocol.Command{OperationID: "crash-off", Kind: protocol.BackupKindBackupNow, VM: &vm},
		repo:    protocol.BackupRepository{Kind: protocol.BackupRepoKindNFS},
		member:  protocol.PoolMember{HostID: "member-1", PeerOrigin: server.URL}, client: server.Client(),
	}
	completion := callPeerBackup(context.Background(), work)
	if !peerBackupNotStarted(completion.err) || !strings.Contains(completion.err.Error(), "online backup requires a running VM") {
		t.Fatalf("member refusal reason was dropped: %v", completion.err)
	}
}

// Regression for BUG-0777: a member that cannot serve one repository must not hold the
// pool's single backup slot (the wait loop retried it forever), and a run the member
// never started settles failed with the reason instead of holding its repository.
func TestUnservableMemberReleasesBackupSlotAndSettlesNeverStartedRun(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerS3Repository(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("vm-1", member.State.HostID)
	run := protocol.BackupRun{OperationID: "op-1", PoolID: master.State.PoolID, HostID: member.State.HostID,
		RepositoryID: repo.ID, Kind: protocol.BackupKindBackupNow, VMID: vm.ID, CreatedAt: time.Now()}
	cmd := protocol.Command{OperationID: run.OperationID, Kind: protocol.BackupKindBackupNow, VM: &vm,
		Backup: &protocol.BackupNowRequest{RepositoryID: repo.ID}}
	work, err := peerBackupWorkForCommand(master, cmd, run)
	if err != nil {
		t.Fatal(err)
	}
	lab.drop(member.State.HostID, "POST:"+poolBackupPeerPath+"/capability/s3")
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	go func() {
		for request := range queue {
			request.Reply <- BackupResponse{Err: ErrBackupNotFound} // the member has no record of the run
		}
	}()
	defer close(queue)

	waited := make(chan peerBackupCompletion, 1)
	go func() { waited <- waitPeerBackupTerminal(context.Background(), work) }()
	select {
	case completion := <-waited:
		if completion.err == nil || peerBackupNotStarted(completion.err) {
			t.Fatalf("a fresh run must stay retryable: %v", completion.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait loop kept the backup slot while the member could not serve the repository")
	}

	work.run.CreatedAt = time.Now().Add(-peerBackupStartLimit - time.Minute)
	completion := callPeerBackup(context.Background(), work)
	if !peerBackupNotStarted(completion.err) || !strings.Contains(completion.err.Error(), "S3 backup capability") {
		t.Fatalf("stale never-started run did not settle with its reason: %v", completion.err)
	}
}
