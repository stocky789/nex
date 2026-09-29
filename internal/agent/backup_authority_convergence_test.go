// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func seedBackupRunFiles(t *testing.T, s *Store, count int) {
	t.Helper()
	if err := os.MkdirAll(poolBackupRunsPath(s), 0700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		run := protocol.BackupRun{OperationID: fmt.Sprintf("run-%05d", i), Kind: protocol.BackupKindBackupNow,
			PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: stamp}
		body, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(poolBackupRunsPath(s), run.OperationID+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func forceV2BackupAuthority(t *testing.T) {
	t.Helper()
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = 1
	t.Cleanup(func() { poolBackupBundleLimit = previous })
}

// Building a replica only numbers it; pages are written by the push and only
// when the signed root is not already staged.
func TestBackupAuthorityV2UnchangedRootIsNotRewritten(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	seedBackupRunFiles(t, master, 200)
	forceV2BackupAuthority(t)
	if _, _, err := buildPoolAuthorityReplica(master); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(master.dir, poolBackupPageDir)); len(entries) != 0 {
		t.Fatalf("building a replica staged %d page entries", len(entries))
	}
	pushReplicas(t, master)
	replica, _, err := buildPoolAuthorityReplica(master)
	if err != nil || replica.BackupManifest == nil || replica.BackupManifest.Version != 2 {
		t.Fatalf("v2 manifest = %#v %v", replica.BackupManifest, err)
	}
	if replica.BackupManifest.PageCount >= 50 {
		t.Fatalf("200 records used %d v2 pages; records are not packed", replica.BackupManifest.PageCount)
	}
	first, err := os.Stat(poolBackupPageFile(master, replica.BackupManifest.SHA256, 0))
	if err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	second, err := os.Stat(poolBackupPageFile(master, replica.BackupManifest.SHA256, 0))
	if err != nil || !os.SameFile(first, second) {
		t.Fatalf("unchanged authority page was rewritten: %v", err)
	}
}

// A member that holds no replica yet must still receive a v2 authority.
func TestBackupAuthorityV2ReachesMemberWithoutReplica(t *testing.T) {
	lab := promotionLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	seedBackupRunFiles(t, master, 3)
	forceV2BackupAuthority(t)
	if stored, err := loadPoolReplica(member); err != nil || stored != nil {
		t.Fatalf("precondition: member already holds a replica: %v", err)
	}
	pushReplicas(t, master)
	stored, err := loadPoolReplica(member)
	if err != nil || stored == nil || stored.Replica.BackupManifest == nil || stored.Replica.BackupManifest.Version != 2 {
		t.Fatalf("fresh member replica = %#v %v", stored, err)
	}
}

// Terminal settlement consumes room its reservation already admitted; an
// unreachable member must not strand the run as pending.
func TestBackupAuthorityV2SettlementIgnoresUnavailableMember(t *testing.T) {
	lab := promotionLab(t, 2)
	master := lab.host("master-1")
	forceV2BackupAuthority(t)
	pushReplicas(t, master)
	now := time.Now().UTC()
	run := protocol.BackupRun{OperationID: "run-1", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1",
		VMID: "vm-1", RepositoryID: "repo-1", Stage: "reserved", Status: "pending", CreatedAt: now}
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	pushReplicas(t, master)
	lab.drop("member-2", "!"+poolReplicaPath)
	poolReplicaMu.Lock()
	for id, status := range poolReplicaObservations[master.dir] {
		status.ReceivedAt = now.Add(-2 * time.Minute)
		poolReplicaObservations[master.dir][id] = status
	}
	poolReplicaMu.Unlock()
	run.Stage, run.Status, run.Error, run.UpdatedAt = "failed", "failed", "disk read failed", now
	if err := SettlePoolBackupRun(master, run, nil); err != nil {
		t.Fatalf("terminal settlement with one member down: %v", err)
	}
	growth := run
	growth.OperationID, growth.Stage, growth.Status, growth.Error = "run-2", "reserved", "pending", ""
	if err := savePoolBackupRun(master, growth); err == nil {
		t.Fatal("new run was admitted while a member cannot confirm v2")
	}
}

// The adopting master imports an empty signed catalog before it re-points
// members, so no member can confirm the new master's authority yet.
func TestPoolPromotionV2EmptyCatalogImportsBeforeRepoint(t *testing.T) {
	lab := promotionLab(t, 2)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	setTestPromotionBackupMount(t, mount)
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	forceV2BackupAuthority(t)
	pushReplicas(t, master)
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-v2-empty", "member-1"))
	if result.Status != "succeeded" {
		t.Fatalf("promotion = %#v transfer=%#v", result, PoolTransferReport(candidate))
	}
	catalog, err := loadPoolBackupCatalog(candidate)
	if err != nil || len(catalog) != 1 || catalog[0].ID != "point-1" {
		t.Fatalf("promoted catalog = %#v %v", catalog, err)
	}
}

// Page roots are collected after a failed member push and after a terminal
// failed transfer; only the pushed root and the stored replica root remain.
func TestBackupAuthorityPagesCollectedAfterFailures(t *testing.T) {
	lab := promotionLab(t, 2)
	master := lab.host("master-1")
	forceV2BackupAuthority(t)
	seedBackupRunFiles(t, master, 3)
	pushReplicas(t, master)
	record := &poolTransferRecord{OperationID: "op-aborted", Action: "promote", Role: "source",
		Stage: "aborted", Status: "failed", TargetHostID: "member-1"}
	if err := savePoolTransfer(master, record); err != nil {
		t.Fatal(err)
	}
	lab.drop("member-2", "!"+poolReplicaPath)
	for i := 3; i < 6; i++ {
		seedBackupRunFiles(t, master, i+1)
		if err := PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err == nil {
			t.Fatal("push to an unreachable member succeeded")
		}
	}
	if dirs := poolBackupBundleDirs(t, master); dirs != 1 {
		t.Fatalf("master kept %d page roots after failed pushes", dirs)
	}
}

// Repairing a corrupt blob must repair every page already linked to it.
func TestBackupAuthorityBlobRepairFixesLinkedPages(t *testing.T) {
	s := seedBackupMaster(t)
	page := []byte("authority page")
	poolBackupBlobMu.Lock()
	defer poolBackupBlobMu.Unlock()
	hash, err := storePoolBackupBlob(s, page)
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(s.dir, poolBackupPageDir, "root", "0000")
	if err := linkPoolBackupBlob(s, hash, linked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poolBackupBlobFile(s, hash), []byte("corrupted page"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := storePoolBackupBlob(s, page); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(linked); err != nil || string(data) != string(page) {
		t.Fatalf("linked page after blob repair = %q %v", data, err)
	}
}
