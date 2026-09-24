// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"testing"
	"time"
)

func clusteredRegistration() StorageRegistration {
	return StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Generation: 2, Kind: StorageKindISCSI, Path: "/var/lib/nex-storage/repo-1", Source: StorageSource{ISCSI: &ISCSIStorageSource{Portal: "iscsi.example.test", TargetIQN: "iqn.2026-09.example:vms", LUN: 1, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "11111111-2222-4333-8444-555555555555", MountPath: "/var/lib/nex-storage/repo-1", Mode: "clustered_gfs2", ClusterID: "pool-1", LockTable: "pool-1:repo-1", EligibleHostIDs: []string{"master-1", "member-2", "member-3"}}}}
}

func clusteredConfig(s StorageRegistration) StorageClusterConfig {
	return StorageClusterConfig{ClusterID: "pool-1", PoolID: s.PoolID, RepositoryID: s.ID, Generation: s.Generation, LockTable: "pool-1:repo-1", DevicePath: "/dev/disk/by-id/scsi-3600508b400105e210000900000490000", MountPath: s.Path, Nodes: []StorageClusterNode{{HostID: "master-1", CorosyncNodeID: 1, CorosyncAddress: "core1.example.test", Fence: StorageClusterFence{Kind: FenceRedfish, Endpoint: "bmc1.example.test", TargetID: "system1", CredentialRef: "fence1"}}, {HostID: "member-2", CorosyncNodeID: 2, CorosyncAddress: "core2.example.test", Fence: StorageClusterFence{Kind: FenceRedfish, Endpoint: "bmc2.example.test", TargetID: "system2", CredentialRef: "fence2"}}, {HostID: "member-3", CorosyncNodeID: 3, CorosyncAddress: "core3.example.test", Fence: StorageClusterFence{Kind: FenceRedfish, Endpoint: "bmc3.example.test", TargetID: "system3", CredentialRef: "fence3"}}}}
}

func TestClusteredISCSIRequiresFullCoreProfile(t *testing.T) {
	s := clusteredRegistration()
	if err := ValidateStoredStorageRegistration(s, false); err != nil {
		t.Fatal(err)
	}
	if !IsPoolSharedStorage(s) {
		t.Fatal("clustered iSCSI is not marked pool shared")
	}
	c := clusteredConfig(s)
	if err := c.ValidFor(s); err != nil {
		t.Fatal(err)
	}
	requested := clusteredRegistration()
	requested.Path, requested.Source.ISCSI.MountPath, requested.Source.ISCSI.ClusterID, requested.Source.ISCSI.LockTable = "", "", "", ""
	if err := ValidateStorageRegistration(requested, false); err != nil {
		t.Fatalf("path-free GFS2 discovery request rejected: %v", err)
	}
	if ValidateStoredStorageRegistration(requested, false) == nil {
		t.Fatal("unobserved clustered GFS2 record accepted")
	}
	s.Source.ISCSI.EligibleHostIDs = s.Source.ISCSI.EligibleHostIDs[:2]
	if err := ValidateStoredStorageRegistration(s, false); err == nil {
		t.Fatal("two-node clustered profile accepted")
	}
}

func TestFencingProofBindsExactConfig(t *testing.T) {
	s, c := clusteredRegistration(), clusteredConfig(clusteredRegistration())
	if c.FencingVerified() {
		t.Fatal("missing proof accepted")
	}
	for _, node := range c.Nodes {
		c.FenceProofs = append(c.FenceProofs, StorageClusterFenceStatus{HostID: node.HostID, Status: "passed", TargetID: node.Fence.TargetID, ExclusionVerified: true, ObservedAt: time.Now(), ConfigGeneration: c.Generation, ConfigDigest: c.Digest()})
	}
	if !c.FencingVerified() {
		t.Fatal("exact fence evidence rejected")
	}
	c.FenceProofs[0].TargetID = "other-system"
	if c.FencingVerified() {
		t.Fatal("proof for another fence target accepted")
	}
	c.FenceProofs[0].TargetID = c.Nodes[0].Fence.TargetID
	c.Generation++
	if c.FencingVerified() {
		t.Fatal("proof survived configuration generation change")
	}
	_ = s
}

func TestBMCTargetIDFollowsRedfishIdRules(t *testing.T) {
	// Dell iDRAC and fence_redfish use System.Embedded.1; the old opaque-ID check rejected the dots.
	base := StorageClusterFence{Kind: FenceRedfish, Endpoint: "bmc.example.test", TargetID: "System.Embedded.1", Username: "root", CredentialRef: "fence-cred"}
	if err := base.Valid(); err != nil {
		t.Fatalf("Dell Redfish system Id rejected: %v", err)
	}
	for _, id := range []string{"1", "system-1", "System.Embedded.1", "blade.1-2"} {
		f := base
		f.TargetID = id
		if err := f.Valid(); err != nil {
			t.Fatalf("valid BMC target %q rejected: %v", id, err)
		}
		f.Kind = FenceIPMI
		if err := f.Valid(); err != nil {
			t.Fatalf("valid IPMI plug %q rejected: %v", id, err)
		}
	}
	// RFC1738 unsafe / path breakers must stay refused (DSP0266 §6.1 / §9.6.6).
	for _, id := range []string{"", ".Embedded", "System Embedded.1", "System/Embedded.1", "System#1", "System|1", "System~1", "{sys}", "a%2Eb"} {
		f := base
		f.TargetID = id
		if err := f.Valid(); err == nil {
			t.Fatalf("unsafe BMC target %q accepted", id)
		}
	}
}

func TestVirshURIIsBoundToPinnedSSHTransport(t *testing.T) {
	base := StorageClusterFence{Kind: FenceVirsh, Endpoint: "fence.example.test", TargetID: "11111111-2222-4333-8444-555555555555", CredentialRef: "credential", SSHHostKeyRef: "hostkey", SSHLogin: "fence", SSHPort: 2222}
	for _, uri := range []string{"qemu:///system", "qemu+ssh://fence@fence.example.test:2222/system"} {
		f := base
		f.VirshURI = uri
		if err := f.Valid(); err != nil {
			t.Fatalf("valid URI %q rejected: %v", uri, err)
		}
	}
	for _, uri := range []string{"qemu+ssh://other@fence.example.test:2222/system", "qemu+ssh://fence@other.example.test:2222/system", "qemu+ssh://fence@fence.example.test/system", "qemu+ssh://fence@fence.example.test:2222/session", "qemu+ssh://fence:password@fence.example.test:2222/system", "qemu+ssh://fence@fence.example.test:2222/system?proxy=x", "test:///default"} {
		f := base
		f.VirshURI = uri
		if err := f.Valid(); err == nil {
			t.Fatalf("unbound URI %q accepted", uri)
		}
	}
}
