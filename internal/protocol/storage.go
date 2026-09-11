// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const StorageRepositoryCapability = "storage-repositories-v1"
const StorageDiscoveryCapability = "storage-discovery-v1"

const (
	StorageKindLocal = "local"
	StorageKindNFS   = "nfs"
	StorageKindZFS   = "zfs"
	StorageKindSMB   = "smb"
	StorageKindISCSI = "iscsi"
)

type NFSStorageSource struct {
	Server    string `json:"server"`
	Export    string `json:"export"`
	Directory string `json:"directory,omitempty"`
}
type ZFSStorageSource struct {
	Pool      string `json:"pool"`
	Dataset   string `json:"dataset"`
	MountPath string `json:"mount_path"`
}
type SMBStorageSource struct {
	Server string `json:"server"`
	Share  string `json:"share"`
	// Directory is a validated existing subdirectory beneath Share, never an SMB path.
	Directory string `json:"directory,omitempty"`
}
type ISCSIStorageSource struct {
	Portal         string `json:"portal"`
	TargetIQN      string `json:"target_iqn"`
	LUN            uint64 `json:"lun"`
	WWID           string `json:"wwid"`
	FilesystemUUID string `json:"filesystem_uuid"`
	MountPath      string `json:"mount_path"`
	// ExclusiveTarget is the operator attestation required when Core cannot inspect the
	// target ACL. CHAP authenticates an initiator but does not prove exclusive write access.
	ExclusiveTarget bool `json:"exclusive_target"`
}

// StorageSource carries only typed, non-secret connection configuration. Local repositories
// retain the pre-v2 StorageRegistration.Path field so old Core journals remain readable.
type StorageSource struct {
	NFS   *NFSStorageSource   `json:"nfs,omitempty"`
	ZFS   *ZFSStorageSource   `json:"zfs,omitempty"`
	SMB   *SMBStorageSource   `json:"smb,omitempty"`
	ISCSI *ISCSIStorageSource `json:"iscsi,omitempty"`
}

type LocalStorageIdentity struct {
	FilesystemUUID string `json:"filesystem_uuid"`
}
type NFSStorageIdentity struct {
	Server       string `json:"server"`
	Export       string `json:"export"`
	FilesystemID string `json:"filesystem_id"`
}
type ZFSStorageIdentity struct {
	PoolGUID    string `json:"pool_guid"`
	DatasetGUID string `json:"dataset_guid"`
}
type SMBStorageIdentity struct {
	Server       string `json:"server"`
	Share        string `json:"share"`
	FilesystemID string `json:"filesystem_id"`
	// MarkerID is a Core-created durable marker inside the share. CIFS statfs identity is not
	// stable across reconnects, so MarkerID establishes that the same share remains mounted.
	MarkerID string `json:"marker_id,omitempty"`
}
type ISCSIStorageIdentity struct {
	TargetIQN      string `json:"target_iqn"`
	LUN            uint64 `json:"lun"`
	WWID           string `json:"wwid"`
	FilesystemUUID string `json:"filesystem_uuid"`
}

// StorageIdentity is observed and canonicalized by Core. It is how Core detects a replaced
// mount or an alias; a registration request may leave it empty until Core verifies the source.
type StorageIdentity struct {
	Local *LocalStorageIdentity `json:"local,omitempty"`
	NFS   *NFSStorageIdentity   `json:"nfs,omitempty"`
	ZFS   *ZFSStorageIdentity   `json:"zfs,omitempty"`
	SMB   *SMBStorageIdentity   `json:"smb,omitempty"`
	ISCSI *ISCSIStorageIdentity `json:"iscsi,omitempty"`
}

// String is a stable, secret-free identity comparison value for Core-to-Core access reports.
func (s StorageIdentity) String() string { b, _ := json.Marshal(s); return string(b) }

type StorageCapabilities struct {
	VMDisk          bool     `json:"vm_disk"`
	ISO             bool     `json:"iso"`
	Thin            bool     `json:"thin"`
	Thick           bool     `json:"thick"`
	Shared          bool     `json:"shared"`
	EligibleHostIDs []string `json:"eligible_host_ids,omitempty"`
}

type StorageAccess struct {
	Status              string    `json:"status"`
	Readable            bool      `json:"readable"`
	Writable            bool      `json:"writable"`
	TotalBytes          uint64    `json:"total_bytes,omitempty"`
	AvailableBytes      *uint64   `json:"available_bytes,omitempty"`
	EffectiveQuotaBytes *uint64   `json:"effective_quota_bytes,omitempty"`
	ObservedAt          time.Time `json:"observed_at,omitempty"`
	Error               string    `json:"error,omitempty"`
}

// StorageLifecycleRequest changes an existing repository. Register deliberately remains the
// legacy Command.Storage shape; that lets old local journals replay unchanged.
type StorageLifecycleRequest struct {
	Action             string               `json:"action"`
	RepositoryID       string               `json:"repository_id"`
	PoolID             string               `json:"pool_id"`
	HostID             string               `json:"host_id"`
	ExpectedGeneration uint64               `json:"expected_generation,omitempty"`
	Registration       *StorageRegistration `json:"registration,omitempty"`
	CredentialReceipt  string               `json:"credential_receipt,omitempty"`
}

// StorageCredentialOffer only grants an addressed Core the chance to redeem an opaque handle.
// It never contains credential bytes and is safe in the normal report response.
type StorageCredentialOffer struct {
	Handle                string    `json:"handle"`
	RequestID             string    `json:"request_id"`
	PoolID                string    `json:"pool_id"`
	HostID                string    `json:"host_id"`
	RepositoryID          string    `json:"repository_id,omitempty"`
	RegistrationRequestID string    `json:"registration_request_id,omitempty"`
	ExpiresAt             time.Time `json:"expires_at"`
}

// StorageCredentials appears only in the transient, authenticated credential handoff body.
// It must never be embedded in Command, a journal record, inventory, result, or diagnostic.
type StorageCredentials struct {
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret"`
}
type StorageCredentialHandoff struct {
	Offer       StorageCredentialOffer `json:"offer"`
	Credentials StorageCredentials     `json:"credentials"`
}
type StorageCredentialReceipt struct {
	Receipt               string    `json:"receipt"`
	RequestID             string    `json:"request_id"`
	PoolID                string    `json:"pool_id"`
	HostID                string    `json:"host_id"`
	RepositoryID          string    `json:"repository_id,omitempty"`
	RegistrationRequestID string    `json:"registration_request_id,omitempty"`
	Status                string    `json:"status"`
	Error                 string    `json:"error,omitempty"`
	RetireEligible        bool      `json:"retire_eligible,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

// StorageCredentialRetireRequest removes an unreferenced Core-local credential receipt. The
// receipt is opaque: this command never carries credential bytes or a password-derived value.
type StorageCredentialRetireRequest struct {
	Receipt string `json:"receipt"`
	PoolID  string `json:"pool_id"`
	HostID  string `json:"host_id"`
}

// StorageDiscoveryRequest is a short-lived Core observation request.  It is never a
// repository command: neither the Controller nor Core journals it, and credentials (if
// supplied for SMB/CHAP discovery) exist only for this one authenticated exchange.
type StorageDiscoveryRequest struct {
	RequestID   string              `json:"request_id"`
	PoolID      string              `json:"pool_id"`
	HostID      string              `json:"host_id"`
	Kind        string              `json:"kind"`
	Server      string              `json:"server,omitempty"`
	Path        string              `json:"path,omitempty"`
	Credentials *StorageCredentials `json:"credentials,omitempty"`
}

type StorageDiscoveryCandidate struct {
	Kind      string        `json:"kind"`
	Source    StorageSource `json:"source"`
	Path      string        `json:"path,omitempty"`
	Browsable bool          `json:"browsable"`
}

// StorageDiscoveryResult deliberately returns only operator-safe source metadata and
// directory names. Credentials, mount options, device paths and command output stay Core-local.
type StorageDiscoveryResult struct {
	RequestID  string                      `json:"request_id"`
	PoolID     string                      `json:"pool_id"`
	HostID     string                      `json:"host_id"`
	Status     string                      `json:"status"`
	Candidates []StorageDiscoveryCandidate `json:"candidates,omitempty"`
	Error      string                      `json:"error,omitempty"`
	ObservedAt time.Time                   `json:"observed_at"`
}

var storageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,253}$`)
var zfsName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*(/[A-Za-z0-9][A-Za-z0-9_.:-]*)*$`)
var storageUUID = regexp.MustCompile(`^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$`)
var iscsiWWID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,255}$`)
var storageOpaqueID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var storageMarkerID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func storagePath(raw string) bool {
	return filepath.IsAbs(raw) && raw != "/" && filepath.Clean(raw) == raw && !strings.ContainsAny(raw, "\x00\r\n")
}
func storageManagedMountPath(raw string) bool {
	return storagePath(raw) && !strings.ContainsAny(raw, "%\\") && !strings.ContainsFunc(raw, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}
func storageServer(raw string) bool {
	return raw != "" && len(raw) <= 253 && !strings.ContainsAny(raw, "/\\\x00 \t\r\n;'\"`$&|<>()")
}
func storageSubdirectory(raw string) bool {
	if raw == "" {
		return true
	}
	return !filepath.IsAbs(raw) && filepath.Clean(raw) == raw && raw != "." && raw != ".." && !strings.HasPrefix(raw, "../") && !strings.ContainsAny(raw, "\\\x00\r\n") && !strings.ContainsFunc(raw, unicode.IsSpace)
}

// ValidateStorageRegistration validates client-supplied intent as well as Core replay input.
// Empty Kind with only Path is the v1 local-directory representation and normalizes to local
// for callers through StorageKind; it is intentionally not rewritten in old journals.
func ValidateStorageRegistration(s StorageRegistration, allowEmptyID bool) error {
	return validateStorageRegistration(s, allowEmptyID, false)
}

// ValidateStoredStorageRegistration also accepts Core's resolved safe mount path for a
// remote source. The request validator above deliberately rejects it: Core chooses mount paths.
func ValidateStoredStorageRegistration(s StorageRegistration, allowEmptyID bool) error {
	return validateStorageRegistration(s, allowEmptyID, true)
}

func validateStorageRegistration(s StorageRegistration, allowEmptyID, allowResolvedPath bool) error {
	if !allowEmptyID && s.ID == "" {
		return errors.New("storage repository ID is required")
	}
	kind := s.StorageKind()
	if kind == "" {
		return errors.New("storage kind must be local, nfs, zfs, smb or iscsi")
	}
	if s.Tombstone {
		return errors.New("a tombstoned storage repository cannot be registered")
	}
	if !allowResolvedPath && s.Identity.count() != 0 {
		return errors.New("Core observes storage identity")
	}
	if s.Identity.SMB != nil && s.Identity.SMB.MarkerID != "" && !storageMarkerID.MatchString(s.Identity.SMB.MarkerID) {
		return errors.New("SMB storage marker ID must be 32 lowercase hexadecimal characters")
	}
	if s.Source.count() > 1 {
		return errors.New("storage source must select exactly one backend")
	}
	switch kind {
	case StorageKindLocal:
		if !storagePath(s.Path) || s.Source.count() != 0 {
			return errors.New("local storage needs one safe absolute path")
		}
	case StorageKindNFS:
		if s.Source.NFS == nil || !storageServer(s.Source.NFS.Server) || !storagePath(s.Source.NFS.Export) || !storageSubdirectory(s.Source.NFS.Directory) || (s.Path != "" && (!allowResolvedPath || !storagePath(s.Path))) {
			return errors.New("NFS storage needs a server and safe absolute export")
		}
	case StorageKindZFS:
		v := s.Source.ZFS
		if v == nil || !zfsName.MatchString(v.Pool) || !zfsName.MatchString(v.Dataset) || !storageManagedMountPath(v.MountPath) || (s.Path != "" && (!allowResolvedPath || !storagePath(s.Path))) {
			return errors.New("ZFS storage needs pool, dataset and safe absolute mount path")
		}
	case StorageKindSMB:
		v := s.Source.SMB
		if v == nil || !storageServer(v.Server) || !storageName.MatchString(v.Share) || !storageSubdirectory(v.Directory) || (s.Path != "" && (!allowResolvedPath || !storagePath(s.Path))) {
			return errors.New("SMB storage needs a server and share")
		}
	case StorageKindISCSI:
		v := s.Source.ISCSI
		if v == nil || !storageServer(v.Portal) || !(strings.HasPrefix(v.TargetIQN, "iqn.") || strings.HasPrefix(v.TargetIQN, "eui.") || strings.HasPrefix(v.TargetIQN, "naa.")) || strings.ContainsAny(v.TargetIQN, "/\\\x00 \t\r\n") || !iscsiWWID.MatchString(v.WWID) || !storageUUID.MatchString(v.FilesystemUUID) || !v.ExclusiveTarget {
			return errors.New("iSCSI storage needs portal, target IQN, LUN WWID, filesystem UUID and exclusive-target attestation")
		}
		if allowResolvedPath {
			if !storagePath(v.MountPath) || (s.Path != "" && (!storagePath(s.Path) || s.Path != v.MountPath)) {
				return errors.New("stored iSCSI storage needs Core's safe resolved mount path")
			}
		} else if v.MountPath != "" || s.Path != "" {
			return errors.New("Core chooses the iSCSI mount path")
		}
	default:
		return errors.New("storage kind must be local, nfs, zfs, smb or iscsi")
	}
	return nil
}

func (s StorageSource) count() int {
	n := 0
	if s.NFS != nil {
		n++
	}
	if s.ZFS != nil {
		n++
	}
	if s.SMB != nil {
		n++
	}
	if s.ISCSI != nil {
		n++
	}
	return n
}

func (s StorageIdentity) count() int {
	n := 0
	if s.Local != nil {
		n++
	}
	if s.NFS != nil {
		n++
	}
	if s.ZFS != nil {
		n++
	}
	if s.SMB != nil {
		n++
	}
	if s.ISCSI != nil {
		n++
	}
	return n
}

// StorageKind returns local for v1 path-only registrations; all v2 callers should persist Kind.
func (s StorageRegistration) StorageKind() string {
	if s.Kind == "" && s.Path != "" && s.Source.count() == 0 {
		return StorageKindLocal
	}
	return s.Kind
}

func ValidateStorageLifecycle(v StorageLifecycleRequest) error {
	if v.RepositoryID == "" || v.PoolID == "" || v.HostID == "" {
		return errors.New("storage lifecycle needs repository, pool and host IDs")
	}
	switch v.Action {
	case "update":
		if v.ExpectedGeneration == 0 || v.Registration == nil {
			return errors.New("storage update needs expected generation and registration")
		}
		if v.Registration.ID != "" && v.Registration.ID != v.RepositoryID {
			return errors.New("storage update repository ID does not match registration")
		}
		if err := ValidateStorageRegistration(*v.Registration, true); err != nil {
			return fmt.Errorf("storage update: %w", err)
		}
	case "recheck":
		if v.Registration != nil || v.CredentialReceipt != "" {
			return errors.New("storage recheck cannot change configuration or credentials")
		}
	case "detach":
		if v.ExpectedGeneration == 0 || v.Registration != nil || v.CredentialReceipt != "" {
			return errors.New("storage detach needs expected generation only")
		}
	default:
		return errors.New("storage lifecycle action must be update, recheck or detach")
	}
	return nil
}

func ValidateStorageCredentialRetire(v StorageCredentialRetireRequest) error {
	if !storageOpaqueID.MatchString(v.Receipt) || !storageOpaqueID.MatchString(v.PoolID) || !storageOpaqueID.MatchString(v.HostID) {
		return errors.New("credential retirement needs valid receipt, pool and host IDs")
	}
	return nil
}

func ValidateStorageDiscovery(v StorageDiscoveryRequest) error {
	if !storageOpaqueID.MatchString(v.RequestID) || !storageOpaqueID.MatchString(v.PoolID) || !storageOpaqueID.MatchString(v.HostID) {
		return errors.New("storage discovery needs valid request, pool and host IDs")
	}
	if v.Kind != StorageKindLocal && v.Kind != StorageKindNFS && v.Kind != StorageKindZFS && v.Kind != StorageKindSMB && v.Kind != StorageKindISCSI {
		return errors.New("storage discovery kind must be local, nfs, zfs, smb or iscsi")
	}
	if v.Server != "" && !storageServer(v.Server) {
		return errors.New("storage discovery server is invalid")
	}
	if v.Path != "" && !storagePath(v.Path) {
		return errors.New("storage discovery path is invalid")
	}
	if v.Credentials != nil && (len(v.Credentials.Secret) > 4096 || len(v.Credentials.Username) > 253 || strings.ContainsAny(v.Credentials.Username+v.Credentials.Secret, "\x00\r\n")) {
		return errors.New("storage discovery credentials are invalid")
	}
	if v.Kind == StorageKindSMB && v.Server == "" {
		return errors.New("SMB discovery needs a server")
	}
	if v.Kind == StorageKindISCSI && v.Server == "" {
		return errors.New("iSCSI discovery needs a portal")
	}
	return nil
}
