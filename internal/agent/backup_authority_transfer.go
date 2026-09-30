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

// stagePoolBackupAuthorityActivationPages writes every backup store into a new tree.
// A signed root has no aggregate byte limit, so its pages are decoded one at a
// time; the caller makes the tree active only with the same atomic pool-peer
// write that makes this host master. A crash before that write keeps the old
// complete tree active.
func stagePoolBackupAuthorityActivationPages(s *Store, manifest *protocol.PoolBackupManifest, generation uint64) (string, func(), error) {
	if manifest != nil && manifest.Valid() != nil || generation == 0 {
		return "", nil, errors.New("replicated backup authority manifest is invalid")
	}
	digest := poolBackupHash(nil) // A nil manifest is an empty authority.
	if manifest != nil {
		digest = manifest.SHA256
	}
	root := filepath.Join("pool-backup-authority", fmt.Sprintf("%020d-%s", generation, digest))
	current, err := loadPoolPeer(s)
	if err != nil || current.BackupActiveRoot == root {
		return "", nil, errors.New("replicated backup authority tree cannot be activated")
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
	policies := poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{}}
	pins := poolBackupPinsState{Pins: map[string]protocol.BackupRestorePin{}}
	visit := func(page *protocol.PoolBackupAuthority) error {
		for _, repo := range page.Repositories {
			repo.MountPath, repo.Access, repo.MemberAccess = "", protocol.StorageAccess{}, nil
			if backupCredentialKind(repo.Kind) {
				if _, err := localBackupCredential(s, repo); err != nil {
					return errors.New("promoted backup credential is unavailable")
				}
			}
			work, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			prepared, prepErr := prepareBackupRepositoryForAdoption(work, s, repo)
			cancel()
			if prepErr != nil {
				return errors.New("the promoted host cannot access a replicated backup repository")
			}
			repos.Repositories[repo.ID] = prepared
		}
		for _, policy := range page.Definitions {
			policies.Policies[policy.ID] = policy
		}
		for _, run := range page.History {
			if run.Status == "pending" {
				run.Status, run.Stage = "needs_attention", "authority_transfer"
				run.Error = "backup outcome was unresolved when pool authority moved; inspect the target before retrying"
				run.UpdatedAt = time.Now().UTC()
			}
			if err := savePoolBackupRun(&stage, run); err != nil {
				return err
			}
		}
		for _, point := range page.Catalog {
			if err := writePoolBackupCatalogEntry(&stage, point); err != nil {
				return err
			}
		}
		for _, pin := range page.Pins {
			pins.Pins[pin.OperationID] = pin
		}
		for _, receipt := range page.MetadataReceipts {
			if receipt.Status == "pending" {
				receipt.Status = "needs_attention"
				receipt.Error = "backup outcome was unresolved when pool authority moved; inspect the target before retrying"
				receipt.ObservedAt = time.Now().UTC()
			}
			if err := savePoolBackupMetadataReceipt(&stage, receipt); err != nil {
				return err
			}
		}
		return nil
	}
	if manifest != nil {
		if err := walkPoolBackupAuthorityPages(s, manifest, visit); err != nil {
			return "", nil, err
		}
	}
	if err := savePoolBackupRepositories(&stage, repos); err != nil {
		return "", nil, err
	}
	if err := savePoolBackupPolicies(&stage, policies); err != nil {
		return "", nil, err
	}
	if err := savePoolBackupPins(&stage, pins); err != nil {
		return "", nil, err
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

var importPoolBackupCatalogForActivation = importPoolBackupCatalog

// The signed catalog was empty, so scan every repository before reporting the
// takeover complete. A partial import remains retryable from catalog-rebuild.
func reconcilePoolBackupCatalogAfterAuthorityAdopt(ctx context.Context, s *Store, record *poolTransferRecord) error {
	fail := func(err error) error {
		record.LastError = poolTransferError(err)
		record.Status = "needs_attention"
		_ = savePoolTransfer(s, record)
		return err
	}
	if err := masterBackupPool(s, ""); err != nil {
		return fail(err)
	}
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return fail(err)
	}
	for _, repo := range repos.Repositories {
		backend, err := openBackupRepositoryStorage(s, repo)
		if err != nil || backend.Verify(ctx) != nil {
			return fail(errors.New("promoted backup repository is unavailable for catalog import"))
		}
		if _, err := importPoolBackupCatalogForActivation(ctx, s, repo.ID); err != nil {
			return fail(fmt.Errorf("promoted backup catalog import failed: %w", err))
		}
	}
	record.Stage, record.Status, record.LastError = "repoint", "pending", ""
	return savePoolTransfer(s, record)
}
