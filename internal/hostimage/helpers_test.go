// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
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

// sandbox points the package's mount directories at a temp dir and returns it.
func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldBase, oldESP, oldMD := mountBase, espMountBase, mdDevDir
	mountBase, espMountBase, mdDevDir = filepath.Join(dir, "run"), filepath.Join(dir, "run", "esp"), filepath.Join(dir, "md")
	t.Cleanup(func() { mountBase, espMountBase, mdDevDir = oldBase, oldESP, oldMD })
	return dir
}

// makeVerityImage returns a hash-tree image (superblock + tree) for data and the root hash.
func makeVerityImage(t *testing.T, data, salt []byte) ([]byte, string) {
	t.Helper()
	p := verityParams{dataBlocks: uint64(len(data) / verityBlock), salt: salt}
	tree, root, err := verityTree(bytes.NewReader(data), p)
	if err != nil {
		t.Fatal(err)
	}
	sb := make([]byte, verityBlock)
	copy(sb, "verity\x00\x00")
	le.PutUint32(sb[8:], 1)
	le.PutUint32(sb[12:], 1)
	copy(sb[32:], "sha256")
	le.PutUint32(sb[64:], verityBlock)
	le.PutUint32(sb[68:], verityBlock)
	le.PutUint64(sb[72:], p.dataBlocks)
	le.PutUint16(sb[80:], uint16(len(salt)))
	copy(sb[88:], salt)
	return append(sb, tree...), hex.EncodeToString(root)
}

func sum(b []byte) ManifestFile {
	h := sha256.Sum256(b)
	return ManifestFile{SHA256: hex.EncodeToString(h[:]), Size: int64(len(b))}
}

// makeRelease writes a verified-format release directory. seed makes every release different.
func makeRelease(t *testing.T, label string, sequence int, seed int64, blocks int) string {
	t.Helper()
	dir := t.TempDir()
	data := make([]byte, blocks*verityBlock)
	rand.New(rand.NewSource(seed)).Read(data)
	verity, root := makeVerityImage(t, data, bytes.Repeat([]byte{byte(seed)}, 32))
	uki := fmt.Sprintf("nex-core_%s_%d.efi", label, sequence)
	files := map[string][]byte{
		"root.erofs":               data,
		"root.verity":              verity,
		"roothash":                 []byte(root + "\n"),
		uki:                        []byte("uki-" + label + fmt.Sprint(seed)),
		"esp/EFI/BOOT/BOOTX64.EFI": []byte("boot-loader"),
		"esp/loader/loader.conf":   []byte("timeout 0\n"),
	}
	m := Manifest{Label: label, Sequence: sequence, BuildID: label, RootHash: root, Files: map[string]ManifestFile{}}
	for name, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		m.Files[name] = sum(b)
	}
	mb, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644); err != nil {
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
