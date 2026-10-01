// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// A member run still pending past peerBackupRunLimit settles needs_attention with a reason
// and stays observable, so a later member result still lands on it.
func TestStalledMemberRunSettlesNeedsAttention(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	old := boundsRun(master, "old", member.State.HostID, "pending")
	old.CreatedAt = time.Now().UTC().Add(-13 * time.Hour)
	if err := savePoolBackupRun(master, old); err != nil {
		t.Fatal(err)
	}
	nextPendingPeerBackup(master, peerBackoff{}, time.Now())
	run, err := loadPoolBackupRun(master, "old")
	if err != nil || run.Status != "needs_attention" || run.Stage != "run_deadline" || !strings.Contains(run.Error, "Check that host") {
		t.Fatalf("stalled run = %+v, %v", run, err)
	}
	fresh := boundsRun(master, "fresh", member.State.HostID, "pending")
	if reason := stalledPeerBackupReason(fresh, time.Now()); reason != "" {
		t.Fatalf("a run within the limit was settled: %s", reason)
	}
}

// The direct wait releases the pool backup slot at the deadline even when the member no longer answers,
// and when a responding member keeps returning non-terminal pending.
func TestWaitPeerBackupTerminalStopsAtDeadline(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	work := pendingPeerBackup{run: protocol.BackupRun{OperationID: "wait", Status: "pending", CreatedAt: time.Now().UTC()}, client: down.Client(),
		member: protocol.PoolMember{HostID: "member-1", PeerOrigin: down.URL}}
	if got := waitPeerBackupTerminal(context.Background(), work); got.err == nil {
		t.Fatalf("a run inside the limit was settled: %+v", got.reply.Result)
	}
	work.run.CreatedAt = time.Now().UTC().Add(-13 * time.Hour)
	got := waitPeerBackupTerminal(context.Background(), work)
	if got.err != nil || got.reply.Result.Status != "needs_attention" || got.reply.Result.Error == "" ||
		got.reply.Run == nil || got.reply.Run.Stage != "run_deadline" {
		t.Fatalf("stalled wait = %+v, %v", got.reply, got.err)
	}

	intent := strings.Repeat("a", 64)
	hungRun := protocol.BackupRun{OperationID: "hung", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1", HostID: "member-1",
		RepositoryID: "repo-1", VMID: "vm-1", RecoveryPointID: "hung", IntentSHA256: intent, Status: "pending",
		Stage: "capturing", CreatedAt: time.Now().UTC().Add(-13 * time.Hour)}
	pending := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(poolBackupPeerResponse{
			Result: protocol.Result{OperationID: hungRun.OperationID, Status: "pending"}, IntentSHA256: intent,
			Run: &protocol.BackupRun{OperationID: hungRun.OperationID, Kind: hungRun.Kind, PoolID: hungRun.PoolID, HostID: hungRun.HostID,
				RepositoryID: hungRun.RepositoryID, VMID: hungRun.VMID, RecoveryPointID: hungRun.RecoveryPointID,
				IntentSHA256: intent, Status: "pending", Stage: "capturing"},
		})
	}))
	defer pending.Close()
	hung := pendingPeerBackup{
		run: hungRun, client: pending.Client(), member: protocol.PoolMember{HostID: hungRun.HostID, PeerOrigin: pending.URL},
		repo: protocol.BackupRepository{Kind: protocol.BackupRepoKindNFS},
	}
	hungGot := waitPeerBackupTerminal(context.Background(), hung)
	if hungGot.err != nil || hungGot.reply.Result.Status != "needs_attention" || hungGot.reply.Run == nil || hungGot.reply.Run.Stage != "run_deadline" {
		t.Fatalf("hung pending wait = %+v, %v", hungGot.reply, hungGot.err)
	}
}

// A master-hosted run that never returns settles needs_attention at the deadline and hands the
// slot back; one that finishes first keeps its own outcome.
func TestLocalRunSettlesAtDeadline(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	run := boundsRun(master, "local", master.State.HostID, "pending")
	run.CreatedAt = time.Now().UTC().Add(-13 * time.Hour)
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	got := runLocalBackupWithDeadline(context.Background(), master, run, func() managedCompletion {
		<-stuck
		return managedCompletion{}
	})
	if got.result.Status != "needs_attention" || got.result.Error == "" {
		t.Fatalf("completion = %+v", got)
	}
	if saved, err := loadPoolBackupRun(master, "local"); err != nil || saved.Status != "needs_attention" || saved.Stage != "run_deadline" {
		t.Fatalf("run = %+v, %v", saved, err)
	}
	run.CreatedAt = time.Now().UTC()
	done := runLocalBackupWithDeadline(context.Background(), master, run, func() managedCompletion {
		return managedCompletion{result: protocol.Result{OperationID: "local", Status: "succeeded"}}
	})
	if done.result.Status != "succeeded" {
		t.Fatalf("finished run = %+v", done)
	}
}

// A member that stops answering is not re-observed later than the stalled-run deadline, however
// far its backoff has grown, so the run settles when due rather than on the next backoff.
func TestPeerBackoffNeverPassesStalledDeadline(t *testing.T) {
	s := &Store{dir: t.TempDir(), State: State{Journal: map[string]JournalEntry{"slow": backupEntry("slow", "vm-1", 1)}}}
	old := stalledRunLimit
	stalledRunLimit = 60 * time.Second
	t.Cleanup(func() { stalledRunLimit = old })
	run := protocol.BackupRun{OperationID: "slow", CreatedAt: time.Now().UTC()}
	next := peerBackoff{"slow": {delay: 2 * time.Minute, answer: peerBackupUnreachable, since: time.Now()}}
	notePeerBackupAnswer(s, next, peerBackupCompletion{run: run, err: errors.New("dial tcp: i/o timeout")})
	if got, deadline := next["slow"].next, run.CreatedAt.Add(stalledRunLimit); got.After(deadline) {
		t.Fatalf("next observation %v is after the deadline %v", got, deadline)
	}
}
