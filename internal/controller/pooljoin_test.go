// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// joinHost is one enrolled Core in the lab: its identity, the pool it masters today, and
// the inventory it reports.
type joinHost struct {
	id, pool string
	cert     *x509.Certificate
	seq      uint64
	caps     []string
	inv      protocol.Inventory
}

type joinLab struct {
	t              *testing.T
	a              *testClient
	master, member joinHost
}

const labCertificateHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func labNICs(names ...string) []protocol.NIC {
	out := []protocol.NIC{{Name: "br0", Kind: "bridge"}}
	for _, name := range names {
		out = append(out, protocol.NIC{Name: name, Kind: "physical", Master: "br0"})
	}
	return out
}

// selfMembership is what a Core that masters its own single-host pool reports.
func selfMembership(host, pool string) *protocol.PoolMembership {
	return &protocol.PoolMembership{PoolID: pool, MasterHostID: host, Generation: 1, Members: []protocol.PoolMember{
		{HostID: host, CertificateSHA256: labCertificateHash, Generation: 1, Status: "ready", ObservedAt: time.Now().UTC()},
	}}
}

func newJoinLab(t *testing.T) *joinLab {
	t.Helper()
	a := newClient(t)
	site := a.site()
	l := &joinLab{t: t, a: a}
	for _, h := range []*joinHost{&l.master, &l.member} {
		enrolled := a.enroll(a.token(site, ""), csr(t))
		h.id, h.cert = enrolled.HostID, certificate(t, enrolled.CertificatePEM)
		h.pool = a.poolOf(h.id)
		h.caps = []string{protocol.ManagedVMCapability, protocol.PoolJoinCapability}
		h.inv = protocol.Inventory{Hostname: "core", AgentVersion: "0.3.5", CPUModels: []string{"Skylake-Server"}, NICs: labNICs("nex0"), Managed: &protocol.ManagedState{Membership: selfMembership(h.id, h.pool)}}
	}
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	return l
}

// send posts one report for a host, optionally adding envelope fields to it.
func (l *joinLab) send(h *joinHost, mutate func(*protocol.ReportRequest)) protocol.ReportResponse {
	l.t.Helper()
	h.seq++
	inv := h.inv
	request := protocol.ReportRequest{Version: protocol.Version, Sequence: h.seq, Capabilities: h.caps, Inventory: &inv}
	if mutate != nil {
		mutate(&request)
	}
	return l.a.report(h.cert, request)
}

// callRaw is call without its status assertion, so a request can run on another goroutine.
func callRaw(a *testClient, method, path string, body any) *httptest.ResponseRecorder {
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		raw = string(b)
	}
	r := httptest.NewRequest(method, "https://nex.test"+path, strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", a.csrf)
	r.AddCookie(a.cookie)
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	a.c.Handler().ServeHTTP(w, r)
	return w
}

func labInvitation(request protocol.PoolRequest) protocol.PoolResult {
	return protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "succeeded", ObservedAt: time.Now().UTC(),
		Invitation: &protocol.PoolJoinInvitation{MasterURL: request.MasterURL, PoolCAPEM: "-----BEGIN CERTIFICATE-----\nlab\n-----END CERTIFICATE-----", Invitation: labInvitationToken, ExpiresAt: time.Now().Add(15 * time.Minute)}}
}

const labInvitationToken = "lab-single-use-invitation-token"

// addMember runs one POST while the master answers the brokered request from its reports,
// exactly as it would in the field: on the report after the one that carried the request.
func (l *joinLab) addMember(body any, answer func(protocol.PoolRequest) protocol.PoolResult) *httptest.ResponseRecorder {
	l.t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- callRaw(l.a, "POST", "/api/v1/pools/"+l.master.pool+"/members", body) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case w := <-done:
			return w
		default:
		}
		response := l.send(&l.master, nil)
		if len(response.PoolRequests) > 0 {
			l.send(&l.master, func(r *protocol.ReportRequest) {
				for _, request := range response.PoolRequests {
					r.PoolResults = append(r.PoolResults, answer(request))
				}
			})
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case w := <-done:
		return w
	case <-time.After(20 * time.Second):
		l.t.Fatal("pool join request never returned")
	}
	return nil
}

func (l *joinLab) joinRequest(requestID string) map[string]string {
	return map[string]string{"request_id": requestID, "host_id": l.member.id, "peer_origin": "https://192.0.2.12:9444", "master_url": "https://192.0.2.11:9444"}
}

func (l *joinLab) storedCommand(t *testing.T) protocol.Command {
	t.Helper()
	var raw string
	if err := l.a.c.db.QueryRow("SELECT command FROM managed_resources WHERE kind='pool-join'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var cmd protocol.Command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// adoptMemberVM gives the candidate the local defaults a Core self-registers and one managed
// VM, so the Controller mirrors a real managed journal for it.
func (l *joinLab) adoptMemberVM() {
	l.member.inv.Managed.Storage = []protocol.StorageRegistration{{ID: "member-iso", PoolID: l.member.pool, HostID: l.member.id, Name: "ISO", Path: "/var/lib/libvirt/images"}}
	l.member.inv.Managed.Networks = []protocol.NetworkRegistration{{ID: "member-net", PoolID: l.member.pool, HostID: l.member.id, Name: "untagged", Kind: "bridge", Source: "br0", Uplink: "nex0"}}
	guest := vmSpec(l.member.pool, l.member.id, "guest", "member-iso", "member-net", "")
	guest.ID = "ec2a2cb6-2d16-4f47-9c39-7a5a3f2f3b91"
	l.member.inv.Managed.VMs = []protocol.VMCreateSpec{guest}
	l.send(&l.member, nil)
	var vms int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='vm-create' AND host_id=?", l.member.id).Scan(&vms); err != nil || vms != 1 {
		l.t.Fatalf("candidate VM was not mirrored: %d (%v)", vms, err)
	}
}

func (l *joinLab) adoptMemberRemoteStorage() {
	l.member.inv.Managed.Storage = []protocol.StorageRegistration{{ID: "member-nfs", PoolID: l.member.pool, HostID: l.member.id, Name: "Remote", Kind: protocol.StorageKindNFS, Generation: 1, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/guests"}}, Access: protocol.StorageAccess{Status: "healthy", Readable: true, Writable: true}}}
	l.send(&l.member, nil)
}

// TestPoolJoinBrokerRoundTripDeliversInvitationAndKeepsItOutOfStorage is the whole slice:
// one brokered invitation, one durable command, and no invitation anywhere a reader can
// reach it afterwards.
func TestPoolJoinBrokerRoundTripDeliversInvitationAndKeepsItOutOfStorage(t *testing.T) {
	l := newJoinLab(t)
	w := l.addMember(l.joinRequest("join-1"), labInvitation)
	if w.Code != http.StatusAccepted {
		t.Fatalf("join: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), labInvitationToken) || strings.Contains(w.Body.String(), "BEGIN CERTIFICATE") {
		t.Fatalf("join response leaked the invitation: %s", w.Body.String())
	}
	var accepted struct {
		HostID     string             `json:"host_id"`
		PeerOrigin string             `json:"peer_origin"`
		Operation  protocol.Operation `json:"operation"`
	}
	read(t, w, &accepted)
	if accepted.HostID != l.member.id || accepted.PeerOrigin != "https://192.0.2.12:9444" || accepted.Operation.Kind != "pool-join" || accepted.Operation.Status != "queued" {
		t.Fatalf("unexpected join response: %+v", accepted)
	}
	// The durable command is the only carrier of the invitation, and it reaches the
	// joining host, not the master.
	if commands := l.send(&l.master, nil).Commands; len(commands) != 0 {
		t.Fatalf("master was sent join commands: %+v", commands)
	}
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].Pool == nil || commands[0].Pool.Action != "join" || commands[0].Pool.Invitation != labInvitationToken || commands[0].Pool.MemberHostID != l.member.id || commands[0].Pool.MasterURL != "https://192.0.2.11:9444" || commands[0].Pool.PeerOrigin != "https://192.0.2.12:9444" {
		t.Fatalf("joining host did not receive the join command: %+v", commands)
	}
	if err := commands[0].Pool.Valid(); err != nil {
		t.Fatalf("delivered join command is invalid: %v", err)
	}
	// Once delivered, the invitation is no longer Controller data.
	l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200)
	if stored := l.storedCommand(t); stored.Pool.Invitation != "" || stored.Pool.PoolCAPEM != "" {
		t.Fatalf("delivered invitation is still stored: %+v", stored.Pool)
	}
	l.send(&l.member, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()}}
	})
	var audits []struct {
		Actor, Action, ResourceID, Detail string
	}
	read(t, l.a.call("GET", "/api/v1/audit", nil, nil, 200), &audits)
	joins := 0
	for _, entry := range audits {
		if strings.Contains(entry.Detail, labInvitationToken) || strings.Contains(entry.ResourceID, labInvitationToken) {
			t.Fatalf("audit row leaked the invitation: %+v", entry)
		}
		if entry.Action == "pool-join" {
			joins++
		}
	}
	if joins != 1 {
		t.Fatalf("expected one pool-join audit row, got %d", joins)
	}
}

func TestPoolJoinRetryAndRefusalMatrix(t *testing.T) {
	l := newJoinLab(t)
	base := "/api/v1/pools/" + l.master.pool + "/members"
	// 400: malformed body and a peer origin that is not a bare https origin.
	l.a.call("POST", base, map[string]string{"request_id": "bad", "host_id": l.member.id, "peer_origin": "https://192.0.2.12:9444/join"}, nil, 400)
	l.a.call("POST", base, map[string]string{"request_id": "bad", "host_id": l.member.id, "peer_origin": "192.0.2.12:9444"}, nil, 400)
	l.a.call("POST", base, map[string]string{"request_id": "", "host_id": l.member.id, "peer_origin": "https://192.0.2.12:9444"}, nil, 400)
	// 404: unknown pool and unknown host.
	l.a.call("POST", "/api/v1/pools/no-such-pool/members", l.joinRequest("missing-pool"), nil, 404)
	missingHost := l.joinRequest("missing-host")
	missingHost["host_id"] = "no-such-host"
	l.a.call("POST", base, missingHost, nil, 404)
	// 409: the pool's own master cannot join itself.
	self := l.joinRequest("self")
	self["host_id"] = l.master.id
	l.a.call("POST", base, self, nil, 409)
	// The successful join, then its exact and changed retries.
	if w := l.addMember(l.joinRequest("join-1"), labInvitation); w.Code != http.StatusAccepted {
		t.Fatalf("join: %d %s", w.Code, w.Body.String())
	}
	var replay struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", base, l.joinRequest("join-1"), nil, 200), &replay)
	if replay.Operation.Kind != "pool-join" {
		t.Fatalf("exact retry did not replay the operation: %+v", replay)
	}
	changed := l.joinRequest("join-1")
	changed["peer_origin"] = "https://192.0.2.99:9444"
	l.a.call("POST", base, changed, nil, 409)
	var count int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='pool-join'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("retries created %d join records (%v)", count, err)
	}
}

func TestPoolJoinRefusesCrossSiteHost(t *testing.T) {
	l := newJoinLab(t)
	base := "/api/v1/pools/" + l.master.pool + "/members"
	otherSite := l.a.site()
	other := l.a.enroll(l.a.token(otherSite, ""), csr(t))
	otherHost := joinHost{id: other.HostID, cert: certificate(t, other.CertificatePEM), pool: l.a.poolOf(other.HostID), caps: []string{protocol.ManagedVMCapability, protocol.PoolJoinCapability},
		inv: protocol.Inventory{Hostname: "other", AgentVersion: "0.3.5", CPUModels: []string{"Skylake-Server"}, NICs: labNICs("nex0"), Managed: &protocol.ManagedState{Membership: selfMembership(other.HostID, l.a.poolOf(other.HostID))}}}
	l.send(&otherHost, nil)
	body := map[string]string{"request_id": "cross-site", "host_id": otherHost.id, "peer_origin": "https://192.0.2.13:9444", "master_url": "https://192.0.2.11:9444"}
	w := l.a.call("POST", base, body, nil, 409)
	if !strings.Contains(w.Body.String(), "same site") {
		t.Fatalf("cross-site refusal: %s", w.Body.String())
	}
}

func TestPoolJoinIgnoresFailedVMCreate(t *testing.T) {
	l := newJoinLab(t)
	l.adoptMemberVM()
	var opID string
	if err := l.a.c.db.QueryRow("SELECT operation_id FROM managed_resources WHERE kind='vm-create' AND host_id=?", l.member.id).Scan(&opID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.a.c.db.Exec("UPDATE operations SET status='failed',error='disk allocation failed',updated_at=? WHERE id=?", stamp(), opID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.a.c.db.Exec("UPDATE managed_resources SET id=? WHERE operation_id=? AND kind='vm-create'", "failed-vm-create:"+opID, opID); err != nil {
		t.Fatal(err)
	}
	if w := l.addMember(l.joinRequest("after-failed-vm"), labInvitation); w.Code != http.StatusAccepted {
		if strings.Contains(w.Body.String(), "managed VMs") {
			t.Fatalf("a failed vm-create must not block join: %s", w.Body.String())
		}
		t.Fatalf("join failed for another reason: %d %s", w.Code, w.Body.String())
	}
}

func TestPoolJoinRefusesStaleHostMissingCapabilityAndIneligibleCandidate(t *testing.T) {
	l := newJoinLab(t)
	base := "/api/v1/pools/" + l.master.pool + "/members"
	// A member that never advertises pool-join-v1.
	l.member.caps = []string{protocol.ManagedVMCapability}
	l.send(&l.member, nil)
	w := l.a.call("POST", base, l.joinRequest("no-member-capability"), nil, 409)
	if !strings.Contains(w.Body.String(), protocol.PoolJoinCapability) || !strings.Contains(w.Body.String(), "upgrade both hosts") {
		t.Fatalf("capability refusal must name the capability and both hosts: %s", w.Body.String())
	}
	l.member.caps = []string{protocol.ManagedVMCapability, protocol.PoolJoinCapability}
	l.send(&l.member, nil)
	// A master that never advertises it.
	l.master.caps = []string{protocol.ManagedVMCapability}
	l.send(&l.master, nil)
	w = l.a.call("POST", base, l.joinRequest("no-master-capability"), nil, 409)
	if !strings.Contains(w.Body.String(), "upgrade both hosts") {
		t.Fatalf("master capability refusal: %s", w.Body.String())
	}
	l.master.caps = []string{protocol.ManagedVMCapability, protocol.PoolJoinCapability}
	l.send(&l.master, nil)
	// Remote storage on the candidate, then a managed VM, are refused here too. Mirrored
	// rows are never withdrawn by a report that simply stops mentioning them, so these run
	// in the order an operator would hit them.
	l.adoptMemberRemoteStorage()
	w = l.a.call("POST", base, l.joinRequest("has-storage"), nil, 409)
	if !strings.Contains(w.Body.String(), "remote storage") {
		t.Fatalf("remote storage refusal: %s", w.Body.String())
	}
	l.adoptMemberVM()
	w = l.a.call("POST", base, l.joinRequest("has-vm"), nil, 409)
	if !strings.Contains(w.Body.String(), "managed VMs") {
		t.Fatalf("managed VM refusal: %s", w.Body.String())
	}
	// A host the Controller has not heard from within StaleAfter cannot be commanded.
	l.a.c.mu.Lock()
	delete(l.a.c.seen, l.member.id)
	l.a.c.mu.Unlock()
	w = l.a.call("POST", base, l.joinRequest("stale-member"), nil, 409)
	if !strings.Contains(w.Body.String(), "joining host must be fresh") {
		t.Fatalf("stale host refusal: %s", w.Body.String())
	}
}

func TestPoolJoinInvitationSkewGrace(t *testing.T) {
	now := time.Now()
	if !poolJoinInvitationCollectable(now.Add(5 * time.Minute)) {
		t.Fatal("future expiry should be collectable")
	}
	if !poolJoinInvitationCollectable(now.Add(-time.Minute)) {
		t.Fatal("expiry one minute ago should still be collectable within skew")
	}
	if poolJoinInvitationCollectable(time.Time{}) {
		t.Fatal("zero expiry should be rejected")
	}
	if poolJoinInvitationCollectable(now.Add(-time.Hour)) {
		t.Fatal("expiry one hour ago should be rejected")
	}
	request := protocol.PoolRequest{Action: "invite", MasterURL: "https://192.0.2.10:9444"}
	result := labInvitation(request)
	result.Invitation.ExpiresAt = now.Add(-time.Minute)
	if !poolResultValid("invite", result) {
		t.Fatalf("broker should accept invitation within skew: %+v", result.Invitation)
	}
	result.Invitation.ExpiresAt = now.Add(-time.Hour)
	if poolResultValid("invite", result) {
		t.Fatalf("broker should reject clearly expired invitation: %+v", result.Invitation)
	}
}

func TestPoolJoinBrokerDeadlineAndResultValidation(t *testing.T) {
	previous := poolJoinWait
	poolJoinWait = 300 * time.Millisecond
	t.Cleanup(func() { poolJoinWait = previous })
	l := newJoinLab(t)
	// A result addressed to another pool, another host or another request is not an answer.
	for name, corrupt := range map[string]func(protocol.PoolResult) protocol.PoolResult{
		"wrong pool":    func(v protocol.PoolResult) protocol.PoolResult { v.PoolID = "other-pool"; return v },
		"wrong host":    func(v protocol.PoolResult) protocol.PoolResult { v.HostID = "other-host"; return v },
		"wrong request": func(v protocol.PoolResult) protocol.PoolResult { v.RequestID = "other-request"; return v },
		"no invitation": func(v protocol.PoolResult) protocol.PoolResult { v.Invitation = nil; return v },
		"expired": func(v protocol.PoolResult) protocol.PoolResult {
			v.Invitation.ExpiresAt = time.Now().Add(-time.Hour)
			return v
		},
	} {
		w := l.addMember(l.joinRequest("deadline-"+strings.ReplaceAll(name, " ", "-")), func(request protocol.PoolRequest) protocol.PoolResult {
			return corrupt(labInvitation(request))
		})
		if w.Code != http.StatusGatewayTimeout {
			t.Fatalf("%s: got %d %s", name, w.Code, w.Body.String())
		}
		var result protocol.PoolResult
		read(t, w, &result)
		if result.Status != "failed" || result.Error == "" || result.Invitation != nil {
			t.Fatalf("%s: deadline body %+v", name, result)
		}
	}
	// A master that reports an honest failure is reported as a conflict, not a timeout.
	w := l.addMember(l.joinRequest("core-refusal"), func(request protocol.PoolRequest) protocol.PoolResult {
		return protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "failed", Error: "host is already a pool member", ObservedAt: time.Now().UTC()}
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already a pool member") {
		t.Fatalf("Core refusal: %d %s", w.Code, w.Body.String())
	}
	var records int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='pool-join'").Scan(&records); err != nil || records != 0 {
		t.Fatalf("a refused join left %d records (%v)", records, err)
	}
}

// poolLabResources registers one pool-shared repository, one pool VLAN on an uplink the
// member does not have, and one ISO on that repository, all reported by the master.
func (l *joinLab) poolLabResources(memberID string, access []protocol.PoolStorageAccess, networkAccess []protocol.PoolNetworkAccess, mediaAccess []protocol.PoolMediaAccess) {
	l.master.inv.Managed.Storage = []protocol.StorageRegistration{{
		ID: "pool-nfs", PoolID: l.master.pool, HostID: l.master.id, Name: "Pool NFS", Kind: protocol.StorageKindNFS, Generation: 1,
		Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/guests"}}, MemberAccess: access}}
	l.master.inv.Managed.Networks = []protocol.NetworkRegistration{{
		ID: "pool-vlan", PoolID: l.master.pool, HostID: l.master.id, Name: "Guests", Kind: "vlan", Uplink: "lab0", VLAN: 100,
		Source: protocol.GuestBridge("lab0", 100), Generation: 1, MemberAccess: networkAccess}}
	l.master.inv.Managed.Media = []protocol.Media{{
		ID: "pool-iso", PoolID: l.master.pool, HostID: l.master.id, StorageID: "pool-nfs", Name: "debian.iso", SizeBytes: 1 << 20, SHA256: strings.Repeat("c", 64), MemberAccess: mediaAccess}}
}

func TestPoolMembersProjectionCountsAccessAndRefusesToGuessWhenMasterIsStale(t *testing.T) {
	l := newJoinLab(t)
	// The master owns the pool and reports the member as ready.
	membership := selfMembership(l.master.id, l.master.pool)
	membership.Generation = 2
	membership.Members[0].Generation = 2
	membership.Members = append(membership.Members, protocol.PoolMember{HostID: l.member.id, CertificateSHA256: labCertificateHash, Generation: 2, Status: "ready", ObservedAt: time.Now().UTC(), PeerOrigin: "https://192.0.2.12:9444"})
	l.master.inv.Managed.Membership = membership
	// storage healthy, network failed, media absent (pending).
	l.poolLabResources(l.member.id,
		[]protocol.PoolStorageAccess{{RepositoryID: "pool-nfs", HostID: l.member.id, Generation: 1, Identity: "nfs-fsid", Readable: true, Writable: true, QEMUWritable: true, Locking: true, ObservedAt: time.Now().UTC()}},
		[]protocol.PoolNetworkAccess{{NetworkID: "pool-vlan", HostID: l.member.id, Generation: 1, ObservedAt: time.Now().UTC(), Error: "uplink lab0 does not exist on this host"}},
		nil)
	l.send(&l.master, nil)
	// The member reports its own convergence checks and an interrupted join.
	l.member.inv.Checks = []protocol.Check{
		{Name: "pool-storage-reconciliation", Status: "healthy", ObservedAt: time.Now().UTC()},
		{Name: "pool-network-reconciliation", Status: "failed", ObservedAt: time.Now().UTC(), Error: "uplink lab0 is missing"},
	}
	l.member.inv.Managed.Join = &protocol.PoolJoinStatus{OperationID: "op-1", Stage: "rehomed", Status: "needs_attention", ObservedAt: time.Now().UTC()}
	l.send(&l.member, nil)

	var view poolMembersView
	read(t, l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200), &view)
	if !view.Authoritative || view.MasterConnection != "fresh" || view.Generation != 2 || len(view.Members) != 2 {
		t.Fatalf("fresh master projection: %+v", view)
	}
	var member poolMemberView
	for _, entry := range view.Members {
		if entry.HostID == l.member.id {
			member = entry
		} else if entry.Role != "master" {
			t.Fatalf("master role missing: %+v", entry)
		}
	}
	if member.Role != "member" || member.Status != "ready" || member.Connection != "fresh" || member.PeerOrigin != "https://192.0.2.12:9444" {
		t.Fatalf("member row: %+v", member)
	}
	if member.Access != (poolMemberAccessCounts{Healthy: 1, Failed: 1, Pending: 1}) {
		t.Fatalf("member access counts: %+v", member.Access)
	}
	if member.Join == nil || member.Join.Stage != "rehomed" || member.Join.Status != "needs_attention" {
		t.Fatalf("member join status: %+v", member.Join)
	}
	// Three convergence checks, the authority-replica check and S6's restricted,
	// promotion and authority-transfer checks, unreported here and so honestly unknown.
	if len(member.Checks) != 7 || member.Checks[0].Status != "healthy" || member.Checks[1].Status != "failed" || member.Checks[1].Detail != "uplink lab0 is missing" || member.Checks[2].Status != "unknown" || member.Checks[3].Name != "pool-authority-replica" || member.Checks[3].Status != "unknown" || member.Checks[4].Name != "pool-restricted" || member.Checks[4].Status != "unknown" || member.Checks[5].Name != "pool-promotion" || member.Checks[5].Status != "unknown" || member.Checks[6].Name != "pool-authority-transfer" || member.Checks[6].Status != "unknown" {
		t.Fatalf("member checks: %+v", member.Checks)
	}
	// Defect 3: the pool VLAN was accepted against the master's NICs; this member reports
	// no lab0, so the projection says so per member.
	uplink := 0
	for _, failure := range member.Failures {
		if failure.Kind == "network" && failure.ID == "pool-vlan" {
			uplink++
		}
	}
	if uplink != 1 {
		t.Fatalf("expected exactly one pool VLAN failure for this member: %+v", member.Failures)
	}
	// A stale observation counts as stale, not healthy.
	l.poolLabResources(l.member.id,
		[]protocol.PoolStorageAccess{{RepositoryID: "pool-nfs", HostID: l.member.id, Generation: 1, Identity: "nfs-fsid", Readable: true, Writable: true, QEMUWritable: true, Locking: true, ObservedAt: time.Now().Add(-l.a.c.cfg.StaleAfter - time.Minute)}},
		nil, nil)
	l.send(&l.master, nil)
	read(t, l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200), &view)
	for _, entry := range view.Members {
		if entry.HostID == l.member.id && entry.Access != (poolMemberAccessCounts{Stale: 1, Pending: 2}) {
			t.Fatalf("stale access counts: %+v", entry.Access)
		}
	}
	// A master that has gone quiet proves nothing about its members.
	l.a.c.mu.Lock()
	delete(l.a.c.seen, l.master.id)
	l.a.c.mu.Unlock()
	read(t, l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200), &view)
	if view.Authoritative || view.MasterConnection != "stale" {
		t.Fatalf("stale master projection: %+v", view)
	}
	for _, entry := range view.Members {
		if entry.Status != "unknown" || entry.Access != (poolMemberAccessCounts{}) || entry.Fence != nil {
			t.Fatalf("stale master reported member state as current: %+v", entry)
		}
		// S6: the member's own health checks are its own report and stay; what the master
		// observed about it does not.
		if entry.HostID == l.member.id && (len(entry.Checks) != 7 || entry.Checks[0].Status != "healthy") {
			t.Fatalf("a quiet master erased the member's own checks: %+v", entry.Checks)
		}
	}
	l.a.call("GET", "/api/v1/pools/no-such-pool/members", nil, nil, 404)
}

func TestPoolJoinCandidatesPreviewsUplinkAndRefusals(t *testing.T) {
	l := newJoinLab(t)
	l.poolLabResources(l.member.id, nil, nil, nil)
	l.master.inv.CPUBaseline = &protocol.CPUBaseline{Model: "Skylake-Server", Features: []protocol.CPUFeature{{Name: "ssbd", Policy: "require"}}, Machine: "q35", Generation: 1, MemberCount: 1, ObservedAt: time.Now().UTC()}
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	candidates := func() []joinCandidate {
		t.Helper()
		var out struct {
			Candidates []joinCandidate `json:"candidates"`
		}
		read(t, l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/join-candidates", nil, nil, 200), &out)
		return out.Candidates
	}
	found := candidates()
	if len(found) != 1 || found[0].HostID != l.member.id || !found[0].Eligible {
		t.Fatalf("candidate preview: %+v", found)
	}
	// A host that reports no peer origin offers no address and Core's default port; a host
	// that reports one offers that address and port, so the operator types nothing.
	if found[0].PeerAddress != "" || found[0].PeerPort != protocol.DefaultPoolPeerPort {
		t.Fatalf("peer address without a report: %+v", found[0])
	}
	l.member.inv.Managed.PeerOrigin = "https://192.0.2.12:9500/"
	l.send(&l.member, nil)
	if found = candidates(); found[0].PeerAddress != "192.0.2.12" || found[0].PeerPort != 9500 {
		t.Fatalf("reported peer origin not offered: %+v", found[0])
	}
	rows := map[string]joinCandidateRow{}
	for _, row := range found[0].Rows {
		rows[row.Check] = row
	}
	// The candidate has nex0, the pool VLAN needs lab0: it may join, but that network
	// will never converge there. This is the projection defect 3 asks for.
	if rows["uplink"].Result != "will_not_converge" || !strings.Contains(rows["uplink"].Subject, "VLAN 100 on lab0") {
		t.Fatalf("uplink row: %+v", rows["uplink"])
	}
	if rows["connection"].Result != "compatible" || rows["capability"].Result != "compatible" || rows["membership"].Result != "compatible" || rows["cpu"].Result != "compatible" || rows["version"].Result != "compatible" {
		t.Fatalf("candidate rows: %+v", rows)
	}
	// Remote storage refuses the join outright.
	l.adoptMemberRemoteStorage()
	found = candidates()
	if len(found) != 1 || found[0].Eligible {
		t.Fatalf("a candidate with remote storage must not be eligible: %+v", found)
	}
	storage := false
	for _, row := range found[0].Rows {
		storage = storage || (row.Check == "storage" && row.Result == "refuses_join")
	}
	if !storage {
		t.Fatalf("remote storage row missing: %+v", found[0].Rows)
	}
	// So does a managed VM.
	l.adoptMemberVM()
	found = candidates()
	if len(found) != 1 || found[0].Eligible {
		t.Fatalf("a candidate with managed VMs must not be eligible: %+v", found)
	}
	vms := false
	for _, row := range found[0].Rows {
		vms = vms || (row.Check == "vms" && row.Result == "refuses_join")
	}
	if !vms {
		t.Fatalf("managed VM row missing: %+v", found[0].Rows)
	}
	l.a.call("GET", "/api/v1/pools/no-such-pool/join-candidates", nil, nil, 404)
}
