// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nex.local/nex/internal/protocol"
)

// stagePoolBackupAuthorityActivation writes every backup store into a new tree.
// The caller makes it active only with the same atomic pool-peer write that makes
// this host master. A crash before that write keeps the old complete tree active.
func stagePoolBackupAuthorityActivation(s *Store, auth *protocol.PoolBackupAuthority, generation uint64) (string, func(), error) {
	if auth == nil {
		auth = &protocol.PoolBackupAuthority{}
	}
	if auth.Valid() != nil || generation == 0 {
		return "", nil, errors.New("replicated backup authority is invalid")
	}
	manifest, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		return "", nil, err
	}
	root := filepath.Join("pool-backup-authority", fmt.Sprintf("%020d-%s", generation, manifest.SHA256))
	current, err := loadPoolPeer(s)
	if err != nil {
		return "", nil, errors.New("this host's pool state is unavailable")
	}
	if current.BackupActiveRoot == root {
		return "", nil, errors.New("replicated backup authority tree is already active")
	}
	parent := filepath.Join(s.dir, "pool-backup-authority")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", nil, err
	}
	temporary, err := os.MkdirTemp(parent, ".stage-")
	if err != nil {
		return "", nil, err
	}
	defer os.RemoveAll(temporary)
	stage := *s
	stage.dir = temporary
	repos := poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{}}
	for _, repo := range auth.Repositories {
		repo.MountPath = ""
		repo.Identity = nil
		repo.Access = protocol.StorageAccess{}
		repo.MemberAccess = nil
		work, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		prepared, prepErr := prepareBackupRepository(work, repo)
		cancel()
		if prepErr != nil {
			return "", nil, errors.New("the promoted host cannot access a replicated backup repository")
		}
		repos.Repositories[repo.ID] = prepared
	}
	if err := savePoolBackupRepositories(&stage, repos); err != nil {
		return "", nil, err
	}
	policies := poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{}}
	for _, policy := range auth.Definitions {
		policies.Policies[policy.ID] = policy
	}
	if err := savePoolBackupPolicies(&stage, policies); err != nil {
		return "", nil, err
	}
	for _, run := range auth.History {
		if err := savePoolBackupRun(&stage, run); err != nil {
			return "", nil, err
		}
	}
	for _, point := range auth.Catalog {
		if err := savePoolBackupCatalogEntry(&stage, point); err != nil {
			return "", nil, err
		}
	}
	pins := poolBackupPinsState{Pins: map[string]protocol.BackupRestorePin{}}
	for _, pin := range auth.Pins {
		pins.Pins[pin.OperationID] = pin
	}
	if err := savePoolBackupPins(&stage, pins); err != nil {
		return "", nil, err
	}
	for _, receipt := range auth.MetadataReceipts {
		if err := savePoolBackupMetadataReceipt(&stage, receipt); err != nil {
			return "", nil, err
		}
	}
	if err := syncPoolBackupTree(temporary); err != nil {
		return "", nil, err
	}
	destination := filepath.Join(s.dir, root)
	if err := os.RemoveAll(destination); err != nil {
		return "", nil, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", nil, err
	}
	if err := syncPoolBackupDirectory(parent); err != nil {
		return "", nil, err
	}
	return root, func() {
		// A peer write can report a late fsync error after its atomic rename.
		// Keep the tree whenever the durable peer record may already select it.
		state, err := loadPoolPeer(s)
		if err == nil && state.BackupActiveRoot != root {
			_ = os.RemoveAll(destination)
		}
	}, nil
}

func syncPoolBackupDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func syncPoolBackupTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return syncPoolBackupDirectory(path)
		}
		return nil
	})
}

var importPoolBackupCatalogForActivation = ImportPoolBackupCatalog

// The signed catalog was empty, so scan every repository before reporting the
// takeover complete. A partial import remains retryable from catalog-rebuild.
func rebuildPoolBackupCatalogAfterAuthorityAdopt(s *Store) error {
	if err := masterBackupPool(s, ""); err != nil {
		return err
	}
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	for _, repo := range repos.Repositories {
		if repo.MountPath == "" {
			return errors.New("promoted backup repository has no mount path")
		}
		if info, err := os.Stat(repo.MountPath); err != nil || !info.IsDir() {
			return errors.New("promoted backup repository is unavailable for catalog import")
		}
		if _, err := importPoolBackupCatalogForActivation(s, repo.ID); err != nil {
			return fmt.Errorf("promoted backup catalog import failed: %w", err)
		}
	}
	return nil
}

func reconcilePoolBackupCatalogAfterAuthorityAdopt(s *Store, record *poolTransferRecord) error {
	if err := rebuildPoolBackupCatalogAfterAuthorityAdopt(s); err != nil {
		record.LastError = poolTransferError(err)
		record.Status = "needs_attention"
		_ = savePoolTransfer(s, record)
		return err
	}
	record.Stage, record.Status, record.LastError = "repoint", "pending", ""
	return savePoolTransfer(s, record)
}
