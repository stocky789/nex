// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

const witnessNSSDB = "/run/nex-witness/nssdb"

func qnetdCA(ctx context.Context) (string, string, *x509.Certificate, error) {
	raw, err := os.ReadFile(filepath.Join(witnessNSSDB, "qnetd-cacert.crt"))
	if err != nil {
		return "", "", nil, err
	}
	start := bytes.Index(raw, []byte("-----BEGIN CERTIFICATE-----"))
	if start < 0 {
		return "", "", nil, fmt.Errorf("qnetd CA certificate is unavailable")
	}
	block, rest := pem.Decode(raw[start:])
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return "", "", nil, fmt.Errorf("invalid qnetd CA export")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.Subject.CommonName != "QNet CA" {
		return "", "", nil, fmt.Errorf("invalid qnetd CA certificate")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dbCert, err := exec.CommandContext(ctx, "/usr/bin/certutil", "-L", "-a", "-d", "sql:"+witnessNSSDB, "-n", "QNet CA").Output()
	if err != nil {
		return "", "", nil, fmt.Errorf("qnetd CA database is unavailable")
	}
	dbBlock, dbRest := pem.Decode(dbCert)
	if dbBlock == nil || dbBlock.Type != "CERTIFICATE" || strings.TrimSpace(string(dbRest)) != "" || !bytes.Equal(dbBlock.Bytes, cert.Raw) {
		return "", "", nil, fmt.Errorf("qnetd CA export does not match its NSS database")
	}
	status, err := exec.CommandContext(ctx, "/usr/bin/corosync-qnetd-tool", "-s").Output()
	if err != nil || !qnetdStatusReady(string(status)) {
		return "", "", nil, fmt.Errorf("qnetd service is not serving the runtime CA with required TLS")
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	h := sha256.Sum256(cert.Raw)
	return string(b), hex.EncodeToString(h[:]), cert, nil
}

func qnetdStatusReady(status string) bool {
	var address, tls string
	for _, line := range strings.Split(status, "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok {
			switch strings.TrimSpace(key) {
			case "QNetd address":
				address = strings.TrimSpace(value)
			case "TLS":
				tls = strings.TrimSpace(value)
			}
		}
	}
	return address == "*:5403" && tls == "Required (client certificate required)"
}

func (c *Controller) agentWitnessCandidates(w http.ResponseWriter, r *http.Request, host string) {
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var pool, site string
	var master string
	if err = tx.QueryRow("SELECT h.pool_id,p.site_id,p.master_host_id FROM hosts h JOIN pools p ON p.id=h.pool_id WHERE h.id=? AND h.revoked=0", host).Scan(&pool, &site, &master); err != nil || master != host {
		fail(w, 409, "witness candidates require the current pool master")
		return
	}
	if err = c.managedHost(tx, pool, host); err != nil {
		fail(w, 409, "witness candidates require a fresh active pool master")
		return
	}
	candidates := protocol.PoolWitnessCandidates{Candidates: []protocol.PoolWitnessCandidate{}}
	poolCandidates, err := sameSitePoolWitnessCandidates(tx, site, pool)
	if err != nil {
		fail(w, 500, "witness candidates unavailable")
		return
	}
	candidates.Candidates = append(candidates.Candidates, poolCandidates...)
	if err = tx.Commit(); err != nil {
		fail(w, 500, "witness candidates unavailable")
		return
	}
	ca, pin, _, err := qnetdCA(r.Context())
	if err != nil {
		fail(w, 503, "Controller witness is not ready")
		return
	}
	candidates.Candidates = append(candidates.Candidates, protocol.PoolWitnessCandidate{Kind: "controller", CAPEM: ca, CASHA256: pin})
	write(w, http.StatusOK, candidates)
}

func sameSitePoolWitnessCandidates(tx *sql.Tx, site, excludePool string) ([]protocol.PoolWitnessCandidate, error) {
	rows, err := tx.Query("SELECT p.id,p.master_host_id,h.inventory FROM pools p JOIN hosts h ON h.id=p.master_host_id WHERE p.site_id=? AND p.id<>? ORDER BY p.id", site, excludePool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []protocol.PoolWitnessCandidate{}
	for rows.Next() {
		var poolID, masterID string
		var raw sql.NullString
		if err = rows.Scan(&poolID, &masterID, &raw); err != nil {
			return nil, err
		}
		var inventory protocol.Inventory
		if raw.Valid {
			_ = json.Unmarshal([]byte(raw.String), &inventory)
		}
		origin := ""
		if inventory.Managed != nil {
			origin = inventory.Managed.PeerOrigin
			if membership := inventory.Managed.Membership; membership != nil && membership.PoolID == poolID && membership.MasterHostID == masterID {
				for _, member := range membership.Members {
					if member.HostID == masterID && member.PeerOrigin != "" {
						origin = member.PeerOrigin
						break
					}
				}
			}
		}
		if address := qnetdAddress(origin); address != "" {
			candidates = append(candidates, protocol.PoolWitnessCandidate{Kind: "pool-master", PoolID: poolID, HostID: masterID, Address: address})
		}
	}
	return candidates, rows.Err()
}

func qnetdAddress(origin string) string {
	normalized := normalizedHTTPSOrigin(origin)
	if normalized == "" {
		return ""
	}
	u, err := url.Parse(normalized)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return net.JoinHostPort(u.Hostname(), "5403")
}

func (c *Controller) agentWitnessRegister(w http.ResponseWriter, r *http.Request, host string) {
	var body protocol.PoolWitnessRegistrationRequest
	if !decodeLimit(w, r, &body, 128<<10) {
		return
	}
	if !identityValid(body.PoolID) || body.ClusterName != protocol.PoolWitnessClusterName(body.PoolID) || body.FencingGeneration == 0 || len(body.CSRPEM) > 64<<10 {
		fail(w, 400, "pool_id, deterministic cluster_name, fencing_generation and csr_pem are required")
		return
	}
	block, rest := pem.Decode([]byte(body.CSRPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(strings.TrimSpace(string(rest))) != 0 {
		fail(w, 400, "csr_pem must contain one PEM certificate request")
		return
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil || csr.Subject.String() != "CN="+body.ClusterName || len(csr.Extensions) != 0 {
		fail(w, 400, "CSR must have a valid signature and the deterministic cluster common name")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	master, status, err := c.freshPoolMaster(tx, body.PoolID, "register a witness")
	if err != nil || master != host {
		if status == 0 {
			status = http.StatusConflict
		}
		fail(w, status, "only the fresh current pool master may register its witness")
		return
	}
	var raw sql.NullString
	if err = tx.QueryRow("SELECT inventory FROM hosts WHERE id=? AND pool_id=? AND revoked=0", host, body.PoolID).Scan(&raw); err != nil || !raw.Valid {
		fail(w, 409, "current pool membership is unavailable")
		return
	}
	var inventory protocol.Inventory
	if err = json.Unmarshal([]byte(raw.String), &inventory); err != nil {
		fail(w, 409, "current pool membership is unavailable")
		return
	}
	membership := reportedMembership(inventory)
	if membership == nil || membership.PoolID != body.PoolID || membership.MasterHostID != host || membership.Generation != body.FencingGeneration {
		fail(w, 409, "reported pool authority does not match witness registration")
		return
	}
	conflict, err := witnessClusterNameConflict(tx, body.PoolID, body.ClusterName)
	if err != nil {
		fail(w, 500, "witness registration could not be confirmed")
		return
	}
	if conflict {
		fail(w, 409, "witness cluster name belongs to another pool")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "witness registration could not be confirmed")
		return
	}

	caPEM, pin, ca, err := qnetdCA(r.Context())
	if err != nil {
		fail(w, 503, "Controller witness is not ready")
		return
	}
	c.witnessMu.Lock()
	defer c.witnessMu.Unlock()
	tmp, err := os.CreateTemp(witnessNSSDB, "request-*.pem")
	if err != nil {
		fail(w, 503, "Controller witness is not ready")
		return
	}
	requestPath := tmp.Name()
	defer os.Remove(requestPath)
	if _, err = tmp.Write(block.Bytes); err == nil {
		err = tmp.Close()
	} else {
		tmp.Close()
	}
	if err != nil {
		fail(w, 500, "witness registration failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/corosync-qnetd-certutil", "-s", "-c", requestPath, "-n", body.ClusterName)
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = output
		fail(w, 503, "Controller witness could not sign the certificate request")
		return
	}
	certPath := filepath.Join(witnessNSSDB, "cluster-"+body.ClusterName+".crt")
	certInfo, err := os.Lstat(certPath)
	if err != nil || !certInfo.Mode().IsRegular() {
		fail(w, 503, "Controller witness certificate is unavailable")
		return
	}
	qnetdGroup, err := user.LookupGroup("coroqnetd")
	if err != nil {
		fail(w, 503, "Controller witness certificate is unavailable")
		return
	}
	groupID, err := strconv.Atoi(qnetdGroup.Gid)
	if err != nil || os.Chown(certPath, -1, groupID) != nil || os.Chmod(certPath, 0640) != nil {
		fail(w, 503, "Controller witness certificate is unavailable")
		return
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		fail(w, 503, "Controller witness certificate is unavailable")
		return
	}
	certs, err := x509.ParseCertificates(certPEM)
	if err != nil || len(certs) != 1 {
		fail(w, 503, "Controller witness certificate is invalid")
		return
	}
	cert := certs[0]
	_, verifyErr := cert.Verify(x509.VerifyOptions{Roots: certPool(ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if cert.Subject.CommonName != body.ClusterName || cert.IsCA || string(cert.RawSubjectPublicKeyInfo) != string(csr.RawSubjectPublicKeyInfo) || verifyErr != nil {
		fail(w, 503, "Controller witness certificate validation failed")
		return
	}
	currentCA, currentPin, _, err := qnetdCA(r.Context())
	if err != nil || currentPin != pin || currentCA != caPEM {
		fail(w, 503, "Controller witness CA changed during registration")
		return
	}
	write(w, 200, protocol.PoolWitnessRegistration{CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})), CAPEM: caPEM, CASHA256: pin})
}

func witnessClusterNameConflict(tx *sql.Tx, poolID, clusterName string) (bool, error) {
	rows, err := tx.Query("SELECT id FROM pools WHERE id<>?", poolID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var otherPoolID string
		if err := rows.Scan(&otherPoolID); err != nil {
			return false, err
		}
		if protocol.PoolWitnessClusterName(otherPoolID) == clusterName {
			return true, nil
		}
	}
	return false, rows.Err()
}

func certPool(cert *x509.Certificate) *x509.CertPool {
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return roots
}
