// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// A management-controller fence device is the pool master's record, not this Controller's.
// Replace the Controller and the whole thing — the profile, the account it signs in with and
// the machine identity that makes the profile safe to use — comes back from the master's next
// report, with nothing to re-enter and no credential ever reaching this process.
func TestBMCFenceProfileIsAdoptedFromTheMastersReport(t *testing.T) {
	l := newJoinLab(t)
	membership := selfMembership(l.master.id, l.master.pool)
	membership.Generation = 2
	membership.Members[0].Generation = 2
	membership.Members[0].SystemUUID = "4c4c4544-0037-3810-8057-b4c04f435331"
	membership.Members = append(membership.Members, protocol.PoolMember{
		HostID: l.member.id, CertificateSHA256: labCertificateHash, Generation: 2, Status: "ready",
		ObservedAt: time.Now().UTC(), SystemUUID: "5b5b5655-0148-4921-9168-c5d15f546442",
		Fence: &protocol.StorageClusterFence{
			Kind: protocol.FenceRedfish, Endpoint: "bmc.example.test", TargetID: "system-1",
			Username: "nex-fence", TLSFingerprint: strings.Repeat("ab", 32),
			CredentialRef: "core-private-reference",
		},
		FenceCredentialVersion: 3,
	})
	l.master.inv.Managed.Membership = membership
	l.send(&l.master, nil)
	l.send(&l.member, nil)

	row := rawMember(t, l.rawMembers(t), l.member.id)
	fence, _ := row["fence"].(map[string]any)
	if fence == nil {
		t.Fatalf("the master's fence record did not reach the client: %+v", row)
	}
	for field, want := range map[string]any{
		"kind":               "redfish",
		"endpoint":           "bmc.example.test",
		"username":           "nex-fence",
		"tls_fingerprint":    strings.Repeat("ab", 32),
		"configured":         true,
		"credential_version": float64(3),
	} {
		if got, ok := fence[field]; !ok || got != want {
			t.Fatalf("fence %s = %v (present %v), want %v", field, got, ok, want)
		}
	}
	// Nothing private is projected: not the Core-side credential reference, and not the
	// target the profile names.
	if body := l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200).Body.String(); strings.Contains(body, "core-private-reference") {
		t.Fatalf("a Core fence credential reference reached a client: %s", body)
	}

	// And the record a fresh Controller adopts is the host's own report, mirrored whole:
	// the machine identity is still there to be read back, because this Controller derived
	// none of it.
	var stored protocol.PoolMembership
	err := withTx(t, l, func(tx *sql.Tx) error {
		inv, invErr := hostInventory(tx, l.master.id)
		if invErr != nil {
			return invErr
		}
		stored = *reportedMembership(inv)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range stored.Members {
		if member.HostID != l.member.id {
			continue
		}
		if member.SystemUUID != "5b5b5655-0148-4921-9168-c5d15f546442" || member.Fence == nil ||
			member.Fence.Username != "nex-fence" || member.Fence.TLSFingerprint != strings.Repeat("ab", 32) {
			t.Fatalf("a replacement Controller would not recover the fencing record: %+v", member)
		}
		return
	}
	t.Fatalf("the member is missing from the adopted membership: %+v", stored)
}

func withTx(t *testing.T, l *joinLab, fn func(*sql.Tx) error) error {
	t.Helper()
	tx, err := l.a.c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

// Whether a host may configure its own management controller is a field of the membership the
// pool master owns, so the request is relayed to the master and takes the same membership
// check a fence profile does. The Controller keeps nothing of its own about it.
func TestBMCProvisioningIsRelayedToTheMasterThatOwnsTheRecord(t *testing.T) {
	l := newJoinLab(t)
	// The same membership a fence profile needs, because it takes the same check: this is a
	// field of the record the master owns.
	membership := selfMembership(l.master.id, l.master.pool)
	membership.Generation = 2
	membership.Members[0].Generation = 2
	membership.Members = append(membership.Members, protocol.PoolMember{
		HostID: l.member.id, CertificateSHA256: labCertificateHash, Generation: 2, Status: "ready", ObservedAt: time.Now().UTC()})
	l.master.inv.Managed.Membership = membership
	l.send(&l.master, nil)
	path := "/api/v1/pools/" + l.master.pool + "/members/" + l.member.id + "/bmc-provisioning"
	var accepted promotionAcceptance
	read(t, l.a.call("POST", path, map[string]any{"request_id": "bmc-1"}, nil, 202), &accepted)

	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Kind != "pool-bmc-provisioning" || commands[0].Pool == nil {
		t.Fatalf("the master did not receive the request: %+v", commands)
	}
	if commands[0].Pool.Action != "bmc-provisioning" || commands[0].Pool.MemberHostID != l.member.id ||
		commands[0].Pool.Membership.MasterHostID != l.master.id {
		t.Fatalf("command = %+v", commands[0].Pool)
	}
	if err := commands[0].Pool.Valid(); err != nil {
		t.Fatalf("delivered command is invalid: %v", err)
	}
	// The host itself is not asked for anything: it reads the decision from the pool
	// records it already holds.
	if memberCommands := l.send(&l.member, nil).Commands; len(memberCommands) != 0 {
		t.Fatalf("the host received a command it does not need: %+v", memberCommands)
	}
	// A host that is not a member of the named pool is refused, exactly as it is for a
	// fence profile: the audit entry must never name a pool the host is not in.
	l.master.inv.Managed.Membership = selfMembership(l.master.id, l.master.pool)
	l.send(&l.master, nil)
	l.a.call("POST", path, map[string]any{"request_id": "bmc-2"}, nil, 409)

	// An exact retry answers with the same operation, never a second one.
	l.a.call("POST", path, map[string]any{"request_id": "bmc-1"}, nil, 200)
	var rows int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='pool-bmc-provisioning'").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("exact retry made a second row: %d (%v)", rows, err)
	}
}

// Nothing Nex Core decides for itself may be supplied by a client: the mark that says Core
// created the account behind a profile, or the cipher suite it selected.
func TestAClientCannotClaimCoreProvisionedAProfile(t *testing.T) {
	l := newJoinLab(t)
	for _, fence := range []map[string]any{
		{"kind": "ipmilan", "endpoint": "bmc.example.test", "target_id": "1", "username": "nexfence", "provisioned_by_core": true},
		{"kind": "ipmilan", "endpoint": "bmc.example.test", "target_id": "1", "username": "nexfence", "cipher": 17},
	} {
		w := callRaw(l.a, "POST", l.fencePath(l.master.id), map[string]any{
			"request_id": "claim-" + fence["kind"].(string), "fence": fence, "credential": map[string]any{"secret": "lab-secret"}})
		if w.Code != 400 {
			t.Fatalf("a client-supplied Core decision was accepted: %d %s", w.Code, w.Body.String())
		}
	}
}

// Host fencing is one pool-wide setting the master keeps. The Controller relays the operator's
// request, projects what the master reports and stores neither: replace it and the setting,
// the count and Core's prerequisite checklist all come back from the master's next report.
func TestHostFencingIsRelayedAndTheChecklistIsMirrored(t *testing.T) {
	l := newJoinLab(t)
	membership := selfMembership(l.master.id, l.master.pool)
	membership.Generation = 2
	membership.Members[0].Generation = 2
	membership.Members[0].Watchdog = &protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogSoftware, Driver: "softdog"}
	membership.FenceSummary = &protocol.PoolFenceSummary{Ready: 0, Total: 1, Unready: []string{l.master.id}, HostFencing: true,
		Cluster: &protocol.PoolFenceCluster{State: protocol.PoolClusterDegraded, Total: 2,
			Witness: &protocol.PoolFenceWitness{State: "operating", Detail: "connected and voting"}},
		Prerequisites: []protocol.PoolFencePrerequisite{
			{Name: protocol.PoolFencePrerequisiteHosts, State: protocol.PoolFencePrerequisiteUnmet, Detail: "a watchdog needs three or more hosts"},
			{Name: protocol.PoolFencePrerequisiteQuorum, State: protocol.PoolFencePrerequisiteUnknown, Detail: "this pool has no quorum service yet"},
		}}
	l.master.inv.Managed.Membership = membership
	l.send(&l.master, nil)

	var view map[string]any
	read(t, l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200), &view)
	fence, _ := view["fence"].(map[string]any)
	if fence == nil || fence["host_fencing"] != true || fence["total"] != float64(1) {
		t.Fatalf("the pool's fencing summary did not reach the client: %+v", view["fence"])
	}
	if checklist, _ := fence["prerequisites"].([]any); len(checklist) != 2 {
		t.Fatalf("Core's prerequisite checklist did not reach the client: %+v", fence["prerequisites"])
	}
	cluster, _ := fence["cluster"].(map[string]any)
	witness, _ := cluster["witness"].(map[string]any)
	if witness["state"] != "operating" {
		t.Fatalf("Core's witness observation did not reach the client: %+v", cluster)
	}
	// The host's own watchdog report is mirrored on its row, verbatim.
	row := rawMember(t, l.rawMembers(t), l.master.id)
	rowFence, _ := row["fence"].(map[string]any)
	watchdog, _ := rowFence["watchdog"].(map[string]any)
	if watchdog == nil || watchdog["kind"] != "software" {
		t.Fatalf("the host's watchdog report did not reach the client: %+v", rowFence)
	}

	path := "/api/v1/pools/" + l.master.pool + "/host-fencing"
	var accepted promotionAcceptance
	read(t, l.a.call("POST", path, map[string]any{"request_id": "fencing-1", "enabled": false}, nil, 202), &accepted)
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Kind != "pool-host-fencing" || commands[0].Pool == nil {
		t.Fatalf("the master did not receive the setting: %+v", commands)
	}
	if commands[0].Pool.Action != "host-fencing" || commands[0].Pool.Enabled ||
		commands[0].Pool.Membership.MasterHostID != l.master.id || commands[0].Pool.Valid() != nil {
		t.Fatalf("command = %+v", commands[0].Pool)
	}
	// An exact retry answers with the same operation, never a second one.
	l.a.call("POST", path, map[string]any{"request_id": "fencing-1", "enabled": false}, nil, 200)
	var rows int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='pool-host-fencing'").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("exact retry made a second row: %d (%v)", rows, err)
	}
	// A request with no stable identity is refused before anything is recorded.
	l.a.call("POST", path, map[string]any{"enabled": true}, nil, 400)
}

// F2's Controller mirror: the same password Core refuses is refused at the form, so an
// operator is told while they are typing rather than by a fence that fails months later.
// (Reported by independent review of slice 4a; regression added after slice 4b's review.)
func TestAPaddedFenceAccountPasswordIsRefusedAtTheBoundary(t *testing.T) {
	for _, secret := range []string{"hunter2 ", " hunter2", "hunter2\t"} {
		if scopedCredentialValid("fence_bmc", protocol.StorageCredentials{Username: "nexfence", Secret: secret}) {
			t.Fatalf("a password a fence agent would alter was accepted: %q", secret)
		}
	}
	if !scopedCredentialValid("fence_bmc", protocol.StorageCredentials{Username: "nexfence", Secret: "hunter2"}) {
		t.Fatal("a usable fence password was refused")
	}
}
