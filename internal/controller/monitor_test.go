// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"nex.local/nex/internal/protocol"
	"os"
	"testing"
	"time"
)

func TestMergeIndependentProbeAndTimeout(t *testing.T) {
	now := time.Now()
	old := &protocol.Inventory{ObservedAt: now, CPUCount: 2, VMs: []protocol.VM{{ID: "vm", Name: "vm", State: "running"}}, Checks: []protocol.Check{{Name: "inventory", Status: "healthy", ObservedAt: now}, {Name: "libvirt", Status: "healthy", ObservedAt: now}}}
	incoming := &protocol.Inventory{ObservedAt: now, CPUCount: 99, VMs: []protocol.VM{{ID: "vm", Name: "vm", State: "paused"}}, Checks: []protocol.Check{{Name: "inventory", Status: "unknown", ObservedAt: now, Error: "probe deadline exceeded"}, {Name: "libvirt", Status: "healthy", ObservedAt: now.Add(time.Second)}}}
	got := mergeInventory(old, incoming)
	if got.CPUCount != 2 || got.VMs[0].State != "paused" || got.Checks[0].Status != "unknown" {
		t.Fatalf("independent probes lost: %+v", got)
	}
	incoming.Checks[1].ObservedAt = now.Add(-time.Second)
	incoming.VMs = []protocol.VM{{ID: "vm", Name: "vm", State: "stopped"}}
	got = mergeInventory(got, incoming)
	if got.VMs[0].State != "stopped" {
		t.Fatal("new sequenced sample rejected after host clock correction")
	}
	startup := &protocol.Inventory{Checks: []protocol.Check{{Name: "inventory", Status: "unknown"}, {Name: "libvirt", Status: "unknown"}}}
	restarted := mergeInventory(got, startup)
	if restarted.CPUCount != 2 || len(restarted.VMs) != 1 || restarted.VMs[0].State != "stopped" || restarted.Checks[1].Status != "unknown" {
		t.Fatalf("startup erased retained observations: %+v", restarted)
	}
	c := &Controller{cfg: Config{StaleAfter: time.Second}, observed: map[string]observationReceipt{"h/inventory": {sample: now, received: now.Add(-2 * time.Second)}}}
	if c.observationFreshness("h/inventory") != "stale" || c.observationFreshness("h/libvirt") != "unknown" {
		t.Fatal("old samples marked fresh")
	}
}
func TestHealthDebounceAndDurability(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := Open(Config{DataDir: dir, Username: "admin", Password: "long-test-password", AlertDebounce: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.db.Exec("INSERT INTO sites VALUES('s','Site'); INSERT INTO pools VALUES('p','s','Pool','h'); INSERT INTO hosts(id,pool_id,hostname) VALUES('h','p','Host')")
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]pendingHealth{}
	c.healthTick(pending)
	var count int
	c.db.QueryRow("SELECT count(*) FROM audit WHERE action='health.stale'").Scan(&count)
	if count != 0 {
		t.Fatal("alert emitted without debounce")
	}
	state := pending["h/connection"]
	state.since = time.Now().Add(-2 * time.Second)
	pending["h/connection"] = state
	c.healthTick(pending)
	c.healthTick(pending)
	c.db.QueryRow("SELECT count(*) FROM audit WHERE action='health.stale'").Scan(&count)
	if count != 1 {
		t.Fatalf("expected one durable transition, got %d", count)
	}
	c.mu.Lock()
	c.seen["h"] = time.Now()
	c.mu.Unlock()
	c.healthTick(pending)
	state = pending["h/connection"]
	state.since = time.Now().Add(-2 * time.Second)
	pending["h/connection"] = state
	c.healthTick(pending)
	c.db.QueryRow("SELECT count(*) FROM audit WHERE action='health.healthy'").Scan(&count)
	if count != 1 {
		t.Fatal("recovery transition missing")
	}
}
func TestPrivateStateAndSingleWriter(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := Open(Config{DataDir: dir, Username: "admin", Password: "long-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Open(Config{DataDir: dir}); err == nil {
		other.Close()
		t.Fatal("two controllers opened authoritative database")
	}
	if _, err = c.db.Exec("UPDATE schema_version SET version=999"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if other, err := Open(Config{DataDir: dir}); err == nil {
		other.Close()
		t.Fatal("unsupported schema accepted")
	}
}
