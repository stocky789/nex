// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/x509"
	"net/http"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// recordedMaster is the pool row's master, which is the mirror the adoption rules move.
func recordedMaster(t *testing.T, l *joinLab, pool string) string {
	t.Helper()
	var master string
	if err := l.a.c.db.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil {
		t.Fatal(err)
	}
	return master
}

// TestSelfClaimedMasteryIsRefusedWithoutMirroredEvidence: a member's own report is a payload
// it fully controls. Adoption needs the recorded master's mirrored membership as evidence, so
// a recorded master that has never reported one must fail the claim closed, not wave it
// through and hand the claimant the pool row and every mirror.
func TestSelfClaimedMasteryIsRefusedWithoutMirroredEvidence(t *testing.T) {
	l := newTransferLab(t)
	l.poolLabResources(l.member.id, nil, nil, nil)
	l.send(&l.master, nil)
	// The master's stored inventory carries no membership: it has not reported one since this
	// Controller took over, which identity.go stores as a nil managed state.
	l.master.inv.Managed.Membership, l.master.inv.Managed.Authority = nil, nil
	l.send(&l.master, nil)
	if _, err := l.a.c.db.Exec("UPDATE hosts SET inventory=NULL WHERE id=?", l.master.id); err != nil {
		t.Fatal(err)
	}
	// The member now claims the pool for itself, at the lowest generation there is.
	claim := l.transferMembership(1, l.member.id)
	l.member.inv.Managed.Membership = claim
	l.send(&l.member, nil)
	if got := recordedMaster(t, l, l.master.pool); got != l.master.id {
		t.Fatalf("a self-claim with no evidence took the pool: master is %s", got)
	}
	var host string
	if err := l.a.c.db.QueryRow("SELECT host_id FROM managed_resources WHERE id='pool-nfs'").Scan(&host); err != nil || host != l.master.id {
		t.Fatalf("a self-claim with no evidence re-keyed the mirrors: %s (%v)", host, err)
	}
}

// TestMemberFirstReconnectStillAdoptsTheRealMaster is the path the evidence rule must not
// close: a replacement Controller, an existing multi-host pool, and the member reconnecting
// first. The member creates the pool row with itself recorded as master, then reports a
// membership naming the real master. That report is the pool's own record, so the real master
// may enroll on it and is adopted when it reports the membership that names it — at the same
// generation, because a reconnect is not a master change.
func TestMemberFirstReconnectStillAdoptsTheRealMaster(t *testing.T) {
	const poolID = "1b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9"
	const memberID = "2b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9"
	const masterID = "3b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9"
	a := newClient(t)
	site := a.site()
	membership := &protocol.PoolMembership{PoolID: poolID, MasterHostID: masterID, Generation: 7, Members: []protocol.PoolMember{
		{HostID: masterID, CertificateSHA256: labCertificateHash, Generation: 7, Status: "ready", ObservedAt: time.Now().UTC()},
		{HostID: memberID, CertificateSHA256: labCertificateHash, Generation: 7, Status: "ready", ObservedAt: time.Now().UTC()},
	}}
	member := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), HostID: memberID, PoolID: poolID, PoolName: "Rack 1"}, 200)
	inv := protocol.Inventory{Hostname: "core-member", AgentVersion: "0.3.5", NICs: labNICs("nex0"), Managed: &protocol.ManagedState{Membership: membership}}
	a.report(certificate(t, member.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.PoolJoinCapability}, Inventory: &inv})

	// The pool's own record names the master, so the master's enrollment is not held.
	master := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), HostID: masterID, PoolID: poolID, PoolName: "Rack 1", Hostname: "core-master"}, 200)
	masterInv := protocol.Inventory{Hostname: "core-master", AgentVersion: "0.3.5", NICs: labNICs("nex0"), Managed: &protocol.ManagedState{Membership: membership}}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.PoolJoinCapability}, Inventory: &masterInv})
	var recorded string
	if err := a.c.db.QueryRow("SELECT master_host_id FROM pools WHERE id=?", poolID).Scan(&recorded); err != nil || recorded != masterID {
		t.Fatalf("the real master was not adopted after a member-first reconnect: %s (%v)", recorded, err)
	}
	// And the member that holds no mastery cannot take the row back by saying it does.
	rival := &protocol.PoolMembership{PoolID: poolID, MasterHostID: memberID, Generation: 7, Members: membership.Members}
	rival.Members = append([]protocol.PoolMember(nil), membership.Members...)
	inv.Managed.Membership = rival
	a.report(certificate(t, member.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability, protocol.PoolJoinCapability}, Inventory: &inv})
	if err := a.c.db.QueryRow("SELECT master_host_id FROM pools WHERE id=?", poolID).Scan(&recorded); err != nil || recorded != masterID {
		t.Fatalf("an equal-generation rival took the adopted pool: %s (%v)", recorded, err)
	}
}

// goneMasterLab is a replacement Controller for a pool of three hosts whose master never
// comes back: only the two members enroll.
type goneMasterLab struct {
	t    *testing.T
	a    *testClient
	site string
}

const (
	goneMasterPool   = "4b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9"
	goneMasterID     = "5b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9"
	goneMasterFirst  = "6b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9"
	goneMasterSecond = "7b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9"
)

var goneMasterCapabilities = []string{protocol.ManagedVMCapability, protocol.PoolJoinCapability, protocol.PoolAuthorityTransferCapability}

func newGoneMasterLab(t *testing.T) *goneMasterLab {
	a := newClient(t)
	return &goneMasterLab{t: t, a: a, site: a.site()}
}

func (l *goneMasterLab) membership(master string, generation uint64, hosts ...string) *protocol.PoolMembership {
	m := &protocol.PoolMembership{PoolID: goneMasterPool, MasterHostID: master, Generation: generation}
	for _, id := range append([]string{goneMasterID, goneMasterFirst, goneMasterSecond}, hosts...) {
		m.Members = append(m.Members, protocol.PoolMember{HostID: id, CertificateSHA256: labCertificateHash, Generation: generation, Status: "ready", ObservedAt: time.Now().UTC()})
	}
	return m
}

func (l *goneMasterLab) recorded() string {
	var master string
	if err := l.a.c.db.QueryRow("SELECT master_host_id FROM pools WHERE id=?", goneMasterPool).Scan(&master); err != nil {
		l.t.Fatal(err)
	}
	return master
}

func (l *goneMasterLab) enroll(id, name string, want int) *x509.Certificate {
	enrolled := l.a.enrollWith(protocol.EnrollRequest{Token: l.a.token(l.site, ""), CSRPEM: csr(l.t), HostID: id, PoolID: goneMasterPool, PoolName: "Rack 2", Hostname: name}, want)
	if want != 200 {
		return nil
	}
	return certificate(l.t, enrolled.CertificatePEM)
}

func (l *goneMasterLab) report(cert *x509.Certificate, name string, sequence uint64, managed protocol.ManagedState) {
	inv := protocol.Inventory{Hostname: name, AgentVersion: "0.4.2", NICs: labNICs("nex0"), Managed: &managed}
	l.a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: sequence, Capabilities: goneMasterCapabilities, Inventory: &inv})
}

// TestReplacementControllerWithTheMasterGoneCanPromote: a replacement Controller, and the old
// master never comes back. The member that reconnects first must not stay recorded as master
// once its own report names another host, or no other member can enroll, the Hosts view
// shows the wrong master, and the member is refused promotion as "already the pool master".
func TestReplacementControllerWithTheMasterGoneCanPromote(t *testing.T) {
	l := newGoneMasterLab(t)
	first := l.enroll(goneMasterFirst, "core-first", 200)
	l.report(first, "core-first", 1, protocol.ManagedState{Membership: l.membership(goneMasterID, 7)})
	if got := l.recorded(); got != goneMasterID {
		t.Fatalf("the first member stays recorded as master although it names %s: %s", goneMasterID, got)
	}
	second := l.enroll(goneMasterSecond, "core-second", 200)
	l.report(second, "core-second", 1, protocol.ManagedState{Membership: l.membership(goneMasterID, 7)})

	var view struct {
		MasterHostID  string `json:"master_host_id"`
		Authoritative bool   `json:"authoritative"`
		Members       []struct {
			HostID string `json:"host_id"`
		} `json:"members"`
	}
	read(t, l.a.call("GET", "/api/v1/pools/"+goneMasterPool+"/members", nil, nil, 200), &view)
	if view.MasterHostID != goneMasterID || view.Authoritative || len(view.Members) != 3 {
		t.Fatalf("the Hosts view does not show the pool the members report: %+v", view)
	}
	tx, err := l.a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	status, refusal := l.a.c.poolPromotionCandidate(tx, goneMasterPool, goneMasterFirst, true)
	tx.Rollback()
	if refusal != nil {
		t.Fatalf("a member of a pool whose master is gone cannot be promoted: %d %v", status, refusal)
	}

	// The promotion runs on Core; its result is the member reporting itself master one
	// generation on, which is followed although the old master never reported here.
	l.report(first, "core-first", 2, protocol.ManagedState{Membership: l.membership(goneMasterFirst, 8)})
	if got := l.recorded(); got != goneMasterFirst {
		t.Fatalf("the promoted member was not adopted as master: %s", got)
	}
	// A same-generation self-claim from the other member does not take it.
	l.report(second, "core-second", 2, protocol.ManagedState{Membership: l.membership(goneMasterSecond, 8)})
	if got := l.recorded(); got != goneMasterFirst {
		t.Fatalf("an equal-generation rival took the promoted pool: %s", got)
	}
}

// TestAMemberCannotAdmitAHostThroughAGoneMastersRecord: while the recorded master has never
// enrolled, the members' reports are the evidence, and each is a payload that member fully
// controls. One member listing an extra host must not let that host enroll into the pool.
func TestAMemberCannotAdmitAHostThroughAGoneMastersRecord(t *testing.T) {
	const intruder = "8b2c3d4e-5f60-4718-8293-a4b5c6d7e8f9"
	l := newGoneMasterLab(t)
	first := l.enroll(goneMasterFirst, "core-first", 200)
	l.report(first, "core-first", 1, protocol.ManagedState{Membership: l.membership(goneMasterID, 7)})
	second := l.enroll(goneMasterSecond, "core-second", 200)
	l.report(second, "core-second", 1, protocol.ManagedState{Membership: l.membership(goneMasterID, 50, intruder)})
	l.enroll(intruder, "core-intruder", http.StatusConflict)
}

// TestAReturningMasterTakesItsMirrorsBack: rows the first member held while it was wrongly
// recorded as master move to the real master when it enrolls, because only the recorded
// master's reports refresh and tombstone them.
func TestAReturningMasterTakesItsMirrorsBack(t *testing.T) {
	l := newGoneMasterLab(t)
	first := l.enroll(goneMasterFirst, "core-first", 200)
	l.report(first, "core-first", 1, protocol.ManagedState{Storage: []protocol.StorageRegistration{{
		ID: "pool-nfs", PoolID: goneMasterPool, HostID: goneMasterFirst, Name: "Pool NFS", Kind: protocol.StorageKindNFS, Generation: 1,
		Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/guests"}}}}})
	l.report(first, "core-first", 2, protocol.ManagedState{Membership: l.membership(goneMasterID, 7)})
	l.enroll(goneMasterID, "core-master", 200)
	var host string
	if err := l.a.c.db.QueryRow("SELECT host_id FROM managed_resources WHERE id='pool-nfs'").Scan(&host); err != nil || host != goneMasterID {
		t.Fatalf("the pool storage mirror stayed on the first member: %s (%v)", host, err)
	}
}

// TestAMemberCannotLeaveItsPoolWithoutTheMastersReport: re-initializing into a new single-host
// pool is what Core does after an eject, so the old pool's master is the evidence for it. A
// host that simply announces a new pool while its master still reports it as a member must not
// be moved out of the pool, or the Controller's view of the pool follows the member instead of
// the master.
func TestAMemberCannotLeaveItsPoolWithoutTheMastersReport(t *testing.T) {
	l := newTransferLab(t)
	fresh := "pool-after-eject"
	l.member.inv.Managed.Membership = selfMembership(l.member.id, fresh)
	l.send(&l.member, nil)
	if got := l.a.poolOf(l.member.id); got != l.master.pool {
		t.Fatalf("a member left its pool with no report from the master: %s", got)
	}
	// The master ejects it. Its next membership marks the member ejected, and only then does
	// the host's new pool become adoptable.
	ejected := l.transferMembership(3, l.master.id)
	for i := range ejected.Members {
		if ejected.Members[i].HostID == l.member.id {
			ejected.Members[i].Status = "ejected"
		}
	}
	l.master.inv.Managed.Membership = ejected
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	if got := l.a.poolOf(l.member.id); got != fresh {
		t.Fatalf("the ejected host was not adopted into its new pool: %s", got)
	}
}

// TestPoolJoinInvitationScrubbedAfterARestartInsideTheTTL: the scrub's only timer dies with
// the process, and a Controller restarted inside the invitation's lifetime finds the row still
// young and declines to scrub it. Without a periodic sweep the invitation and the pool CA sit
// in managed_resources.command until someone happens to call a pool route.
func TestPoolJoinInvitationScrubbedAfterARestartInsideTheTTL(t *testing.T) {
	l := newJoinLab(t)
	if w := l.addMember(l.joinRequest("inside-ttl"), labInvitation); w.Code != http.StatusAccepted {
		t.Fatalf("join: %d %s", w.Code, w.Body.String())
	}
	dir := l.a.c.cfg.DataDir
	if err := l.a.c.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	l.a.c = restarted
	// The restart is inside the TTL, so the start-time scrub correctly leaves it alone.
	if stored := l.storedCommand(t); stored.Pool.Invitation != labInvitationToken {
		t.Fatalf("the young invitation was scrubbed too early: %+v", stored.Pool)
	}
	// Time passes and no pool route is ever called again.
	expired := time.Now().Add(-poolJoinInvitationTTL - time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err = l.a.c.db.Exec("UPDATE operations SET created_at=? WHERE id IN (SELECT operation_id FROM managed_resources WHERE kind='pool-join')", expired); err != nil {
		t.Fatal(err)
	}
	l.a.c.mu.Lock()
	l.a.c.joinScrubbed = time.Time{}
	l.a.c.mu.Unlock()
	l.a.c.healthTick(map[string]pendingHealth{})
	if stored := l.storedCommand(t); stored.Pool.Invitation != "" || stored.Pool.PoolCAPEM != "" {
		t.Fatalf("a restarted Controller kept an expired invitation: %+v", stored.Pool)
	}
}

// TestExpiredPoolJoinFailsInsteadOfBeingRedeliveredEmpty: once the invitation is scrubbed the
// command cannot do its job, so it must stop being a command. An uncollected join fails with
// the reason, and a join already collected by the host is never sent again stripped of the
// credential it needs.
func TestExpiredPoolJoinFailsInsteadOfBeingRedeliveredEmpty(t *testing.T) {
	l := newJoinLab(t)
	if w := l.addMember(l.joinRequest("expired"), labInvitation); w.Code != http.StatusAccepted {
		t.Fatalf("join: %d %s", w.Code, w.Body.String())
	}
	expired := time.Now().Add(-poolJoinInvitationTTL - time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := l.a.c.db.Exec("UPDATE operations SET created_at=? WHERE id IN (SELECT operation_id FROM managed_resources WHERE kind='pool-join')", expired); err != nil {
		t.Fatal(err)
	}
	l.a.c.scrubPoolJoinInvitations()
	var status, failure string
	if err := l.a.c.db.QueryRow("SELECT o.status,o.error FROM operations o JOIN managed_resources r ON r.operation_id=o.id WHERE r.kind='pool-join'").Scan(&status, &failure); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || failure != poolJoinInvitationExpired {
		t.Fatalf("an uncollected expired join stayed deliverable: %s %q", status, failure)
	}
	if commands := l.send(&l.member, nil).Commands; len(commands) != 0 {
		t.Fatalf("an expired join was still delivered: %+v", commands)
	}
	// A join the host did collect keeps its operation — its own result settles it — but is
	// never redelivered once the invitation has been scrubbed out of the stored command.
	if w := l.addMember(l.joinRequest("collected"), labInvitation); w.Code != http.StatusAccepted {
		t.Fatalf("second join: %d %s", w.Code, w.Body.String())
	}
	if commands := l.send(&l.member, nil).Commands; len(commands) != 1 || commands[0].Pool.Invitation != labInvitationToken {
		t.Fatalf("the join was not delivered with its invitation: %+v", commands)
	}
	l.a.c.scrubPoolJoinInvitations()
	if commands := l.send(&l.member, nil).Commands; len(commands) != 0 {
		t.Fatalf("a scrubbed join was redelivered with empty credentials: %+v", commands)
	}
	var second string
	if err := l.a.c.db.QueryRow("SELECT o.status FROM operations o JOIN managed_resources r ON r.operation_id=o.id WHERE r.kind='pool-join' AND r.id<>'' ORDER BY o.rowid DESC LIMIT 1").Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second != "dispatched" {
		t.Fatalf("a join in progress on the host was declared %s", second)
	}
}

// TestPoolJoinMaintenanceAndHostNetworkRoutesRequireASession is the boundary check the newer
// routes never got: no session, no mutation, whatever the body says.
func TestPoolJoinMaintenanceAndHostNetworkRoutesRequireASession(t *testing.T) {
	l := newJoinLab(t)
	anonymous := &testClient{t: t, c: l.a.c}
	anonymous.call("POST", "/api/v1/pools/"+l.master.pool+"/members", l.joinRequest("anon"), nil, http.StatusUnauthorized)
	anonymous.call("POST", "/api/v1/pools/"+l.master.pool+"/members/"+l.member.id+"/maintenance", map[string]any{"request_id": "anon-2", "maintenance": true}, nil, http.StatusUnauthorized)
	anonymous.call("POST", "/api/v1/hosts/"+l.master.id+"/network", map[string]any{"request_id": "anon-3"}, nil, http.StatusUnauthorized)
	anonymous.call("GET", "/api/v1/hosts/"+l.master.id+"/network", nil, nil, http.StatusUnauthorized)
	anonymous.call("GET", "/api/v1/pools/"+l.master.pool+"/join-candidates", nil, nil, http.StatusUnauthorized)
}

// TestADisclaimedMasteryStaysInsideThePool: the master a report names is a value its reporter
// fully controls, so a master disclaiming the row must not be able to hand it to a host of
// another pool. That host never refreshes this pool's mirrors, and the pool's page would
// render the other pool's membership, fence profiles and BMC addresses.
func TestADisclaimedMasteryStaysInsideThePool(t *testing.T) {
	l := newTransferLab(t)
	l.poolLabResources(l.member.id, nil, nil, nil)
	l.send(&l.master, nil)
	enrolled := l.a.enroll(l.a.token(l.a.site(), ""), csr(t))
	outsider := joinHost{id: enrolled.HostID, pool: l.a.poolOf(enrolled.HostID)}

	claim := l.transferMembership(3, outsider.id, outsider)
	l.master.inv.Managed.Membership = claim
	l.send(&l.master, nil)

	if got := recordedMaster(t, l, l.master.pool); got != l.master.id {
		t.Fatalf("a master handed its pool row to a host of another pool: master is %s", got)
	}
	var host string
	if err := l.a.c.db.QueryRow("SELECT host_id FROM managed_resources WHERE id='pool-nfs'").Scan(&host); err != nil || host != l.master.id {
		t.Fatalf("the pool's mirrors were re-keyed onto a host of another pool: %s (%v)", host, err)
	}
}
