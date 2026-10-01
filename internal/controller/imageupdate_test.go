// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// newImageHostLab is a pool whose member is an image-edition Core that offered image-update-v1;
// the master is a classic host that did not.
func newImageHostLab(t *testing.T) *joinLab {
	t.Helper()
	l := newJoinLab(t)
	l.member.caps = append(l.member.caps, protocol.ImageUpdateCapability)
	finished := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	l.member.inv.Image = &protocol.ImageStatus{
		Booted:        protocol.ImageRelease{Label: "2026.10.2", Sequence: 2, RootHash: "aa", Slot: "a"},
		Previous:      &protocol.ImageRelease{Label: "2026.10.1", Sequence: 1, RootHash: "bb", Slot: "b"},
		Channel:       "stable",
		Phase:         "committed",
		RollbackFloor: 1,
		StateFormat:   3,
		Operation:     &protocol.ImageOperation{ID: "req-apply-0001", Kind: "apply", Phase: "committed", Sequence: 2, StartedAt: finished.Add(-time.Minute), FinishedAt: &finished},
	}
	l.send(&l.member, nil)
	return l
}

// The Controller answers image-update-v1 only to a Core that offered it, and shows exactly what the
// host reported: slots, phase, rollback floor and the last operation. Nothing is stored beyond the
// inventory every host report already replaces.
func TestImageUpdateAckAndHostReportedProjection(t *testing.T) {
	l := newImageHostLab(t)
	if resp := l.send(&l.member, nil); resp.ImageUpdate == nil || !*resp.ImageUpdate {
		t.Fatalf("a Core that offered image-update-v1 was not acknowledged: %+v", resp.ImageUpdate)
	}
	if resp := l.send(&l.master, nil); resp.ImageUpdate != nil {
		t.Fatalf("a Core that did not offer image-update-v1 was sent the acknowledgement: %v", *resp.ImageUpdate)
	}
	var view struct {
		HostID     string                `json:"host_id"`
		Connection string                `json:"connection"`
		Supported  bool                  `json:"supported"`
		Image      *protocol.ImageStatus `json:"image"`
	}
	read(t, l.a.call("GET", "/api/v1/hosts/"+l.member.id+"/updates", nil, nil, 200), &view)
	img := view.Image
	if !view.Supported || view.Connection != "fresh" || img == nil || img.Booted.Label != "2026.10.2" || img.Booted.Slot != "a" ||
		img.Previous == nil || img.Previous.Sequence != 1 || img.Phase != "committed" || img.RollbackFloor != 1 || img.Operation == nil || img.Operation.ID != "req-apply-0001" {
		t.Fatalf("host report was not projected faithfully: %+v", view)
	}
	view.Image = nil
	read(t, l.a.call("GET", "/api/v1/hosts/"+l.master.id+"/updates", nil, nil, 200), &view)
	if view.Supported || view.Image != nil {
		t.Fatalf("a classic host reported an image: %+v", view)
	}
	l.a.call("GET", "/api/v1/hosts/missing-host/updates", nil, nil, 404)
}

func TestImageUpdateRelaysToItsHostOnly(t *testing.T) {
	l := newImageHostLab(t)
	path := func(action string) string { return "/api/v1/hosts/" + l.member.id + "/updates/" + action }
	var accepted struct {
		HostID    string             `json:"host_id"`
		Action    string             `json:"action"`
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", path("apply"), map[string]any{"request_id": "req-apply-0002", "sequence": 3, "activate": "stage_only", "running_vms": "refuse"}, nil, 202), &accepted)
	if accepted.HostID != l.member.id || accepted.Action != "apply" || accepted.Operation.Kind != protocol.ImageUpdateCommandKind {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	if commands := l.send(&l.master, nil).Commands; len(commands) != 0 {
		t.Fatalf("another host was sent the command: %+v", commands)
	}
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].Kind != protocol.ImageUpdateCommandKind || commands[0].ImageUpdate == nil {
		t.Fatalf("the host did not receive the command: %+v", commands)
	}
	req := commands[0].ImageUpdate
	if req.HostID != l.member.id || req.Action != "apply" || req.RequestID != "req-apply-0002" || req.Sequence != 3 || req.Activate != "stage_only" || req.RunningVMs != "refuse" {
		t.Fatalf("unexpected command: %+v", req)
	}
	if err := req.Valid(); err != nil {
		t.Fatalf("delivered request is invalid: %v", err)
	}
	// An exact retry replays the same operation; the same request_id for another action is a conflict.
	var replay struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", path("apply"), map[string]any{"request_id": "req-apply-0002", "sequence": 3, "activate": "stage_only", "running_vms": "refuse"}, nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exact retry did not replay: %+v", replay)
	}
	l.a.call("POST", path("activate"), map[string]any{"request_id": "req-apply-0002"}, nil, 409)
	// Every action the host's engine has is relayed under its own name.
	for _, action := range []string{"check", "activate", "rollback", "cancel"} {
		read(t, l.a.call("POST", path(action), map[string]any{"request_id": "req-" + action + "-0001"}, nil, 202), &accepted)
		if accepted.Action != action {
			t.Fatalf("%s relayed as %s", action, accepted.Action)
		}
	}
}

func TestImageUpdateRefusals(t *testing.T) {
	l := newImageHostLab(t)
	member := "/api/v1/hosts/" + l.member.id + "/updates/"
	// A classic host, or a Core that never offered image-update-v1, never receives the command.
	l.a.call("POST", "/api/v1/hosts/"+l.master.id+"/updates/check", map[string]any{"request_id": "req-classic-001"}, nil, 409)
	l.a.call("POST", "/api/v1/hosts/missing-host/updates/check", map[string]any{"request_id": "req-missing-001"}, nil, 404)
	for name, c := range map[string]struct {
		path string
		body map[string]any
	}{
		"unknown action":      {"frobnicate", map[string]any{"request_id": "req-bad-00001"}},
		"no request id":       {"check", map[string]any{}},
		"sequence on check":   {"check", map[string]any{"request_id": "req-bad-00002", "sequence": 3}},
		"activate on rollbck": {"rollback", map[string]any{"request_id": "req-bad-00003", "activate": "reboot"}},
		"bad activate":        {"apply", map[string]any{"request_id": "req-bad-00004", "activate": "sometimes"}},
		"bad running vms":     {"activate", map[string]any{"request_id": "req-bad-00005", "running_vms": "force"}},
		"negative sequence":   {"apply", map[string]any{"request_id": "req-bad-00006", "sequence": -1}},
		"unknown field":       {"check", map[string]any{"request_id": "req-bad-00007", "surprise": 1}},
	} {
		if code := l.a.call("POST", member+c.path, c.body, nil, 400).Code; code != 400 {
			t.Fatalf("%s was accepted with %d", name, code)
		}
	}
}

// Update actions replace the system, so they need the administrator's session and its CSRF token.
func TestImageUpdateNeedsCSRF(t *testing.T) {
	l := newImageHostLab(t)
	csrf := l.a.csrf
	l.a.csrf = ""
	l.a.call("POST", "/api/v1/hosts/"+l.member.id+"/updates/apply", map[string]any{"request_id": "req-csrf-0001"}, nil, 403)
	l.a.csrf = csrf
}
