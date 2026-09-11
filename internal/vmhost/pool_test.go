// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestPoolCommandCannotCreateMembershipFromControllerTransport(t *testing.T) {
	member := func(generation uint64) protocol.PoolMembership {
		return protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: generation, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Generation: generation, Status: "ready"}}}
	}
	command := protocol.Command{OperationID: "pool-op", Kind: "pool-member-join", Pool: &protocol.PoolCommand{RequestID: "join-1", Action: "member-join", Membership: member(1), ExpectedVersion: 1}}
	if err := HandlePoolCommand(context.Background(), t.TempDir(), "master-1", command); err == nil {
		t.Fatal("controller transport created membership")
	}
}

func TestRehomePoolScopePreservesDefaultResourceIdentity(t *testing.T) {
	e := NewManaged(t.TempDir())
	s := protocol.StorageRegistration{ID: "default-store", PoolID: "old-pool", HostID: "old-host", Name: "ISO", Path: "/srv/vms"}
	n := protocol.NetworkRegistration{ID: "default-network", PoolID: "old-pool", HostID: "old-host", Name: "Guests", Kind: "bridge", Source: "br0"}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "store", Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "store", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "network", Kind: "network-register", Network: &n}, Result: protocol.Result{OperationID: "network", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := RehomePoolScope(e.stateDir, "new-pool", "new-host"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Applied(context.Background())
	if len(got.Storage) != 1 || got.Storage[0].ID != s.ID || got.Storage[0].PoolID != "new-pool" || len(got.Networks) != 1 || got.Networks[0].ID != n.ID || got.Networks[0].HostID != "new-host" {
		t.Fatalf("rehomed = %#v", got)
	}
}
