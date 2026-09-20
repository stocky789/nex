// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Maintenance mode is a per-host drain flag the pool master keeps inside the membership
// record it already owns (pool-peer.json). A fresh Controller re-adopts it from the master's
// reported Inventory.Managed.Membership with no operator re-entry, and a master restart
// reloads it from the file; nothing about it lives in a Controller database.
//
// It refuses *new* work on the drained host — a new VM placement, a definition transition and
// a start — and refuses nothing else. Running guests keep running, stop, shutdown and
// force-stop are never blocked (an operator draining a host is usually about to power guests
// off), local recovery is untouched and a member's own convergence continues. A placement
// already admitted when the flag is set completes: this is drain intent, not a fence, and the
// only thing that may never be uncertain is execution ownership, which maintenance does not
// touch. It is also outside the membership generation rule, so draining a host cannot
// invalidate an in-flight placement or a member's published inventory.
//
// The master is the authority. A member checks the flag too, but only as an early, honest
// refusal, and every path a member takes ends at the master's own record anyway. A member's
// own pool-peer.json membership is written at join and at promotion and never again, so the
// drain flag in it is stale for good; its freshest verified copy is the signed authority
// replica the master pushes, which a maintenance change does move (the flag is part of the
// replicated membership content, even though the membership generation deliberately does not
// advance). That copy is at most one master probe interval old while the master is reachable,
// and as old as the last successful push while it is not — which is why a member's refusal is
// an early one, never the decision: the master refuses again from its own record, and a
// local-storage start on a member whose master is unreachable is still allowed to proceed on
// the member's last-known copy, because local power must keep working with the master down.

import (
	"context"
	"errors"
	"time"

	"nex.local/nex/internal/protocol"
)

const poolMaintenanceReplicaPushTimeout = 20 * time.Second

var pushPoolAuthorityReplicasAfterMaintenance = PushPoolAuthorityReplicas

// maintenanceRefused is the one sentence every refusal carries, so a Controller can map a
// maintenance refusal (in a command result or a placement preflight) to one message without
// parsing several. It stays inside the placement error bound (placement.go:106).
const maintenanceRefused = "the selected VM host is in maintenance mode; take it out of maintenance before placing or starting VMs on it"

// poolMaintenanceRefusal answers from an already-loaded membership, so no caller has to take
// the peer lock while it holds the VM lock.
func poolMaintenanceRefusal(membership protocol.PoolMembership, hostID string) error {
	for _, member := range membership.Members {
		if member.HostID == hostID && member.Maintenance {
			return errors.New(maintenanceRefused)
		}
	}
	return nil
}

// poolHostInMaintenance reads this host's freshest known membership. It is used where no
// membership is loaded yet, and never while the peer lock is held.
func poolHostInMaintenance(s *Store, hostID string) error {
	membership, err := Membership(s)
	if err != nil {
		// Not a maintenance refusal: the caller's own membership requirement decides.
		return nil
	}
	return poolMaintenanceRefusal(maintenanceMembershipFor(s, membership), hostID)
}

// maintenanceMembershipFor answers with the freshest membership this host can verify. On the
// master that is its own record. On a member it is the stored authority replica, which the
// master signed and this host verified before storing: the member's own copy of the
// membership never changes after the join, so reading only that would make every member-side
// maintenance check dead code. Nothing is merged — the replica is read for this refusal and
// the member's own record is left exactly as it is.
func maintenanceMembershipFor(s *Store, own protocol.PoolMembership) protocol.PoolMembership {
	if own.MasterHostID == s.State.HostID {
		return own
	}
	record, err := loadPoolReplica(s)
	if err != nil || record == nil || !record.Verified {
		return own
	}
	replica := record.Replica.Membership
	if replica.PoolID != own.PoolID || replica.MasterHostID != own.MasterHostID || replica.Valid() != nil {
		return own
	}
	return replica
}

// ExecutePoolMaintenance records the drain intent on the pool master. It is a definite result
// either way — the flag is a desired state, so re-sending the state a host is already in is a
// success, which is what makes an exact retry after a lost reply safe.
func ExecutePoolMaintenance(s *Store, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	v := cmd.Pool
	if v == nil || v.Action != "maintenance" {
		result.Error = "maintenance command carries no maintenance request"
		return result
	}
	if err := v.Valid(); err != nil {
		result.Error = err.Error()
		return result
	}
	poolPeerMu.Lock()
	unlock := func() { poolPeerMu.Unlock() }
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil {
		unlock()
		result.Error = "pool membership is unavailable"
		return result
	}
	if state.Membership.MasterHostID != s.State.HostID || v.Membership.MasterHostID != s.State.HostID {
		unlock()
		result.Error = "only the pool master records maintenance mode"
		return result
	}
	// A drain is a durable membership write, so it waits for a master change in flight. The
	// refusal is checked against the bytes this call is about to rewrite, under the lock it
	// already holds.
	if err = poolAuthorityWritable(state); err != nil {
		unlock()
		result.Error = err.Error()
		return result
	}
	if v.ExpectedVersion != 0 && v.ExpectedVersion != state.Membership.Generation {
		unlock()
		result.Error = "pool membership changed since this request was composed; review the pool and try again"
		return result
	}
	index := -1
	for i, member := range state.Membership.Members {
		if member.HostID == v.MemberHostID {
			index = i
		}
	}
	if index < 0 {
		unlock()
		result.Error = "no such member of this pool"
		return result
	}
	if state.Membership.Members[index].Status == "ejected" {
		unlock()
		result.Error = "an ejected host has no maintenance state"
		return result
	}
	changed := false
	if state.Membership.Members[index].Maintenance != v.Enabled {
		state.Membership.Members[index].Maintenance = v.Enabled
		state.Membership.Members[index].MaintenanceSince = time.Time{}
		if v.Enabled {
			state.Membership.Members[index].MaintenanceSince = time.Now().UTC()
		}
		// The generation is deliberately not bumped: a drain is not a membership change,
		// and bumping it would invalidate every in-flight placement and published
		// inventory in the pool for a flag no peer check depends on.
		if err = savePoolPeer(s, state); err != nil {
			unlock()
			// The flag may or may not be on disk; the next inventory report states the
			// truth, and an exact retry is safe either way.
			result.Status, result.Error = "needs_attention", "maintenance mode could not be recorded durably"
			return result
		}
		changed = true
	}
	unlock()
	result.Status = "succeeded"
	if changed {
		// Push the changed membership content now rather than waiting for the next probe
		// tick, so a drained member starts refusing from its own verified copy within
		// seconds instead of within a minute.
		//
		// The push is best effort and its outcome is not this operation's outcome. What
		// the operator asked for is done the moment the master's own record is saved:
		// that record is the authority every placement, definition and start is refused
		// by, and the member's copy is an early refusal on top of it, never the decision.
		// A member that was unreachable for this push catches up from the next tick, the
		// same way it does for every other authority write — none of which fail because a
		// member was down. Reporting the shortfall as the drain's result asked an operator
		// to act on something no action would change; the lag itself stays visible per
		// member in the members projection's replica status and connection.
		_ = pushPoolAuthorityReplicasAfterMaintenance(context.Background(), s, poolMaintenanceReplicaPushTimeout)
	}
	return result
}
