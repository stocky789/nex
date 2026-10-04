// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/vmhost"
)

// offlineService is an image-edition host with a signed tree as its update server and an inbox, whose
// update.conf lives in the test directory.
func offlineService(t *testing.T) (s *Service, tree signedTree, inbox, conf, cookie, csrf string) {
	t.Helper()
	tree = signedRelease(t, "2026.10.2", 2)
	dir := t.TempDir()
	inbox, conf = filepath.Join(dir, "inbox"), filepath.Join(dir, "etc", "updates.conf")
	oldConf := nexupdate.UpdatesConfig
	nexupdate.UpdatesConfig = conf
	t.Cleanup(func() { nexupdate.UpdatesConfig = oldConf })
	t.Setenv("NEX_UPDATE_BASE_URL", "")
	t.Setenv("NEX_UPDATE_CHANNEL", "")
	eng := nexupdate.NewEngine(nexupdate.Config{
		Dir: filepath.Join(dir, "image"), TrustDir: filepath.Join(dir, "update"), PinnedTrust: tree.pinned, Root: t.TempDir(),
		Platform: &flowPlatform{}, InboxDir: inbox, PayloadDir: filepath.Join(dir, "payload"),
		Source: func(string) nexupdate.Source { return nexupdate.DirSource{Root: tree.dir} },
	})
	cfg := Config{DataDir: filepath.Join(dir, "state"), AgentStateDir: filepath.Join(dir, "agent"), Authenticate: testRootAuth,
		Engine: vmhost.Config{StateDir: filepath.Join(dir, "vmstate"), StorageDir: filepath.Join(dir, "storage"), Bridge: "br0"}, ImageUpdate: eng}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cookie, csrf = loginLocal(t, s)
	return
}

func putJSON(s *Service, path string, body any, cookie, csrf string) (int, map[string]any) {
	w := callLocal(s, "PUT", path, body, cookie, csrf, "https://core.test")
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func opDone(t *testing.T, s *Service, id, cookie, csrf string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		w := callLocal(s, "GET", "/api/local/v1/updates/operations/"+id, nil, cookie, csrf, "https://core.test")
		var op map[string]any
		json.Unmarshal(w.Body.Bytes(), &op)
		if w.Code == 200 && op["finished_at"] != nil {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s never finished: %d %s", id, w.Code, w.Body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSettingsMediaImportAndFetchRoutes(t *testing.T) {
	s, tree, inbox, conf, cookie, csrf := offlineService(t)

	// Every route needs the root session; the mutating ones need the CSRF token too.
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/local/v1/updates/settings"}, {"GET", "/api/local/v1/updates/media"},
		{"POST", "/api/local/v1/updates/import"}, {"POST", "/api/local/v1/updates/components/base/fetch"},
	} {
		if w := callLocal(s, c.method, c.path, map[string]any{}, "", "", "https://core.test"); w.Code != 401 {
			t.Errorf("%s %s without a session: %d", c.method, c.path, w.Code)
		}
		if c.method != "GET" {
			if w := callLocal(s, c.method, c.path, map[string]any{}, cookie, "", "https://core.test"); w.Code != 403 {
				t.Errorf("%s %s without the CSRF token: %d", c.method, c.path, w.Code)
			}
		}
	}

	// Settings: validated, persisted on the host, reported in the status.
	code, body := putJSON(s, "/api/local/v1/updates/settings", map[string]any{"channel": "stable", "base_url": "https://user:pw@updates.example.test"}, cookie, csrf)
	if code != 400 || body["code"] != "invalid_base_url" {
		t.Fatalf("%d %v", code, body)
	}
	if code, _ = putJSON(s, "/api/local/v1/updates/settings", map[string]any{"channel": "stable", "surprise": 1}, cookie, csrf); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	code, body = putJSON(s, "/api/local/v1/updates/settings", map[string]any{"channel": "dev", "base_url": "http://mirror.example.test:8080/"}, cookie, csrf)
	if code != 200 || body["channel"] != "dev" || body["base_url"] != "http://mirror.example.test:8080" {
		t.Fatalf("%d %v", code, body)
	}
	if b, _ := os.ReadFile(conf); string(b) != "channel=dev\nbase_url=http://mirror.example.test:8080\n" {
		t.Fatalf("updates.conf = %q", b)
	}
	w := callLocal(s, "GET", "/api/local/v1/updates", nil, cookie, csrf, "https://core.test")
	var st nexupdate.ImageStatus
	if json.Unmarshal(w.Body.Bytes(), &st) != nil || st.Channel != "dev" || st.BaseURL != "http://mirror.example.test:8080" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}

	// Media: nothing yet, then a bundle in the inbox.
	w = callLocal(s, "GET", "/api/local/v1/updates/media", nil, cookie, csrf, "https://core.test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"media":[]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if err := os.CopyFS(filepath.Join(inbox, "spring"), os.DirFS(tree.dir)); err != nil {
		t.Fatal(err)
	}
	w = callLocal(s, "GET", "/api/local/v1/updates/media", nil, cookie, csrf, "https://core.test")
	var listed struct{ Media []nexupdate.Media }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Media) != 1 ||
		listed.Media[0].ID != "inbox/spring" || listed.Media[0].Channels[0].Name != "stable" || listed.Media[0].Channels[0].Sequence != 2 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}

	// Import stages through the apply pipeline; the settings above point nowhere useful, so the bundle is the source.
	code, body = post(s, "/api/local/v1/updates/import", map[string]any{"request_id": "req-import-0001", "media": "inbox/spring", "channel": "stable"}, cookie, csrf)
	if code != 202 || body["kind"] != "import" {
		t.Fatalf("%d %v", code, body)
	}
	if op := opDone(t, s, "req-import-0001", cookie, csrf); op["error"] != nil || op["phase"] != "staged" {
		t.Fatalf("%v", op)
	}
	if code, body = post(s, "/api/local/v1/updates/import", map[string]any{"request_id": "req-import-0001", "media": "inbox/spring", "channel": "stable"}, cookie, csrf); code != 200 || body["id"] != "req-import-0001" {
		t.Fatalf("replay: %d %v", code, body)
	}
	if code, body = post(s, "/api/local/v1/updates/import", map[string]any{"request_id": "req-import-0001", "media": "inbox"}, cookie, csrf); code != 409 || body["code"] != "request_conflict" {
		t.Fatalf("changed body: %d %v", code, body)
	}
	if code, body = post(s, "/api/local/v1/updates/import", map[string]any{"media": ""}, cookie, csrf); code != 400 {
		t.Fatalf("no media: %d %v", code, body)
	}

	// Fetch accepts only on_demand payloads: the slot image is not one.
	code, body = post(s, "/api/local/v1/updates/components/base/fetch", map[string]any{"request_id": "req-fetch-0001", "media": "inbox/spring", "channel": "stable"}, cookie, csrf)
	if code != 202 || body["kind"] != "fetch" {
		t.Fatalf("%d %v", code, body)
	}
	op := opDone(t, s, "req-fetch-0001", cookie, csrf)
	if e, _ := op["error"].(map[string]any); e == nil || e["code"] != "not_on_demand" {
		t.Fatalf("%v", op)
	}
}

func TestClassicHostRefusesOfflineRoutes(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	// settings is the one route a package-edition host answers; the others need the image engine.
	if w := callLocal(s, "GET", "/api/local/v1/updates/media", nil, cookie, csrf, "https://core.test"); w.Code != 409 {
		t.Errorf("media: %d", w.Code)
	}
	for _, p := range []string{"import", "components/base/fetch"} {
		if code, body := post(s, "/api/local/v1/updates/"+p, map[string]any{"request_id": "req-classic-001", "media": "inbox"}, cookie, csrf); code != 409 || body["code"] != "unsupported_edition" {
			t.Errorf("%s: %d %v", p, code, body)
		}
	}
}

// A package-edition host has no update server: it takes a channel, kept as the archive URL it reads.
func TestClassicHostChoosesItsChannelThroughItsArchive(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	conf := filepath.Join(t.TempDir(), "updates.conf")
	previous := nexupdate.UpdatesConfig
	nexupdate.UpdatesConfig = conf
	t.Cleanup(func() { nexupdate.UpdatesConfig = previous })
	t.Setenv("NEX_UPDATE_REPO", "")
	if err := os.WriteFile(conf, []byte("repo_url=https://mirror.example.test/nex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, body := putJSON(s, "/api/local/v1/updates/settings", map[string]any{"channel": "dev"}, cookie, csrf)
	if code != 200 || body["channel"] != "dev" || body["base_url"] != "" {
		t.Fatalf("%d %v", code, body)
	}
	if b, _ := os.ReadFile(conf); string(b) != "repo_url=https://mirror.example.test/nex/dev\n" {
		t.Fatalf("the channel is the archive subtree: %q", b)
	}
	for _, c := range []struct {
		body map[string]any
		code string
	}{
		{map[string]any{"channel": "testing"}, "invalid_channel"},
		{map[string]any{"channel": "dev", "base_url": "https://updates.example.test"}, "invalid_base_url"},
	} {
		if code, body := putJSON(s, "/api/local/v1/updates/settings", c.body, cookie, csrf); code != 400 || body["code"] != c.code {
			t.Errorf("%v: %d %v", c.body, code, body)
		}
	}
	if b, _ := os.ReadFile(conf); string(b) != "repo_url=https://mirror.example.test/nex/dev\n" {
		t.Fatalf("a refused request changes nothing: %q", b)
	}
	if code, body = putJSON(s, "/api/local/v1/updates/settings", map[string]any{"channel": "stable"}, cookie, csrf); code != 200 || body["channel"] != "stable" {
		t.Fatalf("%d %v", code, body)
	}
	if b, _ := os.ReadFile(conf); string(b) != "repo_url=https://mirror.example.test/nex\n" {
		t.Fatalf("stable returns to the archive root: %q", b)
	}
}
