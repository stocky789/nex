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
// Only media_id changes. libvirt's change-media swaps the CD drive's source and leaves every
// device's bootindex alone, the drive's own included, so an eject leaves the empty drive in
// the boot order and an insert never invents one. Recording an eject as a boot order change
// would make the durable record describe a domain the host does not have, and, since a
// pool-shared VM applies its media change as a vm-update, would make NeedsStop demand a stop
// for every eject of a running install-media-first VM (BUG-0188).
func ApplyVMMedia(v VMCreateSpec, action, mediaID string) VMCreateSpec {
	if action == "eject" {
		v.MediaID = ""
		return v
	}
	v.MediaID = mediaID
	return v
}
