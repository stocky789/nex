// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestOversizedBackupHistoryPagesAndMutation(t *testing.T) {
	s := seedBackupMaster(t)
	if err := os.MkdirAll(poolBackupRunsPath(s), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	all := make([]protocol.BackupRun, 270)
	for i := range all {
		all[i] = protocol.BackupRun{OperationID: fmt.Sprintf("run-%04d", i), Kind: protocol.BackupKindBackupNow,
			PoolID: s.State.PoolID, RepositoryID: "repo-1", Stage: "failed", Status: "failed",
			Error: strings.Repeat("x", 65500), CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now.Add(time.Duration(i) * time.Second)}
		b, err := json.Marshal(all[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(poolBackupRunsPath(s), all[i].OperationID+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := statsChannelHandler(http.NotFoundHandler(), s)
	var generation, cursor string
	seen := map[string]bool{}
	for {
		query := url.Values{}
		if cursor != "" {
			query.Set("cursor", cursor)
			query.Set("generation", generation)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/backup-runs?"+query.Encode(), nil))
		if w.Code != 200 || w.Body.Len() > backupInventoryPageBytes {
			t.Fatalf("page status=%d bytes=%d: %s", w.Code, w.Body.Len(), w.Body.String())
		}
		var page protocol.BackupRunPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != len(all) || page.AttentionCount != len(all) || len(page.Items) == 0 {
			t.Fatalf("incomplete page: %#v", page)
		}
		if generation == "" {
			generation = page.Generation
		} else if generation != page.Generation {
			t.Fatal("generation changed without source mutation")
		}
		for _, run := range page.Items {
			if seen[run.OperationID] {
				t.Fatalf("duplicate run %s", run.OperationID)
			}
			seen[run.OperationID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != len(all) {
		t.Fatalf("only %d/%d runs traversed", len(seen), len(all))
	}
	// A settlement anywhere in the pool must not restart a walk.
	all[0].Error, all[0].Status, all[0].UpdatedAt = "changed", "needs_attention", time.Now().UTC()
	b, _ := json.Marshal(all[0])
	if err := os.WriteFile(filepath.Join(poolBackupRunsPath(s), all[0].OperationID+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/backup-runs?cursor=1&generation="+generation, nil))
	if w.Code != 200 {
		t.Fatalf("settled run restarted the history walk: status=%d", w.Code)
	}
	added := all[0]
	added.OperationID = "run-added"
	b, _ = json.Marshal(added)
	if err := os.WriteFile(filepath.Join(poolBackupRunsPath(s), added.OperationID+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/backup-runs?cursor=1&generation="+generation, nil))
	if w.Code != 409 {
		t.Fatalf("added run page status=%d: %s", w.Code, w.Body.String())
	}
}

func TestBackupCatalogPageFiltersAndMutation(t *testing.T) {
	s := seedBackupMaster(t)
	if err := os.MkdirAll(poolBackupCatalogPath(s), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	points := make([]protocol.BackupRecoveryPoint, 70)
	for i := range points {
		points[i] = protocol.BackupRecoveryPoint{ID: fmt.Sprintf("point-%04d", i), RepositoryID: "repo-1", PoolID: s.State.PoolID,
			SourceVMID: "vm-1", SourceVMName: "Guest", CapturedAt: now.Add(time.Duration(i) * time.Second),
			Consistency: protocol.BackupCaptureStopped, ManifestSHA256: strings.Repeat("a", 64)}
		b, _ := json.Marshal(points[i])
		if err := os.WriteFile(filepath.Join(poolBackupCatalogPath(s), points[i].ID+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := statsChannelHandler(http.NotFoundHandler(), s)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/backup-catalog?vm_id=vm-1&search=guest", nil))
	if w.Code != 200 {
		t.Fatalf("catalog page: %d %s", w.Code, w.Body.String())
	}
	var page protocol.BackupCatalogPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Total != 70 || len(page.Items) != 64 || page.NextCursor == "" || page.UntestedCount != 70 {
		t.Fatalf("incomplete catalog page: %#v err=%v", page, err)
	}
	if page.Items[0].ID != "point-0069" {
		t.Fatalf("catalog order starts at %s", page.Items[0].ID)
	}
	points[0].Protected = true
	b, _ := json.Marshal(points[0])
	if err := os.WriteFile(filepath.Join(poolBackupCatalogPath(s), points[0].ID+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	next := "/backup-catalog?vm_id=vm-1&search=guest&cursor=" + page.NextCursor + "&generation=" + page.Generation
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", next, nil))
	if w.Code != 200 {
		t.Fatalf("protection change restarted the catalog walk: status=%d", w.Code)
	}
	if err := os.Remove(filepath.Join(poolBackupCatalogPath(s), points[1].ID+".json")); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", next, nil))
	if w.Code != 409 {
		t.Fatalf("removed point page status=%d: %s", w.Code, w.Body.String())
	}
}
