// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func setTestPromotionBackupMount(t *testing.T, mount string) {
	t.Helper()
	testBackupMounts.Store(mount, true)
	t.Cleanup(func() { testBackupMounts.Delete(mount) })
	previous := prepareBackupRepositoryNative
	prepareBackupRepositoryNative = func(_ context.Context, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
		identity := &protocol.NFSStorageIdentity{Server: repo.Source.Server, Export: repo.Source.Export, FilesystemID: "test-nfs"}
		if repo.Identity != nil && *repo.Identity != *identity {
			return protocol.BackupRepository{}, ErrBackupUnavailable
		}
		repo.MountPath = mount
		repo.Identity = identity
		return repo, nil
	}
	t.Cleanup(func() { prepareBackupRepositoryNative = previous })
}

func TestPromotedCatalogImportFailureRemainsRetryable(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	setTestPromotionBackupMount(t, mount)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source:    protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
		Identity:  &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export/backups", FilesystemID: "test-nfs"},
		MountPath: mount, Generation: 1}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	record := &poolTransferRecord{OperationID: "promote-catalog-retry", Action: "promote", Role: "candidate", Stage: "catalog-rebuild", TargetHostID: s.State.HostID}
	if err := savePoolTransfer(s, record); err != nil {
		t.Fatal(err)
	}
	before := peerState(t, s).Membership.Generation
	previousImport := importPoolBackupCatalogForActivation
	t.Cleanup(func() { importPoolBackupCatalogForActivation = previousImport })
	importPoolBackupCatalogForActivation = func(*Store, string) ([]protocol.BackupRecoveryPoint, error) {
		return nil, errors.New("repository temporarily unavailable")
	}
	if err := advancePoolPromotion(context.Background(), s, record, time.Second); err == nil {
		t.Fatal("failed import reported success")
	}
	if status := PoolTransferReport(s); status == nil || status.Stage != "catalog-rebuild" || status.Status != "needs_attention" {
		t.Fatalf("failed import not durable needs_attention: %#v", status)
	}
	point := protocol.BackupRecoveryPoint{ID: "point-retry", RepositoryID: repo.ID, PoolID: "pool-1", SourceVMID: "vm-1",
		CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	importPoolBackupCatalogForActivation = func(store *Store, repositoryID string) ([]protocol.BackupRecoveryPoint, error) {
		if repositoryID != repo.ID {
			t.Fatalf("wrong repository: %s", repositoryID)
		}
		return []protocol.BackupRecoveryPoint{point}, savePoolBackupCatalogEntry(store, point)
	}
	if err := advancePoolPromotion(context.Background(), s, record, time.Second); err != nil {
		t.Fatal(err)
	}
	if status := PoolTransferReport(s); status == nil || status.Stage != "repoint" || status.Status == "needs_attention" {
		t.Fatalf("recovered catalog did not resume transfer: %#v", status)
	}
	if got := peerState(t, s).Membership.Generation; got != before {
		t.Fatalf("catalog retry switched master again: %d -> %d", before, got)
	}
	if points, err := loadPoolBackupCatalog(s); err != nil || len(points) != 1 || points[0].ID != point.ID {
		t.Fatalf("recovered catalog entry missing: %#v %v", points, err)
	}
}

func seedPromotionBackupVM(t *testing.T, master *Store) {
	t.Helper()
	vm := protocol.VMCreateSpec{
		ID: "vm-1", PoolID: master.State.PoolID, HostID: master.State.HostID, Name: "guest", GuestOS: "linux",
		CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: "repo-1", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 2}},
		NICs:  []protocol.VMNIC{{NetworkID: "net-1", Model: "virtio"}}, Boot: protocol.VMBoot{Firmware: "uefi", MediaBootOrder: 1},
	}
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm}}}); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptEmptyBackupAuthorityRemovesStaleRecords(t *testing.T) {
	s := seedBackupMaster(t)
	if err := savePoolBackupPolicies(s, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{"pol-1": testPolicy()}}); err != nil {
		t.Fatal(err)
	}
	root, rollback, err := stagePoolBackupAuthorityActivation(s, nil, 1)
	if err != nil || rollback == nil {
		t.Fatalf("stage empty authority: rollback=%v err=%v", rollback != nil, err)
	}
	peer := peerState(t, s)
	peer.BackupActiveRoot = root
	if err := savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	policies, err := ListPoolBackupPolicies(s)
	if err != nil || len(policies) != 0 {
		t.Fatalf("stale policies survived empty authority: %v %v", policies, err)
	}
}

// Fence-verified promotion must materialize durable backup repositories, policies,
// retained runs and catalog index from the signed authority replica (Campaign B gap).
func TestPoolPromotionRetainsBackupAuthority(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	setTestPromotionBackupMount(t, mount)
	seedPromotionBackupVM(t, master)
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-1")

	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.NextDispatchAt = time.Time{}
	if _, err := UpsertPoolBackupPolicy(master, policy); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC()
	if _, err := ReservePoolBackupRun(master, protocol.BackupRun{
		OperationID: "run-1", Kind: protocol.BackupKindScheduled, PoolID: "pool-1",
		VMID: "vm-1", RepositoryID: "repo-1", RecoveryPointID: "point-1", PolicyID: "pol-1", ScheduleID: "sch-1",
		OccurrenceID: "bso-aaaaaaaaaaaaaa", IntentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Stage: "reserved", Status: "pending", CreatedAt: observed,
	}); err != nil {
		t.Fatal(err)
	}
	if err := SettlePoolBackupRun(master, protocol.BackupRun{
		OperationID: "run-1", Kind: protocol.BackupKindScheduled, PoolID: "pool-1",
		VMID: "vm-1", RepositoryID: "repo-1", PolicyID: "pol-1", ScheduleID: "sch-1",
		OccurrenceID: "bso-aaaaaaaaaaaaaa", RecoveryPointID: "point-1",
		IntentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Stage:        "published", Status: "succeeded", CreatedAt: observed, UpdatedAt: observed,
	}, &protocol.BackupRecoveryPoint{
		ID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1", SourceVMID: "vm-1",
		CapturedAt: observed, Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}); err != nil {
		t.Fatal(err)
	}

	pushReplicas(t, master)
	stored, err := loadPoolReplica(candidate)
	if err != nil || stored == nil || stored.Replica.BackupAuthority == nil {
		t.Fatalf("candidate replica missing backup authority: %#v err=%v", stored, err)
	}
	auth := stored.Replica.BackupAuthority
	if len(auth.Definitions) != 1 || auth.Definitions[0].ID != "pol-1" ||
		len(auth.History) != 1 || auth.History[0].OperationID != "run-1" ||
		len(auth.Catalog) != 1 || auth.Catalog[0].ID != "point-1" ||
		len(auth.Repositories) != 1 || auth.Repositories[0].ID != "repo-1" {
		t.Fatalf("replica backup authority incomplete: %#v", auth)
	}

	before := peerState(t, master).Membership
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-backup-promote", "member-1"))
	if result.Status != "succeeded" {
		t.Fatalf("promotion = %#v", result)
	}
	requirePromoted(t, lab, candidate, before)

	policies, err := ListPoolBackupPolicies(candidate)
	if err != nil || len(policies) != 1 || policies[0].ID != "pol-1" || policies[0].ScheduleID != "sch-1" {
		t.Fatalf("promoted master policies: %#v err=%v", policies, err)
	}
	repos, err := ListPoolBackupRepositories(candidate)
	if err != nil || len(repos) != 1 || repos[0].ID != "repo-1" || repos[0].MountPath != mount {
		t.Fatalf("promoted master repositories: %#v err=%v", repos, err)
	}
	runs, err := listPoolBackupRuns(candidate)
	if err != nil || len(runs) != 1 || runs[0].OperationID != "run-1" || runs[0].Status != "succeeded" {
		t.Fatalf("promoted master runs: %#v err=%v", runs, err)
	}
	catalog, err := loadPoolBackupCatalog(candidate)
	if err != nil || len(catalog) != 1 || catalog[0].ID != "point-1" {
		t.Fatalf("promoted master catalog: %#v err=%v", catalog, err)
	}
	if _, err := TestPoolBackupRepositoryAccess(candidate, "repo-1"); err != nil {
		t.Fatalf("backup ops must be available after promote: %v", err)
	}
}

func TestPromotedMasterRecoversOnlyExactPendingTestCleanup(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	setTestPromotionBackupMount(t, mount)
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: master.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("22222222-2222-4222-8222-222222222222", master.State.HostID)
	run := protocol.BackupRun{OperationID: "test-restore-one", Kind: protocol.BackupKindTestRestore,
		PoolID: master.State.PoolID, HostID: master.State.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1",
		NewVMID: vm.ID, Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC()}
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, CreateOperationID: run.OperationID,
		State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	if err := putBackupRestorePin(master, protocol.BackupRestorePin{RecoveryPointID: run.RecoveryPointID,
		OperationID: run.OperationID, Kind: protocol.BackupPinKindTest, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	req := protocol.TestRestoreCleanupRequest{RepositoryID: run.RepositoryID, RecoveryPointID: run.RecoveryPointID, TestVMID: vm.ID}
	cmd := protocol.Command{OperationID: "cleanup-after-promotion", Kind: "backup-test-cleanup", TestRestoreClean: &req}
	intent, err := backupMetadataIntent(cmd)
	if err != nil {
		t.Fatal(err)
	}
	receipt := protocol.BackupMetadataReceipt{OperationID: cmd.OperationID, Kind: cmd.Kind,
		IntentSHA256: intent, Status: "pending", ObservedAt: time.Now().UTC()}
	if err := savePoolBackupMetadataReceipt(master, receipt); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	before := peerState(t, master).Membership
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	if result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("promote-cleanup", "member-1")); result.Status != "succeeded" {
		t.Fatalf("promotion: %+v", result)
	}
	requirePromoted(t, lab, candidate, before)
	if _, exists := candidate.State.Journal[cmd.OperationID]; exists {
		t.Fatal("promotion unexpectedly transferred the former master's journal")
	}
	receipt.IntentSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := savePoolBackupMetadataReceipt(candidate, receipt); err != nil {
		t.Fatal(err)
	}
	if err := rehydratePendingTestCleanup(candidate); err != nil {
		t.Fatal(err)
	}
	if _, exists := candidate.State.Journal[cmd.OperationID]; exists {
		t.Fatal("mismatched receipt admitted cleanup")
	}
	receipt.IntentSHA256 = intent
	if err := savePoolBackupMetadataReceipt(candidate, receipt); err != nil {
		t.Fatal(err)
	}
	if err := rehydratePendingTestCleanup(candidate); err != nil {
		t.Fatal(err)
	}
	entry, exists := candidate.State.Journal[cmd.OperationID]
	if !exists || entry.Status != "pending" || !entry.PeerDispatched || entry.Command == nil || entry.Command.TestRestoreClean == nil ||
		*entry.Command.TestRestoreClean != req {
		t.Fatalf("exact cleanup command was not recovered: %+v", entry)
	}
	entry.Status = "succeeded"
	candidate.State.Journal[cmd.OperationID] = entry
	if results := commandOutcomes(candidate.State.Journal, nil); len(results) != 0 {
		t.Fatalf("promoted internal cleanup reported a foreign Controller result: %+v", results)
	}
	if pins, err := ActiveBackupRestorePins(candidate); err != nil || !pins[run.RecoveryPointID] {
		t.Fatalf("unreachable owner released test restore pin: %+v %v", pins, err)
	}
}

// When the replica carries repositories but an empty catalog index, promotion rebuilds
// the catalog from validated NFS manifests before advertising backup ops.
func TestPoolPromotionRebuildsCatalogFromNFSWhenIndexEmpty(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	setTestPromotionBackupMount(t, mount)
	seedPromotionBackupVM(t, master)
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-nfs")

	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.NextDispatchAt = time.Time{}
	if _, err := UpsertPoolBackupPolicy(master, policy); err != nil {
		t.Fatal(err)
	}
	if points, err := loadPoolBackupCatalog(master); err != nil || len(points) != 0 {
		t.Fatalf("precondition: empty catalog %#v err=%v", points, err)
	}

	pushReplicas(t, master)
	stored, err := loadPoolReplica(candidate)
	if err != nil || stored == nil || stored.Replica.BackupAuthority == nil {
		t.Fatalf("candidate replica missing backup authority: %#v err=%v", stored, err)
	}
	if len(stored.Replica.BackupAuthority.Catalog) != 0 {
		t.Fatalf("want empty catalog in replica, got %#v", stored.Replica.BackupAuthority.Catalog)
	}

	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-backup-rebuild", "member-1"))
	if result.Status != "succeeded" {
		t.Fatalf("promotion = %#v", result)
	}

	if err := masterBackupPool(candidate, ""); err != nil {
		t.Fatalf("promoted master backup ops unavailable: %v", err)
	}
	catalog, err := loadPoolBackupCatalog(candidate)
	if err != nil || len(catalog) != 1 || catalog[0].ID != "point-nfs" {
		t.Fatalf("promoted master catalog after NFS rebuild: %#v err=%v", catalog, err)
	}
	policies, err := ListPoolBackupPolicies(candidate)
	if err != nil || len(policies) != 1 || policies[0].ID != "pol-1" {
		t.Fatalf("promoted master policies: %#v err=%v", policies, err)
	}
}

func TestAdoptPoolBackupAuthorityAndRebuildFromMount(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	setTestPromotionBackupMount(t, mount)
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-rebuild")
	auth := &protocol.PoolBackupAuthority{
		Repositories: []protocol.BackupRepository{{
			ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
			Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
			Identity:   &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export/backups", FilesystemID: "test-nfs"},
			Generation: 1,
		}},
		Definitions: []protocol.BackupPolicy{testPolicy()},
	}
	if err := auth.Valid(); err != nil {
		t.Fatal(err)
	}
	root, rollback, err := stagePoolBackupAuthorityActivation(s, auth, 1)
	if err != nil || rollback == nil {
		t.Fatalf("stage: rollback=%v err=%v", rollback != nil, err)
	}
	peer := peerState(t, s)
	previousRoot := peer.BackupActiveRoot
	peer.BackupActiveRoot = root
	if err := savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	rebuildPoolBackupCatalogAfterAuthorityAdopt(s)
	catalog, err := loadPoolBackupCatalog(s)
	if err != nil || len(catalog) != 1 || catalog[0].ID != "point-rebuild" {
		t.Fatalf("catalog after NFS rebuild: %#v err=%v", catalog, err)
	}
	policies, err := ListPoolBackupPolicies(s)
	if err != nil || len(policies) != 1 || policies[0].ID != "pol-1" {
		t.Fatalf("policies after adopt: %#v err=%v", policies, err)
	}
	peer.BackupActiveRoot = previousRoot
	if err := savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	rollback()
	if policies, err := loadPoolBackupPolicies(s); err != nil || len(policies.Policies) != 0 {
		t.Fatalf("rollback policies: %#v err=%v", policies, err)
	}
	if catalog, err := loadPoolBackupCatalog(s); err != nil || len(catalog) != 0 {
		t.Fatalf("rollback catalog: %#v err=%v", catalog, err)
	}
	if _, err := os.Stat(filepath.Join(mount, "point-rebuild")); err != nil {
		t.Fatalf("NFS payload must survive rollback: %v", err)
	}
}
