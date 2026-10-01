// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"fmt"
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
	managed.Outcomes = []protocol.ManagedOutcome{{OperationID: "op-create:start", Kind: "vm-power", ID: guest.ID, Name: guest.Name, Status: "failed", Error: "VM created but not started: libvirt did not confirm the start request; inspect the VM on its host"}}
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

func TestPowerQueuePreservesOrderDependenciesAndReplay(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.PowerQueueCapability, protocol.UpdateCapability)
	var created createdVM
	read(t, l.create("create-queue", l.spec("Queued guest"), 202), &created)
	create := l.report().Commands[0]
	applied := *create.VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: create.OperationID, Status: "succeeded"})
	next := applied
	next.Description = "before start"
	edit := l.update("edit-queue", applied.ID, next, 202)
	editID := edit["operation"].(map[string]any)["id"].(string)
	body := map[string]string{"request_id": "queued-start", "action": "start"}
	var power map[string]any
	read(t, l.a.call("POST", "/api/v1/vms/"+applied.ID+"/power", body, nil, 202), &power)
	var replay map[string]any
	read(t, l.a.call("POST", "/api/v1/vms/"+applied.ID+"/power", body, nil, 200), &replay)
	powerID := power["operation"].(map[string]any)["id"].(string)
	if replay["operation"].(map[string]any)["id"] != powerID {
		t.Fatal("replayed queue request created duplicate work")
	}
	// Wall-clock changes cannot reorder admission. Both tasks are delivered in one report.
	if _, err := l.a.c.db.Exec("UPDATE operations SET created_at='2000-01-01T00:00:00Z' WHERE id=?", powerID); err != nil {
		t.Fatal(err)
	}
	cmds := l.report().Commands
	if len(cmds) != 2 || cmds[0].OperationID != editID || cmds[1].OperationID != powerID || cmds[1].Power.AfterOperationID != editID {
		t.Fatalf("queue order/dependency: %+v", cmds)
	}
	// An older Core must never receive a queued command whose prerequisite it ignores.
	l.caps = []string{protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability}
	cmds = l.report().Commands
	if len(cmds) != 1 || cmds[0].OperationID != editID {
		t.Fatalf("downgrade dispatch: %+v", cmds)
	}
}

func TestHostQueueAdoptedWithoutRedispatch(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	inv := &protocol.Inventory{Hostname: "core", CPUCount: 4, MemoryBytes: 8 << 30, Managed: hostDefaults(host.HostID)}
	inv.Managed.Outcomes = []protocol.ManagedOutcome{{OperationID: "host-queued-power", Kind: "vm-power", ID: "guest", Status: "queued"}}
	for i, status := range []string{"queued", "queued", "succeeded", "queued"} {
		inv.Managed.Outcomes[0].Status = status
		r := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: uint64(i + 1), Capabilities: []string{protocol.ManagedVMCapability, protocol.PowerQueueCapability}, Inventory: inv})
		if len(r.Commands) != 0 {
			t.Fatalf("adoption redispatched host queue: %+v", r.Commands)
		}
		var ops []protocol.Operation
		read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
		want := "dispatched"
		if i >= 2 {
			want = "succeeded"
		}
		if len(ops) != 3 || ops[0].ID != "host-queued-power" || ops[0].Status != want {
			t.Fatalf("adopted task: %+v", ops)
		}
	}
}

func TestPowerQueueDowngradeDoesNotStarveOtherVM(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.PowerQueueCapability)
	vm := "12345678-1234-4234-8234-123456789012"
	if _, err := l.a.c.db.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", vm, l.pool, l.host.HostID, "guest", "adopt"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		l.a.call("POST", "/api/v1/vms/"+vm+"/power", map[string]string{"request_id": fmt.Sprintf("power-%d", i), "action": "start"}, nil, 202)
	}
	other := "12345678-1234-4234-8234-123456789013"
	if _, err := l.a.c.db.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", other, l.pool, l.host.HostID, "other", "adopt-other"); err != nil {
		t.Fatal(err)
	}
	l.a.call("POST", "/api/v1/vms/"+other+"/power", map[string]string{"request_id": "other-vm", "action": "start"}, nil, 202)
	l.caps = []string{protocol.ManagedVMCapability, protocol.PowerCapability}
	commands := l.report().Commands
	if len(commands) != 2 || commands[0].Kind != "vm-power" || commands[1].Power == nil || commands[1].Power.VMID != other {
		t.Fatalf("compatible commands starved: %+v", commands)
	}
}

func TestHostQueueLargeOutcomeHistory(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	inv := &protocol.Inventory{Managed: &protocol.ManagedState{}}
	// More than 16383 outcomes exceeded SQLite's two-placeholder lookup limit.
	for i := 0; i < 16384; i++ {
		inv.Managed.Outcomes = append(inv.Managed.Outcomes, protocol.ManagedOutcome{OperationID: fmt.Sprintf("completed-%d", i), Kind: "vm-power", ID: "guest", Status: "succeeded"})
	}
	for seq := uint64(1); seq <= 2; seq++ {
		r := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability, protocol.PowerQueueCapability}, Inventory: inv})
		if len(r.Commands) != 0 {
			t.Fatal("history was redispatched")
		}
	}
	var count int
	if err := a.c.db.QueryRow("SELECT count(*) FROM operations WHERE host_id=?", host.HostID).Scan(&count); err != nil || count != 16384 {
		t.Fatalf("history count=%d err=%v", count, err)
	}
	inv.Managed.Outcomes = make([]protocol.ManagedOutcome, 30000)
	if err := validateInventory(inv); err != nil {
		t.Fatal(err)
	}
	inv.Managed.Outcomes = append(inv.Managed.Outcomes, protocol.ManagedOutcome{})
	if err := validateInventory(inv); err == nil {
		t.Fatal("unbounded outcome inventory accepted")
	}
}
