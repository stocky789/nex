// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"net/http"
	"testing"

	"nex.local/nex/internal/agent"
)

func enrolledPoolMaster(t *testing.T, s *Service) {
	t.Helper()
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	a.store.State.HostID, a.store.State.PoolID = "master-1", "pool-1"
	if err = agent.EnsurePoolMembership(a.store); err != nil {
		t.Fatal(err)
	}
}

// The master-only rollout routes need a root session and CSRF, validate their input and answer a
// refusal with its stable code; the host here has no image engine, so a dry run is refused by it.
func TestPoolRolloutRoutes(t *testing.T) {
	s, _ := testService(t)
	const base = "/api/local/v1/pool/updates"
	if w := callLocal(s, http.MethodGet, base, nil, "", "", "https://core.test"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated read = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPost, base+"/rollouts", map[string]any{"request_id": "r", "sequence": 2}, "", "", "https://core.test"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated start = %d", w.Code)
	}
	cookie, csrf := loginLocal(t, s)
	if w := callLocal(s, http.MethodPost, base+"/rollouts", map[string]any{"request_id": "r", "sequence": 2}, cookie, "", "https://core.test"); w.Code != http.StatusForbidden {
		t.Fatalf("start without CSRF = %d", w.Code)
	}
	if w := callLocal(s, http.MethodGet, base, nil, cookie, csrf, "https://core.test"); w.Code != http.StatusConflict {
		t.Fatalf("an unenrolled host read a rollout: %d %s", w.Code, w.Body)
	}
	enrolledPoolMaster(t, s)
	w := callLocal(s, http.MethodGet, base, nil, cookie, csrf, "https://core.test")
	if w.Code != http.StatusOK || w.Body.String() != "{\"rollout\":null}\n" {
		t.Fatalf("empty rollout read = %d %s", w.Code, w.Body)
	}
	if code, body := post(s, base+"/rollouts", map[string]any{"request_id": "r", "sequence": 0}, cookie, csrf); code != http.StatusBadRequest || body["code"] != "invalid_request" {
		t.Fatalf("a rollout without a sequence = %d %v", code, body)
	}
	if code, _ := post(s, base+"/rollouts", map[string]any{"request_id": "r", "sequence": 2, "surprise": true}, cookie, csrf); code != http.StatusBadRequest {
		t.Fatalf("an unknown field = %d", code)
	}
	if code, body := post(s, base+"/rollouts", map[string]any{"request_id": "r", "sequence": 2, "running_vms": "evacuate", "dry_run": true}, cookie, csrf); code != http.StatusConflict || body["code"] != "evacuation_unavailable" {
		t.Fatalf("evacuate = %d %v", code, body)
	}
	if code, body := post(s, base+"/rollouts", map[string]any{"request_id": "r", "sequence": 2, "dry_run": true}, cookie, csrf); code != http.StatusConflict || body["code"] != "unsupported_edition" {
		t.Fatalf("dry run on a host without the image engine = %d %v", code, body)
	}
	if w = callLocal(s, http.MethodGet, base+"/rollouts/r", nil, cookie, csrf, "https://core.test"); w.Code != http.StatusNotFound {
		t.Fatalf("an unknown rollout = %d", w.Code)
	}
	if code, body := post(s, base+"/rollouts/r/control", map[string]any{"request_id": "c", "action": "pause"}, cookie, csrf); code != http.StatusNotFound || body["code"] != "rollout_not_found" {
		t.Fatalf("control of an unknown rollout = %d %v", code, body)
	}
	if code, body := post(s, base+"/rollouts/r/control", map[string]any{"request_id": "c", "action": "explode"}, cookie, csrf); code != http.StatusBadRequest || body["code"] != "invalid_request" {
		t.Fatalf("an unknown control = %d %v", code, body)
	}
}
