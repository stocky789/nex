// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func placementEngine(t *testing.T) (*ManagedEngine, protocol.VMCreateSpec, protocol.StorageRegistration) {
	t.Helper()
	e := startEngine(t)
	v := managedTestVM()
	v.NICs, v.MediaID, v.Boot = nil, "", protocol.VMBoot{Firmware: "bios"}
	s := protocol.StorageRegistration{ID: "s", PoolID: v.PoolID, HostID: v.HostID, Path: t.TempDir()}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "storage", Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "storage", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	return e, v, s
}

func placementNative(t *testing.T, s protocol.StorageRegistration, ready func(uint64) error) {
	t.Helper()
	lowMemoryHost(t, 65536)
	old := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, got protocol.StorageRegistration, required uint64) (storageAccess, error) {
		if got.ID != s.ID {
			return storageAccess{}, errors.New("wrong storage")
		}
		if err := ready(required); err != nil {
			return storageAccess{}, err
		}
		return storageAccess{Path: s.Path, Available: 1 << 40, Total: 2 << 40, Health: "healthy"}, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = old })
	fakeCommand(t, "virsh", `case "$*" in
*domcapabilities*) printf '%s\n' '<domainCapabilities><domain>kvm</domain><arch>x86_64</arch><os supported="yes"></os></domainCapabilities>' ;;
*domstate*) printf '%s\n' 'shut off' ;;
esac`)
}

func TestCheckVMPlacementCreateIsReadOnlyAndReplays(t *testing.T) {
	e, v, s := placementEngine(t)
	placementNative(t, s, func(uint64) error { return nil })
	cmd := protocol.Command{OperationID: "place-create", Kind: "vm-create", VM: &v}
	before, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := json.Marshal(before)
	storage, err := e.CheckVMPlacement(context.Background(), cmd)
	if err != nil || len(storage) != 1 || storage[0].ID != s.ID || storage[0].Access.AvailableBytes == nil || storage[0].Access.ObservedAt.IsZero() {
		t.Fatalf("placement = %#v, %v", storage, err)
	}
	if _, err = os.Stat(filepath.Join(s.Path, "nex-vms", v.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("placement allocated a VM directory: %v", err)
	}
	after, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("placement changed the managed journal")
	}
	if err = e.save(managedJournal{Command: cmd, Result: protocol.Result{OperationID: cmd.OperationID, Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.CheckVMPlacement(context.Background(), cmd); err != nil {
		t.Fatalf("successful replay refused: %v", err)
	}
}

func TestCheckVMPlacementRejectsMissingReferenceAndCapacity(t *testing.T) {
	e, v, s := placementEngine(t)
	placementNative(t, s, func(uint64) error { return nil })
	missing := v
	missing.Disks = append([]protocol.VMDisk(nil), v.Disks...)
	missing.Disks[0].StorageID = "missing"
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "missing", Kind: "vm-create", VM: &missing}); err == nil {
		t.Fatal("missing storage accepted")
	}
	missingNetwork := v
	missingNetwork.NICs = []protocol.VMNIC{{NetworkID: "missing", Model: "virtio"}}
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "missing-network", Kind: "vm-create", VM: &missingNetwork}); err == nil || !strings.Contains(err.Error(), "network is not registered") {
		t.Fatal("missing network accepted")
	}
	missingMedia := v
	missingMedia.MediaID = "missing-media"
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "missing-media", Kind: "vm-create", VM: &missingMedia}); err == nil || !strings.Contains(err.Error(), "ISO is not imported") {
		t.Fatal("missing media accepted")
	}
	old := managedStorageReadyFn
	managedStorageReadyFn = func(context.Context, protocol.StorageRegistration, uint64) (storageAccess, error) {
		return storageAccess{}, errors.New("insufficient free storage including reserve")
	}
	t.Cleanup(func() { managedStorageReadyFn = old })
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "capacity", Kind: "vm-create", VM: &v}); err == nil {
		t.Fatal("insufficient storage accepted")
	}
	wrongScope := v
	wrongScope.PoolID = "other-pool"
	managedStorageReadyFn = old
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "wrong-scope", Kind: "vm-create", VM: &wrongScope}); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatal("mismatched storage scope accepted")
	}
	tooManyCPUs := v
	tooManyCPUs.CPU.Count = runtime.NumCPU() + 1
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "cpu", Kind: "vm-create", VM: &tooManyCPUs}); err == nil || !strings.Contains(err.Error(), "sufficient host CPUs") {
		t.Fatal("unsupported CPU capacity accepted")
	}
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "wrong-kind", Kind: "vm-power"}); err == nil {
		t.Fatal("non-placement command accepted")
	}
}

func TestCheckVMPlacementReplaysFailedAndRejectsChangedInput(t *testing.T) {
	e, v, s := placementEngine(t)
	placementNative(t, s, func(uint64) error { return nil })
	cmd := protocol.Command{OperationID: "failed", Kind: "vm-create", VM: &v}
	if err := e.save(managedJournal{Command: cmd, Result: protocol.Result{OperationID: cmd.OperationID, Status: "failed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CheckVMPlacement(context.Background(), cmd); err != nil {
		t.Fatalf("failed replay preflight = %v", err)
	}
	result, err := e.Execute(context.Background(), cmd, nil)
	if err != nil || result.Status != "failed" {
		t.Fatalf("failed replay outcome = %#v, %v", result, err)
	}
	changed := cmd
	changedVM := v
	changedVM.Description = "changed"
	changed.VM = &changedVM
	if _, err := e.CheckVMPlacement(context.Background(), changed); err == nil || !strings.Contains(err.Error(), "different input") {
		t.Fatalf("changed replay = %v", err)
	}
}

func TestCheckVMPlacementUpdateOnlyReservesNewDisks(t *testing.T) {
	e, v, s := placementEngine(t)
	var required []uint64
	placementNative(t, s, func(bytes uint64) error { required = append(required, bytes); return nil })
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}, Disks: []string{filepath.Join(s.Path, "nex-vms", v.ID, "disk-0.qcow2")}, Targets: []string{"vda"}, MediaTarget: "sda", DriversTarget: "sdb"}); err != nil {
		t.Fatal(err)
	}
	next := v
	next.Revision, next.Description = 1, "changed"
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "update", Kind: "vm-update", VM: &next}); err != nil {
		t.Fatal(err)
	}
	for _, bytes := range required {
		if bytes != 0 {
			t.Fatalf("existing disk requested new free capacity: %d", bytes)
		}
	}
}

func TestCheckVMPlacementRejectsIncompleteUpdateLayout(t *testing.T) {
	e, v, s := placementEngine(t)
	placementNative(t, s, func(uint64) error { return nil })
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	next := v
	next.Revision = 1
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "update", Kind: "vm-update", VM: &next}); err == nil || !strings.Contains(err.Error(), "disk layout is incomplete") {
		t.Fatalf("incomplete layout = %v", err)
	}
}

func TestCheckVMPlacementHonorsCancellationWhileLocked(t *testing.T) {
	e, v, _ := placementEngine(t)
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(lock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = e.CheckVMPlacement(ctx, protocol.Command{OperationID: "cancel", Kind: "vm-create", VM: &v}); !errors.Is(err, context.Canceled) {
		t.Fatalf("placement lock cancellation = %v", err)
	}
}

// Placement is capped at 20s on the host. A multi-GiB ISO on remote storage cannot be
// fully hashed in that window; preflight only checks presence and recorded size, and
// Execute still verifies the digest before defining the guest.
func TestCheckVMPlacementMediaUsesPresenceNotDigest(t *testing.T) {
	e, v, s := placementEngine(t)
	placementNative(t, s, func(uint64) error { return nil })
	mediaDir := filepath.Join(s.Path, "nex-media")
	if err := os.MkdirAll(mediaDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(mediaDir, "media-1.iso")
	body := make([]byte, 32774)
	copy(body[32769:], []byte("CD001"))
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	media := protocol.Media{
		ID: "media-1", PoolID: v.PoolID, HostID: v.HostID, StorageID: s.ID,
		Name: "install.iso", SizeBytes: int64(len(body)),
		SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
	}
	if err := e.save(managedJournal{
		Command:   protocol.Command{OperationID: "iso", Kind: "iso-import", Media: &media},
		Result:    protocol.Result{OperationID: "iso", Status: "succeeded"},
		MediaPath: path,
	}); err != nil {
		t.Fatal(err)
	}
	withMedia := v
	withMedia.MediaID = media.ID
	withMedia.Boot = protocol.VMBoot{Firmware: "bios", MediaBootOrder: 1}
	withMedia.Disks = append([]protocol.VMDisk(nil), v.Disks...)
	withMedia.Disks[0].BootOrder = 2
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "place-media", Kind: "vm-create", VM: &withMedia}); err != nil {
		t.Fatalf("placement hashed or rejected a present ISO: %v", err)
	}
	if err := os.WriteFile(path, body[:len(body)-1], 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CheckVMPlacement(context.Background(), protocol.Command{OperationID: "place-media-size", Kind: "vm-create", VM: &withMedia}); err == nil || !strings.Contains(err.Error(), "ISO size changed") {
		t.Fatalf("wrong-size ISO = %v", err)
	}
}
