// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// orphanSafetyLab is a master with repo-1 holding one orphaned point per given ID and an empty VM inventory.
func orphanSafetyLab(t *testing.T, points ...string) (*Store, string, backupRepositoryStorage) {
	t.Helper()
	master := newTransferLab(t, 0).host("master-1")
	mount := t.TempDir()
	repo, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range points {
		seedPublishedPoint(t, mount, "repo-1", "pool-1", id, "vm-gone-"+id)
	}
	if _, err := importPoolBackupCatalog(context.Background(), master, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{}}); err != nil {
		t.Fatal(err)
	}
	backend, err := openBackupRepositoryStorage(master, repo)
	if err != nil {
		t.Fatal(err)
	}
	return master, mount, backend
}

type failingDeleteBackend struct{ backupRepositoryStorage }

func (failingDeleteBackend) Delete(context.Context, string) error {
	return errors.New("destination unavailable")
}

func TestOrphanPointNotInCatalogIsNotReportedDeleted(t *testing.T) {
	master, mount, backend := orphanSafetyLab(t, "gone-1")
	if err := os.Remove(filepath.Join(poolBackupCatalogPath(master), "gone-1.json")); err != nil {
		t.Fatal(err)
	}
	req := orphanRequest([]string{"vm-gone-gone-1"}, 1, 0, false)
	result := protocol.BackupOrphanPointResult{RecoveryPointID: "gone-1", VMID: "vm-gone-gone-1", Status: "pending"}
	if err := deleteOrphanedPoint(context.Background(), master, backend, req, &result); err != nil || result.Status != "skipped" {
		t.Fatalf("the destination still holds the point: %+v %v", result, err)
	}
	if err := os.RemoveAll(filepath.Join(mount, "gone-1")); err != nil {
		t.Fatal(err)
	}
	result.Status = "pending"
	if err := deleteOrphanedPoint(context.Background(), master, backend, req, &result); err != nil || result.Status != "deleted" {
		t.Fatalf("the destination confirms it is gone: %+v %v", result, err)
	}
}

func TestOrphanCleanupKeepsProtectionWhenDeleteFails(t *testing.T) {
	master, mount, backend := orphanSafetyLab(t, "gone-1")
	if _, err := SetPoolBackupPointProtected(master, protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "gone-1", Protected: true}); err != nil {
		t.Fatal(err)
	}
	req := orphanRequest([]string{"vm-gone-gone-1"}, 1, 1, true)
	result := protocol.BackupOrphanPointResult{RecoveryPointID: "gone-1", VMID: "vm-gone-gone-1", Status: "pending"}
	if err := deleteOrphanedPoint(context.Background(), master, failingDeleteBackend{backend}, req, &result); err == nil {
		t.Fatalf("a failed delete was reported: %+v", result)
	}
	point, err := getCatalogPoint(master, "repo-1", "gone-1")
	if err != nil || !point.Protected {
		t.Fatalf("protection was dropped: %+v %v", point, err)
	}
	if protected, err := backend.Protected(context.Background(), "gone-1"); err != nil || !protected {
		t.Fatalf("destination protection was dropped: %v %v", protected, err)
	}
	if _, err := os.Stat(filepath.Join(mount, "gone-1")); err != nil {
		t.Fatal(err)
	}
}

func TestOrphanCleanupKeepsPinnedProtectedPoint(t *testing.T) {
	master, _, backend := orphanSafetyLab(t, "gone-1")
	if _, err := SetPoolBackupPointProtected(master, protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "gone-1", Protected: true}); err != nil {
		t.Fatal(err)
	}
	if err := putBackupRestorePin(master, protocol.BackupRestorePin{RecoveryPointID: "gone-1", OperationID: "restore-1",
		Kind: protocol.BackupPinKindNew, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	req := orphanRequest([]string{"vm-gone-gone-1"}, 1, 1, true)
	result := protocol.BackupOrphanPointResult{RecoveryPointID: "gone-1", VMID: "vm-gone-gone-1", Status: "pending"}
	if err := deleteOrphanedPoint(context.Background(), master, backend, req, &result); err != nil || result.Status != "skipped" {
		t.Fatalf("%+v %v", result, err)
	}
	if point, err := getCatalogPoint(master, "repo-1", "gone-1"); err != nil || !point.Protected {
		t.Fatalf("protection was dropped: %+v %v", point, err)
	}
}

func TestOrphanCleanupResumeStopsWithoutAuthority(t *testing.T) {
	master, mount, _ := orphanSafetyLab(t, "gone-1")
	req := orphanRequest([]string{"vm-gone-gone-1"}, 1, 0, false)
	if _, err := planOrphanCleanup(master, "clean-1", req); err != nil {
		t.Fatal(err)
	}
	state := peerState(t, master)
	state.Transfer = &poolTransferRecord{Frozen: true}
	if err := savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
	rec, err := cleanupBackupOrphans(context.Background(), master, "clean-1", req)
	if err == nil || rec.Points[0].Status != "pending" {
		t.Fatalf("a frozen pool kept deleting: %+v %v", rec, err)
	}
	if _, err := os.Stat(filepath.Join(mount, "gone-1")); err != nil {
		t.Fatal("the point was deleted:", err)
	}
}

func TestOrphanCleanupSweepsDeleteMarkers(t *testing.T) {
	master, mount, _ := orphanSafetyLab(t, "gone-1")
	req := orphanRequest([]string{"vm-gone-gone-1"}, 1, 0, false)
	rec, err := cleanupBackupOrphans(context.Background(), master, "clean-1", req)
	if err != nil || rec.Status != "succeeded" {
		t.Fatalf("%+v %v", rec, err)
	}
	if left, _ := filepath.Glob(filepath.Join(mount, ".nex-delete*")); len(left) != 0 {
		t.Fatalf("delete markers left behind: %v", left)
	}
}

func TestOrphanGroupsHaveStableOrder(t *testing.T) {
	master, _, _ := orphanSafetyLab(t, "p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8")
	for i := 0; i < 5; i++ {
		query, err := queryBackupOrphans(context.Background(), master, "repo-1")
		if err != nil || len(query.Groups) != 8 {
			t.Fatalf("%+v %v", query, err)
		}
		for j := 1; j < len(query.Groups); j++ {
			if query.Groups[j-1].VMID >= query.Groups[j].VMID {
				t.Fatalf("unsorted groups: %+v", query.Groups)
			}
		}
	}
}

func TestForgetRefusedWhileCleanupHoldsRetention(t *testing.T) {
	master, _, _ := orphanSafetyLab(t)
	poolBackupRetentionMu.Lock()
	defer poolBackupRetentionMu.Unlock()
	err := forgetPoolBackupRepository(context.Background(), master, protocol.ForgetRepositoryRequest{RepositoryID: "repo-1", Confirmation: "Primary"})
	if err == nil || !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("forget ran under a cleanup: %v", err)
	}
	if _, err := GetPoolBackupRepository(master, "repo-1"); err != nil {
		t.Fatal("the repository was forgotten:", err)
	}
}

func TestForgetKeepsRecordWhenDetachFails(t *testing.T) {
	master, _, _ := orphanSafetyLab(t)
	previous := detachBackupRepositoryNative
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error { return errors.New("target is busy") }
	t.Cleanup(func() { detachBackupRepositoryNative = previous })
	req := protocol.ForgetRepositoryRequest{RepositoryID: "repo-1", Confirmation: "Primary"}
	if err := forgetPoolBackupRepository(context.Background(), master, req); err == nil {
		t.Fatal("a failed detach was reported as forgotten")
	}
	if _, err := GetPoolBackupRepository(master, "repo-1"); err != nil {
		t.Fatal("the record went before the mount did:", err)
	}
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error { return nil }
	if err := forgetPoolBackupRepository(context.Background(), master, req); err != nil {
		t.Fatal(err)
	}
	if _, err := GetPoolBackupRepository(master, "repo-1"); !errors.Is(err, ErrBackupRepoNotFound) {
		t.Fatalf("the repeat did not forget it: %v", err)
	}
}

func TestMemberKeepsRepositoryUsedByItsOwnJournal(t *testing.T) {
	previous := detachBackupRepositoryNative
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error { return nil }
	t.Cleanup(func() { detachBackupRepositoryNative = previous })
	member := newTransferLab(t, 1).host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir()}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	member.State.Journal = map[string]JournalEntry{"backup-1": {Status: "pending", Command: &protocol.Command{
		OperationID: "backup-1", Kind: protocol.BackupKindBackupNow, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}}}
	if err := forgetMemberBackupRepository(context.Background(), member, "repo-1", &repo); err == nil {
		t.Fatal("a member dropped a repository its backup is writing to")
	}
	member.State.Journal["backup-1"] = JournalEntry{Status: "succeeded", Command: member.State.Journal["backup-1"].Command}
	if err := forgetMemberBackupRepository(context.Background(), member, "repo-1", &repo); err != nil {
		t.Fatal(err)
	}
}

func TestForgetWorksAfterCredentialKeyLoss(t *testing.T) {
	s := seedBackupMaster(t)
	receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Cloud", Kind: protocol.BackupRepoKindS3,
		CredentialRef: receipt.Receipt, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups"},
		RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)}}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(s.dir, "backup-credential-key")); err != nil {
		t.Fatal(err)
	}
	if err := forgetPoolBackupRepository(context.Background(), s, protocol.ForgetRepositoryRequest{RepositoryID: "repo-1", Confirmation: "Cloud"}); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(s.dir, clusterSecretDirectory, "backup", "*.json")); len(left) != 0 {
		t.Fatalf("the unreadable receipt stayed: %v", left)
	}
}

func TestLeavingAPoolErasesSMBCredentialFile(t *testing.T) {
	var removed []string
	previous := removeBackupSMBCredentialNative
	removeBackupSMBCredentialNative = func(reference string) error { removed = append(removed, reference); return nil }
	t.Cleanup(func() { removeBackupSMBCredentialNative = previous })
	s := seedBackupMaster(t)
	receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:smb3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	s.State.PoolID = "pool-2"
	if err := eraseForeignBackupCredentials(s); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != receipt.Receipt {
		t.Fatalf("the mount credentials file was not erased: %v", removed)
	}
	if left, _ := filepath.Glob(filepath.Join(s.dir, clusterSecretDirectory, "backup", "*.json")); len(left) != 0 {
		t.Fatalf("receipt left: %v", left)
	}
}

// A cleanup record the authority would refuse must never reach disk: one bad record stops
// every later publication of the pool's backup state.
func TestOrphanCleanupRecordThatBreaksAuthorityIsNotSaved(t *testing.T) {
	master, _, _ := orphanSafetyLab(t, "gone-1")
	req := orphanRequest([]string{"vm-gone-gone-1"}, 1, 0, false)
	rec, err := planOrphanCleanup(master, "clean-1", req)
	if err != nil {
		t.Fatal(err)
	}
	rec.Points[0].Status, rec.Points[0].Reason = "failed", strings.Repeat("x", 5000)
	if err := saveOrphanCleanup(master, rec); err == nil {
		t.Fatal("an oversized reason was saved")
	}
	saved, err := loadOrphanCleanup(master, "clean-1")
	if err != nil || saved.Points[0].Status != "pending" {
		t.Fatalf("the earlier record was damaged: %+v %v", saved, err)
	}
	if _, err := encodePoolBackupAuthorityFromStore(master, nil); err != nil {
		t.Fatalf("the authority cannot be published: %v", err)
	}
}

func TestPoolBackupAuthorityWalkRejectsInvalidOrphanCleanup(t *testing.T) {
	master, _, _ := orphanSafetyLab(t, "gone-1")
	req := orphanRequest([]string{"vm-gone-gone-1"}, 1, 0, false)
	rec, err := planOrphanCleanup(master, "clean-1", req)
	if err != nil {
		t.Fatal(err)
	}
	rec.Points[0].Reason, rec.UpdatedAt = strings.Repeat("x", 5000), time.Now().UTC()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanCleanupPath(master, "clean-1"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := walkPoolBackupAuthorityRecords(master, func(byte, any) error { return nil }); err == nil {
		t.Fatal("an invalid cleanup record was published")
	}
}

// A long cleanup checkpoints its record in batches; every point is still settled and the
// stored record stays complete.
func TestOrphanCleanupOfManyPointsSettlesEveryPoint(t *testing.T) {
	ids := make([]string, 60)
	vms := make([]string, 60)
	for i := range ids {
		ids[i] = fmt.Sprintf("gone-%02d", i)
		vms[i] = "vm-gone-" + ids[i]
	}
	master, mount, _ := orphanSafetyLab(t, ids...)
	req := orphanRequest(vms, len(ids), 0, false)
	rec, err := cleanupBackupOrphans(context.Background(), master, "clean-1", req)
	if err != nil || rec.Status != "succeeded" {
		t.Fatalf("cleanup = %+v %v", rec, err)
	}
	stored, err := loadOrphanCleanup(master, "clean-1")
	if err != nil || stored.Status != "succeeded" || len(stored.Points) != len(ids) {
		t.Fatalf("stored cleanup = %+v %v", stored, err)
	}
	for _, point := range stored.Points {
		if point.Status != "deleted" {
			t.Fatalf("point %s = %+v", point.RecoveryPointID, point)
		}
		if _, err := os.Stat(filepath.Join(mount, point.RecoveryPointID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("destination still holds %s: %v", point.RecoveryPointID, err)
		}
	}
}
