// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/nexupdate"
)

// stubApt redirects nexupdate at a fake apt so this API test never touches the
// host's package manager. The installed version advances once the upgrade runs,
// which is what ApplyLocal checks to decide the update actually happened. A
// non-nil gate holds the install until it is closed, so a test can keep an apply
// running while it checks what shutdown does with it.
func stubApt(t *testing.T, installed, candidate string, gate <-chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	prevSource, prevStamp := nexupdate.AptSourcePath, nexupdate.IndexStampPath
	prevCommand, prevLookPath := nexupdate.Command, nexupdate.LookPath
	nexupdate.AptSourcePath = filepath.Join(dir, "nex.sources")
	nexupdate.IndexStampPath = filepath.Join(dir, "apt-refresh.stamp")
	nexupdate.LookPath = func(string) (string, error) { return "/usr/bin/stub", nil }

	// The apply runs on a background goroutine while the test polls the check
	// endpoint, so the version both sides see has to be race-free.
	var current atomic.Value
	current.Store(installed)
	nexupdate.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := strings.Join(append([]string{name}, args...), " ")
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return exec.CommandContext(ctx, "printf", "%s", current.Load().(string))
		case strings.HasPrefix(line, "apt-cache policy"):
			return exec.CommandContext(ctx, "printf", "  Candidate: %s\n", candidate)
		case name == "dpkg":
			return exec.CommandContext(ctx, name, args...)
		case strings.Contains(line, "install -y"):
			if gate != nil {
				<-gate
			}
			current.Store(candidate)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() {
		nexupdate.AptSourcePath, nexupdate.IndexStampPath = prevSource, prevStamp
		nexupdate.Command, nexupdate.LookPath = prevCommand, prevLookPath
	})
}

func TestLocalUpdatesAuthAndApply(t *testing.T) {
	stubApt(t, "0.1.0", "9.9.9", nil)
	t.Setenv("NEX_UPDATE_REPO", "https://mirror.test/apt")

	s, _ := testService(t)
	if w := callLocal(s, "GET", "/api/local/v1/updates", nil, "", "", ""); w.Code != 401 {
		t.Fatalf("unauth %d", w.Code)
	}
	cookie, csrf := loginLocal(t, s)
	w := callLocal(s, "GET", "/api/local/v1/updates", nil, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("check %d %s", w.Code, w.Body)
	}
	var st nexupdate.Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || !st.UpdateAvailable {
		t.Fatalf("%s", w.Body)
	}
	if st.AvailableVersion != "9.9.9" {
		t.Fatalf("%+v", st)
	}

	w = callLocal(s, "POST", "/api/local/v1/updates/apply", map[string]any{}, cookie, csrf, "https://core.test")
	if w.Code != 202 {
		t.Fatalf("apply %d %s", w.Code, w.Body)
	}
	var result nexupdate.ApplyResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Status != "scheduled" {
		t.Fatalf("%s", w.Body)
	}
	// The background apply should reach a terminal phase, not just start one.
	for i := 0; i < 100; i++ {
		time.Sleep(20 * time.Millisecond)
		w = callLocal(s, "GET", "/api/local/v1/updates", nil, cookie, csrf, "https://core.test")
		_ = json.Unmarshal(w.Body.Bytes(), &st)
		if st.Phase == "done" || st.Phase == "failed" {
			break
		}
	}
	if st.Phase != "done" {
		t.Fatalf("expected a completed apply, got %+v", st)
	}
}

// The apply runs on a goroutine that keeps writing progress into the state
// directory, so shutting the service down while it runs used to leave dpkg
// running and files appearing in a directory the caller was already removing.
func TestCloseWaitsForTheBackgroundUpdate(t *testing.T) {
	gate := make(chan struct{})
	stubApt(t, "0.1.0", "9.9.9", gate)
	t.Setenv("NEX_UPDATE_REPO", "https://mirror.test/apt")

	s, cfg := testService(t)
	cookie, csrf := loginLocal(t, s)
	w := callLocal(s, "POST", "/api/local/v1/updates/apply", map[string]any{}, cookie, csrf, "https://core.test")
	if w.Code != 202 {
		t.Fatalf("apply %d %s", w.Code, w.Body)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned while the apply was still writing to the state directory")
	case <-time.After(100 * time.Millisecond):
	}
	close(gate)
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("Close did not return after the apply finished")
	}
	// Nothing may write here any more; that is what makes removing the directory safe.
	if err := os.RemoveAll(cfg.DataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatalf("state directory came back after Close: %v", err)
	}
}
