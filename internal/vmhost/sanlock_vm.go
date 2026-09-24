// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"

	"nex.local/nex/internal/protocol"
)

type SanlockVMRepository struct {
	RepositoryID string
	Lockspace    string
	Path         string
}

type SanlockVMLease struct {
	RepositoryID string
	Lockspace    string
	Key          string
	Path         string
	Offset       uint64
}

type SanlockJoinedRepository struct {
	RepositoryID string
	Lockspace    string
	HostID       uint64
	Generation   uint64
	HostState    string
}

type sanlockVMContextKey struct{}

type sanlockVMContext struct {
	PoolID                string
	Config                protocol.PoolStorageLeaseConfig
	LocalHostID           uint64
	HostIDs               []uint64
	Joined                []SanlockJoinedRepository
	InitializeVMResources bool
}

// WithPoolStorageLeaseConfig supplies the authority-signed lease configuration and this
// host's native join observations to one Core operation. It is not durable state.
func WithPoolStorageLeaseConfig(ctx context.Context, poolID string, config protocol.PoolStorageLeaseConfig, localHostID uint64, hostIDs []uint64, joined []SanlockJoinedRepository, initializeVMResources bool) context.Context {
	return context.WithValue(ctx, sanlockVMContextKey{}, sanlockVMContext{
		PoolID: poolID, Config: config, LocalHostID: localHostID,
		HostIDs: append([]uint64(nil), hostIDs...), Joined: append([]SanlockJoinedRepository(nil), joined...),
		InitializeVMResources: initializeVMResources,
	})
}

func SanlockVMResourceKey(vmID, repositoryID string) (string, error) {
	if !uuidPattern.MatchString(vmID) || !storageID.MatchString(repositoryID) {
		return "", errors.New("invalid sanlock VM resource identity")
	}
	digest := sha256.Sum256([]byte("nex-vm-repo-lease-v1\x00" + vmID + "\x00" + repositoryID))
	return "nexvm-" + hex.EncodeToString(digest[:20]), nil
}

// BuildSanlockVMLeases sorts by repository ID so every host asks libvirt to acquire
// multiple repository resources in the same order.
func BuildSanlockVMLeases(vmID string, repositories []SanlockVMRepository) ([]SanlockVMLease, error) {
	if !uuidPattern.MatchString(vmID) {
		return nil, errors.New("invalid sanlock VM identity")
	}
	repos := append([]SanlockVMRepository(nil), repositories...)
	slices.SortFunc(repos, func(a, b SanlockVMRepository) int {
		if a.RepositoryID < b.RepositoryID {
			return -1
		}
		if a.RepositoryID > b.RepositoryID {
			return 1
		}
		return 0
	})
	leases := make([]SanlockVMLease, 0, len(repos))
	previous := ""
	for _, repo := range repos {
		if repo.RepositoryID == previous || !sanlockName(repo.Lockspace) ||
			!filepath.IsAbs(repo.Path) || filepath.Clean(repo.Path) != repo.Path {
			return nil, errors.New("invalid sanlock VM repository")
		}
		if !storageID.MatchString(repo.RepositoryID) {
			return nil, errors.New("invalid sanlock VM repository identity")
		}
		path, err := SanlockVMResourcePath(repo.Path, vmID, repo.RepositoryID)
		if err != nil {
			return nil, err
		}
		key, err := SanlockVMResourceKey(vmID, repo.RepositoryID)
		if err != nil {
			return nil, err
		}
		leases = append(leases, SanlockVMLease{RepositoryID: repo.RepositoryID, Lockspace: repo.Lockspace, Key: key, Path: path})
		previous = repo.RepositoryID
	}
	return leases, nil
}

func managedSanlockVMLeases(ctx context.Context, js []managedJournal, vm protocol.VMCreateSpec) ([]managedLease, error) {
	leaseContext, enabled := ctx.Value(sanlockVMContextKey{}).(sanlockVMContext)
	if !enabled {
		return nil, nil
	}
	if err := leaseContext.Config.Valid(leaseContext.PoolID); err != nil || leaseContext.LocalHostID == 0 || leaseContext.Config.PreparedGeneration == 0 {
		return nil, errors.New("pool storage-lease configuration is invalid")
	}
	hostIDs := map[uint64]bool{}
	for _, id := range leaseContext.HostIDs {
		if id == 0 || id > protocol.PoolSanlockMaxHostID || hostIDs[id] {
			return nil, errors.New("pool sanlock host ID set is invalid")
		}
		hostIDs[id] = true
	}
	if !hostIDs[leaseContext.LocalHostID] {
		return nil, errors.New("pool sanlock host ID set omits this host")
	}
	configured := make(map[string]protocol.PoolStorageLeaseRepository, len(leaseContext.Config.Repositories))
	for _, repository := range leaseContext.Config.Repositories {
		configured[repository.RepositoryID] = repository
	}
	joined := make(map[string]SanlockJoinedRepository, len(leaseContext.Joined))
	for _, status := range leaseContext.Joined {
		if status.RepositoryID == "" || status.Lockspace == "" || status.HostID != leaseContext.LocalHostID || status.Generation == 0 || status.HostState != "HOST_LIVE" {
			return nil, errors.New("this host has not joined every configured storage lockspace")
		}
		if _, exists := joined[status.RepositoryID]; exists {
			return nil, errors.New("duplicate local sanlock repository status")
		}
		joined[status.RepositoryID] = status
	}
	for _, repository := range leaseContext.Config.Repositories {
		storage, err := managedStorage(js, repository.RepositoryID)
		if err != nil || storage.StorageKind() != protocol.StorageKindNFS || storage.Generation != repository.RepositoryGeneration ||
			storage.Identity.String() != repository.RepositoryIdentity.String() {
			return nil, errors.New("configured sanlock repository does not match applied NFS storage")
		}
		if status, ok := joined[repository.RepositoryID]; !ok || status.Lockspace != repository.Lockspace {
			return nil, errors.New("this host has not joined every configured storage lockspace")
		}
	}
	used := map[string]bool{}
	repos := make([]SanlockVMRepository, 0)
	for _, disk := range vm.Disks {
		if used[disk.StorageID] {
			continue
		}
		used[disk.StorageID] = true
		storage, err := managedStorage(js, disk.StorageID)
		if err != nil {
			return nil, err
		}
		if storage.StorageKind() != protocol.StorageKindNFS {
			if protocol.IsPoolSharedStorage(storage) {
				return nil, errors.New("storage-lease fencing supports configured shared NFS repositories only")
			}
			continue
		}
		repository, ok := configured[disk.StorageID]
		if !ok || repository.RepositoryGeneration != storage.Generation || repository.RepositoryIdentity.String() != storage.Identity.String() {
			return nil, errors.New("shared NFS repository is missing its exact pool lease configuration")
		}
		if status, ok := joined[disk.StorageID]; !ok || status.HostState != "HOST_LIVE" || status.HostID != leaseContext.LocalHostID {
			return nil, errors.New("this host has not joined the VM repository lockspace")
		}
		path := storageRegistrationPath(storage)
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errors.New("applied NFS repository path is invalid")
		}
		repos = append(repos, SanlockVMRepository{RepositoryID: disk.StorageID, Lockspace: repository.Lockspace, Path: path})
	}
	built, err := BuildSanlockVMLeases(vm.ID, repos)
	if err != nil {
		return nil, err
	}
	leases := make([]managedLease, len(built))
	for i, lease := range built {
		leases[i] = managedLease{Lockspace: lease.Lockspace, Key: lease.Key, Target: sanlockLeaseTarget{Path: lease.Path, Offset: lease.Offset}}
	}
	return leases, nil
}

func ensureManagedSanlockVMResources(ctx context.Context, leases []managedLease) error {
	if len(leases) == 0 {
		return nil
	}
	leaseContext, ok := ctx.Value(sanlockVMContextKey{}).(sanlockVMContext)
	if !ok {
		return errors.New("pool storage-lease configuration is unavailable")
	}
	for _, lease := range leases {
		if err := EnsureSanlockResource(ctx, SanlockResourceConfig{
			SanlockResource:  SanlockResource{Lockspace: lease.Lockspace, Key: lease.Key, Path: lease.Target.Path, Offset: lease.Target.Offset},
			Initialize:       leaseContext.InitializeVMResources,
			MasterAuthorized: leaseContext.InitializeVMResources,
		}); err != nil {
			return err
		}
	}
	return nil
}

func ensureVMLeaseDefinition(ctx context.Context, js []managedJournal, vm protocol.VMCreateSpec) error {
	if _, configured := ctx.Value(sanlockVMContextKey{}).(sanlockVMContext); !configured {
		return nil
	}
	leases, err := managedSanlockVMLeases(ctx, js, vm)
	if err != nil {
		return err
	}
	if err := ensureManagedSanlockVMResources(ctx, leases); err != nil {
		return err
	}
	domain, err := dumpManagedDomain(ctx, vm.ID, false)
	if err != nil {
		return errors.New("cannot verify the persistent VM storage leases")
	}
	if len(domain.Leases) != len(leases) {
		return errors.New("persistent VM storage leases do not match the pool configuration")
	}
	for i := range leases {
		if domain.Leases[i] != leases[i] {
			return errors.New("persistent VM storage leases do not match the pool configuration")
		}
	}
	return nil
}
