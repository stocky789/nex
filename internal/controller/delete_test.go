// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
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

func TestFailedVMCreateDoesNotLeaveControllerProjection(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.DeleteCapability)
	var created createdVM
	read(t, l.create("failed-create", l.spec("no-space"), 202), &created)
	commands := l.report().Commands
	if len(commands) != 1 || commands[0].OperationID != created.Operation.ID {
		t.Fatalf("create dispatch %+v", commands)
	}

	// Core rejects before libvirt creates anything. The failed task remains, but the
	// Controller must not represent its request as a pool VM.
	l.report(protocol.Result{OperationID: created.Operation.ID, Status: "failed", Error: "insufficient storage"})
	var replayKey string
	if err := l.a.c.db.QueryRow("SELECT id FROM managed_resources WHERE operation_id=?", created.Operation.ID).Scan(&replayKey); err != nil || replayKey != failedVMCreateRecordID(created.Operation.ID) {
		t.Fatalf("failed replay record still occupies the VM ID: key=%q err=%v", replayKey, err)
	}
	var vms []managedAppliance
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 0 {
		t.Fatalf("failed create left VM projection: %+v", vms)
	}
	var operations []protocol.Operation
	read(t, l.a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	var failed *protocol.Operation
	for i := range operations {
		if operations[i].ID == created.Operation.ID {
			failed = &operations[i]
		}
	}
	if failed == nil || failed.Status != "failed" || failed.Error != "insufficient storage" {
		t.Fatalf("failed create task was not retained: %+v", operations)
	}
	var replay createdVM
	read(t, l.create("failed-create", l.spec("no-space"), 200), &replay)
	if replay.VMID != created.VMID || replay.Operation.ID != created.Operation.ID || replay.Operation.Status != "failed" {
		t.Fatalf("failed create did not replay its original task: %+v", replay)
	}
	l.create("retry-create", l.spec("no-space"), 202)
}

func TestHistoricalFailedVMCreateStaysHiddenAndReplayable(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	var created createdVM
	read(t, l.create("historic-failure", l.spec("historic-ghost"), 202), &created)
	if commands := l.report().Commands; len(commands) != 1 {
		t.Fatalf("create dispatch %+v", commands)
	}
	report := func(checks []protocol.Check) {
		l.seq++
		inv := &protocol.Inventory{
			Hostname: "core", CPUCount: 8, CPUModels: []string{"Skylake-Server"}, MemoryBytes: 16 << 30,
			Managed: hostDefaults(l.host.HostID),
			Checks:  checks,
		}
		l.a.report(l.cert, protocol.ReportRequest{Version: protocol.Version, Sequence: l.seq, Capabilities: l.caps, Inventory: inv})
	}
	// Seed a prior healthy check while the creation is still dispatched, then simulate a
	// Controller version that recorded Core's terminal failure but retained its payload.
	now := time.Now().UTC()
	report([]protocol.Check{{Name: "managed-vms", Status: "healthy", ObservedAt: now}})
	if _, err := l.a.c.db.Exec("UPDATE operations SET status='failed',error='insufficient storage' WHERE id=?", created.Operation.ID); err != nil {
		t.Fatal(err)
	}
	// Merged, missing and stale checks cannot alter a failed request record. It stays out
	// of the VM projection and adoption conflict set, but the original request replays.
	report([]protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now.Add(time.Millisecond)}})
	var vms []managedAppliance
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 0 {
		t.Fatalf("failed create reappeared after omitted check: %+v", vms)
	}
	report([]protocol.Check{{Name: "managed-vms", Status: "healthy", ObservedAt: now.Add(-l.a.c.cfg.StaleAfter - time.Second)}})
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 0 {
		t.Fatalf("failed create reappeared after stale check: %+v", vms)
	}
	// A later Core definition can even reuse the original identity. Adoption relocates the
	// historical replay row before storing the Core-owned definition.
	adopted := l.spec("historic-ghost")
	adopted.ID = created.VMID
	defaults := hostDefaults(l.host.HostID)
	defaults.VMs = []protocol.VMCreateSpec{adopted}
	l.seq++
	l.a.report(l.cert, protocol.ReportRequest{Version: protocol.Version, Sequence: l.seq, Capabilities: l.caps, Inventory: &protocol.Inventory{
		Hostname: "core", CPUCount: 8, CPUModels: []string{"Skylake-Server"}, MemoryBytes: 16 << 30, Managed: defaults,
		Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: time.Now().UTC()}},
	}})
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || vms[0].ID != adopted.ID || vms[0].Status != "succeeded" {
		t.Fatalf("Core definition was blocked by failed create: %+v", vms)
	}
	var replayKey string
	if err := l.a.c.db.QueryRow("SELECT id FROM managed_resources WHERE operation_id=?", created.Operation.ID).Scan(&replayKey); err != nil || replayKey != failedVMCreateRecordID(created.Operation.ID) {
		t.Fatalf("historical replay record was not relocated: key=%q err=%v", replayKey, err)
	}
	var replay createdVM
	read(t, l.create("historic-failure", l.spec("historic-ghost"), 200), &replay)
	if replay.VMID != created.VMID || replay.Operation.ID != created.Operation.ID || replay.Operation.Status != "failed" {
		t.Fatalf("historical failed create did not replay: %+v", replay)
	}
	l.create("historic-retry", l.spec("historic-ghost"), 202)
}
