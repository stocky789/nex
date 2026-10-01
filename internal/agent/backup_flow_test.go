// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func seedFlowRepository(t *testing.T, s *Store, points ...string) string {
	t.Helper()
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range points {
		seedPublishedPoint(t, mount, "repo-1", s.State.PoolID, id)
	}
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	tagCatalogPolicy(t, s, "repo-1", "policy-1", points...)
	return mount
}

// tagCatalogPolicy records the creating policy on cataloged points, as
// settlement does; scanned points carry none.
func tagCatalogPolicy(t *testing.T, s *Store, repositoryID, policyID string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		point, err := getCatalogPoint(s, repositoryID, id)
		if err != nil {
			t.Fatal(err)
		}
		point.PolicyID = policyID
		if err := savePoolBackupCatalogEntry(s, point); err != nil {
			t.Fatal(err)
		}
	}
}

func replaceCommand(id string) protocol.Command {
	return protocol.Command{OperationID: id, Kind: protocol.BackupKindRestoreReplace,
		RestoreReplace: &protocol.RestoreReplaceRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			OriginalVMID: "11111111-1111-4111-8111-111111111111", StorageID: "storage-1", ConfirmReplace: true}}
}

func pinned(t *testing.T, s *Store, operationID string) bool {
	t.Helper()
	pins, err := loadPoolBackupPins(s)
	if err != nil {
		t.Fatal(err)
	}
	_, ok := pins.Pins[operationID]
	return ok
}

// Regression for BUG-0731 (flow H1): a repository read failure before restore
// replace touched the original VM left the run, its pin and the VM stuck.
func TestRestoreReplaceReadFailureBeforeEffectsFails(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s, "point-1")
	hidden := filepath.Join(t.TempDir(), "point-1")
	execute, reconcile := replaceCommand("replace-read"), replaceCommand("replace-reconcile")
	for _, cmd := range []protocol.Command{execute, reconcile} {
		if err := ensureBackupRunReserved(s, cmd); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(filepath.Join(mount, "point-1"), hidden); err != nil {
		t.Fatal(err)
	}
	result, err := executeRestoreReplace(context.Background(), s, execute, time.Now().UTC())
	if err != nil || result.Status != "failed" || pinned(t, s, execute.OperationID) {
		t.Fatalf("read failure before effects: %+v %v pinned=%t", result, err, pinned(t, s, execute.OperationID))
	}
	run, err := loadPoolBackupRun(s, reconcile.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Error = "needs_attention", "repository read timed out"
	if err := SettlePoolBackupRun(s, run, nil); err != nil {
		t.Fatal(err)
	}
	result, err = reconcileRestoreReplaceAttention(context.Background(), s.dir, s.State, reconcile)
	if err != nil || result.Status != "failed" || pinned(t, s, reconcile.OperationID) {
		t.Fatalf("reconcile without retained original: %+v %v", result, err)
	}
}

// Regression for BUG-0731 (flow L1): a failure that could not be settled must
// not report failed or release the pin while the run is still pending.
func TestFailReplaceRunKeepsRunUncertainWhenSettlementIsRefused(t *testing.T) {
	s := seedBackupMaster(t)
	seedFlowRepository(t, s, "point-1")
	cmd := replaceCommand("replace-frozen")
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatal(err)
	}
	if err := savePoolTransfer(s, &poolTransferRecord{OperationID: "freeze", Action: "promote", Role: "source",
		Stage: "freeze", Status: "pending", TargetHostID: "member-1", Frozen: true}); err != nil {
		t.Fatal(err)
	}
	if result := failReplaceRun(s, cmd, time.Now().UTC(), "refused"); result.Status != "needs_attention" || !pinned(t, s, cmd.OperationID) {
		t.Fatalf("unsettled failure: %+v", result)
	}
}

// Regression for BUG-0731 (flow M1): a member restore that stopped before
// placement has no owner record to prove it, so the staging directory does.
func TestMemberRestoreWithoutStagingSettlesFailed(t *testing.T) {
	lab := newTransferLab(t, 1)
	member := lab.host("member-1")
	cmd := protocol.Command{OperationID: "member-restore", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "Restored", StorageID: "storage-1", HostID: member.State.HostID}}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRun(member, protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: member.State.PoolID,
		HostID: member.State.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: cmd.Restore.NewVMID,
		IntentSHA256: intent, Stage: "placement", Status: "needs_attention", Error: "repository read timed out", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := createRestoreStaging(member, cmd.OperationID); err != nil {
		t.Fatal(err)
	}
	if restorePlacementHasNoEffects(member, cmd.OperationID, cmd.Restore.NewVMID) {
		t.Fatal("staged member restore was treated as unplaced")
	}
	removeRestoreStaging(member, cmd.OperationID)
	result, err := reconcileRestoreAsNewAttention(context.Background(), member.dir, member.State, cmd)
	if err != nil || result.Status != "failed" {
		t.Fatalf("unplaced member restore stayed uncertain: %+v %v", result, err)
	}
}

// Regression for BUG-0731 (unreserved runs): nothing durable exists when a
// reservation fails, so the command fails instead of waiting forever.
func TestUnreservedRestoreFails(t *testing.T) {
	s := seedBackupMaster(t)
	seedFlowRepository(t, s)
	cmd := protocol.Command{OperationID: "restore-missing", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "Restored", StorageID: "storage-1", HostID: s.State.HostID}}
	if result, err := executeRestoreAsNew(context.Background(), s, cmd, time.Now().UTC()); err != nil || result.Status != "failed" {
		t.Fatalf("restore without a reservable run: %+v %v", result, err)
	}
}

// Regression for BUG-0732 (flow M2, storage-creds M1, M6, L5): retention keeps
// the point being settled whatever its capture clock said, skips a point it
// cannot delete, and runs without holding the pool backup lock.
func TestRetentionKeepsSettlingPointAndSkipsUndeletablePoint(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s, "newer", "old-a", "old-b", "new")
	now := time.Now().UTC()
	catalog := map[string]protocol.BackupRecoveryPoint{}
	// "new" is settling but its host's clock was behind; "newer" really is newer.
	for id, age := range map[string]time.Duration{"newer": time.Hour, "old-a": 2 * time.Hour, "old-b": 3 * time.Hour, "new": 4 * time.Hour} {
		point, err := getCatalogPoint(s, "repo-1", id)
		if err != nil {
			t.Fatal(err)
		}
		point.CapturedAt = now.Add(-age)
		if err := savePoolBackupCatalogEntry(s, point); err != nil {
			t.Fatal(err)
		}
		catalog[id] = point
	}
	if err := os.RemoveAll(filepath.Join(mount, "old-a")); err != nil {
		t.Fatal(err)
	}
	created := now.Add(-time.Hour)
	run := protocol.BackupRun{OperationID: "new", Kind: protocol.BackupKindBackupNow, PoolID: s.State.PoolID,
		VMID: "vm-1", RepositoryID: "repo-1", RecoveryPointID: "new", Stage: "reserved", Status: "pending",
		PolicyID: "policy-1", CreatedAt: created, Retention: &protocol.BackupRetention{KeepLatest: 1}}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	previous := verifyBackupRepository
	t.Cleanup(func() { verifyBackupRepository = previous })
	entered, release := make(chan struct{}, 1), make(chan struct{})
	verifyBackupRepository = func(ctx context.Context, repo protocol.BackupRepository) error {
		// Only this test's repository: a leftover goroutine from another test must not take the slot.
		if repo.MountPath == mount {
			select {
			case entered <- struct{}{}:
				<-release
			default:
			}
		}
		return previous(ctx, repo)
	}
	settled := run
	settled.Status, settled.Stage, settled.CreatedAt = "succeeded", "published", now
	point := catalog["new"]
	done := make(chan error, 1)
	go func() { done <- SettlePoolBackupRun(s, settled, &point) }()
	select {
	case <-entered:
		if !poolBackupAuthorityMu.TryLock() {
			close(release)
			<-done
			t.Fatal("retention held the pool backup lock across repository I/O")
		}
		poolBackupAuthorityMu.Unlock()
		close(release)
	case err := <-done:
		t.Fatalf("settlement finished without retention: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal("retention failure blocked settlement:", err)
	}
	for _, id := range []string{"new", "newer"} {
		if _, err := getCatalogPoint(s, "repo-1", id); err != nil {
			t.Fatalf("retention pruned %s: %v", id, err)
		}
	}
	if _, err := getCatalogPoint(s, "repo-1", "old-b"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatal("an undeletable point stopped retention for the rest:", err)
	}
	saved, err := loadPoolBackupRun(s, "new")
	if err != nil || saved.Status != "succeeded" || !strings.Contains(saved.Error, "old-a") || !saved.CreatedAt.Equal(created) {
		t.Fatalf("settled run: %+v %v", saved, err)
	}
	if err := SettlePoolBackupRun(s, settled, &point); err != nil {
		t.Fatal("retention note broke the exact settlement retry:", err)
	}
}

// Regression for BUG-0738: an older point that settles after a newer one must
// not outrank it; its own settlement keeps it and retention takes it later.
func TestLateSettlementKeepsNewerPoint(t *testing.T) {
	s := seedBackupMaster(t)
	seedFlowRepository(t, s, "older", "newer")
	now := time.Now().UTC()
	for id, age := range map[string]time.Duration{"older": 2 * time.Hour, "newer": time.Hour} {
		point, err := getCatalogPoint(s, "repo-1", id)
		if err != nil {
			t.Fatal(err)
		}
		point.CapturedAt = now.Add(-age)
		if err := savePoolBackupCatalogEntry(s, point); err != nil {
			t.Fatal(err)
		}
	}
	run := protocol.BackupRun{OperationID: "older", Kind: protocol.BackupKindBackupNow, PoolID: s.State.PoolID,
		VMID: "vm-1", RepositoryID: "repo-1", RecoveryPointID: "older", Stage: "reserved", Status: "pending",
		PolicyID: "policy-1", CreatedAt: now.Add(-2 * time.Hour), Retention: &protocol.BackupRetention{KeepLatest: 1}}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	tagCatalogPolicy(t, s, "repo-1", "", "older")
	point, err := getCatalogPoint(s, "repo-1", "older")
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.Stage = "succeeded", "published"
	if err := SettlePoolBackupRun(s, run, &point); err != nil {
		t.Fatal(err)
	}
	if settled, err := getCatalogPoint(s, "repo-1", "older"); err != nil || settled.PolicyID != "policy-1" {
		t.Fatalf("settlement did not record the creating policy: %+v %v", settled, err)
	}
	for _, id := range []string{"older", "newer"} {
		if _, err := getCatalogPoint(s, "repo-1", id); err != nil {
			t.Fatalf("late settlement pruned %s: %v", id, err)
		}
	}
	if err := prunePublishedBackupPayloads(context.Background(), s, "repo-1", "vm-1", "policy-1", protocol.BackupRetention{KeepLatest: 1}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "newer"); err != nil {
		t.Fatal("retention pruned the newest point:", err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "older"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatal("a later pass kept the expired older point:", err)
	}
}

// Regression for BUG-0739: prune repository I/O runs outside the pool backup
// lock, the point refuses new pins meanwhile, and the prune is counted only
// after the delete, so an import that scanned during it scans again.
func TestPruneDeletesOutsideLockAndCountsAfterDelete(t *testing.T) {
	s := seedBackupMaster(t)
	seedFlowRepository(t, s, "expired", "kept")
	now := time.Now().UTC()
	for id, age := range map[string]time.Duration{"expired": 2 * time.Hour, "kept": time.Hour} {
		point, err := getCatalogPoint(s, "repo-1", id)
		if err != nil {
			t.Fatal(err)
		}
		point.CapturedAt = now.Add(-age)
		if err := savePoolBackupCatalogEntry(s, point); err != nil {
			t.Fatal(err)
		}
	}
	previous, previousRead := verifyBackupRepository, verifyBackupRepositoryRead
	t.Cleanup(func() { verifyBackupRepository, verifyBackupRepositoryRead = previous, previousRead })
	var heldLock, pinned bool
	var seen []uint64
	// Every repository call after the pass's first Verify belongs to the prune.
	observe := func() {
		seen = append(seen, poolBackupPruneSeq.Load())
		if len(seen) == 1 {
			return
		}
		if !poolBackupAuthorityMu.TryLock() {
			heldLock = true
			return
		}
		poolBackupAuthorityMu.Unlock()
		if !poolPeerMu.TryLock() {
			return // Inside the delete's own authority check; pinning would self-deadlock.
		}
		poolPeerMu.Unlock()
		pin := protocol.BackupRestorePin{RecoveryPointID: "expired", OperationID: "restore-late", Kind: protocol.BackupPinKindNew, CreatedAt: now}
		if putBackupRestorePin(s, pin) == nil {
			pinned = true
		}
	}
	verifyBackupRepository = func(ctx context.Context, repo protocol.BackupRepository) error {
		observe()
		return previous(ctx, repo)
	}
	verifyBackupRepositoryRead = func(ctx context.Context, repo protocol.BackupRepository) error {
		observe()
		return previousRead(ctx, repo)
	}
	if err := prunePublishedBackupPayloads(context.Background(), s, "repo-1", "vm-1", "policy-1", protocol.BackupRetention{KeepLatest: 1}, ""); err != nil {
		t.Fatal(err)
	}
	if len(seen) < 2 || heldLock || pinned {
		t.Fatalf("prune I/O: calls=%d under backup lock=%v pinned meanwhile=%v", len(seen), heldLock, pinned)
	}
	if _, err := getCatalogPoint(s, "repo-1", "expired"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatal("expired point was not pruned:", err)
	}
	if poolBackupPruneSeq.Load() == seen[len(seen)-1] {
		t.Fatal("prune was counted before its delete finished")
	}
	if len(poolBackupPruning) != 0 {
		t.Fatal("prune left its point marked:", poolBackupPruning)
	}
}

// Regression for BUG-0733 (s3 L1, flow M8, dispatch M2): a retried backup whose
// earlier attempt began publishing is reconciled, not captured again; its
// leftover capture work is removed; a settled run completes a stale journal.
func TestInterruptedPublicationIsNotCapturedAgain(t *testing.T) {
	s := seedBackupMaster(t)
	seedFlowRepository(t, s)
	cmd := protocol.Command{OperationID: "backup-interrupted", Kind: protocol.BackupKindBackupNow,
		VM:     &protocol.VMCreateSpec{ID: "vm-1", PoolID: s.State.PoolID, HostID: s.State.HostID},
		Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatal(err)
	}
	if err := saveBackupPublicationRun(s, cmd, time.Now().UTC(), "pending", ""); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(s.dir, "backup-staging", cmd.OperationID)
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := executeBackupNow(context.Background(), s, cmd, time.Now().UTC())
	if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "interrupted") {
		t.Fatalf("interrupted publication was captured again: %+v %v", result, err)
	}
	if _, err := os.Stat(work); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("capture work of the interrupted attempt was left behind:", err)
	}
	entry := JournalEntry{Command: &cmd, Status: "needs_attention"}
	if !publishedLocalBackupNeedsReconcile(s, entry) {
		t.Fatal("failed run with a stale journal was not selected")
	}
	if got := reconcilePublishedBackupNow(context.Background(), s, cmd, time.Now().UTC()); got.Status != "failed" {
		t.Fatalf("settled run was not copied to the journal: %+v", got)
	}
}

// Regression for BUG-0742: a retry must not remove online capture work while
// QEMU may still be writing it; an unprovable job state keeps the files.
func TestRetryKeepsOnlineWorkWhileJobMayBeActive(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s)
	cmd := protocol.Command{OperationID: "backup-online", Kind: protocol.BackupKindBackupNow,
		VM:     &protocol.VMCreateSpec{ID: "vm-1", PoolID: s.State.PoolID, HostID: s.State.HostID},
		Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1", Consistency: protocol.BackupCaptureOnline}}
	if err := ensureBackupRunReserved(s, cmd); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(mount, ".nex-backup-work", cmd.OperationID)
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := executeBackupNow(context.Background(), s, cmd, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(work); err != nil {
		t.Fatal("online capture work was removed without proof the job ended:", err)
	}
}

// Regression for BUG-0734 (flow L2): import must not revert an unprotect made
// while its unlocked scan read the old protection marker.
func TestCatalogImportKeepsOperatorUnprotect(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s, "point-1")
	if err := vmhost.WriteBackupProtectMarker(filepath.Join(mount, "point-1"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if point, err := getCatalogPoint(s, "repo-1", "point-1"); err != nil || point.Protected {
		t.Fatalf("import overrode the catalog's protection: %+v %v", point, err)
	}
}

// Regression for BUG-0735 (flow L7): only a changed generation returns 409, so a
// host that is no longer master is not mistaken for a changed history.
func TestBackupPagesOnMemberAreUnavailable(t *testing.T) {
	lab := newTransferLab(t, 1)
	member := lab.host("member-1")
	w := httptest.NewRecorder()
	statsChannelHandler(http.NotFoundHandler(), member).ServeHTTP(w, httptest.NewRequest("GET", "/backup-runs?cursor=1&generation="+strings.Repeat("a", 64), nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("member page status=%d", w.Code)
	}
}

// A policy's retention ranks and prunes only the points that policy created;
// other policies' and manual backups' points stay however old they are.
func TestRetentionPrunesOnlyThePolicysOwnPoints(t *testing.T) {
	s := seedBackupMaster(t)
	seedFlowRepository(t, s, "a-old", "a-new", "b-old", "manual-old")
	tagCatalogPolicy(t, s, "repo-1", "policy-2", "b-old")
	tagCatalogPolicy(t, s, "repo-1", "", "manual-old")
	now := time.Now().UTC()
	for id, age := range map[string]time.Duration{"a-old": 4 * time.Hour, "b-old": 5 * time.Hour, "manual-old": 6 * time.Hour, "a-new": time.Hour} {
		point, err := getCatalogPoint(s, "repo-1", id)
		if err != nil {
			t.Fatal(err)
		}
		point.CapturedAt = now.Add(-age)
		if err := savePoolBackupCatalogEntry(s, point); err != nil {
			t.Fatal(err)
		}
	}
	if err := prunePublishedBackupPayloads(context.Background(), s, "repo-1", "vm-1", "policy-1", protocol.BackupRetention{KeepLatest: 1}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "a-old"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatal("the policy's own expired point was kept:", err)
	}
	for _, id := range []string{"a-new", "b-old", "manual-old"} {
		if _, err := getCatalogPoint(s, "repo-1", id); err != nil {
			t.Fatalf("policy-1 retention pruned %s: %v", id, err)
		}
	}
	if err := prunePublishedBackupPayloads(context.Background(), s, "repo-1", "vm-1", "", protocol.BackupRetention{KeepLatest: 1}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "manual-old"); err != nil {
		t.Fatal("retention without a policy pruned a manual point:", err)
	}
}
