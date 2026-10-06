// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

// Replacing a failed system disk. When one disk of a mirrored host dies and a blank one takes its place, Core lays
// the surviving disk's partition table on it, copies the surviving disk's verified slots, writes its boot partition
// through the host's boot method (installing the boot loader on the way) and finally adds its nex-data partition to
// the md mirror, which the kernel resyncs in the background. The disk the host runs from is only ever read.
//
// Every step looks at what the replacement disk really holds before it writes, and the record the operation keeps
// (the only state outside the disks) lets a repeated or interrupted request continue instead of repeating a
// destructive step. The partition table's own disk GUID is what says "this disk is the one the operation laid".

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Phases of a replacement, in the order they run, and the statuses of its record.
const (
	ReplaceAccepted = "accepted"
	ReplaceLaying   = "laying"
	ReplaceSlots    = "slots"
	ReplaceBoot     = "boot"
	ReplaceData     = "data"
	ReplaceComplete = "complete"

	ReplaceRunning   = "running"
	ReplaceSucceeded = "succeeded"
	ReplaceFailed    = "failed"
)

// mdSysBlock is where the kernel describes the md arrays; tests replace it.
var mdSysBlock = "/sys/block"

// ReplaceRecord is the durable record of the latest replacement.
type ReplaceRecord struct {
	RequestID string `json:"request_id"`
	// ByID, Serial and SizeBytes name the replacement disk as the operator selected it.
	ByID      string `json:"by_id"`
	Serial    string `json:"serial"`
	SizeBytes uint64 `json:"size_bytes"`
	// Erase is the operator's confirmation to erase this exact disk if it is not blank.
	Erase bool `json:"erase,omitempty"`
	// DiskID is the GPT disk GUID the operation writes: a disk carrying it is the one the operation laid.
	// PreviousID is the disk GUID the replacement carried when its erasure was authorized.
	DiskID     string `json:"disk_id"`
	PreviousID string `json:"previous_disk_id,omitempty"`
	// Formatted: the boot partition was formatted by this operation, so a continuation must not format it again.
	Formatted bool `json:"boot_formatted,omitempty"`
	// BootWritten: the boot partition was converged and the boot loader installed.
	BootWritten bool       `json:"boot_written,omitempty"`
	Phase       string     `json:"phase"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// ReadReplaceRecord reads the record at path; a host that never replaced a disk has none.
func ReadReplaceRecord(path string) (*ReplaceRecord, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r ReplaceRecord
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

func (r *ReplaceRecord) save(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + tempSuffix
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

// ReplaceRequest selects the replacement disk. Target is its whole-disk node, which the caller resolved from the
// stable identity in ByID after it checked that identity, the serial and the size against the live disk.
type ReplaceRequest struct {
	RequestID      string
	ByID, Serial   string
	SizeBytes      uint64
	Target         string
	Erase          bool   // the operator confirmed erasing this exact disk
	BootedRootHash string // the root hash of the running system
}

// refusal is an error raised before this attempt wrote anything.
type refusal struct{ msg string }

func (r *refusal) Error() string { return r.msg + "; this attempt changed nothing" }

func refuse(format string, args ...any) error { return &refusal{fmt.Sprintf(format, args...)} }

// RunningDisk is the whole-disk node the running system started from.
func (h *Host) RunningDisk() (string, error) {
	if h.Running != nil {
		return h.Running()
	}
	node, err := h.W.method().booted()
	if err != nil {
		return "", err
	}
	if node == "" {
		return "", errors.New("the running system was not started by the Nex boot loader, so the disk it runs from is unknown")
	}
	return diskOf(node)
}

// ReplaceDisk rebuilds the host's system disk onto the replacement disk and records its progress at journal. A request
// that repeats a recorded one replays its outcome; one that continues an unfinished replacement of the same disk
// resumes it. An error that is a power cut or a cancelled ctx leaves the record running: the next request
// continues it. Every other failure is recorded as the outcome.
func (h *Host) ReplaceDisk(ctx context.Context, journal string, req ReplaceRequest) error {
	unlock, err := lockDisks(ctx, diskLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	rec, err := beginReplace(journal, req)
	if err != nil {
		return err
	}
	switch rec.Status {
	case ReplaceSucceeded:
		return nil
	case ReplaceFailed:
		return errors.New(rec.Error)
	}
	err = h.replace(ctx, journal, rec, req)
	var pc powerCut
	if err != nil && (ctx.Err() != nil || errors.As(err, &pc)) {
		return err
	}
	now := time.Now().UTC()
	rec.FinishedAt = &now
	if err != nil {
		rec.Status, rec.Error = ReplaceFailed, err.Error()
	} else {
		rec.Status, rec.Phase, rec.Error = ReplaceSucceeded, ReplaceComplete, ""
	}
	if serr := rec.save(journal); serr != nil {
		return errors.Join(err, fmt.Errorf("the system disk replacement record could not be written: %w", serr))
	}
	return err
}

// AbandonReplace turns an unfinished replacement into a failed one with the given reason: its disk is gone, so
// nothing can continue it, and while it stays running no replacement of another disk is accepted. The record of a
// replacement that is not running is left as it is.
func AbandonReplace(journal, reason string) error {
	unlock, err := lockDisks(context.Background(), diskLockWait)
	if err != nil {
		return err
	}
	defer unlock()
	rec, err := ReadReplaceRecord(journal)
	if err != nil || rec == nil || rec.Status != ReplaceRunning {
		return err
	}
	now := time.Now().UTC()
	rec.Status, rec.Error, rec.FinishedAt = ReplaceFailed, reason, &now
	return rec.save(journal)
}

// beginReplace decides what a request means against the record: the same request continues or replays, another
// request for the disk an unfinished or interrupted-after-writing replacement is about continues it, anything else
// starts a new one. A replacement of another disk while one runs is refused.
func beginReplace(journal string, req ReplaceRequest) (*ReplaceRecord, error) {
	old, err := ReadReplaceRecord(journal)
	if err != nil {
		return nil, fmt.Errorf("the system disk replacement record is unreadable: %w", err)
	}
	if old != nil && old.RequestID == req.RequestID {
		if old.ByID != req.ByID {
			return nil, refuse("request %s was already used for another replacement disk", req.RequestID)
		}
		return old, nil
	}
	if old != nil && old.Status == ReplaceRunning && old.ByID != req.ByID {
		return nil, refuse("a replacement onto %s is still running", old.ByID)
	}
	rec := old
	switch {
	case old != nil && old.Status == ReplaceRunning,
		old != nil && old.Status == ReplaceFailed && old.ByID == req.ByID && old.Phase != ReplaceAccepted:
		rec.Status, rec.Error, rec.FinishedAt = ReplaceRunning, "", nil
	default:
		rec = &ReplaceRecord{DiskID: NewGUID().String(), Phase: ReplaceAccepted, Status: ReplaceRunning, StartedAt: time.Now().UTC()}
	}
	rec.RequestID, rec.ByID, rec.Serial, rec.SizeBytes = req.RequestID, req.ByID, req.Serial, req.SizeBytes
	rec.Erase = rec.Erase || req.Erase
	return rec, rec.save(journal)
}

func (h *Host) replace(ctx context.Context, journal string, rec *ReplaceRecord, req ReplaceRequest) error {
	w := *h.W
	m := w.method()
	step := func(phase string) error {
		rec.Phase = phase
		return rec.save(journal)
	}
	if err := crash("replace.accepted"); err != nil {
		return err
	}

	runningPath, err := h.RunningDisk()
	if err != nil {
		return refuse("%v", err)
	}
	src, err := installDisk(runningPath, false)
	if err != nil {
		return refuse("the disk the host runs from cannot be read: %v", err)
	}
	defer src.Close()
	// The disk is looked at read-only first: a wrong selection of the disk the host runs from must never get a
	// writable handle.
	probe, err := installDisk(req.Target, false)
	if err != nil {
		return refuse("the replacement disk cannot be opened: %v", err)
	}
	same, size := sameDisk(src, probe), probe.Size
	probe.Close()
	if same {
		return refuse("%s is the disk the running system started from", req.ByID)
	}
	if uint64(size) != req.SizeBytes {
		return refuse("the replacement disk is %d bytes, not the %d selected", size, req.SizeBytes)
	}
	tgt, err := installDisk(req.Target, true)
	if err != nil {
		return refuse("the replacement disk cannot be opened for writing: %v", err)
	}
	defer tgt.Close()
	// Writes go to the node the kernel names, whatever alias the caller used.
	w.nodeDisk = func(string) (string, error) { return tgt.Path, nil }
	dataNode := NodeName(tgt.Path, DataIndex)

	arr, err := readDataArray()
	if err != nil {
		return refuse("%v", err)
	}
	state, added := arr.member(filepath.Base(dataNode))
	switch {
	case added && memberFaulty(state):
		return refuse("the replacement disk's member of %s has failed; choose a different replacement disk", mdDevice())
	case !added && arr.degraded == 0:
		return refuse("%s is not degraded: there is no disk to replace", mdDevice())
	case !added && (arr.action == "recover" || arr.action == "resync" || arr.action == "reshape"):
		return refuse("%s is still %s; wait for it to finish before replacing a disk", mdDevice(), arr.action)
	}

	srcTable, err := src.ReadTable()
	if err != nil {
		return refuse("the disk the host runs from has no readable partition table: %v", err)
	}
	plan, err := planOf(srcTable)
	if err != nil {
		return refuse("%s: %v", src.Path, err)
	}
	if src.SectorSize != tgt.SectorSize {
		return refuse("the replacement disk has %d-byte sectors, the disk the host runs from %d", tgt.SectorSize, src.SectorSize)
	}
	var end int64
	for _, p := range plan {
		end = max(end, p.Offset+p.Size)
	}
	if end+backupReserve > tgt.Size {
		return refuse("the replacement disk (%d bytes) is too small for the layout of the disk the host runs from (%d bytes needed)", tgt.Size, end+backupReserve)
	}
	slots, err := w.sourceSlots(src, srcTable, req.BootedRootHash)
	if err != nil {
		return refuse("%v", err)
	}
	srcNode, dstNode := NodeName(src.Path, ESPIndex), NodeName(tgt.Path, ESPIndex)
	if err = w.WithESP(ctx, srcNode, func(root string) error {
		if ok, err := m.blessed(root); err != nil {
			return err
		} else if !ok {
			return errNotBlessed
		}
		return nil
	}); errors.Is(err, errNotBlessed) {
		return refuse("the running boot entry is not blessed yet: wait until the host has proven its boot, then retry")
	} else if err != nil {
		return refuse("the boot partition of the disk the host runs from cannot be read: %v", err)
	}
	laid, err := h.prepareTarget(journal, rec, tgt, plan, added)
	if err != nil {
		return err
	}

	if !laid {
		// Whatever an earlier attempt formatted or wrote on a disk that has to be laid again is gone.
		rec.Formatted, rec.BootWritten = false, false
		if err = step(ReplaceLaying); err != nil {
			return err
		}
		if err = crash("replace.laying"); err != nil {
			return err
		}
		w.logf("laying the partition table of %s on %s", src.Path, tgt.Path)
		id, _ := ParseGUID(rec.DiskID)
		if err = layPlan(ctx, w.Runner, tgt, plan, id); err != nil {
			return err
		}
	}
	if err = crash("replace.laid"); err != nil {
		return err
	}

	if err = step(ReplaceSlots); err != nil {
		return err
	}
	for _, slot := range []string{"a", "b"} {
		data, ok := slots[slot]
		if !ok {
			continue
		}
		if selected, ok, err := slotSelected(tgt, data.hash); err != nil {
			return err
		} else if ok && selected == slot {
			w.logf("slot %s already holds %s on %s", slot, data.hash, tgt.Path)
			continue
		}
		if err = w.writeSlot(ctx, []*Disk{tgt}, slot, data, ""); err != nil {
			return err
		}
		if err = crash("replace.slot"); err != nil {
			return err
		}
	}

	if err = step(ReplaceBoot); err != nil {
		return err
	}
	if err = h.bootPartition(ctx, &w, journal, rec, srcNode, dstNode, tgt); err != nil {
		return err
	}

	if err = step(ReplaceData); err != nil {
		return err
	}
	if !added {
		w.logf("adding %s to %s", dataNode, mdDevice())
		if err = w.Runner.Run(ctx, "mdadm", "--add", mdDevice(), dataNode); err != nil {
			return err
		}
		if err = crash("replace.added"); err != nil {
			return err
		}
	}
	if state, ok := arr.member(filepath.Base(dataNode)); !ok {
		return fmt.Errorf("%s is not a member of %s after it was added", dataNode, mdDevice())
	} else if memberFaulty(state) {
		return fmt.Errorf("the replacement disk's member of %s has failed", mdDevice())
	}
	return nil
}

// prepareTarget classifies the replacement disk and reports whether this operation already laid it. A disk that
// carries the operation's disk GUID is ours; one that holds a partition table, or data without one, is only erased
// when the operator confirmed it (now, or earlier in the same operation); a blank one is laid. A member of the
// mirror is always ours: the kernel would not hold anything else.
func (h *Host) prepareTarget(journal string, rec *ReplaceRecord, tgt *Disk, plan []Planned, member bool) (laid bool, err error) {
	t, err := tgt.ReadTable()
	switch {
	case err == nil && t.DiskID.String() == rec.DiskID:
		if err := sameLayout(t, plan); err != nil {
			return false, refuse("the replacement disk carries this operation's partition table but %v", err)
		}
		return true, h.ensureNodes(tgt, t)
	case member:
		return false, refuse("the replacement disk is a member of %s but does not carry this operation's partition table", mdDevice())
	case err == nil:
		if rec.PreviousID != t.DiskID.String() {
			if !rec.Erase {
				return false, refuse("the replacement disk holds a partition table; confirm erasing %s to replace the system disk onto it", rec.ByID)
			}
			rec.PreviousID = t.DiskID.String()
			if err = rec.save(journal); err != nil {
				return false, err
			}
		}
	case errors.Is(err, ErrNoGPT):
		blank, berr := tgt.blank()
		if berr != nil {
			return false, berr
		}
		if !blank && !rec.Erase && rec.PreviousID == "" {
			return false, refuse("the replacement disk holds data; confirm erasing %s to replace the system disk onto it", rec.ByID)
		}
	default:
		return false, err
	}
	if err := validateOffline([]*Disk{tgt}); err != nil {
		return false, refuse("%v", err)
	}
	return false, nil
}

// ensureNodes makes sure the kernel carries the partitions of a disk this operation laid: a power cut during the
// first table write can leave the kernel without them.
func (h *Host) ensureNodes(d *Disk, t *Table) error {
	if !d.block {
		return nil
	}
	if _, err := os.Stat(NodeName(d.Path, len(t.Parts))); err == nil {
		return nil
	}
	if err := d.WriteTable(t); err != nil {
		return err
	}
	if err := d.Reread(); err != nil {
		return err
	}
	return waitNodes(context.Background(), d, len(t.Parts))
}

// bootPartition formats the replacement's boot partition once, converges it from the booted one through the boot
// method and installs the boot loader.
func (h *Host) bootPartition(ctx context.Context, w *Writer, journal string, rec *ReplaceRecord, srcNode, dstNode string, tgt *Disk) error {
	if rec.BootWritten {
		return nil
	}
	m := w.method()
	unlock, err := lockESP(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if !rec.Formatted {
		w.logf("formatting the boot partition %s", dstNode)
		if err = w.Runner.Run(ctx, "mkfs.fat", "-F", "32", "-n", "NEXESP", dstNode); err != nil {
			return err
		}
		rec.Formatted = true
		if err = rec.save(journal); err != nil {
			return err
		}
		if err = crash("replace.formatted"); err != nil {
			return err
		}
	}
	if err = w.withESP(ctx, srcNode, func(src string) error {
		return w.withESP(ctx, dstNode, func(dst string) error {
			w.logf("writing the %s boot partition %s from %s", m.Name(), dstNode, srcNode)
			if err := m.converge(ctx, w, dstNode, dst, src); err != nil {
				return err
			}
			if err := crash("replace.converged"); err != nil {
				return err
			}
			return m.install(ctx, w, tgt.Path, dst)
		})
	}); err != nil {
		return err
	}
	rec.BootWritten = true
	return rec.save(journal)
}

// expectedLabels are the partitions every Nex Core disk carries, by partition number.
var expectedLabels = map[int]string{ESPIndex: ESPLabel, 2: "nex-root-a", 3: "nex-verity-a", 4: "nex-root-b", 5: "nex-verity-b",
	DataIndex: DataLabel, BIOSIndex: BIOSLabel}

// planOf is the partition plan identical to the table of the disk the host runs from, ordered by partition number.
func planOf(t *Table) ([]Planned, error) {
	if len(t.Parts) != len(expectedLabels) {
		return nil, fmt.Errorf("carries %d partitions, not the %d of the Nex Core layout", len(t.Parts), len(expectedLabels))
	}
	var out []Planned
	for _, p := range t.Parts {
		if expectedLabels[p.Index] != p.Label {
			return nil, fmt.Errorf("partition %d is %q, not %q: it does not carry the Nex Core layout", p.Index, p.Label, expectedLabels[p.Index])
		}
		out = append(out, Planned{Index: p.Index, Label: p.Label, Type: p.Type.String(), Offset: t.Offset(p), Size: t.Size(p)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	if out[DataIndex-1].Type != typeRAID {
		return nil, fmt.Errorf("%s is not a RAID member partition", DataLabel)
	}
	return out, nil
}

// sameLayout reports why the table is not the plan, or nil when every partition matches in place and size.
func sameLayout(t *Table, plan []Planned) error {
	if len(t.Parts) != len(plan) {
		return fmt.Errorf("has %d partitions, not %d", len(t.Parts), len(plan))
	}
	for _, p := range plan {
		got, ok := t.ByIndex(p.Index)
		if !ok || got.Label != p.Label || got.Type.String() != p.Type || t.Offset(got) != p.Offset || t.Size(got) != p.Size {
			return fmt.Errorf("partition %d (%s) is not where the layout puts it", p.Index, p.Label)
		}
	}
	return nil
}

// blank reports whether the first and the last MiB of the disk are zero: no partition table, boot code or file
// system signature at either end.
func (d *Disk) blank() (bool, error) {
	buf := make([]byte, MiB)
	zero := make([]byte, MiB)
	for _, off := range []int64{0, d.Size - MiB} {
		if _, err := d.File.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("%s: %w", d.Path, err)
		}
		if !bytes.Equal(buf, zero) {
			return false, nil
		}
	}
	return true, nil
}

// sourceSlots opens every slot of the disk the host runs from that holds a release which verifies against the root
// hash its partition GUIDs name. The slot of the running system must be one of them and carry the running root hash;
// the other slot is copied when it verifies and left empty on the replacement when it does not.
func (w *Writer) sourceSlots(src *Disk, t *Table, booted string) (map[string]slotData, error) {
	bootedID, _, err := SlotIDs(booted)
	if err != nil {
		return nil, fmt.Errorf("running root hash: %w", err)
	}
	running, _, err := slotOf(src, bootedID)
	if err != nil {
		return nil, err
	}
	out := map[string]slotData{}
	for _, slot := range []string{"a", "b"} {
		data, err := w.sourceSlot(src, t, slot)
		switch {
		case err == nil && slot == running && data.hash != booted:
			return nil, fmt.Errorf("%s: the slot of the running system selects root hash %s, not the running %s", src.Path, data.hash, booted)
		case err == nil:
			out[slot] = data
		case slot == running:
			return nil, fmt.Errorf("%s: slot %s holds the running system but does not verify: %w", src.Path, slot, err)
		default:
			w.logf("%s: slot %s holds no verified release (%v); it is not copied", src.Path, slot, err)
		}
	}
	return out, nil
}

// sourceSlot reads one slot of a disk as the data to copy, after it verified the whole hash tree against the root
// hash the slot's two partition GUIDs spell (SlotIDs backwards).
func (w *Writer) sourceSlot(d *Disk, t *Table, slot string) (slotData, error) {
	rl, vl := slotLabels(slot)
	root, ok1 := t.Find(rl)
	verity, ok2 := t.Find(vl)
	if !ok1 || !ok2 {
		return slotData{}, fmt.Errorf("partitions %s and %s are not both present", rl, vl)
	}
	hash := strings.ReplaceAll(root.ID.String()+verity.ID.String(), "-", "")
	rp, err := openPartFlag(d, t, root, os.O_RDONLY)
	if err != nil {
		return slotData{}, err
	}
	defer rp.Close()
	vp, err := openPartFlag(d, t, verity, os.O_RDONLY)
	if err != nil {
		return slotData{}, err
	}
	defer vp.Close()
	sb := make([]byte, verityBlock)
	if _, err = vp.ReadAt(sb, 0); err != nil {
		return slotData{}, fmt.Errorf("verity superblock: %w", err)
	}
	p, err := parseVeritySuperblock(sb)
	if err != nil {
		return slotData{}, err
	}
	rootSize := int64(p.dataBlocks) * verityBlock
	if p.dataBlocks == 0 || rootSize > rp.size {
		return slotData{}, fmt.Errorf("the verity superblock describes %d blocks, the partition holds %d bytes", p.dataBlocks, rp.size)
	}
	w.logf("%s: verifying %s before it is copied", d.Path, rl)
	if err = verifyVerity(rp, rootSize, vp, vp.size, hash); err != nil {
		return slotData{}, err
	}
	return slotData{hash: hash, rootSize: rootSize, veritySize: vp.size,
		openRoot: partReader(d, t, root, rootSize), openVerity: partReader(d, t, verity, vp.size)}, nil
}

// partReader opens the first n bytes of a partition, read-only, each time it is called.
func partReader(d *Disk, t *Table, p Partition, n int64) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		pio, err := openPartFlag(d, t, p, os.O_RDONLY)
		if err != nil {
			return nil, err
		}
		return struct {
			io.Reader
			io.Closer
		}{io.NewSectionReader(pio, 0, n), pio}, nil
	}
}

// dataArray is the md array of the nex-data volume, read from sysfs.
type dataArray struct {
	dir      string // /sys/block/md127/md
	level    string
	action   string
	degraded int
}

func readDataArray() (dataArray, error) {
	real, err := filepath.EvalSymlinks(mdDevice())
	if err != nil {
		return dataArray{}, fmt.Errorf("this host has no mirrored %s volume to rebuild onto a replacement disk", DataLabel)
	}
	a := dataArray{dir: filepath.Join(mdSysBlock, filepath.Base(real), "md")}
	a.level = a.read("level")
	a.action = a.read("sync_action")
	a.degraded, _ = strconv.Atoi(a.read("degraded"))
	if a.level != "raid1" {
		return dataArray{}, fmt.Errorf("%s is a %q array; only a RAID1 mirror is rebuilt onto a replacement disk", mdDevice(), a.level)
	}
	return a, nil
}

func (a dataArray) read(name string) string {
	b, err := os.ReadFile(filepath.Join(a.dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// member is the kernel's state of a partition (sdb6) in the array, and whether it is a member at all.
func (a dataArray) member(partition string) (state string, ok bool) {
	b, err := os.ReadFile(filepath.Join(a.dir, "dev-"+partition, "state"))
	return strings.TrimSpace(string(b)), err == nil
}

func memberFaulty(state string) bool {
	for _, s := range strings.Split(state, ",") {
		if strings.TrimSpace(s) == "faulty" {
			return true
		}
	}
	return false
}
