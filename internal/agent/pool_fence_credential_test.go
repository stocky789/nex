// SPDX-License-Identifier: AGPL-3.0-only
package agent

// The candidate is the host that fences, so it must hold the old master's fence credential
// before the old master is gone. These tests follow that material from the master's private
// store, through the per-member wrap inside the signed replica, to the fake fence on the
// candidate — which asserts it received the exact bytes.

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const labFenceIdentity = "-----BEGIN OPENSSH PRIVATE KEY-----\nlab-fence-identity-not-a-real-key\n-----END OPENSSH PRIVATE KEY-----\n"
const labFenceKnownHosts = "fence.example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILABFENCEHOSTKEY\n"

// writeLabFenceCredential puts the material where the existing credential handoff puts it,
// the way that handoff does: it overwrites, because delivering rotated material is exactly
// what it is for. (The promotion path deliberately refuses to overwrite; that is a different
// caller with a different rule.)
func writeLabFenceCredential(t *testing.T, s *Store, ref, identity string) {
	t.Helper()
	dir := filepath.Join(s.dir, "storage-cluster", "fence-credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ref), []byte(identity), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ref+".known_hosts"), []byte(labFenceKnownHosts), 0600); err != nil {
		t.Fatal(err)
	}
}

// recordLabFenceProfileWithoutMaterial puts a profile in the membership that this master
// holds no material for. It is the state a pool reaches when the credential never arrived,
// or was retired out from under the profile, and it is what the preflight must report.
func recordLabFenceProfileWithoutMaterial(t *testing.T, s *Store, hostID string, fence *protocol.StorageClusterFence) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID == hostID {
			state.Membership.Members[i].Fence = fence
		}
	}
	if err = savePoolPeer(s, state); err != nil {
		t.Fatal(err)
	}
}

func labCredentialRef() string { return labFenceProfile().CredentialRef }

// The whole round trip: the master wraps for exactly this member, the member stores it
// without opening it, and the candidate opens it at promotion time and fences with it.
func TestFenceCredentialReachesTheCandidateThroughTheReplica(t *testing.T) {
	lab := promotionLab(t, 2)
	master, candidate, bystander := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	pushReplicas(t, master)

	stored, err := loadPoolReplica(candidate)
	if err != nil || stored == nil {
		t.Fatalf("candidate replica: %v", err)
	}
	wrapped := poolFenceCredentialFor(stored.Replica, "master-1")
	if wrapped == nil || wrapped.CredentialRef != labCredentialRef() || wrapped.Wrapped == nil {
		t.Fatalf("the replica carries no fence credential for the old master: %#v", wrapped)
	}
	files, err := unwrapPoolFenceCredential(peerState(t, candidate).KeyPEM, stored.Replica, *wrapped)
	if err != nil {
		t.Fatalf("the candidate could not open the credential addressed to it: %v", err)
	}
	if string(files[""]) != labFenceIdentity || string(files[".known_hosts"]) != labFenceKnownHosts {
		t.Fatal("the unwrapped fence credential is not the master's material")
	}

	// A credential wrapped for one member cannot be opened by another, and not at another
	// generation either: the wrap is bound to both.
	other, err := loadPoolReplica(bystander)
	if err != nil || other == nil {
		t.Fatalf("bystander replica: %v", err)
	}
	if _, err = unwrapPoolFenceCredential(peerState(t, candidate).KeyPEM, other.Replica, *poolFenceCredentialFor(other.Replica, "master-1")); err == nil {
		t.Fatal("a member opened a fence credential addressed to another host")
	}
	shifted := stored.Replica
	shifted.Generation++
	if _, err = unwrapPoolFenceCredential(peerState(t, candidate).KeyPEM, shifted, *wrapped); err == nil {
		t.Fatal("a fence credential was opened at another generation")
	}
	tampered := *wrapped
	tampered.Wrapped = &protocol.PoolWrappedKey{EphemeralPublicKey: wrapped.Wrapped.EphemeralPublicKey, Nonce: wrapped.Wrapped.Nonce, Ciphertext: "AAAA" + wrapped.Wrapped.Ciphertext}
	if _, err = unwrapPoolFenceCredential(peerState(t, candidate).KeyPEM, stored.Replica, tampered); err == nil {
		t.Fatal("a tampered fence credential was opened")
	}
}

// A promotion fences with the material it unwrapped, not with something it invented.
func TestPromotionFencesWithTheUnwrappedCredential(t *testing.T) {
	for _, members := range []int{1, 2} {
		t.Run(map[int]string{1: "2-host pool", 2: "3-host pool"}[members], func(t *testing.T) {
			lab := promotionLab(t, members)
			master, candidate := lab.host("master-1"), lab.host("member-1")
			pushReplicas(t, master)
			lab.powerOff("master-1")
			seen := ""
			fakeFence(t, func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
				// The fence runs on the candidate, against the candidate's own state dir.
				identity, readErr := os.ReadFile(filepath.Join(candidate.dir, "storage-cluster", "fence-credentials", labCredentialRef()))
				if readErr != nil {
					t.Errorf("the fence ran without the credential materialized: %v", readErr)
				}
				seen = string(identity)
				return observedPowerOff(0)(hostID, operationID, digest, generation)
			})
			if result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-cred", "member-1")); result.Status != "succeeded" {
				t.Fatalf("promotion = %#v", result)
			}
			if seen != labFenceIdentity {
				t.Fatal("the fence did not receive the pool's own fence credential")
			}
			lab.requireSingleMaster("after a credential-backed promotion", "member-1")
			// The new master holds the profile and its material, so it can re-wrap for the
			// members that remain.
			// The fenced old master is unreachable, so this push reports it and carries on.
			_ = PushPoolAuthorityReplicas(context.Background(), candidate, 5*time.Second)
			if members == 2 {
				replica, err := loadPoolReplica(lab.host("member-2"))
				if err != nil || replica == nil || poolFenceCredentialFor(replica.Replica, "master-1") == nil {
					t.Fatal("the new master did not re-wrap the pool's fence credential for its remaining member")
				}
			}
		})
	}
}

// The candidate must learn it cannot fence before the operator confirms anything.
func TestPreflightReportsAMissingFenceCredential(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	// The profile is recorded but this pool never held the material for it, so the replica
	// it signs carries a profile and no credential.
	recordLabFenceProfileWithoutMaterial(t, master, "master-1", labFenceProfile())
	pushReplicas(t, master)
	stored, err := loadPoolReplica(candidate)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if poolFenceCredentialFor(stored.Replica, "master-1") != nil {
		t.Fatal("the master replicated a credential it does not hold")
	}
	lab.powerOff("master-1")
	verdict := promotionVerdict(t, candidate)
	check := verdictCheck(t, verdict, "old-master-exclusion")
	if check.Status != "blocked" || !strings.Contains(check.Reason, "no fence credential") {
		t.Fatalf("old-master-exclusion = %#v", check)
	}
	if verdict.CanPromote || verdict.ReadyToFence {
		t.Fatalf("a promotion was offered with no fence credential for the old master: %#v", verdict)
	}
	if !verdict.FenceRecorded || verdict.FenceUsable || !strings.Contains(verdict.FenceUnusableReason, "no fence credential") {
		t.Fatalf("the verdict does not report the missing credential structurally: %#v", verdict)
	}
}

// Recording the profile is where the material is bound: the master must hold it, it must fit,
// and a change to it must advance the authority generation so members re-wrap.
func TestFenceCredentialRecordingBindsAndVersionsTheMaterial(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	profile := labFenceProfile()

	// The master must actually hold the material it is about to promise its members.
	result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-missing", "master-1", "master-1", profile))
	if result.Status == "succeeded" || !strings.Contains(result.Error, "no fence credential") {
		t.Fatalf("recording a profile with no material = %#v", result)
	}

	writeLabFenceCredential(t, master, profile.CredentialRef, labFenceIdentity)
	if result = ExecutePoolFenceProfile(master, fenceProfileCommand("fp-1", "master-1", "master-1", profile)); result.Status != "succeeded" {
		t.Fatalf("recording = %#v", result)
	}
	first := poolMemberVersion(t, master, "master-1")
	if first == 0 {
		t.Fatal("recorded material carries no credential version")
	}
	// Re-recording the identical profile and material changes nothing, so it cannot churn
	// the authority generation.
	if result = ExecutePoolFenceProfile(master, fenceProfileCommand("fp-2", "master-1", "master-1", profile)); result.Status != "succeeded" {
		t.Fatalf("re-recording = %#v", result)
	}
	if again := poolMemberVersion(t, master, "master-1"); again != first {
		t.Fatalf("an identical re-record moved the credential version from %d to %d", first, again)
	}
	// Replacing the material does advance it, which is what makes the replica generation
	// move and every member re-wrap.
	writeLabFenceCredential(t, master, profile.CredentialRef, labFenceIdentity+"rotated\n")
	if result = ExecutePoolFenceProfile(master, fenceProfileCommand("fp-3", "master-1", "master-1", profile)); result.Status != "succeeded" {
		t.Fatalf("rotating = %#v", result)
	}
	if rotated := poolMemberVersion(t, master, "master-1"); rotated <= first {
		t.Fatalf("rotated material did not advance the credential version: %d then %d", first, rotated)
	}

	// Oversized material is refused at record time, so a replica can never grow through it.
	big := strings.Repeat("x", vmhost.PoolFenceCredentialLimit+1)
	writeLabFenceCredential(t, master, profile.CredentialRef, big)
	if result = ExecutePoolFenceProfile(master, fenceProfileCommand("fp-4", "master-1", "master-1", profile)); result.Status == "succeeded" {
		t.Fatal("oversized fence credential material was recorded")
	}
}

// Clearing a profile retires its material here and removes it from every member's replica.
func TestClearingAFenceProfileRetiresItsCredential(t *testing.T) {
	lab := promotionLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-clear", "master-1", "master-1", nil)); result.Status != "succeeded" {
		t.Fatalf("clearing = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(master.dir, "storage-cluster", "fence-credentials", labCredentialRef())); !os.IsNotExist(err) {
		t.Fatalf("the cleared fence credential was not retired on the master: %v", err)
	}
	pushReplicas(t, master)
	stored, err := loadPoolReplica(member)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if poolFenceCredentialFor(stored.Replica, "master-1") != nil {
		t.Fatal("a cleared fence credential is still in the member's replica")
	}
	if poolMemberVersion(t, master, "master-1") != 0 {
		t.Fatal("a cleared profile kept its credential version")
	}
}

// Nothing secret reaches inventory, a command result, an error string or the process log.
func TestFenceCredentialNeverLeavesCore(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	var logs bytes.Buffer
	restore := captureLog(t, &logs)
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-secret", "member-1"))
	restore()
	verdict := promotionVerdict(t, candidate)
	reported := []string{logs.String(), result.Error, verdict.ObservedAt.String()}
	for _, check := range verdict.Checks {
		reported = append(reported, check.Reason)
	}
	if status := PoolPromotionReport(candidate); status != nil {
		reported = append(reported, status.Error, status.Stage)
	}
	if status := PoolReplicaReport(candidate); status != nil {
		reported = append(reported, status.Digest, status.Error)
	}
	for _, text := range reported {
		for _, secret := range []string{"lab-fence-identity-not-a-real-key", "LABFENCEHOSTKEY", "BEGIN OPENSSH PRIVATE KEY"} {
			if strings.Contains(text, secret) {
				t.Fatalf("fence credential material reached %q", text)
			}
		}
	}
}

// poolMemberVersion reads the credential version the master recorded in its membership.
func poolMemberVersion(t *testing.T, s *Store, hostID string) uint64 {
	t.Helper()
	member, ok := poolMemberByID(peerState(t, s).Membership, hostID)
	if !ok {
		t.Fatalf("no such member: %s", hostID)
	}
	return member.FenceCredentialVersion
}

// captureLog redirects the process log for the duration of one step.
func captureLog(t *testing.T, into *bytes.Buffer) func() {
	t.Helper()
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(into)
	return func() { log.SetOutput(previousWriter); log.SetFlags(previousFlags) }
}

// After a promotion the new master owns the pool's fence profiles and their material, so it
// retires the material when it clears a profile — the same lifecycle the old master had. And
// a profile recorded for the NEW master is what makes it fence-promotable in turn, which the
// boot rule must keep reading from its own durable record.
func TestFenceCredentialLifecycleFollowsThePoolMaster(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	if result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-life", "member-1")); result.Status != "succeeded" {
		t.Fatalf("promotion = %#v", result)
	}
	credential := filepath.Join(candidate.dir, "storage-cluster", "fence-credentials", labCredentialRef())
	if _, err := os.Stat(credential); err != nil {
		t.Fatalf("the new master does not hold the material it fenced with: %v", err)
	}
	// It retires that material when it clears the profile it belongs to.
	if result := ExecutePoolFenceProfile(candidate, fenceProfileCommand("fp-drop", "member-1", "master-1", nil)); result.Status != "succeeded" {
		t.Fatalf("clearing on the new master = %#v", result)
	}
	if _, err := os.Stat(credential); !os.IsNotExist(err) {
		t.Fatalf("the new master kept a retired fence credential: %v", err)
	}
	// A profile for the new master itself is what would let anyone promote over it later,
	// and recording it is what makes its own boot rule restrict it.
	writeLabFenceCredential(t, candidate, "new-master-fence", labFenceIdentity)
	own := labFenceProfile()
	own.CredentialRef = "new-master-fence"
	if result := ExecutePoolFenceProfile(candidate, fenceProfileCommand("fp-own", "member-1", "member-1", own)); result.Status != "succeeded" {
		t.Fatalf("recording the new master's own profile = %#v", result)
	}
	if !poolSelfFencePromotable(peerState(t, candidate), "member-1") {
		t.Fatal("the new master does not consider itself fence-promotable after recording its own profile")
	}
}

// Replacing a profile with one naming other material retires only the material nothing names
// any more, keeps a moving version, and never disarms a clustered-storage fence that shares
// the same private reference.
func TestReplacingAFenceProfileRetiresOnlyWhatNothingNames(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	first := poolMemberVersion(t, master, "master-1")

	writeLabFenceCredential(t, master, "second-ref", labFenceIdentity)
	replacement := labFenceProfile()
	replacement.CredentialRef = "second-ref"
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-replace", "master-1", "master-1", replacement)); result.Status != "succeeded" {
		t.Fatalf("replacing = %#v", result)
	}
	if next := poolMemberVersion(t, master, "master-1"); next == 0 || next == first {
		t.Fatalf("replacing the material did not move the credential version: %d then %d", first, next)
	}
	dir := filepath.Join(master.dir, "storage-cluster", "fence-credentials")
	if _, err := os.Stat(filepath.Join(dir, labCredentialRef())); !os.IsNotExist(err) {
		t.Fatalf("the replaced material was not retired: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "second-ref")); err != nil {
		t.Fatalf("the new material was retired: %v", err)
	}

	// A second host's profile naming the same reference keeps it alive.
	writeLabFenceCredential(t, master, "shared-ref", labFenceIdentity)
	shared := labFenceProfile()
	shared.CredentialRef = "shared-ref"
	for _, host := range []string{"master-1", "member-1"} {
		if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-shared-"+host, "master-1", host, shared)); result.Status != "succeeded" {
			t.Fatalf("recording the shared profile on %s = %#v", host, result)
		}
	}
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-unshare", "master-1", "master-1", nil)); result.Status != "succeeded" {
		t.Fatalf("clearing one of two profiles = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "shared-ref")); err != nil {
		t.Fatalf("material another recorded profile still names was retired: %v", err)
	}
}
