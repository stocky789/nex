// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func newRolloutLab(t *testing.T) *joinLab {
	t.Helper()
	l := newDrainLab(t)
	for _, h := range []*joinHost{&l.master, &l.member} {
		h.caps = append(h.caps, protocol.PoolUpdateRolloutCapability, protocol.ImageUpdateCapability)
	}
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	return l
}

func (l *joinLab) updatesPath(suffix string) string {
	return "/api/v1/pools/" + l.master.pool + "/updates" + suffix
}

// Every route is one pool-update command to the master. The Controller composes no membership,
// holds no rollout, and answers a replay from the operation it already queued.
func TestPoolUpdateRelaysToTheMasterOnly(t *testing.T) {
	l := newRolloutLab(t)
	var accepted struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.updatesPath("/rollouts"), map[string]any{"request_id": "roll-1", "sequence": 7, "running_vms": "shutdown"}, nil, 202), &accepted)
	if accepted.Operation.Kind != "pool-update" || accepted.Operation.HostID != l.master.id {
		t.Fatalf("operation = %+v", accepted.Operation)
	}
	if commands := l.send(&l.member, nil).Commands; len(commands) != 0 {
		t.Fatalf("a member was sent the rollout command: %+v", commands)
	}
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Pool == nil || commands[0].Kind != "pool-update" {
		t.Fatalf("master commands = %+v", commands)
	}
	cmd := commands[0].Pool
	if cmd.Action != "update-rollout" || cmd.Update == nil || cmd.Update.Sequence != 7 || cmd.Update.RunningVMs != "shutdown" || cmd.Update.DryRun {
		t.Fatalf("command = %+v update = %+v", cmd, cmd.Update)
	}
	if cmd.Membership.MasterHostID != l.master.id || cmd.Membership.PoolID != "" || cmd.Membership.Members != nil {
		t.Fatalf("Controller composed a membership: %+v", cmd.Membership)
	}
	if err := cmd.Valid(); err != nil {
		t.Fatalf("delivered command is invalid: %v", err)
	}
	var replay struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.updatesPath("/rollouts"), map[string]any{"request_id": "roll-1", "sequence": 7, "running_vms": "shutdown"}, nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exact retry did not replay: %+v", replay)
	}
	l.a.call("POST", l.updatesPath("/rollouts"), map[string]any{"request_id": "roll-1", "sequence": 8}, nil, 409)

	l.a.call("POST", l.updatesPath("/check"), map[string]any{"request_id": "check-1", "sequence": 7}, nil, 202)
	l.a.call("POST", l.updatesPath("/rollouts/roll-1/control"), map[string]any{"request_id": "pause-1", "action": "pause"}, nil, 202)
	l.a.call("POST", l.updatesPath("/rollouts/roll-1/control"), map[string]any{"request_id": "rb-1", "action": "rollback_host", "host_id": l.member.id}, nil, 202)
	// Commands are redelivered until a result is reported, so one report carries every pending
	// one; pick them out by what they ask.
	var dry, pause, rollback *protocol.PoolCommand
	for _, c := range l.send(&l.master, nil).Commands {
		switch {
		case c.Pool.Action == "update-rollout" && c.Pool.Update.DryRun:
			dry = c.Pool
		case c.Pool.Action == "update-control" && c.Pool.Update.Control == "pause":
			pause = c.Pool
		case c.Pool.Action == "update-control" && c.Pool.Update.Control == "rollback_host":
			rollback = c.Pool
		}
	}
	if dry == nil || pause == nil || rollback == nil || pause.Update.RolloutID != "roll-1" || rollback.Update.HostID != l.member.id {
		t.Fatalf("commands = dry %+v pause %+v rollback %+v", dry, pause, rollback)
	}
	sent := []protocol.Command{{Pool: dry}, {Pool: pause}, {Pool: rollback}}
	for _, c := range sent {
		if c.Pool.Valid() != nil {
			t.Fatalf("invalid command %+v", c.Pool)
		}
	}
}

func TestPoolUpdateRefusals(t *testing.T) {
	l := newRolloutLab(t)
	body := func(id string) map[string]any { return map[string]any{"request_id": id, "sequence": 3} }
	l.a.call("POST", l.updatesPath("/rollouts"), map[string]any{"sequence": 3}, nil, 400)
	l.a.call("POST", l.updatesPath("/rollouts"), map[string]any{"request_id": "no-sequence"}, nil, 400)
	l.a.call("POST", l.updatesPath("/rollouts"), map[string]any{"request_id": "x", "sequence": 3, "running_vms": "reboot"}, nil, 400)
	l.a.call("POST", l.updatesPath("/rollouts/roll-1/control"), map[string]any{"request_id": "c", "action": "explode"}, nil, 400)
	l.a.call("POST", l.updatesPath("/rollouts/roll-1/control"), map[string]any{"request_id": "c2", "action": "rollback_host"}, nil, 400)
	l.a.call("POST", "/api/v1/pools/no-such-pool/updates/rollouts", body("missing-pool"), nil, 404)
	// A member that does not advertise the capability makes the whole pool "upgrade Nex Core".
	l.member.caps = []string{protocol.ManagedVMCapability, protocol.ManagementNetworkCapability}
	l.send(&l.member, nil)
	w := l.a.call("POST", l.updatesPath("/rollouts"), body("old-member"), nil, 409)
	if !strings.Contains(w.Body.String(), "upgrade Nex Core") || !strings.Contains(w.Body.String(), protocol.PoolUpdateRolloutCapability) {
		t.Fatalf("refusal = %s", w.Body)
	}
	// A master that is not reporting cannot be asked.
	l.member.caps = append(l.member.caps, protocol.PoolUpdateRolloutCapability)
	l.send(&l.member, nil)
	l.forget(&l.master)
	l.a.call("POST", l.updatesPath("/rollouts"), body("stale-master"), nil, 409)
}

// A fresh Controller reads the rollout from the master's report; the Controller never produced it.
func TestPoolUpdateProjectsTheMastersRecord(t *testing.T) {
	l := newRolloutLab(t)
	var empty poolUpdateView
	read(t, l.a.call("GET", l.updatesPath(""), nil, nil, 200), &empty)
	if !empty.Supported || empty.Rollout != nil {
		t.Fatalf("view = %+v", empty)
	}
	rollout := &protocol.PoolUpdateRollout{ID: "roll-1", PoolID: l.master.pool, Sequence: 7, RunningVMs: "refuse", State: protocol.PoolUpdatePaused, Phase: "hosts",
		Pause: &protocol.PoolUpdatePause{Code: protocol.PoolUpdateMasterChanged, Message: "the pool master changed during the rollout; resume or abort it"},
		Hosts: []protocol.PoolUpdateHost{{HostID: l.member.id, Stage: "done"}, {HostID: l.master.id, Stage: "pending"}}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	l.master.inv.Managed.UpdateRollout = rollout
	l.send(&l.master, nil)
	var got poolUpdateView
	read(t, l.a.call("GET", l.updatesPath(""), nil, nil, 200), &got)
	if got.Rollout == nil || got.Rollout.ID != "roll-1" || got.Rollout.Pause == nil || got.Rollout.Pause.Code != protocol.PoolUpdateMasterChanged || len(got.Rollout.Hosts) != 2 {
		t.Fatalf("projection = %+v", got.Rollout)
	}
	l.a.call("GET", "/api/v1/pools/no-such-pool/updates", nil, nil, 404)
}

func TestPoolControlOperationReconcilesAuthenticatedCoreRecovery(t *testing.T) {
	for _, terminal := range []string{"succeeded", "failed"} {
		t.Run(terminal, func(t *testing.T) {
			l := newRolloutLab(t)
			path := l.updatesPath("/rollouts/roll-1/control")
			body := map[string]any{"request_id": "recover-control", "action": "abort"}
			var accepted struct {
				Operation protocol.Operation `json:"operation"`
			}
			read(t, l.a.call("POST", path, body, nil, 202), &accepted)
			if commands := l.send(&l.master, nil).Commands; len(commands) != 1 || commands[0].OperationID != accepted.Operation.ID {
				t.Fatalf("control was not delivered: %+v", commands)
			}
			l.send(&l.master, func(report *protocol.ReportRequest) {
				report.Results = []protocol.Result{{OperationID: accepted.Operation.ID, Status: "needs_attention",
					Error: "replica_pending: Core is awaiting acknowledgement", ObservedAt: time.Now().UTC()}}
			})
			replayedOperation := func() protocol.Operation {
				var response struct {
					Operation protocol.Operation `json:"operation"`
				}
				read(t, l.a.call("POST", path, body, nil, 200), &response)
				return response.Operation
			}
			replay := replayedOperation()
			if replay.ID != accepted.Operation.ID || replay.Status != "needs_attention" {
				t.Fatalf("same-ID request lost unresolved Core operation: %+v", replay)
			}
			l.a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: protocol.Version,
				Sequence: l.member.seq + 1, Results: []protocol.Result{{OperationID: accepted.Operation.ID,
					Status: terminal, ObservedAt: time.Now().UTC()}}}, l.member.cert, 400)
			errorText := ""
			if terminal == "failed" {
				errorText = "rollback_failed: definite host refusal"
			}
			l.send(&l.master, func(report *protocol.ReportRequest) {
				report.Results = []protocol.Result{{OperationID: accepted.Operation.ID, Status: terminal,
					Error: errorText, ObservedAt: time.Now().UTC()}}
			})
			replay = replayedOperation()
			if replay.ID != accepted.Operation.ID || replay.Status != terminal || replay.Error != errorText {
				t.Fatalf("Core recovery did not settle original operation: %+v", replay)
			}
			l.send(&l.master, func(report *protocol.ReportRequest) {
				report.Results = []protocol.Result{{OperationID: accepted.Operation.ID, Status: "failed",
					Error: "late unrelated error", ObservedAt: time.Now().UTC()}}
			})
			replay = replayedOperation()
			if replay.Status != terminal || replay.Error != errorText {
				t.Fatal("a terminal control outcome changed after later results")
			}
		})
	}
}

func TestPoolUpdateRecoveryDoesNotReopenNonControlCommands(t *testing.T) {
	l := newRolloutLab(t)
	path := l.updatesPath("/rollouts")
	body := map[string]any{"request_id": "not-control", "sequence": 7}
	var accepted struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", path, body, nil, 202), &accepted)
	l.send(&l.master, nil)
	for _, status := range []string{"needs_attention", "succeeded"} {
		l.send(&l.master, func(report *protocol.ReportRequest) {
			report.Results = []protocol.Result{{OperationID: accepted.Operation.ID, Status: status, ObservedAt: time.Now().UTC()}}
		})
	}
	read(t, l.a.call("POST", path, body, nil, 200), &accepted)
	if accepted.Operation.Status != "needs_attention" {
		t.Fatal("control recovery permission reopened a different pool update command")
	}
}
