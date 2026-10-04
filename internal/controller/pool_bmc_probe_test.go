// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func (l *joinLab) bmcProbePath(host string) string {
	return "/api/v1/pools/" + l.master.pool + "/members/" + host + "/bmc-probe"
}

// probeBMC runs one Detect while the master plays Core: it redeems the credential offer
// when one is made, then answers the brokered request on the report after the one that
// carried it.
func (l *joinLab) probeBMC(host string, body any, delivered *protocol.StorageCredentials, answer func(protocol.PoolRequest) protocol.PoolResult) *httptest.ResponseRecorder {
	l.t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- callRaw(l.a, "POST", l.bmcProbePath(host), body) }()
	deadline := time.Now().Add(time.Minute)
	for answer != nil && time.Now().Before(deadline) {
		select {
		case w := <-done:
			return w
		default:
		}
		response := l.send(&l.master, nil)
		if delivered != nil && len(response.CredentialOffers) == 1 {
			offer := response.CredentialOffers[0]
			if !strings.Contains(offer.RegistrationRequestID, ":bmc_probe:") {
				l.t.Fatalf("probe credential did not have a transient purpose: %q", offer.RegistrationRequestID)
			}
			var handoff protocol.StorageCredentialHandoff
			read(l.t, l.a.call("GET", "/api/v1/agent/storage/credentials/"+offer.Handle, nil, l.master.cert, 200), &handoff)
			*delivered = handoff.Credentials
			receipt := protocol.StorageCredentialReceipt{Receipt: labFenceReference, RequestID: offer.RequestID, RegistrationRequestID: offer.RegistrationRequestID, PoolID: offer.PoolID, HostID: offer.HostID, Status: "ready"}
			l.a.call("POST", "/api/v1/agent/storage/credentials/"+offer.Handle+"/ack", receipt, l.master.cert, 200)
		}
		if len(response.PoolRequests) > 0 {
			l.send(&l.master, func(r *protocol.ReportRequest) {
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
	case <-time.After(time.Minute):
		l.t.Fatal("bmc probe never returned")
	}
	return nil
}

type bmcProbeResponse struct {
	HostID   string                       `json:"host_id"`
	Status   string                       `json:"status"`
	Error    string                       `json:"error"`
	BMCProbe *protocol.PoolBMCProbeResult `json:"bmc_probe"`
}

func labProbeAnswer(request protocol.PoolRequest) protocol.PoolResult {
	return protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "succeeded", ObservedAt: time.Now().UTC(),
		BMCProbe: &protocol.PoolBMCProbeResult{
			Methods: []protocol.PoolBMCProbeMethod{
				{Kind: protocol.FenceRedfish, Reachable: true, TargetIDs: []string{"System.Embedded.1"}},
				{Kind: protocol.FenceIPMI, Detail: "no answer on UDP 623"},
			},
			Recommended: &protocol.PoolBMCProbeChoice{Kind: protocol.FenceRedfish, TargetID: "System.Embedded.1", Reason: "Redfish answered and manages this host"},
		}}
}

// Detect is one relayed question to the pool master. The password reaches Core over the
// one-time handoff and nowhere else, and nothing about the probe is journalled, audited,
// queued or stored: a replacement Controller has nothing to recover from it.
func TestBMCProbeIsRelayedToTheMasterAndStoresNothing(t *testing.T) {
	l := newPromotionLab(t)
	const secret = "lab-bmc-password"
	var delivered protocol.StorageCredentials
	body := map[string]any{"request_id": "detect-1", "endpoint": "bmc.example.test", "username": "operator", "credential": map[string]any{"secret": secret}}
	var auditsBefore int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM audit").Scan(&auditsBefore); err != nil {
		t.Fatal(err)
	}
	var got bmcProbeResponse
	read(t, l.probeBMC(l.member.id, body, &delivered, func(request protocol.PoolRequest) protocol.PoolResult {
		if request.Action != "bmc-probe" || request.RequestID != "detect-1" || request.HostID != l.master.id || request.MemberHostID != l.member.id ||
			request.Endpoint != "bmc.example.test" || request.Username != "operator" || request.CredentialRef != labFenceReference || request.Valid() != nil {
			t.Errorf("unexpected pool request: %+v", request)
		}
		return labProbeAnswer(request)
	}), &got)
	if delivered.Secret != secret {
		t.Fatalf("Core did not receive the password over the handoff: %+v", delivered)
	}
	if got.HostID != l.member.id || got.Status != "succeeded" || got.Error != "" || got.BMCProbe == nil || got.BMCProbe.Recommended == nil ||
		got.BMCProbe.Recommended.TargetID != "System.Embedded.1" || len(got.BMCProbe.Methods) != 2 {
		t.Fatalf("the master's answer was not relayed verbatim: %+v", got)
	}
	var rows, audits int
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("a probe stored %d managed rows (%v)", rows, err)
	}
	if err := l.a.c.db.QueryRow("SELECT count(*) FROM audit").Scan(&audits); err != nil || audits != auditsBefore {
		t.Fatalf("a probe wrote %d audit rows (%v)", audits-auditsBefore, err)
	}
	var dump string
	if err := l.a.c.db.QueryRow("SELECT coalesce(group_concat(action||resource_id),'') FROM audit").Scan(&dump); err != nil || strings.Contains(dump, secret) {
		t.Fatalf("the password reached the audit trail: %s (%v)", dump, err)
	}
	if commands := l.send(&l.master, nil).Commands; len(commands) != 0 {
		t.Fatalf("a probe queued a command: %+v", commands)
	}
}

// Core's refusal is an answer too: its sentence is shown verbatim, with whatever it did learn
// about which methods answered. An answer that does not validate is discarded as if it had
// not arrived, and the deadline says who did not finish.
func TestBMCProbeRelaysCoreRefusalsAndDiscardsMalformedAnswers(t *testing.T) {
	l := newPromotionLab(t)
	refusal := "neither Redfish nor IPMI answered at bmc.example.test"
	var got bmcProbeResponse
	read(t, l.probeBMC(l.member.id, map[string]any{"request_id": "detect-2", "endpoint": "bmc.example.test"}, nil, func(request protocol.PoolRequest) protocol.PoolResult {
		if request.Username != "" || request.CredentialRef != "" {
			t.Errorf("an anonymous probe named an account: %+v", request)
		}
		return protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "failed", Error: refusal, ObservedAt: time.Now().UTC(),
			BMCProbe: &protocol.PoolBMCProbeResult{Methods: []protocol.PoolBMCProbeMethod{{Kind: protocol.FenceRedfish, Detail: "connection refused"}}}}
	}), &got)
	if got.Status != "failed" || got.Error != refusal || got.BMCProbe == nil || len(got.BMCProbe.Methods) != 1 {
		t.Fatalf("Core's refusal was not relayed with its methods: %+v", got)
	}

	previous := poolBMCProbeWait
	poolBMCProbeWait = 300 * time.Millisecond
	t.Cleanup(func() { poolBMCProbeWait = previous })
	w := l.probeBMC(l.member.id, map[string]any{"request_id": "detect-3", "endpoint": "bmc.example.test"}, nil, func(request protocol.PoolRequest) protocol.PoolResult {
		answer := labProbeAnswer(request)
		answer.BMCProbe.Recommended.TargetID = "../escape"
		return answer
	})
	if w.Code != 504 || !strings.Contains(w.Body.String(), "the pool master did not finish probing the management controller before the deadline") {
		t.Fatalf("a malformed answer was not discarded: %d %s", w.Code, w.Body.String())
	}
}

// The probe takes the fence profile's gates: a stable request, an address, an account only
// with its password, a fresh capable master and a host that is a member of this pool.
func TestBMCProbeRefusals(t *testing.T) {
	l := newPromotionLab(t)
	path := l.bmcProbePath(l.member.id)
	for _, body := range []map[string]any{
		{"endpoint": "bmc.example.test"},
		{"request_id": "r1"},
		{"request_id": "r2", "endpoint": "https://bmc.example.test/redfish"},
		{"request_id": "r3", "endpoint": "bmc.example.test", "username": "operator"},
		{"request_id": "r4", "endpoint": "bmc.example.test", "credential": map[string]any{"secret": "x"}},
		{"request_id": "r5", "endpoint": "bmc.example.test", "username": "operator", "credential": map[string]any{"secret": "padded "}},
		{"request_id": "r6", "endpoint": "bmc.example.test", "kind": "redfish"},
	} {
		l.a.call("POST", path, body, nil, 400)
	}
	anonymous := map[string]any{"request_id": "r7", "endpoint": "bmc.example.test"}
	l.a.call("POST", "/api/v1/pools/no-such-pool/members/"+l.member.id+"/bmc-probe", anonymous, nil, 404)
	l.a.call("POST", l.bmcProbePath("no-such-host"), anonymous, nil, 404)
	l.forget(&l.master)
	if body := l.a.call("POST", path, anonymous, nil, 409).Body.String(); !strings.Contains(body, "must be fresh and active") {
		t.Fatalf("a quiet master was not refused: %s", body)
	}
}

func TestPoolResultValidKeepsEachAnswerToItsAction(t *testing.T) {
	now := time.Now().UTC()
	probe := labProbeAnswer(protocol.PoolRequest{RequestID: "r", PoolID: "p", HostID: "h"}).BMCProbe
	bad := &protocol.PoolBMCProbeResult{Methods: []protocol.PoolBMCProbeMethod{{Kind: "virsh"}}}
	for _, c := range []struct {
		name   string
		action string
		result protocol.PoolResult
		want   bool
	}{
		{"probe answer", "bmc-probe", protocol.PoolResult{Status: "succeeded", ObservedAt: now, BMCProbe: probe}, true},
		{"probe without answer", "bmc-probe", protocol.PoolResult{Status: "succeeded", ObservedAt: now}, false},
		{"malformed probe answer", "bmc-probe", protocol.PoolResult{Status: "succeeded", ObservedAt: now, BMCProbe: bad}, false},
		{"probe with a verdict", "bmc-probe", protocol.PoolResult{Status: "succeeded", ObservedAt: now, BMCProbe: probe, Promotion: &protocol.PoolPromotionVerdict{}}, false},
		{"refused probe with methods", "bmc-probe", protocol.PoolResult{Status: "failed", Error: "no", ObservedAt: now, BMCProbe: probe}, true},
		{"refused probe bare", "bmc-probe", protocol.PoolResult{Status: "failed", Error: "no", ObservedAt: now}, true},
		{"refused probe malformed", "bmc-probe", protocol.PoolResult{Status: "failed", ObservedAt: now, BMCProbe: bad}, false},
		{"probe answer to an invite", "invite", protocol.PoolResult{Status: "failed", ObservedAt: now, BMCProbe: probe}, false},
		{"probe answer to a preflight", "promotion-preflight", protocol.PoolResult{Status: "succeeded", ObservedAt: now, HostID: "h", BMCProbe: probe, Promotion: &protocol.PoolPromotionVerdict{CandidateHostID: "h"}}, false},
	} {
		if got := poolResultValid(c.action, c.result); got != c.want {
			t.Errorf("%s: poolResultValid = %v, want %v", c.name, got, c.want)
		}
	}
}
