// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func rehomeTestSpec(poolID, hostID, storageID string) *protocol.VMCreateSpec {
	return &protocol.VMCreateSpec{
		ID: "11111111-1111-4111-8111-111111111111", PoolID: poolID, HostID: hostID, Name: "Guest", GuestOS: "linux",
		CPU: protocol.VMCPU{Count: 1, Model: "host-model"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: storageID, Name: "disk", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}, Boot: protocol.VMBoot{Firmware: "bios"},
	}
}

// A registration is recorded in more than one place in one journal entry: the command as it
// arrived, and Core's own verified projection of it (AppliedStorage, DownloadedMedia), which
// is what the projections prefer. Rehoming only the command left a joined host's own local
// repository at its pre-join pool, and every local placement on it was refused with
// "storage scope does not match the VM" (BUG-0357). The same entry set also has to survive
// the rehome pass a host runs on every agent start, which is what recovers a host that has
// already joined with the stale scope.
func TestRehomePoolScopeRehomesEveryScopeBearingCopy(t *testing.T) {
	e := NewManaged(t.TempDir())
	local := protocol.StorageRegistration{ID: "local-repo", PoolID: "old-pool", HostID: "old-host", Name: "Local storage", Path: "/var/lib/nex-storage/local", Kind: protocol.StorageKindLocal, Generation: 1}
	applied := local
	media := protocol.Media{URL: "https://example.test/debian.iso", ID: "iso-1", PoolID: "old-pool", HostID: "old-host", StorageID: local.ID, Name: "debian.iso"}
	downloaded := media
	downloaded.URL, downloaded.SizeBytes, downloaded.SHA256 = "", 40000, strings.Repeat("a", 64)
	vm := rehomeTestSpec("old-pool", "old-host", local.ID)
	save := func(j managedJournal) {
		t.Helper()
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	save(managedJournal{Command: protocol.Command{OperationID: "store", Kind: "storage-register", Storage: &local}, AppliedStorage: &applied, Result: protocol.Result{OperationID: "store", Status: "succeeded"}})
	rename := protocol.StorageLifecycleRequest{Action: "update", RepositoryID: local.ID, PoolID: "old-pool", HostID: "old-host", ExpectedGeneration: 1, Registration: &local}
	save(managedJournal{Command: protocol.Command{OperationID: "rename", Kind: "storage-update", StorageLifecycle: &rename}, Result: protocol.Result{OperationID: "rename", Status: "succeeded"}})
	save(managedJournal{Command: protocol.Command{OperationID: "iso", Kind: "iso-import", Media: &media}, DownloadedMedia: &downloaded, Result: protocol.Result{OperationID: "iso", Status: "succeeded"}})
	// A host that has already joined runs this pass again on every agent start, with its
	// managed VMs in the journal. That must not abort the pass.
	save(managedJournal{Command: protocol.Command{OperationID: "vm", Kind: "vm-create", VM: vm}, Result: protocol.Result{OperationID: "vm", Status: "succeeded"}})

	if err := RehomePoolScope(e.stateDir, "new-pool", "new-host"); err != nil {
		t.Fatalf("rehome on a host that already holds VMs: %v", err)
	}
	storage, _ := e.AppliedStorage()
	if len(storage) != 1 || storage[0].ID != local.ID || storage[0].PoolID != "new-pool" || storage[0].HostID != "new-host" {
		t.Fatalf("rehomed storage projection = %#v", storage)
	}
	isos, err := e.AppliedMedia()
	if err != nil || len(isos) != 1 || isos[0].PoolID != "new-pool" || isos[0].HostID != "new-host" {
		t.Fatalf("rehomed media projection = %#v, %v", isos, err)
	}
	// Workload definitions are never retargeted by a join: the join precheck refuses a host
	// that holds any, and this pass must leave the ones a joined host has alone.
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range js {
		if j.Command.VM != nil && (j.Command.VM.PoolID != "old-pool" || j.Command.VM.HostID != "old-host") {
			t.Fatalf("a join rehomed a managed workload: %#v", j.Command.VM)
		}
	}
	// Idempotent: a second pass changes nothing, so a crash between entries is safe to repeat.
	before, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	if err = RehomePoolScope(e.stateDir, "new-pool", "new-host"); err != nil {
		t.Fatal(err)
	}
	after, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("repeated rehome changed the journal: %d -> %d", len(before), len(after))
	}
}

// The same bug class on the way out of a pool: an ejected host becoming its own master keeps
// its local repositories, so every scope-bearing copy of them moves to the new pool too.
func TestRehomeLeavingPoolScopeRehomesAppliedCopies(t *testing.T) {
	e := NewManaged(t.TempDir())
	local := protocol.StorageRegistration{ID: "local-repo", PoolID: "old-pool", HostID: "host-b", Name: "Local storage", Path: "/var/lib/nex-storage/local", Kind: protocol.StorageKindLocal, Generation: 1}
	applied := local
	vm := rehomeTestSpec("old-pool", "host-b", local.ID)
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "store", Kind: "storage-register", Storage: &local}, AppliedStorage: &applied, Result: protocol.Result{OperationID: "store", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "vm", Kind: "vm-create", VM: vm}, Result: protocol.Result{OperationID: "vm", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	deleted := protocol.VMDeleteRequest{VMID: vm.ID, PoolID: "old-pool", HostID: "host-b"}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "delete", Kind: "vm-delete", Delete: &deleted}, Result: protocol.Result{OperationID: "delete", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := RehomeLeavingPoolScope(e.stateDir, "own-pool", "host-b"); err != nil {
		t.Fatal(err)
	}
	storage, _ := e.AppliedStorage()
	if len(storage) != 1 || storage[0].PoolID != "own-pool" {
		t.Fatalf("leaving rehome left an applied copy behind: %#v", storage)
	}
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range js {
		if j.Command.VM != nil && j.Command.VM.PoolID != "own-pool" {
			t.Fatalf("leaving rehome did not carry its own VM: %#v", j.Command.VM)
		}
		if j.Command.Delete != nil && j.Command.Delete.PoolID != "old-pool" {
			t.Fatalf("leaving rehome retargeted old-pool delete history: %#v", j.Command.Delete)
		}
	}
	managed, _ := e.Applied(context.Background())
	if len(managed.VMs) != 0 {
		t.Fatalf("old-pool delete resurrected a VM in the new pool inventory: %#v", managed.VMs)
	}
}

func TestPoolCommandCannotCreateMembershipFromControllerTransport(t *testing.T) {
	member := func(generation uint64) protocol.PoolMembership {
		return protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: generation, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Generation: generation, Status: "ready"}}}
	}
	command := protocol.Command{OperationID: "pool-op", Kind: "pool-member-join", Pool: &protocol.PoolCommand{RequestID: "join-1", Action: "member-join", Membership: member(1), ExpectedVersion: 1}}
	if err := HandlePoolCommand(context.Background(), t.TempDir(), "master-1", command); err == nil {
		t.Fatal("controller transport created membership")
	}
}

func TestRehomePoolScopePreservesDefaultResourceIdentity(t *testing.T) {
	e := NewManaged(t.TempDir())
	s := protocol.StorageRegistration{ID: "default-store", PoolID: "old-pool", HostID: "old-host", Name: "ISO", Path: "/srv/vms"}
	n := protocol.NetworkRegistration{ID: "default-network", PoolID: "old-pool", HostID: "old-host", Name: "Guests", Kind: "bridge", Source: "br0"}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "store", Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "store", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "network", Kind: "network-register", Network: &n}, Result: protocol.Result{OperationID: "network", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := RehomePoolScope(e.stateDir, "new-pool", "new-host"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Applied(context.Background())
	if len(got.Storage) != 1 || got.Storage[0].ID != s.ID || got.Storage[0].PoolID != "new-pool" || len(got.Networks) != 1 || got.Networks[0].ID != n.ID || got.Networks[0].HostID != "new-host" {
		t.Fatalf("rehomed = %#v", got)
	}
}
