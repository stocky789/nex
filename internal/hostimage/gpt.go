// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

const (
	gptEntries    = 128
	gptEntrySize  = 128
	gptHeaderSize = 92

	blkRRPART     = 0x125f
	blkFLSBUF     = 0x1261
	blkSSZGET     = 0x1268
	blkGETSIZE64  = 0x80081272
	posixFadvDont = 4
)

var (
	le = binary.LittleEndian

	// ErrNoGPT means neither the primary nor the backup GPT is valid.
	ErrNoGPT = errors.New("no valid GPT")
)

// GUID holds a GUID in on-disk (mixed-endian) byte order.
type GUID [16]byte

// ParseGUID parses the textual 8-4-4-4-12 form.
func ParseGUID(s string) (GUID, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return GUID{}, fmt.Errorf("invalid GUID %q", s)
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil {
		return GUID{}, fmt.Errorf("invalid GUID %q", s)
	}
	return swapGUID(raw), nil
}

func mustGUID(s string) GUID {
	g, err := ParseGUID(s)
	if err != nil {
		panic(err)
	}
	return g
}

// swapGUID converts between textual and on-disk byte order (an involution).
func swapGUID(b []byte) GUID {
	var g GUID
	copy(g[:], b)
	g[0], g[1], g[2], g[3] = b[3], b[2], b[1], b[0]
	g[4], g[5] = b[5], b[4]
	g[6], g[7] = b[7], b[6]
	return g
}

// String returns the lowercase textual form.
func (g GUID) String() string {
	b := swapGUID(g[:])
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// NewGUID returns a random version 4 GUID.
func NewGUID() GUID {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return swapGUID(b[:])
}

// Partition is one GPT entry; Index is the 1-based entry number.
type Partition struct {
	Index    int
	Type, ID GUID
	FirstLBA uint64
	LastLBA  uint64
	Label    string
}

// Table is the decoded content of a GPT.
type Table struct {
	SectorSize int
	DiskID     GUID
	Parts      []Partition
}

// Find returns the partition with the given label.
func (t *Table) Find(label string) (Partition, bool) {
	for _, p := range t.Parts {
		if p.Label == label {
			return p, true
		}
	}
	return Partition{}, false
}

// Offset and Size are the partition's byte range.
// ByIndex is the entry with the given 1-based entry number. Parts holds only the used entries, so the
// number is not a position in it.
func (t *Table) ByIndex(index int) (Partition, bool) {
	for _, p := range t.Parts {
		if p.Index == index {
			return p, true
		}
	}
	return Partition{}, false
}

func (t *Table) Offset(p Partition) int64 { return int64(p.FirstLBA) * int64(t.SectorSize) }
func (t *Table) Size(p Partition) int64 {
	return int64(p.LastLBA-p.FirstLBA+1) * int64(t.SectorSize)
}

// File is the storage behind a Disk: a regular file or a block device.
type File interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Close() error
}

// Disk is a whole disk (block device, or a regular file in tests).
type Disk struct {
	Path       string
	File       File
	SectorSize int
	Size       int64
	block      bool
}

// OpenDisk opens a block device or regular file and detects its geometry.
func OpenDisk(path string, write bool) (*Disk, error) {
	flag := os.O_RDONLY
	if write {
		flag = os.O_RDWR
	}
	f, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	d := &Disk{Path: path, File: f, SectorSize: 512}
	switch {
	case st.Mode().IsRegular():
		d.Size = st.Size()
	case st.Mode()&os.ModeDevice != 0 && st.Mode()&os.ModeCharDevice == 0:
		d.block = true
		var size uint64
		var ss int32
		if err = ioctl(f.Fd(), blkGETSIZE64, unsafe.Pointer(&size)); err == nil {
			err = ioctl(f.Fd(), blkSSZGET, unsafe.Pointer(&ss))
		}
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		d.Size, d.SectorSize = int64(size), int(ss)
	default:
		f.Close()
		return nil, fmt.Errorf("%s is neither a block device nor a regular file", path)
	}
	return d, nil
}

func (d *Disk) Close() error { return d.File.Close() }

func ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

func (d *Disk) fd() (uintptr, bool) {
	if f, ok := d.File.(*os.File); ok {
		return f.Fd(), true
	}
	return 0, false
}

// Reread asks the kernel to re-read the partition table; regular files are ignored.
func (d *Disk) Reread() error {
	fd, ok := d.fd()
	if !d.block || !ok {
		return nil
	}
	var err error
	for range 20 { // udev may still hold a just-closed partition
		if err = ioctl(fd, blkRRPART, nil); err != syscall.EBUSY {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("%s: re-read partition table: %w", d.Path, err)
	}
	return nil
}

// dropCaches syncs f and discards its cached pages so later reads hit the medium. On a block
// device the buffer cache of that node is flushed too.
func dropCaches(f File, block bool) error {
	if err := f.Sync(); err != nil {
		return err
	}
	of, ok := f.(*os.File)
	if !ok {
		return nil
	}
	if block {
		if err := ioctl(of.Fd(), blkFLSBUF, nil); err != nil {
			return fmt.Errorf("%s: flush buffers: %w", of.Name(), err)
		}
	}
	syscall.Syscall6(syscall.SYS_FADVISE64, of.Fd(), 0, 0, posixFadvDont, 0, 0)
	return nil
}

// ReadTable reads the primary GPT, falling back to the backup.
func (d *Disk) ReadTable() (*Table, error) {
	ss := int64(d.SectorSize)
	last := d.Size/ss - 1
	if last < 2 {
		return nil, fmt.Errorf("%s: %w", d.Path, ErrNoGPT)
	}
	t, perr := d.readCopy(1)
	if perr == nil {
		return t, nil
	}
	t, berr := d.readCopy(last)
	if berr == nil {
		return t, nil
	}
	return nil, fmt.Errorf("%s: %w (primary: %v; backup: %v)", d.Path, ErrNoGPT, perr, berr)
}

func (d *Disk) readCopy(lba int64) (*Table, error) {
	ss := int64(d.SectorSize)
	buf := make([]byte, ss)
	if _, err := d.File.ReadAt(buf, lba*ss); err != nil {
		return nil, err
	}
	if string(buf[:8]) != "EFI PART" {
		return nil, errors.New("bad signature")
	}
	hs := int64(le.Uint32(buf[12:]))
	if hs < gptHeaderSize || hs > ss {
		return nil, errors.New("bad header size")
	}
	crc := le.Uint32(buf[16:])
	le.PutUint32(buf[16:], 0)
	if crc32.ChecksumIEEE(buf[:hs]) != crc {
		return nil, errors.New("header CRC mismatch")
	}
	if int64(le.Uint64(buf[24:])) != lba {
		return nil, errors.New("header LBA mismatch")
	}
	num, size := int64(le.Uint32(buf[80:])), int64(le.Uint32(buf[84:]))
	entLBA := int64(le.Uint64(buf[72:]))
	if size != gptEntrySize || num < 1 || num > 1024 || entLBA < 2 || entLBA*ss+num*size > d.Size {
		return nil, errors.New("bad entries array geometry")
	}
	ents := make([]byte, num*size)
	if _, err := d.File.ReadAt(ents, entLBA*ss); err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(ents) != le.Uint32(buf[88:]) {
		return nil, errors.New("entries CRC mismatch")
	}
	t := &Table{SectorSize: int(ss)}
	copy(t.DiskID[:], buf[56:72])
	for i := range num {
		e := ents[i*size : (i+1)*size]
		var p Partition
		copy(p.Type[:], e[0:16])
		if p.Type == (GUID{}) {
			continue
		}
		p.Index = int(i) + 1
		copy(p.ID[:], e[16:32])
		p.FirstLBA, p.LastLBA = le.Uint64(e[32:]), le.Uint64(e[40:])
		u := make([]uint16, 36)
		for j := range u {
			u[j] = le.Uint16(e[56+2*j:])
		}
		if n := indexZero(u); n >= 0 {
			u = u[:n]
		}
		p.Label = string(utf16.Decode(u))
		t.Parts = append(t.Parts, p)
	}
	return t, nil
}

func indexZero(u []uint16) int {
	for i, v := range u {
		if v == 0 {
			return i
		}
	}
	return -1
}

// WriteTable writes the protective MBR and both GPT copies (backup first).
func (d *Disk) WriteTable(t *Table) error {
	ss := int64(d.SectorSize)
	total := d.Size / ss
	esec := (gptEntries*gptEntrySize + ss - 1) / ss
	first, last := uint64(2+esec), uint64(total-1-esec-1)
	ents := make([]byte, gptEntries*gptEntrySize)
	for _, p := range t.Parts {
		if p.Index < 1 || p.Index > gptEntries || p.FirstLBA < first || p.LastLBA > last || p.FirstLBA > p.LastLBA {
			return fmt.Errorf("%s: partition %d (%s) does not fit the disk", d.Path, p.Index, p.Label)
		}
		e := ents[(p.Index-1)*gptEntrySize:]
		copy(e[0:16], p.Type[:])
		copy(e[16:32], p.ID[:])
		le.PutUint64(e[32:], p.FirstLBA)
		le.PutUint64(e[40:], p.LastLBA)
		for i, u := range utf16.Encode([]rune(p.Label)) {
			if i >= 36 {
				break
			}
			le.PutUint16(e[56+2*i:], u)
		}
	}
	entCRC := crc32.ChecksumIEEE(ents)
	header := func(my, alt, entLBA int64) []byte {
		b := make([]byte, ss)
		copy(b, "EFI PART")
		le.PutUint32(b[8:], 0x00010000)
		le.PutUint32(b[12:], gptHeaderSize)
		le.PutUint64(b[24:], uint64(my))
		le.PutUint64(b[32:], uint64(alt))
		le.PutUint64(b[40:], first)
		le.PutUint64(b[48:], last)
		copy(b[56:72], t.DiskID[:])
		le.PutUint64(b[72:], uint64(entLBA))
		le.PutUint32(b[80:], gptEntries)
		le.PutUint32(b[84:], gptEntrySize)
		le.PutUint32(b[88:], entCRC)
		le.PutUint32(b[16:], crc32.ChecksumIEEE(b[:gptHeaderSize]))
		return b
	}
	mbr := make([]byte, ss)
	mbr[446+2], mbr[446+4] = 2, 0xee // CHS start 0/0/2, type EFI protective
	mbr[446+5], mbr[446+6], mbr[446+7] = 0xff, 0xff, 0xff
	le.PutUint32(mbr[446+8:], 1)
	le.PutUint32(mbr[446+12:], uint32(min(total-1, 0xffffffff)))
	mbr[510], mbr[511] = 0x55, 0xaa
	type write struct {
		lba int64
		b   []byte
	}
	// One copy is complete and durable before the other is touched: a power cut at any point leaves at
	// least one valid copy holding either the old table or the new one, never two torn ones.
	for _, copyWrites := range [][]write{
		{{total - 1 - esec, ents}, {total - 1, header(total-1, 1, total-1-esec)}},
		{{0, mbr}, {2, ents}, {1, header(1, total-1, 2)}},
	} {
		for _, w := range copyWrites {
			if _, err := d.File.WriteAt(w.b, w.lba*ss); err != nil {
				return fmt.Errorf("%s: write GPT: %w", d.Path, err)
			}
		}
		if err := d.File.Sync(); err != nil {
			return fmt.Errorf("%s: sync GPT: %w", d.Path, err)
		}
	}
	return nil
}

// SetPartitionIDs changes unique partition GUIDs (by entry number) in both GPT copies.
func (d *Disk) SetPartitionIDs(ids map[int]GUID) error {
	t, err := d.ReadTable()
	if err != nil {
		return err
	}
	seen := 0
	for i := range t.Parts {
		if g, ok := ids[t.Parts[i].Index]; ok {
			t.Parts[i].ID = g
			seen++
		}
	}
	if seen != len(ids) {
		return fmt.Errorf("%s: partition missing from GPT", d.Path)
	}
	return d.WriteTable(t)
}
