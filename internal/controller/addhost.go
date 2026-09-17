// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type probeRequest struct {
	Address string `json:"address"`
}
type probeResponse struct {
	Address     string    `json:"address"`
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	NotAfter    time.Time `json:"not_after"`
}
type addHostRequest struct {
	Address       string `json:"address"`
	Fingerprint   string `json:"fingerprint"`
	RootPassword  string `json:"root_password"`
	ControllerURL string `json:"controller_url"`
	HostID        string `json:"host_id,omitempty"`
}
type addHostResponse struct {
	HostID string `json:"host_id"`
	PoolID string `json:"pool_id"`
}

var (
	hostnamePattern    = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)
	fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// parseHostAddress validates a bare "host" or "host:port" (no scheme, userinfo, path,
// query or fragment) and fills in the default nex-core-local port when omitted. It
// deliberately applies no loopback/link-local/private-range denylist: probeHost and
// addSiteHost are admin-only, and their blast radius is bounded (probe leaks only
// certificate metadata; addSiteHost reaches exactly two fixed paths and requires the
// caller to already know the target's certificate fingerprint before any password is
// sent). Narrow this if Nex ever needs to stop an admin session from directing the
// controller's outbound TLS client at arbitrary network destinations.
func parseHostAddress(address string) (string, bool) {
	if address == "" || strings.ContainsAny(address, " \t\r\n/\\@?#") || strings.Contains(address, "://") {
		return "", false
	}
	host, port := address, "9443"
	if h, p, err := net.SplitHostPort(address); err == nil {
		host, port = h, p
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", false
	}
	if host == "" || (net.ParseIP(host) == nil && !hostnamePattern.MatchString(host)) {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

// httpsOrigin mirrors the local-agent handoff rule: a bare HTTPS origin with no
// userinfo, query, fragment, or path beyond "/".
func httpsOrigin(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

func fingerprintOf(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// probeCertificate performs an insecure TLS handshake purely to capture the host's
// leaf certificate for operator fingerprint confirmation; it establishes no trust.
func probeCertificate(addr string) (*x509.Certificate, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, err
	}
	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	if err := tlsConn.Handshake(); err != nil {
		return nil, err
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("host presented no certificate")
	}
	return certs[0], nil
}

func (c *Controller) probeHost(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v probeRequest
	if !decode(w, r, &v) {
		return
	}
	addr, ok := parseHostAddress(v.Address)
	if !ok {
		fail(w, 400, "address must be a hostname or IP with an optional port, no scheme, path, or userinfo")
		return
	}
	cert, err := probeCertificate(addr)
	if err != nil {
		fail(w, 502, "host unreachable or TLS handshake failed")
		return
	}
	write(w, 200, probeResponse{Address: addr, Fingerprint: fingerprintOf(cert), Subject: cert.Subject.String(), NotAfter: cert.NotAfter.UTC()})
}

// validateEnrollmentTarget checks the site exists and, when hostID is set, that it
// names a revoked host whose pool belongs to this site. q is *sql.DB or *sql.Tx: the
// caller chooses whether the check needs transactional isolation from a concurrent
// change, so a read-only pre-flight check never holds a transaction across a
// subsequent network call.
func (c *Controller) validateEnrollmentTarget(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, siteID, hostID string) (status int, msg string) {
	var count int
	if q.QueryRow("SELECT count(*) FROM sites WHERE id=?", siteID).Scan(&count) != nil || count != 1 {
		return 400, "site does not exist"
	}
	if hostID != "" {
		if q.QueryRow("SELECT count(*) FROM hosts h JOIN pools p ON p.id=h.pool_id WHERE h.id=? AND p.site_id=? AND h.revoked=1", hostID, siteID).Scan(&count) != nil || count != 1 {
			return 409, "re-enrollment requires a revoked host in the selected site"
		}
	}
	return 0, ""
}

// mintEnrollmentToken issues a site-bound token in its own committed transaction, so
// neither caller holds storage open across a network call. Shared by c.token (manual
// enrollment) and addSiteHost (address + password enrollment).
func (c *Controller) mintEnrollmentToken(actor, siteID, hostID string) (token string, expires time.Time, status int, msg string) {
	tx, err := c.db.Begin()
	if err != nil {
		return "", time.Time{}, 500, "storage unavailable"
	}
	defer tx.Rollback()
	if status, msg = c.validateEnrollmentTarget(tx, siteID, hostID); status != 0 {
		return "", time.Time{}, status, msg
	}
	token = random()
	expires = time.Now().Add(10 * time.Minute)
	if _, err = tx.Exec("INSERT INTO tokens(hash,site_id,host_id,expires) VALUES(?,?,?,?)", digest(token), siteID, hostID, expires.Unix()); err != nil ||
		c.audit(tx, actor, "enrollment-token.create", siteID) != nil || tx.Commit() != nil {
		return "", time.Time{}, 500, "token storage failed"
	}
	return token, expires, 0, ""
}

// deleteToken best-effort removes an unused token row after a failed add-host attempt.
func (c *Controller) deleteToken(token string) {
	c.db.Exec("DELETE FROM tokens WHERE hash=?", digest(token))
}

// addHostClient builds the outbound TLS client used to push enrollment to a host: no
// certificate-authority trust, instead a constant-time leaf-fingerprint check that
// runs during the handshake, so a mismatch aborts the connection before any request
// (including the login password) reaches the wire. *mismatched reports which failure
// mode a subsequent transport error came from.
func addHostClient(fingerprint string, mismatched *bool) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Jar:     jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("host redirects refused")
		},
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS13,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("host presented no certificate")
				}
				if subtle.ConstantTimeCompare([]byte(fingerprintOf(cs.PeerCertificates[0])), []byte(fingerprint)) != 1 {
					*mismatched = true
					return errors.New("certificate fingerprint mismatch")
				}
				return nil
			},
		}},
	}, nil
}

// hostCall issues one JSON request against the host's local API and returns the
// status and decoded body fields callers care about ("error", "csrf_token", "host_id").
func hostCall(ctx context.Context, client *http.Client, method, addr, path, csrfToken string, in any) (status int, out map[string]string, err error) {
	var body io.Reader
	if in != nil {
		data, e := json.Marshal(in)
		if e != nil {
			return 0, nil, e
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+addr+path, body)
	if err != nil {
		return 0, nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", "https://"+addr)
	if csrfToken != "" {
		req.Header.Set("X-CSRF-Token", csrfToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	out = map[string]string{}
	json.Unmarshal(raw, &out) // best-effort; a non-JSON body just yields no fields
	return resp.StatusCode, out, nil
}

func failWithHostID(w http.ResponseWriter, status int, msg, hostID string) {
	write(w, status, map[string]string{"error": msg, "host_id": hostID})
}

// addSiteHost implements POST /api/v1/sites/{id}/hosts: it confirms the fingerprint the
// operator already saw from probeHost, signs in to the host's local API with the root
// password (used once, never stored or logged), mints a site-bound enrollment token,
// and pushes it to the host's local enrollment endpoint. The host then enrolls with
// this controller through the existing agent-enrollment contract. A new host becomes the
// master of a new pool in this site; a host that already has a pool keeps its host-owned pool.
func (c *Controller) addSiteHost(w http.ResponseWriter, r *http.Request, user, csrf string) {
	site := r.PathValue("id")
	var v addHostRequest
	if !decode(w, r, &v) {
		return
	}
	addr, ok := parseHostAddress(v.Address)
	if !ok {
		fail(w, 400, "address must be a hostname or IP with an optional port, no scheme, path, or userinfo")
		return
	}
	if !fingerprintPattern.MatchString(v.Fingerprint) {
		fail(w, 400, "fingerprint must be sha256:<64 lowercase hex characters>")
		return
	}
	if !httpsOrigin(v.ControllerURL) {
		fail(w, 400, "controller_url must be an HTTPS origin without credentials, path, query or fragment")
		return
	}
	if len(v.RootPassword) == 0 || len(v.RootPassword) > 1024 || strings.ContainsRune(v.RootPassword, 0) {
		fail(w, 400, "root password required (maximum 1024 bytes, no NUL)")
		return
	}
	if status, msg := c.validateEnrollmentTarget(c.db, site, v.HostID); status != 0 {
		fail(w, status, msg)
		return
	}
	if c.cfg.ServerCertPEM == "" {
		fail(w, 503, "controller server certificate is not configured; use manual enrollment")
		return
	}

	mismatched := false
	client, err := addHostClient(v.Fingerprint, &mismatched)
	if err != nil {
		fail(w, 500, "outbound client construction failed")
		return
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// (a) Sign in. A fingerprint mismatch fails the TLS handshake itself, so the
	// password never reaches the wire; check it ahead of the generic transport error.
	status, login, err := hostCall(ctx, client, http.MethodPost, addr, "/api/local/v1/session", "", map[string]string{"username": "root", "password": v.RootPassword})
	if mismatched {
		fail(w, 502, "host certificate fingerprint does not match")
		return
	}
	if err != nil {
		fail(w, 502, "host unreachable: could not sign in")
		return
	}
	switch status {
	case 200:
	case 401:
		fail(w, 401, "host rejected the root password")
		return
	case 429:
		fail(w, 429, cmp.Or(login["error"], "host login rate limited"))
		return
	default:
		fail(w, 502, "host login failed")
		return
	}
	defer hostCall(ctx, client, http.MethodDelete, addr, "/api/local/v1/session", login["csrf_token"], nil)

	// (b) Mint the site-bound token only now that the host has authenticated.
	token, _, mstatus, mmsg := c.mintEnrollmentToken(user, site, v.HostID)
	if mstatus != 0 {
		fail(w, mstatus, mmsg)
		return
	}

	// (c) Push it to the host's local enrollment endpoint.
	status, enrolled, err := hostCall(ctx, client, http.MethodPost, addr, "/api/local/v1/enrollment", login["csrf_token"], map[string]string{
		"controller_url": v.ControllerURL,
		"ca_pem":         c.cfg.ServerCertPEM,
		"token":          token,
		"replace_host":   v.HostID,
	})
	if err != nil || status != 200 {
		c.deleteToken(token)
		if status == 409 {
			failWithHostID(w, 409, cmp.Or(enrolled["error"], "already enrolled"), enrolled["host_id"])
			return
		}
		detail := enrolled["error"]
		if detail == "" {
			detail = "connection failed"
		}
		fail(w, 502, fmt.Sprintf("host could not enroll with the controller at %s: %s", v.ControllerURL, detail))
		return
	}

	hostID := enrolled["host_id"]
	var pool string
	if hostID == "" || c.db.QueryRow("SELECT pool_id FROM hosts WHERE id=?", hostID).Scan(&pool) != nil {
		fail(w, 500, "host storage lookup failed")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.audit(tx, user, "host.add", site) != nil || tx.Commit() != nil {
		fail(w, 500, "audit storage failed")
		return
	}
	write(w, 200, addHostResponse{HostID: hostID, PoolID: pool})
}
