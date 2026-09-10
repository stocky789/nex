// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"nex.local/nex/internal/protocol"
	"os"
	"regexp"
	"strings"
	"time"
)

var controllerDNSName = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*\.?$`)

// tlsConfig builds the Controller trust and optional enrolled client identity.
func tlsConfig(s State, authenticated bool) (*tls.Config, error) {
	u, err := url.Parse(s.Controller)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("controller must be an HTTPS origin without credentials, path, query or fragment")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(s.TrustPEM)) {
		return nil, errors.New("controller CA file contains no certificates")
	}
	if name := s.ControllerServerName; name != "" && (len(strings.TrimSuffix(name, ".")) > 253 || !controllerDNSName.MatchString(name) || net.ParseIP(name) != nil) {
		return nil, errors.New("controller TLS server name must be a DNS hostname")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: s.ControllerServerName}
	if authenticated {
		cert, e := tls.X509KeyPair([]byte(s.CertificatePEM), []byte(s.KeyPEM))
		if e != nil {
			return nil, errors.New("agent identity is missing or invalid; enroll first")
		}
		config.Certificates = []tls.Certificate{cert}
	}
	return config, nil
}
func client(s State, authenticated bool) (*http.Client, error) {
	config, err := tlsConfig(s, authenticated)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: config}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("controller redirects are refused") }}, nil
}

// UpdateControllerURL changes only the address of an existing hostname-verified
// identity. The caller must hold exclusive use of the Store (Run must be stopped)
// and must first observe the address on its owned Controller VM and MAC.
func UpdateControllerURL(ctx context.Context, s *Store, controller string) error {
	if s.State.ControllerServerName == "" || s.State.Controller == "" {
		return errors.New("controller address refresh requires a persisted TLS hostname")
	}
	candidate := s.State
	candidate.Controller = controller
	c, err := client(candidate, false)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	u, _ := url.Parse(controller)
	old, err := url.Parse(s.State.Controller)
	if err != nil || old.Port() != u.Port() || net.ParseIP(u.Hostname()) == nil {
		return errors.New("controller address refresh requires an IP origin with the original port")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: c.Transport.(*http.Transport).TLSClientConfig}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return fmt.Errorf("controller address verification failed: %w", err)
	}
	conn.Close()
	previous := s.State.Controller
	s.State.Controller = controller
	if err = s.Save(); err != nil {
		s.State.Controller = previous
		return err
	}
	return nil
}

func post(ctx context.Context, c *http.Client, origin, path string, in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(origin, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("controller connection failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("controller rejected request (HTTP %d)", resp.StatusCode)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	return dec.Decode(out)
}
func acceptIdentity(s *Store, r protocol.EnrollResponse, key, csr string, switching bool) error {
	if r.HostID == "" {
		return errors.New("controller returned an empty host identity")
	}
	if want := cmp.Or(s.State.HostID, s.State.PendingHostID); want != "" && r.HostID != want {
		return errors.New("controller changed the host identity")
	}
	if s.State.PoolID != "" && r.PoolID != "" && r.PoolID != s.State.PoolID {
		return errors.New("controller changed the pool identity")
	}
	pair, err := tls.X509KeyPair([]byte(r.CertificatePEM), []byte(key))
	if err != nil {
		return errors.New("controller returned a certificate that does not match the local private key")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if cert.Subject.CommonName != r.HostID {
		return errors.New("certificate host identity mismatch")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(r.CAPEM)) {
		return errors.New("invalid host certificate CA")
	}
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("invalid host certificate: %w", err)
	}
	if switching {
		// Pending results belong to the previous Controller; the new one learns applied
		// state from the managed journal instead.
		s.State.Journal = map[string]JournalEntry{}
	}
	s.State.HostID = r.HostID
	s.State.PendingHostID = ""
	if s.State.PoolID == "" {
		// Older hosts adopt the pool identity the Controller holds for them.
		s.State.PoolID, s.State.PoolName = r.PoolID, r.PoolName
	}
	s.State.KeyPEM = key
	s.State.CSRPEM = csr
	s.State.CertificatePEM = r.CertificatePEM
	s.State.PendingKeyPEM = ""
	s.State.PendingCSRPEM = ""
	return s.Save()
}
func Enroll(ctx context.Context, s *Store, controller, ca, token, replaceHost string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("enrollment token is empty")
	}
	// A different Controller (URL or trust) is a replacement: the host takes a fresh credential
	// there while keeping this state directory and its host/pool identity, so the pool comes
	// back with its original ID and name and its managed journal is reported and adopted.
	switching := s.State.Controller != "" && (s.State.Controller != controller || s.State.TrustPEM != ca)
	if switching && replaceHost != "" {
		return errors.New("--replace-host applies to the current Controller; enrolling with a different Controller needs no replacement ID")
	}
	if s.State.HostID != "" && replaceHost != s.State.HostID && !switching {
		return errors.New("already enrolled with this Controller; use rotate, or revoke first and explicitly select --replace-host with the current host ID")
	}
	if replaceHost != "" && replaceHost != s.State.HostID {
		return errors.New("replacement host ID does not match local identity")
	}
	s.State.Controller = controller
	s.State.TrustPEM = ca
	c, err := client(s.State, false)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	key, csr := s.State.KeyPEM, s.State.CSRPEM
	if replaceHost != "" {
		key, csr = s.State.PendingKeyPEM, s.State.PendingCSRPEM
	}
	if switching {
		key, csr = "", ""
	}
	if key == "" {
		key, csr, err = newKey()
		if err != nil {
			return err
		}
		if replaceHost != "" {
			s.State.PendingKeyPEM = key
			s.State.PendingCSRPEM = csr
		} else {
			s.State.KeyPEM = key
			s.State.CSRPEM = csr
		}
		if err = s.Save(); err != nil {
			return err
		}
	}
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	// Identity is host-owned: a first-time host mints its own host and pool identity and
	// persists them before asking, so retries present the same IDs and every Controller,
	// including a replacement, adopts the same IDs and pool name.
	if s.State.HostID == "" && (s.State.PendingHostID == "" || s.State.PoolID == "") {
		s.State.PendingHostID = cmp.Or(s.State.PendingHostID, newUUID())
		if s.State.PoolID == "" {
			s.State.PoolID, s.State.PoolName = newUUID(), hostname
		}
		if err = s.Save(); err != nil {
			return err
		}
	}
	var r protocol.EnrollResponse
	req := protocol.EnrollRequest{Version: protocol.Version, Token: strings.TrimSpace(token), CSRPEM: csr, Hostname: hostname, HostID: cmp.Or(s.State.HostID, s.State.PendingHostID), PoolID: s.State.PoolID, PoolName: s.State.PoolName}
	if err = post(ctx, c, controller, "/api/v1/agent/enroll", req, &r); err != nil {
		return err
	}
	if replaceHost != "" && r.HostID != replaceHost {
		return errors.New("replacement enrollment returned a different host; use a token targeting the revoked host")
	}
	return acceptIdentity(s, r, key, csr, switching)
}
func Rotate(ctx context.Context, s *Store) error {
	c, err := client(s.State, true)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	if s.State.PendingKeyPEM == "" {
		s.State.PendingKeyPEM, s.State.PendingCSRPEM, err = newKey()
		if err != nil {
			return err
		}
		if err = s.Save(); err != nil {
			return err
		}
	}
	var r protocol.EnrollResponse
	if err = post(ctx, c, s.State.Controller, "/api/v1/agent/rotate", protocol.RotateRequest{Version: protocol.Version, CSRPEM: s.State.PendingCSRPEM}, &r); err != nil {
		return err
	}
	if r.HostID != s.State.HostID {
		return errors.New("rotation changed host identity")
	}
	return acceptIdentity(s, r, s.State.PendingKeyPEM, s.State.PendingCSRPEM, false)
}
func CertificateExpiry(s State) (time.Time, error) {
	b, _ := pem.Decode([]byte(s.CertificatePEM))
	if b == nil {
		return time.Time{}, errors.New("not enrolled")
	}
	c, e := x509.ParseCertificate(b.Bytes)
	if e != nil {
		return time.Time{}, e
	}
	return c.NotAfter, nil
}
