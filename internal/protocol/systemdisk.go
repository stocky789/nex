// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"errors"
	"regexp"
	"strings"
)

// SystemDiskReplaceCapability gates rebuilding a degraded system-disk mirror. A Core without
// it never receives the command: the Controller refuses the request instead.
const SystemDiskReplaceCapability = "host-system-disk-replace-v1"

var mdArrayName = regexp.MustCompile(`^md[0-9]+$`)

// SystemDiskReplaceRequest rebuilds one degraded md array onto one replacement disk. The
// device is the one the operator picked from the host's own ZFS device discovery listing,
// with the same identity, fingerprint and typed erase confirmation rules; Core revalidates
// all of it against a fresh observation before it touches the disk.
type SystemDiskReplaceRequest struct {
	OperationID string             `json:"operation_id"`
	HostID      string             `json:"host_id"`
	Array       string             `json:"array"` // md0
	Device      ZFSProvisionDevice `json:"device"`
}

func (r SystemDiskReplaceRequest) Valid() error {
	if err := requireNonEmptyBoundedID(r.OperationID, "system disk replacement requires an operation ID"); err != nil {
		return err
	}
	if !storageOpaqueID.MatchString(r.OperationID) {
		return errors.New("system disk replacement requires an operation ID")
	}
	if !storageOpaqueID.MatchString(r.HostID) {
		return errors.New("system disk replacement requires a host")
	}
	if !mdArrayName.MatchString(r.Array) {
		return errors.New("system disk replacement names one md array, such as md0")
	}
	d := r.Device
	if !strings.HasPrefix(d.ByID, "/dev/disk/by-id/") || strings.ContainsAny(d.ByID, "\x00\r\n") || d.Serial == "" || d.SizeBytes == 0 || !zfsDeviceFingerprint.MatchString(d.Fingerprint) {
		return errors.New("system disk replacement device is invalid")
	}
	if d.EraseConfirmation != "" && d.EraseConfirmation != d.ByID {
		return errors.New("the erase confirmation must name the selected device")
	}
	return nil
}
