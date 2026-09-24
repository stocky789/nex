// SPDX-License-Identifier: AGPL-3.0-only
package protocol

// GuestInfo is what the host observed inside a running VM through its host-local
// virtio channels. Facts come from the QEMU guest agent when it answers; channel
// states come from the live libvirt definition. It is observation, never proof of
// guest health, and is absent for stopped VMs and from older Core builds.
type GuestInfo struct {
	// Agent is "connected" when the guest agent answered, "unresponsive" when the
	// channel is attached but it did not, "disconnected" when nothing in the guest
	// has opened the channel, or "no-channel" when the VM definition lacks it.
	Agent string `json:"agent"`
	OS    string `json:"os,omitempty"`
	// OSID is the guest-reported guest-get-osinfo ID (POSIX os-release ID or mswindows).
	OSID      string         `json:"os_id,omitempty"`
	Hostname  string         `json:"hostname,omitempty"`
	Addresses []GuestAddress `json:"addresses,omitempty"`
	// Clipboard is "ready" when the qemu-vdagent channel is connected and the guest-agent
	// channel is not disconnected (evidence of a running guest vdagent; the vdagent port
	// alone can read connected in firmware or with no OS), "waiting" when the channel exists
	// but that evidence is missing, or "no-channel" when the VM was defined without console
	// clipboard support.
	Clipboard string `json:"clipboard"`
	// Resize is true when the guest itself can follow a console resize request: the VM has a
	// VirtIO display *and* the guest is running the agent that acts on it. A VirtIO display
	// alone only means QEMU would accept the request; a guest without the driver ignores it,
	// and offering the control then does nothing (BUG-0201).
	Resize bool `json:"resize"`
	// Media is the live CD drive of the running VM: "loaded" when its tray holds media,
	// "empty" when the drive is there but holds none (a guest can open its own tray, for
	// example when an installer ejects the disc it booted), "no-drive" when the VM has no
	// CD drive. Empty from Core builds that do not observe it. It describes what the guest
	// currently sees, never what Nex recorded; the durable media_id changes only through a
	// media request.
	Media string `json:"media,omitempty"`
	// DriversMedia is the same observation for the VM's second (driver disc) CD drive.
	// Empty from Core builds that define only one drive.
	DriversMedia string `json:"drivers_media,omitempty"`
}

// Media observations reported in GuestInfo.Media.
const (
	MediaLoaded  = "loaded"
	MediaEmpty   = "empty"
	MediaNoDrive = "no-drive"
)

// GuestAddress is one guest-reported IP address in CIDR form on a guest interface.
type GuestAddress struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
}
