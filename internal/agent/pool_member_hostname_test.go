// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"os"
	"testing"
)

func TestPoolMembershipRetainsHostnamesAcrossJoin(t *testing.T) {
	hostname := "master.example.test"
	osHostname = func() (string, error) { return hostname, nil }
	t.Cleanup(func() { osHostname = os.Hostname })
	master, member, _, invitation := poolJoinLab(t)
	if got := poolMember(t, master, master.State.HostID).Hostname; got != hostname {
		t.Fatalf("master hostname = %q", got)
	}
	hostname = "member.example.test"
	result, _ := ExecutePoolJoin(context.Background(), member, poolJoinCommand("join-hostname", member, invitation))
	if result.Status != "succeeded" {
		t.Fatalf("join result = %+v", result)
	}
	if got := poolMember(t, master, member.State.HostID).Hostname; got != hostname {
		t.Fatalf("member hostname = %q", got)
	}
	if got := poolMember(t, member, member.State.HostID).Hostname; got != hostname {
		t.Fatalf("member copy hostname = %q", got)
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	membership.Members[1].Hostname = "bad\nname"
	if err := membership.Valid(); err == nil {
		t.Fatal("invalid durable hostname accepted")
	}
	if _, err := acceptPeerJoin(master, peerJoinRequest{Hostname: "bad\nname"}); err == nil {
		t.Fatal("invalid join hostname accepted")
	}
}

func TestJoinWithoutHostnameIsRefused(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := decodePeerOffer(invitation.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptPeerJoin(master, peerJoinRequest{Offer: offer.Token, HostID: member.State.HostID, CSRPEM: csr}); err == nil {
		t.Fatal("Core joined without a hostname")
	}
	if got := len(peerState(t, master).Membership.Members); got != 1 {
		t.Fatalf("refused join changed membership: %d rows", got)
	}
}

func TestEveryJoiningMemberRecordsItsHostname(t *testing.T) {
	osHostname = func() (string, error) { return "core.example.test", nil }
	t.Cleanup(func() { osHostname = os.Hostname })
	master := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"member-1", "member-2"} {
		joinPeerMember(t, master, peerStore(t, id, "solo-"+id))
		if got := poolMember(t, master, id).Hostname; got != "core.example.test" {
			t.Fatalf("%s hostname = %q", id, got)
		}
	}
}
