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
			v := protocol.VMCreateSpec{ID: "12345678-1234-4234-8234-123456789012", Name: "guest", CPUs: 2, MemoryMiB: 2048, Firmware: firmware}
			b, err := managedDomainXML(v, protocol.NetworkRegistration{Kind: kind, Source: "guest-net"}, "/disk&name", "/guest.iso")
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
			if (d.OS.Firmware == "efi") != (firmware == "uefi") || d.OS.Boot[0].Dev != "cdrom" {
				t.Fatal("firmware/boot mismatch")
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
	v := protocol.VMCreateSpec{ID: "12345678-1234-4234-8234-123456789012", PoolID: "p", HostID: "h", Name: "guest", CPUs: 1, MemoryMiB: 512, DiskGiB: 1, Firmware: "bios", StorageID: "s", NetworkID: "n", MediaID: "m"}
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
	v := protocol.VMCreateSpec{ID: "12345678-1234-4234-8234-123456789012", Name: "guest", CPUs: 2, MemoryMiB: 2048, Firmware: "uefi"}
	wanted, err := managedDomainXML(v, protocol.NetworkRegistration{Kind: "network", Source: "default"}, "/disk", "/iso")
	if err != nil {
		t.Fatal(err)
	}
	if err = validateManagedDefinition(wanted, wanted); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, new string }{
		{"firmware", `firmware="efi"`, `firmware="bios"`}, {"CPU", `mode="host-passthrough"`, `mode="host-model"`}, {"boot", `dev="cdrom"`, `dev="network"`}, {"disk bus", `bus="virtio"`, `bus="sata"`}, {"disk target", `dev="vda"`, `dev="vdb"`}, {"disk ownership", `file="/disk"`, `file="/other"`}, {"network", `network="default"`, `network="other"`}, {"VNC", `type="socket"`, `type="address"`},
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
	normalized := strings.Replace(string(wanted), `unit="MiB">2048`, `unit="KiB">2097152`, 1)
	normalized = strings.Replace(normalized, `machine="q35"`, `machine="pc-q35-10.0"`, 1)
	if err = validateManagedDefinition([]byte(normalized), wanted); err != nil {
		t.Fatalf("libvirt normalized definition rejected: %v", err)
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
	v := protocol.VMCreateSpec{ID: "12345678-1234-4234-8234-123456789012", PoolID: "new-pool", HostID: "new-host", StorageID: "s", NetworkID: "n", MediaID: "m"}
	s, err := managedStorage(js, v.StorageID)
	if err != nil || s.Path != "/srv/vms" {
		t.Fatalf("storage under old pool/host not resolved: %+v %v", s, err)
	}
	n, m, path, err := managedVMRefs(js, v)
	if err != nil || n.Source != "br0" || m.ID != "m" || path != "/srv/vms/nex-media/m.iso" {
		t.Fatalf("references under old pool/host not resolved: %+v %+v %q %v", n, m, path, err)
	}
	v.MediaID = "m2"
	if _, _, _, err := managedVMRefs(js, v); err == nil {
		t.Fatal("ISO on a different storage accepted")
	}
}
