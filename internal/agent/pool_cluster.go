// SPDX-License-Identifier: AGPL-3.0-only
package agent

// The pool owns its cluster. Slice 4b built the watchdog engine and nothing armed, because a
// diskless sbd has no quorum to lose unless a Corosync/Pacemaker cluster exists; this is where
// Nex Core forms one for a pool that has no clustered storage.
//
// Everything here is derived from records the pool already keeps. The nodelist is the master's
// durable membership, read on every host from the replica it already holds; there is no second
// list, no cluster registry and nothing for a Controller to keep. A fresh Controller learns the
// whole of it by asking the master for its membership, exactly as it learns everything else.
//
// The two rules this file exists to keep:
//
//   - A host never comes up armed. Corosync and Pacemaker are started by this reconcile, never
//     enabled at boot, and always with sbd disabled. A host that boots alone — a site power
//     cut, the last host of a pool rebooting — therefore has nothing to reset it while it
//     waits for its peers, which is the one way default-on fencing could reset a healthy host
//     with no failure anywhere in the pool.
//   - Exactly one host arms at a time. Arming means starting sbd, and Debian's sbd.service can
//     only start with corosync.service, so it means a controlled cluster restart. That is safe
//     on a pool's own cluster because nothing runs on it — checked, not assumed — and only
//     while every other host is quorate without the one restarting. Whose turn it is comes
//     from the membership itself: the first ready host that has not reported itself armed.

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strconv"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// The host-side cluster actions, as package variables so a test stands in for Corosync the way
// armLocalWatchdog stands in for sbd.
var (
	applyPoolCluster            = vmhost.ApplyPoolCluster
	applyPoolClusterWithWitness = vmhost.ApplyPoolClusterWithWitness
	leavePoolCluster            = vmhost.LeavePoolCluster
	restartPoolCluster          = vmhost.RestartPoolClusterForArming
	applyPoolClusterProperties  = vmhost.ApplyPoolClusterProperties
	poolClusterPropertiesReady  = vmhost.PoolClusterPropertiesReady
	applyPoolWitnessProperties  = vmhost.ApplyPoolWitnessClusterProperties
	poolWitnessPropertiesReady  = vmhost.PoolWitnessClusterPropertiesReady
	poolClusterBoundSet         = vmhost.PoolClusterBoundSet
)

// poolClusteredStorageDetail is the one sentence a clustered-storage pool gets, everywhere.
// Arming such a pool means changing its no-quorum-policy from freeze to suicide underneath a
// mounted GFS2 filesystem, which changes what an inquorate node does to data in flight. That
// needs its own lab and its own decision, so this release leaves those pools exactly as they
// were and says so rather than half-owning their cluster.
const poolClusteredStorageDetail = "this pool runs clustered storage; watchdog fencing for it is not available yet"

// ReconcilePoolCluster brings this host's Corosync and Pacemaker to what the pool's records
// call for. It runs on every host on every pass and is the whole lifecycle: there is no
// command, no job, and nothing a Controller has to stay alive for.
func ReconcilePoolCluster(ctx context.Context, s *Store) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		return err
	}
	nodes, want, _ := poolClusterIntent(s, state.Membership)
	self, _ := poolMemberByID(state.Membership, s.State.HostID)
	if !want || !poolClusterContains(nodes, s.State.HostID) {
		// Nobody leaves while anybody is still feeding a watchdog. A host that stops its
		// cluster stops sbd with it, which is safe for that host and dangerous for the
		// others: the pool's expected votes do not shrink as hosts go, so the last one
		// standing is a minority of one, still feeding, and resets itself five seconds
		// later. Turning Host fencing off would have done that, and so would maintenance
		// on one host of three.
		//
		// So a teardown is the arming rollout in reverse, and ReconcilePoolWatchdogArming
		// runs it: one host at a time puts its watchdog down by restarting its cluster
		// without sbd, staying a member and staying quorate. Only when the pool's own
		// records say nobody is armed does anyone leave.
		if poolClusterMayLeave(s, state.Membership, nodes, self, want) {
			return leavePoolCluster(ctx)
		}
		// Not yet. Either this host is still feeding — its own arming pass puts the device
		// down by restarting, in turn — or a peer is, and this host leaving would take that
		// peer below quorum. Meanwhile the master keeps the policy current, which above all
		// means taking the quorate side's bound off *before* the first host restarts its
		// cluster rather than after.
		return poolClusterPolicy(ctx, s)
	}
	witnessAddress := ""
	if state.Membership.Witness != nil && len(nodes) == 2 {
		witnessAddress = state.Membership.Witness.Address
	}
	if witnessAddress == "" {
		_, err = applyPoolCluster(ctx, s.dir, state.Membership.PoolID, nodes)
	} else {
		_, err = applyPoolClusterWithWitness(ctx, s.dir, state.Membership.PoolID, nodes, witnessAddress)
	}
	if err != nil {
		return err
	}
	if witnessAddress != "" && vmhost.PoolWitnessCredentialsReady(ctx, s.dir, state.Membership.PoolID, *state.Membership.Witness) {
		if err = vmhost.ActivatePoolWitness(ctx, s.dir, state.Membership.PoolID, *state.Membership.Witness); err != nil {
			return err
		}
	} else if witnessAddress != "" {
		if err = vmhost.DeactivatePoolWitness(ctx); err != nil {
			return err
		}
	} else if err = vmhost.DeactivatePoolWitness(ctx); err != nil {
		return err
	}
	return poolClusterPolicy(ctx, s)
}

// poolClusterPolicy is the master's one write on the pool's cluster, and every host reads it back
// before it arms: a cluster that would keep an inquorate host running is not a fence, and
// ArmLocalWatchdog refuses one. It also removes stonith-watchdog-timeout wherever it finds it —
// see vmhost.poolClusterProperties for why that property has no place on a cluster with no
// resources, and what it cost the nested lab to find out.
func poolClusterPolicy(ctx context.Context, s *Store) error {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return err
	}
	nodes, _, _ := poolClusterIntent(s, membership)
	if poolWitnessPolicyApplies(nodes, membership) {
		current, err := poolWitnessPropertiesReady(ctx)
		if err != nil || current {
			return err
		}
		if err = applyPoolWitnessProperties(ctx); err != nil {
			return err
		}
		current, err = poolWitnessPropertiesReady(ctx)
		if err != nil {
			return err
		}
		if !current {
			return errors.New("two-host witness cluster policy did not read back as freeze")
		}
		return nil
	}
	current, err := poolClusterPropertiesReady(ctx)
	if err != nil || current {
		return err
	}
	if err = applyPoolClusterProperties(ctx); err != nil {
		return err
	}
	current, err = poolClusterPropertiesReady(ctx)
	if err != nil {
		return err
	}
	if !current {
		return errors.New("pool cluster policy did not read back after apply")
	}
	return nil
}

func poolWitnessPolicyApplies(nodes []vmhost.PoolClusterNode, membership protocol.PoolMembership) bool {
	return len(nodes) == 2 && membership.Witness != nil && membership.Witness.Valid(membership.PoolID) == nil
}

// poolClusterMayLeave is whether this host can stop its cluster now. Two things stop it, and both
// of them are a host resetting if they are ignored.
//
// This host still feeding: the cluster it would stop is the quorum its own sbd is reading. It puts
// the device down by restarting instead, in its turn, which its arming pass does.
//
// A peer still feeding, which this host's departure would take below quorum: the pool's expected
// votes do not shrink because somebody stopped, so the last host standing is a minority of one
// with a live device. That is the reset the nested lab measured during maintenance.
func poolClusterMayLeave(s *Store, membership protocol.PoolMembership, nodes []vmhost.PoolClusterNode, self protocol.PoolMember, want bool) bool {
	if poolWatchdogFeeding(self) {
		return false
	}
	feeding := 0
	for _, member := range membership.Members {
		if member.Status == "ready" && member.HostID != s.State.HostID && poolWatchdogFeeding(member) {
			feeding++
		}
	}
	// A peer is still feeding. This host's vote is part of what keeps that peer quorate for as
	// long as the pool still lists it, so it waits: the teardown finishes one host at a time
	// and everybody leaves at the end. The exception is a host the pool has already taken out
	// of the nodelist while the pool still wants a cluster — drained, or ejected — whose peers
	// really are about to reload it out, so its vote stops counting either way, and staying
	// would be the worse choice: an unarmed node on a cluster carrying the quorate side's
	// bound is what shuts that cluster down.
	//
	// It is only an exception while the pool still wants a cluster. In a teardown nobody
	// reloads anything — applyPoolCluster runs only when want is true — so the survivors go on
	// expecting every vote of the nodelist they last applied, and a host leaving on this
	// escape takes one of those votes away from peers that are still feeding. That is two
	// drained hosts of four resetting the two healthy ones. There is no bound to escape from
	// either: in a teardown the master takes it off before the first host restarts.
	return feeding == 0 || (want && !poolClusterContains(nodes, s.State.HostID))
}

// poolWatchdogFeeding is whether this host's device is still live, which is the question the
// teardown turns on. "Armed" is the pool's decision and goes false first; the device follows when
// that host's cluster next stops.
func poolWatchdogFeeding(member protocol.PoolMember) bool {
	return member.WatchdogArmed != nil && (member.WatchdogArmed.Armed || member.WatchdogArmed.Feeding)
}

// poolClusterIntent is the pool's own cluster, derived from the membership the master owns: the
// hosts in it, whether Nex Core forms one at all, and Core's sentence for why it does not.
//
// It is evaluated identically on every host, from records every host holds, so a member decides
// its own participation without asking anyone — which is what has to be true when the master is
// the host that has gone.
func poolClusterIntent(s *Store, membership protocol.PoolMembership) ([]vmhost.PoolClusterNode, bool, string) {
	// Two different counts, deliberately. "Usable" is the hosts this pool could arm, which
	// decides whether it has a majority side at all. The nodelist is those hosts plus any
	// drained host that is still feeding a watchdog: dropping such a host from the
	// survivors' nodelist raises their expected votes above it and resets it, which is the
	// planned-work reset in a different order. It leaves under its own power instead.
	var missing []string
	var nodes []vmhost.PoolClusterNode
	usable := 0
	for _, member := range membership.Members {
		if member.Status != "ready" {
			continue
		}
		if member.Maintenance && !poolWatchdogFeeding(member) {
			continue
		}
		host, err := poolPeerHost(member.PeerOrigin)
		if err != nil {
			missing = append(missing, member.HostID)
			continue
		}
		if !member.Maintenance {
			usable++
		}
		nodes = append(nodes, vmhost.PoolClusterNode{HostID: member.HostID, Address: host})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].HostID < nodes[j].HostID })
	// The nodelist is returned even when the answer is no: a pool that is being torn down
	// still has a cluster, and the hosts in it have to leave it in an order that never
	// leaves one of them feeding a watchdog without a majority.
	switch {
	case membership.HostFencingDisabled:
		return nodes, false, "Host fencing is turned off for this pool"
	case poolHasClusteredStorage(s):
		return nodes, false, poolClusteredStorageDetail
	case len(missing) > 0:
		// Never form a cluster without a host the pool counts as ready: the others would
		// make quorum decisions with an expected vote count that host never agreed to.
		return nodes, false, "Nex Core does not know a cluster address for " + missing[0] + " yet"
	case usable < 2:
		return nodes, false, "this pool has " + strconv.Itoa(usable) +
			" hosts available to it, and a watchdog needs two hosts and a registered qdevice witness to have a majority side"
	case usable == 2 && (membership.Witness == nil || membership.Witness.Valid(membership.PoolID) != nil):
		return nodes, false, "this pool needs three or more hosts, or two hosts with a registered qdevice witness"
	case len(nodes) > protocol.PoolWatchdogClusterHosts:
		return nodes, false, "this pool has " + strconv.Itoa(len(nodes)) + " hosts, more than the " +
			strconv.Itoa(protocol.PoolWatchdogClusterHosts) +
			" Nex Core has exercised watchdog fencing on; no host is armed, and none is left out of the pool's quorum either"
	}
	return nodes, true, ""
}

func poolClusterContains(nodes []vmhost.PoolClusterNode, hostID string) bool {
	for _, node := range nodes {
		if node.HostID == hostID {
			return true
		}
	}
	return false
}

// poolPeerHost is the address Corosync reaches a host on: the host part of the peer origin the
// pool already records for it, so the cluster ring runs where the pool's own traffic runs.
//
// That is the management network, and it is why a management-network change on an armed host is
// a self-reset trigger. Nex Core refuses such a change while this host is armed, the guide says
// so, and the Host fencing setting says so beside the switch.
func poolPeerHost(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("this host has no recorded pool address")
	}
	return u.Hostname(), nil
}

// poolHasClusteredStorage is whether this pool has a clustered-storage repository, which owns
// the host's one corosync.conf. Both halves of the interlock read this: a pool with one never
// gets a pool-owned cluster, and ExecuteStorageClusterRequest refuses to create one on a pool
// whose hosts are armed.
func poolHasClusteredStorage(s *Store) bool {
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		// Unreadable is not "none": the safe answer is the one that arms nothing.
		return true
	}
	return len(state.Operations) > 0
}

// poolWatchdogClusterFree refuses a clustered-storage repository on a pool that owns a watchdog
// cluster, or whose hosts are armed. It is read from the master's membership, which is where
// both facts live.
func poolWatchdogClusterFree(s *Store) error {
	membership, err := Membership(s)
	if err != nil {
		return errors.New("Nex Core cannot read this pool's membership, so it cannot tell whether its hosts are armed")
	}
	for _, member := range membership.Members {
		if member.WatchdogArmed != nil && member.WatchdogArmed.Armed {
			return errors.New("this pool's hosts are feeding watchdogs that reset them when they lose this pool's quorum, and clustered storage needs a cluster that keeps an inquorate host running instead. Turn Host fencing off for this pool first; every host disarms through its own record before the setting takes effect")
		}
	}
	if _, want, _ := poolClusterIntent(s, membership); want {
		return errors.New("Nex Core is forming this pool's own watchdog cluster, and clustered storage needs to own that cluster itself. Turn Host fencing off for this pool first")
	}
	return nil
}

// poolWatchdogArmingTurn is the one host that may restart its cluster to arm right now: the
// first ready host, in host-ID order, that has not reported itself armed. It is derived from
// the membership every host already holds, so there is no grant to keep, nothing to resume and
// no way for two hosts to hold it at once.
//
// A host that cannot arm holds the turn and the rollout stops there, visibly, with that host's
// own reason. Stopping is the safe direction — fewer armed hosts is less protection, never a
// false proof — and an operator is told which host it is rather than discovering a pool that is
// half armed for a reason nobody recorded.
func poolWatchdogArmingTurn(nodes []vmhost.PoolClusterNode, membership protocol.PoolMembership) string {
	for _, node := range nodes {
		member, ok := poolMemberByID(membership, node.HostID)
		if !ok {
			continue
		}
		if member.Watchdog == nil || !protocol.PoolWatchdogArmable(member.Watchdog.Kind) {
			// It can never arm, so it is not a turn anybody is waiting for. The
			// prerequisite checklist already reports it, and it stays in the cluster:
			// it is one of the pool's votes.
			continue
		}
		if member.WatchdogArmed == nil || !member.WatchdogArmed.Armed {
			return node.HostID
		}
	}
	return ""
}

// PoolFenceClusterFor is the master's report of what this pool's cluster is doing. It is
// derived on every report from the membership and each host's own armed report, and is never
// stored: the durable facts are those records, and this is words around them.
func PoolFenceClusterFor(s *Store, membership protocol.PoolMembership, q vmhost.PoolQuorum) *protocol.PoolFenceCluster {
	nodes, want, detail := poolClusterIntent(s, membership)
	state := &protocol.PoolFenceCluster{Total: len(nodes), Detail: detail}
	if !want {
		state.State = protocol.PoolClusterNotAvailable
		if membership.HostFencingDisabled {
			state.State = protocol.PoolClusterOff
		}
		// A teardown that has stalled must say so, and must not read as progress. Every
		// host's record says it is not armed, because the pool decided that first; a host
		// that is still feeding is a host that can still reset itself, and if it cannot
		// take its turn — a peer lost mid-teardown, so nobody's departure would leave the
		// others quorate — it stays that way until that peer comes back.
		var feeding, absent []string
		for _, member := range membership.Members {
			if member.Status != "ready" {
				continue
			}
			if poolWatchdogFeeding(member) {
				feeding = append(feeding, member.HostID)
			} else if q.Observed && !slices.Contains(q.Hosts, member.HostID) {
				absent = append(absent, member.HostID)
			}
		}
		if len(feeding) == 0 {
			return state
		}
		state.State, state.Armed, state.Total = protocol.PoolClusterDegraded, len(feeding), len(nodes)
		state.ArmingHostID = feeding[0]
		// One host at a time can put its device down only while the rest of the cluster is
		// there to stay quorate without it. With a host missing, two hosts of three cannot:
		// either of them leaving takes the other below quorum, and the other is still armed.
		blocked := len(absent) > 0 && len(feeding) > 1
		switch {
		case blocked:
			state.State = protocol.PoolClusterArmed
			state.Detail = "armed — this pool's teardown is blocked by " + absent[0] +
				". These hosts are still feeding a watchdog and none of them can stop without taking the others below quorum, " +
				"so they stay armed and still reset themselves if they lose this pool's quorum. They finish when " + absent[0] +
				" is back in this pool's cluster, or when it is ejected from the pool."
		default:
			state.Detail = "Nex Core is putting this pool's watchdogs down one host at a time; " +
				strconv.Itoa(len(feeding)) + " still feeding one, starting with " + feeding[0] + ". " + state.Detail
		}
		return state
	}
	for _, node := range nodes {
		if member, ok := poolMemberByID(membership, node.HostID); ok && member.WatchdogArmed != nil && member.WatchdogArmed.Armed {
			state.Armed++
		}
	}
	state.ArmingHostID = poolWatchdogArmingTurn(nodes, membership)
	if len(nodes) == 2 && membership.Witness != nil {
		witness := vmhost.ReadPoolWitnessStatus(context.Background(), s.dir, membership.PoolID, *membership.Witness)
		if !membership.Witness.CredentialsReady || !witness.Observed {
			state.State, state.ArmingHostID = protocol.PoolClusterForming, ""
			state.Detail = "this pool's qdevice witness is not ready: " + witness.Error
			return state
		}
		state.State, state.ArmingHostID = protocol.PoolClusterDegraded, ""
		state.Detail = "the qdevice witness is connected and voting; two-host watchdog fencing remains withheld pending native testing of witness loss with one host already down"
		return state
	}
	switch {
	case !q.Observed || !q.Quorate:
		state.State, state.Detail = protocol.PoolClusterForming,
			"Nex Core is forming this pool's cluster; no host is armed until it is quorate"
	case state.ArmingHostID != "":
		state.State = protocol.PoolClusterArming
		state.Detail = "arming host " + strconv.Itoa(state.Armed+1) + " of " + strconv.Itoa(state.Total) +
			"; each host restarts its cluster services once, one host at a time"
	case state.Armed < state.Total:
		state.State = protocol.PoolClusterDegraded
		state.Detail = "some of this pool's hosts have no watchdog Nex Core can arm; the checklist above says which"
	default:
		state.State = protocol.PoolClusterArmed
		state.Detail = "every host of this pool is feeding a watchdog that resets it if it loses this pool's quorum"
	}
	return state
}
