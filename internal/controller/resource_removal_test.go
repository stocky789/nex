// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
)

// reportManaged is l.report with the host's managed journal adjusted, so a test can report a
// pool VLAN, a rename or a tombstone the way the pool master's Core does.
func reportManaged(l *managedLab, mutate func(*protocol.ManagedState), results ...protocol.Result) protocol.ReportResponse {
	l.t.Helper()
	l.seq++
	m := hostDefaults(l.host.HostID)
	if mutate != nil {
		mutate(m)
	}
	inv := &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30,
		NICs:    []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}},
		Managed: m}
	return l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv, Results: results})
}

func networks(l *managedLab) []protocol.NetworkRegistration {
	l.t.Helper()
	var out []protocol.NetworkRegistration
	read(l.t, l.a.call("GET", "/api/v1/networks", nil, nil, 200), &out)
	return out
}

func mediaLibrary(l *managedLab) []protocol.Media {
	l.t.Helper()
	var out []protocol.Media
	read(l.t, l.a.call("GET", "/api/v1/media", nil, nil, 200), &out)
	return out
}

// vlanLab enrolls a pool master and registers one applied pool VLAN on it, which is the state
// every removal and rename test starts from.
func vlanLab(t *testing.T) (*managedLab, protocol.NetworkRegistration) {
	t.Helper()
	l := newManagedLab(t, protocol.ManagedVMCapability)
	var vlan protocol.NetworkRegistration
	req := registrationRequest{RequestID: "vlan-request", PoolID: l.pool, HostID: l.host.HostID, Name: "Guests", Kind: "vlan", Uplink: "nex0", VLAN: 100}
	read(t, l.a.call("POST", "/api/v1/networks", req, nil, 201), &vlan)
	if cmds := l.report().Commands; len(cmds) != 1 || cmds[0].Network == nil || cmds[0].Network.ID != vlan.ID {
		t.Fatalf("VLAN registration not dispatched: %+v", cmds)
	}
	l.report(protocol.Result{OperationID: vlan.OperationID, Status: "succeeded"})
	return l, vlan
}

// appliedVLAN is the master's journal row for that network, as Core reports it.
func appliedVLAN(l *managedLab, vlan protocol.NetworkRegistration) protocol.NetworkRegistration {
	return protocol.NetworkRegistration{ID: vlan.ID, PoolID: l.pool, HostID: l.host.HostID, Name: "Guests", Kind: "vlan",
		Source: protocol.GuestBridge("nex0", 100), Uplink: "nex0", VLAN: 100, Generation: 1}
}

// Removing a pool VLAN is a relay: exactly one command reaches the pool master, the Controller
// decides nothing about the network itself, and every refusal is answered before anything is
// queued.
func TestPoolNetworkRemovalRelaysOneCommandToTheMaster(t *testing.T) {
	l, vlan := vlanLab(t)
	path := "/api/v1/networks/" + vlan.ID
	body := resourceLifecycleAPIRequest{RequestID: "remove-vlan", PoolID: l.pool, ExpectedGeneration: 1}

	cookie, csrf := l.a.cookie, l.a.csrf
	l.a.cookie = nil
	l.a.call("DELETE", path, body, nil, 401)
	l.a.cookie = cookie
	l.a.csrf = ""
	l.a.call("DELETE", path, body, nil, 403)
	l.a.csrf = csrf

	// A client-named host is not part of the contract: the master is resolved from the pool.
	l.a.call("DELETE", path, `{"request_id":"host-named","pool_id":"`+l.pool+`","expected_generation":1,"host_id":"someone-else"}`, nil, 400)
	// The host's own untagged bridge is not a pool VLAN and is never removable.
	l.a.call("DELETE", "/api/v1/networks/"+l.network, resourceLifecycleAPIRequest{RequestID: "remove-bridge", PoolID: l.pool, ExpectedGeneration: 1}, nil, 409)
	// A stale generation is refused with Core's own wording.
	stale := body
	stale.RequestID, stale.ExpectedGeneration = "remove-stale", 7
	l.a.call("DELETE", path, stale, nil, 409)
	// The network belongs to this pool only.
	other := l.a.enroll(l.a.token(l.a.site(), ""), csr(t))
	otherPool := l.a.poolOf(other.HostID)
	l.a.report(certificate(t, other.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: l.caps,
		Inventory: &protocol.Inventory{Hostname: "other", Managed: &protocol.ManagedState{}}})
	foreign := body
	foreign.RequestID, foreign.PoolID = "remove-foreign", otherPool
	l.a.call("DELETE", path, foreign, nil, 404)
	if cmds := reportManaged(l, func(m *protocol.ManagedState) {
		m.Networks = append(m.Networks, appliedVLAN(l, vlan))
	}).Commands; len(cmds) != 0 {
		t.Fatalf("a refused removal queued a command: %+v", cmds)
	}

	read(t, l.a.call("DELETE", path, body, nil, 202), &struct{}{})
	cmds := reportManaged(l, func(m *protocol.ManagedState) {
		m.Networks = append(m.Networks, appliedVLAN(l, vlan))
	}).Commands
	if len(cmds) != 1 || cmds[0].Kind != "network-remove" || cmds[0].ResourceLifecycle == nil {
		t.Fatalf("removal command: %+v", cmds)
	}
	got := *cmds[0].ResourceLifecycle
	if got.Resource != "network" || got.Action != "remove" || got.ResourceID != vlan.ID || got.PoolID != l.pool || got.HostID != l.host.HostID || got.ExpectedGeneration != 1 || got.Name != "" {
		t.Fatalf("removal payload: %+v", got)
	}
	// An identical retry replays the same operation and queues nothing new.
	var replay map[string]any
	read(t, l.a.call("DELETE", path, body, nil, 200), &replay)
	if replay["resource_id"] != vlan.ID {
		t.Fatalf("replay response: %+v", replay)
	}
	// Redelivery of the same dispatched command is at-least-once delivery; a second
	// operation would be a second removal.
	if again := reportManaged(l, nil).Commands; len(again) != 1 || again[0].OperationID != cmds[0].OperationID {
		t.Fatalf("replay queued another operation: %+v", again)
	}
	// A different body under the same request_id is a conflict, not a second removal.
	changed := body
	changed.ExpectedGeneration = 2
	l.a.call("DELETE", path, changed, nil, 409)

	// Core's refusal is the operator's answer, verbatim.
	const refusal = "network cannot be removed while used by VM 11111111-2222-3333-4444-555555555555 on host core-2"
	reportManaged(l, nil, protocol.Result{OperationID: cmds[0].OperationID, Status: "failed", Error: refusal})
	found := false
	for _, op := range l.operations() {
		if op.ID == cmds[0].OperationID {
			found = op.Status == "failed" && op.Error == refusal && op.Kind == "network-remove"
		}
	}
	if !found {
		t.Fatalf("Core's refusal did not reach the task: %+v", l.operations())
	}
	if len(networks(l)) != 2 {
		t.Fatal("a refused removal changed the network library")
	}
}

// Renaming relays too, and the new name is the master's to report back.
func TestPoolNetworkRenameRelaysAndIsMirroredFromTheMaster(t *testing.T) {
	l, vlan := vlanLab(t)
	path := "/api/v1/networks/" + vlan.ID
	l.a.call("PATCH", path, resourceLifecycleAPIRequest{RequestID: "rename-blank", PoolID: l.pool, ExpectedGeneration: 1}, nil, 400)
	l.a.call("PATCH", path, resourceLifecycleAPIRequest{RequestID: "rename-vlan", PoolID: l.pool, ExpectedGeneration: 1, Name: "Lab guests"}, nil, 202)
	cmds := reportManaged(l, func(m *protocol.ManagedState) {
		m.Networks = append(m.Networks, appliedVLAN(l, vlan))
	}).Commands
	if len(cmds) != 1 || cmds[0].Kind != "network-update" || cmds[0].ResourceLifecycle == nil || cmds[0].ResourceLifecycle.Name != "Lab guests" || cmds[0].ResourceLifecycle.HostID != l.host.HostID {
		t.Fatalf("rename command: %+v", cmds)
	}
	// The Controller does not rename anything itself; the master's next report does.
	for _, n := range networks(l) {
		if n.ID == vlan.ID && n.Name != "Guests" {
			t.Fatalf("Controller renamed its mirror before the host reported: %+v", n)
		}
	}
	renamed := appliedVLAN(l, vlan)
	renamed.Name, renamed.Generation = "Lab guests", 2
	reportManaged(l, func(m *protocol.ManagedState) { m.Networks = append(m.Networks, renamed) },
		protocol.Result{OperationID: cmds[0].OperationID, Status: "succeeded"})
	mirrored := false
	for _, n := range networks(l) {
		if n.ID == vlan.ID {
			mirrored = n.Name == "Lab guests" && n.Generation == 2 && n.Source == protocol.GuestBridge("nex0", 100)
		}
	}
	if !mirrored {
		t.Fatalf("rename not mirrored from the master's report: %+v", networks(l))
	}
}

// The tombstone is the only thing that removes a mirror row, it comes from the owning host,
// and nothing ever puts the row back.
func TestNetworkTombstoneDropsTheMirrorAndNeverResurrectsIt(t *testing.T) {
	l, vlan := vlanLab(t)
	applied := appliedVLAN(l, vlan)
	reportManaged(l, func(m *protocol.ManagedState) { m.Networks = append(m.Networks, applied) })
	if len(networks(l)) != 2 {
		t.Fatalf("VLAN missing before removal: %+v", networks(l))
	}
	// A member reporting the same tombstone must not delete the master's row.
	const member = "network-tombstone-member"
	if _, err := l.a.c.db.Exec("INSERT INTO hosts(id,pool_id,hostname) VALUES(?,?,?)", member, l.pool, "member"); err != nil {
		t.Fatal(err)
	}
	membership := &protocol.PoolMembership{PoolID: l.pool, MasterHostID: l.host.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: l.host.HostID, Hostname: l.host.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: member, Hostname: member, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"}}}
	tombstone := protocol.NetworkRegistration{ID: vlan.ID, PoolID: l.pool, HostID: l.host.HostID, Generation: 2, Tombstone: true}
	adopt := func(host string, state *protocol.ManagedState) {
		t.Helper()
		tx, err := l.a.c.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = l.a.c.adoptManaged(tx, host, &protocol.Inventory{Managed: state}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	adopt(member, &protocol.ManagedState{Membership: membership, Networks: []protocol.NetworkRegistration{tombstone}})
	if len(networks(l)) != 2 {
		t.Fatalf("a member's tombstone deleted the master's network: %+v", networks(l))
	}
	// Nor may a member that reports no membership at all: it must not re-home or rename the
	// master's row, which would then let its own tombstone delete it.
	hijack := applied
	hijack.Name = "Hijacked"
	adopt(member, &protocol.ManagedState{Networks: []protocol.NetworkRegistration{hijack}})
	adopt(member, &protocol.ManagedState{Networks: []protocol.NetworkRegistration{tombstone}})
	kept := false
	for _, n := range networks(l) {
		if n.ID == vlan.ID {
			kept = n.Name == vlan.Name && n.HostID == l.host.HostID
		}
	}
	if !kept {
		t.Fatalf("a member without membership changed or removed the master's network: %+v", networks(l))
	}
	// The master's tombstone does remove it.
	reportManaged(l, func(m *protocol.ManagedState) {
		m.Networks = append(m.Networks, tombstone)
		m.Membership = membership
	})
	for _, n := range networks(l) {
		if n.ID == vlan.ID {
			t.Fatalf("tombstoned network still mirrored: %+v", n)
		}
	}
	// A later report that no longer lists the network at all must not bring it back, and
	// neither may a repeat of the tombstone.
	reportManaged(l, func(m *protocol.ManagedState) { m.Membership = membership })
	reportManaged(l, func(m *protocol.ManagedState) {
		m.Networks = append(m.Networks, tombstone)
		m.Membership = membership
	})
	if len(networks(l)) != 1 {
		t.Fatalf("removed network came back: %+v", networks(l))
	}
}

// importedISO is a succeeded ISO import on the lab host, the state an ISO removal starts from.
func importedISO(t *testing.T, l *managedLab) protocol.Media {
	t.Helper()
	v := mediaDownloadRequest{RequestID: "iso-request", StorageID: l.storage, Name: "installer.iso", URL: "https://example.com/installer.iso"}
	var media protocol.Media
	read(t, l.a.call("POST", "/api/v1/media/download", v, nil, 201), &media)
	if cmds := l.report().Commands; len(cmds) != 1 {
		t.Fatalf("import not dispatched: %+v", cmds)
	}
	l.report(protocol.Result{OperationID: media.OperationID, Status: "succeeded"})
	return media
}

func TestImportedISORemovalRelaysToItsHost(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.ISODownloadCapability)
	media := importedISO(t, l)
	path := "/api/v1/media/" + media.ID
	// An imported ISO needs a stable request_id; the old bodyless call is not enough.
	l.a.call("DELETE", path, nil, nil, 400)
	l.a.call("DELETE", path, map[string]string{"request_id": ""}, nil, 400)
	l.a.call("DELETE", path, map[string]string{"request_id": "remove-iso"}, nil, 202)
	cmds := l.report().Commands
	if len(cmds) != 1 || cmds[0].Kind != "media-remove" || cmds[0].ResourceLifecycle == nil {
		t.Fatalf("ISO removal command: %+v", cmds)
	}
	got := *cmds[0].ResourceLifecycle
	if got.Resource != "media" || got.Action != "remove" || got.ResourceID != media.ID || got.PoolID != l.pool || got.HostID != l.host.HostID || got.ExpectedGeneration != 1 {
		t.Fatalf("ISO removal payload: %+v", got)
	}
	l.a.call("DELETE", path, map[string]string{"request_id": "remove-iso"}, nil, 200)
	if extra := l.report().Commands; len(extra) != 1 || extra[0].OperationID != cmds[0].OperationID {
		t.Fatalf("replay queued another operation: %+v", extra)
	}
	// Core's retained journal outcome is the other route a refusal takes to the task list,
	// and it is the only one a replacement Controller ever sees.
	const refusal = "ISO cannot be removed while attached to VM 11111111-2222-3333-4444-555555555555"
	reportManaged(l, func(m *protocol.ManagedState) {
		m.Outcomes = []protocol.ManagedOutcome{{OperationID: cmds[0].OperationID, Kind: "media-remove", ID: media.ID, Name: "installer.iso", Status: "failed", Error: refusal}}
	})
	found := false
	for _, op := range l.operations() {
		if op.ID == cmds[0].OperationID {
			found = op.Status == "failed" && op.Error == refusal
		}
	}
	if !found {
		t.Fatal("Core's ISO refusal did not reach the task")
	}
	if len(mediaLibrary(l)) != 1 {
		t.Fatal("a refused ISO removal changed the library")
	}
	// The tombstone from the owning host is what drops the row; a member's does not.
	const member = "media-tombstone-member"
	if _, err := l.a.c.db.Exec("INSERT INTO hosts(id,pool_id,hostname) VALUES(?,?,?)", member, l.pool, "member"); err != nil {
		t.Fatal(err)
	}
	membership := &protocol.PoolMembership{PoolID: l.pool, MasterHostID: l.host.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: l.host.HostID, Hostname: l.host.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: member, Hostname: member, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"}}}
	tombstone := protocol.Media{ID: media.ID, PoolID: l.pool, HostID: l.host.HostID, Generation: 2, Tombstone: true}
	tx, err := l.a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = l.a.c.adoptManaged(tx, member, &protocol.Inventory{Managed: &protocol.ManagedState{Membership: membership, Media: []protocol.Media{tombstone}}}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(mediaLibrary(l)) != 1 {
		t.Fatal("a member's tombstone deleted the owning host's ISO")
	}
	reportManaged(l, func(m *protocol.ManagedState) { m.Media = append(m.Media, tombstone) })
	if len(mediaLibrary(l)) != 0 {
		t.Fatalf("tombstoned ISO still listed: %+v", mediaLibrary(l))
	}
	// Absence, and a repeated tombstone, keep it gone.
	reportManaged(l, nil)
	reportManaged(l, func(m *protocol.ManagedState) { m.Media = append(m.Media, tombstone) })
	if len(mediaLibrary(l)) != 0 {
		t.Fatalf("removed ISO came back: %+v", mediaLibrary(l))
	}
}

// The failed-import discard keeps working exactly as the library UI calls it: no body at all.
func TestFailedImportDiscardStillTakesNoBody(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.ISODownloadCapability)
	v := mediaDownloadRequest{RequestID: "failed-iso", StorageID: l.storage, Name: "installer.iso", URL: "https://example.com/installer.iso"}
	var media protocol.Media
	read(t, l.a.call("POST", "/api/v1/media/download", v, nil, 201), &media)
	l.report()
	l.report(protocol.Result{OperationID: media.OperationID, Status: "failed", Error: "download refused"})
	l.a.call("DELETE", "/api/v1/media/"+media.ID, nil, nil, 200)
	if len(mediaLibrary(l)) != 0 {
		t.Fatalf("failed import not discarded: %+v", mediaLibrary(l))
	}
	if cmds := l.report().Commands; len(cmds) != 0 {
		t.Fatalf("a local discard queued a host command: %+v", cmds)
	}
}
