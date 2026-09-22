// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Two Core-owned things live here, both about a host shutting itself out.
//
// What each host has. A watchdog is on the machine it would reset, so only that host can say
// what it has — and it has to have said so before it is lost, because the master answers the
// watchdog method for a host that is gone. Detection is sysfs only and never opens the device
// (see vmhost.LocalWatchdog); a capability that has not changed is not reported at all, so a
// settled pool does not re-sign and re-push its authority because a pass ran.
//
// What the pool decided. Host fencing is one pool-wide setting, so it is a decision rather
// than an observation: the master keeps it in the membership it owns, it replicates with
// everything else, and a fresh Controller reads it back from the master with the membership.
// It is on for a pool that has never been told otherwise, which is why the durable field is
// the negative one — a pool recorded before the setting existed keeps the default.

import (
	"context"
	"errors"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// localWatchdog is this host's own detection, as a package variable so a lab can stand in for
// a machine's watchdog the way localSystemUUID stands in for its SMBIOS identity.
var localWatchdog = func(*Store) protocol.PoolHostWatchdog { return vmhost.LocalWatchdog() }

// ReportPoolWatchdog keeps this host's own watchdog capability in the master's membership
// record. The master writes its own row; a member advertises, exactly as it does its machine
// identity. It runs on every reconcile pass, so a pool formed before this existed gains the
// capability of every host that is still alive without re-joining anything.
func ReportPoolWatchdog(ctx context.Context, s *Store) error {
	report := localWatchdog(s)
	if err := report.Valid(); err != nil {
		return err
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	master, current := "", (*protocol.PoolHostWatchdog)(nil)
	if err == nil && state.Membership.Valid() == nil {
		master = state.Membership.MasterHostID
		if member, ok := poolMemberByID(state.Membership, s.State.HostID); ok {
			current = member.Watchdog
		}
		if master == s.State.HostID && !samePoolWatchdogReport(current, report) {
			// Like a peer origin this is a recorded observation, not a membership
			// change, so it bumps no membership generation and waits out a handover
			// rather than writing into one.
			if err = poolAuthorityWritable(state); err == nil {
				err = applyPoolWatchdogReport(s, &state, s.State.HostID, report)
			}
		}
	}
	poolPeerMu.Unlock()
	if err != nil || master == "" || master == s.State.HostID || samePoolWatchdogReport(current, report) {
		return err
	}
	return postPoolAdvertise(ctx, s, map[string]any{"watchdog": report})
}

// applyPoolWatchdogReport writes one host's capability into the master's membership. It is an
// observation about that host's own hardware and nothing more: it cannot change what the pool
// decided about the host, and an unchanged report is not written at all.
func applyPoolWatchdogReport(s *Store, state *poolPeerState, hostID string, report protocol.PoolHostWatchdog) error {
	for i := range state.Membership.Members {
		member := &state.Membership.Members[i]
		if member.HostID != hostID {
			continue
		}
		if samePoolWatchdogReport(member.Watchdog, report) {
			return nil
		}
		member.Watchdog = &report
		return savePoolPeer(s, *state)
	}
	return errors.New("no such member of this pool")
}

func samePoolWatchdogReport(recorded *protocol.PoolHostWatchdog, report protocol.PoolHostWatchdog) bool {
	return recorded != nil && *recorded == report
}

// ExecutePoolHostFencing records the pool's Host fencing setting. Like every other pool
// decision it is the master's: the Controller relays the operator's request and keeps nothing,
// and each host reads the setting from the pool records it already holds.
//
// Turning it on does not arm anything by itself. Nex Core arms a watchdog only where the
// prerequisites it reports with the membership hold, and it reports them whether or not the
// switch is ever opened.
func ExecutePoolHostFencing(s *Store, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	v := cmd.Pool
	if v == nil || v.Action != "host-fencing" {
		result.Error = "this command carries no host fencing request"
		return result
	}
	if err := v.Valid(); err != nil {
		result.Error = err.Error()
		return result
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil {
		result.Error = "pool membership is unavailable"
		return result
	}
	if state.Membership.MasterHostID != s.State.HostID || v.Membership.MasterHostID != s.State.HostID {
		result.Error = "only the pool master records whether host fencing is on"
		return result
	}
	if err = poolAuthorityWritable(state); err != nil {
		result.Error = err.Error()
		return result
	}
	if state.Membership.HostFencingDisabled == !v.Enabled {
		result.Status = "succeeded" // A desired state, not a transition.
		return result
	}
	state.Membership.HostFencingDisabled = !v.Enabled
	if err = savePoolPeer(s, state); err != nil {
		result.Status, result.Error = "needs_attention", "this decision could not be recorded durably"
		return result
	}
	result.Status = "succeeded"
	return result
}
