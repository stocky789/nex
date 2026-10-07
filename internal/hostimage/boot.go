// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

// efiFirmwareDir exists when the running system was started by UEFI firmware; tests replace it.
var efiFirmwareDir = "/sys/firmware/efi"

// BootMethod is how a disk's firmware starts a slot: the one place the update engine and the installer differ
// between UEFI and BIOS hosts. Everything above it (slot layout, root-hash slot selection, A/B staging, rollback)
// is shared. Each method works on a mounted boot partition (the partition labelled nex-esp, which BIOS hosts also
// use for their GRUB files):
//
// Both run the same GRUB menu: it reads one entry per slot (nex/a.cfg, nex/b.cfg) and counts nex_tries_<slot> in
// grub/grubenv; Core clears it (bless). They differ in how the firmware reaches GRUB:
//
//   - UEFI: shim and the Debian-signed GRUB on the ESP, started by a firmware boot entry (uefi.go).
//   - BIOS: GRUB installed in the MBR of every disk (bios.go).
//
// In both, the entry of a release appearing is the commit point and is written last.
type BootMethod interface {
	// Name is "uefi" or "bios".
	Name() string

	// prune removes every entry and kernel that does not belong to keepSequence, and half-written files.
	prune(root string, keepSequence int) error
	// entries lists the boot entries of a boot partition.
	entries(root, node string) ([]UKIEntry, error)
	// arm makes the release's entry bootable with the given number of tries. slot is the slot that holds it.
	arm(root string, rel *Release, slot string, tries int) error
	// disarm takes every entry of the sequence back.
	disarm(root string, sequence int) error
	// rollback gives the entry of sequence to a fresh counter and marks the entry of sequence from exhausted.
	rollback(root string, from, to int) (changed bool, err error)
	// place writes a fresh boot partition holding only the release in slot, blessed (install and repair).
	place(root string, rel *Release, slot string) error
	// install makes the disk start from its boot partition mounted at root (boot code, firmware entry).
	install(ctx context.Context, w *Writer, disk, root string) error

	// booted is the boot partition node the running system started from, or "" when it did not start from a Nex
	// boot loader.
	booted() (string, error)
	// blessed reports whether the entries on the boot partition at root are proven: none has tries left to spend.
	blessed(root string) (bool, error)
	// converge makes the boot partition at dst (the partition node) hold what the one at src holds, and the
	// disk start from it.
	converge(ctx context.Context, w *Writer, node, dst, src string) error

	// bless marks the running entry good (its counter is dropped) or bad (its tries are exhausted).
	bless(ctx context.Context, w *Writer, good bool) error
	// tries is the number of boot tries the running entry has left, counted false once it is blessed.
	tries(ctx context.Context, w *Writer) (left int, counted bool, err error)
}

// The two boot methods.
var (
	UEFI BootMethod = uefiBoot{}
	BIOS BootMethod = biosBoot{}
)

// DetectBoot is the boot method of the running firmware: UEFI when /sys/firmware/efi exists, else BIOS.
func DetectBoot() BootMethod {
	if _, err := os.Stat(efiFirmwareDir); err == nil {
		return UEFI
	}
	return BIOS
}

// ParseBootMethod resolves a flag value: "uefi", "bios", or "" / "auto" for the running firmware.
func ParseBootMethod(name string) (BootMethod, error) {
	switch name {
	case "", "auto":
		return DetectBoot(), nil
	case UEFI.Name():
		return UEFI, nil
	case BIOS.Name():
		return BIOS, nil
	}
	return nil, fmt.Errorf("unknown boot method %q (want uefi, bios or auto)", name)
}

// method is the Writer's boot method, the running firmware's unless one was set.
func (w *Writer) method() BootMethod {
	if w.Boot != nil {
		return w.Boot
	}
	return DetectBoot()
}

// ESPSync copies the booted boot partition's files to every other one, once the running entry has been blessed
// (it carries no boot counter). It is what heals a replaced disk and carries the blessed state to the sibling
// disks; it never runs before the system proved itself.
func (w *Writer) ESPSync(ctx context.Context) error {
	m := w.method()
	booted, err := m.booted()
	if err != nil {
		return err
	}
	if booted == "" {
		w.logf("not started by the Nex boot loader; no boot partition to propagate")
		return nil
	}
	// A system disk replacement or an update step is writing the partitions the sync would write: it does its own
	// convergence, so the sync leaves the other boot partitions to it.
	unlockDisks, err := lockDisks(ctx, 0)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		w.logf("a system disk operation is running; leaving the other boot partitions to it")
		return nil
	}
	if err != nil {
		return err
	}
	defer unlockDisks()
	nodes, err := ESPNodes()
	if err != nil {
		return err
	}
	// One hold for the whole sync: the other partitions are mounted while the booted one still is.
	unlock, err := lockESP(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err = w.withESP(ctx, booted, func(root string) error {
		if ok, err := m.blessed(root); err != nil {
			return err
		} else if !ok {
			return errNotBlessed
		}
		for _, n := range nodes {
			if n == booted {
				continue
			}
			w.logf("converging %s", n)
			if err := w.withESP(ctx, n, func(dst string) error { return m.converge(ctx, w, n, dst, root) }); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
		}
		return nil
	}); errors.Is(err, errNotBlessed) {
		w.logf("the running boot entry is not blessed yet; leaving the other boot partitions unchanged")
		return nil
	} else {
		return err
	}
}

var errNotBlessed = errors.New("boot entry not blessed")

// BlessBoot marks the running entry good or bad on the boot partition it started from.
func (w *Writer) BlessBoot(ctx context.Context, good bool) error {
	return w.method().bless(ctx, w, good)
}

// BootTries is the number of boot tries the running entry has left; counted is false once it is blessed.
func (w *Writer) BootTries(ctx context.Context) (left int, counted bool, err error) {
	return w.method().tries(ctx, w)
}
