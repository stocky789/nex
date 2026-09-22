// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeMDFixture builds a /sys/block tree with one md array, as the kernel exposes it.
func writeMDFixture(t *testing.T, name string, array map[string]string, members map[string]map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "sys/block", name, "md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A plain block device beside the array: it must not be reported as one.
	if err := os.MkdirAll(filepath.Join(root, "sys/block", "sda"), 0o755); err != nil {
		t.Fatal(err)
	}
	for file, value := range array {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for member, files := range members {
		if err := os.MkdirAll(filepath.Join(dir, "dev-"+member), 0o755); err != nil {
			t.Fatal(err)
		}
		for file, value := range files {
			if err := os.WriteFile(filepath.Join(dir, "dev-"+member, file), []byte(value+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func TestReadMDArraysReportsHealthyMirror(t *testing.T) {
	root := writeMDFixture(t, "md0",
		map[string]string{"level": "raid1", "raid_disks": "2", "degraded": "0", "sync_action": "idle", "sync_completed": "none"},
		map[string]map[string]string{
			"sda2": {"state": "in_sync", "slot": "0"},
			"sdb2": {"state": "in_sync", "slot": "1"},
		})
	arrays := ReadMDArrays(root)
	if arrays.Unavailable || len(arrays.Arrays) != 1 {
		t.Fatalf("expected one array, got %+v", arrays)
	}
	array := arrays.Arrays[0]
	if array.SyncSpeedKBs != 0 {
		t.Fatalf("an idle array reports no copy rate, got %d", array.SyncSpeedKBs)
	}
	if array.Name != "md0" || array.Device != "/dev/md0" || array.Level != "raid1" || array.RaidDisks != 2 || array.Degraded != 0 {
		t.Fatalf("unexpected array: %+v", array)
	}
	if array.SyncPercent != nil {
		t.Fatalf("an idle array reports no progress, got %v", *array.SyncPercent)
	}
	if len(array.Members) != 2 || array.Members[0].Device != "/dev/sda2" || array.Members[0].Slot != "0" || !mdMemberInSync(array.Members[0]) {
		t.Fatalf("unexpected members: %+v", array.Members)
	}
}

func TestReadMDArraysReportsDegradedMirror(t *testing.T) {
	root := writeMDFixture(t, "md0",
		map[string]string{"level": "raid1", "raid_disks": "2", "degraded": "1", "sync_action": "idle", "sync_completed": "none"},
		map[string]map[string]string{"sda2": {"state": "in_sync", "slot": "0"}})
	array := ReadMDArrays(root).Arrays[0]
	if array.Degraded != 1 || len(array.Members) != 1 {
		t.Fatalf("expected one member of a degraded mirror: %+v", array)
	}
}

func TestReadMDArraysReportsRebuildProgress(t *testing.T) {
	root := writeMDFixture(t, "md0",
		// Verbatim from a lab host rebuilding a mirror, extra files and all: the reader
		// must ignore what it does not use rather than depend on the exact file set.
		map[string]string{"level": "raid1", "raid_disks": "2", "degraded": "1", "sync_action": "recover",
			"sync_completed": "33274368 / 133097344", "array_state": "active", "metadata_version": "1.2",
			"mismatch_cnt": "0", "sync_speed": "200718", "resync_start": "none"},
		map[string]map[string]string{
			"sda2": {"state": "in_sync", "slot": "0", "errors": "0"},
			"sdb2": {"state": "spare", "slot": "1", "errors": "0"},
		})
	array := ReadMDArrays(root).Arrays[0]
	if array.SyncAction != "recover" || array.SyncPercent == nil || math.Abs(*array.SyncPercent-25) > 0.001 {
		t.Fatalf("expected a quarter-complete recovery: %+v", array)
	}
	if array.SyncSpeedKBs != 200718 {
		t.Fatalf("expected the kernel's own copy rate: %+v", array)
	}
	// The kernel gives the rebuilding member its slot straight away and keeps its state
	// "spare" until the copy finishes, so the slot alone never means a working member.
	if mdMemberInSync(array.Members[1]) || array.Members[1].Slot != "1" {
		t.Fatalf("a spare being rebuilt is not in sync: %+v", array.Members[1])
	}
}

func TestReadMDArraysWithoutSysfsIsUnavailable(t *testing.T) {
	arrays := ReadMDArrays(filepath.Join(t.TempDir(), "missing"))
	if !arrays.Unavailable || arrays.Reason == "" {
		t.Fatalf("expected an unavailable section with a reason, got %+v", arrays)
	}
}
