// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// DataMDName is the md array name and /dev/md/<name> link of a mirrored nex-data.
	DataMDName = "nex-data"
	// DataIndex is the partition number of nex-data on every disk.
	DataIndex = 6
	// SlotA is the slot an install writes; slot B is laid out empty.
	SlotA = "a"
	// etcUpper is where the persistent /etc overlay keeps its upper layer, relative to the data volume
	// (which the host mounts as /var).
	etcUpper = "lib/nex/etc/upper"
	etcWork  = "lib/nex/etc/work"
)

// mountBase is where transient mounts of the installer and the slot writer live; tests replace it.
var (
	mountBase = "/run/nex"
	mdDevDir  = "/dev/md"
)

func mdDevice() string { return filepath.Join(mdDevDir, DataMDName) }

// Options select the disks and inputs of Install and Repair.
type Options struct {
	Layout   string
	Disks    []string
	ImageDir string
	EtcSeed  string // Install only: tree copied into the /etc overlay upper layer
}

// Install wipes the disks, lays the GPT on each, builds nex-data (md RAID1 for the raid1 layout),
// writes slot A with its hash tree, converges every ESP and seeds /etc. Slot B stays empty. Nothing
// is written before the release and the disks have been validated.
func (w *Writer) Install(ctx context.Context, o Options) error {
	rel, disks, err := w.open(o)
	if err != nil {
		return err
	}
	defer closeAll(disks)
	for _, d := range disks {
		w.logf("laying out %s", d.Path)
		if err = LayDisk(ctx, w.Runner, d, o.Layout); err != nil {
			return err
		}
	}
	if err = w.makeData(ctx, disks, o.Layout); err != nil {
		return err
	}
	if err = w.finishDisks(ctx, disks, rel); err != nil {
		return err
	}
	return w.seedEtc(ctx, disks, o, o.EtcSeed)
}

// Repair re-lays everything except nex-data on each disk (GPT entries 1-5 are wiped and rewritten,
// entry 6 keeps its GUID, position and content), writes slot A and converges the ESPs. The data
// volume is never formatted, assembled or written.
func (w *Writer) Repair(ctx context.Context, o Options) error {
	if o.EtcSeed != "" {
		return errors.New("repair keeps /etc: no /etc seed is accepted")
	}
	rel, disks, err := w.open(o)
	if err != nil {
		return err
	}
	defer closeAll(disks)
	tables := make([]*Table, len(disks))
	for i, d := range disks {
		if tables[i], err = repairTable(d, o.Layout); err != nil {
			return err
		}
	}
	for i, d := range disks {
		w.logf("re-laying %s, keeping %s", d.Path, DataLabel)
		if err = d.relay(ctx, w.Runner, tables[i]); err != nil {
			return err
		}
	}
	return w.finishDisks(ctx, disks, rel)
}

// open validates the options and opens the release and the disks for writing.
func (w *Writer) open(o Options) (*Release, []*Disk, error) {
	if err := ValidateLayout(o.Layout, len(o.Disks)); err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, p := range o.Disks {
		if seen[p] {
			return nil, nil, fmt.Errorf("disk %s is listed twice", p)
		}
		seen[p] = true
	}
	w.logf("verifying the release in %s", o.ImageDir)
	rel, err := ReadRelease(o.ImageDir)
	if err != nil {
		return nil, nil, err
	}
	var disks []*Disk
	for _, p := range o.Disks {
		d, err := OpenDisk(p, true)
		if err == nil {
			_, err = Plan(d.Size, o.Layout)
		}
		if err != nil {
			closeAll(disks)
			if d != nil {
				d.Close()
			}
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		disks = append(disks, d)
	}
	return rel, disks, nil
}

func closeAll(disks []*Disk) {
	for _, d := range disks {
		d.Close()
	}
}

// repairTable plans the GPT of a repaired disk: the existing nex-data entry is carried over unchanged.
func repairTable(d *Disk, layout string) (*Table, error) {
	old, err := d.ReadTable()
	if err != nil {
		return nil, fmt.Errorf("%s: nothing to repair: %w", d.Path, err)
	}
	data, ok := old.Find(DataLabel)
	if !ok || data.Index != DataIndex {
		return nil, fmt.Errorf("%s: no %s partition %d to keep", d.Path, DataLabel, DataIndex)
	}
	plan, err := Plan(d.Size, layout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", d.Path, err)
	}
	want := plan[DataIndex-1]
	if old.Offset(data) != want.Offset || old.SectorSize != d.SectorSize {
		return nil, fmt.Errorf("%s: the existing %s does not start where the image layout puts it (%d, not %d); repair cannot keep it",
			d.Path, DataLabel, old.Offset(data), want.Offset)
	}
	if t := mustGUID(want.Type); data.Type != t {
		return nil, fmt.Errorf("%s: %s is not a %s partition for layout %s", d.Path, DataLabel, typeNames[want.Type], layout)
	}
	t := tableFor(plan[:DataIndex-1], d.SectorSize)
	t.DiskID = old.DiskID
	t.Parts = append(t.Parts, data)
	return t, nil
}

// relay replaces the partition table with t, wiping the signatures of every partition except nex-data.
func (d *Disk) relay(ctx context.Context, r Runner, t *Table) error {
	for _, p := range t.Parts {
		if p.Label == DataLabel {
			continue
		}
		off, size := t.Offset(p), t.Size(p)
		for _, g := range [][2]int64{{off, MiB}, {off + size - MiB, MiB}} {
			if err := d.zero(g[0], g[1]); err != nil {
				return fmt.Errorf("%s: wipe: %w", d.Path, err)
			}
		}
	}
	if err := d.WriteTable(t); err != nil {
		return err
	}
	if err := d.Reread(); err != nil {
		return err
	}
	if err := r.Run(ctx, "udevadm", "settle"); err != nil {
		return err
	}
	return waitNodes(ctx, d, len(t.Parts))
}

// makeData formats nex-data: ext4 on the partition, or ext4 on a new md RAID1 over every disk's partition.
func (w *Writer) makeData(ctx context.Context, disks []*Disk, layout string) error {
	dev := NodeName(disks[0].Path, DataIndex)
	if layout == LayoutRAID1 {
		args := []string{"--create", mdDevice(), "--run", "--level=1", "--metadata=1.2",
			"--name=" + DataMDName, "--bitmap=internal", "--raid-devices=" + strconv.Itoa(len(disks))}
		for _, d := range disks {
			args = append(args, NodeName(d.Path, DataIndex))
		}
		w.logf("creating the %s mirror over %d disks", DataMDName, len(disks))
		if err := w.Runner.Run(ctx, "mdadm", args...); err != nil {
			return err
		}
		dev = mdDevice()
		if err := waitPath(ctx, dev, 30*time.Second); err != nil {
			return err
		}
	}
	w.logf("creating the %s file system on %s", DataLabel, dev)
	return w.Runner.Run(ctx, "mkfs.ext4", "-F", "-q", "-L", DataLabel, dev)
}

// finishDisks formats the ESPs, writes slot A on every disk in S2 order and converges the ESPs.
func (w *Writer) finishDisks(ctx context.Context, disks []*Disk, rel *Release) error {
	for _, d := range disks {
		if err := w.Runner.Run(ctx, "mkfs.fat", "-F", "32", "-n", "NEXESP", NodeName(d.Path, 1)); err != nil {
			return err
		}
	}
	if err := w.WriteSlot(ctx, disks, SlotA, rel, ""); err != nil {
		return err
	}
	for _, d := range disks {
		node := NodeName(d.Path, 1)
		w.logf("writing the boot partition %s", node)
		if err := w.WithESP(ctx, node, func(root string) error { return ConvergeESP(root, rel.ESPFiles()) }); err != nil {
			return fmt.Errorf("%s: %w", node, err)
		}
		w.registerBootEntry(ctx, d.Path)
	}
	return nil
}

// registerBootEntry adds a firmware boot entry for the ESP of a disk. The removable-path loader still
// boots without it, so a failure (no efivarfs in a chroot, a read-only variable store) is reported, not fatal.
func (w *Writer) registerBootEntry(ctx context.Context, disk string) {
	err := w.Runner.Run(ctx, "efibootmgr", "--create", "--disk", disk, "--part", "1",
		"--label", "Nex Core "+filepath.Base(disk), "--loader", `\EFI\systemd\systemd-bootx64.efi`)
	if err != nil {
		w.logf("warning: no firmware boot entry for %s (the removable-path loader remains): %v", disk, err)
	}
}

// seedEtc copies the /etc seed into the overlay upper layer on nex-data and leaves the volume unmounted.
func (w *Writer) seedEtc(ctx context.Context, disks []*Disk, o Options, seed string) error {
	dev := NodeName(disks[0].Path, DataIndex)
	if o.Layout == LayoutRAID1 {
		dev = mdDevice()
	}
	mp := filepath.Join(mountBase, "data")
	if err := os.MkdirAll(mp, 0o700); err != nil {
		return err
	}
	if err := w.Runner.Run(ctx, "mount", "-t", "ext4", dev, mp); err != nil {
		return err
	}
	err := func() error {
		for _, dir := range []string{etcUpper, etcWork} {
			if err := os.MkdirAll(filepath.Join(mp, dir), 0o755); err != nil {
				return err
			}
		}
		if seed == "" {
			return nil
		}
		w.logf("seeding /etc from %s", seed)
		return SeedEtc(seed, filepath.Join(mp, etcUpper))
	}()
	if uerr := w.Runner.Run(ctx, "umount", mp); err == nil {
		err = uerr
	}
	if err == nil && o.Layout == LayoutRAID1 {
		err = w.Runner.Run(ctx, "mdadm", "--stop", mdDevice())
	}
	return err
}
