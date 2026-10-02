// SPDX-License-Identifier: AGPL-3.0-only
// Package demo is an explicitly simulated, read-only UI adapter. It never connects
// to Nex Core, imports the Controller engine, or executes infrastructure operations.
package demo

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

const cookieName = "nex_demo_session"
const sessionLifetime = time.Hour

type Config struct {
	UIDir          string
	DocsDir        string
	SourceArchive  string
	PublicOrigin   string
	InsecureCookie bool // Development only; production uses HTTPS through its proxy.
}

type Server struct {
	config   Config
	secret   [32]byte
	root     *os.Root
	docs     *os.Root
	assets   map[string]bool
	fixtures *fixtures
	now      func() time.Time
}

func New(config Config) (*Server, error) {
	origin, err := url.Parse(config.PublicOrigin)
	if err != nil || origin == nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || (origin.Scheme != "https" && !(config.InsecureCookie && origin.Scheme == "http")) {
		return nil, errors.New("public-origin must be an HTTPS origin (HTTP only in explicit insecure development)")
	}
	if config.InsecureCookie && origin.Hostname() != "127.0.0.1" && origin.Hostname() != "localhost" && origin.Hostname() != "::1" {
		return nil, errors.New("insecure development origin must be loopback")
	}
	s := &Server{config: config, assets: make(map[string]bool), now: time.Now}
	if _, err := rand.Read(s.secret[:]); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(config.UIDir)
	if err != nil {
		return nil, fmt.Errorf("open UI: %w", err)
	}
	s.root = root
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("UI assets must not be symlinks")
		}
		if entry.IsDir() {
			return nil
		}
		ext := path.Ext(name)
		if name == "index.html" || ext == ".js" || ext == ".css" || ext == ".svg" || ext == ".png" || ext == ".ico" || ext == ".woff2" || strings.HasSuffix(name, ".js.LEGAL.txt") {
			s.assets["/"+name] = true
		}
		return nil
	})
	if err == nil && !s.assets["/index.html"] {
		err = errors.New("UI index.html missing")
	}
	if err == nil {
		var info os.FileInfo
		info, err = os.Stat(config.SourceArchive)
		if err == nil && (!info.Mode().IsRegular() || info.Size() == 0) {
			err = errors.New("corresponding source archive must be a nonempty regular file")
		}
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	if config.DocsDir == "" {
		config.DocsDir = "docs/api"
	}
	docs, err := os.OpenRoot(config.DocsDir)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("open API reference: %w", err)
	}
	entries, err := fs.ReadDir(docs.FS(), ".")
	if err == nil {
		for _, name := range []string{"index.html", "reference.css", "openapi.json"} {
			found := false
			for _, entry := range entries {
				if entry.Name() == name && entry.Type().IsRegular() {
					found = true
				}
			}
			if !found {
				err = fmt.Errorf("API reference %s must be a regular file", name)
				break
			}
		}
	}
	if err != nil {
		docs.Close()
		root.Close()
		return nil, err
	}
	s.docs = docs
	s.fixtures = newFixtures(s.now().UTC())
	return s, nil
}

func (s *Server) Close() error { return errors.Join(s.root.Close(), s.docs.Close()) }

// sourceWriteWindow bounds one /source download (about 1 Mbit/s for the archive).
const sourceWriteWindow = 20 * time.Minute

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) { reply(w, status, row{"error": message}) }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(s.config.PublicOrigin, "https://") {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	// Reject rather than redirect/normalize: encoded separators and dot paths must
	// never enter a more permissive static or authentication handler.
	if len(r.RequestURI) > 4096 || r.URL.RawPath != "" || strings.Contains(r.URL.Path, "\\") || (path.Clean(r.URL.Path) != r.URL.Path && r.URL.Path != "/api/docs/") || strings.Contains(r.URL.Path, "%") {
		fail(w, 400, "invalid request path")
		return
	}
	if r.Header.Get("Upgrade") != "" {
		fail(w, 403, "Consoles and upgraded connections are unavailable in the read-only demo.")
		return
	}
	if r.URL.Path == "/api/v1/session" {
		s.session(w, r)
		return
	}
	// This method gate applies to every path, including unknown/agent/local paths.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(w, 403, "Read-only simulated demo: infrastructure changes are disabled.")
		return
	}
	if r.URL.Path == "/healthz" {
		reply(w, 200, row{"status": "ok", "simulated": true})
		return
	}
	if r.URL.Path == "/source" {
		file, err := os.Open(s.config.SourceArchive)
		if err != nil {
			fail(w, 503, "source archive unavailable")
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			fail(w, 503, "source archive unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="nex-demo-source.tar.gz"`)
		// The listener-wide write timeout would cut the ~130 MB archive for slow clients.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(sourceWriteWindow))
		http.ServeContent(w, r, "nex-demo-source.tar.gz", info.ModTime(), file)
		return
	}
	document := ""
	switch r.URL.Path {
	case "/api/docs/", "/api/docs/index.html":
		document = "index.html"
	case "/api/docs/reference.css":
		document = "reference.css"
	case "/api/docs/openapi.json", "/api/openapi.json":
		document = "openapi.json"
	}
	if document != "" {
		file, err := s.docs.Open(document)
		if err != nil {
			fail(w, 503, "API reference unavailable")
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			fail(w, 503, "API reference unavailable")
			return
		}
		http.ServeContent(w, r, document, info.ModTime(), file)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if _, ok := s.authenticate(r); !ok {
			fail(w, 401, "Sign in with demo / demo.")
			return
		}
		if r.Method != http.MethodGet {
			fail(w, 405, "API reads require GET")
			return
		}
		s.readAPI(w, r)
		return
	}
	name := r.URL.Path
	if name == "/" {
		name = "/index.html"
	}
	if !s.assets[name] {
		fail(w, 404, "not found")
		return
	}
	file, err := s.root.Open(strings.TrimPrefix(name, "/"))
	if err != nil {
		fail(w, 404, "not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		fail(w, 404, "not found")
		return
	}
	if strings.HasPrefix(name, "/chunks/") {
		// Chunk file names embed a content hash.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}

type sessionClaim struct {
	Expires int64  `json:"exp"`
	Nonce   string `json:"nonce"`
}

func (s *Server) sign(value string) string {
	mac := hmac.New(sha256.New, s.secret[:])
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) authenticate(r *http.Request) (sessionClaim, bool) {
	var claim sessionClaim
	cookie, err := r.Cookie(cookieName)
	if err != nil || len(cookie.Value) > 512 {
		return claim, false
	}
	payload, signature, found := strings.Cut(cookie.Value, ".")
	if !found || !hmac.Equal([]byte(signature), []byte(s.sign(payload))) {
		return claim, false
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(data, &claim) != nil || len(claim.Nonce) != 43 || claim.Expires <= s.now().Unix() || claim.Expires > s.now().Add(sessionLifetime).Unix() {
		return sessionClaim{}, false
	}
	return claim, true
}
func (s *Server) csrf(claim sessionClaim) string { return s.sign("csrf:" + claim.Nonce) }
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	// JSON-only login cannot be submitted by a cross-site HTML form. Browsers
	// omitting Origin still send Fetch Metadata; only non-browser CLI may omit both.
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") == "" && r.Header.Get("Sec-Fetch-Mode") == ""
	}
	return origin == s.config.PublicOrigin
}
func (s *Server) setCookie(w http.ResponseWriter, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", HttpOnly: true, Secure: !s.config.InsecureCookie, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		claim, ok := s.authenticate(r)
		if !ok {
			fail(w, 401, "Sign in with demo / demo.")
			return
		}
		reply(w, 200, row{"username": "demo", "csrf_token": s.csrf(claim)})
	case http.MethodPost:
		if !s.originAllowed(r) {
			fail(w, 403, "same-origin authentication required")
			return
		}
		if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			fail(w, 415, "application/json required")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var credentials struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if decoder.Decode(&credentials) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			fail(w, 400, "invalid credentials body")
			return
		}
		if subtle.ConstantTimeCompare([]byte(credentials.Username), []byte("demo")) != 1 || subtle.ConstantTimeCompare([]byte(credentials.Password), []byte("demo")) != 1 {
			fail(w, 401, "Use the public demo credentials: demo / demo.")
			return
		}
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			fail(w, 503, "authentication unavailable")
			return
		}
		claim := sessionClaim{Expires: s.now().Add(sessionLifetime).Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonce[:])}
		data, _ := json.Marshal(claim)
		payload := base64.RawURLEncoding.EncodeToString(data)
		s.setCookie(w, payload+"."+s.sign(payload), time.Unix(claim.Expires, 0), int(sessionLifetime.Seconds()))
		reply(w, 200, row{"username": "demo", "csrf_token": s.csrf(claim)})
	case http.MethodDelete:
		claim, ok := s.authenticate(r)
		if !ok {
			fail(w, 401, "session expired")
			return
		}
		if !s.originAllowed(r) || !hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.csrf(claim))) {
			fail(w, 403, "same-origin session CSRF token required")
			return
		}
		s.setCookie(w, "", time.Unix(1, 0), -1)
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, 403, "unsupported session operation")
	}
}
