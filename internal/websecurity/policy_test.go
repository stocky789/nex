// SPDX-License-Identifier: AGPL-3.0-only
package websecurity

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGatePeerIsolationCapacityAndExpiry(t *testing.T) {
	var g LoginGate
	now := time.Now()
	for i := 0; i < 5; i++ {
		if !g.Acquire("192.0.2.1:123", now) {
			t.Fatal("initial burst denied")
		}
		g.Release()
	}
	if g.Acquire("192.0.2.1:456", now) {
		t.Fatal("port bypassed limit")
	}
	if !g.Acquire("192.0.2.2:123", now) || !g.Acquire("192.0.2.3:123", now) {
		t.Fatal("another peer denied")
	}
	if g.Acquire("192.0.2.4:123", now) {
		t.Fatal("global hash capacity exceeded")
	}
	g.Release()
	g.Release()
	if !g.Acquire("192.0.2.1:123", now.Add(time.Minute)) {
		t.Fatal("peer failed to recover")
	}
	g.Release()
	if len(g.peers) != 1 {
		t.Fatal("expired peers retained")
	}
	for i := 0; i < 4096; i++ {
		g.peers[string(rune(i))] = peer{until: now.Add(time.Minute)}
	}
	if g.Acquire("new-peer", now) {
		t.Fatal("peer map unbounded")
	}
}
func TestPasswordAndHeaders(t *testing.T) {
	for _, v := range []string{"", "a\x00", "a\r", "a\n", strings.Repeat("a", 1025), strings.Repeat("a", 16) + "\xff"} {
		if ValidNewPassword(v) {
			t.Fatal("invalid password accepted")
		}
	}
	for _, v := range []string{"a", "é", " 🔑 ", " ", strings.Repeat("é", 512)} {
		if !ValidNewPassword(v) {
			t.Fatal("valid password rejected")
		}
	}
	w := httptest.NewRecorder()
	Headers(w)
	for k, v := range map[string]string{"Cache-Control": "no-store", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "Permissions-Policy": "camera=(), microphone=(), geolocation=()"} {
		if w.Header().Get(k) != v {
			t.Fatal(k)
		}
	}
	for _, v := range []string{"object-src 'none'", "form-action 'self'", "base-uri 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), v) {
			t.Fatal(v)
		}
	}
	if w.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("unexpected HSTS")
	}
}
