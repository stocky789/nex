// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"encoding/json"
	"testing"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

// The host-local pool view carries this Core's own restriction, so an operator at the host
// console can read why nothing starts when no Controller answers. The reason and conflicts
// themselves come from the same agent.PoolRestrictedReport the Controller inventory reports,
// which the agent tests cover against a genuinely restricted host.
func TestLocalPoolViewCarriesTheRestriction(t *testing.T) {
	s, cfg := testService(t)
	member := poolTestStore(t, cfg.AgentStateDir, "member-1", "member-pool")
	if err := agent.EnsurePoolMembership(member); err != nil {
		t.Fatal(err)
	}
	// A single-host pool has nobody who could have promoted anything, so it is never
	// restricted and its guests keep starting.
	if err := agent.EnsurePoolBootRestriction(member); err != nil {
		t.Fatal(err)
	}
	if status := agent.PoolRestrictedReport(member); status != nil {
		t.Fatalf("the master of a single-host pool reported a restriction: %#v", status)
	}
	if err := member.Close(); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := loginLocal(t, s)
	w := callLocal(s, "GET", "/api/local/v1/pool", nil, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("pool view = %d %s", w.Code, w.Body)
	}
	var view map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	raw, ok := view["restricted"]
	if !ok {
		t.Fatalf("the host-local pool view does not report the restriction: %s", w.Body)
	}
	var restricted *protocol.PoolRestrictedStatus
	if err := json.Unmarshal(raw, &restricted); err != nil {
		t.Fatal(err)
	}
	if restricted != nil {
		t.Fatalf("an unrestricted host reported %#v", restricted)
	}
}
