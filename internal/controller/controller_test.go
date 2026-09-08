// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

type testClient struct {
	t      *testing.T
	c      *Controller
	cookie *http.Cookie
	csrf   string
}

func newClient(t *testing.T) *testClient {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := Open(Config{DataDir: dir, Username: "admin", Password: "a-long-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	a := &testClient{t: t, c: c}
	t.Cleanup(func() { a.c.Close() })
	w := a.call("POST", "/api/v1/session", map[string]string{"username": "admin", "password": "a-long-test-password"}, nil, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	a.cookie = cookies[0]
	if !a.cookie.Secure || !a.cookie.HttpOnly || a.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	var session map[string]string
	read(t, w, &session)
	a.csrf = session["csrf_token"]
	if a.csrf == "" {
		t.Fatal("missing csrf")
	}
	return a
}
func (a *testClient) call(method, path string, body any, cert *x509.Certificate, want int) *httptest.ResponseRecorder {
	a.t.Helper()
	raw := ""
	if s, ok := body.(string); ok {
		raw = s
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		raw = string(b)
	}
	r := httptest.NewRequest(method, "https://nex.test"+path, strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	if a.cookie != nil {
		r.AddCookie(a.cookie)
	}
	if a.csrf != "" {
		r.Header.Set("X-CSRF-Token", a.csrf)
	}
	if cert != nil {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	}
	w := httptest.NewRecorder()
	a.c.Handler().ServeHTTP(w, r)
	if w.Code != want {
		a.t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}
func read(t *testing.T, w *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}
func csr(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}
func certificate(t *testing.T, s string) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		t.Fatal("missing certificate")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func (a *testClient) pool() string {
	a.t.Helper()
	var s protocol.Site
	read(a.t, a.call("POST", "/api/v1/sites", map[string]string{"name": "Lab"}, nil, 201), &s)
	var p protocol.Pool
	read(a.t, a.call("POST", "/api/v1/pools", map[string]string{"name": "Pool", "site_id": s.ID}, nil, 201), &p)
	if p.SiteID != s.ID || p.ID == "" {
		a.t.Fatal("pool ownership missing")
	}
	return p.ID
}
func (a *testClient) token(pool, host string) string {
	a.t.Helper()
	var v struct {
		Token string `json:"token"`
	}
	read(a.t, a.call("POST", "/api/v1/enrollment-tokens", map[string]string{"pool_id": pool, "host_id": host}, nil, 201), &v)
	return v.Token
}
func (a *testClient) enroll(token, key string) protocol.EnrollResponse {
	a.t.Helper()
	var v protocol.EnrollResponse
	read(a.t, a.call("POST", "/api/v1/agent/enroll", protocol.EnrollRequest{Version: protocol.Version, Token: token, CSRPEM: key, Hostname: "core"}, nil, 200), &v)
	return v
}
func (a *testClient) hosts() []protocol.Host {
	a.t.Helper()
	var v []protocol.Host
	read(a.t, a.call("GET", "/api/v1/hosts", nil, nil, 200), &v)
	return v
}
func (a *testClient) report(cert *x509.Certificate, request protocol.ReportRequest) protocol.ReportResponse {
	a.t.Helper()
	var v protocol.ReportResponse
	read(a.t, a.call("POST", "/api/v1/agent/report", request, cert, 200), &v)
	return v
}

func TestAdminBoundaryAndStrictJSON(t *testing.T) {
	a := newClient(t)
	cookie := a.cookie
	a.cookie = nil
	a.call("GET", "/api/v1/hosts", nil, nil, 401)
	a.cookie = cookie
	csrf := a.csrf
	a.csrf = ""
	a.call("POST", "/api/v1/sites", `{"name":"Lab"}`, nil, 403)
	a.csrf = csrf
	for _, body := range []string{`{"name":"Lab","extra":true}`, `{"name":"Lab"} {}`, `{"name":"Lab"} garbage`} {
		a.call("POST", "/api/v1/sites", body, nil, 400)
	}
	r := httptest.NewRequest("POST", "https://nex.test/api/v1/sites", strings.NewReader(`{"name":"Lab"}`))
	r.AddCookie(cookie)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "https://evil.test")
	w := httptest.NewRecorder()
	a.c.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-origin mutation: %d", w.Code)
	}
	a.call("POST", "/api/v1/pools", `{"name":"Pool","site_id":"absent"}`, nil, 400)
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 1}, nil, 401)
	a.call("DELETE", "/api/v1/session", nil, nil, 200)
	a.call("GET", "/api/v1/session", nil, nil, 401)
}

func TestEnrollmentRotationRevocationAndReplacement(t *testing.T) {
	a := newClient(t)
	pool := a.pool()
	token, key := a.token(pool, ""), csr(t)
	original := a.enroll(token, key)
	if retry := a.enroll(token, key); retry != original {
		t.Fatal("enrollment retry changed identity")
	}
	a.call("POST", "/api/v1/agent/enroll", protocol.EnrollRequest{Version: 1, Token: token, CSRPEM: csr(t), Hostname: "other"}, nil, 409)
	old := certificate(t, original.CertificatePEM)
	a.report(old, protocol.ReportRequest{Version: 1, Sequence: 20, Inventory: &protocol.Inventory{Hostname: "core", CPUCount: 2}})
	rotate := protocol.RotateRequest{Version: 1, CSRPEM: csr(t)}
	var rotated, retry protocol.EnrollResponse
	read(t, a.call("POST", "/api/v1/agent/rotate", rotate, old, 200), &rotated)
	read(t, a.call("POST", "/api/v1/agent/rotate", rotate, old, 200), &retry)
	if rotated != retry || rotated.HostID != original.HostID || rotated.CertificatePEM == original.CertificatePEM {
		t.Fatal("rotation identity/retry broken")
	}
	current := certificate(t, rotated.CertificatePEM)
	a.report(current, protocol.ReportRequest{Version: 1, Sequence: 21})
	a.call("POST", "/api/v1/hosts/"+original.HostID+"/revoke", nil, nil, 200)
	for _, cert := range []*x509.Certificate{old, current} {
		a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 22}, cert, 401)
		a.call("POST", "/api/v1/agent/rotate", rotate, cert, 401)
	}
	first, second := a.token(pool, original.HostID), a.token(pool, original.HostID)
	replacement := a.enroll(first, csr(t))
	if replacement.HostID != original.HostID {
		t.Fatal("re-enrollment changed host identity")
	}
	hosts := a.hosts()
	if len(hosts) != 1 || hosts[0].Connection != "unknown" || hosts[0].Inventory != nil || hosts[0].LastSeen != nil {
		t.Fatalf("replacement inherited observations: %+v", hosts)
	}
	a.call("POST", "/api/v1/agent/enroll", protocol.EnrollRequest{Version: 1, Token: second, CSRPEM: csr(t), Hostname: "other"}, nil, 409)
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 22}, current, 401)
	a.report(certificate(t, replacement.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1})
	if a.hosts()[0].Connection != "fresh" {
		t.Fatal("replacement sequence was not reset")
	}
}

func TestOperationRetryAndRestartFreshness(t *testing.T) {
	a := newClient(t)
	pool := a.pool()
	host := a.enroll(a.token(pool, ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	path := "/api/v1/hosts/" + host.HostID + "/operations"
	request := map[string]string{"kind": "refresh-inventory", "request_id": "request-1"}
	var op, retry protocol.Operation
	read(t, a.call("POST", path, request, nil, 202), &op)
	read(t, a.call("POST", path, request, nil, 200), &retry)
	if op.ID == "" || op != retry {
		t.Fatal("request retry duplicated operation")
	}
	a.call("POST", path, map[string]string{"kind": "refresh-inventory", "request_id": "request-2"}, nil, 409)
	other := a.enroll(a.token(pool, ""), csr(t))
	a.call("POST", "/api/v1/hosts/"+other.HostID+"/operations", request, nil, 409)
	result := protocol.Result{OperationID: op.ID, Status: "succeeded"}
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 1, Results: []protocol.Result{result}}, cert, 409)
	report := protocol.ReportRequest{Version: 1, Sequence: 1, Inventory: &protocol.Inventory{Hostname: "core", CPUCount: 2, VMs: []protocol.VM{{ID: "vm-1", Name: "guest", State: "running"}}}}
	for range 2 {
		response := a.report(cert, report)
		if len(response.Commands) != 1 || response.Commands[0].OperationID != op.ID {
			t.Fatal("command not durably redelivered")
		}
	}
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 1, Results: []protocol.Result{result}}, certificate(t, other.CertificatePEM), 400)
	cfg := a.c.cfg
	ca := a.c.ca.pem
	if err := a.c.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	a.c, err = Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.c.ca.pem != ca {
		t.Fatal("restart changed CA")
	}
	var recovered protocol.Host
	for _, h := range a.hosts() {
		if h.ID == host.HostID {
			recovered = h
		}
	}
	if recovered.Connection != "stale" || recovered.Inventory == nil || recovered.Inventory.VMs[0].State != "running" {
		t.Fatalf("restart lost observations or claimed fresh: %+v", recovered)
	}
	a.report(cert, report)
	for _, h := range a.hosts() {
		if h.ID == host.HostID && h.Connection != "stale" {
			t.Fatal("replayed report marked host fresh")
		}
	}
	report.Sequence = 2
	report.Results = []protocol.Result{result}
	for range 2 {
		response := a.report(cert, report)
		if len(response.Commands) != 0 || len(response.AcknowledgedResults) != 1 || response.AcknowledgedResults[0] != op.ID {
			t.Fatalf("result reconciliation: %+v", response)
		}
	}
	var ops []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	if len(ops) != 1 || ops[0].Status != "succeeded" || ops[0].ID != op.ID {
		t.Fatalf("lost durable result: %+v", ops)
	}
	var audit []protocol.Audit
	read(t, a.call("GET", "/api/v1/audit", nil, nil, 200), &audit)
	completed := 0
	for _, event := range audit {
		if event.Action == "operation.succeeded" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("result retry produced %d completion events", completed)
	}
	a.c.mu.Lock()
	a.c.seen[host.HostID] = time.Now().Add(-2 * a.c.cfg.StaleAfter)
	a.c.mu.Unlock()
	for _, h := range a.hosts() {
		if h.ID == host.HostID && (h.Connection != "stale" || h.Inventory.VMs[0].State != "running") {
			t.Fatal("stale connectivity changed VM state")
		}
	}
}

func TestReportRejectsInvalidInventory(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.pool(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	for _, inventory := range []*protocol.Inventory{
		{CPUCount: -1},
		{CPUCount: 65537},
		{Checks: []protocol.Check{{Name: "libvirt", Status: "invented"}}},
		{Checks: []protocol.Check{{Name: "libvirt", Status: "healthy"}, {Name: "libvirt", Status: "failed"}}},
		{NICs: []protocol.NIC{{Name: ""}}},
		{Disks: []protocol.Disk{{Name: ""}}},
		{VMs: []protocol.VM{{ID: "", Name: "guest"}}},
	} {
		a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 1, Inventory: inventory}, cert, 400)
	}
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 2, Sequence: 1}, cert, 409)
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 0}, cert, 400)
	if a.hosts()[0].Connection != "unknown" {
		t.Fatal("invalid report updated host state")
	}
}
