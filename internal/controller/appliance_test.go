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
	body := map[string]string{"request_id": "bootstrap-1", "site_name": "Lab", "enrollment_token": strings.Repeat("a", 64)}
	cookie, csrf := a.cookie, a.csrf
	a.cookie = nil
	a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 401)
	a.cookie = cookie
	a.csrf = ""
	a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 403)
	a.csrf = csrf
	var bootstrap struct {
		SiteID string `json:"site_id"`
	}
	read(t, a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 201), &bootstrap)
	var retry map[string]any
	read(t, a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 200), &retry)
	if retry["site_id"] != bootstrap.SiteID {
		t.Fatal("bootstrap duplicated site")
	}
	if _, ok := retry["pool_id"]; ok {
		t.Fatal("bootstrap returned a pool before enrollment")
	}
	body["site_name"] = "Different"
	a.call("POST", "/api/v1/appliances/bootstrap", body, nil, 409)
	body["site_name"] = "Lab"
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
	pool := a.poolOf(enrollment.HostID)
	var pools []protocol.Pool
	read(t, a.call("GET", "/api/v1/pools", nil, nil, 200), &pools)
	if len(pools) != 1 || pools[0].ID != pool || pools[0].SiteID != bootstrap.SiteID || pools[0].MasterHostID != enrollment.HostID {
		t.Fatalf("appliance host is not the master of an automatic pool: %+v", pools)
	}
	// pool_id is optional: adoption derives the pool from the host record.
	adopt := applianceRequest{RequestID: "adopt-1", HostID: enrollment.HostID, VMID: uuid, Name: "Nex Controller"}
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
	invalid.PoolID = "other-pool"
	a.call("POST", "/api/v1/appliances/adopt", invalid, nil, 409)
	var adopted map[string]string
	read(t, a.call("POST", "/api/v1/appliances/adopt", adopt, nil, 201), &adopted)
	if adopted["pool_id"] != pool {
		t.Fatalf("adoption did not derive the host pool: %v", adopted)
	}
	a.call("POST", "/api/v1/appliances/adopt", adopt, nil, 200)
	adopt.PoolID = pool
	a.call("POST", "/api/v1/appliances/adopt", adopt, nil, 200)
	invalid = adopt
	invalid.RequestID = "different"
	a.call("POST", "/api/v1/appliances/adopt", invalid, nil, 409)
	var vms []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || vms[0].State != "running" || vms[0].PoolID != pool || !vms[0].Protected {
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
	site := a.site()
	first := a.enroll(a.token(site, ""), csr(t))
	second := a.enroll(a.token(site, ""), csr(t))
	pool := a.poolOf(first.HostID)
	now := time.Now().UTC()
	const uuid = "96aaf605-65d7-4302-9192-528511134330"
	inv := &protocol.Inventory{ObservedAt: now, VMs: []protocol.VM{{ID: uuid, Name: "Controller", State: "running"}}, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
	a.report(certificate(t, first.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Inventory: inv})
	a.report(certificate(t, second.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Inventory: inv})
	request := applianceRequest{RequestID: "adopt", VMID: uuid, Name: "Controller", PoolID: pool, HostID: first.HostID}
	a.call("POST", "/api/v1/appliances/adopt", request, nil, 409)
	a.call("POST", "/api/v1/hosts/"+second.HostID+"/revoke", nil, nil, 200)
	a.call("POST", "/api/v1/appliances/adopt", request, nil, 409)
	second = a.enroll(a.token(site, second.HostID), csr(t))
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
