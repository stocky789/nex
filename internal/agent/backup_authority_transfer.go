// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// stagePoolBackupAuthorityActivationPages writes every backup store into a new tree.
// A signed root has no aggregate byte limit, so its pages are decoded one at a
// time; the caller makes the tree active only with the same atomic pool-peer
// write that makes this host master. A crash before that write keeps the old
// complete tree active.
func stagePoolBackupAuthorityActivationPages(s *Store, manifest *protocol.PoolBackupManifest, generation uint64) (string, func(), func(), error) {
	if manifest != nil && manifest.Valid() != nil || generation == 0 {
		return "", nil, nil, errors.New("replicated backup authority manifest is invalid")
	}
	digest := poolBackupHash(nil) // A nil manifest is an empty authority.
	if manifest != nil {
		digest = manifest.SHA256
	}
	root := filepath.Join("pool-backup-authority", fmt.Sprintf("%020d-%s", generation, digest))
	current, err := loadPoolPeer(s)
	if err != nil || current.BackupActiveRoot == root {
		return "", nil, nil, errors.New("replicated backup authority tree cannot be activated")
	}
	parent := filepath.Join(s.dir, "pool-backup-authority")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", nil, nil, err
	}
	temporary, err := os.MkdirTemp(parent, ".stage-")
	if err != nil {
		return "", nil, nil, err
	}
	defer os.RemoveAll(temporary)
	stage := &Store{dir: temporary, State: s.State} // Not *s: Store holds a lock.
	repos := poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{}}
	policies := poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{}}
	pins := poolBackupPinsState{Pins: map[string]protocol.BackupRestorePin{}}
	var mutations []*backupRepositoryMutation
	release := func() {
		for _, mutation := range mutations {
			mutation.release()
		}
	}
	staged := false
	defer func() {
		if !staged {
			release()
		}
	}()
	visit := func(page *protocol.PoolBackupAuthority) error {
		for _, repo := range page.Repositories {
			// Native units belong to the live host, not the temporary record tree.
			poolBackupAuthorityMu.Lock()
			mutation, err := reserveBackupRepositoryMutation(s, repo.ID)
			poolBackupAuthorityMu.Unlock()
			if err != nil {
				return err
			}
			mutations = append(mutations, mutation)
			repo.MountPath, repo.Access, repo.MemberAccess = "", protocol.StorageAccess{}, nil
			// A repository this host cannot reach or read does not block promotion: the
			// record is adopted and its access is recorded failed for the operator.
			var prepared protocol.BackupRepository
			var prepErr error
			if backupCredentialKind(repo.Kind) {
				_, prepErr = localBackupCredential(s, repo)
			}
			if prepErr == nil {
				work, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				prepared, prepErr = prepareBackupRepositoryForAdoption(work, s, repo, mutation)
				cancel()
			}
			if prepErr != nil {
				prepared = repo
				markBackupRepositoryUnreachable(&prepared)
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
			if err := savePoolBackupRun(stage, run); err != nil {
				return err
			}
		}
		for _, point := range page.Catalog {
			if err := writePoolBackupCatalogEntry(stage, point); err != nil {
				return err
			}
		}
		for _, pin := range page.Pins {
			pins.Pins[pin.OperationID] = pin
		}
		for _, cleanup := range page.OrphanCleanups {
			if err := saveOrphanCleanup(stage, cleanup); err != nil {
				return err
			}
		}
		for _, receipt := range page.MetadataReceipts {
			if receipt.Status == "pending" {
				receipt.Status = "needs_attention"
				receipt.Error = "backup outcome was unresolved when pool authority moved; inspect the target before retrying"
				receipt.ObservedAt = time.Now().UTC()
			}
			if err := savePoolBackupMetadataReceipt(stage, receipt); err != nil {
				return err
			}
		}
		return nil
	}
	if manifest != nil {
		// The visitor reserves repositories, resolves credentials and mounts native units from
		// each page, so the whole root is proven against its signed digest before any page is acted on.
		if err := verifyPoolBackupAuthorityPages(s, manifest); err != nil {
			return "", nil, nil, err
		}
		if err := walkPoolBackupAuthorityPages(s, manifest, visit); err != nil {
			return "", nil, nil, err
		}
	}
	if err := savePoolBackupRepositories(stage, repos); err != nil {
		return "", nil, nil, err
	}
	if err := savePoolBackupPolicies(stage, policies); err != nil {
		return "", nil, nil, err
	}
	if err := savePoolBackupPins(stage, pins); err != nil {
		return "", nil, nil, err
	}
	if err := syncPoolBackupTree(temporary); err != nil {
		return "", nil, nil, err
	}
	destination := filepath.Join(s.dir, root)
	if err := os.RemoveAll(destination); err != nil {
		return "", nil, nil, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", nil, nil, err
	}
	if err := syncPoolBackupDirectory(parent); err != nil {
		return "", nil, nil, err
	}
	staged = true
	return root, func() {
		// A peer write can report a late fsync error after its atomic rename.
		// Keep the tree whenever the durable peer record may already select it.
		state, err := loadPoolPeer(s)
		if err == nil && state.BackupActiveRoot != root {
			_ = os.RemoveAll(destination)
		}
	}, release, nil
}

func markBackupRepositoryUnreachable(repo *protocol.BackupRepository) {
	now := time.Now().UTC()
	reason := "the new pool master could not reach or read this repository; check its network path and credentials from this host"
	repo.Health = protocol.BackupRepositoryHealth{Status: "offline", Error: reason, ObservedAt: now}
	repo.Access = protocol.StorageAccess{Status: "failed", Error: reason, ObservedAt: now}
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

// poolBackupCatalogPendingFile lists the repositories the promoted master adopted while it
// could not reach them, so their catalogs are still imported once they answer.
const poolBackupCatalogPendingFile = "pool-backup-catalog-pending.json"

var poolBackupCatalogPendingMu sync.Mutex

func loadPendingBackupCatalogImports(s *Store) (map[string]bool, error) {
	pending := map[string]bool{}
	raw, err := os.ReadFile(filepath.Join(s.dir, poolBackupCatalogPendingFile))
	if errors.Is(err, os.ErrNotExist) {
		return pending, nil
	}
	var ids []string
	if err != nil || json.Unmarshal(raw, &ids) != nil {
		return nil, errors.New("pending backup catalog imports are unavailable")
	}
	for _, id := range ids {
		pending[id] = true
	}
	return pending, nil
}

func savePendingBackupCatalogImports(s *Store, pending map[string]bool) error {
	path := filepath.Join(s.dir, poolBackupCatalogPendingFile)
	if len(pending) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	ids := slices.Sorted(maps.Keys(pending))
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(path, raw, 0600)
}

// ImportPendingBackupCatalogs imports the catalog of every repository the promoted master
// adopted unreachable, once the probe has found it reachable. It is idempotent, leaves a
// repository that still fails for the next probe, and does nothing on a host that is not master.
func ImportPendingBackupCatalogs(ctx context.Context, s *Store) error {
	poolBackupCatalogPendingMu.Lock()
	defer poolBackupCatalogPendingMu.Unlock()
	pending, err := loadPendingBackupCatalogImports(s)
	if err != nil || len(pending) == 0 {
		return err
	}
	if masterBackupPool(s, "") != nil {
		return nil
	}
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	var failed error
	for _, id := range slices.Sorted(maps.Keys(pending)) {
		repo, ok := repos.Repositories[id]
		if !ok {
			delete(pending, id) // forgotten since: nothing left to import
			continue
		}
		if repo.Access.Status == "failed" {
			continue // still unreachable; the probe records when it answers
		}
		if _, err := importPoolBackupCatalogForActivation(ctx, s, id); err != nil {
			failed = errors.Join(failed, fmt.Errorf("backup catalog import of repository %s: %w", id, err))
			continue
		}
		delete(pending, id)
	}
	return errors.Join(failed, savePendingBackupCatalogImports(s, pending))
}

// The signed catalog was empty, so scan every repository before reporting the takeover
// complete. A repository this host cannot reach does not hold the takeover up: it is recorded
// failed and its catalog is imported by ImportPendingBackupCatalogs once the probe finds it
// reachable. An import that fails for another reason, or a cancelled context, leaves the stage
// at catalog-rebuild to be retried.
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
	poolBackupCatalogPendingMu.Lock()
	defer poolBackupCatalogPendingMu.Unlock()
	pending, err := loadPendingBackupCatalogImports(s)
	if err != nil {
		return fail(err)
	}
	for _, repo := range repos.Repositories {
		if err := ctx.Err(); err != nil {
			return err // Not a verdict on any repository: this attempt is simply over.
		}
		if repo.Access.Status == "failed" {
			pending[repo.ID] = true // Adopted unreachable; imported when it answers.
			continue
		}
		backend, err := openBackupRepositoryStorage(s, repo)
		if err == nil {
			// One unreachable repository must not outlast the activate step.
			work, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = backend.Verify(work)
			cancel()
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // A shutdown cut the check short; the repository was not found unreachable.
			}
			markBackupRepositoryUnreachable(&repo)
			if saveErr := savePoolBackupRepositoryAccess(s, repo); saveErr != nil {
				return fail(saveErr)
			}
			pending[repo.ID] = true
			continue
		}
		if _, err := importPoolBackupCatalogForActivation(ctx, s, repo.ID); err != nil {
			return fail(fmt.Errorf("promoted backup catalog import failed: %w", err))
		}
		delete(pending, repo.ID)
	}
	if err := savePendingBackupCatalogImports(s, pending); err != nil {
		return fail(err)
	}
	record.Stage, record.Status, record.LastError = "repoint", "pending", ""
	return savePoolTransfer(s, record)
}
