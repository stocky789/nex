// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
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
func (a *testClient) site() string {
	a.t.Helper()
	var s protocol.Site
	read(a.t, a.call("POST", "/api/v1/sites", map[string]string{"name": "Lab"}, nil, 201), &s)
	return s.ID
}
func (a *testClient) token(site, host string) string {
	a.t.Helper()
	var v struct {
		Token string `json:"token"`
	}
	read(a.t, a.call("POST", "/api/v1/enrollment-tokens", map[string]string{"site_id": site, "host_id": host}, nil, 201), &v)
	return v.Token
}

// poolOf returns the pool created automatically for an enrolled host.
func (a *testClient) poolOf(host string) string {
	a.t.Helper()
	for _, h := range a.hosts() {
		if h.ID == host {
			return h.PoolID
		}
	}
	a.t.Fatal("host missing")
	return ""
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
	a.call("POST", "/api/v1/enrollment-tokens", `{"site_id":"absent"}`, nil, 400)
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 1}, nil, 401)
	a.call("DELETE", "/api/v1/session", nil, nil, 200)
	a.call("GET", "/api/v1/session", nil, nil, 401)
}

func TestEnrollmentRotationRevocationAndReplacement(t *testing.T) {
	a := newClient(t)
	pool := a.site()
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
	pool := a.site()
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
	host := a.enroll(a.token(a.site(), ""), csr(t))
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

func TestEnrollmentCreatesPoolWithMaster(t *testing.T) {
	a := newClient(t)
	site := a.site()
	host := a.enroll(a.token(site, ""), csr(t))
	var pools []protocol.Pool
	read(t, a.call("GET", "/api/v1/pools", nil, nil, 200), &pools)
	if len(pools) != 1 || pools[0].SiteID != site || pools[0].Name != "core" || pools[0].MasterHostID != host.HostID || pools[0].ID != a.poolOf(host.HostID) {
		t.Fatalf("enrollment did not create a pool mastered by the host: %+v", pools)
	}
	var audits []protocol.Audit
	read(t, a.call("GET", "/api/v1/audit", nil, nil, 200), &audits)
	found := false
	for _, entry := range audits {
		if entry.Action == "pool.create" && entry.ResourceID == pools[0].ID && entry.Actor == "host:"+host.HostID {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing pool.create audit: %+v", audits)
	}
	// Administrators never create pools by hand; the route no longer exists.
	a.call("POST", "/api/v1/pools", `{"name":"Pool","site_id":"`+site+`"}`, nil, 404)
	// Re-enrollment keeps the existing pool and requires the host's own site.
	a.call("POST", "/api/v1/hosts/"+host.HostID+"/revoke", nil, nil, 200)
	other := a.site()
	a.call("POST", "/api/v1/enrollment-tokens", map[string]string{"site_id": other, "host_id": host.HostID}, nil, 409)
	replacement := a.enroll(a.token(site, host.HostID), csr(t))
	read(t, a.call("GET", "/api/v1/pools", nil, nil, 200), &pools)
	if replacement.HostID != host.HostID || len(pools) != 1 || pools[0].MasterHostID != host.HostID || a.poolOf(host.HostID) != pools[0].ID {
		t.Fatalf("re-enrollment changed pool membership: %+v", pools)
	}
}

// Databases from the manual-pool era gain a master per pool and lose pools that never had a host.
func TestOpenMigratesManualPools(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := dir + "/controller.db"
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE schema_version(version INTEGER NOT NULL); INSERT INTO schema_version VALUES(1);
 CREATE TABLE sites(id TEXT PRIMARY KEY,name TEXT NOT NULL);
 CREATE TABLE pools(id TEXT PRIMARY KEY,site_id TEXT NOT NULL REFERENCES sites(id),name TEXT NOT NULL);
 CREATE TABLE hosts(id TEXT PRIMARY KEY,pool_id TEXT NOT NULL REFERENCES pools(id),hostname TEXT NOT NULL,revoked INTEGER NOT NULL DEFAULT 0,last_seen TEXT,inventory TEXT,sequence INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE tokens(hash TEXT PRIMARY KEY,pool_id TEXT NOT NULL REFERENCES pools(id),host_id TEXT,expires INTEGER NOT NULL,csr TEXT,response TEXT);
 CREATE TABLE appliance_bootstraps(request_id TEXT PRIMARY KEY,site_name TEXT NOT NULL,pool_name TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,site_id TEXT NOT NULL REFERENCES sites(id),pool_id TEXT NOT NULL REFERENCES pools(id));
 INSERT INTO sites VALUES('s','Lab');
 INSERT INTO pools VALUES('shared','s','Shared'); INSERT INTO pools VALUES('empty','s','Empty'); INSERT INTO pools VALUES('gone','s','Gone');
 INSERT INTO hosts(id,pool_id,hostname,revoked) VALUES('old','shared','old',1); INSERT INTO hosts(id,pool_id,hostname) VALUES('second','shared','second'); INSERT INTO hosts(id,pool_id,hostname) VALUES('third','shared','third');
 INSERT INTO hosts(id,pool_id,hostname,revoked) VALUES('retired','gone','retired',1);
 INSERT INTO tokens(hash,pool_id,host_id,expires) VALUES('t','empty','',0);
 INSERT INTO appliance_bootstraps VALUES('r','Lab','Shared','h','s','shared')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	c, err := Open(Config{DataDir: dir, Username: "admin", Password: "a-long-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rows, err := c.db.Query("SELECT id,master_host_id FROM pools ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, master string
		if err := rows.Scan(&id, &master); err != nil {
			t.Fatal(err)
		}
		got[id] = master
	}
	if len(got) != 2 || got["shared"] != "second" || got["gone"] != "retired" {
		t.Fatalf("migration masters/orphans wrong: %v", got)
	}
	for table, column := range map[string]string{"tokens": "site_id", "appliance_bootstraps": "site_id"} {
		cols, err := columns(c.db, table)
		if err != nil || !cols[column] || cols["pool_id"] {
			t.Fatalf("%s not recreated: %v %v", table, cols, err)
		}
	}
	var n int
	if c.db.QueryRow("SELECT count(*) FROM tokens").Scan(&n) != nil || n != 0 {
		t.Fatal("old pool-bound tokens survived")
	}
}
