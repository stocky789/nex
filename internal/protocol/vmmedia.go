// SPDX-License-Identifier: AGPL-3.0-only
package protocol

// MediaCapability is reported by agents that insert or eject a managed VM's ISO.
const MediaCapability = "vm-media-v1"

// DriversDriveCapability is reported by agents that define a VM's second CD drive and accept
// a drive in a media request. Controllers must not send drive="drivers" to an agent without it.
const DriversDriveCapability = "vm-media-drivers-drive-v1"

// VMMediaRequest asks the assigned host to insert or eject one VM's CDROM media.
// Insert requires media_id of an ISO already imported on that host.
type VMMediaRequest struct {
	VMID   string `json:"vm_id"`
	PoolID string `json:"pool_id"`
	HostID string `json:"host_id"`
	Action string `json:"action"`
	// Drive names which of the VM's two CD drives the request applies to: "install" (or "",
	// the value every Controller sent before the second drive existed) or "drivers".
	Drive   string `json:"drive,omitempty"`
	MediaID string `json:"media_id,omitempty"`
}

// CD drives a VM carries, named in VMMediaRequest.Drive.
const (
	InstallDrive = "install"
	DriversDrive = "drivers"
)

// ValidMediaAction reports whether a is insert or eject.
func ValidMediaAction(a string) bool {
	switch a {
	case "insert", "eject":
		return true
	}
	return false
}

// ValidMediaDrive reports whether d names a CD drive. "" means the install drive.
func ValidMediaDrive(d string) bool {
	switch d {
	case "", InstallDrive, DriversDrive:
		return true
	}
	return false
}

// ApplyVMMedia records an insert or eject in a VM spec. Both Core (recordVMMedia) and
// Controller (mirrorVMMedia) call this so their mirrors of a media change cannot diverge.
// Only the named drive's media ID changes. libvirt's change-media swaps the CD drive's source
// and leaves every device's bootindex alone, the drive's own included, so an eject leaves the
// empty drive in the boot order and an insert never invents one. Recording an eject as a boot
// order change would make the durable record describe a domain the host does not have, and,
// since a pool-shared VM applies its media change as a vm-update, would make NeedsStop demand a
// stop for every eject of a running install-media-first VM (BUG-0188).
func ApplyVMMedia(v VMCreateSpec, action, drive, mediaID string) VMCreateSpec {
	if action == "eject" {
		mediaID = ""
	}
	if drive == DriversDrive {
		v.DriversMediaID = mediaID
		return v
	}
	v.MediaID = mediaID
	return v
}
