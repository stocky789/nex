// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/wsock"
	"slices"
	"sync"
	"time"
)

// Console sessions are short-lived pairings between one browser WebSocket and one
// outbound host tunnel. They live in memory only: a Controller restart ends every
// console, which the browser reports, and the guest keeps running on its host.
const consoleTicketLifetime = 60 * time.Second

// consoleAgentWait is a variable so tests can shorten it.
var consoleAgentWait = 20 * time.Second

const consoleSessionLimit = 32

type consoleSession struct {
	vmID, hostID, user string
	expires            time.Time
	delivered          bool // sent to the host in a report response
	claimed            bool // browser attached
	agentClaimed       bool // host tunnel admitted, even after the browser takes it
	dead               bool // browser gave up; a late host tunnel is closed
	agent              chan net.Conn
}
type consoleHub struct {
	mu       sync.Mutex
	sessions map[string]*consoleSession // ticket → session; tickets never touch the database
}

func (h *consoleHub) sweep(now time.Time) {
	for key, s := range h.sessions {
		if now.After(s.expires) {
			s.dead = true
			select {
			case conn := <-s.agent:
				conn.Close()
			default:
			}
			delete(h.sessions, key)
		}
	}
}

// pending returns console requests not yet delivered to this host. Delivery is
// single-shot: a host that fails to connect leaves the browser to time out and retry.
func (h *consoleHub) pending(host string) []protocol.ConsoleRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []protocol.ConsoleRequest
	for ticket, s := range h.sessions {
		if s.hostID == host && !s.delivered && !s.dead {
			s.delivered = true
			out = append(out, protocol.ConsoleRequest{Token: ticket, VMID: s.vmID})
		}
	}
	return out
}

// vmHost resolves a known VM (adopted appliance or created guest) to its assigned host.
func (c *Controller) vmHost(id string) (host string, revoked bool, inventory sql.NullString, err error) {
	host, _, revoked, inventory, err = c.vmPlacement(id)
	return
}

// vmPlacement resolves a known VM to its pool, assigned host and that host's observation.
func (c *Controller) vmPlacement(id string) (host, pool string, revoked bool, inventory sql.NullString, err error) {
	err = c.db.QueryRow("SELECT h.id,a.pool_id,h.revoked,h.inventory FROM appliances a JOIN hosts h ON h.id=a.host_id WHERE a.id=?", id).Scan(&host, &pool, &revoked, &inventory)
	if err == sql.ErrNoRows {
		err = c.db.QueryRow("SELECT h.id,r.pool_id,h.revoked,h.inventory FROM managed_resources r JOIN operations o ON o.id=r.operation_id JOIN hosts h ON h.id=r.host_id WHERE r.kind='vm-create' AND r.id=? AND o.status='succeeded'", id).Scan(&host, &pool, &revoked, &inventory)
	}
	return
}

// openConsole issues a single-use console ticket for a running VM on a fresh host.
func (c *Controller) openConsole(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v struct{}
	if !decode(w, r, &v) {
		return
	}
	id := r.PathValue("id")
	host, revoked, inventory, err := c.vmHost(id)
	if err != nil {
		fail(w, 404, "VM not found")
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
		fail(w, 409, "the VM's host is not freshly connected; its console cannot be opened now")
		return
	}
	if !slices.Contains(caps, protocol.ConsoleCapability) {
		fail(w, 409, "the VM's host agent does not support browser consoles; upgrade Nex Core")
		return
	}
	if state, freshness := c.applianceObservation(host, id, revoked, inventory); freshness != "fresh" || state != "running" {
		fail(w, 409, "VM is not observed running; start it before opening its console")
		return
	}
	ticket := random()
	now := time.Now()
	c.consoles.mu.Lock()
	c.consoles.sweep(now)
	// ponytail: the cap bounds unpaired sessions only; count live pipes if viewers multiply.
	if len(c.consoles.sessions) >= consoleSessionLimit {
		c.consoles.mu.Unlock()
		fail(w, 429, "too many open console sessions; close one and retry")
		return
	}
	if c.consoles.sessions == nil {
		c.consoles.sessions = map[string]*consoleSession{}
	}
	c.consoles.sessions[ticket] = &consoleSession{vmID: id, hostID: host, user: user, expires: now.Add(consoleTicketLifetime), agent: make(chan net.Conn, 1)}
	c.consoles.mu.Unlock()
	// A tunnel parked for a browser that never opens its stream is closed at expiry.
	time.AfterFunc(consoleTicketLifetime+time.Second, func() {
		c.consoles.mu.Lock()
		c.consoles.sweep(time.Now())
		c.consoles.mu.Unlock()
	})
	tx, err := c.db.Begin()
	if err == nil {
		defer tx.Rollback()
		err = c.audit(tx, user, "console.open", id)
	}
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "audit storage failed")
		return
	}
	write(w, 201, map[string]any{"ticket": ticket, "vm_id": id, "host_id": host, "expires_at": now.Add(consoleTicketLifetime).UTC(), "websocket": "/api/v1/vms/" + id + "/console/" + ticket})
}

// openHostConsole issues a single-use console ticket for a fresh host's own console.
func (c *Controller) openHostConsole(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v struct{}
	if !decode(w, r, &v) {
		return
	}
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
	var raw string
	c.db.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", id).Scan(&raw)
	var caps []string
	json.Unmarshal([]byte(raw), &caps)
	c.mu.Lock()
	fresh := time.Since(c.seen[id]) <= c.cfg.StaleAfter
	c.mu.Unlock()
	if revoked || !fresh {
		fail(w, 409, "the host is not freshly connected; its console cannot be opened now")
		return
	}
	if !slices.Contains(caps, protocol.HostConsoleCapability) {
		fail(w, 409, "the host agent does not support browser consoles; upgrade Nex Core")
		return
	}
	ticket := random()
	now := time.Now()
	c.consoles.mu.Lock()
	c.consoles.sweep(now)
	if len(c.consoles.sessions) >= consoleSessionLimit {
		c.consoles.mu.Unlock()
		fail(w, 429, "too many open console sessions; close one and retry")
		return
	}
	if c.consoles.sessions == nil {
		c.consoles.sessions = map[string]*consoleSession{}
	}
	c.consoles.sessions[ticket] = &consoleSession{hostID: id, user: user, expires: now.Add(consoleTicketLifetime), agent: make(chan net.Conn, 1)}
	c.consoles.mu.Unlock()
	time.AfterFunc(consoleTicketLifetime+time.Second, func() {
		c.consoles.mu.Lock()
		c.consoles.sweep(time.Now())
		c.consoles.mu.Unlock()
	})
	tx, err := c.db.Begin()
	if err == nil {
		defer tx.Rollback()
		err = c.audit(tx, user, "host.console.open", id)
	}
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "audit storage failed")
		return
	}
	write(w, 201, map[string]any{"ticket": ticket, "host_id": id, "expires_at": now.Add(consoleTicketLifetime).UTC(), "websocket": "/api/v1/hosts/" + id + "/console/" + ticket})
}

// browserConsole upgrades the administrator's WebSocket once the host tunnel arrives.
func (c *Controller) browserConsole(w http.ResponseWriter, r *http.Request, user, csrf string) {
	if !sameOrigin(r) || r.Header.Get("Origin") == "" {
		fail(w, 403, "origin rejected")
		return
	}
	key := r.PathValue("ticket")
	id := r.PathValue("id")
	c.consoles.mu.Lock()
	s := c.consoles.sessions[key]
	match := s != nil && s.user == user && !time.Now().After(s.expires) &&
		((s.vmID != "" && s.vmID == id) || (s.vmID == "" && s.hostID == id))
	if !match {
		c.consoles.mu.Unlock()
		fail(w, 404, "console ticket is unknown, expired or not yours; open the console again")
		return
	}
	if s.claimed {
		c.consoles.mu.Unlock()
		fail(w, 409, "console ticket already used")
		return
	}
	s.claimed = true
	c.consoles.mu.Unlock()
	var tunnel net.Conn
	select {
	case tunnel = <-s.agent:
	case <-time.After(consoleAgentWait):
		c.consoles.mu.Lock()
		s.dead = true
		delete(c.consoles.sessions, key)
		c.consoles.mu.Unlock()
		fail(w, 504, "the host did not connect the console in time; check the host connection and retry")
		return
	case <-r.Context().Done():
		c.consoles.mu.Lock()
		s.dead = true
		delete(c.consoles.sessions, key)
		c.consoles.mu.Unlock()
		return
	}
	ws, err := wsock.Accept(w, r)
	if err != nil {
		tunnel.Close()
		fail(w, 400, err.Error())
		return
	}
	c.consoles.mu.Lock()
	delete(c.consoles.sessions, key)
	c.consoles.mu.Unlock()
	wsock.Pipe(ws, tunnel)
	if s.vmID == "" {
		log.Printf("console closed: host %s user %s", s.hostID, user)
	} else {
		log.Printf("console closed: vm %s user %s", s.vmID, user)
	}
}

// agentConsole receives the host's outbound tunnel and hands it to the waiting browser.
func (c *Controller) agentConsole(w http.ResponseWriter, r *http.Request, host string) {
	c.consoles.mu.Lock()
	s := c.consoles.sessions[r.PathValue("token")]
	if s == nil || s.hostID != host || s.dead || time.Now().After(s.expires) || s.agentClaimed {
		c.consoles.mu.Unlock()
		fail(w, 404, "console session unknown")
		return
	}
	s.agentClaimed = true
	c.consoles.mu.Unlock()
	fall := func() {
		c.consoles.mu.Lock()
		s.agentClaimed = false
		c.consoles.mu.Unlock()
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		fall()
		fail(w, 500, "connection cannot carry a console")
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		fall()
		fail(w, 500, "connection cannot carry a console")
		return
	}
	_ = conn.SetDeadline(time.Time{})
	if _, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: nex-console\r\n\r\n"); err == nil {
		err = rw.Flush()
	}
	if err != nil {
		fall()
		conn.Close()
		return
	}
	c.consoles.mu.Lock()
	defer c.consoles.mu.Unlock()
	if s.dead {
		conn.Close()
		return
	}
	select {
	case s.agent <- wsock.Hijacked{R: rw.Reader, Conn: conn}:
	default:
		conn.Close()
	}
}
