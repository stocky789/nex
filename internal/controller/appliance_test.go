// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestApplianceBootstrapAdoptionAndOutage(t *testing.T) {
	a := newClient(t)
	body := map[string]string{"request_id": "bootstrap-1", "site_name": "Lab", "pool_name": "Pool", "enrollment_token": strings.Repeat("a", 64)}
	cookie, csrf := a.cookie, a.csrf
	a.cookie = nil
	a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 401)
	a.cookie = cookie
	a.csrf = ""
	a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 403)
	a.csrf = csrf
	var bootstrap struct {
		SiteID string `json:"site_id"`
		PoolID string `json:"pool_id"`
	}
	read(t, a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 201), &bootstrap)
	var retry map[string]any
	read(t, a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 200), &retry)
	if retry["pool_id"] != bootstrap.PoolID {
		t.Fatal("bootstrap duplicated pool")
	}
	body["pool_name"] = "Different"
	a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 409)
	body["pool_name"] = "Pool"
	body["request_id"] = "bootstrap-2"
	a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 409)
	body["request_id"] = "bootstrap-1"
	key := csr(t)
	enrollment := a.enroll(body["enrollment_token"], key)
	cert := certificate(t, enrollment.CertificatePEM)
	// Lost enrollment response after expiry recovers the original host using the same CSR.
	if _, err := a.c.db.Exec("UPDATE tokens SET expires=0"); err != nil {
		t.Fatal(err)
	}
	a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 200)
	if got := a.enroll(body["enrollment_token"], key); got.HostID != enrollment.HostID {
		t.Fatal("retry duplicated host")
	}
	const uuid = "96aaf605-65d7-4302-9192-528511134330"
	adopt := applianceRequest{RequestID: "adopt-1", PoolID: bootstrap.PoolID, HostID: enrollment.HostID, VMID: uuid, Name: "Nex Controller"}
	a.cookie = nil
	a.call("POST", "/api/v1/appliances/adopt", adopt, nil, 401)
	a.call("GET", "/api/v1/vms", nil, nil, 401)
	a.cookie = cookie
	a.csrf = ""
	a.call("POST", "/api/v1/appliances/adopt", adopt, nil, 403)
	a.csrf = csrf
	invalid := adopt
	invalid.VMID = "../../disk"
	a.call("POST", "/api/v1/appliances/adopt", invalid, nil, 400)
	invalid.VMID = strings.ToUpper(uuid)
	a.call("POST", "/api/v1/appliances/adopt", invalid, nil, 400)
	a.call("POST", "/api/v1/appliances/adopt", adopt, nil, 409)
	now := time.Now().UTC()
	inv := &protocol.Inventory{ObservedAt: now, VMs: []protocol.VM{{ID: uuid, Name: adopt.Name, State: "running"}}, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Inventory: inv})
	invalid = adopt
	invalid.PoolID = a.pool()
	a.call("POST", "/api/v1/appliances/adopt", invalid, nil, 409)
	a.call("POST", "/api/v1/appliances/adopt", adopt, nil, 201)
	a.call("POST", "/api/v1/appliances/adopt", adopt, nil, 200)
	invalid = adopt
	invalid.RequestID = "different"
	a.call("POST", "/api/v1/appliances/adopt", invalid, nil, 409)
	var vms []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || vms[0].State != "running" || vms[0].PoolID != bootstrap.PoolID || !vms[0].Protected {
		t.Fatalf("unexpected VM %#v", vms)
	}
	a.c.mu.Lock()
	a.c.seen[enrollment.HostID] = time.Now().Add(-time.Hour)
	a.c.mu.Unlock()
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if vms[0].State != "unknown" || vms[0].Freshness != "stale" {
		t.Fatal("outage falsely preserved current running state")
	}
	dataDir := a.c.cfg.DataDir
	if err := a.c.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	a.c, err = Open(Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if vms[0].State != "unknown" || vms[0].Freshness != "unknown" {
		t.Fatal("restart falsely trusted persisted state")
	}
	a.call("POST", "/api/v1/hosts/"+enrollment.HostID+"/revoke", nil, nil, 200)
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if vms[0].State != "unknown" || vms[0].Freshness != "revoked" {
		t.Fatal("revocation falsely trusted state")
	}
	a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 409)
}

func TestApplianceConflictingObservationAndFailedProbe(t *testing.T) {
	a := newClient(t)
	pool := a.pool()
	first := a.enroll(a.token(pool, ""), csr(t))
	second := a.enroll(a.token(pool, ""), csr(t))
	now := time.Now().UTC()
	const uuid = "96aaf605-65d7-4302-9192-528511134330"
	inv := &protocol.Inventory{ObservedAt: now, VMs: []protocol.VM{{ID: uuid, Name: "Controller", State: "running"}}, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
	a.report(certificate(t, first.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Inventory: inv})
	a.report(certificate(t, second.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Inventory: inv})
	request := applianceRequest{RequestID: "adopt", VMID: uuid, Name: "Controller", PoolID: pool, HostID: first.HostID}
	a.call("POST", "/api/v1/appliances/adopt", request, nil, 409)
	a.call("POST", "/api/v1/hosts/"+second.HostID+"/revoke", nil, nil, 200)
	a.call("POST", "/api/v1/appliances/adopt", request, nil, 409)
	second = a.enroll(a.token(pool, second.HostID), csr(t))
	inv.VMs = nil
	inv.Checks[0].ObservedAt = now.Add(time.Second)
	a.report(certificate(t, second.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 2, Inventory: inv})
	inv.VMs = []protocol.VM{{ID: uuid, Name: "Controller", State: "running"}}
	inv.Checks[0].Status = "failed"
	a.report(certificate(t, first.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 2, Inventory: inv})
	a.call("POST", "/api/v1/appliances/adopt", request, nil, 409)
	inv.Checks[0].Status = "healthy"
	inv.Checks[0].ObservedAt = now.Add(2 * time.Second)
	a.report(certificate(t, first.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 3, Inventory: inv})
	a.call("POST", "/api/v1/appliances/adopt", request, nil, 201)
}
