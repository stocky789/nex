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

func TestPoolStorageLeaseRequiresStableDistinctHostIDsAndExactNFSIdentity(t *testing.T) {
	identity := StorageIdentity{NFS: &NFSStorageIdentity{Server: "nfs.example.test", Export: "/exports/vms", FilesystemID: "0:42"}}
	lease := &PoolStorageLeaseConfig{
		Repositories: []PoolStorageLeaseRepository{{RepositoryID: "repo-1", RepositoryGeneration: 3, RepositoryIdentity: identity,
			Lockspace: PoolStorageLeaseLockspaceName("pool-1", "repo-1")}},
		AuthorityRepositoryID: "repo-1", IOTimeoutSeconds: 10, HostDeadSeconds: 60, FenceTimeoutSeconds: 120,
	}
	membership := PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 4,
		StorageLease: lease,
		Members: []PoolMember{
			{HostID: "master-1", SanlockHostID: 1, CertificateSHA256: strings.Repeat("a", 64), Generation: 4, Status: "ready"},
			{HostID: "member-1", SanlockHostID: 2, CertificateSHA256: strings.Repeat("b", 64), Generation: 4, Status: "ready"},
		},
	}
	if err := membership.Valid(); err != nil {
		t.Fatalf("valid lease membership rejected: %v", err)
	}
	for name, change := range map[string]func(*PoolMembership){
		"duplicate host lease ID":    func(m *PoolMembership) { m.Members[1].SanlockHostID = 1 },
		"out of range host lease ID": func(m *PoolMembership) { m.Members[1].SanlockHostID = PoolSanlockMaxHostID + 1 },
		"missing host lease ID":      func(m *PoolMembership) { m.Members[1].SanlockHostID = 0 },
		"non-NFS repository": func(m *PoolMembership) {
			m.StorageLease.Repositories[0].RepositoryIdentity = StorageIdentity{SMB: &SMBStorageIdentity{Server: "files.example.test", Share: "vms", FilesystemID: "1"}}
		},
		"wrong lockspace": func(m *PoolMembership) { m.StorageLease.Repositories[0].Lockspace = "client-path" },
		"unsorted repositories": func(m *PoolMembership) {
			m.StorageLease.Repositories = append(m.StorageLease.Repositories,
				PoolStorageLeaseRepository{RepositoryID: "repo-0", RepositoryGeneration: 1, RepositoryIdentity: identity, Lockspace: PoolStorageLeaseLockspaceName("pool-1", "repo-0")})
		},
		"bound shorter than dead interval": func(m *PoolMembership) { m.StorageLease.FenceTimeoutSeconds = 59 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := membership
			bad.Members = append([]PoolMember(nil), membership.Members...)
			copyLease := *lease
			bad.StorageLease = &copyLease
			change(&bad)
			if bad.Valid() == nil {
				t.Fatal("unsafe lease membership accepted")
			}
		})
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

// A member running an older Core stamps no profile on its results. Refusing the whole report
// would lose every other host's result in it for the sake of one field the master would drop
// anyway, so an unstamped result is accepted and dropped where stamps are compared.
// (Reported by independent review.)
func TestAnUnstampedFenceCheckIsAcceptedAndAMalformedOneIsNot(t *testing.T) {
	older := PoolFenceCheckRequest{Checks: []PoolFenceCheck{
		{HostID: "host-a", Result: PoolFenceCheckOK},
		{HostID: "host-b", Result: PoolFenceCheckUnreachable, Detail: "no route"},
	}}
	if err := older.Valid(); err != nil {
		t.Fatalf("a pass from a Core that does not stamp was refused: %v", err)
	}
	malformed := PoolFenceCheckRequest{Checks: []PoolFenceCheck{{HostID: "host-a", Result: PoolFenceCheckOK, Profile: "zz"}}}
	if malformed.Valid() == nil {
		t.Fatal("a malformed profile stamp was accepted")
	}
}

// Every host's watchdog report crosses the pool's own mTLS, so the master bounds it before it
// records it: the classification is a closed set and the strings are bounded.
func TestAWatchdogReportIsBounded(t *testing.T) {
	if err := (PoolHostWatchdog{Kind: PoolWatchdogHardware, Device: "watchdog0", TimeoutSeconds: 30}).Valid(); err != nil {
		t.Fatalf("a valid watchdog report was refused: %v", err)
	}
	for name, report := range map[string]PoolHostWatchdog{
		"an unsupported kind":  {Kind: "titanium"},
		"an empty kind":        {},
		"a newline in a field": {Kind: PoolWatchdogHardware, Driver: "iTCO\nwdt"},
		"an absurd timeout":    {Kind: PoolWatchdogHardware, TimeoutSeconds: 99999},
	} {
		if report.Valid() == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
