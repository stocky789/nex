// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

var automaticPromotionTestNow time.Time

func useAutomaticPromotionTestClock(t *testing.T) {
	t.Helper()
	previousClock, previousNow := automaticPoolPromotionClock, automaticPromotionTestNow
	automaticPromotionTestNow = time.Now()
	automaticPoolPromotionClock = func() time.Time { return automaticPromotionTestNow }
	automaticPoolPromotionHoldMu.Lock()
	automaticPoolPromotionHolds = map[string]automaticPoolPromotionHoldObservation{}
	automaticPoolPromotionHoldMu.Unlock()
	t.Cleanup(func() {
		automaticPoolPromotionClock, automaticPromotionTestNow = previousClock, previousNow
		automaticPoolPromotionHoldMu.Lock()
		automaticPoolPromotionHolds = map[string]automaticPoolPromotionHoldObservation{}
		automaticPoolPromotionHoldMu.Unlock()
	})
}

func advanceAutomaticPromotionTestClock(d time.Duration) {
	automaticPromotionTestNow = automaticPromotionTestNow.Add(d)
}

func automaticBMCProfileLab(t *testing.T, members int) (*transferLab, *Store, *Store) {
	t.Helper()
	lab := newTransferLab(t, members)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	uuids := map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"}
	for i := 1; i <= members; i++ {
		uuids[fmt.Sprintf("member-%d", i)] = fmt.Sprintf("5b5b5655-0148-4921-9168-c5d15f5464%02x", i)
	}
	labSystemUUIDs(t, uuids)
	for _, host := range lab.hosts {
		if err := ReportPoolSystemUUID(context.Background(), host); err != nil {
			t.Fatalf("report system UUID for %s: %v", host.State.HostID, err)
		}
	}
	server := labRedfishBMC(t, uuids[master.State.HostID])
	profile := labBMCProfile(server, "auto-bmc")
	writeLabBMCCredential(t, master, profile.CredentialRef)
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("auto-fence-profile", master.State.HostID, master.State.HostID, profile)); result.Status != "succeeded" {
		t.Fatalf("record BMC fence profile: %#v", result)
	}
	if err := PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err != nil {
		t.Fatalf("push BMC fence profile: %v", err)
	}
	if err := RunPoolFenceChecks(context.Background(), candidate); err != nil {
		t.Fatalf("candidate BMC checks: %v", err)
	}
	resetAutomaticPoolPromotionHold(candidate.State.HostID)
	return lab, master, candidate
}

func noRealPoolFence(t *testing.T, lab *transferLab, calls *int) {
	t.Helper()
	fakeFence(t, func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
		(*calls)++
		lab.powerOff(hostID)
		return protocol.StorageClusterFenceStatus{RequestID: operationID, HostID: hostID, TargetID: "system-1", Status: "passed", ExclusionVerified: true, ConfigGeneration: generation, ConfigDigest: digest, ObservedAt: time.Now().UTC()}, nil
	})
}

func TestAutomaticPoolPromotionRequiresSafeTwoHostPreflight(t *testing.T) {
	useAutomaticPromotionTestClock(t)
	t.Run("reachable old master is not fenced", func(t *testing.T) {
		lab, _, candidate := automaticBMCProfileLab(t, 1)
		calls := 0
		noRealPoolFence(t, lab, &calls)
		if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
			t.Fatalf("reachable master triggered automatic promotion or fencing: calls=%d", calls)
		}
	})

	t.Run("missing BMC profile", func(t *testing.T) {
		lab := newTransferLab(t, 1)
		master, candidate := lab.host("master-1"), lab.host("member-1")
		if err := PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		lab.powerOff(master.State.HostID)
		calls := 0
		noRealPoolFence(t, lab, &calls)
		if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
			t.Fatalf("missing BMC profile triggered automatic promotion or fencing: calls=%d", calls)
		}
	})

	t.Run("disabled host fencing", func(t *testing.T) {
		lab, master, candidate := automaticBMCProfileLab(t, 1)
		poolPeerMu.Lock()
		state, err := loadPoolPeer(master)
		if err == nil {
			state.Membership.HostFencingDisabled = true
			err = savePoolPeer(master, state)
		}
		poolPeerMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if err = PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		lab.powerOff(master.State.HostID)
		calls := 0
		noRealPoolFence(t, lab, &calls)
		if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
			t.Fatalf("disabled host fencing triggered automatic promotion or fencing: calls=%d", calls)
		}
	})

	t.Run("third ready host blocks", func(t *testing.T) {
		lab, master, candidate := automaticBMCProfileLab(t, 2)
		lab.powerOff(master.State.HostID)
		calls := 0
		noRealPoolFence(t, lab, &calls)
		if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
			t.Fatalf("three ready hosts triggered automatic promotion or fencing: calls=%d", calls)
		}
	})

	t.Run("concurrent transfer blocks", func(t *testing.T) {
		lab, _, candidate := automaticBMCProfileLab(t, 1)
		if err := savePoolTransfer(candidate, &poolTransferRecord{OperationID: "manual-op", Action: "master-change", Role: "candidate", Stage: "preflight", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		lab.powerOff("master-1")
		calls := 0
		noRealPoolFence(t, lab, &calls)
		if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
			t.Fatalf("concurrent transfer triggered automatic promotion or fencing: calls=%d", calls)
		}
	})

	t.Run("BMC check must be fresh and passing", func(t *testing.T) {
		for _, stale := range []bool{false, true} {
			lab, master, candidate := automaticBMCProfileLab(t, 1)
			poolFenceCheckMu.Lock()
			check := poolFenceChecks[candidate.dir][candidate.State.HostID][master.State.HostID]
			if stale {
				check.ObservedAt = time.Now().Add(-poolFenceCheckStaleAfter() - time.Second)
			}
			poolFenceChecks[candidate.dir][candidate.State.HostID][master.State.HostID] = check
			if !stale {
				delete(poolFenceChecks[candidate.dir][candidate.State.HostID], master.State.HostID)
			}
			poolFenceCheckMu.Unlock()
			lab.powerOff(master.State.HostID)
			calls := 0
			noRealPoolFence(t, lab, &calls)
			if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
				t.Fatalf("missing/stale check triggered automatic promotion or fencing (stale=%v, calls=%d)", stale, calls)
			}
		}
	})

	t.Run("ambiguous transient peer error blocks", func(t *testing.T) {
		lab, _, candidate := automaticBMCProfileLab(t, 1)
		// The peer handles the authority probe but its response is lost, so the
		// candidate cannot safely distinguish a reachable master from a dead one.
		lab.drop("master-1", poolAuthorityPath)
		calls := 0
		noRealPoolFence(t, lab, &calls)
		if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
			t.Fatalf("one lost response triggered automatic promotion or fencing: calls=%d", calls)
		}
		lab.drop("master-1", "")
		advanceAutomaticPromotionTestClock(automaticPoolPromotionHold)
		if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
			t.Fatalf("ambiguous transient error triggered automatic promotion or fencing: calls=%d", calls)
		}
	})
}

func TestAutomaticPoolPromotionResumesAndRechecksReturningHost(t *testing.T) {
	useAutomaticPromotionTestClock(t)
	t.Run("interrupted automatic promotion resumes", func(t *testing.T) {
		lab, master, candidate := automaticBMCProfileLab(t, 1)
		lab.powerOff(master.State.HostID)
		calls := 0
		noRealPoolFence(t, lab, &calls)
		if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
			t.Fatal("automatic promotion skipped the initial hold")
		}
		advanceAutomaticPromotionTestClock(automaticPoolPromotionHold)
		if !ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) {
			t.Fatal("automatic promotion did not start after the hold")
		}
		record, err := loadPoolTransfer(candidate)
		if err != nil || record == nil || !record.Automatic || record.OperationID != automaticPoolPromotionOperationID("pool-1", candidate.State.HostID, record.AuthorityGeneration) {
			t.Fatalf("automatic operation was not durable and deterministic: %#v (%v)", record, err)
		}
		if calls != 1 {
			t.Fatalf("fence calls=%d, want one", calls)
		}
		status := PoolPromotionReport(candidate)
		if status == nil || !status.Automatic {
			t.Fatalf("automatic source was not reported: %#v", status)
		}
		if peerState(t, candidate).Membership.MasterHostID != candidate.State.HostID {
			t.Fatalf("candidate did not activate pool authority: stage=%s", record.Stage)
		}
	})

	t.Run("returning old master blocks activation", func(t *testing.T) {
		lab, master, candidate := automaticBMCProfileLab(t, 1)
		lab.powerOff(master.State.HostID)
		calls := 0
		fakeFence(t, func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			calls++
			lab.powerOff(hostID)
			proof := protocol.StorageClusterFenceStatus{RequestID: operationID, HostID: hostID, TargetID: "system-1", Status: "passed", ExclusionVerified: true, ConfigGeneration: generation, ConfigDigest: digest, ObservedAt: time.Now().UTC()}
			lab.powerOn(hostID)
			return proof, nil
		})
		if ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 0 {
			t.Fatal("automatic promotion skipped the initial hold")
		}
		advanceAutomaticPromotionTestClock(automaticPoolPromotionHold)
		if !ReconcileAutomaticPoolPromotion(context.Background(), candidate, time.Second) || calls != 1 {
			t.Fatalf("automatic attempt did not exercise its fence path: calls=%d", calls)
		}
		if peerState(t, master).Membership.MasterHostID != master.State.HostID || peerState(t, candidate).Membership.MasterHostID == candidate.State.HostID {
			t.Fatal("the returning old master was promoted over")
		}
		before := calls
		AdvancePoolAuthorityTransfer(context.Background(), candidate, time.Second)
		if calls != before || peerState(t, master).Membership.MasterHostID != master.State.HostID {
			t.Fatalf("a returning master triggered another fence or was superseded: calls=%d", calls)
		}
	})

	t.Run("durable interruption reloads after fence without fencing twice", func(t *testing.T) {
		lab, master, candidate := automaticBMCProfileLab(t, 1)
		lab.powerOff(master.State.HostID)
		calls := 0
		fakeFence(t, func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			calls++
			lab.powerOff(hostID)
			proof := protocol.StorageClusterFenceStatus{RequestID: operationID, HostID: hostID, TargetID: "system-1", Status: "passed", ExclusionVerified: true, ConfigGeneration: generation, ConfigDigest: digest, ObservedAt: time.Now().UTC()}
			return proof, nil
		})
		replica, err := loadPoolReplica(candidate)
		if err != nil || replica == nil || !replica.Verified {
			t.Fatalf("load candidate replica: %#v (%v)", replica, err)
		}
		operationID := automaticPoolPromotionOperationID(replica.Replica.PoolID, candidate.State.HostID, replica.Replica.Generation)
		record := &poolTransferRecord{
			OperationID: operationID, Action: "promote", Role: "candidate", TargetHostID: candidate.State.HostID,
			Stage: "preflight", Status: "pending", FenceAgreed: []string{master.State.HostID}, Automatic: true,
		}
		if err = savePoolTransfer(candidate, record); err != nil {
			t.Fatal(err)
		}
		if err = advancePoolPromotion(context.Background(), candidate, record, time.Second); err != nil || record.Stage != "fence" {
			t.Fatalf("preflight stage: stage=%s err=%v", record.Stage, err)
		}
		if err = savePoolTransfer(candidate, record); err != nil {
			t.Fatal(err)
		}
		if err = fencePoolExcludedHosts(context.Background(), candidate, record); err != nil || record.Stage != "activate" {
			t.Fatalf("fence stage: stage=%s err=%v", record.Stage, err)
		}
		if calls != 1 {
			t.Fatalf("fence calls before interruption=%d, want one", calls)
		}
		// The fence helper durably recorded its proof before the stage machine persisted
		// the in-memory transition to activate. Reopening here models that crash window.
		before, err := loadPoolTransfer(candidate)
		if err != nil || before == nil || before.Stage != "fence" || !before.Automatic || len(before.Fences) != 1 {
			t.Fatalf("interruption did not leave the durable proof at the fence stage: %#v (%v)", before, err)
		}
		_ = candidate.Close()
		reloaded, err := OpenStore(candidate.dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reloaded.Close() })
		for i, host := range lab.hosts {
			if host.State.HostID == reloaded.State.HostID {
				lab.hosts[i] = reloaded
			}
		}
		AdvancePoolAuthorityTransfer(context.Background(), reloaded, time.Second)
		after, err := loadPoolTransfer(reloaded)
		if err != nil || after == nil || after.OperationID != operationID || after.Stage != "done" {
			t.Fatalf("reloaded transfer did not resume the same operation: %#v (%v)", after, err)
		}
		if calls != 1 {
			t.Fatalf("resume fenced again: calls=%d", calls)
		}
		if peerState(t, reloaded).Membership.MasterHostID != reloaded.State.HostID {
			t.Fatal("reloaded candidate did not become pool master")
		}
	})

	t.Run("Host fencing off after persisted proof blocks automatic resume", func(t *testing.T) {
		lab, master, candidate := automaticBMCProfileLab(t, 1)
		lab.powerOff(master.State.HostID)
		calls := 0
		noRealPoolFence(t, lab, &calls)
		replica, err := loadPoolReplica(candidate)
		if err != nil || replica == nil || !replica.Verified {
			t.Fatalf("load candidate replica: %#v (%v)", replica, err)
		}
		record := &poolTransferRecord{
			OperationID: automaticPoolPromotionOperationID(replica.Replica.PoolID, candidate.State.HostID, replica.Replica.Generation),
			Action:      "promote", Role: "candidate", TargetHostID: candidate.State.HostID,
			Stage: "preflight", Status: "pending", FenceAgreed: []string{master.State.HostID}, Automatic: true,
		}
		if err = savePoolTransfer(candidate, record); err != nil {
			t.Fatal(err)
		}
		if err = advancePoolPromotion(context.Background(), candidate, record, time.Second); err != nil || record.Stage != "fence" {
			t.Fatalf("preflight stage: stage=%s err=%v", record.Stage, err)
		}
		if err = savePoolTransfer(candidate, record); err != nil {
			t.Fatal(err)
		}
		if err = fencePoolExcludedHosts(context.Background(), candidate, record); err != nil || calls != 1 {
			t.Fatalf("fence stage: calls=%d err=%v", calls, err)
		}

		if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
			t.Fatalf("turn Host fencing off on master: %#v", result)
		}
		if err = PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		_ = candidate.Close()
		reloaded, err := OpenStore(candidate.dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reloaded.Close() })
		for i, host := range lab.hosts {
			if host.State.HostID == reloaded.State.HostID {
				lab.hosts[i] = reloaded
			}
		}
		AdvancePoolAuthorityTransfer(context.Background(), reloaded, time.Second)
		after, err := loadPoolTransfer(reloaded)
		if err != nil || after == nil || after.Stage != "fence" || len(after.Fences) != 1 || calls != 1 {
			t.Fatalf("disabled fencing did not hold the durable proof: %#v calls=%d err=%v", after, calls, err)
		}
		if !strings.Contains(after.LastError, "Host fencing is off") {
			t.Fatalf("resume refusal did not explain the switch: %q", after.LastError)
		}

		// Exercise the activation guard directly as well, to cover a switch change after
		// the continuation check but immediately before the authority adoption write.
		after.Stage = "activate"
		if err = savePoolTransfer(reloaded, after); err != nil {
			t.Fatal(err)
		}
		if err = advancePoolPromotion(context.Background(), reloaded, after, time.Second); err == nil ||
			!strings.Contains(err.Error(), "Host fencing is off") || calls != 1 {
			t.Fatalf("activation passed with Host fencing off: err=%v calls=%d", err, calls)
		}
		if peerState(t, reloaded).Membership.MasterHostID == reloaded.State.HostID {
			t.Fatal("automatic promotion activated after Host fencing was disabled")
		}
	})
}

func TestAutomaticPoolPromotionRetryUsesStableOperationIdentity(t *testing.T) {
	first := automaticPoolPromotionOperationID("pool-1", "member-1", 12)
	if first == "" || first != automaticPoolPromotionOperationID("pool-1", "member-1", 12) || first == automaticPoolPromotionOperationID("pool-1", "member-1", 13) {
		t.Fatal("automatic promotion operation identity is not stable and generation-bound")
	}
}
