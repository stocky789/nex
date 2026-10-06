// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"cmp"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReportAcceptsCurrentCoreCapabilityCount(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	capabilities := make([]string, 35)
	for i := range capabilities {
		capabilities[i] = "capability-" + strconv.Itoa(i)
	}
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: capabilities}, cert, 200)
	var stored string
	if err := a.c.db.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", host.HostID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var reported []string
	if err := json.Unmarshal([]byte(stored), &reported); err != nil || !slices.Equal(reported, capabilities) {
		t.Fatalf("stored capabilities = %v, error = %v", reported, err)
	}
	for i := len(capabilities); i < 65; i++ {
		capabilities = append(capabilities, "capability-"+strconv.Itoa(i))
	}
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: capabilities}, cert, 400)
}

func TestRevocationBetweenMiddlewareAndTransaction(t *testing.T) {
	a := newClient(t)
	pool := a.site()
	original := a.enroll(a.token(pool, ""), csr(t))
	old := certificate(t, original.CertificatePEM)
	a.call("POST", "/api/v1/hosts/"+original.HostID+"/revoke", nil, nil, 200)
	replacement := a.enroll(a.token(pool, original.HostID), csr(t))
	for _, kind := range []string{"report", "rotate"} {
		var body any = protocol.ReportRequest{Version: 1, Sequence: 200, Inventory: &protocol.Inventory{Hostname: "stale-old-host", CPUCount: 99}}
		if kind == "rotate" {
			body = protocol.RotateRequest{Version: 1, CSRPEM: csr(t)}
		}
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "https://nex.test/api/v1/agent/"+kind, strings.NewReader(string(raw)))
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{old}}
		w := httptest.NewRecorder()
		if kind == "report" {
			a.c.report(w, r, original.HostID)
		} else {
			a.c.rotate(w, r, original.HostID)
		}
		if w.Code != 401 {
			t.Fatalf("already-authorized stale %s accepted: %d %s", kind, w.Code, w.Body.String())
		}
	}
	var count int
	if err := a.c.db.QueryRow("SELECT count(*) FROM certificates WHERE host_id=?", original.HostID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stale rotation created credential: %d %v", count, err)
	}
	if a.hosts()[0].Inventory != nil {
		t.Fatal("stale report mutated replacement inventory")
	}
	a.report(certificate(t, replacement.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1})
}

func (a *testClient) enrollWith(req protocol.EnrollRequest, want int) protocol.EnrollResponse {
	a.t.Helper()
	req.Version, req.Hostname = protocol.Version, cmp.Or(req.Hostname, "core")
	var v protocol.EnrollResponse
	read(a.t, a.call("POST", "/api/v1/agent/enroll", req, nil, want), &v)
	return v
}
func (a *testClient) pools() []protocol.Pool {
	a.t.Helper()
	var v []protocol.Pool
	read(a.t, a.call("GET", "/api/v1/pools", nil, nil, 200), &v)
	return v
}

// Identity is host-owned: the Controller adopts the host and pool identity a host presents,
// a replacement Controller adopts the same one, agents that present none still get minted IDs, and a
// token holder cannot claim an identity already on record.
func TestHostOwnedIdentity(t *testing.T) {
	const hostID, poolID = "6f1c2a3e-9b7d-4c5e-8a1b-0d2e3f4a5b6c", "0a9b8c7d-6e5f-4a3b-9c2d-1e0f9a8b7c6d"
	a := newClient(t)
	site := a.site()
	key := csr(t)
	present := protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: key, HostID: hostID, PoolID: poolID, PoolName: "Rack 1"}
	r := a.enrollWith(present, 200)
	if r.HostID != hostID || r.PoolID != poolID || r.PoolName != "Rack 1" || certificate(t, r.CertificatePEM).Subject.CommonName != hostID {
		t.Fatalf("presented identity not adopted: %+v", r)
	}
	if h := a.hosts(); len(h) != 1 || h[0].ID != hostID || h[0].PoolID != poolID {
		t.Fatalf("host record: %+v", h)
	}
	if p := a.pools(); len(p) != 1 || p[0].ID != poolID || p[0].Name != "Rack 1" || p[0].MasterHostID != hostID {
		t.Fatalf("pool record: %+v", p)
	}
	a.report(certificate(t, r.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1})

	// A retry with the same key but a new token (lost response) replays the recorded credential.
	again := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: key, HostID: hostID, PoolID: poolID, PoolName: "Rack 1"}, 200)
	if again.CertificatePEM != r.CertificatePEM || again.PoolID != poolID || len(a.hosts()) != 1 || len(a.pools()) != 1 {
		t.Fatalf("same-key retry did not replay: %+v", again)
	}
	a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: key, HostID: "1f1c2a3e-9b7d-4c5e-8a1b-0d2e3f4a5b6c"}, 409)
	// A token from another site never replays a credential held under this one: the same key
	// presenting the active host is refused like any other claim on it.
	a.enrollWith(protocol.EnrollRequest{Token: a.token(a.site(), ""), CSRPEM: key, HostID: hostID, PoolID: poolID, PoolName: "Rack 1"}, 409)

	// Another key claiming the active host, or its pool as a different master, is refused.
	a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), HostID: hostID}, 409)
	a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), HostID: "2f1c2a3e-9b7d-4c5e-8a1b-0d2e3f4a5b6c", PoolID: poolID}, 409)
	for _, bad := range []protocol.EnrollRequest{{HostID: "not-a-uuid"}, {PoolID: "6F1C2A3E-9B7D-4C5E-8A1B-0D2E3F4A5B6C"}, {PoolID: poolID + "; drop"}, {PoolName: "bad\nname"}} {
		bad.Token, bad.CSRPEM = a.token(site, ""), csr(t)
		a.enrollWith(bad, 400)
	}
	if len(a.hosts()) != 1 || len(a.pools()) != 1 {
		t.Fatal("refused enrollments left records")
	}

	// Agents that present nothing get minted IDs named after the hostname.
	minted := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), Hostname: "unnamed"}, 200)
	if len(minted.HostID) != 64 || len(minted.PoolID) != 64 || minted.PoolName != "unnamed" {
		t.Fatalf("minted enrollment: %+v", minted)
	}
	// A host later presents its Controller-issued host ID with a minted pool.
	a.call("POST", "/api/v1/hosts/"+minted.HostID+"/revoke", nil, nil, 200)
	kept := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, minted.HostID), CSRPEM: csr(t), Hostname: "unnamed", HostID: minted.HostID, PoolID: minted.PoolID}, 200)
	if kept.HostID != minted.HostID || kept.PoolID != minted.PoolID || kept.PoolName != "unnamed" {
		t.Fatalf("replacement token did not keep identity: %+v", kept)
	}

	// Replacing a revoked host must present that host and its pool, nothing else.
	a.call("POST", "/api/v1/hosts/"+hostID+"/revoke", nil, nil, 200)
	a.enrollWith(protocol.EnrollRequest{Token: a.token(site, hostID), CSRPEM: csr(t), HostID: hostID, PoolID: "3f1c2a3e-9b7d-4c5e-8a1b-0d2e3f4a5b6c"}, 409)
	a.enrollWith(protocol.EnrollRequest{Token: a.token(site, hostID), CSRPEM: csr(t), HostID: "3f1c2a3e-9b7d-4c5e-8a1b-0d2e3f4a5b6c"}, 409)
	replaced := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, hostID), CSRPEM: csr(t), HostID: hostID, PoolID: poolID, PoolName: "Rack 1"}, 200)
	if replaced.HostID != hostID || replaced.PoolID != poolID || replaced.PoolName != "Rack 1" {
		t.Fatalf("replacement changed identity: %+v", replaced)
	}

	// A brand-new Controller adopts the same identity from the host's enrollment request.
	b := newClient(t)
	present.Token = b.token(b.site(), "")
	if r2 := b.enrollWith(present, 200); r2.HostID != hostID || r2.PoolID != poolID || r2.PoolName != "Rack 1" {
		t.Fatalf("replacement Controller did not adopt identity: %+v", r2)
	}
	if p := b.pools(); len(p) != 1 || p[0].ID != poolID || p[0].Name != "Rack 1" || p[0].MasterHostID != hostID {
		t.Fatalf("replacement Controller pool: %+v", p)
	}
}

func TestReplacementControllerAcknowledgesFormerResultsAndAdoptsZFS(t *testing.T) {
	const hostID = "51b68e3d-c902-4d14-bf31-b66d54bae65b"
	const poolID = "9bb3babd-f22e-4932-b991-f4a8eed9178f"
	a := newClient(t)
	site := a.site()
	host := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), HostID: hostID, PoolID: poolID, PoolName: "Recovered pool"}, 200)
	cert := certificate(t, host.CertificatePEM)
	membership := protocol.PoolMembership{PoolID: poolID, MasterHostID: hostID, Generation: 1, Members: []protocol.PoolMember{{HostID: hostID, Hostname: hostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	storage := protocol.StorageRegistration{ID: "zfs-recovered", PoolID: poolID, HostID: hostID, Name: "Recovered ZFS", Path: "/var/lib/nex-storage/zfs-recovered", Kind: protocol.StorageKindZFS, Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "nex-recovered", Dataset: "vms", MountPath: "/var/lib/nex-storage/zfs-recovered"}}, ZFSProvisioned: true, ZFSScope: "pool", Capabilities: protocol.StorageCapabilities{VMDisk: true}, ZFSHostAccess: []protocol.ZFSHostAccess{{HostID: hostID, Path: "/var/lib/nex-storage/zfs-recovered", Readable: true, Writable: true, ObservedAt: time.Now().UTC()}}}
	response := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.ZFSProvisionCapability}, Results: []protocol.Result{{OperationID: "former-zfs-operation", Status: "succeeded"}}, Inventory: &protocol.Inventory{
		Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{storage}, ZFSProvisions: []protocol.ZFSProvisionHostResult{{RepositoryID: storage.ID, PoolID: poolID, Name: storage.Name, Scope: "pool", HostID: hostID, Status: "succeeded"}}},
		Checks:  []protocol.Check{{Name: "managed-journal", Status: "healthy", ObservedAt: time.Now().UTC()}},
	}})
	if len(response.AcknowledgedResults) != 1 || response.AcknowledgedResults[0] != "former-zfs-operation" {
		t.Fatalf("former Controller result was not safely acknowledged: %#v", response)
	}
	var repositories []protocol.StorageRegistration
	read(t, a.call(http.MethodGet, "/api/v1/storage", nil, nil, http.StatusOK), &repositories)
	if len(repositories) != 1 || repositories[0].ID != storage.ID || !repositories[0].ZFSProvisioned {
		t.Fatalf("fresh Controller did not adopt Core ZFS projection: %#v", repositories)
	}
	other := a.enroll(a.token(site, ""), csr(t))
	if _, err := a.c.db.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", "other-host-operation", other.HostID, "storage-register", "other-request", "succeeded", "", stamp(), stamp(), "system"); err != nil {
		t.Fatal(err)
	}
	a.call(http.MethodPost, "/api/v1/agent/report", protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Results: []protocol.Result{{OperationID: "other-host-operation", Status: "succeeded"}}}, cert, http.StatusBadRequest)
}

func TestFreshControllerAdoptsOnlyCoreReportedPoolMembers(t *testing.T) {
	const masterID = "6f1c2a3e-9b7d-4c5e-8a1b-0d2e3f4a5b6c"
	const poolID = "0a9b8c7d-6e5f-4a3b-9c2d-1e0f9a8b7c6d"
	const memberID = "1f1c2a3e-9b7d-4c5e-8a1b-0d2e3f4a5b6c"
	a := newClient(t)
	site := a.site()
	master := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), HostID: masterID, PoolID: poolID, PoolName: "Core pool"}, http.StatusOK)
	cert := certificate(t, master.CertificatePEM)
	membership := protocol.PoolMembership{PoolID: poolID, MasterHostID: masterID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: masterID, Hostname: masterID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: memberID, Hostname: memberID, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"},
	}}
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership}}})
	member := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), HostID: memberID, PoolID: poolID, PoolName: "Core pool"}, http.StatusOK)
	if member.PoolID != poolID || member.HostID != memberID || len(a.pools()) != 1 {
		t.Fatalf("Core-reported member was not adopted: %+v", member)
	}
	// A caller cannot add itself merely by knowing a pool ID, including after another
	// member enrolled; only the master Core's record is admission authority.
	a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), HostID: "2f1c2a3e-9b7d-4c5e-8a1b-0d2e3f4a5b6c", PoolID: poolID, PoolName: "Core pool"}, http.StatusConflict)
}

func TestCoreReportedJoinMovesExistingPeerIntoMasterPool(t *testing.T) {
	a := newClient(t)
	site := a.site()
	master := a.enroll(a.token(site, ""), csr(t))
	member := a.enroll(a.token(site, ""), csr(t))
	pool := a.poolOf(master.HostID)
	if pool == a.poolOf(member.HostID) {
		t.Fatal("test peers unexpectedly share an initial pool")
	}
	memberCert := certificate(t, member.CertificatePEM)
	// This is the former standalone host's actual Core projection. It must remain
	// under that pool until a later joined-host report confirms the same resource.
	a.report(memberCert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: hostDefaults(member.HostID)}})
	former := a.poolOf(member.HostID)
	const unrelated = "retained-empty-pool"
	if _, err := a.c.db.Exec("INSERT INTO pools(id,site_id,name,master_host_id) VALUES(?,?,?,?)", unrelated, site, "Other", member.HostID); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := a.c.db.QueryRow("SELECT command FROM managed_resources WHERE id='host-iso'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	membership := protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: member.HostID, Hostname: member.HostID, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"},
	}}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership}}})
	// Membership changes the host projection, but an absent resource report must not
	// manufacture a destination assignment or rewrite the original command.
	a.report(memberCert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership}}})
	var heldPool, heldCommand string
	if err := a.c.db.QueryRow("SELECT pool_id,command FROM managed_resources WHERE id='host-iso'").Scan(&heldPool, &heldCommand); err != nil || heldPool != former || heldCommand != before {
		t.Fatalf("unreported resource was rehomed: pool=%q err=%v", heldPool, err)
	}
	// A full Core report re-adopts the resource under the joined pool, allowing only
	// then the empty former auto-pool to disappear.
	joined := hostDefaults(member.HostID)
	joined.Membership = &membership
	a.report(memberCert, protocol.ReportRequest{Version: protocol.Version, Sequence: 3, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: joined}})
	for _, host := range a.hosts() {
		if host.ID == member.HostID && host.PoolID != pool {
			t.Fatalf("member stayed in obsolete Controller pool: %+v", host)
		}
	}
	if pools := a.pools(); len(pools) != 2 || !slices.ContainsFunc(pools, func(p protocol.Pool) bool { return p.ID == pool }) || !slices.ContainsFunc(pools, func(p protocol.Pool) bool { return p.ID == unrelated }) || slices.ContainsFunc(pools, func(p protocol.Pool) bool { return p.ID == former }) {
		t.Fatalf("cleanup removed the wrong pool or retained its source: %+v", pools)
	}
	var got []protocol.StorageRegistration
	read(t, a.call("GET", "/api/v1/storage", nil, nil, http.StatusOK), &got)
	if len(got) != 1 || got[0].PoolID != pool || got[0].HostID != member.HostID {
		t.Fatalf("actual Core resource was not adopted: %+v", got)
	}
}

// Only a durable ZFS provision recovers from needs_attention through a result; every other
// kind settles through Core's reported managed outcome. Unknown result IDs are echoed back
// only when they are well formed.
func TestOnlyZFSProvisionResultsRecoverFromAttention(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	caps := []string{protocol.ManagedVMCapability}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: caps})
	var network protocol.NetworkRegistration
	read(t, a.call(http.MethodPost, "/api/v1/networks", registrationRequest{RequestID: "net-attention", PoolID: pool, HostID: host.HostID, Name: "Servers", Kind: "bridge", Source: "br0"}, nil, 201), &network)
	if len(a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: caps}).Commands) != 1 {
		t.Fatal("network registration was not dispatched")
	}
	status := func() string {
		var ops []protocol.Operation
		read(t, a.call(http.MethodGet, "/api/v1/operations", nil, nil, 200), &ops)
		for _, op := range ops {
			if op.ID == network.OperationID {
				return op.Status
			}
		}
		return ""
	}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 3, Capabilities: caps, Results: []protocol.Result{{OperationID: network.OperationID, Status: "needs_attention", Error: "interrupted"}}})
	if got := status(); got != "needs_attention" {
		t.Fatalf("host result was not recorded: %q", got)
	}
	oversized := strings.Repeat("x", 129)
	acknowledged := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 4, Capabilities: caps, Results: []protocol.Result{
		{OperationID: network.OperationID, Status: "succeeded"}, {OperationID: oversized, Status: "succeeded"}, {OperationID: "former-controller:op 1", Status: "succeeded"}}})
	if got := status(); got != "needs_attention" {
		t.Fatalf("a non-ZFS result reopened a settled attention operation: %q", got)
	}
	// Core journals any command ID up to 128 bytes, so such a result from a former
	// Controller is acknowledged (or Core re-sends it forever); a longer one is not.
	if !slices.Contains(acknowledged.AcknowledgedResults, network.OperationID) || !slices.Contains(acknowledged.AcknowledgedResults, "former-controller:op 1") || slices.Contains(acknowledged.AcknowledgedResults, oversized) {
		t.Fatalf("unexpected acknowledged result IDs: %#v", acknowledged.AcknowledgedResults)
	}
	// Core's own managed outcome is what settles the entry it owns.
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 5, Capabilities: caps, Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{
		Outcomes: []protocol.ManagedOutcome{{OperationID: network.OperationID, Kind: "network-register", ID: network.ID, Name: network.Name, Status: "succeeded"}}}}})
	if got := status(); got != "succeeded" {
		t.Fatalf("Core outcome did not settle the operation: %q", got)
	}
}
