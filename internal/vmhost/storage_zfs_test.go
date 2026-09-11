// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestPrepareZFSBindsNewRepositoryIntoCoreRoot(t *testing.T) {
	oldRun, oldRoot, oldUnits := nativeRun, storageMountRoot, storageUnitRoot
	t.Cleanup(func() { nativeRun, storageMountRoot, storageUnitRoot = oldRun, oldRoot, oldUnits })
	root := t.TempDir()
	storageMountRoot, storageUnitRoot = filepath.Join(root, "storage"), filepath.Join(root, "units")
	s := protocol.StorageRegistration{ID: "zfs", Kind: protocol.StorageKindZFS, Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "tank", Dataset: "vms", MountPath: "/srv/zfs"}}}
	alias := remoteMountPath(s)
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name + " " + args[0] {
		case "zfs get":
			return []byte("99\n4096\nyes\n/srv/zfs\n"), nil
		case "zpool get":
			return []byte("42\n"), nil
		case "zpool list":
			return []byte("ONLINE\n"), nil
		case "findmnt --noheadings":
			if args[len(args)-1] == "/srv/zfs" {
				return []byte("tank/vms zfs\n"), nil
			}
			return []byte("tank/vms zfs " + alias + "\n"), nil
		case "systemctl daemon-reload", "systemctl enable":
			return nil, nil
		}
		t.Fatalf("unexpected command %q %q", name, args)
		return nil, nil
	}
	if err := prepareZFSBind(context.Background(), s, "tank/vms", "/srv/zfs", alias); err != nil {
		t.Fatalf("prepareZFSBind() = %v", err)
	}
	unit, err := os.ReadFile(filepath.Join(storageUnitRoot, systemdMountUnit(alias)))
	if err != nil || string(unit) == "" || !containsAll(string(unit), "What=/srv/zfs", "Where="+alias, "Options=bind") {
		t.Fatalf("ZFS alias unit = %q, %v", unit, err)
	}
}

func containsAll(value string, want ...string) bool {
	for _, part := range want {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}

func TestZFSProbeReadsStableIdentityAndCapacity(t *testing.T) {
	old := nativeRun
	t.Cleanup(func() { nativeRun = old })
	var calls [][]string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch name + " " + args[0] {
		case "zfs get":
			return []byte("99\n4096\nyes\n/srv/zfs\n"), nil
		case "zpool get":
			return []byte("42\n"), nil
		case "zpool list":
			return []byte("ONLINE\n"), nil
		}
		t.Fatalf("unexpected native command: %q %q", name, args)
		return nil, nil
	}
	pool, dataset, available, mounted, mountpoint, health, err := zfsProbe(context.Background(), "tank", "tank/vms")
	if err != nil || pool != "42" || dataset != "99" || available != 4096 || mounted != "yes" || mountpoint != "/srv/zfs" || health != "ONLINE" {
		t.Fatalf("zfsProbe() = %q %q %d %q %q %q %v", pool, dataset, available, mounted, mountpoint, health, err)
	}
	want := [][]string{{"zfs", "get", "-H", "-p", "-o", "value", "guid,available,mounted,mountpoint", "tank/vms"}, {"zpool", "get", "-H", "-p", "-o", "value", "guid", "tank"}, {"zpool", "list", "-H", "-o", "health", "tank"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands = %#v, want %#v", calls, want)
	}
}

func TestInspectZFSRejectsChangedIdentityWithoutMounting(t *testing.T) {
	old := nativeRun
	t.Cleanup(func() { nativeRun = old })
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name + " " + args[0] {
		case "zfs get":
			return []byte("99\n4096\nyes\n/srv/zfs\n"), nil
		case "zpool get":
			return []byte("42\n"), nil
		case "zpool list":
			return []byte("ONLINE\n"), nil
		}
		t.Fatalf("unexpected native command: %q %q", name, args)
		return nil, nil
	}
	s := protocol.StorageRegistration{Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "tank", Dataset: "vms", MountPath: "/srv/zfs"}}, Identity: protocol.StorageIdentity{ZFS: &protocol.ZFSStorageIdentity{PoolGUID: "42", DatasetGUID: "changed"}}}
	if _, err := inspectZFS(context.Background(), s); err == nil {
		t.Fatal("inspectZFS accepted a changed dataset GUID")
	}
}

func TestZFSProbeRejectsBadCapacityAndPoolHealth(t *testing.T) {
	old := nativeRun
	t.Cleanup(func() { nativeRun = old })
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "zfs" {
			return []byte("99\nnot-a-number\nyes\n/srv/zfs\n"), nil
		}
		return []byte("ONLINE\n"), nil
	}
	if _, _, _, _, _, _, err := zfsProbe(context.Background(), "tank", "tank/vms"); err == nil {
		t.Fatal("zfsProbe accepted invalid available capacity")
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name + " " + args[0] {
		case "zfs get":
			return []byte("99\n4096\nyes\n/srv/zfs\n"), nil
		case "zpool get":
			return []byte("42\n"), nil
		default:
			return []byte("DEGRADED\n"), nil
		}
	}
	_, _, _, _, _, health, err := zfsProbe(context.Background(), "tank", "tank/vms")
	if err != nil || health != "DEGRADED" {
		t.Fatalf("zfsProbe() health = %q, %v", health, err)
	}
}

func TestInspectZFSRejectsSystemdUnsafeMountPath(t *testing.T) {
	for _, path := range []string{"/srv/zfs%p", "/srv/zfs\\x", "/srv/zfs space", "/srv/zfs\x1b"} {
		s := protocol.StorageRegistration{Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "tank", Dataset: "vms", MountPath: path}}}
		if _, err := inspectZFS(context.Background(), s); err == nil {
			t.Fatalf("accepted systemd-unsafe ZFS path %q", path)
		}
	}
}

func TestVerifyZFSBindAcceptsExactDuplicateRows(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	alias := "/var/lib/nex-storage/zfs"
	row := "tank/vms zfs " + alias + "\n"
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "findmnt" {
			t.Fatalf("unexpected command %q", name)
		}
		return []byte(row + row), nil
	}
	if err := verifyZFSBind(context.Background(), "tank/vms", alias); err != nil {
		t.Fatalf("duplicate rows rejected: %v", err)
	}
	nativeRun = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(row + "tank/other zfs " + alias + "\n"), nil
	}
	if err := verifyZFSBind(context.Background(), "tank/vms", alias); err == nil {
		t.Fatal("divergent rows accepted")
	}
}

func TestPrepareZFSBindReportsUncertainCleanup(t *testing.T) {
	oldRun, oldUnits := nativeRun, storageUnitRoot
	t.Cleanup(func() { nativeRun, storageUnitRoot = oldRun, oldUnits })
	storageUnitRoot = t.TempDir()
	s := protocol.StorageRegistration{ID: "zfs"}
	alias := filepath.Join(t.TempDir(), "alias")
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "enable" {
			return nil, errors.New("enable failed")
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "disable" {
			return nil, errors.New("disable failed")
		}
		return nil, nil
	}
	err := prepareZFSBind(context.Background(), s, "tank/vms", "/srv/zfs", alias)
	if !errors.Is(err, ErrStorageRollbackUncertain) {
		t.Fatalf("prepareZFSBind() = %v, want rollback uncertainty", err)
	}
	if _, err := os.Stat(filepath.Join(storageUnitRoot, systemdMountUnit(alias))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created unit remained after rollback: %v", err)
	}
}

func TestPrepareZFSBindPreservesExistingUnitOnRetryFailure(t *testing.T) {
	oldRun, oldUnits := nativeRun, storageUnitRoot
	t.Cleanup(func() { nativeRun, storageUnitRoot = oldRun, oldUnits })
	storageUnitRoot = t.TempDir()
	s := protocol.StorageRegistration{ID: "zfs"}
	alias := filepath.Join(t.TempDir(), "alias")
	unitPath := filepath.Join(storageUnitRoot, systemdMountUnit(alias))
	old := zfsBindUnit("/srv/zfs", alias)
	if err := os.WriteFile(unitPath, old, 0600); err != nil {
		t.Fatal(err)
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" && len(args) > 0 && args[0] == "enable" {
			return nil, errors.New("enable failed")
		}
		return nil, nil
	}
	if err := prepareZFSBind(context.Background(), s, "tank/vms", "/srv/zfs", alias); err == nil {
		t.Fatal("accepted failed retry")
	}
	got, err := os.ReadFile(unitPath)
	if err != nil || string(got) != string(old) {
		t.Fatalf("existing unit changed: %q, %v", got, err)
	}
}
