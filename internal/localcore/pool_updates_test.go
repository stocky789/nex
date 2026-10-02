// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	if code, body := post(s, base+"/rollouts/r/control", map[string]any{"action": "pause"}, cookie, csrf); code != http.StatusBadRequest || body["code"] != "invalid_request" {
		t.Fatalf("a control without a request_id = %d %v", code, body)
	}
	if code, body := post(s, base+"/rollouts/r/control", map[string]any{"request_id": strings.Repeat("c", 129), "action": "pause"}, cookie, csrf); code != http.StatusBadRequest || body["code"] != "invalid_request" {
		t.Fatalf("a control with a request_id over 128 characters = %d %v", code, body)
	}
}

// Reading one rollout is as much the master's alone as listing: a member that kept a record from
// an earlier time as master must not report it as the pool's.
func TestPoolRolloutReadIsRefusedOnAMember(t *testing.T) {
	s, _ := testService(t)
	const base = "/api/local/v1/pool/updates"
	cookie, csrf := loginLocal(t, s)
	enrolledPoolMaster(t, s)
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	a.store.State.HostID = "member-2" // the pool's master is master-1
	for _, path := range []string{base, base + "/rollouts/r"} {
		w := callLocal(s, http.MethodGet, path, nil, cookie, csrf, "https://core.test")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not_pool_master") {
			t.Fatalf("GET %s on a member = %d %s", path, w.Code, w.Body)
		}
	}
}

// A control is not tied to the client that sent it: abort restarts guests and clears maintenance, and a
// disconnect halfway must not leave that undone.
func TestPoolRolloutControlOutlivesTheClient(t *testing.T) {
	request, cancelClient := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(request)
	ctx, cancel := poolRolloutControlContext(r)
	defer cancel()
	cancelClient()
	if ctx.Err() != nil {
		t.Fatalf("the control was cancelled with the client: %v", ctx.Err())
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("a control that outlives the client has no bound")
	}
}

// force reaches the coordinator through the control route: it is a known field, it only ends an
// abort that is still putting things back, and it means nothing with another control.
func TestPoolRolloutControlCarriesForce(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	enrolledPoolMaster(t, s)
	record := `{"id":"roll-1","pool_id":"pool-1","sequence":2,"running_vms":"refuse","state":"running","phase":"hosts","membership_generation":1,"hosts":[],"created_at":"2026-10-02T09:00:00Z","updated_at":"2026-10-02T09:00:00Z"}`
	if err := os.WriteFile(filepath.Join(s.cfg.AgentStateDir, "pool-update.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	const path = "/api/local/v1/pool/updates/rollouts/roll-1/control"
	if code, body := post(s, path, map[string]any{"request_id": "c1", "action": "abort", "force": true}, cookie, csrf); code != http.StatusConflict || body["code"] != "abort_not_pending" {
		t.Fatalf("force on a rollout that is not being aborted = %d %v", code, body)
	}
	if code, body := post(s, path, map[string]any{"request_id": "c2", "action": "pause", "force": true}, cookie, csrf); code != http.StatusBadRequest || body["code"] != "invalid_request" {
		t.Fatalf("force with pause = %d %v", code, body)
	}
}
