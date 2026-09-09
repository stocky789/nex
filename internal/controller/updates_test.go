// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"nex.local/nex/internal/nexupdate"
)

func TestUpdatesAuthAndApply(t *testing.T) {
	payload := []byte("deb")
	sum := sha256.Sum256(payload)
	mux := http.NewServeMux()
	mux.HandleFunc("/apt/updates.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"packages": map[string]any{
				"nex-controller": map[string]string{
					"version":  "9.9.9",
					"filename": "nex-controller_9.9.9_amd64.deb",
					"sha256":   hex.EncodeToString(sum[:]),
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("NEX_UPDATE_REPO", srv.URL+"/apt")

	helper := filepath.Join(t.TempDir(), "apply-update")
	pathUnit := filepath.Join(t.TempDir(), "nex-controller-update.path")
	if err := os.WriteFile(helper, []byte("#!/bin/true\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathUnit, []byte("[Path]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	prevBin, prevUnits := nexupdate.ApplyUpdateBinary, nexupdate.UpdatePathUnits
	nexupdate.ApplyUpdateBinary = helper
	nexupdate.UpdatePathUnits = []string{pathUnit}
	t.Cleanup(func() {
		nexupdate.ApplyUpdateBinary = prevBin
		nexupdate.UpdatePathUnits = prevUnits
	})

	a := newClient(t)
	a.cookie, a.csrf = nil, ""
	a.call("GET", "/api/v1/updates", nil, nil, 401)

	a = newClient(t)
	var st nexupdate.Status
	read(t, a.call("GET", "/api/v1/updates", nil, nil, 200), &st)
	if !st.UpdateAvailable || st.AvailableVersion != "9.9.9" || st.Package != "nex-controller" {
		t.Fatalf("%+v", st)
	}

	var result nexupdate.ApplyResult
	read(t, a.call("POST", "/api/v1/updates/apply", map[string]any{}, nil, 202), &result)
	if result.Status != "scheduled" {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(filepath.Join(a.c.cfg.DataDir, "pending-update.json")); err != nil {
		t.Fatal(err)
	}
	var audit []map[string]any
	read(t, a.call("GET", "/api/v1/audit", nil, nil, 200), &audit)
	found := false
	for _, row := range audit {
		if row["action"] == "update.apply" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing audit")
	}
}
