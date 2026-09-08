// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
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
	var firstCSR string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req protocol.EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if firstCSR == "" {
			firstCSR = req.CSRPEM
		} else if req.CSRPEM != firstCSR {
			t.Error("retry changed CSR")
		}
		block, _ := pem.Decode([]byte(req.CSRPEM))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			t.Error(err)
			return
		}
		cert := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "host-1"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, _ := x509.CreateCertificate(rand.Reader, cert, ca, csr.PublicKey, caKey)
		if calls == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(protocol.EnrollResponse{HostID: "host-1", CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), CAPEM: caPEM})
	}))
	defer srv.Close()
	trust := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	s := testStore(t)
	if err := Enroll(context.Background(), s, srv.URL, trust, "token", ""); err == nil {
		t.Fatal("lost response accepted")
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
	if s.State.KeyPEM != key || s.State.HostID != "host-1" {
		t.Fatal("identity changed")
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
