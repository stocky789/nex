// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// GRUB starts the kernel and initrd as files, so a boot entry carries the kernel, the initrd and the command line
// that the release's UKI is made of. They are taken out of the UKI itself, never from a second copy: the release
// signature covers the UKI, and one file means the boot entry cannot disagree with the release.

// Section names of a UKI (systemd's UKI specification). The first profile's sections are the ones used.
const (
	sectionLinux   = ".linux"
	sectionInitrd  = ".initrd"
	sectionCmdline = ".cmdline"
)

// blob is one UKI section: where it is, how long it is and its SHA-256.
type blob struct {
	name string
	size int64
	sha  string
}

// ukiParts is what a boot entry takes from a UKI.
type ukiParts struct {
	cmdline        string
	kernel, initrd blob
}

// sectionSize is the length of a section's content: the virtual size, which excludes the padding that fills the
// last file-alignment block, unless the section is stored shorter than it claims.
func sectionSize(s *pe.Section) int64 {
	if s.VirtualSize > 0 && s.VirtualSize < s.Size {
		return int64(s.VirtualSize)
	}
	return int64(s.Size)
}

// uki is an open UKI file.
type uki struct {
	f   *os.File
	pe  *pe.File
	sec map[string]*pe.Section
}

func openUKI(path string) (*uki, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	pf, err := pe.NewFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s is not a UKI (PE) file: %w", path, err)
	}
	u := &uki{f: f, pe: pf, sec: map[string]*pe.Section{}}
	for _, s := range pf.Sections {
		if _, dup := u.sec[s.Name]; !dup {
			u.sec[s.Name] = s
		}
	}
	return u, nil
}

func (u *uki) Close() error { return u.f.Close() }

// reader streams the content of a section.
func (u *uki) reader(name string) (io.Reader, int64, error) {
	s, ok := u.sec[name]
	if !ok {
		return nil, 0, fmt.Errorf("the UKI has no %s section", name)
	}
	n := sectionSize(s)
	return io.LimitReader(s.Open(), n), n, nil
}

// parts reads the command line and measures kernel and initrd.
func (u *uki) parts() (*ukiParts, error) {
	p := &ukiParts{}
	r, n, err := u.reader(sectionCmdline)
	if err != nil {
		return nil, err
	}
	if n > 64*1024 {
		return nil, fmt.Errorf("the UKI %s section is %d bytes", sectionCmdline, n)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	// ukify ends the text with a NUL byte.
	p.cmdline = strings.TrimSpace(string(bytes.TrimRight(b, "\x00")))
	for _, step := range []struct {
		name string
		dst  *blob
	}{{sectionLinux, &p.kernel}, {sectionInitrd, &p.initrd}} {
		r, n, err := u.reader(step.name)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("the UKI %s section is empty", step.name)
		}
		h := sha256.New()
		if _, err = io.Copy(h, r); err != nil {
			return nil, fmt.Errorf("read the UKI %s section: %w", step.name, err)
		}
		*step.dst = blob{name: step.name, size: n, sha: hex.EncodeToString(h.Sum(nil))}
	}
	return p, nil
}

// readCloser pairs a section reader with the UKI file it reads from.
type readCloser struct {
	io.Reader
	u *uki
}

func (r readCloser) Close() error { return r.u.Close() }

// biosCache holds the parts of a release's UKI, read once.
type biosCache struct {
	once  sync.Once
	parts *ukiParts
	err   error
}

// biosParts returns the kernel, initrd and command line of the release's UKI. The command line must carry the
// manifest's root hash: the entry that names the slot must be the one the manifest describes.
func (r *Release) biosParts() (*ukiParts, error) {
	r.bios.once.Do(func() {
		u, err := openUKI(r.UKIPath())
		if err != nil {
			r.bios.err = err
			return
		}
		defer u.Close()
		p, err := u.parts()
		if err != nil {
			r.bios.err = err
			return
		}
		if cmdlineRootHash(p.cmdline) != r.Manifest.RootHash {
			r.bios.err = fmt.Errorf("the UKI command line names root hash %q, the manifest says %s", cmdlineRootHash(p.cmdline), r.Manifest.RootHash)
			return
		}
		r.bios.parts = p
	})
	return r.bios.parts, r.bios.err
}

// openSection opens a UKI section of the release for streaming; the caller closes it.
func (r *Release) openSection(name string) (io.ReadCloser, error) {
	u, err := openUKI(r.UKIPath())
	if err != nil {
		return nil, err
	}
	rd, _, err := u.reader(name)
	if err != nil {
		u.Close()
		return nil, err
	}
	return readCloser{rd, u}, nil
}

// cmdlineRootHash is the roothash= value of a kernel command line, or "" when it has none.
func cmdlineRootHash(cmdline string) string {
	return cmdlineValue(cmdline, "roothash")
}

// cmdlineValue is the value of the last key=value word named key.
func cmdlineValue(cmdline, key string) string {
	v := ""
	for _, f := range strings.Fields(cmdline) {
		if x, ok := strings.CutPrefix(f, key+"="); ok {
			v = x
		}
	}
	return v
}
