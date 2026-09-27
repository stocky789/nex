// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"sync/atomic"
	"testing"
	"time"
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
