// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
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
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Paths the BIOS code reads, replaced by tests.
var (
	procCmdline = "/proc/cmdline"
	devByUUID   = "/dev/disk/by-uuid"
)

type biosBoot struct{}

func (biosBoot) Name() string { return "bios" }

// biosFile is one file the boot partition must hold. open streams its bytes: a file of the release's UKI is not
// a file of its own, so it is never copied to a temporary place first.
type biosFile struct {
	Rel  string
	Size int64
	SHA  string
	open func() (io.ReadCloser, error)
}

func bytesFile(rel string, b []byte) biosFile {
	sum := sha256.Sum256(b)
	return biosFile{Rel: rel, Size: int64(len(b)), SHA: hex.EncodeToString(sum[:]),
		open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(string(b))), nil }}
}

func diskFile(rel, p string) (biosFile, error) {
	sum, err := sha256File(p)
	if err != nil {
		return biosFile{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return biosFile{}, err
	}
	return biosFile{Rel: rel, Size: st.Size(), SHA: sum, open: func() (io.ReadCloser, error) { return os.Open(p) }}, nil
}

// releaseFiles is what a boot partition holds for a blessed single release in a slot: the menu, an empty counter
// block, the kernel and initrd out of the UKI and the entry.
func (r *Release) releaseFiles(slot string) ([]biosFile, error) {
	p, err := r.biosParts()
	if err != nil {
		return nil, err
	}
	entry, err := bootEntry{Label: r.Manifest.Label, Sequence: r.Manifest.Sequence, Cmdline: p.cmdline}.render()
	if err != nil {
		return nil, err
	}
	env, err := grubEnv{}.render()
	if err != nil {
		return nil, err
	}
	dir := payloadDir(r.Manifest.Sequence)
	section := func(name string, b blob) biosFile {
		return biosFile{Rel: dir + "/" + map[string]string{sectionLinux: "vmlinuz", sectionInitrd: "initrd"}[name], Size: b.size, SHA: b.sha,
			open: func() (io.ReadCloser, error) { return r.openSection(name) }}
	}
	return []biosFile{bytesFile(biosCfg, grubConfig()), bytesFile(biosEnv, env), section(sectionLinux, p.kernel),
		section(sectionInitrd, p.initrd), bytesFile(slotCfgPath(slot), entry)}, nil
}

// biosClass orders the writes of one convergence: payload and menu first, then the counters, then the entries.
func biosClass(rel string) int {
	switch {
	case rel == biosEnv:
		return 1
	case isSlotCfg(rel):
		return 2
	}
	return 0
}

// stageBlob writes f to its temp name beside its place, flushes it and checks the bytes that went in. The temp
// name ends in .new, which the menu ignores.
func stageBlob(root string, f biosFile) (dst string, err error) {
	dst = filepath.Join(root, filepath.FromSlash(f.Rel))
	if err = os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	in, err := f.open()
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := dst + tempSuffix
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(out, h), in, make([]byte, MiB))
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA) {
		err = fmt.Errorf("the source of %s is not the expected bytes", f.Rel)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

// writeBlob puts f in its place: staged, then renamed.
func writeBlob(root string, f biosFile) error {
	dst, err := stageBlob(root, f)
	if err != nil {
		return err
	}
	return os.Rename(dst+tempSuffix, dst)
}

// convergeBIOS makes the boot partition mounted at root hold every wanted file with the wanted bytes. The order
// keeps it bootable at every instant: payload and menu, then the counters, then the entries (an entry names
// files that are all there already); entries and payload that are no longer wanted go last. Files outside grub/
// grub.cfg, grub/grubenv and nex/ (the modules grub-install wrote) are left alone. Each file is verified by
// SHA-256 at the end.
func convergeBIOS(root string, want []biosFile) error {
	if err := removeTemps(root); err != nil {
		return err
	}
	wanted := map[string]biosFile{}
	for _, f := range want {
		wanted[f.Rel] = f
	}
	steps := []string{"files", "env", "entries"}
	for class := range steps {
		for _, f := range want {
			if biosClass(f.Rel) != class {
				continue
			}
			if sum, err := sha256File(filepath.Join(root, filepath.FromSlash(f.Rel))); err == nil && sum == f.SHA {
				continue
			}
			if err := writeBlob(root, f); err != nil {
				return fmt.Errorf("write %s: %w", f.Rel, err)
			}
		}
		if err := crash("converge." + steps[class]); err != nil {
			return err
		}
	}
	if err := pruneBIOS(root, wanted); err != nil {
		return err
	}
	for _, f := range want {
		got, err := sha256File(filepath.Join(root, filepath.FromSlash(f.Rel)))
		if err != nil || got != f.SHA {
			return fmt.Errorf("verify %s after writing: not the expected bytes (%v)", f.Rel, err)
		}
	}
	return syncTree(root)
}

// pruneBIOS removes slot entries first (the commit points), then every other file under nex/, that is not wanted,
// and the directories that end up empty.
func pruneBIOS(root string, wanted map[string]biosFile) error {
	base := filepath.Join(root, biosDir)
	var files, dirs []string
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir() && rel != biosDir:
			dirs = append(dirs, p)
		case !d.IsDir():
			if _, ok := wanted[rel]; !ok {
				files = append(files, rel)
			}
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sort.SliceStable(files, func(i, j int) bool { return isSlotCfg(files[i]) && !isSlotCfg(files[j]) })
	for _, rel := range files {
		if err = os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		if err = os.Remove(d); err != nil && !errors.Is(err, fs.ErrNotExist) && !isNotEmpty(err) {
			return err
		}
	}
	return nil
}

func isNotEmpty(err error) bool { return errors.Is(err, syscall.ENOTEMPTY) }

// scanBIOS lists the files a boot partition's convergence is about, as wanted state for another partition.
func scanBIOS(root string) ([]biosFile, error) {
	var out []biosFile
	add := func(rel string) error {
		f, err := diskFile(rel, filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err == nil {
			out = append(out, f)
		}
		return err
	}
	for _, rel := range []string{biosCfg, biosEnv} {
		if err := add(rel); err != nil {
			return nil, err
		}
	}
	err := filepath.WalkDir(filepath.Join(root, biosDir), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, tempSuffix) {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		return add(filepath.ToSlash(rel))
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return out, nil
}

// slotEntry is one slot's entry with its counter, as found on a boot partition.
type slotEntry struct {
	Slot    string
	Entry   bootEntry
	Tries   int
	Counted bool
}

// readSlots lists the entries of a boot partition and their counters.
func readSlots(root string) ([]slotEntry, grubEnv, error) {
	env, err := readGrubEnv(filepath.Join(root, filepath.FromSlash(biosEnv)))
	if err != nil {
		return nil, nil, err
	}
	var out []slotEntry
	for _, s := range biosSlots {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(slotCfgPath(s))))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		e, err := parseBootEntry(b)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", slotCfgPath(s), err)
		}
		se := slotEntry{Slot: s, Entry: e}
		if v, ok := env[triesVar(s)]; ok {
			se.Counted = true
			if se.Tries, err = strconv.Atoi(v); err != nil || se.Tries < 0 {
				return nil, nil, fmt.Errorf("%s=%q in %s is not a counter", triesVar(s), v, biosEnv)
			}
		}
		out = append(out, se)
	}
	return out, env, nil
}

func (s slotEntry) ukiEntry(node string) UKIEntry {
	u := UKIEntry{Node: node, Name: path.Base(slotCfgPath(s.Slot)), Label: s.Entry.Label, Sequence: s.Entry.Sequence, Counted: s.Counted}
	if s.Counted {
		u.Tries = s.Tries
		u.Done = max(ArmTries-s.Tries, 0)
	}
	return u
}

func (biosBoot) entries(root, node string) ([]UKIEntry, error) {
	slots, _, err := readSlots(root)
	var out []UKIEntry
	for _, s := range slots {
		out = append(out, s.ukiEntry(node))
	}
	return out, err
}

func (biosBoot) prune(root string, keepSequence int) error {
	if err := removeTemps(root); err != nil {
		return err
	}
	slots, env, err := readSlots(root)
	if err != nil {
		return err
	}
	for _, s := range slots {
		if s.Entry.Sequence == keepSequence {
			continue
		}
		if err = os.Remove(filepath.Join(root, filepath.FromSlash(slotCfgPath(s.Slot)))); err != nil {
			return err
		}
		delete(env, triesVar(s.Slot))
	}
	if err = writeGrubEnv(filepath.Join(root, filepath.FromSlash(biosEnv)), env); err != nil {
		return err
	}
	return removePayloadsExcept(root, keepSequence)
}

// removePayloadsExcept removes every kernel directory under nex/ but the keepSequence one.
func removePayloadsExcept(root string, keepSequence int) error {
	entries, err := os.ReadDir(filepath.Join(root, biosDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		m := payloadDirRE.FindStringSubmatch(biosDir + "/" + e.Name())
		if m == nil {
			continue
		}
		if seq, _ := strconv.Atoi(m[1]); seq != keepSequence {
			if err = os.RemoveAll(filepath.Join(root, biosDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return syncDir(filepath.Join(root, biosDir))
}

// arm puts the release's entry into the slot with tries boot tries. Order: kernel and initrd, the entry under a temp
// name (which the menu ignores), the counter, and only then the rename of the entry: the rename is the only
// moment the release can boot. An older entry of the slot keeps booting until then.
func (biosBoot) arm(root string, rel *Release, slot string, tries int) error {
	files, err := rel.releaseFiles(slot)
	if err != nil {
		return err
	}
	var entry biosFile
	for _, f := range files {
		switch {
		case f.Rel == biosEnv:
			// The counters stay as they are except for this slot's, below.
		case isSlotCfg(f.Rel):
			entry = f
		default:
			if sum, err := sha256File(filepath.Join(root, filepath.FromSlash(f.Rel))); err == nil && sum == f.SHA {
				continue
			}
			if err := writeBlob(root, f); err != nil {
				return fmt.Errorf("write %s: %w", f.Rel, err)
			}
		}
	}
	envPath := filepath.Join(root, filepath.FromSlash(biosEnv))
	env, err := readGrubEnv(envPath)
	if err != nil {
		return err
	}
	cfg, err := stageBlob(root, entry)
	if err != nil {
		return err
	}
	if err = crash("arm.temp"); err != nil {
		return err
	}
	env[triesVar(slot)] = strconv.Itoa(tries)
	if err = writeGrubEnv(envPath, env); err != nil {
		os.Remove(cfg + tempSuffix)
		return err
	}
	if err = crash("arm.counted"); err != nil {
		return err
	}
	if err = os.Rename(cfg+tempSuffix, cfg); err != nil {
		return err
	}
	// The same sequence in another slot can only be a stale copy of an earlier try.
	slots, env, err := readSlots(root)
	if err != nil {
		return err
	}
	for _, s := range slots {
		if s.Entry.Sequence == rel.Manifest.Sequence && s.Slot != slot {
			if err = os.Remove(filepath.Join(root, filepath.FromSlash(slotCfgPath(s.Slot)))); err != nil {
				return err
			}
			delete(env, triesVar(s.Slot))
			if err = writeGrubEnv(envPath, env); err != nil {
				return err
			}
		}
	}
	for _, f := range files {
		if f.Rel == biosEnv {
			continue
		}
		if got, err := sha256File(filepath.Join(root, filepath.FromSlash(f.Rel))); err != nil || got != f.SHA {
			return fmt.Errorf("verify %s after writing: not the expected bytes (%v)", f.Rel, err)
		}
	}
	return syncTree(root)
}

func (biosBoot) disarm(root string, sequence int) error {
	if err := removeTemps(root); err != nil {
		return err
	}
	slots, env, err := readSlots(root)
	if err != nil {
		return err
	}
	changed := false
	for _, s := range slots {
		if s.Entry.Sequence != sequence {
			continue
		}
		if err = os.Remove(filepath.Join(root, filepath.FromSlash(slotCfgPath(s.Slot)))); err != nil {
			return err
		}
		delete(env, triesVar(s.Slot))
		changed = true
	}
	if changed {
		if err = writeGrubEnv(filepath.Join(root, filepath.FromSlash(biosEnv)), env); err != nil {
			return err
		}
	}
	if err = os.RemoveAll(filepath.Join(root, filepath.FromSlash(payloadDir(sequence)))); err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(root, biosDir)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return syncDir(filepath.Join(root, biosDir))
}

// rollback follows rollbackESP: the previous entry gets a fresh counter first (the running one, with the higher
// sequence, still wins and boots if the host is interrupted here), then the running one is marked exhausted.
func (biosBoot) rollback(root string, from, to int) (changed bool, err error) {
	envPath := filepath.Join(root, filepath.FromSlash(biosEnv))
	slots, env, err := readSlots(root)
	if err != nil {
		return false, err
	}
	set := func(seq int, v string) error {
		for _, s := range slots {
			if s.Entry.Sequence == seq && env[triesVar(s.Slot)] != v {
				env[triesVar(s.Slot)] = v
				changed = true
				if err := writeGrubEnv(envPath, env); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err = set(to, strconv.Itoa(ArmTries)); err != nil {
		return changed, err
	}
	if err = crash("rollback.armed"); err != nil {
		return changed, err
	}
	return changed, set(from, "0")
}

// place writes a fresh boot partition: the menu, empty counters and the one release, blessed, in its slot.
func (biosBoot) place(root string, rel *Release, slot string) error {
	files, err := rel.releaseFiles(slot)
	if err != nil {
		return err
	}
	return convergeBIOS(root, files)
}

// blessed is false while an entry still has boot tries to spend: the running entry is not proven yet.
func (biosBoot) blessed(root string) (bool, error) {
	slots, _, err := readSlots(root)
	for _, s := range slots {
		if s.Counted && s.Tries > 0 {
			return false, err
		}
	}
	return true, err
}

// booted is the boot partition the menu loaded the running system from, found by the file system UUID the menu put
// on the kernel command line. It is empty when the command line has none (not started by the Nex menu).
func (biosBoot) booted() (string, error) {
	b, err := os.ReadFile(procCmdline)
	if err != nil {
		return "", err
	}
	uuid := cmdlineValue(string(b), bootFSArg)
	if uuid == "" {
		return "", nil
	}
	entries, err := os.ReadDir(devByUUID)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), uuid) {
			node, err := filepath.EvalSymlinks(filepath.Join(devByUUID, e.Name()))
			if err != nil {
				return "", fmt.Errorf("boot partition %s: %w", uuid, err)
			}
			return node, nil
		}
	}
	return "", fmt.Errorf("no partition with file system UUID %s: the boot partition is gone", uuid)
}

func (biosBoot) converge(ctx context.Context, w *Writer, node, dst, src string) error {
	files, err := scanBIOS(src)
	if err != nil {
		return err
	}
	if err = convergeBIOS(dst, files); err != nil {
		return err
	}
	disk, err := w.diskOfNode(node)
	if err != nil {
		return err
	}
	if loaderCurrent(disk, dst) {
		return nil
	}
	w.logf("installing the boot loader on %s", disk)
	return w.grubInstall(ctx, disk, dst)
}

// install puts GRUB on a disk: boot code in the MBR and the core image in the BIOS boot partition. A disk whose
// boot partition already holds the modules and whose MBR already holds the boot code is left as it is.
func (biosBoot) install(ctx context.Context, w *Writer, disk, root string) error {
	if loaderCurrent(disk, root) {
		return nil
	}
	return w.grubInstall(ctx, disk, root)
}

// grubInstall runs grub-install for the disk with the boot partition mounted at bootRoot. The core image is
// embedded in the nex-bios partition (GRUB finds it by type); the modules land beside the menu. With GrubChroot
// the tool runs chrooted: the boot partition mount is bound into the chroot at the same path first.
func (w *Writer) grubInstall(ctx context.Context, disk, bootRoot string) error {
	args := []string{"grub-install", "--target=i386-pc", "--boot-directory=" + bootRoot, "--modules=part_gpt fat", "--no-floppy", disk}
	if w.GrubChroot == "" {
		return w.Runner.Run(ctx, args[0], args[1:]...)
	}
	inside := filepath.Join(w.GrubChroot, bootRoot)
	if err := os.MkdirAll(inside, 0o755); err != nil {
		return err
	}
	if err := w.Runner.Run(ctx, "mount", "--bind", bootRoot, inside); err != nil {
		return err
	}
	err := w.Runner.Run(ctx, "chroot", append([]string{w.GrubChroot}, args...)...)
	if uerr := w.Runner.Run(ctx, "umount", inside); err == nil {
		err = uerr
	}
	return err
}

// loaderCurrent reports whether the disk has GRUB boot code and the partition has its modules: what a replaced
// or blank disk lacks.
func loaderCurrent(disk, bootRoot string) bool {
	if _, err := os.Stat(filepath.Join(bootRoot, filepath.FromSlash(biosModules), "normal.mod")); err != nil {
		return false
	}
	f, err := os.Open(disk)
	if err != nil {
		return false
	}
	defer f.Close()
	var mbr [446]byte
	if _, err = io.ReadFull(f, mbr[:]); err != nil {
		return false
	}
	return hasGrubBootCode(mbr[:])
}

// hasGrubBootCode recognises GRUB's boot.img in the first 446 bytes of an MBR: its jump and its "GRUB " message.
func hasGrubBootCode(mbr []byte) bool {
	return len(mbr) >= 440 && mbr[0] == 0xeb && mbr[2] == 0x90 && strings.Contains(string(mbr[:440]), "GRUB ")
}

var errNotBIOSBooted = errors.New("the running system was not started by the Nex boot menu (no " + bootFSArg + "= on its command line)")

// runningSlot is the slot the running system was started from: the nex.slot= word the menu added, checked
// against the root hash on the command line and in the entry.
func runningSlot(root string) (slotEntry, error) {
	b, err := os.ReadFile(procCmdline)
	if err != nil {
		return slotEntry{}, err
	}
	cmdline := string(b)
	slot, hash := cmdlineValue(cmdline, bootSlotArg), cmdlineValue(cmdline, "roothash")
	slots, _, err := readSlots(root)
	if err != nil {
		return slotEntry{}, err
	}
	for _, s := range slots {
		if s.Slot == slot && cmdlineRootHash(s.Entry.Cmdline) == hash {
			return s, nil
		}
	}
	return slotEntry{}, fmt.Errorf("no boot entry in slot %q holds the running root hash %q", slot, hash)
}

func (biosBoot) bless(ctx context.Context, w *Writer, good bool) error {
	node, err := biosBoot{}.booted()
	if err != nil {
		return err
	}
	if node == "" {
		return errNotBIOSBooted
	}
	return w.WithESP(ctx, node, func(root string) error {
		s, err := runningSlot(root)
		if err != nil {
			return err
		}
		envPath := filepath.Join(root, filepath.FromSlash(biosEnv))
		env, err := readGrubEnv(envPath)
		if err != nil {
			return err
		}
		if good {
			if _, counted := env[triesVar(s.Slot)]; !counted {
				return nil
			}
			delete(env, triesVar(s.Slot))
		} else {
			env[triesVar(s.Slot)] = "0"
		}
		return writeGrubEnv(envPath, env)
	})
}

func (biosBoot) tries(ctx context.Context, w *Writer) (left int, counted bool, err error) {
	node, err := biosBoot{}.booted()
	if err != nil {
		return 0, false, err
	}
	if node == "" {
		return 0, false, errNotBIOSBooted
	}
	err = w.WithESP(ctx, node, func(root string) error {
		s, err := runningSlot(root)
		left, counted = s.Tries, s.Counted
		return err
	})
	return left, counted, err
}
