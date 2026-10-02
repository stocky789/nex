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
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/vmhost"
)

// The swap's readiness check trusts only the certificate pinned at deployment: a Controller that
// answers with another identity, or with an error, is not ready.
func TestControllerProbeVerifiesThePinnedCertificateAndStatus(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer server.Close()
	pinned := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	s := &Service{operations: map[string]deployment{"d": {Operation: vmhost.Operation{Status: "ready"}, Certificate: pinned}}}
	ctx := context.Background()
	if err := s.controllerProbe(ctx, server.URL); err != nil {
		t.Fatalf("a Controller with the pinned certificate must be ready: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := s.controllerProbe(ctx, server.URL); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("an error answer must not be ready: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "other"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	other.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	other.StartTLS()
	defer other.Close()
	if err := s.controllerProbe(ctx, other.URL); err == nil {
		t.Fatal("a Controller presenting another certificate must not be ready")
	}
	s.operations["d"] = deployment{Operation: vmhost.Operation{Status: "needs_attention"}, Certificate: pinned}
	if err := s.controllerProbe(ctx, server.URL); err == nil {
		t.Fatal("without a ready deployment there is no identity to verify against")
	}
}
