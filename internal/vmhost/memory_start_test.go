// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

const startMemoryRefusal = "insufficient available memory to start this VM including 2 GiB host reserve"

// lowMemoryHost fakes a KVM device and /proc/meminfo with plenty of total memory but only
// availableMiB currently available.
func lowMemoryHost(t *testing.T, availableMiB int) {
	t.Helper()
	dir := t.TempDir()
	kvm, meminfo := filepath.Join(dir, "kvm"), filepath.Join(dir, "meminfo")
	if err := os.WriteFile(kvm, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meminfo, []byte("MemTotal:       67108864 kB\nMemFree:          102400 kB\nMemAvailable:   "+strconv.Itoa(availableMiB*1024)+" kB\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldKVM, oldMem := managedKVMDevice, managedMeminfo
	managedKVMDevice, managedMeminfo = kvm, meminfo
	t.Cleanup(func() { managedKVMDevice, managedMeminfo = oldKVM, oldMem })
}

// BUG-0195: creation and stopped edits do not depend on memory that is free right now; only a
// start does. The total-memory dedicated promise is still a creation refusal.
func TestManagedCapacityIgnoresCurrentlyAvailableMemory(t *testing.T) {
	lowMemoryHost(t, 1024)
	fakeCommand(t, "virsh", `printf '%s\n' '<domainCapabilities><domain>kvm</domain><arch>x86_64</arch><os supported="yes"><enum name="firmware"><value>efi</value></enum></os></domainCapabilities>'`)
	v := managedTestVM()
	v.CPU.Count = 1
	for _, mode := range []string{"dedicated", "maximum"} {
		v.Memory = protocol.VMMemory{Mode: mode, SizeMiB: 4096}
		if err := managedCapacity(context.Background(), v, nil); err != nil {
			t.Fatalf("%s VM refused on low available memory: %v", mode, err)
		}
	}
	v.Memory = protocol.VMMemory{Mode: "dedicated", SizeMiB: 64 * 1024}
	if err := managedCapacity(context.Background(), v, nil); err == nil || !strings.Contains(err.Error(), "dedicated memory exceeds") {
		t.Fatalf("dedicated promise above total memory: %v", err)
	}
}

// BUG-0195: a manual start of a shut-off VM is refused before virsh start when available
// memory cannot meet the ceiling plus the host reserve; enough memory starts it, and a
// running VM stays a no-op whatever memory is available.
func TestManagedPowerStartChecksAvailableMemory(t *testing.T) {
	e := startEngine(t)
	calls := startLibvirt(t)
	v := managedTestVM()
	v.Memory = protocol.VMMemory{Mode: "maximum", SizeMiB: 4096}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	start := func(op string) protocol.Result {
		t.Helper()
		r, err := e.Execute(context.Background(), protocol.Command{OperationID: op, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: v.ID, PoolID: v.PoolID, HostID: v.HostID, Action: "start"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	lowMemoryHost(t, 5000)
	t.Setenv("VMHOST_STATE", "shut off")
	if r := start("low"); r.Status != "failed" || r.Error != startMemoryRefusal {
		t.Fatalf("low-memory start %+v", r)
	}
	if got := calls(); got != "domstate "+v.ID+"\n" {
		t.Fatalf("refused start touched libvirt: %q", got)
	}
	t.Setenv("VMHOST_STATE", "running")
	if r := start("running"); r.Status != "succeeded" || calls() != "domstate "+v.ID+"\n" {
		t.Fatalf("start of a running VM must stay a no-op: %+v", r)
	}
	lowMemoryHost(t, 6144)
	t.Setenv("VMHOST_STATE", "shut off")
	if r := start("enough"); r.Status != "succeeded" {
		t.Fatalf("start with enough memory %+v", r)
	}
	if got := calls(); got != "domstate "+v.ID+"\nstart "+v.ID+"\n" {
		t.Fatalf("start calls %q", got)
	}
}

// BUG-0195: the automatic start after creation is refused the same way; the creation still
// succeeds and the refusal is reported as the start outcome.
func TestManagedStartAfterCreateChecksAvailableMemory(t *testing.T) {
	e := startEngine(t)
	calls := startLibvirt(t)
	lowMemoryHost(t, 1024)
	v := managedTestVM()
	v.StartAfterCreate = true
	c := protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}
	if err := e.save(managedJournal{Command: c, Result: protocol.Result{OperationID: "create", Status: "pending"}, Stage: "defined", Domain: []byte("<domain/>")}); err != nil {
		t.Fatal(err)
	}
	e.observe = func(context.Context, managedJournal, string) error { return nil }
	t.Setenv("VMHOST_STATE", "shut off")
	t.Setenv("VMHOST_DOMAINS", v.ID)
	if r, err := e.Execute(context.Background(), c, nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("creation %+v %v", r, err)
	}
	if got := calls(); strings.Contains(got, "start "+v.ID) {
		t.Fatalf("refused automatic start issued virsh start: %q", got)
	}
	got, _ := e.Applied(context.Background())
	if len(got.Outcomes) != 2 || got.Outcomes[1].Error != "VM created but not started: "+startMemoryRefusal {
		t.Fatalf("start outcome %+v", got.Outcomes)
	}
}

// BUG-0195 review: a stopped edit that raised memory may have been defined in libvirt and still
// be unresolved (needs_attention); the start check must use that larger ceiling, not the last
// succeeded one, or an oversized VM could boot past it.
func TestManagedPowerStartUsesUnresolvedLargerMemory(t *testing.T) {
	e := startEngine(t)
	calls := startLibvirt(t)
	v := managedTestVM()
	v.Memory = protocol.VMMemory{Mode: "maximum", SizeMiB: 512}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	next := v
	next.Revision, next.Memory = 1, protocol.VMMemory{Mode: "maximum", SizeMiB: 8192}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "grow", Kind: "vm-update", VM: &next}, Result: protocol.Result{OperationID: "grow", Status: "needs_attention", Error: "definition observation failed"}, Stage: "defining"}); err != nil {
		t.Fatal(err)
	}
	lowMemoryHost(t, 4000)
	t.Setenv("VMHOST_STATE", "shut off")
	r, err := e.Execute(context.Background(), protocol.Command{OperationID: "start", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: v.ID, PoolID: v.PoolID, HostID: v.HostID, Action: "start"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "failed" || r.Error != startMemoryRefusal {
		t.Fatalf("start past an unresolved larger memory edit: %+v", r)
	}
	if got := calls(); strings.Contains(got, "start "+v.ID) {
		t.Fatalf("refused start touched libvirt start: %q", got)
	}
}
