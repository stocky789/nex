// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

var poolBackupAuthorityMu sync.Mutex
var backupHealthActive sync.Map // one filesystem probe per repository and state directory, including timed-out probes
var backupMemberAccessMu sync.Mutex
var backupMemberAccess = map[string]map[string]map[string]protocol.PoolStorageAccess{} // by state directory

var verifyBackupRepository = vmhost.VerifyBackupRepository
var verifyBackupRepositoryRead = vmhost.VerifyBackupRepositoryRead
var prepareBackupRepositoryNative = vmhost.PrepareBackupRepository
var prepareBackupRepositorySMBNative = vmhost.PrepareBackupRepositorySMB
var detachBackupRepositoryNative = vmhost.DetachBackupRepository
var removeBackupSMBCredentialNative = vmhost.RemoveBackupSMBCredential
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
		receipt, err := loadPoolBackupMetadataReceipt(s, strings.TrimSuffix(entry.Name(), ".json"))
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
		if unsafeBackupRepositoryReplacement(s, existing, repo) {
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
	return prepareBackupRepositorySMBNative(ctx, repo, credentials)
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
	// An adopted SMB repository carries the pool's share identity, so preparation only
	// verifies that marker and never creates a new one on a different share.
	expected := repo.SMBIdentity
	repo.SMBIdentity = nil
	if repo.Identity != nil || !validPeerSMBIdentity(repo, expected) {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	credentials, err := ResolveBackupCredentialForAdoption(s, repo)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	repo.SMBIdentity = expected
	return prepareBackupRepositorySMBNative(ctx, repo, credentials)
}

func upsertPoolBackupRepositoryPrepared(s *Store, repo, prepared protocol.BackupRepository) (protocol.BackupRepository, error) {
	if !preparedBackupRepositoryMatches(repo, prepared) {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	poolBackupAuthorityMu.Lock()
	locked := true
	defer func() {
		if locked {
			poolBackupAuthorityMu.Unlock()
		}
	}()
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
	if existing, ok := state.Repositories[repo.ID]; ok && (unsafeBackupRepositoryReplacement(s, existing, repo) ||
		repo.Kind == protocol.BackupRepoKindNFS && !reflect.DeepEqual(existing.Identity, repo.Identity)) {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	if existing, ok := state.Repositories[repo.ID]; ok && repo.Kind == protocol.BackupRepoKindSMB3 &&
		!samePeerSMBIdentity(existing.SMBIdentity, repo.SMBIdentity) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	if existing, ok := state.Repositories[repo.ID]; ok && existing.RemoteIdentity != nil && !reflect.DeepEqual(existing.RemoteIdentity, repo.RemoteIdentity) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	now := time.Now().UTC()
	if existing, ok := state.Repositories[repo.ID]; ok {
		repo.CreatedAt = existing.CreatedAt
		if reflect.DeepEqual(repo.Identity, existing.Identity) &&
			reflect.DeepEqual(repo.SMBIdentity, existing.SMBIdentity) && reflect.DeepEqual(repo.RemoteIdentity, existing.RemoteIdentity) && repo.MountPath == existing.MountPath {
			repo.Health = existing.Health
			repo.LastHealthTransition = existing.LastHealthTransition
		}
	} else {
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
		// Member delivery is network I/O; never hold the pool backup authority across it.
		// A member that misses it is retried by the health loop.
		poolBackupAuthorityMu.Unlock()
		locked = false
		projectBackupCredentialToMembers(context.Background(), s, membership, repo)
	}
	if locked {
		poolBackupAuthorityMu.Unlock()
		locked = false
	}
	// Record real health now instead of "unknown" until the next periodic pass.
	probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repo = checkBackupRepositoryAccessForStore(probeCtx, s, repo)
	return repo, savePoolBackupRepositoryAccess(s, repo)
}

// A repository keeps its credential unless this host can no longer read it (for example the
// sealing key was lost); then registering the same destination again supplies a new one.
func unsafeBackupRepositoryReplacement(s *Store, existing, requested protocol.BackupRepository) bool {
	if existing.CredentialRef != requested.CredentialRef && (!backupCredentialKind(existing.Kind) || credentialReadable(s, existing)) {
		return true
	}
	return existing.Kind != requested.Kind ||
		!reflect.DeepEqual(existing.Source, requested.Source) ||
		!reflect.DeepEqual(existing.SMBSource, requested.SMBSource) || !reflect.DeepEqual(existing.S3Source, requested.S3Source) ||
		!reflect.DeepEqual(existing.SFTPSource, requested.SFTPSource)
}

func credentialReadable(s *Store, repo protocol.BackupRepository) bool {
	_, err := localBackupCredential(s, repo)
	return err == nil
}

// projectBackupCredentialToMembers returns the members that did not receive the credential.
func projectBackupCredentialToMembers(ctx context.Context, s *Store, membership protocol.PoolMembership, repo protocol.BackupRepository) map[string]error {
	failed := map[string]error{}
	for _, member := range membership.Members {
		if member.HostID == s.State.HostID || member.Status != "ready" {
			continue
		}
		err := ErrBackupUnavailable
		if member.PeerOrigin != "" {
			err = deliverPeerBackupCredential(ctx, s, member, repo)
		}
		if err != nil {
			log.Printf("backup repository %s credential not delivered to member %s: %v", repo.ID, member.HostID, err)
			failed[member.HostID] = err
		}
	}
	return failed
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
	clusterSecretMu.Lock()
	files, _, err := readBackupCredentials(s, dir)
	clusterSecretMu.Unlock()
	if err != nil {
		return err
	}
	for _, file := range files {
		r := file.stored.Receipt
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
		access := backupMemberAccess[s.dir][repo.ID][hostID]
		if access.ObservedAt.IsZero() || access.ObservedAt.After(time.Now()) || time.Since(access.ObservedAt) > backupHealthFreshness {
			access = protocol.PoolStorageAccess{RepositoryID: repo.ID, HostID: hostID, Error: "awaiting member probe"}
		}
		rows = append(rows, access)
	}
	return rows
}

func sameBackupProbeRepository(expected, local protocol.BackupRepository) bool {
	return expected.ID == local.ID && expected.PoolID == local.PoolID && expected.Kind == local.Kind &&
		expected.CredentialRef == local.CredentialRef &&
		reflect.DeepEqual(expected.Source, local.Source) && reflect.DeepEqual(expected.SMBSource, local.SMBSource) &&
		reflect.DeepEqual(expected.S3Source, local.S3Source) && reflect.DeepEqual(expected.SFTPSource, local.SFTPSource) &&
		reflect.DeepEqual(expected.Identity, local.Identity) && (expected.Kind != protocol.BackupRepoKindSMB3 || samePeerSMBIdentity(expected.SMBIdentity, local.SMBIdentity)) &&
		reflect.DeepEqual(expected.RemoteIdentity, local.RemoteIdentity)
}

func probePeerBackupRepository(ctx context.Context, s *Store, expected protocol.BackupRepository) protocol.PoolStorageAccess {
	access := protocol.PoolStorageAccess{RepositoryID: expected.ID, HostID: s.State.HostID, ObservedAt: time.Now().UTC()}
	local, err := GetPoolBackupRepository(s, expected.ID)
	missing := errors.Is(err, ErrBackupRepoNotFound)
	if missing {
		adopt := portablePeerRepository(expected)
		adopt.SMBIdentity = expected.SMBIdentity
		local, err = prepareBackupRepositoryForAdoption(ctx, s, adopt)
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
	access := protocol.PoolStorageAccess{RepositoryID: repo.ID, HostID: member.HostID, ObservedAt: time.Now().UTC(), Error: "member backup repository probe unavailable"}
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
		observed.RepositoryID != repo.ID || observed.HostID != member.HostID || observed.ObservedAt.IsZero() {
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
	// CopyArtifacts verifies every artifact digest, so this check reads only metadata.
	manifest, digest, err := backend.StatPoint(ctx, pointID)
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
	repo = checkBackupRepositoryAccessForStore(context.Background(), s, repo)
	return repo, savePoolBackupRepositoryAccess(s, repo)
}

func checkBackupRepositoryAccessForStore(ctx context.Context, s *Store, repo protocol.BackupRepository) protocol.BackupRepository {
	now := time.Now().UTC()
	repo.Health = protocol.BackupRepositoryHealth{Status: "online", ObservedAt: now}
	repo.Access = protocol.StorageAccess{Status: "ok", Readable: true, Writable: true, ObservedAt: now}
	if err := probeBackupRepositoryForStore(ctx, s, repo); err != nil {
		repo.Health.Status, repo.Health.Error = "offline", "backup repository health probe failed"
		repo.Access = protocol.StorageAccess{Status: "failed", Error: err.Error(), ObservedAt: now}
	}
	return repo
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
	listed := map[string]bool{}
	for _, repo := range repos {
		listed[repo.ID] = true
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var undelivered map[string]error
		if backupCredentialKind(repo.Kind) && requirePoolAuthorityWritable(s) == nil {
			if err := retireSupersededBackupCredentials(s, repo); err != nil {
				// Only this repository is unusable; keep probing the others.
				now := time.Now().UTC()
				repo.Health = protocol.BackupRepositoryHealth{Status: "offline", ObservedAt: now,
					Error: "backup credential is unreadable on this host; register the repository again with its credentials"}
				repo.Access = protocol.StorageAccess{Status: "failed", Error: repo.Health.Error, ObservedAt: now}
				if err := savePoolBackupRepositoryAccess(s, repo); err != nil && !errors.Is(err, ErrBackupConflict) && !errors.Is(err, ErrBackupUnavailable) {
					return err
				}
				continue
			}
			undelivered = projectBackupCredentialToMembers(ctx, s, membership, repo)
		}
		// Each pass replaces the repository's rows, so ejected members drop out.
		rows := map[string]protocol.PoolStorageAccess{}
		for _, member := range poolConfirmationPeers(membership, s.State.HostID) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			row := probeBackupMember(ctx, s, repo, member, timeout)
			if err := undelivered[member.HostID]; err != nil && !row.Readable {
				row.Error = "backup credential not delivered: " + err.Error()
			}
			rows[member.HostID] = row
		}
		backupMemberAccessMu.Lock()
		if backupMemberAccess[s.dir] == nil {
			backupMemberAccess[s.dir] = map[string]map[string]protocol.PoolStorageAccess{}
		}
		backupMemberAccess[s.dir][repo.ID] = rows
		backupMemberAccessMu.Unlock()
		key := struct{ dir, id string }{s.dir, repo.ID}
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
	backupMemberAccessMu.Lock()
	for id := range backupMemberAccess[s.dir] {
		if !listed[id] {
			delete(backupMemberAccess[s.dir], id)
		}
	}
	backupMemberAccessMu.Unlock()
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
	if !ok || current.PoolID != repo.PoolID || current.Kind != repo.Kind ||
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
	return writePoolBackupCatalogEntry(s, point)
}

// writePoolBackupCatalogEntry writes a valid point whose capacity was checked.
func writePoolBackupCatalogEntry(s *Store, point protocol.BackupRecoveryPoint) error {
	if err := os.MkdirAll(poolBackupCatalogPath(s), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(point)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(filepath.Join(poolBackupCatalogPath(s), point.ID+".json"), b, 0600)
}

func checkBackupCatalogPointCapacity(s *Store, points ...protocol.BackupRecoveryPoint) error {
	return checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		index := make(map[string]int, len(auth.Catalog))
		for i, point := range auth.Catalog {
			index[point.ID] = i
		}
		for _, point := range points {
			if i, ok := index[point.ID]; ok {
				auth.Catalog[i] = point
			} else {
				index[point.ID] = len(auth.Catalog)
				auth.Catalog = append(auth.Catalog, point)
			}
		}
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
		run, err := loadPoolBackupRun(s, strings.TrimSuffix(entry.Name(), ".json"))
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
	retention, err := commitPoolBackupRun(ctx, s, run, point)
	if err == nil && retention != nil {
		applyBackupRetention(ctx, s, run, *retention)
	}
	return err
}

// applyBackupRetention runs once the run is durable, outside the pool backup
// lock, and never turns the settled backup into a failure.
func applyBackupRetention(ctx context.Context, s *Store, run protocol.BackupRun, retention protocol.BackupRetention) {
	if err := prunePublishedBackupPayloads(ctx, s, run.RepositoryID, run.VMID, run.PolicyID, retention, run.RecoveryPointID); err != nil {
		noteBackupRetentionFailure(s, run.OperationID, err)
	}
}

// commitPoolBackupRun records the settlement and returns the retention to apply.
func commitPoolBackupRun(ctx context.Context, s *Store, run protocol.BackupRun, point *protocol.BackupRecoveryPoint) (*protocol.BackupRetention, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	run.Error = resultError(run.Error)
	if err := run.Valid(); err != nil {
		return nil, ErrBackupInvalid
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	membership, err := Membership(s)
	if err != nil || membership.PoolID != run.PoolID {
		return nil, ErrBackupUnavailable
	}
	if membership.MasterHostID == s.State.HostID {
		if err := requirePoolAuthorityWritable(s); err != nil {
			return nil, err
		}
	}
	existing, err := loadPoolBackupRun(s, run.OperationID)
	if err != nil {
		return nil, err
	}
	kindOK := existing.Kind == run.Kind ||
		((existing.Kind == protocol.BackupKindScheduled || existing.Kind == protocol.BackupKindPolicyRunNow) &&
			run.Kind == protocol.BackupKindBackupNow)
	if !kindOK || existing.RepositoryID != run.RepositoryID || existing.VMID != run.VMID ||
		existing.RecoveryPointID != run.RecoveryPointID || existing.NewVMID != run.NewVMID ||
		(existing.HostID != "" && run.HostID != "" && existing.HostID != run.HostID) {
		return nil, ErrBackupConflict
	}
	if existing.IntentSHA256 != "" && run.IntentSHA256 != "" && existing.IntentSHA256 != run.IntentSHA256 {
		return nil, ErrBackupConflict
	}
	if run.ManifestSHA256 != "" && existing.ManifestSHA256 != run.ManifestSHA256 {
		return nil, ErrBackupConflict
	}
	if existing.Status == "succeeded" || existing.Status == "failed" {
		// A succeeded run's error can only be a later retention note.
		if existing.Status == run.Status && existing.Stage == run.Stage && (existing.Error == run.Error || existing.Status == "succeeded") {
			if existing.Kind == protocol.BackupKindPolicyRunNow {
				return nil, settlePolicyRunNowReceipt(s, existing)
			}
			return nil, nil
		}
		return nil, ErrBackupConflict
	}
	run.PolicyID = existing.PolicyID
	run.ManifestSHA256 = existing.ManifestSHA256
	if run.HostID == "" {
		run.HostID = existing.HostID
	}
	run.Retention = existing.Retention
	run.CaptureMode = existing.CaptureMode
	run.UpdatedAt = time.Now().UTC()
	run.CreatedAt = existing.CreatedAt
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
			return nil, errors.New("successful backup settlement requires a valid catalog point")
		}
		// Retention is scoped to the creating policy, so the catalog records it.
		p := *point
		p.PolicyID = run.PolicyID
		point = &p
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
		return nil, err
	}
	var retention *protocol.BackupRetention
	if run.Status == "succeeded" && (run.Kind == protocol.BackupKindBackupNow || run.Kind == protocol.BackupKindScheduled || run.Kind == protocol.BackupKindPolicyRunNow) {
		if err := savePoolBackupCatalogEntry(s, *point); err != nil {
			return nil, err
		}
		if membership.MasterHostID == s.State.HostID {
			retention = run.Retention
		}
	}
	if err := savePoolBackupRun(s, run); err != nil {
		return nil, err
	}
	if run.Kind == protocol.BackupKindPolicyRunNow {
		return retention, settlePolicyRunNowReceipt(s, run)
	}
	return retention, nil
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

// poolBackupPruneSeq counts retention deletions, so a catalog import can tell
// whether its unlocked scan may include a point retention has since removed.
var poolBackupPruneSeq atomic.Uint64

// poolBackupRetentionMu runs one retention pass at a time, so concurrent
// settlements never rank the catalog while another pass is deleting from it.
var poolBackupRetentionMu sync.Mutex

// poolBackupPruning holds the points retention is deleting outside the pool
// backup lock; restore pins and protection refuse them. Guarded by
// poolBackupAuthorityMu.
var poolBackupPruning = map[string]bool{}

// prunePublishedBackupPayloads applies one policy's retention to the points that
// policy created for one VM in a repository, ranked by capture time. Manual
// backups and other policies' points are never counted or removed. settling is the point being settled: its
// own settlement never removes it, even when the capturing host's clock was
// behind, but it never outranks a point captured after it. A point that cannot
// be removed is skipped so it never holds back the others; the joined error
// names each one.
func prunePublishedBackupPayloads(parent context.Context, s *Store, repositoryID, vmID, policyID string, retention protocol.BackupRetention, settling string) error {
	poolBackupRetentionMu.Lock()
	defer poolBackupRetentionMu.Unlock()
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
		if point.RepositoryID == repositoryID && policyID != "" && point.PolicyID == policyID {
			eligible = append(eligible, point)
		}
	}
	_, prune := protocol.RetentionEligiblePrune(eligible, vmID, retention, time.Now().UTC(), pins)
	prune = slices.DeleteFunc(prune, func(point protocol.BackupRecoveryPoint) bool { return point.ID == settling })
	if len(prune) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	if err := backend.Verify(ctx); err != nil {
		return err
	}
	var failed []error
	for _, point := range prune {
		if err := ctx.Err(); err != nil {
			failed = append(failed, err)
			break
		}
		if err := pruneRecoveryPoint(ctx, s, backend, repositoryID, point.ID); err != nil {
			failed = append(failed, fmt.Errorf("recovery point %s: %w", point.ID, err))
		}
	}
	return errors.Join(failed...)
}

// pruneRecoveryPoint deletes one point. Pins and protection are rechecked under
// the pool backup lock, where the point is also marked pruning so none can be
// taken while the repository I/O runs outside the lock.
func pruneRecoveryPoint(ctx context.Context, s *Store, backend backupRepositoryStorage, repositoryID, id string) error {
	due, err := func() (bool, error) {
		poolBackupAuthorityMu.Lock()
		defer poolBackupAuthorityMu.Unlock()
		pins, err := ActiveBackupRestorePins(s)
		if err != nil {
			return false, err
		}
		point, err := getCatalogPoint(s, repositoryID, id)
		if errors.Is(err, ErrBackupNotFound) || err == nil && (point.Protected || pins[id]) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		poolBackupPruning[id] = true
		return true, nil
	}()
	if err != nil || !due {
		return err
	}
	protected, err := backend.Protected(ctx, id)
	if err == nil && !protected {
		err = backend.Delete(ctx, id)
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	delete(poolBackupPruning, id)
	// Counted once the delete is over, so an import that scanned during it
	// scans again instead of cataloging a removed point.
	poolBackupPruneSeq.Add(1)
	if err != nil || protected {
		return err
	}
	if err := os.Remove(filepath.Join(poolBackupCatalogPath(s), id+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// noteBackupRetentionFailure records on the settled run that retention left
// expired points behind; the next settlement for the VM tries them again.
func noteBackupRetentionFailure(s *Store, operationID string, pruneErr error) {
	log.Printf("backup retention for %s left expired recovery points: %v", operationID, pruneErr)
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	run, err := loadPoolBackupRun(s, operationID)
	if err != nil || run.Status != "succeeded" || requirePoolAuthorityWritable(s) != nil {
		return
	}
	run.Error = resultError("retention could not remove every expired recovery point: " + pruneErr.Error())
	run.UpdatedAt = time.Now().UTC()
	err = checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		for i := range auth.History {
			if auth.History[i].OperationID == operationID {
				auth.History[i] = run
			}
		}
	})
	if err == nil {
		err = savePoolBackupRun(s, run)
	}
	if err != nil {
		log.Printf("backup retention note for %s was not saved: %v", operationID, err)
	}
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
