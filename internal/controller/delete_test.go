// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
)

func TestDeleteVMGatesDispatchAndMirrorRemoval(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability, protocol.DeleteCapability)
	spec := l.spec("guest-delete")
	var created createdVM
	read(t, l.create("create-del", spec, 202), &created)
	id := created.VMID
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	body := map[string]any{"request_id": "del-1", "delete_disks": false}
	l.a.call("DELETE", "/api/v1/vms/missing", body, nil, 404)
	l.a.c.db.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", id, l.pool, l.host.HostID, "controller", "adopt-del")
	if w := l.a.call("DELETE", "/api/v1/vms/"+id, body, nil, 409); !strings.Contains(w.Body.String(), "Controller appliance") {
		t.Fatal(w.Body.String())
	}
	l.a.c.db.Exec("DELETE FROM appliances WHERE id=?", id)

	l.reportState("running", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}})
	if w := l.a.call("DELETE", "/api/v1/vms/"+id, body, nil, 409); !strings.Contains(w.Body.String(), "stop the VM") {
		t.Fatal(w.Body.String())
	}
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}})

	noCap := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability)
	spec2 := noCap.spec("no-delete-cap")
	var created2 createdVM
	read(t, noCap.create("create-2", spec2, 202), &created2)
	cmds := noCap.report().Commands
	applied2 := *cmds[0].VM
	noCap.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied2}}, protocol.Result{OperationID: cmds[0].OperationID, Status: "succeeded"})
	if w := noCap.a.call("DELETE", "/api/v1/vms/"+created2.VMID, map[string]any{"request_id": "del-cap", "delete_disks": false}, nil, 409); !strings.Contains(w.Body.String(), "does not support deleting") {
		t.Fatal(w.Body.String())
	}

	var first map[string]any
	read(t, l.a.call("DELETE", "/api/v1/vms/"+id, body, nil, 202), &first)
	op := first["operation"].(map[string]any)
	if first["vm_id"] != id || first["delete_disks"] != false || op["kind"] != "vm-delete" || op["status"] != "queued" {
		t.Fatalf("delete response %v", first)
	}
	var replay map[string]any
	read(t, l.a.call("DELETE", "/api/v1/vms/"+id, body, nil, 200), &replay)
	if replay["operation"].(map[string]any)["id"] != op["id"] {
		t.Fatal("replay created a second operation")
	}
	if w := l.a.call("DELETE", "/api/v1/vms/"+id, map[string]any{"request_id": "del-2", "delete_disks": true}, nil, 409); !strings.Contains(w.Body.String(), "still pending") {
		t.Fatal(w.Body.String())
	}
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || commands[0].Kind != "vm-delete" || commands[0].Delete == nil || commands[0].Delete.VMID != id || commands[0].Delete.DeleteDisks {
		t.Fatalf("dispatched %+v", commands)
	}
	tomb := protocol.VMTombstone{ID: id, PoolID: l.pool, HostID: l.host.HostID, Tombstone: true}
	l.reportState("shut off", &protocol.ManagedState{DeletedVMs: []protocol.VMTombstone{tomb}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	var vms []managedAppliance
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	for _, vm := range vms {
		if vm.ID == id {
			t.Fatalf("deleted VM still listed: %+v", vm)
		}
	}
}
