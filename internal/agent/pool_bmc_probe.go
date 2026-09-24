// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// probeBMC is a package variable so a lab can stand in for a real management controller.
var probeBMC = vmhost.ProbeBMC

// ExecutePoolBMCProbe answers a brokered bmc-probe on the master, the host that will fence
// through the controller. It is an answer, not a record: nothing it learns is written to the
// membership, and the password handed over for it is retired whatever the outcome, so a probe
// leaves no credential behind.
func ExecutePoolBMCProbe(ctx context.Context, s *Store, request protocol.PoolRequest) (result protocol.PoolResult) {
	result = protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "failed", ObservedAt: time.Now().UTC()}
	if request.Action != "bmc-probe" || request.Valid() != nil || request.HostID != s.State.HostID || request.PoolID != s.State.PoolID {
		result.Error = "pool request does not address this host"
		return result
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		result.Error = "pool membership is unavailable"
		return result
	}
	secret := ""
	if request.CredentialRef != "" {
		// A probe may only open material handed over for it. A reference that names a
		// recorded fence device would send that device's password to whatever address the
		// probe was given.
		if poolFenceCredentialNamed(s, state, request.CredentialRef) {
			result.Error = "this credential belongs to a recorded fence device, so it cannot be used to probe a management controller"
			return result
		}
		secret, err = readProbeSecret(s, request)
		if err != nil {
			result.Error = "the management controller probe credential is unavailable"
			return result
		}
		defer func() {
			if err := retireProbeSecret(s, request.CredentialRef); err != nil {
				result.Status = "failed"
				result.Error = "the management controller probe credential could not be retired"
				result.BMCProbe = nil
			}
		}()
	}
	if state.Membership.MasterHostID != s.State.HostID {
		result.Error = "only the pool master probes a management controller"
		return result
	}
	member, ok := poolMemberByID(state.Membership, request.MemberHostID)
	if !ok || member.Status == "ejected" {
		result.Error = "no such member of this pool"
		return result
	}
	probe, err := probeBMC(ctx, request.Endpoint, request.Username, secret, member.SystemUUID)
	if probe.Valid() == nil {
		result.BMCProbe = &probe
	}
	if err != nil {
		result.Error = resultError(err.Error())
		return result
	}
	result.Status = "succeeded"
	return result
}

// poolFenceCredentialNamed reports whether anything this host keeps names the reference:
// a recorded profile, this host's own provisioned account, or a storage cluster's fencing.
// Unreadable cluster state names everything, because it proves nothing is unused.
func poolFenceCredentialNamed(s *Store, state poolPeerState, ref string) bool {
	for _, member := range state.Membership.Members {
		if member.Fence != nil && member.Fence.CredentialRef == ref {
			return true
		}
	}
	if state.LocalBMC != nil && state.LocalBMC.Ref == ref {
		return true
	}
	clusters, err := loadStorageClusters(s)
	if err != nil {
		return true
	}
	for _, operation := range clusters.Operations {
		for _, node := range operation.Config.Nodes {
			if node.Fence.CredentialRef == ref {
				return true
			}
		}
	}
	return false
}
