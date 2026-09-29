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
	"reflect"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func testPolicy() protocol.BackupPolicy {
	return protocol.BackupPolicy{
		ID: "pol-1", ScheduleID: "sch-1", PoolID: "pool-1", Name: "Nightly", Enabled: true,
		RepositoryID: "repo-1", CaptureMode: protocol.BackupCaptureStopped, TimeZone: "UTC",
		Rule:  protocol.BackupScheduleRule{Frequency: protocol.BackupScheduleDaily, Hour: 2, Minute: 0},
		VMIDs: []string{"vm-1"}, Retention: protocol.BackupRetention{KeepLatest: 7}, Revision: 1,
		NextDispatchAt: time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC),
	}
}

func TestScheduledMemberDispatchSendsPortableRepository(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
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
	run := protocol.BackupRun{OperationID: "schedule-1", Kind: protocol.BackupKindScheduled, PoolID: "pool-1", VMID: vm.ID,
		RepositoryID: "repo-1", RecoveryPointID: "schedule-1", PolicyID: "pol-1", ScheduleID: "sch-1", OccurrenceID: "schedule-1",
		PolicyRevision: 1, OccurrenceAt: time.Now().UTC(), CaptureMode: protocol.BackupCaptureStopped, Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	run, _, err := prepareScheduledBackupRun(master, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReservePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	memberRun := run
	memberRun.HostID = member.State.HostID
	if err := savePoolBackupRun(member, memberRun); err != nil {
		t.Fatal(err)
	}
	work, ok, err := nextPendingPeerBackup(master)
	if err != nil || !ok || work.repo.MountPath != "" || !reflect.DeepEqual(work.repo.Identity, work.localRepo.Identity) || work.repo.Access.Status != "" {
		t.Fatalf("peer work retained host-local repository fields: %+v ok=%v err=%v", work.repo, ok, err)
	}
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	seen := make(chan protocol.BackupRepository, 1)
	go func() {
		for i := 0; i < 2; i++ {
			request := <-queue
			if request.Command == nil {
				request.Reply <- BackupResponse{Err: ErrBackupNotFound}
				continue
			}
			seen <- *request.PeerRepository
			request.Reply <- BackupResponse{Result: &protocol.Result{OperationID: run.OperationID, Status: "pending"}}
		}
	}()
	completion := callPeerBackup(context.Background(), work)
	if completion.err != nil || completion.reply.Result.Status != "pending" {
		t.Fatalf("authenticated peer dispatch: %+v", completion)
	}
	wire := <-seen
	if wire.MountPath != "" || !reflect.DeepEqual(wire.Identity, work.localRepo.Identity) || wire.Access.Status != "" {
		t.Fatalf("POST carried master host mount: %+v", wire)
	}
}

func TestPeerRestoreReplyRejectsSameOperationDifferentTarget(t *testing.T) {
	master := protocol.BackupRun{
		OperationID: "restore-op", Kind: protocol.BackupKindRestoreNew, PoolID: "pool-1", HostID: "member-1",
		RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: "11111111-1111-4111-8111-111111111111",
		IntentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: "pending", Stage: "reserved", CreatedAt: time.Now().UTC(),
	}
	memberRun := master
	memberRun.NewVMID = "22222222-2222-4222-8222-222222222222"
	work := pendingPeerBackup{run: master, member: protocol.PoolMember{HostID: "member-1"}}
	reply := poolBackupPeerResponse{Result: protocol.Result{OperationID: master.OperationID, Status: "pending"},
		IntentSHA256: master.IntentSHA256, Run: &memberRun}
	if peerBackupReplyMatches(work, reply) {
		t.Fatal("same operation ID must not accept a different restore target")
	}
	for name, change := range map[string]func(*protocol.BackupRun){
		"foreign pool": func(run *protocol.BackupRun) { run.PoolID = "other-pool" },
		"missing host": func(run *protocol.BackupRun) { run.HostID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			other := master
			change(&other)
			reply.Run = &other
			if peerBackupReplyMatches(work, reply) {
				t.Fatal("peer reply did not bind pool and execution host")
			}
		})
	}
}

func TestPeerTestRestoreWaitsForJournalToCatchUpWithRun(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	if _, err := ImportPoolBackupCatalog(master, "repo-1"); err != nil {
		t.Fatal(err)
	}
	point, err := getCatalogPoint(master, "repo-1", "point-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupCatalogEntry(member, point); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "test-restore-op", Kind: protocol.BackupKindTestRestore,
		TestRestore: &protocol.TestRestoreRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "11111111-1111-4111-8111-111111111111", NewVMName: "Test", StorageID: "storage-1", HostID: member.State.HostID}}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: "pool-1", HostID: member.State.HostID,
		RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: cmd.TestRestore.NewVMID, IntentSHA256: intent, ManifestSHA256: point.ManifestSHA256,
		Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	if _, err := ReservePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	memberRun := run
	memberRun.Stage, memberRun.Status = "registered", "succeeded"
	if err := savePoolBackupRun(member, memberRun); err != nil {
		t.Fatal(err)
	}
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	statuses := []string{"pending", "succeeded"}
	go func() {
		for _, status := range statuses {
			request := <-queue
			request.Reply <- BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID, Status: status}}
		}
	}()
	work, err := peerBackupWorkForCommand(master, cmd, run)
	if err != nil {
		t.Fatal(err)
	}
	first := callPeerBackup(context.Background(), work)
	var permanent permanentPeerBackupError
	if first.err == nil || errors.As(first.err, &permanent) || first.reply.Result.Status != "pending" {
		t.Fatalf("terminal run with pending journal must await owner completion: %+v", first)
	}
	second := callPeerBackup(context.Background(), work)
	if second.err != nil || second.reply.Result.Status != "succeeded" || second.reply.Run == nil || second.reply.Run.Stage != "registered" {
		t.Fatalf("settled peer result: %+v", second)
	}
	for _, status := range []string{"pending", "needs_attention"} {
		reply := second.reply
		reply.Result.Status = status
		var permanent permanentPeerBackupError
		if err := validatePeerBackupReply(context.Background(), work, reply); err == nil || errors.As(err, &permanent) {
			t.Fatalf("journal %s while run succeeded must remain retryable: %v", status, err)
		}
	}
	inconsistent := second.reply
	inconsistent.Run = &run
	if err := validatePeerBackupReply(context.Background(), work, inconsistent); !errors.As(err, &permanent) {
		t.Fatalf("succeeded journal with pending run must be rejected: %v", err)
	}
}

func TestMemberRestorePeerRoundTripPreservesRegisteredStage(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	if _, err := ImportPoolBackupCatalog(master, "repo-1"); err != nil {
		t.Fatal(err)
	}
	point, err := getCatalogPoint(master, "repo-1", "point-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupCatalogEntry(member, point); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "restore-op", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "11111111-1111-4111-8111-111111111111", NewVMName: "Restored", StorageID: "storage-1", HostID: member.State.HostID}}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: "pool-1", HostID: member.State.HostID,
		RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: cmd.Restore.NewVMID, IntentSHA256: intent, ManifestSHA256: point.ManifestSHA256,
		Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	if _, err := ReservePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	memberRun := run
	memberRun.Stage, memberRun.Status = "registered", "succeeded"
	if err := savePoolBackupRun(member, memberRun); err != nil {
		t.Fatal(err)
	}
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	go func() {
		for i := 0; i < 2; i++ {
			request := <-queue
			if request.Command == nil {
				request.Reply <- BackupResponse{Err: ErrBackupNotFound}
				continue
			}
			request.Reply <- BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded"}}
		}
	}()
	work, err := peerBackupWorkForCommand(master, cmd, run)
	if err != nil {
		t.Fatal(err)
	}
	reply := callPeerBackup(context.Background(), work)
	if reply.err != nil || reply.reply.Run == nil || reply.reply.Run.Stage != "registered" {
		t.Fatalf("restore peer reply: %+v", reply)
	}
	settled := run
	settled.Stage, settled.Status = reply.reply.Run.Stage, reply.reply.Result.Status
	if err := SettlePoolBackupRun(master, settled, nil); err != nil {
		t.Fatal(err)
	}
	stored, err := loadPoolBackupRun(master, run.OperationID)
	if err != nil || stored.Stage != "registered" || stored.Status != "succeeded" {
		t.Fatalf("master restore run: %+v err=%v", stored, err)
	}
}

func TestCleanMemberRestorePeerPersistsMasterCatalogDigest(t *testing.T) {
	for _, kind := range []string{protocol.BackupKindRestoreNew, protocol.BackupKindTestRestore, protocol.BackupKindRestoreReplace} {
		t.Run(kind, func(t *testing.T) {
			lab := newTransferLab(t, 1)
			master, member := lab.host("master-1"), lab.host("member-1")
			mount := t.TempDir()
			setTestPromotionBackupMount(t, mount)
			seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1", "11111111-1111-4111-8111-111111111111")
			if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
				ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
				Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := ImportPoolBackupCatalog(master, "repo-1"); err != nil {
				t.Fatal(err)
			}
			point, err := getCatalogPoint(master, "repo-1", "point-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := getCatalogPoint(member, "repo-1", "point-1"); !errors.Is(err, ErrBackupNotFound) {
				t.Fatalf("member unexpectedly has catalog point: %v", err)
			}
			cmd := protocol.Command{OperationID: "peer-restore-op", Kind: kind}
			run := protocol.BackupRun{OperationID: cmd.OperationID, Kind: kind, PoolID: "pool-1", HostID: member.State.HostID,
				RepositoryID: "repo-1", RecoveryPointID: "point-1", ManifestSHA256: point.ManifestSHA256,
				Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
			switch kind {
			case protocol.BackupKindRestoreNew:
				cmd.Restore = &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
					NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "Restored", StorageID: "storage-1", HostID: member.State.HostID}
				run.NewVMID = cmd.Restore.NewVMID
			case protocol.BackupKindTestRestore:
				cmd.TestRestore = &protocol.TestRestoreRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
					NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "Test", StorageID: "storage-1", HostID: member.State.HostID}
				run.NewVMID = cmd.TestRestore.NewVMID
			case protocol.BackupKindRestoreReplace:
				cmd.RestoreReplace = &protocol.RestoreReplaceRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
					OriginalVMID: "11111111-1111-4111-8111-111111111111", StorageID: "storage-1", HostID: member.State.HostID, ConfirmReplace: true}
				run.VMID = cmd.RestoreReplace.OriginalVMID
			}
			run.IntentSHA256, err = backupIntentSHA256(cmd)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReservePoolBackupRun(master, run); err != nil {
				t.Fatal(err)
			}
			queue := make(chan BackupRequest)
			peerBackupRequests.Store(member, queue)
			defer peerBackupRequests.Delete(member)
			go func() {
				for i := 0; i < 3; i++ {
					request := <-queue
					request.Reply <- acceptBackupRequest(context.Background(), member, request)
				}
			}()
			work, err := peerBackupWorkForCommand(master, cmd, run)
			if err != nil {
				t.Fatal(err)
			}
			completion := callPeerBackup(context.Background(), work)
			if completion.err != nil || completion.reply.Result.Status != "pending" ||
				completion.reply.CatalogManifestSHA256 != point.ManifestSHA256 {
				t.Fatalf("clean member peer admission: %+v", completion)
			}
			stored, err := loadPoolBackupRun(member, cmd.OperationID)
			if err != nil || stored.ManifestSHA256 != point.ManifestSHA256 {
				t.Fatalf("member retained run digest: %+v err=%v", stored, err)
			}
			if _, _, err := validatedCatalogBackupManifest(context.Background(), member, protocol.BackupRepository{
				ID: "repo-1", PoolID: "pool-1", Kind: protocol.BackupRepoKindNFS,
				Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
				Identity: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export/backups", FilesystemID: "test-nfs"},
			}, "point-1", stored.ManifestSHA256); err != nil {
				t.Fatalf("member restart manifest binding: %v", err)
			}
			changedPoint := point
			changedPoint.ManifestSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			body, err := json.Marshal(poolBackupPeerRequest{Command: cmd, Repository: work.repo, CatalogPoint: &changedPoint})
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
				work.member.PeerOrigin+poolBackupPeerPath+"/restore", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			response, err := work.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("changed peer catalog digest status = %d", response.StatusCode)
			}
			pinKind := protocol.BackupPinKindNew
			if kind == protocol.BackupKindTestRestore {
				pinKind = protocol.BackupPinKindTest
			} else if kind == protocol.BackupKindRestoreReplace {
				pinKind = protocol.BackupPinKindReplace
			}
			if err := putBackupRestorePin(master, protocol.BackupRestorePin{
				RecoveryPointID: "point-1", OperationID: cmd.OperationID, Kind: pinKind, CreatedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(poolBackupCatalogPath(master), "point-1.json")); err != nil {
				t.Fatal(err)
			}
			if _, err := peerBackupWorkForCommand(master, cmd, run); !errors.Is(err, ErrBackupConflict) {
				t.Fatalf("missing master catalog must refuse exact replay: %v", err)
			}
			if pins, err := ActiveBackupRestorePins(master); err != nil || !pins["point-1"] {
				t.Fatalf("missing catalog released restore pin: %+v err=%v", pins, err)
			}
		})
	}
}

func TestRestorePeerRefusesOlderMemberWithoutCatalogRoute(t *testing.T) {
	var postedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postedPath = r.URL.Path
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	point := protocol.BackupRecoveryPoint{ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	work := pendingPeerBackup{run: protocol.BackupRun{OperationID: "restore-1"},
		command: protocol.Command{Kind: protocol.BackupKindRestoreNew}, point: &point,
		member: protocol.PoolMember{PeerOrigin: server.URL}, client: server.Client()}
	completion := callPeerBackup(context.Background(), work)
	var permanent permanentPeerBackupError
	if !errors.As(completion.err, &permanent) || postedPath != poolBackupPeerPath+"/restore" {
		t.Fatalf("older member must refuse catalog-bound restore without native work: path=%q result=%+v", postedPath, completion)
	}
}

func TestMemberRestoreNeedsMasterVMRecord(t *testing.T) {
	s := seedBackupMaster(t)
	run := protocol.BackupRun{Kind: protocol.BackupKindRestoreNew, PoolID: "pool-1", HostID: "member-1",
		NewVMID: "11111111-1111-4111-8111-111111111111"}
	if err := verifyMemberRestoreOwnerRecord(s, run); err == nil {
		t.Fatal("member success without a master VM record must remain uncertain")
	}
	vm := *poolVMTestSpec(run.NewVMID, run.HostID)
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: run.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "defined", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := verifyMemberRestoreOwnerRecord(s, run); err != nil {
		t.Fatalf("matching durable master record refused: %v", err)
	}
}

func TestMemberCleanupPeerBindsExactReceipt(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	testRun := protocol.BackupRun{OperationID: "test-run", Kind: protocol.BackupKindTestRestore, PoolID: "pool-1", HostID: member.State.HostID,
		RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: "11111111-1111-4111-8111-111111111111",
		Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(master, testRun); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "cleanup-op", Kind: "backup-test-cleanup", TestRestoreClean: &protocol.TestRestoreCleanupRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: testRun.NewVMID}}
	work, remote, err := memberCleanupWork(master, cmd)
	if err != nil || !remote || work.repo.MountPath != "" || work.repo.Identity == nil {
		t.Fatalf("member cleanup work: %+v remote=%v err=%v", work, remote, err)
	}
	receipt := protocol.BackupMetadataReceipt{OperationID: cmd.OperationID, Kind: cmd.Kind, IntentSHA256: work.intent,
		Status: "succeeded", ObservedAt: time.Now().UTC()}
	if err := savePoolBackupMetadataReceipt(member, receipt); err != nil {
		t.Fatal(err)
	}
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	go func() {
		for i := 0; i < 2; i++ {
			request := <-queue
			if request.Command == nil {
				request.Reply <- BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID, Status: "pending"}}
			} else {
				request.Reply <- BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded"}}
			}
		}
	}()
	result, err := waitPeerCleanup(context.Background(), work)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("cleanup receipt round trip: %+v err=%v", result, err)
	}
}

func TestCleanupReceiptExactRetryAndChangedIntent(t *testing.T) {
	s := seedBackupMaster(t)
	cmd := protocol.Command{OperationID: "cleanup-op", Kind: "backup-test-cleanup", TestRestoreClean: &protocol.TestRestoreCleanupRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: "11111111-1111-4111-8111-111111111111"}}
	intent, err := backupMetadataIntent(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupMetadataReceipt(s, protocol.BackupMetadataReceipt{
		OperationID: cmd.OperationID, Kind: cmd.Kind, IntentSHA256: intent, Status: "succeeded", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	response, done := terminalBackupMetadataResponse(s, BackupRequest{Command: &cmd})
	if !done || response.Result == nil || response.Result.Status != "succeeded" {
		t.Fatalf("exact retry lost terminal receipt: %+v done=%v", response, done)
	}
	changed := cmd
	changed.TestRestoreClean = &protocol.TestRestoreCleanupRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-2", TestVMID: cmd.TestRestoreClean.TestVMID}
	response, done = terminalBackupMetadataResponse(s, BackupRequest{Command: &changed})
	if !done || !errors.Is(response.Err, ErrBackupConflict) {
		t.Fatalf("changed intent reused operation: %+v done=%v", response, done)
	}
}

func TestEvaluateBackupScheduleTickMissesWithoutCatchUpStorm(t *testing.T) {
	policy := testPolicy()
	policy.LastDispatchAt = time.Date(2026, 9, 20, 2, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)
	existing := map[string]protocol.BackupRun{}
	pending := map[string]bool{}
	tick, updated, err := EvaluateBackupScheduleTick(policy, now, existing, pending)
	if err != nil {
		t.Fatal(err)
	}
	if len(tick.Dispatch) != 1 {
		t.Fatalf("want one dispatch, got %d", len(tick.Dispatch))
	}
	if len(tick.Missed) == 0 || len(tick.Missed) > protocol.BackupMissedCatchUpCap {
		t.Fatalf("missed count out of bounds: %d", len(tick.Missed))
	}
	if updated.NextDispatchAt.Before(now) || !updated.NextDispatchAt.After(now) && updated.NextDispatchAt.Equal(now) {
		// next must be in the future after 27th 02:00
	}
	wantNext := time.Date(2026, 9, 28, 2, 0, 0, 0, time.UTC)
	if !updated.NextDispatchAt.Equal(wantNext) {
		t.Fatalf("next=%s want=%s", updated.NextDispatchAt, wantNext)
	}
}

func TestEvaluateBackupScheduleTickLongDowntimeUsesLatestSlot(t *testing.T) {
	policy := testPolicy()
	policy.LastDispatchAt = time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)
	tick, updated, err := EvaluateBackupScheduleTick(policy, now, map[string]protocol.BackupRun{}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC)
	if len(tick.Dispatch) != 1 || !tick.Dispatch[0].OccurrenceAt.Equal(want) || !updated.LastDispatchAt.Equal(want) {
		t.Fatalf("long downtime must dispatch latest occurrence: %+v %+v", tick, updated)
	}
}

func TestApplyBackupScheduleTickHonorsHostLimitAndVMOverride(t *testing.T) {
	s := seedBackupMaster(t)
	vms, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"vm-1", "vm-2"} {
		vms.VMs[id] = poolVMRecord{PoolID: "pool-1", VMID: id, HostID: "host-a", State: "stopped", Epoch: 1, Spec: *poolVMTestSpec(id, "host-a")}
	}
	if err := savePoolVMs(s, vms); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.VMIDs = []string{"vm-1", "vm-2"}
	policy.Limits.MaxConcurrentPerHost = 1
	policy.Overrides = []protocol.BackupPolicyVMOverride{{VMID: "vm-1", CaptureMode: protocol.BackupCaptureOnline, Retention: &protocol.BackupRetention{KeepLatest: 2}}}
	policy.LastDispatchAt = time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC)
	if err := savePoolBackupPolicies(s, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{policy.ID: policy}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBackupScheduleTick(s, time.Date(2026, 9, 27, 2, 5, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %v, %v", runs, err)
	}
	statuses := map[string]string{}
	for _, run := range runs {
		statuses[run.VMID] = run.Status
		if run.VMID == "vm-1" && (run.CaptureMode != protocol.BackupCaptureOnline || run.Retention == nil || run.Retention.KeepLatest != 2) {
			t.Fatalf("VM override was not captured: %+v", run)
		}
	}
	if statuses["vm-1"] != "pending" || statuses["vm-2"] != "skipped_overlap" {
		t.Fatalf("host limit was not enforced: %v", statuses)
	}
}

func TestEvaluateBackupScheduleTickRefusesDuplicateOccurrence(t *testing.T) {
	policy := testPolicy()
	slot := time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC)
	policy.LastDispatchAt = slot.Add(-24 * time.Hour)
	occ := protocol.BackupOccurrenceID(policy.ScheduleID, "vm-1", slot)
	existing := map[string]protocol.BackupRun{
		occ: {OperationID: occ, OccurrenceID: occ, Status: "pending"},
	}
	tick, _, err := EvaluateBackupScheduleTick(policy, slot.Add(time.Hour), existing, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tick.Dispatch) != 0 {
		t.Fatal("duplicate occurrence must not dispatch again")
	}
	if len(tick.Skipped) != 1 {
		t.Fatalf("want skipped duplicate, got %d", len(tick.Skipped))
	}
}

func TestEvaluateBackupScheduleTickControllerAbsentLogic(t *testing.T) {
	// Scheduler advances with no Controller connection: only Core policy state matters.
	policy := testPolicy()
	policy.LastDispatchAt = time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 27, 2, 5, 0, 0, time.UTC)
	tick, updated, err := EvaluateBackupScheduleTick(policy, now, map[string]protocol.BackupRun{}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tick.Dispatch) != 1 || tick.Dispatch[0].Kind != protocol.BackupKindScheduled {
		t.Fatalf("Controller-absent tick must dispatch scheduled run: %+v", tick.Dispatch)
	}
	if tick.Dispatch[0].OccurrenceID == "" || tick.Dispatch[0].PolicyRevision != 1 {
		t.Fatal("occurrence identity and policy revision required before dispatch")
	}
	if updated.Paused {
		t.Fatal("tick must not pause the policy")
	}
}

func TestEvaluateBackupScheduleTickOverlapSkips(t *testing.T) {
	policy := testPolicy()
	policy.LastDispatchAt = time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 27, 2, 5, 0, 0, time.UTC)
	tick, _, err := EvaluateBackupScheduleTick(policy, now, map[string]protocol.BackupRun{}, map[string]bool{"vm-1": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tick.Dispatch) != 0 || len(tick.Skipped) != 1 || tick.Skipped[0].Status != "skipped_overlap" {
		t.Fatalf("overlap must skip: dispatch=%d skipped=%v", len(tick.Dispatch), tick.Skipped)
	}
}

func TestRetentionPruneLeavesProtected(t *testing.T) {
	now := time.Now().UTC()
	points := []protocol.BackupRecoveryPoint{
		{ID: "a", SourceVMID: "vm-1", CapturedAt: now.Add(-time.Hour), Protected: true},
		{ID: "b", SourceVMID: "vm-1", CapturedAt: now.Add(-2 * time.Hour)},
		{ID: "c", SourceVMID: "vm-1", CapturedAt: now.Add(-3 * time.Hour)},
	}
	_, prune := protocol.RetentionEligiblePrune(points, "vm-1", protocol.BackupRetention{KeepLatest: 1}, now, nil)
	for _, p := range prune {
		if p.ID == "a" {
			t.Fatal("protected point must not prune")
		}
	}
}
