// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"io"
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

// repointPoolMaster makes the member pull from a stand-in master endpoint. The peer client
// still pins the real master's CA, so the stand-in must serve the master's TLS identity.
func repointPoolMaster(t *testing.T, member *Store, url string) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(member)
	if err != nil {
		t.Fatal(err)
	}
	state.MasterURL = url
	if err = savePoolPeer(member, state); err != nil {
		t.Fatal(err)
	}
}

// standInMaster serves handler with the master's peer TLS identity.
func standInMaster(t *testing.T, master *Store, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

// writeAppliedPoolVM journals a valid succeeded definition: Core drops a journal whose
// command does not validate, so a half-filled spec would silently prove nothing.
func writeAppliedPoolVM(t *testing.T, s *Store, id, networkID, mediaID string) protocol.VMCreateSpec {
	t.Helper()
	vm := protocol.VMCreateSpec{
		ID: id, PoolID: s.State.PoolID, HostID: s.State.HostID, Name: "guest", GuestOS: "linux",
		CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: "repo-1", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 2}},
		NICs:  []protocol.VMNIC{{NetworkID: networkID, Model: "virtio"}},
		Boot:  protocol.VMBoot{Firmware: "uefi", MediaBootOrder: 1}, MediaID: mediaID,
	}
	writeAppliedPoolVMSpec(t, s, vm)
	return vm
}

func writeAppliedPoolVMSpec(t *testing.T, s *Store, vm protocol.VMCreateSpec) {
	t.Helper()
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	op := "vm-" + vm.ID
	journal, err := json.Marshal(map[string]any{
		"command": protocol.Command{OperationID: op, Kind: "vm-create", VM: &vm},
		"result":  protocol.Result{OperationID: op, Status: "succeeded"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, op+".json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
}

func journalEntries(t *testing.T, s *Store) []struct {
	Command protocol.Command `json:"command"`
	Result  protocol.Result  `json:"result"`
} {
	t.Helper()
	var out []struct {
		Command protocol.Command `json:"command"`
		Result  protocol.Result  `json:"result"`
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, "managed-vms"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, "managed-vms", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var journal struct {
			Command protocol.Command `json:"command"`
			Result  protocol.Result  `json:"result"`
		}
		if json.Unmarshal(b, &journal) != nil {
			continue
		}
		out = append(out, journal)
	}
	return out
}

func memberNetworks(t *testing.T, s *Store) []protocol.NetworkRegistration {
	t.Helper()
	out, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedNetworks()
	if err != nil {
		t.Fatalf("applied networks: %v", err)
	}
	return out
}

func liveMemberNetwork(t *testing.T, s *Store, id string) (protocol.NetworkRegistration, bool) {
	t.Helper()
	for _, n := range memberNetworks(t, s) {
		if n.ID == id && !n.Tombstone {
			return n, true
		}
	}
	return protocol.NetworkRegistration{}, false
}

func memberNetworkTombstoned(t *testing.T, s *Store, id string) bool {
	t.Helper()
	for _, n := range memberNetworks(t, s) {
		if n.ID == id && n.Tombstone {
			return true
		}
	}
	return false
}

func memberMediaLive(t *testing.T, s *Store, id string) bool {
	t.Helper()
	media, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedMedia()
	if err != nil {
		t.Fatalf("applied media: %v", err)
	}
	for _, m := range media {
		if m.ID == id && !m.Tombstone {
			return true
		}
	}
	return false
}

func poolVLAN(id, pool, host, name, uplink string, vlan int, generation uint64) protocol.NetworkRegistration {
	return protocol.NetworkRegistration{
		ID: id, PoolID: pool, HostID: host, Name: name, Kind: "vlan",
		Source: protocol.GuestBridge(uplink, vlan), Uplink: uplink, VLAN: vlan, Generation: generation,
	}
}

// joinedPair returns a master and a member that joined it, with the member pulling from the
// master's real peer handler. The VLANs here use uplink names no host has, so the native
// teardown sees an absent bridge and an absent stanza: a completed removal.
func joinedPair(t *testing.T) (*Store, *Store) {
	t.Helper()
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	ClearPoolConvergeStatusForTest(member)
	ClearPoolNetworkAccessForTest(master)
	ClearPoolMediaAccessForTest(master)
	return master, member
}

// A network the master no longer lists is removed on the member and tombstoned, so nothing
// rebuilds it. A VLAN this host kept from a former pool is not this master's to remove.
func TestMemberRemovesNetworkTheMasterNoLongerLists(t *testing.T) {
	master, member := joinedPair(t)
	removed := poolVLAN("vlan-gone", "pool-1", member.State.HostID, "Servers", "nexqa", 100, 1)
	former := poolVLAN("vlan-former", "old-pool", member.State.HostID, "Old", "nexqb", 200, 1)
	local := protocol.NetworkRegistration{ID: "local-br", PoolID: "pool-1", HostID: member.State.HostID, Kind: "bridge", Source: "br0", Uplink: "nex0"}
	writeAppliedPoolNetwork(t, member, removed)
	writeAppliedPoolNetwork(t, member, former)
	writeAppliedPoolNetwork(t, member, local)

	if err := reconcilePoolNetworksFromMaster(context.Background(), member, time.Second); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if _, live := liveMemberNetwork(t, member, removed.ID); live {
		t.Fatal("member kept a network the master no longer lists")
	}
	if !memberNetworkTombstoned(t, member, removed.ID) {
		t.Fatal("removed network was not tombstoned on the member")
	}
	if _, live := liveMemberNetwork(t, member, former.ID); !live {
		t.Fatal("a former pool's VLAN was removed by this pool's master")
	}
	if _, live := liveMemberNetwork(t, member, local.ID); !live {
		t.Fatal("a host-local bridge was removed by pool convergence")
	}
	// The master is unaware of the member's teardown: absence is the whole contract.
	if reported := PoolNetworkMemberAccess(master, []protocol.NetworkRegistration{removed}); len(reported[0].MemberAccess) != 1 || reported[0].MemberAccess[0].Error == "" {
		t.Fatalf("master access rows = %#v", reported[0].MemberAccess)
	}
	// A later pass must not hand the removed network to EnsureAppliedNetwork, which would
	// rebuild its bridge, and must not re-register or re-remove it.
	entries := len(journalEntries(t, member))
	if err := reconcilePoolNetworksFromMaster(context.Background(), member, time.Second); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if _, live := liveMemberNetwork(t, member, removed.ID); live || !memberNetworkTombstoned(t, member, removed.ID) {
		t.Fatal("a second pass resurrected a removed network")
	}
	if after := len(journalEntries(t, member)); after != entries {
		t.Fatalf("a settled removal wrote %d more journal entries", after-entries)
	}
}

// The ISO sibling: an ISO the master dropped from its library leaves the member's storage.
func TestMemberRemovesISOTheMasterNoLongerLists(t *testing.T) {
	_, member := joinedPair(t)
	storage := protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: member.State.HostID, Name: "shared", Path: t.TempDir(),
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1,
	}
	writeAppliedPoolStorage(t, member, storage)
	shared := protocol.Media{ID: "iso-gone", PoolID: "pool-1", HostID: member.State.HostID, StorageID: storage.ID, Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("a", 64)}
	local := protocol.Media{ID: "iso-local", PoolID: "pool-1", HostID: member.State.HostID, StorageID: "host-local-repo", Name: "tools.iso", SizeBytes: 40000, SHA256: strings.Repeat("b", 64)}
	writeAppliedPoolMedia(t, member, shared)
	writeAppliedPoolMedia(t, member, local)

	if err := reconcilePoolMediaFromMaster(context.Background(), member, time.Second); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if memberMediaLive(t, member, shared.ID) {
		t.Fatal("member kept an ISO the master no longer lists")
	}
	if !memberMediaLive(t, member, local.ID) {
		t.Fatal("a host-local ISO was removed by pool convergence")
	}
}

// Absence only means removal in a list that passed the gate. Every failing shape must
// return before the orphan loop, with the member's networks and ISOs untouched.
func TestFailedMasterListRemovesNothing(t *testing.T) {
	entry := poolVLAN("vlan-1", "pool-1", "master-1", "Servers", "nexqa", 100, 1)
	foreign := poolVLAN("vlan-2", "another-pool", "master-1", "Foreign", "nexqa", 101, 1)
	media := protocol.Media{ID: "iso-1", PoolID: "pool-1", HostID: "master-1", StorageID: "repo-1", Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("a", 64)}
	foreignMedia := media
	foreignMedia.ID, foreignMedia.PoolID = "iso-2", "another-pool"

	cases := []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
	}{
		{"server error", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }, time.Second},
		{"undecodable body", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{not json")) }, time.Second},
		{"foreign pool entry", func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "media") {
				_ = json.NewEncoder(w).Encode([]protocol.Media{foreignMedia})
				return
			}
			_ = json.NewEncoder(w).Encode([]protocol.NetworkRegistration{foreign})
		}, time.Second},
		{"expired list context", func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			if strings.Contains(r.URL.Path, "media") {
				_ = json.NewEncoder(w).Encode([]protocol.Media{})
				return
			}
			_ = json.NewEncoder(w).Encode([]protocol.NetworkRegistration{})
		}, 20 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			master, member := joinedPair(t)
			server := standInMaster(t, master, c.handler)
			repointPoolMaster(t, member, server.URL)
			held := entry
			held.HostID = member.State.HostID
			writeAppliedPoolNetwork(t, member, held)
			storage := protocol.StorageRegistration{
				ID: "repo-1", PoolID: "pool-1", HostID: member.State.HostID, Name: "shared", Path: t.TempDir(),
				Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1,
			}
			writeAppliedPoolStorage(t, member, storage)
			heldMedia := media
			heldMedia.HostID = member.State.HostID
			writeAppliedPoolMedia(t, member, heldMedia)

			if err := reconcilePoolNetworksFromMaster(context.Background(), member, c.timeout); err == nil {
				t.Fatal("a failing network list was accepted as authoritative")
			}
			if err := reconcilePoolMediaFromMaster(context.Background(), member, c.timeout); err == nil {
				t.Fatal("a failing media list was accepted as authoritative")
			}
			if _, live := liveMemberNetwork(t, member, held.ID); !live {
				t.Fatal("a network was removed from a list that never passed the gate")
			}
			if !memberMediaLive(t, member, heldMedia.ID) {
				t.Fatal("an ISO was removed from a list that never passed the gate")
			}
		})
	}
}

// corruptJournal makes one entry of this host's managed journal unreadable, as a half-written
// or damaged file does. Every projection built from the journal is then a subset of what the
// host actually holds.
func corruptJournal(t *testing.T, s *Store) {
	t.Helper()
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "half-written.json"), []byte(`{"command":{`), 0600); err != nil {
		t.Fatal(err)
	}
}

// The member reads absence from the master's list as removal, so a master that cannot read its
// own journal whole must answer the peer list with a failure, never with a short list. One
// damaged file on the master would otherwise tear down every pool VLAN and unlink every shared
// ISO on every member — exactly when the master is least able to be trusted.
func TestIncompleteMasterJournalFailsThePeerListClosed(t *testing.T) {
	master, member := joinedPair(t)
	held := poolVLAN("vlan-1", "pool-1", member.State.HostID, "Servers", "nexqa", 100, 1)
	writeAppliedPoolNetwork(t, member, held)
	storage := protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: member.State.HostID, Name: "shared", Path: t.TempDir(),
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1,
	}
	writeAppliedPoolStorage(t, member, storage)
	heldMedia := protocol.Media{ID: "iso-1", PoolID: "pool-1", HostID: member.State.HostID, StorageID: storage.ID, Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("a", 64)}
	writeAppliedPoolMedia(t, member, heldMedia)
	corruptJournal(t, master)

	// The real peer handler, over the real peer TLS, answers 503 rather than 200 [].
	client, err := PoolPeerClient(member, masterPeerURL(t, member))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	for _, path := range []string{"/api/v1/pool/networks", "/api/v1/pool/media", "/api/v1/pool/networks/vlan-1", "/api/v1/pool/media/iso-1"} {
		response, err := client.Get(masterPeerURL(t, member) + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<12))
		response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d %q", path, response.StatusCode, body)
		}
		if !strings.Contains(string(body), "managed journal is incomplete") || strings.Contains(string(body), "half-written") {
			t.Fatalf("%s body = %q", path, body)
		}
	}
	// The member's gate already treats a non-200 list as "remove nothing"; prove it end to end.
	if err := reconcilePoolNetworksFromMaster(context.Background(), member, time.Second); err == nil {
		t.Fatal("an incomplete master list was accepted as authoritative")
	}
	if err := reconcilePoolMediaFromMaster(context.Background(), member, time.Second); err == nil {
		t.Fatal("an incomplete master media list was accepted as authoritative")
	}
	if _, live := liveMemberNetwork(t, member, held.ID); !live {
		t.Fatal("a network was removed because the master could not read its own journal")
	}
	if !memberMediaLive(t, member, heldMedia.ID) {
		t.Fatal("an ISO was removed because the master could not read its own journal")
	}
	// A member that cannot read its own journal whole removes nothing either.
	corruptJournal(t, member)
	if err := removeOrphanPoolNetworks(context.Background(), member, map[string]bool{}, time.Second); err == nil {
		t.Fatal("a member tore down networks from its own incomplete journal")
	}
	if err := removeOrphanPoolMedia(context.Background(), member, map[string]bool{}, time.Second); err == nil {
		t.Fatal("a member removed ISOs from its own incomplete journal")
	}
}

// A removal on this host is permanent, so a master that lists the ID again — restored from an
// older backup, say — cannot be answered with a registration that can only fail. The member
// must refuse before Execute: no journal entry however many passes run, and a message the
// operator can act on.
func TestMemberRefusesAResourceItAlreadyRemoved(t *testing.T) {
	master, member := joinedPair(t)
	vlan := poolVLAN("vlan-back", "pool-1", member.State.HostID, "Servers", "nexqa", 100, 1)
	writeAppliedPoolNetwork(t, member, vlan)
	storage := protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: member.State.HostID, Name: "shared", Path: t.TempDir(),
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1,
	}
	writeAppliedPoolStorage(t, member, storage)
	media := protocol.Media{ID: "iso-back", PoolID: "pool-1", HostID: member.State.HostID, StorageID: storage.ID, Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("a", 64)}
	writeAppliedPoolMedia(t, member, media)

	// The member removes both because the master lists neither, then the master lists them again.
	if err := reconcilePoolNetworksFromMaster(context.Background(), member, time.Second); err != nil {
		t.Fatalf("first network pass: %v", err)
	}
	if err := reconcilePoolMediaFromMaster(context.Background(), member, time.Second); err != nil {
		t.Fatalf("first media pass: %v", err)
	}
	if !memberNetworkTombstoned(t, member, vlan.ID) || memberMediaLive(t, member, media.ID) {
		t.Fatal("the member did not remove what the master stopped listing")
	}
	listed := vlan
	listed.HostID = master.State.HostID
	listedMedia := media
	listedMedia.HostID = master.State.HostID
	server := standInMaster(t, master, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/pool/networks":
			_ = json.NewEncoder(w).Encode([]protocol.NetworkRegistration{listed})
		case "/api/v1/pool/networks/" + listed.ID:
			_ = json.NewEncoder(w).Encode(listed)
		case "/api/v1/pool/media":
			_ = json.NewEncoder(w).Encode([]protocol.Media{listedMedia})
		case "/api/v1/pool/media/" + listedMedia.ID:
			_ = json.NewEncoder(w).Encode(listedMedia)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	repointPoolMaster(t, member, server.URL)

	before := len(journalEntries(t, member))
	var networkErr, mediaErr error
	for pass := 0; pass < 3; pass++ {
		networkErr = reconcilePoolNetworksFromMaster(context.Background(), member, time.Second)
		mediaErr = reconcilePoolMediaFromMaster(context.Background(), member, time.Second)
		if networkErr == nil || mediaErr == nil {
			t.Fatalf("pass %d: converge reported success for a resource this host removed", pass)
		}
	}
	if after := len(journalEntries(t, member)); after != before {
		t.Fatalf("re-listed removed resources wrote %d journal entries", after-before)
	}
	recordPoolConverge(member, poolNetworkConvergeCheck, networkErr)
	recordPoolConverge(member, poolMediaConvergeCheck, mediaErr)
	checks := PoolConvergeChecks(member)
	if len(checks) != 2 {
		t.Fatalf("converge checks = %#v", checks)
	}
	want := "pool network " + vlan.ID + " was removed on this host and is listed again by the master; remove it on the master and add it again"
	if !strings.Contains(checks[0].Error, want) {
		t.Fatalf("network converge check = %q", checks[0].Error)
	}
	if !strings.Contains(checks[1].Error, "pool ISO "+media.ID+" was removed on this host and is listed again by the master") {
		t.Fatalf("media converge check = %q", checks[1].Error)
	}
	if _, live := liveMemberNetwork(t, member, vlan.ID); live || memberMediaLive(t, member, media.ID) {
		t.Fatal("a tombstoned resource came back")
	}
}

// A teardown that fails natively on every pass must not write a journal file on every pass:
// at the shipped probe interval that reaches the engine's retained-entry cap within a day,
// after which every managed operation on the host fails. One entry per resource per hour.
func TestRepeatedNativeFailureWritesOneJournalEntryPerHour(t *testing.T) {
	_, member := joinedPair(t)
	storage := protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: member.State.HostID, Name: "shared", Path: t.TempDir(),
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1,
	}
	writeAppliedPoolStorage(t, member, storage)
	// The published path is a non-empty directory, so the unlink fails for a reason no
	// precheck can see: a genuinely unexpected native failure.
	stuck := filepath.Join(t.TempDir(), "stuck.iso")
	if err := os.MkdirAll(filepath.Join(stuck, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	media := protocol.Media{ID: "iso-stuck", PoolID: "pool-1", HostID: member.State.HostID, StorageID: storage.ID, Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("a", 64)}
	writeAppliedPoolMediaAt(t, member, media, stuck)
	before := len(journalEntries(t, member))

	for pass := 0; pass < 4; pass++ {
		if err := removeOrphanPoolMedia(context.Background(), member, map[string]bool{}, time.Second); err == nil {
			t.Fatalf("pass %d: a failing unlink reported convergence", pass)
		}
	}
	if after := len(journalEntries(t, member)); after != before+1 {
		t.Fatalf("four failing passes wrote %d journal entries, want 1", after-before)
	}
	if memberMediaLive(t, member, media.ID) == false {
		t.Fatal("a failed removal tombstoned the ISO anyway")
	}
	// The stored failure is what a replay returns, and the next hour's ID is a different one.
	generation := protocol.MediaGeneration(media)
	if poolConvergeOperation("media-remove", media.ID, generation) != poolConvergeOperation("media-remove", media.ID, generation) {
		t.Fatal("the converge operation ID is not stable within the hour")
	}
	if poolConvergeOperation("media-remove", media.ID, generation) == poolConvergeOperation("media-remove", media.ID, generation+1) {
		t.Fatal("the converge operation ID ignores the generation")
	}
}

// A member refuses to tear down a network or ISO one of its own VMs uses, keeps the
// resource, reports the refusal in its converge check, and writes no journal entry for it
// however many passes run.
func TestMemberRefusesRemovalWhileALocalVMUsesTheResource(t *testing.T) {
	_, member := joinedPair(t)
	network := poolVLAN("vlan-used", "pool-1", member.State.HostID, "Servers", "nexqa", 100, 1)
	writeAppliedPoolNetwork(t, member, network)
	storage := protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: member.State.HostID, Name: "shared", Path: t.TempDir(),
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1,
	}
	writeAppliedPoolStorage(t, member, storage)
	media := protocol.Media{ID: "iso-used", PoolID: "pool-1", HostID: member.State.HostID, StorageID: storage.ID, Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("a", 64)}
	writeAppliedPoolMedia(t, member, media)
	vm := writeAppliedPoolVM(t, member, "11111111-1111-4111-8111-111111111111", network.ID, media.ID)
	before := len(journalEntries(t, member))

	for pass := 0; pass < 2; pass++ {
		networkErr := reconcilePoolNetworksFromMaster(context.Background(), member, time.Second)
		mediaErr := reconcilePoolMediaFromMaster(context.Background(), member, time.Second)
		if networkErr == nil || mediaErr == nil {
			t.Fatalf("pass %d: converge reported success while a local VM used the resource", pass)
		}
		recordPoolConverge(member, poolNetworkConvergeCheck, networkErr)
		recordPoolConverge(member, poolMediaConvergeCheck, mediaErr)
	}
	if _, live := liveMemberNetwork(t, member, network.ID); !live {
		t.Fatal("a network a local VM uses was torn down")
	}
	if !memberMediaLive(t, member, media.ID) {
		t.Fatal("an ISO a local VM holds was removed")
	}
	if after := len(journalEntries(t, member)); after != before {
		t.Fatalf("refused removals wrote %d journal entries", after-before)
	}
	checks := PoolConvergeChecks(member)
	if len(checks) != 2 || !strings.Contains(checks[0].Error, vm.ID) || !strings.Contains(checks[0].Error, network.ID) {
		t.Fatalf("network converge check = %#v", checks)
	}
	if !strings.Contains(checks[1].Error, media.ID) || !strings.Contains(checks[1].Error, "attached") {
		t.Fatalf("media converge check = %#v", checks[1])
	}
	for _, check := range checks {
		if len(check.Error) > 240 {
			t.Fatalf("converge check message is unbounded: %d bytes", len(check.Error))
		}
	}
}

// A member that is behind on renames must step through every generation Core defines, so
// the access generation it reports is one it actually applied. A changed uplink or VLAN is
// never re-plumbed under running VMs.
func TestPoolNetworkRenameSteps(t *testing.T) {
	current := poolVLAN("vlan-1", "pool-1", "member-1", "Servers", "nexqa", 100, 1)
	same := current
	if steps, err := poolNetworkRenameSteps(current, same); steps != 0 || err != nil {
		t.Fatalf("identical entry = %d, %v", steps, err)
	}
	renamed := current
	renamed.Name, renamed.Generation = "Production", 2
	if steps, err := poolNetworkRenameSteps(current, renamed); steps != 1 || err != nil {
		t.Fatalf("one rename = %d, %v", steps, err)
	}
	behind := current
	behind.Name, behind.Generation = "Production", 4
	if steps, err := poolNetworkRenameSteps(current, behind); steps != 3 || err != nil {
		t.Fatalf("three renames behind = %d, %v", steps, err)
	}
	// Generation 0 means 1 on the wire; a master that never bumped must mean no work.
	zero, zeroed := current, current
	zero.Generation, zeroed.Generation = 0, 1
	if steps, err := poolNetworkRenameSteps(zero, zeroed); steps != 0 || err != nil {
		t.Fatalf("effective generation 1 = %d, %v", steps, err)
	}
	for _, changed := range []protocol.NetworkRegistration{
		poolVLAN("vlan-1", "pool-1", "member-1", "Servers", "nexqa", 101, 2),
		poolVLAN("vlan-1", "pool-1", "member-1", "Servers", "nexqc", 100, 2),
	} {
		if _, err := poolNetworkRenameSteps(current, changed); err == nil || err.Error() != "pool network vlan-1 changed uplink or VLAN; remove and re-add it" {
			t.Fatalf("re-plumb refusal = %v", err)
		}
	}
}

// A rename must reach Core as network-update at the member's own generation, never as a
// second network-register: the network's resource ID stays reserved by the first one.
func TestMemberRenameIssuesNetworkUpdate(t *testing.T) {
	_, member := joinedPair(t)
	held := poolVLAN("vlan-1", "pool-1", member.State.HostID, "Servers", "nexqa", 100, 1)
	writeAppliedPoolNetwork(t, member, held)
	renamed := held
	renamed.Name, renamed.Generation = "Production", 2

	_ = applyPoolNetworkNative(context.Background(), member, renamed)
	updates, registers := 0, 0
	for _, journal := range journalEntries(t, member) {
		switch {
		case journal.Command.Kind == "network-update" && journal.Command.ResourceLifecycle != nil:
			updates++
			v := journal.Command.ResourceLifecycle
			if v.ResourceID != held.ID || v.ExpectedGeneration != 1 || v.Name != "Production" || v.PoolID != member.State.PoolID || v.HostID != member.State.HostID {
				t.Fatalf("rename payload = %#v", v)
			}
		case journal.Command.Kind == "network-register" && journal.Command.Network != nil && journal.Command.Network.ID == held.ID:
			registers++
		}
	}
	if updates != 1 || registers != 1 {
		t.Fatalf("rename journalled %d updates and %d registrations", updates, registers)
	}
}

// A member that applied its renames reports the master's generation, which is what the
// master accepts as proof of convergence. Reporting a generation it never applied would
// show the pool converged while the member still held the old name.
func TestMemberReportsTheMasterGenerationAfterRenames(t *testing.T) {
	master, member := joinedPair(t)
	vlan := poolVLAN("vlan-1", "pool-1", master.State.HostID, "Production", "nexqa", 100, 3)
	writeAppliedPoolNetwork(t, master, vlan)
	held := vlan
	held.HostID, held.Name, held.Generation = member.State.HostID, "Servers", 1
	writeAppliedPoolNetwork(t, member, held)
	writeAppliedPoolNetworkRename(t, member, held.ID, 1, "Staging")
	writeAppliedPoolNetworkRename(t, member, held.ID, 2, "Production")

	local, live := liveMemberNetwork(t, member, held.ID)
	if !live || protocol.NetworkGeneration(local) != protocol.NetworkGeneration(vlan) || local.Name != "Production" {
		t.Fatalf("member network after renames = %#v", local)
	}
	if steps, err := poolNetworkRenameSteps(local, vlan); steps != 0 || err != nil {
		t.Fatalf("converged member still owes %d renames (%v)", steps, err)
	}
	// The master only records an access report at a generation it currently holds.
	client, err := PoolPeerClient(member, masterPeerURL(t, member))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	access := protocol.PoolNetworkAccess{NetworkID: held.ID, HostID: member.State.HostID, Generation: protocol.NetworkGeneration(local), ObservedAt: time.Now().UTC()}
	if err = postPoolNetworkAccess(context.Background(), client, masterPeerURL(t, member), held.ID, access); err != nil {
		t.Fatalf("master refused the converged generation: %v", err)
	}
	stale := access
	stale.Generation = 1
	if err = postPoolNetworkAccess(context.Background(), client, masterPeerURL(t, member), held.ID, stale); err == nil {
		t.Fatal("master accepted an access report at a superseded generation")
	}
}

func masterPeerURL(t *testing.T, member *Store) string {
	t.Helper()
	url, err := PoolMasterURL(member)
	if err != nil {
		t.Fatal(err)
	}
	return url
}

// writeAppliedPoolNetworkRename journals a succeeded rename, as Core records one.
func writeAppliedPoolNetworkRename(t *testing.T, s *Store, id string, expected uint64, name string) {
	t.Helper()
	op := "rename-" + id + "-" + name
	journal, err := json.Marshal(map[string]any{
		"command": protocol.Command{OperationID: op, Kind: "network-update", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
			Resource: "network", Action: "update", ResourceID: id, PoolID: s.State.PoolID, HostID: s.State.HostID,
			ExpectedGeneration: expected, Name: name,
		}},
		"result": protocol.Result{OperationID: op, Status: "succeeded"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.dir, "managed-vms", op+".json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
}

// The master removes a pool resource for the whole pool, so it refuses while any member's
// published VM still references it, and refuses whenever it cannot prove that it knows.
func TestMasterRefusesPoolWideResourceRemoval(t *testing.T) {
	master, member := joinedPair(t)
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	remove := protocol.Command{OperationID: strings.Repeat("a", 64), Kind: "network-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
		Resource: "network", Action: "remove", ResourceID: "vlan-1", PoolID: "pool-1", HostID: master.State.HostID, ExpectedGeneration: 1,
	}}
	removeISO := protocol.Command{OperationID: strings.Repeat("b", 64), Kind: "media-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
		Resource: "media", Action: "remove", ResourceID: "iso-1", PoolID: "pool-1", HostID: master.State.HostID, ExpectedGeneration: 1,
	}}

	// No member has published at this membership generation yet: fail closed.
	if err = BeginPoolResourceRemoval(context.Background(), master, remove); err == nil || err.Error() != "pool VM inventory is incomplete; wait for every member to report before removing this network" {
		t.Fatalf("stale inventory refusal = %v", err)
	}
	if err = BeginPoolResourceRemoval(context.Background(), master, removeISO); err == nil || !strings.HasSuffix(err.Error(), "removing this ISO") {
		t.Fatalf("stale ISO inventory refusal = %v", err)
	}

	vm := protocol.VMCreateSpec{
		ID: "22222222-2222-4222-8222-222222222222", PoolID: "pool-1", HostID: member.State.HostID,
		Name: "web", NICs: []protocol.VMNIC{{NetworkID: "vlan-1"}}, DriversMediaID: "iso-1",
	}
	state := poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{
		member.State.HostID: {HostID: member.State.HostID, Generation: membership.Generation, VMs: []protocol.VMCreateSpec{vm}},
	}}
	if err = savePoolVMs(master, state); err != nil {
		t.Fatal(err)
	}
	if err = BeginPoolResourceRemoval(context.Background(), master, remove); err == nil ||
		err.Error() != "network cannot be removed while used by VM "+vm.ID+" on host "+member.State.HostID {
		t.Fatalf("member VM refusal = %v", err)
	}
	if err = BeginPoolResourceRemoval(context.Background(), master, removeISO); err == nil ||
		err.Error() != "ISO cannot be removed while attached to VM "+vm.ID+" on host "+member.State.HostID {
		t.Fatalf("member ISO refusal = %v", err)
	}
	// A member that drops out of ready still runs the VMs it last published: its stale
	// snapshot keeps holding the network rather than being skipped.
	setMemberStatus := func(status string) {
		t.Helper()
		poolPeerMu.Lock()
		defer poolPeerMu.Unlock()
		peer, err := loadPoolPeer(master)
		if err != nil {
			t.Fatal(err)
		}
		for i := range peer.Membership.Members {
			if peer.Membership.Members[i].HostID == member.State.HostID {
				peer.Membership.Members[i].Status = status
			}
		}
		if err = savePoolPeer(master, peer); err != nil {
			t.Fatal(err)
		}
	}
	setMemberStatus("pending")
	if err = BeginPoolResourceRemoval(context.Background(), master, remove); err == nil || !strings.Contains(err.Error(), "used by VM "+vm.ID) {
		t.Fatalf("a not-ready member's VM stopped holding its network: %v", err)
	}
	setMemberStatus("ready")
	// A rename touches no VM definition, so the same inventory state allows it.
	rename := protocol.Command{OperationID: strings.Repeat("e", 64), Kind: "network-update", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
		Resource: "network", Action: "update", ResourceID: "vlan-1", PoolID: "pool-1",
		HostID: master.State.HostID, ExpectedGeneration: 1, Name: "Production",
	}}
	if err = BeginPoolResourceRemoval(context.Background(), master, rename); err != nil {
		t.Fatalf("master refused a rename: %v", err)
	}

	// The VM stops using the resource: the master stops refusing.
	state.Inventory[member.State.HostID] = poolVMInventory{HostID: member.State.HostID, Generation: membership.Generation}
	if err = savePoolVMs(master, state); err != nil {
		t.Fatal(err)
	}
	if err = BeginPoolResourceRemoval(context.Background(), master, remove); err != nil {
		t.Fatalf("master refused a removal no pool VM uses: %v", err)
	}

	// A member must not accept a Controller-issued removal at all; its own converge loop
	// applies the master's decision through the engine directly.
	memberCommand := remove
	memberCommand.ResourceLifecycle = &protocol.ResourceLifecycleRequest{
		Resource: "network", Action: "remove", ResourceID: "vlan-1", PoolID: "pool-1",
		HostID: member.State.HostID, ExpectedGeneration: 1,
	}
	if err = BeginPoolResourceRemoval(context.Background(), member, memberCommand); err == nil || !strings.Contains(err.Error(), "only the pool master") {
		t.Fatalf("member accepted a Controller-issued removal: %v", err)
	}
}

// A single-host pool is a pool whose only host is its master. It has no member inventory to
// wait for, and Core's local dependency check still protects its own VMs.
func TestSingleHostPoolMasterMayRemove(t *testing.T) {
	master := peerStore(t, "solo-1", "pool-solo")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []protocol.Command{
		{OperationID: strings.Repeat("c", 64), Kind: "network-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
			Resource: "network", Action: "remove", ResourceID: "vlan-1", PoolID: master.State.PoolID, HostID: master.State.HostID, ExpectedGeneration: 1}},
		{OperationID: strings.Repeat("d", 64), Kind: "media-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
			Resource: "media", Action: "remove", ResourceID: "iso-1", PoolID: master.State.PoolID, HostID: master.State.HostID, ExpectedGeneration: 1}},
	} {
		if err := BeginPoolResourceRemoval(context.Background(), master, cmd); err != nil {
			t.Fatalf("single-host pool refused %s: %v", cmd.Kind, err)
		}
	}
}

// The new lifecycle kinds must reach the engine with the host the payload names.
func TestResourceLifecycleCommandRouting(t *testing.T) {
	for _, kind := range []string{"network-remove", "network-update", "media-remove"} {
		if !managedCommand(kind) {
			t.Fatalf("%s is not an accepted managed command", kind)
		}
		cmd := protocol.Command{Kind: kind, ResourceLifecycle: &protocol.ResourceLifecycleRequest{HostID: "host-9"}}
		if commandHost(cmd) != "host-9" {
			t.Fatalf("%s routed to %q", kind, commandHost(cmd))
		}
	}
}
