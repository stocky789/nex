// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitProbe(t *testing.T, m *Monitor, name, status string) ProbeResult {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		for _, r := range m.Results() {
			if r.Name == name && r.Status == status {
				return r
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("probe %s never became %s: %+v", name, status, m.Results())
	return ProbeResult{}
}

func TestStuckProbeDoesNotBlockOrOverlap(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var stuckCalls, goodCalls atomic.Int32
	m := NewMonitor(10*time.Millisecond, map[string]Probe{
		"stuck":     func(context.Context) (any, error) { stuckCalls.Add(1); <-release; return "late", nil },
		"inventory": func(context.Context) (any, error) { goodCalls.Add(1); return NativeInventory{Hostname: "lab"}, nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Tick(ctx)
	waitProbe(t, m, "inventory", "healthy")
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 20; i++ {
		m.Tick(ctx)
		time.Sleep(time.Millisecond)
	}
	r := waitProbe(t, m, "stuck", "unknown")
	if stuckCalls.Load() != 1 || goodCalls.Load() < 2 {
		t.Fatalf("workers stuck=%d good=%d", stuckCalls.Load(), goodCalls.Load())
	}
	if r.Data != nil || r.Message != "probe deadline exceeded; previous observation retained" {
		t.Fatalf("late result published: %+v", r)
	}
	if m.Snapshot().Hostname != "lab" {
		t.Fatal("healthy inventory unavailable")
	}
}

func TestFailedProbeRetainsObservationWithoutClaimingFreshSuccess(t *testing.T) {
	var calls atomic.Int32
	m := NewMonitor(time.Second, map[string]Probe{"inventory": func(context.Context) (any, error) {
		if calls.Add(1) > 1 {
			return nil, errors.New("unavailable")
		}
		return NativeInventory{Hostname: "lab"}, nil
	}})
	m.Tick(context.Background())
	first := waitProbe(t, m, "inventory", "healthy")
	m.Tick(context.Background())
	failed := waitProbe(t, m, "inventory", "failed")
	snapshot := m.Snapshot()
	if !failed.LastSuccess.Equal(first.LastSuccess) || !snapshot.ObservedAt.Equal(first.LastSuccess) || snapshot.Hostname != "lab" {
		t.Fatalf("lost stale observation: %+v", snapshot)
	}
	snapshot.Checks[0].Status = "corrupted"
	if m.Snapshot().Checks[0].Status != "failed" {
		t.Fatal("snapshot aliases mutable state")
	}
}

func TestSnapshotRedactsUnsafeProbeFailureButKeepsSafeReason(t *testing.T) {
	m := NewMonitor(time.Second, map[string]Probe{
		"libvirt": func(context.Context) (any, error) {
			return nil, errors.New("connect qemu+ssh://operator:private-token@host.example.test/system: permission denied")
		},
		"block-disks": func(context.Context) (any, error) {
			return nil, publicProbeError("block device observation limit exceeded (256)")
		},
	})
	m.Tick(context.Background())
	waitProbe(t, m, "libvirt", "failed")
	waitProbe(t, m, "block-disks", "failed")
	checks := m.Snapshot().Checks
	if checks[0].Error != "block device observation limit exceeded (256)" {
		t.Fatalf("safe reason was not retained: %+v", checks[0])
	}
	if checks[1].Error != "local libvirt is unavailable" || strings.Contains(checks[1].Error, "private-token") || strings.Contains(checks[1].Error, "host.example.test") {
		t.Fatalf("unsafe libvirt failure reached inventory: %+v", checks[1])
	}
}

func TestLateSuccessDiscarded(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	m := NewMonitor(5*time.Millisecond, map[string]Probe{"late": func(context.Context) (any, error) { close(started); <-release; return "obsolete", nil }})
	m.Tick(context.Background())
	<-started
	time.Sleep(10 * time.Millisecond)
	if r := m.Results()[0]; r.Status != "unknown" || r.Message == "not yet observed" {
		t.Fatalf("deadline not reflected without another tick: %+v", r)
	}
	close(release)
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		m.mu.Lock()
		busy := m.workers["late"].busy
		m.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(time.Millisecond)
	}
	r := m.Results()[0]
	if r.Status != "unknown" || r.Data != nil || !r.LastSuccess.IsZero() {
		t.Fatalf("late success was published: %+v", r)
	}
}

func TestVirshOutputIsBounded(t *testing.T) {
	var output cappedOutput
	if _, err := output.Write(make([]byte, 1024*1024)); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Write([]byte("x")); err == nil {
		t.Fatal("unbounded subprocess output accepted")
	}
	if output.Len() != 1024*1024 {
		t.Fatal("output grew beyond limit")
	}
}

func TestCPUModelsAreValidatedSortedAndDistinct(t *testing.T) {
	models, err := cpuModels("qemu64\nSkylake-Server\nqemu64\n")
	if err != nil || !reflect.DeepEqual(models, []string{"Skylake-Server", "qemu64"}) {
		t.Fatalf("models=%q err=%v", models, err)
	}
	if _, err := cpuModels("bad/model"); err == nil {
		t.Fatal("unsafe CPU model accepted")
	}
}

func TestCPUModelProbeUsesNormalConnectionWhileOtherVirshQueriesStayReadonly(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "virsh-calls")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NEX_VIRSH_CALLS"
case " $* " in
  *" domcapabilities "*)
    case " $* " in
      *" --readonly "*) echo 'operation forbidden: read only access prevents virConnectGetDomainCapabilities' >&2; exit 41 ;;
    esac
    printf '%s\n' '<domainCapabilities><cpu><mode name="custom"><model usable="yes">qemu64</model></mode></cpu></domainCapabilities>'
    ;;
  *" list --all --uuid "*)
    case " $* " in
      *" --readonly "*) : ;;
      *) echo 'list query was not readonly' >&2; exit 42 ;;
    esac
    ;;
  *) echo "unexpected virsh arguments: $*" >&2; exit 43 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NEX_VIRSH_CALLS", calls)

	probes := NativeProbes("test", nil)
	data, err := probes["cpu-models"](context.Background())
	if err != nil {
		t.Fatalf("CPU model probe failed: %v", err)
	}
	if got, want := data, any([]string{"qemu64"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("CPU models = %#v, want %#v", got, want)
	}
	if _, err := probes["libvirt"](context.Background()); err != nil {
		t.Fatalf("readonly libvirt probe failed: %v", err)
	}

	lines := strings.FieldsFunc(string(mustReadFile(t, calls)), func(r rune) bool { return r == '\n' })
	if len(lines) != 2 {
		t.Fatalf("virsh calls = %q", lines)
	}
	if strings.Contains(lines[0], "--readonly") || !strings.Contains(lines[0], "--connect qemu:///system domcapabilities") {
		t.Fatalf("domcapabilities connection = %q", lines[0])
	}
	if !strings.Contains(lines[1], "--readonly --connect qemu:///system list --all --uuid") {
		t.Fatalf("list connection lost readonly mode: %q", lines[1])
	}
}

func TestCPUModelProbeFailsClosedOnInvalidCapabilities(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case " $* " in
  *" domcapabilities "*) printf '%s\n' '<domainCapabilities><cpu><mode name="custom"><model usable="yes">bad/model</model></mode></cpu></domainCapabilities>' ;;
  *) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := NativeProbes("test", nil)["cpu-models"](context.Background()); err == nil {
		t.Fatal("invalid domain capabilities were accepted")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFailedCPUModelProbeIsNotPresentedAsCurrent(t *testing.T) {
	var calls atomic.Int32
	m := NewMonitor(time.Second, map[string]Probe{"cpu-models": func(context.Context) (any, error) {
		if calls.Add(1) == 1 {
			return []string{"qemu64"}, nil
		}
		return nil, errors.New("libvirt unavailable")
	}})
	m.Tick(context.Background())
	waitProbe(t, m, "cpu-models", "healthy")
	if got := m.Snapshot().CPUModels; !reflect.DeepEqual(got, []string{"qemu64"}) {
		t.Fatalf("current models=%q", got)
	}
	m.Tick(context.Background())
	waitProbe(t, m, "cpu-models", "failed")
	if got := m.Snapshot().CPUModels; got != nil {
		t.Fatalf("stale models presented as current: %q", got)
	}
}

func TestObservationTimestampPredatesInFlightRefresh(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{})
	m := NewMonitor(time.Second, map[string]Probe{"inventory": func(context.Context) (any, error) {
		close(started)
		<-release
		return NativeInventory{Hostname: "lab"}, nil
	}})
	m.Tick(context.Background())
	<-started
	requestedAt := time.Now().UTC()
	close(release)
	waitProbe(t, m, "inventory", "healthy")
	if m.Snapshot().ObservedAt.After(requestedAt) {
		t.Fatal("pre-request sample falsely satisfies fresh inventory request")
	}
}

func TestBlockDisksSizeAndInvalidInput(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sda"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "sda", "size")
	if err := os.WriteFile(path, []byte("2048\n"), 0600); err != nil {
		t.Fatal(err)
	}
	disks, err := readBlockDisks(context.Background(), root)
	if err != nil || len(disks) != 1 || disks[0].Name != "/dev/sda" || disks[0].SizeBytes != 1048576 {
		t.Fatalf("disks=%+v err=%v", disks, err)
	}
	if err := os.WriteFile(path, []byte("18446744073709551615"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBlockDisks(context.Background(), root); err == nil {
		t.Fatal("overflow accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readBlockDisks(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestEventsTriggerCoalescedObservation(t *testing.T) {
	var calls atomic.Int32
	m := NewMonitor(time.Second, map[string]Probe{"inventory": func(context.Context) (any, error) { calls.Add(1); return NativeInventory{}, nil }})
	output := &eventOutput{ctx: context.Background(), monitor: m}
	if n, err := output.Write([]byte("event\n")); n != 6 || err != nil {
		t.Fatalf("write %d %v", n, err)
	}
	waitProbe(t, m, "inventory", "healthy")
	for i := 0; i < 100; i++ {
		_, _ = output.Write([]byte("event\n"))
	}
	if calls.Load() != 1 {
		t.Fatal("event storm was not coalesced")
	}
	if n, err := output.Write(make([]byte, 1024*1024)); n != 1024*1024 || err != nil {
		t.Fatalf("discard long event: %d %v", n, err)
	}
}

func TestEventWatcherCancelsAndReapsProcess(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	m := NewMonitor(time.Second, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.WatchLibvirt(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event subprocess was not reaped on cancellation")
	}
}

func TestVirtualizationInventoryExcludesUnrelatedCPUFlags(t *testing.T) {
	flags := make([]string, 200)
	for i := range flags {
		flags[i] = "unrelated"
	}
	flags = append(flags, "vmx", "ept", "vpid", "svm", "npt", "nrip_save", "avic", "sse2", "aes")
	m := NewMonitor(time.Second, map[string]Probe{"inventory": func(context.Context) (any, error) { return NativeInventory{CPUFlags: flags}, nil }})
	m.Tick(context.Background())
	waitProbe(t, m, "inventory", "healthy")
	want := []string{"vmx", "ept", "vpid", "svm", "npt", "nrip_save", "avic"}
	if got := m.Snapshot().Virtualization; !reflect.DeepEqual(got, want) {
		t.Fatalf("virtualization = %v, want %v", got, want)
	}
}

func TestSnapshotAttachesGuestObservationsToRunningVMsOnly(t *testing.T) {
	guest := &protocol.GuestInfo{Agent: "connected", Hostname: "g1", Clipboard: "ready", Resize: true}
	m := NewMonitor(time.Second, map[string]Probe{
		"libvirt": func(context.Context) (any, error) {
			return []protocol.VM{{ID: "A", Name: "one", State: "running"}, {ID: "B", Name: "two", State: "shut off"}}, nil
		},
		"guest": func(context.Context) (any, error) {
			return []guestObservation{{"a", guest}, {"B", &protocol.GuestInfo{Agent: "connected"}}}, nil
		},
	})
	m.Tick(context.Background())
	waitProbe(t, m, "libvirt", "healthy")
	waitProbe(t, m, "guest", "healthy")
	vms := m.Snapshot().VMs
	if len(vms) != 2 || !reflect.DeepEqual(vms[0].Guest, guest) || vms[1].Guest != nil {
		t.Fatalf("guest attachment wrong: %+v", vms)
	}
	// A guest probe that later fails keeps its retained data out of the snapshot.
	fail := NewMonitor(50*time.Millisecond, map[string]Probe{
		"libvirt": func(context.Context) (any, error) {
			return []protocol.VM{{ID: "A", Name: "one", State: "running"}}, nil
		},
		"guest": func(ctx context.Context) (any, error) { <-ctx.Done(); return []guestObservation{{"A", guest}}, nil },
	})
	fail.Tick(context.Background())
	waitProbe(t, fail, "libvirt", "healthy")
	time.Sleep(80 * time.Millisecond)
	if vms := fail.Snapshot().VMs; len(vms) != 1 || vms[0].Guest != nil {
		t.Fatalf("stale guest data attached: %+v", vms)
	}
}

func TestCPUBaselineProbeDeadlineAndFailedObservation(t *testing.T) {
	var calls atomic.Int32
	m := NewMonitor(time.Millisecond, map[string]Probe{"cpu-baseline": func(ctx context.Context) (any, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 29*time.Second {
			return nil, errors.New("CPU discovery received ordinary short probe deadline")
		}
		if calls.Add(1) > 1 {
			return nil, errors.New("discovery failed")
		}
		return protocol.CPUBaseline{Model: "FutureCPU-v1", Features: []protocol.CPUFeature{{Name: "feature", Policy: "require"}}}, nil
	}})
	m.Tick(context.Background())
	waitProbe(t, m, "cpu-baseline", "healthy")
	snapshot := m.Snapshot()
	if snapshot.CPUBaseline == nil {
		t.Fatal("baseline missing")
	}
	snapshot.CPUBaseline.Features[0].Name = "changed"
	if m.Snapshot().CPUBaseline.Features[0].Name != "feature" {
		t.Fatal("snapshot aliases cached feature list")
	}
	m.Tick(context.Background())
	waitProbe(t, m, "cpu-baseline", "failed")
	if m.Snapshot().CPUBaseline != nil {
		t.Fatal("failed discovery retained successful baseline")
	}
}

func TestNICSpeedAndUptimeParsing(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if got := nicSpeed(write("speed", "1000\n")); got != 1000 {
		t.Fatalf("speed = %d", got)
	}
	if got := nicSpeed(write("virtual", "-1\n")); got != 0 {
		t.Fatalf("unknown speed = %d", got)
	}
	if got := nicSpeed(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("missing speed = %d", got)
	}
	if got := uptimeSeconds(write("uptime", "12345.67 54321.00\n")); got != 12345 {
		t.Fatalf("uptime = %d", got)
	}
	if got := uptimeSeconds(filepath.Join(dir, "missing")); got != 0 {
		t.Fatalf("missing uptime = %d", got)
	}
	m := NewMonitor(time.Second, map[string]Probe{"inventory": func(context.Context) (any, error) {
		return NativeInventory{UptimeSeconds: 12345, NICs: []NativeNIC{{Name: "nex0", SpeedMbps: 1000}}}, nil
	}})
	m.Tick(context.Background())
	waitProbe(t, m, "inventory", "healthy")
	if inv := m.Snapshot(); inv.UptimeSeconds != 12345 || len(inv.NICs) != 1 || inv.NICs[0].SpeedMbps != 1000 {
		t.Fatalf("inventory = %+v", inv)
	}
}
