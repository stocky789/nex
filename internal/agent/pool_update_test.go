// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"reflect"
	"slices"
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
	rollbacks   map[string]int  // rollback identities that actually armed the previous slot
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
	// controllerVM is a Controller appliance this host owns; the controller step swaps it once per
	// distinct step ID and fails once with controllerCode when that is set.
	controllerVM   bool
	swapped        bool
	controllerCode string
	controllerIDs  []string          // every step ID the controller step was asked under
	partitioned    bool              // up and ready in the pool's records, but not voting in Corosync
	stageFailed    map[string]string // stage request IDs that failed, with the code: the engine answers a replay the same way
	failedOps      map[string]bool   // undrain step IDs whose guest start failed: the engine's journal replays the failure
	startFailures  int               // how many distinct undrain step IDs still fail to start a guest
	guestCode      string            // a definite refusal of every drain and undrain
	statusFails    bool              // the status read fails while steps still work
	drainPending   bool              // a drain that has not finished and has not failed either
}

type fakeUpdatePool struct {
	t      *testing.T
	master *Store
	hosts  map[string]*fakeUpdateHost
	mu     sync.Mutex
	// downNow is the set of hosts that are rebooting right now.
	maxDown           int
	maxMaintenance    int
	activationOrder   []string
	crashAfterStep    func(host, action string) // called after a step returns, to simulate a crash
	witness           bool                      // a qdevice witness votes in the pool's Corosync
	loseRollbackReply bool
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
	if h.downFor > 0 || h.statusFails {
		return protocol.PoolUpdateHostStatus{}, errors.New("unreachable")
	}
	st := protocol.PoolUpdateHostStatus{HostID: h.id, Channel: h.channel, Phase: h.phase, RunningSequence: h.sequence,
		StateFormat: h.state, FreeBytes: h.free, ObservedAt: time.Now()}
	if h.capability {
		st.Capabilities = []string{protocol.PoolUpdateRolloutCapability}
	}
	st.ControllerVM = h.controllerVM
	if h.staged {
		st.StagedSequence = h.target.Sequence
	}
	// The engine offers no release the host already runs, so Target is nil once it does.
	if target := h.target; h.sequence < target.Sequence {
		st.Target = &target
	}
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
	// A member that predates the routes answers 404 to the step route; the live transport turns that into this error.
	if !h.capability {
		return protocol.PoolUpdateStepResult{}, errPoolUpdateUnsupported
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
		case h.stageFailed[step.StepID] != "":
			res = protocol.PoolUpdateStepResult{Code: h.stageFailed[step.StepID], Message: "refused by the host"}
		case h.stageCode != "":
			h.stageFailed[step.StepID] = h.stageCode
			res = protocol.PoolUpdateStepResult{Code: h.stageCode, Message: "refused by the host"}
		case !h.stageSeen[step.StepID]:
			h.stageSeen[step.StepID] = true
			h.stageIDs = append(h.stageIDs, step.StepID)
			h.staged = true
		}
	case "drain":
		if h.guestCode != "" {
			res = protocol.PoolUpdateStepResult{Code: h.guestCode, Message: "the host cannot do this"}
			break
		}
		if h.drainPending {
			res = protocol.PoolUpdateStepResult{}
			break
		}
		for _, id := range step.VMIDs {
			h.guests[id] = false
		}
	case "undrain":
		if h.guestCode != "" {
			res = protocol.PoolUpdateStepResult{Code: h.guestCode, Message: "the host cannot do this"}
			break
		}
		if h.failedOps[step.StepID] || h.startFailures > 0 {
			if !h.failedOps[step.StepID] {
				h.startFailures--
				h.failedOps[step.StepID] = true
			}
			res = protocol.PoolUpdateStepResult{Message: "the shared storage is not mounted yet"}
			break
		}
		for _, id := range step.VMIDs {
			if !h.guests[id] {
				h.guests[id] = true
				h.starts[id]++
			}
		}
	case "controller":
		if !h.controllerVM {
			p.t.Fatalf("controller step on %s, which owns no Controller", h.id)
		}
		h.controllerIDs = append(h.controllerIDs, step.StepID)
		if h.controllerCode != "" {
			res = protocol.PoolUpdateStepResult{Code: h.controllerCode, Message: "the previous image and its data were restored"}
			h.controllerCode = ""
			break
		}
		h.swapped = true
	case "activate":
		// controller_first: no host may go down before the Controller runs its new image.
		for _, other := range p.hosts {
			if other.controllerVM && other.target.ControllerImage && !other.swapped {
				p.t.Fatalf("activate on %s before the Controller on %s was swapped", h.id, other.id)
			}
		}
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
			p.activationOrder = append(p.activationOrder, h.id)
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
		if h.rollbacks[step.StepID] == 0 {
			h.sequence, h.phase, h.downFor = h.sequence-1, "rolled_back", 1
			h.rollbacks[step.StepID]++
		}
	}
	if p.crashAfterStep != nil {
		p.crashAfterStep(h.id, step.Action)
	}
	if step.Action == "rollback" && p.loseRollbackReply {
		p.loseRollbackReply = false
		return protocol.PoolUpdateStepResult{}, errors.New("rollback reply lost after delivery")
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
	controlRequests    int
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
			activations: map[string]int{}, rollbacks: map[string]int{}, stageSeen: map[string]bool{}, stageFailed: map[string]string{}, failedOps: map[string]bool{},
			bootsTo: 2, state: 2, channel: "stable", free: 1 << 40, capability: true,
			target: protocol.PoolUpdateTarget{Sequence: 2, StateFormat: 2, MixedPool: true, RequiredBytes: 1 << 20}}
	}
	maintenanceMembership(t, master, "master-1", members...)
	lab := &updateLab{T: t, pool: pool, clock: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)}
	prevPeers, prevNow, prevHA := poolUpdatePeerFactory, poolUpdateNow, poolUpdateHAEnabled
	prevPush, prevMaintPush, prevQuorum := pushPoolAuthorityReplicasAfterUpdate, pushPoolAuthorityReplicasAfterMaintenance, localQuorum
	prevTimeout, prevBoot, prevSoak := poolUpdateStepTimeout, poolUpdateBootDeadline, poolUpdateMinSoak
	poolUpdatePeerFactory = func(*Store) poolUpdatePeers { return pool }
	poolUpdateNow = func() time.Time { return lab.clock }
	localQuorum = func(context.Context) (vmhost.PoolQuorum, error) { return pool.quorum(), nil }
	noPush := func(context.Context, *Store, time.Duration) error { return nil }
	pushPoolAuthorityReplicasAfterUpdate, pushPoolAuthorityReplicasAfterMaintenance = noPush, noPush
	poolUpdateStepTimeout, poolUpdateBootDeadline, poolUpdateMinSoak = 10*time.Minute, 5*time.Minute, 3*time.Second
	t.Cleanup(func() {
		poolUpdatePeerFactory, poolUpdateNow, poolUpdateHAEnabled = prevPeers, prevNow, prevHA
		pushPoolAuthorityReplicasAfterUpdate, pushPoolAuthorityReplicasAfterMaintenance, localQuorum = prevPush, prevMaintPush, prevQuorum
		poolUpdateStepTimeout, poolUpdateBootDeadline, poolUpdateMinSoak = prevTimeout, prevBoot, prevSoak
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

func (l *updateLab) nextControlID() string {
	l.controlRequests++
	return fmt.Sprintf("control-%d", l.controlRequests)
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
	if _, err := lab.c.Control(context.Background(), lab.nextControlID(), protocol.PoolUpdateRequest{RolloutID: "refuse", Control: "resume"}); err != nil {
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
		// Votes alone would pass (3 left of 3 needed), but a ready host of the nodelist is not in the partition.
		{"a nodelist host absent with votes to spare", vmhost.PoolQuorum{Observed: true, Quorate: true, Hosts: []string{"master-1", "member-2"}, ExpectedVotes: 5, TotalVotes: 4, Quorum: 3}, protocol.PoolUpdateQuorumMargin},
		{"five expected, three present", vmhost.PoolQuorum{Observed: true, Quorate: true, Hosts: all, ExpectedVotes: 5, TotalVotes: 3, Quorum: 3}, protocol.PoolUpdateQuorumMargin},
		{"five expected, four present", vmhost.PoolQuorum{Observed: true, Quorate: true, Hosts: all, ExpectedVotes: 5, TotalVotes: 4, Quorum: 3}, ""},
	}
	for _, tc := range cases {
		localQuorum = func(context.Context) (vmhost.PoolQuorum, error) { return tc.q, nil }
		if got := refusalCode(poolUpdateQuorumGate(context.Background(), lab.pool.master, membership, "member-2")); got != tc.code {
			t.Fatalf("%s: code %q, want %q", tc.name, got, tc.code)
		}
	}
	localQuorum = func(context.Context) (vmhost.PoolQuorum, error) {
		return vmhost.PoolQuorum{}, errors.New("corosync is not running")
	}
	if got := refusalCode(poolUpdateQuorumGate(context.Background(), lab.pool.master, membership, "member-2")); got != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("an unreadable quorum on a clustered pool: %q", got)
	}
	// A host without Corosync at all (an image without the cluster layer) forms no cluster, so there is
	// no quorum to lose; a Corosync that is installed but unreadable still refuses (above).
	localQuorum = func(context.Context) (vmhost.PoolQuorum, error) {
		return vmhost.PoolQuorum{}, fmt.Errorf("corosync-quorumtool failed: %w", exec.ErrNotFound)
	}
	if got := refusalCode(poolUpdateQuorumGate(context.Background(), lab.pool.master, membership, "member-2")); got != "" {
		t.Fatalf("a host with no Corosync installed: %q", got)
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
		return lab.c.Control(ctx, lab.nextControlID(), protocol.PoolUpdateRequest{RolloutID: "c", Control: control, HostID: host})
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
	if _, err := lab.c.Control(ctx, lab.nextControlID(), protocol.PoolUpdateRequest{RolloutID: "nope", Control: "pause"}); refusalCode(err) != "rollout_not_found" {
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
	t.Run("a crash after the maintenance flip is cleaned up by abort", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		if err := lab.start("gate-r", "refuse"); err != nil {
			t.Fatal(err)
		}
		lab.run(20, func(r *protocol.PoolUpdateRollout) bool { return r.Phase == "hosts" })
		// The coordinator died after it flipped maintenance on and before it saved the next stage.
		if err := poolUpdateSetMaintenance(lab.pool.master, "member-2", true); err != nil {
			t.Fatal(err)
		}
		lab.pool.host("member-3").partitioned = true
		r := lab.run(5, paused)
		if r.Pause.Code != protocol.PoolUpdateQuorumMargin || r.Hosts[0].Stage != stageStaged {
			t.Fatalf("pause = %+v hosts = %+v", r.Pause, r.Hosts)
		}
		if _, err := lab.c.Control(context.Background(), lab.nextControlID(), protocol.PoolUpdateRequest{RolloutID: "gate-r", Control: "abort"}); err != nil {
			t.Fatal(err)
		}
		if m, _ := memberOf(lab.pool.master, "member-2"); m.Maintenance {
			t.Fatal("abort left maintenance on a host the record still showed as staged")
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
	if _, err := lab.c.Control(context.Background(), lab.nextControlID(), resume); refusalCode(err) != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("resume on a lost majority: %v", err)
	}
	if r := lab.rollout(); r.State != protocol.PoolUpdatePaused {
		t.Fatalf("a refused resume changed the record: %+v", r)
	}
	lab.pool.host("member-2").partitioned = false
	if _, err := lab.c.Control(context.Background(), lab.nextControlID(), resume); err != nil {
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
	lab.run(120, func(r *protocol.PoolUpdateRollout) bool {
		return len(poolUpdateHostRecord(r, "member-2").StoppedByUpdate) > 0
	})
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
		_, err := lab.c.Control(ctx, lab.nextControlID(), protocol.PoolUpdateRequest{RolloutID: "restage-1", Control: control, HostID: host})
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

// controller_first: the host that owns the Controller VM swaps its appliance before any host
// activates (the fake fails the test if one does), under one step ID however often the coordinator
// is restarted, and the rollout then completes as usual.
func TestControllerFirstSwapsBeforeAnyHostActivates(t *testing.T) {
	lab := newUpdateLab(t, 3)
	lab.restartCoordinator = true
	owner := lab.pool.host("member-3")
	owner.controllerVM = true
	for _, h := range lab.pool.hosts {
		h.target.ControllerImage = true
	}
	if err := lab.start("ctl-first-1", "refuse"); err != nil {
		t.Fatal(err)
	}
	r := lab.run(400, ended)
	if r.State != protocol.PoolUpdateFinalized || !owner.swapped {
		t.Fatalf("final = %+v swapped=%v", r, owner.swapped)
	}
	if c := r.Controller; c == nil || c.Stage != protocol.PoolUpdateControllerDone || c.HostID != "member-3" {
		t.Fatalf("controller record = %+v", c)
	}
	for _, id := range owner.controllerIDs[1:] {
		if id != owner.controllerIDs[0] {
			t.Fatalf("the swap was asked under several step IDs: %v", owner.controllerIDs)
		}
	}
	if lab.pool.host("master-1").swapped || lab.pool.host("member-2").swapped {
		t.Fatal("a host without the Controller was asked to swap it")
	}
}

func TestControllerFirstFailurePausesBeforeAnyHostAndResumeAsksAgain(t *testing.T) {
	lab := newUpdateLab(t, 3)
	owner := lab.pool.host("master-1")
	owner.controllerVM, owner.controllerCode = true, "controller_rolled_back"
	for _, h := range lab.pool.hosts {
		h.target.ControllerImage = true
	}
	if err := lab.start("ctl-first-2", "refuse"); err != nil {
		t.Fatal(err)
	}
	r := lab.run(100, paused)
	if r.Pause == nil || r.Pause.Code != protocol.PoolUpdateNeedsAttention || !strings.Contains(r.Pause.Message, "controller_rolled_back") || r.Phase != "controller" {
		t.Fatalf("pause = %+v phase %s", r.Pause, r.Phase)
	}
	for id, h := range lab.pool.hosts {
		if h.sequence != 1 || len(h.activations) != 0 {
			t.Fatalf("%s went on with the release although the Controller swap failed", id)
		}
	}
	if _, err := lab.c.Control(context.Background(), lab.nextControlID(), protocol.PoolUpdateRequest{RolloutID: "ctl-first-2", Control: "resume"}); err != nil {
		t.Fatal(err)
	}
	r = lab.run(600, ended)
	if r.State != protocol.PoolUpdateFinalized || !owner.swapped {
		t.Fatalf("final = %+v swapped=%v", r, owner.swapped)
	}
	if len(owner.controllerIDs) != 2 || owner.controllerIDs[0] == owner.controllerIDs[1] {
		t.Fatalf("a resumed swap must be a new swap, not a replay of the failed one: %v", owner.controllerIDs)
	}
}

func TestControllerFirstSkipsWhenThereIsNothingToSwap(t *testing.T) {
	lab := newUpdateLab(t, 3)
	// A Controller is here, but the release carries no Controller image.
	lab.pool.host("member-2").controllerVM = true
	if err := lab.start("ctl-first-3", "refuse"); err != nil {
		t.Fatal(err)
	}
	r := lab.run(400, ended)
	if r.State != protocol.PoolUpdateFinalized || len(lab.pool.host("member-2").controllerIDs) != 0 {
		t.Fatalf("final = %+v", r)
	}
}

// editMembership changes the master's membership rows without changing its generation.
func (l *updateLab) editMembership(change func(*protocol.PoolMembership)) {
	l.Helper()
	poolPeerMu.Lock()
	state, err := loadPoolPeer(l.pool.master)
	if err == nil {
		change(&state.Membership)
		err = savePoolPeer(l.pool.master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		l.Fatal(err)
	}
}

func (l *updateLab) control(id, control, host string) (*protocol.PoolUpdateRollout, error) {
	l.Helper()
	return l.c.Control(context.Background(), l.nextControlID(), protocol.PoolUpdateRequest{RolloutID: id, Control: control, HostID: host})
}

// A host that already runs and has committed the release (it joined on it, or an aborted rollout had
// updated it) is not refused as "release unavailable": the engine offers no release it already runs.
// The rollout updates the others and finalizes with that host counted as done.
func TestRolloutStartsForHostsThatAlreadyRunTheRelease(t *testing.T) {
	lab := newUpdateLab(t, 3)
	current := lab.pool.host("member-2")
	current.sequence = 2
	_, plan, err := lab.c.Start(context.Background(), "dry-current", protocol.PoolUpdateRequest{Sequence: 2, DryRun: true})
	if err != nil || strings.Join(plan.Order, ",") != "member-3,master-1" || strings.Join(plan.Current, ",") != "member-2" {
		t.Fatalf("plan = %+v err = %v", plan, err)
	}
	if err = lab.start("partial", "refuse"); err != nil {
		t.Fatal(err)
	}
	if r := lab.rollout(); r.Hosts[0].HostID != "member-2" || r.Hosts[0].Stage != stageDone {
		t.Fatalf("the host already on the release is not recorded as done: %+v", r.Hosts)
	}
	if r := lab.run(400, ended); r.State != protocol.PoolUpdateFinalized || r.FinalizedSequence != 2 {
		t.Fatalf("final = %+v", r)
	}
	if len(current.stageIDs) != 0 || len(current.activations) != 0 {
		t.Fatalf("a host already on the release was staged or activated: %v %v", current.stageIDs, current.activations)
	}
	if lab.pool.host("member-3").sequence != 2 || lab.pool.host("master-1").sequence != 2 {
		t.Fatal("the other hosts were not updated")
	}
	// Every host on the release: nothing to do, and the rollout still finalizes.
	if err = lab.start("nothing-left", "refuse"); err != nil {
		t.Fatalf("a rollout of a release every host runs: %v", err)
	}
	if r := lab.run(100, ended); r.State != protocol.PoolUpdateFinalized || r.FinalizedSequence != 2 {
		t.Fatalf("final = %+v", r)
	}
	// A host that never updated reports idle, not committed: it runs the release, so it is done.
	lab.pool.host("member-3").phase = "idle"
	if err = lab.start("idle-current", "refuse"); err != nil {
		t.Fatalf("a host that runs the release in phase idle was refused: %v", err)
	}
	if r := lab.run(100, ended); r.State != protocol.PoolUpdateFinalized || r.FinalizedSequence != 2 {
		t.Fatalf("final = %+v pause %+v", r, r.Pause)
	}
	// A host that runs the release but has not committed it is not "done".
	lab.pool.host("member-3").phase = "booted"
	if refusalCode(lab.start("uncommitted", "refuse")) != protocol.PoolUpdateUnconfirmedHost {
		t.Fatal("a host that runs the release uncommitted was treated as done")
	}
}

// A resume after a refused or failed step is a new attempt: the engine answers a repeated request ID
// with the failure it recorded, so a resume under the old IDs would pause again with the same code.
func TestResumeAfterAFailedStageRetriesUnderANewAttempt(t *testing.T) {
	lab := newUpdateLab(t, 3)
	h := lab.pool.host("member-3")
	h.stageCode = "insufficient_space"
	if err := lab.start("stage-fail", "refuse"); err != nil {
		t.Fatal(err)
	}
	r := lab.run(100, paused)
	if r.Pause.Code != protocol.PoolUpdateNeedsAttention || !strings.Contains(r.Pause.Message, "insufficient_space") {
		t.Fatalf("pause = %+v", r.Pause)
	}
	h.stageCode = "" // the operator made room
	if _, err := lab.control("stage-fail", "resume", ""); err != nil {
		t.Fatal(err)
	}
	r = lab.run(600, ended)
	if r.State != protocol.PoolUpdateFinalized || h.sequence != 2 {
		t.Fatalf("final = %+v (member-3 runs %d)", r, h.sequence)
	}
	if got := poolUpdateHostRecord(r, "member-3").Attempt; got != 1 {
		t.Fatalf("member-3 attempt = %d, want 1", got)
	}
}

// A guest start the host reports as failed is journaled by the engine under its operation ID and
// replayed unchanged; the coordinator retries it as a new step, so a mount that was not up yet at the
// first start does not strand the guest until the step times out.
func TestAFailedGuestStartIsRetriedAsANewOperation(t *testing.T) {
	lab := newUpdateLab(t, 3)
	h := lab.pool.host("member-2")
	h.guests["vm-a"], h.startFailures = true, 1
	if err := lab.start("start-fail", "shutdown"); err != nil {
		t.Fatal(err)
	}
	r := lab.run(400, ended)
	if r.State != protocol.PoolUpdateFinalized || h.starts["vm-a"] != 1 || !h.guests["vm-a"] {
		t.Fatalf("final = %+v starts %v", r, h.starts)
	}
	if len(h.failedOps) != 1 || poolUpdateHostRecord(r, "member-2").Attempt < 1 {
		t.Fatalf("failed ops %v, attempt %d: the retry was not a new operation", h.failedOps, poolUpdateHostRecord(r, "member-2").Attempt)
	}
}

// A host's definite refusal of drain or undrain stops the rollout at once with the host's own code
// and message, instead of being waited out as "guests did not shut down in time". An inventory that
// cannot be listed yet is the one code that is retried.
func TestGuestStepRefusalsPauseWithTheHostsWords(t *testing.T) {
	t.Run("drain", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		lab.pool.host("member-2").guests["vm-a"] = true
		lab.pool.host("member-2").guestCode = "unsupported_edition"
		began := lab.clock
		if err := lab.start("drain-refused", "shutdown"); err != nil {
			t.Fatal(err)
		}
		r := lab.run(100, paused)
		if r.Pause.Code != protocol.PoolUpdateNeedsAttention || !strings.Contains(r.Pause.Message, "unsupported_edition") ||
			!strings.Contains(r.Pause.Message, "the host cannot do this") || poolUpdateHostRecord(r, "member-2").Stage != stageDrain {
			t.Fatalf("pause = %+v hosts = %+v", r.Pause, r.Hosts)
		}
		if lab.clock.Sub(began) >= poolUpdateStepTimeout {
			t.Fatal("the refusal was only reported after the step timeout")
		}
	})
	t.Run("undrain", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		lab.pool.host("member-2").guests["vm-a"] = true
		if err := lab.start("undrain-refused", "shutdown"); err != nil {
			t.Fatal(err)
		}
		lab.run(400, func(r *protocol.PoolUpdateRollout) bool {
			return poolUpdateHostRecord(r, "member-2").Stage == stageRestart
		})
		lab.pool.host("member-2").guestCode = "unsupported_edition"
		began := lab.clock
		r := lab.run(100, paused)
		if r.Pause.Code != protocol.PoolUpdateNeedsAttention || !strings.Contains(r.Pause.Message, "the host cannot do this") {
			t.Fatalf("pause = %+v", r.Pause)
		}
		if lab.clock.Sub(began) >= poolUpdateStepTimeout {
			t.Fatal("the refusal was only reported after the step timeout")
		}
	})
	t.Run("an inventory that cannot be listed yet is retried", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		h := lab.pool.host("member-2")
		h.guests["vm-a"] = true
		if err := lab.start("inventory", "shutdown"); err != nil {
			t.Fatal(err)
		}
		lab.run(400, func(r *protocol.PoolUpdateRollout) bool {
			return poolUpdateHostRecord(r, "member-2").Stage == stageRestart
		})
		h.guestCode = "vm_inventory_unavailable"
		for range 10 {
			if err := lab.coordinator().Advance(context.Background()); err != nil {
				t.Fatal(err)
			}
			lab.clock = lab.clock.Add(time.Second)
		}
		if r := lab.rollout(); r.State != protocol.PoolUpdateRunning {
			t.Fatalf("a transient inventory failure stopped the rollout: %+v", r.Pause)
		}
		h.guestCode = ""
		if r := lab.run(400, ended); r.State != protocol.PoolUpdateFinalized {
			t.Fatalf("final = %+v", r)
		}
	})
}

// The activation gate is not skipped when the member's status cannot be read: nothing is sent until
// the answer is there, and a pool that lost its majority meanwhile never has the host armed.
func TestActivateWaitsWhenTheStatusCannotBeRead(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("status-gap", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(100, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageActivate })
	h := lab.pool.host("member-2")
	h.statusFails = true
	lab.pool.host("member-3").partitioned = true // activating now would take a second voter away
	r := lab.run(700, paused)
	if r.Pause.Code != protocol.PoolUpdateStepTimeout || len(h.activations) != 0 {
		t.Fatalf("pause = %+v activations %v", r.Pause, h.activations)
	}
}

// The waits that had no deadline now pause the rollout with the documented step_timeout (or
// unconfirmed for a host that cannot confirm), naming the host.
func TestEveryWaitTimesOut(t *testing.T) {
	t.Run("staging", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		if err := lab.start("wait-stage", "refuse"); err != nil {
			t.Fatal(err)
		}
		lab.pool.host("member-3").downFor = 1 << 30
		r := lab.run(700, paused)
		if r.Pause.Code != protocol.PoolUpdateStepTimeout || r.Phase != "staging" || !strings.Contains(r.Pause.Message, "member-3") {
			t.Fatalf("pause = %+v phase %s", r.Pause, r.Phase)
		}
	})
	t.Run("a staged host that cannot be reached", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		if err := lab.start("wait-staged", "refuse"); err != nil {
			t.Fatal(err)
		}
		lab.run(50, func(r *protocol.PoolUpdateRollout) bool { return r.Phase == "hosts" })
		lab.pool.host("member-2").downFor = 1 << 30
		r := lab.run(700, paused)
		if r.Pause.Code != protocol.PoolUpdateStepTimeout || r.Hosts[0].Stage != stageStaged || !strings.Contains(r.Pause.Message, "member-2") {
			t.Fatalf("pause = %+v hosts %+v", r.Pause, r.Hosts)
		}
	})
	t.Run("a host that cannot confirm", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		if err := lab.start("wait-confirm", "refuse"); err != nil {
			t.Fatal(err)
		}
		lab.run(100, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageConfirm })
		lab.pool.host("member-2").downFor = 1 << 30
		r := lab.run(700, paused)
		if r.Pause.Code != protocol.PoolUpdateUnconfirmedHost || r.Hosts[0].Stage != stageConfirm {
			t.Fatalf("pause = %+v hosts %+v", r.Pause, r.Hosts)
		}
	})
}

// settle drives the coordinator pass by pass for d of fake time, whatever the rollout does.
func (l *updateLab) settle(d time.Duration) {
	l.Helper()
	for end := l.clock.Add(d); l.clock.Before(end); {
		if err := l.coordinator().Advance(context.Background()); err != nil {
			l.Fatalf("advance: %v", err)
		}
		l.clock = l.clock.Add(time.Second)
		l.pool.tick()
	}
}

func aborted(r *protocol.PoolUpdateRollout) bool { return r.State == protocol.PoolUpdateAborted }

func (l *updateLab) forceAbort(id string) (*protocol.PoolUpdateRollout, error) {
	l.Helper()
	return l.c.Control(context.Background(), l.nextControlID(), protocol.PoolUpdateRequest{RolloutID: id, Control: "abort", Force: true})
}

// An abort that cannot confirm its cleanup does not end: the rollout stays paused
// abort_cleanup_pending, the coordinator keeps putting back what it owes, and only a confirmed
// cleanup (guests running, maintenance cleared) makes it aborted.
func TestAbortStaysOpenUntilEveryHostIsPutBack(t *testing.T) {
	t.Run("a host that comes back after the abort", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		h := lab.pool.host("member-3")
		h.bootFails, h.guests["vm-a"] = true, true
		if err := lab.start("abort-boot", "shutdown"); err != nil {
			t.Fatal(err)
		}
		lab.run(2000, paused)
		r, err := lab.control("abort-boot", "abort", "")
		if err != nil || r.State != protocol.PoolUpdatePaused || r.Pause.Code != protocol.PoolUpdateAbortCleanupPending || !strings.Contains(r.Pause.Message, "member-3") {
			t.Fatalf("abort of a host that is down = %+v %v", r, err)
		}
		if m, _ := memberOf(lab.pool.master, "member-3"); m.Maintenance {
			t.Fatal("abort left a booting host in maintenance mode")
		}
		host := poolUpdateHostRecord(r, "member-3")
		if !strings.Contains(host.Error, "vm-a") || len(host.StoppedByUpdate) != 1 {
			t.Fatalf("the guest the abort could not restart was not recorded: %+v", host)
		}
		lab.settle(time.Minute)
		if r = lab.rollout(); aborted(r) || r.Pause == nil || r.Pause.Code != protocol.PoolUpdateAbortCleanupPending {
			t.Fatalf("the abort ended while the host was still down: %+v", r)
		}
		h.downFor, h.bootFails = 0, false
		r = lab.run(200, aborted)
		host = poolUpdateHostRecord(r, "member-3")
		if !h.guests["vm-a"] || len(host.StoppedByUpdate) != 0 || host.Error != "" || r.Pause != nil {
			t.Fatalf("after the host came back: guest running %v, host %+v, pause %+v", h.guests["vm-a"], host, r.Pause)
		}
	})
	t.Run("a guest that will not start", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		h := lab.pool.host("member-2")
		h.guests["vm-a"], h.startFailures = true, 1<<30
		if err := lab.start("abort-start", "shutdown"); err != nil {
			t.Fatal(err)
		}
		began := lab.clock
		r := lab.run(2000, paused)
		if r.Pause.Code != protocol.PoolUpdateNeedsAttention || poolUpdateHostRecord(r, "member-2").Stage != stageRestart ||
			!strings.Contains(r.Pause.Message, "vm_start_failed") || !strings.Contains(r.Pause.Message, "the shared storage is not mounted yet") {
			t.Fatalf("pause = %+v hosts %+v", r.Pause, r.Hosts)
		}
		if lab.clock.Sub(began) >= poolUpdateStepTimeout {
			t.Fatal("a failing guest start was retried until the step timeout")
		}
		r, err := lab.control("abort-start", "abort", "")
		if err != nil || r.State != protocol.PoolUpdatePaused || r.Pause.Code != protocol.PoolUpdateAbortCleanupPending ||
			!strings.Contains(poolUpdateHostRecord(r, "member-2").Error, "vm-a") || !strings.Contains(r.Pause.Message, "keeps retrying") {
			t.Fatalf("abort = %+v %v", r, err)
		}
		// The coordinator retries under new operations, but only for poolUpdateAbortWindow.
		lab.settle(poolUpdateAbortWindow + time.Minute)
		r = lab.rollout()
		if aborted(r) || !strings.Contains(r.Pause.Message, "automatic retries stopped") {
			t.Fatalf("after the window: %+v pause %+v", r.State, r.Pause)
		}
		attempts := len(h.failedOps)
		if attempts > int(poolUpdateAbortWindow/poolUpdateRetryPause)+int(poolUpdateRetryWindow/poolUpdateRetryPause)+4 {
			t.Fatalf("%d failed start operations: the retries are not bounded", attempts)
		}
		lab.settle(10 * time.Minute)
		if len(h.failedOps) != attempts {
			t.Fatalf("the coordinator kept retrying after its window: %d then %d operations", attempts, len(h.failedOps))
		}
		// An operator's abort tries again at once, and ends once the guest runs.
		if r, err = lab.control("abort-start", "abort", ""); err != nil || aborted(r) || len(h.failedOps) != attempts+1 {
			t.Fatalf("operator retry = %+v %v (operations %d, was %d)", r.State, err, len(h.failedOps), attempts)
		}
		h.startFailures = 0
		r, err = lab.control("abort-start", "abort", "")
		if err != nil || !aborted(r) || !h.guests["vm-a"] || len(poolUpdateHostRecord(r, "member-2").StoppedByUpdate) != 0 {
			t.Fatalf("abort once the guest can start = %+v %v", r, err)
		}
	})
}

// The operator can end an abort that cannot finish with force; what was left stays in the record.
// Everything else about a pending abort is refused until then.
func TestForcedAbortAndWhatAPendingAbortRefuses(t *testing.T) {
	lab := newUpdateLab(t, 3)
	h := lab.pool.host("member-2")
	h.guests["vm-a"], h.startFailures = true, 1<<30
	if err := lab.start("force-abort", "shutdown"); err != nil {
		t.Fatal(err)
	}
	lab.run(2000, paused)
	if _, err := lab.forceAbort("force-abort"); refusalCode(err) != "abort_not_pending" {
		t.Fatalf("force on a rollout that is not being aborted: %v", err)
	}
	if _, err := lab.c.Control(context.Background(), lab.nextControlID(), protocol.PoolUpdateRequest{RolloutID: "force-abort", Control: "pause", Force: true}); refusalCode(err) != "invalid_request" {
		t.Fatalf("force with another control: %v", err)
	}
	if r, err := lab.control("force-abort", "abort", ""); err != nil || r.Pause == nil || r.Pause.Code != protocol.PoolUpdateAbortCleanupPending {
		t.Fatalf("abort = %+v %v", r, err)
	}
	if _, err := lab.control("force-abort", "resume", ""); refusalCode(err) != protocol.PoolUpdateAbortCleanupPending {
		t.Fatalf("resume of a rollout being aborted: %v", err)
	}
	if _, err := lab.control("force-abort", "rollback_host", "member-2"); refusalCode(err) != protocol.PoolUpdateAbortCleanupPending {
		t.Fatalf("rollback_host of a rollout being aborted: %v", err)
	}
	if err := lab.start("another", "refuse"); refusalCode(err) != "rollout_in_progress" {
		t.Fatalf("a new rollout while the abort is pending: %v", err)
	}
	r, err := lab.forceAbort("force-abort")
	host := poolUpdateHostRecord(r, "member-2")
	if err != nil || !aborted(r) || r.Pause != nil || len(host.StoppedByUpdate) != 1 || !strings.Contains(host.Error, "vm-a") {
		t.Fatalf("forced abort = %+v %v", r, err)
	}
	if _, err = lab.forceAbort("force-abort"); refusalCode(err) != "rollout_finished" {
		t.Fatalf("a second forced abort: %v", err)
	}
}

// A pending abort is what a promoted master inherits too: it carries on with it instead of turning
// it into an ordinary master_changed pause that a resume could revive.
func TestPromotedMasterKeepsAPendingAbort(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	pending := &protocol.PoolUpdateRollout{ID: "abort-1", PoolID: "pool-1", Sequence: 2, RunningVMs: "shutdown", State: protocol.PoolUpdatePaused,
		Pause: &protocol.PoolUpdatePause{Code: protocol.PoolUpdateAbortCleanupPending, Message: "member-1 is owed"},
		Phase: "hosts", MembershipGeneration: 1, CreatedAt: now, UpdatedAt: now,
		Hosts: []protocol.PoolUpdateHost{{HostID: "member-1", Stage: stageBoot, StoppedByUpdate: []string{"vm-a"}}}}
	promoted := peerStore(t, "member-2", "pool-1")
	if _, err := AdoptPoolUpdateOnPromotion(promoted, pending); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPoolUpdate(promoted)
	if err != nil || !poolUpdateAborting(got) || got.Hosts[0].StoppedByUpdate[0] != "vm-a" {
		t.Fatalf("the promoted master's record = %+v %v", got, err)
	}
}

// rollback_host reboots a host, so it takes the same quorum and readiness gates as any other
// coordinator-initiated stop.
func TestRollbackHostIsGated(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("rb-gate", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(400, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageDone })
	if _, err := lab.control("rb-gate", "pause", ""); err != nil {
		t.Fatal(err)
	}
	lab.pool.host("member-3").partitioned = true
	if _, err := lab.control("rb-gate", "rollback_host", "member-2"); refusalCode(err) != protocol.PoolUpdateQuorumMargin {
		t.Fatalf("rollback_host with another voter missing: %v", err)
	}
	if h := lab.pool.host("member-2"); h.sequence != 2 || h.phase == "rolled_back" {
		t.Fatalf("the host was rolled back through a lost majority: %+v", h)
	}
	lab.pool.host("member-3").partitioned = false
	if _, err := lab.control("rb-gate", "rollback_host", "member-2"); err != nil {
		t.Fatalf("rollback_host with the pool whole: %v", err)
	}
}

// The record on the members that could take over follows the rollout stage by stage, so a promoted
// master resumes where the host stands and not at an earlier stage that would, for instance, shut its
// restarted guests down again.
func TestRolloutIsReplicatedAtEveryStageChange(t *testing.T) {
	lab := newUpdateLab(t, 3)
	seen := map[string]bool{}
	pushPoolAuthorityReplicasAfterUpdate = func(context.Context, *Store, time.Duration) error {
		if r, _ := LoadPoolUpdate(lab.pool.master); r != nil && len(r.Hosts) > 0 {
			seen[r.Hosts[0].Stage] = true
		}
		return nil
	}
	if err := lab.start("replicate", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(400, ended)
	for _, stage := range []string{stagePending, stageStaged, stageTeardown, stageDrain, stageActivate, stageBoot, stageConfirm, stageRelease, stageRestart, stageSoak, stageDone} {
		if !seen[stage] {
			t.Fatalf("the record was never replicated with the first host at %q (seen %v)", stage, seen)
		}
	}
}

// A record a former master kept is not revived when authority returns to it: what it inherits decides.
func TestStaleLocalRolloutIsNotRevivedOnPromotion(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	stale := &protocol.PoolUpdateRollout{ID: "old", PoolID: "pool-1", Sequence: 2, RunningVMs: "refuse", State: protocol.PoolUpdateRunning,
		Phase: "hosts", MembershipGeneration: 1, CreatedAt: now, UpdatedAt: now,
		Hosts: []protocol.PoolUpdateHost{{HostID: "member-2", Stage: stageDrain}}}
	promoted := peerStore(t, "member-2", "pool-1")
	if err := savePoolUpdate(promoted, stale); err != nil {
		t.Fatal(err)
	}
	// The authority it inherits has no rollout: the one on disk was ended by another master.
	undo, err := AdoptPoolUpdateOnPromotion(promoted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadPoolUpdate(promoted); poolUpdateActive(got) {
		t.Fatalf("a stale running record owns the pool again: %+v", got)
	}
	undo()
	if got, _ := LoadPoolUpdate(promoted); got == nil || got.State != protocol.PoolUpdateRunning {
		t.Fatalf("undo did not put the earlier file back: %+v", got)
	}
	// The authority says the same rollout finished: that record, not the stale one.
	finished := *stale
	finished.State, finished.FinalizedSequence = protocol.PoolUpdateFinalized, 2
	if _, err = AdoptPoolUpdateOnPromotion(promoted, &finished); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadPoolUpdate(promoted); got == nil || got.State != protocol.PoolUpdateFinalized || got.FinalizedSequence != 2 {
		t.Fatalf("the finished rollout was not adopted over the stale one: %+v", got)
	}
}

// With armed watchdogs on a pool of four, only the host being drained leaves the cluster; the
// others keep feeding for as long as the pool wants its cluster, and the rollout does not wait for
// them. A pool that stops wanting a cluster tears it down everywhere and the rollout does wait.
func TestTeardownWaitsOnlyForTheDrainedHostWhileThePoolKeepsItsCluster(t *testing.T) {
	feeding := func(l *updateLab, ids ...string) {
		l.editMembership(func(m *protocol.PoolMembership) {
			for i := range m.Members {
				if slices.Contains(ids, m.Members[i].HostID) {
					m.Members[i].WatchdogArmed = &protocol.PoolWatchdogArmed{Feeding: true}
				}
			}
		})
	}
	// The drained host stops feeding when the pool flips it into maintenance, as its own reconcile does.
	disarmOnDrain := func(l *updateLab) {
		original := poolUpdateMaintenance
		poolUpdateMaintenance = func(s *Store, hostID string, on bool) error {
			err := original(s, hostID, on)
			if err == nil && on {
				l.editMembership(func(m *protocol.PoolMembership) {
					for i := range m.Members {
						if m.Members[i].HostID == hostID {
							m.Members[i].WatchdogArmed = nil
						}
					}
				})
			}
			return err
		}
		l.Cleanup(func() { poolUpdateMaintenance = original })
	}
	t.Run("four hosts keep their cluster", func(t *testing.T) {
		lab := newUpdateLab(t, 4)
		feeding(lab, "master-1", "member-2", "member-3", "member-4")
		disarmOnDrain(lab)
		if err := lab.start("td-four", "refuse"); err != nil {
			t.Fatal(err)
		}
		if r := lab.run(900, ended); r.State != protocol.PoolUpdateFinalized {
			t.Fatalf("final = %+v pause %+v", r, r.Pause)
		}
	})
	t.Run("three hosts tear their cluster down together", func(t *testing.T) {
		lab := newUpdateLab(t, 3)
		feeding(lab, "master-1", "member-2", "member-3")
		disarmOnDrain(lab)
		if err := lab.start("td-three", "refuse"); err != nil {
			t.Fatal(err)
		}
		r := lab.run(900, ended)
		if r.State != protocol.PoolUpdatePaused || r.Pause.Code != protocol.PoolUpdateStepTimeout || !strings.Contains(r.Pause.Message, "watchdogs") {
			t.Fatalf("a pool-wide teardown did not wait for the other hosts' watchdogs: %+v %+v", r.State, r.Pause)
		}
	})
}

// A guest step whose inventory stays unavailable (libvirt not coming back after a reboot) is
// retried for a few minutes, then pauses needs_attention with the code: it is not waited out until
// the six-hour step timeout.
func TestInventoryThatStaysUnavailablePausesAfterTheRetryWindow(t *testing.T) {
	for _, stage := range []string{stageDrain, stageRestart} {
		t.Run(stage, func(t *testing.T) {
			lab := newUpdateLab(t, 3)
			h := lab.pool.host("member-2")
			h.guests["vm-a"] = true
			if err := lab.start("inventory-gone-"+stage, "shutdown"); err != nil {
				t.Fatal(err)
			}
			if stage == stageRestart {
				lab.run(400, func(r *protocol.PoolUpdateRollout) bool {
					return poolUpdateHostRecord(r, "member-2").Stage == stageRestart
				})
			}
			h.guestCode = "vm_inventory_unavailable"
			began := lab.clock
			r := lab.run(2000, paused)
			if r.Pause.Code != protocol.PoolUpdateNeedsAttention || !strings.Contains(r.Pause.Message, "vm_inventory_unavailable") || poolUpdateHostRecord(r, "member-2").Stage != stage {
				t.Fatalf("pause = %+v hosts %+v", r.Pause, r.Hosts)
			}
			if waited := lab.clock.Sub(began); waited < poolUpdateRetryWindow || waited > poolUpdateRetryWindow+2*time.Minute {
				t.Fatalf("paused after %s, want about %s (the step timeout is %s)", waited, poolUpdateRetryWindow, poolUpdateStepTimeout)
			}
			// The operator fixes libvirt and resumes: the retry starts a fresh window.
			h.guestCode = ""
			if _, err := lab.control("inventory-gone-"+stage, "resume", ""); err != nil {
				t.Fatal(err)
			}
			if r = lab.run(600, ended); r.State != protocol.PoolUpdateFinalized {
				t.Fatalf("final = %+v pause %+v", r, r.Pause)
			}
		})
	}
}

// A step that alternates between failing and making progress opens a new retry window each time
// it fails again, but the step as a whole still ends at the step timeout.
func TestFailureEpisodesDoNotResetTheStepTimeout(t *testing.T) {
	lab := newUpdateLab(t, 3)
	h := lab.pool.host("member-2")
	h.guests["vm-a"] = true
	if err := lab.start("episodes", "shutdown"); err != nil {
		t.Fatal(err)
	}
	lab.run(400, func(r *protocol.PoolUpdateRollout) bool {
		return poolUpdateHostRecord(r, "member-2").Stage == stageDrain
	})
	began := lab.clock
	r := lab.run(2000, func(r *protocol.PoolUpdateRollout) bool {
		// Four minutes of failure (inside the retry window), then one answer that is no failure.
		if (lab.clock.Sub(began)/time.Second)%241 == 240 {
			h.guestCode, h.drainPending = "", true
		} else {
			h.guestCode, h.drainPending = "vm_inventory_unavailable", false
		}
		return paused(r)
	})
	if r.Pause.Code != protocol.PoolUpdateStepTimeout {
		t.Fatalf("pause = %+v", r.Pause)
	}
	if waited := lab.clock.Sub(began); waited < poolUpdateStepTimeout || waited > poolUpdateStepTimeout+time.Minute {
		t.Fatalf("paused after %s, want the step timeout %s", waited, poolUpdateStepTimeout)
	}
}

// A rollout record written before RetryStartedAt existed still loads, and the field is not written
// while it is unset.
func TestHostRecordWithoutRetryStartLoads(t *testing.T) {
	var host protocol.PoolUpdateHost
	if err := json.Unmarshal([]byte(`{"host_id":"member-2","stage":"drain","step_started_at":"2026-01-01T00:00:00Z","deadline_at":"0001-01-01T00:00:00Z","soak_until":"0001-01-01T00:00:00Z","error":"x"}`), &host); err != nil {
		t.Fatal(err)
	}
	if !host.RetryStartedAt.IsZero() || poolUpdateRetryStart(&host) != host.StepStartedAt {
		t.Fatalf("an old record's retry window does not fall back to the step start: %+v", host)
	}
	out, err := json.Marshal(protocol.PoolUpdateHost{HostID: "member-2"})
	if err != nil || strings.Contains(string(out), "retry_started_at") {
		t.Fatalf("unset retry start was written: %s %v", out, err)
	}
}

// peers that never answer until the caller gives up.
type hangingUpdatePeers struct{}

func (hangingUpdatePeers) Status(ctx context.Context, _ protocol.PoolMember) (protocol.PoolUpdateHostStatus, error) {
	<-ctx.Done()
	return protocol.PoolUpdateHostStatus{}, ctx.Err()
}

func (hangingUpdatePeers) Step(ctx context.Context, _ protocol.PoolMember, _ protocol.PoolUpdateStep) (protocol.PoolUpdateStepResult, error) {
	<-ctx.Done()
	return protocol.PoolUpdateStepResult{}, ctx.Err()
}

// A pass over unreachable hosts ends at its own deadline, so the lock a pause or an abort waits for
// is not held while every host in turn times out.
func TestAPassIsBoundedWhileHostsDoNotAnswer(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("hang", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.c.peers = hangingUpdatePeers{}
	previous := poolUpdatePassTimeout
	poolUpdatePassTimeout = 50 * time.Millisecond
	t.Cleanup(func() { poolUpdatePassTimeout = previous })
	done := make(chan error, 1)
	go func() { done <- lab.c.Advance(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a pass over hosts that never answer did not end at its deadline")
	}
	if r := lab.rollout(); r.State != protocol.PoolUpdateRunning || r.Phase != "staging" {
		t.Fatalf("the rollout moved without an answer: %+v", r)
	}
}

// A replica push that does not reach every member never stops the rollout, and is not silent; a
// push that was only busy (the probe loop's push was running) is not a miss.
func TestAReplicaPushThatFailsIsLoggedAndTheRolloutCarriesOn(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		logged bool
	}{
		"unreachable member": {errors.New("member-2 is unreachable"), true},
		"busy":               {errPoolAuthorityReplicaBusy, false},
	} {
		t.Run(name, func(t *testing.T) {
			lab := newUpdateLab(t, 3)
			pushPoolAuthorityReplicasAfterUpdate = func(context.Context, *Store, time.Duration) error { return tc.err }
			var logged strings.Builder
			previousOutput := log.Writer()
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(previousOutput) })
			if err := lab.start("push-fails", "refuse"); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(logged.String(), "not acknowledged by every member"); got != tc.logged {
				t.Fatalf("logged = %q, want a trace: %v", logged.String(), tc.logged)
			}
			if strings.Contains(logged.String(), "member-2") {
				t.Fatalf("the log carries the push error text: %q", logged.String())
			}
			if r := lab.run(400, ended); r.State != protocol.PoolUpdateFinalized {
				t.Fatalf("final = %+v pause %+v", r, r.Pause)
			}
		})
	}
}

func TestStartReplayIncludesNormalizedRunningVMsPolicy(t *testing.T) {
	for _, first := range []string{"", "refuse", "shutdown"} {
		t.Run("policy_"+first, func(t *testing.T) {
			lab := newUpdateLab(t, 3)
			if err := lab.start("policy-replay", first); err != nil {
				t.Fatal(err)
			}
			before := lab.rollout()
			equivalent := "refuse"
			changed := "shutdown"
			if first == "refuse" {
				equivalent = ""
			} else if first == "shutdown" {
				equivalent, changed = "shutdown", "refuse"
			}
			if err := lab.start("policy-replay", equivalent); err != nil {
				t.Fatalf("equivalent replay: %v", err)
			}
			if err := lab.start("policy-replay", changed); refusalCode(err) != "request_conflict" {
				t.Fatalf("changed shutdown intent accepted: %v", err)
			}
			if !reflect.DeepEqual(before, lab.rollout()) {
				t.Fatal("replay changed the durable rollout")
			}
		})
	}
}

func TestControlReplayDoesNotRepeatRollbackAfterResumeAndLaterRollout(t *testing.T) {
	lab := newUpdateLab(t, 3)
	ctx := context.Background()
	if err := lab.start("control-replay", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(400, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageDone })
	if _, err := lab.control("control-replay", "pause", ""); err != nil {
		t.Fatal(err)
	}
	request := protocol.PoolUpdateRequest{RolloutID: "control-replay", Control: "rollback_host", HostID: "member-2"}
	first, err := lab.c.Control(ctx, "rollback-replay", request)
	if err != nil {
		t.Fatal(err)
	}
	lab.pool.tick()
	if _, err = lab.control("control-replay", "resume", ""); err != nil {
		t.Fatal(err)
	}
	if r := lab.run(600, ended); r.State != protocol.PoolUpdateFinalized {
		t.Fatalf("resume ended at %+v", r)
	}
	lab.c = NewPoolUpdateCoordinator(lab.pool.master)
	got, err := lab.c.Control(ctx, "rollback-replay", request)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("replay after resume = %+v %v; want %+v", got, err, first)
	}
	for _, host := range lab.pool.hosts {
		host.target.Sequence, host.bootsTo = 3, 3
	}
	if _, _, err = lab.c.Start(ctx, "later-rollout", protocol.PoolUpdateRequest{Sequence: 3}); err != nil {
		t.Fatal(err)
	}
	latest := lab.rollout()
	promoted := peerStore(t, "member-3", "pool-1")
	if _, err = AdoptPoolUpdateOnPromotion(promoted, latest); err != nil {
		t.Fatal(err)
	}
	membership, err := Membership(lab.pool.master)
	if err != nil {
		t.Fatal(err)
	}
	maintenanceMembership(t, promoted, "member-3", membership.Members...)
	lab.pool.master, lab.c = promoted, NewPoolUpdateCoordinator(promoted)
	before := lab.rollout()
	got, err = lab.c.Control(ctx, "rollback-replay", request)
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("replay after new rollout and promotion = %+v %v", got, err)
	}
	if !reflect.DeepEqual(before, lab.rollout()) || len(lab.pool.host("member-2").rollbacks) != 1 || lab.pool.host("member-2").sequence != 2 {
		t.Fatal("delayed control changed current rollout or rolled the host back again")
	}
	for _, changed := range []protocol.PoolUpdateRequest{
		{RolloutID: "later-rollout", Control: "rollback_host", HostID: "member-2"},
		{RolloutID: "control-replay", Control: "rollback_host", HostID: "master-1"},
		{RolloutID: "control-replay", Control: "resume"},
	} {
		if _, err = lab.c.Control(ctx, "rollback-replay", changed); refusalCode(err) != "request_conflict" {
			t.Fatalf("changed intent accepted: %+v %v", changed, err)
		}
	}
}

func TestControlReplaysOriginalRefusalAfterStateChanges(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("refused-control", "refuse"); err != nil {
		t.Fatal(err)
	}
	request := protocol.PoolUpdateRequest{RolloutID: "refused-control", Control: "rollback_host", HostID: "member-2"}
	_, first := lab.c.Control(context.Background(), "refusal-replay", request)
	if refusalCode(first) != "rollout_running" {
		t.Fatalf("first refusal = %v", first)
	}
	if _, err := lab.control("refused-control", "pause", ""); err != nil {
		t.Fatal(err)
	}
	lab.c = NewPoolUpdateCoordinator(lab.pool.master)
	if _, err := lab.c.Control(context.Background(), "refusal-replay", request); err == nil || err.Error() != first.Error() {
		t.Fatalf("refusal replay = %v; want %v", err, first)
	}
	request.Control, request.HostID = "resume", ""
	if _, err := lab.c.Control(context.Background(), "refusal-replay", request); refusalCode(err) != "request_conflict" {
		t.Fatalf("refused identity reused with changed intent: %v", err)
	}
}

func TestPendingRollbackBlocksTransitionsAndRetriesTheSameHostOperation(t *testing.T) {
	lab := newUpdateLab(t, 3)
	ctx := context.Background()
	if err := lab.start("pending-control", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(400, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageDone })
	if _, err := lab.control("pending-control", "pause", ""); err != nil {
		t.Fatal(err)
	}
	request := protocol.PoolUpdateRequest{RolloutID: "pending-control", Control: "rollback_host", HostID: "member-2"}
	lab.pool.crashAfterStep = func(_, action string) {
		if action == "rollback" {
			panic("crash after rollback")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("rollback did not reach the simulated crash")
			}
		}()
		_, _ = lab.c.Control(ctx, "pending-rollback", request)
	}()
	lab.pool.crashAfterStep = nil
	before := lab.rollout()
	if _, err := lab.control("pending-control", "resume", ""); refusalCode(err) != "control_pending" {
		t.Fatalf("resume overtook pending rollback: %v", err)
	}
	changed := request
	changed.HostID = "master-1"
	if _, err := lab.c.Control(ctx, "pending-rollback", changed); refusalCode(err) != "request_conflict" {
		t.Fatalf("pending intent changed: %v", err)
	}
	if err := lab.c.Advance(ctx); refusalCode(err) != "rollback_pending" || !reflect.DeepEqual(before, lab.rollout()) {
		t.Fatalf("Advance did not preserve ambiguous pending control: %v", err)
	}
	lab.pool.tick()
	lab.c = NewPoolUpdateCoordinator(lab.pool.master)
	result, err := lab.c.Control(ctx, "pending-rollback", request)
	if err != nil || result.Pause == nil || result.Pause.Code != protocol.PoolUpdateRolledBack {
		t.Fatalf("pending retry = %+v %v", result, err)
	}
	if len(lab.pool.host("member-2").rollbacks) != 1 || lab.pool.host("member-2").sequence != 1 {
		t.Fatal("pending replay armed a second rollback")
	}
	if _, err = lab.control("pending-control", "resume", ""); err != nil {
		t.Fatalf("completed pending control still blocks resume: %v", err)
	}
}

func TestPromotedMasterUpdatesLastWithoutDisplacingAnActiveHost(t *testing.T) {
	for _, tc := range []struct {
		name, promote, stage string
		want                 []string
	}{
		{"untouched_master", "member-2", stageStaged, []string{"member-3", "master-1", "member-2"}},
		{"active_member", "member-3", stageDrain, []string{"member-2", "master-1", "member-3"}},
		{"active_master_finishes_safely", "member-2", stageDrain, []string{"member-2", "member-3", "master-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lab := newUpdateLab(t, 3)
			lab.pool.host("member-2").guests["vm-active"] = true
			// Keep member-2 first despite its guest, then test preservation of the active host.
			lab.pool.host("member-3").guests["vm-other"] = true
			if err := lab.start("promotion-order", "shutdown"); err != nil {
				t.Fatal(err)
			}
			lab.run(100, func(r *protocol.PoolUpdateRollout) bool {
				return r.Phase == "hosts" && r.Hosts[0].Stage == tc.stage
			})
			inherited := lab.rollout()
			membership, err := Membership(lab.pool.master)
			if err != nil {
				t.Fatal(err)
			}
			promoted := peerStore(t, tc.promote, "pool-1")
			if _, err = AdoptPoolUpdateOnPromotion(promoted, inherited); err != nil {
				t.Fatal(err)
			}
			maintenanceMembership(t, promoted, tc.promote, membership.Members...)
			lab.pool.master, lab.c = promoted, NewPoolUpdateCoordinator(promoted)
			if tc.stage == stageDrain {
				adopted := lab.rollout()
				if adopted.Hosts[0].HostID != "member-2" || !reflect.DeepEqual(adopted.Hosts[0], inherited.Hosts[0]) {
					t.Fatal("promotion displaced or rewrote the host already in maintenance")
				}
			}
			if _, err = lab.control("promotion-order", "resume", ""); err != nil {
				t.Fatal(err)
			}
			if r := lab.run(600, ended); r.State != protocol.PoolUpdateFinalized {
				t.Fatalf("promoted rollout = %+v", r)
			}
			if !reflect.DeepEqual(lab.pool.activationOrder, tc.want) {
				t.Fatalf("activation order = %v; want %v", lab.pool.activationOrder, tc.want)
			}
			for _, host := range lab.pool.hosts {
				for vmID, running := range host.guests {
					if !running || host.starts[vmID] != 1 {
						t.Fatalf("promotion stranded or restarted guest twice: %s %s", host.id, vmID)
					}
				}
			}
		})
	}
}

func TestPendingControlStopsCoordinatorBeforeAnyHostTransition(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("pending-abort", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(50, func(r *protocol.PoolUpdateRollout) bool { return r.Phase == "hosts" })
	r := lab.rollout()
	r.Controls = append(r.Controls, protocol.PoolUpdateControlResult{RequestID: "interrupted-abort",
		Request: protocol.PoolUpdateRequest{RolloutID: r.ID, Control: "abort"}})
	if err := savePoolUpdate(lab.pool.master, r); err != nil {
		t.Fatal(err)
	}
	lab.c = NewPoolUpdateCoordinator(lab.pool.master)
	if err := lab.c.Advance(context.Background()); err != nil || lab.rollout().State != protocol.PoolUpdateAborted || len(lab.pool.activationOrder) != 0 {
		t.Fatalf("coordinator did not complete accepted abort before advancing hosts: %v", err)
	}
	if _, err := lab.c.Control(context.Background(), "interrupted-abort", r.Controls[0].Request); err != nil {
		t.Fatalf("accepted abort could not finish: %v", err)
	}
	if lab.rollout().State != protocol.PoolUpdateAborted {
		t.Fatal("pending abort did not end the rollout")
	}
}

func TestRollbackReplicationFailureRetainsSafeReplayAcrossPromotion(t *testing.T) {
	for _, failure := range []string{"intent", "result"} {
		t.Run(failure, func(t *testing.T) {
			lab := newUpdateLab(t, 3)
			ctx := context.Background()
			if err := lab.start("replicated-control", "refuse"); err != nil {
				t.Fatal(err)
			}
			lab.run(400, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageDone })
			if _, err := lab.control("replicated-control", "pause", ""); err != nil {
				t.Fatal(err)
			}
			request := protocol.PoolUpdateRequest{RolloutID: "replicated-control", Control: "rollback_host", HostID: "member-2"}
			fail := true
			var acknowledged *protocol.PoolUpdateRollout
			pushPoolAuthorityReplicasAfterUpdate = func(context.Context, *Store, time.Duration) error {
				r := lab.rollout()
				control := r.Controls[len(r.Controls)-1]
				pending := control.Rollout == nil && control.Code == ""
				if fail && (failure == "intent" && pending || failure == "result" && !pending) {
					return errors.New("replica delivery unavailable")
				}
				acknowledged = r
				return nil
			}
			if _, err := lab.c.Control(ctx, "replicated-rollback", request); refusalCode(err) != "replica_pending" {
				t.Fatalf("replication failure = %v", err)
			}
			host := lab.pool.host("member-2")
			if failure == "intent" {
				if len(host.rollbacks) != 0 {
					t.Fatal("rollback ran before its intent was acknowledged")
				}
				if _, err := lab.control(request.RolloutID, "resume", ""); refusalCode(err) != "control_pending" {
					t.Fatalf("resume overtook unacknowledged rollback: %v", err)
				}
			} else {
				if acknowledged == nil || poolUpdatePendingControl(acknowledged) == nil || len(host.rollbacks) != 1 {
					t.Fatal("result failure did not leave acknowledged pending intent and one rollback")
				}
				membership, err := Membership(lab.pool.master)
				if err != nil {
					t.Fatal(err)
				}
				promoted := peerStore(t, "member-3", "pool-1")
				if _, err = AdoptPoolUpdateOnPromotion(promoted, acknowledged); err != nil {
					t.Fatal(err)
				}
				maintenanceMembership(t, promoted, "member-3", membership.Members...)
				lab.pool.master, lab.c = promoted, NewPoolUpdateCoordinator(promoted)
				lab.pool.tick()
			}
			fail = false
			if failure == "result" {
				lab.pool.master.State.Journal = nil // The new master has none of the old command journal.
				if err := lab.c.Advance(ctx); err != nil || poolUpdatePendingControl(lab.rollout()) != nil {
					t.Fatalf("promoted Core needed old journal or Controller retry to finish accepted control: %v", err)
				}
			}
			got, err := lab.c.Control(ctx, "replicated-rollback", request)
			if err != nil || got.Pause == nil || got.Pause.Code != protocol.PoolUpdateRolledBack || len(host.rollbacks) != 1 || host.sequence != 1 {
				t.Fatalf("replication retry = %+v %v; rollbacks %v sequence %d", got, err, host.rollbacks, host.sequence)
			}
			replay, err := lab.c.Control(ctx, "replicated-rollback", request)
			if err != nil || !reflect.DeepEqual(got, replay) || len(host.rollbacks) != 1 {
				t.Fatalf("settled control replay = %+v %v", replay, err)
			}
		})
	}
}

func TestRelayedControlUsesStableRequestIdentity(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("relayed-control", "refuse"); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "controller-operation-1", Kind: "pool-update",
		Pool: &protocol.PoolCommand{RequestID: "relayed-pause", Action: "update-control",
			Membership: protocol.PoolMembership{MasterHostID: "master-1"},
			Update:     &protocol.PoolUpdateRequest{RolloutID: "relayed-control", Control: "pause"}}}
	if result := ExecutePoolUpdate(context.Background(), lab.pool.master, cmd); result.Status != "succeeded" {
		t.Fatalf("relayed pause = %+v", result)
	}
	if _, err := lab.control("relayed-control", "resume", ""); err != nil {
		t.Fatal(err)
	}
	cmd.OperationID = "controller-operation-retry"
	if result := ExecutePoolUpdate(context.Background(), lab.pool.master, cmd); result.Status != "succeeded" || lab.rollout().State != protocol.PoolUpdateRunning {
		t.Fatalf("relayed retry applied pause again: %+v", result)
	}
	cmd.Pool.Update.Control = "abort"
	if result := ExecutePoolUpdate(context.Background(), lab.pool.master, cmd); result.Status != "failed" || !strings.HasPrefix(result.Error, "request_conflict:") {
		t.Fatalf("relayed identity accepted changed intent: %+v", result)
	}
}

func TestCoreContinuesAmbiguousRollbackWithoutController(t *testing.T) {
	lab := newUpdateLab(t, 3)
	ctx := context.Background()
	if err := lab.start("lost-rollback", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(400, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageDone })
	if _, err := lab.control("lost-rollback", "pause", ""); err != nil {
		t.Fatal(err)
	}
	request := protocol.PoolUpdateRequest{RolloutID: "lost-rollback", Control: "rollback_host", HostID: "member-2"}
	lab.pool.loseRollbackReply = true
	if _, err := lab.c.Control(ctx, "lost-response", request); refusalCode(err) != "rollback_pending" {
		t.Fatalf("ambiguous rollback = %v", err)
	}
	if _, err := lab.control("lost-rollback", "resume", ""); refusalCode(err) != "control_pending" {
		t.Fatalf("resume accepted while rollback outcome was unknown: %v", err)
	}
	if err := lab.c.Advance(ctx); refusalCode(err) != "rollback_pending" {
		t.Fatalf("offline rollback outcome was settled: %v", err)
	}
	lab.pool.tick()
	lab.c = NewPoolUpdateCoordinator(lab.pool.master)
	if err := lab.c.Advance(ctx); err != nil {
		t.Fatalf("Core continuation did not reconcile rollback: %v", err)
	}
	host := lab.pool.host("member-2")
	if len(host.rollbacks) != 1 || host.sequence != 1 || poolUpdateHostRecord(lab.rollout(), host.id).Stage != stageRolledBack {
		t.Fatal("continuation lost the rollback outcome or armed it twice")
	}
	first, err := lab.c.Control(ctx, "lost-response", request)
	if err != nil || first.Pause == nil || first.Pause.Code != protocol.PoolUpdateRolledBack {
		t.Fatalf("reconciled result replay = %+v %v", first, err)
	}
	if _, err = lab.control("lost-rollback", "resume", ""); err != nil {
		t.Fatal(err)
	}
	if r := lab.run(600, ended); r.State != protocol.PoolUpdateFinalized {
		t.Fatalf("resume skipped the rolled-back host: %+v", r)
	}
	replay, err := lab.c.Control(ctx, "lost-response", request)
	if err != nil || !reflect.DeepEqual(replay, first) || len(host.rollbacks) != 1 {
		t.Fatalf("original result changed after resumed update: %+v %v", replay, err)
	}
}

func TestUnacknowledgedControlResultBlocksTransitionsUntilCoreAcknowledges(t *testing.T) {
	for _, action := range []string{"abort", "rollback_host"} {
		t.Run(action, func(t *testing.T) {
			lab := newUpdateLab(t, 3)
			ctx := context.Background()
			if err := lab.start("unacked-result", "refuse"); err != nil {
				t.Fatal(err)
			}
			request := protocol.PoolUpdateRequest{RolloutID: "unacked-result", Control: action}
			if action == "rollback_host" {
				lab.run(400, func(r *protocol.PoolUpdateRollout) bool { return r.Hosts[0].Stage == stageDone })
				if _, err := lab.control(request.RolloutID, "pause", ""); err != nil {
					t.Fatal(err)
				}
				request.HostID = "member-2"
			}
			fail := true
			pushPoolAuthorityReplicasAfterUpdate = func(context.Context, *Store, time.Duration) error {
				r := lab.rollout()
				if fail && r.Controls[len(r.Controls)-1].Rollout != nil {
					return errors.New("terminal replica reply unavailable")
				}
				return nil
			}
			if _, err := lab.c.Control(ctx, "unacked-control", request); refusalCode(err) != "replica_pending" {
				t.Fatalf("failed result ACK = %v", err)
			}
			before := lab.rollout()
			original := before.Controls[len(before.Controls)-1].Rollout
			if _, err := lab.control(request.RolloutID, "resume", ""); refusalCode(err) != "control_pending" {
				t.Fatalf("new control overtook unacknowledged terminal result: %v", err)
			}
			if _, _, err := lab.c.Start(ctx, "new-rollout", protocol.PoolUpdateRequest{Sequence: 2}); refusalCode(err) != "control_pending" {
				t.Fatalf("new rollout overtook unacknowledged terminal result: %v", err)
			}
			if err := lab.c.Advance(ctx); refusalCode(err) != "replica_pending" || !reflect.DeepEqual(before, lab.rollout()) {
				t.Fatalf("Advance changed unacknowledged result: %v", err)
			}
			fail = false
			lab.c = NewPoolUpdateCoordinator(lab.pool.master)
			if err := lab.c.Advance(ctx); err != nil || poolUpdatePendingControl(lab.rollout()) != nil {
				t.Fatalf("Core did not finish result ACK: %v", err)
			}
			replay, err := lab.c.Control(ctx, "unacked-control", request)
			if err != nil || !reflect.DeepEqual(replay, original) {
				t.Fatalf("result changed during ACK recovery: %+v %v", replay, err)
			}
		})
	}
}

func TestControlCapacityAdmissionPreservesAcceptedReplayHistory(t *testing.T) {
	lab := newUpdateLab(t, 64)
	ctx := context.Background()
	if err := lab.start("large-history", "refuse"); err != nil {
		t.Fatal(err)
	}
	r := lab.rollout()
	snapshot := *r
	snapshot.Controls = nil
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	count := (protocol.PoolReplicaLimit - len(raw) - 16384) / (len(raw) + 256)
	for i := range count {
		r.Controls = append(r.Controls, protocol.PoolUpdateControlResult{
			RequestID: fmt.Sprintf("retained-control-%03d", i),
			Request:   protocol.PoolUpdateRequest{RolloutID: r.ID, Control: "pause"}, Rollout: &snapshot})
	}
	if err = savePoolUpdate(lab.pool.master, r); err != nil {
		t.Fatal(err)
	}
	if _, _, err = buildPoolAuthorityReplica(lab.pool.master); err != nil {
		t.Fatalf("accepted history was not representable before admission: %v", err)
	}
	before := lab.rollout()
	if _, err = lab.c.Control(ctx, "no-room-for-result", protocol.PoolUpdateRequest{RolloutID: r.ID, Control: "pause"}); refusalCode(err) != "authority_capacity" {
		t.Fatalf("full terminal snapshot was not reserved: %v", err)
	}
	if !reflect.DeepEqual(before, lab.rollout()) {
		t.Fatal("refused admission changed accepted history or left an impossible pending receipt")
	}
	replay, err := lab.c.Control(ctx, r.Controls[0].RequestID, r.Controls[0].Request)
	if err != nil || !reflect.DeepEqual(replay, &snapshot) {
		t.Fatalf("capacity refusal discarded an accepted replay: %v", err)
	}
}

func TestPromotionPreservesMaintenanceBeforeCoordinatorStageWasSaved(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("maintenance-window", "refuse"); err != nil {
		t.Fatal(err)
	}
	lab.run(50, func(r *protocol.PoolUpdateRollout) bool { return r.Phase == "hosts" })
	if err := poolUpdateSetMaintenance(lab.pool.master, "member-2", true); err != nil {
		t.Fatal(err)
	}
	inherited := lab.rollout()
	membership, err := Membership(lab.pool.master)
	if err != nil {
		t.Fatal(err)
	}
	promoted := peerStore(t, "member-2", "pool-1")
	if _, err = AdoptPoolUpdateOnPromotion(promoted, inherited); err != nil {
		t.Fatal(err)
	}
	maintenanceMembership(t, promoted, "member-2", membership.Members...)
	lab.pool.master, lab.c = promoted, NewPoolUpdateCoordinator(promoted)
	if _, err = lab.control(inherited.ID, "resume", ""); err != nil {
		t.Fatalf("resume displaced a host with replicated maintenance: %v", err)
	}
	if r := lab.run(600, ended); r.State != protocol.PoolUpdateFinalized || lab.pool.activationOrder[0] != "member-2" {
		t.Fatalf("maintenance crash recovery = %+v order %v", r, lab.pool.activationOrder)
	}
}

func TestNewRolloutCapacityAdmissionPreservesCompletedControlHistory(t *testing.T) {
	lab := newUpdateLab(t, 64)
	ctx := context.Background()
	if err := lab.start("previous-rollout", "refuse"); err != nil {
		t.Fatal(err)
	}
	r := lab.rollout()
	snapshot := *r
	snapshot.Controls = nil
	r.State, r.Hosts = protocol.PoolUpdateAborted, nil
	r.Controls = []protocol.PoolUpdateControlResult{{RequestID: "retained-large-result",
		Request: protocol.PoolUpdateRequest{RolloutID: r.ID, Control: "pause"}, Rollout: &snapshot}}
	if err := savePoolUpdate(lab.pool.master, r); err != nil {
		t.Fatal(err)
	}
	replica, _, err := buildPoolAuthorityReplica(lab.pool.master)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(replica)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Hosts = append([]protocol.PoolUpdateHost(nil), snapshot.Hosts...)
	snapshot.Hosts[0].Error = strings.Repeat("x", protocol.PoolReplicaLimit-len(raw)-4096)
	if err = savePoolUpdate(lab.pool.master, r); err != nil {
		t.Fatal(err)
	}
	if _, _, err = buildPoolAuthorityReplica(lab.pool.master); err != nil {
		t.Fatalf("previous authority was not representable: %v", err)
	}
	before := lab.rollout()
	if _, _, err = lab.c.Start(ctx, "would-exceed-authority", protocol.PoolUpdateRequest{Sequence: 2}); refusalCode(err) != "authority_capacity" {
		t.Fatalf("new rollout did not admit its own full record: %v", err)
	}
	if !reflect.DeepEqual(before, lab.rollout()) {
		t.Fatal("failed new-rollout admission replaced completed history")
	}
}

func TestPendingResultReservationProtectsOtherAuthorityAdmission(t *testing.T) {
	lab := newUpdateLab(t, 3)
	if err := lab.start("reserved-result", "refuse"); err != nil {
		t.Fatal(err)
	}
	replica, _, err := buildPoolAuthorityReplica(lab.pool.master)
	if err != nil {
		t.Fatal(err)
	}
	replica.UpdateRollout.Controls = []protocol.PoolUpdateControlResult{{RequestID: "reserved-abort",
		Request: protocol.PoolUpdateRequest{RolloutID: replica.UpdateRollout.ID, Control: "abort"}, ReservedResultBytes: 32768}}
	raw, err := json.Marshal(replica)
	if err != nil {
		t.Fatal(err)
	}
	replica.VMs = json.RawMessage(`{"vm-capacity":{"description":"` + strings.Repeat("x", protocol.PoolReplicaLimit-len(raw)-16384) + `"}}`)
	peer, err := loadPoolPeer(lab.pool.master)
	if err != nil {
		t.Fatal(err)
	}
	if err = poolAuthorityReplicaFitsAddressedBodies(lab.pool.master, replica, peer.CAKeyPEM); err == nil {
		t.Fatal("another authority write consumed the accepted control's terminal-result room")
	}
	replica.UpdateRollout.Controls[0].ReservedResultBytes = 0
	if err = poolAuthorityReplicaFitsAddressedBodies(lab.pool.master, replica, peer.CAKeyPEM); err != nil {
		t.Fatalf("test authority did not fit without the reservation: %v", err)
	}
	for _, invalid := range []int{-1, protocol.PoolReplicaLimit + 1, int(^uint(0) >> 1)} {
		replica.UpdateRollout.Controls[0].ReservedResultBytes = invalid
		if err = poolAuthorityReplicaFitsAddressedBodies(lab.pool.master, replica, peer.CAKeyPEM); err == nil {
			t.Fatalf("invalid/overflowing reservation admitted: %d", invalid)
		}
	}
}

func updateSnapshotCapacityFixture(t *testing.T, snapshotFirst bool) (*updateLab, poolVMRequest, protocol.PoolUpdateRequest) {
	t.Helper()
	lab := newUpdateLab(t, 3)
	if err := lab.start("capacity-race", "refuse"); err != nil {
		t.Fatal(err)
	}
	s := lab.pool.master
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	if err := poolVMLocal(s, spec.ID, "define", nil, true, &spec, nil); err != nil {
		t.Fatal(err)
	}
	snapshotRequest := poolVMRequest{VMID: spec.ID, Action: "snapshot-reserve", OperationID: "capacity-snapshot",
		VM: &spec, Snapshot: &protocol.VMSnapshotRequest{Name: "capacity-point", Description: strings.Repeat("d", 512)},
		SnapshotCreatedAt: lab.clock, IntentSHA256: strings.Repeat("a", 64), OwnerOperationOrder: 1}
	if snapshotFirst {
		if err := reserveUpdateCapacitySnapshot(s, snapshotRequest); err != nil {
			t.Fatal(err)
		}
	}
	r := lab.rollout()
	history := *r
	history.Controls = nil
	history.Hosts = append([]protocol.PoolUpdateHost(nil), r.Hosts...)
	r.Controls = []protocol.PoolUpdateControlResult{{RequestID: "retained-capacity-result",
		Request: protocol.PoolUpdateRequest{RolloutID: r.ID, Control: "pause"}, Rollout: &history}}
	request := protocol.PoolUpdateRequest{RolloutID: r.ID, Control: "abort"}
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := poolUpdateResultReservation(r, membership, request)
	if err != nil {
		t.Fatal(err)
	}
	prospective := *r
	prospective.Controls = append(append([]protocol.PoolUpdateControlResult(nil), r.Controls...),
		protocol.PoolUpdateControlResult{RequestID: "capacity-control", Request: request, ReservedResultBytes: reserved})
	replica, _, err := buildPoolAuthorityReplica(s)
	if err != nil {
		t.Fatal(err)
	}
	replica.UpdateRollout = &prospective
	padUpdateCapacityHistory(t, s, &history, replica)
	if err = savePoolUpdate(s, r); err != nil {
		t.Fatal(err)
	}
	return lab, snapshotRequest, request
}

func reserveUpdateCapacitySnapshot(s *Store, request poolVMRequest) error {
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	_, err := reservePoolVMSnapshotLocked(s, request.VM.HostID, request)
	return err
}

func TestConcurrentControlAndSnapshotCapacityAdmissionsCannotBothEnlargeAuthority(t *testing.T) {
	lab, snapshotRequest, request := updateSnapshotCapacityFixture(t, false)
	s := lab.pool.master
	originalProjection := poolUpdateAuthorityProjection
	originalPush := pushPoolAuthorityReplicasAfterUpdate
	t.Cleanup(func() {
		poolUpdateAuthorityProjection = originalProjection
		pushPoolAuthorityReplicasAfterUpdate = originalPush
	})
	projected, releaseProjection := make(chan struct{}), make(chan struct{})
	poolUpdateAuthorityProjection = func(store *Store, vms json.RawMessage) (protocol.PoolAuthorityReplica, string, error) {
		replica, digest, err := originalProjection(store, vms)
		close(projected)
		<-releaseProjection
		return replica, digest, err
	}
	pushPoolAuthorityReplicasAfterUpdate = func(context.Context, *Store, time.Duration) error {
		return errors.New("replica acknowledgement unavailable")
	}
	controlDone := make(chan error, 1)
	go func() {
		_, err := lab.c.Control(context.Background(), "capacity-control", request)
		controlDone <- err
	}()
	<-projected
	snapshotDone := make(chan error, 1)
	go func() { snapshotDone <- reserveUpdateCapacitySnapshot(s, snapshotRequest) }()
	var snapshotErr error
	snapshotFinished := false
	select {
	case snapshotErr = <-snapshotDone:
		snapshotFinished = true
	case <-time.After(time.Second):
		// The control has not published its reservation yet. A serialized VM
		// admission waits until that publication rather than using the old budget.
	}
	close(releaseProjection)
	controlErr := <-controlDone
	if !snapshotFinished {
		snapshotErr = <-snapshotDone
	}
	if refusalCode(controlErr) != "replica_pending" {
		t.Fatalf("accepted control did not retain its unresolved receipt: %v", controlErr)
	}
	if snapshotErr == nil || !strings.Contains(snapshotErr.Error(), "limit") {
		t.Fatalf("competing snapshot consumed the accepted control's result capacity: %v", snapshotErr)
	}
	poolUpdateAuthorityProjection = originalProjection
	pushPoolAuthorityReplicasAfterUpdate = func(context.Context, *Store, time.Duration) error { return nil }
	result, err := lab.c.Control(context.Background(), "capacity-control", request)
	if err != nil || result.State != protocol.PoolUpdateAborted {
		t.Fatalf("accepted pending control could not record its terminal outcome: %+v (%v)", result, err)
	}
	replayed, err := lab.c.Control(context.Background(), "capacity-control", request)
	if err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatalf("terminal result did not survive competing snapshot refusal: %+v (%v)", replayed, err)
	}
	replica, _, err := buildPoolAuthorityReplica(s)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = poolAuthorityReplicaFitsAddressedBodies(s, replica, peer.CAKeyPEM); err != nil {
		t.Fatalf("accepted result cannot be replicated: %v", err)
	}
}

func TestControlCapacityRespectsAcceptedSnapshotTerminalHeadroom(t *testing.T) {
	lab, snapshotRequest, request := updateSnapshotCapacityFixture(t, true)
	s := lab.pool.master
	before := lab.rollout()
	if _, err := lab.c.Control(context.Background(), "capacity-control", request); refusalCode(err) != "authority_capacity" {
		t.Fatalf("control consumed an already-accepted snapshot's terminal room: %v", err)
	}
	if !reflect.DeepEqual(before, lab.rollout()) {
		t.Fatal("refused control changed accepted rollout history")
	}
	err := settleUpdateCapacitySnapshot(s, snapshotRequest.VMID, "completed")
	if err != nil {
		t.Fatalf("previously accepted snapshot could not settle after control refusal: %v", err)
	}
	catalog, err := ListPoolVMSnapshots(context.Background(), s, snapshotRequest.VMID)
	if err != nil || len(catalog) != 1 || catalog[0].ID != snapshotRequest.OperationID {
		t.Fatalf("settled snapshot was not visible to its consumer: %+v (%v)", catalog, err)
	}
}

func padUpdateCapacityHistory(t *testing.T, s *Store, history *protocol.PoolUpdateRollout, replica protocol.PoolAuthorityReplica) {
	t.Helper()
	peer, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	// Either tested enlargement fits alone, but there is no unclaimed room for
	// a competing enlargement or an outcome larger than this exact projection.
	low, high := 0, protocol.PoolReplicaLimit
	for low < high {
		mid := low + (high-low+1)/2
		history.Hosts[0].Error = strings.Repeat("x", mid)
		replica.Sections, err = replica.SectionDigests()
		if err != nil {
			t.Fatal(err)
		}
		if poolAuthorityReplicaFitsAddressedBodies(s, replica, peer.CAKeyPEM) == nil {
			low = mid
		} else {
			high = mid - 1
		}
	}
	if low < 8 {
		t.Fatal("capacity fixture has no accepted-history room")
	}
	history.Hosts[0].Error = strings.Repeat("x", low-8)
}

func settleUpdateCapacitySnapshot(s *Store, vmID, outcome string) error {
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	peer, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return err
	}
	record := state.VMs[vmID]
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: *record.PendingRecovery, Outcome: outcome,
		JournalSHA256: strings.Repeat("b", 64), Snapshot: record.PendingSnapshot}
	return settlePoolVMSnapshotStateLocked(s, &state, peer.Membership, record.HostID, evidence)
}

func TestControlCapacityDoesNotSpendPendingSnapshotDeleteSpace(t *testing.T) {
	lab, snapshotRequest, request := updateSnapshotCapacityFixture(t, false)
	s := lab.pool.master
	if err := reserveUpdateCapacitySnapshot(s, snapshotRequest); err != nil {
		t.Fatal(err)
	}
	if err := settleUpdateCapacitySnapshot(s, snapshotRequest.VMID, "completed"); err != nil {
		t.Fatal(err)
	}
	catalog, err := ListPoolVMSnapshots(context.Background(), s, snapshotRequest.VMID)
	if err != nil || len(catalog) != 1 {
		t.Fatalf("initial accepted snapshot missing: %+v (%v)", catalog, err)
	}
	deleteRequest := snapshotRequest
	deleteRequest.OperationID, deleteRequest.RecoveryKind = "capacity-delete", "snapshot-delete"
	deleteRequest.OwnerOperationOrder = 2
	deleteRequest.Snapshot = &protocol.VMSnapshotRequest{SnapshotID: catalog[0].ID, Target: &catalog[0]}
	if err = reserveUpdateCapacitySnapshot(s, deleteRequest); err != nil {
		t.Fatal(err)
	}
	r := lab.rollout()
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := poolUpdateResultReservation(r, membership, request)
	if err != nil {
		t.Fatal(err)
	}
	prospective := *r
	prospective.Controls = append(append([]protocol.PoolUpdateControlResult(nil), r.Controls...),
		protocol.PoolUpdateControlResult{RequestID: "capacity-control", Request: request, ReservedResultBytes: reserved})
	replica, _, err := buildPoolAuthorityReplica(s)
	if err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	replica.VMs = completedUpdateCapacityVMs(t, state)
	replica.UpdateRollout = &prospective
	// A completed deletion would make room, but it has not happened and may
	// report a definitive no-effects failure instead.
	padUpdateCapacityHistory(t, s, r.Controls[0].Rollout, replica)
	if err = savePoolUpdate(s, r); err != nil {
		t.Fatal(err)
	}
	before := lab.rollout()
	pushes := 0
	originalPush := pushPoolAuthorityReplicasAfterUpdate
	pushPoolAuthorityReplicasAfterUpdate = func(context.Context, *Store, time.Duration) error {
		pushes++
		return errors.New("replica acknowledgement unavailable")
	}
	t.Cleanup(func() { pushPoolAuthorityReplicasAfterUpdate = originalPush })
	if _, err = lab.c.Control(context.Background(), "capacity-control", request); refusalCode(err) != "authority_capacity" {
		t.Fatalf("control spent space from an uncompleted snapshot deletion: %v", err)
	}
	if pushes != 0 || !reflect.DeepEqual(before, lab.rollout()) {
		t.Fatal("refused control published history or attempted replica/effect work")
	}
	if err = settleUpdateCapacitySnapshot(s, snapshotRequest.VMID, "failed-before-effects"); err != nil {
		t.Fatalf("accepted deletion could not preserve its no-effects outcome: %v", err)
	}
	after, err := ListPoolVMSnapshots(context.Background(), s, snapshotRequest.VMID)
	if err != nil || !reflect.DeepEqual(after, catalog) {
		t.Fatalf("failed deletion did not retain its original catalog: %+v (%v)", after, err)
	}
}

func completedUpdateCapacityVMs(t *testing.T, state poolVMState) json.RawMessage {
	t.Helper()
	completed := make(map[string]poolVMRecord, len(state.VMs))
	for id, record := range state.VMs {
		if record.PendingRecovery != nil {
			evidence := protocol.PoolVMRecoveryEvidence{Reservation: *record.PendingRecovery, Outcome: "completed",
				JournalSHA256: strings.Repeat("c", 64), Snapshot: record.PendingSnapshot, ResultSpec: record.PendingRecoveryResultSpec}
			if err := settlePoolVMRecovery(&record, record.HostID, evidence); err != nil {
				t.Fatal(err)
			}
		}
		completed[id] = record
	}
	raw, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNewRolloutCapacityDoesNotNetIndependentSnapshotCreateAndDelete(t *testing.T) {
	lab := newUpdateLab(t, 16)
	s := lab.pool.master
	if err := lab.start("previous-capacity-rollout", "refuse"); err != nil {
		t.Fatal(err)
	}
	createSpec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	if err := poolVMLocal(s, createSpec.ID, "define", nil, true, &createSpec, nil); err != nil {
		t.Fatal(err)
	}
	deleteSpec := *poolVMTestSpec("22222222-2222-4222-8222-222222222222", "member-2")
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	deleteRecord := poolVMRecord{PoolID: s.State.PoolID, VMID: deleteSpec.ID, HostID: deleteSpec.HostID,
		Epoch: 1, State: "stopped", Spec: deleteSpec}
	original, err := poolVMRecoveryReservationFor(deleteRecord, "retained-delete-target", "snapshot-create", strings.Repeat("a", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshotDescriptorForTest(original, "retained-point", strings.Repeat("d", 512))
	deleteRecord.Snapshots = []protocol.PoolVMSnapshot{target}
	state.VMs[deleteSpec.ID] = deleteRecord
	if err = savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	createRequest := poolVMRequest{VMID: createSpec.ID, Action: "snapshot-reserve", OperationID: "independent-create",
		VM: &createSpec, Snapshot: &protocol.VMSnapshotRequest{Name: "new-point", Description: strings.Repeat("d", 512)},
		SnapshotCreatedAt: lab.clock, IntentSHA256: strings.Repeat("b", 64), OwnerOperationOrder: 1}
	deleteRequest := poolVMRequest{VMID: deleteSpec.ID, Action: "snapshot-reserve", RecoveryKind: "snapshot-delete",
		OperationID: "independent-delete", VM: &deleteSpec,
		Snapshot:     &protocol.VMSnapshotRequest{SnapshotID: target.ID, Target: &target},
		IntentSHA256: strings.Repeat("c", 64), OwnerOperationOrder: 2}
	if err = reserveUpdateCapacitySnapshot(s, createRequest); err != nil {
		t.Fatal(err)
	}
	if err = reserveUpdateCapacitySnapshot(s, deleteRequest); err != nil {
		t.Fatal(err)
	}
	r := lab.rollout()
	history := *r
	history.Controls = nil
	history.Hosts = append([]protocol.PoolUpdateHost(nil), r.Hosts...)
	r.Controls = []protocol.PoolUpdateControlResult{{RequestID: "retained-mixed-result",
		Request: protocol.PoolUpdateRequest{RolloutID: r.ID, Control: "pause"}, Rollout: &history}}
	next := *r
	next.ID = strings.Repeat("n", 128)
	next.Hosts = append([]protocol.PoolUpdateHost(nil), r.Hosts...)
	r.State, r.Phase, r.Hosts = protocol.PoolUpdateAborted, "finalize", nil
	replica, _, err := buildPoolAuthorityReplica(s)
	if err != nil {
		t.Fatal(err)
	}
	state, err = loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	// The aggregate completed endpoint allows the deletion to cancel creation
	// growth. Real members can finish in the opposite order, or never delete.
	replica.VMs = completedUpdateCapacityVMs(t, state)
	replica.UpdateRollout = &next
	padUpdateCapacityHistory(t, s, &history, replica)
	if err = savePoolUpdate(s, r); err != nil {
		t.Fatal(err)
	}
	if err = poolVMRecoverySettlementHeadroom(s, state); err != nil {
		t.Fatalf("fixture consumed previously accepted snapshot room before new-rollout admission: %v", err)
	}
	before := lab.rollout()
	if _, _, err = lab.c.Start(context.Background(), next.ID, protocol.PoolUpdateRequest{Sequence: 2, RunningVMs: "refuse"}); refusalCode(err) != "authority_capacity" {
		t.Fatalf("new rollout netted one member's possible deletion against another's promised creation: %v", err)
	}
	if !reflect.DeepEqual(before, lab.rollout()) {
		t.Fatal("refused new rollout changed accepted history")
	}
	if err = settleUpdateCapacitySnapshot(s, createSpec.ID, "completed"); err != nil {
		t.Fatalf("create-first settlement lost its promised room: %v", err)
	}
	if err = settleUpdateCapacitySnapshot(s, deleteSpec.ID, "failed-before-effects"); err != nil {
		t.Fatalf("failed deletion could not retain its catalog and receipt: %v", err)
	}
	created, err := ListPoolVMSnapshots(context.Background(), s, createSpec.ID)
	if err != nil || len(created) != 1 || created[0].ID != createRequest.OperationID {
		t.Fatalf("completed creation was not visible: %+v (%v)", created, err)
	}
	retained, err := ListPoolVMSnapshots(context.Background(), s, deleteSpec.ID)
	if err != nil || !reflect.DeepEqual(retained, []protocol.PoolVMSnapshot{target}) {
		t.Fatalf("no-effects deletion failed to retain its catalog: %+v (%v)", retained, err)
	}
	replayed, err := lab.c.Control(context.Background(), r.Controls[0].RequestID, r.Controls[0].Request)
	if err != nil || !reflect.DeepEqual(replayed, &history) {
		t.Fatalf("refused rollout or independent settlements changed an accepted control replay: %+v (%v)", replayed, err)
	}
	replica, _, err = buildPoolAuthorityReplica(s)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = poolAuthorityReplicaFitsAddressedBodies(s, replica, peer.CAKeyPEM); err != nil {
		t.Fatalf("independently settled outcomes cannot be replicated: %v", err)
	}
}
