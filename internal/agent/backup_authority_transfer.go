// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
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
		repo.Access = protocol.StorageAccess{}
		repo.MemberAccess = nil
		if backupCredentialKind(repo.Kind) {
			if _, err := localBackupCredential(s, repo); err != nil {
				return "", nil, errors.New("promoted backup credential is unavailable")
			}
		}
		work, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		prepared, prepErr := prepareBackupRepositoryForAdoption(work, s, repo)
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

// A v2 signed root has no aggregate byte limit. Decode one page at a time into
// a staged tree; only the final hash/count check permits the atomic master flip.
func stagePoolBackupAuthorityActivationPages(s *Store, manifest *protocol.PoolBackupManifest, generation uint64) (string, func(), error) {
	if manifest == nil || manifest.Version != 2 || manifest.Valid() != nil || generation == 0 {
		return "", nil, errors.New("replicated backup authority manifest is invalid")
	}
	if err := validatePoolBackupAuthorityPagesV2(s, manifest); err != nil {
		return "", nil, err
	}
	root := filepath.Join("pool-backup-authority", fmt.Sprintf("%020d-%s", generation, manifest.SHA256))
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
	counts := [6]int{}
	digest := sha256.New()
	total := 0
	for index := 0; index < manifest.PageCount; index++ {
		page, err := readPoolBackupPage(s, manifest, index)
		if err != nil {
			return "", nil, err
		}
		total += len(page)
		_, _ = digest.Write(page)
		decoded := &protocol.PoolBackupAuthority{}
		if err := decodePoolBackupPage(decoded, page); err != nil {
			return "", nil, err
		}
		for _, repo := range decoded.Repositories {
			counts[0]++
			if repo.Valid() != nil || repos.Repositories[repo.ID].ID != "" {
				return "", nil, errors.New("replicated backup repository is invalid or duplicated")
			}
			repo.MountPath, repo.Access, repo.MemberAccess = "", protocol.StorageAccess{}, nil
			if backupCredentialKind(repo.Kind) {
				if _, err := localBackupCredential(s, repo); err != nil {
					return "", nil, errors.New("promoted backup credential is unavailable")
				}
			}
			work, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			prepared, prepErr := prepareBackupRepositoryForAdoption(work, s, repo)
			cancel()
			if prepErr != nil {
				return "", nil, errors.New("the promoted host cannot access a replicated backup repository")
			}
			repos.Repositories[repo.ID] = prepared
		}
		for _, policy := range decoded.Definitions {
			counts[1]++
			if policy.Valid() != nil || policies.Policies[policy.ID].ID != "" {
				return "", nil, errors.New("replicated backup policy is invalid or duplicated")
			}
			policies.Policies[policy.ID] = policy
		}
		for _, run := range decoded.History {
			counts[2]++
			if run.Valid() != nil {
				return "", nil, errors.New("replicated backup run is invalid")
			}
			if run.Status == "pending" {
				run.Status, run.Stage = "needs_attention", "authority_transfer"
				run.Error = "backup outcome was unresolved when pool authority moved; inspect the target before retrying"
				run.UpdatedAt = time.Now().UTC()
			}
			if err := saveUniquePoolBackupActivationRecord(filepath.Join(poolBackupRunsPath(&stage), run.OperationID+".json"), func() error { return savePoolBackupRun(&stage, run) }); err != nil {
				return "", nil, err
			}
		}
		for _, point := range decoded.Catalog {
			counts[3]++
			if point.Valid() != nil {
				return "", nil, errors.New("replicated backup point is invalid")
			}
			if err := saveUniquePoolBackupActivationRecord(filepath.Join(poolBackupCatalogPath(&stage), point.ID+".json"), func() error { return savePoolBackupCatalogEntry(&stage, point) }); err != nil {
				return "", nil, err
			}
		}
		for _, pin := range decoded.Pins {
			counts[4]++
			if pin.Valid() != nil || pins.Pins[pin.OperationID].OperationID != "" {
				return "", nil, errors.New("replicated backup pin is invalid or duplicated")
			}
			pins.Pins[pin.OperationID] = pin
		}
		for _, receipt := range decoded.MetadataReceipts {
			counts[5]++
			if receipt.Valid() != nil {
				return "", nil, errors.New("replicated backup receipt is invalid")
			}
			if receipt.Status == "pending" {
				receipt.Status = "needs_attention"
				receipt.Error = "backup outcome was unresolved when pool authority moved; inspect the target before retrying"
				receipt.ObservedAt = time.Now().UTC()
			}
			if err := saveUniquePoolBackupActivationRecord(filepath.Join(poolBackupReceiptsPath(&stage), receipt.OperationID+".json"), func() error { return savePoolBackupMetadataReceipt(&stage, receipt) }); err != nil {
				return "", nil, err
			}
		}
	}
	if total != manifest.TotalBytes || fmt.Sprintf("%x", digest.Sum(nil)) != manifest.SHA256 ||
		counts != [6]int{manifest.Repositories, manifest.Definitions, manifest.History, manifest.Catalog, manifest.Pins, manifest.MetadataReceipts} {
		return "", nil, errors.New("replicated backup authority root or counts do not match")
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
		state, err := loadPoolPeer(s)
		if err == nil && state.BackupActiveRoot != root {
			_ = os.RemoveAll(destination)
		}
	}, nil
}

func saveUniquePoolBackupActivationRecord(path string, save func() error) error {
	if _, err := os.Stat(path); err == nil {
		return errors.New("replicated backup authority has a duplicate record")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return save()
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
func rebuildPoolBackupCatalogAfterAuthorityAdopt(ctx context.Context, s *Store) error {
	if err := masterBackupPool(s, ""); err != nil {
		return err
	}
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	for _, repo := range repos.Repositories {
		backend, err := openBackupRepositoryStorage(s, repo)
		if err != nil || backend.Verify(ctx) != nil {
			return errors.New("promoted backup repository is unavailable for catalog import")
		}
		if _, err := importPoolBackupCatalogForActivation(ctx, s, repo.ID); err != nil {
			return fmt.Errorf("promoted backup catalog import failed: %w", err)
		}
	}
	return nil
}

func reconcilePoolBackupCatalogAfterAuthorityAdopt(ctx context.Context, s *Store, record *poolTransferRecord) error {
	if err := rebuildPoolBackupCatalogAfterAuthorityAdopt(ctx, s); err != nil {
		record.LastError = poolTransferError(err)
		record.Status = "needs_attention"
		_ = savePoolTransfer(s, record)
		return err
	}
	record.Stage, record.Status, record.LastError = "repoint", "pending", ""
	return savePoolTransfer(s, record)
}
