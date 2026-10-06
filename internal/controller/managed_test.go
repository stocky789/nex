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

// hostDefaults is what a Core self-registers in its journal and reports for adoption: the
// default ISO storage and the untagged network of its bridge on a physical port. The pool is
// assigned on adoption, so the host's own identity stands in for it.
func hostDefaults(host string) *protocol.ManagedState {
	return &protocol.ManagedState{
		Storage:  []protocol.StorageRegistration{{ID: "host-iso", PoolID: host, HostID: host, Name: "ISO", Path: "/var/lib/libvirt/images"}},
		Networks: []protocol.NetworkRegistration{{ID: "host-untagged", PoolID: host, HostID: host, Name: "nex0 untagged", Kind: "bridge", Source: "br0", Uplink: "nex0"}},
	}
}

// managedLab is an enrolled managed host whose self-registered ISO storage and untagged
// network are mirrored, so VM specifications have references to resolve.
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
	if cmds := l.report().Commands; len(cmds) != 0 {
		t.Fatalf("host-reported defaults were dispatched: %+v", cmds)
	}
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
	inv := &protocol.Inventory{Hostname: "core", CPUCount: 8, CPUModels: []string{"Skylake-Server"}, MemoryBytes: 16 << 30, NICs: nics, Managed: hostDefaults(l.host.HostID)}
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
		iso := hostDefaults(host.HostID)
		iso.Networks = nil
		v := protocol.ReportRequest{Version: 1, Sequence: sequence, Results: results, Inventory: &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, Managed: iso}}
		if caps {
			v.Capabilities = []string{protocol.ManagedVMCapability}
		}
		return a.report(cert, v)
	}
	req := registrationRequest{RequestID: "storage1", PoolID: pool, HostID: host.HostID, Name: "Local", Path: "/var/lib/nex/vms"}
	a.call("POST", "/api/v1/storage", req, nil, 409)
	// The host's own ISO storage is mirrored from its journal, never issued as a command.
	if cmds := report(true).Commands; len(cmds) != 0 {
		t.Fatalf("host default dispatched %#v", cmds)
	}
	var mirrored []protocol.StorageRegistration
	read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &mirrored)
	if len(mirrored) != 1 || mirrored[0].ID != "host-iso" || mirrored[0].Name != "ISO" || mirrored[0].Status != "succeeded" || mirrored[0].PoolID != pool {
		t.Fatalf("mirrored ISO storage %+v", mirrored)
	}
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
		t.Fatal("agent without the capability received managed command")
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
	// Free space can grow between Statfs calls when another test cleans up.
	oversized.ContentLength = 1 << 62
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
	// The list exposes the shape the VM was created with; an automatic MAC is reported as the
	// effective derived address so an edit can name the interface.
	s := list[0].Spec
	if s == nil || len(s.Disks) != 1 || s.Disks[0].Name != "system" || s.Disks[0].SizeGiB != 10 || s.Disks[0].Bus != "virtio" || !s.Disks[0].Thin || s.Disks[0].BootOrder != 2 ||
		len(s.NICs) != 1 || s.NICs[0].NetworkID != network.ID || s.NICs[0].Model != "virtio" || s.NICs[0].MAC != protocol.DerivedMAC(created.VMID, 0) ||
		s.CPU.Count != 2 || s.CPU.Model != "host-passthrough" || s.Memory.Mode != "dedicated" || s.Memory.SizeMiB != 1024 ||
		s.Boot.Firmware != "uefi" || s.Boot.Machine != "q35" || s.Boot.MediaBootOrder != 1 || s.MediaID != media.ID || s.GuestOS != "linux" {
		t.Fatalf("created shape missing from VM list %+v", s)
	}
	// A running VM's guest observation is passed through from the fresh host report. The live
	// CD drive travels with it: a guest can empty its own tray while the VM keeps the media
	// Nex recorded, and only the host can report that difference.
	guest := &protocol.GuestInfo{Agent: "connected", OS: "Debian GNU/Linux 13 (trixie)", OSID: "debian", Hostname: "guest1", Addresses: []protocol.GuestAddress{{Interface: "enp1s0", Address: "192.168.122.10/24"}}, Clipboard: "ready", Resize: true, Media: protocol.MediaEmpty}
	inventory := func(g *protocol.GuestInfo) *protocol.Inventory {
		return &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: time.Now().UTC()}}, VMs: []protocol.VM{{ID: created.VMID, Name: "guest1", State: "running", Guest: g}}}
	}
	sequence++
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inventory(guest)})
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &list)
	if len(list) != 1 || list[0].State != "running" || !reflect.DeepEqual(list[0].Guest, guest) {
		t.Fatalf("guest observation missing from VM list %#v", list[0].Guest)
	}
	if list[0].Spec == nil || list[0].Spec.MediaID != media.ID {
		t.Fatalf("an ejected tray must not change the recorded media %#v", list[0].Spec)
	}
	// An unknown observation is refused like any other guest value; a Core that does not read the drive sends none.
	unknown := *guest
	unknown.Media = "exploding"
	sequence++
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inventory(&unknown)}, cert, 400)
	withoutMedia := *guest
	withoutMedia.Media = ""
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inventory(&withoutMedia)})
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
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical"}, {Name: "br0", Kind: "bridge"}, {Name: "nex1", Kind: "physical"}, {Name: "kindless"}}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "core", NICs: nics}})
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
	// The same uplink/VLAN, or a plain registration of the derived bridge, is one host
	// resource: the host holds the authoritative record and reports the conflict as a failed
	// task; the Controller queues the request and shows what the host answered.
	dup := base
	dup.RequestID = "vlan-dup"
	dup.Name = "Servers again"
	var dupNetwork, plainNetwork protocol.NetworkRegistration
	read(t, a.call("POST", "/api/v1/networks", dup, nil, 201), &dupNetwork)
	plain := registrationRequest{RequestID: "bridge-dup", PoolID: pool, HostID: host.HostID, Name: "Manual", Kind: "bridge", Source: "nex1v100"}
	read(t, a.call("POST", "/api/v1/networks", plain, nil, 201), &plainNetwork)
	reason := "network nex1v100 is already registered on this host as Servers"
	if cmds := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 4, Capabilities: []string{protocol.ManagedVMCapability}}).Commands; len(cmds) != 3 {
		t.Fatalf("duplicate registrations are the host's to refuse; dispatched %+v", cmds)
	}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 5, Capabilities: []string{protocol.ManagedVMCapability}, Results: []protocol.Result{
		{OperationID: dupNetwork.OperationID, Status: "failed", Error: reason}, {OperationID: plainNetwork.OperationID, Status: "failed", Error: reason}}})
	// Untagged on the NIC reported without a kind, and a second tag on the same uplink, are distinct.
	other := base
	other.RequestID, other.Name, other.Uplink, other.VLAN = "vlan-kindless", "Untagged", "kindless", 0
	read(t, a.call("POST", "/api/v1/networks", other, nil, 201), &network)
	if network.Source != "kindlessv0" {
		t.Fatal(network.Source)
	}
	var list []protocol.NetworkRegistration
	read(t, a.call("GET", "/api/v1/networks", nil, nil, 200), &list)
	byName := map[string]protocol.NetworkRegistration{}
	for _, n := range list {
		byName[n.Name] = n
	}
	if len(list) != 4 || byName["Servers"].VLAN != 100 || byName["Untagged"].Uplink != "kindless" || byName["Servers again"].Status != "failed" || byName["Manual"].Error != reason {
		t.Fatalf("list %+v", list)
	}
}

// The host owns its default ISO storage and untagged networks: it self-registers them in its
// journal and the Controller only mirrors them, so a report never turns into a command and an
// agent without managed-VM support gets nothing.
func TestHostReportedDefaultsAreMirroredNotIssued(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	pool := a.poolOf(host.HostID)
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}}
	report := func(seq uint64, caps bool) protocol.ReportResponse {
		v := protocol.ReportRequest{Version: 1, Sequence: seq, Inventory: &protocol.Inventory{Hostname: "core", NICs: nics, Managed: hostDefaults(host.HostID)}}
		if caps {
			v.Capabilities = []string{protocol.ManagedVMCapability}
		}
		return a.report(cert, v)
	}
	var networks []protocol.NetworkRegistration
	var storage []protocol.StorageRegistration
	load := func() {
		read(t, a.call("GET", "/api/v1/networks", nil, nil, 200), &networks)
		read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &storage)
	}
	report(1, false)
	load()
	if len(networks) != 0 || len(storage) != 0 {
		t.Fatal("agent without the capability must not get resources")
	}
	for seq := uint64(2); seq < 4; seq++ {
		if cmds := report(seq, true).Commands; len(cmds) != 0 {
			t.Fatalf("host defaults dispatched as commands: %+v", cmds)
		}
		load()
		if len(networks) != 1 || networks[0].ID != "host-untagged" || networks[0].Kind != "bridge" || networks[0].Source != "br0" || networks[0].Uplink != "nex0" || networks[0].Name != "nex0 untagged" || networks[0].PoolID != pool || networks[0].HostID != host.HostID || networks[0].Status != "succeeded" {
			t.Fatalf("mirrored network %+v", networks)
		}
		if len(storage) != 1 || storage[0].ID != "host-iso" || storage[0].Name != "ISO" || storage[0].Path != "/var/lib/libvirt/images" || storage[0].PoolID != pool || storage[0].Status != "succeeded" {
			t.Fatalf("mirrored storage %+v", storage)
		}
	}
	// A manual VLAN on the same uplink still works alongside the host's default network.
	read(t, a.call("POST", "/api/v1/networks", registrationRequest{RequestID: "vlan15", PoolID: pool, HostID: host.HostID, Name: "vlan15", Kind: "vlan", Uplink: "nex0", VLAN: 15}, nil, 201), &networks[0])
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
	adopted := vmSpec("old-pool", "old-host", " QA Web.example – 東京 ", "stor-1", "net-default", "iso-1")
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
	if response := report(1); len(response.Commands) != 0 {
		t.Fatalf("adopted entries must not be dispatched: %+v", response.Commands)
	}
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
	if len(storage) != 1 || storage[0].ID != "stor-1" || storage[0].Name != "Local" || storage[0].PoolID != pool || storage[0].HostID != host.HostID || storage[0].Status != "succeeded" {
		t.Fatalf("adopted storage %+v", storage)
	}
	if len(networks) != 2 || networks[0].ID != "net-default" || networks[0].Name != "nex0 untagged" || networks[0].Uplink != "nex0" || networks[0].Status != "succeeded" ||
		networks[1].ID != "net-vlan15" || networks[1].VLAN != 15 || networks[1].PoolID != pool || networks[1].HostID != host.HostID || networks[1].Status != "succeeded" {
		t.Fatalf("networks %+v", networks)
	}
	if len(media) != 1 || media[0].ID != "iso-1" || media[0].StorageID != "stor-1" || media[0].PoolID != pool || media[0].HostID != host.HostID || media[0].SHA256 != sha || media[0].Status != "succeeded" {
		t.Fatalf("media %+v", media)
	}
	if len(vms) != 1 || vms[0].ID != "vm-1" || vms[0].Name != adopted.Name || vms[0].PoolID != pool || vms[0].HostID != host.HostID || vms[0].Status != "succeeded" || vms[0].Spec == nil || vms[0].Spec.MediaID != "iso-1" || vms[0].Spec.PoolID != pool ||
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
	if len(storage) != 1 || storage[0].ID != "stor-1" || len(networks) != 2 || len(media) != 1 || len(vms) != 1 {
		t.Fatalf("re-report changed resources: %+v %d %d %d", storage, len(networks), len(media), len(vms))
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
		"cpu model":           func(v *protocol.VMCreateSpec) { v.CPU.Model = "Skylake Server" },
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
		"repeated boot": func(v *protocol.VMCreateSpec) { v.Boot.MediaBootOrder = v.Disks[0].BootOrder },
		"supplied id":   func(v *protocol.VMCreateSpec) { v.ID = "chosen-by-the-client" },
		"invalid name":  func(v *protocol.VMCreateSpec) { v.Name = "Guest/1" },
	} {
		v := l.spec("guest1")
		bad(&v)
		l.create("bad-"+name, v, 400)
	}
	l.create("", l.spec("guest1"), 400)
	// Two interfaces: one manual address and one derived from the assigned VM identity.
	first := l.spec("guest1")
	first.NICs[0].MAC = "52:54:00:aa:bb:01"
	first.NICs = append(first.NICs, protocol.VMNIC{NetworkID: l.network, Model: "e1000e"})
	var created createdVM
	read(t, l.create("vm1", first, 202), &created)
	if !applianceUUID(created.VMID) {
		t.Fatal("invalid stable uuid")
	}
	// A duplicate MAC or name is not the Controller's call: the host checks its journal and
	// libvirt and answers with a failed task, which the Controller records as reported.
	var dup createdVM
	read(t, l.create("duplicate-mac", func() protocol.VMCreateSpec { v := l.spec("guest2"); v.NICs[0].MAC = "52:54:00:aa:bb:01"; return v }(), 202), &dup)
	l.create("duplicate-name", l.spec("guest1"), 202)
	l.report()
	l.report(protocol.Result{OperationID: dup.Operation.ID, Status: "failed", Error: "MAC address 52:54:00:aa:bb:01 is already used by VM guest1 on this host"})
	var vms []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	for _, vm := range vms {
		if vm.ID == dup.VMID {
			t.Fatalf("failed create remained in VM list: %+v", vms)
		}
	}
	var operations []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	for _, operation := range operations {
		if operation.ID == dup.Operation.ID && operation.Status == "failed" && strings.Contains(operation.Error, "already used by VM guest1") {
			goto duplicateRecorded
		}
	}
	t.Fatalf("host-reported conflict missing from task history: %+v", operations)
duplicateRecorded:
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

func TestCreateVMPlacementDoesNotTrustResourceMirrors(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	v := l.spec("master-checked")
	v.Disks[0].StorageID = "not-in-controller-mirror"
	v.NICs[0].NetworkID = "also-not-in-controller-mirror"
	var created createdVM
	read(t, l.create("master-checked", v, 202), &created)
	commands := l.report().Commands
	if len(commands) != 1 || commands[0].VM == nil || commands[0].VM.Disks[0].StorageID != v.Disks[0].StorageID || commands[0].VM.NICs[0].NetworkID != v.NICs[0].NetworkID {
		t.Fatalf("Controller did not relay unmirrored placement request: %+v", commands)
	}
	l.report(protocol.Result{OperationID: created.Operation.ID, Status: "failed", Error: "storage is not available to this pool"})
	var replay createdVM
	read(t, l.create("master-checked", v, 200), &replay)
	if replay.Operation.ID != created.Operation.ID || replay.Operation.Status != "failed" || replay.Operation.Error == "" {
		t.Fatalf("failed Core placement was not safely replayed: %+v", replay)
	}
}

func TestUpdateVMPlacementDoesNotTrustResourceMirrors(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.UpdateCapability)
	var created createdVM
	read(t, l.create("initial", l.spec("initial"), 202), &created)
	initial := *l.report().Commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{initial}}, protocol.Result{OperationID: created.Operation.ID, Status: "succeeded"})
	next := initial
	next.NICs[0].MAC = protocol.NICMAC(created.VMID, 0, initial.NICs[0])
	next.NICs[0].NetworkID = "not-in-controller-mirror"
	l.update("unmirrored-update", created.VMID, next, 202)
	commands := l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{initial}}).Commands
	if len(commands) != 1 || commands[0].VM == nil || commands[0].VM.NICs[0].NetworkID != next.NICs[0].NetworkID {
		t.Fatalf("Controller did not relay unmirrored update: %+v", commands)
	}
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

// start_after_create is honoured by the host inside the creation itself: the Controller
// dispatches the flag with the vm-create command and issues no follow-on power request,
// whatever the creation's result and whether or not the host reports vm-power-v1.
func TestStartAfterCreateIsTheHostsCreation(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	v := l.spec("guest-started")
	v.StartAfterCreate = true
	var created createdVM
	read(t, l.create("vm-started", v, 202), &created)
	cmds := l.report().Commands
	if len(cmds) != 1 || cmds[0].VM == nil || !cmds[0].VM.StartAfterCreate {
		t.Fatalf("creation command must carry start_after_create: %+v", cmds)
	}
	for _, status := range []string{"succeeded", "needs_attention"} {
		l.caps = append(l.caps, protocol.PowerCapability)
		if cmds := l.report(protocol.Result{OperationID: created.Operation.ID, Status: status}).Commands; len(cmds) != 0 {
			t.Fatalf("follow-on dispatched after %s: %+v", status, cmds)
		}
	}
	for _, op := range l.operations() {
		if op.Kind == "vm-power" {
			t.Fatalf("Controller issued a start: %+v", op)
		}
	}
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
	guest := vmSpec("old-pool", "old-host", " QA Web.example – 東京 ", "stor-1", "net-default", "")
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

// A missing outcome is not settlement; only an explicit terminal Core outcome closes it.
func TestAdoptedOutcomeRequiresCoreSettlement(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	now := time.Now().UTC()
	managed := &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{
		{OperationID: "op-stuck", Kind: "vm-update", ID: "12345678-1234-4234-8234-123456789014", Name: "stuck", Status: "needs_attention", Error: "interrupted before completion; retry from a new request"},
		{OperationID: "op-failed", Kind: "storage-register", ID: "stor-x", Name: "Broken", Status: "failed", Error: "storage is not writable"},
	}}
	inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, Managed: managed,
		Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
	status := func() map[string]protocol.Operation {
		var ops []protocol.Operation
		read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
		out := map[string]protocol.Operation{}
		for _, op := range ops {
			out[op.RequestID] = op
		}
		return out
	}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	if op := status()["adopt-outcome:"+host.HostID+":op-stuck"]; op.Status != "needs_attention" {
		t.Fatalf("outcome not adopted: %+v", op)
	}
	managed.Outcomes = managed.Outcomes[1:]
	inv.ObservedAt = now.Add(time.Second)
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	ops := status()
	if op := ops["adopt-outcome:"+host.HostID+":op-stuck"]; op.Status != "needs_attention" {
		t.Fatalf("missing outcome was falsely settled: %+v", op)
	}
	if op := ops["adopt-outcome:"+host.HostID+":op-failed"]; op.Status != "failed" {
		t.Fatalf("reported failure was closed: %+v", op)
	}
	managed.Outcomes = append(managed.Outcomes, protocol.ManagedOutcome{
		OperationID: "op-stuck", Kind: "vm-update", ID: "12345678-1234-4234-8234-123456789014", Status: "succeeded",
	})
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 3, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	if op := status()["adopt-outcome:"+host.HostID+":op-stuck"]; op.Status != "succeeded" || op.Error != "" {
		t.Fatalf("explicit settlement was not adopted: %+v", op)
	}
}

func TestCoreRecoverySettlesControllerOperation(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	seq := uint64(0)
	report := func(results []protocol.Result, outcomes []protocol.ManagedOutcome) protocol.ReportResponse {
		seq++
		return a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability}, Results: results, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Outcomes: outcomes}}})
	}
	report(nil, nil)
	req := storageRegisterRequest{RequestID: "recovered-storage", PoolID: pool, HostID: host.HostID, Name: "Local", Path: "/var/lib/nex/vms"}
	var queued protocol.StorageRegistration
	read(t, a.call("POST", "/api/v1/storage", req, nil, 201), &queued)
	commands := report(nil, nil).Commands
	if len(commands) != 1 || commands[0].OperationID != queued.OperationID {
		t.Fatalf("storage command %#v", commands)
	}
	report([]protocol.Result{{OperationID: queued.OperationID, Status: "needs_attention", Error: "interrupted before completion"}}, nil)
	var operations []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	if len(operations) == 0 || operations[0].ID != queued.OperationID || operations[0].Status != "needs_attention" {
		t.Fatalf("interrupted operation %+v", operations)
	}
	// Outcomes cannot retarget an operation: both kind and reporting host are immutable.
	report(nil, []protocol.ManagedOutcome{{OperationID: queued.OperationID, Kind: "network-register", ID: queued.ID, Status: "succeeded"}})
	var still protocol.StorageRegistration
	read(t, a.call("POST", "/api/v1/storage", req, nil, 200), &still)
	if still.Status != "needs_attention" {
		t.Fatalf("mismatched kind changed operation: %+v", still)
	}
	other := a.enroll(a.token(a.site(), ""), csr(t))
	a.report(certificate(t, other.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{{OperationID: queued.OperationID, Kind: "storage-register", ID: queued.ID, Status: "succeeded"}}}}})
	read(t, a.call("POST", "/api/v1/storage", req, nil, 200), &still)
	if still.Status != "needs_attention" {
		t.Fatalf("cross-host outcome changed operation: %+v", still)
	}
	// A later Core journal recovery is not another transport result. It must still settle the
	// Controller's original operation and make the same client request replay as succeeded.
	report(nil, []protocol.ManagedOutcome{{OperationID: queued.OperationID, Kind: "storage-register", ID: queued.ID, Name: queued.Name, Status: "succeeded"}})
	var replay protocol.StorageRegistration
	read(t, a.call("POST", "/api/v1/storage", req, nil, 200), &replay)
	if replay.Status != "succeeded" || replay.OperationID != queued.OperationID {
		t.Fatalf("Core recovery did not settle replay: %+v", replay)
	}
	report(nil, []protocol.ManagedOutcome{{OperationID: queued.OperationID, Kind: "storage-register", ID: queued.ID, Status: "needs_attention", Error: "stale"}})
	read(t, a.call("POST", "/api/v1/storage", req, nil, 200), &replay)
	if replay.Status != "succeeded" {
		t.Fatalf("stale recovery outcome downgraded success: %+v", replay)
	}
}

// A queued outcome that carries a reason shows it on the still-dispatched task, and the task
// stays dispatched; the reason clears when Core stops reporting one.
func TestQueuedOutcomeReasonShowsOnDispatchedTask(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	seq := uint64(0)
	report := func(outcomes []protocol.ManagedOutcome) protocol.ReportResponse {
		seq++
		return a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Outcomes: outcomes}}})
	}
	report(nil)
	req := storageRegisterRequest{RequestID: "waiting-storage", PoolID: a.poolOf(host.HostID), HostID: host.HostID, Name: "Local", Path: "/var/lib/nex/vms"}
	var queued protocol.StorageRegistration
	read(t, a.call("POST", "/api/v1/storage", req, nil, 201), &queued)
	report(nil)
	task := func() protocol.Operation {
		var ops []protocol.Operation
		read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
		for _, op := range ops {
			if op.ID == queued.OperationID {
				return op
			}
		}
		t.Fatal("operation missing")
		return protocol.Operation{}
	}
	outcome := protocol.ManagedOutcome{OperationID: queued.OperationID, Kind: "storage-register", ID: queued.ID, Status: "queued", Error: "member unreachable"}
	report([]protocol.ManagedOutcome{outcome})
	if op := task(); op.Status != "dispatched" || op.Error != "member unreachable" {
		t.Fatalf("reason not shown on dispatched task: %+v", op)
	}
	outcome.Error = ""
	report([]protocol.ManagedOutcome{outcome})
	if op := task(); op.Status != "dispatched" || op.Error != "" {
		t.Fatalf("reason not cleared: %+v", op)
	}
	expired := time.Now().UTC().Add(-adoptedQueuedGrace - time.Minute)
	if _, err := a.c.db.Exec("UPDATE operations SET updated_at=? WHERE id=?", expired.Format(time.RFC3339Nano), queued.OperationID); err != nil {
		t.Fatal(err)
	}
	report([]protocol.ManagedOutcome{outcome})
	if op := task(); op.Status != "dispatched" || !op.UpdatedAt.After(expired) {
		t.Fatalf("unchanged queued reason did not refresh own operation: %+v", op)
	}
}

// VM power, media, update and delete requests audit the VM they act on, so a VM's activity can
// be read from GET /api/v1/audit (GM-08). vm-create already audits its VM id.
func TestVMOperationAuditsNameTheVM(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability, protocol.MediaCapability, protocol.DeleteCapability)
	spec := l.spec("audited")
	var created createdVM
	read(t, l.create("create-audit", spec, 202), &created)
	id := created.VMID
	commands := l.report().Commands
	if len(commands) != 1 || commands[0].Kind != "vm-create" {
		t.Fatalf("dispatch %+v", commands)
	}
	applied := *commands[0].VM
	state := &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}
	settle := func() {
		t.Helper()
		var results []protocol.Result
		for _, cmd := range l.reportState("shut off", state).Commands {
			results = append(results, protocol.Result{OperationID: cmd.OperationID, Status: "succeeded"})
		}
		l.reportState("shut off", state, results...)
	}
	settle()
	next := spec
	next.Description = "audited edit"
	l.update("edit-audit", id, next, 202)
	settle()
	l.a.call("POST", "/api/v1/vms/"+id+"/power", map[string]string{"request_id": "power-audit", "action": "start"}, nil, 202)
	settle()
	l.a.call("POST", "/api/v1/vms/"+id+"/media", map[string]string{"request_id": "media-audit", "action": "eject"}, nil, 202)
	settle()
	l.a.call("DELETE", "/api/v1/vms/"+id, map[string]any{"request_id": "delete-audit"}, nil, 202)
	var audits []protocol.Audit
	read(t, l.a.call("GET", "/api/v1/audit", nil, nil, 200), &audits)
	seen := map[string]bool{}
	for _, e := range audits {
		switch e.Action {
		case "vm-create", "vm-update", "vm-power", "vm-media", "vm-delete":
			if e.ResourceID != id {
				t.Errorf("%s audited resource_id %q, want VM id %q", e.Action, e.ResourceID, id)
			}
			seen[e.Action] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("audit actions %v in %+v", seen, audits)
	}
}

// A pool VLAN requested against a member is re-homed onto the master. The retry the UI sends
// after a lost response carries the member host again and must replay, not burn the request ID.
func TestPoolVLANRequestedOnMemberReplays(t *testing.T) {
	a := newClient(t)
	site := a.site()
	master := a.enroll(a.token(site, ""), csr(t))
	pool := a.poolOf(master.HostID)
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", NICs: []protocol.NIC{{Name: "nex1", Kind: "physical"}}}})
	member := a.enroll(a.token(site, ""), csr(t))
	a.report(certificate(t, member.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}})
	if _, err := a.c.db.Exec("UPDATE hosts SET pool_id=? WHERE id=?", pool, member.HostID); err != nil {
		t.Fatal(err)
	}
	request := registrationRequest{RequestID: "vlan-member", PoolID: pool, HostID: member.HostID, Name: "Servers", Kind: "vlan", Uplink: "nex1", VLAN: 100}
	var created, replayed protocol.NetworkRegistration
	read(t, a.call("POST", "/api/v1/networks", request, nil, 201), &created)
	if created.HostID != master.HostID || created.Source != "nex1v100" {
		t.Fatalf("pool VLAN was not re-homed onto the master: %+v", created)
	}
	read(t, a.call("POST", "/api/v1/networks", request, nil, 200), &replayed)
	if replayed.ID != created.ID || replayed.OperationID != created.OperationID {
		t.Fatalf("identical retry did not replay the original registration: %+v", replayed)
	}
}

// An entry the host reported as unusable gets one needs_attention operation. When the host
// reports it repaired, adoption must close that operation instead of leaving it open forever.
func TestRepairedNetworkClosesAdoptionAttention(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	report := func(sequence uint64, source string) {
		a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: []string{protocol.ManagedVMCapability},
			Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Networks: []protocol.NetworkRegistration{
				{ID: "net-held", Name: "Held", Kind: "bridge", Source: "br0"},
				{ID: "net-repaired", Name: "Repaired", Kind: "bridge", Source: source}}}}})
	}
	// The host first reports the entry claiming a bridge another registration already holds.
	report(1, "br0")
	attention := "adopt-attention:" + host.HostID + ":network:net-repaired"
	status := func() string {
		var ops []protocol.Operation
		read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
		for _, op := range ops {
			if op.RequestID == attention {
				return op.Status
			}
		}
		return ""
	}
	if got := status(); got != "needs_attention" {
		t.Fatalf("unadoptable network was not recorded as needing attention: %q", got)
	}
	report(2, "br1")
	if got := status(); got != "succeeded" {
		t.Fatalf("adopted network left its attention operation open: %q", got)
	}
}

// One unusable master-reported VLAN must not fail the whole host report: it is recorded as
// needing attention and the held projection is left as it was.
func TestInvalidMasterVLANDoesNotFailHostReport(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	inventory := &protocol.Inventory{Hostname: "core", NICs: []protocol.NIC{{Name: "nex1", Kind: "physical"}}}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inventory})
	var network protocol.NetworkRegistration
	read(t, a.call("POST", "/api/v1/networks", registrationRequest{RequestID: "vlan-held", PoolID: pool, HostID: host.HostID, Name: "Servers", Kind: "vlan", Uplink: "nex1", VLAN: 100}, nil, 201), &network)
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", NICs: inventory.NICs, Managed: &protocol.ManagedState{Networks: []protocol.NetworkRegistration{
			{ID: network.ID, Name: "Servers", Kind: "vlan", Source: "wrong", Uplink: "nex1", VLAN: 100}}}}})
	var list []protocol.NetworkRegistration
	read(t, a.call("GET", "/api/v1/networks", nil, nil, 200), &list)
	if len(list) != 1 || list[0].Source != "nex1v100" {
		t.Fatalf("invalid master row replaced the held projection: %+v", list)
	}
	var ops []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	attention := func() string {
		read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
		for _, op := range ops {
			if op.RequestID == "adopt-attention:"+host.HostID+":network:"+network.ID {
				return op.Status
			}
		}
		return ""
	}
	if attention() != "needs_attention" {
		t.Fatalf("invalid master VLAN was not recorded as needing attention: %+v", ops)
	}
	// The repaired held row must close the attention it opened.
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 3, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", NICs: inventory.NICs, Managed: &protocol.ManagedState{Networks: []protocol.NetworkRegistration{
			{ID: network.ID, Name: "Servers", Kind: "vlan", Source: "nex1v100", Uplink: "nex1", VLAN: 100}}}}})
	if got := attention(); got == "needs_attention" {
		t.Fatalf("repaired held VLAN kept its attention operation open: %+v", ops)
	}
}

func TestMissingQueuedMirrorRequiresCoreSettlement(t *testing.T) {
	for _, kind := range []string{"vm-release-retained", "vm-power", "backup-now", "snapshot-create"} {
		t.Run(kind, func(t *testing.T) {
			a := newClient(t)
			host := a.enroll(a.token(a.site(), ""), csr(t))
			cert := certificate(t, host.CertificatePEM)
			seq := uint64(0)
			outcome := protocol.ManagedOutcome{OperationID: "core-pending", Kind: kind, ID: "11111111-1111-4111-8111-111111111111", Status: "queued"}
			report := func(outcomes ...protocol.ManagedOutcome) {
				seq++
				a.report(cert, protocol.ReportRequest{Version: 1, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability},
					Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Outcomes: outcomes}}})
			}
			requestID := "adopt-outcome:" + host.HostID + ":" + outcome.OperationID
			task := func() protocol.Operation {
				var ops []protocol.Operation
				read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
				for _, op := range ops {
					if op.RequestID == requestID {
						return op
					}
				}
				t.Fatal("adopted task missing")
				return protocol.Operation{}
			}
			report(outcome)
			if op := task(); op.Status != "dispatched" {
				t.Fatalf("queued mirror: %+v", op)
			}
			expired := time.Now().UTC().Add(-adoptedQueuedGrace - time.Minute).Format(time.RFC3339Nano)
			if _, err := a.c.db.Exec("UPDATE operations SET updated_at=? WHERE request_id=?", expired, requestID); err != nil {
				t.Fatal(err)
			}
			for i := range 2 {
				report()
				if op := task(); op.Status != "needs_attention" {
					t.Fatalf("missing report %d falsely settled task: %+v", i+1, op)
				}
			}
			outcome.Status = "succeeded"
			report(outcome)
			if op := task(); op.Status != "succeeded" || op.Error != "" {
				t.Fatalf("terminal Core settlement: %+v", op)
			}
			outcome.Status = "queued"
			report(outcome)
			report()
			if op := task(); op.Status != "succeeded" {
				t.Fatalf("terminal history regressed: %+v", op)
			}
		})
	}
}

func TestQueuedObservationRefreshesMirrorSilenceClock(t *testing.T) {
	for _, kind := range []string{"vm-release-retained", "backup-now", "snapshot-create"} {
		t.Run(kind, func(t *testing.T) {
			a := newClient(t)
			host := a.enroll(a.token(a.site(), ""), csr(t))
			cert := certificate(t, host.CertificatePEM)
			seq := uint64(0)
			outcome := protocol.ManagedOutcome{OperationID: "core-waiting", Kind: kind, ID: "11111111-1111-4111-8111-111111111111", Status: "queued"}
			report := func(outcomes ...protocol.ManagedOutcome) {
				seq++
				a.report(cert, protocol.ReportRequest{Version: 1, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability},
					Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Outcomes: outcomes}}})
			}
			requestID := "adopt-outcome:" + host.HostID + ":" + outcome.OperationID
			expired := time.Now().UTC().Add(-adoptedQueuedGrace - time.Minute).Format(time.RFC3339Nano)
			age := func() {
				if _, err := a.c.db.Exec("UPDATE operations SET updated_at=? WHERE request_id=?", expired, requestID); err != nil {
					t.Fatal(err)
				}
			}
			task := func() (string, string) {
				var status, updated string
				if err := a.c.db.QueryRow("SELECT status,updated_at FROM operations WHERE request_id=?", requestID).Scan(&status, &updated); err != nil {
					t.Fatal(err)
				}
				return status, updated
			}
			report(outcome)
			age()
			report(outcome)
			if status, updated := task(); status != "dispatched" || updated <= expired {
				t.Fatalf("unchanged queued observation not refreshed: %s at %s", status, updated)
			}
			report()
			if status, _ := task(); status != "dispatched" {
				t.Fatalf("fresh observation followed by one missing heartbeat: %s", status)
			}
			age()
			report()
			if status, _ := task(); status != "needs_attention" {
				t.Fatalf("expired silence: %s", status)
			}
			var silenced string
			if err := a.c.db.QueryRow("SELECT error FROM operations WHERE request_id=?", requestID).Scan(&silenced); err != nil || silenced == "" {
				t.Fatalf("silence did not record a reason: %q %v", silenced, err)
			}
			age()
			outcome.Error = "core waiting reason"
			report(outcome)
			if status, updated := task(); status != "needs_attention" || updated <= expired {
				t.Fatalf("queued reappearance must refresh without settling: %s at %s", status, updated)
			}
			var kept string
			if err := a.c.db.QueryRow("SELECT error FROM operations WHERE request_id=?", requestID).Scan(&kept); err != nil || kept != silenced {
				t.Fatalf("queued observation overwrote the needs_attention reason: %q, was %q (%v)", kept, silenced, err)
			}
		})
	}
}
