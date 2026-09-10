// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestEnrollmentLostResponseAndIdentityPersistence(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	caDER, _ := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	calls := 0
	var first protocol.EnrollRequest
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req protocol.EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if calls == 1 {
			first = req
		} else if req != first {
			t.Errorf("retry changed the request: %+v != %+v", req, first)
		}
		if !uuidForm(req.HostID) || !uuidForm(req.PoolID) || req.PoolName == "" {
			t.Errorf("host did not present its own identity: %+v", req)
		}
		block, _ := pem.Decode([]byte(req.CSRPEM))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			t.Error(err)
			return
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: req.HostID}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, _ := x509.CreateCertificate(rand.Reader, cert, ca, csr.PublicKey, caKey)
		if calls == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(protocol.EnrollResponse{HostID: req.HostID, PoolID: req.PoolID, PoolName: req.PoolName, CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), CAPEM: caPEM})
	}))
	defer srv.Close()
	trust := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	s := testStore(t)
	if err := Enroll(context.Background(), s, srv.URL, trust, "token", ""); err == nil {
		t.Fatal("lost response accepted")
	}
	if s.State.HostID != "" || s.State.PendingHostID != first.HostID || s.State.PoolID != first.PoolID || s.State.PoolName != first.PoolName {
		t.Fatalf("identity not persisted before the request, or treated as enrolled: %+v", s.State)
	}
	key := s.State.KeyPEM
	dir := s.dir
	s.Close()
	var err error
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = Enroll(context.Background(), s, srv.URL, trust, "token", ""); err != nil {
		t.Fatal(err)
	}
	if s.State.KeyPEM != key || s.State.HostID != first.HostID || s.State.PendingHostID != "" || s.State.PoolID != first.PoolID {
		t.Fatalf("identity changed: %+v", s.State)
	}
	if err = Enroll(context.Background(), s, srv.URL, trust, "token", ""); err == nil {
		t.Fatal("silent replacement accepted")
	}
	info, _ := os.Stat(filepath.Join(dir, "state.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("private state permissions")
	}
}
func TestTrustAndDirectorySafety(t *testing.T) {
	s := testStore(t)
	if _, err := OpenStore(s.dir); err == nil {
		t.Fatal("concurrent state owner accepted")
	}
	if _, err := client(State{Controller: "http://localhost", TrustPEM: "invalid"}, false); err == nil {
		t.Fatal("plaintext accepted")
	}
	if _, err := client(State{Controller: "https://localhost", TrustPEM: "invalid"}, false); err == nil {
		t.Fatal("invalid trust accepted")
	}
	dir := filepath.Join(t.TempDir(), "public")
	os.Mkdir(dir, 0755)
	if _, err := OpenStore(dir); err == nil {
		t.Fatal("public state directory accepted")
	}
}
func TestJournalReplayAndFreshInventory(t *testing.T) {
	s := testStore(t)
	cmd := protocol.Command{OperationID: "op-1", Kind: "refresh-inventory"}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	requested := s.State.Journal[cmd.OperationID].RequestedAt
	completeJournal(s, &protocol.Inventory{ObservedAt: requested.Add(-time.Second)}, time.Hour)
	if s.State.Journal[cmd.OperationID].Status != "pending" {
		t.Fatal("old inventory completed command")
	}
	completeJournal(s, &protocol.Inventory{ObservedAt: requested.Add(time.Second)}, time.Hour)
	if err := applyResponse(s, protocol.ReportResponse{AcknowledgedResults: []string{cmd.OperationID}, Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	e := s.State.Journal[cmd.OperationID]
	if e.Status != "succeeded" || !e.Acknowledged || !e.RequestedAt.Equal(requested) {
		t.Fatal("replayed completed command")
	}
	s.Close()
	reopened, err := OpenStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if e := reopened.State.Journal[cmd.OperationID]; e.Status != "succeeded" || !e.Acknowledged {
		t.Fatal("journal not durable")
	}
	if err := applyResponse(reopened, protocol.ReportResponse{Commands: []protocol.Command{{OperationID: "bad", Kind: "start-vm"}}}); err != nil {
		t.Fatal(err)
	}
	if reopened.State.Journal["bad"].Status != "failed" {
		t.Fatal("mutation command accepted")
	}
}

func TestRunStopsOnJournalPersistenceFailure(t *testing.T) {
	s := testStore(t)
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		path := filepath.Join(s.dir, "state.json")
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Error(err)
		}
		json.NewEncoder(w).Encode(protocol.ReportResponse{Commands: []protocol.Command{{OperationID: "refresh", Kind: "refresh-inventory"}}})
	}))
	defer srv.Close()
	keyDER, err := x509.MarshalPKCS8PrivateKey(srv.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	s.State.Controller = srv.URL
	s.State.TrustPEM = certPEM
	s.State.CertificatePEM = certPEM
	s.State.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: 100 * time.Millisecond, ProbeTimeout: 20 * time.Millisecond})
	var persistence persistenceError
	if !errors.As(err, &persistence) {
		t.Fatalf("want fatal persistence failure, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("reported %d times after persistence failure", calls)
	}
}

func uuidForm(s string) bool {
	return regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(s)
}

// issuingController returns a fake Controller with its own CA. Like a real one it adopts the
// presented host identity and mints a pool ("pool-of-"+host) for a host presenting none; with
// mint set it issues that host ID regardless, like a Controller ignoring presented identity.
func issuingController(t *testing.T, mint string) (*httptest.Server, string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA " + mint}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	caDER, _ := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.EnrollRequest
		json.NewDecoder(r.Body).Decode(&req)
		block, _ := pem.Decode([]byte(req.CSRPEM))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			t.Error(err)
			return
		}
		resp := protocol.EnrollResponse{HostID: cmp.Or(mint, req.HostID), PoolID: req.PoolID, PoolName: req.PoolName}
		if resp.PoolID == "" {
			resp.PoolID, resp.PoolName = "pool-of-"+resp.HostID, req.Hostname
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: resp.HostID}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, _ := x509.CreateCertificate(rand.Reader, cert, ca, csr.PublicKey, caKey)
		resp.CertificatePEM, resp.CAPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), caPEM
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
}

// A host connects to a replacement Controller in place: a fresh credential there, the same
// host and pool identity (so the pool comes back with its original ID and name), the same
// state directory (so its managed journal is reported), pending results for the old one dropped.
func TestEnrollWithReplacementController(t *testing.T) {
	first, firstTrust := issuingController(t, "")
	second, secondTrust := issuingController(t, "")
	s := testStore(t)
	if err := Enroll(context.Background(), s, first.URL, firstTrust, "token", ""); err != nil {
		t.Fatal(err)
	}
	oldKey, host, pool, poolName := s.State.KeyPEM, s.State.HostID, s.State.PoolID, s.State.PoolName
	hostname, _ := os.Hostname()
	if !uuidForm(host) || !uuidForm(pool) || poolName != hostname {
		t.Fatalf("host did not generate its own identity: %+v", s.State)
	}
	s.State.Journal["op-old"] = JournalEntry{Status: "pending"}
	if err := Enroll(context.Background(), s, second.URL, secondTrust, "token", host); err == nil {
		t.Fatal("replace-host accepted against a different Controller")
	}
	if err := Enroll(context.Background(), s, second.URL, secondTrust, "token", ""); err != nil {
		t.Fatal("switching Controller refused:", err)
	}
	if s.State.HostID != host || s.State.PoolID != pool || s.State.PoolName != poolName || s.State.Controller != second.URL || s.State.TrustPEM != secondTrust || s.State.KeyPEM == oldKey || len(s.State.Journal) != 0 {
		t.Fatalf("replacement did not keep identity with a fresh credential: %+v", s.State)
	}
	if err := Enroll(context.Background(), s, second.URL, secondTrust, "token", ""); err == nil {
		t.Fatal("duplicate enrollment with the same Controller accepted")
	}
}

// A host enrolled before host-owned identity keeps its Controller-issued host ID and adopts
// the pool identity the Controller reports; a Controller that answers with a different host
// identity is refused and leaves the host unenrolled there.
func TestEnrollLegacyIdentityAdoptionAndChangedIdentityRefused(t *testing.T) {
	srv, trust := issuingController(t, "")
	s := testStore(t)
	s.State.HostID = "legacy-host"
	s.State.Controller, s.State.TrustPEM = "https://old.example", "old trust"
	if err := Enroll(context.Background(), s, srv.URL, trust, "token", ""); err != nil {
		t.Fatal(err)
	}
	hostname, _ := os.Hostname()
	if s.State.HostID != "legacy-host" || s.State.PoolID != "pool-of-legacy-host" || s.State.PoolName != hostname {
		t.Fatalf("legacy host did not keep its ID and adopt the reported pool: %+v", s.State)
	}
	other, otherTrust := issuingController(t, "someone-else")
	fresh := testStore(t)
	if err := Enroll(context.Background(), fresh, other.URL, otherTrust, "token", ""); err == nil || fresh.State.HostID != "" || fresh.State.CertificatePEM != "" || fresh.State.PendingHostID == "" {
		t.Fatalf("Controller changing the host identity was accepted: %v %+v", err, fresh.State)
	}
	if err := Enroll(context.Background(), s, other.URL, otherTrust, "token", ""); err == nil || s.State.HostID != "legacy-host" || s.State.CertificatePEM == "" {
		t.Fatalf("enrolled host accepted a changed identity: %v %+v", err, s.State)
	}
}
