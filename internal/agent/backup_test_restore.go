// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

var requestTestRestorePlacement = poolVMDefineRequestPeer

var registerTestRestoredAsNew = func(ctx context.Context, engine *vmhost.ManagedEngine, operationID string, spec protocol.VMCreateSpec, localDir, manifestSHA string, artifacts []protocol.BackupDiskArtifact, firmware ...protocol.BackupFirmwareArtifact) error {
	return engine.RegisterRestoredAsNew(ctx, operationID, spec, localDir, manifestSHA, artifacts, firmware...)
}

var cleanupTestRestoredVM = (*vmhost.ManagedEngine).CleanupTestRestoredVM
var verifyTestRestoredVM = (*vmhost.ManagedEngine).VerifyTestRestoredVM

func executeTestRestore(ctx context.Context, store *Store, cmd protocol.Command, observed time.Time) (protocol.Result, error) {
	if cmd.TestRestore == nil || cmd.TestRestore.Valid() != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: "invalid test-restore command", ObservedAt: observed}, nil
	}
	if err := ensureBackupRunReserved(store, cmd); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	req := *cmd.TestRestore
	repo, err := GetPoolBackupRepository(store, req.RepositoryID)
	if err != nil {
		return failTestRestoreRun(store, cmd, observed, err.Error()), nil
	}
	backend, err := openBackupRepositoryStorage(store, repo)
	if err != nil {
		return failTestRestoreRun(store, cmd, observed, err.Error()), nil
	}
	run, err := loadPoolBackupRun(store, cmd.OperationID)
	if err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	manifest, manifestSHA, err := validatedCatalogBackupManifest(ctx, store, repo, req.RecoveryPointID, run.ManifestSHA256)
	if err != nil {
		if !restorePlacementHasNoEffects(store, cmd.OperationID, req.NewVMID) {
			return attentionTestRestoreRun(store, cmd, observed, err.Error()), nil
		}
		return failTestRestoreRun(store, cmd, observed, err.Error()), nil
	}
	if manifest.CapturedSpec == nil {
		return failTestRestoreRun(store, cmd, observed, "recovery point lacks a captured VM specification; re-backup before test restore"), nil
	}
	localDir := filepath.Join(store.dir, "backup-restore", cmd.OperationID)
	if err := backend.CopyArtifacts(ctx, req.RecoveryPointID, localDir, manifest); err != nil {
		return failTestRestoreRun(store, cmd, observed, err.Error()), nil
	}
	asNew := protocol.RestoreAsNewRequest{
		RepositoryID: req.RepositoryID, RecoveryPointID: req.RecoveryPointID,
		NewVMID: req.NewVMID, NewVMName: req.NewVMName, StorageID: req.StorageID, HostID: req.HostID,
	}
	spec, err := vmhost.BuildRestoreAsNewSpec(*manifest.CapturedSpec, asNew, store.State.PoolID, store.State.HostID)
	if err != nil {
		return failTestRestoreRun(store, cmd, observed, err.Error()), nil
	}
	engine := vmhost.NewManaged(filepath.Join(store.dir, "managed-vms"))
	if err := requestTestRestorePlacement(ctx, store, spec, cmd.OperationID); err != nil {
		if restorePlacementHasNoEffects(store, cmd.OperationID, spec.ID) {
			return failTestRestoreRun(store, cmd, observed, err.Error()), nil
		}
		return attentionTestRestoreRun(store, cmd, observed, err.Error()), nil
	}
	if err := registerTestRestoredAsNew(ctx, engine, cmd.OperationID, spec, localDir, manifestSHA, manifest.Disks, manifest.Firmware...); err != nil {
		if !errors.Is(err, vmhost.ErrRestorePlacementUncertain) && releaseProvenFailedRestorePlacement(ctx, store, spec, cmd.OperationID) == nil {
			return failTestRestoreRun(store, cmd, observed, err.Error()), nil
		}
		return attentionTestRestoreRun(store, cmd, observed, err.Error()), nil
	}
	intent, _ := backupIntentSHA256(cmd)
	if err := SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindTestRestore, PoolID: store.State.PoolID,
		RepositoryID: repo.ID, RecoveryPointID: req.RecoveryPointID, NewVMID: req.NewVMID,
		IntentSHA256: intent, Stage: "registered", Status: "succeeded", CreatedAt: observed, UpdatedAt: observed,
	}, nil); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	return protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: observed}, nil
}

func failTestRestoreRun(store *Store, cmd protocol.Command, observed time.Time, message string) protocol.Result {
	intent, _ := backupIntentSHA256(cmd)
	if err := SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindTestRestore, PoolID: store.State.PoolID,
		RepositoryID: cmd.TestRestore.RepositoryID, RecoveryPointID: cmd.TestRestore.RecoveryPointID,
		NewVMID: cmd.TestRestore.NewVMID, IntentSHA256: intent, Stage: "failed", Status: "failed",
		Error: message, CreatedAt: observed, UpdatedAt: observed,
	}, nil); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}
	}
	_ = clearBackupRestorePin(store, cmd.TestRestore.RecoveryPointID, cmd.OperationID)
	return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: message, ObservedAt: observed}
}

func attentionTestRestoreRun(store *Store, cmd protocol.Command, observed time.Time, message string) protocol.Result {
	intent, _ := backupIntentSHA256(cmd)
	_ = SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindTestRestore, PoolID: store.State.PoolID,
		RepositoryID: cmd.TestRestore.RepositoryID, RecoveryPointID: cmd.TestRestore.RecoveryPointID,
		NewVMID: cmd.TestRestore.NewVMID, IntentSHA256: intent, Stage: "placement", Status: "needs_attention",
		Error: message, CreatedAt: observed, UpdatedAt: observed,
	}, nil)
	return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: message, ObservedAt: observed}
}

type testRestoreVerificationPlan struct {
	Request    protocol.TestRestoreVerifyRequest `json:"request"`
	Run        protocol.BackupRun                `json:"run"`
	VM         protocol.VMCreateSpec             `json:"vm"`
	VerifiedAt time.Time                         `json:"-"`
}

func prepareTestRestoreVerification(s *Store, req protocol.TestRestoreVerifyRequest) (testRestoreVerificationPlan, error) {
	if err := req.Valid(); err != nil {
		return testRestoreVerificationPlan{}, ErrBackupInvalid
	}
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return testRestoreVerificationPlan{}, err
	}
	cleanup := protocol.TestRestoreCleanupRequest{RepositoryID: req.RepositoryID, RecoveryPointID: req.RecoveryPointID, TestVMID: req.TestVMID}
	run, err := matchingTestRestoreRun(s, cleanup)
	if err != nil {
		return testRestoreVerificationPlan{}, ErrBackupConflict
	}
	poolVMMu.Lock()
	record, err := testRestoreOwner(s, run)
	poolVMMu.Unlock()
	if err != nil {
		return testRestoreVerificationPlan{}, err
	}
	return testRestoreVerificationPlan{Request: req, Run: run, VM: record.Spec}, nil
}

func verifyTestRestoreNative(ctx context.Context, s *Store, plan testRestoreVerificationPlan) error {
	if plan.Run.HostID == s.State.HostID {
		return verifyTestRestoredVM(vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")), ctx, plan.Run.OperationID, plan.VM,
			plan.Request.Result == protocol.BackupTestResultPassed)
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return ErrBackupUnavailable
	}
	member, ok := poolMemberByID(membership, plan.Run.HostID)
	if !ok || member.Status != "ready" || member.PeerOrigin == "" {
		return ErrBackupUnavailable
	}
	client, err := poolTransferClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, member.PeerOrigin+poolBackupPeerPath+"/verify-test", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	reply, status, err := doPeerBackup(client, request)
	if err != nil || status != http.StatusOK || reply.Result.Status != "succeeded" || reply.Result.OperationID != plan.Run.OperationID ||
		reply.Run == nil || !sameTestRestoreRun(*reply.Run, plan.Run) || reply.IntentSHA256 != plan.Run.IntentSHA256 {
		return ErrBackupUnavailable
	}
	return nil
}

func settleTestVerificationProofFailure(s *Store, cmd protocol.Command, proofErr error) error {
	intent, err := backupMetadataIntent(cmd)
	if err != nil {
		return err
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil {
		return err
	}
	if receipt.Kind != "backup-test-verify" || receipt.IntentSHA256 != intent {
		return ErrBackupConflict
	}
	if receipt.Status != "pending" {
		return nil
	}
	receipt.Status = "needs_attention"
	if errors.Is(proofErr, ErrBackupConflict) || errors.Is(proofErr, ErrBackupInvalid) {
		receipt.Status = "failed"
	}
	receipt.Error = proofErr.Error()
	if len(receipt.Error) > 4096 {
		receipt.Error = receipt.Error[:4096]
	}
	receipt.ObservedAt = time.Now().UTC()
	return savePoolBackupMetadataReceipt(s, receipt)
}

func sameTestRestoreRun(a, b protocol.BackupRun) bool {
	return a.OperationID == b.OperationID && a.Kind == protocol.BackupKindTestRestore && a.Status == "succeeded" &&
		a.PoolID == b.PoolID && a.HostID == b.HostID && a.RepositoryID == b.RepositoryID &&
		a.RecoveryPointID == b.RecoveryPointID && a.NewVMID == b.NewVMID && a.IntentSHA256 == b.IntentSHA256
}

// RecordTestRestoreVerification persists a verified native test result on the catalog point.
func RecordTestRestoreVerification(s *Store, plan testRestoreVerificationPlan) (protocol.BackupRecoveryPoint, error) {
	req := plan.Request
	if err := req.Valid(); err != nil || plan.VerifiedAt.IsZero() || time.Since(plan.VerifiedAt) > time.Minute {
		return protocol.BackupRecoveryPoint{}, ErrBackupInvalid
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	point, err := getCatalogPoint(s, req.RepositoryID, req.RecoveryPointID)
	if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	cleanup := protocol.TestRestoreCleanupRequest{RepositoryID: req.RepositoryID, RecoveryPointID: req.RecoveryPointID, TestVMID: req.TestVMID}
	run, err := matchingTestRestoreRun(s, cleanup)
	if err != nil {
		if errors.Is(err, ErrBackupNotFound) {
			return protocol.BackupRecoveryPoint{}, ErrBackupConflict
		}
		return protocol.BackupRecoveryPoint{}, err
	}
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	record, err := testRestoreOwner(s, run)
	if err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	if !sameTestRestoreRun(run, plan.Run) || !reflect.DeepEqual(record.Spec, plan.VM) {
		return protocol.BackupRecoveryPoint{}, ErrBackupConflict
	}
	point.LastTestResult = req.Result
	point.LastTestedAt = time.Now().UTC()
	point.LastTestVMID = req.TestVMID
	if err := savePoolBackupCatalogEntry(s, point); err != nil {
		return protocol.BackupRecoveryPoint{}, err
	}
	return point, nil
}

type testRestoreCleanupPlan struct {
	Request protocol.TestRestoreCleanupRequest
	RunID   string
	VM      protocol.VMCreateSpec
}

func matchingTestRestoreRun(s *Store, req protocol.TestRestoreCleanupRequest) (protocol.BackupRun, error) {
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return protocol.BackupRun{}, err
	}
	return matchingTestRestoreRunIn(runs, req)
}

func matchingTestRestoreRunIn(runs []protocol.BackupRun, req protocol.TestRestoreCleanupRequest) (protocol.BackupRun, error) {
	var match protocol.BackupRun
	for _, run := range runs {
		if run.Kind == protocol.BackupKindTestRestore && run.Status == "succeeded" && run.NewVMID == req.TestVMID &&
			run.RecoveryPointID == req.RecoveryPointID && run.RepositoryID == req.RepositoryID {
			if match.OperationID != "" {
				return protocol.BackupRun{}, ErrBackupConflict
			}
			match = run
		}
	}
	if match.OperationID == "" {
		return protocol.BackupRun{}, ErrBackupNotFound
	}
	return match, nil
}

// A promoted master inherits cleanup receipts but not the former master's
// command journal. Recover only the command proved by the retained receipt.
func rehydratePendingTestCleanup(s *Store) error {
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID != s.State.HostID {
		return nil
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	receipts, err := listPoolBackupMetadataReceipts(s)
	if err != nil {
		return err
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return err
	}
	for _, receipt := range receipts {
		if receipt.Kind != "backup-test-cleanup" || receipt.Status != "pending" {
			continue
		}
		if _, exists := s.State.Journal[receipt.OperationID]; exists {
			continue
		}
		var command protocol.Command
		matches := 0
		for _, run := range runs {
			if run.Kind != protocol.BackupKindTestRestore || run.Status != "succeeded" ||
				run.PoolID != s.State.PoolID || run.HostID == "" {
				continue
			}
			req := protocol.TestRestoreCleanupRequest{RepositoryID: run.RepositoryID,
				RecoveryPointID: run.RecoveryPointID, TestVMID: run.NewVMID}
			if req.Valid() != nil {
				continue
			}
			candidate := protocol.Command{OperationID: receipt.OperationID, Kind: receipt.Kind, TestRestoreClean: &req}
			intent, err := backupMetadataIntent(candidate)
			if err != nil {
				return err
			}
			if intent == receipt.IntentSHA256 {
				command = candidate
				matches++
			}
		}
		if matches != 1 {
			continue
		}
		// The old master owned the Controller operation. Keep recovery internal
		// so the promoted master does not report a foreign operation result.
		if err := applyResponseWithPeer(s, protocol.ReportResponse{Commands: []protocol.Command{command}}, true); err != nil {
			return err
		}
	}
	return nil
}

func testRestoreOwner(s *Store, run protocol.BackupRun) (poolVMRecord, error) {
	pool, err := loadPoolVMs(s)
	if err != nil {
		return poolVMRecord{}, err
	}
	record, ok := pool.VMs[run.NewVMID]
	if !ok || run.HostID == "" || record.CreateOperationID != run.OperationID ||
		record.VMID != run.NewVMID || record.HostID != run.HostID || record.PoolID != s.State.PoolID ||
		record.Spec.ID != run.NewVMID || record.Spec.HostID != run.HostID || record.Spec.PoolID != s.State.PoolID ||
		record.PendingRecovery != nil || record.PendingUpdate != nil || record.PendingSnapshot != nil ||
		record.PendingStartOperationID != "" || len(record.Snapshots) != 0 {
		return poolVMRecord{}, ErrBackupConflict
	}
	return record, nil
}

func prepareTestRestoreCleanup(s *Store, req protocol.TestRestoreCleanupRequest, allowMember ...bool) (testRestoreCleanupPlan, error) {
	if err := req.Valid(); err != nil {
		return testRestoreCleanupPlan{}, ErrBackupInvalid
	}
	member := len(allowMember) > 0 && allowMember[0]
	if !member {
		if err := masterBackupPool(s, s.State.PoolID); err != nil {
			return testRestoreCleanupPlan{}, err
		}
	} else if membership, err := Membership(s); err != nil || membership.MasterHostID == s.State.HostID {
		return testRestoreCleanupPlan{}, ErrBackupUnavailable
	}
	run, err := matchingTestRestoreRun(s, req)
	if err != nil {
		return testRestoreCleanupPlan{}, err
	}
	if run.HostID != s.State.HostID {
		return testRestoreCleanupPlan{}, ErrBackupConflict
	}
	if member {
		vm, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).VMDefinition(req.TestVMID)
		if err != nil {
			vm = protocol.VMCreateSpec{ID: req.TestVMID, PoolID: s.State.PoolID, HostID: s.State.HostID}
		}
		if vm.ID != req.TestVMID || vm.PoolID != s.State.PoolID || vm.HostID != s.State.HostID {
			return testRestoreCleanupPlan{}, ErrBackupConflict
		}
		return testRestoreCleanupPlan{Request: req, RunID: run.OperationID, VM: vm}, nil
	}
	poolVMMu.Lock()
	record, err := testRestoreOwner(s, run)
	poolVMMu.Unlock()
	if errors.Is(err, ErrBackupConflict) {
		pool, loadErr := loadPoolVMs(s)
		if loadErr != nil {
			return testRestoreCleanupPlan{}, loadErr
		}
		if _, exists := pool.VMs[req.TestVMID]; exists {
			return testRestoreCleanupPlan{}, err
		}
		// An exact native delete receipt must prove an absent owner on replay.
		return testRestoreCleanupPlan{Request: req, RunID: run.OperationID, VM: protocol.VMCreateSpec{
			ID: req.TestVMID, PoolID: s.State.PoolID, HostID: s.State.HostID,
		}}, nil
	}
	if err != nil {
		return testRestoreCleanupPlan{}, err
	}
	if record.HostID != s.State.HostID {
		return testRestoreCleanupPlan{}, ErrBackupConflict
	}
	return testRestoreCleanupPlan{Request: req, RunID: run.OperationID, VM: record.Spec}, nil
}

func executeTestRestoreCleanupNative(ctx context.Context, dir string, plan testRestoreCleanupPlan) error {
	engine := vmhost.NewManaged(filepath.Join(dir, "managed-vms"))
	sum := sha256.Sum256([]byte(plan.RunID))
	deleteID := "test-clean-" + hex.EncodeToString(sum[:16])
	result, err := cleanupTestRestoredVM(engine, ctx, plan.RunID, plan.VM, deleteID)
	if err != nil {
		return err
	}
	if result.Status != "succeeded" {
		return errors.New("test VM cleanup needs reconciliation: " + result.Error)
	}
	return os.RemoveAll(filepath.Join(dir, "backup-restore", plan.RunID))
}

func finishTestRestoreCleanup(s *Store, plan testRestoreCleanupPlan) error {
	if membership, err := Membership(s); err == nil && membership.MasterHostID != s.State.HostID {
		return nil
	}
	poolVMMu.Lock()
	pool, err := loadPoolVMs(s)
	if err == nil {
		record, ok := pool.VMs[plan.VM.ID]
		if ok && (record.CreateOperationID != plan.RunID || record.HostID != plan.VM.HostID || !poolVMRecordMatchesSpec(record, plan.VM) ||
			record.PendingRecovery != nil || record.PendingUpdate != nil || record.PendingSnapshot != nil ||
			record.PendingStartOperationID != "" || len(record.Snapshots) != 0) {
			err = ErrBackupConflict
		} else if ok {
			delete(pool.VMs, plan.VM.ID)
			err = savePoolVMs(s, pool)
		}
	}
	poolVMMu.Unlock()
	if err != nil {
		return err
	}
	return clearBackupRestorePin(s, plan.Request.RecoveryPointID, plan.RunID)
}

func finishMemberTestRestoreCleanup(s *Store, req protocol.TestRestoreCleanupRequest, ownerRun protocol.BackupRun) error {
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return err
	}
	run, err := matchingTestRestoreRun(s, req)
	if err != nil || run.OperationID != ownerRun.OperationID || run.HostID != ownerRun.HostID || run.HostID == s.State.HostID {
		return ErrBackupConflict
	}
	poolVMMu.Lock()
	state, err := loadPoolVMs(s)
	if err != nil {
		poolVMMu.Unlock()
		return err
	}
	record, ok := state.VMs[req.TestVMID]
	if ok && (record.CreateOperationID != run.OperationID || record.VMID != req.TestVMID || record.HostID != run.HostID || record.PoolID != s.State.PoolID ||
		record.PendingRecovery != nil || record.PendingUpdate != nil || record.PendingSnapshot != nil ||
		record.PendingStartOperationID != "" || len(record.Snapshots) != 0) {
		poolVMMu.Unlock()
		return ErrBackupConflict
	}
	if ok {
		delete(state.VMs, req.TestVMID)
		err = savePoolVMs(s, state)
	}
	poolVMMu.Unlock()
	if err != nil {
		return err
	}
	return clearBackupRestorePin(s, req.RecoveryPointID, ownerRun.OperationID)
}

// CleanupTestRestoreVM deletes the exact stopped test VM and its copied disks.
func CleanupTestRestoreVM(s *Store, req protocol.TestRestoreCleanupRequest) error {
	plan, err := prepareTestRestoreCleanup(s, req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := executeTestRestoreCleanupNative(ctx, s.dir, plan); err != nil {
		return err
	}
	return finishTestRestoreCleanup(s, plan)
}
