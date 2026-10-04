// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestStatsRelay(t *testing.T) {
	oldWait, oldQuery := statsAgentWait, statsQueryWait
	defer func() { statsAgentWait, statsQueryWait = oldWait, oldQuery }()
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	pool := a.poolOf(host.HostID)
	vm := "0f3f7a86-1d0e-4e3c-a1a4-7c5d3f1e2b90"
	path := "/api/v1/vms/" + vm + "/stats?range=24h"

	cookie := a.cookie
	a.cookie = nil
	a.call("GET", path, nil, nil, 401)
	a.cookie = cookie
	a.call("GET", path, nil, nil, 404)
	a.c.db.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", vm, pool, host.HostID, "controller", "adopt-1")
	a.call("GET", path, nil, nil, 409) // never reported: stale
	sequence := uint64(0)
	report := func(caps ...string) protocol.ReportResponse {
		sequence++
		now := time.Now().UTC()
		inv := &protocol.Inventory{ObservedAt: now, Hostname: "core", CPUCount: 4, MemoryBytes: 8 << 30, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: now}}}
		return a.report(cert, protocol.ReportRequest{Version: 1, Sequence: sequence, Capabilities: append([]string{protocol.ManagedVMCapability}, caps...), Inventory: inv})
	}
	report()
	if body := a.call("GET", path, nil, nil, 409).Body.String(); !strings.Contains(body, "upgrade Nex Core") {
		t.Fatal(body)
	}
	report(protocol.UsageHistoryCapability)
	if body := a.call("GET", "/api/v1/vms/"+vm+"/stats?range=2d", nil, nil, 400).Body.String(); !strings.Contains(body, "live") {
		t.Fatal(body)
	}
	a.call("GET", "/api/v1/hosts/nope/stats?range=10m", nil, nil, 404)

	// query runs a browser request in the background; it blocks until the host answers.
	query := func(p string) chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest("GET", "https://nex.test"+p, nil)
			r.AddCookie(a.cookie)
			w := httptest.NewRecorder()
			a.c.Handler().ServeHTTP(w, r)
			done <- w
		}()
		return done
	}
	// token waits for the pending channel to reach the host's next report, exactly once.
	token := func() string {
		for i := 0; i < 100; i++ {
			if got := report(protocol.UsageHistoryCapability).StatsChannels; len(got) == 1 {
				if again := report(protocol.UsageHistoryCapability).StatsChannels; len(again) != 0 {
					t.Fatal("stats channel delivered twice")
				}
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
		case <-time.After(time.Minute):
			t.Fatal("query never finished")
		}
		return nil
	}

	// The host never dials.
	statsAgentWait = 100 * time.Millisecond
	done := query(path)
	token()
	result(done, 503)
	statsAgentWait = 5 * time.Second

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agent/stats/{token}", func(w http.ResponseWriter, r *http.Request) { a.c.agentStats(w, r, host.HostID) })
	mux.HandleFunc("POST /api/v1/other/stats/{token}", func(w http.ResponseWriter, r *http.Request) { a.c.agentStats(w, r, "other-host") })
	server := httptest.NewServer(mux)
	defer server.Close()
	dial := func(p string) (net.Conn, *bufio.Reader, int) {
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(time.Minute))
		fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: nex-stats\r\nContent-Length: 0\r\n\r\n", p)
		reader := bufio.NewReader(conn)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		return conn, reader, resp.StatusCode
	}
	// answer serves one query the way a Core agent would.
	answer := func(conn net.Conn, reader *bufio.Reader, status int) {
		req, err := http.ReadRequest(reader)
		if err != nil {
			t.Error(err)
			return
		}
		q := req.URL.Query()
		body := `{"error":"not found"}`
		if status == 200 {
			b, _ := json.Marshal(protocol.StatsHistory{Object: q.Get("object"), ID: q.Get("id"), Range: q.Get("range"), StepSeconds: 60, Start: time.Now().UTC(), Series: []protocol.StatsSeries{{Key: "cpu.total", Group: "cpu", Label: "CPU", Unit: "percent", Values: make([]*float64, 1440)}}})
			body = string(b)
		}
		fmt.Fprintf(conn, "HTTP/1.1 %d X\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", status, len(body), body)
	}

	done = query(path)
	tok := token()
	if _, _, code := dial("/api/v1/other/stats/" + tok); code != 404 {
		t.Fatalf("another host attached the channel: %d", code)
	}
	if _, _, code := dial("/api/v1/agent/stats/wrong"); code != 404 {
		t.Fatalf("unknown token attached: %d", code)
	}
	conn, reader, code := dial("/api/v1/agent/stats/" + tok)
	if code != 101 {
		t.Fatalf("stats upgrade %d", code)
	}
	answer(conn, reader, 200)
	var history protocol.StatsHistory
	read(t, result(done, 200), &history)
	if history.ID != vm || history.HostID != host.HostID || history.Range != "24h" || len(history.Series) != 1 {
		t.Fatalf("history %+v", history)
	}
	// A second query reuses the open channel: no new request reaches the host.
	done = query(path)
	answer(conn, reader, 404)
	result(done, 404)
	if got := report(protocol.UsageHistoryCapability).StatsChannels; len(got) != 0 {
		t.Fatalf("open channel requested again: %+v", got)
	}
	if _, _, code := dial("/api/v1/agent/stats/" + tok); code != 404 {
		t.Fatalf("second connection attached: %d", code)
	}

	// A host that stops answering times out, and the next query opens a new channel.
	statsQueryWait = 100 * time.Millisecond
	result(query(path), 504)
	statsQueryWait = 5 * time.Second
	done = query("/api/v1/hosts/" + host.HostID + "/stats?range=10m")
	next := token()
	if next == tok {
		t.Fatal("closed channel token reused")
	}
	conn, reader, code = dial("/api/v1/agent/stats/" + next)
	if code != 101 {
		t.Fatalf("reopened stats upgrade %d", code)
	}
	req, err := http.ReadRequest(reader)
	if err != nil || req.URL.Query().Get("object") != "host" || req.URL.Query().Has("id") {
		t.Fatalf("host query %v %v", req, err)
	}
	body := `{"object":"host","range":"10m","step_seconds":5,"start":"2026-09-16T00:00:00Z","series":[]}`
	fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	read(t, result(done, 200), &history)
	if history.Object != "host" || history.ID != host.HostID || history.HostID != host.HostID {
		t.Fatalf("host history %+v", history)
	}
	// A reused channel the agent closed is replaced transparently, not reported as 502.
	conn.Close()
	done = query(path)
	next = token()
	conn, reader, code = dial("/api/v1/agent/stats/" + next)
	if code != 101 {
		t.Fatalf("replacement stats upgrade %d", code)
	}
	answer(conn, reader, 200)
	result(done, 200)

	// Live queries ride the same channel: step 1, exactly 120 points.
	reply := func(status int, body string) {
		if _, err := http.ReadRequest(reader); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "HTTP/1.1 %d X\r\nContent-Length: %d\r\n\r\n%s", status, len(body), body)
	}
	livePath := "/api/v1/vms/" + vm + "/stats?range=live"
	points := func(n int) string { return "[" + strings.TrimSuffix(strings.Repeat("1,", n), ",") + "]" }
	live := func(n int) string {
		return `{"object":"vm","id":"` + vm + `","range":"live","step_seconds":1,"start":"2026-09-16T00:00:00Z","series":[{"key":"cpu.total","group":"cpu","label":"CPU","unit":"percent","values":` + points(n) + `}]}`
	}
	done = query(livePath)
	reply(200, live(120))
	read(t, result(done, 200), &history)
	if history.Range != "live" || history.StepSeconds != 1 || len(history.Series[0].Values) != 120 {
		t.Fatalf("live history %+v", history)
	}
	done = query(livePath)
	reply(429, `{"error":"live sampling limit reached (8 objects)"}`)
	if body := result(done, 429).Body.String(); !strings.Contains(body, "live sampling limit reached (8 objects)") {
		t.Fatal(body)
	}
	// A host message too long or unprintable to show an operator is replaced.
	for _, message := range []string{strings.Repeat("x", 201), "limit\u001b[31m reached"} {
		done = query(livePath)
		b, _ := json.Marshal(map[string]string{"error": message})
		reply(429, string(b))
		if body := result(done, 429).Body.String(); !strings.Contains(body, "already sampling its maximum number of objects") {
			t.Fatal(body)
		}
	}
	done = query(livePath)
	reply(200, live(121))
	result(done, 502)
	// A short series breaks the one-value-per-step contract as well.
	done = query(livePath)
	reply(200, live(3))
	result(done, 502)
	// The channel survives the refusals: the next query needs no new channel request.
	done = query(livePath)
	reply(200, live(120))
	result(done, 200)

	// A reply that breaks the contract is refused, not relayed.
	done = query(path)
	req, _ = http.ReadRequest(reader)
	body = `{"object":"vm","id":"someone-else","range":"24h","step_seconds":60,"series":[]}`
	fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	result(done, 502)

	// A fresh channel's endless header is cut off at the byte bound: 502, not memory growth.
	conn.Close()
	done = query(path)
	conn, reader, code = dial("/api/v1/agent/stats/" + token())
	if code != 101 {
		t.Fatalf("header-limit stats upgrade %d", code)
	}
	go func() {
		http.ReadRequest(reader)
		fmt.Fprint(conn, "HTTP/1.1 200 OK\r\n")
		line := "X-Filler: " + strings.Repeat("a", 1000) + "\r\n"
		for i := 0; i < 5000; i++ {
			if _, err := fmt.Fprint(conn, line); err != nil {
				return
			}
		}
	}()
	result(done, 502)
	conn.Close()
}

// An earlier waiter's timeout must not end a later waiter's own wait on the same channel.
func TestStatsWaitersKeepTheirOwnDeadline(t *testing.T) {
	c := &Controller{}
	first, _ := c.stats.open("h")
	time.Sleep(20 * time.Millisecond)
	second, _ := c.stats.open("h")
	if first != second {
		t.Fatal("waiters did not share the pending channel")
	}
	if c.stats.done("h", first) {
		t.Fatal("unattached channel reported attached")
	}
	select {
	case <-second.ready:
		t.Fatal("later waiter woken by an earlier timeout")
	default:
	}
	if got := c.stats.pending("h"); len(got) != 1 {
		t.Fatalf("pending channel lost: %+v", got)
	}
	if c.stats.done("h", second) {
		t.Fatal("unattached channel reported attached")
	}
	select {
	case <-second.ready:
	default:
		t.Fatal("last waiter did not close the unattached channel")
	}
	if c.stats.channels["h"] != nil {
		t.Fatal("closed channel still registered")
	}
}
