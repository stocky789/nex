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
const ZFSProvisionCapability = "storage-zfs-provision-v1"

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
	// Mode is exclusive (the legacy/default single-writer profile) or clustered_gfs2.
	// A clustered LUN is an already-formatted GFS2 filesystem; Core never formats it.
	Mode            string   `json:"mode,omitempty"`
	ClusterID       string   `json:"cluster_id,omitempty"`
	LockTable       string   `json:"lock_table,omitempty"`
	EligibleHostIDs []string `json:"eligible_host_ids,omitempty"`
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
	FilesystemType string `json:"filesystem_type,omitempty"`
	GFS2LockTable  string `json:"gfs2_lock_table,omitempty"`
	GFS2Journals   uint32 `json:"gfs2_journals,omitempty"`
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
	Username      string `json:"username,omitempty"`
	Secret        string `json:"secret,omitempty"`
	Domain        string `json:"domain,omitempty"`
	SSHPrivateKey string `json:"ssh_private_key,omitempty"`
	SSHKnownHosts string `json:"ssh_known_hosts,omitempty"`
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
	// Existing is true when Core observed a leftover or current Nex repository
	// marker in that folder. It is advisory for picking; registration still
	// verifies identity and access.
	Existing bool       `json:"existing,omitempty"`
	Device   *ZFSDevice `json:"device,omitempty"`
}

type ZFSDevice struct {
	ByID        string `json:"by_id"`
	Model       string `json:"model,omitempty"`
	Serial      string `json:"serial,omitempty"`
	SizeBytes   uint64 `json:"size_bytes"`
	Rotational  bool   `json:"rotational"`
	Eligibility string `json:"eligibility"`
	Fingerprint string `json:"fingerprint"`
}

type ZFSProvisionDevice struct {
	ByID              string `json:"by_id"`
	Serial            string `json:"serial"`
	SizeBytes         uint64 `json:"size_bytes"`
	EraseConfirmation string `json:"erase_confirmation,omitempty"`
	Fingerprint       string `json:"fingerprint"`
}

type ZFSProvisionHost struct {
	HostID  string               `json:"host_id"`
	Layout  string               `json:"layout"`
	Devices []ZFSProvisionDevice `json:"devices"`
}

// Repository identity is deliberately absent: Nex Core assigns it while
// durably accepting this master-owned request.
type ZFSProvisionRequest struct {
	// RepositoryID is empty for a new repository. A later pool-scoped host
	// configuration names an existing Core-owned identity only.
	RepositoryID string             `json:"repository_id,omitempty"`
	Name         string             `json:"name"`
	PoolID       string             `json:"pool_id"`
	Scope        string             `json:"scope"`
	Hosts        []ZFSProvisionHost `json:"hosts"`
}

type ZFSProvisionHostResult struct {
	RepositoryID string               `json:"repository_id"`
	PoolID       string               `json:"pool_id"`
	Name         string               `json:"name"`
	Scope        string               `json:"scope"`
	HostID       string               `json:"host_id"`
	Layout       string               `json:"layout"`
	Devices      []ZFSProvisionDevice `json:"devices"`
	Status       string               `json:"status"`
	Error        string               `json:"error,omitempty"`
	PoolGUID     string               `json:"pool_guid,omitempty"`
	DatasetGUID  string               `json:"dataset_guid,omitempty"`
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

var zfsName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*(/[A-Za-z0-9][A-Za-z0-9_.:-]*)*$`)
var storageSMBShareName = regexp.MustCompile(`^[\pL\pN][\pL\pN ._$:()\-]{0,253}$`)
var storageUUID = regexp.MustCompile(`^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$`)
var iscsiWWID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,255}$`)
var storageOpaqueID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var storageMarkerID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var zfsDeviceFingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)
var storageLockTable = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

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
	return !filepath.IsAbs(raw) && filepath.Clean(raw) == raw && raw != "." && raw != ".." && !strings.HasPrefix(raw, "../") && !strings.ContainsAny(raw, "\\\x00\r\n") && !strings.ContainsFunc(raw, unicode.IsControl)
}
func storageSMBShare(raw string) bool {
	return strings.TrimSpace(raw) == raw && storageSMBShareName.MatchString(raw)
}

func storageSMBDomain(raw string) bool {
	if raw == "" {
		return true
	}
	return len(raw) <= 253 && strings.TrimSpace(raw) == raw && !strings.ContainsAny(raw, "/\\= \t\x00\r\n;'\"`$&|<>()")
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
		if v == nil || !storageServer(v.Server) || !storageSMBShare(v.Share) || !storageSubdirectory(v.Directory) || (s.Path != "" && (!allowResolvedPath || !storagePath(s.Path))) {
			return errors.New("SMB storage needs a server and share")
		}
	case StorageKindISCSI:
		v := s.Source.ISCSI
		if v == nil || !storageServer(v.Portal) || !(strings.HasPrefix(v.TargetIQN, "iqn.") || strings.HasPrefix(v.TargetIQN, "eui.") || strings.HasPrefix(v.TargetIQN, "naa.")) || strings.ContainsAny(v.TargetIQN, "/\\\x00 \t\r\n") || !iscsiWWID.MatchString(v.WWID) || !storageUUID.MatchString(v.FilesystemUUID) {
			return errors.New("iSCSI storage needs portal, target IQN, LUN WWID and filesystem UUID")
		}
		if IsClusteredISCSI(s) {
			if v.ExclusiveTarget || !validEligibleHosts(v.EligibleHostIDs) || (allowResolvedPath && (!storageOpaqueID.MatchString(v.ClusterID) || !storageLockTable.MatchString(v.LockTable))) || (!allowResolvedPath && ((v.ClusterID != "" && !storageOpaqueID.MatchString(v.ClusterID)) || (v.LockTable != "" && !storageLockTable.MatchString(v.LockTable)))) {
				return errors.New("clustered iSCSI needs cluster ID, lock table and three to five eligible hosts")
			}
		} else if v.Mode != "" && v.Mode != "exclusive" {
			return errors.New("iSCSI mode must be exclusive or clustered_gfs2")
		} else if !v.ExclusiveTarget || v.ClusterID != "" || v.LockTable != "" || len(v.EligibleHostIDs) != 0 {
			return errors.New("exclusive iSCSI needs exclusive-target attestation only")
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

// IsPoolSharedStorage identifies source types that may legitimately be prepared on
// more than one Core. It is not a VM execution lease: callers still authorize a
// single writable VM owner through the pool master.
func IsPoolSharedStorage(s StorageRegistration) bool {
	return s.StorageKind() == StorageKindNFS || s.StorageKind() == StorageKindSMB || IsClusteredISCSI(s)
}

// StorageBlocksPoolJoin is the one rule both Core's join precheck and the Controller's
// join-candidate preview apply to an applied registration, so the preview and the host
// cannot disagree. A repository the host has detached projects as a tombstone — no kind, no
// path, so StorageKind() is "" rather than local — and a detached repository is not existing
// remote storage: it blocks nothing.
func StorageBlocksPoolJoin(s StorageRegistration) bool {
	return !s.Tombstone && s.StorageKind() != StorageKindLocal
}

func IsClusteredISCSI(s StorageRegistration) bool {
	return s.StorageKind() == StorageKindISCSI && s.Source.ISCSI != nil && s.Source.ISCSI.Mode == "clustered_gfs2"
}

func validEligibleHosts(hosts []string) bool {
	if len(hosts) < 3 || len(hosts) > 5 {
		return false
	}
	seen := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		if !storageOpaqueID.MatchString(host) || seen[host] {
			return false
		}
		seen[host] = true
	}
	return true
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
	if v.Kind != StorageKindLocal && v.Kind != StorageKindNFS && v.Kind != StorageKindZFS && v.Kind != StorageKindSMB && v.Kind != StorageKindISCSI && v.Kind != "zfs-device" {
		return errors.New("storage discovery kind must be local, nfs, zfs, smb, iscsi or zfs-device")
	}
	if v.Server != "" && !storageServer(v.Server) {
		return errors.New("storage discovery server is invalid")
	}
	if v.Path != "" && !storagePath(v.Path) {
		return errors.New("storage discovery path is invalid")
	}
	if v.Credentials != nil && (len(v.Credentials.Secret) > 4096 || len(v.Credentials.Username) > 253 || !storageSMBDomain(v.Credentials.Domain) || strings.ContainsAny(v.Credentials.Username+v.Credentials.Secret+v.Credentials.Domain, "\x00\r\n")) {
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

func ValidateZFSProvision(v ZFSProvisionRequest) error {
	if !storageOpaqueID.MatchString(v.PoolID) || (v.RepositoryID != "" && !storageOpaqueID.MatchString(v.RepositoryID)) || strings.TrimSpace(v.Name) == "" || len(v.Name) > 128 || (v.Scope != "host" && v.Scope != "pool") || len(v.Hosts) == 0 {
		return errors.New("ZFS provision needs name, pool scope and hosts")
	}
	seen := map[string]bool{}
	for _, host := range v.Hosts {
		if !storageOpaqueID.MatchString(host.HostID) || seen[host.HostID] || !ValidZFSLayout(ZFSLayout(host.Layout, len(host.Devices)), len(host.Devices)) {
			return errors.New("ZFS provision host or layout is invalid")
		}
		seen[host.HostID] = true
		byID := map[string]bool{}
		for _, device := range host.Devices {
			if !strings.HasPrefix(device.ByID, "/dev/disk/by-id/") || strings.ContainsAny(device.ByID, "\x00\r\n") || device.Serial == "" || device.SizeBytes == 0 || !zfsDeviceFingerprint.MatchString(device.Fingerprint) || byID[device.ByID] {
				return errors.New("ZFS provision device is invalid")
			}
			byID[device.ByID] = true
			if device.EraseConfirmation != "" && device.EraseConfirmation != device.ByID {
				return errors.New("ZFS erase confirmation must name the selected device")
			}
		}
	}
	if v.Scope == "host" && len(v.Hosts) != 1 {
		return errors.New("host-scoped ZFS provision needs one host")
	}
	return nil
}

// ValidZFSLayout reports whether an explicit layout can be built from exactly
// this many devices. An empty layout is not a layout: callers that accept the
// Core default resolve it with ZFSLayout first, so the default is visible where
// it is applied rather than hidden in this check.
func ValidZFSLayout(layout string, devices int) bool {
	switch layout {
	case "single":
		return devices == 1
	case "mirror":
		return devices == 2
	case "raidz1":
		return devices >= 3
	case "raidz2":
		return devices >= 4
	case "striped_mirrors":
		return devices >= 4 && devices%2 == 0
	}
	return false
}

// ZFSLayout applies Core's fixed defaults when an operator did not request an
// override. It is shared by validation, the host worker and API clients.
func ZFSLayout(layout string, devices int) string {
	if layout != "" {
		return layout
	}
	if devices == 1 {
		return "single"
	}
	if devices == 2 {
		return "mirror"
	}
	return "raidz1"
}
