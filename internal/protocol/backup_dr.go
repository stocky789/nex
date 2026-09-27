// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"errors"
	"time"
)

const (
	BackupKindRestoreReplace = "backup-restore-replace"
	BackupKindTestRestore    = "backup-test-restore"
	BackupTestResultPassed   = "passed"
	BackupTestResultFailed   = "failed"
	BackupPinKindReplace     = "restore-replace"
	BackupPinKindNew         = "restore-as-new"
	BackupPinKindTest        = "test-restore"
)

// RestoreReplaceRequest restores a recovery point over an existing VM identity.
// Distinct from restore-as-new: requires explicit ConfirmReplace, retains the original
// disk set until exclusive cutover, and refuses unsafe in-place overwrite.
type RestoreReplaceRequest struct {
	HostID          string `json:"host_id,omitempty"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	OriginalVMID    string `json:"original_vm_id"`
	StorageID       string `json:"storage_id"`
	ConfirmReplace  bool   `json:"confirm_replace"`
}

func (r RestoreReplaceRequest) Valid() error {
	if (r.HostID != "" && !ValidSnapshotOperationID(r.HostID)) ||
		!ValidSnapshotOperationID(r.RepositoryID) || !ValidSnapshotOperationID(r.RecoveryPointID) ||
		r.OriginalVMID == "" || len(r.OriginalVMID) > 128 || r.StorageID == "" || len(r.StorageID) > 128 {
		return errors.New("restore-replace request is invalid")
	}
	if !r.ConfirmReplace {
		return errors.New("restore-replace requires confirm_replace")
	}
	return nil
}

// ProtectRecoveryPointRequest sets or clears deletion protection on a catalog point.
// Protection is also written beside the repository payload so it survives catalog rebuild.
type ProtectRecoveryPointRequest struct {
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	Protected       bool   `json:"protected"`
}

func (r ProtectRecoveryPointRequest) Valid() error {
	if !ValidSnapshotOperationID(r.RepositoryID) || !ValidSnapshotOperationID(r.RecoveryPointID) {
		return errors.New("protect recovery point request is invalid")
	}
	return nil
}

// TestRestoreRequest restores as a new VM marked for verification and later cleanup.
type TestRestoreRequest struct {
	HostID          string `json:"host_id,omitempty"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	NewVMID         string `json:"new_vm_id"`
	NewVMName       string `json:"new_vm_name"`
	StorageID       string `json:"storage_id"`
}

func (r TestRestoreRequest) Valid() error {
	return (RestoreAsNewRequest{
		HostID:       r.HostID,
		RepositoryID: r.RepositoryID, RecoveryPointID: r.RecoveryPointID,
		NewVMID: r.NewVMID, NewVMName: r.NewVMName, StorageID: r.StorageID,
	}).Valid()
}

func (r TestRestoreRequest) AsNew() RestoreAsNewRequest {
	return RestoreAsNewRequest{
		HostID:       r.HostID,
		RepositoryID: r.RepositoryID, RecoveryPointID: r.RecoveryPointID,
		NewVMID: r.NewVMID, NewVMName: r.NewVMName, StorageID: r.StorageID,
	}
}

// TestRestoreVerifyRequest records the operator verification result in Core.
type TestRestoreVerifyRequest struct {
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	TestVMID        string `json:"test_vm_id"`
	Result          string `json:"result"`
}

func (r TestRestoreVerifyRequest) Valid() error {
	if !ValidSnapshotOperationID(r.RepositoryID) || !ValidSnapshotOperationID(r.RecoveryPointID) ||
		r.TestVMID == "" || len(r.TestVMID) > 128 {
		return errors.New("test-restore verify request is invalid")
	}
	if r.Result != BackupTestResultPassed && r.Result != BackupTestResultFailed {
		return errors.New("test-restore result must be passed or failed")
	}
	return nil
}

// TestRestoreCleanupRequest removes only the selected test VM / staging for that test.
type TestRestoreCleanupRequest struct {
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	TestVMID        string `json:"test_vm_id"`
}

func (r TestRestoreCleanupRequest) Valid() error {
	if !ValidSnapshotOperationID(r.RepositoryID) || !ValidSnapshotOperationID(r.RecoveryPointID) ||
		r.TestVMID == "" || len(r.TestVMID) > 128 {
		return errors.New("test-restore cleanup request is invalid")
	}
	return nil
}

// CatalogImportRequest scans one repository and imports validated manifests into the catalog.
type CatalogImportRequest struct {
	RepositoryID string `json:"repository_id"`
}

func (r CatalogImportRequest) Valid() error {
	if !ValidSnapshotOperationID(r.RepositoryID) {
		return errors.New("catalog import repository is invalid")
	}
	return nil
}

// BackupRestorePin prevents prune while a restore uses the recovery point.
// Durable on the pool master; survives restart and catalog rebuild.
type BackupRestorePin struct {
	RecoveryPointID string    `json:"recovery_point_id"`
	OperationID     string    `json:"operation_id"`
	Kind            string    `json:"kind"`
	CreatedAt       time.Time `json:"created_at"`
}

func (p BackupRestorePin) Valid() error {
	if !ValidSnapshotOperationID(p.RecoveryPointID) || !ValidSnapshotOperationID(p.OperationID) ||
		p.CreatedAt.IsZero() {
		return errors.New("backup restore pin is invalid")
	}
	switch p.Kind {
	case BackupPinKindReplace, BackupPinKindNew, BackupPinKindTest:
		return nil
	default:
		return errors.New("backup restore pin kind is invalid")
	}
}

// PreflightRestoreReplaceReason returns a refusal reason, or empty when replace may proceed.
// Unsafe in-place overwrite (running guest, missing confirmation, identity mismatch) is refused.
func PreflightRestoreReplaceReason(req RestoreReplaceRequest, originalPowerState string, originalExists bool, inPlaceOverwrite bool) string {
	if err := req.Valid(); err != nil {
		return err.Error()
	}
	if !originalExists {
		return "original VM is not defined on this host; use restore as new"
	}
	if originalPowerState != "" && originalPowerState != "shut off" {
		return "original VM must be shut off before replace; refuse unsafe in-place overwrite while powered"
	}
	if inPlaceOverwrite {
		return "refuse unsafe in-place overwrite; restore replace retains originals until exclusive cutover"
	}
	return ""
}
