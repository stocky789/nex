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
	// Settlement is never gated on member replicas; a frozen authority is the
	// admission refusal that leaves the published point unsettled.
	transfer := &poolTransferRecord{OperationID: "freeze-settlement", Action: "promote", Role: "source",
		Stage: "freeze", Status: "pending", TargetHostID: member.State.HostID, Frozen: true}
	if err := savePoolTransfer(master, transfer); err != nil {
		t.Fatal(err)
	}
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
	transfer.Stage, transfer.Status, transfer.Frozen = "aborted", "failed", false
	if err := savePoolTransfer(master, transfer); err != nil {
		t.Fatal(err)
	}
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
	// An exact request answers from the journal; the background reconciler,
	// not the request, does the repository work.
	if replay := acceptBackupRequest(context.Background(), master, BackupRequest{Command: &cmd}); replay.Err != nil ||
		replay.Result == nil || replay.Result.Status != "needs_attention" {
		t.Fatalf("exact request while unsettled: %+v", replay)
	}
	journaled := *master.State.Journal[cmd.OperationID].Command
	if missing := reconcilePublishedBackupNow(context.Background(), master, journaled, time.Now().UTC()); missing.Status != "needs_attention" {
		t.Fatalf("missing point reconcile must not recapture: %+v", missing)
	}
	if err := os.Rename(hidden, pointDir); err != nil {
		t.Fatal(err)
	}
	retry := reconcilePublishedBackupNow(context.Background(), master, journaled, time.Now().UTC())
	if retry.Status != "succeeded" {
		t.Fatalf("reconcile did not settle: %+v", retry)
	}
	if err := completeBackgroundManagedCommand(master, managedCompletion{result: retry, background: true}, nil); err != nil {
		t.Fatal(err)
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
