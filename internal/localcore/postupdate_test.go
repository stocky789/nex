// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/nexupdate"
)

func waitForProgressPhase(t *testing.T, dir, want string) nexupdate.Progress {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if p, ok := nexupdate.ReadProgress(dir); ok && p.Phase == want {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	p, _ := nexupdate.ReadProgress(dir)
	t.Fatalf("progress phase = %q, want %q", p.Phase, want)
	return p
}

func enrollForTest(t *testing.T, dir string) {
	t.Helper()
	store, err := agent.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.State.HostID = "existing-host"
	if err = store.Save(); err != nil {
		t.Fatal(err)
	}
	store.Close()
}

// An update that restarted the service must not end "done" while the Controller hears
// nothing from the host: that silence is exactly how a broken restart stays invisible.
func TestPostUpdateCheckFlagsAHostThatNeverReports(t *testing.T) {
	s, cfg := testService(t)
	enrollForTest(t, cfg.AgentStateDir)
	prev := postUpdateReportWait
	postUpdateReportWait = 1500 * time.Millisecond
	t.Cleanup(func() { postUpdateReportWait = prev })
	if err := nexupdate.BeginPostUpdateCheck(cfg.DataDir, ""); err != nil {
		t.Fatal(err)
	}
	if err := nexupdate.WriteProgress(cfg.DataDir, nexupdate.Progress{Phase: nexupdate.PhaseVerifying, Percent: 98}); err != nil {
		t.Fatal(err)
	}
	s.WatchPostUpdate()
	p := waitForProgressPhase(t, cfg.DataDir, nexupdate.PhaseAttention)
	if p.Error == "" {
		t.Fatal("attention outcome gave the operator no explanation")
	}
	if nexupdate.PostUpdateCheckPending(cfg.DataDir) {
		t.Fatal("a resolved check left its marker, so every restart would repeat it")
	}
}

// A host with no Controller has nothing to report to; its update is complete.
func TestPostUpdateCheckCompletesForUnenrolledHost(t *testing.T) {
	s, cfg := testService(t)
	if err := nexupdate.BeginPostUpdateCheck(cfg.DataDir, ""); err != nil {
		t.Fatal(err)
	}
	s.WatchPostUpdate()
	waitForProgressPhase(t, cfg.DataDir, "done")
	if nexupdate.PostUpdateCheckPending(cfg.DataDir) {
		t.Fatal("check marker left behind")
	}
}

// With no marker the watcher must not touch an unrelated progress file.
func TestPostUpdateCheckIgnoresRestartsWithoutAnUpdate(t *testing.T) {
	s, cfg := testService(t)
	enrollForTest(t, cfg.AgentStateDir)
	s.WatchPostUpdate()
	time.Sleep(1200 * time.Millisecond)
	if _, ok := nexupdate.ReadProgress(filepath.Clean(cfg.DataDir)); ok {
		t.Fatal("a plain restart wrote update progress")
	}
}
