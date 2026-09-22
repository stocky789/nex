// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/nexupdate"
)

// stubApt redirects nexupdate at a fake apt so this API test never touches the
// host's package manager. The Controller applies updates through a privileged
// helper, so only the check path needs apt here.
func stubApt(t *testing.T, installed, candidate string) {
	t.Helper()
	dir := t.TempDir()
	prevSource, prevStamp := nexupdate.AptSourcePath, nexupdate.IndexStampPath
	prevCommand, prevLookPath := nexupdate.Command, nexupdate.LookPath
	nexupdate.AptSourcePath = filepath.Join(dir, "nex.sources")
	nexupdate.IndexStampPath = filepath.Join(dir, "apt-refresh.stamp")
	nexupdate.LookPath = func(string) (string, error) { return "/usr/bin/stub", nil }
	nexupdate.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := strings.Join(append([]string{name}, args...), " ")
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return exec.CommandContext(ctx, "printf", "%s", installed)
		case strings.HasPrefix(line, "apt-cache policy"):
			return exec.CommandContext(ctx, "printf", "  Candidate: %s\n", candidate)
		case name == "dpkg":
			return exec.CommandContext(ctx, name, args...)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() {
		nexupdate.AptSourcePath, nexupdate.IndexStampPath = prevSource, prevStamp
		nexupdate.Command, nexupdate.LookPath = prevCommand, prevLookPath
	})
}

func TestUpdatesAuthAndApply(t *testing.T) {
	stubApt(t, "0.1.0", "9.9.9")
	t.Setenv("NEX_UPDATE_REPO", "https://mirror.test/apt")

	helper := filepath.Join(t.TempDir(), "apply-update")
	pathUnit := filepath.Join(t.TempDir(), "nex-controller-update.path")
	if err := os.WriteFile(helper, []byte("#!/bin/true\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathUnit, []byte("[Path]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	prevBin, prevUnits := nexupdate.ApplyUpdateBinary, nexupdate.UpdatePathUnits
	nexupdate.ApplyUpdateBinary = helper
	nexupdate.UpdatePathUnits = []string{pathUnit}
	t.Cleanup(func() {
		nexupdate.ApplyUpdateBinary = prevBin
		nexupdate.UpdatePathUnits = prevUnits
	})

	a := newClient(t)
	a.cookie, a.csrf = nil, ""
	a.call("GET", "/api/v1/updates", nil, nil, 401)

	a = newClient(t)
	var st nexupdate.Status
	read(t, a.call("GET", "/api/v1/updates", nil, nil, 200), &st)
	if !st.UpdateAvailable || st.AvailableVersion != "9.9.9" || st.Package != "nex-controller" {
		t.Fatalf("%+v", st)
	}

	var result nexupdate.ApplyResult
	read(t, a.call("POST", "/api/v1/updates/apply", map[string]any{}, nil, 202), &result)
	if result.Status != "scheduled" {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(filepath.Join(a.c.cfg.DataDir, "pending-update.json")); err != nil {
		t.Fatal(err)
	}
	var audit []map[string]any
	read(t, a.call("GET", "/api/v1/audit", nil, nil, 200), &audit)
	found := false
	for _, row := range audit {
		if row["action"] == "update.apply" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing audit")
	}
}
