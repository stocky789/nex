// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
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
	pool := a.pool()
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
