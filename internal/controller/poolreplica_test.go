// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// newReplicaLab is a joined pool whose hosts both support the signed authority replica.
func newReplicaLab(t *testing.T) *joinLab {
	t.Helper()
	l := newJoinLab(t)
	l.master.inv.Managed.Membership = l.joinedMembership(false, time.Time{})
	l.member.inv.Managed.Membership = l.joinedMembership(false, time.Time{})
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	return l
}

// setReplica makes both hosts report one authority-replica situation: what the master last
// saw the member acknowledge, and what the member itself says it holds.
func (l *joinLab) setReplica(ackGeneration uint64, ackError string, held *protocol.PoolReplicaStatus) {
	membership := l.joinedMembership(false, time.Time{})
	member := &membership.Members[1]
	member.ReplicaGeneration, member.ReplicaError, member.ReplicaDigest = ackGeneration, ackError, "9f2c"
	if ackGeneration != 0 || ackError != "" {
		member.ReplicaObservedAt = time.Now().UTC()
	}
	l.master.inv.Managed.Membership = membership
	l.member.inv.Managed.Replica = held
	l.send(&l.master, nil)
	l.send(&l.member, nil)
}

func heldReplica(generation uint64, verified bool, err string) *protocol.PoolReplicaStatus {
	return &protocol.PoolReplicaStatus{Generation: generation, Digest: "9f2c", Verified: verified, Error: err, ReceivedAt: time.Now().UTC(), SignedAt: time.Now().UTC()}
}

func (l *joinLab) replica(t *testing.T, host string) poolMemberReplica {
	t.Helper()
	view := l.memberView(t, host)
	if view.Replica == nil {
		t.Fatalf("no replica projection for %s: %+v", host, view)
	}
	return *view.Replica
}

// TestPoolMemberReplicaStates walks every state the projection can report, from the two host
// reports it is allowed to read and nothing else.
func TestPoolMemberReplicaStates(t *testing.T) {
	l := newReplicaLab(t)

	// Nothing has been pushed yet: honest, not healthy.
	l.setReplica(0, "", nil)
	if got := l.replica(t, l.member.id); got.State != "awaiting" || got.Generation != 0 || got.AcknowledgedGeneration != 0 {
		t.Fatalf("awaiting: %+v", got)
	}

	// Both sides agree at one generation, verified, no error.
	l.setReplica(7, "", heldReplica(7, true, ""))
	got := l.replica(t, l.member.id)
	if got.State != "current" || got.Generation != 7 || got.AcknowledgedGeneration != 7 || !got.Verified || got.ReceivedAt == "" || got.AcknowledgedAt == "" {
		t.Fatalf("current: %+v", got)
	}

	// The member says it holds 8, the master only ever saw 7: the two reports disagree,
	// so neither is evidence that this member is current.
	l.setReplica(7, "", heldReplica(8, true, ""))
	if got := l.replica(t, l.member.id); got.State != "behind" || got.Generation != 8 || got.AcknowledgedGeneration != 7 {
		t.Fatalf("member ahead of the master's observation: %+v", got)
	}
	// ...and the other way round.
	l.setReplica(9, "", heldReplica(8, true, ""))
	if got := l.replica(t, l.member.id); got.State != "behind" {
		t.Fatalf("member behind the master's acknowledgement: %+v", got)
	}

	// Bytes the member has not proven against the pool CA are never current.
	l.setReplica(7, "", heldReplica(7, false, ""))
	if got := l.replica(t, l.member.id); got.State != "unverified" {
		t.Fatalf("unverified: %+v", got)
	}

	// A master-reported refusal wins over everything but freshness.
	l.setReplica(0, "pool member did not report its authority replica", heldReplica(7, true, ""))
	if got := l.replica(t, l.member.id); got.State != "refused" || got.Error == "" {
		t.Fatalf("master refusal: %+v", got)
	}
	// So does the member's own reported refusal.
	l.setReplica(7, "", heldReplica(7, true, "pool authority replica section digest does not match"))
	if got := l.replica(t, l.member.id); got.State != "refused" || got.Error != "pool authority replica section digest does not match" {
		t.Fatalf("member refusal: %+v", got)
	}

	// The master's row carries no replica: it holds the authority, not a copy of it.
	if master := l.memberView(t, l.master.id); master.Replica != nil {
		t.Fatalf("master row carries a replica: %+v", master.Replica)
	}
}

// TestPoolMemberReplicaUnknownWhenEitherSideIsStale is the rule that matters most: a replica
// that was current an hour ago is not evidence of anything now.
//
// S6 narrows it to the half it is actually about. A member that is not reporting says
// nothing at all. A member that is reporting still says what it holds, whatever the master
// is doing — that report is the evidence for choosing a promotion candidate when the master
// is the host that is gone. What the master observed about that replica, and the verdict
// current/behind that compares the two, stay unknown without a fresh master.
func TestPoolMemberReplicaUnknownWhenEitherSideIsStale(t *testing.T) {
	for _, stale := range []string{"master", "member"} {
		t.Run(stale, func(t *testing.T) {
			l := newReplicaLab(t)
			l.setReplica(7, "", heldReplica(7, true, ""))
			if got := l.replica(t, l.member.id); got.State != "current" {
				t.Fatalf("precondition: %+v", got)
			}
			if stale == "master" {
				l.forget(&l.master)
			} else {
				l.forget(&l.member)
			}
			got := l.replica(t, l.member.id)
			if got.State != "unknown" || got.AcknowledgedGeneration != 0 || got.AcknowledgedAt != "" {
				t.Fatalf("stale %s still reported the master's observation: %+v", stale, got)
			}
			if stale == "member" && (got.Generation != 0 || got.ReceivedAt != "" || got.Digest != "" || got.Verified) {
				t.Fatalf("a member that is not reporting still reported a replica: %+v", got)
			}
			if stale == "master" && (got.Generation != 7 || got.Digest == "" || !got.Verified || got.ReceivedAt == "") {
				t.Fatalf("a reporting member's own replica was blanked by a quiet master: %+v", got)
			}
		})
	}
}

// TestPoolMemberReplicaCheckIsProjected: the member's own pool-authority-replica health check
// is carried in the same per-member checks list as the three convergence checks.
func TestPoolMemberReplicaCheckIsProjected(t *testing.T) {
	l := newReplicaLab(t)
	l.member.inv.Checks = []protocol.Check{{Name: "pool-authority-replica", Status: "failed", ObservedAt: time.Now().UTC(), Error: "pool authority replica observation is stale"}}
	l.setReplica(7, "", heldReplica(7, true, ""))
	found := false
	for _, check := range l.memberView(t, l.member.id).Checks {
		if check.Name != "pool-authority-replica" {
			continue
		}
		found = true
		if check.Status != "failed" || check.Detail != "pool authority replica observation is stale" {
			t.Fatalf("replica check: %+v", check)
		}
	}
	if !found {
		t.Fatal("pool-authority-replica is missing from the member's checks")
	}
	// S6: a stale master proves nothing about the member's checks — in either direction.
	// They are the member's own report and stay exactly as the member reported them.
	l.forget(&l.master)
	checks := l.memberView(t, l.member.id).Checks
	kept := false
	for _, check := range checks {
		if check.Name == "pool-authority-replica" {
			kept = check.Status == "failed" && check.Detail == "pool authority replica observation is stale"
		}
	}
	if !kept {
		t.Fatalf("a quiet master erased the member's own checks: %+v", checks)
	}
}

// TestPoolMemberReplicaPeerLagIsBehind covers the one input the HTTP lab cannot produce with
// two hosts: a peer of the same pool acknowledged at a higher generation.
func TestPoolMemberReplicaPeerLagIsBehind(t *testing.T) {
	host := hostRow{id: "core-02", connection: "fresh",
		inventory: protocol.Inventory{Managed: &protocol.ManagedState{Replica: heldReplica(5, true, "")}}}
	member := protocol.PoolMember{HostID: "core-02", ReplicaGeneration: 5, ReplicaObservedAt: time.Now().UTC()}
	if got := memberReplica(true, host, member, 5, 0); got.State != "current" {
		t.Fatalf("highest generation in the pool: %+v", got)
	}
	if got := memberReplica(true, host, member, 6, 0); got.State != "behind" {
		t.Fatalf("a peer holds a newer replica: %+v", got)
	}
	// S5: the master now reports its own authority generation, so a member that agrees
	// with the master's observation but lags the authority itself is behind.
	if got := memberReplica(true, host, member, 5, 5); got.State != "current" {
		t.Fatalf("at the master's reported authority generation: %+v", got)
	}
	if got := memberReplica(true, host, member, 5, 6); got.State != "behind" {
		t.Fatalf("below the master's reported authority generation: %+v", got)
	}
}

// TestPoolMemberReplicaProjectionPersistsNothing: the whole slice is a projection. Reading it
// must not add an operation, a managed resource or an audit row.
func TestPoolMemberReplicaProjectionPersistsNothing(t *testing.T) {
	l := newReplicaLab(t)
	l.setReplica(7, "", heldReplica(7, true, ""))
	counts := func() [3]int {
		t.Helper()
		var out [3]int
		for i, table := range []string{"operations", "managed_resources", "audit"} {
			if err := l.a.c.db.QueryRow("SELECT count(*) FROM " + table).Scan(&out[i]); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	before := counts()
	if got := l.replica(t, l.member.id); got.State != "current" {
		t.Fatalf("precondition: %+v", got)
	}
	l.members(t)
	if after := counts(); after != before {
		t.Fatalf("the replica projection wrote rows: %v -> %v", before, after)
	}
}
