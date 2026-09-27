// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func managedSnapshotRevertFixture(t *testing.T) (*ManagedEngine, managedJournal, protocol.PoolVMSnapshot) {
	t.Helper()
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	targetID := "snapshot-target"
	createCommand, capture, createReservation := managedRecoveryEntryForTest(vm, targetID)
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(vm)
	if err != nil {
		t.Fatal(err)
	}
	capture.Dependencies = &dependencies
	createReservation.IntentSHA256, err = managedSnapshotIntentDigest(createCommand, capture)
	if err != nil {
		t.Fatal(err)
	}
	capture.ObservedAfter = cloneSnapshotObservation(&capture.ExpectedAfter)
	createJournal := managedJournal{
		Command: createCommand, Snapshot: &capture, RecoveryReservation: &createReservation,
		RecoveryStage: "completed", RecoveryOutcome: "completed", RecoverySettled: true,
		Result: protocol.Result{OperationID: targetID, Status: "succeeded"}, Stage: "snapshot-completed",
		Disks:      []string{capture.ExpectedAfter.Disks[0].Source},
		DiskChains: [][]string{append([]string(nil), capture.ExpectedAfter.Disks[0].Layers...)},
		Targets:    []string{capture.ExpectedAfter.Disks[0].Target}, Domain: append([]byte(nil), capture.BeforeDomain...),
	}
	if err := e.saveSnapshotJournal(createJournal); err != nil {
		t.Fatal(err)
	}
	descriptor, err := managedSnapshotDescriptor(createCommand, createReservation, capture)
	if err != nil {
		t.Fatal(err)
	}
	commandVM := vm
	commandVM.Name = "current-display-name"
	commandVM.Tags = append(commandVM.Tags, "keep-current-tag")
	commandVM.Revision++
	command := protocol.Command{
		OperationID: "snapshot-revert-op", Kind: "snapshot-revert", VM: &commandVM,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: targetID, Target: &descriptor},
	}
	resultSpec, err := protocol.PoolVMSnapshotRevertSpec(vm, commandVM)
	if err != nil {
		t.Fatal(err)
	}
	targetSHA, err := protocol.PoolVMSnapshotDescriptorSHA256(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	targetJournal, err := readSmall(filepath.Join(e.stateDir, targetID+".json"), managedSnapshotJournalLimit)
	if err != nil {
		t.Fatal(err)
	}
	targetJournalHash := sha256.Sum256(targetJournal)
	currentXML := append([]byte(nil), capture.BeforeDomain...)
	var currentDomain managedDomain
	if err := xml.Unmarshal(currentXML, &currentDomain); err != nil {
		t.Fatal(err)
	}
	currentDomain.Name = commandVM.Name
	currentDomain.Devices.Disks[0].Source.File = capture.ExpectedAfter.Disks[0].Source
	currentXML, err = xml.Marshal(currentDomain)
	if err != nil {
		t.Fatal(err)
	}
	currentObservation := *cloneSnapshotObservation(&capture.ExpectedAfter)
	currentObservation.DefinitionSHA256, err = snapshotDefinitionDigest(currentXML)
	if err != nil {
		t.Fatal(err)
	}
	currentTargets, currentMediaTarget, currentDriversTarget := managedTargets(commandVM)
	currentCommand := protocol.Command{OperationID: "snapshot-current-vm", Kind: "vm-update", VM: &commandVM}
	if err := e.save(managedJournal{
		Command: currentCommand, Result: protocol.Result{OperationID: currentCommand.OperationID, Status: "succeeded"}, Stage: "defined",
		Disks:      []string{capture.ExpectedAfter.Disks[0].Source},
		DiskChains: [][]string{append([]string(nil), capture.ExpectedAfter.Disks[0].Layers...)},
		Targets:    currentTargets, MediaTarget: currentMediaTarget, DriversTarget: currentDriversTarget, Domain: currentXML,
	}); err != nil {
		t.Fatal(err)
	}
	record := managedSnapshotRevertRecord{
		TargetSnapshotID: targetID, TargetSnapshotSHA256: targetSHA,
		TargetJournalSHA256:   hex.EncodeToString(targetJournalHash[:]),
		TargetNativeXMLSHA256: strings.Repeat("e", 64), CurrentSnapshotBefore: managedSnapshotNativeName(targetID),
		CreatedAt: time.Now().UTC(), CapturedSpec: vm, ResultSpec: resultSpec,
		BeforeDomain: currentXML, TargetDomain: append([]byte(nil), capture.BeforeDomain...),
		Before: currentObservation, TargetBefore: *cloneSnapshotObservation(&capture.Before),
		Target: *cloneSnapshotObservation(&capture.Before),
	}
	intent, err := managedSnapshotRevertIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	specJSON, _ := json.Marshal(&commandVM)
	diskJSON, _ := json.Marshal(commandVM.Disks)
	resultJSON, _ := json.Marshal(resultSpec)
	specHash, diskHash, resultHash := sha256.Sum256(specJSON), sha256.Sum256(diskJSON), sha256.Sum256(resultJSON)
	reservation := protocol.PoolVMRecoveryReservation{
		OperationID: command.OperationID, Kind: "snapshot-revert", OwnerOperationOrder: createReservation.OwnerOperationOrder + 1,
		PoolID: commandVM.PoolID, VMID: commandVM.ID, OwnerHostID: commandVM.HostID, Epoch: createReservation.Epoch,
		SpecRevision: commandVM.Revision, SpecSHA256: hex.EncodeToString(specHash[:]),
		DiskIdentitySHA256: hex.EncodeToString(diskHash[:]), IntentSHA256: intent,
		TargetSnapshotID: targetID, TargetSnapshotSHA256: targetSHA, ResultSpecSHA256: hex.EncodeToString(resultHash[:]),
	}
	if err := validateManagedSnapshotRevertReservation(command, record, reservation); err != nil {
		t.Fatal(err)
	}
	revertHead := filepath.Join(filepath.Dir(capture.Before.Disks[0].Source), "revert-head.qcow2")
	postXML := append([]byte(nil), capture.BeforeDomain...)
	var postDomain managedDomain
	if err := xml.Unmarshal(postXML, &postDomain); err != nil {
		t.Fatal(err)
	}
	postDomain.Name = resultSpec.Name
	postDomain.Devices.Disks[0].Source.File = revertHead
	postXML, err = xml.Marshal(postDomain)
	if err != nil {
		t.Fatal(err)
	}
	targets, mediaTarget, driversTarget := managedTargets(resultSpec)
	observed := *cloneSnapshotObservation(&capture.Before)
	observed.DefinitionSHA256, err = snapshotDefinitionDigest(postXML)
	if err != nil {
		t.Fatal(err)
	}
	observed.Disks[0].Source = revertHead
	observed.Disks[0].Layers = append([]string{revertHead}, observed.Disks[0].Layers...)
	record.ObservedAfter = &observed
	record.ObservedDomain = append([]byte(nil), postXML...)
	observedDisks, observedChains := snapshotObservationProjection(observed)
	return e, managedJournal{
		Command: command, SnapshotRevert: &record, RecoveryReservation: &reservation,
		RecoveryStage: "completed", RecoveryOutcome: "completed", Stage: "snapshot-revert-completed",
		Result:     protocol.Result{OperationID: command.OperationID, Status: "pending"},
		Disks:      observedDisks,
		DiskChains: observedChains,
		Targets:    targets, Domain: postXML,
		CurrentProjection: &managedSnapshotCurrentProjection{
			Spec: resultSpec, Disks: observedDisks,
			DiskChains: observedChains,
			Domain:     postXML, Targets: targets, MediaTarget: mediaTarget, DriversTarget: driversTarget,
		},
	}, descriptor
}

func TestSnapshotRevertFirmwareStagesMissingTargetAndResumesRename(t *testing.T) {
	root := t.TempDir()
	engine := &ManagedEngine{stateDir: filepath.Join(root, "state")}
	if err := os.Mkdir(engine.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := filepath.Join(root, "nvram.fd")
	target := filepath.Join(root, "saved-nvram.fd")
	temporary := source + ".revert.tmp"
	previous := source + ".revert.previous"
	if err := os.WriteFile(source, []byte("current firmware"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("saved firmware"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := managedSnapshotManifest(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	targetManifest, err := managedSnapshotManifest(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	record := managedSnapshotRevertRecord{Firmware: []managedSnapshotRevertFirmware{{
		Kind: "nvram", SourcePath: source, TargetPath: target, TemporaryPath: temporary, PreviousPath: previous,
		BeforeManifest: before, TargetManifest: targetManifest,
	}}}
	if err := engine.stageSnapshotRevertFirmware(ctx, &record); err != nil {
		t.Fatalf("stage target from an absent temp path: %v", err)
	}
	if err := os.Rename(source, previous); err != nil {
		t.Fatal(err)
	}
	if err := engine.applySnapshotRevertFirmware(ctx, &record); err != nil {
		t.Fatalf("resume after source-to-previous rename: %v", err)
	}
	got, err := managedSnapshotManifest(ctx, source)
	if err != nil || !snapshotManifestsEqual(got, targetManifest) {
		t.Fatalf("restored firmware manifest=%+v err=%v", got, err)
	}
	old, err := managedSnapshotManifest(ctx, previous)
	if err != nil || !snapshotManifestsEqual(old, before) {
		t.Fatalf("preserved previous firmware manifest=%+v err=%v", old, err)
	}
}

func TestSnapshotRevertFirmwareAcceptsAlreadyInstalledTarget(t *testing.T) {
	root := t.TempDir()
	engine := &ManagedEngine{stateDir: filepath.Join(root, "state")}
	ctx := context.Background()
	source := filepath.Join(root, "nvram.fd")
	target := filepath.Join(root, "saved-nvram.fd")
	if err := os.WriteFile(source, []byte("saved firmware"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("saved firmware"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := managedSnapshotManifest(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	record := managedSnapshotRevertRecord{Firmware: []managedSnapshotRevertFirmware{{
		Kind: "nvram", SourcePath: source, TargetPath: target,
		TemporaryPath: source + ".revert.tmp", PreviousPath: source + ".revert.previous",
		BeforeManifest: manifest, TargetManifest: manifest,
	}}}
	if err := engine.applySnapshotRevertFirmware(ctx, &record); err != nil {
		t.Fatalf("verify target already installed by native revert: %v", err)
	}
}

func TestSnapshotRevertCompletedJournalRequiresObservedProjection(t *testing.T) {
	e, completed, _ := managedSnapshotRevertFixture(t)
	t.Setenv("VMHOST_REVERT_VM_ID", completed.Command.VM.ID)
	fakeCommand(t, "virsh", `echo "$VMHOST_REVERT_VM_ID"`)
	path := filepath.Join(e.stateDir, completed.Command.OperationID+".json")
	if err := e.saveSnapshotJournal(completed); err != nil {
		t.Fatal(err)
	}
	_, err := e.readJournal(filepath.Base(path))
	if err != nil {
		t.Fatalf("read complete revert journal: %v", err)
	}
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	current, err := managedCurrent(js, completed.Command.VM.ID)
	wantDisks, wantChains := snapshotObservationProjection(*completed.SnapshotRevert.ObservedAfter)
	if err != nil || current.Command.VM == nil || current.Command.VM.Revision != completed.Command.VM.Revision ||
		!reflect.DeepEqual(current.Disks, []string{completed.SnapshotRevert.Before.Disks[0].Source}) {
		t.Fatalf("unsettled revert leaked its result projection: current=%+v err=%v", current, err)
	}
	state, _ := e.Applied(context.Background())
	if len(state.VMs) != 1 || state.VMs[0].Revision != completed.Command.VM.Revision {
		t.Fatalf("unsettled revert leaked into owner inventory: %+v", state.VMs)
	}

	completed.RecoverySettled = true
	completed.Result.Status = "succeeded"
	if err := e.saveSnapshotJournal(completed); err != nil {
		t.Fatal(err)
	}
	js, err = e.journals()
	if err != nil {
		t.Fatal(err)
	}
	current, err = managedCurrent(js, completed.Command.VM.ID)
	if err != nil || current.Command.VM == nil || !reflect.DeepEqual(*current.Command.VM, completed.SnapshotRevert.ResultSpec) ||
		!reflect.DeepEqual(current.Disks, wantDisks) || !reflect.DeepEqual(current.DiskChains, wantChains) {
		t.Fatalf("settled revert projection differs from observed result: current=%+v err=%v", current, err)
	}
	state, _ = e.Applied(context.Background())
	if len(state.VMs) != 1 || state.VMs[0].Revision != completed.SnapshotRevert.ResultSpec.Revision || state.VMs[0].Name != completed.SnapshotRevert.ResultSpec.Name {
		t.Fatalf("settled revert result was not published in owner inventory: %+v", state.VMs)
	}

	invalid := completed
	invalid.CurrentProjection = nil
	if err := e.saveSnapshotJournal(invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.readJournal(filepath.Base(path)); err == nil {
		t.Fatal("accepted completed revert without an atomic current projection")
	}

	invalid = completed
	record := *invalid.SnapshotRevert
	invalid.SnapshotRevert = &record
	invalid.SnapshotRevert.ObservedAfter = nil
	if err := e.saveSnapshotJournal(invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.readJournal(filepath.Base(path)); err == nil {
		t.Fatal("accepted completed revert without a native observation")
	}

	invalid = completed
	invalid.CurrentProjection = &managedSnapshotCurrentProjection{}
	if err := e.saveSnapshotJournal(invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.readJournal(filepath.Base(path)); err == nil {
		t.Fatal("accepted an incomplete current projection")
	}
}

func runSnapshotRevertExecutorStageTest(t *testing.T, nativeAlready bool) (*ManagedEngine, protocol.Command, protocol.VMCreateSpec, string) {
	root := t.TempDir()
	e := NewManaged(filepath.Join(root, "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	vm.Name = "captured-name"
	vmDir := filepath.Join(root, "storage", "nex-vms", vm.ID)
	snapshotDir := filepath.Join(vmDir, "snapshots")
	if err := os.MkdirAll(snapshotDir, 0700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(vmDir, "base.qcow2")
	capturedHead := filepath.Join(snapshotDir, "snapshot-target-vda.qcow2")
	revertHead := filepath.Join(snapshotDir, "reverted-vda.qcow2")
	for _, path := range []string{base, capturedHead, revertHead} {
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
	baseXML, err := testDomainXML(vm, nil, []string{base}, "")
	if err != nil {
		t.Fatal(err)
	}
	definitionHash, err := snapshotDefinitionDigest(baseXML)
	if err != nil {
		t.Fatal(err)
	}
	captureCommand := protocol.Command{OperationID: "snapshot-target", Kind: "snapshot-create", VM: &vm, Snapshot: &protocol.VMSnapshotRequest{Name: "captured point"}}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(vm)
	if err != nil {
		t.Fatal(err)
	}
	storageDependencies := []managedSnapshotStorageDependency{{StorageID: storage.ID, Generation: 1, IdentitySHA256: identity, SnapshotArtifactPath: snapshotDir}}
	capture := managedSnapshotRecord{
		SnapshotID: captureCommand.OperationID, Name: captureCommand.Snapshot.Name, CreatedAt: time.Now().UTC(),
		BeforeDomain: baseXML, Before: managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: definitionHash,
			Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: base, Layers: []string{base}}}},
		ExpectedAfter: managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: definitionHash,
			Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: capturedHead, Layers: []string{capturedHead, base}}}},
		StorageDependencies: storageDependencies, Dependencies: &dependencies,
	}
	capture.ObservedAfter = cloneSnapshotObservation(&capture.ExpectedAfter)
	creationTime := strconv.FormatInt(capture.CreatedAt.Unix(), 10)
	domainInner := string(baseXML[len(`<domain type="kvm">`) : len(baseXML)-len(`</domain>`)])
	_, mediaTargetName, driversTargetName := managedTargets(vm)
	snapshotDiskXML := func(head string) string {
		return `<disk name="` + targets[0] + `" snapshot="external"><driver type="qcow2"/><source file="` + head + `"/></disk>` +
			`<disk name="` + mediaTargetName + `" snapshot="no"/>` +
			`<disk name="` + driversTargetName + `" snapshot="no"/>`
	}
	targetXML := []byte(`<domainsnapshot><name>nex-snapshot-target</name><state>shutoff</state><creationTime>` + creationTime + `</creationTime><memory snapshot="no"/><disks>` + snapshotDiskXML(capturedHead) + `</disks><domain type="kvm">` + domainInner + `</domain></domainsnapshot>`)
	targetAfterXML := []byte(`<domainsnapshot><name>nex-snapshot-target</name><state>shutoff</state><creationTime>` + creationTime + `</creationTime><memory snapshot="no"/><disks>` + snapshotDiskXML(revertHead) + `</disks><domain type="kvm">` + domainInner + `</domain></domainsnapshot>`)
	targetXMLPath := filepath.Join(root, "target-snapshot.xml")
	targetAfterXMLPath := filepath.Join(root, "target-snapshot-after.xml")
	if err := os.WriteFile(targetXMLPath, targetXML, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetAfterXMLPath, targetAfterXML, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VMHOST_REVERT_TARGET_XML", targetXMLPath)
	t.Setenv("VMHOST_REVERT_TARGET_XML_AFTER", targetAfterXMLPath)
	t.Setenv("VMHOST_REVERT_CURRENT_SNAPSHOT", "nex-prior-point")
	t.Setenv("VMHOST_REVERT_TARGET_SNAPSHOT", "nex-snapshot-target")
	statePath := filepath.Join(root, "native-reverted")
	t.Setenv("VMHOST_REVERT_STATE", statePath)
	t.Setenv("VMHOST_REVERT_BASE", base)
	t.Setenv("VMHOST_REVERT_CURRENT_HEAD", capturedHead)
	t.Setenv("VMHOST_REVERT_HEAD", revertHead)
	logPath := filepath.Join(root, "virsh.log")
	t.Setenv("VMHOST_REVERT_LOG", logPath)
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
dominfo) echo 'Autostart: disable';;
dumpxml) if [ -f "$VMHOST_REVERT_STATE" ]; then printf '%s' "$VMHOST_REVERT_XML"; else printf '%s' "$VMHOST_REVERT_BEFORE_XML"; fi;;
snapshot-current) if [ -f "$VMHOST_REVERT_STATE" ]; then echo "$VMHOST_REVERT_TARGET_SNAPSHOT"; else echo "$VMHOST_REVERT_CURRENT_SNAPSHOT"; fi;;
snapshot-dumpxml) if [ -f "$VMHOST_REVERT_STATE" ]; then cat "$VMHOST_REVERT_TARGET_XML_AFTER"; else cat "$VMHOST_REVERT_TARGET_XML"; fi;;
list) ;;
start) echo start >> "$VMHOST_REVERT_LOG";;
snapshot-revert) echo snapshot-revert >> "$VMHOST_REVERT_LOG"; touch "$VMHOST_REVERT_STATE";;
*) exit 2;;
esac`)
	fakeCommand(t, "qemu-img", `if [ "$1" = check ]; then exit 0; fi
if [ "$1" = info ] && [ "$3" = --backing-chain ]; then
  head="$VMHOST_REVERT_CURRENT_HEAD"
  if [ -f "$VMHOST_REVERT_STATE" ]; then head="$VMHOST_REVERT_HEAD"; fi
  printf '[{"filename":"%s","format":"qcow2","backing-filename":"%s","full-backing-filename":"%s"},{"filename":"%s","format":"qcow2"}]\n' "$head" "$VMHOST_REVERT_BASE" "$VMHOST_REVERT_BASE" "$VMHOST_REVERT_BASE"
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
		Disks: []string{capturedHead}, DiskChains: [][]string{{capturedHead, base}}, Targets: targets, Domain: baseXML}); err != nil {
		t.Fatal(err)
	}
	currentVM := vm
	currentVM.Name = "renamed-after-capture"
	currentVM.Tags = []string{"preserve-current-tag"}
	currentVM.Revision++
	currentXML, err := testDomainXML(currentVM, nil, []string{capturedHead}, "")
	if err != nil {
		t.Fatal(err)
	}
	postXML := mustRevertDomainXML(t, currentXML, currentVM.Name, revertHead)
	t.Setenv("VMHOST_REVERT_BEFORE_XML", string(currentXML))
	t.Setenv("VMHOST_REVERT_XML", string(postXML))
	if nativeAlready {
		if err := os.WriteFile(statePath, []byte("reverted"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	currentTargets, mediaTarget, driversTarget := managedTargets(currentVM)
	currentRaw := currentXML
	currentObservation := managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: mustSnapshotDefinitionDigest(t, currentRaw),
		Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: capturedHead, Layers: []string{capturedHead, base}}}}
	revertCommandVM := currentVM
	revertCommandVM.NICs, revertCommandVM.MediaID = nil, ""
	revertCommand := protocol.Command{OperationID: "snapshot-revert-op", Kind: "snapshot-revert", VM: &revertCommandVM,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: descriptor.ID, Target: &descriptor}}
	resultSpec, err := protocol.PoolVMSnapshotRevertSpec(vm, revertCommandVM)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "current-update", Kind: "vm-update", VM: &currentVM},
		Result: protocol.Result{OperationID: "current-update", Status: "succeeded"}, Stage: "defined", Disks: []string{capturedHead},
		DiskChains: [][]string{{capturedHead, base}}, Targets: currentTargets, MediaTarget: mediaTarget, DriversTarget: driversTarget, Domain: currentXML}); err != nil {
		t.Fatal(err)
	}
	targetDescriptorHash, err := protocol.PoolVMSnapshotDescriptorSHA256(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	targetJournalBytes, err := readSmall(filepath.Join(e.stateDir, descriptor.ID+".json"), managedSnapshotJournalLimit)
	if err != nil {
		t.Fatal(err)
	}
	targetJournalHash := sha256.Sum256(targetJournalBytes)
	targetDomain := append([]byte(nil), capture.BeforeDomain...)
	resultSpec, err = protocol.PoolVMSnapshotRevertSpec(*captureCommand.VM, currentVM)
	if err != nil {
		t.Fatal(err)
	}
	revertRecord := managedSnapshotRevertRecord{
		TargetSnapshotID: descriptor.ID, TargetSnapshotSHA256: targetDescriptorHash,
		TargetJournalSHA256: hex.EncodeToString(targetJournalHash[:]), TargetNativeXMLSHA256: capture.NativeXMLSHA256,
		CurrentSnapshotBefore: "nex-prior-point", CreatedAt: time.Now().UTC(), CapturedSpec: vm, ResultSpec: resultSpec,
		BeforeDomain: currentRaw, TargetDomain: targetDomain, Before: currentObservation,
		TargetBefore: capture.Before, Target: capture.Before,
	}
	intent, err := managedSnapshotRevertIntentDigest(revertCommand, revertRecord)
	if err != nil {
		t.Fatal(err)
	}
	currentSpecJSON, _ := json.Marshal(revertCommand.VM)
	currentDiskJSON, _ := json.Marshal(revertCommand.VM.Disks)
	resultSpecJSON, _ := json.Marshal(resultSpec)
	currentSpecHash, currentDiskHash, resultHash := sha256.Sum256(currentSpecJSON), sha256.Sum256(currentDiskJSON), sha256.Sum256(resultSpecJSON)
	reservation := protocol.PoolVMRecoveryReservation{
		OperationID: revertCommand.OperationID, Kind: "snapshot-revert", OwnerOperationOrder: createReservation.OwnerOperationOrder + 1,
		PoolID: currentVM.PoolID, VMID: currentVM.ID, OwnerHostID: currentVM.HostID, Epoch: createReservation.Epoch,
		SpecRevision: currentVM.Revision, SpecSHA256: hex.EncodeToString(currentSpecHash[:]),
		DiskIdentitySHA256: hex.EncodeToString(currentDiskHash[:]), IntentSHA256: intent,
		TargetSnapshotID: descriptor.ID, TargetSnapshotSHA256: targetDescriptorHash, ResultSpecSHA256: hex.EncodeToString(resultHash[:]),
	}
	if err := validateManagedSnapshotRevertReservation(revertCommand, revertRecord, reservation); err != nil {
		t.Fatal(err)
	}
	stage := "reserved"
	if nativeAlready {
		stage = "native"
	}
	if err := e.saveSnapshotJournal(managedJournal{
		Command: revertCommand, SnapshotRevert: &revertRecord, RecoveryReservation: &reservation,
		RecoveryStage: stage, Stage: "snapshot-revert-" + stage, Result: protocol.Result{OperationID: revertCommand.OperationID, Status: "pending"},
		Disks: []string{capturedHead}, DiskChains: [][]string{{capturedHead, base}}, Targets: currentTargets, Domain: currentRaw,
	}); err != nil {
		t.Fatal(err)
	}
	reserveCalls, settleCalls := 0, 0
	reserve := func(context.Context, protocol.Command, string, protocol.VMCreateSpec) (protocol.PoolVMRecoveryReservation, error) {
		reserveCalls++
		return protocol.PoolVMRecoveryReservation{}, errors.New("native-stage replay must not reserve again")
	}
	settle := func(_ context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
		settleCalls++
		if evidence.Outcome != "completed" || evidence.Reservation != reservation || evidence.ResultSpec == nil ||
			!reflect.DeepEqual(*evidence.ResultSpec, resultSpec) {
			return errors.New("revert evidence differs from the selected target")
		}
		return nil
	}
	result, err := e.RevertStoppedSnapshot(context.Background(), revertCommand, reserve, settle)
	if err != nil || result.Status != "succeeded" || reserveCalls != 0 || settleCalls != 1 {
		t.Fatalf("native-stage reconciliation result=%+v reserve=%d settle=%d err=%v", result, reserveCalls, settleCalls, err)
	}
	replay, err := e.RevertStoppedSnapshot(context.Background(), revertCommand, reserve, settle)
	if err != nil || replay.Status != "succeeded" || reserveCalls != 0 || settleCalls != 1 {
		t.Fatalf("settled snapshot-revert replay=%+v reserve=%d settle=%d err=%v", replay, reserveCalls, settleCalls, err)
	}
	raw, readErr := os.ReadFile(logPath)
	wantNativeCalls := 0
	if !nativeAlready {
		wantNativeCalls = 1
	}
	if got := strings.Count(string(raw), "snapshot-revert"); got != wantNativeCalls || readErr != nil && wantNativeCalls != 0 {
		t.Fatalf("native snapshot-revert calls=%d, want %d; output=%q err=%v", got, wantNativeCalls, raw, readErr)
	}
	settledXML, err := os.ReadFile(filepath.Join(e.stateDir, revertCommand.OperationID+".json"))
	if err != nil || !strings.Contains(string(settledXML), resultSpec.Name) {
		t.Fatalf("current display name was not retained in settled projection: err=%v", err)
	}
	return e, revertCommand, resultSpec, logPath
}

func TestSnapshotRevertNativeStageReconcilesWithoutRepeatingAndKeepsCurrentName(t *testing.T) {
	runSnapshotRevertExecutorStageTest(t, true)
}

func TestSnapshotRevertReservedStageRunsNativeOnceAndSettles(t *testing.T) {
	runSnapshotRevertExecutorStageTest(t, false)
}

func TestSnapshotRevertNativeStageEffectAbsentRerunsOnce(t *testing.T) {
	root := t.TempDir()
	e := NewManaged(filepath.Join(root, "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	vm.Name = "captured-name"
	vmDir := filepath.Join(root, "storage", "nex-vms", vm.ID)
	snapshotDir := filepath.Join(vmDir, "snapshots")
	if err := os.MkdirAll(snapshotDir, 0700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(vmDir, "base.qcow2")
	capturedHead := filepath.Join(snapshotDir, "snapshot-target-vda.qcow2")
	revertHead := filepath.Join(snapshotDir, "reverted-vda.qcow2")
	for _, path := range []string{base, capturedHead, revertHead} {
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
	baseXML, err := testDomainXML(vm, nil, []string{base}, "")
	if err != nil {
		t.Fatal(err)
	}
	definitionHash, err := snapshotDefinitionDigest(baseXML)
	if err != nil {
		t.Fatal(err)
	}
	captureCommand := protocol.Command{OperationID: "snapshot-target", Kind: "snapshot-create", VM: &vm, Snapshot: &protocol.VMSnapshotRequest{Name: "captured point"}}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(vm)
	if err != nil {
		t.Fatal(err)
	}
	storageDependencies := []managedSnapshotStorageDependency{{StorageID: storage.ID, Generation: 1, IdentitySHA256: identity, SnapshotArtifactPath: snapshotDir}}
	capture := managedSnapshotRecord{
		SnapshotID: captureCommand.OperationID, Name: captureCommand.Snapshot.Name, CreatedAt: time.Now().UTC(),
		BeforeDomain: baseXML, Before: managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: definitionHash,
			Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: base, Layers: []string{base}}}},
		ExpectedAfter: managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: definitionHash,
			Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: capturedHead, Layers: []string{capturedHead, base}}}},
		StorageDependencies: storageDependencies, Dependencies: &dependencies,
	}
	capture.ObservedAfter = cloneSnapshotObservation(&capture.ExpectedAfter)
	creationTime := strconv.FormatInt(capture.CreatedAt.Unix(), 10)
	domainInner := string(baseXML[len(`<domain type="kvm">`) : len(baseXML)-len(`</domain>`)])
	_, mediaTargetName, driversTargetName := managedTargets(vm)
	snapshotDiskXML := func(head string) string {
		return `<disk name="` + targets[0] + `" snapshot="external"><driver type="qcow2"/><source file="` + head + `"/></disk>` +
			`<disk name="` + mediaTargetName + `" snapshot="no"/>` +
			`<disk name="` + driversTargetName + `" snapshot="no"/>`
	}
	targetXML := []byte(`<domainsnapshot><name>nex-snapshot-target</name><state>shutoff</state><creationTime>` + creationTime + `</creationTime><memory snapshot="no"/><disks>` + snapshotDiskXML(capturedHead) + `</disks><domain type="kvm">` + domainInner + `</domain></domainsnapshot>`)
	targetAfterXML := []byte(`<domainsnapshot><name>nex-snapshot-target</name><state>shutoff</state><creationTime>` + creationTime + `</creationTime><memory snapshot="no"/><disks>` + snapshotDiskXML(revertHead) + `</disks><domain type="kvm">` + domainInner + `</domain></domainsnapshot>`)
	targetXMLPath := filepath.Join(root, "target-snapshot.xml")
	targetAfterXMLPath := filepath.Join(root, "target-snapshot-after.xml")
	if err := os.WriteFile(targetXMLPath, targetXML, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetAfterXMLPath, targetAfterXML, 0600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "native-reverted")
	logPath := filepath.Join(root, "virsh.log")
	t.Setenv("VMHOST_REVERT_TARGET_XML", targetXMLPath)
	t.Setenv("VMHOST_REVERT_TARGET_XML_AFTER", targetAfterXMLPath)
	t.Setenv("VMHOST_REVERT_CURRENT_SNAPSHOT", "nex-prior-point")
	t.Setenv("VMHOST_REVERT_TARGET_SNAPSHOT", "nex-snapshot-target")
	t.Setenv("VMHOST_REVERT_STATE", statePath)
	t.Setenv("VMHOST_REVERT_BASE", base)
	t.Setenv("VMHOST_REVERT_CURRENT_HEAD", capturedHead)
	t.Setenv("VMHOST_REVERT_HEAD", revertHead)
	t.Setenv("VMHOST_REVERT_LOG", logPath)
	// stage=native, effect absent: no state file, current still prior.
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
dominfo) echo 'Autostart: disable';;
dumpxml) if [ -f "$VMHOST_REVERT_STATE" ]; then printf '%s' "$VMHOST_REVERT_XML"; else printf '%s' "$VMHOST_REVERT_BEFORE_XML"; fi;;
snapshot-current) if [ -f "$VMHOST_REVERT_STATE" ]; then echo "$VMHOST_REVERT_TARGET_SNAPSHOT"; else echo "$VMHOST_REVERT_CURRENT_SNAPSHOT"; fi;;
snapshot-dumpxml) if [ -f "$VMHOST_REVERT_STATE" ]; then cat "$VMHOST_REVERT_TARGET_XML_AFTER"; else cat "$VMHOST_REVERT_TARGET_XML"; fi;;
list) ;;
start) echo start >> "$VMHOST_REVERT_LOG";;
snapshot-revert) echo snapshot-revert >> "$VMHOST_REVERT_LOG"; touch "$VMHOST_REVERT_STATE";;
*) exit 2;;
esac`)
	fakeCommand(t, "qemu-img", `if [ "$1" = check ]; then exit 0; fi
if [ "$1" = info ] && [ "$3" = --backing-chain ]; then
  head="$VMHOST_REVERT_CURRENT_HEAD"
  if [ -f "$VMHOST_REVERT_STATE" ]; then head="$VMHOST_REVERT_HEAD"; fi
  printf '[{"filename":"%s","format":"qcow2","backing-filename":"%s","full-backing-filename":"%s"},{"filename":"%s","format":"qcow2"}]\n' "$head" "$VMHOST_REVERT_BASE" "$VMHOST_REVERT_BASE" "$VMHOST_REVERT_BASE"
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
		Disks: []string{capturedHead}, DiskChains: [][]string{{capturedHead, base}}, Targets: targets, Domain: baseXML}); err != nil {
		t.Fatal(err)
	}
	currentVM := vm
	currentVM.Name = "renamed-after-capture"
	currentVM.Tags = []string{"preserve-current-tag"}
	currentVM.Revision++
	currentXML, err := testDomainXML(currentVM, nil, []string{capturedHead}, "")
	if err != nil {
		t.Fatal(err)
	}
	postXML := mustRevertDomainXML(t, currentXML, currentVM.Name, revertHead)
	t.Setenv("VMHOST_REVERT_BEFORE_XML", string(currentXML))
	t.Setenv("VMHOST_REVERT_XML", string(postXML))
	currentTargets, mediaTarget, driversTarget := managedTargets(currentVM)
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "current-update", Kind: "vm-update", VM: &currentVM},
		Result: protocol.Result{OperationID: "current-update", Status: "succeeded"}, Stage: "defined", Disks: []string{capturedHead},
		DiskChains: [][]string{{capturedHead, base}}, Targets: currentTargets, MediaTarget: mediaTarget, DriversTarget: driversTarget, Domain: currentXML}); err != nil {
		t.Fatal(err)
	}
	targetDescriptorHash, err := protocol.PoolVMSnapshotDescriptorSHA256(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	targetJournalBytes, err := readSmall(filepath.Join(e.stateDir, descriptor.ID+".json"), managedSnapshotJournalLimit)
	if err != nil {
		t.Fatal(err)
	}
	targetJournalHash := sha256.Sum256(targetJournalBytes)
	resultSpec, err := protocol.PoolVMSnapshotRevertSpec(*captureCommand.VM, currentVM)
	if err != nil {
		t.Fatal(err)
	}
	currentObservation := managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: mustSnapshotDefinitionDigest(t, currentXML),
		Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: storage.ID, StorageGeneration: 1, StorageIdentitySHA256: identity, Source: capturedHead, Layers: []string{capturedHead, base}}}}
	revertCommand := protocol.Command{OperationID: "snapshot-revert-effect-absent", Kind: "snapshot-revert", VM: &currentVM,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: descriptor.ID, Target: &descriptor}}
	revertRecord := managedSnapshotRevertRecord{
		TargetSnapshotID: descriptor.ID, TargetSnapshotSHA256: targetDescriptorHash,
		TargetJournalSHA256: hex.EncodeToString(targetJournalHash[:]), TargetNativeXMLSHA256: capture.NativeXMLSHA256,
		CurrentSnapshotBefore: "nex-prior-point", CreatedAt: time.Now().UTC(), CapturedSpec: vm, ResultSpec: resultSpec,
		BeforeDomain: currentXML, TargetDomain: append([]byte(nil), capture.BeforeDomain...), Before: currentObservation,
		TargetBefore: capture.Before, Target: capture.Before,
	}
	intent, err := managedSnapshotRevertIntentDigest(revertCommand, revertRecord)
	if err != nil {
		t.Fatal(err)
	}
	currentSpecJSON, _ := json.Marshal(revertCommand.VM)
	currentDiskJSON, _ := json.Marshal(revertCommand.VM.Disks)
	resultSpecJSON, _ := json.Marshal(resultSpec)
	currentSpecHash, currentDiskHash, resultHash := sha256.Sum256(currentSpecJSON), sha256.Sum256(currentDiskJSON), sha256.Sum256(resultSpecJSON)
	reservation := protocol.PoolVMRecoveryReservation{
		OperationID: revertCommand.OperationID, Kind: "snapshot-revert", OwnerOperationOrder: createReservation.OwnerOperationOrder + 1,
		PoolID: currentVM.PoolID, VMID: currentVM.ID, OwnerHostID: currentVM.HostID, Epoch: createReservation.Epoch,
		SpecRevision: currentVM.Revision, SpecSHA256: hex.EncodeToString(currentSpecHash[:]),
		DiskIdentitySHA256: hex.EncodeToString(currentDiskHash[:]), IntentSHA256: intent,
		TargetSnapshotID: descriptor.ID, TargetSnapshotSHA256: targetDescriptorHash, ResultSpecSHA256: hex.EncodeToString(resultHash[:]),
	}
	if err := e.saveSnapshotJournal(managedJournal{
		Command: revertCommand, SnapshotRevert: &revertRecord, RecoveryReservation: &reservation,
		RecoveryStage: "native", Stage: "snapshot-revert-native", Result: protocol.Result{OperationID: revertCommand.OperationID, Status: "pending"},
		Disks: []string{capturedHead}, DiskChains: [][]string{{capturedHead, base}}, Targets: currentTargets, Domain: currentXML,
	}); err != nil {
		t.Fatal(err)
	}
	settleCalls := 0
	result, err := e.RevertStoppedSnapshot(context.Background(), revertCommand,
		func(context.Context, protocol.Command, string, protocol.VMCreateSpec) (protocol.PoolVMRecoveryReservation, error) {
			t.Fatal("effect-absent resume must not reserve again")
			return protocol.PoolVMRecoveryReservation{}, errors.New("unexpected reserve")
		},
		func(_ context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
			settleCalls++
			if evidence.Outcome != "completed" {
				return errors.New("unexpected outcome")
			}
			return nil
		})
	if err != nil || result.Status != "succeeded" || settleCalls != 1 {
		t.Fatalf("effect-absent resume result=%+v settle=%d err=%v", result, settleCalls, err)
	}
	raw, readErr := os.ReadFile(logPath)
	if readErr != nil || strings.Count(string(raw), "snapshot-revert") != 1 {
		t.Fatalf("effect-absent resume must re-run snapshot-revert once: calls=%q err=%v", raw, readErr)
	}
	replay, err := e.RevertStoppedSnapshot(context.Background(), revertCommand,
		func(context.Context, protocol.Command, string, protocol.VMCreateSpec) (protocol.PoolVMRecoveryReservation, error) {
			return protocol.PoolVMRecoveryReservation{}, errors.New("unexpected reserve")
		},
		func(context.Context, protocol.PoolVMRecoveryEvidence) error {
			t.Fatal("settled replay must not settle again")
			return errors.New("unexpected settle")
		})
	if err != nil || replay.Status != "succeeded" {
		t.Fatalf("settled replay=%+v err=%v", replay, err)
	}
	raw, _ = os.ReadFile(logPath)
	if strings.Count(string(raw), "snapshot-revert") != 1 {
		t.Fatalf("settled replay repeated native revert: %q", raw)
	}
}

func TestSnapshotRevertSettleFailedBeforeEffectsSetsFailedStatus(t *testing.T) {
	e, completed, _ := managedSnapshotRevertFixture(t)
	completed.RecoveryStage = "failed-before-effects"
	completed.RecoveryOutcome = "failed-before-effects"
	completed.Stage = "snapshot-revert-failed-before-effects"
	completed.CurrentProjection = nil
	completed.SnapshotRevert.ObservedAfter = nil
	completed.SnapshotRevert.ObservedDomain = nil
	completed.SnapshotRevert.ObservedTargetNativeXMLSHA256 = ""
	completed.Result.Status = "pending"
	if err := e.saveSnapshotJournal(completed); err != nil {
		t.Fatal(err)
	}
	settleCalls := 0
	result, err := e.settleSnapshotRevert(context.Background(), completed, func(_ context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
		settleCalls++
		if evidence.Outcome != "failed-before-effects" || evidence.Reservation != *completed.RecoveryReservation {
			t.Fatalf("settle evidence=%#v", evidence)
		}
		return nil
	})
	if err != nil || result.Status != "failed" || settleCalls != 1 {
		t.Fatalf("failed-before-effects settle result=%+v settle=%d err=%v", result, settleCalls, err)
	}
	settled, err := e.readJournal(completed.Command.OperationID + ".json")
	if err != nil || !settled.RecoverySettled || settled.Result.Status != "failed" {
		t.Fatalf("settled journal status=%q settled=%v err=%v", settled.Result.Status, settled.RecoverySettled, err)
	}
}

func TestManagedPowerAfterSettledSnapshotPredecessorExecutes(t *testing.T) {
	e, vm, reservation := managedRecoveryFixture(t)
	seed, err := e.readJournal("create-seed.json")
	if err != nil {
		t.Fatal(err)
	}
	_, capture, _ := managedRecoveryEntryForTest(vm, "snapshot-chain-seed")
	seed.Disks, seed.DiskChains = snapshotObservationProjection(capture.Before)
	seed.Targets = managedSnapshotTargets(capture.Before)
	seed.Domain = append([]byte(nil), capture.BeforeDomain...)
	if err := e.save(seed); err != nil {
		t.Fatal(err)
	}
	point, err := e.readJournal(reservation.OperationID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	point.RecoverySettled = true
	point.Result.Status = "succeeded"
	if err := e.saveSnapshotJournal(point); err != nil {
		t.Fatal(err)
	}
	e.observeChains = func(_ context.Context, _ []managedJournal, current managedJournal) ([][]string, error) {
		return cloneDiskChains(current.DiskChains), nil
	}
	log := filepath.Join(t.TempDir(), "virsh.log")
	t.Setenv("VMHOST_REVERT_POWER_LOG", log)
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
start) echo start >> "$VMHOST_REVERT_POWER_LOG";;
*) exit 2;; esac`)
	command := protocol.Command{OperationID: "start-after-snapshot", Kind: "vm-power", Power: &protocol.VMPowerRequest{
		VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "start", AfterOperationID: reservation.OperationID,
	}}
	result, err := e.Execute(context.Background(), command, nil)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("start after settled snapshot predecessor=%+v err=%v", result, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "start\n" {
		t.Fatalf("settled snapshot predecessor did not permit one start: calls=%q err=%v", calls, err)
	}
}

func TestManagedPowerAfterSettledSnapshotRevertPredecessorExecutes(t *testing.T) {
	e, revertCommand, resultSpec, log := runSnapshotRevertExecutorStageTest(t, true)
	e.observeChains = func(_ context.Context, _ []managedJournal, current managedJournal) ([][]string, error) {
		return cloneDiskChains(current.DiskChains), nil
	}
	command := protocol.Command{OperationID: "start-after-settled-revert", Kind: "vm-power", Power: &protocol.VMPowerRequest{
		VMID: resultSpec.ID, PoolID: resultSpec.PoolID, HostID: resultSpec.HostID, Action: "start", AfterOperationID: revertCommand.OperationID,
	}}
	result, err := e.Execute(context.Background(), command, nil)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("start after settled snapshot-revert predecessor=%+v err=%v", result, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || strings.Count(string(calls), "start") != 1 || strings.Contains(string(calls), "snapshot-revert") {
		t.Fatalf("settled snapshot-revert predecessor did not permit one start: calls=%q err=%v", calls, err)
	}
}

func TestManagedPowerAfterFailedExplicitPredecessorRefusesBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		wrongVM      bool
	}{
		{name: "failed", status: "failed"},
		{name: "pending", status: "pending"},
		{name: "needs-attention", status: "needs_attention"},
		{name: "wrong-vm", status: "succeeded", wrongVM: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := managedTestEngine(filepath.Join(t.TempDir(), "managed"))
			if err := os.MkdirAll(e.stateDir, 0700); err != nil {
				t.Fatal(err)
			}
			vm := managedTestVM()
			predecessorVM := vm
			predecessorVM.Revision++
			if tc.wrongVM {
				predecessorVM.ID = "11111111-2222-4333-a444-555555555555"
			}
			predecessor := protocol.Command{OperationID: "older-update-" + tc.name, Kind: "vm-update", VM: &predecessorVM}
			if err := e.save(managedJournal{
				Command: predecessor,
				Result:  protocol.Result{OperationID: predecessor.OperationID, Status: tc.status, Error: "definition unavailable"}, Stage: "update-interrupted",
			}); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "virsh.log")
			t.Setenv("VMHOST_REVERT_POWER_LOG", log)
			fakeCommand(t, "virsh", `echo called >> "$VMHOST_REVERT_POWER_LOG"; exit 2`)
			command := protocol.Command{OperationID: "start-after-" + tc.name, Kind: "vm-power", Power: &protocol.VMPowerRequest{
				VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "start", AfterOperationID: predecessor.OperationID,
			}}
			result, err := e.Execute(context.Background(), command, nil)
			if err != nil || result.Status != "failed" || result.Error == "" {
				t.Fatalf("start after explicit %s predecessor=%+v err=%v", tc.name, result, err)
			}
			if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("power command reached native host despite %s predecessor: %v", tc.name, err)
			}
		})
	}
}

func mustRevertDomainXML(t *testing.T, raw []byte, name, source string) []byte {
	t.Helper()
	var domain managedDomain
	if err := xml.Unmarshal(raw, &domain); err != nil {
		t.Fatal(err)
	}
	domain.Name = name
	for i := range domain.Devices.Disks {
		if domain.Devices.Disks[i].Readonly == nil && domain.Devices.Disks[i].Device != "cdrom" && domain.Devices.Disks[i].Source != nil {
			domain.Devices.Disks[i].Source.File = source
		}
	}
	b, err := xml.Marshal(domain)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustSnapshotDefinitionDigest(t *testing.T, raw []byte) string {
	t.Helper()
	digest, err := snapshotDefinitionDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestValidateManagedSnapshotRevertRecordAllowsEmptyDescription(t *testing.T) {
	vm := managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	captured := vm
	current := vm
	current.Revision = vm.Revision + 1
	current.Name = "after-edit"
	result, err := protocol.PoolVMSnapshotRevertSpec(captured, current)
	if err != nil {
		t.Fatal(err)
	}
	target := protocol.PoolVMSnapshot{
		ID: "snap-empty-desc", VMID: vm.ID, Name: "point",
		CreatedAt: time.Now().UTC().Truncate(time.Second), SpecRevision: captured.Revision,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
	}
	command := protocol.Command{OperationID: "revert-empty-desc", Kind: "snapshot-revert", VM: &current,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: target.ID, Target: &target}}
	before := snapshotObservationFixture()
	raw := snapshotBeforeDomainFixture()
	record := managedSnapshotRevertRecord{
		TargetSnapshotID: target.ID, TargetSnapshotSHA256: managedSnapshotDescriptorDigest(target),
		TargetJournalSHA256: strings.Repeat("c", 64), TargetNativeXMLSHA256: strings.Repeat("d", 64),
		TargetNativeIdentity: &managedSnapshotNativeIdentity{Description: "", CreationTime: "1790407671"},
		CreatedAt: time.Now().UTC(), BeforeDomain: raw, Before: before, TargetDomain: raw, TargetBefore: before, Target: before,
		CapturedSpec: captured, ResultSpec: result,
	}
	if err := validateManagedSnapshotRevertRecord(command, record); err != nil {
		t.Fatalf("empty native description rejected: %v", err)
	}
	record.TargetNativeIdentity.CreationTime = ""
	if err := validateManagedSnapshotRevertRecord(command, record); err == nil {
		t.Fatal("accepted missing creation time")
	}
}
