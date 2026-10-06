// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
)

// uefiBoot boots the same GRUB menu, entries and counters as biosBoot; only the way the firmware reaches GRUB
// differs. The firmware starts shim (EFI/BOOT/BOOTX64.EFI), shim starts the Debian-signed GRUB beside it
// (grubx64.efi), and that GRUB reads EFI/debian/grub.cfg, its built-in prefix, which hands over to grub/grub.cfg.
// Each disk gets a firmware boot entry for its shim; the removable path also reaches it.
type uefiBoot struct{ biosBoot }

func (uefiBoot) Name() string { return "uefi" }

// loaderDir holds the files the firmware starts; they come from the release's esp/ tree.
const loaderDir = "EFI"

// loaderFiles is the release's esp/ tree: shim, GRUB, MokManager and the hand-over to grub/grub.cfg.
func (r *Release) loaderFiles() ([]biosFile, error) {
	var out []biosFile
	for name := range r.Manifest.Files {
		if rel, ok := strings.CutPrefix(name, "esp/"); ok {
			f, err := diskFile(rel, filepath.Join(r.Dir, filepath.FromSlash(name)))
			if err != nil {
				return nil, err
			}
			out = append(out, f)
		}
	}
	return out, nil
}

// scanLoader lists the loader files of a boot partition, as wanted state for another one.
func scanLoader(root string) ([]biosFile, error) {
	var out []biosFile
	err := filepath.WalkDir(filepath.Join(root, loaderDir), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, tempSuffix) {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		f, err := diskFile(filepath.ToSlash(rel), p)
		out = append(out, f)
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

// writeLoader brings the loader files up to the release's, before its entry can appear.
func writeLoader(root string, rel *Release) error {
	files, err := rel.loaderFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if sum, err := sha256File(filepath.Join(root, filepath.FromSlash(f.Rel))); err == nil && sum == f.SHA {
			continue
		}
		if err := writeBlob(root, f); err != nil {
			return err
		}
	}
	return nil
}

func (u uefiBoot) arm(root string, rel *Release, slot string, tries int) error {
	if err := writeLoader(root, rel); err != nil {
		return err
	}
	return u.biosBoot.arm(root, rel, slot, tries)
}

func (uefiBoot) place(root string, rel *Release, slot string) error {
	loader, err := rel.loaderFiles()
	if err != nil {
		return err
	}
	files, err := rel.releaseFiles(slot)
	if err != nil {
		return err
	}
	return convergeBIOS(root, append(loader, files...))
}

// install registers a firmware boot entry for the disk's shim.
func (uefiBoot) install(ctx context.Context, w *Writer, disk, _ string) error {
	w.registerBootEntry(ctx, disk)
	return nil
}

// converge copies the boot partition, loader files included. The firmware boot entry of a replaced disk is
// registered by install, which the disk replacement runs after this.
func (uefiBoot) converge(_ context.Context, _ *Writer, _, dst, src string) error {
	loader, err := scanLoader(src)
	if err != nil {
		return err
	}
	files, err := scanBIOS(src)
	if err != nil {
		return err
	}
	return convergeBIOS(dst, append(loader, files...))
}
