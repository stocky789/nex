// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"nex.local/nex/internal/protocol"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeConsoleTunnelsVNCSocketToController(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "vnc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(conn, conn); conn.Close() }()
		}
	}()
	old := consoleSocket
	consoleSocket = func(ctx context.Context, id string) (string, error) {
		if id != "vm-1" {
			return "", errors.New("VM is not running")
		}
		return socket, nil
	}
	defer func() { consoleSocket = old }()
	seen := make(chan string, 4)
	relayed := make(chan string, 1)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Method + " " + r.URL.Path + " " + r.Header.Get("Upgrade") + " " + r.Proto
		if strings.HasSuffix(r.URL.Path, "/refused") {
			http.Error(w, "unknown", 404)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: nex-console\r\n\r\n")
		rw.Flush()
		conn.Write([]byte("RFB 003.008\n"))
		echoed, err := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if err == nil {
			relayed <- echoed
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := serveConsole(ctx, identity, protocol.ConsoleRequest{Token: "bad/token", VMID: "vm-1"}); err == nil {
		t.Fatal("invalid token accepted")
	}
	if err := serveConsole(ctx, identity, protocol.ConsoleRequest{Token: "ticket", VMID: "vm-2"}); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("stopped VM: %v", err)
	}
	if err := serveConsole(ctx, identity, protocol.ConsoleRequest{Token: "refused", VMID: "vm-1"}); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("refused session: %v", err)
	}
	if err := serveConsole(ctx, identity, protocol.ConsoleRequest{Token: "ticket", VMID: "vm-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-relayed:
		if got != "RFB 003.008\n" {
			t.Fatalf("relayed %q", got)
		}
	case <-time.After(time.Minute):
		t.Fatal("bytes were not relayed through the VNC socket")
	}
	<-seen
	if request := <-seen; request != "POST /api/v1/agent/console/ticket nex-console HTTP/1.1" {
		t.Fatalf("tunnel request %q", request)
	}
}

// BUG-0051 regression: serveConsole must use the host console session handed to it
// directly rather than dialing a socket address, since internal/vmhost.HostConsoleSession
// no longer exposes one (a socket any local uid could reach used to hand out an
// unauthenticated root shell). This fakes the hook with an in-process net.Pipe, the
// same shape the real implementation returns, and confirms serveConsole relays it
// without trying to dial anything.
func TestServeConsoleUsesHostSessionDirectly(t *testing.T) {
	agentSide, serverSide := net.Pipe()
	old := hostConsoleSession
	calls := 0
	hostConsoleSession = func() (net.Conn, error) {
		calls++
		return agentSide, nil
	}
	defer func() { hostConsoleSession = old }()
	go func() {
		io.Copy(serverSide, serverSide)
		serverSide.Close()
	}()

	relayed := make(chan string, 1)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: nex-console\r\n\r\n")
		rw.Flush()
		conn.Write([]byte("host shell\n"))
		echoed, err := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if err == nil {
			relayed <- echoed
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := serveConsole(ctx, identity, protocol.ConsoleRequest{Token: "ticket", VMID: ""}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("hostConsoleSession called %d times, want 1", calls)
	}
	select {
	case got := <-relayed:
		if got != "host shell\n" {
			t.Fatalf("relayed %q", got)
		}
	case <-time.After(time.Minute):
		t.Fatal("bytes were not relayed through the host console session")
	}
}
