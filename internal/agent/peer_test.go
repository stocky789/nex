// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/tls"
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

func joinPeerMember(t *testing.T, master, member *Store) {
	t.Helper()
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	// Establish the master's CA before configuring the temporary join listener.
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
		t.Fatal(err)
	}
}

func advertiseTestPeer(t *testing.T, master, member *Store) *httptest.Server {
	t.Helper()
	config, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		for i := range state.Membership.Members {
			if state.Membership.Members[i].HostID == member.State.HostID {
				state.Membership.Members[i].PeerOrigin = server.URL
			}
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestPoolMemberPeerClientPinsCurrentMemberOriginAndCertificate(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	member := peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	server := advertiseTestPeer(t, master, member)

	client, err := PoolMemberPeerClient(master, server.URL, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("master-to-member mTLS: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("member response = %d", response.StatusCode)
	}

	other := peerStore(t, "member-2", "other-pool")
	joinPeerMember(t, master, other)
	otherServer := advertiseTestPeer(t, master, other)
	// The endpoint can be current while its certificate is not: pinning must
	// reject this CA-signed, but wrong, member identity during TLS verification.
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		for i := range state.Membership.Members {
			if state.Membership.Members[i].HostID == member.State.HostID {
				state.Membership.Members[i].PeerOrigin = otherServer.URL
			}
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	client, err = PoolMemberPeerClient(master, otherServer.URL, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	if response, err = client.Get(otherServer.URL); err == nil {
		response.Body.Close()
		t.Fatal("mismatched member certificate was accepted")
	}

	poolPeerMu.Lock()
	state, err = loadPoolPeer(master)
	if err == nil {
		for i := range state.Membership.Members {
			if state.Membership.Members[i].HostID == member.State.HostID {
				state.Membership.Members[i].PeerOrigin = server.URL
				state.Membership.Members[i].Status = "pending"
			}
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PoolMemberPeerClient(master, server.URL, member.State.HostID); err == nil {
		t.Fatal("stale member was accepted")
	}
	if _, err = PoolMemberPeerClient(master, server.URL, "not-a-member"); err == nil {
		t.Fatal("out-of-scope member was accepted")
	}
}

func TestStorageClusterMemberVerifyPathsAcceptOnlyCurrentMaster(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	member := peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	server := httptest.NewUnstartedServer(PoolStorageClusterHandler(member))
	config, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	defer server.Close()

	poolPeerMu.Lock()
	masterState, err := loadPoolPeer(master)
	if err == nil {
		for i := range masterState.Membership.Members {
			if masterState.Membership.Members[i].HostID == member.State.HostID {
				masterState.Membership.Members[i].PeerOrigin = server.URL
			}
		}
		err = savePoolPeer(master, masterState)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	client, err := PoolMemberPeerClient(master, server.URL, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL + poolStorageClusterPath + "/proof/repository-1")
	if err != nil {
		t.Fatalf("current master proof request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("current master proof status = %d", response.StatusCode)
	}

	foreign := peerStore(t, "member-2", "other-pool")
	joinPeerMember(t, master, foreign)
	poolPeerMu.Lock()
	foreignState, err := loadPoolPeer(foreign)
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	foreignCert, err := tls.X509KeyPair([]byte(foreignState.CertificatePEM), []byte(foreignState.KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	foreignClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{foreignCert}, InsecureSkipVerify: true}}}
	response, err = foreignClient.Get(server.URL + poolStorageClusterPath + "/proof/repository-1")
	if err != nil {
		t.Fatalf("foreign member request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("foreign member proof status = %d", response.StatusCode)
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

func TestPoolPeerExactRoutesRejectWrongMethodWith405(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	member := peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	client, err := PoolPeerClient(member, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/pool/advertise", poolJoinPath} {
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodPost {
			t.Fatalf("GET %s = %d Allow=%q, want 405 Allow=POST", path, response.StatusCode, response.Header.Get("Allow"))
		}
	}
}
