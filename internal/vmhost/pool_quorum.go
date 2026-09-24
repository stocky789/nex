// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// A watchdog exclusion proof rests on the pool being quorate without the target, and that
// answer has to come from the engine that actually decides it. Nex Core's own heartbeats
// would be a second opinion about the same question: sbd resets a host because Corosync's
// votequorum told it that it had lost quorum, so the survivors' claim that it did must be
// read from the same votequorum and not from whether Nex could reach it.

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// PoolQuorum is one host's own reading of the pool's Corosync quorum. It is never durable and
// never replicated: it is what this host can see right now, read from the engine that decides
// it, and it is the second of the three conditions of a watchdog exclusion proof.
type PoolQuorum struct {
	// Observed is whether this host could read a quorum at all. A host with no Corosync
	// running reports false, which is not the same as "not quorate": with no Corosync
	// nothing is being reset either, so there is nothing to prove in either direction.
	Observed bool `json:"observed"`
	Quorate  bool `json:"quorate"`
	// RingID is Corosync's own identifier for this membership. It changes on every
	// membership event, so a change in it means a node left or joined between two
	// readings — a flap no sampling interval could otherwise see.
	RingID string `json:"ring_id,omitempty"`
	// Hosts is the Nex Core host IDs in this host's partition, as Corosync names them.
	Hosts []string `json:"hosts,omitempty"`
}

// Excludes is whether this host is in a quorate partition that the named host is not in.
func (q PoolQuorum) Excludes(hostID string) bool {
	if !q.Observed || !q.Quorate || hostID == "" {
		return false
	}
	for _, host := range q.Hosts {
		if host == hostID {
			return false
		}
	}
	return true
}

// LocalQuorum is this host's own view of the pool's Corosync quorum: whether this host is in
// the quorate partition, and which hosts are in that partition with it. The node names are the
// durable Nex Core host IDs, because that is what Nex writes into the nodelist.
//
// A host with no Corosync running has no view at all, which is reported as such and is never
// read as "not quorate": nobody is being reset either, so there is nothing to prove.
func LocalQuorum(ctx context.Context) (PoolQuorum, error) {
	out, err := clusterRun(ctx, "corosync-quorumtool", "-p")
	// corosync-quorumtool exits non-zero precisely when the node is not quorate, so its
	// output is parsed either way and only an unreadable one is an error.
	if len(out) == 0 {
		if err == nil {
			err = errors.New("corosync reported nothing about this pool's quorum")
		}
		return PoolQuorum{}, err
	}
	q := PoolQuorum{Observed: true}
	members := false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Ring ID:") {
			q.RingID = strings.TrimSpace(strings.TrimPrefix(line, "Ring ID:"))
		}
		if strings.HasPrefix(line, "Quorate:") {
			q.Quorate = strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(line, "Quorate:")), "yes")
		}
		if strings.HasPrefix(line, "Membership information") {
			members = true
			continue
		}
		if !members {
			continue
		}
		// The header is "Nodeid Votes Name", or "Nodeid Votes Qdevice Name" where a
		// quorum device is configured, and the local node's row ends "(local)". So the
		// name is the last field, not the third: a pool that gains a quorum device must
		// not start reading every host's name as "NR". A row is one whose first field is
		// a node ID.
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "Nodeid" {
			continue
		}
		nodeID, err := strconv.Atoi(fields[0])
		if err != nil || nodeID <= 0 {
			continue
		}
		name := fields[len(fields)-1]
		if name == "(local)" {
			name = fields[len(fields)-2]
		}
		q.Hosts = append(q.Hosts, name)
	}
	if len(q.Hosts) == 0 {
		return PoolQuorum{}, errors.New("corosync reported no membership for this pool")
	}
	return q, nil
}
