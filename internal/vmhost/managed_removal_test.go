// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// removalEngine journals a succeeded VLAN registration without touching host links: the bridge
// name is one no host carries, so every teardown step here is the idempotent "already gone" one.
// TestGuestVLANNetns covers the same removal against real links.
func newRemovalEngine(t *testing.T) *ManagedEngine {
	t.Helper()
	e := &ManagedEngine{stateDir: filepath.Join(t.TempDir(), "state"), networkDir: t.TempDir()}
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	return e
}

func removalEngine(t *testing.T) (*ManagedEngine, protocol.NetworkRegistration) {
	t.Helper()
	e := newRemovalEngine(t)
	n := vlanNet("nexq", 100)
	n.PoolID, n.HostID = "pool", "host"
	if err := e.save(managedJournal{
		Command: protocol.Command{OperationID: "op-net", Kind: "network-register", Network: &n},
		Result:  protocol.Result{OperationID: "op-net", Status: "succeeded"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeGuestBridgeFile(n, e.networkDir); err != nil {
		t.Fatal(err)
	}
	return e, n
}

func appliedNetworks(t *testing.T, e *ManagedEngine) []protocol.NetworkRegistration {
	t.Helper()
	out, err := e.AppliedNetworks()
	if err != nil {
		t.Fatalf("applied networks: %v", err)
	}
	return out
}

func appliedMedia(t *testing.T, e *ManagedEngine) []protocol.Media {
	t.Helper()
	out, err := e.AppliedMedia()
	if err != nil {
		t.Fatalf("applied media: %v", err)
	}
	return out
}

func removeNetwork(op string, n protocol.NetworkRegistration, generation uint64) protocol.Command {
	return protocol.Command{OperationID: op, Kind: "network-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
		Resource: "network", Action: "remove", ResourceID: n.ID, PoolID: n.PoolID, HostID: n.HostID, ExpectedGeneration: generation,
	}}
}

func renameNetwork(op string, n protocol.NetworkRegistration, generation uint64, name string) protocol.Command {
	return protocol.Command{OperationID: op, Kind: "network-update", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
		Resource: "network", Action: "update", ResourceID: n.ID, PoolID: n.PoolID, HostID: n.HostID, ExpectedGeneration: generation, Name: name,
	}}
}

func removeMedia(op, id string, generation uint64) protocol.Command {
	return protocol.Command{OperationID: op, Kind: "media-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
		Resource: "media", Action: "remove", ResourceID: id, PoolID: "pool", HostID: "host", ExpectedGeneration: generation,
	}}
}

func succeed(t *testing.T, e *ManagedEngine, c protocol.Command) protocol.Result {
	t.Helper()
	r, err := e.Execute(context.Background(), c, nil)
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("%s: %v %+v", c.Kind, err, r)
	}
	return r
}

func refuse(t *testing.T, e *ManagedEngine, c protocol.Command, want string) {
	t.Helper()
	r, err := e.Execute(context.Background(), c, nil)
	if err != nil || r.Status == "succeeded" || !strings.Contains(r.Error, want) {
		t.Fatalf("%s: want refusal %q, got %v %+v", c.Kind, want, err, r)
	}
}

func TestNetworkRemoveTombstonesAndStopsRebuild(t *testing.T) {
	e, n := removalEngine(t)
	file := filepath.Join(e.networkDir, "interfaces.d", "nex-"+n.Source)
	succeed(t, e, removeNetwork("op-remove", n, 1))

	applied := appliedNetworks(t, e)
	if len(applied) != 1 || applied[0].ID != n.ID || !applied[0].Tombstone || applied[0].Generation != 2 {
		t.Fatalf("applied networks after removal: %+v", applied)
	}
	state, _ := e.Applied(context.Background())
	if len(state.Networks) != 1 || !state.Networks[0].Tombstone {
		t.Fatalf("reported state after removal: %+v", state.Networks)
	}
	js, _ := e.readable()
	if _, err := managedNetwork(js, n.ID); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("removed network still resolves: %v", err)
	}
	if exists(file) {
		t.Fatal("interfaces.d stanza survived removal")
	}
	// EnsureAppliedNetwork must report it gone and rebuild nothing.
	if err := e.EnsureAppliedNetwork(context.Background(), n.ID); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("EnsureAppliedNetwork rebuilt a removed network: %v", err)
	}
	if exists(file) || exists(sysNet+n.Source) {
		t.Fatal("EnsureAppliedNetwork wrote host configuration for a removed network")
	}
	// Replay returns the stored result; the same ID with different input is refused.
	if r := succeed(t, e, removeNetwork("op-remove", n, 1)); r.OperationID != "op-remove" {
		t.Fatalf("replay: %+v", r)
	}
	refuse(t, e, removeNetwork("op-remove", n, 7), "different input")
	// A second removal under a fresh operation ID finds nothing left to remove; the
	// generation refusal is what a stale expectation on a LIVE network gets (see the
	// rename test), and either way no second teardown runs.
	refuse(t, e, removeNetwork("op-remove-2", n, 1), "network is not registered on this host")
}

func TestNetworkRemoveRefusedWhileVMUsesIt(t *testing.T) {
	e, n := removalEngine(t)
	v := protocol.VMCreateSpec{
		ID: "12345678-1234-4234-8234-123456789012", PoolID: "pool", HostID: "host", Name: "guest",
		CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: "st-1", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}},
		NICs:  []protocol.VMNIC{{NetworkID: n.ID, Model: "virtio"}}, Boot: protocol.VMBoot{Firmware: "bios"},
	}
	// A stopped VM counts: the definition still names the network.
	if err := e.save(managedJournal{
		Command: protocol.Command{OperationID: "op-vm", Kind: "vm-create", VM: &v},
		Result:  protocol.Result{OperationID: "op-vm", Status: "succeeded"},
	}); err != nil {
		t.Fatal(err)
	}
	refuse(t, e, removeNetwork("op-remove", n, 1), "network cannot be removed while used by VM "+v.ID)
	if got := e.NetworkDependencies(n.ID); len(got) != 1 || got[0] != v.ID {
		t.Fatalf("precheck dependencies: %+v", got)
	}
	if applied := appliedNetworks(t, e); len(applied) != 1 || applied[0].Tombstone {
		t.Fatalf("refused removal must keep the network: %+v", applied)
	}
	// Deleting the VM releases it.
	if err := e.save(managedJournal{
		Command: protocol.Command{OperationID: "op-del", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: v.ID, PoolID: "pool", HostID: "host"}},
		Result:  protocol.Result{OperationID: "op-del", Status: "succeeded"},
	}); err != nil {
		t.Fatal(err)
	}
	succeed(t, e, removeNetwork("op-remove-2", n, 1))
}

func TestNetworkRenameKeepsEverythingElse(t *testing.T) {
	e, n := removalEngine(t)
	file := filepath.Join(e.networkDir, "interfaces.d", "nex-"+n.Source)
	before, _ := os.ReadFile(file)
	succeed(t, e, renameNetwork("op-rename", n, 1, "Guests"))
	applied := appliedNetworks(t, e)
	if len(applied) != 1 {
		t.Fatalf("applied networks: %+v", applied)
	}
	got := applied[0]
	if got.Name != "Guests" || got.Generation != 2 || got.Tombstone {
		t.Fatalf("rename result: %+v", got)
	}
	if got.Kind != n.Kind || got.Source != n.Source || got.Uplink != n.Uplink || got.VLAN != n.VLAN || got.ID != n.ID {
		t.Fatalf("rename changed more than the name: %+v", got)
	}
	after, _ := os.ReadFile(file)
	if !strings.Contains(string(after), `"Guests"`) || !strings.Contains(string(after), "bridge_ports "+guestPort(n)) {
		t.Fatalf("stanza after rename:\n%s", after)
	}
	renamed := n
	renamed.Name = "Guests"
	if string(after) != interfacesFile(renamed) || strings.Count(string(after), "\n") != strings.Count(string(before), "\n") {
		t.Fatalf("rename rewrote more than the header:\nbefore %s\nafter %s", before, after)
	}
	// Uplink and VLAN cannot be expressed in the payload, and a second registration of the
	// same ID with a different VLAN is refused by the permanent resource reservation.
	other := vlanNet("nexq", 200)
	other.PoolID, other.HostID = "pool", "host"
	refuse(t, e, protocol.Command{OperationID: "op-revlan", Kind: "network-register", Network: &other}, "resource ID already reserved")
	// A rename at the superseded generation is refused, and a removal must now use 2.
	refuse(t, e, renameNetwork("op-rename-2", n, 1, "Again"), "network generation is no longer current")
	refuse(t, e, removeNetwork("op-remove", n, 1), "network generation is no longer current")
	succeed(t, e, removeNetwork("op-remove-2", n, 2))
	if applied := appliedNetworks(t, e); len(applied) != 1 || !applied[0].Tombstone || applied[0].Generation != 3 {
		t.Fatalf("tombstone after rename: %+v", applied)
	}
}

func TestOnlyVLANNetworksAreRemovable(t *testing.T) {
	e := newRemovalEngine(t)
	n := protocol.NetworkRegistration{ID: "net-br", PoolID: "pool", HostID: "host", Name: "Default", Kind: "bridge", Source: "br0"}
	if err := e.save(managedJournal{
		Command: protocol.Command{OperationID: "op-net", Kind: "network-register", Network: &n},
		Result:  protocol.Result{OperationID: "op-net", Status: "succeeded"},
	}); err != nil {
		t.Fatal(err)
	}
	refuse(t, e, removeNetwork("op-remove", n, 1), "only pool VLAN networks can be removed")
	refuse(t, e, renameNetwork("op-rename", n, 1, "Other"), "only pool VLAN networks can be removed")
	if applied := appliedNetworks(t, e); len(applied) != 1 || applied[0].Tombstone {
		t.Fatalf("host-automatic network must survive: %+v", applied)
	}
	refuse(t, e, removeNetwork("op-missing", protocol.NetworkRegistration{ID: "net-none", PoolID: "pool", HostID: "host"}, 1), "network is not registered on this host")
}

func mediaEngine(t *testing.T) (*ManagedEngine, protocol.Media, string) {
	t.Helper()
	e := newRemovalEngine(t)
	path := filepath.Join(t.TempDir(), "debian.iso")
	if err := os.WriteFile(path, []byte("iso"), 0644); err != nil {
		t.Fatal(err)
	}
	// An import journalled before Media.Generation existed: zero must read as 1.
	m := protocol.Media{ID: "iso-1", PoolID: "pool", HostID: "host", StorageID: "st-1", Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("ab", 32)}
	if err := e.save(managedJournal{
		Command:   protocol.Command{OperationID: "op-iso", Kind: "iso-import", Media: &m},
		Result:    protocol.Result{OperationID: "op-iso", Status: "succeeded"},
		MediaPath: path,
	}); err != nil {
		t.Fatal(err)
	}
	return e, m, path
}

func TestMediaRemoveUnlinksAndTombstones(t *testing.T) {
	e, m, path := mediaEngine(t)
	succeed(t, e, removeMedia("op-remove", m.ID, 1))
	if exists(path) {
		t.Fatal("published ISO survived removal")
	}
	applied := appliedMedia(t, e)
	if len(applied) != 1 || applied[0].ID != m.ID || !applied[0].Tombstone || applied[0].Generation != 2 {
		t.Fatalf("applied media after removal: %+v", applied)
	}
	state, _ := e.Applied(context.Background())
	if len(state.Media) != 1 || !state.Media[0].Tombstone {
		t.Fatalf("reported media: %+v", state.Media)
	}
	js, _ := e.readable()
	if _, _, err := managedMedia(js, m.ID); err == nil {
		t.Fatal("removed ISO still resolves")
	}
	if _, _, err := publishedMediaPath(js, m.ID); err == nil {
		t.Fatal("removed ISO still publishes a path")
	}
	refuse(t, e, removeMedia("op-remove-2", m.ID, 1), "ISO is not imported on this host")
}

func TestMediaRemoveTreatsMissingFileAsDone(t *testing.T) {
	e, m, path := mediaEngine(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	succeed(t, e, removeMedia("op-remove", m.ID, 1))
	if applied := appliedMedia(t, e); len(applied) != 1 || !applied[0].Tombstone {
		t.Fatalf("applied media: %+v", applied)
	}
}

func TestMediaRemoveRefusedWhileAttached(t *testing.T) {
	for _, field := range []string{"media", "drivers"} {
		t.Run(field, func(t *testing.T) {
			e, m, path := mediaEngine(t)
			v := protocol.VMCreateSpec{
				ID: "12345678-1234-4234-8234-123456789012", PoolID: "pool", HostID: "host", Name: "guest",
				CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
				Disks: []protocol.VMDisk{{StorageID: "st-1", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}},
				Boot:  protocol.VMBoot{Firmware: "bios"},
			}
			if field == "media" {
				v.MediaID = m.ID
			} else {
				v.DriversMediaID = m.ID
			}
			if err := e.save(managedJournal{
				Command: protocol.Command{OperationID: "op-vm", Kind: "vm-create", VM: &v},
				Result:  protocol.Result{OperationID: "op-vm", Status: "succeeded"},
			}); err != nil {
				t.Fatal(err)
			}
			refuse(t, e, removeMedia("op-remove", m.ID, 1), "ISO cannot be removed while attached to VM "+v.ID)
			if !exists(path) {
				t.Fatal("refused removal unlinked the ISO")
			}
			if got := e.MediaDependencies(m.ID); len(got) != 1 || got[0] != v.ID {
				t.Fatalf("precheck dependencies: %+v", got)
			}
		})
	}
}

// Journals written before removal existed carry no tombstone and no media generation; they
// must still project as live resources at generation 1.
func TestOldFormatJournalStillProjects(t *testing.T) {
	e, n := removalEngine(t)
	if _, m, _ := mediaEngine(t); m.Generation != 0 {
		t.Fatal("fixture must be a pre-generation import")
	}
	js, _ := e.readable()
	live := networkProjection(js)
	if got, ok := live[n.ID]; !ok || got.Generation != 0 || protocol.NetworkGeneration(got) != 1 || got.Name != n.Name {
		t.Fatalf("legacy network projection: %+v %v", got, ok)
	}
	if len(networkTombstones(js)) != 0 || len(mediaTombstones(js)) != 0 {
		t.Fatal("legacy journal must hold no tombstones")
	}
	// Generation 1 is what a removal must present for such a record.
	succeed(t, e, removeNetwork("op-remove", n, 1))
}

// A removed network releases its bridge name: the same uplink and VLAN must be registrable
// again under a NEW ID. The same ID cannot come back — Execute reserves network:<id> forever —
// so re-adding is always a fresh registration.
func TestReRegisterAfterRemoval(t *testing.T) {
	e, n := removalEngine(t)
	succeed(t, e, removeNetwork("op-remove", n, 1))
	js, _ := e.readable()
	fresh := n
	fresh.ID, fresh.Name = "net-2", "Servers again"
	if err := registrationConflict(js, protocol.Command{OperationID: "op-net-2", Kind: "network-register", Network: &fresh}); err != nil {
		t.Fatalf("tombstoned network blocked a fresh registration: %v", err)
	}
	same := n
	refuse(t, e, protocol.Command{OperationID: "op-net-again", Kind: "network-register", Network: &same}, "resource ID already reserved")
	// A live network still holds its bridge name against another ID.
	e2, live := removalEngine(t)
	js2, _ := e2.readable()
	other := live
	other.ID = "net-3"
	if err := registrationConflict(js2, protocol.Command{OperationID: "op-net-3", Kind: "network-register", Network: &other}); err == nil {
		t.Fatal("a live network must still hold its bridge name")
	}
}

func TestRemoveGuestBridgeOwnershipProof(t *testing.T) {
	dir := t.TempDir()
	n := vlanNet("nexq", 100)
	file := filepath.Join(dir, "interfaces.d", "nex-"+n.Source)
	// No file and no bridge: already removed.
	if err := removeGuestBridge(context.Background(), n, dir); err != nil {
		t.Fatalf("absent network: %v", err)
	}
	if err := writeGuestBridgeFile(n, dir); err != nil {
		t.Fatal(err)
	}
	// A file written by something else is not proof, whatever it says.
	if err := os.WriteFile(file, []byte("# operator's own bridge\nauto nexqv100\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := removeGuestBridge(context.Background(), n, dir); err == nil || !strings.Contains(err.Error(), "was not created by Nex") {
		t.Fatalf("foreign stanza accepted: %v", err)
	}
	// A Nex file for a different network ID on the same bridge name is not proof either.
	other := n
	other.ID = "net-9"
	if err := os.WriteFile(file, []byte(interfacesFile(other)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := removeGuestBridge(context.Background(), n, dir); err == nil || !strings.Contains(err.Error(), "was not created by Nex") {
		t.Fatalf("stanza for another network accepted: %v", err)
	}
	// Our own file, renamed since: the name is not part of the ownership proof.
	renamed := n
	renamed.Name = "Renamed"
	if err := os.WriteFile(file, []byte(interfacesFile(renamed)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := removeGuestBridge(context.Background(), n, dir); err != nil {
		t.Fatalf("own renamed stanza refused: %v", err)
	}
	if exists(file) {
		t.Fatal("stanza survived removal")
	}
	// Non-VLAN registrations never reach the native path.
	if err := removeGuestBridge(context.Background(), protocol.NetworkRegistration{ID: "x", Kind: "bridge", Source: "br0"}, dir); err == nil {
		t.Fatal("non-VLAN teardown accepted")
	}
}

// The ownership proof guards the one destructive network operation Core has, and the operator
// types the name it embeds. A network that claims a removed network's bridge name may be given
// a name containing the victim's ownership text; a substring proof would then hand the victim's
// removal the crafted network's live bridge and stanza.
func TestOwnershipProofRejectsANameThatEmbedsAnotherNetwork(t *testing.T) {
	dir := t.TempDir()
	victim := vlanNet("nexq", 100)
	crafted := victim
	crafted.ID = "net-crafted"
	crafted.Name = "x" + interfacesOwnership(victim)
	if err := writeGuestBridgeFile(crafted, dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "interfaces.d", "nex-"+crafted.Source)
	header, _, _ := strings.Cut(string(mustRead(t, file)), "\n")
	if !strings.Contains(header, interfacesOwnership(victim)) {
		t.Fatalf("fixture no longer embeds the victim's ownership text: %s", header)
	}
	if err := removeGuestBridge(context.Background(), victim, dir); err == nil || !strings.Contains(err.Error(), "was not created by Nex") {
		t.Fatalf("a crafted name passed another network's ownership proof: %v", err)
	}
	if !exists(file) {
		t.Fatal("the crafted network's stanza was deleted by another network's removal")
	}
	// The crafted network owns its own file, before and after a rename rewrites the header.
	if err := removeGuestBridge(context.Background(), crafted, dir); err != nil {
		t.Fatalf("own stanza refused: %v", err)
	}
	renamed := crafted
	renamed.Name = "Renamed" + interfacesOwnership(victim)
	if err := writeGuestBridgeFile(renamed, dir); err != nil {
		t.Fatal(err)
	}
	if err := removeGuestBridge(context.Background(), crafted, dir); err != nil {
		t.Fatalf("own renamed stanza refused: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A peer list a member reads absence from must never be served short. One unreadable entry
// makes every projection a subset of what this host holds, so the list projections fail while
// the host's own inventory report keeps working and surfaces the same condition as a check.
func TestAppliedListsFailClosedOnAnIncompleteJournal(t *testing.T) {
	e, n := removalEngine(t)
	if err := os.WriteFile(filepath.Join(e.stateDir, "corrupt.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AppliedNetworks(); !errors.Is(err, ErrManagedJournalIncomplete) {
		t.Fatalf("AppliedNetworks on an unreadable journal = %v", err)
	}
	if _, err := e.AppliedMedia(); !errors.Is(err, ErrManagedJournalIncomplete) {
		t.Fatalf("AppliedMedia on an unreadable journal = %v", err)
	}
	if err := e.CheckNetworkRemoval(context.Background(), n.ID); !errors.Is(err, ErrManagedJournalIncomplete) {
		t.Fatalf("CheckNetworkRemoval on an unreadable journal = %v", err)
	}
	if _, err := e.NetworkRemoved(n.ID); !errors.Is(err, ErrManagedJournalIncomplete) {
		t.Fatalf("NetworkRemoved on an unreadable journal = %v", err)
	}
	// The host's own report must not die on one bad file: it still lists the network and
	// says so through the managed-journal check.
	state, checks := e.Applied(context.Background())
	if len(state.Networks) != 1 || state.Networks[0].ID != n.ID {
		t.Fatalf("inventory report lost its networks over one bad entry: %+v", state.Networks)
	}
	if !slices.ContainsFunc(checks, func(c protocol.Check) bool {
		return c.Name == "managed-journal" && c.Status == "failed"
	}) {
		t.Fatalf("managed-journal check = %+v", checks)
	}
}

// On pool-shared storage the published ISO is the pool's only copy. A member's record is an
// adoption of the file the importing host wrote, so its removal drops the record and leaves the
// file: one member reaching a wrong conclusion must not destroy the ISO for the whole pool.
func TestMediaRemoveUnlinksOnlyWhatThisHostImported(t *testing.T) {
	for _, adopted := range []bool{false, true} {
		name := map[bool]string{false: "imported here", true: "adopted from shared storage"}[adopted]
		t.Run(name, func(t *testing.T) {
			e := newRemovalEngine(t)
			path := filepath.Join(t.TempDir(), "debian.iso")
			if err := os.WriteFile(path, []byte("iso"), 0644); err != nil {
				t.Fatal(err)
			}
			m := protocol.Media{ID: "iso-1", PoolID: "pool", HostID: "host", StorageID: "st-1", Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("ab", 32)}
			op := "op-iso"
			if adopted {
				op = sharedMediaAdoptionOperation(m.ID)
			}
			if err := e.save(managedJournal{
				Command:   protocol.Command{OperationID: op, Kind: "iso-import", Media: &m},
				Result:    protocol.Result{OperationID: op, Status: "succeeded"},
				MediaPath: path,
			}); err != nil {
				t.Fatal(err)
			}
			succeed(t, e, removeMedia("op-remove", m.ID, 1))
			if exists(path) == !adopted {
				t.Fatalf("adopted=%v left the shared ISO file present=%v", adopted, exists(path))
			}
			// Either way the local record is gone, which is all convergence needs.
			if applied := appliedMedia(t, e); len(applied) != 1 || !applied[0].Tombstone {
				t.Fatalf("applied media after removal: %+v", applied)
			}
		})
	}
}

// The converge dry run must report every refusal the operation itself would hit, or a refusal
// it misses is journalled on every probe tick until the retained-entry cap bricks the host.
func TestCheckRemovalReportsTheSameRefusalsAsTheOperation(t *testing.T) {
	ctx := context.Background()
	t.Run("not created by Nex", func(t *testing.T) {
		e, n := removalEngine(t)
		file := filepath.Join(e.networkDir, "interfaces.d", "nex-"+n.Source)
		if err := os.WriteFile(file, []byte("# operator's own bridge\nauto "+n.Source+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		want := "was not created by Nex"
		if err := e.CheckNetworkRemoval(ctx, n.ID); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("dry run = %v", err)
		}
		refuse(t, e, removeNetwork("op-remove", n, 1), want)
		if !exists(file) {
			t.Fatal("a refused removal deleted the stanza")
		}
	})
	t.Run("used by a VM", func(t *testing.T) {
		e, n := removalEngine(t)
		v := protocol.VMCreateSpec{
			ID: "12345678-1234-4234-8234-123456789012", PoolID: "pool", HostID: "host", Name: "guest",
			CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
			Disks: []protocol.VMDisk{{StorageID: "st-1", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}},
			NICs:  []protocol.VMNIC{{NetworkID: n.ID, Model: "virtio"}}, Boot: protocol.VMBoot{Firmware: "bios"},
		}
		if err := e.save(managedJournal{
			Command: protocol.Command{OperationID: "op-vm", Kind: "vm-create", VM: &v},
			Result:  protocol.Result{OperationID: "op-vm", Status: "succeeded"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.CheckNetworkRemoval(ctx, n.ID); err == nil || !strings.Contains(err.Error(), "used by VM "+v.ID) {
			t.Fatalf("dry run = %v", err)
		}
	})
	t.Run("nothing to remove", func(t *testing.T) {
		e, n := removalEngine(t)
		if err := e.CheckNetworkRemoval(ctx, n.ID); err != nil {
			t.Fatalf("a removable network was refused: %v", err)
		}
		succeed(t, e, removeNetwork("op-remove", n, 1))
		if err := e.CheckNetworkRemoval(ctx, n.ID); err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Fatalf("dry run after removal = %v", err)
		}
		removed, err := e.NetworkRemoved(n.ID)
		if err != nil || !removed {
			t.Fatalf("NetworkRemoved after removal = %v %v", removed, err)
		}
	})
	t.Run("media", func(t *testing.T) {
		e, m, _ := mediaEngine(t)
		if err := e.CheckMediaRemoval(ctx, m.ID); err != nil {
			t.Fatalf("a removable ISO was refused: %v", err)
		}
		succeed(t, e, removeMedia("op-remove", m.ID, 1))
		if err := e.CheckMediaRemoval(ctx, m.ID); err == nil || !strings.Contains(err.Error(), "not imported on this host") {
			t.Fatalf("dry run after removal = %v", err)
		}
		removed, err := e.MediaRemoved(m.ID)
		if err != nil || !removed {
			t.Fatalf("MediaRemoved after removal = %v %v", removed, err)
		}
	})
}

func TestVLANPortInUseByAnotherStanza(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "interfaces.d")
	first := vlanNet("nexq", 100)
	second := first
	second.ID, second.Name = "net-2", "Second"
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nex-"+first.Source), []byte(interfacesFile(first)), 0644); err != nil {
		t.Fatal(err)
	}
	used, err := vlanPortInUse(dir, "nex-"+first.Source, guestPort(first))
	if err != nil || used {
		t.Fatalf("only our own stanza names the port: %v %v", used, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nex-other"), []byte(interfacesFile(second)), 0644); err != nil {
		t.Fatal(err)
	}
	used, err = vlanPortInUse(dir, "nex-"+first.Source, guestPort(first))
	if err != nil || !used {
		t.Fatalf("a second Nex stanza must keep the VLAN device: %v %v", used, err)
	}
	used, err = vlanPortInUse(filepath.Join(dir, "absent"), "nex-x", "nexq.100")
	if err != nil || used {
		t.Fatalf("absent directory: %v %v", used, err)
	}
}

func TestResourceLifecycleCommandValidation(t *testing.T) {
	n := vlanNet("nexq", 100)
	good := removeNetwork("op-1", n, 1)
	if err := validateManaged(good); err != nil {
		t.Fatal(err)
	}
	if managedResourceID(good) != "network-lifecycle:net-1:op-1" {
		t.Fatalf("resource slot %q", managedResourceID(good))
	}
	if got := managedResourceID(removeMedia("op-2", "iso-1", 1)); got != "media-lifecycle:iso-1:op-2" {
		t.Fatalf("media resource slot %q", got)
	}
	mismatched := good
	mismatched.Kind = "media-remove"
	twoPayloads := good
	twoPayloads.Network = &n
	noPayload := protocol.Command{OperationID: "op-3", Kind: "network-remove"}
	for name, c := range map[string]protocol.Command{
		"kind does not match payload": mismatched,
		"two payloads":                twoPayloads,
		"no payload":                  noPayload,
	} {
		if validateManaged(c) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A member's first attempt at a pool VLAN commonly fails because its uplink or trunk is not
// ready yet. That failure must not reserve the network's ID for ever: the next converge pass,
// under its next operation ID, has to reach the real apply again and report the real cause.
func TestFailedPoolVLANRegistrationCanBeRetried(t *testing.T) {
	e := newRemovalEngine(t)
	n := vlanNet("nexmissing", 100)
	n.PoolID, n.HostID = "pool", "host"
	first, _ := e.Execute(context.Background(), protocol.Command{OperationID: "attempt-1", Kind: "network-register", Network: &n}, nil)
	if first.Status != "failed" {
		t.Fatalf("registration on a missing uplink = %+v", first)
	}
	second, _ := e.Execute(context.Background(), protocol.Command{OperationID: "attempt-2", Kind: "network-register", Network: &n}, nil)
	if second.Status != "failed" || strings.Contains(second.Error, "already reserved") || second.Error != first.Error {
		t.Fatalf("retry after a failed registration = %+v, first = %+v", second, first)
	}
}
