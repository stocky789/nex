// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// An observation that decides nothing never rewrites a run that already needs attention.
func TestPeerObservationUnsettled(t *testing.T) {
	reply := func(status string) poolBackupPeerResponse {
		return poolBackupPeerResponse{Result: protocol.Result{Status: status}}
	}
	cases := []struct {
		name string
		peer peerBackupCompletion
		want bool
	}{
		{"transport error", peerBackupCompletion{err: errors.New("dial tcp: refused")}, true},
		{"permanent refusal", peerBackupCompletion{err: permanentPeerBackupError{errors.New("refused")}}, true},
		{"member still running", peerBackupCompletion{reply: reply("pending")}, true},
		{"member needs attention", peerBackupCompletion{reply: reply("needs_attention")}, true},
		{"member succeeded", peerBackupCompletion{reply: reply("succeeded")}, false},
		{"member failed", peerBackupCompletion{reply: reply("failed")}, false},
		{"success with a late error", peerBackupCompletion{reply: reply("succeeded"), err: errors.New("settling")}, false},
	}
	for _, tc := range cases {
		if got := peerObservationUnsettled(tc.peer); got != tc.want {
			t.Errorf("%s: unsettled = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A member-side peer entry never reports a queued outcome, even with a blocker reason.
func TestMemberPeerBackupEntryReportsNoPhantomOutcome(t *testing.T) {
	entry := backupEntry("run-b", "vm-1", 1)
	entry.PeerDispatched, entry.Error = true, "waiting for another backup or snapshot to finish"
	if got := commandOutcomes(map[string]JournalEntry{"run-b": entry}, nil); len(got) != 0 {
		t.Fatalf("member reported a phantom outcome: %+v", got)
	}
}

// Several blockers change in one pass, and a cleanup that needs attention keeps its reason.
func TestBlockersBatchAndKeepAttentionReason(t *testing.T) {
	journal := map[string]JournalEntry{}
	for i, id := range []string{"a", "b", "c"} {
		journal[id] = backupEntry(id, "vm-1", i+1)
	}
	s := &Store{dir: t.TempDir(), State: State{Journal: journal}}
	noteQueuedBackupBlockers(s, "a", "")
	for _, id := range []string{"b", "c"} {
		if s.State.Journal[id].Error == "" {
			t.Fatalf("%s has no blocker", id)
		}
	}
	if _, err := os.Stat(s.dir + "/state.json"); err != nil {
		t.Fatalf("blockers were not persisted: %v", err)
	}
	s.State.Journal["cl"] = JournalEntry{Status: "needs_attention", Error: "test VM cleanup needs reconciliation: x", Acknowledged: true,
		Command: &protocol.Command{OperationID: "cl", Kind: "backup-test-cleanup"}}
	noteCleanupBlocker(s, "cl", peerBackupUnreachable)
	if e := s.State.Journal["cl"]; e.Error != "test VM cleanup needs reconciliation: x" || !e.Acknowledged {
		t.Fatalf("attention reason overwritten: %+v", e)
	}
}

// A stalled local backup still owns its worker: snapshots of that VM wait, other VMs do not.
func TestSnapshotsWaitForLiveBackupWorkerOfSameVM(t *testing.T) {
	snapshot := func(id, vm string, order uint64) JournalEntry {
		return JournalEntry{Status: "pending", Order: order, Command: &protocol.Command{OperationID: id, Kind: "snapshot-create", VM: &protocol.VMCreateSpec{ID: vm}}}
	}
	backup := backupEntry("backup-1", "vm-1", 1)
	backup.Status = "needs_attention"
	journal := map[string]JournalEntry{"backup-1": backup, "snap-same": snapshot("snap-same", "vm-1", 2), "snap-other": snapshot("snap-other", "vm-2", 3)}
	live := map[string]bool{"backup-1": true}
	blocked := snapshotsOfLiveBackupVMs(journal, live)
	if !blocked["snap-same"] || blocked["snap-other"] || blocked["backup-1"] {
		t.Fatalf("blocked = %v", blocked)
	}
	if next := nextManagedCommand(journal, "", live, blocked); next != "snap-other" {
		t.Fatalf("next = %q", next)
	}
	if blocked := snapshotsOfLiveBackupVMs(journal, nil); len(blocked) != 0 {
		t.Fatalf("no live worker still blocked: %v", blocked)
	}
}

// A VM spec that cannot be read is retried; a VM that is gone settles the run failed.
func TestUnreadableVMSpecRetriesInsteadOfFailingRun(t *testing.T) {
	master, _, run, _ := memberRunLab(t)
	run.Status, run.Stage = "pending", "reserved"
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poolVMFile(master), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	backoff := peerBackoff{}
	if _, ok := nextPendingPeerBackup(master, backoff, time.Now()); ok {
		t.Fatal("a run was dispatched without its VM spec")
	}
	if got, err := loadPoolBackupRun(master, run.OperationID); err != nil || got.Status != "pending" {
		t.Fatalf("read error settled the run: %+v, %v", got, err)
	}
	if !backoff.waiting(run.OperationID, time.Now()) {
		t.Fatal("read error was not backed off")
	}
}

// Only the typed stage marks a test restore the member never started.
func TestTestRestoreNeverStartedUsesStageNotText(t *testing.T) {
	run := protocol.BackupRun{Status: "failed", Stage: "failed", Error: errPeerBackupNotStarted.Error()}
	if testRestoreNeverStarted(run) {
		t.Fatal("a started run was treated as never started because of its error text")
	}
	run.Stage = stageNotStarted
	if !testRestoreNeverStarted(run) {
		t.Fatal("typed never-started run not recognised")
	}
}

// A needs-attention journal entry of a member that cannot settle it does not pin a transfer.
func TestUnobservableMemberJournalEntryDoesNotBlockTransfer(t *testing.T) {
	lab := newTransferLab(t, 2)
	master := lab.host("master-1")
	run := boundsRun(master, "on-member-1", "member-1", "needs_attention")
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("vm-on-member-1", "member-1")
	cmd := protocol.Command{OperationID: run.OperationID, Kind: protocol.BackupKindBackupNow, VM: vm, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	master.State.Journal = map[string]JournalEntry{run.OperationID: {Command: &cmd, Status: "needs_attention"}}
	if err := unsettledBackupTransferRefusal(master, "eject", "member-2"); err == nil {
		t.Fatal("an unresolved run on a ready member must still block")
	}
	if err := unsettledBackupTransferRefusal(master, "eject", "member-1"); err != nil {
		t.Fatalf("journal entry of the ejected member blocked the eject: %v", err)
	}
}

// An aged receipt is only settled when the change is not refused for another reason.
func TestTransferRefusalLeavesAgedReceiptsAloneWhenRefusing(t *testing.T) {
	s := seedBackupMaster(t)
	digest := strings.Repeat("a", 64)
	old := protocol.BackupMetadataReceipt{OperationID: "old-op", Kind: "backup-repository-register", IntentSHA256: digest, Status: "pending", ObservedAt: time.Now().Add(-2 * peerBackupRunLimit)}
	fresh := protocol.BackupMetadataReceipt{OperationID: "fresh-op", Kind: "backup-repository-register", IntentSHA256: digest, Status: "pending", ObservedAt: time.Now()}
	for _, receipt := range []protocol.BackupMetadataReceipt{old, fresh} {
		if err := savePoolBackupMetadataReceipt(s, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if err := unsettledBackupTransferRefusal(s, "promote", ""); err == nil {
		t.Fatal("a fresh pending receipt must refuse")
	}
	if got, err := loadPoolBackupMetadataReceipt(s, "old-op"); err != nil || got.Status != "pending" {
		t.Fatalf("refused change still settled the aged receipt: %+v, %v", got, err)
	}
}

// A busy repository is a retryable answer to a member backup request, not a permanent refusal.
func TestMemberBackupRequestOnBusyRepositoryIsRetryable(t *testing.T) {
	s := seedBackupMaster(t)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID}
	poolBackupAuthorityMu.Lock()
	mutation, err := reserveBackupRepositoryMutation(s, repo.ID)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	defer mutation.release()
	cmd := protocol.Command{OperationID: "busy-backup", Kind: protocol.BackupKindBackupNow,
		Backup: &protocol.BackupNowRequest{RepositoryID: repo.ID}}
	response := acceptBackupRequest(t.Context(), s, BackupRequest{Command: &cmd, PeerRepository: &repo})
	if !errors.Is(response.Err, ErrBackupUnavailable) || errors.Is(response.Err, ErrBackupConflict) {
		t.Fatalf("busy repository answered %v, want a retryable unavailable", response.Err)
	}
}
