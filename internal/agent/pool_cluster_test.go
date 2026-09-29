// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestPoolClusterUsesVerifiedMemberReplicaForWitness(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	local := labMembership(t, member)
	if local.Witness != nil || local.Members[1].PeerOrigin != "" {
		t.Fatalf("test needs stale member membership: %#v", local)
	}
	ca, pin := witnessTestCA(t, "pool-witness")
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Membership.Generation = 5
		for i := range state.Membership.Members {
			state.Membership.Members[i].Generation = 5
		}
		state.Membership.Witness = &protocol.PoolWitnessRecord{Kind: "controller", Address: "192.0.2.10:5403", CAPEM: ca, CASHA256: pin,
			FencingGeneration: state.Membership.Generation, ClusterName: protocol.PoolWitnessClusterName(state.Membership.PoolID), CredentialsReady: true}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	replica, _, err := buildPoolAuthorityReplica(master)
	if err != nil {
		t.Fatal(err)
	}
	replica.Generation, replica.MemberHostID = 1, member.State.HostID
	replica.WitnessCredentials = []protocol.PoolWrappedWitnessCredential{{HostID: member.State.HostID,
		FencingGeneration: replica.Membership.Witness.FencingGeneration, Wrapped: &protocol.PoolWrappedKey{}}}
	signer, err := poolCASigner(master)
	if err == nil {
		err = signPoolAuthorityReplica(signer, &replica)
	}
	if err != nil {
		t.Fatal(err)
	}
	digest, err := replica.ContentDigest()
	if err == nil {
		err = savePoolReplica(member, poolReplicaRecord{Replica: replica, Digest: digest, Verified: true})
	}
	if err != nil {
		t.Fatal(err)
	}
	previousApply, previousReady, previousActivate, previousDeactivate := applyPoolClusterWithWitness, poolWitnessCredentialsReady, activatePoolWitness, deactivatePoolWitness
	previousRead, previousMint, previousInstall := readPoolClusterAuthKey, mintPoolClusterAuthKey, installPoolClusterAuthKey
	t.Cleanup(func() {
		applyPoolClusterWithWitness, poolWitnessCredentialsReady, activatePoolWitness, deactivatePoolWitness = previousApply, previousReady, previousActivate, previousDeactivate
		readPoolClusterAuthKey, mintPoolClusterAuthKey, installPoolClusterAuthKey = previousRead, previousMint, previousInstall
	})
	applied := false
	installed := false
	readPoolClusterAuthKey = func(*Store) ([]byte, error) { return make([]byte, 128), nil }
	mintPoolClusterAuthKey = func(*Store, string) ([]byte, error) { return make([]byte, 128), nil }
	installPoolClusterAuthKey = func(key []byte) error { installed = len(key) == 128; return nil }
	applyPoolClusterWithWitness = func(_ context.Context, _, _ string, nodes []vmhost.PoolClusterNode, address string) (bool, error) {
		applied = installed && len(nodes) == 2 && address == "192.0.2.10:5403"
		return true, nil
	}
	poolWitnessCredentialsReady = func(context.Context, string, string, protocol.PoolWitnessRecord) bool { return true }
	activatePoolWitness = func(context.Context, string, string, protocol.PoolWitnessRecord) error { return nil }
	deactivatePoolWitness = func(context.Context) error { return errors.New("witness was unexpectedly deactivated") }
	if err := ReconcilePoolCluster(context.Background(), member); err != nil || !applied {
		t.Fatalf("verified replica witness was not applied: applied=%v err=%v", applied, err)
	}
	poolPeerMu.Lock()
	state, err = loadPoolPeer(member)
	if err == nil {
		state.Membership.Generation = 5
		for i := range state.Membership.Members {
			state.Membership.Members[i].Generation = 5
		}
		err = savePoolPeer(member, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	applied = false
	if err := ReconcilePoolCluster(context.Background(), member); err != nil || !applied {
		t.Fatalf("same-generation signed witness was not applied: applied=%v err=%v", applied, err)
	}
	applied, installed = false, false
	installPoolClusterAuthKey = func([]byte) error { return errors.New("key install failed") }
	if err := ReconcilePoolCluster(context.Background(), member); err == nil || applied {
		t.Fatalf("cluster started without its key: %v", err)
	}
	installPoolClusterAuthKey = func([]byte) error { return nil }
	replica.Signature = "invalid"
	if err := savePoolReplica(member, poolReplicaRecord{Replica: replica, Digest: digest, Verified: true}); err != nil {
		t.Fatal(err)
	}
	if err := ReconcilePoolCluster(context.Background(), member); err == nil || applied {
		t.Fatalf("cluster accepted an invalid replica signature: %v", err)
	}
}

func TestTwoHostWitnessDoesNotReplaceClusterWhileWatchdogFeeds(t *testing.T) {
	lab, _ := labArmedPool(t)
	master := lab.host("master-1")
	setMemberStatus(t, master, "member-2", "ejected")
	ca, pin := witnessTestCA(t, "pool-witness")
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Membership.Witness = &protocol.PoolWitnessRecord{Kind: "controller", Address: "192.0.2.10:5403", CAPEM: ca, CASHA256: pin,
			FencingGeneration: state.Membership.Generation, ClusterName: protocol.PoolWitnessClusterName(state.Membership.PoolID), CredentialsReady: true}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	membership := labMembership(t, master)
	nodes, wanted, _ := poolClusterIntent(master, membership)
	self, _ := poolMemberByID(membership, master.State.HostID)
	if !wanted || !poolWatchdogShouldDisarm(master, membership, nodes, self) {
		t.Fatal("a two-host witness transition did not ask an already armed host to disarm")
	}
	if err := ReconcilePoolCluster(context.Background(), master); err == nil || !strings.Contains(err.Error(), "disarm this host first") {
		t.Fatalf("armed cluster was allowed to change to two-host witness: %v", err)
	}
	if err := poolClusterPolicy(context.Background(), master); err == nil || !strings.Contains(err.Error(), "disarm") {
		t.Fatalf("armed cluster was allowed to switch to the two-host freeze policy: %v", err)
	}
}

func TestFrozenAuthorityBlocksWatchdogBeforeSBDStarts(t *testing.T) {
	lab, pool := labFencingPool(t)
	pool.round()
	pool.settle()
	master := lab.host("master-1")
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Transfer = &poolTransferRecord{Action: "eject", Role: "source", Stage: "freeze", Frozen: true}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	pool.current = master.State.HostID
	if err := ReconcilePoolWatchdogArming(context.Background(), master); err == nil {
		t.Fatal("arming proceeded without a durable feeding intent")
	}
	if pool.feeding[master.State.HostID] || pool.enabled[master.State.HostID] {
		t.Fatal("SBD started while authority could not record feeding intent")
	}
}

func TestArmedThreeHostPoolRefusesDrainToTwoHostWitness(t *testing.T) {
	lab, _ := labArmedPool(t)
	master := lab.host("master-1")
	ca, pin := witnessTestCA(t, "pool-witness")
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Membership.Witness = &protocol.PoolWitnessRecord{Kind: "controller", Address: "192.0.2.10:5403", CAPEM: ca, CASHA256: pin,
			FencingGeneration: state.Membership.Generation, ClusterName: protocol.PoolWitnessClusterName(state.Membership.PoolID), CredentialsReady: true}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	generation := memberRecord(t, master, "member-2").Generation
	result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "member-2", true, generation))
	if result.Status != "failed" || !strings.Contains(result.Error, "stop every host watchdog") {
		t.Fatalf("armed witness drain = %#v", result)
	}
	if memberRecord(t, master, "member-2").Maintenance {
		t.Fatal("armed member entered maintenance")
	}
	poolPeerMu.Lock()
	state, err = loadPoolPeer(master)
	if err == nil {
		for i := range state.Membership.Members {
			if state.Membership.Members[i].HostID == "member-2" {
				state.Membership.Members[i].Maintenance = true
				state.Membership.Members[i].MaintenanceSince = time.Now().UTC()
			}
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if retry := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "member-2", true, generation)); retry.Status != "succeeded" {
		t.Fatalf("an already recorded drain did not accept an exact retry: %#v", retry)
	}
}

// labWatchdogPool models the three things the earlier fakes flattened, and each of them hid a way
// to reset a healthy host:
//
//   - sbd has three states, not two. It can be *enabled and not running* — which is what a host
//     that armed once and then rebooted looks like, and what makes starting its cluster arm it.
//   - disarming has latency. DisarmLocalWatchdog disables the unit and the device goes on being
//     fed until that host's cluster next stops, so "not armed" in the record and "not feeding"
//     on the machine are minutes apart.
//   - quorum moves as hosts leave. Each host holds its own nodelist, so expected votes do not
//     shrink because somebody else stopped, and a host can be the last one standing against an
//     expectation of three.
//
// After every pass it applies the physics: a host that is feeding a watchdog inside a cluster
// that is not quorate resets. That is the assertion the whole slice exists to keep.
type labWatchdogPool struct {
	t       *testing.T
	lab     *transferLab
	current string
	enabled map[string]bool     // sbd.service is enabled on this host
	feeding map[string]bool     // sbd is running, so the device is being fed
	running map[string]bool     // this host's corosync/pacemaker are up
	nodes   map[string][]string // the nodelist this host last applied
	ring    int
	bound   bool // stonith-watchdog-timeout is set on this cluster
	resets  []string
	emerg   []string
}

func newLabWatchdogPool(t *testing.T, lab *transferLab) *labWatchdogPool {
	t.Helper()
	p := &labWatchdogPool{t: t, lab: lab, enabled: map[string]bool{}, feeding: map[string]bool{},
		running: map[string]bool{}, nodes: map[string][]string{}, ring: 1}
	previousArm, previousDisarm, previousArmed := armLocalWatchdog, disarmLocalWatchdog, localWatchdogArmed
	previousApply, previousLeave, previousRestart := applyPoolCluster, leavePoolCluster, restartPoolCluster
	previousProperties, previousReady := applyPoolClusterProperties, poolClusterPropertiesReady
	previousBound, previousQuorum := poolClusterBoundSet, localQuorum
	previousRead, previousMint, previousInstall := readPoolClusterAuthKey, mintPoolClusterAuthKey, installPoolClusterAuthKey
	readPoolClusterAuthKey = func(*Store) ([]byte, error) { return make([]byte, 128), nil }
	mintPoolClusterAuthKey = func(*Store, string) ([]byte, error) { return make([]byte, 128), nil }
	installPoolClusterAuthKey = func([]byte) error { return nil }

	armLocalWatchdog = func(context.Context, protocol.PoolHostWatchdog) error {
		if !p.running[p.current] {
			return vmhost.ErrWatchdogNoCluster
		}
		p.enabled[p.current] = true
		if p.feeding[p.current] {
			return nil
		}
		// Debian's sbd.service refuses a manual start: it can only join at the cluster's
		// next start.
		return vmhost.ErrWatchdogArmingPending
	}
	disarmLocalWatchdog = func(context.Context, protocol.PoolHostWatchdog) error {
		p.enabled[p.current] = false
		if !p.feeding[p.current] {
			return nil
		}
		// Disabling is inert on a running daemon; the device is released at the next
		// cluster stop, which is the whole of F3.
		return vmhost.ErrWatchdogDisarmPending
	}
	localWatchdogArmed = func(context.Context) (bool, error) { return p.feeding[p.current], nil }

	applyPoolCluster = func(_ context.Context, _, _ string, nodes []vmhost.PoolClusterNode) (bool, error) {
		var hosts []string
		for _, node := range nodes {
			hosts = append(hosts, node.HostID)
		}
		changed := !slices.Equal(p.nodes[p.current], hosts)
		p.nodes[p.current] = hosts
		if !p.running[p.current] {
			// ApplyPoolCluster disables sbd before it starts, unconditionally.
			p.enabled[p.current] = false
			p.start(p.current)
		}
		return changed, nil
	}
	leavePoolCluster = func(context.Context) error { p.stop(p.current); return nil }
	restartPoolCluster = func(context.Context) error { p.stop(p.current); p.start(p.current); return nil }
	// The CIB is modelled, not stubbed. The bound is the one property whose ordering matters,
	// and stubbing it out is what hid the reset the nested lab measured.
	applyPoolClusterProperties = func(context.Context) error { p.bound = false; return nil }
	poolClusterPropertiesReady = func(context.Context) (bool, error) { return !p.bound, nil }
	poolClusterBoundSet = func(context.Context) bool { return p.bound }
	localQuorum = func(context.Context) (vmhost.PoolQuorum, error) { return p.quorum(p.current), nil }

	t.Cleanup(func() {
		armLocalWatchdog, disarmLocalWatchdog, localWatchdogArmed = previousArm, previousDisarm, previousArmed
		applyPoolCluster, leavePoolCluster, restartPoolCluster = previousApply, previousLeave, previousRestart
		applyPoolClusterProperties, poolClusterPropertiesReady = previousProperties, previousReady
		poolClusterBoundSet, localQuorum = previousBound, previousQuorum
		readPoolClusterAuthKey, mintPoolClusterAuthKey, installPoolClusterAuthKey = previousRead, previousMint, previousInstall
		for _, host := range lab.hosts {
			ClearPoolQuorumForTest(host)
		}
	})
	return p
}

func (p *labWatchdogPool) start(host string) {
	p.running[host] = true
	p.feeding[host] = p.enabled[host] // sbd joins as a dependency of corosync, or not at all.
	p.ring++
}

func (p *labWatchdogPool) stop(host string) {
	if !p.running[host] {
		return
	}
	p.running[host] = false
	p.feeding[host] = false // PartOf=corosync.service: stopping the cluster releases the device.
	p.ring++
}

// quorum is what this host reads from its own Corosync: the hosts whose cluster is up, against
// the expected votes of the nodelist this host itself holds.
func (p *labWatchdogPool) quorum(host string) vmhost.PoolQuorum {
	if !p.running[host] {
		return vmhost.PoolQuorum{}
	}
	var present []string
	for _, peer := range p.lab.hosts {
		if p.running[peer.State.HostID] && slices.Contains(p.nodes[host], peer.State.HostID) {
			present = append(present, peer.State.HostID)
		}
	}
	sort.Strings(present)
	expected := len(p.nodes[host])
	return vmhost.PoolQuorum{Observed: true, Quorate: len(present) >= expected/2+1,
		RingID: "1." + strings.Repeat("a", p.ring), Hosts: present}
}

// physics applies the two rules the nested lab measured.
//
// The first is Pacemaker's: with stonith-watchdog-timeout set and no sbd on a node, that node's
// pacemaker-controld logs "emerg: Shutting down: stonith-watchdog-timeout configured but SBD not
// active" and takes corosync with it — and the lab measured the shutdown reaching nodes that were
// still feeding, whose sbd then expired rather than being released. That is a reset.
//
// The second is sbd's: a host feeding a watchdog inside a cluster that cannot see a majority
// resets. sbd expires in five seconds and a reconcile tick is ten apart, so a host left in that
// state between two passes is a host that resets.
func (p *labWatchdogPool) physics() {
	unarmedNode := false
	for _, host := range p.lab.hosts {
		id := host.State.HostID
		if p.running[id] && !p.feeding[id] {
			unarmedNode = true
		}
	}
	if p.bound && unarmedNode {
		for _, host := range p.lab.hosts {
			id := host.State.HostID
			if !p.running[id] {
				continue
			}
			p.emerg = append(p.emerg, id)
			if p.feeding[id] {
				p.resets = append(p.resets, id)
			}
			p.running[id], p.feeding[id] = false, false
		}
	}
	for _, host := range p.lab.hosts {
		id := host.State.HostID
		if p.feeding[id] && p.running[id] && !p.quorum(id).Quorate {
			p.resets = append(p.resets, id)
			p.feeding[id], p.running[id], p.enabled[id] = false, false, p.enabled[id]
		}
	}
}

// pass is one host's reconcile tick, in the order Run does it: arming first, so a host that must
// stop being armed has the pool's record say so before its cluster goes down under it, and a
// host that has just booted disables sbd before its cluster starts.
func (p *labWatchdogPool) pass(host *Store) error {
	p.current = host.State.HostID
	if err := ObservePoolQuorum(context.Background(), host); err != nil {
		return err
	}
	armErr := ReconcilePoolWatchdogArming(context.Background(), host)
	if err := ReconcilePoolCluster(context.Background(), host); err != nil {
		return err
	}
	p.physics()
	return armErr
}

// round is one tick on every host, followed by the master's replica reaching the members.
func (p *labWatchdogPool) round() {
	p.t.Helper()
	for _, host := range p.lab.hosts {
		if err := p.pass(host); err != nil {
			p.t.Fatalf("%s could not reconcile: %v", host.State.HostID, err)
		}
	}
	syncPoolMembershipForTest(p.t, p.lab)
}

// settle backdates the settle clock on every host, which is what a minute of an unchanged
// Corosync membership does.
func (p *labWatchdogPool) settle() {
	p.t.Helper()
	for _, host := range p.lab.hosts {
		p.current = host.State.HostID
		if err := ObservePoolQuorum(context.Background(), host); err != nil {
			p.t.Fatal(err)
		}
	}
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	for _, host := range p.lab.hosts {
		if since, ok := poolQuorumRingSince[host.dir]; ok && !since.IsZero() {
			poolQuorumRingSince[host.dir] = time.Now().Add(-2 * protocol.PoolWatchdogBound(0))
		}
	}
}

func (p *labWatchdogPool) armedHosts() []string {
	var armed []string
	for _, host := range p.lab.hosts {
		if p.feeding[host.State.HostID] {
			armed = append(armed, host.State.HostID)
		}
	}
	sort.Strings(armed)
	return armed
}

// labFencingPool is three hosts with hardware watchdogs and Host fencing on, with no cluster
// yet: the state a pool is in the moment Nex Core is asked to arm it.
func labFencingPool(t *testing.T) (*transferLab, *labWatchdogPool) {
	t.Helper()
	return labFencingPoolOf(t, 2)
}

func labFencingPoolOf(t *testing.T, members int) (*transferLab, *labWatchdogPool) {
	t.Helper()
	lab := newTransferLab(t, members)
	watchdogs := map[string]protocol.PoolHostWatchdog{}
	for _, host := range lab.hosts {
		watchdogs[host.State.HostID] = protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogHardware,
			Device: "watchdog-" + host.State.HostID, Driver: "iTCO_wdt", TimeoutSeconds: 30}
	}
	labWatchdogs(t, watchdogs)
	syncPoolMembershipForTest(t, lab)
	pool := newLabWatchdogPool(t, lab)
	for _, host := range lab.hosts {
		if err := ReportPoolWatchdog(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	return lab, pool
}

// armWholePool runs the rollout to completion the way a live pool does: rounds of ticks, with a
// settled Corosync membership between them.
func armWholePool(t *testing.T, lab *transferLab, pool *labWatchdogPool) {
	t.Helper()
	for i := 0; i < 12 && len(pool.armedHosts()) < len(lab.hosts); i++ {
		pool.settle()
		pool.round()
	}
	if len(pool.armedHosts()) != len(lab.hosts) {
		t.Fatalf("the pool did not finish arming: %v", pool.armedHosts())
	}
	if len(pool.resets) > 0 {
		t.Fatalf("arming reset %v", pool.resets)
	}
}

// The whole of the slice's safety in one sequence: a pool forms its cluster unarmed, arms
// nothing until it has watched that cluster hold one whole membership for a proof bound, and
// then arms exactly one host at a time — because arming means restarting that host's cluster
// services, and two hosts doing that at once is a pool that has lost its majority for nothing.
func TestAPoolFormsItsClusterUnarmedAndThenArmsOneHostAtATime(t *testing.T) {
	lab, pool := labFencingPool(t)
	master := lab.host("master-1")

	pool.round()
	if len(pool.armedHosts()) > 0 {
		t.Fatalf("a host armed before this pool's cluster had settled: %v", pool.armedHosts())
	}
	for _, host := range lab.hosts {
		if !pool.running[host.State.HostID] || len(pool.nodes[host.State.HostID]) != 3 {
			t.Fatalf("%s did not form this pool's whole cluster: %v", host.State.HostID, pool.nodes[host.State.HostID])
		}
	}
	if row := memberRow(t, master, "master-1").WatchdogArmed; row == nil || row.Armed ||
		!strings.Contains(row.Detail, "waiting for this pool's cluster") {
		t.Fatalf("the master did not say why it is not armed yet: %#v", row)
	}

	for round := 1; round <= 3; round++ {
		before := len(pool.armedHosts())
		pool.settle()
		pool.round()
		if got := len(pool.armedHosts()) - before; got != 1 {
			t.Fatalf("round %d armed %d hosts, not one: %v", round, got, pool.armedHosts())
		}
	}
	for _, hostID := range []string{"master-1", "member-1", "member-2"} {
		row := memberRow(t, master, hostID).WatchdogArmed
		if row == nil || !row.Armed || row.ArmedAt.IsZero() {
			t.Fatalf("%s is not armed after the rollout: %#v", hostID, row)
		}
	}
	if len(pool.resets) > 0 {
		t.Fatalf("arming reset %v", pool.resets)
	}
	before := len(pool.resets)
	pool.settle()
	pool.round()
	pool.round()
	if len(pool.resets) != before || len(pool.armedHosts()) != 3 {
		t.Fatalf("a settled armed pool disturbed itself: resets %v armed %v", pool.resets, pool.armedHosts())
	}
}

// F1. The cold-start defence cannot rest on a network call. A host that boots with sbd still
// enabled from the last time it armed — which is every previously armed host — must disable it
// before its cluster starts, whether or not it can reach its master to record anything. The
// state this reproduces is a whole-pool power-on where the master is not up yet.
// (Reported by independent review of slice 4c.)
func TestABootingHostDisablesSbdEvenWhenItCannotReachItsMaster(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	member := lab.host("member-2")

	// The host reboots: sbd is still enabled, nothing is running.
	pool.stop("member-2")
	pool.enabled["member-2"] = true
	ClearPoolQuorumForTest(member)
	// And its master is unreachable, so nothing it reports can be recorded.
	lab.drop("master-1", "!*")
	t.Cleanup(func() { lab.drop("master-1", "") })

	// The arming pass on its own, before any cluster pass: a host that is not feeding
	// anything must disable sbd whether or not it can record the withdrawal. This is the
	// agent half of the fix, and asserting it here rather than only the end state is what
	// makes it load-bearing — the cluster pass disables sbd too, and would hide it.
	pool.current = member.State.HostID
	if err := ReconcilePoolWatchdogArming(context.Background(), member); err == nil {
		t.Fatal("a host that could not record anything reported success")
	}
	if pool.enabled["member-2"] {
		t.Fatal("the arming pass left sbd enabled because it could not reach the master, so the next cluster start arms this host alone")
	}
	pool.enabled["member-2"] = true // Put it back and prove the whole pass over again.
	if err := pool.pass(member); err == nil {
		t.Fatal("a host that could not record anything reported success")
	}
	if pool.enabled["member-2"] {
		t.Fatal("a booting host started its cluster with sbd still enabled, so it comes up armed and alone")
	}
	if pool.feeding["member-2"] {
		t.Fatal("a booting host came up feeding its watchdog while its master was unreachable")
	}
	if !pool.running["member-2"] {
		t.Fatal("the host did not start this pool's cluster at all")
	}
	if len(pool.resets) > 0 {
		t.Fatalf("a booting host reset: %v", pool.resets)
	}
}

// F3. Turning Host fencing off, or anything else that takes the pool below three usable hosts,
// must not reset a host. Disarming has latency — the device is released only when that host's
// cluster next stops — and expected votes do not shrink because somebody else left, so an
// unordered teardown leaves the last host standing inquorate and still feeding.
// (Reported by independent review of slice 4c.)
func TestTurningHostFencingOffTearsThePoolDownWithoutResettingAnybody(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	master := lab.host("master-1")

	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	syncPoolMembershipForTest(t, lab)
	for i := 0; i < 12 && len(pool.armedHosts()) > 0; i++ {
		pool.settle()
		pool.round()
	}
	if armed := pool.armedHosts(); len(armed) > 0 {
		t.Fatalf("hosts were still feeding their watchdogs after host fencing was turned off: %v", armed)
	}
	for i := 0; i < 3; i++ {
		pool.round()
	}
	for _, host := range lab.hosts {
		if pool.running[host.State.HostID] {
			t.Fatalf("%s kept this pool's cluster running after host fencing was turned off", host.State.HostID)
		}
	}
	if len(pool.resets) > 0 {
		t.Fatalf("turning host fencing off reset %v", pool.resets)
	}
	if row := memberRow(t, master, "master-1").WatchdogArmed; row == nil || row.Armed ||
		!strings.Contains(row.Detail, "turned off") {
		t.Fatalf("the pool did not say why it disarmed: %#v", row)
	}
}

// F3, the other trigger. Planned work must never reset a healthy host: a three-host pool that
// puts one host in maintenance has two left, which has no majority side, so the whole pool
// disarms — one host at a time, everybody staying in the cluster until nobody is feeding.
func TestMaintenanceOnAThreeHostPoolDisarmsItWithoutResettingAnybody(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	master := lab.host("master-1")

	generation := memberRecord(t, master, "member-2").Generation
	if result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "member-2", true, generation)); result.Status != "succeeded" {
		t.Fatalf("draining a host = %#v", result)
	}
	syncPoolMembershipForTest(t, lab)
	for i := 0; i < 12 && len(pool.armedHosts()) > 0; i++ {
		pool.settle()
		pool.round()
	}
	if armed := pool.armedHosts(); len(armed) > 0 {
		t.Fatalf("hosts were still feeding their watchdogs after this pool fell to two: %v", armed)
	}
	for i := 0; i < 3; i++ {
		pool.round()
	}
	if len(pool.resets) > 0 {
		t.Fatalf("maintenance reset %v", pool.resets)
	}
	row := memberRow(t, master, "master-1").WatchdogArmed
	if row == nil || row.Armed || !strings.Contains(row.Detail, "three or more") {
		t.Fatalf("the pool did not say why it disarmed: %#v", row)
	}
	if state := PoolFenceClusterFor(master, labMembership(t, master), poolLastQuorum(master.dir)); state.State != protocol.PoolClusterNotAvailable {
		t.Fatalf("a pool of two reported its cluster as %#v", state)
	}
}

// F5. In a pool of four or five, a drained host must leave before the survivors reload it out of
// their nodelist: dropping it while it is still feeding raises their expected votes above it and
// resets it, which is the same planned-work reset in a different order.
// (Reported by independent review of slice 4c.)
func TestADrainedHostLeavesBeforeTheSurvivorsReloadItOut(t *testing.T) {
	lab, pool := labFencingPoolOf(t, 3)
	armWholePool(t, lab, pool)
	master := lab.host("master-1")

	generation := memberRecord(t, master, "member-3").Generation
	if result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "member-3", true, generation)); result.Status != "succeeded" {
		t.Fatalf("draining a host = %#v", result)
	}
	syncPoolMembershipForTest(t, lab)
	for i := 0; i < 12; i++ {
		pool.settle()
		pool.round()
		// While the drained host is still feeding, it must still be in every survivor's
		// nodelist: that is what keeps their expected votes from rising above it.
		if pool.feeding["member-3"] {
			for _, host := range lab.hosts[:3] {
				if !slices.Contains(pool.nodes[host.State.HostID], "member-3") {
					t.Fatalf("%s reloaded a still-armed drained host out of its nodelist", host.State.HostID)
				}
			}
		}
	}
	if pool.feeding["member-3"] || pool.running["member-3"] {
		if pool.feeding["member-3"] {
			t.Fatal("the drained host never stopped feeding its watchdog")
		}
	}
	if slices.Contains(pool.armedHosts(), "member-3") {
		t.Fatal("the drained host is still armed")
	}
	for _, hostID := range []string{"master-1", "member-1", "member-2"} {
		if !pool.feeding[hostID] {
			t.Fatalf("%s stopped feeding its watchdog because another host went into maintenance", hostID)
		}
	}
	if len(pool.resets) > 0 {
		t.Fatalf("draining one host of a four-host pool reset %v", pool.resets)
	}
	if len(pool.emerg) > 0 {
		t.Fatalf("the pool's cluster was shut down by the quorate side's bound: %v", pool.emerg)
	}
}

// F5, the case the single-drain test cannot reach. Draining two hosts of four takes the pool
// below three usable hosts, so it tears down — and a drained host that has stopped feeding is no
// longer in the nodelist either. Leaving on that alone is the reset: with the teardown running
// nobody reloads a nodelist, so the survivors still expect four votes and the two that are left
// go inquorate while they are still feeding.
// (Reported by independent review of slice 4c, rev 4.)
func TestDrainingTwoHostsOfFourTearsThePoolDownWithoutResettingAnybody(t *testing.T) {
	lab, pool := labFencingPoolOf(t, 3)
	armWholePool(t, lab, pool)
	master := lab.host("master-1")

	for _, hostID := range []string{"master-1", "member-1"} {
		generation := memberRecord(t, master, hostID).Generation
		if result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", hostID, true, generation)); result.Status != "succeeded" {
			t.Fatalf("draining %s = %#v", hostID, result)
		}
		syncPoolMembershipForTest(t, lab)
	}

	for i := 0; i < 16 && len(pool.armedHosts()) > 0; i++ {
		pool.settle()
		pool.round()
	}
	if armed := pool.armedHosts(); len(armed) > 0 {
		t.Fatalf("hosts were still feeding their watchdogs after this pool fell below three usable hosts: %v", armed)
	}
	for i := 0; i < 3; i++ {
		pool.round()
	}
	for _, host := range lab.hosts {
		if pool.running[host.State.HostID] {
			t.Fatalf("%s kept this pool's cluster running after the teardown", host.State.HostID)
		}
	}
	if len(pool.resets) > 0 {
		t.Fatalf("draining two hosts of four reset %v", pool.resets)
	}
	if len(pool.emerg) > 0 {
		t.Fatalf("the pool's cluster was shut down by the quorate side's bound: %v", pool.emerg)
	}
}

// F2. The rejoin rule must not refuse every proof for ever. A Corosync membership event this
// host watched is not evidence that the hosts which stayed have rejoined — and the pool's own
// arming rollout is a sequence of membership events, so a blanket refresh poisoned every host
// that armed before the last one.
//
// This is the independent reviewer's scenario, driven the way the rollout drives it: one host
// restarts its cluster services to arm, which is two membership events with the target present
// throughout, and only then does the target die for real.
// (Reported by independent review of slice 4c.)
func TestAnotherHostsArmingRestartDoesNotBlockProofsAgainstTheMaster(t *testing.T) {
	lab, _ := labArmedPool(t)
	prover, master := lab.host("member-1"), lab.host("master-1")
	generation := peerState(t, master).Membership.Generation
	target := memberRow(t, master, "master-1")
	bound := protocol.PoolWatchdogBound(target.WatchdogArmed.TimeoutSeconds)

	// member-2 restarts its cluster to arm: it leaves the ring, then rejoins it. The target
	// is in both readings and has never been away.
	labQuorumRing(t, "2.1", "master-1", "member-1")
	if err := ObservePoolQuorum(context.Background(), prover); err != nil {
		t.Fatal(err)
	}
	labQuorumRing(t, "2.2", "master-1", "member-1", "member-2")
	if err := ObservePoolQuorum(context.Background(), prover); err != nil {
		t.Fatal(err)
	}

	// Later the master dies for real.
	labQuorumRing(t, "2.3", "member-1", "member-2")
	if err := ObservePoolQuorum(context.Background(), prover); err != nil {
		t.Fatal(err)
	}
	labHeld(prover, "master-1", bound+time.Second)
	proof, err := labProof(t, prover, target, generation)
	if err != nil || !proof.ExclusionVerified {
		t.Fatalf("another host's arming restart blocked the proof against the master: %v", err)
	}
	if err := PoolWatchdogUsable(prover.dir, target); err != nil {
		t.Fatalf("readiness and the proof disagree about the same host: %v", err)
	}

	// A window this host was not watching is still not continuity: the clock restarts, so
	// the bound has to elapse again before anything may rest on it.
	labStaleObservation(prover, 2*poolQuorumGap)
	labQuorumRing(t, "2.4", "member-1", "member-2")
	if err := ObservePoolQuorum(context.Background(), prover); err != nil {
		t.Fatal(err)
	}
	if _, err := labProof(t, prover, target, generation); err == nil {
		t.Fatal("a proof rested on a window this host was not watching")
	}
}

// F2, the other half of the reviewer's scenario and the one that must not deadlock: the target
// genuinely rejoins, so its record is no longer evidence — and the pool then re-confirms it
// through production code, with no test helper involved, and the proof becomes grantable again.
// (Reported by independent review of slice 4c.)
func TestARejoinedHostIsProvableAgainOnceThePoolReconfirmsIt(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	master, prover := lab.host("master-1"), lab.host("member-1")
	generation := labMembership(t, master).Generation
	bound := protocol.PoolWatchdogBound(vmhost.SBDWatchdogTimeout)

	// The master's cluster bounces: it leaves the prover's partition and comes back. Its
	// armed record predates the return, so nothing may rest on it yet.
	pool.stop("master-1")
	pool.round()
	pool.start("master-1")
	pool.settle()
	if err := PoolWatchdogUsable(prover.dir, memberRow(t, master, "master-1")); err == nil {
		t.Fatal("a record written before the target rebooted was usable")
	}

	// The pool re-confirms: the master's own arming pass verifies sbd on the membership it
	// is in now and re-reports, the master records it, and the replica reaches the member.
	pool.round()
	pool.round()
	target := memberRow(t, master, "master-1")
	if err := PoolWatchdogUsable(prover.dir, target); err != nil {
		t.Fatalf("a re-confirmed host is still unprovable, so the feature grants no proofs at all: %v", err)
	}

	// And now the real partition.
	pool.current = prover.State.HostID
	pool.stop("master-1")
	if err := ObservePoolQuorum(context.Background(), prover); err != nil {
		t.Fatal(err)
	}
	labHeld(prover, "master-1", bound+time.Second)
	proof, err := labProof(t, prover, target, generation)
	if err != nil || !proof.ExclusionVerified {
		t.Fatalf("a re-confirmed, partitioned master produced no proof: %v", err)
	}
}

// V4. The ring bucket is the production re-confirmation, so it needs a test in which it is the
// only thing that could have moved the record: a peer restarts its cluster to arm, the target
// keeps feeding the same device throughout and its record is otherwise identical, and the target
// must still re-confirm — because a survivor that watched the membership change has to be able to
// act on that record again.
// (Reported by independent review of slice 4c, rev 2.)
func TestAnArmedHostReconfirmsItselfAfterAMembershipChange(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	master, prover := lab.host("master-1"), lab.host("member-1")
	generation := labMembership(t, master).Generation
	bound := protocol.PoolWatchdogBound(vmhost.SBDWatchdogTimeout)

	pool.settle()
	pool.round()
	before := *memberRow(t, master, "master-1").WatchdogArmed
	if !before.Armed || before.ArmedAt.IsZero() {
		t.Fatalf("the target is not armed: %#v", before)
	}

	// A peer bounces its cluster. The target never stops feeding — its own sbd keeps the same
	// device the whole way through — so nothing but the re-confirmation can change its record.
	pool.stop("member-2")
	pool.round()
	pool.start("member-2")
	pool.settle()
	pool.round()
	if !pool.feeding["master-1"] {
		t.Fatal("the target stopped feeding, so this test no longer isolates the re-confirmation")
	}
	after := *memberRow(t, master, "master-1").WatchdogArmed
	if !after.Armed || !after.ArmedAt.After(before.ArmedAt) {
		t.Fatalf("the target never re-confirmed after a membership event it stayed through: %v then %v",
			before.ArmedAt, after.ArmedAt)
	}
	if after.Kind != before.Kind || after.Device != before.Device ||
		after.TimeoutSeconds != before.TimeoutSeconds || after.FencingGeneration != before.FencingGeneration {
		t.Fatalf("more than the confirmation changed: %#v then %#v", before, after)
	}

	// And a survivor can act on it: the same host, partitioned, is provable.
	pool.current = prover.State.HostID
	pool.stop("master-1")
	if err := ObservePoolQuorum(context.Background(), prover); err != nil {
		t.Fatal(err)
	}
	labHeld(prover, "master-1", bound+time.Second)
	if proof, err := labProof(t, prover, after2member(t, master, "master-1"), generation); err != nil || !proof.ExclusionVerified {
		t.Fatalf("a re-confirmed host was not provable after it was partitioned: %v", err)
	}
}

func after2member(t *testing.T, s *Store, hostID string) protocol.PoolMember {
	t.Helper()
	return memberRow(t, s, hostID)
}

// V2. A blind window on the *proving* host must not permanently block proofs against a host that
// never moved. The prover missing readings says nothing about the target's cluster, and the
// target's own ring never changed, so the ring bucket alone left it unprovable for ever.
// (Reported by independent review of slice 4c, rev 2.)
func TestABlindWindowOnTheProverDoesNotPermanentlyBlockProofs(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	master, prover := lab.host("master-1"), lab.host("member-1")
	generation := labMembership(t, master).Generation
	bound := protocol.PoolWatchdogBound(vmhost.SBDWatchdogTimeout)

	// The prover misses readings for longer than its observation gap. Nothing else happens:
	// no membership event anywhere, and the target goes on feeding the same device.
	labStaleObservation(prover, 2*poolQuorumGap)
	pool.settle()
	pool.round()

	// The target re-confirms on its own clock — once per proof bound — so the evidence the
	// prover holds becomes newer than the blind window it could not see through.
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		for i := range state.Membership.Members {
			if row := state.Membership.Members[i].WatchdogArmed; row != nil && row.Armed {
				row.ArmedAt = row.ArmedAt.Add(-2 * protocol.PoolWatchdogBound(0))
			}
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	pool.settle()
	pool.round()
	pool.round()

	pool.current = prover.State.HostID
	pool.stop("master-1")
	if err := ObservePoolQuorum(context.Background(), prover); err != nil {
		t.Fatal(err)
	}
	labHeld(prover, "master-1", bound+time.Second)
	target := memberRow(t, master, "master-1")
	// Load-bearing: the backdated record is also the prover's evidence source, so the test
	// must show the *host's own pass* rewrote it. Without the time bucket the record stays
	// backdated and this fails before the proof is even asked for.
	if !target.WatchdogArmed.ArmedAt.After(time.Now().Add(-protocol.PoolWatchdogBound(0))) {
		t.Fatalf("the armed host never re-confirmed after a bound, so a survivor has no newer evidence than the blind window: %v",
			target.WatchdogArmed.ArmedAt)
	}
	if err := PoolWatchdogUsable(prover.dir, target); err != nil {
		t.Fatalf("one missed reading on the proving host permanently blocked proofs against a healthy armed host: %v", err)
	}
	if proof, err := labProof(t, prover, target, generation); err != nil || !proof.ExclusionVerified {
		t.Fatalf("no proof after the pool re-confirmed: %v", err)
	}
}

// V3. A host lost in the middle of a teardown must not leave the rest feeding for ever with the
// record saying they are not armed. The remaining hosts finish once their own departure would not
// take a still-feeding peer below quorum, and while any host is still feeding the pool says so.
// (Reported by independent review of slice 4c, rev 2.)
func TestATeardownIsReportedHonestlyWhenALostHostBlocksIt(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	master := lab.host("master-1")

	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	syncPoolMembershipForTest(t, lab)
	// The host is lost *before* any host has taken a turn, which is the ordering that makes
	// the arithmetic bite: two hosts of three are left feeding, and neither can stop without
	// taking the other below quorum. Refusing is correct; saying it is progress is not.
	pool.running["member-2"], pool.feeding["member-2"] = false, false
	pool.ring++

	for i := 0; i < 8; i++ {
		pool.settle()
		pool.round()
	}
	if armed := pool.armedHosts(); len(armed) != 2 {
		t.Fatalf("two hosts should still be feeding and unable to stop: %v", armed)
	}
	if len(pool.resets) > 0 {
		t.Fatalf("losing a host before any teardown turn reset %v", pool.resets)
	}
	state := PoolFenceClusterFor(master, labMembership(t, master), poolLastQuorum(master.dir))
	if state.State != protocol.PoolClusterArmed || !strings.Contains(state.Detail, "blocked by member-2") ||
		!strings.Contains(state.Detail, "stay armed") {
		t.Fatalf("a blocked teardown did not say so: %#v", state)
	}

	// The lost host returns: the teardown finishes, one host at a time, resetting nobody.
	pool.start("member-2")
	for i := 0; i < 12 && len(pool.armedHosts()) > 0; i++ {
		pool.settle()
		pool.round()
	}
	if armed := pool.armedHosts(); len(armed) > 0 {
		t.Fatalf("the teardown did not finish once the lost host was back: %v", armed)
	}
	if len(pool.resets) > 0 || len(pool.emerg) > 0 {
		t.Fatalf("finishing the teardown reset %v (emerg %v)", pool.resets, pool.emerg)
	}
}

// The last line of defence if a CIB ever carries stonith-watchdog-timeout from somewhere else — an
// older Nex Core, a clustered-storage cluster, an administrator. Nex Core never writes it, and a
// host will not restart its cluster while it is there, because starting a cluster with that property
// set and no sbd on this node is what takes the cluster down on every node.
func TestNoHostRestartsItsClusterWhileAForeignWatchdogBoundIsSet(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	master := lab.host("master-1")

	// A bound appears from outside, and the master's policy pass is not reached yet.
	pool.bound = true
	previous := applyPoolClusterProperties
	applyPoolClusterProperties = func(context.Context) error { return nil } // it does not remove it
	t.Cleanup(func() { applyPoolClusterProperties = previous })

	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	syncPoolMembershipForTest(t, lab)
	for i := 0; i < 4; i++ {
		pool.settle()
		for _, host := range lab.hosts {
			err := pool.pass(host)
			if host == master {
				if err == nil || !strings.Contains(err.Error(), "policy did not read back") {
					t.Fatalf("master accepted a foreign watchdog bound: %v", err)
				}
			} else if err != nil {
				t.Fatalf("%s could not reconcile: %v", host.State.HostID, err)
			}
		}
		syncPoolMembershipForTest(t, lab)
	}
	if len(pool.armedHosts()) != 3 {
		t.Fatalf("a host restarted its cluster while a foreign bound was set: %v", pool.armedHosts())
	}
	if len(pool.emerg) > 0 || len(pool.resets) > 0 {
		t.Fatalf("the cluster was shut down or a host reset: emerg %v resets %v", pool.emerg, pool.resets)
	}

	// The master removes it, and the teardown proceeds.
	applyPoolClusterProperties = func(context.Context) error { pool.bound = false; return nil }
	for i := 0; i < 12 && len(pool.armedHosts()) > 0; i++ {
		pool.settle()
		pool.round()
	}
	if armed := pool.armedHosts(); len(armed) > 0 {
		t.Fatalf("the teardown never finished once the bound was removed: %v", armed)
	}
	if len(pool.emerg) > 0 || len(pool.resets) > 0 {
		t.Fatalf("finishing after the bound was removed reset %v (emerg %v)", pool.resets, pool.emerg)
	}
}

// R5. A hung `systemctl stop pacemaker` must leave the host armed and a member, say so, and never
// be escalated by killing the watchdog daemon. Measured in the nested laboratory: that stop hung
// indefinitely on those guests more than once.
// (Reported by independent review of slice 4c, rev 3.)
func TestAHungClusterStopLeavesTheHostArmedAndSaysSo(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	master := lab.host("master-1")

	previous := restartPoolCluster
	restartPoolCluster = func(context.Context) error { return vmhost.ErrPoolClusterStopStalled }
	t.Cleanup(func() { restartPoolCluster = previous })

	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	syncPoolMembershipForTest(t, lab)
	for i := 0; i < 4; i++ {
		pool.settle()
		pool.round()
	}
	if len(pool.armedHosts()) != 3 {
		t.Fatalf("a host whose stop hung stopped feeding anyway: %v", pool.armedHosts())
	}
	if len(pool.resets) > 0 {
		t.Fatalf("a hung stop reset %v", pool.resets)
	}
	row := memberRow(t, master, "master-1").WatchdogArmed
	if row == nil || row.Armed || !row.Feeding || !strings.Contains(row.Detail, "did not stop when asked") {
		t.Fatalf("a hung stop was not reported: %#v", row)
	}
	state := PoolFenceClusterFor(master, labMembership(t, master), poolLastQuorum(master.dir))
	if state.State != protocol.PoolClusterDegraded || !strings.Contains(state.Detail, "master-1") {
		t.Fatalf("a stalled teardown did not name the host: %#v", state)
	}
}

// Clustered storage owns this host's one corosync.conf and writes a policy that keeps an
// inquorate host running, which is the opposite of what a watchdog needs. So the interlock runs
// both ways, in Core: such a pool is never given a watchdog cluster and says why, and a pool
// that has one refuses to take a clustered repository on.
func TestClusteredStorageAndWatchdogFencingExcludeEachOther(t *testing.T) {
	lab, pool := labFencingPool(t)
	armWholePool(t, lab, pool)
	master := lab.host("master-1")

	result := ExecuteStorageClusterRequest(context.Background(), master, protocol.StorageClusterRequest{
		RequestID: "req-1", PoolID: master.State.PoolID, HostID: master.State.HostID, Action: "apply"})
	if result.Status != "failed" || !strings.Contains(result.Error, "Turn Host fencing off") {
		t.Fatalf("a clustered repository was accepted on a pool whose hosts are armed: %#v", result)
	}

	// And a pool that already runs clustered storage is left exactly as it was.
	storageClusterMu.Lock()
	state, err := loadStorageClusters(master)
	if err == nil {
		state.Operations["op-1"] = storageClusterOperation{RequestID: "op-1"}
		err = saveStorageClusters(master, state)
	}
	storageClusterMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	_, want, detail := poolClusterIntent(master, labMembership(t, master))
	if want || !strings.Contains(detail, "clustered storage") {
		t.Fatalf("a clustered-storage pool was given a watchdog cluster: %v %q", want, detail)
	}
}

// A pool bigger than this code has been exercised on is reported, and no host is left out of the
// cluster the rest of the pool makes quorum decisions with. Silently excluding one would arm
// hosts against an arithmetic that host never agreed to.
func TestAPoolAboveTheTestedCeilingIsReportedAndNoHostIsExcluded(t *testing.T) {
	lab, _ := labFencingPool(t)
	master := lab.host("master-1")
	membership := labMembership(t, master)
	for i := 0; i < protocol.PoolWatchdogClusterHosts; i++ {
		membership.Members = append(membership.Members, protocol.PoolMember{
			HostID: "extra-" + string(rune('a'+i)), Status: "ready", PeerOrigin: "https://198.51.100.99:9444"})
	}
	nodes, want, detail := poolClusterIntent(master, membership)
	if want || !strings.Contains(detail, "no host is armed") {
		t.Fatalf("a pool above the tested ceiling formed a cluster anyway: %v %q", want, detail)
	}
	if len(nodes) != len(membership.Members) {
		t.Fatalf("a host was left out of the pool's own quorum: %v", nodes)
	}
}

func labMembership(t *testing.T, s *Store) protocol.PoolMembership {
	t.Helper()
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	return membership
}

// The pool's ring runs on the management network, so a staged management-network edit on an
// armed host partitions it and its watchdog resets it. That is planned work resetting a healthy
// host, so Core refuses it at the host rather than documenting the hazard and hoping.
func TestAManagementNetworkChangeIsRefusedWhileThisHostIsArmed(t *testing.T) {
	armed := true
	previous := localWatchdogArmed
	localWatchdogArmed = func(context.Context) (bool, error) { return armed, nil }
	t.Cleanup(func() { localWatchdogArmed = previous })
	if err := managementNetworkUnarmed(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "Turn Host fencing off") {
		t.Fatalf("a management-network change was allowed on an armed host: %v", err)
	}
	armed = false
	if err := managementNetworkUnarmed(context.Background()); err != nil {
		t.Fatalf("a management-network change was refused on a host that is not armed: %v", err)
	}
}

// A host that has just completed its CSR join is a ready member with no peer origin yet: the
// origin arrives later, over /pool/advertise. Nex Core forms no cluster while it does not know
// a host's address — but the hosts already in one must stay in it. Dropping the nodelist along
// with the intent made every unarmed host stop its cluster under an armed peer, which is the
// last host standing as a minority of one, still feeding: a healthy host resetting because
// somebody else enrolled.
// (Reported by independent review of slice 4c.)
func TestAJoiningHostWithNoAddressYetDoesNotTearThePoolDown(t *testing.T) {
	lab, pool := labFencingPool(t)
	master := lab.host("master-1")
	for i := 0; i < 12 && len(pool.armedHosts()) == 0; i++ {
		pool.settle()
		pool.round()
	}
	if armed := pool.armedHosts(); len(armed) != 1 {
		t.Fatalf("this test needs a part-way rollout, and the pool armed %v", armed)
	}

	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Membership.Generation++
		for i := range state.Membership.Members {
			state.Membership.Members[i].Generation = state.Membership.Generation
		}
		state.Membership.Members = append(state.Membership.Members, protocol.PoolMember{HostID: "member-9", Hostname: "member-9",
			CertificateSHA256: strings.Repeat("ab", 32), Generation: state.Membership.Generation, Status: "ready"})
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	syncPoolMembershipForTest(t, lab)

	// The pool stands down, because it will not make quorum decisions without a host it
	// counts as ready — but it stands down the way any teardown does: the armed host puts
	// its own device down first, and nobody stops a cluster under a peer that is feeding.
	for i := 0; i < 3; i++ {
		pool.round()
		for _, host := range lab.hosts {
			if pool.running[host.State.HostID] && len(pool.nodes[host.State.HostID]) == 0 {
				t.Fatalf("%s is in a cluster it does not know the members of", host.State.HostID)
			}
		}
	}
	if len(pool.resets) > 0 {
		t.Fatalf("a host enrolling reset %v", pool.resets)
	}
}
