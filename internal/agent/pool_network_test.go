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
	"nex.local/nex/internal/vmhost"
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
	if checks := PoolConvergeChecks(master); len(checks) != 2 || checks[0].Status != "healthy" || checks[1].Status != "healthy" {
		t.Fatalf("master converge clear checks: %#v", checks)
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
	poolPeerMu.Lock()
	state, err := loadPoolPeer(member)
	if err == nil {
		state.Membership.MasterHostID = member.State.HostID
		err = savePoolPeer(member, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	checks = PoolConvergeChecks(member)
	if len(checks) != 2 || checks[0].Status != "healthy" || checks[1].Status != "healthy" || checks[0].Freshness != "fresh" || checks[1].Freshness != "fresh" {
		t.Fatalf("new master converge clears = %#v", checks)
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

// poolJournalEntries counts the managed journal files a store holds. A member converge pass
// runs every probe tick, so how many entries a persistently failing resource mints per day is
// the safety property here: the engine refuses every managed operation past 10000 entries.
func poolJournalEntries(t *testing.T, s *Store) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.dir, "managed-vms"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		// The engine keeps its lock file in the same directory.
		if filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	return n
}

// usePoolNetworkSysfs points the apply precheck at scratch sysfs and procfs roots.
func usePoolNetworkSysfs(t *testing.T) string {
	t.Helper()
	sys, proc := t.TempDir()+"/", t.TempDir()+"/"
	t.Cleanup(vmhost.NetworkSysfsRootsForTests(sys, proc))
	return sys
}

func qaPoolVLAN(s *Store) protocol.NetworkRegistration {
	return protocol.NetworkRegistration{
		ID: "vlan-100", PoolID: s.State.PoolID, HostID: s.State.HostID, Name: "Servers",
		Kind: "vlan", Source: protocol.GuestBridge("nexqa", 100), Uplink: "nexqa", VLAN: 100, Generation: 1,
	}
}

// A member whose pool VLAN apply was refused by a host precondition — the uplink NIC does not
// exist yet — used to journal the refusal under an operation ID pinned to the UTC clock hour,
// so every later tick replayed that stored failure and the member only retried for real when
// the hour changed: anywhere from seconds to 60 minutes after the operator fixed the uplink.
// A precondition is re-evaluated on every tick and journals nothing, so the retry is immediate.
func TestPoolNetworkApplyRefusalJournalsNothingAndRetriesWhenTheCauseIsFixed(t *testing.T) {
	member := peerStore(t, "member-1", "pool-1")
	sys := usePoolNetworkSysfs(t)
	vlan := qaPoolVLAN(member)
	for pass := 0; pass < 3; pass++ {
		err := applyPoolNetworkNative(context.Background(), member, vlan)
		var refusal poolConvergeRefusal
		if !errors.As(err, &refusal) || !strings.Contains(err.Error(), "nexqa") {
			t.Fatalf("pass %d apply error = %v", pass, err)
		}
		if n := poolJournalEntries(t, member); n != 0 {
			t.Fatalf("pass %d: a persistently refused apply journalled %d entries", pass, n)
		}
	}
	// The operator creates the uplink. The very next pass must attempt the apply for real,
	// in the same clock hour, rather than replaying the stored failure.
	if err := os.MkdirAll(sys+vlan.Uplink, 0700); err != nil {
		t.Fatal(err)
	}
	if err := applyPoolNetworkNative(context.Background(), member, vlan); err == nil {
		t.Fatal("apply succeeded although this host has no such uplink")
	}
	if n := poolJournalEntries(t, member); n != 1 {
		t.Fatalf("member did not retry on the next tick after the cause was fixed: %d journal entries", n)
	}
}

// The bound the hour-pinned operation ID buys: a failure the precheck cannot see coming still
// journals at most one entry per UTC hour, so a member that never converges writes at most 24
// entries per resource per day and cannot reach the engine's 10000-entry cap by itself.
func TestPoolNetworkUnexpectedFailureJournalsAtMostOncePerHour(t *testing.T) {
	member := peerStore(t, "member-1", "pool-1")
	sys := usePoolNetworkSysfs(t)
	vlan := qaPoolVLAN(member)
	// The precheck passes; the native apply still fails, because the real host has no such NIC.
	if err := os.MkdirAll(sys+vlan.Uplink, 0700); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	previous := poolConvergeNow
	t.Cleanup(func() { poolConvergeNow = previous })
	for tick := time.Duration(0); tick < 24*time.Hour; tick += time.Minute {
		poolConvergeNow = func() time.Time { return base.Add(tick) }
		if err := applyPoolNetworkNative(context.Background(), member, vlan); err == nil {
			t.Fatalf("apply succeeded at %v", tick)
		}
	}
	if n := poolJournalEntries(t, member); n > 24 {
		t.Fatalf("a day of failing converge passes journalled %d entries", n)
	}
}

// A replayed journal failure is not an observation. Re-posting it with this pass's clock made
// the master's member_access row look like a fresh observation of a condition nobody checked.
func TestPoolNetworkReplayedFailureKeepsTheMastersObservation(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	ClearPoolNetworkAccessForTest(master)
	vlan := protocol.NetworkRegistration{
		ID: "vlan-100", PoolID: "pool-1", HostID: master.State.HostID, Name: "Servers",
		Kind: "vlan", Source: protocol.GuestBridge("nex1", 100), Uplink: "nex1", VLAN: 100, Generation: 1,
	}
	writeAppliedPoolNetwork(t, master, vlan)
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)

	previous := applyPoolNetwork
	t.Cleanup(func() { applyPoolNetwork = previous })
	applyPoolNetwork = func(context.Context, *Store, protocol.NetworkRegistration) error {
		return poolConvergeRefusalf("uplink nex1 does not exist on this host")
	}
	if err = reconcilePoolNetworksFromMaster(context.Background(), member, time.Second); err == nil {
		t.Fatal("reconcile reported success for a refused apply")
	}
	reported := PoolNetworkMemberAccess(master, []protocol.NetworkRegistration{vlan})
	if len(reported[0].MemberAccess) != 1 || reported[0].MemberAccess[0].Error == "" {
		t.Fatalf("freshly refused apply was not reported: %#v", reported[0].MemberAccess)
	}
	firstSeen := reported[0].MemberAccess[0].ObservedAt
	if time.Since(firstSeen) > time.Minute {
		t.Fatalf("a freshly evaluated refusal carried an old observation time: %v", firstSeen)
	}

	replayedAt := time.Now().UTC().Add(-42 * time.Minute)
	applyPoolNetwork = func(context.Context, *Store, protocol.NetworkRegistration) error {
		return poolReplayedFailure{at: replayedAt, detail: "uplink nex1 does not exist on this host"}
	}
	time.Sleep(time.Millisecond)
	if err = reconcilePoolNetworksFromMaster(context.Background(), member, time.Second); err == nil {
		t.Fatal("reconcile reported success for a replayed failure")
	}
	reported = PoolNetworkMemberAccess(master, []protocol.NetworkRegistration{vlan})
	if len(reported[0].MemberAccess) != 1 || !reported[0].MemberAccess[0].ObservedAt.Equal(firstSeen) {
		t.Fatalf("a replayed journal failure was re-stamped as a new observation: %#v", reported[0].MemberAccess)
	}
}

// The precheck must refuse exactly the host preconditions the native apply refuses, and must
// never approve on a condition it cannot see: it only ever returns a refusal or nil.
func TestPoolNetworkApplyPrecheckRefusals(t *testing.T) {
	sys := usePoolNetworkSysfs(t)
	vlan := protocol.NetworkRegistration{
		ID: "vlan-100", PoolID: "pool-1", Kind: "vlan",
		Source: protocol.GuestBridge("nexqa", 100), Uplink: "nexqa", VLAN: 100, Generation: 1,
	}
	mk := func(parts ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(append([]string{sys}, parts...)...), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := poolNetworkApplyRefusal(protocol.NetworkRegistration{ID: "x", Kind: "vlan", Uplink: "nexqa"}); err == nil {
		t.Fatal("precheck accepted an invalid VLAN registration")
	}
	if err := poolNetworkApplyRefusal(vlan); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing uplink = %v", err)
	}
	mk("nexqa", "bridge")
	if err := poolNetworkApplyRefusal(vlan); err == nil || !strings.Contains(err.Error(), "physical NIC or bond") {
		t.Fatalf("bridge uplink = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(sys, "nexqa", "bridge")); err != nil {
		t.Fatal(err)
	}
	mk("bond9", "bonding")
	if err := os.Symlink(filepath.Join(sys, "bond9"), filepath.Join(sys, "nexqa", "master")); err != nil {
		t.Fatal(err)
	}
	if err := poolNetworkApplyRefusal(vlan); err == nil || !strings.Contains(err.Error(), "member of bond") {
		t.Fatalf("bond member uplink = %v", err)
	}
	if err := os.Remove(filepath.Join(sys, "nexqa", "master")); err != nil {
		t.Fatal(err)
	}
	mk("nexqa.100")
	mk("someone-else")
	if err := os.Symlink(filepath.Join(sys, "someone-else"), filepath.Join(sys, "nexqa.100", "master")); err != nil {
		t.Fatal(err)
	}
	if err := poolNetworkApplyRefusal(vlan); err == nil || !strings.Contains(err.Error(), "already used by") {
		t.Fatalf("claimed VLAN device = %v", err)
	}
	if err := os.Remove(filepath.Join(sys, "nexqa.100", "master")); err != nil {
		t.Fatal(err)
	}
	mk(vlan.Source)
	if err := poolNetworkApplyRefusal(vlan); err == nil || !strings.Contains(err.Error(), "not a bridge") {
		t.Fatalf("bridge name taken = %v", err)
	}
	mk(vlan.Source, "bridge")
	if err := poolNetworkApplyRefusal(vlan); err != nil {
		t.Fatalf("precheck refused a host that satisfies every visible precondition: %v", err)
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
