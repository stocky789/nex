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
	for _, cmd := range report([]string{protocol.ManagedVMCapability, protocol.PoolVMPlacementCapability}).Commands {
		enrolled = append(enrolled, protocol.Result{OperationID: cmd.OperationID, Status: "succeeded"})
	}
	if w := a.call("POST", "/api/v1/vms/"+vm+"/media", body, nil, 409); !strings.Contains(w.Body.String(), "does not support central media") {
		t.Fatal(w.Body.String())
	}
	caps := []string{protocol.ManagedVMCapability, protocol.PoolVMPlacementCapability, protocol.MediaCapability}
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
	if err := json.Unmarshal([]byte(createRaw), &mirrored); err != nil || mirrored.VM == nil || mirrored.VM.MediaID != "" || mirrored.VM.Boot.MediaBootOrder != 2 {
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
// must keep every boot order the host's domain still holds, the emptied CD drive's included:
// virsh change-media renumbers nothing, so a renumbered mirror would publish a boot layout the
// VM does not have and, on a pool-shared VM, refuse the eject of a running guest (BUG-0188).
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
	if mirrored.VM.MediaID != "" || mirrored.VM.Boot.MediaBootOrder != 1 || mirrored.VM.Disks[0].BootOrder != 2 {
		t.Fatalf("eject must not renumber the mirrored boot order: %+v", mirrored.VM)
	}
}

func TestCreateAndInsertISOFromSeparateStorage(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.MediaCapability)
	var disks protocol.StorageRegistration
	read(t, l.a.call("POST", "/api/v1/storage", registrationRequest{RequestID: "disks", PoolID: l.pool, HostID: l.host.HostID, Name: "Disks", Path: "/var/lib/nex/vms"}, nil, 201), &disks)
	l.report()
	l.report(protocol.Result{OperationID: disks.OperationID, Status: "succeeded"})
	iso := make([]byte, 65536)
	copy(iso[32769:], "CD001")
	var media protocol.Media
	read(t, l.a.call("POST", "/api/v1/media?storage_id="+l.storage+"&name=guest.iso&request_id=iso-lib", string(iso), nil, 201), &media)
	l.report()
	l.report(protocol.Result{OperationID: media.OperationID, Status: "succeeded"})

	spec := vmSpec(l.pool, l.host.HostID, "guest-split", disks.ID, l.network, media.ID)
	var created createdVM
	read(t, l.create("create-split", spec, 202), &created)
	cmds := l.report().Commands
	if len(cmds) != 1 || cmds[0].VM == nil || cmds[0].VM.MediaID != media.ID || cmds[0].VM.Disks[0].StorageID != disks.ID {
		t.Fatalf("create should attach an ISO from library storage: %+v", cmds)
	}
	l.report(protocol.Result{OperationID: created.Operation.ID, Status: "succeeded"})

	blank := vmSpec(l.pool, l.host.HostID, "guest-blank", disks.ID, l.network, "")
	var other createdVM
	read(t, l.create("create-blank", blank, 202), &other)
	l.report()
	l.report(protocol.Result{OperationID: other.Operation.ID, Status: "succeeded"})
	l.a.call("POST", "/api/v1/vms/"+other.VMID+"/media", map[string]string{"request_id": "insert-lib", "action": "insert", "media_id": media.ID}, nil, 202)
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

// drivers_media_id is a second ISO reference and must pass the same checks as the install ISO:
// a succeeded import on the VM's own host, on a host that defines the driver disc drive
// (BUG-0228). Without them the Controller queues a create or edit Core can only fail.
func TestVMDriversMediaIsValidatedLikeInstallMedia(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.MediaCapability, protocol.UpdateCapability)
	upload := func(request string, mark byte) protocol.Media {
		iso := make([]byte, 65536)
		copy(iso[32769:], "CD001")
		iso[1] = mark
		var media protocol.Media
		read(t, l.a.call("POST", "/api/v1/media?storage_id="+l.storage+"&name="+request+".iso&request_id="+request, string(iso), nil, 201), &media)
		l.report()
		l.report(protocol.Result{OperationID: media.OperationID, Status: "succeeded"})
		return media
	}
	install, drivers := upload("install", 0), upload("drivers", 1)
	spec := func(name, driversID string) protocol.VMCreateSpec {
		s := vmSpec(l.pool, l.host.HostID, name, l.storage, l.network, install.ID)
		s.DriversMediaID = driversID
		return s
	}
	expect := func(w string, want string) {
		t.Helper()
		if !strings.Contains(w, want) {
			t.Fatalf("want %q in %s", want, w)
		}
	}
	// Legacy host: the drivers drive is refused before anything is queued, blank stays allowed.
	expect(l.create("no-cap", spec("guest-nocap", drivers.ID), 409).Body.String(), "driver disc")
	var created createdVM
	read(t, l.create("create-plain", spec("guest-plain", ""), 202), &created)
	commands := l.report().Commands
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{*commands[0].VM}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	expect(l.update("edit-nocap", created.VMID, spec("guest-plain", drivers.ID), 409)["error"].(string), "driver disc")

	l.caps = append(l.caps, protocol.DriversDriveCapability)
	l.report()

	read(t, l.create("create-valid", spec("guest-valid", drivers.ID), 202), &created)
	if cmds := l.report().Commands; len(cmds) != 1 || cmds[0].VM.DriversMediaID != drivers.ID {
		t.Fatalf("drivers media not dispatched: %+v", cmds)
	}
	l.create("create-valid", spec("guest-valid", drivers.ID), 200)
}

// BUG-0228: the web UI names drive "drivers" for driver disc insert and eject. The Controller
// must accept it, refuse it for a host without the drivers drive, carry it to Core, and key
// idempotent replay on it; an omitted drive stays the install drive.
func TestVMMediaDriversDrive(t *testing.T) {
	l, media := mediaLabFixture(t)
	var created createdVM
	read(t, l.create("create-drive", vmSpec(l.pool, l.host.HostID, "guest-drive", l.storage, l.network, media.ID), 202), &created)
	cmds := l.report().Commands
	l.report(protocol.Result{OperationID: cmds[0].OperationID, Status: "succeeded"})
	path := "/api/v1/vms/" + created.VMID + "/media"
	send := func(body map[string]string, status int) string {
		t.Helper()
		return l.a.call("POST", path, body, nil, status).Body.String()
	}
	dispatched := func(action, drive string) {
		t.Helper()
		c := l.report().Commands
		if len(c) != 1 || c[0].VMMedia == nil || c[0].VMMedia.Action != action || c[0].VMMedia.Drive != drive {
			t.Fatalf("want %s on drive %q dispatched: %+v", action, drive, c)
		}
		l.report(protocol.Result{OperationID: c[0].OperationID, Status: "succeeded"})
	}
	insert := map[string]string{"request_id": "drivers-insert", "action": "insert", "media_id": media.ID, "drive": "drivers"}
	if w := send(map[string]string{"request_id": "bad-drive", "action": "eject", "drive": "floppy"}, 400); !strings.Contains(w, "drive must be install or drivers") {
		t.Fatalf("invalid drive: %s", w)
	}
	if w := send(insert, 409); !strings.Contains(w, "does not support a driver disc drive") {
		t.Fatalf("legacy host: %s", w)
	}
	l.caps = append(l.caps, protocol.DriversDriveCapability)
	l.report()
	if w := send(insert, 400); !strings.Contains(w, "the driver disc must differ from the install media") {
		t.Fatalf("install ISO into the driver drive: %s", w)
	}
	if w := send(map[string]string{"request_id": "drivers-unknown", "action": "insert", "media_id": "missing-iso", "drive": "drivers"}, 404); !strings.Contains(w, "the driver disc ISO is not registered and verified on the VM's host and pool") {
		t.Fatalf("unknown driver disc: %s", w)
	}
	send(map[string]string{"request_id": "install-eject", "action": "eject"}, 202)
	dispatched("eject", "")
	var first, replay map[string]any
	read(t, l.a.call("POST", path, insert, nil, 202), &first)
	read(t, l.a.call("POST", path, insert, nil, 200), &replay)
	if first["operation"].(map[string]any)["id"] != replay["operation"].(map[string]any)["id"] {
		t.Fatalf("replay must return the original operation: %v %v", first, replay)
	}
	dispatched("insert", "drivers")
	changed := map[string]string{"request_id": "drivers-insert", "action": "insert", "media_id": media.ID}
	if w := send(changed, 409); !strings.Contains(w, "another payload") {
		t.Fatalf("replay with changed drive: %s", w)
	}
	if w := send(map[string]string{"request_id": "install-same", "action": "insert", "media_id": media.ID, "drive": "install"}, 400); !strings.Contains(w, "the driver disc must differ from the install media") {
		t.Fatalf("driver disc into the install drive: %s", w)
	}
	send(map[string]string{"request_id": "drivers-eject", "action": "eject", "drive": "drivers"}, 202)
	dispatched("eject", "drivers")
	send(map[string]string{"request_id": "install-insert", "action": "insert", "media_id": media.ID}, 202)
	dispatched("insert", "")
}
