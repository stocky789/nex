// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
)

// This file is support for the tests of packages that drive the slot writer (internal/nexupdate).
// Nothing in the product calls it.

// verityImage is a dm-verity hash-tree image (superblock + tree) for data and its root hash: the one writer of
// the superblock layout, shared by SyntheticRelease and the package's own tests.
func verityImage(data, salt []byte) ([]byte, string, error) {
	p := verityParams{dataBlocks: uint64(len(data) / verityBlock), salt: salt}
	tree, root, err := verityTree(bytes.NewReader(data), p)
	if err != nil {
		return nil, "", err
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
	return append(sb, tree...), hex.EncodeToString(root), nil
}

// SyntheticRelease writes a tiny but fully valid release directory (erofs-sized random blocks, a real
// hash tree, a UKI and an ESP tree) into dir. The seed makes every release different, which gives it
// a different root hash.
func SyntheticRelease(dir, label string, sequence int, seed int64, blocks int) error {
	data := make([]byte, blocks*verityBlock)
	var key [32]byte
	binary.LittleEndian.PutUint64(key[:], uint64(seed))
	rand.NewChaCha8(key).Read(data)
	verity, rootHash, err := verityImage(data, bytes.Repeat([]byte{byte(seed)}, 32))
	if err != nil {
		return err
	}
	files := map[string][]byte{
		"root.erofs":  data,
		"root.verity": verity,
		"roothash":    []byte(rootHash + "\n"),
		fmt.Sprintf("nex-core_%s_%d.efi", label, sequence): []byte(fmt.Sprintf("uki-%s-%d-%d", label, sequence, seed)),
		"esp/EFI/BOOT/BOOTX64.EFI":                         []byte("boot-loader"),
		"esp/loader/loader.conf":                           []byte("timeout 0\n"),
	}
	m := Manifest{Label: label, Sequence: sequence, BuildID: fmt.Sprintf("%s-%d", label, sequence), RootHash: rootHash, Files: map[string]ManifestFile{}}
	for name, b := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err = os.WriteFile(full, b, 0o644); err != nil {
			return err
		}
		h := sha256.Sum256(b)
		m.Files[name] = ManifestFile{SHA256: hex.EncodeToString(h[:]), Size: int64(len(b))}
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644)
}

// SandboxMounts points the transient mount directories at base and returns the function that
// restores them, so a test can run the slot writer and the ESP code without root or real mounts
// (pair it with a Runner that does nothing).
func SandboxMounts(base string) (restore func()) {
	oldBase, oldESP, oldMD := mountBase, espMountBase, mdDevDir
	mountBase, espMountBase, mdDevDir = filepath.Join(base, "run"), filepath.Join(base, "run", "esp"), filepath.Join(base, "md")
	return func() { mountBase, espMountBase, mdDevDir = oldBase, oldESP, oldMD }
}

// ESPRoot is where SandboxMounts puts the (not really mounted) ESP of a partition node.
func ESPRoot(node string) string { return filepath.Join(espMountBase, filepath.Base(node)) }
