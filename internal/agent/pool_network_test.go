// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func writeAppliedPoolNetwork(t *testing.T, s *Store, network protocol.NetworkRegistration) {
	t.Helper()
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	network.MemberAccess, network.OperationID, network.Status, network.Error = nil, "", "", ""
	if network.Generation == 0 {
		network.Generation = 1
	}
	op := "network-" + network.ID
	journal, err := json.Marshal(map[string]any{
		"command": protocol.Command{OperationID: op, Kind: "network-register", Network: &network},
		"result":  protocol.Result{OperationID: op, Status: "succeeded"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, op+".json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPoolNetworkPeerListAndMemberReconcile(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	vlan := protocol.NetworkRegistration{
		ID: "vlan-100", PoolID: "pool-1", HostID: master.State.HostID, Name: "Servers",
		Kind: "vlan", Source: protocol.GuestBridge("nex1", 100), Uplink: "nex1", VLAN: 100, Generation: 1,
	}
	local := protocol.NetworkRegistration{
		ID: "local-br", PoolID: "pool-1", HostID: master.State.HostID, Name: "nex0 untagged",
		Kind: "bridge", Source: "br0", Uplink: "nex0",
	}
	writeAppliedPoolNetwork(t, master, vlan)
	writeAppliedPoolNetwork(t, master, local)

	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := PoolPeerClient(member, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)

	resp, err := client.Get(server.URL + "/api/v1/pool/networks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var listed []protocol.NetworkRegistration
	if err = json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != vlan.ID || listed[0].HostID != member.State.HostID {
		t.Fatalf("peer network list = %#v", listed)
	}

	applied := false
	previous := applyPoolNetwork
	t.Cleanup(func() { applyPoolNetwork = previous })
	applyPoolNetwork = func(_ context.Context, s *Store, network protocol.NetworkRegistration) error {
		applied = true
		writeAppliedPoolNetwork(t, s, network)
		return nil
	}
	if err = reconcilePoolNetworksFromMaster(context.Background(), member, time.Second); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !applied {
		t.Fatal("member did not apply pool VLAN")
	}
	reported := PoolNetworkMemberAccess(master, []protocol.NetworkRegistration{vlan, local})
	if len(reported[0].MemberAccess) != 1 || reported[0].MemberAccess[0].HostID != member.State.HostID || reported[0].MemberAccess[0].Error != "" || !reported[0].MemberAccess[0].UplinkPresent {
		t.Fatalf("member access = %#v", reported[0].MemberAccess)
	}
	if len(reported[1].MemberAccess) != 0 {
		t.Fatalf("host-local bridge carried member access: %#v", reported[1].MemberAccess)
	}
}

// Member rows are the master's report of its own pool. A member reporting placeholder rows
// for its peers invents pool state it is not responsible for, and an ejected host's last
// report must stop being reported rather than persisting in the process-local map.
func TestPoolMemberAccessIsReportedOnlyByTheMaster(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	ClearPoolNetworkAccessForTest(member)
	vlan := protocol.NetworkRegistration{ID: "vlan-1", PoolID: "pool-1", HostID: master.State.HostID, Kind: "vlan", Source: "nex1v10", Uplink: "nex1", VLAN: 10, Generation: 1}
	if reported := PoolNetworkMemberAccess(member, []protocol.NetworkRegistration{vlan}); len(reported[0].MemberAccess) != 0 {
		t.Fatalf("member reported pool network member access: %#v", reported[0].MemberAccess)
	}
	storage := protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: master.State.HostID, Name: "shared", Path: t.TempDir(),
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1,
	}
	media := protocol.Media{ID: "iso-1", PoolID: "pool-1", HostID: master.State.HostID, StorageID: storage.ID, Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("a", 64)}
	ClearPoolMediaAccessForTest(member)
	if reported := PoolMediaMemberAccess(member, []protocol.Media{media}, []protocol.StorageRegistration{storage}); len(reported[0].MemberAccess) != 0 {
		t.Fatalf("member reported pool media member access: %#v", reported[0].MemberAccess)
	}
}

// Discarding the network and media converge results left a member whose VLAN or shared ISO
// never materialized reporting itself healthy. The check states the outcome only; remote
// error text may name hosts or credentials and never belongs in a reported check.
func TestPoolConvergeChecksSurfaceMemberFailures(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	ClearPoolConvergeStatusForTest(member)
	if checks := PoolConvergeChecks(master); len(checks) != 0 {
		t.Fatalf("master reported member converge checks: %#v", checks)
	}
	checks := PoolConvergeChecks(member)
	if len(checks) != 2 || checks[0].Status != "unknown" || checks[1].Status != "unknown" {
		t.Fatalf("first pass checks = %#v", checks)
	}
	recordPoolConverge(member, poolNetworkConvergeCheck, errors.New("transport failed: secret-marker"))
	recordPoolConverge(member, poolMediaConvergeCheck, nil)
	checks = PoolConvergeChecks(member)
	if len(checks) != 2 || checks[0].Status != "failed" || checks[0].Error != "pool-network-reconciliation failed" || strings.Contains(checks[0].Error, "secret") {
		t.Fatalf("network converge check = %#v", checks)
	}
	if checks[1].Status != "healthy" || checks[1].Error != "" {
		t.Fatalf("media converge check = %#v", checks[1])
	}
}

// A journal entry records what this host was asked to apply, not what is on the host now.
// Converge that short-circuits on it reports a removed guest bridge as present forever.
func TestPoolNetworkConvergeObservesTheBridgeOnAJournalMatch(t *testing.T) {
	member := peerStore(t, "member-1", "pool-1")
	vlan := protocol.NetworkRegistration{
		ID: "vlan-100", PoolID: "pool-1", HostID: member.State.HostID, Name: "Servers",
		Kind: "vlan", Source: protocol.GuestBridge("nexqa", 100), Uplink: "nexqa", VLAN: 100, Generation: 1,
	}
	writeAppliedPoolNetwork(t, member, vlan)
	if err := applyPoolNetworkNative(context.Background(), member, vlan); err == nil {
		t.Fatal("converge reported a guest bridge that is not present on this host")
	}
}

func TestPoolNetworkMemberAccessAwaitingPlaceholder(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	ClearPoolNetworkAccessForTest(master)
	vlan := protocol.NetworkRegistration{ID: "vlan-1", PoolID: "pool-1", HostID: master.State.HostID, Kind: "vlan", Source: "nex1v10", Uplink: "nex1", VLAN: 10, Generation: 1}
	reported := PoolNetworkMemberAccess(master, []protocol.NetworkRegistration{vlan})
	if len(reported[0].MemberAccess) != 1 || reported[0].MemberAccess[0].Error != "awaiting member reconciliation" {
		t.Fatalf("placeholder = %#v", reported[0].MemberAccess)
	}
}
