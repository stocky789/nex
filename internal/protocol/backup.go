// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	BackupManifestVersion = 1
	BackupCaptureStopped  = "stopped"
	// BackupCaptureOnline is crash-consistent online full backup (libvirt push-mode).
	BackupCaptureOnline   = "crash"
	BackupCaptureQuiesced = "quiesced"
	BackupKindBackupNow   = "backup-now"
	BackupKindRestoreNew  = "backup-restore-as-new"
	BackupRepoKindNFS     = "nfs"
)

// ValidBackupConsistency reports whether consistency is a known backup capture mode.
func ValidBackupConsistency(consistency string) bool {
	switch consistency {
	case BackupCaptureStopped, BackupCaptureOnline, BackupCaptureQuiesced:
		return true
	default:
		return false
	}
}

// BackupConsistencyRunning is true for online crash-consistent or quiesced capture.
func BackupConsistencyRunning(consistency string) bool {
	return consistency == BackupCaptureOnline || consistency == BackupCaptureQuiesced
}

// BackupRepository is the master-owned NFS backup destination registration.
// Destination kind is external NFS only for product registration.
type BackupRepository struct {
	ID         string              `json:"id"`
	PoolID     string              `json:"pool_id"`
	Name       string              `json:"name"`
	Kind       string              `json:"kind"`
	Source     NFSStorageSource    `json:"source"`
	Identity   *NFSStorageIdentity `json:"identity,omitempty"`
	Generation uint64              `json:"generation,omitempty"`
	MountPath  string              `json:"mount_path,omitempty"`
	Access     StorageAccess       `json:"access,omitempty"`
	// MemberAccess is master-reported per-host observed write/restore access.
	MemberAccess []PoolStorageAccess `json:"member_access,omitempty"`
	CreatedAt    time.Time           `json:"created_at,omitempty"`
	UpdatedAt    time.Time           `json:"updated_at,omitempty"`
}

func (r BackupRepository) Valid() error {
	if !ValidSnapshotOperationID(r.ID) || (r.PoolID != "" && len(r.PoolID) > 128) ||
		!ValidResourceName(r.Name) || r.Kind != BackupRepoKindNFS {
		return errors.New("backup repository identity is invalid")
	}
	if strings.TrimSpace(r.Source.Server) == "" || strings.TrimSpace(r.Source.Export) == "" ||
		strings.ContainsAny(r.Source.Server, "/\\\x00") || strings.ContainsRune(r.Source.Export, '\x00') ||
		(r.Source.Directory != "" && (filepath.IsAbs(r.Source.Directory) || strings.Contains(r.Source.Directory, "..") ||
			strings.ContainsRune(r.Source.Directory, '\x00'))) {
		return errors.New("backup repository NFS source is invalid")
	}
	if r.MountPath != "" && (!filepath.IsAbs(r.MountPath) || filepath.Clean(r.MountPath) != r.MountPath) {
		return errors.New("backup repository mount path is invalid")
	}
	return nil
}

// BackupDiskArtifact describes one complete standalone disk image in a recovery point.
type BackupDiskArtifact struct {
	Target   string `json:"target"`
	Filename string `json:"filename"`
	Format   string `json:"format"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

func (d BackupDiskArtifact) Valid() error {
	if d.Target == "" || len(d.Target) > 16 || d.Format != "qcow2" || d.Size < 0 ||
		!lowerSHA256(d.SHA256) || !safeBackupRelativePath(d.Filename) {
		return errors.New("backup disk artifact is invalid")
	}
	return nil
}

// BackupManifest is the versioned recovery-point descriptor written to the repository.
// Incomplete or corrupt manifests are never restorable. SpecSHA256 alone is insufficient
// for restore: CapturedSpec holds the VM definition used to register a restored guest.
type BackupManifest struct {
	Version            int                      `json:"version"`
	RecoveryPointID    string                   `json:"recovery_point_id"`
	RepositoryID       string                   `json:"repository_id"`
	PoolID             string                   `json:"pool_id"`
	SourceVMID         string                   `json:"source_vm_id"`
	SourceVMName       string                   `json:"source_vm_name,omitempty"`
	CapturedAt         time.Time                `json:"captured_at"`
	Consistency        string                   `json:"consistency"`
	SpecSHA256         string                   `json:"spec_sha256"`
	DiskIdentitySHA256 string                   `json:"disk_identity_sha256"`
	CapturedSpec       *VMCreateSpec            `json:"captured_spec,omitempty"`
	Disks              []BackupDiskArtifact     `json:"disks"`
	Firmware           []BackupFirmwareArtifact `json:"firmware,omitempty"`
	Complete           bool                     `json:"complete"`
	Success            int                      `json:"success"`
}

// BackupFirmwareArtifact names a captured firmware payload relative to the recovery point.
type BackupFirmwareArtifact struct {
	Kind     string `json:"kind"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

func (f BackupFirmwareArtifact) Valid() error {
	if (f.Kind != "nvram" && f.Kind != "swtpm") || !safeBackupRelativePath(f.Filename) ||
		!lowerSHA256(f.SHA256) || f.Size < 0 {
		return errors.New("backup firmware artifact is invalid")
	}
	return nil
}

func (m BackupManifest) Valid() error {
	if m.Version != BackupManifestVersion || !ValidSnapshotOperationID(m.RecoveryPointID) ||
		!ValidSnapshotOperationID(m.RepositoryID) || m.PoolID == "" || m.SourceVMID == "" ||
		m.CapturedAt.IsZero() || !ValidBackupConsistency(m.Consistency) ||
		!lowerSHA256(m.SpecSHA256) || !lowerSHA256(m.DiskIdentitySHA256) ||
		len(m.Disks) == 0 || len(m.Disks) > 32 {
		return errors.New("backup manifest identity is invalid")
	}
	if !m.Complete || m.Success != 1 {
		return errors.New("backup manifest is incomplete")
	}
	if m.CapturedSpec != nil {
		if m.CapturedSpec.ID != m.SourceVMID {
			return errors.New("backup manifest captured spec identity is invalid")
		}
		if err := ValidateVMSpec(*m.CapturedSpec); err != nil {
			return errors.New("backup manifest captured spec is invalid")
		}
		raw, err := json.Marshal(m.CapturedSpec)
		if err != nil {
			return errors.New("backup manifest captured spec is invalid")
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != m.SpecSHA256 {
			return errors.New("backup manifest captured spec digest does not match")
		}
	}
	seen := map[string]bool{}
	for _, disk := range m.Disks {
		if disk.Valid() != nil || seen[disk.Target] || seen[disk.Filename] {
			return errors.New("backup manifest disk set is invalid")
		}
		seen[disk.Target], seen[disk.Filename] = true, true
	}
	seenFW := map[string]bool{}
	for _, fw := range m.Firmware {
		if fw.Valid() != nil || seenFW[fw.Kind] || seen[fw.Filename] {
			return errors.New("backup manifest firmware set is invalid")
		}
		seenFW[fw.Kind], seen[fw.Filename] = true, true
	}
	return nil
}

func safeBackupRelativePath(name string) bool {
	return name != "" && utf8.ValidString(name) && len(name) <= 200 &&
		!filepath.IsAbs(name) && filepath.Clean(name) == name && name != ".." &&
		!strings.HasPrefix(name, ".."+string(filepath.Separator)) &&
		!strings.ContainsRune(name, '\x00') && !strings.ContainsAny(name, `/\`)
}

// BackupRecoveryPoint is the master catalog index entry for one completed recovery point.
// The repository remains authoritative for the payload; the catalog never invents completion.
type BackupRecoveryPoint struct {
	ID             string    `json:"id"`
	RepositoryID   string    `json:"repository_id"`
	PoolID         string    `json:"pool_id"`
	SourceVMID     string    `json:"source_vm_id"`
	SourceVMName   string    `json:"source_vm_name,omitempty"`
	CapturedAt     time.Time `json:"captured_at"`
	Consistency    string    `json:"consistency"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	Protected      bool      `json:"protected,omitempty"`
	LastTestResult string    `json:"last_test_result,omitempty"`
	LastTestedAt   time.Time `json:"last_tested_at,omitempty"`
	LastTestVMID   string    `json:"last_test_vm_id,omitempty"`
}

func (p BackupRecoveryPoint) Valid() error {
	if !ValidSnapshotOperationID(p.ID) || !ValidSnapshotOperationID(p.RepositoryID) ||
		p.PoolID == "" || p.SourceVMID == "" || p.CapturedAt.IsZero() ||
		!ValidBackupConsistency(p.Consistency) || !lowerSHA256(p.ManifestSHA256) {
		return errors.New("backup recovery point is invalid")
	}
	switch p.LastTestResult {
	case "", BackupTestResultPassed, BackupTestResultFailed:
	default:
		return errors.New("backup recovery point test result is invalid")
	}
	if p.LastTestVMID != "" && len(p.LastTestVMID) > 128 {
		return errors.New("backup recovery point test VM id is invalid")
	}
	return nil
}

// BackupNowRequest starts a full backup of one VM into a registered repository.
// Omitted consistency means stopped. Online uses crash (crash-consistent) or quiesced.
type BackupNowRequest struct {
	RepositoryID string `json:"repository_id"`
	Consistency  string `json:"consistency,omitempty"`
}

func (r BackupNowRequest) Valid() error {
	if !ValidSnapshotOperationID(r.RepositoryID) {
		return errors.New("backup-now repository is invalid")
	}
	switch r.Consistency {
	case "", BackupCaptureStopped, BackupCaptureOnline, BackupCaptureQuiesced:
		return nil
	default:
		return errors.New("backup-now consistency must be stopped, crash, or quiesced")
	}
}

// EffectiveConsistency returns the capture mode used when Consistency is omitted.
func (r BackupNowRequest) EffectiveConsistency() string {
	if r.Consistency == "" {
		return BackupCaptureStopped
	}
	return r.Consistency
}

// RestoreAsNewRequest restores a recovery point as a new stopped VM from local copies.
type RestoreAsNewRequest struct {
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	NewVMID         string `json:"new_vm_id"`
	NewVMName       string `json:"new_vm_name"`
	StorageID       string `json:"storage_id"`
	HostID          string `json:"host_id"`
}

func (r RestoreAsNewRequest) Valid() error {
	if !ValidSnapshotOperationID(r.RepositoryID) || !ValidSnapshotOperationID(r.RecoveryPointID) ||
		!storageUUID.MatchString(r.NewVMID) || r.NewVMID != strings.ToLower(r.NewVMID) || !ValidResourceName(r.NewVMName) ||
		r.StorageID == "" || len(r.StorageID) > 128 || len(r.HostID) > 128 {
		return errors.New("restore-as-new request is invalid")
	}
	return nil
}

// BackupRun is the master-owned durable per-operation record under pool-backup-runs/.
type BackupRun struct {
	OperationID     string           `json:"operation_id"`
	Kind            string           `json:"kind"`
	PoolID          string           `json:"pool_id"`
	VMID            string           `json:"vm_id,omitempty"`
	HostID          string           `json:"host_id,omitempty"`
	RepositoryID    string           `json:"repository_id"`
	RecoveryPointID string           `json:"recovery_point_id,omitempty"`
	NewVMID         string           `json:"new_vm_id,omitempty"`
	PolicyID        string           `json:"policy_id,omitempty"`
	ScheduleID      string           `json:"schedule_id,omitempty"`
	OccurrenceID    string           `json:"occurrence_id,omitempty"`
	PolicyRevision  uint64           `json:"policy_revision,omitempty"`
	CaptureMode     string           `json:"capture_mode,omitempty"`
	Retention       *BackupRetention `json:"retention,omitempty"`
	OccurrenceAt    time.Time        `json:"occurrence_at,omitempty"`
	Stage           string           `json:"stage"`
	Status          string           `json:"status"`
	Error           string           `json:"error,omitempty"`
	IntentSHA256    string           `json:"intent_sha256,omitempty"`
	ManifestSHA256  string           `json:"manifest_sha256,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// BackupMetadataReceipt preserves the outcome of one Core metadata mutation
// across master promotion so the same operation ID can be retried exactly.
type BackupMetadataReceipt struct {
	OperationID  string    `json:"operation_id"`
	Kind         string    `json:"kind"`
	IntentSHA256 string    `json:"intent_sha256"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	ObservedAt   time.Time `json:"observed_at"`
}

func (r BackupMetadataReceipt) Valid() error {
	if !ValidSnapshotOperationID(r.OperationID) || r.Kind == "" || len(r.Kind) > 128 ||
		!lowerSHA256(r.IntentSHA256) || !backupRunStatusOK(r.Status) ||
		len(r.Error) > 4096 || r.ObservedAt.IsZero() {
		return errors.New("backup metadata receipt is invalid")
	}
	return nil
}

func backupRunKindOK(kind string) bool {
	return kind == BackupKindBackupNow || kind == BackupKindRestoreNew ||
		kind == BackupKindRestoreReplace || kind == BackupKindTestRestore ||
		kind == BackupKindScheduled || kind == BackupKindPolicyRunNow
}

func backupRunStatusOK(status string) bool {
	switch status {
	case "pending", "succeeded", "failed", "needs_attention", "missed", "skipped_overlap":
		return true
	default:
		return false
	}
}

func (r BackupRun) Valid() error {
	if !ValidSnapshotOperationID(r.OperationID) || !backupRunKindOK(r.Kind) ||
		r.PoolID == "" || !ValidSnapshotOperationID(r.RepositoryID) || len(r.HostID) > 128 || r.Stage == "" ||
		!backupRunStatusOK(r.Status) || r.CreatedAt.IsZero() {
		return errors.New("backup run record is invalid")
	}
	if r.IntentSHA256 != "" && !lowerSHA256(r.IntentSHA256) {
		return errors.New("backup run intent digest is invalid")
	}
	if r.ManifestSHA256 != "" && !lowerSHA256(r.ManifestSHA256) {
		return errors.New("backup run manifest digest is invalid")
	}
	if r.CaptureMode != "" && !ValidBackupConsistency(r.CaptureMode) {
		return errors.New("backup run capture mode is invalid")
	}
	if r.Retention != nil {
		if err := r.Retention.Valid(); err != nil {
			return err
		}
	}
	if r.OccurrenceID != "" && !ValidSnapshotOperationID(r.OccurrenceID) {
		return errors.New("backup run occurrence id is invalid")
	}
	if r.PolicyID != "" && !ValidSnapshotOperationID(r.PolicyID) {
		return errors.New("backup run policy id is invalid")
	}
	if r.ScheduleID != "" && !ValidSnapshotOperationID(r.ScheduleID) {
		return errors.New("backup run schedule id is invalid")
	}
	if (r.Kind == BackupKindScheduled || r.Kind == BackupKindPolicyRunNow) && r.RepositoryID == "" {
		return errors.New("scheduled backup run requires a repository")
	}
	return nil
}
