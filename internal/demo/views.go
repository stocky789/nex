// SPDX-License-Identifier: AGPL-3.0-only
package demo

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Server) readAPI(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		fail(w, 404, "demo API route not found")
		return
	}
	resource := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	parts := strings.Split(resource, "/")
	if len(parts) == 1 {
		if items, ok := s.fixtures.lists[resource]; ok {
			filtered := make([]row, 0, len(items))
			for _, item := range items {
				if q := r.URL.Query().Get("pool_id"); q != "" && item["pool_id"] != q {
					continue
				}
				if q := r.URL.Query().Get("host_id"); q != "" && item["host_id"] != q {
					continue
				}
				if q := r.URL.Query().Get("site_id"); q != "" && item["site_id"] != q {
					continue
				}
				filtered = append(filtered, item)
			}
			reply(w, 200, filtered)
			return
		}
		if resource == "updates" {
			reply(w, 200, row{"package": "nex-demo", "installed_version": "simulated", "update_available": false, "repo_url": "", "phase": "idle", "message": "Read-only simulated demo; updates are disabled."})
			return
		}
		if resource == "version" {
			reply(w, 200, row{"version": "simulated", "product": "Nex demo", "simulated": true})
			return
		}
	}
	if resource == "storage/cluster" {
		pool := r.URL.Query().Get("pool_id")
		if find(s.fixtures.lists["pools"], pool) == nil {
			fail(w, 404, "pool not found")
			return
		}
		reply(w, 200, row{"pool_id": pool, "status": "succeeded", "observed_at": s.now().UTC(), "operations": []row{}})
		return
	}
	if len(parts) >= 2 && (parts[0] == "hosts" || parts[0] == "vms") {
		item := find(s.fixtures.lists[parts[0]], parts[1])
		if item == nil {
			fail(w, 404, "resource not found")
			return
		}
		if len(parts) == 2 {
			reply(w, 200, item)
			return
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "stats":
				s.stats(w, r, parts[0], item)
				return
			case "snapshots":
				if parts[0] == "vms" {
					reply(w, 200, s.fixtures.snapshots[parts[1]])
					return
				}
			case "hardware":
				if parts[0] == "hosts" {
					reply(w, 200, s.hardware(item))
					return
				}
			case "network":
				if parts[0] == "hosts" {
					reply(w, 200, row{"host_id": item["id"], "hostname": item["hostname"], "connection": "fresh", "supported": true, "state": "none", "management_network": nil, "nics": item["inventory"].(row)["nics"], "check": row{"name": "management-network", "status": "healthy", "detail": "Fictional management network"}})
					return
				}
			}
		}
	}
	if len(parts) >= 2 && parts[0] == "pools" {
		pool := parts[1]
		if find(s.fixtures.lists["pools"], pool) == nil {
			fail(w, 404, "pool not found")
			return
		}
		if len(parts) == 2 {
			reply(w, 200, find(s.fixtures.lists["pools"], pool))
			return
		}
		if len(parts) == 3 {
			switch parts[2] {
			case "members":
				reply(w, 200, s.fixtures.members[pool])
				return
			case "join-candidates":
				reply(w, 200, row{"candidates": []row{}})
				return
			case "backup-repositories":
				reply(w, 200, s.fixtures.backups[pool])
				return
			case "backup-policies":
				reply(w, 200, s.fixtures.policies[pool])
				return
			case "backup-catalog":
				s.backupPage(w, r, pool, true)
				return
			case "backup-runs":
				s.backupPage(w, r, pool, false)
				return
			}
		}
		if len(parts) == 5 && parts[2] == "backup-repositories" && parts[4] == "orphans" {
			if find(s.fixtures.backups[pool], parts[3]) == nil {
				fail(w, 404, "repository not found")
				return
			}
			reply(w, 200, row{"repository_id": parts[3], "groups": []row{}, "warnings": []string{"Simulated inventory; no backup files exist."}})
			return
		}
	}
	fail(w, 404, "demo API route not found")
}

func (s *Server) backupPage(w http.ResponseWriter, r *http.Request, pool string, catalog bool) {
	q := r.URL.Query()
	allowed := map[string]bool{"vm_id": true, "repository_id": true, "search": true, "cursor": true, "generation": true, "limit": true, "recovery_point_id": !catalog, "policy_id": !catalog, "status": !catalog}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			fail(w, 400, "invalid backup query")
			return
		}
	}
	if generation := q.Get("generation"); generation != "" && generation != s.fixtures.generation {
		fail(w, 409, "Demo inventory generation changed; reload the first page.")
		return
	}
	all := s.fixtures.runs[pool]
	if catalog {
		all = s.fixtures.points[pool]
	}
	items := make([]row, 0, len(all))
	for _, item := range all {
		vmKey := "vm_id"
		if catalog {
			vmKey = "source_vm_id"
		}
		if value := q.Get("vm_id"); value != "" && item[vmKey] != value {
			continue
		}
		match := true
		for _, key := range []string{"repository_id", "recovery_point_id", "policy_id", "status"} {
			if value := q.Get(key); value != "" && item[key] != value {
				match = false
			}
		}
		if !match {
			continue
		}
		if search := strings.ToLower(q.Get("search")); search != "" {
			text := fmt.Sprint(item["id"], " ", item["source_vm_name"], " ", item[vmKey])
			if !strings.Contains(strings.ToLower(text), search) {
				continue
			}
		}
		items = append(items, item)
	}
	dateKey := "updated_at"
	if catalog {
		dateKey = "captured_at"
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i][dateKey].(string) > items[j][dateKey].(string) })
	limit := 20
	if value := q.Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			fail(w, 400, "limit must be 1 through 100")
			return
		}
	}
	// Bind cursors to the generation, pool, endpoint and filters. They cannot silently
	// continue another VM/repository's page after a filter change.
	bindingQuery := url.Values{}
	for key, values := range q {
		if key != "cursor" && key != "generation" {
			bindingQuery[key] = values
		}
	}
	binding := fmt.Sprintf("%x", sha256.Sum256([]byte(r.URL.Path+"?"+bindingQuery.Encode()+s.fixtures.generation)))
	start := 0
	if cursor := q.Get("cursor"); cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		offset, signature, ok := strings.Cut(string(data), ":")
		if err != nil || !ok || signature != s.sign(binding+":"+offset) {
			fail(w, 400, "invalid cursor for this query")
			return
		}
		start, err = strconv.Atoi(offset)
		if err != nil || start < 0 || start > len(items) {
			fail(w, 400, "invalid cursor offset")
			return
		}
	}
	end := min(start+limit, len(items))
	out := row{"items": items[start:end], "generation": s.fixtures.generation, "total": len(items)}
	if end < len(items) {
		offset := strconv.Itoa(end)
		out["next_cursor"] = base64.RawURLEncoding.EncodeToString([]byte(offset + ":" + s.sign(binding+":"+offset)))
	}
	if catalog {
		untested := 0
		days := []string{}
		seen := map[string]bool{}
		for _, item := range items {
			if item["last_test_result"] != "passed" {
				untested++
			} else if out["last_restore_test"] == nil {
				out["last_restore_test"] = row{"result": "passed", "tested_at": item["last_tested_at"]}
			}
			day := item["captured_at"].(string)[:10]
			if !seen[day] {
				days = append(days, day)
				seen[day] = true
			}
		}
		out["untested_count"] = untested
		out["backup_days_14"] = days
	} else {
		attention := 0
		for _, item := range items {
			if item["status"] == "failed" || item["status"] == "needs_attention" || item["status"] == "missed" {
				attention++
			}
			if out["last_backup_run"] == nil && strings.HasPrefix(item["kind"].(string), "backup-") && item["kind"] != "backup-test-restore" {
				out["last_backup_run"] = row{"status": item["status"], "updated_at": item["updated_at"]}
			}
		}
		out["attention_count"] = attention
	}
	reply(w, 200, out)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request, kind string, item row) {
	ranges := map[string]struct{ step, count int }{"live": {1, 120}, "10m": {5, 120}, "24h": {60, 1440}, "30d": {3600, 720}, "1y": {86400, 365}}
	span := r.URL.Query().Get("range")
	if span == "" {
		span = "10m"
	}
	tier, ok := ranges[span]
	if !ok {
		fail(w, 400, "unsupported stats range")
		return
	}
	object := "host"
	hostID := item["id"]
	memory := float64(128 * GiB)
	if kind == "vms" {
		object = "vm"
		hostID = item["host_id"]
		memory = float64(item["spec"].(row)["memory"].(row)["size_mib"].(int)) * 1024 * 1024
	}
	series := []row{}
	add := func(key, group, label, unit string, base float64) {
		values := make([]any, tier.count)
		for i := range tier.count {
			if object == "vm" && item["state"] != "running" {
				values[i] = nil
			} else {
				values[i] = base * (1 + 0.12*math.Sin(float64(i)/11) + 0.03*math.Cos(float64(i)/3))
			}
		}
		if key == "memory.total" {
			for i := range values {
				values[i] = memory
			}
		}
		series = append(series, row{"key": key, "group": group, "label": label, "unit": unit, "values": values})
	}
	add("cpu.total", "cpu", "CPU total", "percent", 28)
	add("memory.used", "memory", "Memory used", "bytes", memory*0.46)
	add("memory.total", "memory", "Memory capacity", "bytes", memory)
	add("net.eno1.rx", "network", "Network received", "bytes_per_second", 4e6)
	add("net.eno1.tx", "network", "Network transmitted", "bytes_per_second", 2e6)
	add("disk.vda.read_bytes", "disk", "Disk reads", "bytes_per_second", 8e6)
	add("disk.vda.write_bytes", "disk", "Disk writes", "bytes_per_second", 3e6)
	if object == "host" {
		add("load.1", "load", "1 minute load", "load", 3.2)
		add("temperature.cpu", "temperature", "CPU package", "celsius", 47)
	}
	now := s.now().UTC().Truncate(time.Duration(tier.step) * time.Second)
	reply(w, 200, row{"object": object, "id": item["id"], "host_id": hostID, "range": span, "step_seconds": tier.step, "start": now.Add(-time.Duration(tier.count-1) * time.Duration(tier.step) * time.Second), "series": series})
}

func (s *Server) hardware(host row) row {
	return row{"host_id": host["id"], "observed_at": s.now().UTC(), "system": row{"vendor": "Fictional Systems", "model": "Demo rack server", "serial": "SIMULATED-" + host["id"].(string), "board_vendor": "Fictional Systems", "board_name": "Demo mainboard", "bios_vendor": "Example Firmware", "bios_version": "simulated-1.0", "bios_date": "2026-01-01"}, "cpu": row{"model": "AMD EPYC 7313P", "sockets": 1, "cores": 16, "threads": 32}, "memory": row{"total_bytes": 128 * GiB, "modules": []row{{"slot": "DIMM A1", "size_bytes": 64 * GiB, "type": "DDR4", "speed_mts": 3200}, {"slot": "DIMM B1", "size_bytes": 64 * GiB, "type": "DDR4", "speed_mts": 3200}}}, "disks": row{"devices": []row{{"name": "nvme0n1", "model": "Fictional NVMe", "serial": "SIMULATED-A", "size_bytes": 2 * 1024 * GiB, "rotational": false, "transport": "nvme"}, {"name": "nvme1n1", "model": "Fictional NVMe", "serial": "SIMULATED-B", "size_bytes": 2 * 1024 * GiB, "rotational": false, "transport": "nvme"}}}, "pci": row{"devices": []row{{"address": "0000:01:00.0", "class": "Ethernet controller", "class_id": "0200", "vendor": "Fictional Devices", "vendor_id": "0000", "device": "Demo 10GbE", "device_id": "0000"}}}, "sensors": row{"temperatures": []row{{"key": "temperature.cpu", "chip": "simulated", "label": "CPU package", "max_celsius": 85, "crit_celsius": 95}}}, "md_arrays": row{"arrays": []row{}}}
}
