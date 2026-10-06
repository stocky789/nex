// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/hostimage"
	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/vmhost"
)

// idlePlatform is a host that is up and has nothing staged.
type idlePlatform struct{}

func (idlePlatform) Booted(context.Context) (nexupdate.Booted, error) {
	return nexupdate.Booted{RootHash: strings.Repeat("0", 64), Label: "dev", Sequence: 4}, nil
}
func (idlePlatform) Verify(string) (*hostimage.Release, error) { return nil, nil }
func (idlePlatform) Stage(context.Context, *hostimage.Release, nexupdate.Booted) (string, error) {
	return "", nil
}
func (idlePlatform) Arm(context.Context, *hostimage.Release) error         { return nil }
func (idlePlatform) Disarm(context.Context, nexupdate.ReleaseRecord) error { return nil }
func (idlePlatform) Rollback(context.Context, nexupdate.ReleaseRecord, nexupdate.ReleaseRecord) error {
	return nil
}
func (idlePlatform) Entries(context.Context) ([]hostimage.UKIEntry, error) { return nil, nil }

// idleUpdateEngine is an update engine over a host that is up and has nothing staged, with no update
// source configured, rooted in dir.
func idleUpdateEngine(dir string) *nexupdate.Engine {
	return nexupdate.NewEngine(nexupdate.Config{
		Dir: filepath.Join(dir, "image"), TrustDir: filepath.Join(dir, "update"), Root: dir,
		Platform: idlePlatform{},
		Settings: func() (string, string) { return "", "stable" }, // no base URL configured
	})
}

func imageService(t *testing.T) (*Service, string, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "state"), AgentStateDir: filepath.Join(dir, "agent"), Authenticate: testRootAuth,
		Engine: vmhost.Config{StateDir: filepath.Join(dir, "vmstate"), StorageDir: filepath.Join(dir, "storage"), Bridge: "br0"}, ImageUpdate: idleUpdateEngine(dir)}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cookie, csrf := loginLocal(t, s)
	return s, cookie, csrf
}

func post(s *Service, path string, body any, cookie, csrf string) (int, map[string]any) {
	w := callLocal(s, "POST", path, body, cookie, csrf, "https://core.test")
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestImageUpdateRoutes(t *testing.T) {
	s, cookie, csrf := imageService(t)
	for _, p := range []string{"check", "apply", "activate", "rollback", "cancel"} {
		if w := callLocal(s, "POST", "/api/local/v1/updates/"+p, map[string]any{}, "", "", "https://core.test"); w.Code != 401 {
			t.Fatalf("%s without a session: %d", p, w.Code)
		}
	}
	w := callLocal(s, "GET", "/api/local/v1/updates", nil, cookie, csrf, "https://core.test")
	var st nexupdate.ImageStatus
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil || st.Phase != "idle" || st.Running || st.Channel != "stable" ||
		len(st.Slots) != 1 || st.Slots[0].Role != "booted" || st.Slots[0].Sequence != 4 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}

	code, body := post(s, "/api/local/v1/updates/check", map[string]any{}, cookie, csrf)
	if code != 503 || body["code"] != "source_unconfigured" || body["error"] == "" {
		t.Fatalf("%d %v", code, body)
	}
	for _, c := range []struct {
		path string
		body map[string]any
		code int
		why  string
	}{
		{"activate", map[string]any{"request_id": "req-activate-01"}, 409, "nothing_staged"},
		{"rollback", map[string]any{"request_id": "req-rollback-01"}, 409, "nothing_to_roll_back"},
		{"cancel", map[string]any{"request_id": "req-cancel-0001"}, 409, "nothing_to_cancel"},
		{"apply", map[string]any{"request_id": "x"}, 400, "invalid_request"},
		{"apply", map[string]any{"request_id": "req-apply-0001", "scope": "controller"}, 409, "scope_unavailable"},
		{"apply", map[string]any{"request_id": "req-apply-0001", "activate": "sometimes"}, 400, "invalid_request"},
	} {
		code, body = post(s, "/api/local/v1/updates/"+c.path, c.body, cookie, csrf)
		if code != c.code || body["code"] != c.why {
			t.Errorf("%s %v: %d %v, want %d %s", c.path, c.body, code, body, c.code, c.why)
		}
	}
	if code, _ = post(s, "/api/local/v1/updates/apply", map[string]any{"request_id": "req-apply-0001", "surprise": 1}, cookie, csrf); code != 400 {
		t.Errorf("unknown fields: %d", code)
	}

	// A valid apply is accepted, runs, and fails with the stable code in its operation; the host keeps running.
	code, body = post(s, "/api/local/v1/updates/apply", map[string]any{"request_id": "req-apply-0002"}, cookie, csrf)
	if code != 202 || body["id"] != "req-apply-0002" || body["kind"] != "apply" {
		t.Fatalf("%d %v", code, body)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		w = callLocal(s, "GET", "/api/local/v1/updates", nil, cookie, csrf, "https://core.test")
		st = nexupdate.ImageStatus{}
		json.Unmarshal(w.Body.Bytes(), &st)
		if st.Operation != nil && st.Operation.FinishedAt != nil && !st.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("apply never finished: %s", w.Body)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st.Operation.Error == nil || st.Operation.Error.Code != "source_unconfigured" || st.Phase != "idle" || st.Slots[0].Sequence != 4 {
		t.Fatalf("%+v", st)
	}
	// The exact replay is 200 with the original operation; a changed body is 409.
	code, body = post(s, "/api/local/v1/updates/apply", map[string]any{"request_id": "req-apply-0002"}, cookie, csrf)
	if code != 200 || body["id"] != "req-apply-0002" {
		t.Fatalf("%d %v", code, body)
	}
	code, body = post(s, "/api/local/v1/updates/apply", map[string]any{"request_id": "req-apply-0002", "activate": "reboot"}, cookie, csrf)
	if code != 409 || body["code"] != "request_conflict" {
		t.Fatalf("%d %v", code, body)
	}
}

