// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fenceProof(now time.Time) *StorageClusterFenceStatus {
	proof := promotionProof(now)
	return &proof
}

func promotionProof(now time.Time) StorageClusterFenceStatus {
	return StorageClusterFenceStatus{
		RequestID: "op-1", HostID: "master-1", Status: "passed", TargetID: "target-1",
		ExclusionVerified: true, ObservedAt: now.Add(-time.Minute),
		ConfigGeneration: 7, ConfigDigest: PoolPromotionFenceDigest("pool-1", "master-1", "op-1", 7),
	}
}

func TestPoolPromotionFenceProofFreshBoundary(t *testing.T) {
	now := time.Now().UTC()
	atLimit := now.Add(-PoolPromotionFenceWindow)
	if !PoolPromotionFenceProofFresh(atLimit, now) {
		t.Fatal("a proof observed exactly at the window limit should still be fresh")
	}
	if PoolPromotionFenceProofFresh(atLimit.Add(-time.Nanosecond), now) {
		t.Fatal("a proof one nanosecond past the window should be stale")
	}
}

// Only an observed power-off, bound to exactly this promotion and observed inside the
// freshness window, is proof. Every other shape is refused here, once, for every caller.
func TestPoolPromotionFenceVerified(t *testing.T) {
	now := time.Now().UTC()
	fence := &StorageClusterFence{Kind: FenceVirsh, TargetID: "target-1", Endpoint: "fence.example.test", CredentialRef: "cred-1", VirshURI: "qemu:///system", SSHLogin: "fence", SSHPort: 22, SSHHostKeyRef: "key-1"}
	member := PoolMember{HostID: "master-1", Fence: fence}
	if err := PoolPromotionFenceVerified(fenceProof(now), member, "pool-1", "master-1", "op-1", 7, now); err != nil {
		t.Fatalf("a bound, observed, fresh proof was refused: %v", err)
	}
	cases := []struct {
		name   string
		break_ func(*StorageClusterFenceStatus)
	}{
		{"no proof at all", nil},
		{"a fence request that merely returned success", func(p *StorageClusterFenceStatus) { p.ExclusionVerified = false }},
		{"an uncertain exclusion", func(p *StorageClusterFenceStatus) { p.Status = "uncertain" }},
		{"another host", func(p *StorageClusterFenceStatus) { p.HostID = "member-2" }},
		{"another fence target", func(p *StorageClusterFenceStatus) { p.TargetID = "target-2" }},
		{"another operation", func(p *StorageClusterFenceStatus) { p.RequestID = "op-2" }},
		{"another generation", func(p *StorageClusterFenceStatus) { p.ConfigGeneration = 8 }},
		{"another promotion's digest", func(p *StorageClusterFenceStatus) {
			p.ConfigDigest = PoolPromotionFenceDigest("pool-1", "master-1", "op-2", 7)
		}},
		{"an observation older than the window", func(p *StorageClusterFenceStatus) {
			p.ObservedAt = now.Add(-PoolPromotionFenceWindow - time.Minute)
		}},
		{"no observation at all", func(p *StorageClusterFenceStatus) { p.ObservedAt = time.Time{} }},
		{"an observation from the future", func(p *StorageClusterFenceStatus) { p.ObservedAt = now.Add(time.Hour) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var proof *StorageClusterFenceStatus
			if tc.break_ != nil {
				value := promotionProof(now)
				tc.break_(&value)
				proof = &value
			}
			if err := PoolPromotionFenceVerified(proof, member, "pool-1", "master-1", "op-1", 7, now); err == nil {
				t.Fatal("this was accepted as a fence proof")
			}
		})
	}
	// A host with no usable fence profile can never be excluded, whatever the proof says.
	if err := PoolPromotionFenceVerified(fenceProof(now), PoolMember{HostID: "master-1"}, "pool-1", "master-1", "op-1", 7, now); err == nil {
		t.Fatal("a proof was accepted for a host with no fence profile")
	}
}

func TestPoolCommandValidatesPromotionAndFenceProfile(t *testing.T) {
	fence := &StorageClusterFence{Kind: FenceVirsh, TargetID: "target-1", Endpoint: "fence.example.test", CredentialRef: "cred-1", VirshURI: "qemu:///system", SSHLogin: "fence", SSHPort: 22, SSHHostKeyRef: "key-1"}
	valid := []PoolCommand{
		{RequestID: "r1", Action: "promote", MemberHostID: "member-1", FenceConfirmed: true},
		{RequestID: "r2", Action: "fence-profile", MemberHostID: "master-1", Membership: PoolMembership{MasterHostID: "master-1"}, Fence: fence},
		{RequestID: "r3", Action: "fence-profile", MemberHostID: "master-1", Membership: PoolMembership{MasterHostID: "master-1"}},
	}
	for _, command := range valid {
		if err := command.Valid(); err != nil {
			t.Fatalf("%s was refused: %v", command.Action, err)
		}
	}
	invalid := []PoolCommand{
		{RequestID: "r4", Action: "promote"},
		{RequestID: "r5", Action: "fence-profile", MemberHostID: "master-1"},
		{RequestID: "r6", Action: "fence-profile", MemberHostID: "master-1", Membership: PoolMembership{MasterHostID: "master-1"}, Fence: &StorageClusterFence{Kind: "telnet"}},
	}
	for _, command := range invalid {
		if err := command.Valid(); err == nil {
			t.Fatalf("an invalid %s command was accepted: %#v", command.Action, command)
		}
	}
}

// The fields a caller reads structurally instead of matching on Core's sentences, and the
// per-member fence credential material's place in the replica: covered by the signature,
// excluded from the content digest, so one generation has one digest for every member.
func TestPromotionAndRestrictionWireFields(t *testing.T) {
	verdict := PoolPromotionVerdict{
		PoolID: "pool-1", CandidateHostID: "member-1", OldMasterHostID: "master-1",
		CanPromote: false, ReadyToFence: true, FenceRequired: true,
		FenceRecorded: true, FenceUsable: true,
	}
	b, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"can_promote":false`, `"ready_to_fence":true`, `"fence_recorded":true`, `"fence_usable":true`} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("verdict JSON lacks %s: %s", field, b)
		}
	}
	if strings.Contains(string(b), "fence_unusable_reason") {
		t.Fatalf("an empty reason was encoded: %s", b)
	}
	restricted := PoolRestrictedStatus{Kind: "superseded", Reason: "…"}
	if b, err = json.Marshal(restricted); err != nil || !strings.Contains(string(b), `"kind":"superseded"`) {
		t.Fatalf("restricted JSON = %s (%v)", b, err)
	}
}

func TestReplicaContentDigestIgnoresPerMemberSecrets(t *testing.T) {
	replica := PoolAuthorityReplica{
		PoolID: "pool-1", MasterHostID: "master-1", Generation: 4, MemberHostID: "member-1",
		Membership: PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 2},
		VMs:        json.RawMessage(`{}`),
	}
	base, err := replica.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	// The pool CA key and the fence credentials are sealed per member, so two members of one
	// generation must still report the same digest.
	addressed := replica
	addressed.MemberHostID = "member-2"
	addressed.CAKeyWrapped = &PoolWrappedKey{Ciphertext: "aaaa"}
	addressed.FenceCredentials = []PoolWrappedFenceCredential{{HostID: "master-1", CredentialRef: "ref-1", Version: 3, Wrapped: &PoolWrappedKey{Ciphertext: "bbbb"}}}
	other, err := addressed.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	if other != base {
		t.Fatal("per-member sealed material changed the replica's content digest")
	}
	// The version that does move the digest lives in the membership, which is what makes
	// rotated material advance the authority generation.
	rotated := replica
	rotated.Membership.Members = []PoolMember{{HostID: "master-1", FenceCredentialVersion: 2}}
	moved, err := rotated.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	if moved == base {
		t.Fatal("a credential version change did not move the replica's content digest")
	}
}
