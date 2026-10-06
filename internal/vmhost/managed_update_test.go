// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// A running guest takes device additions, removals and media changes; everything else waits
// for a stop, and a disk's storage, size and provisioning never change.
func TestNeedsStopClassifiesEdits(t *testing.T) {
	cur := managedTestVM()
	cur.NICs = []protocol.VMNIC{{NetworkID: "n", Model: "virtio"}, {NetworkID: "n", Model: "e1000e", MAC: "52:54:00:aa:bb:cc"}}
	hot := cur
	// Ejecting the ISO keeps the CD drive in the boot order; only the media changes.
	hot.Description, hot.GuestOS, hot.Autostart, hot.MediaID, hot.Tags = "notes", "windows", true, "", []string{"web"}
	hot.Disks = append(hot.Disks, protocol.VMDisk{StorageID: "s", Name: "data", SizeGiB: 5, Bus: "scsi"})
	// The first interface is dropped and the second, named by its MAC, keeps its settings.
	hot.NICs = []protocol.VMNIC{{NetworkID: "n", Model: "e1000e", MAC: "52:54:00:aa:bb:cc"}, {NetworkID: "n", Model: "virtio"}}
	if stops, err := protocol.NeedsStop(cur, hot); err != nil || len(stops) != 0 {
		t.Fatalf("hot edit classified %v %v", stops, err)
	}
	for name, mutate := range map[string]func(*protocol.VMCreateSpec){
		"name":       func(v *protocol.VMCreateSpec) { v.Name = "other" },
		"cpu":        func(v *protocol.VMCreateSpec) { v.CPU.Count = 4 },
		"memory":     func(v *protocol.VMCreateSpec) { v.Memory.SizeMiB = 4096 },
		"firmware":   func(v *protocol.VMCreateSpec) { v.Boot.Firmware = "bios" },
		"media boot": func(v *protocol.VMCreateSpec) { v.Boot.MediaBootOrder = 2; v.Disks[0].BootOrder = 1 },
		"disk bus":   func(v *protocol.VMCreateSpec) { v.Disks[0].Bus = "sata" },
		"disk boot":  func(v *protocol.VMCreateSpec) { v.Disks[0].BootOrder = 3 },
		"sata add": func(v *protocol.VMCreateSpec) {
			v.Disks = append(v.Disks, protocol.VMDisk{StorageID: "s", Name: "x", SizeGiB: 1, Bus: "sata"})
		},
		"nic model": func(v *protocol.VMCreateSpec) { v.NICs[1].Model = "virtio" },
		"nic mtu":   func(v *protocol.VMCreateSpec) { v.NICs[0].MAC = protocol.DerivedMAC(v.ID, 0); v.NICs[0].MTU = 9000 },
	} {
		next := cur
		next.Disks = append([]protocol.VMDisk{}, cur.Disks...)
		next.NICs = append([]protocol.VMNIC{}, cur.NICs...)
		mutate(&next)
		if stops, err := protocol.NeedsStop(cur, next); err != nil || len(stops) == 0 {
			t.Fatalf("%s: not classified as stop-first: %v %v", name, stops, err)
		}
	}
	for name, mutate := range map[string]func(*protocol.VMCreateSpec){
		"size":    func(v *protocol.VMCreateSpec) { v.Disks[0].SizeGiB = 40 },
		"storage": func(v *protocol.VMCreateSpec) { v.Disks[0].StorageID = "other" },
		"thin":    func(v *protocol.VMCreateSpec) { v.Disks[0].Thin = true },
	} {
		next := cur
		next.Disks = append([]protocol.VMDisk{}, cur.Disks...)
		mutate(&next)
		if _, err := protocol.NeedsStop(cur, next); err == nil {
			t.Fatalf("%s: immutable disk field accepted", name)
		}
	}
}

// Existing disks keep their paths and targets by name, a new disk lands on the first unused
// file index and the next free target letter, and a removed disk's file is retained.
func TestManagedUpdateLayoutKeepsExistingDisks(t *testing.T) {
	dir := t.TempDir()
	s := protocol.StorageRegistration{ID: "s", Path: dir}
	js := []managedJournal{{Command: protocol.Command{Kind: "storage-register", Storage: &s}, Result: protocol.Result{Status: "succeeded"}}}
	cur := managedTestVM()
	cur.Disks = []protocol.VMDisk{{StorageID: "s", Name: "system", SizeGiB: 20, Bus: "virtio", BootOrder: 2}, {StorageID: "s", Name: "old", SizeGiB: 5, Bus: "sata"}}
	vmDir := filepath.Join(dir, "nex-vms", cur.ID)
	curJ := managedJournal{Command: protocol.Command{Kind: "vm-create", VM: &cur}, Disks: []string{filepath.Join(vmDir, "disk-0.qcow2"), filepath.Join(vmDir, "disk-1.qcow2")}, Targets: []string{"vda", "sda"}, MediaTarget: "sdb"}
	// A retained file from an earlier removal must not be reused.
	os.MkdirAll(vmDir, 0755)
	os.WriteFile(filepath.Join(vmDir, "disk-2.qcow2"), nil, 0600)
	next := cur
	next.Disks = []protocol.VMDisk{{StorageID: "s", Name: "data", SizeGiB: 10, Bus: "scsi"}, {StorageID: "s", Name: "system", SizeGiB: 20, Bus: "virtio", BootOrder: 2}}
	l, err := managedUpdateLayout(js, curJ, next)
	if err != nil {
		t.Fatal(err)
	}
	if l.Disks[1] != curJ.Disks[0] || l.Targets[1] != "vda" || l.Disks[0] != filepath.Join(vmDir, "disk-3.qcow2") || l.Targets[0] != "sdc" || l.MediaTarget != "sdb" {
		t.Fatalf("layout %+v", l)
	}
	if len(l.New) != 1 || l.New[0] != 0 || len(l.Retained) != 1 || l.Retained[0] != curJ.Disks[1] {
		t.Fatalf("new %v retained %v", l.New, l.Retained)
	}
	if len(l.Targets_) != 1 || l.Targets_[0].Bytes != (4<<30)+(10<<30) {
		t.Fatalf("targets %+v", l.Targets_)
	}
}

// The current definition is the highest succeeded revision, and Applied reports it once.
func TestManagedCurrentAndAppliedFollowRevisions(t *testing.T) {
	e := managedTestEngine(t.TempDir())
	os.MkdirAll(e.stateDir, 0700)
	v0 := managedTestVM()
	v1, v2, failed := v0, v0, v0
	v1.Revision, v1.Description = 1, "first edit"
	v2.Revision, v2.Description, v2.Tags = 2, "second edit", []string{"prod", "web"}
	failed.Revision, failed.Description = 3, "never landed"
	for op, entry := range map[string]managedJournal{
		"op-create": {Command: protocol.Command{OperationID: "op-create", Kind: "vm-create", VM: &v0}, Result: protocol.Result{OperationID: "op-create", Status: "succeeded"}, Disks: []string{"/d0"}},
		"op-1":      {Command: protocol.Command{OperationID: "op-1", Kind: "vm-update", VM: &v1}, Result: protocol.Result{OperationID: "op-1", Status: "succeeded"}, Disks: []string{"/d0"}, Targets: []string{"vda"}, MediaTarget: "sda"},
		"op-2":      {Command: protocol.Command{OperationID: "op-2", Kind: "vm-update", VM: &v2}, Result: protocol.Result{OperationID: "op-2", Status: "succeeded"}, Disks: []string{"/d0"}, Targets: []string{"vda"}, MediaTarget: "sda"},
		"op-3":      {Command: protocol.Command{OperationID: "op-3", Kind: "vm-update", VM: &failed}, Result: protocol.Result{OperationID: "op-3", Status: "failed"}},
	} {
		_ = op
		if err := e.save(entry); err != nil {
			t.Fatal(err)
		}
	}
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	cur, err := managedCurrent(js, v0.ID)
	if err != nil || cur.Command.VM.Revision != 2 || cur.Command.VM.Description != "second edit" {
		t.Fatalf("current %+v %v", cur.Command.VM, err)
	}
	applied, _ := e.Applied(context.Background())
	if len(applied.VMs) != 1 || applied.VMs[0].Revision != 2 || strings.Join(applied.VMs[0].Tags, ",") != "prod,web" {
		t.Fatalf("applied %+v", applied.VMs)
	}
	// Creation journals predate target names; they are derived the way creation assigned them.
	entry := js[0]
	for _, j := range js {
		if j.Command.Kind == "vm-create" {
			entry = j
		}
	}
	if len(entry.Targets) != 0 {
		t.Fatal("fixture should lack targets")
	}
	only := []managedJournal{entry}
	if cur, err = managedCurrent(only, v0.ID); err != nil || cur.Targets[0] != "vda" || cur.MediaTarget != "sda" || cur.DriversTarget != "sdb" {
		t.Fatalf("derived targets %+v %v", cur, err)
	}
}

// The live path attaches what the persistent definition lacks, detaches what it should not
// have, swaps media on the CD drive, and never touches devices already in place.
func TestManagedApplyLiveReconcilesDevices(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	fragments := filepath.Join(t.TempDir(), "fragments")
	t.Setenv("MANAGED_CALLS", calls)
	t.Setenv("MANAGED_FRAGMENTS", fragments)
	cur := managedTestVM()
	cur.NICs = []protocol.VMNIC{{NetworkID: "n", Model: "virtio"}, {NetworkID: "n", Model: "e1000e", MAC: "52:54:00:aa:bb:cc"}}
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}, {Kind: "bridge", Source: "br0"}}
	actual, err := managedDomainXML(cur, nets, []string{"/vm/disk-0.qcow2"}, []string{"vda"}, "sda", "/iso/old.iso", "sdb", "")
	if err != nil {
		t.Fatal(err)
	}
	actualPath := filepath.Join(t.TempDir(), "actual.xml")
	os.WriteFile(actualPath, actual, 0600)
	fakeCommand(t, "virsh", `echo "$@" >> "$MANAGED_CALLS"
case "$*" in
*dumpxml*--inactive*) cat "`+actualPath+`";;
*dumpxml*) cat "${MANAGED_LIVE_XML:-`+actualPath+`}";;
*attach-device*) cat "$5" >> "$MANAGED_FRAGMENTS"; echo >> "$MANAGED_FRAGMENTS";;
esac`)
	next := cur
	next.Disks = append(next.Disks, protocol.VMDisk{StorageID: "s", Name: "data", SizeGiB: 5, Bus: "scsi"})
	next.NICs = []protocol.VMNIC{{NetworkID: "n", Model: "e1000e", MAC: "52:54:00:aa:bb:cc"}, {NetworkID: "n", Model: "virtio", MAC: "52:54:00:00:00:01"}}
	// The driver disc drive keeps sdb across the edit, the way managedUpdateLayout reserves it,
	// so the new disk takes the next free SATA letter rather than renaming a device.
	expected, err := managedDomainXML(next, nets, []string{"/vm/disk-0.qcow2", "/vm/disk-1.qcow2"}, []string{"vda", "sdc"}, "sda", "", "sdb", "")
	if err != nil {
		t.Fatal(err)
	}
	e := managedTestEngine(t.TempDir())
	os.MkdirAll(e.stateDir, 0700)
	if err = e.applyLive(context.Background(), cur.ID, expected, "", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{"dumpxml", "dumpxml", "attach-device", "attach-device", "attach-device", "detach-device", "dumpxml", "change-media"}
	if len(lines) != len(want) {
		t.Fatalf("calls:\n%s", b)
	}
	for i, line := range lines {
		if !strings.Contains(line, want[i]) {
			t.Fatalf("call %d %q, want %s", i, line, want[i])
		}
	}
	if !strings.Contains(lines[7], "sda --force --live --config --eject") {
		t.Fatalf("media eject argv: %s", lines[7])
	}
	if !strings.HasSuffix(lines[2], "--persistent") || !strings.HasSuffix(lines[5], "--persistent") {
		t.Fatalf("device operations must persist: %s", b)
	}
	attached, _ := os.ReadFile(fragments)
	if first := strings.SplitN(string(attached), "\n", 2)[0]; !strings.HasPrefix(first, "<controller ") || !strings.Contains(first, `iothread="1"`) {
		t.Fatalf("first hotplug must attach a real libvirt SCSI controller: %q", first)
	}
	// The guest can empty its own tray: the live drive is then already in the wanted state
	// and virsh refuses an eject on it, so only the persistent definition is changed.
	livePath := filepath.Join(t.TempDir(), "live.xml")
	live, err := managedDomainXML(cur, nets, []string{"/vm/disk-0.qcow2"}, []string{"vda"}, "sda", "", "sdb", "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(livePath, live, 0600)
	t.Setenv("MANAGED_LIVE_XML", livePath)
	os.Remove(calls)
	if err = e.applyLive(context.Background(), cur.ID, expected, "", ""); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(calls)
	media := ""
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.Contains(line, "change-media") {
			media = line
		}
	}
	if !strings.Contains(media, "sda --force --config --eject") || strings.Contains(media, "--live") {
		t.Fatalf("eject of a tray the guest already emptied: %q", b)
	}
	var got managedDomain
	xml.Unmarshal(expected, &got)
	if got.Devices.Disks[1].Target.Dev != "sdc" || got.Devices.Disks[2].Device != "cdrom" || got.Devices.Disks[2].Source != nil {
		t.Fatalf("expected definition %+v", got.Devices.Disks)
	}
}

func TestManagedAutomaticIOTuning(t *testing.T) {
	v := managedTestVM()
	v.NICs = append(v.NICs, protocol.VMNIC{NetworkID: "n", Model: "e1000e", MAC: "52:54:00:aa:bb:cc"})
	v.Disks = append(v.Disks, protocol.VMDisk{StorageID: "s", Name: "scsi", SizeGiB: 1, Bus: "scsi"}, protocol.VMDisk{StorageID: "s", Name: "sata", SizeGiB: 1, Bus: "sata"})
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}, {Kind: "bridge", Source: "br0"}}
	for _, count := range []int{1, 2, 4, 8} {
		v.CPU.Count = count
		b, err := testDomainXML(v, nets, []string{"/virtio", "/scsi", "/sata"}, "")
		if err != nil {
			t.Fatal(err)
		}
		var d managedDomain
		if err := xml.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		queues := min(count, 4)
		if d.IOThreads != 1 || d.Devices.Disks[0].Driver.IOThread != 1 || d.Devices.Disks[1].Driver.IOThread != 0 || d.Devices.Disks[2].Driver.IOThread != 0 || controllerIOThread(d.Devices.Controllers[0]) != 1 || nicQueues(d.Devices.Interfaces[0]) != queues || d.Devices.Interfaces[1].Driver != nil {
			t.Fatalf("%d vCPUs: generated device tuning differs: %s", count, b)
		}
		if count == 1 && d.Devices.Interfaces[0].Driver != nil {
			t.Fatalf("one-vCPU interface must use libvirt's single queue: %s", b)
		}
	}
	v.Disks = v.Disks[2:]
	b, err := testDomainXML(v, nets, []string{"/sata"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var d managedDomain
	xml.Unmarshal(b, &d)
	if d.IOThreads != 0 {
		t.Fatalf("SATA-only VM should not allocate IOThreads: %s", b)
	}
}

func TestManagedLiveEditPreservesExistingTuning(t *testing.T) {
	v := managedTestVM()
	v.CPU.Count = 4
	v.Disks = append(v.Disks, protocol.VMDisk{StorageID: "s", Name: "new", SizeGiB: 1, Bus: "virtio"})
	v.NICs = append(v.NICs, protocol.VMNIC{NetworkID: "n", Model: "virtio", MAC: "52:54:00:aa:bb:cc"})
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}, {Kind: "bridge", Source: "br0"}}
	expected, err := testDomainXML(v, nets, []string{"/old", "/new"}, "")
	if err != nil {
		t.Fatal(err)
	}
	old := v
	old.Disks, old.NICs = old.Disks[:1], old.NICs[:1]
	current, err := testDomainXML(old, nets[:1], []string{"/old"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got managedDomain
	xml.Unmarshal(current, &got)
	got.IOThreads = 0
	got.Devices.Disks[0].Driver.IOThread = 0
	got.Devices.Interfaces[0].Driver = nil
	updated, err := preserveLiveTuning(expected, got)
	if err != nil {
		t.Fatal(err)
	}
	var want managedDomain
	xml.Unmarshal(updated, &want)
	if want.IOThreads != 1 || want.Devices.Disks[0].Driver.IOThread != 0 || want.Devices.Disks[1].Driver.IOThread != 1 || want.Devices.Interfaces[0].Driver != nil || nicQueues(want.Devices.Interfaces[1]) != 4 {
		t.Fatalf("existing devices changed or new defaults missing: %s", updated)
	}
	got.Devices.Disks[0].Source.File = "/different"
	if _, err := preserveLiveTuning(expected, got); err == nil {
		t.Fatal("changed existing disk source accepted")
	}

	v.Disks[0].Bus = "scsi"
	v.Disks[1].Bus = "scsi"
	scsi, err := managedDomainXML(v, nets, []string{"/old", "/new"}, []string{"sda", "sdd"}, "sdb", "", "sdc", "")
	if err != nil {
		t.Fatal(err)
	}
	old.Disks[0].Bus = "scsi"
	current, err = testDomainXML(old, nets[:1], []string{"/old"}, "")
	if err != nil {
		t.Fatal(err)
	}
	got = managedDomain{}
	xml.Unmarshal(current, &got)
	got.IOThreads = 0
	got.Devices.Controllers[0].Driver = nil
	updated, err = preserveLiveTuning(scsi, got)
	if err != nil {
		t.Fatal(err)
	}
	want = managedDomain{}
	xml.Unmarshal(updated, &want)
	if want.IOThreads != 0 || controllerIOThread(want.Devices.Controllers[0]) != 0 {
		t.Fatalf("existing SCSI controller changed while running: %s", updated)
	}
}

func TestManagedLiveObservationRejectsConfigOnlyHotplug(t *testing.T) {
	var d managedDomain
	d.IOThreads = 2
	d.IOThreadIDs = &managedIOThreadIDs{Threads: []struct {
		ID int `xml:"id,attr"`
	}{{ID: 2}}}
	if !hasIOThread(d, 1) || !hasIOThread(d, 2) || hasIOThread(d, 3) {
		t.Fatal("implicit IOThread IDs were not normalized")
	}
	d.IOThreads = 1
	if hasIOThread(d, 1) || !hasIOThread(d, 2) {
		t.Fatal("explicit non-default IOThread ID mistaken for ID 1")
	}
	v := managedTestVM()
	v.CPU.Count = 4
	b, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var actual managedDomain
	xml.Unmarshal(b, &actual)
	if err := validateManagedDefinition(b, b); err != nil {
		t.Fatalf("persistent definition should match before a partial live attach: %v", err)
	}
	actual.Devices.Interfaces[0].Driver.Queues = 1
	live, _ := xml.Marshal(actual)
	if err := validateManagedLiveDevices(live, b); err == nil {
		t.Fatal("inactive-only NIC queue change accepted as a complete live edit")
	}
	actual.Devices.Interfaces[0].Driver.Queues = 4
	actual.Devices.Disks[0].Driver.IOThread = 0
	live, _ = xml.Marshal(actual)
	if err := validateManagedLiveDevices(live, b); err == nil {
		t.Fatal("inactive-only disk IOThread change accepted as a complete live edit")
	}
	actual.Devices.Disks[0].Driver.IOThread = 1
	actual.IOThreadIDs = &managedIOThreadIDs{Threads: []struct {
		ID int `xml:"id,attr"`
	}{{ID: 2}}}
	live, _ = xml.Marshal(actual)
	if err := validateManagedLiveDevices(live, b); err == nil {
		t.Fatal("thread count without ID 1 accepted")
	}
	actual.IOThreadIDs = nil
	actual.Devices.Interfaces = append(actual.Devices.Interfaces, managedNIC{}) // an unplug still pending
	live, _ = xml.Marshal(actual)
	if err := validateManagedLiveDevices(live, b); err != nil {
		t.Fatalf("pending extra live device should be harmless: %v", err)
	}
}

func TestManagedObserveDefinitionRejectsConfigOnlyLiveHotplug(t *testing.T) {
	dir := t.TempDir()
	v := managedTestVM()
	v.Disks = nil // No storage fixtures are needed to exercise the observer's live-device check.
	v.CPU.Count = 4
	expected, err := managedDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, nil, nil, "sda", "", "sdb", "")
	if err != nil {
		t.Fatal(err)
	}
	var actual managedDomain
	xml.Unmarshal(expected, &actual)
	actual.Devices.Interfaces[0].Driver.Queues = 1
	live, _ := xml.Marshal(actual)
	for name, body := range map[string][]byte{"persistent.xml": expected, "live.xml": live} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MANAGED_DIR", dir)
	fakeCommand(t, "virsh", `case "$3" in
dominfo) echo 'Autostart: disable';;
domstate) echo running;;
dumpxml) case "$*" in *--inactive*) cat "$MANAGED_DIR/persistent.xml";; *) cat "$MANAGED_DIR/live.xml";; esac;;
esac`)
	e := managedTestEngine(filepath.Join(dir, "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	j := managedJournal{Command: protocol.Command{Kind: "vm-update", VM: &v}, Domain: expected}
	if err := e.observeDefinition(context.Background(), j, "running"); err == nil || !strings.Contains(err.Error(), "running VM network queues") {
		t.Fatalf("observer accepted an inactive-only queue change: %v", err)
	}
}

func TestManagedLibvirtNetworkSourceNormalization(t *testing.T) {
	v := managedTestVM()
	expected, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "network", Source: "default"}}, []string{"/disk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var resolved managedDomain
	xml.Unmarshal(expected, &resolved)
	resolved.Devices.Interfaces[0].Source.Bridge = "virbr0" // libvirt resolves the network's current bridge in live XML.
	b, _ := xml.Marshal(resolved)
	if err := validateManagedLiveDevices(b, expected); err != nil {
		t.Fatalf("resolved bridge changed a managed network NIC: %v", err)
	}
	if err := validateManagedDefinition(b, expected); err != nil {
		t.Fatalf("resolved bridge changed persistent ownership: %v", err)
	}
	if _, err := preserveLiveTuning(expected, resolved); err != nil {
		t.Fatalf("resolved bridge blocked a live edit: %v", err)
	}
	resolved.Devices.Interfaces[0].Source.Network = "other"
	b, _ = xml.Marshal(resolved)
	if err := validateManagedLiveDevices(b, expected); err == nil {
		t.Fatal("different managed network accepted")
	}
}

func TestManagedApplyLiveRetriesPartialIOThreadAdd(t *testing.T) {
	dir := t.TempDir()
	v := managedTestVM()
	v.Disks = []protocol.VMDisk{{StorageID: "s", Name: "old", SizeGiB: 1, Bus: "sata"}}
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}
	initial, err := managedDomainXML(v, nets, []string{"/old"}, []string{"sda"}, "sdb", "", "sdc", "")
	if err != nil {
		t.Fatal(err)
	}
	var threaded managedDomain
	xml.Unmarshal(initial, &threaded)
	threaded.IOThreads = 1
	withThread, _ := xml.Marshal(threaded)
	v.Disks = append(v.Disks, protocol.VMDisk{StorageID: "s", Name: "new", SizeGiB: 1, Bus: "virtio"})
	expected, err := managedDomainXML(v, nets, []string{"/old", "/new"}, []string{"sda", "vda"}, "sdb", "", "sdc", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"initial.xml": initial, "threaded.xml": withThread} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MANAGED_DIR", dir)
	fakeCommand(t, "virsh", `echo "$@" >> "$MANAGED_DIR/calls"
case "$*" in
*dumpxml*--inactive*) if [ -e "$MANAGED_DIR/config" ]; then cat "$MANAGED_DIR/threaded.xml"; else cat "$MANAGED_DIR/initial.xml"; fi;;
*dumpxml*) if [ -e "$MANAGED_DIR/live" ]; then cat "$MANAGED_DIR/threaded.xml"; else cat "$MANAGED_DIR/initial.xml"; fi;;
*iothreadadd*)
  case "$*" in *--config*) touch "$MANAGED_DIR/config";; esac
  case "$*" in *--live*)
    if [ ! -e "$MANAGED_DIR/failed" ]; then touch "$MANAGED_DIR/failed"; exit 1; fi
    touch "$MANAGED_DIR/live";;
  esac;;
esac`)
	e := managedTestEngine(filepath.Join(dir, "state"))
	os.MkdirAll(e.stateDir, 0700)
	if err := e.applyLive(context.Background(), v.ID, expected, "", ""); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("partial IOThread add must remain uncertain: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Contains(string(calls), "attach-device") {
		t.Fatalf("disk attached before IOThread existed on both sides: %s", calls)
	}
	if err := e.applyLive(context.Background(), v.ID, expected, "", ""); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Count(string(calls), "iothreadadd") != 2 || strings.Count(string(calls), "attach-device") != 1 || !strings.Contains(string(calls), "iothreadadd "+v.ID+" 1 --live\n") {
		t.Fatalf("retry did not complete only the missing live side before attach: %s", calls)
	}
}

// Phase 2 gate open defect 1: detach-device --persistent drops an interface from the persistent
// definition at once, but the live device stays until the guest acknowledges the unplug. An edit
// that hot-plugs an interface with that address fails at validation, before any disk is allocated
// or device attached, so it is a plain failure rather than needs_attention; once the guest
// releases the interface the same edit succeeds.
func TestManagedUpdateRefusesMACStillLive(t *testing.T) {
	dir := t.TempDir()
	kept, pending := "52:54:00:aa:bb:02", "52:54:00:aa:bb:01"
	cur := managedTestVM()
	cur.MediaID, cur.Boot = "", protocol.VMBoot{Firmware: "bios"}
	cur.Disks[0].BootOrder = 1
	cur.Memory.Mode = "maximum"
	cur.NICs = []protocol.VMNIC{{NetworkID: "n", Model: "virtio", MAC: kept}}
	net := protocol.NetworkRegistration{ID: "n", PoolID: "p", HostID: "h", Kind: "network", Source: "default"}
	next := cur
	next.Revision = 1
	next.NICs = []protocol.VMNIC{{NetworkID: "n", Model: "virtio", MAC: kept}, {NetworkID: "n", Model: "virtio", MAC: pending}}
	nets := []protocol.NetworkRegistration{net, net}
	persistent, err := managedDomainXML(cur, nets[:1], []string{"/vm/disk-0.qcow2"}, []string{"vda"}, "sda", "", "sdb", "")
	if err != nil {
		t.Fatal(err)
	}
	after, err := managedDomainXML(next, nets, []string{"/vm/disk-0.qcow2"}, []string{"vda"}, "sda", "", "sdb", "")
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string][]byte{"persistent.xml": persistent, "after.xml": after}
	for name, b := range paths {
		if err = os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(dir, "calls")
	t.Setenv("MANAGED_CALLS", calls)
	t.Setenv("MANAGED_DIR", dir)
	// The live domain still holds the pending interface until the test releases it.
	t.Setenv("MANAGED_LIVE", filepath.Join(dir, "after.xml"))
	fakeCommand(t, "virsh", `echo "$@" >> "$MANAGED_CALLS"
case "$*" in
*domstate*) echo running;;
*net-list*) echo default;;
*dominfo*) echo "Autostart:      disable";;
*attach-device*) touch "$MANAGED_DIR/attached";;
*dumpxml*--inactive*) if [ -e "$MANAGED_DIR/attached" ]; then cat "$MANAGED_DIR/after.xml"; else cat "$MANAGED_DIR/persistent.xml"; fi;;
*dumpxml*) if [ -e "$MANAGED_DIR/attached" ]; then cat "$MANAGED_DIR/after.xml"; else cat "$MANAGED_LIVE"; fi;;
esac`)
	e := managedTestEngine(filepath.Join(dir, "state"))
	os.MkdirAll(e.stateDir, 0700)
	for _, entry := range []managedJournal{
		{Command: protocol.Command{OperationID: "op-net", Kind: "network-register", Network: &net}, Result: protocol.Result{OperationID: "op-net", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "op-create", Kind: "vm-create", VM: &cur}, Result: protocol.Result{OperationID: "op-create", Status: "succeeded"}, Domain: persistent, Disks: []string{"/vm/disk-0.qcow2"}, Targets: []string{"vda"}, MediaTarget: "sda", DriversTarget: "sdb"},
	} {
		if err = e.save(entry); err != nil {
			t.Fatal(err)
		}
	}
	r, err := e.Execute(context.Background(), protocol.Command{OperationID: "op-add", Kind: "vm-update", VM: &next}, nil)
	want := "network interface MAC " + pending + " is still attached to the running VM; wait for the guest to release it or stop the VM, then save again"
	if err != nil || r.Status != "failed" || r.Error != want {
		t.Fatalf("add of a MAC the guest has not released: %+v %v", r, err)
	}
	b, _ := os.ReadFile(calls)
	for _, effect := range []string{"attach-device", "detach-device", "change-media", "autostart"} {
		if strings.Contains(string(b), effect) {
			t.Fatalf("a refused edit must not touch the VM: %s", b)
		}
	}
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range js {
		if j.Command.OperationID == "op-add" && (j.Stage == "allocating" || j.Stage == "attaching" || len(j.Domain) != 0) {
			t.Fatalf("a refused edit must stop at validation: stage %q", j.Stage)
		}
	}
	// The guest released the interface: the next edit at the same revision passes validation and
	// attaches the interface once. This unit harness has no registered storage repository (that
	// needs root-owned mounts and the libvirt-qemu user), so the edit's final disk observation
	// cannot succeed here; the storage error it stops at proves the MAC check let it through.
	t.Setenv("MANAGED_LIVE", filepath.Join(dir, "persistent.xml"))
	os.Remove(calls)
	if r, err = e.Execute(context.Background(), protocol.Command{OperationID: "op-retry", Kind: "vm-update", VM: &next}, nil); err != nil || r.Error != "storage is not registered on this host" {
		t.Fatalf("retry after release: %+v %v", r, err)
	}
	b, _ = os.ReadFile(calls)
	if strings.Count(string(b), "attach-device") != 1 {
		t.Fatalf("retry after release must attach once: %s", b)
	}
}

// A vm-update that failed or needs_attention reserved nothing: Applied does not treat either
// as the current definition, the revision they named is still the next one the Controller
// sends, and the retry must reach the edit instead of the reservation.
func TestManagedFailedUpdateDoesNotReserveRevision(t *testing.T) {
	e := managedTestEngine(t.TempDir())
	os.MkdirAll(e.stateDir, 0700)
	v0 := managedTestVM()
	v1 := v0
	v1.Revision, v1.Description = 1, "first try"
	for _, entry := range []managedJournal{
		{Command: protocol.Command{OperationID: "op-create", Kind: "vm-create", VM: &v0}, Result: protocol.Result{OperationID: "op-create", Status: "succeeded"}, Disks: []string{"/d0"}, Targets: []string{"vda"}, MediaTarget: "sda"},
		{Command: protocol.Command{OperationID: "op-1", Kind: "vm-update", VM: &v1}, Result: protocol.Result{OperationID: "op-1", Status: "failed", Error: "VM is running; stop it first"}, Stage: "validating"},
	} {
		if err := e.save(entry); err != nil {
			t.Fatal(err)
		}
	}
	fakeCommand(t, "virsh", "echo crashed")
	retry := v1
	retry.Description = "second try"
	r, err := e.Execute(context.Background(), protocol.Command{OperationID: "op-2", Kind: "vm-update", VM: &retry}, nil)
	if err != nil || r.Status != "failed" || !strings.HasPrefix(r.Error, "VM is crashed") {
		t.Fatalf("retry did not reach the edit: %+v %v", r, err)
	}
}

// needs_attention after define (for example libvirt CPU check mismatch) must not permanently
// lock the revision: Applied never promotes that entry to the current definition.
func TestManagedNeedsAttentionUpdateDoesNotReserveRevision(t *testing.T) {
	e := managedTestEngine(t.TempDir())
	os.MkdirAll(e.stateDir, 0700)
	v0 := managedTestVM()
	v1 := v0
	v1.Revision, v1.Description = 1, "first try"
	for _, entry := range []managedJournal{
		{Command: protocol.Command{OperationID: "op-create", Kind: "vm-create", VM: &v0}, Result: protocol.Result{OperationID: "op-create", Status: "succeeded"}, Disks: []string{"/d0"}, Targets: []string{"vda"}, MediaTarget: "sda"},
		{Command: protocol.Command{OperationID: "op-1", Kind: "vm-update", VM: &v1}, Result: protocol.Result{OperationID: "op-1", Status: "needs_attention", Error: "VM CPU or machine changed"}, Stage: "defining", Domain: []byte("<domain/>"), Disks: []string{"/d0"}, Targets: []string{"vda"}, MediaTarget: "sda"},
	} {
		if err := e.save(entry); err != nil {
			t.Fatal(err)
		}
	}
	fakeCommand(t, "virsh", "echo crashed")
	retry := v1
	retry.Description = "second try"
	r, err := e.Execute(context.Background(), protocol.Command{OperationID: "op-2", Kind: "vm-update", VM: &retry}, nil)
	if err != nil || r.Status != "failed" || !strings.HasPrefix(r.Error, "VM is crashed") {
		t.Fatalf("retry did not reach the edit: %+v %v", r, err)
	}
}

// An edit a dead agent process left at defining, attaching or defined is settled on report the
// way a re-sent operation would be: a matching persistent definition means it succeeded, in
// the state the edit left the guest.
func TestAppliedPromotesInterruptedEdit(t *testing.T) {
	e := managedTestEngine(t.TempDir())
	os.MkdirAll(e.stateDir, 0700)
	v0 := managedTestVM()
	v1 := v0
	v1.Revision, v1.Description = 1, "edited"
	create := managedJournal{Command: protocol.Command{OperationID: "op-create", Kind: "vm-create", VM: &v0}, Result: protocol.Result{OperationID: "op-create", Status: "succeeded"}, Stage: "defined", Disks: []string{"/d0"}, Targets: []string{"vda"}, MediaTarget: "sda"}
	edit := managedJournal{Command: protocol.Command{OperationID: "op-1", Kind: "vm-update", VM: &v1}, Result: protocol.Result{OperationID: "op-1", Status: "pending", ObservedAt: time.Now().Add(-2 * time.Minute)}, Stage: "defined", Domain: []byte("<domain/>"), Disks: []string{"/d0"}, Targets: []string{"vda"}, MediaTarget: "sda"}
	for _, entry := range []managedJournal{create, edit} {
		if err := e.save(entry); err != nil {
			t.Fatal(err)
		}
	}
	var states []string
	e.observe = func(_ context.Context, j managedJournal, state string) error {
		states = append(states, j.Command.OperationID+":"+state)
		return nil
	}
	fakeCommand(t, "virsh", "printf '%s\\n' "+v0.ID)
	got, _ := e.Applied(context.Background())
	if len(states) != 1 || states[0] != "op-1:shut off" || len(got.Outcomes) != 2 || got.Outcomes[0].Status != "succeeded" || got.Outcomes[1].Status != "succeeded" || len(got.VMs) != 1 || got.VMs[0].Revision != 1 {
		t.Fatalf("interrupted edit not promoted: %v %+v", states, got)
	}
	saved, err := e.readJournal("op-1.json")
	if err != nil || saved.Result.Status != "succeeded" || saved.Stage != "defined" || saved.Result.Error != "" {
		t.Fatalf("promotion not durable: %+v %v", saved, err)
	}
	// An edit interrupted while attaching devices left the guest running.
	edit.Command.OperationID, edit.Result.OperationID, edit.Stage, edit.Result.Status = "op-2", "op-2", "attaching", "needs_attention"
	v2 := v1
	v2.Revision = 2
	edit.Command.VM = &v2
	if err := e.save(edit); err != nil {
		t.Fatal(err)
	}
	if got, _ = e.Applied(context.Background()); len(states) != 2 || states[1] != "op-2:running" || got.VMs[0].Revision != 2 {
		t.Fatalf("attaching edit not promoted: %v %+v", states, got)
	}
}
