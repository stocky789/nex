// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"errors"
	"nex.local/nex/internal/protocol"
	"slices"
	"time"
)

type observationReceipt struct {
	sample   time.Time
	received time.Time
}

// Called with mu held; wall-clock timestamps never refresh a repeated sample.
func (c *Controller) observationFreshness(key string) string {
	v, ok := c.observed[key]
	if !ok {
		return "unknown"
	}
	if time.Since(v.received) > c.cfg.StaleAfter {
		return "stale"
	}
	return "fresh"
}
func validateInventory(v *protocol.Inventory) error {
	if v == nil {
		return nil
	}
	if v.CPUCount < 0 || v.CPUCount > 65536 || len(v.CPUModels) > 256 || len(v.NICs) > 1024 || len(v.Disks) > 4096 || len(v.VMs) > 10000 || len(v.Checks) > 256 || len(v.Virtualization) > 1024 || len(v.Hostname) > 200 || len(v.CPUModel) > 4096 || len(v.OSVersion) > 4096 || len(v.AgentVersion) > 200 {
		return errors.New("inventory limits exceeded")
	}
	for _, model := range v.CPUModels {
		if !protocol.ValidCPUModel(model) {
			return errors.New("invalid CPU model")
		}
	}
	names := map[string]bool{}
	for _, check := range v.Checks {
		if !nameValid(check.Name) || names[check.Name] || len(check.Error) > 4096 {
			return errors.New("invalid or duplicate health check")
		}
		names[check.Name] = true
		switch check.Status {
		case "healthy", "degraded", "failed", "unknown", "unsupported":
		default:
			return errors.New("invalid health status")
		}
	}
	for _, nic := range v.NICs {
		if !nameValid(nic.Name) || len(nic.Addresses) > 128 || len(nic.Kind) > 20 || len(nic.Master) > 200 || len(nic.Parent) > 200 {
			return errors.New("invalid NIC")
		}
	}
	if m := v.Managed; m != nil {
		if len(m.Storage) > 10000 || len(m.Networks) > 10000 || len(m.Media) > 10000 || len(m.VMs) > 10000 {
			return errors.New("managed state limits exceeded")
		}
		var fields []string
		for _, s := range m.Storage {
			fields = append(fields, s.ID, s.Name)
		}
		for _, n := range m.Networks {
			fields = append(fields, n.ID, n.Name)
		}
		for _, md := range m.Media {
			fields = append(fields, md.ID, md.Name)
		}
		for _, vm := range m.VMs {
			fields = append(fields, vm.ID, vm.Name)
		}
		for _, f := range fields {
			if !nameValid(f) {
				return errors.New("invalid managed entry")
			}
		}
	}
	for _, disk := range v.Disks {
		if !nameValid(disk.Name) {
			return errors.New("invalid disk")
		}
	}
	for _, vm := range v.VMs {
		if !nameValid(vm.ID) || !nameValid(vm.Name) || len(vm.State) > 100 {
			return errors.New("invalid VM")
		}
		if g := vm.Guest; g != nil {
			if !slices.Contains([]string{"connected", "unresponsive", "disconnected", "no-channel"}, g.Agent) ||
				!slices.Contains([]string{"ready", "waiting", "no-channel"}, g.Clipboard) ||
				len(g.OS) > 4096 || len(g.Hostname) > 200 || len(g.Addresses) > 32 {
				return errors.New("invalid VM")
			}
			for _, a := range g.Addresses {
				if len(a.Interface) > 200 || len(a.Address) > 100 {
					return errors.New("invalid VM")
				}
			}
		}
	}
	return nil
}
func mergeInventory(old, incoming *protocol.Inventory) *protocol.Inventory {
	if old == nil {
		return incoming
	}
	out := *old
	if !incoming.ObservedAt.IsZero() && !incoming.ObservedAt.Equal(old.ObservedAt) {
		out = *incoming
	}
	previous := map[string]protocol.Check{}
	next := map[string]protocol.Check{}
	for _, check := range old.Checks {
		previous[check.Name] = check
	}
	for _, check := range incoming.Checks {
		next[check.Name] = check
	}
	newer := func(name string) bool {
		n, ok := next[name]
		o, exists := previous[name]
		return ok && !n.ObservedAt.IsZero() && (!exists || !n.ObservedAt.Equal(o.ObservedAt))
	}
	out.Managed = incoming.Managed
	out.VMs = old.VMs
	if newer("libvirt") {
		out.VMs = incoming.VMs
	}
	disks := []protocol.Disk{}
	for _, disk := range old.Disks {
		kind := "block-disks"
		if disk.AvailableBytes != nil {
			kind = "disk"
		}
		if !newer(kind) {
			disks = append(disks, disk)
		}
	}
	for _, disk := range incoming.Disks {
		kind := "block-disks"
		if disk.AvailableBytes != nil {
			kind = "disk"
		}
		if newer(kind) {
			disks = append(disks, disk)
		}
	}
	out.Disks = disks
	checks := append([]protocol.Check(nil), old.Checks...)
	for _, check := range incoming.Checks {
		found := false
		for i := range checks {
			if checks[i].Name == check.Name {
				found = true
				if !check.ObservedAt.Equal(checks[i].ObservedAt) || (check.ObservedAt.Equal(checks[i].ObservedAt) && check.Status != "healthy") {
					checks[i] = check
				}
				break
			}
		}
		if !found {
			checks = append(checks, check)
		}
	}
	out.Checks = checks
	return &out
}

type pendingHealth struct {
	status, detail string
	since          time.Time
}

func (c *Controller) monitorHealth() {
	defer close(c.stopped)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	pending := map[string]pendingHealth{}
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.healthTick(pending)
		}
	}
}
func (c *Controller) healthTick(pending map[string]pendingHealth) {
	rows, err := c.db.Query("SELECT id,revoked,inventory FROM hosts")
	if err != nil {
		return
	}
	current := map[string]pendingHealth{}
	for rows.Next() {
		var id string
		var revoked bool
		var raw sql.NullString
		if rows.Scan(&id, &revoked, &raw) != nil {
			rows.Close()
			return
		}
		c.mu.Lock()
		connection := "stale"
		if seen, ok := c.seen[id]; ok && time.Since(seen) <= c.cfg.StaleAfter {
			connection = "healthy"
		}
		if revoked {
			connection = "revoked"
		}
		current[id+"/connection"] = pendingHealth{status: connection, detail: "Management connection " + connection}
		if raw.Valid && !revoked {
			var inv protocol.Inventory
			if json.Unmarshal([]byte(raw.String), &inv) == nil {
				for _, check := range inv.Checks {
					status := check.Status
					freshness := c.observationFreshness(id + "/" + check.Name)
					if freshness != "fresh" {
						status = freshness
					}
					current[id+"/"+check.Name] = pendingHealth{status: status, detail: check.Error}
				}
			}
		}
		c.mu.Unlock()
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	now := time.Now()
	for key, state := range current {
		p, ok := pending[key]
		if !ok || p.status != state.status || p.detail != state.detail {
			state.since = now
			pending[key] = state
			continue
		}
		if now.Sub(p.since) < c.cfg.AlertDebounce {
			continue
		}
		tx, err := c.db.Begin()
		if err != nil {
			continue
		}
		var status, detail string
		err = tx.QueryRow("SELECT status,detail FROM health_states WHERE resource_id=?", key).Scan(&status, &detail)
		if err != nil && err != sql.ErrNoRows {
			tx.Rollback()
			continue
		}
		if err == nil && status == p.status && detail == p.detail {
			tx.Rollback()
			continue
		}
		_, err = tx.Exec("INSERT INTO health_states VALUES(?,?,?) ON CONFLICT(resource_id) DO UPDATE SET status=excluded.status,detail=excluded.detail", key, p.status, p.detail)
		if err == nil {
			_, err = tx.Exec("INSERT INTO audit(actor,action,resource_id,created_at,detail) VALUES(?,?,?,?,?)", "monitor", "health."+p.status, key, stamp(), p.detail)
		}
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}
	for key := range pending {
		if _, ok := current[key]; !ok {
			delete(pending, key)
		}
	}
}
