// SPDX-License-Identifier: AGPL-3.0-only
package agent

// A master change whose target is destroyed between the hand-off and the re-point. The
// outgoing master has signed its authority away and refuses every authority write for good,
// which is what makes a second writer impossible — and, without a way out, what leaves a
// two-host pool with no master at all. The way out is the same evidence everything else
// here runs on: an observed power-off of the target.

import (
	"context"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// handedOffLab drives a master change until the source has durably handed its authority over
// and the target is unreachable. interrupt selects which interleaving: "transfer" lets the
// target handle the activation and loses the reply, "!transfer" refuses the request before the
// handler runs, so the target never activated at all. Both look identical to the source.
func handedOffLab(t *testing.T, interrupt string) (*transferLab, *Store, *Store) {
	t.Helper()
	lab := promotionLab(t, 1)
	master, target := lab.host("master-1"), lab.host("member-1")
	// The target has a fence profile of its own: without one, nothing here is possible.
	writeLabFenceCredential(t, master, "target-fence", labFenceIdentity)
	profile := labFenceProfile()
	profile.CredentialRef = "target-fence"
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-target", "master-1", "member-1", profile)); result.Status != "succeeded" {
		t.Fatalf("recording the target's fence profile = %#v", result)
	}
	pushReplicas(t, master)
	lab.drop("member-1", interrupt)
	ExecutePoolTransfer(context.Background(), master, transferCommand("op-lost", "master-change", "member-1", "master-1"))
	record, err := loadPoolTransfer(master)
	if err != nil || record == nil || !record.HandedOff {
		t.Fatalf("the source did not hand its authority over: %#v (%v)", record, err)
	}
	// The target is destroyed: it answers nothing, ever again.
	lab.powerOff("member-1")
	return lab, master, target
}

func cancelCommand(operationID, master, target string, fenceHosts []string) protocol.Command {
	command := transferCommand(operationID, "transfer-cancel", target, master)
	command.Pool.FenceConfirmed = len(fenceHosts) != 0
	command.Pool.FenceHostIDs = fenceHosts
	return command
}

// Without a fence proof of the target, the source stays handed-off: an unanswered RPC is not
// evidence that the target never activated.
func TestHandedOffMasterWithoutAProofStaysHandedOff(t *testing.T) {
	lab, master, _ := handedOffLab(t, "transfer")
	result := ExecutePoolTransfer(context.Background(), master, cancelCommand("op-lost", "master-1", "member-1", nil))
	if result.Status == "succeeded" {
		t.Fatalf("a cancel succeeded with no evidence about the target: %#v", result)
	}
	if err := requirePoolAuthorityWritable(master); err == nil {
		t.Fatal("the source resumed writing authority with no evidence")
	}
	lab.requireNoWriter("a handed-off source with no evidence")
}

// With an observed power-off of the target, the source resumes — above anything the target
// could have minted, whether or not it activated.
func TestHandedOffMasterResumesOnAFenceProofOfTheTarget(t *testing.T) {
	for _, interrupt := range []string{"transfer", "!transfer"} {
		name := "target activated then died"
		if strings.HasPrefix(interrupt, "!") {
			name = "target never activated"
		}
		t.Run(name, func(t *testing.T) {
			lab, master, target := handedOffLab(t, interrupt)
			activated := peerState(t, target).Membership.MasterHostID == "member-1"
			if strings.HasPrefix(interrupt, "!") == activated {
				t.Fatalf("the interleaving did not produce the intended case: activated=%v", activated)
			}
			fakeFence(t, observedPowerOff(0))
			result := ExecutePoolTransfer(context.Background(), master, cancelCommand("op-lost", "master-1", "member-1", []string{"member-1"}))
			if result.Status != "succeeded" || !strings.Contains(result.Error, "resumed") {
				t.Fatalf("the fence-backed cancel = %#v", result)
			}
			lab.requireSingleMaster("after the fence-backed resume", "master-1")

			// Whatever the target minted is superseded: it comes back, finds a higher
			// authority naming another master, and takes the superseded path.
			lab.powerOn("member-1")
			if err := EnsurePoolBootRestriction(target); err != nil {
				t.Fatal(err)
			}
			lab.requireSingleMaster("the moment the target returns", "master-1")
			ReconcilePoolRestriction(context.Background(), target, 5*time.Second)
			lab.requireSingleMaster("after the target reconciles", "master-1")
			if peerState(t, target).Membership.MasterHostID != "master-1" {
				t.Fatalf("the returning target follows %q", peerState(t, target).Membership.MasterHostID)
			}
			if activated && requirePoolStartPath(target) == nil {
				// A target that took the authority and then died must come back
				// restricted; one that never took it was only ever a member.
				t.Fatal("the returning target started guests before reconciling")
			}
			// And the resumed master is a working master again.
			if err := requirePoolAuthorityWritable(master); err != nil {
				t.Fatalf("the resumed master still refuses authority writes: %v", err)
			}
			if err := PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err != nil {
				t.Fatalf("the resumed master could not replicate: %v", err)
			}
		})
	}
}

// The escape is exactly as bound as every other fence here.
func TestFenceBackedCancelRefusesAnythingButAProofOfTheTarget(t *testing.T) {
	cases := []struct {
		name   string
		hosts  []string
		answer func(string, string, string, uint64) (protocol.StorageClusterFenceStatus, error)
	}{
		{"a proof of another host", []string{"member-1"}, func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			return observedPowerOff(0)("master-1", operationID, digest, generation)
		}},
		{"a stale proof", []string{"member-1"}, observedPowerOff(protocol.PoolPromotionFenceWindow + time.Minute)},
		{"a fence that only acknowledged the request", []string{"member-1"}, func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			proof, _ := observedPowerOff(0)(hostID, operationID, digest, generation)
			proof.Status, proof.ExclusionVerified = "uncertain", false
			return proof, nil
		}},
		{"a host list naming somebody else", []string{"master-1"}, observedPowerOff(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lab, master, _ := handedOffLab(t, "transfer")
			fakeFence(t, tc.answer)
			result := ExecutePoolTransfer(context.Background(), master, cancelCommand("op-lost", "master-1", "member-1", tc.hosts))
			if result.Status == "succeeded" {
				t.Fatalf("the cancel accepted %s: %#v", tc.name, result)
			}
			if err := requirePoolAuthorityWritable(master); err == nil {
				t.Fatalf("the source resumed on %s", tc.name)
			}
			lab.requireNoWriter("after " + tc.name)
		})
	}
}
