// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestSnapshotReserveAndSettlementReplicateBeforeAcknowledgementAndPromotion(t *testing.T) {
	lab := promotionLab(t, 2)
	master, owner, bystander := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	spec := labSpec(labVMA, owner.State.HostID)
	writePoolVMRecord(t, master, labOwnedVM(labVMA, owner.State.HostID))
	writeAppliedPoolVMSpec(t, owner, spec)

	command := protocol.Command{
		OperationID: "snapshot-promote",
		Kind:        "snapshot-create",
		VM:          &spec,
		Snapshot:    &protocol.VMSnapshotRequest{Name: "before-promotion"},
	}
	createdAt := time.Date(2026, 9, 26, 1, 2, 3, 0, time.UTC)
	reservation, err := ReservePoolVMSnapshot(context.Background(), owner, command, 1, strings.Repeat("a", 64), createdAt)
	if err != nil {
		t.Fatalf("reserve snapshot: %v", err)
	}
	assertSnapshotReplica := func(host *Store, settled *protocol.PoolVMRecoveryEvidence) {
		t.Helper()
		replica := storedReplica(t, host)
		if replica == nil {
			t.Fatalf("%s has no verified authority replica", host.State.HostID)
		}
		var records map[string]poolVMRecord
		if err := json.Unmarshal(replica.Replica.VMs, &records); err != nil {
			t.Fatalf("decode %s replica: %v", host.State.HostID, err)
		}
		record, ok := records[labVMA]
		if !ok {
			t.Fatalf("%s replica omitted VM %s", host.State.HostID, labVMA)
		}
		if settled == nil {
			if record.PendingRecovery == nil || *record.PendingRecovery != reservation || record.PendingSnapshot == nil || record.PendingSnapshot.ID != command.OperationID {
				t.Fatalf("%s acknowledged reservation without exact pending state: %#v", host.State.HostID, record)
			}
			return
		}
		if record.PendingRecovery != nil || record.PendingSnapshot != nil || record.LastSettledRecovery == nil || !reflect.DeepEqual(*record.LastSettledRecovery, *settled) {
			t.Fatalf("%s acknowledged settlement without exact terminal receipt: %#v", host.State.HostID, record)
		}
	}
	assertSnapshotReplica(owner, nil)
	assertSnapshotReplica(bystander, nil)

	masterState, err := loadPoolVMs(master)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := *masterState.VMs[labVMA].PendingSnapshot
	evidence := protocol.PoolVMRecoveryEvidence{
		Reservation:   reservation,
		Outcome:       "completed",
		JournalSHA256: strings.Repeat("b", 64),
		Snapshot:      &descriptor,
	}
	if err := poolVMSnapshotSettlementPeer(context.Background(), owner, evidence); err != nil {
		t.Fatalf("settle snapshot: %v", err)
	}
	assertSnapshotReplica(owner, &evidence)
	assertSnapshotReplica(bystander, &evidence)

	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	if result := ExecutePoolPromotion(context.Background(), owner, promoteCommand("promote-with-snapshot", owner.State.HostID)); result.Status != "succeeded" {
		t.Fatalf("promote member with acknowledged snapshot catalog: %#v", result)
	}
	adopted := poolVMRecords(t, owner)[labVMA]
	if len(adopted.Snapshots) != 1 || !reflect.DeepEqual(adopted.Snapshots[0], descriptor) {
		t.Fatalf("promotion did not adopt the acknowledged snapshot catalog: %#v", adopted)
	}
}

func TestSnapshotReplicaFailureKeepsDurableIntentForExactRetry(t *testing.T) {
	lab := promotionLab(t, 2)
	master, owner, bystander := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	spec := labSpec(labVMA, owner.State.HostID)
	writePoolVMRecord(t, master, labOwnedVM(labVMA, owner.State.HostID))
	writeAppliedPoolVMSpec(t, owner, spec)
	command := protocol.Command{
		OperationID: "snapshot-retry",
		Kind:        "snapshot-create",
		VM:          &spec,
		Snapshot:    &protocol.VMSnapshotRequest{Name: "retry-after-replica"},
	}
	createdAt := time.Date(2026, 9, 26, 2, 3, 4, 0, time.UTC)
	intent := strings.Repeat("c", 64)

	lab.drop(bystander.State.HostID, "!*")
	if _, err := ReservePoolVMSnapshot(context.Background(), owner, command, 1, intent, createdAt); err == nil {
		t.Fatal("reservation passed while a ready member could not acknowledge the replica")
	}
	state, err := loadPoolVMs(master)
	if err != nil || state.VMs[labVMA].PendingRecovery == nil || state.VMs[labVMA].PendingRecovery.OperationID != command.OperationID || len(state.VMs[labVMA].Snapshots) != 0 {
		t.Fatalf("lost reserve acknowledgement did not retain only the exact pending intent: %#v (%v)", state.VMs[labVMA], err)
	}
	lab.drop(bystander.State.HostID, "")
	reservation, err := ReservePoolVMSnapshot(context.Background(), owner, command, 1, intent, createdAt)
	if err != nil {
		t.Fatalf("exact reservation retry: %v", err)
	}
	state, err = loadPoolVMs(master)
	if err != nil || state.VMs[labVMA].PendingRecovery == nil || *state.VMs[labVMA].PendingRecovery != reservation || len(state.VMs[labVMA].Snapshots) != 0 {
		t.Fatalf("exact reserve retry changed intent or created a catalog point: %#v (%v)", state.VMs[labVMA], err)
	}

	descriptor := *state.VMs[labVMA].PendingSnapshot
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("d", 64), Snapshot: &descriptor}
	lab.drop(bystander.State.HostID, "!*")
	if err := poolVMSnapshotSettlementPeer(context.Background(), owner, evidence); err == nil {
		t.Fatal("settlement passed while a ready member could not acknowledge the terminal replica")
	}
	state, err = loadPoolVMs(master)
	if err != nil || state.VMs[labVMA].PendingRecovery != nil || len(state.VMs[labVMA].Snapshots) != 1 || state.VMs[labVMA].Snapshots[0].ID != command.OperationID {
		t.Fatalf("lost settlement acknowledgement did not retain one exact committed catalog point: %#v (%v)", state.VMs[labVMA], err)
	}
	lab.drop(bystander.State.HostID, "")
	if err := poolVMSnapshotSettlementPeer(context.Background(), owner, evidence); err != nil {
		t.Fatalf("exact settlement retry: %v", err)
	}
	state, err = loadPoolVMs(master)
	if err != nil || len(state.VMs[labVMA].Snapshots) != 1 || state.VMs[labVMA].Snapshots[0].ID != command.OperationID {
		t.Fatalf("exact settlement retry duplicated the catalog point: %#v (%v)", state.VMs[labVMA], err)
	}
	for _, host := range []*Store{owner, bystander} {
		replica := storedReplica(t, host)
		var records map[string]poolVMRecord
		if replica == nil || json.Unmarshal(replica.Replica.VMs, &records) != nil || len(records[labVMA].Snapshots) != 1 || records[labVMA].Snapshots[0].ID != command.OperationID {
			t.Fatalf("%s did not converge on the exact settled catalog point: %+v", host.State.HostID, replica)
		}
	}
}
