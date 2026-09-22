// SPDX-License-Identifier: AGPL-3.0-only
package agent

// A watchdog exclusion proof is the one proof in Nex that nobody performs: the target reset
// itself, and the surviving host only establishes that it must have. That makes it the most
// dangerous kind of evidence in the product, so the three conditions of the plan's rule change
// are each measured here and each recorded in the proof.
//
//	(a) The target reported, itself, while it was alive, that it had armed a watchdog that
//	    resets the machine independently of its kernel — and that report is in the durable
//	    record this candidate holds, at the fencing generation being promoted. A software
//	    watchdog never counts: a hung kernel does not run it.
//	(b) This host is in a quorate partition that does not contain the target, read from
//	    Corosync's own votequorum. Nex's own heartbeats are not evidence here: sbd resets a
//	    host because votequorum told it so, so the survivors must read the same votequorum.
//	(c) That state has held continuously, on a monotonic clock, for at least twice the
//	    target's own watchdog timeout. Any flap restarts the clock: the target reappearing,
//	    quorum being lost, or this agent restarting, because a restarted agent cannot know
//	    what happened while it was not looking.
//
// There is no force, no operator override and no "declare host lost". A pool of two hosts has
// no majority side and therefore never produces one of these.

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// The one quorum read, as a package variable so a lab stands in for Corosync.
var localQuorum = vmhost.LocalQuorum

// poolQuorumGap is how long a gap in observation makes continuity unknown. It is derived from
// the shortest bound a proof can ever need, and must stay well inside it: a blind window as
// long as the bound could hide a target leaving, rejoining and leaving again, and the clock
// would run straight through it.
//
// Half the minimum bound, not a quarter: the reading rides its own ten-second tick under a
// twenty-second timeout, so a quarter of the bound left no margin at all over one slow
// corosync-quorumtool, and a reading that is merely late would have flapped every row's
// watchdog method and restarted every clock. Half is still far enough inside the bound that no
// window can hide a full expiry.
var poolQuorumGap = protocol.PoolWatchdogBound(0) / 2

var poolQuorumMu sync.Mutex

// poolQuorumSince is, per state directory and per target host, when this host's uninterrupted
// quorate-without-that-host state began. It is memory only and deliberately so: a restarted
// agent starts every clock from zero, which is the honest answer to "how long has this held?"
// when nothing was watching.
var poolQuorumSince = map[string]map[string]time.Time{}
var poolQuorumSeen = map[string]time.Time{}
var poolQuorumLast = map[string]vmhost.PoolQuorum{}

// poolQuorumRingSince is when this host first saw the Corosync membership it is in now. It is
// what "the cluster has settled" means before a host arms: not that it is quorate at this
// instant, but that the same set of hosts has been the membership for a whole proof bound.
var poolQuorumRingSince = map[string]time.Time{}

// poolWatchdogJoined is, per target, when this host last saw that target *join* its partition,
// and poolWatchdogEvidence is the armed record this host held for that target and when it first
// held it. Together they close the withdrawal window: a host that left, came back unarmed and
// was partitioned again would otherwise be excluded on the strength of a record written before
// it rebooted. A proof therefore needs evidence that is not older than the target's last
// rejoin. Ageing the record out instead would refuse exactly the proofs this feature exists for
// — a legitimately partitioned host cannot refresh its report either.
var poolWatchdogJoined = map[string]map[string]time.Time{}
var poolWatchdogEvidence = map[string]map[string]poolArmedEvidence{}

// poolWatchdogConfirmedRing is the Corosync membership this host last verified its own arming
// on. It is the producing half of the rule above: a host that rejoins re-confirms, once, and
// that confirmation is what lets a survivor act on its armed record again.
var poolWatchdogConfirmedRing = map[string]string{}

type poolArmedEvidence struct {
	record protocol.PoolWatchdogArmed
	at     time.Time
}

// ObservePoolQuorum takes one reading of the pool's Corosync quorum and advances or restarts
// the continuity clocks. It runs on the reconcile tick and changes nothing outside this
// process: a reading is not a decision.
func ObservePoolQuorum(ctx context.Context, s *Store) error {
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	q, qErr := localQuorum(ctx)
	now := time.Now()
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	// Two ways continuity becomes unknown: nobody was looking for long enough that a whole
	// expiry could have hidden in the window, or Corosync itself saw a membership event
	// between two readings, which its ring identifier reports and sampling cannot.
	previous := poolQuorumLast[s.dir]
	// Two different questions, deliberately kept apart.
	//
	// "Blind" is this host not having been looking: no reading for longer than a window a
	// whole expiry could hide in, or a reading whose membership identity cannot be
	// established at all, which would leave the flap detector silently inoperative. Only
	// blindness can hide a host leaving and rejoining between two readings, so only
	// blindness makes every host's arming unconfirmed again.
	//
	// "Gap" adds a Corosync membership event, which this host *did* see. That restarts the
	// continuity clocks and the settle clock, because the pool is not the pool it was — but
	// it must not be read as every host having rejoined, or the pool's own arming rollout
	// would make every previously armed host unprovable for ever.
	blind := poolQuorumSeen[s.dir].IsZero() || now.Sub(poolQuorumSeen[s.dir]) > poolQuorumGap ||
		(q.Observed && q.RingID == "")
	gap := blind || previous.RingID != q.RingID
	if gap {
		poolQuorumRingSince[s.dir] = now
	}
	poolWatchdogObserveMembers(s.dir, membership, previous, q, blind, now)
	poolQuorumSeen[s.dir], poolQuorumLast[s.dir] = now, q
	if qErr != nil || !q.Observed || !q.Quorate {
		// No quorum, or none this host can read. Nothing is excluded by anything, and
		// every clock restarts: a partition this host cannot see is one it cannot claim.
		delete(poolQuorumSince, s.dir)
		return qErr
	}
	inPartition := map[string]bool{}
	for _, host := range q.Hosts {
		inPartition[host] = true
	}
	if gap {
		delete(poolQuorumSince, s.dir)
	}
	since := poolQuorumSince[s.dir]
	if since == nil {
		since = map[string]time.Time{}
		poolQuorumSince[s.dir] = since
	}
	for _, member := range membership.Members {
		if member.Status != "ready" || member.HostID == s.State.HostID {
			continue
		}
		if inPartition[member.HostID] {
			delete(since, member.HostID) // Back in the partition: the clock restarts.
			continue
		}
		if _, running := since[member.HostID]; !running {
			since[member.HostID] = now
		}
	}
	return nil
}

// poolWatchdogObserveMembers records, for every other host, when it last joined this host's
// Corosync partition and when the armed record this host holds about it last changed. Both are
// this host's own clock, so nothing here compares two machines' clocks.
func poolWatchdogObserveMembers(dir string, membership protocol.PoolMembership, previous, q vmhost.PoolQuorum, blind bool, now time.Time) {
	joined, evidence := poolWatchdogJoined[dir], poolWatchdogEvidence[dir]
	if joined == nil {
		joined, evidence = map[string]time.Time{}, map[string]poolArmedEvidence{}
		poolWatchdogJoined[dir], poolWatchdogEvidence[dir] = joined, evidence
	}
	before := map[string]bool{}
	for _, host := range previous.Hosts {
		before[host] = true
	}
	for _, member := range membership.Members {
		if member.WatchdogArmed != nil {
			held := *member.WatchdogArmed
			held.ObservedAt = time.Time{} // The master's stamp is not what changed.
			if current, ok := evidence[member.HostID]; !ok || current.record != held {
				evidence[member.HostID] = poolArmedEvidence{record: held, at: now}
			}
		} else {
			delete(evidence, member.HostID)
		}
		// A host is "joining" when it is in this reading and was not in the last one, and
		// after a blind window, because that is a window a whole rejoin could have hidden
		// in. A membership event this host watched is not a rejoin of the hosts that
		// stayed.
		if slices.Contains(q.Hosts, member.HostID) && (blind || !before[member.HostID]) {
			joined[member.HostID] = now
		}
	}
}

// poolQuorumSettled is whether this host has watched one unchanged Corosync membership, quorate
// and containing every host of the pool's cluster, for a full proof bound. It is the gate in
// front of arming: a host that arms into a cluster that is still forming is inquorate from its
// first moment and resets itself with nothing wrong anywhere.
func poolQuorumSettled(dir string, nodes []vmhost.PoolClusterNode) bool {
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	q, seen := poolQuorumLast[dir], poolQuorumSeen[dir]
	if seen.IsZero() || time.Since(seen) > poolQuorumGap || !q.Observed || !q.Quorate {
		return false
	}
	for _, node := range nodes {
		if !slices.Contains(q.Hosts, node.HostID) {
			return false
		}
	}
	since := poolQuorumRingSince[dir]
	return !since.IsZero() && time.Since(since) >= protocol.PoolWatchdogBound(0)
}

// poolWatchdogRejoined is whether the target has joined this host's partition since the last
// time this host learned anything new about that target's arming. When it has, this host is
// holding a record that predates the target's return and may describe a spell of arming that
// ended at its last reboot, so it refuses rather than proving an exclusion against a host that
// came back unarmed.
func PoolWatchdogRejoined(dir, hostID string) bool {
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	return poolWatchdogRejoined(dir, hostID)
}

func poolWatchdogRejoined(dir, hostID string) bool {
	joined, ok := poolWatchdogJoined[dir][hostID]
	if !ok {
		return false
	}
	evidence, known := poolWatchdogEvidence[dir][hostID]
	return !known || evidence.at.Before(joined)
}

// poolWatchdogHeld is how long this host has continuously been quorate without one host, and
// the partition it is reading, taken fresh rather than from the last tick: a proof is made at
// the moment it is used.
func poolWatchdogHeld(ctx context.Context, s *Store, hostID string) (time.Duration, vmhost.PoolQuorum) {
	_ = ObservePoolQuorum(ctx, s)
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	q := poolQuorumLast[s.dir]
	if !q.Excludes(hostID) {
		return 0, q
	}
	started, ok := poolQuorumSince[s.dir][hostID]
	if !ok {
		return 0, q
	}
	return time.Since(started), q
}

// poolLastQuorum is what this host last read from Corosync, for the reports that are composed
// without running anything. A reading older than the gap above is no reading at all.
func poolLastQuorum(dir string) vmhost.PoolQuorum {
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	if seen := poolQuorumSeen[dir]; seen.IsZero() || time.Since(seen) > poolQuorumGap {
		return vmhost.PoolQuorum{}
	}
	return poolQuorumLast[dir]
}

// ClearPoolQuorumForTest drops what this host has observed, which is what a restart does.
func ClearPoolQuorumForTest(s *Store) {
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	delete(poolQuorumSince, s.dir)
	delete(poolQuorumSeen, s.dir)
	delete(poolQuorumLast, s.dir)
	delete(poolQuorumRingSince, s.dir)
	delete(poolWatchdogJoined, s.dir)
	delete(poolWatchdogEvidence, s.dir)
	delete(poolWatchdogConfirmedRing, s.dir)
}

// poolWatchdogWait is Core's sentence about how much longer this host must stay quorate
// without the target before that target's watchdog has certainly reset it. It reads the last
// observation rather than taking one, because it is composed inside a verdict and a verdict
// runs no new work.
func poolWatchdogWait(s *Store, member protocol.PoolMember) string {
	bound := protocol.PoolWatchdogBound(0)
	if member.WatchdogArmed != nil {
		bound = protocol.PoolWatchdogBound(member.WatchdogArmed.TimeoutSeconds)
	}
	poolQuorumMu.Lock()
	started, ok := poolQuorumSince[s.dir][member.HostID]
	q := poolQuorumLast[s.dir]
	poolQuorumMu.Unlock()
	if !ok || !q.Excludes(member.HostID) {
		return "this host is not yet in a quorate partition without " + member.HostID +
			", so the " + bound.String() + " its watchdog needs has not started"
	}
	if remaining := bound - time.Since(started); remaining > 0 {
		return "its watchdog resets it after " + bound.String() + " out of this pool's quorate partition; " +
			remaining.Round(time.Second).String() + " of that remains"
	}
	return "its watchdog has had the " + bound.String() + " it needs"
}

// PoolWatchdogUsable answers, without measuring anything, whether a watchdog exclusion could
// ever prove this host excluded: the target's own armed report, in the durable record, at the
// generation of that record. It is the watchdog half of poolPromotionFenceReadiness.
//
// The generation comes from the member record itself and is never a parameter. A pool has two
// counters — membership and authority — and handing this the wrong one made readiness and the
// promotion path disagree without anything failing.
func PoolWatchdogUsable(dir string, member protocol.PoolMember) error {
	armed, generation := member.WatchdogArmed, member.Generation
	switch {
	case armed == nil || !armed.Armed:
		return errPoolWatchdog(member.HostID + " never reported that it armed a watchdog")
	case !protocol.PoolWatchdogArmable(armed.Kind) || armed.TimeoutSeconds == 0:
		return errPoolWatchdog(member.HostID + " reports no watchdog that could reset it independently of its kernel")
	case armed.FencingGeneration != generation:
		return errPoolWatchdog(member.HostID + " armed its watchdog under an earlier record of this pool")
	case PoolWatchdogRejoined(dir, member.HostID):
		// The same condition the proof applies, in the same place the operator's verdict
		// is composed. Slice 4b's review found readiness and the proof disagreeing once;
		// a condition that lives in only one of them is how that happens again.
		return errPoolWatchdog(poolWatchdogRejoinedReason(member.HostID))
	}
	return nil
}

// poolWatchdogRejoinedReason is one sentence, used by the readiness verdict and the proof alike.
func poolWatchdogRejoinedReason(hostID string) string {
	return hostID + " rejoined this pool's cluster after the last thing this host learned about its arming, " +
		"so that record may describe a watchdog it stopped feeding when it restarted; this host waits for it to confirm its arming again"
}

type poolWatchdogError string

func (e poolWatchdogError) Error() string { return string(e) }
func errPoolWatchdog(reason string) error { return poolWatchdogError(reason) }

// poolWatchdogFenceVerify produces the proof, or the refusal that says how long remains. It
// powers nothing off: everything it does is read the pool's records and the pool's quorum.
func poolWatchdogFenceVerify(ctx context.Context, s *Store, member protocol.PoolMember, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
	proof := protocol.StorageClusterFenceStatus{
		RequestID: operationID, HostID: member.HostID, Status: "unverified",
		Method: protocol.PoolFenceMethodWatchdogProof, ConfigGeneration: generation,
		ConfigDigest: digest, ObservedAt: time.Now().UTC(),
	}
	if err := PoolWatchdogUsable(s.dir, member); err != nil {
		proof.Status, proof.Error = "failed", err.Error()
		return proof, err
	}
	armed := member.WatchdogArmed
	bound := protocol.PoolWatchdogBound(armed.TimeoutSeconds)
	held, q := poolWatchdogHeld(ctx, s, member.HostID)
	proof.Watchdog = &protocol.PoolWatchdogExclusion{
		Kind: armed.Kind, TimeoutSeconds: armed.TimeoutSeconds, ArmedGeneration: member.Generation,
		HeldSeconds: int(held / time.Second), BoundSeconds: int(bound / time.Second), Partition: q.Hosts,
	}
	switch {
	case !q.Observed:
		proof.Error = "this host cannot read this pool's quorum, so it cannot establish that " + member.HostID + " lost it"
	case !q.Quorate:
		proof.Error = "this host is not in this pool's quorate partition, so it may not conclude that " + member.HostID + " has been shut out"
	case !q.Excludes(member.HostID):
		proof.Error = "this pool's quorate partition still contains " + member.HostID
	case len(q.Hosts) < 2:
		proof.Error = "a watchdog exclusion needs a majority partition, which needs three or more hosts"
	case held < bound:
		proof.Error = member.HostID + " has been out of this pool's quorate partition for " +
			strconv.Itoa(int(held/time.Second)) + "s of the " + bound.String() +
			" its watchdog needs; wait " + (bound - held).Round(time.Second).String() + " and try again"
	default:
		proof.Status, proof.ExclusionVerified = "passed", true
		return proof, nil
	}
	return proof, errPoolWatchdog(proof.Error)
}
