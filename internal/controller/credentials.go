// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"strings"

	"nex.local/nex/internal/protocol"
)

// scopedCredentialValid applies the bounds shared by clustered-storage and fence-profile
// credential delivery: an SSH fence takes a key and known hosts and no password, a BMC or
// CHAP fence takes a password and no key material, and neither carries control characters.
func scopedCredentialValid(kind string, v protocol.StorageCredentials) bool {
	if len(v.Username) > 4096 || len(v.Secret) > 65536 || len(v.SSHPrivateKey) > 65536 || len(v.SSHKnownHosts) > 65536 {
		return false
	}
	if strings.ContainsAny(v.Username+v.Secret+v.SSHPrivateKey+v.SSHKnownHosts, "\x00\r") {
		return false
	}
	if kind == "fence_ssh" {
		return v.Secret == "" && v.SSHPrivateKey != "" && v.SSHKnownHosts != ""
	}
	if strings.ContainsAny(v.Username+v.Secret, "\n") {
		return false
	}
	// A fence account's password becomes a script fence_ipmilan runs, and the agent strips
	// the whitespace around what it printed. Core refuses those passwords itself; the same
	// refusal here means the operator is told at the form rather than by a failed fence.
	if kind == "fence_bmc" && strings.TrimSpace(v.Secret) != v.Secret {
		return false
	}
	return v.Secret != "" && v.SSHPrivateKey == "" && v.SSHKnownHosts == ""
}
