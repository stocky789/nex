// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func replicaFixture(t *testing.T) PoolAuthorityReplica {
	t.Helper()
	membership := PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 2, Members: []PoolMember{
		{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 2, Status: "ready"},
		{HostID: "member-1", Hostname: "member-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 2, Status: "ready"},
	}}
	replica := PoolAuthorityReplica{
		PoolID: "pool-1", MasterHostID: "master-1", Generation: 7, MemberHostID: "member-1",
		CreatedAt: time.Now().UTC(), Membership: membership, VMs: json.RawMessage(`{"vm-1":{"host_id":"member-1","epoch":3}}`),
		Storage: []StorageRegistration{}, Networks: []NetworkRegistration{}, Media: []Media{}, StorageClusters: []StorageClusterConfig{},
	}
	sections, err := replica.SectionDigests()
	if err != nil {
		t.Fatal(err)
	}
	replica.Sections = sections
	if err = replica.Valid(); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}
	return replica
}

// The content digest identifies the authority, not the envelope: the member it is
// addressed to, the wrap, the creation time, the signature and the generation are outside
// it, so one generation has one digest on every member and a re-push is recognizably the
// same copy. Anything that is actually replicated is inside it.
func TestPoolReplicaContentDigestIgnoresEnvelopeOnly(t *testing.T) {
	replica := replicaFixture(t)
	base, err := replica.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PoolAuthorityReplica){
		"member":    func(r *PoolAuthorityReplica) { r.MemberHostID = "member-2" },
		"created":   func(r *PoolAuthorityReplica) { r.CreatedAt = r.CreatedAt.Add(time.Hour) },
		"wrap":      func(r *PoolAuthorityReplica) { r.CAKeyWrapped = &PoolWrappedKey{Ciphertext: "x"} },
		"signature": func(r *PoolAuthorityReplica) { r.Signature = "another" },
		"generation": func(r *PoolAuthorityReplica) {
			r.Generation++
		},
	} {
		changed := replica
		mutate(&changed)
		digest, err := changed.ContentDigest()
		if err != nil {
			t.Fatal(err)
		}
		if digest != base {
			t.Fatalf("%s changed the content digest", name)
		}
	}
	for name, mutate := range map[string]func(*PoolAuthorityReplica){
		"pool":       func(r *PoolAuthorityReplica) { r.PoolID = "pool-2" },
		"master":     func(r *PoolAuthorityReplica) { r.MasterHostID = "master-2" },
		"membership": func(r *PoolAuthorityReplica) { r.Membership.Members = r.Membership.Members[:1] },
		"vms":        func(r *PoolAuthorityReplica) { r.VMs = json.RawMessage(`{}`) },
		"storage":    func(r *PoolAuthorityReplica) { r.Storage = []StorageRegistration{{ID: "repo-1"}} },
		"networks":   func(r *PoolAuthorityReplica) { r.Networks = []NetworkRegistration{{ID: "vlan-1"}} },
		"media":      func(r *PoolAuthorityReplica) { r.Media = []Media{{ID: "iso-1"}} },
		"clusters":   func(r *PoolAuthorityReplica) { r.StorageClusters = []StorageClusterConfig{{ClusterID: "c-1"}} },
	} {
		changed := replica
		mutate(&changed)
		digest, err := changed.ContentDigest()
		if err != nil {
			t.Fatal(err)
		}
		if digest == base {
			t.Fatalf("%s did not change the content digest", name)
		}
	}
}

// Section digests prove a replica is whole. A dropped, reordered or rewritten section is
// refused rather than read as a shorter authority.
func TestPoolReplicaSectionsMustBeComplete(t *testing.T) {
	replica := replicaFixture(t)
	if len(replica.Sections) != len(PoolReplicaSections) {
		t.Fatalf("sections = %#v", replica.Sections)
	}
	for i, name := range PoolReplicaSections {
		if replica.Sections[i].Name != name || len(replica.Sections[i].SHA256) != 64 {
			t.Fatalf("section %d = %#v", i, replica.Sections[i])
		}
	}
	short := replica
	short.Sections = replica.Sections[:len(replica.Sections)-1]
	if err := short.Valid(); err == nil || !strings.Contains(err.Error(), "does not declare every section") {
		t.Fatalf("short section list accepted: %v", err)
	}
	reordered := replica
	reordered.Sections = append([]PoolReplicaSection{replica.Sections[1]}, replica.Sections[1:]...)
	if err := reordered.Valid(); err == nil {
		t.Fatal("reordered section list accepted")
	}
	rewritten := replica
	rewritten.Sections = append([]PoolReplicaSection(nil), replica.Sections...)
	rewritten.Sections[2].SHA256 = strings.Repeat("0", 64)
	if err := rewritten.Valid(); err == nil || !strings.Contains(err.Error(), "section digest does not match") {
		t.Fatalf("rewritten section digest accepted: %v", err)
	}
	changed := replica
	changed.Media = []Media{{ID: "iso-1"}}
	if err := changed.Valid(); err == nil || !strings.Contains(err.Error(), "section digest does not match") {
		t.Fatalf("a replaced section with a stale digest was accepted: %v", err)
	}
}

func TestPoolAuthorityReplicaValid(t *testing.T) {
	for name, mutate := range map[string]func(*PoolAuthorityReplica){
		"no pool":         func(r *PoolAuthorityReplica) { r.PoolID = "" },
		"no master":       func(r *PoolAuthorityReplica) { r.MasterHostID = "" },
		"no member":       func(r *PoolAuthorityReplica) { r.MemberHostID = "" },
		"no generation":   func(r *PoolAuthorityReplica) { r.Generation = 0 },
		"other pool":      func(r *PoolAuthorityReplica) { r.Membership.PoolID = "pool-2" },
		"other master":    func(r *PoolAuthorityReplica) { r.Membership.MasterHostID = "master-2" },
		"no VM record":    func(r *PoolAuthorityReplica) { r.VMs = nil },
		"invalid VMs":     func(r *PoolAuthorityReplica) { r.VMs = json.RawMessage("{") },
		"long pool":       func(r *PoolAuthorityReplica) { r.PoolID = strings.Repeat("p", 129) },
		"bad membership":  func(r *PoolAuthorityReplica) { r.Membership.Members[0].Status = "ejected" },
		"no declarations": func(r *PoolAuthorityReplica) { r.Sections = nil },
	} {
		replica := replicaFixture(t)
		mutate(&replica)
		if err := replica.Valid(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// SigningBytes covers the per-member address and the wrap, so a replica sealed for one
// member cannot be re-addressed to another without invalidating the signature.
func TestPoolReplicaSigningBytesCoverTheEnvelope(t *testing.T) {
	replica := replicaFixture(t)
	replica.Signature = "ignored"
	base, err := replica.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(base), "ignored") {
		t.Fatal("signing bytes include the signature")
	}
	for _, mutate := range []func(*PoolAuthorityReplica){
		func(r *PoolAuthorityReplica) { r.MemberHostID = "member-2" },
		func(r *PoolAuthorityReplica) { r.Generation++ },
		func(r *PoolAuthorityReplica) { r.CAKeyWrapped = &PoolWrappedKey{Ciphertext: "x"} },
		func(r *PoolAuthorityReplica) { r.CreatedAt = r.CreatedAt.Add(time.Second) },
	} {
		changed := replica
		mutate(&changed)
		b, err := changed.SigningBytes()
		if err != nil {
			t.Fatal(err)
		}
		if string(b) == string(base) {
			t.Fatal("signing bytes did not change with the envelope")
		}
	}
}

func TestPoolReplicaRejectsDroppedControlReplayHistory(t *testing.T) {
	replica := replicaFixture(t)
	result := &PoolUpdateRollout{ID: "rollout-1", PoolID: replica.PoolID, Sequence: 2,
		RunningVMs: "refuse", State: PoolUpdatePaused, Phase: "hosts"}
	rollout := *result
	rollout.Controls = []PoolUpdateControlResult{{RequestID: "pause-1",
		Request: PoolUpdateRequest{RolloutID: rollout.ID, Control: "pause"}, Rollout: result}}
	replica.UpdateRollout = &rollout
	var err error
	if replica.Sections, err = replica.SectionDigests(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(replica)
	if err != nil {
		t.Fatal(err)
	}
	var adopted PoolAuthorityReplica
	if err = json.Unmarshal(raw, &adopted); err != nil || adopted.Valid() != nil {
		t.Fatalf("receipt-bearing replica was not preserved: %v", err)
	}
	adopted.UpdateRollout.Controls = nil // An older typed decoder discards the unknown field.
	if err = adopted.Valid(); err == nil {
		t.Fatal("a replica with silently dropped replay receipts was accepted")
	}
}

func TestPoolUpdateReplayRecordRejectsAmbiguousOrRecursiveResults(t *testing.T) {
	base := PoolUpdateRollout{ID: "rollout-1", PoolID: "pool-1", Sequence: 2,
		RunningVMs: "refuse", State: PoolUpdateRunning, Phase: "hosts"}
	pending := PoolUpdateControlResult{RequestID: "control-1",
		Request: PoolUpdateRequest{RolloutID: base.ID, Control: "abort"}}
	for name, controls := range map[string][]PoolUpdateControlResult{
		"duplicate_identity": {pending, pending},
		"two_pending":        {pending, {RequestID: "control-2", Request: pending.Request}},
		"other_rollout_pending": {{RequestID: pending.RequestID,
			Request: PoolUpdateRequest{RolloutID: "another-rollout", Control: "abort"}}},
		"result_and_error":     {{RequestID: pending.RequestID, Request: pending.Request, Rollout: &base, Code: "failed"}},
		"negative_reservation": {{RequestID: pending.RequestID, Request: pending.Request, ReservedResultBytes: -1}},
		"overflow_reservation": {{RequestID: pending.RequestID, Request: pending.Request, ReservedResultBytes: PoolReplicaLimit + 1}},
		"terminal_reservation": {{RequestID: pending.RequestID, Request: pending.Request, Rollout: &base, ReservedResultBytes: 1}},
		"ack_without_result":   {{RequestID: pending.RequestID, Request: pending.Request, ReplicationPending: true}},
		"nonrollback_started":  {{RequestID: pending.RequestID, Request: pending.Request, RollbackStarted: true}},
	} {
		t.Run(name, func(t *testing.T) {
			r := base
			r.Controls = controls
			if err := r.Valid(); err == nil {
				t.Fatal("ambiguous replay history accepted")
			}
		})
	}
	recursive := base
	recursive.Controls = []PoolUpdateControlResult{{RequestID: pending.RequestID, Request: pending.Request, Rollout: &recursive}}
	if err := recursive.Valid(); err == nil {
		t.Fatal("recursive replay snapshot accepted")
	}
}
