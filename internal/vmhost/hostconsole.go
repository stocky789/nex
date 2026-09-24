// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Host console reuses the same RFB/noVNC path as VM consoles. The host is not a
// QEMU guest, so the agent serves a local RFB endpoint that wraps a root shell PTY.
//
// The only consumer is this same agent process (internal/agent/console.go dials it
// and pipes it straight to the Controller over the enrolled mTLS session). A real
// listener therefore has no legitimate second caller, and an abstract/UNIX socket
// address is reachable by any local uid, including a QEMU guest process — that would
// hand an unauthenticated root shell to anything on the host. HostConsoleSession
// hands the caller the client end of an in-process net.Pipe instead: nothing but
// this process can ever reach the server end.

const (
	hostCols   = 80
	hostRows   = 25
	hostFontW  = 8
	hostFontH  = 16
	hostWidth  = hostCols * hostFontW
	hostHeight = hostRows * hostFontH
)

// HostConsoleSession starts one host console shell and returns the agent-side end
// of an in-process pipe carrying RFB; the RFB/PTY session runs in a goroutine on
// the other end. Each call is a fresh session with its own shell.
func HostConsoleSession() (net.Conn, error) {
	agentSide, serverSide := net.Pipe()
	go serveHostRFB(serverSide)
	return agentSide, nil
}

func serveHostRFB(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.WriteString(c, "RFB 003.008\n"); err != nil {
		return
	}
	ver := make([]byte, 12)
	if _, err := io.ReadFull(c, ver); err != nil {
		return
	}
	if _, err := c.Write([]byte{1, 1}); err != nil { // one security type: None
		return
	}
	sec := make([]byte, 1)
	if _, err := io.ReadFull(c, sec); err != nil || sec[0] != 1 {
		return
	}
	if _, err := c.Write([]byte{0, 0, 0, 0}); err != nil {
		return
	}
	init := make([]byte, 1)
	if _, err := io.ReadFull(c, init); err != nil {
		return
	}
	name := []byte("Nex Core")
	serverInit := make([]byte, 24+len(name))
	binary.BigEndian.PutUint16(serverInit[0:], hostWidth)
	binary.BigEndian.PutUint16(serverInit[2:], hostHeight)
	serverInit[4] = 32 // bits-per-pixel
	serverInit[5] = 24 // depth
	serverInit[6] = 0  // little-endian
	serverInit[7] = 1  // true-colour
	binary.BigEndian.PutUint16(serverInit[8:], 255)
	binary.BigEndian.PutUint16(serverInit[10:], 255)
	binary.BigEndian.PutUint16(serverInit[12:], 255)
	serverInit[14], serverInit[15], serverInit[16] = 16, 8, 0 // RGB shifts
	binary.BigEndian.PutUint32(serverInit[20:], uint32(len(name)))
	copy(serverInit[24:], name)
	if _, err := c.Write(serverInit); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})

	master, slave, err := openPty()
	if err != nil {
		return
	}
	defer master.Close()
	setWinsize(master, hostRows, hostCols)
	cmd := exec.Command("/bin/bash", "-l")
	cmd.Env = append(os.Environ(), "TERM=dumb", fmt.Sprintf("COLUMNS=%d", hostCols), fmt.Sprintf("LINES=%d", hostRows))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err = cmd.Start(); err != nil {
		slave.Close()
		return
	}
	slave.Close()
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	term := newHostTerm()
	var mu sync.Mutex
	var wmu sync.Mutex
	// dirty tracks terminal content changed since the last frame actually sent.
	// wantUpdate tracks an incremental FramebufferUpdateRequest that arrived while
	// !dirty: the PTY reader pushes the deferred frame itself once new output makes
	// the terminal dirty, instead of every read chunk forcing a full frame like an
	// idle console re-requesting nothing new (noVNC otherwise streams full frames
	// back-to-back even at idle).
	dirty := true
	wantUpdate := false
	sendFrame := func() error {
		mu.Lock()
		fb := term.frame()
		dirty = false
		// A frame just went out, so any deferred incremental request is satisfied:
		// leaving wantUpdate set would make the PTY reader push one unsolicited full
		// frame the next time output arrives.
		wantUpdate = false
		mu.Unlock()
		wmu.Lock()
		defer wmu.Unlock()
		return writeFB(c, fb)
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				mu.Lock()
				term.write(buf[:n])
				dirty = true
				push := wantUpdate
				wantUpdate = false
				mu.Unlock()
				if push {
					_ = sendFrame()
				}
			}
			if err != nil {
				_ = c.Close()
				return
			}
		}
	}()

	var keys keyState
	msg := make([]byte, 1)
	for {
		if _, err := io.ReadFull(c, msg); err != nil {
			return
		}
		switch msg[0] {
		case 0: // SetPixelFormat
			if _, err := io.CopyN(io.Discard, c, 19); err != nil {
				return
			}
		case 2: // SetEncodings
			hdr := make([]byte, 3)
			if _, err := io.ReadFull(c, hdr); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint16(hdr[1:]))
			if _, err := io.CopyN(io.Discard, c, int64(n*4)); err != nil {
				return
			}
		case 3: // FramebufferUpdateRequest
			body := make([]byte, 9)
			if _, err := io.ReadFull(c, body); err != nil {
				return
			}
			incremental := body[0] != 0
			mu.Lock()
			isDirty := dirty
			if incremental && !isDirty {
				wantUpdate = true
			}
			mu.Unlock()
			if incremental && !isDirty {
				continue // deferred: the PTY reader sends it once content changes
			}
			if err := sendFrame(); err != nil {
				return
			}
		case 4: // KeyEvent
			body := make([]byte, 7)
			if _, err := io.ReadFull(c, body); err != nil {
				return
			}
			down := body[0] != 0
			sym := binary.BigEndian.Uint32(body[3:])
			if sym == 0xffe3 || sym == 0xffe4 { // Control_L / Control_R
				keys.ctrl = down
				continue
			}
			if !down {
				continue
			}
			if b := keysymByte(sym, keys.ctrl); len(b) > 0 {
				_, _ = master.Write(b)
			}
		case 5: // PointerEvent
			if _, err := io.CopyN(io.Discard, c, 5); err != nil {
				return
			}
		case 6: // ClientCutText
			pad := make([]byte, 7)
			if _, err := io.ReadFull(c, pad); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(pad[3:])
			if n > 1<<20 {
				return
			}
			if _, err := io.CopyN(io.Discard, c, int64(n)); err != nil {
				return
			}
		default:
			return
		}
	}
}

func writeFB(c net.Conn, pixels []byte) error {
	hdr := []byte{0, 0, 0, 1} // FramebufferUpdate, 1 rect
	var rect [12]byte
	binary.BigEndian.PutUint16(rect[4:], hostWidth)
	binary.BigEndian.PutUint16(rect[6:], hostHeight)
	// encoding 0 = raw at rect[8:12]
	if _, err := c.Write(hdr); err != nil {
		return err
	}
	if _, err := c.Write(rect[:]); err != nil {
		return err
	}
	_, err := c.Write(pixels)
	return err
}

// hostTerm escape states: ground text, then the ESC / CSI / OSC parsers that
// drop sequences programs still emit despite TERM=dumb (apt colour, coloured
// prompts). Only the ESC byte used to be discarded, so "[32m" printed literally.
const (
	termGround = iota
	termESC
	termCSI
	termOSC
	termOSCST // OSC saw ESC; waiting for '\' to finish ST
	termCharset
)

type hostTerm struct {
	cells []byte
	cur   int
	esc   int
}

func newHostTerm() *hostTerm {
	t := &hostTerm{cells: make([]byte, hostCols*hostRows)}
	for i := range t.cells {
		t.cells[i] = ' '
	}
	return t
}

func (t *hostTerm) write(b []byte) {
	for _, ch := range b {
		switch t.esc {
		case termESC:
			switch ch {
			case '[':
				t.esc = termCSI
			case ']':
				t.esc = termOSC
			case '(', ')':
				t.esc = termCharset
			default:
				t.esc = termGround // single-character escape: eat it
			}
			continue
		case termCSI:
			// CSI parameters and intermediates are 0x20-0x3F; finals are 0x40-0x7E.
			if ch >= 0x40 && ch <= 0x7e {
				t.esc = termGround
			}
			continue
		case termOSC:
			if ch == 0x07 { // BEL
				t.esc = termGround
			} else if ch == 0x1b {
				t.esc = termOSCST
			}
			continue
		case termOSCST:
			if ch == '\\' {
				t.esc = termGround
			} else if ch == 0x1b {
				// nested ESC while waiting for ST: restart escape parse
				t.esc = termESC
			} else {
				t.esc = termOSC
			}
			continue
		case termCharset:
			t.esc = termGround
			continue
		}

		switch ch {
		case 0x1b:
			t.esc = termESC
		case '\r':
			t.cur -= t.cur % hostCols
		case '\n':
			t.cur -= t.cur % hostCols
			t.cur += hostCols
			if t.cur >= len(t.cells) {
				t.scroll()
			}
		case '\b':
			if t.cur%hostCols > 0 {
				t.cur--
				t.cells[t.cur] = ' '
			}
		case '\t':
			// Compute the stop once: put() advances t.cur, so re-evaluating the bound
			// each iteration would shrink it mid-loop and emit roughly half the spaces.
			n := 8 - (t.cur%hostCols)%8
			for i := 0; i < n; i++ {
				t.put(' ')
			}
		default:
			if ch >= 32 {
				t.put(ch)
			}
		}
	}
}

func (t *hostTerm) put(ch byte) {
	if t.cur >= len(t.cells) {
		t.scroll()
	}
	t.cells[t.cur] = ch
	t.cur++
	if t.cur >= len(t.cells) {
		t.scroll()
	}
}

func (t *hostTerm) scroll() {
	copy(t.cells, t.cells[hostCols:])
	for i := hostCols * (hostRows - 1); i < len(t.cells); i++ {
		t.cells[i] = ' '
	}
	t.cur = hostCols * (hostRows - 1)
}

func (t *hostTerm) frame() []byte {
	out := make([]byte, hostWidth*hostHeight*4)
	fg := [3]byte{0xdd, 0xdd, 0xdd}
	bg := [3]byte{0x00, 0x00, 0x00}
	for row := 0; row < hostRows; row++ {
		for col := 0; col < hostCols; col++ {
			ch := t.cells[row*hostCols+col]
			glyph := hostGlyph(ch)
			for y := 0; y < hostFontH; y++ {
				bits := glyph[y]
				for x := 0; x < hostFontW; x++ {
					px, py := col*hostFontW+x, row*hostFontH+y
					i := (py*hostWidth + px) * 4
					on := bits&(0x80>>x) != 0
					if on {
						out[i], out[i+1], out[i+2] = fg[2], fg[1], fg[0] // BGRA for little-endian RFB RGB shifts
					} else {
						out[i], out[i+1], out[i+2] = bg[2], bg[1], bg[0]
					}
					out[i+3] = 0xff
				}
			}
		}
	}
	// cursor block
	if t.cur >= 0 && t.cur < len(t.cells) {
		col, row := t.cur%hostCols, t.cur/hostCols
		for y := hostFontH - 2; y < hostFontH; y++ {
			for x := 0; x < hostFontW; x++ {
				i := ((row*hostFontH+y)*hostWidth + col*hostFontW + x) * 4
				out[i], out[i+1], out[i+2] = fg[2], fg[1], fg[0]
			}
		}
	}
	return out
}

func hostGlyph(ch byte) []byte {
	if ch < 32 || ch > 126 {
		ch = '?'
	}
	off := int(ch-32) * hostFontH
	return hostConsoleFont[off : off+hostFontH]
}

// keyState tracks the one modifier this terminal understands: whether Control is
// currently held. noVNC sends a KeyEvent per physical key, so Ctrl+C arrives as a
// Control_L down, then a 'c' down/up, then a Control_L up.
type keyState struct {
	ctrl bool
}

func keysymByte(sym uint32, ctrl bool) []byte {
	if ctrl {
		switch {
		case sym == 0x20: // Ctrl+Space -> NUL, as terminals send it
			return []byte{0}
		case sym >= 0x40 && sym <= 0x5f:
			// Ctrl+@ -> 0x00, Ctrl+A..Ctrl+Z -> 0x01..0x1a, Ctrl+[ \ ] ^ _ -> 0x1b..0x1f
			return []byte{byte(sym & 0x1f)}
		case sym >= 'a' && sym <= 'z':
			return []byte{byte(sym & 0x1f)}
		}
	}
	switch sym {
	case 0xff0d, 0xff8d: // Return / KP Enter
		return []byte{'\r'}
	case 0xff08: // BackSpace
		return []byte{0x7f}
	case 0xff09: // Tab
		return []byte{'\t'}
	case 0xff1b: // Escape
		return []byte{0x1b}
	case 0xffff: // Delete
		return []byte{0x1b, '[', '3', '~'}
	case 0xff51: // Left
		return []byte{0x1b, '[', 'D'}
	case 0xff52: // Up
		return []byte{0x1b, '[', 'A'}
	case 0xff53: // Right
		return []byte{0x1b, '[', 'C'}
	case 0xff54: // Down
		return []byte{0x1b, '[', 'B'}
	case 0xff50: // Home
		return []byte{0x1b, '[', 'H'}
	case 0xff57: // End
		return []byte{0x1b, '[', 'F'}
	}
	if sym >= 0x20 && sym <= 0x7e {
		return []byte{byte(sym)}
	}
	if sym < 0x20 {
		return []byte{byte(sym)}
	}
	return nil
}

func openPty() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	var n int
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), uintptr(syscall.TIOCGPTN), uintptr(unsafe.Pointer(&n)))
	if errno != 0 {
		m.Close()
		return nil, nil, errno
	}
	unlock := 0
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), uintptr(syscall.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlock)))
	if errno != 0 {
		m.Close()
		return nil, nil, errno
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}

type winsize struct{ Row, Col, Xpixel, Ypixel uint16 }

func setWinsize(f *os.File, rows, cols int) {
	ws := winsize{Row: uint16(rows), Col: uint16(cols)}
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&ws)))
}