// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nex.local/nex/internal/protocol"
)

type storageClusterStateDirContextKey struct{}

var (
	preparePoolStorageAt               = storagePrepareAt
	prepareClusteredPoolStorageAccess  = PrepareClusteredISCSI
	validateClusteredPoolStorageAccess = ValidateClusteredISCSIAt
)

// StorageClusterConfig returns the trusted profile for clustered iSCSI preparation.
func StorageClusterConfig(ctx context.Context) (protocol.StorageClusterConfig, bool) {
	config, ok := ctx.Value(storageClusterConfigContextKey{}).(protocol.StorageClusterConfig)
	return config, ok
}

// WithStorageClusterConfig carries the Core-master-approved profile into the private
// member storage transaction; it is never supplied by a client command.
func WithStorageClusterConfig(ctx context.Context, config protocol.StorageClusterConfig) context.Context {
	return context.WithValue(ctx, storageClusterConfigContextKey{}, config)
}

// WithStorageClusterStateDir carries the local Core state root through a
// no-mutation admission check. It is transient execution context, never a
// second durable cluster record.
func WithStorageClusterStateDir(ctx context.Context, stateDir string) context.Context {
	return context.WithValue(ctx, storageClusterStateDirContextKey{}, stateDir)
}

func storageClusterStateDir(ctx context.Context) (string, bool) {
	stateDir, ok := ctx.Value(storageClusterStateDirContextKey{}).(string)
	return stateDir, ok && filepath.IsAbs(stateDir) && filepath.Clean(stateDir) == stateDir
}

// PreparePoolStorage is the member-side, idempotent native preparation boundary. NFS,
// SMB, and a config-validated clustered GFS2 iSCSI profile may be reconciled; local ZFS
// and ordinary iSCSI remain host/single-writer storage.
func PreparePoolStorage(ctx context.Context, agentStateDir string, s protocol.StorageRegistration) (protocol.StorageRegistration, error) {
	if !protocol.IsPoolSharedStorage(s) {
		return s, errors.New("repository is not eligible for pool-member reconciliation")
	}
	var access storageAccess
	var err error
	if protocol.IsClusteredISCSI(s) {
		config, ok := StorageClusterConfig(ctx)
		if !ok {
			return s, errors.New("clustered iSCSI requires a Core cluster configuration")
		}
		access, err = prepareClusteredPoolStorageAccess(ctx, agentStateDir, s, config)
	} else {
		access, err = preparePoolStorageAt(ctx, s, filepath.Join(agentStateDir, "storage-credentials"))
	}
	if err != nil {
		return s, err
	}
	if access.ReadOnly || access.Health != "healthy" {
		return s, errors.New("pool repository is not writable")
	}
	if !protocol.IsClusteredISCSI(s) {
		if err = qemuStorageAccess(ctx, access.Path); err != nil {
			return s, err
		}
	}
	s.Path, s.Identity = access.Path, access.Observed
	s.Access = observedStorageAccess(access, time.Now().UTC())
	return s, nil
}

// ValidatePoolStorageAccess is the no-mutation reconciliation path for an already
// registered member repository. It re-inspects the active mount/session and proves
// libvirt-qemu access without mounting, logging in, or creating a managed journal entry.
func ValidatePoolStorageAccess(ctx context.Context, s protocol.StorageRegistration) (protocol.StorageRegistration, error) {
	if !protocol.IsPoolSharedStorage(s) {
		return s, errors.New("repository is not eligible for pool-member reconciliation")
	}
	var access storageAccess
	var err error
	if protocol.IsClusteredISCSI(s) {
		config, ok := StorageClusterConfig(ctx)
		if !ok {
			return s, errors.New("clustered iSCSI requires a Core cluster configuration")
		}
		stateDir, ok := storageClusterStateDir(ctx)
		if !ok {
			return s, errors.New("clustered iSCSI requires the local Core state directory")
		}
		access, err = validateClusteredPoolStorageAccess(ctx, stateDir, s, config)
	} else {
		access, err = storageInspect(ctx, s)
	}
	if err != nil {
		return s, err
	}
	if access.ReadOnly || access.Health != "healthy" {
		return s, errors.New("pool repository is not writable")
	}
	if !protocol.IsClusteredISCSI(s) {
		if err = qemuStorageAccess(ctx, access.Path); err != nil {
			return s, err
		}
	}
	s.Path, s.Identity = access.Path, access.Observed
	s.Access = observedStorageAccess(access, time.Now().UTC())
	return s, nil
}

// PoolStorageCredential returns only the selected SMB credential to an already
// authenticated pool peer. Callers must never log or persist its return outside the
// recipient Core credential store.
func PoolStorageCredential(stateDir, receipt string) (protocol.StorageCredentials, error) {
	if !storageID.MatchString(receipt) {
		return protocol.StorageCredentials{}, errors.New("invalid storage credential receipt")
	}
	dir, err := credentialDir(stateDir)
	if err != nil {
		return protocol.StorageCredentials{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return protocol.StorageCredentials{}, err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readSmall(filepath.Join(dir, entry.Name()), 1<<20)
		if err != nil {
			return protocol.StorageCredentials{}, err
		}
		var stored storedCredential
		if json.Unmarshal(b, &stored) == nil && stored.Receipt == receipt && stored.Status == "ready" {
			return protocol.StorageCredentials{Username: stored.Username, Secret: stored.Secret, Domain: stored.Domain}, nil
		}
	}
	return protocol.StorageCredentials{}, errors.New("storage credential is unavailable")
}

// SavePoolStorageCredential materializes peer-delivered SMB credentials under this
// member's private state with a member-scoped receipt; it never reuses the master's receipt.
func SavePoolStorageCredential(stateDir, poolID, hostID, repositoryID string, generation uint64, credentials protocol.StorageCredentials) (string, error) {
	request := PoolStorageCredentialRequestID(repositoryID, generation)
	receipt, err := SaveStorageCredential(stateDir, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{RequestID: request, RegistrationRequestID: request, PoolID: poolID, HostID: hostID, RepositoryID: repositoryID}, Credentials: credentials})
	if err != nil {
		return "", err
	}
	return receipt.Receipt, nil
}

// PoolStorageCredentialRequestID is member-local and generation-bound. It must replace
// the master's request ID before ManagedEngine binds the member's opaque receipt.
func PoolStorageCredentialRequestID(repositoryID string, generation uint64) string {
	return fmt.Sprintf("pool-peer-%s-%d", repositoryID, generation)
}
