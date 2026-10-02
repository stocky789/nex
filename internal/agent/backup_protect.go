// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolBackupPinsFile = "pool-backup-pins.json"

type poolBackupPinsState struct {
	Pins map[string]protocol.BackupRestorePin `json:"pins"`
}

func poolBackupPinsPath(s *Store) string {
	return filepath.Join(poolBackupStoreDir(s), poolBackupPinsFile)
}

func loadPoolBackupPins(s *Store) (poolBackupPinsState, error) {
	state := poolBackupPinsState{Pins: map[string]protocol.BackupRestorePin{}}
	raw, err := os.ReadFile(poolBackupPinsPath(s))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return poolBackupPinsState{}, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return poolBackupPinsState{}, err
	}
	if state.Pins == nil {
		state.Pins = map[string]protocol.BackupRestorePin{}
	}
	for key, pin := range state.Pins {
		if pin.Valid() != nil {
			return poolBackupPinsState{}, ErrBackupInvalid
		}
		if key != pin.OperationID {
			// Releases before the per-operation pin key stored one pin per recovery point.
			return poolBackupPinsState{}, fmt.Errorf("%w: the restore pin state at %s uses the old per-recovery-point format; finish or clean up unfinished restores and test restores on the previous release, then remove that file", ErrBackupInvalid, poolBackupPinsPath(s))
		}
	}
	return state, nil
}

func savePoolBackupPins(s *Store, state poolBackupPinsState) error {
	if state.Pins == nil {
		state.Pins = map[string]protocol.BackupRestorePin{}
	}
	if err := checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		auth.Pins = auth.Pins[:0]
		for _, pin := range state.Pins {
			auth.Pins = append(auth.Pins, pin)
		}
	}); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(poolBackupPinsPath(s), raw, 0600)
}

// ActiveBackupRestorePins returns recovery-point IDs currently pinned against prune.
func ActiveBackupRestorePins(s *Store) (map[string]bool, error) {
	state, err := loadPoolBackupPins(s)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(state.Pins))
	for _, pin := range state.Pins {
		out[pin.RecoveryPointID] = true
	}
	return out, nil
}

func backupPointPinned(state poolBackupPinsState, recoveryPointID string) bool {
	for _, pin := range state.Pins {
		if pin.RecoveryPointID == recoveryPointID {
			return true
		}
	}
	return false
}

func putBackupRestorePin(s *Store, pin protocol.BackupRestorePin) error {
	if err := pin.Valid(); err != nil {
		return ErrBackupInvalid
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	state, err := loadPoolBackupPins(s)
	if err != nil {
		return err
	}
	if existing, ok := state.Pins[pin.OperationID]; ok {
		if existing.RecoveryPointID != pin.RecoveryPointID || existing.Kind != pin.Kind {
			return ErrBackupConflict
		}
		return nil
	}
	if poolBackupPruning[pin.RecoveryPointID] {
		return ErrBackupConflict
	}
	state.Pins[pin.OperationID] = pin
	return savePoolBackupPins(s, state)
}

func clearBackupRestorePin(s *Store, recoveryPointID, operationID string) error {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	state, err := loadPoolBackupPins(s)
	if err != nil {
		return err
	}
	existing, ok := state.Pins[operationID]
	if !ok {
		return nil
	}
	if existing.RecoveryPointID != recoveryPointID {
		return ErrBackupConflict
	}
	delete(state.Pins, operationID)
	return savePoolBackupPins(s, state)
}

// Reconcile a terminal restore's leftover pin after an interrupted settlement.
// A successful test restore stays pinned until its test VM is cleaned up.
func reconcileTerminalBackupRestorePins(s *Store) error {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID != s.State.HostID {
		return nil
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	state, err := loadPoolBackupPins(s)
	if err != nil {
		return err
	}
	changed := false
	for operationID, pin := range state.Pins {
		run, err := loadPoolBackupRun(s, operationID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if run.OperationID != operationID || run.PoolID != s.State.PoolID || run.RecoveryPointID != pin.RecoveryPointID {
			continue
		}
		terminal := run.Status == "failed" && run.Stage == "failed"
		switch pin.Kind {
		case protocol.BackupPinKindNew:
			terminal = run.Kind == protocol.BackupKindRestoreNew && (terminal || run.Status == "succeeded" && run.Stage == "registered")
		case protocol.BackupPinKindReplace:
			terminal = run.Kind == protocol.BackupKindRestoreReplace && (terminal || run.Status == "succeeded" && run.Stage == "replaced")
		case protocol.BackupPinKindTest:
			terminal = run.Kind == protocol.BackupKindTestRestore && terminal
		default:
			terminal = false
		}
		if terminal {
			delete(state.Pins, operationID)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return savePoolBackupPins(s, state)
}

type backupPointMetadataKey struct {
	store, repositoryID, pointID string
}

// Reservations serialize marker changes without holding the authority mutex
// during repository I/O. Retention and repository removal check them at admission.
var backupPointMetadataReservations = map[backupPointMetadataKey]bool{}

// The caller holds poolBackupAuthorityMu.
func backupPointMetadataBusy(s *Store, repositoryID, pointID string) bool {
	return backupPointMetadataReservations[backupPointMetadataKey{s.dir, repositoryID, pointID}]
}

// The caller holds poolBackupAuthorityMu.
func backupRepositoryPointMetadataBusy(s *Store, repositoryID string) bool {
	for key := range backupPointMetadataReservations {
		if key.store == s.dir && key.repositoryID == repositoryID {
			return true
		}
	}
	return false
}

func reserveBackupPointMetadata(ctx context.Context, s *Store, repositoryID, pointID string) error {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	key := backupPointMetadataKey{s.dir, repositoryID, pointID}
	if poolBackupPruning[pointID] || backupPointMetadataReservations[key] {
		return ErrBackupConflict
	}
	backupPointMetadataReservations[key] = true
	return nil
}

func releaseBackupPointMetadata(s *Store, repositoryID, pointID string) {
	poolBackupAuthorityMu.Lock()
	delete(backupPointMetadataReservations, backupPointMetadataKey{s.dir, repositoryID, pointID})
	poolBackupAuthorityMu.Unlock()
}

// The caller holds poolBackupAuthorityMu. Protection requests supply their new
// choice; imports preserve the current choice, including a concurrent unprotect.
// The second result reports whether the merged point already matches the catalog.
func mergeBackupPointMetadata(s *Store, point protocol.BackupRecoveryPoint, preserveProtection bool) (protocol.BackupRecoveryPoint, bool, error) {
	prior, err := getCatalogPoint(s, point.RepositoryID, point.ID)
	if errors.Is(err, ErrBackupNotFound) {
		return point, false, nil
	}
	if err != nil {
		return point, false, err
	}
	if preserveProtection {
		point.Protected = prior.Protected
	}
	if prior.LastTestResult != "" {
		point.LastTestResult = prior.LastTestResult
		point.LastTestedAt = prior.LastTestedAt
		point.LastTestVMID = prior.LastTestVMID
	}
	return point, point == prior, nil
}

// The caller reserves the metadata and capacity-checks point. Verification may
// still settle during marker/capacity I/O; re-admit any newer merged record.
func commitBackupPointMetadata(ctx context.Context, s *Store, point protocol.BackupRecoveryPoint, preserveProtection bool) (protocol.BackupRecoveryPoint, error) {
	for {
		if err := ctx.Err(); err != nil {
			return point, err
		}
		poolBackupAuthorityMu.Lock()
		err := masterBackupPool(s, s.State.PoolID)
		if err == nil {
			err = requirePoolAuthorityWritable(s)
		}
		var current protocol.BackupRecoveryPoint
		if err == nil {
			// A forgotten repository must not regain catalog entries from an import or rebuild
			// that was already running when it was forgotten.
			err = requireBackupPointRepository(s, point.RepositoryID)
		}
		if err == nil {
			current, _, err = mergeBackupPointMetadata(s, point, preserveProtection)
		}
		if err == nil {
			err = current.Valid()
		}
		if err != nil {
			poolBackupAuthorityMu.Unlock()
			return point, err
		}
		if current == point {
			err = writePoolBackupCatalogEntry(s, current)
			poolBackupAuthorityMu.Unlock()
			return current, err
		}
		poolBackupAuthorityMu.Unlock()
		if err := checkBackupCatalogPointCapacity(s, current); err != nil {
			return point, err
		}
		point = current
	}
}

// requireBackupPointRepository refuses catalog writes for a repository that is gone or
// being forgotten. The caller holds poolBackupAuthorityMu.
func requireBackupPointRepository(s *Store, repositoryID string) error {
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	if _, ok := repos.Repositories[repositoryID]; !ok {
		return ErrBackupRepoNotFound
	}
	if backupForgetPending(s, repositoryID) {
		return ErrBackupConflict
	}
	return nil
}

// SetPoolBackupPointProtected updates catalog + repository protect marker.
func SetPoolBackupPointProtected(s *Store, req protocol.ProtectRecoveryPointRequest) (protocol.BackupRecoveryPoint, error) {
	if err := req.Valid(); err != nil {
		return protocol.BackupRecoveryPoint{}, ErrBackupInvalid
	}
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	repo, err := GetPoolBackupRepository(s, req.RepositoryID)
	if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manifest, digest, err := backend.StatPoint(ctx, req.RecoveryPointID)
	if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	if manifest.RecoveryPointID != req.RecoveryPointID || manifest.RepositoryID != req.RepositoryID || manifest.PoolID != s.State.PoolID {
		return protocol.BackupRecoveryPoint{}, ErrBackupConflict
	}
	if err := reserveBackupPointMetadata(ctx, s, req.RepositoryID, req.RecoveryPointID); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	defer releaseBackupPointMetadata(s, req.RepositoryID, req.RecoveryPointID)
	poolBackupAuthorityMu.Lock()
	point, err := getCatalogPoint(s, req.RepositoryID, req.RecoveryPointID)
	poolBackupAuthorityMu.Unlock()
	if errors.Is(err, ErrBackupNotFound) {
		point = protocol.BackupRecoveryPoint{
			ID: req.RecoveryPointID, RepositoryID: req.RepositoryID, PoolID: s.State.PoolID,
			SourceVMID: manifest.SourceVMID, SourceVMName: manifest.SourceVMName,
			CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency,
			ManifestSHA256: digest,
		}
	} else if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	point.Protected = req.Protected
	if err := checkBackupCatalogPointCapacity(s, point); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	if err := backend.SetProtected(ctx, req.RecoveryPointID, req.Protected); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	return commitBackupPointMetadata(ctx, s, point, false)
}

func getCatalogPoint(s *Store, repositoryID, pointID string) (protocol.BackupRecoveryPoint, error) {
	if !protocol.ValidSnapshotOperationID(repositoryID) || !protocol.ValidSnapshotOperationID(pointID) {
		return protocol.BackupRecoveryPoint{}, ErrBackupInvalid
	}
	raw, err := readBoundedPoolBackupFile(filepath.Join(poolBackupCatalogPath(s), pointID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return protocol.BackupRecoveryPoint{}, ErrBackupNotFound
	}
	if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	var point protocol.BackupRecoveryPoint
	if len(raw) > protocol.PoolBackupPageLimit || json.Unmarshal(raw, &point) != nil ||
		point.Valid() != nil || point.ID != pointID || point.PoolID != s.State.PoolID {
		return protocol.BackupRecoveryPoint{}, errors.New("backup catalog entry is corrupt")
	}
	if point.RepositoryID != repositoryID {
		return protocol.BackupRecoveryPoint{}, ErrBackupNotFound
	}
	return point, nil
}
