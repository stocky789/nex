// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolBackupOrphanCleanupsDir = "pool-backup-orphan-cleanups"

// orphanSizingBudget must stay under the Core HTTP write timeout (30 s) and the Controller's
// relay wait (10 s); points not sized in time are counted, not guessed.
var orphanSizingBudget = 6 * time.Second

// The cleanup record is rewritten whole, so a long plan saves every few points, not each.
const (
	orphanCheckpointPoints   = 25
	orphanCheckpointInterval = 2 * time.Second
)

// orphanView is a repository's recovery points whose VM is not in the master's VM inventory.
type orphanView struct {
	repo     protocol.BackupRepository
	byVM     map[string][]protocol.BackupRecoveryPoint
	inUse    map[string]bool // points pinned by an unfinished restore or named by a pending run
	busyVMs  map[string]bool // VMs a pending run on the repository names
	warnings []string
}

func (v orphanView) skipped(point protocol.BackupRecoveryPoint) bool {
	return v.inUse[point.ID] || v.busyVMs[point.SourceVMID]
}

// poolVMIDs is the master's durable VM inventory. A host that is offline still has its VMs
// here, so an outage never makes a VM look absent; a missing inventory is not evidence of
// absence either, so it is refused.
func poolVMIDs(s *Store) (map[string]bool, error) {
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	if _, err := os.Stat(poolVMFile(s)); err != nil {
		return nil, fmt.Errorf("%w: the pool VM inventory is not available on this host", ErrBackupConflict)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return nil, fmt.Errorf("%w: the pool VM inventory cannot be read", ErrBackupConflict)
	}
	ids := make(map[string]bool, len(state.VMs))
	for id := range state.VMs {
		ids[id] = true
	}
	return ids, nil
}

func viewBackupOrphans(s *Store, repositoryID string) (orphanView, error) {
	view, policies, catalog, err := snapshotBackupOrphans(s, repositoryID)
	if err != nil {
		return orphanView{}, err
	}
	// The inventory is read after the catalog, so a VM created and backed up in between is
	// never reported as an orphan. It is read outside the authority lock, which poolVMMu
	// must not be taken under.
	vms, err := poolVMIDs(s)
	if err != nil {
		return orphanView{}, err
	}
	for _, policy := range policies {
		for _, id := range policy.VMIDs {
			if policy.RepositoryID == repositoryID && !vms[id] {
				view.warnings = append(view.warnings, fmt.Sprintf("backup job %q still lists VM %s, which is not in the pool", policy.Name, id))
			}
		}
	}
	sort.Strings(view.warnings)
	for _, point := range catalog {
		if point.RepositoryID == repositoryID && !vms[point.SourceVMID] {
			view.byVM[point.SourceVMID] = append(view.byVM[point.SourceVMID], point)
		}
	}
	return view, nil
}

// snapshotBackupOrphans reads the repository, its busy state and the catalog consistently.
func snapshotBackupOrphans(s *Store, repositoryID string) (orphanView, map[string]protocol.BackupPolicy, []protocol.BackupRecoveryPoint, error) {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return orphanView{}, nil, nil, err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return orphanView{}, nil, nil, err
	}
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return orphanView{}, nil, nil, err
	}
	view := orphanView{byVM: map[string][]protocol.BackupRecoveryPoint{}, inUse: map[string]bool{}, busyVMs: map[string]bool{}}
	var ok bool
	if view.repo, ok = repos.Repositories[repositoryID]; !ok {
		return orphanView{}, nil, nil, ErrBackupRepoNotFound
	}
	catalog, err := loadPoolBackupCatalog(s)
	if err != nil {
		return orphanView{}, nil, nil, err
	}
	if view.inUse, err = ActiveBackupRestorePins(s); err != nil {
		return orphanView{}, nil, nil, err
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return orphanView{}, nil, nil, err
	}
	for _, run := range runs {
		if run.RepositoryID == repositoryID && run.Status == "pending" {
			view.busyVMs[run.VMID] = true
			view.inUse[run.RecoveryPointID] = true
		}
	}
	policies, err := loadPoolBackupPolicies(s)
	if err != nil {
		return orphanView{}, nil, nil, err
	}
	return view, policies.Policies, catalog, nil
}

// queryBackupOrphans is read-only. Sizes come from the destination's manifests within a fixed
// budget; points it could not size are counted instead of guessed.
// ponytail: sequential stat calls; parallelise if a repository holds thousands of orphans.
func queryBackupOrphans(ctx context.Context, s *Store, repositoryID string) (protocol.BackupOrphanQuery, error) {
	if !protocol.ValidSnapshotOperationID(repositoryID) {
		return protocol.BackupOrphanQuery{}, ErrBackupInvalid
	}
	view, err := viewBackupOrphans(s, repositoryID)
	if err != nil {
		return protocol.BackupOrphanQuery{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, orphanSizingBudget)
	defer cancel()
	backend, backendErr := openBackupRepositoryStorage(s, view.repo)
	out := protocol.BackupOrphanQuery{RepositoryID: repositoryID, Groups: []protocol.BackupOrphanGroup{}, Warnings: view.warnings}
	for vmID, points := range view.byVM {
		group := protocol.BackupOrphanGroup{VMID: vmID, RecoveryPoints: len(points), Oldest: points[0].CapturedAt, Newest: points[0].CapturedAt}
		for _, point := range points {
			if point.Protected {
				group.ProtectedPoints++
			}
			if view.skipped(point) {
				group.InUse = true
			}
			if point.CapturedAt.Before(group.Oldest) {
				group.Oldest = point.CapturedAt
			}
			if !point.CapturedAt.Before(group.Newest) {
				group.Newest, group.VMName = point.CapturedAt, point.SourceVMName
			}
			if backendErr != nil {
				group.SizeUnknownPoints++
				continue
			}
			manifest, _, err := backend.StatPoint(ctx, point.ID)
			if err != nil {
				group.SizeUnknownPoints++
				continue
			}
			for _, disk := range manifest.Disks {
				group.SizeBytes += disk.Size
			}
			for _, firmware := range manifest.Firmware {
				group.SizeBytes += firmware.Size
			}
		}
		out.Groups = append(out.Groups, group)
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].VMID < out.Groups[j].VMID })
	return out, nil
}

// orphanAuthority is checked before every delete, so a cleanup that outlives the pool's
// authority (or is resumed on a deposed host) stops instead of deleting.
func orphanAuthority(s *Store) error {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return err
	}
	return requirePoolAuthorityWritable(s)
}

func orphanCleanupsPath(s *Store) string {
	return filepath.Join(poolBackupStoreDir(s), poolBackupOrphanCleanupsDir)
}

func orphanCleanupPath(s *Store, operationID string) string {
	return filepath.Join(orphanCleanupsPath(s), operationID+".json")
}

func listOrphanCleanups(s *Store) ([]protocol.BackupOrphanCleanup, error) {
	entries, err := os.ReadDir(orphanCleanupsPath(s))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cleanups []protocol.BackupOrphanCleanup
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		cleanup, err := loadOrphanCleanup(s, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		cleanups = append(cleanups, cleanup)
	}
	return cleanups, nil
}

func loadOrphanCleanup(s *Store, operationID string) (protocol.BackupOrphanCleanup, error) {
	var rec protocol.BackupOrphanCleanup
	raw, err := os.ReadFile(orphanCleanupPath(s, operationID))
	if err != nil {
		return rec, err
	}
	if json.Unmarshal(raw, &rec) != nil || rec.OperationID != operationID {
		return rec, errors.New("orphaned backup cleanup record is corrupt")
	}
	return rec, nil
}

func saveOrphanCleanup(s *Store, rec protocol.BackupOrphanCleanup) error {
	rec.UpdatedAt = time.Now().UTC()
	// The record is replicated in the pool authority; one the authority refuses would stop
	// every later publication, so it is never written.
	if err := rec.Valid(); err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(orphanCleanupPath(s, rec.OperationID)), 0700); err != nil {
		return err
	}
	return vmhost.AtomicWrite(orphanCleanupPath(s, rec.OperationID), raw, 0600)
}

// planOrphanCleanup re-checks the confirmed selection against the master's current state and
// records which points will be deleted and which are skipped, before anything is deleted.
func planOrphanCleanup(s *Store, operationID string, req protocol.OrphanCleanupRequest) (protocol.BackupOrphanCleanup, error) {
	rec := protocol.BackupOrphanCleanup{OperationID: operationID, Request: req, Status: "running", Points: []protocol.BackupOrphanPointResult{}}
	if want := protocol.OrphanCleanupConfirmation(req.RecoveryPoints, req.ProtectedPoints, req.IncludeProtected); req.Confirmation != want {
		return rec, backupRefusal(fmt.Sprintf("type %q to confirm", want))
	}
	view, err := viewBackupOrphans(s, req.RepositoryID)
	if err != nil {
		return rec, err
	}
	if view.busyVMs[""] {
		return rec, backupRefusal("a restore or test restore is running on this repository; wait for it to finish")
	}
	points, protected := 0, 0
	for _, vmID := range req.VMIDs {
		if len(view.byVM[vmID]) == 0 {
			return rec, backupRefusal(fmt.Sprintf("VM %s has no orphaned recovery points in this repository now; review and confirm again", vmID))
		}
		sort.Slice(view.byVM[vmID], func(i, j int) bool { return view.byVM[vmID][i].CapturedAt.Before(view.byVM[vmID][j].CapturedAt) })
		for _, point := range view.byVM[vmID] {
			points++
			result := protocol.BackupOrphanPointResult{RecoveryPointID: point.ID, VMID: vmID, Status: "pending"}
			if point.Protected {
				protected++
				if !req.IncludeProtected {
					result.Status, result.Reason = "skipped", "protected"
				}
			}
			if view.skipped(point) {
				result.Status, result.Reason = "skipped", "in use by an unfinished restore, test restore or backup"
			}
			rec.Points = append(rec.Points, result)
		}
	}
	if points != req.RecoveryPoints || protected != req.ProtectedPoints {
		return rec, backupRefusal(fmt.Sprintf("these VMs now hold %d orphaned recovery points (%d protected), not the %d (%d protected) confirmed; review and confirm again",
			points, protected, req.RecoveryPoints, req.ProtectedPoints))
	}
	return rec, saveOrphanCleanup(s, rec)
}

// cleanupBackupOrphans deletes the planned points through the retention delete, which removes
// the destination's data before the catalog entry. The plan is durable, so a repeat of the same
// operation continues where it stopped without rechecking the confirmed counts.
func cleanupBackupOrphans(ctx context.Context, s *Store, operationID string, req protocol.OrphanCleanupRequest) (protocol.BackupOrphanCleanup, error) {
	rec, err := loadOrphanCleanup(s, operationID)
	if errors.Is(err, os.ErrNotExist) {
		rec, err = planOrphanCleanup(s, operationID, req)
	}
	if err != nil || rec.Status != "running" {
		return rec, err
	}
	poolBackupRetentionMu.Lock()
	defer poolBackupRetentionMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	repo, err := GetPoolBackupRepository(s, req.RepositoryID)
	if err != nil {
		return rec, err
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		return rec, err
	}
	// The plan is rewritten whole, so progress is checkpointed in batches. A crash repeats at
	// most one batch, and deleting an already deleted point is confirmed, not repeated.
	unsaved, lastSave := 0, time.Now()
	checkpoint := func(force bool) error {
		if unsaved == 0 || !force && unsaved < orphanCheckpointPoints && time.Since(lastSave) < orphanCheckpointInterval {
			return nil
		}
		if err := saveOrphanCleanup(s, rec); err != nil {
			return err
		}
		unsaved, lastSave = 0, time.Now()
		return nil
	}
	loopErr := func() error {
		for i := range rec.Points {
			if rec.Points[i].Status != "pending" {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := orphanAuthority(s); err != nil {
				return err
			}
			result := &rec.Points[i]
			if err := deleteOrphanedPoint(ctx, s, backend, req, result); err != nil {
				result.Status, result.Reason = "failed", resultError(err.Error())
			}
			unsaved++
			if err := checkpoint(false); err != nil {
				return err
			}
		}
		return nil
	}()
	if saveErr := checkpoint(true); loopErr == nil {
		loopErr = saveErr
	}
	if loopErr != nil {
		return rec, loopErr
	}
	if sweeper, ok := backend.(interface{ SweepDeleteMarkers(context.Context) }); ok {
		sweeper.SweepDeleteMarkers(ctx)
	}
	rec.Status = "succeeded"
	failed := 0
	for _, result := range rec.Points {
		if result.Status == "failed" {
			failed++
		}
	}
	if failed > 0 {
		rec.Status, rec.Error = "failed", fmt.Sprintf("%d recovery points could not be deleted; fix the destination and run the cleanup again", failed)
	}
	return rec, saveOrphanCleanup(s, rec)
}

func deleteOrphanedPoint(ctx context.Context, s *Store, backend backupRepositoryStorage, req protocol.OrphanCleanupRequest, result *protocol.BackupOrphanPointResult) error {
	// The VM may have been re-created since the plan; its points are then no longer orphans.
	vms, err := poolVMIDs(s)
	if err != nil {
		return err
	}
	if vms[result.VMID] {
		result.Status, result.Reason = "skipped", "the VM exists again"
		return nil
	}
	point, err := getCatalogPoint(s, req.RepositoryID, result.RecoveryPointID)
	if errors.Is(err, ErrBackupNotFound) {
		// Deleted only when the destination confirms it; a forgotten or re-cataloged
		// repository still holds the bytes.
		if _, _, err := backend.StatPoint(ctx, result.RecoveryPointID); errors.Is(err, os.ErrNotExist) {
			result.Status = "deleted"
			return nil
		} else if err != nil {
			return fmt.Errorf("the destination could not confirm the point is gone: %w", err)
		}
		result.Status, result.Reason = "skipped", "not in the catalog but still on the destination; rebuild the catalog and review again"
		return nil
	}
	if err != nil {
		return err
	}
	reprotect := false
	if point.Protected {
		if !req.IncludeProtected {
			result.Status, result.Reason = "skipped", "protected"
			return nil
		}
		pins, err := ActiveBackupRestorePins(s)
		if err != nil {
			return err
		}
		if pins[point.ID] {
			result.Status, result.Reason = "skipped", "in use by an unfinished restore or test restore"
			return nil
		}
		// The delete refuses protected points, so protection is lifted for it and put back
		// unless the point is really gone from the catalog after prune.
		if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{RepositoryID: req.RepositoryID, RecoveryPointID: point.ID}); err != nil {
			return err
		}
		reprotect = true
	}
	err = pruneRecoveryPoint(ctx, s, backend, req.RepositoryID, point.ID)
	// pruneRecoveryPoint leaves a point pinned by a restore that started meanwhile in place.
	_, catalogErr := getCatalogPoint(s, req.RepositoryID, point.ID)
	stillCataloged := catalogErr == nil
	if reprotect && stillCataloged {
		if _, protectErr := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{RepositoryID: req.RepositoryID, RecoveryPointID: point.ID, Protected: true}); protectErr != nil {
			err = errors.Join(err, fmt.Errorf("protection could not be restored: %w", protectErr))
		}
	}
	if err != nil {
		return err
	}
	if stillCataloged {
		result.Status, result.Reason = "skipped", "in use by an unfinished restore or test restore"
		return nil
	}
	result.Status = "deleted"
	return nil
}

// serveBackupOrphans answers the Controller's read-only queries: the orphans of a repository, or
// the record of one cleanup.
func serveBackupOrphans(w http.ResponseWriter, r *http.Request, s *Store) {
	if s == nil {
		http.Error(w, "backup history unavailable", http.StatusServiceUnavailable)
		return
	}
	var value any
	var err error
	if id := r.URL.Query().Get("request_id"); id != "" {
		if !protocol.ValidSnapshotOperationID(id) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		value, err = loadOrphanCleanup(s, id)
	} else {
		value, err = queryBackupOrphans(r.Context(), s, r.URL.Query().Get("repository_id"))
	}
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, ErrBackupRepoNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrBackupInvalid):
		http.Error(w, "invalid request", http.StatusBadRequest)
	case errors.Is(err, ErrBackupConflict):
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	case err != nil:
		http.Error(w, "pool master required", http.StatusServiceUnavailable)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
}
