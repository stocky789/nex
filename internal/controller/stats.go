// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"nex.local/nex/internal/protocol"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Usage history lives on the hosts. The Controller only relays a query over a stats
// channel the host dials on request, and keeps nothing: a Controller restart closes every
// channel and the next query opens a new one.
var (
	statsAgentWait = 20 * time.Second // variables so tests can shorten them
	statsQueryWait = 10 * time.Second
	statsIdle      = 5 * time.Minute
)

const (
	statsBodyLimit   = 4 << 20
	statsHeaderLimit = 64 << 10
	statsSeriesLimit = 128
)

type statsChannel struct {
	token     string
	delivered bool // sent to the host in a report response
	waiters   int  // browser queries still waiting for the host to dial
	query     sync.Mutex
	ready     chan struct{} // closed once conn is attached or the channel dies
	readyOnce sync.Once
	dead      bool
	conn      net.Conn
	reader    *bufio.Reader
	idle      *time.Timer
}

type statsHub struct {
	mu       sync.Mutex
	channels map[string]*statsChannel // host → its one channel
}

// open returns the host's channel, creating a pending one when none is usable, and
// whether it was already attached. Every caller waits on it and then calls done.
func (h *statsHub) open(host string) (*statsChannel, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch := h.channels[host]; ch != nil && !ch.dead {
		ch.waiters++
		return ch, ch.conn != nil
	}
	if h.channels == nil {
		h.channels = map[string]*statsChannel{}
	}
	ch := &statsChannel{token: random(), ready: make(chan struct{}), waiters: 1}
	h.channels[host] = ch
	return ch, false
}

// done ends one caller's wait. It reports whether the channel is attached; the last
// waiter on a channel the host never dialed closes it, so each waiter keeps its own
// full wait instead of being woken by an earlier one's timeout.
func (h *statsHub) done(host string, ch *statsChannel) bool {
	h.mu.Lock()
	ch.waiters--
	attached := ch.conn != nil && !ch.dead
	last := ch.waiters == 0
	h.mu.Unlock()
	if !attached && last {
		h.close(host, ch)
	}
	return attached
}

func (h *statsHub) close(host string, ch *statsChannel) {
	h.mu.Lock()
	if h.channels[host] == ch {
		delete(h.channels, host)
	}
	ch.dead = true
	conn := ch.conn
	if ch.idle != nil {
		ch.idle.Stop()
	}
	ch.readyOnce.Do(func() { close(ch.ready) })
	h.mu.Unlock()
	// Closing a TLS connection can block on close_notify; never hold the hub lock for it,
	// because pending() runs inside the report handler's database transaction.
	if conn != nil {
		conn.Close()
	}
}

// pending returns this host's channel request once, like console requests.
func (h *statsHub) pending(host string) []protocol.StatsChannelRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := h.channels[host]
	if ch == nil || ch.delivered || ch.dead || ch.conn != nil {
		return nil
	}
	ch.delivered = true
	return []protocol.StatsChannelRequest{{Token: ch.token}}
}

func (c *Controller) vmStats(w http.ResponseWriter, r *http.Request, user, csrf string) {
	id := r.PathValue("id")
	host, revoked, _, err := c.vmHost(id)
	if err != nil {
		fail(w, 404, "VM not found")
		return
	}
	c.relayStats(w, r, "vm", id, host, revoked)
}

func (c *Controller) hostStats(w http.ResponseWriter, r *http.Request, user, csrf string) {
	id := r.PathValue("id")
	var revoked bool
	err := c.db.QueryRow("SELECT revoked FROM hosts WHERE id=?", id).Scan(&revoked)
	if err == sql.ErrNoRows {
		fail(w, 404, "host not found")
		return
	}
	if err != nil {
		fail(w, 500, "storage failed")
		return
	}
	c.relayStats(w, r, "host", id, id, revoked)
}

func (c *Controller) relayStats(w http.ResponseWriter, r *http.Request, object, id, host string, revoked bool) {
	rng := r.URL.Query().Get("range")
	tier, ok := protocol.StatsRanges[rng]
	if !ok {
		fail(w, 400, "range must be live, 10m, 24h, 30d or 1y")
		return
	}
	var raw string
	var caps []string
	c.db.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", host).Scan(&raw)
	json.Unmarshal([]byte(raw), &caps)
	c.mu.Lock()
	fresh := time.Since(c.seen[host]) <= c.cfg.StaleAfter
	c.mu.Unlock()
	if revoked || !fresh {
		fail(w, 409, "the host is not freshly connected; its usage history cannot be read now")
		return
	}
	if !slices.Contains(caps, protocol.UsageHistoryCapability) {
		fail(w, 409, "the host agent does not keep usage history; upgrade Nex Core")
		return
	}
	query := url.Values{"object": {object}, "range": {rng}}
	if object == "vm" {
		query.Set("id", id)
	}
	var status int
	var history protocol.StatsHistory
	var refusal string
	// Queries are read-only, so a broken channel that was already open (the agent
	// restarted, or closed it while idle) is retried once on a fresh channel.
	for attempt := 0; ; attempt++ {
		ch, reused := c.stats.open(host)
		timer := time.NewTimer(statsAgentWait)
		select {
		case <-ch.ready:
		case <-timer.C:
		case <-r.Context().Done():
		}
		timer.Stop()
		attached := c.stats.done(host, ch)
		if r.Context().Err() != nil {
			return
		}
		if !attached {
			fail(w, 503, "the host did not open a stats connection in time; check the host connection and retry")
			return
		}
		c.stats.mu.Lock()
		if !ch.dead {
			ch.idle.Reset(statsIdle) // before the query, so it cannot fire mid-query
		}
		c.stats.mu.Unlock()
		var err error
		status, history, refusal, err = ch.ask(query)
		if err == nil {
			break
		}
		c.stats.close(host, ch)
		var timeout net.Error
		timedOut := errors.As(err, &timeout) && timeout.Timeout()
		// A host that stopped answering would only stall a retry as well.
		if reused && attempt == 0 && !timedOut {
			continue
		}
		if timedOut {
			fail(w, 504, "the host did not answer the usage history query in time; retry")
		} else {
			fail(w, 502, "the host stats connection failed; retry")
		}
		return
	}
	switch {
	case status == 404:
		fail(w, 404, "the host has no usage history for this "+map[string]string{"vm": "VM", "host": "host"}[object])
		return
	case status == 429:
		// Live sampling is at the host's cap. The host's own message says so; it is shown
		// only when short and printable, otherwise a fixed one is.
		if refusal == "" || len(refusal) > 200 || strings.ContainsFunc(refusal, func(r rune) bool { return !unicode.IsPrint(r) }) {
			refusal = "the host is already sampling its maximum number of objects every second; retry later"
		}
		fail(w, 429, refusal)
		return
	case status != 200:
		fail(w, 502, "the host could not read its usage history")
		return
	}
	if !validHistory(history, object, id, rng, tier) {
		fail(w, 502, "the host returned malformed usage history")
		return
	}
	history.ID, history.HostID = id, host
	write(w, 200, history)
}

// ask sends one query on the channel; queries from concurrent browsers take turns.
// For a non-200 reply it also returns the host's error message.
func (ch *statsChannel) ask(query url.Values) (int, protocol.StatsHistory, string, error) {
	var history protocol.StatsHistory
	ch.query.Lock()
	defer ch.query.Unlock()
	if ch.conn == nil {
		return 0, history, "", errors.New("stats channel closed")
	}
	req, err := http.NewRequest("GET", "http://nex/stats?"+query.Encode(), nil)
	if err != nil {
		return 0, history, "", err
	}
	_ = ch.conn.SetDeadline(time.Now().Add(statsQueryWait))
	if err = req.Write(ch.conn); err != nil {
		return 0, history, "", err
	}
	// Headers and body together are bounded, so a misbehaving host cannot stream an
	// endless header. Responses are strictly one per request, so nothing is buffered
	// past this response.
	limited := &io.LimitedReader{R: ch.reader, N: statsBodyLimit + statsHeaderLimit}
	resp, err := http.ReadResponse(bufio.NewReader(limited), req)
	if err != nil {
		return 0, history, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, history, "", err
	}
	if limited.N <= 0 {
		return 0, history, "", errors.New("stats response too large")
	}
	_ = ch.conn.SetDeadline(time.Time{})
	if resp.StatusCode != 200 {
		var refused struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &refused)
		return resp.StatusCode, history, refused.Error, nil
	}
	if json.Unmarshal(body, &history) != nil {
		return 502, history, "", nil
	}
	return 200, history, "", nil
}

func validHistory(h protocol.StatsHistory, object, id, rng string, tier protocol.StatsTier) bool {
	// A host query carries no id; the host may name itself or leave it empty.
	if h.Object != object || h.Range != rng || h.StepSeconds != int(tier.Step/time.Second) || len(h.Series) > statsSeriesLimit {
		return false
	}
	if object == "vm" && h.ID != id || object == "host" && h.ID != "" && h.ID != id {
		return false
	}
	for _, s := range h.Series {
		if len(s.Values) != tier.Points {
			return false
		}
	}
	return true
}

// agentStats receives the host's outbound stats connection for its pending channel.
func (c *Controller) agentStats(w http.ResponseWriter, r *http.Request, host string) {
	if r.Header.Get("Upgrade") != "nex-stats" {
		fail(w, 400, "stats connection must upgrade to nex-stats")
		return
	}
	c.stats.mu.Lock()
	ch := c.stats.channels[host]
	usable := ch != nil && ch.token == r.PathValue("token") && !ch.dead && ch.conn == nil
	c.stats.mu.Unlock()
	if !usable {
		fail(w, 404, "stats channel unknown")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		fail(w, 500, "connection cannot carry a stats channel")
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		fail(w, 500, "connection cannot carry a stats channel")
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if _, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: nex-stats\r\n\r\n"); err == nil {
		err = rw.Flush()
	}
	if err != nil {
		conn.Close()
		return
	}
	c.stats.mu.Lock()
	if ch.dead || ch.conn != nil {
		c.stats.mu.Unlock()
		conn.Close()
		return
	}
	ch.conn = conn
	ch.reader = rw.Reader
	ch.idle = time.AfterFunc(statsIdle, func() { c.stats.close(host, ch) })
	ch.readyOnce.Do(func() { close(ch.ready) })
	c.stats.mu.Unlock()
}
