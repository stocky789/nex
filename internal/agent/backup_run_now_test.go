// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestPolicyRunNowResumesPublishedRetentionAfterWriteRecovery(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	setTestPromotionBackupMount(t, mount)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("vm-1", s.State.HostID)
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: s.State.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.Paused = true
	if err := savePoolBackupPolicies(s, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{policy.ID: policy}}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "retention-retry", Kind: protocol.BackupKindPolicyRunNow,
		BackupPolicy: &protocol.BackupPolicy{ID: policy.ID}, VM: &protocol.VMCreateSpec{ID: vm.ID}}
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
		t.Fatalf("run-now reservation: %+v", response)
	}
	if err := completeBackupMetadataRequest(s, cmd.OperationID, cmd, response, nil); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", s.State.PoolID, cmd.OperationID)
	manifest, digest, err := vmhost.ReadBackupManifest(mount + "/" + cmd.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := loadPoolBackupRun(s, cmd.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Stage = "succeeded", "published"
	point := protocol.BackupRecoveryPoint{ID: run.RecoveryPointID, RepositoryID: run.RepositoryID,
		PoolID: run.PoolID, SourceVMID: run.VMID, CapturedAt: manifest.CapturedAt,
		Consistency: manifest.Consistency, ManifestSHA256: digest}
	previousWrite := verifyBackupRepository
	verifyBackupRepository = func(context.Context, protocol.BackupRepository) error { return errors.New("repository refuses writes") }
	t.Cleanup(func() { verifyBackupRepository = previousWrite })
	if err := SettlePoolBackupRun(s, run, &point); err == nil {
		t.Fatal("retention denial settled the run")
	}
	canceled, cancelSettlement := context.WithCancel(context.Background())
	cancelSettlement()
	if err := settlePoolBackupRun(canceled, s, run, &point); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled retry entered settlement: %v", err)
	}
	if err := completeBackgroundManagedCommand(s, managedCompletion{result: protocol.Result{
		OperationID: cmd.OperationID, Status: "needs_attention", ObservedAt: time.Now().UTC(),
	}, background: true}, nil); err != nil {
		t.Fatal(err)
	}
	if replay := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd}); replay.Err != nil || replay.Result == nil || replay.Result.Status != "needs_attention" {
		t.Fatalf("denied prune must remain unsettled: %+v", replay)
	}
	verifyBackupRepository = previousWrite
	entry := s.State.Journal[cmd.OperationID]
	if !publishedLocalBackupNeedsReconcile(s, entry) {
		t.Fatal("published policy run was not selected for Core-owned retry")
	}
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: 100 * time.Millisecond, ProbeTimeout: 20 * time.Millisecond})
	}()
	for {
		run, err := loadPoolBackupRun(s, cmd.OperationID)
		if err == nil && run.Status == "succeeded" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Core Run stopped before retry settlement: %v", err)
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Core Run did not reconcile the published policy backup")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Core Run stopped with error: %v", err)
	}
	replay := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if replay.Err != nil || replay.Result == nil || replay.Result.Status != "succeeded" {
		t.Fatalf("settled policy run did not survive exact replay: %+v", replay)
	}
	if run, err := loadPoolBackupRun(s, cmd.OperationID); err != nil || run.Status != "succeeded" {
		t.Fatalf("retained run: %+v, %v", run, err)
	}
	if receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID); err != nil || receipt.Status != "succeeded" {
		t.Fatalf("retained receipt: %+v, %v", receipt, err)
	}
	if points, err := loadPoolBackupCatalog(s); err != nil || len(points) != 1 || points[0].ID != cmd.OperationID {
		t.Fatalf("catalog changed on exact retry: %+v, %v", points, err)
	}
	runUntil := func(operationID string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: 100 * time.Millisecond, ProbeTimeout: 20 * time.Millisecond})
		}()
		for {
			raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
			var observed State
			if err == nil && json.Unmarshal(raw, &observed) == nil && observed.Journal[operationID].Status == "succeeded" {
				break
			}
			select {
			case err := <-done:
				t.Fatalf("Core Run stopped before journal recovery: %v", err)
			case <-time.After(20 * time.Millisecond):
			case <-ctx.Done():
				<-done
				t.Fatalf("Core Run did not recover the published backup journal: %+v", s.State.Journal[operationID])
			}
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Core Run stopped with error: %v", err)
		}
	}
	entry = s.State.Journal[cmd.OperationID]
	entry.Status = "needs_attention" // Crash after durable run/receipt, before journal completion.
	s.State.Journal[cmd.OperationID] = entry
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if !publishedLocalBackupNeedsReconcile(s, entry) {
		t.Fatal("succeeded run with unfinished journal was not selected")
	}
	runUntil(cmd.OperationID)

	scheduled := protocol.BackupRun{OperationID: "scheduled-retention-retry", Kind: protocol.BackupKindScheduled,
		PoolID: s.State.PoolID, VMID: vm.ID, RepositoryID: policy.RepositoryID, PolicyID: policy.ID,
		ScheduleID: policy.ScheduleID, Retention: &policy.Retention, CaptureMode: policy.CaptureMode,
		Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	scheduled, scheduledCmd, err := prepareScheduledBackupRun(s, scheduled)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReservePoolBackupRun(s, scheduled); err != nil {
		t.Fatal(err)
	}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{scheduledCmd}}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", s.State.PoolID, scheduled.OperationID)
	manifest, digest, err = vmhost.ReadBackupManifest(mount + "/" + scheduled.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	scheduled.Status, scheduled.Stage = "succeeded", "published"
	point = protocol.BackupRecoveryPoint{ID: scheduled.RecoveryPointID, RepositoryID: scheduled.RepositoryID,
		PoolID: scheduled.PoolID, SourceVMID: scheduled.VMID, CapturedAt: manifest.CapturedAt,
		Consistency: manifest.Consistency, ManifestSHA256: digest}
	verifyBackupRepository = func(context.Context, protocol.BackupRepository) error { return errors.New("repository refuses writes") }
	if err := SettlePoolBackupRun(s, scheduled, &point); err == nil {
		t.Fatal("scheduled retention denial settled the run")
	}
	if err := completeBackgroundManagedCommand(s, managedCompletion{result: protocol.Result{
		OperationID: scheduled.OperationID, Status: "needs_attention", ObservedAt: time.Now().UTC(),
	}, background: true}, nil); err != nil {
		t.Fatal(err)
	}
	verifyBackupRepository = previousWrite
	if !publishedLocalBackupNeedsReconcile(s, s.State.Journal[scheduled.OperationID]) {
		t.Fatal("scheduled published run was not selected")
	}
	runUntil(scheduled.OperationID)
	if run, err := loadPoolBackupRun(s, scheduled.OperationID); err != nil || run.Status != "succeeded" || run.Kind != protocol.BackupKindScheduled {
		t.Fatalf("scheduled run changed identity or remained unsettled: %+v, %v", run, err)
	}
}

func TestPolicyRunNowAcknowledgesReservedRun(t *testing.T) {
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("vm-1", s.State.HostID)
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: s.State.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	if err := savePoolBackupPolicies(s, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{policy.ID: policy}}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "run-now-ack", Kind: protocol.BackupKindPolicyRunNow,
		BackupPolicy: &protocol.BackupPolicy{ID: policy.ID}, VM: &protocol.VMCreateSpec{ID: vm.ID}}
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if response.Err != nil || response.Result == nil || response.Result.Status != "pending" || response.Result.ObservedAt.IsZero() {
		t.Fatalf("reserved run was not acknowledged: %+v", response)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil || receipt.Status != "pending" || receipt.ObservedAt.IsZero() {
		t.Fatalf("run-now receipt: %+v, %v", receipt, err)
	}
	run, err := loadPoolBackupRun(s, cmd.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Stage = "succeeded", "published"
	point := protocol.BackupRecoveryPoint{ID: run.RecoveryPointID, RepositoryID: run.RepositoryID,
		PoolID: run.PoolID, SourceVMID: run.VMID, CapturedAt: time.Now().UTC(),
		Consistency: protocol.BackupCaptureStopped, ManifestSHA256: strings.Repeat("a", 64)}
	if err := SettlePoolBackupRun(s, run, &point); err != nil {
		t.Fatal(err)
	}
	receipt, err = loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil || receipt.Status != "succeeded" {
		t.Fatalf("settled receipt: %+v, %v", receipt, err)
	}

	// A crash between run settlement and receipt update leaves the old receipt pending.
	receipt.Status, receipt.ObservedAt = "pending", response.Result.ObservedAt
	if err := savePoolBackupMetadataReceipt(s, receipt); err != nil {
		t.Fatal(err)
	}
	replay := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if replay.Err != nil || replay.Result == nil || replay.Result.Status != "succeeded" {
		t.Fatalf("exact replay did not reconcile terminal run: %+v", replay)
	}
	receipt, err = loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil || receipt.Status != "succeeded" {
		t.Fatalf("reconciled receipt: %+v, %v", receipt, err)
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil || len(runs) != 1 {
		t.Fatalf("exact replay duplicated run: %+v, %v", runs, err)
	}
}

func TestControllerPolicyRunNowReusesJournalIdentity(t *testing.T) {
	s := seedBackupMaster(t)
	vm := *poolVMTestSpec("vm-1", s.State.HostID)
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: s.State.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	if err := savePoolBackupPolicies(s, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{policy.ID: policy}}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "controller-run-now", Kind: protocol.BackupKindPolicyRunNow,
		BackupPolicy: &protocol.BackupPolicy{ID: policy.ID, PoolID: s.State.PoolID}, VM: &protocol.VMCreateSpec{ID: vm.ID}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
		t.Fatalf("Controller run-now reservation: %+v", response)
	}
	if err := completeBackupMetadataRequest(s, cmd.OperationID, cmd, response, nil); err != nil {
		t.Fatalf("pending reservation stopped the Core Run loop: %v", err)
	}
	entry := s.State.Journal[cmd.OperationID]
	if entry.Command == nil || entry.Command.Kind != protocol.BackupKindBackupNow || entry.Status != "pending" {
		t.Fatalf("Controller operation did not become executable backup: %+v", entry)
	}
	if nextManagedCommand(s.State.Journal, "") != cmd.OperationID ||
		dispatchCommand(entry, s.State.Journal).Kind != protocol.BackupKindBackupNow {
		t.Fatal("reserved Controller backup is not dispatchable")
	}
	outcomes := policyRunNowOutcomes(s, commandOutcomes(s.State.Journal, nil))
	if len(outcomes) != 1 || outcomes[0].Kind != protocol.BackupKindPolicyRunNow {
		t.Fatalf("Controller outcome lost request kind: %+v", outcomes)
	}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatalf("exact Controller replay rejected: %v", err)
	}
	changed := cmd
	changed.VM = &protocol.VMCreateSpec{ID: "other-vm"}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{changed}}); err == nil {
		t.Fatal("changed Controller replay reused operation ID")
	}
	// A restart after reservation but before journal transition retries the same run.
	entry.Command = &cmd
	s.State.Journal[cmd.OperationID] = entry
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	replay := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if replay.Err != nil || replay.Result == nil || replay.Result.Status != "pending" ||
		s.State.Journal[cmd.OperationID].Command.Kind != protocol.BackupKindBackupNow {
		t.Fatalf("reserved run was not resumed: %+v", replay)
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil || len(runs) != 1 {
		t.Fatalf("Controller reservation count: %+v, %v", runs, err)
	}
	run := runs[0]
	run.Status, run.Stage, run.Error = "failed", "failed", "simulated backup failure"
	if err := SettlePoolBackupRun(s, run, nil); err != nil {
		t.Fatal(err)
	}
	entry = s.State.Journal[cmd.OperationID]
	entry.Status, entry.Error, entry.ObservedAt = run.Status, run.Error, time.Now().UTC()
	s.State.Journal[cmd.OperationID] = entry
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatalf("terminal Controller command replay rejected: %v", err)
	}
	replay = acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if replay.Err != nil || replay.Result == nil || replay.Result.Status != "failed" || replay.Result.Error != run.Error {
		t.Fatalf("terminal exact replay: %+v", replay)
	}
}

func TestControllerPolicyRunNowMemberUsesOneDispatcher(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: master.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("vm-1", member.State.HostID)
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: master.State.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	if err := savePoolBackupPolicies(master, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{policy.ID: policy}}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "member-run-now", Kind: protocol.BackupKindPolicyRunNow,
		BackupPolicy: &protocol.BackupPolicy{ID: policy.ID}, VM: &protocol.VMCreateSpec{ID: vm.ID}}
	if err := applyResponse(master, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	intent, err := backupMetadataIntent(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupMetadataReceipt(master, protocol.BackupMetadataReceipt{
		OperationID: cmd.OperationID, Kind: cmd.Kind, IntentSHA256: intent, Status: "pending", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: master.State.PoolID,
		VMID: vm.ID, RepositoryID: policy.RepositoryID, PolicyID: policy.ID, ScheduleID: policy.ScheduleID,
		PolicyRevision: policy.Revision, RecoveryPointID: cmd.OperationID, CaptureMode: policy.CaptureMode,
		Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	run, _, err = prepareScheduledBackupRun(master, run)
	if err != nil {
		t.Fatal(err)
	}
	if run.HostID != member.State.HostID {
		t.Fatalf("member policy run reserved for %q, want %q", run.HostID, member.State.HostID)
	}
	// An older pending reservation may lack HostID; metadata replay binds it before dispatch.
	run.HostID = ""
	if _, err := ReservePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	if _, ready, err := nextPendingPeerBackup(master); err != nil || ready {
		t.Fatalf("scheduler dispatched before Controller journal recovery: ready=%v err=%v", ready, err)
	}
	response := acceptBackupRequest(context.Background(), master, BackupRequest{Command: &cmd})
	if response.Err != nil || response.Result == nil {
		t.Fatalf("member run-now admission: %+v", response)
	}
	bound, err := loadPoolBackupRun(master, cmd.OperationID)
	if err != nil || bound.HostID != member.State.HostID {
		t.Fatalf("member run-now replay was not bound to member: %+v, %v", bound, err)
	}
	if _, ready, err := nextPendingPeerBackup(master); err != nil || ready {
		t.Fatalf("scheduler duplicated Controller-owned member dispatch: ready=%v err=%v", ready, err)
	}
	vm.HostID = master.State.HostID
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: master.State.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	entry := master.State.Journal[cmd.OperationID]
	entry.Command = &cmd
	master.State.Journal[cmd.OperationID] = entry
	if _, err := ReservePolicyRunNow(master, policy.ID, vm.ID, cmd.OperationID); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("placement change retargeted pending run: %v", err)
	}
	bound, err = loadPoolBackupRun(master, cmd.OperationID)
	if err != nil || bound.HostID != member.State.HostID {
		t.Fatalf("placement change rewrote bound host: %+v, %v", bound, err)
	}
}

func TestControllerPolicyRunNowRunLoopDispatchesAfterReservation(t *testing.T) {
	s := seedBackupMaster(t)
	vm := *poolVMTestSpec("vm-1", s.State.HostID)
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: s.State.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	if err := savePoolBackupPolicies(s, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{policy.ID: policy}}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "run-loop-policy", Kind: protocol.BackupKindPolicyRunNow,
		BackupPolicy: &protocol.BackupPolicy{ID: policy.ID}, VM: &protocol.VMCreateSpec{ID: vm.ID}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: 100 * time.Millisecond, ProbeTimeout: 20 * time.Millisecond})
	}()
	for {
		run, err := loadPoolBackupRun(s, cmd.OperationID)
		if err == nil && run.Status == "failed" {
			break // Missing repository makes execution fail after the worker has dispatched.
		}
		select {
		case err := <-done:
			t.Fatalf("Core Run stopped before backup settlement: %v", err)
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Core Run did not dispatch the reserved backup")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Core Run stopped with error: %v", err)
	}
}
