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
)

func TestReportRestoreMissingPointFailsBeforeReservation(t *testing.T) {
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "restore-missing-point", Kind: "backup-restore-as-new",
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-missing",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1"}}
	// A reservation that fails without a definite answer stays uncertain.
	vm := *poolVMTestSpec("vm-1", s.State.HostID)
	uncertain := protocol.Command{OperationID: "backup-reserve-uncertain", Kind: protocol.BackupKindBackupNow, VM: &vm,
		Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1", Consistency: protocol.BackupCaptureStopped}}
	previousReserve := reserveBackupRun
	reserveBackupRun = func(*Store, protocol.BackupRun) (protocol.BackupRun, error) {
		return protocol.BackupRun{}, errors.New("run record write outcome unknown")
	}
	t.Cleanup(func() { reserveBackupRun = previousReserve })
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd, uncertain}}); err != nil {
		t.Fatal(err)
	}
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: time.Second, ProbeTimeout: 20 * time.Millisecond})
	}()
	for {
		raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
		var observed State
		if err == nil && json.Unmarshal(raw, &observed) == nil &&
			observed.Journal[cmd.OperationID].Status != "pending" && observed.Journal[uncertain.OperationID].Status != "pending" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Core Run stopped before refusal: %v", err)
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Core Run did not settle missing point")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	entry := s.State.Journal[cmd.OperationID]
	if entry.Status != "failed" || !strings.Contains(entry.Error, ErrBackupNotFound.Error()) {
		t.Fatalf("missing point journal: %+v", entry)
	}
	if entry := s.State.Journal[uncertain.OperationID]; entry.Status != "needs_attention" {
		t.Fatalf("uncertain reservation journal: %+v", entry)
	}
	if _, err := loadPoolBackupRun(s, cmd.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing point created a run: %v", err)
	}
	if pins, err := loadPoolBackupPins(s); err != nil || len(pins.Pins) != 0 {
		t.Fatalf("missing point created a pin: pins=%+v err=%v", pins, err)
	}
	if vms, err := loadPoolVMs(s); err != nil || len(vms.VMs) != 0 {
		t.Fatalf("missing point created a VM: vms=%+v err=%v", vms, err)
	}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil || s.State.Journal[cmd.OperationID].Status != "failed" {
		t.Fatalf("exact replay changed refusal: entry=%+v err=%v", s.State.Journal[cmd.OperationID], err)
	}
}
