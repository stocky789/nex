// SPDX-License-Identifier: AGPL-3.0-only
package wsock

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type client struct {
	conn net.Conn
	r    *bufio.Reader
}

func dial(t *testing.T, server *httptest.Server, headers string) (*client, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = conn.Write([]byte("GET /console HTTP/1.1\r\nHost: x\r\n" + headers + "\r\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &client{conn, r}, resp
}

const upgrade = "Upgrade: websocket\r\nConnection: keep-alive, Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"

func (c *client) send(opcode byte, payload []byte) {
	head := []byte{0x80 | opcode, 0x80}
	switch n := len(payload); {
	case n < 126:
		head[1] |= byte(n)
	default:
		head[1] |= 126
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	}
	mask := []byte{1, 2, 3, 4}
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	c.conn.Write(append(append(head, mask...), masked...))
}
func (c *client) recv(t *testing.T) (byte, []byte) {
	t.Helper()
	var head [2]byte
	if _, err := io.ReadFull(c.r, head[:]); err != nil {
		t.Fatal(err)
	}
	size := int(head[1] & 0x7f)
	if size == 126 {
		var ext [2]byte
		io.ReadFull(c.r, ext[:])
		size = int(binary.BigEndian.Uint16(ext[:]))
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		t.Fatal(err)
	}
	return head[0] & 0x0f, payload
}

func TestHandshakeRelayPingAndClose(t *testing.T) {
	backend := make(chan net.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := Accept(w, r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		a, b := net.Pipe()
		backend <- b
		Pipe(ws, a)
	}))
	defer server.Close()
	c, resp := dial(t, server, upgrade+"Sec-WebSocket-Protocol: binary\r\n")
	if resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" || resp.Header.Get("Sec-WebSocket-Protocol") != "binary" {
		t.Fatalf("handshake %v %v", resp.StatusCode, resp.Header)
	}
	peer := <-backend
	peer.SetDeadline(time.Now().Add(5 * time.Second))
	big := strings.Repeat("rfb", 1000)
	c.send(0x2, []byte(big))
	got := make([]byte, len(big))
	if _, err := io.ReadFull(peer, got); err != nil || string(got) != big {
		t.Fatalf("client→backend relay: %v %q", err, got[:8])
	}
	c.send(0x9, []byte("ping"))
	if op, payload := c.recv(t); op != 0xA || string(payload) != "ping" {
		t.Fatalf("pong %x %q", op, payload)
	}
	go peer.Write([]byte("server bytes"))
	if op, payload := c.recv(t); op != 0x2 || string(payload) != "server bytes" {
		t.Fatalf("backend→client relay %x %q", op, payload)
	}
	c.send(0x8, nil)
	if op, _ := c.recv(t); op != 0x8 {
		t.Fatalf("expected close frame, got %x", op)
	}
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("backend stayed open after client close")
	}
}

func TestRejectsBadUpgradeAndUnmaskedFrames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := Accept(w, r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		_, _ = io.Copy(io.Discard, ws)
	}))
	defer server.Close()
	for _, headers := range []string{"", strings.Replace(upgrade, "13", "8", 1), strings.Replace(upgrade, "dGhlIHNhbXBsZSBub25jZQ==", "short", 1)} {
		if _, resp := dial(t, server, headers); resp.StatusCode != 400 {
			t.Fatalf("accepted invalid upgrade %q: %d", headers, resp.StatusCode)
		}
	}
	c, resp := dial(t, server, upgrade)
	if resp.StatusCode != 101 {
		t.Fatal(resp.Status)
	}
	c.conn.Write([]byte{0x82, 0x01, 'x'})
	if op, _ := c.recv(t); op != 0x8 {
		t.Fatalf("unmasked frame should close the stream, got %x", op)
	}
}
