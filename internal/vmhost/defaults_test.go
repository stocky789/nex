// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestDefaultNetworksFollowBridgesOnPhysicalPorts(t *testing.T) {
	nics := []protocol.NIC{
		{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"},
		{Name: "nex0.15", Kind: "vlan", Parent: "nex0", Master: "nex0v15"}, {Name: "nex0v15", Kind: "bridge"},
		{Name: "vnet0", Kind: "virtual", Master: "br0"},
		{Name: "enx001122334455", Kind: "physical", Master: "br1"}, {Name: "br1", Kind: "bridge"},
		{Name: "bond0", Kind: "bond", Master: "br2"}, {Name: "br2", Kind: "bridge"},
	}
	got := DefaultNetworks(nics)
	if len(got) != 2 || got[0].Name != "nex0 untagged" || got[0].Source != "br0" || got[0].Uplink != "nex0" || got[0].Kind != "bridge" || got[1].Name != "bond0 untagged" || got[1].Source != "br2" {
		t.Fatalf("defaults %+v", got)
	}
}

// startEngine is an engine whose state directory exists, as it would after any command.
func startEngine(t *testing.T) *ManagedEngine {
	t.Helper()
	e := NewManaged(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	return e
}

// journalsOf reads the engine journal, tolerating an engine that has written nothing yet.
func journalsOf(t *testing.T, e *ManagedEngine) []managedJournal {
	t.Helper()
	js, err := e.journals()
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return js
}

func TestRegisterDefaultsIsIdempotentAndRespectsExistingEntries(t *testing.T) {
	ctx := context.Background()
	e := NewManaged(filepath.Join(t.TempDir(), "state"))
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "nexbr0"}, {Name: "nexbr0", Kind: "bridge"}}
	if err := e.RegisterDefaults(ctx, "bad host!", nics); err == nil {
		t.Fatal("invalid host identity accepted")
	}
	if err := e.RegisterDefaults(ctx, "host-1", nics); err != nil {
		t.Fatal(err)
	}
	count := func(want int) []managedJournal {
		t.Helper()
		js := journalsOf(t, e)
		if len(js) != want {
			t.Fatalf("journal has %d entries, want %d: %+v", len(js), want, js)
		}
		return js
	}
	js := count(2)
	var storage, network int
	for _, j := range js {
		switch {
		case j.Command.Storage != nil:
			storage++
			s := j.Command.Storage
			if s.Name != DefaultStorageName || s.Path != DefaultStoragePath || s.HostID != "host-1" || s.PoolID != "host-1" || !identifier.MatchString(s.ID) {
				t.Fatalf("default storage %+v", s)
			}
		case j.Command.Network != nil:
			network++
			n := j.Command.Network
			if n.Name != "nex0 untagged" || n.Kind != "bridge" || n.Source != "nexbr0" || n.Uplink != "nex0" || n.HostID != "host-1" || !identifier.MatchString(n.ID) {
				t.Fatalf("default network %+v", n)
			}
		}
	}
	if storage != 1 || network != 1 {
		t.Fatalf("storage %d network %d", storage, network)
	}
	// Re-running, in whatever state the entries ended up, adds nothing.
	if err := e.RegisterDefaults(ctx, "host-1", nics); err != nil {
		t.Fatal(err)
	}
	count(2)
	// A new physical uplink adds exactly its untagged network.
	nics = append(nics, protocol.NIC{Name: "nex1", Kind: "physical", Master: "nexbr1"}, protocol.NIC{Name: "nexbr1", Kind: "bridge"})
	if err := e.RegisterDefaults(ctx, "host-1", nics); err != nil {
		t.Fatal(err)
	}
	for _, j := range count(3) {
		if j.Command.Network != nil && j.Command.Network.Source == "nexbr1" && j.Command.Network.Name != "nex1 untagged" {
			t.Fatalf("new uplink network %+v", j.Command.Network)
		}
	}
	// A host that already received Controller-issued defaults, under Controller IDs and a
	// pool of its own, gets no duplicates.
	issued := startEngine(t)
	for _, c := range []protocol.Command{
		{OperationID: "ctl-op-1", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "ctl-storage", PoolID: "pool-1", HostID: "host-1", Name: "ISO", Path: DefaultStoragePath}},
		{OperationID: "ctl-op-2", Kind: "network-register", Network: &protocol.NetworkRegistration{ID: "ctl-network", PoolID: "pool-1", HostID: "host-1", Name: "nex0 untagged", Kind: "bridge", Source: "nexbr0", Uplink: "nex0"}},
	} {
		if err := issued.save(managedJournal{Command: c, Result: protocol.Result{OperationID: c.OperationID, Status: "succeeded"}, Stage: "verified"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := issued.RegisterDefaults(ctx, "host-1", nics[:2]); err != nil {
		t.Fatal(err)
	}
	if js := journalsOf(t, issued); len(js) != 2 {
		t.Fatalf("Controller-issued defaults duplicated: %+v", js)
	}
}

func TestRegistrationConflictsOnThisHost(t *testing.T) {
	storage := func(op, status, path string) managedJournal {
		return managedJournal{Command: protocol.Command{OperationID: op, Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: op, Name: "Local " + op, Path: path}}, Result: protocol.Result{Status: status}}
	}
	network := func(op, status, kind, source string) managedJournal {
		return managedJournal{Command: protocol.Command{OperationID: op, Kind: "network-register", Network: &protocol.NetworkRegistration{ID: op, Name: "Net " + op, Kind: kind, Source: source}}, Result: protocol.Result{Status: status}}
	}
	js := []managedJournal{storage("s1", "succeeded", "/srv/a"), storage("s2", "failed", "/srv/b"), storage("s3", "pending", "/srv/c"),
		network("n1", "succeeded", "vlan", "nex0v100"), network("n2", "succeeded", "network", "default"), network("n3", "failed", "bridge", "br9")}
	want := func(c protocol.Command, conflict string) {
		t.Helper()
		err := registrationConflict(js, c)
		if (conflict == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), conflict)) {
			t.Fatalf("%+v: %v, want %q", c, err, conflict)
		}
	}
	want(protocol.Command{OperationID: "x", Storage: &protocol.StorageRegistration{Path: "/srv/a"}}, "/srv/a is already registered on this host as Local s1")
	want(protocol.Command{OperationID: "x", Storage: &protocol.StorageRegistration{Path: "/srv/b"}}, "")
	want(protocol.Command{OperationID: "x", Storage: &protocol.StorageRegistration{Path: "/srv/c"}}, "already registered")
	want(protocol.Command{OperationID: "s1", Storage: &protocol.StorageRegistration{Path: "/srv/a"}}, "")
	want(protocol.Command{OperationID: "x", Network: &protocol.NetworkRegistration{Kind: "bridge", Source: "nex0v100"}}, "nex0v100 is already registered on this host as Net n1")
	want(protocol.Command{OperationID: "x", Network: &protocol.NetworkRegistration{Kind: "vlan", Source: "nex0v100"}}, "already registered")
	want(protocol.Command{OperationID: "x", Network: &protocol.NetworkRegistration{Kind: "bridge", Source: "default"}}, "")
	want(protocol.Command{OperationID: "x", Network: &protocol.NetworkRegistration{Kind: "network", Source: "default"}}, "already registered")
	want(protocol.Command{OperationID: "x", Network: &protocol.NetworkRegistration{Kind: "bridge", Source: "br9"}}, "")
}

func TestVMConflictsOnThisHost(t *testing.T) {
	other := managedTestVM()
	other.ID, other.Name = "11111111-2222-4333-a444-555555555555", "guest-a"
	other.NICs = []protocol.VMNIC{{NetworkID: "net-1", Model: "virtio"}, {NetworkID: "net-1", Model: "virtio", MAC: "52:54:00:aa:bb:cc"}}
	failed := managedTestVM()
	failed.ID, failed.Name = "22222222-2222-4333-a444-555555555555", "guest-failed"
	js := []managedJournal{
		{Command: protocol.Command{OperationID: "a", Kind: "vm-create", VM: &other}, Result: protocol.Result{Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "f", Kind: "vm-create", VM: &failed}, Result: protocol.Result{Status: "failed"}},
	}
	v := managedTestVM()
	v.ID, v.Name = "33333333-2222-4333-a444-555555555555", "guest-b"
	if err := vmConflict(js, v); err != nil {
		t.Fatal(err)
	}
	if err := vmConflict(js, other); err != nil {
		t.Fatal("a VM never conflicts with its own entry:", err)
	}
	v.Name = "guest-failed"
	if err := vmConflict(js, v); err != nil {
		t.Fatal("a failed creation holds no name:", err)
	}
	v.Name = "guest-a"
	if err := vmConflict(js, v); err == nil || !strings.Contains(err.Error(), "name guest-a is already used") {
		t.Fatalf("duplicate name: %v", err)
	}
	v.Name = "guest-b"
	for _, mac := range []string{"52:54:00:aa:bb:cc", protocol.DerivedMAC(other.ID, 0)} {
		v.NICs = []protocol.VMNIC{{NetworkID: "net-1", Model: "virtio", MAC: mac}}
		if err := vmConflict(js, v); err == nil || !strings.Contains(err.Error(), "MAC address "+mac+" is already used by VM guest-a") {
			t.Fatalf("duplicate MAC %s: %v", mac, err)
		}
	}
}

func TestDedicatedMemoryIsAdmissionControlledAgainstTotalMemory(t *testing.T) {
	spec := func(id, mode string, mib int, status string) managedJournal {
		v := managedTestVM()
		v.ID, v.Memory = id, protocol.VMMemory{Mode: mode, SizeMiB: mib}
		return managedJournal{Command: protocol.Command{OperationID: id, Kind: "vm-create", VM: &v}, Result: protocol.Result{Status: status}}
	}
	js := []managedJournal{spec("d1", "dedicated", 8192, "succeeded"), spec("m1", "maximum", 8192, "succeeded"), spec("d2", "dedicated", 4096, "pending"), spec("d3", "dedicated", 4096, "failed")}
	// Succeeded and pending dedicated VMs are promises; maximum and failed ones are not.
	if got := dedicatedCommitted(js, "new"); got != 12288 {
		t.Fatalf("committed %d", got)
	}
	if got := dedicatedCommitted(js, "d1"); got != 4096 {
		t.Fatalf("committed excluding own entry %d", got)
	}
	total := uint64(32768 * 1024)
	// 12288 promised + 2048 reserve leaves exactly 18432 on a 32 GiB host.
	if !dedicatedFits(total, 12288, protocol.VMMemory{Mode: "dedicated", SizeMiB: 18432}) {
		t.Fatal("exact fit refused")
	}
	if dedicatedFits(total, 12288, protocol.VMMemory{Mode: "dedicated", SizeMiB: 18433}) {
		t.Fatal("one MiB over the remaining promise accepted")
	}
	if !dedicatedFits(total, 12288, protocol.VMMemory{Mode: "maximum", SizeMiB: 65536}) {
		t.Fatal("maximum mode must not be admission-controlled against promises")
	}
}

// startVirsh fakes libvirt with a file-backed domain state: start flips it to running and is
// counted, so the tests can prove a start is issued exactly once.
func startVirsh(t *testing.T, state string, startFails bool) (calls string) {
	t.Helper()
	dir := t.TempDir()
	stateFile, calls := filepath.Join(dir, "state"), filepath.Join(dir, "calls")
	if err := os.WriteFile(stateFile, []byte(state+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_STATE", stateFile)
	t.Setenv("FAKE_CALLS", calls)
	fail := "true"
	if startFails {
		fail = "exit 1"
	}
	fakeCommand(t, "virsh", `case "$3" in
 domstate) cat "$FAKE_STATE" ;;
 start) echo start >> "$FAKE_CALLS"; `+fail+`; echo running > "$FAKE_STATE" ;;
 *) exit 1 ;;
esac`)
	return calls
}
func startCalls(t *testing.T, path string) int {
	t.Helper()
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "start")
}

func TestStartAfterCreateStartsOnceAndRecordsFailure(t *testing.T) {
	ctx := context.Background()
	v := managedTestVM()
	v.StartAfterCreate = true
	create := func() managedJournal {
		return managedJournal{Command: protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "create-1", Status: "pending"}, Stage: "defined"}
	}
	t.Run("starts once", func(t *testing.T) {
		calls := startVirsh(t, "shut off", false)
		e := startEngine(t)
		j := create()
		if err := e.startCreated(ctx, &j); err != nil || j.Start != "started" || j.Stage != "started" {
			t.Fatalf("%v %+v", err, j)
		}
		if err := e.startCreated(ctx, &j); err != nil || startCalls(t, calls) != 1 {
			t.Fatalf("second call started again: %v, %d starts", err, startCalls(t, calls))
		}
		stopped := create()
		stopped.Command.VM = new(protocol.VMCreateSpec)
		*stopped.Command.VM = v
		stopped.Command.VM.StartAfterCreate = false
		if err := e.startCreated(ctx, &stopped); err != nil || stopped.Start != "" || startCalls(t, calls) != 1 {
			t.Fatalf("a VM not asking for a start was started: %v %+v", err, stopped)
		}
	})
	t.Run("failure keeps the creation and reports why", func(t *testing.T) {
		startVirsh(t, "shut off", true)
		e := startEngine(t)
		j := create()
		err := e.startCreated(ctx, &j)
		if err == nil || !strings.Contains(err.Error(), "VM created but not started") || j.Start == "" || j.Stage != "starting" {
			t.Fatalf("%v %+v", err, j)
		}
	})
	t.Run("retry after an interrupted start observes instead of starting again", func(t *testing.T) {
		calls := startVirsh(t, "running", false)
		e := startEngine(t)
		j := create()
		j.Stage = "starting"
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
		r, err := e.Execute(ctx, j.Command, nil)
		if err != nil || r.Status != "succeeded" || startCalls(t, calls) != 0 {
			t.Fatalf("%+v %v, %d starts", r, err, startCalls(t, calls))
		}
		js := journalsOf(t, e)
		if len(js) != 1 || js[0].Start != "started" || js[0].Stage != "started" {
			t.Fatalf("journal %+v", js)
		}
		// A retry of the finished creation returns the durable result without touching libvirt.
		if r, err = e.Execute(ctx, j.Command, nil); err != nil || r.Status != "succeeded" || startCalls(t, calls) != 0 {
			t.Fatalf("replay %+v %v, %d starts", r, err, startCalls(t, calls))
		}
	})
	t.Run("interrupted start that did not take effect needs attention", func(t *testing.T) {
		calls := startVirsh(t, "shut off", false)
		e := startEngine(t)
		j := create()
		j.Stage = "starting"
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
		r, err := e.Execute(ctx, j.Command, nil)
		if err != nil || r.Status != "needs_attention" || !strings.Contains(r.Error, "VM created but not started") || startCalls(t, calls) != 0 {
			t.Fatalf("%+v %v, %d starts", r, err, startCalls(t, calls))
		}
	})
}
