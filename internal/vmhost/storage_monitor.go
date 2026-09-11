// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
)

// StorageMonitor keeps host observations separate from the Core journal. A stuck kernel
// filesystem call can outlive its context, so it consumes one of a small fixed set of slots
// until it returns instead of allowing each report to start another blocked goroutine.
const maxStorageProbeWorkers = 8

type storageProbe func(context.Context, protocol.StorageRegistration) (storageAccess, error)
type storageAccessCheck func(context.Context, string) error

type storageProbeState struct {
	repository protocol.StorageRegistration
	access     protocol.StorageAccess
	busy       bool
	deadline   time.Time
	cancel     context.CancelFunc
}

type StorageMonitor struct {
	mu      sync.Mutex
	timeout time.Duration
	probe   storageProbe
	entries map[string]*storageProbeState
	running int
	next    int
}

func NewStorageMonitor(timeout time.Duration) *StorageMonitor {
	return newStorageMonitor(timeout, verifiedStorageProbe(storageInspect, qemuStorageAccess))
}

func verifiedStorageProbe(inspect storageProbe, check storageAccessCheck) storageProbe {
	return func(ctx context.Context, s protocol.StorageRegistration) (storageAccess, error) {
		access, err := inspect(ctx, s)
		if err != nil {
			return storageAccess{}, err
		}
		if err = check(ctx, access.Path); err != nil {
			return storageAccess{}, err
		}
		return access, nil
	}
}

func newStorageMonitor(timeout time.Duration, probe storageProbe) *StorageMonitor {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &StorageMonitor{timeout: timeout, probe: probe, entries: map[string]*storageProbeState{}}
}

// Tick starts at most maxStorageProbeWorkers inspections plus a disposable QEMU-account access
// probe. It never waits for native I/O and it never mounts, logs in, repairs, or changes the
// durable repository projection.
func (m *StorageMonitor) Tick(ctx context.Context, repositories []protocol.StorageRegistration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	live := make(map[string]protocol.StorageRegistration, len(repositories))
	for _, s := range repositories {
		if s.ID != "" && !s.Tombstone {
			live[s.ID] = s
		}
	}
	for id := range m.entries {
		if _, ok := live[id]; !ok {
			delete(m.entries, id)
		}
	}
	ids := make([]string, 0, len(live))
	for id, s := range live {
		ids = append(ids, id)
		entry, ok := m.entries[id]
		if !ok || entry.repository.Generation != s.Generation || !reflect.DeepEqual(entry.repository, s) {
			if ok && entry.busy {
				entry.cancel()
			}
			m.entries[id] = &storageProbeState{repository: s, access: unknownStorageAccess("not yet observed")}
		}
	}
	sort.Strings(ids)
	for _, entry := range m.entries {
		if entry.busy && !now.Before(entry.deadline) {
			entry.cancel()
			entry.access.Status = "unknown"
			entry.access.Readable, entry.access.Writable = false, false
			entry.access.Error = "storage probe deadline exceeded; observation is stale"
		}
	}
	if len(ids) == 0 || ctx.Err() != nil {
		return
	}
	for attempts := 0; attempts < len(ids) && m.running < maxStorageProbeWorkers; attempts++ {
		id := ids[(m.next+attempts)%len(ids)]
		entry := m.entries[id]
		if entry.busy {
			continue
		}
		entry.busy = true
		entry.deadline = now.Add(m.timeout)
		probeCtx, cancel := context.WithDeadline(ctx, entry.deadline)
		entry.cancel = cancel
		m.running++
		go m.run(probeCtx, entry)
	}
	m.next = (m.next + 1) % len(ids)
}

func (m *StorageMonitor) run(ctx context.Context, entry *storageProbeState) {
	got, err := m.probe(ctx, entry.repository)
	now := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	defer entry.cancel()
	entry.busy = false
	m.running--
	if ctx.Err() != nil || !now.Before(entry.deadline) {
		entry.access.Status = "unknown"
		entry.access.Readable, entry.access.Writable = false, false
		entry.access.Error = "storage probe deadline exceeded; observation is stale"
		return
	}
	if err != nil {
		entry.access = protocol.StorageAccess{Status: "unavailable", Error: err.Error(), ObservedAt: now}
		return
	}
	available := got.Available
	entry.access = protocol.StorageAccess{Status: got.Health, Readable: true, Writable: !got.ReadOnly, TotalBytes: got.Total, AvailableBytes: &available, ObservedAt: now}
	if entry.access.Status == "" {
		entry.access.Status = "healthy"
	}
}

// Overlay copies durable repository records and replaces only their ephemeral access result.
// A caller must invoke Tick independently of its report/heartbeat exchange.
func (m *StorageMonitor) Overlay(repositories []protocol.StorageRegistration) []protocol.StorageRegistration {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]protocol.StorageRegistration, len(repositories))
	for i, s := range repositories {
		out[i] = s
		if entry := m.entries[s.ID]; entry != nil {
			if entry.repository.Generation == s.Generation {
				out[i].Access = entry.access
			} else {
				out[i].Access = unknownStorageAccess("storage configuration changed; awaiting observation")
			}
		} else if !s.Tombstone {
			out[i].Access = unknownStorageAccess("not yet observed")
		}
	}
	return out
}

func unknownStorageAccess(message string) protocol.StorageAccess {
	return protocol.StorageAccess{Status: "unknown", Error: message}
}
