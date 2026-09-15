// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"nex.local/nex/internal/protocol"
)

// The Core service runs with UMask=0077. Exercise that process-wide setting in
// a child so parallel package tests cannot observe it.
func TestStorageMountRootRepairsPrivateUmask(t *testing.T) {
	const helper = "NEX_STORAGE_MOUNT_ROOT_UMASK_HELPER"
	if os.Getenv(helper) == "1" {
		_ = syscall.Umask(0077)
		storageMountRoot = filepath.Join(os.Getenv("NEX_STORAGE_MOUNT_ROOT_PARENT"), "mounts")
		if err := ensureStorageMountRoot(); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStorageMountRootRepairsPrivateUmask$")
	cmd.Env = append(os.Environ(), helper+"=1", "NEX_STORAGE_MOUNT_ROOT_PARENT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("private-umask helper: %v\n%s", err, out)
	}
	path := filepath.Join(root, "mounts")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0711 {
		t.Fatalf("mount root = %v, %v", info, err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	old := storageMountRoot
	storageMountRoot = path
	t.Cleanup(func() { storageMountRoot = old })
	if err := ensureStorageMountRoot(); err != nil {
		t.Fatalf("repair existing Core root: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0711 {
		t.Fatalf("repaired mount root = %v, %v", info, err)
	}
}

func TestNFSKernelFilesystemIdentity(t *testing.T) {
	const volumes = `NV SERVER PORT DEV FSID FSC
v2 7f000003 801 0:63 9:1 no
v4 7f000001 801 0:64 a88eaa57e8754855:a3cbc231758653f8 no
v4 7f000002 801 0:65 9:2 no
`
	got, err := nfsFilesystemIDForDevice(volumes, "0:64")
	if err != nil {
		t.Fatal(err)
	}
	const want = "nfs4:7f000001:801:a88eaa57e8754855:a3cbc231758653f8"
	if got != want {
		t.Fatalf("NFS filesystem identity = %q, want %q", got, want)
	}
	// statfs can report zero for both mounts. The kernel NFS FSIDs still keep
	// the two distinct remote filesystems distinct.
	other, err := nfsFilesystemIDForDevice(volumes, "0:65")
	if err != nil || other == got {
		t.Fatalf("second NFS backing identity = %q, %v", other, err)
	}
}

func TestNFSIdentityIncludesSelectedDirectory(t *testing.T) {
	v := &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports/vms", Directory: "team"}
	got, err := nfsIdentity(protocol.StorageRegistration{}, v, v.Server+":"+v.Export, storageAccess{}, "nfs4:7f000001:801:1:2")
	if err != nil || !strings.Contains(got.Identity, ":directory=team:") {
		t.Fatalf("NFS directory identity = %+v, %v", got, err)
	}
}

func TestRemoteSelectedDirectoryDoesNotRequireLocalRootOwnership(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0770); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Team VMs", "2026"), 0777); err != nil {
		t.Fatal(err)
	}
	base := storageAccess{Path: root, Identity: "nfs:root", FilesystemID: "fsid", Health: "healthy"}
	got, err := remoteSubpathAccess(root, "Team VMs/2026", base)
	if err != nil || got.Path != filepath.Join(root, "Team VMs", "2026") || got.Identity != base.Identity {
		t.Fatalf("selected remote directory = %+v, %v", got, err)
	}
	if err := os.Symlink("/", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := remoteSubpathAccess(root, "link", base); err == nil {
		t.Fatal("remote symlink accepted")
	}
}

func TestRemoteUnitPreservesOrdinarySMBShareName(t *testing.T) {
	unit := string(remoteUnit("//smb.example.test/VM Files$", "/var/lib/nex-storage/repo-1", "cifs", "defaults"))
	if !strings.Contains(unit, "What=//smb.example.test/VM Files$\n") {
		t.Fatalf("SMB source was not preserved in mount unit: %q", unit)
	}
}

func TestNFSKernelFilesystemIdentityBindsPathDevice(t *testing.T) {
	oldRead, oldStat := readNFSVolumes, statNFSPath
	t.Cleanup(func() { readNFSVolumes, statNFSPath = oldRead, oldStat })
	path := t.TempDir()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("temporary path did not expose Linux stat data")
	}
	device := linuxDevice(stat.Dev)
	readNFSVolumes = func(string) ([]byte, error) {
		return []byte("NV SERVER PORT DEV FSID FSC\nv4 7f000001 801 " + device + " 1:2 no\n"), nil
	}
	got, err := nfsFilesystemID(path)
	if err != nil || got != "nfs4:7f000001:801:1:2" {
		t.Fatalf("path-bound NFS identity = %q, %v", got, err)
	}
	readNFSVolumes = func(string) ([]byte, error) {
		return []byte("NV SERVER PORT DEV FSID FSC\nv4 7f000001 801 0:0 1:2 no\n"), nil
	}
	if _, err := nfsFilesystemID(path); err == nil {
		t.Fatal("NFS volume row for a different device was accepted")
	}
}

func TestNFSKernelFilesystemIdentityIPv6AndZeroProtocolFSID(t *testing.T) {
	got, err := nfsFilesystemIDForDevice("NV SERVER PORT DEV FSID FSC\nv4 20010db8000000000000000000000001 801 0:64 0:0 yes\n", "0:64")
	if err != nil {
		t.Fatal(err)
	}
	if want := "nfs4:20010db8000000000000000000000001:801:0:0"; got != want {
		t.Fatalf("NFS IPv6/zero protocol FSID = %q, want %q", got, want)
	}
	if !validNFSFilesystemID(got) {
		t.Fatal("generated NFS identity failed its persisted-profile validation")
	}
}

func TestNFSKernelFilesystemIdentityRejectsUnavailableOrAmbiguousRows(t *testing.T) {
	for _, tc := range []struct {
		name, volumes string
	}{
		{"missing device", "NV SERVER PORT DEV FSID FSC\nv4 7f000001 801 0:65 1:2 no\n"},
		{"missing header", "v4 7f000001 801 0:64 1:2 no\n"},
		{"duplicate header", "NV SERVER PORT DEV FSID FSC\nNV SERVER PORT DEV FSID FSC\nv4 7f000001 801 0:64 1:2 no\n"},
		{"duplicate device", "NV SERVER PORT DEV FSID FSC\nv4 7f000001 801 0:64 1:2 no\nv4 7f000001 801 0:64 1:2 no\n"},
		{"malformed fsid", "NV SERVER PORT DEV FSID FSC\nv4 7f000001 801 0:64 1:bad! no\n"},
		{"malformed server", "NV SERVER PORT DEV FSID FSC\nv4 7F000001 801 0:64 1:2 no\n"},
		{"short IPv4 server", "NV SERVER PORT DEV FSID FSC\nv4 7f00001 801 0:64 1:2 no\n"},
		{"long IPv4 server", "NV SERVER PORT DEV FSID FSC\nv4 07f000001 801 0:64 1:2 no\n"},
		{"short IPv6 server", "NV SERVER PORT DEV FSID FSC\nv4 20010db800000000000000000000001 801 0:64 1:2 no\n"},
		{"invalid port", "NV SERVER PORT DEV FSID FSC\nv4 7f000001 0 0:64 1:2 no\n"},
		{"v3 target", "NV SERVER PORT DEV FSID FSC\nv3 7f000001 801 0:64 1:2 no\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := nfsFilesystemIDForDevice(tc.volumes, "0:64"); err == nil {
				t.Fatal("invalid NFS volume table was accepted")
			}
		})
	}
}

func TestNFSKernelFilesystemIdentityChangesWhenServerOrFilesystemChanges(t *testing.T) {
	base := "NV SERVER PORT DEV FSID FSC\nv4 7f000001 801 0:64 1:2 no\n"
	serverChanged := "NV SERVER PORT DEV FSID FSC\nv4 7f000002 801 0:64 1:2 no\n"
	filesystemChanged := "NV SERVER PORT DEV FSID FSC\nv4 7f000001 801 0:64 1:3 no\n"
	baseID, err := nfsFilesystemIDForDevice(base, "0:64")
	if err != nil {
		t.Fatal(err)
	}
	for _, volumes := range []string{serverChanged, filesystemChanged} {
		got, err := nfsFilesystemIDForDevice(volumes, "0:64")
		if err != nil || got == baseID {
			t.Fatalf("changed kernel NFS identity = %q, %v", got, err)
		}
	}
}

func TestNFSIdentityRejectsLegacyStatfsAndChangedProtocolProfiles(t *testing.T) {
	v := &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}
	const observed = "nfs4:7f000001:801:1:2"
	access := storageAccess{FilesystemID: "0000000000000000"}
	for _, stored := range []string{"", "0000000000000000", "nfs:legacy:fsid=0000000000000000", "nfs4:7f000001:801:1:3"} {
		s := protocol.StorageRegistration{Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: v.Server, Export: v.Export, FilesystemID: stored}}}
		if _, err := nfsIdentity(s, v, "nfs.example.test:/vms", access, observed); err == nil {
			t.Fatalf("stored NFS identity %q was auto-upgraded", stored)
		}
	}
	s := protocol.StorageRegistration{}
	got, err := nfsIdentity(s, v, "nfs.example.test:/vms", access, observed)
	if err != nil || got.Observed.NFS.FilesystemID != observed || got.FilesystemID != observed {
		t.Fatalf("fresh NFS identity = %+v, %v", got, err)
	}
}

func TestLinuxDeviceEncoding(t *testing.T) {
	if got := linuxDevice((uint64(0) << 32) | (uint64(64) & 0xff)); got != "0:64" {
		t.Fatalf("simple device = %q", got)
	}
	// Linux's split device encoding must retain high major/minor bits too.
	const major, minor = uint64(0x12345), uint64(0x456789)
	dev := (minor & 0xff) | ((major & 0xfff) << 8) | ((minor &^ 0xff) << 12) | ((major &^ 0xfff) << 32)
	if got, want := linuxDevice(dev), "74565:4548489"; got != want {
		t.Fatalf("extended device = %q, want %q", got, want)
	}
}

func TestSMBMarkerDurableIdentity(t *testing.T) {
	path := t.TempDir()
	v := &protocol.SMBStorageSource{Server: "smb.example.test", Share: "vms"}
	access := storageAccess{FilesystemID: "volatile-a", Identity: "smb://smb.example.test/vms:fsid=volatile-a"}
	// A new registration creates and returns its Core-owned durable identity.
	newRegistration := protocol.StorageRegistration{}
	created, err := smbIdentity(newRegistration, v, path, "//smb.example.test/vms", access)
	marker := created.Observed.SMB.MarkerID
	if err != nil || len(marker) != 32 || created.FilesystemID != "smb-marker:"+marker {
		t.Fatalf("create marker identity = %+v, %v", created, err)
	}
	registration := protocol.StorageRegistration{Identity: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: v.Server, Share: v.Share, MarkerID: marker}}}
	got, err := smbIdentity(registration, v, path, "//smb.example.test/vms", access)
	if err != nil || got.FilesystemID != "smb-marker:"+marker || got.Observed.SMB.MarkerID != marker || got.Observed.SMB.FilesystemID != "volatile-a" {
		t.Fatalf("marker identity = %+v, %v", got, err)
	}
	// A CIFS volume create time may change across reconnects. The exact marker remains valid.
	access.FilesystemID = "volatile-b"
	if got, err = smbIdentity(registration, v, path, "//smb.example.test/vms", access); err != nil || got.FilesystemID != "smb-marker:"+marker || got.Observed.SMB.FilesystemID != "volatile-b" {
		t.Fatalf("volatile CIFS fsid rejected despite marker = %+v, %v", got, err)
	}
	if err = os.WriteFile(filepath.Join(path, smbMarkerName), []byte(strings.Repeat("f", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = smbIdentity(registration, v, path, "//smb.example.test/vms", access); err == nil {
		t.Fatal("changed marker accepted")
	}
	if err = os.Remove(filepath.Join(path, smbMarkerName)); err != nil {
		t.Fatal(err)
	}
	if _, err = smbIdentity(registration, v, path, "//smb.example.test/vms", access); err == nil {
		t.Fatal("missing expected marker accepted")
	}
	if _, err = os.Lstat(filepath.Join(path, smbMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing expected marker was recreated: %v", err)
	}
	// Different SMB aliases to the same mounted share produce the same collision key.
	other := t.TempDir()
	first, err := smbIdentity(protocol.StorageRegistration{}, v, other, "//smb.example.test/vms", access)
	if err != nil {
		t.Fatal(err)
	}
	second, err := smbIdentity(protocol.StorageRegistration{}, &protocol.SMBStorageSource{Server: "smb-alias.example.test", Share: "vms"}, other, "//smb-alias.example.test/vms", access)
	if err != nil || first.FilesystemID != second.FilesystemID {
		t.Fatalf("SMB alias collision key = %q, %q, %v", first.FilesystemID, second.FilesystemID, err)
	}
}

func TestSMBMarkerRejectsUnsafeFilesAndConcurrentCreate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(string) error
	}{
		{"truncated", func(path string) error { return os.WriteFile(filepath.Join(path, smbMarkerName), []byte("abc"), 0600) }},
		{"oversized", func(path string) error {
			return os.WriteFile(filepath.Join(path, smbMarkerName), []byte(strings.Repeat("a", 33)), 0600)
		}},
		{"symlink", func(path string) error { return os.Symlink("missing", filepath.Join(path, smbMarkerName)) }},
		{"hardlink", func(path string) error {
			p := filepath.Join(path, smbMarkerName)
			if err := os.WriteFile(p, []byte(strings.Repeat("a", 32)), 0600); err != nil {
				return err
			}
			return os.Link(p, filepath.Join(path, "other"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			if err := tc.setup(path); err != nil {
				t.Fatal(err)
			}
			if _, err := smbMarker(path); err == nil {
				t.Fatal("unsafe marker accepted")
			}
		})
	}
	path := t.TempDir()
	const workers = 8
	markers := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			marker, err := smbMarker(path)
			markers <- marker
			errs <- err
		}()
	}
	wg.Wait()
	close(markers)
	close(errs)
	var got []string
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for marker := range markers {
		got = append(got, marker)
	}
	if len(got) != workers {
		t.Fatalf("concurrent markers count = %d, want %d (%q)", len(got), workers, got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[0] {
			t.Fatalf("concurrent markers = %q", got)
		}
	}
}

func TestSMBMarkerDirectorySyncPolicy(t *testing.T) {
	old := syncSMBMarkerDir
	t.Cleanup(func() { syncSMBMarkerDir = old })
	syncSMBMarkerDir = func(string) error { return syscall.EOPNOTSUPP }
	if _, err := smbMarker(t.TempDir()); err != nil {
		t.Fatalf("known unsupported directory sync rejected: %v", err)
	}
	syncSMBMarkerDir = func(string) error { return syscall.EIO }
	if _, err := smbMarker(t.TempDir()); err == nil {
		t.Fatal("unexpected directory sync failure accepted")
	}
}

func TestSMBMarkerRejectsRemovedEntryWithoutPanic(t *testing.T) {
	old := lstatSMBMarker
	t.Cleanup(func() { lstatSMBMarker = old })
	path := t.TempDir()
	lstatSMBMarker = func(marker string) (os.FileInfo, error) {
		if err := os.Remove(marker); err != nil {
			return nil, err
		}
		return os.Lstat(marker)
	}
	if _, err := smbMarker(path); err == nil {
		t.Fatal("removed marker accepted")
	}
}

func TestSMBMarkerLegacyFSIDRemainsStrict(t *testing.T) {
	v := &protocol.SMBStorageSource{Server: "smb.example.test", Share: "vms"}
	legacy := protocol.StorageRegistration{Identity: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: v.Server, Share: v.Share, FilesystemID: "old"}}}
	if _, err := smbIdentity(legacy, v, t.TempDir(), "//smb.example.test/vms", storageAccess{FilesystemID: "new"}); err == nil {
		t.Fatal("legacy storage auto-upgraded across a changed CIFS fsid")
	}
	legacyPath := t.TempDir()
	if got, err := smbIdentity(legacy, v, legacyPath, "//smb.example.test/vms", storageAccess{FilesystemID: "old"}); err != nil || got.Observed.SMB.MarkerID != "" {
		t.Fatalf("matching legacy filesystem identity = %+v, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(legacyPath, smbMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("matching legacy identity created marker: %v", err)
	}
	incomplete := protocol.StorageRegistration{Identity: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: v.Server, Share: v.Share}}}
	path := t.TempDir()
	if _, err := smbIdentity(incomplete, v, path, "//smb.example.test/vms", storageAccess{FilesystemID: "new"}); err == nil {
		t.Fatal("incomplete legacy identity auto-upgraded")
	}
	if _, err := os.Lstat(filepath.Join(path, smbMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete legacy identity created marker: %v", err)
	}
}

func TestPrepareRemoteStorageUsesManagedStrictMount(t *testing.T) {
	oldRun, oldMount, oldUnit, oldCredentials := nativeRun, storageMountRoot, storageUnitRoot, storageCredentialRoot
	t.Cleanup(func() {
		nativeRun, storageMountRoot, storageUnitRoot, storageCredentialRoot = oldRun, oldMount, oldUnit, oldCredentials
	})
	root := t.TempDir()
	storageMountRoot, storageUnitRoot, storageCredentialRoot = filepath.Join(root, "mounts"), filepath.Join(root, "units"), filepath.Join(root, "credentials")
	if got, want := systemdMountUnit("/var/lib/nex-storage/smb"), "var-lib-nex\\x2dstorage-smb.mount"; got != want {
		t.Fatalf("mount unit = %q, want %q", got, want)
	}
	if err := os.MkdirAll(storageCredentialRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageCredentialRoot, "receipt-1"), []byte("username=operator\npassword=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "findmnt" {
			path := args[len(args)-1]
			if strings.Contains(path, "nfs") {
				return []byte("nfs.example.test:/vms nfs4 rw,vers=4,hard,_netdev\n"), nil
			}
			return []byte("//smb.example.test/vms cifs rw,vers=3.1.1,seal,cache=strict,_netdev\n"), nil
		}
		return nil, nil
	}

	nfs := protocol.StorageRegistration{ID: "nfs", PoolID: "pool", HostID: "host", Generation: 1, Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}}}
	if _, err := prepareRemote(context.Background(), nfs, "nfs.example.test:/vms", "nfs4", "vers=4,hard,_netdev"); err != nil {
		t.Fatalf("prepare NFS mount: %v", err)
	}
	smb := protocol.StorageRegistration{ID: "smb", PoolID: "pool", HostID: "host", Generation: 1, Kind: protocol.StorageKindSMB, CredentialReceipt: "receipt-1", Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "vms"}}}
	if _, err := prepareRemote(context.Background(), smb, "//smb.example.test/vms", "cifs", "vers=3.1.1,seal,cache=strict,credentials="+filepath.Join(storageCredentialRoot, "receipt-1")+",_netdev"); err != nil {
		t.Fatalf("prepare SMB mount: %v", err)
	}
	_, unitPath := remoteUnitPath(smb)
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{"Type=cifs", "Before=libvirtd.service", "vers=3.1.1", "seal", "cache=strict", "credentials=" + filepath.Join(storageCredentialRoot, "receipt-1")} {
		if !strings.Contains(text, want) {
			t.Errorf("SMB unit missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "password=secret") {
		t.Error("SMB secret entered mount unit")
	}
	if len(calls) == 0 {
		t.Error("no native commands run")
	}
}

func TestVerifyRemoteMountAcceptsExactDuplicateNamespaceRows(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	line := "//smb.example.test/vms cifs rw,vers=3.1.1,seal,cache=strict,uid=1000,gid=1000,file_mode=0600,dir_mode=0700 /var/lib/nex-storage/smb\n"
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "findmnt" {
			t.Fatalf("unexpected command %q", name)
		}
		return []byte(line + line), nil
	}
	if err := verifyRemoteMount(context.Background(), "/var/lib/nex-storage/smb", "//smb.example.test/vms", "cifs", smbRequiredMountOptions("1000", "1000")); err != nil {
		t.Fatalf("exact duplicate mount rows rejected: %v", err)
	}
}

func TestSMBMountMapsFilesToLibvirtQEMU(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "id" || len(args) != 2 || args[1] != "libvirt-qemu" {
			t.Fatalf("identity command = %q %q", name, args)
		}
		switch args[0] {
		case "-u":
			return []byte("64055\n"), nil
		case "-g":
			return []byte("64056\n"), nil
		default:
			t.Fatalf("identity flag = %q", args[0])
			return nil, nil
		}
	}
	uid, gid, err := libvirtQEMUIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := smbMountOptions("/private/receipt", uid, gid)
	for _, want := range []string{"vers=3.1.1", "seal", "cache=strict", "credentials=/private/receipt", "uid=64055", "gid=64056", "file_mode=0600", "dir_mode=0700"} {
		if !strings.Contains(","+got+",", ","+want+",") {
			t.Errorf("SMB options missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "noperm") || strings.Contains(got, "0777") {
		t.Fatalf("SMB options weaken local permissions: %s", got)
	}
}

func TestSMBMountRejectsUnsafeOrMismappedOptions(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	path := "/var/lib/nex-storage/smb"
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "findmnt" {
			t.Fatalf("command = %q", name)
		}
		return []byte("//smb.example.test/vms cifs rw,vers=3.1.1,seal,cache=strict,uid=64055,gid=64056,file_mode=0600,dir_mode=0700,noperm " + path + "\n"), nil
	}
	err := verifyRemoteMount(context.Background(), path, "//smb.example.test/vms", "cifs", smbRequiredMountOptions("64055", "64056"))
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe SMB mount accepted: %v", err)
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte("//smb.example.test/vms cifs rw,vers=3.1.1,seal,cache=strict,uid=0,gid=64056,file_mode=0600,dir_mode=0700 " + path + "\n"), nil
	}
	err = verifyRemoteMount(context.Background(), path, "//smb.example.test/vms", "cifs", smbRequiredMountOptions("64055", "64056"))
	if err == nil || !strings.Contains(err.Error(), "uid=64055") {
		t.Fatalf("mismapped SMB mount accepted: %v", err)
	}
}

func TestLibvirtQEMUIdentityRejectsNonNumericOutput(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte("0,dir_mode=0777\n"), nil
	}
	if _, _, err := libvirtQEMUIdentity(context.Background()); err == nil {
		t.Fatal("unsafe identity output accepted")
	}
}

func TestRemoteMountRejectsReplacementAndAdminUnit(t *testing.T) {
	oldRun, oldMount, oldUnit := nativeRun, storageMountRoot, storageUnitRoot
	t.Cleanup(func() { nativeRun, storageMountRoot, storageUnitRoot = oldRun, oldMount, oldUnit })
	root := t.TempDir()
	storageMountRoot, storageUnitRoot = filepath.Join(root, "mounts"), filepath.Join(root, "units")
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "findmnt" {
			return []byte("other.example.test:/vms nfs4 rw,vers=4,hard\n"), nil
		}
		return nil, nil
	}
	s := protocol.StorageRegistration{ID: "nfs", PoolID: "pool", HostID: "host", Generation: 1, Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}}}
	if _, err := prepareNFS(context.Background(), s); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("replaced mount accepted: %v", err)
	}
	_, unitPath := remoteUnitPath(s)
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed validation left a new mount unit behind: %v", err)
	}
	if err := os.WriteFile(unitPath, []byte("[Mount]\nWhat=admin:/vms\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := detachRemote(context.Background(), s); err == nil {
		t.Fatal("administrator unit detached")
	}
	if _, err := prepareRemote(allowStorageRemount(context.Background()), s, "nfs.example.test:/vms", "nfs4", "vers=4,hard,_netdev"); err == nil {
		t.Fatal("administrator unit replaced")
	}
	if got, err := os.ReadFile(unitPath); err != nil || string(got) != "[Mount]\nWhat=admin:/vms\n" {
		t.Fatalf("administrator unit changed: %q, %v", got, err)
	}
}

func TestSMBCredentialRotationRestoresWorkingUnitOnRestartFailure(t *testing.T) {
	oldRun, oldMount, oldUnit := nativeRun, storageMountRoot, storageUnitRoot
	t.Cleanup(func() { nativeRun, storageMountRoot, storageUnitRoot = oldRun, oldMount, oldUnit })
	root := t.TempDir()
	storageMountRoot, storageUnitRoot = filepath.Join(root, "mounts"), filepath.Join(root, "units")
	s := protocol.StorageRegistration{ID: "smb", Kind: protocol.StorageKindSMB}
	unit, unitPath := remoteUnitPath(s)
	old := remoteUnit("//smb.example.test/vms", remoteMountPath(s), "cifs", smbMountOptions("/private/old", "64055", "64056"))
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, old, 0600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "systemctl" && len(args) == 2 && args[0] == "restart" && args[1] == unit {
			return nil, errors.New("new credentials rejected")
		}
		return nil, nil
	}
	_, err := prepareRemote(allowStorageRemount(context.Background()), s, "//smb.example.test/vms", "cifs", smbMountOptions("/private/new", "64055", "64056"))
	if err == nil {
		t.Fatal("credential rotation unexpectedly succeeded")
	}
	got, readErr := os.ReadFile(unitPath)
	if readErr != nil || string(got) != string(old) {
		t.Fatalf("working unit was not restored: %q, %v", got, readErr)
	}
	if len(calls) != 5 || strings.Join(calls[3], " ") != "systemctl daemon-reload" || strings.Join(calls[4], " ") != "systemctl restart "+unit {
		t.Fatalf("rotation did not reload and restart the restored unit: %#v", calls)
	}
}

func remoteMountTestSetup(t *testing.T) (*[][]string, func(protocol.StorageRegistration) ([]byte, string)) {
	t.Helper()
	oldRun, oldMount, oldUnit := nativeRun, storageMountRoot, storageUnitRoot
	t.Cleanup(func() { nativeRun, storageMountRoot, storageUnitRoot = oldRun, oldMount, oldUnit })
	root := t.TempDir()
	storageMountRoot, storageUnitRoot = filepath.Join(root, "mounts"), filepath.Join(root, "units")
	calls := &[][]string{}
	writeUnit := func(s protocol.StorageRegistration) ([]byte, string) {
		_, unitPath := remoteUnitPath(s)
		content := remoteUnit("//smb.example.test/vms", remoteMountPath(s), "cifs", smbMountOptions("/private/receipt", "64055", "64056"))
		if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(unitPath, content, 0600); err != nil {
			t.Fatal(err)
		}
		return content, unitPath
	}
	return calls, writeUnit
}

func TestStorageRecheckStartsInactiveRemoteMountUnit(t *testing.T) {
	calls, writeUnit := remoteMountTestSetup(t)
	s := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Name: "repo-1", Kind: protocol.StorageKindSMB, Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "vms"}}, Generation: 1}
	content, unitPath := writeUnit(s)
	unit, _ := remoteUnitPath(s)
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		switch {
		case name == "findmnt":
			return []byte("/dev/vda1 ext4 rw,relatime /\n"), nil
		case name == "id":
			return []byte("64055\n"), nil
		case name == "systemctl" && len(args) > 0 && args[0] == "is-active":
			return nil, errors.New("inactive")
		}
		return nil, nil
	}
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "register-1", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	request := protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: "repo-1", PoolID: "pool-1", HostID: "host-1"}
	result, _ := e.Execute(context.Background(), protocol.Command{OperationID: "recheck-1", Kind: "storage-recheck", StorageLifecycle: &request}, nil)
	if !strings.Contains(result.Error, "not mounted") {
		t.Fatalf("recheck result = %+v", result)
	}
	started := false
	for _, c := range *calls {
		line := strings.Join(c, " ")
		if line == "systemctl start "+unit {
			started = true
		}
		for _, bad := range []string{"restart", "stop", "enable", "disable", "daemon-reload"} {
			if c[0] == "systemctl" && len(c) > 1 && c[1] == bad {
				t.Fatalf("recheck ran %q", line)
			}
		}
	}
	if !started {
		t.Fatalf("recheck did not start inactive unit: %#v", *calls)
	}
	if got, err := os.ReadFile(unitPath); err != nil || string(got) != string(content) {
		t.Fatalf("unit changed: %q, %v", got, err)
	}
}

func TestStartRemoteMountsSkipsActiveBusyAndAdminUnits(t *testing.T) {
	calls, writeUnit := remoteMountTestSetup(t)
	smb := func(id string) protocol.StorageRegistration {
		return protocol.StorageRegistration{ID: id, PoolID: "pool-1", HostID: "host-1", Name: id, Kind: protocol.StorageKindSMB, Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "vms"}}, Generation: 1}
	}
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	units := map[string][]byte{}
	for _, id := range []string{"inactive", "active", "busy", "admin"} {
		s := smb(id)
		if err := e.save(managedJournal{Command: protocol.Command{OperationID: "register-" + id, Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "register-" + id, Status: "succeeded"}}); err != nil {
			t.Fatal(err)
		}
		content, unitPath := writeUnit(s)
		if id == "admin" {
			content = []byte("[Mount]\nWhat=admin:/vms\n")
			if err := os.WriteFile(unitPath, content, 0600); err != nil {
				t.Fatal(err)
			}
		}
		units[unitPath] = content
	}
	busy := protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: "busy", PoolID: "pool-1", HostID: "host-1"}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "recheck-busy", Kind: "storage-recheck", StorageLifecycle: &busy}, Result: protocol.Result{OperationID: "recheck-busy", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	activeUnit, _ := remoteUnitPath(smb("active"))
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if name == "systemctl" && args[0] == "is-active" && args[len(args)-1] != activeUnit {
			return nil, errors.New("inactive")
		}
		return nil, nil
	}
	e.StartRemoteMounts(context.Background())
	inactiveUnit, _ := remoteUnitPath(smb("inactive"))
	var got []string
	for _, c := range *calls {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{"systemctl is-active --quiet " + activeUnit, "systemctl is-active --quiet " + inactiveUnit, "systemctl start --no-block " + inactiveUnit}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	entries, _ := os.ReadDir(storageUnitRoot)
	if len(entries) != len(units) {
		t.Fatalf("unit files = %d, want %d", len(entries), len(units))
	}
	for path, content := range units {
		if b, err := os.ReadFile(path); err != nil || string(b) != string(content) {
			t.Fatalf("unit %s changed: %q, %v", path, b, err)
		}
	}
}

func TestVerifyRemoteMountReportsUnmountedParent(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("/dev/vda1 ext4 rw,relatime /\n"), nil
	}
	if err := verifyRemoteMount(context.Background(), "/var/lib/nex-storage/smb", "//smb.example.test/vms", "cifs", nil); !errors.Is(err, errStorageNotMounted) {
		t.Fatalf("unmounted path error = %v", err)
	}
}
