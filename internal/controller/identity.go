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
	"slices"
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

// poolIdentityHeldRefusal is the enrollment refusal a pool member gets while its pool master
// has not yet told this Controller that the member is ready. It is a normal, documented wait
// during a Controller replacement, so the add-host route recognises it by name and answers a
// retryable conflict instead of a transport failure.
const poolIdentityHeldRefusal = "pool identity is held; the pool master has not reported this ready member"

func (c *Controller) enroll(w http.ResponseWriter, r *http.Request) {
	var v protocol.EnrollRequest
	if !decode(w, r, &v) {
		return
	}
	if v.Version != protocol.LegacyVersion && v.Version != protocol.Version {
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
		// token from the same site, gets the credential already recorded instead of a
		// duplicate or a refusal. A token from another site enrolls afresh under its own site.
		previous := protocol.EnrollResponse{CAPEM: c.ca.pem}
		if tx.QueryRow("SELECT c.host_id,h.pool_id,p.name,c.pem FROM certificates c JOIN hosts h ON h.id=c.host_id JOIN pools p ON p.id=h.pool_id WHERE c.csr=? AND c.expires>? AND h.revoked=0 AND p.site_id=?", digest(v.CSRPEM), time.Now().Unix(), site).Scan(&previous.HostID, &previous.PoolID, &previous.PoolName, &previous.CertificatePEM) == nil {
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
		if tx.QueryRow("SELECT count(*) FROM pools WHERE id=?", pool).Scan(&n) != nil {
			fail(w, 500, "pool lookup failed")
			return
		}
		if n != 0 {
			if !c.coreReportedPoolMember(tx, site, pool, host) {
				fail(w, 409, poolIdentityHeldRefusal)
				return
			}
			if tx.QueryRow("SELECT name FROM pools WHERE id=?", pool).Scan(&poolName) != nil || (v.PoolName != "" && v.PoolName != poolName) {
				fail(w, 409, "presented pool name does not match the Core-reported pool")
				return
			}
			fresh = false // The master Core already owns this pool's durable identity.
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
	// A recorded master enrolling late (the pool's own record named it while it was away)
	// takes back the mirror rows another host held meanwhile.
	var master string
	if err = tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil || (master == host && rekeyPoolMirrors(tx, pool, host) != nil) {
		fail(w, 500, "pool mirror storage failed")
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

// coreReportedPoolMember allows a new Controller to adopt a peer only after its
// already-enrolled master has reported the Core-owned membership record.  The database
// copy is a disposable inventory projection, never an authority to add or retain members.
func (c *Controller) coreReportedPoolMember(tx *sql.Tx, site, pool, host string) bool {
	var master string
	if tx.QueryRow("SELECT master_host_id FROM pools WHERE id=? AND site_id=?", pool, site).Scan(&master) != nil {
		return false
	}
	var membership *protocol.PoolMembership
	var raw string
	var revoked bool
	switch err := tx.QueryRow("SELECT revoked,inventory FROM hosts WHERE id=?", master).Scan(&revoked, &raw); {
	case err == sql.ErrNoRows:
		// A recorded master that never enrolled here: the pool's record as its enrolled
		// hosts reported it is the only evidence there is (see adoptReportedMaster).
		if membership, err = poolRecordNaming(tx, pool, master); err != nil {
			return false
		}
	case err != nil || revoked:
		return false
	default:
		var inventory protocol.Inventory
		if json.Unmarshal([]byte(raw), &inventory) == nil {
			membership = reportedMembership(inventory)
		}
	}
	if membership == nil {
		return false
	}
	// The recorded master's own report is the evidence, and it settles two cases: it names
	// itself master, or it names the host now presenting this pool identity. The second is a
	// replacement Controller where a member reconnected first and so holds the pool row it
	// created: its report is the pool's own record that the host enrolling now is the master,
	// and adoptReportedMaster moves the row once that master reports for itself.
	if membership.Valid() != nil || membership.PoolID != pool || (membership.MasterHostID != master && membership.MasterHostID != host) {
		return false
	}
	for _, member := range membership.Members {
		if member.HostID == host && member.Status == "ready" {
			return true
		}
	}
	return false
}

// adoptReportedPoolAssignment moves an already-enrolled peer's disposable Controller
// projection only after the destination master has reported it as a ready Core member.
// Joining never deletes the peer's existing journal mirrors; its next report reprojects
// them under the Core-owned pool identity.
func (c *Controller) adoptReportedPoolAssignment(tx *sql.Tx, host string, inventory *protocol.Inventory) (string, error) {
	if inventory == nil || inventory.Managed == nil || inventory.Managed.Membership == nil {
		return "", nil
	}
	membership := inventory.Managed.Membership
	if membership.Valid() != nil || membership.MasterHostID == host {
		return "", nil
	}
	ready := false
	for _, member := range membership.Members {
		ready = ready || member.HostID == host && member.Status == "ready"
	}
	if !ready {
		return "", nil
	}
	var site, current string
	if err := tx.QueryRow("SELECT p.site_id,h.pool_id FROM hosts h JOIN pools p ON p.id=h.pool_id WHERE h.id=?", host).Scan(&site, &current); err != nil {
		return "", err
	}
	if current != membership.PoolID && c.coreReportedPoolMember(tx, site, membership.PoolID, host) {
		_, err := tx.Exec("UPDATE hosts SET pool_id=? WHERE id=?", membership.PoolID, host)
		return current, err
	}
	return "", nil
}

// cleanupJoinedAutoPool removes only an already-empty former automatically-created pool after
// this host's Core membership is known. Actual Core inventory adoption, not this cleanup,
// rehomes resource projections and preserves command/payload/audit history.
func (c *Controller) cleanupJoinedAutoPool(tx *sql.Tx, host, former string, inventory *protocol.Inventory) error {
	if inventory == nil || inventory.Managed == nil || inventory.Managed.Membership == nil {
		return nil
	}
	membership := inventory.Managed.Membership
	if membership.Valid() != nil || membership.MasterHostID == host {
		return nil
	}
	var site string
	if err := tx.QueryRow("SELECT p.site_id FROM hosts h JOIN pools p ON p.id=h.pool_id WHERE h.id=?", host).Scan(&site); err != nil || !c.coreReportedPoolMember(tx, site, membership.PoolID, host) {
		return err
	}
	var candidates []string
	if former != "" {
		candidates = []string{former}
	} else {
		// A host already moved by an older Controller has no remembered source pool.
		// Only its one empty, same-site, hostname-named auto-pool is safe to prune;
		// ambiguity leaves projections visible rather than deleting an unrelated pool.
		var hostname string
		if err := tx.QueryRow("SELECT hostname FROM hosts WHERE id=?", host).Scan(&hostname); err != nil {
			return err
		}
		rows, err := tx.Query("SELECT id FROM pools WHERE master_host_id=? AND site_id=? AND name=? AND id<>?", host, site, hostname, membership.PoolID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			candidates = append(candidates, id)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(candidates) != 1 {
			return nil
		}
	}
	for _, id := range candidates {
		var hosts, resources, appliances int
		if err := tx.QueryRow("SELECT count(*) FROM hosts WHERE pool_id=?", id).Scan(&hosts); err != nil || hosts != 0 {
			return err
		}
		if err := tx.QueryRow("SELECT count(*) FROM managed_resources WHERE pool_id=?", id).Scan(&resources); err != nil || resources != 0 {
			return err
		}
		if err := tx.QueryRow("SELECT count(*) FROM appliances WHERE pool_id=?", id).Scan(&appliances); err != nil || appliances != 0 {
			return err
		}
		if _, err := tx.Exec("DELETE FROM pools WHERE id=? AND master_host_id=?", id, host); err != nil {
			return err
		}
	}
	return nil
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
	if v.Version != protocol.LegacyVersion && v.Version != protocol.Version {
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
	if v.Version != protocol.LegacyVersion && v.Version != protocol.Version {
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
	if len(v.StorageClusters) > 128 {
		fail(w, 400, "too many clustered storage results")
		return
	}
	if len(v.PoolResults) > 32 {
		fail(w, 400, "too many pool results")
		return
	}
	for _, result := range v.StorageClusters {
		if !clusterResultValid(result) || (!slices.Contains(v.Capabilities, protocol.StorageClusterSessionMonitorCapability) && clusterResultHasSessionMonitor(result)) {
			fail(w, 400, "invalid clustered storage result")
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
	var formerPool string
	previousSamples := map[string]time.Time{}
	var seq uint64
	var revoked bool
	if tx.QueryRow("SELECT sequence,revoked,inventory FROM hosts WHERE id=?", host).Scan(&seq, &revoked, &previous) != nil || revoked {
		fail(w, 401, "host revoked")
		return
	}
	out := protocol.ReportResponse{Commands: []protocol.Command{}, AcknowledgedResults: []string{}}
	c.completeStorageDiscoveries(host, v.StorageDiscoveries)
	c.completeStorageClusters(host, v.StorageClusters)
	c.completePoolResults(host, v.PoolResults)
	// A v2 Core asks for this acknowledgement before it advertises the repository
	// capability. v1 peers receive their original response shape unchanged.
	if v.Version == protocol.Version {
		repositoryCapability := protocol.StorageRepositoryCapability
		// Reuse the established support capability handshake: a Core must not send
		// ZFS-only inventory to a strict older Controller until this acknowledgement.
		// storage-zfs-replace-v1 is the narrowest ack for hot spares, widened layouts
		// and the additive zfs_pool / pool observation fields.
		if slices.Contains(v.Capabilities, protocol.ZFSProvisionCapability) {
			repositoryCapability = protocol.ZFSProvisionCapability
		}
		if slices.Contains(v.Capabilities, protocol.ZFSReplaceCapability) {
			repositoryCapability = protocol.ZFSReplaceCapability
		}
		out.StorageRepositories = &protocol.StorageRepositorySupport{ProtocolVersion: protocol.Version, Capability: repositoryCapability}
		clusterCapability := protocol.StorageClusterCapability
		if slices.Contains(v.Capabilities, protocol.StorageClusterSessionMonitorCapability) {
			clusterCapability = protocol.StorageClusterSessionMonitorCapability
		}
		out.StorageClusters = &protocol.StorageClusterSupport{Capability: clusterCapability}
		if slices.Contains(v.Capabilities, protocol.StorageRepositoryCapability) {
			out.CredentialOffers = c.credentials.offers(host)
		}
	}
	if v.Version == protocol.Version && slices.Contains(v.Capabilities, protocol.StorageDiscoveryCapability) {
		out.StorageDiscoveries = c.takeStorageDiscoveries(host)
	}
	if v.Version == protocol.Version && slices.Contains(v.Capabilities, protocol.StorageClusterCapability) {
		out.StorageClusterRequests = c.takeStorageClusters(host)
	}
	// The one brokered round trip serves two actions now: an invitation, which needs the
	// join capability, and a promotion preflight, which needs the transfer one. A host that
	// advertises either can be asked, and the broker only ever holds a request addressed to
	// it; a host that advertises neither is never asked anything.
	if v.Version == protocol.Version && (slices.Contains(v.Capabilities, protocol.PoolJoinCapability) || slices.Contains(v.Capabilities, protocol.PoolAuthorityTransferCapability)) {
		out.PoolRequests = c.takePoolRequests(host)
	}
	for _, res := range v.Results {
		var status, kind string
		err = tx.QueryRow("SELECT status,kind FROM operations WHERE id=? AND host_id=?", res.OperationID, host).Scan(&status, &kind)
		if err != nil {
			if err == sql.ErrNoRows {
				// A replacement Controller has no record of terminal work that Core
				// completed under the former Controller. Acknowledge that host's
				// retained result without manufacturing state from it. An operation
				// known for another host is still a rejected cross-host result.
				var owner string
				ownerErr := tx.QueryRow("SELECT host_id FROM operations WHERE id=?", res.OperationID).Scan(&owner)
				if ownerErr == nil && owner != host {
					fail(w, 400, "result operation does not belong to host")
					return
				}
				if ownerErr == sql.ErrNoRows {
					// Nothing here is stored. Echo any ID the agent accepts for a command
					// (non-empty, at most 128 bytes) so a former Controller's result is
					// acknowledged instead of being re-sent forever.
					if res.OperationID != "" && len(res.OperationID) <= 128 {
						out.AcknowledgedResults = append(out.AcknowledgedResults, res.OperationID)
					}
					continue
				}
				if ownerErr != nil {
					fail(w, 500, "result lookup failed")
					return
				}
			}
			fail(w, 400, "result operation does not belong to host")
			return
		}
		if status == "queued" {
			fail(w, 409, "operation has not been delivered")
			return
		}
		// Core may recover a durable ZFS provision after it previously reported
		// needs_attention (an interrupted create whose native receipt later verifies).
		// Accept only that exact authoritative recovery; other kinds settle through the
		// host's reported managed outcomes, and the Controller never infers success from
		// an adopted storage projection.
		if status == "dispatched" || (status == "needs_attention" && kind == "storage-zfs-provision" && res.Status == "succeeded") {
			if _, err = tx.Exec("UPDATE operations SET status=?,error=?,updated_at=? WHERE id=?", res.Status, res.Error, stamp(), res.OperationID); err != nil || c.audit(tx, "host:"+host, "operation."+res.Status, res.OperationID) != nil {
				fail(w, 500, "result storage failed")
				return
			}
			if res.Status == "failed" && kind == "vm-create" {
				if _, err = tx.Exec("UPDATE managed_resources SET id=? WHERE operation_id=? AND kind='vm-create'", failedVMCreateRecordID(res.OperationID), res.OperationID); err != nil {
					fail(w, 500, "VM replay record storage failed")
					return
				}
			}
			if res.Status == "succeeded" {
				if kind == "vm-media" {
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
			if formerPool, err = c.adoptReportedPoolAssignment(tx, host, v.Inventory); err != nil {
				fail(w, 500, "pool membership adoption failed")
				return
			}
			// The hosts own the pool master. A host reporting itself master with newer
			// evidence than this Controller mirrors is followed here, before its journal is
			// projected, so the projection is keyed to the master the pool now names.
			if err = c.adoptReportedMaster(tx, host, v.Inventory); err != nil {
				fail(w, 500, "pool master adoption failed")
				return
			}
			if v.Inventory.Managed != nil {
				c.credentials.observeReceipts(host, v.Inventory.Managed.CredentialReceipts)
			}
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
			if slices.Contains(v.Capabilities, protocol.ManagedVMCapability) {
				err = c.adoptManaged(tx, host, merged)
				if err == nil {
					err = c.cleanupJoinedAutoPool(tx, host, formerPool, merged)
				}
			}
			// Managed definitions are mirrored into managed_resources. Keep only the
			// master-owned membership and ZFS outcomes needed for host status here.
			stored := *merged
			// Membership is retained only as the master's latest Core report so a
			// replacement Controller can enroll a proven peer. Other managed state is
			// projected into managed_resources and must not be duplicated in inventory.
			if merged.Managed != nil && (merged.Managed.Membership != nil || merged.Managed.Join != nil || merged.Managed.Replica != nil || merged.Managed.Transfer != nil || merged.Managed.Authority != nil || merged.Managed.Promotion != nil || merged.Managed.Restricted != nil || len(merged.Managed.ZFSProvisions) != 0) {
				// These are Core's master-journal outcomes, retained only as the latest
				// host observation so the Controller can render per-host status. A fresh
				// Controller re-adopts them from the next Core inventory report.
				// Join is the joining host's own staged-join report, retained for the same
				// reason: it is per-host status a replacement Controller must be able to show.
				// Replica is this host's own report of the replica it holds; it is retained
				// the same minimal way, mirrored for display only and never acted on.
				// Transfer is this host's own part of a master change or an eject, and
				// Authority is the master's own generation of the authority the members
				// replicate. Both are retained the same minimal way: mirrored for display,
				// never acted on, and re-reported by the hosts after any Controller loss.
				// Promotion is a candidate's own fence-verified promotion and Restricted is
				// this host's own refusal to act on pool authority. Both are retained the
				// same minimal way, and for a sharper reason: they are what an operator
				// reads when the pool master is gone, so a Controller that restarts must be
				// able to show them from the last report until the next one arrives.
				stored.Managed = &protocol.ManagedState{Membership: merged.Managed.Membership, Join: merged.Managed.Join, Replica: merged.Managed.Replica, Transfer: merged.Managed.Transfer, Authority: merged.Managed.Authority, Promotion: merged.Managed.Promotion, Restricted: merged.Managed.Restricted, ZFSProvisions: merged.Managed.ZFSProvisions}
			} else {
				stored.Managed = nil
			}
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
	// The hardware relay shares this channel, so either capability is reason to deliver it.
	if slices.Contains(v.Capabilities, protocol.UsageHistoryCapability) || slices.Contains(v.Capabilities, protocol.HostHardwareCapability) {
		out.StatsChannels = c.stats.pending(host)
	}
	rows, err := tx.Query("SELECT o.id,o.kind FROM operations o WHERE o.host_id=? AND o.status IN ('queued','dispatched') AND NOT (o.request_id='adopt-outcome:'||o.host_id||':'||o.id AND NOT EXISTS (SELECT 1 FROM managed_resources r WHERE r.operation_id=o.id)) ORDER BY o.rowid", host)
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
			if !slices.Contains(v.Capabilities, protocol.ManagedVMCapability) {
				continue
			}
			var raw string
			if tx.QueryRow("SELECT command FROM managed_resources WHERE operation_id=?", cmd.OperationID).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &cmd) != nil {
				fail(w, 500, "command payload unavailable")
				return
			}
		}
		// A pool-join whose invitation has been scrubbed is not a command any more: the host
		// already collected the one that carried the credential, and resending it emptied
		// would only be refused. Its own result settles the operation.
		if cmd.Kind == "pool-join" && cmd.Pool != nil && cmd.Pool.Invitation == "" {
			continue
		}
		if cmd.Power != nil && cmd.Power.AfterOperationID != "" && !slices.Contains(v.Capabilities, protocol.PowerQueueCapability) {
			continue
		}
		if cmd.Media != nil && cmd.Media.URL != "" && !slices.Contains(v.Capabilities, protocol.ISODownloadCapability) {
			continue
		}
		if storageRepositoryCommand(cmd) && (v.Version != protocol.Version || !slices.Contains(v.Capabilities, protocol.StorageRepositoryCapability)) {
			continue
		}
		filtered = append(filtered, cmd)
		if len(filtered) == 100 {
			break
		}
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

func storageRepositoryCommand(cmd protocol.Command) bool {
	if cmd.StorageLifecycle != nil {
		return true
	}
	if cmd.CredentialRetire != nil {
		return true
	}
	return cmd.Kind == "storage-register" && cmd.Storage != nil && cmd.Storage.StorageKind() != protocol.StorageKindLocal
}

func transactionIdentity(tx *sql.Tx, r *http.Request, host string) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	var n int
	return tx.QueryRow("SELECT count(*) FROM certificates JOIN hosts ON hosts.id=certificates.host_id WHERE serial=? AND certificates.host_id=? AND expires>? AND revoked=0", r.TLS.PeerCertificates[0].SerialNumber.String(), host, time.Now().Unix()).Scan(&n) == nil && n == 1
}
