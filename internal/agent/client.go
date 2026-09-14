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
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&failure)
		return httpStatusError{Status: resp.StatusCode, Detail: strings.ToValidUTF8(failure.Error, "�")}
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	return dec.Decode(out)
}

type httpStatusError struct {
	Status int
	Detail string
}

func (e httpStatusError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("controller rejected request (HTTP %d): %s", e.Status, e.Detail)
	}
	return fmt.Sprintf("controller rejected request (HTTP %d)", e.Status)
}

func copyState(in State) State {
	out := in
	out.Journal = make(map[string]JournalEntry, len(in.Journal))
	for id, entry := range in.Journal {
		out.Journal[id] = entry
	}
	return out
}

// saveEnrollmentAttempt makes the exact credential and host identity that will
// be sent durable before any enrollment request is issued. A pre-publication
// failure must leave the caller's State unchanged: otherwise an in-process
// retry could send a CSR which was never saved for a later retry after restart.
func saveEnrollmentAttempt(s *Store, next State) error {
	previous := s.State
	s.State = next
	if err := s.Save(); err != nil {
		if !stateWasPublished(err) {
			s.State = previous
		}
		return err
	}
	return nil
}

// acceptIdentity validates the remote result before changing State, then saves a
// complete replacement State.  In particular, a replacement enrollment never
// leaves a new key/trust paired with the old certificate when its response is
// rejected or lost.
func acceptIdentity(s *Store, r protocol.EnrollResponse, key, csr, controller, trust, serverName string, switching bool) error {
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
	next := copyState(s.State)
	if switching {
		// Managed work belongs to Core and keeps running after Controller replacement.
		// Existing results are adopted from inventory, not sent as old transport replies.
		for id, entry := range next.Journal {
			if entry.Command == nil || !managedCommand(entry.Command.Kind) {
				delete(next.Journal, id)
				continue
			}
			entry.Acknowledged = true
			next.Journal[id] = entry
		}
		// A replacement Controller must acknowledge v2 support before Core sends it
		// any repository commands or inventory extensions.
		next.StorageProtocolReady = false
		next.StorageClusterProtocolReady = false
		next.StorageClusterSessionMonitorProtocolReady = false
		next.StorageProtocolLegacy = false
	}
	next.Controller, next.TrustPEM, next.ControllerServerName = controller, trust, serverName
	next.HostID = r.HostID
	next.PendingHostID = ""
	if next.PoolID == "" {
		// Older hosts adopt the pool identity the Controller holds for them.
		next.PoolID, next.PoolName = r.PoolID, r.PoolName
	}
	next.KeyPEM = key
	next.CSRPEM = csr
	next.CertificatePEM = r.CertificatePEM
	next.PendingKeyPEM = ""
	next.PendingCSRPEM = ""
	previous := s.State
	s.State = next
	if err := s.Save(); err != nil {
		if stateWasPublished(err) {
			return err
		}
		s.State = previous
		return err
	}
	return nil
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
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	next := copyState(s.State)
	pending := replaceHost != "" || switching
	key, csr := next.KeyPEM, next.CSRPEM
	if pending {
		// Keep the active identity on the old Controller until the new one
		// proves it accepted this CSR. Pending credentials survive a lost
		// response and make the retry byte-for-byte identical.
		key, csr = next.PendingKeyPEM, next.PendingCSRPEM
	}
	if key == "" {
		key, csr, err = newKey()
		if err != nil {
			return err
		}
		if pending {
			next.PendingKeyPEM, next.PendingCSRPEM = key, csr
		} else {
			next.KeyPEM, next.CSRPEM = key, csr
		}
	}
	if switching {
		// The replacement origin/trust is used only for this unauthenticated
		// request until the Controller accepts the pending CSR.
	} else {
		// A first enrollment has no active certificate to preserve, so retaining
		// its intended endpoint/trust makes its pending identity retryable.
		next.Controller, next.TrustPEM = controller, ca
	}
	// Identity is host-owned: a first-time host mints its own host and pool identity and
	// persists them before asking, so retries present the same IDs and every Controller,
	// including a replacement, adopts the same IDs and pool name.
	if next.HostID == "" && (next.PendingHostID == "" || next.PoolID == "") {
		next.PendingHostID = cmp.Or(next.PendingHostID, newUUID())
		if next.PoolID == "" {
			next.PoolID, next.PoolName = newUUID(), hostname
		}
	}
	// This save intentionally occurs on every request, even when the pending
	// key already exists. It makes retry identity a durable precondition of POST.
	if err = saveEnrollmentAttempt(s, next); err != nil {
		return err
	}
	candidate := s.State
	if switching {
		candidate.Controller, candidate.TrustPEM, candidate.ControllerServerName = controller, ca, ""
	}
	c, err := client(candidate, false)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	var r protocol.EnrollResponse
	// Enrollment has no repository fields, so retain the v1 shape until the
	// authenticated report preflight establishes Controller v2 support.
	req := protocol.EnrollRequest{Version: protocol.LegacyVersion, Token: strings.TrimSpace(token), CSRPEM: csr, Hostname: hostname, HostID: cmp.Or(s.State.HostID, s.State.PendingHostID), PoolID: s.State.PoolID, PoolName: s.State.PoolName}
	if err = post(ctx, c, controller, "/api/v1/agent/enroll", req, &r); err != nil {
		return err
	}
	if replaceHost != "" && r.HostID != replaceHost {
		return errors.New("replacement enrollment returned a different host; use a token targeting the revoked host")
	}
	return acceptIdentity(s, r, key, csr, controller, ca, candidate.ControllerServerName, switching)
}
func Rotate(ctx context.Context, s *Store) error {
	c, err := client(s.State, true)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	next := copyState(s.State)
	if next.PendingKeyPEM == "" {
		next.PendingKeyPEM, next.PendingCSRPEM, err = newKey()
		if err != nil {
			return err
		}
	}
	if err = saveEnrollmentAttempt(s, next); err != nil {
		return err
	}
	var r protocol.EnrollResponse
	if err = post(ctx, c, s.State.Controller, "/api/v1/agent/rotate", protocol.RotateRequest{Version: protocol.LegacyVersion, CSRPEM: s.State.PendingCSRPEM}, &r); err != nil {
		return err
	}
	if r.HostID != s.State.HostID {
		return errors.New("rotation changed host identity")
	}
	return acceptIdentity(s, r, s.State.PendingKeyPEM, s.State.PendingCSRPEM, s.State.Controller, s.State.TrustPEM, s.State.ControllerServerName, false)
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
