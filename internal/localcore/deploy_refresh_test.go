// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/agent"
)

func dhcpRefreshIdentity(t *testing.T) (trust string, key *ecdsa.PrivateKey, der []byte, certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nex-controller"},
		DNSNames: []string{"nex-controller"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err = x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	trust = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientCert := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "host-1"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientCert, cert, &clientKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: clientKeyDER}))
	return trust, key, der, certPEM, keyPEM
}

func TestRefreshDHCPAgentReadyWaitsForControllerListen(t *testing.T) {
	prev := dhcpControllerReadyWait
	dhcpControllerReadyWait = 8 * time.Second
	t.Cleanup(func() { dhcpControllerReadyWait = prev })

	trust, key, der, certPEM, keyPEM := dhcpRefreshIdentity(t)

	// Pick a free port, then leave it closed so early verify attempts get
	// connection refused — the same race as guest-agent DHCP before nex-controller listens.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://" + ln.Addr().String()
	addr := ln.Addr().String()
	if err = ln.Close(); err != nil {
		t.Fatal(err)
	}

	dir := privateDir(t)
	store, err := agent.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.State = agent.State{
		Controller:           strings.Replace(endpoint, "127.0.0.1", "127.0.0.2", 1),
		ControllerServerName: "nex-controller",
		TrustPEM:             trust,
		HostID:               "host-1",
		PoolID:               "pool-1",
		CertificatePEM:       certPEM,
		KeyPEM:               keyPEM,
	}
	if err = store.Save(); err != nil {
		t.Fatal(err)
	}
	store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		releaseHandoffAgent(dir)
	}()

	started := make(chan struct{})
	var srv *httptest.Server
	go func() {
		time.Sleep(1500 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Errorf("late listen: %v", err)
			return
		}
		srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		srv.Listener = ln
		srv.TLS = &tls.Config{
			Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
			MinVersion:   tls.VersionTLS13,
		}
		srv.StartTLS()
		close(started)
	}()
	t.Cleanup(func() {
		if srv != nil {
			srv.Close()
		}
	})

	if err = refreshDHCPAgentReady(ctx, dir, endpoint, trust, "nex-controller"); err != nil {
		t.Fatalf("expected delayed Controller listen to succeed: %v", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("refresh succeeded before Controller started listening")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	handoffAgents.Lock()
	a := handoffAgents.entries[abs]
	startedAgent := a != nil && a.started
	handoffAgents.Unlock()
	if !startedAgent {
		t.Fatal("embedded agent was not started after DHCP refresh")
	}
}

func TestRefreshDHCPAgentReadyFailsWhenControllerNeverListens(t *testing.T) {
	prev := dhcpControllerReadyWait
	dhcpControllerReadyWait = 2 * time.Second
	t.Cleanup(func() { dhcpControllerReadyWait = prev })

	trust, _, _, certPEM, keyPEM := dhcpRefreshIdentity(t)
	dir := privateDir(t)
	store, err := agent.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.State = agent.State{
		Controller:           "https://127.0.0.1:1",
		ControllerServerName: "nex-controller",
		TrustPEM:             trust,
		HostID:               "host-1",
		PoolID:               "pool-1",
		CertificatePEM:       certPEM,
		KeyPEM:               keyPEM,
	}
	if err = store.Save(); err != nil {
		t.Fatal(err)
	}
	store.Close()

	err = refreshDHCPAgentReady(context.Background(), dir, "https://127.0.0.1:1", trust, "nex-controller")
	if err == nil {
		t.Fatal("expected failure when Controller never listens")
	}
	if !strings.Contains(err.Error(), "Controller is not ready") {
		t.Fatalf("want bounded ready error, got %v", err)
	}
}
