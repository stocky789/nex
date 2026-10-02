// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ArmTries is the boot-assessment counter an armed UKI starts with (+3): three failed boots and
// systemd-boot stops preferring it.
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

var ukiNameParseRE = regexp.MustCompile(`^nex-core_(.+)_([0-9]+)(?:\+([0-9]+)(?:-([0-9]+))?)?\.efi$`)

// UKIEntry is one boot entry on an ESP: nex-core_<label>_<sequence>[+tries[-done]].efi.
type UKIEntry struct {
	Node     string // ESP partition node, empty for a bare name
	Name     string
	Label    string
	Sequence int
	Counted  bool // the name carries a boot counter: not blessed
	Tries    int  // tries left
	Done     int  // tries already used
}

// Bad is an entry whose tries are used up without a bless.
func (e UKIEntry) Bad() bool { return e.Counted && e.Tries == 0 }

// Blessed is an entry without a counter.
func (e UKIEntry) Blessed() bool { return !e.Counted }

// ParseUKIName decodes a UKI file name.
func ParseUKIName(name string) (UKIEntry, bool) {
	m := ukiNameParseRE.FindStringSubmatch(name)
	if m == nil {
		return UKIEntry{}, false
	}
	e := UKIEntry{Name: name, Label: m[1]}
	e.Sequence, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		e.Counted = true
		e.Tries, _ = strconv.Atoi(m[3])
		if m[4] != "" {
			e.Done, _ = strconv.Atoi(m[4])
		}
	}
	return e, true
}

func ukiName(label string, sequence int, suffix string) string {
	return fmt.Sprintf("nex-core_%s_%d%s.efi", label, sequence, suffix)
}

// BootOrder is the order systemd-boot tries the entries of one ESP: entries with tries left (or no
// counter) before exhausted ones, then the higher sequence first. sd-boot compares the UKI's os-release
// IMAGE_VERSION (VERSION when that is absent) as a version string, so image/release.py makes both the
// zero-padded sequence and nothing else: only then is that comparison the numeric sequence order modelled
// here, whatever the label looks like. The first element is the default entry.
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

// Host is this machine's disks and boot partitions for update operations. Nil functions use the
// sysfs discovery of the running system.
type Host struct {
	W         *Writer
	DiskPaths func() ([]string, error)
	ESPs      func() ([]string, error)
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
// on every disk (WriteSlot's S2 order). Before the slot is touched, every UKI on every ESP that does
// not belong to keepSequence (the running version) is removed: the UKI is the commit point and a
// stale one would name a slot that is about to be torn. It returns the slot written. Nothing is armed.
func (h *Host) StageSlot(ctx context.Context, rel *Release, bootedRootHash string, keepSequence int) (string, error) {
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
		if err = h.W.WithESP(ctx, n, func(root string) error { return pruneOtherUKIs(root, keepSequence) }); err != nil {
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

// pruneOtherUKIs removes every UKI that is not the keepSequence one, and half-written files.
func pruneOtherUKIs(root string, keepSequence int) error {
	if err := removeTemps(root); err != nil {
		return err
	}
	dir := filepath.Join(root, ukiDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		u, ok := ParseUKIName(e.Name())
		if !ok || u.Sequence == keepSequence {
			continue
		}
		if err = os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return syncDir(dir)
}

// readUKIs lists the UKIs of an ESP root.
func readUKIs(root, node string) ([]UKIEntry, error) {
	entries, err := os.ReadDir(filepath.Join(root, ukiDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []UKIEntry
	for _, e := range entries {
		if u, ok := ParseUKIName(e.Name()); ok {
			u.Node = node
			out = append(out, u)
		}
	}
	return out, nil
}

// Entries lists the UKIs of every ESP.
func (h *Host) Entries(ctx context.Context) ([]UKIEntry, error) {
	nodes, err := h.espNodes()
	if err != nil {
		return nil, err
	}
	var out []UKIEntry
	for _, n := range nodes {
		if err = h.W.WithESP(ctx, n, func(root string) error {
			us, err := readUKIs(root, n)
			out = append(out, us...)
			return err
		}); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
	}
	return out, nil
}

// requireSlot refuses unless every disk holds the release's slot, selected by its root hash.
func requireSlot(disks []*Disk, rootHash string) error {
	for _, d := range disks {
		if _, ok, err := slotSelected(d, rootHash); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("%s: no slot holds root hash %s", d.Path, rootHash)
		}
	}
	return nil
}

// Arm puts the release's UKI on every ESP as nex-core_<label>_<sequence>+3.efi and refreshes the
// loader files. The running version's UKI stays beside it as the fallback. Per ESP the UKI is
// written to a .new name (which the boot loader ignores), flushed, and renamed: the rename is the
// only moment the entry appears. The slot must already be staged on every disk; ESPs are armed one
// after the other, and an ESP that has not been reached boots its old, self-consistent slot.
//
// Arm is all or nothing as far as the disks allow: when an ESP fails, the entry is taken back from
// every ESP reached so far (the failed one included), so a failed Arm leaves the release staged. If
// that undo fails too, the error wraps ErrArmNotUndone and the caller must treat the release as armed.
func (h *Host) Arm(ctx context.Context, rel *Release) error {
	disks, err := h.openDisks()
	if err != nil {
		return err
	}
	err = requireSlot(disks, rel.Manifest.RootHash)
	closeAll(disks)
	if err != nil {
		return err
	}
	nodes, err := h.espNodes()
	if err != nil {
		return err
	}
	for i, n := range nodes {
		if err = h.W.WithESP(ctx, n, func(root string) error { return armESP(root, rel, ArmTries) }); err != nil {
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
		if err := h.W.WithESP(ctx, n, func(root string) error { return disarmESP(root, sequence) }); err != nil {
			failed = append(failed, fmt.Errorf("undo %s: %w", n, err))
		}
	}
	if len(failed) == 0 {
		return cause
	}
	return fmt.Errorf("%w: %w; %w", ErrArmNotUndone, cause, errors.Join(failed...))
}

// disarmESP is armESP's inverse for the boot entry: every UKI of the sequence (under whatever counter)
// and every half-written file goes, so the ESP boots what it booted before the Arm. The loader files
// armESP refreshed stay: they are the release's own and boot the old UKI as well.
func disarmESP(root string, sequence int) error {
	dir := filepath.Join(root, ukiDir)
	us, err := readUKIs(root, "")
	if err != nil {
		return err
	}
	for _, u := range us {
		if u.Sequence == sequence {
			if err = os.Remove(filepath.Join(dir, u.Name)); err != nil {
				return err
			}
		}
	}
	if err = removeTemps(root); err != nil {
		return err
	}
	if _, err = os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return syncDir(dir)
}

func armESP(root string, rel *Release, tries int) error {
	for _, f := range rel.ESPFiles() {
		if isUKI(f.Rel) {
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(f.Rel))
		want, err := sha256File(f.Path)
		if err != nil {
			return err
		}
		if got, err := sha256File(dst); err == nil && got == want {
			continue
		}
		if err = copyAtomic(f.Path, dst); err != nil {
			return fmt.Errorf("write %s: %w", f.Rel, err)
		}
	}
	dir := filepath.Join(root, ukiDir)
	dst := filepath.Join(dir, ukiName(rel.Manifest.Label, rel.Manifest.Sequence, fmt.Sprintf("+%d", tries)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := copyUKI(rel.UKIPath(), dst); err != nil {
		return err
	}
	// Any older copy of the same sequence under another counter (a bad entry of an earlier try)
	// is gone once the new entry exists.
	us, err := readUKIs(root, "")
	if err != nil {
		return err
	}
	for _, u := range us {
		if u.Sequence == rel.Manifest.Sequence && filepath.Join(dir, u.Name) != dst {
			if err = os.Remove(filepath.Join(dir, u.Name)); err != nil {
				return err
			}
		}
	}
	want, err := sha256File(rel.UKIPath())
	if err != nil {
		return err
	}
	if got, err := sha256File(dst); err != nil || got != want {
		return fmt.Errorf("verify %s after writing: not the expected bytes (%v)", path.Base(dst), err)
	}
	return syncDir(dir)
}

// copyUKI is copyAtomic with a crash point between the flushed temp file and the rename.
func copyUKI(src, dst string) error {
	tmp := dst + tempSuffix
	if err := copyFile(src, tmp); err != nil {
		return err
	}
	if err := crash("arm.temp"); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dst))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = out.ReadFrom(in); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// Rollback makes the retained previous version the next boot: on every ESP its UKI gets a fresh
// +3 counter first (the running version, with a higher sequence, still sorts first and boots if
// the host is interrupted here), and only then is the running version's UKI marked exhausted
// (+0-3) so the previous one sorts first. The previous slot must still be selected on every disk.
// A failure before any ESP changed leaves every disk as it was; one after it wraps ErrRollbackPartial.
// Repeating the call finishes the ESPs that are left.
func (h *Host) Rollback(ctx context.Context, fromSequence, toSequence int, toRootHash string) error {
	disks, err := h.openDisks()
	if err != nil {
		return err
	}
	err = requireSlot(disks, toRootHash)
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
			us, err := readUKIs(root, n)
			if err != nil {
				return err
			}
			for _, u := range us {
				if u.Sequence == toSequence {
					return nil
				}
			}
			return fmt.Errorf("no UKI of sequence %d to roll back to", toSequence)
		}); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	for i, n := range nodes {
		changed := false
		if err = h.W.WithESP(ctx, n, func(root string) (err error) {
			changed, err = rollbackESP(root, fromSequence, toSequence)
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

// Disarm takes the entry of an armed release back from every ESP, so that every disk boots what it booted
// before the Arm: the way out of an Arm that could not be undone (ErrArmNotUndone). Every ESP is tried; the
// error joins the ones that failed, and then the release may still boot.
func (h *Host) Disarm(ctx context.Context, sequence int) error {
	nodes, err := h.espNodes()
	if err != nil {
		return err
	}
	var failed []error
	for _, n := range nodes {
		if err = h.W.WithESP(ctx, n, func(root string) error { return disarmESP(root, sequence) }); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", n, err))
		}
	}
	return errors.Join(failed...)
}

// rollbackESP reports whether it renamed anything: an error with changed false left the ESP as it was.
func rollbackESP(root string, from, to int) (changed bool, err error) {
	dir := filepath.Join(root, ukiDir)
	us, err := readUKIs(root, "")
	if err != nil {
		return false, err
	}
	for _, u := range us {
		if u.Sequence != to {
			continue
		}
		dst := ukiName(u.Label, u.Sequence, fmt.Sprintf("+%d", ArmTries))
		if u.Name != dst {
			if err = os.Rename(filepath.Join(dir, u.Name), filepath.Join(dir, dst)); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	if err = crash("rollback.armed"); err != nil {
		return changed, err
	}
	for _, u := range us {
		if u.Sequence != from {
			continue
		}
		dst := ukiName(u.Label, u.Sequence, fmt.Sprintf("+0-%d", ArmTries))
		if u.Name != dst {
			if err = os.Rename(filepath.Join(dir, u.Name), filepath.Join(dir, dst)); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	return changed, syncDir(dir)
}

// Bless drops the boot counter of the UKI with the sequence, as systemd-bless-boot does once the
// running system has proved itself. It is used by the lab harness and by tests; on a host the
// service does it.
func Bless(root string, sequence int) error {
	dir := filepath.Join(root, ukiDir)
	us, err := readUKIs(root, "")
	if err != nil {
		return err
	}
	for _, u := range us {
		if u.Sequence == sequence && u.Counted {
			if err = os.Rename(filepath.Join(dir, u.Name), filepath.Join(dir, ukiName(u.Label, u.Sequence, ""))); err != nil {
				return err
			}
		}
	}
	return syncDir(dir)
}

// MarkTried decrements the boot counter of a UKI as the boot loader does when it boots the entry
// (+3 -> +2-1 ...). Lab harness and tests only.
func MarkTried(root string, sequence int) error {
	dir := filepath.Join(root, ukiDir)
	us, err := readUKIs(root, "")
	if err != nil {
		return err
	}
	for _, u := range us {
		if u.Sequence == sequence && u.Counted && u.Tries > 0 {
			suffix := fmt.Sprintf("+%d-%d", u.Tries-1, u.Done+1)
			if err = os.Rename(filepath.Join(dir, u.Name), filepath.Join(dir, ukiName(u.Label, u.Sequence, suffix))); err != nil {
				return err
			}
		}
	}
	return nil
}

// isBadUKI reports whether an ESP-relative path is a UKI with no tries left; the ESP sync ignores
// those when it decides whether the running entry is blessed.
func isBadUKI(rel string) bool {
	u, ok := ParseUKIName(path.Base(rel))
	return ok && u.Bad() && strings.HasSuffix(rel, ".efi")
}
