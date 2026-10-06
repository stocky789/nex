// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// recorder is a Runner that records calls and runs none of them. Mounts are no-ops: the mount
// directories the code creates under mountBase / espMountBase stand in for the file systems.
type recorder struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error // first word(s) of a call -> error
}

func (r *recorder) Run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	if name == "mdadm" && len(args) > 1 && args[0] == "--create" { // stands in for the device node udev would create
		os.MkdirAll(filepath.Dir(args[1]), 0o755)
		os.WriteFile(args[1], nil, 0o644)
	}
	for prefix, err := range r.fail {
		if strings.HasPrefix(call, prefix) {
			return err
		}
	}
	return nil
}

func (r *recorder) index(prefix string) int {
	for i, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

// sandbox points the package's mount directories at a temp dir and returns it. The simulated machine started through
// UEFI (tests of the BIOS method set Writer.Boot).
func sandbox(t *testing.T) string {
	t.Helper()
	restore := SandboxMounts(t.TempDir())
	t.Cleanup(restore)
	return filepath.Dir(mountBase)
}

// makeVerityImage returns a hash-tree image (superblock + tree) for data and the root hash.
func makeVerityImage(t *testing.T, data, salt []byte) ([]byte, string) {
	t.Helper()
	image, root, err := verityImage(data, salt)
	if err != nil {
		t.Fatal(err)
	}
	return image, root
}

// makeRelease writes a verified-format release directory. seed makes every release different.
func makeRelease(t *testing.T, label string, sequence int, seed int64, blocks int) string {
	t.Helper()
	dir := t.TempDir()
	if err := SyntheticRelease(dir, label, sequence, seed, blocks); err != nil {
		t.Fatal(err)
	}
	return dir
}

// sparseDisk creates a sparse disk image file of the given size.
func sparseDisk(t *testing.T, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "disk.img")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	return p
}

// diskDigest hashes a disk image while streaming it: the images are sparse files of several GiB that
// must never be read into memory whole.
func diskDigest(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return [sha256.Size]byte(h.Sum(nil))
}

// diskHead returns the first n bytes of a disk image.
func diskHead(t *testing.T, path string, n int) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, n)
	if _, err = f.ReadAt(b, 0); err != nil {
		t.Fatal(err)
	}
	return b
}

func readTable(t *testing.T, path string) *Table {
	t.Helper()
	d, err := OpenDisk(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tb, err := d.ReadTable()
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func part(t *testing.T, tb *Table, label string) Partition {
	t.Helper()
	p, ok := tb.Find(label)
	if !ok {
		t.Fatalf("no partition %s", label)
	}
	return p
}

func readPart(t *testing.T, path string, tb *Table, label string, n int) []byte {
	t.Helper()
	p := part(t, tb, label)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, n)
	if _, err = f.ReadAt(b, tb.Offset(p)); err != nil {
		t.Fatal(err)
	}
	return b
}
