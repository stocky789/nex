// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

const backupInventoryPageBytes = 2 << 20

// authorizePage answers and returns false unless this host is the pool master
// and the request's filters and cursor are well formed.
func authorizePage(w http.ResponseWriter, r *http.Request, s *Store) (q url.Values, cursor int, ok bool) {
	if s == nil {
		http.Error(w, "backup history unavailable", http.StatusServiceUnavailable)
		return nil, 0, false
	}
	// 409 is reserved for a changed generation between pages.
	if membership, err := Membership(s); err != nil || membership.MasterHostID != s.State.HostID {
		http.Error(w, "pool master required", http.StatusServiceUnavailable)
		return nil, 0, false
	}
	q = r.URL.Query()
	if len(q.Get("search")) > 200 || len(q.Get("vm_id")) > 128 || len(q.Get("repository_id")) > 128 || len(q.Get("recovery_point_id")) > 128 || len(q.Get("generation")) > 64 {
		http.Error(w, "invalid backup page filter", http.StatusBadRequest)
		return nil, 0, false
	}
	if q.Has("cursor") {
		var err error
		cursor, err = strconv.Atoi(q.Get("cursor"))
		if err != nil || cursor < 0 || q.Get("generation") == "" {
			http.Error(w, "invalid backup page cursor", http.StatusBadRequest)
			return nil, 0, false
		}
	}
	return q, cursor, true
}

// ownedDistinct reports whether every record belongs to the pool and none repeats.
func ownedDistinct[T any](records []T, poolID string, key func(T) (pool, id string)) bool {
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		pool, id := key(record)
		if pool != poolID || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func serveBackupRunPage(w http.ResponseWriter, r *http.Request, s *Store) {
	q, cursor, ok := authorizePage(w, r, s)
	if !ok {
		return
	}
	// ponytail: each page reads the retained records; add an on-disk index if
	// measured page latency exceeds the reverse channel's ten-second deadline.
	// The lock covers only the reads; a slow reader never holds it.
	poolBackupAuthorityMu.Lock()
	runs, err := listPoolBackupRuns(s)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		http.Error(w, "backup history unavailable", http.StatusServiceUnavailable)
		return
	}
	if !ownedDistinct(runs, s.State.PoolID, func(run protocol.BackupRun) (string, string) { return run.PoolID, run.OperationID }) {
		http.Error(w, "backup runs are invalid", http.StatusServiceUnavailable)
		return
	}
	// A page generation covers which records exist and their order, not their
	// status, so settlements and protection changes never restart a walk. Run
	// order is fixed by the immutable creation time; forgetting a repository removes
	// its runs, which changes the ID list and so the generation.
	ids := make([]string, len(runs))
	for i, run := range runs {
		ids[i] = run.OperationID
	}
	generation := pageGeneration(struct {
		Master string
		Runs   []string
	}{s.State.HostID, ids})
	keep := func(run protocol.BackupRun) bool {
		point := q.Get("recovery_point_id")
		return point == "" || run.RecoveryPointID == point
	}
	servePage(w, q, cursor, generation, runs, keep, "backup run exceeds page limit", func(items []protocol.BackupRun, total int, next string) any {
		page := protocol.BackupRunPage{Generation: generation, Items: items, Total: total, NextCursor: next}
		for _, run := range runs {
			if run.Status == "failed" || run.Status == "missed" || run.Status == "needs_attention" {
				page.AttentionCount++
			}
			if run.Kind == protocol.BackupKindBackupNow || run.Kind == protocol.BackupKindScheduled || run.Kind == protocol.BackupKindPolicyRunNow {
				if page.LastBackupRun == nil || run.UpdatedAt.After(page.LastBackupRun.UpdatedAt) {
					page.LastBackupRun = &protocol.BackupRunSummary{Status: run.Status, UpdatedAt: run.UpdatedAt}
				}
			}
		}
		return page
	})
}

func serveBackupCatalogPage(w http.ResponseWriter, r *http.Request, s *Store) {
	q, cursor, ok := authorizePage(w, r, s)
	if !ok {
		return
	}
	poolBackupAuthorityMu.Lock()
	points, err := loadPoolBackupCatalog(s)
	var repos []protocol.BackupRepository
	if err == nil {
		repos, err = ListPoolBackupRepositories(s)
	}
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		http.Error(w, "backup history unavailable", http.StatusServiceUnavailable)
		return
	}
	if !ownedDistinct(points, s.State.PoolID, func(point protocol.BackupRecoveryPoint) (string, string) { return point.PoolID, point.ID }) {
		http.Error(w, "backup catalog is invalid", http.StatusServiceUnavailable)
		return
	}
	repoNames := map[string]string{}
	for _, repo := range repos {
		repoNames[repo.ID] = strings.ToLower(repo.Name)
	}
	ids := make([]string, len(points))
	for i, point := range points {
		ids[i] = point.ID
	}
	generation := pageGeneration(struct {
		Master string
		Points []string
		Names  map[string]string
	}{s.State.HostID, ids, repoNames})
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))
	keep := func(point protocol.BackupRecoveryPoint) bool {
		if vm := q.Get("vm_id"); vm != "" && point.SourceVMID != vm {
			return false
		}
		if repo := q.Get("repository_id"); repo != "" && point.RepositoryID != repo {
			return false
		}
		return search == "" || strings.Contains(strings.ToLower(point.SourceVMName), search) || strings.Contains(strings.ToLower(point.SourceVMID), search) ||
			strings.Contains(strings.ToLower(point.ID), search) || strings.Contains(strings.ToLower(point.RepositoryID), search) ||
			strings.Contains(repoNames[point.RepositoryID], search)
	}
	servePage(w, q, cursor, generation, points, keep, "backup point exceeds page limit", func(items []protocol.BackupRecoveryPoint, total int, next string) any {
		page := protocol.BackupCatalogPage{Generation: generation, Items: items, Total: total, NextCursor: next, BackupDays14: []string{}}
		days := map[string]bool{}
		cutoff := time.Now().UTC().AddDate(0, 0, -13).Format("2006-01-02")
		for _, point := range points {
			if point.LastTestResult != protocol.BackupTestResultPassed {
				page.UntestedCount++
			}
			if !point.LastTestedAt.IsZero() && (page.LastRestoreTest == nil || point.LastTestedAt.After(page.LastRestoreTest.TestedAt)) {
				page.LastRestoreTest = &protocol.BackupTestSummary{Result: point.LastTestResult, TestedAt: point.LastTestedAt}
			}
			day := point.CapturedAt.UTC().Format("2006-01-02")
			if day >= cutoff {
				days[day] = true
			}
		}
		for day := range days {
			page.BackupDays14 = append(page.BackupDays14, day)
		}
		sort.Strings(page.BackupDays14)
		return page
	})
}

func pageGeneration(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func pageEnd[T any](items []T, start int) int {
	bytes := 0
	end := start
	for end < len(items) && end-start < 64 {
		b, _ := json.Marshal(items[end])
		if bytes+len(b)+1024 > backupInventoryPageBytes {
			break
		}
		bytes += len(b) + 1
		end++
	}
	return end
}

// servePage answers one cursor page of the items that pass keep. respond builds
// the endpoint's page around that slice.
func servePage[T any](w http.ResponseWriter, q url.Values, cursor int, generation string, all []T, keep func(T) bool, oversize string,
	respond func(items []T, total int, next string) any) {
	if g := q.Get("generation"); g != "" && g != generation {
		http.Error(w, "backup history changed; restart from first page", http.StatusConflict)
		return
	}
	filtered := make([]T, 0, len(all))
	for _, item := range all {
		if keep(item) {
			filtered = append(filtered, item)
		}
	}
	if cursor > len(filtered) {
		http.Error(w, "invalid backup page cursor", http.StatusBadRequest)
		return
	}
	end := pageEnd(filtered, cursor)
	if end == cursor && cursor < len(filtered) {
		http.Error(w, oversize, http.StatusRequestEntityTooLarge)
		return
	}
	next := ""
	if end < len(filtered) {
		next = strconv.Itoa(end)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(respond(filtered[cursor:end], len(filtered), next))
}
