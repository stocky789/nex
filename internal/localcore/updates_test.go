// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"nex.local/nex/internal/nexupdate"
)

func TestLocalUpdatesAuthAndApply(t *testing.T) {
	payload := []byte("deb-bytes")
	sum := sha256.Sum256(payload)
	mux := http.NewServeMux()
	mux.HandleFunc("/apt/updates.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"packages": map[string]any{
				"nex-core-local": map[string]string{
					"version":  "9.9.9",
					"filename": "nex-core-local_9.9.9_amd64.deb",
					"sha256":   hex.EncodeToString(sum[:]),
				},
			},
		})
	})
	mux.HandleFunc("/apt/nex-core-local_9.9.9_amd64.deb", func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	})
	// NormalizeRepoURL rewrites plain-http loopback URLs to the public repo, so the
	// test server must be TLS and trusted through the package HTTP client.
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	prevHTTP := nexupdate.HTTPClient
	nexupdate.HTTPClient = srv.Client()
	t.Cleanup(func() { nexupdate.HTTPClient = prevHTTP })
	t.Setenv("NEX_UPDATE_REPO", srv.URL+"/apt")

	s, _ := testService(t)
	if w := callLocal(s, "GET", "/api/local/v1/updates", nil, "", "", ""); w.Code != 401 {
		t.Fatalf("unauth %d", w.Code)
	}
	cookie, csrf := loginLocal(t, s)
	w := callLocal(s, "GET", "/api/local/v1/updates", nil, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("check %d %s", w.Code, w.Body)
	}
	var st nexupdate.Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || !st.UpdateAvailable {
		t.Fatalf("%s", w.Body)
	}

	orig := nexupdate.Command
	nexupdate.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { nexupdate.Command = orig })

	w = callLocal(s, "POST", "/api/local/v1/updates/apply", map[string]any{}, cookie, csrf, "https://core.test")
	if w.Code != 202 {
		t.Fatalf("apply %d %s", w.Code, w.Body)
	}
	var result nexupdate.ApplyResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Status != "scheduled" {
		t.Fatalf("%s", w.Body)
	}
	// Background apply should leave a progress file eventually.
	for i := 0; i < 50; i++ {
		time.Sleep(20 * time.Millisecond)
		w = callLocal(s, "GET", "/api/local/v1/updates", nil, cookie, csrf, "https://core.test")
		_ = json.Unmarshal(w.Body.Bytes(), &st)
		if st.Phase != "" {
			break
		}
	}
	if st.Phase == "" {
		t.Fatalf("expected progress phase, got %+v", st)
	}
}
