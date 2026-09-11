// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func peerStore(t *testing.T, host, pool string) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.State.HostID, s.State.PoolID = host, pool
	return s
}

func TestPoolPeerJoinRecoversLostResponse(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	var lost atomic.Bool
	joinHandler := PoolPeerHandler(master)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !lost.Swap(true) {
			var request peerJoinRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode first join: %v", err)
				return
			}
			if _, err := acceptPeerJoin(master, request); err != nil {
				t.Errorf("first join: %v", err)
			}
			return // The master committed membership, but the response was lost.
		}
		joinHandler.ServeHTTP(w, r)
	}))
	_, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	// The offer is made only after its endpoint is known; TLS config creation does not alter it.
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err == nil {
		t.Fatal("lost response unexpectedly completed")
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
		t.Fatalf("lost-response retry: %v", err)
	}
	if gotURL, err := PoolMasterURL(member); err != nil || gotURL != server.URL {
		t.Fatalf("master endpoint = %q, %v", gotURL, err)
	}
	got, err := Membership(member)
	if err != nil || got.PoolID != "pool-1" || len(got.Members) != 2 || got.Members[1].HostID != "member-1" || member.State.PoolID != "pool-1" {
		t.Fatalf("membership = %#v, %v", got, err)
	}
	if _, err = PoolJoinCSR(member); err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err == nil {
		t.Fatal("offer accepted with a replacement key")
	}
	authServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := AuthenticatePoolPeer(master, r); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	authServer.TLS, err = PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	authServer.StartTLS()
	defer authServer.Close()
	peerClient, err := PoolPeerClient(member, authServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := peerClient.Get(authServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("authenticated peer = %d %q", response.StatusCode, body)
	}
}

func TestPoolPeerJoinPinsMasterCA(t *testing.T) {
	master, impostor, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "master-1", "pool-2"), peerStore(t, "member-1", "pool-3")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(impostor))
	_, _, err = CreatePoolJoinOffer(impostor, "https://master.example.test", member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(impostor)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err == nil {
		t.Fatal("wrong master CA was accepted")
	}
}

func TestPoolRehomeResumes(t *testing.T) {
	s := peerStore(t, "member-1", "old-pool")
	if err := savePoolPeer(s, poolPeerState{PendingRehome: &poolRehome{PoolID: "new-pool", PoolName: "New Pool"}}); err != nil {
		t.Fatal(err)
	}
	if err := ResumePoolRehome(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if s.State.PoolID != "new-pool" || s.State.PoolName != "New Pool" {
		t.Fatalf("state = %#v", s.State)
	}
	state, err := loadPoolPeer(s)
	if err != nil || state.PendingRehome != nil {
		t.Fatalf("pending rehome = %#v, %v", state.PendingRehome, err)
	}
}
