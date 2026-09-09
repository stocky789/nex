// SPDX-License-Identifier: AGPL-3.0-only

// Package wsock is the minimal RFC 6455 server side needed to bridge a browser
// console client to a byte stream. It accepts one upgrade, relays binary data
// frames as an opaque stream, answers pings and closes cleanly.
package wsock

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaxFrame bounds one client frame; console input never approaches it.
const MaxFrame = 1 << 20

const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Conn is an accepted WebSocket. Read yields data-frame payload bytes, Write
// sends one binary frame. Both are safe for one reader and one writer.
type Conn struct {
	conn    net.Conn
	r       *bufio.Reader
	wmu     sync.Mutex
	pending []byte
	closed  bool
}

// Accept validates the upgrade request, hijacks the connection and completes the
// handshake. Callers must already have authenticated the request and its Origin.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !tokenListContains(r.Header.Get("Connection"), "upgrade") || r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("websocket upgrade required")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
		return nil, errors.New("invalid websocket key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("connection cannot be hijacked")
	}
	sum := sha1.Sum([]byte(key + acceptGUID))
	response := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n"
	// noVNC negotiates no subprotocol by default; echo "binary" for clients that still ask.
	if tokenListContains(r.Header.Get("Sec-WebSocket-Protocol"), "binary") {
		response += "Sec-WebSocket-Protocol: binary\r\n"
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	// The server's per-request deadlines no longer apply to a long-lived stream.
	_ = conn.SetDeadline(time.Time{})
	if _, err = rw.WriteString(response + "\r\n"); err == nil {
		err = rw.Flush()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{conn: conn, r: rw.Reader}, nil
}

func tokenListContains(header, want string) bool {
	for _, v := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}

// Read returns payload bytes from the next data frame, transparently answering
// pings. A close frame or protocol violation ends the stream with io.EOF.
func (c *Conn) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		opcode, payload, err := c.frame()
		if err != nil {
			c.Close()
			return 0, io.EOF
		}
		switch opcode {
		case 0x0, 0x1, 0x2:
			c.pending = payload
		case 0x8:
			c.Close()
			return 0, io.EOF
		case 0x9:
			if err := c.write(0xA, payload); err != nil {
				return 0, io.EOF
			}
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *Conn) frame() (byte, []byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(c.r, head[:]); err != nil {
		return 0, nil, err
	}
	opcode := head[0] & 0x0f
	if head[0]&0x70 != 0 || (opcode > 0x2 && opcode < 0x8) || opcode > 0xA {
		return 0, nil, errors.New("unsupported frame")
	}
	if head[1]&0x80 == 0 {
		return 0, nil, errors.New("client frames must be masked")
	}
	size := uint64(head[1] & 0x7f)
	switch size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(ext[:])
	}
	if size > MaxFrame || (opcode >= 0x8 && size > 125) {
		return 0, nil, errors.New("frame too large")
	}
	var mask [4]byte
	if _, err := io.ReadFull(c.r, mask[:]); err != nil {
		return 0, nil, err
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return opcode, payload, nil
}

// Write sends p as one binary frame.
func (c *Conn) Write(p []byte) (int, error) {
	if err := c.write(0x2, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *Conn) write(opcode byte, payload []byte) error {
	head := []byte{0x80 | opcode, 0}
	switch n := len(payload); {
	case n < 126:
		head[1] = byte(n)
	case n < 1<<16:
		head[1] = 126
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	default:
		head[1] = 127
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	if _, err := c.conn.Write(append(head, payload...)); err != nil {
		return err
	}
	return nil
}

// Close sends a normal close frame on a best-effort basis and closes the socket.
func (c *Conn) Close() error {
	c.wmu.Lock()
	if c.closed {
		c.wmu.Unlock()
		return nil
	}
	c.closed = true
	_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = c.conn.Write([]byte{0x88, 0x02, 0x03, 0xE8})
	c.wmu.Unlock()
	return c.conn.Close()
}

// Pipe relays bytes both ways until either side ends, then closes both.
func Pipe(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	<-done
	a.Close()
	b.Close()
	<-done
}

// Hijacked reads through the bufio.Reader returned by Hijack, which may already
// hold bytes that arrived with the upgrade request, and otherwise uses the socket.
type Hijacked struct {
	R *bufio.Reader
	net.Conn
}

func (h Hijacked) Read(p []byte) (int, error) { return h.R.Read(p) }
