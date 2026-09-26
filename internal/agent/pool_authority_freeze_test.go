// SPDX-License-Identifier: AGPL-3.0-only
package agent

// The freeze exists so that one authority generation names exactly one content. This is the
// table that proves it: it freezes the master, drives every entry point that can write a
// replicated section, and asserts the replica's content digest is byte-identical afterwards.
// A mutation that slips past the guard shows up here as a changed digest at a pinned
// generation, which is the drift that makes a hand-off carry content nobody acknowledged.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func frozenAuthorityDigest(t *testing.T, s *Store) string {
	t.Helper()
	_, digest, err := buildPoolAuthorityReplica(s)
	if err != nil {
		t.Fatalf("building the replica: %v", err)
	}
	return digest
}

func TestAuthorityFreezeDetectsVMChangeAfterReplicaBuild(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec("44444444-4444-4444-8444-444444444444", "master-1")
	state := poolVMState{VMs: map[string]poolVMRecord{
		spec.ID: {PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID, Epoch: 1, State: "stopped", Spec: spec},
	}}
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	builtVMs, err := poolReplicaVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	state.VMs[spec.ID] = poolVMRecord{PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID, Epoch: 2, State: "stopped", Spec: spec}
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	matches, err := poolAuthorityVMSectionMatches(builtVMs, state.VMs)
	if err != nil || matches {
		t.Fatalf("freeze accepted a VM section changed after replica build: matches=%v err=%v", matches, err)
	}
}

// freezeLabAuthority drives the transfer machine far enough to persist the freeze, with the
// target unreachable so nothing else moves.
func freezeLabAuthority(t *testing.T, lab *transferLab, operation string) *Store {
	t.Helper()
	master := lab.host("master-1")
	lab.drop("member-1", "!*")
	result := ExecutePoolTransfer(context.Background(), master, transferCommand(operation, "master-change", "member-1", "master-1"))
	if result.Status == "succeeded" {
		t.Fatalf("the transfer completed with an unreachable target: %#v", result)
	}
	record, err := loadPoolTransfer(master)
	if err != nil || record == nil || !record.Frozen {
		t.Fatalf("authority was not frozen: %#v (%v)", record, err)
	}
	return master
}

// Every write to every replicated section, while the authority is frozen.
func TestFrozenAuthorityContentDigestNeverMoves(t *testing.T) {
	vmID := "44444444-4444-4444-8444-444444444444"
	other := "55555555-5555-4555-8555-555555555555"
	cases := []struct {
		section string
		mutate  func(t *testing.T, lab *transferLab, master *Store)
	}{
		{"vms/placement", func(t *testing.T, lab *transferLab, master *Store) {
			_ = poolVMLocal(master, vmID, "define", nil, true, poolVMTestSpec(vmID, "master-1"), nil)
		}},
		{"vms/start", func(t *testing.T, lab *transferLab, master *Store) {
			permit := ""
			_ = poolVMLocal(master, vmID, "start", &permit, false, nil, nil)
		}},
		{"vms/peer-inventory", func(t *testing.T, lab *transferLab, master *Store) {
			// The member's routine inventory publish, which seeds and prunes owner records.
			membership, err := Membership(master)
			if err != nil {
				t.Fatal(err)
			}
			spec := *poolVMTestSpec(other, "member-1")
			spec.PoolID = "pool-1"
			_ = recordPoolVMInventory(master, membership, poolVMInventory{
				HostID: "member-1", Generation: membership.Generation, VMs: []protocol.VMCreateSpec{spec},
				Storage: map[string]protocol.StorageIdentity{}, Observed: map[string]string{other: "shut off"},
				ObservedAt: time.Now().UTC(),
			})
		}},
		{"vms/peer-prune", func(t *testing.T, lab *transferLab, master *Store) {
			// A member reporting its own delete tombstone prunes the owner record, which is
			// a write to the replicated vms section even when nothing else seeds.
			membership, err := Membership(master)
			if err != nil {
				t.Fatal(err)
			}
			_ = recordPoolVMInventory(master, membership, poolVMInventory{
				HostID: "member-1", Generation: membership.Generation, VMs: []protocol.VMCreateSpec{},
				Storage: map[string]protocol.StorageIdentity{}, Observed: map[string]string{},
				Deleted: []string{other}, ObservedAt: time.Now().UTC(),
			})
		}},
		{"vms/own-inventory", func(t *testing.T, lab *transferLab, master *Store) {
			_ = PublishPoolVMInventory(context.Background(), master)
		}},
		{"membership/maintenance", func(t *testing.T, lab *transferLab, master *Store) {
			_ = ExecutePoolMaintenance(master, protocol.Command{OperationID: "op-drain", Kind: "pool-maintenance", Pool: &protocol.PoolCommand{
				RequestID: "r-drain", Action: "maintenance", MemberHostID: "member-1", Enabled: true,
				Membership: protocol.PoolMembership{MasterHostID: "master-1"},
			}})
		}},
		{"membership/fence-profile", func(t *testing.T, lab *transferLab, master *Store) {
			writeLabFenceCredential(t, master, "freeze-ref", labFenceIdentity)
			profile := labFenceProfile()
			profile.CredentialRef = "freeze-ref"
			_ = ExecutePoolFenceProfile(master, fenceProfileCommand("op-fence", "master-1", "member-1", profile))
		}},
		{"membership/join", func(t *testing.T, lab *transferLab, master *Store) {
			_, _ = CreatePoolJoinInvitation(master, lab.origin["master-1"], "member-9")
		}},
		{"storage_clusters/apply", func(t *testing.T, lab *transferLab, master *Store) {
			// The Controller-driven clustered-storage apply writes the replicated
			// storage_clusters section, so it takes the same guard as every other
			// registry write, and says so in the retryable frozen sentence.
			result := ExecuteStorageClusterRequest(context.Background(), master, protocol.StorageClusterRequest{
				RequestID: "cluster-freeze", PoolID: master.State.PoolID, HostID: master.State.HostID, Action: "apply",
				Config: protocol.StorageClusterConfig{RepositoryID: "repo-1"},
			})
			if result.Status != "failed" || !strings.Contains(result.Error, poolAuthorityFrozenRefusal) {
				t.Fatalf("a frozen master answered a clustered-storage apply with %+v", result)
			}
			fence := ExecuteStorageClusterRequest(context.Background(), master, protocol.StorageClusterRequest{
				RequestID: "cluster-freeze-fence", PoolID: master.State.PoolID, HostID: master.State.HostID, Action: "verify_fence",
				FenceHostID: "member-1", FenceConfirmation: "FENCE member-1", Config: protocol.StorageClusterConfig{RepositoryID: "repo-1"},
			})
			if fence.Status != "failed" || !strings.Contains(fence.Error, poolAuthorityFrozenRefusal) {
				t.Fatalf("a frozen master answered a clustered-storage fence request with %+v", fence)
			}
			// A read is not a write and stays available while the authority is frozen.
			if status := ExecuteStorageClusterRequest(context.Background(), master, protocol.StorageClusterRequest{
				RequestID: "cluster-freeze-status", PoolID: master.State.PoolID, HostID: master.State.HostID, Action: "status",
			}); status.Status != "accepted" {
				t.Fatalf("a frozen master refused a clustered-storage status read: %+v", status)
			}
		}},
		{"storage+networks+media/registry", func(t *testing.T, lab *transferLab, master *Store) {
			for _, kind := range []string{"storage-register", "network-register", "media-remove", "iso-import", "storage-detach"} {
				if err := poolRegistryCommandWritable(master, protocol.Command{Kind: kind}); err == nil {
					t.Fatalf("the managed worker would have performed %q during a freeze", kind)
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.section, func(t *testing.T) {
			lab := newTransferLab(t, 1)
			master := lab.host("master-1")
			writePoolVMRecord(t, master, labVM("pool-1", vmID, "master-1"))
			writePoolVMRecord(t, master, labVM("pool-1", other, "member-1"))
			writeAppliedPoolStorage(t, master, labSharedStorage(t))
			freezeLabAuthority(t, lab, "op-freeze")
			before := frozenAuthorityDigest(t, master)
			tc.mutate(t, lab, master)
			if after := frozenAuthorityDigest(t, master); after != before {
				t.Fatalf("a frozen authority's content changed: %s then %s", before, after)
			}
		})
	}
}

// A prune or seed a freeze refused is deferred, not dropped: the inventory snapshot is still
// durable, and the owner records settle from it once the transfer is over.
func TestOwnerRecordsSettleAfterTheFreezeEnds(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	spec := *poolVMTestSpec("66666666-6666-4666-8666-666666666666", "member-1")
	spec.PoolID = "pool-1"
	inventory := poolVMInventory{
		HostID: "member-1", Generation: membership.Generation, VMs: []protocol.VMCreateSpec{spec},
		Storage: map[string]protocol.StorageIdentity{}, Observed: map[string]string{spec.ID: "shut off"},
		ObservedAt: time.Now().UTC(),
	}
	freezeLabAuthority(t, lab, "op-defer")
	if err = recordPoolVMInventory(master, membership, inventory); !errors.Is(err, errPoolVMOwnersDeferred) {
		t.Fatalf("a frozen authority answered the publication with %v", err)
	}
	if _, ok := poolVMRecords(t, master)[spec.ID]; ok {
		t.Fatal("a frozen authority seeded an owner record")
	}
	// The snapshot itself is durable, so nothing is lost by the refusal.
	poolVMMu.Lock()
	state, loadErr := loadPoolVMs(master)
	poolVMMu.Unlock()
	if loadErr != nil || state.Inventory["member-1"].Generation != membership.Generation {
		t.Fatalf("the inventory snapshot was refused as well: %#v (%v)", state.Inventory, loadErr)
	}
	// End the transfer; the same publication now settles.
	record, err := loadPoolTransfer(master)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	record.Stage, record.Frozen, record.Status = "aborted", false, "failed"
	if err = savePoolTransfer(master, record); err != nil {
		t.Fatal(err)
	}
	// The master's own snapshot completes the generation, as it does in a running pool.
	if err = recordPoolVMInventory(master, membership, poolVMInventory{
		HostID: "master-1", Generation: membership.Generation, VMs: []protocol.VMCreateSpec{},
		Storage: map[string]protocol.StorageIdentity{}, Observed: map[string]string{}, ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("the master's own snapshot after the freeze: %v", err)
	}
	if err = recordPoolVMInventory(master, membership, inventory); err != nil {
		t.Fatalf("settling after the freeze: %v", err)
	}
	if _, ok := poolVMRecords(t, master)[spec.ID]; !ok {
		t.Fatal("the owner record never settled after the transfer ended")
	}
}

// The two entry points the first audit missed: a member advertising its endpoint writes the
// membership, and a clustered-storage operation writes the storage_clusters section.
func TestFrozenAuthorityRefusesAdvertisedEndpoints(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	freezeLabAuthority(t, lab, "op-advertise")
	lab.drop("member-1", "")
	before := frozenAuthorityDigest(t, master)
	moved := strings.TrimRight(lab.origin["member-1"], "/") + ":0"
	if err := AdvertisePoolPeerOrigin(context.Background(), member, lab.origin["member-1"]); err == nil {
		t.Fatal("a frozen master accepted an advertised endpoint")
	}
	if after := frozenAuthorityDigest(t, master); after != before {
		t.Fatalf("an advertised endpoint changed a frozen authority: %s then %s", before, after)
	}
	_ = moved
	// Deferred, not dropped: the member keeps the advertisement pending and the same call
	// lands once the transfer is over.
	record, err := loadPoolTransfer(master)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	if peerState(t, member).PendingPeerOrigin == "" {
		t.Fatal("the refused advertisement was dropped instead of retained")
	}
	record.Stage, record.Frozen, record.Status = "aborted", false, "failed"
	if err = savePoolTransfer(master, record); err != nil {
		t.Fatal(err)
	}
	if err = RetryPoolPeerOrigin(context.Background(), member); err != nil {
		t.Fatalf("advertising after the freeze: %v", err)
	}
}
