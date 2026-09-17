// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/controller"
	"nex.local/nex/internal/vmhost"
)

func privateDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestHandoffDurableSecrets(t *testing.T) {
	c := HandoffConfig{AgentStateDir: privateDir(t), ControllerURL: "https://controller.test", RequestID: "deployment-1", SiteName: "Site", VMID: "vm", Name: "Controller", AdminPassword: "never-write-this"}
	first, err := loadHandoffState(c)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadHandoffState(c)
	if err != nil || first != second || len(first.Token) != 64 {
		t.Fatalf("unstable token: %v", err)
	}
	path := filepath.Join(c.AgentStateDir, "handoff.json")
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if strings.Contains(string(data), c.AdminPassword) || info.Mode().Perm() != 0600 {
		t.Fatal("bootstrap secret storage is not private")
	}
	changed := c
	changed.VMID = "another-vm"
	if _, err = loadHandoffState(changed); err == nil {
		t.Fatal("accepted changed deployment")
	}
	first.Token = ""
	first.Completed = true
	if err = saveHandoffState(c.AgentStateDir, first); err != nil {
		t.Fatal(err)
	}
	if _, err = loadHandoffState(c); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), second.Token) {
		t.Fatal("completed bootstrap retained token")
	}
}

// This runs the actual Controller's TLS, session, bootstrap transaction and CSR
// enrollment. Closing a real connection after commit simulates a lost response.
// Native libvirt observation/appliance boot is intentionally outside this check.
func TestHandoffControllerLostResponses(t *testing.T) {
	ctl, err := controller.Open(controller.Config{DataDir: privateDir(t), Username: "root", Password: "test-controller-password"})
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	var dropBootstrap, dropEnroll atomic.Bool
	dropBootstrap.Store(true)
	dropEnroll.Store(true)
	handler := ctl.Handler()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		drop := r.URL.Path == "/api/v1/appliances/bootstrap" && dropBootstrap.CompareAndSwap(true, false) || r.URL.Path == "/api/v1/agent/enroll" && dropEnroll.CompareAndSwap(true, false)
		if drop {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code != 201 && recorder.Code != 200 {
				t.Errorf("lost reply was not committed: %d %s", recorder.Code, recorder.Body.String())
			}
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			conn.Close()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	defer srv.Close()
	trust := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	h, err := newHandoffHTTP(srv.URL, trust)
	if err != nil {
		t.Fatal(err)
	}
	defer h.client.CloseIdleConnections()
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if _, err = h.call(context.Background(), "POST", "/api/v1/session", map[string]string{"username": "root", "password": "test-controller-password"}, &session); err != nil {
		t.Fatal(err)
	}
	h.csrf = session.CSRF
	cfg := HandoffConfig{AgentStateDir: privateDir(t), ControllerURL: srv.URL, TrustPEM: trust, RequestID: "bootstrap-test", SiteName: "Site"}
	state, err := loadHandoffState(cfg)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"request_id": cfg.RequestID, "site_name": cfg.SiteName, "enrollment_token": state.Token}
	var bootstrap struct {
		PoolID string `json:"pool_id"`
	}
	if _, err = h.call(context.Background(), "POST", "/api/v1/appliances/bootstrap", body, &bootstrap); err == nil {
		t.Fatal("expected lost bootstrap response")
	}
	if _, err = h.call(context.Background(), "POST", "/api/v1/appliances/bootstrap", body, &bootstrap); err != nil {
		t.Fatal(err)
	}
	// Bootstrap creates one site; the pool appears only when the appliance host enrolls.
	for path, want := range map[string]int{"/api/v1/sites": 1, "/api/v1/pools": 0} {
		var rows []json.RawMessage
		if _, err = h.call(context.Background(), "GET", path, nil, &rows); err != nil || len(rows) != want {
			t.Fatalf("scope %s: %d %v", path, len(rows), err)
		}
	}
	savedCSRF := h.csrf
	h.csrf = ""
	if status, _ := h.call(context.Background(), "POST", "/api/v1/appliances/bootstrap", body, nil); status != 403 {
		t.Fatal("bootstrap accepted missing CSRF")
	}
	h.csrf = savedCSRF
	store, err := agent.OpenStore(cfg.AgentStateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = agent.Enroll(context.Background(), store, srv.URL, trust, state.Token, ""); err == nil {
		t.Fatal("expected lost enrollment response")
	}
	csr := store.State.CSRPEM
	store.Close()
	store, err = agent.OpenStore(cfg.AgentStateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = agent.Enroll(context.Background(), store, srv.URL, trust, state.Token, ""); err != nil {
		t.Fatal(err)
	}
	if store.State.HostID == "" || store.State.CSRPEM != csr {
		t.Fatal("retry changed enrollment key")
	}
	var hosts []json.RawMessage
	if _, err = h.call(context.Background(), "GET", "/api/v1/hosts", nil, &hosts); err != nil || len(hosts) != 1 {
		t.Fatalf("duplicate host: %d %v", len(hosts), err)
	}
	untrusted := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("untrusted Controller received request") }))
	defer untrusted.Close()
	bad, err := newHandoffHTTP(untrusted.URL, trust)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.client.CloseIdleConnections()
	// httptest uses the same certificate across servers; a hostname mismatch proves
	// verification is active independently of that shared test certificate.
	bad.origin = strings.Replace(bad.origin, "127.0.0.1", "localhost", 1)
	if _, err = bad.call(context.Background(), "GET", "/api/v1/session", nil, nil); err == nil {
		t.Fatal("accepted wrong TLS hostname")
	}
}

func TestResumeUnenrolledReleasesStore(t *testing.T) {
	dir := privateDir(t)
	if err := ResumeAgent(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	s, err := agent.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

// The Controller and agent HTTP/report implementations are real; only virsh
// output is simulated. This verifies orchestration, not bootability or hardware.
func TestHandoffAdoptsObservedApplianceAndResumesOffline(t *testing.T) {
	const vmID = "42d944ac-093d-4240-99a1-d44f3301f124"
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$4\" in\nlist) printf '%s\\n' '" + vmID + "';;\ndomstate) printf 'running\\n';;\ndomname) printf 'Controller\\n';;\nevent) exit 0;;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctl, err := controller.Open(controller.Config{DataDir: privateDir(t), Username: "root", Password: "test-controller-password"})
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	var ready atomic.Int32
	var markerReady, uiReady atomic.Bool
	adopted := make(chan struct{}, 1)
	uiChecked := make(chan struct{}, 1)
	handler := ctl.Handler()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/session" && r.Method == "GET" && ready.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/appliance-ready.json" {
			if !markerReady.Load() {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"bootstrap_version":1,"status":"ready"}`))
			return
		}
		if r.URL.Path == "/" {
			if !uiReady.Load() {
				w.WriteHeader(403)
			} else {
				w.Write([]byte("<!doctype html><title>Nex</title>"))
			}
			select {
			case uiChecked <- struct{}{}:
			default:
			}
			return
		}
		if r.URL.Path == "/api/v1/appliances/adopt" {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			w.Write(rec.Body.Bytes())
			if rec.Code == 200 || rec.Code == 201 {
				select {
				case adopted <- struct{}{}:
				default:
				}
			}
			return
		}
		handler.ServeHTTP(w, r)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequestClientCert}
	srv.StartTLS()
	defer srv.Close()
	trust := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	cfg := HandoffConfig{ControllerURL: srv.URL, TrustPEM: trust, AdminPassword: "test-controller-password", SiteName: "Site", VMID: vmID, Name: "Controller", RequestID: "complete-handoff", AgentStateDir: privateDir(t)}
	// Handoff owns its readiness/adoption timeouts; its parent represents the
	// service lifetime, which must also cover enrollment and subsequent recovery.
	ctx, cancel := context.WithCancel(t.Context())
	defer func() {
		cancel()
		releaseHandoffAgent(cfg.AgentStateDir)
	}()
	result := make(chan error, 1)
	go func() { result <- Handoff(ctx, cfg) }()
	select {
	case <-adopted:
	case err = <-result:
		t.Fatalf("handoff returned before adoption: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err = <-result:
		t.Fatalf("handoff completed while UI403 and readiness marker absent: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	markerReady.Store(true)
	select {
	case <-uiChecked:
	case err = <-result:
		t.Fatalf("handoff returned before checking UI: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err = <-result:
		t.Fatalf("handoff completed with marker but UI403: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	uiReady.Store(true)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	persisted, err := loadHandoffState(cfg)
	if err != nil || !persisted.Completed || persisted.Token != "" {
		t.Fatalf("handoff incomplete: %+v %v", persisted, err)
	}

	// Historical Completed records lacked first-boot evidence and must not
	// bypass the new gate merely because adoption previously succeeded.
	persisted.ReadyVersion = 0
	if err = saveHandoffState(cfg.AgentStateDir, persisted); err != nil {
		t.Fatal(err)
	}
	markerReady.Store(false)
	go func() { result <- Handoff(ctx, cfg) }()
	select {
	case err = <-result:
		t.Fatalf("historical completion bypassed readiness: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	markerReady.Store(true)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	persisted, err = loadHandoffState(cfg)
	if err != nil || persisted.ReadyVersion != 1 {
		t.Fatalf("readiness evidence not persisted: %v", err)
	}
	handoffAgents.Lock()
	a := handoffAgents.entries[cfg.AgentStateDir]
	hostID := a.hostID
	handoffAgents.Unlock()
	cancel()
	<-a.done
	// Lock acquisition waits for the old agent's Store to be released.
	for {
		handoffAgents.Lock()
		_, exists := handoffAgents.entries[cfg.AgentStateDir]
		handoffAgents.Unlock()
		if !exists {
			break
		}
		time.Sleep(time.Millisecond)
	}
	srv.Close()
	resumed, stop := context.WithCancel(context.Background())
	if err = ResumeAgent(resumed, cfg.AgentStateDir); err != nil {
		stop()
		t.Fatal(err)
	}
	handoffAgents.Lock()
	a = handoffAgents.entries[cfg.AgentStateDir]
	same := a.hostID == hostID
	handoffAgents.Unlock()
	if !same {
		stop()
		t.Fatal("resume changed host identity")
	}
	// Completed handoff must also return during an outage without needing admin credentials.
	cfg.AdminPassword = ""
	if err = Handoff(resumed, cfg); err != nil {
		stop()
		t.Fatal(err)
	}
	stop()
	<-a.done
}

func TestDHCPResumeDefersOldAgentUntilDiscovery(t *testing.T) {
	for _, status := range []string{"needs_attention", "ready"} {
		t.Run(status, func(t *testing.T) {
			s, cfg := testService(t)
			store, err := agent.OpenStore(cfg.AgentStateDir)
			if err != nil {
				t.Fatal(err)
			}
			store.State.HostID = "existing-host"
			store.State.Controller = "https://192.0.2.10:8443"
			store.State.ControllerServerName = "controller"
			if err = store.Save(); err != nil {
				t.Fatal(err)
			}
			store.Close()
			s.operations["request-001"] = deployment{Request: DeployRequest{NetworkMode: "dhcp"}, Operation: vmhost.Operation{ID: "request-001", Status: status}}
			// Hold the worker slot to isolate startup ordering from VM command fixtures.
			s.active["request-001"] = true
			if err = s.Resume(); err != nil {
				t.Fatal(err)
			}
			handoffAgents.Lock()
			_, started := handoffAgents.entries[cfg.AgentStateDir]
			handoffAgents.Unlock()
			if started {
				t.Fatal("resumed old DHCP endpoint before discovery")
			}
			check, err := agent.OpenStore(cfg.AgentStateDir)
			if err != nil {
				t.Fatalf("old agent locked state before discovery: %v", err)
			}
			check.Close()
		})
	}
}

func TestDHCPHandoffUsesHostnameCertificateAtObservedIP(t *testing.T) {
	cert, key, err := instanceTLS("controller")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()
	h, err := newHandoffHTTP(srv.URL, cert, "controller")
	if err != nil {
		t.Fatal(err)
	}
	defer h.client.CloseIdleConnections()
	if _, err = h.call(context.Background(), "GET", "/", nil, nil); err != nil {
		t.Fatalf("hostname trust at observed IP failed: %v", err)
	}
	wrong, err := newHandoffHTTP(srv.URL, cert, "other")
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.client.CloseIdleConnections()
	if _, err = wrong.call(context.Background(), "GET", "/", nil, nil); err == nil {
		t.Fatal("wrong hostname accepted")
	}
}
