// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestPublishedBackupExactRetrySettlesAfterAuthorityCatchesUp(t *testing.T) {
	lab := promotionLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	setTestPromotionBackupMount(t, mount)
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "published-retry", Kind: protocol.BackupKindBackupNow,
		VM:     &protocol.VMCreateSpec{ID: "vm-1", PoolID: "pool-1", HostID: master.State.HostID},
		Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	if err := applyResponse(master, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	if err := ensureBackupRunReserved(master, cmd); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", cmd.OperationID)
	pointDir := filepath.Join(mount, cmd.OperationID)
	before, err := os.ReadDir(pointDir)
	if err != nil {
		t.Fatal(err)
	}
	beforeHashes := make(map[string][sha256.Size]byte, len(before))
	for _, file := range before {
		data, err := os.ReadFile(filepath.Join(pointDir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		beforeHashes[file.Name()] = sha256.Sum256(data)
	}
	pushReplicas(t, master)
	auth, err := PoolBackupAuthorityRefs(master)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		t.Fatal(err)
	}
	previousLimit := poolBackupBundleLimit
	poolBackupBundleLimit = manifest.TotalBytes
	t.Cleanup(func() { poolBackupBundleLimit = previousLimit })
	poolReplicaMu.Lock()
	status := poolReplicaObservations[master.dir][member.State.HostID]
	status.BackupPageVersion = 1
	poolReplicaObservations[master.dir][member.State.HostID] = status
	poolReplicaMu.Unlock()
	result, err := executeBackupNow(context.Background(), master, cmd, time.Now().UTC())
	if err != nil || result.Status != "needs_attention" {
		t.Fatalf("settlement refusal: %+v, %v", result, err)
	}
	if err := completeBackgroundManagedCommand(master, managedCompletion{result: result, background: true}, nil); err != nil {
		t.Fatal(err)
	}
	if run, err := loadPoolBackupRun(master, cmd.OperationID); err != nil || run.Status != "pending" {
		t.Fatalf("run before retry: %+v, %v", run, err)
	}
	if points, err := loadPoolBackupCatalog(master); err != nil || len(points) != 0 {
		t.Fatalf("catalog before retry: %+v, %v", points, err)
	}
	status.BackupPageVersion = 2
	poolReplicaMu.Lock()
	poolReplicaObservations[master.dir][member.State.HostID] = status
	poolReplicaMu.Unlock()
	hidden := filepath.Join(t.TempDir(), cmd.OperationID)
	if err := os.Rename(pointDir, hidden); err != nil {
		t.Fatal(err)
	}
	previousWrite, previousRead := verifyBackupRepository, verifyBackupRepositoryRead
	t.Cleanup(func() { verifyBackupRepository, verifyBackupRepositoryRead = previousWrite, previousRead })
	verifyBackupRepository = func(context.Context, protocol.BackupRepository) error {
		return errors.New("repository refuses writes")
	}
	verifyBackupRepositoryRead = previousWrite
	missing := acceptBackupRequest(context.Background(), master, BackupRequest{Command: &cmd})
	if missing.Err != nil || missing.Result == nil || missing.Result.Status != "needs_attention" {
		t.Fatalf("missing point replay must not recapture: %+v", missing)
	}
	if err := os.Rename(hidden, pointDir); err != nil {
		t.Fatal(err)
	}
	retry := acceptBackupRequest(context.Background(), master, BackupRequest{Command: &cmd})
	if retry.Err != nil || retry.Result == nil || retry.Result.Status != "succeeded" {
		t.Fatalf("exact retry did not settle: %+v", retry)
	}
	if run, err := loadPoolBackupRun(master, cmd.OperationID); err != nil || run.Status != "succeeded" {
		t.Fatalf("run after retry: %+v, %v", run, err)
	}
	points, err := loadPoolBackupCatalog(master)
	if err != nil || len(points) != 1 || points[0].ID != cmd.OperationID || points[0].ManifestSHA256 == "" {
		t.Fatalf("catalog after retry: %+v, %v", points, err)
	}
	replay := acceptBackupRequest(context.Background(), master, BackupRequest{Command: &cmd})
	if replay.Err != nil || replay.Result == nil || replay.Result.Status != "succeeded" {
		t.Fatalf("settled reply replay: %+v", replay)
	}
	after, err := os.ReadDir(pointDir)
	if err != nil || len(after) != len(before) {
		t.Fatalf("published artifacts changed: %d -> %d, %v", len(before), len(after), err)
	}
	for _, file := range after {
		data, err := os.ReadFile(filepath.Join(pointDir, file.Name()))
		if err != nil || sha256.Sum256(data) != beforeHashes[file.Name()] {
			t.Fatalf("published artifact %s changed: %v", file.Name(), err)
		}
	}
}
