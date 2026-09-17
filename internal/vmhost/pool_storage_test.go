// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestPreparePoolStorageRefusesLocalZFSAndISCSISecondaryWriter(t *testing.T) {
	for _, kind := range []string{protocol.StorageKindLocal, protocol.StorageKindZFS, protocol.StorageKindISCSI} {
		s := protocol.StorageRegistration{ID: "repo-1", Kind: kind}
		if _, err := PreparePoolStorage(context.Background(), t.TempDir(), s); err == nil {
			t.Fatalf("%s secondary writer accepted", kind)
		}
	}
}

func TestPreparePoolStorageRejectsClusteredISCSIWithoutCoreProfile(t *testing.T) {
	s := protocol.StorageRegistration{ID: "repo-1", Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2", EligibleHostIDs: []string{"host-1", "host-2", "host-3"}}}}
	if _, err := PreparePoolStorage(context.Background(), t.TempDir(), s); err == nil {
		t.Fatal("clustered iSCSI was prepared without a Core-owned cluster profile")
	}
}

func TestValidatePoolStorageAccessRunsOneClusteredQEMUProof(t *testing.T) {
	oldNative, oldValidate := nativeRun, validateClusteredPoolStorageAccess
	t.Cleanup(func() { nativeRun, validateClusteredPoolStorageAccess = oldNative, oldValidate })
	c := clusterTestConfig()
	path := t.TempDir()
	proofs := 0
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "runuser" && len(args) > 5 && args[3] == "test" && args[4] == "-x" {
			proofs++
		}
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	validateClusteredPoolStorageAccess = func(ctx context.Context, _ string, _ protocol.StorageRegistration, _ protocol.StorageClusterConfig) (storageAccess, error) {
		if err := qemuStorageAccess(ctx, path); err != nil {
			return storageAccess{}, err
		}
		return storageAccess{Path: path, Health: "healthy"}, nil
	}
	ctx := WithStorageClusterStateDir(WithStorageClusterConfig(context.Background(), c), t.TempDir())
	if _, err := ValidatePoolStorageAccess(ctx, clusterTestRegistration(c)); err != nil {
		t.Fatal(err)
	}
	if proofs != 1 {
		t.Fatalf("clustered QEMU proofs = %d, want 1", proofs)
	}
}

func TestValidatePoolStorageAccessPropagatesClusteredQEMUProofFailure(t *testing.T) {
	oldNative, oldCluster, oldHealth := nativeRun, clusterRun, clusterHealthyAt
	t.Cleanup(func() { nativeRun, clusterRun, clusterHealthyAt = oldNative, oldCluster, oldHealth })
	c := clusterTestConfig()
	c.MountPath = "/var/lib"
	s := clusterTestRegistration(c)
	s.Path, s.Source.ISCSI.MountPath = c.MountPath, c.MountPath
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		switch name {
		case "lsblk":
			return []byte(`{"blockdevices":[{"path":"/dev/fake","type":"disk","uuid":"12345678-1234-1234-1234-123456789abc","wwn":"3600508b400105e210000900000490000","fstype":"gfs2","hctl":"0:0:0:1"}]}`), nil
		case "runuser":
			return nil, errors.New("denied")
		default:
			t.Fatalf("unexpected native command %s", name)
			return nil, nil
		}
	}
	clusterRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		switch name {
		case "tunegfs2":
			return []byte("Lock protocol: lock_dlm\nLock table: pool-1:repo-1\n"), nil
		case "gfs2_edit":
			return []byte("journal0: clean\njournal1: clean\njournal2: clean\n"), nil
		default:
			t.Fatalf("unexpected cluster command %s", name)
			return nil, nil
		}
	}
	clusterHealthyAt = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
		return protocol.StorageClusterStatus{Healthy: true, Quorate: true, Mounted: true, FencingVerified: true}, nil
	}
	ctx := WithStorageClusterStateDir(WithStorageClusterConfig(context.Background(), c), t.TempDir())
	if _, err := ValidatePoolStorageAccess(ctx, s); err == nil || !strings.Contains(err.Error(), "not traversable") {
		t.Fatalf("clustered QEMU proof failure = %v", err)
	}
}

func TestPreparePoolStorageRunsOneClusteredQEMUProof(t *testing.T) {
	oldNative, oldPrepare := nativeRun, prepareClusteredPoolStorageAccess
	t.Cleanup(func() { nativeRun, prepareClusteredPoolStorageAccess = oldNative, oldPrepare })
	c := clusterTestConfig()
	path := t.TempDir()
	proofs := 0
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "runuser" && len(args) > 5 && args[3] == "test" && args[4] == "-x" {
			proofs++
		}
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	prepareClusteredPoolStorageAccess = func(ctx context.Context, _ string, s protocol.StorageRegistration, _ protocol.StorageClusterConfig) (storageAccess, error) {
		if err := qemuStorageAccess(ctx, path); err != nil {
			return storageAccess{}, err
		}
		return storageAccess{Path: path, Health: "healthy", Observed: s.Identity}, nil
	}
	if _, err := PreparePoolStorage(WithStorageClusterConfig(context.Background(), c), t.TempDir(), clusterTestRegistration(c)); err != nil {
		t.Fatal(err)
	}
	if proofs != 1 {
		t.Fatalf("clustered QEMU proofs = %d, want 1", proofs)
	}
}

func TestPreparePoolStorageRunsQEMUProofForNFSAndSMB(t *testing.T) {
	oldNative, oldPrepare := nativeRun, preparePoolStorageAt
	t.Cleanup(func() {
		nativeRun, preparePoolStorageAt = oldNative, oldPrepare
	})
	path := t.TempDir()
	proofs := 0
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "runuser" && len(args) > 5 && args[3] == "test" && args[4] == "-x" {
			proofs++
		}
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	preparePoolStorageAt = func(context.Context, protocol.StorageRegistration, string) (storageAccess, error) {
		return storageAccess{Path: path, Health: "healthy"}, nil
	}
	for _, tc := range []struct {
		name string
		s    protocol.StorageRegistration
	}{
		{name: "nfs", s: protocol.StorageRegistration{ID: "repo-1", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}}}},
		{name: "smb", s: protocol.StorageRegistration{ID: "repo-1", Kind: protocol.StorageKindSMB, Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: "files.example.test", Share: "vms"}}}},
	} {
		proofs = 0
		if _, err := PreparePoolStorage(context.Background(), t.TempDir(), tc.s); err != nil {
			t.Fatalf("%s preparation = %v", tc.name, err)
		}
		if proofs != 1 {
			t.Fatalf("%s QEMU proofs = %d, want 1", tc.name, proofs)
		}
	}
}

func TestExpectedPeerIdentityFailsBeforeSuccessfulRegistration(t *testing.T) {
	oldPrepare, oldReady, oldRun := storagePrepareAtFn, managedStorageReadyFn, nativeRun
	t.Cleanup(func() { storagePrepareAtFn, managedStorageReadyFn, nativeRun = oldPrepare, oldReady, oldRun })
	path := t.TempDir()
	storagePrepareAtFn = func(context.Context, protocol.StorageRegistration, string) (storageAccess, error) {
		return storageAccess{Path: path, Observed: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "other.example.test", Export: "/export"}}}, nil
	}
	managedStorageReadyFn = func(context.Context, protocol.StorageRegistration, uint64) (storageAccess, error) {
		return storageAccess{Path: path}, nil
	}
	nativeRun = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export"}}}
	result, err := e.Execute(WithExpectedStorageIdentity(context.Background(), protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export"}}), protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &storage}, nil)
	if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "identity") {
		t.Fatalf("identity mismatch = %+v, %v", result, err)
	}
	applied, _ := e.Applied(context.Background())
	if len(applied.Storage) != 0 {
		t.Fatalf("mismatched identity was journaled as storage: %#v", applied.Storage)
	}
}

func TestExpectedPeerGenerationIsPreservedOnInitialMemberImport(t *testing.T) {
	oldPrepare, oldReady := storagePrepareAtFn, managedStorageReadyFn
	t.Cleanup(func() { storagePrepareAtFn, managedStorageReadyFn = oldPrepare, oldReady })
	path := t.TempDir()
	identity := protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export"}}
	storagePrepareAtFn = func(context.Context, protocol.StorageRegistration, string) (storageAccess, error) {
		return storageAccess{Path: path, Observed: identity, Health: "healthy", Available: 2 << 30}, nil
	}
	managedStorageReadyFn = func(context.Context, protocol.StorageRegistration, uint64) (storageAccess, error) {
		return storageAccess{Path: path, Observed: identity, Health: "healthy", Available: 2 << 30}, nil
	}
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "member-1", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export"}}}
	result, err := e.Execute(WithPoolStorageExpectation(context.Background(), identity, 7), protocol.Command{OperationID: "memberimport7", Kind: "storage-register", Storage: &storage}, nil)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("import = %+v, %v", result, err)
	}
	applied, _ := e.Applied(context.Background())
	if len(applied.Storage) != 1 || applied.Storage[0].Generation != 7 {
		t.Fatalf("generation was not retained: %#v", applied.Storage)
	}
}

func TestExpectedSMBMarkerIdentityIgnoresReconnectFilesystemDiagnostic(t *testing.T) {
	expected := protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: "files.example.test", Share: "vms", MarkerID: "0123456789abcdef0123456789abcdef", FilesystemID: "master-diagnostic"}}
	actual := protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: "files.example.test", Share: "vms", MarkerID: "0123456789abcdef0123456789abcdef", FilesystemID: "member-diagnostic"}}
	if !StorageIdentityMatchesExpected(expected, actual) {
		t.Fatal("same SMB marker rejected after reconnect")
	}
	actual.SMB.MarkerID = "fedcba9876543210fedcba9876543210"
	if StorageIdentityMatchesExpected(expected, actual) {
		t.Fatal("different SMB marker accepted")
	}
}

func TestPoolSMBCredentialUsesMemberScopedRegistrationRequest(t *testing.T) {
	state := t.TempDir()
	receipt, err := SavePoolStorageCredential(state, "pool-1", "member-1", "repo-1", 3, protocol.StorageCredentials{Username: "operator", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err = bindCredentialReceipt(state, receipt, "repo-1", "pool-1", "member-1", "master-registration-3"); err == nil {
		t.Fatal("member credential accepted a master registration request ID")
	}
	if err = bindCredentialReceipt(state, receipt, "repo-1", "pool-1", "member-1", PoolStorageCredentialRequestID("repo-1", 3)); err != nil {
		t.Fatalf("member-scoped receipt did not bind: %v", err)
	}
}

func TestPoolSMBCredentialPreservesDomain(t *testing.T) {
	state := t.TempDir()
	want := protocol.StorageCredentials{Username: "operator", Secret: "secret", Domain: "truenas"}
	receipt, err := SavePoolStorageCredential(state, "pool-1", "member-1", "repo-1", 3, want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PoolStorageCredential(state, receipt)
	if err != nil || got != want {
		t.Fatalf("pool SMB credential = %#v, %v", got, err)
	}
	mount, err := os.ReadFile(filepath.Join(state, credentialDirectory, receipt))
	if err != nil || !strings.Contains(string(mount), "domain=truenas\n") || !strings.Contains(string(mount), "username=operator\n") {
		t.Fatalf("mount credential %q: %v", mount, err)
	}
}

// The local repository a host registers for itself holds VM disks as well as ISO images, so
// its default name must not present it as ISO media. That name is a durable pool-master
// record: a changed default may only name a host registering this path for the first time. An existing
// registration, whatever it is called, is never renamed, duplicated or re-registered, and a
// fresh Controller re-adopts it under the name the host stored.
func TestDefaultLocalStorageNameIsAccurateAndNeverRenamesAnExistingRegistration(t *testing.T) {
	ctx := context.Background()
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "nexbr0"}, {Name: "nexbr0", Kind: "bridge"}}
	// The property, not a chosen wording: the name must not present the repository as ISO media.
	if strings.Contains(strings.ToLower(DefaultStorageName), "iso") || strings.TrimSpace(DefaultStorageName) == "" {
		t.Fatalf("default local repository name %q names ISO media, although the repository holds VM disks too", DefaultStorageName)
	}
	registrations := func(e *ManagedEngine) []protocol.StorageRegistration {
		t.Helper()
		var out []protocol.StorageRegistration
		for _, j := range journalsOf(t, e) {
			if j.Command.Storage != nil {
				out = append(out, *j.Command.Storage)
			}
		}
		return out
	}
	prev := DefaultStoragePath
	t.Cleanup(func() { DefaultStoragePath = prev })
	DefaultStoragePath = filepath.Join(t.TempDir(), "images")
	if err := os.MkdirAll(DefaultStoragePath, 0755); err != nil {
		t.Fatal(err)
	}
	fresh := NewManaged(filepath.Join(t.TempDir(), "state"))
	if err := fresh.RegisterDefaults(ctx, "pool-1", "host-1", nics); err != nil {
		t.Fatal(err)
	}
	if got := registrations(fresh); len(got) != 1 || got[0].Name != DefaultStorageName || got[0].Path != DefaultStoragePath {
		t.Fatalf("fresh host default storage: %+v", got)
	}
	for _, existing := range []string{"ISO", "Operator's own name"} {
		t.Run(existing, func(t *testing.T) {
			e := startEngine(t)
			s := protocol.StorageRegistration{ID: "existing-store", PoolID: "pool-1", HostID: "host-1", Name: existing, Path: DefaultStoragePath}
			j := managedJournal{Command: protocol.Command{OperationID: "existing-op", Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "existing-op", Status: "succeeded"}}
			if err := e.save(j); err != nil {
				t.Fatal(err)
			}
			// An upgraded Core calls this on every uplink change, not only at first connection.
			for i := 0; i < 2; i++ {
				if err := e.RegisterDefaults(ctx, "pool-1", "host-1", nics); err != nil {
					t.Fatal(err)
				}
			}
			got := registrations(e)
			if len(got) != 1 || got[0].ID != s.ID || got[0].Name != existing || got[0].Path != DefaultStoragePath {
				t.Fatalf("existing registration changed: %+v", got)
			}
			// A fresh Controller adopts the host's stored record, not the new default.
			applied, _ := e.Applied(ctx)
			if len(applied.Storage) != 1 || applied.Storage[0].ID != s.ID || applied.Storage[0].Name != existing {
				t.Fatalf("adoption reported %+v", applied.Storage)
			}
		})
	}
}
