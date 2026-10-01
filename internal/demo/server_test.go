// SPDX-License-Identifier: AGPL-3.0-only
package demo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"index.html": "<!doctype html><title>Demo</title>", "app.js": "export const demo = true;", "private.go": "private source must not be served", "app.js.map": "private source map must not be served"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	docs := t.TempDir()
	for name, content := range map[string]string{"index.html": "<!doctype html><title>API reference</title>", "reference.css": "main { color: black }", "openapi.json": `{"openapi":"3.1.0","paths":{}}`, "private.go": "private docs source must not be served"} {
		if err := os.WriteFile(filepath.Join(docs, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(t.TempDir(), "source.tar.gz")
	if err := os.WriteFile(source, []byte("exact matching test archive"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{UIDir: dir, DocsDir: docs, SourceArchive: source, PublicOrigin: "https://demo.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func request(s *Server, method, path, body string, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://demo.example.com"+path, strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	w := request(s, "POST", "/api/v1/session", `{"username":"demo","password":"demo"}`, nil, map[string]string{"Content-Type": "application/json", "Origin": "https://demo.example.com"})
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies: %v", cookies)
	}
	return cookies[0], session.CSRF
}

func TestSessionExpiryTamperingAndLogout(t *testing.T) {
	s := testServer(t)
	clock := time.Now()
	s.now = func() time.Time { return clock }
	cookie, csrf := login(t, s)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 3600 {
		t.Fatalf("unsafe session cookie: %v", cookie)
	}
	w := request(s, "GET", "/api/v1/session", "", cookie, nil)
	var observed struct {
		CSRF string `json:"csrf_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &observed)
	if w.Code != 200 || observed.CSRF != csrf {
		t.Fatalf("session token not stable: %d %s", w.Code, w.Body.String())
	}
	tampered := *cookie
	tampered.Value = "x" + tampered.Value[1:]
	if w := request(s, "GET", "/api/v1/vms", "", &tampered, nil); w.Code != 401 {
		t.Fatalf("tampered session accepted: %d", w.Code)
	}
	if w := request(s, "DELETE", "/api/v1/session", "", cookie, map[string]string{"Origin": "https://demo.example.com", "X-CSRF-Token": "wrong"}); w.Code != 403 {
		t.Fatalf("logout accepted wrong CSRF: %d", w.Code)
	}
	w = request(s, "DELETE", "/api/v1/session", "", cookie, map[string]string{"Origin": "https://demo.example.com", "X-CSRF-Token": csrf})
	if w.Code != 204 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("logout did not expire browser cookie: %d %v", w.Code, w.Result().Cookies())
	}
	clock = clock.Add(sessionLifetime)
	if w := request(s, "GET", "/api/v1/vms", "", cookie, nil); w.Code != 401 {
		t.Fatalf("expired cookie accepted: %d", w.Code)
	}
}

func TestLoginInputAndFixedOrigin(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct {
		name, body, origin, fetch string
		status                    int
	}{
		{"invalid credentials", `{"username":"demo","password":"invalid"}`, "https://demo.example.com", "", 401},
		{"unknown JSON field", `{"username":"demo","password":"demo","address":"https://target.example.test"}`, "https://demo.example.com", "", 400},
		{"oversized input", `{"username":"demo","password":"` + strings.Repeat("a", 2048) + `"}`, "https://demo.example.com", "", 400},
		{"trailing JSON", `{"username":"demo","password":"demo"} {}`, "https://demo.example.com", "", 400},
		{"foreign origin", `{"username":"demo","password":"demo"}`, "https://attacker.example.test", "", 403},
		{"null origin", `{"username":"demo","password":"demo"}`, "null", "", 403},
		{"browser missing origin", `{"username":"demo","password":"demo"}`, "", "same-origin", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(s, "POST", "/api/v1/session", tc.body, nil, map[string]string{"Content-Type": "application/json", "Origin": tc.origin, "Sec-Fetch-Site": tc.fetch})
			if w.Code != tc.status {
				t.Fatalf("got %d want %d", w.Code, tc.status)
			}
		})
	}
	// Failed public credentials never lock out another visitor.
	cookie, _ := login(t, s)
	if w := request(s, "GET", "/api/v1/sites", "", cookie, nil); w.Code != 200 {
		t.Fatalf("valid account locked: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "https://attacker.example.test/api/v1/session", strings.NewReader(`{"username":"demo","password":"demo"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://attacker.example.test")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("untrusted Host/XFP changed allowed origin: %d", w.Code)
	}
	if w := request(s, "POST", "/api/v1/session", `{"username":"demo","password":"demo"}`, nil, map[string]string{"Content-Type": "application/json"}); w.Code != 200 {
		t.Fatalf("CLI JSON login denied: %d", w.Code)
	}
	if w := request(s, "POST", "/api/v1/session", `username=demo&password=demo`, nil, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); w.Code != 415 {
		t.Fatalf("cross-site form accepted: %d", w.Code)
	}
}

func TestWritesDeniedWithoutOutboundEffects(t *testing.T) {
	s := testServer(t)
	cookie, _ := login(t, s)
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	body := `{"address":"` + target.URL + `","url":"` + target.URL + `","server":"` + target.URL + `"}`
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "CONNECT", "OPTIONS", "TRACE"} {
		for _, path := range []string{"/api/v1/hosts/probe", "/api/v1/hosts/add", "/api/v1/agent/enroll", "/api/v1/storage/discover", "/api/v1/media/download", "/api/v1/vms/demo-vm-01/power", "/api/v1/pools/pool-harbor/backup-restore", "/unknown"} {
			w := request(s, method, path, body, cookie, nil)
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("%s %s did not reject the mutation: %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"/api/v1/vms/demo-vm-01/console", "/api/local/v1/host", "/api/v1/agent/enroll", "/api/v1/media/download", "/api/v1/pools/pool-harbor/backup-download"} {
		if w := request(s, "GET", path, "", cookie, nil); w.Code != 404 {
			t.Fatalf("unsupported read %s got %d", path, w.Code)
		}
	}
	if w := request(s, "GET", "/api/v1/vms", "", cookie, map[string]string{"Upgrade": "websocket"}); w.Code != 403 {
		t.Fatalf("websocket allowed: %d", w.Code)
	}
	if requests.Load() != 0 {
		t.Fatalf("read-only server contacted infrastructure %d times", requests.Load())
	}
}

func TestCatalogFiltersPaginationAndScope(t *testing.T) {
	s := testServer(t)
	cookie, _ := login(t, s)
	type point struct {
		ID         string `json:"id"`
		Pool       string `json:"pool_id"`
		VM         string `json:"source_vm_id"`
		Repository string `json:"repository_id"`
	}
	type catalog struct {
		Items      []point `json:"items"`
		Cursor     string  `json:"next_cursor"`
		Generation string  `json:"generation"`
		Total      int     `json:"total"`
	}
	base := "/api/v1/pools/pool-harbor/backup-catalog?vm_id=demo-vm-01&repository_id=pool-harbor-backup-1&limit=2"
	wantIDs, atlasIDs := map[string]bool{}, map[string]bool{}
	for _, item := range s.fixtures.points["pool-harbor"] {
		if item["source_vm_id"] == "demo-vm-01" && item["repository_id"] == "pool-harbor-backup-1" {
			wantIDs[item["id"].(string)] = true
		}
		if strings.Contains(strings.ToLower(item["source_vm_name"].(string)), "atlas") {
			atlasIDs[item["id"].(string)] = true
		}
	}
	foreignVM := ""
	for _, vm := range s.fixtures.lists["vms"] {
		if vm["pool_id"] != "pool-harbor" {
			foreignVM = vm["id"].(string)
			break
		}
	}
	if foreignVM == "" {
		t.Fatal("fixture needs a VM in another pool to exercise scope isolation")
	}
	cursor := ""
	generation := ""
	ids := map[string]bool{}
	for {
		path := base
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor) + "&generation=" + url.QueryEscape(generation)
		}
		w := request(s, "GET", path, "", cookie, nil)
		if w.Code != 200 {
			t.Fatalf("catalog: %d %s", w.Code, w.Body.String())
		}
		var p catalog
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p.Total != len(wantIDs) {
			t.Fatalf("filtered total %d want %d", p.Total, len(wantIDs))
		}
		for _, item := range p.Items {
			if item.Pool != "pool-harbor" || item.VM != "demo-vm-01" || item.Repository != "pool-harbor-backup-1" || !wantIDs[item.ID] || ids[item.ID] {
				t.Fatalf("scope/duplicate point: %+v", item)
			}
			ids[item.ID] = true
		}
		if generation != "" && generation != p.Generation {
			t.Fatal("generation changed between pages")
		}
		generation = p.Generation
		if cursor == "" {
			if w := request(s, "GET", base+"&generation=stale", "", cookie, nil); w.Code != 409 {
				t.Fatalf("stale generation accepted: %d", w.Code)
			}
			if w := request(s, "GET", strings.Replace(base, "demo-vm-01", "demo-vm-02", 1)+"&cursor="+url.QueryEscape(p.Cursor), "", cookie, nil); w.Code != 400 {
				t.Fatalf("cross-filter cursor accepted: %d", w.Code)
			}
		}
		cursor = p.Cursor
		if cursor == "" {
			break
		}
	}
	if len(ids) != len(wantIDs) {
		t.Fatalf("pagination lost points: %v", ids)
	}
	for _, query := range []string{"vm_id=" + url.QueryEscape(foreignVM), "repository_id=pool-meadow-backup-1", "search=does-not-exist"} {
		w := request(s, "GET", "/api/v1/pools/pool-harbor/backup-catalog?"+query, "", cookie, nil)
		var p catalog
		json.Unmarshal(w.Body.Bytes(), &p)
		if w.Code != 200 || p.Total != 0 || len(p.Items) != 0 {
			t.Fatalf("foreign/no-match filter returned points: %d %s", w.Code, w.Body.String())
		}
	}
	w := request(s, "GET", "/api/v1/pools/pool-harbor/backup-catalog?search=ATLAS", "", cookie, nil)
	var p catalog
	json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != 200 || p.Total != len(atlasIDs) {
		t.Fatalf("case-insensitive name search: %d %s", w.Code, w.Body.String())
	}
	for _, item := range p.Items {
		if !atlasIDs[item.ID] {
			t.Fatalf("case-insensitive search returned unrelated point: %+v", item)
		}
	}
}

func TestStaticTraversalAndSourceDelivery(t *testing.T) {
	s := testServer(t)
	for _, path := range []string{"/../private.go", "/%2e%2e/private.go", "/chunks/%2f..%2fprivate.go", "/private.go", "/app.js.map", "/source/../private.go", "//index.html", "/api/v1/%73ession"} {
		w := request(s, "GET", path, "", nil, nil)
		if w.Code != 400 && w.Code != 404 {
			t.Fatalf("unsafe path %s got %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "must not be served") {
			t.Fatalf("disclosed file at %s", path)
		}
	}
	w := request(s, "GET", "/source", "", nil, nil)
	if w.Code != 200 || w.Body.String() != "exact matching test archive" || w.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("source not exact: %d %s", w.Code, w.Body.String())
	}
	w = request(s, "GET", "/", "", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Demo</title>") || w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("unsafe/broken index response: %d %v", w.Code, w.Header())
	}
	if w := request(s, "GET", "/api/v1/vms", "", nil, nil); w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthenticated/cacheable inventory: %d", w.Code)
	}
}

func TestAPIReferenceIsAnonymousButDoesNotExposeAPIOperations(t *testing.T) {
	s := testServer(t)
	w := request(s, "GET", "/api/docs/", "", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>API reference</title>") {
		t.Fatalf("anonymous reference missing: %d %s", w.Code, w.Body.String())
	}
	direct := request(s, "GET", "/api/docs/openapi.json", "", nil, nil)
	alias := request(s, "GET", "/api/openapi.json", "", nil, nil)
	if direct.Code != 200 || alias.Code != 200 || direct.Body.String() != `{"openapi":"3.1.0","paths":{}}` || alias.Body.String() != direct.Body.String() {
		t.Fatalf("OpenAPI delivery changed bytes: %d/%d %q", direct.Code, alias.Code, alias.Body.String())
	}
	for _, path := range []string{"/api/docs/private.go", "/api/docs/../private.go", "/api/docs/%2e%2e/private.go", "/api/docs/index.html/extra"} {
		w := request(s, "GET", path, "", nil, nil)
		if w.Code == 200 || strings.Contains(w.Body.String(), "must not be served") {
			t.Fatalf("documentation root escaped: %s %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request(s, "POST", "/api/docs/", "{}", nil, nil); w.Code != 403 {
		t.Fatalf("documentation exception permitted a write: %d", w.Code)
	}
	if w := request(s, "GET", "/api/v1/hosts", "", nil, nil); w.Code != 401 {
		t.Fatalf("documentation exception made inventory public: %d", w.Code)
	}
}

func TestStoppedVMStatsHaveNoInventedUsage(t *testing.T) {
	s := testServer(t)
	cookie, _ := login(t, s)
	w := request(s, "GET", "/api/v1/vms/demo-vm-05/stats?range=10m", "", cookie, nil)
	var history struct {
		Series []struct {
			Key    string     `json:"key"`
			Values []*float64 `json:"values"`
		} `json:"series"`
		Step  int       `json:"step_seconds"`
		Start time.Time `json:"start"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || history.Step != 5 || history.Start.IsZero() {
		t.Fatalf("bad history: %d %s", w.Code, w.Body.String())
	}
	for _, series := range history.Series {
		if series.Key == "memory.total" {
			continue
		}
		for _, value := range series.Values {
			if value != nil {
				t.Fatalf("stopped VM fabricated %s usage: %v", series.Key, *value)
			}
		}
	}
	if w := request(s, "GET", "/api/v1/vms/demo-vm-05/stats?range=unsupported", "", cookie, nil); w.Code != 400 {
		t.Fatalf("unsupported range accepted: %d", w.Code)
	}
}

func TestRefuseUIEscapingSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "private.js")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "app.js")); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{UIDir: dir, SourceArchive: target, PublicOrigin: "https://demo.example.com"})
	if err == nil {
		t.Fatal("accepted UI symlink escaping asset root")
	}
}

func TestCookieWorksThroughHTTPTransport(t *testing.T) {
	s := testServer(t)
	server := httptest.NewServer(s)
	defer server.Close()
	cookie, _ := login(t, s)
	req, err := http.NewRequest("GET", server.URL+"/api/v1/pools/pool-harbor/members", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(cookie)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		PoolID  string `json:"pool_id"`
		Master  string `json:"master_host_id"`
		Members []struct {
			ID   string `json:"host_id"`
			Role string `json:"role"`
		} `json:"members"`
	}
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	masters := 0
	for _, member := range report.Members {
		if member.Role == "master" {
			masters++
			if member.ID != report.Master {
				t.Fatal("member master contradicts pool authority")
			}
		}
	}
	if res.StatusCode != 200 || report.PoolID != "pool-harbor" || masters != 1 {
		t.Fatalf("invalid pool authority: %d %s", res.StatusCode, body)
	}
}
