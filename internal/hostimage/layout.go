// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	MiB = 1 << 20
	GiB = 1 << 30

	BIOSSize    = 1 * MiB // GRUB's core image is embedded here on BIOS hosts
	ESPSize     = 1 * GiB
	SlotSize    = 4 * GiB // root partition size; the erofs image must fit
	VeritySize  = 64 * MiB
	MinDataSize = 2 * GiB

	// backupReserve covers the backup GPT at the end of the disk for any sector size.
	backupReserve = gptEntries*gptEntrySize + 4096

	LayoutSingle = "single"
	LayoutRAID1  = "raid1"

	BIOSLabel = "nex-bios"
	ESPLabel  = "nex-esp"
	DataLabel = "nex-data"

	// ESPIndex is the partition number of the boot partition (the ESP; BIOS hosts keep their GRUB files there too) on
	// every disk. BIOSIndex is the number of the BIOS boot partition: it sits in front of the ESP on the disk but
	// carries the last number, so the numbers of every other partition do not depend on it.
	ESPIndex  = 1
	BIOSIndex = 7

	typeBIOS   = "21686148-6449-6e6f-744e-656564454649"
	typeESP    = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	typeRoot   = "4f68bce3-e8cd-4db1-96e7-fbcaf984b709"
	typeVerity = "2c7357ed-ebd2-46d9-aec1-23d437ec2bf5"
	typeData   = "0fc63daf-8483-4772-8e79-3d69d8477de4"
	typeRAID   = "a19d880f-05fc-4d3b-a006-743f0f84911e"
)

var typeNames = map[string]string{
	typeBIOS: "bios-boot", typeESP: "esp", typeRoot: "root-x86-64", typeVerity: "root-x86-64-verity",
	typeData: "linux-generic", typeRAID: "linux-raid",
}

// Planned is one partition of the planned layout, in bytes.
type Planned struct {
	Index  int
	Label  string
	Type   string // textual type GUID
	Offset int64
	Size   int64
}

// TypeName is the short name of the partition type ("esp", "root-x86-64", ...).
func (p Planned) TypeName() string { return typeNames[p.Type] }

func alignDown(n int64) int64 { return n / MiB * MiB }

// ValidateLayout checks the layout name against the number of disks.
func ValidateLayout(layout string, disks int) error {
	switch {
	case layout == LayoutSingle && disks == 1:
	case layout == LayoutRAID1 && disks >= 2:
	case layout == LayoutSingle:
		return fmt.Errorf("layout single needs exactly 1 disk, got %d", disks)
	case layout == LayoutRAID1:
		return fmt.Errorf("layout raid1 needs at least 2 disks, got %d", disks)
	default:
		return fmt.Errorf("unknown layout %q (want single or raid1)", layout)
	}
	return nil
}

// MinDiskBytes is the smallest disk that fits the layout with MinDataSize.
func MinDiskBytes() int64 {
	return MiB + BIOSSize + ESPSize + 2*(SlotSize+VeritySize) + MinDataSize + MiB
}

// Plan returns the partition table for a disk of diskBytes, ordered by partition number. Every disk carries the BIOS
// boot partition whichever firmware the host boots with, so the same layout starts under both.
func Plan(diskBytes int64, layout string) ([]Planned, error) {
	if layout != LayoutSingle && layout != LayoutRAID1 {
		return nil, fmt.Errorf("unknown layout %q (want single or raid1)", layout)
	}
	if diskBytes < MinDiskBytes() {
		return nil, fmt.Errorf("disk of %d bytes is too small: the layout needs at least %d bytes (%d GiB data minimum)",
			diskBytes, MinDiskBytes(), MinDataSize/GiB)
	}
	dataType := typeData
	if layout == LayoutRAID1 {
		dataType = typeRAID
	}
	specs := []struct {
		label, typ string
		size       int64
	}{
		{ESPLabel, typeESP, ESPSize},
		{"nex-root-a", typeRoot, SlotSize},
		{"nex-verity-a", typeVerity, VeritySize},
		{"nex-root-b", typeRoot, SlotSize},
		{"nex-verity-b", typeVerity, VeritySize},
	}
	var out []Planned
	off := int64(MiB)
	bios := Planned{BIOSIndex, BIOSLabel, typeBIOS, off, BIOSSize}
	off += BIOSSize
	for i, s := range specs {
		out = append(out, Planned{ESPIndex + i, s.label, s.typ, off, s.size})
		off += s.size
	}
	end := alignDown(diskBytes - backupReserve)
	return append(out, Planned{DataIndex, DataLabel, dataType, off, end - off}, bios), nil
}

// NodeName is the kernel partition node for a disk node: /dev/vda -> /dev/vda1, /dev/nvme0n1 -> /dev/nvme0n1p1.
func NodeName(disk string, index int) string {
	if c := disk[len(disk)-1]; c >= '0' && c <= '9' {
		return fmt.Sprintf("%sp%d", disk, index)
	}
	return fmt.Sprintf("%s%d", disk, index)
}

// tableFor builds a GPT for the plan with random GUIDs everywhere.
func tableFor(plan []Planned, ss int) *Table {
	t := &Table{SectorSize: ss, DiskID: NewGUID()}
	for _, p := range plan {
		t.Parts = append(t.Parts, Partition{
			Index: p.Index, Type: mustGUID(p.Type), ID: NewGUID(), Label: p.Label,
			FirstLBA: uint64(p.Offset / int64(ss)), LastLBA: uint64((p.Offset+p.Size)/int64(ss) - 1),
		})
	}
	return t
}

func (d *Disk) zero(off, n int64) error {
	_, err := d.File.WriteAt(make([]byte, n), off)
	return err
}

// LayDisk wipes old signatures and writes a fresh GPT with random partition GUIDs.
func LayDisk(ctx context.Context, r Runner, d *Disk, layout string) error {
	plan, err := Plan(d.Size, layout)
	if err != nil {
		return fmt.Errorf("%s: %w", d.Path, err)
	}
	return layPlan(ctx, r, d, plan, NewGUID())
}

// layPlan wipes the signatures the plan's partitions and the disk's ends may carry and writes the plan as the
// disk's GPT under the given disk GUID, with random partition GUIDs.
func layPlan(ctx context.Context, r Runner, d *Disk, plan []Planned, diskID GUID) error {
	regions := [][2]int64{{0, MiB}, {d.Size - MiB, MiB}}
	for _, p := range plan {
		regions = append(regions, [2]int64{p.Offset, MiB}, [2]int64{p.Offset + p.Size - MiB, MiB})
	}
	for _, g := range regions {
		if err := d.zero(g[0], g[1]); err != nil {
			return fmt.Errorf("%s: wipe: %w", d.Path, err)
		}
	}
	if err := crash("lay.wiped"); err != nil {
		return err
	}
	t := tableFor(plan, d.SectorSize)
	t.DiskID = diskID
	if err := d.WriteTable(t); err != nil {
		return err
	}
	if err := d.Reread(); err != nil {
		return err
	}
	if err := r.Run(ctx, "udevadm", "settle"); err != nil {
		return err
	}
	return waitNodes(ctx, d, len(plan))
}

// waitNodes polls for kernel partition nodes of a block device.
func waitNodes(ctx context.Context, d *Disk, n int) error {
	if !d.block {
		return nil
	}
	for i := 1; i <= n; i++ {
		node := NodeName(d.Path, i)
		if err := waitPath(ctx, node, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func waitPath(ctx context.Context, path string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", filepath.Clean(path))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
