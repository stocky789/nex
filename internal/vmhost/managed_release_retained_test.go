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

// releaseRetainedLab is a VM on local storage whose second revision detached an original disk.
type releaseRetainedLab struct {
	e              *ManagedEngine
	vm             protocol.VMCreateSpec
	live, original string
}

func newReleaseRetainedLab(t *testing.T, domstate string) releaseRetainedLab {
	t.Helper()
	storageRoot := t.TempDir()
	vm := managedTestVM()
	vmDir := filepath.Join(storageRoot, "nex-vms", vm.ID)
	if err := os.MkdirAll(vmDir, 0700); err != nil {
		t.Fatal(err)
	}
	live, original := filepath.Join(vmDir, "live.qcow2"), filepath.Join(vmDir, "original.qcow2")
	for _, path := range []string{live, original} {
		if err := os.WriteFile(path, []byte("qcow2"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, _ protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		return storageAccess{Path: storageRoot, Total: 1 << 40, Available: 1 << 40}, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	t.Setenv("VMHOST_STATE", domstate)
	t.Setenv("VMHOST_VM_ID", vm.ID)
	fakeCommand(t, "virsh", `case "$3" in domstate) echo "$VMHOST_STATE";; list) printf '%s\n' "$VMHOST_VM_ID";; *) exit 2;; esac`)
	e := managedTestEngine(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Path: storageRoot, Kind: protocol.StorageKindLocal}
	first, second := vm, vm
	first.Revision, second.Revision = 0, 1
	for _, j := range []managedJournal{
		{Command: protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &storage}, Result: protocol.Result{Status: "succeeded"}, AppliedStorage: &storage},
		{Command: protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &first}, Result: protocol.Result{Status: "succeeded"}, Stage: "defined",
			Disks: []string{original}, DiskChains: [][]string{{original}}, Targets: []string{"vda"}},
		{Command: protocol.Command{OperationID: "replace-1", Kind: "vm-update", VM: &second}, Result: protocol.Result{Status: "succeeded"}, Stage: "defined",
			Disks: []string{live}, DiskChains: [][]string{{live}}, Targets: []string{"vda"}, Retained: []string{original, live}},
	} {
		j.Result.OperationID = j.Command.OperationID
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	return releaseRetainedLab{e: e, vm: vm, live: live, original: original}
}

func (l releaseRetainedLab) release(t *testing.T, op, confirmation string) protocol.Result {
	t.Helper()
	r, err := l.e.Execute(context.Background(), protocol.Command{OperationID: op, Kind: "vm-release-retained",
		ReleaseRetained: &protocol.VMReleaseRetainedRequest{VMID: l.vm.ID, PoolID: l.vm.PoolID, HostID: l.vm.HostID, Confirmation: confirmation}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fileExists(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestReleaseRetainedOriginalsDeletesOnlyDetachedFiles(t *testing.T) {
	l := newReleaseRetainedLab(t, "shut off")
	if r := l.release(t, "release-wrong-name", "other"); r.Status != "failed" || !strings.Contains(r.Error, "VM's name") || !fileExists(l.original) {
		t.Fatalf("wrong confirmation: %+v", r)
	}
	if r := l.release(t, "release-1", l.vm.Name); r.Status != "succeeded" {
		t.Fatalf("release failed: %+v", r)
	}
	if fileExists(l.original) || !fileExists(l.live) {
		t.Fatalf("original present=%v live present=%v", fileExists(l.original), fileExists(l.live))
	}
	if again := l.release(t, "release-1", l.vm.Name); again.Status != "succeeded" {
		t.Fatalf("replay of the same request changed: %+v", again)
	}
	if r := l.release(t, "release-2", l.vm.Name); r.Status != "failed" || !strings.Contains(r.Error, "no retained") {
		t.Fatalf("second release with nothing left: %+v", r)
	}
}

func TestReleaseRetainedOriginalsRefusesRunningVM(t *testing.T) {
	l := newReleaseRetainedLab(t, "running")
	if r := l.release(t, "release-running", l.vm.Name); r.Status != "failed" || !strings.Contains(r.Error, "stop the VM") || !fileExists(l.original) {
		t.Fatalf("running VM: %+v", r)
	}
}
