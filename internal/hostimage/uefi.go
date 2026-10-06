// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"errors"
)

type uefiBoot struct{}

func (uefiBoot) Name() string { return "uefi" }

func (uefiBoot) prune(root string, keepSequence int) error { return pruneOtherUKIs(root, keepSequence) }

func (uefiBoot) entries(root, node string) ([]UKIEntry, error) { return readUKIs(root, node) }

func (uefiBoot) arm(root string, rel *Release, _ string, tries int) error {
	return armESP(root, rel, tries)
}

func (uefiBoot) disarm(root string, sequence int) error { return disarmESP(root, sequence) }

func (uefiBoot) rollback(root string, from, to int) (bool, error) {
	return rollbackESP(root, from, to)
}

func (uefiBoot) place(root string, rel *Release, _ string) error {
	return ConvergeESP(root, rel.ESPFiles())
}

// install registers a firmware boot entry for the ESP of the disk. The removable-path loader still boots
// without it, so a failure is reported, not fatal.
func (uefiBoot) install(ctx context.Context, w *Writer, disk, _ string) error {
	w.registerBootEntry(ctx, disk)
	return nil
}

func (uefiBoot) booted() (string, error) { return BootedESP() }

// blessed is false while a UKI of the ESP still carries a boot counter with tries left.
func (uefiBoot) blessed(root string) (bool, error) {
	files, err := ScanESP(root)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		if isUKI(f.Rel) && counterRE.MatchString(f.Rel) && !isBadUKI(f.Rel) {
			return false, nil
		}
	}
	return true, nil
}

func (uefiBoot) converge(_ context.Context, _ *Writer, _, dst, src string) error {
	desired, err := ScanESP(src)
	if err != nil {
		return err
	}
	return ConvergeESP(dst, desired)
}

var errUEFIBless = errors.New("a UEFI host is blessed by systemd-bless-boot, not by nex-hostimage")

func (uefiBoot) bless(context.Context, *Writer, bool) error { return errUEFIBless }

func (uefiBoot) tries(context.Context, *Writer) (int, bool, error) { return 0, false, errUEFIBless }
