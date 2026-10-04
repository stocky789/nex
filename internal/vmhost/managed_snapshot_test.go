// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func snapshotObservationFixture() managedSnapshotObservation {
	observation := managedSnapshotObservation{
		DomainState:      "shut off",
		AutostartEnabled: false,
		Disks: []managedSnapshotDiskObservation{{
			Target: "vda", StorageID: "s", StorageGeneration: 2,
			StorageIdentitySHA256: strings.Repeat("b", 64), Source: "/srv/vm/disk.qcow2",
			Layers: []string{"/srv/vm/disk.qcow2", "/srv/vm/base.qcow2"},
		}},
	}
	raw := snapshotBeforeDomainFixture()
	observation.DefinitionSHA256, _ = snapshotDefinitionDigest(raw)
	return observation
}

func snapshotBeforeDomainFixture() []byte {
	domain := managedDomain{Type: "kvm", UUID: "00000000-0000-4000-8000-000000000001"}
	domain.Devices.Disks = []managedDisk{{Type: "file", Device: "disk", Driver: driverXML{Type: "qcow2"}, Source: &sourceXML{File: "/srv/vm/disk.qcow2"}, Target: targetXML{Dev: "vda"}}}
	raw, _ := xml.Marshal(domain)
	return raw
}

func TestValidateManagedDefinitionAcceptsIdenticalKiBMemory(t *testing.T) {
	vm := managedTestVM()
	vm.Memory.SizeMiB = 512
	wanted, err := testDomainXML(vm, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var domain managedDomain
	if err := xml.Unmarshal(wanted, &domain); err != nil {
		t.Fatal(err)
	}
	domain.Memory.Unit = "KiB"
	domain.Memory.Value *= 1024
	if domain.CurrentMemory != nil {
		domain.CurrentMemory.Unit = "KiB"
		domain.CurrentMemory.Value *= 1024
	}
	raw, err := xml.Marshal(domain)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedDefinition(raw, raw); err != nil {
		t.Fatalf("identical native XML with KiB memory was rejected: %v", err)
	}
	if err := validateManagedDefinition(raw, wanted); err != nil {
		t.Fatalf("native KiB memory did not match the requested MiB memory: %v", err)
	}
	for _, delta := range []int{-1, 1} {
		changedDomain := domain
		changedCurrent := *domain.CurrentMemory
		changedCurrent.Value += delta
		changedDomain.CurrentMemory = &changedCurrent
		changed, err := xml.Marshal(changedDomain)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateManagedDefinition(changed, raw); err == nil {
			t.Fatalf("accepted native currentMemory change of %d KiB", delta)
		}
	}
	omittedCurrent := domain
	omittedValue := memoryXML{Value: vm.Memory.SizeMiB}
	omittedCurrent.CurrentMemory = &omittedValue
	omittedRaw, err := xml.Marshal(omittedCurrent)
	if err != nil {
		t.Fatal(err)
	}
	explicitCurrent := domain
	explicitValue := memoryXML{Unit: "KiB", Value: vm.Memory.SizeMiB}
	explicitCurrent.CurrentMemory = &explicitValue
	explicitRaw, err := xml.Marshal(explicitCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedDefinition(omittedRaw, explicitRaw); err != nil {
		t.Fatalf("omitted currentMemory unit did not default to KiB: %v", err)
	}
	domain.Memory.Value = vm.Memory.SizeMiB
	domain.Memory.Unit = ""
	omittedUnit, err := xml.Marshal(domain)
	if err != nil {
		t.Fatal(err)
	}
	domain.Memory.Unit = "KiB"
	explicitKiB, err := xml.Marshal(domain)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedDefinition(omittedUnit, explicitKiB); err != nil {
		t.Fatalf("omitted native memory unit did not default to KiB: %v", err)
	}
	domain.Memory.Unit = "MiB"
	explicitMiB, err := xml.Marshal(domain)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedDefinition(omittedUnit, explicitMiB); err == nil {
		t.Fatal("interpreted omitted native memory unit as MiB")
	}
	for _, delta := range []int{-1, 1} {
		domain.Memory.Unit = "KiB"
		domain.Memory.Value = vm.Memory.SizeMiB*1024 + delta
		changed, err := xml.Marshal(domain)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateManagedDefinition(changed, raw); err == nil {
			t.Fatalf("accepted a native memory change of %d KiB", delta)
		}
	}
	domain.Memory.Unit = "unsupported"
	unsupported, err := xml.Marshal(domain)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedDefinition(unsupported, raw); err == nil {
		t.Fatal("accepted unsupported native memory unit")
	}
}

func snapshotExpectedObservationFixture() managedSnapshotObservation {
	observation := snapshotObservationFixture()
	observation.Disks[0].Source = "/srv/vm/snapshot-snap-001-vda.qcow2"
	observation.Disks[0].Layers = append([]string{observation.Disks[0].Source}, observation.Disks[0].Layers...)
	return observation
}

func TestSnapshotStorageIdentityDigestUsesStableLocalAndNetworkIdentity(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "local")
	local := protocol.StorageRegistration{Path: localPath, Kind: protocol.StorageKindLocal}
	localAccess := storageAccess{
		Path: localPath, Identity: "local:" + localPath + ":fsid=fsid-a", FilesystemID: "fsid-a",
	}
	localDigest, err := snapshotStorageIdentityDigest(local, localAccess)
	if err != nil {
		t.Fatalf("empty typed local identity should use observed path/fsid: %v", err)
	}
	local.Identity = protocol.StorageIdentity{Local: &protocol.LocalStorageIdentity{FilesystemUUID: "unobserved"}}
	if _, err := snapshotStorageIdentityDigest(local, localAccess); err == nil {
		t.Fatal("accepted an unverified typed identity on a local registration")
	}
	local.Identity = protocol.StorageIdentity{}
	changedFSID := localAccess
	changedFSID.FilesystemID = "fsid-b"
	changedFSID.Identity = "local:" + localPath + ":fsid=fsid-b"
	changedDigest, err := snapshotStorageIdentityDigest(local, changedFSID)
	if err != nil || changedDigest == localDigest {
		t.Fatalf("local filesystem replacement was not bound: digest=%q err=%v", changedDigest, err)
	}
	otherPath := filepath.Join(t.TempDir(), "local")
	changedPath := storageAccess{Path: otherPath, Identity: "local:" + otherPath + ":fsid=fsid-a", FilesystemID: "fsid-a"}
	changedDigest, err = snapshotStorageIdentityDigest(protocol.StorageRegistration{Path: otherPath, Kind: protocol.StorageKindLocal}, changedPath)
	if err != nil || changedDigest == localDigest {
		t.Fatalf("local path replacement was not bound: digest=%q err=%v", changedDigest, err)
	}

	nfs := protocol.StorageRegistration{Kind: protocol.StorageKindNFS,
		Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/vms", FilesystemID: "nfs-a"}}}
	nfsAccess := storageAccess{Observed: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/vms", FilesystemID: "nfs-b"}}}
	if _, err := snapshotStorageIdentityDigest(nfs, nfsAccess); err == nil {
		t.Fatal("accepted a changed typed network storage identity")
	}

	smb := protocol.StorageRegistration{Kind: protocol.StorageKindSMB,
		Identity: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "vms", FilesystemID: "diagnostic-old", MarkerID: "marker-1"}}}
	smbAccess := storageAccess{Observed: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "vms", FilesystemID: "diagnostic-new", MarkerID: "marker-1"}}}
	smbDigest, err := snapshotStorageIdentityDigest(smb, smbAccess)
	if err != nil {
		t.Fatalf("stable SMB marker identity rejected diagnostic drift: %v", err)
	}
	smbAccess.Observed.SMB.FilesystemID = "diagnostic-third"
	if got, err := snapshotStorageIdentityDigest(smb, smbAccess); err != nil || got != smbDigest {
		t.Fatalf("SMB diagnostic filesystem ID changed stable digest: digest=%q err=%v", got, err)
	}
}

func TestValidateManagedSnapshotRequestBindsStableOperationIDAndDescription(t *testing.T) {
	vm := managedTestVM()
	command := protocol.Command{
		Kind: "snapshot-create", OperationID: "snap-001",
		VM:       &vm,
		Snapshot: &protocol.VMSnapshotRequest{Name: "before-upgrade", Description: "checkpoint"},
	}
	if err := validateManagedSnapshotRequest(command); err != nil {
		t.Fatal(err)
	}
	before, expected := snapshotObservationFixture(), snapshotExpectedObservationFixture()
	record := managedSnapshotRecord{SnapshotID: command.OperationID, Name: command.Snapshot.Name,
		Description: command.Snapshot.Description, CreatedAt: time.Now().UTC(), BeforeDomain: snapshotBeforeDomainFixture(), Before: before, ExpectedAfter: expected,
		StorageDependencies: []managedSnapshotStorageDependency{{StorageID: "s", Generation: 2,
			IdentitySHA256: strings.Repeat("b", 64), SnapshotArtifactPath: "/srv/vm/recovery/snap-001"}},
		Firmware: []managedSnapshotFirmwareArtifact{{Kind: "nvram", SourcePath: "/var/lib/libvirt/qemu/nvram/vm_VARS.fd",
			SnapshotPath: "/srv/vm/recovery/snap-001/nvram.fd"}}}
	first, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	command.Snapshot.Description = "different intent"
	record.Description = command.Snapshot.Description
	second, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("snapshot description was not bound to the intent digest")
	}
	command.Snapshot.Description = "checkpoint"
	record.Description = command.Snapshot.Description
	record.Before.Disks[0].Layers[1] = "/srv/vm/other-base.qcow2"
	record.ExpectedAfter.Disks[0].Layers[2] = "/srv/vm/other-base.qcow2"
	changedChain, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	if first == changedChain {
		t.Fatal("observed pre-create backing chain was not bound to the intent digest")
	}

	command.Snapshot.Name = ""
	if err := validateManagedSnapshotRequest(command); err == nil {
		t.Fatal("accepted an empty snapshot name")
	}
}

func TestRetainedSnapshotKeepsHistoricalResourceDependencies(t *testing.T) {
	vm := managedTestVM()
	vm.DriversMediaID = "drivers-1"
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(vm)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := managedJournal{
		Command:       protocol.Command{OperationID: "snap-1", Kind: "snapshot-create", VM: &vm},
		Result:        protocol.Result{OperationID: "snap-1", Status: "succeeded"},
		RecoveryStage: "completed", RecoveryOutcome: "completed", RecoverySettled: true,
		Snapshot: &managedSnapshotRecord{SnapshotID: "snap-1", Dependencies: &dependencies},
	}
	current := vm
	current.Revision++
	current.Disks = nil
	current.NICs = nil
	current.MediaID = ""
	current.DriversMediaID = ""
	js := []managedJournal{snapshot, {Command: protocol.Command{OperationID: "update-1", Kind: "vm-update", VM: &current}, Result: protocol.Result{OperationID: "update-1", Status: "succeeded"}}}
	if got := storageDependencies(js, "s"); !slices.Contains(got, vm.ID) {
		t.Fatalf("retained snapshot disk repository was omitted: %v", got)
	}
	if got := networkDependencies(js, "n"); !slices.Contains(got, vm.ID) {
		t.Fatalf("retained snapshot network was omitted: %v", got)
	}
	if got := mediaDependencies(js, "m"); !slices.Contains(got, vm.ID) {
		t.Fatalf("retained snapshot media was omitted: %v", got)
	}
	if got := mediaDependencies(js, "drivers-1"); !slices.Contains(got, vm.ID) {
		t.Fatalf("retained snapshot driver media was omitted: %v", got)
	}
	if got := storageDependencies(js, "unrelated"); len(got) != 0 {
		t.Fatalf("unrelated storage was blocked by a known snapshot: %v", got)
	}
	if err := mediaRemovalRefusal(js, "m"); err == nil {
		t.Fatal("ejecting the captured ISO from the current VM made snapshot media removable")
	}
}

func TestUnknownLegacySnapshotDependenciesFailClosed(t *testing.T) {
	vm := managedTestVM()
	legacy := managedJournal{
		Command:       protocol.Command{OperationID: "snap-legacy", Kind: "snapshot-create", VM: &vm},
		Result:        protocol.Result{OperationID: "snap-legacy", Status: "succeeded"},
		RecoveryStage: "completed", RecoveryOutcome: "completed", RecoverySettled: true,
		Snapshot: &managedSnapshotRecord{SnapshotID: "snap-legacy"},
	}
	for _, check := range []struct {
		name string
		deps []string
	}{
		{"storage", storageDependencies([]managedJournal{legacy}, "unrelated")},
		{"network", networkDependencies([]managedJournal{legacy}, "unrelated")},
		{"media", mediaDependencies([]managedJournal{legacy}, "unrelated")},
	} {
		if len(check.deps) == 0 {
			t.Errorf("unknown legacy %s dependency set did not fail closed", check.name)
		}
	}
}

func TestUnresolvedSnapshotIntentBlocksItsCapturedResources(t *testing.T) {
	vm := managedTestVM()
	intent := managedJournal{
		Command:       protocol.Command{OperationID: "snap-pending", Kind: "snapshot-create", VM: &vm},
		Result:        protocol.Result{OperationID: "snap-pending", Status: "pending"},
		RecoveryStage: "local-intent", Snapshot: &managedSnapshotRecord{SnapshotID: "snap-pending"},
	}
	if got := networkDependencies([]managedJournal{intent}, "n"); !slices.Contains(got, vm.ID) {
		t.Fatalf("unresolved snapshot did not reserve its captured network: %v", got)
	}
}

func TestSnapshotIntentDependenciesAreBoundAndLegacyDigestStaysStable(t *testing.T) {
	vm := managedTestVM()
	command := protocol.Command{Kind: "snapshot-create", OperationID: "snap-deps", VM: &vm, Snapshot: &protocol.VMSnapshotRequest{Name: "before-upgrade"}}
	record := managedSnapshotRecord{SnapshotID: command.OperationID, Name: command.Snapshot.Name, CreatedAt: time.Now().UTC(),
		BeforeDomain: snapshotBeforeDomainFixture(), Before: snapshotObservationFixture(), ExpectedAfter: snapshotExpectedObservationFixture(),
		StorageDependencies: []managedSnapshotStorageDependency{{StorageID: "s", Generation: 2, IdentitySHA256: strings.Repeat("b", 64), SnapshotArtifactPath: "/srv/vm/recovery/snap-deps"}}}
	legacy, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(*command.VM)
	if err != nil {
		t.Fatal(err)
	}
	record.Dependencies = &dependencies
	withDependencies, err := managedSnapshotIntentDigest(command, record)
	if err != nil || legacy == withDependencies {
		t.Fatalf("new intent dependencies were not bound: legacy=%q new=%q err=%v", legacy, withDependencies, err)
	}
	dependencies.NetworkIDs[0] = "different-network"
	record.Dependencies = &dependencies
	if _, err := managedSnapshotIntentDigest(command, record); err == nil {
		t.Fatal("snapshot intent accepted dependencies different from its exact VM spec")
	}
}

func TestCompletedSnapshotDescriptorCarriesExactDependencies(t *testing.T) {
	vm := managedTestVM()
	command, record, reservation := managedRecoveryEntryForTest(vm, "snap-descriptor")
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(vm)
	if err != nil {
		t.Fatal(err)
	}
	record.Dependencies = &dependencies
	reservation.IntentSHA256, err = managedSnapshotIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := managedSnapshotDescriptor(command, reservation, record)
	if err != nil || descriptor.Dependencies == nil || !reflect.DeepEqual(*descriptor.Dependencies, dependencies) {
		t.Fatalf("snapshot descriptor lost its exact dependency set: %+v, %v", descriptor, err)
	}
	wrong := record
	wrongDependencies := *cloneSnapshotDependencies(record.Dependencies)
	wrongDependencies.NetworkIDs[0] = "different-network"
	wrong.Dependencies = &wrongDependencies
	if _, err := managedSnapshotDescriptor(command, reservation, wrong); err == nil {
		t.Fatal("snapshot descriptor accepted dependencies different from the reserved intent")
	}
	legacy := record
	legacy.Dependencies = nil
	legacyReservation := reservation
	legacyReservation.IntentSHA256, err = managedSnapshotIntentDigest(command, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor, err := managedSnapshotDescriptor(command, legacyReservation, legacy); err != nil || descriptor.Dependencies != nil {
		t.Fatalf("legacy snapshot descriptor must retain its nil dependencies: %+v, %v", descriptor, err)
	}
	if _, err := managedSnapshotDescriptor(command, reservation, func() managedSnapshotRecord {
		legacy := record
		legacy.Dependencies = nil
		return legacy
	}()); err == nil {
		t.Fatal("completed snapshot descriptor accepted a legacy unknown dependency set")
	}
}

func TestLegacyCompletedSnapshotJournalAndReplayRemainCompatible(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed-vms"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	command, record, reservation := managedRecoveryEntryForTest(vm, "legacy-snapshot")
	if record.Dependencies != nil {
		t.Fatal("legacy fixture unexpectedly has new dependency metadata")
	}
	result := protocol.Result{OperationID: command.OperationID, Status: "succeeded", ObservedAt: time.Unix(123, 0).UTC()}
	if err := e.save(managedJournal{Command: command, Snapshot: &record, Result: result, Stage: "defined",
		RecoveryReservation: &reservation, RecoveryStage: "completed", RecoveryOutcome: "completed", RecoverySettled: true}); err != nil {
		t.Fatal(err)
	}
	loaded, err := e.readJournal(command.OperationID + ".json")
	if err != nil || loaded.Snapshot.Dependencies != nil {
		t.Fatalf("legacy completed journal did not load unchanged: %+v, %v", loaded.Snapshot, err)
	}
	descriptor, err := managedSnapshotDescriptor(command, reservation, *loaded.Snapshot)
	if err != nil || descriptor.Dependencies != nil {
		t.Fatalf("legacy completed descriptor did not preserve nil dependencies: %+v, %v", descriptor, err)
	}
	replayed, err := e.Execute(context.Background(), command, nil)
	if err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatalf("legacy terminal replay changed: got %+v err=%v want %+v", replayed, err, result)
	}
}

func TestManagedSnapshotObservationRejectsRunningAndIncompleteDiskSets(t *testing.T) {
	observation := snapshotObservationFixture()
	if err := validateManagedSnapshotObservation(observation); err != nil {
		t.Fatal(err)
	}
	observation.DomainState = "running"
	if err := validateManagedSnapshotObservation(observation); err != nil {
		t.Fatalf("running observation should be valid for crash/quiesced capture: %v", err)
	}
	observation.DomainState = "paused"
	if err := validateManagedSnapshotObservation(observation); err == nil {
		t.Fatal("accepted a paused VM snapshot observation")
	}
	observation = snapshotObservationFixture()
	observation.Disks = append(observation.Disks, observation.Disks[0])
	if err := validateManagedSnapshotObservation(observation); err == nil {
		t.Fatal("accepted duplicate writable disk targets")
	}
	observation = snapshotObservationFixture()
	observation.Disks[0].Layers = observation.Disks[0].Layers[:1]
	if err := validateManagedSnapshotObservation(observation); err != nil {
		t.Fatalf("standalone observed chain should be valid: %v", err)
	}
}

func TestManagedSnapshotFirmwareCopyPreservesManifestAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "snapshot", "state")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, "nested", "state.bin")
	if err := os.WriteFile(file, []byte("firmware-state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest, err := copyManagedSnapshotFirmware(context.Background(), source, destination)
	if err != nil {
		t.Fatal(err)
	}
	copyManifest, err := managedSnapshotManifest(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotManifestsEqual(manifest, copyManifest) || !manifest.Exists || len(manifest.Files) != 3 {
		t.Fatalf("firmware copy manifest mismatch: source copy=%+v destination=%+v", manifest, copyManifest)
	}
	for _, entry := range manifest.Files {
		if entry.Directory && entry.Size != 0 {
			t.Fatalf("directory size was not normalized: %+v", entry)
		}
	}
	if _, err = copyManagedSnapshotFirmware(context.Background(), source, destination); err == nil {
		t.Fatal("overwrote an existing firmware snapshot artifact")
	}

	badSource := filepath.Join(root, "bad-source")
	if err = os.Mkdir(badSource, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(file, filepath.Join(badSource, "linked-state")); err != nil {
		t.Fatal(err)
	}
	if _, err = copyManagedSnapshotFirmware(context.Background(), badSource, filepath.Join(root, "bad-copy")); err == nil {
		t.Fatal("copied a symlink from firmware state")
	}
	linkedSource := filepath.Join(root, "linked-source")
	if err = os.Mkdir(linkedSource, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(linkedSource, "state"), []byte("state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(filepath.Join(linkedSource, "state"), filepath.Join(linkedSource, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err = managedSnapshotManifest(context.Background(), linkedSource); err == nil {
		t.Fatal("accepted hard-linked TPM state")
	}
}

func TestManagedSnapshotRecordRequiresExpectedPostCreateProjection(t *testing.T) {
	observation := snapshotObservationFixture()
	record := managedSnapshotRecord{
		SnapshotID: "snap-001", Name: "before-upgrade", CreatedAt: time.Now().UTC(), BeforeDomain: snapshotBeforeDomainFixture(),
		Before: observation, ExpectedAfter: snapshotExpectedObservationFixture(),
		StorageDependencies: []managedSnapshotStorageDependency{{
			StorageID: "s", Generation: 2, IdentitySHA256: strings.Repeat("b", 64),
			SnapshotArtifactPath: "/srv/vm/recovery/snap-001",
		}},
	}
	if err := validateManagedSnapshotRecord("snap-001", record); err != nil {
		t.Fatal(err)
	}
	bad := record
	bad.ExpectedAfter.Disks = nil
	if err := validateManagedSnapshotRecord("snap-001", bad); err == nil {
		t.Fatal("accepted a snapshot journal without its expected head projection")
	}
}

func TestManagedSnapshotResumeNeverRepeatsAnUncertainNativeRequest(t *testing.T) {
	cases := []struct {
		name          string
		stage         string
		before        bool
		after         bool
		present       bool
		absent        bool
		outputsAbsent bool
		want          string
	}{
		{"first reserved attempt", "reserved", true, false, false, true, true, "create"},
		{"interrupted native exact before state", "native", true, false, false, true, true, "failed-before-effects"},
		{"interrupted native left output file", "native", true, false, false, true, false, "needs-attention"},
		{"completed native state", "native", false, true, true, false, false, "complete"},
		{"heads changed without metadata", "native", false, true, false, true, false, "needs-attention"},
		{"mixed projection", "reserved", false, false, false, true, true, "needs-attention"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := managedSnapshotResumeAction(tc.stage, tc.before, tc.after, tc.present, tc.absent, tc.outputsAbsent)
			if got != tc.want {
				t.Fatalf("resume action=%q want %q", got, tc.want)
			}
		})
	}
}

func TestBlockedSnapshotCaptureAllowsUnrelatedVMStart(t *testing.T) {
	e, vm, command, reserve, settle, disk, domainXML := stoppedSnapshotTestFixture(t, "snapshot-blocked")

	other := vm
	other.ID = "87654321-1234-4234-8234-123456789012"
	other.Name = "other"
	otherCreate := protocol.Command{OperationID: "other-create", Kind: "vm-create", VM: &other}
	if err := e.save(managedJournal{Command: otherCreate, Result: protocol.Result{OperationID: otherCreate.OperationID, Status: "succeeded"}, Stage: "defined", Disks: []string{disk}, DiskChains: [][]string{{disk}}, Targets: []string{"vda"}, Domain: domainXML}); err != nil {
		t.Fatal(err)
	}

	started := filepath.Join(t.TempDir(), "snapshot-started")
	release := filepath.Join(t.TempDir(), "release-snapshot")
	log := filepath.Join(t.TempDir(), "virsh.log")
	t.Setenv("VMHOST_SNAPSHOT_STARTED", started)
	t.Setenv("VMHOST_SNAPSHOT_RELEASE", release)
	t.Setenv("VMHOST_SNAPSHOT_LOG", log)
	t.Setenv("VMHOST_SNAPSHOT_DOMAIN", string(domainXML))
	t.Setenv("VMHOST_SNAPSHOT_DISK", disk)
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
dominfo) echo 'Autostart: disable';;
dumpxml) printf '%s' "$VMHOST_SNAPSHOT_DOMAIN";;
snapshot-list) ;;
snapshot-create-as) touch "$VMHOST_SNAPSHOT_STARTED"; while [ ! -e "$VMHOST_SNAPSHOT_RELEASE" ]; do sleep 0.01; done; exit 1;;
start) echo "start $4" >> "$VMHOST_SNAPSHOT_LOG";;
*) echo "unexpected $*" >> "$VMHOST_SNAPSHOT_LOG"; exit 2;;
esac`)
	fakeCommand(t, "qemu-img", `case "$*" in
*--backing-chain*) printf '[{"filename":"%s","format":"qcow2"}]\n' "$VMHOST_SNAPSHOT_DISK";;
*) exit 2;;
esac`)

	snapshotDone := make(chan struct {
		result protocol.Result
		err    error
	}, 1)
	go func() {
		result, err := e.CreateStoppedSnapshot(context.Background(), command, reserve, settle)
		snapshotDone <- struct {
			result protocol.Result
			err    error
		}{result, err}
	}()
	snapshotReceived := false
	defer func() {
		_ = os.WriteFile(release, nil, 0600)
		if !snapshotReceived {
			<-snapshotDone
		}
	}()
	select {
	case <-testFileSignal(started):
	case completed := <-snapshotDone:
		snapshotReceived = true
		t.Fatalf("snapshot did not reach native capture: result=%+v err=%v", completed.result, completed.err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the native snapshot command")
	}

	start := protocol.Command{OperationID: "other-start", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: other.ID, PoolID: other.PoolID, HostID: other.HostID, Action: "start"}}
	startDone := make(chan struct {
		result protocol.Result
		err    error
	}, 1)
	go func() {
		result, err := e.Execute(context.Background(), start, nil)
		startDone <- struct {
			result protocol.Result
			err    error
		}{result, err}
	}()
	select {
	case completed := <-startDone:
		if completed.err != nil || completed.result.Status != "succeeded" {
			t.Fatalf("unrelated VM start did not complete during snapshot capture: result=%+v err=%v", completed.result, completed.err)
		}
	case <-time.After(3 * time.Second):
		_ = os.WriteFile(release, nil, 0600)
		t.Fatal("unrelated VM start waited for the blocked native snapshot command")
	}

	_ = os.WriteFile(release, nil, 0600)
	completed := <-snapshotDone
	snapshotReceived = true
	if completed.err != nil || completed.result.Status != "failed" {
		t.Fatalf("controlled no-effect snapshot failure did not settle cleanly: result=%+v err=%v", completed.result, completed.err)
	}
	if calls, err := os.ReadFile(log); err != nil || !strings.Contains(string(calls), "start "+other.ID) {
		t.Fatalf("unrelated VM start did not reach libvirt: calls=%q err=%v", calls, err)
	}
}

func TestPersistedNativeSnapshotRetryReconcilesWithoutRepeatingCreate(t *testing.T) {
	e, vm, command, _, _, disk, _ := stoppedSnapshotTestFixture(t, "snapshot-restart")
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	current, err := managedCurrent(js, vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	record, _, _, err := e.snapshotIntent(context.Background(), command, js, current, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	intent, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	_, _, reservation := managedRecoveryEntryForTest(vm, command.OperationID)
	reservation.IntentSHA256 = intent
	beforeDisks, beforeChains := snapshotProjection(record, false)
	pending := managedJournal{
		Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending"},
		Stage: "snapshot-native", RecoveryStage: "native", Snapshot: &record,
		RecoveryReservation: &reservation, Disks: beforeDisks, DiskChains: beforeChains, Targets: []string{"vda"}, Domain: record.BeforeDomain,
	}
	if err := e.saveSnapshotJournal(pending); err != nil {
		t.Fatal(err)
	}

	started := filepath.Join(t.TempDir(), "snapshot-create-called")
	log := filepath.Join(t.TempDir(), "virsh.log")
	t.Setenv("VMHOST_RESTART_CREATE_MARKER", started)
	t.Setenv("VMHOST_RESTART_LOG", log)
	t.Setenv("VMHOST_RESTART_DOMAIN", string(record.BeforeDomain))
	t.Setenv("VMHOST_RESTART_DISK", disk)
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
dominfo) echo 'Autostart: disable';;
dumpxml) printf '%s' "$VMHOST_RESTART_DOMAIN";;
snapshot-list) ;;
snapshot-create-as) touch "$VMHOST_RESTART_CREATE_MARKER"; exit 99;;
*) echo "$*" >> "$VMHOST_RESTART_LOG";;
esac`)
	fakeCommand(t, "qemu-img", `case "$*" in
*--backing-chain*) printf '[{"filename":"%s","format":"qcow2"}]\n' "$VMHOST_RESTART_DISK";;
info\ *) printf '{"format":"qcow2"}\n';;
*) exit 2;;
esac`)

	// A new engine instance and the exact original command model a Core process restart.
	restarted := NewManaged(e.stateDir)
	result, err := restarted.CreateStoppedSnapshot(context.Background(), command,
		func(context.Context, protocol.Command, string, time.Time) (protocol.PoolVMRecoveryReservation, error) {
			return protocol.PoolVMRecoveryReservation{}, errors.New("recovery unexpectedly requested a new reservation")
		},
		func(_ context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
			if evidence.Reservation != reservation || evidence.Outcome != "failed-before-effects" {
				return errors.New("recovery did not settle the exact failed-before-effects evidence")
			}
			return nil
		})
	if err != nil || result.Status != "failed" {
		t.Fatalf("same-operation post-restart reconciliation result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(started); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("same-operation recovery repeated native snapshot creation: %v", err)
	}
	journal, err := restarted.readJournal(command.OperationID + ".json")
	if err != nil || !journal.RecoverySettled || journal.RecoveryStage != "failed-before-effects" || journal.Result.Status != "failed" {
		t.Fatalf("persisted uncertain operation did not settle after observation: journal=%+v err=%v", journal, err)
	}
}

func stoppedSnapshotTestFixture(t *testing.T, operationID string) (*ManagedEngine, protocol.VMCreateSpec, protocol.Command,
	func(context.Context, protocol.Command, string, time.Time) (protocol.PoolVMRecoveryReservation, error),
	func(context.Context, protocol.PoolVMRecoveryEvidence) error, string, []byte) {
	t.Helper()
	e, vm := NewManaged(filepath.Join(t.TempDir(), "managed")), managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		t.Skip("snapshot storage ancestry validation requires a root-owned fixture")
	}
	fixtureRoot, err := os.MkdirTemp("/var/lib", "nex-snapshot-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fixtureRoot) })
	if err := os.Chmod(fixtureRoot, 0755); err != nil {
		t.Fatal(err)
	}
	storageRoot := filepath.Join(fixtureRoot, "storage")
	vmDir := filepath.Join(storageRoot, "nex-vms", vm.ID)
	for _, path := range []string{storageRoot, filepath.Dir(vmDir), vmDir} {
		if _, _, err := managedDirectory(path); err != nil {
			t.Fatal(err)
		}
	}
	disk := filepath.Join(vmDir, "disk-0.qcow2")
	if err := os.WriteFile(disk, []byte("source marker"), 0600); err != nil {
		t.Fatal(err)
	}
	domainXML, err := testDomainXML(vm, nil, []string{disk}, "")
	if err != nil {
		t.Fatal(err)
	}
	targets, _, _ := managedTargets(vm)
	create := protocol.Command{OperationID: operationID + "-seed", Kind: "vm-create", VM: &vm}
	if err := e.save(managedJournal{Command: create, Result: protocol.Result{OperationID: create.OperationID, Status: "succeeded"}, Stage: "defined",
		Disks: []string{disk}, DiskChains: [][]string{{disk}}, Targets: targets, Domain: domainXML}); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Generation: 1, Path: storageRoot, Kind: protocol.StorageKindLocal}
	access := storageAccess{Path: storageRoot, Identity: "local:" + storageRoot + ":fsid=test-fsid", FilesystemID: "test-fsid", Total: 1 << 30, Available: 1 << 30, Health: "healthy"}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: operationID + "-storage", Kind: "storage-register", Storage: &storage}, AppliedStorage: &storage,
		Result: protocol.Result{OperationID: operationID + "-storage", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: operationID, Kind: "snapshot-create", VM: &vm, Snapshot: &protocol.VMSnapshotRequest{Name: "before restart"}}
	t.Setenv("VMHOST_SNAPSHOT_DOMAIN", string(domainXML))
	t.Setenv("VMHOST_SNAPSHOT_DISK", disk)
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
dominfo) echo 'Autostart: disable';;
dumpxml) printf '%s' "$VMHOST_SNAPSHOT_DOMAIN";;
snapshot-list) ;;
*) exit 2;;
esac`)
	fakeCommand(t, "qemu-img", `case "$*" in
*--backing-chain*) printf '[{"filename":"%s","format":"qcow2"}]\n' "$VMHOST_SNAPSHOT_DISK";;
*) exit 2;;
esac`)
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, got protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		if got.ID != storage.ID || got.Path != storage.Path {
			return storageAccess{}, errors.New("unexpected storage registration")
		}
		return access, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	e.observeChains = func(_ context.Context, _ []managedJournal, candidate managedJournal) ([][]string, error) {
		chains := make([][]string, len(candidate.DiskChains))
		for i := range candidate.DiskChains {
			chains[i] = append([]string(nil), candidate.DiskChains[i]...)
		}
		return chains, nil
	}
	reserve := func(_ context.Context, got protocol.Command, intent string, _ time.Time) (protocol.PoolVMRecoveryReservation, error) {
		if !reflect.DeepEqual(got, command) {
			return protocol.PoolVMRecoveryReservation{}, errors.New("reservation callback received a different command")
		}
		_, _, reservation := managedRecoveryEntryForTest(vm, command.OperationID)
		reservation.IntentSHA256 = intent
		return reservation, nil
	}
	settle := func(_ context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
		if evidence.Reservation.OperationID != command.OperationID || evidence.Outcome != "failed-before-effects" {
			return errors.New("unexpected snapshot settlement evidence")
		}
		return nil
	}
	return e, vm, command, reserve, settle, disk, domainXML
}

func testFileSignal(path string) <-chan struct{} {
	ready := make(chan struct{})
	go func() {
		deadline := time.Now().Add(time.Minute)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(path); err == nil {
				close(ready)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return ready
}

func TestManagedSnapshotSettlementLostReplyReusesImmutableReceipt(t *testing.T) {
	e, vm := NewManaged(filepath.Join(t.TempDir(), "managed")), managedTestVM()
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	command, record, reservation := managedRecoveryEntryForTest(vm, "snapshot-lost-ack")
	reservation.OwnerOperationOrder = 1
	record.NativeXMLSHA256 = ""
	j := managedJournal{
		Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending"},
		Stage: "snapshot-failed-before-effects", Snapshot: &record,
		RecoveryReservation: &reservation, RecoveryStage: "failed-before-effects", RecoveryOutcome: "failed-before-effects",
	}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	rawBefore, err := readSmall(filepath.Join(e.stateDir, command.OperationID+".json"), managedSnapshotJournalLimit)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(rawBefore)
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "failed-before-effects", JournalSHA256: hex.EncodeToString(digest[:])}
	var firstReceipt protocol.PoolVMRecoveryEvidence
	first, err := e.settleSnapshotEvidence(context.Background(), j, evidence, func(_ context.Context, got protocol.PoolVMRecoveryEvidence) error {
		firstReceipt = got
		return errors.New("simulated lost settlement reply")
	})
	if err != nil || first.Status != "needs_attention" {
		t.Fatalf("lost acknowledgement result=%+v err=%v", first, err)
	}
	rawAfter, err := readSmall(filepath.Join(e.stateDir, command.OperationID+".json"), managedSnapshotJournalLimit)
	if err != nil || !bytes.Equal(rawBefore, rawAfter) {
		t.Fatalf("terminal journal changed after lost acknowledgement: err=%v", err)
	}
	second, err := e.settleSnapshotEvidence(context.Background(), j, evidence, func(_ context.Context, got protocol.PoolVMRecoveryEvidence) error {
		if got != firstReceipt {
			return errors.New("retry changed the previously submitted receipt")
		}
		return nil
	})
	if err != nil || second.Status != "failed" {
		t.Fatalf("receipt replay result=%+v err=%v", second, err)
	}
	settled, err := e.readJournal(command.OperationID + ".json")
	if err != nil || !settled.RecoverySettled || settled.Result.Status != "failed" {
		t.Fatalf("settlement acknowledgement was not journaled: %+v err=%v", settled, err)
	}
}

func TestManagedSnapshotPreflightCleanupIsDurableAndRefusesReservedIntent(t *testing.T) {
	e, vm := NewManaged(filepath.Join(t.TempDir(), "managed")), managedTestVM()
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	command, record, reservation := managedRecoveryEntryForTest(vm, "snapshot-preflight-cleanup")
	reservation.OwnerOperationOrder = 1
	path := filepath.Join(e.stateDir, command.OperationID+".json")
	preflight := managedJournal{
		Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending"},
		Stage: "snapshot-preflight", RecoveryStage: "preflight",
		Snapshot: &managedSnapshotRecord{SnapshotID: record.SnapshotID, Name: record.Name, Description: record.Description, CreatedAt: record.CreatedAt},
	}
	if err := e.saveSnapshotJournal(preflight); err != nil {
		t.Fatal(err)
	}
	if err := e.removePreflightSnapshot(context.Background(), command.OperationID); err != nil {
		t.Fatalf("remove preflight fence: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight fence remains: %v", err)
	}
	if err := e.removePreflightSnapshot(context.Background(), command.OperationID); err != nil {
		t.Fatalf("sync already-absent preflight fence: %v", err)
	}

	reserved := managedJournal{
		Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending"},
		Stage: "snapshot-reserved", RecoveryStage: "reserved", Snapshot: &record, RecoveryReservation: &reservation,
	}
	if err := e.saveSnapshotJournal(reserved); err != nil {
		t.Fatal(err)
	}
	if err := e.removePreflightSnapshot(context.Background(), command.OperationID); err == nil {
		t.Fatal("removed an acknowledged master reservation as a preflight fence")
	}
	if _, err := e.readJournal(command.OperationID + ".json"); err != nil {
		t.Fatalf("reserved snapshot journal was not preserved: %v", err)
	}
}

func TestManagedSnapshotPreflightCleanupFailureNeedsAttention(t *testing.T) {
	e, vm := NewManaged(filepath.Join(t.TempDir(), "managed")), managedTestVM()
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	command, record, reservation := managedRecoveryEntryForTest(vm, "snapshot-preflight-cleanup-failure")
	reservation.OwnerOperationOrder = 1
	j := managedJournal{
		Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending"},
		Stage: "snapshot-reserved", RecoveryStage: "reserved", Snapshot: &record, RecoveryReservation: &reservation,
	}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("snapshot recovery journal exceeds its durable record limit")
	result, err := e.failSnapshotPreflight(context.Background(), protocol.Result{OperationID: command.OperationID, Status: "failed"}, command.OperationID, cause)
	if err != nil || result.Status != "needs_attention" || !strings.Contains(result.Error, cause.Error()) || !strings.Contains(result.Error, "no longer safely removable") {
		t.Fatalf("cleanup refusal result=%+v err=%v", result, err)
	}
	if _, err := e.readJournal(command.OperationID + ".json"); err != nil {
		t.Fatalf("reserved journal was lost after cleanup refusal: %v", err)
	}
}

func TestManagedSnapshotAdmissionDenialNeverRunsNativeAndReplaysSettlement(t *testing.T) {
	e, vm := NewManaged(filepath.Join(t.TempDir(), "managed")), managedTestVM()
	vm.Boot.Firmware = "bios"
	vm.NICs, vm.MediaID = nil, ""
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	storageRoot := filepath.Join(t.TempDir(), "storage")
	vmDir := filepath.Join(storageRoot, "nex-vms", vm.ID)
	if err := os.MkdirAll(vmDir, 0700); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(vmDir, "disk-0.qcow2")
	if err := os.WriteFile(disk, []byte("stopped snapshot marker"), 0600); err != nil {
		t.Fatal(err)
	}
	domainXML, err := testDomainXML(vm, nil, []string{disk}, "")
	if err != nil {
		t.Fatal(err)
	}
	targets, mediaTarget, driversTarget := managedTargets(vm)
	createdAt := time.Now().UTC()
	storageRequest := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Generation: 1, Path: storageRoot, Kind: protocol.StorageKindLocal}
	storage := storageRequest
	localAccess := storageAccess{Path: storageRoot, Identity: "local:" + storageRoot + ":fsid=fixture-fsid", FilesystemID: "fixture-fsid", Total: 1 << 30, Available: 1 << 30}
	identityHash, err := snapshotStorageIdentityDigest(storage, localAccess)
	if err != nil {
		t.Fatal(err)
	}
	create := protocol.Command{OperationID: "snapshot-denial-seed", Kind: "vm-create", VM: &vm}
	if err := e.save(managedJournal{
		Command: create, Result: protocol.Result{OperationID: create.OperationID, Status: "succeeded"}, Stage: "defined",
		Disks: []string{disk}, DiskChains: [][]string{{disk}}, Targets: targets, MediaTarget: mediaTarget,
		DriversTarget: driversTarget, Domain: domainXML,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.readJournal(create.OperationID + ".json"); err != nil {
		t.Fatalf("read VM seed: %v", err)
	}
	storageCommand := protocol.Command{OperationID: "storage-seed", Kind: "storage-register", Storage: &storageRequest}
	if err := e.save(managedJournal{
		Command:        storageCommand,
		AppliedStorage: &storage, Result: protocol.Result{OperationID: "storage-seed", Status: "succeeded"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.readJournal(storageCommand.OperationID + ".json"); err != nil {
		t.Fatalf("read storage seed: %v", err)
	}
	command, record, reservation := managedRecoveryEntryForTest(vm, "snapshot-denial-op")
	command.Snapshot.Name = "existing-name"
	record.Name = command.Snapshot.Name
	record.CreatedAt = createdAt
	record.BeforeDomain = append([]byte(nil), domainXML...)
	record.Before.Disks[0].Source = disk
	record.Before.Disks[0].Layers = []string{disk}
	record.Before.Disks[0].StorageGeneration = 1
	record.Before.Disks[0].StorageIdentitySHA256 = identityHash
	record.Before.DefinitionSHA256, err = snapshotDefinitionDigest(domainXML)
	if err != nil {
		t.Fatal(err)
	}
	newHead, err := managedSnapshotExternalPath(vmDir, command.OperationID, targets[0])
	if err != nil {
		t.Fatal(err)
	}
	record.ExpectedAfter = managedSnapshotObservation{
		DomainState: "shut off", DefinitionSHA256: record.Before.DefinitionSHA256,
		Disks: []managedSnapshotDiskObservation{{Target: targets[0], StorageID: "s", StorageGeneration: 1,
			StorageIdentitySHA256: identityHash, Source: newHead, Layers: []string{newHead, disk}}},
	}
	record.StorageDependencies = []managedSnapshotStorageDependency{{StorageID: "s", Generation: 1,
		IdentitySHA256: identityHash, SnapshotArtifactPath: vmDir}}
	record.NativeXMLSHA256 = ""
	reservation.AdmissionRefusal = "name-in-use"
	reservation.OwnerOperationOrder = 1
	intent, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	reservation.IntentSHA256 = intent
	j := managedJournal{
		Command: command, Snapshot: &record,
		RecoveryStage: "local-intent", Stage: "snapshot-local-intent",
		Result: protocol.Result{OperationID: command.OperationID, Status: "pending", ObservedAt: createdAt},
	}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(context.Context, protocol.StorageRegistration, uint64) (storageAccess, error) {
		return localAccess, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	e.observeChains = func(_ context.Context, _ []managedJournal, candidate managedJournal) ([][]string, error) {
		chains := make([][]string, len(candidate.DiskChains))
		for i := range candidate.DiskChains {
			chains[i] = append([]string(nil), candidate.DiskChains[i]...)
		}
		return chains, nil
	}
	log := filepath.Join(t.TempDir(), "virsh.log")
	domainPath := filepath.Join(t.TempDir(), "domain.xml")
	if err := os.WriteFile(domainPath, domainXML, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VMHOST_DENIAL_LOG", log)
	t.Setenv("VMHOST_DENIAL_DOMAIN", domainPath)
	t.Setenv("VMHOST_DENIAL_DISK", disk)
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
dominfo) echo 'Autostart: disable';;
dumpxml) cat "$VMHOST_DENIAL_DOMAIN";;
snapshot-list) ;;
*) echo "$3" >> "$VMHOST_DENIAL_LOG"; exit 2;;
esac`)
	fakeCommand(t, "qemu-img", `case "$*" in
*--backing-chain*) printf '[{"filename":"%s","format":"qcow2"}]\n' "$VMHOST_DENIAL_DISK";;
*) exit 2;;
esac`)
	reserveCalls, settleCalls := 0, 0
	reserve := func(_ context.Context, got protocol.Command, gotIntent string, _ time.Time) (protocol.PoolVMRecoveryReservation, error) {
		reserveCalls++
		if got.OperationID != command.OperationID || gotIntent != intent {
			return protocol.PoolVMRecoveryReservation{}, errors.New("reservation callback received a different snapshot intent")
		}
		return reservation, nil
	}
	settle := func(_ context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
		settleCalls++
		if evidence.Reservation != reservation || evidence.Outcome != "failed-before-effects" {
			return errors.New("denial settlement did not carry the exact failed-before-effects receipt")
		}
		if settleCalls == 1 {
			return errors.New("simulated lost settlement acknowledgement")
		}
		return nil
	}
	first, err := e.CreateStoppedSnapshot(context.Background(), command, reserve, settle)
	if err != nil || first.Status != "needs_attention" {
		t.Fatalf("denied reservation with lost ACK result=%+v err=%v", first, err)
	}
	journalBeforeReplay, err := readSmall(filepath.Join(e.stateDir, command.OperationID+".json"), managedSnapshotJournalLimit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.CreateStoppedSnapshot(context.Background(), command, reserve, settle)
	if err != nil || second.Status != "failed" || !strings.Contains(second.Error, "already in use") {
		t.Fatalf("denied reservation replay result=%+v err=%v", second, err)
	}
	journal, err := e.readJournal(command.OperationID + ".json")
	if err != nil || !journal.RecoverySettled || journal.RecoveryOutcome != "failed-before-effects" || journal.RecoveryReservation == nil || journal.RecoveryReservation.AdmissionRefusal != "name-in-use" {
		t.Fatalf("denied reservation did not settle its exact journal: %+v err=%v", journal, err)
	}
	if reserveCalls != 1 || settleCalls != 2 {
		t.Fatalf("reservation/settlement callbacks called reserve=%d settle=%d", reserveCalls, settleCalls)
	}
	if calls, _ := os.ReadFile(log); strings.Contains(string(calls), "snapshot-create-as") {
		t.Fatalf("native snapshot creation ran despite master denial: %q", calls)
	}
	if _, err := os.Lstat(newHead); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied snapshot left an external output head: %v", err)
	}
	if !strings.Contains(string(journalBeforeReplay), "name-in-use") {
		t.Fatal("exact denial disposition was not persisted before failed-before-effects settlement")
	}
}

func TestSnapshotReserveRefusalClearsLocalIntentFence(t *testing.T) {
	e, _, command, _, settle, _, _ := stoppedSnapshotTestFixture(t, "snapshot-reserve-refuse")
	reserve := func(context.Context, protocol.Command, string, time.Time) (protocol.PoolVMRecoveryReservation, error) {
		return protocol.PoolVMRecoveryReservation{}, errors.New("pool master refused snapshot reservation")
	}
	first, err := e.CreateStoppedSnapshot(context.Background(), command, reserve, settle)
	if err != nil || first.Status != "failed" || !strings.Contains(first.Error, "refused snapshot reservation") {
		t.Fatalf("reserve refusal result=%+v err=%v", first, err)
	}
	if _, err := e.readJournal(command.OperationID + ".json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local intent remained after reserve refusal: %v", err)
	}
	// A later create for the same VM must not be fenced by the refused intent.
	command.OperationID = "snapshot-reserve-refuse-retry"
	command.Snapshot = &protocol.VMSnapshotRequest{Name: "after refusal"}
	reserveOK := func(_ context.Context, got protocol.Command, intent string, created time.Time) (protocol.PoolVMRecoveryReservation, error) {
		return protocol.PoolVMRecoveryReservation{
			OperationID: got.OperationID, Kind: "snapshot-create", OwnerOperationOrder: 1,
			PoolID: got.VM.PoolID, VMID: got.VM.ID, OwnerHostID: got.VM.HostID, Epoch: 1,
			SpecRevision: got.VM.Revision, SpecSHA256: strings.Repeat("a", 64),
			DiskIdentitySHA256: strings.Repeat("b", 64), IntentSHA256: intent,
		}, nil
	}
	// Native create will fail in this fixture (virsh exits 2); the point is admission past the fence.
	second, err := e.CreateStoppedSnapshot(context.Background(), command, reserveOK, settle)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status == "failed" && strings.Contains(second.Error, "unresolved local snapshot intent") {
		t.Fatalf("refused intent still fenced the VM: %+v", second)
	}
}

func TestAbortSnapshotLocalIntentRemovesUnreservedJournal(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	command := protocol.Command{OperationID: "snapAbort01deadbeef", Kind: "snapshot-create", VM: &vm,
		Snapshot: &protocol.VMSnapshotRequest{Name: "refused"}}
	record := managedSnapshotRecord{
		SnapshotID: command.OperationID, Name: "refused", CreatedAt: time.Now().UTC(),
		BeforeDomain: snapshotBeforeDomainFixture(), Before: snapshotObservationFixture(),
		ExpectedAfter: snapshotExpectedObservationFixture(),
		StorageDependencies: []managedSnapshotStorageDependency{{
			StorageID: "s", Generation: 2, IdentitySHA256: strings.Repeat("b", 64),
			SnapshotArtifactPath: "/srv/vm/recovery/" + command.OperationID,
		}},
	}
	j := managedJournal{Command: command, Snapshot: &record, RecoveryStage: "local-intent", Stage: "snapshot-local-intent",
		Result: protocol.Result{OperationID: command.OperationID, Status: "needs_attention", Error: "pool master refused snapshot reservation", ObservedAt: time.Now().UTC()}}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "virsh", `case "$3" in
snapshot-list) ;;
*) exit 2;;
esac`)
	result, err := e.abortSnapshotLocalIntent(context.Background(), j, errors.New("pool master refused snapshot reservation"))
	if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "refused snapshot reservation") {
		t.Fatalf("abort result=%+v err=%v", result, err)
	}
	if _, err := e.readJournal(command.OperationID + ".json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local intent journal remained: %v", err)
	}
	if err := recoveryVMReservationRefusal([]managedJournal{}, vm.ID, "other-op"); err != nil {
		t.Fatalf("cleared intent still refused other work: %v", err)
	}
}

// The master may have saved a reservation it could not confirm. Dropping the local
// intent then strands the master's copy (and, for a fresh VM, makes it undeletable).
func TestAbortSnapshotLocalIntentKeepsIntentWhenMasterReservationUnconfirmed(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	command := protocol.Command{OperationID: "snapKeep01deadbeef", Kind: "snapshot-create", VM: &vm,
		Snapshot: &protocol.VMSnapshotRequest{Name: "unconfirmed"}}
	record := managedSnapshotRecord{
		SnapshotID: command.OperationID, Name: "unconfirmed", CreatedAt: time.Now().UTC(),
		BeforeDomain: snapshotBeforeDomainFixture(), Before: snapshotObservationFixture(),
		ExpectedAfter: snapshotExpectedObservationFixture(),
		StorageDependencies: []managedSnapshotStorageDependency{{
			StorageID: "s", Generation: 2, IdentitySHA256: strings.Repeat("b", 64),
			SnapshotArtifactPath: "/srv/vm/recovery/" + command.OperationID,
		}},
	}
	j := managedJournal{Command: command, Snapshot: &record, RecoveryStage: "local-intent", Stage: "snapshot-local-intent",
		Result: protocol.Result{OperationID: command.OperationID, Status: "pending", ObservedAt: time.Now().UTC()}}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	cause := errors.Join(ErrSnapshotReservationUnconfirmed, errors.New("pool master refused snapshot reservation"))
	result, err := e.abortSnapshotLocalIntent(context.Background(), j, cause)
	if err != nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "retries it") {
		t.Fatalf("unconfirmed reservation result=%+v err=%v", result, err)
	}
	kept, err := e.readJournal(command.OperationID + ".json")
	if err != nil || kept.RecoveryStage != "local-intent" || kept.RecoveryReservation != nil || kept.Result.Status != "needs_attention" {
		t.Fatalf("local intent was not kept for a re-drive: %+v err=%v", kept, err)
	}
}

func TestSnapshotReserveValidationFailureKeepsJournal(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	command, record, good := managedRecoveryEntryForTest(vm, "snapReserveMismatch01")
	record.NativeXMLSHA256 = ""
	intent, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	good.IntentSHA256 = intent
	j := managedJournal{
		Command: command, Snapshot: &record,
		RecoveryStage: "local-intent", Stage: "snapshot-local-intent",
		Result: protocol.Result{OperationID: command.OperationID, Status: "pending", ObservedAt: record.CreatedAt},
	}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	reserve := func(_ context.Context, got protocol.Command, gotIntent string, _ time.Time) (protocol.PoolVMRecoveryReservation, error) {
		if got.OperationID != command.OperationID || gotIntent != intent {
			return protocol.PoolVMRecoveryReservation{}, errors.New("unexpected reservation callback")
		}
		bad := good
		bad.IntentSHA256 = strings.Repeat("f", 64)
		return bad, nil
	}
	settle := func(context.Context, protocol.PoolVMRecoveryEvidence) error {
		return errors.New("settlement must not run after reservation mismatch")
	}
	result, err := e.CreateStoppedSnapshot(context.Background(), command, reserve, settle)
	if err != nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "does not match the local snapshot intent") {
		t.Fatalf("mismatched reservation result=%+v err=%v", result, err)
	}
	journal, err := e.readJournal(command.OperationID + ".json")
	if err != nil {
		t.Fatalf("journal must remain after reserve validation failure: %v", err)
	}
	if journal.RecoveryStage != "local-intent" || journal.RecoveryReservation != nil {
		t.Fatalf("mismatched reservation must not bind or delete the local intent: stage=%q reservation=%v", journal.RecoveryStage, journal.RecoveryReservation != nil)
	}
	if journal.Result.Status != "needs_attention" {
		t.Fatalf("expected needs_attention on retained local intent: %+v", journal.Result)
	}
}

func TestSnapshotReserveSaveFailureNeedsAttention(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	vm := managedTestVM()
	vm.Boot.Firmware, vm.NICs, vm.MediaID = "bios", nil, ""
	command, record, reservation := managedRecoveryEntryForTest(vm, "snapReserveSaveFail01")
	record.NativeXMLSHA256 = ""
	intent, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		t.Fatal(err)
	}
	reservation.IntentSHA256 = intent
	j := managedJournal{
		Command: command, Snapshot: &record,
		RecoveryStage: "local-intent", Stage: "snapshot-local-intent",
		Result: protocol.Result{OperationID: command.OperationID, Status: "pending", ObservedAt: record.CreatedAt},
	}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	vmLock, err := managedSnapshotOperationLock(context.Background(), e.stateDir, vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	managedSnapshotUnlock(vmLock)
	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unlock(global)
	if err := os.Chmod(e.stateDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.stateDir, 0700) })
	reserve := func(_ context.Context, got protocol.Command, gotIntent string, _ time.Time) (protocol.PoolVMRecoveryReservation, error) {
		if got.OperationID != command.OperationID || gotIntent != intent {
			return protocol.PoolVMRecoveryReservation{}, errors.New("unexpected reservation callback")
		}
		return reservation, nil
	}
	settle := func(context.Context, protocol.PoolVMRecoveryEvidence) error {
		return errors.New("settlement must not run after save failure")
	}
	result, err := e.CreateStoppedSnapshot(context.Background(), command, reserve, settle)
	if result.Status != "needs_attention" {
		t.Fatalf("post-reserve save failure must not return disposable failed: result=%+v err=%v", result, err)
	}
	_ = os.Chmod(e.stateDir, 0700)
	journal, readErr := e.readJournal(command.OperationID + ".json")
	if readErr != nil {
		t.Fatalf("local intent journal was deleted after reserve succeeded: %v", readErr)
	}
	if journal.RecoveryStage != "local-intent" && journal.RecoveryReservation == nil {
		t.Fatalf("journal was cleared after reserve: %+v", journal)
	}
}

func TestSnapshotObservationDigestFailClosed(t *testing.T) {
	valid := snapshotObservationFixture()
	invalid := valid
	invalid.DomainState = "paused"
	if _, err := snapshotObservationDigest(invalid); err == nil {
		t.Fatal("invalid observation digest should error")
	}
	match, err := snapshotObservationDigestsMatch(invalid, invalid)
	if err == nil {
		t.Fatal("expected digest error for invalid observations")
	}
	if match {
		t.Fatal("invalid observations must not match when digests fail")
	}
	match, err = snapshotObservationDigestsMatch(valid, invalid)
	if match {
		t.Fatalf("valid observation matched invalid: err=%v", err)
	}
	ok, err := snapshotObservationMatchesHash(invalid, strings.Repeat("a", 64))
	if err == nil || ok {
		t.Fatalf("invalid observation must not match a hash: ok=%v err=%v", ok, err)
	}
	want, err := snapshotObservationDigest(valid)
	if err != nil {
		t.Fatal(err)
	}
	ok, err = snapshotObservationMatchesHash(valid, want)
	if err != nil || !ok {
		t.Fatalf("valid observation should match its digest: ok=%v err=%v", ok, err)
	}
}

func TestSnapshotReadonlyDiskTargetsForceSnapshotNo(t *testing.T) {
	domain := managedDomain{Type: "kvm", UUID: "00000000-0000-4000-8000-000000000001"}
	domain.Devices.Disks = []managedDisk{
		{Type: "file", Device: "disk", Source: &sourceXML{File: "/srv/vm/disk.qcow2"}, Target: targetXML{Dev: "vda"}},
		{Type: "file", Device: "cdrom", Source: &sourceXML{File: "/media/install.iso"}, Target: targetXML{Dev: "sda"}, Readonly: &struct{}{}},
		{Type: "file", Device: "disk", Source: &sourceXML{File: "/srv/vm/data.qcow2"}, Target: targetXML{Dev: "vdb"}, Readonly: &struct{}{}},
	}
	raw, err := xml.Marshal(domain)
	if err != nil {
		t.Fatal(err)
	}
	got := snapshotReadonlyDiskTargets(raw, map[string]bool{"vda": true})
	if !reflect.DeepEqual(got, []string{"sda", "vdb"}) {
		t.Fatalf("readonly diskspec targets=%v want [sda vdb]", got)
	}
	if got := snapshotReadonlyDiskTargets(raw, map[string]bool{"vda": true, "vdb": true}); !reflect.DeepEqual(got, []string{"sda"}) {
		t.Fatalf("writable readonly disk must be excluded: %v", got)
	}
}

// Every libvirt query on the snapshot preflight and execution paths proves nothing when libvirt
// never answered it (daemon or socket unreachable, killed on timeout), so each must classify as
// ErrOwnershipUnconfirmed, not a refusal. libvirt's own answer, such as a missing domain or
// snapshot, is final: retrying it would fence the VM forever.
func TestSnapshotProbeFailureIsUnconfirmedNotRefusal(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	vm := managedTestVM()
	current := managedJournal{
		Command: protocol.Command{OperationID: "seed", Kind: "vm-create", VM: &vm},
		Result:  protocol.Result{OperationID: "seed", Status: "succeeded"},
		Disks:   []string{"/srv/vm/disk.qcow2"}, Targets: []string{"vda"},
	}
	revert := managedJournal{Command: protocol.Command{VM: &vm}, SnapshotRevert: &managedSnapshotRevertRecord{}}
	ctx := context.Background()
	checks := map[string]func() error{
		"domstate": func() error { _, _, _, err := e.observeManagedSnapshot(ctx, nil, current, "stopped"); return err },
		"dominfo":  func() error { _, _, _, err := e.observeManagedSnapshot(ctx, nil, current, "stopped"); return err },
		"dumpxml":  func() error { _, _, _, err := e.observeManagedSnapshot(ctx, nil, current, "stopped"); return err },
		"snapshot-list": func() error {
			_, err := managedSnapshotAbsent(ctx, vm.ID, "n")
			return err
		},
		"snapshot-dumpxml": func() error {
			_, _, err := readManagedNativeSnapshotXML(ctx, vm.ID, "n")
			return err
		},
		"snapshot-current":     func() error { _, err := snapshotRevertCurrentName(ctx, vm.ID); return err },
		"revert-domstate":      func() error { _, _, err := e.observeSnapshotRevert(ctx, nil, revert); return err },
		"revert-dominfo":       func() error { _, _, err := e.observeSnapshotRevert(ctx, nil, revert); return err },
		"revert-dumpxml":       func() error { _, _, err := e.observeSnapshotRevert(ctx, nil, revert); return err },
		"revert-snapshot-list": func() error { return refuseSnapshotDeleteWithChildren(ctx, vm.ID, "n") },
	}
	for name, check := range checks {
		for _, reason := range []struct {
			name, stderr string
			unconfirmed  bool
		}{
			{"daemon unreachable", "Failed to connect socket to '/run/libvirt/libvirt-sock': No such file or directory", true},
			{"connection lost", "Cannot recv data: Connection reset by peer", true},
			{"timeout", "Timed out during operation: cannot acquire state change lock", true},
			{"domain missing", "Domain not found: no domain with matching name 'gone'", false},
			{"snapshot missing", "Domain snapshot not found: no domain snapshot with matching name 'gone'", false},
		} {
			t.Run(name+"/"+reason.name, func(t *testing.T) {
				fail := strings.TrimPrefix(name, "revert-")
				t.Setenv("FAIL_VIRSH", fail)
				t.Setenv("VIRSH_REASON", reason.stderr)
				fakeCommand(t, "virsh", `[ "$3" = "$FAIL_VIRSH" ] && { echo "error: $VIRSH_REASON" >&2; exit 1; }
case "$3" in domstate) echo "shut off";; dominfo) echo "Autostart: disable";; esac`)
				err := check()
				if err == nil || errors.Is(err, ErrOwnershipUnconfirmed) != reason.unconfirmed {
					t.Fatalf("%s failure %q: unconfirmed=%t want %t (%v)", name, reason.name, errors.Is(err, ErrOwnershipUnconfirmed), reason.unconfirmed, err)
				}
			})
		}
	}
	t.Run("killed client", func(t *testing.T) {
		fakeCommand(t, "virsh", `kill -9 $$`)
		if _, err := managedSnapshotAbsent(ctx, vm.ID, "n"); !errors.Is(err, ErrOwnershipUnconfirmed) {
			t.Fatalf("a killed virsh is not unconfirmed: %v", err)
		}
	})
}

// Firmware sources that fail local validation are refusals, not an unconfirmed query: no retry
// can change a NVRAM path outside libvirt's directory or a TPM source it does not own.
func TestSnapshotFirmwareSourceValidationIsARefusal(t *testing.T) {
	vm := managedTestVM()
	uefi := managedDomain{}
	uefi.OS.Firmware = "efi"
	uefi.OS.NVRAM = &nvramXML{Path: "/tmp/outside-libvirt/OVMF_VARS.fd"}
	if _, err := snapshotFirmwareSources(context.Background(), uefi, vm.ID, t.TempDir(), "op-1"); err == nil || errors.Is(err, ErrOwnershipUnconfirmed) {
		t.Fatalf("NVRAM outside libvirt's directory = %v, want a definite refusal", err)
	}
}

// A preflight that keeps failing to reach libvirt holds its journal only for the retry window.
func TestSnapshotPreflightUnconfirmedExpires(t *testing.T) {
	e, vm := NewManaged(filepath.Join(t.TempDir(), "managed")), managedTestVM()
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: "snapshot-preflight-stale", Kind: "snapshot-create", VM: &vm, Snapshot: &protocol.VMSnapshotRequest{Name: "point"}}
	record := managedSnapshotRecord{SnapshotID: command.OperationID, Name: "point", CreatedAt: time.Now().UTC()}
	started := time.Now().UTC().Add(-snapshotPreflightRetryWindow - time.Minute)
	j := managedJournal{Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending", ObservedAt: started},
		Stage: "snapshot-preflight", RecoveryStage: "preflight", Snapshot: &record}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	result, err := e.failSnapshotPreflight(context.Background(), protocol.Result{OperationID: command.OperationID, Status: "failed"}, command.OperationID, ErrOwnershipUnconfirmed)
	if err != nil || result.Status != "failed" {
		t.Fatalf("expired preflight result = %+v, %v", result, err)
	}
	if _, err := e.readJournal(command.OperationID + ".json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired preflight journal kept: %v", err)
	}
}

func TestManagedNativeSnapshotXMLParsesLibvirtRevertDisksTip(t *testing.T) {
	const tip = "/var/lib/libvirt/images/nex-vms/vm/disk-0.qcow2.aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	good := []byte(`<domainsnapshot>
  <name>point-a</name>
  <state>shutoff</state>
  <creationTime>1690000000</creationTime>
  <memory snapshot="no"/>
  <disks>
    <disk name="vda" snapshot="external" type="file">
      <driver type="qcow2"/>
      <source file="/var/lib/libvirt/images/nex-vms/vm/disk-0.qcow2"/>
    </disk>
  </disks>
  <revertDisks>
    <disk name="vda" snapshot="external" type="file">
      <driver type="qcow2"/>
      <source file="` + tip + `"/>
    </disk>
  </revertDisks>
</domainsnapshot>`)
	var snapshot managedNativeSnapshotXML
	if err := xml.Unmarshal(good, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RevertDisks) != 1 || snapshot.RevertDisks[0].Name != "vda" || snapshot.RevertDisks[0].Source.File != tip {
		t.Fatalf("libvirt revertDisks tip was not parsed: %#v", snapshot.RevertDisks)
	}
	wrongCase := []byte(strings.ReplaceAll(string(good), "revertDisks", "revertdisks"))
	var ignored managedNativeSnapshotXML
	if err := xml.Unmarshal(wrongCase, &ignored); err != nil {
		t.Fatal(err)
	}
	if len(ignored.RevertDisks) != 0 {
		t.Fatalf("lowercase revertdisks must not populate RevertDisks on Go's XML decoder: %#v", ignored.RevertDisks)
	}
}

// A libvirt restart makes the other-domain ownership probe fail; that is transient, so the
// preflight journal is kept and the result is not a terminal failure.
func TestManagedSnapshotPreflightKeepsOperationWhenOwnershipUnconfirmed(t *testing.T) {
	e, vm := NewManaged(filepath.Join(t.TempDir(), "managed")), managedTestVM()
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: "snapshot-ownership-restart", Kind: "snapshot-create", VM: &vm, Snapshot: &protocol.VMSnapshotRequest{Name: "point"}}
	record := managedSnapshotRecord{SnapshotID: command.OperationID, Name: "point", CreatedAt: time.Now().UTC()}
	j := managedJournal{Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending"},
		Stage: "snapshot-preflight", RecoveryStage: "preflight", Snapshot: &record}
	if err := e.saveSnapshotJournal(j); err != nil {
		t.Fatal(err)
	}
	result, err := e.failSnapshotPreflight(context.Background(), protocol.Result{OperationID: command.OperationID, Status: "failed"}, command.OperationID, ErrOwnershipUnconfirmed)
	if err != nil || result.Status != "needs_attention" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := e.readJournal(command.OperationID + ".json"); err != nil {
		t.Fatalf("preflight journal dropped: %v", err)
	}
}
