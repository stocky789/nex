// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.1.0", "0.1.1", -1},
		{"0.1.1", "0.1.0", 1},
		{"0.1.0-dev", "0.1.0", -1},
		{"0.1.0", "0.1.0-dev", 1},
		{"0.1.0+apt1", "0.1.0", 1},
		{"0.1.0", "0.1.0+apt1", -1},
		{"0.1.0+apt1", "0.1.0+apt2", -1},
		{"1:0.1.0", "0.2.0", 1},
		{"0.9.0", "0.10.0", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Fatalf("CompareVersions(%q, %q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	if got := NormalizeRepoURL("http://10.0.0.1:8080/apt/"); got != defaultRepoURL {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeRepoURL("http://127.0.0.1:8081"); got != defaultRepoURL {
		t.Fatalf("got %q", got)
	}
	if got := NormalizeRepoURL("https://mirror.example/apt/"); got != "https://mirror.example/apt" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("NEX_UPDATE_REPO", "http://192.168.1.10/apt")
	if got := DefaultRepoURL(); got != defaultRepoURL {
		t.Fatalf("DefaultRepoURL=%q", got)
	}
}

func TestCheckAndDownload(t *testing.T) {
	payload := []byte("fake-deb-content")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	mux.HandleFunc("/apt/updates.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(updatesIndex{Packages: map[string]packageInfo{
			"nex-core-local": {Version: "0.2.0", Filename: "nex-core-local_0.2.0_amd64.deb", SHA256: digest},
		}})
	})
	mux.HandleFunc("/apt/nex-core-local_0.2.0_amd64.deb", func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	prev := HTTPClient
	HTTPClient = srv.Client()
	t.Cleanup(func() { HTTPClient = prev })

	st, err := Check(context.Background(), srv.URL+"/apt/", "nex-core-local")
	if err != nil {
		t.Fatal(err)
	}
	if st.Error != "" || !st.UpdateAvailable || st.AvailableVersion != "0.2.0" {
		t.Fatalf("%+v", st)
	}
	if st.RepoURL != srv.URL+"/apt" {
		t.Fatalf("repo %q", st.RepoURL)
	}

	dir := t.TempDir()
	path, ver, err := DownloadDeb(context.Background(), srv.URL+"/apt", "nex-core-local", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ver != "0.2.0" {
		t.Fatal(ver)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("deb content: %v", err)
	}

	bad := t.TempDir()
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/apt/updates.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(updatesIndex{Packages: map[string]packageInfo{
			"nex-core-local": {Version: "0.2.0", Filename: "bad.deb", SHA256: digest},
		}})
	})
	mux2.HandleFunc("/apt/bad.deb", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered"))
	})
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	HTTPClient = srv2.Client()
	if _, _, err := DownloadDeb(context.Background(), srv2.URL+"/apt", "nex-core-local", bad, nil); err == nil {
		t.Fatal("expected sha256 failure")
	}
}

func TestRequestApply(t *testing.T) {
	dir := t.TempDir()
	if err := RequestApply(dir, "http://example.test/apt/", "nex-controller"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "pending-update.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p pendingUpdate
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.Package != "nex-controller" || p.RepoURL != "http://example.test/apt" || p.RequestedAt == "" {
		t.Fatalf("%+v", p)
	}
	prog, ok := ReadProgress(dir)
	if !ok || prog.Phase != "downloading" {
		t.Fatalf("%+v %v", prog, ok)
	}
}

func TestDownloadProgress(t *testing.T) {
	payload := make([]byte, 200_000)
	for i := range payload {
		payload[i] = byte(i)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	mux.HandleFunc("/apt/updates.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(updatesIndex{Packages: map[string]packageInfo{
			"nex-controller": {Version: "1.0.0", Filename: "p.deb", SHA256: digest, Size: int64(len(payload))},
		}})
	})
	mux.HandleFunc("/apt/p.deb", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	prev := HTTPClient
	HTTPClient = srv.Client()
	t.Cleanup(func() { HTTPClient = prev })
	seen := 0
	_, _, err := DownloadDeb(context.Background(), srv.URL+"/apt", "nex-controller", t.TempDir(), func(pct int) {
		if pct > seen {
			seen = pct
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 50 {
		t.Fatalf("expected progress callbacks, max=%d", seen)
	}
}

func TestCheckUnreachable(t *testing.T) {
	st, err := Check(context.Background(), "http://127.0.0.1:1/apt", "nex-controller")
	if err != nil {
		t.Fatal(err)
	}
	if st.Error == "" || st.UpdateAvailable {
		t.Fatalf("%+v", st)
	}
}
