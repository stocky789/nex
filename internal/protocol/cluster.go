// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
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
	Kind          string `json:"kind"`
	Endpoint      string `json:"endpoint,omitempty"`
	TargetID      string `json:"target_id,omitempty"` // BMC chassis/system or lab-domain identity
	CredentialRef string `json:"credential_ref,omitempty"`
	DevicePath    string `json:"device_path,omitempty"`
	VirshURI      string `json:"virsh_uri,omitempty"`
	SSHLogin      string `json:"ssh_login,omitempty"`
	SSHPort       uint16 `json:"ssh_port,omitempty"`
	SSHHostKeyRef string `json:"ssh_host_key_ref,omitempty"`
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

func (f StorageClusterFence) Valid() error {
	if f.CredentialRef != "" && !storageOpaqueID.MatchString(f.CredentialRef) {
		return errors.New("invalid fence credential reference")
	}
	switch f.Kind {
	case FenceRedfish, FenceIPMI:
		if !storageServer(f.Endpoint) || !storageOpaqueID.MatchString(f.TargetID) || f.CredentialRef == "" || f.DevicePath != "" || f.VirshURI != "" || f.SSHLogin != "" || f.SSHPort != 0 || f.SSHHostKeyRef != "" {
			return errors.New("invalid BMC fence profile")
		}
	case FenceVirsh:
		if !storageServer(f.Endpoint) || !storageOpaqueID.MatchString(f.TargetID) || !storageOpaqueID.MatchString(f.SSHLogin) || f.SSHPort == 0 || f.CredentialRef == "" || !storageOpaqueID.MatchString(f.SSHHostKeyRef) || f.DevicePath != "" || !validVirshURI(f) {
			return errors.New("invalid virsh fence profile")
		}
	case FenceSCSIPR:
		if f.Endpoint != "" || f.CredentialRef != "" || f.VirshURI != "" || f.SSHLogin != "" || f.SSHPort != 0 || f.SSHHostKeyRef != "" || !storageSCSIDevice(f.DevicePath) {
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
