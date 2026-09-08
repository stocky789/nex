// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocalIdleCookieAndCredentialBounds(t *testing.T) {
	s, _ := testService(t)
	w := callLocal(s, "POST", "/api/local/v1/session", map[string]string{"username": "root", "password": "local-admin-password"}, "", "", "https://core.test")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != "__Host-nex_local_session" || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatal(cookie)
	}
	var data map[string]string
	json.Unmarshal(w.Body.Bytes(), &data)
	s.mu.Lock()
	before := s.sessions[cookie.Value]
	s.mu.Unlock()
	w = callLocal(s, "GET", "/api/local/v1/session", nil, cookie.Value, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	s.mu.Lock()
	after := s.sessions[cookie.Value]
	s.mu.Unlock()
	if before != after {
		t.Fatal("poll refreshed idle lifetime")
	}
	// Exercise the common authentication boundary without mutating host state.
	s.mu.Lock()
	short := before
	short.IdleExpires = time.Now().Add(time.Minute)
	s.sessions[cookie.Value] = short
	s.mu.Unlock()
	mutation := httptest.NewRequest("POST", "https://core.test/checked-mutation", nil)
	mutation.AddCookie(cookie)
	hit := false
	handler := s.auth(func(w http.ResponseWriter, r *http.Request) { hit = true })
	handler(httptest.NewRecorder(), mutation)
	s.mu.Lock()
	denied := s.sessions[cookie.Value]
	s.mu.Unlock()
	if hit || denied.IdleExpires != short.IdleExpires {
		t.Fatal("denied mutation refreshed session")
	}
	mutation.Header.Set("X-CSRF-Token", data["csrf_token"])
	handler(httptest.NewRecorder(), mutation)
	s.mu.Lock()
	refreshed := s.sessions[cookie.Value]
	s.mu.Unlock()
	if !hit || refreshed.IdleExpires.Before(time.Now().Add(29*time.Minute)) {
		t.Fatal("authenticated mutation did not refresh")
	}
	r := httptest.NewRequest("GET", "https://core.test/api/local/v1/session", nil)
	old := *cookie
	old.Name = "nex_local_session"
	r.AddCookie(&old)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("legacy cookie accepted")
	}
	for _, absolute := range []bool{false, true} {
		s.mu.Lock()
		v := before
		if absolute {
			v.Expires = time.Now().Add(-time.Second)
		} else {
			v.IdleExpires = time.Now().Add(-time.Second)
		}
		s.sessions[cookie.Value] = v
		s.mu.Unlock()
		if w = callLocal(s, "GET", "/api/local/v1/session", nil, cookie.Value, "", ""); w.Code != 401 {
			t.Fatal("expired session accepted")
		}
	}
	for _, v := range []map[string]string{{"username": strings.Repeat("a", 121), "password": "x"}, {"username": "root", "password": strings.Repeat("x", 1025)}, {"username": "root", "password": ""}} {
		if w = callLocal(s, "POST", "/api/local/v1/session", v, "", "", "https://core.test"); w.Code != 400 {
			t.Fatal("invalid credential length", w.Code)
		}
	}
}
func TestLocalLoginPeerIsolation(t *testing.T) {
	s, _ := testService(t)
	attempt := func(peer string) int {
		r := httptest.NewRequest("POST", "https://core.test/api/local/v1/session", strings.NewReader(`{"username":"root","password":"local-admin-password"}`))
		r.RemoteAddr = peer
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://core.test")
		r.Header.Set("X-Forwarded-For", randomID())
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 5; i++ {
		if code := attempt("203.0.113.1:10"); code != 200 {
			t.Fatal(code)
		}
	}
	if code := attempt("203.0.113.1:11"); code != 429 {
		t.Fatal("port/proxy bypass", code)
	}
	if code := attempt("203.0.113.2:10"); code != 200 {
		t.Fatal("peer starved", code)
	}
}
