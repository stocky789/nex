// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// vmSpec is a valid specification for the full VM shape: one disk, one automatic-MAC
// interface, and the install ISO first in the boot order when one is selected.
func vmSpec(pool, host, name, storage, network, media string) protocol.VMCreateSpec {
	v := protocol.VMCreateSpec{PoolID: pool, HostID: host, Name: name, Description: "lab guest", GuestOS: "linux",
		CPU:    protocol.VMCPU{Count: 2, Model: "host-passthrough"},
		Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 1024},
		Disks:  []protocol.VMDisk{{StorageID: storage, Name: "system", SizeGiB: 10, Bus: "virtio", Thin: true, BootOrder: 1}},
		NICs:   []protocol.VMNIC{{NetworkID: network, Model: "virtio"}},
		Boot:   protocol.VMBoot{Firmware: "uefi", Machine: "q35"}}
	if media != "" {
		v.MediaID, v.Boot.MediaBootOrder, v.Disks[0].BootOrder = media, 1, 2
	}
	return v
}

// createdVM is the create response: the assigned identity and its durable operation.
type createdVM struct {
	VMID      string             `json:"vm_id"`
	Operation protocol.Operation `json:"operation"`
}

// managedLab is an enrolled managed host with its automatic ISO storage and untagged network
// verified, so VM specifications have references to resolve.
type managedLab struct {
	a                      *testClient
	t                      *testing.T
	cert                   *x509.Certificate
	host                   protocol.EnrollResponse
	caps                   []string
	seq                    uint64
	pool, storage, network string
}

func newManagedLab(t *testing.T, caps ...string) *managedLab {
	t.Helper()
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	l := &managedLab{a: a, t: t, cert: certificate(t, host.CertificatePEM), host: host, caps: caps, pool: a.poolOf(host.HostID)}
	var done []protocol.Result
	for _, cmd := range l.report().Commands {
		done = append(done, protocol.Result{OperationID: cmd.OperationID, Status: "succeeded"})
	}
	l.report(done...)
	var storage []protocol.StorageRegistration
	var networks []protocol.NetworkRegistration
	read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &storage)
	read(t, a.call("GET", "/api/v1/networks", nil, nil, 200), &networks)
	if len(storage) != 1 || storage[0].Status != "succeeded" || len(networks) != 1 || networks[0].Status != "succeeded" {
		t.Fatalf("lab storage %+v networks %+v", storage, networks)
	}
	l.storage, l.network = storage[0].ID, networks[0].ID
	return l
}
func (l *managedLab) report(results ...protocol.Result) protocol.ReportResponse {
	l.t.Helper()
	l.seq++
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}}
	inv := &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, NICs: nics}
	return l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv, Results: results})
}
func (l *managedLab) spec(name string) protocol.VMCreateSpec {
	return vmSpec(l.pool, l.host.HostID, name, l.storage, l.network, "")
}
func (l *managedLab) create(request string, v protocol.VMCreateSpec, want int) *httptest.ResponseRecorder {
	l.t.Helper()
	return l.a.call("POST", "/api/v1/vms", createVMRequest{RequestID: request, VMCreateSpec: v}, nil, want)
}
func (l *managedLab) operations() []protocol.Operation {
	l.t.Helper()
	var out []protocol.Operation
	read(l.t, l.a.call("GET", "/api/v1/operations", nil, nil, 200), &out)
	return out
}

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
	// Declared length beyond free staging space is refused before any byte is read.
	mediaDir := filepath.Join(a.c.cfg.DataDir, "media")
	var fs syscall.Statfs_t
	if err := syscall.Statfs(mediaDir, &fs); err != nil {
		t.Fatal(err)
	}
	budget := int64(fs.Bavail)*int64(fs.Bsize) - mediaStagingReserve
	if budget < 32774 {
		t.Skipf("media staging needs over %d bytes free", mediaStagingReserve+32774)
	}
	oversized := httptest.NewRequest("POST", "https://nex.test"+uploadPath, nil)
	oversized.ContentLength = budget + 1
	oversized.AddCookie(a.cookie)
	oversized.Header.Set("X-CSRF-Token", a.csrf)
	oversizedResponse := httptest.NewRecorder()
	a.c.Handler().ServeHTTP(oversizedResponse, oversized)
	if oversizedResponse.Code != 507 {
		t.Fatalf("declared length over budget: %d %s", oversizedResponse.Code, oversizedResponse.Body.String())
	}

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
	v := createVMRequest{RequestID: "vm1", VMCreateSpec: vmSpec(pool, host.HostID, "guest1", storage.ID, network.ID, media.ID)}
	var created createdVM
	read(t, a.call("POST", "/api/v1/vms", v, nil, 202), &created)
	sequence++
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: time.Now().UTC()}}, VMs: []protocol.VM{{ID: created.VMID, Name: "guest1", State: "shut off"}}}})
	a.call("POST", "/api/v1/appliances/adopt", applianceRequest{RequestID: "adopt-guest", PoolID: pool, HostID: host.HostID, VMID: created.VMID, Name: "guest1"}, nil, 409)
	if !applianceUUID(created.VMID) {
		t.Fatal("invalid stable uuid")
	}
	var replay createdVM
	read(t, a.call("POST", "/api/v1/vms", v, nil, 200), &replay)
	if replay.VMID != created.VMID || replay.Operation.ID != created.Operation.ID {
		t.Fatalf("replay created a second VM operation: %+v", replay)
	}
	// Nex Core attaches the ISO from one of the VM's own storage targets.
	elsewhere := createVMRequest{RequestID: "vm-iso-elsewhere", VMCreateSpec: vmSpec(pool, host.HostID, "guest2", auto.Commands[0].Storage.ID, network.ID, media.ID)}
	if w := a.call("POST", "/api/v1/vms", elsewhere, nil, 409); !strings.Contains(w.Body.String(), "disk storage targets") {
		t.Fatal(w.Body.String())
	}
	v.CPU.Count = 3
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
	// The list exposes the shape the VM was created with, including its automatic MAC.
	s := list[0].Spec
	if s == nil || len(s.Disks) != 1 || s.Disks[0].Name != "system" || s.Disks[0].SizeGiB != 10 || s.Disks[0].Bus != "virtio" || !s.Disks[0].Thin || s.Disks[0].BootOrder != 2 ||
		len(s.NICs) != 1 || s.NICs[0].NetworkID != network.ID || s.NICs[0].Model != "virtio" || s.NICs[0].MAC != "" ||
		s.CPU.Count != 2 || s.CPU.Model != "host-passthrough" || s.Memory.Mode != "dedicated" || s.Memory.SizeMiB != 1024 ||
		s.Boot.Firmware != "uefi" || s.Boot.Machine != "q35" || s.Boot.MediaBootOrder != 1 || s.MediaID != media.ID || s.GuestOS != "linux" {
		t.Fatalf("created shape missing from VM list %+v", s)
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
	// A declared length beyond the staging budget is refused before any byte is read.
	l := newManagedLab(t, protocol.ManagedVMCapability)
	r := httptest.NewRequest("POST", "https://nex.test/api/v1/media?name=x.iso&storage_id="+l.storage+"&request_id=x", nil)
	r.AddCookie(l.a.cookie)
	r.Header.Set("X-CSRF-Token", l.a.csrf)
	r.ContentLength = 1 << 62
	w := httptest.NewRecorder()
	l.a.c.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusInsufficientStorage {
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
	// The journal's start-after-creation belongs to a creation that already finished; a
	// rebuilt Controller must adopt the VM without powering it on.
	adopted := vmSpec("old-pool", "old-host", "guest1", "stor-1", "net-default", "iso-1")
	adopted.ID, adopted.StartAfterCreate = "vm-1", true
	// Every operator-visible choice is pool-scoped state the master owns, so a rebuilt
	// Controller must recover the operator's disk label, adapter model and MTU too, not just
	// the VM's identity and its storage/network references.
	adopted.Disks[0].Name = "corepool-guest1"
	adopted.NICs = []protocol.VMNIC{{NetworkID: "net-default", Model: "virtio", MTU: 9000}, {NetworkID: "net-vlan15", Model: "vmxnet3", MAC: "52:54:00:aa:bb:cc"}}
	managed := &protocol.ManagedState{
		Storage: []protocol.StorageRegistration{{ID: "stor-1", PoolID: "old-pool", HostID: "old-host", Name: "Local", Path: "/var/lib/nex/vms"}},
		Networks: []protocol.NetworkRegistration{
			{ID: "net-default", PoolID: "old-pool", HostID: "old-host", Name: "nex0 untagged", Kind: "bridge", Source: "br0", Uplink: "nex0"},
			{ID: "net-vlan15", PoolID: "old-pool", HostID: "old-host", Name: "vlan15", Kind: "vlan", Source: "nex0v15", Uplink: "nex0", VLAN: 15},
			{ID: "bad id!", Name: "bogus", Kind: "vlan", Source: "wrong", Uplink: "nex0", VLAN: 7},
		},
		Media: []protocol.Media{{ID: "iso-1", PoolID: "old-pool", HostID: "old-host", StorageID: "stor-1", Name: "guest.iso", SizeBytes: 65536, SHA256: sha}},
		VMs:   []protocol.VMCreateSpec{adopted},
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
	if len(vms) != 1 || vms[0].ID != "vm-1" || vms[0].Name != "guest1" || vms[0].PoolID != pool || vms[0].HostID != host.HostID || vms[0].Status != "succeeded" || vms[0].Spec == nil || vms[0].Spec.MediaID != "iso-1" || vms[0].Spec.PoolID != pool ||
		len(vms[0].Spec.Disks) != 1 || vms[0].Spec.Disks[0].StorageID != "stor-1" || len(vms[0].Spec.NICs) != 2 || vms[0].Spec.NICs[0].NetworkID != "net-default" || vms[0].Spec.StartAfterCreate {
		t.Fatalf("vms %+v", vms)
	}
	if s := vms[0].Spec; s.Disks[0].Name != "corepool-guest1" || s.NICs[0].Model != "virtio" || s.NICs[0].MTU != 9000 || s.NICs[1].Model != "vmxnet3" || s.NICs[1].MAC != "52:54:00:aa:bb:cc" {
		t.Fatalf("adoption lost the operator's disk label, adapter model or MTU: %+v", s)
	}
	var ops []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	for _, op := range ops {
		if op.Kind == "vm-power" {
			t.Fatalf("adoption powered a VM: %+v", op)
		}
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
	v := createVMRequest{RequestID: "vm2", VMCreateSpec: vmSpec(pool, host.HostID, "guest2", "stor-1", "net-vlan15", "iso-1")}
	a.call("POST", "/api/v1/vms", v, nil, 202)
	if commands := report(4).Commands; len(commands) != 1 || commands[0].VM == nil || commands[0].VM.Name != "guest2" {
		t.Fatalf("new VM command %+v", commands)
	}
}
func TestCreateVMSpecificationReferencesAndScope(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	a := l.a
	// Bounds both sides enforce, surfaced through the API as invalid input.
	for name, bad := range map[string]func(v *protocol.VMCreateSpec){
		"no disks":            func(v *protocol.VMCreateSpec) { v.Disks = nil },
		"nine disks":          func(v *protocol.VMCreateSpec) { addDisks(v, 8) },
		"duplicate disk":      func(v *protocol.VMCreateSpec) { d := v.Disks[0]; d.BootOrder = 0; v.Disks = append(v.Disks, d) },
		"disk name escape":    func(v *protocol.VMCreateSpec) { v.Disks[0].Name = "../escape" },
		"disk too large":      func(v *protocol.VMCreateSpec) { v.Disks[0].SizeGiB = 4096 },
		"disk too small":      func(v *protocol.VMCreateSpec) { v.Disks[0].SizeGiB = 0 },
		"disk bus":            func(v *protocol.VMCreateSpec) { v.Disks[0].Bus = "ide" },
		"no boot device":      func(v *protocol.VMCreateSpec) { v.Disks[0].BootOrder = 0 },
		"boot order negative": func(v *protocol.VMCreateSpec) { v.Disks[0].BootOrder = -1 },
		"boot order clash":    func(v *protocol.VMCreateSpec) { addDisks(v, 1); v.Disks[1].BootOrder = 1 },
		"cpu topology":        func(v *protocol.VMCreateSpec) { v.CPU.Sockets, v.CPU.Cores, v.CPU.Threads = 2, 2, 1 },
		"cpu model":           func(v *protocol.VMCreateSpec) { v.CPU.Model = "Skylake-Server" },
		"cpu floor":           func(v *protocol.VMCreateSpec) { v.CPU.Count = 0 },
		"cpu ceiling":         func(v *protocol.VMCreateSpec) { v.CPU.Count = 65 },
		"memory mode":         func(v *protocol.VMCreateSpec) { v.Memory.Mode = "ballooned" },
		"memory floor":        func(v *protocol.VMCreateSpec) { v.Memory.SizeMiB = 256 },
		"memory ceiling":      func(v *protocol.VMCreateSpec) { v.Memory.SizeMiB = 262145 },
		"firmware":            func(v *protocol.VMCreateSpec) { v.Boot.Firmware = "coreboot" },
		"machine type":        func(v *protocol.VMCreateSpec) { v.Boot.Machine = "../q35" },
		"guest os":            func(v *protocol.VMCreateSpec) { v.GuestOS = "plan9" },
		"description":         func(v *protocol.VMCreateSpec) { v.Description = strings.Repeat("x", 4097) },
		"nine nics":           func(v *protocol.VMCreateSpec) { addNICs(v, 8) },
		"nic model":           func(v *protocol.VMCreateSpec) { v.NICs[0].Model = "pcnet" },
		"nic mtu on e1000":    func(v *protocol.VMCreateSpec) { v.NICs[0].Model, v.NICs[0].MTU = "e1000", 9000 },
		"nic mtu range":       func(v *protocol.VMCreateSpec) { v.NICs[0].MTU = 9217 },
		"multicast mac":       func(v *protocol.VMCreateSpec) { v.NICs[0].MAC = "53:54:00:12:34:56" },
		"upper case mac":      func(v *protocol.VMCreateSpec) { v.NICs[0].MAC = "52:54:00:AB:CD:EF" },
		"short mac":           func(v *protocol.VMCreateSpec) { v.NICs[0].MAC = "52:54:00:12:34" },
		"repeated mac": func(v *protocol.VMCreateSpec) {
			addNICs(v, 1)
			v.NICs[0].MAC, v.NICs[1].MAC = "52:54:00:12:34:56", "52:54:00:12:34:56"
		},
		"media boot order": func(v *protocol.VMCreateSpec) { v.Boot.MediaBootOrder = 2 },
		"supplied id":      func(v *protocol.VMCreateSpec) { v.ID = "chosen-by-the-client" },
		"invalid name":     func(v *protocol.VMCreateSpec) { v.Name = "Guest_1" },
	} {
		v := l.spec("guest1")
		bad(&v)
		l.create("bad-"+name, v, 400)
	}
	l.create("", l.spec("guest1"), 400)
	// References must name a resource this host has verified in this pool.
	for name, bad := range map[string]func(v *protocol.VMCreateSpec){
		"storage": func(v *protocol.VMCreateSpec) { v.Disks[0].StorageID = "absent" },
		"network": func(v *protocol.VMCreateSpec) { v.NICs[0].NetworkID = "absent" },
		"media": func(v *protocol.VMCreateSpec) {
			v.MediaID, v.Boot.MediaBootOrder, v.Disks[0].BootOrder = "absent", 1, 2
		},
	} {
		v := l.spec("guest1")
		bad(&v)
		l.create("missing-"+name, v, 404)
	}
	// A registration the host has not verified yet is not a usable reference either.
	var queued protocol.StorageRegistration
	read(t, a.call("POST", "/api/v1/storage", registrationRequest{RequestID: "extra-storage", PoolID: l.pool, HostID: l.host.HostID, Name: "Extra", Path: "/var/lib/nex/vms"}, nil, 201), &queued)
	unverified := l.spec("guest1")
	unverified.Disks[0].StorageID = queued.ID
	l.create("unverified-storage", unverified, 404)
	over := l.spec("guest1")
	over.CPU.Count = 16
	l.create("over-capacity", over, 409)
	// Two interfaces: one manual address and one derived from the assigned VM identity.
	first := l.spec("guest1")
	first.NICs[0].MAC = "52:54:00:aa:bb:01"
	first.NICs = append(first.NICs, protocol.VMNIC{NetworkID: l.network, Model: "e1000e"})
	var created createdVM
	read(t, l.create("vm1", first, 202), &created)
	if !applianceUUID(created.VMID) {
		t.Fatal("invalid stable uuid")
	}
	for name, mac := range map[string]string{"supplied": "52:54:00:aa:bb:01", "derived": protocol.DerivedMAC(created.VMID, 1)} {
		v := l.spec("guest2")
		v.NICs[0].MAC = mac
		if w := l.create("duplicate-"+name+"-mac", v, 409); !strings.Contains(w.Body.String(), "already used by another VM") {
			t.Fatal(w.Body.String())
		}
	}
	l.create("duplicate-name", l.spec("guest1"), 409)
	// The host must be in the VM's pool, freshly connected and still active.
	other := a.enroll(a.token(a.site(), ""), csr(t))
	wrongPool := l.spec("guest2")
	wrongPool.HostID = other.HostID
	l.create("wrong-pool", wrongPool, 409)
	a.c.mu.Lock()
	a.c.seen[l.host.HostID] = time.Now().Add(-time.Hour)
	a.c.mu.Unlock()
	if w := l.create("stale-host", l.spec("guest2"), 409); !strings.Contains(w.Body.String(), "must be fresh") {
		t.Fatal(w.Body.String())
	}
	l.report()
	l.create("fresh-again", l.spec("guest2"), 202)
	a.call("POST", "/api/v1/hosts/"+l.host.HostID+"/revoke", nil, nil, 200)
	l.create("revoked-host", l.spec("guest3"), 409)
}

// addDisks appends n further valid disks, outside the boot order.
func addDisks(v *protocol.VMCreateSpec, n int) {
	for i := 0; i < n; i++ {
		d := v.Disks[0]
		d.Name, d.BootOrder = fmt.Sprintf("data%d", i), 0
		v.Disks = append(v.Disks, d)
	}
}

// addNICs appends n further automatic-MAC interfaces on the same network.
func addNICs(v *protocol.VMCreateSpec, n int) {
	for i := 0; i < n; i++ {
		v.NICs = append(v.NICs, v.NICs[0])
	}
}

func TestStartAfterCreateIssuesTheExistingPowerStart(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	start := func(name string) protocol.VMCreateSpec {
		v := l.spec(name)
		v.StartAfterCreate = true
		return v
	}
	// A creation that does not succeed is never started.
	var failed createdVM
	read(t, l.create("vm-failed", start("guest-failed"), 202), &failed)
	l.report()
	if cmds := l.report(protocol.Result{OperationID: failed.Operation.ID, Status: "failed", Error: "disk allocation failed"}).Commands; len(cmds) != 0 {
		t.Fatalf("failed creation dispatched %+v", cmds)
	}
	// A host that cannot accept central power requests still creates the VM and records why
	// the start was not performed, without retrying it on every report.
	var unpowered createdVM
	read(t, l.create("vm-unpowered", start("guest-unpowered"), 202), &unpowered)
	l.report()
	if cmds := l.report(protocol.Result{OperationID: unpowered.Operation.ID, Status: "succeeded"}).Commands; len(cmds) != 0 {
		t.Fatalf("power dispatched to a host without the capability %+v", cmds)
	}
	refused := starts(l.operations())["start-after-create:"+unpowered.VMID]
	if refused.Status != "failed" || refused.Actor != "system" || !strings.Contains(refused.Error, "does not support central power requests") {
		t.Fatalf("refused start %+v", refused)
	}
	// The capability arriving later does not resurrect a start the operator was told to make.
	l.caps = append(l.caps, protocol.PowerCapability)
	if cmds := l.report().Commands; len(cmds) != 0 {
		t.Fatalf("refused start retried %+v", cmds)
	}
	var created createdVM
	read(t, l.create("vm-started", start("guest-started"), 202), &created)
	l.report()
	cmds := l.report(protocol.Result{OperationID: created.Operation.ID, Status: "succeeded"}).Commands
	if len(cmds) != 1 || cmds[0].Kind != "vm-power" || cmds[0].Power == nil || cmds[0].Power.VMID != created.VMID || cmds[0].Power.Action != "start" ||
		cmds[0].Power.HostID != l.host.HostID || cmds[0].Power.PoolID != l.pool {
		t.Fatalf("follow-on start %+v", cmds)
	}
	issued := starts(l.operations())
	if len(issued) != 2 || issued["start-after-create:"+created.VMID].Status != "dispatched" {
		t.Fatalf("start operations %+v", issued)
	}
	if _, wrong := issued["start-after-create:"+failed.VMID]; wrong {
		t.Fatal("a failed creation was started")
	}
	// Every VM was created; only the last one was started.
	var vms []managedAppliance
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 3 {
		t.Fatalf("VM list %+v", vms)
	}
}

// starts indexes the follow-on start operations by their stable request_id.
func starts(ops []protocol.Operation) map[string]protocol.Operation {
	out := map[string]protocol.Operation{}
	for _, op := range ops {
		if op.Kind == "vm-power" {
			out[op.RequestID] = op
		}
	}
	return out
}

// Dedicated memory is a promise, so the host admission-controls it against what it has already
// promised. Both independent reviews found this unimplemented: five 16 GiB dedicated VMs were
// accepted on a 16 GiB host while the wizard, guide and API reference all told the operator the
// memory was reserved and never given to another VM. Maximum mode reserves nothing and must
// still be allowed past the same point.
func TestDedicatedMemoryIsAdmissionControlled(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	// The lab host reports 16 GiB; 2 GiB is reserved for the host itself.
	first := l.spec("dedicated1")
	first.Memory = protocol.VMMemory{Mode: "dedicated", SizeMiB: 8192}
	l.create("req-dedicated-1", first, 202)

	// 8192 already promised + 8192 + 2048 reserve > 16384: the host cannot promise it.
	second := l.spec("dedicated2")
	second.Memory = protocol.VMMemory{Mode: "dedicated", SizeMiB: 8192}
	if body := l.create("req-dedicated-2", second, 409).Body.String(); !strings.Contains(body, "left to promise") {
		t.Fatalf("second dedicated VM was not refused for capacity: %s", body)
	}

	// Same size in maximum mode reserves nothing and is allowed to oversubscribe.
	relaxed := l.spec("maximum1")
	relaxed.Memory = protocol.VMMemory{Mode: "maximum", SizeMiB: 8192}
	l.create("req-maximum-1", relaxed, 202)

	// A maximum VM neither consumes the dedicated allowance nor frees it: what still fits
	// dedicated is bounded by the dedicated commitments alone. 8192 promised + 2048 reserve
	// leaves exactly 6144, so that is the boundary in both directions.
	over := l.spec("dedicated3")
	over.Memory = protocol.VMMemory{Mode: "dedicated", SizeMiB: 6145}
	if body := l.create("req-dedicated-3", over, 409).Body.String(); !strings.Contains(body, "left to promise") {
		t.Fatalf("dedicated VM one MiB over the remaining promise was accepted: %s", body)
	}
	fits := l.spec("dedicated4")
	fits.Memory = protocol.VMMemory{Mode: "dedicated", SizeMiB: 6144}
	l.create("req-dedicated-4", fits, 202)
}

// A replaced Controller rebuilds the appliance row from the host's deploy journal, records
// the host's failed and interrupted operations as task history, and never skips an entry
// silently: invalid, conflicting, unresolved and libvirt-missing entries each leave one
// needs_attention operation and an audit entry, resolved once the entry is adopted.
func TestAdoptionRebuildsApplianceOutcomesAndAttention(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	pool := a.poolOf(host.HostID)
	appliance := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	guest := vmSpec("old-pool", "old-host", "guest1", "stor-1", "net-default", "")
	guest.ID = "12345678-1234-4234-8234-123456789012"
	gone := vmSpec("old-pool", "old-host", "gone", "stor-1", "net-default", "")
	gone.ID = "12345678-1234-4234-8234-123456789013"
	managed := &protocol.ManagedState{
		Storage:    []protocol.StorageRegistration{{ID: "stor-1", Name: "Local", Path: "/var/lib/nex/vms"}},
		Networks:   []protocol.NetworkRegistration{{ID: "net-default", Name: "nex0 untagged", Kind: "bridge", Source: "br0", Uplink: "nex0"}, {ID: "bad id!", Name: "bogus", Kind: "vlan", Source: "wrong", Uplink: "nex0", VLAN: 7}},
		Media:      []protocol.Media{{ID: "iso-orphan", StorageID: "stor-missing", Name: "orphan.iso", SizeBytes: 65536, SHA256: strings.Repeat("ab", 32)}},
		VMs:        []protocol.VMCreateSpec{guest, gone},
		MissingVMs: []string{gone.ID},
		Appliances: []protocol.Appliance{{ID: appliance, Name: "nex-controller", RequestID: "deploy-1", Role: "controller"}},
		Outcomes: []protocol.ManagedOutcome{
			{OperationID: "op-failed", Kind: "storage-register", ID: "stor-x", Name: "Broken", Status: "failed", Error: "storage is not writable"},
			{OperationID: "op-stuck", Kind: "vm-create", ID: "12345678-1234-4234-8234-123456789014", Name: "stuck", Status: "needs_attention", Error: "disk allocation interrupted"},
			{OperationID: "bad op!", Kind: "vm-create", ID: "x", Status: "failed"},
		},
	}
	now := time.Now().UTC()
	inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, Managed: managed,
		NICs:   []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}},
		VMs:    []protocol.VM{{ID: appliance, Name: "nex-controller", State: "running"}, {ID: guest.ID, Name: "guest1", State: "shut off"}},
		Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
	report := func(seq uint64) protocol.ReportResponse {
		return a.report(cert, protocol.ReportRequest{Version: 1, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	}
	report(1)
	var vms []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	found := map[string]managedAppliance{}
	for _, v := range vms {
		found[v.ID] = v
	}
	if v := found[appliance]; v.Role != "controller" || !v.Protected || v.PoolID != pool || v.HostID != host.HostID || v.Name != "nex-controller" || v.State != "running" {
		t.Fatalf("appliance not rebuilt from the host report: %+v", vms)
	}
	if v := found[guest.ID]; v.Status != "succeeded" || v.PoolID != pool {
		t.Fatalf("guest not adopted: %+v", vms)
	}
	if _, ok := found[gone.ID]; ok || len(vms) != 2 {
		t.Fatalf("VM missing from libvirt was adopted: %+v", vms)
	}
	// The rebuilt row is what the handoff would have created, so its adoption call replays.
	a.call("POST", "/api/v1/appliances/adopt", applianceRequest{RequestID: "deploy-1", HostID: host.HostID, VMID: appliance, Name: "nex-controller"}, nil, 200)
	a.call("POST", "/api/v1/appliances/adopt", applianceRequest{RequestID: "deploy-2", HostID: host.HostID, VMID: appliance, Name: "nex-controller"}, nil, 409)
	var ops []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	byRequest := map[string]protocol.Operation{}
	for _, op := range ops {
		byRequest[op.RequestID] = op
	}
	if op := byRequest["adopt-outcome:"+host.HostID+":op-failed"]; op.ID != "op-failed" || op.Kind != "storage-register" || op.Status != "failed" || op.Error != "storage is not writable" || op.Actor != "system" || op.HostID != host.HostID {
		t.Fatalf("failed outcome not adopted: %+v", ops)
	}
	if op := byRequest["adopt-outcome:"+host.HostID+":op-stuck"]; op.Status != "needs_attention" || op.Kind != "vm-create" {
		t.Fatalf("needs_attention outcome not adopted: %+v", ops)
	}
	attention := func(key string) protocol.Operation {
		op, ok := byRequest["adopt-attention:"+host.HostID+":"+key]
		if !ok || op.Status != "needs_attention" || op.Actor != "system" || !strings.HasPrefix(op.Error, "not adopted from host report: ") {
			t.Fatalf("no needs_attention record for %s: %+v", key, ops)
		}
		return op
	}
	attention("network:bad id!")
	attention("media:iso-orphan")
	if op := attention("vm:" + gone.ID); !strings.Contains(op.Error, "missing from libvirt") {
		t.Fatalf("missing VM reason: %+v", op)
	}
	for _, op := range ops {
		if op.RequestID == "adopt-outcome:"+host.HostID+":bad op!" {
			t.Fatalf("invalid outcome adopted: %+v", op)
		}
	}
	var audits []protocol.Audit
	read(t, a.call("GET", "/api/v1/audit", nil, nil, 200), &audits)
	actions := map[string]int{}
	reasons := ""
	for _, e := range audits {
		actions[e.Action]++
		reasons += e.Detail + "\n"
	}
	if actions["appliance.adopt"] != 1 || actions["adopt.outcome"] != 2 || actions["adopt.attention"] != 3 || !strings.Contains(reasons, "bogus") || !strings.Contains(reasons, "stor-missing") {
		t.Fatalf("audit %v\n%s", actions, reasons)
	}
	// Every later report is steady state: nothing duplicated, nothing new recorded.
	inv.ObservedAt = now.Add(time.Second)
	report(2)
	report(3)
	var again []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &again)
	if len(again) != len(ops) {
		t.Fatalf("repeated reports changed operations: %d -> %d", len(ops), len(again))
	}
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 2 {
		t.Fatalf("repeated reports duplicated VMs: %+v", vms)
	}
	// Once the host resolves an entry (the ISO's storage arrives), its attention record closes.
	managed.Storage = append(managed.Storage, protocol.StorageRegistration{ID: "stor-missing", Name: "Second", Path: "/var/lib/nex/vms2"})
	inv.ObservedAt = now.Add(2 * time.Second)
	report(4)
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &again)
	for _, op := range again {
		if op.RequestID == "adopt-attention:"+host.HostID+":media:iso-orphan" && (op.Status != "succeeded" || op.Error != "") {
			t.Fatalf("resolved entry still needs attention: %+v", op)
		}
	}
	var media []protocol.Media
	read(t, a.call("GET", "/api/v1/media", nil, nil, 200), &media)
	if len(media) != 1 || media[0].ID != "iso-orphan" || media[0].Status != "succeeded" {
		t.Fatalf("media not adopted after its storage: %+v", media)
	}
	// The appliance row follows the host when it re-enrolls under a new identity after revocation.
	a.call("POST", "/api/v1/hosts/"+host.HostID+"/revoke", nil, nil, 200)
	second := a.enroll(a.token(a.site(), ""), csr(t))
	inv.ObservedAt = now.Add(3 * time.Second)
	a.report(certificate(t, second.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	moved := false
	for _, v := range vms {
		if v.ID == appliance && v.HostID == second.HostID && v.PoolID == a.poolOf(second.HostID) {
			moved = true
		}
	}
	if !moved {
		t.Fatalf("appliance did not follow the re-enrolled host: %+v", vms)
	}
}
