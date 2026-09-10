// SPDX-License-Identifier: AGPL-3.0-only
package protocol

// ConsoleCapability is reported by agents that can attach a running VM's local
// VNC display to a Controller session.
const ConsoleCapability = "console-v1"

// HostConsoleCapability is reported by agents whose host console (a root shell
// rendered as RFB) honours this Controller's ticketing. It is distinct from
// ConsoleCapability, which every agent old enough to support VM consoles already
// advertises: without a separate capability, a new Controller would enable the
// host console tab against an old Core that has never implemented it, issue a
// ticket, and leave the browser to hang until the 504 timeout instead of getting
// the immediate "upgrade Nex Core" refusal.
const HostConsoleCapability = "host-console-v1"

// ConsoleRequest asks the enrolled host to connect a local RFB socket to the
// Controller. An empty VMID means the host's own console. It is delivered once
// through the report response and is not a durable operation: the token is a
// single-use pairing secret that expires within a minute, and the host must
// reconnect only over its enrolled mTLS identity.
type ConsoleRequest struct {
	Token string `json:"token"`
	VMID  string `json:"vm_id,omitempty"`
}
