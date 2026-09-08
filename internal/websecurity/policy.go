// SPDX-License-Identifier: AGPL-3.0-only
package websecurity

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const IdleLifetime = 30 * time.Minute
const AbsoluteLifetime = 8 * time.Hour
const SessionLimit = 64

func ValidNewPassword(v string) bool {
	return utf8.ValidString(v) && len(v) > 0 && len(v) <= 1024 && !strings.ContainsAny(v, "\x00\r\n")
}
func Headers(w http.ResponseWriter) {
	for k, v := range map[string]string{
		"Cache-Control":           "no-store",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
		"Content-Security-Policy": "default-src 'self'; object-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'; img-src 'self' data:",
	} {
		w.Header().Set(k, v)
	}
}

type peer struct {
	attempts int
	until    time.Time
}

// LoginGate bounds expensive password checks; callers must finish decoding and
// validating requests before acquisition. Proxy headers never establish identity.
type LoginGate struct {
	mu     sync.Mutex
	peers  map[string]peer
	active int
}

func (g *LoginGate) Acquire(remote string, now time.Time) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.peers == nil {
		g.peers = make(map[string]peer)
	}
	for k, p := range g.peers {
		if !now.Before(p.until) {
			delete(g.peers, k)
		}
	}
	p, exists := g.peers[host]
	if p.attempts >= 5 || g.active >= 2 || (!exists && len(g.peers) >= 4096) {
		return false
	}
	if !exists {
		p.until = now.Add(time.Minute)
	}
	p.attempts++
	g.peers[host] = p
	g.active++
	return true
}
func (g *LoginGate) Release() { g.mu.Lock(); g.active--; g.mu.Unlock() }
