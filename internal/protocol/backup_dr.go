// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"errors"
	"fmt"
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

// ForgetRepositoryRequest drops a repository and its recovery-point and run records from the
// pool; the destination's data is never touched. Confirmation is the repository name and the
// counts are what the operator was shown, so a catalog that changed since is refused.
type ForgetRepositoryRequest struct {
	RepositoryID    string `json:"repository_id"`
	Confirmation    string `json:"confirmation"`
	RecoveryPoints  int    `json:"recovery_points"`
	ProtectedPoints int    `json:"protected_points"`
}

func (r ForgetRepositoryRequest) Valid() error {
	if !ValidSnapshotOperationID(r.RepositoryID) || len(r.Confirmation) > 256 ||
		r.RecoveryPoints < 0 || r.ProtectedPoints < 0 || r.ProtectedPoints > r.RecoveryPoints {
		return errors.New("forget backup repository request is invalid")
	}
	return nil
}

// OrphanCleanupRequest deletes the recovery points of the chosen VMs, which are no longer in the
// pool's VM inventory, from a repository's destination and the catalog. Counts are the totals the
// operator was shown for the chosen VMs; Core refuses if they changed since. Confirmation is
// OrphanCleanupConfirmation for those counts. Protected points are deleted only with
// IncludeProtected.
type OrphanCleanupRequest struct {
	RepositoryID     string   `json:"repository_id"`
	VMIDs            []string `json:"vm_ids"`
	IncludeProtected bool     `json:"include_protected,omitempty"`
	Confirmation     string   `json:"confirmation"`
	RecoveryPoints   int      `json:"recovery_points"`
	ProtectedPoints  int      `json:"protected_points"`
}

func (r OrphanCleanupRequest) Valid() error {
	seen := map[string]bool{}
	for _, id := range r.VMIDs {
		if id == "" || len(id) > 128 || seen[id] {
			return errors.New("orphaned backup cleanup request is invalid")
		}
		seen[id] = true
	}
	if !ValidSnapshotOperationID(r.RepositoryID) || len(r.VMIDs) == 0 || len(r.VMIDs) > 1000 || len(r.Confirmation) > 256 ||
		r.RecoveryPoints < 1 || r.ProtectedPoints < 0 || r.ProtectedPoints > r.RecoveryPoints {
		return errors.New("orphaned backup cleanup request is invalid")
	}
	return nil
}

// OrphanCleanupConfirmation is the text the operator types; with protected points included it
// states how many.
func OrphanCleanupConfirmation(points, protected int, includeProtected bool) string {
	text := fmt.Sprintf("delete %d recovery points", points)
	if includeProtected {
		text += fmt.Sprintf(" including %d protected", protected)
	}
	return text
}

// BackupOrphanGroup is one missing VM's recovery points in a repository.
type BackupOrphanGroup struct {
	VMID              string    `json:"vm_id"`
	VMName            string    `json:"vm_name,omitempty"`
	RecoveryPoints    int       `json:"recovery_points"`
	ProtectedPoints   int       `json:"protected_points"`
	SizeBytes         int64     `json:"size_bytes"`
	SizeUnknownPoints int       `json:"size_unknown_points,omitempty"`
	Oldest            time.Time `json:"oldest"`
	Newest            time.Time `json:"newest"`
	// InUse: an unfinished restore or test restore pins a point, or a backup run on the
	// repository names the VM. Cleanup always skips those points.
	InUse bool `json:"in_use,omitempty"`
}

// BackupOrphanQuery lists a repository's recovery points whose VM is not in the master's VM inventory.
type BackupOrphanQuery struct {
	RepositoryID string              `json:"repository_id"`
	Groups       []BackupOrphanGroup `json:"groups"`
	Warnings     []string            `json:"warnings,omitempty"`
}

// BackupOrphanPointResult is one recovery point's outcome: pending, deleted, skipped or failed.
type BackupOrphanPointResult struct {
	RecoveryPointID string `json:"recovery_point_id"`
	VMID            string `json:"vm_id"`
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
}

// BackupOrphanCleanup is the master's durable record of one cleanup: the planned points and what
// happened to each. Status is running, succeeded or failed.
type BackupOrphanCleanup struct {
	OperationID string                    `json:"operation_id"`
	Request     OrphanCleanupRequest      `json:"request"`
	Status      string                    `json:"status"`
	Error       string                    `json:"error,omitempty"`
	Points      []BackupOrphanPointResult `json:"points"`
	UpdatedAt   time.Time                 `json:"updated_at"`
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
