// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ArmTries is the boot-assessment counter an armed entry starts with: after three failed boots the menu
// stops preferring it.
const ArmTries = 3

// crashPoint is nil in production. Tests set it to stop an operation at a named point, which stands
// for a power cut there: the function returns without undoing anything.
var crashPoint func(point string) error

// powerCut marks an error from a crash point: the process is gone at that moment, so nothing runs
// after it, undo included.
type powerCut struct{ error }

func (p powerCut) Unwrap() error { return p.error }

func crash(point string) error {
	if crashPoint != nil {
		if err := crashPoint(point); err != nil {
			return powerCut{err}
		}
	}
	return nil
}

// ErrArmNotUndone is wrapped by the error of an Arm that failed after it had touched an ESP and then
// could not take its entry back from every ESP it reached: some disk may still boot the release.
var ErrArmNotUndone = errors.New("arm failed and could not be undone on every ESP")

// ErrRollbackPartial is wrapped by the error of a Rollback that failed after it had changed an ESP: some
// disk may boot the previous release already and the others the running one. An error without it left every
// ESP as it was.
var ErrRollbackPartial = errors.New("rollback failed after it changed an ESP")

// UKIEntry is one boot entry on a boot partition: a slot's entry and its counter.
type UKIEntry struct {
	Node     string // ESP partition node, empty for a bare name
	Name     string
	Label    string
	Sequence int
	Counted  bool // the entry has a boot counter: not blessed
	Tries    int  // tries left
	Done     int  // tries already used
}

// Bad is an entry whose tries are used up without a bless.
func (e UKIEntry) Bad() bool { return e.Counted && e.Tries == 0 }

// Blessed is an entry without a counter.
func (e UKIEntry) Blessed() bool { return !e.Counted }

// Host is this machine's disks and boot partitions for update operations. Nil functions use the
// sysfs discovery of the running system.
type Host struct {
	W         *Writer
	DiskPaths func() ([]string, error)
	ESPs      func() ([]string, error)
	// Running is the whole-disk node the running system started from; nil asks the boot method (RunningDisk).
	Running func() (string, error)
}

func (h *Host) espNodes() ([]string, error) {
	if h.ESPs != nil {
		return h.ESPs()
	}
	return ESPNodes()
}

func (h *Host) diskPaths() ([]string, error) {
	if h.DiskPaths != nil {
		return h.DiskPaths()
	}
	nodes, err := ESPNodes()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range nodes {
		d, err := diskOf(n)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// diskOf is the whole-disk node of a partition node: the parent directory of the partition in sysfs.
func diskOf(node string) (string, error) {
	real, err := filepath.EvalSymlinks(filepath.Join(sysClassBlock, filepath.Base(node)))
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(real, "partition")); err != nil {
		return "", fmt.Errorf("%s is not a partition", node)
	}
	return filepath.Join(devDir, filepath.Base(filepath.Dir(real))), nil
}

func (h *Host) openDisks() ([]*Disk, error) {
	paths, err := h.diskPaths()
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("no image disks found")
	}
	var disks []*Disk
	for _, p := range paths {
		d, err := OpenDisk(p, true)
		if err != nil {
			closeAll(disks)
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		disks = append(disks, d)
	}
	return disks, nil
}

// slotOf is the slot ("a" or "b") of the disk whose root partition carries the GUID, and the other one.
func slotOf(d *Disk, root GUID) (have, other string, err error) {
	t, err := d.ReadTable()
	if err != nil {
		return "", "", err
	}
	var found []string
	for _, s := range []string{"a", "b"} {
		rl, _ := slotLabels(s)
		if p, ok := t.Find(rl); ok && p.ID == root {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		return "", "", fmt.Errorf("%s: the running root hash is held by %d slots, not exactly one", d.Path, len(found))
	}
	if found[0] == "a" {
		return "a", "b", nil
	}
	return "b", "a", nil
}

// slotSelected reports whether both slot partitions of the disk carry the GUIDs of the root hash.
func slotSelected(d *Disk, rootHash string) (string, bool, error) {
	rootID, verityID, err := SlotIDs(rootHash)
	if err != nil {
		return "", false, err
	}
	t, err := d.ReadTable()
	if err != nil {
		return "", false, err
	}
	for _, s := range []string{"a", "b"} {
		rl, vl := slotLabels(s)
		r, ok1 := t.Find(rl)
		v, ok2 := t.Find(vl)
		if ok1 && ok2 && r.ID == rootID && v.ID == verityID {
			return s, true, nil
		}
	}
	return "", false, nil
}

// StageSlot makes room for the release and writes it into the slot the running system does not use,
// on every disk (WriteSlot's S2 order). Before the slot is touched, every entry on every ESP that does
// not belong to keepSequence (the running version) is removed: the entry is the commit point and a
// stale one would name a slot that is about to be torn. It returns the slot written. Nothing is armed.
func (h *Host) StageSlot(ctx context.Context, rel *Release, bootedRootHash string, keepSequence int) (string, error) {
	unlock, err := lockDisks(ctx, diskLockWait)
	if err != nil {
		return "", err
	}
	defer unlock()
	bootedID, _, err := SlotIDs(bootedRootHash)
	if err != nil {
		return "", fmt.Errorf("running root hash: %w", err)
	}
	disks, err := h.openDisks()
	if err != nil {
		return "", err
	}
	defer closeAll(disks)
	target := ""
	for _, d := range disks {
		_, other, err := slotOf(d, bootedID)
		if err != nil {
			return "", err
		}
		if target != "" && target != other {
			return "", fmt.Errorf("%s: the disks disagree on which slot is running", d.Path)
		}
		target = other
	}
	nodes, err := h.espNodes()
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		if err = h.W.WithESP(ctx, n, func(root string) error { return h.W.method().prune(root, keepSequence) }); err != nil {
			return "", fmt.Errorf("%s: %w", n, err)
		}
	}
	if err = crash("stage.pruned"); err != nil {
		return "", err
	}
	if err = h.W.WriteSlot(ctx, disks, target, rel, bootedRootHash); err != nil {
		return "", err
	}
	return target, nil
}

// Entries lists the boot entries of every ESP.
func (h *Host) Entries(ctx context.Context) ([]UKIEntry, error) {
	nodes, err := h.espNodes()
	if err != nil {
		return nil, err
	}
	var out []UKIEntry
	for _, n := range nodes {
		if err = h.W.WithESP(ctx, n, func(root string) error {
			us, err := h.W.method().entries(root, n)
			out = append(out, us...)
			return err
		}); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
	}
	return out, nil
}

// requireSlot refuses unless every disk holds the release's slot, selected by its root hash, and returns that slot:
// the disks must agree on it, the boot entry names it.
func requireSlot(disks []*Disk, rootHash string) (string, error) {
	slot := ""
	for _, d := range disks {
		s, ok, err := slotSelected(d, rootHash)
		if err != nil {
			return "", err
		} else if !ok {
			return "", fmt.Errorf("%s: no slot holds root hash %s", d.Path, rootHash)
		}
		if slot != "" && s != slot {
			return "", fmt.Errorf("%s: the disks disagree on the slot that holds root hash %s", d.Path, rootHash)
		}
		slot = s
	}
	return slot, nil
}

// Arm puts the release's entry on every ESP with ArmTries boot tries (on UEFI it refreshes the loader files
// first). The running version's entry stays beside it as the fallback. Per ESP the entry is written to a
// .new name (which the menu ignores), flushed, and renamed: the rename is the only moment the entry appears.
// The slot must already be staged on every disk; ESPs are armed one after the other, and an ESP that has not
// been reached boots its old, self-consistent slot.
//
// Arm is all or nothing as far as the disks allow: when an ESP fails, the entry is taken back from
// every ESP reached so far (the failed one included), so a failed Arm leaves the release staged. If
// that undo fails too, the error wraps ErrArmNotUndone and the caller must treat the release as armed.
func (h *Host) Arm(ctx context.Context, rel *Release) error {
	unlock, err := lockDisks(ctx, diskLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	m := h.W.method()
	disks, err := h.openDisks()
	if err != nil {
		return err
	}
	slot, err := requireSlot(disks, rel.Manifest.RootHash)
	closeAll(disks)
	if err != nil {
		return err
	}
	nodes, err := h.espNodes()
	if err != nil {
		return err
	}
	for i, n := range nodes {
		if err = h.W.WithESP(ctx, n, func(root string) error { return m.arm(root, rel, slot, ArmTries) }); err != nil {
			return h.undoArm(ctx, rel.Manifest.Sequence, nodes[:i+1], fmt.Errorf("%s: %w", n, err))
		}
		h.W.logf("armed %s", n)
		if err = crash("arm.esp-done"); err != nil {
			return err
		}
	}
	return nil
}

// undoArm takes the entry of a failed Arm back from the ESPs it reached and returns cause, or cause
// wrapped with ErrArmNotUndone when an ESP could not be cleaned. A power cut is not undone: nothing
// runs after it. The undo does not stop for a cancelled ctx: leaving the entry behind is the worse end.
func (h *Host) undoArm(ctx context.Context, sequence int, nodes []string, cause error) error {
	var pc powerCut
	if errors.As(cause, &pc) {
		return cause
	}
	ctx = context.WithoutCancel(ctx)
	var failed []error
	for _, n := range nodes {
		if err := h.W.WithESP(ctx, n, func(root string) error { return h.W.method().disarm(root, sequence) }); err != nil {
			failed = append(failed, fmt.Errorf("undo %s: %w", n, err))
		}
	}
	if len(failed) == 0 {
		return cause
	}
	return fmt.Errorf("%w: %w; %w", ErrArmNotUndone, cause, errors.Join(failed...))
}

// Rollback makes the retained previous version the next boot: on every ESP its entry gets a fresh
// counter first (the running version, with a higher sequence, still wins and boots if the host is
// interrupted here), and only then are the running version's tries set to 0 so the previous one wins. The
// previous slot must still be selected on every disk.
// A failure before any ESP changed leaves every disk as it was; one after it wraps ErrRollbackPartial.
// Repeating the call finishes the ESPs that are left.
func (h *Host) Rollback(ctx context.Context, fromSequence, toSequence int, toRootHash string) error {
	unlock, err := lockDisks(ctx, diskLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	m := h.W.method()
	disks, err := h.openDisks()
	if err != nil {
		return err
	}
	_, err = requireSlot(disks, toRootHash)
	closeAll(disks)
	if err != nil {
		return err
	}
	nodes, err := h.espNodes()
	if err != nil {
		return err
	}
	// Check every ESP before touching any.
	for _, n := range nodes {
		if err = h.W.WithESP(ctx, n, func(root string) error {
			us, err := m.entries(root, n)
			if err != nil {
				return err
			}
			for _, u := range us {
				if u.Sequence == toSequence {
					return nil
				}
			}
			return fmt.Errorf("no boot entry of sequence %d to roll back to", toSequence)
		}); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	for i, n := range nodes {
		changed := false
		if err = h.W.WithESP(ctx, n, func(root string) (err error) {
			changed, err = m.rollback(root, fromSequence, toSequence)
			return err
		}); err != nil {
			err = fmt.Errorf("%s: %w", n, err)
			var pc powerCut
			if (i > 0 || changed) && !errors.As(err, &pc) {
				return fmt.Errorf("%w: %w", ErrRollbackPartial, err)
			}
			return err
		}
		if err = crash("rollback.esp-done"); err != nil {
			return err
		}
	}
	return nil
}

// Disarm takes the entry of an armed release back from every boot partition, so that every disk boots what it booted
// before the Arm: the way out of an Arm that could not be undone (ErrArmNotUndone). Every one is tried; the
// error joins the ones that failed, and then the release may still boot.
func (h *Host) Disarm(ctx context.Context, sequence int) error {
	unlock, err := lockDisks(ctx, diskLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	nodes, err := h.espNodes()
	if err != nil {
		return err
	}
	var failed []error
	for _, n := range nodes {
		if err = h.W.WithESP(ctx, n, func(root string) error { return h.W.method().disarm(root, sequence) }); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", n, err))
		}
	}
	return errors.Join(failed...)
}
