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
	// Older state keyed each pin by recovery point. Reindex the values so an
	// unresolved restore does not exclude a different target using that point.
	byOperation := make(map[string]protocol.BackupRestorePin, len(state.Pins))
	for key, pin := range state.Pins {
		if pin.Valid() != nil || key != pin.OperationID && key != pin.RecoveryPointID {
			return poolBackupPinsState{}, ErrBackupInvalid
		}
		if _, ok := byOperation[pin.OperationID]; ok {
			return poolBackupPinsState{}, ErrBackupConflict
		}
		byOperation[pin.OperationID] = pin
	}
	state.Pins = byOperation
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
	manifest, digest, err := backend.ReadPoint(ctx, req.RecoveryPointID)
	if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	if manifest.RecoveryPointID != req.RecoveryPointID || manifest.RepositoryID != req.RepositoryID || manifest.PoolID != s.State.PoolID {
		return protocol.BackupRecoveryPoint{}, ErrBackupConflict
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := requirePoolAuthorityWritable(s); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	catalog, err := loadPoolBackupCatalog(s)
	if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	point := protocol.BackupRecoveryPoint{
		ID: req.RecoveryPointID, RepositoryID: req.RepositoryID, PoolID: s.State.PoolID,
		SourceVMID: manifest.SourceVMID, SourceVMName: manifest.SourceVMName,
		CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency,
		ManifestSHA256: digest, Protected: req.Protected,
	}
	for i := range catalog {
		if catalog[i].ID == req.RecoveryPointID && catalog[i].RepositoryID == req.RepositoryID {
			point = catalog[i]
			point.Protected = req.Protected
			break
		}
	}
	if err := checkBackupCatalogPointCapacity(s, point); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	if err := backend.SetProtected(ctx, req.RecoveryPointID, req.Protected); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	if err := savePoolBackupCatalogEntry(s, point); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	return point, nil
}

func getCatalogPoint(s *Store, repositoryID, pointID string) (protocol.BackupRecoveryPoint, error) {
	catalog, err := loadPoolBackupCatalog(s)
	if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	for _, point := range catalog {
		if point.ID == pointID && point.RepositoryID == repositoryID {
			return point, nil
		}
	}
	return protocol.BackupRecoveryPoint{}, ErrBackupNotFound
}
