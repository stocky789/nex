// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
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
	files, err := ScanESP(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, _ := os.ReadFile(f.Path)
		out[f.Rel] = string(b)
	}
	return out
}

func desiredFrom(t *testing.T, files map[string]string) []ESPFile {
	t.Helper()
	src := t.TempDir()
	writeFiles(t, src, files)
	out, err := ScanESP(src)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConvergeESP(t *testing.T) {
	esp := t.TempDir()
	writeFiles(t, esp, map[string]string{
		"EFI/Linux/nex-core_old_1.efi": "old uki", // no longer desired: pruned
		"EFI/BOOT/BOOTX64.EFI":         "stale loader",
		"EFI/BOOT/stray.tmp.new":       "half written",
		"keep/me.txt":                  "not ours",
	})
	want := map[string]string{
		"EFI/BOOT/BOOTX64.EFI":        "loader",
		"EFI/Linux/nex-core_r2_2.efi": "uki 2",
		"loader/loader.conf":          "timeout 0\n",
	}
	if err := ConvergeESP(esp, desiredFrom(t, want)); err != nil {
		t.Fatal(err)
	}
	got := tree(t, esp)
	for rel, c := range want {
		if got[rel] != c {
			t.Errorf("%s = %q, want %q", rel, got[rel], c)
		}
	}
	if _, ok := got["EFI/Linux/nex-core_old_1.efi"]; ok {
		t.Error("undesired UKI kept")
	}
	if got["keep/me.txt"] != "not ours" {
		t.Error("a file outside the desired set was removed")
	}
	if _, err := os.Stat(filepath.Join(esp, "EFI/BOOT/stray.tmp.new")); err == nil {
		t.Error("leftover half-written file kept")
	}
	// Idempotent.
	if err := ConvergeESP(esp, desiredFrom(t, want)); err != nil {
		t.Fatal(err)
	}
}

func TestConvergeESPKeepsBlessedCopyWithoutRewriting(t *testing.T) {
	esp := t.TempDir()
	// Booted ESP renamed the UKI when it was blessed; the desired state carries the new name.
	writeFiles(t, esp, map[string]string{"EFI/Linux/nex-core_r2_2+3.efi": "uki 2"})
	if err := ConvergeESP(esp, desiredFrom(t, map[string]string{"EFI/Linux/nex-core_r2_2.efi": "uki 2"})); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, esp); !reflect.DeepEqual(got, map[string]string{"EFI/Linux/nex-core_r2_2.efi": "uki 2"}) {
		t.Errorf("got %v", got)
	}
}

func TestConvergeESPFailureNeverRemovesWorkingUKI(t *testing.T) {
	esp := t.TempDir()
	writeFiles(t, esp, map[string]string{"EFI/Linux/nex-core_r1_1.efi": "uki 1"})
	// The new UKI source disappears before it can be copied: the old, bootable one must stay.
	desired := []ESPFile{{Rel: "EFI/Linux/nex-core_r2_2.efi", Path: filepath.Join(t.TempDir(), "missing")}}
	if err := ConvergeESP(esp, desired); err == nil {
		t.Fatal("missing source accepted")
	}
	if got := tree(t, esp); got["EFI/Linux/nex-core_r1_1.efi"] != "uki 1" {
		t.Errorf("working UKI lost: %v", got)
	}
}

func efiString(s string) []byte {
	u := utf16.Encode([]rune(s + "\x00"))
	b := []byte{6, 0, 0, 0}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

// fakeSystem builds sysfs, by-partuuid, mounts and the loader variable for ESPs: nodes -> directories.
func fakeSystem(t *testing.T, booted string, esps map[string]string) {
	t.Helper()
	dir := t.TempDir()
	oldA, oldB, oldC, oldD, oldE := sysClassBlock, devByPartUUID, procMounts, loaderDevicePartUUID, devDir
	sysClassBlock, devByPartUUID, procMounts = filepath.Join(dir, "sys"), filepath.Join(dir, "by-partuuid"), filepath.Join(dir, "mounts")
	loaderDevicePartUUID = filepath.Join(dir, "LoaderDevicePartUUID")
	devDir = filepath.Join(dir, "dev")
	os.MkdirAll(devDir, 0o755)
	t.Cleanup(func() {
		sysClassBlock, devByPartUUID, procMounts, loaderDevicePartUUID, devDir = oldA, oldB, oldC, oldD, oldE
	})
	os.MkdirAll(devByPartUUID, 0o755)
	var mounts strings.Builder
	var names []string
	for n := range esps {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		os.MkdirAll(filepath.Join(sysClassBlock, n), 0o755)
		os.WriteFile(filepath.Join(sysClassBlock, n, "uevent"), []byte("DEVTYPE=partition\nPARTNAME=nex-esp\n"), 0o644)
		os.WriteFile(filepath.Join(sysClassBlock, n+"x"), nil, 0o644) // noise entry without uevent
		node := filepath.Join(devDir, n)
		os.WriteFile(node, nil, 0o644)
		uuid := "00000000-0000-0000-0000-00000000000" + string(rune('1'+i))
		os.Symlink(node, filepath.Join(devByPartUUID, uuid))
		if n == booted {
			os.WriteFile(loaderDevicePartUUID, efiString(strings.ToUpper(uuid)), 0o644)
		}
		fmtMount := filepath.Join(devDir, n) + " " + esps[n] + " vfat rw 0 0\n"
		mounts.WriteString(fmtMount)
	}
	os.WriteFile(procMounts, []byte(mounts.String()), 0o644)
}

func TestESPNodesFindsByPartitionName(t *testing.T) {
	fakeSystem(t, "", map[string]string{"vda1": t.TempDir(), "vdb1": t.TempDir()})
	os.MkdirAll(filepath.Join(sysClassBlock, "vda6"), 0o755)
	os.WriteFile(filepath.Join(sysClassBlock, "vda6", "uevent"), []byte("PARTNAME=nex-data\n"), 0o644)
	os.MkdirAll(filepath.Join(sysClassBlock, "vdc1"), 0o755)
	os.WriteFile(filepath.Join(sysClassBlock, "vdc1", "uevent"), []byte("PARTNAME=other-nex-esp\n"), 0o644)
	nodes, err := ESPNodes()
	if err != nil || !reflect.DeepEqual(nodes, []string{filepath.Join(devDir, "vda1"), filepath.Join(devDir, "vdb1")}) {
		t.Errorf("%v %v", nodes, err)
	}
}

func TestESPSyncPropagatesBlessedEntry(t *testing.T) {
	sandbox(t)
	booted, other := t.TempDir(), t.TempDir()
	fakeSystem(t, "vda1", map[string]string{"vda1": booted, "vdb1": other})
	writeFiles(t, booted, map[string]string{"EFI/BOOT/BOOTX64.EFI": "loader", "EFI/Linux/nex-core_r2_2.efi": "uki 2"})
	writeFiles(t, other, map[string]string{"EFI/Linux/nex-core_r1_1.efi": "uki 1"})
	if err := (&Writer{Runner: &recorder{}}).ESPSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tree(t, booted), tree(t, other)) {
		t.Errorf("ESPs differ: %v vs %v", tree(t, booted), tree(t, other))
	}
}

func TestESPSyncWaitsForBless(t *testing.T) {
	sandbox(t)
	booted, other := t.TempDir(), t.TempDir()
	fakeSystem(t, "vda1", map[string]string{"vda1": booted, "vdb1": other})
	writeFiles(t, booted, map[string]string{"EFI/Linux/nex-core_r2_2+3-1.efi": "uki 2"})
	writeFiles(t, other, map[string]string{"EFI/Linux/nex-core_r1_1.efi": "uki 1"})
	if err := (&Writer{Runner: &recorder{}}).ESPSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, other); len(got) != 1 || got["EFI/Linux/nex-core_r1_1.efi"] != "uki 1" {
		t.Errorf("the previous ESP was changed before the boot entry was blessed: %v", got)
	}
}

func TestESPSyncMountsUnmountedESP(t *testing.T) {
	sandbox(t)
	booted := t.TempDir()
	fakeSystem(t, "vda1", map[string]string{"vda1": booted})
	// vdb1 exists but is not mounted anywhere (no line in /proc/mounts): it is mounted transiently.
	os.MkdirAll(filepath.Join(sysClassBlock, "vdb1"), 0o755)
	os.WriteFile(filepath.Join(sysClassBlock, "vdb1", "uevent"), []byte("PARTNAME=nex-esp\n"), 0o644)
	writeFiles(t, booted, map[string]string{"EFI/Linux/nex-core_r2_2.efi": "uki 2"})
	rec := &recorder{}
	if err := (&Writer{Runner: rec}).ESPSync(context.Background()); err != nil {
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
	fakeSystem(t, "vda1", map[string]string{"vda1": t.TempDir()})
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
