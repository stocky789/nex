// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type stalledLoginBody struct {
	entered chan struct{}
	release chan struct{}
}

func (b *stalledLoginBody) Read(p []byte) (int, error) {
	close(b.entered)
	<-b.release
	return 0, io.EOF
}
func (b *stalledLoginBody) Close() error { return nil }
func TestIncompleteLoginDoesNotReserveHashCapacity(t *testing.T) {
	a := newClient(t)
	for i := 0; i < 2; i++ {
		time.Sleep(120 * time.Millisecond)
		b := &stalledLoginBody{make(chan struct{}), make(chan struct{})}
		r := httptest.NewRequest("POST", "https://nex.test/api/v1/session", b)
		r.RemoteAddr = "192.0.2.10:4000"
		r.Header.Set("Content-Type", "application/json")
		done := make(chan struct{})
		go func() { a.c.Handler().ServeHTTP(httptest.NewRecorder(), r); close(done) }()
		select {
		case <-b.entered:
		case <-time.After(time.Second):
			t.Fatal("request did not read body")
		}
		defer func() { close(b.release); <-done }()
	}
	time.Sleep(120 * time.Millisecond)
	r := httptest.NewRequest("POST", "https://nex.test/api/v1/session", strings.NewReader(`{"username":"root","password":"a-long-test-password"}`))
	r.RemoteAddr = "192.0.2.20:4000"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.c.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("incomplete requests starved complete login: %d %s", w.Code, w.Body)
	}
}
func TestSessionIdleAbsoluteAndLegacyCookie(t *testing.T) {
	a := newClient(t)
	if a.cookie.Name != "__Host-nex_session" || a.cookie.Domain != "" || a.cookie.Path != "/" {
		t.Fatal("unsafe cookie scope")
	}
	var before int64
	a.c.db.QueryRow("SELECT idle_expires FROM sessions").Scan(&before)
	a.call("GET", "/api/v1/session", nil, nil, 200)
	var after int64
	a.c.db.QueryRow("SELECT idle_expires FROM sessions").Scan(&after)
	if before != after {
		t.Fatal("poll extended idle deadline")
	}
	a.c.db.Exec("UPDATE sessions SET idle_expires=?", time.Now().Add(time.Minute).Unix())
	a.call("POST", "/api/v1/sites", map[string]string{"name": "idle-refresh"}, nil, 201)
	a.c.db.QueryRow("SELECT idle_expires FROM sessions").Scan(&after)
	if after < time.Now().Add(29*time.Minute).Unix() {
		t.Fatal("mutation did not extend idle deadline")
	}
	a.cookie.Name = "nex_session"
	a.call("GET", "/api/v1/session", nil, nil, 401)
	a.cookie.Name = "__Host-nex_session"
	a.c.db.Exec("UPDATE sessions SET idle_expires=?", time.Now().Add(-time.Second).Unix())
	a.call("GET", "/api/v1/session", nil, nil, 401)
	a.c.db.Exec("UPDATE sessions SET idle_expires=?,expires=?", time.Now().Add(time.Hour).Unix(), time.Now().Add(-time.Second).Unix())
	a.call("GET", "/api/v1/session", nil, nil, 401)
}
func TestControllerLoginPeerIgnoresForwardedHeaders(t *testing.T) {
	a := newClient(t)
	attempt := func(peer, forward string) int {
		r := httptest.NewRequest("POST", "https://nex.test/api/v1/session", strings.NewReader(`{"username":"root","password":"wrong"}`))
		r.RemoteAddr = peer
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", forward)
		r.Header.Set("Forwarded", "for="+forward)
		w := httptest.NewRecorder()
		a.c.Handler().ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 5; i++ {
		if code := attempt("203.0.113.1:10", "198.51.100.1"); code != 401 {
			t.Fatal(code)
		}
	}
	if code := attempt("203.0.113.1:11", "198.51.100.2"); code != 429 {
		t.Fatal("proxy header bypass", code)
	}
	if code := attempt("192.0.2.2:10", "198.51.100.1"); code != 401 {
		t.Fatal("other peer starved", code)
	}
}
func TestControllerSessionCapPrunesExpired(t *testing.T) {
	a := newClient(t)
	for i := 0; i < 63; i++ {
		_, err := a.c.db.Exec("INSERT INTO sessions(token,username,csrf,expires,idle_expires) VALUES(?, 'root','csrf',?,?)", strings.Repeat("x", i+1), time.Now().Add(time.Hour).Unix(), time.Now().Add(time.Minute).Unix())
		if err != nil {
			t.Fatal(err)
		}
	}
	a.call("POST", "/api/v1/session", map[string]string{"username": "root", "password": "a-long-test-password"}, nil, 429)
	a.c.db.Exec("UPDATE sessions SET idle_expires=0")
	a.call("POST", "/api/v1/session", map[string]string{"username": "root", "password": "a-long-test-password"}, nil, 200)
	var count int
	a.c.db.QueryRow("SELECT count(*) FROM sessions").Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
}
func TestExistingDatabaseGetsIdleDeadlineAndKeepsPassword(t *testing.T) {
	a := newClient(t)
	cfg := a.c.cfg
	// Simulate the original four-column session schema and existing shorter hash.
	salt := "legacy"
	hash, _ := pbkdf2.Key(sha256.New, "old-password", []byte(salt), 600000, 32)
	if _, err := a.c.db.Exec("UPDATE admins SET salt=?,password=?", salt, hex.EncodeToString(hash)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.c.db.Exec("ALTER TABLE sessions DROP COLUMN idle_expires"); err != nil {
		t.Fatal(err)
	}
	a.c.Close()
	cfg.Password = ""
	c, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.c = c
	a.call("GET", "/api/v1/session", nil, nil, 401)
	a.call("POST", "/api/v1/session", map[string]string{"username": "root", "password": "old-password"}, nil, 200)
}

// Short passwords must bootstrap and authenticate unchanged, including Unicode and spaces.
func TestControllerShortPasswordBootstrapAndLogin(t *testing.T) {
	for _, password := range []string{"a", "é", " 🔑 "} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		c, err := Open(Config{DataDir: dir, Username: "root", Password: password})
		if err != nil {
			t.Fatal("short password bootstrap failed:", err)
		}
		a := &testClient{t: t, c: c}
		a.call("POST", "/api/v1/session", map[string]string{"username": "root", "password": password + "wrong"}, nil, 401)
		a.call("POST", "/api/v1/session", map[string]string{"username": "root", "password": password}, nil, 200)
		c.Close()
	}
}
