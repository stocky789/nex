// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedMediaChange(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "virsh.log")
	isoDir := t.TempDir()
	isoPath := filepath.Join(isoDir, "media-1.iso")
	body, media := managedTestISO()
	if err := os.WriteFile(isoPath, body, 0444); err != nil {
		t.Fatal(err)
	}
	xmlWith := `<domain><devices><disk device="disk" type="file"><target dev="vda" bus="virtio"/></disk><disk device="cdrom" type="file"><source file="` + isoPath + `"/><target dev="sda" bus="sata"/><readonly/></disk></devices></domain>`
	xmlEmpty := `<domain><devices><disk device="disk" type="file"><target dev="vda" bus="virtio"/></disk><disk device="cdrom" type="file"><target dev="sda" bus="sata"/><readonly/></disk></devices></domain>`
	xmlNone := `<domain><devices><disk device="disk" type="file"><target dev="vda" bus="virtio"/></disk></devices></domain>`
	// change-media refuses an eject on media that is already empty, the way real virsh does
	// ("doesn't have media"; --force does not bypass it): VMHOST_TRAY_EMPTY simulates that so a
	// fix that still calls change-media for an already-empty eject is caught by the fake failing.
	fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_LOG"
case "$3" in
domstate) printf '%s\n' "$VMHOST_STATE";;
dumpxml) printf '%s\n' "$VMHOST_XML";;
attach-device) [ "$VMHOST_FAIL" = attach ] && exit 1; exit 0;;
change-media)
	[ "$VMHOST_FAIL" = change ] && exit 1
	case "$*" in *--eject*) [ "$VMHOST_TRAY_EMPTY" = 1 ] && exit 1;; esac
	exit 0;;
*) exit 2;;
esac`)
	t.Setenv("VMHOST_LOG", log)
	e := NewManaged(dir)
	vm := "11111111-2222-4333-a444-555555555555"
	ok := protocol.Result{OperationID: "create", Status: "succeeded"}
	create := managedJournal{
		Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &protocol.VMCreateSpec{
			ID: vm, PoolID: "pool1", HostID: "host1", Name: "guest", MediaID: "media-1",
			CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
			Disks: []protocol.VMDisk{{StorageID: "storage-1", Name: "system", SizeGiB: 10, Bus: "virtio", BootOrder: 1}},
			Boot:  protocol.VMBoot{Firmware: "bios", MediaBootOrder: 2},
		}},
		Result: ok, MediaPath: isoPath,
	}
	storage := managedJournal{Command: protocol.Command{OperationID: "st", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "storage-1", PoolID: "pool1", HostID: "host1", Name: "local", Path: isoDir}}, Result: protocol.Result{OperationID: "st", Status: "succeeded"}}
	iso := managedJournal{Command: protocol.Command{OperationID: "iso", Kind: "iso-import", Media: &media}, Result: protocol.Result{OperationID: "iso", Status: "succeeded"}, MediaPath: isoPath}
	for _, j := range []managedJournal{storage, iso, create} {
		b, _ := json.Marshal(j)
		if err := os.WriteFile(filepath.Join(dir, j.Command.OperationID+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := func() string { b, _ := os.ReadFile(log); os.Remove(log); return string(b) }
	command := func(op, action, mediaID string) protocol.Command {
		return protocol.Command{OperationID: op, Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: vm, PoolID: "pool1", HostID: "host1", Action: action, MediaID: mediaID}}
	}

	t.Setenv("VMHOST_STATE", "running")
	t.Setenv("VMHOST_XML", xmlWith)
	if r, err := e.Execute(context.Background(), command("m1", "eject", ""), nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("eject running: %v %+v", err, r)
	}
	got := calls()
	if !strings.Contains(got, "change-media "+vm+" sda --force --live --config --eject") {
		t.Fatalf("running eject argv %q", got)
	}
	applied := e.Applied()
	if len(applied.VMs) != 1 || applied.VMs[0].MediaID != "" || applied.VMs[0].Boot.MediaBootOrder != 0 {
		t.Fatalf("journal after eject %+v", applied.VMs)
	}

	t.Setenv("VMHOST_XML", xmlEmpty)
	if r, _ := e.Execute(context.Background(), command("m2", "insert", "media-1"), nil); r.Status != "succeeded" {
		t.Fatalf("insert running: %+v", r)
	}
	got = calls()
	if !strings.Contains(got, "change-media "+vm+" sda --force --live --config "+isoPath+" --update") {
		t.Fatalf("running insert argv %q", got)
	}
	applied = e.Applied()
	if applied.VMs[0].MediaID != "media-1" {
		t.Fatalf("journal after insert %+v", applied.VMs[0])
	}

	t.Setenv("VMHOST_STATE", "shut off")
	t.Setenv("VMHOST_XML", xmlNone)
	if r, _ := e.Execute(context.Background(), command("m3", "insert", "media-1"), nil); r.Status != "succeeded" {
		t.Fatalf("stopped insert without tray: %+v", r)
	}
	got = calls()
	if !strings.Contains(got, "attach-device "+vm) || !strings.Contains(got, "change-media "+vm+" sda --force --config "+isoPath+" --update") || strings.Contains(got, "--live") {
		t.Fatalf("stopped insert argv %q", got)
	}

	t.Setenv("VMHOST_STATE", "running")
	t.Setenv("VMHOST_XML", xmlNone)
	if r, _ := e.Execute(context.Background(), command("m4", "insert", "media-1"), nil); r.Status != "failed" || !strings.Contains(r.Error, "no CD drive while running") {
		t.Fatalf("running without tray must refuse: %+v", r)
	}

	// A tray that genuinely holds media (xmlWith): the eject must still call change-media and
	// surface a real libvirt refusal, distinct from the already-empty case tested below.
	t.Setenv("VMHOST_FAIL", "change")
	t.Setenv("VMHOST_XML", xmlWith)
	if r, _ := e.Execute(context.Background(), command("m5", "eject", ""), nil); r.Status != "failed" {
		t.Fatalf("refused change-media: %+v", r)
	}
	t.Setenv("VMHOST_FAIL", "")

	if r, _ := e.Execute(context.Background(), command("m6", "insert", ""), nil); r.Status != "failed" {
		t.Fatalf("insert without media_id: %+v", r)
	}
	if r, _ := e.Execute(context.Background(), command("m7", "explode", ""), nil); r.Status != "failed" {
		t.Fatalf("invalid action: %+v", r)
	}

	// Interrupted after change-media: reconcile from inactive dumpxml.
	j := managedJournal{Command: command("m8", "eject", ""), Result: protocol.Result{OperationID: "m8", Status: "pending"}, Stage: "changing-media"}
	b, _ := json.Marshal(j)
	os.WriteFile(filepath.Join(dir, "m8.json"), b, 0600)
	t.Setenv("VMHOST_XML", xmlEmpty)
	calls()
	if r, _ := e.Execute(context.Background(), command("m8", "eject", ""), nil); r.Status != "succeeded" || strings.Contains(calls(), "change-media") {
		t.Fatalf("interrupted eject already empty: %+v", r)
	}

	// A direct eject (not a reconciled interruption) on a tray that is already empty: nothing
	// to do at the libvirt level, so it must succeed without calling change-media or attaching
	// a tray. VMHOST_TRAY_EMPTY makes the fake refuse an eject the way real virsh would if the
	// fix regressed and this still reached change-media.
	t.Setenv("VMHOST_STATE", "shut off")
	t.Setenv("VMHOST_XML", xmlEmpty)
	t.Setenv("VMHOST_TRAY_EMPTY", "1")
	calls()
	if r, _ := e.Execute(context.Background(), command("m9", "eject", ""), nil); r.Status != "succeeded" {
		t.Fatalf("eject on already-empty tray: %+v", r)
	}
	if got := calls(); strings.Contains(got, "change-media") || strings.Contains(got, "attach-device") {
		t.Fatalf("eject on already-empty tray must not call virsh: %q", got)
	}

	// Eject with no CD drive defined at all: same outcome, and no tray is attached just to
	// immediately eject from it.
	t.Setenv("VMHOST_XML", xmlNone)
	if r, _ := e.Execute(context.Background(), command("m10", "eject", ""), nil); r.Status != "succeeded" {
		t.Fatalf("eject with no CD drive: %+v", r)
	}
	if got := calls(); strings.Contains(got, "change-media") || strings.Contains(got, "attach-device") {
		t.Fatalf("eject with no drive must not call virsh: %q", got)
	}
	t.Setenv("VMHOST_TRAY_EMPTY", "")

	// Stage "changing-media" means the virsh call was issued, so it must not be journalled
	// before the media path is resolved: an insert that never reaches virsh (here because the
	// ISO cannot be resolved, the same window a crash during ISO verification falls in) leaves
	// a journal that reconciles by re-applying, not a permanent "submit a new request" failure.
	t.Setenv("VMHOST_STATE", "shut off")
	t.Setenv("VMHOST_XML", xmlEmpty)
	if r, _ := e.Execute(context.Background(), command("m11", "insert", "gone"), nil); r.Status == "succeeded" {
		t.Fatalf("insert of an unknown ISO: %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(dir, "m11.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	if j.Stage == "changing-media" {
		t.Fatalf("nothing was issued, so the journal must not claim a media change was in flight: %+v", j)
	}
	// The same journal after an interruption: pending, not "changing-media", so reconcile
	// re-applies it instead of reporting a permanent failure.
	j.Command, j.Result = command("m11", "insert", "media-1"), protocol.Result{OperationID: "m11", Status: "pending"}
	b, _ = json.Marshal(j)
	if err = os.WriteFile(filepath.Join(dir, "m11.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	calls()
	if r, _ := e.Execute(context.Background(), command("m11", "insert", "media-1"), nil); r.Status != "succeeded" {
		t.Fatalf("interrupted insert before the media path was recorded must re-apply: %+v", r)
	}
	if got := calls(); !strings.Contains(got, "change-media "+vm+" sda --force --config "+isoPath+" --update") {
		t.Fatalf("re-applied insert argv %q", got)
	}
}

// An eject on a running VM must decide from the live definition. A tray changed outside Nex,
// or media attached as a host device rather than a file, leaves the persistent definition
// looking empty while the guest still holds the ISO; short-circuiting on that reports success
// without ejecting anything.
func TestManagedMediaEjectReadsLiveDefinition(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "virsh.log")
	vm := "11111111-2222-4333-a444-555555555555"
	// dumpxml --inactive shows an empty tray; the live definition holds media as <source dev>.
	fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_LOG"
case "$3" in
domstate) printf 'running
';;
dumpxml)
	case "$*" in
	*--inactive*) printf '%s
' "$VMHOST_XML_INACTIVE";;
	*) printf '%s
' "$VMHOST_XML";;
	esac;;
change-media) exit 0;;
*) exit 2;;
esac`)
	t.Setenv("VMHOST_LOG", log)
	t.Setenv("VMHOST_XML", `<domain><devices><disk device="cdrom" type="block"><source dev="/dev/sr0"/><target dev="sda" bus="sata"/><readonly/></disk></devices></domain>`)
	t.Setenv("VMHOST_XML_INACTIVE", `<domain><devices><disk device="cdrom" type="file"><target dev="sda" bus="sata"/><readonly/></disk></devices></domain>`)
	e := NewManaged(dir)
	create := managedJournal{
		Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &protocol.VMCreateSpec{
			ID: vm, PoolID: "pool1", HostID: "host1", Name: "guest",
			CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
			Disks: []protocol.VMDisk{{StorageID: "storage-1", Name: "system", SizeGiB: 10, Bus: "virtio", BootOrder: 1}},
			Boot:  protocol.VMBoot{Firmware: "bios"},
		}},
		Result: protocol.Result{OperationID: "create", Status: "succeeded"},
	}
	b, _ := json.Marshal(create)
	if err := os.WriteFile(filepath.Join(dir, "create.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	c := protocol.Command{OperationID: "live-eject", Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: vm, PoolID: "pool1", HostID: "host1", Action: "eject"}}
	if r, err := e.Execute(context.Background(), c, nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("eject: %v %+v", err, r)
	}
	got, _ := os.ReadFile(log)
	if !strings.Contains(string(got), "change-media "+vm+" sda --force --live --config --eject") {
		t.Fatalf("eject must issue change-media against the live tray: %q", got)
	}
}
