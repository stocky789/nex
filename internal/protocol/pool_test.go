// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "testing"

func TestPoolCommandRejectsStaleOrUnboundMember(t *testing.T) {
	m := PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 2, Members: []PoolMember{{HostID: "master-1", CertificateSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Generation: 2, Status: "ready"}, {HostID: "member-1", CertificateSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Generation: 2, Status: "ready"}}}
	if err := (PoolCommand{RequestID: "join-1", Action: "member-join", Membership: m, ExpectedVersion: 2}).Valid(); err != nil {
		t.Fatalf("valid pool command: %v", err)
	}
	if err := (PoolCommand{RequestID: "join-1", Action: "member-join", Membership: m, ExpectedVersion: 1}).Valid(); err == nil {
		t.Fatal("stale membership generation accepted")
	}
	m.Members[1].CertificateSHA256 = "unbound"
	if err := (PoolCommand{RequestID: "join-1", Action: "member-join", Membership: m}).Valid(); err == nil {
		t.Fatal("unbound member certificate accepted")
	}
}
