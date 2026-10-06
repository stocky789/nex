// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestISCSIFilesystemRequiresUniqueSupportedFilesystem(t *testing.T) {
	raw := []byte(`{"blockdevices":[{"path":"/dev/sdb","type":"disk","wwn":"3600508b400105e210000900000490000","hctl":"7:0:0:4","children":[{"path":"/dev/sdb1","type":"part","uuid":"01234567-89ab-cdef-0123-456789abcdef","fstype":"ext4"}]}]}`)
	gotWWID, gotFS, err := iscsiFilesystemForLUN(raw, "01234567-89ab-cdef-0123-456789abcdef", 4)
	if err != nil || !reflect.DeepEqual([]string{gotWWID, gotFS}, []string{"3600508b400105e210000900000490000", "ext4"}) {
		t.Fatalf("iscsiFilesystem() = %q, %q, %v", gotWWID, gotFS, err)
	}
	if _, _, err = iscsiFilesystem([]byte(`{"blockdevices":[{"uuid":"01234567-89ab-cdef-0123-456789abcdef","wwn":"abc","fstype":"btrfs"}]}`), "01234567-89ab-cdef-0123-456789abcdef"); err == nil {
		t.Fatal("accepted unsupported filesystem")
	}
	if _, _, err = iscsiFilesystemForLUN(raw, "01234567-89ab-cdef-0123-456789abcdef", 5); err == nil {
		t.Fatal("accepted another LUN")
	}
}

func TestPrepareISCSIMountUnitUsesStableByIDDevice(t *testing.T) {
	oldRun, oldUnit := nativeRun, storageUnitRoot
	t.Cleanup(func() { nativeRun, storageUnitRoot = oldRun, oldUnit })
	storageUnitRoot = t.TempDir()
	var calls [][]string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}
	s := protocol.StorageRegistration{ID: "repo", Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{MountPath: "/var/lib/nex-storage/repo"}}}
	if _, err := prepareISCSIMountUnit(context.Background(), s, "/dev/disk/by-id/scsi-3600508b400105e210000900000490000", "xfs"); err != nil {
		t.Fatal(err)
	}
	unitName, path := iscsiUnitPath(s)
	unit, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(unit), "What=/dev/disk/by-id/scsi-3600508b400105e210000900000490000") || !strings.Contains(string(unit), "open-iscsi.service") {
		t.Fatalf("unexpected iSCSI unit %q: %v", unit, err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", unitName}}) {
		t.Fatalf("commands = %#v", calls)
	}
	if filepath.Dir(path) != storageUnitRoot {
		t.Fatal("unit escaped configured unit root")
	}
}

func iscsiNoSessionTestError(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 21").Run()
	if err == nil {
		t.Fatal("expected exit 21")
	}
	return err
}

func TestPrepareISCSIRejectsMismatchedIdentityBeforeCommands(t *testing.T) {
	old := nativeRun
	defer func() { nativeRun = old }()
	called := false
	nativeRun = func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	s := protocol.StorageRegistration{Kind: "iscsi", Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "storage.example", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", MountPath: "/var/lib/nex-storage/repo", ExclusiveTarget: true}}, Identity: protocol.StorageIdentity{ISCSI: &protocol.ISCSIStorageIdentity{TargetIQN: "iqn.elsewhere:vm", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", WWID: "3600508b400105e210000900000490000"}}}
	if _, err := prepareISCSI(context.Background(), s); err == nil || called {
		t.Fatalf("prepareISCSI() err=%v called=%v; want validation failure before commands", err, called)
	}
}

func TestInspectISCSIRejectsClonedUUIDMountedFromAnotherLUN(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	const uuid = "01234567-89ab-cdef-0123-456789abcdef"
	s := protocol.StorageRegistration{
		ID:   "repo",
		Kind: protocol.StorageKindISCSI,
		Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{
			Portal: "storage.example", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4,
			FilesystemUUID: uuid, MountPath: filepath.Join(storageMountRoot, "repo"), ExclusiveTarget: true,
		}},
		Identity: protocol.StorageIdentity{ISCSI: &protocol.ISCSIStorageIdentity{
			TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4, FilesystemUUID: uuid,
		}},
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "lsblk":
			return []byte(`{"blockdevices":[{"path":"/dev/sdb","type":"disk","wwn":"3600508b400105e210000900000490000","hctl":"7:0:0:4","children":[{"path":"/dev/sdb1","type":"part","uuid":"01234567-89ab-cdef-0123-456789abcdef","fstype":"ext4"}]},{"path":"/dev/sdc","type":"disk","wwn":"3600508b400105e210000900000490001","hctl":"7:0:0:5","children":[{"path":"/dev/sdc1","type":"part","uuid":"01234567-89ab-cdef-0123-456789abcdef","fstype":"ext4"}]}]}`), nil
		case "findmnt":
			return []byte("/dev/sdc1 " + uuid + " ext4 rw " + s.Source.ISCSI.MountPath + "\n"), nil
		default:
			t.Fatalf("unexpected command %q %q", name, args)
			return nil, nil
		}
	}
	if _, err := inspectISCSI(context.Background(), s); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("inspectISCSI() = %v, want wrong source rejected", err)
	}
}

func TestInspectISCSIObservesClusteredGFS2Identity(t *testing.T) {
	oldRun, oldClusterRun, oldAccess := nativeRun, clusterRun, inspectISCSIPathAccess
	t.Cleanup(func() { nativeRun, clusterRun, inspectISCSIPathAccess = oldRun, oldClusterRun, oldAccess })
	const uuid = "01234567-89ab-cdef-0123-456789abcdef"
	path := "/var/lib/nex-storage/repo"
	s := protocol.StorageRegistration{ID: "repo", Kind: protocol.StorageKindISCSI, Path: path, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "storage.example", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4, FilesystemUUID: uuid, MountPath: path, Mode: "clustered_gfs2", ClusterID: "cluster", LockTable: "cluster:shared", EligibleHostIDs: []string{"host-1", "host-2", "host-3"}}}, Identity: protocol.StorageIdentity{ISCSI: &protocol.ISCSIStorageIdentity{TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4, FilesystemUUID: uuid, FilesystemType: "gfs2", GFS2LockTable: "cluster:shared", GFS2Journals: 3}}}
	if _, err := validISCSI(s); err != nil {
		t.Fatalf("test registration invalid: %v, path=%q expected=%q", err, path, remoteMountPath(s))
	}
	inspectISCSIPathAccess = func(string, string, bool) (storageAccess, error) {
		return storageAccess{Path: path, Health: "healthy"}, nil
	}
	clusterRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		switch name {
		case "tunegfs2":
			return []byte("Locking protocol: lock_dlm\nLock table: cluster:shared\n"), nil
		case "gfs2_edit":
			return []byte("journal0: a\njournal1: b\njournal2: c\njournal3: d\njournal4: e\n"), nil
		default:
			t.Fatalf("unexpected cluster command %q", name)
			return nil, nil
		}
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "lsblk":
			return []byte(`{"blockdevices":[{"path":"/dev/sdb","type":"disk","wwn":"3600508b400105e210000900000490000","hctl":"7:0:0:4","children":[{"path":"/dev/sdb1","type":"part","uuid":"01234567-89ab-cdef-0123-456789abcdef","fstype":"gfs2"}]}]}`), nil
		case "findmnt":
			return []byte("/dev/sdb1 " + uuid + " gfs2 rw " + path + "\n"), nil
		default:
			t.Fatalf("unexpected command %q %q", name, args)
			return nil, nil
		}
	}
	got, err := inspectISCSI(context.Background(), s)
	if err != nil || got.Observed.ISCSI == nil || got.Observed.ISCSI.FilesystemType != "gfs2" || got.Observed.ISCSI.GFS2LockTable != "cluster:shared" || got.Observed.ISCSI.GFS2Journals != 5 {
		t.Fatalf("clustered GFS2 observation = %#v, %v", got.Observed, err)
	}
	clusterRun = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Locking protocol: lock_dlm\nLock table: another:table\n"), nil
	}
	if _, err = inspectISCSI(context.Background(), s); err == nil || !strings.Contains(err.Error(), "GFS2 lock") {
		t.Fatalf("mismatched GFS2 metadata admitted: %v", err)
	}
}

func TestDetachISCSIRefusesClusteredProfileBeforeNativeMutation(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("clustered detach issued a native command")
		return nil, nil
	}
	s := protocol.StorageRegistration{Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2"}}}
	if err := detachISCSI(context.Background(), s); err == nil || !strings.Contains(err.Error(), "coordinated") {
		t.Fatalf("clustered detach = %v", err)
	}
}

func TestPrepareISCSIRollsBackFailedDiscovery(t *testing.T) {
	oldRun, oldMount, oldNodeRoot := nativeRun, storageMountRoot, iscsiNodeRoot
	t.Cleanup(func() { nativeRun, storageMountRoot, iscsiNodeRoot = oldRun, oldMount, oldNodeRoot })
	storageMountRoot = t.TempDir()
	iscsiNodeRoot = t.TempDir()
	var calls [][]string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "iscsiadm" && len(args) > 1 && args[1] == "session" {
			return nil, iscsiNoSessionTestError(t)
		}
		if name == "iscsiadm" && len(args) > 7 && args[6] == "--op" && args[7] == "new" {
			v := &protocol.ISCSIStorageSource{Portal: "storage.example", TargetIQN: "iqn.2026-01.example:vm"}
			path := iscsiNodePath(v)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(path, []byte("node.startup = manual\n"), 0600)
		}
		if name == "lsblk" {
			return []byte(`{"blockdevices":[]}`), nil
		}
		return nil, nil
	}
	s := protocol.StorageRegistration{ID: "repo", Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "storage.example", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", MountPath: filepath.Join(storageMountRoot, "repo"), ExclusiveTarget: true}}}
	if _, err := prepareISCSI(context.Background(), s); err == nil {
		t.Fatal("accepted missing LUN")
	}
	lines := make([]string, len(calls))
	for i, call := range calls {
		lines[i] = strings.Join(call, " ")
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"node.startup -v manual", "--logout", "--op delete"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rollback omitted %q: %s", want, joined)
		}
	}
}

func TestISCSIChapCredentialsStayOutOfCommands(t *testing.T) {
	oldRun, oldRoot, oldCredentials, oldOwner := nativeRun, iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot
	t.Cleanup(func() {
		nativeRun, iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot = oldRun, oldRoot, oldCredentials, oldOwner
	})
	root := t.TempDir()
	iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot = filepath.Join(root, "nodes"), filepath.Join(root, "credentials"), filepath.Join(root, "owners")
	if err := os.MkdirAll(storageCredentialRoot, 0700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(storageCredentialRoot, "receipt-1")
	if err := os.WriteFile(credential, []byte("username=chap-user\npassword=not-in-args\n"), 0600); err != nil {
		t.Fatal(err)
	}
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm"}
	var calls [][]string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "iscsiadm" && len(args) > 1 && args[1] == "session" {
			return nil, iscsiNoSessionTestError(t)
		}
		if len(args) > 1 && args[1] == "session" {
			return nil, iscsiNoSessionTestError(t)
		}
		if len(args) == 8 && args[6] == "--op" && args[7] == "new" {
			path := iscsiNodePath(v)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(path, []byte("node.startup = manual\n"), 0600)
		}
		return nil, nil
	}
	s := protocol.StorageRegistration{ID: "repo", CredentialReceipt: "receipt-1", CredentialPresent: true}
	created, err := configureISCSICHAP(context.Background(), s, v)
	if err != nil || !created.nodeCreated {
		t.Fatalf("configureISCSICHAP() = %v, %v", created, err)
	}
	config, err := os.ReadFile(iscsiNodePath(v))
	if err != nil || !strings.Contains(string(config), "node.session.auth.password = not-in-args") {
		t.Fatalf("CHAP node config = %q, %v", config, err)
	}
	for _, call := range calls {
		if strings.Contains(strings.Join(call, " "), "not-in-args") {
			t.Fatalf("CHAP secret leaked to command: %#v", call)
		}
	}
}

func TestISCSIChapCredentialFileRequiresExactFields(t *testing.T) {
	for _, raw := range []string{"username=user\npassword=secret\n", "username=user\npassword=with=equals\n"} {
		if _, _, err := iscsiCHAPFields(raw); err != nil {
			t.Fatalf("iscsiCHAPFields(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"username=user\n", "username=user\npassword=secret\nextra=x\n", "username=user\nusername=again\npassword=secret\n"} {
		if _, _, err := iscsiCHAPFields(raw); err == nil {
			t.Fatalf("iscsiCHAPFields(%q) accepted invalid credential", raw)
		}
	}
}

func TestISCSIRefusesUnmanagedNodeAndSession(t *testing.T) {
	oldRun, oldRoot, oldOwner := nativeRun, iscsiNodeRoot, iscsiOwnerRoot
	t.Cleanup(func() { nativeRun, iscsiNodeRoot, iscsiOwnerRoot = oldRun, oldRoot, oldOwner })
	iscsiNodeRoot, iscsiOwnerRoot = t.TempDir(), t.TempDir()
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm"}
	path := iscsiNodePath(v)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("node.startup = manual\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unmanaged node ran a command")
		return nil, nil
	}
	if _, err := configureISCSICHAP(context.Background(), protocol.StorageRegistration{}, v); err == nil {
		t.Fatal("accepted unmanaged node")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "iscsiadm" && len(args) > 1 && args[1] == "session" {
			return []byte("Target: " + v.TargetIQN + "\nCurrent Portal: storage.example:3260,1\n"), nil
		}
		return nil, nil
	}
	if _, err := configureISCSICHAP(context.Background(), protocol.StorageRegistration{}, v); !errors.Is(err, ErrISCSIUnmanagedSession) {
		t.Fatalf("configureISCSICHAP() error = %v, want unmanaged session", err)
	}
}

func TestISCSIChapRotationPreservesExistingCredentials(t *testing.T) {
	oldRun, oldRoot, oldCredentials, oldOwner := nativeRun, iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot
	t.Cleanup(func() {
		nativeRun, iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot = oldRun, oldRoot, oldCredentials, oldOwner
	})
	root := t.TempDir()
	iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot = filepath.Join(root, "nodes"), filepath.Join(root, "credentials"), filepath.Join(root, "owners")
	if err := os.MkdirAll(storageCredentialRoot, 0700); err != nil {
		t.Fatal(err)
	}
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm"}
	path := iscsiNodePath(v)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	old := nexISCSINodeConfig([]byte("node.startup = manual\n"), "old-user", "old-secret")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageCredentialRoot, "receipt"), []byte("username=new-user\npassword=new-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "iscsiadm" && len(args) > 1 && args[1] == "session" {
			return []byte("Target: " + v.TargetIQN + "\nCurrent Portal: storage.example:3260,1\n"), nil
		}
		return nil, nil
	}
	s := protocol.StorageRegistration{CredentialReceipt: "receipt", CredentialPresent: true}
	if _, err := configureISCSICHAP(context.Background(), s, v); !errors.Is(err, ErrISCSICredentialRotationReconnect) {
		t.Fatalf("configureISCSICHAP() error = %v, want safe reconnect", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(old) {
		t.Fatalf("active rotation changed credentials: %q, %v", got, err)
	}
	nativeRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 1 && args[1] == "session" {
			return nil, iscsiNoSessionTestError(t)
		}
		return nil, errors.New("login rejected")
	}
	if _, err := prepareISCSI(context.Background(), protocol.StorageRegistration{ID: "repo", Kind: protocol.StorageKindISCSI, CredentialReceipt: "receipt", CredentialPresent: true, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: v.Portal, TargetIQN: v.TargetIQN, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", MountPath: remoteMountPath(protocol.StorageRegistration{ID: "repo"}), ExclusiveTarget: true}}}); err == nil {
		t.Fatal("accepted failed login")
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != string(old) {
		t.Fatalf("failed rotation did not restore credentials: %q, %v", got, err)
	}
}

func TestNexISCSINodeConfigIsIdempotentForUnchangedCredentials(t *testing.T) {
	first := nexISCSINodeConfig([]byte("node.startup = manual\n"), "user", "secret")
	second := nexISCSINodeConfig(first, "user", "secret")
	if string(second) != string(first) {
		t.Fatalf("unchanged owned iSCSI configuration drifted on replay:\nfirst=%q\nsecond=%q", first, second)
	}
}

func TestConfigureISCSICHAPActiveSidecarOwnedMatchingAuthIsNoop(t *testing.T) {
	oldRun, oldRoot, oldCredentials, oldOwner := nativeRun, iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot
	t.Cleanup(func() {
		nativeRun, iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot = oldRun, oldRoot, oldCredentials, oldOwner
	})
	root := t.TempDir()
	iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot = filepath.Join(root, "nodes"), filepath.Join(root, "credentials"), filepath.Join(root, "owners")
	if err := os.MkdirAll(storageCredentialRoot, 0700); err != nil {
		t.Fatal(err)
	}
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef"}
	path := iscsiNodePath(v)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	markerless := []byte("node.startup = manual\nnode.session.auth.authmethod = CHAP\nnode.session.auth.username = user\nnode.session.auth.password = secret\n")
	if err := os.WriteFile(path, markerless, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), storageOwnerRootKey{}, iscsiOwnerRoot)
	if err := writeNexISCSIOwner(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageCredentialRoot, "receipt"), []byte("username=user\npassword=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "iscsiadm" && len(args) > 1 && args[1] == "session" {
			return []byte("Target: " + v.TargetIQN + "\nCurrent Portal: storage.example:3260,1\n"), nil
		}
		return nil, nil
	}
	if change, err := configureISCSICHAP(ctx, protocol.StorageRegistration{CredentialReceipt: "receipt", CredentialPresent: true}, v); err != nil || change.reconnect || change.path != "" {
		t.Fatalf("matching sidecar-owned active auth was not a no-op: change=%+v err=%v", change, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(markerless) {
		t.Fatalf("sidecar-owned node was rewritten: %q, %v", got, err)
	}
}

func TestConfigureISCSICHAPActiveNoAuthAbsentOrNoneIsNoop(t *testing.T) {
	oldRun, oldRoot, oldCredentials, oldOwner := nativeRun, iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot
	t.Cleanup(func() {
		nativeRun, iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot = oldRun, oldRoot, oldCredentials, oldOwner
	})
	root := t.TempDir()
	iscsiNodeRoot, storageCredentialRoot, iscsiOwnerRoot = filepath.Join(root, "nodes"), filepath.Join(root, "credentials"), filepath.Join(root, "owners")
	for _, tc := range []struct{ name, auth string }{{"absent", ""}, {"none", "node.session.auth.authmethod = None\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:" + tc.name, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef"}
			path := iscsiNodePath(v)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte("node.startup = manual\n" + tc.auth)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), storageOwnerRootKey{}, iscsiOwnerRoot)
			if err := writeNexISCSIOwner(ctx, v); err != nil {
				t.Fatal(err)
			}
			nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "iscsiadm" && len(args) > 1 && args[1] == "session" {
					return []byte("Target: " + v.TargetIQN + "\nCurrent Portal: storage.example:3260,1\n"), nil
				}
				return nil, nil
			}
			if change, err := configureISCSICHAP(ctx, protocol.StorageRegistration{}, v); err != nil || change.reconnect || change.path != "" {
				t.Fatalf("active no-auth %s was not a no-op: change=%+v err=%v", tc.name, change, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(original) {
				t.Fatalf("active no-auth %s was rewritten: %q, %v", tc.name, got, err)
			}
		})
	}
}

func TestPrepareISCSIReportsUncertainCredentialRollback(t *testing.T) {
	oldRun, oldMount, oldNodeRoot, oldCredentials, oldUnits := nativeRun, storageMountRoot, iscsiNodeRoot, storageCredentialRoot, storageUnitRoot
	t.Cleanup(func() {
		nativeRun, storageMountRoot, iscsiNodeRoot, storageCredentialRoot, storageUnitRoot = oldRun, oldMount, oldNodeRoot, oldCredentials, oldUnits
	})
	root := t.TempDir()
	storageMountRoot, iscsiNodeRoot = filepath.Join(root, "mounts"), filepath.Join(root, "nodes")
	storageCredentialRoot, storageUnitRoot = filepath.Join(root, "credentials"), filepath.Join(root, "units")
	if err := os.MkdirAll(storageCredentialRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageCredentialRoot, "new"), []byte("username=new-user\npassword=new-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4, FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", MountPath: filepath.Join(storageMountRoot, "repo"), ExclusiveTarget: true}
	if err := os.MkdirAll(filepath.Dir(iscsiNodePath(v)), 0700); err != nil {
		t.Fatal(err)
	}
	oldConfig := nexISCSINodeConfig([]byte("node.startup = manual\n"), "old-user", "old-secret")
	if err := os.WriteFile(iscsiNodePath(v), oldConfig, 0600); err != nil {
		t.Fatal(err)
	}
	var logins, mounts int
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "findmnt" {
			return []byte("/dev/sdb1 " + v.MountPath + " ext4\n"), nil
		}
		if name == "lsblk" {
			return []byte(`{"blockdevices":[{"path":"/dev/sdb","type":"disk","wwn":"3600508b400105e210000900000490000","hctl":"7:0:0:4","children":[{"path":"/dev/sdb1","type":"part","uuid":"01234567-89ab-cdef-0123-456789abcdef","fstype":"ext4"}]}]}`), nil
		}
		if name == "iscsiadm" && len(args) > 0 && args[len(args)-1] == "--login" {
			logins++
			if logins == 2 {
				return nil, errors.New("old session rejected")
			}
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "start" {
			mounts++
			if mounts == 1 {
				return nil, errors.New("new mount rejected")
			}
		}
		return nil, nil
	}
	s := protocol.StorageRegistration{ID: "repo", Kind: protocol.StorageKindISCSI, CredentialReceipt: "new", CredentialPresent: true, Source: protocol.StorageSource{ISCSI: v}}
	previous := s
	previous.CredentialReceipt = "old"
	ctx := context.WithValue(allowStorageRemount(context.Background()), storagePreviousKey{}, previous)
	if _, err := prepareISCSI(ctx, s); !errors.Is(err, ErrISCSIRollbackUncertain) || !strings.Contains(err.Error(), "old session rejected") {
		t.Fatalf("prepareISCSI() = %v, want visible failed rollback", err)
	}
	got, err := os.ReadFile(iscsiNodePath(v))
	if err != nil || string(got) != string(oldConfig) {
		t.Fatalf("rollback node config = %q, %v", got, err)
	}
	if logins != 2 || mounts != 2 {
		t.Fatalf("recovery calls: logins=%d mounts=%d, want 2 each", logins, mounts)
	}
}

func TestNexISCSIOwnerSurvivesNodeRewrite(t *testing.T) {
	oldRoot := iscsiNodeRoot
	t.Cleanup(func() { iscsiNodeRoot = oldRoot })
	iscsiNodeRoot = t.TempDir()
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4, FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef"}
	if err := os.MkdirAll(filepath.Dir(iscsiNodePath(v)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iscsiNodePath(v), []byte("node.startup = automatic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeNexISCSIOwner(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if !nexISCSINode(context.Background(), v) {
		t.Fatal("sidecar ownership was not recognized after node rewrite")
	}
}

func TestISCSIConfigureAcceptsScopedOwnerAfterNodeRewrite(t *testing.T) {
	oldRun, oldRoot, oldOwner := nativeRun, iscsiNodeRoot, iscsiOwnerRoot
	t.Cleanup(func() { nativeRun, iscsiNodeRoot, iscsiOwnerRoot = oldRun, oldRoot, oldOwner })
	root := t.TempDir()
	iscsiNodeRoot, iscsiOwnerRoot = filepath.Join(root, "nodes"), filepath.Join(root, "owners")
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4, FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef"}
	if err := os.MkdirAll(filepath.Dir(iscsiNodePath(v)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iscsiNodePath(v), []byte("node.startup = automatic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeNexISCSIOwner(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "iscsiadm" && len(args) > 1 && args[1] == "session" {
			return []byte("Target: " + v.TargetIQN + " (non-flash)\nCurrent Portal: storage.example:3260,1\n"), nil
		}
		return nil, nil
	}
	if _, err := configureISCSICHAP(allowStorageRemount(context.Background()), protocol.StorageRegistration{}, v); err != nil {
		t.Fatalf("scoped owner did not authorize rewritten node: %v", err)
	}
}

func TestISCSISessionListedMatchesOnlyExactTargetAndPortal(t *testing.T) {
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm"}
	out := []byte("Target: iqn.2026-01.example:other (non-flash)\nCurrent Portal: storage.example:3260,9\n\nTarget: iqn.2026-01.example:vm (non-flash)\nCurrent Portal: storage.example:3260,9\n")
	if !iscsiSessionListed(out, v) {
		t.Fatal("exact session was not recognized")
	}
	if iscsiSessionListed([]byte("Target: iqn.2026-01.example:vm (flash)\nCurrent Portal: other.example:3260,1\n"), v) {
		t.Fatal("different portal was accepted as an active session")
	}
	actual := []byte("Target: iqn.2026-09.test.nex:lab (non-flash)\n  Current Portal: 127.0.0.1:3260,1\n")
	if !iscsiSessionListed(actual, &protocol.ISCSIStorageSource{TargetIQN: "iqn.2026-09.test.nex:lab", Portal: "127.0.0.1:3260"}) {
		t.Fatal("actual Debian host:port,tpgt session was not recognized")
	}
}

func TestISCSISessionPortalStrictGrammar(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"127.0.0.1:3260,1", "127.0.0.1:3260"},
		{"host:1,0", "host:1"},
		{"host:65535,65535", "host:65535"},
		{"[2001:db8::1]:3260,1", "[2001:db8::1]:3260"},
		{"host:3260,,1", ""},
		{"host:3260,nope", ""},
		{"host:0,1", ""},
		{"host:65536,1", ""},
		{"host:nope,1", ""},
	} {
		if got := iscsiSessionPortal(tc.raw); got != tc.want {
			t.Errorf("iscsiSessionPortal(%q)=%q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestISCSIMountFieldsAcceptExactDuplicateRows(t *testing.T) {
	row := "/dev/sdb1 01234567-89ab-cdef-0123-456789abcdef ext4 rw /var/lib/nex-storage/repo\n"
	fields, err := iscsiMountFields([]byte(row + row))
	if err != nil || len(fields) != 5 || fields[0] != "/dev/sdb1" {
		t.Fatalf("duplicate iSCSI rows = %#v, %v", fields, err)
	}
	if _, err := iscsiMountFields([]byte(row + "/dev/sdc1 01234567-89ab-cdef-0123-456789abcdef ext4 rw /var/lib/nex-storage/repo\n")); err == nil {
		t.Fatal("accepted divergent iSCSI mount rows")
	}
}

func TestISCSISessionExistsErrorRequiresExit15(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 15").Run()
	if !iscsiSessionExistsError(err) {
		t.Fatalf("exit 15 was not recognized: %v", err)
	}
	err = exec.Command("sh", "-c", "exit 1").Run()
	if iscsiSessionExistsError(err) {
		t.Fatalf("non-15 exit was accepted: %v", err)
	}
	if iscsiSessionExistsError(errors.New("iscsiadm failed: exit status 15")) {
		t.Fatal("untyped error text was accepted as a session-exists result")
	}
}

func TestISCSINoActiveSessionsErrorRequiresTypedExit21(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 21").Run()
	if !iscsiNoActiveSessionsError(err) {
		t.Fatalf("exit 21 not recognized: %v", err)
	}
	err = exec.Command("sh", "-c", "exit 1").Run()
	if iscsiNoActiveSessionsError(err) {
		t.Fatalf("non-21 accepted: %v", err)
	}
	if iscsiNoActiveSessionsError(errors.New("No active sessions")) {
		t.Fatal("untyped text accepted")
	}
}

func TestDetachISCSICleansOwnerAfterNodeDeleteCrash(t *testing.T) {
	oldRun, oldMount, oldNode, oldOwner, oldUnits := nativeRun, storageMountRoot, iscsiNodeRoot, iscsiOwnerRoot, storageUnitRoot
	t.Cleanup(func() {
		nativeRun, storageMountRoot, iscsiNodeRoot, iscsiOwnerRoot, storageUnitRoot = oldRun, oldMount, oldNode, oldOwner, oldUnits
	})
	root := t.TempDir()
	storageMountRoot, iscsiNodeRoot, iscsiOwnerRoot, storageUnitRoot = filepath.Join(root, "m"), filepath.Join(root, "n"), filepath.Join(root, "o"), filepath.Join(root, "u")
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4, FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", MountPath: filepath.Join(storageMountRoot, "repo"), ExclusiveTarget: true}
	s := protocol.StorageRegistration{ID: "repo", Kind: protocol.StorageKindISCSI, Path: v.MountPath, Source: protocol.StorageSource{ISCSI: v}}
	if err := writeNexISCSIOwner(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	owner := iscsiOwnerPath(context.Background(), v)
	var bad bool
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "iscsiadm" {
			bad = true
		}
		if name == "findmnt" {
			return []byte(storageMountRoot + " /dev/root\n"), nil
		}
		return nil, nil
	}
	if err := detachISCSI(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if bad {
		t.Fatal("crash replay mutated absent node")
	}
	if _, err := os.Stat(owner); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner remained: %v", err)
	}
}

func TestDetachISCSIIgnoresAncestorMountOnReplay(t *testing.T) {
	oldRun, oldMount, oldNodeRoot, oldUnits := nativeRun, storageMountRoot, iscsiNodeRoot, storageUnitRoot
	t.Cleanup(func() {
		nativeRun, storageMountRoot, iscsiNodeRoot, storageUnitRoot = oldRun, oldMount, oldNodeRoot, oldUnits
	})
	root := t.TempDir()
	storageMountRoot, iscsiNodeRoot, storageUnitRoot = filepath.Join(root, "mounts"), filepath.Join(root, "nodes"), filepath.Join(root, "units")
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4, FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", MountPath: filepath.Join(storageMountRoot, "repo"), ExclusiveTarget: true}
	s := protocol.StorageRegistration{ID: "repo", Kind: protocol.StorageKindISCSI, Path: v.MountPath, Source: protocol.StorageSource{ISCSI: v}}
	var umount bool
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "findmnt" {
			return []byte(storageMountRoot + " /dev/root\n"), nil
		}
		if name == "systemctl" && len(args) > 0 && args[0] == "stop" {
			umount = true
		}
		return nil, nil
	}
	if err := detachISCSI(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if umount {
		t.Fatal("detach attempted to stop an ancestor mount after a replay")
	}
}

func TestStorageDetachAtUsesCustomEngineOwnerRoot(t *testing.T) {
	oldRun, oldMount, oldNodeRoot, oldUnits := nativeRun, storageMountRoot, iscsiNodeRoot, storageUnitRoot
	t.Cleanup(func() {
		nativeRun, storageMountRoot, iscsiNodeRoot, storageUnitRoot = oldRun, oldMount, oldNodeRoot, oldUnits
	})
	root := t.TempDir()
	storageMountRoot, iscsiNodeRoot, storageUnitRoot = filepath.Join(root, "mounts"), filepath.Join(root, "nodes"), filepath.Join(root, "units")
	credentialRoot := filepath.Join(root, "custom-engine", "storage-credentials")
	v := &protocol.ISCSIStorageSource{Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", LUN: 4, FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", MountPath: filepath.Join(storageMountRoot, "repo"), ExclusiveTarget: true}
	s := protocol.StorageRegistration{ID: "repo", Kind: protocol.StorageKindISCSI, Path: v.MountPath, Source: protocol.StorageSource{ISCSI: v}}
	ctx := context.WithValue(context.Background(), storageOwnerRootKey{}, filepath.Join(filepath.Dir(credentialRoot), "iscsi-owners"))
	if err := writeNexISCSIOwner(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(iscsiNodePath(v)), 0700); err != nil {
		t.Fatal(err)
	}
	// Model iscsiadm canonicalizing its node file and dropping the Nex marker.
	if err := os.WriteFile(iscsiNodePath(v), []byte("node.startup = automatic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "findmnt" {
			return []byte(storageMountRoot + " /dev/root\n"), nil
		}
		if name == "iscsiadm" && len(args) > 0 && args[len(args)-1] == "delete" {
			deleted = true
		}
		return nil, nil
	}
	if err := storageDetachAt(context.Background(), s, credentialRoot); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("custom-root owner was not used to clean the rewritten node")
	}
	if _, err := os.Stat(iscsiOwnerPath(ctx, v)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner record remained after detach: %v", err)
	}
}

func TestManagedStoragePrepareRollsBackCapacityRefusal(t *testing.T) {
	oldRun, oldPrepare, oldReady, oldMount, oldNodeRoot, oldUnits := nativeRun, storagePrepareAtFn, managedStorageReadyFn, storageMountRoot, iscsiNodeRoot, storageUnitRoot
	t.Cleanup(func() {
		nativeRun, storagePrepareAtFn, managedStorageReadyFn = oldRun, oldPrepare, oldReady
		storageMountRoot, iscsiNodeRoot, storageUnitRoot = oldMount, oldNodeRoot, oldUnits
	})
	root := t.TempDir()
	engineRoot := filepath.Join(root, "engine")
	storageMountRoot = filepath.Join(root, "mounts")
	iscsiNodeRoot = filepath.Join(root, "nodes")
	storageUnitRoot = filepath.Join(root, "units")
	credentialRoot := filepath.Join(engineRoot, credentialDirectory)
	ownerRoot := filepath.Join(engineRoot, "iscsi-owners")
	if err := os.MkdirAll(credentialRoot, 0700); err != nil {
		t.Fatal(err)
	}
	v := &protocol.ISCSIStorageSource{
		Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:undersized",
		WWID: "3600508b400105e210000900000490099", LUN: 7,
		FilesystemUUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		MountPath:      filepath.Join(storageMountRoot, "undersized"), ExclusiveTarget: true,
	}
	s := protocol.StorageRegistration{
		ID: "undersized", Kind: protocol.StorageKindISCSI, Path: v.MountPath,
		Source: protocol.StorageSource{ISCSI: v},
		Identity: protocol.StorageIdentity{ISCSI: &protocol.ISCSIStorageIdentity{
			TargetIQN: v.TargetIQN, LUN: v.LUN, WWID: v.WWID, FilesystemUUID: v.FilesystemUUID,
		}},
	}
	if err := os.MkdirAll(v.MountPath, 0711); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(iscsiNodePath(v)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iscsiNodePath(v), []byte("node.startup = automatic\n"+nexISCSINodeMarker), 0600); err != nil {
		t.Fatal(err)
	}
	ownerCtx := context.WithValue(context.Background(), storageOwnerRootKey{}, ownerRoot)
	if err := os.MkdirAll(filepath.Dir(iscsiOwnerPath(ownerCtx, v)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iscsiOwnerPath(ownerCtx, v), []byte(iscsiOwnerRecord(v)), 0600); err != nil {
		t.Fatal(err)
	}
	unitName, unitPath := iscsiUnitPath(s)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte(nexMountMarker+"[Mount]\nWhere="+v.MountPath+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	storagePrepareAtFn = func(_ context.Context, got protocol.StorageRegistration, gotRoot string) (storageAccess, error) {
		if got.ID != s.ID || gotRoot != credentialRoot {
			t.Fatalf("prepare used unexpected args: %#v %q", got, gotRoot)
		}
		return storageAccess{Path: v.MountPath, Observed: s.Identity, Health: "healthy", Available: 1 << 20}, nil
	}
	managedStorageReadyFn = func(_ context.Context, got protocol.StorageRegistration, required uint64) (storageAccess, error) {
		if got.Path != v.MountPath || required != 1<<30 {
			t.Fatalf("ready used unexpected args: %#v %d", got, required)
		}
		return storageAccess{}, errors.New("insufficient free storage including reserve")
	}
	var calls [][]string
	sessionActive := true
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch name {
		case "findmnt":
			return []byte(storageMountRoot + " /dev/root\n"), nil
		case "iscsiadm":
			if len(args) > 1 && args[1] == "session" {
				if !sessionActive {
					return nil, iscsiNoSessionTestError(t)
				}
				return []byte("Target: " + v.TargetIQN + "\nCurrent Portal: storage.example:3260,1\n"), nil
			}
			if len(args) > 0 && args[len(args)-1] == "--logout" {
				sessionActive = false
			}
			if len(args) > 0 && args[len(args)-1] == "delete" {
				_ = os.Remove(iscsiNodePath(v))
			}
			return nil, nil
		default:
			return nil, nil
		}
	}

	e := &ManagedEngine{stateDir: filepath.Join(engineRoot, "managed-vms")}
	_, err := e.managedStoragePrepare(context.Background(), s, 1<<30)
	if err == nil || !strings.Contains(err.Error(), "insufficient free storage including reserve") {
		t.Fatalf("managedStoragePrepare() = %v, want capacity refusal", err)
	}
	if errors.Is(err, ErrStorageRollbackUncertain) {
		t.Fatalf("capacity refusal reported uncertain rollback: %v", err)
	}
	joined := ""
	for _, call := range calls {
		joined += strings.Join(call, " ") + "\n"
	}
	for _, want := range []string{
		"systemctl disable " + unitName,
		"node.startup -v manual",
		"--logout",
		"--op delete",
		"systemctl daemon-reload",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("capacity refusal omitted cleanup %q:\n%s", want, joined)
		}
	}
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mount unit remained after capacity refusal: %v", err)
	}
	if _, err := os.Stat(iscsiNodePath(v)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("iSCSI node remained after capacity refusal: %v", err)
	}
	if _, err := os.Stat(iscsiOwnerPath(ownerCtx, v)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("iSCSI owner remained after capacity refusal: %v", err)
	}
}

func TestManagedStoragePrepareReportsUncertainCapacityCleanup(t *testing.T) {
	oldPrepare, oldReady, oldDetachRun := storagePrepareAtFn, managedStorageReadyFn, nativeRun
	t.Cleanup(func() {
		storagePrepareAtFn, managedStorageReadyFn, nativeRun = oldPrepare, oldReady, oldDetachRun
	})
	root := t.TempDir()
	engineRoot := filepath.Join(root, "engine")
	if err := os.MkdirAll(filepath.Join(engineRoot, credentialDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	v := &protocol.ISCSIStorageSource{
		Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:cleanup-fail",
		WWID: "3600508b400105e210000900000490088", LUN: 3,
		FilesystemUUID: "11111111-2222-3333-4444-555555555555",
		MountPath:      filepath.Join(root, "mount"), ExclusiveTarget: true,
	}
	s := protocol.StorageRegistration{
		ID: "cleanup-fail", Kind: protocol.StorageKindISCSI, Path: v.MountPath,
		Source:   protocol.StorageSource{ISCSI: v},
		Identity: protocol.StorageIdentity{ISCSI: &protocol.ISCSIStorageIdentity{TargetIQN: v.TargetIQN, LUN: v.LUN, WWID: v.WWID, FilesystemUUID: v.FilesystemUUID}},
	}
	storagePrepareAtFn = func(context.Context, protocol.StorageRegistration, string) (storageAccess, error) {
		return storageAccess{Path: v.MountPath, Observed: s.Identity}, nil
	}
	managedStorageReadyFn = func(context.Context, protocol.StorageRegistration, uint64) (storageAccess, error) {
		return storageAccess{}, errors.New("insufficient free storage including reserve")
	}
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("detach failed")
	}
	_, err := (&ManagedEngine{stateDir: filepath.Join(engineRoot, "managed-vms")}).managedStoragePrepare(context.Background(), s, 1<<30)
	if !errors.Is(err, ErrStorageRollbackUncertain) || !strings.Contains(err.Error(), "insufficient free storage including reserve") {
		t.Fatalf("managedStoragePrepare() = %v, want joined capacity and uncertain cleanup", err)
	}
}

func TestPrepareISCSIRollbackRemovesEngineOwnerReceipt(t *testing.T) {
	oldRun, oldMount, oldNodeRoot, oldUnits := nativeRun, storageMountRoot, iscsiNodeRoot, storageUnitRoot
	t.Cleanup(func() {
		nativeRun, storageMountRoot, iscsiNodeRoot, storageUnitRoot = oldRun, oldMount, oldNodeRoot, oldUnits
	})
	root := t.TempDir()
	storageMountRoot = filepath.Join(root, "mounts")
	iscsiNodeRoot = filepath.Join(root, "nodes")
	storageUnitRoot = filepath.Join(root, "units")
	ownerRoot := filepath.Join(root, "engine", "iscsi-owners")
	v := &protocol.ISCSIStorageSource{
		Portal: "storage.example:3260", TargetIQN: "iqn.2026-01.example:owner-rollback",
		WWID: "3600508b400105e210000900000490077", LUN: 4,
		FilesystemUUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		MountPath:      filepath.Join(storageMountRoot, "owner-rollback"), ExclusiveTarget: true,
	}
	s := protocol.StorageRegistration{ID: "owner-rollback", Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: v}}
	ctx := context.WithValue(context.Background(), storageOwnerRootKey{}, ownerRoot)
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "iscsiadm" && len(args) > 1 && args[1] == "session" {
			return nil, iscsiNoSessionTestError(t)
		}
		if name == "iscsiadm" && len(args) > 7 && args[6] == "--op" && args[7] == "new" {
			path := iscsiNodePath(v)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(path, []byte("node.startup = manual\n"+nexISCSINodeMarker), 0600)
		}
		if name == "lsblk" {
			// Present a different LUN WWID so preparation fails after the owned node exists.
			return []byte(`{"blockdevices":[{"path":"/dev/sdb","type":"disk","wwn":"3600508b400105e210000900000490066","hctl":"7:0:0:4","children":[{"path":"/dev/sdb1","type":"part","uuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","fstype":"ext4"}]}]}`), nil
		}
		if name == "iscsiadm" && len(args) > 0 && args[len(args)-1] == "delete" {
			_ = os.Remove(iscsiNodePath(v))
		}
		return nil, nil
	}
	if _, err := prepareISCSI(ctx, s); err == nil || !strings.Contains(err.Error(), "WWID") {
		t.Fatalf("prepareISCSI() = %v, want WWID mismatch", err)
	}
	if _, err := os.Stat(iscsiNodePath(v)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("node remained after prepare rollback: %v", err)
	}
	if _, err := os.Stat(iscsiOwnerPath(ctx, v)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("engine owner receipt remained after prepare rollback: %v", err)
	}
}

func TestValidateStorageISCSISessionStrictReadOnly(t *testing.T) {
	old := nativeRun
	t.Cleanup(func() { nativeRun = old })
	const portal = "Storage.Example:3260"
	const target = "iqn.2026-09.example:shared"
	sessionID, err := StorageISCSISessionID(portal, target)
	if err != nil {
		t.Fatal(err)
	}
	fixtureAt := func(observedPortal, sid, observedTarget, connection, session, daemon string) []byte {
		// Captured Debian 13 `iscsiadm -m session -P 1` structure. It has
		// no `tcp: [SID]` header; the transport and SID are fields below.
		return []byte("Target: " + observedTarget + " (non-flash)\n" +
			"\tCurrent Portal: " + observedPortal + ",1\n" +
			"\tPersistent Portal: " + observedPortal + ",1\n" +
			"\t\t**********\n\t\tInterface:\n\t\t**********\n" +
			"\t\tIface Transport: tcp\n\t\tSID: " + sid + "\n" +
			"\tiSCSI Connection State: " + connection + "\n" +
			"\tiSCSI Session State: " + session + "\n" +
			"\tInternal iscsid Session State: " + daemon + "\n")
	}
	fixture := func(sid, observedTarget, connection, session, daemon string) []byte {
		return fixtureAt("storage.example:3260", sid, observedTarget, connection, session, daemon)
	}
	var calls [][]string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return fixture("7", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE"), nil
	}
	if err := ValidateStorageISCSISession(context.Background(), portal, target, sessionID); err != nil {
		t.Fatalf("ValidateStorageISCSISession() = %v", err)
	}
	if !reflect.DeepEqual(calls, [][]string{{"iscsiadm", "-m", "session", "-P", "1"}}) {
		t.Fatalf("native commands = %#v, want read-only session inspection", calls)
	}
	// A separate portal is a distinct transport identity. Its good record must
	// not be treated as this monitor's health, nor poison it.
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		return append(fixture("7", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE"), fixtureAt("other.example:3260", "8", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE")...), nil
	}
	if err := ValidateStorageISCSISession(context.Background(), portal, target, sessionID); err != nil {
		t.Fatalf("well-formed distinct alternate portal = %v", err)
	}
	for _, tc := range []struct {
		name string
		out  []byte
	}{
		{"missing", nil},
		{"wrong-target", fixture("7", "iqn.2026-09.example:other", "LOGGED IN", "LOGGED_IN", "NO CHANGE")},
		{"unknown", fixture("7", target, "Unknown", "LOGGED_IN", "NO CHANGE")},
		{"recovering", fixture("7", target, "LOGGED IN", "LOGGED_IN", "REOPEN")},
		{"missing-session-state", fixture("7", target, "LOGGED IN", "", "NO CHANGE")},
		{"wrong-persistent-portal", []byte(strings.Replace(string(fixture("7", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE")), "Persistent Portal: storage.example:3260,1", "Persistent Portal: other.example:3260,1", 1))},
		{"non-tcp", []byte(strings.Replace(string(fixture("7", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE")), "Iface Transport: tcp", "Iface Transport: iser", 1))},
		{"malformed-sid", fixture("x", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE")},
		{"duplicate", append(fixture("7", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE"), fixture("8", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE")...)},
		{"duplicate-field", append(fixture("7", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE"), []byte("\tSID: 7\n")...)},
		{"incomplete-alternate", append(fixture("7", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE"), []byte("Target: "+target+" (non-flash)\n\tCurrent Portal: other.example:3260,1\n")...)},
		{"duplicate-sid-alternate", append(fixture("7", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE"), fixtureAt("other.example:3260", "7", target, "LOGGED IN", "LOGGED_IN", "NO CHANGE")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nativeRun = func(context.Context, string, ...string) ([]byte, error) { return tc.out, nil }
			if err := ValidateStorageISCSISession(context.Background(), portal, target, sessionID); err == nil {
				t.Fatal("accepted unsafe iSCSI session output")
			}
		})
	}
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("private native detail")
	}
	if err := ValidateStorageISCSISession(context.Background(), portal, target, sessionID); err == nil || strings.Contains(err.Error(), "private native detail") {
		t.Fatalf("native error = %v, want bounded public error", err)
	}
}

func TestStorageISCSISessionIDCanonicalizesDefaultPort(t *testing.T) {
	withoutPort, err := StorageISCSISessionID("Storage.Example", "iqn.2026-09.example:shared")
	if err != nil {
		t.Fatal(err)
	}
	withPort, err := StorageISCSISessionID("storage.example:3260", "iqn.2026-09.example:shared")
	if err != nil || withoutPort != withPort || len(withPort) != len("nex-iscsi-")+64 || !strings.HasPrefix(withPort, "nex-iscsi-") {
		t.Fatalf("canonical session IDs = %q, %q, %v", withoutPort, withPort, err)
	}
	canonicalPortal, sessionID, err := StorageISCSISessionParameters("Storage.Example", "iqn.2026-09.example:shared")
	if err != nil || canonicalPortal != "storage.example:3260" || sessionID != withPort {
		t.Fatalf("StorageISCSISessionParameters() = %q, %q, %v", canonicalPortal, sessionID, err)
	}
	if _, err := StorageISCSISessionID("storage.example:0", "iqn.2026-09.example:shared"); err == nil {
		t.Fatal("accepted invalid portal")
	}
}
