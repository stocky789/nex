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
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

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
printf 'default\nlab\n'`)
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
	// A second registration of the same libvirt network is a host-side conflict now; the
	// interrupted one registers another network.
	c.OperationID = "op-3"
	n.ID = "other"
	n.Source = "lab"
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
			b, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: kind, Source: "guest-net"}}, []string{"/disk&name"}, "/guest.iso")
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

// A VM created install-media-first (media_boot_order 1, disk boot_order 2, the shape
// managedTestVM returns) keeps its disk boot order after an eject: virsh change-media leaves
// every bootindex in the domain alone, so renumbering the record would make it describe a
// domain that does not exist. The gap this leaves must still validate, or a replacement
// Controller would skip adopting a still-running VM.
func TestManagedRecordVMMediaKeepsBootOrderOnEject(t *testing.T) {
	e := NewManaged(t.TempDir())
	v := managedTestVM()
	if v.Boot.MediaBootOrder != 1 || v.Disks[0].BootOrder != 2 {
		t.Fatalf("fixture must be install-media-first: %+v", v.Boot)
	}
	c := protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}
	j := managedJournal{Command: c, Result: protocol.Result{OperationID: "create", Status: "succeeded"}}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	if err := e.recordVMMedia(protocol.VMMediaRequest{VMID: v.ID, PoolID: v.PoolID, HostID: v.HostID, Action: "eject"}); err != nil {
		t.Fatal(err)
	}
	applied, _ := e.Applied(context.Background())
	if len(applied.VMs) != 1 {
		t.Fatalf("VM missing after eject: %+v", applied.VMs)
	}
	got := applied.VMs[0]
	if err := protocol.ValidateVMSpec(got); err != nil {
		t.Fatalf("post-eject spec fails validation, a fresh Controller could not adopt this VM: %v (%+v)", err, got)
	}
	if got.MediaID != "" || got.Boot.MediaBootOrder != 0 || got.Disks[0].BootOrder != 2 {
		t.Fatalf("eject must not renumber the disk boot order libvirt still holds: %+v", got)
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
	wanted, err := testDomainXML(v, nets, []string{"/disk-0", "/disk-1"}, "/iso")
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
	b, err := testDomainXML(v, nets, paths, "/fast/nex-media/m.iso")
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
	if len(d.Devices.Controllers) != 2+12 || d.Devices.Controllers[0].Model != "virtio-scsi" || d.Devices.Controllers[1].Model != "pcie-root" {
		t.Fatalf("SCSI disks need a virtio-scsi controller beside pcie-root and the root ports: %s", b)
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
	// A VM without install media still has its CD drive, empty, so an ISO can be inserted later
	// without hot-plugging a SATA device.
	v.Boot.MediaBootOrder, v.MediaID = 0, ""
	if b, err = testDomainXML(v, nets, paths, ""); err != nil {
		t.Fatalf("media-free VM: %v", err)
	}
	var empty managedDomain
	if err = xml.Unmarshal(b, &empty); err != nil || len(empty.Devices.Disks) != 4 || empty.Devices.Disks[3].Device != "cdrom" || empty.Devices.Disks[3].Source != nil || empty.Devices.Disks[3].Boot != nil {
		t.Fatalf("media-free VM must carry an empty CD drive: %v\n%s", err, b)
	}
	// q35 definitions carry spare PCIe root ports so VirtIO devices can be hot-plugged later.
	if strings.Count(string(b), `model="pcie-root-port"`) != 12 {
		t.Fatalf("expected spare root ports: %s", b)
	}
	if _, err = testDomainXML(v, nets[:1], paths, ""); err == nil {
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
			b, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "/m.iso")
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
	b, _ := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	var d managedDomain
	if xml.Unmarshal(b, &d) != nil || d.VCPU != 4 || d.CPU.Mode != "host-model" || d.CPU.Topology == nil || *d.CPU.Topology != (managedTopology{1, 4, 1}) {
		t.Fatalf("default topology is one socket of Count cores: %s", b)
	}
	v.CPU = protocol.VMCPU{Count: 4, Sockets: 2, Cores: 2, Threads: 1, Model: "host-passthrough"}
	b, _ = testDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	if xml.Unmarshal(b, &d) != nil || d.CPU.Topology == nil || *d.CPU.Topology != (managedTopology{2, 2, 1}) {
		t.Fatalf("requested topology: %s", b)
	}
	v.CPU.Model = "Skylake-Server"
	b, _ = testDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	if xml.Unmarshal(b, &d) != nil || d.CPU.Mode != "custom" || d.CPU.Match != "exact" || d.CPU.Check != "partial" || d.CPU.Model == nil || *d.CPU.Model != (cpuModelXML{Fallback: "forbid", Value: "Skylake-Server"}) {
		t.Fatalf("named CPU model is not exact/partial with forbidden fallback: %s", b)
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
	if managedCapacity(context.Background(), v, nil) == nil {
		t.Fatal("more vCPUs than host CPUs accepted")
	}
}

func TestAvailableCPUModelUsesUsableDomainCapabilities(t *testing.T) {
	query := func(_ context.Context, args ...string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"domcapabilities", "--virttype", "kvm", "--arch", "x86_64", "--machine", "q35"}) {
			t.Fatalf("query=%q", args)
		}
		return []byte(`<domainCapabilities><cpu><mode name="custom">
			<model usable="yes">Skylake-Server</model>
			<model usable="no">EPYC-Genoa</model>
			<model usable="yes">qemu64</model>
		</mode></cpu></domainCapabilities>`), nil
	}
	if !availableCPUModel(context.Background(), "Skylake-Server", query) || availableCPUModel(context.Background(), "EPYC-Genoa", query) {
		t.Fatal("CPU model availability did not follow usable domain capabilities")
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

// Each adapter model must reach libvirt as its own model type, and an MTU must appear only for
// virtio, the one model QEMU can communicate an MTU to (host_mtu). A list of names that all
// produced the same device, or an MTU that was silently dropped, would be the real failure.
func TestNICModelsAndMTUReachTheDomain(t *testing.T) {
	for _, model := range protocol.NICModels {
		v := managedTestVM()
		v.NICs = []protocol.VMNIC{{NetworkID: "net-1", Model: model}}
		if model == "virtio" {
			v.NICs[0].MTU = 9000
		}
		b, err := testDomainXML(v, []protocol.NetworkRegistration{{ID: "net-1", Kind: "bridge", Source: "br0"}}, []string{"/vms/disk-0.qcow2"}, "")
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		var d managedDomain
		if err = xml.Unmarshal(b, &d); err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		if len(d.Devices.Interfaces) != 1 || d.Devices.Interfaces[0].Model.Type != model {
			t.Fatalf("%s: adapter model not carried into the domain: %s", model, b)
		}
		mtu := d.Devices.Interfaces[0].MTU
		if model == "virtio" {
			if mtu == nil || mtu.Size != 9000 {
				t.Fatalf("virtio MTU dropped: %s", b)
			}
		} else if mtu != nil {
			t.Fatalf("%s: MTU emitted for a model that cannot tell the guest: %s", model, b)
		}
	}
}

// verifyDomain compares interfaces field by field because MTU is a pointer; whole-struct
// equality would compare addresses and reject every adapter carrying an MTU.
func TestVerifyDomainAcceptsAnEqualMTU(t *testing.T) {
	want := managedNIC{Type: "bridge"}
	want.Model.Type, want.MTU = "virtio", &mtuXML{Size: 9000}
	got := want
	got.MTU = &mtuXML{Size: 9000}
	if !sameMTU(got.MTU, want.MTU) {
		t.Fatal("an equal MTU held in a different pointer was treated as a change")
	}
	got.MTU = &mtuXML{Size: 1500}
	if sameMTU(got.MTU, want.MTU) {
		t.Fatal("a different MTU was accepted")
	}
	got.MTU = nil
	if sameMTU(got.MTU, want.MTU) {
		t.Fatal("a missing requested MTU was accepted")
	}
	if !sameMTU(&mtuXML{Size: 1500}, nil) {
		t.Fatal("an MTU we never requested must not fail the VM")
	}
}

// One corrupt journal file must not empty the reported state: a replaced Controller would
// then adopt nothing from the pool. The bad file is skipped and named in a failed check.
func TestAppliedSurvivesCorruptJournalEntry(t *testing.T) {
	e := NewManaged(t.TempDir())
	s := protocol.StorageRegistration{ID: "st-1", PoolID: "p", HostID: "h", Name: "Local", Path: "/srv/vms"}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "op-st", Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "op-st", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.stateDir, "bad.json"), []byte(`{"command":{"kind":"storage-register","operation_id":"bad"},"result":{"status":"pending"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, checks := e.Applied(context.Background())
	if len(got.Storage) != 1 || got.Storage[0].ID != "st-1" {
		t.Fatalf("corrupt file emptied the applied state: %+v", got)
	}
	var journal *protocol.Check
	for i := range checks {
		if checks[i].Name == "managed-journal" {
			journal = &checks[i]
		}
	}
	if journal == nil || journal.Status != "failed" || !strings.Contains(journal.Error, "bad.json") {
		t.Fatalf("corrupt file not surfaced as a failed managed-journal check: %+v", checks)
	}
	// An intact journal reports the check healthy and Execute still fails closed on corruption.
	if _, err := e.Execute(context.Background(), protocol.Command{OperationID: "op-2", Kind: "network-register", Network: &protocol.NetworkRegistration{ID: "n", PoolID: "p", HostID: "h", Kind: "network", Source: "default"}}, nil); err == nil {
		t.Fatal("corrupt journal accepted by Execute")
	}
	os.Remove(filepath.Join(e.stateDir, "bad.json"))
	if _, checks = e.Applied(context.Background()); checks[0].Name != "managed-journal" || checks[0].Status != "healthy" {
		t.Fatalf("intact journal not healthy: %+v", checks)
	}
	// A journal directory that does not exist yet is an empty state, not a failure.
	if got, checks = NewManaged(filepath.Join(t.TempDir(), "absent")).Applied(context.Background()); len(got.Storage) != 0 || checks[0].Status != "healthy" {
		t.Fatalf("absent journal: %+v %+v", got, checks)
	}
}

// Failed and needs_attention entries are reported as outcomes so a replaced Controller's task
// history matches the host; a pending entry nothing in this process executes was left behind
// by an earlier process and is reported as needs_attention.
func TestAppliedReportsOutcomes(t *testing.T) {
	e := NewManaged(t.TempDir())
	v := managedTestVM()
	for _, j := range []managedJournal{
		{Command: protocol.Command{OperationID: "op-fail", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "st-x", PoolID: "p", HostID: "h", Name: "Broken", Path: "/srv/broken"}}, Result: protocol.Result{OperationID: "op-fail", Status: "failed", Error: "storage is not writable"}},
		{Command: protocol.Command{OperationID: "op-vm", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "op-vm", Status: "needs_attention", Error: "disk allocation interrupted"}, Stage: "allocating"},
		{Command: protocol.Command{OperationID: "op-pending", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: v.ID, PoolID: "p", HostID: "h", Action: "start"}}, Result: protocol.Result{OperationID: "op-pending", Status: "pending"}},
	} {
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := e.Applied(context.Background())
	if len(got.Outcomes) != 3 || len(got.VMs) != 0 || len(got.Storage) != 0 {
		t.Fatalf("outcomes %+v", got)
	}
	by := map[string]protocol.ManagedOutcome{}
	for _, o := range got.Outcomes {
		by[o.OperationID] = o
	}
	if o := by["op-fail"]; o.Kind != "storage-register" || o.ID != "st-x" || o.Name != "Broken" || o.Status != "failed" || o.Error != "storage is not writable" {
		t.Fatalf("failed outcome %+v", o)
	}
	if o := by["op-vm"]; o.Kind != "vm-create" || o.ID != v.ID || o.Name != "guest" || o.Status != "needs_attention" {
		t.Fatalf("needs_attention outcome %+v", o)
	}
	if o := by["op-pending"]; o.Kind != "vm-power" || o.ID != v.ID || o.Status != "needs_attention" || o.Error != "interrupted before completion; retry from a new request" {
		t.Fatalf("orphaned pending outcome %+v", o)
	}
	if saved, err := e.readJournal("op-pending.json"); err != nil || saved.Result.Status != "pending" {
		t.Fatalf("orphaned entry must stay on disk as it was: %+v %v", saved, err)
	}
}

// A pending entry whose operation is executing in this process is in flight, not orphaned.
func TestAppliedSkipsPendingEntriesInFlight(t *testing.T) {
	e := startEngine(t)
	gate := filepath.Join(t.TempDir(), "go")
	t.Setenv("VMHOST_GATE", gate)
	fakeCommand(t, "virsh", `case "$3" in domstate) while [ ! -e "$VMHOST_GATE" ]; do sleep 0.02; done; printf 'shut off\n';; start) exit 0;; list) exit 0;; *) exit 2;; esac`)
	v := managedTestVM()
	c := protocol.Command{OperationID: "p-live", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: v.ID, PoolID: "p", HostID: "h", Action: "start"}}
	done := make(chan protocol.Result, 1)
	go func() { r, _ := e.Execute(context.Background(), c, nil); done <- r }()
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat(filepath.Join(e.stateDir, "p-live.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending entry never persisted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, _ := e.Applied(context.Background()); len(got.Outcomes) != 0 {
		t.Fatalf("executing operation reported as orphaned: %+v", got.Outcomes)
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Status != "succeeded" {
		t.Fatalf("power %+v", r)
	}
	if got, _ := e.Applied(context.Background()); len(got.Outcomes) != 0 {
		t.Fatalf("finished operation reported: %+v", got.Outcomes)
	}
}

// startLibvirt fakes libvirt for the start step: domstate answers $VMHOST_STATE, list names the
// domains in $VMHOST_DOMAINS, start fails when $VMHOST_FAIL is set, and every call is logged.
func startLibvirt(t *testing.T) func() string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "virsh.log")
	t.Setenv("VMHOST_LOG", log)
	fakeCommand(t, "virsh", `echo "$3 $4" >> "$VMHOST_LOG"; case "$3" in domstate) printf '%s\n' "$VMHOST_STATE";; list) printf '%s\n' $VMHOST_DOMAINS;; start) [ -n "$VMHOST_FAIL" ] && exit 1; exit 0;; *) exit 2;; esac`)
	return func() string { b, _ := os.ReadFile(log); os.Remove(log); return string(b) }
}

// A verified definition is a succeeded creation whatever the automatic start did: the failure
// reason lands in the journal's start field, is reported as a failed <operation>:start
// vm-power outcome while the VM is listed as a normal managed VM, and a retried creation
// neither starts the guest again nor loses the recorded reason.
func TestManagedStartFailureKeepsCreationSucceeded(t *testing.T) {
	e := startEngine(t)
	calls := startLibvirt(t)
	v := managedTestVM()
	v.StartAfterCreate = true
	c := protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}
	// Interrupted after the definition was verified, before the start step.
	j := managedJournal{Command: c, Result: protocol.Result{OperationID: "create", Status: "pending"}, Stage: "defined", Domain: []byte("<domain/>"), Disks: []string{"/srv/vms/disk-0.qcow2"}}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	e.observe = func(context.Context, managedJournal, string) error { return nil }
	t.Setenv("VMHOST_STATE", "shut off")
	t.Setenv("VMHOST_FAIL", "1")
	t.Setenv("VMHOST_DOMAINS", v.ID)
	r, err := e.Execute(context.Background(), c, nil)
	if err != nil || r.Status != "succeeded" || r.Error != "" {
		t.Fatalf("creation with a failed start: %+v %v", r, err)
	}
	reason := "libvirt refused the start request; inspect the VM on its host"
	if got := calls(); got != "domstate "+v.ID+"\nstart "+v.ID+"\n" {
		t.Fatalf("start calls %q", got)
	}
	saved, err := e.readJournal("create.json")
	if err != nil || saved.Start != reason || saved.Stage != "started" {
		t.Fatalf("start reason not recorded: %+v %v", saved, err)
	}
	got, checks := e.Applied(context.Background())
	if len(got.VMs) != 1 || got.VMs[0].ID != v.ID || len(got.MissingVMs) != 0 || checks[1].Status != "healthy" {
		t.Fatalf("VM not reported as managed: %+v %+v", got, checks)
	}
	want := protocol.ManagedOutcome{OperationID: "create:start", Kind: "vm-power", ID: v.ID, Name: "guest", Status: "failed", Error: "VM created but not started: " + reason}
	if len(got.Outcomes) != 1 || got.Outcomes[0] != want {
		t.Fatalf("start outcome %+v, want %+v", got.Outcomes, want)
	}
	calls()
	if r, err = e.Execute(context.Background(), c, nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("retry %+v %v", r, err)
	}
	if got := calls(); got != "" {
		t.Fatalf("retry touched libvirt: %q", got)
	}
	if saved, err = e.readJournal("create.json"); err != nil || saved.Start != reason {
		t.Fatalf("retry lost the start reason: %+v %v", saved, err)
	}
	// A start that succeeds reports no outcome.
	t.Setenv("VMHOST_FAIL", "")
	v2 := v
	v2.ID, v2.Name = "12345678-1234-4234-8234-123456789013", "guest2"
	c2 := protocol.Command{OperationID: "create-2", Kind: "vm-create", VM: &v2}
	j.Command, j.Result.OperationID = c2, "create-2"
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	if r, err = e.Execute(context.Background(), c2, nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("started creation %+v %v", r, err)
	}
	t.Setenv("VMHOST_DOMAINS", v.ID+" "+v2.ID)
	if got, _ = e.Applied(context.Background()); len(got.VMs) != 2 || len(got.Outcomes) != 1 || got.Outcomes[0].OperationID != "create:start" {
		t.Fatalf("started creation reported an outcome: %+v", got)
	}
}

// An entry that died at the start step is settled from libvirt: a running domain means the
// single start took effect; otherwise the start is recorded as interrupted and reported as a
// failed outcome. The creation is succeeded either way, on a re-sent operation and on report,
// and a reason an earlier process recorded is never overwritten.
func TestManagedInterruptedStartSettlesFromLibvirt(t *testing.T) {
	e := startEngine(t)
	calls := startLibvirt(t)
	seed := func(op, id string, status, start string) protocol.Command {
		v := managedTestVM()
		v.ID, v.Name, v.StartAfterCreate = id, "guest-"+op, true
		c := protocol.Command{OperationID: op, Kind: "vm-create", VM: &v}
		j := managedJournal{Command: c, Result: protocol.Result{OperationID: op, Status: status, ObservedAt: time.Now().Add(-2 * time.Minute)}, Stage: "starting", Start: start, Domain: []byte("<domain/>"), Disks: []string{"/srv/vms/disk-0.qcow2"}}
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
		return c
	}
	running := seed("running", "12345678-1234-4234-8234-123456789021", "pending", "")
	t.Setenv("VMHOST_STATE", "running")
	r, err := e.Execute(context.Background(), running, nil)
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("re-sent creation with a running domain: %+v %v", r, err)
	}
	if got := calls(); got != "domstate "+running.VM.ID+"\n" {
		t.Fatalf("settling must observe only: %q", got)
	}
	if saved, err := e.readJournal("running.json"); err != nil || saved.Start != "started" || saved.Stage != "started" {
		t.Fatalf("running domain not recorded as started: %+v %v", saved, err)
	}
	// Left behind by a dead process: report settles it without a re-sent operation.
	stopped := seed("stopped", "12345678-1234-4234-8234-123456789022", "pending", "")
	legacy := seed("legacy", "12345678-1234-4234-8234-123456789023", "needs_attention", "libvirt refused the start request; inspect the VM on its host")
	t.Setenv("VMHOST_STATE", "shut off")
	t.Setenv("VMHOST_DOMAINS", running.VM.ID+" "+stopped.VM.ID+" "+legacy.VM.ID)
	got, _ := e.Applied(context.Background())
	if len(got.VMs) != 3 || len(got.Outcomes) != 2 {
		t.Fatalf("settled creations: %+v", got)
	}
	by := map[string]protocol.ManagedOutcome{}
	for _, o := range got.Outcomes {
		by[o.OperationID] = o
	}
	if o := by["stopped:start"]; o.Kind != "vm-power" || o.Status != "failed" || o.ID != stopped.VM.ID || o.Error != "VM created but not started: interrupted before the automatic start completed" {
		t.Fatalf("interrupted start outcome %+v", o)
	}
	if o := by["legacy:start"]; o.Error != "VM created but not started: libvirt refused the start request; inspect the VM on its host" {
		t.Fatalf("recorded reason overwritten: %+v", o)
	}
	for _, op := range []string{"stopped", "legacy"} {
		if saved, err := e.readJournal(op + ".json"); err != nil || saved.Result.Status != "succeeded" || saved.Stage != "started" {
			t.Fatalf("%s not durably succeeded: %+v %v", op, saved, err)
		}
	}
	// A re-sent operation for the settled entry replays the durable result.
	calls()
	if r, err = e.Execute(context.Background(), stopped, nil); err != nil || r.Status != "succeeded" || calls() != "" {
		t.Fatalf("settled entry replay %+v %v", r, err)
	}
}

// A creation interrupted after its definition step is re-observed on report and promoted to
// succeeded when the domain matches; a replaced Controller never re-sends the operation, so
// without this the VM would never be adopted. Observation runs at most once a minute.
func TestAppliedPromotesInterruptedCreation(t *testing.T) {
	e := NewManaged(t.TempDir())
	v := managedTestVM()
	j := managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "create", Status: "needs_attention", Error: "interrupted", ObservedAt: time.Now().Add(-2 * time.Minute)}, Stage: "defined", Domain: []byte("<domain/>"), Disks: []string{"/srv/vms/disk-0.qcow2"}}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.observe = func(context.Context, managedJournal, string) error {
		calls++
		return errors.New("VM state is not confirmed stopped")
	}
	got, _ := e.Applied(context.Background())
	if calls != 1 || len(got.VMs) != 0 || len(got.Outcomes) != 1 || got.Outcomes[0].Error != "VM state is not confirmed stopped" {
		t.Fatalf("failed observation not recorded: calls=%d %+v", calls, got)
	}
	if got, _ = e.Applied(context.Background()); calls != 1 {
		t.Fatal("re-observed within a minute")
	}
	j.Result.ObservedAt = time.Now().Add(-2 * time.Minute)
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	e.observe = func(context.Context, managedJournal, string) error { calls++; return nil }
	fakeCommand(t, "virsh", "printf '%s\\n' "+v.ID)
	if got, _ = e.Applied(context.Background()); calls != 2 || len(got.VMs) != 1 || len(got.Outcomes) != 0 || len(got.MissingVMs) != 0 {
		t.Fatalf("matching domain not promoted: %+v", got)
	}
	saved, err := e.readJournal("create.json")
	if err != nil || saved.Result.Status != "succeeded" || saved.Stage != "defined" || saved.Result.Error != "" {
		t.Fatalf("promotion not durable: %+v %v", saved, err)
	}
	// An entry interrupted before the definition step has nothing to observe.
	j.Command.OperationID, j.Result.OperationID, j.Stage, j.Result.Status = "alloc", "alloc", "allocating", "needs_attention"
	v2 := v
	v2.ID = "12345678-1234-4234-8234-123456789013"
	j.Command.VM = &v2
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	if e.Applied(context.Background()); calls != 2 {
		t.Fatal("allocation-stage entry observed")
	}
}

// A journaled VM whose domain libvirt no longer lists is reported as missing with a failed
// check instead of being adopted as a live guest; when libvirt cannot answer nothing is flagged.
func TestAppliedFlagsVMsMissingFromLibvirt(t *testing.T) {
	e := NewManaged(t.TempDir())
	v := managedTestVM()
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}, Stage: "defined"}); err != nil {
		t.Fatal(err)
	}
	check := func(name string, checks []protocol.Check) protocol.Check {
		for _, c := range checks {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("check %s missing: %+v", name, checks)
		return protocol.Check{}
	}
	fakeCommand(t, "virsh", "exit 1")
	got, checks := e.Applied(context.Background())
	if len(got.VMs) != 1 || len(got.MissingVMs) != 0 || check("managed-vms", checks).Status != "unknown" {
		t.Fatalf("libvirt outage flagged a VM: %+v %+v", got, checks)
	}
	fakeCommand(t, "virsh", "printf 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee\\n'")
	got, checks = e.Applied(context.Background())
	if len(got.VMs) != 1 || len(got.MissingVMs) != 1 || got.MissingVMs[0] != v.ID {
		t.Fatalf("undefined VM not flagged: %+v", got)
	}
	if c := check("managed-vms", checks); c.Status != "failed" || !strings.Contains(c.Error, v.ID) {
		t.Fatalf("missing VM check %+v", c)
	}
	fakeCommand(t, "virsh", "printf '%s\\n' "+strings.ToUpper(v.ID))
	if got, checks = e.Applied(context.Background()); len(got.MissingVMs) != 0 || check("managed-vms", checks).Status != "healthy" {
		t.Fatalf("defined VM flagged: %+v %+v", got, checks)
	}
}

// The appliance engine's deploy journal is the host's ownership record of the Controller VM.
func TestDeploymentsListsApplianceOwnership(t *testing.T) {
	e := testEngine(t)
	if err := e.save(journal{Operation: Operation{ID: "deploy-1", VMID: "12345678-1234-4234-8234-123456789012", Kind: "deploy", Status: "booted"}, Name: "nex-controller"}); err != nil {
		t.Fatal(err)
	}
	if err := e.save(journal{Operation: Operation{ID: "power-1", VMID: "12345678-1234-4234-8234-123456789012", Kind: "power", Status: "completed"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.config.StateDir, "junk.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	got := Deployments(e.config.StateDir)
	if len(got) != 1 || got[0] != (protocol.Appliance{ID: "12345678-1234-4234-8234-123456789012", Name: "nex-controller", RequestID: "deploy-1", Role: "controller"}) {
		t.Fatalf("deployments %+v", got)
	}
	if Deployments(filepath.Join(e.config.StateDir, "absent")) != nil {
		t.Fatal("absent journal listed deployments")
	}
}

// testDomainXML builds a definition with creation-time target names.
func testDomainXML(v protocol.VMCreateSpec, nets []protocol.NetworkRegistration, disks []string, iso string) ([]byte, error) {
	targets, media := managedTargets(v)
	return managedDomainXML(v, nets, disks, targets, media, iso)
}
