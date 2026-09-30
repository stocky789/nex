// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func seedLargeBackupRuns(t *testing.T, s *Store, count int) {
	t.Helper()
	if err := os.MkdirAll(poolBackupRunsPath(s), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		run := protocol.BackupRun{OperationID: fmt.Sprintf("run-%05d", i), Kind: protocol.BackupKindBackupNow,
			PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed",
			Error: strings.Repeat("x", 4<<10), CreatedAt: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)}
		body, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(poolBackupRunsPath(s), run.OperationID+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// pagePosts records every backup-page request a member receives.
type pagePosts struct {
	mu     sync.Mutex
	opens  [][]byte
	pages  int
	excess int // request bytes beyond the page data itself
}

func recordPagePosts(lab *transferLab) *pagePosts {
	posts := &pagePosts{}
	lab.intercept.Store(func(_ string, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != poolBackupPagePath {
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var upload struct {
			Replica json.RawMessage `json:"replica"`
			Data    []byte          `json:"data"`
		}
		_ = json.Unmarshal(body, &upload)
		posts.mu.Lock()
		defer posts.mu.Unlock()
		if len(upload.Replica) != 0 {
			posts.opens = append(posts.opens, body)
			return
		}
		posts.pages++
		posts.excess += len(body) - base64.StdEncoding.EncodedLen(len(upload.Data))
	})
	return posts
}

func postPage(t *testing.T, master *Store, lab *transferLab, body any) int {
	t.Helper()
	client, err := PoolMemberPeerClient(master, lab.origin["member-1"], "member-1")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(body)
	response, err := client.Post(lab.origin["member-1"]+poolBackupPagePath, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

// The signed replica crosses the wire once per push; every page request after
// it carries only the manifest root, so a large authority costs its page data.
func TestBackupAuthorityPushSendsSignedReplicaOncePerSession(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	seedLargeBackupRuns(t, master, 300)
	forceV2BackupAuthority(t)
	posts := recordPagePosts(lab)
	pushReplicas(t, master)
	replica, _, err := buildPoolAuthorityReplica(master)
	if err != nil || replica.BackupManifest == nil || replica.BackupManifest.PageCount < 4 {
		t.Fatalf("manifest = %#v %v", replica.BackupManifest, err)
	}
	if len(posts.opens) != 1 || posts.pages < replica.BackupManifest.PageCount {
		t.Fatalf("session opens = %d, page requests = %d, pages = %d", len(posts.opens), posts.pages, replica.BackupManifest.PageCount)
	}
	if per := posts.excess / posts.pages; per > 400 {
		t.Fatalf("a page request carried %d bytes besides its data", per)
	}
}

func TestBackupAuthorityPushSessionRefusesForgedStaleAndExpired(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	seedLargeBackupRuns(t, master, 300)
	posts := recordPagePosts(lab)
	pushReplicas(t, master)
	replica, _, err := buildPoolAuthorityReplica(master)
	manifest := replica.BackupManifest
	if err != nil || manifest == nil || len(posts.opens) != 1 {
		t.Fatalf("manifest = %#v opens = %d %v", manifest, len(posts.opens), err)
	}
	page, err := readPoolBackupPage(master, manifest, 0)
	if err != nil {
		t.Fatal(err)
	}
	send := func(sha string, data []byte) int {
		return postPage(t, master, lab, poolBackupPageUpload{ManifestSHA256: sha, Index: 0, Data: data})
	}
	if code := send(manifest.SHA256, page); code != http.StatusNoContent {
		t.Fatalf("page of the open session = %d", code)
	}
	if code := postPage(t, master, lab, poolBackupPageUpload{ManifestSHA256: manifest.SHA256, Index: 0, ReuseSHA256: "not-a-digest"}); code != http.StatusBadRequest {
		t.Fatalf("page reuse of an invalid digest = %d", code)
	}
	if code := send(strings.Repeat("a", 64), page); code != http.StatusConflict {
		t.Fatalf("page of a manifest that was never verified = %d", code)
	}
	var open map[string]any
	if err := json.Unmarshal(posts.opens[0], &open); err != nil {
		t.Fatal(err)
	}
	open["replica"].(map[string]any)["generation"] = replica.Generation + 100
	if code := postPage(t, master, lab, open); code != http.StatusConflict {
		t.Fatalf("session opened with a forged replica = %d", code)
	}
	poolBackupPushSessionMu.Lock()
	member := lab.host("member-1")
	session := poolBackupPushSessions[member.dir]
	session.expires = time.Now().Add(-time.Second)
	poolBackupPushSessions[member.dir] = session
	poolBackupPushSessionMu.Unlock()
	if code := send(manifest.SHA256, page); code != http.StatusConflict {
		t.Fatalf("page after the session expired = %d", code)
	}
}

func TestPoolBackupAuthorityCapacityWarnsAtEightyPercentOfHardLimits(t *testing.T) {
	s := seedBackupMaster(t)
	seedLargeBackupRuns(t, s, 3)
	if err := refreshPoolBackupAuthorityUsage(s); err != nil {
		t.Fatal(err)
	}
	set := func(edit func(*protocol.PoolBackupManifest)) []protocol.Check {
		poolBackupAuthorityUsageMu.Lock()
		usage := poolBackupAuthorityUsages[s.dir]
		usage.manifest = protocol.PoolBackupManifest{}
		edit(&usage.manifest)
		poolBackupAuthorityUsages[s.dir] = usage
		poolBackupAuthorityUsageMu.Unlock()
		return PoolBackupAuthorityCapacityCheck(s)
	}
	if checks := PoolBackupAuthorityCapacityCheck(s); len(checks) != 1 || checks[0].Status != "healthy" {
		t.Fatalf("small authority = %+v", checks)
	}
	// Record counts and bytes only choose the replication form; they are not limits.
	if checks := set(func(m *protocol.PoolBackupManifest) {
		m.History, m.Catalog, m.Pins, m.MetadataReceipts, m.TotalBytes = 100000, 100000, 100000, 100000, protocol.PoolBackupBundleLimit
	}); checks[0].Status != "healthy" {
		t.Fatalf("paged-form thresholds warned: %+v", checks)
	}
	if checks := set(func(m *protocol.PoolBackupManifest) { m.Definitions = 799 }); checks[0].Status != "healthy" {
		t.Fatalf("79.9%% of policies = %+v", checks)
	}
	if checks := set(func(m *protocol.PoolBackupManifest) { m.Definitions = 800 }); checks[0].Status != "degraded" || !strings.Contains(checks[0].Error, "policies 800 of 1000") {
		t.Fatalf("80%% of policies = %+v", checks)
	}
	if checks := set(func(m *protocol.PoolBackupManifest) { m.Repositories = 205 }); checks[0].Status != "degraded" || !strings.Contains(checks[0].Error, "repositories 205 of 256") {
		t.Fatalf("80%% of repositories = %+v", checks)
	}
}

// Regression for BUG-0751: only the pool master measures and reports capacity.
func TestPoolBackupAuthorityCapacityIsMeasuredOnlyOnTheMaster(t *testing.T) {
	lab := promotionLab(t, 1)
	member := lab.host("member-1")
	if err := refreshPoolBackupAuthorityUsage(member); err != nil {
		t.Fatal(err)
	}
	if checks := PoolBackupAuthorityCapacityCheck(member); len(checks) != 0 {
		t.Fatalf("member reported capacity: %+v", checks)
	}
	master := lab.host("master-1")
	if err := refreshPoolBackupAuthorityUsage(master); err != nil {
		t.Fatal(err)
	}
	if checks := PoolBackupAuthorityCapacityCheck(master); len(checks) != 1 {
		t.Fatalf("master did not report capacity: %+v", checks)
	}
}

// Regression for BUG-0751: each accepted page extends the session, so a slow push finishes.
func TestBackupAuthorityPushSessionSlidesOnAcceptedPages(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	seedLargeBackupRuns(t, master, 300)
	pushReplicas(t, master)
	replica, _, err := buildPoolAuthorityReplica(master)
	manifest := replica.BackupManifest
	if err != nil || manifest == nil {
		t.Fatalf("manifest = %#v %v", manifest, err)
	}
	page, err := readPoolBackupPage(master, manifest, 0)
	if err != nil {
		t.Fatal(err)
	}
	member := lab.host("member-1")
	soon := time.Now().Add(time.Minute)
	poolBackupPushSessionMu.Lock()
	session := poolBackupPushSessions[member.dir]
	session.expires = soon
	poolBackupPushSessions[member.dir] = session
	poolBackupPushSessionMu.Unlock()
	if code := postPage(t, master, lab, poolBackupPageUpload{ManifestSHA256: manifest.SHA256, Index: 0, Data: page}); code != http.StatusNoContent {
		t.Fatalf("page = %d", code)
	}
	poolBackupPushSessionMu.Lock()
	extended := poolBackupPushSessions[member.dir].expires
	poolBackupPushSessionMu.Unlock()
	if !extended.After(soon.Add(poolBackupPushSessionTTL / 2)) {
		t.Fatalf("session expiry %v was not extended past %v", extended, soon)
	}
}
