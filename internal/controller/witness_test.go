// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"os"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestQnetdLiveReadiness(t *testing.T) {
	if os.Getenv("NEX_QNETD_LIVE_TEST") != "1" {
		t.Skip("set NEX_QNETD_LIVE_TEST=1 in an isolated qnetd runtime")
	}
	if _, _, _, err := qnetdCA(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := newClient(t)
	site := a.site()
	enrolled := a.enroll(a.token(site, ""), csr(t))
	cert := certificate(t, enrolled.CertificatePEM)
	cluster := protocol.PoolWitnessClusterName(enrolled.PoolID)
	membership := protocol.PoolMembership{
		PoolID: enrolled.PoolID, MasterHostID: enrolled.HostID, Generation: 2,
		Members: []protocol.PoolMember{{HostID: enrolled.HostID, Hostname: enrolled.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 2, Status: "ready"}},
	}
	a.report(cert, protocol.ReportRequest{
		Version: protocol.Version, Sequence: 1,
		Capabilities: []string{protocol.ManagedVMCapability},
		Inventory:    &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership}},
	})
	var candidates protocol.PoolWitnessCandidates
	w := a.call("GET", "/api/v1/agent/witness/candidates", nil, cert, 200)
	read(t, w, &candidates)
	if len(candidates.Candidates) != 1 || candidates.Candidates[0].Kind != "controller" {
		t.Fatalf("live witness candidates: %+v", candidates)
	}
	csrPEM := witnessCSR(t, cluster)
	register := func() protocol.PoolWitnessRegistration {
		t.Helper()
		var result protocol.PoolWitnessRegistration
		w := a.call("POST", "/api/v1/agent/witness/register", protocol.PoolWitnessRegistrationRequest{
			PoolID: enrolled.PoolID, ClusterName: cluster, FencingGeneration: 2, CSRPEM: csrPEM,
		}, cert, 200)
		if w.Code != 200 {
			t.Fatalf("live witness registration: code=%d body=%s", w.Code, w.Body.String())
		}
		read(t, w, &result)
		return result
	}
	first := register()
	second := register()
	if first.Address != "" || second.Address != "" || first.CAPEM == "" || first.CertificatePEM == "" || first.CASHA256 == "" || second.CASHA256 != first.CASHA256 {
		t.Fatalf("live witness registration returned invalid Controller candidate: first=%+v second=%+v", first, second)
	}
}

func TestWitnessEndpointsAuthenticateMasterAndCheckGeneration(t *testing.T) {
	a := newClient(t)
	site := a.site()
	enrolled := a.enroll(a.token(site, ""), csr(t))
	cert := certificate(t, enrolled.CertificatePEM)
	cluster := protocol.PoolWitnessClusterName(enrolled.PoolID)
	a.call("GET", "/api/v1/agent/witness/candidates", nil, nil, 401)

	membership := protocol.PoolMembership{
		PoolID: enrolled.PoolID, MasterHostID: enrolled.HostID, Generation: 2,
		Members: []protocol.PoolMember{{HostID: enrolled.HostID, Hostname: enrolled.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 2, Status: "ready"}},
	}
	a.report(cert, protocol.ReportRequest{
		Version: protocol.Version, Sequence: 1,
		Capabilities: []string{protocol.ManagedVMCapability},
		Inventory:    &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership}},
	})
	csrPEM := witnessCSR(t, cluster)
	register := func(generation uint64, csr string, want int) int {
		w := a.call("POST", "/api/v1/agent/witness/register", protocol.PoolWitnessRegistrationRequest{
			PoolID: enrolled.PoolID, ClusterName: cluster, FencingGeneration: generation, CSRPEM: csr,
		}, cert, want)
		return w.Code
	}
	if got := register(1, csrPEM, 409); got != 409 {
		t.Fatalf("stale fencing generation returned %d", got)
	}
	if register(2, witnessCSR(t, "wrong-cluster"), 400) != 400 {
		t.Fatal("CSR with a different cluster name was accepted")
	}
}

func TestSameSiteWitnessCandidatesExcludeRequestingPool(t *testing.T) {
	a := newClient(t)
	site := a.site()
	local := a.enroll(a.token(site, ""), csr(t))
	remotePool, remoteHost := "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003"
	inventory, err := json.Marshal(protocol.Inventory{Managed: &protocol.ManagedState{PeerOrigin: "https://192.0.2.44:9444"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.c.db.Exec("INSERT INTO pools(id,site_id,name,master_host_id) VALUES(?,?,?,?)", remotePool, site, "remote", remoteHost); err != nil {
		t.Fatal(err)
	}
	if _, err = a.c.db.Exec("INSERT INTO hosts(id,pool_id,hostname,inventory) VALUES(?,?,?,?)", remoteHost, remotePool, "remote-core", string(inventory)); err != nil {
		t.Fatal(err)
	}
	tx, err := a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	got, err := sameSitePoolWitnessCandidates(tx, site, local.PoolID)
	_ = tx.Rollback()
	if err != nil || len(got) != 1 || got[0].Kind != "pool-master" || got[0].PoolID != remotePool || got[0].HostID != remoteHost || got[0].Address != "192.0.2.44:5403" || got[0].CAPEM != "" || got[0].CASHA256 != "" {
		t.Fatalf("same-site witness candidates: %+v, %v", got, err)
	}
}

func TestWitnessClusterNameConflict(t *testing.T) {
	a := newClient(t)
	site := a.site()
	first := a.enroll(a.token(site, ""), csr(t))
	second := a.enroll(a.token(site, ""), csr(t))
	tx, err := a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	name := protocol.PoolWitnessClusterName(first.PoolID)
	if conflict, err := witnessClusterNameConflict(tx, first.PoolID, name); err != nil || conflict {
		t.Fatalf("own cluster name conflicts: conflict=%t err=%v", conflict, err)
	}
	if conflict, err := witnessClusterNameConflict(tx, second.PoolID, name); err != nil || !conflict {
		t.Fatalf("other pool's cluster name accepted: conflict=%t err=%v", conflict, err)
	}
}

func witnessCSR(t *testing.T, commonName string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}
