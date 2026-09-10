// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"nex.local/nex/internal/protocol"
)

func managedTestISO() ([]byte, protocol.Media) {
	b := make([]byte, 40<<10)
	copy(b[32769:], "CD001")
	h := sha256.Sum256(b)
	return b, protocol.Media{ID: "media-1", PoolID: "pool-1", HostID: "host-1", StorageID: "storage-1", SizeBytes: int64(len(b)), SHA256: hex.EncodeToString(h[:])}
}
func TestManagedISOPublication(t *testing.T) {
	body, m := managedTestISO()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		data []byte
		hash string
		fail bool
	}{
		{"valid", body, m.SHA256, false}, {"short", body[:len(body)-1], m.SHA256, true}, {"long", append(append([]byte{}, body...), 0), m.SHA256, true}, {"wrong hash", body, strings.Repeat("0", 64), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.iso")
			media := m
			media.SHA256 = tc.hash
			err := receiveISO(ctx, path, media, bytes.NewReader(tc.data))
			if (err != nil) != tc.fail {
				t.Fatalf("receive: %v", err)
			}
			if _, err := os.Stat(path + ".partial"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("partial upload retained")
			}
			if tc.fail {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("invalid media published")
				}
				return
			}
			if err := verifyISO(path, m); err != nil {
				t.Fatal(err)
			}
			if err := receiveISO(ctx, path, m, bytes.NewReader(body)); err == nil {
				t.Fatal("existing ISO overwritten")
			}
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
			if verifyISO(path, m) == nil {
				t.Fatal("writable ISO accepted")
			}
		})
	}
	t.Run("not ISO", func(t *testing.T) {
		bad := make([]byte, len(body))
		h := sha256.Sum256(bad)
		m.SHA256 = hex.EncodeToString(h[:])
		if receiveISO(ctx, filepath.Join(t.TempDir(), "image.iso"), m, bytes.NewReader(bad)) == nil {
			t.Fatal("non ISO accepted")
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		if receiveISO(ctx, filepath.Join(t.TempDir(), "image.iso"), m, bytes.NewReader(body)) == nil {
			t.Fatal("cancel ignored")
		}
	})
}
func TestManagedValidationAndLazyConstruction(t *testing.T) {
	root := t.TempDir()
	e := NewManaged(filepath.Join(root, "state"))
	c := protocol.Command{OperationID: "../escape", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "s", PoolID: "p", HostID: "h", Path: root}}
	r, err := e.Execute(context.Background(), c, nil)
	if err != nil || r.Status != "failed" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(e.stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid command created state")
	}
	c.OperationID = "op"
	if validateManaged(c) != nil {
		t.Fatal("valid storage rejected")
	}
	c.Network = &protocol.NetworkRegistration{}
	if validateManaged(c) == nil {
		t.Fatal("multiple payloads accepted")
	}
}
func TestManagedJournalReplayAndInterruptedNoEffects(t *testing.T) {
	ctx := context.Background()
	e := NewManaged(t.TempDir())
	c := protocol.Command{OperationID: "op-1", Kind: "network-register", Network: &protocol.NetworkRegistration{ID: "net", PoolID: "pool", HostID: "host", Kind: "network", Source: "default"}}
	count := filepath.Join(t.TempDir(), "calls")
	t.Setenv("MANAGED_CALLS", count)
	fakeCommand(t, "virsh", `echo call >> "$MANAGED_CALLS"
printf 'default\n'`)
	r, err := e.Execute(ctx, c, nil)
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("%+v %v", r, err)
	}
	reopened := NewManaged(e.stateDir)
	r, err = reopened.Execute(ctx, c, nil)
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("replay: %+v %v", r, err)
	}
	b, _ := os.ReadFile(count)
	if strings.Count(string(b), "call") != 1 {
		t.Fatal("replayed effects")
	}
	changed := c
	n := *c.Network
	n.Source = "other"
	changed.Network = &n
	r, err = e.Execute(ctx, changed, nil)
	if err != nil || r.Status != "failed" {
		t.Fatal("changed replay accepted")
	}
	c.OperationID = "op-2"
	r, err = e.Execute(ctx, c, nil)
	if err != nil || r.Status != "failed" {
		t.Fatal("reserved resource reused")
	}
	c.OperationID = "op-3"
	n.ID = "other"
	n.Source = "default"
	c.Network = &n
	j := managedJournal{Command: c, Result: protocol.Result{OperationID: c.OperationID, Status: "pending"}, Stage: "validating"}
	if err = e.save(j); err != nil {
		t.Fatal(err)
	}
	r, err = e.Execute(ctx, c, nil)
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("interruption: %+v %v", r, err)
	}
	b, _ = os.ReadFile(count)
	if strings.Count(string(b), "call") != 2 {
		t.Fatal("interrupted registration was not verified")
	}
}
func TestManagedReferencesAndXML(t *testing.T) {
	s := protocol.StorageRegistration{ID: "s", PoolID: "p", HostID: "h", Path: "/var/lib/libvirt/images"}
	js := []managedJournal{{Command: protocol.Command{Storage: &s}, Result: protocol.Result{Status: "succeeded"}}}
	if _, err := managedStorage(js, "other"); err == nil {
		t.Fatal("unknown storage ID accepted")
	}
	js[0].Result.Status = "pending"
	if _, err := managedStorage(js, "s"); err == nil {
		t.Fatal("pending storage accepted")
	}
	for _, kind := range []string{"bridge", "network"} {
		for _, firmware := range []string{"bios", "uefi"} {
			v := managedTestVM()
			v.Boot.Firmware = firmware
			b, err := managedDomainXML(v, []protocol.NetworkRegistration{{Kind: kind, Source: "guest-net"}}, []string{"/disk&name"}, "/guest.iso")
			if err != nil {
				t.Fatal(err)
			}
			var d managedDomain
			if err = xml.Unmarshal(b, &d); err != nil {
				t.Fatal(err)
			}
			if d.UUID != v.ID || d.Devices.Disks[0].Source.File != "/disk&name" || d.Devices.Disks[1].Readonly == nil || d.Devices.Graphics[0].Listen.Type != "socket" || d.Devices.Graphics[0].Autoport != "no" || d.CPU.Mode != "host-passthrough" {
				t.Fatalf("unsafe domain: %s", b)
			}
			if (d.OS.Firmware == "efi") != (firmware == "uefi") || len(d.OS.Boot) != 0 || bootOrder(d.Devices.Disks[1].Boot) != 1 {
				t.Fatalf("firmware/boot mismatch: %s", b)
			}
			// Console features: guest agent, clipboard channel, absolute pointer and a resizable display.
			text := string(b)
			for _, want := range []string{`<channel type="unix">`, `name="org.qemu.guest_agent.0"`, `<channel type="qemu-vdagent">`, `<clipboard copypaste="yes">`, `<mouse mode="server">`, `name="com.redhat.spice.0"`, `<input type="tablet" bus="usb">`, `<model type="virtio"></model>`} {
				if !strings.Contains(text, want) {
					t.Fatalf("definition lacks %s: %s", want, b)
				}
			}
			if strings.Count(text, "<graphics") != 1 {
				t.Fatalf("expected exactly one display: %s", b)
			}
		}
	}
}
func TestManagedStorageRejectsSymlinksAndUnsupportedFilesystems(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if localStorage(link, 0) == nil {
		t.Fatal("symlink storage accepted")
	}
	if localStorage(filepath.Join(dir, "missing"), 0) == nil {
		t.Fatal("missing storage accepted")
	}
	path := filepath.Join(dir, "subdir")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := managedSubdir(path); err == nil {
		t.Fatal("untrusted directory accepted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0700 {
		t.Fatal("existing permissions altered")
	}
}
func TestManagedMediaInterruptedReconciliation(t *testing.T) {
	e := NewManaged(t.TempDir())
	body, m := managedTestISO()
	path := filepath.Join(t.TempDir(), "image.iso")
	if err := receiveISO(context.Background(), path, m, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	c := protocol.Command{OperationID: "op", Kind: "iso-import", Media: &m}
	j := managedJournal{Command: c, Result: protocol.Result{OperationID: "op", Status: "pending"}, Stage: "receiving", MediaPath: path}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	r, err := e.Execute(context.Background(), c, func(context.Context, string) (io.ReadCloser, error) { t.Fatal("replayed fetch"); return nil, nil })
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestManagedVMInterruptedAllocationNeverReplays(t *testing.T) {
	e := NewManaged(t.TempDir())
	v := managedTestVM()
	c := protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}
	j := managedJournal{Command: c, Result: protocol.Result{OperationID: "create", Status: "pending"}, Stage: "allocating"}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "qemu-img", "exit 99")
	fakeCommand(t, "virsh", "exit 99")
	r, err := e.Execute(context.Background(), c, nil)
	if err != nil || r.Status != "needs_attention" {
		t.Fatalf("%+v %v", r, err)
	}
	b, err := os.ReadFile(filepath.Join(e.stateDir, "create.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved managedJournal
	if json.Unmarshal(b, &saved) != nil || saved.Result.Status != "needs_attention" {
		t.Fatal("uncertain result not durable")
	}
	// The appliance engine has a distinct ownership journal and refuses general guests.
	appliance := testEngine(t)
	if _, err := appliance.Power(context.Background(), "power", v.ID, "start"); err == nil {
		t.Fatal("general guest entered appliance power recovery")
	}
}

func TestManagedDefinitionTampering(t *testing.T) {
	v := managedTestVM()
	v.Memory = protocol.VMMemory{Mode: "maximum", SizeMiB: 2048}
	v.CPU = protocol.VMCPU{Count: 4, Sockets: 2, Cores: 2, Threads: 1, Model: "host-passthrough"}
	v.Disks = append(v.Disks, protocol.VMDisk{StorageID: "s", Name: "data", SizeGiB: 10, Bus: "scsi", BootOrder: 3})
	v.NICs = append(v.NICs, protocol.VMNIC{NetworkID: "n2", Model: "e1000e", MAC: "52:54:00:aa:bb:cc"})
	nets := []protocol.NetworkRegistration{{Kind: "network", Source: "default"}, {Kind: "bridge", Source: "br0"}}
	wanted, err := managedDomainXML(v, nets, []string{"/disk-0", "/disk-1"}, "/iso")
	if err != nil {
		t.Fatal(err)
	}
	if err = validateManagedDefinition(wanted, wanted); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, new string }{
		{"firmware", `firmware="efi"`, `firmware="bios"`}, {"CPU", `mode="host-passthrough"`, `mode="host-model"`}, {"topology", `sockets="2"`, `sockets="4"`},
		{"machine", `machine="q35"`, `machine="pc-i440fx-10.0"`}, {"os boot list", `</os>`, `<boot dev="hd"></boot></os>`},
		{"first disk boot order", `order="2"`, `order="4"`}, {"second disk boot order", `order="3"`, `order="4"`}, {"media boot order", `order="1"`, `order="4"`},
		{"disk bus", `bus="virtio"`, `bus="sata"`}, {"disk target", `dev="vda"`, `dev="vdb"`}, {"second disk target", `dev="sda"`, `dev="sdc"`},
		{"disk ownership", `file="/disk-0"`, `file="/other"`}, {"second disk ownership", `file="/disk-1"`, `file="/other"`}, {"ISO ownership", `file="/iso"`, `file="/other"`},
		{"network", `network="default"`, `network="other"`}, {"second network", `bridge="br0"`, `bridge="other"`}, {"second MAC", `52:54:00:aa:bb:cc`, `52:54:00:aa:bb:cd`},
		{"balloon", `model="virtio" freePageReporting="on"`, `model="none"`}, {"free page reporting", `freePageReporting="on"`, `freePageReporting="off"`}, {"VNC", `type="socket"`, `type="address"`},
		{"extra disk", `</devices>`, `<disk type="file" device="disk"><driver name="qemu" type="qcow2"></driver><source file="/evil"></source><target dev="vdz" bus="virtio"></target></disk></devices>`},
		{"extra interface", `</devices>`, `<interface type="bridge"><source bridge="evil"></source><mac address="52:54:00:00:00:99"></mac><model type="virtio"></model></interface></devices>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(string(wanted), tc.old, tc.new, 1)
			if changed == string(wanted) {
				t.Fatal("fixture did not mutate")
			}
			if validateManagedDefinition([]byte(changed), wanted) == nil {
				t.Fatal("tampered definition accepted")
			}
		})
	}
	// libvirt normalizes units and machine aliases, always reports currentMemory and adds its
	// own controllers; none of that is tampering.
	normalized := strings.Replace(string(wanted), `unit="MiB">2048`, `unit="KiB">2097152`, 1)
	normalized = strings.Replace(normalized, `machine="q35"`, `machine="pc-q35-10.0"`, 1)
	normalized = strings.Replace(normalized, `</memory>`, `</memory><currentMemory unit="KiB">2097152</currentMemory>`, 1)
	normalized = strings.Replace(normalized, `</devices>`, `<controller type="usb" index="0" model="qemu-xhci"></controller></devices>`, 1)
	if err = validateManagedDefinition([]byte(normalized), wanted); err != nil {
		t.Fatalf("libvirt normalized definition rejected: %v", err)
	}
	// A libvirt that does not echo freePageReporting must not fail every "maximum" VM.
	if err = validateManagedDefinition([]byte(strings.Replace(string(wanted), ` freePageReporting="on"`, "", 1)), wanted); err != nil {
		t.Fatalf("absent freePageReporting rejected: %v", err)
	}
}

func TestManagedStorageRejectsRootOwnedWritableAncestor(t *testing.T) {
	// Linux /tmp is root-owned but writable: root ownership alone is insufficient.
	info, err := os.Stat("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || info.Mode().Perm()&0022 == 0 {
		t.Fatal("this check requires the Linux root-owned writable /tmp directory")
	}
	if trustedManagedPath("/tmp") == nil {
		t.Fatal("root-owned writable storage ancestor accepted")
	}
}
func TestManagedCorruptJournalFailsClosed(t *testing.T) {
	e := NewManaged(t.TempDir())
	if err := os.WriteFile(filepath.Join(e.stateDir, "bad.json"), []byte(`{"command":{"kind":"storage-register","operation_id":"bad"},"result":{"status":"pending"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c := protocol.Command{OperationID: "good", Kind: "network-register", Network: &protocol.NetworkRegistration{ID: "n", PoolID: "p", HostID: "h", Kind: "network", Source: "default"}}
	if _, err := e.Execute(context.Background(), c, nil); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}

func TestManagedStoragePermissions(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  uint32
		mode os.FileMode
		want bool
	}{
		{"root private", 0, 0700, false}, {"root group only", 0, 0750, false}, {"root traversable", 0, 0711, true}, {"root readable", 0, 0755, true}, {"group writable", 0, 0775, false}, {"world writable", 0, 0777, false}, {"user owned", 1000, 0755, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := managedStoragePermissions(tc.uid, tc.mode); got != tc.want {
				t.Fatalf("permissions accepted=%v want %v", got, tc.want)
			}
		})
	}
}

// A host re-enrolled under a replaced Controller keeps journal entries recorded under the old
// pool and host IDs; VM creation must still resolve them by resource ID.
func TestManagedReferencesIgnorePoolAndHostIDs(t *testing.T) {
	ok := protocol.Result{Status: "succeeded"}
	old := managedJournal{Command: protocol.Command{OperationID: "s", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "s", PoolID: "old-pool", HostID: "old-host", Path: "/srv/vms"}}, Result: ok}
	net := managedJournal{Command: protocol.Command{OperationID: "n", Kind: "network-register", Network: &protocol.NetworkRegistration{ID: "n", PoolID: "old-pool", HostID: "old-host", Kind: "bridge", Source: "br0"}}, Result: ok}
	iso := managedJournal{Command: protocol.Command{OperationID: "m", Kind: "iso-import", Media: &protocol.Media{ID: "m", PoolID: "old-pool", HostID: "old-host", StorageID: "s"}}, Result: ok, MediaPath: "/srv/vms/nex-media/m.iso"}
	wrongStorage := managedJournal{Command: protocol.Command{OperationID: "m2", Kind: "iso-import", Media: &protocol.Media{ID: "m2", PoolID: "new-pool", HostID: "new-host", StorageID: "s2"}}, Result: ok}
	js := []managedJournal{old, net, iso, wrongStorage}
	s, err := managedStorage(js, "s")
	if err != nil || s.Path != "/srv/vms" {
		t.Fatalf("storage under old pool/host not resolved: %+v %v", s, err)
	}
	n, err := managedNetwork(js, "n")
	if err != nil || n.Source != "br0" {
		t.Fatalf("network under old pool/host not resolved: %+v %v", n, err)
	}
	m, path, err := managedMedia(js, "m")
	if err != nil || m.StorageID != "s" || path != "/srv/vms/nex-media/m.iso" {
		t.Fatalf("ISO under old pool/host not resolved: %+v %q %v", m, path, err)
	}
	if _, err := managedNetwork(js, "other"); err == nil {
		t.Fatal("unknown network accepted")
	}
	if _, _, err := managedMedia(js, "other"); err == nil {
		t.Fatal("unknown ISO accepted")
	}
}

func managedTestVM() protocol.VMCreateSpec {
	return protocol.VMCreateSpec{ID: "12345678-1234-4234-8234-123456789012", PoolID: "p", HostID: "h", Name: "guest", GuestOS: "linux",
		CPU: protocol.VMCPU{Count: 2, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 2048},
		Disks:   []protocol.VMDisk{{StorageID: "s", Name: "system", SizeGiB: 20, Bus: "virtio", BootOrder: 2}},
		NICs:    []protocol.VMNIC{{NetworkID: "n", Model: "virtio"}},
		Boot:    protocol.VMBoot{Firmware: "uefi", MediaBootOrder: 1},
		MediaID: "m"}
}

// Every disk and NIC reaches the definition on its own bus and target, and the boot order lives
// on the devices: libvirt refuses a domain that also carries an <os> boot list.
func TestManagedDomainXMLDevicesAndBootOrder(t *testing.T) {
	v := managedTestVM()
	v.Disks = []protocol.VMDisk{{StorageID: "fast", Name: "system", SizeGiB: 20, Bus: "virtio", BootOrder: 2}, {StorageID: "bulk", Name: "data", SizeGiB: 40, Bus: "sata"}, {StorageID: "fast", Name: "logs", SizeGiB: 10, Bus: "scsi", BootOrder: 3}}
	v.NICs = []protocol.VMNIC{{NetworkID: "n", Model: "virtio"}, {NetworkID: "n2", Model: "e1000e", MAC: "52:54:00:aa:bb:cc"}, {NetworkID: "n", Model: "virtio"}}
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}, {Kind: "network", Source: "default"}, {Kind: "bridge", Source: "br0"}}
	paths := []string{"/fast/disk-0.qcow2", "/bulk/disk-1.qcow2", "/fast/disk-2.qcow2"}
	b, err := managedDomainXML(v, nets, paths, "/fast/nex-media/m.iso")
	if err != nil {
		t.Fatal(err)
	}
	var d managedDomain
	if err = xml.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Devices.Disks) != 4 {
		t.Fatalf("expected three disks and the install ISO: %s", b)
	}
	for i, want := range []struct {
		dev, bus, file string
		order          int
	}{{"vda", "virtio", paths[0], 2}, {"sda", "sata", paths[1], 0}, {"sdb", "scsi", paths[2], 3}, {"sdc", "sata", "/fast/nex-media/m.iso", 1}} {
		got := d.Devices.Disks[i]
		if got.Target.Dev != want.dev || got.Target.Bus != want.bus || got.Source.File != want.file || bootOrder(got.Boot) != want.order {
			t.Fatalf("disk %d is %+v, want %+v", i, got, want)
		}
	}
	if d.Devices.Disks[3].Device != "cdrom" || d.Devices.Disks[3].Readonly == nil {
		t.Fatal("install ISO must be a read-only cdrom")
	}
	if len(d.Devices.Controllers) != 1 || d.Devices.Controllers[0].Model != "virtio-scsi" {
		t.Fatalf("SCSI disks need a virtio-scsi controller: %s", b)
	}
	if len(d.OS.Boot) != 0 || strings.Contains(string(b), "<boot dev=") {
		t.Fatalf("per-device boot order must not be mixed with an os boot list: %s", b)
	}
	if len(d.Devices.Interfaces) != 3 {
		t.Fatalf("expected three interfaces: %s", b)
	}
	if d.Devices.Interfaces[0].Type != "bridge" || d.Devices.Interfaces[0].Source.Bridge != "br0" || d.Devices.Interfaces[0].Model.Type != "virtio" || d.Devices.Interfaces[0].MAC.Address != protocol.DerivedMAC(v.ID, 0) {
		t.Fatalf("derived NIC: %+v", d.Devices.Interfaces[0])
	}
	if d.Devices.Interfaces[1].Type != "network" || d.Devices.Interfaces[1].Source.Network != "default" || d.Devices.Interfaces[1].Model.Type != "e1000e" || d.Devices.Interfaces[1].MAC.Address != "52:54:00:aa:bb:cc" {
		t.Fatalf("manual NIC: %+v", d.Devices.Interfaces[1])
	}
	// Two derived NICs on one VM must differ: the MAC comes from the VM ID and the interface index.
	if d.Devices.Interfaces[2].MAC.Address != protocol.DerivedMAC(v.ID, 2) || d.Devices.Interfaces[2].MAC.Address == d.Devices.Interfaces[0].MAC.Address {
		t.Fatalf("second derived NIC: %+v", d.Devices.Interfaces[2])
	}
	// A VM without install media has no cdrom at all.
	v.Boot.MediaBootOrder, v.MediaID = 0, ""
	if b, err = managedDomainXML(v, nets, paths, ""); err != nil || strings.Contains(string(b), "cdrom") {
		t.Fatalf("media-free VM: %v\n%s", err, b)
	}
	if _, err = managedDomainXML(v, nets[:1], paths, ""); err == nil {
		t.Fatal("mismatched device lists accepted")
	}
}

func TestManagedDomainXMLMemoryModesAndTopology(t *testing.T) {
	for _, tc := range []struct {
		mode, model, reporting string
		current                bool
	}{{"dedicated", "none", "", true}, {"maximum", "virtio", "on", false}} {
		t.Run(tc.mode, func(t *testing.T) {
			v := managedTestVM()
			v.Memory = protocol.VMMemory{Mode: tc.mode, SizeMiB: 4096}
			b, err := managedDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "/m.iso")
			if err != nil {
				t.Fatal(err)
			}
			var d managedDomain
			if err = xml.Unmarshal(b, &d); err != nil {
				t.Fatal(err)
			}
			if d.Memory != (memoryXML{"MiB", 4096}) || (d.CurrentMemory != nil) != tc.current {
				t.Fatalf("memory ceiling: %s", b)
			}
			if tc.current && *d.CurrentMemory != (memoryXML{"MiB", 4096}) {
				t.Fatalf("dedicated memory must be held: %s", b)
			}
			if d.Devices.Memballoon.Model != tc.model || d.Devices.Memballoon.FreePageReporting != tc.reporting {
				t.Fatalf("balloon %+v, want model %q reporting %q", d.Devices.Memballoon, tc.model, tc.reporting)
			}
		})
	}
	v := managedTestVM()
	v.CPU = protocol.VMCPU{Count: 4, Model: "host-model"}
	b, _ := managedDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	var d managedDomain
	if xml.Unmarshal(b, &d) != nil || d.VCPU != 4 || d.CPU.Mode != "host-model" || d.CPU.Topology == nil || *d.CPU.Topology != (managedTopology{1, 4, 1}) {
		t.Fatalf("default topology is one socket of Count cores: %s", b)
	}
	v.CPU = protocol.VMCPU{Count: 4, Sockets: 2, Cores: 2, Threads: 1, Model: "host-passthrough"}
	b, _ = managedDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	if xml.Unmarshal(b, &d) != nil || d.CPU.Topology == nil || *d.CPU.Topology != (managedTopology{2, 2, 1}) {
		t.Fatalf("requested topology: %s", b)
	}
}

// Free space is checked once per storage target against the sum of the disks landing on it.
func TestManagedDiskPlanSumsPerTarget(t *testing.T) {
	ok := protocol.Result{Status: "succeeded"}
	js := []managedJournal{
		{Command: protocol.Command{Storage: &protocol.StorageRegistration{ID: "fast", Path: "/srv/fast"}}, Result: ok},
		{Command: protocol.Command{Storage: &protocol.StorageRegistration{ID: "bulk", Path: "/srv/bulk"}}, Result: ok},
	}
	v := managedTestVM()
	v.Disks = []protocol.VMDisk{{StorageID: "fast", Name: "system", SizeGiB: 20, Bus: "virtio", BootOrder: 2}, {StorageID: "bulk", Name: "data", SizeGiB: 100, Bus: "virtio"}, {StorageID: "fast", Name: "logs", SizeGiB: 30, Bus: "virtio"}}
	targets, disks, err := managedDiskPlan(js, v)
	if err != nil {
		t.Fatal(err)
	}
	want := []managedTarget{
		{ID: "fast", Path: "/srv/fast", Dir: "/srv/fast/nex-vms/" + v.ID, Bytes: (20 + 30 + 4) << 30},
		{ID: "bulk", Path: "/srv/bulk", Dir: "/srv/bulk/nex-vms/" + v.ID, Bytes: (100 + 4) << 30},
	}
	if len(targets) != len(want) {
		t.Fatalf("one entry per target: %+v", targets)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("target %d is %+v, want %+v", i, targets[i], want[i])
		}
	}
	if len(disks) != 3 || disks[0] != want[0].Dir+"/disk-0.qcow2" || disks[1] != want[1].Dir+"/disk-1.qcow2" || disks[2] != want[0].Dir+"/disk-2.qcow2" {
		t.Fatalf("disks are named by index inside their own target: %q", disks)
	}
	v.Disks[1].StorageID = "missing"
	if _, _, err = managedDiskPlan(js, v); err == nil {
		t.Fatal("unregistered storage accepted")
	}
}

func TestManagedDiskAllocationArguments(t *testing.T) {
	thick := qemuCreateArgs("/srv/vms/disk-0.qcow2", protocol.VMDisk{SizeGiB: 20})
	if strings.Join(thick, " ") != "create -f qcow2 -o preallocation=falloc /srv/vms/disk-0.qcow2 20G" {
		t.Fatalf("thick disk: %q", thick)
	}
	thin := qemuCreateArgs("/srv/vms/disk-1.qcow2", protocol.VMDisk{SizeGiB: 40, Thin: true})
	if strings.Join(thin, " ") != "create -f qcow2 /srv/vms/disk-1.qcow2 40G" {
		t.Fatalf("thin disk: %q", thin)
	}
}

// Preflight refuses a VM whose ceiling the host cannot meet, in both memory modes.
func TestManagedCapacityUsesTheMemoryCeiling(t *testing.T) {
	for _, mode := range []string{"dedicated", "maximum"} {
		if !managedMemoryFits(6*1024*1024, protocol.VMMemory{Mode: mode, SizeMiB: 4096}) {
			t.Fatalf("%s: 4 GiB refused with 6 GiB available", mode)
		}
		if managedMemoryFits(6*1024*1024, protocol.VMMemory{Mode: mode, SizeMiB: 5120}) {
			t.Fatalf("%s: ceiling exceeding the 2 GiB host reserve accepted", mode)
		}
	}
	v := managedTestVM()
	v.CPU.Count = runtime.NumCPU() + 1
	if managedCapacity(context.Background(), v) == nil {
		t.Fatal("more vCPUs than host CPUs accepted")
	}
}

func TestManagedAutostartObservation(t *testing.T) {
	for _, tc := range []struct {
		info string
		want bool
	}{{"Id:             -\nName:           guest\nAutostart:      enable\n", true}, {"Name:           guest\nAutostart:      disable\n", false}, {"Name:           guest\n", false}, {"", false}} {
		if got := autostartEnabled([]byte(tc.info)); got != tc.want {
			t.Fatalf("autostart %v for %q", got, tc.info)
		}
	}
}

func TestManagedVMCommandValidation(t *testing.T) {
	v := managedTestVM()
	c := protocol.Command{OperationID: "op", Kind: "vm-create", VM: &v}
	if err := validateManaged(c); err != nil {
		t.Fatalf("valid VM rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*protocol.VMCreateSpec)
	}{
		{"UUID", func(s *protocol.VMCreateSpec) { s.ID = "guest" }},
		{"name", func(s *protocol.VMCreateSpec) { s.Name = "Guest Machine" }},
		{"vCPUs", func(s *protocol.VMCreateSpec) { s.CPU.Count = 65 }},
		{"memory", func(s *protocol.VMCreateSpec) { s.Memory.SizeMiB = 262145 }},
		{"no disks", func(s *protocol.VMCreateSpec) { s.Disks = nil }},
		{"disk storage", func(s *protocol.VMCreateSpec) { s.Disks[0].StorageID = "../escape" }},
		{"network", func(s *protocol.VMCreateSpec) { s.NICs[0].NetworkID = "../escape" }},
		{"media", func(s *protocol.VMCreateSpec) { s.MediaID = "../escape" }},
		{"topology", func(s *protocol.VMCreateSpec) { s.CPU.Sockets, s.CPU.Cores, s.CPU.Threads = 3, 3, 3 }},
		{"boot order", func(s *protocol.VMCreateSpec) { s.Disks[0].BootOrder, s.Boot.MediaBootOrder = 0, 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := managedTestVM()
			bad.Disks = append([]protocol.VMDisk{}, bad.Disks...)
			bad.NICs = append([]protocol.VMNIC{}, bad.NICs...)
			tc.change(&bad)
			if validateManaged(protocol.Command{OperationID: "op", Kind: "vm-create", VM: &bad}) == nil {
				t.Fatal("invalid VM accepted")
			}
		})
	}
}

// A journal that lost a disk is not evidence of a complete VM.
func TestManagedCreationEvidenceCoversEveryDisk(t *testing.T) {
	e := NewManaged(t.TempDir())
	v := managedTestVM()
	v.Disks = append(v.Disks, protocol.VMDisk{StorageID: "s", Name: "data", SizeGiB: 10, Bus: "virtio"})
	j := managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}, Domain: []byte("<domain/>"), Disks: []string{"/srv/vms/disk-0.qcow2"}}
	err := e.observeCreated(context.Background(), j)
	if err == nil || !strings.Contains(err.Error(), "evidence unavailable") {
		t.Fatalf("incomplete disk evidence accepted: %v", err)
	}
}
