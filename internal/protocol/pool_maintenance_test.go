// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPoolMaintenanceCommandValid(t *testing.T) {
	valid := PoolCommand{RequestID: "req-1", Action: "maintenance", MemberHostID: "member-2", Enabled: true, Membership: PoolMembership{MasterHostID: "master-1"}}
	if err := valid.Valid(); err != nil {
		t.Fatalf("a well-formed maintenance command was refused: %v", err)
	}
	// Disabling carries no membership either, and an expected generation is optional.
	exit := valid
	exit.Enabled, exit.ExpectedVersion = false, 7
	if err := exit.Valid(); err != nil {
		t.Fatalf("exit was refused: %v", err)
	}
	for name, bad := range map[string]PoolCommand{
		"no request":  {Action: "maintenance", MemberHostID: "member-2", Membership: PoolMembership{MasterHostID: "master-1"}},
		"no master":   {RequestID: "req-1", Action: "maintenance", MemberHostID: "member-2"},
		"no member":   {RequestID: "req-1", Action: "maintenance", Membership: PoolMembership{MasterHostID: "master-1"}},
		"long master": {RequestID: "req-1", Action: "maintenance", MemberHostID: "member-2", Membership: PoolMembership{MasterHostID: strings.Repeat("m", 129)}},
		"long member": {RequestID: "req-1", Action: "maintenance", MemberHostID: strings.Repeat("h", 129), Membership: PoolMembership{MasterHostID: "master-1"}},
	} {
		if err := bad.Valid(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// Every other action keeps its full membership requirement.
	if err := (PoolCommand{RequestID: "req-1", Action: "member-join", Membership: PoolMembership{MasterHostID: "master-1"}}).Valid(); err == nil {
		t.Error("a member-join without a valid membership was accepted")
	}
}

// The Controller and the UI decode these names; freeze them.
func TestPoolMaintenanceWireShape(t *testing.T) {
	command, err := json.Marshal(Command{OperationID: "op-1", Kind: "pool-maintenance", Pool: &PoolCommand{
		RequestID: "req-1", Action: "maintenance", MemberHostID: "member-2", Enabled: true, ExpectedVersion: 7,
		Membership: PoolMembership{MasterHostID: "master-1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `"pool":{"request_id":"req-1","action":"maintenance","membership":{"pool_id":"","master_host_id":"master-1","generation":0,"members":null},"expected_version":7,"member_host_id":"member-2","enabled":true}`
	if !strings.Contains(string(command), want) {
		t.Fatalf("command wire shape:\n got %s\nwant %s", command, want)
	}
	since := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	member, err := json.Marshal(PoolMember{HostID: "member-2", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready", Maintenance: true, MaintenanceSince: since})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(member), `"maintenance":true,"maintenance_since":"2026-09-19T10:00:00Z"`) {
		t.Fatalf("member wire shape: %s", member)
	}
	// A host that is not drained reports no flag and a zero time; nothing else about the
	// membership an older Controller already understands changes.
	plain, err := json.Marshal(PoolMember{HostID: "member-2", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), `"maintenance":`) || !strings.Contains(string(plain), `"maintenance_since":"0001-01-01T00:00:00Z"`) {
		t.Fatalf("an undrained member reported maintenance: %s", plain)
	}
}
