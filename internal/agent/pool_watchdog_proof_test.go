// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// labHeld backdates how long this host has been quorate without one target, which is the one
// thing a test cannot wait out: the bound is a minute and the clock is monotonic on purpose.
func labHeld(s *Store, hostID string, held time.Duration) {
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	if poolQuorumSince[s.dir] == nil {
		poolQuorumSince[s.dir] = map[string]time.Time{}
	}
	poolQuorumSince[s.dir][hostID] = time.Now().Add(-held)
}

// labProof asks one host to prove that another has shut itself out, exactly as a promotion
// does: the same member record, the same operation and the same generation binding.
func labProof(t *testing.T, prover *Store, member protocol.PoolMember, generation uint64) (protocol.StorageClusterFenceStatus, error) {
	t.Helper()
	digest := protocol.PoolPromotionFenceDigest("pool-1", member.HostID, "op-1", generation)
	return poolWatchdogFenceVerify(context.Background(), prover, member, "op-1", digest, generation)
}

// The whole rule change, in one test: a proof holds only when the target said it armed, this
// host is in a quorate partition without it, and that has held for at least twice the
// target's own timeout. Each condition is removed in turn and the proof must be refused.
func TestAWatchdogProofNeedsAllThreeConditions(t *testing.T) {
	lab, _ := labArmedPool(t)
	prover, master := lab.host("member-1"), lab.host("master-1")
	generation := peerState(t, master).Membership.Generation
	target := memberRow(t, master, "master-1")
	bound := protocol.PoolWatchdogBound(target.WatchdogArmed.TimeoutSeconds)

	// The quorate partition this host reads must exclude the target.
	labQuorum(t, true, "member-1", "member-2")
	if err := ObservePoolQuorum(context.Background(), prover); err != nil {
		t.Fatal(err)
	}

	// Before the bound: refused, and the refusal says how long remains.
	labHeld(prover, "master-1", bound/2)
	proof, err := labProof(t, prover, target, generation)
	if err == nil || proof.ExclusionVerified {
		t.Fatalf("a proof was granted before the bound: %#v", proof)
	}
	if !strings.Contains(err.Error(), "wait ") {
		t.Fatalf("the refusal before the bound does not say how long remains: %v", err)
	}
	if err = protocol.PoolPromotionFenceVerified(&proof, target, "pool-1", "master-1", "op-1", generation, time.Now().UTC()); err == nil {
		t.Fatal("an unverified watchdog proof was accepted as evidence")
	}

	// After the bound: granted, and it is evidence.
	labHeld(prover, "master-1", bound+time.Second)
	if proof, err = labProof(t, prover, target, generation); err != nil || !proof.ExclusionVerified {
		t.Fatalf("a proof was refused after the bound: %#v %v", proof, err)
	}
	if proof.Method != protocol.PoolFenceMethodWatchdogProof || proof.Watchdog == nil ||
		proof.Watchdog.HeldSeconds < proof.Watchdog.BoundSeconds || len(proof.Watchdog.Partition) != 2 {
		t.Fatalf("the proof does not record its own evidence: %#v", proof.Watchdog)
	}
	if err = protocol.PoolPromotionFenceVerified(&proof, target, "pool-1", "master-1", "op-1", generation, time.Now().UTC()); err != nil {
		t.Fatalf("a complete watchdog proof was not accepted: %v", err)
	}

	// A proof for another promotion, another host or another generation is refused exactly
	// as a fence-device proof is.
	for name, broken := range map[string]func(*protocol.StorageClusterFenceStatus){
		"another operation":  func(p *protocol.StorageClusterFenceStatus) { p.RequestID = "op-2" },
		"another host":       func(p *protocol.StorageClusterFenceStatus) { p.HostID = "member-2" },
		"another generation": func(p *protocol.StorageClusterFenceStatus) { p.ConfigGeneration = generation + 1 },
		"a claim it held longer than it did": func(p *protocol.StorageClusterFenceStatus) {
			p.Watchdog.BoundSeconds = 1
		},
		"a partition that contains the target": func(p *protocol.StorageClusterFenceStatus) {
			p.Watchdog.Partition = append(p.Watchdog.Partition, "master-1")
		},
		"a partition of one host": func(p *protocol.StorageClusterFenceStatus) {
			p.Watchdog.Partition = []string{"member-1"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := proof
			watchdog := *proof.Watchdog
			value.Watchdog = &watchdog
			broken(&value)
			if protocol.PoolPromotionFenceVerified(&value, target, "pool-1", "master-1", "op-1", generation, time.Now().UTC()) == nil {
				t.Fatal("this was accepted as a watchdog exclusion proof")
			}
		})
	}
}

// Three refusals that are about the target rather than about the wait: a host that never
// armed, one whose watchdog is only the kernel's, and one whose armed report is from an
// earlier record of the pool.
func TestAWatchdogProofIsRefusedForATargetThatCouldNotHaveReset(t *testing.T) {
	lab, _ := labArmedPool(t)
	prover, master := lab.host("member-1"), lab.host("master-1")
	generation := peerState(t, master).Membership.Generation
	labQuorum(t, true, "member-1", "member-2")
	_ = ObservePoolQuorum(context.Background(), prover)
	labHeld(prover, "master-1", 2*time.Hour)

	target := memberRow(t, master, "master-1")
	for name, row := range map[string]*protocol.PoolWatchdogArmed{
		"a host that never reported arming": nil,
		"a host that reported it disarmed":  {Armed: false},
		"a software watchdog": {Armed: true, Kind: protocol.PoolWatchdogSoftware,
			TimeoutSeconds: 60, FencingGeneration: generation},
		"an armed report from an earlier record of the pool": {Armed: true, Kind: protocol.PoolWatchdogHardware,
			TimeoutSeconds: 30, FencingGeneration: generation - 1},
	} {
		t.Run(name, func(t *testing.T) {
			subject := target
			subject.WatchdogArmed = row
			proof, err := labProof(t, prover, subject, generation)
			if err == nil || proof.ExclusionVerified {
				t.Fatalf("%s produced a proof: %#v", name, proof)
			}
			if protocol.PoolPromotionFenceVerified(&proof, subject, "pool-1", "master-1", "op-1", generation, time.Now().UTC()) == nil {
				t.Fatal("it was accepted as evidence anyway")
			}
		})
	}
}

// The continuity clock is the third condition, so everything that makes continuity unknown
// must restart it: the target reappearing, the quorum being lost, and this agent restarting.
func TestEveryFlapRestartsTheContinuityClock(t *testing.T) {
	lab, _ := labArmedPool(t)
	prover, master := lab.host("member-1"), lab.host("master-1")
	generation := peerState(t, master).Membership.Generation
	target := memberRow(t, master, "master-1")
	bound := protocol.PoolWatchdogBound(target.WatchdogArmed.TimeoutSeconds)

	granted := func(step string) bool {
		t.Helper()
		proof, err := labProof(t, prover, target, generation)
		t.Logf("%s: %v", step, err)
		return err == nil && proof.ExclusionVerified
	}
	partitioned := func(held time.Duration) {
		labQuorum(t, true, "member-1", "member-2")
		_ = ObservePoolQuorum(context.Background(), prover)
		labHeld(prover, "master-1", held)
	}

	partitioned(bound + time.Second)
	if !granted("a held partition") {
		t.Fatal("a partition held past the bound produced no proof")
	}

	// The target reappears: whatever it was doing, it was not resetting, so the clock
	// starts again from zero.
	labQuorum(t, true, "master-1", "member-1", "member-2")
	if err := ObservePoolQuorum(context.Background(), prover); err != nil {
		t.Fatal(err)
	}
	if granted("the target back in the partition") {
		t.Fatal("a proof survived the target reappearing")
	}
	labQuorum(t, true, "member-1", "member-2")
	_ = ObservePoolQuorum(context.Background(), prover)
	if granted("immediately after the target left again") {
		t.Fatal("the clock was not restarted by the target reappearing")
	}

	// Quorum lost: this host may not conclude anything about anybody.
	partitioned(bound + time.Second)
	labQuorum(t, false)
	_ = ObservePoolQuorum(context.Background(), prover)
	if granted("without quorum") {
		t.Fatal("a proof was granted from a host that is not in the majority")
	}
	// A rejoin is more than a restarted clock: the record this host holds was written before
	// the target came back, and a host that comes back comes back unarmed until the pool
	// arms it again. So the bound elapsing is not enough on its own — the pool has to have
	// confirmed the arming since the rejoin.
	partitioned(bound + time.Second)
	if granted("the bound elapsed but the arming was never confirmed after the rejoin") {
		t.Fatal("a proof rested on a record written before the target rejoined")
	}
	labReconfirmArming(t, lab, "master-1")
	partitioned(bound + time.Second)
	if !granted("after quorum returned, the arming was confirmed and the bound elapsed again") {
		t.Fatal("a held partition after quorum returned produced no proof")
	}

	// This agent restarting: nothing was watching, so continuity is unknown and starts
	// again from zero rather than being assumed.
	ClearPoolQuorumForTest(prover)
	labQuorum(t, true, "member-1", "member-2")
	_ = ObservePoolQuorum(context.Background(), prover)
	if granted("after an agent restart") {
		t.Fatal("a restarted agent claimed continuity it could not have observed")
	}
}

// Two hosts have no majority side, so no watchdog proof is possible there at all — the
// owner's decision 3, enforced where it matters rather than only described.
func TestATwoHostPoolNeverProducesAWatchdogProof(t *testing.T) {
	lab, _ := labArmedPool(t)
	prover, master := lab.host("member-1"), lab.host("master-1")
	generation := peerState(t, master).Membership.Generation
	target := memberRow(t, master, "master-1")
	// Two hosts: the survivor is alone in its partition, which is never a majority.
	labQuorum(t, true, "member-1")
	_ = ObservePoolQuorum(context.Background(), prover)
	labHeld(prover, "master-1", 2*time.Hour)
	proof, err := labProof(t, prover, target, generation)
	if err == nil || proof.ExclusionVerified || !strings.Contains(err.Error(), "majority") {
		t.Fatalf("a two-host pool produced a watchdog proof: %#v %v", proof, err)
	}
}

// The proof and the readiness must agree about which host they are talking about, which means
// agreeing about which generation counter stamps an armed report. A pool has two — the
// membership generation and the authority generation — and they are structurally independent,
// so a test that hands the proof the number the arming path wrote proves nothing about the
// promotion path, which passes a different one.
//
// This test therefore drives the real consumers: the verdict an operator is shown
// (poolPromotionFenceReadiness, through the preflight) and the fence step that would exclude
// the host (fencePoolExcludedHosts). If they ever disagree with readiness again, a pool reads
// "Watchdog · armed" and refuses to promote at the one moment it matters.
// (Reported by independent review of slice 4b.)
func TestTheWatchdogProofAndReadinessAgreeThroughTheRealPromotionPath(t *testing.T) {
	lab := labArmedPromotionPool(t)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	// The master's own fence device is deliberately withdrawn, so the watchdog is the only
	// method left and the promotion has to use it.
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-clear", "master-1", "master-1", nil)); result.Status != "succeeded" {
		t.Fatalf("clearing the master's fence device = %#v", result)
	}
	replica := labPromotionReplica(t, lab, candidate)

	// Readiness, which is what the operator is shown.
	if method := watchdogMethod(t, master, "master-1"); method.State != protocol.PoolFenceMethodReady {
		t.Fatalf("the master is not watchdog-ready, so this test cannot compare: %#v", method)
	}
	// The same host, through the code a promotion actually runs.
	recorded, usable, kind, reason := poolPromotionFenceReadiness(candidate, peerState(t, candidate), replica,
		mustMember(t, replica.Membership, "master-1"))
	if !recorded || !usable || kind != protocol.PoolFenceWatchdog {
		t.Fatalf("readiness says the master can be shut out by its watchdog, the promotion path says recorded=%v usable=%v kind=%q: %s",
			recorded, usable, kind, reason)
	}

	// And the fence step itself, which is what produces the proof.
	record := labWatchdogFence(t, lab, candidate, "op-1")
	proof := poolPromotionProofFor(record.Fences, "master-1")
	if proof == nil || proof.Method != protocol.PoolFenceMethodWatchdogProof || !proof.ExclusionVerified {
		t.Fatalf("no watchdog exclusion proof was produced: %#v", proof)
	}

	// And the activate stage, which re-proves the whole verdict before adopting the
	// authority. A watchdog proof has to count as the old master's exclusion there too, or
	// a watchdog-only promotion sticks at "activate" and the pool is left with no master.
	// (Reported by independent review of slice 4c.)
	if err := activatePromotedPoolAuthority(context.Background(), candidate, record, 5*time.Second); err != nil {
		t.Fatalf("the watchdog exclusion proof did not prove the promotion: %v", err)
	}
	if record.Stage == "fence" {
		t.Fatal("the activate stage went back to fencing instead of adopting the authority")
	}
	if got := peerState(t, candidate).Membership.MasterHostID; got != "member-1" {
		t.Fatalf("the promoted candidate is not this pool's master: %s", got)
	}
}

// Readiness and execution must agree about the *method* too. A master can have a fence profile
// that is perfectly valid and still be unfenceable by this candidate, because the sealed
// material never reached its replica. Readiness falls back to the master's own watchdog and
// says the promotion can proceed, so the promotion has to follow it there instead of failing
// on the device path and leaving the pool with a dead master it was told it could replace.
// (Reported by independent review of slice 4c.)
func TestAPromotionFencesByTheMethodReadinessPromised(t *testing.T) {
	lab := labArmedPromotionPool(t)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	// The profile stays recorded and usable; only the material behind it is gone, so the
	// replica push carries no fence credential for the old master.
	if err := os.RemoveAll(filepath.Join(master.dir, "storage-cluster", "fence-credentials")); err != nil {
		t.Fatal(err)
	}
	replica := labPromotionReplica(t, lab, candidate)
	oldMaster := mustMember(t, replica.Membership, "master-1")
	if vmhost.PoolFenceUsable(oldMaster.Fence, oldMaster.SystemUUID) != nil {
		t.Fatalf("this test needs the old master's fence profile to stay usable: %#v", oldMaster.Fence)
	}
	recorded, usable, kind, reason := poolPromotionFenceReadiness(candidate, peerState(t, candidate), replica, oldMaster)
	if !recorded || !usable || kind != protocol.PoolFenceWatchdog {
		t.Fatalf("readiness did not fall back to the watchdog: recorded=%v usable=%v kind=%q: %s",
			recorded, usable, kind, reason)
	}

	record := labWatchdogFence(t, lab, candidate, "op-credential-gone")
	proof := poolPromotionProofFor(record.Fences, "master-1")
	if proof == nil || proof.Method != protocol.PoolFenceMethodWatchdogProof || !proof.ExclusionVerified {
		t.Fatalf("the promotion did not exclude the master the way readiness promised: %#v", proof)
	}
}

// labArmedPromotionPool is a three-host pool whose master has a recorded fence profile and
// whose every host has armed a watchdog and reported it: both exclusion methods exist, so a
// test can withdraw one and watch what the promotion does with the other.
func labArmedPromotionPool(t *testing.T) *transferLab {
	t.Helper()
	lab := promotionLab(t, 2)
	watchdogs := map[string]protocol.PoolHostWatchdog{}
	for _, host := range lab.hosts {
		watchdogs[host.State.HostID] = protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogHardware,
			Device: "watchdog-" + host.State.HostID, Driver: "iTCO_wdt", TimeoutSeconds: 30}
	}
	labWatchdogs(t, watchdogs)
	labQuorum(t, true, "master-1", "member-1", "member-2")
	arming := newLabArming(t)
	syncPoolMembershipForTest(t, lab)
	for _, host := range lab.hosts {
		if err := ReportPoolWatchdog(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	for range lab.hosts {
		labSettledCluster(t, lab)
		for _, host := range lab.hosts {
			arming.reconcile(t, host)
		}
		syncPoolMembershipForTest(t, lab)
	}
	return lab
}

// labPromotionReplica publishes the pool's records — which moves the authority generation away
// from the membership generation, exactly what a settled, live pool does — and returns the
// replica the candidate would be promoted from.
func labPromotionReplica(t *testing.T, lab *transferLab, candidate *Store) protocol.PoolAuthorityReplica {
	t.Helper()
	pushReplicas(t, lab.host("master-1"))
	pushReplicas(t, lab.host("master-1"))
	for _, host := range lab.hosts[1:] {
		ConvergePoolAuthority(context.Background(), host, 5*time.Second)
	}
	stored, err := loadPoolReplica(candidate)
	if err != nil || stored == nil {
		t.Fatalf("the candidate holds no replica: %v", err)
	}
	if stored.Replica.Generation == stored.Replica.Membership.Generation {
		t.Skip("this pool's two generation counters coincide, so this test proves nothing")
	}
	for _, host := range lab.hosts {
		if err = ObservePoolQuorum(context.Background(), host); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ClearPoolQuorumForTest(host) })
	}
	return stored.Replica
}

// labWatchdogFence is the destructive step of a promotion the survivors have waited out: the
// master is gone, the two of them have held the quorum without it for longer than its bound,
// and the candidate runs the fence stage.
func labWatchdogFence(t *testing.T, lab *transferLab, candidate *Store, operationID string) *poolTransferRecord {
	t.Helper()
	labQuorum(t, true, "member-1", "member-2")
	if err := ObservePoolQuorum(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	labHeld(candidate, "master-1", 2*time.Hour)
	record := &poolTransferRecord{OperationID: operationID, Action: "promote", Stage: "fence",
		FenceAgreed: []string{"master-1"}}
	lab.powerOff("master-1")
	if err := fencePoolExcludedHosts(context.Background(), candidate, record); err != nil {
		t.Fatalf("the promotion could not exclude the old master: %v", err)
	}
	return record
}

func mustMember(t *testing.T, membership protocol.PoolMembership, hostID string) protocol.PoolMember {
	t.Helper()
	member, ok := poolMemberByID(membership, hostID)
	if !ok {
		t.Fatalf("no member %s", hostID)
	}
	return member
}

// A flap that nobody observed is still a flap. The continuity clock may only run across
// windows short enough that a full expiry could not have hidden inside one, and Corosync's own
// ring identifier — which changes on every membership event — must restart it outright,
// because no sampling interval can see a target leave and rejoin between two readings.
// (Reported by independent review of slice 4b.)
func TestAnUnobservedFlapRestartsTheContinuityClock(t *testing.T) {
	lab, _ := labArmedPool(t)
	prover, master := lab.host("member-1"), lab.host("master-1")
	target := memberRow(t, master, "master-1")

	// The gap that is tolerated must be shorter than the shortest bound a proof can need,
	// or a blind window could hide the whole expiry and a rejoin inside it.
	if poolQuorumGap >= protocol.PoolWatchdogBound(0) {
		t.Fatalf("an observation gap of %s is not shorter than the minimum bound %s",
			poolQuorumGap, protocol.PoolWatchdogBound(0))
	}

	granted := func() bool {
		proof, err := labProof(t, prover, target, target.Generation)
		return err == nil && proof.ExclusionVerified
	}
	partitioned := func(ring string, held time.Duration) {
		labQuorumRing(t, ring, "member-1", "member-2")
		_ = ObservePoolQuorum(context.Background(), prover)
		labHeld(prover, "master-1", held)
	}

	partitioned("1.a0", protocol.PoolWatchdogBound(30)+time.Second)
	if !granted() {
		t.Fatal("a held partition past the bound produced no proof")
	}

	// A gap longer than the tolerated window: nobody was watching, so continuity is unknown.
	labStaleObservation(prover, poolQuorumGap+time.Second)
	labQuorumRing(t, "1.a0", "member-1", "member-2")
	_ = ObservePoolQuorum(context.Background(), prover)
	if granted() {
		t.Fatal("the clock survived a window long enough to hide a whole expiry")
	}

	// And a ring change with the target still absent: Corosync saw a membership event this
	// host's sampling did not, so the clock restarts whatever the readings looked like.
	partitioned("1.a0", protocol.PoolWatchdogBound(30)+time.Second)
	if !granted() {
		t.Fatal("a held partition past the bound produced no proof")
	}
	// The target is still absent and the readings are still prompt, but Corosync reports a
	// new ring: something joined or left while this host was between readings.
	labQuorumRing(t, "1.ac", "member-1", "member-2")
	_ = ObservePoolQuorum(context.Background(), prover)
	if granted() {
		t.Fatal("the clock survived a Corosync membership change")
	}
}
