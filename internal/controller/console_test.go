// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
)

type blockedConsoleHijacker struct {
	http.ResponseWriter
	entered chan struct{}
	release chan struct{}
}

func (w blockedConsoleHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	close(w.entered)
	<-w.release
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

func TestConsoleAuthorizationPairingAndRelay(t *testing.T) {
	a := newClient(t)
	site := a.site()
	host := a.enroll(a.token(site, ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	pool := a.poolOf(host.HostID)
	vm := "0f3f7a86-1d0e-4e3c-a1a4-7c5d3f1e2b90"
	a.call("POST", "/api/v1/vms/"+vm+"/console", map[string]any{}, nil, 404)
	a.c.db.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", vm, pool, host.HostID, "controller", "adopt-1")
	// Host never reported: not fresh.
	read := func(w *httptest.ResponseRecorder) string { return w.Body.String() }
	if body := read(a.call("POST", "/api/v1/vms/"+vm+"/console", map[string]any{}, nil, 409)); !strings.Contains(body, "not freshly connected") {
		t.Fatal(body)
	}
	sequence := uint64(0)
	report := func(caps []string, state string) protocol.ReportResponse {
		sequence++
		now := time.Now().UTC()
		inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 4, MemoryBytes: 8 << 30, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}, VMs: []protocol.VM{{ID: vm, Name: "controller", State: state}}}
		return a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: caps, Inventory: inv})
	}
	report([]string{protocol.ManagedVMCapability}, "running")
	if body := read(a.call("POST", "/api/v1/vms/"+vm+"/console", map[string]any{}, nil, 409)); !strings.Contains(body, "does not support browser consoles") {
		t.Fatal(body)
	}
	report([]string{protocol.ManagedVMCapability, protocol.ConsoleCapability}, "shut off")
	if body := read(a.call("POST", "/api/v1/vms/"+vm+"/console", map[string]any{}, nil, 409)); !strings.Contains(body, "not observed running") {
		t.Fatal(body)
	}
	report([]string{protocol.ManagedVMCapability, protocol.ConsoleCapability}, "running")
	a.call("POST", "/api/v1/vms/"+vm+"/console", `{"extra":1}`, nil, 400)
	var ticket struct{ Ticket, WebSocket string }
	var raw map[string]any
	read2 := a.call("POST", "/api/v1/vms/"+vm+"/console", map[string]any{}, nil, 201)
	readJSON(t, read2, &raw)
	ticket.Ticket, _ = raw["ticket"].(string)
	ticket.WebSocket, _ = raw["websocket"].(string)
	if ticket.Ticket == "" || ticket.WebSocket != "/api/v1/vms/"+vm+"/console/"+ticket.Ticket || raw["host_id"] != host.HostID {
		t.Fatalf("ticket response %v", raw)
	}
	var audit []protocol.Audit
	readJSON(t, a.call("GET", "/api/v1/audit", nil, nil, 200), &audit)
	if audit[0].Action != "console.open" || audit[0].ResourceID != vm || audit[0].Actor != "root" {
		t.Fatalf("audit %+v", audit[0])
	}
	// The host learns about the request exactly once, on its next report.
	first := report([]string{protocol.ManagedVMCapability, protocol.ConsoleCapability}, "running")
	if len(first.Consoles) != 1 || first.Consoles[0].Token != ticket.Ticket || first.Consoles[0].VMID != vm {
		t.Fatalf("console request %+v", first.Consoles)
	}
	if again := report([]string{protocol.ManagedVMCapability, protocol.ConsoleCapability}, "running"); len(again.Consoles) != 0 {
		t.Fatal("console request delivered twice")
	}
	// Hijacking needs a real server: identity is injected the way the middleware would.
	mux := http.NewServeMux()
	hijackEntered, hijackRelease := make(chan struct{}), make(chan struct{})
	mux.HandleFunc("GET /api/v1/vms/{id}/console/{ticket}", func(w http.ResponseWriter, r *http.Request) {
		a.c.browserConsole(blockedConsoleHijacker{w, hijackEntered, hijackRelease}, r, "root", "")
	})
	mux.HandleFunc("POST /api/v1/agent/console/{token}", func(w http.ResponseWriter, r *http.Request) { a.c.agentConsole(w, r, host.HostID) })
	mux.HandleFunc("POST /api/v1/other/console/{token}", func(w http.ResponseWriter, r *http.Request) { a.c.agentConsole(w, r, "other-host") })
	server := httptest.NewServer(mux)
	defer server.Close()
	defer func() {
		select {
		case <-hijackRelease:
		default:
			close(hijackRelease)
		}
	}()
	origin := "https://" + strings.TrimPrefix(server.URL, "http://")
	dial := func(request string) (net.Conn, *http.Response) {
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(time.Minute))
		if _, err = conn.Write([]byte(request)); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		return conn, resp
	}
	wsRequest := func(path, originHeader string) string {
		return "GET " + path + " HTTP/1.1\r\nHost: " + strings.TrimPrefix(server.URL, "http://") + "\r\nOrigin: " + originHeader + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
	}
	agentRequest := func(path string) string {
		return "POST " + path + " HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: nex-console\r\nContent-Length: 0\r\n\r\n"
	}
	if _, resp := dial(wsRequest(ticket.WebSocket, "https://evil.test")); resp.StatusCode != 403 {
		t.Fatalf("cross-origin websocket accepted: %d", resp.StatusCode)
	}
	if _, resp := dial(wsRequest("/api/v1/vms/"+vm+"/console/wrong-ticket", origin)); resp.StatusCode != 404 {
		t.Fatalf("unknown ticket accepted: %d", resp.StatusCode)
	}
	if _, resp := dial(agentRequest("/api/v1/other/console/" + ticket.Ticket)); resp.StatusCode != 404 {
		t.Fatalf("another host attached the tunnel: %d", resp.StatusCode)
	}
	// sameOrigin compares against https://host; the plain test server mimics it via Origin.
	browserDone := make(chan *http.Response, 1)
	var browser net.Conn
	go func() {
		conn, resp := dial(wsRequest(ticket.WebSocket, origin))
		browser = conn
		browserDone <- resp
	}()
	tunnel, resp := dial(agentRequest("/api/v1/agent/console/" + ticket.Ticket))
	if resp.StatusCode != 101 {
		t.Fatalf("host tunnel %d", resp.StatusCode)
	}
	select {
	case <-hijackEntered:
	case <-time.After(time.Minute):
		t.Fatal("browser never consumed the host tunnel")
	}
	if _, resp := dial(agentRequest("/api/v1/agent/console/" + ticket.Ticket)); resp.StatusCode != 404 {
		t.Fatalf("second tunnel accepted: %d", resp.StatusCode)
	}
	close(hijackRelease)
	select {
	case resp := <-browserDone:
		if resp.StatusCode != 101 {
			t.Fatalf("browser upgrade %d", resp.StatusCode)
		}
	case <-time.After(time.Minute):
		t.Fatal("browser never paired")
	}
	// Host → browser: raw bytes become one binary frame.
	tunnel.Write([]byte("RFB 003.008\n"))
	frame := make([]byte, 2+12)
	if _, err := io.ReadFull(browser, frame); err != nil || frame[0] != 0x82 || frame[1] != 12 || string(frame[2:]) != "RFB 003.008\n" {
		t.Fatalf("host→browser frame %v %q", err, frame)
	}
	// Browser → host: masked frame payload reaches the tunnel unmasked.
	payload := []byte("RFB 003.008\n")
	mask := []byte{9, 8, 7, 6}
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	browser.Write(append(append([]byte{0x82, 0x80 | byte(len(payload))}, mask...), masked...))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(tunnel, got); err != nil || string(got) != string(payload) {
		t.Fatalf("browser→host relay %v %q", err, got)
	}
	if _, resp := dial(wsRequest(ticket.WebSocket, origin)); resp.StatusCode != 404 {
		t.Fatalf("consumed ticket reused: %d", resp.StatusCode)
	}
	tunnel.Close()
	if _, err := io.ReadFull(browser, frame[:2]); err != nil || frame[0] != 0x88 {
		t.Fatalf("browser not closed after host tunnel ended: %v %x", err, frame[0])
	}
	browser.Close()
}

func TestConsoleRejectsConcurrentHostTunnelBeforeUpgrade(t *testing.T) {
	c := &Controller{consoles: consoleHub{sessions: map[string]*consoleSession{
		"ticket": {hostID: "host", user: "root", expires: time.Now().Add(time.Minute), agent: make(chan net.Conn, 1)},
	}}}
	entered, release := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /console/{token}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Block-Hijack") == "1" {
			w = blockedConsoleHijacker{w, entered, release}
		}
		c.agentConsole(w, r, "host")
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		c.consoles.mu.Lock()
		c.consoles.sweep(time.Now().Add(2 * time.Minute))
		c.consoles.mu.Unlock()
	}()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Minute))
	if _, err := io.WriteString(conn, "POST /console/ticket HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: nex-console\r\nX-Test-Block-Hijack: 1\r\nContent-Length: 0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Minute):
		t.Fatal("first host tunnel did not reach its upgrade")
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/console/ticket", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "nex-console")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("second host tunnel admitted before the first upgrade: %d", response.StatusCode)
	}
	close(release)
	first, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || first.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("first host tunnel upgrade: response=%v error=%v", first, err)
	}
}

type failingHijacker struct{ http.ResponseWriter }

func (failingHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack failed")
}

func TestConsoleHostTunnelRetriesAfterFailedHijack(t *testing.T) {
	c := &Controller{consoles: consoleHub{sessions: map[string]*consoleSession{
		"ticket": {hostID: "host", user: "root", expires: time.Now().Add(time.Minute), agent: make(chan net.Conn, 1)},
	}}}
	req := httptest.NewRequest(http.MethodPost, "/console/ticket", nil)
	req.SetPathValue("token", "ticket")
	w := httptest.NewRecorder()
	c.agentConsole(failingHijacker{w}, req, "host")
	if w.Code != 500 {
		t.Fatalf("failed hijack answered %d", w.Code)
	}
	c.consoles.mu.Lock()
	claimed := c.consoles.sessions["ticket"].agentClaimed
	c.consoles.mu.Unlock()
	if claimed {
		t.Fatal("a failed hijack left the session unclaimable")
	}
}

func readJSON(t *testing.T, w *httptest.ResponseRecorder, out any) { t.Helper(); read(t, w, out) }

func TestConsoleBrowserTimesOutWithoutHost(t *testing.T) {
	c := &Controller{consoles: consoleHub{sessions: map[string]*consoleSession{}}}
	c.consoles.sessions["t"] = &consoleSession{vmID: "vm", hostID: "h", user: "root", expires: time.Now().Add(time.Minute), agent: make(chan net.Conn, 1)}
	old := consoleAgentWait
	consoleAgentWait = 50 * time.Millisecond
	defer func() { consoleAgentWait = old }()
	r := httptest.NewRequest("GET", "https://nex.test/api/v1/vms/vm/console/t", nil)
	r.Header.Set("Origin", "https://nex.test")
	r.SetPathValue("id", "vm")
	r.SetPathValue("ticket", "t")
	w := httptest.NewRecorder()
	c.browserConsole(w, r, "root", "")
	if w.Code != 504 || c.consoles.sessions["t"] != nil {
		t.Fatalf("timeout %d %v", w.Code, c.consoles.sessions)
	}
	// A late tunnel for the abandoned session is refused.
	r = httptest.NewRequest("POST", "https://nex.test/api/v1/agent/console/t", nil)
	r.SetPathValue("token", "t")
	w = httptest.NewRecorder()
	c.agentConsole(w, r, "h")
	if w.Code != 404 {
		t.Fatalf("late tunnel %d", w.Code)
	}
}

func TestHostConsoleTicket(t *testing.T) {
	a := newClient(t)
	site := a.site()
	host := a.enroll(a.token(site, ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	read := func(w *httptest.ResponseRecorder) string { return w.Body.String() }
	if body := read(a.call("POST", "/api/v1/hosts/"+host.HostID+"/console", map[string]any{}, nil, 409)); !strings.Contains(body, "not freshly connected") {
		t.Fatal(body)
	}
	sequence := uint64(0)
	report := func(caps []string) protocol.ReportResponse {
		sequence++
		now := time.Now().UTC()
		inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 4, MemoryBytes: 8 << 30, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
		return a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: caps, Inventory: inv})
	}
	report([]string{protocol.ManagedVMCapability})
	if body := read(a.call("POST", "/api/v1/hosts/"+host.HostID+"/console", map[string]any{}, nil, 409)); !strings.Contains(body, "does not support browser consoles") {
		t.Fatal(body)
	}
	// BUG-0056 regression: an old Core reporting console-v1 (VM consoles only, already
	// deployed everywhere) must not pass the host console gate on its own — otherwise a
	// new Controller issues a ticket the old agent can never fulfil and the browser hangs
	// until the 504 timeout instead of getting an immediate upgrade refusal.
	report([]string{protocol.ManagedVMCapability, protocol.ConsoleCapability})
	if body := read(a.call("POST", "/api/v1/hosts/"+host.HostID+"/console", map[string]any{}, nil, 409)); !strings.Contains(body, "does not support browser consoles") {
		t.Fatal(body)
	}
	report([]string{protocol.ManagedVMCapability, protocol.ConsoleCapability, protocol.HostConsoleCapability})
	var raw map[string]any
	readJSON(t, a.call("POST", "/api/v1/hosts/"+host.HostID+"/console", map[string]any{}, nil, 201), &raw)
	ticket, _ := raw["ticket"].(string)
	ws, _ := raw["websocket"].(string)
	if ticket == "" || ws != "/api/v1/hosts/"+host.HostID+"/console/"+ticket || raw["host_id"] != host.HostID {
		t.Fatalf("ticket response %v", raw)
	}
	var audit []protocol.Audit
	readJSON(t, a.call("GET", "/api/v1/audit", nil, nil, 200), &audit)
	if audit[0].Action != "host.console.open" || audit[0].ResourceID != host.HostID {
		t.Fatalf("audit %+v", audit[0])
	}
	first := report([]string{protocol.ManagedVMCapability, protocol.ConsoleCapability, protocol.HostConsoleCapability})
	if len(first.Consoles) != 1 || first.Consoles[0].Token != ticket || first.Consoles[0].VMID != "" {
		t.Fatalf("console request %+v", first.Consoles)
	}
}
