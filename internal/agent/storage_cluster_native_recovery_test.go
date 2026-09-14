// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestResumeApplyingClusterWithoutNativeReceiptReappliesDurableConfig(t *testing.T) {
	oldGate := installClusterStorageBootGate
	var gates atomic.Int32
	installClusterStorageBootGate = func(*Store) error { gates.Add(1); return nil }
	t.Cleanup(func() { installClusterStorageBootGate = oldGate })
	s := clusterRecoveryStore(t)
	oldConnect, oldApply, oldMonitor, oldPath := connectStorageCluster, applyNativeStorageCluster, validateStorageClusterSessionMonitor, clusterCorosyncAuthKeyPath
	t.Cleanup(func() {
		connectStorageCluster, applyNativeStorageCluster, validateStorageClusterSessionMonitor, clusterCorosyncAuthKeyPath = oldConnect, oldApply, oldMonitor, oldPath
	})
	clusterCorosyncAuthKeyPath = t.TempDir() + "/authkey"
	var applied atomic.Int32
	connectStorageCluster = func(_ context.Context, _ string, r protocol.StorageRegistration, _ protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		if gates.Load() == 0 {
			t.Fatal("native connect ran before the durable boot gate was installed")
		}
		return r, nil
	}
	applyNativeStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		applied.Add(1)
		return errors.New("native fixture")
	}
	validateStorageClusterSessionMonitor = func(context.Context, string, string, string) error { return nil }
	registration := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Name: "shared", Path: "/var/lib/nex-storage/repo-1", Kind: protocol.StorageKindISCSI, Generation: 1}
	registration.Source.ISCSI = &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:shared", LUN: 1, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "12345678-1234-1234-1234-123456789abc", MountPath: registration.Path, Mode: "clustered_gfs2", ClusterID: "cluster-1", LockTable: "cluster-1:repo", EligibleHostIDs: []string{"master-1", "member-1", "member-2"}}
	registration.Identity.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: registration.Source.ISCSI.TargetIQN, LUN: 1, WWID: registration.Source.ISCSI.WWID, FilesystemUUID: registration.Source.ISCSI.FilesystemUUID, FilesystemType: "gfs2", GFS2LockTable: "cluster-1:repo", GFS2Journals: 3}
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", registration); err != nil {
		t.Fatal(err)
	}
	config := protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: "repo-1", ClusterID: "cluster-1", Generation: 1, LockTable: "cluster-1:repo", DevicePath: "/dev/disk/by-id/scsi-3600508b400105e210000900000490000", MountPath: registration.Path}
	for i, host := range []string{"master-1", "member-1", "member-2"} {
		config.Nodes = append(config.Nodes, protocol.StorageClusterNode{HostID: host, CorosyncNodeID: uint32(i + 1), CorosyncAddress: "core" + string(rune('1'+i)) + ".example.test", Fence: protocol.StorageClusterFence{Kind: protocol.FenceVirsh, Endpoint: "fence.example.test", TargetID: "11111111-2222-3333-4444-55555555555" + string(rune('1'+i)), CredentialRef: "credential" + string(rune('1'+i)), SSHHostKeyRef: "hostkey" + string(rune('1'+i)), SSHLogin: "root", SSHPort: 22, VirshURI: "qemu:///system"}})
	}
	for _, n := range config.Nodes {
		config.FenceProofs = append(config.FenceProofs, protocol.StorageClusterFenceStatus{HostID: n.HostID, TargetID: n.Fence.TargetID, Status: "passed", ExclusionVerified: true, ObservedAt: time.Now(), ConfigGeneration: 1, ConfigDigest: config.Digest()})
	}
	op := storageClusterOperation{RequestID: "resume-native", Digest: "durable-resume-intent", MembershipGeneration: 1, Registration: registration, Config: config, Status: "applying"}
	for _, node := range config.Nodes {
		op.Members = append(op.Members, clusterMemberStatus{HostID: node.HostID, Status: protocol.StorageClusterStatus{RepositoryID: config.RepositoryID, ClusterID: config.ClusterID, SessionMonitorReady: true, Status: "pending"}, Generation: config.Generation, MembershipGeneration: 1, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "monitor", UpdatedAt: time.Now().UTC()})
	}
	// An old member omits the capability bit. It must block CIB application even
	// though the master itself has the new monitor.
	op.Members[1].Status.SessionMonitorReady = false
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	ResumeStorageClusterOperations(context.Background(), s)
	if applied.Load() != 0 {
		t.Fatal("old member capability omission reached native CIB apply")
	}
	state, err := loadStorageClusters(s)
	if err != nil || state.Operations[op.RequestID].Status != "applying" {
		t.Fatalf("monitor gate state=%#v err=%v", state.Operations[op.RequestID], err)
	}
	state.Operations[op.RequestID].Members[1].Status.SessionMonitorReady = true
	state.Operations[op.RequestID].Members[1].UpdatedAt = time.Now().UTC()
	if err := saveStorageClusters(s, state); err != nil {
		t.Fatal(err)
	}
	ResumeStorageClusterOperations(context.Background(), s)
	if applied.Load() != 1 {
		state, _ := loadStorageClusters(s)
		t.Fatalf("native apply calls = %d state=%#v", applied.Load(), state.Operations[op.RequestID])
	}
	// An existing durable applying/failed intent still installs (or verifies)
	// the gate on a later reconciliation even when this node's WWID is absent.
	// It must not advance to the CIB writer in that condition.
	gates.Store(0)
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		return protocol.StorageRegistration{}, errors.New("fixture WWID absent")
	}
	ResumeStorageClusterOperations(context.Background(), s)
	if gates.Load() != 1 || applied.Load() != 1 {
		state, _ := loadStorageClusters(s)
		t.Fatalf("missing WWID gate=%d native=%d state=%#v", gates.Load(), applied.Load(), state.Operations[op.RequestID])
	}
}

func TestResumeNeedsAttentionRetainsCompletedActivationReceipt(t *testing.T) {
	s, op := bootProjection(t, "needs_attention")
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", op.Registration); err != nil {
		t.Fatal(err)
	}
	op.Activation = &storageClusterActivation{
		RequestID: op.RequestID, OperationDigest: op.Digest, RepositoryID: op.Config.RepositoryID,
		Generation: op.Config.Generation, ConfigDigest: op.Config.Digest(), MembershipGeneration: op.MembershipGeneration,
		CommittedAt: time.Now().UTC(),
	}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	oldGate, oldConnect, oldAuthKeyPath := installClusterStorageBootGate, connectStorageCluster, clusterCorosyncAuthKeyPath
	t.Cleanup(func() {
		installClusterStorageBootGate, connectStorageCluster, clusterCorosyncAuthKeyPath = oldGate, oldConnect, oldAuthKeyPath
	})
	installClusterStorageBootGate = func(*Store) error { return nil }
	clusterCorosyncAuthKeyPath = t.TempDir() + "/authkey"
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		return protocol.StorageRegistration{}, errors.New("fixture stops after resume replay")
	}

	// A generic needs_attention retry queues the durable operation before
	// replaying it. Before the fix, that fresh replay record silently dropped
	// this completed initial-admission receipt.
	ResumeStorageClusterOperations(context.Background(), s)
	state, err := loadStorageClusters(s)
	if err != nil {
		t.Fatal(err)
	}
	got := state.Operations[op.RequestID]
	if got.Activation == nil || !got.Activation.CommittedAt.Equal(op.Activation.CommittedAt) || !clusterOperationActivated(got, mustMembership(t, s), got.Config) {
		t.Fatalf("resume replay lost completed activation receipt: %#v", got)
	}
}

func TestResumeWithNativeReceiptInstallsBootGateWithoutNativeReapply(t *testing.T) {
	for _, status := range []string{"applying", "healthy"} {
		t.Run(status, func(t *testing.T) {
			s, op := bootProjection(t, status)
			if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", op.Registration); err != nil {
				t.Fatal(err)
			}
			health := protocol.StorageClusterStatus{RepositoryID: op.Config.RepositoryID, ClusterID: op.Config.ClusterID, Healthy: true, Quorate: true, Mounted: true}
			op.Members = []clusterMemberStatus{{HostID: s.State.HostID, Status: health, Generation: op.Config.Generation, MembershipGeneration: op.MembershipGeneration, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: time.Now()}}
			if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
				t.Fatal(err)
			}
			oldGate, oldConnect, oldApply, oldFencing, oldHealth := installClusterStorageBootGate, connectStorageCluster, applyNativeStorageCluster, reconcileNativeStorageClusterFencing, observeStorageClusterHealth
			t.Cleanup(func() {
				installClusterStorageBootGate, connectStorageCluster, applyNativeStorageCluster, reconcileNativeStorageClusterFencing, observeStorageClusterHealth = oldGate, oldConnect, oldApply, oldFencing, oldHealth
			})
			var gates, connects, applies, fencing atomic.Int32
			installClusterStorageBootGate = func(*Store) error { gates.Add(1); return nil }
			connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
				connects.Add(1)
				return protocol.StorageRegistration{}, errors.New("missing WWID must not be retried")
			}
			applyNativeStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
				applies.Add(1)
				return errors.New("CIB must not be reconciled")
			}
			reconcileNativeStorageClusterFencing = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
				fencing.Add(1)
				return nil
			}
			observeStorageClusterHealth = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
				return health, nil
			}
			ResumeStorageClusterOperations(context.Background(), s)
			if gates.Load() != 1 || fencing.Load() != 1 || connects.Load() != 0 || applies.Load() != 0 {
				t.Fatalf("safe upgrade reconcile gate=%d fencing=%d connect=%d apply=%d", gates.Load(), fencing.Load(), connects.Load(), applies.Load())
			}
		})
	}
}

func TestResumeWithNativeReceiptRetriesFencingPolicyWithoutNativeReapply(t *testing.T) {
	s, op := bootProjection(t, "applying")
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", op.Registration); err != nil {
		t.Fatal(err)
	}
	health := protocol.StorageClusterStatus{RepositoryID: op.Config.RepositoryID, ClusterID: op.Config.ClusterID, Healthy: true, Quorate: true, Mounted: true}
	op.Members = []clusterMemberStatus{{HostID: s.State.HostID, Status: health, Generation: op.Config.Generation, MembershipGeneration: op.MembershipGeneration, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: time.Now()}}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	oldGate, oldConnect, oldApply, oldFencing, oldHealth := installClusterStorageBootGate, connectStorageCluster, applyNativeStorageCluster, reconcileNativeStorageClusterFencing, observeStorageClusterHealth
	t.Cleanup(func() {
		installClusterStorageBootGate, connectStorageCluster, applyNativeStorageCluster, reconcileNativeStorageClusterFencing, observeStorageClusterHealth = oldGate, oldConnect, oldApply, oldFencing, oldHealth
	})
	installClusterStorageBootGate = func(*Store) error { return nil }
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		t.Fatal("fencing policy retry reached iSCSI")
		return protocol.StorageRegistration{}, nil
	}
	applyNativeStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		t.Fatal("fencing policy retry reached resource/CIB apply")
		return nil
	}
	attempts := 0
	reconcileNativeStorageClusterFencing = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		attempts++
		if attempts == 1 {
			return errors.New("pcs unavailable")
		}
		return nil
	}
	observeStorageClusterHealth = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
		return health, nil
	}
	ResumeStorageClusterOperations(context.Background(), s)
	state, err := loadStorageClusters(s)
	if err != nil || state.Operations[op.RequestID].Status != "needs_attention" || state.Operations[op.RequestID].Error != clusterFencingPolicyReconcileFailure {
		t.Fatalf("fencing policy failure was not durable: %#v err=%v", state.Operations[op.RequestID], err)
	}
	ResumeStorageClusterOperations(context.Background(), s)
	state, err = loadStorageClusters(s)
	if err != nil || attempts != 2 || state.Operations[op.RequestID].Error != "" {
		t.Fatalf("fencing policy retry did not recover safely: attempts=%d state=%#v err=%v", attempts, state.Operations[op.RequestID], err)
	}
}

func TestLegacyTransportUpgradeRefreshesStaleMasterHealthBeforeReadinessGate(t *testing.T) {
	s, op := bootProjection(t, "healthy")
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", op.Registration); err != nil {
		t.Fatal(err)
	}
	op.Activation = &storageClusterActivation{RequestID: op.RequestID, OperationDigest: op.Digest, RepositoryID: op.Config.RepositoryID, Generation: op.Config.Generation, ConfigDigest: op.Config.Digest(), MembershipGeneration: op.MembershipGeneration, CommittedAt: time.Now().Add(-time.Minute)}
	health := func() protocol.StorageClusterStatus {
		return protocol.StorageClusterStatus{RepositoryID: op.Config.RepositoryID, ClusterID: op.Config.ClusterID, SessionMonitorReady: true, Status: "healthy", Healthy: true, Quorate: true, Mounted: true, FencingVerified: true, Fences: append([]protocol.StorageClusterFenceStatus(nil), op.Config.FenceProofs...)}
	}
	for _, node := range op.Config.Nodes {
		observed := time.Now().UTC()
		if node.HostID == s.State.HostID {
			observed = observed.Add(-3 * time.Minute)
		}
		op.Members = append(op.Members, clusterMemberStatus{HostID: node.HostID, Status: health(), Generation: op.Config.Generation, MembershipGeneration: op.MembershipGeneration, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: observed})
	}
	if missing, err := clusterMissingNativeReceipt(s, op); err != nil || missing {
		t.Fatalf("legacy fixture did not retain matching native receipt: missing=%v err=%v", missing, err)
	}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	if state, err := loadStorageClusters(s); err != nil || len(state.Operations[op.RequestID].Members) != 3 {
		t.Fatalf("legacy fixture members were not durable: state=%#v err=%v", state.Operations[op.RequestID], err)
	}
	if state, err := loadStorageClusters(s); err != nil {
		t.Fatal(err)
	} else if missing, err := clusterMissingNativeReceipt(s, state.Operations[op.RequestID]); err != nil || missing {
		t.Fatalf("durable legacy receipt was not recognized: missing=%v err=%v", missing, err)
	}
	oldGate, oldHealth, oldMonitor, oldTransport, oldFencing := installClusterStorageBootGate, observeStorageClusterHealth, validateStorageClusterSessionMonitor, reconcileNativeStorageClusterTransport, reconcileNativeStorageClusterFencing
	t.Cleanup(func() {
		installClusterStorageBootGate, observeStorageClusterHealth, validateStorageClusterSessionMonitor, reconcileNativeStorageClusterTransport, reconcileNativeStorageClusterFencing = oldGate, oldHealth, oldMonitor, oldTransport, oldFencing
	})
	installClusterStorageBootGate = func(*Store) error { return nil }
	observeStorageClusterHealth = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
		return health(), nil
	}
	validateStorageClusterSessionMonitor = func(context.Context, string, string, string) error { return nil }
	transportCalls := 0
	reconcileNativeStorageClusterTransport = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		transportCalls++
		return nil
	}
	reconcileNativeStorageClusterFencing = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		return nil
	}
	ResumeStorageClusterOperations(context.Background(), s)
	state, err := loadStorageClusters(s)
	if err != nil {
		t.Fatal(err)
	}
	got := state.Operations[op.RequestID]
	if transportCalls != 1 || got.Status != "healthy" || got.Activation == nil || !got.Activation.CommittedAt.Equal(op.Activation.CommittedAt) || time.Since(got.Members[0].UpdatedAt) > time.Minute || !got.Members[0].Status.SessionMonitorReady {
		t.Fatalf("legacy transport retry did not refresh and settle safely: calls=%d operation=%#v", transportCalls, got)
	}
	// A failed legacy upgrade follows its dedicated retry branch. It must refresh
	// the stale live master observation before testing readiness again, then
	// restore the observed healthy state rather than remain needs_attention.
	got.Status, got.Error = "needs_attention", clusterTransportMonitorReconcileFailure
	got.Members[0].UpdatedAt = time.Now().Add(-3 * time.Minute)
	state.Operations[got.RequestID] = got
	if err := saveStorageClusters(s, state); err != nil {
		t.Fatal(err)
	}
	ResumeStorageClusterOperations(context.Background(), s)
	state, err = loadStorageClusters(s)
	if err != nil {
		t.Fatal(err)
	}
	got = state.Operations[op.RequestID]
	if transportCalls != 2 || got.Status != "healthy" || got.Error != "" || time.Since(got.Members[0].UpdatedAt) > time.Minute {
		t.Fatalf("legacy transport needs_attention retry did not settle: calls=%d operation=%#v", transportCalls, got)
	}
}

func TestClusterFencingPolicyRejectsStaleMembershipBeforeCIBWrite(t *testing.T) {
	s, op := bootProjection(t, "applying")
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", op.Registration); err != nil {
		t.Fatal(err)
	}
	op.Members = []clusterMemberStatus{{HostID: s.State.HostID, Generation: op.Config.Generation, MembershipGeneration: op.MembershipGeneration, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: time.Now()}}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	peer, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	peer.Membership.Generation++
	if err := savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	old := reconcileNativeStorageClusterFencing
	t.Cleanup(func() { reconcileNativeStorageClusterFencing = old })
	var writes atomic.Int32
	reconcileNativeStorageClusterFencing = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		writes.Add(1)
		return nil
	}
	if err := reconcileClusterFencingPolicy(context.Background(), s, op); err == nil || writes.Load() != 0 {
		t.Fatalf("stale membership reached CIB reconciliation: writes=%d err=%v", writes.Load(), err)
	}
}

func TestClusterFencingPolicyCancellationDoesNotRestoreApplying(t *testing.T) {
	s, op := bootProjection(t, "needs_attention")
	op.Error = clusterFencingPolicyReconcileFailure
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", op.Registration); err != nil {
		t.Fatal(err)
	}
	op.Members = []clusterMemberStatus{{HostID: s.State.HostID, Generation: op.Config.Generation, MembershipGeneration: op.MembershipGeneration, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: time.Now()}}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	old := reconcileNativeStorageClusterFencing
	t.Cleanup(func() { reconcileNativeStorageClusterFencing = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reconcileNativeStorageClusterFencing = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		cancel() // A late successful command result must not promote this operation.
		return nil
	}
	ResumeStorageClusterOperations(ctx, s)
	state, err := loadStorageClusters(s)
	if err != nil || state.Operations[op.RequestID].Status != "needs_attention" || state.Operations[op.RequestID].Error != clusterFencingPolicyReconcileFailure {
		t.Fatalf("canceled policy reconcile promoted state: %#v err=%v", state.Operations[op.RequestID], err)
	}
}

func TestBootGateOnlyFailureIsDurableAndRetriesWithoutNativeReapply(t *testing.T) {
	s, op := bootProjection(t, "applying")
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", op.Registration); err != nil {
		t.Fatal(err)
	}
	health := protocol.StorageClusterStatus{RepositoryID: op.Config.RepositoryID, ClusterID: op.Config.ClusterID, Healthy: true, Quorate: true, Mounted: true}
	op.Members = []clusterMemberStatus{{HostID: s.State.HostID, Status: health, Generation: op.Config.Generation, MembershipGeneration: op.MembershipGeneration, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: time.Now()}}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	oldGate, oldConnect, oldApply := installClusterStorageBootGate, connectStorageCluster, applyNativeStorageCluster
	t.Cleanup(func() {
		installClusterStorageBootGate, connectStorageCluster, applyNativeStorageCluster = oldGate, oldConnect, oldApply
	})
	attempts := 0
	installClusterStorageBootGate = func(*Store) error {
		attempts++
		if attempts == 1 {
			return errors.New("fixture daemon reload failed")
		}
		return nil
	}
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		t.Fatal("gate-only retry reached iSCSI")
		return protocol.StorageRegistration{}, nil
	}
	applyNativeStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		t.Fatal("gate-only retry reached CIB")
		return nil
	}
	ResumeStorageClusterOperations(context.Background(), s)
	state, err := loadStorageClusters(s)
	if err != nil || state.Operations[op.RequestID].Status != "needs_attention" || state.Operations[op.RequestID].Error != clusterBootGateInstallFailure {
		t.Fatalf("gate failure not durable: state=%#v err=%v", state.Operations[op.RequestID], err)
	}
	ResumeStorageClusterOperations(context.Background(), s)
	state, err = loadStorageClusters(s)
	if err != nil || attempts != 2 || state.Operations[op.RequestID].Status != "applying" || state.Operations[op.RequestID].Error != "" {
		t.Fatalf("gate retry state=%#v attempts=%d err=%v", state.Operations[op.RequestID], attempts, err)
	}
}

func TestClusterNativeReceiptRequiresCommittedMatchingRegistration(t *testing.T) {
	for _, status := range []string{"applying", "healthy"} {
		t.Run(status, func(t *testing.T) {
			s, op := bootProjection(t, status)
			op.Members = []clusterMemberStatus{{HostID: s.State.HostID, Generation: op.Config.Generation, MembershipGeneration: op.MembershipGeneration, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: time.Now()}}
			if missing, err := clusterMissingNativeReceipt(s, op); err != nil || !missing {
				t.Fatal("health report without a managed registration suppressed recovery")
			}
			if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", op.Registration); err != nil {
				t.Fatal(err)
			}
			if missing, err := clusterMissingNativeReceipt(s, op); err != nil || missing {
				t.Fatal("matching committed registration was not accepted as a native receipt")
			}
			mismatched := op
			mismatched.Registration.Source.ISCSI = &protocol.ISCSIStorageSource{}
			*mismatched.Registration.Source.ISCSI = *op.Registration.Source.ISCSI
			mismatched.Registration.Source.ISCSI.WWID = "3600508b400105e210000900000490001"
			if missing, err := clusterMissingNativeReceipt(s, mismatched); err != nil || !missing {
				t.Fatal("mismatched durable registration suppressed recovery")
			}
		})
	}
}

func TestUnreadableClusterReceiptFailsClosedWithoutNativeReapply(t *testing.T) {
	s, op := bootProjection(t, "applying")
	op.Members = []clusterMemberStatus{{HostID: s.State.HostID, Generation: op.Config.Generation, MembershipGeneration: op.MembershipGeneration, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: time.Now()}}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	managedDir := s.dir + "/managed-vms"
	if err := os.MkdirAll(managedDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managedDir+"/corrupt.json", []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	oldConnect, oldApply := connectStorageCluster, applyNativeStorageCluster
	t.Cleanup(func() { connectStorageCluster, applyNativeStorageCluster = oldConnect, oldApply })
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		t.Fatal("unreadable receipt reached iSCSI")
		return protocol.StorageRegistration{}, nil
	}
	applyNativeStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		t.Fatal("unreadable receipt reached CIB")
		return nil
	}
	ResumeStorageClusterOperations(context.Background(), s)
	state, err := loadStorageClusters(s)
	if err != nil || state.Operations[op.RequestID].Status != "needs_attention" || state.Operations[op.RequestID].Error != clusterNativeReceiptUnavailable {
		t.Fatalf("unreadable receipt was not fail-closed: state=%#v err=%v", state.Operations[op.RequestID], err)
	}
	// Repeated unreadable ticks remain a receipt-only error; they never fall
	// through the generic needs-attention native recovery branch.
	ResumeStorageClusterOperations(context.Background(), s)
	state, err = loadStorageClusters(s)
	if err != nil || state.Operations[op.RequestID].Error != clusterNativeReceiptUnavailable {
		t.Fatalf("unreadable receipt retry changed state: %#v err=%v", state.Operations[op.RequestID], err)
	}
	if err = os.Remove(managedDir + "/corrupt.json"); err != nil {
		t.Fatal(err)
	}
	if err = vmhost.CommitClusteredStorageRegistration(context.Background(), managedDir, op.Registration); err != nil {
		t.Fatal(err)
	}
	ResumeStorageClusterOperations(context.Background(), s)
	state, err = loadStorageClusters(s)
	if err != nil || state.Operations[op.RequestID].Status != "applying" || state.Operations[op.RequestID].Error != "" {
		t.Fatalf("readable receipt did not recover without native replay: %#v err=%v", state.Operations[op.RequestID], err)
	}
}
