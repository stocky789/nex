// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestReleaseRetainedOriginalsRouteQueuesHostCommand(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	var created createdVM
	read(t, l.create("create-release", l.spec("guest-release"), 202), &created)
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	path := "/api/v1/vms/" + created.VMID + "/release-retained"
	body := map[string]any{"request_id": "release-1", "confirmation": "guest-release"}
	l.a.call("POST", path, map[string]any{"request_id": "release-x"}, nil, 400)
	l.a.call("POST", "/api/v1/vms/missing/release-retained", body, nil, 404)
	var first map[string]any
	read(t, l.a.call("POST", path, body, nil, 202), &first)
	if first["operation"].(map[string]any)["kind"] != "vm-release-retained" {
		t.Fatalf("queued %v", first)
	}
	l.a.call("POST", path, body, nil, 200)
	if w := l.a.call("POST", path, map[string]any{"request_id": "release-2", "confirmation": "guest-release"}, nil, 409); !strings.Contains(w.Body.String(), "still pending") {
		t.Fatal(w.Body.String())
	}
	commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	if len(commands) != 1 || commands[0].Kind != "vm-release-retained" || commands[0].ReleaseRetained == nil ||
		commands[0].ReleaseRetained.VMID != created.VMID || commands[0].ReleaseRetained.Confirmation != "guest-release" {
		t.Fatalf("dispatched %+v", commands)
	}
}

// A queued restore-replace has not retained the originals yet; a release accepted now would
// run right after it and delete the rollback copies.
func TestReleaseRetainedRefusedWhileRestoreReplaceQueued(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	var created createdVM
	read(t, l.create("create-release-restore", l.spec("guest-restore"), 202), &created)
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	var pool, host string
	if err := l.a.c.db.QueryRow("SELECT pool_id,host_id FROM managed_resources WHERE kind='vm-create'").Scan(&pool, &host); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"backup-restore-replace", "backup-test-restore"} {
		now := stamp()
		_, err := l.a.c.db.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", "op-"+kind, host, kind, "req-"+kind, "queued", "", now, now, "root")
		if err == nil {
			_, err = l.a.c.db.Exec("INSERT INTO managed_resources VALUES(?,?,?,?,?,?,?,?,?)", "res-"+kind, kind, pool, host, created.VMID, "req-"+kind, "{}", "{}", "op-"+kind)
		}
		if err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"request_id": "release-" + kind, "confirmation": "guest-restore"}
		if w := l.a.call("POST", "/api/v1/vms/"+created.VMID+"/release-retained", body, nil, 409); !strings.Contains(w.Body.String(), "restore request") {
			t.Fatalf("%s: %s", kind, w.Body.String())
		}
		l.a.c.db.Exec("DELETE FROM operations WHERE id=?", "op-"+kind)
	}
}

// A release accepted by Core's local API is adopted as an in-flight mirror. A Controller-queued
// mirror made the agent's result for it a 409 forever and wedged the host.
func TestCoreLocalReleaseRetainedMirrorAcceptsAgentResult(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	outcome := func(status string) *protocol.Inventory {
		return &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{{
			OperationID: "core-release-1", Kind: "vm-release-retained", ID: "11111111-1111-4111-8111-111111111111", Status: status,
		}}}}
	}
	capabilities := []string{protocol.ManagedVMCapability}
	response := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: capabilities, Inventory: outcome("queued")})
	if len(response.Commands) != 0 {
		t.Fatalf("adopted release was dispatched: %#v", response.Commands)
	}
	status := func() string {
		var s string
		if err := a.c.db.QueryRow("SELECT status FROM operations WHERE id='core-release-1'").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := status(); s != "dispatched" {
		t.Fatalf("adopted queued release is %q", s)
	}
	// a.report requires 200: the result must not be answered 409 "not delivered".
	response = a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: capabilities, Inventory: outcome("succeeded"),
		Results: []protocol.Result{{OperationID: "core-release-1", Status: "succeeded"}}})
	if len(response.AcknowledgedResults) != 0 && response.AcknowledgedResults[0] != "core-release-1" {
		t.Fatalf("acknowledged %v", response.AcknowledgedResults)
	}
	if s := status(); s != "succeeded" {
		t.Fatalf("release mirror is %q", s)
	}
}
