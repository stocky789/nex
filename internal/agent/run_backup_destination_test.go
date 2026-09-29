// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestBackupDestinationCapabilities(t *testing.T) {
	for _, kind := range []string{protocol.BackupRepoKindNFS, protocol.BackupRepoKindSMB3, protocol.BackupRepoKindS3, protocol.BackupRepoKindSFTP} {
		if !backupDestinationSupported(kind) {
			t.Fatalf("%s backup destination is unavailable", kind)
		}
	}
	if backupDestinationSupported("ftp") {
		t.Fatal("unsupported FTP backup destination advertised")
	}
}

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
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: time.Second, ProbeTimeout: 20 * time.Millisecond})
	}()
	for s.State.Journal[cmd.OperationID].Status == "pending" {
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
