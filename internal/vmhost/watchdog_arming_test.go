// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// labSBD stands in for systemd and for /etc/default/sbd, and records every command, so a test
// can assert what was and was not run. active is what `systemctl show ActiveState` answers.
func labSBD(t *testing.T, loaded bool, active *bool, noPacemaker ...bool) *[]string {
	t.Helper()
	calls := &[]string{}
	pacemaker := !(len(noPacemaker) > 0 && noPacemaker[0])
	labPolicy, labEnabled = "suicide", true
	previousRun, previousPath := clusterRun, sbdConfigPath
	sbdConfigPath = filepath.Join(t.TempDir(), "sbd")
	clusterRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(append([]string{name}, args...), " "))
		switch {
		case len(args) > 0 && args[0] == "show" && strings.Contains(strings.Join(args, " "), "LoadState"):
			if loaded {
				return []byte("loaded\n"), nil
			}
			return []byte("not-found\n"), nil
		case len(args) > 0 && args[0] == "show" && strings.Contains(strings.Join(args, " "), "UnitFileState"):
			if labEnabled {
				return []byte("enabled\n"), nil
			}
			return []byte("disabled\n"), nil
		case len(args) > 0 && args[0] == "show":
			if strings.Contains(strings.Join(args, " "), "pacemaker") {
				if pacemaker {
					return []byte("active\n"), nil
				}
				return []byte("inactive\n"), nil
			}
			if *active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), nil
		case len(args) > 0 && args[0] == "enable":
			labEnabled = true
		case len(args) > 0 && args[0] == "disable":
			labEnabled = false
		case len(args) > 0 && args[0] == "kill":
			*active = false
		}
		if name == "crm_attribute" {
			return []byte(labPolicy + "\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { clusterRun, sbdConfigPath = previousRun, previousPath })
	return calls
}

// labPolicy is the cluster's no-quorum-policy as crm_attribute would report it, and labEnabled
// the unit file state — Debian's sbd package leaves it enabled on install.
var labPolicy = "suicide"
var labEnabled = true

func hardware() protocol.PoolHostWatchdog {
	return protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogHardware, Device: "watchdog0", TimeoutSeconds: 30}
}

// sbd owns the device, in diskless watchdog-only mode, and the configuration says exactly
// that: no shared block device to poison, and quorum as the only input.
func TestSBDIsConfiguredForDisklessWatchdogOnlyMode(t *testing.T) {
	rendered := renderSBDConfig("watchdog0")
	// SBD_PACEMAKER is the load-bearing one: it is a diskless sbd's only quorum source, and
	// with it off sbd watches nothing and holds the watchdog open for ever. (Measured.)
	for _, want := range []string{"SBD_WATCHDOG_DEV=/dev/watchdog0", "SBD_WATCHDOG_TIMEOUT=5", "SBD_PACEMAKER=yes"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the sbd configuration does not set %s:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "SBD_DEVICE=") {
		t.Fatalf("watchdog-only mode must name no block device:\n%s", rendered)
	}
}

// Arming is idempotent: a reconcile pass on a host that is already armed must not rewrite the
// file or restart the daemon that is holding a live watchdog. A restart closes the device.
func TestArmingAnAlreadyArmedHostRestartsNothing(t *testing.T) {
	active := true
	calls := labSBD(t, true, &active)
	if err := ArmLocalWatchdog(context.Background(), hardware()); err != nil {
		t.Fatal(err)
	}
	before := len(*calls)
	for i := 0; i < 5; i++ {
		if err := ArmLocalWatchdog(context.Background(), hardware()); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range (*calls)[before:] {
		if strings.Contains(call, "restart") || strings.Contains(call, "start") || strings.Contains(call, "kill") {
			t.Fatalf("a settled host ran %q; arming must be idempotent", call)
		}
	}
	if data, err := os.ReadFile(sbdConfigPath); err != nil || string(data) != renderSBDConfig("watchdog0") {
		t.Fatalf("the sbd configuration was not written as rendered: %q %v", data, err)
	}
}

// A host whose Corosync is already running cannot begin feeding its watchdog without a
// cluster restart, which must never happen by itself. It is enabled and reported pending.
// (Measured in the lab: Debian's sbd.service refuses a manual start.)
func TestArmingAHostWhoseClusterIsAlreadyRunningIsPendingAndRestartsNothing(t *testing.T) {
	active := false
	calls := labSBD(t, true, &active)
	err := ArmLocalWatchdog(context.Background(), hardware())
	if err != ErrWatchdogArmingPending {
		t.Fatalf("a host with a running cluster was not reported pending: %v", err)
	}
	if active {
		t.Fatal("sbd was started under a running cluster")
	}
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "enable sbd.service") {
		t.Fatalf("a pending host did not enable sbd for its next cluster start: %v", *calls)
	}
	for _, call := range *calls {
		if strings.Contains(call, "restart") || strings.Contains(call, "start corosync") ||
			strings.Contains(call, "start pacemaker") {
			t.Fatalf("arming restarted a running cluster: %q", call)
		}
	}
}

// A host with no Pacemaker has no quorum source for a diskless sbd, so arming it would leave
// it holding the watchdog open and never expiring it — armed on paper, unfenceable in fact.
func TestAHostWithNoClusterIsNotArmed(t *testing.T) {
	active := false
	calls := labSBD(t, true, &active, true)
	if err := ArmLocalWatchdog(context.Background(), hardware()); err != ErrWatchdogNoCluster {
		t.Fatalf("a host with no Pacemaker was not refused: %v", err)
	}
	if active {
		t.Fatal("sbd was started on a host with no quorum to lose")
	}
	for _, call := range *calls {
		if strings.Contains(call, "enable") || strings.Contains(call, "start") {
			t.Fatalf("a host with no cluster ran %q", call)
		}
	}
}

// A cluster whose policy keeps an inquorate host running would go on feeding its watchdog, so
// arming there is a promise of a protection that does not exist. Measured in the lab: with
// no-quorum-policy=freeze a partitioned host was still running after two minutes.
func TestAClusterThatKeepsAnInquorateHostRunningIsNotArmed(t *testing.T) {
	active := false
	calls := labSBD(t, true, &active)
	labPolicy = "freeze"
	if err := ArmLocalWatchdog(context.Background(), hardware()); err != ErrWatchdogClusterWillNotSelfFence {
		t.Fatalf("a cluster that will not self-fence was not refused: %v", err)
	}
	if active {
		t.Fatal("sbd was started on a cluster that would never let the host reset")
	}
	for _, call := range *calls {
		if strings.Contains(call, "enable") || strings.Contains(call, "start") {
			t.Fatalf("a cluster that will not self-fence ran %q", call)
		}
	}
}

// Only a device that resets the machine independently of the kernel is ever armed, and a host
// with no sbd package is refused with a sentence rather than left looking armed.
func TestOnlyAnIndependentWatchdogIsArmed(t *testing.T) {
	active := false
	labSBD(t, true, &active)
	for _, kind := range []string{protocol.PoolWatchdogSoftware, protocol.PoolWatchdogUnknown, protocol.PoolWatchdogNone} {
		if err := ArmLocalWatchdog(context.Background(), protocol.PoolHostWatchdog{Kind: kind, Device: "watchdog0"}); err == nil {
			t.Fatalf("a %s watchdog was armed", kind)
		}
	}
	if active {
		t.Fatal("a refused arming started sbd anyway")
	}
	labSBD(t, false, &active)
	if err := ArmLocalWatchdog(context.Background(), hardware()); err == nil || !strings.Contains(err.Error(), "sbd package") {
		t.Fatalf("a host without sbd was not refused clearly: %v", err)
	}
}

// Disarming stops this host arming again and deliberately does NOT touch a running cluster's
// services. Measured in the lab: `systemctl disable` under a live cluster is inert — corosync,
// pacemaker and sbd all stayed active and the device stayed held — while a SIGTERM left the
// unit `failed` on a `RequiredBy=corosync.service` edge, with no clustered filesystem mounted.
// So a host that is still feeding one says so and releases it at the next cluster stop.
func TestDisarmingNeverDisturbsARunningCluster(t *testing.T) {
	active := true
	calls := labSBD(t, true, &active)
	err := DisarmLocalWatchdog(context.Background(), hardware())
	if err != ErrWatchdogDisarmPending {
		t.Fatalf("a host still feeding its watchdog was not reported pending: %v", err)
	}
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "disable sbd.service") {
		t.Fatalf("disarming did not stop this host arming again: %v", *calls)
	}
	for _, call := range *calls {
		if strings.Contains(call, "kill") || strings.Contains(call, "stop") || strings.Contains(call, "restart") {
			t.Fatalf("disarming disturbed a running cluster: %q", call)
		}
	}
	if !active {
		t.Fatal("disarming stopped a daemon holding a live watchdog under a running cluster")
	}

	// A host that is not feeding one is simply disabled, with nothing left pending — and a
	// second pass over an already-disabled unit runs no systemctl at all.
	active = false
	if err = DisarmLocalWatchdog(context.Background(), hardware()); err != nil {
		t.Fatalf("disarming a host that is not armed failed: %v", err)
	}
	if labEnabled {
		t.Fatal("an unwanted sbd unit was left enabled; the next cluster start would pull it in")
	}
	settled := len(*calls)
	if err = DisarmLocalWatchdog(context.Background(), hardware()); err != nil {
		t.Fatal(err)
	}
	for _, call := range (*calls)[settled:] {
		if strings.Contains(call, "disable") {
			t.Fatalf("a settled host ran %q every pass", call)
		}
	}

	// nowayout is the permanent form of the same answer: only a reboot puts the timer down.
	active = true
	nowayout := hardware()
	nowayout.NoWayOut = true
	if err = DisarmLocalWatchdog(context.Background(), nowayout); err != ErrWatchdogNoWayOut {
		t.Fatalf("a nowayout host did not say a reboot is needed: %v", err)
	}
}

// The quorum this engine decides on is Corosync's own, so it is read from Corosync's own tool
// and never from anything of Nex's.
func TestLocalQuorumReadsCorosync(t *testing.T) {
	previous := clusterRun
	t.Cleanup(func() { clusterRun = previous })
	output := `Quorum information
------------------
Quorum provider:  corosync_votequorum
Nodes:            2
Node ID:          1
Quorate:          Yes

Votequorum information
----------------------
Expected votes:   3
Total votes:      2
Quorum:           2
Flags:            Quorate

Membership information
----------------------
    Nodeid      Votes Name
         1          1 core-a (local)
         2          1 core-b
`
	clusterRun = func(context.Context, string, ...string) ([]byte, error) { return []byte(output), nil }
	q, err := LocalQuorum(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !q.Observed || !q.Quorate || len(q.Hosts) != 2 || q.Hosts[0] != "core-a" || q.Hosts[1] != "core-b" {
		t.Fatalf("LocalQuorum() = %#v", q)
	}
	if !q.Excludes("core-c") || q.Excludes("core-a") {
		t.Fatalf("Excludes is wrong for %#v", q)
	}
	// A pool with a quorum device gains a column before the name. Reading the third field
	// would make every host's name "NR", so the name is the last one. (Found in the lab.)
	clusterRun = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Quorate:          Yes\n\nMembership information\n----------------------\n" +
			"    Nodeid      Votes    Qdevice Name\n         1          1         NR core-a (local)\n" +
			"         2          1         NR core-b\n"), nil
	}
	if q, err = LocalQuorum(context.Background()); err != nil || len(q.Hosts) != 2 ||
		q.Hosts[0] != "core-a" || q.Hosts[1] != "core-b" {
		t.Fatalf("a quorum-device column broke the membership names: %#v %v", q, err)
	}

	// A host with no Corosync has no view at all, which is not "not quorate".
	clusterRun = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	if q, err = LocalQuorum(context.Background()); err == nil || q.Observed || q.Excludes("core-c") {
		t.Fatalf("a host with no quorum service claimed one: %#v %v", q, err)
	}
}
