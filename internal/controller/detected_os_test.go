// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestDetectedOSReAdoptedFromCoreWithoutLiveGuest(t *testing.T) {
	const id = "12345678-1234-4234-8234-123456789012"
	detected := protocol.DetectedOS{OS: "Debian GNU/Linux 13", OSID: "debian", ObservedAt: time.Now().UTC().Add(-time.Hour)}
	// Each Controller starts with an empty database and only receives Core's durable report.
	for i := 0; i < 2; i++ {
		a := newClient(t)
		host := a.enroll(a.token(a.site(), ""), csr(t))
		cert := certificate(t, host.CertificatePEM)
		pool := a.poolOf(host.HostID)
		managed := hostDefaults(host.HostID)
		managed.Membership = selfMembership(host.HostID, pool)
		guest := vmSpec(pool, host.HostID, "guest", "host-iso", "host-untagged", "")
		guest.ID = id
		managed.VMs = []protocol.VMCreateSpec{guest}
		managed.DetectedOS = map[string]protocol.DetectedOS{id: detected}
		now := time.Now().UTC()
		inv := &protocol.Inventory{ObservedAt: now, Managed: managed, VMs: []protocol.VM{{ID: id, Name: "guest", State: "shut off"}}, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
		report := func(seq uint64, want int) {
			a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: protocol.Version, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv}, cert, want)
		}
		report(1, 200)
		check := func(want bool) {
			t.Helper()
			var vms []managedAppliance
			read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
			if len(vms) != 1 {
				t.Fatalf("VM was not adopted: %+v", vms)
			}
			if got := vms[0].DetectedOS; (got != nil) != want || (want && *got != detected) {
				t.Fatalf("detected OS = %+v, want record %v", got, want)
			}
			if vms[0].Guest != nil {
				t.Fatal("historical OS became live guest information")
			}
		}
		check(true)
		a.c.mu.Lock()
		a.c.seen[host.HostID] = time.Now().Add(-2 * a.c.cfg.StaleAfter)
		a.c.mu.Unlock()
		check(true)
		cookie := a.cookie
		a.cookie = nil
		a.call("GET", "/api/v1/vms", nil, nil, 401)
		a.cookie = cookie
		// A Core that omits the record, or an explicitly cleared record, does not retain a Controller copy.
		managed.DetectedOS = nil
		report(2, 200)
		check(false)
		managed.DetectedOS = map[string]protocol.DetectedOS{id: detected}
		report(3, 200)
		check(true)
		// A pool membership mismatch must not lend another pool's historical identity.
		managed.Membership = selfMembership(host.HostID, "another-pool")
		raw, _ := json.Marshal(inv)
		if _, err := a.c.db.Exec("UPDATE hosts SET inventory=? WHERE id=?", string(raw), host.HostID); err != nil {
			t.Fatal(err)
		}
		check(false)
		managed.Membership = nil
		report(4, 200)
		check(false)
		managed.Membership = selfMembership(host.HostID, pool)
		report(5, 200)
		check(true)
		a.call("POST", "/api/v1/hosts/"+host.HostID+"/revoke", nil, nil, 200)
		check(false)
	}
}

func TestDetectedOSInventoryValidation(t *testing.T) {
	const id = "12345678-1234-4234-8234-123456789012"
	valid := protocol.DetectedOS{OS: "Debian GNU/Linux", OSID: "debian", ObservedAt: time.Now().UTC()}
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	for _, bad := range []protocol.DetectedOS{
		{OS: "", ObservedAt: valid.ObservedAt},
		{OS: "   ", ObservedAt: valid.ObservedAt},
		{OS: strings.Repeat("x", 4097), ObservedAt: valid.ObservedAt},
		{OS: valid.OS, OSID: strings.Repeat("x", 201), ObservedAt: valid.ObservedAt},
		{OS: valid.OS},
	} {
		a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{DetectedOS: map[string]protocol.DetectedOS{id: bad}}}}, cert, 400)
	}
	inv := &protocol.Inventory{Managed: &protocol.ManagedState{DetectedOS: map[string]protocol.DetectedOS{"not-a-vm": valid}}}
	if validateInventory(inv) == nil {
		t.Fatal("invalid VM identity accepted")
	}
	inv.Managed.DetectedOS = map[string]protocol.DetectedOS{id: valid}
	if err := validateInventory(inv); err != nil {
		t.Fatal(err)
	}
}

func TestDetectedOSCapabilityAcknowledgement(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	for i, test := range []struct {
		capabilities []string
		want         bool
	}{
		{nil, false},
		{[]string{protocol.DetectedOSCapability}, true},
	} {
		response := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: uint64(i + 1), Capabilities: test.capabilities})
		if (response.DetectedOS != nil) != test.want || (test.want && !*response.DetectedOS) {
			t.Fatalf("unexpected capability acknowledgement: %+v", response.DetectedOS)
		}
	}
}

func TestDetectedOSMemberCannotReplaceMasterRecord(t *testing.T) {
	const id = "12345678-1234-4234-8234-123456789012"
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	detected := protocol.DetectedOS{OS: "Debian GNU/Linux", OSID: "debian", ObservedAt: time.Now().UTC()}
	state := &protocol.ManagedState{Membership: selfMembership(host.HostID, pool), DetectedOS: map[string]protocol.DetectedOS{id: detected}}
	a.report(certificate(t, host.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Inventory: &protocol.Inventory{Managed: state}})
	spoof := detected
	spoof.OS = "Member overwrite attempt"
	raw, _ := json.Marshal(protocol.Inventory{Managed: &protocol.ManagedState{DetectedOS: map[string]protocol.DetectedOS{id: spoof}}})
	if _, err := a.c.db.Exec("INSERT INTO hosts(id,pool_id,hostname,inventory) VALUES(?,?,?,?)", "member-core", pool, "member-core", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.c.db.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", id, pool, "member-core", "Controller", "deploy-member"); err != nil {
		t.Fatal(err)
	}
	var vms []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || vms[0].DetectedOS == nil || *vms[0].DetectedOS != detected {
		t.Fatalf("master OS record not used: %+v", vms)
	}
	a.call("POST", "/api/v1/hosts/"+host.HostID+"/revoke", nil, nil, 200)
	vms = nil
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if vms[0].DetectedOS != nil {
		t.Fatal("member supplied historical OS after master revocation")
	}
}
