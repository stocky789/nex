// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"nex.local/nex/internal/vmhost"
	"nex.local/nex/internal/websecurity"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type session struct {
	CSRF        string
	Expires     time.Time
	IdleExpires time.Time
}

func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// The helper authenticates root against the host PAM policy; passwords only travel on stdin.
func authenticateRoot(ctx context.Context, password string) error {
	return runRootAuth(ctx, "/usr/lib/nex/nex-root-auth", password)
}
func runRootAuth(ctx context.Context, helper, password string) error {
	if len(password) == 0 || len(password) > 1024 || strings.ContainsRune(password, '\x00') {
		return errors.New("invalid credentials")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper)
	cmd.Stdin = strings.NewReader(password)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	cmd.WaitDelay = time.Second
	return cmd.Run()
}
func readPrivate(path string, out any) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return errors.New("state file must be regular and private")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func savePrivate(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		fail(w, 415, "application/json required")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "invalid JSON request")
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "one JSON object required")
		return false
	}
	return true
}
func (s *Service) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("__Host-nex_local_session")
		if err != nil {
			fail(w, 401, "sign in required")
			return
		}
		s.mu.Lock()
		sess, ok := s.sessions[cookie.Value]
		s.mu.Unlock()
		if !ok || !time.Now().Before(sess.Expires) || !time.Now().Before(sess.IdleExpires) {
			fail(w, 401, "session expired")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRF)) != 1 {
			fail(w, 403, "CSRF token required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			s.mu.Lock()
			// Do not resurrect a session concurrently removed by logout.
			if current, exists := s.sessions[cookie.Value]; exists {
				current.IdleExpires = time.Now().Add(websecurity.IdleLifetime)
				s.sessions[cookie.Value] = current
			}
			s.mu.Unlock()
		}
		// A served, authenticated local API call is the second way this host observes its
		// own reachability over a staged management network configuration, and the only one
		// left when the Controller is unreachable. It counts only when the listening socket
		// that accepted it holds an address of the management bridge and the caller is
		// somewhere else: an operator at the host console on 127.0.0.1 is exactly the person
		// who can confirm a change that reaches nobody. A no-op unless one is applied.
		local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if local != nil {
			_ = vmhost.RecordManagementConnectivity(s.cfg.AgentStateDir, "local-api", time.Now(),
				vmhost.ConnectionAddr(local.String()), vmhost.ConnectionAddr(r.RemoteAddr))
		}
		next(w, r)
	}
}
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &v) {
		return
	}
	if len(v.Username) == 0 || len(v.Username) > 120 || len(v.Password) == 0 || len(v.Password) > 1024 || strings.ContainsRune(v.Password, '\x00') {
		fail(w, 400, "invalid credential length")
		return
	}
	if !s.loginGate.Acquire(r.RemoteAddr, time.Now()) {
		fail(w, 429, "login rate limited or busy; retry shortly")
		return
	}
	defer s.loginGate.Release()
	if v.Username != "root" || s.cfg.Authenticate(r.Context(), v.Password) != nil {
		fail(w, 401, "invalid credentials")
		return
	}
	token, csrf := randomID(), randomID()
	s.mu.Lock()
	for key, v := range s.sessions {
		if !time.Now().Before(v.Expires) || !time.Now().Before(v.IdleExpires) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= websecurity.SessionLimit {
		s.mu.Unlock()
		fail(w, 429, "session limit reached")
		return
	}
	s.sessions[token] = session{csrf, time.Now().Add(websecurity.AbsoluteLifetime), time.Now().Add(websecurity.IdleLifetime)}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-nex_local_session", Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	reply(w, 200, map[string]string{"username": "root", "csrf_token": csrf})
}
func (s *Service) getSession(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("__Host-nex_local_session")
	s.mu.Lock()
	v := s.sessions[c.Value]
	s.mu.Unlock()
	reply(w, 200, map[string]string{"username": "root", "csrf_token": v.CSRF})
}
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("__Host-nex_local_session")
	s.mu.Lock()
	delete(s.sessions, c.Value)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-nex_local_session", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	reply(w, 200, map[string]bool{"ok": true})
}
