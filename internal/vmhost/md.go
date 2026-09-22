// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// The Linux md arrays this host assembles, read straight from sysfs. mdadm is not parsed:
// the kernel's own files are the record, and reading them keeps the whole thing testable
// against a fixture tree.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"nex.local/nex/internal/protocol"
)

// ReadMDArrays observes every assembled md array. root is "/" in production and a fixture
// tree in tests.
func ReadMDArrays(root string) protocol.HardwareMDArrays {
	arrays := protocol.HardwareMDArrays{Arrays: []protocol.MDArray{}}
	entries, err := os.ReadDir(filepath.Join(root, "sys/block"))
	if err != nil {
		arrays.Unavailable, arrays.Reason = true, "the kernel exposes no block devices for this host"
		return arrays
	}
	for _, entry := range entries {
		dir := filepath.Join(root, "sys/block", entry.Name(), "md")
		if !strings.HasPrefix(entry.Name(), "md") {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue // a block device whose name merely starts with "md" is not an array
		}
		if len(arrays.Arrays) == protocol.HardwareMaxMDArrays {
			break
		}
		arrays.Arrays = append(arrays.Arrays, readMDArray(entry.Name(), dir))
	}
	return arrays
}

func readMDArray(name, dir string) protocol.MDArray {
	array := protocol.MDArray{Name: name, Device: "/dev/" + name, Level: mdSysfs(dir, "level"), Members: []protocol.MDMember{}}
	array.RaidDisks, _ = strconv.Atoi(mdSysfs(dir, "raid_disks"))
	array.Degraded, _ = strconv.Atoi(mdSysfs(dir, "degraded"))
	array.SyncAction = mdSysfs(dir, "sync_action")
	if percent, ok := mdSyncPercent(mdSysfs(dir, "sync_completed")); ok {
		array.SyncPercent = &percent
	}
	// "none" while nothing is running, which parses to zero and is then omitted.
	array.SyncSpeedKBs, _ = strconv.Atoi(mdSysfs(dir, "sync_speed"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return array
	}
	for _, entry := range entries {
		member, ok := strings.CutPrefix(entry.Name(), "dev-")
		if !ok {
			continue // md/ also holds the array's own files, which are not members
		}
		if len(array.Members) == protocol.HardwareMaxMDMembers {
			break
		}
		array.Members = append(array.Members, protocol.MDMember{
			Name:   member,
			Device: "/dev/" + member,
			Slot:   mdSysfs(filepath.Join(dir, entry.Name()), "slot"),
			State:  mdSysfs(filepath.Join(dir, entry.Name()), "state"),
		})
	}
	return array
}

// mdSyncPercent reads md/sync_completed, which is "<done> / <total>" in sectors while an
// action runs and "none" when none does. A zero total is no progress, not a division.
func mdSyncPercent(value string) (float64, bool) {
	done, total, ok := strings.Cut(value, "/")
	if !ok {
		return 0, false
	}
	position, err := strconv.ParseFloat(strings.TrimSpace(done), 64)
	size, sizeErr := strconv.ParseFloat(strings.TrimSpace(total), 64)
	if err != nil || sizeErr != nil || size <= 0 {
		return 0, false
	}
	return position / size * 100, true
}

func mdSysfs(dir, name string) string {
	value, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(value))
}

// mdMemberInSync reports whether the kernel counts this member as carrying the array's data.
func mdMemberInSync(member protocol.MDMember) bool {
	for _, state := range strings.Split(member.State, ",") {
		if strings.TrimSpace(state) == "in_sync" {
			return true
		}
	}
	return false
}
