// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func transferMembership() PoolMembership {
	return PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 3, Members: []PoolMember{
		{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 3, Status: "ready"},
		{HostID: "host-b", CertificateSHA256: strings.Repeat("b", 64), Generation: 3, Status: "ready"},
	}}
}

func transferNotice() PoolAuthorityNotice {
	return PoolAuthorityNotice{
		PoolID: "pool-1", OperationID: "op-1", Action: "promote", FromHostID: "host-a",
		ToHostID: "host-b", TargetHostID: "host-b", AuthorityGeneration: 7,
		Membership: transferMembership(), IssuedAt: time.Unix(1700000000, 0).UTC(), Signature: "c2ln",
	}
}

func TestPoolAuthorityNoticeValidMatrix(t *testing.T) {
	if err := transferNotice().Valid(); err != nil {
		t.Fatalf("a complete notice was refused: %v", err)
	}
	cases := map[string]func(*PoolAuthorityNotice){
		"no pool":       func(n *PoolAuthorityNotice) { n.PoolID = "" },
		"no operation":  func(n *PoolAuthorityNotice) { n.OperationID = "" },
		"wrong action":  func(n *PoolAuthorityNotice) { n.Action = "demote" },
		"no issuer":     func(n *PoolAuthorityNotice) { n.FromHostID = "" },
		"no addressee":  func(n *PoolAuthorityNotice) { n.ToHostID = "" },
		"no target":     func(n *PoolAuthorityNotice) { n.TargetHostID = "" },
		"no generation": func(n *PoolAuthorityNotice) { n.AuthorityGeneration = 0 },
		"unsigned":      func(n *PoolAuthorityNotice) { n.Signature = "" },
		"long pool":     func(n *PoolAuthorityNotice) { n.PoolID = strings.Repeat("p", 129) },
		"long host":     func(n *PoolAuthorityNotice) { n.ToHostID = strings.Repeat("h", 129) },
	}
	for name, break_ := range cases {
		notice := transferNotice()
		break_(&notice)
		if err := notice.Valid(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// Every field a host acts on is inside the signature, so a notice cannot be re-addressed,
// renumbered or given another membership in transit.
func TestPoolAuthorityNoticeSigningBytesCoverEveryDecision(t *testing.T) {
	base, err := transferNotice().SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*PoolAuthorityNotice){
		"pool":       func(n *PoolAuthorityNotice) { n.PoolID = "pool-2" },
		"operation":  func(n *PoolAuthorityNotice) { n.OperationID = "op-2" },
		"action":     func(n *PoolAuthorityNotice) { n.Action = "eject" },
		"issuer":     func(n *PoolAuthorityNotice) { n.FromHostID = "host-c" },
		"addressee":  func(n *PoolAuthorityNotice) { n.ToHostID = "host-c" },
		"target":     func(n *PoolAuthorityNotice) { n.TargetHostID = "host-c" },
		"generation": func(n *PoolAuthorityNotice) { n.AuthorityGeneration = 8 },
		"membership": func(n *PoolAuthorityNotice) { n.Membership.MasterHostID = "host-b" },
		"member row": func(n *PoolAuthorityNotice) { n.Membership.Members[1].Status = "ejected" },
		"issued at":  func(n *PoolAuthorityNotice) { n.IssuedAt = n.IssuedAt.Add(time.Second) },
	}
	for name, change := range changes {
		notice := transferNotice()
		notice.Membership.Members = append([]PoolMember(nil), notice.Membership.Members...)
		change(&notice)
		b, err := notice.SigningBytes()
		if err != nil {
			t.Fatal(err)
		}
		if string(b) == string(base) {
			t.Fatalf("changing the %s left the signed bytes identical", name)
		}
	}
	// The signature itself is never part of what it signs.
	signed := transferNotice()
	signed.Signature = "another"
	b, err := signed.SigningBytes()
	if err != nil || string(b) != string(base) {
		t.Fatalf("the signature is inside its own signed bytes (%v)", err)
	}
}

func TestPoolTransferRequestValidMatrix(t *testing.T) {
	notice := transferNotice()
	for _, action := range []string{"activate", "repoint", "leave"} {
		request := PoolTransferRequest{OperationID: "op-1", Action: action, Notice: notice}
		if err := request.Valid(); err != nil {
			t.Fatalf("%s was refused: %v", action, err)
		}
		mismatched := request
		mismatched.Notice.OperationID = "op-2"
		if err := mismatched.Valid(); err == nil {
			t.Fatalf("%s accepted a notice for another operation", action)
		}
		unsigned := request
		unsigned.Notice.Signature = ""
		if err := unsigned.Valid(); err == nil {
			t.Fatalf("%s accepted an unsigned notice", action)
		}
	}
	// An abort carries no notice: it asks a host what it did, and the answer is its own.
	if err := (PoolTransferRequest{OperationID: "op-1", Action: "abort"}).Valid(); err != nil {
		t.Fatalf("abort was refused: %v", err)
	}
	if err := (PoolTransferRequest{OperationID: "op-1", Action: "promote"}).Valid(); err == nil {
		t.Fatal("an unknown transfer action was accepted")
	}
	if err := (PoolTransferRequest{Action: "abort"}).Valid(); err == nil {
		t.Fatal("a transfer without an operation was accepted")
	}
}

func TestPoolCommandTransferActions(t *testing.T) {
	for _, action := range []string{"master-change", "eject", "transfer-cancel"} {
		command := PoolCommand{RequestID: "r", Action: action, MemberHostID: "host-b", Membership: PoolMembership{MasterHostID: "host-a"}}
		if err := command.Valid(); err != nil {
			t.Fatalf("%s was refused: %v", action, err)
		}
		withoutMaster := command
		withoutMaster.Membership = PoolMembership{}
		if err := withoutMaster.Valid(); err == nil {
			t.Fatalf("%s was accepted without the pool master", action)
		}
		withoutTarget := command
		withoutTarget.MemberHostID = ""
		if err := withoutTarget.Valid(); err == nil {
			t.Fatalf("%s was accepted without a target host", action)
		}
	}
	// The existing actions keep their own rules.
	if err := (PoolCommand{RequestID: "r", Action: "member-join", Membership: transferMembership()}).Valid(); err != nil {
		t.Fatalf("member-join changed: %v", err)
	}
	if err := (PoolCommand{RequestID: "r", Action: "handover"}).Valid(); err == nil {
		t.Fatal("an invented pool command action was accepted")
	}
}

// The reported shapes are what a Controller mirrors; they must round trip unchanged.
func TestPoolTransferReportedShapesRoundTrip(t *testing.T) {
	status := PoolTransferStatus{
		OperationID: "op-1", Action: "promote", Role: "source", TargetHostID: "host-b",
		Stage: "replicate", Status: "pending", AuthorityGeneration: 7, Frozen: true,
		ObservedAt: time.Unix(1700000000, 0).UTC(),
	}
	b, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var back PoolTransferStatus
	if json.Unmarshal(b, &back) != nil || back != status {
		t.Fatalf("transfer status round trip: %s", b)
	}
	if !strings.Contains(string(b), `"frozen":true`) || !strings.Contains(string(b), `"authority_generation":7`) {
		t.Fatalf("transfer status omitted a reported field: %s", b)
	}
	record := PoolAuthorityRecord{Generation: 9, Digest: "abcd", ObservedAt: time.Unix(1700000000, 0).UTC()}
	if b, err = json.Marshal(record); err != nil {
		t.Fatal(err)
	}
	var authority PoolAuthorityRecord
	if json.Unmarshal(b, &authority) != nil || authority != record {
		t.Fatalf("authority record round trip: %s", b)
	}
}
