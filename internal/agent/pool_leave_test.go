// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// leaveFixture gives one host the three pool-owned things an ejected member has to release:
// a pool NFS repository, an ISO adopted from it, and a pool VLAN network. Every record is a
// real managed journal entry, so the teardown runs the engine's real refusals and apply paths.
type leaveFixture struct {
	repo    protocol.StorageRegistration
	media   protocol.Media
	vlan    protocol.NetworkRegistration
	isoPath string
}

func seedLeaveResources(t *testing.T, s *Store) leaveFixture {
	t.Helper()
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	repoDir := t.TempDir()
	// The command carries the client-shaped registration; Core's own resolved mount path is
	// the applied projection, exactly as a real register journals it.
	requested := protocol.StorageRegistration{
		ID: "repo-1", PoolID: s.State.PoolID, HostID: s.State.HostID, Name: "shared",
		Kind: protocol.StorageKindNFS, Generation: 1,
		Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}},
	}
	repo := requested
	repo.Path = repoDir
	entry, err := json.Marshal(map[string]any{
		"command":         protocol.Command{OperationID: "repo-op", Kind: "storage-register", Storage: &requested},
		"result":          protocol.Result{OperationID: "repo-op", Status: "succeeded"},
		"applied_storage": repo,
		"stage":           "verified",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "repo-op.json"), entry, 0600); err != nil {
		t.Fatal(err)
	}

	body := make([]byte, 40<<10)
	copy(body[32769:], "CD001")
	if err := os.MkdirAll(filepath.Join(repoDir, "nex-media"), 0755); err != nil {
		t.Fatal(err)
	}
	isoPath := filepath.Join(repoDir, "nex-media", "iso-1.iso")
	if err := os.WriteFile(isoPath, body, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	media := protocol.Media{ID: "iso-1", PoolID: s.State.PoolID, HostID: s.State.HostID, StorageID: repo.ID,
		Name: "install.iso", SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	// The pool's own ISO, adopted by this member: its record is this host's, the file is the
	// pool's. Releasing the record must never unlink the file.
	if err := vmhost.NewManaged(dir).AdoptSharedMedia(context.Background(), media); err != nil {
		t.Fatal(err)
	}

	vlan := protocol.NetworkRegistration{ID: "vlan-100", PoolID: s.State.PoolID, HostID: s.State.HostID, Name: "Servers",
		Kind: "vlan", Source: protocol.GuestBridge("nexqa", 100), Uplink: "nexqa", VLAN: 100, Generation: 1}
	writeManagedJournal(t, dir, "vlan-op", protocol.Command{OperationID: "vlan-op", Kind: "network-register", Network: &vlan})
	return leaveFixture{repo: repo, media: media, vlan: vlan, isoPath: isoPath}
}

// seedLocalVM journals a VM this host keeps through the eject, holding the pool ISO in its CD
// drive and its NIC on the pool VLAN. Core never deletes it or edits its NICs to get past it.
func seedLocalVM(t *testing.T, s *Store, f leaveFixture) protocol.VMCreateSpec {
	t.Helper()
	vm := protocol.VMCreateSpec{ID: "11111111-1111-4111-8111-111111111111", PoolID: s.State.PoolID, HostID: s.State.HostID,
		Name: "Local", CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"},
		Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks:  []protocol.VMDisk{{StorageID: "local-disk", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}},
		NICs:   []protocol.VMNIC{{NetworkID: f.vlan.ID, Model: "virtio"}},
		Boot:   protocol.VMBoot{Firmware: "bios"}, MediaID: f.media.ID}
	writeManagedJournal(t, filepath.Join(s.dir, "managed-vms"), "local-vm", protocol.Command{OperationID: "local-vm", Kind: "vm-create", VM: &vm})
	return vm
}

func appliedOf(t *testing.T, s *Store) *protocol.ManagedState {
	t.Helper()
	managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(context.Background())
	if managed == nil {
		t.Fatal("managed state is unavailable")
	}
	return managed
}

func liveResourceCounts(t *testing.T, s *Store) (storage, media, networks int) {
	t.Helper()
	managed := appliedOf(t, s)
	for _, entry := range managed.Storage {
		if !entry.Tombstone {
			storage++
		}
	}
	for _, entry := range managed.Media {
		if !entry.Tombstone {
			media++
		}
	}
	for _, entry := range managed.Networks {
		if !entry.Tombstone && protocol.IsPoolSharedNetwork(entry) {
			networks++
		}
	}
	return storage, media, networks
}

// ejectMember runs the real eject on the master and returns the ejected host.
func ejectMember(t *testing.T, lab *transferLab, op string) *Store {
	t.Helper()
	master, ejected := lab.host("master-1"), lab.host("member-1")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand(op, "eject", "member-1", "master-1")); result.Status != "succeeded" {
		t.Fatalf("eject = %#v", result)
	}
	lab.requireSingleMaster("after the eject", "master-1")
	if record := peerState(t, ejected).Transfer; record == nil || record.Role != "ejected" || record.Stage != "leave" {
		t.Fatalf("ejected host record = %#v", peerState(t, ejected).Transfer)
	}
	return ejected
}

// BUG-0356: the eject's teardown ran the repository detach while the ISO adopted from it was
// still recorded, and carried a stable operation ID, so the managed journal replayed the
// stored refusal for ever. The host kept its membership, its mount and its VLAN stanzas, the
// master reported the transfer done, and only wiping the host's agent state recovered it.
func TestEjectedHostReleasesPoolResourcesInDependencyOrder(t *testing.T) {
	lab := newTransferLab(t, 2)
	usePoolNetworkSysfs(t)
	member := lab.host("member-1")
	fixture := seedLeaveResources(t, member)
	if storage, media, networks := liveResourceCounts(t, member); storage != 1 || media != 1 || networks != 1 {
		t.Fatalf("fixture did not apply: storage=%d media=%d networks=%d", storage, media, networks)
	}
	ejected := ejectMember(t, lab, "op-eject-resources")
	oldPool := ejected.State.PoolID
	if !ResumePoolLeave(context.Background(), ejected) {
		t.Fatalf("the ejected host did not re-initialize: %#v", peerState(t, ejected).Transfer)
	}
	if storage, media, networks := liveResourceCounts(t, ejected); storage != 0 || media != 0 || networks != 0 {
		t.Fatalf("pool resources survived the leave: storage=%d media=%d networks=%d", storage, media, networks)
	}
	// The ISO file belongs to the pool, not to this host: only the record leaves.
	if info, err := os.Stat(fixture.isoPath); err != nil || info.Size() != fixture.media.SizeBytes {
		t.Fatalf("the leave unlinked the pool's own ISO file: %v", err)
	}
	if rec := peerState(t, ejected).Transfer; rec != nil && rec.LastError != "" {
		t.Fatalf("the leave completed with an error recorded: %s", rec.LastError)
	}
	if ejected.State.PoolID == oldPool || ejected.State.PoolID == "" {
		t.Fatalf("the ejected host kept its old pool ID %q", ejected.State.PoolID)
	}
	state := peerState(t, ejected)
	if state.Membership.MasterHostID != "member-1" || state.Membership.PoolID != ejected.State.PoolID || len(state.Membership.Members) != 1 {
		t.Fatalf("the ejected host is not the master of its own new pool: %#v", state.Membership)
	}
	if record := state.Transfer; record == nil || record.Stage != "done" || record.Status != "succeeded" {
		t.Fatalf("the leave did not report completion: %#v", record)
	}
	// A re-initialized host with nothing but tombstones left can be offered a join again.
	if err := PoolJoinPrecheck(context.Background(), ejected, nil); err != nil {
		t.Fatalf("a cleanly left host cannot join a pool again: %v", err)
	}
}

// A later signed eject in the same UTC hour must write a new cleanup receipt even when the
// master retained every resource generation. Otherwise Execute replays the first cycle's
// successful removal, whose tombstone was consumed by the intervening rejoin, and storage
// remains blocked by the apparently live ISO.
func TestSignedLeaveCleanupUsesCurrentMembershipAcrossSameHourCycles(t *testing.T) {
	member := peerStore(t, "member-1", "pool-1")
	usePoolNetworkSysfs(t)
	fixture := seedLeaveResources(t, member)
	engine := vmhost.NewManaged(filepath.Join(member.dir, "managed-vms"))
	record := func(generation uint64) *poolTransferRecord {
		return &poolTransferRecord{OperationID: fmt.Sprintf("eject-cycle-%d", generation), Action: "eject", Role: "ejected", TargetHostID: member.State.HostID, MembershipGeneration: generation,
			Notice: &protocol.PoolAuthorityNotice{PoolID: member.State.PoolID, TargetHostID: member.State.HostID, Membership: protocol.PoolMembership{PoolID: member.State.PoolID, Generation: generation}}}
	}
	if first, retry := poolLeaveCleanupOperation(record(25), "media-remove", fixture.media.ID, protocol.MediaGeneration(fixture.media)), poolLeaveCleanupOperation(record(25), "media-remove", fixture.media.ID, protocol.MediaGeneration(fixture.media)); first != retry {
		t.Fatalf("one leave retry changed its cleanup operation: %s != %s", first, retry)
	} else if later := poolLeaveCleanupOperation(record(27), "media-remove", fixture.media.ID, protocol.MediaGeneration(fixture.media)); first == later {
		t.Fatal("later membership reused the earlier leave cleanup operation")
	}
	if err := detachPoolSharedResources(context.Background(), member, engine, record(25)); err != nil {
		t.Fatalf("first signed leave cleanup: %v", err)
	}
	removal := func(resource, id string, generation, membership uint64) string {
		t.Helper()
		for _, entry := range journalEntries(t, member) {
			v := entry.Command.ResourceLifecycle
			matches := v != nil && v.Resource == resource && v.ResourceID == id && v.ExpectedGeneration == generation
			if resource == "storage" && entry.Command.StorageLifecycle != nil {
				s := entry.Command.StorageLifecycle
				matches = s.Action == "detach" && s.RepositoryID == id && s.ExpectedGeneration == generation
			}
			if entry.Result.Status == "succeeded" && matches {
				var raw map[string]any
				body, err := os.ReadFile(filepath.Join(member.dir, "managed-vms", entry.Command.OperationID+".json"))
				if err != nil || json.Unmarshal(body, &raw) != nil {
					t.Fatalf("read %s cleanup: %v", resource, err)
				}
				cleanup, _ := raw["eject_cleanup"].(map[string]any)
				if cleanup["MembershipGeneration"] == float64(membership) {
					return entry.Command.OperationID
				}
			}
		}
		t.Fatalf("missing membership-%d %s cleanup", membership, resource)
		return ""
	}
	storageRemoval := removal("storage", fixture.repo.ID, fixture.repo.Generation, 25)
	mediaRemoval := removal("media", fixture.media.ID, protocol.MediaGeneration(fixture.media), 25)
	networkRemoval := removal("network", fixture.vlan.ID, protocol.NetworkGeneration(fixture.vlan), 25)
	// These are the durable receipts written by the normal rejoin paths. The media receipt
	// rewrites the deterministic shared-adoption operation, as production does.
	storageRequest := fixture.repo
	storageRequest.Path = ""
	storageReceipt := map[string]any{"command": protocol.Command{OperationID: "storage-rejoin", Kind: "storage-register", Storage: &storageRequest}, "result": protocol.Result{OperationID: "storage-rejoin", Status: "succeeded"}, "applied_storage": fixture.repo, "eject_cleanup_reapply": map[string]any{"RemovalOperationID": storageRemoval, "MembershipGeneration": 26}}
	body, err := json.Marshal(storageReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(member.dir, "managed-vms", "storage-rejoin.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	networkReceipt := map[string]any{"command": protocol.Command{OperationID: "network-rejoin", Kind: "network-register", Network: &fixture.vlan}, "result": protocol.Result{OperationID: "network-rejoin", Status: "succeeded"}, "eject_cleanup_reapply": map[string]any{"RemovalOperationID": networkRemoval, "MembershipGeneration": 26}}
	body, err = json.Marshal(networkReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(member.dir, "managed-vms", "network-rejoin.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	mediaFiles, err := filepath.Glob(filepath.Join(member.dir, "managed-vms", "media-*.json"))
	if err != nil || len(mediaFiles) != 1 {
		t.Fatalf("shared-media receipt = %v, %v", mediaFiles, err)
	}
	body, err = os.ReadFile(mediaFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	var mediaReceipt map[string]any
	if err = json.Unmarshal(body, &mediaReceipt); err != nil {
		t.Fatal(err)
	}
	mediaReceipt["eject_cleanup_reapply"] = map[string]any{"RemovalOperationID": mediaRemoval, "MembershipGeneration": 26}
	body, err = json.Marshal(mediaReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(mediaFiles[0], body, 0600); err != nil {
		t.Fatal(err)
	}
	if storage, media, networks := liveResourceCounts(t, member); storage != 1 || media != 1 || networks != 1 {
		t.Fatalf("intervening rejoin did not restore resources: storage=%d media=%d networks=%d", storage, media, networks)
	}
	if err := savePoolTransfer(member, record(27)); err != nil {
		t.Fatal(err)
	}
	if !ResumePoolLeave(context.Background(), member) {
		t.Fatalf("second same-hour signed leave did not reinitialize: %#v", peerState(t, member).Transfer)
	}
	if storage, media, networks := liveResourceCounts(t, member); storage != 0 || media != 0 || networks != 0 {
		t.Fatalf("second leave kept pool resources: storage=%d media=%d networks=%d", storage, media, networks)
	}
	secondStorage := removal("storage", fixture.repo.ID, fixture.repo.Generation, 27)
	secondMedia := removal("media", fixture.media.ID, protocol.MediaGeneration(fixture.media), 27)
	secondNetwork := removal("network", fixture.vlan.ID, protocol.NetworkGeneration(fixture.vlan), 27)
	if secondStorage == storageRemoval || secondMedia == mediaRemoval || secondNetwork == networkRemoval {
		t.Fatalf("a later leave reused cleanup receipts: storage=%t media=%t network=%t", secondStorage == storageRemoval, secondMedia == mediaRemoval, secondNetwork == networkRemoval)
	}
}

// A local VM the ejected host keeps still holds the pool ISO and the pool VLAN. Core never
// deletes it or edits its NICs: the leave stays in its stage, names the VM in Core's own
// words, journals nothing at all, and converges on the very next tick once it is fixed.
func TestLeaveRefusalNamesTheLocalVMJournalsNothingAndConverges(t *testing.T) {
	lab := newTransferLab(t, 2)
	usePoolNetworkSysfs(t)
	member := lab.host("member-1")
	fixture := seedLeaveResources(t, member)
	vm := seedLocalVM(t, member, fixture)
	ejected := ejectMember(t, lab, "op-eject-blocked")
	before := poolJournalEntries(t, ejected)
	for pass := 0; pass < 5; pass++ {
		if ResumePoolLeave(context.Background(), ejected) {
			t.Fatalf("pass %d: the leave completed while a local VM still held pool resources", pass)
		}
		record := peerState(t, ejected).Transfer
		if record == nil || record.Stage != "leave" {
			t.Fatalf("pass %d: leave record = %#v", pass, record)
		}
		if !strings.Contains(record.LastError, fixture.media.ID) || !strings.Contains(record.LastError, vm.ID) {
			t.Fatalf("pass %d: the blocker is not named: %q", pass, record.LastError)
		}
		if n := poolJournalEntries(t, ejected); n != before {
			t.Fatalf("pass %d: a refused leave journalled %d entries (was %d)", pass, n, before)
		}
	}
	// The master's own record is its own work: the membership without that host is written
	// and durable, and that is all it reports. What says whether the host has actually left
	// is the host's own report, which is what the Controller projects per participant.
	if master := PoolTransferReport(lab.host("master-1")); master == nil || master.Stage != "done" {
		t.Fatalf("master transfer = %#v", master)
	}
	// The host reports its own progress honestly while it has not left.
	status := PoolTransferReport(ejected)
	if status == nil || status.Role != "ejected" || status.Stage != "leave" {
		t.Fatalf("reported transfer = %#v", status)
	}
	if status.Status != "pending" && status.Status != "needs_attention" {
		t.Fatalf("a stranded host reported %q", status.Status)
	}
	if !strings.Contains(status.Error, vm.ID) {
		t.Fatalf("the reported blocker does not name the VM: %q", status.Error)
	}
	// The operator detaches the VM from the pool ISO and the pool VLAN. Nothing else changes.
	freed := vm
	freed.MediaID, freed.NICs, freed.Revision = "", nil, 1
	writeManagedJournal(t, filepath.Join(ejected.dir, "managed-vms"), "local-vm-2", protocol.Command{OperationID: "local-vm-2", Kind: "vm-update", VM: &freed})
	if !ResumePoolLeave(context.Background(), ejected) {
		t.Fatalf("the leave did not converge on the next tick: %#v", peerState(t, ejected).Transfer)
	}
	if storage, media, networks := liveResourceCounts(t, ejected); storage != 0 || media != 0 || networks != 0 {
		t.Fatalf("pool resources survived the leave: storage=%d media=%d networks=%d", storage, media, networks)
	}
	if vms := appliedOf(t, ejected).VMs; len(vms) != 1 || vms[0].ID != vm.ID {
		t.Fatalf("the leave touched the host's local VM: %#v", vms)
	}
}

// The bound the hour-pinned operation ID buys the leave, measured the way the pool network
// converge loop measures it: a persistent refusal is a precondition, so a day of ticks mints
// no journal entry at all and the host cannot reach the engine's 10000-entry cap by leaving.
func TestLeaveJournalGrowthIsBoundedOverASimulatedDay(t *testing.T) {
	lab := newTransferLab(t, 2)
	usePoolNetworkSysfs(t)
	member := lab.host("member-1")
	fixture := seedLeaveResources(t, member)
	seedLocalVM(t, member, fixture)
	ejected := ejectMember(t, lab, "op-eject-day")
	before := poolJournalEntries(t, ejected)
	base := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	previous := poolConvergeNow
	t.Cleanup(func() { poolConvergeNow = previous })
	for tick := time.Duration(0); tick < 24*time.Hour; tick += time.Minute {
		poolConvergeNow = func() time.Time { return base.Add(tick) }
		if ResumePoolLeave(context.Background(), ejected) {
			t.Fatalf("the leave completed at %v although the blocker stands", tick)
		}
	}
	if n := poolJournalEntries(t, ejected); n != before {
		t.Fatalf("a day of refused leave ticks journalled %d entries (was %d)", n, before)
	}
}

// Recovery for a host the released build already stranded: its journal holds the failed
// storage-detach under the old stable operation ID and its membership says ejected. It must
// finish by itself on the next ticks after the upgrade, with no manual state edit and no
// master involvement at all.
func TestStrandedEjectedHostConvergesAfterUpgrade(t *testing.T) {
	lab := newTransferLab(t, 2)
	usePoolNetworkSysfs(t)
	member := lab.host("member-1")
	fixture := seedLeaveResources(t, member)
	ejected := ejectMember(t, lab, "op-eject-stranded")
	// Exactly what the released build left behind: the leave's stable-ID storage-detach,
	// stored failed because the adopted ISO still referenced the repository.
	digest := sha256.Sum256([]byte("pool-leave\x00storage\x00" + fixture.repo.ID))
	op := "leave-" + fmt.Sprintf("%x", digest[:])[:32]
	entry, err := json.Marshal(map[string]any{
		"command": protocol.Command{OperationID: op, Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{
			Action: "detach", RepositoryID: fixture.repo.ID, PoolID: fixture.repo.PoolID, HostID: fixture.repo.HostID, ExpectedGeneration: 1}},
		"result": protocol.Result{OperationID: op, Status: "failed", Error: "storage cannot detach while used by ISO " + fixture.media.ID},
		"stage":  "validating",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(ejected.dir, "managed-vms", op+".json"), entry, 0600); err != nil {
		t.Fatal(err)
	}
	// No master: the stranded host finishes on its own tick.
	if !ResumePoolLeave(context.Background(), ejected) {
		t.Fatalf("the stranded host did not converge: %#v", peerState(t, ejected).Transfer)
	}
	if storage, media, networks := liveResourceCounts(t, ejected); storage != 0 || media != 0 || networks != 0 {
		t.Fatalf("pool resources survived the recovery: storage=%d media=%d networks=%d", storage, media, networks)
	}
	if err := PoolJoinPrecheck(context.Background(), ejected, nil); err != nil {
		t.Fatalf("a recovered host cannot join a pool again: %v", err)
	}
}

// The master refuses the eject while the target keeps a local VM that depends on a resource
// leaving with the pool, from its own per-member inventory, so the operator fixes it before
// the eject instead of discovering a stranded host afterwards.
func TestEjectIsRefusedWhileALocalVMDependsOnAPoolResource(t *testing.T) {
	lab := newTransferLab(t, 2)
	usePoolNetworkSysfs(t)
	master, member := lab.host("master-1"), lab.host("member-1")
	fixture := seedLeaveResources(t, master)
	vm := protocol.VMCreateSpec{ID: "22222222-2222-4222-8222-222222222222", PoolID: member.State.PoolID, HostID: member.State.HostID,
		Name: "Local", CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"},
		Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		NICs:   []protocol.VMNIC{{NetworkID: fixture.vlan.ID, Model: "virtio"}},
		Boot:   protocol.VMBoot{Firmware: "bios"}}
	writePoolVMInventory(t, master, member.State.HostID, []protocol.VMCreateSpec{vm})
	result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-refuse-local", "eject", "member-1", "master-1"))
	if result.Status == "succeeded" || !strings.Contains(result.Error, vm.ID) || !strings.Contains(result.Error, fixture.vlan.ID) {
		t.Fatalf("eject with a dependent local VM = %#v", result)
	}
	lab.requireSingleMaster("after a refused eject", "master-1")
	if record := peerState(t, master).Transfer; record != nil && record.Frozen {
		t.Fatalf("a refused eject left the authority frozen: %#v", record)
	}
	// The same VM on a network the pool does not own is no reason to refuse.
	free := vm
	free.NICs = []protocol.VMNIC{{NetworkID: "local-bridge", Model: "virtio"}}
	free.Disks = []protocol.VMDisk{{StorageID: "local-disk", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}
	if err := os.MkdirAll(filepath.Join(member.dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	writeManagedJournal(t, filepath.Join(member.dir, "managed-vms"), "local-only-vm", protocol.Command{OperationID: "local-only-vm", Kind: "vm-create", VM: &free})
	writePoolVMInventory(t, master, member.State.HostID, []protocol.VMCreateSpec{free})
	writePoolVMRecord(t, master, poolVMRecord{PoolID: free.PoolID, VMID: free.ID, HostID: free.HostID, State: "stopped", Epoch: 1, Spec: free})
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-allow-local", "eject", "member-1", "master-1")); result.Status != "succeeded" {
		t.Fatalf("eject with an independent local VM = %#v", result)
	}
	if _, kept := poolVMRecords(t, master)[free.ID]; kept {
		t.Fatal("local VM owner record survived the eject")
	}
	if !ResumePoolLeave(context.Background(), member) {
		t.Fatalf("local VM eject did not reinitialize: %#v", peerState(t, member).Transfer)
	}
	if vms := appliedOf(t, member).VMs; len(vms) != 1 || vms[0].ID != free.ID || vms[0].PoolID != member.State.PoolID || vms[0].HostID != member.State.HostID {
		t.Fatalf("local VM was not retained in the new pool: %#v", vms)
	}
}

func writePoolVMInventory(t *testing.T, s *Store, host string, vms []protocol.VMCreateSpec) {
	t.Helper()
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if state.Inventory == nil {
		state.Inventory = map[string]poolVMInventory{}
	}
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	state.Inventory[host] = poolVMInventory{HostID: host, Generation: membership.Generation, VMs: vms, ObservedAt: time.Now().UTC()}
	if err = savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
}
