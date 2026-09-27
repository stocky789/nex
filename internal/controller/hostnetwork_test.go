// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// labSpec is a management configuration in the reserved example range.
func labSpec() protocol.ManagementNetworkSpec {
	return protocol.ManagementNetworkSpec{
		Interfaces: []protocol.ManagementNIC{{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "static", VLAN: 10, Address: "192.0.2.10/24", Gateway: "192.0.2.1", Management: true}},
		DNS:        []string{"192.0.2.53"},
	}
}

// newNetworkLab is one enrolled host that supports staged management-network edits.
func newNetworkLab(t *testing.T) *joinLab {
	t.Helper()
	l := newJoinLab(t)
	l.member.caps = append(l.member.caps, protocol.ManagementNetworkCapability)
	l.send(&l.member, nil)
	return l
}

func (l *joinLab) networkPath() string { return "/api/v1/hosts/" + l.member.id + "/network" }

func (l *joinLab) hostNetwork(t *testing.T) hostNetworkView {
	t.Helper()
	var out hostNetworkView
	read(t, l.a.call("GET", l.networkPath(), nil, nil, 200), &out)
	return out
}

// TestHostNetworkCommandReachesItsHost is the relay: one durable command, addressed to the
// host whose configuration it changes, carrying the spec Core renders.
func TestHostNetworkCommandReachesItsHost(t *testing.T) {
	l := newNetworkLab(t)
	var accepted struct {
		HostID    string             `json:"host_id"`
		Action    string             `json:"action"`
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.networkPath(), map[string]any{"request_id": "net-1", "action": "stage", "spec": labSpec(), "window_seconds": 600}, nil, 202), &accepted)
	if accepted.HostID != l.member.id || accepted.Action != "stage" || accepted.Operation.Kind != "management-network" || accepted.Operation.HostID != l.member.id {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	if commands := l.send(&l.master, nil).Commands; len(commands) != 0 {
		t.Fatalf("another host was sent the command: %+v", commands)
	}
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].ManagementNetwork == nil {
		t.Fatalf("the host did not receive the command: %+v", commands)
	}
	request := commands[0].ManagementNetwork
	if commands[0].Kind != "management-network" || request.HostID != l.member.id || request.Action != "stage" || request.WindowSeconds != 600 || len(request.Spec.Interfaces) != 1 || request.Spec.Interfaces[0].Address != "192.0.2.10/24" {
		t.Fatalf("unexpected command: %+v", request)
	}
	if request.OperationID != commands[0].OperationID {
		t.Fatalf("request and command disagree on the operation: %+v", request)
	}
	if err := request.Valid(); err != nil {
		t.Fatalf("delivered request is invalid: %v", err)
	}
	// Exact retry replays the same operation; a changed payload conflicts.
	var replay struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.networkPath(), map[string]any{"request_id": "net-1", "action": "stage", "spec": labSpec(), "window_seconds": 600}, nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exact retry did not replay: %+v", replay)
	}
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "net-1", "action": "cancel"}, nil, 409)
	var audits []protocol.Audit
	read(t, l.a.call("GET", "/api/v1/audit", nil, nil, 200), &audits)
	rows := 0
	for _, entry := range audits {
		if entry.Action == "management-network" {
			rows++
			if entry.ResourceID != "management-network:"+l.member.id+":"+accepted.Operation.ID {
				t.Fatalf("unexpected audit row: %+v", entry)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("expected one management-network audit row, got %d", rows)
	}
}

func TestHostNetworkRefusalMatrix(t *testing.T) {
	l := newNetworkLab(t)
	// 400: no request_id, an unknown action, a stage with no spec, a window out of bounds.
	l.a.call("POST", l.networkPath(), map[string]any{"action": "stage", "spec": labSpec()}, nil, 400)
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "bad-action", "action": "reboot"}, nil, 400)
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "no-spec", "action": "stage"}, nil, 400)
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "bad-window", "action": "cancel", "window_seconds": 99999}, nil, 400)
	// 404: unknown host, on both routes.
	l.a.call("POST", "/api/v1/hosts/no-such-host/network", map[string]any{"request_id": "missing", "action": "cancel"}, nil, 404)
	l.a.call("GET", "/api/v1/hosts/no-such-host/network", nil, nil, 404)
	// 409: a host that does not advertise the capability.
	w := l.a.call("POST", "/api/v1/hosts/"+l.master.id+"/network", map[string]any{"request_id": "no-capability", "action": "cancel"}, nil, 409)
	if !strings.Contains(w.Body.String(), protocol.ManagementNetworkCapability) {
		t.Fatalf("capability refusal does not name the capability: %s", w.Body.String())
	}
	// 409: the revision the operator saw is not the one the host reports.
	l.member.inv.ManagementNetwork = &protocol.ManagementNetworkStatus{Revision: 3, Stage: protocol.ManagementNetworkConfirmed, ObservedAt: time.Now().UTC()}
	l.send(&l.member, nil)
	w = l.a.call("POST", l.networkPath(), map[string]any{"request_id": "stale-revision", "action": "stage", "spec": labSpec(), "expected_revision": 2}, nil, 409)
	if !strings.Contains(w.Body.String(), "revision 3") {
		t.Fatalf("revision refusal does not name the reported revision: %s", w.Body.String())
	}
}

// TestHostNetworkApplyNeedsTheTypedHostname: the one edit that can strand a host names its
// target at this boundary too, not only in Core.
func TestHostNetworkApplyNeedsTheTypedHostname(t *testing.T) {
	l := newNetworkLab(t)
	// An apply acts on the change the host reports as staged; the Controller takes its id
	// from that report and never from its own memory.
	l.member.inv.ManagementNetwork = &protocol.ManagementNetworkStatus{Revision: 1, Stage: protocol.ManagementNetworkStaged, OperationID: "op-stage", WindowSeconds: 600, ObservedAt: time.Now().UTC()}
	l.send(&l.member, nil)
	// Valid() refuses an apply with no confirmation at all.
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "no-confirmation", "action": "apply"}, nil, 400)
	w := l.a.call("POST", l.networkPath(), map[string]any{"request_id": "wrong-confirmation", "action": "apply", "confirmation": "some-other-host"}, nil, 400)
	if !strings.Contains(w.Body.String(), l.member.inv.Hostname) {
		t.Fatalf("the refusal does not name the host: %s", w.Body.String())
	}
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "apply-1", "action": "apply", "confirmation": strings.ToUpper(l.member.inv.Hostname) + " "}, nil, 202)
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].ManagementNetwork == nil {
		t.Fatalf("the host did not receive the apply: %+v", commands)
	}
	// Core checks the confirmation itself, so it must ride in the command unchanged.
	if got := commands[0].ManagementNetwork.Confirmation; got != strings.ToUpper(l.member.inv.Hostname)+" " {
		t.Fatalf("confirmation was not forwarded verbatim: %q", got)
	}
	if got := commands[0].ManagementNetwork.ChangeID; got != "op-stage" {
		t.Fatalf("the apply targets change %q, not the one the host reports", got)
	}
}

// TestHostNetworkConfirmWaitsForTheHostToReportAgain is the outage rule: after an apply the
// host may come back on a new address, and confirm becomes possible the moment it does.
func TestHostNetworkConfirmWaitsForTheHostToReportAgain(t *testing.T) {
	l := newNetworkLab(t)
	l.member.inv.ManagementNetwork = &protocol.ManagementNetworkStatus{Revision: 1, Stage: protocol.ManagementNetworkApplied, OperationID: "op-apply", Deadline: time.Now().Add(9 * time.Minute).UTC(), SecondsRemaining: 540, ObservedAt: time.Now().UTC()}
	l.send(&l.member, nil)
	l.forget(&l.member)
	// While it is quiet: stage, apply and confirm are refused; cancel is not, because a
	// durable cancel is how a staged change is undone on a host that has gone silent.
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "stage-quiet", "action": "stage", "spec": labSpec()}, nil, 409)
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "apply-quiet", "action": "apply", "confirmation": l.member.inv.Hostname}, nil, 409)
	w := l.a.call("POST", l.networkPath(), map[string]any{"request_id": "confirm-quiet", "action": "confirm"}, nil, 409)
	if !strings.Contains(w.Body.String(), "has not reported since the change was applied") {
		t.Fatalf("unexpected confirm refusal: %s", w.Body.String())
	}
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "cancel-quiet", "action": "cancel"}, nil, 202)
	// The deadline stays visible while the host is quiet, as unknown rather than as an
	// outcome, which is exactly what the countdown must show.
	view := l.hostNetwork(t)
	if view.Connection == "fresh" || view.State != "unknown" || view.Record == nil || view.Record.Stage != protocol.ManagementNetworkApplied || view.Record.Deadline.IsZero() {
		t.Fatalf("a quiet host's countdown was not reported as unknown-with-deadline: %+v", view)
	}
	// It reports again, from wherever it now is: confirm is immediately possible.
	l.send(&l.member, nil)
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "confirm-1", "action": "confirm"}, nil, 202)
	commands := l.send(&l.member, nil).Commands
	actions := []string{}
	for _, cmd := range commands {
		if cmd.ManagementNetwork != nil {
			actions = append(actions, cmd.ManagementNetwork.Action)
		}
	}
	if len(actions) != 2 || actions[0] != "cancel" || actions[1] != "confirm" {
		t.Fatalf("unexpected delivered actions: %v", actions)
	}
}

// TestHostNetworkProjection: fresh reads the host's record, stale reads "unknown", and a
// Core conflict is marked by its agreed suffix and nothing else.
func TestHostNetworkProjection(t *testing.T) {
	l := newNetworkLab(t)
	view := l.hostNetwork(t)
	if !view.Supported || view.Connection != "fresh" || view.State != "none" || view.Record != nil || len(view.NICs) == 0 || view.LastOperation != nil {
		t.Fatalf("unexpected empty projection: %+v", view)
	}
	if view.Hostname != l.member.inv.Hostname {
		t.Fatalf("the projection does not report the host's own name: %q", view.Hostname)
	}
	l.member.inv.ManagementNetwork = &protocol.ManagementNetworkStatus{Revision: 2, Stage: protocol.ManagementNetworkStaged, OperationID: "op-stage", ObservedGateway: "192.0.2.1", ObservedAt: time.Now().UTC()}
	l.member.inv.Checks = []protocol.Check{{Name: "management-network", Status: "failed", Error: "the rollback restored the previous configuration", ObservedAt: time.Now().UTC()}}
	l.send(&l.member, nil)
	if view = l.hostNetwork(t); view.State != protocol.ManagementNetworkStaged || view.Record == nil || view.Record.Revision != 2 || view.Record.ObservedGateway != "192.0.2.1" || view.Check == nil || view.Check.Status != "failed" {
		t.Fatalf("the host's record was not projected: %+v", view)
	}
	// A host without the capability offers no control at all.
	var master hostNetworkView
	read(t, l.a.call("GET", "/api/v1/hosts/"+l.master.id+"/network", nil, nil, 200), &master)
	if master.Supported {
		t.Fatal("a host without management-network-v1 was reported as supporting it")
	}
	// A Core conflict ends with the agreed suffix; nothing else is a conflict.
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "apply-1", "action": "apply", "confirmation": l.member.inv.Hostname}, nil, 202)
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 {
		t.Fatalf("the host did not receive the apply: %+v", commands)
	}
	l.send(&l.member, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "failed", Error: "management network revision is 3, not 2: " + protocol.ManagementNetworkConflict, ObservedAt: time.Now().UTC()}}
	})
	last := l.hostNetwork(t).LastOperation
	if last == nil || last.Action != "apply" || last.Status != "failed" || !last.Conflict {
		t.Fatalf("Core's conflict was not surfaced: %+v", last)
	}
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "cancel-1", "action": "cancel"}, nil, 202)
	commands = l.send(&l.member, nil).Commands
	l.send(&l.member, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "failed", Error: "the confirmation does not name this host", ObservedAt: time.Now().UTC()}}
	})
	if last = l.hostNetwork(t).LastOperation; last == nil || last.Conflict {
		t.Fatalf("a plain refusal was marked as a conflict: %+v", last)
	}
}

// TestHostNetworkSpecIsOrdinaryCommandData: there is no secret in a network spec, so it is
// stored like any other command and is not scrubbed. The audit row still holds none of it.
func TestHostNetworkSpecIsOrdinaryCommandData(t *testing.T) {
	l := newNetworkLab(t)
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "net-1", "action": "stage", "spec": labSpec()}, nil, 202)
	var raw string
	if err := l.a.c.db.QueryRow("SELECT command FROM managed_resources WHERE kind='management-network'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "192.0.2.10/24") {
		t.Fatalf("the staged spec was not stored for delivery: %s", raw)
	}
	var audits []protocol.Audit
	read(t, l.a.call("GET", "/api/v1/audit", nil, nil, 200), &audits)
	for _, entry := range audits {
		if strings.Contains(entry.ResourceID, "192.0.2.10") || strings.Contains(entry.Detail, "192.0.2.10") {
			t.Fatalf("an audit row carries the spec: %+v", entry)
		}
	}
}
