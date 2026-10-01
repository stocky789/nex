// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// SeedEtc copies the /etc seed staged by the installer into the overlay upper layer. Regular files,
// directories and symlinks are accepted, with mode, owner and symlink targets preserved; anything else
// (devices, sockets, FIFOs) is refused. Existing upper content is replaced file by file.
func SeedEtc(seed, upper string) error {
	if err := os.MkdirAll(upper, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(seed, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(seed, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(upper, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		st, _ := info.Sys().(*syscall.Stat_t)
		chown := func() error {
			if st == nil {
				return nil
			}
			return os.Lchown(dst, int(st.Uid), int(st.Gid))
		}
		switch {
		case d.IsDir():
			if err = os.MkdirAll(dst, info.Mode().Perm()); err != nil {
				return err
			}
			if err = os.Chmod(dst, info.Mode().Perm()|info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
				return err
			}
			return chown()
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			os.Remove(dst)
			if err = os.Symlink(target, dst); err != nil {
				return err
			}
			return chown()
		case info.Mode().IsRegular():
			if err = copyAtomic(p, dst); err != nil {
				return err
			}
			if err = os.Chmod(dst, info.Mode().Perm()); err != nil {
				return err
			}
			return chown()
		default:
			return fmt.Errorf("%s: cannot seed %s (only files, directories and symlinks)", p, info.Mode().Type())
		}
	})
}

// Shadowed is a file in the /etc overlay upper layer that hides a different file of the same name in the image.
type Shadowed struct {
	Path string // below /etc, slash separated
	// Whiteout is true when the upper layer deletes a file the image ships.
	Whiteout bool
}

// AuditEtc lists regular files of the upper layer that mask a different regular file of the image's
// /etc (the lower layer), and whiteouts that delete image files. A copied-up passwd, shadow or unit
// hides later image changes, so the audit is what tells an operator which files no longer follow
// updates. Files that only exist in the upper layer are local configuration and are not reported.
func AuditEtc(lower, upper string) ([]Shadowed, error) {
	var out []Shadowed
	err := filepath.WalkDir(upper, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(upper, p)
		if err != nil {
			return err
		}
		low := filepath.Join(lower, rel)
		lowInfo, lerr := os.Lstat(low)
		if lerr != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeCharDevice != 0 { // overlayfs whiteout: character device 0/0
			out = append(out, Shadowed{Path: filepath.ToSlash(rel), Whiteout: true})
			return nil
		}
		if !info.Mode().IsRegular() || !lowInfo.Mode().IsRegular() {
			return nil
		}
		same, err := sameContent(p, low)
		if err != nil {
			return err
		}
		if !same {
			out = append(out, Shadowed{Path: filepath.ToSlash(rel)})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

func sameContent(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		if ea != nil || eb != nil {
			return ea == eb || (ea != nil && eb != nil), nil
		}
	}
}
