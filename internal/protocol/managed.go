// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"errors"
	"fmt"
	"regexp"
)

const ManagedVMCapability = "managed-vm-v1"
const MaxISOBytes int64 = 8 << 30

type StorageRegistration struct {
	ID          string `json:"id"`
	PoolID      string `json:"pool_id"`
	HostID      string `json:"host_id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}
type NetworkRegistration struct {
	ID          string `json:"id"`
	PoolID      string `json:"pool_id"`
	HostID      string `json:"host_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Uplink      string `json:"uplink,omitempty"`
	VLAN        int    `json:"vlan,omitempty"`
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

type Media struct {
	ID          string `json:"id"`
	PoolID      string `json:"pool_id"`
	HostID      string `json:"host_id"`
	StorageID   string `json:"storage_id"`
	Name        string `json:"name"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}
type VMCreateSpec struct {
	ID        string `json:"id"`
	PoolID    string `json:"pool_id"`
	HostID    string `json:"host_id"`
	Name      string `json:"name"`
	StorageID string `json:"storage_id"`
	NetworkID string `json:"network_id"`
	MediaID   string `json:"media_id"`
	CPUs      int    `json:"cpus"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGiB   int    `json:"disk_gib"`
	Firmware  string `json:"firmware"`
}

var uplinkName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,9}$`)

// GuestBridge derives the deterministic bridge name for a VLAN network: uplink name plus
// "v" and the tag. Names stay within IFNAMSIZ (15) because uplinks are limited to 10 characters.
func GuestBridge(uplink string, vlan int) string { return fmt.Sprintf("%sv%d", uplink, vlan) }

// ValidUplink reports whether name can be an uplink NIC: plain, at most 10 characters.
func ValidUplink(name string) bool { return uplinkName.MatchString(name) }

// ValidateVLAN checks a kind "vlan" registration's shape on both Controller and Core.
func ValidateVLAN(n NetworkRegistration) error {
	if n.Kind != "vlan" || !uplinkName.MatchString(n.Uplink) || n.VLAN < 0 || n.VLAN > 4094 || n.Source != GuestBridge(n.Uplink, n.VLAN) {
		return errors.New("VLAN networks need an uplink NIC name of at most 10 characters, a VLAN ID 0–4094 and the derived bridge name")
	}
	return nil
}
