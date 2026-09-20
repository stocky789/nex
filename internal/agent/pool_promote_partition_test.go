// SPDX-License-Identifier: AGPL-3.0-only
package agent

// The partition case the promotion proof exists for: two survivors that cannot see each
// other, each able to reach the Controller, each asked to take over the same dead master.
// Exactly one of them may ever become master, and only on evidence that the other is
// excluded — never on the other's silence.

import (
	"context"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// partitionLab is a three-host pool whose master is dead and whose two survivors cannot
// reach each other. Every host has a recorded fence profile, so either survivor could in
// principle fence the other.
func partitionLab(t *testing.T) (*transferLab, *Store, *Store) {
	t.Helper()
	lab := promotionLab(t, 2)
	master := lab.host("master-1")
	for _, host := range []string{"member-1", "member-2"} {
		writeLabFenceCredential(t, master, host+"-fence", labFenceIdentity)
		profile := labFenceProfile()
		profile.CredentialRef = host + "-fence"
		if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-"+host, "master-1", host, profile)); result.Status != "succeeded" {
			t.Fatalf("recording %s's fence profile = %#v", host, result)
		}
	}
	pushReplicas(t, master)
	lab.powerOff("master-1")
	// The two survivors are cut off from each other but not from the Controller.
	lab.drop("member-1", "!*")
	lab.drop("member-2", "!*")
	return lab, lab.host("member-1"), lab.host("member-2")
}

// The reviewer's scenario: without fencing the other survivor, both promotions must be
// refused. Silence is never agreement.
func TestPartitionedSurvivorsAreNotBothPromoted(t *testing.T) {
	lab, first, second := partitionLab(t)
	fakeFence(t, observedPowerOff(0))
	firstResult := ExecutePoolPromotion(context.Background(), first, promoteCommand("op-first", "member-1"))
	lab.requireNoWriter("after the first promotion attempt")
	secondResult := ExecutePoolPromotion(context.Background(), second, promoteCommand("op-second", "member-2"))
	if masters := lab.masters(); len(masters) > 1 {
		t.Fatalf("DUAL MASTER: %v (first=%#v second=%#v)", masters, firstResult, secondResult)
	}
	if firstResult.Status == "succeeded" || secondResult.Status == "succeeded" {
		t.Fatalf("a promotion succeeded over an unreachable survivor: first=%#v second=%#v", firstResult, secondResult)
	}
	for _, result := range []protocol.Result{firstResult, secondResult} {
		if !strings.Contains(result.Error, "member-") {
			t.Fatalf("the refusal does not name the host that blocks it: %#v", result)
		}
	}
	lab.requireNoWriter("after both promotion attempts")
}

// The preflight says structurally which hosts block and whether each could be fenced, so an
// operator sees the whole destructive decision before confirming any of it.
func TestPreflightNamesEveryHostThatMustBeFenced(t *testing.T) {
	_, candidate, _ := partitionLab(t)
	verdict := promotionVerdict(t, candidate)
	if verdict.CanPromote || verdict.ReadyToFence == false && len(verdict.FenceHostIDs) == 0 {
		t.Fatalf("verdict = %#v", verdict)
	}
	if len(verdict.FenceHostIDs) != 2 {
		t.Fatalf("fence_host_ids = %v, want the old master and the unreachable survivor", verdict.FenceHostIDs)
	}
	blocking := map[string]protocol.PoolPromotionHost{}
	for _, host := range verdict.Hosts {
		blocking[host.HostID] = host
	}
	other := blocking["member-2"]
	if !other.Blocks || !other.FenceRecorded || !other.FenceUsable {
		t.Fatalf("the unreachable survivor's row = %#v", other)
	}
	if old := blocking["master-1"]; !old.Blocks || !old.FenceUsable {
		t.Fatalf("the old master's row = %#v", old)
	}
}

// Fencing both excluded hosts is what makes exactly one survivor promotable.
func TestPromotionSucceedsWhenEveryBlockingHostIsFenced(t *testing.T) {
	lab, candidate, _ := partitionLab(t)
	fakeFence(t, observedPowerOff(0))
	command := promoteCommand("op-both", "member-1")
	command.Pool.FenceHostIDs = []string{"master-1", "member-2"}
	if result := ExecutePoolPromotion(context.Background(), candidate, command); result.Status != "succeeded" {
		t.Fatalf("promotion = %#v", result)
	}
	lab.powerOff("member-2") // The fence this promotion performed.
	lab.requireSingleMaster("after fencing both excluded hosts", "member-1")
	// The fenced survivor's guests are classified like the fenced master's: visible, and
	// not startable here.
	verdict := PoolPromotionPreflight(context.Background(), candidate, "", 5*time.Second)
	if verdict.OldMasterHostID == "" {
		t.Fatalf("verdict after promotion = %#v", verdict)
	}
}

// The operator must have seen every host they are powering off named.
func TestPromotionRefusesAFenceListThatDoesNotMatch(t *testing.T) {
	lab, candidate, _ := partitionLab(t)
	fakeFence(t, func(string, string, string, uint64) (protocol.StorageClusterFenceStatus, error) {
		t.Fatal("a fence ran for a host list the operator did not agree to")
		return protocol.StorageClusterFenceStatus{}, nil
	})
	for _, list := range [][]string{{"master-1"}, {"master-1", "member-2", "member-9"}, {"member-2"}, nil} {
		command := promoteCommand("op-list", "member-1")
		command.Pool.FenceHostIDs = list
		result := ExecutePoolPromotion(context.Background(), candidate, command)
		if result.Status == "succeeded" {
			t.Fatalf("a promotion ran with the host list %v", list)
		}
		if !strings.Contains(result.Error, "power off") && !strings.Contains(result.Error, "fence") {
			t.Fatalf("refusal for %v = %q", list, result.Error)
		}
	}
	lab.requireNoWriter("after every mismatched host list")
}

// A proof for another host, another operation or an expired observation is not proof here
// either: the second fence is bound exactly as the first is.
func TestPromotionRefusesAnUnboundProofForTheSecondHost(t *testing.T) {
	cases := []struct {
		name   string
		answer func(string, string, string, uint64) (protocol.StorageClusterFenceStatus, error)
	}{
		{"stale for the survivor", func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			age := time.Duration(0)
			if hostID == "member-2" {
				age = protocol.PoolPromotionFenceWindow + time.Minute
			}
			return observedPowerOff(age)(hostID, operationID, digest, generation)
		}},
		{"bound to another operation", func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			if hostID == "member-2" {
				operationID = "another-operation"
			}
			return observedPowerOff(0)(hostID, operationID, digest, generation)
		}},
		{"bound to another host", func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			if hostID == "member-2" {
				hostID = "master-1"
			}
			return observedPowerOff(0)(hostID, operationID, digest, generation)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lab, candidate, _ := partitionLab(t)
			fakeFence(t, tc.answer)
			command := promoteCommand("op-bound", "member-1")
			command.Pool.FenceHostIDs = []string{"master-1", "member-2"}
			if result := ExecutePoolPromotion(context.Background(), candidate, command); result.Status == "succeeded" {
				t.Fatalf("a promotion accepted an unbound second proof: %#v", result)
			}
			lab.requireNoWriter("after an unbound second proof")
		})
	}
}

// Two candidates that CAN see each other: the one that started second finds the first's
// promotion record and refuses, so exactly one proceeds.
func TestConcurrentReachableCandidatesLeaveOneMaster(t *testing.T) {
	lab := promotionLab(t, 2)
	master := lab.host("master-1")
	for _, host := range []string{"member-1", "member-2"} {
		writeLabFenceCredential(t, master, host+"-fence", labFenceIdentity)
		profile := labFenceProfile()
		profile.CredentialRef = host + "-fence"
		if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-"+host, "master-1", host, profile)); result.Status != "succeeded" {
			t.Fatalf("recording %s's fence profile = %#v", host, result)
		}
	}
	pushReplicas(t, master)
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))

	first := ExecutePoolPromotion(context.Background(), lab.host("member-1"), promoteCommand("op-a", "member-1"))
	if first.Status != "succeeded" {
		t.Fatalf("the first promotion = %#v", first)
	}
	second := ExecutePoolPromotion(context.Background(), lab.host("member-2"), promoteCommand("op-b", "member-2"))
	if second.Status == "succeeded" {
		t.Fatalf("a second candidate promoted itself over a reachable master: %#v", second)
	}
	lab.requireSingleMaster("after two concurrent reachable candidates", "member-1")
}

// A promotion never fences a master that is reachable and healthy: the refusal is a decision
// at the preflight, before anything destructive can run.
func TestPromotionNeverFencesAReachableHealthyMaster(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	fakeFence(t, func(string, string, string, uint64) (protocol.StorageClusterFenceStatus, error) {
		t.Fatal("a promotion fenced a reachable, healthy pool master")
		return protocol.StorageClusterFenceStatus{}, nil
	})
	result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-healthy", "member-1"))
	if result.Status == "succeeded" || !strings.Contains(result.Error, "planned master change") {
		t.Fatalf("promotion against a healthy master = %#v", result)
	}
	lab.requireSingleMaster("after refusing to promote over a healthy master", "master-1")
}
