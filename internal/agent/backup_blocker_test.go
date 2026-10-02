// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// A pending Controller-dispatched backup reports why it is waiting as a queued outcome, and the
// reason clears when the member answers normally. A member-side peer entry never reports one:
// the master's own entry carries the reason.
func TestPendingPeerBackupReportsBlockerReason(t *testing.T) {
	s := &Store{dir: t.TempDir(), State: State{Journal: map[string]JournalEntry{"run-1": {
		Status: "pending", RequestedAt: time.Now(),
		Command: &protocol.Command{OperationID: "run-1", Kind: protocol.BackupKindBackupNow, VM: &protocol.VMCreateSpec{ID: "vm-1"}},
	}}}}
	if got := commandOutcomes(s.State.Journal, nil); len(got) != 1 || got[0].Status != "queued" || got[0].Error != "" {
		t.Fatalf("no reason yet: %+v", got)
	}
	noteBlocker(s, "run-1", peerBackupUnreachable)
	got := commandOutcomes(s.State.Journal, nil)
	if len(got) != 1 || got[0].Status != "queued" || got[0].Error != peerBackupUnreachable {
		t.Fatalf("reason not reported: %+v", got)
	}
	noteBlocker(s, "run-1", "")
	if got := commandOutcomes(s.State.Journal, nil); len(got) != 1 || got[0].Error != "" {
		t.Fatalf("reason not cleared: %+v", got)
	}
	entry := s.State.Journal["run-1"]
	entry.Status = "succeeded"
	s.State.Journal["run-1"] = entry
	noteBlocker(s, "run-1", "late")
	if s.State.Journal["run-1"].Error != "" {
		t.Fatal("settled entry was changed")
	}
}

func backupEntry(id, vm string, order int) JournalEntry {
	return JournalEntry{Status: "pending", Order: uint64(order), RequestedAt: time.Now(),
		Command: &protocol.Command{OperationID: id, Kind: protocol.BackupKindBackupNow, VM: &protocol.VMCreateSpec{ID: vm}}}
}

// A queued backup blocked by another run says so, and the reason clears once it is picked.
func TestQueuedBackupReportsWhyItIsBlocked(t *testing.T) {
	first, second := backupEntry("run-1", "vm-1", 1), backupEntry("run-2", "vm-1", 2)
	s := &Store{dir: t.TempDir(), State: State{Journal: map[string]JournalEntry{"run-1": first, "run-2": second}}}
	noteQueuedBackupBlockers(s, "run-1", "")
	if got := s.State.Journal["run-2"].Error; got == "" {
		t.Fatal("blocked backup has no reason")
	}
	if got := s.State.Journal["run-1"].Error; got != "" {
		t.Fatalf("picked backup shows a reason: %q", got)
	}
	noteQueuedBackupBlockers(s, "run-2", "")
	if got := s.State.Journal["run-2"].Error; got != "" {
		t.Fatalf("reason not cleared once picked: %q", got)
	}
	other := backupEntry("run-3", "vm-9", 3)
	s.State.Journal["run-3"] = other
	noteQueuedBackupBlockers(s, "", "run-2")
	if got := s.State.Journal["run-3"].Error; got == "" {
		t.Fatal("backup held by an active run has no reason")
	}
}

// A direct dispatch to an unreachable member records the reason on the pending run.
func TestDirectPeerBackupRecordsUnreachableMember(t *testing.T) {
	s := &Store{dir: t.TempDir(), State: State{Journal: map[string]JournalEntry{"run-1": backupEntry("run-1", "vm-1", 1)}}}
	next := peerBackoff{}
	notePeerBackupAnswer(s, next, peerBackupCompletion{run: protocol.BackupRun{OperationID: "run-1"}, err: errors.New("dial tcp: refused")})
	if got := s.State.Journal["run-1"].Error; got != peerBackupUnreachable {
		t.Fatalf("reason = %q", got)
	}
}

// A test cleanup that cannot reach its member says so.
func TestCleanupBlockerReason(t *testing.T) {
	s := &Store{dir: t.TempDir(), State: State{Journal: map[string]JournalEntry{"c-1": {Status: "needs_attention", Acknowledged: true,
		Command: &protocol.Command{OperationID: "c-1", Kind: "backup-test-cleanup"}}}}}
	noteCleanupBlocker(s, "c-1", peerBackupUnreachable)
	if e := s.State.Journal["c-1"]; e.Error != peerBackupUnreachable || e.Acknowledged {
		t.Fatalf("entry = %+v", e)
	}
}

// The deadline explanation survives a later unresolved observation but not a real outcome.
func TestDeadlineReasonSurvivesUnresolvedObservation(t *testing.T) {
	entry := backupEntry("run-1", "vm-1", 1)
	entry.Status, entry.Error = "needs_attention", stalledRunReason()
	s := &Store{dir: t.TempDir(), State: State{Journal: map[string]JournalEntry{"run-1": entry}}}
	observe := func(status, msg string) JournalEntry {
		done := managedCompletion{background: true, result: protocol.Result{OperationID: "run-1", Status: status, Error: msg, ObservedAt: time.Now()}}
		if err := completeBackgroundManagedCommand(s, done, nil); err != nil {
			t.Fatal(err)
		}
		return s.State.Journal["run-1"]
	}
	if e := observe("needs_attention", "published recovery point is absent"); e.Error != stalledRunReason() {
		t.Fatalf("deadline reason replaced: %q", e.Error)
	}
	if e := observe("failed", "real failure"); e.Status != "failed" || e.Error != "real failure" {
		t.Fatalf("outcome hidden: %+v", e)
	}
}

// A test cleanup still pending while its member is unreachable reports why, in the Tasks outcome.
func TestPendingCleanupReportsUnreachableMember(t *testing.T) {
	s := &Store{dir: t.TempDir(), State: State{Journal: map[string]JournalEntry{"c-1": {Status: "pending",
		Command: &protocol.Command{OperationID: "c-1", Kind: "backup-test-cleanup", TestRestoreClean: &protocol.TestRestoreCleanupRequest{TestVMID: "vm-1"}}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	work := peerCleanupWork{command: *s.State.Journal["c-1"].Command, client: &http.Client{}, member: protocol.PoolMember{PeerOrigin: "http://127.0.0.1:1"}}
	_, _ = waitPeerCleanup(ctx, work, cancel)
	noteCleanupBlocker(s, "c-1", peerBackupUnreachable)
	got := commandOutcomes(s.State.Journal, nil)
	if ctx.Err() == nil || len(got) != 1 || got[0].Error != peerBackupUnreachable {
		t.Fatalf("unreachable member not reported: %+v", got)
	}
}
