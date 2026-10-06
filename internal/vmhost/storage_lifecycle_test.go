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

func lifecycleLocalStorage(id, path string) protocol.StorageRegistration {
	return protocol.StorageRegistration{ID: id, PoolID: "pool-1", HostID: "host-1", Name: id, Path: path}
}

func lifecycleCommand(op, kind string, v protocol.StorageLifecycleRequest) protocol.Command {
	return protocol.Command{OperationID: op, Kind: kind, StorageLifecycle: &v}
}

func lifecyclePtr(s protocol.StorageRegistration) *protocol.StorageRegistration { return &s }

func lifecycleMedia(id, pool, host, storage string) protocol.Media {
	return protocol.Media{ID: id, PoolID: pool, HostID: host, StorageID: storage, Name: "disc.iso", SizeBytes: 40 << 10,
		SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
}

// This uses mocked ZFS and QEMU commands; it exercises journal behavior, not a real mount.
func TestStorageLifecycleAllowsIndependentRechecksAndGenerationChain(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	e := NewManaged(filepath.Join(root, "state", "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "zfs":
			return []byte("dataset-guid\n2147483648\nyes\n/tmp\n"), nil
		case "zpool":
			if len(args) > 0 && args[0] == "list" {
				return []byte("ONLINE\n"), nil
			}
			return []byte("pool-guid\n"), nil
		case "findmnt":
			return []byte("pool/dataset zfs\n"), nil
		case "id":
			return []byte(strconv.Itoa(os.Getuid()) + "\n"), nil
		default:
			return nil, nil
		}
	}
	base := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Name: "repo-1", Kind: protocol.StorageKindZFS, Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "pool", Dataset: "dataset", MountPath: "/tmp"}}, Path: "/tmp", Generation: 1}
	requested := base
	requested.Path = ""
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &requested}, Result: protocol.Result{OperationID: "register-1", Status: "succeeded"}, AppliedStorage: &base}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"recheck-1", "recheck-2"} {
		request := protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: "repo-1", PoolID: "pool-1", HostID: "host-1"}
		if result, err := e.Execute(ctx, lifecycleCommand(op, "storage-recheck", request), nil); err != nil || result.Status != "succeeded" {
			t.Fatalf("%s = %+v, %v", op, result, err)
		}
	}
	for generation, name := range []string{"first", "second"} {
		next := base
		next.Name, next.Path = name, ""
		request := protocol.StorageLifecycleRequest{Action: "update", RepositoryID: "repo-1", PoolID: "pool-1", HostID: "host-1", ExpectedGeneration: uint64(generation + 1), Registration: &next}
		if result, err := e.Execute(ctx, lifecycleCommand("update-"+name, "storage-update", request), nil); err != nil || result.Status != "succeeded" {
			t.Fatalf("update %d = %+v, %v", generation+1, result, err)
		}
	}
	got, _ := e.Applied(ctx)
	if len(got.Storage) != 1 || got.Storage[0].Generation != 3 || got.Storage[0].Name != "second" {
		t.Fatalf("applied storage = %+v", got.Storage)
	}
}

func TestStorageDetachReleasesAttachedCredential(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	storage := filepath.Join(root, "storage")
	if err := os.Mkdir(storage, 0711); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	e := NewManaged(filepath.Join(state, "managed"))
	handoff := testCredentialHandoff()
	receipt, err := SaveStorageCredential(state, handoff)
	if err != nil {
		t.Fatal(err)
	}
	s := lifecycleLocalStorage("repo-1", storage)
	s.CredentialReceipt, s.CredentialRequestID = receipt.Receipt, handoff.Offer.RegistrationRequestID
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := bindCredentialReceipt(state, receipt.Receipt, s.ID, s.PoolID, s.HostID, s.CredentialRequestID); err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "register-1", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	retire := func(op string) protocol.Command {
		return protocol.Command{OperationID: op, Kind: "storage-credential-retire", CredentialRetire: &protocol.StorageCredentialRetireRequest{Receipt: receipt.Receipt, PoolID: "pool-1", HostID: "host-1"}}
	}
	if result, err := e.Execute(ctx, retire("retire-in-use"), nil); err != nil || result.Status != "failed" || !strings.Contains(result.Error, "attached") {
		t.Fatalf("in-use retire = %+v, %v", result, err)
	}
	detach := protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "repo-1", PoolID: "pool-1", HostID: "host-1", ExpectedGeneration: 1}
	if result, err := e.Execute(ctx, lifecycleCommand("detach-1", "storage-detach", detach), nil); err != nil || result.Status != "succeeded" {
		t.Fatalf("detach = %+v, %v", result, err)
	}
	retired, err := os.ReadFile(credentialPath(state, handoff.Offer.RequestID))
	if err != nil || strings.Contains(string(retired), handoff.Credentials.Secret) || !strings.Contains(string(retired), `"status":"retired"`) {
		t.Fatalf("detach did not retire the attached credential %q: %v", retired, err)
	}
	if _, err = os.Stat(filepath.Join(state, credentialDirectory, receipt.Receipt)); !os.IsNotExist(err) {
		t.Fatalf("mount credential remains after detach: %v", err)
	}
	if result, err := e.Execute(ctx, retire("retire-after-detach"), nil); err != nil || result.Status != "succeeded" {
		t.Fatalf("retire after detach = %+v, %v", result, err)
	}
}

func TestStorageProjectionPreservesRemoteCredentialOnMetadataRename(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   string
		source protocol.StorageSource
	}{
		{"SMB", protocol.StorageKindSMB, protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: "files.example.test", Share: "vms"}}},
		{"iSCSI", protocol.StorageKindISCSI, protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "iscsi.example.test", TargetIQN: "iqn.2026-01.example:vm", WWID: "disk-1", FilesystemUUID: "11111111-1111-4111-8111-111111111111", ExclusiveTarget: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Name: "before", Kind: tc.kind, Source: tc.source, Path: "/var/lib/nex-storage/repo-1", Generation: 1, CredentialReceipt: "receipt-1", CredentialPresent: true, CredentialRequestID: "request-1"}
			next := current
			next.Name, next.Generation = "after", 2
			js := []managedJournal{
				{Command: protocol.Command{Kind: "storage-register", Storage: &current}, Result: protocol.Result{Status: "succeeded"}},
				{Command: protocol.Command{Kind: "storage-update", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "update", RepositoryID: current.ID, PoolID: current.PoolID, HostID: current.HostID, ExpectedGeneration: 1, Registration: &protocol.StorageRegistration{Name: "after", Kind: tc.kind, Source: tc.source}}}, Result: protocol.Result{Status: "succeeded"}, AppliedStorage: &next},
			}
			got := storageProjection(js)[current.ID]
			if got.CredentialReceipt != "receipt-1" || !got.CredentialPresent || got.CredentialRequestID != "request-1" || !storageCredentialAttached(js, "receipt-1") {
				t.Fatalf("metadata rename lost cleanup protection: %+v", got)
			}
		})
	}
}

func TestStorageRegistrationConflictsCoverUncertainAndObservedAliases(t *testing.T) {
	remote := func(id, server string) protocol.StorageRegistration {
		return protocol.StorageRegistration{ID: id, PoolID: "pool-1", HostID: "host-1", Name: id, Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: server, Export: "/vms"}}}
	}
	for _, status := range []string{"pending", "needs_attention"} {
		t.Run(status, func(t *testing.T) {
			old := remote("first", "nfs.example.test")
			js := []managedJournal{{Command: protocol.Command{OperationID: "first", Kind: "storage-register", Storage: &old}, Result: protocol.Result{Status: status}}}
			if err := registrationConflict(js, protocol.Command{OperationID: "second", Kind: "storage-register", Storage: lifecyclePtr(remote("second", "nfs.example.test"))}); err == nil {
				t.Fatal("uncertain remote registration released its source")
			}
			old.Identity = protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/vms", FilesystemID: "fs-1"}}
			alias := remote("alias", "other.example.test")
			alias.Source.NFS.Export = "/other"
			alias.Identity = protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "other.example.test", Export: "/other", FilesystemID: "fs-1"}}
			js[0].Command.Storage = &old
			if err := registrationConflict(js, protocol.Command{OperationID: "alias", Kind: "storage-register", Storage: &alias}); err == nil {
				t.Fatal("uncertain remote registration released an observed backing identity")
			}
		})
	}
	for _, tc := range []struct {
		name      string
		old, next protocol.StorageRegistration
	}{
		{"NFS filesystem alias", protocol.StorageRegistration{ID: "nfs-1", Name: "nfs-1", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "one.example.test", Export: "/vms"}}, Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "one.example.test", Export: "/vms", FilesystemID: "fs-1"}}}, protocol.StorageRegistration{ID: "nfs-2", Name: "nfs-2", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "two.example.test", Export: "/other"}}, Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "two.example.test", Export: "/other", FilesystemID: "fs-1"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			js := []managedJournal{{Command: protocol.Command{Kind: "storage-register", Storage: &tc.old}, Result: protocol.Result{Status: "succeeded"}}}
			if err := registrationConflict(js, protocol.Command{OperationID: "alias", Kind: "storage-register", Storage: &tc.next}); err == nil {
				t.Fatal("observed backing alias accepted")
			}
		})
	}
}

func TestCredentialRotationSafeRejectsUncertainStorageUsers(t *testing.T) {
	v := managedTestVM()
	fakeCommand(t, "virsh", `if [ "$3" = domstate ]; then printf 'shut off\n'; fi`)
	for _, tc := range []struct {
		name  string
		entry managedJournal
	}{
		{"needs attention disk", managedJournal{Command: protocol.Command{Kind: "vm-update", VM: &v}, Result: protocol.Result{Status: "needs_attention"}}},
		{"needs attention media", managedJournal{Command: protocol.Command{Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: v.ID}}, Result: protocol.Result{Status: "needs_attention"}}},
		{"uncertain ISO import", managedJournal{Command: protocol.Command{Kind: "iso-import", Media: &protocol.Media{StorageID: v.Disks[0].StorageID}}, Result: protocol.Result{Status: "needs_attention"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := credentialRotationSafe(context.Background(), append([]managedJournal{{Command: protocol.Command{Kind: "vm-create", VM: &v}, Result: protocol.Result{Status: "succeeded"}}}, tc.entry), v.Disks[0].StorageID); err == nil {
				t.Fatal("credential rotation accepted an uncertain storage user")
			}
		})
	}
}

func TestLocalPathChangeIsNotMetadataOnly(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "one"), filepath.Join(root, "two")
	if err := os.Mkdir(first, 0711); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0711); err != nil {
		t.Fatal(err)
	}
	e := NewManaged(filepath.Join(root, "state", "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	current := lifecycleLocalStorage("repo-1", first)
	current.Generation = 1
	v := managedTestVM()
	v.Disks[0].StorageID = current.ID
	for _, j := range []managedJournal{
		{Command: protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &current}, Result: protocol.Result{OperationID: "register-1", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "vm-1", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "vm-1", Status: "succeeded"}},
	} {
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	next := lifecycleLocalStorage("repo-1", second)
	request := protocol.StorageLifecycleRequest{Action: "update", RepositoryID: current.ID, PoolID: current.PoolID, HostID: current.HostID, ExpectedGeneration: 1, Registration: &next}
	result, err := e.Execute(context.Background(), lifecycleCommand("move-local", "storage-update", request), nil)
	if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "cannot update while used") {
		t.Fatalf("local path change was treated as metadata-only: %+v, %v", result, err)
	}
}

func TestRetainedSnapshotBlocksStorageSourceChangeAfterVMEdit(t *testing.T) {
	root := t.TempDir()
	e := NewManaged(filepath.Join(root, "state", "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	current := lifecycleLocalStorage("repo-1", "/srv/snapshot-dependency-test")
	current.Generation = 1
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &current}, Result: protocol.Result{OperationID: "register-1", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Disks[0].StorageID = current.ID
	command, snapshot, reservation := managedRecoveryEntryForTest(vm, "snapshot-1")
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(vm)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Dependencies = &dependencies
	reservation.IntentSHA256, err = managedSnapshotIntentDigest(command, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: command, Snapshot: &snapshot, Result: protocol.Result{OperationID: command.OperationID, Status: "succeeded"}, Stage: "defined",
		RecoveryReservation: &reservation, RecoveryStage: "completed", RecoveryOutcome: "completed", RecoverySettled: true}); err != nil {
		t.Fatal(err)
	}
	updated := vm
	updated.Revision++
	updated.Disks = []protocol.VMDisk{{StorageID: "repo-elsewhere", Name: "system", SizeGiB: 20, Bus: "virtio", BootOrder: 2}}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "vm-edit", Kind: "vm-update", VM: &updated}, Result: protocol.Result{OperationID: "vm-edit", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	next := protocol.StorageRegistration{ID: "repo-1", PoolID: current.PoolID, HostID: current.HostID, Name: current.Name,
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}}
	setForTest(t, &nativeRun, func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("unexpected NFS mount effect")
	})
	request := protocol.StorageLifecycleRequest{Action: "update", RepositoryID: current.ID, PoolID: current.PoolID, HostID: current.HostID, ExpectedGeneration: 1, Registration: &next}
	result, err := e.Execute(context.Background(), lifecycleCommand("move-snapshot-repo", "storage-update", request), nil)
	if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "cannot update while used") {
		t.Fatalf("storage source changed underneath a retained snapshot: %+v, %v", result, err)
	}
}

// BUG-0354: storageDependencies scanned every succeeded iso-import entry for the repository
// and never consulted the media tombstones, so one ISO imported onto a repository blocked its
// detach for ever — removing the ISO first did not help. The wiped-disk half of the same
// function already had this fixed (BUG-0222); the ISO half did not.
func TestRemovedISOIsNotAStorageDependency(t *testing.T) {
	repo := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Name: "shared", Path: "/srv/shared",
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1}
	media := lifecycleMedia("iso-1", "pool-1", "host-1", repo.ID)
	js := []managedJournal{
		{Command: protocol.Command{OperationID: "reg", Kind: "storage-register", Storage: &repo}, Result: protocol.Result{OperationID: "reg", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "imp", Kind: "iso-import", Media: &media}, Result: protocol.Result{OperationID: "imp", Status: "succeeded"}},
	}
	if got := storageDependencies(js, repo.ID); len(got) != 1 || got[0] != "ISO iso-1" {
		t.Fatalf("an imported ISO must block detach: %v", got)
	}
	js = append(js, managedJournal{
		Command: protocol.Command{OperationID: "rm", Kind: "media-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
			Action: "remove", Resource: "media", ResourceID: media.ID, PoolID: "pool-1", HostID: "host-1", ExpectedGeneration: 1}},
		Result: protocol.Result{OperationID: "rm", Status: "succeeded"},
	})
	if got := storageDependencies(js, repo.ID); len(got) != 0 {
		t.Fatalf("a removed ISO still blocks detach: %v", got)
	}
}

// The dry run and the real refusal are one function, as CheckNetworkRemoval and the network
// teardown are: a leave that prechecks must not be able to drift from the detach it precedes.
func TestCheckStorageDetachReportsTheDependencyWithoutJournalling(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	e := NewManaged(filepath.Join(root, "state", "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	repo := lifecycleLocalStorage("repo-1", filepath.Join(root, "srv"))
	if err := os.MkdirAll(repo.Path, 0700); err != nil {
		t.Fatal(err)
	}
	media := lifecycleMedia("iso-1", repo.PoolID, repo.HostID, repo.ID)
	for _, j := range []managedJournal{
		{Command: protocol.Command{OperationID: "reg", Kind: "storage-register", Storage: &repo}, Result: protocol.Result{OperationID: "reg", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "imp", Kind: "iso-import", Media: &media}, Result: protocol.Result{OperationID: "imp", Status: "succeeded"}},
	} {
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadDir(e.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	err = e.CheckStorageDetach(ctx, repo.ID)
	if err == nil || !strings.Contains(err.Error(), "storage cannot detach while used by ISO iso-1") {
		t.Fatalf("CheckStorageDetach = %v", err)
	}
	after, err := os.ReadDir(e.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("the precheck journalled: %d entries before, %d after", len(before), len(after))
	}
	// The real detach refuses in exactly the same words, so the two cannot drift.
	result, err := e.Execute(ctx, lifecycleCommand("det", "storage-detach", protocol.StorageLifecycleRequest{
		Action: "detach", RepositoryID: repo.ID, PoolID: repo.PoolID, HostID: repo.HostID, ExpectedGeneration: 1}), nil)
	if err != nil || result.Status == "succeeded" {
		t.Fatalf("detach with an ISO on it: %+v %v", result, err)
	}
	if result.Error != err2s(e.CheckStorageDetach(ctx, repo.ID)) {
		t.Fatalf("precheck %q and detach %q disagree", err2s(e.CheckStorageDetach(ctx, repo.ID)), result.Error)
	}
}

func err2s(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A refused registration must not leave its plaintext credential behind until a client retires it.
func TestFailedStorageRegistrationReleasesCredential(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	e := NewManaged(filepath.Join(state, "managed"))
	handoff := testCredentialHandoff()
	receipt, err := SaveStorageCredential(state, handoff)
	if err != nil {
		t.Fatal(err)
	}
	s := lifecycleLocalStorage("repo-1", e.stateDir)
	s.CredentialReceipt, s.CredentialRequestID = receipt.Receipt, handoff.Offer.RegistrationRequestID
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := e.Execute(context.Background(), protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &s}, nil)
	if err != nil || result.Status != "failed" {
		t.Fatalf("registration = %+v, %v", result, err)
	}
	if _, err = os.Stat(filepath.Join(state, credentialDirectory, receipt.Receipt)); !os.IsNotExist(err) {
		t.Fatalf("credential remains after a failed registration: %v", err)
	}
	if _, err = SaveStorageCredential(state, handoff); err == nil {
		t.Fatal("failed browser registration handoff was reusable after retirement")
	}
}

// Native preparation is simulated; credential cleanup, replay admission and the
// managed storage projection exercise the durable member registration boundary.
func TestPoolStorageCredentialRetryAfterFailedPreparation(t *testing.T) {
	for _, retire := range []bool{false, true} {
		name := "recover"
		if retire {
			name = "explicit-retirement"
		}
		t.Run(name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			e := NewManaged(filepath.Join(state, "managed"))
			path := t.TempDir()
			const generation = 7
			s := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "member-1", Generation: generation, Kind: protocol.StorageKindSMB, Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: "files.example.test", Share: "vms"}}}
			credentials := testCredentialHandoff().Credentials
			receipt, err := SavePoolStorageCredential(state, s.PoolID, s.HostID, s.ID, generation, credentials)
			if err != nil {
				t.Fatal(err)
			}
			s.CredentialReceipt, s.CredentialRequestID = receipt, PoolStorageCredentialRequestID(s.ID, generation)
			identity := protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: "files.example.test", Share: "vms", MarkerID: "0123456789abcdef0123456789abcdef"}}
			access := storageAccess{Path: path, Observed: identity, Health: "healthy", Available: 2 << 30}
			oldPrepare, oldReady := storagePrepareAtFn, managedStorageReadyFn
			t.Cleanup(func() { storagePrepareAtFn, managedStorageReadyFn = oldPrepare, oldReady })
			serverAvailable := false
			storagePrepareAtFn = func(context.Context, protocol.StorageRegistration, string) (storageAccess, error) {
				got, err := PoolStorageCredential(state, receipt)
				if err != nil || got != credentials {
					t.Fatal("member preparation could not read the exact delivered credential")
				}
				if !serverAvailable {
					return storageAccess{}, errors.New("SMB server temporarily unavailable")
				}
				return access, nil
			}
			managedStorageReadyFn = func(context.Context, protocol.StorageRegistration, uint64) (storageAccess, error) {
				return access, nil
			}
			ctx := WithPoolStorageExpectation(context.Background(), identity, generation)
			command := protocol.Command{OperationID: "member-register-first", Kind: "storage-register", Storage: &s}
			result, err := e.Execute(ctx, command, nil)
			if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "temporarily unavailable") {
				t.Fatalf("first preparation = %+v, %v", result, err)
			}
			if _, err = os.Stat(filepath.Join(state, credentialDirectory, receipt)); !os.IsNotExist(err) {
				t.Fatalf("failed preparation left a member secret: %v", err)
			}
			if retire {
				command := protocol.Command{OperationID: "retire-member-handoff", Kind: "storage-credential-retire", CredentialRetire: &protocol.StorageCredentialRetireRequest{Receipt: receipt, PoolID: s.PoolID, HostID: s.HostID}}
				if result, err = e.Execute(ctx, command, nil); err != nil || result.Status != "succeeded" {
					t.Fatalf("explicit retirement = %+v, %v", result, err)
				}
				if _, err = SavePoolStorageCredential(state, s.PoolID, s.HostID, s.ID, generation, credentials); err == nil {
					t.Fatal("explicitly retired peer handoff was recreated")
				}
				return
			}
			changed := credentials
			changed.Secret += "-changed"
			if _, err = SavePoolStorageCredential(state, s.PoolID, s.HostID, s.ID, generation, changed); err == nil {
				t.Fatal("same generation accepted different credentials")
			}
			replayed, err := SavePoolStorageCredential(state, s.PoolID, s.HostID, s.ID, generation, credentials)
			if err != nil || replayed != receipt {
				t.Fatalf("same-generation handoff retry = %q, %v", replayed, err)
			}
			serverAvailable = true
			command.OperationID = "member-register-retry"
			if result, err = e.Execute(ctx, command, nil); err != nil || result.Status != "succeeded" {
				t.Fatalf("recovered preparation = %+v, %v", result, err)
			}
			applied, checks := e.AppliedStorage()
			if len(applied) != 1 || applied[0].ID != s.ID || applied[0].Generation != generation || applied[0].CredentialReceipt != receipt || !applied[0].CredentialPresent {
				t.Fatalf("recovered member projection = %+v, checks = %+v", applied, checks)
			}
		})
	}
}
