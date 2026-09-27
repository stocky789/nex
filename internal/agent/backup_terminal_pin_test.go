// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"nex.local/nex/internal/protocol"
	"testing"
	"time"
)

func TestTerminalRestorePinsReconcileAfterRestart(t *testing.T) {
	s := seedBackupMaster(t)
	now := time.Now().UTC()
	tests := []struct {
		id, kind, pinKind, status, stage string
		clear                            bool
	}{
		{"new-success", protocol.BackupKindRestoreNew, protocol.BackupPinKindNew, "succeeded", "registered", true},
		{"new-failed", protocol.BackupKindRestoreNew, protocol.BackupPinKindNew, "failed", "failed", true},
		{"replace-success", protocol.BackupKindRestoreReplace, protocol.BackupPinKindReplace, "succeeded", "replaced", true},
		{"replace-failed", protocol.BackupKindRestoreReplace, protocol.BackupPinKindReplace, "failed", "failed", true},
		{"test-failed", protocol.BackupKindTestRestore, protocol.BackupPinKindTest, "failed", "failed", true},
		{"test-success", protocol.BackupKindTestRestore, protocol.BackupPinKindTest, "succeeded", "registered", false},
		{"new-attention", protocol.BackupKindRestoreNew, protocol.BackupPinKindNew, "needs_attention", "placement", false},
		{"new-unproven", protocol.BackupKindRestoreNew, protocol.BackupPinKindNew, "succeeded", "placement", false},
	}
	pins := poolBackupPinsState{Pins: map[string]protocol.BackupRestorePin{}}
	for _, tc := range tests {
		pointID := "point-" + tc.id
		run := protocol.BackupRun{OperationID: tc.id, Kind: tc.kind, PoolID: s.State.PoolID,
			HostID: s.State.HostID, RepositoryID: "repo-1", RecoveryPointID: pointID,
			Status: tc.status, Stage: tc.stage, CreatedAt: now, UpdatedAt: now,
		}
		if tc.kind == protocol.BackupKindRestoreReplace {
			run.VMID = "11111111-1111-4111-8111-111111111111"
		} else {
			run.NewVMID = "22222222-2222-4222-8222-222222222222"
		}
		if err := savePoolBackupRun(s, run); err != nil {
			t.Fatal(err)
		}
		pins.Pins[tc.id] = protocol.BackupRestorePin{OperationID: tc.id, RecoveryPointID: pointID,
			Kind: tc.pinKind, CreatedAt: now}
	}
	if err := savePoolBackupPins(s, pins); err != nil {
		t.Fatal(err)
	}
	// The durable terminal run was written, but its pin clear was interrupted.
	restarted := &Store{dir: s.dir, State: copyState(s.State)}
	if err := reconcileTerminalBackupRestorePins(restarted); err != nil {
		t.Fatal(err)
	}
	retained, err := loadPoolBackupPins(restarted)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range tests {
		_, exists := retained.Pins[tc.id]
		if exists == tc.clear {
			t.Fatalf("pin %s after restart: retained=%t", tc.id, exists)
		}
	}
	if err := reconcileTerminalBackupRestorePins(restarted); err != nil {
		t.Fatalf("exact retry changed terminal pin state: %v", err)
	}
}
