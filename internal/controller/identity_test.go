// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"cmp"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
)

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
// a replacement Controller adopts the same one, older agents still get minted IDs, and a
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

	// Older agents present nothing and get minted IDs named after the hostname.
	legacy := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, ""), CSRPEM: csr(t), Hostname: "older"}, 200)
	if len(legacy.HostID) != 64 || len(legacy.PoolID) != 64 || legacy.PoolName != "older" {
		t.Fatalf("legacy enrollment: %+v", legacy)
	}
	// A legacy host later presents its Controller-issued host ID with a minted pool.
	a.call("POST", "/api/v1/hosts/"+legacy.HostID+"/revoke", nil, nil, 200)
	kept := a.enrollWith(protocol.EnrollRequest{Token: a.token(site, legacy.HostID), CSRPEM: csr(t), Hostname: "older", HostID: legacy.HostID, PoolID: legacy.PoolID}, 200)
	if kept.HostID != legacy.HostID || kept.PoolID != legacy.PoolID || kept.PoolName != "older" {
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
