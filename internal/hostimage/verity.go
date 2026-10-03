// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

const verityBlock = 4096

type verityParams struct {
	dataBlocks uint64
	salt       []byte
}

// parseVeritySuperblock reads a veritysetup format-1 superblock (sha256, 4096/4096 blocks).
func parseVeritySuperblock(b []byte) (verityParams, error) {
	if len(b) < 344 || string(b[:8]) != "verity\x00\x00" {
		return verityParams{}, errors.New("verity superblock missing")
	}
	if le.Uint32(b[8:]) != 1 || le.Uint32(b[12:]) != 1 {
		return verityParams{}, errors.New("unsupported verity version or hash type")
	}
	if alg := string(bytes.TrimRight(b[32:64], "\x00")); alg != "sha256" {
		return verityParams{}, fmt.Errorf("unsupported verity hash algorithm %q", alg)
	}
	if le.Uint32(b[64:]) != verityBlock || le.Uint32(b[68:]) != verityBlock {
		return verityParams{}, errors.New("verity block sizes must be 4096")
	}
	saltSize := int(le.Uint16(b[80:]))
	if saltSize > 256 {
		return verityParams{}, errors.New("verity salt too long")
	}
	return verityParams{dataBlocks: le.Uint64(b[72:]), salt: b[88 : 88+saltSize]}, nil
}

// verityTree computes the hash levels (top level first, without the superblock) and the root hash.
func verityTree(data io.Reader, p verityParams) (tree, root []byte, err error) {
	h := sha256.New()
	hashBlock := func(dst, block []byte) []byte {
		h.Reset()
		h.Write(p.salt)
		h.Write(block)
		return h.Sum(dst)
	}
	level := make([]byte, 0, p.dataBlocks*sha256.Size)
	block := make([]byte, verityBlock)
	for range p.dataBlocks {
		if _, err = io.ReadFull(data, block); err != nil {
			return nil, nil, err
		}
		level = hashBlock(level, block)
	}
	var levels [][]byte
	for {
		level = append(level, make([]byte, (verityBlock-len(level)%verityBlock)%verityBlock)...)
		levels = append(levels, level)
		n := len(level) / verityBlock
		if n == 1 {
			root = hashBlock(nil, level)
			break
		}
		next := make([]byte, 0, n*sha256.Size)
		for i := range n {
			next = hashBlock(next, level[i*verityBlock:(i+1)*verityBlock])
		}
		level = next
	}
	for i := len(levels) - 1; i >= 0; i-- {
		tree = append(tree, levels[i]...)
	}
	return tree, root, nil
}

// VerifyVerity checks a release's root.erofs and root.verity files against the root hash.
func VerifyVerity(erofs, verity, rootHash string) error {
	ef, err := os.Open(erofs)
	if err != nil {
		return err
	}
	defer ef.Close()
	vf, err := os.Open(verity)
	if err != nil {
		return err
	}
	defer vf.Close()
	es, err := ef.Stat()
	if err != nil {
		return err
	}
	vs, err := vf.Stat()
	if err != nil {
		return err
	}
	if vs.Size() > VeritySize {
		return fmt.Errorf("%s is %d bytes, larger than the %d byte verity partition", verity, vs.Size(), VeritySize)
	}
	if err = verifyVerity(ef, es.Size(), vf, vs.Size(), rootHash); err != nil {
		return fmt.Errorf("%s: %w", erofs, err)
	}
	return nil
}

// verifyVerity recomputes the dm-verity hash tree over dataLen bytes of data with the parameters from
// the superblock at the start of hash and requires the stored tree (hashLen readable bytes, which may
// extend past the tree, as a partition does) and the root hash to match.
func verifyVerity(data io.ReaderAt, dataLen int64, hash io.ReaderAt, hashLen int64, rootHash string) error {
	want, err := hex.DecodeString(rootHash)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("root hash %q is not 64 hex characters", rootHash)
	}
	sb := make([]byte, verityBlock)
	if _, err = hash.ReadAt(sb, 0); err != nil {
		return fmt.Errorf("verity superblock: %w", err)
	}
	p, err := parseVeritySuperblock(sb)
	if err != nil {
		return err
	}
	if dataLen != int64(p.dataBlocks)*verityBlock || p.dataBlocks == 0 {
		return fmt.Errorf("data is %d bytes, the verity superblock describes %d blocks of %d", dataLen, p.dataBlocks, verityBlock)
	}
	tree, root, err := verityTree(io.NewSectionReader(data, 0, dataLen), p)
	if err != nil {
		return fmt.Errorf("read data: %w", err)
	}
	if int64(verityBlock+len(tree)) > hashLen {
		return errors.New("hash tree is truncated")
	}
	stored := make([]byte, len(tree))
	if _, err = hash.ReadAt(stored, verityBlock); err != nil {
		return fmt.Errorf("read hash tree: %w", err)
	}
	if !bytes.Equal(stored, tree) {
		return errors.New("hash tree does not match the data")
	}
	if !bytes.Equal(root, want) {
		return fmt.Errorf("root hash is %x, not %s", root, rootHash)
	}
	return nil
}
