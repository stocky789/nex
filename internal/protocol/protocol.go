// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "time"

const Version = 1

type Site struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Pool struct {
	ID     string `json:"id"`
	SiteID string `json:"site_id"`
	Name   string `json:"name"`
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
	Disks          []Disk    `json:"disks"`
	VMs            []VM      `json:"vms"`
	Checks         []Check   `json:"checks"`
}
type Host struct {
	ID         string     `json:"id"`
	PoolID     string     `json:"pool_id"`
	Hostname   string     `json:"hostname"`
	Revoked    bool       `json:"revoked"`
	Connection string     `json:"connection"`
	LastSeen   *time.Time `json:"last_seen"`
	Inventory  *Inventory `json:"inventory"`
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
	OperationID string `json:"operation_id"`
	Kind        string `json:"kind"`
}
type Result struct {
	OperationID string    `json:"operation_id"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}
type ReportRequest struct {
	Version   int        `json:"version"`
	Sequence  uint64     `json:"sequence"`
	Inventory *Inventory `json:"inventory,omitempty"`
	Results   []Result   `json:"results"`
}
type ReportResponse struct {
	Commands            []Command `json:"commands"`
	AcknowledgedResults []string  `json:"acknowledged_results"`
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
