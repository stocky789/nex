// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// removesCatalogOnDelete removes the point, then reports a failed delete.
type removesCatalogOnDelete struct {
	backupRepositoryStorage
	catalog string
}

func (b removesCatalogOnDelete) Delete(ctx context.Context, id string) error {
	_ = b.backupRepositoryStorage.Delete(ctx, id)
	_ = os.Remove(b.catalog)
	return errors.New("destination reported an error")
}

// Protection lifted for an orphan delete is not put back on a point that is gone.
func TestOrphanDeleteDoesNotReprotectAPointGone(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "gone-1", "vm-gone")
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "gone-1", Protected: true}); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{}}); err != nil {
		t.Fatal(err)
	}
	repo, err := GetPoolBackupRepository(s, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	real, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		t.Fatal(err)
	}
	backend := removesCatalogOnDelete{real, filepath.Join(poolBackupCatalogPath(s), "gone-1.json")}
	result := protocol.BackupOrphanPointResult{VMID: "vm-gone", RecoveryPointID: "gone-1"}
	err = deleteOrphanedPoint(context.Background(), s, backend, protocol.OrphanCleanupRequest{RepositoryID: "repo-1", IncludeProtected: true}, &result)
	if err == nil || strings.Contains(err.Error(), "protection could not be restored") {
		t.Fatalf("a gone point was re-protected: %v", err)
	}
}

// A needs_attention backup-now run by a member that has left the pool settles instead of
// being re-observed forever and blocking later backups of the VM.
func TestNeedsAttentionBackupOfDepartedMemberSettles(t *testing.T) {
	s := seedBackupMaster(t)
	vm := poolVMTestSpec("vm-1", "member-gone")
	cmd := protocol.Command{OperationID: "departed", Kind: protocol.BackupKindBackupNow, VM: vm, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	run := protocol.BackupRun{OperationID: cmd.OperationID, Kind: protocol.BackupKindBackupNow, PoolID: "pool-1", HostID: "member-gone",
		RepositoryID: "repo-1", VMID: vm.ID, Stage: "peer_verification", Status: "needs_attention", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	result, settled := journalBackupSettlement(s, JournalEntry{Command: &cmd, Status: "needs_attention", Order: 1})
	if !settled || result.Status != "failed" || !strings.Contains(result.Error, "no longer in the pool") {
		t.Fatalf("departed member's backup was not settled: %+v %v", result, settled)
	}
	if got, err := loadPoolBackupRun(s, run.OperationID); err != nil || got.Status != "failed" {
		t.Fatalf("run row = %+v %v", got, err)
	}
}
