// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestPowerRequestsAreAuthorizedDurableAndDispatched(t *testing.T) {
	a := newClient(t)
	site := a.site()
	host := a.enroll(a.token(site, ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	pool := a.poolOf(host.HostID)
	vm := "0f3f7a86-1d0e-4e3c-a1a4-7c5d3f1e2b91"
	body := map[string]string{"request_id": "power-1", "action": "start"}
	a.call("POST", "/api/v1/vms/"+vm+"/power", body, nil, 404)
	a.c.db.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", vm, pool, host.HostID, "controller", "adopt-2")
	a.call("POST", "/api/v1/vms/"+vm+"/power", map[string]string{"request_id": "power-1", "action": "explode"}, nil, 400)
	a.call("POST", "/api/v1/vms/"+vm+"/power", body, nil, 409) // never reported
	sequence := uint64(0)
	report := func(caps []string, results ...protocol.Result) protocol.ReportResponse {
		sequence++
		now := time.Now().UTC()
		inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 4, MemoryBytes: 8 << 30, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}, VMs: []protocol.VM{{ID: vm, Name: "controller", State: "shut off"}}}
		return a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: caps, Inventory: inv, Results: results})
	}
	// The host owns its default resources, so an enrolled host is handed no commands.
	if enrolled := report([]string{protocol.ManagedVMCapability}).Commands; len(enrolled) != 0 {
		t.Fatalf("enrollment commands %+v", enrolled)
	}
	if w := a.call("POST", "/api/v1/vms/"+vm+"/power", body, nil, 409); !strings.Contains(w.Body.String(), "does not support central power") {
		t.Fatal(w.Body.String())
	}
	caps := []string{protocol.ManagedVMCapability, protocol.PowerCapability}
	report(caps)
	var first map[string]any
	read(t, a.call("POST", "/api/v1/vms/"+vm+"/power", body, nil, 202), &first)
	op := first["operation"].(map[string]any)
	if first["vm_id"] != vm || first["action"] != "start" || op["kind"] != "vm-power" || op["status"] != "queued" {
		t.Fatalf("power response %v", first)
	}
	var replay map[string]any
	read(t, a.call("POST", "/api/v1/vms/"+vm+"/power", body, nil, 200), &replay)
	if replay["operation"].(map[string]any)["id"] != op["id"] {
		t.Fatal("replay created a second operation")
	}
	a.call("POST", "/api/v1/vms/"+vm+"/power", map[string]string{"request_id": "power-1", "action": "reboot"}, nil, 409)
	if w := a.call("POST", "/api/v1/vms/"+vm+"/power", map[string]string{"request_id": "power-2", "action": "reboot"}, nil, 409); !strings.Contains(w.Body.String(), "still pending") {
		t.Fatal(w.Body.String())
	}
	commands := report(caps).Commands
	if len(commands) != 1 || commands[0].Kind != "vm-power" || commands[0].Power == nil || commands[0].Power.VMID != vm || commands[0].Power.Action != "start" || commands[0].Power.HostID != host.HostID || commands[0].Power.PoolID != pool {
		t.Fatalf("dispatched %+v", commands)
	}
	report(caps, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	var ops []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	if ops[0].Kind != "vm-power" || ops[0].Status != "succeeded" || ops[0].HostID != host.HostID {
		t.Fatalf("operation %+v", ops[0])
	}
	read(t, a.call("POST", "/api/v1/vms/"+vm+"/power", map[string]string{"request_id": "power-2", "action": "force-stop"}, nil, 202), &first)
	var vms []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 {
		t.Fatalf("power rows leaked into the VM list: %d", len(vms))
	}
}

// A creation whose automatic start failed is still a succeeded creation: the host reports the
// failed start as a <operation>:start vm-power outcome, which lands in task history, while the
// VM itself is adopted as a normal managed VM that accepts a power request.
func TestPowerAcceptedAfterFailedAutomaticStart(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	guest := vmSpec("old-pool", "old-host", "guest1", "host-iso", "host-untagged", "")
	guest.ID, guest.StartAfterCreate = "12345678-1234-4234-8234-123456789012", true
	managed := hostDefaults(host.HostID)
	managed.VMs = []protocol.VMCreateSpec{guest}
	managed.Outcomes = []protocol.ManagedOutcome{{OperationID: "op-create:start", Kind: "vm-power", ID: guest.ID, Name: guest.Name, Status: "failed", Error: "VM created but not started: libvirt refused the start request; inspect the VM on its host"}}
	now := time.Now().UTC()
	inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, Managed: managed,
		NICs:   []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}},
		VMs:    []protocol.VM{{ID: guest.ID, Name: guest.Name, State: "shut off"}},
		Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
	caps := []string{protocol.ManagedVMCapability, protocol.PowerCapability}
	seq := uint64(0)
	report := func() protocol.ReportResponse {
		seq++
		return a.report(cert, protocol.ReportRequest{Version: 1, Sequence: seq, Capabilities: caps, Inventory: inv})
	}
	if cmds := report().Commands; len(cmds) != 0 {
		t.Fatalf("adoption dispatched commands: %+v", cmds)
	}
	var vms []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || vms[0].ID != guest.ID || vms[0].Status != "succeeded" || vms[0].State != "shut off" {
		t.Fatalf("VM with a failed start not adopted as a normal VM: %+v", vms)
	}
	var ops []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	found := false
	for _, op := range ops {
		if op.ID == "op-create:start" {
			found = op.Kind == "vm-power" && op.Status == "failed" && strings.HasPrefix(op.Error, "VM created but not started") && op.RequestID == "adopt-outcome:"+host.HostID+":op-create:start"
		}
	}
	if !found {
		t.Fatalf("failed start missing from task history: %+v", ops)
	}
	var first map[string]any
	read(t, a.call("POST", "/api/v1/vms/"+guest.ID+"/power", map[string]string{"request_id": "power-1", "action": "start"}, nil, 202), &first)
	cmds := report().Commands
	if len(cmds) != 1 || cmds[0].Kind != "vm-power" || cmds[0].Power == nil || cmds[0].Power.VMID != guest.ID || cmds[0].Power.Action != "start" {
		t.Fatalf("power on the VM was not dispatched: %+v", cmds)
	}
	// The outcome is recorded once, however many reports repeat it.
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	n := 0
	for _, op := range ops {
		if op.ID == "op-create:start" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("start outcome recorded %d times", n)
	}
}
