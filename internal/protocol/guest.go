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
	Agent     string         `json:"agent"`
	OS        string         `json:"os,omitempty"`
	Hostname  string         `json:"hostname,omitempty"`
	Addresses []GuestAddress `json:"addresses,omitempty"`
	// Clipboard is "ready" when the guest opened the qemu-vdagent channel (a running
	// spice-vdagent), "waiting" when the channel exists but nothing opened it, or
	// "no-channel" when the VM was defined without console clipboard support.
	Clipboard string `json:"clipboard"`
	// Resize is true when the VM has a VirtIO display, so the console can ask the guest
	// to change its resolution to the browser's size.
	Resize bool `json:"resize"`
}

// GuestAddress is one guest-reported IP address in CIDR form on a guest interface.
type GuestAddress struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
}
