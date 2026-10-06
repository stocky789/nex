// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, tempSuffix) {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// fakeSystem builds sysfs, mounts and device nodes for ESPs: nodes -> directories.
func fakeSystem(t *testing.T, esps map[string]string) {
	t.Helper()
	dir := t.TempDir()
	oldA, oldC, oldE := sysClassBlock, procMounts, devDir
	sysClassBlock, procMounts = filepath.Join(dir, "sys"), filepath.Join(dir, "mounts")
	devDir = filepath.Join(dir, "dev")
	os.MkdirAll(devDir, 0o755)
	t.Cleanup(func() { sysClassBlock, procMounts, devDir = oldA, oldC, oldE })
	var mounts strings.Builder
	var names []string
	for n := range esps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		os.MkdirAll(filepath.Join(sysClassBlock, n), 0o755)
		os.WriteFile(filepath.Join(sysClassBlock, n, "uevent"), []byte("DEVTYPE=partition\nPARTNAME=nex-esp\n"), 0o644)
		os.WriteFile(filepath.Join(sysClassBlock, n+"x"), nil, 0o644) // noise entry without uevent
		os.WriteFile(filepath.Join(devDir, n), nil, 0o644)
		mounts.WriteString(filepath.Join(devDir, n) + " " + esps[n] + " vfat rw 0 0\n")
	}
	os.WriteFile(procMounts, []byte(mounts.String()), 0o644)
}

func TestESPNodesFindsByPartitionName(t *testing.T) {
	fakeSystem(t, map[string]string{"vda1": t.TempDir(), "vdb1": t.TempDir()})
	os.MkdirAll(filepath.Join(sysClassBlock, "vda6"), 0o755)
	os.WriteFile(filepath.Join(sysClassBlock, "vda6", "uevent"), []byte("PARTNAME=nex-data\n"), 0o644)
	os.MkdirAll(filepath.Join(sysClassBlock, "vdc1"), 0o755)
	os.WriteFile(filepath.Join(sysClassBlock, "vdc1", "uevent"), []byte("PARTNAME=other-nex-esp\n"), 0o644)
	nodes, err := ESPNodes()
	if err != nil || !reflect.DeepEqual(nodes, []string{filepath.Join(devDir, "vda1"), filepath.Join(devDir, "vdb1")}) {
		t.Errorf("%v %v", nodes, err)
	}
}

func TestESPSyncMountsUnmountedESP(t *testing.T) {
	sandbox(t)
	booted := t.TempDir()
	if err := UEFI.place(booted, readRelease(t, "r1", 1), "a"); err != nil {
		t.Fatal(err)
	}
	fakeBIOSSystem(t, "vda1", "", map[string]string{"vda1": booted})
	// vdb1 exists but is not mounted anywhere (no line in /proc/mounts): it is mounted transiently.
	os.MkdirAll(filepath.Join(sysClassBlock, "vdb1"), 0o755)
	os.WriteFile(filepath.Join(sysClassBlock, "vdb1", "uevent"), []byte("PARTNAME=nex-esp\n"), 0o644)
	rec := &recorder{}
	if err := (&Writer{Runner: rec, Boot: UEFI}).ESPSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(espMountBase, "vdb1")
	if rec.index("mount -t vfat -o umask=0077,iocharset=ascii "+filepath.Join(devDir, "vdb1")+" "+mp) < 0 || rec.index("umount "+mp) < 0 {
		t.Errorf("transient mount not used: %v", rec.calls)
	}
}

// The engine's reconcile at boot and nex-bless-boot mount the same transient ESP directory. A user that
// finds it mounted borrows it, so two users overlapping makes the first one's unmount fail with
// "target is busy" (the update lab saw nex-update-reconcile.service exit 1 on it).
func TestWithESPDoesNotOverlapOtherUsersOfTheTransientMount(t *testing.T) {
	sandbox(t)
	fakeSystem(t, map[string]string{"vda1": t.TempDir()})
	node := filepath.Join(devDir, "vdb1") // mounted nowhere, so every user mounts it transiently
	w := &Writer{Runner: &recorder{}}
	var active, overlapped atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := w.WithESP(context.Background(), node, func(string) error {
				if active.Add(1) > 1 {
					overlapped.Add(1)
				}
				time.Sleep(20 * time.Millisecond)
				active.Add(-1)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if overlapped.Load() != 0 {
		t.Errorf("%d users held the transient ESP mount at the same time", overlapped.Load())
	}
}

func TestSeedEtcAndAudit(t *testing.T) {
	seed, upper, lower := t.TempDir(), filepath.Join(t.TempDir(), "upper"), t.TempDir()
	writeFiles(t, seed, map[string]string{"hostname": "h\n", "ssh/sshd_config.d/10.conf": "x", "passwd": "root:x:0:0\n"})
	os.Chmod(filepath.Join(seed, "hostname"), 0o600)
	os.Symlink("/etc/hostname", filepath.Join(seed, "link"))
	if err := SeedEtc(seed, upper); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(upper, "hostname")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode not preserved: %v %v", st, err)
	}
	if l, _ := os.Readlink(filepath.Join(upper, "link")); l != "/etc/hostname" {
		t.Errorf("symlink: %q", l)
	}
	// A FIFO in the seed is refused.
	os.Remove(filepath.Join(seed, "link"))
	if err := syscallMkfifo(filepath.Join(seed, "fifo")); err == nil {
		if err = SeedEtc(seed, filepath.Join(t.TempDir(), "u")); err == nil {
			t.Error("FIFO seeded")
		}
	}
	// Audit: passwd differs from the image's, hostname is local-only, ssh conf identical.
	writeFiles(t, lower, map[string]string{"passwd": "root:x:0:0\nnew:x:1:1\n", "ssh/sshd_config.d/10.conf": "x", "os-release": "o"})
	got, err := AuditEtc(lower, upper)
	if err != nil || !reflect.DeepEqual(got, []Shadowed{{Path: "passwd"}}) {
		t.Errorf("audit: %v %v", got, err)
	}
	// A lower layer that is missing or not a directory is an error, not "nothing shadowed".
	if got, err := AuditEtc(filepath.Join(lower, "no-such-dir"), upper); err == nil {
		t.Errorf("missing lower accepted: %v", got)
	}
	if got, err := AuditEtc(filepath.Join(lower, "passwd"), upper); err == nil {
		t.Errorf("file as lower accepted: %v", got)
	}
}

// A UEFI boot partition is the GRUB one plus the loader the firmware starts; a replaced disk gets all of it.
func TestUEFIBootPartitionCarriesTheLoaderAndTheMenu(t *testing.T) {
	sandbox(t)
	src, dst := t.TempDir(), t.TempDir()
	rel1, rel2 := readRelease(t, "r1", 1), readRelease(t, "r2", 2)
	if err := UEFI.place(src, rel1, "a"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "EFI/BOOT/grubx64.efi"), []byte("older grub"), 0o644)
	if err := UEFI.arm(src, rel2, "b", ArmTries); err != nil {
		t.Fatal(err)
	}
	got := tree(t, src)
	if got["EFI/BOOT/BOOTX64.EFI"] != "shim" || got["EFI/BOOT/grubx64.efi"] != "grub" || got["EFI/debian/grub.cfg"] != "configfile /grub/grub.cfg\n" {
		t.Errorf("arm did not bring the loader up to the release's: %v", keysOf(got))
	}
	if got["grub/grub.cfg"] != string(grubConfig()) {
		t.Error("no boot menu")
	}
	assertBootable(t, src)
	if err := UEFI.converge(context.Background(), &Writer{Runner: &recorder{}}, "n", dst, src); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tree(t, dst), got) {
		t.Errorf("converged partition differs: %v vs %v", keysOf(tree(t, dst)), keysOf(got))
	}
}
