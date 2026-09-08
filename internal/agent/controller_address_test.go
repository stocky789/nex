// SPDX-License-Identifier: AGPL-3.0-only
package agent

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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestControllerHostnameTrustAndAddressPersistence(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(101), Subject: pkix.Name{CommonName: "controller.test"}, DNSNames: []string{"controller.test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()
	trust := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	base := State{Controller: srv.URL, TrustPEM: trust}
	for _, name := range []string{"", "wrong.test", "controller.test"} {
		s := base
		s.ControllerServerName = name
		c, err := client(s, false)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.Get(srv.URL)
		c.CloseIdleConnections()
		if resp != nil {
			resp.Body.Close()
		}
		if (err == nil) != (name == "controller.test") {
			t.Fatalf("TLS name %q: %v", name, err)
		}
	}
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer other.Close()
	untrusted := base
	untrusted.ControllerServerName = "controller.test"
	untrusted.TrustPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.Certificate().Raw}))
	c, err := client(untrusted, false)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := c.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("untrusted certificate accepted")
	}
	c.CloseIdleConnections()
	for _, name := range []string{"bad name", "https://controller.test", "127.0.0.1", "*.test", "-bad.test", strings.Repeat("a", 64) + ".test"} {
		s := base
		s.ControllerServerName = name
		if _, err := client(s, false); err == nil {
			t.Fatalf("invalid TLS name accepted: %q", name)
		}
	}
	s := testStore(t)
	s.State = base
	s.State.Controller = strings.Replace(srv.URL, "127.0.0.1", "127.0.0.2", 1)
	s.State.ControllerServerName = "controller.test"
	s.State.HostID = "host-preserved"
	s.State.Sequence = 43
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	if err := UpdateControllerURL(context.Background(), s, srv.URL); err != nil {
		t.Fatal(err)
	}
	if s.State.HostID != "host-preserved" || s.State.Sequence != 43 {
		t.Fatal("address refresh changed host identity")
	}
	previous := s.State.Controller
	for _, bad := range []string{"http://127.0.0.1", srv.URL + "/bad", strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), other.URL} {
		if err := UpdateControllerURL(context.Background(), s, bad); err == nil {
			t.Fatalf("invalid address refresh accepted: %s", bad)
		}
		if s.State.Controller != previous {
			t.Fatal("failed refresh changed persisted address")
		}
	}
	s.Close()
	reopened, err := OpenStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.State.Controller != srv.URL || reopened.State.ControllerServerName != "controller.test" || reopened.State.HostID != "host-preserved" {
		t.Fatal("controller identity not persisted")
	}
	c, err = client(reopened.State, false)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(reopened.State.Controller)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	c.CloseIdleConnections()
	reopened.State.ControllerServerName = ""
	if err := UpdateControllerURL(context.Background(), reopened, srv.URL); err == nil {
		t.Fatal("legacy IP identity silently changed")
	}
}
