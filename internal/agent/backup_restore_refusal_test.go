// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// A member's staging directory already exists when it asks the master for placement, so
// only the master's definite refusal proves nothing was placed: the run must settle
// failed and leave no staging directory, never needs_attention.
func TestRestoreAsNewDefiniteMasterRefusalFailsAndRemovesStaging(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	importTestBackupCatalog(t, s)
	previous := requestBackupRestorePlacement
	t.Cleanup(func() { requestBackupRestorePlacement = previous })
	requestBackupRestorePlacement = func(_ context.Context, store *Store, spec protocol.VMCreateSpec, _ string) error {
		// A leftover owner row makes the no-effects inference ambiguous; the refusal alone must decide.
		if err := savePoolVMs(store, poolVMState{VMs: map[string]poolVMRecord{spec.ID: {PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID}}}); err != nil {
			t.Fatal(err)
		}
		return poolVMPlacementRefusal("pool master refused VM placement: no capacity")
	}
	cmd := protocol.Command{OperationID: "restore-refused-staging", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1", HostID: s.State.HostID}}
	result, err := executeRestoreAsNew(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "failed" {
		t.Fatalf("definite master refusal must settle failed: %+v %v", result, err)
	}
	if _, statErr := os.Lstat(restoreStagingDir(s, cmd.OperationID)); !os.IsNotExist(statErr) {
		t.Fatalf("refused restore left its staging directory: %v", statErr)
	}
	if pins, pinErr := ActiveBackupRestorePins(s); pinErr != nil || pins["point-1"] {
		t.Fatalf("restore pin remained after the definite refusal: %+v %v", pins, pinErr)
	}
}
