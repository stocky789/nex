// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// A watchdog has nothing to lose unless the pool keeps a quorum, and diskless sbd reads that
// quorum through Pacemaker and has no other source. So a pool that wants its hosts armed needs
// a Corosync/Pacemaker cluster of its own, and this is where Nex Core forms one.
//
// It carries no resources. Its entire purpose is that votequorum decides, on every host, which
// side of a partition is the majority, and that Pacemaker's no-quorum-policy lets the minority
// side die. That is why a plain pool's cluster may be restarted one host at a time and a
// clustered-storage cluster may not: there is nothing running on it to interrupt.
//
// A host has one /etc/corosync/corosync.conf, so it has one cluster. Clustered storage already
// owns that file where a pool has a clustered repository, and this release leaves such pools
// exactly as they were — unarmed, and saying so. writeCorosyncConfig refuses to take a file
// over from the other consumer, so the interlock holds even if an intent check is wrong.
//
// Nothing here is enabled at boot, deliberately. The whole cold-start hazard of watchdog
// fencing is a host starting an armed cluster alone: inquorate from the first moment, it would
// reset itself in a loop with no failure anywhere in the pool. Nex Core therefore starts this
// cluster itself, after it has read the pool's records, and always unarmed; sbd joins only at a
// later, deliberate restart once quorum has been observed to hold.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

// poolClusterMarker identifies a corosync.conf this Core wrote for a pool's own watchdog
// cluster, so the clustered-storage writer refuses it and vice versa.
const poolClusterMarker = "# Nex pool watchdog cluster; no resources run on it.\n"

// PoolClusterNode is one host of the pool's own cluster: the durable Nex Core host ID Corosync
// will know it by, and the address its peers reach it on.
type PoolClusterNode struct {
	HostID  string
	Address string
}

// PoolClusterNodeID derives Corosync's numeric node ID from the durable host ID. It is stable
// across reinstalls and master changes because the host ID is, and an ejected host's number is
// simply never in a nodelist again rather than being handed to somebody else.
func PoolClusterNodeID(hostID string) uint32 {
	digest := sha256.Sum256([]byte("nex-pool-cluster-node\x00" + hostID))
	// Keep IDs produced by the previous scheme when they were already in Corosync's
	// 16-bit range. Knet rejects larger IDs, so fold the rest into its supported range;
	// RenderPoolCluster still refuses any collision within one pool.
	legacy := binary.BigEndian.Uint32(digest[:4])&0x7fffffff | 1
	if legacy <= 65535 {
		return legacy
	}
	return binary.BigEndian.Uint32(digest[:4])%65535 + 1
}

// PoolClusterName is the Corosync cluster name for a pool. Corosync accepts a short token, and
// a pool ID is an opaque durable identifier, so this is a stable digest of it rather than the
// identifier itself.
func PoolClusterName(poolID string) string {
	return protocol.PoolWitnessClusterName(poolID)
}

// RenderPoolCluster is the pool's corosync.conf. Every ready host is in it: a host left out of
// the nodelist is a host the rest of the pool makes quorum decisions without, which is the one
// arithmetic no host may be armed against.
func RenderPoolCluster(poolID string, nodes []PoolClusterNode) (string, error) {
	return RenderPoolClusterWithWitness(poolID, nodes, "")
}

func RenderPoolClusterWithWitness(poolID string, nodes []PoolClusterNode, witnessAddress string) (string, error) {
	if len(nodes) < 3 {
		if len(nodes) != 2 || witnessAddress == "" {
			return "", errors.New("a pool watchdog cluster needs three hosts or a registered qdevice witness")
		}
	}
	if witnessAddress != "" {
		if _, _, err := net.SplitHostPort(witnessAddress); err != nil {
			return "", errors.New("pool witness address is invalid")
		}
	}
	entries := make([]corosyncNode, 0, len(nodes))
	seen := map[uint32]string{}
	for _, node := range nodes {
		if node.HostID == "" || node.Address == "" {
			return "", errors.New("every host of a pool watchdog cluster needs an identifier and an address")
		}
		id := PoolClusterNodeID(node.HostID)
		if other, clash := seen[id]; clash {
			return "", errors.New("two hosts of this pool derive the same Corosync node identifier: " + other + " and " + node.HostID)
		}
		seen[id] = node.HostID
		entries = append(entries, corosyncNode{address: node.Address, name: node.HostID, id: id})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	// A two-host witness cluster uses freeze, not SBD. WaitForAll can keep the
	// survivor inquorate after its witness returns while the other host is down.
	return renderCorosyncConfig(poolClusterMarker, PoolClusterName(poolID), entries, len(nodes) != 2, witnessAddress), nil
}

// PoolClusterOwnsCorosync is whether the configuration on this host is the pool's own cluster.
// A host with no configuration, or with clustered storage's, is not one this code may touch.
func PoolClusterOwnsCorosync() bool {
	config, err := os.ReadFile(corosyncConfigPath)
	return err == nil && strings.Contains(string(config), poolClusterMarker)
}

// PoolClusterCanOwnCorosync checks configuration ownership before Core installs
// the pool key, so a foreign cluster's authentication is never changed.
func PoolClusterCanOwnCorosync(stateDir string) error {
	old, err := os.ReadFile(corosyncConfigPath)
	return checkCorosyncConfigOwner(stateDir, old, err, poolClusterMarker)
}

// ApplyPoolCluster brings this host's Corosync and Pacemaker to the pool's rendered
// configuration and leaves them running. It reports whether the configuration changed.
//
// A membership change is applied with corosync-cfgtool -R wherever Corosync is already
// running: reloading the nodelist moves every host's expected votes together, without
// restarting a peer that is feeding a watchdog. Only arming needs a restart, and only the one
// host whose turn it is takes one.
func ApplyPoolCluster(ctx context.Context, stateDir, poolID string, nodes []PoolClusterNode) (bool, error) {
	return ApplyPoolClusterWithWitness(ctx, stateDir, poolID, nodes, "")
}

func ApplyPoolClusterWithWitness(ctx context.Context, stateDir, poolID string, nodes []PoolClusterNode, witnessAddress string) (bool, error) {
	rendered, err := RenderPoolClusterWithWitness(poolID, nodes, witnessAddress)
	if err != nil {
		return false, err
	}
	changed, err := writeCorosyncConfig(stateDir, rendered, poolClusterMarker)
	if err != nil {
		return false, err
	}
	running, err := unitActive(ctx, "corosync.service")
	if err != nil {
		return changed, err
	}
	if !running {
		// Started, never enabled, and never armed. Disabling sbd here rather than relying
		// on the arming pass having got there first is the whole cold-start defence, and it
		// has to be structural: the arming pass can fail to reach the master — which is
		// exactly the state a host boots into after a site power cut — and sbd is still
		// enabled from the last time this host armed, on a corosync.service.requires edge
		// that would pull it in here. The only path that starts a cluster *with* sbd is
		// RestartPoolClusterForArming, which is reached only after ArmLocalWatchdog has
		// deliberately re-enabled it.
		if _, err = clusterRun(ctx, "systemctl", "disable", "sbd.service"); err != nil {
			return changed, err
		}
		if _, err = clusterRun(ctx, "systemctl", "start", "corosync.service", "pacemaker.service"); err != nil {
			return changed, err
		}
		return changed, nil
	}
	if changed {
		if _, err = clusterRun(ctx, "corosync-cfgtool", "-R"); err != nil {
			return changed, err
		}
	}
	active, err := unitActive(ctx, "pacemaker.service")
	if err != nil || active {
		return changed, err
	}
	_, err = clusterRun(ctx, "systemctl", "start", "pacemaker.service")
	return changed, err
}

// poolClusterStopTimeout bounds one attempt at stopping this host's cluster services. Measured in
// the nested laboratory: `systemctl stop pacemaker.service` hung indefinitely on those guests more
// than once and had to be killed by hand. Cancelling the client does not cancel the systemd job, so
// a hung stop is reported and retried on the next pass rather than escalated — and sbd is never
// signalled to get out of it, because a host that is still feeding a watchdog inside a quorate
// cluster is safe, and one that has had its daemon killed is not.
var poolClusterStopTimeout = 20 * time.Second

// ErrPoolClusterStopStalled is a stop that did not finish inside its bound.
var ErrPoolClusterStopStalled = errors.New("this host's cluster services did not stop when asked, so it is still a member and still feeding its watchdog; Nex Core will try again and will not kill the watchdog daemon to get out of it")

// stopPoolCluster stops pacemaker and corosync under their own bound.
func stopPoolCluster(ctx context.Context) error {
	stop, done := context.WithTimeout(ctx, poolClusterStopTimeout)
	defer done()
	if _, err := clusterRun(stop, "systemctl", "stop", "pacemaker.service", "corosync.service"); err != nil {
		if stop.Err() != nil && ctx.Err() == nil {
			return ErrPoolClusterStopStalled
		}
		return err
	}
	return nil
}

// LeavePoolCluster takes this host out of the pool's cluster. Debian's sbd.service is
// PartOf=corosync.service as well as RequiredBy it, and PartOf is the property that does the work
// here: stopping corosync stops sbd with it, so the device is released. RequiredBy propagates a
// stop the other way round and would not.
//
// It touches nothing unless the configuration on disk is this pool's own cluster, and callers
// must not reach it while any host of that cluster is still armed: a host that stops its cluster
// while a peer is still feeding a watchdog can take that peer below quorum.
func LeavePoolCluster(ctx context.Context) error {
	if !PoolClusterOwnsCorosync() {
		return nil
	}
	if err := DeactivatePoolWitness(ctx); err != nil {
		return err
	}
	running, err := unitActive(ctx, "corosync.service")
	if err != nil {
		return err
	}
	if running {
		if err = stopPoolCluster(ctx); err != nil {
			return err
		}
	}
	// And release the file. A host has one corosync.conf, so leaving this cluster behind on
	// disk is what would stop the pool ever registering a clustered-storage repository —
	// which is the remedy Core itself names when it refuses one.
	if err = os.Remove(corosyncConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// RestartPoolClusterForArming is the one restart in this slice, and the only way a host can
// begin or stop feeding a watchdog: Debian's sbd.service refuses a manual start and exists only
// as a dependency of corosync.service.
//
// It is safe here for one reason and the reason is checked, not assumed: a pool's own cluster
// has no resources on it, so stopping it stops nothing that was running. A CIB with any
// resource in it is somebody else's cluster and this refuses to touch it. The caller is
// responsible for the other half — that it holds the pool's one arming turn and that the rest
// of the pool stays quorate without it.
func RestartPoolClusterForArming(ctx context.Context) error {
	if !PoolClusterOwnsCorosync() {
		return errors.New("this host's Corosync configuration is not this pool's own watchdog cluster")
	}
	if err := poolClusterHasNoResources(ctx); err != nil {
		return err
	}
	if err := stopPoolCluster(ctx); err != nil {
		return err
	}
	_, err := clusterRun(ctx, "systemctl", "start", "corosync.service", "pacemaker.service")
	return err
}

// poolClusterHasNoResources refuses a restart of a cluster that is running anything. It is the
// structural guard behind the rule that only a plain pool's cluster is restarted this way.
func poolClusterHasNoResources(ctx context.Context) error {
	current, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		return errors.New("Nex Core could not read this pool's cluster configuration, so it will not restart it")
	}
	var root cibNode
	if err := xml.Unmarshal(current, &root); err != nil {
		return errors.New("this pool's cluster configuration is unreadable, so Nex Core will not restart it")
	}
	found := root.walk(func(n cibNode) bool {
		return n.XMLName.Local == "primitive"
	})
	if found {
		return errors.New("this pool's cluster is running resources, so Nex Core will not restart it to arm a watchdog")
	}
	return nil
}

// ApplyPoolClusterProperties writes the pool cluster's whole policy. Only the master writes it,
// and every host reads it back before it arms: clusterSelfFences refuses any cluster whose
// no-quorum-policy would keep an inquorate host alive, which is the difference between a
// watchdog that resets a lost host and one that never expires.
func ApplyPoolClusterProperties(ctx context.Context) error {
	return applyPoolClusterProperties(ctx, poolClusterProperties())
}

// ApplyPoolWitnessClusterProperties keeps the two-host witness cluster running in a
// partition. Automatic watchdog fencing remains disabled until the witness-loss case is
// accepted; `suicide` here could reset a surviving host when qnetd disappears.
func ApplyPoolWitnessClusterProperties(ctx context.Context) error {
	return applyPoolClusterProperties(ctx, poolWitnessClusterProperties())
}

func applyPoolClusterProperties(ctx context.Context, properties []string) error {
	current, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		return err
	}
	for _, property := range properties {
		if _, err := clusterRun(ctx, "pcs", "property", "set", property); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if !cibHasPropertyName(current, "stonith-watchdog-timeout") {
		return nil
	}
	// Removed, not merely never written: a pool whose CIB carries this from an earlier Nex
	// Core, or from an administrator, is one node-without-sbd away from losing its whole
	// cluster. See poolClusterProperties.
	_, err = clusterRun(ctx, "pcs", "property", "unset", "stonith-watchdog-timeout")
	return err
}

// poolClusterProperties is the pool cluster's policy, and nothing else.
//
//   - no-quorum-policy=suicide is what makes the watchdog a fence at all. Measured: with
//     freeze a partitioned host was still running after two minutes; with suicide the same
//     host had reset and rebooted 15.7 s after its link went down.
//   - stonith-enabled with no fence resource configured means the watchdog is the only way a
//     node is excluded, which is exactly the topology this cluster exists for. It costs bounded
//     log noise: Pacemaker logs "no STONITH resources have been defined" and writes one
//     pe-error file per transition, capped by its own series limit, so a healthy pool reports
//     configuration errors to crm_verify and pcs status. Measured alternative:
//     stonith-enabled=false silences it and an armed host still self-fenced at 17.9 s, because
//     Pacemaker counts sbd as the fencing mechanism (have-watchdog). It is kept true because
//     every other partition measurement in slices 4b and 4c was taken under it and the fence
//     firing is the one property that must not be risked on one run.
//   - stonith-watchdog-timeout is deliberately NOT written, and is removed where it is found.
//     It is the quorate side's wait before it treats a watchdog-only node as fenced, which
//     matters when Pacemaker has resources to recover — and this cluster has none, by
//     construction. What it costs is not theoretical: Pacemaker answers the property plus one
//     node without sbd by logging "emerg: Shutting down: stonith-watchdog-timeout configured
//     (30s) but SBD not active" and taking the cluster down, and the nested lab measured that
//     shutdown reaching nodes that were still feeding, whose sbd then expired — a healthy host
//     reset. Every ordering that produces an unarmed node on this cluster is ordinary: forming
//     it, the arming rollout, a teardown, and a host rebooting back into an armed pool. Nex's
//     own proof bound (PoolWatchdogBound, 60 s, four times the measured worst case) is what
//     gates a promotion, and it is not this property. The property belongs with the slice that
//     puts DLM and GFS2 resources on a pool's cluster, where the wait means something.
func poolClusterProperties() []string {
	return []string{"no-quorum-policy=suicide", "stonith-enabled=true"}
}

func poolWitnessClusterProperties() []string {
	return []string{"no-quorum-policy=freeze", "stonith-enabled=true"}
}

// PoolClusterBoundSet is whether the quorate side's bound is on this cluster right now, read from
// the CIB and not from anybody's record. It is the precondition on a teardown restart: starting a
// cluster with the bound set and no sbd on that node is the one thing Pacemaker answers by shutting
// the cluster down on every node, which takes the quorum away from the hosts that are still
// feeding. An unreadable CIB answers "set", because that is the answer that waits.
func PoolClusterBoundSet(ctx context.Context) bool {
	current, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		return true
	}
	return cibHasPropertyName(current, "stonith-watchdog-timeout")
}

// PoolClusterPropertiesReady is whether the pool's cluster already says all of it, read back
// from the CIB rather than remembered from having written it.
func PoolClusterPropertiesReady(ctx context.Context) (bool, error) {
	return poolClusterPropertiesReady(ctx, poolClusterProperties())
}

// PoolWitnessClusterPropertiesReady reads back the safe two-host policy from the CIB.
func PoolWitnessClusterPropertiesReady(ctx context.Context) (bool, error) {
	return poolClusterPropertiesReady(ctx, poolWitnessClusterProperties())
}

func poolClusterPropertiesReady(ctx context.Context, properties []string) (bool, error) {
	current, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		return false, err
	}
	for _, property := range properties {
		name, value, _ := strings.Cut(property, "=")
		if !cibHasProperty(current, name, value) {
			return false, nil
		}
	}
	// A bound from anywhere is not "current": it takes down the Pacemaker of every node that
	// has no sbd, and every ordinary event on this cluster produces one.
	return !cibHasPropertyName(current, "stonith-watchdog-timeout"), nil
}
