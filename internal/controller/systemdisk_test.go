// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func systemDiskBody(requestID string) map[string]any {
	return map[string]any{"request_id": requestID, "device": map[string]any{
		"by_id": "/dev/disk/by-id/scsi-replacement", "serial": "replacement-serial",
		"size_bytes": 1 << 30, "fingerprint": strings.Repeat("a", 64),
	}}
}

// newSystemDiskLab is a pool whose member is a Core that can replace its system disk.
func newSystemDiskLab(t *testing.T) *joinLab {
	t.Helper()
	l := newJoinLab(t)
	l.member.caps = append(l.member.caps, protocol.SystemDiskReplaceCapability)
	l.send(&l.member, nil)
	return l
}

func (l *joinLab) systemDiskPath() string { return "/api/v1/hosts/" + l.member.id + "/system-disk" }

// The relay is one durable command carrying the selected disk, addressed to the host whose disk
// is replaced. The same request_id replays; a new one is a new command.
func TestSystemDiskReplaceReachesItsHostAndReplays(t *testing.T) {
	l := newSystemDiskLab(t)
	var accepted struct {
		HostID    string             `json:"host_id"`
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-0001"), nil, 202), &accepted)
	if accepted.HostID != l.member.id || accepted.Operation.Kind != protocol.SystemDiskReplaceCommandKind || accepted.Operation.HostID != l.member.id {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	if commands := l.send(&l.master, nil).Commands; len(commands) != 0 {
		t.Fatalf("another host was sent the command: %+v", commands)
	}
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].SystemDiskReplace == nil || commands[0].Kind != protocol.SystemDiskReplaceCommandKind {
		t.Fatalf("the host did not receive the command: %+v", commands)
	}
	request := commands[0].SystemDiskReplace
	if request.HostID != l.member.id || request.RequestID != "disk-0001" || request.Device.ByID != "/dev/disk/by-id/scsi-replacement" || request.Device.Serial != "replacement-serial" {
		t.Fatalf("unexpected command: %+v", request)
	}
	if err := request.Valid(); err != nil {
		t.Fatalf("delivered request is invalid: %v", err)
	}
	// A lost reply is retried with the same request_id: same operation, no second command.
	var replay struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-0001"), nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exact retry did not replay: %+v", replay)
	}
	if again := l.send(&l.member, nil).Commands; len(again) != 1 {
		t.Fatalf("a replay queued another command: %+v", again)
	}
	// A retry after a failure uses a new request_id and is a new operation.
	var next struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-0002"), nil, 202), &next)
	if next.Operation.ID == accepted.Operation.ID {
		t.Fatalf("a new request_id reused the operation: %+v", next)
	}
	delivered := 0
	for _, cmd := range l.send(&l.member, nil).Commands {
		if cmd.SystemDiskReplace != nil && cmd.SystemDiskReplace.RequestID == "disk-0002" {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("the new request was delivered %d times", delivered)
	}
}

func TestSystemDiskReplaceRefusals(t *testing.T) {
	l := newSystemDiskLab(t)
	// A Core that never offered the capability never receives the command.
	l.a.call("POST", "/api/v1/hosts/"+l.master.id+"/system-disk", systemDiskBody("disk-old-core"), nil, 409)
	l.a.call("POST", "/api/v1/hosts/missing-host/system-disk", systemDiskBody("disk-missing"), nil, 404)
	for name, mutate := range map[string]func(map[string]any){
		"no request id":   func(body map[string]any) { body["request_id"] = "" },
		"bad request id":  func(body map[string]any) { body["request_id"] = "../x" },
		"unstable device": func(body map[string]any) { body["device"].(map[string]any)["by_id"] = "/dev/sdb" },
		"no fingerprint":  func(body map[string]any) { body["device"].(map[string]any)["fingerprint"] = "" },
		"wrong confirmation": func(body map[string]any) {
			body["device"].(map[string]any)["erase_confirmation"] = "/dev/disk/by-id/other"
		},
		"unknown field": func(body map[string]any) { body["array"] = "md0" },
	} {
		body := systemDiskBody("disk-" + strings.ReplaceAll(name, " ", "-"))
		mutate(body)
		if code := l.a.call("POST", l.systemDiskPath(), body, nil, 400).Code; code != 400 {
			t.Fatalf("%s was accepted with %d", name, code)
		}
	}
	if commands := l.send(&l.member, nil).Commands; len(commands) != 0 {
		t.Fatalf("a refused request queued a command: %+v", commands)
	}
}

// A revoked host and a host that is not freshly connected are refused before anything is queued.
func TestSystemDiskReplaceRefusesRevokedAndStaleHosts(t *testing.T) {
	l := newSystemDiskLab(t)
	l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-fresh-01"), nil, 202)
	l.a.c.mu.Lock()
	l.a.c.seen[l.member.id] = time.Now().Add(-24 * time.Hour)
	l.a.c.mu.Unlock()
	l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-stale-01"), nil, 409)
	if _, err := l.a.c.db.Exec("UPDATE hosts SET revoked=1 WHERE id=?", l.member.id); err != nil {
		t.Fatal(err)
	}
	l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-revoked-1"), nil, 409)
}

// The replacement erases a disk, so it needs the administrator's session and its CSRF token.
func TestSystemDiskReplaceNeedsCSRF(t *testing.T) {
	l := newSystemDiskLab(t)
	csrf := l.a.csrf
	l.a.csrf = ""
	l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-csrf-001"), nil, 403)
	l.a.csrf = csrf
}
