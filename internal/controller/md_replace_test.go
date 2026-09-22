// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func systemDiskBody(requestID string) map[string]any {
	return map[string]any{"request_id": requestID, "array": "md0", "device": map[string]any{
		"by_id": "/dev/disk/by-id/scsi-replacement", "serial": "replacement-serial",
		"size_bytes": 1 << 30, "fingerprint": strings.Repeat("a", 64),
	}}
}

// newSystemDiskLab is one enrolled host that can replace its system disk.
func newSystemDiskLab(t *testing.T) *joinLab {
	t.Helper()
	l := newJoinLab(t)
	l.member.caps = append(l.member.caps, protocol.SystemDiskReplaceCapability)
	l.send(&l.member, nil)
	return l
}

func (l *joinLab) systemDiskPath() string { return "/api/v1/hosts/" + l.member.id + "/system-disk" }

// TestSystemDiskReplaceReachesItsHost is the relay: one durable command carrying the array
// and the selected disk, addressed to the host whose mirror it rebuilds.
func TestSystemDiskReplaceReachesItsHost(t *testing.T) {
	l := newSystemDiskLab(t)
	var accepted struct {
		HostID    string             `json:"host_id"`
		Array     string             `json:"array"`
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-1"), nil, 202), &accepted)
	if accepted.HostID != l.member.id || accepted.Array != "md0" || accepted.Operation.Kind != "host-system-disk-replace" {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	if commands := l.send(&l.master, nil).Commands; len(commands) != 0 {
		t.Fatalf("another host was sent the command: %+v", commands)
	}
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].SystemDiskReplace == nil || commands[0].Kind != "host-system-disk-replace" {
		t.Fatalf("the host did not receive the command: %+v", commands)
	}
	request := commands[0].SystemDiskReplace
	if request.HostID != l.member.id || request.Array != "md0" || request.Device.ByID != "/dev/disk/by-id/scsi-replacement" || request.OperationID != commands[0].OperationID {
		t.Fatalf("unexpected command: %+v", request)
	}
	if err := request.Valid(); err != nil {
		t.Fatalf("delivered request is invalid: %v", err)
	}
	// An exact retry replays the same operation; a changed payload is a conflict.
	var replay struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-1"), nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exact retry did not replay: %+v", replay)
	}
	changed := systemDiskBody("disk-1")
	changed["array"] = "md1"
	l.a.call("POST", l.systemDiskPath(), changed, nil, 409)
}

func TestSystemDiskReplaceRefusesOldCoreAndInvalidInput(t *testing.T) {
	l := newSystemDiskLab(t)
	// An old Core never receives the command: the capability is what the Controller checks.
	l.a.call("POST", "/api/v1/hosts/"+l.master.id+"/system-disk", systemDiskBody("disk-old-core"), nil, 409)
	for name, mutate := range map[string]func(map[string]any){
		"no array":        func(body map[string]any) { delete(body, "array") },
		"not an md array": func(body map[string]any) { body["array"] = "sda" },
		"unstable device": func(body map[string]any) { body["device"].(map[string]any)["by_id"] = "/dev/sdb" },
		"no fingerprint":  func(body map[string]any) { body["device"].(map[string]any)["fingerprint"] = "" },
		"wrong confirmation": func(body map[string]any) {
			body["device"].(map[string]any)["erase_confirmation"] = "/dev/disk/by-id/other"
		},
	} {
		body := systemDiskBody("disk-" + strings.ReplaceAll(name, " ", "-"))
		mutate(body)
		if code := l.a.call("POST", l.systemDiskPath(), body, nil, 400).Code; code != 400 {
			t.Fatalf("%s was accepted with %d", name, code)
		}
	}
}

// The replacement erases a disk, so it needs the administrator's session and its CSRF token.
func TestSystemDiskReplaceNeedsCSRF(t *testing.T) {
	l := newSystemDiskLab(t)
	csrf := l.a.csrf
	l.a.csrf = ""
	l.a.call("POST", l.systemDiskPath(), systemDiskBody("disk-csrf"), nil, 403)
	l.a.csrf = csrf
}
