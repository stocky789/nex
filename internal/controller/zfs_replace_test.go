// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func zfsReplaceBody(l *joinLab, requestID string) map[string]any {
	return map[string]any{"request_id": requestID, "repository_id": "zfs-1", "pool_id": l.master.pool, "host_id": l.master.id,
		"old_device": "/dev/disk/by-id/disk-old", "device": map[string]any{
			"by_id": "/dev/disk/by-id/disk-new", "serial": "new-serial", "size_bytes": 1 << 30, "fingerprint": strings.Repeat("a", 64),
		}}
}

// newZFSReplaceLab is one enrolled pool master that provisions and repairs ZFS storage.
func newZFSReplaceLab(t *testing.T) *joinLab {
	t.Helper()
	l := newJoinLab(t)
	l.master.caps = append(l.master.caps, protocol.ZFSProvisionCapability, protocol.ZFSReplaceCapability)
	l.send(&l.master, nil)
	return l
}

const zfsReplacePath = "/api/v1/storage/zfs/replace"

// The pool master owns the repository record, so the command goes to it even when a member
// holds the pool; only Core decides whether the disks are what it was told.
func TestZFSReplaceReachesThePoolMaster(t *testing.T) {
	l := newZFSReplaceLab(t)
	var accepted struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", zfsReplacePath, zfsReplaceBody(l, "replace-1"), nil, 202), &accepted)
	if accepted.Operation.Kind != "storage-zfs-replace" {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	if commands := l.send(&l.member, nil).Commands; len(commands) != 0 {
		t.Fatalf("another host was sent the command: %+v", commands)
	}
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].ZFSReplace == nil || commands[0].Kind != "storage-zfs-replace" {
		t.Fatalf("the pool master did not receive the command: %+v", commands)
	}
	request := commands[0].ZFSReplace
	if request.RepositoryID != "zfs-1" || request.HostID != l.master.id || request.OldDevice != "/dev/disk/by-id/disk-old" || request.Device.ByID != "/dev/disk/by-id/disk-new" || request.OperationID != commands[0].OperationID {
		t.Fatalf("unexpected command: %+v", request)
	}
	if err := request.Valid(); err != nil {
		t.Fatalf("delivered request is invalid: %v", err)
	}
	var replay struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", zfsReplacePath, zfsReplaceBody(l, "replace-1"), nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exact retry did not replay: %+v", replay)
	}
	changed := zfsReplaceBody(l, "replace-1")
	changed["old_device"] = "/dev/disk/by-id/disk-other"
	l.a.call("POST", zfsReplacePath, changed, nil, 409)
}

func TestZFSReplaceRefusesOldCoreAndInvalidInput(t *testing.T) {
	l := newZFSReplaceLab(t)
	// A Core without the capability never receives the command.
	l.member.caps = append(l.member.caps, protocol.ZFSProvisionCapability)
	l.send(&l.member, nil)
	old := zfsReplaceBody(l, "replace-old-core")
	old["pool_id"], old["host_id"] = l.member.pool, l.member.id
	if body := l.a.call("POST", zfsReplacePath, old, nil, 409).Body.String(); !strings.Contains(body, protocol.ZFSReplaceCapability) {
		t.Fatalf("a Core without the capability was refused for another reason: %s", body)
	}
	for name, mutate := range map[string]func(map[string]any){
		"no repository":       func(body map[string]any) { delete(body, "repository_id") },
		"no host":             func(body map[string]any) { delete(body, "host_id") },
		"unstable old device": func(body map[string]any) { body["old_device"] = "/dev/sdb" },
		"same disk":           func(body map[string]any) { body["old_device"] = "/dev/disk/by-id/disk-new" },
		"no fingerprint":      func(body map[string]any) { body["device"].(map[string]any)["fingerprint"] = "" },
		"wrong confirmation": func(body map[string]any) {
			body["device"].(map[string]any)["erase_confirmation"] = "/dev/disk/by-id/other"
		},
	} {
		body := zfsReplaceBody(l, "replace-"+strings.ReplaceAll(name, " ", "-"))
		mutate(body)
		if code := l.a.call("POST", zfsReplacePath, body, nil, 400).Code; code != 400 {
			t.Fatalf("%s was accepted with %d", name, code)
		}
	}
}

// Replacing a disk erases one, so it needs the administrator's session and its CSRF token.
func TestZFSReplaceNeedsCSRF(t *testing.T) {
	l := newZFSReplaceLab(t)
	csrf := l.a.csrf
	l.a.csrf = ""
	l.a.call("POST", zfsReplacePath, zfsReplaceBody(l, "replace-csrf"), nil, 403)
	l.a.csrf = csrf
}

// Both the pool master and the selected host must advertise replace, and the selected host
// must be a fresh member of the named pool — not a foreign or revoked host.
func TestZFSReplaceChecksSelectedHostCapabilityAndMembership(t *testing.T) {
	l := newZFSReplaceLab(t)
	membership := l.transferMembership(2, l.master.id)
	l.master.inv.Managed.Membership, l.member.inv.Managed.Membership = membership, membership
	l.master.inv.Managed.Authority = labAuthority(9)
	l.member.caps = append(l.member.caps, protocol.ZFSProvisionCapability)
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	if got := l.a.poolOf(l.member.id); got != l.master.pool {
		t.Fatalf("member was not adopted into the master's pool: %s", got)
	}
	oldMember := zfsReplaceBody(l, "replace-member-old-core")
	oldMember["host_id"] = l.member.id
	if body := l.a.call("POST", zfsReplacePath, oldMember, nil, 409).Body.String(); !strings.Contains(body, protocol.ZFSReplaceCapability) {
		t.Fatalf("a member without replace capability was refused for another reason: %s", body)
	}

	l = newZFSReplaceLab(t)
	l.member.caps = append(l.member.caps, protocol.ZFSProvisionCapability, protocol.ZFSReplaceCapability)
	l.send(&l.member, nil)
	foreign := zfsReplaceBody(l, "replace-foreign-host")
	foreign["host_id"] = l.member.id
	if body := l.a.call("POST", zfsReplacePath, foreign, nil, 409).Body.String(); !strings.Contains(body, "fresh, active and in the selected pool") {
		t.Fatalf("a foreign-pool host was refused for another reason: %s", body)
	}

	l = newZFSReplaceLab(t)
	membership = l.transferMembership(2, l.master.id)
	l.master.inv.Managed.Membership, l.member.inv.Managed.Membership = membership, membership
	l.master.inv.Managed.Authority = labAuthority(9)
	l.member.caps = append(l.member.caps, protocol.ZFSProvisionCapability, protocol.ZFSReplaceCapability)
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	l.a.call("POST", "/api/v1/hosts/"+l.member.id+"/revoke", nil, nil, 200)
	revoked := zfsReplaceBody(l, "replace-revoked-host")
	revoked["host_id"] = l.member.id
	if body := l.a.call("POST", zfsReplacePath, revoked, nil, 409).Body.String(); !strings.Contains(body, "fresh, active and in the selected pool") {
		t.Fatalf("a revoked host was refused for another reason: %s", body)
	}
}
