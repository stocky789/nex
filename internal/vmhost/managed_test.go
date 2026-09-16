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
	"os/exec"
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
			// SMB mounts force file_mode=0600, so chmod 0444 cannot stick there;
			// content integrity rests on size and SHA-256, not permission bits.
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			if err := verifyISO(path, m); err != nil {
				t.Fatalf("SMB-mode ISO refused: %v", err)
			}
			if err := os.WriteFile(path, append(append([]byte{}, body...), 0), 0600); err != nil {
				t.Fatal(err)
			}
			if verifyISO(path, m) == nil {
				t.Fatal("changed ISO accepted")
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

func TestStorageProjectionUsesGenerationsAndTombstones(t *testing.T) {
	base := protocol.StorageRegistration{ID: "storage-1", PoolID: "pool-1", HostID: "host-1", Name: "old", Path: "/srv/old"}
	next := protocol.StorageRegistration{Name: "new", Path: "/srv/new"}
	js := []managedJournal{
		// Deliberately reverse a natural event order; journal filenames have no sequence.
		{Command: protocol.Command{Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: base.ID, PoolID: base.PoolID, HostID: base.HostID, ExpectedGeneration: 2}}, Result: protocol.Result{Status: "succeeded"}},
		{Command: protocol.Command{Kind: "storage-update", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "update", RepositoryID: base.ID, PoolID: base.PoolID, HostID: base.HostID, ExpectedGeneration: 1, Registration: &next}}, Result: protocol.Result{Status: "succeeded"}, AppliedStorage: &protocol.StorageRegistration{ID: base.ID, PoolID: base.PoolID, HostID: base.HostID, Name: "new", Path: "/srv/new", Generation: 2}},
		{Command: protocol.Command{Kind: "storage-register", Storage: &base}, Result: protocol.Result{Status: "succeeded"}},
	}
	if got := storageProjection(js); len(got) != 0 {
		t.Fatalf("detached storage reappeared from historical journal: %+v", got)
	}
}

func TestStorageProjectionUsesCoreAllocatedRegistration(t *testing.T) {
	request := protocol.StorageRegistration{PoolID: "pool-1", HostID: "host-1", Name: "remote", Kind: protocol.StorageKindNFS}
	applied := request
	applied.ID, applied.Generation, applied.Path = "core-storage-1", 1, "/var/lib/nex-storage/core-storage-1"
	applied.Capabilities = storageCapabilities(applied)
	js := []managedJournal{{Command: protocol.Command{Kind: "storage-register", Storage: &request}, Result: protocol.Result{Status: "succeeded"}, AppliedStorage: &applied}}
	got, err := managedStorage(js, applied.ID)
	if err != nil || got.ID != applied.ID || got.Generation != 1 || !got.Capabilities.VMDisk || !got.Capabilities.ISO {
		t.Fatalf("Core allocation was not projected: %+v %v", got, err)
	}
}

func TestStorageCorePathsAllocatesISCSIMount(t *testing.T) {
	s := protocol.StorageRegistration{ID: "core-storage-1", Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{TargetIQN: "iqn.2026-01.example:vm"}}}
	storageCorePaths(&s)
	if got, want := s.Source.ISCSI.MountPath, filepath.Join(storageMountRoot, s.ID); got != want {
		t.Fatalf("iSCSI mount path = %q, want Core path %q", got, want)
	}
}

func TestStorageProjectionUpgradesLegacyClusteredISCSICapabilities(t *testing.T) {
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Generation: 1, Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2", EligibleHostIDs: []string{"host-1", "host-2", "host-3"}}}}
	got := storageProjection([]managedJournal{{Command: protocol.Command{Kind: "storage-register", Storage: &storage}, Result: protocol.Result{Status: "succeeded"}, AppliedStorage: &storage}})[storage.ID]
	if !got.Capabilities.VMDisk || !got.Capabilities.Shared || !reflect.DeepEqual(got.Capabilities.EligibleHostIDs, storage.Source.ISCSI.EligibleHostIDs) {
		t.Fatalf("legacy clustered iSCSI capabilities = %#v", got.Capabilities)
	}
}

func TestClusteredISCSIUpdateRefusesBeforeNativePreparation(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("clustered update issued a native command")
		return nil, nil
	}
	current := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Generation: 1, Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2"}}}
	command := protocol.Command{Kind: "storage-update", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "update", RepositoryID: current.ID, PoolID: current.PoolID, HostID: current.HostID, ExpectedGeneration: 1, Registration: &protocol.StorageRegistration{Name: "renamed"}}}
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	err := e.apply(context.Background(), &managedJournal{Command: command}, []managedJournal{{Command: protocol.Command{Kind: "storage-register", Storage: &current}, Result: protocol.Result{Status: "succeeded"}, AppliedStorage: &current}}, nil)
	if err == nil || !strings.Contains(err.Error(), "coordinated") {
		t.Fatalf("clustered update = %v", err)
	}
}

func TestClusteredISCSIDetachRefusesBeforeNativePreparation(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("clustered detach issued a native command")
		return nil, nil
	}
	current := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Generation: 1, Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2"}}}
	command := protocol.Command{Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: current.ID, PoolID: current.PoolID, HostID: current.HostID, ExpectedGeneration: 1}}
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	err := e.apply(context.Background(), &managedJournal{Command: command}, []managedJournal{{Command: protocol.Command{Kind: "storage-register", Storage: &current}, Result: protocol.Result{Status: "succeeded"}, AppliedStorage: &current}}, nil)
	if err == nil || !strings.Contains(err.Error(), "coordinated") {
		t.Fatalf("clustered detach = %v", err)
	}
}

func TestClusteredISCSIRegistrationRestoresOnlyTrustedConfigMountPath(t *testing.T) {
	config := clusterTestConfig()
	storage := protocol.StorageRegistration{ID: config.RepositoryID, PoolID: config.PoolID, HostID: "host-1", Generation: config.Generation, Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{
		Mode: "clustered_gfs2", ClusterID: config.ClusterID, LockTable: config.LockTable, MountPath: config.MountPath,
		EligibleHostIDs: []string{"host-1", "host-2", "host-3"}, TargetIQN: "iqn.2026-09.example.test:nex", WWID: "3600140509f6f9e020ef432c841051db1", FilesystemUUID: "11111111-2222-3333-4444-555555555555",
	}}}
	if err := restoreClusteredRegistrationPath(WithStorageClusterConfig(context.Background(), config), &storage); err != nil {
		t.Fatal(err)
	}
	if storage.Path != config.MountPath {
		t.Fatalf("trusted clustered mount path = %q, want %q", storage.Path, config.MountPath)
	}
	storage.Path = ""
	storage.Source.ISCSI.MountPath = "/untrusted"
	if err := restoreClusteredRegistrationPath(WithStorageClusterConfig(context.Background(), config), &storage); err == nil {
		t.Fatal("untrusted clustered mount path was accepted")
	}
}

func TestISCSIRegistrationConflictPreventsSecondSessionPreparation(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	calls := 0
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("native preparation must not run")
	}
	iscsi := func(portal, id string, lun uint64) protocol.StorageRegistration {
		return protocol.StorageRegistration{ID: id, PoolID: "pool-1", HostID: "host-1", Name: id, Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: portal, TargetIQN: "iqn.2026-01.example:vm", LUN: lun, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", ExclusiveTarget: true}}}
	}
	e := NewManaged(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	firstRequest := iscsi("iscsi.example.test", "", 0)
	firstApplied := iscsi("iscsi.example.test", "first", 0)
	firstApplied.Source.ISCSI.MountPath, firstApplied.Path = filepath.Join(storageMountRoot, "first"), filepath.Join(storageMountRoot, "first")
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "first-op", Kind: "storage-register", Storage: &firstRequest}, Result: protocol.Result{OperationID: "first-op", Status: "succeeded"}, AppliedStorage: &firstApplied}); err != nil {
		t.Fatal(err)
	}
	second := iscsi("other-portal.example.test", "", 1) // distinct Core mount path and portal alias
	second.Source.ISCSI.WWID = "0x" + second.Source.ISCSI.WWID
	second.Source.ISCSI.FilesystemUUID = strings.ToUpper(second.Source.ISCSI.FilesystemUUID)
	r, err := e.Execute(context.Background(), protocol.Command{OperationID: "second-op", Kind: "storage-register", Storage: &second}, nil)
	if err != nil || r.Status != "failed" || calls != 0 {
		t.Fatalf("second iSCSI registration = %+v, %v, native calls=%d", r, err, calls)
	}
	alias := iscsi("other-portal.example.test", "second", 2)
	alias.Source.ISCSI.MountPath = filepath.Join(storageMountRoot, "second")
	if err := registrationConflict([]managedJournal{{Command: protocol.Command{OperationID: "first-op", Kind: "storage-register", Storage: &firstRequest}, Result: protocol.Result{OperationID: "first-op", Status: "needs_attention"}, AppliedStorage: &firstApplied}}, protocol.Command{OperationID: "alias-op", Kind: "storage-register", Storage: &alias}); err == nil {
		t.Fatal("portal alias for the same iSCSI WWID/filesystem was accepted")
	}
}

func TestISCSISourceChangeIgnoresCoreMountPathButConflictsWithOtherRepository(t *testing.T) {
	base := protocol.StorageRegistration{ID: "one", Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "iscsi.example.test", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", MountPath: filepath.Join(storageMountRoot, "one"), ExclusiveTarget: true}}}
	otherPath := base
	otherPath.Source.ISCSI = &protocol.ISCSIStorageSource{Portal: base.Source.ISCSI.Portal, TargetIQN: base.Source.ISCSI.TargetIQN, WWID: base.Source.ISCSI.WWID, FilesystemUUID: base.Source.ISCSI.FilesystemUUID, MountPath: filepath.Join(storageMountRoot, "two"), ExclusiveTarget: true}
	if storageSourceChanged(base, otherPath) {
		t.Fatal("Core-generated iSCSI mount path was treated as a source mutation")
	}
	otherLUN := otherPath
	otherLUN.Source.ISCSI = &protocol.ISCSIStorageSource{Portal: otherPath.Source.ISCSI.Portal, TargetIQN: otherPath.Source.ISCSI.TargetIQN, WWID: otherPath.Source.ISCSI.WWID, FilesystemUUID: otherPath.Source.ISCSI.FilesystemUUID, MountPath: otherPath.Source.ISCSI.MountPath, ExclusiveTarget: otherPath.Source.ISCSI.ExclusiveTarget}
	otherLUN.Source.ISCSI.LUN = 1
	if !storageSourceChanged(base, otherLUN) {
		t.Fatal("iSCSI LUN update was not treated as a source mutation")
	}
	if !storageSourcesConflict(base, otherLUN) {
		t.Fatal("source update could claim another repository's iSCSI session")
	}
	formatted := otherPath
	formatted.Source.ISCSI = &protocol.ISCSIStorageSource{Portal: otherPath.Source.ISCSI.Portal, TargetIQN: otherPath.Source.ISCSI.TargetIQN, WWID: "0x" + otherPath.Source.ISCSI.WWID, FilesystemUUID: strings.ToUpper(otherPath.Source.ISCSI.FilesystemUUID), MountPath: otherPath.Source.ISCSI.MountPath, ExclusiveTarget: true}
	if storageSourceChanged(otherPath, formatted) {
		t.Fatal("equivalent iSCSI WWID/UUID spelling triggered a source update")
	}
	observedA := protocol.StorageIdentity{ISCSI: &protocol.ISCSIStorageIdentity{WWID: otherPath.Source.ISCSI.WWID, FilesystemUUID: otherPath.Source.ISCSI.FilesystemUUID}}
	observedB := protocol.StorageIdentity{ISCSI: &protocol.ISCSIStorageIdentity{WWID: formatted.Source.ISCSI.WWID, FilesystemUUID: formatted.Source.ISCSI.FilesystemUUID}}
	if !storageSameBacking(observedA, observedB) {
		t.Fatal("equivalent observed iSCSI WWID/UUID spelling was not recognized")
	}
	// A successful historical command is folded through active projection; after a
	// detach it must not reserve its old source forever. Unresolved work still does.
	firstCommand := base
	firstCommand.ID = "one"
	candidate := otherPath
	candidate.ID = "two"
	detached := []managedJournal{
		{Command: protocol.Command{OperationID: "one-register", Kind: "storage-register", Storage: &firstCommand}, Result: protocol.Result{OperationID: "one-register", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "one-detach", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "one", PoolID: "pool-1", HostID: "host-1", ExpectedGeneration: 1}}, Result: protocol.Result{OperationID: "one-detach", Status: "succeeded"}},
	}
	if err := registrationConflict(detached, protocol.Command{OperationID: "two-register", Kind: "storage-register", Storage: &candidate}); err != nil {
		t.Fatalf("detached iSCSI source remained reserved: %v", err)
	}
	pending := []managedJournal{{Command: protocol.Command{OperationID: "one-pending", Kind: "storage-register", Storage: &firstCommand}, Result: protocol.Result{OperationID: "one-pending", Status: "pending"}}}
	if err := registrationConflict(pending, protocol.Command{OperationID: "two-pending", Kind: "storage-register", Storage: &candidate}); err == nil {
		t.Fatal("pending iSCSI source was released")
	}
	active := []managedJournal{{Command: protocol.Command{OperationID: "one-register", Kind: "storage-register", Storage: &firstCommand}, Result: protocol.Result{OperationID: "one-register", Status: "succeeded"}}}
	updatedOwn := otherLUN
	updatedOwn.ID = "one"
	if err := registrationConflict(active, protocol.Command{OperationID: "one-update", Kind: "storage-register", Storage: &updatedOwn}); err != nil {
		t.Fatalf("own iSCSI source update conflicted with immutable history: %v", err)
	}
	if err := registrationConflict(active, protocol.Command{OperationID: "two-update", Kind: "storage-register", Storage: &candidate}); err == nil {
		t.Fatal("iSCSI source update could claim another active repository session")
	}
}

func TestZFSCoreAliasIsProjectedWithoutRepointingLegacyMount(t *testing.T) {
	alias := filepath.Join(storageMountRoot, "zfs-new")
	legacy := protocol.StorageRegistration{
		ID: "zfs-legacy", PoolID: "pool-1", HostID: "host-1", Name: "legacy", Kind: protocol.StorageKindZFS,
		Path: "/srv/prototype", Generation: 1,
		Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "tank", Dataset: "prototype", MountPath: "/srv/prototype"}},
	}
	newRequest := protocol.StorageRegistration{
		PoolID: "pool-1", HostID: "host-1", Name: "new", Kind: protocol.StorageKindZFS,
		Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "tank", Dataset: "final", MountPath: "/srv/final"}},
	}
	newApplied := newRequest
	newApplied.ID, newApplied.Generation, newApplied.Path = "zfs-new", 1, alias
	js := []managedJournal{
		{Command: protocol.Command{Kind: "storage-register", Storage: &legacy}, Result: protocol.Result{Status: "succeeded"}},
		{Command: protocol.Command{Kind: "storage-register", Storage: &newRequest}, Result: protocol.Result{Status: "succeeded"}, AppliedStorage: &newApplied},
	}
	projected := storageProjection(js)
	if got := storageRegistrationPath(projected["zfs-new"]); got != alias {
		t.Fatalf("new ZFS repository path = %q, want Core alias %q", got, alias)
	}
	if got := storageRegistrationPath(projected["zfs-legacy"]); got != "/srv/prototype" {
		t.Fatalf("legacy ZFS repository was repointed to %q", got)
	}
	v := managedTestVM()
	v.Disks = []protocol.VMDisk{{StorageID: "zfs-new", Name: "system", SizeGiB: 20, Bus: "virtio", Thin: true}}
	targets, _, err := managedDiskPlan(js, v)
	if err != nil || len(targets) != 1 || targets[0].Path != alias {
		t.Fatalf("new VM target = %+v, %v; want Core alias", targets, err)
	}
}

func TestStorageWasPreparedUsesCoreAllocatedRepositoryID(t *testing.T) {
	request := protocol.StorageRegistration{PoolID: "pool-1", HostID: "host-1", Name: "zfs", Kind: protocol.StorageKindZFS}
	applied := request
	applied.ID = "core-zfs"
	if !storageWasPrepared([]managedJournal{{Command: protocol.Command{Kind: "storage-register", Storage: &request}, AppliedStorage: &applied}}, applied.ID) {
		t.Fatal("Core-allocated repository was not recognized as prepared")
	}
}

func TestSMBMarkerBackingConflictPrefersDurableMarker(t *testing.T) {
	identity := func(marker, fs string) protocol.StorageIdentity {
		return protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: "files.example.test", Share: "vms", MarkerID: marker, FilesystemID: fs}}
	}
	markerA := "0123456789abcdef0123456789abcdef"
	markerB := "fedcba9876543210fedcba9876543210"
	if !storageSameBacking(identity(markerA, "volatile-one"), identity(markerA, "volatile-two")) {
		t.Fatal("same durable SMB marker was not recognized across aliases")
	}
	if storageSameBacking(identity(markerA, "shared-diagnostic"), identity(markerB, "shared-diagnostic")) {
		t.Fatal("distinct durable SMB markers collided on a volatile filesystem ID")
	}
	if !storageSameBacking(identity("", "legacy-fs"), identity("", "legacy-fs")) {
		t.Fatal("legacy SMB filesystem identity was not retained")
	}
	if !storageSameBacking(identity(markerA, "legacy-fs"), identity("", "legacy-fs")) {
		t.Fatal("marker and legacy SMB identities lost their matching legacy filesystem collision")
	}
	if storageSameBacking(identity(markerA, "marker-fs"), identity("", "legacy-fs")) {
		t.Fatal("mixed SMB profiles with distinct filesystem IDs collided")
	}

	storage := func(id, server, share, marker, fs string) protocol.StorageRegistration {
		return protocol.StorageRegistration{ID: id, PoolID: "pool-1", HostID: "host-1", Name: id, Kind: protocol.StorageKindSMB, Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: server, Share: share}}, Identity: identity(marker, fs)}
	}
	old := storage("first", "one.example.test", "vms", markerA, "volatile-one")
	sameMarker := storage("second", "two.example.test", "other", markerA, "volatile-two")
	differentMarker := storage("third", "three.example.test", "other", markerB, "volatile-one")
	legacyDifferentFS := storage("legacy-other", "four.example.test", "other", "", "legacy-fs")
	legacySameFS := storage("legacy-same", "five.example.test", "other", "", "volatile-one")
	for _, status := range []string{"succeeded", "pending", "needs_attention"} {
		t.Run(status, func(t *testing.T) {
			js := []managedJournal{{Command: protocol.Command{OperationID: "first", Kind: "storage-register", Storage: &old}, Result: protocol.Result{Status: status}}}
			if err := registrationConflict(js, protocol.Command{OperationID: "second", Kind: "storage-register", Storage: &sameMarker}); err == nil {
				t.Fatal("same-marker SMB alias accepted")
			}
			if err := registrationConflict(js, protocol.Command{OperationID: "third", Kind: "storage-register", Storage: &differentMarker}); err != nil {
				t.Fatalf("different-marker SMB alias collided on filesystem diagnostic: %v", err)
			}
			if err := registrationConflict(js, protocol.Command{OperationID: "legacy-other", Kind: "storage-register", Storage: &legacyDifferentFS}); err != nil {
				t.Fatalf("unrelated legacy SMB source was globally blocked: %v", err)
			}
			if err := registrationConflict(js, protocol.Command{OperationID: "legacy-same", Kind: "storage-register", Storage: &legacySameFS}); err == nil {
				t.Fatal("mixed SMB profiles with matching legacy filesystem ID did not collide")
			}
		})
	}
}

// The package service uses UMask=0077. Use a subprocess so this process's other
// tests cannot observe the process-wide mask change.
func TestManagedDirectoryPreservesQEMUTraversalUnderPrivateUmask(t *testing.T) {
	const helper = "NEX_MANAGED_DIRECTORY_UMASK_HELPER"
	if os.Getenv(helper) == "1" {
		_ = syscall.Umask(0077) // dedicated helper process; no other tests observe this mask
		dir := filepath.Join(os.Getenv("NEX_MANAGED_DIRECTORY_ROOT"), "nex-vms")
		info, created, err := managedDirectory(dir)
		if err != nil || !created || !info.IsDir() || info.Mode().Perm() != 0711 {
			t.Fatalf("remote repository directory = %+v created=%t err=%v", info, created, err)
		}
		return
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestManagedDirectoryPreservesQEMUTraversalUnderPrivateUmask$")
	cmd.Env = append(os.Environ(), helper+"=1", "NEX_MANAGED_DIRECTORY_ROOT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("private-umask helper: %v\n%s", err, out)
	}
	info, err := os.Stat(filepath.Join(root, "nex-vms"))
	if err != nil || info.Mode().Perm() != 0711 {
		t.Fatalf("QEMU traversal directory = %v, %v", info, err)
	}
}

func TestManagedDirectoryPreservesExistingRemoteDirectoryAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "server-owned")
	if err := os.Mkdir(existing, 0750); err != nil {
		t.Fatal(err)
	}
	if _, created, err := managedDirectory(existing); err != nil || created {
		t.Fatalf("existing remote directory = created=%t err=%v", created, err)
	}
	info, err := os.Stat(existing)
	if err != nil || info.Mode().Perm() != 0750 {
		t.Fatalf("existing remote directory mode changed: %v, %v", info, err)
	}
	link := filepath.Join(root, "repository-link")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := managedDirectory(link); err == nil {
		t.Fatal("symlinked repository directory accepted")
	}
}

func TestCredentialRotationSafe(t *testing.T) {
	v := managedTestVM()
	js := []managedJournal{{Command: protocol.Command{Kind: "vm-create", VM: &v}, Result: protocol.Result{Status: "succeeded"}}}
	for _, tc := range []struct {
		name, state string
		pending     *managedJournal
		wantErr     bool
	}{
		{"stopped", "shut off", nil, false},
		{"running", "running", nil, true},
		{"paused", "paused", nil, true},
		{"unknown", "", nil, true},
		{"pending disk", "shut off", &managedJournal{Command: protocol.Command{Kind: "vm-update", VM: &v}, Result: protocol.Result{Status: "pending"}}, true},
		{"pending media", "shut off", &managedJournal{Command: protocol.Command{Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: v.ID}}, Result: protocol.Result{Status: "pending"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeCommand(t, "virsh", `if [ "$3" = domstate ]; then [ -n "$VMHOST_STATE" ] || exit 1; printf '%s\n' "$VMHOST_STATE"; fi`)
			t.Setenv("VMHOST_STATE", tc.state)
			cases := append([]managedJournal{}, js...)
			if tc.pending != nil {
				cases = append(cases, *tc.pending)
			}
			err := credentialRotationSafe(context.Background(), cases, v.Disks[0].StorageID)
			if (err != nil) != tc.wantErr {
				t.Fatalf("credentialRotationSafe: %v", err)
			}
		})
	}
}

func TestNextStorageReconcileOldestDue(t *testing.T) {
	now := time.Now().UTC()
	entry := func(id, status string, age time.Duration) managedJournal {
		return managedJournal{Command: protocol.Command{OperationID: id, Kind: "storage-recheck", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: "repo", PoolID: "pool", HostID: "host"}}, Result: protocol.Result{Status: status, ObservedAt: now.Add(-age)}}
	}
	js := []managedJournal{entry("later", "needs_attention", 2*time.Minute), entry("failed", "failed", 10*time.Minute), entry("first", "needs_attention", 3*time.Minute)}
	if got := nextStorageReconcile(js, now); got == nil || got.Command.OperationID != "first" {
		t.Fatalf("oldest due storage reconciliation = %+v", got)
	}
	js[0].Result.ObservedAt = js[2].Result.ObservedAt
	if got := nextStorageReconcile(js, now); got == nil || got.Command.OperationID != "first" {
		t.Fatalf("operation ID tie-break = %+v", got)
	}
}

func TestReconcileRemoteVMCreateStoppedObservesOnly(t *testing.T) {
	e := startEngine(t)
	v := managedTestVM()
	v.StartAfterCreate = true
	request := protocol.StorageRegistration{ID: v.Disks[0].StorageID, PoolID: v.PoolID, HostID: v.HostID, Name: "shared", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}}}
	remote := request
	remote.Path, remote.Generation = "/remote", 1
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage", Kind: "storage-register", Storage: &request}, AppliedStorage: &remote, Result: protocol.Result{OperationID: "storage", Status: "succeeded", ObservedAt: time.Now().UTC()}, Stage: "registered"}); err != nil {
		t.Fatal(err)
	}
	j := managedJournal{Command: protocol.Command{OperationID: "remote-create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "remote-create", Status: "needs_attention", Error: "interrupted", ObservedAt: time.Now().Add(-2 * time.Minute)}, Stage: "defining", Domain: []byte("<domain/>")}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	observed, claimed := 0, 0
	e.observe = func(_ context.Context, got managedJournal, state string) error {
		observed++
		if got.Command.OperationID != j.Command.OperationID || state != "shut off" {
			t.Fatalf("unexpected observation %+v state=%q", got, state)
		}
		return nil
	}
	if err := e.ReconcileRemoteVMCreateStopped(context.Background(), func(_ context.Context, got protocol.VMCreateSpec) error {
		if got.ID != v.ID || got.PoolID != v.PoolID || got.HostID != v.HostID || len(got.Disks) != 1 {
			t.Fatalf("unexpected validation input %+v", got)
		}
		return nil
	}, func(got protocol.VMCreateSpec) error {
		claimed++
		if got.ID != v.ID {
			t.Fatalf("claim VM %q", got.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := e.readJournal("remote-create.json")
	if err != nil {
		t.Fatal(err)
	}
	if observed != 1 || claimed != 1 || got.Result.Status != "succeeded" || got.Stage != "defined" || got.Start != startInterrupted {
		t.Fatalf("remote stopped recovery = observed %d claimed %d journal %+v", observed, claimed, got)
	}
	// The only native seam was the observer. In particular this path never calls
	// the vm-create replay path that can allocate disks or start the guest.
	if err = e.ReconcileRemoteVMCreateStopped(context.Background(), func(context.Context, protocol.VMCreateSpec) error {
		t.Fatal("settled operation revalidated")
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
}

// remoteUpdateEngine journals a succeeded NFS-backed revision 0 and an unresolved revision 1
// update (the ISO ejected) in stage, age old. It returns both definitions and domains.
func remoteUpdateEngine(t *testing.T, stage string, age time.Duration) (*ManagedEngine, protocol.VMCreateSpec, protocol.VMCreateSpec, []byte, []byte) {
	t.Helper()
	e := startEngine(t)
	previous := managedTestVM()
	next := previous
	next.Revision, next.MediaID = 1, ""
	nets := []protocol.NetworkRegistration{{Kind: "network", Source: "default"}}
	previousDomain, err := testDomainXML(previous, nets, []string{"/remote/nex-vms/disk"}, "/remote/nex-media/m.iso")
	if err != nil {
		t.Fatal(err)
	}
	nextDomain, err := testDomainXML(next, nets, []string{"/remote/nex-vms/disk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.StorageRegistration{ID: previous.Disks[0].StorageID, PoolID: previous.PoolID, HostID: previous.HostID, Name: "shared", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}}}
	remote := request
	remote.Path, remote.Generation = "/remote", 1
	for _, entry := range []managedJournal{
		{Command: protocol.Command{OperationID: "storage", Kind: "storage-register", Storage: &request}, AppliedStorage: &remote, Result: protocol.Result{OperationID: "storage", Status: "succeeded", ObservedAt: time.Now().UTC()}, Stage: "registered"},
		{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &previous}, Result: protocol.Result{OperationID: "create", Status: "succeeded", ObservedAt: time.Now().UTC()}, Stage: "defined", Domain: previousDomain},
		{Command: protocol.Command{OperationID: "remote-update", Kind: "vm-update", VM: &next}, Result: protocol.Result{OperationID: "remote-update", Status: "needs_attention", Error: "other domain disk cannot be inspected for exclusive ownership", ObservedAt: time.Now().Add(-age)}, Stage: stage, Domain: nextDomain},
	} {
		if err := e.save(entry); err != nil {
			t.Fatal(err)
		}
	}
	return e, previous, next, previousDomain, nextDomain
}

// BUG-0192: an update whose define landed but whose observation failed is settled from a later
// native observation only. Nothing is defined, attached, allocated or started again.
func TestReconcileRemoteVMUpdateSettlesAppliedDefinition(t *testing.T) {
	ctx := context.Background()
	allow := func(context.Context, protocol.VMCreateSpec) error { return nil }
	read := func(t *testing.T, e *ManagedEngine) managedJournal {
		t.Helper()
		j, err := e.readJournal("remote-update.json")
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	t.Run("applied definition succeeds", func(t *testing.T) {
		e, _, next, _, _ := remoteUpdateEngine(t, "defining", 2*time.Minute)
		fakeCommand(t, "virsh", "exit 99")
		observed := 0
		e.observe = func(_ context.Context, got managedJournal, state string) error {
			observed++
			if got.Command.OperationID != "remote-update" || state != "shut off" {
				t.Fatalf("unexpected observation %+v state=%q", got, state)
			}
			return nil
		}
		if err := e.ReconcileRemoteVMUpdate(ctx, func(_ context.Context, got protocol.VMCreateSpec) error {
			if !reflect.DeepEqual(got, next) {
				t.Fatalf("validated %+v", got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if got := read(t, e); observed != 1 || got.Result.Status != "succeeded" || got.Result.Error != "" || got.Stage != "defined" || got.UnappliedObserved {
			t.Fatalf("applied update = observed %d journal %+v", observed, got)
		}
		evidence, err := e.SuccessfulVMUpdateEvidence(ctx, next.ID)
		if err != nil || evidence.OperationID != "remote-update" || !reflect.DeepEqual(evidence.Current, next) {
			t.Fatalf("settled update evidence %+v err=%v", evidence, err)
		}
		if err = e.ReconcileRemoteVMUpdate(ctx, func(context.Context, protocol.VMCreateSpec) error {
			t.Fatal("settled update revalidated")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("entry superseded by a succeeded retry is left alone", func(t *testing.T) {
		e, _, next, _, nextDomain := remoteUpdateEngine(t, "defining", 2*time.Minute)
		retry := next
		if err := e.save(managedJournal{Command: protocol.Command{OperationID: "retry-update", Kind: "vm-update", VM: &retry}, Result: protocol.Result{OperationID: "retry-update", Status: "succeeded", ObservedAt: time.Now().UTC()}, Stage: "defined", Domain: nextDomain}); err != nil {
			t.Fatal(err)
		}
		e.observe = func(context.Context, managedJournal, string) error {
			t.Fatal("superseded update observed")
			return nil
		}
		if err := e.ReconcileRemoteVMUpdate(ctx, allow); err != nil {
			t.Fatal(err)
		}
		if got := read(t, e); got.Result.Status != "needs_attention" || got.Stage != "defining" {
			t.Fatalf("superseded update journal %+v", got)
		}
	})
	t.Run("observation failure keeps needs_attention", func(t *testing.T) {
		e, _, next, _, _ := remoteUpdateEngine(t, "defining", 2*time.Minute)
		fakeCommand(t, "virsh", "exit 99")
		e.observe = func(context.Context, managedJournal, string) error {
			return errors.New("cannot observe persistent VM definition")
		}
		if err := e.ReconcileRemoteVMUpdate(ctx, allow); err != nil {
			t.Fatal(err)
		}
		if got := read(t, e); got.Result.Status != "needs_attention" || got.Stage != "defining" || got.Result.Error != "cannot observe persistent VM definition" {
			t.Fatalf("unobserved update journal %+v", got)
		}
		if _, err := e.FailedVMUpdateEvidence(ctx, next.ID, "remote-update", next); err == nil {
			t.Fatal("unobserved update became failure evidence")
		}
	})
	t.Run("recent entry is not observed", func(t *testing.T) {
		e, _, _, _, _ := remoteUpdateEngine(t, "defining", 10*time.Second)
		e.observe = func(context.Context, managedJournal, string) error { t.Fatal("recent update observed"); return nil }
		if err := e.ReconcileRemoteVMUpdate(ctx, allow); err != nil {
			t.Fatal(err)
		}
		if got := read(t, e); got.Result.Status != "needs_attention" {
			t.Fatalf("recent update journal %+v", got)
		}
	})
	t.Run("invalid admission is not observed", func(t *testing.T) {
		e, _, _, _, _ := remoteUpdateEngine(t, "defining", 2*time.Minute)
		e.observe = func(context.Context, managedJournal, string) error { t.Fatal("invalid admission observed"); return nil }
		if err := e.ReconcileRemoteVMUpdate(ctx, func(context.Context, protocol.VMCreateSpec) error {
			return errors.New("remote VM storage is not currently eligible")
		}); err != nil {
			t.Fatal(err)
		}
		if got := read(t, e); got.Result.Status != "needs_attention" || !strings.Contains(got.Result.Error, "not currently eligible") {
			t.Fatalf("invalid admission journal %+v", got)
		}
	})
	t.Run("attaching entry observes a running guest", func(t *testing.T) {
		e, _, _, _, _ := remoteUpdateEngine(t, "attaching", 2*time.Minute)
		e.observe = func(_ context.Context, _ managedJournal, state string) error {
			if state != "running" {
				t.Fatalf("attaching update observed as %q", state)
			}
			return nil
		}
		if err := e.ReconcileRemoteVMUpdate(ctx, allow); err != nil {
			t.Fatal(err)
		}
		if got := read(t, e); got.Result.Status != "succeeded" || got.Stage != "defined" {
			t.Fatalf("attaching update journal %+v", got)
		}
	})
}

// BUG-0192: an update is failure evidence after a native boundary only when the stopped VM's
// persistent definition is still exactly its predecessor and not the attempted one.
func TestReconcileRemoteVMUpdateAbortsProvenUnappliedDefinition(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, stage, state string
		predecessor        bool
		want               string
	}{
		{"stopped predecessor definition proves unapplied", "defining", "shut off", true, "failed"},
		{"allocation left the predecessor definition", "allocating", "shut off", true, "failed"},
		{"attempted definition stays ambiguous", "defining", "shut off", false, "needs_attention"},
		{"running guest stays ambiguous", "defining", "running", true, "needs_attention"},
		{"live attachment stays ambiguous", "attaching", "shut off", true, "needs_attention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, previous, next, previousDomain, nextDomain := remoteUpdateEngine(t, tc.stage, 2*time.Minute)
			domain := nextDomain
			if tc.predecessor {
				domain = previousDomain
			}
			path := filepath.Join(t.TempDir(), "domain.xml")
			if err := os.WriteFile(path, domain, 0600); err != nil {
				t.Fatal(err)
			}
			fakeCommand(t, "virsh", `case "$3" in domstate) printf '%s\n' '`+tc.state+`';; dumpxml) cat '`+path+`';; *) exit 2;; esac`)
			e.observe = func(context.Context, managedJournal, string) error {
				return errors.New("VM definition differs from requested ownership or resources")
			}
			if err := e.ReconcileRemoteVMUpdate(ctx, func(context.Context, protocol.VMCreateSpec) error { return nil }); err != nil {
				t.Fatal(err)
			}
			got, err := e.readJournal("remote-update.json")
			if err != nil || got.Result.Status != tc.want || got.UnappliedObserved != (tc.want == "failed") || got.Stage != tc.stage {
				t.Fatalf("journal %+v err=%v", got, err)
			}
			evidence, err := e.FailedVMUpdateEvidence(ctx, next.ID, "remote-update", next)
			if tc.want == "failed" && (err != nil || !reflect.DeepEqual(evidence.Previous, previous) || !reflect.DeepEqual(evidence.Current, next)) {
				t.Fatalf("proven unapplied update evidence %+v err=%v", evidence, err)
			}
			if tc.want != "failed" && err == nil {
				t.Fatal("ambiguous update became failure evidence")
			}
		})
	}
}

func TestReconcileRemoteVMCreateStoppedRefusesInvalidAdmission(t *testing.T) {
	e := startEngine(t)
	v := managedTestVM()
	request := protocol.StorageRegistration{ID: v.Disks[0].StorageID, PoolID: v.PoolID, HostID: v.HostID, Name: "shared", Kind: protocol.StorageKindSMB, Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "vms"}}}
	remote := request
	remote.Path, remote.Generation = "/remote", 1
	for _, entry := range []managedJournal{
		{Command: protocol.Command{OperationID: "storage", Kind: "storage-register", Storage: &request}, AppliedStorage: &remote, Result: protocol.Result{OperationID: "storage", Status: "succeeded", ObservedAt: time.Now().UTC()}, Stage: "registered"},
		{Command: protocol.Command{OperationID: "remote-create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "remote-create", Status: "needs_attention", Error: "interrupted", ObservedAt: time.Now().Add(-2 * time.Minute)}, Stage: "defining", Domain: []byte("<domain/>")},
	} {
		if err := e.save(entry); err != nil {
			t.Fatal(err)
		}
	}
	e.observe = func(context.Context, managedJournal, string) error {
		t.Fatal("invalid admission observed native VM")
		return nil
	}
	if err := e.ReconcileRemoteVMCreateStopped(context.Background(), func(context.Context, protocol.VMCreateSpec) error {
		return errors.New("pool owner or storage is not currently eligible")
	}, func(protocol.VMCreateSpec) error { t.Fatal("invalid admission claimed autostart"); return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := e.readJournal("remote-create.json")
	if err != nil || got.Result.Status != "needs_attention" || got.Stage != "defining" || !strings.Contains(got.Result.Error, "not currently eligible") {
		t.Fatalf("invalid admission journal %+v err=%v", got, err)
	}
}

func TestReconcileRemoteVMCreateStoppedDeadlineCannotSettle(t *testing.T) {
	e := startEngine(t)
	v := managedTestVM()
	request := protocol.StorageRegistration{ID: v.Disks[0].StorageID, PoolID: v.PoolID, HostID: v.HostID, Name: "shared", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}}}
	remote := request
	remote.Path, remote.Generation = "/remote", 1
	for _, entry := range []managedJournal{
		{Command: protocol.Command{OperationID: "storage", Kind: "storage-register", Storage: &request}, AppliedStorage: &remote, Result: protocol.Result{OperationID: "storage", Status: "succeeded", ObservedAt: time.Now().UTC()}, Stage: "registered"},
		{Command: protocol.Command{OperationID: "remote-create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "remote-create", Status: "needs_attention", Error: "interrupted", ObservedAt: time.Now().Add(-2 * time.Minute)}, Stage: "defined", Domain: []byte("<domain/>")},
	} {
		if err := e.save(entry); err != nil {
			t.Fatal(err)
		}
	}
	observed := 0
	e.observe = func(context.Context, managedJournal, string) error { observed++; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.ReconcileRemoteVMCreateStopped(ctx, func(context.Context, protocol.VMCreateSpec) error { return nil }, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("deadline reconciliation error = %v", err)
	}
	if observed != 0 {
		t.Fatal("expired context observed native VM")
	}
	got, err := e.readJournal("remote-create.json")
	if err != nil || got.Result.Status != "needs_attention" || got.Stage != "defined" {
		t.Fatalf("expired context settled journal %+v err=%v", got, err)
	}
}

func TestReconcileRemoteVMCreateStoppedCanceledClaimCannotSettle(t *testing.T) {
	e := startEngine(t)
	v := managedTestVM()
	request := protocol.StorageRegistration{ID: v.Disks[0].StorageID, PoolID: v.PoolID, HostID: v.HostID, Name: "shared", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}}}
	remote := request
	remote.Path, remote.Generation = "/remote", 1
	for _, entry := range []managedJournal{
		{Command: protocol.Command{OperationID: "storage", Kind: "storage-register", Storage: &request}, AppliedStorage: &remote, Result: protocol.Result{OperationID: "storage", Status: "succeeded", ObservedAt: time.Now().UTC()}, Stage: "registered"},
		{Command: protocol.Command{OperationID: "remote-create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "remote-create", Status: "needs_attention", Error: "interrupted", ObservedAt: time.Now().Add(-2 * time.Minute)}, Stage: "defined", Domain: []byte("<domain/>")},
	} {
		if err := e.save(entry); err != nil {
			t.Fatal(err)
		}
	}
	e.observe = func(context.Context, managedJournal, string) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.ReconcileRemoteVMCreateStopped(ctx, func(context.Context, protocol.VMCreateSpec) error { return nil }, func(protocol.VMCreateSpec) error { cancel(); return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := e.readJournal("remote-create.json")
	if err != nil || got.Result.Status != "needs_attention" || got.Stage != "defined" || !strings.Contains(got.Result.Error, context.Canceled.Error()) {
		t.Fatalf("canceled claim settled journal %+v err=%v", got, err)
	}
}

func TestReconcileStoragePersistsOldestOriginalCommand(t *testing.T) {
	e := NewManaged(t.TempDir())
	now := time.Now().UTC()
	registration := func(id string) protocol.StorageRegistration {
		return protocol.StorageRegistration{ID: id, PoolID: "pool", HostID: "host", Name: id, Path: "/srv/" + id, Generation: 1}
	}
	for _, id := range []string{"repo-a", "repo-b"} {
		s := registration(id)
		if err := e.save(managedJournal{Command: protocol.Command{OperationID: "register-" + id, Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "register-" + id, Status: "succeeded"}, AppliedStorage: &s}); err != nil {
			t.Fatal(err)
		}
	}
	failed := managedJournal{Command: protocol.Command{OperationID: "failed", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "repo-a", PoolID: "pool", HostID: "host", ExpectedGeneration: 1}}, Result: protocol.Result{OperationID: "failed", Status: "failed", ObservedAt: now.Add(-3 * time.Minute)}}
	due := managedJournal{Command: protocol.Command{OperationID: "oldest", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "repo-b", PoolID: "pool", HostID: "host", ExpectedGeneration: 1}}, Result: protocol.Result{OperationID: "oldest", Status: "needs_attention", ObservedAt: now.Add(-2 * time.Minute)}}
	if err := e.save(failed); err != nil {
		t.Fatal(err)
	}
	if err := e.save(due); err != nil {
		t.Fatal(err)
	}
	if err := e.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := e.readJournal("oldest.json")
	if err != nil || got.Result.Status != "succeeded" || got.Command.OperationID != "oldest" {
		t.Fatalf("reconciled journal = %+v, %v", got, err)
	}
	kept, err := e.readJournal("failed.json")
	if err != nil || kept.Result.Status != "failed" {
		t.Fatalf("terminal failure replayed: %+v, %v", kept, err)
	}
}

func TestManagedDomainXMLUsesDefaultEmulator(t *testing.T) {
	v := managedTestVM()
	b, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "network", Source: "default"}}, []string{"/disk.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "<emulator>") {
		t.Fatalf("domain overrides libvirt's standard emulator: %s", b)
	}
	if !strings.Contains(string(b), `mode="host-passthrough" check="none"`) {
		t.Fatalf("host-passthrough must request check=none: %s", b)
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
// managedTestVM returns) keeps every boot order after an eject, the emptied CD drive's
// included: virsh change-media leaves every bootindex in the domain alone, so renumbering the
// record would make it describe a domain that does not exist — and would make the eject of a
// running pool-shared VM a stop-first edit (BUG-0188).
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
	if got.MediaID != "" || got.Boot.MediaBootOrder != 1 || got.Disks[0].BootOrder != 2 {
		t.Fatalf("eject must not renumber the boot order libvirt still holds: %+v", got)
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
	// own controllers; none of that is tampering. It also injects check="none" migratable="on"
	// on host-passthrough when the domain omitted check.
	normalized := strings.Replace(string(wanted), `unit="MiB">2048`, `unit="KiB">2097152`, 1)
	normalized = strings.Replace(normalized, `machine="q35"`, `machine="pc-q35-10.0"`, 1)
	normalized = strings.Replace(normalized, `</memory>`, `</memory><currentMemory unit="KiB">2097152</currentMemory>`, 1)
	normalized = strings.Replace(normalized, `mode="host-passthrough" check="none"`, `mode="host-passthrough" check="none" migratable="on"`, 1)
	normalized = strings.Replace(normalized, `</devices>`, `<controller type="usb" index="0" model="qemu-xhci"></controller></devices>`, 1)
	if err = validateManagedDefinition([]byte(normalized), wanted); err != nil {
		t.Fatalf("libvirt normalized definition rejected: %v", err)
	}
	// A domain that omitted check must still match libvirt's injected check="none".
	omitCheck := strings.Replace(string(wanted), ` check="none"`, "", 1)
	if err = validateManagedDefinition([]byte(wanted), []byte(omitCheck)); err != nil {
		t.Fatalf("libvirt check=none against omitted check rejected: %v", err)
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

func TestSuccessfulVMUpdateEvidenceRequiresExactSucceededHistory(t *testing.T) {
	write := func(t *testing.T, e *ManagedEngine, j managedJournal) {
		t.Helper()
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	newEngine := func(t *testing.T) (*ManagedEngine, protocol.VMCreateSpec, protocol.VMCreateSpec) {
		t.Helper()
		e := NewManaged(t.TempDir())
		if err := os.MkdirAll(e.stateDir, 0700); err != nil {
			t.Fatal(err)
		}
		previous := managedTestVM()
		current := previous
		current.Revision, current.Autostart = 1, true
		return e, previous, current
	}
	t.Run("exact predecessor and successor", func(t *testing.T) {
		e, previous, current := newEngine(t)
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &previous}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}})
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "update", Kind: "vm-update", VM: &current}, Result: protocol.Result{OperationID: "update", Status: "succeeded"}})
		got, err := e.SuccessfulVMUpdateEvidence(context.Background(), current.ID)
		if err != nil || got.OperationID != "update" || got.Status != "succeeded" || !reflect.DeepEqual(got.Previous, previous) || !reflect.DeepEqual(got.Current, current) {
			t.Fatalf("evidence %#v, %v", got, err)
		}
	})
	t.Run("duplicate current revision", func(t *testing.T) {
		e, previous, current := newEngine(t)
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &previous}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}})
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "update-one", Kind: "vm-update", VM: &current}, Result: protocol.Result{OperationID: "update-one", Status: "succeeded"}})
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "update-two", Kind: "vm-update", VM: &current}, Result: protocol.Result{OperationID: "update-two", Status: "succeeded"}})
		if _, err := e.SuccessfulVMUpdateEvidence(context.Background(), current.ID); err == nil {
			t.Fatal("duplicate revision accepted")
		}
	})
	t.Run("missing predecessor", func(t *testing.T) {
		e, _, current := newEngine(t)
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "update", Kind: "vm-update", VM: &current}, Result: protocol.Result{OperationID: "update", Status: "succeeded"}})
		if _, err := e.SuccessfulVMUpdateEvidence(context.Background(), current.ID); err == nil {
			t.Fatal("missing predecessor accepted")
		}
	})
	for _, tc := range []struct {
		status   string
		revision int
	}{
		{status: "pending", revision: 1},
		{status: "needs_attention", revision: 2},
	} {
		t.Run("unresolved "+tc.status+" update", func(t *testing.T) {
			e, previous, current := newEngine(t)
			newer := current
			newer.Revision = tc.revision
			write(t, e, managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &previous}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}})
			write(t, e, managedJournal{Command: protocol.Command{OperationID: "update", Kind: "vm-update", VM: &current}, Result: protocol.Result{OperationID: "update", Status: "succeeded"}})
			write(t, e, managedJournal{Command: protocol.Command{OperationID: "unresolved-" + tc.status, Kind: "vm-update", VM: &newer}, Result: protocol.Result{OperationID: "unresolved-" + tc.status, Status: tc.status}})
			if _, err := e.SuccessfulVMUpdateEvidence(context.Background(), current.ID); err == nil {
				t.Fatalf("unresolved %s update accepted", tc.status)
			}
		})
	}
	t.Run("failed later update does not supersede", func(t *testing.T) {
		e, previous, current := newEngine(t)
		failed := current
		failed.Revision = 2
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &previous}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}})
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "update", Kind: "vm-update", VM: &current}, Result: protocol.Result{OperationID: "update", Status: "succeeded"}})
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "failed", Kind: "vm-update", VM: &failed}, Result: protocol.Result{OperationID: "failed", Status: "failed"}})
		if got, err := e.SuccessfulVMUpdateEvidence(context.Background(), current.ID); err != nil || !reflect.DeepEqual(got.Current, current) {
			t.Fatalf("failed update changed evidence %#v, %v", got, err)
		}
	})
	t.Run("unreadable journal", func(t *testing.T) {
		e, _, current := newEngine(t)
		if err := os.WriteFile(filepath.Join(e.stateDir, "bad.json"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := e.SuccessfulVMUpdateEvidence(context.Background(), current.ID); err == nil {
			t.Fatal("unreadable journal accepted")
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		e, _, current := newEngine(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := e.SuccessfulVMUpdateEvidence(ctx, current.ID); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled evidence = %v", err)
		}
	})
}

func TestFailedVMUpdateEvidenceRequiresExactFailedCurrentHistory(t *testing.T) {
	write := func(t *testing.T, e *ManagedEngine, j managedJournal) {
		t.Helper()
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	newEngine := func(t *testing.T) (*ManagedEngine, protocol.VMCreateSpec, protocol.VMCreateSpec) {
		t.Helper()
		e := NewManaged(t.TempDir())
		if err := os.MkdirAll(e.stateDir, 0700); err != nil {
			t.Fatal(err)
		}
		previous := managedTestVM()
		attempted := previous
		attempted.Revision, attempted.Autostart = 1, true
		return e, previous, attempted
	}
	valid := func(t *testing.T, e *ManagedEngine, previous, attempted protocol.VMCreateSpec) {
		t.Helper()
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &previous}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}})
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "failed", Kind: "vm-update", VM: &attempted}, Result: protocol.Result{OperationID: "failed", Status: "failed"}, Stage: "validating"})
	}
	t.Run("exact failed update retains predecessor", func(t *testing.T) {
		e, previous, attempted := newEngine(t)
		valid(t, e, previous, attempted)
		got, err := e.FailedVMUpdateEvidence(context.Background(), attempted.ID, "failed", attempted)
		if err != nil || got.OperationID != "failed" || got.Status != "failed" || !reflect.DeepEqual(got.Previous, previous) || !reflect.DeepEqual(got.Current, attempted) {
			t.Fatalf("evidence %#v, %v", got, err)
		}
	})
	for _, status := range []string{"pending", "needs_attention"} {
		t.Run("target "+status+" is not failed evidence", func(t *testing.T) {
			e, previous, attempted := newEngine(t)
			write(t, e, managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &previous}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}})
			write(t, e, managedJournal{Command: protocol.Command{OperationID: "failed", Kind: "vm-update", VM: &attempted}, Result: protocol.Result{OperationID: "failed", Status: status}, Stage: "validating"})
			if _, err := e.FailedVMUpdateEvidence(context.Background(), attempted.ID, "failed", attempted); err == nil {
				t.Fatalf("%s target accepted as failed evidence", status)
			}
		})
	}
	t.Run("post-native stage is not failed evidence", func(t *testing.T) {
		e, previous, attempted := newEngine(t)
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &previous}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}})
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "failed", Kind: "vm-update", VM: &attempted}, Result: protocol.Result{OperationID: "failed", Status: "failed"}, Stage: "defining"})
		if _, err := e.FailedVMUpdateEvidence(context.Background(), attempted.ID, "failed", attempted); err == nil {
			t.Fatal("post-native failure accepted")
		}
	})
	t.Run("observed unapplied post-native stage is failed evidence", func(t *testing.T) {
		e, previous, attempted := newEngine(t)
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &previous}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}})
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "failed", Kind: "vm-update", VM: &attempted}, Result: protocol.Result{OperationID: "failed", Status: "failed"}, Stage: "defining", UnappliedObserved: true})
		if got, err := e.FailedVMUpdateEvidence(context.Background(), attempted.ID, "failed", attempted); err != nil || !reflect.DeepEqual(got.Previous, previous) {
			t.Fatalf("observed unapplied failure evidence %#v, %v", got, err)
		}
	})
	t.Run("operation and attempted definition must match", func(t *testing.T) {
		e, previous, attempted := newEngine(t)
		valid(t, e, previous, attempted)
		if _, err := e.FailedVMUpdateEvidence(context.Background(), attempted.ID, "other", attempted); err == nil {
			t.Fatal("wrong operation accepted")
		}
		mismatch := attempted
		mismatch.Autostart = false
		if _, err := e.FailedVMUpdateEvidence(context.Background(), attempted.ID, "failed", mismatch); err == nil {
			t.Fatal("wrong attempted definition accepted")
		}
	})
	t.Run("missing predecessor is rejected", func(t *testing.T) {
		e, _, attempted := newEngine(t)
		write(t, e, managedJournal{Command: protocol.Command{OperationID: "failed", Kind: "vm-update", VM: &attempted}, Result: protocol.Result{OperationID: "failed", Status: "failed"}, Stage: "validating"})
		if _, err := e.FailedVMUpdateEvidence(context.Background(), attempted.ID, "failed", attempted); err == nil {
			t.Fatal("missing predecessor accepted")
		}
	})
	t.Run("newer successful or unresolved mutation is rejected", func(t *testing.T) {
		for _, status := range []string{"succeeded", "pending", "needs_attention"} {
			t.Run(status, func(t *testing.T) {
				e, previous, attempted := newEngine(t)
				valid(t, e, previous, attempted)
				newer := attempted
				newer.Revision = 2
				write(t, e, managedJournal{Command: protocol.Command{OperationID: "newer-" + status, Kind: "vm-update", VM: &newer}, Result: protocol.Result{OperationID: "newer-" + status, Status: status}})
				if _, err := e.FailedVMUpdateEvidence(context.Background(), attempted.ID, "failed", attempted); err == nil {
					t.Fatalf("newer %s update accepted", status)
				}
			})
		}
	})
}

func TestVMHasPoolSharedStorageHistorySurvivesLaterLocalUpdate(t *testing.T) {
	e := NewManaged(t.TempDir())
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	v := managedTestVM()
	storage := protocol.StorageRegistration{ID: v.Disks[0].StorageID, PoolID: v.PoolID, HostID: v.HostID, Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/guests", Directory: "vm"}}}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage", Kind: "storage-register", Storage: &storage}, Result: protocol.Result{OperationID: "storage", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	local := v
	local.Revision = 1
	local.Disks = append([]protocol.VMDisk(nil), v.Disks...)
	local.Disks[0].StorageID = "local-repo"
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "local", Kind: "vm-update", VM: &local}, Result: protocol.Result{OperationID: "local", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := e.VMHasPoolSharedStorageHistory(context.Background(), v.ID); err != nil || !got {
		t.Fatalf("shared history after local successor = %v, %v", got, err)
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

func TestPublishedMediaPathIndependentOfDiskStorage(t *testing.T) {
	ok := protocol.Result{Status: "succeeded"}
	isoStore := managedJournal{Command: protocol.Command{OperationID: "iso-st", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "iso-lib", Path: "/var/lib/libvirt/images"}}, Result: ok}
	diskStore := managedJournal{Command: protocol.Command{OperationID: "disk-st", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "disks", Path: "/srv/vms"}}, Result: ok}
	iso := managedJournal{Command: protocol.Command{OperationID: "m", Kind: "iso-import", Media: &protocol.Media{ID: "m", StorageID: "iso-lib"}}, Result: ok, MediaPath: "/var/lib/libvirt/images/nex-media/m.iso"}
	m, path, err := publishedMediaPath([]managedJournal{isoStore, diskStore, iso}, "m")
	if err != nil || m.StorageID != "iso-lib" || path != "/var/lib/libvirt/images/nex-media/m.iso" {
		t.Fatalf("ISO on a separate library: %+v %q %v", m, path, err)
	}
	wrong := iso
	wrong.MediaPath = "/srv/vms/nex-media/m.iso"
	if _, _, err := publishedMediaPath([]managedJournal{isoStore, diskStore, wrong}, "m"); err == nil {
		t.Fatal("ISO path on disk storage accepted as the published library file")
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
	if len(d.Devices.Disks) != 5 {
		t.Fatalf("expected three disks, the install ISO and the driver disc drive: %s", b)
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
	// A VM without install media still has both CD drives, empty, so an ISO can be inserted
	// later without hot-plugging a SATA device.
	v.Boot.MediaBootOrder, v.MediaID = 0, ""
	if b, err = testDomainXML(v, nets, paths, ""); err != nil {
		t.Fatalf("media-free VM: %v", err)
	}
	var empty managedDomain
	if err = xml.Unmarshal(b, &empty); err != nil || len(empty.Devices.Disks) != 5 {
		t.Fatalf("media-free VM must carry both CD drives: %v\n%s", err, b)
	}
	for _, cd := range empty.Devices.Disks[3:] {
		if cd.Device != "cdrom" || cd.Source != nil || cd.Boot != nil {
			t.Fatalf("media-free VM must carry an empty CD drive: %+v\n%s", cd, b)
		}
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

// A start refuses a VM whose ceiling available memory cannot meet, in both memory modes.
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
		{"name", func(s *protocol.VMCreateSpec) { s.Name = "Guest/Machine" }},
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

func TestManagedVMRenameRejectsPaddedDuplicate(t *testing.T) {
	cur := managedTestVM()
	next := cur
	next.Name, next.Revision = "  Guest Machine  ", 1
	js := []managedJournal{{Command: protocol.Command{Kind: "vm-create", VM: &cur}, Result: protocol.Result{Status: "succeeded"}, Disks: []string{"/disk.qcow2"}}}
	j := managedJournal{Command: protocol.Command{OperationID: "rename", Kind: "vm-update", VM: &next}}
	fakeCommand(t, "virsh", `case "$3" in
domstate) printf 'shut off\n';;
list) printf '  Guest Machine  \n';;
*) exit 1;;
esac`)
	err := NewManaged(t.TempDir()).update(context.Background(), &j, js)
	if err == nil || err.Error() != "VM name already exists" {
		t.Fatalf("padded duplicate name was not rejected: %v", err)
	}
}

func TestManagedVMHumanNames(t *testing.T) {
	for _, name := range []string{"Guest Machine", "app.example.test", "数据库 & <Test>", "--help", "  Guest Machine  "} {
		t.Run(name, func(t *testing.T) {
			v := managedTestVM()
			v.Name = name
			for _, kind := range []string{"vm-create", "vm-update"} {
				if kind == "vm-update" {
					v.Revision = 1
				}
				c := protocol.Command{OperationID: "op-name", Kind: kind, VM: &v}
				if err := validateManaged(c); err != nil {
					t.Fatalf("%s rejected name: %v", kind, err)
				}
				b, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "network", Source: "default"}}, []string{"/disk.qcow2"}, "")
				if err != nil {
					t.Fatal(err)
				}
				var d managedDomain
				if err = xml.Unmarshal(b, &d); err != nil || d.Name != name || d.UUID != v.ID {
					t.Fatalf("XML changed VM identity: %+v %v", d, err)
				}
				e := NewManaged(t.TempDir())
				if err = e.save(managedJournal{Command: c, Domain: b, Result: protocol.Result{OperationID: c.OperationID, Status: "succeeded"}}); err != nil {
					t.Fatal(err)
				}
				fakeCommand(t, "virsh", "printf '%s\\n' '"+v.ID+"'")
				got, checks := NewManaged(e.stateDir).Applied(context.Background())
				if len(got.VMs) != 1 || got.VMs[0].Name != name || got.VMs[0].ID != v.ID || got.VMs[0].Revision != v.Revision {
					t.Fatalf("reopened journal lost VM identity: %+v %+v", got, checks)
				}
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

func TestAppliedStorageProjectsDurableStorageWithoutPendingEntries(t *testing.T) {
	e := NewManaged(t.TempDir())
	base := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Name: "before", Path: "/srv/before", Generation: 1}
	next := base
	next.Name, next.Path, next.Generation = "after", "/srv/after", 2
	tombstoned := protocol.StorageRegistration{ID: "gone", PoolID: "pool-1", HostID: "host-1", Name: "gone", Path: "/srv/gone", Generation: 1}
	pending := protocol.StorageRegistration{ID: "pending", PoolID: "pool-1", HostID: "host-1", Name: "pending", Path: "/srv/pending", Generation: 1}
	for _, j := range []managedJournal{
		{Command: protocol.Command{OperationID: "register", Kind: "storage-register", Storage: &base}, Result: protocol.Result{OperationID: "register", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "update", Kind: "storage-update", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "update", RepositoryID: base.ID, PoolID: base.PoolID, HostID: base.HostID, ExpectedGeneration: 1, Registration: &next}}, AppliedStorage: &next, Result: protocol.Result{OperationID: "update", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "old", Kind: "storage-register", Storage: &tombstoned}, Result: protocol.Result{OperationID: "old", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "detach", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: tombstoned.ID, PoolID: tombstoned.PoolID, HostID: tombstoned.HostID, ExpectedGeneration: 1}}, Result: protocol.Result{OperationID: "detach", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "pending", Kind: "storage-register", Storage: &pending}, Result: protocol.Result{OperationID: "pending", Status: "pending"}},
	} {
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.stateDir, "bad.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	storage, checks := e.AppliedStorage()
	byID := map[string]protocol.StorageRegistration{}
	for _, s := range storage {
		byID[s.ID] = s
	}
	if got := byID[base.ID]; got.Generation != 2 || got.Name != "after" || got.Path != "/srv/after" || got.OperationID != "" || got.Status != "" || got.Error != "" {
		t.Fatalf("updated storage projection = %#v", got)
	}
	if got, ok := byID[tombstoned.ID]; !ok || !got.Tombstone || got.Generation != 2 {
		t.Fatalf("detach tombstone = %#v", got)
	}
	if _, ok := byID[pending.ID]; ok {
		t.Fatalf("pending storage entered applied projection: %#v", storage)
	}
	if len(checks) != 1 || checks[0].Name != "managed-journal" || checks[0].Status != "failed" || !strings.Contains(checks[0].Error, "bad.json") {
		t.Fatalf("storage journal check = %#v", checks)
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
	if got, _ := e.Applied(context.Background()); len(got.Outcomes) != 1 || got.Outcomes[0].Status != "succeeded" {
		t.Fatalf("finished operation missing: %+v", got.Outcomes)
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
	// The trailing domstate is powerVM observing the domain after the failed start: a failed
	// call says nothing about what the guest did, so the outcome is settled by observation
	// (BUG-0213). Here the domain is still shut off, so the start stays a recorded failure.
	reason := "libvirt did not confirm the start request; inspect the VM on its host"
	if got := calls(); got != "domstate "+v.ID+"\nstart "+v.ID+"\ndomstate "+v.ID+"\n" {
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
	if len(got.Outcomes) != 2 || got.Outcomes[0].Status != "succeeded" || got.Outcomes[1] != want {
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
	// A start that succeeds reports its successful creation, without a failed start outcome.
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
	if got, _ = e.Applied(context.Background()); len(got.VMs) != 2 || len(got.Outcomes) != 3 || got.Outcomes[2].OperationID != "create:start" {
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
	if len(got.VMs) != 3 || len(got.Outcomes) != 5 {
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
	if got, _ = e.Applied(context.Background()); calls != 2 || len(got.VMs) != 1 || len(got.Outcomes) != 1 || got.Outcomes[0].Status != "succeeded" || len(got.MissingVMs) != 0 {
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

// testDomainXML builds a definition with creation-time target names and an empty driver disc.
func testDomainXML(v protocol.VMCreateSpec, nets []protocol.NetworkRegistration, disks []string, iso string) ([]byte, error) {
	return testDomainXMLWithDrivers(v, nets, disks, iso, "")
}

func testDomainXMLWithDrivers(v protocol.VMCreateSpec, nets []protocol.NetworkRegistration, disks []string, iso, drivers string) ([]byte, error) {
	targets, media, driversDev := managedTargets(v)
	return managedDomainXML(v, nets, disks, targets, media, iso, driversDev, drivers)
}

func TestLibvirtCPUCheckNoneAccepted(t *testing.T) {
	v := managedTestVM()
	wanted, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "network", Source: "default"}}, []string{"/disk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	live := strings.Replace(string(wanted), `mode="host-passthrough" check="none"`, `mode="host-passthrough" check="none" migratable="on"`, 1)
	if live == string(wanted) {
		t.Fatal("fixture did not mutate")
	}
	if err = validateManagedDefinition([]byte(live), wanted); err != nil {
		t.Fatalf("libvirt migratable injection rejected: %v", err)
	}
	omit := strings.Replace(string(wanted), ` check="none"`, "", 1)
	if err = validateManagedDefinition([]byte(wanted), []byte(omit)); err != nil {
		t.Fatalf("check=none against omitted check rejected: %v", err)
	}
}
