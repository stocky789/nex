// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestBackupRepositoryHealthTransitionsAndFreshness(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	_, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	probe := func() protocol.BackupRepository {
		t.Helper()
		if err := probePoolBackupRepositories(context.Background(), s, time.Second); err != nil {
			t.Fatal(err)
		}
		repos, err := ListPoolBackupRepositories(s)
		if err != nil || len(repos) != 1 {
			t.Fatalf("repositories = %v, %v", repos, err)
		}
		return repos[0]
	}
	if repo := probe(); repo.Health.Status != "online" || repo.LastHealthTransition != nil {
		t.Fatalf("initial health = %+v, %+v", repo.Health, repo.LastHealthTransition)
	}
	entries, err := os.ReadDir(mount)
	if err != nil || len(entries) != 1 || entries[0].Name() != "owner.json" {
		t.Fatalf("probe left files: %v, %v", entries, err)
	}
	if err := os.Remove(filepath.Join(mount, "owner.json")); err != nil {
		t.Fatal(err)
	}
	if err := vmhost.ClaimBackupRepositoryOwner(mount, "repo-2", "pool-2"); err != nil {
		t.Fatal(err)
	}
	failed := probe()
	if failed.Health.Status != "offline" || failed.LastHealthTransition == nil || failed.LastHealthTransition.Status != "offline" {
		t.Fatalf("failed health = %+v, %+v", failed.Health, failed.LastHealthTransition)
	}
	if repeat := probe(); repeat.LastHealthTransition.At != failed.LastHealthTransition.At {
		t.Fatalf("repeated failure added transition: %+v", repeat.LastHealthTransition)
	}
	if err := os.Remove(filepath.Join(mount, "owner.json")); err != nil {
		t.Fatal(err)
	}
	if err := vmhost.ClaimBackupRepositoryOwner(mount, "repo-1", "pool-1"); err != nil {
		t.Fatal(err)
	}
	recovered := probe()
	if recovered.Health.Status != "online" || recovered.LastHealthTransition == nil || recovered.LastHealthTransition.Status != "recovered" {
		t.Fatalf("recovered health = %+v, %+v", recovered.Health, recovered.LastHealthTransition)
	}
	authority, err := PoolBackupAuthorityRefs(s)
	if err != nil || len(authority.Repositories) != 1 || authority.Repositories[0].Health.Status != "" || authority.Repositories[0].LastHealthTransition.Status != "recovered" {
		t.Fatalf("replicated health = %+v, %v", authority, err)
	}
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		t.Fatal(err)
	}
	repo := state.Repositories["repo-1"]
	repo.Health.ObservedAt = time.Now().Add(-backupHealthFreshness - time.Second)
	state.Repositories[repo.ID] = repo
	if err := savePoolBackupRepositories(s, state); err != nil {
		t.Fatal(err)
	}
	listed, err := ListPoolBackupRepositories(s)
	if err != nil || listed[0].Health.Status != "unknown" || listed[0].LastHealthTransition.Status != "recovered" {
		t.Fatalf("stale health = %+v, %v", listed, err)
	}
}

func TestBackupRepositoryHealthTimeoutDoesNotOverlapProbe(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	_, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldVerify := verifyBackupRepository
	defer func() { verifyBackupRepository = oldVerify }()
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	verifyBackupRepository = func(context.Context, protocol.BackupRepository) error {
		calls.Add(1)
		<-release
		return nil
	}
	start := time.Now()
	if err := probePoolBackupRepositories(context.Background(), s, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timed-out repository probe blocked the scheduler")
	}
	if err := probePoolBackupRepositories(context.Background(), s, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("overlapping probes = %d", calls.Load())
	}
	repos, err := ListPoolBackupRepositories(s)
	if err != nil || repos[0].Health.Status != "offline" || repos[0].LastHealthTransition == nil || repos[0].LastHealthTransition.Status != "offline" {
		t.Fatalf("timeout health = %+v, %v", repos, err)
	}
}

func healthTestRepository(t *testing.T) *Store {
	t.Helper()
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

// A health observation cannot grow the replicated authority, so recording it never
// re-reads every authority record: a record the authority walk rejects does not stop it.
func TestBackupRepositoryHealthSaveDoesNotWalkTheAuthority(t *testing.T) {
	s := healthTestRepository(t)
	if err := os.MkdirAll(orphanCleanupsPath(s), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanCleanupPath(s, "cleanup-bad"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := probePoolBackupRepositories(context.Background(), s, time.Second); err != nil {
		t.Fatalf("health observation was refused by the authority walk: %v", err)
	}
	repos, err := ListPoolBackupRepositories(s)
	if err != nil || len(repos) != 1 || repos[0].Health.Status != "online" {
		t.Fatalf("health = %+v, %v", repos, err)
	}
}

// Shutting down mid-probe says nothing about the destination, so it is not recorded offline.
func TestBackupProbeCancelledByShutdownIsNotRecordedOffline(t *testing.T) {
	s := healthTestRepository(t)
	oldVerify := verifyBackupRepository
	defer func() { verifyBackupRepository = oldVerify }()
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	verifyBackupRepository = func(context.Context, protocol.BackupRepository) error {
		close(started)
		<-release
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- probePoolBackupRepositories(ctx, s, time.Minute) }()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled probe pass reported success")
	}
	repos, err := ListPoolBackupRepositories(s)
	if err != nil || len(repos) != 1 || repos[0].Health.Status == "offline" || repos[0].LastHealthTransition != nil {
		t.Fatalf("shutdown was recorded as an outage: %+v, %v", repos, err)
	}
}
