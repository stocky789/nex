// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	_ "github.com/mattn/go-sqlite3"
	"io"
	"net/http"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/websecurity"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	DataDir, Username, Password, UIPath, DocsPath, SourcePath string
	StaleAfter                                                time.Duration
	AlertDebounce                                             time.Duration
}
type Controller struct {
	db        *sql.DB
	cfg       Config
	mu        sync.Mutex
	seen      map[string]time.Time
	ca        *authority
	lock      *os.File
	loginGate websecurity.LoginGate
	observed  map[string]observationReceipt
	stop      chan struct{}
	stopped   chan struct{}
}

func Open(cfg Config) (*Controller, error) {
	if cfg.AlertDebounce <= 0 {
		cfg.AlertDebounce = 5 * time.Second
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = 30 * time.Second
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(cfg.DataDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("data directory must be a private real directory (0700)")
	}
	for _, name := range []string{"controller.db", "controller.db-wal", "controller.db-shm", "host-ca.pem", "host-ca-key.pem", "controller.lock"} {
		if info, e := os.Lstat(filepath.Join(cfg.DataDir, name)); e == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("controller state must be regular private files")
		}
	}
	lock, err := os.OpenFile(filepath.Join(cfg.DataDir, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another controller owns this data directory")
	}
	success := false
	defer func() {
		if !success {
			lock.Close()
		}
	}()
	dbfile, err := os.OpenFile(filepath.Join(cfg.DataDir, "controller.db"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	dbfile.Close()
	db, err := sql.Open("sqlite3", filepath.Join(cfg.DataDir, "controller.db")+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	defer func() {
		if !success {
			db.Close()
		}
	}()
	db.SetMaxOpenConns(1)
	var schemaExists int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_version'").Scan(&schemaExists); err != nil {
		return nil, err
	}
	if schemaExists > 0 {
		var count, version int
		if err = db.QueryRow("SELECT count(*),coalesce(min(version),0) FROM schema_version").Scan(&count, &version); err != nil || count != 1 || version != 1 {
			return nil, errors.New("unsupported database schema version")
		}
	}

	c := &Controller{db: db, cfg: cfg, seen: map[string]time.Time{}, lock: lock, observed: map[string]observationReceipt{}, stop: make(chan struct{}), stopped: make(chan struct{})}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS schema_version(version INTEGER NOT NULL); INSERT INTO schema_version SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM schema_version);
 CREATE TABLE IF NOT EXISTS admins(username TEXT PRIMARY KEY,salt TEXT NOT NULL,password TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS sessions(token TEXT PRIMARY KEY,username TEXT NOT NULL,csrf TEXT NOT NULL,expires INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS sites(id TEXT PRIMARY KEY,name TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS pools(id TEXT PRIMARY KEY,site_id TEXT NOT NULL REFERENCES sites(id),name TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS hosts(id TEXT PRIMARY KEY,pool_id TEXT NOT NULL REFERENCES pools(id),hostname TEXT NOT NULL,revoked INTEGER NOT NULL DEFAULT 0,last_seen TEXT,inventory TEXT,sequence INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS tokens(hash TEXT PRIMARY KEY,pool_id TEXT NOT NULL REFERENCES pools(id),host_id TEXT,expires INTEGER NOT NULL,csr TEXT,response TEXT);
 CREATE TABLE IF NOT EXISTS certificates(serial TEXT PRIMARY KEY,host_id TEXT NOT NULL REFERENCES hosts(id),csr TEXT NOT NULL,pem TEXT NOT NULL,expires INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS operations(id TEXT PRIMARY KEY,host_id TEXT NOT NULL REFERENCES hosts(id),kind TEXT NOT NULL,request_id TEXT NOT NULL UNIQUE,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,actor TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT,actor TEXT NOT NULL,action TEXT NOT NULL,resource_id TEXT NOT NULL,created_at TEXT NOT NULL,detail TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS appliance_bootstraps(request_id TEXT PRIMARY KEY,site_name TEXT NOT NULL,pool_name TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,site_id TEXT NOT NULL REFERENCES sites(id),pool_id TEXT NOT NULL REFERENCES pools(id));
 CREATE TABLE IF NOT EXISTS appliances(id TEXT PRIMARY KEY,pool_id TEXT NOT NULL REFERENCES pools(id),host_id TEXT NOT NULL REFERENCES hosts(id),name TEXT NOT NULL,request_id TEXT NOT NULL UNIQUE);
 CREATE TABLE IF NOT EXISTS health_states(resource_id TEXT PRIMARY KEY,status TEXT NOT NULL,detail TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// Add an idle deadline to existing databases. Old sessions expire immediately.
	rows, err := db.Query("PRAGMA table_info(sessions)")
	if err != nil {
		return nil, err
	}
	hasIdle := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		if name == "idle_expires" {
			hasIdle = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if !hasIdle {
		if _, err = db.Exec("ALTER TABLE sessions ADD COLUMN idle_expires INTEGER NOT NULL DEFAULT 0"); err != nil {
			return nil, err
		}
	}
	var version int
	if err = db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 1 {
		db.Close()
		return nil, errors.New("unsupported database schema version")
	}
	var n int
	if err = db.QueryRow("SELECT count(*) FROM admins").Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		if !websecurity.ValidNewPassword(cfg.Password) || !nameValid(cfg.Username) {
			return nil, errors.New("first start requires administrator username and a nonempty password (maximum 1024 UTF-8 bytes; no NUL or line breaks)")
		}
		salt := random()
		hash, _ := pbkdf2.Key(sha256.New, cfg.Password, []byte(salt), 600000, 32)
		_, err = db.Exec("INSERT INTO admins VALUES(?,?,?)", cfg.Username, salt, hex.EncodeToString(hash))
		if err != nil {
			return nil, err
		}
	}
	c.ca, err = loadAuthority(cfg.DataDir)
	if err != nil {
		db.Close()
		return nil, err
	}
	c.cfg.Password = ""
	success = true
	go c.monitorHealth()
	return c, nil
}
func (c *Controller) Close() error {
	close(c.stop)
	<-c.stopped
	err := c.db.Close()
	c.lock.Close()
	return err
}
func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func digest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func stamp() string          { return time.Now().UTC().Format(time.RFC3339Nano) }
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	write(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "invalid JSON request")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(w, 400, "multiple JSON values")
		return false
	}
	return true
}
func nameValid(s string) bool {
	return len(strings.TrimSpace(s)) > 0 && len(s) <= 200 && !strings.ContainsAny(s, "\r\n\x00")
}
func (c *Controller) audit(tx *sql.Tx, actor, action, id string) error {
	_, err := tx.Exec("INSERT INTO audit(actor,action,resource_id,created_at) VALUES(?,?,?,?)", actor, action, id, stamp())
	return err
}
func (c *Controller) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/v1/session", c.login)
	m.HandleFunc("GET /api/v1/session", c.admin(c.session))
	m.HandleFunc("DELETE /api/v1/session", c.admin(c.logout))
	m.HandleFunc("GET /api/v1/sites", c.admin(c.sites))
	m.HandleFunc("POST /api/v1/sites", c.admin(c.sites))
	m.HandleFunc("GET /api/v1/pools", c.admin(c.pools))
	m.HandleFunc("POST /api/v1/pools", c.admin(c.pools))
	m.HandleFunc("GET /api/v1/hosts", c.admin(c.hosts))
	m.HandleFunc("GET /api/v1/vms", c.admin(c.appliances))
	m.HandleFunc("POST /api/v1/appliances/adopt", c.admin(c.adoptAppliance))
	m.HandleFunc("POST /api/v1/appliances/bootstrap", c.admin(c.bootstrapAppliance))
	m.HandleFunc("POST /api/v1/enrollment-tokens", c.admin(c.token))
	m.HandleFunc("POST /api/v1/hosts/{id}/revoke", c.admin(c.revoke))
	m.HandleFunc("POST /api/v1/hosts/{id}/operations", c.admin(c.operation))
	m.HandleFunc("GET /api/v1/operations", c.admin(c.operations))
	m.HandleFunc("GET /api/v1/audit", c.admin(c.audits))
	m.HandleFunc("POST /api/v1/agent/enroll", c.enroll)
	m.HandleFunc("POST /api/v1/agent/report", c.agent(c.report))
	m.HandleFunc("POST /api/v1/agent/rotate", c.agent(c.rotate))
	m.HandleFunc("GET /source", func(w http.ResponseWriter, r *http.Request) {
		if c.cfg.SourcePath == "" {
			fail(w, 503, "matching source archive is not configured")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="nex-source.tar.gz"`)
		http.ServeFile(w, r, c.cfg.SourcePath)
	})
	m.Handle("GET /api/docs/", http.StripPrefix("/api/docs/", http.FileServer(http.Dir(c.cfg.DocsPath))))
	m.HandleFunc("GET /api/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(c.cfg.DocsPath, "openapi.json"))
	})
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "API route not found") })
	m.Handle("/", http.FileServer(http.Dir(c.cfg.UIPath)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		websecurity.Headers(w)
		m.ServeHTTP(w, r)
	})
}
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	return o == "" || o == "https://"+r.Host
}
func (c *Controller) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, 403, "origin rejected")
		return
	}
	var v struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &v) {
		return
	}
	if len(v.Password) == 0 || len(v.Password) > 1024 || !nameValid(v.Username) {
		fail(w, 400, "invalid credential length")
		return
	}
	if !c.loginGate.Acquire(r.RemoteAddr, time.Now()) {
		fail(w, 429, "login rate limited or busy; retry shortly")
		return
	}
	defer c.loginGate.Release()
	var salt, expected string
	err := c.db.QueryRow("SELECT salt,password FROM admins WHERE username=?", v.Username).Scan(&salt, &expected)
	if err != nil {
		salt = strings.Repeat("0", 64)
		expected = strings.Repeat("0", 64)
	}
	hash, _ := pbkdf2.Key(sha256.New, v.Password, []byte(salt), 600000, 32)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(hash)), []byte(expected)) != 1 || err != nil {
		fail(w, 401, "invalid credentials")
		return
	}
	token, csrf := random(), random()
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "session storage failed")
		return
	}
	defer tx.Rollback()
	now := time.Now()
	if _, err = tx.Exec("DELETE FROM sessions WHERE expires<=? OR idle_expires<=?", now.Unix(), now.Unix()); err != nil {
		fail(w, 500, "session storage failed")
		return
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil {
		fail(w, 500, "session storage failed")
		return
	}
	if count >= websecurity.SessionLimit {
		fail(w, 429, "session limit reached")
		return
	}
	_, err = tx.Exec("INSERT INTO sessions(token,username,csrf,expires,idle_expires) VALUES(?,?,?,?,?)", digest(token), v.Username, csrf, now.Add(websecurity.AbsoluteLifetime).Unix(), now.Add(websecurity.IdleLifetime).Unix())
	if err != nil {
		fail(w, 500, "session storage failed")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "session storage failed")
		return
	}

	http.SetCookie(w, &http.Cookie{Name: "__Host-nex_session", Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	write(w, 200, map[string]string{"username": v.Username, "csrf_token": csrf})
}

type adminHandler func(http.ResponseWriter, *http.Request, string, string)

func (c *Controller) admin(next adminHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("__Host-nex_session")
		if err != nil {
			fail(w, 401, "administrator login required")
			return
		}
		var user, csrf string
		err = c.db.QueryRow("SELECT username,csrf FROM sessions WHERE token=? AND expires>? AND idle_expires>?", digest(cookie.Value), time.Now().Unix(), time.Now().Unix()).Scan(&user, &csrf)
		if err != nil {
			fail(w, 401, "session expired or invalid")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && (!sameOrigin(r) || subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1) {
			fail(w, 403, "CSRF token required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if _, err := c.db.Exec("UPDATE sessions SET idle_expires=? WHERE token=?", time.Now().Add(websecurity.IdleLifetime).Unix(), digest(cookie.Value)); err != nil {
				fail(w, 500, "session storage failed")
				return
			}
		}
		next(w, r, user, csrf)
	}
}
func (c *Controller) session(w http.ResponseWriter, r *http.Request, user, csrf string) {
	write(w, 200, map[string]string{"username": user, "csrf_token": csrf})
}
func (c *Controller) logout(w http.ResponseWriter, r *http.Request, user, csrf string) {
	cookie, _ := r.Cookie("__Host-nex_session")
	if _, err := c.db.Exec("DELETE FROM sessions WHERE token=?", digest(cookie.Value)); err != nil {
		fail(w, 500, "logout storage failed")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "__Host-nex_session", Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	write(w, 200, map[string]bool{"ok": true})
}
func (c *Controller) sites(w http.ResponseWriter, r *http.Request, user, csrf string) {
	if r.Method == "POST" {
		var v struct {
			Name string `json:"name"`
		}
		if !decode(w, r, &v) {
			return
		}
		if !nameValid(v.Name) {
			fail(w, 400, "name required (maximum 200 characters)")
			return
		}
		id := random()
		tx, err := c.db.Begin()
		if err != nil {
			fail(w, 500, "storage unavailable")
			return
		}
		defer tx.Rollback()
		_, err = tx.Exec("INSERT INTO sites VALUES(?,?)", id, strings.TrimSpace(v.Name))
		if err == nil {
			err = c.audit(tx, user, "site.create", id)
		}
		if err != nil || tx.Commit() != nil {
			fail(w, 500, "site storage failed")
			return
		}
		write(w, 201, protocol.Site{ID: id, Name: strings.TrimSpace(v.Name)})
		return
	}
	rows, err := c.db.Query("SELECT id,name FROM sites ORDER BY name,id")
	if err != nil {
		fail(w, 500, "site lookup failed")
		return
	}
	defer rows.Close()
	out := []protocol.Site{}
	for rows.Next() {
		var v protocol.Site
		if err := rows.Scan(&v.ID, &v.Name); err != nil {
			fail(w, 500, "stored data decode failed")
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, "stored data read failed")
		return
	}
	write(w, 200, out)
}
func (c *Controller) pools(w http.ResponseWriter, r *http.Request, user, csrf string) {
	if r.Method == "POST" {
		var v struct {
			Name   string `json:"name"`
			SiteID string `json:"site_id"`
		}
		if !decode(w, r, &v) {
			return
		}
		if !nameValid(v.Name) {
			fail(w, 400, "name required (maximum 200 characters)")
			return
		}
		id := random()
		tx, err := c.db.Begin()
		if err != nil {
			fail(w, 500, "storage unavailable")
			return
		}
		defer tx.Rollback()
		_, err = tx.Exec("INSERT INTO pools VALUES(?,?,?)", id, v.SiteID, strings.TrimSpace(v.Name))
		if err != nil {
			fail(w, 400, "site does not exist")
			return
		}
		if c.audit(tx, user, "pool.create", id) != nil || tx.Commit() != nil {
			fail(w, 500, "pool storage failed")
			return
		}
		write(w, 201, protocol.Pool{ID: id, SiteID: v.SiteID, Name: strings.TrimSpace(v.Name)})
		return
	}
	rows, err := c.db.Query("SELECT id,site_id,name FROM pools ORDER BY name,id")
	if err != nil {
		fail(w, 500, "pool lookup failed")
		return
	}
	defer rows.Close()
	out := []protocol.Pool{}
	for rows.Next() {
		var v protocol.Pool
		if err := rows.Scan(&v.ID, &v.SiteID, &v.Name); err != nil {
			fail(w, 500, "stored data decode failed")
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, "stored data read failed")
		return
	}
	write(w, 200, out)
}
func (c *Controller) hosts(w http.ResponseWriter, r *http.Request, user, csrf string) {
	rows, err := c.db.Query("SELECT id,pool_id,hostname,revoked,last_seen,inventory FROM hosts ORDER BY hostname,id")
	if err != nil {
		fail(w, 500, "host lookup failed")
		return
	}
	defer rows.Close()
	out := []protocol.Host{}
	c.mu.Lock()
	defer c.mu.Unlock()
	for rows.Next() {
		var v protocol.Host
		var seen, inv sql.NullString
		if err := rows.Scan(&v.ID, &v.PoolID, &v.Hostname, &v.Revoked, &seen, &inv); err != nil {
			fail(w, 500, "stored data decode failed")
			return
		}
		v.Connection = "unknown"
		if seen.Valid {
			t, _ := time.Parse(time.RFC3339Nano, seen.String)
			v.LastSeen = &t
			v.Connection = "stale"
		}
		if t, ok := c.seen[v.ID]; ok && time.Since(t) <= c.cfg.StaleAfter {
			v.Connection = "fresh"
		}
		if v.Revoked {
			v.Connection = "revoked"
		}
		if inv.Valid {
			if err := json.Unmarshal([]byte(inv.String), &v.Inventory); err != nil {
				fail(w, 500, "stored inventory decode failed")
				return
			}
			for i := range v.Inventory.Checks {
				v.Inventory.Checks[i].Freshness = c.observationFreshness(v.ID + "/" + v.Inventory.Checks[i].Name)
			}
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, "stored data read failed")
		return
	}
	write(w, 200, out)
}
func (c *Controller) token(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v struct {
		PoolID string `json:"pool_id"`
		HostID string `json:"host_id"`
	}
	if !decode(w, r, &v) {
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var count int
	if tx.QueryRow("SELECT count(*) FROM pools WHERE id=?", v.PoolID).Scan(&count) != nil || count != 1 {
		fail(w, 400, "pool does not exist")
		return
	}
	if v.HostID != "" {
		if tx.QueryRow("SELECT count(*) FROM hosts WHERE id=? AND pool_id=? AND revoked=1", v.HostID, v.PoolID).Scan(&count) != nil || count != 1 {
			fail(w, 409, "re-enrollment requires a revoked host in the selected pool")
			return
		}
	}
	token := random()
	expires := time.Now().Add(10 * time.Minute)
	_, err = tx.Exec("INSERT INTO tokens(hash,pool_id,host_id,expires) VALUES(?,?,?,?)", digest(token), v.PoolID, v.HostID, expires.Unix())
	if err != nil || c.audit(tx, user, "enrollment-token.create", v.PoolID) != nil || tx.Commit() != nil {
		fail(w, 500, "token storage failed")
		return
	}
	write(w, 201, map[string]any{"token": token, "expires_at": expires.UTC()})
}
func (c *Controller) revoke(w http.ResponseWriter, r *http.Request, user, csrf string) {
	id := r.PathValue("id")
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE hosts SET revoked=1 WHERE id=?", id)
	if err != nil {
		fail(w, 500, "revocation failed")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "host not found")
		return
	}
	if _, err = tx.Exec("DELETE FROM certificates WHERE host_id=?", id); err != nil {
		fail(w, 500, "revocation storage failed")
		return
	}
	if _, err = tx.Exec("DELETE FROM tokens WHERE host_id=?", id); err != nil {
		fail(w, 500, "revocation storage failed")
		return
	}
	if c.audit(tx, user, "host.revoke", id) != nil || tx.Commit() != nil {
		fail(w, 500, "revocation failed")
		return
	}
	write(w, 200, map[string]bool{"ok": true})
}
func scanOperation(row interface{ Scan(...any) error }) (protocol.Operation, error) {
	var v protocol.Operation
	var created, updated string
	err := row.Scan(&v.ID, &v.HostID, &v.Kind, &v.RequestID, &v.Status, &v.Error, &created, &updated, &v.Actor)
	v.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return v, err
}
func (c *Controller) operation(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v struct {
		Kind      string `json:"kind"`
		RequestID string `json:"request_id"`
	}
	if !decode(w, r, &v) {
		return
	}
	if v.Kind != "refresh-inventory" || !nameValid(v.RequestID) {
		fail(w, 400, "refresh-inventory and stable request_id required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	old, err := scanOperation(tx.QueryRow("SELECT * FROM operations WHERE request_id=?", v.RequestID))
	if err == nil {
		if old.HostID != r.PathValue("id") || old.Kind != v.Kind {
			fail(w, 409, "request_id already used for another operation")
			return
		}
		write(w, 200, old)
		return
	}
	var revoked bool
	if tx.QueryRow("SELECT revoked FROM hosts WHERE id=?", r.PathValue("id")).Scan(&revoked) != nil {
		fail(w, 404, "host not found")
		return
	}
	if revoked {
		fail(w, 409, "host is revoked")
		return
	}
	var n int
	if err = tx.QueryRow("SELECT count(*) FROM operations WHERE host_id=? AND status IN ('queued','dispatched')", r.PathValue("id")).Scan(&n); err != nil {
		fail(w, 500, "operation lookup failed")
		return
	}
	if n > 0 {
		fail(w, 409, "host already has a pending refresh")
		return
	}
	now := time.Now().UTC()
	op := protocol.Operation{ID: random(), HostID: r.PathValue("id"), Kind: v.Kind, RequestID: v.RequestID, Status: "queued", CreatedAt: now, UpdatedAt: now, Actor: user}
	_, err = tx.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", op.ID, op.HostID, op.Kind, op.RequestID, op.Status, "", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), user)
	if err != nil || c.audit(tx, user, "operation.create", op.ID) != nil || tx.Commit() != nil {
		fail(w, 500, "operation storage failed")
		return
	}
	write(w, 202, op)
}
func (c *Controller) operations(w http.ResponseWriter, r *http.Request, user, csrf string) {
	rows, err := c.db.Query("SELECT * FROM operations ORDER BY created_at DESC LIMIT 1000")
	if err != nil {
		fail(w, 500, "operation lookup failed")
		return
	}
	defer rows.Close()
	out := []protocol.Operation{}
	for rows.Next() {
		v, err := scanOperation(rows)
		if err != nil {
			fail(w, 500, "operation decode failed")
			return
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, "stored data read failed")
		return
	}
	write(w, 200, out)
}
func (c *Controller) audits(w http.ResponseWriter, r *http.Request, user, csrf string) {
	rows, err := c.db.Query("SELECT id,actor,action,resource_id,created_at,detail FROM audit ORDER BY id DESC LIMIT 1000")
	if err != nil {
		fail(w, 500, "audit lookup failed")
		return
	}
	defer rows.Close()
	out := []protocol.Audit{}
	for rows.Next() {
		var v protocol.Audit
		var t string
		if err := rows.Scan(&v.ID, &v.Actor, &v.Action, &v.ResourceID, &t, &v.Detail); err != nil {
			fail(w, 500, "stored data decode failed")
			return
		}
		v.CreatedAt, _ = time.Parse(time.RFC3339Nano, t)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		fail(w, 500, "stored data read failed")
		return
	}
	write(w, 200, out)
}
