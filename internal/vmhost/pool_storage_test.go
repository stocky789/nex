// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
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
