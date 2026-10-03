// SPDX-License-Identifier: AGPL-3.0-only
package demo

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// Fixture drift must not present registrations the real Core would reject.
func TestBackupDestinationsSatisfyCoreContract(t *testing.T) {
	fixtures := newFixtures(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	for _, destinations := range fixtures.backups {
		for _, destination := range destinations {
			data, err := json.Marshal(destination)
			if err != nil {
				t.Fatal(err)
			}
			var repository protocol.BackupRepository
			if err := json.Unmarshal(data, &repository); err != nil {
				t.Fatal(err)
			}
			if err := repository.Valid(); err != nil {
				t.Errorf("%s cannot be reported by Core: %v", repository.ID, err)
			}
		}
	}
}

func TestVMInventoryMatchesPoolPlacement(t *testing.T) {
	f := newFixtures(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	identities, names := map[string]bool{}, map[string]bool{}
	hostVMs := map[string]map[string]string{}
	hostNames := map[string]map[string]bool{}
	hostRunning := map[string]int{}
	for _, host := range f.lists["hosts"] {
		id := host["id"].(string)
		hostVMs[id] = map[string]string{}
		hostNames[id] = map[string]bool{}
	}
	for _, vm := range f.lists["vms"] {
		id, name := vm["id"].(string), vm["name"].(string)
		if id == "" || name == "" || identities[id] || names[name] {
			t.Fatalf("VM identity or name is empty or duplicated: %q / %q", id, name)
		}
		identities[id], names[name] = true, true
		hostID := vm["host_id"].(string)
		host := find(f.lists["hosts"], hostID)
		if host == nil || host["pool_id"] != vm["pool_id"] {
			t.Fatalf("%s assigned outside its pool: %s", id, hostID)
		}
		hostVMs[hostID][id] = vm["state"].(string)
		hostNames[hostID][name] = true
		if vm["state"] == "running" {
			hostRunning[hostID]++
		}
		usage := vm["usage"].(row)
		if cpu, ok := usage["cpu_percent"].(int); ok && (cpu < 0 || cpu > 100) {
			t.Errorf("%s reports impossible CPU utilization: %d", id, cpu)
		}
		if usage["disk_used_bytes"].(uint64) > usage["disk_capacity_bytes"].(uint64) {
			t.Errorf("%s uses more disk than its capacity", id)
		}
		spec := vm["spec"].(row)
		tags := map[string]bool{}
		for _, tag := range spec["tags"].([]string) {
			if tag == "" || tags[tag] {
				t.Errorf("%s has empty or duplicate tag %q", id, tag)
			}
			tags[tag] = true
		}
		for _, disk := range spec["disks"].([]row) {
			storage := find(f.lists["storage"], disk["storage_id"].(string))
			if storage == nil || storage["pool_id"] != vm["pool_id"] {
				t.Fatalf("%s has storage outside its pool", id)
			}
			accessible := false
			for _, access := range storage["member_access"].([]row) {
				if access["host_id"] == hostID && access["qemu_writable"] == true {
					accessible = true
				}
			}
			if !accessible {
				t.Errorf("%s host cannot write its disk", id)
			}
		}
		for _, nic := range spec["nics"].([]row) {
			network := find(f.lists["networks"], nic["network_id"].(string))
			if network == nil || network["pool_id"] != vm["pool_id"] {
				t.Fatalf("%s has a network outside its pool", id)
			}
			accessible := false
			for _, access := range network["member_access"].([]row) {
				if access["host_id"] == hostID && access["uplink_present"] == true {
					accessible = true
				}
			}
			if !accessible {
				t.Errorf("%s host cannot access its network", id)
			}
		}
	}
	for _, host := range f.lists["hosts"] {
		id := host["id"].(string)
		inventory := host["inventory"].(row)["vms"].([]row)
		actual := map[string]string{}
		for _, vm := range inventory {
			actual[vm["id"].(string)] = vm["state"].(string)
		}
		if len(inventory) != len(actual) || !reflect.DeepEqual(actual, hostVMs[id]) {
			t.Errorf("%s inventory does not match VM placements: got %v, want %v", id, actual, hostVMs[id])
		}
		report := f.members[host["pool_id"].(string)]
		var member row
		for _, candidate := range report["members"].([]row) {
			if candidate["host_id"] == id {
				if member != nil {
					t.Fatalf("%s appears twice in its pool membership", id)
				}
				member = candidate
			}
		}
		if member == nil {
			t.Fatalf("%s is absent from its pool membership", id)
		}
		guests := member["guests"].(row)
		actualNames := map[string]bool{}
		for _, name := range guests["names"].([]string) {
			actualNames[name] = true
		}
		if guests["known"] != true || guests["total"] != len(hostVMs[id]) || guests["running"] != hostRunning[id] ||
			len(guests["names"].([]string)) != len(actualNames) || !reflect.DeepEqual(actualNames, hostNames[id]) {
			t.Errorf("%s membership guest inventory does not match its VM assignments: %v", id, guests)
		}
	}
}

func TestBackupReferencesStayWithinSourcePool(t *testing.T) {
	f := newFixtures(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	for poolID, policies := range f.policies {
		for _, policy := range policies {
			repository := find(f.backups[poolID], policy["repository_id"].(string))
			if policy["pool_id"] != poolID || repository == nil || repository["pool_id"] != poolID {
				t.Fatalf("%s policy has a repository outside its pool", policy["id"])
			}
			for _, vmID := range policy["vm_ids"].([]string) {
				vm := find(f.lists["vms"], vmID)
				if vm == nil || vm["pool_id"] != poolID {
					t.Errorf("%s policy references VM %s outside its pool", policy["id"], vmID)
				}
			}
		}
		for _, point := range f.points[poolID] {
			vm := find(f.lists["vms"], point["source_vm_id"].(string))
			repository := find(f.backups[poolID], point["repository_id"].(string))
			if point["pool_id"] != poolID || vm == nil || vm["pool_id"] != poolID || repository == nil || repository["pool_id"] != poolID {
				t.Fatalf("%s recovery point has an unresolved or cross-pool source", point["id"])
			}
			if point["source_vm_name"] != vm["name"] {
				t.Errorf("%s recovery point names the wrong source VM", point["id"])
			}
		}
		for _, run := range f.runs[poolID] {
			vm := find(f.lists["vms"], run["vm_id"].(string))
			repository := find(f.backups[poolID], run["repository_id"].(string))
			if vm == nil || vm["pool_id"] != poolID || repository == nil || repository["pool_id"] != poolID || run["host_id"] != vm["host_id"] {
				t.Fatalf("%s run has an unresolved or misplaced VM/repository/host", run["operation_id"])
			}
			if policyID, ok := run["policy_id"].(string); ok && find(policies, policyID) == nil {
				t.Errorf("%s run references an unknown policy", run["operation_id"])
			}
			if pointID, ok := run["recovery_point_id"].(string); ok {
				point := find(f.points[poolID], pointID)
				if point == nil || point["source_vm_id"] != vm["id"] || point["repository_id"] != run["repository_id"] {
					t.Errorf("%s run references the wrong recovery point", run["operation_id"])
				}
			}
		}
	}
}

func TestSimulatedFencingMatchesMembership(t *testing.T) {
	f := newFixtures(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	for poolID, report := range f.members {
		summary := report["fence"].(row)
		cluster := summary["cluster"].(row)
		members := report["members"].([]row)
		ready, armed := 0, 0
		unready := map[string]bool{}
		for _, member := range members {
			hostID := member["host_id"].(string)
			host := find(f.lists["hosts"], hostID)
			if host == nil || host["pool_id"] != poolID {
				t.Fatalf("%s membership contains a host outside its pool", poolID)
			}
			fence := member["fence"].(row)
			readiness := fence["readiness"].(row)
			watchdog := fence["watchdog_armed"].(row)
			if fence["watchdog"].(row)["kind"] != "emulated" || watchdog["kind"] != "emulated" {
				t.Errorf("%s demo must not report hardware watchdog proof", hostID)
			}
			if readiness["ready"] == true {
				ready++
			} else {
				unready[hostID] = true
			}
			if watchdog["armed"] == true {
				armed++
			}
			if readiness["ready"] != watchdog["armed"] || watchdog["armed"] != summary["host_fencing"] {
				t.Errorf("%s mock readiness disagrees with watchdog/setting", hostID)
			}
			for _, method := range readiness["methods"].([]row) {
				if method["kind"] == "watchdog" && (method["state"] == "ready") != (watchdog["armed"] == true) {
					t.Errorf("%s watchdog method disagrees with its arming report", hostID)
				}
			}
		}
		actualUnready := map[string]bool{}
		for _, hostID := range summary["unready"].([]string) {
			actualUnready[hostID] = true
		}
		if summary["total"] != len(members) || cluster["total"] != len(members) || summary["ready"] != ready || cluster["armed"] != armed ||
			len(summary["unready"].([]string)) != len(actualUnready) || !reflect.DeepEqual(actualUnready, unready) {
			t.Errorf("%s fencing counts do not match membership readiness", poolID)
		}
		if summary["host_fencing"] == true {
			if cluster["state"] != "armed" {
				t.Errorf("%s enabled mock cluster is not armed", poolID)
			}
			for _, prerequisite := range summary["prerequisites"].([]row) {
				if prerequisite["state"] != "met" {
					t.Errorf("%s mock-armed prerequisite is not met: %v", poolID, prerequisite)
				}
			}
		} else if cluster["state"] != "off" || armed != 0 {
			t.Errorf("%s disabled mock cluster must be off with no armed members", poolID)
		}
	}
}
