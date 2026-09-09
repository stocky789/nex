// SPDX-License-Identifier: AGPL-3.0-only
package protocol

// ConsoleCapability is reported by agents that can attach a running VM's local VNC
// socket to a Controller console session.
const ConsoleCapability = "console-v1"

// ConsoleRequest asks the enrolled host to connect one running VM's VNC socket to
// the Controller. It is delivered once through the report response and is not a
// durable operation: the token is a single-use pairing secret that expires within
// a minute, and the host must reconnect only over its enrolled mTLS identity.
type ConsoleRequest struct {
	Token string `json:"token"`
	VMID  string `json:"vm_id"`
}
