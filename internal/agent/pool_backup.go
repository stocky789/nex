// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

var poolBackupAuthorityMu sync.Mutex
var backupHealthActive sync.Map // one filesystem probe per repository and Store, including timed-out probes
var backupMemberAccessMu sync.Mutex
var backupMemberAccess = map[*Store]map[string]map[string]protocol.PoolStorageAccess{}

var verifyBackupRepository = vmhost.VerifyBackupRepository
var verifyBackupRepositoryRead = vmhost.VerifyBackupRepositoryRead
var prepareBackupRepositoryNative = vmhost.PrepareBackupRepository
var prepareBackupRepositorySMBNative = vmhost.PrepareBackupRepositorySMB
var prepareBackupRepositorySFTPNative = prepareSFTPBackupRepository

const (
	poolBackupRepositoriesFile = "pool-backup-repositories.json"
	poolBackupCatalogDir       = "pool-backup-catalog"
	poolBackupRunsDir          = "pool-backup-runs"
	poolBackupReceiptsDir      = "pool-backup-receipts"
	backupHealthInterval       = time.Minute
	backupHealthFreshness      = 2 * backupHealthInterval
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

var writeBackupMetadataReceipt = vmhost.AtomicWrite

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
		return persistenceError{err}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return persistenceError{err}
	}
	if err := writeBackupMetadataReceipt(filepath.Join(poolBackupReceiptsPath(s), receipt.OperationID+".json"), raw, 0600); err != nil {
		return persistenceError{err}
	}
	return nil
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
	for id, repo := range state.Repositories {
		repo.MemberAccess = nil
		state.Repositories[id] = repo
	}
	if err := checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		auth.Repositories = auth.Repositories[:0]
		for _, repo := range state.Repositories {
			repo.Access = protocol.StorageAccess{}
			repo.MemberAccess = nil
			repo.Health = protocol.BackupRepositoryHealth{}
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

func prepareBackupRepository(ctx context.Context, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	if err := repo.Valid(); err != nil || repo.Kind != protocol.BackupRepoKindNFS {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	if repo.MountPath != "" || repo.Identity != nil {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	return prepareBackupRepositoryNative(ctx, repo)
}

func prepareBackupRepositoryForRegistration(ctx context.Context, s *Store, repo protocol.BackupRepository, operationID string) (protocol.BackupRepository, error) {
	// ponytail: one registration holds this lock through its bounded probe; split it if transfer latency becomes a problem.
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID || membership.PoolID != repo.PoolID {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return protocol.BackupRepository{}, err
	}
	if repo.Kind == protocol.BackupRepoKindNFS && (repo.Valid() != nil || repo.MountPath != "" || repo.Identity != nil) {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	if existing, err := GetPoolBackupRepository(s, repo.ID); err == nil {
		if unsafeBackupRepositoryReplacement(existing, repo) {
			return protocol.BackupRepository{}, ErrBackupUnavailable
		}
		if repo.Kind == protocol.BackupRepoKindNFS {
			if existing.Identity == nil || existing.MountPath == "" {
				return protocol.BackupRepository{}, ErrBackupUnavailable
			}
			repo.Identity, repo.MountPath = existing.Identity, existing.MountPath
			return prepareBackupRepositoryNative(ctx, repo)
		}
	} else if !errors.Is(err, ErrBackupRepoNotFound) {
		return protocol.BackupRepository{}, err
	}
	if repo.Kind == protocol.BackupRepoKindNFS {
		return prepareBackupRepository(ctx, repo)
	}
	if repo.Kind == protocol.BackupRepoKindSFTP {
		if repo.Valid() != nil || repo.MountPath != "" || repo.Identity != nil || repo.SMBIdentity != nil || repo.RemoteIdentity != nil || repo.CredentialRef == "" {
			return protocol.BackupRepository{}, ErrBackupInvalid
		}
		credentials, err := ResolveBackupCredentialForRegistration(s, repo, operationID)
		if err != nil {
			return protocol.BackupRepository{}, err
		}
		return prepareBackupRepositorySFTPNative(ctx, repo, s.dir, credentials)
	}
	if repo.Kind == protocol.BackupRepoKindS3 {
		if repo.Valid() != nil || repo.MountPath != "" || repo.Identity != nil || repo.SMBIdentity != nil || repo.RemoteIdentity != nil || repo.CredentialRef == "" {
			return protocol.BackupRepository{}, ErrBackupInvalid
		}
		credentials, err := ResolveBackupCredentialForRegistration(s, repo, operationID)
		if err != nil {
			return protocol.BackupRepository{}, err
		}
		return prepareS3BackupRepository(ctx, s, repo, credentials)
	}
	if repo.Kind != protocol.BackupRepoKindSMB3 || repo.Valid() != nil || repo.MountPath != "" || repo.Identity != nil || repo.SMBIdentity != nil {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	credentials, err := ResolveBackupCredentialForRegistration(s, repo, operationID)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	return prepareBackupRepositorySMBNative(ctx, repo, s.dir, credentials)
}

func prepareBackupRepositoryForAdoption(ctx context.Context, s *Store, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	if repo.Kind == protocol.BackupRepoKindNFS {
		if repo.Valid() != nil || repo.Identity == nil || repo.MountPath != "" {
			return protocol.BackupRepository{}, ErrBackupInvalid
		}
		return prepareBackupRepositoryNative(ctx, repo)
	}
	if repo.Kind == protocol.BackupRepoKindSFTP {
		if repo.Valid() != nil || repo.MountPath != "" || repo.Identity != nil || repo.SMBIdentity != nil || repo.RemoteIdentity == nil || repo.CredentialRef == "" {
			return protocol.BackupRepository{}, ErrBackupInvalid
		}
		credentials, err := ResolveBackupCredentialForAdoption(s, repo)
		if err != nil {
			return protocol.BackupRepository{}, err
		}
		return prepareBackupRepositorySFTPNative(ctx, repo, s.dir, credentials)
	}
	if repo.Kind == protocol.BackupRepoKindS3 {
		if repo.Valid() != nil || repo.MountPath != "" || repo.Identity != nil || repo.SMBIdentity != nil || repo.RemoteIdentity == nil || repo.CredentialRef == "" {
			return protocol.BackupRepository{}, ErrBackupInvalid
		}
		credentials, err := ResolveBackupCredentialForAdoption(s, repo)
		if err != nil {
			return protocol.BackupRepository{}, err
		}
		return prepareS3BackupRepository(ctx, s, repo, credentials)
	}
	if repo.Kind != protocol.BackupRepoKindSMB3 || repo.Valid() != nil || repo.MountPath != "" || repo.Identity != nil || repo.SMBIdentity != nil {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	credentials, err := ResolveBackupCredentialForAdoption(s, repo)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	return prepareBackupRepositorySMBNative(ctx, repo, s.dir, credentials)
}

func upsertPoolBackupRepositoryPrepared(s *Store, repo, prepared protocol.BackupRepository) (protocol.BackupRepository, error) {
	if !preparedBackupRepositoryMatches(repo, prepared) {
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
	repo.Health = protocol.BackupRepositoryHealth{}
	repo.LastHealthTransition = nil
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	if existing, ok := state.Repositories[repo.ID]; ok && (unsafeBackupRepositoryReplacement(existing, repo) ||
		repo.Kind == protocol.BackupRepoKindNFS && !reflect.DeepEqual(existing.Identity, repo.Identity)) {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	if existing, ok := state.Repositories[repo.ID]; ok && repo.Kind == protocol.BackupRepoKindSMB3 &&
		!samePeerSMBIdentity(existing.SMBIdentity, repo.SMBIdentity) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	now := time.Now().UTC()
	if existing, ok := state.Repositories[repo.ID]; ok {
		if (existing.Kind != repo.Kind || existing.CredentialRef != repo.CredentialRef ||
			!reflect.DeepEqual(existing.Source, repo.Source) || !reflect.DeepEqual(existing.SMBSource, repo.SMBSource) ||
			!reflect.DeepEqual(existing.S3Source, repo.S3Source) || !reflect.DeepEqual(existing.SFTPSource, repo.SFTPSource)) && existing.Generation > 0 {
			repo.Generation = existing.Generation + 1
			repo.CreatedAt = existing.CreatedAt
		} else {
			repo.Generation = existing.Generation
			if repo.Generation == 0 {
				repo.Generation = 1
			}
			repo.CreatedAt = existing.CreatedAt
		}
		if repo.Generation == existing.Generation && reflect.DeepEqual(repo.Identity, existing.Identity) &&
			reflect.DeepEqual(repo.SMBIdentity, existing.SMBIdentity) && reflect.DeepEqual(repo.RemoteIdentity, existing.RemoteIdentity) && repo.MountPath == existing.MountPath {
			repo.Health = existing.Health
			repo.LastHealthTransition = existing.LastHealthTransition
		}
	} else {
		if repo.Generation == 0 {
			repo.Generation = 1
		}
		repo.CreatedAt = now
	}
	repo.UpdatedAt = now
	if backupCredentialKind(repo.Kind) {
		if _, err := localBackupCredential(s, repo); err != nil {
			return protocol.BackupRepository{}, ErrBackupUnavailable
		}
	}
	state.Repositories[repo.ID] = repo
	if err := savePoolBackupRepositories(s, state); err != nil {
		return protocol.BackupRepository{}, err
	}
	if backupCredentialKind(repo.Kind) {
		if err := retireSupersededBackupCredentials(s, repo); err != nil {
			return protocol.BackupRepository{}, ErrBackupUnavailable
		}
		if projectBackupCredentialToMembers(context.Background(), s, membership, repo) == nil {
			if err := cleanupRetiredBackupSMBCredentials(s, repo); err != nil {
				return protocol.BackupRepository{}, ErrBackupUnavailable
			}
		}
	}
	return repo, nil
}

func unsafeBackupRepositoryReplacement(existing, requested protocol.BackupRepository) bool {
	return existing.Kind != requested.Kind || existing.CredentialRef != requested.CredentialRef ||
		!reflect.DeepEqual(existing.Source, requested.Source) ||
		!reflect.DeepEqual(existing.SMBSource, requested.SMBSource) || !reflect.DeepEqual(existing.S3Source, requested.S3Source) ||
		!reflect.DeepEqual(existing.SFTPSource, requested.SFTPSource)
}

func projectBackupCredentialToMembers(ctx context.Context, s *Store, membership protocol.PoolMembership, repo protocol.BackupRepository) error {
	unavailable := false
	for _, member := range membership.Members {
		if member.HostID == s.State.HostID || member.Status != "ready" {
			continue
		}
		if member.PeerOrigin == "" {
			unavailable = true
			continue
		}
		if err := deliverPeerBackupCredential(ctx, s, member, repo); err != nil {
			unavailable = true
		}
	}
	if unavailable {
		return ErrBackupUnavailable
	}
	return nil
}

func retireSupersededBackupCredentials(s *Store, repo protocol.BackupRepository) error {
	current, err := localBackupCredential(s, repo)
	if err != nil {
		return err
	}
	dir, err := backupCredentialDir(s)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := readClusterSecret(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		var stored storedBackupCredential
		if json.Unmarshal(raw, &stored) != nil {
			return errors.New("backup credential receipt is corrupt")
		}
		r := stored.Receipt
		if r.PoolID != repo.PoolID || r.HostID != s.State.HostID || r.RepositoryID != repo.ID ||
			r.Reference == repo.CredentialRef || !r.CreatedAt.Before(current.Receipt.CreatedAt) {
			continue
		}
		old := protocol.BackupRepository{ID: r.RepositoryID, PoolID: r.PoolID, Kind: r.Kind, CredentialRef: r.Reference}
		if err := RetireBackupCredential(s, old, repo); err != nil {
			return err
		}
	}
	return nil
}

func cleanupRetiredBackupSMBCredentials(s *Store, repo protocol.BackupRepository) error {
	projection, err := localBackupCredential(s, repo)
	if err != nil {
		return err
	}
	for _, retired := range projection.Retired {
		if retired.Kind == protocol.BackupRepoKindSMB3 {
			if err := vmhost.RetireBackupRepositorySMBCredential(s.dir, retired.Reference); err != nil {
				return err
			}
		}
	}
	return nil
}

func preparedBackupRepositoryMatches(repo, prepared protocol.BackupRepository) bool {
	if repo.Valid() != nil || prepared.Valid() != nil || repo.Kind != prepared.Kind || repo.ID != prepared.ID ||
		repo.PoolID != prepared.PoolID || repo.Name != prepared.Name || repo.CredentialRef != prepared.CredentialRef ||
		!reflect.DeepEqual(repo.Source, prepared.Source) || !reflect.DeepEqual(repo.SMBSource, prepared.SMBSource) ||
		!reflect.DeepEqual(repo.S3Source, prepared.S3Source) || !reflect.DeepEqual(repo.SFTPSource, prepared.SFTPSource) || repo.MountPath != "" || repo.SMBIdentity != nil {
		return false
	}
	if repo.Kind == protocol.BackupRepoKindS3 || repo.Kind == protocol.BackupRepoKindSFTP {
		return prepared.MountPath == "" && prepared.Identity == nil && prepared.SMBIdentity == nil &&
			prepared.RemoteIdentity != nil && (repo.RemoteIdentity == nil || reflect.DeepEqual(repo.RemoteIdentity, prepared.RemoteIdentity))
	}
	if prepared.MountPath == "" {
		return false
	}
	switch repo.Kind {
	case protocol.BackupRepoKindNFS:
		return prepared.Identity != nil && (repo.Identity == nil || reflect.DeepEqual(repo.Identity, prepared.Identity)) && prepared.SMBIdentity == nil
	case protocol.BackupRepoKindSMB3:
		return prepared.Identity == nil && prepared.SMBIdentity != nil && prepared.SMBIdentity.MarkerID != "" &&
			prepared.SMBIdentity.Server == repo.SMBSource.Server && prepared.SMBIdentity.Share == repo.SMBSource.Share
	default:
		return false
	}
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
		if repo.Health.Status == "" || time.Since(repo.Health.ObservedAt) > backupHealthFreshness {
			repo.Health.Status = "unknown"
		}
		repo.MemberAccess = poolBackupMemberAccess(s, repo)
		out = append(out, repo)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func poolBackupMemberAccess(s *Store, repo protocol.BackupRepository) []protocol.PoolStorageAccess {
	members := readyPoolMembers(s)
	backupMemberAccessMu.Lock()
	defer backupMemberAccessMu.Unlock()
	rows := make([]protocol.PoolStorageAccess, 0, len(members))
	for _, hostID := range members {
		access := backupMemberAccess[s][repo.ID][hostID]
		if access.Generation != repo.Generation || access.ObservedAt.IsZero() || access.ObservedAt.After(time.Now()) || time.Since(access.ObservedAt) > backupHealthFreshness {
			access = protocol.PoolStorageAccess{RepositoryID: repo.ID, HostID: hostID, Generation: repo.Generation, Error: "awaiting member probe"}
		}
		rows = append(rows, access)
	}
	return rows
}

func sameBackupProbeRepository(expected, local protocol.BackupRepository) bool {
	return expected.ID == local.ID && expected.PoolID == local.PoolID && expected.Kind == local.Kind &&
		expected.Generation == local.Generation && expected.CredentialRef == local.CredentialRef &&
		reflect.DeepEqual(expected.Source, local.Source) && reflect.DeepEqual(expected.SMBSource, local.SMBSource) &&
		reflect.DeepEqual(expected.S3Source, local.S3Source) && reflect.DeepEqual(expected.SFTPSource, local.SFTPSource) &&
		reflect.DeepEqual(expected.Identity, local.Identity) && (expected.Kind != protocol.BackupRepoKindSMB3 || samePeerSMBIdentity(expected.SMBIdentity, local.SMBIdentity)) &&
		reflect.DeepEqual(expected.RemoteIdentity, local.RemoteIdentity)
}

func probePeerBackupRepository(ctx context.Context, s *Store, expected protocol.BackupRepository) protocol.PoolStorageAccess {
	access := protocol.PoolStorageAccess{RepositoryID: expected.ID, HostID: s.State.HostID, Generation: expected.Generation, ObservedAt: time.Now().UTC()}
	local, err := GetPoolBackupRepository(s, expected.ID)
	missing := errors.Is(err, ErrBackupRepoNotFound)
	if missing {
		local, err = prepareBackupRepositoryForAdoption(ctx, s, portablePeerRepository(expected))
	}
	if err == nil && !sameBackupProbeRepository(expected, local) {
		err = ErrBackupConflict
	}
	if err == nil && missing {
		poolBackupAuthorityMu.Lock()
		state, loadErr := loadPoolBackupRepositories(s)
		if loadErr == nil {
			if current, exists := state.Repositories[expected.ID]; exists {
				if sameBackupProbeRepository(expected, current) {
					local = current
				} else {
					loadErr = ErrBackupConflict
				}
			} else {
				state.Repositories[expected.ID] = local
				loadErr = savePoolBackupRepositories(s, state)
			}
		}
		poolBackupAuthorityMu.Unlock()
		err = loadErr
	}
	if err == nil {
		err = probeBackupRepositoryForStore(ctx, s, local)
	}
	access.ObservedAt = time.Now().UTC()
	if err != nil {
		access.Error = "backup repository access probe failed"
	} else {
		access.Readable, access.Writable = true, true
	}
	return access
}

func probeBackupMember(ctx context.Context, s *Store, repo protocol.BackupRepository, member protocol.PoolMember, timeout time.Duration) protocol.PoolStorageAccess {
	access := protocol.PoolStorageAccess{RepositoryID: repo.ID, HostID: member.HostID, Generation: repo.Generation, ObservedAt: time.Now().UTC(), Error: "member backup repository probe unavailable"}
	client, err := PoolMemberPeerClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return access
	}
	defer client.CloseIdleConnections()
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	repo.MemberAccess = nil
	body, err := json.Marshal(repo)
	if err != nil {
		return access
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolBackupPeerPath+"/access", strings.NewReader(string(body)))
	if err != nil {
		return access
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return access
	}
	defer response.Body.Close()
	var observed protocol.PoolStorageAccess
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&observed) != nil ||
		observed.RepositoryID != repo.ID || observed.HostID != member.HostID || observed.Generation != repo.Generation || observed.ObservedAt.IsZero() {
		return access
	}
	observed.ObservedAt = time.Now().UTC()
	return observed
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
	return filesystemBackupStorage{repo: repo}.ReadPoint(ctx, pointID)
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
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	manifest, digest, err := backend.ReadPoint(ctx, pointID)
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if err := vmhost.ValidateBackupFirmwareForRestore(manifest); err != nil {
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
	repo, err = checkBackupRepositoryAccessForStore(context.Background(), s, repo)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	return repo, savePoolBackupRepositoryAccess(s, repo)
}

func checkBackupRepositoryAccessForStore(ctx context.Context, s *Store, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	repo.Health = protocol.BackupRepositoryHealth{}
	if err := probeBackupRepositoryForStore(ctx, s, repo); err != nil {
		repo.Access = protocol.StorageAccess{Status: "failed", Error: err.Error(), ObservedAt: time.Now().UTC()}
	} else {
		repo.Access = protocol.StorageAccess{Status: "ok", Readable: true, Writable: true, ObservedAt: time.Now().UTC()}
	}
	return repo, nil
}

func probeBackupRepositoryForStore(ctx context.Context, s *Store, repo protocol.BackupRepository) error {
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		return err
	}
	return backend.Probe(ctx)
}

func probePoolBackupRepositories(ctx context.Context, s *Store, timeout time.Duration) error {
	repos, err := ListPoolBackupRepositories(s)
	if errors.Is(err, ErrBackupUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if backupCredentialKind(repo.Kind) && requirePoolAuthorityWritable(s) == nil {
			if err := retireSupersededBackupCredentials(s, repo); err != nil {
				return ErrBackupUnavailable
			}
			if projectBackupCredentialToMembers(ctx, s, membership, repo) == nil {
				if err := cleanupRetiredBackupSMBCredentials(s, repo); err != nil {
					return ErrBackupUnavailable
				}
			}
		}
		for _, member := range poolConfirmationPeers(membership, s.State.HostID) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			access := probeBackupMember(ctx, s, repo, member, timeout)
			backupMemberAccessMu.Lock()
			if backupMemberAccess[s] == nil {
				backupMemberAccess[s] = map[string]map[string]protocol.PoolStorageAccess{}
			}
			if backupMemberAccess[s][repo.ID] == nil {
				backupMemberAccess[s][repo.ID] = map[string]protocol.PoolStorageAccess{}
			}
			backupMemberAccess[s][repo.ID][member.HostID] = access
			backupMemberAccessMu.Unlock()
		}
		key := struct {
			store *Store
			id    string
		}{s, repo.ID}
		if _, active := backupHealthActive.LoadOrStore(key, true); active {
			continue
		}
		work, cancel := context.WithTimeout(ctx, timeout)
		finished := make(chan error, 1)
		probeRepo := repo
		go func() {
			defer backupHealthActive.Delete(key)
			finished <- probeBackupRepositoryForStore(work, s, probeRepo)
		}()
		var probeErr error
		select {
		case probeErr = <-finished:
		case <-work.Done():
			probeErr = work.Err()
		}
		cancel()
		now := time.Now().UTC()
		repo.Health = protocol.BackupRepositoryHealth{Status: "online", ObservedAt: now}
		repo.Access = protocol.StorageAccess{Status: "ok", Readable: true, Writable: true, ObservedAt: now}
		if probeErr != nil {
			repo.Health.Status = "offline"
			repo.Health.Error = "backup repository health probe failed"
			repo.Access = protocol.StorageAccess{Status: "failed", Error: repo.Health.Error, ObservedAt: now}
		}
		if err := savePoolBackupRepositoryAccess(s, repo); err != nil && !errors.Is(err, ErrBackupConflict) && !errors.Is(err, ErrBackupUnavailable) {
			return err
		}
	}
	return nil
}

func savePoolBackupRepositoryAccess(s *Store, repo protocol.BackupRepository) error {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return ErrBackupUnavailable
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	current, ok := state.Repositories[repo.ID]
	if !ok || current.PoolID != repo.PoolID || current.Generation != repo.Generation || current.Kind != repo.Kind ||
		!reflect.DeepEqual(current.Source, repo.Source) || !reflect.DeepEqual(current.SMBSource, repo.SMBSource) ||
		!reflect.DeepEqual(current.S3Source, repo.S3Source) || !reflect.DeepEqual(current.SFTPSource, repo.SFTPSource) ||
		!reflect.DeepEqual(current.Identity, repo.Identity) ||
		!reflect.DeepEqual(current.SMBIdentity, repo.SMBIdentity) || !reflect.DeepEqual(current.RemoteIdentity, repo.RemoteIdentity) ||
		current.MountPath != repo.MountPath || current.CredentialRef != repo.CredentialRef {
		return ErrBackupConflict
	}
	if repo.Health.Status == "online" || repo.Health.Status == "offline" {
		if repo.Health.ObservedAt.Before(current.Health.ObservedAt) {
			return ErrBackupConflict
		}
		if current.Health.Status != repo.Health.Status && (repo.Health.Status == "offline" || current.Health.Status == "offline") {
			status := "recovered"
			if repo.Health.Status == "offline" {
				status = "offline"
			}
			current.LastHealthTransition = &protocol.BackupRepositoryHealthTransition{Status: status, At: repo.Health.ObservedAt, Error: repo.Health.Error}
		}
		current.Health = repo.Health
	}
	if repo.Access.Status != "" {
		if repo.Access.ObservedAt.Before(current.Access.ObservedAt) {
			return ErrBackupConflict
		}
		current.Access = repo.Access
	}
	state.Repositories[repo.ID] = current
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
			(existing.HostID == "" && run.HostID != "" && run.HostID != s.State.HostID &&
				(existing.Kind == protocol.BackupKindScheduled || existing.Kind == protocol.BackupKindPolicyRunNow)) ||
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
	return settlePoolBackupRun(context.Background(), s, run, point)
}

func settlePoolBackupRun(ctx context.Context, s *Store, run protocol.BackupRun, point *protocol.BackupRecoveryPoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
			if existing.Kind == protocol.BackupKindPolicyRunNow {
				return settlePolicyRunNowReceipt(s, existing)
			}
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
		if run.Retention != nil && membership.MasterHostID == s.State.HostID {
			if err := prunePublishedBackupPayloadsContext(ctx, s, run.RepositoryID, run.VMID, *run.Retention); err != nil {
				return err
			}
		}
	}
	if err := savePoolBackupRun(s, run); err != nil {
		return err
	}
	if run.Kind == protocol.BackupKindPolicyRunNow {
		return settlePolicyRunNowReceipt(s, run)
	}
	return nil
}

// Caller holds poolBackupAuthorityMu; the run is the durable execution result.
func settlePolicyRunNowReceipt(s *Store, run protocol.BackupRun) error {
	receipt, err := loadPoolBackupMetadataReceipt(s, run.OperationID)
	if errors.Is(err, os.ErrNotExist) {
		return nil // Controller-dispatched runs have no Core-local receipt.
	}
	if err != nil {
		return err
	}
	if receipt.Kind != protocol.BackupKindPolicyRunNow {
		return ErrBackupConflict
	}
	if receipt.Status == run.Status && receipt.Error == run.Error {
		return nil
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	receipt.Status, receipt.Error, receipt.ObservedAt = run.Status, run.Error, run.UpdatedAt
	return savePoolBackupMetadataReceipt(s, receipt)
}

func prunePublishedBackupPayloads(s *Store, repositoryID, vmID string, retention protocol.BackupRetention) error {
	return prunePublishedBackupPayloadsContext(context.Background(), s, repositoryID, vmID, retention)
}

func prunePublishedBackupPayloadsContext(parent context.Context, s *Store, repositoryID, vmID string, retention protocol.BackupRetention) error {
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return err
	}
	repo, err := GetPoolBackupRepository(s, repositoryID)
	if err != nil {
		return err
	}
	backend, err := openBackupRepositoryStorage(s, repo)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := backend.Verify(ctx); err != nil {
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
		protected, err := backend.Protected(ctx, point.ID)
		if err != nil {
			return err
		}
		if protected {
			continue
		}
		if err := backend.Delete(ctx, point.ID); err != nil {
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
