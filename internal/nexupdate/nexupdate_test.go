// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/rootfs"
)

// fakeCommands redirects Command and LookPath at a recording stub so tests exercise
// the update logic without touching the host's real apt or dpkg. handle receives the
// full argv as one string and returns the stdout and exit code the stub should give.
func fakeCommands(t *testing.T, handle func(line string) (string, int)) *[]string {
	t.Helper()
	var calls []string
	prevCommand, prevLookPath := Command, LookPath
	LookPath = func(string) (string, error) { return "/usr/bin/stub", nil }
	Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, line)
		out, code := handle(line)
		if name == "findmnt" {
			// The root window reads the root's options through the same runner that writes.
			out, code = testRootOptions+"\n", 0
		}
		return exec.CommandContext(ctx, "sh", "-c",
			"printf %s "+shellQuote(out)+"; exit "+strconv.Itoa(code))
	}
	t.Cleanup(func() { Command, LookPath = prevCommand, prevLookPath })
	return &calls
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// testRootOptions is what findmnt reports for / in these tests; setRootOptions changes it.
var testRootOptions = "rw,relatime"

func setRootOptions(t *testing.T, options string) {
	t.Helper()
	testRootOptions = options
	t.Cleanup(func() { testRootOptions = "rw,relatime" })
}

// isolatePaths redirects the apt source and refresh stamp into a temp directory so
// tests never write to /etc/apt or /var/lib/nex.
func isolatePaths(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	prevSource, prevStamp := AptSourcePath, IndexStampPath
	AptSourcePath = filepath.Join(dir, "nex.sources")
	IndexStampPath = filepath.Join(dir, "apt-refresh.stamp")
	t.Cleanup(func() { AptSourcePath, IndexStampPath = prevSource, prevStamp })
}

func joined(calls *[]string) string { return strings.Join(*calls, "\n") }

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.1.1", -1},
		{"0.1.1", "0.1.0", 1},
		{"0.1.0-dev", "0.1.0", 1},
		{"0.1.0", "0.1.0-dev", -1},
		{"0.1.0+apt1", "0.1.0", 1},
		{"0.1.0", "0.1.0+apt1", -1},
		{"0.1.0+apt1", "0.1.0+apt2", -1},
		{"1:0.1.0", "0.2.0", 1},
		{"0.9.0", "0.10.0", -1},
		// Feature tags like +vlan1 sort after +upd3; bump the numeric version to supersede them.
		{"0.1.0+vlan1", "0.1.0+upd3", 1},
		{"0.1.0+vlan1", "0.1.1", -1},
		{"0.1.0+console2", "0.1.1", -1},
		{"1.0~rc1", "1.0", -1},
		{"1.0-1", "1.0", 1},
		{"1.0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000002", "1.1", 1},
	}
	exits := make(map[string]int)
	for _, c := range cases {
		exits["dpkg --compare-versions "+c.a+" lt "+c.b] = 1
		exits["dpkg --compare-versions "+c.a+" gt "+c.b] = 1
		if c.want < 0 {
			exits["dpkg --compare-versions "+c.a+" lt "+c.b] = 0
		} else if c.want > 0 {
			exits["dpkg --compare-versions "+c.a+" gt "+c.b] = 0
		}
	}
	calls := fakeCommands(t, func(line string) (string, int) {
		code, ok := exits[line]
		if !ok {
			t.Fatalf("unexpected command %q", line)
		}
		return "", code
	})
	for _, c := range cases {
		if got, err := CompareVersions(context.Background(), c.a, c.b); err != nil || got != c.want {
			t.Fatalf("CompareVersions(%q, %q)=(%d, %v) want %d", c.a, c.b, got, err, c.want)
		}
	}
	if got := strings.Count(joined(calls), "dpkg --compare-versions"); got < len(cases) {
		t.Fatalf("native dpkg comparator was not used for every case:\n%s", joined(calls))
	}
}

func TestCompareVersionsReturnsCommandFailures(t *testing.T) {
	fakeCommands(t, func(line string) (string, int) {
		if strings.HasPrefix(line, "dpkg --compare-versions") {
			return "bad version", 2
		}
		return "", 0
	})
	if _, err := CompareVersions(context.Background(), "bad:1", "1.0"); err == nil {
		t.Fatal("invalid dpkg comparison succeeded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompareVersions(ctx, "1.0", "1.1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestCompareVersionsReturnsStartFailure(t *testing.T) {
	previous := Command
	Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/nonexistent/dpkg")
	}
	t.Cleanup(func() { Command = previous })
	if _, err := CompareVersions(context.Background(), "1.0", "1.1"); err == nil {
		t.Fatal("missing dpkg succeeded")
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	if got := NormalizeRepoURL("http://10.0.0.1:8080/apt/"); got != defaultRepoURL {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeRepoURL("http://127.0.0.1:8081"); got != defaultRepoURL {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeRepoURL("https://mirror.example/apt/"); got != "https://mirror.example/apt" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("NEX_UPDATE_REPO", "http://192.168.1.10/apt")
	if got := DefaultRepoURL(); got != defaultRepoURL {
		t.Fatalf("DefaultRepoURL=%q", got)
	}
}

// The apt source is what actually authenticates downloads, so it must name the
// packaged keyring and follow the configured repo URL.
func TestWriteAptSourceTracksRepoURL(t *testing.T) {
	isolatePaths(t)
	writeAptSource("https://mirror.example/apt/")
	b, err := os.ReadFile(AptSourcePath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "URIs: https://mirror.example/apt\n") {
		t.Fatalf("repo URL not written: %q", got)
	}
	if !strings.Contains(got, "Signed-By: "+aptKeyringPath) {
		t.Fatalf("keyring not pinned: %q", got)
	}
	writeAptSource("https://other.example/apt")
	b, _ = os.ReadFile(AptSourcePath)
	if !strings.Contains(string(b), "URIs: https://other.example/apt\n") {
		t.Fatalf("repo change not applied: %q", b)
	}
}

func TestCheckReportsAptCandidate(t *testing.T) {
	isolatePaths(t)
	calls := fakeCommands(t, func(line string) (string, int) {
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return "0.1.0", 0
		case strings.HasPrefix(line, "apt-cache policy"):
			return "nex-core-local:\n  Installed: 0.1.0\n  Candidate: 0.2.0\n", 0
		}
		return "", 0
	})

	st, err := Check(context.Background(), "https://mirror.example/apt/", "nex-core-local")
	if err != nil {
		t.Fatal(err)
	}
	if st.Error != "" || !st.UpdateAvailable {
		t.Fatalf("%+v", st)
	}
	if st.InstalledVersion != "0.1.0" || st.AvailableVersion != "0.2.0" {
		t.Fatalf("%+v", st)
	}
	if st.RepoURL != "https://mirror.example/apt" {
		t.Fatalf("repo %q", st.RepoURL)
	}
	// The candidate must come from apt, never from a separate index download.
	if !strings.Contains(joined(calls), "apt-get -q") {
		t.Fatalf("expected an index refresh, got:\n%s", joined(calls))
	}
	if strings.Contains(joined(calls), "updates.json") {
		t.Fatalf("check still fetches a bespoke index:\n%s", joined(calls))
	}
}

// A package no configured source offers must read as an error, not as "up to date".
func TestCheckWithoutCandidate(t *testing.T) {
	isolatePaths(t)
	fakeCommands(t, func(line string) (string, int) {
		if strings.HasPrefix(line, "apt-cache policy") {
			return "N: Unable to locate package nex-core-local\n", 0
		}
		return "0.1.0", 0
	})
	st, err := Check(context.Background(), "https://mirror.example/apt", "nex-core-local")
	if err != nil {
		t.Fatal(err)
	}
	if st.Error == "" || st.UpdateAvailable || st.AvailableVersion != "" {
		t.Fatalf("%+v", st)
	}
}

// apt-cache prints "Candidate: (none)" for a known package with no installable
// version; that is not an available update.
func TestCheckCandidateNone(t *testing.T) {
	isolatePaths(t)
	fakeCommands(t, func(line string) (string, int) {
		if strings.HasPrefix(line, "apt-cache policy") {
			return "nex-core-local:\n  Installed: 0.1.0\n  Candidate: (none)\n", 0
		}
		return "0.1.0", 0
	})
	st, _ := Check(context.Background(), "https://mirror.example/apt", "nex-core-local")
	if st.Error == "" || st.UpdateAvailable {
		t.Fatalf("%+v", st)
	}
}

func TestCheckReportsComparatorFailure(t *testing.T) {
	isolatePaths(t)
	fakeCommands(t, func(line string) (string, int) {
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return "0.1.0", 0
		case strings.HasPrefix(line, "apt-cache policy"):
			return "Candidate: 0.2.0\n", 0
		case strings.HasPrefix(line, "dpkg --compare-versions"):
			return "invalid version", 2
		}
		return "", 0
	})
	st, err := Check(context.Background(), "https://mirror.example/apt", "nex-core-local")
	if err != nil || st.Error != "could not compare package versions" || st.UpdateAvailable {
		t.Fatalf("%v %+v", err, st)
	}
}

func TestCheckWithCacheUsesServiceOwnedAptLists(t *testing.T) {
	calls := fakeCommands(t, func(line string) (string, int) {
		if strings.HasPrefix(line, "apt-cache policy nex-controller") {
			return "Candidate: 0.2.0\n", 0
		}
		return "", 0
	})
	st, err := CheckWithCache(context.Background(), "https://mirror.example/apt", "nex-controller", t.TempDir())
	if err != nil || st.Error != "" || !st.UpdateAvailable || st.AvailableVersion != "0.2.0" {
		t.Fatalf("%v %+v", err, st)
	}
	if !strings.Contains(joined(calls), "Dir::State::lists=") || !strings.Contains(joined(calls), "Dir::Etc::sourceparts=") {
		t.Fatalf("Controller cache paths missing:\n%s", joined(calls))
	}
}

// The index refresh is rate limited because the UI polls Check while an update runs.
func TestRefreshIndexHonoursStamp(t *testing.T) {
	isolatePaths(t)
	calls := fakeCommands(t, func(string) (string, int) { return "", 0 })
	refreshIndex(context.Background())
	first := len(*calls)
	if first == 0 {
		t.Fatal("expected an initial refresh")
	}
	refreshIndex(context.Background())
	if len(*calls) != first {
		t.Fatalf("refreshed twice inside indexMaxAge:\n%s", joined(calls))
	}
	stale := time.Now().Add(-indexMaxAge - time.Minute)
	if err := os.Chtimes(IndexStampPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	refreshIndex(context.Background())
	if len(*calls) == first {
		t.Fatal("expected a refresh once the stamp aged out")
	}
}

// A failed refresh must not stamp success, or a transient outage would suppress
// refreshes for the whole indexMaxAge window.
func TestRefreshIndexFailureLeavesNoStamp(t *testing.T) {
	isolatePaths(t)
	fakeCommands(t, func(string) (string, int) { return "could not resolve host", 1 })
	refreshIndex(context.Background())
	if _, err := os.Stat(IndexStampPath); !os.IsNotExist(err) {
		t.Fatalf("stamp written despite failure: %v", err)
	}
}

// CheckWithCache stamps a failed refresh so GET /api/v1/updates does not run apt-get update
// twice when it then checks nex-core-local against the same unreachable archive.
func TestCheckWithCacheFailedRefreshStampsAttempt(t *testing.T) {
	calls := fakeCommands(t, func(line string) (string, int) {
		if strings.Contains(line, "apt-get") && strings.Contains(line, "update") {
			return "could not resolve host", 1
		}
		if strings.HasPrefix(line, "apt-cache policy") {
			return "Candidate: (none)\n", 0
		}
		return "", 0
	})
	cache := t.TempDir()
	st, err := CheckWithCache(context.Background(), "https://mirror.example/apt", "nex-controller", cache)
	if err != nil || st.Error == "" {
		t.Fatalf("expected a refresh error status: %v %+v", err, st)
	}
	stamp := filepath.Join(cache, "apt-refresh.stamp")
	if _, err := os.Stat(stamp); err != nil {
		t.Fatalf("failed refresh left no stamp: %v", err)
	}
	updates := 0
	for _, line := range *calls {
		if strings.Contains(line, "apt-get") && strings.Contains(line, "update") {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("first CheckWithCache ran apt-get update %d times:\n%s", updates, joined(calls))
	}
	if _, err := CheckWithCache(context.Background(), "https://mirror.example/apt", "nex-core-local", cache); err != nil {
		t.Fatal(err)
	}
	updates = 0
	for _, line := range *calls {
		if strings.Contains(line, "apt-get") && strings.Contains(line, "update") {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("second CheckWithCache refreshed again:\n%s", joined(calls))
	}
}

// The whole point of the change: upgrades go through apt so dependencies resolve,
// and never through dpkg -i, which cannot satisfy them.
func TestApplyLocalUpgradesViaApt(t *testing.T) {
	isolatePaths(t)
	installed := "0.1.0"
	calls := fakeCommands(t, func(line string) (string, int) {
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return installed, 0
		case strings.Contains(line, "install -y nex-core-local"):
			installed = "0.2.0"
			return "Setting up nex-core-local (0.2.0)", 0
		}
		return "", 0
	})

	res, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-core-local", t.TempDir())
	if err != nil {
		t.Fatalf("%v (%+v)", err, res)
	}
	if res.Status != "scheduled" {
		t.Fatalf("%+v", res)
	}
	got := joined(calls)
	if !strings.Contains(got, "apt-get -q -o Dpkg::Options::=--force-confold -o DPkg::Lock::Timeout=60 install -y nex-core-local") {
		t.Fatalf("apt install not invoked:\n%s", got)
	}
	if strings.Contains(got, "--only-upgrade") {
		t.Fatalf("still using --only-upgrade (skips binary-only appliances):\n%s", got)
	}
	if strings.Contains(got, "dpkg -i") {
		t.Fatalf("still installing a bare .deb:\n%s", got)
	}
	if !strings.Contains(got, "systemctl restart nex-core-local.service") {
		t.Fatalf("service not restarted:\n%s", got)
	}
}

// OpenZFS is recommended, not depended on, so an upgrade from a release whose host
// has no contrib component still succeeds. The apply that follows must install the
// recommended packages that are missing, and must leave installed ones alone.
func TestApplyLocalInstallsMissingRecommendedPackages(t *testing.T) {
	isolatePaths(t)
	installed := "0.1.0"
	calls := fakeCommands(t, func(line string) (string, int) {
		switch {
		case strings.HasPrefix(line, "dpkg-query -W -f=${db:Status-Status} zfsutils-linux"):
			return "installed", 0
		case strings.HasPrefix(line, "dpkg-query -W -f=${db:Status-Status}"):
			return "", 1
		case strings.HasPrefix(line, "dpkg-query"):
			return installed, 0
		case strings.Contains(line, "install -y nex-core-local"):
			installed = "0.2.0"
			return "Setting up nex-core-local (0.2.0)", 0
		}
		return "", 0
	})

	if res, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-core-local", t.TempDir()); err != nil {
		t.Fatalf("%v (%+v)", err, res)
	}
	got := joined(calls)
	if !strings.Contains(got, "install -y linux-headers-amd64 zfs-dkms\n") &&
		!strings.HasSuffix(got, "install -y linux-headers-amd64 zfs-dkms") {
		t.Fatalf("missing recommended packages not installed (or an installed one reinstalled):\n%s", got)
	}
	if !strings.Contains(got, "systemctl restart nex-core-local.service") {
		t.Fatalf("service not restarted:\n%s", got)
	}
}

// A read-only-root host: dpkg only ever runs between a remount rw and a remount ro,
// and the root is made read-only again whether or not the install worked.
func TestApplyLocalOpensRootWindowOnReadOnlyRoot(t *testing.T) {
	for _, installFails := range []bool{false, true} {
		isolatePaths(t)
		setRootOptions(t, "ro,relatime")
		installed := "0.1.0"
		calls := fakeCommands(t, func(line string) (string, int) {
			switch {
			case strings.HasPrefix(line, "dpkg-query"):
				return installed, 0
			case strings.Contains(line, "install -y nex-core-local"):
				if installFails {
					return "E: unable to write", 100
				}
				installed = "0.2.0"
			}
			return "", 0
		})
		_, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-core-local", t.TempDir())
		if (err != nil) != installFails {
			t.Fatalf("installFails=%v: %v", installFails, err)
		}
		var order []string
		for _, line := range *calls {
			switch {
			case line == "mount -o remount,rw /":
				order = append(order, "rw")
			case strings.Contains(line, "install -y nex-core-local"):
				order = append(order, "install")
			case line == "mount -o remount,ro /":
				order = append(order, "ro")
			}
		}
		if got := strings.Join(order, ","); got != "rw,install,ro" {
			t.Fatalf("installFails=%v: root window order %q:\n%s", installFails, got, joined(calls))
		}
	}
}

// The package install shares one window lock with every other root writer (the BIOS
// bootloader write of a disk replacement): it waits for an open window instead of treating
// that window's writable root as the normal state and being made read-only underneath itself.
func TestApplyLocalWaitsForAnotherRootWindow(t *testing.T) {
	isolatePaths(t)
	installed := "0.1.0"
	var mu sync.Mutex
	calls := fakeCommands(t, func(line string) (string, int) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return installed, 0
		case strings.Contains(line, "install -y nex-core-local"):
			installed = "0.2.0"
		}
		return "", 0
	})
	closeOther, err := rootfs.OpenWritable(context.Background(), func(context.Context, string, ...string) ([]byte, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-core-local", t.TempDir())
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("the install ran inside another writer's window")
	case <-time.After(150 * time.Millisecond):
	}
	mu.Lock()
	early := strings.Contains(joined(calls), "install -y nex-core-local")
	mu.Unlock()
	if early {
		t.Fatal("apt install started before the other window closed")
	}
	closeOther()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// If / cannot be made writable nothing is installed: dpkg would fail half way through
// unpacking instead.
func TestApplyLocalRefusesToInstallWhenRootStaysReadOnly(t *testing.T) {
	isolatePaths(t)
	setRootOptions(t, "ro,relatime")
	calls := fakeCommands(t, func(line string) (string, int) {
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return "0.1.0", 0
		case line == "mount -o remount,rw /":
			return "mount: permission denied", 32
		}
		return "", 0
	})
	dir := t.TempDir()
	res, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-core-local", dir)
	if err == nil || res.Status != "failed" || !strings.Contains(res.Message, "permission denied") {
		t.Fatalf("%+v %v", res, err)
	}
	if got := joined(calls); strings.Contains(got, "install -y") || strings.Contains(got, "remount,ro") {
		t.Fatalf("installed or remounted after a failed window:\n%s", got)
	}
	if p, ok := ReadProgress(dir); !ok || p.Phase != "failed" {
		t.Fatalf("progress %+v", p)
	}
}

// A writable root (older hosts, the Controller appliance) is never remounted.
func TestApplyLocalLeavesWritableRootAlone(t *testing.T) {
	isolatePaths(t)
	installed := "0.1.0"
	calls := fakeCommands(t, func(line string) (string, int) {
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return installed, 0
		case strings.Contains(line, "install -y nex-core-local"):
			installed = "0.2.0"
		}
		return "", 0
	})
	if _, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-core-local", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(joined(calls), "mount -o") {
		t.Fatalf("remounted a writable root:\n%s", joined(calls))
	}
}

// A Controller apply must not drag a host's ZFS packages onto the appliance.
func TestApplyLocalControllerSkipsCoreRecommendations(t *testing.T) {
	isolatePaths(t)
	installed := "0.1.0"
	calls := fakeCommands(t, func(line string) (string, int) {
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return installed, 0
		case strings.Contains(line, "install -y nex-controller"):
			installed = "0.2.0"
			return "Setting up nex-controller (0.2.0)", 0
		}
		return "", 0
	})
	if res, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-controller", t.TempDir()); err != nil {
		t.Fatalf("%v (%+v)", err, res)
	}
	if strings.Contains(joined(calls), "zfs-dkms") {
		t.Fatalf("controller apply touched ZFS packages:\n%s", joined(calls))
	}
}

// apt exits 0 when nothing needed upgrading, so the version must be re-read rather
// than reporting a successful update that never happened.
func TestApplyLocalRejectsUnchangedVersion(t *testing.T) {
	isolatePaths(t)
	fakeCommands(t, func(line string) (string, int) {
		if strings.HasPrefix(line, "dpkg-query") {
			return "0.1.0", 0
		}
		if strings.HasPrefix(line, "dpkg --compare-versions") {
			return "", 1
		}
		return "nex-core-local is already the newest version (0.1.0).", 0
	})
	dir := t.TempDir()
	res, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-core-local", dir)
	if err == nil || res.Status != "failed" {
		t.Fatalf("expected failure, got %+v (%v)", res, err)
	}
	prog, ok := ReadProgress(dir)
	if !ok || prog.Phase != "failed" || prog.Error == "" {
		t.Fatalf("%+v %v", prog, ok)
	}
}

// A failing apt run must surface apt's own output; that text is all an operator sees.
func TestApplyLocalReportsAptFailure(t *testing.T) {
	isolatePaths(t)
	fakeCommands(t, func(line string) (string, int) {
		if strings.Contains(line, "install -y nex-core-local") {
			return "E: Unable to correct problems, you have held broken packages.", 1
		}
		if strings.HasPrefix(line, "dpkg-query") {
			return "0.1.0", 0
		}
		return "", 0
	})
	dir := t.TempDir()
	res, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-core-local", dir)
	if err == nil || res.Status != "failed" {
		t.Fatalf("expected failure, got %+v", res)
	}
	if !strings.Contains(res.Message, "held broken packages") {
		t.Fatalf("apt output not surfaced: %q", res.Message)
	}
	prog, _ := ReadProgress(dir)
	if !strings.Contains(prog.Error, "held broken packages") {
		t.Fatalf("progress lost the error: %+v", prog)
	}
}

func TestApplyLocalRejectsComparatorFailure(t *testing.T) {
	isolatePaths(t)
	installed := "0.1.0"
	fakeCommands(t, func(line string) (string, int) {
		switch {
		case strings.HasPrefix(line, "dpkg-query"):
			return installed, 0
		case strings.Contains(line, "install -y nex-core-local"):
			installed = "0.2.0"
			return "", 0
		case strings.HasPrefix(line, "dpkg --compare-versions"):
			return "invalid version", 2
		}
		return "", 0
	})
	res, err := ApplyLocal(context.Background(), "https://mirror.example/apt", "nex-core-local", t.TempDir())
	if err == nil || res.Status != "failed" || res.Message != "could not compare package versions" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRequestApply(t *testing.T) {
	dir := t.TempDir()
	if err := RequestApply(dir, "http://example.test/apt/", "nex-controller"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "pending-update.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p pendingUpdate
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.Package != "nex-controller" || p.RepoURL != "http://example.test/apt" || p.RequestedAt == "" {
		t.Fatalf("%+v", p)
	}
	prog, ok := ReadProgress(dir)
	if !ok || prog.Phase != "downloading" {
		t.Fatalf("%+v %v", prog, ok)
	}
}

func TestMergeProgressStaleInstalling(t *testing.T) {
	dir := t.TempDir()
	if err := WriteProgress(dir, Progress{Phase: "installing", Percent: 90, Message: "Installing package"}); err != nil {
		t.Fatal(err)
	}
	st := Status{InstalledVersion: "0.1.2", AvailableVersion: "0.1.2", UpdateAvailable: false}
	MergeProgress(&st, dir)
	if st.Phase != "done" || st.Progress == nil || *st.Progress != 100 {
		t.Fatalf("%+v", st)
	}
	prog, ok := ReadProgress(dir)
	if !ok || prog.Phase != "done" {
		t.Fatalf("%+v %v", prog, ok)
	}
}

// Only a restarted process can be stuck mid-install. While this one is still applying,
// apt reports the new version as soon as dpkg unpacks it, and repairing the phase then
// would announce a finished update over an apply that has not restarted the service yet.
func TestMergeProgressLeavesARunningApplyAlone(t *testing.T) {
	isolatePaths(t)
	gate := make(chan struct{})
	fakeCommands(t, func(line string) (string, int) {
		if strings.Contains(line, "install -y") {
			<-gate
		}
		if strings.HasPrefix(line, "dpkg-query") {
			return "9.9.9", 0
		}
		return "", 0
	})
	dir := t.TempDir()
	if _, err := StartApplyLocal("https://mirror.test/apt", "nex-core-local", dir); err != nil {
		t.Fatal(err)
	}
	defer func() { close(gate); WaitApply() }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if p, ok := ReadProgress(dir); ok && p.Phase == "installing" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the apply never reached installing")
		}
	}
	st := Status{InstalledVersion: "9.9.9", AvailableVersion: "9.9.9"}
	MergeProgress(&st, dir)
	if st.Phase != "installing" {
		t.Fatalf("apply still running, reported %+v", st)
	}
}
