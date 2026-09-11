// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"os"
	"testing"
)

func TestPoolCAIsDurableAndPrivate(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	member := peerStore(t, "member-1", "pool-2")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = CreatePoolJoinOffer(s, "https://master.example.test", member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolPeer(s)
	if err != nil || state.CAKeyPEM == "" || state.Membership.MasterHostID != "master-1" {
		t.Fatalf("pool CA state = %#v, %v", state, err)
	}
	info, err := os.Stat(poolStatePath(s))
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("pool peer state is not private: %v %v", info, err)
	}
}
