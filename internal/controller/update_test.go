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
		defaults.DeletedVMs = managed.DeletedVMs
		defaults.MissingVMs = managed.MissingVMs
	}
	inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 8, CPUModels: []string{"Skylake-Server"}, MemoryBytes: 16 << 30, NICs: nics, Managed: defaults, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
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
	spec := l.spec(" QA Web.example – 東京 ")
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
	next.Name = "--QA Web.example & 東京 (renamed)"
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
	unknownCPU := next
	unknownCPU.CPU.Model = "EPYC-Genoa"
	l.update("edit-0", id, unknownCPU, 409)
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
	if sent.ID != id || sent.Name != next.Name || sent.Revision != 1 || sent.Description != "edited" || len(sent.NICs) != 2 || sent.NICs[0].MAC != protocol.DerivedMAC(id, 0) || sent.NICs[1].MAC != protocol.DerivedMAC(id, 1) || sent.StartAfterCreate {
		t.Fatalf("dispatched definition %+v", sent)
	}
	// The host applies it and reports the new definition as current; the mirror follows.
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{sent}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	var vms []managedAppliance
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || vms[0].Spec == nil || vms[0].Name != next.Name || vms[0].Spec.Name != next.Name || vms[0].Spec.Revision != 1 || vms[0].Spec.Description != "edited" || len(vms[0].Spec.NICs) != 2 || vms[0].Spec.NICs[1].MAC != protocol.DerivedMAC(id, 1) {
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

// Removing an earlier interface must not let an unnamed kept one inherit the removed one's
// position-derived address: Core matches by MAC and would detach the interface the caller kept.
func TestUpdateVMRemovingInterfaceRequiresNamedMACs(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability)
	spec := l.spec("two-nics")
	spec.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio"}, {NetworkID: l.network, Model: "e1000e"}}
	var created createdVM
	read(t, l.create("create-1", spec, 202), &created)
	id := created.VMID
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	keepSecond := spec
	keepSecond.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "e1000e"}}
	if r := l.update("edit-drop", id, keepSecond, 400); !strings.Contains(r["error"].(string), "MAC") {
		t.Fatal(r)
	}
	swap := spec
	swap.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "e1000e"}, {NetworkID: l.network, Model: "rtl8139"}}
	if r := l.update("edit-swap", id, swap, 400); !strings.Contains(r["error"].(string), "MAC") {
		t.Fatal(r)
	}
	keepSecond.NICs[0].MAC = protocol.DerivedMAC(id, 1)
	l.update("edit-drop-named", id, keepSecond, 202)
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || len(commands[0].VM.NICs) != 1 || commands[0].VM.NICs[0].MAC != protocol.DerivedMAC(id, 1) {
		t.Fatalf("dispatch %+v", commands)
	}
}

// TestUpdateVMEditorRoundTripOnNeverEditedVM sends the edit the bundled editor builds from the VM
// listing on a VM created with automatic MACs and never edited: removing the first interface, and
// changing an existing interface's model, must be accepted and keep each interface's address.
func TestUpdateVMEditorRoundTripOnNeverEditedVM(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability)
	spec := l.spec("fresh-nics")
	spec.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio"}, {NetworkID: l.network, Model: "e1000e"}}
	var created createdVM
	read(t, l.create("create-1", spec, 202), &created)
	id := created.VMID
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	listed := func() protocol.VMCreateSpec {
		var vms []managedAppliance
		read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
		for _, v := range vms {
			if v.ID == id && v.Spec != nil {
				return *v.Spec
			}
		}
		t.Fatalf("VM %s not listed: %+v", id, vms)
		return protocol.VMCreateSpec{}
	}
	first, second := protocol.DerivedMAC(id, 0), protocol.DerivedMAC(id, 1)
	editor := listed()
	if len(editor.NICs) != 2 || editor.NICs[0].MAC != first || editor.NICs[1].MAC != second {
		t.Fatalf("listing must report effective MACs: %+v", editor.NICs)
	}
	// The editor copies the listed definition and sends it back without the first interface.
	drop := editor
	drop.NICs = editor.NICs[1:]
	l.update("editor-drop-first", id, drop, 202)
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || len(commands[0].VM.NICs) != 1 || commands[0].VM.NICs[0].MAC != second {
		t.Fatalf("dispatch %+v", commands)
	}
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{*commands[0].VM}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	// A second never-edited VM: change the first interface's adapter model only.
	spec = l.spec("fresh-model")
	spec.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio"}, {NetworkID: l.network, Model: "e1000e"}}
	read(t, l.create("create-2", spec, 202), &created)
	id, dropped := created.VMID, *commands[0].VM
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{dropped}}).Commands
	fresh := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{dropped, fresh}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	editor = listed()
	model := editor
	model.NICs = append([]protocol.VMNIC{}, editor.NICs...)
	model.NICs[0].Model = "e1000e"
	l.update("editor-model", id, model, 202)
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{dropped, fresh}}).Commands
	if len(commands) != 1 || len(commands[0].VM.NICs) != 2 || commands[0].VM.NICs[0].MAC != protocol.DerivedMAC(id, 0) || commands[0].VM.NICs[0].Model != "e1000e" || commands[0].VM.NICs[1].MAC != protocol.DerivedMAC(id, 1) {
		t.Fatalf("dispatch %+v", commands)
	}
	if stops, err := protocol.NeedsStop(fresh, *commands[0].VM); err != nil || len(stops) != 1 {
		t.Fatalf("only the changed interface needs a stop: %v %v", stops, err)
	}
}

// Gate4 review: when every current interface is named by MAC, a MAC-less interface is new. It
// must not trip the positional guard, and it is frozen before the stop check to an address no
// current interface holds, so it never inherits the MAC of an interface removed in the same edit.
func TestUpdateVMRemovePlusAddGetsFreshMAC(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability)
	spec := l.spec("swap-nics")
	spec.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio"}, {NetworkID: l.network, Model: "e1000e"}}
	var created createdVM
	read(t, l.create("create-swap", spec, 202), &created)
	id := created.VMID
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("running", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	d1, d2, d3 := protocol.DerivedMAC(id, 1), protocol.DerivedMAC(id, 2), protocol.DerivedMAC(id, 3)
	edit := applied
	edit.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "e1000e", MAC: d1}, {NetworkID: l.network, Model: "e1000e"}}
	// Running: removing d0 and adding a like interface is a live add, not a reconfigured d0.
	l.update("swap-running", id, edit, 202)
	commands = l.reportState("running", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || len(commands[0].VM.NICs) != 2 || commands[0].VM.NICs[0].MAC != d1 || commands[0].VM.NICs[1].MAC != d2 {
		t.Fatalf("new interface must get a fresh MAC: %+v", commands)
	}
	if stops, err := protocol.NeedsStop(applied, *commands[0].VM); err != nil || len(stops) != 0 {
		t.Fatalf("Core would refuse what the Controller accepted: %v %v", stops, err)
	}
	applied = *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	// Remove the first interface and add one of a different model in the same save. The new one
	// is drawn above the highest index in use, not the d0 removed in the earlier running save.
	edit = applied
	edit.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "e1000e", MAC: d2}, {NetworkID: l.network, Model: "rtl8139"}}
	l.update("swap-model", id, edit, 202)
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || len(commands[0].VM.NICs) != 2 || commands[0].VM.NICs[0].MAC != d2 || commands[0].VM.NICs[1].MAC != d3 || commands[0].VM.NICs[1].Model != "rtl8139" {
		t.Fatalf("dispatch %+v", commands)
	}
}

// Phase 2 gate open defect 1: an interface removed from a running VM in one save can stay live
// until the guest acknowledges the unplug, and the Controller cannot see that. A new automatic
// interface in a later save is drawn above the highest derived address the current definition
// uses, so it does not recycle the one just removed; Core refuses any address still live.
func TestUpdateVMAddAfterRunningRemovalSkipsRemovedMAC(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability)
	spec := l.spec("pending-unplug")
	spec.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio"}, {NetworkID: l.network, Model: "virtio"}}
	var created createdVM
	read(t, l.create("create-pending", spec, 202), &created)
	id := created.VMID
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("running", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	d0, d1, d2 := protocol.DerivedMAC(id, 0), protocol.DerivedMAC(id, 1), protocol.DerivedMAC(id, 2)
	// Save 1 removes the first interface while the guest runs.
	edit := applied
	edit.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio", MAC: d1}}
	l.update("pending-drop", id, edit, 202)
	commands = l.reportState("running", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	applied = *commands[0].VM
	l.reportState("running", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	// Save 2 adds an interface: it must not take d0, which the guest may still hold.
	edit = applied
	edit.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio", MAC: d1}, {NetworkID: l.network, Model: "virtio"}}
	l.update("pending-add", id, edit, 202)
	commands = l.reportState("running", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || len(commands[0].VM.NICs) != 2 || commands[0].VM.NICs[1].MAC == d0 || commands[0].VM.NICs[1].MAC != d2 {
		t.Fatalf("new interface recycled a removed MAC: %+v", commands)
	}
}

// Review of BUG-0256: the highest index comes from the current definition only, so a manual MAC
// in the request equal to the last derived address does not restart allocation at index 0, and
// once the current definition uses index 255 the lowest free index is taken instead of none.
func TestUpdateVMAutomaticMACHighestIndexFromCurrentDefinition(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability)
	spec := l.spec("last-index")
	spec.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio"}, {NetworkID: l.network, Model: "virtio"}}
	var created createdVM
	read(t, l.create("create-last", spec, 202), &created)
	id := created.VMID
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	d0, d1, d2, last := protocol.DerivedMAC(id, 0), protocol.DerivedMAC(id, 1), protocol.DerivedMAC(id, 2), protocol.DerivedMAC(id, 255)
	edit := applied
	edit.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio", MAC: d1}}
	l.update("last-drop", id, edit, 202)
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	applied = *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	// Current [d1]; the request adds a manual d255 and an automatic interface.
	edit = applied
	edit.NICs = []protocol.VMNIC{{NetworkID: l.network, Model: "virtio", MAC: d1}, {NetworkID: l.network, Model: "virtio", MAC: last}, {NetworkID: l.network, Model: "virtio"}}
	l.update("last-add", id, edit, 202)
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || len(commands[0].VM.NICs) != 3 || commands[0].VM.NICs[2].MAC != d2 {
		t.Fatalf("a manual last derived address must not restart allocation at d0: %+v", commands)
	}
	applied = *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	// Current [d1, d255, d2]: nothing above 255 is free, so the lowest free index d0 is used.
	edit = applied
	edit.NICs = append(append([]protocol.VMNIC{}, applied.NICs...), protocol.VMNIC{NetworkID: l.network, Model: "virtio"})
	l.update("last-fallback", id, edit, 202)
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || len(commands[0].VM.NICs) != 4 || commands[0].VM.NICs[3].MAC != d0 {
		t.Fatalf("with index 255 in use the lowest free index is taken: %+v", commands)
	}
}
