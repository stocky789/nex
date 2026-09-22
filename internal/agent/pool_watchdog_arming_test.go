// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// labArming stands in for sbd on every host: one armed flag per host, and a record of every
// arm and disarm, so a test can assert that a host was never asked to do either.
type labArming struct {
	armed    map[string]bool
	current  string
	actions  []string
	nowayout bool
	armErr   error
	// pending is Debian's sbd.service: it refuses a manual start and exists only as a
	// dependency of corosync.service, so a host whose cluster is already up is enabled and
	// pending until that cluster is restarted.
	pending bool
}

func newLabArming(t *testing.T) *labArming {
	t.Helper()
	lab := &labArming{armed: map[string]bool{}}
	previousArm, previousDisarm, previousArmed := armLocalWatchdog, disarmLocalWatchdog, localWatchdogArmed
	// The fakes are keyed by the device name, which each lab host's watchdog carries, so
	// three hosts in one process do not share one flag.
	armLocalWatchdog = func(_ context.Context, w protocol.PoolHostWatchdog) error {
		if lab.armErr != nil {
			return lab.armErr
		}
		if lab.pending && !lab.armed[w.Device] {
			return vmhost.ErrWatchdogArmingPending
		}
		if !lab.armed[w.Device] {
			lab.actions = append(lab.actions, "arm "+w.Device)
		}
		lab.armed[w.Device] = true
		return nil
	}
	disarmLocalWatchdog = func(_ context.Context, w protocol.PoolHostWatchdog) error {
		if lab.nowayout {
			return errPoolWatchdog("this host's watchdog driver was built with nowayout")
		}
		if lab.armed[w.Device] {
			lab.actions = append(lab.actions, "disarm "+w.Device)
		}
		lab.armed[w.Device] = false
		return nil
	}
	localWatchdogArmed = func(context.Context) (bool, error) { return lab.armed[lab.current], nil }
	t.Cleanup(func() {
		armLocalWatchdog, disarmLocalWatchdog, localWatchdogArmed = previousArm, previousDisarm, previousArmed
	})
	return lab
}

// current is the device the host being reconciled owns; the helper below sets it.
func (l *labArming) reconcile(t *testing.T, host *Store) {
	t.Helper()
	if err := l.tryReconcile(host); err != nil {
		t.Fatalf("%s could not reconcile its watchdog arming: %v", host.State.HostID, err)
	}
}

// tryReconcile is the same pass where the failure is the point: a host that cannot reach its
// master to record a withdrawal reports that as an error and stays armed.
func (l *labArming) tryReconcile(host *Store) error {
	l.current = "watchdog-" + host.State.HostID
	return ReconcilePoolWatchdogArming(context.Background(), host)
}

// labQuorum makes every host read the same Corosync partition.
func labQuorum(t *testing.T, quorate bool, hosts ...string) {
	t.Helper()
	labQuorumRing(t, "1.1", hosts...)
	if !quorate {
		previous := localQuorum
		localQuorum = func(context.Context) (vmhost.PoolQuorum, error) {
			return vmhost.PoolQuorum{Observed: true}, nil
		}
		t.Cleanup(func() { localQuorum = previous })
	}
}

// labQuorumRing is the same with Corosync's ring identifier, which changes on every membership
// event and is what catches a flap between two readings.
func labQuorumRing(t *testing.T, ring string, hosts ...string) {
	t.Helper()
	previous := localQuorum
	localQuorum = func(context.Context) (vmhost.PoolQuorum, error) {
		return vmhost.PoolQuorum{Observed: true, Quorate: true, RingID: ring, Hosts: hosts}, nil
	}
	t.Cleanup(func() { localQuorum = previous })
}

// labStaleObservation backdates this host's last reading, which is what a stalled reconcile
// pass does.
func labStaleObservation(s *Store, age time.Duration) {
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	poolQuorumSeen[s.dir] = time.Now().Add(-age)
}

// labArmedPool is a three-host pool where every host has a hardware watchdog, the quorum is
// whole, and every host has armed and reported it.
func labArmedPool(t *testing.T) (*transferLab, *labArming) {
	t.Helper()
	return labArmedPoolOf(t, 2)
}

// labArmedPoolOf is the same with a chosen number of members, for the cases that need a pool
// that still has a majority after one host is taken out of it.
func labArmedPoolOf(t *testing.T, members int) (*transferLab, *labArming) {
	t.Helper()
	lab := newTransferLab(t, members)
	watchdogs := map[string]protocol.PoolHostWatchdog{}
	var hostIDs []string
	for _, host := range lab.hosts {
		watchdogs[host.State.HostID] = protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogHardware,
			Device: "watchdog-" + host.State.HostID, Driver: "iTCO_wdt", TimeoutSeconds: 30}
		hostIDs = append(hostIDs, host.State.HostID)
	}
	labWatchdogs(t, watchdogs)
	labQuorum(t, true, hostIDs...)
	// Members joined one at a time, so each holds the membership as it was when it joined.
	// In a live pool the master's replica push catches them up; here it is copied, because
	// what is under test is what a host does with the record it holds.
	syncPoolMembershipForTest(t, lab)
	arming := newLabArming(t)
	for _, host := range lab.hosts {
		if err := ReportPoolWatchdog(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	// Nothing arms until the pool's own cluster has been quorate with every host in it for
	// a whole proof bound, and then only one host at a time: each pass arms whichever host
	// the membership says the turn belongs to, and the master's replica carries that to the
	// others. One round per host is exactly what a live pool does, more slowly.
	for range lab.hosts {
		labSettledCluster(t, lab)
		for _, host := range lab.hosts {
			arming.reconcile(t, host)
		}
		syncPoolMembershipForTest(t, lab)
	}
	labSettledCluster(t, lab)
	return lab, arming
}

// labReconfirmArming is what a live pool does after a host has been away: the host reports its
// arming again as a new spell, the master records it, and the replica carries it to every
// member. It is the one thing that makes a survivor's record evidence about the target as it is
// now rather than as it was before it left.
func labReconfirmArming(t *testing.T, lab *transferLab, hostID string) {
	t.Helper()
	master := lab.host("master-1")
	row := memberRow(t, master, hostID).WatchdogArmed
	if row == nil || !row.Armed {
		t.Fatalf("%s has no armed report to confirm", hostID)
	}
	report := *row
	report.ArmedAt = time.Now().UTC()
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		err = applyPoolWatchdogArmedReport(master, &state, hostID, report)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	syncPoolMembershipForTest(t, lab)
}

// labSettledCluster is a pool whose Corosync membership every host has been reading, unchanged
// and whole, for longer than a proof bound: the state a real pool reaches a minute after its
// cluster forms, and the gate in front of arming.
func labSettledCluster(t *testing.T, lab *transferLab) {
	t.Helper()
	for _, host := range lab.hosts {
		if err := ObservePoolQuorum(context.Background(), host); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ClearPoolQuorumForTest(host) })
	}
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	for _, host := range lab.hosts {
		poolQuorumRingSince[host.dir] = time.Now().Add(-2 * protocol.PoolWatchdogBound(0))
	}
}

// A host arms when the pool's own prerequisites hold, reports it durably at the pool's
// fencing generation, and a settled pool then writes nothing at all: the membership is signed
// and replicated, so a pass that found no change must not re-push the pool's authority.
func TestArmingFollowsTheSettingAndIsRecordedOnceAtTheCurrentGeneration(t *testing.T) {
	lab, arming := labArmedPool(t)
	master := lab.host("master-1")
	for _, hostID := range []string{"master-1", "member-1", "member-2"} {
		row := memberRow(t, master, hostID).WatchdogArmed
		if row == nil || !row.Armed || row.Kind != protocol.PoolWatchdogHardware || row.TimeoutSeconds == 0 {
			t.Fatalf("%s did not report an armed watchdog: %#v", hostID, row)
		}
		if row.FencingGeneration != peerState(t, master).Membership.Generation {
			t.Fatalf("%s armed at generation %d, not the pool's", hostID, row.FencingGeneration)
		}
	}
	before, digest := peerState(t, master).Membership.Generation, peerState(t, master).AuthorityDigest
	actions := len(arming.actions)
	for i := 0; i < 20; i++ {
		for _, host := range lab.hosts {
			arming.reconcile(t, host)
		}
	}
	if state := peerState(t, master); state.Membership.Generation != before || state.AuthorityDigest != digest {
		t.Fatal("twenty unchanged arming passes moved the pool's authority")
	}
	if len(arming.actions) != actions {
		t.Fatalf("a settled pool armed or disarmed again: %v", arming.actions[actions:])
	}

	// Turning the setting off disarms every host cleanly, and nothing resets.
	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	syncPoolMembershipForTest(t, lab) // What the master's replica push delivers in a live pool.
	for _, host := range lab.hosts {
		arming.reconcile(t, host)
	}
	for device, armed := range arming.armed {
		if armed {
			t.Fatalf("%s stayed armed after host fencing was turned off", device)
		}
	}
	if row := memberRow(t, master, "master-1").WatchdogArmed; row == nil || row.Armed ||
		!strings.Contains(row.Detail, "turned off") {
		t.Fatalf("the master did not report why it disarmed: %#v", row)
	}
}

// A host that cannot stop feeding its watchdog says so, and the pool's record says it is not
// armed either way. Those are two different questions: the record governs whether a survivor
// may prove an exclusion, and the safe answer once a disarm has been ordered is no, whether or
// not the device was actually released. The machine may still reset itself, which is also
// safe; what must never happen is a proof against a host nobody can account for.
func TestAHostThatCannotStopFeedingItsWatchdogSaysSoAndIsNotProvable(t *testing.T) {
	lab, arming := labArmedPool(t)
	master := lab.host("master-1")
	arming.nowayout = true
	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	arming.reconcile(t, master)
	row := memberRow(t, master, "master-1").WatchdogArmed
	if row == nil || row.Armed || !strings.Contains(row.Detail, "nowayout") {
		t.Fatalf("a host that could not stop feeding its watchdog did not say so: %#v", row)
	}
	if err := PoolWatchdogUsable(master.dir, memberRow(t, master, "master-1")); err == nil {
		t.Fatal("a host the pool has ordered disarmed was still provable")
	}
	if !arming.armed["watchdog-master-1"] {
		t.Fatal("Nex Core claimed a nowayout host stopped feeding its device")
	}
}

// The lethal ordering: a host must never stop feeding its watchdog while the pool's durable
// record still says it is armed, because a candidate holding that record would prove an
// exclusion against a host that is still running its VMs. The record goes first, and a host
// that cannot record it stays armed and says so.
// (Reported by independent review of slice 4b.)
func TestAHostIsNeverDisarmedBeforeThePoolRecordsIt(t *testing.T) {
	lab, arming := labArmedPool(t)
	master, member := lab.host("master-1"), lab.host("member-1")
	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	syncPoolMembershipForTest(t, lab)
	// The master is gone, so the member cannot record anything. It must keep feeding.
	lab.powerOff("master-1")
	if err := arming.tryReconcile(member); err == nil {
		t.Fatal("a member that could not record its withdrawal reported success")
	}
	if !arming.armed["watchdog-member-1"] {
		t.Fatal("a member disarmed while the pool's record still said it was armed")
	}
	if slices.Contains(arming.actions, "disarm watchdog-member-1") {
		t.Fatalf("a disarm was attempted before it could be recorded: %v", arming.actions)
	}
	// The master returns: now the record can be written, and only then does it disarm.
	lab.powerOn("master-1")
	arming.reconcile(t, member)
	if arming.armed["watchdog-member-1"] {
		t.Fatal("the member never disarmed once the pool could record it")
	}
	if row := memberRow(t, master, "member-1").WatchdogArmed; row == nil || row.Armed {
		t.Fatalf("the master does not record the member as disarmed: %#v", row)
	}
}

// Falling to two hosts disarms every remaining host: two hosts have no majority, so neither
// side of a partition may conclude that it is the one that should keep running.
func TestFallingToTwoHostsDisarms(t *testing.T) {
	lab, arming := labArmedPool(t)
	master := lab.host("master-1")
	setMemberStatus(t, master, "member-2", "ejected")
	arming.reconcile(t, master)
	if arming.armed["watchdog-master-1"] {
		t.Fatal("a two-host pool stayed armed")
	}
	if row := memberRow(t, master, "master-1").WatchdogArmed; row == nil || row.Armed ||
		!strings.Contains(row.Detail, "three or more") {
		t.Fatalf("the master did not say why a two-host pool disarms: %#v", row)
	}
}

// Nothing that is not a genuine quorum loss may ever cost a host its watchdog feed. A host
// with no watchdog at all, one whose device something else already holds, and one whose sbd
// could not be started are each reported and left alone.
func TestAHostIsNeverArmedAgainstItsOwnMachine(t *testing.T) {
	lab, arming := labArmedPool(t)
	master := lab.host("master-1")

	// A device another program already feeds is reported, not taken: two owners of one
	// timer is how a watchdog ends up fed by the wrong thing.
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{"master-1": {Kind: protocol.PoolWatchdogHardware,
		Device: "watchdog-master-1", Driver: "iTCO_wdt", TimeoutSeconds: 30, InUse: true}})
	arming.armed["watchdog-master-1"] = false
	arming.reconcile(t, master)
	if row := memberRow(t, master, "master-1").WatchdogArmed; row == nil || row.Armed ||
		!strings.Contains(row.Detail, "already holds") {
		t.Fatalf("a host with a taken device did not say so: %#v", row)
	}

	// A host whose sbd will not start says so and is never reported armed.
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{"master-1": {Kind: protocol.PoolWatchdogHardware,
		Device: "watchdog-master-1", Driver: "iTCO_wdt", TimeoutSeconds: 30}})
	arming.armErr = errPoolWatchdog("the sbd package is not installed on this host")
	arming.reconcile(t, master)
	row := memberRow(t, master, "master-1").WatchdogArmed
	if row == nil || row.Armed || !strings.Contains(row.Detail, "sbd package") {
		t.Fatalf("a host that could not arm was not reported honestly: %#v", row)
	}
	if row.Kind != "" || row.TimeoutSeconds != 0 || row.FencingGeneration != 0 {
		t.Fatalf("an unarmed report carried evidence a proof could rest on: %#v", row)
	}
}

// An armed host that is reported ready is one that really would reset itself: armed, at this
// pool's current record, and inside a quorum it could lose.
func TestAWatchdogIsReadyOnlyWhenArmedAtThisGenerationInsideAQuorum(t *testing.T) {
	lab, _ := labArmedPool(t)
	master := lab.host("master-1")
	if method := watchdogMethod(t, master, "member-1"); method.State != protocol.PoolFenceMethodReady {
		t.Fatalf("an armed host inside a quorum was not ready: %#v", method)
	}
	if summary := poolSummary(t, master); summary.Ready != summary.Total || summary.Total != 3 {
		t.Fatalf("armed hosts were not counted as protected: %#v", summary)
	}
	if got := poolPrerequisite(t, master, protocol.PoolFencePrerequisiteQuorum); got.State != protocol.PoolFencePrerequisiteMet {
		t.Fatalf("a quorate pool did not meet the quorum prerequisite: %#v", got)
	}
	// A watchdog must never satisfy the prerequisite for arming watchdogs: the fence-device
	// line is about fence devices. (Reported by independent review of slice 4a.)
	if got := poolPrerequisite(t, master, protocol.PoolFencePrerequisiteBMC); got.State == protocol.PoolFencePrerequisiteMet {
		t.Fatalf("an armed watchdog satisfied the fence-device prerequisite: %#v", got)
	}

	// A pool that loses quorum has no ready watchdog: nothing about it is ready while the
	// engine that would reset a host cannot say who is in the majority.
	labQuorum(t, false)
	for _, host := range lab.hosts {
		_ = ObservePoolQuorum(context.Background(), host)
	}
	if method := watchdogMethod(t, master, "member-1"); method.State == protocol.PoolFenceMethodReady {
		t.Fatalf("a host was ready through a watchdog with no quorum: %#v", method)
	}
}

// An armed report from an earlier record of the pool is not evidence about the pool as it is
// now: the set of hosts that make a majority has changed under it.
func TestAnArmedReportFromAnEarlierGenerationIsNotReadiness(t *testing.T) {
	lab, _ := labArmedPool(t)
	master := lab.host("master-1")
	setWatchdogArmedGeneration(t, master, "member-1", 1)
	method := watchdogMethod(t, master, "member-1")
	if method.State == protocol.PoolFenceMethodReady || !strings.Contains(method.Reason, "earlier record") {
		t.Fatalf("a stale-generation armed report was accepted: %#v", method)
	}
}

// setWatchdogArmedGeneration rewrites one host's armed report, which is how a pool that has
// changed since a host armed is reproduced without racing a real membership change.
func setWatchdogArmedGeneration(t *testing.T, master *Store, hostID string, generation uint64) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID == hostID && state.Membership.Members[i].WatchdogArmed != nil {
			armed := *state.Membership.Members[i].WatchdogArmed
			armed.FencingGeneration = generation
			state.Membership.Members[i].WatchdogArmed = &armed
		}
	}
	if err = savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
}

// syncPoolMembershipForTest gives every member the master's current membership record.
func syncPoolMembershipForTest(t *testing.T, lab *transferLab) {
	t.Helper()
	membership := peerState(t, lab.host("master-1")).Membership
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	for _, host := range lab.hosts[1:] {
		state, err := loadPoolPeer(host)
		if err != nil {
			t.Fatal(err)
		}
		state.Membership = membership
		if err = savePoolPeer(host, state); err != nil {
			t.Fatal(err)
		}
	}
}

// "Armed" has to mean "armed and still verified", not "was armed once". Every pass re-checks
// the things that make a watchdog actually fire — Pacemaker running, the cluster's policy
// letting an inquorate host die, sbd running its configuration — and if any of them stops
// holding, the host must withdraw its own armed report. Otherwise the survivors go on proving
// exclusions against a host that will never reset.
//
// The live case: three armed hosts, then a clustered-storage repository writes
// no-quorum-policy=freeze on the same cluster. Measured in this slice's lab, a partitioned host
// on a freeze cluster was still running after two minutes.
// (Reported by independent review of slice 4b.)
func TestAnArmedReportIsWithdrawnWhenReverificationFails(t *testing.T) {
	lab, arming := labArmedPool(t)
	master := lab.host("master-1")
	if row := memberRow(t, master, "master-1").WatchdogArmed; row == nil || !row.Armed {
		t.Fatalf("the pool did not start armed: %#v", row)
	}

	// Something else changes the cluster under Nex Core: the policy that lets an inquorate
	// host reset itself is gone. sbd is still running and the device is still held.
	arming.armErr = errPoolWatchdog("this pool's cluster keeps an inquorate host running instead of letting it reset itself")
	arming.reconcile(t, master)

	row := memberRow(t, master, "master-1").WatchdogArmed
	if row == nil || row.Armed {
		t.Fatalf("a host whose re-verification failed still reports armed: %#v", row)
	}
	if !strings.Contains(row.Detail, "inquorate") {
		t.Fatalf("the withdrawal does not say why: %#v", row)
	}
	// And the withdrawal carries no evidence a proof could rest on.
	if row.Kind != "" || row.TimeoutSeconds != 0 || row.FencingGeneration != 0 {
		t.Fatalf("a withdrawn report kept evidence: %#v", row)
	}
	// The proof is refused, which is the whole point.
	if err := PoolWatchdogUsable(master.dir, memberRow(t, master, "master-1")); err == nil {
		t.Fatal("a proof was still usable against a host that will not reset")
	}
	if method := watchdogMethod(t, master, "master-1"); method.State == protocol.PoolFenceMethodReady {
		t.Fatalf("readiness still reports a withdrawn host as protected: %#v", method)
	}
}

// An armed host reads its own sbd back through the kernel's watchdog state: a device that is
// open and being fed reads "active", which is exactly what the in-use gate sees. Treating that
// as another program's device made every host disarm itself the pass after it armed, and the
// host that holds the arming turn would hold it for ever, so the pool never finished arming.
// (Reported by independent review of slice 4c.)
func TestAnArmedHostDoesNotReadItsOwnSBDAsAnotherProgram(t *testing.T) {
	lab, arming := labArmedPool(t)
	master := lab.host("master-1")
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{"master-1": {Kind: protocol.PoolWatchdogHardware,
		Device: "watchdog-master-1", Driver: "iTCO_wdt", TimeoutSeconds: 30, InUse: true}})
	arming.reconcile(t, master)
	if !arming.armed["watchdog-master-1"] {
		t.Fatal("an armed host stopped feeding its watchdog because its own sbd holds the device")
	}
	if row := memberRow(t, master, "master-1").WatchdogArmed; row == nil || !row.Armed || row.Detail != "" {
		t.Fatalf("an armed host was recorded as not armed: %#v", row)
	}
	if slices.Contains(arming.actions, "disarm watchdog-master-1") {
		t.Fatalf("an armed host was asked to disarm: %v", arming.actions)
	}
}

// The other side of the same gate. A host in maintenance must put its watchdog down, and while
// it is armed its own sbd is what holds the device — so the in-use gate sits directly in front
// of the maintenance one, and reading that device as another program's would return before the
// maintenance branch is ever reached and leave the host feeding through the planned work.
func TestAnArmedHostInMaintenancePutsItsWatchdogDown(t *testing.T) {
	// Four hosts, so that draining one still leaves a pool that wants a cluster: a
	// three-host pool losing one disarms for having no majority, which is a different gate.
	lab, arming := labArmedPoolOf(t, 3)
	master := lab.host("master-1")
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{"master-1": {Kind: protocol.PoolWatchdogHardware,
		Device: "watchdog-master-1", Driver: "iTCO_wdt", TimeoutSeconds: 30, InUse: true}})
	generation := memberRecord(t, master, "master-1").Generation
	if result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "master-1", true, generation)); result.Status != "succeeded" {
		t.Fatalf("draining the master = %#v", result)
	}
	arming.reconcile(t, master)
	if arming.armed["watchdog-master-1"] {
		t.Fatal("a host in maintenance kept feeding its watchdog")
	}
	if row := memberRow(t, master, "master-1").WatchdogArmed; row == nil || row.Armed ||
		!strings.Contains(row.Detail, "maintenance") {
		t.Fatalf("the master did not say why maintenance disarmed it: %#v", row)
	}
}

// Arming is an exposure like a fence profile: from the moment a host reports it, its peers can
// fence-promote over it with no profile and no credential anywhere. It must therefore come back
// from the reset that promotion assumes under the same boot restriction a profile earns, or it
// reboots into a pool that has already moved its VMs and starts them a second time.
// (Reported by independent review of slice 4c.)
func TestAnArmedMasterIsRestrictedOnTheBootAfterItsWatchdogCouldHaveResetIt(t *testing.T) {
	const boot = "11111111-1111-4111-8111-111111111111"
	previous := poolBootAuthorityID
	poolBootAuthorityID = func() (string, error) { return boot, nil }
	t.Cleanup(func() { poolBootAuthorityID = previous })

	lab, arming := labArmedPool(t)
	master := lab.host("master-1")
	state := peerState(t, master)
	// SelfFence is the field the vmhost boot gate reads to decide whether this host's boot
	// is checked at all; without it a watchdog-only master starts its guests unchallenged.
	if state.SelfFence == nil {
		t.Fatal("arming recorded no self-fence exposure, so this host's next boot is never gated")
	}
	if state.BootAuthority == nil || state.BootAuthority.HostID != "master-1" || state.BootAuthority.BootID != boot {
		t.Fatalf("arming bound no boot authority to this boot: %#v", state.BootAuthority)
	}

	// The reset and the reboot, which is only a new boot identity: nothing else changed.
	poolBootAuthorityID = func() (string, error) { return "22222222-2222-4222-8222-222222222222", nil }
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	if err := requirePoolStartPath(master); err == nil {
		t.Fatal("a rebooted armed master started its guests without re-proving its authority")
	}

	// It re-proves its authority, and disarming then starts releasing the exposure through
	// the same path a withdrawn fence profile uses.
	clearPoolRestriction(master, poolRestrictedUnconfirmed)
	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	syncPoolMembershipForTest(t, lab)
	arming.reconcile(t, master)
	if got := peerState(t, master).SelfFence; got == nil || got.ClearedAt == 0 {
		t.Fatalf("disarming did not start releasing the exposure: %#v", got)
	}
}
