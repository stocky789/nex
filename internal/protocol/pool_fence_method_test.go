// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"testing"
	"time"
)

func TestPoolPromotionFenceRejectsUnknownMethod(t *testing.T) {
	now := time.Now().UTC()
	proof := &StorageClusterFenceStatus{
		Method: "unsupported", HostID: "master-1", TargetID: "target-1", RequestID: "op-1",
		Status: "passed", ExclusionVerified: true, ConfigGeneration: 7,
		ConfigDigest: PoolPromotionFenceDigest("pool-1", "master-1", "op-1", 7), ObservedAt: now,
	}
	fence := &StorageClusterFence{Kind: FenceVirsh, TargetID: "target-1", Endpoint: "fence.example.test", CredentialRef: "cred-1", VirshURI: "qemu:///system", SSHLogin: "fence", SSHPort: 22, SSHHostKeyRef: "key-1"}
	member := PoolMember{HostID: "master-1", Fence: fence}
	if err := PoolPromotionFenceVerified(proof, member, "pool-1", "master-1", "op-1", 7, now); err == nil {
		t.Fatal("unknown fence method accepted")
	}
}
