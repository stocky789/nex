// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// A test restore that failed before Core saved its creator journal leaves no VM to
// delete. Cleanup must settle through the real engine and release the pin, not
// retry forever on "creator journal is unavailable".
func TestCleanupOfFailedTestRestoreWithoutJournalSettles(t *testing.T) {
	s := seedBackupMaster(t)
	if err := os.MkdirAll(filepath.Join(s.dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	// libvirt answers with no domains, which is what proves the test VM absent.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	run := protocol.BackupRun{OperationID: "test-restore-op", Kind: protocol.BackupKindTestRestore, PoolID: "pool-1",
		HostID: s.State.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: "22222222-2222-4222-8222-222222222222",
		Stage: "failed", Status: "failed", Error: "copy artifacts: repository unreadable", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{RecoveryPointID: "point-1", OperationID: run.OperationID,
		Kind: protocol.BackupPinKindTest, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareTestRestoreCleanup(s, protocol.TestRestoreCleanupRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: run.NewVMID})
	if err != nil {
		t.Fatal(err)
	}
	if err := executeTestRestoreCleanupNative(context.Background(), s.dir, plan); err != nil {
		t.Fatalf("cleanup did not settle: %v", err)
	}
	if err := finishTestRestoreCleanup(s, plan); err != nil {
		t.Fatal(err)
	}
	if pins, err := ActiveBackupRestorePins(s); err != nil || pins["point-1"] {
		t.Fatalf("pin remained: %v %v", pins, err)
	}
}

// A member's staging directory already exists when it asks the master for placement, so
// only the master's definite refusal proves nothing was placed: the run must settle
// failed and leave no staging directory or pin, never needs_attention.
func TestTestRestoreDefiniteMasterRefusalFailsAndRemovesStaging(t *testing.T) {
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
	previous := requestTestRestorePlacement
	t.Cleanup(func() { requestTestRestorePlacement = previous })
	requestTestRestorePlacement = func(_ context.Context, store *Store, spec protocol.VMCreateSpec, _ string) error {
		// A leftover owner row makes the no-effects inference ambiguous; the refusal alone must decide.
		if err := savePoolVMs(store, poolVMState{VMs: map[string]poolVMRecord{spec.ID: {PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID}}}); err != nil {
			t.Fatal(err)
		}
		return poolVMPlacementRefusal("pool master refused VM placement: no capacity")
	}
	cmd := protocol.Command{OperationID: "test-restore-refused-staging", Kind: protocol.BackupKindTestRestore,
		TestRestore: &protocol.TestRestoreRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "test-vm", StorageID: "storage-1", HostID: s.State.HostID}}
	result, err := executeTestRestore(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "failed" {
		t.Fatalf("definite master refusal must settle failed: %+v %v", result, err)
	}
	if _, statErr := os.Lstat(restoreStagingDir(s, cmd.OperationID)); !os.IsNotExist(statErr) {
		t.Fatalf("refused test restore left its staging directory: %v", statErr)
	}
	if pins, pinErr := ActiveBackupRestorePins(s); pinErr != nil || pins["point-1"] {
		t.Fatalf("restore pin remained after the definite refusal: %+v %v", pins, pinErr)
	}
}
