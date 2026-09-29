// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestFreshControllerRetainsMasterSnapshotCatalogObservation(t *testing.T) {
	a := newClient(t)
	site := a.site()
	master := a.enroll(a.token(site, ""), csr(t))
	member := a.enroll(a.token(site, ""), csr(t))
	membership := &protocol.PoolMembership{PoolID: a.poolOf(master.HostID), MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
	}}
	snapshot := protocol.PoolVMSnapshot{ID: "snapshot-1", VMID: "11111111-1111-4111-8111-111111111111", Name: "Before update", Description: "Original description", CreatedAt: time.Now().UTC(), SpecRevision: 2, SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64)}
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Membership: membership, PoolVMSnapshots: []protocol.PoolVMSnapshot{snapshot}, PoolVMSnapshotsReported: true}}
	report := protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv}
	a.report(certificate(t, master.CertificatePEM), report)
	var raw string
	if err := a.c.db.QueryRow("SELECT inventory FROM hosts WHERE id=?", master.HostID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved protocol.Inventory
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Managed == nil || !saved.Managed.PoolVMSnapshotsReported || len(saved.Managed.PoolVMSnapshots) != 1 || saved.Managed.PoolVMSnapshots[0] != snapshot {
		t.Fatalf("fresh Controller lost Core catalog identity or metadata: %#v", saved.Managed)
	}
	// A member cannot report a master's authoritative catalog under its own certificate.
	a.call("POST", "/api/v1/agent/report", report, certificate(t, member.CertificatePEM), 400)
	inv.Managed.PoolVMSnapshotsReported = false
	if validateInventory(inv) == nil {
		t.Fatal("accepted nonempty catalog without complete observation")
	}
	inv.Managed.PoolVMSnapshotsReported = true
	inv.Managed.PoolVMSnapshots = append(inv.Managed.PoolVMSnapshots, snapshot)
	if validateInventory(inv) == nil {
		t.Fatal("accepted duplicate snapshot identity")
	}
}

func TestCoreSnapshotOutcomeAdoptionRequiresExplicitSettlement(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{}}
	for i, status := range []string{"needs_attention", "", "succeeded", "needs_attention"} {
		inv.Managed.Outcomes = nil
		if status != "" {
			inv.Managed.Outcomes = []protocol.ManagedOutcome{{OperationID: "snapshot-1", Kind: "snapshot-create", ID: "11111111-1111-4111-8111-111111111111", Name: "Before update", Status: status}}
		}
		response := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: uint64(i + 1), Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
		if len(response.Commands) != 0 {
			t.Fatal("Controller redispatched a Core-owned snapshot")
		}
		var operations []protocol.Operation
		read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
		want := "needs_attention"
		if i >= 2 {
			want = "succeeded"
		}
		found := false
		for _, operation := range operations {
			if operation.RequestID == "adopt-outcome:"+host.HostID+":snapshot-1" {
				found = true
				if operation.ID != adoptedSnapshotOperationID(host.HostID, "snapshot-1") || operation.Kind != "snapshot-create" || operation.Status != want || operation.HostID != host.HostID {
					t.Fatalf("step %d snapshot adoption: %#v; want %s", i, operation, want)
				}
			}
		}
		if !found {
			t.Fatal("Core snapshot history was not adopted")
		}
	}
}

func TestCoreSnapshotRevertOutcomeAdoptionRequiresExplicitSettlement(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{}}
	for i, status := range []string{"needs_attention", "", "succeeded"} {
		inv.Managed.Outcomes = nil
		if status != "" {
			inv.Managed.Outcomes = []protocol.ManagedOutcome{{OperationID: "revert-1", Kind: "snapshot-revert", ID: "11111111-1111-4111-8111-111111111111", Status: status}}
		}
		response := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: uint64(i + 1), Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
		if len(response.Commands) != 0 {
			t.Fatal("Controller redispatched a Core-owned snapshot revert")
		}
		var operations []protocol.Operation
		read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
		want := "needs_attention"
		if i == 2 {
			want = "succeeded"
		}
		found := false
		for _, operation := range operations {
			if operation.RequestID == "adopt-outcome:"+host.HostID+":revert-1" {
				found = true
				if operation.ID != adoptedSnapshotOperationID(host.HostID, "revert-1") || operation.Kind != "snapshot-revert" || operation.Status != want || operation.HostID != host.HostID {
					t.Fatalf("step %d snapshot-revert adoption: %#v; want %s", i, operation, want)
				}
			}
		}
		if !found {
			t.Fatal("Core snapshot-revert history was not adopted")
		}
	}
}

func TestSnapshotOutcomeIDsAreScopedToCoreHost(t *testing.T) {
	a := newClient(t)
	site := a.site()
	first := a.enroll(a.token(site, ""), csr(t))
	second := a.enroll(a.token(site, ""), csr(t))
	coreOperationID := "same-core-id"
	firstProjectionID := adoptedSnapshotOperationID(first.HostID, coreOperationID)
	for _, item := range []struct {
		host        protocol.EnrollResponse
		sequence    uint64
		kind        string
		operationID string
	}{
		{host: first, sequence: 1, kind: "snapshot-create", operationID: coreOperationID},
		// A Core ID can equal an earlier adopted Controller projection ID. It is
		// still a distinct host-local operation and must receive its own projection.
		{host: first, sequence: 2, kind: "snapshot-create", operationID: firstProjectionID},
		{host: second, sequence: 1, kind: "snapshot-create", operationID: coreOperationID},
		{host: first, sequence: 3, kind: "snapshot-revert", operationID: "same-revert-id"},
		{host: second, sequence: 2, kind: "snapshot-revert", operationID: "same-revert-id"},
	} {
		inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{{
			OperationID: item.operationID, Kind: item.kind, ID: "11111111-1111-4111-8111-111111111111",
			Name: "Before update", Status: "succeeded",
		}}}}
		a.report(certificate(t, item.host.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: item.sequence, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	}

	var operations []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	firstRevertProjectionID := adoptedSnapshotOperationID(first.HostID, "same-revert-id")
	want := map[string]protocol.Operation{
		"adopt-outcome:" + first.HostID + ":" + coreOperationID:   {ID: firstProjectionID, HostID: first.HostID, Kind: "snapshot-create"},
		"adopt-outcome:" + first.HostID + ":" + firstProjectionID: {ID: adoptedSnapshotOperationID(first.HostID, firstProjectionID), HostID: first.HostID, Kind: "snapshot-create"},
		"adopt-outcome:" + second.HostID + ":" + coreOperationID:  {ID: adoptedSnapshotOperationID(second.HostID, coreOperationID), HostID: second.HostID, Kind: "snapshot-create"},
		"adopt-outcome:" + first.HostID + ":same-revert-id":       {ID: firstRevertProjectionID, HostID: first.HostID, Kind: "snapshot-revert"},
		"adopt-outcome:" + second.HostID + ":same-revert-id":      {ID: adoptedSnapshotOperationID(second.HostID, "same-revert-id"), HostID: second.HostID, Kind: "snapshot-revert"},
	}
	for requestID, expected := range want {
		found := false
		for _, operation := range operations {
			if operation.RequestID != requestID {
				continue
			}
			found = true
			if operation.ID != expected.ID || operation.HostID != expected.HostID || operation.Kind != expected.Kind || operation.Status != "succeeded" {
				t.Fatalf("snapshot outcome %q adopted as %#v; want %#v", requestID, operation, expected)
			}
		}
		if !found {
			t.Fatalf("snapshot outcome %q was lost", requestID)
		}
	}
	if want["adopt-outcome:"+first.HostID+":"+coreOperationID].ID == want["adopt-outcome:"+second.HostID+":"+coreOperationID].ID {
		t.Fatal("host-scoped snapshot operation IDs collided")
	}
	if want["adopt-outcome:"+first.HostID+":same-revert-id"].ID == want["adopt-outcome:"+second.HostID+":same-revert-id"].ID {
		t.Fatal("host-scoped snapshot-revert operation IDs collided")
	}
}

func TestControllerSnapshotOutcomeSettlesOwnOperationWithoutAdoptGhost(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	var created createdVM
	read(t, l.create("create-snap", l.spec("snap-guest"), 202), &created)
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	var queued map[string]any
	read(t, l.a.call("POST", "/api/v1/vms/"+created.VMID+"/snapshots", map[string]string{
		"request_id": "ctrl-snap-1", "name": "Before update",
	}, nil, 202), &queued)
	op := queued["operation"].(map[string]any)
	if op["id"] != "ctrl-snap-1" || op["kind"] != "snapshot-create" || op["status"] != "queued" {
		t.Fatalf("queued snapshot %#v", queued)
	}
	if w := l.a.call("POST", "/api/v1/vms/"+created.VMID+"/snapshots", map[string]string{
		"request_id": "ctrl-snap-2", "name": "Another",
	}, nil, 409); !strings.Contains(w.Body.String(), "still pending") {
		t.Fatalf("pending snapshot gate: %s", w.Body.String())
	}

	l.seq++
	defaults := hostDefaults(l.host.HostID)
	defaults.VMs = []protocol.VMCreateSpec{applied}
	defaults.Outcomes = []protocol.ManagedOutcome{{
		OperationID: "ctrl-snap-1", Kind: "snapshot-create", ID: created.VMID, Name: "Before update", Status: "queued",
	}}
	inv := &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30, NICs: []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}},
		Managed: defaults, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: time.Now().UTC()}},
		VMs: []protocol.VM{{ID: created.VMID, Name: "snap-guest", State: "shut off"}}}
	response := l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv})
	if len(response.Commands) != 1 || response.Commands[0].OperationID != "ctrl-snap-1" || response.Commands[0].Kind != "snapshot-create" {
		t.Fatalf("report commands %#v", response.Commands)
	}

	var operations []protocol.Operation
	read(t, l.a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	var snapRows int
	for _, operation := range operations {
		if operation.ID == "ctrl-snap-1" || strings.Contains(operation.RequestID, "ctrl-snap-1") {
			snapRows++
			if operation.ID != "ctrl-snap-1" || operation.RequestID != "ctrl-snap-1" || operation.Kind != "snapshot-create" {
				t.Fatalf("extra or remapped snapshot row %#v", operation)
			}
		}
		if strings.HasPrefix(operation.RequestID, "adopt-outcome:") && strings.Contains(operation.RequestID, "ctrl-snap-1") {
			t.Fatalf("adopt ghost for Controller snapshot: %#v", operation)
		}
	}
	if snapRows != 1 {
		t.Fatalf("expected one Controller snapshot operation, got %d in %#v", snapRows, operations)
	}

	l.seq++
	defaults.Outcomes = []protocol.ManagedOutcome{{
		OperationID: "ctrl-snap-1", Kind: "snapshot-create", ID: created.VMID, Name: "Before update", Status: "succeeded",
	}}
	inv.Managed = defaults
	response = l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv,
		Results: []protocol.Result{{OperationID: "ctrl-snap-1", Status: "succeeded"}}})
	if len(response.Commands) != 0 {
		t.Fatalf("settled snapshot still dispatched %#v", response.Commands)
	}
	read(t, l.a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	for _, operation := range operations {
		if operation.ID == "ctrl-snap-1" && operation.Status != "succeeded" {
			t.Fatalf("own snapshot not settled: %#v", operation)
		}
	}
}

func TestQueuedAdoptedSnapshotOutcomesAreNotDispatched(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{{
		OperationID: "core-queued-snap", Kind: "snapshot-create", ID: "11111111-1111-4111-8111-111111111111",
		Name: "Before update", Status: "queued",
	}}}}
	response := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	if len(response.Commands) != 0 {
		t.Fatalf("adopted queued snapshot was dispatched: %#v", response.Commands)
	}
	var operations []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	found := false
	for _, operation := range operations {
		if operation.RequestID == "adopt-outcome:"+host.HostID+":core-queued-snap" {
			found = true
			if operation.Status != "queued" || operation.ID != adoptedSnapshotOperationID(host.HostID, "core-queued-snap") {
				t.Fatalf("adopted queued snapshot %#v", operation)
			}
		}
	}
	if !found {
		t.Fatal("queued Core snapshot was not adopted")
	}
	response = a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	if len(response.Commands) != 0 {
		t.Fatalf("second report redispatched adopt-outcome: %#v", response.Commands)
	}
}

func TestSnapshotMetadataNeedsAttentionIsNotAutoSucceeded(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{{
		OperationID: "meta-1", Kind: "snapshot-metadata", ID: "11111111-1111-4111-8111-111111111111",
		Name: "Before update", Status: "needs_attention", Error: "catalog write interrupted",
	}}}}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	inv.Managed.Outcomes = nil
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	var operations []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	found := false
	for _, operation := range operations {
		if operation.RequestID == "adopt-outcome:"+host.HostID+":meta-1" {
			found = true
			if operation.Status != "needs_attention" || operation.Kind != "snapshot-metadata" {
				t.Fatalf("snapshot-metadata auto-succeeded: %#v", operation)
			}
		}
	}
	if !found {
		t.Fatal("snapshot-metadata outcome was not adopted")
	}
}

func TestRemappedSnapshotIDConflictRecordsAttention(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	coreID := "conflict-core"
	projection := adoptedSnapshotOperationID(host.HostID, coreID)
	now := stamp()
	if _, err := a.c.db.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", projection, host.HostID, "vm-create", "other-request", "succeeded", "", now, now, "system"); err != nil {
		t.Fatal(err)
	}
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{{
		OperationID: coreID, Kind: "snapshot-create", ID: "11111111-1111-4111-8111-111111111111",
		Name: "Before update", Status: "succeeded",
	}}}}
	response := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	if len(response.Commands) != 0 {
		t.Fatalf("conflict report redispatched commands: %#v", response.Commands)
	}
	var operations []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	attention := false
	for _, operation := range operations {
		if operation.RequestID == "adopt-attention:"+host.HostID+":snapshot:"+coreID {
			attention = true
			if operation.Status != "needs_attention" || !strings.Contains(operation.Error, "conflicts with existing history") {
				t.Fatalf("attention %#v", operation)
			}
		}
		if operation.RequestID == "adopt-outcome:"+host.HostID+":"+coreID {
			t.Fatalf("conflict still inserted adopt-outcome: %#v", operation)
		}
	}
	if !attention {
		t.Fatalf("missing adopt attention for remapped id conflict: %#v", operations)
	}
}
