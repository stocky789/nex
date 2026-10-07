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
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	tempSuffix = ".new" // the menu and the firmware ignore a half-written file
	// espLockWait bounds the wait for the ESP lock; nex-bless-boot holds it for one short mount, so this is
	// far above any healthy hold and only a stuck holder reaches it.
	espLockWait = 2 * time.Minute
)

// Paths the ESP code reads, replaced by tests.
var (
	sysClassBlock = "/sys/class/block"
	procMounts    = "/proc/self/mounts"
	devDir        = "/dev"
	espMountBase  = "/run/nex/esp"
)

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
	return flockFile(ctx, espMountBase+".lock", espLockWait, "the ESP mount is in use by another Core process")
}

// flockFile takes an exclusive flock on path, waiting for it until ctx ends or wait has passed.
func flockFile(ctx context.Context, path string, wait time.Duration, busy string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
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
			return nil, fmt.Errorf("%s: %w", busy, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// diskLockWait is how long an update step waits for a system disk replacement before it gives up; a
// replacement holds the disk lock for its whole run, which is far longer than any other holder.
var diskLockWait = 10 * time.Second

// lockDisks serializes the operations that write the system disks' partitions and boot partitions: slot
// staging, arming, rollback and the system disk replacement. wait bounds how long the caller queues.
func lockDisks(ctx context.Context, wait time.Duration) (func(), error) {
	return flockFile(ctx, filepath.Join(mountBase, "disks.lock"), wait, "a system disk operation (an update step or a disk replacement) is running")
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
