// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func monitorStorage(id string) protocol.StorageRegistration {
	return protocol.StorageRegistration{ID: id, Generation: 1, Kind: protocol.StorageKindLocal, Path: "/srv/" + id}
}

func waitStorageStatus(t *testing.T, m *StorageMonitor, storage []protocol.StorageRegistration, want string) []protocol.StorageRegistration {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got := m.Overlay(storage)
		if len(got) > 0 && got[0].Access.Status == want {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("storage health did not become %q: %+v", want, m.Overlay(storage))
	return nil
}

func TestStorageMonitorOverlaysEphemeralHealth(t *testing.T) {
	m := newStorageMonitor(time.Second, func(context.Context, protocol.StorageRegistration) (storageAccess, error) {
		return storageAccess{Total: 100, Available: 40, Health: "healthy"}, nil
	})
	storage := []protocol.StorageRegistration{monitorStorage("repo-1")}
	m.Tick(context.Background(), storage)
	got := waitStorageStatus(t, m, storage, "healthy")[0]
	if got.Access.AvailableBytes == nil || *got.Access.AvailableBytes != 40 || !got.Access.Writable || storage[0].Access.Status != "" {
		t.Fatalf("health overlay is not an ephemeral copy: %+v", got)
	}
}

func TestStorageMonitorRefusesQEMUAccessFailure(t *testing.T) {
	m := newStorageMonitor(time.Second, verifiedStorageProbe(
		func(context.Context, protocol.StorageRegistration) (storageAccess, error) {
			return storageAccess{Path: "/srv/repo-1", Health: "healthy"}, nil
		},
		func(context.Context, string) error { return errors.New("libvirt-qemu denied") },
	))
	storage := []protocol.StorageRegistration{monitorStorage("repo-1")}
	m.Tick(context.Background(), storage)
	got := waitStorageStatus(t, m, storage, "unavailable")[0].Access
	if got.Readable || got.Writable || got.Error != "libvirt-qemu denied" {
		t.Fatalf("QEMU access failure was advertised as usable storage: %+v", got)
	}
}

func TestStorageMonitorCapsHungProbes(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	var started atomic.Int32
	m := newStorageMonitor(5*time.Millisecond, func(context.Context, protocol.StorageRegistration) (storageAccess, error) {
		started.Add(1)
		<-block // Models an uninterruptible kernel filesystem call.
		return storageAccess{}, nil
	})
	storage := make([]protocol.StorageRegistration, maxStorageProbeWorkers+4)
	for i := range storage {
		storage[i] = monitorStorage(string(rune('a' + i)))
	}
	m.Tick(context.Background(), storage)
	time.Sleep(10 * time.Millisecond)
	m.Tick(context.Background(), storage)
	if got := started.Load(); got != maxStorageProbeWorkers {
		t.Fatalf("started %d blocked probes, want fixed cap %d", got, maxStorageProbeWorkers)
	}
	if got := m.Overlay(storage)[0].Access; got.Status != "unknown" || got.Error == "" {
		t.Fatalf("expired probe did not report stale unknown access: %+v", got)
	}
}

func TestStorageMonitorDeadlineClearsPlacementAccess(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	var calls atomic.Int32
	m := newStorageMonitor(5*time.Millisecond, func(context.Context, protocol.StorageRegistration) (storageAccess, error) {
		if calls.Add(1) == 1 {
			return storageAccess{Total: 100, Available: 40, Health: "healthy"}, nil
		}
		<-block
		return storageAccess{}, nil
	})
	storage := []protocol.StorageRegistration{monitorStorage("repo-1")}
	m.Tick(context.Background(), storage)
	if got := waitStorageStatus(t, m, storage, "healthy")[0].Access; !got.Readable || !got.Writable {
		t.Fatalf("initial healthy probe lacks access: %+v", got)
	}
	m.Tick(context.Background(), storage) // Starts the second, deliberately hung probe.
	time.Sleep(10 * time.Millisecond)
	m.Tick(context.Background(), storage)
	if got := m.Overlay(storage)[0].Access; got.Status != "unknown" || got.Readable || got.Writable {
		t.Fatalf("expired storage probe left optimistic placement access: %+v", got)
	}
}

func TestStorageMonitorDoesNotReusePriorGeneration(t *testing.T) {
	m := newStorageMonitor(time.Second, func(context.Context, protocol.StorageRegistration) (storageAccess, error) {
		return storageAccess{Health: "healthy"}, nil
	})
	storage := []protocol.StorageRegistration{monitorStorage("repo-1")}
	m.Tick(context.Background(), storage)
	waitStorageStatus(t, m, storage, "healthy")
	storage[0].Generation++
	if got := m.Overlay(storage)[0].Access; got.Status != "unknown" || got.Readable || got.Writable {
		t.Fatalf("new storage generation inherited prior health: %+v", got)
	}
}
