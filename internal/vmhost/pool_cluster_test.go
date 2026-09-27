// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func labNodes() []PoolClusterNode {
	return []PoolClusterNode{{HostID: "host-a", Address: "198.51.100.11"},
		{HostID: "host-b", Address: "198.51.100.12"}, {HostID: "host-c", Address: "198.51.100.13"}}
}

// A pool's cluster is every ready host, on a ring that will not let a partition which has
// never seen the whole pool decide it is the majority, and its node names are the durable host
// IDs the rest of the fencing path already reads back out of corosync-quorumtool.
func TestThePoolClusterIsEveryHostAndWaitsForThemAll(t *testing.T) {
	rendered, err := RenderPoolCluster("pool-1", labNodes())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"wait_for_all: 1", "name: host-a", "name: host-b", "name: host-c",
		"ring0_addr: 198.51.100.11", "provider: corosync_votequorum", poolClusterMarker} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the pool cluster configuration does not contain %q:\n%s", want, rendered)
		}
	}
	if id := PoolClusterNodeID("host-a"); id == 0 || id != PoolClusterNodeID("host-a") {
		t.Fatalf("a host's Corosync node identifier is not stable and non-zero: %d", id)
	}
	if id := PoolClusterNodeID("host-a"); id > 65535 {
		t.Fatalf("node ID %d exceeds Corosync's supported 16-bit range", id)
	}
	if id := PoolClusterNodeID("legacy-24623"); id != 5615 {
		t.Fatalf("previously valid node ID changed: %d", id)
	}
	if PoolClusterNodeID("host-a") == PoolClusterNodeID("host-b") {
		t.Fatal("two hosts derive the same Corosync node identifier")
	}
	if _, err = RenderPoolCluster("pool-1", labNodes()[:2]); err == nil {
		t.Fatal("a two-host pool was given a watchdog cluster, which has no majority side")
	}
	if _, err = RenderPoolCluster("pool-1", append(labNodes(), PoolClusterNode{HostID: "host-d"})); err == nil {
		t.Fatal("a host with no address was accepted into the nodelist")
	}
}

func TestTwoHostPoolClusterNeedsTlsQdevice(t *testing.T) {
	if _, err := RenderPoolClusterWithWitness("pool-1", labNodes()[:2], "198.51.100.20:5403"); err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderPoolClusterWithWitness("pool-1", labNodes()[:2], "198.51.100.20:5403")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"model: net", "votes: 1", "host: 198.51.100.20", "port: 5403", "algorithm: ffsplit", "tie_breaker: lowest", "tls: required"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("qdevice configuration lacks %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "wait_for_all: 1") {
		t.Fatalf("two-host witness config would block requorum after witness recovery:\n%s", rendered)
	}
	if _, err := RenderPoolClusterWithWitness("pool-1", labNodes()[:2], "bad address"); err == nil {
		t.Fatal("accepted malformed qnetd address")
	}
}

// One host has one corosync.conf, so it has one cluster. Clustered storage writes
// no-quorum-policy=freeze on its own, which is measurably a cluster that keeps an inquorate
// host running; that must never land under hosts that are feeding watchdogs, and neither
// consumer may take the other's file over. This is the interlock at the file itself, under the
// intent checks rather than instead of them.
func TestNeitherClusterTakesOverTheOthersCorosyncConfiguration(t *testing.T) {
	dir := t.TempDir()
	previous := corosyncConfigPath
	corosyncConfigPath = filepath.Join(dir, "corosync.conf")
	t.Cleanup(func() { corosyncConfigPath = previous })

	pool, err := RenderPoolCluster("pool-1", labNodes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writeCorosyncConfig(dir, pool, poolClusterMarker); err != nil {
		t.Fatal(err)
	}
	if !PoolClusterOwnsCorosync() {
		t.Fatal("the pool's own cluster configuration is not recognised as its own")
	}
	if err := PoolClusterCanOwnCorosync(dir); err != nil {
		t.Fatalf("pool cluster refused its own configuration: %v", err)
	}
	storage, err := RenderStorageCluster(protocol.StorageClusterConfig{ClusterID: "sc1",
		Nodes: []protocol.StorageClusterNode{{HostID: "host-a", CorosyncAddress: "198.51.100.11", CorosyncNodeID: 1},
			{HostID: "host-b", CorosyncAddress: "198.51.100.12", CorosyncNodeID: 2},
			{HostID: "host-c", CorosyncAddress: "198.51.100.13", CorosyncNodeID: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writeCorosyncConfig(dir, storage, ""); err == nil {
		t.Fatal("clustered storage replaced the pool's own watchdog cluster configuration")
	}
	if _, err = writeCorosyncConfig(dir, storage, ""); err == nil {
		t.Fatal("a second attempt succeeded")
	}
	// And the other direction: a host that already runs clustered storage is never taken
	// over by the pool's watchdog cluster.
	if err = os.WriteFile(corosyncConfigPath, []byte(storage), 0600); err != nil {
		t.Fatal(err)
	}
	if PoolClusterOwnsCorosync() {
		t.Fatal("clustered storage's configuration was read as the pool's own")
	}
	if err := PoolClusterCanOwnCorosync(dir); err == nil {
		t.Fatal("pool cluster key could overwrite clustered storage's authentication")
	}
	if _, err = writeCorosyncConfig(dir, pool, poolClusterMarker); err == nil {
		t.Fatal("the pool's watchdog cluster replaced clustered storage's configuration")
	}
}

// labCluster stands in for systemd, corosync and the CIB.
type labCluster struct {
	calls     []string
	active    map[string]bool
	resources bool
	cib       string
}

func newLabCluster(t *testing.T, dir string) *labCluster {
	t.Helper()
	lab := &labCluster{active: map[string]bool{}, cib: "<cib><configuration><resources/></configuration></cib>"}
	previousRun, previousPath := clusterRun, corosyncConfigPath
	corosyncConfigPath = filepath.Join(dir, "corosync.conf")
	clusterRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		lab.calls = append(lab.calls, strings.Join(append([]string{name}, args...), " "))
		switch {
		case name == "cibadmin":
			if lab.resources {
				return []byte(`<cib><configuration><resources><primitive id="something"/></resources></configuration></cib>`), nil
			}
			return []byte(lab.cib), nil
		case name == "systemctl" && len(args) > 0 && args[0] == "show":
			for _, unit := range args {
				if lab.active[unit] {
					return []byte("active\n"), nil
				}
			}
			return []byte("inactive\n"), nil
		case name == "systemctl" && len(args) > 0 && args[0] == "start":
			for _, unit := range args[1:] {
				lab.active[unit] = true
			}
		case name == "systemctl" && len(args) > 0 && args[0] == "stop":
			for _, unit := range args[1:] {
				lab.active[unit] = false
			}
		}
		return nil, nil
	}
	t.Cleanup(func() { clusterRun, corosyncConfigPath = previousRun, previousPath })
	return lab
}

func (l *labCluster) ran(fragment string) bool {
	for _, call := range l.calls {
		if strings.Contains(call, fragment) {
			return true
		}
	}
	return false
}

// The cold-start rule, in code: Nex Core starts this cluster itself and never enables it at
// boot, because a host that comes up armed and alone — a site power cut, the last host of a
// pool rebooting — is inquorate from its first moment and would reset itself in a loop with
// nothing wrong anywhere in the pool.
//
// And a membership change is a reload, not a restart: adding or removing a host must move
// every host's expected votes without stopping a peer that is feeding a watchdog.
func TestThePoolClusterIsStartedByCoreAndReloadedOnAMembershipChange(t *testing.T) {
	dir := t.TempDir()
	lab := newLabCluster(t, dir)
	if _, err := ApplyPoolCluster(context.Background(), dir, "pool-1", labNodes()); err != nil {
		t.Fatal(err)
	}
	if lab.ran("enable") {
		t.Fatalf("the pool's cluster was enabled at boot: %v", lab.calls)
	}
	if !lab.ran("systemctl start corosync.service pacemaker.service") {
		t.Fatalf("the pool's cluster was not started: %v", lab.calls)
	}
	// The cold-start defence has to be here and unconditional. sbd is still enabled from the
	// last time this host armed, on a corosync.service.requires edge, and the arming pass
	// that would have disabled it can fail to reach the master — which is exactly the state
	// a host boots into after a site power cut.
	disable, start := -1, -1
	for i, call := range lab.calls {
		if strings.Contains(call, "disable sbd.service") && disable < 0 {
			disable = i
		}
		if strings.Contains(call, "start corosync.service") && start < 0 {
			start = i
		}
	}
	if disable < 0 || disable > start {
		t.Fatalf("the pool's cluster was started without disabling sbd first, so a host that boots alone comes up armed: %v", lab.calls)
	}
	lab.calls = nil
	if _, err := ApplyPoolCluster(context.Background(), dir, "pool-1", labNodes()); err != nil {
		t.Fatal(err)
	}
	if lab.ran("start") || lab.ran("cfgtool") {
		t.Fatalf("an unchanged pass disturbed a running cluster: %v", lab.calls)
	}
	lab.calls = nil
	if _, err := ApplyPoolCluster(context.Background(), dir, "pool-1", append(labNodes(),
		PoolClusterNode{HostID: "host-d", Address: "198.51.100.14"})); err != nil {
		t.Fatal(err)
	}
	if !lab.ran("corosync-cfgtool -R") || lab.ran("stop") || lab.ran("restart") {
		t.Fatalf("a membership change restarted the cluster instead of reloading it: %v", lab.calls)
	}
}

// Arming is the one restart, and it is safe only because a pool's own cluster runs nothing.
// That is checked against the CIB rather than assumed, so this can never become the thing that
// restarts Corosync under a mounted clustered filesystem.
func TestArmingNeverRestartsAClusterThatIsRunningAnything(t *testing.T) {
	dir := t.TempDir()
	lab := newLabCluster(t, dir)
	if _, err := ApplyPoolCluster(context.Background(), dir, "pool-1", labNodes()); err != nil {
		t.Fatal(err)
	}
	lab.resources = true
	lab.calls = nil
	if err := RestartPoolClusterForArming(context.Background()); err == nil {
		t.Fatal("a cluster with resources on it was restarted to arm a watchdog")
	}
	if lab.ran("stop") || lab.ran("start") {
		t.Fatalf("it was restarted anyway: %v", lab.calls)
	}
	lab.resources = false
	lab.calls = nil
	if err := RestartPoolClusterForArming(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !lab.ran("systemctl stop pacemaker.service corosync.service") ||
		!lab.ran("systemctl start corosync.service pacemaker.service") {
		t.Fatalf("the cluster was not restarted one service order at a time: %v", lab.calls)
	}
}

// The policy is what makes the watchdog a fence rather than a promise: a cluster that keeps an
// inquorate host running never expires its watchdog, measured at over two minutes.
//
// And stonith-watchdog-timeout must never be on this cluster. Pacemaker answers that property
// plus one node without sbd by shutting the cluster down — measured in the nested lab, where the
// shutdown reached nodes that were still feeding and reset one of them. Every ordinary event on a
// pool's cluster produces an unarmed node: forming it, the arming rollout, a teardown, a host
// rebooting back into an armed pool. The property buys a wait before Pacemaker recovers resources
// and this cluster has none, so it is not written, and it is removed where it is found.
func TestThePoolClusterPolicyIsWrittenAndTheWatchdogBoundIsRemoved(t *testing.T) {
	dir := t.TempDir()
	lab := newLabCluster(t, dir)
	if err := ApplyPoolClusterProperties(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"no-quorum-policy=suicide", "stonith-enabled=true"} {
		if !lab.ran(want) {
			t.Fatalf("the pool cluster policy did not set %s: %v", want, lab.calls)
		}
	}
	if lab.ran("property set stonith-watchdog-timeout") {
		t.Fatalf("the pool's cluster was given a bound that shuts it down as soon as one node has no sbd: %v", lab.calls)
	}
	ready, err := PoolClusterPropertiesReady(context.Background())
	if err != nil || ready {
		t.Fatalf("an empty CIB was read as a cluster that lets an inquorate host die: %v %v", ready, err)
	}
	base := `<nvpair name="no-quorum-policy" value="suicide"/><nvpair name="stonith-enabled" value="true"/>`
	wrap := func(inner string) string {
		return `<cib><configuration><crm_config><cluster_property_set>` + inner +
			`</cluster_property_set></crm_config></configuration></cib>`
	}
	lab.cib = wrap(base)
	if ready, err = PoolClusterPropertiesReady(context.Background()); err != nil || !ready {
		t.Fatalf("the policy this cluster needs was not read back: %v %v", ready, err)
	}

	// A bound from an earlier Nex Core, or from an administrator, is removed.
	lab.cib = wrap(base + `<nvpair name="stonith-watchdog-timeout" value="30s"/>`)
	if ready, err = PoolClusterPropertiesReady(context.Background()); err != nil || ready {
		t.Fatalf("a cluster carrying the bound was read as current: %v %v", ready, err)
	}
	lab.calls = nil
	if err = ApplyPoolClusterProperties(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !lab.ran("property unset stonith-watchdog-timeout") {
		t.Fatalf("a bound this cluster must not carry was left on it: %v", lab.calls)
	}
	// And a host will not start a cluster whose CIB still carries one.
	if !PoolClusterBoundSet(context.Background()) {
		t.Fatal("a CIB carrying the bound was read as clear")
	}
	lab.cib = wrap(base)
	if PoolClusterBoundSet(context.Background()) {
		t.Fatal("a clear CIB was read as carrying the bound")
	}
}

// Q4. The stop is bounded, and the bound is the number the whole stalled-teardown report depends
// on. Measured in the nested laboratory: `systemctl stop pacemaker.service` hung indefinitely on
// those guests more than once and had to be killed by hand. On timeout this must answer
// ErrPoolClusterStopStalled rather than block the reconcile pass, and it must never reach for the
// watchdog daemon to get out of it.
// (Reported by independent review of slice 4c, rev 4.)
func TestAHungClusterStopIsBoundedAndNeverKillsTheWatchdogDaemon(t *testing.T) {
	// The shipped bound itself, pinned: it has to be short enough that the arming worker's
	// own pass reports the stall rather than being cut off mid-attempt, and non-zero.
	if poolClusterStopTimeout <= 0 || poolClusterStopTimeout > time.Minute {
		t.Fatalf("the cluster stop is not bounded usefully: %s", poolClusterStopTimeout)
	}
	previousRun, previousTimeout := clusterRun, poolClusterStopTimeout
	var calls []string
	poolClusterStopTimeout = 150 * time.Millisecond
	clusterRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		<-ctx.Done() // systemd accepted the job and never finished it.
		return nil, ctx.Err()
	}
	t.Cleanup(func() { clusterRun, poolClusterStopTimeout = previousRun, previousTimeout })

	started := time.Now()
	err := stopPoolCluster(context.Background())
	if !errors.Is(err, ErrPoolClusterStopStalled) {
		t.Fatalf("a hung stop did not report itself as stalled: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the stop was not bounded: it took %s", elapsed)
	}
	for _, call := range calls {
		if strings.Contains(call, "sbd") || strings.Contains(call, "kill") {
			t.Fatalf("a hung stop reached for the watchdog daemon: %v", calls)
		}
	}
	// And a caller's own cancellation is its own error, not a stall this host must report.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = stopPoolCluster(ctx); errors.Is(err, ErrPoolClusterStopStalled) {
		t.Fatal("a cancelled pass was reported as a stalled stop")
	}
}

// The rendered configuration has to be one Corosync will actually parse. The nested lab lost two
// whole runs to a stray brace in the laboratory's own copy of it, which corosync refused with
// "Extra characters before or after closing bracket" — and a host whose corosync.conf does not
// parse has no cluster at all.
func TestTheRenderedCorosyncConfigurationIsBalanced(t *testing.T) {
	for _, rendered := range []string{mustRenderPool(t), mustRenderStorage(t)} {
		if strings.Count(rendered, "{") != strings.Count(rendered, "}") {
			t.Fatalf("Corosync would refuse this configuration:\n%s", rendered)
		}
		for _, line := range strings.Split(rendered, "\n") {
			if strings.Contains(line, "}") && strings.TrimSpace(line) != "}" && !strings.HasSuffix(strings.TrimSpace(line), "{") {
				t.Fatalf("a closing brace shares a line with other characters: %q", line)
			}
		}
	}
}

func mustRenderPool(t *testing.T) string {
	t.Helper()
	rendered, err := RenderPoolCluster("pool-1", labNodes())
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}

func mustRenderStorage(t *testing.T) string {
	t.Helper()
	rendered, err := RenderStorageCluster(protocol.StorageClusterConfig{ClusterID: "sc1",
		Nodes: []protocol.StorageClusterNode{{HostID: "host-a", CorosyncAddress: "198.51.100.11", CorosyncNodeID: 1},
			{HostID: "host-b", CorosyncAddress: "198.51.100.12", CorosyncNodeID: 2},
			{HostID: "host-c", CorosyncAddress: "198.51.100.13", CorosyncNodeID: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	return rendered
}

// sbd must not begin feeding the moment a cluster starts forming. Nex Core's first defence is
// that it starts the cluster itself and always unarmed; this is the second, and it is the
// engine's own.
func TestSBDWaitsForTheClusterToFormBeforeItFeeds(t *testing.T) {
	if !strings.Contains(renderSBDConfig("watchdog0"), "SBD_DELAY_START=yes") {
		t.Fatalf("sbd begins feeding a cluster that is still forming:\n%s", renderSBDConfig("watchdog0"))
	}
	if !strings.HasPrefix(renderSBDConfig("watchdog0"), sbdConfigMarker) {
		t.Fatal("a Nex-managed sbd configuration carries no marker, so a package upgrade cannot tell an armed host from a fresh one")
	}
}

// Leaving the pool's cluster has to release the file as well as the services. A host has one
// corosync.conf, and Core's own refusal tells the operator to turn Host fencing off before
// registering a clustered-storage repository — which only works if the watchdog cluster's
// configuration is gone once nobody is in it.
// (Reported by independent review of slice 4c.)
func TestLeavingThePoolClusterReleasesItsCorosyncConfiguration(t *testing.T) {
	dir := t.TempDir()
	lab := newLabCluster(t, dir)
	if _, err := ApplyPoolCluster(context.Background(), dir, "pool-1", labNodes()); err != nil {
		t.Fatal(err)
	}
	if err := LeavePoolCluster(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !lab.ran("systemctl stop pacemaker.service corosync.service") {
		t.Fatalf("the pool's cluster was not stopped: %v", lab.calls)
	}
	if PoolClusterOwnsCorosync() {
		t.Fatal("the pool's cluster configuration was left on disk after this host left it")
	}
	if _, err := writeCorosyncConfig(dir, mustRenderStorage(t), ""); err != nil {
		t.Fatalf("clustered storage still cannot be registered after host fencing was turned off: %v", err)
	}
	// And leaving again, with nothing of this pool's left on disk, does nothing at all.
	lab.calls = nil
	if err := LeavePoolCluster(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(lab.calls) > 0 {
		t.Fatalf("leaving a cluster this host is not in touched the host: %v", lab.calls)
	}
}
