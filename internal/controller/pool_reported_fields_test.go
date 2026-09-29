// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// rawMembers reads GET /members as generic JSON. The typed view would pass a field through
// by construction; these tests are about what reaches the client, so they read the bytes.
func (l *joinLab) rawMembers(t *testing.T) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200).Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func rawMember(t *testing.T, view map[string]any, host string) map[string]any {
	t.Helper()
	members, _ := view["members"].([]any)
	for _, entry := range members {
		row, _ := entry.(map[string]any)
		if row["host_id"] == host {
			return row
		}
	}
	t.Fatalf("no member row for %s: %+v", host, view)
	return nil
}

// TestPoolMembersReportsCoreOwnedTransferAndRestrictionFields: every field Core added to
// the transfer, promotion and restriction reports has to reach the client, or the UI is
// left inferring it from stage names and sentence matching. Nothing here is computed: the
// projection copies the host's own report.
func TestPoolMembersReportsCoreOwnedTransferAndRestrictionFields(t *testing.T) {
	l := newTransferLab(t)
	now := time.Now().UTC()
	l.master.inv.Managed.Transfer = &protocol.PoolTransferStatus{OperationID: "op-1", Action: "promote", Role: "source", TargetHostID: l.member.id, Stage: "freeze", Status: "pending", Frozen: true, Cancellable: true, HandedOff: true, AuthorityGeneration: 9, ObservedAt: now}
	l.member.inv.Managed.Transfer = &protocol.PoolTransferStatus{OperationID: "op-1", Action: "promote", Role: "target", TargetHostID: l.member.id, Stage: "activate", Status: "pending", Cancellable: true, AuthorityGeneration: 9, ObservedAt: now}
	l.member.inv.Managed.Restricted = &protocol.PoolRestrictedStatus{Kind: "superseded", Reason: "this host's pool authority was superseded by another master", Since: now, Conflicts: []string{"vm-7"}, ObservedAt: now}
	l.member.inv.Managed.Promotion = &protocol.PoolPromotionStatus{OperationID: "op-2", Stage: "fence", Status: "pending", Cancellable: false, ObservedAt: now,
		Verdict: &protocol.PoolPromotionVerdict{PoolID: l.master.pool, CandidateHostID: l.member.id, OldMasterHostID: l.master.id, AuthorityGeneration: 9,
			CanPromote: false, ReadyToFence: true, FenceRequired: true, FenceRecorded: true, FenceUsable: false,
			FenceUnusableReason: "this host does not report a unique hardware identity, so a management controller cannot be confirmed to be its own", ObservedAt: now}}
	l.send(&l.master, nil)
	l.send(&l.member, nil)

	view := l.rawMembers(t)
	transfer, _ := view["transfer"].(map[string]any)
	if transfer == nil || transfer["cancellable"] != true {
		t.Fatalf("the master's own cancellable flag did not reach the client: %+v", view["transfer"])
	}
	// handed_off is the source's own answer to "has the authority already moved?", and the
	// resume gate reads it instead of matching Core's sentence, so it must arrive verbatim
	// and be present on a target that never sets it.
	if transfer["handed_off"] != true {
		t.Fatalf("the master's own handed_off flag did not reach the client: %+v", view["transfer"])
	}
	member := rawMember(t, view, l.member.id)
	memberTransfer, _ := member["transfer"].(map[string]any)
	if memberTransfer == nil || memberTransfer["cancellable"] != true {
		t.Fatalf("the member's own cancellable flag did not reach the client: %+v", member["transfer"])
	}
	if got, ok := memberTransfer["handed_off"]; !ok || got != false {
		t.Fatalf("the target's handed_off flag = %v (present %v), want false and present: %+v", got, ok, memberTransfer)
	}
	restricted, _ := member["restricted"].(map[string]any)
	if restricted == nil || restricted["kind"] != "superseded" {
		t.Fatalf("the restriction kind did not reach the client: %+v", member["restricted"])
	}
	promotion, _ := view["promotion"].(map[string]any)
	if promotion == nil || promotion["host_id"] != l.member.id {
		t.Fatalf("the candidate's promotion was not projected: %+v", view["promotion"])
	}
	// A promotion fences, so Core reports cancellable false — reported, not inferred, so
	// the key must be readable even though it is false.
	if _, ok := promotion["cancellable"]; ok && promotion["cancellable"] != false {
		t.Fatalf("the promotion's cancellable flag was rewritten: %+v", promotion["cancellable"])
	}
	verdict, _ := promotion["verdict"].(map[string]any)
	if verdict == nil {
		t.Fatalf("the promotion verdict was not projected: %+v", promotion)
	}
	for field, want := range map[string]any{
		"ready_to_fence":        true,
		"fence_recorded":        true,
		"fence_usable":          false,
		"can_promote":           false,
		"fence_unusable_reason": "this host does not report a unique hardware identity, so a management controller cannot be confirmed to be its own",
	} {
		if got, ok := verdict[field]; !ok || got != want {
			t.Fatalf("verdict %s = %v (present %v), want %v", field, got, ok, want)
		}
	}
	// A promotion that fences reports cancellable false and it stays false.
	l.member.inv.Managed.Promotion.Cancellable = true
	l.send(&l.member, nil)
	promotion, _ = l.rawMembers(t)["promotion"].(map[string]any)
	if promotion["cancellable"] != true {
		t.Fatalf("the promotion's reported cancellable flag was not mirrored: %+v", promotion)
	}
}

// TestPromotionPreflightReportsTheWholeFenceVerdict: the preflight body is the candidate's
// verdict verbatim, including the fields that tell the UI whether confirming proceeds to a
// fence or to a refusal.
func TestPromotionPreflightReportsTheWholeFenceVerdict(t *testing.T) {
	l := newPromotionLab(t)
	l.forget(&l.master)
	w := l.preflight(&l.member, map[string]string{"host_id": l.member.id}, func(request protocol.PoolRequest) protocol.PoolResult {
		result := labVerdict(request, false)
		result.Promotion.ReadyToFence = true
		result.Promotion.FenceRecorded = true
		result.Promotion.FenceUsable = true
		result.Promotion.FenceUnusableReason = ""
		return result
	})
	body := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	verdict, _ := body["promotion"].(map[string]any)
	if verdict == nil || verdict["ready_to_fence"] != true || verdict["fence_recorded"] != true || verdict["fence_usable"] != true {
		t.Fatalf("the preflight did not relay the fence verdict fields: %+v", body["promotion"])
	}
	if _, ok := verdict["fence_unusable_reason"]; ok {
		t.Fatalf("an empty reason must not be sent as a value: %+v", verdict)
	}
}

// TestPoolMembersReportsWhenTheMasterLastReported: the stale-master banner has to be able
// to say since when. The timestamp is the hosts row's own last_seen — the mirror's record
// of a host report — and nothing new is stored for it.
func TestPoolMembersReportsWhenTheMasterLastReported(t *testing.T) {
	l := newTransferLab(t)
	view := l.members(t)
	seen, err := time.Parse(time.RFC3339, view.MasterSeenAt)
	if err != nil || time.Since(seen) > time.Minute {
		t.Fatalf("master_seen_at = %q (%v)", view.MasterSeenAt, err)
	}
	// It is the last report, not the freshness verdict: a master that has gone quiet still
	// says when it was last heard from.
	l.forget(&l.master)
	if quiet := l.members(t); quiet.MasterConnection != "stale" || quiet.MasterSeenAt != view.MasterSeenAt {
		t.Fatalf("a quiet master lost its last-seen stamp: %+v", quiet)
	}
	// A host that has never reported has no stamp at all.
	if got := hostSeenAt(""); got != "" {
		t.Fatalf("a host that never reported claimed a last-seen of %q", got)
	}
}

// TestPoolMemberChecksIncludeTheTransferCheck: hosts report pool-authority-transfer like
// the other pool checks, so it belongs in the same column. The order is the client
// contract, so this pins it.
func TestPoolMemberChecksIncludeTheTransferCheck(t *testing.T) {
	l := newTransferLab(t)
	l.member.inv.Checks = []protocol.Check{{Name: "pool-authority-transfer", Status: "failed", Error: "this host has not re-pointed at the new pool master", ObservedAt: time.Now().UTC()}}
	l.send(&l.member, nil)
	checks := l.memberView(t, l.member.id).Checks
	want := []string{"pool-storage-reconciliation", "pool-network-reconciliation", "pool-media-reconciliation", "pool-authority-replica", "pool-restricted", "pool-promotion", "pool-authority-transfer"}
	if len(checks) != len(want) {
		t.Fatalf("member checks: %+v", checks)
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Fatalf("check %d is %q, want %q: %+v", i, checks[i].Name, name, checks)
		}
	}
	last := checks[len(checks)-1]
	if last.Status != "failed" || last.Detail != "this host has not re-pointed at the new pool master" {
		t.Fatalf("the transfer check was not projected from the host's own report: %+v", last)
	}
}

// TestPoolMembersReplacesRestrictedFailureWithCoreClear proves the projection consumes the
// Core-owned clear sample. PoolRestrictedCheck emits this healthy observation only after the
// durable restriction is gone; retaining the prior failed row would falsely show a recovered
// host as restricted.
func TestPoolMembersReplacesRestrictedFailureWithCoreClear(t *testing.T) {
	l := newTransferLab(t)
	failedAt := time.Now().UTC()
	l.member.inv.Managed.Restricted = &protocol.PoolRestrictedStatus{Kind: "unconfirmed", Reason: "this pool master has not confirmed its authority since it restarted", Since: failedAt, ObservedAt: failedAt}
	l.member.inv.Checks = []protocol.Check{{Name: "pool-restricted", Status: "failed", Freshness: "fresh", ObservedAt: failedAt, Error: l.member.inv.Managed.Restricted.Reason}}
	l.send(&l.member, nil)

	if member := l.memberView(t, l.member.id); member.Restricted == nil || member.Checks[4].Status != "failed" {
		t.Fatalf("restricted Core report was not projected: %+v", member)
	}

	clearedAt := failedAt.Add(time.Millisecond)
	l.member.inv.Managed.Restricted = nil
	l.member.inv.Checks = []protocol.Check{{Name: "pool-restricted", Status: "healthy", Freshness: "fresh", ObservedAt: clearedAt}}
	l.send(&l.member, nil)

	member := l.memberView(t, l.member.id)
	if member.Restricted != nil {
		t.Fatalf("cleared Core restriction remained projected: %+v", member.Restricted)
	}
	for _, check := range member.Checks {
		if check.Name == "pool-restricted" {
			if check.Status != "healthy" || check.Detail != "" {
				t.Fatalf("clear Core check = %+v, want healthy without old refusal", check)
			}
			return
		}
	}
	t.Fatalf("pool-restricted check missing from %+v", member.Checks)
}

// TestForwardOnlyCancelRefusalsAreConflicts: Core refuses a cancel once the transfer has
// taken its first irreversible step. Those refusals are conflicts an operator must act on,
// and none of them is retried unchanged — the transfer completes, it does not come back.
func TestForwardOnlyCancelRefusalsAreConflicts(t *testing.T) {
	l := newTransferLab(t)
	for i, refusal := range []string{
		"host " + l.member.id + " is already removed from this pool's membership; the eject can only go forward",
		"this pool promotion has powered the old pool master off; it cannot be cancelled and can only go forward",
	} {
		id := "forward-only-" + string(rune('a'+i))
		l.a.call("POST", "/api/v1/pools/"+l.master.pool+"/master/cancel", map[string]any{"request_id": id, "host_id": l.member.id}, nil, 202)
		commands := l.send(&l.master, nil).Commands
		if len(commands) != 1 {
			t.Fatalf("master did not receive the cancel: %+v", commands)
		}
		l.send(&l.master, func(r *protocol.ReportRequest) {
			r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "failed", Error: refusal, ObservedAt: time.Now().UTC()}}
		})
		last := l.members(t).LastTransfer
		if last == nil || last.Action != "transfer-cancel" || !last.Conflict || last.Retryable {
			t.Fatalf("%q was surfaced as %+v", refusal, last)
		}
	}
}

func claimInventory(pool, master string, generation uint64, hosts ...string) protocol.Inventory {
	claim := &protocol.PoolMembership{PoolID: pool, MasterHostID: master, Generation: generation}
	for _, host := range hosts {
		claim.Members = append(claim.Members, protocol.PoolMember{HostID: host, Hostname: host, CertificateSHA256: labCertificateHash, Generation: generation, Status: "ready", ObservedAt: time.Now().UTC()})
	}
	return protocol.Inventory{Managed: &protocol.ManagedState{Membership: claim}}
}

// TestMasterClaimantsFollowsTheAdoptionRule pins master_conflict to the rule adoption uses:
// a strictly higher generation reported by the host its own membership names as master is
// evidence this Controller follows, so it is never a conflict. What is left is an
// equal-generation rival, or a newer membership naming a third host, which adoption cannot
// take from this reporter.
func TestMasterClaimantsFollowsTheAdoptionRule(t *testing.T) {
	const pool, master = "p-1", "m"
	hosts := map[string]hostRow{
		// The recorded master itself is never a claimant.
		master: {id: master, poolID: pool, inventory: claimInventory(pool, master, 7, master, "rival-equal")},
		// A rival at the mirrored generation: neither side is newer.
		"rival-equal": {id: "rival-equal", poolID: pool, inventory: claimInventory(pool, "rival-equal", 7, "rival-equal", master)},
		// A self-claim one generation on is exactly what adoptReportedMaster follows.
		"rival-newer": {id: "rival-newer", poolID: pool, inventory: claimInventory(pool, "rival-newer", 8, "rival-newer", master)},
		// A self-claim behind the mirror is stale, not a conflict.
		"rival-older": {id: "rival-older", poolID: pool, inventory: claimInventory(pool, "rival-older", 6, "rival-older", master)},
		// A newer membership naming a third host: adoption follows only that host's own
		// report, so the mirror is knowingly behind and says so.
		"third-party": {id: "third-party", poolID: pool, inventory: claimInventory(pool, "x", 9, "x", "third-party")},
		// A newer membership that still names the recorded master is just a fresher copy.
		"agrees-newer": {id: "agrees-newer", poolID: pool, inventory: claimInventory(pool, master, 9, master, "agrees-newer")},
		// An ordinary member at the mirrored generation.
		"follower": {id: "follower", poolID: pool, inventory: claimInventory(pool, master, 7, master, "follower")},
		// Another pool's master is not this pool's conflict.
		"other-pool": {id: "other-pool", poolID: "p-2", inventory: claimInventory("p-2", "other-pool", 9, "other-pool")},
		// An invalid membership is no evidence at all.
		"garbage": {id: "garbage", poolID: pool, inventory: claimInventory(pool, "garbage", 7)},
		// A host that reports no membership.
		"silent": {id: "silent", poolID: pool},
	}
	got := masterClaimants(hosts, pool, master, 7)
	want := []string{"rival-equal", "third-party"}
	if len(got) != len(want) {
		t.Fatalf("master_conflict = %v, want %v", got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Fatalf("master_conflict = %v, want %v", got, want)
		}
	}
	// With no mirrored membership there is no generation for a self-claim to equal, and
	// every self-claim is therefore adoptable rather than a conflict; a membership naming
	// a third host still is one.
	if got = masterClaimants(hosts, pool, master, 0); len(got) != 1 || got[0] != "third-party" {
		t.Fatalf("master_conflict without a mirrored membership = %v", got)
	}
}

// TestPoolMembersReportsWhenEachMemberJoined: joined_at is the master's durable admission
// time for the member, copied as reported. Like peer_origin it is a record, not an
// observation, so a quiet master still reports it; a record without one reports "".
func TestPoolMembersReportsWhenEachMemberJoined(t *testing.T) {
	l := newTransferLab(t)
	joined := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	l.master.inv.Managed.Membership.Members[1].ObservedAt = joined
	l.master.inv.Managed.Membership.Members[0].ObservedAt = time.Time{}
	l.send(&l.master, nil)
	if got := rawMember(t, l.rawMembers(t), l.member.id)["joined_at"]; got != "2026-09-01T10:30:00Z" {
		t.Fatalf("joined_at = %v", got)
	}
	if got := rawMember(t, l.rawMembers(t), l.master.id)["joined_at"]; got != "" {
		t.Fatalf("a member record without an admission time reported joined_at %v", got)
	}
	l.forget(&l.master)
	if got := rawMember(t, l.rawMembers(t), l.member.id)["joined_at"]; got != "2026-09-01T10:30:00Z" {
		t.Fatalf("a quiet master lost the join time: %v", got)
	}
}
