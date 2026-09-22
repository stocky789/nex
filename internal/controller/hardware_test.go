// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
)

// The hardware route relays one live observation over the same stats channel as usage
// history, so it refuses in the same ways: no session, unknown host, stale host, a Core too
// old to answer, no channel, no answer, and a reply outside the bounds.
func TestHostHardwareRelay(t *testing.T) {
	oldWait, oldQuery := statsAgentWait, statsQueryWait
	defer func() { statsAgentWait, statsQueryWait = oldWait, oldQuery }()
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	path := "/api/v1/hosts/" + host.HostID + "/hardware"

	cookie := a.cookie
	a.cookie = nil
	a.call("GET", path, nil, nil, 401)
	a.cookie = cookie
	a.call("GET", "/api/v1/hosts/nope/hardware", nil, nil, 404)
	a.call("GET", path, nil, nil, 409) // never reported: stale

	sequence := uint64(0)
	report := func(caps ...string) protocol.ReportResponse {
		sequence++
		now := time.Now().UTC()
		inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 4, MemoryBytes: 8 << 30}
		return a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence,
			Capabilities: append([]string{protocol.ManagedVMCapability, protocol.UsageHistoryCapability}, caps...), Inventory: inv})
	}
	report()
	if body := a.call("GET", path, nil, nil, 409).Body.String(); !strings.Contains(body, "upgrade Nex Core") {
		t.Fatal(body)
	}
	report(protocol.HostHardwareCapability)

	query := func() chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest("GET", "https://nex.test"+path, nil)
			r.AddCookie(a.cookie)
			w := httptest.NewRecorder()
			a.c.Handler().ServeHTTP(w, r)
			done <- w
		}()
		return done
	}
	token := func() string {
		for i := 0; i < 100; i++ {
			if got := report(protocol.HostHardwareCapability).StatsChannels; len(got) == 1 {
				return got[0].Token
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("stats channel never requested")
		return ""
	}
	result := func(done chan *httptest.ResponseRecorder, want int) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case w := <-done:
			if w.Code != want {
				t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
			}
			return w
		case <-time.After(5 * time.Second):
			t.Fatal("query never finished")
		}
		return nil
	}

	// The host never dials.
	statsAgentWait = 100 * time.Millisecond
	done := query()
	token()
	result(done, 503)
	statsAgentWait = 5 * time.Second

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agent/stats/{token}", func(w http.ResponseWriter, r *http.Request) { a.c.agentStats(w, r, host.HostID) })
	server := httptest.NewServer(mux)
	defer server.Close()
	dial := func(tok string) (net.Conn, *bufio.Reader) {
		t.Helper()
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprintf(conn, "POST /api/v1/agent/stats/%s HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: nex-stats\r\nContent-Length: 0\r\n\r\n", tok)
		reader := bufio.NewReader(conn)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil || resp.StatusCode != 101 {
			t.Fatalf("stats upgrade %v %v", resp, err)
		}
		return conn, reader
	}
	// reply answers one request the way a Core agent would, checking it asked for /hardware.
	reply := func(conn net.Conn, reader *bufio.Reader, body string) {
		t.Helper()
		req, err := http.ReadRequest(reader)
		if err != nil {
			t.Fatal(err)
		}
		if req.URL.Path != "/hardware" {
			t.Fatalf("agent asked for %q", req.URL.Path)
		}
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	}
	good := `{"observed_at":"2026-09-22T10:00:00Z","system":{"vendor":"Example Systems","model":"EX-2200","serial":"EXAMPLE0000001"},"cpu":{"model":"Example CPU E-16","sockets":2},"memory":{"unavailable":true,"reason":"dmidecode is not installed","total_bytes":137438953472},"disks":{"devices":[]},"pci":{"devices":[]},"sensors":{"temperatures":[{"key":"temp.coretemp-0000:00:18.3.1","chip":"coretemp","label":"CPU 0 package","max_celsius":95}]}}`

	done = query()
	conn, reader := dial(token())
	reply(conn, reader, good)
	var hw protocol.HostHardware
	read(t, result(done, 200), &hw)
	if hw.HostID != host.HostID || hw.System.Model != "EX-2200" || hw.CPU.Sockets != 2 {
		t.Fatalf("hardware %+v", hw)
	}
	// A section the host could not read keeps its reason and does not fail the relay.
	if !hw.Memory.Unavailable || hw.Memory.TotalBytes != 137438953472 {
		t.Fatalf("memory %+v", hw.Memory)
	}
	if len(hw.Sensors.Temperatures) != 1 || hw.Sensors.Temperatures[0].MaxCelsius == nil {
		t.Fatalf("sensors %+v", hw.Sensors)
	}

	// A reply past the bounds is refused, not relayed.
	done = query()
	devices := strings.TrimSuffix(strings.Repeat(`{"address":"0000:00:00.0"},`, protocol.HardwareMaxPCIDevices+1), ",")
	reply(conn, reader, `{"observed_at":"2026-09-22T10:00:00Z","pci":{"devices":[`+devices+`]}}`)
	result(done, 502)

	// A channel the agent closed is replaced transparently: the read-only query is retried
	// on a fresh channel rather than reported as a failure.
	conn.Close()
	done = query()
	conn, reader = dial(token())
	reply(conn, reader, good)
	result(done, 200)

	// A reply larger than the channel carries is cut off at the byte bound, not buffered.
	// It arrives on a channel this query opened itself, so there is no retry left.
	conn.Close()
	done = query()
	conn, reader = dial(token())
	reply(conn, reader, `{"observed_at":"2026-09-22T10:00:00Z","system":{"model":"`+strings.Repeat("x", statsBodyLimit+statsHeaderLimit)+`"}}`)
	result(done, 502)

	done = query()
	conn, reader = dial(token())
	reply(conn, reader, good)
	result(done, 200)

	// A host that stops answering times out.
	statsQueryWait = 100 * time.Millisecond
	result(query(), 504)
	conn.Close()
}
