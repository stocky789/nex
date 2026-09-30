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

// refusedNFS registers a typed repository Core allocates the ID for and then refuses. The
// registration is left as the Controller's pending-storage replay row.
func refusedNFS(l *managedLab, request, name, export string) (storageRegisterRequest, protocol.StorageRegistration) {
	l.t.Helper()
	var s protocol.StorageRegistration
	body := storageRegisterRequest{RequestID: request, PoolID: l.pool, HostID: l.host.HostID, Name: name,
		Kind:   protocol.StorageKindNFS,
		Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: export}}}
	read(l.t, l.a.call("POST", "/api/v1/storage", body, nil, 201), &s)
	if s.ID != "" {
		l.t.Fatalf("the Controller allocated a repository identity: %+v", s)
	}
	cmds := reportCurrent(l, nil).Commands
	if len(cmds) != 1 || cmds[0].Storage == nil {
		l.t.Fatalf("registration not dispatched: %+v", cmds)
	}
	reportCurrent(l, nil, protocol.Result{OperationID: cmds[0].OperationID, Status: "failed", Error: "storage source is already registered on this host"})
	s.OperationID = cmds[0].OperationID
	return body, s
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

// TestRefusedStorageRegistrationIsNotListed: a registration Core refused leaves no repository
// row, keeps its error in the operation history, and replays as the same failed result.
func TestRefusedStorageRegistrationIsNotListed(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.StorageRepositoryCapability)
	body, s := refusedNFS(l, "nfs-refused", "Refused NFS", "/refused")

	for _, row := range storageRows(l) {
		if row.Name == "Refused NFS" {
			t.Fatalf("the refused registration is listed: %+v", row)
		}
	}
	var ops []protocol.Operation
	read(t, l.a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	found := false
	for _, op := range ops {
		found = found || op.ID == s.OperationID && op.Status == "failed" && op.Error == "storage source is already registered on this host"
	}
	if !found {
		t.Fatalf("the failure is not in the operation history: %+v", ops)
	}
	var replay protocol.StorageRegistration
	read(t, l.a.call("POST", "/api/v1/storage", body, nil, 200), &replay)
	if replay.OperationID != s.OperationID || replay.Status != "failed" {
		t.Fatalf("replay did not return the failed result: %+v", replay)
	}
	if cmds := reportCurrent(l, nil).Commands; len(cmds) != 0 {
		t.Fatalf("replay dispatched a new command: %+v", cmds)
	}

	// Core answered the refused operation late: the row now mirrors a repository the host
	// holds under Core's own ID and must stay listed.
	_, applied := refusedNFS(l, "nfs-applied", "Applied NFS", "/applied")
	reportCurrent(l, func(m *protocol.ManagedState) {
		m.Storage = append(m.Storage, protocol.StorageRegistration{ID: "core-allocated-nfs", PoolID: l.pool, HostID: l.host.HostID,
			Name: "Applied NFS", Kind: protocol.StorageKindNFS, Generation: 1, OperationID: applied.OperationID,
			Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/applied"}}})
	})
	if !hasStorage(l, "core-allocated-nfs") {
		t.Fatalf("the host-reported repository was not mirrored: %+v", storageRows(l))
	}
	l.a.call("DELETE", "/api/v1/storage/"+l.storage, nil, nil, 404)
}
