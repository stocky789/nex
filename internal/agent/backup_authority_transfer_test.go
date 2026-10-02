// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// cancelAfterFirstCheck reports no error to its first Err call and a cancellation to every
// later one, which models a shutdown that lands while a repository is being verified.
type cancelAfterFirstCheck struct {
	context.Context
	checks atomic.Int32
}

func (c *cancelAfterFirstCheck) Err() error {
	if c.checks.Add(1) > 1 {
		return context.Canceled
	}
	return nil
}

func unreachableTestRepository(t *testing.T, s *Store) protocol.BackupRepository {
	t.Helper()
	repo := protocol.BackupRepository{ID: "repo-bad", PoolID: "pool-1", Name: "Bad", Kind: protocol.BackupRepoKindNFS,
		Source:    protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/bad"},
		MountPath: filepath.Join(t.TempDir(), "missing"),
		Identity:  &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export/bad", FilesystemID: "test-nfs-bad"}}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	return repo
}

// A shutdown that interrupts the repository check is not evidence the repository is
// unreachable: it must not be recorded offline, and the stage must stay retryable.
func TestCatalogReconcileCancellationIsNotAnUnreachableRepository(t *testing.T) {
	s := seedBackupMaster(t)
	repo := unreachableTestRepository(t, s)
	record := &poolTransferRecord{OperationID: "promote-cancelled", Action: "promote", Role: "candidate", Stage: "catalog-rebuild", TargetHostID: s.State.HostID}
	if err := savePoolTransfer(s, record); err != nil {
		t.Fatal(err)
	}
	ctx := &cancelAfterFirstCheck{Context: context.Background()}
	if err := reconcilePoolBackupCatalogAfterAuthorityAdopt(ctx, s, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reconcile = %v", err)
	}
	if status := PoolTransferReport(s); status == nil || status.Stage != "catalog-rebuild" {
		t.Fatalf("cancelled reconcile moved the transfer on: %#v", status)
	}
	got, err := GetPoolBackupRepository(s, repo.ID)
	if err != nil || got.Access.Status == "failed" || got.Health.Status == "offline" {
		t.Fatalf("a cancelled check marked the repository unreachable: %+v %v", got, err)
	}
	if pending, err := loadPendingBackupCatalogImports(s); err != nil || len(pending) != 0 {
		t.Fatalf("a cancelled check queued an import: %v %v", pending, err)
	}
}

// A repository the promoted master could not reach does not hold the takeover up, but its
// catalog is imported as soon as the probe finds it reachable instead of staying empty.
func TestUnreachableRepositoryCatalogImportsOnceItAnswers(t *testing.T) {
	s := seedBackupMaster(t)
	repo := unreachableTestRepository(t, s)
	record := &poolTransferRecord{OperationID: "promote-unreachable-retry", Action: "promote", Role: "candidate", Stage: "catalog-rebuild", TargetHostID: s.State.HostID}
	if err := savePoolTransfer(s, record); err != nil {
		t.Fatal(err)
	}
	var imported []string
	previous := importPoolBackupCatalogForActivation
	t.Cleanup(func() { importPoolBackupCatalogForActivation = previous })
	importPoolBackupCatalogForActivation = func(_ context.Context, _ *Store, id string) ([]protocol.BackupRecoveryPoint, error) {
		imported = append(imported, id)
		return nil, nil
	}
	if err := reconcilePoolBackupCatalogAfterAuthorityAdopt(context.Background(), s, record); err != nil {
		t.Fatal(err)
	}
	if status := PoolTransferReport(s); status == nil || status.Stage != "repoint" {
		t.Fatalf("an unreachable repository held the takeover up: %#v", status)
	}
	if len(imported) != 0 {
		t.Fatalf("imported the catalog of an unreachable repository: %v", imported)
	}
	if err := ImportPendingBackupCatalogs(context.Background(), s); err != nil || len(imported) != 0 {
		t.Fatalf("a probe pass before the repository answered imported %v (%v)", imported, err)
	}
	reachable, err := GetPoolBackupRepository(s, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	reachable.Access = protocol.StorageAccess{Status: "ok", Readable: true, Writable: true, ObservedAt: time.Now().UTC()}
	reachable.Health = protocol.BackupRepositoryHealth{Status: "online", ObservedAt: time.Now().UTC()}
	if err := savePoolBackupRepositoryAccess(s, reachable); err != nil {
		t.Fatal(err)
	}
	if err := ImportPendingBackupCatalogs(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0] != repo.ID {
		t.Fatalf("imports after the repository answered = %v", imported)
	}
	if err := ImportPendingBackupCatalogs(context.Background(), s); err != nil || len(imported) != 1 {
		t.Fatalf("a settled import ran again: %v (%v)", imported, err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, poolBackupCatalogPendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending import marker kept after the import: %v", err)
	}
}

// The activation visitor reserves repositories and mounts native units from each page, so a
// root whose digest does not match must be refused before any page is acted on.
func TestActivationVerifiesWholeRootBeforeActingOnPages(t *testing.T) {
	s := seedBackupMaster(t)
	var prepared atomic.Int32
	previous := prepareBackupRepositoryNative
	t.Cleanup(func() { prepareBackupRepositoryNative = previous })
	prepareBackupRepositoryNative = func(_ context.Context, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
		prepared.Add(1)
		return repo, nil
	}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source:   protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
		Identity: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export/backups", FilesystemID: "test-nfs"}}
	manifest, err := stagePoolBackupAuthority(s, &protocol.PoolBackupAuthority{Repositories: []protocol.BackupRepository{repo}})
	if err != nil {
		t.Fatal(err)
	}
	// Change one letter of the repository's name: the page still decodes and validates, but the
	// root no longer matches its signed digest.
	page := poolBackupPageFile(s, manifest.SHA256, 0)
	body, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte(strings.Replace(string(body), "Primary", "Primarz", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := stagePoolBackupAuthorityActivationPages(s, manifest, 7); err == nil {
		t.Fatal("a root that does not match its digest was activated")
	}
	if prepared.Load() != 0 {
		t.Fatalf("repository preparation ran %d times on an unverified root", prepared.Load())
	}
}

// A page of a verified root is replaced in one step: the new content is in place, the old
// content is never missing in between, and no temporary link is left behind.
func TestLinkPoolBackupBlobReplacesPageAtomically(t *testing.T) {
	s := seedBackupMaster(t)
	oldPage, newPage := []byte("old page content"), []byte("new page content")
	path := filepath.Join(t.TempDir(), "0000")
	poolBackupBlobMu.Lock()
	oldHash, err := storePoolBackupBlob(s, oldPage)
	newHash, newErr := storePoolBackupBlob(s, newPage)
	poolBackupBlobMu.Unlock()
	if err != nil || newErr != nil {
		t.Fatal(err, newErr)
	}
	if err := linkPoolBackupBlob(s, oldHash, path); err != nil {
		t.Fatal(err)
	}
	if err := linkPoolBackupBlob(s, newHash, path); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(newPage) {
		t.Fatalf("page = %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("temporary link left beside the page: %v", entries)
	}
	// A stale temporary link from an interrupted install does not block the next one.
	if err := os.WriteFile(path+".link", []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := linkPoolBackupBlob(s, oldHash, path); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(oldPage) {
		t.Fatalf("page after interrupted install = %q", got)
	}
}

// Growth admission asks every ready member for its replica status while the caller holds the
// backup authority lock, so slow members are waited for together, not one after another.
func TestBackupAuthorityGrowthAsksMembersInParallel(t *testing.T) {
	lab := promotionLab(t, 3)
	master := lab.host("master-1")
	pushReplicas(t, master)
	const delay = 600 * time.Millisecond
	lab.intercept.Store(func(_ string, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == poolReplicaPath {
			time.Sleep(delay)
		}
	})
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := requirePoolBackupMembersCurrent(master, membership); err != nil {
		t.Fatalf("members holding the current replica refused growth: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*delay+delay/2 {
		t.Fatalf("three slow members were asked one after another: %s", elapsed)
	}
}
