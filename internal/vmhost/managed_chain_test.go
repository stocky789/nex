// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestParseManagedDiskChain(t *testing.T) {
	head := filepath.Join(string(filepath.Separator), "storage", "head.qcow2")
	base := filepath.Join(string(filepath.Separator), "storage", "base.qcow2")
	valid, _ := json.Marshal([]map[string]string{
		{"filename": head, "format": "qcow2", "backing-filename": "base.qcow2", "full-backing-filename": base},
		{"filename": base, "format": "qcow2"},
	})
	got, err := parseManagedDiskChain(head, valid)
	if err != nil || !reflect.DeepEqual(got, []string{head, base}) {
		t.Fatalf("chain=%v err=%v", got, err)
	}
	for name, body := range map[string]string{
		"reordered parent": `[{"filename":"` + head + `","format":"qcow2","backing-filename":"other.qcow2"},{"filename":"` + base + `","format":"qcow2"}]`,
		"truncated":        `[{"filename":"` + head + `","format":"qcow2","backing-filename":"base.qcow2"}]`,
		"cycle":            `[{"filename":"` + head + `","format":"qcow2","backing-filename":"` + head + `"},{"filename":"` + head + `","format":"qcow2"}]`,
		"raw layer":        `[{"filename":"` + head + `","format":"raw"}]`,
		"relative head":    `[{"filename":"head.qcow2","format":"qcow2"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseManagedDiskChain(head, []byte(body)); err == nil {
				t.Fatal("invalid chain accepted")
			}
		})
	}
	deep := make([]map[string]string, maxManagedDiskChain+1)
	for i := range deep {
		path := filepath.Join(string(filepath.Separator), "storage", fmt.Sprintf("layer-%03d.qcow2", i))
		deep[i] = map[string]string{"filename": path, "format": "qcow2"}
		if i+1 < len(deep) {
			next := filepath.Join(string(filepath.Separator), "storage", fmt.Sprintf("layer-%03d.qcow2", i+1))
			deep[i]["backing-filename"] = filepath.Base(next)
			deep[i]["full-backing-filename"] = next
		}
	}
	deepJSON, _ := json.Marshal(deep)
	if _, err := parseManagedDiskChain(filepath.Join(string(filepath.Separator), "storage", "layer-000.qcow2"), deepJSON); err == nil {
		t.Fatal("chain deeper than native observation bound accepted")
	}
}

func stubManagedDiskChains(e *ManagedEngine) {
	e.observeChains = func(_ context.Context, _ []managedJournal, j managedJournal) ([][]string, error) {
		return managedDiskChains(j)
	}
}

func managedTestEngine(stateDir string) *ManagedEngine {
	e := NewManaged(stateDir)
	stubManagedDiskChains(e)
	return e
}

func TestManagedDiskChainsRequireJournalHeads(t *testing.T) {
	j := managedJournal{Disks: []string{"/vm/a.qcow2", "/vm/b.qcow2"}, DiskChains: [][]string{{"/vm/a.qcow2", "/vm/base.qcow2"}}}
	if _, err := managedDiskChains(j); err == nil {
		t.Fatal("misaligned chains accepted")
	}
	j.DiskChains = [][]string{{"/vm/wrong.qcow2"}, {"/vm/b.qcow2"}}
	if _, err := managedDiskChains(j); err == nil {
		t.Fatal("chain with wrong head accepted")
	}
}

func TestManagedDiskXMLUsesLiveDefinitionForPausedGuests(t *testing.T) {
	for _, state := range []string{"running", "paused", "pmsuspended", "in shutdown"} {
		args := managedDiskXMLArgs("vm-1", state)
		if !reflect.DeepEqual(args, []string{"dumpxml", "vm-1"}) {
			t.Fatalf("%s selected inactive definition: %v", state, args)
		}
	}
	if args := managedDiskXMLArgs("vm-1", "shut off"); !reflect.DeepEqual(args, []string{"dumpxml", "vm-1", "--inactive"}) {
		t.Fatalf("stopped guest did not select persistent definition: %v", args)
	}
}

func TestVerifyManagedDiskHeadsRequiresExactUniqueWritableTargets(t *testing.T) {
	tests := []struct {
		name    string
		domain  string
		targets []string
		disks   []string
		wantErr bool
	}{
		{
			name:    "extra writable disk",
			domain:  `<domain><devices><disk device="disk"><source file="/vm/a.qcow2"/><target dev="vda"/></disk><disk device="disk"><source file="/vm/extra.qcow2"/><target dev="vdb"/></disk></devices></domain>`,
			targets: []string{"vda"}, disks: []string{"/vm/a.qcow2"}, wantErr: true,
		},
		{
			name:    "duplicate writable target",
			domain:  `<domain><devices><disk device="disk"><source file="/vm/a.qcow2"/><target dev="vda"/></disk><disk device="disk"><source file="/vm/b.qcow2"/><target dev="vda"/></disk></devices></domain>`,
			targets: []string{"vda"}, disks: []string{"/vm/a.qcow2"}, wantErr: true,
		},
		{
			name:    "duplicate writable source",
			domain:  `<domain><devices><disk device="disk"><source file="/vm/a.qcow2"/><target dev="vda"/></disk><disk device="disk"><source file="/vm/a.qcow2"/><target dev="vdb"/></disk></devices></domain>`,
			targets: []string{"vda", "vdb"}, disks: []string{"/vm/a.qcow2", "/vm/b.qcow2"}, wantErr: true,
		},
		{
			name:    "writable LUN",
			domain:  `<domain><devices><disk device="disk"><source file="/vm/a.qcow2"/><target dev="vda"/></disk><disk device="lun"><source file="/vm/extra.qcow2"/><target dev="sda"/></disk></devices></domain>`,
			targets: []string{"vda"}, disks: []string{"/vm/a.qcow2"}, wantErr: true,
		},
		{
			name:    "writable disk with missing device type",
			domain:  `<domain><devices><disk device="disk"><source file="/vm/a.qcow2"/><target dev="vda"/></disk><disk><source file="/vm/extra.qcow2"/><target dev="vdb"/></disk></devices></domain>`,
			targets: []string{"vda"}, disks: []string{"/vm/a.qcow2"}, wantErr: true,
		},
		{
			name:    "readonly media ignored",
			domain:  `<domain><devices><disk device="disk"><source file="/vm/a.qcow2"/><target dev="vda"/></disk><disk device="disk"><source file="/media/data.iso"/><target dev="sda"/><readonly/></disk><disk device="cdrom"><source file="/media/install.iso"/><target dev="sdb"/><readonly/></disk></devices></domain>`,
			targets: []string{"vda"}, disks: []string{"/vm/a.qcow2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var domain managedDomain
			if err := xml.Unmarshal([]byte(tt.domain), &domain); err != nil {
				t.Fatal(err)
			}
			err := verifyManagedDiskHeads(domain, tt.targets, tt.disks)
			if (err != nil) != tt.wantErr {
				t.Fatalf("verifyManagedDiskHeads() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyManagedDiskChainsBindsXMLJournalAndRegisteredStorage(t *testing.T) {
	storageRoot := t.TempDir()
	vm := managedTestVM()
	vmDir := filepath.Join(storageRoot, "nex-vms", vm.ID)
	if err := os.MkdirAll(vmDir, 0700); err != nil {
		t.Fatal(err)
	}
	head, base := filepath.Join(vmDir, "head.qcow2"), filepath.Join(vmDir, "base.qcow2")
	for _, path := range []string{head, base} {
		if err := os.WriteFile(path, []byte("qcow2"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	domain := `<domain><devices><disk type="file" device="disk"><source file="` + head + `"/><target dev="vda"/></disk></devices></domain>`
	t.Setenv("VMHOST_CHAIN_JSON", `[{"filename":"`+head+`","format":"qcow2","backing-filename":"base.qcow2","full-backing-filename":"`+base+`"},{"filename":"`+base+`","format":"qcow2"}]`)
	t.Setenv("VMHOST_DOMAIN_XML", domain)
	fakeCommand(t, "virsh", `case "$3" in domstate) echo 'shut off';; dumpxml) printf '%s\n' "$VMHOST_DOMAIN_XML";; list) printf '%s\n' "$VMHOST_VM_ID";; *) exit 2;; esac`)
	t.Setenv("VMHOST_VM_ID", vm.ID)
	fakeCommand(t, "qemu-img", `case "$*" in *--backing-chain*) printf '%s\n' "$VMHOST_CHAIN_JSON";; *) case "$5" in *head.qcow2*) printf '%s\n' '{"format":"qcow2","backing-filename":"base.qcow2"}';; *) printf '%s\n' '{"format":"qcow2"}';; esac;; esac`)
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, storage protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		return storageAccess{Path: storageRoot, Total: 1 << 40, Available: 1 << 40}, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Kind: protocol.StorageKindNFS}
	js := []managedJournal{{Command: protocol.Command{Kind: "storage-register", Storage: &storage}, Result: protocol.Result{Status: "succeeded"}}}
	j := managedJournal{
		Command: protocol.Command{Kind: "vm-create", VM: &vm}, Disks: []string{head}, DiskChains: [][]string{{head, base}}, Targets: []string{"vda"},
	}
	if _, err := verifyManagedDiskChains(context.Background(), js, j); err != nil {
		t.Fatalf("valid observed chain refused: %v", err)
	}
	withExtraDisk := strings.Replace(domain, "</devices>", `<disk type="file" device="disk"><source file="`+filepath.Join(vmDir, "unmanaged.qcow2")+`"/><target dev="vdb"/></disk></devices>`, 1)
	t.Setenv("VMHOST_DOMAIN_XML", withExtraDisk)
	if _, err := verifyManagedDiskChains(context.Background(), js, j); err == nil {
		t.Fatal("unmanaged writable disk was not refused")
	}
	withReadonlyMedia := strings.Replace(domain, "</devices>", `<disk type="file" device="cdrom"><source file="/media/install.iso"/><target dev="sda"/><readonly/></disk></devices>`, 1)
	t.Setenv("VMHOST_DOMAIN_XML", withReadonlyMedia)
	if _, err := verifyManagedDiskChains(context.Background(), js, j); err != nil {
		t.Fatalf("read-only CD-ROM media refused: %v", err)
	}
	t.Setenv("VMHOST_DOMAIN_XML", strings.Replace(domain, head, base, 1))
	if _, err := verifyManagedDiskChains(context.Background(), js, j); err == nil || !strings.Contains(err.Error(), "head differs") {
		t.Fatalf("mismatched XML head was accepted: %v", err)
	}
}

func TestDeleteDiskSetRejectsForeignReferenceToRetainedDisk(t *testing.T) {
	storageRoot := t.TempDir()
	vm := managedTestVM()
	vmDir := filepath.Join(storageRoot, "nex-vms", vm.ID)
	if err := os.MkdirAll(vmDir, 0700); err != nil {
		t.Fatal(err)
	}
	head, retained := filepath.Join(vmDir, "current.qcow2"), filepath.Join(vmDir, "retained.qcow2")
	foreignHead := filepath.Join(t.TempDir(), "foreign.qcow2")
	for _, path := range []string{head, retained, foreignHead} {
		if err := os.WriteFile(path, []byte("qcow2"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Path: storageRoot, Kind: protocol.StorageKindLocal}
	storageJournal := managedJournal{Command: protocol.Command{Kind: "storage-register", Storage: &storage}, Result: protocol.Result{Status: "succeeded"}}
	vmJournal := managedJournal{Command: protocol.Command{Kind: "vm-create", VM: &vm}, Result: protocol.Result{Status: "succeeded"}, Disks: []string{head}, DiskChains: [][]string{{head, retained}}, Targets: []string{"vda"}}
	delete := managedJournal{Command: protocol.Command{Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, DeleteDisks: true}}, Result: protocol.Result{Status: "needs_attention"}, Retained: []string{head, retained}, Stage: "undefining"}
	js := []managedJournal{storageJournal, vmJournal}
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, _ protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		return storageAccess{Path: storageRoot, Total: 1 << 40, Available: 1 << 40}, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })

	ownerID, foreignID := vm.ID, "6fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	log := filepath.Join(t.TempDir(), "virsh.log")
	t.Setenv("VMHOST_LOG", log)
	t.Setenv("VMHOST_IDS", ownerID+"\n"+foreignID)
	t.Setenv("VMHOST_XML", `<domain><devices><disk device="disk"><source file="`+foreignHead+`"/></disk></devices></domain>`)
	t.Setenv("VMHOST_CHAIN_JSON", `[{"filename":"`+foreignHead+`","format":"qcow2","backing-filename":"retained.qcow2","full-backing-filename":"`+retained+`"},{"filename":"`+retained+`","format":"qcow2"}]`)
	fakeCommand(t, "virsh", `case "$3" in list) printf '%s\n' "$VMHOST_IDS";; dumpxml) printf '%s\n' "$VMHOST_XML";; undefine) echo called >> "$VMHOST_LOG";; *) exit 2;; esac`)
	fakeCommand(t, "qemu-img", `printf '%s\n' "$VMHOST_CHAIN_JSON"`)
	e := NewManaged(t.TempDir())
	if err := e.finishDelete(context.Background(), &delete, js, true); err == nil || !strings.Contains(err.Error(), "another domain") {
		t.Fatalf("foreign domain's retained-disk reference was not refused: %v", err)
	}
	if _, err := os.Stat(retained); err != nil {
		t.Fatalf("refused deletion modified the retained disk: %v", err)
	}
	if called, _ := os.ReadFile(log); len(called) != 0 {
		t.Fatalf("refused deletion reached undefine: %q", called)
	}

	// A resumed delete of an already undefined VM does not query its domstate. If the foreign
	// reference is gone, the remaining journaled files can be safely retried.
	t.Setenv("VMHOST_IDS", ownerID)
	delete.Stage = "wiping"
	if err := e.verifyManagedDeleteFiles(context.Background(), js, delete); err != nil {
		t.Fatalf("safe retry of undefined VM's retained disk set failed: %v", err)
	}
}

func TestPoolVMRecoveryEvidenceRefusesPartialAndOnlyListsJournalBindings(t *testing.T) {
	e := NewManaged(t.TempDir())
	v := managedTestVM()
	command, snapshot, reservation := managedRecoveryEntryForTest(v, "recovery-op")
	j := managedJournal{
		Command: command, Snapshot: &snapshot,
		Result: protocol.Result{OperationID: reservation.OperationID, Status: "pending"},
		Stage:  "defined", RecoveryReservation: &reservation, RecoveryStage: "native",
	}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	secondCommand, secondSnapshot, second := managedRecoveryEntryForTest(v, "recovery-op-2")
	secondJournal := managedJournal{
		Command: secondCommand, Snapshot: &secondSnapshot,
		Result: protocol.Result{OperationID: second.OperationID, Status: "pending"},
		Stage:  "defined", RecoveryReservation: &second, RecoveryStage: "native",
	}
	if err := e.save(secondJournal); err != nil {
		t.Fatal(err)
	}
	got, err := e.PoolVMRecoveryReservations()
	if err != nil || !reflect.DeepEqual(got, []protocol.PoolVMRecoveryReservation{reservation, second}) {
		t.Fatalf("reservations=%+v err=%v", got, err)
	}
	for _, item := range got {
		if _, err := e.PoolVMRecoveryEvidence(context.Background(), item); err == nil {
			t.Fatal("partial native operation produced settlement evidence")
		}
	}
	other := reservation
	other.Epoch++
	if _, err := e.PoolVMRecoveryEvidence(context.Background(), other); err == nil {
		t.Fatal("mismatched pool reservation produced evidence")
	}
}

func TestPoolVMRecoveryEvidenceRejectsChangedStorageIdentityOrGeneration(t *testing.T) {
	for _, change := range []string{"unchanged", "identity", "generation"} {
		t.Run(change, func(t *testing.T) {
			e := NewManaged(t.TempDir())
			vm := managedTestVM()
			command, snapshot, reservation := managedRecoveryEntryForTest(vm, "recovery-op")
			storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Name: "local", Kind: protocol.StorageKindLocal, Path: "/srv"}
			access := storageAccess{Path: storage.Path, Identity: "local:" + storage.Path + ":fsid=fsid-a", FilesystemID: "fsid-a"}
			identity, err := snapshotStorageIdentityDigest(storage, access)
			if err != nil {
				t.Fatal(err)
			}
			for i := range snapshot.Before.Disks {
				snapshot.Before.Disks[i].StorageIdentitySHA256 = identity
				snapshot.ExpectedAfter.Disks[i].StorageIdentitySHA256 = identity
			}
			for i := range snapshot.StorageDependencies {
				snapshot.StorageDependencies[i].IdentitySHA256 = identity
			}
			snapshot.NativeXMLSHA256 = ""
			intent, err := managedSnapshotIntentDigest(command, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			reservation.IntentSHA256 = intent
			if err := e.save(managedJournal{
				Command: command, Snapshot: &snapshot,
				Result: protocol.Result{OperationID: command.OperationID, Status: "pending"}, Stage: "snapshot-failed-before-effects",
				RecoveryReservation: &reservation, RecoveryStage: "failed-before-effects", RecoveryOutcome: "failed-before-effects",
			}); err != nil {
				t.Fatal(err)
			}
			storageJournal := managedJournal{Command: protocol.Command{OperationID: "storage", Kind: "storage-register", Storage: &storage}, Result: protocol.Result{OperationID: "storage", Status: "succeeded"}}
			if err := e.save(storageJournal); err != nil {
				t.Fatal(err)
			}
			if change == "generation" {
				storage.Generation = 2
				storageJournal.Command.Storage = &storage
				if err := e.save(storageJournal); err != nil {
					t.Fatal(err)
				}
			}
			domainXML := string(snapshot.BeforeDomain)
			t.Setenv("VMHOST_DOMAIN_XML", domainXML)
			fakeCommand(t, "virsh", `case "$3" in snapshot-list) exit 0;; domstate) echo 'shut off';; dominfo) echo 'Autostart: disable';; dumpxml) printf '%s\n' "$VMHOST_DOMAIN_XML";; *) exit 2;; esac`)
			previousReady := managedStorageReadyFn
			managedStorageReadyFn = func(_ context.Context, requested protocol.StorageRegistration, _ uint64) (storageAccess, error) {
				current := access
				if change == "identity" {
					current.FilesystemID = "fsid-replaced"
					current.Identity = "local:" + requested.Path + ":fsid=" + current.FilesystemID
				}
				return current, nil
			}
			t.Cleanup(func() { managedStorageReadyFn = previousReady })
			e.observeChains = func(_ context.Context, _ []managedJournal, j managedJournal) ([][]string, error) {
				return managedDiskChains(j)
			}
			_, err = e.PoolVMRecoveryEvidence(context.Background(), reservation)
			if change == "unchanged" {
				if err != nil {
					t.Fatalf("unchanged storage binding was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("lookalike disk chain with changed storage identity or generation produced settlement evidence")
			}
			if !strings.Contains(err.Error(), "storage identity or generation") {
				t.Fatalf("settlement failed before the storage binding check: %v", err)
			}
		})
	}
}

func TestPoolVMRecoveryReservationsOnFreshHost(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed-vms"))
	got, err := e.PoolVMRecoveryReservations()
	if err != nil || len(got) != 0 {
		t.Fatalf("fresh host reservations=%v err=%v", got, err)
	}
}

// A snapshot delete unlinks the overlay its snapshot-create journal still names; the VM
// delete that follows must not treat that gone file as an unavailable disk (X-R1).
func TestDeleteDiskSetSkipsOverlayUnlinkedBySnapshotDelete(t *testing.T) {
	storageRoot := t.TempDir()
	vm := managedTestVM()
	vmDir := filepath.Join(storageRoot, "nex-vms", vm.ID)
	if err := os.MkdirAll(vmDir, 0700); err != nil {
		t.Fatal(err)
	}
	base, overlay := filepath.Join(vmDir, "disk-0.qcow2"), filepath.Join(vmDir, "snap.qcow2")
	if err := os.WriteFile(base, []byte("qcow2"), 0600); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Path: storageRoot, Kind: protocol.StorageKindLocal}
	storageJournal := managedJournal{Command: protocol.Command{Kind: "storage-register", Storage: &storage}, Result: protocol.Result{Status: "succeeded"}}
	created := managedJournal{Command: protocol.Command{Kind: "snapshot-create", VM: &vm}, Result: protocol.Result{Status: "succeeded"}, Disks: []string{overlay}, DiskChains: [][]string{{overlay, base}}, Targets: []string{"vda"}}
	current := managedJournal{Command: protocol.Command{Kind: "vm-create", VM: &vm}, Result: protocol.Result{Status: "succeeded"}, Disks: []string{base}, DiskChains: [][]string{{base}}, Targets: []string{"vda"}}
	js := []managedJournal{storageJournal, created, current}
	paths := managedDeleteDiskPaths(js, current)
	if len(paths) != 1 || paths[0] != base {
		t.Fatalf("delete disk set = %v, want only the existing base", paths)
	}
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, _ protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		return storageAccess{Path: storageRoot, Total: 1 << 40, Available: 1 << 40}, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	fakeCommand(t, "virsh", `case "$3" in list) echo "`+vm.ID+`";; *) exit 2;; esac`)
	del := managedJournal{Command: protocol.Command{Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, DeleteDisks: true}}, Retained: paths, Stage: "undefining"}
	if err := NewManaged(t.TempDir()).verifyManagedDeleteFiles(context.Background(), js, del); err != nil {
		t.Fatalf("delete refused after snapshot delete unlinked its overlay: %v", err)
	}
}
