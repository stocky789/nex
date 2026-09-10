// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestMediaRequestsAreAuthorizedDurableAndDispatched(t *testing.T) {
	a := newClient(t)
	site := a.site()
	host := a.enroll(a.token(site, ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	pool := a.poolOf(host.HostID)
	vm := "0f3f7a86-1d0e-4e3c-a1a4-7c5d3f1e2b91"
	mediaID := "iso-media-1"
	body := map[string]string{"request_id": "media-1", "action": "eject"}
	a.call("POST", "/api/v1/vms/"+vm+"/media", body, nil, 404)

	storageID := "st-1"
	a.c.db.Exec(`INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)`, "op-st", host.HostID, "storage-register", "req-st", "succeeded", "", stamp(), stamp(), "root")
	a.c.db.Exec(`INSERT INTO managed_resources VALUES(?,?,?,?,?,?,?,?,?)`, storageID, "storage-register", pool, host.HostID, "local", "req-st",
		`{"request_id":"req-st","pool_id":"`+pool+`","host_id":"`+host.HostID+`","name":"local","path":"/srv/vms"}`,
		`{"operation_id":"op-st","kind":"storage-register","storage":{"id":"`+storageID+`","pool_id":"`+pool+`","host_id":"`+host.HostID+`","name":"local","path":"/srv/vms"}}`, "op-st")
	a.c.db.Exec(`INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)`, "op-iso", host.HostID, "iso-import", "req-iso", "succeeded", "", stamp(), stamp(), "root")
	a.c.db.Exec(`INSERT INTO managed_resources VALUES(?,?,?,?,?,?,?,?,?)`, mediaID, "iso-import", pool, host.HostID, "install.iso", "req-iso",
		`{"request_id":"req-iso","storage_id":"`+storageID+`","name":"install.iso","size_bytes":40000,"sha256":"`+strings.Repeat("a", 64)+`"}`,
		`{"operation_id":"op-iso","kind":"iso-import","media":{"id":"`+mediaID+`","pool_id":"`+pool+`","host_id":"`+host.HostID+`","storage_id":"`+storageID+`","name":"install.iso","size_bytes":40000,"sha256":"`+strings.Repeat("a", 64)+`"}}`, "op-iso")
	vmCmd := `{"operation_id":"op-vm","kind":"vm-create","vm":{"id":"` + vm + `","pool_id":"` + pool + `","host_id":"` + host.HostID + `","name":"guest","cpu":{"count":1,"model":"host-passthrough"},"memory":{"mode":"dedicated","size_mib":512},"disks":[{"storage_id":"` + storageID + `","name":"system","size_gib":10,"bus":"virtio","boot_order":1}],"nics":[],"boot":{"firmware":"bios","media_boot_order":2},"media_id":"` + mediaID + `"}}`
	a.c.db.Exec(`INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)`, "op-vm", host.HostID, "vm-create", "req-vm", "succeeded", "", stamp(), stamp(), "root")
	a.c.db.Exec(`INSERT INTO managed_resources VALUES(?,?,?,?,?,?,?,?,?)`, vm, "vm-create", pool, host.HostID, "guest", "req-vm",
		`{"request_id":"req-vm","pool_id":"`+pool+`","host_id":"`+host.HostID+`","name":"guest","media_id":"`+mediaID+`"}`, vmCmd, "op-vm")

	a.call("POST", "/api/v1/vms/"+vm+"/media", map[string]string{"request_id": "media-1", "action": "explode"}, nil, 400)
	a.call("POST", "/api/v1/vms/"+vm+"/media", map[string]string{"request_id": "media-1", "action": "insert"}, nil, 400)
	a.call("POST", "/api/v1/vms/"+vm+"/media", body, nil, 409) // never reported

	sequence := uint64(0)
	report := func(caps []string, results ...protocol.Result) protocol.ReportResponse {
		sequence++
		now := time.Now().UTC()
		inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 4, MemoryBytes: 8 << 30, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}, VMs: []protocol.VM{{ID: vm, Name: "guest", State: "shut off"}}}
		return a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: caps, Inventory: inv, Results: results})
	}
	var enrolled []protocol.Result
	for _, cmd := range report([]string{protocol.ManagedVMCapability}).Commands {
		enrolled = append(enrolled, protocol.Result{OperationID: cmd.OperationID, Status: "succeeded"})
	}
	if w := a.call("POST", "/api/v1/vms/"+vm+"/media", body, nil, 409); !strings.Contains(w.Body.String(), "does not support central media") {
		t.Fatal(w.Body.String())
	}
	caps := []string{protocol.ManagedVMCapability, protocol.MediaCapability}
	report(caps, enrolled...)

	var first map[string]any
	read(t, a.call("POST", "/api/v1/vms/"+vm+"/media", body, nil, 202), &first)
	op := first["operation"].(map[string]any)
	if first["vm_id"] != vm || first["action"] != "eject" || op["kind"] != "vm-media" || op["status"] != "queued" {
		t.Fatalf("media response %v", first)
	}
	var replay map[string]any
	read(t, a.call("POST", "/api/v1/vms/"+vm+"/media", body, nil, 200), &replay)
	if replay["operation"].(map[string]any)["id"] != op["id"] {
		t.Fatal("replay created a second operation")
	}
	a.call("POST", "/api/v1/vms/"+vm+"/media", map[string]string{"request_id": "media-1", "action": "insert", "media_id": mediaID}, nil, 409)
	if w := a.call("POST", "/api/v1/vms/"+vm+"/media", map[string]string{"request_id": "media-2", "action": "insert", "media_id": mediaID}, nil, 409); !strings.Contains(w.Body.String(), "still pending") {
		t.Fatal(w.Body.String())
	}
	commands := report(caps).Commands
	if len(commands) != 1 || commands[0].Kind != "vm-media" || commands[0].VMMedia == nil || commands[0].VMMedia.VMID != vm || commands[0].VMMedia.Action != "eject" || commands[0].VMMedia.HostID != host.HostID || commands[0].VMMedia.PoolID != pool {
		t.Fatalf("dispatched %+v", commands)
	}
	report(caps, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	var createRaw string
	a.c.db.QueryRow("SELECT command FROM managed_resources WHERE id=? AND kind='vm-create'", vm).Scan(&createRaw)
	var mirrored protocol.Command
	if err := json.Unmarshal([]byte(createRaw), &mirrored); err != nil || mirrored.VM == nil || mirrored.VM.MediaID != "" || mirrored.VM.Boot.MediaBootOrder != 0 {
		t.Fatalf("mirror after eject %+v raw %s err %v", mirrored.VM, createRaw, err)
	}

	read(t, a.call("POST", "/api/v1/vms/"+vm+"/media", map[string]string{"request_id": "media-2", "action": "insert", "media_id": mediaID}, nil, 202), &first)
	commands = report(caps).Commands
	if len(commands) != 1 || commands[0].VMMedia == nil || commands[0].VMMedia.Action != "insert" || commands[0].VMMedia.MediaID != mediaID {
		t.Fatalf("insert dispatched %+v", commands)
	}
	report(caps, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	a.c.db.QueryRow("SELECT command FROM managed_resources WHERE id=? AND kind='vm-create'", vm).Scan(&createRaw)
	if err := json.Unmarshal([]byte(createRaw), &mirrored); err != nil || mirrored.VM == nil || mirrored.VM.MediaID != mediaID {
		t.Fatalf("mirror after insert %+v", mirrored.VM)
	}

	var ops []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	found := false
	for _, op := range ops {
		if op.Kind == "vm-media" && op.Status == "succeeded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing succeeded vm-media op %+v", ops)
	}
	var vms []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	for _, v := range vms {
		if v.ID != vm {
			continue
		}
		if v.Spec == nil || v.Spec.MediaID != mediaID {
			t.Fatalf("VM list media %+v", v.Spec)
		}
	}
}

// mediaLabFixture is a managed-vm-and-media-capable lab with one imported ISO ready to attach.
func mediaLabFixture(t *testing.T) (*managedLab, protocol.Media) {
	t.Helper()
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.MediaCapability)
	iso := make([]byte, 65536)
	copy(iso[32769:], "CD001")
	uploadPath := "/api/v1/media?storage_id=" + l.storage + "&name=guest.iso&request_id=media-upload"
	var media protocol.Media
	read(t, l.a.call("POST", uploadPath, string(iso), nil, 201), &media)
	l.report()
	l.report(protocol.Result{OperationID: media.OperationID, Status: "succeeded"})
	return l, media
}

// A VM created install-media-first (media_boot_order 1, disk boot_order 2, the shape vmSpec
// returns when it is given install media) must stay adoptable after an eject, and the mirror
// must keep the disk boot order the host's domain still holds: virsh change-media renumbers
// nothing, so mirroring a renumbered order would publish a boot device the VM does not have.
func TestMirrorVMMediaKeepsBootOrderOnEject(t *testing.T) {
	l, media := mediaLabFixture(t)
	spec := vmSpec(l.pool, l.host.HostID, "guest-media-first", l.storage, l.network, media.ID)
	if spec.Boot.MediaBootOrder != 1 || spec.Disks[0].BootOrder != 2 {
		t.Fatalf("fixture must be install-media-first: %+v", spec.Boot)
	}
	var created createdVM
	read(t, l.create("create-media-first", spec, 202), &created)
	dispatched := l.report().Commands
	if len(dispatched) != 1 || dispatched[0].VM == nil {
		t.Fatalf("VM create not dispatched: %+v", dispatched)
	}
	l.report(protocol.Result{OperationID: created.Operation.ID, Status: "succeeded"})

	l.a.call("POST", "/api/v1/vms/"+created.VMID+"/media", map[string]string{"request_id": "eject-media-first", "action": "eject"}, nil, 202)
	commands := l.report().Commands
	if len(commands) != 1 || commands[0].VMMedia == nil || commands[0].VMMedia.Action != "eject" {
		t.Fatalf("eject not dispatched: %+v", commands)
	}
	l.report(protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	var raw string
	if err := l.a.c.db.QueryRow("SELECT command FROM managed_resources WHERE id=? AND kind='vm-create'", created.VMID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var mirrored protocol.Command
	if json.Unmarshal([]byte(raw), &mirrored) != nil || mirrored.VM == nil {
		t.Fatalf("mirror unavailable: %s", raw)
	}
	if err := protocol.ValidateVMSpec(*mirrored.VM); err != nil {
		t.Fatalf("post-eject mirrored spec fails validation, a fresh Controller could not adopt this VM: %v (%+v)", err, mirrored.VM)
	}
	if mirrored.VM.MediaID != "" || mirrored.VM.Boot.MediaBootOrder != 0 || mirrored.VM.Disks[0].BootOrder != 2 {
		t.Fatalf("eject must not renumber the mirrored boot order: %+v", mirrored.VM)
	}
}

// mirrorVMMedia used to rewrite the vm-create row's stored request payload, the record
// replayResource compares an idempotent replay against. A media change must not disturb it:
// replaying the original create request_id must still return the original VM, not a conflict.
func TestVMCreateReplayStaysIdempotentAfterMediaChange(t *testing.T) {
	l, media := mediaLabFixture(t)
	req := createVMRequest{RequestID: "create-replay-media", VMCreateSpec: vmSpec(l.pool, l.host.HostID, "guest-replay", l.storage, l.network, media.ID)}
	var created createdVM
	read(t, l.a.call("POST", "/api/v1/vms", req, nil, 202), &created)
	l.report()
	l.report(protocol.Result{OperationID: created.Operation.ID, Status: "succeeded"})

	l.a.call("POST", "/api/v1/vms/"+created.VMID+"/media", map[string]string{"request_id": "eject-replay", "action": "eject"}, nil, 202)
	commands := l.report().Commands
	if len(commands) != 1 || commands[0].VMMedia == nil {
		t.Fatalf("eject not dispatched: %+v", commands)
	}
	l.report(protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	var replay createdVM
	read(t, l.a.call("POST", "/api/v1/vms", req, nil, 200), &replay)
	if replay.VMID != created.VMID || replay.Operation.ID != created.Operation.ID {
		t.Fatalf("original create request_id must stay idempotent after a media change: got %+v, want %+v", replay, created)
	}
}
