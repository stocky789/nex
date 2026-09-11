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

// PreparePoolStorage is the member-side, idempotent native preparation boundary. Only
// NFS and SMB can be reconciled to another Core; local ZFS and ordinary iSCSI remain
// host/single-writer storage and are rejected before any mount or session side effect.
func PreparePoolStorage(ctx context.Context, agentStateDir string, s protocol.StorageRegistration) (protocol.StorageRegistration, error) {
	if s.StorageKind() != protocol.StorageKindNFS && s.StorageKind() != protocol.StorageKindSMB {
		return s, errors.New("only NFS and SMB repositories may be reconciled to pool members")
	}
	access, err := storagePrepareAt(ctx, s, filepath.Join(agentStateDir, "storage-credentials"))
	if err != nil {
		return s, err
	}
	if access.ReadOnly || access.Health != "healthy" {
		return s, errors.New("pool repository is not writable")
	}
	if err = qemuStorageAccess(ctx, access.Path); err != nil {
		return s, err
	}
	s.Path, s.Identity = access.Path, access.Observed
	available := access.Available
	s.Access = protocol.StorageAccess{Status: "healthy", Readable: true, Writable: true, TotalBytes: access.Total, AvailableBytes: &available, ObservedAt: time.Now().UTC()}
	return s, nil
}

// ValidatePoolStorageAccess is the no-mutation reconciliation path for an already
// registered member repository. It re-inspects the active mount/session and proves
// libvirt-qemu access without mounting, logging in, or creating a managed journal entry.
func ValidatePoolStorageAccess(ctx context.Context, s protocol.StorageRegistration) (protocol.StorageRegistration, error) {
	if s.StorageKind() != protocol.StorageKindNFS && s.StorageKind() != protocol.StorageKindSMB {
		return s, errors.New("only NFS and SMB repositories may be reconciled to pool members")
	}
	access, err := storageInspect(ctx, s)
	if err != nil {
		return s, err
	}
	if access.ReadOnly || access.Health != "healthy" {
		return s, errors.New("pool repository is not writable")
	}
	if err = qemuStorageAccess(ctx, access.Path); err != nil {
		return s, err
	}
	s.Path, s.Identity = access.Path, access.Observed
	available := access.Available
	s.Access = protocol.StorageAccess{Status: "healthy", Readable: true, Writable: true, TotalBytes: access.Total, AvailableBytes: &available, ObservedAt: time.Now().UTC()}
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
			return protocol.StorageCredentials{Username: stored.Username, Secret: stored.Secret}, nil
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
