// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"errors"
	"time"
)

// SystemDiskReplaceCapability is offered by a Core that can rebuild a mirrored system disk onto a
// replacement disk. A Core without it never receives the command: the Controller refuses the request.
const SystemDiskReplaceCapability = "host-system-disk-replace-v1"

// SystemDiskReplaceCommandKind is the Command.Kind a Controller relays to one host.
const SystemDiskReplaceCommandKind = "host-system-disk-replace"

// SystemDiskReplaceRequest asks one host to rebuild its system disk onto one replacement disk.
// The device is the one the operator picked from the host's own disk discovery listing, with the same
// identity, fingerprint and typed erase confirmation rules as every destructive disk operation; Core
// revalidates all of it against a fresh observation before it writes anything, and refuses the disk the
// running system started from. RequestID is the idempotency key: the host keeps its own durable record of
// the operation, so a redelivered or retried request continues it and never repeats a destructive step.
type SystemDiskReplaceRequest struct {
	HostID    string             `json:"host_id"`
	RequestID string             `json:"request_id"`
	Device    ZFSProvisionDevice `json:"device"`
}

func (r SystemDiskReplaceRequest) Valid() error {
	if !storageOpaqueID.MatchString(r.HostID) {
		return errors.New("system disk replacement requires a host")
	}
	if !storageOpaqueID.MatchString(r.RequestID) {
		return errors.New("system disk replacement requires a stable request_id")
	}
	if validZFSDevice(r.Device) != nil {
		return errors.New("system disk replacement device is invalid")
	}
	return nil
}

// HardwareSystemDisk is the host's own report on its system disk: the disk the running system
// started from (never a replacement target) and its record of the latest replacement. The
// mirror itself and its resync are reported in HostHardware.MDArrays.
type HardwareSystemDisk struct {
	BootedDisk  string                 `json:"booted_disk,omitempty"` // "/dev/sda"
	Replacement *SystemDiskReplacement `json:"replacement,omitempty"`
}

// SystemDiskReplacement is Core's durable record of one replacement, read from the host when
// asked for. It is the same record that lets an interrupted replacement continue after Core restarts
// or the Controller is replaced.
type SystemDiskReplacement struct {
	RequestID string `json:"request_id"`
	ByID      string `json:"by_id"`
	Serial    string `json:"serial"`
	SizeBytes uint64 `json:"size_bytes"`
	// Phase is accepted, laying (the partition table is being written to the replacement), slots (the verified
	// root slots are being copied), boot (boot partition and boot loader), data (the replacement is being added to
	// the nex-data mirror) or complete. Status is running, succeeded or failed.
	Phase      string     `json:"phase"`
	Status     string     `json:"status"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Resync is the kernel's own progress of the nex-data mirror onto the replacement, set once the
	// disk has been added to it.
	Resync *SystemDiskResync `json:"resync,omitempty"`
}

// SystemDiskResync is what the kernel reports for the nex-data array. Percent is set only while
// an action runs, so an idle array has no invented progress.
type SystemDiskResync struct {
	Array    string   `json:"array"`  // md127
	Member   string   `json:"member"` // the replacement's partition, sdb6
	State    string   `json:"state"`  // the member's kernel state: spare, in_sync, faulty, ...
	Action   string   `json:"action,omitempty"`
	Percent  *float64 `json:"percent,omitempty"`
	SpeedKBs int      `json:"speed_kbs,omitempty"`
	Degraded int      `json:"degraded"`
}
