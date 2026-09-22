// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestZFSVdevArgsBuildEachSupportedLayout(t *testing.T) {
	devices := func(n int) []zfsProvisionDevice {
		out := make([]zfsProvisionDevice, n)
		for i := range out {
			out[i] = zfsProvisionDevice{ByID: fmt.Sprintf("/dev/disk/by-id/disk-%d", i+1)}
		}
		return out
	}
	spares := []zfsProvisionDevice{{ByID: "/dev/disk/by-id/spare-1"}}
	for _, tc := range []struct {
		layout string
		count  int
		spares []zfsProvisionDevice
		want   []string
	}{
		{"single", 1, nil, []string{"/dev/disk/by-id/disk-1"}},
		{"mirror", 2, nil, []string{"mirror", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2"}},
		{"mirror", 3, nil, []string{"mirror", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2", "/dev/disk/by-id/disk-3"}},
		{"raidz1", 3, nil, []string{"raidz1", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2", "/dev/disk/by-id/disk-3"}},
		{"raidz2", 4, nil, []string{"raidz2", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2", "/dev/disk/by-id/disk-3", "/dev/disk/by-id/disk-4"}},
		{"raidz3", 5, nil, []string{"raidz3", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2", "/dev/disk/by-id/disk-3", "/dev/disk/by-id/disk-4", "/dev/disk/by-id/disk-5"}},
		{"striped_mirrors", 4, nil, []string{"mirror", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2", "mirror", "/dev/disk/by-id/disk-3", "/dev/disk/by-id/disk-4"}},
		{"striped_mirrors", 6, nil, []string{"mirror", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2", "mirror", "/dev/disk/by-id/disk-3", "/dev/disk/by-id/disk-4", "mirror", "/dev/disk/by-id/disk-5", "/dev/disk/by-id/disk-6"}},
		// A hot spare is appended as its own vdev and satisfies no part of the layout.
		{"mirror", 2, spares, []string{"mirror", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2", "spare", "/dev/disk/by-id/spare-1"}},
		{"raidz1", 3, spares, []string{"raidz1", "/dev/disk/by-id/disk-1", "/dev/disk/by-id/disk-2", "/dev/disk/by-id/disk-3", "spare", "/dev/disk/by-id/spare-1"}},
	} {
		if got := zfsVdevArgs(tc.layout, devices(tc.count), tc.spares); !slices.Equal(got, tc.want) {
			t.Fatalf("%s/%d vdevs = %#v", tc.layout, tc.count, got)
		}
		if !protocol.ValidZFSLayout(tc.layout, tc.count) {
			t.Fatalf("%s/%d rejected by the shared layout check", tc.layout, tc.count)
		}
	}
}

func TestVerifyZFSProvisionVdevsRejectsDifferentTaggedPool(t *testing.T) {
	oldRun, oldResolve := zfsProvisionRun, zfsDiscoveryEvalSymlinks
	t.Cleanup(func() { zfsProvisionRun, zfsDiscoveryEvalSymlinks = oldRun, oldResolve })
	zfsDiscoveryEvalSymlinks = func(path string) (string, error) { return path, nil }
	zfsProvisionRun = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("\tmirror-0 ONLINE\n\t  /dev/disk/by-id/other ONLINE\n\t  /dev/disk/by-id/disk-2 ONLINE\n"), nil
	}
	v := zfsProvisionIntent{Pool: "nex-repo-1", Layout: "mirror", Devices: []zfsProvisionDevice{{ByID: "/dev/disk/by-id/disk-1"}, {ByID: "/dev/disk/by-id/disk-2"}}}
	if err := verifyZFSProvisionVdevs(context.Background(), v); err == nil {
		t.Fatal("accepted different tagged pool devices")
	}
	zfsProvisionRun = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("unavailable") }
	if err := verifyZFSProvisionVdevs(context.Background(), v); err == nil {
		t.Fatal("accepted unavailable zpool status")
	}
}
