// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// zpool-status-degraded.json and zpool-status-resilvering.json are captured
// `zpool status --json` output from OpenZFS 2.3.9 on a disposable Nex Core host: a mirror
// with one member offlined, and a resilver in progress. zpool-status-idle-derived.json is
// derived from a capture of the same pool by removing what the running resilver added
// (`status`, `action`, `scan_stats` and the per-vdev `scan_processed`), because every
// capture taken of that lab pool had a resilver in flight; it is labelled derived, not
// captured.
func TestDecodeZFSPoolStatus(t *testing.T) {
	for _, tc := range []struct {
		file, state, scan, scanState string
		offline                      string
	}{
		{file: "zpool-status-idle-derived.json", state: "ONLINE"},
		{file: "zpool-status-degraded.json", state: "DEGRADED", offline: "/root/fx/a.img"},
		{file: "zpool-status-resilvering.json", state: "ONLINE", scan: "RESILVER", scanState: "SCANNING"},
	} {
		out, err := os.ReadFile("testdata/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		status := decodeZFSPoolStatus(out, "fixturepool")
		if status == nil {
			t.Fatalf("%s: no pool detail decoded", tc.file)
		}
		if status.Name != "fixturepool" || status.State != tc.state {
			t.Fatalf("%s: pool %q state %q", tc.file, status.Name, status.State)
		}
		if len(status.Devices) != 2 {
			t.Fatalf("%s: leaf devices = %#v", tc.file, status.Devices)
		}
		for _, d := range status.Devices {
			if d.Path == "" || d.State == "" || d.ReadErrors != "0" || d.WriteErrors != "0" || d.ChecksumErrors != "0" {
				t.Fatalf("%s: device %#v", tc.file, d)
			}
		}
		if tc.offline != "" {
			if status.Devices[0].Path != tc.offline || status.Devices[0].State != "OFFLINE" || status.Devices[1].State != "ONLINE" {
				t.Fatalf("%s: offlined leaf not reported: %#v", tc.file, status.Devices)
			}
			if !strings.Contains(status.Status, "taken offline") || !strings.Contains(status.Action, "zpool online") {
				t.Fatalf("%s: status %q action %q", tc.file, status.Status, status.Action)
			}
			if strings.ContainsAny(status.Status, "\n\t") {
				t.Fatalf("%s: unwrapped status sentence %q", tc.file, status.Status)
			}
		}
		if tc.scan == "" {
			continue
		}
		if status.Scan == nil || status.Scan.Function != tc.scan || status.Scan.State != tc.scanState || status.Scan.ToExamine == "" || status.Scan.Examined == "" {
			t.Fatalf("%s: scan = %#v", tc.file, status.Scan)
		}
	}
	if decodeZFSPoolStatus([]byte(`{"pools":{"other":{"state":"ONLINE"}}}`), "fixturepool") != nil {
		t.Fatal("decoded detail for a pool the output does not describe")
	}
	if decodeZFSPoolStatus([]byte("not json"), "fixturepool") != nil {
		t.Fatal("decoded detail from invalid output")
	}
}

// zpool-status-spare-derived.json is derived from the idle fixture by adding OpenZFS class
// containers (spares + logs) beside the root. Leaves under those containers must not appear
// in the replaceable device list, and empty name/state leaves are dropped.
func TestDecodeZFSPoolStatusIgnoresClassContainersAndEmptyLeaves(t *testing.T) {
	out, err := os.ReadFile("testdata/zpool-status-spare-derived.json")
	if err != nil {
		t.Fatal(err)
	}
	status := decodeZFSPoolStatus(out, "fixturepool")
	if status == nil || len(status.Devices) != 2 {
		t.Fatalf("spare fixture decoded as %#v", status)
	}
	for _, d := range status.Devices {
		if d.Parent != "" || d.State == "AVAIL" || strings.Contains(d.Path, "spare") {
			t.Fatalf("class-container leaf leaked into devices: %#v", status.Devices)
		}
		if d.Name == "" || d.State == "" {
			t.Fatalf("empty leaf kept: %#v", d)
		}
	}
}

// A replacing-0 vdev keeps parent=replacing on both the failed and replacement leaves so
// Core and the UI can refuse a nested replace before wipefs.
func TestDecodeZFSPoolStatusMarksReplacingLeaves(t *testing.T) {
	out, err := os.ReadFile("testdata/zpool-status-replacing-derived.json")
	if err != nil {
		t.Fatal(err)
	}
	status := decodeZFSPoolStatus(out, "fixturepool")
	if status == nil || len(status.Devices) != 3 {
		t.Fatalf("replacing fixture decoded as %#v", status)
	}
	var replacing, plain int
	for _, d := range status.Devices {
		switch d.Parent {
		case "replacing":
			replacing++
		case "":
			plain++
		default:
			t.Fatalf("unexpected parent %q on %#v", d.Parent, d)
		}
	}
	if replacing != 2 || plain != 1 {
		t.Fatalf("parent markers = replacing:%d plain:%d in %#v", replacing, plain, status.Devices)
	}
}

// A mirror that lost one member keeps serving its VMs: it must stay readable, writable and
// acceptable to placement, with the failed device named in the pool detail.
func TestInspectZFSHealthMapping(t *testing.T) {
	degraded, err := os.ReadFile("testdata/zpool-status-degraded.json")
	if err != nil {
		t.Fatal(err)
	}
	// A root-owned, non-writable ancestor is what Core demands of a repository mountpoint.
	const mount = "/usr/share"
	for _, tc := range []struct{ health, status string }{
		{health: "ONLINE", status: "healthy"},
		{health: "DEGRADED", status: "degraded"},
		{health: "FAULTED"},
		{health: "SUSPENDED"},
		{health: "UNAVAIL"},
		{health: "REMOVED"},
	} {
		old := nativeRun
		nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name + " " + args[0] {
			case "zfs get":
				return []byte("99\n4096\nyes\n" + mount + "\n"), nil
			case "zpool get":
				return []byte("42\n"), nil
			case "zpool list":
				return []byte(tc.health + "\n"), nil
			case "zpool status":
				return degraded, nil
			case "findmnt --noheadings":
				return []byte("fixturepool/vms zfs\n"), nil
			}
			t.Fatalf("unexpected native command: %q %q", name, args)
			return nil, nil
		}
		s := protocol.StorageRegistration{Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "fixturepool", Dataset: "vms", MountPath: mount}}}
		a, err := inspectZFS(context.Background(), s)
		nativeRun = old
		if tc.status == "" {
			if err == nil {
				t.Fatalf("inspectZFS accepted pool health %s", tc.health)
			}
			continue
		}
		if err != nil {
			t.Fatalf("inspectZFS(%s): %v", tc.health, err)
		}
		if a.Health != tc.status || a.ReadOnly || !storageServesData(a.Health) {
			t.Fatalf("inspectZFS(%s) health = %q readonly = %v", tc.health, a.Health, a.ReadOnly)
		}
		if a.ZFSPool == nil || a.ZFSPool.Devices[0].State != "OFFLINE" {
			t.Fatalf("inspectZFS(%s) pool detail = %#v", tc.health, a.ZFSPool)
		}
		if reported := observedStorageAccess(a, time.Now().UTC()); reported.Status != tc.status || !reported.Readable || !reported.Writable || reported.ZFSPool != a.ZFSPool {
			t.Fatalf("observedStorageAccess(%s) = %#v", tc.health, reported)
		}
	}
	if storageServesData("FAULTED") || storageServesData("unavailable") {
		t.Fatal("a pool that cannot serve data was accepted")
	}
}
