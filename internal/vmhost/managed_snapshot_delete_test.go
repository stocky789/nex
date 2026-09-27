// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func runSnapshotDeleteExecutorStageTest(t *testing.T, nativeAlready bool) (*ManagedEngine, protocol.Command, string) {
	t.Helper()
	root := t.TempDir()
	e := NewManaged(filepath.Join(root, "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	vmDir := filepath.Join(root, "storage", "nex-vms", vm.ID)
	snapshotDir := filepath.Join(vmDir, "snapshots")
	if err := os.MkdirAll(snapshotDir, 0700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(vmDir, "base.qcow2")
	overlay := filepath.Join(snapshotDir, "snapshot-target-vda.qcow2")
	for _, path := range []string{base, overlay} {
		if err := os.WriteFile(path, []byte("fixture image"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	storageRoot := filepath.Join(root, "storage")
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Name: "disk", Path: storageRoot, Kind: protocol.StorageKindLocal, Generation: 1}
	access := storageAccess{Path: storageRoot, Identity: "local:" + storageRoot + ":fsid=test-fsid", FilesystemID: "test-fsid", Total: 1 << 30, Available: 1 << 30, Health: "healthy"}
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, got protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		if got.ID != storage.ID || got.Path != storage.Path {
			return storageAccess{}, errors.New("unexpected storage registration")
		}
		return access, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	identity, err := snapshotStorageIdentityDigest(storage, access)
	if err != nil {
		t.Fatal(err)
	}
	targets, _, _ := managedTargets(vm)
	domainXML, err := testDomainXML(vm, nil, []string{overlay}, "")
	if err != nil {
		t.Fatal(err)
	}
	afterXML, err := testDomainXML(vm, nil, []string{base}, "")
	if err != nil {
		t.Fatal(err)
	}
	definitionHash, err := snapshotDefinitionDigest(domainXML)
	if err != nil {
		t.Fatal(err)
	}
	captureCommand := protocol.Command{OperationID: "snapshot-target", Kind: "snapshot-create", VM: &vm, Snapshot: &protocol.VMSnapshotRequest{Name: "leaf point"}}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(vm)
	if err != nil {
		t.Fatal(err)
	}
	capture := managedSnapshotRecord{
		SnapshotID: captureCommand.OperationID, Name: captureCommand.Snapshot.Name, CreatedAt: time.Now().UTC(),
		BeforeDomain: afterXML, Before: managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: mustSnapshotDefinitionDigest(t, afterXML),
			Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: base, Layers: []string{base}}}},
		ExpectedAfter: managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: definitionHash,
			Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: overlay, Layers: []string{overlay, base}}}},
		StorageDependencies: []managedSnapshotStorageDependency{{StorageID: storage.ID, Generation: 1, IdentitySHA256: identity, SnapshotArtifactPath: snapshotDir}},
		Dependencies:        &dependencies,
	}
	capture.ObservedAfter = cloneSnapshotObservation(&capture.ExpectedAfter)
	creationTime := strconv.FormatInt(capture.CreatedAt.Unix(), 10)
	domainInner := string(afterXML[len(`<domain type="kvm">`) : len(afterXML)-len(`</domain>`)])
	_, mediaTargetName, driversTargetName := managedTargets(vm)
	snapshotXML := []byte(`<domainsnapshot><name>nex-snapshot-target</name><description>leaf</description><state>shutoff</state><creationTime>` + creationTime + `</creationTime><memory snapshot="no"/><disks>` +
		`<disk name="` + targets[0] + `" snapshot="external"><driver type="qcow2"/><source file="` + overlay + `"/></disk>` +
		`<disk name="` + mediaTargetName + `" snapshot="no"/>` +
		`<disk name="` + driversTargetName + `" snapshot="no"/>` +
		`</disks><domain type="kvm">` + domainInner + `</domain></domainsnapshot>`)
	snapshotXMLPath := filepath.Join(root, "target-snapshot.xml")
	if err := os.WriteFile(snapshotXMLPath, snapshotXML, 0600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "native-deleted")
	logPath := filepath.Join(root, "virsh.log")
	t.Setenv("VMHOST_DELETE_STATE", statePath)
	t.Setenv("VMHOST_DELETE_LOG", logPath)
	t.Setenv("VMHOST_DELETE_SNAPSHOT_XML", snapshotXMLPath)
	t.Setenv("VMHOST_DELETE_BEFORE_XML", string(domainXML))
	t.Setenv("VMHOST_DELETE_AFTER_XML", string(afterXML))
	t.Setenv("VMHOST_DELETE_BASE", base)
	t.Setenv("VMHOST_DELETE_OVERLAY", overlay)
	t.Setenv("VMHOST_DELETE_TARGET", "nex-snapshot-target")
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
dominfo) echo 'Autostart: disable';;
dumpxml)
  if [ -f "$VMHOST_DELETE_STATE" ] || [ -f "$VMHOST_DELETE_COMMITTED" ]; then
    printf '%s' "$VMHOST_DELETE_AFTER_XML"
  else
    printf '%s' "$VMHOST_DELETE_BEFORE_XML"
  fi;;
define)
  echo define >> "$VMHOST_DELETE_LOG"
  touch "$VMHOST_DELETE_COMMITTED"
  ;;
snapshot-current) if [ -f "$VMHOST_DELETE_STATE" ]; then echo ''; else echo "$VMHOST_DELETE_TARGET"; fi;;
snapshot-list) if [ -f "$VMHOST_DELETE_STATE" ]; then exit 0; else echo "$VMHOST_DELETE_TARGET"; fi;;
snapshot-dumpxml) if [ -f "$VMHOST_DELETE_STATE" ]; then exit 1; else cat "$VMHOST_DELETE_SNAPSHOT_XML"; fi;;
snapshot-delete)
  echo snapshot-delete >> "$VMHOST_DELETE_LOG"
  echo "$*" >> "$VMHOST_DELETE_LOG"
  case "$*" in *--metadata*) ;; *) exit 2;; esac
  touch "$VMHOST_DELETE_STATE"
  ;;
list) ;;
*) exit 2;;
esac`)
	t.Setenv("VMHOST_DELETE_COMMITTED", filepath.Join(root, "native-committed"))
	fakeCommand(t, "qemu-img", `if [ "$1" = commit ]; then
  echo commit >> "$VMHOST_DELETE_LOG"
  echo "$*" >> "$VMHOST_DELETE_LOG"
  touch "$VMHOST_DELETE_QEMU_COMMIT"
  exit 0
fi
if [ "$1" = check ]; then exit 0; fi
if [ "$1" = info ] && [ "$3" = --backing-chain ]; then
  head="$VMHOST_DELETE_OVERLAY"
  if [ -f "$VMHOST_DELETE_STATE" ] || [ -f "$VMHOST_DELETE_COMMITTED" ]; then head="$VMHOST_DELETE_BASE"; fi
  if [ "$2" = "$VMHOST_DELETE_BASE" ] || [ "$head" = "$VMHOST_DELETE_BASE" ]; then
    printf '[{"filename":"%s","format":"qcow2"}]\n' "$VMHOST_DELETE_BASE"
  else
    printf '[{"filename":"%s","format":"qcow2","backing-filename":"%s","full-backing-filename":"%s"},{"filename":"%s","format":"qcow2"}]\n' "$VMHOST_DELETE_OVERLAY" "$VMHOST_DELETE_BASE" "$VMHOST_DELETE_BASE" "$VMHOST_DELETE_BASE"
  fi
  exit 0
fi
if [ "$1" = info ]; then printf '{"format":"qcow2"}\n'; exit 0; fi
exit 2`)
	t.Setenv("VMHOST_DELETE_QEMU_COMMIT", filepath.Join(root, "native-qemu-commit"))
	capture.NativeXMLSHA256, err = managedSnapshotXMLDigest(context.Background(), vm.ID, managedSnapshotNativeName(captureCommand.OperationID), capture)
	if err != nil {
		t.Fatal(err)
	}
	createIntent, err := managedSnapshotIntentDigest(captureCommand, capture)
	if err != nil {
		t.Fatal(err)
	}
	_, _, createReservation := managedRecoveryEntryForTest(vm, captureCommand.OperationID)
	createReservation.IntentSHA256 = createIntent
	createReservation.SpecRevision = vm.Revision
	if err := validateManagedSnapshotReservation(captureCommand, capture, createReservation); err != nil {
		t.Fatal(err)
	}
	descriptor, err := managedSnapshotDescriptor(captureCommand, createReservation, capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage-seed", Kind: "storage-register", Storage: &storage}, AppliedStorage: &storage,
		Result: protocol.Result{OperationID: "storage-seed", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.saveSnapshotJournal(managedJournal{Command: captureCommand, Snapshot: &capture, RecoveryReservation: &createReservation,
		RecoveryStage: "completed", RecoveryOutcome: "completed", RecoverySettled: true,
		Result: protocol.Result{OperationID: captureCommand.OperationID, Status: "succeeded"}, Stage: "snapshot-completed",
		Disks: []string{overlay}, DiskChains: [][]string{{overlay, base}}, Targets: targets, Domain: domainXML}); err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "current-create", Kind: "vm-create", VM: &vm},
		Result: protocol.Result{OperationID: "current-create", Status: "succeeded"}, Stage: "defined", Disks: []string{overlay},
		DiskChains: [][]string{{overlay, base}}, Targets: targets, Domain: domainXML}); err != nil {
		t.Fatal(err)
	}
	if nativeAlready {
		if err := os.WriteFile(statePath, []byte("deleted"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "native-committed"), []byte("committed"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(overlay); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	deleteCommand := protocol.Command{OperationID: "snapshot-delete-op", Kind: "snapshot-delete", VM: &vm,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: descriptor.ID, Target: &descriptor}}
	targetDigest, err := protocol.PoolVMSnapshotDescriptorSHA256(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	deleteRecord := managedSnapshotDeleteRecord{
		TargetSnapshotID: descriptor.ID, TargetSnapshotSHA256: targetDigest,
		TargetNativeXMLSHA256: capture.NativeXMLSHA256,
		TargetNativeIdentity:  &managedSnapshotNativeIdentity{Description: "leaf", CreationTime: creationTime},
		CurrentSnapshotBefore: "nex-snapshot-target", WasCurrent: true, CreatedAt: time.Now().UTC(),
		BeforeDomain: domainXML, Before: *capture.ObservedAfter,
		TargetOverlays: []managedSnapshotDeleteOverlay{{Target: targets[0], Path: overlay, Parent: base}},
		ParentSources:  []managedSnapshotDeleteOverlay{{Target: targets[0], Path: base}},
	}
	if nativeAlready {
		deleteRecord.MetadataDeleted = true
		deleteRecord.UnlinkedPaths = []string{overlay}
	}
	intent, err := managedSnapshotDeleteIntentDigest(deleteCommand, deleteRecord)
	if err != nil {
		t.Fatal(err)
	}
	specJSON, _ := json.Marshal(deleteCommand.VM)
	diskJSON, _ := json.Marshal(deleteCommand.VM.Disks)
	specHash, diskHash := sha256.Sum256(specJSON), sha256.Sum256(diskJSON)
	reservation := protocol.PoolVMRecoveryReservation{
		OperationID: deleteCommand.OperationID, Kind: "snapshot-delete", OwnerOperationOrder: createReservation.OwnerOperationOrder + 1,
		PoolID: vm.PoolID, VMID: vm.ID, OwnerHostID: vm.HostID, Epoch: 1, SpecRevision: vm.Revision,
		SpecSHA256: hex.EncodeToString(specHash[:]), DiskIdentitySHA256: hex.EncodeToString(diskHash[:]), IntentSHA256: intent,
		TargetSnapshotID: descriptor.ID, TargetSnapshotSHA256: targetDigest,
	}
	if err := validateManagedSnapshotDeleteReservation(deleteCommand, deleteRecord, reservation); err != nil {
		t.Fatal(err)
	}
	stage := "reserved"
	if nativeAlready {
		stage = "native"
	}
	seed := managedJournal{
		Command: deleteCommand, SnapshotDelete: &deleteRecord, RecoveryReservation: &reservation,
		RecoveryStage: stage, Stage: "snapshot-delete-" + stage, Result: protocol.Result{OperationID: deleteCommand.OperationID, Status: "pending"},
		Disks: []string{overlay}, DiskChains: [][]string{{overlay, base}}, Targets: targets, Domain: domainXML,
	}
	if nativeAlready {
		seed.Disks, seed.DiskChains, seed.Domain = []string{base}, [][]string{{base}}, afterXML
	}
	if err := e.saveSnapshotJournal(seed); err != nil {
		t.Fatal(err)
	}
	reserveCalls, settleCalls := 0, 0
	reserve := func(ctx context.Context, command protocol.Command, intentDigest string) (protocol.PoolVMRecoveryReservation, error) {
		reserveCalls++
		if intentDigest != intent {
			t.Fatalf("reserve intent=%s want %s", intentDigest, intent)
		}
		return reservation, nil
	}
	settle := func(ctx context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
		settleCalls++
		if evidence.Reservation != reservation || evidence.Outcome != "completed" || evidence.Snapshot != nil || evidence.ResultSpec != nil {
			t.Fatalf("settle evidence=%#v", evidence)
		}
		return nil
	}
	result, err := e.DeleteStoppedSnapshot(context.Background(), deleteCommand, reserve, settle)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("delete=%+v err=%v", result, err)
	}
	replay, err := e.DeleteStoppedSnapshot(context.Background(), deleteCommand, reserve, settle)
	if err != nil || replay.Status != "succeeded" || settleCalls != 1 || reserveCalls != 0 {
		t.Fatalf("settled snapshot-delete replay=%+v reserve=%d settle=%d err=%v", replay, reserveCalls, settleCalls, err)
	}
	raw, readErr := os.ReadFile(logPath)
	wantNative := 1
	if nativeAlready {
		wantNative = 0
	}
	gotDelete := 0
	gotCommit := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "snapshot-delete" {
			gotDelete++
		}
		if line == "commit" {
			gotCommit++
		}
	}
	if gotDelete != wantNative || readErr != nil && wantNative != 0 {
		t.Fatalf("native snapshot-delete calls=%d, want %d; output=%q err=%v", gotDelete, wantNative, raw, readErr)
	}
	if !nativeAlready {
		if gotCommit != 1 {
			t.Fatalf("native qemu-img commit calls=%d, want 1; output=%q", gotCommit, raw)
		}
		if strings.Contains(string(raw), "--delete") {
			t.Fatalf("consolidation used --delete: %q", raw)
		}
	}
	if _, err := os.Lstat(overlay); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlay still present after leaf delete: %v", err)
	}
	return e, deleteCommand, logPath
}

func TestSnapshotDeleteNativeStageReconcilesWithoutRepeating(t *testing.T) {
	runSnapshotDeleteExecutorStageTest(t, true)
}

func TestSnapshotDeleteReservedStageRunsNativeOnceAndSettles(t *testing.T) {
	runSnapshotDeleteExecutorStageTest(t, false)
}

func TestSnapshotDeleteRefusesNativeChildrenBeforeEffects(t *testing.T) {
	vm := managedTestVM()
	fakeCommand(t, "virsh", `case "$3" in
snapshot-list) printf 'nex-parent-point\nnex-child-point\n';;
snapshot-dumpxml)
  case "$*" in
  *nex-child-point*) printf '<domainsnapshot><name>nex-child-point</name><parent><name>nex-parent-point</name></parent></domainsnapshot>';;
  *) printf '<domainsnapshot><name>nex-parent-point</name></domainsnapshot>';;
  esac;;
*) exit 2;;
esac`)
	if err := refuseSnapshotDeleteWithChildren(context.Background(), vm.ID, "nex-parent-point"); err == nil || !strings.Contains(err.Error(), "native children") {
		t.Fatalf("parent with children was accepted: %v", err)
	}
}

func TestSnapshotDeleteRedefineAlreadyDoneResumesWithoutCommit(t *testing.T) {
	root := t.TempDir()
	e := NewManaged(filepath.Join(root, "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	vmDir := filepath.Join(root, "storage", "nex-vms", vm.ID)
	snapshotDir := filepath.Join(vmDir, "snapshots")
	if err := os.MkdirAll(snapshotDir, 0700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(vmDir, "base.qcow2")
	overlay := filepath.Join(snapshotDir, "snapshot-target-vda.qcow2")
	for _, path := range []string{base, overlay} {
		if err := os.WriteFile(path, []byte("fixture image"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	storageRoot := filepath.Join(root, "storage")
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Name: "disk", Path: storageRoot, Kind: protocol.StorageKindLocal, Generation: 1}
	access := storageAccess{Path: storageRoot, Identity: "local:" + storageRoot + ":fsid=test-fsid", FilesystemID: "test-fsid", Total: 1 << 30, Available: 1 << 30, Health: "healthy"}
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, got protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		if got.ID != storage.ID || got.Path != storage.Path {
			return storageAccess{}, errors.New("unexpected storage registration")
		}
		return access, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	identity, err := snapshotStorageIdentityDigest(storage, access)
	if err != nil {
		t.Fatal(err)
	}
	targets, _, _ := managedTargets(vm)
	domainXML, err := testDomainXML(vm, nil, []string{overlay}, "")
	if err != nil {
		t.Fatal(err)
	}
	afterXML, err := testDomainXML(vm, nil, []string{base}, "")
	if err != nil {
		t.Fatal(err)
	}
	definitionHash, err := snapshotDefinitionDigest(domainXML)
	if err != nil {
		t.Fatal(err)
	}
	captureCommand := protocol.Command{OperationID: "snapshot-target", Kind: "snapshot-create", VM: &vm, Snapshot: &protocol.VMSnapshotRequest{Name: "leaf point"}}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(vm)
	if err != nil {
		t.Fatal(err)
	}
	capture := managedSnapshotRecord{
		SnapshotID: captureCommand.OperationID, Name: captureCommand.Snapshot.Name, CreatedAt: time.Now().UTC(),
		BeforeDomain: afterXML, Before: managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: mustSnapshotDefinitionDigest(t, afterXML),
			Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: base, Layers: []string{base}}}},
		ExpectedAfter: managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: definitionHash,
			Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: overlay, Layers: []string{overlay, base}}}},
		StorageDependencies: []managedSnapshotStorageDependency{{StorageID: storage.ID, Generation: 1, IdentitySHA256: identity, SnapshotArtifactPath: snapshotDir}},
		Dependencies:        &dependencies,
	}
	capture.ObservedAfter = cloneSnapshotObservation(&capture.ExpectedAfter)
	creationTime := strconv.FormatInt(capture.CreatedAt.Unix(), 10)
	domainInner := string(afterXML[len(`<domain type="kvm">`) : len(afterXML)-len(`</domain>`)])
	_, mediaTargetName, driversTargetName := managedTargets(vm)
	snapshotXML := []byte(`<domainsnapshot><name>nex-snapshot-target</name><description>leaf</description><state>shutoff</state><creationTime>` + creationTime + `</creationTime><memory snapshot="no"/><disks>` +
		`<disk name="` + targets[0] + `" snapshot="external"><driver type="qcow2"/><source file="` + overlay + `"/></disk>` +
		`<disk name="` + mediaTargetName + `" snapshot="no"/>` +
		`<disk name="` + driversTargetName + `" snapshot="no"/>` +
		`</disks><domain type="kvm">` + domainInner + `</domain></domainsnapshot>`)
	snapshotXMLPath := filepath.Join(root, "target-snapshot.xml")
	if err := os.WriteFile(snapshotXMLPath, snapshotXML, 0600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "native-deleted")
	logPath := filepath.Join(root, "virsh.log")
	committedPath := filepath.Join(root, "native-committed")
	t.Setenv("VMHOST_DELETE_STATE", statePath)
	t.Setenv("VMHOST_DELETE_LOG", logPath)
	t.Setenv("VMHOST_DELETE_SNAPSHOT_XML", snapshotXMLPath)
	t.Setenv("VMHOST_DELETE_BEFORE_XML", string(domainXML))
	t.Setenv("VMHOST_DELETE_AFTER_XML", string(afterXML))
	t.Setenv("VMHOST_DELETE_BASE", base)
	t.Setenv("VMHOST_DELETE_OVERLAY", overlay)
	t.Setenv("VMHOST_DELETE_TARGET", "nex-snapshot-target")
	t.Setenv("VMHOST_DELETE_COMMITTED", committedPath)
	// Redefine already applied: inactive XML is on parents, metadata still present.
	if err := os.WriteFile(committedPath, []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
dominfo) echo 'Autostart: disable';;
dumpxml)
  if [ -f "$VMHOST_DELETE_STATE" ] || [ -f "$VMHOST_DELETE_COMMITTED" ]; then
    printf '%s' "$VMHOST_DELETE_AFTER_XML"
  else
    printf '%s' "$VMHOST_DELETE_BEFORE_XML"
  fi;;
define)
  echo define >> "$VMHOST_DELETE_LOG"
  touch "$VMHOST_DELETE_COMMITTED"
  ;;
snapshot-current) if [ -f "$VMHOST_DELETE_STATE" ]; then echo ''; else echo "$VMHOST_DELETE_TARGET"; fi;;
snapshot-list) if [ -f "$VMHOST_DELETE_STATE" ]; then exit 0; else echo "$VMHOST_DELETE_TARGET"; fi;;
snapshot-dumpxml) if [ -f "$VMHOST_DELETE_STATE" ]; then exit 1; else cat "$VMHOST_DELETE_SNAPSHOT_XML"; fi;;
snapshot-delete)
  echo snapshot-delete >> "$VMHOST_DELETE_LOG"
  echo "$*" >> "$VMHOST_DELETE_LOG"
  case "$*" in *--metadata*) ;; *) exit 2;; esac
  touch "$VMHOST_DELETE_STATE"
  ;;
list) ;;
*) exit 2;;
esac`)
	t.Setenv("VMHOST_DELETE_QEMU_COMMIT", filepath.Join(root, "native-qemu-commit"))
	fakeCommand(t, "qemu-img", `if [ "$1" = commit ]; then
  echo commit >> "$VMHOST_DELETE_LOG"
  echo "$*" >> "$VMHOST_DELETE_LOG"
  touch "$VMHOST_DELETE_QEMU_COMMIT"
  exit 0
fi
if [ "$1" = check ]; then exit 0; fi
if [ "$1" = info ] && [ "$3" = --backing-chain ]; then
  head="$VMHOST_DELETE_OVERLAY"
  if [ -f "$VMHOST_DELETE_STATE" ] || [ -f "$VMHOST_DELETE_COMMITTED" ]; then head="$VMHOST_DELETE_BASE"; fi
  if [ "$2" = "$VMHOST_DELETE_BASE" ] || [ "$head" = "$VMHOST_DELETE_BASE" ]; then
    printf '[{"filename":"%s","format":"qcow2"}]\n' "$VMHOST_DELETE_BASE"
  else
    printf '[{"filename":"%s","format":"qcow2","backing-filename":"%s","full-backing-filename":"%s"},{"filename":"%s","format":"qcow2"}]\n' "$VMHOST_DELETE_OVERLAY" "$VMHOST_DELETE_BASE" "$VMHOST_DELETE_BASE" "$VMHOST_DELETE_BASE"
  fi
  exit 0
fi
if [ "$1" = info ]; then printf '{"format":"qcow2"}\n'; exit 0; fi
exit 2`)
	capture.NativeXMLSHA256, err = managedSnapshotXMLDigest(context.Background(), vm.ID, managedSnapshotNativeName(captureCommand.OperationID), capture)
	if err != nil {
		t.Fatal(err)
	}
	createIntent, err := managedSnapshotIntentDigest(captureCommand, capture)
	if err != nil {
		t.Fatal(err)
	}
	_, _, createReservation := managedRecoveryEntryForTest(vm, captureCommand.OperationID)
	createReservation.IntentSHA256 = createIntent
	createReservation.SpecRevision = vm.Revision
	if err := validateManagedSnapshotReservation(captureCommand, capture, createReservation); err != nil {
		t.Fatal(err)
	}
	descriptor, err := managedSnapshotDescriptor(captureCommand, createReservation, capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage-seed", Kind: "storage-register", Storage: &storage}, AppliedStorage: &storage,
		Result: protocol.Result{OperationID: "storage-seed", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.saveSnapshotJournal(managedJournal{Command: captureCommand, Snapshot: &capture, RecoveryReservation: &createReservation,
		RecoveryStage: "completed", RecoveryOutcome: "completed", RecoverySettled: true,
		Result: protocol.Result{OperationID: captureCommand.OperationID, Status: "succeeded"}, Stage: "snapshot-completed",
		Disks: []string{overlay}, DiskChains: [][]string{{overlay, base}}, Targets: targets, Domain: domainXML}); err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "current-create", Kind: "vm-create", VM: &vm},
		Result: protocol.Result{OperationID: "current-create", Status: "succeeded"}, Stage: "defined", Disks: []string{base},
		DiskChains: [][]string{{base}}, Targets: targets, Domain: afterXML}); err != nil {
		t.Fatal(err)
	}
	deleteCommand := protocol.Command{OperationID: "snapshot-delete-redefine-done", Kind: "snapshot-delete", VM: &vm,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: descriptor.ID, Target: &descriptor}}
	targetDigest, err := protocol.PoolVMSnapshotDescriptorSHA256(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	deleteRecord := managedSnapshotDeleteRecord{
		TargetSnapshotID: descriptor.ID, TargetSnapshotSHA256: targetDigest,
		TargetNativeXMLSHA256: capture.NativeXMLSHA256,
		TargetNativeIdentity:  &managedSnapshotNativeIdentity{Description: "leaf", CreationTime: creationTime},
		CurrentSnapshotBefore: "nex-snapshot-target", WasCurrent: true, CreatedAt: time.Now().UTC(),
		BeforeDomain: domainXML, Before: *capture.ObservedAfter,
		TargetOverlays: []managedSnapshotDeleteOverlay{{Target: targets[0], Path: overlay, Parent: base}},
		ParentSources:  []managedSnapshotDeleteOverlay{{Target: targets[0], Path: base}},
	}
	intent, err := managedSnapshotDeleteIntentDigest(deleteCommand, deleteRecord)
	if err != nil {
		t.Fatal(err)
	}
	specJSON, _ := json.Marshal(deleteCommand.VM)
	diskJSON, _ := json.Marshal(deleteCommand.VM.Disks)
	specHash, diskHash := sha256.Sum256(specJSON), sha256.Sum256(diskJSON)
	reservation := protocol.PoolVMRecoveryReservation{
		OperationID: deleteCommand.OperationID, Kind: "snapshot-delete", OwnerOperationOrder: createReservation.OwnerOperationOrder + 1,
		PoolID: vm.PoolID, VMID: vm.ID, OwnerHostID: vm.HostID, Epoch: 1, SpecRevision: vm.Revision,
		SpecSHA256: hex.EncodeToString(specHash[:]), DiskIdentitySHA256: hex.EncodeToString(diskHash[:]), IntentSHA256: intent,
		TargetSnapshotID: descriptor.ID, TargetSnapshotSHA256: targetDigest,
	}
	if err := e.saveSnapshotJournal(managedJournal{
		Command: deleteCommand, SnapshotDelete: &deleteRecord, RecoveryReservation: &reservation,
		RecoveryStage: "native", Stage: "snapshot-delete-native", Result: protocol.Result{OperationID: deleteCommand.OperationID, Status: "pending"},
		Disks: []string{base}, DiskChains: [][]string{{base}}, Targets: targets, Domain: afterXML,
	}); err != nil {
		t.Fatal(err)
	}
	settleCalls := 0
	result, err := e.DeleteStoppedSnapshot(context.Background(), deleteCommand,
		func(context.Context, protocol.Command, string) (protocol.PoolVMRecoveryReservation, error) {
			t.Fatal("redefine-done resume must not reserve again")
			return protocol.PoolVMRecoveryReservation{}, errors.New("unexpected reserve")
		},
		func(_ context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
			settleCalls++
			if evidence.Outcome != "completed" {
				t.Fatalf("settle evidence=%#v", evidence)
			}
			return nil
		})
	if err != nil || result.Status != "succeeded" || settleCalls != 1 {
		t.Fatalf("redefine-done resume result=%+v settle=%d err=%v", result, settleCalls, err)
	}
	raw, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	gotCommit, gotDefine, gotDelete := 0, 0, 0
	for _, line := range strings.Split(string(raw), "\n") {
		switch line {
		case "commit":
			gotCommit++
		case "define":
			gotDefine++
		case "snapshot-delete":
			gotDelete++
		}
	}
	if gotCommit != 0 {
		t.Fatalf("redefine-done resume repeated qemu-img commit: %q", raw)
	}
	if gotDefine != 0 {
		t.Fatalf("redefine-done resume repeated define: %q", raw)
	}
	if gotDelete != 1 {
		t.Fatalf("redefine-done resume snapshot-delete calls=%d; output=%q", gotDelete, raw)
	}
	if _, err := os.Lstat(overlay); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlay still present after redefine-done resume: %v", err)
	}
}

func TestSnapshotDeleteSettleFailedBeforeEffectsSetsFailedStatus(t *testing.T) {
	e, vm := NewManaged(filepath.Join(t.TempDir(), "managed")), managedTestVM()
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	target := protocol.PoolVMSnapshot{
		ID: "snap-delete-failed", VMID: vm.ID, Name: "point",
		CreatedAt: time.Now().UTC().Truncate(time.Second), SpecRevision: vm.Revision,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
	}
	command := protocol.Command{OperationID: "delete-failed-before", Kind: "snapshot-delete", VM: &vm,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: target.ID, Target: &target}}
	before := snapshotObservationFixture()
	rawDomain := snapshotBeforeDomainFixture()
	digest, err := protocol.PoolVMSnapshotDescriptorSHA256(target)
	if err != nil {
		t.Fatal(err)
	}
	record := managedSnapshotDeleteRecord{
		TargetSnapshotID: target.ID, TargetSnapshotSHA256: digest,
		TargetNativeXMLSHA256: strings.Repeat("d", 64),
		TargetNativeIdentity:  &managedSnapshotNativeIdentity{Description: "point", CreationTime: "1790407677"},
		CreatedAt: time.Now().UTC(), BeforeDomain: rawDomain, Before: before,
		TargetOverlays: []managedSnapshotDeleteOverlay{{Target: "vda", Path: "/srv/vm/snap.qcow2", Parent: "/srv/vm/disk.qcow2"}},
	}
	intent, err := managedSnapshotDeleteIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	specJSON, _ := json.Marshal(command.VM)
	diskJSON, _ := json.Marshal(command.VM.Disks)
	specHash, diskHash := sha256.Sum256(specJSON), sha256.Sum256(diskJSON)
	reservation := protocol.PoolVMRecoveryReservation{
		OperationID: command.OperationID, Kind: "snapshot-delete", OwnerOperationOrder: 1,
		PoolID: vm.PoolID, VMID: vm.ID, OwnerHostID: vm.HostID, Epoch: 1, SpecRevision: vm.Revision,
		SpecSHA256: hex.EncodeToString(specHash[:]), DiskIdentitySHA256: hex.EncodeToString(diskHash[:]), IntentSHA256: intent,
		TargetSnapshotID: target.ID, TargetSnapshotSHA256: digest,
	}
	if err := validateManagedSnapshotDeleteReservation(command, record, reservation); err != nil {
		t.Fatal(err)
	}
	j := managedJournal{
		Command: command, SnapshotDelete: &record, RecoveryReservation: &reservation,
		RecoveryStage: "failed-before-effects", RecoveryOutcome: "failed-before-effects",
		Stage: "snapshot-delete-failed-before-effects", Result: protocol.Result{OperationID: command.OperationID, Status: "pending"},
	}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VMHOST_DELETE_TARGET", managedSnapshotNativeName(target.ID))
	fakeCommand(t, "virsh", `case "$3" in
snapshot-list) echo "$VMHOST_DELETE_TARGET";;
snapshot-dumpxml) printf '<domainsnapshot><name>%s</name><creationTime>1</creationTime></domainsnapshot>' "$VMHOST_DELETE_TARGET";;
*) exit 2;;
esac`)
	settleCalls := 0
	result, err := e.settleSnapshotDelete(context.Background(), j, func(_ context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
		settleCalls++
		if evidence.Outcome != "failed-before-effects" || evidence.Reservation != reservation {
			t.Fatalf("settle evidence=%#v", evidence)
		}
		return nil
	})
	if err != nil || result.Status != "failed" || settleCalls != 1 {
		t.Fatalf("failed-before-effects settle result=%+v settle=%d err=%v", result, settleCalls, err)
	}
	settled, err := e.readJournal(command.OperationID + ".json")
	if err != nil || !settled.RecoverySettled || settled.Result.Status != "failed" {
		t.Fatalf("settled journal=%+v err=%v", settled, err)
	}
}

func TestValidateManagedSnapshotDeleteRecordAllowsEmptyDescription(t *testing.T) {
	vm := managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	target := protocol.PoolVMSnapshot{
		ID: "snap-empty-desc", VMID: vm.ID, Name: "point",
		CreatedAt: time.Now().UTC().Truncate(time.Second), SpecRevision: vm.Revision,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
	}
	command := protocol.Command{OperationID: "delete-empty-desc", Kind: "snapshot-delete", VM: &vm,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: target.ID, Target: &target}}
	before := snapshotObservationFixture()
	raw := snapshotBeforeDomainFixture()
	digest, err := protocol.PoolVMSnapshotDescriptorSHA256(target)
	if err != nil {
		t.Fatal(err)
	}
	record := managedSnapshotDeleteRecord{
		TargetSnapshotID: target.ID, TargetSnapshotSHA256: digest,
		TargetNativeXMLSHA256: strings.Repeat("d", 64),
		TargetNativeIdentity:  &managedSnapshotNativeIdentity{Description: "", CreationTime: "1790407677"},
		CreatedAt: time.Now().UTC(), BeforeDomain: raw, Before: before,
		TargetOverlays: []managedSnapshotDeleteOverlay{{Target: "vda", Path: "/srv/vm/snap.qcow2", Parent: "/srv/vm/disk.qcow2"}},
	}
	if err := validateManagedSnapshotDeleteRecord(command, record); err != nil {
		t.Fatalf("empty native description rejected: %v", err)
	}
	record.TargetNativeIdentity.CreationTime = ""
	if err := validateManagedSnapshotDeleteRecord(command, record); err == nil {
		t.Fatal("accepted missing creation time")
	}
}
