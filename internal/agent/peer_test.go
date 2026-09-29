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
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
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
	// Every enrolled Core starts as the master of its own pool; a single-host master may join another.
	for _, s := range []*Store{master, member} {
		if err := EnsurePoolMembership(s); err != nil {
			t.Fatal(err)
		}
	}
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

// An ejected row is historical membership, not a permanent ban on the host identity. Once
// that Core has become the master of its own pool, a new invitation must replace the old
// certificate with a ready row instead of making the host unrejoinable.
func TestEjectedMemberCanRejoinWithANewCertificate(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Membership.Generation++
		for i := range state.Membership.Members {
			state.Membership.Members[i].Generation = state.Membership.Generation
		}
		state.Membership.Members = append(state.Membership.Members, protocol.PoolMember{HostID: "member-1", CertificateSHA256: strings.Repeat("a", 64), Generation: state.Membership.Generation, Status: "ejected"})
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	member := peerStore(t, "member-1", "new-pool")
	if err := EnsurePoolMembership(member); err != nil {
		t.Fatal(err)
	}
	invitation, err := CreatePoolJoinInvitation(master, "https://master.example.test", member.State.HostID)
	if err != nil {
		t.Fatalf("invitation for ejected member: %v", err)
	}
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := decodePeerOffer(invitation.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptPeerJoin(master, peerJoinRequest{Offer: offer.Token, HostID: member.State.HostID, Hostname: "member.example.test", CSRPEM: csr}); err != nil {
		t.Fatalf("rejoin after eject: %v", err)
	}
	state, err = loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	var matches []protocol.PoolMember
	for _, candidate := range state.Membership.Members {
		if candidate.HostID == member.State.HostID {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 || matches[0].Status != "ready" || matches[0].CertificateSHA256 == strings.Repeat("a", 64) {
		t.Fatalf("rejoined membership row = %#v", matches)
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
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: "old-pool", VMID: vm.ID, HostID: s.State.HostID, Spec: *vm}}, Inventory: map[string]poolVMInventory{s.State.HostID: {HostID: s.State.HostID, Generation: 1, VMs: []protocol.VMCreateSpec{*vm}}}}); err != nil {
		t.Fatal(err)
	}
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
	vms, err := loadPoolVMs(s)
	if err != nil || len(vms.VMs) != 0 || len(vms.Inventory) != 0 {
		t.Fatalf("former pool VM authority survived rehome: %#v, %v", vms, err)
	}
}

func TestPoolRehomeRefusesUnsettledSnapshot(t *testing.T) {
	for _, status := range []string{"pending", "needs_attention"} {
		t.Run(status, func(t *testing.T) {
			s := peerStore(t, "member-1", "old-pool")
			vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
			s.State.Journal["snapshot-1"] = JournalEntry{
				Command: &protocol.Command{OperationID: "snapshot-1", Kind: "snapshot-create", VM: vm, Snapshot: &protocol.VMSnapshotRequest{Name: "Before update"}},
				Status:  status,
			}
			if err := s.Save(); err != nil {
				t.Fatal(err)
			}
			if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: "old-pool", VMID: vm.ID, HostID: s.State.HostID, Spec: *vm}}, Inventory: map[string]poolVMInventory{s.State.HostID: {HostID: s.State.HostID, Generation: 1, VMs: []protocol.VMCreateSpec{*vm}}}}); err != nil {
				t.Fatal(err)
			}
			if err := savePoolPeer(s, poolPeerState{PendingRehome: &poolRehome{PoolID: "new-pool", PoolName: "New Pool"}}); err != nil {
				t.Fatal(err)
			}

			err := ResumePoolRehome(context.Background(), s)
			if err == nil || !strings.Contains(err.Error(), "snapshot recovery") {
				t.Fatalf("rehome with %s snapshot = %v, want snapshot recovery refusal", status, err)
			}
			if s.State.PoolID != "old-pool" || s.State.PoolName == "New Pool" {
				t.Fatalf("refused rehome changed Core identity: %#v", s.State)
			}
			state, err := loadPoolPeer(s)
			if err != nil || state.PendingRehome == nil || state.PendingRehome.PoolID != "new-pool" {
				t.Fatalf("refused rehome cleared retry marker: %#v, %v", state.PendingRehome, err)
			}
			vms, err := loadPoolVMs(s)
			if err != nil || vms.VMs[vm.ID].PoolID != "old-pool" || len(vms.Inventory) != 1 {
				t.Fatalf("refused rehome cleared old pool ownership: %#v, %v", vms, err)
			}
		})
	}
}

// A deleted VM can leave an old solo pool's owner projection behind.  Joining another
// pool must discard that projection before it can be copied into a signed replica and
// later adopted by a promoted member.
func TestPoolRehomeDoesNotResurrectDeletedSoloVMOnPromotion(t *testing.T) {
	const vmID = "11111111-1111-4111-8111-111111111111"
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "solo-1")
	for _, store := range []*Store{master, member} {
		if err := EnsurePoolMembership(store); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(member.dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	stale := *poolVMTestSpec(vmID, member.State.HostID)
	stale.PoolID, stale.Revision = member.State.PoolID, 1
	writeManagedJournal(t, filepath.Join(member.dir, "managed-vms"), "create", protocol.Command{OperationID: "create", Kind: "vm-create", VM: &stale})
	writeManagedJournal(t, filepath.Join(member.dir, "managed-vms"), "delete", protocol.Command{OperationID: "delete", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: stale.ID, PoolID: stale.PoolID, HostID: stale.HostID}})
	if err := savePoolVMs(member, poolVMState{VMs: map[string]poolVMRecord{vmID: {PoolID: stale.PoolID, VMID: vmID, HostID: member.State.HostID, State: "stopped", Epoch: 2, Spec: stale}}, Inventory: map[string]poolVMInventory{member.State.HostID: {HostID: member.State.HostID, Generation: 1, Deleted: []string{vmID}}}}); err != nil {
		t.Fatal(err)
	}

	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	masterServer := httptest.NewUnstartedServer(PoolPeerHandler(master))
	masterServer.TLS = config
	masterServer.StartTLS()
	t.Cleanup(masterServer.Close)
	ca, offer, err := CreatePoolJoinOffer(master, masterServer.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, masterServer.URL, ca, offer); err != nil {
		t.Fatal(err)
	}
	if got, err := loadPoolVMs(member); err != nil || len(got.VMs) != 0 || len(got.Inventory) != 0 {
		t.Fatalf("solo VM authority survived join: %#v, %v", got, err)
	}
	// The completed rehome is safe to repeat after a lost caller response.
	if err = ResumePoolRehome(context.Background(), member); err != nil {
		t.Fatalf("resumed join = %v", err)
	}

	serveMemberPeer(t, master, member)
	setMemberPeerOrigin(t, master, master.State.HostID, masterServer.URL)
	destination := labOwnedVM(vmID, master.State.HostID)
	writePoolVMRecord(t, master, destination)
	writeLabFenceCredential(t, master, labFenceProfile().CredentialRef, labFenceIdentity)
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fence-profile", master.State.HostID, master.State.HostID, labFenceProfile())); result.Status != "succeeded" {
		t.Fatalf("fence profile = %#v", result)
	}
	if err = PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("signed replica = %v", err)
	}
	if replica := storedReplica(t, member); replica == nil || !strings.Contains(string(replica.Replica.VMs), vmID) || strings.Contains(string(replica.Replica.VMs), `"host_id":"member-1"`) {
		t.Fatalf("replica did not preserve only destination authority: %#v", replica)
	}

	masterServer.Close() // The promotion preflight must treat the old master as fenced off.
	fakeFence(t, observedPowerOff(0))
	if result := ExecutePoolPromotion(context.Background(), member, promoteCommand("rehome-promotion", member.State.HostID)); result.Status != "succeeded" {
		t.Fatalf("promotion = %#v", result)
	}
	adopted := poolVMRecords(t, member)
	gotJSON, _ := json.Marshal(adopted[vmID])
	wantJSON, _ := json.Marshal(destination)
	if len(adopted) != 1 || string(gotJSON) != string(wantJSON) {
		t.Fatalf("promotion resurrected solo authority instead of destination authority: %#v", adopted)
	}
}

func TestPoolRehomeCancellationKeepsMarkerForRetry(t *testing.T) {
	s := peerStore(t, "member-1", "old-pool")
	if err := savePoolPeer(s, poolPeerState{PendingRehome: &poolRehome{PoolID: "new-pool", PoolName: "New Pool"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poolVMFile(s), []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ResumePoolRehome(ctx, s); err != context.Canceled {
		t.Fatalf("cancelled rehome = %v, want context cancellation", err)
	}
	if state := peerState(t, s); state.PendingRehome == nil {
		t.Fatal("failed rehome cleared its retry marker")
	}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{}}); err != nil {
		t.Fatal(err)
	}
	if err := ResumePoolRehome(context.Background(), s); err != nil {
		t.Fatalf("rehome retry = %v", err)
	}
	if state := peerState(t, s); state.PendingRehome != nil {
		t.Fatalf("rehome marker after successful retry = %#v", state.PendingRehome)
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
