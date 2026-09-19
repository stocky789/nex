// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func discoverZFSDevices(ctx context.Context, request protocol.StorageDiscoveryRequest) protocol.StorageDiscoveryResult {
	result := protocol.StorageDiscoveryResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "failed", ObservedAt: time.Now().UTC()}
	if err := protocol.ValidateStorageDiscovery(request); err != nil {
		result.Error = err.Error()
		return result
	}
	devices, err := vmhost.DiscoverZFSDevices(ctx)
	if err != nil {
		result.Error = "ZFS device discovery is unavailable"
		return result
	}
	for _, device := range devices {
		eligibility := device.Reason
		if device.Eligible && device.RequiresErase {
			eligibility = "erase_required"
		}
		if device.Eligible && !device.RequiresErase {
			eligibility = "blank"
		}
		result.Candidates = append(result.Candidates, protocol.StorageDiscoveryCandidate{Kind: request.Kind, Device: &protocol.ZFSDevice{ByID: device.ByID, Model: device.Model, Serial: device.Serial, SizeBytes: device.SizeBytes, Rotational: device.Rotational, Eligibility: eligibility, Fingerprint: device.Fingerprint}})
	}
	result.Status = "succeeded"
	return result
}
