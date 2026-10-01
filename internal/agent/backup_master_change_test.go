// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// The old master's planned-change command returns before the new master answers, so its
// journal receipt settles from the durable pool membership (the target is now the master),
// never from a transfer record that may be gone or replaced by then.
func TestPlannedMasterChangeReceiptSettlesFromMembership(t *testing.T) {
	s := seedBackupMaster(t)
	// host-a leads the pool. The change to host-b has not happened yet.
	cmd := protocol.Command{OperationID: "op-change", Kind: "pool-transfer",
		Pool: &protocol.PoolCommand{Action: "master-change", MemberHostID: "host-b"}}
	s.State.Journal = map[string]JournalEntry{"op-change": {Command: &cmd, Status: "needs_attention", Error: "not confirmed"}}
	reconcilePoolPromotionJournal(s)
	if entry := s.State.Journal["op-change"]; entry.Status != "needs_attention" {
		t.Fatalf("settled before the master changed: %+v", entry)
	}
	// host-a now leads and holds the finished source record: the state a demoted old master
	// reads after the change.
	cmd.Pool = &protocol.PoolCommand{Action: "master-change", MemberHostID: "host-a"}
	if err := savePoolTransfer(s, &poolTransferRecord{OperationID: "op-change", Action: "promote", Role: "source", TargetHostID: "host-a", Stage: "done", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	reconcilePoolPromotionJournal(s)
	entry := s.State.Journal["op-change"]
	if entry.Status != "succeeded" || entry.Error != "" || entry.Acknowledged {
		t.Fatalf("receipt = %+v", entry)
	}
	// Settled once: a second pass leaves the entry alone.
	reconcilePoolPromotionJournal(s)
	if s.State.Journal["op-change"] != entry {
		t.Fatalf("receipt changed on a second pass")
	}
}

// An earlier unresolved change to the same target must not settle because the target became
// master through some other operation.
func TestMasterChangeReceiptIgnoresAnotherOperationsPromotion(t *testing.T) {
	s := seedBackupMaster(t)
	if err := savePoolTransfer(s, &poolTransferRecord{OperationID: "op-new", Action: "promote", Role: "source", TargetHostID: "host-a", Stage: "done", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "op-old", Kind: "pool-transfer",
		Pool: &protocol.PoolCommand{Action: "master-change", MemberHostID: "host-a"}}
	s.State.Journal = map[string]JournalEntry{"op-old": {Command: &cmd, Status: "needs_attention", Error: "not confirmed"}}
	for range 2 {
		reconcilePoolPromotionJournal(s)
		if entry := s.State.Journal["op-old"]; entry.Status != "needs_attention" || !strings.Contains(entry.Error, "another operation") {
			t.Fatalf("receipt = %+v", entry)
		}
	}
}

// An NFS repository adopted while unreachable is mounted by the next probe once it answers.
func TestProbeRemountsRepositoryAdoptedOffline(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	previous := prepareBackupRepositoryNative
	t.Cleanup(func() { prepareBackupRepositoryNative = previous })
	prepareBackupRepositoryNative = func(_ context.Context, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
		repo.MountPath = mount
		return repo, nil
	}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source:   protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
		Identity: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export/backups", FilesystemID: "test-nfs"}}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	if err := probePoolBackupRepositories(context.Background(), s, time.Second); err != nil {
		t.Fatal(err)
	}
	if got, err := GetPoolBackupRepository(s, repo.ID); err != nil || got.MountPath != mount {
		t.Fatalf("repository after probe = %+v, %v", got, err)
	}
}

// An orphan-cleanup record is part of the signed authority, so the new master still answers
// for an operation the old master ran.
func TestOrphanCleanupRecordTravelsWithAuthority(t *testing.T) {
	master := seedBackupMaster(t)
	record := protocol.BackupOrphanCleanup{
		OperationID: "orph-1", Status: "succeeded", UpdatedAt: time.Now().UTC(),
		Request: protocol.OrphanCleanupRequest{
			RepositoryID: "repo-1", VMIDs: []string{"vm-gone"}, Confirmation: "delete 1 recovery points", RecoveryPoints: 1,
		},
		Points: []protocol.BackupOrphanPointResult{
			{RecoveryPointID: "point-1", VMID: "vm-gone", Status: "deleted"},
		},
	}
	if err := saveOrphanCleanup(master, record); err != nil {
		t.Fatal(err)
	}
	manifest, err := stagePoolBackupAuthorityFromStore(master)
	if err != nil || manifest == nil || manifest.OrphanCleanups != 1 {
		t.Fatalf("manifest = %+v, %v", manifest, err)
	}
	root, _, err := stagePoolBackupAuthorityActivationPages(master, manifest, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadOrphanCleanup(&Store{dir: filepath.Join(master.dir, root)}, "orph-1")
	if err != nil || got.Status != "succeeded" {
		t.Fatalf("cleanup on the new master = %+v, %v", got, err)
	}
}

// A stale pending receipt or a journal entry whose durable record already ended must not pin
// the pool, but fresh pending work still does.
func TestStaleBackupOperationsDoNotBlockMasterChange(t *testing.T) {
	s := seedBackupMaster(t)
	receipt := protocol.BackupMetadataReceipt{OperationID: "rcpt-1", Kind: "backup-test-cleanup", IntentSHA256: strings.Repeat("a", 64),
		Status: "pending", ObservedAt: time.Now().UTC()}
	if err := savePoolBackupMetadataReceipt(s, receipt); err != nil {
		t.Fatal(err)
	}
	if err := unsettledBackupTransferRefusal(s, "promote", "member-1"); err == nil || !strings.Contains(err.Error(), "rcpt-1") {
		t.Fatalf("fresh pending receipt = %v", err)
	}
	receipt.ObservedAt = time.Now().UTC().Add(-13 * time.Hour)
	if err := savePoolBackupMetadataReceipt(s, receipt); err != nil {
		t.Fatal(err)
	}
	if err := unsettledBackupTransferRefusal(s, "promote", "member-1"); err != nil {
		t.Fatalf("stale receipt blocked: %v", err)
	}
	if got, _ := loadPoolBackupMetadataReceipt(s, "rcpt-1"); got.Status != "needs_attention" || got.Error == "" {
		t.Fatalf("stale receipt = %+v", got)
	}
	ended := protocol.BackupMetadataReceipt{OperationID: "rcpt-2", Kind: "backup-catalog-import", IntentSHA256: strings.Repeat("b", 64),
		Status: "succeeded", ObservedAt: time.Now().UTC()}
	if err := savePoolBackupMetadataReceipt(s, ended); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "rcpt-2", Kind: "backup-catalog-import"}
	s.State.Journal = map[string]JournalEntry{"rcpt-2": {Command: &cmd, Status: "needs_attention"}}
	if err := unsettledBackupTransferRefusal(s, "promote", "member-1"); err != nil {
		t.Fatalf("journal entry of an ended operation blocked: %v", err)
	}
}
