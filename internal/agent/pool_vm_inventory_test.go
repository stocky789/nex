// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestPublishPoolVMInventoryRefreshesStaleMemberAndAcceptsPartialSnapshot(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	memberA := peerStore(t, "member-a", "other-pool")
	memberB := peerStore(t, "member-b", "other-pool")
	csr, err := PoolJoinCSR(memberA)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", memberA.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	join := func(member *Store) {
		memberCSR, err := PoolJoinCSR(member)
		if err != nil {
			t.Fatal(err)
		}
		ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, memberCSR)
		if err != nil {
			t.Fatal(err)
		}
		if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
			t.Fatal(err)
		}
	}
	join(memberA) // member A retains generation 2 after member B joins.
	join(memberB)
	before, err := Membership(memberA)
	if err != nil || before.Generation != 2 {
		t.Fatalf("stale member setup = %#v, %v", before, err)
	}
	if err = PublishPoolVMInventory(context.Background(), memberA); err != nil {
		t.Fatalf("stale member inventory did not refresh/retry: %v", err)
	}
	after, err := Membership(memberA)
	if err != nil || after.Generation != 3 {
		t.Fatalf("refreshed member membership = %#v, %v", after, err)
	}
	state, err := loadPoolVMs(master)
	if err != nil || state.Inventory[memberA.State.HostID].Generation != 3 || len(state.VMs) != 0 {
		t.Fatalf("partial current snapshot was not retained without owners: %#v, %v", state, err)
	}
}

func TestPoolVMInventoryAcceptsRealHealthyManagedObservations(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-a", Name: "shared", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports", Directory: "guests"}}, Generation: 1}
	journal := map[string]any{"command": protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &storage}, "result": protocol.Result{OperationID: "register-1", Status: "succeeded"}, "applied_storage": storage}
	b, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dir, "managed-vms")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "register-1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	inv, err := localPoolVMInventory(context.Background(), s, 1)
	if err != nil || inv.Storage[storage.ID] != storage.Identity {
		t.Fatalf("healthy local inventory = %#v, %v", inv, err)
	}
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 1, Members: []protocol.PoolMember{{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	state := poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{"host-a": inv}}
	if err = seedPoolVMOwners(s, membership, &state, "host-a"); err != nil {
		t.Fatalf("healthy master inventory was rejected: %v", err)
	}
}

func TestPoolVMInventoryKeepsPausedLocalDefinitionWithoutBlockingPool(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	storage := protocol.StorageRegistration{ID: "local-repo", PoolID: "pool-1", HostID: "host-a", Name: "local", Path: "/var/lib/nex-storage/local", Kind: protocol.StorageKindLocal}
	journal := map[string]any{"command": protocol.Command{OperationID: "register-local", Kind: "storage-register", Storage: &storage}, "result": protocol.Result{OperationID: "register-local", Status: "succeeded"}, "applied_storage": storage}
	b, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dir, "managed-vms")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "register-local.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	vm.Disks[0].StorageID = storage.ID
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 1, Members: []protocol.PoolMember{{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	state := poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{"host-a": {HostID: "host-a", Generation: 1, VMs: []protocol.VMCreateSpec{*vm}, Observed: map[string]string{vm.ID: "paused"}}}}
	if err = seedPoolVMOwners(s, membership, &state, "host-a"); err != nil {
		t.Fatalf("paused local definition blocked pool inventory: %v", err)
	}
	if record := state.VMs[vm.ID]; !poolVMRecordMatchesSpec(record, *vm) || record.State != "defined" {
		t.Fatalf("paused local definition = %#v", record)
	}
}

func TestPoolVMInventoryAdvancesOnlyWithExactSuccessfulUpdateEvidence(t *testing.T) {
	previous := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	next := *previous
	next.Revision, next.Autostart = 1, true
	record := poolVMRecord{PoolID: "pool-1", VMID: previous.ID, HostID: "host-a", State: "running", Autostart: false, Epoch: 1, Spec: *previous}
	if err := settlePoolVMInventoryDefinition(&record, "host-a", next, nil); err == nil {
		t.Fatal("newer inventory definition without journal evidence advanced the master record")
	}
	evidence := vmhost.VMUpdateEvidence{OperationID: "update-1", Status: "succeeded", Previous: *previous, Current: next}
	if err := settlePoolVMInventoryDefinition(&record, "host-a", next, &evidence); err != nil {
		t.Fatalf("exact successful update evidence did not recover the master record: %v", err)
	}
	if !poolVMRecordMatchesSpec(record, next) || !record.Autostart || record.PendingUpdate != nil {
		t.Fatalf("recovered master record = %#v", record)
	}
	bad := evidence
	bad.Current = *previous
	record = poolVMRecord{PoolID: "pool-1", VMID: previous.ID, HostID: "host-a", State: "running", Spec: *previous}
	if err := settlePoolVMInventoryDefinition(&record, "host-a", next, &bad); err == nil {
		t.Fatal("mismatched journal evidence advanced the master record")
	}
}

func TestPoolVMInventoryDoesNotReplayForeignCachedRuntimeObservation(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "owner-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{
		{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: "owner-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"},
	}}
	state := poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: "pool-1", VMID: vm.ID, HostID: "owner-1", State: "running", Epoch: 3, Spec: *vm}}, Inventory: map[string]poolVMInventory{
		"master-1": {HostID: "master-1", Generation: 1, Observed: map[string]string{}},
		"owner-1":  {HostID: "owner-1", Generation: 1, VMs: []protocol.VMCreateSpec{*vm}, Observed: map[string]string{vm.ID: "shut off"}},
	}}
	if err := seedPoolVMOwners(s, membership, &state, "master-1"); err != nil {
		t.Fatal(err)
	}
	if state.VMs[vm.ID].State != "running" || state.VMs[vm.ID].Epoch != 3 {
		t.Fatalf("foreign cached observation changed owner execution state: %#v", state.VMs[vm.ID])
	}
	if err := seedPoolVMOwners(s, membership, &state, "owner-1"); err != nil {
		t.Fatal(err)
	}
	if state.VMs[vm.ID].State != "stopped" || state.VMs[vm.ID].Epoch != 3 {
		t.Fatalf("fresh owner observation did not reconcile safely: %#v", state.VMs[vm.ID])
	}
}

func TestPoolVMInventoryRequiresEveryReadyMemberAtCurrentGeneration(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 7, Members: []protocol.PoolMember{
		{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready"},
		{HostID: "host-b", CertificateSHA256: strings.Repeat("b", 64), Generation: 7, Status: "ready"},
	}}
	state := poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{
		"host-a": {HostID: "host-a", Generation: 7},
	}}
	if err := seedPoolVMOwners(s, membership, &state, "host-a"); err == nil {
		t.Fatal("incomplete current-generation inventory seeded a shared VM owner")
	}
}

func TestPoolVMInventoryRetainsFirstSnapshotWithoutOwnerAdmission(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 7, Members: []protocol.PoolMember{
		{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready"},
		{HostID: "host-b", CertificateSHA256: strings.Repeat("b", 64), Generation: 7, Status: "ready"},
	}}
	if err := recordPoolVMInventory(s, membership, poolVMInventory{HostID: "host-a", Generation: 7}); err == nil {
		t.Fatal("partial inventory was treated as complete")
	}
	got, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Inventory["host-a"].Generation != 7 || len(got.VMs) != 0 {
		t.Fatalf("partial snapshot was not retained safely: %#v", got)
	}
}

func TestPoolVMInventoryRejectsStaleGeneration(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 7, Members: []protocol.PoolMember{{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready"}}}
	if err := recordPoolVMInventory(s, membership, poolVMInventory{HostID: "host-a", Generation: 6}); err == nil {
		t.Fatal("stale VM inventory was accepted")
	}
}

func TestPoolVMInventoryRefreshesOnlyNewerPinnedMembership(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	current := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-master", Generation: 2, Members: []protocol.PoolMember{
		{HostID: "host-master", CertificateSHA256: strings.Repeat("a", 64), Generation: 2, Status: "ready"},
		{HostID: "host-a", CertificateSHA256: strings.Repeat("b", 64), Generation: 2, Status: "ready"},
	}}
	if err := savePoolPeer(s, poolPeerState{Membership: current}); err != nil {
		t.Fatal(err)
	}
	fresh := current
	fresh.Generation = 3
	fresh.Members = append(fresh.Members, protocol.PoolMember{HostID: "host-b", CertificateSHA256: strings.Repeat("c", 64), Generation: 3, Status: "ready"})
	for i := range fresh.Members {
		fresh.Members[i].Generation = 3
	}
	if err := updatePoolVMMembership(s, fresh); err != nil {
		t.Fatal(err)
	}
	got, err := Membership(s)
	if err != nil || got.Generation != 3 || len(got.Members) != 3 {
		t.Fatalf("membership refresh = %#v, %v", got, err)
	}
	fresh.Generation = 4
	for i := range fresh.Members {
		fresh.Members[i].Generation = 4
	}
	fresh.Members[1].CertificateSHA256 = strings.Repeat("d", 64)
	if err := updatePoolVMMembership(s, fresh); err == nil {
		t.Fatal("membership refresh changed the pinned local peer identity")
	}
}
