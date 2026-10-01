// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// fakeUpdateHost stands for one host's image engine and guests. It records what the coordinator
// does to it and fails the test the moment a pool-wide safety property would be broken.
type fakeUpdateHost struct {
	id          string
	sequence    uint64 // running release
	staged      bool
	phase       string
	guests      map[string]bool // vm id -> running
	starts      map[string]int  // vm id -> number of start requests that really started it
	activations map[string]int  // step id -> times the engine armed
	downFor     int             // status/step calls that still fail while the host reboots
	bootsTo     uint64          // release the host comes back on
	bootFails   bool            // never comes back
	checkCode   string
	stageCode   string
	state       int
	target      protocol.PoolUpdateTarget
	channel     string
	free        uint64
	capability  bool
	stageSeen   map[string]bool // engine request IDs already used to stage: a replay changes nothing
	stageIDs    []string        // request IDs of the stage operations that really ran, in order
	partitioned bool            // up and ready in the pool's records, but not voting in Corosync
}

type fakeUpdatePool struct {
	t      *testing.T
	master *Store
	hosts  map[string]*fakeUpdateHost
	mu     sync.Mutex
	// downNow is the set of hosts that are rebooting right now.
	maxDown        int
	maxMaintenance int
	crashAfterStep func(host, action string) // called after a step returns, to simulate a crash
	witness        bool                      // a qdevice witness votes in the pool's Corosync
}

func (p *fakeUpdatePool) host(id string) *fakeUpdateHost { return p.hosts[id] }

func (p *fakeUpdatePool) checkInvariants(where string) {
	p.t.Helper()
	membership, err := Membership(p.master)
	if err != nil {
		p.t.Fatal(err)
	}
	inMaintenance, down := 0, 0
	for _, member := range membership.Members {
		if member.Maintenance {
			inMaintenance++
		}
		if h := p.hosts[member.HostID]; h != nil && h.downFor > 0 {
			down++
		}
	}
	if inMaintenance > p.maxMaintenance {
		p.maxMaintenance = inMaintenance
	}
	if down > p.maxDown {
		p.maxDown = down
	}
	if inMaintenance > 1 {
		p.t.Fatalf("%s: %d hosts are in maintenance at once", where, inMaintenance)
	}
	// Quorum: votes do not shrink when a host stops, so a majority of the ready voters must stay.
	ready := 0
	for _, member := range membership.Members {
		if member.Status == "ready" {
			ready++
		}
	}
	if voters := ready; voters >= 3 && down > 0 && (voters-down)*2 <= voters {
		p.t.Fatalf("%s: quorum lost: %d of %d voters are down", where, down, voters)
	}
}

// quorum is what the master's Corosync would print: one vote for each host that is up and in the
// partition, the witness's vote when it votes, and the majority the nodelist needs. A pool of
// fewer than three hosts without a witness forms no cluster, so nothing can be observed.
func (p *fakeUpdatePool) quorum() vmhost.PoolQuorum {
	if len(p.hosts) < 3 && !p.witness {
		return vmhost.PoolQuorum{}
	}
	q := vmhost.PoolQuorum{Observed: true, ExpectedVotes: len(p.hosts)}
	for id, h := range p.hosts {
		if h.downFor == 0 && !h.partitioned {
			q.Hosts = append(q.Hosts, id)
		}
	}
	sort.Strings(q.Hosts)
	q.TotalVotes = len(q.Hosts)
	if p.witness {
		q.ExpectedVotes++
		q.TotalVotes++
	}
	q.Quorum = q.ExpectedVotes/2 + 1
	q.Quorate = q.TotalVotes >= q.Quorum
	return q
}

func (p *fakeUpdatePool) Status(_ context.Context, member protocol.PoolMember) (protocol.PoolUpdateHostStatus, error) {
	h := p.hosts[member.HostID]
	if h.downFor > 0 {
		return protocol.PoolUpdateHostStatus{}, errors.New("unreachable")
	}
	st := protocol.PoolUpdateHostStatus{HostID: h.id, Channel: h.channel, Phase: h.phase, RunningSequence: h.sequence,
		StateFormat: h.state, FreeBytes: h.free, ObservedAt: time.Now()}
	if h.capability {
		st.Capabilities = []string{protocol.PoolUpdateRolloutCapability}
	}
	if h.staged {
		st.StagedSequence = h.target.Sequence
	}
	target := h.target
	st.Target = &target
	for id, running := range h.guests {
		if running {
			st.RunningVMs = append(st.RunningVMs, id)
		}
	}
	return st, nil
}

func (p *fakeUpdatePool) Step(_ context.Context, member protocol.PoolMember, step protocol.PoolUpdateStep) (protocol.PoolUpdateStepResult, error) {
	h := p.hosts[member.HostID]
	if err := step.Valid(); err != nil {
		p.t.Fatalf("coordinator sent an invalid step: %v", err)
	}
	// Every step ID is an engine request ID for stage, activate and rollback; the engine refuses
	// a malformed one, so the fake fails the test the moment the coordinator mints one.
	if !nexupdate.ValidRequestID(step.StepID) {
		p.t.Fatalf("coordinator sent step id %q, which the engine would refuse", step.StepID)
	}
	if h.downFor > 0 {
		return protocol.PoolUpdateStepResult{}, errors.New("unreachable")
	}
	p.checkInvariants(member.HostID + " " + step.Action)
	res := protocol.PoolUpdateStepResult{Done: true}
	switch step.Action {
	case "check":
		if h.checkCode != "" {
			res = protocol.PoolUpdateStepResult{Code: h.checkCode, Message: "refused by the host"}
		}
	case "stage":
		switch {
		case h.stageCode != "":
			res = protocol.PoolUpdateStepResult{Code: h.stageCode, Message: "refused by the host"}
		case !h.stageSeen[step.StepID]:
			h.stageSeen[step.StepID] = true
			h.stageIDs = append(h.stageIDs, step.StepID)
			h.staged = true
		}
	case "drain":
		for _, id := range step.VMIDs {
			h.guests[id] = false
		}
	case "undrain":
		for _, id := range step.VMIDs {
			if !h.guests[id] {
				h.guests[id] = true
				h.starts[id]++
			}
		}
	case "activate":
		// The engine's last guard: never reboot a host that still runs guests, and the host
		// must already be the one host in maintenance.
		for id, running := range h.guests {
			if running {
				p.t.Fatalf("activate on %s while guest %s still runs", h.id, id)
			}
		}
		if m, _ := memberOf(p.master, h.id); !m.Maintenance {
			p.t.Fatalf("activate on %s outside maintenance mode", h.id)
		}
		if h.activations[step.StepID] == 0 && h.sequence != step.Sequence {
			if !h.staged {
				res = protocol.PoolUpdateStepResult{Code: "not_staged", Message: "nothing is staged on this host"}
				break
			}
			// The reboot takes this host's vote away: the votes left must still be a majority.
			if q := p.quorum(); q.Observed && q.TotalVotes-1 < q.Quorum {
				p.t.Fatalf("activate on %s leaves %d live votes of the %d needed", h.id, q.TotalVotes-1, q.Quorum)
			}
			h.downFor = 2
			h.phase = "armed"
			h.sequence = h.bootsTo
			h.staged = false
		}
		h.activations[step.StepID]++
		if h.bootFails {
			h.downFor = 1 << 30
		}
	case "confirm":
		if h.sequence != step.Sequence {
			res = protocol.PoolUpdateStepResult{}
		}
	case "rollback":
		h.sequence, h.phase, h.downFor = h.sequence-1, "rolled_back", 1
	}
	if p.crashAfterStep != nil {
		p.crashAfterStep(h.id, step.Action)
	}
	return res, nil
}

func memberOf(s *Store, id string) (protocol.PoolMember, bool) {
	membership, err := Membership(s)
	if err != nil {
		return protocol.PoolMember{}, false
	}
	return poolMemberByID(membership, id)
}

// tick is one second of wall time: hosts that are rebooting get closer to coming back.
func (p *fakeUpdatePool) tick() {
	for _, h := range p.hosts {
		if h.downFor > 0 && h.downFor < 1<<20 {
			h.downFor--
			if h.downFor == 0 && h.phase == "armed" {
				h.phase = "committed"
			}
			if h.downFor == 0 && h.phase == "rolled_back" {
				h.phase = "rolled_back"
			}
		}
	}
}

type updateLab struct {
	*testing.T
	pool  *fakeUpdatePool
	clock time.Time
	// restartCoordinator, when set, builds a fresh coordinator for every pass: the whole
	// in-memory state is thrown away between steps.
	restartCoordinator bool
	c                  *poolUpdateCoordinator
}

// newUpdateLab builds a pool master with n hosts (master-1 plus members) with addresses so the
// pool forms a cluster of n voters, and a fake image engine on each.
func newUpdateLab(t *testing.T, n int) *updateLab {
	t.Helper()
	master := peerStore(t, "master-1", "pool-1")
	var members []protocol.PoolMember
	pool := &fakeUpdatePool{t: t, master: master, hosts: map[string]*fakeUpdateHost{}}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("member-%d", i)
		if i == 1 {
			id = "master-1"
		}
		member := maintenanceMember(id, false)
		member.PeerOrigin = fmt.Sprintf("https://192.0.2.%d:8443", i)
		members = append(members, member)
		pool.hosts[id] = &fakeUpdateHost{id: id, sequence: 1, phase: "committed", guests: map[string]bool{}, starts: map[string]int{},
			activations: map[string]int{}, stageSeen: map[string]bool{}, bootsTo: 2, state: 2, channel: "stable", free: 1 << 40, capability: true,
			target: protocol.PoolUpdateTarget{Sequence: 2, StateFormat: 2, MixedPool: true, RequiredBytes: 1 << 20}}
	}
	maintenanceMembership(t, master, "master-1", members...)
	lab := &updateLab{T: t, pool: pool, clock: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)}
	prevPeers, prevNow := poolUpdatePeerFactory, poolUpdateNow
	prevPush, prevMaintPush, prevQuorum := pushPoolAuthorityReplicasAfterUpdate, pushPoolAuthorityReplicasAfterMaintenance, localQuorum
	prevTimeout, prevBoot, prevSoak := poolUpdateStepTimeout, poolUpdateBootDeadline, poolUpdateMinSoak
	poolUpdatePeerFactory = func(*Store) poolUpdatePeers { return pool }
	poolUpdateNow = func() time.Time { return lab.clock }
	localQuorum = func(context.Context) (vmhost.PoolQuorum, error) { return pool.quorum(), nil }
	noPush := func(context.Context, *Store, time.Duration) error { return nil }
	pushPoolAuthorityReplicasAfterUpdate, pushPoolAuthorityReplicasAfterMaintenance = noPush, noPush
	poolUpdateStepTimeout, poolUpdateBootDeadline, poolUpdateMinSoak = 10*time.Minute, 5*time.Minute, 3*time.Second
	t.Cleanup(func() {
		poolUpdatePeerFactory, poolUpdateNow = prevPeers, prevNow
		pushPoolAuthorityReplicasAfterUpdate, pushPoolAuthorityReplicasAfterMaintenance, localQuorum = prevPush, prevMaintPush, prevQuorum
		poolUpdateStepTimeout, poolUpdateBootDeadline, poolUpdateMinSoak = prevTimeout, prevBoot, prevSoak
		poolUpdateHAEnabled = func(*Store) bool { return false }
	})
	lab.c = NewPoolUpdateCoordinator(master)
	return lab
}

func (l *updateLab) coordinator() *poolUpdateCoordinator {
	if l.restartCoordinator {
		l.c = NewPoolUpdateCoordinator(l.pool.master)
	}
	return l.c
}

func (l *updateLab) start(id, vms string) error {
	_, _, err := l.coordinator().Start(context.Background(), id, protocol.PoolUpdateRequest{Sequence: 2, RunningVMs: vms})
	return err
}

func (l *updateLab) rollout() *protocol.PoolUpdateRollout {
	l.Helper()
	r, err := LoadPoolUpdate(l.pool.master)
	if err != nil || r == nil {
		l.Fatalf("no rollout record: %v", err)
	}
	return r
}

// run advances the rollout one second at a time until stop(r) holds.
func (l *updateLab) run(limit int, stop func(*protocol.PoolUpdateRollout) bool) *protocol.PoolUpdateRollout {
	l.Helper()
	for range limit {
		if err := l.coordinator().Advance(context.Background()); err != nil {
			l.Fatalf("advance: %v", err)
		}
		l.pool.checkInvariants("after pass")
		if r := l.rollout(); stop(r) {
			return r
		}
		l.clock = l.clock.Add(time.Second)
		l.pool.tick()
	}
	l.Fatalf("rollout did not reach the expected state in %d passes: %+v", limit, l.rollout())
	return nil
}

func ended(r *protocol.PoolUpdateRollout) bool  { return r.State != protocol.PoolUpdateRunning }
func paused(r *protocol.PoolUpdateRollout) bool { return r.State == protocol.PoolUpdatePaused }

func refusalCode(err error) string {
	var refusal *PoolUpdateError
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

// The whole rollout: members first by running-guest count then host ID, the master last, one
// host in maintenance at a time, quorum kept, guests restarted once each on their own host.
func TestRolloutUpdatesOneHostAtATimeMasterLast(t *testing.T) {
	lab := newUpdateLab(t, 3)
	lab.pool.host("member-2").guests["vm-a"] = true
	lab.pool.host("member-3").guests["vm-b"], lab.pool.host("member-3").guests["vm-c"] = true, true
	lab.pool.host("master-1").guests["vm-m"] = true
	if err := lab.start("rollout-1", "shutdown"); err != nil {
		t.Fatal(err)
	}
	r := lab.rollout()
	var order []string
	for _, h := range r.Hosts {
		order = append(order, h.HostID)
	}
	if got := strings.Join(order, ","); got != "member-2,member-3,master-1" {
		t.Fatalf("order = %s, want members by running guests then ID, master last", got)
	}
	r = lab.run(400, ended)
	if r.State != protocol.PoolUpdateFinalized || r.FinalizedSequence != 2 {
		t.Fatalf("final = %+v", r)
	}
	for id, h := range lab.pool.hosts {
		if h.sequence != 2 {
			t.Fatalf("%s runs release %d", id, h.sequence)
		}
		for vm, n := range h.starts {
			if n != 1 || !h.guests[vm] {
				t.Fatalf("guest %s on %s started %d times, running=%v", vm, id, n, h.guests[vm])
			}
		}
		if member, _ := memberOf(lab.pool.master, id); member.Maintenance {
			t.Fatalf("%s left in maintenance mode", id)
		}
	}
	if lab.pool.maxMaintenance != 1 || lab.pool.maxDown != 1 {
		t.Fatalf("observed %d hosts in maintenance and %d down at once, want exactly one", lab.pool.maxMaintenance, lab.pool.maxDown)
	}
	if lab.pool.host("member-2").starts["vm-b"] != 0 || lab.pool.host("member-3").starts["vm-b"] != 1 {
		t.Fatal("a guest was restarted somewhere other than the host that ran it")
	}
}

// Throw the coordinator's memory away between every pass: the record alone resumes the rollout,
// no guest starts twice, and the same safety properties hold.
func TestRolloutResumesAfterEveryCoordinatorRestart(t *testing.T) {
	lab := newUpdateLab(t, 3)
	lab.restartCoordinator = true
	lab.pool.host("member-2").guests["vm-a"] = true
	if err := lab.start("rollout-restart", "shutdown"); err != nil {
		t.Fatal(err)
	}
	if r := lab.run(400, ended); r.State != protocol.PoolUpdateFinalized {
		t.Fatalf("final = %+v", r)
	}
	if lab.pool.host("member-2").starts["vm-a"] != 1 {
		t.Fatalf("restart duplicated a guest start: %+v", lab.pool.host("member-2").starts)
	}
}

// A coordinator that dies after a step was delivered but before it saved repeats that step with
// the same step ID, and the host engine acts once.
func TestRolloutStepsAreIdempotentAcrossACrashAfterActivate(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("rollout-crash", "refuse"); err != nil {
		t.Fatal(err)
	}
	crashes := 0
	lab.pool.crashAfterStep = func(host, action string) {
		if action == "activate" && crashes < 2 {
			crashes++
			// The reply is lost: the delivered step is repeated by a fresh coordinator.
			lab.c = NewPoolUpdateCoordinator(lab.pool.master)
		}
	}
	lab.run(400, ended)
	for id, h := range lab.pool.hosts {
		for step, n := range h.activations {
			if n > 1 && h.sequence != 2 {
				t.Fatalf("%s armed step %s %d times", id, step, n)
			}
		}
	}
	if r := lab.rollout(); r.State != protocol.PoolUpdateFinalized {
		t.Fatalf("final = %+v", r)
	}
}

func TestPreflightRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*updateLab)
		vms   string
		code  string
	}{
		{"evacuate", func(*updateLab) {}, "evacuate", "evacuation_unavailable"},
		{"member without the capability", func(l *updateLab) { l.pool.host("member-2").capability = false }, "refuse", "upgrade_required"},
		{"different channel", func(l *updateLab) { l.pool.host("member-3").channel = "dev" }, "refuse", "channel_mismatch"},
		{"bridge release required", func(l *updateLab) { l.pool.host("member-2").checkCode = "bridge_required" }, "refuse", "bridge_required"},
		{"state format would go down", func(l *updateLab) { l.pool.host("member-2").target.StateFormat = 1 }, "refuse", "state_format_downgrade"},
		{"not enough free space", func(l *updateLab) { l.pool.host("member-3").free = 10 }, "refuse", "insufficient_space"},
		{"release not offered", func(l *updateLab) { l.pool.host("member-2").target.Sequence = 9 }, "refuse", "release_unavailable"},
		{"mixed pool not allowed without shutdown", func(l *updateLab) {
			for _, h := range l.pool.hosts {
				h.target.MixedPool = false
			}
		}, "refuse", "mixed_pool_requires_shutdown"},
		{"controller too old to know", func(l *updateLab) {
			for _, h := range l.pool.hosts {
				h.target.MinControllerAPIEpoch = 3
			}
		}, "refuse", "controller_unknown"},
		{"HA enabled", func(*updateLab) { poolUpdateHAEnabled = func(*Store) bool { return true } }, "refuse", protocol.PoolUpdateHAEnabled},
		{"a host is already in maintenance", func(l *updateLab) {
			if err := poolUpdateSetMaintenance(l.pool.master, "member-2", true); err != nil {
				l.Fatal(err)
			}
		}, "refuse", protocol.PoolUpdateHostBusy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lab := newUpdateLab(t, 3)
			tc.setup(lab)
			err := lab.start("rollout-refused", tc.vms)
			if refusalCode(err) != tc.code {
				t.Fatalf("code = %q (%v), want %q", refusalCode(err), err, tc.code)
			}
			if r, _ := LoadPoolUpdate(lab.pool.master); r != nil {
				t.Fatalf("a refused preflight left a record: %+v", r)
			}
		})
	}
}

func TestDryRunChecksEverythingAndRecordsNothing(t *testing.T) {
	lab := newUpdateLab(t, 3)
	_, plan, err := lab.c.Start(context.Background(), "dry", protocol.PoolUpdateRequest{Sequence: 2, DryRun: true})
	if err != nil || plan == nil || strings.Join(plan.Order, ",") != "member-2,member-3,master-1" {
		t.Fatalf("plan = %+v err = %v", plan, err)
	}
	if r, _ := LoadPoolUpdate(lab.pool.master); r != nil {
		t.Fatalf("dry run recorded %+v", r)
	}
	lab.pool.host("member-3").free = 1
	if _, _, err = lab.c.Start(context.Background(), "dry", protocol.PoolUpdateRequest{Sequence: 2, DryRun: true}); refusalCode(err) != "insufficient_space" {
		t.Fatalf("dry run did not refuse: %v", err)
	}
}

// A second rollout is refused while one is active; replaying the same request is the same rollout.
func TestStartIsIdempotentAndExclusive(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("one", "refuse"); err != nil {
		t.Fatal(err)
	}
	first := lab.rollout()
	if err := lab.start("one", "refuse"); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	if again := lab.rollout(); again.CreatedAt != first.CreatedAt || again.ID != "one" {
		t.Fatalf("replay changed the record: %+v", again)
	}
	if refusalCode(lab.start("two", "refuse")) != "rollout_in_progress" {
		t.Fatal("a second rollout was accepted while one is active")
	}
	if _, _, err := lab.c.Start(context.Background(), "one", protocol.PoolUpdateRequest{Sequence: 3}); refusalCode(err) != "request_conflict" {
		t.Fatalf("a changed request under the same ID: %v", err)
	}
}

// running_vms refuse waits for the guest, then pauses naming it; that host is not rebooted and no
// later host is touched.
func TestRefusePolicyWaitsThenPausesNamingTheGuest(t *testing.T) {
	lab := newUpdateLab(t, 3)
	lab.pool.host("member-3").guests["vm-busy"] = true
	if err := lab.start("refuse", "refuse"); err != nil {
		t.Fatal(err)
	}
	r := lab.run(2000, paused)
	if r.Pause.Code != protocol.PoolUpdateRunningGuests || !strings.Contains(r.Pause.Message, "vm-busy") {
		t.Fatalf("pause = %+v", r.Pause)
	}
	if h := lab.pool.host("member-3"); h.sequence != 1 || len(h.activations) != 0 || !h.guests["vm-busy"] {
		t.Fatalf("the blocked host was touched: %+v", h)
	}
	if h := lab.pool.host("master-1"); h.sequence != 1 {
		t.Fatalf("the master was updated before the blocked member: %+v", h)
	}
	if lab.pool.host("member-2").sequence != 2 {
		t.Fatal("the member before the blocked one was not updated")
	}
	// The operator stops the guest and resumes: the rollout completes.
	lab.pool.host("member-3").guests["vm-busy"] = false
	if _, err := lab.c.Control(context.Background(), protocol.PoolUpdateRequest{RolloutID: "refuse", Control: "resume"}); err != nil {
		t.Fatal(err)
	}
	if r = lab.run(400, ended); r.State != protocol.PoolUpdateFinalized {
		t.Fatalf("final = %+v", r)
	}
}

// A host that never returns pauses needs_attention at boot_deadline; nothing restarts elsewhere
// and no guest is assumed stopped.
func TestHostThatDoesNotReturnPausesNeedsAttention(t *testing.T) {
	lab := newUpdateLab(t, 3)
	lab.pool.host("member-3").bootFails = true
	lab.pool.host("member-3").guests["vm-a"] = true
	if err := lab.start("lost", "shutdown"); err != nil {
		t.Fatal(err)
	}
	r := lab.run(2000, paused)
	if r.Pause.Code != protocol.PoolUpdateNeedsAttention || r.Hosts[1].HostID != "member-3" || r.Hosts[1].Stage != stageBoot {
		t.Fatalf("pause = %+v hosts = %+v", r.Pause, r.Hosts)
	}
	if lab.pool.host("member-3").starts["vm-a"] != 0 || lab.pool.host("master-1").sequence != 1 {
		t.Fatal("something was restarted or updated while a host was missing")
	}
	if member, _ := memberOf(lab.pool.master, "member-3"); !member.Maintenance {
		t.Fatal("the missing host left maintenance mode on its own")
	}
	if got := r.Hosts[1].StoppedByUpdate; len(got) != 1 || got[0] != "vm-a" {
		t.Fatalf("the stopped guest was not remembered: %v", got)
	}
}

// A host that comes back on its old release (boot fell back) pauses; finalize never runs.
func TestRolledBackHostPausesAndBlocksFinalize(t *testing.T) {
	lab := newUpdateLab(t, 3)
	h := lab.pool.host("member-3")
	h.bootsTo = 1
	if err := lab.start("rb", "refuse"); err != nil {
		t.Fatal(err)
	}
	h.phase = "rolled_back"
	lab.pool.crashAfterStep = func(host, action string) {
		if host == "member-3" && action == "activate" {
			h.phase = "rolled_back"
		}
	}
	r := lab.run(600, paused)
	if r.Pause.Code != protocol.PoolUpdateNeedsAttention && r.Pause.Code != protocol.PoolUpdateRolledBack {
		t.Fatalf("pause = %+v", r.Pause)
	}
	if r.FinalizedSequence != 0 || r.State == protocol.PoolUpdateFinalized {
		t.Fatalf("finalized with a rolled back host: %+v", r)
	}
}

// The gate reads the master's live Corosync, not the membership rows: votes present, the majority
// they need, a witness that is really voting, and every other host really in the partition. A
// pool that forms no cluster has no quorum to lose; one that should have a cluster but whose
// quorum cannot be read is refused.
func TestQuorumGateReadsLiveVotes(t *testing.T) {
	gate := func(lab *updateLab, host string) string {
		membership, err := Membership(lab.pool.master)
		if err != nil {
			t.Fatal(err)
		}
		return refusalCode(poolUpdateQuorumGate(context.Background(), lab.pool.master, membership, host))
	}
	if got := gate(newUpdateLab(t, 3), "member-2"); got != "" {
		t.Fatalf("three hosts voting: %q", got)
	}
	if got := gate(newUpdateLab(t, 1), "master-1"); got != "" {
		t.Fatalf("single host forms no cluster: %q", got)
	}
	if got := gate(newUpdateLab(t, 2), "member-2"); got != "" {
		t.Fatalf("two hosts without a witness form no cluster: %q", got)
	}
	// Ready in the pool's records, not voting in Corosync: the old row count said yes.
	partitioned := newUpdateLab(t, 3)
	partitioned.pool.host("member-3").partitioned = true
	if got := gate(partitioned, "member-2"); got != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("a ready host that is not voting still counted: %q", got)
	}
	down := newUpdateLab(t, 3)
	down.pool.host("member-3").downFor = 5
	if got := gate(down, "member-2"); got != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("a host that is down still counted: %q", got)
	}
	witnessed := newUpdateLab(t, 2)
	ca, pin := witnessTestCA(t, "qnetd")
	poolPeerMu.Lock()
	state, err := loadPoolPeer(witnessed.pool.master)
	if err == nil {
		state.Membership.Witness = &protocol.PoolWitnessRecord{Kind: "controller", Address: "192.0.2.20:5403", CAPEM: ca, CASHA256: pin,
			FencingGeneration: 1, ClusterName: protocol.PoolWitnessClusterName("pool-1")}
		err = savePoolPeer(witnessed.pool.master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// The witness is registered but its vote is not present: Corosync cannot be read at all
	// here (two hosts alone form no cluster), and the pool says so instead of assuming it votes.
	if got := gate(witnessed, "member-2"); got != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("a registered witness that is not voting was assumed to vote: %q", got)
	}
	witnessed.pool.witness = true
	if got := gate(witnessed, "member-2"); got != "" {
		t.Fatalf("two hosts and a voting witness: %q", got)
	}
	witnessed.pool.host("master-1").partitioned = true
	if got := gate(witnessed, "member-2"); got != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("one host and the witness are not a majority of three once the other stops: %q", got)
	}
	// Clustered storage with only two voters is refused: the witness is not a storage voter pair.
	clustered := newUpdateLab(t, 2)
	if err = saveStorageClusters(clustered.pool.master, storageClusterState{Operations: map[string]storageClusterOperation{"op": {}}, FenceRequests: map[string]storageFenceRequest{}}); err != nil {
		t.Fatal(err)
	}
	if got := gate(clustered, "member-2"); got != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("two voters with clustered storage: %q", got)
	}
}

// The votes arithmetic itself, on Corosync readings written out by hand.
func TestQuorumGateVoteArithmetic(t *testing.T) {
	lab := newUpdateLab(t, 3)
	membership, err := Membership(lab.pool.master)
	if err != nil {
		t.Fatal(err)
	}
	all := []string{"master-1", "member-2", "member-3"}
	cases := []struct {
		name string
		q    vmhost.PoolQuorum
		code string
	}{
		{"all three voting", vmhost.PoolQuorum{Observed: true, Quorate: true, Hosts: all, ExpectedVotes: 3, TotalVotes: 3, Quorum: 2}, ""},
		{"not quorate", vmhost.PoolQuorum{Observed: true, Quorate: false, Hosts: all[:1], ExpectedVotes: 3, TotalVotes: 1, Quorum: 2}, protocol.PoolUpdateQuorumMargin},
		{"votes not reported", vmhost.PoolQuorum{Observed: true, Quorate: true, Hosts: all}, protocol.PoolUpdateQuorumMargin},
		{"a nodelist host absent", vmhost.PoolQuorum{Observed: true, Quorate: true, Hosts: []string{"master-1", "member-2"}, ExpectedVotes: 3, TotalVotes: 2, Quorum: 2}, protocol.PoolUpdateQuorumMargin},
		{"five expected, three present", vmhost.PoolQuorum{Observed: true, Quorate: true, Hosts: all, ExpectedVotes: 5, TotalVotes: 3, Quorum: 3}, protocol.PoolUpdateQuorumMargin},
		{"five expected, four present", vmhost.PoolQuorum{Observed: true, Quorate: true, Hosts: all, ExpectedVotes: 5, TotalVotes: 4, Quorum: 3}, ""},
	}
	for _, tc := range cases {
		localQuorum = func(context.Context) (vmhost.PoolQuorum, error) { return tc.q, nil }
		if got := refusalCode(poolUpdateQuorumGate(context.Background(), lab.pool.master, membership, "member-2")); got != tc.code {
			t.Fatalf("%s: code %q, want %q", tc.name, got, tc.code)
		}
	}
	localQuorum = func(context.Context) (vmhost.PoolQuorum, error) { return vmhost.PoolQuorum{}, errors.New("corosync is not running") }
	if got := refusalCode(poolUpdateQuorumGate(context.Background(), lab.pool.master, membership, "member-2")); got != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("an unreadable quorum on a clustered pool: %q", got)
	}
}

// Another host leaving ready during the rollout stops it before the next host enters maintenance.
func TestGateRefusesWhenAnotherHostIsNotReady(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("g", "refuse"); err != nil {
		t.Fatal(err)
	}
	poolPeerMu.Lock()
	state, _ := loadPoolPeer(lab.pool.master)
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID == "member-3" {
			state.Membership.Members[i].Status = "failed"
		}
	}
	err := savePoolPeer(lab.pool.master, state)
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	r := lab.run(50, paused)
	if r.Pause.Code != protocol.PoolUpdateQuorumMargin || lab.pool.host("member-2").sequence != 1 {
		t.Fatalf("pause = %+v", r.Pause)
	}
}

func TestControlsPauseResumeAbortRollback(t *testing.T) {
	lab := newUpdateLab(t, 3)
	ctx := context.Background()
	if err := lab.start("c", "refuse"); err != nil {
		t.Fatal(err)
	}
	ctl := func(control, host string) (*protocol.PoolUpdateRollout, error) {
		return lab.c.Control(ctx, protocol.PoolUpdateRequest{RolloutID: "c", Control: control, HostID: host})
	}
	// pause takes effect between hosts, not in the middle of one.
	if r, err := ctl("pause", ""); err != nil || r.State != protocol.PoolUpdatePaused || r.Pause.Code != protocol.PoolUpdatePausedByOperator {
		t.Fatalf("pause before any host started = %+v %v", r, err)
	}
	if err := lab.c.Advance(ctx); err != nil || lab.rollout().Hosts[0].Stage != stagePending {
		t.Fatalf("a paused rollout advanced: %+v", lab.rollout())
	}
	if _, err := ctl("resume", ""); err != nil {
		t.Fatal(err)
	}
	lab.run(30, func(r *protocol.PoolUpdateRollout) bool {
		return r.Hosts[0].Stage != stagePending && r.Hosts[0].Stage != stageStaged
	})
	if r, err := ctl("pause", ""); err != nil || r.State != protocol.PoolUpdateRunning || !r.PauseRequested {
		t.Fatalf("pause mid-host did not wait for the host: %+v %v", r, err)
	}
	r := lab.run(400, paused)
	if r.Hosts[0].Stage != stageDone || r.Hosts[1].Stage != stageStaged {
		t.Fatalf("pause did not land after the current host: %+v", r.Hosts)
	}
	if _, err := ctl("rollback_host", "member-2"); err != nil {
		t.Fatalf("rollback_host: %v", err)
	}
	if r = lab.rollout(); r.Pause.Code != protocol.PoolUpdateRolledBack || r.Hosts[0].Stage != stageRolledBack {
		t.Fatalf("after rollback_host = %+v", r)
	}
	if _, err := ctl("rollback_host", "member-3"); refusalCode(err) != "host_not_activated" {
		t.Fatalf("rollback of an untouched host: %v", err)
	}
	if r, err := ctl("abort", ""); err != nil || r.State != protocol.PoolUpdateAborted {
		t.Fatalf("abort = %+v %v", r, err)
	}
	if member, _ := memberOf(lab.pool.master, "member-2"); member.Maintenance {
		t.Fatal("abort left the rolled back host in maintenance mode")
	}
	if _, err := ctl("resume", ""); refusalCode(err) != "rollout_finished" {
		t.Fatalf("resume after abort: %v", err)
	}
	if _, err := lab.c.Control(ctx, protocol.PoolUpdateRequest{RolloutID: "nope", Control: "pause"}); refusalCode(err) != "rollout_not_found" {
		t.Fatalf("unknown rollout: %v", err)
	}
	// After an abort the pool can roll out again.
	lab.pool.tick() // the rolled back host finished rebooting
	if err := lab.start("again", "refuse"); err != nil {
		t.Fatalf("a new rollout after abort: %v", err)
	}
}

// A promoted master finds the rollout paused master_changed, whatever it was doing; the old
// master, no longer the master, changes nothing.
func TestPromotedMasterFindsRolloutPausedMasterChanged(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("m", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(30, func(r *protocol.PoolUpdateRollout) bool {
		return r.Hosts[0].Stage == stageDrain || r.Hosts[0].Stage == stageActivate
	})
	inherited := lab.rollout()
	promoted := peerStore(t, "member-2", "pool-1")
	undo, err := AdoptPoolUpdateOnPromotion(promoted, inherited)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadPoolUpdate(promoted)
	if err != nil || got.State != protocol.PoolUpdatePaused || got.Pause.Code != protocol.PoolUpdateMasterChanged {
		t.Fatalf("promoted record = %+v %v", got, err)
	}
	if got.Hosts[0].Stage != inherited.Hosts[0].Stage {
		t.Fatal("adoption rewrote host progress")
	}
	if inherited.State != protocol.PoolUpdateRunning {
		t.Fatal("adoption mutated the record it was given")
	}
	// A promotion that does not complete takes the inherited record back out again.
	undo()
	if r, _ := LoadPoolUpdate(promoted); r != nil {
		t.Fatalf("undo left the adopted record behind: %+v", r)
	}
	// A finished rollout is not resurrected as a paused one.
	done := *inherited
	done.State = protocol.PoolUpdateFinalized
	other := peerStore(t, "member-3", "pool-1")
	if _, err = AdoptPoolUpdateOnPromotion(other, &done); err != nil {
		t.Fatal(err)
	}
	if r, _ := LoadPoolUpdate(other); r != nil {
		t.Fatalf("a finalized rollout was adopted: %+v", r)
	}
	// The old master no longer holds authority: its pass changes nothing.
	poolPeerMu.Lock()
	state, _ := loadPoolPeer(lab.pool.master)
	state.Membership.MasterHostID = "member-2"
	err = savePoolPeer(lab.pool.master, state)
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	before := lab.rollout()
	if err = lab.c.Advance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := lab.rollout(); after.UpdatedAt != before.UpdatedAt || after.State != protocol.PoolUpdateRunning {
		t.Fatalf("a former master advanced the rollout: %+v", after)
	}
}

// The rollout record rides in the signed replica as the update_rollout section, and only then.
func TestRolloutIsAReplicatedAuthoritySection(t *testing.T) {
	lab := newUpdateLab(t, 3)
	plain, _, err := buildPoolAuthorityReplica(lab.pool.master)
	if err != nil {
		t.Fatal(err)
	}
	if plain.UpdateRollout != nil || len(plain.Sections) != len(protocol.PoolReplicaSections) {
		t.Fatalf("a pool that never rolled out declares %d sections", len(plain.Sections))
	}
	if err = lab.start("section", "refuse"); err != nil {
		t.Fatal(err)
	}
	withRollout, digest, err := buildPoolAuthorityReplica(lab.pool.master)
	if err != nil {
		t.Fatal(err)
	}
	last := withRollout.Sections[len(withRollout.Sections)-1]
	if withRollout.UpdateRollout == nil || last.Name != protocol.PoolReplicaUpdateRolloutSection || withRollout.VerifySections() != nil {
		t.Fatalf("sections = %+v", withRollout.Sections)
	}
	changed := withRollout
	rollout := *withRollout.UpdateRollout
	rollout.State = protocol.PoolUpdatePaused
	changed.UpdateRollout = &rollout
	if changed.VerifySections() == nil {
		t.Fatal("a tampered rollout section still verified")
	}
	if again, _ := changed.ContentDigest(); again == digest {
		t.Fatal("the content digest does not cover the rollout record")
	}
}

// With no Controller configured anywhere the rollout still completes, and what a fresh Controller
// would read from inventory is the master's own record.
func TestRolloutNeedsNoController(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if lab.pool.master.State.Controller != "" {
		t.Fatal("the lab master knows a Controller")
	}
	if err := lab.start("nc", "refuse"); err != nil {
		t.Fatal(err)
	}
	r := lab.run(400, ended)
	if r.State != protocol.PoolUpdateFinalized {
		t.Fatalf("final = %+v", r)
	}
	if projected := PoolUpdateForPool(lab.pool.master); projected == nil || projected.ID != "nc" || projected.FinalizedSequence != 2 {
		t.Fatalf("inventory projection = %+v", projected)
	}
}

// Membership generation change (a join, an eject) pauses before the next step.
func TestMembershipChangePausesTheRollout(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("mg", "refuse"); err != nil {
		t.Fatal(err)
	}
	poolPeerMu.Lock()
	state, _ := loadPoolPeer(lab.pool.master)
	state.Membership.Generation++
	for i := range state.Membership.Members {
		state.Membership.Members[i].Generation = state.Membership.Generation
	}
	err := savePoolPeer(lab.pool.master, state)
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	r := lab.run(5, paused)
	if r.Pause.Code != protocol.PoolUpdateMembershipChanged {
		t.Fatalf("pause = %+v", r.Pause)
	}
}

// Step IDs are engine request IDs: whatever the rollout and host IDs look like they come out
// valid, bounded, repeatable, and different for every stage and attempt. (Every rollout test also
// runs against a fake host that refuses an ID the engine would refuse.)
func TestStepIDsAreValidBoundedAndDifferPerAttempt(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range [][3]string{
		{"rollout-1", "master-1", "stage"},
		{strings.Repeat("r", 128), "host/with/slashes and spaces", "activate"},
		{"é", "ü", "x"},
	} {
		for attempt := range 3 {
			id := poolUpdateStepID(tc[0], tc[1], tc[2], attempt)
			if !nexupdate.ValidRequestID(id) || id != poolUpdateStepID(tc[0], tc[1], tc[2], attempt) {
				t.Fatalf("step id %q for %v attempt %d is not a valid, repeatable request id", id, tc, attempt)
			}
			if seen[id] {
				t.Fatalf("step id %q repeats", id)
			}
			seen[id] = true
		}
	}
}

// The gates read live votes where the decision is made: a host the pool still lists as ready but
// that has stopped voting blocks the next host from entering maintenance, and blocks an
// activation that was already waiting.
func TestGatesAreRunAgainBeforeMaintenanceAndBeforeActivate(t *testing.T) {
	t.Run("before maintenance", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		if err := lab.start("gate-m", "refuse"); err != nil {
			t.Fatal(err)
		}
		lab.pool.host("member-3").partitioned = true
		r := lab.run(20, paused)
		if r.Pause.Code != protocol.PoolUpdateQuorumMargin {
			t.Fatalf("pause = %+v", r.Pause)
		}
		if m, _ := memberOf(lab.pool.master, "member-2"); m.Maintenance {
			t.Fatal("member-2 entered maintenance without a voting majority behind it")
		}
	})
	t.Run("a record already at the maintenance stage", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		if err := lab.start("gate-r", "refuse"); err != nil {
			t.Fatal(err)
		}
		lab.run(20, func(r *protocol.PoolUpdateRollout) bool { return r.Phase == "hosts" })
		r := lab.rollout()
		r.Hosts[0].Stage = stageMaintenance
		if err := savePoolUpdate(lab.pool.master, r); err != nil {
			t.Fatal(err)
		}
		lab.pool.host("member-3").partitioned = true
		r = lab.run(5, paused)
		if m, _ := memberOf(lab.pool.master, "member-2"); r.Pause.Code != protocol.PoolUpdateQuorumMargin || m.Maintenance {
			t.Fatalf("maintenance flipped without the gate: pause %+v maintenance %v", r.Pause, m.Maintenance)
		}
	})
	t.Run("before activate", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		if err := lab.start("gate-a", "refuse"); err != nil {
			t.Fatal(err)
		}
		lab.run(60, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageActivate })
		lab.pool.host("member-3").partitioned = true
		r := lab.run(5, paused)
		if r.Pause.Code != protocol.PoolUpdateQuorumMargin || len(lab.pool.host("member-2").activations) != 0 {
			t.Fatalf("activate went ahead on a lost majority: pause %+v activations %v", r.Pause, lab.pool.host("member-2").activations)
		}
	})
}

// A resume is a fresh entry to the host that is next: the pool may have changed while it waited.
func TestResumeRunsTheGatesAgain(t *testing.T) {
	lab := newUpdateLab(t, 3)
	lab.pool.host("member-3").guests["vm-busy"] = true
	if err := lab.start("resume-gate", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(2000, paused)
	lab.pool.host("member-3").guests["vm-busy"] = false
	lab.pool.host("member-2").partitioned = true // updated earlier, ready in the records, not voting now
	resume := protocol.PoolUpdateRequest{RolloutID: "resume-gate", Control: "resume"}
	if _, err := lab.c.Control(context.Background(), resume); refusalCode(err) != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("resume on a lost majority: %v", err)
	}
	if r := lab.rollout(); r.State != protocol.PoolUpdatePaused {
		t.Fatalf("a refused resume changed the record: %+v", r)
	}
	lab.pool.host("member-2").partitioned = false
	if _, err := lab.c.Control(context.Background(), resume); err != nil {
		t.Fatal(err)
	}
	if r := lab.run(400, ended); r.State != protocol.PoolUpdateFinalized {
		t.Fatalf("final = %+v", r)
	}
}

// The guests a rollout stops are on every member that could take over before the first one is
// asked to shut down; while a member has not acknowledged, nothing is stopped.
func TestGuestsAreNotStoppedBeforeTheRecordIsAcknowledged(t *testing.T) {
	lab := newUpdateLab(t, 3)
	lab.pool.host("member-2").guests["vm-a"] = true
	var acked *protocol.PoolUpdateRollout
	refuse := true
	pushPoolAuthorityReplicasAfterUpdate = func(context.Context, *Store, time.Duration) error {
		r, err := LoadPoolUpdate(lab.pool.master)
		if err != nil || r == nil {
			t.Errorf("replicating a rollout that is not on disk: %v", err)
			return err
		}
		if refuse {
			return errors.New("member-3 has not acknowledged the replica")
		}
		acked = r
		return nil
	}
	drains := 0
	lab.pool.crashAfterStep = func(host, action string) {
		if action != "drain" {
			return
		}
		drains++
		recorded := poolUpdateHostRecord(acked, host)
		if acked == nil || recorded == nil || len(recorded.StoppedByUpdate) == 0 {
			t.Errorf("a guest on %s was asked to stop before the record naming it was acknowledged: %+v", host, acked)
		}
	}
	if err := lab.start("repl-1", "shutdown"); err != nil {
		t.Fatal(err)
	}
	lab.run(120, func(r *protocol.PoolUpdateRollout) bool { return len(poolUpdateHostRecord(r, "member-2").StoppedByUpdate) > 0 })
	for range 20 {
		if err := lab.coordinator().Advance(context.Background()); err != nil {
			t.Fatal(err)
		}
		lab.clock = lab.clock.Add(time.Second)
		lab.pool.tick()
	}
	if drains != 0 || !lab.pool.host("member-2").guests["vm-a"] {
		t.Fatalf("the guest was stopped with no acknowledgement: drains %d", drains)
	}
	refuse = false
	if r := lab.run(400, ended); r.State != protocol.PoolUpdateFinalized {
		t.Fatalf("final = %+v", r)
	}
	if drains == 0 || lab.pool.host("member-2").starts["vm-a"] != 1 {
		t.Fatalf("drains %d, starts %v", drains, lab.pool.host("member-2").starts)
	}
}

// A host rolled back by the operator and resumed is staged and activated again under a new
// attempt: the engine answers a repeated request ID with the operation it already ran, so a
// resume under the old IDs would reboot nothing.
func TestResumeAfterRollbackStagesAgainUnderANewAttempt(t *testing.T) {
	lab := newUpdateLab(t, 3)
	ctx := context.Background()
	if err := lab.start("restage-1", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(400, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageDone })
	ctl := func(control, host string) error {
		_, err := lab.c.Control(ctx, protocol.PoolUpdateRequest{RolloutID: "restage-1", Control: control, HostID: host})
		return err
	}
	if err := ctl("pause", ""); err != nil {
		t.Fatal(err)
	}
	if err := ctl("rollback_host", "member-2"); err != nil {
		t.Fatal(err)
	}
	lab.pool.tick()
	if err := ctl("resume", ""); err != nil {
		t.Fatal(err)
	}
	if r := lab.rollout(); r.Hosts[0].Attempt != 1 || r.Hosts[0].Stage != stagePending {
		t.Fatalf("resume did not start a new attempt: %+v", r.Hosts[0])
	}
	r := lab.run(600, ended)
	h := lab.pool.host("member-2")
	if r.State != protocol.PoolUpdateFinalized || h.sequence != 2 {
		t.Fatalf("final = %+v (member-2 runs %d)", r, h.sequence)
	}
	if len(h.stageIDs) != 2 || h.stageIDs[0] == h.stageIDs[1] || len(h.activations) != 2 {
		t.Fatalf("member-2 staged under %v and was activated under %v; want two attempts of each", h.stageIDs, h.activations)
	}
}
