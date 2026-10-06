// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 1, Members: []protocol.PoolMember{{HostID: "host-a", Hostname: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	state := poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{"host-a": inv}}
	if err = seedPoolVMOwners(s, membership, &state, "host-a"); err != nil {
		t.Fatalf("healthy master inventory was rejected: %v", err)
	}
}

func failedStartInventoryFixture(t *testing.T) (*Store, protocol.VMCreateSpec) {
	t.Helper()
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "local-1", PoolID: s.State.PoolID, HostID: s.State.HostID, Name: "local", Path: t.TempDir(), Kind: protocol.StorageKindLocal, Generation: 1}
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	vm.Disks[0].StorageID = storage.ID
	if err := os.MkdirAll(filepath.Join(s.dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, record any) {
		t.Helper()
		b, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.dir, "managed-vms", name+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("register-local", map[string]any{"command": protocol.Command{OperationID: "register-local", Kind: "storage-register", Storage: &storage}, "result": protocol.Result{OperationID: "register-local", Status: "succeeded"}, "applied_storage": storage})
	write("create-local", map[string]any{"command": protocol.Command{OperationID: "create-local", Kind: "vm-create", VM: &vm}, "result": protocol.Result{OperationID: "create-local", Status: "succeeded"}})
	autostartVirsh(t, vm.ID)
	return s, vm
}

func TestPoolVMInventorySettlesExactFailedStartAfterRestart(t *testing.T) {
	s, local := failedStartInventoryFixture(t)
	const operationID = "start-memory-preflight"
	command := protocol.Command{OperationID: operationID, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: local.ID, PoolID: local.PoolID, HostID: local.HostID, Action: "start"}}
	journal, err := json.Marshal(map[string]any{
		"command": command,
		"result":  protocol.Result{OperationID: operationID, Status: "failed", Error: "insufficient available memory"},
		"stage":   "validating",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "managed-vms", operationID+".json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
	record := poolVMRecord{PoolID: local.PoolID, VMID: local.ID, HostID: local.HostID, State: "starting", Epoch: 2, PendingStartOperationID: operationID, Spec: local}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{local.ID: record}}); err != nil {
		t.Fatal(err)
	}
	if err := PublishPoolVMInventory(context.Background(), s); err != nil {
		t.Fatalf("owner inventory did not reconcile the failed start: %v", err)
	}
	settled := mustLoadPoolVMs(t, s).VMs[local.ID]
	if settled.State != "stopped" || settled.PendingStartOperationID != "" || settled.Epoch != 2 {
		t.Fatalf("exact failed start was not safely released: %+v", settled)
	}
}

func TestPoolVMInventorySettlesFailedStartAfterCreateAfterRestart(t *testing.T) {
	s, local := failedStartInventoryFixture(t)
	local.StartAfterCreate = true
	create, err := json.Marshal(map[string]any{
		"command":       protocol.Command{OperationID: "create-local", Kind: "vm-create", VM: &local},
		"result":        protocol.Result{OperationID: "create-local", Status: "succeeded"},
		"stage":         "started",
		"start":         "libvirt refused the start request",
		"start_outcome": "failed-before-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "managed-vms", "create-local.json"), create, 0600); err != nil {
		t.Fatal(err)
	}
	record := poolVMRecord{PoolID: local.PoolID, VMID: local.ID, HostID: local.HostID, State: "starting", Epoch: 2, PendingStartOperationID: "create-local", Spec: local}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{local.ID: record}}); err != nil {
		t.Fatal(err)
	}
	if err := PublishPoolVMInventory(context.Background(), s); err != nil {
		t.Fatalf("owner inventory did not reconcile failed StartAfterCreate: %v", err)
	}
	settled := mustLoadPoolVMs(t, s).VMs[local.ID]
	if settled.State != "stopped" || settled.PendingStartOperationID != "" || settled.Epoch != 2 {
		t.Fatalf("failed StartAfterCreate was not safely released: %+v", settled)
	}
}

func TestPoolVMInventoryKeepsAmbiguousAutomaticStartAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		start     string
		outcome   string
		observed  string
		wantState string
		pending   string
	}{
		{name: "request outcome uncertain and shut off", start: "libvirt did not confirm the start request; inspect the VM on its host", outcome: "uncertain", observed: "shut off", wantState: "starting", pending: "create-auto"},
		{name: "interrupted outcome and shut off", start: "interrupted before the automatic start completed", outcome: "uncertain", observed: "shut off", wantState: "starting", pending: "create-auto"},
		{name: "reason without outcome and shut off", start: "libvirt refused the start request; inspect the VM on its host", observed: "shut off", wantState: "starting", pending: "create-auto"},
		{name: "uncertain but currently running", start: "libvirt did not confirm the start request; inspect the VM on its host", outcome: "uncertain", observed: "running", wantState: "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, local := failedStartInventoryFixture(t)
			_ = os.Remove(filepath.Join(s.dir, "managed-vms", "create-local.json"))
			local.StartAfterCreate = true
			t.Setenv("NEX_TEST_STATE", tc.observed)
			create, err := json.Marshal(map[string]any{
				"command":       protocol.Command{OperationID: "create-auto", Kind: "vm-create", VM: &local},
				"result":        protocol.Result{OperationID: "create-auto", Status: "succeeded"},
				"stage":         "started",
				"start":         tc.start,
				"start_outcome": tc.outcome,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.dir, "managed-vms", "create-auto.json"), create, 0600); err != nil {
				t.Fatal(err)
			}
			record := poolVMRecord{PoolID: local.PoolID, VMID: local.ID, HostID: local.HostID, State: "starting", Epoch: 2, PendingStartOperationID: "create-auto", Spec: local}
			if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{local.ID: record}}); err != nil {
				t.Fatal(err)
			}
			if err := PublishPoolVMInventory(context.Background(), s); err != nil {
				t.Fatalf("owner inventory: %v", err)
			}
			settled := mustLoadPoolVMs(t, s).VMs[local.ID]
			if settled.State != tc.wantState || settled.PendingStartOperationID != tc.pending {
				t.Fatalf("automatic start reconciliation = %+v, want state %s pending %s", settled, tc.wantState, tc.pending)
			}
		})
	}
}

func TestPoolVMInventorySettlesObservedRunningStartAfterLostAcknowledgement(t *testing.T) {
	s, local := failedStartInventoryFixture(t)
	t.Setenv("NEX_TEST_STATE", "running")
	const operationID = "start-lost-ack"
	command := protocol.Command{OperationID: operationID, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: local.ID, PoolID: local.PoolID, HostID: local.HostID, Action: "start"}}
	journal, err := json.Marshal(map[string]any{
		"command": command,
		"result":  protocol.Result{OperationID: operationID, Status: "succeeded"},
		"stage":   "powering",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "managed-vms", operationID+".json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
	record := poolVMRecord{PoolID: local.PoolID, VMID: local.ID, HostID: local.HostID, State: "starting", Epoch: 2, PendingStartOperationID: operationID, Spec: local}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{local.ID: record}}); err != nil {
		t.Fatal(err)
	}
	if err := PublishPoolVMInventory(context.Background(), s); err != nil {
		t.Fatalf("owner inventory did not reconcile the observed running guest: %v", err)
	}
	settled := mustLoadPoolVMs(t, s).VMs[local.ID]
	if settled.State != "running" || settled.PendingStartOperationID != "" || settled.Epoch != 2 {
		t.Fatalf("observed running guest was not reconciled: %+v", settled)
	}
}

func TestPoolVMInventoryKeepsStartPendingWithoutExactDefinitiveFailure(t *testing.T) {
	for _, tc := range []struct {
		name        string
		journalID   string
		journalKind string
		status      string
		pending     string
		observed    string
		wantState   string
		wantPending string
	}{
		{name: "different operation", journalID: "older-failed-start", journalKind: "vm-power", status: "failed", pending: "current-start", observed: "shut off", wantState: "starting", wantPending: "current-start"},
		{name: "ambiguous result", journalID: "current-start", journalKind: "vm-power", status: "needs_attention", pending: "current-start", observed: "shut off", wantState: "starting", wantPending: "current-start"},
		{name: "failed stop command", journalID: "current-start", journalKind: "vm-power", status: "failed", pending: "current-start", observed: "shut off", wantState: "starting", wantPending: "current-start"},
		{name: "running observation", journalID: "current-start", journalKind: "vm-power", status: "needs_attention", pending: "current-start", observed: "running", wantState: "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, local := failedStartInventoryFixture(t)
			t.Setenv("NEX_TEST_STATE", tc.observed)
			action := "start"
			if tc.name == "failed stop command" {
				action = "shutdown"
			}
			command := protocol.Command{OperationID: tc.journalID, Kind: tc.journalKind, Power: &protocol.VMPowerRequest{VMID: local.ID, PoolID: local.PoolID, HostID: local.HostID, Action: action}}
			journal, err := json.Marshal(map[string]any{
				"command": command,
				"result":  protocol.Result{OperationID: tc.journalID, Status: tc.status, Error: "native start unresolved"},
				"stage":   "powering",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.dir, "managed-vms", tc.journalID+".json"), journal, 0600); err != nil {
				t.Fatal(err)
			}
			record := poolVMRecord{PoolID: local.PoolID, VMID: local.ID, HostID: local.HostID, State: "starting", Epoch: 2, PendingStartOperationID: tc.pending, Spec: local}
			if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{local.ID: record}}); err != nil {
				t.Fatal(err)
			}
			if err := PublishPoolVMInventory(context.Background(), s); err != nil {
				t.Fatalf("inventory publish: %v", err)
			}
			unsettled := mustLoadPoolVMs(t, s).VMs[local.ID]
			if unsettled.State != tc.wantState || unsettled.PendingStartOperationID != tc.wantPending {
				t.Fatalf("start reconciliation = %+v, want state %s pending %s", unsettled, tc.wantState, tc.wantPending)
			}
		})
	}
}

func TestPoolVMInventoryKeepsFailedStartFencedDuringUpdateOrRecovery(t *testing.T) {
	for _, kind := range []string{"update", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			s, local := failedStartInventoryFixture(t)
			const operationID = "start-pending-work"
			command := protocol.Command{OperationID: operationID, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: local.ID, PoolID: local.PoolID, HostID: local.HostID, Action: "start"}}
			journal, err := json.Marshal(map[string]any{"command": command, "result": protocol.Result{OperationID: operationID, Status: "failed", Error: "preflight failed"}, "stage": "validating"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.dir, "managed-vms", operationID+".json"), journal, 0600); err != nil {
				t.Fatal(err)
			}
			record := poolVMRecord{PoolID: local.PoolID, VMID: local.ID, HostID: local.HostID, State: "starting", Epoch: 2, PendingStartOperationID: operationID, Spec: local}
			if kind == "update" {
				next := local
				next.Revision++
				record.PendingUpdate = &poolVMPendingUpdate{OperationID: "update-pending", Next: next}
			} else {
				stopped := record
				stopped.State, stopped.PendingStartOperationID = "stopped", ""
				reservation, err := poolVMRecoveryReservationFor(stopped, "snapshot-pending", "snapshot-create", strings.Repeat("d", 64), 1)
				if err != nil {
					t.Fatal(err)
				}
				if err := reservePoolVMRecovery(&stopped, reservation); err != nil {
					t.Fatal(err)
				}
				descriptor := snapshotDescriptorForTest(reservation, "pending", "")
				dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(local)
				if err != nil {
					t.Fatal(err)
				}
				descriptor.Dependencies = &dependencies
				stopped.PendingSnapshot = &descriptor
				record.PendingRecovery, record.PendingSnapshot = stopped.PendingRecovery, stopped.PendingSnapshot
			}
			inv, err := localPoolVMInventory(context.Background(), s, 1)
			if err != nil {
				t.Fatalf("owner inventory: %v", err)
			}
			membership, err := Membership(s)
			if err != nil {
				t.Fatal(err)
			}
			state := poolVMState{VMs: map[string]poolVMRecord{local.ID: record}, Inventory: map[string]poolVMInventory{local.HostID: inv}}
			if err := seedPoolVMOwners(s, membership, &state, local.HostID); err != nil {
				t.Fatalf("owner seed: %v", err)
			}
			unsettled := state.VMs[local.ID]
			if unsettled.State != "starting" || unsettled.PendingStartOperationID != operationID {
				t.Fatalf("failed start bypassed pending %s: %+v", kind, unsettled)
			}
		})
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
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 1, Members: []protocol.PoolMember{{HostID: "host-a", Hostname: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
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
		{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: "owner-1", Hostname: "owner-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"},
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
		{HostID: "host-a", Hostname: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready"},
		{HostID: "host-b", Hostname: "host-b", CertificateSHA256: strings.Repeat("b", 64), Generation: 7, Status: "ready"},
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
		{HostID: "host-a", Hostname: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready"},
		{HostID: "host-b", Hostname: "host-b", CertificateSHA256: strings.Repeat("b", 64), Generation: 7, Status: "ready"},
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
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 7, Members: []protocol.PoolMember{{HostID: "host-a", Hostname: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready"}}}
	if err := recordPoolVMInventory(s, membership, poolVMInventory{HostID: "host-a", Generation: 6}); err == nil {
		t.Fatal("stale VM inventory was accepted")
	}
}

func TestPoolReplicaRejoinRequiresCapturedSameGenerationAuthority(t *testing.T) {
	captured := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 7, Members: []protocol.PoolMember{
		{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready"},
		{HostID: "member-1", Hostname: "member-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 7, Status: "ready", ReplicaAckExcluded: true},
	}}
	state := poolVMState{Inventory: map[string]poolVMInventory{"member-1": {HostID: "member-1", Generation: 7}}}
	peer := poolPeerState{Membership: captured, AuthorityDigest: strings.Repeat("c", 64)}
	if !poolReplicaRejoinStillCurrent(peer, captured, peer.AuthorityDigest, peer.AuthorityDigest, state, "member-1", "master-1") {
		t.Fatal("the exact captured authority and accepted inventory were rejected")
	}
	if poolReplicaRejoinStillCurrent(peer, captured, peer.AuthorityDigest, strings.Repeat("d", 64), state, "member-1", "master-1") {
		t.Fatal("same-generation content that changed after the acknowledged digest was treated as current")
	}
	changed := captured
	changed.Members = append([]protocol.PoolMember(nil), captured.Members...)
	changed.Members[1].Maintenance = true // authority-only change, same membership generation
	peer.Membership = changed
	if poolReplicaRejoinStillCurrent(peer, captured, peer.AuthorityDigest, peer.AuthorityDigest, state, "member-1", "master-1") {
		t.Fatal("a same-generation authority change after replica push was treated as the acknowledged snapshot")
	}
}

func TestPoolVMInventoryRefreshesOnlyNewerPinnedMembership(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	current := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-master", Generation: 2, Members: []protocol.PoolMember{
		{HostID: "host-master", Hostname: "host-master", CertificateSHA256: strings.Repeat("a", 64), Generation: 2, Status: "ready"},
		{HostID: "host-a", Hostname: "host-a", CertificateSHA256: strings.Repeat("b", 64), Generation: 2, Status: "ready"},
	}}
	if err := savePoolPeer(s, poolPeerState{Membership: current}); err != nil {
		t.Fatal(err)
	}
	fresh := current
	fresh.Generation = 3
	fresh.Members = append(fresh.Members, protocol.PoolMember{HostID: "host-b", Hostname: "host-b", CertificateSHA256: strings.Repeat("c", 64), Generation: 3, Status: "ready"})
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

// Once any host had been ejected, no shared VM could be started again: an eject marks the
// membership row and keeps it for ever, so a completeness rule that required every row to be
// "ready" was never satisfiable again. The inventory-driven settle is the only path that
// records a shared VM as stopped, so the master's record stayed "running" and every later
// start was refused (BUG-0358). Completeness is a question about the hosts that can own or
// run a pool VM now; an ejected host is not one of them.
func TestPoolVMInventorySettlesWithAnEjectedMembershipRow(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	mine := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "master-1")
	theirs := poolVMTestSpec("22222222-2222-4222-8222-222222222222", "owner-1")
	gone := poolVMTestSpec("33333333-3333-4333-8333-333333333333", "gone-1")
	ejected := maintenanceMember("gone-1", false)
	ejected.Status = "ejected"
	membership := maintenanceMembership(t, s, "master-1", maintenanceMember("master-1", false), maintenanceMember("owner-1", false), ejected)
	// Both guests were started, then stopped natively. A shared-storage stop deliberately
	// posts nothing to the master, so this snapshot is the only path that records it.
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{
		mine.ID:   {PoolID: "pool-1", VMID: mine.ID, HostID: "master-1", State: "running", Epoch: 2, Spec: *mine},
		theirs.ID: {PoolID: "pool-1", VMID: theirs.ID, HostID: "owner-1", State: "running", Epoch: 2, Spec: *theirs},
	}, Inventory: map[string]poolVMInventory{
		// The ejected host's last snapshot, left at the generation it was ejected at.
		"gone-1": {HostID: "gone-1", Generation: 2, VMs: []protocol.VMCreateSpec{*gone}, Observed: map[string]string{gone.ID: "running"}},
	}}); err != nil {
		t.Fatal(err)
	}
	report := func(host string, vm *protocol.VMCreateSpec, complete bool) {
		t.Helper()
		inv := poolVMInventory{HostID: host, Generation: membership.Generation, VMs: []protocol.VMCreateSpec{*vm}, Observed: map[string]string{vm.ID: "shut off"}}
		err := recordPoolVMInventory(s, membership, inv)
		if complete && err != nil {
			t.Fatalf("an ejected membership row blocked the %s settle: %v", host, err)
		}
		if !complete && !errors.Is(err, errPoolVMInventoryIncomplete) {
			t.Fatalf("the first snapshot of a generation is not a complete picture: %v", err)
		}
	}
	// The first host to report cannot settle anything: the other ready member has not been
	// heard from at this generation yet. Only the ejected row is exempt.
	report("master-1", mine, false)
	report("owner-1", theirs, true)
	report("master-1", mine, true)
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{mine.ID, theirs.ID} {
		if got := state.VMs[id]; got.State != "stopped" || got.Epoch != 2 {
			t.Fatalf("stopped shared VM %s was not settled: %#v", id, got)
		}
	}
	if _, ok := state.VMs[gone.ID]; ok {
		t.Fatal("an ejected host's stale snapshot seeded a pool VM owner record")
	}
	// The master's own guest can be started again; the member's record is back in the one
	// state the master's start admission accepts.
	permit, epoch := "", uint64(0)
	if err := poolVMLocalBound(s, mine.ID, "start", "inventory-restart", 0, &permit, false, nil, &epoch); err != nil {
		t.Fatalf("start on the master after a stop: %v", err)
	}
	if permit == "" || epoch != 3 {
		t.Fatalf("start admission = %q, epoch %d", permit, epoch)
	}
}

// An ejected row cannot hold a pool VM record by construction, which is what makes it safe to
// leave out of the completeness rule: the master refuses the eject from its own records while
// the target still owns one, including when it could not reach that host to ask.
func TestPoolEjectIsRefusedWhileTheTargetOwnsAPoolVM(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "owner-1")
	membership := maintenanceMembership(t, s, "master-1", maintenanceMember("master-1", false), maintenanceMember("owner-1", false))
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{
		vm.ID: {PoolID: "pool-1", VMID: vm.ID, HostID: "owner-1", State: "running", Epoch: 1, Spec: *vm},
	}, Inventory: map[string]poolVMInventory{"owner-1": {HostID: "owner-1", Generation: membership.Generation, VMs: []protocol.VMCreateSpec{*vm}, Storage: map[string]protocol.StorageIdentity{vm.Disks[0].StorageID: {}}, Observed: map[string]string{vm.ID: "running"}}}}); err != nil {
		t.Fatal(err)
	}
	target, _ := poolMemberByID(membership, "owner-1")
	err := poolEjectRefusal(s, membership, target)
	if err == nil || !strings.Contains(err.Error(), vm.ID) {
		t.Fatalf("a host still owning a pool VM record was ejectable: %v", err)
	}
}

func TestPoolEjectRefusesStaleTargetInventoryEvenWithoutAnOwnerRecord(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	vm := poolVMTestSpec("22222222-2222-4222-8222-222222222222", "owner-1")
	membership := maintenanceMembership(t, s, "master-1", maintenanceMember("master-1", false), maintenanceMember("owner-1", false))
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{
		"owner-1": {HostID: "owner-1", Generation: membership.Generation - 1, VMs: []protocol.VMCreateSpec{*vm}, Storage: map[string]protocol.StorageIdentity{vm.Disks[0].StorageID: {}}, Observed: map[string]string{vm.ID: "running"}},
	}}); err != nil {
		t.Fatal(err)
	}
	target, _ := poolMemberByID(membership, "owner-1")
	if err := poolEjectRefusal(s, membership, target); err == nil || !strings.Contains(err.Error(), "current VM inventory") {
		t.Fatalf("stale target inventory allowed an eject: %v", err)
	}
}

// A host that is no longer part of the pool must stop holding the pool's resources too: its
// snapshot is dropped when the next inventory is recorded, so pool-vms.json does not keep it
// for ever and no later pass reads it.
func TestPoolVMInventoryDropsSnapshotsOfNonMembers(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 12, Members: []protocol.PoolMember{
		{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 12, Status: "ready"},
		{HostID: "gone-1", Hostname: "gone-1", CertificateSHA256: strings.Repeat("c", 64), Generation: 12, Status: "ejected"},
	}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{
		"gone-1": {HostID: "gone-1", Generation: 9},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recordPoolVMInventory(s, membership, poolVMInventory{HostID: "master-1", Generation: 12}); err != nil {
		t.Fatalf("a pool whose only other row is ejected could not record inventory: %v", err)
	}
	got, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Inventory["gone-1"]; ok {
		t.Fatalf("an ejected host's snapshot was retained: %#v", got.Inventory)
	}
}

// An ejected host keeps local VM definitions under its new pool, but a VM it deleted in
// the old pool remains old-pool history. If it later rejoins that same pool, the master
// needs the tombstone to retire any stale owner record; the deleted definition must never
// reappear in the new pool's inventory.
func TestPoolVMInventoryPublishesOldPoolDeleteAfterEjectAndSamePoolRejoin(t *testing.T) {
	ctx := context.Background()
	master := peerStore(t, "master-1", "pool-1")
	member := peerStore(t, "member-1", "pool-1")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(member.dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", member.State.HostID)
	writeManagedJournal(t, filepath.Join(member.dir, "managed-vms"), "create", protocol.Command{OperationID: "create", Kind: "vm-create", VM: vm})
	writeManagedJournal(t, filepath.Join(member.dir, "managed-vms"), "delete", protocol.Command{OperationID: "delete", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: "pool-1", HostID: member.State.HostID}})
	if err := vmhost.RehomeLeavingPoolScope(filepath.Join(member.dir, "managed-vms"), "own-pool", member.State.HostID); err != nil {
		t.Fatal(err)
	}
	member.State.PoolID = "own-pool"
	if err := member.Save(); err != nil {
		t.Fatal(err)
	}
	if inv, err := localPoolVMInventory(ctx, member, 1); err != nil || len(inv.VMs) != 0 || len(inv.Deleted) != 0 {
		t.Fatalf("ejected own-pool inventory resurrected/deleted = %#v, %v", inv, err)
	}
	// Rejoining pool-1 rehomes no workload and leaves the old-pool delete history intact.
	member.State.PoolID = "pool-1"
	if err := member.Save(); err != nil {
		t.Fatal(err)
	}
	membership := maintenanceMembership(t, master, master.State.HostID, maintenanceMember(master.State.HostID, false), maintenanceMember(member.State.HostID, false))
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: "pool-1", VMID: vm.ID, HostID: member.State.HostID, State: "running", Epoch: 3, Spec: *vm}}, Inventory: map[string]poolVMInventory{}}); err != nil {
		t.Fatal(err)
	}
	if err := recordPoolVMInventory(master, membership, poolVMInventory{HostID: master.State.HostID, Generation: membership.Generation}); !errors.Is(err, errPoolVMInventoryIncomplete) {
		t.Fatalf("master snapshot = %v", err)
	}
	inv, err := localPoolVMInventory(ctx, member, membership.Generation)
	if err != nil || len(inv.VMs) != 0 || len(inv.Deleted) != 1 || inv.Deleted[0] != vm.ID {
		t.Fatalf("same-pool rejoin inventory = %#v, %v", inv, err)
	}
	if err = recordPoolVMInventory(master, membership, inv); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(master)
	if err != nil {
		t.Fatal(err)
	}
	if _, stale := state.VMs[vm.ID]; stale {
		t.Fatalf("same-pool delete did not prune stale owner: %#v", state.VMs)
	}
}

// The safety property the completeness rule exists for is unchanged: a member that can still
// own or run a pool VM — ready, or pending or failed after having been ready — must be in the
// picture before any owner record is settled from it. The refusal now names the host, because
// "pool VM inventory is incomplete for current membership" alone told an operator nothing.
func TestPoolVMInventoryStillRefusesAnIncompletePictureAndNamesTheHost(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	for _, status := range []string{"ready", "pending", "failed"} {
		t.Run(status, func(t *testing.T) {
			membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 12, Members: []protocol.PoolMember{
				{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 12, Status: "ready"},
				{HostID: "silent-1", Hostname: "silent-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 12, Status: status},
			}}
			state := poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{
				"master-1": {HostID: "master-1", Generation: 12},
			}}
			err := seedPoolVMOwners(s, membership, &state, "master-1")
			if !errors.Is(err, errPoolVMInventoryIncomplete) {
				t.Fatalf("a %s member that has not reported did not block the seed: %v", status, err)
			}
			if !strings.Contains(err.Error(), "silent-1") {
				t.Fatalf("the refusal does not name the host it is waiting for: %v", err)
			}
		})
	}
}
