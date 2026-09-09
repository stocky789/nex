// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLocalConsoleBridgesRunningVM(t *testing.T) {
	s, _ := testService(t)
	socket := filepath.Join(t.TempDir(), "vnc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		conn.Write([]byte("RFB 003.008\n"))
		io.Copy(conn, conn)
		conn.Close()
	}()
	bin := t.TempDir()
	id := "11111111-2222-4333-a444-555555555555"
	xml := `<domain type='kvm'><name>g</name><uuid>` + id + `</uuid><devices><graphics type='vnc' autoport='no'><listen type='socket' socket='` + socket + `'/></graphics></devices></domain>`
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\ncase \"$3\" in domstate) printf '%s\\n' \"$VMHOST_STATE\";; dumpxml) printf '%s\\n' \"$VMHOST_XML\";; *) exit 2;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VMHOST_XML", xml)
	t.Setenv("VMHOST_STATE", "running")
	server := httptest.NewUnstartedServer(s.Handler())
	server.StartTLS()
	defer server.Close()
	hostPort := strings.TrimPrefix(server.URL, "https://")
	origin := "https://" + hostPort
	dial := func(request string) (net.Conn, *http.Response) {
		conn, err := tls.Dial("tcp", hostPort, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		conn.Write([]byte(request))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		return conn, resp
	}
	credentials := `{"username":"root","password":"local-admin-password"}`
	_, resp := dial("POST /api/local/v1/session HTTP/1.1\r\nHost: " + hostPort + "\r\nOrigin: " + origin + "\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(credentials)) + "\r\n\r\n" + credentials)
	if resp.StatusCode != 200 || len(resp.Cookies()) != 1 {
		t.Fatalf("login %d", resp.StatusCode)
	}
	cookie := resp.Cookies()[0]
	ws := func(vm, originHeader, cookieValue string) string {
		return "GET /api/local/v1/vms/" + vm + "/console HTTP/1.1\r\nHost: " + hostPort + "\r\nOrigin: " + originHeader + "\r\nCookie: " + cookie.Name + "=" + cookieValue + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
	}
	if _, resp := dial(ws(id, origin, "wrong")); resp.StatusCode != 401 {
		t.Fatalf("no session: %d", resp.StatusCode)
	}
	if _, resp := dial(ws(id, "https://evil.test", cookie.Value)); resp.StatusCode != 403 {
		t.Fatalf("cross-origin: %d", resp.StatusCode)
	}
	t.Setenv("VMHOST_STATE", "shut off")
	if _, resp := dial(ws(id, origin, cookie.Value)); resp.StatusCode != 409 {
		t.Fatalf("stopped VM: %d", resp.StatusCode)
	}
	t.Setenv("VMHOST_STATE", "running")
	conn, resp := dial(ws(id, origin, cookie.Value))
	if resp.StatusCode != 101 {
		t.Fatalf("upgrade %d", resp.StatusCode)
	}
	frame := make([]byte, 14)
	if _, err := io.ReadFull(conn, frame); err != nil || frame[0] != 0x82 || string(frame[2:]) != "RFB 003.008\n" {
		t.Fatalf("VNC greeting not relayed: %v %q", err, frame)
	}
	conn.Write([]byte{0x82, 0x83, 1, 2, 3, 4, 'a' ^ 1, 'b' ^ 2, 'c' ^ 3})
	if _, err := io.ReadFull(conn, frame[:5]); err != nil || string(frame[2:5]) != "abc" {
		t.Fatalf("echo through VNC socket: %v %q", err, frame[:5])
	}
	conn.Close()
}
