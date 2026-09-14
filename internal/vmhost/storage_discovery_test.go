// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func request(kind string) protocol.StorageDiscoveryRequest {
	return protocol.StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: kind, Server: "storage.example.test"}
}
func TestDiscoverStorageListsOnlyExistingDirectories(t *testing.T) {
	oldSafe := discoveryLocalSafe
	discoveryLocalSafe = func(string, uint64) error { return nil }
	t.Cleanup(func() { discoveryLocalSafe = oldSafe })
	root := t.TempDir()
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "safe"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	g := DiscoverStorage(context.Background(), protocol.StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: protocol.StorageKindLocal, Path: root})
	if g.Status != "succeeded" || len(g.Candidates) != 2 {
		t.Fatalf("%+v", g)
	}
}
func TestDiscoverStorageRejectsLocalRootSymlink(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	g := DiscoverStorage(context.Background(), protocol.StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: protocol.StorageKindLocal, Path: link})
	if g.Status != "failed" {
		t.Fatalf("%+v", g)
	}
}
func TestDiscoverStorageNativeSources(t *testing.T) {
	old := nativeRun
	t.Cleanup(func() { nativeRun = old })
	var calls [][]string
	nativeRun = func(_ context.Context, n string, a ...string) ([]byte, error) {
		calls = append(calls, append([]string{n}, a...))
		switch n {
		case "id":
			return []byte("64055\n"), nil
		case "showmount":
			return []byte("Export list:\n/exports/vms *\n"), nil
		case "smbclient":
			i := slices.Index(a, "-A")
			info, e := os.Stat(a[i+1])
			if e != nil || info.Mode().Perm() != 0600 {
				t.Fatal("credential file")
			}
			return []byte("Disk|vms|storage\n"), nil
		case "iscsiadm":
			return []byte("198.51.100.10:3260,1 iqn.2026-09.test:store\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	for _, k := range []string{protocol.StorageKindNFS, protocol.StorageKindSMB, protocol.StorageKindISCSI} {
		g := DiscoverStorage(context.Background(), request(k))
		if g.Status != "succeeded" || len(g.Candidates) != 1 {
			t.Fatalf("%s: %+v", k, g)
		}
	}
	nfsDirectory := request(protocol.StorageKindNFS)
	nfsDirectory.Path = "/exports/vms"
	nativeRun = func(_ context.Context, n string, a ...string) ([]byte, error) {
		if n == "id" {
			return []byte("64055\n"), nil
		}
		if n == "nfs-ls" {
			if len(a) != 1 || !strings.Contains(a[0], "?uid=64055&gid=64055") {
				t.Fatalf("nfs-ls args %q", a)
			}
			return []byte("-rw-r--r-- 1 1000 1000 8 file\ndrwxrwxrwx 2 1000 1000 8 Team VMs\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	g := DiscoverStorage(context.Background(), nfsDirectory)
	if g.Status != "succeeded" || len(g.Candidates) != 1 || g.Candidates[0].Source.NFS.Directory != "Team VMs" {
		t.Fatalf("NFS directory discovery %+v", g)
	}
	if !slices.Contains(calls[2], "nonpersistent") || slices.Contains(calls[2], "--login") {
		t.Fatalf("%q", calls[2])
	}
	for _, c := range calls {
		if c[0] == "smbclient" {
			_, e := os.Stat(c[slices.Index(c, "-A")+1])
			if !errors.Is(e, os.ErrNotExist) {
				t.Fatal("credential retained")
			}
		}
	}
}

func TestDiscoverSMBDirectoriesSupportsSpacesWithoutCommandParsing(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "smbclient":
			if !slices.Contains(args, "//storage.example.test/VM Files$") || !slices.Contains(args, "Team VMs; quit") || !slices.Contains(args, "-D") || !slices.Contains(args, "-c") || !slices.Contains(args, "ls") {
				t.Fatalf("SMB directory was not passed as a separate argument: %q", args)
			}
			return []byte("  Finance VMs                          D        0  Wed Jan 1 00:00:00 2026\n"), nil
		}
		return nil, errors.New("unexpected native command")
	}
	r := request(protocol.StorageKindSMB)
	r.Path = "/VM Files$/Team VMs; quit"
	g := DiscoverStorage(context.Background(), r)
	if g.Status != "succeeded" || len(g.Candidates) != 1 || g.Candidates[0].Path != "/VM Files$/Team VMs; quit/Finance VMs" {
		t.Fatalf("SMB directory discovery %+v", g)
	}
}
func TestDiscoverStorageCancellationIsReported(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g := DiscoverStorage(ctx, request(protocol.StorageKindNFS))
	if g.Status != "cancelled" {
		t.Fatalf("%+v", g)
	}
}

func TestDiscoverNFSEncodesSelectedExportBeforeAddingQEMUQuery(t *testing.T) {
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "id" {
			return []byte("64055\n"), nil
		}
		if name == "nfs-ls" {
			if len(args) != 1 || args[0] != "nfs://nfs.example.test/exports/vms%3Fuid=attacker%23fragment?uid=64055&gid=64055" {
				t.Fatalf("nfs-ls URL = %q", args)
			}
			return []byte("drwxrwxrwx discovered\n"), nil
		}
		return nil, errors.New("unexpected native command")
	}
	g := DiscoverStorage(context.Background(), protocol.StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: protocol.StorageKindNFS, Server: "nfs.example.test", Path: "/exports/vms?uid=attacker#fragment"})
	if g.Status != "succeeded" {
		t.Fatalf("encoded NFS discovery = %+v", g)
	}
}
func TestDiscoverStorageRejectsUnsafeInput(t *testing.T) {
	g := DiscoverStorage(context.Background(), protocol.StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: protocol.StorageKindNFS, Server: "bad;server"})
	if g.Status != "failed" {
		t.Fatalf("%+v", g)
	}
}
