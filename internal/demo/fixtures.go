// SPDX-License-Identifier: AGPL-3.0-only
package demo

import (
	"crypto/sha256"
	"fmt"
	"time"
)

type row map[string]any

type fixtures struct {
	lists      map[string][]row
	members    map[string]row
	backups    map[string][]row
	policies   map[string][]row
	points     map[string][]row
	runs       map[string][]row
	snapshots  map[string][]row
	generation string
}

const GiB = uint64(1024 * 1024 * 1024)

// The demo reports simulated current observations, rather than turning every
// storage access light stale thirty seconds after the process started.
type currentObservation struct{}

func (currentObservation) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Now().UTC().Format(time.RFC3339) + `"`), nil
}

func newFixtures(now time.Time) *fixtures {
	f := &fixtures{lists: map[string][]row{}, members: map[string]row{}, backups: map[string][]row{}, policies: map[string][]row{}, points: map[string][]row{}, runs: map[string][]row{}, snapshots: map[string][]row{}, generation: fmt.Sprintf("demo-%d", now.UnixNano())}
	for _, name := range []string{"sites", "pools", "hosts", "vms", "storage", "networks", "media", "operations", "audit"} {
		f.lists[name] = []row{}
	}
	stamp := func(hours int) any {
		if hours == 0 {
			return currentObservation{}
		}
		return now.Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)
	}
	f.lists["sites"] = []row{{"id": "site-harbor", "name": "Harbor · fictional"}, {"id": "site-meadow", "name": "Meadow · fictional"}}
	pools := []struct {
		id, name, site, environment string
		hosts, vms                  int
	}{{"pool-harbor", "Harbor production", "site-harbor", "production", 5, 20}, {"pool-lab", "Harbor research", "site-harbor", "lab", 3, 12}, {"pool-meadow", "Meadow edge", "site-meadow", "edge", 3, 12}}
	capabilities := []string{"managed-vm-v1", "vm-tags-v1", "usage-history-v1", "host-hardware-v1", "management-network-v1", "vm-cpu-baseline-v1"}
	vmNames := []string{"atlas-web", "atlas-database", "cedar-files", "maple-build", "willow-desktop", "aurora-lab", "birch-firewall", "meadow-api", "meadow-monitor", "juniper-archive"}
	osNames := []string{"Debian GNU/Linux 13", "Ubuntu 24.04 LTS", "Windows Server 2025", "Fedora Linux 43", "Windows 11", "FreeBSD 14.3", "Debian GNU/Linux 13", "Alpine Linux 3.22", "Ubuntu 24.04 LTS", "Rocky Linux 9"}
	osIDs := []string{"debian", "ubuntu", "mswindows", "fedora", "mswindows", "freebsd", "debian", "alpine", "ubuntu", "rocky"}
	workloadTags := [][]string{
		{"web", "frontend", "critical"},
		{"database", "postgresql", "critical"},
		{"windows", "files", "internal"},
		{"ci", "automation", "development"},
		{"desktop", "windows", "testing"},
		{"research", "bsd", "sandbox"},
		{"network", "security", "gateway"},
		{"api", "backend", "services"},
		{"monitoring", "metrics", "observability"},
		{"archive", "backup", "cold-storage"},
	}
	vmIndex := 0
	for pi, pool := range pools {
		master := pool.id + "-host-1"
		f.lists["pools"] = append(f.lists["pools"], row{"id": pool.id, "name": pool.name, "site_id": pool.site, "master_host_id": master})
		hostIDs := []string{}
		members := []row{}
		fencingEnabled := pool.environment != "lab"
		fenceState, clusterState, prerequisiteState := "not_ready", "off", "unmet"
		fenceDetail := "Simulation only: host fencing is disabled; no watchdog is armed and no hardware is accessed."
		clusterDetail := fmt.Sprintf("Simulation only: the fictional %d-node pool quorum/watchdog cluster is off; no hardware is accessed.", pool.hosts)
		ready := 0
		if fencingEnabled {
			fenceState, clusterState, prerequisiteState = "ready", "armed", "met"
			fenceDetail = "Simulation only: an emulated watchdog is mock-armed; no hardware is accessed or proven."
			clusterDetail = fmt.Sprintf("Simulation only: the fictional %d-node pool quorum/watchdog cluster is mock-armed; no hardware or real quorum is proven.", pool.hosts)
			ready = pool.hosts
		}
		for hi := range pool.hosts {
			id := fmt.Sprintf("%s-host-%d", pool.id, hi+1)
			hostIDs = append(hostIDs, id)
			name := fmt.Sprintf("%s-%02d.example.test", pool.id, hi+1)
			mac := fmt.Sprintf("02:00:00:%02x:%02x:01", pi, hi)
			nics := []row{{"name": "eno1", "mac": mac, "kind": "physical", "addresses": []string{fmt.Sprintf("192.0.2.%d/24", 10+pi*10+hi)}, "state": "up", "speed_mbps": 10000}, {"name": "br-workloads", "mac": mac, "kind": "bridge", "addresses": []string{}, "state": "up"}}
			inventory := row{"observed_at": stamp(0), "hostname": name, "cpu_model": "AMD EPYC 7313P", "cpu_count": 32, "cpu_models": []string{"EPYC-Milan", "host-model"}, "cpu_baseline": row{"model": "EPYC-Milan", "features": []row{}, "machine": "q35", "generation": 7, "member_count": pool.hosts, "observed_at": stamp(0)}, "memory_bytes": 128 * GiB, "virtualization": []string{"kvm"}, "os_version": "Nex Core · simulated Debian 13", "agent_version": "demo", "nics": nics, "uptime_seconds": 12 * 86400, "disks": []row{{"name": "nvme0n1", "size_bytes": 2 * 1024 * GiB}, {"name": "nvme1n1", "size_bytes": 2 * 1024 * GiB}}, "checks": []row{{"name": "libvirt", "status": "healthy", "freshness": "fresh", "observed_at": stamp(0), "last_success": stamp(0)}, {"name": "pool-storage-reconciliation", "status": "healthy", "freshness": "fresh", "observed_at": stamp(0)}}, "vms": []row{}, "managed": row{"iso_downloads": []row{}}}
			f.lists["hosts"] = append(f.lists["hosts"], row{"id": id, "pool_id": pool.id, "hostname": name, "revoked": false, "capabilities": capabilities, "connection": "fresh", "last_seen": stamp(0), "inventory": inventory})
			role := "member"
			if hi == 0 {
				role = "master"
			}
			members = append(members, row{"host_id": id, "name": name, "role": role, "status": "ready", "connection": "fresh", "maintenance": false, "joined_at": stamp(240), "checks": []row{{"name": "pool-storage-reconciliation", "status": "healthy", "detail": "Simulated storage access verified"}, {"name": "pool-network-reconciliation", "status": "healthy"}}, "access": row{"healthy": 4, "pending": 0, "failed": 0, "stale": 0}, "failures": []row{}, "guests": row{"known": true, "total": 0, "running": 0, "names": []string{}}, "replica": row{"generation": 7, "acknowledged_generation": 7, "verified": true, "state": "current", "received_at": stamp(0), "acknowledged_at": stamp(0)}})
			members[len(members)-1]["fence"] = row{
				"configured": false, "provisioning_disabled": true,
				"bmc":            row{"state": "not_available", "detail": "Simulation only: no management controller or hardware is accessed."},
				"readiness":      row{"ready": fencingEnabled, "methods": []row{{"kind": "watchdog", "state": fenceState, "reason": fenceDetail}}},
				"watchdog":       row{"kind": "emulated", "device": "/dev/watchdog-demo", "driver": "fictional-demo", "timeout_seconds": 60, "nowayout": false, "in_use": fencingEnabled},
				"watchdog_armed": row{"armed": fencingEnabled, "kind": "emulated", "device": "/dev/watchdog-demo", "timeout_seconds": 60, "fencing_generation": 7, "detail": fenceDetail, "observed_at": stamp(0)},
			}
		}
		f.members[pool.id] = row{"pool_id": pool.id, "pool_name": pool.name, "master_host_id": master, "master_connection": "fresh", "master_seen_at": stamp(0), "generation": 7, "authoritative": true, "authority": row{"generation": 7, "digest": fmt.Sprintf("%x", sha256.Sum256([]byte(pool.id))), "observed_at": stamp(0)}, "members": members, "master_conflict": []string{}}
		unready := []string{}
		if !fencingEnabled {
			unready = append(unready, hostIDs...)
		}
		f.members[pool.id]["fence"] = row{
			"ready": ready, "total": len(members), "unready": unready, "host_fencing": fencingEnabled,
			"cluster": row{"state": clusterState, "armed": ready, "total": len(members), "detail": clusterDetail},
			"prerequisites": []row{
				{"name": "hosts", "state": "met", "detail": fmt.Sprintf("Simulation only: all %d fictional hosts are ready and fresh; no hardware is accessed.", pool.hosts)},
				{"name": "watchdogs", "state": prerequisiteState, "detail": fenceDetail},
				{"name": "quorum", "state": prerequisiteState, "detail": clusterDetail},
			},
		}
		sharedID := pool.id + "-nfs"
		for si, kind := range []string{"local", "nfs", "zfs"} {
			id := pool.id + "-" + kind
			access := []row{}
			eligible := []string{master}
			if kind == "nfs" {
				eligible = hostIDs
			}
			for _, host := range eligible {
				access = append(access, row{"repository_id": id, "host_id": host, "generation": 1, "identity": "fictional-" + id, "readable": true, "writable": true, "qemu_writable": true, "locking": true, "observed_at": stamp(0)})
			}
			source := row{}
			if kind == "nfs" {
				source["nfs"] = row{"server": "storage.example.test", "export": "/exports/" + pool.id}
			}
			if kind == "zfs" {
				source["zfs"] = row{"pool": "tank", "dataset": pool.id, "mount_path": "/tank/" + pool.id}
			}
			accessInfo := row{"status": "healthy", "readable": true, "writable": true, "available_bytes": uint64(900+si*250) * GiB, "total_bytes": 2 * 1024 * GiB, "observed_at": stamp(0)}
			if kind == "zfs" {
				accessInfo["zfs_pool"] = row{"name": "tank", "state": "ONLINE", "status": "Fictional mirror is healthy", "devices": []row{{"name": "mirror-0", "state": "ONLINE"}, {"name": "nvme0n1", "parent": "mirror-0", "state": "ONLINE", "read_errors": "0", "write_errors": "0", "checksum_errors": "0"}, {"name": "nvme1n1", "parent": "mirror-0", "state": "ONLINE", "read_errors": "0", "write_errors": "0", "checksum_errors": "0"}}}
			}
			f.lists["storage"] = append(f.lists["storage"], row{"id": id, "pool_id": pool.id, "host_id": master, "name": pool.name + " · " + kind, "operation_id": "demo-storage-" + id, "status": "succeeded", "kind": kind, "path": "/var/lib/nex/storage/" + id, "source": source, "generation": 1, "capabilities": row{"vm_disk": true, "iso": true, "thin": true, "thick": true, "shared": kind == "nfs", "eligible_host_ids": eligible}, "access": accessInfo, "member_access": access, "zfs_scope": "host", "zfs_provisioned": kind == "zfs"})
			if kind == "zfs" {
				f.lists["storage"][len(f.lists["storage"])-1]["zfs_host_access"] = []row{{"host_id": master, "pool_guid": "fictional-tank-" + pool.id, "dataset_guid": "fictional-dataset-" + pool.id, "path": "/tank/" + pool.id, "readable": true, "writable": true, "total_bytes": 2 * 1024 * GiB, "available_bytes": uint64(900+si*250) * GiB, "observed_at": stamp(0), "pool": accessInfo["zfs_pool"]}}
			}
		}
		for ni, label := range []string{"Workloads", "Services"} {
			id := fmt.Sprintf("%s-network-%d", pool.id, ni+1)
			bridge := fmt.Sprintf("br-vlan%d", 110+ni)
			access := []row{}
			for _, host := range hostIDs {
				access = append(access, row{"network_id": id, "host_id": host, "generation": 1, "bridge": bridge, "uplink_present": true, "observed_at": stamp(0)})
			}
			f.lists["networks"] = append(f.lists["networks"], row{"id": id, "pool_id": pool.id, "host_id": master, "name": label, "operation_id": "demo-network-" + id, "status": "succeeded", "kind": "vlan", "source": bridge, "uplink": "eno1", "vlan": 110 + ni, "generation": 1, "member_access": access})
		}
		for mi, label := range []string{"Debian 13 installer.iso", "Windows Server evaluation.iso", "VirtIO guest tools.iso"} {
			id := fmt.Sprintf("%s-media-%d", pool.id, mi+1)
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte("fictional:"+id)))
			access := []row{}
			for _, host := range hostIDs {
				access = append(access, row{"media_id": id, "host_id": host, "storage_id": sharedID, "sha256": digest, "observed_at": stamp(0)})
			}
			f.lists["media"] = append(f.lists["media"], row{"id": id, "pool_id": pool.id, "host_id": master, "name": label, "operation_id": "demo-media-" + id, "status": "succeeded", "storage_id": sharedID, "size_bytes": uint64(mi+1) * GiB, "sha256": digest, "member_access": access})
		}
		vmIDs := []string{}
		for vi := range pool.vms {
			index := vmIndex
			template := index % len(vmNames)
			name := vmNames[template]
			if cycle := index / len(vmNames); cycle > 0 {
				name = fmt.Sprintf("%s-%02d", name, cycle+1)
			}
			tags := append([]string{"fictional", pool.environment}, workloadTags[template]...)
			vmIndex++
			id := fmt.Sprintf("demo-vm-%02d", index+1)
			vmIDs = append(vmIDs, id)
			host := hostIDs[vi%len(hostIDs)]
			state := "running"
			if template == 4 || template == 9 {
				state = "shut off"
			}
			if template == 5 {
				state = "paused"
			}
			guestOS := "linux"
			if template == 2 || template == 4 {
				guestOS = "windows"
			}
			if template == 5 {
				guestOS = "other"
			}
			memory := 4096
			cpu := 2
			if template == 1 || template == 2 {
				memory = 16384
				cpu = 8
			}
			mac := fmt.Sprintf("02:00:00:10:00:%02x", index+1)
			spec := row{"revision": 3, "description": "Fictional " + name + " workload. No real infrastructure or data.", "guest_os": guestOS, "cpu": row{"count": cpu, "sockets": 1, "cores": cpu, "threads": 1, "model": "EPYC-Milan", "features": []row{}}, "memory": row{"mode": "dedicated", "size_mib": memory}, "disks": []row{{"storage_id": sharedID, "name": "system", "size_gib": 80 + template*20, "bus": "virtio", "thin": true, "boot_order": 1}}, "nics": []row{{"network_id": pool.id + "-network-1", "model": "virtio", "mac": mac, "mtu": 1500}}, "boot": row{"firmware": "uefi", "machine": "q35", "media_boot_order": 0, "secure_boot": "on", "tpm": "none"}, "media_id": "", "drivers_media_id": "", "start_after_create": false, "autostart": template != 5, "tags": tags}
			vm := row{"id": id, "pool_id": pool.id, "host_id": host, "name": name, "operation_id": "demo-create-" + id, "status": "succeeded", "state": state, "freshness": "fresh", "role": "", "protected": false, "spec": spec, "detected_os": row{"os": osNames[template], "os_id": osIDs[template], "observed_at": stamp(0)}, "usage": row{"observed_at": stamp(0), "sample_window_ms": 5000, "cpu_percent": 12 + template*3, "memory_assigned_bytes": uint64(memory) * 1024 * 1024, "memory_maximum_bytes": uint64(memory) * 1024 * 1024, "memory_host_bytes": uint64(memory) * 700 * 1024, "disk_used_bytes": uint64(15+template*5) * GiB, "disk_capacity_bytes": uint64(80+template*20) * GiB}}
			if state == "running" {
				vm["guest"] = row{"agent": "connected", "os": osNames[template], "os_id": osIDs[template], "hostname": name + ".example.test", "addresses": []row{{"interface": "eth0", "address": fmt.Sprintf("198.51.100.%d/24", index+20)}}, "clipboard": "no-channel", "resize": false, "media": "empty"}
			} else {
				vm["usage"].(row)["cpu_percent"] = nil
				vm["usage"].(row)["memory_host_bytes"] = nil
			}
			f.lists["vms"] = append(f.lists["vms"], vm)
			f.snapshots[id] = []row{{"id": "snapshot-" + id, "vm_id": id, "name": "Before maintenance · simulated", "description": "Fictional snapshot; cannot be restored or downloaded.", "created_at": stamp(48), "spec_revision": 2}}
			for _, h := range f.lists["hosts"] {
				if h["id"] == host {
					inv := h["inventory"].(row)
					inv["vms"] = append(inv["vms"].([]row), row{"id": id, "state": state})
				}
			}
			for _, m := range members {
				if m["host_id"] == host {
					guests := m["guests"].(row)
					guests["total"] = guests["total"].(int) + 1
					if state == "running" {
						guests["running"] = guests["running"].(int) + 1
					}
					guests["names"] = append(guests["names"].([]string), name)
				}
			}
		}
		f.backups[pool.id] = []row{}
		for ri, kind := range []string{"nfs", "s3"} {
			id := fmt.Sprintf("%s-backup-%d", pool.id, ri+1)
			access := []row{}
			for _, host := range hostIDs {
				access = append(access, row{"repository_id": id, "host_id": host, "identity": "fictional-" + id, "readable": true, "writable": true, "qemu_writable": true, "locking": true, "observed_at": stamp(0)})
			}
			repo := row{
				"id": id, "pool_id": pool.id,
				"name": []string{"Nightly vault", "Offsite archive"}[ri], "kind": kind,
				"created_at": stamp(720), "updated_at": stamp(0),
				"access": row{
					"status": "ok", "readable": true, "writable": true,
					"total_bytes": 8 * 1024 * GiB, "available_bytes": 5 * 1024 * GiB,
					"observed_at": stamp(0),
				},
				"health":        row{"status": "online", "observed_at": stamp(0)},
				"member_access": access,
			}
			if kind == "s3" {
				repo["s3_source"] = row{"endpoint": "https://objects.example.test", "bucket": "fictional-backups", "prefix": pool.id, "region": "example-1"}
			} else {
				repo["source"] = row{"server": "backup.example.test", "export": "/vault/" + pool.id}
			}
			f.backups[pool.id] = append(f.backups[pool.id], repo)
		}
		policyID := pool.id + "-nightly"
		lastDispatch := now.Truncate(24 * time.Hour).Add(2*time.Hour + 30*time.Minute)
		if lastDispatch.After(now) {
			lastDispatch = lastDispatch.Add(-24 * time.Hour)
		}
		testedAt := minTime(now, lastDispatch.Add(30*time.Minute)).Format(time.RFC3339)
		f.policies[pool.id] = []row{{"id": policyID, "schedule_id": policyID + "-schedule", "pool_id": pool.id, "name": "Nightly workloads · simulated", "enabled": true, "paused": false, "repository_id": pool.id + "-backup-1", "capture_mode": "stopped", "time_zone": "UTC", "rule": row{"frequency": "daily", "hour": 2, "minute": 30}, "vm_ids": vmIDs, "retention": row{"keep_latest": 7, "protect_newer_than": int64(24 * time.Hour)}, "limits": row{"max_concurrent_per_host": 1, "max_concurrent_per_repo": 2}, "revision": 2, "next_dispatch_at": lastDispatch.Add(24 * time.Hour).Format(time.RFC3339), "last_dispatch_at": lastDispatch.Format(time.RFC3339)}}
		archiveID := pool.id + "-weekly"
		archiveVMs := vmIDs[len(vmIDs)-1:]
		archiveDispatch := lastDispatch.AddDate(0, 0, -int(lastDispatch.Weekday()))
		f.policies[pool.id] = append(f.policies[pool.id], row{"id": archiveID, "schedule_id": archiveID + "-schedule", "pool_id": pool.id, "name": "Weekly archive · simulated", "enabled": true, "paused": true, "repository_id": pool.id + "-backup-2", "capture_mode": "stopped", "time_zone": "UTC", "rule": row{"frequency": "weekly", "hour": 2, "minute": 30, "weekdays": []int{0}}, "vm_ids": archiveVMs, "retention": row{"keep_latest": 4}, "revision": 1, "last_dispatch_at": archiveDispatch.Format(time.RFC3339)})
		f.points[pool.id] = []row{}
		f.runs[pool.id] = []row{}
		for day := range 7 {
			for _, vmID := range vmIDs {
				vm := find(f.lists["vms"], vmID)
				id := fmt.Sprintf("%s-point-%d", vmID, day)
				captured := lastDispatch.Add(-time.Duration(day) * 24 * time.Hour).Format(time.RFC3339)
				tested := ""
				if day == 0 {
					tested = "passed"
				}
				point := row{"id": id, "repository_id": pool.id + "-backup-1", "pool_id": pool.id, "source_vm_id": vmID, "source_vm_name": vm["name"], "captured_at": captured, "consistency": "stopped", "manifest_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("fictional:"+id))), "protected": day == 6, "last_test_result": tested}
				if tested != "" {
					point["last_tested_at"] = testedAt
					point["last_test_vm_id"] = "simulated-test-" + vmID
				}
				f.points[pool.id] = append(f.points[pool.id], point)
				run := row{"operation_id": "demo-backup-" + id, "kind": "backup-scheduled", "recovery_point_id": id, "policy_id": policyID, "vm_id": vmID, "repository_id": pool.id + "-backup-1", "host_id": vm["host_id"], "status": "succeeded", "updated_at": captured}
				f.runs[pool.id] = append(f.runs[pool.id], run)
				if day == 0 {
					f.runs[pool.id] = append(f.runs[pool.id], row{"operation_id": "demo-verify-" + id, "kind": "backup-test-restore", "recovery_point_id": id, "vm_id": vmID, "repository_id": pool.id + "-backup-1", "host_id": vm["host_id"], "status": "succeeded", "updated_at": testedAt})
				}
			}
		}
		archiveVM := find(f.lists["vms"], archiveVMs[0])
		archivePoint := archiveVMs[0] + "-weekly-point"
		archiveTime := archiveDispatch.Format(time.RFC3339)
		f.points[pool.id] = append(f.points[pool.id], row{"id": archivePoint, "repository_id": pool.id + "-backup-2", "pool_id": pool.id, "source_vm_id": archiveVMs[0], "source_vm_name": archiveVM["name"], "captured_at": archiveTime, "consistency": "stopped", "manifest_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("fictional:"+archivePoint))), "protected": true})
		f.runs[pool.id] = append(f.runs[pool.id], row{"operation_id": "demo-archive-" + archivePoint, "kind": "backup-scheduled", "policy_id": archiveID, "vm_id": archiveVMs[0], "repository_id": pool.id + "-backup-2", "recovery_point_id": archivePoint, "host_id": archiveVM["host_id"], "status": "succeeded", "updated_at": archiveTime})
		// A failed subsequent run keeps its last valid recovery point visible.
		f.runs[pool.id] = append([]row{{"operation_id": "demo-failed-" + pool.id, "kind": "backup-policy-run-now", "policy_id": policyID, "vm_id": vmIDs[0], "repository_id": pool.id + "-backup-1", "host_id": master, "status": "failed", "error": "Simulated source VM is still running; previous stopped recovery point retained.", "updated_at": now.Format(time.RFC3339)}}, f.runs[pool.id]...)
	}
	for i, vm := range f.lists["vms"] {
		f.lists["operations"] = append(f.lists["operations"], row{"id": "demo-create-" + vm["id"].(string), "host_id": vm["host_id"], "kind": "vm-create", "request_id": fmt.Sprintf("demo-request-%d", i), "status": "succeeded", "created_at": stamp(240 - i), "updated_at": stamp(240 - i), "actor": "fictional-operator"})
		f.lists["audit"] = append(f.lists["audit"], row{"id": i + 1, "actor": "fictional-operator", "action": "vm-create", "resource_id": vm["id"], "created_at": stamp(240 - i), "detail": "Simulated demo history; no operation was executed."})
	}
	for _, pool := range pools {
		run := f.runs[pool.id][0]
		f.lists["operations"] = append([]row{{"id": run["operation_id"], "host_id": run["host_id"], "kind": "vm-backup", "request_id": run["operation_id"], "status": "failed", "error": run["error"], "created_at": run["updated_at"], "updated_at": run["updated_at"], "actor": "fictional-scheduler"}}, f.lists["operations"]...)
	}
	return f
}

func find(items []row, id string) row {
	for _, item := range items {
		if item["id"] == id {
			return item
		}
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
