// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
)

const (
	ukiDir     = "EFI/Linux"
	tempSuffix = ".new" // never ends in .efi, so the boot loader ignores a half-written file
	// espLockWait bounds the wait for the ESP lock; nex-bless-boot holds it for one short mount, so this is
	// far above any healthy hold and only a stuck holder reaches it.
	espLockWait = 2 * time.Minute
)

// Paths the ESP code reads, replaced by tests.
var (
	loaderDevicePartUUID = "/sys/firmware/efi/efivars/LoaderDevicePartUUID-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
	sysClassBlock        = "/sys/class/block"
	devByPartUUID        = "/dev/disk/by-partuuid"
	procMounts           = "/proc/self/mounts"
	devDir               = "/dev"
	espMountBase         = "/run/nex/esp"
)

// ESPFile is one file an ESP must hold: Rel is the slash path below the ESP root, Path the source.
type ESPFile struct {
	Rel, Path string
}

// counterRE matches the boot-assessment counter in a UKI name: +tries or +tries-done.
var counterRE = regexp.MustCompile(`\+\d+(-\d+)?\.efi$`)

func isUKI(rel string) bool { return path.Dir(rel) == ukiDir && strings.HasSuffix(rel, ".efi") }

// ukiStem is the UKI name without its counter: nex-core_x_3+2-1.efi -> nex-core_x_3.efi.
func ukiStem(name string) string { return counterRE.ReplaceAllString(name, ".efi") }

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyAtomic writes src to dst through dst+".new", flushes it and renames it into place.
func copyAtomic(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + tempSuffix
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// ConvergeESP makes the ESP mounted at root hold every desired file with the desired bytes.
// Files outside EFI/Linux that are not desired are left alone. Under EFI/Linux the UKIs end up
// exactly as desired (names include the boot counter). Order: everything but UKIs first, then the
// UKIs, then the removal of UKIs that are no longer desired, so an interrupted run never leaves
// the ESP without a bootable entry; every desired file is verified by SHA-256 at the end.
func ConvergeESP(root string, desired []ESPFile) error {
	var plain, ukis []ESPFile
	want := map[string]string{}
	for _, f := range desired {
		sum, err := sha256File(f.Path)
		if err != nil {
			return err
		}
		want[f.Rel] = sum
		if isUKI(f.Rel) {
			ukis = append(ukis, f)
		} else {
			plain = append(plain, f)
		}
	}
	if err := removeTemps(root); err != nil {
		return err
	}
	for _, f := range plain {
		dst := filepath.Join(root, filepath.FromSlash(f.Rel))
		if sum, err := sha256File(dst); err == nil && sum == want[f.Rel] {
			continue
		}
		if err := copyAtomic(f.Path, dst); err != nil {
			return fmt.Errorf("write %s: %w", f.Rel, err)
		}
	}
	for _, f := range ukis {
		if err := placeUKI(root, f, want[f.Rel]); err != nil {
			return fmt.Errorf("write %s: %w", f.Rel, err)
		}
	}
	if err := pruneUKIs(root, want); err != nil {
		return err
	}
	for rel, sum := range want {
		got, err := sha256File(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || got != sum {
			return fmt.Errorf("verify %s after writing: not the expected bytes (%v)", rel, err)
		}
	}
	return syncTree(root)
}

// placeUKI installs one UKI. A file with the same stem and the same bytes under a different
// counter (a blessed copy) is renamed instead of copied again.
func placeUKI(root string, f ESPFile, sum string) error {
	dir := filepath.Join(root, ukiDir)
	dst := filepath.Join(root, filepath.FromSlash(f.Rel))
	if got, err := sha256File(dst); err == nil && got == sum {
		return nil
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if name == path.Base(f.Rel) || !strings.HasSuffix(name, ".efi") || ukiStem(name) != ukiStem(path.Base(f.Rel)) {
			continue
		}
		if got, err := sha256File(filepath.Join(dir, name)); err == nil && got == sum {
			return os.Rename(filepath.Join(dir, name), dst)
		}
	}
	return copyAtomic(f.Path, dst)
}

// pruneUKIs removes UKIs under EFI/Linux that are not desired.
func pruneUKIs(root string, want map[string]string) error {
	dir := filepath.Join(root, ukiDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		rel := path.Join(ukiDir, e.Name())
		if _, ok := want[rel]; !ok && strings.HasSuffix(e.Name(), ".efi") {
			if err = os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeTemps(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, tempSuffix) {
			return os.Remove(p)
		}
		return err
	})
}

func syncTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		return syncDir(p)
	})
}

// ScanESP lists every file below an ESP root as desired state for another ESP.
func ScanESP(root string) ([]ESPFile, error) {
	var out []ESPFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, tempSuffix) {
			return err
		}
		rel, err := filepath.Rel(root, p)
		out = append(out, ESPFile{Rel: filepath.ToSlash(rel), Path: p})
		return err
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, err
}

// mountedAt returns the mount point of a device node, if it is mounted anywhere.
func mountedAt(node string) string {
	b, _ := os.ReadFile(procMounts)
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == node {
			return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(f[1])
		}
	}
	return ""
}

// WithESP runs fn with the ESP on a partition node mounted: at its existing mount point, or else
// transiently under /run/nex/esp/<node>. The transient mount is shared with nex-bless-boot, which runs
// on every counted boot, so both hold /run/nex/esp.lock for the whole mount, use and unmount: otherwise
// one side borrows the other's mount and the unmount that follows fails with "target is busy".
func (w *Writer) WithESP(ctx context.Context, node string, fn func(root string) error) error {
	unlock, err := lockESP(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return w.withESP(ctx, node, fn)
}

// withESP is WithESP for a caller that already holds the ESP lock (the lock is not reentrant).
func (w *Writer) withESP(ctx context.Context, node string, fn func(root string) error) (err error) {
	if mp := mountedAt(node); mp != "" {
		return fn(mp)
	}
	dir := filepath.Join(espMountBase, filepath.Base(node))
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err = w.Runner.Run(ctx, "mount", "-t", "vfat", "-o", "umask=0077,iocharset=ascii", node, dir); err != nil {
		return err
	}
	defer func() {
		if uerr := w.Runner.Run(ctx, "umount", dir); err == nil {
			err = uerr
		}
	}()
	return fn(dir)
}

// lockESP takes the exclusive lock every user of the transient ESP mounts holds while it mounts, uses and
// unmounts one. Closing the file releases it, including when the holder dies. The wait ends with ctx or
// after espLockWait, so a stuck holder fails this caller instead of hanging it.
func lockESP(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(espMountBase), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(espMountBase+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(espLockWait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("the ESP mount is in use by another Core process: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ESPNodes returns the partition nodes of every ESP, found by GPT partition name (never by md
// membership or fstab), sorted.
func ESPNodes() ([]string, error) {
	entries, err := os.ReadDir(sysClassBlock)
	if err != nil {
		return nil, err
	}
	var nodes []string
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(sysClassBlock, e.Name(), "uevent"))
		if err == nil && bytes.Contains(append([]byte("\n"), b...), []byte("\nPARTNAME="+ESPLabel+"\n")) {
			nodes = append(nodes, filepath.Join(devDir, e.Name()))
		}
	}
	sort.Strings(nodes)
	return nodes, nil
}

// BootedESP is the partition node of the ESP the running system was loaded from, as the boot
// loader recorded it (LoaderDevicePartUUID). It is empty when the firmware did not boot us via
// systemd-boot.
func BootedESP() (string, error) {
	b, err := os.ReadFile(loaderDevicePartUUID)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	uuid, err := parseEFIString(b)
	if err != nil {
		return "", err
	}
	node, err := filepath.EvalSymlinks(filepath.Join(devByPartUUID, strings.ToLower(uuid)))
	if err != nil {
		return "", fmt.Errorf("boot partition %s: %w", uuid, err)
	}
	return node, nil
}

// parseEFIString decodes an efivarfs file: 4 attribute bytes, then a NUL-terminated UTF-16LE string.
func parseEFIString(b []byte) (string, error) {
	if len(b) < 6 {
		return "", errors.New("EFI variable too short")
	}
	b = b[4:]
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		c := le.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u)), nil
}

// ESPSync copies the booted ESP's files to every other ESP, once the running UKI has been blessed
// (its name carries no boot counter). It is what heals a replaced disk and carries the blessed
// name to the sibling disks; it never runs before the system proved itself.
func (w *Writer) ESPSync(ctx context.Context) error {
	booted, err := BootedESP()
	if err != nil {
		return err
	}
	if booted == "" {
		w.logf("not booted through systemd-boot; no ESP to propagate")
		return nil
	}
	nodes, err := ESPNodes()
	if err != nil {
		return err
	}
	// One hold for the whole sync: copyOut mounts the other ESPs while the booted one is still mounted.
	unlock, err := lockESP(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	var desired []ESPFile
	if err = w.withESP(ctx, booted, func(root string) error {
		desired, err = ScanESP(root)
		if err != nil {
			return err
		}
		for _, f := range desired {
			if isUKI(f.Rel) && counterRE.MatchString(f.Rel) && !isBadUKI(f.Rel) {
				return errNotBlessed
			}
		}
		return w.copyOut(ctx, booted, nodes, desired)
	}); errors.Is(err, errNotBlessed) {
		w.logf("the running boot entry is not blessed yet; leaving the other ESPs unchanged")
		return nil
	}
	return err
}

var errNotBlessed = errors.New("boot entry not blessed")

func (w *Writer) copyOut(ctx context.Context, booted string, nodes []string, desired []ESPFile) error {
	for _, n := range nodes {
		if n == booted {
			continue
		}
		w.logf("converging %s", n)
		if err := w.withESP(ctx, n, func(root string) error { return ConvergeESP(root, desired) }); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	return nil
}
