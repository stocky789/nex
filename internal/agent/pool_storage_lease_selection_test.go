// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestPoolStorageLeaseSelectionKeepsPreparationOnlyForUnchangedSelection(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	selected := &protocol.PoolStorageLeaseConfig{
		Repositories: []protocol.PoolStorageLeaseRepository{{
			RepositoryID: "repo-1", RepositoryGeneration: 3,
			RepositoryIdentity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{
				Server: "nfs.example.test", Export: "/exports/vms", FilesystemID: "0:42",
			}},
			Lockspace: protocol.PoolStorageLeaseLockspaceName("pool-1", "repo-1"),
		}},
		AuthorityRepositoryID: "repo-1", IOTimeoutSeconds: 10, HostDeadSeconds: 60, FenceTimeoutSeconds: 120,
	}
	if err := setPoolStorageLeaseSelection(s, membership, selected); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	state.Membership.StorageLease.PreparedGeneration = 42
	if err := savePoolPeer(s, state); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := setPoolStorageLeaseSelection(s, membership, selected); err != nil {
			t.Fatal(err)
		}
		current, err := Membership(s)
		if err != nil || current.StorageLease.PreparedGeneration != 42 {
			t.Fatalf("unchanged selection lost preparation: %+v, %v", current.StorageLease, err)
		}
	}
	changed := *selected
	changed.FenceTimeoutSeconds++
	if err := setPoolStorageLeaseSelection(s, membership, &changed); err != nil {
		t.Fatal(err)
	}
	current, err := Membership(s)
	if err != nil || current.StorageLease.PreparedGeneration != 0 || current.StorageLease.FenceTimeoutSeconds != changed.FenceTimeoutSeconds {
		t.Fatalf("changed selection kept preparation or was not saved: %+v, %v", current.StorageLease, err)
	}
}
