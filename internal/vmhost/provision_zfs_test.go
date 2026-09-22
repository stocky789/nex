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

func TestZFSProvisionCreatesCoreOwnedPoolOnce(t *testing.T) {
	oldRun, oldRoot := nativeRun, storageMountRoot
	oldDiscover := zfsProvisionDiscover
	oldProvisionRun := zfsProvisionRun
	t.Cleanup(func() {
		nativeRun, storageMountRoot, zfsProvisionDiscover, zfsProvisionRun = oldRun, oldRoot, oldDiscover, oldProvisionRun
	})
	zfsProvisionRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nativeRun(ctx, name, args...)
	}
	storageMountRoot = filepath.Join(t.TempDir(), "storage")
	request := testZFSProvision(storageMountRoot)
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint}}, nil
	}
	var calls [][]string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch name {
		case "zpool":
			if len(args) > 0 && args[0] == "list" {
				if strings.Contains(strings.Join(args, " "), "health") {
					return []byte("ONLINE\n"), nil
				}
				return []byte("\n"), nil
			}
			if len(args) > 0 && args[0] == "get" {
				return []byte("pool-guid\n"), nil
			}
		case "zfs":
			if len(args) > 0 && args[0] == "get" {
				return []byte("dataset-guid\n"), nil
			}
		case "lsblk":
			return []byte(`{"blockdevices":[{"path":"/dev/sdb","serial":"disk-1","size":1073741824,"type":"disk","mountpoints":"","fstype":""}]}`), nil
		case "wipefs":
			return []byte(`{"signatures":[]}`), nil
		}
		return nil, nil
	}
	result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("Provision() = %+v, %v", result, err)
	}
	if !provisionCall(calls, "zpool", "create", "-o", "ashift=12", "-O", "nex:repository=repo-1", "-m", "none", "nex-repo-1", "/dev/disk/by-id/disk-1") {
		t.Fatalf("zpool create call missing: %#v", calls)
	}
	if provisionCommandCalled(calls, "sgdisk") {
		t.Fatalf("provision requires undeclared gdisk dependency: %#v", calls)
	}
	if !provisionCall(calls, "zfs", "create", "-o", "compression=lz4", "-o", "atime=off", "-o", "xattr=sa", "-o", "mountpoint="+request.MountPath, "nex-repo-1/vms") {
		t.Fatalf("dataset create call missing: %#v", calls)
	}
}

func TestZFSProvisionRetryAdoptsMarkedPoolWithoutWiping(t *testing.T) {
	oldRun, oldRoot := nativeRun, storageMountRoot
	oldDiscover := zfsProvisionDiscover
	oldProvisionRun := zfsProvisionRun
	t.Cleanup(func() {
		nativeRun, storageMountRoot, zfsProvisionDiscover, zfsProvisionRun = oldRun, oldRoot, oldDiscover, oldProvisionRun
	})
	zfsProvisionRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nativeRun(ctx, name, args...)
	}
	storageMountRoot = filepath.Join(t.TempDir(), "storage")
	request, state := testZFSProvision(storageMountRoot), t.TempDir()
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return nil, errors.New("must not discover adopted pool")
	}
	var wiped bool
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "zpool" && len(args) > 0 && args[0] == "list" && strings.Contains(strings.Join(args, " "), "health") {
			return []byte("ONLINE\n"), nil
		}
		if name == "zpool" && len(args) > 0 && args[0] == "list" {
			return []byte("nex-repo-1\n"), nil
		}
		if name == "zpool" && len(args) > 0 && args[0] == "status" {
			return []byte("\t/dev/disk/by-id/disk-1 ONLINE\n"), nil
		}
		if name == "zpool" && len(args) > 0 && args[0] == "get" {
			return []byte("repo-1\n"), nil
		}
		if name == "zfs" && len(args) > 0 && args[0] == "get" {
			if strings.Contains(strings.Join(args, " "), "nex:repository") {
				return []byte("repo-1\n"), nil
			}
			if strings.Contains(strings.Join(args, " "), " mounted ") {
				return []byte("yes\n"), nil
			}
			return []byte(request.MountPath + "\n"), nil
		}
		if name == "wipefs" {
			wiped = true
		}
		return nil, nil
	}
	result, err := NewZFSProvisioner(state).provision(context.Background(), request)
	if err != nil || result.Status != "succeeded" || wiped {
		t.Fatalf("adopt = %+v, %v, wiped=%v", result, err, wiped)
	}
}

func TestCoreProvisionedZFSCapabilitiesExcludeISO(t *testing.T) {
	provisioned := ZFSProvisionStorage("repo-1", "pool-1", "host-1", "fast")
	if capabilities := storageCapabilities(provisioned); capabilities.ISO || !capabilities.VMDisk {
		t.Fatalf("provisioned ZFS capabilities = %#v", capabilities)
	}
	existing := provisioned
	existing.ZFSProvisioned = false
	if capabilities := storageCapabilities(existing); !capabilities.ISO {
		t.Fatalf("existing ZFS capabilities lost ISO support: %#v", capabilities)
	}
}

func TestZFSVdevWholeDiskRequiresSysfsPartitionMarker(t *testing.T) {
	oldRoot, oldRead, oldEval := zfsProvisionSysBlockRoot, zfsProvisionReadFile, zfsProvisionEvalSymlinks
	t.Cleanup(func() {
		zfsProvisionSysBlockRoot, zfsProvisionReadFile, zfsProvisionEvalSymlinks = oldRoot, oldRead, oldEval
	})
	zfsProvisionSysBlockRoot = "/test/sys"
	zfsProvisionReadFile = func(path string) ([]byte, error) {
		if path == "/test/sys/nvme0n1p1/partition" || path == "/test/sys/vdb1/partition" || path == "/test/sys/sdb1/partition" {
			return []byte("1\n"), nil
		}
		return nil, os.ErrNotExist
	}
	zfsProvisionEvalSymlinks = func(path string) (string, error) {
		switch path {
		case "/test/sys/nvme0n1p1":
			return "/sys/devices/pci/block/nvme0n1/nvme0n1p1", nil
		case "/test/sys/vdb1":
			return "/sys/devices/virtio/block/vdb/vdb1", nil
		case "/test/sys/sdb1":
			return "/sys/devices/scsi/block/sdb/sdb1", nil
		}
		return "", os.ErrNotExist
	}
	if got := zfsVdevWholeDisk("/dev/nvme0n1"); got != "/dev/nvme0n1" {
		t.Fatalf("whole NVMe = %q", got)
	}
	if got := zfsVdevWholeDisk("/dev/nvme0n1p1"); got != "/dev/nvme0n1" {
		t.Fatalf("NVMe partition = %q", got)
	}
	if got := zfsVdevWholeDisk("/dev/vdb1"); got != "/dev/vdb" {
		t.Fatalf("virtio partition = %q", got)
	}
	if got := zfsVdevWholeDisk("/dev/sdb1"); got != "/dev/sdb" {
		t.Fatalf("SATA partition = %q", got)
	}
}

func TestZFSProvisionResumesPartitionedNVMePoolWithoutWipe(t *testing.T) {
	oldRun, oldProvisionRun := nativeRun, zfsProvisionRun
	oldDiscoveryEval := zfsDiscoveryEvalSymlinks
	oldRoot, oldRead, oldEval := zfsProvisionSysBlockRoot, zfsProvisionReadFile, zfsProvisionEvalSymlinks
	t.Cleanup(func() {
		nativeRun, zfsProvisionRun = oldRun, oldProvisionRun
		zfsProvisionSysBlockRoot, zfsProvisionReadFile, zfsProvisionEvalSymlinks, zfsDiscoveryEvalSymlinks = oldRoot, oldRead, oldEval, oldDiscoveryEval
	})
	zfsDiscoveryEvalSymlinks = func(path string) (string, error) {
		if path == "/dev/disk/by-id/nvme-test" {
			return "/dev/nvme0n1", nil
		}
		return path, nil
	}
	zfsProvisionRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nativeRun(ctx, name, args...)
	}
	zfsProvisionSysBlockRoot = "/test/sys"
	zfsProvisionReadFile = func(path string) ([]byte, error) {
		if path == "/test/sys/nvme0n1p1/partition" {
			return []byte("1\n"), nil
		}
		return nil, os.ErrNotExist
	}
	zfsProvisionEvalSymlinks = func(path string) (string, error) {
		if path == "/test/sys/nvme0n1p1" {
			return "/sys/devices/pci/block/nvme0n1/nvme0n1p1", nil
		}
		return "", os.ErrNotExist
	}
	request := testZFSProvision(storageMountRoot)
	request.Devices[0].ByID = "/dev/disk/by-id/nvme-test"
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, request.OperationID+".json"), []byte(`{"request":{"operation_id":"zfs-op-1","repository_id":"repo-1","pool":"nex-repo-1","dataset":"vms","mount_path":"`+request.MountPath+`","layout":"single","devices":[{"by_id":"/dev/disk/by-id/nvme-test","serial":"disk-1","size_bytes":1073741824,"fingerprint":"`+testZFSFingerprint+`"}]},"result":{"operation_id":"zfs-op-1","status":"needs_attention"},"stage":"creating"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "zpool" && args[0] == "list" {
			if strings.Contains(strings.Join(args, " "), "health") {
				return []byte("ONLINE\n"), nil
			}
			return []byte("nex-repo-1\n"), nil
		}
		if name == "zpool" && args[0] == "status" {
			return []byte("\t/dev/nvme0n1p1 ONLINE\n"), nil
		}
		if name == "zpool" && args[0] == "get" {
			return []byte("pool-guid\n"), nil
		}
		if name == "zfs" && args[0] == "get" {
			if strings.Contains(strings.Join(args, " "), "nex:repository") {
				return []byte("repo-1\n"), nil
			}
			if strings.Contains(strings.Join(args, " "), "mountpoint") {
				return nil, errors.New("missing dataset")
			}
			return []byte("dataset-guid\n"), nil
		}
		return nil, nil
	}
	result, err := NewZFSProvisioner(state).provision(context.Background(), request)
	if err != nil || result.Status != "succeeded" || !provisionCallContains(calls, "zfs create") || provisionCallContains(calls, "wipefs") || provisionCallContains(calls, "zpool create") {
		t.Fatalf("partition recovery = %#v %v calls=%#v", result, err, calls)
	}
}

func provisionCallContains(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func TestZFSProvisionChangedDeviceNeedsAttentionWithoutErase(t *testing.T) {
	oldRun, oldRoot := nativeRun, storageMountRoot
	oldDiscover := zfsProvisionDiscover
	oldProvisionRun := zfsProvisionRun
	t.Cleanup(func() {
		nativeRun, storageMountRoot, zfsProvisionDiscover, zfsProvisionRun = oldRun, oldRoot, oldDiscover, oldProvisionRun
	})
	zfsProvisionRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nativeRun(ctx, name, args...)
	}
	storageMountRoot = filepath.Join(t.TempDir(), "storage")
	request := testZFSProvision(storageMountRoot)
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{{ByID: "/dev/disk/by-id/disk-1", Serial: "replacement", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint}}, nil
	}
	var wiped bool
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "zpool" && len(args) > 0 && args[0] == "list" {
			return []byte("\n"), nil
		}
		if name == "lsblk" {
			return []byte(`{"blockdevices":[{"path":"/dev/sdb","serial":"replacement","size":1073741824,"type":"disk"}]}`), nil
		}
		if name == "wipefs" {
			wiped = true
		}
		return nil, errors.New("unexpected command")
	}
	result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request)
	if err == nil || result.Status != "needs_attention" || wiped || !strings.Contains(result.Error, "changed") {
		t.Fatalf("changed device = %+v, %v, wiped=%v", result, err, wiped)
	}
}

// The worker refuses a selection without the exact fingerprint discovery
// observed, so every fixture carries one.
const testZFSFingerprint = "1111111111111111111111111111111111111111111111111111111111111111"

func testZFSProvision(root string) zfsProvisionIntent {
	return zfsProvisionIntent{OperationID: "zfs-op-1", RepositoryID: "repo-1", Pool: "nex-repo-1", Dataset: "vms", MountPath: filepath.Join(root, "repo-1"), Layout: "single", Devices: []zfsProvisionDevice{{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint}}}
}

// provisionStub answers the read-only queries a provision run makes, records
// every command so a test can prove exactly which disks were touched, and lets
// a test mark a disk as carrying old data or a single step as failing.
func provisionStub(calls *[][]string, dirty map[string]bool, fail map[string]error) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		if len(args) == 0 {
			return nil, nil
		}
		if err := fail[name+" "+args[0]]; err != nil {
			return nil, err
		}
		switch {
		case name == "wipefs" && args[0] == "--json":
			if dirty[args[len(args)-1]] {
				return []byte(`{"signatures":[{"offset":"0x438","type":"ext4"}]}`), nil
			}
			return []byte(`{"signatures":[]}`), nil
		case name == "zpool" && args[0] == "list" && slices.Contains(args, "health"):
			return []byte("ONLINE\n"), nil
		case name == "zpool" && args[0] == "list":
			return []byte("\n"), nil
		case name == "zpool" && args[0] == "get":
			return []byte("pool-guid\n"), nil
		case name == "zfs" && args[0] == "get":
			return []byte("dataset-guid\n"), nil
		}
		return nil, nil
	}
}

// provisionHarness points the worker at the recording stub and a scratch mount
// root, and returns the recorded calls.
func provisionHarness(t *testing.T, dirty map[string]bool, fail map[string]error) *[][]string {
	t.Helper()
	oldRun, oldRoot, oldDiscover, oldProvisionRun := nativeRun, storageMountRoot, zfsProvisionDiscover, zfsProvisionRun
	t.Cleanup(func() {
		nativeRun, storageMountRoot, zfsProvisionDiscover, zfsProvisionRun = oldRun, oldRoot, oldDiscover, oldProvisionRun
	})
	zfsProvisionRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nativeRun(ctx, name, args...)
	}
	storageMountRoot = filepath.Join(t.TempDir(), "storage")
	calls := &[][]string{}
	nativeRun = provisionStub(calls, dirty, fail)
	return calls
}

func TestZFSProvisionRefusesDataFoundAfterDiscoveryWithoutEraseConfirmation(t *testing.T) {
	// Discovery observed a blank disk; data appeared before the wipe. The
	// pre-wipe gate is the one that must still refuse it.
	calls := provisionHarness(t, map[string]bool{"/dev/disk/by-id/disk-1": true}, nil)
	request := testZFSProvision(storageMountRoot)
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint}}, nil
	}
	result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request)
	if err == nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "old data without erase confirmation") {
		t.Fatalf("unconfirmed old data = %+v, %v", result, err)
	}
	if provisionCall(*calls, "wipefs", "--all", "/dev/disk/by-id/disk-1") || provisionCommandCalled(*calls, "zpool") && provisionCall(*calls, "zpool", "create") {
		t.Fatalf("refused provision still touched the disk: %#v", *calls)
	}
}

func TestZFSProvisionWipesOnlySelectedDevicesAndNeverForcesPoolCreate(t *testing.T) {
	calls := provisionHarness(t, map[string]bool{"/dev/disk/by-id/disk-1": true, "/dev/disk/by-id/disk-2": true, "/dev/disk/by-id/disk-3": true}, nil)
	request := testZFSProvision(storageMountRoot)
	request.Layout = "mirror"
	request.Devices = []zfsProvisionDevice{
		{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, EraseConfirmed: true, Fingerprint: testZFSFingerprint},
		{ByID: "/dev/disk/by-id/disk-2", Serial: "disk-2", SizeBytes: 1073741824, EraseConfirmed: true, Fingerprint: testZFSFingerprint},
	}
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{
			{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, RequiresErase: true, Fingerprint: testZFSFingerprint},
			{ByID: "/dev/disk/by-id/disk-2", Serial: "disk-2", SizeBytes: 1073741824, Eligible: true, RequiresErase: true, Fingerprint: testZFSFingerprint},
			{ByID: "/dev/disk/by-id/disk-3", Serial: "disk-3", SizeBytes: 1073741824, Eligible: true, RequiresErase: true, Fingerprint: testZFSFingerprint},
		}, nil
	}
	result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("mirror provision = %+v, %v", result, err)
	}
	var wiped []string
	for _, call := range *calls {
		if call[0] == "wipefs" && len(call) == 3 && call[1] == "--all" {
			wiped = append(wiped, call[2])
		}
		if call[0] == "zpool" && len(call) > 1 && call[1] == "create" && slices.Contains(call, "-f") {
			t.Fatalf("zpool create forced over an unexpected disk state: %#v", call)
		}
	}
	if !slices.Equal(wiped, []string{"/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2"}) {
		t.Fatalf("wiped = %#v, calls = %#v", wiped, *calls)
	}
	if !provisionCall(*calls, "zpool", "create", "-o", "ashift=12", "-O", "nex:repository=repo-1", "-m", "none", "nex-repo-1", "mirror", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2") {
		t.Fatalf("mirror zpool create missing: %#v", *calls)
	}
}

// A hot spare is re-checked and wiped like a data disk: RequiresErase must reach wipefs, and an
// ineligible spare must refuse before any destructive call (SR-035).
func TestZFSProvisionWipesARequiresEraseSpare(t *testing.T) {
	calls := provisionHarness(t, map[string]bool{"/dev/disk/by-id/spare-1": true}, nil)
	request := testZFSProvision(storageMountRoot)
	request.Layout = "mirror"
	request.Devices = []zfsProvisionDevice{
		{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint},
		{ByID: "/dev/disk/by-id/disk-2", Serial: "disk-2", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint},
	}
	request.Spares = []zfsProvisionDevice{{ByID: "/dev/disk/by-id/spare-1", Serial: "spare-1", SizeBytes: 1073741824, EraseConfirmed: true, Fingerprint: testZFSFingerprint}}
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{
			{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint},
			{ByID: "/dev/disk/by-id/disk-2", Serial: "disk-2", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint},
			{ByID: "/dev/disk/by-id/spare-1", Serial: "spare-1", SizeBytes: 1073741824, Eligible: true, RequiresErase: true, Fingerprint: testZFSFingerprint},
		}, nil
	}
	result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("spare wipe provision = %+v, %v", result, err)
	}
	if !provisionCall(*calls, "wipefs", "--all", "/dev/disk/by-id/spare-1") {
		t.Fatalf("RequiresErase spare was not wiped: %#v", *calls)
	}
}

func TestZFSProvisionRefusesAnIneligibleSpareBeforeAnyWipe(t *testing.T) {
	calls := provisionHarness(t, nil, nil)
	request := testZFSProvision(storageMountRoot)
	request.Layout = "mirror"
	request.Devices = []zfsProvisionDevice{
		{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint},
		{ByID: "/dev/disk/by-id/disk-2", Serial: "disk-2", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint},
	}
	request.Spares = []zfsProvisionDevice{{ByID: "/dev/disk/by-id/spare-1", Serial: "spare-1", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint}}
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{
			{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint},
			{ByID: "/dev/disk/by-id/disk-2", Serial: "disk-2", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint},
			{ByID: "/dev/disk/by-id/spare-1", Serial: "spare-1", SizeBytes: 1073741824, Eligible: false, Reason: "mounted", Fingerprint: testZFSFingerprint},
		}, nil
	}
	result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request)
	if err == nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "changed or is in use") {
		t.Fatalf("ineligible spare = %+v, %v", result, err)
	}
	if provisionCommandCalled(*calls, "wipefs") || provisionCall(*calls, "zpool", "create") {
		t.Fatalf("ineligible spare still reached a destructive call: %#v", *calls)
	}
}

// autoreplace=on is what makes a hot spare engage; assert it and the spare vdev on create, and
// that a spare-free request does not carry the property (SR-036).
func TestZFSProvisionCreateEnablesAutoreplaceWhenSparesArePresent(t *testing.T) {
	calls := provisionHarness(t, nil, nil)
	request := testZFSProvision(storageMountRoot)
	request.Layout = "mirror"
	request.Devices = []zfsProvisionDevice{
		{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint},
		{ByID: "/dev/disk/by-id/disk-2", Serial: "disk-2", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint},
	}
	request.Spares = []zfsProvisionDevice{{ByID: "/dev/disk/by-id/spare-1", Serial: "spare-1", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint}}
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{
			{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint},
			{ByID: "/dev/disk/by-id/disk-2", Serial: "disk-2", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint},
			{ByID: "/dev/disk/by-id/spare-1", Serial: "spare-1", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint},
		}, nil
	}
	if result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request); err != nil || result.Status != "succeeded" {
		t.Fatalf("spare provision = %+v, %v", result, err)
	}
	if !provisionCall(*calls, "zpool", "create", "-o", "ashift=12", "-O", "nex:repository=repo-1", "-m", "none", "-o", "autoreplace=on", "nex-repo-1", "mirror", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2", "spare", "/dev/disk/by-id/spare-1") {
		t.Fatalf("zpool create missing autoreplace/spare: %#v", *calls)
	}
}

func TestZFSProvisionCreateOmitsAutoreplaceWithoutSpares(t *testing.T) {
	calls := provisionHarness(t, nil, nil)
	request := testZFSProvision(storageMountRoot)
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint}}, nil
	}
	if result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request); err != nil || result.Status != "succeeded" {
		t.Fatalf("spare-free provision = %+v, %v", result, err)
	}
	for _, call := range *calls {
		if call[0] == "zpool" && len(call) > 1 && call[1] == "create" && slices.Contains(call, "autoreplace=on") {
			t.Fatalf("spare-free zpool create carried autoreplace: %#v", call)
		}
	}
}

// recordZFSProvisionStages makes every durable stage observable in order, so a
// test can prove both halves of the wipe boundary: that "wipe-started" is on
// disk before the first destructive command, and that it is never written when
// no device was wiped.
func recordZFSProvisionStages(t *testing.T, calls *[][]string) *[]string {
	t.Helper()
	old := saveZFSProvision
	t.Cleanup(func() { saveZFSProvision = old })
	stages := &[]string{}
	saveZFSProvision = func(path string, j zfsProvisionJournal) error {
		*stages = append(*stages, j.Stage)
		*calls = append(*calls, []string{"journal", j.Stage})
		return old(path, j)
	}
	return stages
}

func TestZFSProvisionRecordsNoWipeWhenEveryDeviceIsBlank(t *testing.T) {
	// A crash after the wipe gate must not strand blank, untouched disks behind
	// a receipt claiming they were already wiped.
	calls := provisionHarness(t, nil, map[string]error{"zpool create": errors.New("interrupted")})
	stages := recordZFSProvisionStages(t, calls)
	request := testZFSProvision(storageMountRoot)
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint}}, nil
	}
	if result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request); err == nil || result.Status != "needs_attention" {
		t.Fatalf("failed create = %+v, %v", result, err)
	}
	if provisionCall(*calls, "wipefs", "--all", "/dev/disk/by-id/disk-1") {
		t.Fatalf("blank disk was wiped: %#v", *calls)
	}
	if slices.Contains(*stages, "wipe-started") {
		t.Fatalf("blank run recorded a wipe: %#v", *stages)
	}
}

func TestZFSProvisionJournalsWipeStartedBeforeTheFirstWipe(t *testing.T) {
	calls := provisionHarness(t, map[string]bool{"/dev/disk/by-id/disk-1": true}, nil)
	recordZFSProvisionStages(t, calls)
	request := testZFSProvision(storageMountRoot)
	request.Devices[0].EraseConfirmed = true
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, RequiresErase: true, Fingerprint: testZFSFingerprint}}, nil
	}
	if result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request); err != nil || result.Status != "succeeded" {
		t.Fatalf("confirmed wipe = %+v, %v", result, err)
	}
	marked, wiped := -1, -1
	for i, call := range *calls {
		if call[0] == "journal" && call[1] == "wipe-started" && marked < 0 {
			marked = i
		}
		if call[0] == "wipefs" && len(call) == 3 && call[1] == "--all" && wiped < 0 {
			wiped = i
		}
	}
	if marked < 0 || wiped < 0 || marked > wiped {
		t.Fatalf("wipe was not durable before it happened: %#v", *calls)
	}
}

func TestZFSProvisionAdoptRefusesTaggedPoolBuiltFromOtherDevices(t *testing.T) {
	// A tagged pool alone is not proof: the operation may only be reported as
	// succeeded when the pool is built from exactly the selected disks.
	oldRun, oldRoot, oldDiscover, oldProvisionRun := nativeRun, storageMountRoot, zfsProvisionDiscover, zfsProvisionRun
	t.Cleanup(func() {
		nativeRun, storageMountRoot, zfsProvisionDiscover, zfsProvisionRun = oldRun, oldRoot, oldDiscover, oldProvisionRun
	})
	zfsProvisionRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nativeRun(ctx, name, args...)
	}
	storageMountRoot = filepath.Join(t.TempDir(), "storage")
	request := testZFSProvision(storageMountRoot)
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return nil, errors.New("must not rediscover an adopted pool")
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "zpool" && args[0] == "list" && strings.Contains(strings.Join(args, " "), "health"):
			return []byte("ONLINE\n"), nil
		case name == "zpool" && args[0] == "list":
			return []byte("nex-repo-1\n"), nil
		case name == "zpool" && args[0] == "status":
			return []byte("\t/dev/disk/by-id/someone-elses-disk ONLINE\n"), nil
		case name == "zpool" && args[0] == "get":
			return []byte("pool-guid\n"), nil
		case name == "zfs" && args[0] == "get":
			if strings.Contains(strings.Join(args, " "), "nex:repository") {
				return []byte("repo-1\n"), nil
			}
			if strings.Contains(strings.Join(args, " "), " mounted ") {
				return []byte("yes\n"), nil
			}
			return []byte(request.MountPath + "\n"), nil
		}
		return nil, nil
	}
	result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request)
	if err == nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "do not match the selected devices") {
		t.Fatalf("adopted foreign-vdev pool = %+v, %v", result, err)
	}
}

func TestValidateZFSProvisionRequiresDeviceFingerprint(t *testing.T) {
	request := testZFSProvision(t.TempDir())
	request.MountPath = filepath.Join(storageMountRoot, request.RepositoryID)
	if err := validateZFSProvision(request); err != nil {
		t.Fatalf("valid request = %v", err)
	}
	for _, fingerprint := range []string{"", "not-hex", strings.Repeat("a", 63), strings.Repeat("A", 64)} {
		bad := request
		bad.Devices = []zfsProvisionDevice{{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Fingerprint: fingerprint}}
		if err := validateZFSProvision(bad); err == nil {
			t.Fatalf("accepted device fingerprint %q", fingerprint)
		}
	}
	empty := request
	empty.Layout = ""
	if err := validateZFSProvision(empty); err == nil {
		t.Fatal("accepted an unresolved layout")
	}
}

func TestZFSProvisionResumeAfterWipeNeedsAttention(t *testing.T) {
	state := t.TempDir()
	calls := provisionHarness(t, nil, nil)
	request := testZFSProvision(storageMountRoot)
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		t.Fatal("interrupted wipe must not be re-driven")
		return nil, nil
	}
	journal := zfsProvisionJournal{Request: request, Result: zfsProvisionResult{OperationID: request.OperationID, Status: "pending"}, Stage: "wipe-started"}
	if err := saveZFSProvision(filepath.Join(state, request.OperationID+".json"), journal); err != nil {
		t.Fatal(err)
	}
	result, err := NewZFSProvisioner(state).provision(context.Background(), request)
	if err == nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "interrupted after selected devices were wiped") {
		t.Fatalf("interrupted wipe = %+v, %v", result, err)
	}
	if provisionCommandCalled(*calls, "wipefs") {
		t.Fatalf("interrupted wipe was repeated: %#v", *calls)
	}
}

func TestZFSProvisionRefusesPoolNameHeldByAnotherRepository(t *testing.T) {
	oldRun, oldRoot, oldDiscover, oldProvisionRun := nativeRun, storageMountRoot, zfsProvisionDiscover, zfsProvisionRun
	t.Cleanup(func() {
		nativeRun, storageMountRoot, zfsProvisionDiscover, zfsProvisionRun = oldRun, oldRoot, oldDiscover, oldProvisionRun
	})
	zfsProvisionRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nativeRun(ctx, name, args...)
	}
	storageMountRoot = filepath.Join(t.TempDir(), "storage")
	request := testZFSProvision(storageMountRoot)
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		t.Fatal("a foreign pool must be refused before any device is considered")
		return nil, nil
	}
	var calls [][]string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "zpool" && args[0] == "list" {
			return []byte("nex-repo-1\n"), nil
		}
		if name == "zfs" && args[0] == "get" {
			return []byte("other-repo\n"), nil
		}
		return nil, nil
	}
	result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request)
	if err == nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "already in use") {
		t.Fatalf("foreign tagged pool = %+v, %v", result, err)
	}
	if provisionCommandCalled(calls, "wipefs") {
		t.Fatalf("foreign tagged pool was wiped: %#v", calls)
	}
}

func TestCheckZFSProvisionDevicesRefusesIneligibleOrRefingerprintedDevice(t *testing.T) {
	old := zfsProvisionDiscover
	t.Cleanup(func() { zfsProvisionDiscover = old })
	selected := []zfsProvisionDevice{{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint}}
	observed := ZFSDeviceCandidate{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint}
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) { return []ZFSDeviceCandidate{observed}, nil }
	if err := checkZFSProvisionDevices(context.Background(), selected); err != nil {
		t.Fatalf("unchanged device = %v", err)
	}
	ineligible := observed
	ineligible.Eligible, ineligible.Reason = false, "mounted"
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) { return []ZFSDeviceCandidate{ineligible}, nil }
	if err := checkZFSProvisionDevices(context.Background(), selected); err == nil {
		t.Fatal("accepted a device that is no longer eligible")
	}
	// The serial and size are unchanged: only the fingerprint records that the
	// disk's partitions or signatures moved under the operator's selection.
	refingerprinted := observed
	refingerprinted.Fingerprint = "2222222222222222222222222222222222222222222222222222222222222222"
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) { return []ZFSDeviceCandidate{refingerprinted}, nil }
	if err := checkZFSProvisionDevices(context.Background(), selected); err == nil {
		t.Fatal("accepted a device whose observed state changed")
	}
}

func TestZFSProvisionRefusesRefingerprintedDeviceBeforeWiping(t *testing.T) {
	calls := provisionHarness(t, map[string]bool{"/dev/disk/by-id/disk-1": true}, nil)
	request := testZFSProvision(storageMountRoot)
	request.Devices[0].EraseConfirmed = true
	zfsProvisionDiscover = func(context.Context) ([]ZFSDeviceCandidate, error) {
		return []ZFSDeviceCandidate{{ByID: "/dev/disk/by-id/disk-1", Serial: "disk-1", SizeBytes: 1073741824, Eligible: true, RequiresErase: true, Fingerprint: "3333333333333333333333333333333333333333333333333333333333333333"}}, nil
	}
	result, err := NewZFSProvisioner(t.TempDir()).provision(context.Background(), request)
	if err == nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "changed") {
		t.Fatalf("refingerprinted device = %+v, %v", result, err)
	}
	if provisionCommandCalled(*calls, "wipefs") {
		t.Fatalf("refingerprinted device was wiped: %#v", *calls)
	}
}

func provisionCall(calls [][]string, want ...string) bool {
	for _, call := range calls {
		if strings.Join(call, "\x00") == strings.Join(want, "\x00") {
			return true
		}
	}
	return false
}

func provisionCommandCalled(calls [][]string, name string) bool {
	for _, call := range calls {
		if len(call) > 0 && call[0] == name {
			return true
		}
	}
	return false
}
