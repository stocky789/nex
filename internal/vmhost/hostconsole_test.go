// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// rfbHandshake drives the client half of the RFB 3.8/None-security handshake
// HostConsoleSession's server side speaks, and leaves c ready for message exchange.
func rfbHandshake(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	ver := make([]byte, 12)
	if _, err := io.ReadFull(c, ver); err != nil || string(ver) != "RFB 003.008\n" {
		t.Fatalf("version %q %v", ver, err)
	}
	if _, err := io.WriteString(c, "RFB 003.008\n"); err != nil {
		t.Fatal(err)
	}
	sec := make([]byte, 2)
	if _, err := io.ReadFull(c, sec); err != nil || sec[0] != 1 || sec[1] != 1 {
		t.Fatalf("security types %v %v", sec, err)
	}
	if _, err := c.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	result := make([]byte, 4)
	if _, err := io.ReadFull(c, result); err != nil || binary.BigEndian.Uint32(result) != 0 {
		t.Fatalf("security result %v %v", result, err)
	}
	if _, err := c.Write([]byte{1}); err != nil { // shared
		t.Fatal(err)
	}
	init := make([]byte, 24)
	if _, err := io.ReadFull(c, init); err != nil {
		t.Fatal(err)
	}
	if w, h := binary.BigEndian.Uint16(init[0:]), binary.BigEndian.Uint16(init[2:]); w != hostWidth || h != hostHeight {
		t.Fatalf("framebuffer %dx%d", w, h)
	}
	n := binary.BigEndian.Uint32(init[20:])
	name := make([]byte, n)
	if _, err := io.ReadFull(c, name); err != nil || string(name) != "Nex Core" {
		t.Fatalf("name %q %v", name, err)
	}
	_ = c.SetDeadline(time.Time{})
}

func requestFrame(c net.Conn, incremental byte) error {
	body := make([]byte, 10) // type(1) incremental(1) x(2) y(2) w(2) h(2)
	body[0] = 3
	body[1] = incremental
	_, err := c.Write(body)
	return err
}

func readFrame(t *testing.T, c net.Conn) {
	t.Helper()
	hdr := make([]byte, 4+12)
	if _, err := io.ReadFull(c, hdr); err != nil {
		t.Fatal(err)
	}
	if hdr[0] != 0 {
		t.Fatalf("not a FramebufferUpdate: %v", hdr)
	}
	pixels := make([]byte, hostWidth*hostHeight*4)
	if _, err := io.ReadFull(c, pixels); err != nil {
		t.Fatal(err)
	}
}

func sendKey(t *testing.T, c net.Conn, sym uint32, down bool) {
	t.Helper()
	msg := make([]byte, 8) // type(1) down-flag(1) padding(2) keysym(4)
	msg[0] = 4
	if down {
		msg[1] = 1
	}
	binary.BigEndian.PutUint32(msg[4:], sym)
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
}

func TestHostConsoleSessionRFBHandshake(t *testing.T) {
	conn, err := HostConsoleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rfbHandshake(t, conn)
}

// BUG-0051 regression: the host console used to listen on the abstract UNIX socket
// @nex-core-host-console with RFB security None, so any local uid (including a QEMU
// guest process) could connect and get an unauthenticated root shell. HostConsoleSession
// now hands the caller an in-process net.Pipe end and opens no listener at all; confirm
// the old address is unreachable.
func TestHostConsoleHasNoLocalListener(t *testing.T) {
	if _, err := net.DialTimeout("unix", "@nex-core-host-console", 200*time.Millisecond); err == nil {
		t.Fatal("the old host console socket address is still reachable; it must not listen at all")
	}
}

// BUG-0052 regression: every FramebufferUpdateRequest used to get a full ~1MB raw
// frame regardless of the incremental flag, so an idle console streamed frames
// back-to-back because noVNC re-requests immediately after each update. An
// incremental request with nothing changed must now go unanswered until the
// terminal actually changes, at which point the server pushes the deferred frame
// itself.
func TestHostConsoleIncrementalUpdateOnlyOnChange(t *testing.T) {
	conn, err := HostConsoleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rfbHandshake(t, conn)

	drain := func() {
		if err := requestFrame(conn, 0); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		readFrame(t, conn)
		_ = conn.SetDeadline(time.Time{})
	}
	drain() // whatever the shell has printed by the time the handshake finishes
	time.Sleep(200 * time.Millisecond)
	drain() // and anything from shell startup that lands shortly after

	if err := requestFrame(conn, 1); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle console answered an incremental request with nothing changed")
	} else if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected a read timeout, got %v", err)
	}
	_ = conn.SetDeadline(time.Time{})

	// Typing echoes through the PTY, which marks the terminal dirty; the server must
	// push the deferred frame on its own rather than waiting for another request.
	sendKey(t, conn, 'x', true)
	sendKey(t, conn, 'x', false)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	readFrame(t, conn)
	_ = conn.SetDeadline(time.Time{})

	// A deferred incremental request that a later full request satisfies must not
	// leave the server owing a frame: the next PTY output would otherwise arrive as
	// an unsolicited full frame nobody asked for.
	time.Sleep(200 * time.Millisecond)
	drain()
	if err := requestFrame(conn, 1); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected the incremental request to defer, got %v", err)
	}
	_ = conn.SetDeadline(time.Time{})
	drain() // a full request answers now and clears the deferred one
	sendKey(t, conn, 'y', true)
	sendKey(t, conn, 'y', false)
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("output pushed an unsolicited frame after a satisfied incremental request, got %v", err)
	}
	_ = conn.SetDeadline(time.Time{})
}

// BUG-0053 regression: KeyEvent tracked no modifier state, so Ctrl+C/D/Z arrived as
// plain letters and arrow keys were dropped.
func TestKeysymByteCtrlAndArrows(t *testing.T) {
	cases := []struct {
		sym  uint32
		ctrl bool
		want string
	}{
		{sym: 'c', ctrl: true, want: "\x03"},
		{sym: 'C', ctrl: true, want: "\x03"},
		{sym: 'd', ctrl: true, want: "\x04"},
		{sym: 'z', ctrl: true, want: "\x1a"},
		{sym: 'c', ctrl: false, want: "c"},
		{sym: '\\', ctrl: true, want: "\x1c"}, // Ctrl+\ (quit)
		{sym: '[', ctrl: true, want: "\x1b"},
		{sym: '@', ctrl: true, want: "\x00"},
		{sym: ' ', ctrl: true, want: "\x00"},
		{sym: '\\', ctrl: false, want: "\\"},
		{sym: 0xff51, ctrl: false, want: "\x1b[D"}, // Left
		{sym: 0xff52, ctrl: false, want: "\x1b[A"}, // Up
		{sym: 0xff53, ctrl: false, want: "\x1b[C"}, // Right
		{sym: 0xff54, ctrl: false, want: "\x1b[B"}, // Down
		{sym: 0xff50, ctrl: false, want: "\x1b[H"}, // Home
		{sym: 0xff57, ctrl: false, want: "\x1b[F"}, // End
	}
	for _, c := range cases {
		if got := string(keysymByte(c.sym, c.ctrl)); got != c.want {
			t.Fatalf("keysymByte(%#x, ctrl=%v) = %q, want %q", c.sym, c.ctrl, got, c.want)
		}
	}
}

// BUG-0054 regression: the tab-expansion loop re-evaluated its bound after each
// put() advanced the cursor, so it emitted roughly half the intended spaces.
func TestHostTermTabStopsAtNextMultipleOfEight(t *testing.T) {
	fromCol0 := newHostTerm()
	fromCol0.write([]byte("\t"))
	if fromCol0.cur != 8 {
		t.Fatalf("tab from column 0: cur = %d, want 8", fromCol0.cur)
	}
	fromCol3 := newHostTerm()
	fromCol3.write([]byte("abc\t"))
	if fromCol3.cur != 8 {
		t.Fatalf("tab after \"abc\": cur = %d, want 8", fromCol3.cur)
	}
}
