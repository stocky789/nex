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

func backupInventoryPage(w http.ResponseWriter, r *http.Request, s *Store, catalog bool) {
	if s == nil {
		http.Error(w, "backup history unavailable", http.StatusServiceUnavailable)
		return
	}
	// 409 is reserved for a changed generation between pages.
	if membership, err := Membership(s); err != nil || membership.MasterHostID != s.State.HostID {
		http.Error(w, "pool master required", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	cursor := 0
	if len(q.Get("search")) > 200 || len(q.Get("vm_id")) > 128 || len(q.Get("repository_id")) > 128 || len(q.Get("recovery_point_id")) > 128 || len(q.Get("generation")) > 64 {
		http.Error(w, "invalid backup page filter", http.StatusBadRequest)
		return
	}
	if q.Has("cursor") {
		var err error
		cursor, err = strconv.Atoi(q.Get("cursor"))
		if err != nil || cursor < 0 || q.Get("generation") == "" {
			http.Error(w, "invalid backup page cursor", http.StatusBadRequest)
			return
		}
	}
	// ponytail: each page reads the retained records; add an on-disk index if
	// measured page latency exceeds the reverse channel's ten-second deadline.
	// The lock covers only the reads; a slow reader never holds it.
	poolBackupAuthorityMu.Lock()
	var points []protocol.BackupRecoveryPoint
	var repos []protocol.BackupRepository
	var runs []protocol.BackupRun
	var loadErr error
	if catalog {
		points, loadErr = loadPoolBackupCatalog(s)
		if loadErr == nil {
			repos, loadErr = ListPoolBackupRepositories(s)
		}
	} else {
		runs, loadErr = listPoolBackupRuns(s)
	}
	poolID, hostID := s.State.PoolID, s.State.HostID
	poolBackupAuthorityMu.Unlock()
	if loadErr != nil {
		http.Error(w, "backup history unavailable", http.StatusServiceUnavailable)
		return
	}
	if catalog {
		seen := make(map[string]bool, len(points))
		for _, point := range points {
			if point.PoolID != poolID || seen[point.ID] {
				http.Error(w, "backup catalog is invalid", http.StatusServiceUnavailable)
				return
			}
			seen[point.ID] = true
		}
		serveBackupCatalogPage(w, q, points, repos, hostID, cursor)
		return
	}
	seen := make(map[string]bool, len(runs))
	for _, run := range runs {
		if run.PoolID != poolID || seen[run.OperationID] {
			http.Error(w, "backup runs are invalid", http.StatusServiceUnavailable)
			return
		}
		seen[run.OperationID] = true
	}
	serveBackupRunPage(w, q, runs, hostID, cursor)
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

func pageReady(w http.ResponseWriter, q url.Values, generation string, cursor, total int) bool {
	if q.Get("generation") != "" && q.Get("generation") != generation {
		http.Error(w, "backup history changed; restart from first page", http.StatusConflict)
		return false
	}
	if cursor > total {
		http.Error(w, "invalid backup page cursor", http.StatusBadRequest)
		return false
	}
	return true
}

// A page generation covers which records exist and their order, not their
// status, so settlements and protection changes never restart a walk. Run
// order is fixed by the immutable creation time, and runs are never deleted.
func serveBackupRunPage(w http.ResponseWriter, q url.Values, all []protocol.BackupRun, master string, cursor int) {
	ids := make([]string, len(all))
	for i, run := range all {
		ids[i] = run.OperationID
	}
	generation := pageGeneration(struct {
		Master string
		Runs   []string
	}{master, ids})
	if !pageReady(w, q, generation, cursor, len(all)) {
		return
	}
	page := protocol.BackupRunPage{Generation: generation, Items: []protocol.BackupRun{}}
	for _, run := range all {
		if run.Status == "failed" || run.Status == "missed" || run.Status == "needs_attention" {
			page.AttentionCount++
		}
		if run.Kind == protocol.BackupKindBackupNow || run.Kind == protocol.BackupKindScheduled || run.Kind == protocol.BackupKindPolicyRunNow {
			if page.LastBackupRun == nil || run.UpdatedAt.After(page.LastBackupRun.UpdatedAt) {
				page.LastBackupRun = &protocol.BackupRunSummary{Status: run.Status, UpdatedAt: run.UpdatedAt}
			}
		}
	}
	filtered := make([]protocol.BackupRun, 0, len(all))
	for _, run := range all {
		if point := q.Get("recovery_point_id"); point != "" && run.RecoveryPointID != point {
			continue
		}
		filtered = append(filtered, run)
	}
	if cursor > len(filtered) {
		http.Error(w, "invalid backup page cursor", http.StatusBadRequest)
		return
	}
	page.Total = len(filtered)
	end := pageEnd(filtered, cursor)
	if end == cursor && cursor < len(filtered) {
		http.Error(w, "backup run exceeds page limit", http.StatusRequestEntityTooLarge)
		return
	}
	page.Items = filtered[cursor:end]
	if end < len(filtered) {
		page.NextCursor = strconv.Itoa(end)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

func serveBackupCatalogPage(w http.ResponseWriter, q url.Values, all []protocol.BackupRecoveryPoint, repos []protocol.BackupRepository, master string, cursor int) {
	repoNames := map[string]string{}
	for _, repo := range repos {
		repoNames[repo.ID] = strings.ToLower(repo.Name)
	}
	ids := make([]string, len(all))
	for i, point := range all {
		ids[i] = point.ID
	}
	generation := pageGeneration(struct {
		Master string
		Points []string
		Names  map[string]string
	}{master, ids, repoNames})
	if !pageReady(w, q, generation, cursor, len(all)) {
		return
	}
	page := protocol.BackupCatalogPage{Generation: generation, Items: []protocol.BackupRecoveryPoint{}, BackupDays14: []string{}}
	days := map[string]bool{}
	cutoff := time.Now().UTC().AddDate(0, 0, -13).Format("2006-01-02")
	for _, point := range all {
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
	filtered := make([]protocol.BackupRecoveryPoint, 0, len(all))
	for _, point := range all {
		if vm := q.Get("vm_id"); vm != "" && point.SourceVMID != vm {
			continue
		}
		if repo := q.Get("repository_id"); repo != "" && point.RepositoryID != repo {
			continue
		}
		if search := strings.ToLower(strings.TrimSpace(q.Get("search"))); search != "" &&
			!strings.Contains(strings.ToLower(point.SourceVMName), search) && !strings.Contains(strings.ToLower(point.SourceVMID), search) &&
			!strings.Contains(strings.ToLower(point.ID), search) && !strings.Contains(strings.ToLower(point.RepositoryID), search) &&
			!strings.Contains(repoNames[point.RepositoryID], search) {
			continue
		}
		filtered = append(filtered, point)
	}
	if cursor > len(filtered) {
		http.Error(w, "invalid backup page cursor", http.StatusBadRequest)
		return
	}
	page.Total = len(filtered)
	end := pageEnd(filtered, cursor)
	if end == cursor && cursor < len(filtered) {
		http.Error(w, "backup point exceeds page limit", http.StatusRequestEntityTooLarge)
		return
	}
	page.Items = filtered[cursor:end]
	if end < len(filtered) {
		page.NextCursor = strconv.Itoa(end)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}
