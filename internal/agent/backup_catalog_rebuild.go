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

// importPoolBackupCatalog scans a repository and merges validated manifests into the catalog.
// Existing protected flags, test results and restore pins are preserved across rebuild.
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
	// The scan runs unlocked. If retention deletes a point meanwhile, scan again
	// rather than cataloging a point that no longer exists.
	var scanned []protocol.BackupRecoveryPoint
	for attempt := 0; ; attempt++ {
		pruned := poolBackupPruneSeq.Load()
		scanned, err = backend.Scan(ctx)
		if err != nil {
			return nil, err
		}
		poolBackupAuthorityMu.Lock()
		if poolBackupPruneSeq.Load() == pruned {
			break
		}
		poolBackupAuthorityMu.Unlock()
		if attempt == 2 {
			return nil, ErrBackupUnavailable
		}
	}
	defer poolBackupAuthorityMu.Unlock()
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return nil, err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return nil, err
	}
	if _, err := loadPoolBackupPins(s); err != nil {
		return nil, err
	}
	existing, err := loadPoolBackupCatalog(s)
	if err != nil {
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
			return nil, err
		}
		scanned[i] = point
	}
	if err := checkBackupCatalogPointCapacity(s, scanned...); err != nil {
		return nil, err
	}
	imported := make([]protocol.BackupRecoveryPoint, 0, len(scanned))
	for _, point := range scanned {
		if err := ctx.Err(); err != nil {
			return imported, err
		}
		if point.Protected {
			if err := backend.SetProtected(ctx, point.ID, true); err != nil {
				return imported, err
			}
		}
		if err := writePoolBackupCatalogEntry(s, point); err != nil {
			return imported, err
		}
		imported = append(imported, point)
	}
	return imported, nil
}

// RebuildPoolBackupCatalogFromRepository scans and imports before removing stale entries.
func RebuildPoolBackupCatalogFromRepository(ctx context.Context, s *Store, repositoryID string) ([]protocol.BackupRecoveryPoint, error) {
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return nil, err
	}
	imported, err := importPoolBackupCatalog(ctx, s, repositoryID)
	if err != nil {
		return nil, err
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
	repo, err := GetPoolBackupRepository(s, repositoryID)
	if err != nil {
		return imported, err
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		return imported, err
	}
	entries, err := backend.Entries(ctx)
	if err != nil {
		return imported, err
	}
	for _, name := range entries {
		if name != "" && name[0] != '.' && protocol.ValidSnapshotOperationID(name) && !seen[name] {
			return imported, errors.New("repository contains an unreadable or invalid recovery point: " + name)
		}
	}
	for _, point := range existing {
		if point.RepositoryID == repositoryID && !seen[point.ID] && !point.Protected {
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

// LoadRetainedOriginal returns the recover-original path for a replace operation.
func LoadRetainedOriginal(s *Store, operationID string) (retainedOriginalRecord, error) {
	raw, err := os.ReadFile(retainedOriginalPath(s, operationID))
	if err != nil {
		return retainedOriginalRecord{}, err
	}
	var rec retainedOriginalRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return retainedOriginalRecord{}, err
	}
	return rec, nil
}
