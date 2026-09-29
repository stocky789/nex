// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestRunTestVerificationWaitsForNativeProofWithoutBlockingReports(t *testing.T) {
	s := seedBackupMaster(t)
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	point := protocol.BackupRecoveryPoint{
		ID: "point-1", RepositoryID: "repo-1", PoolID: s.State.PoolID, SourceVMID: "vm-1",
		CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if err := savePoolBackupCatalogEntry(s, point); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRun(s, protocol.BackupRun{
		OperationID: "test-1", Kind: protocol.BackupKindTestRestore, PoolID: s.State.PoolID,
		RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: vm.ID, HostID: vm.HostID,
		Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, CreateOperationID: "test-1", State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	var reports atomic.Int32
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reports.Add(1)
		json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	original := verifyTestRestoredVM
	verifyTestRestoredVM = func(_ *vmhost.ManagedEngine, ctx context.Context, operationID string, spec protocol.VMCreateSpec, passed bool) error {
		if operationID != "test-1" || spec.ID != vm.ID || !passed {
			t.Errorf("wrong native target: %s %s passed=%t", operationID, spec.ID, passed)
		}
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { verifyTestRestoredVM = original })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	requests := make(chan BackupRequest)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: time.Second, ProbeTimeout: 20 * time.Millisecond, BackupRequests: requests})
	}()
	command := protocol.Command{OperationID: "verify-1", Kind: "backup-test-verify", TestRestoreVerify: &protocol.TestRestoreVerifyRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: vm.ID, Result: protocol.BackupTestResultPassed,
	}}
	reply := make(chan BackupResponse, 1)
	select {
	case requests <- BackupRequest{Command: &command, Reply: reply}:
	case err := <-done:
		t.Fatalf("Run stopped before request: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("native proof never started")
	}
	if receipt, err := loadPoolBackupMetadataReceipt(s, command.OperationID); err != nil || receipt.Status != "pending" {
		t.Fatalf("receipt before proof: %+v %v", receipt, err)
	}
	for reports.Load() < 2 {
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Run did not keep reporting during native proof")
		}
	}
	if point, err := getCatalogPoint(s, "repo-1", "point-1"); err != nil || point.LastTestResult != "" {
		t.Fatalf("catalog changed before proof: %+v %v", point, err)
	}
	close(release)
	select {
	case result := <-reply:
		if result.Err != nil || result.Result == nil || result.Result.Status != "succeeded" {
			t.Fatalf("verification result: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("verification did not complete")
	}
	if point, err := getCatalogPoint(s, "repo-1", "point-1"); err != nil || point.LastTestResult != protocol.BackupTestResultPassed {
		t.Fatalf("catalog after proof: %+v %v", point, err)
	}
	verifyTestRestoredVM = func(_ *vmhost.ManagedEngine, _ context.Context, _ string, _ protocol.VMCreateSpec, passed bool) error {
		if passed {
			t.Error("failed result requested full recovery proof")
		}
		return ErrBackupConflict
	}
	command.OperationID = "verify-2"
	failed := *command.TestRestoreVerify
	failed.Result = protocol.BackupTestResultFailed
	command.TestRestoreVerify = &failed
	reply = make(chan BackupResponse, 1)
	select {
	case requests <- BackupRequest{Command: &command, Reply: reply}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case result := <-reply:
		if result.Err != nil || result.Result == nil || result.Result.Status != "failed" {
			t.Fatalf("denied proof result: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("denied proof did not settle")
	}
	if receipt, err := loadPoolBackupMetadataReceipt(s, command.OperationID); err != nil || receipt.Status != "failed" {
		t.Fatalf("denied proof receipt: %+v %v", receipt, err)
	}
	if point, err := getCatalogPoint(s, "repo-1", "point-1"); err != nil || point.LastTestResult != protocol.BackupTestResultPassed {
		t.Fatalf("denied proof changed catalog: %+v %v", point, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestControllerCatalogCommandJoinsLocalRunAndSettles(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "rebuild-joined", Kind: "backup-catalog-rebuild",
		CatalogImport: &protocol.CatalogImportRequest{RepositoryID: "repo-1"}}
	var deliver atomic.Bool
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := protocol.ReportResponse{}
		if deliver.Load() {
			response.Commands = []protocol.Command{cmd}
		}
		json.NewEncoder(w).Encode(response)
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	// Hold the locally started rebuild until the Controller has issued the same command,
	// then end it without an outcome, so its receipt stays pending.
	release := make(chan struct{})
	original := verifyBackupRepositoryRead
	verifyBackupRepositoryRead = func(ctx context.Context, repo protocol.BackupRepository) error {
		select {
		case <-release:
			return errors.New("repository offline")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { verifyBackupRepositoryRead = original })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	requests := make(chan BackupRequest)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: time.Hour, ProbeTimeout: 20 * time.Millisecond, BackupRequests: requests})
	}()
	reply := make(chan BackupResponse, 1)
	select {
	case requests <- BackupRequest{Command: &cmd, Reply: reply}:
	case err := <-done:
		t.Fatalf("Run stopped before request: %v", err)
	}
	// The scan is still held, so a reply proves it runs off the Run loop.
	select {
	case local := <-reply:
		if local.Result == nil || local.Result.Status != "pending" {
			t.Fatalf("local rebuild did not start: %+v", local)
		}
	case <-ctx.Done():
		t.Fatal("local rebuild reply waited for its repository scan")
	}
	deliver.Store(true)
	for {
		raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
		var observed State
		if err == nil && json.Unmarshal(raw, &observed) == nil && observed.Journal[cmd.OperationID].Command != nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run stopped before the Controller command: %v", err)
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Controller command never arrived")
		}
	}
	// The Run loop saved the command and then dispatched it, joining the active
	// rebuild, before it can answer another request.
	select {
	case requests <- BackupRequest{Reply: reply}:
		<-reply
	case err := <-done:
		t.Fatalf("Run stopped before joining the rebuild: %v", err)
	}
	close(release)
	for {
		raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
		var observed State
		if err == nil && json.Unmarshal(raw, &observed) == nil && observed.Journal[cmd.OperationID].Status == "needs_attention" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run stopped before settlement: %v", err)
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Controller catalog command stayed pending after the joined rebuild finished")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run stopped with error: %v", err)
	}
}

func TestAttentionJournalAdoptsSettledMemberRun(t *testing.T) {
	s := seedBackupMaster(t)
	vm := *poolVMTestSpec("vm-1", "member-1")
	cmd := protocol.Command{OperationID: "member-backup", Kind: protocol.BackupKindBackupNow, VM: &vm,
		Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1", Consistency: protocol.BackupCaptureStopped}}
	// Another master settled the run; this host's journal still needs attention.
	if err := savePoolBackupRun(s, protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: s.State.PoolID,
		HostID: "member-1", VMID: vm.ID, RepositoryID: "repo-1", RecoveryPointID: cmd.OperationID,
		Stage: "published", Status: "succeeded", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	s.State.Journal[cmd.OperationID] = JournalEntry{Status: "needs_attention", Command: &cmd, ObservedAt: time.Now().UTC()}
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(protocol.ReportResponse{})
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
		done <- Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: time.Hour, ProbeTimeout: 20 * time.Millisecond})
	}()
	for {
		raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
		var observed State
		if err == nil && json.Unmarshal(raw, &observed) == nil && observed.Journal[cmd.OperationID].Status == "succeeded" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run stopped before adoption: %v", err)
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("journal stayed needing attention after the pool run settled")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run stopped with error: %v", err)
	}
}
