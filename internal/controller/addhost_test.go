// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/agent"
)

// controllerTLSServer serves the Controller under test over a real TLS listener with
// a self-signed HTTPS certificate that is deliberately NOT chained to the host-identity
// CA, mirroring the deployed appliance. The push must send this served certificate as
// the host's trust; pushing c.ca.pem instead makes the fake host's agent.Enroll fail TLS.
func controllerTLSServer(t *testing.T, c *Controller) *httptest.Server {
	return controllerTLSServerWith(t, c, func(tmpl *x509.Certificate) {
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	})
}

// controllerTLSServerWith lets a test shape the served certificate's names: the appliance
// deployed with DHCP carries only its hostname, never the address a host connects to.
func controllerTLSServerWith(t *testing.T, c *Controller, shape func(*x509.Certificate)) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "controller"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	shape(tmpl)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(c.Handler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	c.cfg.ServerCertPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return srv
}

// fakeLocalHost stands in for nex-core-local: it serves the same three routes the
// real host exposes for the Controller's add-host push, and its enrollment handler
// really calls agent.Enroll against the Controller under test so the resulting host
// row, pool, and audit trail are the genuine product of that flow.
type fakeLocalHost struct {
	t          *testing.T
	password   string
	csrf       string
	dir        string // temp agent state dir; unique per fakeLocalHost
	failEnroll bool
	older      bool // a nex-core-local from before server_name rejects the field it does not know

	mu         sync.Mutex
	loginCalls int
	serverName string // server_name from the last enrollment push
}

func newFakeLocalHost(t *testing.T, password string) *fakeLocalHost {
	t.Helper()
	dir := t.TempDir()
	// agent.OpenStore requires a private (0700) directory; t.TempDir() is not
	// guaranteed to be created that strictly narrow depending on umask.
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return &fakeLocalHost{t: t, password: password, csrf: "fake-host-csrf", dir: dir}
}

func (f *fakeLocalHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/local/v1/session":
		f.mu.Lock()
		f.loginCalls++
		f.mu.Unlock()
		var v struct{ Username, Password string }
		json.NewDecoder(r.Body).Decode(&v)
		if v.Username != "root" || v.Password != f.password {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "local_session", Value: "session-token", Path: "/"})
		json.NewEncoder(w).Encode(map[string]string{"username": "root", "csrf_token": f.csrf})
	case r.Method == "DELETE" && r.URL.Path == "/api/local/v1/session":
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	case r.Method == "POST" && r.URL.Path == "/api/local/v1/enrollment":
		if _, err := r.Cookie("local_session"); err != nil || r.Header.Get("X-CSRF-Token") != f.csrf {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		if f.failEnroll {
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]string{"error": "simulated local enrollment failure"})
			return
		}
		var v struct {
			ControllerURL string `json:"controller_url"`
			CAPEM         string `json:"ca_pem"`
			Token         string `json:"token"`
			ReplaceHost   string `json:"replace_host"`
			ServerName    string `json:"server_name"`
		}
		raw, _ := io.ReadAll(r.Body)
		if f.older && bytes.Contains(raw, []byte(`"server_name"`)) {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON request"})
			return
		}
		json.Unmarshal(raw, &v)
		f.mu.Lock()
		f.serverName = v.ServerName
		f.mu.Unlock()
		store, err := agent.OpenStore(f.dir)
		if err != nil {
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		defer store.Close()
		if err := agent.Enroll(r.Context(), store, v.ControllerURL, v.CAPEM, v.Token, v.ReplaceHost, v.ServerName); err != nil {
			w.WriteHeader(502)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"host_id": store.State.HostID})
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeLocalHost) loginAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loginCalls
}

func (f *fakeLocalHost) pushedServerName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.serverName
}

// addr strips the scheme from an httptest.Server URL for use as an "address" field.
func addr(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "https://") }

func TestAddHostProbe(t *testing.T) {
	a := newClient(t)
	host := httptest.NewTLSServer(newFakeLocalHost(t, "irrelevant"))
	defer host.Close()

	var v probeResponse
	read(t, a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": addr(host)}, nil, 200), &v)
	if v.Fingerprint != fingerprintOf(host.Certificate()) {
		t.Fatalf("probe fingerprint mismatch: %s", v.Fingerprint)
	}
	if v.Address != addr(host) || v.Subject == "" || v.NotAfter.IsZero() {
		t.Fatalf("incomplete probe response: %+v", v)
	}

	a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": "not a host"}, nil, 400)
	a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": "127.0.0.1:1"}, nil, 502)

	cookie := a.cookie
	a.cookie = nil
	a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": addr(host)}, nil, 401)
	a.cookie = cookie
}

func TestAddSiteHostSuccess(t *testing.T) {
	a := newClient(t)
	controller := controllerTLSServer(t, a.c)
	defer controller.Close()
	site := a.site()

	fake := newFakeLocalHost(t, "correct horse battery staple")
	host := httptest.NewTLSServer(fake)
	defer host.Close()
	var probe probeResponse
	read(t, a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": addr(host)}, nil, 200), &probe)

	var v addHostResponse
	read(t, a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: probe.Fingerprint, RootPassword: fake.password, ControllerURL: controller.URL,
	}, nil, 200), &v)
	if v.HostID == "" || v.PoolID == "" {
		t.Fatalf("incomplete add-host response: %+v", v)
	}
	if got := fake.pushedServerName(); got != "" {
		t.Fatalf("the certificate covers the Controller address, yet server_name %q was pushed", got)
	}

	hosts := a.hosts()
	if len(hosts) != 1 || hosts[0].ID != v.HostID || hosts[0].PoolID != v.PoolID {
		t.Fatalf("host not recorded under its pool: %+v", hosts)
	}
	if a.poolOf(v.HostID) != v.PoolID {
		t.Fatal("pool_id does not match the pool the enrollment created")
	}
	var pools []struct {
		ID           string
		MasterHostID string `json:"master_host_id"`
		SiteID       string `json:"site_id"`
	}
	read(t, a.call("GET", "/api/v1/pools", nil, nil, 200), &pools)
	found := false
	for _, p := range pools {
		if p.ID == v.PoolID {
			found = true
			if p.MasterHostID != v.HostID || p.SiteID != site {
				t.Fatalf("pool not mastered by the new host in this site: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("auto-created pool missing")
	}

	var audits []struct {
		Action     string
		ResourceID string `json:"resource_id"`
		Detail     string
	}
	read(t, a.call("GET", "/api/v1/audit", nil, nil, 200), &audits)
	var gotToken, gotAdd bool
	for _, e := range audits {
		if e.Action == "enrollment-token.create" && e.ResourceID == site {
			gotToken = true
		}
		if e.Action == "host.add" && e.ResourceID == site {
			gotAdd = true
		}
		if strings.Contains(e.Detail, fake.password) {
			t.Fatal("root password leaked into audit detail")
		}
	}
	if !gotToken || !gotAdd {
		t.Fatalf("missing enrollment-token.create/host.add audit rows: %+v", audits)
	}

	var unused int
	if a.c.db.QueryRow("SELECT count(*) FROM tokens WHERE csr IS NULL").Scan(&unused) != nil || unused != 0 {
		t.Fatalf("unused token row left behind: %d", unused)
	}
}

// addHostByIPTo enrolls a fake host against a Controller served under the given certificate
// shape, with the Controller address given by IP as an operator would type it.
func addHostByIPTo(t *testing.T, shape func(*x509.Certificate), older bool, status int) (*fakeLocalHost, map[string]string) {
	t.Helper()
	a := newClient(t)
	controller := controllerTLSServerWith(t, a.c, shape)
	t.Cleanup(controller.Close)
	site := a.site()
	fake := newFakeLocalHost(t, "correct horse battery staple")
	fake.older = older
	host := httptest.NewTLSServer(fake)
	t.Cleanup(host.Close)
	var probe probeResponse
	read(t, a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": addr(host)}, nil, 200), &probe)
	var body map[string]string
	read(t, a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: probe.Fingerprint, RootPassword: fake.password, ControllerURL: controller.URL,
	}, nil, status), &body)
	return fake, body
}

// TestAddSiteHostSendsCertificateServerName reproduces adding a host to an appliance deployed
// with DHCP: its certificate names only its hostname and the operator gives the Controller
// address by IP. Without the certificate's name the host's own TLS check fails with
// "cannot validate certificate ... because it doesn't contain any IP SANs".
func TestAddSiteHostSendsCertificateServerName(t *testing.T) {
	fake, body := addHostByIPTo(t, func(tmpl *x509.Certificate) { tmpl.DNSNames = []string{"nex-controller"} }, false, 200)
	if body["host_id"] == "" {
		t.Fatalf("incomplete add-host response: %v", body)
	}
	if got := fake.pushedServerName(); got != "nex-controller" {
		t.Fatalf("server_name pushed = %q, want the certificate's hostname", got)
	}
}

// An older nex-core-local rejects the field; the operator must be told what to do, not
// shown "invalid JSON request".
func TestAddSiteHostOlderHostRejectsServerName(t *testing.T) {
	_, body := addHostByIPTo(t, func(tmpl *x509.Certificate) { tmpl.DNSNames = []string{"nex-controller"} }, true, 502)
	if !strings.Contains(body["error"], "nex-controller") || !strings.Contains(body["error"], "upgrade") {
		t.Fatalf("expected the certificate name and the upgrade in the error: %v", body)
	}
}

func TestAddSiteHostFingerprintMismatch(t *testing.T) {
	a := newClient(t)
	controllerTLSServer(t, a.c).Close() // configures the served certificate the push sends as trust
	site := a.site()
	fake := newFakeLocalHost(t, "correct horse battery staple")
	host := httptest.NewTLSServer(fake)
	defer host.Close()

	wrong := "sha256:" + strings.Repeat("0", 64)
	var body map[string]string
	read(t, a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: wrong, RootPassword: fake.password, ControllerURL: "https://controller.test",
	}, nil, 502), &body)
	if !strings.Contains(body["error"], "fingerprint") {
		t.Fatalf("expected fingerprint error, got %v", body)
	}
	if fake.loginAttempts() != 0 {
		t.Fatal("host received a login request despite a fingerprint mismatch")
	}
	var n int
	if a.c.db.QueryRow("SELECT count(*) FROM tokens").Scan(&n) != nil || n != 0 {
		t.Fatal("token minted despite fingerprint mismatch")
	}
}

func TestAddSiteHostWrongPassword(t *testing.T) {
	a := newClient(t)
	controllerTLSServer(t, a.c).Close() // configures the served certificate the push sends as trust
	site := a.site()
	fake := newFakeLocalHost(t, "correct horse battery staple")
	host := httptest.NewTLSServer(fake)
	defer host.Close()
	var probe probeResponse
	read(t, a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": addr(host)}, nil, 200), &probe)

	a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: probe.Fingerprint, RootPassword: "wrong password", ControllerURL: "https://controller.test",
	}, nil, 401)
	var n int
	if a.c.db.QueryRow("SELECT count(*) FROM tokens").Scan(&n) != nil || n != 0 {
		t.Fatal("token minted despite rejected password")
	}
}

func TestAddSiteHostUnknownSite(t *testing.T) {
	a := newClient(t)
	a.call("POST", "/api/v1/sites/absent/hosts", addHostRequest{
		Address: "127.0.0.1:9443", Fingerprint: "sha256:" + strings.Repeat("0", 64),
		RootPassword: "x", ControllerURL: "https://controller.test",
	}, nil, 400)
}

func TestAddSiteHostReEnrollmentRequiresRevokedHost(t *testing.T) {
	a := newClient(t)
	site := a.site()
	host := a.enroll(a.token(site, ""), csr(t))
	// host is active, not revoked: re-enrollment targeting it must be refused before any network attempt.
	a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: "127.0.0.1:9443", Fingerprint: "sha256:" + strings.Repeat("0", 64),
		RootPassword: "x", ControllerURL: "https://controller.test", HostID: host.HostID,
	}, nil, 409)
}

// TestAddSiteHostReEnrollmentSuccess exercises the actual re-enrollment success path
// through the push flow end to end (not just the up-front rejection covered by
// TestAddSiteHostReEnrollmentRequiresRevokedHost): add a host, revoke it, then add it
// again by address+password with host_id set, and confirm the same pool is kept and
// the host is no longer revoked.
func TestAddSiteHostReEnrollmentSuccess(t *testing.T) {
	a := newClient(t)
	controller := controllerTLSServer(t, a.c)
	defer controller.Close()
	site := a.site()

	fake := newFakeLocalHost(t, "correct horse battery staple")
	host := httptest.NewTLSServer(fake)
	defer host.Close()
	var probe probeResponse
	read(t, a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": addr(host)}, nil, 200), &probe)

	var first addHostResponse
	read(t, a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: probe.Fingerprint, RootPassword: fake.password, ControllerURL: controller.URL,
	}, nil, 200), &first)

	a.call("POST", "/api/v1/hosts/"+first.HostID+"/revoke", nil, nil, 200)

	var second addHostResponse
	read(t, a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: probe.Fingerprint, RootPassword: fake.password, ControllerURL: controller.URL,
		HostID: first.HostID,
	}, nil, 200), &second)
	if second.HostID != first.HostID {
		t.Fatalf("re-enrollment changed host identity: %s -> %s", first.HostID, second.HostID)
	}
	if second.PoolID != first.PoolID {
		t.Fatalf("re-enrollment moved host to a different pool: %s -> %s", first.PoolID, second.PoolID)
	}

	hosts := a.hosts()
	if len(hosts) != 1 || hosts[0].ID != first.HostID || hosts[0].Revoked {
		t.Fatalf("host not un-revoked and kept as the sole host: %+v", hosts)
	}
}

func TestAddSiteHostLocalEnrollmentFailure(t *testing.T) {
	a := newClient(t)
	controllerTLSServer(t, a.c).Close() // configures the served certificate the push sends as trust
	site := a.site()
	fake := newFakeLocalHost(t, "correct horse battery staple")
	fake.failEnroll = true
	host := httptest.NewTLSServer(fake)
	defer host.Close()
	var probe probeResponse
	read(t, a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": addr(host)}, nil, 200), &probe)

	var body map[string]string
	read(t, a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: probe.Fingerprint, RootPassword: fake.password, ControllerURL: "https://controller.test",
	}, nil, 502), &body)
	if !strings.Contains(body["error"], "controller.test") {
		t.Fatalf("expected controller URL in error detail: %v", body)
	}
	var n int
	if a.c.db.QueryRow("SELECT count(*) FROM tokens").Scan(&n) != nil || n != 0 {
		t.Fatal("token row left behind after host enrollment failure")
	}
}

func TestAddSiteHostUnauthenticated(t *testing.T) {
	a := newClient(t)
	site := a.site()
	a.cookie = nil
	a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: "127.0.0.1:9443", Fingerprint: "sha256:" + strings.Repeat("0", 64),
		RootPassword: "x", ControllerURL: "https://controller.test",
	}, nil, 401)
}

func TestAddSiteHostRequiresServerCertificate(t *testing.T) {
	a := newClient(t)
	site := a.site()
	fake := newFakeLocalHost(t, "irrelevant")
	host := httptest.NewTLSServer(fake)
	defer host.Close()
	a.c.cfg.ServerCertPEM = ""
	a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: fingerprintOf(host.Certificate()), RootPassword: "irrelevant", ControllerURL: "https://controller.example",
	}, nil, 503)
	if fake.loginAttempts() != 0 {
		t.Fatal("host must not be contacted when the controller cannot supply its trust certificate")
	}
}
