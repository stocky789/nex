// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/vmhost"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRootAuth(_ context.Context, password string) error {
	if password != "local-admin-password" {
		return errors.New("invalid credentials")
	}
	return nil
}

func testService(t *testing.T) (*Service, Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "state"), AgentStateDir: filepath.Join(dir, "agent"), Authenticate: testRootAuth, Engine: vmhost.Config{StateDir: filepath.Join(dir, "vmstate"), StorageDir: filepath.Join(dir, "storage"), Bridge: "br0"}}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, cfg
}
func callLocal(s *Service, method, path string, in any, cookie, csrf, origin string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(in)
	r := httptest.NewRequest(method, "https://core.test"+path, bytes.NewReader(b))
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.Header.Set("X-CSRF-Token", csrf)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "__Host-nex_local_session", Value: cookie})
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func loginLocal(t *testing.T, s *Service) (string, string) {
	t.Helper()
	w := callLocal(s, "POST", "/api/local/v1/session", map[string]string{"username": "root", "password": "local-admin-password"}, "", "", "https://core.test")
	if w.Code != 200 {
		t.Fatalf("login %d %s", w.Code, w.Body)
	}
	var v map[string]string
	json.Unmarshal(w.Body.Bytes(), &v)
	c := w.Result().Cookies()[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	return c.Value, v["csrf_token"]
}
func TestAuthOriginCSRFAndNoStoredCredentials(t *testing.T) {
	s, cfg := testService(t)
	if w := callLocal(s, "GET", "/api/local/v1/deployments", nil, "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := callLocal(s, "POST", "/api/local/v1/session", map[string]string{"username": "root", "password": "local-admin-password"}, "", "", "https://evil.test"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	cookie, csrf := loginLocal(t, s)
	if w := callLocal(s, "DELETE", "/api/local/v1/session", nil, cookie, "", "https://core.test"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := callLocal(s, "GET", "/api/local/v1/session", nil, cookie, "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := callLocal(s, "DELETE", "/api/local/v1/session", nil, cookie, csrf, "https://core.test"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := callLocal(s, "GET", "/api/local/v1/session", nil, cookie, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("local credential storage must not be created", err)
	}
	s.Close()
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loginLocal(t, reopened)
}
func TestInvalidDeploymentNeverPersistsOrMutates(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	valid := DeployRequest{RequestID: "request-001", Name: "nex-controller", Hostname: "controller", CPUs: 2, MemoryMiB: 4096, DiskGiB: 32, AddressCIDR: "192.168.1.10/24", Gateway: "192.168.1.1", DNS: []string{"192.168.1.1"}, AdminPassword: "controller-password", SiteName: "Site", Bridge: "br0", StorageDir: s.cfg.Engine.StorageDir}
	cases := []DeployRequest{}
	v := valid
	v.AdminPassword = ""
	cases = append(cases, v)
	v = valid
	v.Name = "not a DNS name"
	cases = append(cases, v)
	v = valid
	v.AddressCIDR = "192.168.1.255/24"
	cases = append(cases, v)
	v = valid
	v.DNS = []string{"2001:db8::1"}
	cases = append(cases, v)
	v = valid
	v.DiskGiB = 4096
	cases = append(cases, v)
	v = valid
	v.Bridge = "other"
	cases = append(cases, v)
	for _, v := range cases {
		w := callLocal(s, "POST", "/api/local/v1/deployments", v, cookie, csrf, "https://core.test")
		if w.Code != 400 {
			t.Fatalf("invalid accepted %d %s", w.Code, w.Body)
		}
		if len(s.operations) != 0 {
			t.Fatal("invalid request persisted deployment")
		}
	}
}
func TestReadyRetryIsDurableNoOpAndSecretsAreNotReturned(t *testing.T) {
	s, cfg := testService(t)
	cookie, csrf := loginLocal(t, s)
	s.operations["request-001"] = deployment{Operation: vmhost.Operation{ID: "request-001", Status: "ready", Progress: 100}, Request: DeployRequest{AdminPassword: "private-bootstrap-secret"}, Key: "private-TLS-key"}
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/local/v1/deployments", "/api/local/v1/deployments/request-001"} {
		w := callLocal(s, "GET", path, nil, cookie, "", "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "private-") {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := callLocal(s, "POST", "/api/local/v1/deployments/request-001/retry", struct{}{}, cookie, csrf, "https://core.test")
	if w.Code != 202 || len(s.active) != 0 {
		t.Fatal(w.Code, s.active)
	}
	s.Close()
	again, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if again.operations["request-001"].Operation.Status != "ready" {
		t.Fatal("lost durable operation")
	}
}
func TestLoginThrottleAndStrictJSON(t *testing.T) {
	s, _ := testService(t)
	v := map[string]string{"username": "root", "password": "wrong"}
	for i := 0; i < 5; i++ {
		if w := callLocal(s, "POST", "/api/local/v1/session", v, "", "", "https://core.test"); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := callLocal(s, "POST", "/api/local/v1/session", v, "", "", "https://core.test"); w.Code != 429 {
		t.Fatal(w.Code)
	}
	v["extra"] = "rejected"
	if w := callLocal(s, "POST", "/api/local/v1/session", v, "", "", "https://core.test"); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestGeneratedTLSBindsOnlySelectedIP(t *testing.T) {
	cert, key, err := instanceTLS("192.168.1.10/24")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	if pair.Leaf.VerifyHostname("192.168.1.10") != nil || pair.Leaf.VerifyHostname("192.168.1.11") == nil {
		t.Fatal("TLS address binding failed")
	}
}

func TestDHCPDeploymentCertificate(t *testing.T) {
	v := DeployRequest{NetworkMode: "dhcp", Hostname: "controller"}
	cert, key, err := deploymentTLS(v)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	if pair.Leaf.VerifyHostname("controller") != nil || pair.Leaf.VerifyHostname("other") == nil {
		t.Fatal("DHCP certificate hostname identity incorrect")
	}
}

func TestPowerDoesNotRequireTypedHostname(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	for _, action := range []string{"start", "shutdown", "force-stop"} {
		w := callLocal(s, "POST", "/api/local/v1/vms/not-owned/actions", map[string]string{"request_id": "power-001", "action": action}, cookie, csrf, "https://core.test")
		if w.Code != 409 || !strings.Contains(w.Body.String(), "VM action refused") {
			t.Fatalf("action %s did not reach ownership validation: %d %s", action, w.Code, w.Body)
		}
	}
}
