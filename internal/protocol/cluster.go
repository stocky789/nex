// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	FenceRedfish = "redfish"
	FenceIPMI    = "ipmilan"
	FenceVirsh   = "virsh"   // disposable-lab Core VMs only
	FenceSCSIPR  = "scsi_pr" // requires validated SPC-3 persistent reservations
)

// StorageClusterFence is intentionally a closed, typed profile. CredentialRef
// names a root-only Core secret and never carries a password or arbitrary agent
// arguments from a Controller request.
type StorageClusterFence struct {
	Kind     string `json:"kind"`
	Endpoint string `json:"endpoint,omitempty"`
	TargetID string `json:"target_id,omitempty"` // BMC chassis/system or lab-domain identity
	Username string `json:"username,omitempty"`  // BMC account name; its password is the credential
	// TLSFingerprint pins the exact certificate a Redfish controller must present, as 64
	// lower-case hex digits of its leaf SHA-256.  Core captures it itself when the profile
	// is recorded and reports it so an operator can compare it with the controller's own
	// page; it is not a secret and a client never supplies one.  A factory BMC certificate
	// is self-signed with no usable name, so this pin, not a chain, is the identity of the
	// endpoint the password is sent to and the power-off is issued through.
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`
	// Cipher is the RMCP+ cipher suite an IPMI fence must use, as the controller's own
	// LAN channel offers it.  Zero means none was recorded and Core picks one itself;
	// suite 0 — no authentication, no integrity, no confidentiality — is never selected
	// and never accepted here, so the value is only ever absent, 17 or 3.
	Cipher uint8 `json:"cipher,omitempty"`
	// ProvisionedByCore marks the account behind this profile as one Nex Core created on
	// the host's own management controller in-band.  It is how a host knows, after a
	// reboot or a master change, that the account under its name is its own to replace
	// rather than somebody else's to leave alone, and it is why an operator-entered
	// profile is never overwritten by automatic provisioning.  A Controller never sets
	// it: it is written by the pool master when a host's own Core reports the account it
	// created.
	ProvisionedByCore bool   `json:"provisioned_by_core,omitempty"`
	CredentialRef     string `json:"credential_ref,omitempty"`
	DevicePath        string `json:"device_path,omitempty"`
	VirshURI          string `json:"virsh_uri,omitempty"`
	SSHLogin          string `json:"ssh_login,omitempty"`
	SSHPort           uint16 `json:"ssh_port,omitempty"`
	SSHHostKeyRef     string `json:"ssh_host_key_ref,omitempty"`
}

type StorageClusterNode struct {
	HostID          string              `json:"host_id"`
	CorosyncNodeID  uint32              `json:"corosync_node_id"`
	CorosyncAddress string              `json:"corosync_address"`
	Fence           StorageClusterFence `json:"fence"`
}

// StorageClusterConfig is a Core-owned projection of the master's accepted
// clustered-iSCSI record. Corosync/Pacemaker files and CIB state are runtime
// realizations of it, never Controller state.
type StorageClusterConfig struct {
	ClusterID    string                      `json:"cluster_id"`
	PoolID       string                      `json:"pool_id"`
	RepositoryID string                      `json:"repository_id"`
	Generation   uint64                      `json:"generation"`
	LockTable    string                      `json:"lock_table"`
	DevicePath   string                      `json:"device_path"`
	MountPath    string                      `json:"mount_path"`
	Nodes        []StorageClusterNode        `json:"nodes"`
	FenceProofs  []StorageClusterFenceStatus `json:"fence_proofs,omitempty"`
}

// StorageClusterFenceStatus is Core-recorded evidence from an actual fence and
// exclusion observation. A status query or UI confirmation must never create it.
type StorageClusterFenceStatus struct {
	RequestID         string    `json:"request_id,omitempty"`
	HostID            string    `json:"host_id"`
	Status            string    `json:"status"` // unverified, passed, failed, uncertain
	TargetID          string    `json:"target_id,omitempty"`
	ExclusionVerified bool      `json:"exclusion_verified"`
	ObservedAt        time.Time `json:"observed_at,omitempty"`
	Error             string    `json:"error,omitempty"`
	ConfigGeneration  uint64    `json:"config_generation"`
	ConfigDigest      string    `json:"config_digest"`
	// Method names how the exclusion was proved.  Empty is a fence device Core powered the
	// host off through and independently observed off, which is every proof there was
	// before watchdog fencing; "watchdog" is the host resetting itself, whose evidence is
	// the record below.  A clustered-storage fence only ever produces the empty one.
	Method string `json:"method,omitempty"`
	// Watchdog is the whole evidence of a watchdog exclusion, so that a proof can be read
	// back later and judged rather than believed.  It is present only when Method says so.
	Watchdog *PoolWatchdogExclusion `json:"watchdog,omitempty"`
	// StorageLease is the exact native sanlock observation used for a storage-lease proof.
	StorageLease *PoolStorageLeaseExclusion `json:"storage_lease,omitempty"`
}

// PoolFenceMethodWatchdog is the Method value of a watchdog exclusion proof.
const PoolFenceMethodWatchdogProof = "watchdog"

// PoolFenceMethodStorageLeaseProof is the Method value of a sanlock exclusion proof.
const PoolFenceMethodStorageLeaseProof = "storage-lease"

// PoolStorageLeaseExclusion records the sanlock host status and continuous observation
// interval used to exclude one host. RepositoryIdentityDigest is derived from Core's
// observed StorageIdentity; matching a configured mount path alone is not repository
// identity. HOST_FREE and HOST_FAIL are not exclusion proof.
type PoolStorageLeaseExclusion struct {
	Repositories           []PoolStorageLeaseLockspaceProof `json:"repositories"`
	HostLeaseID            uint64                           `json:"host_lease_id"`
	BoundSeconds           int                              `json:"bound_seconds"`
	WatchdogKind           string                           `json:"watchdog_kind"`
	WatchdogTimeoutSeconds int                              `json:"watchdog_timeout_seconds"`
	ArmedGeneration        uint64                           `json:"armed_generation"`
}

type PoolStorageLeaseLockspaceProof struct {
	RepositoryID             string `json:"repository_id"`
	RepositoryGeneration     uint64 `json:"repository_generation"`
	RepositoryIdentityDigest string `json:"repository_identity_digest"`
	Lockspace                string `json:"lockspace"`
	HostGeneration           uint64 `json:"host_generation"`
	HostState                string `json:"host_state"`
	HostDeadSeconds          int    `json:"host_dead_seconds"`
	HeldSeconds              int    `json:"held_seconds"`
}

const PoolStorageLeaseHostDead = "HOST_DEAD"

// Valid rejects incomplete native evidence. Pool/member and operation binding are checked
// by the promotion verifier, which has the durable records needed to make those checks.
func (e PoolStorageLeaseExclusion) Valid() error {
	if len(e.Repositories) == 0 || len(e.Repositories) > 64 || e.HostLeaseID == 0 || e.HostLeaseID > PoolSanlockMaxHostID || e.BoundSeconds <= 0 ||
		e.WatchdogKind != PoolWatchdogHardware || e.WatchdogTimeoutSeconds <= 0 || e.WatchdogTimeoutSeconds > 43200 ||
		e.BoundSeconds < e.WatchdogTimeoutSeconds*2 ||
		e.ArmedGeneration == 0 {
		return errors.New("storage-lease exclusion evidence is incomplete or unsafe")
	}
	previous := ""
	for _, repository := range e.Repositories {
		if repository.RepositoryID == "" || repository.RepositoryGeneration == 0 || !validSHA256(repository.RepositoryIdentityDigest) ||
			repository.Lockspace == "" || len(repository.Lockspace) > 48 || strings.ContainsAny(repository.Lockspace, "\x00\r\n") ||
			repository.HostGeneration == 0 || repository.HostState != PoolStorageLeaseHostDead ||
			repository.HostDeadSeconds <= 0 || e.BoundSeconds < repository.HostDeadSeconds || repository.HeldSeconds < e.BoundSeconds ||
			previous != "" && previous >= repository.RepositoryID {
			return errors.New("storage-lease lockspace evidence is incomplete or unsafe")
		}
		previous = repository.RepositoryID
	}
	return nil
}

// PoolWatchdogExclusion is what a proving host observed, one field per condition of the rule
// change, so nothing about a proof has to be reconstructed from the code that made it.
type PoolWatchdogExclusion struct {
	// Kind and TimeoutSeconds are the target's own armed report: the device class it armed
	// and the timeout it armed with.  An emulated kind says the target is itself a virtual
	// machine, so this proof is evidence about a hypervisor and not about hardware.
	Kind           string `json:"kind"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	// ArmedGeneration is the pool's fencing generation the target armed at, which must be
	// the generation being promoted.
	ArmedGeneration uint64 `json:"armed_generation"`
	// HeldSeconds is how long this host has continuously been in a quorate partition
	// without the target, on a monotonic clock, and BoundSeconds is how long it had to be.
	// Any flap restarts the clock, so this is elapsed time and not an average.
	HeldSeconds  int `json:"held_seconds"`
	BoundSeconds int `json:"bound_seconds"`
	// Partition is the hosts Corosync reports in this host's partition at the moment of the
	// proof — the majority side, by the pool's own quorum service.
	Partition []string `json:"partition,omitempty"`
}

type StorageClusterStatus struct {
	ClusterID    string `json:"cluster_id"`
	RepositoryID string `json:"repository_id"`
	// SessionMonitorReady is an authenticated member readiness attestation. It
	// is true only after this Core has proved the installed Nex OCF transport
	// monitor/local CLI and a live, exact local iSCSI session. It deliberately
	// does not infer aggregate cluster or GFS2 health: masters use it solely to
	// block enabling the native monitor until every configured member is ready.
	SessionMonitorReady bool                        `json:"session_monitor_ready,omitempty"`
	Healthy             bool                        `json:"healthy"`
	Quorate             bool                        `json:"quorate"`
	Mounted             bool                        `json:"mounted"`
	Error               string                      `json:"error,omitempty"`
	Status              string                      `json:"status"` // unconfigured, pending, blocked, healthy, failed
	FencingVerified     bool                        `json:"fencing_verified"`
	Fences              []StorageClusterFenceStatus `json:"fences,omitempty"`
}

func (c StorageClusterConfig) ValidFor(s StorageRegistration) error {
	if !IsClusteredISCSI(s) || s.Source.ISCSI == nil {
		return errors.New("cluster configuration needs clustered iSCSI storage")
	}
	v := s.Source.ISCSI
	if c.ClusterID != v.ClusterID || c.LockTable != v.LockTable || c.PoolID != s.PoolID || c.RepositoryID != s.ID || c.Generation != s.Generation {
		return errors.New("cluster configuration does not match storage registration")
	}
	if len(c.Nodes) != len(v.EligibleHostIDs) || !validEligibleHosts(v.EligibleHostIDs) {
		return errors.New("cluster configuration needs three to five eligible nodes")
	}
	allowed, seenHost, seenNode := map[string]bool{}, map[string]bool{}, map[uint32]bool{}
	for _, host := range v.EligibleHostIDs {
		allowed[host] = true
	}
	for _, node := range c.Nodes {
		if !allowed[node.HostID] || seenHost[node.HostID] || node.CorosyncNodeID == 0 || seenNode[node.CorosyncNodeID] || !storageServer(node.CorosyncAddress) || node.Fence.Valid() != nil {
			return errors.New("invalid clustered storage node")
		}
		seenHost[node.HostID], seenNode[node.CorosyncNodeID] = true, true
	}
	if !storageSCSIDevice(c.DevicePath) || !storagePath(c.MountPath) {
		return errors.New("cluster configuration needs a stable device path and mount path")
	}
	return nil
}

// certificateFingerprint is a SHA-256 leaf digest: 64 lower-case hex digits.
var certificateFingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)

// bmcTargetID is a Redfish ComputerSystem Id or an IPMI plug used as one URI path segment.
// DMTF DSP0266 §9.6.6: Id must not contain RFC1738-defined unsafe characters
// ({ } space | ^ ~ [ ] ` \), and Redfish URIs must not percent-encode path segments.
// Period and hyphen are allowed and required for Dell iDRAC (System.Embedded.1). Length is
// capped so a profile stays bounded; the first character must be alphanumeric so a bare
// "." or "-" cannot slip through as a segment.
var bmcTargetID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (f StorageClusterFence) Valid() error {
	if f.CredentialRef != "" && !storageOpaqueID.MatchString(f.CredentialRef) {
		return errors.New("invalid fence credential reference")
	}
	switch f.Kind {
	case FenceRedfish, FenceIPMI:
		if !storageServer(f.Endpoint) || !bmcTargetID.MatchString(f.TargetID) || (f.Username != "" && !storageOpaqueID.MatchString(f.Username)) || (f.TLSFingerprint != "" && !certificateFingerprint.MatchString(f.TLSFingerprint)) || f.CredentialRef == "" || f.DevicePath != "" || f.VirshURI != "" || f.SSHLogin != "" || f.SSHPort != 0 || f.SSHHostKeyRef != "" {
			return errors.New("invalid BMC fence profile")
		}
		if f.Cipher != 0 && f.Cipher != 3 && f.Cipher != 17 || f.Cipher != 0 && f.Kind != FenceIPMI {
			return errors.New("invalid BMC fence profile")
		}
	case FenceVirsh:
		if !storageServer(f.Endpoint) || !storageOpaqueID.MatchString(f.TargetID) || !storageOpaqueID.MatchString(f.SSHLogin) || f.SSHPort == 0 || f.CredentialRef == "" || !storageOpaqueID.MatchString(f.SSHHostKeyRef) || f.DevicePath != "" || f.Username != "" || f.TLSFingerprint != "" || f.Cipher != 0 || f.ProvisionedByCore || !validVirshURI(f) {
			return errors.New("invalid virsh fence profile")
		}
	case FenceSCSIPR:
		if f.Endpoint != "" || f.CredentialRef != "" || f.Username != "" || f.TLSFingerprint != "" || f.Cipher != 0 || f.ProvisionedByCore || f.VirshURI != "" || f.SSHLogin != "" || f.SSHPort != 0 || f.SSHHostKeyRef != "" || !storageSCSIDevice(f.DevicePath) {
			return errors.New("invalid SCSI persistent-reservation fence profile")
		}
	default:
		return errors.New("unsupported fence profile")
	}
	return nil
}

// validVirshURI makes the descriptive URI a strict binding to the pinned SSH
// transport rather than an ignored second connection. fence_virsh itself has
// no URI option; qemu:///system declares the remote daemon it must observe.
func validVirshURI(f StorageClusterFence) bool {
	if f.VirshURI == "qemu:///system" {
		return true
	}
	u, err := url.Parse(f.VirshURI)
	if err != nil || u.User == nil {
		return false
	}
	_, hasPassword := u.User.Password()
	if u.Scheme != "qemu+ssh" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "/system" || u.User.Username() != f.SSHLogin || hasPassword || u.Hostname() != f.Endpoint {
		return false
	}
	port := u.Port()
	return (port == "" && f.SSHPort == 22) || port == strconv.Itoa(int(f.SSHPort))
}

func (c StorageClusterConfig) FencingVerified() bool {
	digest := c.Digest()
	passed := map[string]StorageClusterFenceStatus{}
	for _, proof := range c.FenceProofs {
		passed[proof.HostID] = proof
	}
	for _, node := range c.Nodes {
		proof, ok := passed[node.HostID]
		if !ok || proof.Status != "passed" || !proof.ExclusionVerified || proof.TargetID != node.Fence.TargetID || proof.ObservedAt.IsZero() || proof.ConfigGeneration != c.Generation || proof.ConfigDigest != digest {
			return false
		}
	}
	return true
}

// Digest binds fencing proof to exact accepted config without including the proof itself.
func (c StorageClusterConfig) Digest() string {
	copy := c
	copy.FenceProofs = nil
	copy.Nodes = copy.SortedNodes()
	b, _ := json.Marshal(copy)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func StorageClusterFenceConfigDigest(c StorageClusterConfig) string { return c.Digest() }

func storageSCSIDevice(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && len(path) > len("/dev/disk/by-id/") && path[:len("/dev/disk/by-id/")] == "/dev/disk/by-id/"
}

// SortedNodes returns a stable copy for deterministic configuration rendering.
func (c StorageClusterConfig) SortedNodes() []StorageClusterNode {
	nodes := append([]StorageClusterNode(nil), c.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].CorosyncNodeID < nodes[j].CorosyncNodeID })
	return nodes
}
