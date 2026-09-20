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
	// Usage is present for running VMs on Core builds that sample it. It is absent for
	// stopped VMs and whenever the host could not sample this VM within its probe deadline.
	Usage *VMUsage `json:"usage,omitempty"`
}

// VMUsage is what the host measured of a running VM's resource consumption. It is sampled
// from libvirt without taking the domain's job, so libvirt answers some of it only when no
// other operation holds the VM: every optional member is honest-unknown when absent, which
// is a different fact from the number being zero. Nothing here is durable. The host keeps
// the previous CPU counter in memory and reports the rate itself, because a Controller that
// subtracted two cumulative counters would be deriving pool state a host already knows.
type VMUsage struct {
	// ObservedAt is when this sample was taken, not when it was reported. A host that could
	// not resample a VM within its probe deadline re-reports the previous sample unchanged,
	// so this timestamp is the only honest statement of how old these numbers are.
	ObservedAt time.Time `json:"observed_at"`
	// SampleWindowMS is the wall-clock gap between the two samples CPUPercent averages over,
	// floored at 1 so a rate never arrives with a window rounded away to zero. It is zero
	// when CPUPercent is absent.
	SampleWindowMS uint32 `json:"sample_window_ms"`
	// CPUPercent is whole percent of this VM's own allocated vCPUs, rounded, averaged over
	// SampleWindowMS: 100 means every vCPU was busy for the whole window. It is absent until
	// a second sample exists, so a VM that just started, an agent that just restarted and a
	// VM whose counter reset all report no rate for one probe instead of a wrong one. The
	// host threads emulating the VM's devices are included, so a busy VM can pass 100; the
	// value is clamped at 1000, because anything above that is arithmetic, not observation.
	CPUPercent *uint16 `json:"cpu_percent,omitempty"`
	// MemoryAssignedBytes is what the guest currently may use and MemoryMaximumBytes its
	// configured ceiling. This is allocation, never usage. Nex does not report memory used
	// inside the guest: that needs balloon statistics a Nex VM definition does not enable,
	// and the guest agent has no command for it.
	MemoryAssignedBytes uint64 `json:"memory_assigned_bytes"`
	MemoryMaximumBytes  uint64 `json:"memory_maximum_bytes"`
	// MemoryHostBytes is the resident host memory this VM's process occupies: what the VM
	// costs the host, not what the guest is using. Absent when libvirt could not read it
	// without waiting for another operation on this VM.
	MemoryHostBytes *uint64 `json:"memory_host_bytes,omitempty"`
	// DiskUsedBytes is the space this VM's own disk images occupy in their repository right
	// now, and DiskCapacityBytes the virtual size the guest sees; a thin disk grows towards
	// the second. CD drives, firmware variable stores and any image outside the per-VM
	// directory Nex writes are not counted. Both are absent when libvirt could not read them
	// without waiting for another operation on this VM.
	DiskUsedBytes     *uint64 `json:"disk_used_bytes,omitempty"`
	DiskCapacityBytes *uint64 `json:"disk_capacity_bytes,omitempty"`
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
	// DeletedVMs are intentional vm-delete tombstones so a Controller drops its mirror; this is
	// not the same as MissingVMs (accidental undefine while the journal still expects the VM).
	DeletedVMs []VMTombstone `json:"deleted_vms,omitempty"`
	// Appliances are the Controller appliance VMs this host deployed and owns.
	Appliances []Appliance `json:"appliances,omitempty"`
	// Outcomes are journal entries that ended failed or needs_attention: task history a
	// replaced Controller records so host truth is not lost with the old database.
	Outcomes           []ManagedOutcome           `json:"outcomes,omitempty"`
	CredentialReceipts []StorageCredentialReceipt `json:"credential_receipts,omitempty"`
	ZFSProvisions      []ZFSProvisionHostResult   `json:"zfs_provisions,omitempty"`
	Membership         *PoolMembership            `json:"membership,omitempty"`
	// Join is this host's durable staged pool join, reported until it is ready so a
	// replaced Controller adopts an interrupted join instead of losing it.
	Join *PoolJoinStatus `json:"join,omitempty"`
	// Replica is this member's stored copy of the master's signed pool authority. It is
	// observed state: what this host holds and how fresh it is. Nothing acts on it, no
	// health is derived from it, and the Controller mirrors it without storing it.
	Replica *PoolReplicaStatus `json:"replica,omitempty"`
	// Transfer is this host's own part of a planned master change or a member eject. Every
	// participant reports its own stage; the Controller assembles the picture and drives
	// nothing, so a transfer finishes with no Controller connection at all.
	Transfer *PoolTransferStatus `json:"transfer,omitempty"`
	// Authority is the pool master's own durable authority number and the content digest it
	// numbered. Only a master reports it, and it is what a member's reported replica
	// generation is compared against: without it a replica that is one generation behind is
	// indistinguishable from a current one.
	Authority *PoolAuthorityRecord `json:"authority,omitempty"`
	// Promotion is a candidate's own fence-verified promotion: its durable stage and, when
	// it has one, the structured verdict of the proof. The Controller displays it.
	Promotion *PoolPromotionStatus `json:"promotion,omitempty"`
	// Restricted is this host's own Core-enforced refusal to act on pool authority while it
	// cannot prove it holds the current one. It is reported so an operator sees why a host
	// starts nothing; it is enforced on Core whether or not anything is reading it.
	Restricted *PoolRestrictedStatus `json:"restricted,omitempty"`
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

// CPUBaseline is a Core-master observation of a common CPU definition for current members.
// A VM stores its selected model/features independently; refresh never changes a VM.
type CPUBaseline struct {
	Model       string       `json:"model"`
	Features    []CPUFeature `json:"features"`
	Machine     string       `json:"machine"`
	Generation  uint64       `json:"generation"`
	MemberCount int          `json:"member_count"`
	ObservedAt  time.Time    `json:"observed_at"`
	Error       string       `json:"error,omitempty"`
}

type Inventory struct {
	ObservedAt     time.Time    `json:"observed_at"`
	Hostname       string       `json:"hostname"`
	CPUModel       string       `json:"cpu_model"`
	CPUModels      []string     `json:"cpu_models"`
	CPUBaseline    *CPUBaseline `json:"cpu_baseline,omitempty"`
	CPUCount       int          `json:"cpu_count"`
	MemoryBytes    uint64       `json:"memory_bytes"`
	Virtualization []string     `json:"virtualization"`
	OSVersion      string       `json:"os_version"`
	AgentVersion   string       `json:"agent_version"`
	NICs           []NIC        `json:"nics"`
	// Managed is the pool-scoped configuration this host has durably applied, reported from
	// the Core journal. Controller mirrors it: any entry it does not hold is adopted with the
	// same identity and name, so a replaced Controller recovers a pool from its master.
	Managed *ManagedState `json:"managed,omitempty"`
	// ManagementNetwork is this host's own staged/applied management network change,
	// host-scoped rather than pool-scoped. It is the only path by which a Controller
	// learns that a rollback deadline is running.
	ManagementNetwork *ManagementNetworkStatus `json:"management_network,omitempty"`
	Disks             []Disk                   `json:"disks"`
	VMs               []VM                     `json:"vms"`
	Checks            []Check                  `json:"checks"`
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
	Storage           *StorageRegistration            `json:"storage,omitempty"`
	Network           *NetworkRegistration            `json:"network,omitempty"`
	Media             *Media                          `json:"media,omitempty"`
	VM                *VMCreateSpec                   `json:"vm,omitempty"`
	Power             *VMPowerRequest                 `json:"power,omitempty"`
	Delete            *VMDeleteRequest                `json:"delete,omitempty"`
	VMMedia           *VMMediaRequest                 `json:"vm_media,omitempty"`
	StorageLifecycle  *StorageLifecycleRequest        `json:"storage_lifecycle,omitempty"`
	ResourceLifecycle *ResourceLifecycleRequest       `json:"resource_lifecycle,omitempty"`
	CredentialRetire  *StorageCredentialRetireRequest `json:"credential_retire,omitempty"`
	ZFSProvision      *ZFSProvisionRequest            `json:"zfs_provision,omitempty"`
	Pool              *PoolCommand                    `json:"pool,omitempty"`
	ManagementNetwork *ManagementNetworkRequest       `json:"management_network,omitempty"`
	OperationID       string                          `json:"operation_id"`
	Kind              string                          `json:"kind"`
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
	PoolResults        []PoolResult             `json:"pool_results,omitempty"`
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
	StatsChannels          []StatsChannelRequest     `json:"stats_channels,omitempty"`
	PoolRequests           []PoolRequest             `json:"pool_requests,omitempty"`
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
