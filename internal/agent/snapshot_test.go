// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func writeSnapshotTestVMJournal(t *testing.T, s *Store, vm *protocol.VMCreateSpec) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"command": protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: vm},
		"result":  protocol.Result{OperationID: "create-1", Status: "succeeded"},
		"stage":   "defined",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "create-1.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalSnapshotAdmissionPersistsExactRequestAndScopesReplay(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	writeSnapshotTestVMJournal(t, s, vm)
	dir := filepath.Join(s.dir, "managed-vms")
	request := SnapshotRequest{VMID: vm.ID, Command: &protocol.Command{
		OperationID: "snapshot-1", Kind: "snapshot-create", Snapshot: &protocol.VMSnapshotRequest{Name: "Before update"},
	}}
	response := acceptSnapshotRequest(context.Background(), s, request)
	if response.Err != nil || response.Result == nil || response.Result.Status != "pending" {
		t.Fatalf("admission: %#v", response)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved State
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	entry := saved.Journal["snapshot-1"]
	if entry.Order == 0 || entry.Command == nil || entry.Command.VM == nil || entry.Command.VM.ID != vm.ID || entry.Command.Snapshot.Name != "Before update" || entry.Status != "pending" {
		t.Fatalf("acknowledged without exact durable command: %#v", entry)
	}
	s.State = saved
	// An exact retry uses the retained spec even after the live definition is gone.
	if err := os.Remove(filepath.Join(dir, "create-1.json")); err != nil {
		t.Fatal(err)
	}
	if got := acceptSnapshotRequest(context.Background(), s, request); got.Err != nil || got.Result.OperationID != "snapshot-1" {
		t.Fatalf("exact replay: %#v", got)
	}
	if s.State.Journal["snapshot-1"].Order != entry.Order {
		t.Fatal("exact replay after state reload changed the durable operation order")
	}
	request.Command.Snapshot.Name = "Different intent"
	if got := acceptSnapshotRequest(context.Background(), s, request); !errors.Is(got.Err, ErrSnapshotConflict) {
		t.Fatalf("changed intent replay: %#v", got)
	}
	lookup := SnapshotRequest{VMID: vm.ID, OperationID: "snapshot-1"}
	if got := acceptSnapshotRequest(context.Background(), s, lookup); got.Err != nil || got.Result.Status != "pending" {
		t.Fatalf("retained operation lookup: %#v", got)
	}
	lookup.VMID = "22222222-2222-4222-8222-222222222222"
	if got := acceptSnapshotRequest(context.Background(), s, lookup); !errors.Is(got.Err, ErrSnapshotNotFound) {
		t.Fatalf("cross-VM lookup: %#v", got)
	}
	entry = s.State.Journal["snapshot-1"]
	entry.Status = "needs_attention"
	s.State.Journal["snapshot-1"] = entry
	request.Command.OperationID = "snapshot-2"
	if got := acceptSnapshotRequest(context.Background(), s, request); !errors.Is(got.Err, ErrSnapshotConflict) {
		t.Fatalf("new operation bypassed unresolved snapshot: %#v", got)
	}
	request.Command.OperationID = "snapshot-1"
	request.Command.Snapshot.Name = "Before update"
	if got := acceptSnapshotRequest(context.Background(), s, request); got.Err != nil || got.Result.Status != "pending" {
		t.Fatalf("exact reconciliation request: %#v", got)
	}
}

func TestLocalSnapshotRevertAdmissionCapturesCatalogTargetAndExactReplay(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	writeSnapshotTestVMJournal(t, s, vm)
	if err := poolVMLocal(s, vm.ID, "define", nil, true, vm, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[vm.ID]
	reservation, err := poolVMRecoveryReservationFor(record, "snapshot-001", "snapshot-create", strings.Repeat("a", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	target := snapshotDescriptorForTest(reservation, "before-update", "known good")
	record.Snapshots = []protocol.PoolVMSnapshot{target}
	state.VMs[vm.ID] = record
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	missing := SnapshotRequest{VMID: vm.ID, Command: &protocol.Command{
		OperationID: "revert-missing", Kind: "snapshot-revert", Snapshot: &protocol.VMSnapshotRequest{SnapshotID: "snapshot-missing"},
	}}
	if got := acceptSnapshotRequest(context.Background(), s, missing); !errors.Is(got.Err, ErrSnapshotTargetNotFound) {
		t.Fatalf("unknown target snapshot: %#v", got)
	}
	request := SnapshotRequest{VMID: vm.ID, Command: &protocol.Command{
		OperationID: "revert-001", Kind: "snapshot-revert", Snapshot: &protocol.VMSnapshotRequest{SnapshotID: target.ID},
	}}
	accepted := acceptSnapshotRequest(context.Background(), s, request)
	if accepted.Err != nil || accepted.Result == nil || accepted.Result.Status != "pending" {
		t.Fatalf("revert admission: %#v", accepted)
	}
	entry := s.State.Journal[request.Command.OperationID]
	if entry.Order == 0 || entry.Command == nil || entry.Command.VM == nil || entry.Command.VM.ID != vm.ID ||
		entry.Command.Snapshot == nil || entry.Command.Snapshot.SnapshotID != target.ID || entry.Command.Snapshot.Target == nil ||
		!reflect.DeepEqual(*entry.Command.Snapshot.Target, target) || entry.Status != "pending" {
		t.Fatalf("accepted revert did not persist current VM and exact catalog descriptor: %#v", entry)
	}
	request.Command.Snapshot.SnapshotID = "snapshot-unknown"
	if got := acceptSnapshotRequest(context.Background(), s, request); !errors.Is(got.Err, ErrSnapshotConflict) {
		t.Fatalf("same revert operation ID changed target: %#v", got)
	}
	request.Command.Snapshot.SnapshotID = request.Command.OperationID
	if got := acceptSnapshotRequest(context.Background(), s, request); !errors.Is(got.Err, ErrSnapshotConflict) {
		t.Fatalf("same revert operation ID changed target to itself: %#v", got)
	}
	request.Command.Snapshot.SnapshotID = target.ID
	if got := acceptSnapshotRequest(context.Background(), s, request); got.Err != nil || got.Result == nil || got.Result.OperationID != request.Command.OperationID {
		t.Fatalf("exact durable revert retry: %#v", got)
	}
	lookup := SnapshotRequest{VMID: vm.ID, OperationID: request.Command.OperationID}
	if got := acceptSnapshotRequest(context.Background(), s, lookup); got.Err != nil || got.Result == nil || got.Result.Status != "pending" {
		t.Fatalf("revert status lookup: %#v", got)
	}
	lookup.VMID = "22222222-2222-4222-8222-222222222222"
	if got := acceptSnapshotRequest(context.Background(), s, lookup); !errors.Is(got.Err, ErrSnapshotNotFound) {
		t.Fatalf("cross-VM revert status lookup: %#v", got)
	}
	if got := acceptSnapshotRequest(context.Background(), s, SnapshotRequest{VMID: vm.ID, Command: &protocol.Command{
		OperationID: target.ID, Kind: "snapshot-revert", Snapshot: &protocol.VMSnapshotRequest{SnapshotID: target.ID},
	}}); !errors.Is(got.Err, ErrSnapshotInvalid) {
		t.Fatalf("operation ID reused as target ID: %#v", got)
	}
	newRequest := SnapshotRequest{VMID: vm.ID, Command: &protocol.Command{
		OperationID: "revert-003", Kind: "snapshot-revert", Snapshot: &protocol.VMSnapshotRequest{SnapshotID: target.ID},
	}}
	if got := acceptSnapshotRequest(context.Background(), s, newRequest); !errors.Is(got.Err, ErrSnapshotConflict) {
		t.Fatalf("second revert bypassed unresolved first revert: %#v", got)
	}
	entry = s.State.Journal[request.Command.OperationID]
	entry.Status, entry.Error = "needs_attention", "native state uncertain"
	s.State.Journal[request.Command.OperationID] = entry
	if got := acceptSnapshotRequest(context.Background(), s, request); got.Err != nil || got.Result == nil || got.Result.Status != "pending" {
		t.Fatalf("exact unresolved revert did not re-enter observation: %#v", got)
	}
	if !s.State.Journal[request.Command.OperationID].RequestedAt.Equal(entry.RequestedAt) || s.State.Journal[request.Command.OperationID].Order != entry.Order {
		t.Fatal("revert retry changed its durable acceptance order")
	}
}

func TestSnapshotBackgroundDispatchLeavesEmergencyAndUnrelatedControlAvailable(t *testing.T) {
	s := testStore(t)
	snapshot := protocol.Command{OperationID: "snapshot-1", Kind: "snapshot-create", VM: &protocol.VMCreateSpec{ID: "vm-1"}, Snapshot: &protocol.VMSnapshotRequest{Name: "Before update"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{snapshot}}); err != nil {
		t.Fatal(err)
	}
	stop := protocol.Command{OperationID: "stop-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: "vm-1", Action: "force-stop"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{stop}}); err != nil {
		t.Fatal(err)
	}
	if s.State.Journal[stop.OperationID].AfterOperationID != "" || nextManagedCommand(s.State.Journal, snapshot.OperationID) != stop.OperationID {
		t.Fatal("snapshot blocked emergency stop dispatch")
	}
	entry := s.State.Journal[stop.OperationID]
	entry.Status = "succeeded"
	s.State.Journal[stop.OperationID] = entry
	unrelated := protocol.Command{OperationID: "start-2", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: "vm-2", Action: "start"}}
	join := protocol.Command{OperationID: "join-1", Kind: "pool-join"}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{join, unrelated}}); err != nil {
		t.Fatal(err)
	}
	if got := nextManagedCommand(s.State.Journal, snapshot.OperationID); got != unrelated.OperationID {
		t.Fatalf("expected unrelated VM control while snapshot/identity change held, got %q", got)
	}
	entry = s.State.Journal[snapshot.OperationID]
	entry.Status = "needs_attention"
	s.State.Journal[snapshot.OperationID] = entry
	if !hasUnsettledSnapshot(s.State.Journal, "") || nextManagedCommand(s.State.Journal, "") == join.OperationID {
		t.Fatal("unresolved durable snapshot allowed pool identity rewrite")
	}
}

func TestSnapshotRevertIsManagedQueuedAndFencesPoolIdentityChanges(t *testing.T) {
	s := testStore(t)
	vm := &protocol.VMCreateSpec{ID: "vm-1", PoolID: "pool-1", HostID: "host-1", Name: "guest"}
	revert := protocol.Command{
		OperationID: "revert-1", Kind: "snapshot-revert", VM: vm,
		Snapshot: &protocol.VMSnapshotRequest{SnapshotID: "snapshot-1", Target: &protocol.PoolVMSnapshot{ID: "snapshot-1"}},
	}
	if !managedCommand(revert.Kind) || commandHost(revert) != vm.HostID {
		t.Fatal("snapshot revert was not treated as a host-addressed managed command")
	}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{revert}}); err != nil {
		t.Fatal(err)
	}
	if got := nextManagedCommand(s.State.Journal, ""); got != revert.OperationID {
		t.Fatalf("revert was not selected from the durable queue: %q", got)
	}
	start := protocol.Command{OperationID: "start-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, HostID: vm.HostID, Action: "start"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{start}}); err != nil {
		t.Fatal(err)
	}
	if s.State.Journal[start.OperationID].AfterOperationID != revert.OperationID {
		t.Fatal("same-VM power change did not wait for snapshot revert")
	}
	entry := s.State.Journal[revert.OperationID]
	entry.Status = "needs_attention"
	s.State.Journal[revert.OperationID] = entry
	if nextManagedCommand(s.State.Journal, "") == start.OperationID {
		t.Fatal("already-chained same-VM start was released while revert needed attention")
	}
	entry = s.State.Journal[start.OperationID]
	entry.Status = "failed"
	s.State.Journal[start.OperationID] = entry
	reboot := protocol.Command{OperationID: "reboot-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, HostID: vm.HostID, Action: "reboot"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{reboot}}); err != nil {
		t.Fatal(err)
	}
	if s.State.Journal[reboot.OperationID].AfterOperationID != revert.OperationID {
		t.Fatal("new same-VM reboot was not linked to unresolved snapshot revert")
	}
	stop := protocol.Command{OperationID: "stop-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, HostID: vm.HostID, Action: "force-stop", AfterOperationID: revert.OperationID}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{stop}}); err != nil {
		t.Fatal(err)
	}
	if s.State.Journal[stop.OperationID].AfterOperationID != "" || s.State.Journal[stop.OperationID].Command.Power.AfterOperationID != revert.OperationID || dispatchCommand(s.State.Journal[stop.OperationID], s.State.Journal).Power.AfterOperationID != "" || nextManagedCommand(s.State.Journal, revert.OperationID) != stop.OperationID {
		t.Fatal("snapshot revert blocked emergency force-stop")
	}
	entry = s.State.Journal[stop.OperationID]
	entry.Status = "succeeded"
	s.State.Journal[stop.OperationID] = entry
	shutdown := protocol.Command{OperationID: "shutdown-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, HostID: vm.HostID, Action: "shutdown"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{shutdown}}); err != nil {
		t.Fatal(err)
	}
	if s.State.Journal[shutdown.OperationID].AfterOperationID != "" || s.State.Journal[shutdown.OperationID].Command.Power.AfterOperationID != "" || dispatchCommand(s.State.Journal[shutdown.OperationID], s.State.Journal).Power.AfterOperationID != "" || nextManagedCommand(s.State.Journal, revert.OperationID) != shutdown.OperationID {
		t.Fatal("snapshot revert blocked emergency shutdown")
	}
	entry = s.State.Journal[shutdown.OperationID]
	entry.Status = "succeeded"
	s.State.Journal[shutdown.OperationID] = entry
	unrelated := protocol.Command{OperationID: "other-vm-start", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: "vm-2", HostID: vm.HostID, Action: "start"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{unrelated}}); err != nil {
		t.Fatal(err)
	}
	join := protocol.Command{OperationID: "join-1", Kind: "pool-join"}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{join}}); err != nil {
		t.Fatal(err)
	}
	if !hasUnsettledSnapshot(s.State.Journal, vm.ID) || !hasUnsettledSnapshot(s.State.Journal, "") || nextManagedCommand(s.State.Journal, "") != unrelated.OperationID {
		t.Fatal("unresolved snapshot revert allowed a pool identity rewrite")
	}
	outcomes := commandOutcomes(s.State.Journal, nil)
	found := false
	for _, outcome := range outcomes {
		if outcome.OperationID == revert.OperationID {
			found = outcome.Kind == "snapshot-revert" && outcome.Status == "needs_attention" && outcome.ID == vm.ID
		}
	}
	if !found {
		t.Fatalf("replacement Controller outcome omitted snapshot revert: %#v", outcomes)
	}
}

func TestNeedsAttentionSnapshotKeepsSameVMPowerBehindBothSnapshotKinds(t *testing.T) {
	for _, kind := range []string{"snapshot-create", "snapshot-revert", "snapshot-delete"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			vm := &protocol.VMCreateSpec{ID: "vm-1", PoolID: "pool-1", HostID: "host-1", Name: "guest"}
			oldPower := protocol.Command{OperationID: "old-power", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, HostID: vm.HostID, Action: "start"}}
			s.State.Journal[oldPower.OperationID] = JournalEntry{Order: 1, Status: "failed", Command: &oldPower}
			snapshot := protocol.Command{OperationID: "snapshot-1", Kind: kind, VM: vm}
			if kind == "snapshot-create" {
				snapshot.Snapshot = &protocol.VMSnapshotRequest{Name: "before-update"}
			} else {
				snapshot.Snapshot = &protocol.VMSnapshotRequest{SnapshotID: "target-1", Target: &protocol.PoolVMSnapshot{ID: "target-1"}}
			}
			if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{snapshot}}); err != nil {
				t.Fatal(err)
			}
			start := protocol.Command{OperationID: "start-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, HostID: vm.HostID, Action: "start", AfterOperationID: oldPower.OperationID}}
			if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{start}}); err != nil {
				t.Fatal(err)
			}
			if s.State.Journal[start.OperationID].AfterOperationID != oldPower.OperationID || s.State.Journal[start.OperationID].Command.Power.AfterOperationID != oldPower.OperationID || dispatchCommand(s.State.Journal[start.OperationID], s.State.Journal).Power.AfterOperationID != oldPower.OperationID {
				t.Fatal("older explicit predecessor bypassed the unresolved snapshot or changed replay identity")
			}
			entry := s.State.Journal[snapshot.OperationID]
			entry.Status = "needs_attention"
			s.State.Journal[snapshot.OperationID] = entry
			if got := nextManagedCommand(s.State.Journal, ""); got == start.OperationID {
				t.Fatal("already-chained start was released while snapshot recovery was unresolved")
			}
			entry = s.State.Journal[start.OperationID]
			entry.Status = "failed"
			s.State.Journal[start.OperationID] = entry
			reboot := protocol.Command{OperationID: "reboot-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, HostID: vm.HostID, Action: "reboot"}}
			if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{reboot}}); err != nil {
				t.Fatal(err)
			}
			if s.State.Journal[reboot.OperationID].AfterOperationID != snapshot.OperationID {
				t.Fatal("new reboot was not linked to needs_attention snapshot")
			}
			entry = s.State.Journal[snapshot.OperationID]
			entry.Status = "succeeded"
			s.State.Journal[snapshot.OperationID] = entry
			entry = s.State.Journal[reboot.OperationID]
			entry.Status = "succeeded"
			s.State.Journal[reboot.OperationID] = entry
			conditional := protocol.Command{OperationID: "conditional-start", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, HostID: vm.HostID, Action: "start", AfterOperationID: oldPower.OperationID}}
			if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{conditional}}); err != nil {
				t.Fatal(err)
			}
			if got := nextManagedCommand(s.State.Journal, ""); got != conditional.OperationID || dispatchCommand(s.State.Journal[conditional.OperationID], s.State.Journal).Power.AfterOperationID != oldPower.OperationID {
				t.Fatal("failed original predecessor was lost when the conditional command was dispatched")
			}
		})
	}
}

func TestUnsettledSnapshotBlocksSameVMNonEmergencyManagedCommands(t *testing.T) {
	s := testStore(t)
	vm := &protocol.VMCreateSpec{ID: "vm-1", PoolID: "pool-1", HostID: "host-1", Name: "guest"}
	snapshot := protocol.Command{OperationID: "snapshot-1", Kind: "snapshot-create", VM: vm, Snapshot: &protocol.VMSnapshotRequest{Name: "before"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{snapshot}}); err != nil {
		t.Fatal(err)
	}
	entry := s.State.Journal[snapshot.OperationID]
	entry.Status = "needs_attention"
	s.State.Journal[snapshot.OperationID] = entry
	update := protocol.Command{OperationID: "update-1", Kind: "vm-update", VM: vm}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{update}}); err != nil {
		t.Fatal(err)
	}
	deleteCmd := protocol.Command{OperationID: "delete-1", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{deleteCmd}}); err != nil {
		t.Fatal(err)
	}
	other := protocol.Command{OperationID: "other-start", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: "vm-2", HostID: vm.HostID, Action: "start"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{other}}); err != nil {
		t.Fatal(err)
	}
	if got := nextManagedCommand(s.State.Journal, ""); got != other.OperationID {
		t.Fatalf("cross-VM control should stay available ahead of snapshot heal: got %q", got)
	}
	entry = s.State.Journal[other.OperationID]
	entry.Status = "succeeded"
	s.State.Journal[other.OperationID] = entry
	if got := nextManagedCommand(s.State.Journal, ""); got == update.OperationID || got == deleteCmd.OperationID {
		t.Fatalf("same-VM non-emergency work dispatched while snapshot needs attention: %q", got)
	}
	if got := nextManagedCommand(s.State.Journal, ""); got != snapshot.OperationID {
		t.Fatalf("expected snapshot heal when no cross-VM work remains: got %q", got)
	}
	edit := protocol.Command{OperationID: "edit-pending", Kind: "vm-update", VM: vm}
	s.State.Journal[edit.OperationID] = JournalEntry{Order: 50, Status: "pending", Command: &edit}
	stop := protocol.Command{OperationID: "stop-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, HostID: vm.HostID, Action: "force-stop"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{stop}}); err != nil {
		t.Fatal(err)
	}
	if s.State.Journal[stop.OperationID].AfterOperationID != edit.OperationID {
		t.Fatalf("emergency stop cleared a non-snapshot predecessor: %#v", s.State.Journal[stop.OperationID])
	}
}

func TestNeedsAttentionSnapshotReentersFromQueueAndControllerReplay(t *testing.T) {
	s := testStore(t)
	vm := &protocol.VMCreateSpec{ID: "vm-1", PoolID: "pool-1", HostID: "host-1", Name: "guest"}
	snapshot := protocol.Command{OperationID: "snapshot-1", Kind: "snapshot-create", VM: vm, Snapshot: &protocol.VMSnapshotRequest{Name: "before"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{snapshot}}); err != nil {
		t.Fatal(err)
	}
	entry := s.State.Journal[snapshot.OperationID]
	entry.Status, entry.Error = "needs_attention", "native state uncertain"
	s.State.Journal[snapshot.OperationID] = entry
	if got := nextManagedCommand(s.State.Journal, ""); got != snapshot.OperationID {
		t.Fatalf("needs_attention snapshot was not selected for observation: %q", got)
	}
	if got := nextManagedCommand(s.State.Journal, snapshot.OperationID); got != snapshot.OperationID {
		t.Fatalf("active needs_attention snapshot did not re-enter: %q", got)
	}
	otherSnap := protocol.Command{OperationID: "snapshot-2", Kind: "snapshot-create", VM: &protocol.VMCreateSpec{ID: "vm-2"}, Snapshot: &protocol.VMSnapshotRequest{Name: "other"}}
	s.State.Journal[otherSnap.OperationID] = JournalEntry{Order: 2, Status: "pending", Command: &otherSnap}
	if got := nextManagedCommand(s.State.Journal, snapshot.OperationID); got == otherSnap.OperationID {
		t.Fatal("second snapshot started while first still needs attention")
	}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{snapshot}}); err != nil {
		t.Fatal(err)
	}
	if s.State.Journal[snapshot.OperationID].Status != "pending" || s.State.Journal[snapshot.OperationID].Error != "" {
		t.Fatalf("identical Controller replay did not flip needs_attention to pending: %#v", s.State.Journal[snapshot.OperationID])
	}
}

func TestSnapshotAdmissionWaitsForPendingDelete(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	delete := protocol.Command{OperationID: "delete-1", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{
		VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID,
	}}
	s.State.Journal[delete.OperationID] = JournalEntry{Command: &delete, Status: "pending", Order: 1}
	request := SnapshotRequest{VMID: vm.ID, Command: &protocol.Command{
		OperationID: "snapshot-1", Kind: "snapshot-create", Snapshot: &protocol.VMSnapshotRequest{Name: "Before delete"},
	}}
	if got := acceptSnapshotRequest(context.Background(), s, request); !errors.Is(got.Err, ErrSnapshotConflict) {
		t.Fatalf("snapshot accepted while delete is pending: %#v", got)
	}
}

func TestSnapshotBackgroundExecutionRejectsAnotherHostBeforeNativeWork(t *testing.T) {
	command := protocol.Command{OperationID: "snapshot-1", Kind: "snapshot-create", VM: &protocol.VMCreateSpec{ID: "vm-1", PoolID: "pool-1", HostID: "host-other"}}
	result, err := executeStoppedSnapshot(context.Background(), t.TempDir(), State{HostID: "host-local", PoolID: "pool-1"}, command)
	if err != nil || result.OperationID != command.OperationID || result.Status != "failed" {
		t.Fatalf("foreign snapshot command was not refused before native work: %#v, %v", result, err)
	}
}

func TestSnapshotBackgroundExecutionRequiresDurableAdmissionOrder(t *testing.T) {
	command := protocol.Command{OperationID: "snapshot-1", Kind: "snapshot-create", VM: &protocol.VMCreateSpec{ID: "vm-1", PoolID: "pool-1", HostID: "host-local"}}
	different := command
	different.OperationID = "snapshot-other"
	for _, entry := range []JournalEntry{
		{Command: &command},
		{Order: 1},
		{Order: 1, Command: &different},
	} {
		identity := State{HostID: "host-local", PoolID: "pool-1", Journal: map[string]JournalEntry{command.OperationID: entry}}
		result, err := executeStoppedSnapshot(context.Background(), t.TempDir(), identity, command)
		if err == nil || err.Error() != "snapshot command has no matching durable admission order" || result.OperationID != command.OperationID {
			t.Fatalf("snapshot without matching durable order reached execution: %#v, %v", result, err)
		}
	}
}
