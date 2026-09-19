// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDiscoverZFSDevicesClassifiesOnlySafeWholeDisks(t *testing.T) {
	oldRun, oldDir, oldRead, oldHolders, oldEval, oldByID := nativeRun, zfsDiscoveryReadDir, zfsDiscoveryReadFile, zfsDiscoveryHolders, zfsDiscoveryEvalSymlinks, diskByIDPath
	t.Cleanup(func() {
		nativeRun, zfsDiscoveryReadDir, zfsDiscoveryReadFile, zfsDiscoveryHolders, zfsDiscoveryEvalSymlinks, diskByIDPath = oldRun, oldDir, oldRead, oldHolders, oldEval, oldByID
	})
	zfsDiscoveryReadFile = func(string) ([]byte, error) { return []byte("Filename\tType\tSize\tUsed\tPriority\n"), nil }
	zfsDiscoveryHolders = func(string) ([]os.DirEntry, error) { return nil, nil }
	root := t.TempDir()
	diskByIDPath = root
	for _, name := range []string{"wwn-safe", "wwn-old", "wwn-mounted", "wwn-lvm", "wwn-zpool", "wwn-zpool-part1"} {
		if err := os.Symlink("/dev/"+name, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	zfsDiscoveryEvalSymlinks = func(path string) (string, error) {
		if strings.HasPrefix(path, root+string(filepath.Separator)) {
			return "/dev/" + filepath.Base(path), nil
		}
		return path, nil
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "lsblk":
			if len(args) == 0 || args[0] != "--json" || !slices.Contains(args, "--tree") {
				t.Fatalf("lsblk args %q", args)
			}
			return []byte(`{"blockdevices":[
{"path":"/dev/wwn-safe","type":"disk","model":" Fast ","serial":" S1 ","size":100,"rota":false},
{"path":"/dev/wwn-old","type":"disk","serial":"S2","size":200,"rota":true,"pttype":"gpt"},
{"path":"/dev/wwn-mounted","type":"disk","children":[{"path":"/dev/wwn-mounted1","type":"part","fstype":"ext4","mountpoints":["/"]}]},
{"path":"/dev/wwn-lvm","type":"disk","fstype":"LVM2_member"},
{"path":"/dev/wwn-zpool","type":"disk","children":[{"path":"/dev/wwn-zpool1","type":"part"}]},
{"path":"/dev/loop0","type":"loop"}]}`), nil
		case "zpool":
			if len(args) != 2 || args[0] != "status" || args[1] != "-P" {
				t.Fatalf("zpool args %q", args)
			}
			return []byte("  /dev/wwn-zpool1  ONLINE\n"), nil
		case "wipefs":
			return []byte(`{"signatures":[]}`), nil
		}
		return nil, errors.New("unexpected command")
	}
	got, err := DiscoverZFSDevices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("devices = %+v", got)
	}
	if got[0].ByID != filepath.Join(root, "wwn-lvm") || got[0].Eligible || got[0].Reason != "member of md or LVM" {
		t.Fatalf("lvm = %+v", got[0])
	}
	if got[1].ByID != filepath.Join(root, "wwn-mounted") || got[1].Eligible || got[1].Reason != "mounted" {
		t.Fatalf("mounted = %+v", got[1])
	}
	if got[2].ByID != filepath.Join(root, "wwn-old") || !got[2].Eligible || !got[2].RequiresErase || got[2].Reason != "contains existing data" {
		t.Fatalf("old = %+v", got[2])
	}
	if got[3].ByID != filepath.Join(root, "wwn-safe") || !got[3].Eligible || got[3].RequiresErase || got[3].Model != "Fast" || got[3].Serial != "S1" {
		t.Fatalf("safe = %+v", got[3])
	}
	if got[4].ByID != filepath.Join(root, "wwn-zpool") || got[4].Eligible || got[4].Reason != "member of an imported zpool" {
		t.Fatalf("zpool = %+v", got[4])
	}
}

func TestDiscoverZFSDevicesFailsClosedWhenObservationFails(t *testing.T) {
	old := nativeRun
	t.Cleanup(func() { nativeRun = old })
	nativeRun = func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, errors.New("unavailable") }
	if _, err := DiscoverZFSDevices(context.Background()); err == nil {
		t.Fatal("accepted unavailable observation")
	}
}

func TestZFSDeviceEligibilityRejectsActiveSwapAndMountedSystemAncestor(t *testing.T) {
	swap := zfsDiscoveryBlock{Path: "/dev/sdb", Type: "disk", Children: []zfsDiscoveryBlock{{Path: "/dev/sdb1", Type: "part", FSType: "swap"}}}
	if eligible, _, reason := zfsDeviceEligibility(swap, nil, map[string]bool{"/dev/sdb1": true}); eligible || reason != "active swap" {
		t.Fatalf("swap eligibility = %t, %q", eligible, reason)
	}
	system := zfsDiscoveryBlock{Path: "/dev/sda", Type: "disk", Children: []zfsDiscoveryBlock{{Path: "/dev/sda2", Type: "part", Mountpoints: []string{"/"}}}}
	if eligible, _, reason := zfsDeviceEligibility(system, nil, nil); eligible || reason != "mounted" {
		t.Fatalf("system disk eligibility = %t, %q", eligible, reason)
	}
}

func TestZFSBlockHasHoldersFailsClosed(t *testing.T) {
	old := zfsDiscoveryHolders
	t.Cleanup(func() { zfsDiscoveryHolders = old })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dm-0"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	zfsDiscoveryHolders = func(string) ([]os.DirEntry, error) { return os.ReadDir(dir) }
	held, err := zfsBlockHasHolders(zfsDiscoveryBlock{Path: "/dev/sdb", Type: "disk"})
	if err != nil || !held {
		t.Fatalf("holders = %t, %v", held, err)
	}
	zfsDiscoveryHolders = func(string) ([]os.DirEntry, error) { return nil, errors.New("unavailable") }
	if _, err := zfsBlockHasHolders(zfsDiscoveryBlock{Path: "/dev/sdb", Type: "disk"}); err == nil {
		t.Fatal("accepted unavailable holder observation")
	}
}

func TestZFSPoolMembersIgnoresNoPoolsOutput(t *testing.T) {
	if members := zfsPoolMembers("no pools available"); len(members) != 0 {
		t.Fatalf("members = %v", members)
	}
}

func TestZFSDeviceFingerprintIncludesWipefsSignatures(t *testing.T) {
	block := zfsDiscoveryBlock{Path: "/dev/sdb", Type: "disk", FSType: "ext4"}
	candidate := ZFSDeviceCandidate{ByID: "/dev/disk/by-id/disk", Serial: "serial", SizeBytes: 1, Eligible: true, RequiresErase: true, Reason: "contains existing data"}
	first := zfsDeviceFingerprint(block, []zfsDeviceSignature{{Offset: "0x438", Type: "ext4", UUID: "old"}}, candidate)
	second := zfsDeviceFingerprint(block, []zfsDeviceSignature{{Offset: "0x438", Type: "ext4", UUID: "new"}}, candidate)
	if first == second {
		t.Fatal("filesystem signature change did not change fingerprint")
	}
}

func TestDiscoverZFSDevicesAcceptsNoImportedPools(t *testing.T) {
	oldRun, oldDir, oldRead, oldHolders, oldEval, oldByID := nativeRun, zfsDiscoveryReadDir, zfsDiscoveryReadFile, zfsDiscoveryHolders, zfsDiscoveryEvalSymlinks, diskByIDPath
	t.Cleanup(func() {
		nativeRun, zfsDiscoveryReadDir, zfsDiscoveryReadFile, zfsDiscoveryHolders, zfsDiscoveryEvalSymlinks, diskByIDPath = oldRun, oldDir, oldRead, oldHolders, oldEval, oldByID
	})
	root := t.TempDir()
	diskByIDPath = root
	if err := os.Symlink("/dev/sdb", filepath.Join(root, "disk")); err != nil {
		t.Fatal(err)
	}
	zfsDiscoveryEvalSymlinks = func(path string) (string, error) {
		if path == filepath.Join(root, "disk") {
			return "/dev/sdb", nil
		}
		return path, nil
	}
	zfsDiscoveryReadFile = func(string) ([]byte, error) { return []byte("Filename\tType\tSize\tUsed\tPriority\n"), nil }
	zfsDiscoveryHolders = func(string) ([]os.DirEntry, error) { return nil, nil }
	// Run the real zpool failure through run(), so the allowance is tested
	// against the error value the host actually produces: a failed command
	// returns no stdout at all, and its reason survives only in the error.
	fakeCommand(t, "zpool", `echo "no pools available" >&2; exit 1`)
	nativeRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "lsblk":
			return []byte(`{"blockdevices":[{"path":"/dev/sdb","type":"disk","serial":"disk","size":1}]}`), nil
		case "zpool":
			return run(ctx, name, args...)
		case "wipefs":
			return []byte(`{"signatures":[]}`), nil
		}
		return nil, errors.New("unexpected command")
	}
	devices, err := DiscoverZFSDevices(context.Background())
	if err != nil || len(devices) != 1 || !devices[0].Eligible {
		t.Fatalf("devices = %+v, err = %v", devices, err)
	}
	fakeCommand(t, "zpool", `echo "cannot open '/dev/sdb': permission denied" >&2; exit 1`)
	if _, err := DiscoverZFSDevices(context.Background()); err == nil {
		t.Fatal("accepted a genuine zpool failure as an empty pool list")
	}
}

// util-linux prints a flat device list unless --tree is given. Every in-use check walks a
// disk's children, so a flat list once offered the disk of an imported, online zpool as erasable.
func TestDiscoverZFSDevicesRefusesAFlatDeviceList(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "lsblk" {
			return []byte(`{"blockdevices":[{"path":"/dev/vdb","type":"disk","size":100},{"path":"/dev/vdb1","type":"part","fstype":"zfs_member"}]}`), nil
		}
		return nil, nil
	}
	if devices, err := DiscoverZFSDevices(context.Background()); err == nil {
		t.Fatalf("a flat lsblk list offered devices: %+v", devices)
	}
}
