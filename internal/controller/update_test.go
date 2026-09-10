// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
)

// reportState is a host report with an observed VM state and the host's applied definitions.
func (l *managedLab) reportState(state string, managed *protocol.ManagedState, results ...protocol.Result) protocol.ReportResponse {
	l.t.Helper()
	l.seq++
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}}
	now := time.Now().UTC()
	defaults := hostDefaults(l.host.HostID)
	if managed != nil {
		defaults.VMs = managed.VMs
		for _, v := range managed.VMs {
			inv_vm := protocol.VM{ID: v.ID, Name: v.Name, State: state}
			defaults.MissingVMs = nil
			_ = inv_vm
		}
	}
	inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, NICs: nics, Managed: defaults, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
	for _, v := range defaults.VMs {
		inv.VMs = append(inv.VMs, protocol.VM{ID: v.ID, Name: v.Name, State: state})
	}
	return l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv, Results: results})
}

func (l *managedLab) update(request, id string, v protocol.VMCreateSpec, want int) map[string]any {
	l.t.Helper()
	var out map[string]any
	w := l.a.call("PUT", "/api/v1/vms/"+id, updateVMRequest{RequestID: request, VMCreateSpec: v}, nil, want)
	if want == 202 || want == 200 {
		read(l.t, w, &out)
	} else {
		out = map[string]any{"error": w.Body.String()}
	}
	return out
}

func TestUpdateVMValidatesFreezesMACsDispatchesAndRefreshes(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability)
	spec := l.spec("guest")
	var created createdVM
	read(t, l.create("create-1", spec, 202), &created)
	id := created.VMID
	commands := l.report().Commands
	if len(commands) != 1 || commands[0].Kind != "vm-create" {
		t.Fatalf("dispatch %+v", commands)
	}
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	next := spec
	next.Description = "edited"
	next.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio", MAC: protocol.DerivedMAC(id, 0)}, {NetworkID: l.network, Model: "e1000e"}}
	l.update("edit-0", "0f3f7a86-1d0e-4e3c-a1a4-7c5d3f1e2b91", next, 404)
	moved := next
	moved.PoolID = "other"
	if r := l.update("edit-0", id, moved, 400); !strings.Contains(r["error"].(string), "does not move") {
		t.Fatal(r)
	}
	started := next
	started.StartAfterCreate = true
	l.update("edit-0", id, started, 400)
	resized := next
	resized.Disks = []protocol.VMDisk{{StorageID: l.storage, Name: "system", SizeGiB: 40, Bus: "virtio", Thin: true, BootOrder: 1}}
	if r := l.update("edit-0", id, resized, 400); !strings.Contains(r["error"].(string), "cannot be changed after creation") {
		t.Fatal(r)
	}
	first := l.update("edit-1", id, next, 202)
	op := first["operation"].(map[string]any)
	if first["vm_id"] != id || op["kind"] != "vm-update" || op["status"] != "queued" {
		t.Fatalf("update response %v", first)
	}
	if replay := l.update("edit-1", id, next, 200); replay["operation"].(map[string]any)["id"] != op["id"] {
		t.Fatal("replay created a second operation")
	}
	if r := l.update("edit-2", id, next, 409); !strings.Contains(r["error"].(string), "still pending") {
		t.Fatal(r)
	}
	l.a.call("POST", "/api/v1/vms/"+id+"/power", map[string]string{"request_id": "power-1", "action": "start"}, nil, 409)
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || commands[0].Kind != "vm-update" || commands[0].VM == nil {
		t.Fatalf("dispatch %+v", commands)
	}
	sent := *commands[0].VM
	if sent.ID != id || sent.Revision != 1 || sent.Description != "edited" || len(sent.NICs) != 2 || sent.NICs[0].MAC != protocol.DerivedMAC(id, 0) || sent.NICs[1].MAC != protocol.DerivedMAC(id, 1) || sent.StartAfterCreate {
		t.Fatalf("dispatched definition %+v", sent)
	}
	// The host applies it and reports the new definition as current; the mirror follows.
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{sent}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	var vms []managedAppliance
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || vms[0].Spec == nil || vms[0].Spec.Revision != 1 || vms[0].Spec.Description != "edited" || len(vms[0].Spec.NICs) != 2 || vms[0].Spec.NICs[1].MAC != protocol.DerivedMAC(id, 1) {
		t.Fatalf("mirror after report %+v", vms)
	}
	// The next edit builds on revision 1. Uniqueness of the frozen addresses against other VMs
	// is the host's check, as for creation.
	again := sent
	again.Description = "third"
	again.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "e1000e", MAC: protocol.DerivedMAC(id, 1)}}
	if r := l.update("edit-3", id, again, 202); r["vm_id"] != id {
		t.Fatal(r)
	}
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{sent}}).Commands
	if len(commands) != 1 || commands[0].VM.Revision != 2 || len(commands[0].VM.NICs) != 1 {
		t.Fatalf("second edit %+v", commands)
	}
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{sent}}, protocol.Result{OperationID: commands[0].OperationID, Status: "failed", Error: "libvirt refused"})
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if vms[0].Spec.Revision != 1 || vms[0].Spec.Description != "edited" {
		t.Fatalf("a failed edit must not move the mirror: %+v", vms[0].Spec)
	}
	// Operations list carries the edits; the VM list does not grow.
	kinds := map[string]int{}
	for _, op := range l.operations() {
		kinds[op.Kind]++
	}
	if kinds["vm-update"] != 2 || len(vms) != 1 {
		t.Fatalf("operations %v vms %d", kinds, len(vms))
	}
}

func TestUpdateVMRunningStateAndGates(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability)
	spec := l.spec("guest")
	var created createdVM
	read(t, l.create("create-1", spec, 202), &created)
	id := created.VMID
	commands := l.report().Commands
	applied := *commands[0].VM
	state := &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}
	l.reportState("running", state, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	more := spec
	more.CPU.Count = 4
	if r := l.update("edit-cpu", id, more, 409); !strings.Contains(r["error"].(string), "stop the VM first to change the CPU configuration") {
		t.Fatal(r)
	}
	hot := spec
	hot.NICs = append(hot.NICs, protocol.VMNIC{NetworkID: l.network, Model: "virtio"})
	l.update("edit-nic", id, hot, 202)
	// A pending edit blocks a power request as well as another edit.
	if w := l.a.call("POST", "/api/v1/vms/"+id+"/power", map[string]string{"request_id": "power-1", "action": "shutdown"}, nil, 409); !strings.Contains(w.Body.String(), "pending") {
		t.Fatal(w.Body.String())
	}
	commands = l.reportState("running", state).Commands
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{*commands[0].VM}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	// Observed host capacity still bounds an edit.
	huge := *commands[0].VM
	huge.CPU.Count = 16
	if r := l.update("edit-huge", id, huge, 409); !strings.Contains(r["error"].(string), "exceeds observed host capacity") {
		t.Fatal(r)
	}
	// The appliance is never edited here, and a host without the capability is refused.
	l.a.c.db.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", "0f3f7a86-1d0e-4e3c-a1a4-7c5d3f1e2b91", l.pool, l.host.HostID, "controller", "adopt-2")
	if r := l.update("edit-appliance", "0f3f7a86-1d0e-4e3c-a1a4-7c5d3f1e2b91", spec, 409); !strings.Contains(r["error"].(string), "appliance") {
		t.Fatal(r)
	}
	l.caps = []string{protocol.ManagedVMCapability}
	l.reportState("shut off", state)
	if r := l.update("edit-old-core", id, spec, 409); !strings.Contains(r["error"].(string), "does not support editing") {
		t.Fatal(r)
	}
}
