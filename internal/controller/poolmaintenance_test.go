// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// joinedMembership is what a master reports once the member has joined: one record, two
// hosts, and the drain flag the master alone owns.
func (l *joinLab) joinedMembership(drained bool, since time.Time) *protocol.PoolMembership {
	return &protocol.PoolMembership{PoolID: l.master.pool, MasterHostID: l.master.id, Generation: 2, Members: []protocol.PoolMember{
		{HostID: l.master.id, CertificateSHA256: labCertificateHash, Generation: 2, Status: "ready", ObservedAt: time.Now().UTC()},
		{HostID: l.member.id, CertificateSHA256: labCertificateHash, Generation: 2, Status: "ready", ObservedAt: time.Now().UTC(), Maintenance: drained, MaintenanceSince: since},
	}}
}

// drainLab is a joined pool whose master supports maintenance mode.
func newDrainLab(t *testing.T) *joinLab {
	t.Helper()
	l := newJoinLab(t)
	l.master.caps = append(l.master.caps, protocol.PoolMaintenanceCapability, protocol.PoolVMPlacementCapability)
	l.member.caps = append(l.member.caps, protocol.PoolMaintenanceCapability, protocol.PoolVMPlacementCapability)
	l.master.inv.Managed.Membership = l.joinedMembership(false, time.Time{})
	l.member.inv.Managed.Membership = l.joinedMembership(false, time.Time{})
	l.member.inv.VMs = []protocol.VM{{ID: "vm-1", Name: "build", State: "running"}, {ID: "vm-2", Name: "db", State: "shut off"}}
	// Reported VMs are carried by the host's libvirt observation, which is what makes them
	// the Controller's mirror of that host's guests.
	l.member.inv.Checks = []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: time.Now().UTC()}}
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	return l
}

// forget makes a host stale without touching its stored report, the way a host that stops
// reaching the Controller looks.
func (l *joinLab) forget(h *joinHost) {
	l.a.c.mu.Lock()
	delete(l.a.c.seen, h.id)
	l.a.c.mu.Unlock()
}

func (l *joinLab) maintenancePath() string {
	return "/api/v1/pools/" + l.master.pool + "/members/" + l.member.id + "/maintenance"
}

func (l *joinLab) members(t *testing.T) poolMembersView {
	t.Helper()
	var out poolMembersView
	read(t, l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200), &out)
	return out
}

func (l *joinLab) memberView(t *testing.T, host string) poolMemberView {
	t.Helper()
	for _, member := range l.members(t).Members {
		if member.HostID == host {
			return member
		}
	}
	t.Fatalf("no projection for %s", host)
	return poolMemberView{}
}

// TestPoolMaintenanceCommandGoesToTheMaster is the whole relay: the drained host is named in
// the command, but the command is addressed to the master, which owns the record.
func TestPoolMaintenanceCommandGoesToTheMaster(t *testing.T) {
	l := newDrainLab(t)
	var accepted struct {
		HostID    string             `json:"host_id"`
		Enabled   bool               `json:"enabled"`
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.maintenancePath(), map[string]any{"request_id": "drain-1", "enabled": true, "expected_version": 2}, nil, 202), &accepted)
	if accepted.HostID != l.member.id || !accepted.Enabled || accepted.Operation.Kind != "pool-maintenance" || accepted.Operation.HostID != l.master.id {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	if commands := l.send(&l.member, nil).Commands; len(commands) != 0 {
		t.Fatalf("the drained host was sent the command: %+v", commands)
	}
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Pool == nil {
		t.Fatalf("master did not receive the maintenance command: %+v", commands)
	}
	cmd := commands[0].Pool
	if commands[0].Kind != "pool-maintenance" || cmd.Action != "maintenance" || cmd.MemberHostID != l.member.id || !cmd.Enabled || cmd.ExpectedVersion != 2 {
		t.Fatalf("unexpected command: %+v", cmd)
	}
	// Only the routing target is on the wire: the Controller composes no membership.
	if cmd.Membership.MasterHostID != l.master.id || cmd.Membership.PoolID != "" || cmd.Membership.Generation != 0 || cmd.Membership.Members != nil {
		t.Fatalf("Controller composed a membership: %+v", cmd.Membership)
	}
	if err := cmd.Valid(); err != nil {
		t.Fatalf("delivered command is invalid: %v", err)
	}
	// Exact retry replays; a changed payload for the same request_id is a conflict.
	var replay struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.maintenancePath(), map[string]any{"request_id": "drain-1", "enabled": true, "expected_version": 2}, nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exact retry did not replay: %+v", replay)
	}
	l.a.call("POST", l.maintenancePath(), map[string]any{"request_id": "drain-1", "enabled": false}, nil, 409)
	// The audit row names the action and the drained host, and carries no other payload.
	var audits []protocol.Audit
	read(t, l.a.call("GET", "/api/v1/audit", nil, nil, 200), &audits)
	rows := 0
	for _, entry := range audits {
		if entry.Action == "pool-maintenance" {
			rows++
			if entry.ResourceID != "pool-maintenance:"+l.member.id+":"+accepted.Operation.ID || entry.Detail != "" {
				t.Fatalf("unexpected audit row: %+v", entry)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("expected one pool-maintenance audit row, got %d", rows)
	}
}

func TestPoolMaintenanceRefusalMatrix(t *testing.T) {
	l := newDrainLab(t)
	body := func(id string) map[string]any { return map[string]any{"request_id": id, "enabled": true} }
	// 400: no request_id.
	l.a.call("POST", l.maintenancePath(), map[string]any{"enabled": true}, nil, 400)
	// 404: unknown pool and unknown host.
	l.a.call("POST", "/api/v1/pools/no-such-pool/members/"+l.member.id+"/maintenance", body("missing-pool"), nil, 404)
	l.a.call("POST", "/api/v1/pools/"+l.master.pool+"/members/no-such-host/maintenance", body("missing-host"), nil, 404)
	// 409: a host that is not a member of this pool's reported membership.
	l.master.inv.Managed.Membership = selfMembership(l.master.id, l.master.pool)
	l.send(&l.master, nil)
	l.a.call("POST", l.maintenancePath(), body("not-a-member"), nil, 409)
	l.master.inv.Managed.Membership = l.joinedMembership(false, time.Time{})
	l.send(&l.master, nil)
	// 409: the generation the operator saw is not the one the master reports.
	l.a.call("POST", l.maintenancePath(), map[string]any{"request_id": "stale-version", "enabled": true, "expected_version": 1}, nil, 409)
	// 409: the master no longer advertises the capability.
	l.master.caps = []string{protocol.ManagedVMCapability, protocol.PoolJoinCapability}
	l.send(&l.master, nil)
	w := l.a.call("POST", l.maintenancePath(), body("no-capability"), nil, 409)
	if !strings.Contains(w.Body.String(), protocol.PoolMaintenanceCapability) {
		t.Fatalf("capability refusal does not name the capability: %s", w.Body.String())
	}
	l.master.caps = append(l.master.caps, protocol.PoolMaintenanceCapability)
	l.send(&l.master, nil)
	// 409: the master is not reporting, so nothing can be recorded.
	l.forget(&l.master)
	l.a.call("POST", l.maintenancePath(), body("stale-master"), nil, 409)
}

// TestPoolMaintenanceProjection checks the three honesty rules: an unreported flag is
// unknown, an unreported host has an unknown guest list, and a Core conflict is marked.
func TestPoolMaintenanceProjection(t *testing.T) {
	l := newDrainLab(t)
	if !l.members(t).MaintenanceSupported {
		t.Fatal("the master advertises pool-maintenance-v1 but the projection says otherwise")
	}
	view := l.memberView(t, l.member.id)
	if view.Maintenance == nil || *view.Maintenance {
		t.Fatalf("expected a reported, undrained member: %+v", view.Maintenance)
	}
	if !view.Guests.Known || view.Guests.Total != 2 || view.Guests.Running != 1 || len(view.Guests.Names) != 2 {
		t.Fatalf("unexpected guest list: %+v", view.Guests)
	}
	// The master records the drain; the projection reports it with its timestamp.
	since := time.Now().UTC().Truncate(time.Second)
	l.master.inv.Managed.Membership = l.joinedMembership(true, since)
	l.send(&l.master, nil)
	view = l.memberView(t, l.member.id)
	if view.Maintenance == nil || !*view.Maintenance || view.MaintenanceSince != since.Format(time.RFC3339) {
		t.Fatalf("drain was not projected: %+v %q", view.Maintenance, view.MaintenanceSince)
	}
	// A host that is not reporting has no guest list: unknown, never none.
	l.forget(&l.member)
	view = l.memberView(t, l.member.id)
	if view.Guests.Known || view.Guests.Total != 0 || len(view.Guests.Names) != 0 {
		t.Fatalf("a quiet host reported a guest list: %+v", view.Guests)
	}
	if view.Maintenance == nil || !*view.Maintenance {
		t.Fatal("the master is still fresh; its record must still be projected")
	}
	// A master that is not reporting makes the flag unknown, not false.
	l.forget(&l.master)
	if view = l.memberView(t, l.master.id); view.Maintenance != nil {
		t.Fatalf("a quiet master's flag was projected as known: %+v", view.Maintenance)
	}
}

// TestPoolMaintenanceCoreConflictIsSurfaced: Core's membership conflict arrives as a failed
// operation, and the projection marks it as the conflict it is.
func TestPoolMaintenanceCoreConflictIsSurfaced(t *testing.T) {
	l := newDrainLab(t)
	var accepted struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.maintenancePath(), map[string]any{"request_id": "drain-1", "enabled": true}, nil, 202), &accepted)
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 {
		t.Fatalf("master did not receive the command: %+v", commands)
	}
	l.send(&l.master, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "failed", Error: poolMaintenanceConflicts[0], ObservedAt: time.Now().UTC()}}
	})
	last := l.memberView(t, l.member.id).LastMaintenance
	if last == nil || last.OperationID != accepted.Operation.ID || last.Status != "failed" || !last.Conflict || last.Error != poolMaintenanceConflicts[0] {
		t.Fatalf("Core's conflict was not surfaced: %+v", last)
	}
	// A refusal that is not a membership conflict is a plain failure.
	read(t, l.a.call("POST", l.maintenancePath(), map[string]any{"request_id": "drain-2", "enabled": true}, nil, 202), &accepted)
	commands = l.send(&l.master, nil).Commands
	l.send(&l.master, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "failed", Error: "no such member of this pool", ObservedAt: time.Now().UTC()}}
	})
	if last = l.memberView(t, l.member.id).LastMaintenance; last == nil || last.Conflict {
		t.Fatalf("a plain refusal was marked as a conflict: %+v", last)
	}
}

// TestPoolMaintenanceStopsOfferingADrainedHost: presentation only. The Controller declines
// to offer a drained host as a placement target; Core refuses it again regardless.
func TestPoolMaintenanceStopsOfferingADrainedHost(t *testing.T) {
	l := newDrainLab(t)
	check := func() error {
		tx, err := l.a.c.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return l.a.c.placementHost(tx, l.master.pool, l.member.id)
	}
	// The member reports its own pool as its former one until it re-homes; align the mirror
	// with the joined membership the master reports.
	if _, err := l.a.c.db.Exec("UPDATE hosts SET pool_id=? WHERE id=?", l.master.pool, l.member.id); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatalf("an undrained member was refused: %v", err)
	}
	l.master.inv.Managed.Membership = l.joinedMembership(true, time.Now().UTC())
	l.send(&l.master, nil)
	err := check()
	if err == nil || !strings.Contains(err.Error(), "is in maintenance mode") {
		t.Fatalf("a drained member was still offered: %v", err)
	}
	// A master that is not reporting cannot be the source of a refusal: unknown is unknown,
	// and Core decides.
	l.forget(&l.master)
	if err = check(); err != nil {
		t.Fatalf("a stale mirror refused a placement: %v", err)
	}
}

func TestPoolMaintenanceIsIdempotentByRequestID(t *testing.T) {
	l := newDrainLab(t)
	first := l.a.call("POST", l.maintenancePath(), map[string]any{"request_id": "drain-1", "enabled": true}, nil, 202)
	second := l.a.call("POST", l.maintenancePath(), map[string]any{"request_id": "drain-1", "enabled": true}, nil, 200)
	if first.Body.String() == "" || second.Body.String() == "" {
		t.Fatal("empty acceptance body")
	}
	var count int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='pool-maintenance'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("a retry created another command: %d (%v)", count, err)
	}
	if code := l.a.call("POST", l.maintenancePath(), map[string]any{"request_id": "drain-1", "enabled": true}, nil, 200).Code; code != http.StatusOK {
		t.Fatalf("unexpected retry status %d", code)
	}
}
