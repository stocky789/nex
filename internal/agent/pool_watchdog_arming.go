// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Arming is a host acting on its own machine. It reads the pool's decision from the records
// it already holds — the Host fencing setting and its own membership row — and needs no
// command of its own, because the setting is the only decision and it already replicates.
//
// The rules a host must never break, and how each is kept here:
//
//   - A host must never reset because of arming, disarming, an upgrade, a join, an eject, a
//     master change, an agent restart or a Controller outage. sbd feeds the device, not this
//     agent, so this agent restarting or being killed starves nothing; arming is idempotent,
//     so a reconcile pass every few minutes neither rewrites a file nor restarts a live sbd;
//     and disarming is a clean stop that closes the device with the kernel's magic character.
//   - A host arms only when the pool's own prerequisites hold. Falling to two hosts, or the
//     setting being turned off, disarms.
//   - Nothing here restarts Corosync. A host whose Corosync is owned by a clustered-storage
//     cluster is armed alongside it — sbd is a separate service — and a host with no Corosync
//     at all is not armed and says so, rather than being armed against a quorum nobody keeps.
//   - Where the kernel was built with nowayout, a clean disarm is impossible. Nex Core detects
//     that and reports it rather than resetting the host to get out of it.

import (
	"context"
	"errors"
	"slices"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// The three host-side actions, as package variables so a lab stands in for sbd the way
// localWatchdog stands in for a machine's sysfs.
var (
	armLocalWatchdog    = vmhost.ArmLocalWatchdog
	disarmLocalWatchdog = vmhost.DisarmLocalWatchdog
	localWatchdogArmed  = vmhost.LocalWatchdogArmed
)

// ReconcilePoolWatchdogArming brings this host's watchdog to the state the pool's records
// call for, and reports what it actually did. It is the whole arming lifecycle: there is no
// command, no job and nothing for a Controller to stay alive for.
func ReconcilePoolWatchdogArming(ctx context.Context, s *Store) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		return err
	}
	self, ok := poolMemberByID(state.Membership, s.State.HostID)
	if !ok || self.Status != "ready" {
		return nil
	}
	watchdog := localWatchdog(s)
	armed, armedErr := localWatchdogArmed(ctx)
	if armedErr != nil {
		// Nothing is known about sbd, so nothing is changed and nothing is claimed.
		return reportPoolWatchdogArmed(ctx, s, protocol.PoolWatchdogArmed{
			Detail: "Nex Core could not read whether this host's watchdog is armed"})
	}
	want, turn, why := poolWatchdogArmingIntent(ctx, s, state.Membership, watchdog, armed)
	// The generation is the membership's, the counter that moves when the set of hosts that
	// makes a majority changes. It is read back off the member record everywhere it is
	// checked, so the two can never be different numbers.
	report := protocol.PoolWatchdogArmed{Armed: armed, Feeding: armed, Kind: watchdog.Kind,
		Device: watchdog.Device, TimeoutSeconds: vmhost.SBDWatchdogTimeout,
		FencingGeneration: self.Generation, Detail: why}
	switch {
	case want:
		// Arming and re-verifying are the same call, deliberately: "armed" has to mean
		// "armed and still verified". ArmLocalWatchdog re-checks Pacemaker, the cluster's
		// policy and sbd's own configuration every pass, and its verdict decides the
		// report — a sentence in Detail that nothing reads would let a host keep claiming
		// a protection that stopped existing.
		if err = armLocalWatchdog(ctx, watchdog); err != nil {
			if !errors.Is(err, vmhost.ErrWatchdogArmingPending) || !turn {
				report.Armed, report.Detail = false, boundPoolPeerText(200, "this host could not arm its watchdog", err.Error())
				break
			}
			// sbd is enabled and can only join at the cluster's next start, and this
			// host holds the pool's one arming turn: every other host is quorate
			// without it, the pool's cluster runs no resources, and only one host
			// takes this step at a time. That is what makes restarting it safe, and
			// each of those is checked rather than assumed.
			if err = restartPoolCluster(ctx); err != nil {
				report.Armed, report.Detail = false, boundPoolPeerText(200, "this host could not restart its cluster services to arm its watchdog", err.Error())
				break
			}
		}
		// Read back rather than assume: sbd starts with the cluster, so a host whose
		// cluster was already up is enabled and pending, not armed.
		if report.Armed, err = localWatchdogArmed(ctx); err != nil || !report.Armed {
			report.Armed, report.Detail = false, vmhost.ErrWatchdogArmingPending.Error()
			break
		}
		report.Detail = ""
		report.ArmedAt = poolWatchdogArmedAt(s, self.WatchdogArmed, report)
	default:
		// Not wanted. The record goes first, but only for a host that is actually feeding
		// a device: a host must never stop feeding its watchdog while the pool's durable
		// record still says it is armed, because a candidate holding that record would
		// prove an exclusion against a host that disarmed minutes ago and is still running
		// its VMs. If that record cannot be written — the master is unreachable, which is
		// exactly the state a proof gets used in — such a host stays armed and says so.
		//
		// A host that is NOT feeding anything is the opposite case and must not be caught
		// by the same rule. It is the state a host boots into, and it is precisely when
		// the master is unreachable — a site power cut brings a pool up in some order —
		// that sbd must be disabled before this host's cluster starts. Nothing about that
		// can rest on a network call succeeding.
		withdrawn := poolWatchdogNotArmed(report, why)
		reportErr := reportPoolWatchdogArmed(ctx, s, withdrawn)
		feeding := armed
		if reportErr != nil && armed {
			report.Detail = boundPoolPeerText(200, "this host stays armed until the pool records that it is disarming", reportErr.Error())
			return reportPoolWatchdogArmed(ctx, s, report)
		}
		if err = disarmLocalWatchdog(ctx, watchdog); err != nil {
			// The record already says not armed, so no proof can rest on this host;
			// the reason is what the operator acts on.
			withdrawn = poolWatchdogNotArmed(report, boundPoolPeerText(200, "this host could not stop feeding its watchdog", err.Error()))
		}
		if errors.Is(err, vmhost.ErrWatchdogDisarmPending) && turn {
			// sbd is disabled and this host is still feeding the device, which it can
			// only put down when its cluster next stops — and it holds the pool's one
			// teardown turn, so every other host of the cluster is up and quorate
			// without it. Restarting its own cluster services is how it stops feeding
			// without anybody losing a quorum they are being watched against. Leaving
			// the cluster outright is what must not happen while a peer is still
			// armed, and ReconcilePoolCluster keeps everyone in until nobody is.
			if err = restartPoolCluster(ctx); err != nil {
				withdrawn = poolWatchdogNotArmed(report, boundPoolPeerText(200, "this host could not restart its cluster services to stop feeding its watchdog", err.Error()))
			} else if feeding, err = localWatchdogArmed(ctx); err != nil {
				feeding = true
			} else if !feeding {
				withdrawn = poolWatchdogNotArmed(report, why)
			}
		}
		withdrawn.Feeding = feeding
		if reportErr != nil {
			return reportErr
		}
		return reportPoolWatchdogArmed(ctx, s, withdrawn)
	}
	if !report.Armed {
		report = poolWatchdogNotArmed(report, report.Detail)
	}
	return reportPoolWatchdogArmed(ctx, s, report)
}

// poolWatchdogNotArmed is the one shape an unarmed report may take. It carries no device
// class, timeout or generation: there is nothing armed for a proof to have been measured
// against, and leaving them would let a stale record read as evidence.
func poolWatchdogNotArmed(report protocol.PoolWatchdogArmed, detail string) protocol.PoolWatchdogArmed {
	report.Armed, report.Detail = false, detail
	report.Kind, report.Device, report.TimeoutSeconds, report.FencingGeneration = "", "", 0, 0
	report.ArmedAt = time.Time{}
	return report
}

// poolWatchdogArmingIntent is the pool's decision for this host: whether it should be feeding a
// watchdog, whether it holds the pool's one arming turn, and Core's sentence for why it is not
// armed. It is evaluated on the host that would be reset, from records that host already holds,
// deliberately: a host must be able to decide *not* to arm itself when the master is
// unreachable, and the master is exactly the host that may have gone.
//
// The order of the gates is the order of the hazards. A host that is already armed passes the
// last two unconditionally: losing sight of the pool's quorum is not a reason to stop feeding a
// watchdog — it is the reason the watchdog exists — and the turn has moved on to somebody else.
func poolWatchdogArmingIntent(ctx context.Context, s *Store, membership protocol.PoolMembership, w protocol.PoolHostWatchdog, armed bool) (want, turn bool, why string) {
	nodes, wanted, detail := poolClusterIntent(s, membership)
	if !wanted {
		return false, poolWatchdogTeardownTurn(ctx, s, membership, nodes, armed), detail
	}
	if len(nodes) == 2 {
		// Two-host qdevice loss has not been tested with one host already down. The current
		// cluster policy therefore keeps the remaining host running on quorum loss, and
		// Core withholds SBD arming and witness exclusion until that failure case and the
		// product's no-VM-stop promise are reconciled.
		return false, false, "two-host watchdog fencing is withheld pending native testing of witness loss with one host already down"
	}
	if !poolClusterContains(nodes, s.State.HostID) {
		return false, poolWatchdogTeardownTurn(ctx, s, membership, nodes, armed),
			"this host is leaving this pool's cluster"
	}
	if !protocol.PoolWatchdogArmable(w.Kind) {
		return false, false, "this host has no watchdog that could reset it independently of its kernel"
	}
	if w.InUse && !armed {
		// One device, one owner. Something else is already feeding it, and taking it
		// would make two owners of one timer. An armed host is excluded deliberately:
		// the kernel reports a device as in use while it is open, so this host's own sbd
		// is exactly what it would be reading here.
		return false, false, "another program on this host already holds its watchdog device"
	}
	if self, ok := poolMemberByID(membership, s.State.HostID); ok && self.Maintenance {
		// Planned work is about to happen to this host, which is exactly what must not
		// partition a host that can reset itself. It stays in the cluster until its device
		// is down, then leaves.
		return false, poolWatchdogTeardownTurn(ctx, s, membership, nodes, armed),
			"this host is in maintenance, so Nex Core is putting its watchdog down before any work starts"
	}
	if armed {
		return true, false, ""
	}
	// Nothing arms until this host has watched the whole pool hold one Corosync membership
	// for a full proof bound. A host that armed into a cluster that was still forming would
	// be inquorate from its first moment, and would reset itself with nothing wrong.
	if !poolQuorumSettled(s.dir, nodes) {
		return false, false, "this host is waiting for this pool's cluster to be quorate with every host in it before it arms"
	}
	if next := poolWatchdogArmingTurn(nodes, membership); next != s.State.HostID {
		// Arming restarts this host's cluster services, so exactly one host in the pool
		// may be doing it at any moment. Whose turn it is comes from the membership, so
		// two hosts cannot both believe it is theirs.
		return false, false, "this pool is arming its hosts one at a time and it is " + next + "'s turn"
	}
	return true, true, ""
}

// poolWatchdogTeardownTurn is whether this host may take the one cluster restart the pool allows
// right now in order to stop feeding its watchdog. It is the arming turn's mirror, and it exists
// for the same reason: a host puts its device down by restarting its own cluster services, and
// two hosts doing that at once is a pool that has lost its majority for nothing.
//
// The order is the same host-ID order, over the hosts that are armed and should not be, so every
// host derives the same answer from the record it holds. A host only takes its turn while it can
// see the whole cluster quorate without counting on itself, because that is what makes its own
// departure survivable for the others.
func poolWatchdogTeardownTurn(ctx context.Context, s *Store, membership protocol.PoolMembership, nodes []vmhost.PoolClusterNode, armed bool) bool {
	if !armed || !poolClusterTeardownSafe(s, membership, nodes) {
		return false
	}
	if poolClusterBoundSet(ctx) {
		// The quorate side's bound is still on this cluster. Starting a cluster with it set
		// and no sbd on this node is answered by Pacemaker shutting the cluster down on
		// every node, which takes the quorum away from the peers that are still feeding —
		// measured in the nested lab as a reset of one of them. The master takes the bound
		// off first; this waits for that to have happened rather than racing it.
		return false
	}
	for _, node := range nodes {
		member, ok := poolMemberByID(membership, node.HostID)
		if !ok || !poolWatchdogFeeding(member) {
			continue
		}
		if !poolWatchdogShouldDisarm(s, membership, nodes, member) {
			continue
		}
		return node.HostID == s.State.HostID
	}
	return false
}

// poolClusterTeardownSafe is whether this host's cluster services can be restarted without taking
// a peer that is still feeding a watchdog below quorum. A host that has already stopped feeding
// cannot be reset by losing quorum, so the arithmetic is only about the ones that still are: after
// this host leaves, every still-feeding peer must remain in a majority of the nodelist.
//
// It is deliberately not "the cluster is whole". A host lost in the middle of a teardown would
// otherwise stall it for ever with the rest still feeding, which is worse: the pool's record says
// they are not armed, and one more loss resets the survivor.
func poolClusterTeardownSafe(s *Store, membership protocol.PoolMembership, nodes []vmhost.PoolClusterNode) bool {
	q := poolLastQuorum(s.dir)
	if !q.Observed || !q.Quorate || !slices.Contains(q.Hosts, s.State.HostID) {
		return false
	}
	feeding := 0
	for _, node := range nodes {
		member, ok := poolMemberByID(membership, node.HostID)
		if ok && node.HostID != s.State.HostID && poolWatchdogFeeding(member) {
			feeding++
		}
	}
	if feeding == 0 {
		return true
	}
	return len(q.Hosts)-1 >= len(nodes)/2+1
}

// poolWatchdogShouldDisarm is the pool's decision about another host, read from the same records
// that host reads about itself: a host stops being armed when the pool stops wanting a cluster at
// all, or when it is being drained out of the one it has.
func poolWatchdogShouldDisarm(s *Store, membership protocol.PoolMembership, nodes []vmhost.PoolClusterNode, member protocol.PoolMember) bool {
	if _, wanted, _ := poolClusterIntent(s, membership); !wanted {
		return true
	}
	return member.Maintenance || member.Status != "ready" || !poolClusterContains(nodes, member.HostID)
}

// poolWatchdogArmedAt is this host's confirmation that it is feeding a watchdog *now*, and it
// is the whole of the pool's re-confirmation mechanism.
//
// It moves for two reasons, and stands still otherwise so that a settled pool writes and
// replicates nothing. It moves when the spell of arming is new — a reboot, a cluster restart —
// because a survivor must be able to tell "armed since before it left" from "was armed once, and
// has been up unarmed since", and an identical record is never written. And it moves when this
// host has verified itself armed on a Corosync membership it had not verified on before: a host
// that rejoins the pool must re-confirm its arming before a survivor may act on it, and only the
// host itself can say whether it verified sbd after that join. The caller has already re-run the
// whole of ArmLocalWatchdog and read sbd's state back, so reaching here is that verification.
//
// There are two buckets, and the second one is a cost the first made unavoidable. The ring bucket
// covers a host that rejoins: it confirms once per membership event and an unchanging cluster
// writes nothing. But a *survivor* that simply missed a reading for longer than its observation
// gap has to re-establish evidence about hosts that never moved at all — and nothing about those
// hosts' own rings changed, so the ring bucket alone leaves them unprovable for ever. So an armed
// host also re-confirms once per proof bound: one small record per host per minute, which is the
// price of a survivor ever being able to act again after a missed reading.
func poolWatchdogArmedAt(s *Store, recorded *protocol.PoolWatchdogArmed, report protocol.PoolWatchdogArmed) time.Time {
	same := recorded != nil && recorded.Armed && recorded.Kind == report.Kind && recorded.Device == report.Device &&
		recorded.TimeoutSeconds == report.TimeoutSeconds && recorded.FencingGeneration == report.FencingGeneration &&
		!recorded.ArmedAt.IsZero()
	if same && !poolWatchdogRingChanged(s.dir) && time.Since(recorded.ArmedAt) < protocol.PoolWatchdogBound(0) {
		return recorded.ArmedAt
	}
	return time.Now().UTC()
}

// poolWatchdogRingChanged reports whether this host has verified its arming on the Corosync
// membership it is in now, and records that it has. It is memory only: a restarted agent
// re-confirms once, which is the honest answer to "did you check?" when it was not running.
func poolWatchdogRingChanged(dir string) bool {
	poolQuorumMu.Lock()
	defer poolQuorumMu.Unlock()
	ring := poolQuorumLast[dir].RingID
	if ring == "" || poolWatchdogConfirmedRing[dir] == ring {
		return false
	}
	poolWatchdogConfirmedRing[dir] = ring
	return true
}

// reportPoolWatchdogArmed keeps this host's arming in the master's durable membership, the
// same way the capability beside it is kept: the master writes its own row, a member
// advertises, and an unchanged report is not written or sent at all.
func reportPoolWatchdogArmed(ctx context.Context, s *Store, report protocol.PoolWatchdogArmed) error {
	if err := report.Valid(); err != nil {
		return err
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	master, current := "", (*protocol.PoolWatchdogArmed)(nil)
	if err == nil && state.Membership.Valid() == nil {
		master = state.Membership.MasterHostID
		if member, ok := poolMemberByID(state.Membership, s.State.HostID); ok {
			current = member.WatchdogArmed
		}
		if master == s.State.HostID && !samePoolWatchdogArmedReport(current, report) {
			if err = poolAuthorityWritable(state); err == nil {
				err = applyPoolWatchdogArmedReport(s, &state, s.State.HostID, report)
			}
		}
	}
	poolPeerMu.Unlock()
	if err != nil || master == "" || master == s.State.HostID || samePoolWatchdogArmedReport(current, report) {
		return err
	}
	return postPoolAdvertise(ctx, s, map[string]any{"watchdog_armed": report})
}

func applyPoolWatchdogArmedReport(s *Store, state *poolPeerState, hostID string, report protocol.PoolWatchdogArmed) error {
	for i := range state.Membership.Members {
		member := &state.Membership.Members[i]
		if member.HostID != hostID {
			continue
		}
		if samePoolWatchdogArmedReport(member.WatchdogArmed, report) {
			return nil
		}
		// ObservedAt is when this finding changed, stamped by the master that records
		// it. It is not read by anything that decides; the fencing generation is.
		report.ObservedAt = time.Now().UTC()
		self := hostID == s.State.HostID
		wasPromotable := self && poolSelfFencePromotable(*state, hostID)
		member.WatchdogArmed = &report
		if self {
			// Arming makes this host fence-promotable with no profile involved, so it
			// takes on the same boot restriction a profile would: a master that its
			// watchdog reset must re-prove its authority before its guests start.
			if err := recordPoolSelfFenceExposure(state, hostID, wasPromotable); err != nil {
				return err
			}
		}
		return savePoolPeer(s, *state)
	}
	return errors.New("no such member of this pool")
}

// samePoolWatchdogArmedReport compares everything but the stamp, so a settled pool does not
// re-sign and re-push its authority replica because a pass ran.
func samePoolWatchdogArmedReport(recorded *protocol.PoolWatchdogArmed, report protocol.PoolWatchdogArmed) bool {
	if recorded == nil {
		return false
	}
	report.ObservedAt = recorded.ObservedAt
	return *recorded == report
}
