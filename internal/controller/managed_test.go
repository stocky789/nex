// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManagedWorkflowScopeRetryAndMedia(t *testing.T) {
	a := newClient(t)
	site := a.site()
	host := a.enroll(a.token(site, ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	sequence := uint64(0)
	report := func(caps bool, results ...protocol.Result) protocol.ReportResponse {
		sequence++
		v := protocol.ReportRequest{Version: 1, Sequence: sequence, Results: results, Inventory: &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30}}
		if caps {
			v.Capabilities = []string{protocol.ManagedVMCapability}
		}
		return a.report(cert, v)
	}
	req := registrationRequest{RequestID: "storage1", PoolID: pool, HostID: host.HostID, Name: "Local", Path: "/var/lib/nex/vms"}
	a.call("POST", "/api/v1/storage", req, nil, 409)
	auto := report(true)
	if len(auto.Commands) != 1 || auto.Commands[0].Storage == nil || auto.Commands[0].Storage.Path != defaultLocalStoragePath || auto.Commands[0].Storage.Name != defaultLocalStorageName {
		t.Fatalf("auto ISO storage %#v", auto.Commands)
	}
	report(true, protocol.Result{OperationID: auto.Commands[0].OperationID, Status: "succeeded"})
	a.call("POST", "/api/v1/hosts/"+host.HostID+"/operations", map[string]string{"request_id": "reserved-request", "kind": "refresh-inventory"}, nil, 202)
	reserved := req
	reserved.RequestID = "reserved-request"
	a.call("POST", "/api/v1/storage", reserved, nil, 409)
	refresh := report(true).Commands[0]
	report(true, protocol.Result{OperationID: refresh.OperationID, Status: "succeeded"})
	var storage protocol.StorageRegistration
	read(t, a.call("POST", "/api/v1/storage", req, nil, 201), &storage)
	read(t, a.call("POST", "/api/v1/storage", req, nil, 200), &storage)
	req.Path = "/different"
	a.call("POST", "/api/v1/storage", req, nil, 409)
	if len(report(false).Commands) != 0 {
		t.Fatal("old agent received managed command")
	}
	commands := report(true).Commands
	if len(commands) != 1 || commands[0].Storage.ID != storage.ID {
		t.Fatalf("storage command %#v", commands)
	}
	report(true, protocol.Result{OperationID: storage.OperationID, Status: "succeeded"})
	networkReq := registrationRequest{RequestID: "network1", PoolID: pool, HostID: host.HostID, Name: "Guests", Kind: "bridge", Source: "br0"}
	var network protocol.NetworkRegistration
	read(t, a.call("POST", "/api/v1/networks", networkReq, nil, 201), &network)
	report(true)
	report(true, protocol.Result{OperationID: network.OperationID, Status: "failed", Error: "bridge absent"})
	networkReq.RequestID = "network2"
	read(t, a.call("POST", "/api/v1/networks", networkReq, nil, 201), &network)
	report(true)
	report(true, protocol.Result{OperationID: network.OperationID, Status: "succeeded"})
	uploadPath := "/api/v1/media?storage_id=" + storage.ID + "&name=guest.iso&request_id=iso1"
	// Sparse files exercise actual streamed-byte limits without an 8GiB allocation.
	sparsePath := filepath.Join(a.c.cfg.DataDir, "media", "reserved")
	sparse, err := os.Create(sparsePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = sparse.Truncate(maxStagingBytes - 40000); err != nil {
		t.Fatal(err)
	}
	sparse.Close()
	oversized := httptest.NewRequest("POST", "https://nex.test"+uploadPath, bytes.NewReader(make([]byte, 65536)))
	oversized.ContentLength = -1
	oversized.AddCookie(a.cookie)
	oversized.Header.Set("X-CSRF-Token", a.csrf)
	oversizedResponse := httptest.NewRecorder()
	a.c.Handler().ServeHTTP(oversizedResponse, oversized)
	if oversizedResponse.Code != 413 {
		t.Fatalf("actual body limit: %d %s", oversizedResponse.Code, oversizedResponse.Body.String())
	}
	os.Remove(sparsePath)

	a.call("POST", uploadPath, "bad", nil, 400)
	iso := make([]byte, 65536)
	copy(iso[32769:], "CD001")
	var media protocol.Media
	read(t, a.call("POST", uploadPath, string(iso), nil, 201), &media)
	a.call("POST", uploadPath, string(iso), nil, 200)
	iso[1] = 1
	a.call("POST", uploadPath, string(iso), nil, 409)
	iso[1] = 0
	a.call("GET", "/api/v1/agent/media/"+media.ID, nil, cert, 409)
	report(true)
	other := a.enroll(a.token(site, ""), csr(t))
	a.call("GET", "/api/v1/agent/media/"+media.ID, nil, certificate(t, other.CertificatePEM), 404)
	if got := a.call("GET", "/api/v1/agent/media/"+media.ID, nil, cert, 200).Body.Bytes(); !bytes.Equal(got, iso) {
		t.Fatal("download changed bytes")
	}
	report(true, protocol.Result{OperationID: media.OperationID, Status: "succeeded"})
	if _, err := os.Stat(filepath.Join(a.c.cfg.DataDir, "media", media.ID)); !os.IsNotExist(err) {
		t.Fatal("terminal import retained staged blob", err)
	}
	v := createVMRequest{RequestID: "vm1", VMCreateSpec: protocol.VMCreateSpec{PoolID: pool, HostID: host.HostID, Name: "guest1", StorageID: storage.ID, NetworkID: network.ID, MediaID: media.ID, CPUs: 2, MemoryMiB: 1024, DiskGiB: 10, Firmware: "bios"}}
	var created struct {
		VMID      string             `json:"vm_id"`
		Operation protocol.Operation `json:"operation"`
	}
	read(t, a.call("POST", "/api/v1/vms", v, nil, 202), &created)
	sequence++
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: time.Now().UTC()}}, VMs: []protocol.VM{{ID: created.VMID, Name: "guest1", State: "shut off"}}}})
	a.call("POST", "/api/v1/appliances/adopt", applianceRequest{RequestID: "adopt-guest", PoolID: pool, HostID: host.HostID, VMID: created.VMID, Name: "guest1"}, nil, 409)
	if !applianceUUID(created.VMID) {
		t.Fatal("invalid stable uuid")
	}
	a.call("POST", "/api/v1/vms", v, nil, 200)
	v.CPUs = 3
	a.call("POST", "/api/v1/vms", v, nil, 409)
	v.RequestID = "vm2"
	v.HostID = other.HostID
	a.call("POST", "/api/v1/vms", v, nil, 409)
	dispatched := report(true).Commands
	if len(dispatched) != 1 || dispatched[0].VM.ID != created.VMID {
		t.Fatalf("bad VM command %#v", dispatched)
	}
	report(true, protocol.Result{OperationID: created.Operation.ID, Status: "needs_attention", Error: "reconcile existing disk"})
	var list []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &list)
	if len(list) != 1 || list[0].ID != created.VMID || list[0].Status != "needs_attention" || list[0].State != "shut off" || list[0].Guest != nil {
		t.Fatalf("bad VM list %#v", list)
	}
	// A running VM's guest observation is passed through from the fresh host report.
	guest := &protocol.GuestInfo{Agent: "connected", OS: "Debian GNU/Linux 13 (trixie)", Hostname: "guest1", Addresses: []protocol.GuestAddress{{Interface: "enp1s0", Address: "192.168.122.10/24"}}, Clipboard: "ready", Resize: true}
	sequence++
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: time.Now().UTC()}}, VMs: []protocol.VM{{ID: created.VMID, Name: "guest1", State: "running", Guest: guest}}}})
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &list)
	if len(list) != 1 || list[0].State != "running" || !reflect.DeepEqual(list[0].Guest, guest) {
		t.Fatalf("guest observation missing from VM list %#v", list[0].Guest)
	}
}
func TestManagedAuthorizationAndActualBodyLimits(t *testing.T) {
	a := newClient(t)
	for _, route := range []string{"/api/v1/storage", "/api/v1/networks", "/api/v1/media", "/api/v1/vms"} {
		cookie := a.cookie
		a.cookie = nil
		a.call("POST", route, nil, nil, 401)
		a.cookie = cookie
		csrf := a.csrf
		a.csrf = ""
		a.call("POST", route, nil, nil, 403)
		a.csrf = csrf
	}
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}})
	a.call("POST", "/api/v1/storage", registrationRequest{RequestID: "bad", PoolID: pool, HostID: host.HostID, Name: "bad", Path: "/var/../root"}, nil, 400)
	r := httptest.NewRequest("POST", "https://nex.test/api/v1/media?name=x.iso&storage_id=x&request_id=x", nil)
	r.AddCookie(a.cookie)
	r.Header.Set("X-CSRF-Token", a.csrf)
	r.ContentLength = protocol.MaxISOBytes + 1
	w := httptest.NewRecorder()
	a.c.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestVLANNetworkRegistration(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical"}, {Name: "br0", Kind: "bridge"}, {Name: "nex1", Kind: "physical"}, {Name: "legacy"}}
	first := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "core", NICs: nics}})
	for _, cmd := range first.Commands {
		if cmd.Storage != nil {
			a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "core", NICs: nics}, Results: []protocol.Result{{OperationID: cmd.OperationID, Status: "succeeded"}}})
		}
	}
	base := registrationRequest{RequestID: "vlan1", PoolID: pool, HostID: host.HostID, Name: "Servers", Kind: "vlan", Uplink: "nex1", VLAN: 100}
	for name, bad := range map[string]func(r *registrationRequest){
		"tag too high":      func(r *registrationRequest) { r.VLAN = 4095 },
		"explicit source":   func(r *registrationRequest) { r.Source = "br0" },
		"long uplink":       func(r *registrationRequest) { r.Uplink = "enx001122334455" },
		"vlan on bridge":    func(r *registrationRequest) { r.Kind = "bridge"; r.Source = "br0" },
		"uplink on network": func(r *registrationRequest) { r.Kind = "network"; r.Source = "default"; r.VLAN = 0 },
	} {
		r := base
		bad(&r)
		r.RequestID = "bad-" + name
		a.call("POST", "/api/v1/networks", r, nil, 400)
	}
	for name, uplink := range map[string]string{"unobserved": "nex9", "bridge": "br0"} {
		r := base
		r.Uplink = uplink
		r.RequestID = "conflict-" + name
		a.call("POST", "/api/v1/networks", r, nil, 409)
	}
	var network protocol.NetworkRegistration
	read(t, a.call("POST", "/api/v1/networks", base, nil, 201), &network)
	if network.Kind != "vlan" || network.Source != "nex1v100" || network.Uplink != "nex1" || network.VLAN != 100 {
		t.Fatalf("derived registration %+v", network)
	}
	read(t, a.call("POST", "/api/v1/networks", base, nil, 200), &network)
	commands := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 3, Capabilities: []string{protocol.ManagedVMCapability}}).Commands
	if len(commands) != 1 || commands[0].Network == nil || commands[0].Network.Source != "nex1v100" || commands[0].Network.VLAN != 100 {
		t.Fatalf("VLAN command %#v", commands)
	}
	// The same uplink/VLAN, or a plain registration of the derived bridge, is one host resource.
	dup := base
	dup.RequestID = "vlan-dup"
	dup.Name = "Servers again"
	a.call("POST", "/api/v1/networks", dup, nil, 409)
	plain := registrationRequest{RequestID: "bridge-dup", PoolID: pool, HostID: host.HostID, Name: "Manual", Kind: "bridge", Source: "nex1v100"}
	a.call("POST", "/api/v1/networks", plain, nil, 409)
	// Untagged on the legacy-reported NIC without a kind, and a second tag on the same uplink, are distinct.
	other := base
	other.RequestID, other.Name, other.Uplink, other.VLAN = "vlan-legacy", "Untagged", "legacy", 0
	read(t, a.call("POST", "/api/v1/networks", other, nil, 201), &network)
	if network.Source != "legacyv0" {
		t.Fatal(network.Source)
	}
	var list []protocol.NetworkRegistration
	read(t, a.call("GET", "/api/v1/networks", nil, nil, 200), &list)
	if len(list) != 2 || list[0].VLAN != 100 || list[1].Uplink != "legacy" {
		t.Fatalf("list %+v", list)
	}
}
func TestDefaultUntaggedNetworkAutoRegistration(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	pool := a.poolOf(host.HostID)
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}, {Name: "nex0.15", Kind: "vlan", Parent: "nex0", Master: "nex0v15"}, {Name: "nex0v15", Kind: "bridge"}, {Name: "vnet0", Kind: "virtual", Master: "br0"}}
	report := func(seq uint64, caps bool) protocol.ReportResponse {
		v := protocol.ReportRequest{Version: 1, Sequence: seq, Inventory: &protocol.Inventory{Hostname: "core", NICs: nics}}
		if caps {
			v.Capabilities = []string{protocol.ManagedVMCapability}
		}
		return a.report(cert, v)
	}
	var list []protocol.NetworkRegistration
	report(1, false)
	read(t, a.call("GET", "/api/v1/networks", nil, nil, 200), &list)
	if len(list) != 0 {
		t.Fatal("old agent must not receive auto networks")
	}
	// A bridge on a physical port is registered once; the VLAN bridge (port is a VLAN device) is not.
	// Default ISO local storage is also queued on the first managed report.
	response := report(2, true)
	read(t, a.call("GET", "/api/v1/networks", nil, nil, 200), &list)
	if len(list) != 1 || list[0].Kind != "bridge" || list[0].Source != "br0" || list[0].Uplink != "nex0" || list[0].Name != "nex0 untagged" || list[0].PoolID != pool {
		t.Fatalf("auto network %+v", list)
	}
	var storage []protocol.StorageRegistration
	read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &storage)
	if len(storage) != 1 || storage[0].Name != defaultLocalStorageName || storage[0].Path != defaultLocalStoragePath || storage[0].PoolID != pool {
		t.Fatalf("auto ISO storage %+v", storage)
	}
	if len(response.Commands) != 2 {
		t.Fatalf("auto network+storage commands %+v", response.Commands)
	}
	var networkCmd, storageCmd *protocol.Command
	for i := range response.Commands {
		switch {
		case response.Commands[i].Network != nil:
			networkCmd = &response.Commands[i]
		case response.Commands[i].Storage != nil:
			storageCmd = &response.Commands[i]
		}
	}
	if networkCmd == nil || networkCmd.Network.Source != "br0" || storageCmd == nil || storageCmd.Storage.Path != defaultLocalStoragePath {
		t.Fatalf("auto network/storage command %+v", response.Commands)
	}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 3, Capabilities: []string{protocol.ManagedVMCapability}, Results: []protocol.Result{{OperationID: list[0].OperationID, Status: "failed", Error: "bridge missing"}}})
	report(4, true)
	read(t, a.call("GET", "/api/v1/networks", nil, nil, 200), &list)
	if len(list) != 1 || list[0].Status != "failed" {
		t.Fatalf("failed auto registration must not be recreated: %+v", list)
	}
	read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &storage)
	if len(storage) != 1 {
		t.Fatalf("auto ISO storage recreated: %+v", storage)
	}
	// A manual VLAN on the same uplink still works alongside the default network.
	read(t, a.call("POST", "/api/v1/networks", registrationRequest{RequestID: "vlan15", PoolID: pool, HostID: host.HostID, Name: "vlan15", Kind: "vlan", Uplink: "nex0", VLAN: 15}, nil, 201), &list[0])
}
func TestDefaultISOStorageAutoRegistration(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	pool := a.poolOf(host.HostID)
	report := func(seq uint64, caps bool, results ...protocol.Result) protocol.ReportResponse {
		v := protocol.ReportRequest{Version: 1, Sequence: seq, Results: results, Inventory: &protocol.Inventory{Hostname: "core"}}
		if caps {
			v.Capabilities = []string{protocol.ManagedVMCapability}
		}
		return a.report(cert, v)
	}
	var list []protocol.StorageRegistration
	report(1, false)
	read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &list)
	if len(list) != 0 {
		t.Fatal("old agent must not receive auto storage")
	}
	response := report(2, true)
	read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &list)
	if len(list) != 1 || list[0].Name != defaultLocalStorageName || list[0].Path != defaultLocalStoragePath || list[0].PoolID != pool || list[0].HostID != host.HostID {
		t.Fatalf("auto ISO %+v", list)
	}
	if len(response.Commands) != 1 || response.Commands[0].Storage == nil || response.Commands[0].Storage.Path != defaultLocalStoragePath {
		t.Fatalf("auto ISO command %+v", response.Commands)
	}
	report(3, true, protocol.Result{OperationID: list[0].OperationID, Status: "failed", Error: "path missing"})
	report(4, true)
	read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &list)
	if len(list) != 1 || list[0].Status != "failed" {
		t.Fatalf("failed auto ISO must not be recreated: %+v", list)
	}
	// Failed definite registrations can be revalidated with a new request_id; the path stays free.
	var other protocol.StorageRegistration
	read(t, a.call("POST", "/api/v1/storage", registrationRequest{RequestID: "retry-iso", PoolID: pool, HostID: host.HostID, Name: "ISO", Path: defaultLocalStoragePath}, nil, 201), &other)
	read(t, a.call("POST", "/api/v1/storage", registrationRequest{RequestID: "extra", PoolID: pool, HostID: host.HostID, Name: "Extra", Path: "/var/lib/nex/vms"}, nil, 201), &other)
}
func TestHostReportedManagedStateIsAdopted(t *testing.T) {
	// A rebuilt Controller mirrors the master's journal: IDs and names survive, pool/host are rewritten.
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	pool := a.poolOf(host.HostID)
	sha := strings.Repeat("ab", 32)
	managed := &protocol.ManagedState{
		Storage: []protocol.StorageRegistration{{ID: "stor-1", PoolID: "old-pool", HostID: "old-host", Name: "Local", Path: "/var/lib/nex/vms"}},
		Networks: []protocol.NetworkRegistration{
			{ID: "net-default", PoolID: "old-pool", HostID: "old-host", Name: "nex0 untagged", Kind: "bridge", Source: "br0", Uplink: "nex0"},
			{ID: "net-vlan15", PoolID: "old-pool", HostID: "old-host", Name: "vlan15", Kind: "vlan", Source: "nex0v15", Uplink: "nex0", VLAN: 15},
			{ID: "bad id!", Name: "bogus", Kind: "vlan", Source: "wrong", Uplink: "nex0", VLAN: 7},
		},
		Media: []protocol.Media{{ID: "iso-1", PoolID: "old-pool", HostID: "old-host", StorageID: "stor-1", Name: "guest.iso", SizeBytes: 65536, SHA256: sha}},
		VMs:   []protocol.VMCreateSpec{{ID: "vm-1", PoolID: "old-pool", HostID: "old-host", Name: "guest1", StorageID: "stor-1", NetworkID: "net-default", MediaID: "iso-1", CPUs: 2, MemoryMiB: 1024, DiskGiB: 10, Firmware: "bios"}},
	}
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}}
	inv := &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, NICs: nics, Managed: managed}
	report := func(seq uint64) protocol.ReportResponse {
		return a.report(cert, protocol.ReportRequest{Version: 1, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	}
	response := report(1)
	if len(response.Commands) != 1 || response.Commands[0].Storage == nil || response.Commands[0].Storage.Path != defaultLocalStoragePath {
		t.Fatalf("adopted entries must not be dispatched; only auto ISO: %+v", response.Commands)
	}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv, Results: []protocol.Result{{OperationID: response.Commands[0].OperationID, Status: "succeeded"}}})
	var storage []protocol.StorageRegistration
	var networks []protocol.NetworkRegistration
	var media []protocol.Media
	var vms []managedAppliance
	load := func() {
		read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &storage)
		read(t, a.call("GET", "/api/v1/networks", nil, nil, 200), &networks)
		read(t, a.call("GET", "/api/v1/media", nil, nil, 200), &media)
		read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	}
	load()
	if len(storage) != 2 {
		t.Fatalf("storage %+v", storage)
	}
	byID := map[string]protocol.StorageRegistration{}
	for _, s := range storage {
		byID[s.ID] = s
	}
	if s := byID["stor-1"]; s.Name != "Local" || s.PoolID != pool || s.HostID != host.HostID || s.Status != "succeeded" {
		t.Fatalf("adopted storage %+v", storage)
	}
	foundISO := false
	for _, s := range storage {
		if s.Path == defaultLocalStoragePath && s.Name == defaultLocalStorageName && s.Status == "succeeded" {
			foundISO = true
		}
	}
	if !foundISO {
		t.Fatalf("auto ISO missing: %+v", storage)
	}
	if len(networks) != 2 || networks[0].ID != "net-default" || networks[0].Name != "nex0 untagged" || networks[0].Uplink != "nex0" || networks[0].Status != "succeeded" ||
		networks[1].ID != "net-vlan15" || networks[1].VLAN != 15 || networks[1].PoolID != pool || networks[1].HostID != host.HostID || networks[1].Status != "succeeded" {
		t.Fatalf("networks %+v", networks)
	}
	if len(media) != 1 || media[0].ID != "iso-1" || media[0].StorageID != "stor-1" || media[0].PoolID != pool || media[0].HostID != host.HostID || media[0].SHA256 != sha || media[0].Status != "succeeded" {
		t.Fatalf("media %+v", media)
	}
	if len(vms) != 1 || vms[0].ID != "vm-1" || vms[0].Name != "guest1" || vms[0].PoolID != pool || vms[0].HostID != host.HostID || vms[0].Status != "succeeded" || vms[0].Spec == nil || vms[0].Spec.MediaID != "iso-1" || vms[0].Spec.PoolID != pool {
		t.Fatalf("vms %+v", vms)
	}
	// Re-reporting creates nothing; an ID already held by another resource is skipped.
	managed.Storage = append(managed.Storage, protocol.StorageRegistration{ID: "net-default", Name: "Clash", Path: "/other"})
	if response := report(3); len(response.Commands) != 0 {
		t.Fatalf("re-report dispatched %+v", response.Commands)
	}
	load()
	if len(storage) != 2 || len(networks) != 2 || len(media) != 1 || len(vms) != 1 {
		t.Fatalf("re-report changed resources: %d %d %d %d", len(storage), len(networks), len(media), len(vms))
	}
	stillLocal := false
	for _, s := range storage {
		if s.ID == "stor-1" {
			stillLocal = true
		}
	}
	if !stillLocal {
		t.Fatalf("adopted storage lost: %+v", storage)
	}
	// Adopted references resolve for new work on the same host.
	v := createVMRequest{RequestID: "vm2", VMCreateSpec: protocol.VMCreateSpec{PoolID: pool, HostID: host.HostID, Name: "guest2", StorageID: "stor-1", NetworkID: "net-vlan15", MediaID: "iso-1", CPUs: 2, MemoryMiB: 1024, DiskGiB: 10, Firmware: "bios"}}
	a.call("POST", "/api/v1/vms", v, nil, 202)
	if commands := report(4).Commands; len(commands) != 1 || commands[0].VM == nil || commands[0].VM.Name != "guest2" {
		t.Fatalf("new VM command %+v", commands)
	}
}
