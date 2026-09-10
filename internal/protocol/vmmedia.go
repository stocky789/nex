// SPDX-License-Identifier: AGPL-3.0-only
package protocol

// MediaCapability is reported by agents that insert or eject a managed VM's ISO.
const MediaCapability = "vm-media-v1"

// VMMediaRequest asks the assigned host to insert or eject one VM's CDROM media.
// Insert requires media_id of an ISO already imported on that host.
type VMMediaRequest struct {
	VMID    string `json:"vm_id"`
	PoolID  string `json:"pool_id"`
	HostID  string `json:"host_id"`
	Action  string `json:"action"`
	MediaID string `json:"media_id,omitempty"`
}

// ValidMediaAction reports whether a is insert or eject.
func ValidMediaAction(a string) bool {
	switch a {
	case "insert", "eject":
		return true
	}
	return false
}

// ApplyVMMedia records an insert or eject in a VM spec. Both Core (recordVMMedia) and
// Controller (mirrorVMMedia) call this so their mirrors of a media change cannot diverge.
// Boot order is deliberately untouched: libvirt's change-media only swaps the CD drive's
// source, and an attached tray carries no boot element, so a media change never alters any
// device's bootindex on the host. Recording one here would make the durable record claim a
// boot device the domain does not have. An eject drops the media boot order with the media,
// leaving the disks' orders exactly as the domain still holds them, which is why
// ValidateVMSpec accepts the gap that leaves.
func ApplyVMMedia(v VMCreateSpec, action, mediaID string) VMCreateSpec {
	if action == "eject" {
		v.MediaID = ""
		v.Boot.MediaBootOrder = 0
		return v
	}
	v.MediaID = mediaID
	return v
}
