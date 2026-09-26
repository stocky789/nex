// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedDeleteRetainsOrWipesDisks(t *testing.T) {
	vm := managedTestVM()
	vm.MediaID = ""
	storageRoot := t.TempDir()
	disk := filepath.Join(storageRoot, "nex-vms", vm.ID, "disk-0.qcow2")
	if err := os.MkdirAll(filepath.Dir(disk), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("guest-data"), 0600); err != nil {
		t.Fatal(err)
	}
	seed := func(t *testing.T, e *ManagedEngine) {
		t.Helper()
		if err := os.MkdirAll(e.stateDir, 0700); err != nil {
			t.Fatal(err)
		}
		create := protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &vm}
		storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Path: storageRoot, Kind: protocol.StorageKindLocal}
		if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &storage}, Result: protocol.Result{OperationID: "storage-1", Status: "succeeded"}}); err != nil {
			t.Fatal(err)
		}
		if err := e.save(managedJournal{
			Command: create,
			Result:  protocol.Result{OperationID: create.OperationID, Status: "succeeded"},
			Stage:   "defined",
			Disks:   []string{disk},
			Targets: []string{"vda"}, MediaTarget: "sda",
		}); err != nil {
			t.Fatal(err)
		}
	}
	command := func(op string, wipe bool) protocol.Command {
		return protocol.Command{OperationID: op, Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, DeleteDisks: wipe}}
	}

	t.Run("retain", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "state")
		log := filepath.Join(t.TempDir(), "virsh.log")
		fakeCommand(t, "virsh", `echo "$3 $4 $5" >> "$VMHOST_LOG"; case "$3" in
domstate) echo 'shut off';;
list) ;;
undefine) [ "$5" = "--nvram" ] || exit 3; exit 0;;
*) exit 2;; esac`)
		t.Setenv("VMHOST_LOG", log)
		e := managedTestEngine(dir)
		seed(t, e)
		deleteCommand := command("del-retain", false)
		r, err := e.Execute(context.Background(), deleteCommand, nil)
		if err != nil || r.Status != "succeeded" {
			t.Fatalf("retain delete: %+v %v", r, err)
		}
		if _, err := os.Stat(disk); err != nil {
			t.Fatalf("disk must remain: %v", err)
		}
		applied, _ := e.Applied(context.Background())
		if len(applied.VMs) != 0 || len(applied.DeletedVMs) != 1 || !applied.DeletedVMs[0].Tombstone || applied.DeletedVMs[0].ID != vm.ID {
			t.Fatalf("applied after retain: %+v", applied)
		}
		js, _ := e.journals()
		found := false
		for _, j := range js {
			if j.Command.Kind == "vm-delete" && len(j.Retained) == 1 && j.Retained[0] == disk {
				found = true
			}
		}
		if !found {
			t.Fatal("retained disk path missing from delete journal")
		}
		calls, _ := os.ReadFile(log)
		if !strings.Contains(string(calls), "undefine "+vm.ID+" --nvram") {
			t.Fatalf("undefine calls: %q", calls)
		}
	})

	t.Run("wipe", func(t *testing.T) {
		previousReady := managedStorageReadyFn
		managedStorageReadyFn = func(_ context.Context, _ protocol.StorageRegistration, _ uint64) (storageAccess, error) {
			return storageAccess{Path: storageRoot, Total: 1 << 40, Available: 1 << 40}, nil
		}
		t.Cleanup(func() { managedStorageReadyFn = previousReady })
		wipeDisk := filepath.Join(storageRoot, "nex-vms", vm.ID, "wipe-disk.qcow2")
		if err := os.MkdirAll(filepath.Dir(wipeDisk), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(wipeDisk), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wipeDisk, []byte("guest-data"), 0600); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "state")
		fakeCommand(t, "virsh", `case "$3" in
domstate) echo 'shut off';;
list) ;;
undefine) exit 0;;
*) exit 2;; esac`)
		e := managedTestEngine(dir)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		create := protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &vm}
		storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Path: storageRoot, Kind: protocol.StorageKindLocal}
		if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &storage}, Result: protocol.Result{OperationID: "storage-1", Status: "succeeded"}}); err != nil {
			t.Fatal(err)
		}
		if err := e.save(managedJournal{
			Command: create,
			Result:  protocol.Result{OperationID: create.OperationID, Status: "succeeded"},
			Stage:   "defined",
			Disks:   []string{wipeDisk},
			Targets: []string{"vda"}, MediaTarget: "sda",
		}); err != nil {
			t.Fatal(err)
		}
		deleteCommand := command("del-wipe", true)
		r, err := e.Execute(context.Background(), deleteCommand, nil)
		if err != nil || r.Status != "succeeded" {
			t.Fatalf("wipe delete: %+v %v", r, err)
		}
		if _, err := os.Stat(wipeDisk); !os.IsNotExist(err) {
			t.Fatalf("disk must be removed: %v", err)
		}
		applied, _ := e.Applied(context.Background())
		if len(applied.VMs) != 0 || len(applied.DeletedVMs) != 1 {
			t.Fatalf("applied after wipe: %+v", applied)
		}
	})

	t.Run("refuse-running", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "state")
		fakeCommand(t, "virsh", `case "$3" in domstate) echo running;; *) exit 2;; esac`)
		e := managedTestEngine(dir)
		seed(t, e)
		deleteCommand := command("del-run", false)
		r, err := e.Execute(context.Background(), deleteCommand, nil)
		if err != nil || r.Status != "failed" || !strings.Contains(r.Error, "stop the VM") {
			t.Fatalf("running delete must refuse: %+v %v", r, err)
		}
		if _, err := os.Stat(disk); err != nil {
			t.Fatal("refused delete must not touch disks")
		}
	})
}

func TestValidateManagedDelete(t *testing.T) {
	vm := managedTestVM()
	ok := protocol.Command{OperationID: "d1", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID}}
	if err := validateManaged(ok); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Delete = &protocol.VMDeleteRequest{VMID: "not-a-uuid", PoolID: vm.PoolID, HostID: vm.HostID}
	if validateManaged(bad) == nil {
		t.Fatal("invalid VM id accepted")
	}
	both := ok
	both.Power = &protocol.VMPowerRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "start"}
	if validateManaged(both) == nil {
		t.Fatal("two payloads accepted")
	}
}

// Per-VM host state outlives the revision that created it. A VM created with a TPM, then edited
// to drop it, still has swtpm state under /var/lib/libvirt/swtpm/<uuid> holding its endorsement
// key and whatever the guest sealed to it; the same applies to the NVRAM file of a VM that was
// created UEFI and edited to BIOS. Asking only the current definition left both behind, so the
// undefine flags come from every revision the journal holds.
func TestManagedDeleteRemovesStateAnEditDroppedLater(t *testing.T) {
	for _, c := range []struct {
		name        string
		first, next func(*protocol.VMCreateSpec)
		want        []string
		absent      []string
	}{
		{
			name:  "TPM removed by a later edit",
			first: func(v *protocol.VMCreateSpec) { v.Boot.TPM = protocol.TPM20 },
			next:  func(v *protocol.VMCreateSpec) { v.Boot.TPM = protocol.TPMNone },
			want:  []string{"--tpm", "--nvram"},
		},
		{
			name:   "UEFI changed to BIOS by a later edit",
			first:  func(v *protocol.VMCreateSpec) { v.Boot.Firmware = "uefi" },
			next:   func(v *protocol.VMCreateSpec) { v.Boot.Firmware = "bios"; v.Boot.SecureBoot = "" },
			want:   []string{"--nvram"},
			absent: []string{"--tpm"},
		},
		{
			name:   "never had either",
			first:  func(v *protocol.VMCreateSpec) { v.Boot.Firmware = "bios" },
			next:   func(v *protocol.VMCreateSpec) { v.Description = "edited" },
			absent: []string{"--tpm", "--nvram"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			created := managedTestVM()
			created.MediaID = ""
			c.first(&created)
			edited := created
			edited.Revision = 1
			c.next(&edited)

			dir := filepath.Join(t.TempDir(), "state")
			log := filepath.Join(t.TempDir(), "virsh.log")
			fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_LOG"; case "$3" in
domstate) echo 'shut off';;
list) ;;
undefine) exit 0;;
*) exit 2;; esac`)
			t.Setenv("VMHOST_LOG", log)
			e := managedTestEngine(dir)
			stubManagedDiskChains(e)
			if err := os.MkdirAll(e.stateDir, 0700); err != nil {
				t.Fatal(err)
			}
			disk := filepath.Join(t.TempDir(), "disk-0.qcow2")
			if err := os.WriteFile(disk, []byte("guest-data"), 0600); err != nil {
				t.Fatal(err)
			}
			for i, v := range []protocol.VMCreateSpec{created, edited} {
				kind, op := "vm-create", "create-1"
				if i == 1 {
					kind, op = "vm-update", "update-1"
				}
				spec := v
				cmd := protocol.Command{OperationID: op, Kind: kind, VM: &spec}
				if err := e.save(managedJournal{
					Command: cmd,
					Result:  protocol.Result{OperationID: op, Status: "succeeded"},
					Stage:   "defined",
					Disks:   []string{disk}, Targets: []string{"vda"}, MediaTarget: "sda",
				}); err != nil {
					t.Fatal(err)
				}
			}
			del := protocol.Command{OperationID: "del-1", Kind: "vm-delete",
				Delete: &protocol.VMDeleteRequest{VMID: created.ID, PoolID: created.PoolID, HostID: created.HostID}}
			r, err := e.Execute(context.Background(), del, nil)
			if err != nil || r.Status != "succeeded" {
				t.Fatalf("delete: %+v %v", r, err)
			}
			calls, _ := os.ReadFile(log)
			var undefine string
			for _, line := range strings.Split(string(calls), "\n") {
				if strings.Contains(line, "undefine") {
					undefine = line
				}
			}
			if undefine == "" {
				t.Fatalf("no undefine call: %q", calls)
			}
			for _, flag := range c.want {
				if !strings.Contains(undefine, flag) {
					t.Errorf("undefine %q must carry %s: state the edit dropped would survive deletion", undefine, flag)
				}
			}
			for _, flag := range c.absent {
				if strings.Contains(undefine, flag) {
					t.Errorf("undefine %q must not carry %s: the VM never had it", undefine, flag)
				}
			}
		})
	}
}
