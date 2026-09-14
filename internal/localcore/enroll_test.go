// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

// safeLog serializes writes so a background agent goroutine logging concurrently
// with a test reading the buffer cannot race the buffer itself.
type safeLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeLog) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func captureLog(t *testing.T) *safeLog {
	t.Helper()
	buf := &safeLog{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(nowhereWriter{}) })
	return buf
}

type nowhereWriter struct{}

func (nowhereWriter) Write(p []byte) (int, error) { return len(p), nil }

// fakeController serves POST /api/v1/agent/enroll with a real issued certificate,
// following the pattern in internal/agent/client_test.go. respond controls the
// per-call outcome so tests can simulate the Controller rejecting a token.
func fakeController(t *testing.T, respond func(protocol.EnrollRequest) (int, protocol.EnrollResponse)) (*httptest.Server, string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The embedded agent starts reporting immediately after a successful enrollment;
		// only /enroll is faked here, everything else (e.g. periodic reports) 404s harmlessly.
		if r.URL.Path != "/api/v1/agent/enroll" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req protocol.EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		status, resp := respond(req)
		if status == http.StatusOK && req.HostID != "" {
			// Identity is host-owned: a real Controller adopts the presented host ID.
			resp.HostID = req.HostID
		}
		if status == http.StatusOK {
			block, _ := pem.Decode([]byte(req.CSRPEM))
			csr, err := x509.ParseCertificateRequest(block.Bytes)
			if err != nil {
				t.Error(err)
				return
			}
			cert := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: resp.HostID}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
			der, err := x509.CreateCertificate(rand.Reader, cert, ca, csr.PublicKey, caKey)
			if err != nil {
				t.Fatal(err)
			}
			resp.CertificatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
			resp.CAPEM = caPEM
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	trust := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return srv, trust
}

func hostLocal(t *testing.T, s *Service, cookie string) map[string]any {
	t.Helper()
	w := callLocal(s, "GET", "/api/local/v1/host", nil, cookie, "", "")
	if w.Code != 200 {
		t.Fatalf("host %d %s", w.Code, w.Body)
	}
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEnrollmentRequiresAuthAndCSRF(t *testing.T) {
	s, _ := testService(t)
	if w := callLocal(s, "POST", "/api/local/v1/enrollment", map[string]string{}, "", "", "https://core.test"); w.Code != 401 {
		t.Fatalf("unauthenticated enrollment accepted: %d %s", w.Code, w.Body)
	}
	cookie, _ := loginLocal(t, s)
	if w := callLocal(s, "POST", "/api/local/v1/enrollment", map[string]string{"controller_url": "https://controller.test", "ca_pem": "x", "token": "t"}, cookie, "", "https://core.test"); w.Code != 403 {
		t.Fatalf("missing CSRF accepted: %d %s", w.Code, w.Body)
	}
}

func TestEnrollmentValidationRejectsWithoutPersisting(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	_, trust := fakeController(t, func(protocol.EnrollRequest) (int, protocol.EnrollResponse) {
		t.Fatal("controller must not be contacted for invalid input")
		return 0, protocol.EnrollResponse{}
	})
	valid := map[string]string{"controller_url": "https://controller.test:9443", "ca_pem": trust, "token": "a-valid-token"}
	cases := []map[string]string{}
	for _, mutate := range []func(map[string]string){
		func(v map[string]string) { v["controller_url"] = "http://controller.test" },
		func(v map[string]string) { v["controller_url"] = "https://user:pass@controller.test" },
		func(v map[string]string) { v["controller_url"] = "https://controller.test/path" },
		func(v map[string]string) { v["controller_url"] = "https://controller.test?x=1" },
		func(v map[string]string) { v["ca_pem"] = "not a certificate" },
		func(v map[string]string) { v["token"] = "" },
		func(v map[string]string) { v["token"] = strings.Repeat("a", 257) },
		func(v map[string]string) { v["token"] = "bad\x00token" },
		func(v map[string]string) { v["replace_host"] = strings.Repeat("h", 129) },
	} {
		v := map[string]string{}
		for k, val := range valid {
			v[k] = val
		}
		mutate(v)
		cases = append(cases, v)
	}
	for i, v := range cases {
		w := callLocal(s, "POST", "/api/local/v1/enrollment", v, cookie, csrf, "https://core.test")
		if w.Code != 400 {
			t.Fatalf("case %d: invalid input accepted: %d %s", i, w.Code, w.Body)
		}
	}
	if got := hostLocal(t, s, cookie)["host_id"]; got != "" {
		t.Fatalf("invalid input persisted identity: %v", got)
	}
}

func TestReconcileEnrollmentCacheUsesPublishedStoreState(t *testing.T) {
	_, cfg := testService(t)
	handoffAgents.Lock()
	a, err := getHandoffAgent(cfg.AgentStateDir)
	if err != nil {
		handoffAgents.Unlock()
		t.Fatal(err)
	}
	// Model the state immediately after an accepted replacement was renamed but
	// its directory durability confirmation failed. The HTTP caller still gets
	// 502, yet the restarted registry must not retain old cached credentials.
	a.hostID, a.controller, a.trust = "old-host", "https://old.example", "old trust"
	a.store.State.HostID = "new-host"
	a.store.State.Controller = "https://replacement.example"
	a.store.State.TrustPEM = "replacement trust"
	reconcileEnrollmentCache(a)
	gotHost, gotController, gotTrust := a.hostID, a.controller, a.trust
	handoffAgents.Unlock()
	if gotHost != "new-host" || gotController != "https://replacement.example" || gotTrust != "replacement trust" {
		t.Fatal("published Store state did not replace stale enrollment cache")
	}

	handoffAgents.Lock()
	a.store.State.HostID = ""
	reconcileEnrollmentCache(a)
	gotHost, gotController = a.hostID, a.controller
	handoffAgents.Unlock()
	if gotHost != "" || gotController != "" {
		t.Fatal("pending first enrollment appeared enrolled in local cache")
	}
}

func TestEnrollmentSuccessPersistsAndReportsOnHost(t *testing.T) {
	buf := captureLog(t)
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	if got := hostLocal(t, s, cookie); got["host_id"] != "" || got["controller"] != "" {
		t.Fatalf("fresh host already enrolled: %v", got)
	}
	const token = "s3cr3t-enrollment-token-0123456789"
	srv, trust := fakeController(t, func(req protocol.EnrollRequest) (int, protocol.EnrollResponse) {
		if req.Token != token {
			t.Errorf("unexpected token forwarded: %q", req.Token)
		}
		return http.StatusOK, protocol.EnrollResponse{HostID: "host-1"}
	})
	body := map[string]string{"controller_url": srv.URL, "ca_pem": trust, "token": token}
	w := callLocal(s, "POST", "/api/local/v1/enrollment", body, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("enrollment failed: %d %s", w.Code, w.Body)
	}
	var v map[string]string
	json.Unmarshal(w.Body.Bytes(), &v)
	hostID := v["host_id"]
	if hostID == "" {
		t.Fatalf("unexpected host_id: %v", v)
	}
	host := hostLocal(t, s, cookie)
	if host["host_id"] != hostID || host["controller"] != srv.URL {
		t.Fatalf("host did not report enrollment: %v", host)
	}

	// Second call without replace_host must not re-enroll or forward the token again.
	w = callLocal(s, "POST", "/api/local/v1/enrollment", body, cookie, csrf, "https://core.test")
	if w.Code != 409 {
		t.Fatalf("re-enrollment without replace_host accepted: %d %s", w.Code, w.Body)
	}
	json.Unmarshal(w.Body.Bytes(), &v)
	if v["host_id"] != hostID {
		t.Fatalf("409 missing host_id: %v", v)
	}

	s.Close()
	if strings.Contains(buf.String(), token) {
		t.Fatal("enrollment token appeared in log output")
	}
}

// TestEnrollmentReplaceHostSuccess exercises the re-enrollment success path directly
// against POST /api/local/v1/enrollment: a non-empty replace_host that matches the
// store's already-persisted host ID must be accepted (not just rejected when it does
// not match, as TestEnrollmentSuccessPersistsAndReportsOnHost's second call covers).
func TestEnrollmentReplaceHostSuccess(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	const token = "s3cr3t-enrollment-token-0123456789"
	srv, trust := fakeController(t, func(req protocol.EnrollRequest) (int, protocol.EnrollResponse) {
		return http.StatusOK, protocol.EnrollResponse{HostID: "host-1"}
	})
	body := map[string]string{"controller_url": srv.URL, "ca_pem": trust, "token": token}
	w := callLocal(s, "POST", "/api/local/v1/enrollment", body, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("initial enrollment failed: %d %s", w.Code, w.Body)
	}
	hostID, _ := hostLocal(t, s, cookie)["host_id"].(string)

	replace := map[string]string{"controller_url": srv.URL, "ca_pem": trust, "token": token, "replace_host": hostID}
	w = callLocal(s, "POST", "/api/local/v1/enrollment", replace, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("re-enrollment with matching replace_host rejected: %d %s", w.Code, w.Body)
	}
	var v map[string]string
	json.Unmarshal(w.Body.Bytes(), &v)
	if v["host_id"] != hostID {
		t.Fatalf("re-enrollment changed host identity: %v", v)
	}
	host := hostLocal(t, s, cookie)
	if host["host_id"] != hostID || host["controller"] != srv.URL {
		t.Fatalf("host did not report re-enrollment: %v", host)
	}
}

func TestEnrollmentControllerRejectionLeavesHostUnenrolled(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	srv, trust := fakeController(t, func(protocol.EnrollRequest) (int, protocol.EnrollResponse) {
		return http.StatusUnauthorized, protocol.EnrollResponse{}
	})
	body := map[string]string{"controller_url": srv.URL, "ca_pem": trust, "token": "rejected-token"}
	w := callLocal(s, "POST", "/api/local/v1/enrollment", body, cookie, csrf, "https://core.test")
	if w.Code != 502 {
		t.Fatalf("controller rejection did not surface as 502: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "rejected-token") {
		t.Fatal("token echoed back in error")
	}
	if got := hostLocal(t, s, cookie)["host_id"]; got != "" {
		t.Fatalf("host enrolled despite controller rejection: %v", got)
	}
}

func TestReplacementEnrollmentFailureKeepsOldCredentialAndPendingRetry(t *testing.T) {
	s, cfg := testService(t)
	cookie, csrf := loginLocal(t, s)
	first, firstTrust := fakeController(t, func(protocol.EnrollRequest) (int, protocol.EnrollResponse) {
		return http.StatusOK, protocol.EnrollResponse{HostID: "host-1"}
	})
	if w := callLocal(s, "POST", "/api/local/v1/enrollment", map[string]string{"controller_url": first.URL, "ca_pem": firstTrust, "token": "first-token"}, cookie, csrf, "https://core.test"); w.Code != http.StatusOK {
		t.Fatalf("initial enrollment: %d %s", w.Code, w.Body)
	}
	oldController, oldTrust := first.URL, firstTrust
	failure, failureTrust := fakeController(t, func(protocol.EnrollRequest) (int, protocol.EnrollResponse) {
		return http.StatusUnauthorized, protocol.EnrollResponse{}
	})
	if w := callLocal(s, "POST", "/api/local/v1/enrollment", map[string]string{"controller_url": failure.URL, "ca_pem": failureTrust, "token": "replacement-token"}, cookie, csrf, "https://core.test"); w.Code != http.StatusBadGateway {
		t.Fatalf("replacement rejection: %d %s", w.Code, w.Body)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := agent.OpenStore(cfg.AgentStateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Close()
	if stored.State.Controller != oldController || stored.State.TrustPEM != oldTrust || stored.State.CertificatePEM == "" || stored.State.KeyPEM == "" || stored.State.PendingKeyPEM == "" || stored.State.PendingCSRPEM == "" {
		t.Fatal("replacement rejection did not preserve old active credential and pending retry")
	}
}

// Add host from a replacement Controller succeeds on an already enrolled host; only the
// same Controller is refused with 409.
func TestEnrollmentWithReplacementControllerSucceeds(t *testing.T) {
	s, cfg := testService(t)
	cookie, csrf := loginLocal(t, s)
	first, firstTrust := fakeController(t, func(protocol.EnrollRequest) (int, protocol.EnrollResponse) {
		return http.StatusOK, protocol.EnrollResponse{HostID: "host-1"}
	})
	second, secondTrust := fakeController(t, func(protocol.EnrollRequest) (int, protocol.EnrollResponse) {
		return http.StatusOK, protocol.EnrollResponse{HostID: "host-2"}
	})
	w := callLocal(s, "POST", "/api/local/v1/enrollment", map[string]string{"controller_url": first.URL, "ca_pem": firstTrust, "token": "token-one"}, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("first enrollment: %d %s", w.Code, w.Body)
	}
	hostID, _ := hostLocal(t, s, cookie)["host_id"].(string)
	body := map[string]string{"controller_url": second.URL, "ca_pem": secondTrust, "token": "token-two"}
	w = callLocal(s, "POST", "/api/local/v1/enrollment", body, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("replacement Controller enrollment refused: %d %s", w.Code, w.Body)
	}
	host := hostLocal(t, s, cookie)
	if host["host_id"] != hostID || host["controller"] != second.URL {
		t.Fatalf("host did not switch Controller: %v", host)
	}
	w = callLocal(s, "POST", "/api/local/v1/enrollment", body, cookie, csrf, "https://core.test")
	if w.Code != 409 {
		t.Fatalf("same-Controller re-enrollment accepted: %d %s", w.Code, w.Body)
	}
	// Close must stop the embedded agent before TempDir cleanup (BUG-0046).
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cfg.AgentStateDir); err != nil {
		t.Fatalf("agent state still busy after Close: %v", err)
	}
}
