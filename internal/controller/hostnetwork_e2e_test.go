// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// The staged management-network edit spans three layers, and each one passed its own unit
// suite while the whole flow could not work: the Controller mints a fresh operation id per
// durable command, and Core matches apply/confirm/cancel against the staged change's id. This
// check crosses the layers on purpose — every command the Controller queues is executed by the
// real agent against the real engine, on a scratch /etc with faked ifup/ifdown — so no future
// change can leave the two sides agreeing only with themselves.

// managementHostLab is one host's state directory plus the /etc tree the engine edits. The
// baseline files are produced by Core's own renderer, so they carry the headers Core requires
// as proof that it may overwrite them.
func managementHostLab(t *testing.T) string {
	t.Helper()
	root, dir := t.TempDir(), t.TempDir()
	t.Cleanup(vmhost.ManagementEtcRootForTests(root))
	bin := t.TempDir()
	for _, name := range []string{"ifup", "ifdown", "ip", "dhcpcd"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	// The kernel view Core checks a spec against: the NIC the lab spec names, with its MAC, and
	// the br0 it uplinks. Without it Core refuses the stage, as it must on a real host whose
	// cards do not match.
	sys := t.TempDir() + "/"
	t.Cleanup(vmhost.NetworkSysfsRootsForTests(sys, t.TempDir()+"/"))
	for path, content := range map[string]string{"nex0/address": "52:54:00:12:34:56\n", "br0/address": "52:54:00:12:34:56\n", "br0/brif/nex0": ""} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(sys, path)), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sys, path), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(sys, "br0/bridge"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../br0", filepath.Join(sys, "nex0/master")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc/network/interfaces.d"), 0755); err != nil {
		t.Fatal(err)
	}
	files, err := vmhost.RenderManagementNetwork(protocol.ManagementNetworkSpec{
		Interfaces: []protocol.ManagementNIC{{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "dhcp", Management: true}},
		DNS:        []string{"192.0.2.53"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		rel     string
		content *string
	}{{"etc/network/interfaces", files.Interfaces}, {"etc/dhcpcd.conf", files.DHCPCD}, {"etc/resolv.conf", files.Resolv}} {
		if target.content == nil {
			t.Fatalf("the baseline render produced no %s", target.rel)
		}
		if err := os.WriteFile(filepath.Join(root, target.rel), []byte(*target.content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runStep submits one REST step, takes the command the Controller queued for the host, runs it
// on the host, and reports the host's resulting record back — the whole round trip.
func (l *joinLab) runStep(t *testing.T, dir string, body map[string]any, status int) protocol.Result {
	t.Helper()
	l.a.call("POST", l.networkPath(), body, nil, status)
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].ManagementNetwork == nil {
		t.Fatalf("the host was not sent exactly one management-network command for %v: %+v", body["action"], commands)
	}
	result := agent.ExecuteManagementNetwork(context.Background(), dir, commands[0])
	reported, err := vmhost.ReadManagementNetworkStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.member.inv.ManagementNetwork = reported
	l.send(&l.member, func(r *protocol.ReportRequest) { r.Results = []protocol.Result{result} })
	return result
}

func TestHostNetworkStagedEditEndToEnd(t *testing.T) {
	dir := managementHostLab(t)
	l := newNetworkLab(t)
	// The agent checks the typed confirmation against this machine's own name, which is also
	// the name the Controller requires, so the host reports it.
	name, err := os.Hostname()
	if err != nil || name == "" {
		t.Skip("this machine does not report a hostname")
	}
	l.member.inv.Hostname, l.member.inv.ObservedAt = name, time.Now().UTC()
	l.send(&l.member, nil)
	if reported := l.hostNetwork(t).Hostname; reported != name {
		t.Fatalf("the host's reported name is %q, want %q", reported, name)
	}

	if result := l.runStep(t, dir, map[string]any{"request_id": "net-stage", "action": "stage", "spec": labSpec(), "window_seconds": 720}, 202); result.Status != "succeeded" {
		t.Fatalf("stage was refused by the host: %+v", result)
	}
	view := l.hostNetwork(t)
	if view.State != protocol.ManagementNetworkStaged || view.Record == nil || view.Record.OperationID == "" {
		t.Fatalf("the staged record was not reported: %+v", view)
	}
	if view.Record.WindowSeconds != 720 {
		t.Fatalf("the operator's rollback window is not readable after a reload: %+v", view.Record)
	}

	if result := l.runStep(t, dir, map[string]any{"request_id": "net-apply", "action": "apply", "confirmation": name}, 202); result.Status != "succeeded" {
		t.Fatalf("apply was refused by the host: %+v", result)
	}
	if view = l.hostNetwork(t); view.State != protocol.ManagementNetworkApplied || view.Record.SecondsRemaining <= 0 {
		t.Fatalf("the applied record was not reported with a running deadline: %+v", view)
	}
	// Confirmation needs the host's own evidence that the new configuration carries traffic:
	// a connection that terminated on the management bridge. This lab runs entirely over
	// loopback, so the observation is planted; what makes one count is covered in vmhost.
	if err := vmhost.ManagementConnectivityForTests(dir); err != nil {
		t.Fatal(err)
	}
	if result := l.runStep(t, dir, map[string]any{"request_id": "net-confirm", "action": "confirm"}, 202); result.Status != "succeeded" {
		t.Fatalf("confirm was refused by the host: %+v", result)
	}
	if view = l.hostNetwork(t); view.State != protocol.ManagementNetworkConfirmed || view.Record.Revision != 1 {
		t.Fatalf("the confirmed record was not reported: %+v", view)
	}

	// A second change, discarded: the durable cancel must reach the change it names.
	if result := l.runStep(t, dir, map[string]any{"request_id": "net-stage-2", "action": "stage", "spec": labSpec(), "expected_revision": 1}, 202); result.Status != "succeeded" {
		t.Fatalf("the second stage was refused by the host: %+v", result)
	}
	staged := l.hostNetwork(t).Record.OperationID
	if result := l.runStep(t, dir, map[string]any{"request_id": "net-cancel", "action": "cancel", "change_id": staged}, 202); result.Status != "succeeded" {
		t.Fatalf("cancel was refused by the host: %+v", result)
	}
	if view = l.hostNetwork(t); view.State != protocol.ManagementNetworkCancelled {
		t.Fatalf("the cancelled record was not reported: %+v", view)
	}
}

// An operator who sees that the new configuration is wrong, while the host is still reachable,
// must be able to undo it at once rather than wait the window out: "cancel" on an applied,
// unconfirmed change is that early rollback, and it is strictly safer than waiting. The
// rollback bounces the management interface, so the result may not reach the Controller in the
// same exchange — the host journals it and reports it, with its new record, when it is back on
// the old configuration.
func TestHostNetworkOperatorRollbackEndToEnd(t *testing.T) {
	dir := managementHostLab(t)
	l := newNetworkLab(t)
	name, err := os.Hostname()
	if err != nil || name == "" {
		t.Skip("this machine does not report a hostname")
	}
	l.member.inv.Hostname, l.member.inv.ObservedAt = name, time.Now().UTC()
	l.send(&l.member, nil)

	if result := l.runStep(t, dir, map[string]any{"request_id": "roll-stage", "action": "stage", "spec": labSpec()}, 202); result.Status != "succeeded" {
		t.Fatalf("stage was refused by the host: %+v", result)
	}
	if result := l.runStep(t, dir, map[string]any{"request_id": "roll-apply", "action": "apply", "confirmation": name}, 202); result.Status != "succeeded" {
		t.Fatalf("apply was refused by the host: %+v", result)
	}
	view := l.hostNetwork(t)
	if view.State != protocol.ManagementNetworkApplied || view.Record == nil {
		t.Fatalf("the applied record was not reported: %+v", view)
	}
	change := view.Record.OperationID

	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "roll-cancel", "action": "cancel", "change_id": change}, nil, 202)
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].ManagementNetwork == nil || commands[0].ManagementNetwork.ChangeID != change {
		t.Fatalf("the host was not sent the early rollback: %+v", commands)
	}
	result := agent.ExecuteManagementNetwork(context.Background(), dir, commands[0])
	if result.Status != "succeeded" {
		t.Fatalf("the operator's early rollback was refused by the host: %+v", result)
	}
	reported, err := vmhost.ReadManagementNetworkStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reported.Stage != protocol.ManagementNetworkRolledBack || !reported.RequestedRollback {
		t.Fatalf("the host did not record an operator-requested rollback: %+v", reported)
	}
	if !strings.Contains(reported.LastError, "operator") {
		t.Fatalf("the recorded reason does not name the operator: %+v", reported)
	}
	// The exchange the rollback interrupted: the host reports both the journaled result and its
	// new record only when it reaches the Controller again, on the previous configuration.
	l.member.inv.ManagementNetwork = reported
	l.send(&l.member, func(r *protocol.ReportRequest) { r.Results = []protocol.Result{result} })
	if view = l.hostNetwork(t); view.State != protocol.ManagementNetworkRolledBack {
		t.Fatalf("the rolled-back record was not reported after the host came back: %+v", view)
	}
	if view.LastOperation == nil || view.LastOperation.Status != "succeeded" || view.LastOperation.Error != "" {
		t.Fatalf("the journaled rollback result did not reach the Controller: %+v", view.LastOperation)
	}
	// Repeating it is a conflict, not a second rollback: the change is already undone.
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "roll-cancel-2", "action": "cancel", "change_id": change}, nil, 202)
	commands = l.send(&l.member, nil).Commands
	if len(commands) != 1 {
		t.Fatalf("expected the repeated cancel to be delivered: %+v", commands)
	}
	if again := agent.ExecuteManagementNetwork(context.Background(), dir, commands[0]); again.Status != "failed" ||
		!strings.HasSuffix(again.Error, protocol.ManagementNetworkConflict) {
		t.Fatalf("cancelling an already rolled-back change must be a conflict: %+v", again)
	}
}

// The change an action names is the host's own reported one. A Controller that has nothing to
// act on says so, and a body naming a change the host no longer reports is a conflict rather
// than a command that would silently target whatever is staged now.
func TestHostNetworkActionsNameTheReportedChange(t *testing.T) {
	l := newNetworkLab(t)
	w := l.a.call("POST", l.networkPath(), map[string]any{"request_id": "nothing-staged", "action": "cancel"}, nil, 409)
	if body := w.Body.String(); !strings.Contains(body, "reports no management network change") {
		t.Fatalf("unexpected refusal with no record: %s", body)
	}
	l.member.inv.ManagementNetwork = &protocol.ManagementNetworkStatus{Revision: 2, Stage: protocol.ManagementNetworkStaged, OperationID: "op-now", ObservedAt: time.Now().UTC()}
	l.send(&l.member, nil)
	w = l.a.call("POST", l.networkPath(), map[string]any{"request_id": "stale-change", "action": "cancel", "change_id": "op-before"}, nil, 409)
	if body := w.Body.String(); !strings.Contains(body, "changed since you loaded it") {
		t.Fatalf("unexpected refusal for a stale change_id: %s", body)
	}
	l.a.call("POST", l.networkPath(), map[string]any{"request_id": "fresh-change", "action": "cancel", "change_id": "op-now"}, nil, 202)
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].ManagementNetwork == nil {
		t.Fatalf("the host did not receive the cancel: %+v", commands)
	}
	request := commands[0].ManagementNetwork
	if request.ChangeID != "op-now" || request.OperationID != commands[0].OperationID || request.OperationID == request.ChangeID {
		t.Fatalf("the command must carry its own operation id and the host's change id: %+v", request)
	}
}
