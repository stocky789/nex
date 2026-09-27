// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

var poolBackupAuthorityMu sync.Mutex

var verifyBackupRepository = vmhost.VerifyBackupRepository
var prepareBackupRepositoryNative = vmhost.PrepareBackupRepository

const (
	poolBackupRepositoriesFile = "pool-backup-repositories.json"
	poolBackupCatalogDir       = "pool-backup-catalog"
	poolBackupRunsDir          = "pool-backup-runs"
	poolBackupReceiptsDir      = "pool-backup-receipts"
)

var (
	ErrBackupInvalid      = errors.New("invalid backup request")
	ErrBackupConflict     = errors.New("backup request conflicts with retained work")
	ErrBackupNotFound     = errors.New("backup operation not found")
	ErrBackupUnavailable  = errors.New("backup state is unavailable on this host")
	ErrBackupRepoNotFound = errors.New("backup repository not found")
)

type poolBackupRepositoriesState struct {
	Repositories map[string]protocol.BackupRepository `json:"repositories"`
}

func poolBackupRepositoriesPath(s *Store) string {
	return filepath.Join(poolBackupStoreDir(s), poolBackupRepositoriesFile)
}

func poolBackupCatalogPath(s *Store) string {
	return filepath.Join(poolBackupStoreDir(s), poolBackupCatalogDir)
}

func poolBackupRunsPath(s *Store) string {
	return filepath.Join(poolBackupStoreDir(s), poolBackupRunsDir)
}

func poolBackupReceiptsPath(s *Store) string {
	return filepath.Join(poolBackupStoreDir(s), poolBackupReceiptsDir)
}

func loadPoolBackupMetadataReceipt(s *Store, operationID string) (protocol.BackupMetadataReceipt, error) {
	raw, err := os.ReadFile(filepath.Join(poolBackupReceiptsPath(s), operationID+".json"))
	if err != nil {
		return protocol.BackupMetadataReceipt{}, err
	}
	var receipt protocol.BackupMetadataReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Valid() != nil || receipt.OperationID != operationID {
		return protocol.BackupMetadataReceipt{}, errors.New("backup metadata receipt is corrupt")
	}
	return receipt, nil
}

func savePoolBackupMetadataReceipt(s *Store, receipt protocol.BackupMetadataReceipt) error {
	receipt.Error = resultError(receipt.Error)
	if err := receipt.Valid(); err != nil {
		return err
	}
	if err := checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		for i := range auth.MetadataReceipts {
			if auth.MetadataReceipts[i].OperationID == receipt.OperationID {
				auth.MetadataReceipts[i] = receipt
				return
			}
		}
		auth.MetadataReceipts = append(auth.MetadataReceipts, receipt)
	}); err != nil {
		return err
	}
	if err := os.MkdirAll(poolBackupReceiptsPath(s), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(filepath.Join(poolBackupReceiptsPath(s), receipt.OperationID+".json"), raw, 0600)
}

func listPoolBackupMetadataReceipts(s *Store) ([]protocol.BackupMetadataReceipt, error) {
	entries, err := os.ReadDir(poolBackupReceiptsPath(s))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipts []protocol.BackupMetadataReceipt
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		receipt, err := loadPoolBackupMetadataReceipt(s, stringsTrimSuffixJSON(entry.Name()))
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].OperationID < receipts[j].OperationID })
	return receipts, nil
}

func loadPoolBackupRepositories(s *Store) (poolBackupRepositoriesState, error) {
	state := poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{}}
	b, err := os.ReadFile(poolBackupRepositoriesPath(s))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return poolBackupRepositoriesState{}, err
	}
	if state.Repositories == nil {
		state.Repositories = map[string]protocol.BackupRepository{}
	}
	return state, nil
}

func savePoolBackupRepositories(s *Store, state poolBackupRepositoriesState) error {
	if state.Repositories == nil {
		state.Repositories = map[string]protocol.BackupRepository{}
	}
	if err := checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		auth.Repositories = auth.Repositories[:0]
		for _, repo := range state.Repositories {
			repo.Access = protocol.StorageAccess{}
			repo.MemberAccess = nil
			auth.Repositories = append(auth.Repositories, repo)
		}
	}); err != nil {
		return err
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(poolBackupRepositoriesPath(s), b, 0600)
}

// UpsertPoolBackupRepository registers or updates an NFS backup destination on the master.
func UpsertPoolBackupRepository(s *Store, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	prepared, err := prepareBackupRepository(context.Background(), repo)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	return upsertPoolBackupRepositoryPrepared(s, repo, prepared)
}

func prepareBackupRepository(ctx context.Context, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	if err := repo.Valid(); err != nil {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	if repo.MountPath != "" || repo.Identity != nil {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	return prepareBackupRepositoryNative(ctx, repo)
}

func upsertPoolBackupRepositoryPrepared(s *Store, repo, prepared protocol.BackupRepository) (protocol.BackupRepository, error) {
	if repo.Valid() != nil || prepared.Valid() != nil || !reflect.DeepEqual(repo.Source, prepared.Source) ||
		repo.ID != prepared.ID || repo.PoolID != prepared.PoolID || prepared.MountPath == "" || prepared.Identity == nil {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID || membership.PoolID != repo.PoolID {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return protocol.BackupRepository{}, err
	}
	repo = prepared
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	now := time.Now().UTC()
	if existing, ok := state.Repositories[repo.ID]; ok {
		if !reflect.DeepEqual(existing.Source, repo.Source) && existing.Generation > 0 {
			// Same ID with a different NFS source is a deliberate update; bump generation.
			repo.Generation = existing.Generation + 1
			repo.CreatedAt = existing.CreatedAt
		} else {
			repo.Generation = existing.Generation
			if repo.Generation == 0 {
				repo.Generation = 1
			}
			repo.CreatedAt = existing.CreatedAt
		}
	} else {
		if repo.Generation == 0 {
			repo.Generation = 1
		}
		repo.CreatedAt = now
	}
	repo.UpdatedAt = now
	state.Repositories[repo.ID] = repo
	if err := savePoolBackupRepositories(s, state); err != nil {
		return protocol.BackupRepository{}, err
	}
	return repo, nil
}

// ListPoolBackupRepositories returns the master's durable destination list.
func ListPoolBackupRepositories(s *Store) ([]protocol.BackupRepository, error) {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return nil, ErrBackupUnavailable
	}
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.BackupRepository, 0, len(state.Repositories))
	for _, repo := range state.Repositories {
		out = append(out, repo)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetPoolBackupRepository returns one durable destination by ID.
func GetPoolBackupRepository(s *Store, id string) (protocol.BackupRepository, error) {
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	repo, ok := state.Repositories[id]
	if ok && repo.PoolID == s.State.PoolID {
		return repo, nil
	}
	return protocol.BackupRepository{}, ErrBackupRepoNotFound
}

func validatedPublishedBackupManifest(ctx context.Context, repo protocol.BackupRepository, pointID string) (protocol.BackupManifest, string, error) {
	if err := verifyBackupRepository(ctx, repo); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	manifest, digest, err := vmhost.ReadBackupManifest(filepath.Join(repo.MountPath, pointID))
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if manifest.RecoveryPointID != pointID || manifest.RepositoryID != repo.ID || manifest.PoolID != repo.PoolID {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	}
	if err := vmhost.VerifyPublishedBackupRecoveryPoint(filepath.Join(repo.MountPath, pointID), manifest); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	return manifest, digest, nil
}

func validatedBackupManifest(ctx context.Context, repo protocol.BackupRepository, pointID string) (protocol.BackupManifest, string, error) {
	manifest, digest, err := validatedPublishedBackupManifest(ctx, repo, pointID)
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if err := vmhost.ValidateBackupFirmwareForRestore(manifest); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	return manifest, digest, nil
}

func restoreCatalogManifestSHA(s *Store, repositoryID, pointID string) (string, error) {
	point, err := getCatalogPoint(s, repositoryID, pointID)
	if err != nil {
		return "", err
	}
	if point.ID != pointID || point.RepositoryID != repositoryID || point.PoolID != s.State.PoolID {
		return "", ErrBackupConflict
	}
	return point.ManifestSHA256, nil
}

func validatedCatalogBackupManifest(ctx context.Context, s *Store, repo protocol.BackupRepository, pointID, expectedSHA string) (protocol.BackupManifest, string, error) {
	if expectedSHA == "" {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	}
	manifest, digest, err := validatedBackupManifest(ctx, repo, pointID)
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	currentSHA, err := restoreCatalogManifestSHA(s, repo.ID, pointID)
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if repo.PoolID != s.State.PoolID || currentSHA != expectedSHA || digest != expectedSHA {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	}
	return manifest, digest, nil
}

func savePeerRestoreCatalogPoint(s *Store, point protocol.BackupRecoveryPoint) error {
	existing, err := getCatalogPoint(s, point.RepositoryID, point.ID)
	if err == nil {
		if existing.PoolID != point.PoolID || existing.ManifestSHA256 != point.ManifestSHA256 {
			return ErrBackupConflict
		}
		return nil
	}
	if !errors.Is(err, ErrBackupNotFound) {
		return err
	}
	return savePoolBackupCatalogEntry(s, point)
}

// TestPoolBackupRepositoryAccess checks the mounted path and owner marker for a destination.
func TestPoolBackupRepositoryAccess(s *Store, id string) (protocol.BackupRepository, error) {
	repo, err := GetPoolBackupRepository(s, id)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	repo, err = checkBackupRepositoryAccess(context.Background(), repo)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	return repo, savePoolBackupRepositoryAccess(s, repo)
}

func checkBackupRepositoryAccess(ctx context.Context, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	if repo.MountPath == "" {
		return protocol.BackupRepository{}, errors.New("backup repository has no mount path")
	}
	if err := verifyBackupRepository(ctx, repo); err != nil {
		repo.Access = protocol.StorageAccess{Status: "failed", Error: err.Error(), ObservedAt: time.Now().UTC()}
	} else {
		repo.Access = protocol.StorageAccess{Status: "ok", Readable: true, Writable: true, ObservedAt: time.Now().UTC()}
	}
	return repo, nil
}

func savePoolBackupRepositoryAccess(s *Store, repo protocol.BackupRepository) error {
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	if current, ok := state.Repositories[repo.ID]; !ok || current.PoolID != repo.PoolID || current.Generation != repo.Generation || !reflect.DeepEqual(current.Source, repo.Source) {
		return ErrBackupConflict
	}
	state.Repositories[repo.ID] = repo
	return savePoolBackupRepositories(s, state)
}

func savePoolBackupCatalogEntry(s *Store, point protocol.BackupRecoveryPoint) error {
	if err := point.Valid(); err != nil {
		return err
	}
	if err := checkBackupCatalogPointCapacity(s, point); err != nil {
		return err
	}
	if err := os.MkdirAll(poolBackupCatalogPath(s), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(point)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(filepath.Join(poolBackupCatalogPath(s), point.ID+".json"), b, 0600)
}

func checkBackupCatalogPointCapacity(s *Store, point protocol.BackupRecoveryPoint) error {
	return checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		for i := range auth.Catalog {
			if auth.Catalog[i].ID == point.ID {
				auth.Catalog[i] = point
				return
			}
		}
		auth.Catalog = append(auth.Catalog, point)
	})
}

func loadPoolBackupCatalog(s *Store) ([]protocol.BackupRecoveryPoint, error) {
	dir := poolBackupCatalogPath(s)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]protocol.BackupRecoveryPoint, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var point protocol.BackupRecoveryPoint
		if err := json.Unmarshal(raw, &point); err != nil || point.Valid() != nil {
			return nil, errors.New("backup catalog entry is corrupt")
		}
		out = append(out, point)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CapturedAt.Equal(out[j].CapturedAt) {
			return out[i].CapturedAt.After(out[j].CapturedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func savePoolBackupRun(s *Store, run protocol.BackupRun) error {
	if err := run.Valid(); err != nil {
		return err
	}
	if err := checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		for i := range auth.History {
			if auth.History[i].OperationID == run.OperationID {
				auth.History[i] = run
				return
			}
		}
		auth.History = append(auth.History, run)
	}); err != nil {
		return err
	}
	if err := os.MkdirAll(poolBackupRunsPath(s), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(run)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(filepath.Join(poolBackupRunsPath(s), run.OperationID+".json"), b, 0600)
}

func loadPoolBackupRun(s *Store, operationID string) (protocol.BackupRun, error) {
	raw, err := os.ReadFile(filepath.Join(poolBackupRunsPath(s), operationID+".json"))
	if err != nil {
		return protocol.BackupRun{}, err
	}
	var run protocol.BackupRun
	if err := json.Unmarshal(raw, &run); err != nil || run.Valid() != nil {
		return protocol.BackupRun{}, errors.New("backup run record is corrupt")
	}
	return run, nil
}

func listPoolBackupRuns(s *Store) ([]protocol.BackupRun, error) {
	dir := poolBackupRunsPath(s)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]protocol.BackupRun, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		run, err := loadPoolBackupRun(s, stringsTrimSuffixJSON(entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].OperationID < out[j].OperationID
	})
	return out, nil
}

func stringsTrimSuffixJSON(name string) string {
	return name[:len(name)-len(".json")]
}

// ReservePoolBackupRun persists the durable master run record before native effects.
func ReservePoolBackupRun(s *Store, run protocol.BackupRun) (protocol.BackupRun, error) {
	if err := run.Valid(); err != nil {
		return protocol.BackupRun{}, ErrBackupInvalid
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	membership, err := Membership(s)
	if err != nil || membership.PoolID != run.PoolID {
		return protocol.BackupRun{}, ErrBackupUnavailable
	}
	if membership.MasterHostID == s.State.HostID {
		if err := requirePoolAuthorityWritable(s); err != nil {
			return protocol.BackupRun{}, err
		}
	}
	if existing, err := loadPoolBackupRun(s, run.OperationID); err == nil {
		kindOK := existing.Kind == run.Kind ||
			((existing.Kind == protocol.BackupKindScheduled || existing.Kind == protocol.BackupKindPolicyRunNow) && run.Kind == protocol.BackupKindBackupNow)
		if !kindOK || existing.RepositoryID != run.RepositoryID || existing.VMID != run.VMID ||
			existing.RecoveryPointID != run.RecoveryPointID || existing.NewVMID != run.NewVMID ||
			existing.IntentSHA256 != run.IntentSHA256 || existing.ManifestSHA256 != run.ManifestSHA256 ||
			(existing.HostID != "" && run.HostID != "" && existing.HostID != run.HostID) {
			return protocol.BackupRun{}, ErrBackupConflict
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return protocol.BackupRun{}, err
	}
	run.Stage = "reserved"
	run.Status = "pending"
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.UpdatedAt = now
	if err := savePoolBackupRun(s, run); err != nil {
		return protocol.BackupRun{}, err
	}
	return run, nil
}

// SettlePoolBackupRun commits terminal evidence and, on successful backup-now, catalogs the point.
func SettlePoolBackupRun(s *Store, run protocol.BackupRun, point *protocol.BackupRecoveryPoint) error {
	run.Error = resultError(run.Error)
	if err := run.Valid(); err != nil {
		return ErrBackupInvalid
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	membership, err := Membership(s)
	if err != nil || membership.PoolID != run.PoolID {
		return ErrBackupUnavailable
	}
	if membership.MasterHostID == s.State.HostID {
		if err := requirePoolAuthorityWritable(s); err != nil {
			return err
		}
	}
	existing, err := loadPoolBackupRun(s, run.OperationID)
	if err != nil {
		return err
	}
	kindOK := existing.Kind == run.Kind ||
		((existing.Kind == protocol.BackupKindScheduled || existing.Kind == protocol.BackupKindPolicyRunNow) &&
			run.Kind == protocol.BackupKindBackupNow)
	if !kindOK || existing.RepositoryID != run.RepositoryID || existing.VMID != run.VMID ||
		existing.RecoveryPointID != run.RecoveryPointID || existing.NewVMID != run.NewVMID ||
		(existing.HostID != "" && run.HostID != "" && existing.HostID != run.HostID) {
		return ErrBackupConflict
	}
	if existing.IntentSHA256 != "" && run.IntentSHA256 != "" && existing.IntentSHA256 != run.IntentSHA256 {
		return ErrBackupConflict
	}
	if run.ManifestSHA256 != "" && existing.ManifestSHA256 != run.ManifestSHA256 {
		return ErrBackupConflict
	}
	if existing.Status == "succeeded" || existing.Status == "failed" {
		if existing.Status == run.Status && existing.Stage == run.Stage && existing.Error == run.Error {
			return nil
		}
		return ErrBackupConflict
	}
	run.PolicyID = existing.PolicyID
	run.ManifestSHA256 = existing.ManifestSHA256
	if run.HostID == "" {
		run.HostID = existing.HostID
	}
	run.Retention = existing.Retention
	run.CaptureMode = existing.CaptureMode
	run.UpdatedAt = time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = existing.CreatedAt
	}
	run.ScheduleID = existing.ScheduleID
	run.OccurrenceID = existing.OccurrenceID
	run.PolicyRevision = existing.PolicyRevision
	run.OccurrenceAt = existing.OccurrenceAt
	// Preserve reserved kind (scheduled / run-now) across native settlement.
	if existing.Kind == protocol.BackupKindScheduled || existing.Kind == protocol.BackupKindPolicyRunNow {
		run.Kind = existing.Kind
	}
	if run.Status == "succeeded" && (run.Kind == protocol.BackupKindBackupNow || run.Kind == protocol.BackupKindScheduled || run.Kind == protocol.BackupKindPolicyRunNow) {
		if point == nil || point.Valid() != nil || point.ID != run.RecoveryPointID ||
			point.RepositoryID != run.RepositoryID || point.PoolID != run.PoolID || point.SourceVMID != run.VMID {
			return errors.New("successful backup settlement requires a valid catalog point")
		}
	}
	if err := checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		for i := range auth.History {
			if auth.History[i].OperationID == run.OperationID {
				auth.History[i] = run
				break
			}
		}
		if point != nil && run.Status == "succeeded" && (run.Kind == protocol.BackupKindBackupNow || run.Kind == protocol.BackupKindScheduled || run.Kind == protocol.BackupKindPolicyRunNow) {
			for i := range auth.Catalog {
				if auth.Catalog[i].ID == point.ID {
					auth.Catalog[i] = *point
					return
				}
			}
			auth.Catalog = append(auth.Catalog, *point)
		}
	}); err != nil {
		return err
	}
	if run.Status == "succeeded" && (run.Kind == protocol.BackupKindBackupNow || run.Kind == protocol.BackupKindScheduled || run.Kind == protocol.BackupKindPolicyRunNow) {
		if err := savePoolBackupCatalogEntry(s, *point); err != nil {
			return err
		}
		if run.Retention != nil {
			if err := prunePublishedBackupPayloads(s, run.RepositoryID, run.VMID, *run.Retention); err != nil {
				return err
			}
		}
	}
	return savePoolBackupRun(s, run)
}

func prunePublishedBackupPayloads(s *Store, repositoryID, vmID string, retention protocol.BackupRetention) error {
	repo, err := GetPoolBackupRepository(s, repositoryID)
	if err != nil {
		return err
	}
	if err := verifyBackupRepository(context.Background(), repo); err != nil {
		return err
	}
	pins, err := ActiveBackupRestorePins(s)
	if err != nil {
		return err
	}
	all, err := loadPoolBackupCatalog(s)
	if err != nil {
		return err
	}
	var eligible []protocol.BackupRecoveryPoint
	for _, point := range all {
		if point.RepositoryID == repositoryID && point.SourceVMID == vmID {
			eligible = append(eligible, point)
		}
	}
	_, prune := protocol.RetentionEligiblePrune(eligible, vmID, retention, time.Now().UTC(), pins)
	for _, point := range prune {
		pointDir := filepath.Join(repo.MountPath, point.ID)
		if vmhost.ReadBackupProtectMarker(pointDir) {
			continue
		}
		if _, err := os.Stat(filepath.Join(pointDir, ".nex-protected")); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.RemoveAll(pointDir); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(poolBackupCatalogPath(s), point.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func backupIntentSHA256(command protocol.Command) (string, error) {
	payload := struct {
		Kind           string                          `json:"kind"`
		VMID           string                          `json:"vm_id,omitempty"`
		Backup         *protocol.BackupNowRequest      `json:"backup,omitempty"`
		Restore        *protocol.RestoreAsNewRequest   `json:"restore,omitempty"`
		RestoreReplace *protocol.RestoreReplaceRequest `json:"restore_replace,omitempty"`
		TestRestore    *protocol.TestRestoreRequest    `json:"test_restore,omitempty"`
	}{Kind: command.Kind}
	if command.VM != nil {
		payload.VMID = command.VM.ID
	}
	payload.Backup, payload.Restore = command.Backup, command.Restore
	payload.RestoreReplace, payload.TestRestore = command.RestoreReplace, command.TestRestore
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// PoolBackupInventory reports master-owned backup state for fresh Controller adoption.
func PoolBackupInventory(s *Store) (repos []protocol.BackupRepository, catalog []protocol.BackupRecoveryPoint, runs []protocol.BackupRun, policies []protocol.BackupPolicy, reported bool, err error) {
	membership, err := Membership(s)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	if membership.MasterHostID != s.State.HostID {
		return nil, nil, nil, nil, false, nil
	}
	repos, err = ListPoolBackupRepositories(s)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	catalog, err = loadPoolBackupCatalog(s)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	runs, err = listPoolBackupRuns(s)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	policies, err = ListPoolBackupPolicies(s)
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	return repos, catalog, runs, policies, true, nil
}
