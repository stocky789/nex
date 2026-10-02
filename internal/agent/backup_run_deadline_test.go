// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// A member run still pending past stalledRunLimit settles needs_attention with a reason
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

func TestLocalRunDeadlinePreservesWorkerAndLateCompletion(t *testing.T) {
	master := seedBackupMaster(t)
	run := boundsRun(master, "local", master.State.HostID, "pending")
	run.CreatedAt = time.Now().UTC().Add(-13 * time.Hour)
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var once sync.Once
	stalled, finished := make(chan managedCompletion, 1), make(chan managedCompletion, 1)
	received := false
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		if !received {
			<-finished
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		finished <- runLocalBackupWithDeadline(ctx, master, run, func() managedCompletion {
			<-release
			return managedCompletion{result: protocol.Result{OperationID: run.OperationID, Status: "succeeded"}}
		}, func(completion managedCompletion) { stalled <- completion })
	}()
	select {
	case got := <-stalled:
		if got.result.Status != "needs_attention" {
			t.Fatalf("deadline result = %+v", got.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadline was not reported")
	}
	if saved, err := loadPoolBackupRun(master, run.OperationID); err != nil || saved.Status != "needs_attention" {
		t.Fatalf("deadline run = %+v, %v", saved, err)
	}
	cancel()
	select {
	case got := <-finished:
		received = true
		t.Fatalf("cancellation was mistaken for stopped work: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	select {
	case got := <-finished:
		received = true
		if got.result.Status != "succeeded" {
			t.Fatalf("late completion was lost: %+v", got.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopped worker did not release ownership")
	}
}

func TestLocalRunFinishedBeforeDeadlineKeepsOutcome(t *testing.T) {
	master := seedBackupMaster(t)
	run := boundsRun(master, "finished", master.State.HostID, "pending")
	run.CreatedAt = time.Now().UTC()
	got := runLocalBackupWithDeadline(context.Background(), master, run, func() managedCompletion {
		return managedCompletion{result: protocol.Result{OperationID: run.OperationID, Status: "succeeded"}}
	}, func(completion managedCompletion) { t.Errorf("finished work reported stalled: %+v", completion.result) })
	if got.result.Status != "succeeded" {
		t.Fatalf("finished run = %+v", got.result)
	}
}

func TestRunDeadlineDoesNotReconcileLiveRestoreStaging(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "late-completion"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			master := seedBackupMaster(t)
			seedFlowRepository(t, master, "point-1")
			cmd := protocol.Command{OperationID: "live-restore", Kind: protocol.BackupKindRestoreNew,
				Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1",
					NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored",
					StorageID: "storage-1", HostID: master.State.HostID}}
			if err := applyResponse(master, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
				t.Fatal(err)
			}
			if err := ensureBackupRunReserved(master, cmd); err != nil {
				t.Fatal(err)
			}
			run, err := loadPoolBackupRun(master, cmd.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			run.CreatedAt = time.Now().UTC().Add(-13 * time.Hour)
			if err := savePoolBackupRun(master, run); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var starts atomic.Int32
			previous := requestBackupRestorePlacement
			requestBackupRestorePlacement = func(context.Context, *Store, protocol.VMCreateSpec, string) error {
				starts.Add(1)
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
				return errors.New("disposable placement refusal after staging")
			}
			t.Cleanup(func() { requestBackupRestorePlacement = previous })
			results := make(chan protocol.Result, 32)
			_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var report protocol.ReportRequest
				if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
					t.Error(err)
					return
				}
				for _, result := range report.Results {
					if result.OperationID == cmd.OperationID {
						select {
						case results <- result:
						default:
						}
					}
				}
				_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
			}))
			master.State.Controller, master.State.TrustPEM = identity.Controller, identity.TrustPEM
			master.State.CertificatePEM, master.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			done := make(chan error, 1)
			var once sync.Once
			runStopped := false
			defer func() {
				once.Do(func() { close(release) })
				cancel()
				if !runStopped {
					if err := <-done; err != nil {
						t.Errorf("Run stopped with error: %v", err)
					}
				}
			}()
			go func() {
				done <- Run(ctx, master, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: 100 * time.Millisecond, ProbeTimeout: 20 * time.Millisecond})
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("restore never reached live staging")
			}
			for observations := 0; observations < 4; {
				select {
				case result := <-results:
					if result.Status != "needs_attention" {
						t.Fatalf("live restore was reconciled terminal: %+v", result)
					}
					observations++
				case <-ctx.Done():
					t.Fatal("deadline outcome was not reported while work remained live")
				}
			}
			artifact := filepath.Join(restoreStagingDir(master, cmd.OperationID), "vda.qcow2")
			if bytes, err := os.ReadFile(artifact); err != nil || string(bytes) != "disk-bytes-for-restore" {
				t.Fatalf("live staging was removed or altered: %q, %v", bytes, err)
			}
			if starts.Load() != 1 {
				t.Fatalf("live restore was executed %d times", starts.Load())
			}
			if !pinned(t, master, cmd.OperationID) {
				t.Fatal("live restore lost its recovery-point pin")
			}
			if shutdown {
				cancel()
				select {
				case err := <-done:
					runStopped = true
					t.Fatalf("Run returned while its worker still owned staging: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				if _, err := os.Stat(artifact); err != nil {
					t.Fatalf("shutdown removed live staging: %v", err)
				}
				once.Do(func() { close(release) })
				err := <-done
				runStopped = true
				if err != nil {
					t.Fatalf("Run shutdown failed: %v", err)
				}
				if saved, err := loadPoolBackupRun(master, cmd.OperationID); err != nil || saved.Status != "failed" {
					t.Fatalf("stopped worker did not persist its outcome before shutdown: %+v, %v", saved, err)
				}
				return
			}
			once.Do(func() { close(release) })
			for {
				select {
				case result := <-results:
					if result.Status == "failed" {
						if _, err := os.Stat(artifact); !os.IsNotExist(err) {
							t.Fatalf("stopped, refused restore retained staging: %v", err)
						}
						return
					}
				case <-ctx.Done():
					t.Fatal("late worker outcome never reached the owner journal")
				}
			}
		})
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
