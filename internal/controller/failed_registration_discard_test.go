// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"testing"

	"nex.local/nex/internal/protocol"
)

// refusedVLAN registers a pool VLAN the host then refuses. That is the state D1 is about: a
// Controller request row for something no host holds and nothing can be asked to undo.
func refusedVLAN(l *managedLab, request, name string, vlan int) protocol.NetworkRegistration {
	l.t.Helper()
	var n protocol.NetworkRegistration
	read(l.t, l.a.call("POST", "/api/v1/networks", registrationRequest{RequestID: request, PoolID: l.pool, HostID: l.host.HostID, Name: name, Kind: "vlan", Uplink: "nex0", VLAN: vlan}, nil, 201), &n)
	if cmds := l.report().Commands; len(cmds) != 1 || cmds[0].Network == nil || cmds[0].Network.ID != n.ID {
		l.t.Fatalf("registration not dispatched: %+v", cmds)
	}
	l.report(protocol.Result{OperationID: n.OperationID, Status: "failed", Error: "network is already registered on this host"})
	for _, row := range networks(l) {
		if row.ID == n.ID && row.Status == "failed" {
			return n
		}
	}
	l.t.Fatalf("registration did not fail: %+v", networks(l))
	return n
}

// failedStorageID is the ID the storage list gives a refused registration, which is the only
// handle an operator has for it.
func failedStorageID(l *managedLab, name string) string {
	l.t.Helper()
	for _, row := range storageRows(l) {
		if row.Name == name && row.Status == "failed" {
			if row.ID == "" {
				l.t.Fatalf("a refused repository is listed with an empty id and cannot be addressed: %+v", row)
			}
			return row.ID
		}
	}
	l.t.Fatalf("no failed registration named %q: %+v", name, storageRows(l))
	return ""
}

// refusedNFS registers a typed repository Core allocates the ID for and then refuses. The
// registration row keeps the Controller's own pending ID, which must still address the row.
func refusedNFS(l *managedLab, request, name, export string) protocol.StorageRegistration {
	l.t.Helper()
	var s protocol.StorageRegistration
	read(l.t, l.a.call("POST", "/api/v1/storage", storageRegisterRequest{RequestID: request, PoolID: l.pool, HostID: l.host.HostID, Name: name,
		Kind:   protocol.StorageKindNFS,
		Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: export}}}, nil, 201), &s)
	// Core allocates a repository ID, so a registration still in flight has none. That is
	// the shipped contract and stays; what must not survive is a *failed* row with none.
	if s.ID != "" {
		l.t.Fatalf("the Controller allocated a repository identity: %+v", s)
	}
	cmds := reportCurrent(l, nil).Commands
	if len(cmds) != 1 || cmds[0].Storage == nil {
		l.t.Fatalf("registration not dispatched: %+v", cmds)
	}
	reportCurrent(l, nil, protocol.Result{OperationID: cmds[0].OperationID, Status: "failed", Error: "storage source is already registered on this host"})
	s.OperationID = cmds[0].OperationID
	s.ID = failedStorageID(l, name)
	return s
}

// reportCurrent is reportManaged at the current protocol version, which a host must speak
// before the Controller will deliver it a typed storage-repository command at all.
func reportCurrent(l *managedLab, mutate func(*protocol.ManagedState), results ...protocol.Result) protocol.ReportResponse {
	l.t.Helper()
	l.seq++
	m := hostDefaults(l.host.HostID)
	if mutate != nil {
		mutate(m)
	}
	inv := &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30,
		NICs:    []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}},
		Managed: m}
	return l.a.report(l.cert, protocol.ReportRequest{Version: protocol.Version, Sequence: l.seq, Capabilities: l.caps, Inventory: inv, Results: results})
}

func storageRows(l *managedLab) []protocol.StorageRegistration {
	l.t.Helper()
	var out []protocol.StorageRegistration
	read(l.t, l.a.call("GET", "/api/v1/storage", nil, nil, 200), &out)
	return out
}

// audited counts audit entries with one action, so a local discard is provably recorded.
func audited(l *managedLab, action string) int {
	l.t.Helper()
	var entries []protocol.Audit
	read(l.t, l.a.call("GET", "/api/v1/audit", nil, nil, 200), &entries)
	n := 0
	for _, entry := range entries {
		if entry.Action == action {
			n++
		}
	}
	return n
}

func hasNetwork(l *managedLab, id string) bool {
	for _, row := range networks(l) {
		if row.ID == id {
			return true
		}
	}
	return false
}

func hasStorage(l *managedLab, id string) bool {
	for _, row := range storageRows(l) {
		if row.ID == id {
			return true
		}
	}
	return false
}

// TestFailedNetworkRegistrationIsDiscardable: a registration the pool master refused is only
// this Controller's request record, so it must be discardable exactly like a failed ISO
// import — without relaying anything — and the discard must be impossible on a registration
// a host reports as applied.
func TestFailedNetworkRegistrationIsDiscardable(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	n := refusedVLAN(l, "vlan-refused", "Refused", 100)

	var discarded map[string]any
	read(t, l.a.call("DELETE", "/api/v1/networks/"+n.ID, nil, nil, 200), &discarded)
	if discarded["deleted"] != true || discarded["id"] != n.ID {
		t.Fatalf("discard response: %+v", discarded)
	}
	if hasNetwork(l, n.ID) {
		t.Fatalf("the refused registration survived its discard: %+v", networks(l))
	}
	if cmds := l.report().Commands; len(cmds) != 0 {
		t.Fatalf("a local discard asked a host to do something: %+v", cmds)
	}
	if audits := audited(l, "network.discard"); audits != 1 {
		t.Fatalf("discard audit entries: %d", audits)
	}

	// The guard. A failed operation whose network a host has since reported as applied is a
	// live pool resource in this Controller's mirror, and no discard may delete it.
	applied := refusedVLAN(l, "vlan-applied", "Applied", 101)
	reportManaged(l, func(m *protocol.ManagedState) {
		m.Networks = append(m.Networks, protocol.NetworkRegistration{ID: applied.ID, PoolID: l.pool, HostID: l.host.HostID,
			Name: "Applied", Kind: "vlan", Source: protocol.GuestBridge("nex0", 101), Uplink: "nex0", VLAN: 101, Generation: 1})
	})
	l.a.call("DELETE", "/api/v1/networks/"+applied.ID, nil, nil, 409)
	if !hasNetwork(l, applied.ID) {
		t.Fatalf("a host-reported network was discarded as a failed request: %+v", networks(l))
	}
}

// TestFailedStorageRegistrationIsAddressableAndDiscardable: the same for a repository whose
// durable ID Core never allocated, which additionally has to be addressable at all.
func TestFailedStorageRegistrationIsAddressableAndDiscardable(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.StorageRepositoryCapability)
	s := refusedNFS(l, "nfs-refused", "Refused NFS", "/refused")

	var discarded map[string]any
	read(t, l.a.call("DELETE", "/api/v1/storage/"+s.ID, nil, nil, 200), &discarded)
	if discarded["deleted"] != true {
		t.Fatalf("discard response: %+v", discarded)
	}
	if hasStorage(l, s.ID) {
		t.Fatalf("the refused registration survived its discard: %+v", storageRows(l))
	}
	if cmds := reportCurrent(l, nil).Commands; len(cmds) != 0 {
		t.Fatalf("a local discard asked a host to do something: %+v", cmds)
	}
	if audits := audited(l, "storage.discard"); audits != 1 {
		t.Fatalf("discard audit entries: %d", audits)
	}

	// The guard, for the storage shape of it: Core answered the refused operation late and
	// the row is now the mirror of a repository the host holds under Core's own ID.
	applied := refusedNFS(l, "nfs-applied", "Applied NFS", "/applied")
	reportCurrent(l, func(m *protocol.ManagedState) {
		m.Storage = append(m.Storage, protocol.StorageRegistration{ID: "core-allocated-nfs", PoolID: l.pool, HostID: l.host.HostID,
			Name: "Applied NFS", Kind: protocol.StorageKindNFS, Generation: 1, OperationID: applied.OperationID,
			Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/applied"}}})
	})
	if !hasStorage(l, "core-allocated-nfs") {
		t.Fatalf("the host-reported repository was not mirrored: %+v", storageRows(l))
	}
	l.a.call("DELETE", "/api/v1/storage/core-allocated-nfs", nil, nil, 409)
	if !hasStorage(l, "core-allocated-nfs") {
		t.Fatalf("a host-reported repository was discarded as a failed request: %+v", storageRows(l))
	}
	// An adopted repository is removed by relaying a detach, never by a local discard.
	l.a.call("DELETE", "/api/v1/storage/"+l.storage, nil, nil, 404)
	if !hasStorage(l, l.storage) {
		t.Fatalf("an adopted repository was discarded: %+v", storageRows(l))
	}
}
