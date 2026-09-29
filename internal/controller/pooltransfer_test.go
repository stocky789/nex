// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// transferMembership is what the master reports for the lab pool: every host ready, at one
// generation, with the peer origin a promotion target must have.
func (l *joinLab) transferMembership(generation uint64, master string, extra ...joinHost) *protocol.PoolMembership {
	out := &protocol.PoolMembership{PoolID: l.master.pool, MasterHostID: master, Generation: generation}
	for _, h := range append([]joinHost{l.master, l.member}, extra...) {
		out.Members = append(out.Members, protocol.PoolMember{HostID: h.id, Hostname: h.id, CertificateSHA256: labCertificateHash, Generation: generation, Status: "ready", ObservedAt: time.Now().UTC(), PeerOrigin: "https://192.0.2.12:9444"})
	}
	return out
}

func labAuthority(generation uint64) *protocol.PoolAuthorityRecord {
	return &protocol.PoolAuthorityRecord{Generation: generation, Digest: "9f2c", ObservedAt: time.Now().UTC()}
}

// newTransferLab is a joined two-host pool with the master reporting its own authority
// generation.
func newTransferLab(t *testing.T) *joinLab {
	t.Helper()
	l := newJoinLab(t)
	membership := l.transferMembership(2, l.master.id)
	l.master.inv.Managed.Membership, l.member.inv.Managed.Membership = membership, membership
	l.master.inv.Managed.Authority = labAuthority(9)
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	if got := l.a.poolOf(l.member.id); got != l.master.pool {
		t.Fatalf("the member reports the joined membership but was not adopted into the pool: %s", got)
	}
	return l
}

// joinThird enrolls a third host into the lab pool.
func (l *joinLab) joinThird(t *testing.T) joinHost {
	t.Helper()
	var site string
	if err := l.a.c.db.QueryRow("SELECT site_id FROM pools WHERE id=?", l.master.pool).Scan(&site); err != nil {
		t.Fatal(err)
	}
	enrolled := l.a.enroll(l.a.token(site, ""), csr(t))
	third := joinHost{id: enrolled.HostID, cert: certificate(t, enrolled.CertificatePEM), pool: l.a.poolOf(enrolled.HostID), caps: []string{protocol.ManagedVMCapability}}
	third.inv = protocol.Inventory{Hostname: "core", AgentVersion: "0.3.5", NICs: labNICs("nex0"), Managed: &protocol.ManagedState{}}
	membership := l.transferMembership(3, l.master.id, third)
	l.master.inv.Managed.Membership, l.member.inv.Managed.Membership = membership, membership
	third.inv.Managed.Membership = membership
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	l.send(&third, nil)
	if got := l.a.poolOf(third.id); got != l.master.pool {
		t.Fatalf("the third host was not adopted into the pool: %s", got)
	}
	return third
}

func (l *joinLab) masterPath() string { return "/api/v1/pools/" + l.master.pool + "/master" }
func (l *joinLab) ejectPath() string {
	return "/api/v1/pools/" + l.master.pool + "/members/" + l.member.id
}
func (l *joinLab) poolName(t *testing.T) string {
	t.Helper()
	var name string
	if err := l.a.c.db.QueryRow("SELECT name FROM pools WHERE id=?", l.master.pool).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

type transferAcceptance struct {
	HostID    string             `json:"host_id"`
	Action    string             `json:"action"`
	Operation protocol.Operation `json:"operation"`
}

// TestPoolMasterChangeRelaysOneCommandToTheMaster is the whole relay: one durable command,
// addressed to the current master, naming the host that is to take over.
func TestPoolMasterChangeRelaysOneCommandToTheMaster(t *testing.T) {
	l := newTransferLab(t)
	body := map[string]any{"request_id": "change-1", "host_id": l.member.id, "expected_version": 2, "confirmation": l.poolName(t)}
	var accepted transferAcceptance
	read(t, l.a.call("POST", l.masterPath(), body, nil, 202), &accepted)
	if accepted.HostID != l.member.id || accepted.Action != "master-change" || accepted.Operation.Kind != "pool-transfer" || accepted.Operation.HostID != l.master.id {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	// The target is not told anything: the master runs the transfer.
	if commands := l.send(&l.member, nil).Commands; len(commands) != 0 {
		t.Fatalf("the target was sent the command: %+v", commands)
	}
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Pool == nil || commands[0].Kind != "pool-transfer" {
		t.Fatalf("master did not receive the transfer command: %+v", commands)
	}
	cmd := commands[0].Pool
	if cmd.Action != "master-change" || cmd.MemberHostID != l.member.id || cmd.ExpectedVersion != 2 || cmd.RequestID != "change-1" {
		t.Fatalf("unexpected command: %+v", cmd)
	}
	// Only the routing target is on the wire; the Controller composes no membership.
	if cmd.Membership.MasterHostID != l.master.id || cmd.Membership.PoolID != "" || cmd.Membership.Generation != 0 || cmd.Membership.Members != nil {
		t.Fatalf("Controller composed a membership: %+v", cmd.Membership)
	}
	if err := cmd.Valid(); err != nil {
		t.Fatalf("delivered command is invalid: %v", err)
	}
	// Exact retry replays the same operation; a changed payload is a conflict.
	var replay transferAcceptance
	read(t, l.a.call("POST", l.masterPath(), body, nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exact retry did not replay: %+v", replay)
	}
	changed := map[string]any{"request_id": "change-1", "host_id": l.member.id, "confirmation": l.poolName(t)}
	l.a.call("POST", l.masterPath(), changed, nil, 409)
	// One audit row names the action and the pool.
	var audits []protocol.Audit
	read(t, l.a.call("GET", "/api/v1/audit", nil, nil, 200), &audits)
	rows := 0
	for _, entry := range audits {
		if entry.Action == "pool.master-change" {
			rows++
			if entry.ResourceID != l.master.pool {
				t.Fatalf("unexpected audit row: %+v", entry)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("expected one pool.master-change audit row, got %d", rows)
	}
}

// TestPoolEjectRelaysOneCommandToTheMaster: the ejected host may be unreachable, and that
// changes nothing about where the command goes.
func TestPoolEjectRelaysOneCommandToTheMaster(t *testing.T) {
	l := newTransferLab(t)
	l.forget(&l.member)
	var accepted transferAcceptance
	read(t, l.a.call("DELETE", l.ejectPath(), map[string]any{"request_id": "eject-1", "confirmation": "core"}, nil, 202), &accepted)
	if accepted.HostID != l.member.id || accepted.Action != "eject" {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Pool == nil || commands[0].Pool.Action != "eject" || commands[0].Pool.MemberHostID != l.member.id {
		t.Fatalf("master did not receive the eject: %+v", commands)
	}
	if err := commands[0].Pool.Valid(); err != nil {
		t.Fatalf("delivered command is invalid: %v", err)
	}
}

// TestPoolTransferCancel relays the third action, and is deliberately not refused while a
// transfer is in progress: that is what it cancels.
func TestPoolTransferCancel(t *testing.T) {
	l := newTransferLab(t)
	l.master.inv.Managed.Transfer = &protocol.PoolTransferStatus{OperationID: "op-1", Action: "promote", Role: "source", TargetHostID: l.member.id, Stage: "replicate", Status: "pending", Frozen: true, AuthorityGeneration: 9, ObservedAt: time.Now().UTC()}
	l.send(&l.master, nil)
	// A master change is refused while one is running...
	w := l.a.call("POST", l.masterPath(), map[string]any{"request_id": "change-2", "host_id": l.member.id, "confirmation": l.poolName(t)}, nil, 409)
	if !strings.Contains(w.Body.String(), "already in progress") {
		t.Fatalf("unexpected refusal: %s", w.Body.String())
	}
	// ...and the cancel is not.
	var accepted transferAcceptance
	read(t, l.a.call("POST", l.masterPath()+"/cancel", map[string]any{"request_id": "cancel-1", "host_id": l.member.id}, nil, 202), &accepted)
	if accepted.Action != "transfer-cancel" || accepted.Operation.HostID != l.master.id {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Pool == nil || commands[0].Pool.Action != "transfer-cancel" || commands[0].Pool.MemberHostID != l.member.id {
		t.Fatalf("master did not receive the cancel: %+v", commands)
	}
	if err := commands[0].Pool.Valid(); err != nil {
		t.Fatalf("delivered command is invalid: %v", err)
	}
	// The cancel names the target, so it cannot be sent without one.
	l.a.call("POST", l.masterPath()+"/cancel", map[string]any{"request_id": "cancel-2"}, nil, 400)
}

func TestPoolTransferRefusalMatrix(t *testing.T) {
	l := newTransferLab(t)
	name := l.poolName(t)
	change := func(id, host, confirmation string) map[string]any {
		return map[string]any{"request_id": id, "host_id": host, "confirmation": confirmation}
	}
	// 400: no request_id, and a confirmation that is not the pool name.
	l.a.call("POST", l.masterPath(), map[string]any{"host_id": l.member.id, "confirmation": name}, nil, 400)
	w := l.a.call("POST", l.masterPath(), change("wrong-confirmation", l.member.id, "not-the-pool"), nil, 400)
	if !strings.Contains(w.Body.String(), name) {
		t.Fatalf("the confirmation refusal does not name the pool: %s", w.Body.String())
	}
	// 400: the eject confirmation is the ejected host's name.
	w = l.a.call("DELETE", l.ejectPath(), map[string]any{"request_id": "wrong-host-name", "confirmation": "not-the-host"}, nil, 400)
	if !strings.Contains(w.Body.String(), "host name") {
		t.Fatalf("the eject confirmation refusal is not about the host name: %s", w.Body.String())
	}
	// 404: unknown pool, unknown host.
	l.a.call("POST", "/api/v1/pools/no-such-pool/master", change("missing-pool", l.member.id, name), nil, 404)
	l.a.call("POST", l.masterPath(), change("missing-host", "no-such-host", name), nil, 404)
	// 409: the pool master is already the master, and it cannot be ejected either.
	l.a.call("POST", l.masterPath(), change("already-master", l.master.id, name), nil, 409)
	l.a.call("DELETE", "/api/v1/pools/"+l.master.pool+"/members/"+l.master.id, map[string]any{"request_id": "eject-master", "confirmation": "core"}, nil, 409)
	// 409: a host the master's membership does not name.
	outsider := l.joinThird(t)
	l.master.inv.Managed.Membership = l.transferMembership(4, l.master.id)
	l.send(&l.master, nil)
	w = l.a.call("POST", l.masterPath(), change("not-a-member", outsider.id, name), nil, 409)
	if !strings.Contains(w.Body.String(), "no such member") {
		t.Fatalf("unexpected refusal: %s", w.Body.String())
	}
	// 409: a member that is not ready, or has no peer endpoint, cannot take the authority.
	membership := l.transferMembership(5, l.master.id)
	membership.Members[1].PeerOrigin = ""
	l.master.inv.Managed.Membership = membership
	l.send(&l.master, nil)
	w = l.a.call("POST", l.masterPath(), change("no-peer-origin", l.member.id, name), nil, 409)
	if !strings.Contains(w.Body.String(), "ready member with a peer endpoint") {
		t.Fatalf("unexpected refusal: %s", w.Body.String())
	}
	l.master.inv.Managed.Membership = l.transferMembership(5, l.master.id)
	l.send(&l.master, nil)
	// 409: the generation the operator saw is not the one the master reports.
	l.a.call("POST", l.masterPath(), map[string]any{"request_id": "stale-version", "host_id": l.member.id, "confirmation": name, "expected_version": 2}, nil, 409)
	// 409: the target is not reporting, so it cannot acknowledge the authority.
	l.forget(&l.member)
	l.a.call("POST", l.masterPath(), change("stale-target", l.member.id, name), nil, 409)
	l.send(&l.member, nil)
	// 409: the master is not reporting.
	l.forget(&l.master)
	l.a.call("POST", l.masterPath(), change("stale-master", l.member.id, name), nil, 409)
	l.a.call("DELETE", l.ejectPath(), map[string]any{"request_id": "stale-master-eject", "confirmation": "core"}, nil, 409)
	l.send(&l.master, nil)
}

// TestPoolTransferProjection covers what the operator sees: the master's authority and
// transfer, each participant's own view, and nothing at all from a host that is quiet.
func TestPoolTransferProjection(t *testing.T) {
	l := newTransferLab(t)
	view := l.members(t)
	if view.Authority == nil || view.Authority.Generation != 9 || view.Transfer != nil || view.LastTransfer != nil {
		t.Fatalf("unexpected idle projection: %+v", view)
	}
	l.master.inv.Managed.Transfer = &protocol.PoolTransferStatus{OperationID: "op-1", Action: "promote", Role: "source", TargetHostID: l.member.id, Stage: "replicate", Status: "pending", Frozen: true, AuthorityGeneration: 9, ObservedAt: time.Now().UTC()}
	l.member.inv.Managed.Transfer = &protocol.PoolTransferStatus{OperationID: "op-1", Action: "promote", Role: "target", TargetHostID: l.member.id, Stage: "activate", Status: "pending", AuthorityGeneration: 9, ObservedAt: time.Now().UTC()}
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	view = l.members(t)
	if view.Transfer == nil || view.Transfer.Role != "source" || view.Transfer.Stage != "replicate" || !view.Transfer.Frozen {
		t.Fatalf("the master's transfer was not projected: %+v", view.Transfer)
	}
	if got := l.memberView(t, l.member.id).Transfer; got == nil || got.Role != "target" || got.Stage != "activate" {
		t.Fatalf("the target's own view was not projected: %+v", got)
	}
	// A participant that is not reporting says nothing: its last stage is not truth now.
	l.forget(&l.member)
	if got := l.memberView(t, l.member.id).Transfer; got != nil {
		t.Fatalf("a quiet participant still reported a stage: %+v", got)
	}
	l.send(&l.member, nil)
	// Neither does a quiet master, for the pool-level view or the authority.
	l.forget(&l.master)
	if view = l.members(t); view.Transfer != nil || view.Authority != nil {
		t.Fatalf("a quiet master still reported a transfer or an authority: %+v", view)
	}
}

// TestPoolTransferFrozenAuthorityIsRetryable: Core's freeze refusal arrives as a failed
// operation, and it is the one an operator retries unchanged when the transfer completes.
func TestPoolTransferFrozenAuthorityIsRetryable(t *testing.T) {
	l := newTransferLab(t)
	var accepted transferAcceptance
	read(t, l.a.call("POST", l.masterPath(), map[string]any{"request_id": "frozen-1", "host_id": l.member.id, "confirmation": l.poolName(t)}, nil, 202), &accepted)
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 {
		t.Fatalf("master did not receive the command: %+v", commands)
	}
	l.send(&l.master, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "failed", Error: poolAuthorityFrozen, ObservedAt: time.Now().UTC()}}
	})
	last := l.members(t).LastTransfer
	if last == nil || last.OperationID != accepted.Operation.ID || last.Action != "master-change" || !last.Conflict || !last.Retryable {
		t.Fatalf("the frozen refusal was not surfaced as retryable: %+v", last)
	}
	// Another refusal is a conflict but not retryable; an unknown one is neither.
	for _, refusal := range []struct {
		text      string
		conflict  bool
		retryable bool
	}{{"no such member of this pool", true, false}, {"pool transfer state is unavailable", false, false}} {
		id := "frozen-" + refusal.text[:6]
		read(t, l.a.call("POST", l.masterPath(), map[string]any{"request_id": id, "host_id": l.member.id, "confirmation": l.poolName(t)}, nil, 202), &accepted)
		commands = l.send(&l.master, nil).Commands
		l.send(&l.master, func(r *protocol.ReportRequest) {
			r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "failed", Error: refusal.text, ObservedAt: time.Now().UTC()}}
		})
		if last = l.members(t).LastTransfer; last == nil || last.Conflict != refusal.conflict || last.Retryable != refusal.retryable {
			t.Fatalf("%q was marked %+v", refusal.text, last)
		}
	}
}

// TestPoolReplicaBehindAgainstReportedAuthority: with the master reporting its authority
// generation, a member that agrees with the master's observation but lags the authority is
// behind — the case a two-host pool could not show before.
func TestPoolReplicaBehindAgainstReportedAuthority(t *testing.T) {
	l := newTransferLab(t)
	l.master.inv.Managed.Membership.Members[1].ReplicaGeneration = 9
	l.master.inv.Managed.Membership.Members[1].ReplicaObservedAt = time.Now().UTC()
	l.member.inv.Managed.Replica = heldReplica(9, true, "")
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	if got := l.replica(t, l.member.id); got.State != "current" {
		t.Fatalf("at the reported authority generation: %+v", got)
	}
	l.master.inv.Managed.Authority = labAuthority(10)
	l.send(&l.master, nil)
	if got := l.replica(t, l.member.id); got.State != "behind" {
		t.Fatalf("below the reported authority generation: %+v", got)
	}
}

// TestControllerFollowsAReportedMasterChange is defect 7's mirror half: the hosts own the
// master, so the pool row and the mirror rows only the master may refresh follow them.
func TestControllerFollowsAReportedMasterChange(t *testing.T) {
	l := newTransferLab(t)
	l.poolLabResources(l.member.id, nil, nil, nil)
	l.send(&l.master, nil)
	mirrorHost := func(id string) string {
		var host string
		if err := l.a.c.db.QueryRow("SELECT host_id FROM managed_resources WHERE id=?", id).Scan(&host); err != nil {
			t.Fatal(err)
		}
		return host
	}
	for _, id := range []string{"pool-nfs", "pool-vlan", "pool-iso"} {
		if got := mirrorHost(id); got != l.master.id {
			t.Fatalf("%s is mirrored under %s, not the master", id, got)
		}
	}
	// A rival claim at the generation already mirrored is not evidence: the mirror keeps
	// its row and says so.
	rival := l.transferMembership(2, l.member.id)
	l.member.inv.Managed.Membership = rival
	l.send(&l.member, nil)
	if got := l.a.poolOf(l.member.id); got != l.master.pool {
		t.Fatalf("an equal-generation claim moved the host: %s", got)
	}
	var recorded string
	if err := l.a.c.db.QueryRow("SELECT master_host_id FROM pools WHERE id=?", l.master.pool).Scan(&recorded); err != nil || recorded != l.master.id {
		t.Fatalf("an equal-generation claim flapped the mirror: %s (%v)", recorded, err)
	}
	if conflicts := l.members(t).MasterConflict; len(conflicts) != 1 || conflicts[0] != l.member.id {
		t.Fatalf("the rival claim was not shown as a conflict: %+v", conflicts)
	}
	// The pool moved: the new master reports the membership that names it, one generation
	// on. The Controller follows the hosts.
	promoted := l.transferMembership(3, l.member.id)
	l.member.inv.Managed.Membership = promoted
	l.send(&l.member, nil)
	if err := l.a.c.db.QueryRow("SELECT master_host_id FROM pools WHERE id=?", l.master.pool).Scan(&recorded); err != nil || recorded != l.member.id {
		t.Fatalf("the pool row did not follow the hosts: %s (%v)", recorded, err)
	}
	for _, id := range []string{"pool-nfs", "pool-vlan", "pool-iso"} {
		if got := mirrorHost(id); got != l.member.id {
			t.Fatalf("%s is still mirrored under the old master (%s)", id, got)
		}
	}
	if conflicts := l.members(t).MasterConflict; len(conflicts) != 0 {
		t.Fatalf("the adopted master is still reported as a conflict: %+v", conflicts)
	}
	// The new master's reports now refresh and tombstone the pool's rows; the old master's
	// no longer do.
	l.member.inv.Managed.Storage, l.member.inv.Managed.Networks, l.member.inv.Managed.Media = l.master.inv.Managed.Storage, l.master.inv.Managed.Networks, l.master.inv.Managed.Media
	l.member.inv.Managed.Networks[0].Name = "Guests renamed"
	l.send(&l.member, nil)
	var name string
	if err := l.a.c.db.QueryRow("SELECT name FROM managed_resources WHERE id='pool-vlan'").Scan(&name); err != nil || name != "Guests renamed" {
		t.Fatalf("the new master's report did not refresh the mirror: %q (%v)", name, err)
	}
}

// TestEjectedHostReinitializesIntoAnAdoptedPool: an ejected host comes back as the master
// of a new single-host pool, keeping its host ID. The Controller adopts it, with no
// operator action and no second identity for the host.
func TestEjectedHostReinitializesIntoAnAdoptedPool(t *testing.T) {
	l := newTransferLab(t)
	fresh := "pool-after-eject"
	// The master's report of the eject is the evidence the host may re-home; see
	// TestAMemberCannotLeaveItsPoolWithoutTheMastersReport for the half without it.
	ejected := l.transferMembership(3, l.master.id)
	for i := range ejected.Members {
		if ejected.Members[i].HostID == l.member.id {
			ejected.Members[i].Status = "ejected"
		}
	}
	l.master.inv.Managed.Membership = ejected
	l.send(&l.master, nil)
	l.member.inv.Managed.Membership = selfMembership(l.member.id, fresh)
	l.send(&l.member, nil)
	if got := l.a.poolOf(l.member.id); got != fresh {
		t.Fatalf("the re-initialized pool was not adopted: %s", got)
	}
	var site, master, name string
	if err := l.a.c.db.QueryRow("SELECT site_id,master_host_id,name FROM pools WHERE id=?", fresh).Scan(&site, &master, &name); err != nil {
		t.Fatalf("no pool row for the re-initialized host: %v", err)
	}
	var poolSite string
	if err := l.a.c.db.QueryRow("SELECT site_id FROM pools WHERE id=?", l.master.pool).Scan(&poolSite); err != nil {
		t.Fatal(err)
	}
	if site != poolSite || master != l.member.id || name != "core" {
		t.Fatalf("unexpected adopted pool: site=%s master=%s name=%s", site, master, name)
	}
	// The host itself is the same host: same ID, same certificate, still enrolled.
	if l.send(&l.member, nil).AcknowledgedResults == nil && l.a.poolOf(l.member.id) != fresh {
		t.Fatal("the re-initialized host stopped being able to report")
	}
	// The pool it left is untouched, and still has its master.
	var old string
	if err := l.a.c.db.QueryRow("SELECT master_host_id FROM pools WHERE id=?", l.master.pool).Scan(&old); err != nil || old != l.master.id {
		t.Fatalf("the old pool lost its master: %s (%v)", old, err)
	}
}

// TestPoolTransferSurvivesAControllerReplacement: the Controller is a relay. Reopened from
// the same database mid-transfer, it holds no stage of its own and projects the transfer
// from the hosts' next reports.
func TestPoolTransferSurvivesAControllerReplacement(t *testing.T) {
	l := newTransferLab(t)
	read(t, l.a.call("POST", l.masterPath(), map[string]any{"request_id": "survive-1", "host_id": l.member.id, "confirmation": l.poolName(t)}, nil, 202), &transferAcceptance{})
	l.master.inv.Managed.Transfer = &protocol.PoolTransferStatus{OperationID: "op-1", Action: "promote", Role: "source", TargetHostID: l.member.id, Stage: "activate", Status: "pending", Frozen: true, AuthorityGeneration: 9, ObservedAt: time.Now().UTC()}
	l.send(&l.master, nil)
	cfg := l.a.c.cfg
	if err := l.a.c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The lab's cleanup closes whatever a.c is; the old Controller is already closed.
	l.a.c = reopened
	// Nothing is known until the hosts report again: the Controller kept no stage.
	if view := l.members(t); view.Transfer != nil || view.Authority != nil {
		t.Fatalf("a reopened Controller claimed to know the transfer: %+v", view)
	}
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	view := l.members(t)
	if view.Transfer == nil || view.Transfer.Stage != "activate" || view.Authority == nil || view.Authority.Generation != 9 {
		t.Fatalf("the reopened Controller did not re-project the transfer: %+v", view.Transfer)
	}
	// The command it relayed is still the only thing it stored, and it is still queued for
	// the master, so an interrupted relay is not lost either.
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Kind != "pool-transfer" {
		t.Fatalf("the durable command did not survive the replacement: %+v", commands)
	}
	if w := httpStatusOfTransferRows(t, l); w != 1 {
		t.Fatalf("expected exactly one stored pool-transfer row, got %d", w)
	}
}

func httpStatusOfTransferRows(t *testing.T, l *joinLab) int {
	t.Helper()
	var rows int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='pool-transfer'").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestPoolTransferRoutesRequireASession is the boundary check every mutation gets.
func TestPoolTransferRoutesRequireASession(t *testing.T) {
	l := newTransferLab(t)
	anonymous := &testClient{t: t, c: l.a.c}
	anonymous.call("POST", l.masterPath(), map[string]any{"request_id": "anon", "host_id": l.member.id, "confirmation": l.poolName(t)}, nil, http.StatusUnauthorized)
	anonymous.call("DELETE", l.ejectPath(), map[string]any{"request_id": "anon-2", "confirmation": "core"}, nil, http.StatusUnauthorized)
	anonymous.call("POST", l.masterPath()+"/cancel", map[string]any{"request_id": "anon-3", "host_id": l.member.id}, nil, http.StatusUnauthorized)
}

// TestFenceBackedCancelIsPassedThroughAndReadsAsSuccess: the one case where a cancel powers
// a host off — the target took the hand-off and then stopped answering, so the pool's
// authority would stay with a dead host. The operator's agreement is two fields that mean
// nothing apart, and Core's answer to the cancellation it performed is a success with a
// reason, never a failed transfer.
func TestFenceBackedCancelIsPassedThroughAndReadsAsSuccess(t *testing.T) {
	l := newTransferLab(t)
	cancel := l.masterPath() + "/cancel"
	// Half a confirmation is not one, in either direction.
	for _, half := range []map[string]any{
		{"request_id": "half-1", "host_id": l.member.id, "fence_confirmed": true},
		{"request_id": "half-2", "host_id": l.member.id, "fence_host_ids": []string{l.member.id}},
	} {
		if body := l.a.call("POST", cancel, half, nil, 400).Body.String(); !strings.Contains(body, "fence_host_ids") {
			t.Fatalf("a half confirmation was not refused: %s", body)
		}
	}
	// A plain cancel still carries neither.
	l.a.call("POST", cancel, map[string]any{"request_id": "plain", "host_id": l.member.id}, nil, 202)
	if cmd := l.send(&l.master, nil).Commands[0].Pool; cmd.FenceConfirmed || cmd.FenceHostIDs != nil {
		t.Fatalf("a plain cancel invented a power-off: %+v", cmd)
	}
	var accepted transferAcceptance
	read(t, l.a.call("POST", cancel, map[string]any{"request_id": "fenced", "host_id": l.member.id, "fence_confirmed": true, "fence_host_ids": []string{l.member.id}}, nil, 202), &accepted)
	// The earlier plain cancel is redelivered until it is answered, so the fenced one is
	// found by the operation it was accepted as.
	command := protocol.Command{}
	for _, queued := range l.send(&l.master, nil).Commands {
		if queued.OperationID == accepted.Operation.ID {
			command = queued
		}
	}
	if command.Pool == nil {
		t.Fatalf("the master did not receive the fence-backed cancel")
	}
	if cmd := command.Pool; !cmd.FenceConfirmed || !slices.Equal(cmd.FenceHostIDs, []string{l.member.id}) || cmd.Valid() != nil {
		t.Fatalf("the confirmation did not reach Core verbatim: %+v", cmd)
	}
	// A master-change body may not carry it at all: it is the cancel route's field.
	l.a.call("POST", l.masterPath(), map[string]any{"request_id": "wrong-route", "host_id": l.member.id, "confirmation": l.poolName(t), "fence_confirmed": true}, nil, 400)

	reason := "pool peer " + l.member.id + " did not answer, and this pool's authority stays with it until that host is powered off"
	l.send(&l.master, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: command.OperationID, Status: "succeeded", Error: reason, ObservedAt: time.Now().UTC()}}
	})
	last := l.members(t).LastTransfer
	if last == nil || last.Status != "succeeded" || last.Error != "" || last.Reason != reason || last.Conflict || last.Retryable {
		t.Fatalf("a performed cancellation was not projected as a success with its reason: %+v", last)
	}
}
