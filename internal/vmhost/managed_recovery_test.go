// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func managedRecoveryFixture(t *testing.T) (*ManagedEngine, protocol.VMCreateSpec, protocol.PoolVMRecoveryReservation) {
	t.Helper()
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.Mkdir(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	v := managedTestVM()
	create := protocol.Command{OperationID: "create-seed", Kind: "vm-create", VM: &v}
	if err := e.save(managedJournal{Command: create, Result: protocol.Result{OperationID: create.OperationID, Status: "succeeded"}, Stage: "defined"}); err != nil {
		t.Fatal(err)
	}
	command, snapshot, reservation := managedRecoveryEntryForTest(v, "snapshot-op")
	// Recovery reservations bind to the exact locally journaled VM definition.
	// Native completion remains needs_attention until the master durably settles it.
	if err := e.save(managedJournal{
		Command: command, Snapshot: &snapshot,
		Result: protocol.Result{OperationID: reservation.OperationID, Status: "needs_attention"}, Stage: "defined",
		RecoveryReservation: &reservation, RecoveryStage: "completed", RecoveryOutcome: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	return e, v, reservation
}

func managedRecoveryEntryForTest(v protocol.VMCreateSpec, operationID string) (protocol.Command, managedSnapshotRecord, protocol.PoolVMRecoveryReservation) {
	spec, _ := json.Marshal(v)
	disks, _ := json.Marshal(v.Disks)
	specHash, diskHash := sha256.Sum256(spec), sha256.Sum256(disks)
	command := protocol.Command{
		OperationID: operationID, Kind: "snapshot-create", VM: &v,
		Snapshot: &protocol.VMSnapshotRequest{Name: "recovery test"},
	}
	targets, _, _ := managedTargets(v)
	beforeDisks := make([]managedSnapshotDiskObservation, len(v.Disks))
	afterDisks := make([]managedSnapshotDiskObservation, len(v.Disks))
	dependencies := map[string]managedSnapshotStorageDependency{}
	for i, disk := range v.Disks {
		source := filepath.Join("/srv/nex-vms", v.ID, "disk-"+strconv.Itoa(i)+".qcow2")
		copyPath := filepath.Join("/srv/nex-vms", v.ID, "snapshots", operationID+"-"+strconv.Itoa(i)+".qcow2")
		storageHash := sha256.Sum256([]byte("storage:" + disk.StorageID))
		identity := hex.EncodeToString(storageHash[:])
		beforeDisks[i] = managedSnapshotDiskObservation{
			Target: targets[i], StorageID: disk.StorageID, StorageGeneration: 1, StorageIdentitySHA256: identity,
			Source: source, Layers: []string{source},
		}
		afterDisks[i] = managedSnapshotDiskObservation{
			Target: targets[i], StorageID: disk.StorageID, StorageGeneration: 1, StorageIdentitySHA256: identity,
			Source: copyPath, Layers: []string{copyPath, source},
		}
		if _, exists := dependencies[disk.StorageID]; !exists {
			dependencies[disk.StorageID] = managedSnapshotStorageDependency{
				StorageID: disk.StorageID, Generation: 1, IdentitySHA256: identity,
				SnapshotArtifactPath: filepath.Join("/srv/nex-vms", v.ID, "snapshots", operationID+"-"+disk.StorageID+".qcow2"),
			}
		}
	}
	depList := make([]managedSnapshotStorageDependency, 0, len(dependencies))
	for _, dependency := range dependencies {
		depList = append(depList, dependency)
	}
	record := managedSnapshotRecord{
		SnapshotID: operationID, Name: command.Snapshot.Name, Description: command.Snapshot.Description, CreatedAt: time.Now().UTC(),
		Before:              managedSnapshotObservation{DomainState: "shut off", Disks: beforeDisks},
		ExpectedAfter:       managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: strings.Repeat("c", 64), Disks: afterDisks},
		StorageDependencies: depList,
	}
	domain := managedDomain{Type: "kvm", UUID: v.ID}
	for _, disk := range beforeDisks {
		domain.Devices.Disks = append(domain.Devices.Disks, managedDisk{
			Type: "file", Device: "disk", Driver: driverXML{Type: "qcow2"},
			Source: &sourceXML{File: disk.Source}, Target: targetXML{Dev: disk.Target},
		})
	}
	record.BeforeDomain, _ = xml.Marshal(domain)
	record.Before.DefinitionSHA256, _ = snapshotDefinitionDigest(record.BeforeDomain)
	record.ExpectedAfter.DefinitionSHA256 = record.Before.DefinitionSHA256
	record.NativeXMLSHA256 = strings.Repeat("d", 64)
	intentHashHex, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		panic(err)
	}
	reservation := protocol.PoolVMRecoveryReservation{
		OperationID: operationID, Kind: "snapshot-create", OwnerOperationOrder: 1, PoolID: v.PoolID, VMID: v.ID, OwnerHostID: v.HostID,
		Epoch: 1, SpecRevision: v.Revision, SpecSHA256: hex.EncodeToString(specHash[:]),
		DiskIdentitySHA256: hex.EncodeToString(diskHash[:]), IntentSHA256: intentHashHex,
	}
	return command, record, reservation
}

func TestManagedRecoveryReservationFencesVMCommands(t *testing.T) {
	e, vm, _ := managedRecoveryFixture(t)
	updated := vm
	updated.Revision++
	commands := []protocol.Command{
		{OperationID: "start", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "start"}},
		{OperationID: "reboot", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "reboot"}},
		{OperationID: "edit", Kind: "vm-update", VM: &updated},
		{OperationID: "media", Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "eject"}},
		{OperationID: "delete", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID}},
	}
	for _, command := range commands {
		t.Run(command.OperationID, func(t *testing.T) {
			result, err := e.Execute(context.Background(), command, nil)
			if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "snapshot intent") {
				t.Fatalf("command crossed unresolved recovery fence: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestManagedDeleteRefusesSettledSnapshotUnderExecutionLock(t *testing.T) {
	for _, wipe := range []bool{false, true} {
		t.Run(strconv.FormatBool(wipe), func(t *testing.T) {
			e, vm, reservation := managedRecoveryFixture(t)
			command, snapshot, reservation := managedRecoveryEntryForTest(vm, reservation.OperationID)
			if err := e.save(managedJournal{
				Command: command, Snapshot: &snapshot,
				Result: protocol.Result{OperationID: reservation.OperationID, Status: "succeeded"}, Stage: "defined",
				RecoveryReservation: &reservation, RecoveryStage: "completed", RecoveryOutcome: "completed", RecoverySettled: true,
			}); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "virsh.log")
			fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_LOG"; exit 0`)
			t.Setenv("VMHOST_LOG", log)

			delete := protocol.Command{OperationID: "delete-after-snapshot", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{
				VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, DeleteDisks: wipe,
			}}
			result, err := e.Execute(context.Background(), delete, nil)
			if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "retained snapshots") {
				t.Fatalf("delete crossed settled snapshot gate: result=%+v err=%v", result, err)
			}
			if calls, _ := os.ReadFile(log); len(calls) != 0 {
				t.Fatalf("delete reached native commands before refusal: %q", calls)
			}
		})
	}
}

func TestFailedBeforeEffectsSnapshotDoesNotRetainDeleteGate(t *testing.T) {
	e, vm, reservation := managedRecoveryFixture(t)
	command, snapshot, reservation := managedRecoveryEntryForTest(vm, reservation.OperationID)
	if err := e.save(managedJournal{
		Command: command, Snapshot: &snapshot,
		Result: protocol.Result{OperationID: reservation.OperationID, Status: "failed"}, Stage: "preflight",
		RecoveryReservation: &reservation, RecoveryStage: "failed-before-effects", RecoveryOutcome: "failed-before-effects", RecoverySettled: true,
	}); err != nil {
		t.Fatal(err)
	}
	js := mustManagedJournals(t, e)
	if err := retainedSnapshotDeleteRefusal(js, vm.ID); err != nil {
		t.Fatalf("failed-before-effects operation retained snapshot delete gate: %v", err)
	}
}

func TestManagedRecoveryReservationDoesNotReplayCreateAutostart(t *testing.T) {
	e, vm, reservation := managedRecoveryFixture(t)
	vm.StartAfterCreate = true
	command, snapshot, reservation := managedRecoveryEntryForTest(vm, reservation.OperationID)
	if !recoverySpecMatches(vm, reservation) {
		t.Fatal("reservation does not bind to the start-after-create VM spec")
	}
	if err := e.save(managedJournal{
		Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending"}, Stage: "defined",
		Snapshot: &snapshot, RecoveryReservation: &reservation, RecoveryStage: "native",
	}); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "virsh.log")
	fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_LOG"`)
	t.Setenv("VMHOST_LOG", log)

	result, err := e.Execute(context.Background(), command, nil)
	if err != nil || result.Status != "needs_attention" {
		t.Fatalf("reserved exact replay result=%+v err=%v", result, err)
	}
	journal, err := e.readJournal(command.OperationID + ".json")
	if err != nil || journal.Result.Status != "pending" {
		t.Fatalf("exact replay changed durable native-operation state: journal=%+v err=%v", journal.Result, err)
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 {
		t.Fatalf("reserved create replay issued native/autostart commands: %q", calls)
	}
}

func TestManagedRecoveryReservationFencesPromotionOfOtherVMJournal(t *testing.T) {
	e, vm, _ := managedRecoveryFixture(t)
	updated := vm
	updated.Revision++
	command := protocol.Command{OperationID: "interrupted-edit", Kind: "vm-update", VM: &updated}
	if err := e.save(managedJournal{
		Command: command,
		Result:  protocol.Result{OperationID: command.OperationID, Status: "pending", ObservedAt: time.Now().Add(-2 * time.Minute)},
		Stage:   "defined",
	}); err != nil {
		t.Fatal(err)
	}
	observed := false
	e.observe = func(context.Context, managedJournal, string) error {
		observed = true
		return nil
	}

	js := mustManagedJournals(t, e)
	_ = e.promote(context.Background(), js, nil)
	journal, err := e.readJournal(command.OperationID + ".json")
	if err != nil || observed || journal.Result.Status != "pending" {
		t.Fatalf("reserved VM's unrelated journal was promoted: observed=%v journal=%+v err=%v", observed, journal.Result, err)
	}
}

func TestVMDefinitionReturnsCurrentJournaledSpecWithoutNativeObservation(t *testing.T) {
	e, vm, _ := managedRecoveryFixture(t)
	vm.Revision++
	vm.Description = "current journaled revision"
	if err := e.save(managedJournal{
		Command: protocol.Command{OperationID: "current-edit", Kind: "vm-update", VM: &vm},
		Result:  protocol.Result{OperationID: "current-edit", Status: "succeeded"}, Stage: "defined",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := e.VMDefinition(vm.ID)
	if err != nil || !reflect.DeepEqual(got, vm) {
		t.Fatalf("current definition=%+v err=%v", got, err)
	}
	deleted := protocol.Command{OperationID: "delete-current", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID}}
	if err := e.save(managedJournal{Command: deleted, Result: protocol.Result{OperationID: deleted.OperationID, Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.VMDefinition(vm.ID); err == nil {
		t.Fatal("deleted VM definition was returned")
	}
}

func TestManagedRecoveryReservationAllowsEmergencyStopAndUnrelatedVM(t *testing.T) {
	e, vm, _ := managedRecoveryFixture(t)
	other := managedTestVM()
	other.ID = "87654321-1234-4234-8234-123456789012"
	other.Name = "other"
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "other-create", Kind: "vm-create", VM: &other}, Result: protocol.Result{OperationID: "other-create", Status: "succeeded"}, Stage: "defined"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"shutdown", "force-stop"} {
		command := protocol.Command{OperationID: "stop-" + action, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: action}}
		if err := recoveryReservationRefusal(mustManagedJournals(t, e), command); err != nil {
			t.Fatalf("emergency %s was fenced: %v", action, err)
		}
		log := filepath.Join(t.TempDir(), "virsh.log")
		fakeCommand(t, "virsh", `case "$3" in
domstate) echo running;;
shutdown|destroy) echo "$3" >> "$VMHOST_LOG";;
*) exit 2;; esac`)
		t.Setenv("VMHOST_LOG", log)
		result, err := e.Execute(context.Background(), command, nil)
		if err != nil || result.Status != "succeeded" {
			t.Fatalf("emergency %s command result=%+v err=%v", action, result, err)
		}
		calls, _ := os.ReadFile(log)
		want := action
		if action == "force-stop" {
			want = "destroy"
		}
		if !strings.Contains(string(calls), want) {
			t.Fatalf("emergency %s did not reach libvirt: %q", action, calls)
		}
	}
	if err := recoveryReservationRefusal(mustManagedJournals(t, e), protocol.Command{OperationID: "other-start", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: other.ID, PoolID: other.PoolID, HostID: other.HostID, Action: "start"}}); err != nil {
		t.Fatalf("unrelated VM was fenced: %v", err)
	}
}

func TestManagedRecoveryReservationFencesStorageDetach(t *testing.T) {
	e, vm, _ := managedRecoveryFixture(t)
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Path: t.TempDir(), Kind: protocol.StorageKindLocal}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage", Kind: "storage-register", Storage: &storage}, Result: protocol.Result{OperationID: "storage", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	// A later tombstone removes the live definition reference; the retained recovery
	// reservation must independently keep its disk repository attached.
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "legacy-delete", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID}}, Result: protocol.Result{OperationID: "legacy-delete", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: "detach", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: storage.ID, PoolID: storage.PoolID, HostID: storage.HostID, ExpectedGeneration: 1}}
	result, err := e.Execute(context.Background(), command, nil)
	if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "snapshot recovery reservation") {
		t.Fatalf("detach crossed unresolved recovery fence: result=%+v err=%v", result, err)
	}
}

func mustManagedJournals(t *testing.T, e *ManagedEngine) []managedJournal {
	t.Helper()
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	return js
}
