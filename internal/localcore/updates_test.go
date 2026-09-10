// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"encoding/json"
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
// which is what ApplyLocal checks to decide the update actually happened.
func stubApt(t *testing.T, installed, candidate string) {
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
		case strings.Contains(line, "install -y"):
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
	stubApt(t, "0.1.0", "9.9.9")
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
