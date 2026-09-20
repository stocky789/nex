// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

// A promotion powers hosts off, and the hosts it powers off are named by Core's preflight
// and confirmed by the operator. That consent travels as one list across three layers, and
// each layer passed its own suite while the whole flow could not work: Core began refusing
// every promotion command without `fence_host_ids` while the Controller still composed one
// carrying `fence_confirmed` alone, and the Controller's own tests stayed green because
// they answer with a fake Core. This check crosses the layers on purpose — the exact
// protocol.Command the Controller queued is handed to the real agent entry point that
// enforces the consent — so that drift cannot return silently.

// poolPromotionFenceConsent is Core's own refusal of a promotion command that names no
// hosts (internal/agent/pool_promote.go). It is written out here rather than imported
// because this check exists to notice when the two sides stop agreeing.
const poolPromotionFenceConsent = "this promotion will power off other hosts; confirm the hosts this pool reports before it can run"

// coreStore is a real agent state directory, so the command is executed by the entry point
// Core's command loop uses rather than by a re-implementation of its rules.
func coreStore(t *testing.T, host string) *agent.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := agent.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.State.HostID = host
	return s
}

func TestPromotionFenceConsentReachesCoreEndToEnd(t *testing.T) {
	l := newPromotionLab(t)
	// The old master is the host that died: it is one of the hosts the promotion powers
	// off, together with a partitioned host this Controller has never heard of.
	fenced := []string{"core-ghost", l.master.id}
	slices.Sort(fenced)
	l.forget(&l.master)

	var verdict preflightResponse
	read(t, l.preflight(&l.member, map[string]string{"host_id": l.member.id}, func(request protocol.PoolRequest) protocol.PoolResult {
		result := labVerdict(request, false)
		result.Promotion.OldMasterHostID = l.master.id
		result.Promotion.ReadyToFence = true
		result.Promotion.FenceHostIDs = fenced
		return result
	}), &verdict)
	if verdict.Promotion == nil || !slices.Equal(verdict.Promotion.FenceHostIDs, fenced) {
		t.Fatalf("the preflight verdict did not reach the operator with the hosts it would power off: %+v", verdict.Promotion)
	}

	// The operator confirms what the preflight named. The order is the caller's, so it is
	// deliberately reversed here: the list is a set, and a replay of the same confirmation
	// must not depend on how it was typed.
	confirmed := append([]string(nil), verdict.Promotion.FenceHostIDs...)
	slices.Reverse(confirmed)
	body := map[string]any{"request_id": "promote-consent", "host_id": l.member.id, "confirmation": l.poolName(t), "fence_confirmed": true, "fence_host_ids": confirmed}
	var accepted promotionAcceptance
	read(t, l.a.call("POST", l.promotePath(), body, nil, 202), &accepted)
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].Kind != "pool-promote" || commands[0].Pool == nil {
		t.Fatalf("the candidate did not receive the promotion: %+v", commands)
	}
	cmd := commands[0]
	if err := cmd.Pool.Valid(); err != nil {
		t.Fatalf("the queued command is not a valid pool command: %v", err)
	}
	if !cmd.Pool.FenceConfirmed || !slices.Equal(cmd.Pool.FenceHostIDs, fenced) {
		t.Fatalf("the command must carry the confirmed hosts, sorted: %+v", cmd.Pool)
	}
	// An exact retry, typed in the other order again, is the same operation: the payload
	// is normalised before it is recorded.
	var replay promotionAcceptance
	read(t, l.a.call("POST", l.promotePath(), map[string]any{"request_id": "promote-consent", "host_id": l.member.id, "confirmation": l.poolName(t), "fence_confirmed": true, "fence_host_ids": fenced}, nil, 200), &replay)
	if replay.Operation.ID != accepted.Operation.ID {
		t.Fatalf("the same confirmation in another order made a second operation: %+v", replay)
	}

	// The real Core entry point. It must not refuse this command for the consent it does
	// carry; it refuses the same command with the list removed, which is what proves this
	// check is watching the gate it claims to watch.
	// The scratch state directory holds no pool, so Core gets past the consent gate and
	// refuses further in ("pool membership is unavailable"). That is the assertion: the
	// consent is accepted, and what stops this command is the empty lab, not the contract.
	if result := agent.ExecutePoolPromotion(context.Background(), coreStore(t, l.member.id), cmd); result.Error == poolPromotionFenceConsent {
		t.Fatalf("Core refused the Controller's own promotion command for missing fence consent: %+v", result)
	}
	stripped, pool := cmd, *cmd.Pool
	pool.FenceHostIDs = nil
	stripped.Pool = &pool
	if result := agent.ExecutePoolPromotion(context.Background(), coreStore(t, l.member.id), stripped); result.Error != poolPromotionFenceConsent {
		t.Fatalf("Core's fence-consent gate is not where this check expects it: %+v", result)
	}
}

// The Controller never composes, completes or corrects the list: it is the operator's
// confirmation of what Core named, and Core re-computes it at execution time.
func TestPromoteFenceHostRefusals(t *testing.T) {
	l := newPromotionLab(t)
	name := l.poolName(t)
	body := func(hosts any, id string) map[string]any {
		out := map[string]any{"request_id": id, "host_id": l.member.id, "confirmation": name, "fence_confirmed": true}
		if hosts != nil {
			out["fence_host_ids"] = hosts
		}
		return out
	}
	for _, refusal := range []struct {
		name  string
		hosts any
		id    string
	}{
		{"absent", nil, "no-list"},
		{"empty", []string{}, "empty-list"},
		{"duplicated", []string{l.master.id, l.master.id}, "duplicate"},
		{"blank", []string{""}, "blank"},
	} {
		got := l.a.call("POST", l.promotePath(), body(refusal.hosts, refusal.id), nil, 400).Body.String()
		if !strings.Contains(got, "preflight") {
			t.Fatalf("the %s list was not refused with a pointer to the preflight: %s", refusal.name, got)
		}
	}
	// A host this Controller mirrors in another pool cannot be one this pool would fence.
	// A host it has never heard of is accepted: the mirror is not the pool's record, and
	// Core re-computes the whole list before it powers anything off.
	if _, err := l.a.c.db.Exec("INSERT INTO pools(id,site_id,name,master_host_id) SELECT 'other-pool',site_id,'Other','core-other' FROM pools WHERE id=?", l.master.pool); err != nil {
		t.Fatal(err)
	}
	if _, err := l.a.c.db.Exec("INSERT INTO hosts(id,pool_id,hostname) VALUES('core-other','other-pool','core-other')"); err != nil {
		t.Fatal(err)
	}
	other := l.a.call("POST", l.promotePath(), body([]string{l.master.id, "core-other"}, "other-pool-host"), nil, 400).Body.String()
	if !strings.Contains(other, "core-other") {
		t.Fatalf("a host of another pool was not refused by name: %s", other)
	}
	l.a.call("POST", l.promotePath(), body([]string{l.master.id, "core-ghost"}, "unknown-host"), nil, 202)
}
