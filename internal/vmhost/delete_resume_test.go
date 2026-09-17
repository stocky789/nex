// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deleteResumeHost is a fake libvirt whose domain stays listed until undefine runs.
func deleteResumeHost(t *testing.T, defined bool) (log string) {
	t.Helper()
	vm := managedTestVM()
	log = filepath.Join(t.TempDir(), "virsh.log")
	gone := filepath.Join(t.TempDir(), "gone")
	if !defined {
		if err := os.WriteFile(gone, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("VMHOST_LOG", log)
	t.Setenv("VMHOST_GONE", gone)
	fakeCommand(t, "virsh", `case "$3" in
domstate) [ -f "$VMHOST_GONE" ] && exit 1; echo 'shut off';;
list) [ -n "$VMHOST_SNAP" ] && cp "$VMHOST_STATE/del-1.json" "$VMHOST_SNAP"; [ -f "$VMHOST_GONE" ] || echo `+vm.ID+`;;
undefine) echo "$*" >> "$VMHOST_LOG"; touch "$VMHOST_GONE";;
*) exit 2;; esac`)
	return log
}

// seedInterruptedDelete journals a VM and a wipe delete left in stage by an agent that died.
func seedInterruptedDelete(t *testing.T, stage string) (*ManagedEngine, protocol.Command, string) {
	t.Helper()
	vm := managedTestVM()
	vm.MediaID = ""
	disk := filepath.Join(t.TempDir(), "nex-vms", vm.ID, "disk-0.qcow2")
	if err := os.MkdirAll(filepath.Dir(disk), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk, []byte("guest-data"), 0600); err != nil {
		t.Fatal(err)
	}
	e := NewManaged(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	create := protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &vm}
	del := protocol.Command{OperationID: "del-1", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, DeleteDisks: true}}
	for _, j := range []managedJournal{
		{Command: create, Result: protocol.Result{OperationID: create.OperationID, Status: "succeeded"}, Stage: "defined", Disks: []string{disk}, Targets: []string{"vda"}, MediaTarget: "sda"},
		{Command: del, Result: protocol.Result{OperationID: del.OperationID, Status: "needs_attention", Error: "interrupted"}, Stage: stage, Retained: []string{disk}},
	} {
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	return e, del, disk
}

func assertDeleteSettled(t *testing.T, e *ManagedEngine, disk string) {
	t.Helper()
	if _, err := os.Stat(disk); !os.IsNotExist(err) {
		t.Fatalf("disk the operator asked to wipe survived: %v", err)
	}
	applied, _ := e.Applied(context.Background())
	if len(applied.VMs) != 0 || len(applied.DeletedVMs) != 1 {
		t.Fatalf("VM must be tombstoned once: %+v", applied)
	}
	js, _ := e.journals()
	for _, j := range js {
		if j.Command.OperationID == "del-1" && (j.Result.Status != "succeeded" || len(j.Retained) != 0) {
			t.Fatalf("interrupted delete not settled: %+v", j)
		}
	}
}

// DEL-1: an agent that dies after undefine (or mid-wipe) must finish the wipe when the delete
// is re-sent, rather than reporting the leftover disk as needs_attention forever.
func TestInterruptedWipeDeleteResumesOnResend(t *testing.T) {
	for _, c := range []struct {
		stage   string
		defined bool
	}{{"wiping", false}, {"undefining", false}, {"undefining", true}} {
		t.Run(c.stage, func(t *testing.T) {
			log := deleteResumeHost(t, c.defined)
			e, del, disk := seedInterruptedDelete(t, c.stage)
			r, err := e.Execute(context.Background(), del, nil)
			if err != nil || r.Status != "succeeded" {
				t.Fatalf("resent delete: %+v %v", r, err)
			}
			assertDeleteSettled(t, e, disk)
			calls, _ := os.ReadFile(log)
			if c.defined != strings.Contains(string(calls), "undefine") {
				t.Fatalf("undefine must run exactly when the domain is still defined: %q", calls)
			}
		})
	}
}

// DEL-1: a replaced Controller never re-sends the old operation; a new delete for the same VM
// settles the interrupted one instead of being refused as unresolved.
func TestNewDeleteSettlesInterruptedDelete(t *testing.T) {
	deleteResumeHost(t, false)
	e, del, disk := seedInterruptedDelete(t, "wiping")
	again := del
	again.OperationID = "del-2"
	r, err := e.Execute(context.Background(), again, nil)
	if err != nil || r.Status != "failed" || !strings.Contains(r.Error, "already deleted") {
		t.Fatalf("new delete: %+v %v", r, err)
	}
	assertDeleteSettled(t, e, disk)
}

// A still-defined domain that someone started must not be undefined by a resumed delete.
func TestInterruptedDeleteRefusesRunningDomain(t *testing.T) {
	vm := managedTestVM()
	fakeCommand(t, "virsh", `case "$3" in
domstate) echo running;;
list) echo `+vm.ID+`;;
*) exit 2;; esac`)
	e, del, disk := seedInterruptedDelete(t, "undefining")
	r, err := e.Execute(context.Background(), del, nil)
	if err != nil || r.Status != "needs_attention" || !strings.Contains(r.Error, "stop the VM") {
		t.Fatalf("resumed delete of a running domain: %+v %v", r, err)
	}
	if _, err := os.Stat(disk); err != nil {
		t.Fatal("refused resume must not touch disks")
	}
}

// DEL-3: disks a wipe delete removed no longer block detaching their repository.
func TestWipedVMRetainedDisksAreNotStorageDependencies(t *testing.T) {
	vm := managedTestVM()
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Path: "/srv/s"}
	update := vm
	update.Revision = 1
	del := func(wipe bool) []managedJournal {
		return []managedJournal{
			{Command: protocol.Command{Kind: "storage-register", Storage: &storage}, Result: protocol.Result{Status: "succeeded"}},
			{Command: protocol.Command{OperationID: "u", Kind: "vm-update", VM: &update}, Result: protocol.Result{Status: "succeeded"}, Retained: []string{"/srv/s/nex-vms/old.qcow2"}},
			{Command: protocol.Command{OperationID: "d", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, DeleteDisks: wipe}}, Result: protocol.Result{Status: "succeeded"}, Retained: []string{"/srv/s/nex-vms/disk-0.qcow2"}},
		}
	}
	if got := storageDependencies(del(true), "s"); len(got) != 0 {
		t.Fatalf("wiped disks still block detach: %v", got)
	}
	if got := storageDependencies(del(false), "s"); len(got) == 0 {
		t.Fatal("retained disks of a deleted VM must still block detach")
	}
}

// Review R-1: a new delete that keeps disks must never settle an interrupted delete by wiping
// them; the latest explicit operator choice cannot be overridden by an earlier request's.
func TestNewRetainDeleteRefusesInterruptedWipeDelete(t *testing.T) {
	deleteResumeHost(t, false)
	e, del, disk := seedInterruptedDelete(t, "wiping")
	again := del
	again.OperationID = "del-2"
	keep := *del.Delete
	keep.DeleteDisks = false
	again.Delete = &keep
	r, err := e.Execute(context.Background(), again, nil)
	if err != nil || r.Status != "failed" || !strings.Contains(r.Error, "disk choice") {
		t.Fatalf("retain delete over interrupted wipe: %+v %v", r, err)
	}
	if b, err := os.ReadFile(disk); err != nil || string(b) != "guest-data" {
		t.Fatalf("retain delete wiped the disk: %v", err)
	}
}

// G3 2e: a resumed delete persists its mid-way wiping stage while it is still running; that
// write must read pending, not a provisional needs_attention the operator would see as stuck.
func TestResumedDeleteMidwaySaveStaysPending(t *testing.T) {
	deleteResumeHost(t, false)
	e, del, disk := seedInterruptedDelete(t, "wiping")
	j, err := e.readJournal("del-1.json")
	if err != nil {
		t.Fatal(err)
	}
	j.Result.Status, j.Result.Error = "pending", ""
	if err = e.save(j); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "midway.json")
	t.Setenv("VMHOST_STATE", e.stateDir)
	t.Setenv("VMHOST_SNAP", snap)
	r, err := e.Execute(context.Background(), del, nil)
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("resent delete: %+v %v", r, err)
	}
	assertDeleteSettled(t, e, disk)
	b, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	var mid managedJournal
	if err = json.Unmarshal(b, &mid); err != nil || mid.Stage != "wiping" || mid.Result.Status != "pending" {
		t.Fatalf("mid-way save must read pending at stage wiping: %s %v", b, err)
	}
}
