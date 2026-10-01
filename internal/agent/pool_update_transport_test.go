// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
)

// The master's view of a member goes over the real pinned mTLS transport to the member's own
// handler: what the member answers is what the coordinator reads, not a zero value.
func TestLivePeersReadTheMembersAnswerOverTheRealTransport(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	engine := &stepEngine{refuse: map[string]*nexupdate.Error{}, status: nexupdate.ImageStatus{
		Channel: "stable", Phase: nexupdate.PhaseCommitted, StateFormat: 2,
		Slots: []nexupdate.SlotStatus{{Role: "booted", Sequence: 1}, {Role: "staged", Sequence: 2}}}}
	guests := &stepGuests{running: map[string]bool{"vm-1": true}}
	SetImageUpdater(engine)
	previous := poolUpdateGuestOps
	poolUpdateGuestOps = func(*Store) PoolUpdateGuests { return guests }
	t.Cleanup(func() { SetImageUpdater(nil); poolUpdateGuestOps = previous })

	member, ok := memberOf(master, "member-1")
	if !ok || member.PeerOrigin != lab.origin["member-1"] {
		t.Fatalf("the lab's member row = %+v", member)
	}
	peers := livePoolUpdatePeers{master}
	status, err := peers.Status(context.Background(), member)
	if err != nil {
		t.Fatal(err)
	}
	if status.HostID != "member-1" || status.Channel != "stable" || status.RunningSequence != 1 || status.StagedSequence != 2 ||
		status.StateFormat != 2 || !reflect.DeepEqual(status.RunningVMs, []string{"vm-1"}) || len(status.Capabilities) != 1 {
		t.Fatalf("status over the transport = %+v", status)
	}
	stage := protocol.PoolUpdateStep{RolloutID: "transport", StepID: poolUpdateStepID("transport", "member-1", "stage", 0), Action: "stage", Sequence: 2}
	result, err := peers.Step(context.Background(), member, stage)
	if err != nil || !result.Done {
		t.Fatalf("a staged host's stage step over the transport = %+v %v", result, err)
	}
	engine.status.Slots = []nexupdate.SlotStatus{{Role: "booted", Sequence: 1}}
	engine.refuse["apply"] = &nexupdate.Error{Code: "insufficient_space", Message: "no room"}
	result, err = peers.Step(context.Background(), member, stage)
	if err != nil || result.Done || result.Code != "insufficient_space" || result.Message != "no room" {
		t.Fatalf("a refused stage over the transport = %+v %v", result, err)
	}
}

// A promotion has the inherited rollout on disk before the write that makes the host master, so
// there is never a master without it; a promotion that fails takes it back out.
func TestPromotionRecordsTheInheritedRolloutBeforeItPublishesTheMaster(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, target := lab.host("master-1"), lab.host("member-1")
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	rollout := &protocol.PoolUpdateRollout{ID: "inherit-1", PoolID: "pool-1", Sequence: 2, RunningVMs: "refuse", State: protocol.PoolUpdateRunning,
		Phase: "hosts", MembershipGeneration: 1, CreatedAt: now, UpdatedAt: now,
		Hosts: []protocol.PoolUpdateHost{{HostID: "member-1", Stage: stageDrain}, {HostID: "master-1", Stage: stageStaged}}}
	if err := savePoolUpdate(master, rollout); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	replica := storedReplica(t, target)
	if replica.Replica.UpdateRollout == nil {
		t.Fatal("the replica carries no rollout")
	}
	record := &poolTransferRecord{OperationID: "op-inherit", Action: "promote", Role: "target", TargetHostID: "member-1", Stage: "activate"}
	var atPublish *protocol.PoolUpdateRollout
	poolTransferFailSavePeerAfterVMAdopt = func() error {
		// This is the last step before the peer record is written; the record must exist now.
		atPublish, _ = LoadPoolUpdate(target)
		return errors.New("test injected save failure")
	}
	t.Cleanup(func() { poolTransferFailSavePeerAfterVMAdopt = nil })
	if _, err := adoptPoolAuthorityFromReplica(context.Background(), target, replica.Replica, record); err == nil {
		t.Fatal("activation should fail when the peer-state write fails")
	}
	if atPublish == nil || atPublish.State != protocol.PoolUpdatePaused || atPublish.Pause.Code != protocol.PoolUpdateMasterChanged {
		t.Fatalf("at the moment of publishing, the inherited rollout was %+v", atPublish)
	}
	if got, _ := LoadPoolUpdate(target); got != nil {
		t.Fatalf("a promotion that failed left the inherited rollout behind: %+v", got)
	}
	if peerState(t, target).Membership.MasterHostID != "master-1" {
		t.Fatal("the target recorded itself master without a durable peer write")
	}
	poolTransferFailSavePeerAfterVMAdopt = nil
	if _, err := adoptPoolAuthorityFromReplica(context.Background(), target, replica.Replica, record); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadPoolUpdate(target); got == nil || got.State != protocol.PoolUpdatePaused || got.Hosts[0].Stage != stageDrain {
		t.Fatalf("the promoted master's rollout = %+v", got)
	}
}
