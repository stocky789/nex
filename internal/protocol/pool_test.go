// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPoolCommandRejectsStaleOrUnboundMember(t *testing.T) {
	m := PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 2, Members: []PoolMember{{HostID: "master-1", CertificateSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Generation: 2, Status: "ready"}, {HostID: "member-1", CertificateSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Generation: 2, Status: "ready"}}}
	if err := (PoolCommand{RequestID: "join-1", Action: "member-join", Membership: m, ExpectedVersion: 2}).Valid(); err != nil {
		t.Fatalf("valid pool command: %v", err)
	}
	if err := (PoolCommand{RequestID: "join-1", Action: "member-join", Membership: m, ExpectedVersion: 1}).Valid(); err == nil {
		t.Fatal("stale membership generation accepted")
	}
	m.Members[1].CertificateSHA256 = "unbound"
	if err := (PoolCommand{RequestID: "join-1", Action: "member-join", Membership: m}).Valid(); err == nil {
		t.Fatal("unbound member certificate accepted")
	}
}

func TestPoolJoinCommandRequiresTheWholeInvitation(t *testing.T) {
	join := PoolCommand{RequestID: "join-1", Action: "join", MemberHostID: "member-1", MasterURL: "https://master.example.test", PoolCAPEM: "-----BEGIN CERTIFICATE-----", Invitation: "invitation", PeerOrigin: "https://member.example.test"}
	// A join carries no membership record: the joining host is not a member yet.
	if err := join.Valid(); err != nil {
		t.Fatalf("valid pool join: %v", err)
	}
	for name, mutate := range map[string]func(*PoolCommand){
		"no request":      func(c *PoolCommand) { c.RequestID = "" },
		"no member":       func(c *PoolCommand) { c.MemberHostID = "" },
		"long member":     func(c *PoolCommand) { c.MemberHostID = string(make([]byte, 129)) },
		"no master":       func(c *PoolCommand) { c.MasterURL = "" },
		"no CA":           func(c *PoolCommand) { c.PoolCAPEM = "" },
		"no invitation":   func(c *PoolCommand) { c.Invitation = "" },
		"long invitation": func(c *PoolCommand) { c.Invitation = string(make([]byte, 8193)) },
		"long origin":     func(c *PoolCommand) { c.PeerOrigin = string(make([]byte, 513)) },
		"unknown action":  func(c *PoolCommand) { c.Action = "leave" },
	} {
		invalid := join
		mutate(&invalid)
		if err := invalid.Valid(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// A caller must be able to tell "this Core says the authority is not handed off" from "this
// Core is too old to say", so handed_off is on the wire even when false, like cancellable.
func TestPoolTransferStatusAlwaysReportsHandedOff(t *testing.T) {
	body, err := json.Marshal(PoolTransferStatus{OperationID: "op-1", Action: "promote", Role: "source"})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if got, ok := wire["handed_off"]; !ok || got != false {
		t.Fatalf("handed_off = %v (present %v), want false and present: %s", got, ok, body)
	}
	if body, err = json.Marshal(PoolTransferStatus{HandedOff: true}); err != nil || !strings.Contains(string(body), `"handed_off":true`) {
		t.Fatalf("handed_off true did not reach the wire: %s (%v)", body, err)
	}
}
