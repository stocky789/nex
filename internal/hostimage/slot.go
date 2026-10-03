// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"fmt"
	"io"
	"os"
	"unsafe"
)

// Writer carries the tools and progress sink of install, repair and slot writes.
type Writer struct {
	Runner Runner
	Logf   func(format string, args ...any)
}

func (w *Writer) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}

// settle makes udev re-probe the block devices (the kernel never carries partition GUIDs; blkid
// reads them from the on-disk GPT) and waits for the symlinks to catch up.
func (w *Writer) settle(ctx context.Context) error {
	if err := w.Runner.Run(ctx, "udevadm", "trigger", "--action=change", "--subsystem-match=block"); err != nil {
		return err
	}
	return w.Runner.Run(ctx, "udevadm", "settle", "--timeout=30")
}

// partIO is one partition's bytes. On a block device it is the partition node, so reads after a
// write cannot be served from the whole-disk node's cache; on a regular file it is a slice of it.
type partIO struct {
	f     File
	off   int64
	size  int64
	block bool
	close bool
}

func openPart(d *Disk, t *Table, p Partition) (*partIO, error) {
	if !d.block {
		return &partIO{f: d.File, off: t.Offset(p), size: t.Size(p)}, nil
	}
	node := NodeName(d.Path, p.Index)
	f, err := os.OpenFile(node, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	var size uint64
	if err = ioctl(f.Fd(), blkGETSIZE64, unsafe.Pointer(&size)); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", node, err)
	}
	if int64(size) != t.Size(p) {
		f.Close()
		return nil, fmt.Errorf("%s is %d bytes but the GPT says %d: the kernel's partition table is stale", node, size, t.Size(p))
	}
	return &partIO{f: f, size: int64(size), block: true, close: true}, nil
}

func (p *partIO) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(b)) > p.size {
		return 0, io.EOF
	}
	return p.f.ReadAt(b, p.off+off)
}

func (p *partIO) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(b)) > p.size {
		return 0, fmt.Errorf("write beyond the end of the partition")
	}
	return p.f.WriteAt(b, p.off+off)
}

func (p *partIO) Sync() error { return dropCaches(p.f, p.block) }

func (p *partIO) Close() error {
	if p.close {
		return p.f.Close()
	}
	return nil
}

func slotLabels(slot string) (root, verity string) {
	return "nex-root-" + slot, "nex-verity-" + slot
}

// slotTarget is one disk's copy of a slot.
type slotTarget struct {
	d            *Disk
	root, verity Partition
	rootSize     int64
	hash         string
}

func resolveSlot(d *Disk, slot string) (slotTarget, *Table, error) {
	t, err := d.ReadTable()
	if err != nil {
		return slotTarget{}, nil, err
	}
	rl, vl := slotLabels(slot)
	root, ok1 := t.Find(rl)
	verity, ok2 := t.Find(vl)
	if !ok1 || !ok2 {
		return slotTarget{}, nil, fmt.Errorf("%s: partitions %s and %s are not both present", d.Path, rl, vl)
	}
	return slotTarget{d: d, root: root, verity: verity}, t, nil
}

// WriteSlot writes the release's erofs and hash tree into one slot of every disk in S2 order:
//
//  1. give both slot partitions fresh random GUIDs on all disks, so that nothing, not even a stale
//     copy of the old release or a sibling disk's duplicate GUID, can match a roothash while the slot
//     is torn;
//  2. write root and hash tree, flush, and read them back from the medium to verify the whole
//     hash tree on every disk;
//  3. only then set the GUIDs derived from the roothash on all disks.
//
// A crash anywhere leaves the slot unselectable and the other slot untouched. booted is the root
// hash of the running system (empty at install time); the slot that holds it is refused.
func (w *Writer) WriteSlot(ctx context.Context, disks []*Disk, slot string, rel *Release, booted string) error {
	rootID, verityID, err := SlotIDs(rel.Manifest.RootHash)
	if err != nil {
		return err
	}
	erofsSt, err := os.Stat(rel.Erofs())
	if err != nil {
		return err
	}
	verityBytes, err := os.Stat(rel.Verity())
	if err != nil {
		return err
	}
	var targets []slotTarget
	for _, d := range disks {
		tg, t, err := resolveSlot(d, slot)
		if err != nil {
			return err
		}
		if booted != "" {
			if bootedRoot, _, err := SlotIDs(booted); err != nil {
				return err
			} else if tg.root.ID == bootedRoot {
				return fmt.Errorf("%s: slot %s holds the running system and is never rewritten", d.Path, slot)
			}
		}
		if erofsSt.Size() > t.Size(tg.root) || verityBytes.Size() > t.Size(tg.verity) {
			return fmt.Errorf("%s: release (%d + %d bytes) does not fit slot %s (%d + %d bytes)",
				d.Path, erofsSt.Size(), verityBytes.Size(), slot, t.Size(tg.root), t.Size(tg.verity))
		}
		tg.rootSize, tg.hash = erofsSt.Size(), rel.Manifest.RootHash
		targets = append(targets, tg)
	}
	w.logf("invalidating slot %s on %d disk(s)", slot, len(targets))
	for _, tg := range targets {
		if err = tg.d.SetPartitionIDs(map[int]GUID{tg.root.Index: NewGUID(), tg.verity.Index: NewGUID()}); err != nil {
			return err
		}
	}
	if err = w.settle(ctx); err != nil {
		return err
	}
	for _, tg := range targets {
		if err = w.writeAndVerify(tg, rel); err != nil {
			return err
		}
	}
	w.logf("slot %s verified on %d disk(s); selecting it by root hash %s", slot, len(targets), rel.Manifest.RootHash)
	for _, tg := range targets {
		if err = tg.d.SetPartitionIDs(map[int]GUID{tg.root.Index: rootID, tg.verity.Index: verityID}); err != nil {
			return err
		}
	}
	if err = w.settle(ctx); err != nil {
		return err
	}
	for _, tg := range targets {
		t, err := tg.d.ReadTable()
		if err != nil {
			return err
		}
		r, rok := t.ByIndex(tg.root.Index)
		v, vok := t.ByIndex(tg.verity.Index)
		if !rok || !vok || r.ID != rootID || v.ID != verityID {
			return fmt.Errorf("%s: slot %s GUIDs did not change", tg.d.Path, slot)
		}
	}
	return nil
}

func (w *Writer) writeAndVerify(tg slotTarget, rel *Release) error {
	t, err := tg.d.ReadTable()
	if err != nil {
		return err
	}
	for _, step := range []struct {
		part Partition
		src  string
		what string
	}{{tg.root, rel.Erofs(), "root"}, {tg.verity, rel.Verity(), "hash tree"}} {
		pio, err := openPart(tg.d, t, step.part)
		if err != nil {
			return err
		}
		w.logf("%s: writing %s to %s", tg.d.Path, step.what, step.part.Label)
		err = copyInto(pio, step.src)
		if cerr := pio.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("%s: write %s: %w", tg.d.Path, step.part.Label, err)
		}
	}
	return w.verifyPartitions(t, tg)
}

func copyInto(dst *partIO, srcPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	if _, err = io.CopyBuffer(io.NewOffsetWriter(dst, 0), io.LimitReader(src, dst.size), make([]byte, MiB)); err != nil {
		return err
	}
	return dst.Sync()
}

// verifyPartitions reads the slot back from the medium and checks the whole hash tree.
func (w *Writer) verifyPartitions(t *Table, tg slotTarget) error {
	d := tg.d
	root, err := openPart(d, t, tg.root)
	if err != nil {
		return err
	}
	defer root.Close()
	verity, err := openPart(d, t, tg.verity)
	if err != nil {
		return err
	}
	defer verity.Close()
	if err = dropCaches(root.f, root.block); err != nil {
		return err
	}
	if err = dropCaches(verity.f, verity.block); err != nil {
		return err
	}
	w.logf("%s: verifying %s", d.Path, tg.root.Label)
	if err = verifyVerity(root, tg.rootSize, verity, verity.size, tg.hash); err != nil {
		return fmt.Errorf("%s: slot %s read back wrong: %w", d.Path, tg.root.Label, err)
	}
	return nil
}
