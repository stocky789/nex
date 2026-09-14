// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "time"

const (
	LegacyVersion = 1
	Version       = 2
)

type Site struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Pool struct {
	ID           string `json:"id"`
	SiteID       string `json:"site_id"`
	Name         string `json:"name"`
	MasterHostID string `json:"master_host_id"`
}
type Check struct {
	Freshness   string     `json:"freshness,omitempty"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	ObservedAt  time.Time  `json:"observed_at"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	Error       string     `json:"error,omitempty"`
}
type NIC struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac"`
	Kind      string   `json:"kind,omitempty"`
	Master    string   `json:"master,omitempty"`
	Parent    string   `json:"parent,omitempty"`
	Addresses []string `json:"addresses"`
	State     string   `json:"state"`
}
type Disk struct {
	Name           string  `json:"name"`
	SizeBytes      uint64  `json:"size_bytes"`
	AvailableBytes *uint64 `json:"available_bytes,omitempty"`
}
type VM struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	// Guest is present for running VMs on Core builds that observe guest channels.
	Guest *GuestInfo `json:"guest,omitempty"`
}

// ManagedState lists succeeded registrations and VM definitions from the host journal.
// Pool, host and resource IDs are host-owned and stable across Controllers; adoption
// still records each entry under the current pool/host, which is a no-op for hosts that
// presented their own identity at enrollment.
type ManagedState struct {
	Storage  []StorageRegistration `json:"storage,omitempty"`
	Networks []NetworkRegistration `json:"networks,omitempty"`
	Media    []Media               `json:"media,omitempty"`
	VMs      []VMCreateSpec        `json:"vms,omitempty"`
	// MissingVMs names entries of VMs whose persistent domain libvirt no longer lists; the
	// journal still holds them, but a Controller must not adopt them as live guests.
	MissingVMs []string `json:"missing_vms,omitempty"`
	// Appliances are the Controller appliance VMs this host deployed and owns.
	Appliances []Appliance `json:"appliances,omitempty"`
	// Outcomes are journal entries that ended failed or needs_attention: task history a
	// replaced Controller records so host truth is not lost with the old database.
	Outcomes           []ManagedOutcome           `json:"outcomes,omitempty"`
	CredentialReceipts []StorageCredentialReceipt `json:"credential_receipts,omitempty"`
	Membership         *PoolMembership            `json:"membership,omitempty"`
}

// Ports lists the physical NICs or bonds enslaved to this bridge.
func (n NIC) Ports(all []NIC) []string {
	var ports []string
	for _, port := range all {
		if port.Master == n.Name && (port.Kind == "physical" || port.Kind == "bond") {
			ports = append(ports, port.Name)
		}
	}
	return ports
}

type Inventory struct {
	ObservedAt     time.Time `json:"observed_at"`
	Hostname       string    `json:"hostname"`
	CPUModel       string    `json:"cpu_model"`
	CPUModels      []string  `json:"cpu_models"`
	CPUCount       int       `json:"cpu_count"`
	MemoryBytes    uint64    `json:"memory_bytes"`
	Virtualization []string  `json:"virtualization"`
	OSVersion      string    `json:"os_version"`
	AgentVersion   string    `json:"agent_version"`
	NICs           []NIC     `json:"nics"`
	// Managed is the pool-scoped configuration this host has durably applied, reported from
	// the Core journal. Controller mirrors it: any entry it does not hold is adopted with the
	// same identity and name, so a replaced Controller recovers a pool from its master.
	Managed *ManagedState `json:"managed,omitempty"`
	Disks   []Disk        `json:"disks"`
	VMs     []VM          `json:"vms"`
	Checks  []Check       `json:"checks"`
}
type Host struct {
	Capabilities []string   `json:"capabilities,omitempty"`
	ID           string     `json:"id"`
	PoolID       string     `json:"pool_id"`
	Hostname     string     `json:"hostname"`
	Revoked      bool       `json:"revoked"`
	Connection   string     `json:"connection"`
	LastSeen     *time.Time `json:"last_seen"`
	Inventory    *Inventory `json:"inventory"`
}

// EnrollRequest presents the host-owned identity: the host generates host_id, and as a
// pool master pool_id and pool_name, on first enrollment and keeps them across Controllers.
// Empty values come from agents enrolled before host-owned identity; the Controller mints
// what is missing and the response reports it for the host to keep.
type EnrollRequest struct {
	Version  int    `json:"version"`
	Token    string `json:"token"`
	CSRPEM   string `json:"csr_pem"`
	Hostname string `json:"hostname"`
	HostID   string `json:"host_id,omitempty"`
	PoolID   string `json:"pool_id,omitempty"`
	PoolName string `json:"pool_name,omitempty"`
}

// EnrollResponse echoes the adopted identity; host_id and pool_id equal the request when
// presented. Rotation responses carry only host_id.
type EnrollResponse struct {
	HostID         string `json:"host_id"`
	PoolID         string `json:"pool_id,omitempty"`
	PoolName       string `json:"pool_name,omitempty"`
	CertificatePEM string `json:"certificate_pem"`
	CAPEM          string `json:"ca_pem"`
}
type RotateRequest struct {
	Version int    `json:"version"`
	CSRPEM  string `json:"csr_pem"`
}
type Command struct {
	Storage          *StorageRegistration            `json:"storage,omitempty"`
	Network          *NetworkRegistration            `json:"network,omitempty"`
	Media            *Media                          `json:"media,omitempty"`
	VM               *VMCreateSpec                   `json:"vm,omitempty"`
	Power            *VMPowerRequest                 `json:"power,omitempty"`
	VMMedia          *VMMediaRequest                 `json:"vm_media,omitempty"`
	StorageLifecycle *StorageLifecycleRequest        `json:"storage_lifecycle,omitempty"`
	CredentialRetire *StorageCredentialRetireRequest `json:"credential_retire,omitempty"`
	Pool             *PoolCommand                    `json:"pool,omitempty"`
	OperationID      string                          `json:"operation_id"`
	Kind             string                          `json:"kind"`
}
type Result struct {
	OperationID string    `json:"operation_id"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}
type ReportRequest struct {
	Capabilities       []string                 `json:"capabilities,omitempty"`
	Version            int                      `json:"version"`
	Sequence           uint64                   `json:"sequence"`
	Inventory          *Inventory               `json:"inventory,omitempty"`
	Results            []Result                 `json:"results"`
	StorageDiscoveries []StorageDiscoveryResult `json:"storage_discoveries,omitempty"`
	StorageClusters    []StorageClusterResult   `json:"storage_clusters,omitempty"`
}
type ReportResponse struct {
	Commands               []Command                 `json:"commands"`
	AcknowledgedResults    []string                  `json:"acknowledged_results"`
	Consoles               []ConsoleRequest          `json:"consoles,omitempty"`
	CredentialOffers       []StorageCredentialOffer  `json:"credential_offers,omitempty"`
	StorageRepositories    *StorageRepositorySupport `json:"storage_repositories,omitempty"`
	StorageClusters        *StorageClusterSupport    `json:"storage_clusters_support,omitempty"`
	StorageDiscoveries     []StorageDiscoveryRequest `json:"storage_discoveries,omitempty"`
	StorageClusterRequests []StorageClusterRequest   `json:"storage_clusters,omitempty"`
}

// StorageClusterCapability acknowledges the proof request identifier. A Core
// must receive StorageClusterSupport for this exact capability before it sends
// clustered-storage results, so a strict older Controller never sees fields it
// cannot decode.
const StorageClusterCapability = "storage-clustered-iscsi-v2"

// StorageClusterSessionMonitorCapability is an additive report-envelope
// extension. A Core sends member session-monitor readiness only after the
// Controller returns this exact acknowledgement. Keeping the negotiation in
// the existing capability string makes both directions safe for strict older
// decoders: older Controllers ignore the offered capability and acknowledge
// only StorageClusterCapability; older Cores never offer it.
const StorageClusterSessionMonitorCapability = "storage-cluster-session-monitor-v1"

type StorageClusterSupport struct {
	Capability string `json:"capability"`
}

// StorageClusterRequest and StorageClusterResult are transient Controller/Core
// envelopes. The Core owns every operation and configuration they carry; the
// Controller keeps neither after the report exchange finishes.
type StorageClusterRequest struct {
	RequestID          string                        `json:"request_id"`
	PoolID             string                        `json:"pool_id"`
	HostID             string                        `json:"host_id"`
	FenceHostID        string                        `json:"fence_host_id,omitempty"`
	FenceConfirmation  string                        `json:"fence_confirmation,omitempty"`
	Action             string                        `json:"action"` // apply, status, or verify_fence
	CredentialRequests []StorageClusterCredentialRef `json:"credential_requests,omitempty"`
	Registration       StorageRegistration           `json:"registration,omitempty"`
	Config             StorageClusterConfig          `json:"config,omitempty"`
	ExpectedGeneration uint64                        `json:"expected_generation,omitempty"`
}

// StorageClusterCredentialRef names a Core-private, one-time handoff. It has
// no secret or receipt: Core resolves it to a local opaque alias before native
// configuration and distributes required peer material over pool mTLS.
type StorageClusterCredentialRef struct {
	RequestID    string `json:"request_id"`
	Kind         string `json:"kind"` // chap, fence_bmc, or fence_ssh
	TargetHostID string `json:"target_host_id"`
}

type StorageClusterOperation struct {
	RequestID            string                       `json:"request_id"`
	MembershipGeneration uint64                       `json:"membership_generation"`
	Config               StorageClusterConfig         `json:"config"`
	Status               string                       `json:"status"`
	Error                string                       `json:"error,omitempty"`
	UpdatedAt            time.Time                    `json:"updated_at"`
	Members              []StorageClusterMemberReport `json:"members,omitempty"`
}

// StorageClusterMemberReport binds an observed health result to its exact
// member and accepted configuration. The Controller can therefore present
// per-member health without inferring identity from an unordered status slice.
type StorageClusterMemberReport struct {
	HostID               string               `json:"host_id"`
	Status               StorageClusterStatus `json:"status"`
	Generation           uint64               `json:"generation"`
	MembershipGeneration uint64               `json:"membership_generation"`
	ClusterID            string               `json:"cluster_id"`
	ConfigDigest         string               `json:"config_digest"`
	Stage                string               `json:"stage"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

type StorageClusterResult struct {
	RequestID  string                    `json:"request_id"`
	PoolID     string                    `json:"pool_id"`
	HostID     string                    `json:"host_id"`
	Status     string                    `json:"status"` // accepted, queued, succeeded, failed, needs_attention, stale
	Error      string                    `json:"error,omitempty"`
	ObservedAt time.Time                 `json:"observed_at"`
	Operations []StorageClusterOperation `json:"operations,omitempty"`
}

// StorageRepositorySupport is the Controller-first v2 upgrade acknowledgement. A Core must
// receive this explicit support before it activates repository features requiring protocol v2.
type StorageRepositorySupport struct {
	ProtocolVersion int    `json:"protocol_version"`
	Capability      string `json:"capability"`
}
type Operation struct {
	ID        string    `json:"id"`
	HostID    string    `json:"host_id"`
	Kind      string    `json:"kind"`
	RequestID string    `json:"request_id"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Actor     string    `json:"actor"`
}
type Audit struct {
	Detail     string    `json:"detail,omitempty"`
	ID         int64     `json:"id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	ResourceID string    `json:"resource_id"`
	CreatedAt  time.Time `json:"created_at"`
}
