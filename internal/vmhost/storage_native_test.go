// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestQEMUStorageAccessUsesSeparatePOSIXTests(t *testing.T) {
	old := nativeRun
	t.Cleanup(func() { nativeRun = old })
	var calls [][]string
	path := t.TempDir()
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	if err := qemuStorageAccess(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"runuser", "-u", "libvirt-qemu", "--", "test", "-x", path},
		{"id", "-u", "libvirt-qemu"},
	}
	if len(calls) != 4 || !reflect.DeepEqual(calls[:2], want) || calls[2][0] != "runuser" || calls[2][5] != "-r" || calls[3][5] != "-w" || filepath.Dir(calls[2][6]) != path || calls[2][6] != calls[3][6] {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

type qemuProbeModeInfo struct {
	os.FileInfo
	mode os.FileMode
	sys  any
}

func (info qemuProbeModeInfo) Mode() os.FileMode { return info.mode }
func (info qemuProbeModeInfo) Sys() any          { return info.sys }

func TestQEMUStorageAccessSkipsOnlyExactQEMUModeChmod(t *testing.T) {
	root := t.TempDir()
	oldRun, oldStat, oldChmod := nativeRun, qemuProbeStat, qemuProbeChmod
	t.Cleanup(func() {
		nativeRun, qemuProbeStat, qemuProbeChmod = oldRun, oldStat, oldChmod
	})
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	chmods := 0
	qemuProbeChmod = func(file *os.File, mode os.FileMode) error {
		chmods++
		return oldChmod(file, mode)
	}
	if err := qemuStorageAccess(context.Background(), root); err != nil {
		t.Fatalf("exact mode proof: %v", err)
	}
	if chmods != 0 {
		t.Fatalf("exact regular 0600 probe chmod calls = %d, want 0", chmods)
	}

	for _, test := range []struct {
		name string
		stat func(*os.File) (os.FileInfo, error)
	}{
		{
			name: "restrictive-mode",
			stat: func(file *os.File) (os.FileInfo, error) {
				info, err := oldStat(file)
				return qemuProbeModeInfo{FileInfo: info, mode: 0400, sys: info.Sys()}, err
			},
		},
		{
			name: "special-mode",
			stat: func(file *os.File) (os.FileInfo, error) {
				info, err := oldStat(file)
				return qemuProbeModeInfo{FileInfo: info, mode: 0600 | os.ModeSetuid, sys: info.Sys()}, err
			},
		},
		{
			name: "stat-failure",
			stat: func(*os.File) (os.FileInfo, error) { return nil, errors.New("stat failed") },
		},
		{
			name: "no-syscall-data",
			stat: func(file *os.File) (os.FileInfo, error) {
				info, err := oldStat(file)
				return qemuProbeModeInfo{FileInfo: info, mode: 0600}, err
			},
		},
		{
			name: "uid-mismatch",
			stat: func(file *os.File) (os.FileInfo, error) {
				info, err := oldStat(file)
				return qemuProbeModeInfo{FileInfo: info, mode: 0600, sys: &syscall.Stat_t{Uid: uint32(os.Getuid() + 1)}}, err
			},
		},
		{
			name: "nil-stat-data",
			stat: func(file *os.File) (os.FileInfo, error) {
				info, err := oldStat(file)
				return qemuProbeModeInfo{FileInfo: info, mode: 0600, sys: (*syscall.Stat_t)(nil)}, err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			chmods = 0
			qemuProbeStat = test.stat
			if err := qemuStorageAccess(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			if chmods != 1 {
				t.Fatalf("fallback chmod calls = %d, want 1", chmods)
			}
			qemuProbeStat = oldStat
		})
	}

	chmods = 0
	qemuProbeChmod = func(*os.File, os.FileMode) error {
		chmods++
		return errors.New("chmod failed")
	}
	qemuProbeStat = func(*os.File) (os.FileInfo, error) { return nil, errors.New("stat failed") }
	if err := qemuStorageAccess(context.Background(), root); err == nil || !strings.Contains(err.Error(), "chmod failed") {
		t.Fatalf("chmod failure = %v", err)
	}
	if chmods != 1 {
		t.Fatalf("failing chmod calls = %d, want 1", chmods)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("failed probe cleanup = %v, %v", entries, err)
	}
}

func TestCredentialFileUsesBoundCoreRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "receipt"), []byte("password=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), storageCredentialRootKey{}, root)
	got, err := credentialFile(ctx, protocol.StorageRegistration{CredentialReceipt: "receipt"})
	if err != nil || got != filepath.Join(root, "receipt") {
		t.Fatalf("credentialFile() = %q, %v", got, err)
	}
}

func installGFSQEMUProbeHooks(t *testing.T, machineID *string) {
	t.Helper()
	oldStatfs, oldFstatfs, oldMachineID := qemuStorageStatfs, qemuStorageFstatfs, qemuMachineID
	oldTrusted, oldOwner := qemuGFSRootTrusted, qemuProbeOwner
	oldRemove, oldMountFile, oldRunInDir := qemuProbeRemove, qemuProbeMountFile, qemuRunInDir
	qemuStorageStatfs = func(_ string, stat *syscall.Statfs_t) error {
		stat.Type = int64(gfs2SuperMagic)
		return nil
	}
	qemuStorageFstatfs = func(_ int, stat *syscall.Statfs_t) error {
		stat.Type = int64(gfs2SuperMagic)
		return nil
	}
	qemuMachineID = func(string) ([]byte, error) { return []byte(*machineID + "\n"), nil }
	qemuGFSRootTrusted = func(string) error { return nil }
	qemuProbeOwner = func(uid uint32) bool { return uid == uint32(os.Getuid()) }
	t.Cleanup(func() {
		qemuStorageStatfs, qemuStorageFstatfs, qemuMachineID = oldStatfs, oldFstatfs, oldMachineID
		qemuGFSRootTrusted, qemuProbeOwner = oldTrusted, oldOwner
		qemuProbeRemove, qemuProbeMountFile, qemuRunInDir = oldRemove, oldMountFile, oldRunInDir
	})
}

func qemuProbeDirPath(t *testing.T, dir *os.File) string {
	t.Helper()
	path, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(dir.Fd())))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

type qemuProbeCall struct {
	dir  string
	name string
}

func qemuProbeCalls(t *testing.T, root string) qemuProbeCall {
	t.Helper()
	oldRun, oldRunInDir := nativeRun, qemuRunInDir
	t.Cleanup(func() { nativeRun, qemuRunInDir = oldRun, oldRunInDir })
	var calls []qemuProbeCall
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		if name == "runuser" && len(args) == 6 && (args[4] == "-r" || args[4] == "-w") {
			calls = append(calls, qemuProbeCall{dir: filepath.Dir(args[5]), name: args[5]})
		}
		return nil, nil
	}
	qemuRunInDir = func(ctx context.Context, dir *os.File, name string, args ...string) ([]byte, error) {
		if name == "mkdir" {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir = qemuProbeDirPath(t, dir)
			return cmd.CombinedOutput()
		}
		if name == "runuser" && len(args) == 6 && (args[4] == "-r" || args[4] == "-w") {
			calls = append(calls, qemuProbeCall{dir: qemuProbeDirPath(t, dir), name: args[5]})
		}
		return nil, nil
	}
	if err := qemuStorageAccess(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != calls[1] {
		t.Fatalf("QEMU calls = %#v", calls)
	}
	return calls[0]
}

func TestQEMUStorageAccessGFSUsesPerMachineLeaf(t *testing.T) {
	root := t.TempDir()
	machineID := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &machineID)
	first := qemuProbeCalls(t, root)
	if first.dir == root || !strings.HasPrefix(filepath.Base(first.dir), ".nex-qemu-probe-") {
		t.Fatalf("first probe parent = %q, want per-machine leaf below %q", first.dir, root)
	}
	if entries, err := os.ReadDir(first.dir); err != nil || len(entries) != 0 {
		t.Fatalf("first leaf entries = %v, %v; want no probe files", entries, err)
	}
	machineID = "fedcba9876543210fedcba9876543210"
	second := qemuProbeCalls(t, root)
	if second.dir == first.dir {
		t.Fatalf("different machine identities used the same leaf %q", first.dir)
	}
	machineID = "0123456789abcdef0123456789abcdef"
	third := qemuProbeCalls(t, root)
	if third.dir != first.dir {
		t.Fatalf("same machine identity leaf = %q, want %q", third.dir, first.dir)
	}
	for _, leaf := range []string{first.dir, second.dir} {
		if entries, err := os.ReadDir(leaf); err != nil || len(entries) != 0 {
			t.Fatalf("leaf %q entries = %v, %v; want no probe files", leaf, entries, err)
		}
	}
}

// This would have used root before NFS joined the descriptor-isolated probe set.
func TestQEMUStorageAccessNFSUsesPerMachineLeaf(t *testing.T) {
	root := t.TempDir()
	machineID := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &machineID)
	qemuStorageStatfs = func(_ string, stat *syscall.Statfs_t) error {
		stat.Type = int64(nfsSuperMagic)
		return nil
	}
	qemuStorageFstatfs = func(_ int, stat *syscall.Statfs_t) error {
		stat.Type = int64(nfsSuperMagic)
		return nil
	}
	probe := qemuProbeCalls(t, root)
	if probe.dir == root || !strings.HasPrefix(filepath.Base(probe.dir), ".nex-qemu-probe-") {
		t.Fatalf("NFS probe parent = %q, want per-machine leaf below %q", probe.dir, root)
	}
	if entries, err := os.ReadDir(probe.dir); err != nil || len(entries) != 0 {
		t.Fatalf("NFS leaf entries = %v, %v; want no probe files", entries, err)
	}
}

func TestQEMUNFSProbeDirectoryRetainsRootOwnerRequirement(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	qemuStorageStatfs = func(_ string, stat *syscall.Statfs_t) error {
		stat.Type = int64(nfsSuperMagic)
		return nil
	}
	qemuStorageFstatfs = func(_ int, stat *syscall.Statfs_t) error {
		stat.Type = int64(nfsSuperMagic)
		return nil
	}
	qemuProbeOwner = func(uint32) bool { return false }
	probe := qemuProbeCalls(t, root)
	if probe.dir != root {
		t.Fatalf("non-root NFS probe parent = %q, want original root %q", probe.dir, root)
	}
}

func TestQEMUGFSProbeDirectoryRejectsNonRootLeaf(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	qemuProbeOwner = func(uint32) bool { return false }
	if leaf, isolated, err := qemuGFSProbeDirectory(context.Background(), root); err == nil || !isolated || leaf != nil {
		t.Fatalf("non-root GFS2 leaf = %v, %t, %v; want rejection", leaf, isolated, err)
	}
}

func TestQEMUStorageAccessGFSFallsBackForUntrustedRoot(t *testing.T) {
	root := t.TempDir()
	machineID := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &machineID)
	qemuGFSRootTrusted = func(string) error { return errors.New("untrusted") }
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	var probe string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		if name == "runuser" && len(args) == 6 && args[4] == "-r" {
			probe = args[5]
		}
		return nil, nil
	}
	if err := qemuStorageAccess(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(probe); got != root {
		t.Fatalf("fallback probe parent = %q, want %q", got, root)
	}
}

func TestQEMUStorageAccessNonGFSUsesOriginalRoot(t *testing.T) {
	root := t.TempDir()
	oldStatfs, oldRun := qemuStorageStatfs, nativeRun
	qemuStorageStatfs = func(string, *syscall.Statfs_t) error { return errors.New("unavailable") }
	t.Cleanup(func() { qemuStorageStatfs, nativeRun = oldStatfs, oldRun })
	var probe string
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		if name == "runuser" && len(args) == 6 && args[4] == "-r" {
			probe = args[5]
		}
		return nil, nil
	}
	if err := qemuStorageAccess(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(probe); got != root {
		t.Fatalf("non-GFS probe parent = %q, want %q", got, root)
	}
}

func TestQEMUGFSProbeDirectoryDoesNotIsolateChangedRootFilesystem(t *testing.T) {
	root := t.TempDir()
	machineID := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &machineID)
	qemuStorageFstatfs = func(_ int, stat *syscall.Statfs_t) error {
		stat.Type = 0
		return nil
	}
	leaf, isolated, err := qemuGFSProbeDirectory(context.Background(), root)
	if err == nil || !isolated || leaf != nil {
		t.Fatalf("changed held filesystem = %v, %t, %v; want rejection", leaf, isolated, err)
	}
}

func TestQEMUGFSProbeDirectoryRejectsUnsafeLeafAndIdentity(t *testing.T) {
	validID := "0123456789abcdef0123456789abcdef"
	for _, machineID := range []string{"", "0123456789ABCDEF0123456789ABCDEF", strings.Repeat("0", 32)} {
		t.Run("machine-id="+strconv.Quote(machineID), func(t *testing.T) {
			id := machineID
			installGFSQEMUProbeHooks(t, &id)
			if leaf, isolated, err := qemuGFSProbeDirectory(context.Background(), t.TempDir()); err == nil || !isolated || leaf != nil {
				t.Fatalf("qemuGFSProbeDirectory() = %v, %t, %v; want identity failure", leaf, isolated, err)
			}
		})
	}
	t.Run("machine-id-missing", func(t *testing.T) {
		id := validID
		installGFSQEMUProbeHooks(t, &id)
		qemuMachineID = func(string) ([]byte, error) { return nil, errors.New("missing") }
		if _, isolated, err := qemuGFSProbeDirectory(context.Background(), t.TempDir()); err == nil || !isolated {
			t.Fatalf("want missing identity failure")
		}
	})
	for _, unsafe := range []string{"mode", "owner", "symlink"} {
		t.Run(unsafe, func(t *testing.T) {
			root := t.TempDir()
			id := validID
			installGFSQEMUProbeHooks(t, &id)
			leaf, _, err := qemuGFSProbeDirectory(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			path := qemuProbeDirPath(t, leaf)
			leaf.Close()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if unsafe == "symlink" {
				if err := os.Symlink(root, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
				if unsafe == "owner" {
					qemuProbeOwner = func(uint32) bool { return false }
				}
			}
			if _, isolated, err := qemuGFSProbeDirectory(context.Background(), root); err == nil || !isolated {
				t.Fatalf("unsafe %s leaf was accepted", unsafe)
			}
		})
	}
}

func TestQEMUGFSProbeDirectoryRejectsMountMismatchAndCancellation(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	calls := 0
	qemuProbeMountFile = func(*os.File) (string, error) {
		calls++
		if calls == 1 {
			return "root", nil
		}
		return "leaf", nil
	}
	if _, isolated, err := qemuGFSProbeDirectory(context.Background(), root); err == nil || !isolated {
		t.Fatalf("want mount mismatch")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	qemuProbeMountFile = qemuProbeMountIDFile
	if _, isolated, err := qemuGFSProbeDirectory(ctx, t.TempDir()); !errors.Is(err, context.Canceled) || !isolated {
		t.Fatalf("canceled directory creation = %v, %t", err, isolated)
	}
}

func TestQEMUStorageAccessGFSFailsWhenCleanupFails(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	qemuProbeRemove = func(string) error { return errors.New("remove failed") }
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	qemuRunInDir = func(_ context.Context, _ *os.File, name string, _ ...string) ([]byte, error) {
		if name == "mkdir" {
			return nil, errors.New("test mkdir hook must create leaf")
		}
		return nil, nil
	}
	// Use the real anchored mkdir for this case, and only intercept QEMU access checks.
	qemuRunInDir = func(ctx context.Context, dir *os.File, name string, args ...string) ([]byte, error) {
		if name == "mkdir" {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir = qemuProbeDirPath(t, dir)
			return cmd.CombinedOutput()
		}
		return nil, nil
	}
	if err := qemuStorageAccess(context.Background(), root); err == nil || !strings.Contains(err.Error(), "cannot remove") {
		t.Fatalf("cleanup error = %v", err)
	}
}

func TestQEMUStorageAccessGFSCleansProbeAfterQEMUFailure(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	oldRun := nativeRun
	t.Cleanup(func() { nativeRun = oldRun })
	var dir, name string
	nativeRun = func(_ context.Context, command string, _ ...string) ([]byte, error) {
		if command == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	qemuRunInDir = func(ctx context.Context, leaf *os.File, command string, args ...string) ([]byte, error) {
		if command == "mkdir" {
			cmd := exec.CommandContext(ctx, command, args...)
			cmd.Dir = qemuProbeDirPath(t, leaf)
			return cmd.CombinedOutput()
		}
		dir, name = qemuProbeDirPath(t, leaf), args[5]
		if args[4] == "-w" {
			return nil, errors.New("write denied")
		}
		return nil, nil
	}
	if err := qemuStorageAccess(context.Background(), root); err == nil {
		t.Fatal("QEMU failure succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe remains: %v", err)
	}
}

func TestQEMUGFSProbeDirectoryCreatesTraversableLeafUnderRestrictiveUmask(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	oldUmask := syscall.Umask(0077)
	t.Cleanup(func() { syscall.Umask(oldUmask) })
	leaf, isolated, err := qemuGFSProbeDirectory(context.Background(), root)
	if err != nil || !isolated {
		t.Fatalf("directory = %v, %t, %v", leaf, isolated, err)
	}
	info, err := leaf.Stat()
	leaf.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0711 {
		t.Fatalf("fresh leaf mode = %04o, want 0711", got)
	}
}

func TestQEMUGFSProbeDirectoryConcurrentFirstUse(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var leaves [2]string
	var wait sync.WaitGroup
	for i := range leaves {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			leaf, isolated, err := qemuGFSProbeDirectory(context.Background(), root)
			if err == nil && !isolated {
				err = errors.New("GFS2 root was not isolated")
			}
			if leaf != nil {
				leaves[i] = qemuProbeDirPath(t, leaf)
				leaf.Close()
			}
			errs <- err
		}(i)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent first use: %v", err)
		}
	}
	if leaves[0] == "" || leaves[0] != leaves[1] {
		t.Fatalf("concurrent leaves = %q", leaves)
	}
}

func TestQEMUGFSProbeDirectoryReusesLeafWithoutMkdir(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	oldRun := qemuRunInDir
	t.Cleanup(func() { qemuRunInDir = oldRun })
	mkdirs := 0
	qemuRunInDir = func(ctx context.Context, dir *os.File, name string, args ...string) ([]byte, error) {
		if name != "mkdir" {
			return nil, errors.New("unexpected native command")
		}
		mkdirs++
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = qemuProbeDirPath(t, dir)
		return cmd.CombinedOutput()
	}
	first, isolated, err := qemuGFSProbeDirectory(context.Background(), root)
	if err != nil || !isolated {
		t.Fatalf("first = %v, %t, %v", first, isolated, err)
	}
	first.Close()
	second, isolated, err := qemuGFSProbeDirectory(context.Background(), root)
	if err != nil || !isolated {
		t.Fatalf("second = %v, %t, %v", second, isolated, err)
	}
	second.Close()
	if mkdirs != 1 {
		t.Fatalf("mkdir invocations = %d, want 1", mkdirs)
	}
}

func TestQEMUGFSProbeDirectoryAnchorsRootAgainstPathReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "gfs")
	moved := filepath.Join(parent, "moved")
	other := filepath.Join(parent, "other")
	if err := os.Mkdir(root, 0711); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(other, 0711); err != nil {
		t.Fatal(err)
	}
	machineID := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &machineID)
	oldRun := qemuRunInDir
	t.Cleanup(func() { qemuRunInDir = oldRun })
	qemuRunInDir = func(ctx context.Context, dir *os.File, name string, args ...string) ([]byte, error) {
		if name != "mkdir" {
			return nil, errors.New("unexpected command")
		}
		if err := os.Rename(root, moved); err != nil {
			return nil, err
		}
		if err := os.Symlink(other, root); err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = qemuProbeDirPath(t, dir)
		return cmd.CombinedOutput()
	}
	leaf, isolated, err := qemuGFSProbeDirectory(context.Background(), root)
	if err != nil || !isolated {
		t.Fatalf("anchored directory = %v, %t, %v", leaf, isolated, err)
	}
	leafPath := qemuProbeDirPath(t, leaf)
	leaf.Close()
	if filepath.Dir(leafPath) != moved {
		t.Fatalf("leaf parent = %q, want held root %q", filepath.Dir(leafPath), moved)
	}
}

func prepareGFSQEMUFlightRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	leaf, isolated, err := qemuGFSProbeDirectory(context.Background(), root)
	if err != nil || !isolated {
		t.Fatalf("prepare leaf = %v, %t, %v", leaf, isolated, err)
	}
	if err := leaf.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func prepareNFSQEMUFlightRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	qemuStorageStatfs = func(_ string, stat *syscall.Statfs_t) error {
		stat.Type = int64(nfsSuperMagic)
		return nil
	}
	qemuStorageFstatfs = func(_ int, stat *syscall.Statfs_t) error {
		stat.Type = int64(nfsSuperMagic)
		return nil
	}
	leaf, isolated, err := qemuGFSProbeDirectory(context.Background(), root)
	if err != nil || !isolated {
		t.Fatalf("prepare NFS leaf = %v, %t, %v", leaf, isolated, err)
	}
	if err := leaf.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func resetQEMUGFSProofFlights(t *testing.T) {
	t.Helper()
	qemuGFSProofFlights.Lock()
	qemuGFSProofFlights.byKey = map[qemuGFSProofKey]*qemuGFSProofFlight{}
	qemuGFSProofFlights.Unlock()
	t.Cleanup(func() {
		qemuGFSProofFlights.Lock()
		defer qemuGFSProofFlights.Unlock()
		if len(qemuGFSProofFlights.byKey) != 0 {
			t.Fatalf("QEMU GFS proof flights leaked: %d", len(qemuGFSProofFlights.byKey))
		}
	})
}

// This fails against the pre-BUG0169 implementation, which runs both full
// create/sync/QEMU/cleanup proofs for concurrent callers of the same GFS leaf.
func TestQEMUGFSAccessCoalescesConcurrentLeafProof(t *testing.T) {
	root := prepareGFSQEMUFlightRoot(t)
	resetQEMUGFSProofFlights(t)
	oldRun, oldRunInDir := nativeRun, qemuRunInDir
	t.Cleanup(func() { nativeRun, qemuRunInDir = oldRun, oldRunInDir })
	started := make(chan struct{})
	release := make(chan struct{})
	secondRead := make(chan struct{}, 1)
	var mu sync.Mutex
	reads, writes := 0, 0
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	qemuRunInDir = func(_ context.Context, _ *os.File, name string, args ...string) ([]byte, error) {
		if name != "runuser" {
			return nil, errors.New("unexpected command")
		}
		mu.Lock()
		if args[4] == "-r" {
			reads++
			if reads == 1 {
				close(started)
			} else {
				secondRead <- struct{}{}
			}
		} else if args[4] == "-w" {
			writes++
		}
		first := reads == 1 && args[4] == "-r"
		mu.Unlock()
		if first {
			<-release
		}
		return nil, nil
	}
	results := make(chan error, 2)
	go func() { results <- qemuStorageAccess(context.Background(), root) }()
	<-started
	go func() { results <- qemuStorageAccess(context.Background(), root) }()
	select {
	case <-secondRead:
		close(release)
		t.Fatal("concurrent GFS callers ran duplicate full proofs")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	if reads != 1 || writes != 1 {
		mu.Unlock()
		t.Fatalf("QEMU proof calls = read:%d write:%d, want one pair", reads, writes)
	}
	mu.Unlock()
	if err := qemuStorageAccess(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 2 || writes != 2 {
		t.Fatalf("post-proof calls = read:%d write:%d, want a fresh pair", reads, writes)
	}
}

// This fails against the pre-BUG0172 selector, which did not share a held NFS leaf.
func TestQEMUNFSAccessCoalescesConcurrentLeafProof(t *testing.T) {
	root := prepareNFSQEMUFlightRoot(t)
	resetQEMUGFSProofFlights(t)
	oldRun, oldRunInDir := nativeRun, qemuRunInDir
	t.Cleanup(func() { nativeRun, qemuRunInDir = oldRun, oldRunInDir })
	started := make(chan struct{})
	release := make(chan struct{})
	secondRead := make(chan struct{}, 1)
	var mu sync.Mutex
	reads, writes := 0, 0
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	qemuRunInDir = func(_ context.Context, _ *os.File, name string, args ...string) ([]byte, error) {
		if name != "runuser" {
			return nil, errors.New("unexpected command")
		}
		mu.Lock()
		if args[4] == "-r" {
			reads++
			if reads == 1 {
				close(started)
			} else {
				secondRead <- struct{}{}
			}
		} else if args[4] == "-w" {
			writes++
		}
		first := reads == 1 && args[4] == "-r"
		mu.Unlock()
		if first {
			<-release
		}
		return nil, nil
	}
	results := make(chan error, 2)
	go func() { results <- qemuStorageAccess(context.Background(), root) }()
	<-started
	go func() { results <- qemuStorageAccess(context.Background(), root) }()
	select {
	case <-secondRead:
		close(release)
		t.Fatal("concurrent NFS callers ran duplicate full proofs")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 || writes != 1 {
		t.Fatalf("NFS QEMU proof calls = read:%d write:%d, want one pair", reads, writes)
	}
}

func TestQEMUGFSAccessFollowerCancellationDoesNotStartProof(t *testing.T) {
	root := prepareGFSQEMUFlightRoot(t)
	resetQEMUGFSProofFlights(t)
	oldRun, oldRunInDir := nativeRun, qemuRunInDir
	t.Cleanup(func() { nativeRun, qemuRunInDir = oldRun, oldRunInDir })
	started, release := make(chan struct{}), make(chan struct{})
	secondRead := make(chan struct{}, 1)
	var mu sync.Mutex
	reads := 0
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	qemuRunInDir = func(_ context.Context, _ *os.File, _ string, args ...string) ([]byte, error) {
		mu.Lock()
		if args[4] == "-r" {
			reads++
			if reads == 1 {
				close(started)
			} else {
				secondRead <- struct{}{}
			}
		}
		first := reads == 1 && args[4] == "-r"
		mu.Unlock()
		if first {
			<-release
		}
		return nil, nil
	}
	leader := make(chan error, 1)
	go func() { leader <- qemuStorageAccess(context.Background(), root) }()
	<-started
	followerCtx, cancel := context.WithCancel(context.Background())
	follower := make(chan error, 1)
	go func() { follower <- qemuStorageAccess(followerCtx, root) }()
	cancel()
	if err := <-follower; !errors.Is(err, context.Canceled) {
		close(release)
		t.Fatalf("cancelled follower = %v", err)
	}
	select {
	case <-secondRead:
		close(release)
		t.Fatal("cancelled follower started a duplicate proof")
	default:
	}
	close(release)
	if err := <-leader; err != nil {
		t.Fatal(err)
	}
}

func TestQEMUGFSAccessFailedLeaderLetsLiveFollowerRetry(t *testing.T) {
	root := prepareGFSQEMUFlightRoot(t)
	resetQEMUGFSProofFlights(t)
	oldRun, oldRunInDir := nativeRun, qemuRunInDir
	t.Cleanup(func() { nativeRun, qemuRunInDir = oldRun, oldRunInDir })
	started, release := make(chan struct{}), make(chan struct{})
	secondRead := make(chan struct{}, 1)
	var mu sync.Mutex
	reads, writes := 0, 0
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	qemuRunInDir = func(_ context.Context, _ *os.File, _ string, args ...string) ([]byte, error) {
		mu.Lock()
		if args[4] == "-r" {
			reads++
			if reads == 1 {
				close(started)
			} else {
				secondRead <- struct{}{}
			}
		} else if args[4] == "-w" {
			writes++
		}
		first := reads == 1 && args[4] == "-r"
		mu.Unlock()
		if first {
			<-release
			return nil, errors.New("leader proof failed")
		}
		return nil, nil
	}
	leader := make(chan error, 1)
	follower := make(chan error, 1)
	go func() { leader <- qemuStorageAccess(context.Background(), root) }()
	<-started
	go func() { follower <- qemuStorageAccess(context.Background(), root) }()
	select {
	case <-secondRead:
		close(release)
		t.Fatal("follower bypassed the in-flight leader")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-leader; err == nil {
		t.Fatal("leader failure succeeded")
	}
	if err := <-follower; err != nil {
		t.Fatalf("live follower did not retry: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 2 || writes != 1 {
		t.Fatalf("failed-leader calls = read:%d write:%d, want retry pair", reads, writes)
	}
}

func TestQEMUGFSAccessExpiredLeaderIsNotShared(t *testing.T) {
	root := prepareGFSQEMUFlightRoot(t)
	resetQEMUGFSProofFlights(t)
	oldRun, oldRunInDir := nativeRun, qemuRunInDir
	t.Cleanup(func() { nativeRun, qemuRunInDir = oldRun, oldRunInDir })
	started, release := make(chan struct{}), make(chan struct{})
	secondRead := make(chan struct{}, 1)
	var mu sync.Mutex
	reads, writes := 0, 0
	nativeRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	qemuRunInDir = func(_ context.Context, _ *os.File, _ string, args ...string) ([]byte, error) {
		mu.Lock()
		if args[4] == "-r" {
			reads++
			if reads == 1 {
				close(started)
			} else {
				secondRead <- struct{}{}
			}
		} else if args[4] == "-w" {
			writes++
		}
		first := reads == 1 && args[4] == "-r"
		mu.Unlock()
		if first {
			<-release
		}
		return nil, nil
	}
	leaderCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := make(chan error, 1)
	follower := make(chan error, 1)
	go func() { leader <- qemuStorageAccess(leaderCtx, root) }()
	<-started
	go func() { follower <- qemuStorageAccess(context.Background(), root) }()
	select {
	case <-secondRead:
		close(release)
		t.Fatal("follower bypassed the live leader")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	close(release)
	if err := <-leader; !errors.Is(err, context.Canceled) {
		t.Fatalf("expired leader = %v", err)
	}
	if err := <-follower; err != nil {
		t.Fatalf("live follower did not refresh after expiry: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 2 || writes != 2 {
		t.Fatalf("expired-leader calls = read:%d write:%d, want two full proofs", reads, writes)
	}
}

func TestQEMUGFSProofKeyKeepsDistinctLeavesAndUIDsIndependent(t *testing.T) {
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	installGFSQEMUProbeHooks(t, &id)
	open := func(root string) *os.File {
		leaf, isolated, err := qemuGFSProbeDirectory(context.Background(), root)
		if err != nil || !isolated {
			t.Fatalf("open %q = %v, %t, %v", root, leaf, isolated, err)
		}
		return leaf
	}
	first, second := open(firstRoot), open(secondRoot)
	defer first.Close()
	defer second.Close()
	firstKey, ok := qemuGFSProofKeyFor(first, os.Getuid())
	if !ok {
		t.Fatal("first GFS proof key unavailable")
	}
	secondKey, ok := qemuGFSProofKeyFor(second, os.Getuid())
	if !ok || firstKey == secondKey {
		t.Fatal("distinct held leaves shared a QEMU proof key")
	}
	otherUID, ok := qemuGFSProofKeyFor(first, os.Getuid()+1)
	if !ok || firstKey == otherUID {
		t.Fatal("different QEMU UIDs shared a proof key")
	}
}
