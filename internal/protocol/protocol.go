// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "time"

const Version = 1

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
// Pool and host IDs inside entries are those of the Controller that created them and are
// rewritten on adoption; resource IDs are stable across Controllers.
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
	Outcomes []ManagedOutcome `json:"outcomes,omitempty"`
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
type EnrollRequest struct {
	Version  int    `json:"version"`
	Token    string `json:"token"`
	CSRPEM   string `json:"csr_pem"`
	Hostname string `json:"hostname"`
}
type EnrollResponse struct {
	HostID         string `json:"host_id"`
	CertificatePEM string `json:"certificate_pem"`
	CAPEM          string `json:"ca_pem"`
}
type RotateRequest struct {
	Version int    `json:"version"`
	CSRPEM  string `json:"csr_pem"`
}
type Command struct {
	Storage     *StorageRegistration `json:"storage,omitempty"`
	Network     *NetworkRegistration `json:"network,omitempty"`
	Media       *Media               `json:"media,omitempty"`
	VM          *VMCreateSpec        `json:"vm,omitempty"`
	Power       *VMPowerRequest      `json:"power,omitempty"`
	VMMedia     *VMMediaRequest      `json:"vm_media,omitempty"`
	OperationID string               `json:"operation_id"`
	Kind        string               `json:"kind"`
}
type Result struct {
	OperationID string    `json:"operation_id"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}
type ReportRequest struct {
	Capabilities []string   `json:"capabilities,omitempty"`
	Version      int        `json:"version"`
	Sequence     uint64     `json:"sequence"`
	Inventory    *Inventory `json:"inventory,omitempty"`
	Results      []Result   `json:"results"`
}
type ReportResponse struct {
	Commands            []Command        `json:"commands"`
	AcknowledgedResults []string         `json:"acknowledged_results"`
	Consoles            []ConsoleRequest `json:"consoles,omitempty"`
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
