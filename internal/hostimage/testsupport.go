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
	"sort"
	"strconv"
)

// This file is support for the tests of this package and of packages that drive the slot writer (internal/nexupdate).
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

// SyntheticUKI is a minimal PE file with the three sections a BIOS boot entry is made of: a kernel, an initrd and
// a command line with the root hash. It is not bootable; it parses like a UKI.
func SyntheticUKI(label string, sequence int, seed int64, rootHash string) []byte {
	secs := []struct {
		name string
		data []byte
	}{
		{sectionLinux, []byte(fmt.Sprintf("kernel-%s-%d-%d", label, sequence, seed))},
		{sectionInitrd, []byte(fmt.Sprintf("initrd-%s-%d-%d", label, sequence, seed))},
		{sectionCmdline, []byte("roothash=" + rootHash + " ro console=tty0 panic=10\x00")},
	}
	const peOff, optSize = 0x40, 240
	hdrEnd := peOff + 4 + 20 + optSize + 40*len(secs)
	raw := (hdrEnd + 511) / 512 * 512
	hdr := make([]byte, raw)
	copy(hdr, "MZ")
	le.PutUint32(hdr[0x3c:], peOff)
	copy(hdr[peOff:], "PE\x00\x00")
	coff := hdr[peOff+4:]
	le.PutUint16(coff[0:], 0x8664)
	le.PutUint16(coff[2:], uint16(len(secs)))
	le.PutUint16(coff[16:], optSize)
	le.PutUint16(coff[18:], 0x0022)
	opt := coff[20:]
	le.PutUint16(opt[0:], 0x20b)
	le.PutUint32(opt[108:], 16) // NumberOfRvaAndSizes
	var body []byte
	for i, s := range secs {
		padded := (len(s.data) + 511) / 512 * 512
		h := coff[20+optSize+40*i:]
		copy(h[0:8], s.name)
		le.PutUint32(h[8:], uint32(len(s.data)))    // VirtualSize: the content, without the padding
		le.PutUint32(h[12:], uint32(0x1000*(i+1)))  // VirtualAddress
		le.PutUint32(h[16:], uint32(padded))        // SizeOfRawData
		le.PutUint32(h[20:], uint32(raw+len(body))) // PointerToRawData
		le.PutUint32(h[36:], 0x40000040)            // initialised data, readable
		body = append(body, s.data...)
		body = append(body, make([]byte, padded-len(s.data))...)
	}
	return append(hdr, body...)
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
		fmt.Sprintf("nex-core_%s_%d.efi", label, sequence): SyntheticUKI(label, sequence, seed, rootHash),
		"esp/EFI/BOOT/BOOTX64.EFI":                         []byte("shim"),
		"esp/EFI/BOOT/grubx64.efi":                         []byte("grub"),
		"esp/EFI/debian/grub.cfg":                          []byte("configfile /grub/grub.cfg\n"),
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
// (pair it with a Runner that does nothing). The simulated host started through UEFI.
func SandboxMounts(base string) (restore func()) {
	oldBase, oldESP, oldMD, oldEFI, oldSys := mountBase, espMountBase, mdDevDir, efiFirmwareDir, mdSysBlock
	mountBase, espMountBase, mdDevDir = filepath.Join(base, "run"), filepath.Join(base, "run", "esp"), filepath.Join(base, "md")
	mdSysBlock = filepath.Join(base, "sys-block")
	// The simulated host is a UEFI one, whatever the machine running the test is.
	efiFirmwareDir = filepath.Join(base, "efi")
	os.MkdirAll(efiFirmwareDir, 0o755)
	return func() {
		mountBase, espMountBase, mdDevDir, efiFirmwareDir, mdSysBlock = oldBase, oldESP, oldMD, oldEFI, oldSys
	}
}

// ESPRoot is where SandboxMounts puts the (not really mounted) ESP of a partition node.
func ESPRoot(node string) string { return filepath.Join(espMountBase, filepath.Base(node)) }

// BootOrder is the order the GRUB menu prefers the entries of one boot partition: entries with tries left (or no
// counter) before exhausted ones, then the higher sequence first. The first element is the default entry.
func BootOrder(entries []UKIEntry) []UKIEntry {
	out := append([]UKIEntry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bad() != out[j].Bad() {
			return !out[i].Bad()
		}
		return out[i].Sequence > out[j].Sequence
	})
	return out
}

// MarkTried is what the GRUB menu does when it boots the entry of seq once: one try fewer, saved.
func MarkTried(root string, seq int) error {
	slots, env, err := readSlots(root)
	if err != nil {
		return err
	}
	for _, s := range slots {
		if s.Entry.Sequence == seq && s.Counted && s.Tries > 0 {
			env[triesVar(s.Slot)] = strconv.Itoa(s.Tries - 1)
		}
	}
	return writeGrubEnv(filepath.Join(root, filepath.FromSlash(biosEnv)), env)
}

// Bless is what the bless step does to the entry of seq: its counter goes.
func Bless(root string, seq int) error {
	slots, env, err := readSlots(root)
	if err != nil {
		return err
	}
	for _, s := range slots {
		if s.Entry.Sequence == seq {
			delete(env, triesVar(s.Slot))
		}
	}
	return writeGrubEnv(filepath.Join(root, filepath.FromSlash(biosEnv)), env)
}
