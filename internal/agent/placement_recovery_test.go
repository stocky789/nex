// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// placementRecoveryPool is a master with one member whose managed journal is dir.
func placementRecoveryPool(t *testing.T) (master, member *Store, membership protocol.PoolMembership, dir string) {
	t.Helper()
	master = peerStore(t, "master-a", "pool-1")
	member = peerStore(t, "member-b", "pool-1")
	membership = maintenanceMembership(t, master, "master-a", maintenanceMember("master-a", false), maintenanceMember("member-b", false))
	dir = filepath.Join(member.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return master, member, membership, dir
}

func publishPlacementRecovery(t *testing.T, master, member *Store, membership protocol.PoolMembership) poolVMState {
	t.Helper()
	inv, err := localPoolVMInventory(context.Background(), member, membership.Generation)
	if err != nil {
		t.Fatal(err)
	}
	_ = recordPoolVMInventory(master, membership, poolVMInventory{HostID: "master-a", Generation: membership.Generation})
	if err = recordPoolVMInventory(master, membership, inv); err != nil {
		t.Fatalf("member inventory refused: %v", err)
	}
	state, err := loadPoolVMs(master)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// BUG-0538: the owner's succeeded vm-delete tombstone proves the VM ID can never be defined on that
// host again (and deleteVM refuses while any local change for the VM is unresolved), so an
// unresolved master update reservation can no longer take effect. Keeping the record makes a
// deleted VM a permanent phantom that also blocks ejecting its host.
func TestDeletedOwnerVMPrunesRecordWithUnresolvedUpdate(t *testing.T) {
	master, member, membership, dir := placementRecoveryPool(t)
	vm := poolVMTestSpec("22222222-2222-4222-8222-222222222222", "member-b")
	next := *vm
	next.Revision = 1
	writeManagedJournal(t, dir, "create", protocol.Command{OperationID: "create", Kind: "vm-create", VM: vm})
	writeManagedJournal(t, dir, "delete", protocol.Command{OperationID: "delete", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: "pool-1", HostID: "member-b"}})
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{
		vm.ID: {PoolID: "pool-1", VMID: vm.ID, HostID: "member-b", State: "stopped", Epoch: 2, Spec: *vm, PendingUpdate: &poolVMPendingUpdate{OperationID: "update-lost-reply", Next: next}},
	}, Inventory: map[string]poolVMInventory{}}); err != nil {
		t.Fatal(err)
	}
	if record, stale := publishPlacementRecovery(t, master, member, membership).VMs[vm.ID]; stale {
		t.Fatalf("deleted VM kept its master owner record: %+v", record)
	}
}

// BUG-0536: the master records a create's owner before the target runs it, and nothing ever
// releases that record when the target proves the create failed before any VM-specific
// effect (stage "validating"). The guide promises such a failure creates no VM row.
func TestProvenFailedCreateReleasesMasterRecord(t *testing.T) {
	master, member, membership, dir := placementRecoveryPool(t)
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "member-b")
	b, err := json.Marshal(map[string]any{"command": protocol.Command{OperationID: "create", Kind: "vm-create", VM: vm}, "result": protocol.Result{OperationID: "create", Status: "failed", Error: "interrupted before allocation"}, "stage": "validating"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "create.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{
		vm.ID: {PoolID: "pool-1", VMID: vm.ID, HostID: "member-b", State: "defined", Epoch: 1, Spec: *vm},
	}, Inventory: map[string]poolVMInventory{}}); err != nil {
		t.Fatal(err)
	}
	if record, stale := publishPlacementRecovery(t, master, member, membership).VMs[vm.ID]; stale {
		t.Fatalf("a create the owner proved failed kept its master owner record: %+v", record)
	}
}

// BUG-0537: the master reserved an update and its reply was lost, so the member refused the
// operation before executing it. The member journals that refusal (RecordRefusal); its next
// inventory proves the update failed unapplied and the master releases the reservation, which
// otherwise nothing could ever settle.
func TestRefusedUpdateReleasesMasterReservation(t *testing.T) {
	master, member, membership, dir := placementRecoveryPool(t)
	vm := poolVMTestSpec("33333333-3333-4333-8333-333333333333", "member-b")
	next := *vm
	next.Revision, next.Name = 1, "Renamed guest"
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *domstate*) echo 'shut off';; *list*) echo " + vm.ID + ";; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeManagedJournal(t, dir, "create", protocol.Command{OperationID: "create", Kind: "vm-create", VM: vm})
	update := protocol.Command{OperationID: "update-lost-reply", Kind: "vm-update", VM: &next}
	if err := vmhost.NewManaged(dir).RecordRefusal(context.Background(), update, "pool master request failed: EOF"); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{
		vm.ID: {PoolID: "pool-1", VMID: vm.ID, HostID: "member-b", State: "stopped", Epoch: 1, Spec: *vm, PendingUpdate: &poolVMPendingUpdate{OperationID: update.OperationID, Next: next}},
	}, Inventory: map[string]poolVMInventory{}}); err != nil {
		t.Fatal(err)
	}
	record := publishPlacementRecovery(t, master, member, membership).VMs[vm.ID]
	if record.PendingUpdate != nil || record.Spec.Revision != 0 || record.LastAbortedUpdate == nil {
		t.Fatalf("refused update left the master reservation: %+v", record)
	}
}

// BUG-0537: the worker itself must journal an update the master refused before any local
// journal entry existed; otherwise nothing ever proves the reservation failed unapplied.
func TestManagedWorkerJournalsRefusedUpdate(t *testing.T) {
	master, member, masterServer, _ := cpuPeerPair(t)
	member.State.PoolID = master.State.PoolID
	masterServer.Close()
	vm := poolVMTestSpec("44444444-4444-4444-8444-444444444444", member.State.HostID)
	vm.PoolID, vm.Revision = member.State.PoolID, 1
	update := protocol.Command{OperationID: "update-refused", Kind: "vm-update", VM: vm}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands, results := make(chan protocol.Command, 1), make(chan managedCompletion, 1)
	done := make(chan struct{})
	go func() { defer close(done); managedWorker(ctx, member.dir, member.State, commands, results) }()
	commands <- update
	select {
	case completion := <-results:
		if completion.result.Status != "failed" {
			t.Fatalf("refused update = %+v", completion)
		}
	case <-time.After(time.Minute):
		t.Fatal("worker did not settle refused update")
	}
	cancel()
	<-done
	if _, ok := vmhost.NewManaged(filepath.Join(member.dir, "managed-vms")).JournaledCommand(update.OperationID); !ok {
		t.Fatal("refused update was not journaled")
	}
}
