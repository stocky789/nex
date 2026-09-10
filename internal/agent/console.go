// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"nex.local/nex/internal/wsock"
	"strings"
	"time"
)

// consoleSocket is replaced by tests; production resolves the VM on system libvirt.
var consoleSocket = vmhost.ConsoleSocket

// hostConsoleSession is replaced by tests; production starts the host RFB session
// in-process. It returns a net.Conn directly rather than a socket to dial: the host
// console has no legitimate consumer besides this process, so it is never exposed
// on any listener a second local process could reach (see internal/vmhost/hostconsole.go).
var hostConsoleSession = vmhost.HostConsoleSession

// serveConsole attaches one local RFB endpoint (VM VNC, or the host console when
// VMID is empty) to the Controller session. The connection is outbound over the
// enrolled mTLS identity, so no listener is opened on the network and raw RFB
// never leaves the host unprotected.
func serveConsole(ctx context.Context, s State, req protocol.ConsoleRequest) error {
	if req.Token == "" || len(req.Token) > 128 || strings.ContainsAny(req.Token, "/\\?#% \r\n") {
		return errors.New("invalid console token")
	}
	var vnc net.Conn
	var err error
	if req.VMID == "" {
		vnc, err = hostConsoleSession()
		if err != nil {
			return err
		}
	} else {
		var socket string
		socket, err = consoleSocket(ctx, req.VMID)
		if err != nil {
			return err
		}
		dialer := net.Dialer{Timeout: 5 * time.Second}
		vnc, err = dialer.DialContext(ctx, "unix", socket)
		if err != nil {
			return fmt.Errorf("console socket: %w", err)
		}
	}
	defer vnc.Close()
	config, err := tlsConfig(s, true)
	if err != nil {
		return err
	}
	config.NextProtos = []string{"http/1.1"}
	u, _ := url.Parse(s.Controller)
	port := u.Port()
	if port == "" {
		port = "443"
	}
	tlsDialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: config}
	conn, err := tlsDialer.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return fmt.Errorf("controller console connection failed: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	request := "POST /api/v1/agent/console/" + url.PathEscape(req.Token) + " HTTP/1.1\r\nHost: " + u.Host + "\r\nConnection: Upgrade\r\nUpgrade: nex-console\r\nContent-Length: 0\r\n\r\n"
	if _, err = conn.Write([]byte(request)); err != nil {
		return err
	}
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		return fmt.Errorf("controller console response: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("controller refused console session (HTTP %d)", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); vnc.Close() })
	defer stop()
	wsock.Pipe(wsock.Hijacked{R: r, Conn: conn}, vnc)
	return nil
}
