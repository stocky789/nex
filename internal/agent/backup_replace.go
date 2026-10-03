// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// applyExclusiveReplaceCutover is the managed exclusive cutover step. Tests stub it;
// production calls ManagedEngine.ApplyExclusiveReplaceCutover.
var applyExclusiveReplaceCutover = func(ctx context.Context, engine *vmhost.ManagedEngine, operationID string, spec protocol.VMCreateSpec, localDir string, artifacts []protocol.BackupDiskArtifact, firmware ...protocol.BackupFirmwareArtifact) error {
	return engine.ApplyExclusiveReplaceCutover(ctx, operationID, spec, localDir, artifacts, firmware...)
}

var reserveReplacePoolOwner = func(ctx context.Context, s *Store, operationID string, previous, next protocol.VMCreateSpec) error {
	return poolVMUpdateRequestPeer(ctx, s, previous.ID, "update-reserve", operationID, &previous, &next, nil)
}

var commitReplacePoolOwner = func(ctx context.Context, s *Store, engine *vmhost.ManagedEngine, operationID, vmID string) error {
	evidence, err := engine.SuccessfulVMUpdateEvidence(ctx, vmID)
	if err != nil || evidence.OperationID != operationID || evidence.Status != "succeeded" {
		return errors.New("restored VM update evidence is unavailable")
	}
	return poolVMUpdateRequestPeer(ctx, s, vmID, "update-commit", operationID, nil, nil, &evidence)
}

// executeRestoreReplace copies artifacts locally, retains original disk paths, then runs
// exclusive cutover into new storage files and an updated stopped definition. cutover-ready
// alone is never success; the restore pin clears only after cutover settles succeeded.
func executeRestoreReplace(ctx context.Context, store *Store, cmd protocol.Command, observed time.Time) (protocol.Result, error) {
	if cmd.RestoreReplace == nil || cmd.RestoreReplace.Valid() != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: "invalid restore-replace command", ObservedAt: observed}, nil
	}
	if err := ensureBackupRunReserved(store, cmd); err != nil {
		return unreservedBackupResult(store, cmd, observed, err), nil
	}
	req := *cmd.RestoreReplace
	repo, err := GetPoolBackupRepository(store, req.RepositoryID)
	if err != nil {
		return failReplaceRun(store, cmd, observed, err.Error()), nil
	}
	backend, err := openBackupRepositoryStorage(store, repo)
	if err != nil {
		return failReplaceRun(store, cmd, observed, err.Error()), nil
	}
	run, err := loadPoolBackupRun(store, cmd.OperationID)
	if err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	manifest, _, err := validatedCatalogBackupManifest(ctx, store, repo, req.RecoveryPointID, run.ManifestSHA256)
	if err == nil && manifest.CapturedSpec == nil {
		err = errors.New("recovery point lacks a captured VM specification; re-backup before restore-replace")
	}
	if err != nil {
		if replaceHasNoEffects(store, cmd.OperationID) {
			return failReplaceRun(store, cmd, observed, err.Error()), nil
		}
		return attentionReplaceRun(store, cmd, observed, run.Stage, err.Error()), nil
	}
	engine := vmhost.NewManaged(filepath.Join(store.dir, "managed-vms"))
	vm, vmErr := engine.VMDefinition(req.OriginalVMID)
	originalExists := vmErr == nil && vm.ID == req.OriginalVMID
	powerState := ""
	if originalExists {
		if state, stateErr := vmhost.ManagedVMState(ctx, req.OriginalVMID); stateErr == nil {
			powerState = state
		} else {
			return failReplaceRun(store, cmd, observed, "original VM state is unavailable"), nil
		}
	}
	if reason := protocol.PreflightRestoreReplaceReason(req, powerState, originalExists, false); reason != "" {
		return failReplaceRun(store, cmd, observed, reason), nil
	}
	disks, err := engine.BackupDiskSources(req.OriginalVMID)
	if err != nil {
		return failReplaceRun(store, cmd, observed, err.Error()), nil
	}
	localDir := restoreStagingDir(store, cmd.OperationID)
	// Explicit refusal of writing restored images onto the live original paths.
	for _, disk := range disks {
		if filepath.Dir(disk.Source) == localDir {
			continue
		}
		// Any attempt to treat original Source as the restore destination is in-place overwrite.
		if err := refuseInPlaceOverwrite(disk.Source, localDir); err != nil {
			return failReplaceRun(store, cmd, observed, err.Error()), nil
		}
	}
	if err := backend.CopyArtifacts(ctx, req.RecoveryPointID, localDir, manifest); err != nil {
		return failReplaceRun(store, cmd, observed, err.Error()), nil
	}
	paths := map[string]string{}
	for _, disk := range disks {
		paths[disk.Target] = disk.Source
	}
	if err := writeRetainedOriginal(store, retainedOriginalRecord{
		OperationID: cmd.OperationID, OriginalVMID: req.OriginalVMID, DiskPaths: paths,
		RecoveryPointID: req.RecoveryPointID, CreatedAt: observed,
	}); err != nil {
		return failReplaceRun(store, cmd, observed, err.Error()), nil
	}
	// Cutover marker: exclusive ownership moves only after retained-original is durable.
	cutover := map[string]any{
		"operation_id": cmd.OperationID, "original_vm_id": req.OriginalVMID,
		"local_restore_dir": localDir, "stage": "cutover-ready", "exclusive": true,
	}
	raw, _ := json.Marshal(cutover)
	if err := vmhost.AtomicWrite(filepath.Join(store.dir, "backup-replace", cmd.OperationID, "cutover.json"), raw, 0600); err != nil {
		return failReplaceRun(store, cmd, observed, err.Error()), nil
	}
	spec, err := vmhost.BuildRestoreReplaceSpec(vm, *manifest.CapturedSpec, req)
	if err != nil {
		return failReplaceRun(store, cmd, observed, err.Error()), nil
	}
	intent, _ := backupIntentSHA256(cmd)
	if err := reserveReplacePoolOwner(ctx, store, cmd.OperationID, vm, spec); err != nil {
		return attentionReplaceRun(store, cmd, observed, "owner-reserve", err.Error()), nil
	}
	if err := applyExclusiveReplaceCutover(ctx, engine, cmd.OperationID, spec, localDir, manifest.Disks, manifest.Firmware...); err != nil {
		stage := vmhost.ReplaceCutoverStage(err)
		if stage == "" {
			stage = "cutover-ready"
		}
		status := "needs_attention"
		message := err.Error()
		if vmhost.IsReplaceCutoverAttention(err) {
			message = "restore replace cutover needs attention at " + stage + ": " + err.Error()
		}
		if settleErr := SettlePoolBackupRun(store, protocol.BackupRun{
			OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreReplace, PoolID: store.State.PoolID,
			VMID: req.OriginalVMID, RepositoryID: repo.ID, RecoveryPointID: req.RecoveryPointID,
			IntentSHA256: intent, Stage: stage, Status: status, Error: message,
			CreatedAt: observed, UpdatedAt: observed,
		}, nil); settleErr != nil {
			return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: settleErr.Error(), ObservedAt: observed}, nil
		}
		// The owner reservation remains pending until native evidence proves an outcome.
		return protocol.Result{OperationID: cmd.OperationID, Status: status, Error: message, ObservedAt: observed}, nil
	}
	if err := commitReplacePoolOwner(ctx, store, engine, cmd.OperationID, req.OriginalVMID); err != nil {
		return attentionReplaceRun(store, cmd, observed, "owner-commit", err.Error()), nil
	}
	if err := SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreReplace, PoolID: store.State.PoolID,
		VMID: req.OriginalVMID, RepositoryID: repo.ID, RecoveryPointID: req.RecoveryPointID,
		IntentSHA256: intent, Stage: "replaced", Status: "succeeded",
		CreatedAt: observed, UpdatedAt: observed,
	}, nil); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	_ = clearBackupRestorePin(store, req.RecoveryPointID, cmd.OperationID)
	removeRestoreStaging(store, cmd.OperationID)
	return protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: observed}, nil
}

func attentionReplaceRun(store *Store, cmd protocol.Command, observed time.Time, stage, message string) protocol.Result {
	intent, _ := backupIntentSHA256(cmd)
	_ = SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreReplace, PoolID: store.State.PoolID,
		VMID: cmd.RestoreReplace.OriginalVMID, RepositoryID: cmd.RestoreReplace.RepositoryID,
		RecoveryPointID: cmd.RestoreReplace.RecoveryPointID, IntentSHA256: intent,
		Stage: stage, Status: "needs_attention", Error: message, CreatedAt: observed, UpdatedAt: observed,
	}, nil)
	return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: message, ObservedAt: observed}
}

// reconcileRestoreReplaceAttention observes a retained native cutover without replaying it.
func reconcileRestoreReplaceAttention(ctx context.Context, dir string, identity State, cmd protocol.Command) (protocol.Result, error) {
	observed := time.Now().UTC()
	if cmd.Kind != protocol.BackupKindRestoreReplace || cmd.RestoreReplace == nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: "invalid replace reconciliation", ObservedAt: observed}, nil
	}
	store := &Store{dir: dir, State: identity}
	run, err := loadPoolBackupRun(store, cmd.OperationID)
	// A pending run here is one whose attention settlement did not persist.
	if err != nil || run.Status != "needs_attention" && run.Status != "pending" || run.Kind != protocol.BackupKindRestoreReplace {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: "replace run has no retained uncertain state", ObservedAt: observed}, nil
	}
	if replaceHasNoEffects(store, cmd.OperationID) {
		message := run.Error
		if message == "" {
			message = "restore replace stopped before it changed the original VM"
		}
		return failReplaceRun(store, cmd, observed, message), nil
	}
	repo, err := GetPoolBackupRepository(store, cmd.RestoreReplace.RepositoryID)
	if err == nil {
		_, _, err = validatedCatalogBackupManifest(ctx, store, repo, cmd.RestoreReplace.RecoveryPointID, run.ManifestSHA256)
	}
	if err != nil {
		return attentionReplaceRun(store, cmd, observed, run.Stage, err.Error()), nil
	}
	engine := vmhost.NewManaged(filepath.Join(dir, "managed-vms"))
	native, err := engine.ReconcileExclusiveReplaceCutover(ctx, cmd.OperationID)
	if err != nil || native.Status != "succeeded" {
		message := "native replace outcome is still unresolved"
		if err != nil {
			message = err.Error()
		} else if native.Error != "" {
			message = native.Error
		}
		return attentionReplaceRun(store, cmd, observed, run.Stage, message), nil
	}
	if err := commitReplacePoolOwner(ctx, store, engine, cmd.OperationID, cmd.RestoreReplace.OriginalVMID); err != nil {
		return attentionReplaceRun(store, cmd, observed, "owner-commit", err.Error()), nil
	}
	intent, _ := backupIntentSHA256(cmd)
	if err := SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreReplace, PoolID: store.State.PoolID,
		VMID: cmd.RestoreReplace.OriginalVMID, RepositoryID: cmd.RestoreReplace.RepositoryID,
		RecoveryPointID: cmd.RestoreReplace.RecoveryPointID, IntentSHA256: intent,
		Stage: "replaced", Status: "succeeded", CreatedAt: observed, UpdatedAt: observed,
	}, nil); err != nil {
		return attentionReplaceRun(store, cmd, observed, "owner-commit", err.Error()), nil
	}
	_ = clearBackupRestorePin(store, cmd.RestoreReplace.RecoveryPointID, cmd.OperationID)
	removeRestoreStaging(store, cmd.OperationID)
	return protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: observed}, nil
}

// The retained-original record is written before the owner reservation and
// cutover, so without it restore replace has not touched the original VM.
func replaceHasNoEffects(store *Store, operationID string) bool {
	_, err := os.Lstat(retainedOriginalPath(store, operationID))
	return errors.Is(err, os.ErrNotExist)
}

func refuseInPlaceOverwrite(originalPath, restoreDir string) error {
	if originalPath == "" || restoreDir == "" {
		return errors.New("restore replace paths are incomplete")
	}
	cleanOrig := filepath.Clean(originalPath)
	cleanRestore := filepath.Clean(restoreDir)
	if cleanOrig == cleanRestore || filepath.Dir(cleanOrig) == cleanRestore {
		return errors.New("refuse unsafe in-place overwrite; restore replace retains originals until exclusive cutover")
	}
	return nil
}

func failReplaceRun(store *Store, cmd protocol.Command, observed time.Time, message string) protocol.Result {
	intent, _ := backupIntentSHA256(cmd)
	if err := SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreReplace, PoolID: store.State.PoolID,
		VMID: cmd.RestoreReplace.OriginalVMID, RepositoryID: cmd.RestoreReplace.RepositoryID,
		RecoveryPointID: cmd.RestoreReplace.RecoveryPointID, IntentSHA256: intent,
		Stage: "failed", Status: "failed", Error: message, CreatedAt: observed, UpdatedAt: observed,
	}, nil); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}
	}
	_ = clearBackupRestorePin(store, cmd.RestoreReplace.RecoveryPointID, cmd.OperationID)
	removeRestoreStaging(store, cmd.OperationID)
	return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: message, ObservedAt: observed}
}
