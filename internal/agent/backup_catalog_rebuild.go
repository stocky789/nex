// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

var writeImportedBackupProtectMarker = vmhost.WriteBackupProtectMarker
var checkImportedBackupCatalogPointCapacity = checkBackupCatalogPointCapacity
var listBackupCatalogRepositoryEntries = func(ctx context.Context, backend backupRepositoryStorage) ([]string, error) {
	return backend.Entries(ctx)
}

// importPoolBackupCatalog scans a repository and merges validated manifests into the catalog.
// Existing protected flags, test results and restore pins are preserved across rebuild.
// Capacity probes and repository marker writes run unlocked so one slow repository cannot
// hold poolBackupAuthorityMu for minutes; the lock is taken only for local catalog reads/writes.
func importPoolBackupCatalog(ctx context.Context, s *Store, repositoryID string) ([]protocol.BackupRecoveryPoint, error) {
	if !protocol.ValidSnapshotOperationID(repositoryID) {
		return nil, ErrBackupInvalid
	}
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return nil, err
	}
	repo, err := GetPoolBackupRepository(s, repositoryID)
	if err != nil {
		return nil, err
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// The scan and remote marker work run unlocked. If retention deletes a point
	// meanwhile, scan again rather than cataloging a point that no longer exists.
	for attempt := 0; ; attempt++ {
		pruned := poolBackupPruneSeq.Load()
		scanned, err := backend.Scan(ctx)
		if err != nil {
			return nil, err
		}
		poolBackupAuthorityMu.Lock()
		if poolBackupPruneSeq.Load() != pruned {
			poolBackupAuthorityMu.Unlock()
			if attempt == 2 {
				return nil, ErrBackupUnavailable
			}
			continue
		}
		if err := masterBackupPool(s, s.State.PoolID); err != nil {
			poolBackupAuthorityMu.Unlock()
			return nil, err
		}
		if err := requirePoolAuthorityWritable(s); err != nil {
			poolBackupAuthorityMu.Unlock()
			return nil, err
		}
		if _, err := loadPoolBackupPins(s); err != nil {
			poolBackupAuthorityMu.Unlock()
			return nil, err
		}
		existing, err := loadPoolBackupCatalog(s)
		if err != nil {
			poolBackupAuthorityMu.Unlock()
			return nil, err
		}
		byID := map[string]protocol.BackupRecoveryPoint{}
		for _, point := range existing {
			if point.RepositoryID == repositoryID {
				byID[point.ID] = point
			}
		}
		for i, point := range scanned {
			if prior, ok := byID[point.ID]; ok {
				// The catalog holds the operator's latest protection choice; the scanned
				// marker may predate an unprotect made during the scan.
				point.Protected = prior.Protected
				if prior.LastTestResult != "" {
					point.LastTestResult = prior.LastTestResult
					point.LastTestedAt = prior.LastTestedAt
					point.LastTestVMID = prior.LastTestVMID
				}
			}
			if err := point.Valid(); err != nil {
				poolBackupAuthorityMu.Unlock()
				return nil, err
			}
			scanned[i] = point
		}
		poolBackupAuthorityMu.Unlock()
		// Capacity may probe ready members over HTTP; keep that off the backup lock.
		if err := checkImportedBackupCatalogPointCapacity(s, scanned...); err != nil {
			return nil, err
		}
		imported := make([]protocol.BackupRecoveryPoint, 0, len(scanned))
		restart := false
		for _, point := range scanned {
			if err := ctx.Err(); err != nil {
				return imported, err
			}
			point, writeErr := func() (protocol.BackupRecoveryPoint, error) {
				admitted := point
				if err := reserveBackupPointMetadata(ctx, s, repositoryID, point.ID); err != nil {
					return point, err
				}
				defer releaseBackupPointMetadata(s, repositoryID, point.ID)
				poolBackupAuthorityMu.Lock()
				if poolBackupPruneSeq.Load() != pruned {
					poolBackupAuthorityMu.Unlock()
					restart = true
					return point, nil
				}
				point, unchanged, err := mergeBackupPointMetadata(s, point, true)
				poolBackupAuthorityMu.Unlock()
				if err != nil {
					return point, err
				}
				// Reuse bulk admission only for an existing record that is still unchanged.
				// New records and concurrent metadata changes need fresh admission.
				if prior, cataloged := byID[point.ID]; !cataloged || prior != admitted || point != admitted || !unchanged {
					if err := checkBackupCatalogPointCapacity(s, point); err != nil {
						return point, err
					}
				}
				if point.Protected {
					if err := backend.SetProtected(ctx, point.ID, true); err != nil {
						return point, err
					}
				}
				return commitBackupPointMetadata(ctx, s, point, true)
			}()
			if writeErr != nil {
				return imported, writeErr
			}
			if restart {
				break
			}
			imported = append(imported, point)
		}
		if restart {
			if attempt == 2 {
				return nil, ErrBackupUnavailable
			}
			continue
		}
		return imported, nil
	}
}

// RebuildPoolBackupCatalogFromRepository scans and imports before removing stale entries.
// A cancelled or failed import returns without stale deletion so a partial pass cannot
// drop catalog entries that were not re-imported. Repository listing runs unlocked.
func RebuildPoolBackupCatalogFromRepository(ctx context.Context, s *Store, repositoryID string) ([]protocol.BackupRecoveryPoint, error) {
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return nil, err
	}
	imported, err := importPoolBackupCatalog(ctx, s, repositoryID)
	if err != nil {
		return nil, err
	}
	repo, err := GetPoolBackupRepository(s, repositoryID)
	if err != nil {
		return imported, err
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		return imported, err
	}
	// Only records observed before the unlocked listing can be stale candidates.
	// Settlement after that snapshot must survive even if the listing missed it.
	poolBackupAuthorityMu.Lock()
	before, err := loadPoolBackupCatalog(s)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		return imported, err
	}
	candidates := make(map[string]protocol.BackupRecoveryPoint, len(before))
	for _, point := range before {
		if point.RepositoryID == repositoryID {
			candidates[point.ID] = point
		}
	}
	entries, err := listBackupCatalogRepositoryEntries(ctx, backend)
	if err != nil {
		return imported, err
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := requirePoolAuthorityWritable(s); err != nil {
		return imported, err
	}
	pins, err := loadPoolBackupPins(s)
	if err != nil {
		return imported, err
	}
	existing, err := loadPoolBackupCatalog(s)
	if err != nil {
		return imported, err
	}
	seen := make(map[string]bool, len(imported))
	for _, point := range imported {
		seen[point.ID] = true
	}
	for _, name := range entries {
		if name != "" && name[0] != '.' && protocol.ValidSnapshotOperationID(name) && !seen[name] {
			return imported, errors.New("repository contains an unreadable or invalid recovery point: " + name)
		}
	}
	for _, point := range existing {
		prior, candidate := candidates[point.ID]
		if candidate && prior == point && !seen[point.ID] && !point.Protected &&
			!backupPointMetadataBusy(s, repositoryID, point.ID) {
			if pinned := backupPointPinned(pins, point.ID); pinned {
				continue
			}
			path := filepath.Join(poolBackupCatalogPath(s), point.ID+".json")
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return imported, err
			}
		}
	}
	return imported, nil
}

type retainedOriginalRecord struct {
	OperationID     string            `json:"operation_id"`
	OriginalVMID    string            `json:"original_vm_id"`
	DiskPaths       map[string]string `json:"disk_paths"`
	RecoveryPointID string            `json:"recovery_point_id"`
	CreatedAt       time.Time         `json:"created_at"`
}

func retainedOriginalPath(s *Store, operationID string) string {
	return filepath.Join(s.dir, "backup-replace", operationID, "retained-original.json")
}

func writeRetainedOriginal(s *Store, rec retainedOriginalRecord) error {
	if err := os.MkdirAll(filepath.Dir(retainedOriginalPath(s, rec.OperationID)), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(retainedOriginalPath(s, rec.OperationID), raw, 0600)
}
