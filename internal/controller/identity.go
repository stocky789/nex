// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func loadAuthority(dir string) (*authority, error) {
	certPath, keyPath := filepath.Join(dir, "host-ca.pem"), filepath.Join(dir, "host-ca-key.pem")
	certPEM, err := os.ReadFile(certPath)
	if os.IsNotExist(err) {
		if _, e := os.Stat(keyPath); !os.IsNotExist(e) {
			return nil, errors.New("host CA certificate missing but key exists; restore matching identity")
		}
		key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return nil, e
		}
		serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Nex host identity CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
		der, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if e != nil {
			return nil, e
		}
		keyDER, e := x509.MarshalECPrivateKey(key)
		if e != nil {
			return nil, e
		}
		if e = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); e != nil {
			return nil, e
		}
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if e = os.WriteFile(certPath, certPEM, 0600); e != nil {
			return nil, e
		}
	} else if err != nil {
		return nil, err
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("unsupported CA key")
	}
	return &authority{cert, key, string(certPEM)}, nil
}
func (c *Controller) TLSConfig() *tls.Config {
	// HTTP/1.1 is preferred so browser console WebSockets and host console tunnels can
	// upgrade the connection; HTTP/2 cannot be hijacked.
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequestClientCert, NextProtos: []string{"http/1.1"}}
}
func (c *Controller) issue(csrPEM, hostID string) (string, string, error) {
	block, rest := pem.Decode([]byte(csrPEM))
	if block == nil || len(rest) > 0 || block.Type != "CERTIFICATE REQUEST" {
		return "", "", errors.New("invalid CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return "", "", errors.New("invalid CSR signature")
	}
	switch key := csr.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if key.Curve != elliptic.P256() {
			return "", "", errors.New("P-256 host key required")
		}
	default:
		return "", "", errors.New("P-256 host key required")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: hostID}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.ca.cert, csr.PublicKey, c.ca.key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), serial.String(), nil
}
func (c *Controller) enroll(w http.ResponseWriter, r *http.Request) {
	var v protocol.EnrollRequest
	if !decode(w, r, &v) {
		return
	}
	if v.Version != protocol.Version {
		fail(w, 409, "unsupported protocol version")
		return
	}
	if !nameValid(v.Hostname) {
		fail(w, 400, "hostname required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var site, host string
	var expiry int64
	var csr, response sql.NullString
	err = tx.QueryRow("SELECT site_id,host_id,expires,csr,response FROM tokens WHERE hash=?", digest(v.Token)).Scan(&site, &host, &expiry, &csr, &response)
	if err != nil || expiry < time.Now().Unix() {
		fail(w, 401, "enrollment token invalid or expired")
		return
	}
	if csr.Valid {
		if csr.String != digest(v.CSRPEM) {
			fail(w, 409, "enrollment token already consumed")
			return
		}
		var previous protocol.EnrollResponse
		json.Unmarshal([]byte(response.String), &previous)
		var revoked bool
		if tx.QueryRow("SELECT revoked FROM hosts WHERE id=?", previous.HostID).Scan(&revoked) != nil || revoked {
			fail(w, 401, "host revoked")
			return
		}
		write(w, 200, previous)
		return
	}
	// Identity is host-owned: the host presents the host and pool identity it generated and
	// keeps, and any Controller adopts it unchanged. Agents from before host-owned identity
	// present nothing and get Controller-minted IDs, which they then keep. A token bound to a
	// revoked host re-issues that host's credential and keeps its pool; only that path may
	// reuse a host ID already on record, so a token holder cannot claim an active host.
	if (v.HostID != "" && !identityValid(v.HostID)) || (v.PoolID != "" && !identityValid(v.PoolID)) || (v.PoolName != "" && !nameValid(v.PoolName)) {
		fail(w, 400, "host_id and pool_id must be UUIDs and pool_name a name")
		return
	}
	var pool, poolName string
	fresh := host == ""
	if fresh {
		host, pool, poolName = v.HostID, v.PoolID, v.PoolName
		if host == "" {
			host = random()
		}
		if pool == "" {
			pool = random()
		}
		if poolName == "" {
			poolName = v.Hostname
		}
		// The same key is the same host: a retry after a lost response, even with a new
		// token, gets the credential already recorded instead of a duplicate or a refusal.
		previous := protocol.EnrollResponse{CAPEM: c.ca.pem}
		if tx.QueryRow("SELECT c.host_id,h.pool_id,p.name,c.pem FROM certificates c JOIN hosts h ON h.id=c.host_id JOIN pools p ON p.id=h.pool_id WHERE c.csr=? AND c.expires>? AND h.revoked=0", digest(v.CSRPEM), time.Now().Unix()).Scan(&previous.HostID, &previous.PoolID, &previous.PoolName, &previous.CertificatePEM) == nil {
			if (v.HostID != "" && v.HostID != previous.HostID) || (v.PoolID != "" && v.PoolID != previous.PoolID) {
				fail(w, 409, "presented identity does not match the host holding this key")
				return
			}
			b, _ := json.Marshal(previous)
			if _, err = tx.Exec("UPDATE tokens SET csr=?,response=?,host_id=? WHERE hash=?", digest(v.CSRPEM), string(b), previous.HostID, digest(v.Token)); err != nil || c.audit(tx, "host:"+previous.HostID, "host.enroll", previous.HostID) != nil || tx.Commit() != nil {
				fail(w, 500, "enrollment storage failed")
				return
			}
			write(w, 200, previous)
			return
		}
		var n int
		if tx.QueryRow("SELECT count(*) FROM hosts WHERE id=?", host).Scan(&n) != nil || n != 0 {
			fail(w, 409, "host identity already enrolled; revoke it and use a token targeting that host")
			return
		}
		if tx.QueryRow("SELECT count(*) FROM pools WHERE id=?", pool).Scan(&n) != nil || n != 0 {
			fail(w, 409, "pool identity already held by another master host")
			return
		}
	} else {
		var revoked bool
		if tx.QueryRow("SELECT h.revoked,h.pool_id,p.name FROM hosts h JOIN pools p ON p.id=h.pool_id WHERE h.id=? AND p.site_id=?", host, site).Scan(&revoked, &pool, &poolName) != nil || !revoked {
			fail(w, 409, "replacement host must still be revoked")
			return
		}
		if (v.HostID != "" && v.HostID != host) || (v.PoolID != "" && v.PoolID != pool) {
			fail(w, 409, "presented identity does not match the host this token replaces")
			return
		}
	}
	cert, serial, err := c.issue(v.CSRPEM, host)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if fresh {
		if _, err = tx.Exec("INSERT INTO pools(id,site_id,name,master_host_id) VALUES(?,?,?,?)", pool, site, poolName, host); err != nil || c.audit(tx, "host:"+host, "pool.create", pool) != nil {
			fail(w, 500, "pool storage failed")
			return
		}
	}
	_, err = tx.Exec("INSERT INTO hosts(id,pool_id,hostname) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET revoked=0,hostname=excluded.hostname,sequence=0,last_seen=NULL,inventory=NULL", host, pool, v.Hostname)
	if err != nil {
		fail(w, 500, "host storage failed")
		return
	}
	if _, err = tx.Exec("DELETE FROM certificates WHERE host_id=?", host); err != nil {
		fail(w, 500, "certificate storage failed")
		return
	}
	if _, err = tx.Exec("INSERT INTO certificates VALUES(?,?,?,?,?)", serial, host, digest(v.CSRPEM), cert, time.Now().Add(90*24*time.Hour).Unix()); err != nil {
		fail(w, 500, "certificate storage failed")
		return
	}
	out := protocol.EnrollResponse{HostID: host, PoolID: pool, PoolName: poolName, CertificatePEM: cert, CAPEM: c.ca.pem}
	b, _ := json.Marshal(out)
	if _, err = tx.Exec("UPDATE tokens SET csr=?,response=?,host_id=? WHERE hash=?", digest(v.CSRPEM), string(b), host, digest(v.Token)); err != nil || c.audit(tx, "host:"+host, "host.enroll", host) != nil || tx.Commit() != nil {
		fail(w, 500, "enrollment storage failed")
		return
	}
	c.mu.Lock()
	delete(c.seen, host)
	c.mu.Unlock()
	write(w, 200, out)
}

// identityValid accepts a host-generated lowercase UUID or a Controller-minted 64-hex ID
// issued to hosts enrolled before host-owned identity, which they keep.
var identityValid = regexp.MustCompile(`^(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{64})$`).MatchString

type agentHandler func(http.ResponseWriter, *http.Request, string)

func (c *Controller) agent(next agentHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			fail(w, 401, "host certificate required")
			return
		}
		cert := r.TLS.PeerCertificates[0]
		roots := x509.NewCertPool()
		roots.AddCert(c.ca.cert)
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			fail(w, 401, "invalid host certificate")
			return
		}
		var host string
		err := c.db.QueryRow("SELECT certificates.host_id FROM certificates JOIN hosts ON hosts.id=certificates.host_id WHERE serial=? AND expires>? AND revoked=0", cert.SerialNumber.String(), time.Now().Unix()).Scan(&host)
		if err != nil || cert.Subject.CommonName != host {
			fail(w, 401, "host identity revoked or expired")
			return
		}
		next(w, r, host)
	}
}
func (c *Controller) rotate(w http.ResponseWriter, r *http.Request, host string) {
	var v protocol.RotateRequest
	if !decode(w, r, &v) {
		return
	}
	if v.Version != protocol.Version {
		fail(w, 409, "unsupported protocol version")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if !transactionIdentity(tx, r, host) {
		fail(w, 401, "host credential no longer valid")
		return
	}
	var cert string
	err = tx.QueryRow("SELECT pem FROM certificates WHERE host_id=? AND csr=? AND expires>?", host, digest(v.CSRPEM), time.Now().Unix()).Scan(&cert)
	if err == nil {
		write(w, 200, protocol.EnrollResponse{HostID: host, CertificatePEM: cert, CAPEM: c.ca.pem})
		return
	}
	cert, serial, err := c.issue(v.CSRPEM, host)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if _, err = tx.Exec("UPDATE certificates SET expires=MIN(expires,?) WHERE host_id=?", time.Now().Add(10*time.Minute).Unix(), host); err != nil {
		fail(w, 500, "rotation storage failed")
		return
	}
	if _, err = tx.Exec("INSERT INTO certificates VALUES(?,?,?,?,?)", serial, host, digest(v.CSRPEM), cert, time.Now().Add(90*24*time.Hour).Unix()); err != nil || c.audit(tx, "host:"+host, "host.rotate", host) != nil || tx.Commit() != nil {
		fail(w, 500, "rotation storage failed")
		return
	}
	write(w, 200, protocol.EnrollResponse{HostID: host, CertificatePEM: cert, CAPEM: c.ca.pem})
}
func (c *Controller) report(w http.ResponseWriter, r *http.Request, host string) {
	var v protocol.ReportRequest
	if !decodeLimit(w, r, &v, 16<<20) {
		return
	}
	if v.Version != protocol.Version {
		fail(w, 409, "unsupported protocol version")
		return
	}
	if err := validateInventory(v.Inventory); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(v.Capabilities) > 32 {
		fail(w, 400, "too many capabilities")
		return
	}
	for _, capability := range v.Capabilities {
		if len(capability) > 100 {
			fail(w, 400, "invalid capability")
			return
		}
	}
	if v.Sequence == 0 || v.Sequence > 1<<63-1 || len(v.Results) > 100 {
		fail(w, 400, "invalid report sequence or result count")
		return
	}
	for _, res := range v.Results {
		if (res.Status != "succeeded" && res.Status != "failed" && res.Status != "needs_attention") || len(res.Error) > 4096 {
			fail(w, 400, "invalid operation result")
			return
		}
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if !transactionIdentity(tx, r, host) {
		fail(w, 401, "host credential no longer valid")
		return
	}
	var previous sql.NullString
	var merged *protocol.Inventory
	previousSamples := map[string]time.Time{}
	var seq uint64
	var revoked bool
	if tx.QueryRow("SELECT sequence,revoked,inventory FROM hosts WHERE id=?", host).Scan(&seq, &revoked, &previous) != nil || revoked {
		fail(w, 401, "host revoked")
		return
	}
	out := protocol.ReportResponse{Commands: []protocol.Command{}, AcknowledgedResults: []string{}}
	for _, res := range v.Results {
		var status string
		err = tx.QueryRow("SELECT status FROM operations WHERE id=? AND host_id=?", res.OperationID, host).Scan(&status)
		if err != nil {
			fail(w, 400, "result operation does not belong to host")
			return
		}
		if status == "queued" {
			fail(w, 409, "operation has not been delivered")
			return
		}
		if status == "dispatched" {
			if _, err = tx.Exec("UPDATE operations SET status=?,error=?,updated_at=? WHERE id=?", res.Status, res.Error, stamp(), res.OperationID); err != nil || c.audit(tx, "host:"+host, "operation."+res.Status, res.OperationID) != nil {
				fail(w, 500, "result storage failed")
				return
			}
			if res.Status == "succeeded" {
				var kind string
				if tx.QueryRow("SELECT kind FROM operations WHERE id=?", res.OperationID).Scan(&kind) == nil && kind == "vm-media" {
					if err = c.mirrorVMMedia(tx, res.OperationID); err != nil {
						fail(w, 500, "media mirror failed")
						return
					}
				}
			}
		}
		out.AcknowledgedResults = append(out.AcknowledgedResults, res.OperationID)
	}
	if v.Sequence > seq {
		capabilities, _ := json.Marshal(v.Capabilities)
		if _, err = tx.Exec("INSERT INTO host_capabilities VALUES(?,?) ON CONFLICT(host_id) DO UPDATE SET capabilities=excluded.capabilities", host, string(capabilities)); err != nil {
			fail(w, 500, "capability storage failed")
			return
		}
		if v.Inventory != nil {
			if previous.Valid {
				if err = json.Unmarshal([]byte(previous.String), &merged); err != nil {
					fail(w, 500, "stored inventory invalid")
					return
				}
			}
			if merged != nil {
				for _, check := range merged.Checks {
					previousSamples[check.Name] = check.ObservedAt
				}
			}
			merged = mergeInventory(merged, v.Inventory)
			if len(merged.Checks) > 256 {
				fail(w, 400, "too many distinct health checks")
				return
			}
			if hasManagedCapability(v.Capabilities) {
				err = c.adoptManaged(tx, host, merged)
			}
			// The managed journal is mirrored into managed_resources, not stored as inventory.
			stored := *merged
			stored.Managed = nil
			b, _ := json.Marshal(stored)
			if err == nil {
				_, err = tx.Exec("UPDATE hosts SET inventory=?,last_seen=?,sequence=? WHERE id=?", string(b), stamp(), v.Sequence, host)
			}
		} else {
			_, err = tx.Exec("UPDATE hosts SET last_seen=?,sequence=? WHERE id=?", stamp(), v.Sequence, host)
		}
		if err != nil {
			fail(w, 500, "report storage failed")
			return
		}
	}
	out.Consoles = c.consoles.pending(host)
	rows, err := tx.Query("SELECT id,kind FROM operations WHERE host_id=? AND status IN ('queued','dispatched') ORDER BY created_at LIMIT 100", host)
	if err != nil {
		fail(w, 500, "command lookup failed")
		return
	}
	for rows.Next() {
		var cmd protocol.Command
		if rows.Scan(&cmd.OperationID, &cmd.Kind) != nil {
			rows.Close()
			fail(w, 500, "command decode failed")
			return
		}
		out.Commands = append(out.Commands, cmd)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		fail(w, 500, "command lookup failed")
		return
	}
	filtered := out.Commands[:0]
	for _, cmd := range out.Commands {
		if cmd.Kind != "refresh-inventory" {
			if !hasManagedCapability(v.Capabilities) {
				continue
			}
			var raw string
			if tx.QueryRow("SELECT command FROM managed_resources WHERE operation_id=?", cmd.OperationID).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &cmd) != nil {
				fail(w, 500, "command payload unavailable")
				return
			}
		}
		filtered = append(filtered, cmd)
	}
	out.Commands = filtered
	for _, cmd := range out.Commands {
		if _, err = tx.Exec("UPDATE operations SET status='dispatched',updated_at=? WHERE id=? AND status='queued'", stamp(), cmd.OperationID); err != nil {
			fail(w, 500, "command dispatch failed")
			return
		}
	}
	if tx.Commit() != nil {
		fail(w, 500, "report commit failed")
		return
	}
	if v.Sequence > seq {
		c.mu.Lock()
		c.seen[host] = time.Now()
		if merged != nil {
			for _, check := range merged.Checks {
				key := host + "/" + check.Name
				old, ok := c.observed[key]
				if !check.ObservedAt.IsZero() && ((!ok && !check.ObservedAt.Equal(previousSamples[check.Name])) || (ok && !check.ObservedAt.Equal(old.sample))) {
					c.observed[key] = observationReceipt{sample: check.ObservedAt, received: time.Now()}
				}
			}
		}
		c.mu.Unlock()
	}
	c.cleanMedia()
	write(w, 200, out)
}

func transactionIdentity(tx *sql.Tx, r *http.Request, host string) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	var n int
	return tx.QueryRow("SELECT count(*) FROM certificates JOIN hosts ON hosts.id=certificates.host_id WHERE serial=? AND certificates.host_id=? AND expires>? AND revoked=0", r.TLS.PeerCertificates[0].SerialNumber.String(), host, time.Now().Unix()).Scan(&n) == nil && n == 1
}
