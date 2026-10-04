// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

const labFenceReference = "5f3a9c1e8b7d4a2f6c0e1d3b"

// newPromotionLab is a joined two-host pool where the member is a promotion candidate: it
// holds a verified replica and advertises the transfer capability, and the master can
// receive a credential offer.
func newPromotionLab(t *testing.T) *joinLab {
	t.Helper()
	l := newTransferLab(t)
	l.master.caps = append(l.master.caps, protocol.StorageRepositoryCapability)
	l.member.inv.Managed.Replica = heldReplica(9, true, "")
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	return l
}

func (l *joinLab) preflightPath() string {
	return "/api/v1/pools/" + l.master.pool + "/master/promote/preflight"
}
func (l *joinLab) promotePath() string {
	return "/api/v1/pools/" + l.master.pool + "/master/promote"
}
func (l *joinLab) fencePath(host string) string {
	return "/api/v1/pools/" + l.master.pool + "/members/" + host + "/fence-profile"
}

func labVerdict(request protocol.PoolRequest, canPromote bool) protocol.PoolResult {
	return protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "succeeded", ObservedAt: time.Now().UTC(),
		Promotion: &protocol.PoolPromotionVerdict{
			PoolID: request.PoolID, CandidateHostID: request.HostID, OldMasterHostID: "core-gone",
			AuthorityGeneration: 9, CanPromote: canPromote, FenceRequired: true,
			Checks: []protocol.PoolPromotionCheck{
				{Name: "replica-integrity", Status: "passed"},
				{Name: "old-master-exclusion", Status: "unknown", Reason: "the pool master core-gone has not been fenced for this promotion; Core fences it and observes the power-off when the promotion is confirmed", Hosts: []string{"core-gone"}},
			},
			ObservedAt: time.Now().UTC(),
		}}
}

// preflight runs one POST while the candidate answers the brokered request from its own
// reports, which is where a pool request is answered: on the report after the one that
// carried it.
func (l *joinLab) preflight(h *joinHost, body any, answer func(protocol.PoolRequest) protocol.PoolResult) *httptest.ResponseRecorder {
	l.t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- callRaw(l.a, "POST", l.preflightPath(), body) }()
	deadline := time.Now().Add(time.Minute)
	for answer != nil && time.Now().Before(deadline) {
		select {
		case w := <-done:
			return w
		default:
		}
		response := l.send(h, nil)
		if len(response.PoolRequests) > 0 {
			l.send(h, func(r *protocol.ReportRequest) {
				for _, request := range response.PoolRequests {
					r.PoolResults = append(r.PoolResults, answer(request))
				}
			})
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case w := <-done:
		return w
	case <-time.After(20 * time.Second):
		l.t.Fatal("promotion preflight never returned")
	}
	return nil
}

type preflightResponse struct {
	HostID    string                         `json:"host_id"`
	Promotion *protocol.PoolPromotionVerdict `json:"promotion"`
}

type promotionAcceptance struct {
	HostID    string             `json:"host_id"`
	Action    string             `json:"action"`
	Operation protocol.Operation `json:"operation"`
}

// TestPromotionPreflightIsOneRoundTripAndStoresNothing: the verdict is the candidate's, the
// round trip is one report, and the Controller keeps none of it — including when the pool
// master is long gone, which is the only situation a preflight is ever run in.
func TestPromotionPreflightIsOneRoundTripAndStoresNothing(t *testing.T) {
	l := newPromotionLab(t)
	// The master is the host that died. That is the point, so it must not block anything.
	l.forget(&l.master)
	var got preflightResponse
	read(t, l.preflight(&l.member, map[string]string{"host_id": l.member.id}, func(request protocol.PoolRequest) protocol.PoolResult {
		if request.Action != "promotion-preflight" || request.HostID != l.member.id || request.PoolID != l.master.pool || request.MemberHostID != "" {
			t.Errorf("unexpected pool request: %+v", request)
		}
		return labVerdict(request, false)
	}), &got)
	if got.HostID != l.member.id || got.Promotion == nil || got.Promotion.CandidateHostID != l.member.id || len(got.Promotion.Checks) != 2 {
		t.Fatalf("verdict was not relayed verbatim: %+v", got)
	}
	if got.Promotion.Checks[1].Status != "unknown" || !got.Promotion.FenceRequired {
		t.Fatalf("an unproved exclusion must not be reported as passed: %+v", got.Promotion.Checks[1])
	}
	// Nothing was journalled, audited or queued: a preflight changes nothing.
	var rows, audits int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("a preflight stored %d managed rows (%v)", rows, err)
	}
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM audit WHERE action IN ('pool.promote','pool.fence-profile')").Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("a preflight wrote %d pool audit rows (%v)", audits, err)
	}
	if commands := l.send(&l.member, nil).Commands; len(commands) != 0 {
		t.Fatalf("a preflight queued a command: %+v", commands)
	}
}

// TestPromotionPreflightDeadlineAndRefusals: the candidate is the host that must be fresh,
// capable and not already the master; the deadline is honest about who did not answer.
func TestPromotionPreflightDeadlineAndRefusals(t *testing.T) {
	l := newPromotionLab(t)
	previous := poolJoinWait
	poolJoinWait = 300 * time.Millisecond
	t.Cleanup(func() { poolJoinWait = previous })

	w := l.preflight(&l.member, map[string]string{"host_id": l.member.id}, nil)
	if w.Code != 504 || !strings.Contains(w.Body.String(), "the promotion candidate did not answer the promotion preflight before the deadline") {
		t.Fatalf("deadline answer: %d %s", w.Code, w.Body.String())
	}
	l.a.call("POST", l.preflightPath(), map[string]string{"host_id": ""}, nil, 400)
	l.a.call("POST", "/api/v1/pools/no-such-pool/master/promote/preflight", map[string]string{"host_id": l.member.id}, nil, 404)
	l.a.call("POST", l.preflightPath(), map[string]string{"host_id": "no-such-host"}, nil, 404)
	if body := l.a.call("POST", l.preflightPath(), map[string]string{"host_id": l.master.id}, nil, 409).Body.String(); !strings.Contains(body, "already the pool master") {
		t.Fatalf("the recorded master was not refused: %s", body)
	}
	// A candidate that is not reporting cannot answer, and is refused before anything is
	// relayed to it.
	l.forget(&l.member)
	if body := l.a.call("POST", l.preflightPath(), map[string]string{"host_id": l.member.id}, nil, 409).Body.String(); !strings.Contains(body, "must be fresh and active to answer a preflight") {
		t.Fatalf("a quiet candidate was not refused: %s", body)
	}
}

// TestPromoteRelaysOneCommandToTheCandidate is the whole relay: one durable command, sent to
// the candidate rather than the master, carrying the operator's acknowledgement and nothing
// else. The master is not told anything; it is gone.
func TestPromoteRelaysOneCommandToTheCandidate(t *testing.T) {
	l := newPromotionLab(t)
	l.forget(&l.master)
	body := map[string]any{"request_id": "promote-1", "host_id": l.member.id, "confirmation": l.poolName(t), "fence_confirmed": true, "fence_host_ids": []string{l.master.id}}
	var accepted promotionAcceptance
	read(t, l.a.call("POST", l.promotePath(), body, nil, 202), &accepted)
	if accepted.HostID != l.member.id || accepted.Action != "promote" || accepted.Operation.Kind != "pool-promote" || accepted.Operation.HostID != l.member.id {
		t.Fatalf("unexpected acceptance: %+v", accepted)
	}
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].Kind != "pool-promote" || commands[0].Pool == nil {
		t.Fatalf("the candidate did not receive the promotion: %+v", commands)
	}
	cmd := commands[0].Pool
	if cmd.Action != "promote" || cmd.MemberHostID != l.member.id || cmd.RequestID != "promote-1" || !cmd.FenceConfirmed {
		t.Fatalf("unexpected command: %+v", cmd)
	}
	// No membership is composed here: the candidate reads the replica it already holds.
	if cmd.Membership.MasterHostID != "" || cmd.Membership.Members != nil || cmd.ExpectedVersion != 0 {
		t.Fatalf("the Controller composed pool state: %+v", cmd.Membership)
	}
	if err := cmd.Valid(); err != nil {
		t.Fatalf("delivered command is invalid: %v", err)
	}
	var replay promotionAcceptance
	read(t, l.a.call("POST", l.promotePath(), body, nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("exact retry made a second operation: %+v", replay)
	}
	changed := map[string]any{"request_id": "promote-1", "host_id": l.master.id, "confirmation": l.poolName(t), "fence_confirmed": true, "fence_host_ids": []string{l.master.id}}
	l.a.call("POST", l.promotePath(), changed, nil, 409)
	var rows int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='pool-promote'").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("expected exactly one stored promotion row, got %d (%v)", rows, err)
	}
	var audited int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM audit WHERE action='pool.promote' AND resource_id=?", l.master.pool).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("promotion audit rows: %d (%v)", audited, err)
	}
}

// TestPromoteRefusalMatrix covers every refusal the Controller is allowed to make, and the
// one it must never make: it does not judge the old pool master.
func TestPromoteRefusalMatrix(t *testing.T) {
	l := newPromotionLab(t)
	name := l.poolName(t)
	base := func(extra map[string]any) map[string]any {
		body := map[string]any{"request_id": "refuse-" + randomSuffix(extra), "host_id": l.member.id, "confirmation": name, "fence_confirmed": true, "fence_host_ids": []string{l.master.id}}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	if body := l.a.call("POST", l.promotePath(), base(map[string]any{"confirmation": "not-the-pool"}), nil, 400).Body.String(); !strings.Contains(body, "must be the pool name") {
		t.Fatalf("a wrong confirmation was not refused: %s", body)
	}
	if body := l.a.call("POST", l.promotePath(), base(map[string]any{"fence_confirmed": false}), nil, 400).Body.String(); !strings.Contains(body, "powers the old pool master off") {
		t.Fatalf("a missing acknowledgement was not refused: %s", body)
	}
	l.a.call("POST", l.promotePath(), map[string]any{"host_id": l.member.id, "confirmation": name, "fence_confirmed": true, "fence_host_ids": []string{l.master.id}}, nil, 400)
	l.a.call("POST", "/api/v1/pools/no-such-pool/master/promote", base(nil), nil, 404)
	l.a.call("POST", l.promotePath(), base(map[string]any{"host_id": "no-such-host"}), nil, 404)
	if body := l.a.call("POST", l.promotePath(), base(map[string]any{"host_id": l.master.id}), nil, 409).Body.String(); !strings.Contains(body, "already the pool master") {
		t.Fatalf("the recorded master was not refused: %s", body)
	}

	// The old master answering the Controller is not evidence about the pool network, so a
	// promotion is accepted while it is perfectly fresh here: Core probes it and refuses.
	l.a.call("POST", l.promotePath(), base(map[string]any{"request_id": "master-fresh"}), nil, 202)

	// A candidate whose own membership names it master is already the master.
	// At the same generation as the mirrored one, so this is a claim, not evidence: the
	// pool row does not move and the refusal is about what the candidate itself says.
	l.member.inv.Managed.Membership = l.transferMembership(2, l.member.id)
	l.send(&l.member, nil)
	if body := l.a.call("POST", l.promotePath(), base(map[string]any{"request_id": "self-master"}), nil, 409).Body.String(); !strings.Contains(body, "already the pool master") {
		t.Fatalf("a self-declared master was not refused: %s", body)
	}
	// A candidate that is a member but not ready cannot be promoted.
	notReady := l.transferMembership(2, l.master.id)
	notReady.Members[1].Status = "pending"
	l.member.inv.Managed.Membership = notReady
	l.send(&l.member, nil)
	if body := l.a.call("POST", l.promotePath(), base(map[string]any{"request_id": "not-ready"}), nil, 409).Body.String(); !strings.Contains(body, "only a ready member of this pool can be promoted") {
		t.Fatalf("a pending member was not refused: %s", body)
	}
	// A candidate that has not reported a membership at all says so honestly.
	l.member.inv.Managed.Membership = nil
	l.send(&l.member, nil)
	if body := l.a.call("POST", l.promotePath(), base(map[string]any{"request_id": "no-membership"}), nil, 409).Body.String(); !strings.Contains(body, "has not reported its pool membership") {
		t.Fatalf("an unreported membership was not refused: %s", body)
	}
	// Freshness is about the candidate.
	l.member.inv.Managed.Membership = l.transferMembership(2, l.master.id)
	l.send(&l.member, nil)
	l.forget(&l.member)
	if body := l.a.call("POST", l.promotePath(), base(map[string]any{"request_id": "quiet"}), nil, 409).Body.String(); !strings.Contains(body, "must be fresh and active to be promoted") {
		t.Fatalf("a quiet candidate was not refused: %s", body)
	}
}

// randomSuffix keeps each refusal on its own request_id without inventing a counter.
func randomSuffix(extra map[string]any) string {
	if id, ok := extra["request_id"].(string); ok {
		return id
	}
	return random()
}

// TestPromotionRefusalFromCoreIsSurfacedVerbatim: Core's own sentence arrives on the
// operation and is shown where the operator acted, unrewritten.
func TestPromotionRefusalFromCoreIsSurfacedVerbatim(t *testing.T) {
	l := newPromotionLab(t)
	var accepted promotionAcceptance
	read(t, l.a.call("POST", l.promotePath(), map[string]any{"request_id": "async-1", "host_id": l.member.id, "confirmation": l.poolName(t), "fence_confirmed": true, "fence_host_ids": []string{l.master.id}}, nil, 202), &accepted)
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 {
		t.Fatalf("candidate did not receive the command: %+v", commands)
	}
	refusal := "the pool master " + l.master.id + " is reachable and holds this pool's authority; change the pool master with a planned master change instead of promoting"
	l.send(&l.member, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "failed", Error: refusal, ObservedAt: time.Now().UTC()}}
	})
	last := l.members(t).LastPromotion
	if last == nil || last.OperationID != accepted.Operation.ID || last.Action != "promote" || last.Error != refusal || last.Conflict || last.Retryable {
		t.Fatalf("Core's refusal was not surfaced verbatim: %+v", last)
	}
}

func TestPromotionOperationRecoversAfterCoreCompletes(t *testing.T) {
	l := newPromotionLab(t)
	var accepted promotionAcceptance
	read(t, l.a.call("POST", l.promotePath(), map[string]any{"request_id": "recover-promote", "host_id": l.member.id, "confirmation": l.poolName(t), "fence_confirmed": true, "fence_host_ids": []string{l.master.id}}, nil, 202), &accepted)
	if commands := l.send(&l.member, nil).Commands; len(commands) != 1 || commands[0].OperationID != accepted.Operation.ID {
		t.Fatalf("promotion command = %+v", commands)
	}
	operation := func() protocol.Operation {
		var operations []protocol.Operation
		read(t, l.a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
		for _, candidate := range operations {
			if candidate.ID == accepted.Operation.ID {
				return candidate
			}
		}
		t.Fatalf("promotion operation %s missing", accepted.Operation.ID)
		return protocol.Operation{}
	}
	l.send(&l.member, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: accepted.Operation.ID, Status: "needs_attention", Error: "temporary fencer failure", ObservedAt: time.Now().UTC()}}
	})
	if got := operation(); got.Status != "needs_attention" {
		t.Fatalf("interrupted promotion = %+v", got)
	}
	l.send(&l.member, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: accepted.Operation.ID, Status: "succeeded", ObservedAt: time.Now().UTC()}}
	})
	if got := operation(); got.Status != "succeeded" || got.Error != "" {
		t.Fatalf("recovered promotion = %+v", got)
	}
}

func TestPromotionOperationRecoversFromInventoryAfterOldControllerAcknowledgedResult(t *testing.T) {
	l := newPromotionLab(t)
	var accepted promotionAcceptance
	read(t, l.a.call("POST", l.promotePath(), map[string]any{"request_id": "recover-old-controller", "host_id": l.member.id, "confirmation": l.poolName(t), "fence_confirmed": true, "fence_host_ids": []string{l.master.id}}, nil, 202), &accepted)
	if commands := l.send(&l.member, nil).Commands; len(commands) != 1 || commands[0].OperationID != accepted.Operation.ID {
		t.Fatalf("promotion command = %+v", commands)
	}
	l.send(&l.member, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: accepted.Operation.ID, Status: "needs_attention", Error: "temporary fencer failure", ObservedAt: time.Now().UTC()}}
	})
	// An older Controller acknowledges Core's later success without settling its
	// operation row. Core then has no unacknowledged result to send after upgrade.
	l.member.inv.Managed.Promotion = &protocol.PoolPromotionStatus{OperationID: accepted.Operation.ID, Stage: "repoint", Status: "needs_attention", ObservedAt: time.Now().UTC()}
	l.send(&l.member, nil)
	if last := l.members(t).LastPromotion; last == nil || last.Status != "needs_attention" {
		t.Fatalf("unfinished promotion settled: %+v", last)
	}
	cfg := l.a.c.cfg
	if err := l.a.c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l.a.c = reopened
	l.member.inv.Managed.Promotion = &protocol.PoolPromotionStatus{OperationID: accepted.Operation.ID, Stage: "done", Status: "succeeded", ObservedAt: time.Now().UTC()}
	l.send(&l.member, nil)
	if last := l.members(t).LastPromotion; last == nil || last.Status != "succeeded" || last.Error != "" {
		t.Fatalf("durable promotion did not settle old receipt: %+v", last)
	}
}

func TestPromotionMembershipConflictIsMarkedConflict(t *testing.T) {
	l := newPromotionLab(t)
	var accepted promotionAcceptance
	read(t, l.a.call("POST", l.promotePath(), map[string]any{"request_id": "async-conflict", "host_id": l.member.id, "confirmation": l.poolName(t), "fence_confirmed": true, "fence_host_ids": []string{l.master.id}}, nil, 202), &accepted)
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 {
		t.Fatalf("candidate did not receive the command: %+v", commands)
	}
	refusal := poolPromotionConflicts[0]
	l.send(&l.member, func(r *protocol.ReportRequest) {
		r.Results = []protocol.Result{{OperationID: commands[0].OperationID, Status: "failed", Error: refusal, ObservedAt: time.Now().UTC()}}
	})
	last := l.members(t).LastPromotion
	if last == nil || last.OperationID != accepted.Operation.ID || last.Error != refusal || !last.Conflict {
		t.Fatalf("membership conflict was not marked: %+v", last)
	}
}

// deliverFenceCredentialFromCore plays Core's half of the one-time credential handoff: it
// collects the offer from its report, redeems it and acknowledges an opaque reference.
func (l *joinLab) postFenceProfile(body any, delivered *protocol.StorageCredentials) *httptest.ResponseRecorder {
	l.t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- callRaw(l.a, "POST", l.fencePath(l.master.id), body) }()
	deadline := time.Now().Add(time.Minute)
	for delivered != nil && time.Now().Before(deadline) {
		select {
		case w := <-done:
			return w
		default:
		}
		offers := l.send(&l.master, nil).CredentialOffers
		if len(offers) == 1 {
			var handoff protocol.StorageCredentialHandoff
			read(l.t, l.a.call("GET", "/api/v1/agent/storage/credentials/"+offers[0].Handle, nil, l.master.cert, 200), &handoff)
			*delivered = handoff.Credentials
			receipt := protocol.StorageCredentialReceipt{Receipt: labFenceReference, RequestID: offers[0].RequestID, RegistrationRequestID: offers[0].RegistrationRequestID, PoolID: offers[0].PoolID, HostID: offers[0].HostID, Status: "ready"}
			l.a.call("POST", "/api/v1/agent/storage/credentials/"+offers[0].Handle+"/ack", receipt, l.master.cert, 200)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case w := <-done:
		return w
	case <-time.After(20 * time.Second):
		l.t.Fatal("fence profile request never returned")
	}
	return nil
}

func labVirshFence() map[string]any {
	return map[string]any{"kind": "virsh", "endpoint": "lab.example.test", "target_id": "core-01", "virsh_uri": "qemu:///system", "ssh_login": "fence", "ssh_port": 22}
}

// TestFenceProfileKeepsTheSecretOutOfTheController: the material goes to Core over the
// existing one-time handoff, and only Core's opaque reference reaches the command. Nothing
// about it is stored, audited or echoed.
func TestFenceProfileKeepsTheSecretOutOfTheController(t *testing.T) {
	l := newPromotionLab(t)
	const key = "-----BEGIN OPENSSH PRIVATE KEY-----\nlab-fence-key\n-----END OPENSSH PRIVATE KEY-----"
	var delivered protocol.StorageCredentials
	body := map[string]any{"request_id": "fence-1", "fence": labVirshFence(),
		"credential": map[string]any{"ssh_private_key": key, "ssh_known_hosts": "lab.example.test ssh-ed25519 AAAAC3lab"}}
	w := l.postFenceProfile(body, &delivered)
	if w.Code != 202 {
		t.Fatalf("fence profile was refused: %d %s", w.Code, w.Body.String())
	}
	if delivered.SSHPrivateKey != key {
		t.Fatalf("Core did not receive the key over the handoff: %+v", delivered)
	}
	if strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), labFenceReference) {
		t.Fatalf("the response echoed credential material: %s", w.Body.String())
	}
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Kind != "pool-fence-profile" || commands[0].Pool == nil || commands[0].Pool.Fence == nil {
		t.Fatalf("the master did not receive the fence profile: %+v", commands)
	}
	fence := commands[0].Pool.Fence
	if fence.CredentialRef != labFenceReference || fence.SSHHostKeyRef != labFenceReference || fence.Kind != "virsh" {
		t.Fatalf("the command does not carry Core's own reference: %+v", fence)
	}
	if commands[0].Pool.Membership.MasterHostID != l.master.id || commands[0].Pool.MemberHostID != l.master.id {
		t.Fatalf("the fence profile was not addressed to the master: %+v", commands[0].Pool)
	}
	if err := commands[0].Pool.Valid(); err != nil {
		t.Fatalf("delivered command is invalid: %v", err)
	}
	// The whole database, and the audit trail, are free of the secret. The stored payload
	// is the client's own profile, so an exact retry still compares equal.
	for _, table := range []string{"SELECT coalesce(group_concat(payload),'')||coalesce(group_concat(command),'') FROM managed_resources", "SELECT coalesce(group_concat(action||resource_id),'') FROM audit"} {
		var dump string
		if err := l.a.c.db.QueryRow(table).Scan(&dump); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(dump, key) || strings.Contains(dump, "lab.example.test ssh-ed25519") {
			t.Fatalf("credential material reached the database: %s", dump)
		}
	}
	var payload string
	if err := l.a.c.db.QueryRow("SELECT payload FROM managed_resources WHERE kind='pool-fence-profile'").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, labFenceReference) || strings.Contains(payload, "credential") {
		t.Fatalf("the replay payload holds the delivered credential: %s", payload)
	}
	var accepted promotionAcceptance
	read(t, l.postFenceProfile(body, nil), &accepted)
	if accepted.Action != "fence-profile" || accepted.HostID != l.master.id {
		t.Fatalf("exact retry answer: %+v", accepted)
	}
	var rows int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='pool-fence-profile'").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("exact retry made a second row: %d (%v)", rows, err)
	}
	var audited int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM audit WHERE action='pool.fence-profile' AND resource_id=?", l.master.pool).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("fence profile audit rows: %d (%v)", audited, err)
	}
}

// TestFenceProfileClearAndRefusals: clearing needs no credential, an invalid profile is
// refused before anything is delivered, and the master must be fresh — this is the one S6
// route that does address the master, because the profile is its membership record.
func TestFenceProfileClearAndRefusals(t *testing.T) {
	l := newPromotionLab(t)
	var accepted promotionAcceptance
	read(t, l.a.call("POST", l.fencePath(l.master.id), map[string]any{"request_id": "clear-1", "fence": nil}, nil, 202), &accepted)
	commands := l.send(&l.master, nil).Commands
	if len(commands) != 1 || commands[0].Pool == nil || commands[0].Pool.Fence != nil || commands[0].Pool.Action != "fence-profile" {
		t.Fatalf("a cleared profile was not relayed as a nil fence: %+v", commands)
	}
	l.a.call("POST", l.fencePath(l.master.id), map[string]any{"request_id": "bad-1", "fence": map[string]any{"kind": "nonsense"}}, nil, 400)
	if body := l.a.call("POST", l.fencePath(l.master.id), map[string]any{"request_id": "bad-2", "fence": nil, "credential": map[string]any{"secret": "x"}}, nil, 400).Body.String(); !strings.Contains(body, "delivered only with a fence profile") {
		t.Fatalf("a credential without a profile was not refused: %s", body)
	}
	// An SSH fence takes key material, not a password.
	l.a.call("POST", l.fencePath(l.master.id), map[string]any{"request_id": "bad-3", "fence": labVirshFence(), "credential": map[string]any{"secret": "hunter2"}}, nil, 400)
	l.a.call("POST", "/api/v1/pools/no-such-pool/members/"+l.master.id+"/fence-profile", map[string]any{"request_id": "bad-4", "fence": nil}, nil, 404)
	l.a.call("POST", l.fencePath("no-such-host"), map[string]any{"request_id": "bad-5", "fence": nil}, nil, 404)
	// A host that is not a member of this pool has no fence profile here.
	third := l.joinThird(t)
	l.master.inv.Managed.Membership = l.transferMembership(2, l.master.id)
	l.send(&l.master, nil)
	if body := l.a.call("POST", l.fencePath(third.id), map[string]any{"request_id": "bad-6", "fence": nil}, nil, 409).Body.String(); !strings.Contains(body, "no such member of this pool") {
		t.Fatalf("a non-member was not refused: %s", body)
	}
	l.forget(&l.master)
	if body := l.a.call("POST", l.fencePath(l.master.id), map[string]any{"request_id": "bad-8", "fence": nil}, nil, 409).Body.String(); !strings.Contains(body, "must be fresh and active to record a fence profile") {
		t.Fatalf("a quiet master was not refused: %s", body)
	}
}

// TestPromotionProjectionShowsMemberOwnReportsWithAStaleMaster is the projection rule S6
// corrects: choosing a promotion candidate means reading each surviving member's own
// report, and the master is the host that is gone.
func TestPromotionProjectionShowsMemberOwnReportsWithAStaleMaster(t *testing.T) {
	l := newPromotionLab(t)
	// The master records a fence profile for itself, which is master-derived state.
	membership := l.transferMembership(2, l.master.id)
	membership.Members[0].Fence = &protocol.StorageClusterFence{Kind: "virsh", Endpoint: "lab.example.test", TargetID: "core-01", CredentialRef: labFenceReference, VirshURI: "qemu:///system", SSHLogin: "fence", SSHPort: 22, SSHHostKeyRef: labFenceReference}
	l.master.inv.Managed.Membership, l.member.inv.Managed.Membership = membership, membership
	l.member.inv.Checks = []protocol.Check{{Name: "pool-restricted", Status: "failed", ObservedAt: time.Now().UTC(), Error: "this pool master has not confirmed its authority since it restarted"}}
	l.member.inv.Managed.Restricted = &protocol.PoolRestrictedStatus{Reason: "this pool master has not confirmed its authority since it restarted", Since: time.Now().UTC(), Conflicts: []string{"vm-7"}, ObservedAt: time.Now().UTC()}
	l.member.inv.Managed.Promotion = &protocol.PoolPromotionStatus{OperationID: "op-1", Stage: "fence", Status: "pending", ObservedAt: time.Now().UTC()}
	l.send(&l.master, nil)
	l.send(&l.member, nil)

	fresh := l.memberView(t, l.member.id)
	master := l.memberView(t, l.master.id)
	if master.Fence == nil || master.Fence.Kind != "virsh" || !master.Fence.Configured || master.Fence.Endpoint != "lab.example.test" {
		t.Fatalf("the master's fence profile was not projected: %+v", master.Fence)
	}
	if strings.Contains(l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200).Body.String(), labFenceReference) {
		t.Fatalf("the projection exposed the credential reference")
	}
	if fresh.Restricted == nil || len(fresh.Restricted.Conflicts) != 1 {
		t.Fatalf("member-own fields missing with a fresh master: %+v", fresh)
	}
	view := l.members(t)
	if view.Promotion == nil || view.Promotion.HostID != l.member.id || view.Promotion.Stage != "fence" {
		t.Fatalf("the candidate's promotion was not projected: %+v", view.Promotion)
	}

	// Now the master goes away. Everything it observes becomes unknown; everything the
	// member reports about itself stays.
	l.forget(&l.master)
	view = l.members(t)
	if view.Authoritative || view.Promotion == nil || view.Promotion.Stage != "fence" {
		t.Fatalf("a quiet master erased the candidate's promotion: %+v", view)
	}
	stale := l.memberView(t, l.member.id)
	if stale.Restricted == nil || stale.Restricted.Reason == "" || stale.Restricted.Conflicts[0] != "vm-7" {
		t.Fatalf("a quiet master erased the member's own restriction: %+v", stale.Restricted)
	}
	restricted := false
	for _, check := range stale.Checks {
		if check.Name == "pool-restricted" {
			restricted = check.Status == "failed"
		}
	}
	if !restricted {
		t.Fatalf("a quiet master erased the member's own checks: %+v", stale.Checks)
	}
	if stale.Replica == nil || stale.Replica.Generation != 9 || !stale.Replica.Verified {
		t.Fatalf("a quiet master erased the member's own replica: %+v", stale.Replica)
	}
	// Master-derived halves are unknown, and no state is claimed that needs the master.
	if stale.Replica.AcknowledgedGeneration != 0 || stale.Replica.AcknowledgedAt != "" || stale.Replica.State != "unknown" {
		t.Fatalf("a quiet master's observation was reported as present truth: %+v", stale.Replica)
	}
	if stale.Status != "unknown" || stale.Maintenance != nil || stale.Fence != nil {
		t.Fatalf("master-derived fields survived a quiet master: %+v", stale)
	}
	// A member that is not reporting either says nothing at all.
	l.forget(&l.member)
	quiet := l.memberView(t, l.member.id)
	if quiet.Restricted != nil || len(quiet.Checks) != 0 || (quiet.Replica != nil && quiet.Replica.Generation != 0) {
		t.Fatalf("a quiet member still reported for itself: %+v", quiet)
	}
}

// TestPromotedMasterIsAdoptedAndTheFencedOldMasterCannotFlapIt: a promotion is adopted
// exactly like a planned master change — the pool follows the host its own membership names
// as master at a strictly higher generation — and the fenced old master's later reports,
// which still name itself at the old generation, change nothing.
func TestPromotedMasterIsAdoptedAndTheFencedOldMasterCannotFlapIt(t *testing.T) {
	l := newPromotionLab(t)
	recorded := func() string {
		var master string
		if err := l.a.c.db.QueryRow("SELECT master_host_id FROM pools WHERE id=?", l.master.pool).Scan(&master); err != nil {
			t.Fatal(err)
		}
		return master
	}
	promoted := l.transferMembership(3, l.member.id)
	l.member.inv.Managed.Membership = promoted
	l.member.inv.Managed.Authority = labAuthority(10)
	l.send(&l.member, nil)
	if recorded() != l.member.id {
		t.Fatalf("the promotion was not adopted: %s", recorded())
	}
	// The fenced old master powers back on still believing it is master, at the generation
	// it was fenced at. It is not newer, so it changes nothing, and it is restricted.
	l.master.inv.Managed.Restricted = &protocol.PoolRestrictedStatus{Reason: "this host's pool authority was superseded by another master; it starts no shared-storage guest and writes no pool authority until it has rejoined and reconciled", Since: time.Now().UTC(), ObservedAt: time.Now().UTC()}
	for i := 0; i < 3; i++ {
		l.send(&l.master, nil)
		if recorded() != l.member.id {
			t.Fatalf("a stale old master flapped the pool row: %s", recorded())
		}
	}
	view := l.members(t)
	// The old master claims mastery at a strictly lower generation, which is not a rival
	// claim at all: the pool simply moved on without it.
	if view.MasterHostID != l.member.id || len(view.MasterConflict) != 0 {
		t.Fatalf("the promoted master was not the recorded one: %+v", view)
	}
	old := l.memberView(t, l.master.id)
	if old.Restricted == nil || !strings.Contains(old.Restricted.Reason, "superseded by another master") {
		t.Fatalf("the restricted old master was not projected with its reason: %+v", old.Restricted)
	}
}

// TestPromotionSurvivesAControllerReplacement: the Controller is a relay. Reopened from the
// same database mid-promotion it knows nothing, and re-projects the promotion from the
// hosts' next reports.
func TestPromotionSurvivesAControllerReplacement(t *testing.T) {
	l := newPromotionLab(t)
	read(t, l.a.call("POST", l.promotePath(), map[string]any{"request_id": "survive-1", "host_id": l.member.id, "confirmation": l.poolName(t), "fence_confirmed": true, "fence_host_ids": []string{l.master.id}}, nil, 202), &promotionAcceptance{})
	l.member.inv.Managed.Promotion = &protocol.PoolPromotionStatus{OperationID: "op-1", Stage: "activate", Status: "pending", ObservedAt: time.Now().UTC()}
	l.send(&l.member, nil)
	cfg := l.a.c.cfg
	if err := l.a.c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l.a.c = reopened
	if view := l.members(t); view.Promotion != nil {
		t.Fatalf("a reopened Controller claimed to know the promotion: %+v", view.Promotion)
	}
	l.send(&l.member, nil)
	view := l.members(t)
	if view.Promotion == nil || view.Promotion.Stage != "activate" || view.Promotion.HostID != l.member.id {
		t.Fatalf("the reopened Controller did not re-project the promotion: %+v", view.Promotion)
	}
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].Kind != "pool-promote" {
		t.Fatalf("the durable command did not survive the replacement: %+v", commands)
	}
	var rows int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE kind='pool-promote'").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("expected exactly one stored promotion row, got %d (%v)", rows, err)
	}
}

// TestPromotionRoutesRequireASession is the boundary check every mutation gets.
func TestPromotionRoutesRequireASession(t *testing.T) {
	l := newPromotionLab(t)
	for _, route := range []struct{ method, path string }{
		{"POST", l.preflightPath()},
		{"POST", l.promotePath()},
		{"POST", l.fencePath(l.master.id)},
	} {
		w := callRaw(&testClient{c: l.a.c, csrf: "", cookie: l.a.cookie}, route.method, route.path, map[string]any{"request_id": "x", "host_id": l.member.id})
		if w.Code != 403 && w.Code != 401 {
			t.Fatalf("%s %s without CSRF: %d", route.method, route.path, w.Code)
		}
	}
}
