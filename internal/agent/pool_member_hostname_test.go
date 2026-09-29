// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestPoolMembershipHostnamesRequireControllerAcknowledgement(t *testing.T) {
	managed := &protocol.ManagedState{Membership: &protocol.PoolMembership{Members: []protocol.PoolMember{{HostID: "master-1", Hostname: "master.example.test"}}}}
	if got := membershipNamesForController(managed, false).Membership.Members[0].Hostname; got != "" {
		t.Fatalf("preflight hostname = %q", got)
	}
	if got := managed.Membership.Members[0].Hostname; got != "master.example.test" {
		t.Fatalf("durable membership was changed by projection: %q", got)
	}
	if got := membershipNamesForController(managed, true).Membership.Members[0].Hostname; got != "master.example.test" {
		t.Fatalf("acknowledged hostname = %q", got)
	}
}

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
	legacy, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Members[1].Hostname = ""
	if err := legacy.Valid(); err != nil {
		t.Fatalf("legacy membership rejected: %v", err)
	}
	legacy.Members[1].Hostname = "bad\nname"
	if err := legacy.Valid(); err == nil {
		t.Fatal("invalid durable hostname accepted")
	}
	if _, err := acceptPeerJoin(master, peerJoinRequest{Hostname: "bad\nname"}); err == nil {
		t.Fatal("invalid join hostname accepted")
	}
}

func TestNamedPoolRefusesLegacyJoin(t *testing.T) {
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
		t.Fatal("legacy Core joined a pool whose signed membership contains hostnames")
	}
	if got := len(peerState(t, master).Membership.Members); got != 1 {
		t.Fatalf("legacy join changed membership: %d rows", got)
	}
}

func TestLegacyJoinReplaySurvivesHostnameBackfill(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Membership.Members[0].Hostname = ""
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := decodePeerOffer(invitation.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	request := peerJoinRequest{Offer: offer.Token, HostID: member.State.HostID, CSRPEM: csr}
	first, err := acceptPeerJoin(master, request)
	if err != nil {
		t.Fatal(err)
	}
	poolPeerMu.Lock()
	state, err = loadPoolPeer(master)
	if err == nil {
		state.Membership.Members[0].Hostname = "master.example.test"
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	replay, err := acceptPeerJoin(master, request)
	if err != nil || replay.CertificatePEM != first.CertificatePEM || len(replay.Membership.Members) != 2 {
		t.Fatalf("retained join replay failed after hostname repair: %v", err)
	}
}

func TestLegacyPoolHostnamesWaitForEveryPeerAndConverge(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	for _, host := range []*Store{master, member} {
		poolPeerMu.Lock()
		state, err := loadPoolPeer(host)
		if err == nil {
			for i := range state.Membership.Members {
				state.Membership.Members[i].Hostname = ""
			}
			err = savePoolPeer(host, state)
		}
		poolPeerMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	beforeGeneration := peerState(t, master).Membership.Generation
	hostname := "master.example.test"
	osHostname = func() (string, error) { return hostname, nil }
	t.Cleanup(func() { osHostname = os.Hostname })
	config, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	oldPeer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := AuthenticatePoolMaster(member, r); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	oldPeer.TLS = config
	oldPeer.StartTLS()
	t.Cleanup(oldPeer.Close)
	setMemberPeerOrigin(t, master, member.State.HostID, oldPeer.URL)
	if err := ReportPoolHostname(context.Background(), master); err == nil {
		t.Fatal("master recorded a hostname while a peer lacked format support")
	}
	hostname = "member.example.test"
	if err := ReportPoolHostname(context.Background(), member); err == nil {
		t.Fatal("member hostname was admitted while a peer lacked format support")
	}
	if poolMember(t, master, master.State.HostID).Hostname != "" || poolMember(t, master, member.State.HostID).Hostname != "" {
		t.Fatal("legacy pool names changed before every peer upgraded")
	}
	setMemberPeerOrigin(t, master, member.State.HostID, lab.origin[member.State.HostID])
	lab.drop(member.State.HostID, "!/replica")
	hostname = "master.example.test"
	if err := ReportPoolHostname(context.Background(), master); err == nil {
		t.Fatal("master recorded a hostname while a peer was offline")
	}
	lab.drop(member.State.HostID, "")
	if err := ReportPoolHostname(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	hostname = "member.example.test"
	if err := ReportPoolHostname(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if got := poolMember(t, master, master.State.HostID).Hostname; got != "master.example.test" {
		t.Fatalf("master hostname = %q", got)
	}
	if got := poolMember(t, master, member.State.HostID).Hostname; got != "member.example.test" {
		t.Fatalf("member hostname = %q", got)
	}
	if peerState(t, master).Membership.Generation != beforeGeneration {
		t.Fatal("hostname repair advanced membership generation")
	}
	if err := PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err != nil {
		t.Fatalf("signed hostname replica: %v", err)
	}
	replica, err := loadPoolReplica(member)
	if err != nil || replica == nil || !replica.Verified || replica.Replica.Membership.Members[1].Hostname != "member.example.test" {
		t.Fatalf("member did not verify durable hostnames in authority replica: %v", err)
	}
	before, err := os.Stat(poolStatePath(master))
	if err != nil {
		t.Fatal(err)
	}
	hostname = "master.example.test"
	if err := ReportPoolHostname(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	hostname = "member.example.test"
	if err := ReportPoolHostname(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(poolStatePath(master))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged hostname rewrote membership")
	}
	if err := postPoolAdvertise(context.Background(), member, map[string]any{"hostname": "bad\nname"}); err == nil {
		t.Fatal("invalid peer hostname accepted")
	}
}
