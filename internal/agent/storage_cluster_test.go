// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestStorageClusterPeerRejectsUnauthenticatedRequest(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	r := httptest.NewRequest(http.MethodGet, poolStorageClusterPath, nil)
	w := httptest.NewRecorder()
	PoolStorageClusterHandler(s).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated peer status = %d", w.Code)
	}
}

func TestStorageClusterFenceRequestIsDurablyOneShot(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	var calls atomic.Int32
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	old := verifyStorageClusterFenceNative
	verifyStorageClusterFenceNative = func(ctx context.Context, _ string, c protocol.StorageClusterConfig, host string) (protocol.StorageClusterFenceStatus, error) {
		calls.Add(1)
		<-release
		return protocol.StorageClusterFenceStatus{HostID: host, TargetID: c.Nodes[0].Fence.TargetID, Status: "uncertain", ConfigGeneration: c.Generation, ConfigDigest: c.Digest(), ObservedAt: time.Now()}, context.Canceled
	}
	t.Cleanup(func() { verifyStorageClusterFenceNative = old })
	request := protocol.StorageClusterRequest{RequestID: "fence-once", PoolID: "pool-1", HostID: "master-1", Action: "verify_fence", FenceHostID: "host-2", FenceConfirmation: "FENCE host-2", Config: protocol.StorageClusterConfig{RepositoryID: "repo-1", Generation: 1, Nodes: []protocol.StorageClusterNode{{HostID: "host-2", Fence: protocol.StorageClusterFence{TargetID: "target-2"}}}}}
	request.Config.ClusterID, request.Config.LockTable = "cluster-1", "cluster-1:shared"
	request.Config.DevicePath, request.Config.MountPath = "/dev/disk/by-id/scsi-example", "/var/lib/nex-storage/repo-1"
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{"cluster-1": {RequestID: "cluster-1", Config: request.Config}}}); err != nil {
		t.Fatal(err)
	}
	if ExecuteStorageClusterRequest(context.Background(), s, request).Status != "accepted" {
		t.Fatal("first fence request was not accepted")
	}
	if ExecuteStorageClusterRequest(context.Background(), s, request).Status != "accepted" {
		t.Fatal("replay did not return durable fence state")
	}
	pending := clusterOperationsPublic(s)
	if len(pending) != 1 || pending[0].Status != "verifying_fence" || len(pending[0].Config.FenceProofs) != 1 || pending[0].Config.FenceProofs[0].RequestID != request.RequestID || pending[0].Config.FenceProofs[0].Status != "unverified" {
		t.Fatalf("accepted fence lacks visible pending evidence: %#v", pending)
	}
	competing := request
	competing.RequestID = "another-fence-request"
	if ExecuteStorageClusterRequest(context.Background(), s, competing).Status != "failed" {
		t.Fatal("overlapping destructive request was accepted")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	var recorded, settled time.Time
	for {
		storageClusterMu.Lock()
		state, err := loadStorageClusters(s)
		storageClusterMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if state.FenceRequests[request.RequestID].Status == "needs_attention" {
			// The request goroutine rewrites the state once more after the native result
			// is recorded; wait for that write so TempDir cleanup never races it.
			if !state.FenceRequests[request.RequestID].UpdatedAt.Equal(recorded) {
				recorded = state.FenceRequests[request.RequestID].UpdatedAt
				settled = time.Now().Add(100 * time.Millisecond)
			}
			if time.Now().After(settled) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("fence outcome did not become durable")
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("native fence calls = %d", calls.Load())
	}
	if got := clusterOperationsPublic(s); len(got) != 1 || got[0].RequestID != "cluster-1" {
		t.Fatalf("fence intent leaked into public operations: %#v", got)
	}
	changed := request
	changed.FenceHostID = "host-3"
	changed.FenceConfirmation = "FENCE host-3"
	if ExecuteStorageClusterRequest(context.Background(), s, changed).Status != "failed" {
		t.Fatal("changed fence replay accepted")
	}
}

func TestStorageClusterFenceRejectsUnresolvedBootstrap(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	request := protocol.StorageClusterRequest{RequestID: "fence-before-bootstrap", PoolID: "pool-1", HostID: "master-1", Action: "verify_fence", FenceHostID: "host-2", FenceConfirmation: "FENCE host-2", Config: protocol.StorageClusterConfig{RepositoryID: "repo-1"}}
	op := storageClusterOperation{RequestID: "setup-1", Config: request.Config, Status: "needs_attention", Error: "clustered iSCSI bootstrap failed"}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	if got := ExecuteStorageClusterRequest(context.Background(), s, request); got.Status != "failed" {
		t.Fatalf("unresolved bootstrap accepted destructive fence: %s", got.Status)
	}
	state, err := loadStorageClusters(s)
	if err != nil || len(state.FenceRequests) != 0 || len(state.Operations[op.RequestID].Config.FenceProofs) != 0 {
		t.Fatal("refused fence changed bootstrap state")
	}
}

func TestStorageClusterPublicMemberReportPreservesObservedIdentity(t *testing.T) {
	s := clusterRecoveryStore(t)
	now := time.Now().UTC().Round(0)
	digest := strings.Repeat("a", 64)
	op := storageClusterOperation{RequestID: "operation-1", MembershipGeneration: 1, Config: protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: "repo-1", ClusterID: "cluster-1", Generation: 1}, Status: "applying", Activation: &storageClusterActivation{RequestID: "operation-1", OperationDigest: "private", RepositoryID: "repo-1", Generation: 1, ConfigDigest: digest, MembershipGeneration: 1, CommittedAt: now}, Members: []clusterMemberStatus{{HostID: "member-1", Status: protocol.StorageClusterStatus{RepositoryID: "repo-1", ClusterID: "cluster-1", Healthy: true, Quorate: true, Mounted: true, Status: "healthy"}, Generation: 1, MembershipGeneration: 1, ClusterID: "cluster-1", ConfigDigest: digest, Stage: "health", UpdatedAt: now}}}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(clusterOperationsPublic(s))
	if err != nil {
		t.Fatal(err)
	}
	var wire []protocol.StorageClusterOperation
	if err = json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 1 || len(wire[0].Members) != 1 {
		t.Fatalf("public JSON lost member reports: %s", b)
	}
	if strings.Contains(string(b), "activation") || strings.Contains(string(b), "operation_digest") {
		t.Fatalf("private activation receipt leaked through public operation JSON: %s", b)
	}
	member := wire[0].Members[0]
	if member.HostID != "member-1" || member.Status.Status != "healthy" || !member.Status.Healthy || member.Generation != 1 || member.MembershipGeneration != 1 || member.ClusterID != "cluster-1" || member.ConfigDigest != digest || member.Stage != "health" || !member.UpdatedAt.Equal(now) {
		t.Fatalf("public JSON changed member report: %#v", member)
	}
}

func TestStorageClusterDirectFenceProofRetainsPriorRequestReceipt(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	config := protocol.StorageClusterConfig{ClusterID: "cluster-1", PoolID: "pool-1", RepositoryID: "repo-1", Generation: 1, LockTable: "cluster-1:shared", DevicePath: "/dev/disk/by-id/scsi-example", MountPath: "/var/lib/nex-storage/repo-1", Nodes: []protocol.StorageClusterNode{{HostID: "host-2", Fence: protocol.StorageClusterFence{TargetID: "target-2"}}, {HostID: "host-3", Fence: protocol.StorageClusterFence{TargetID: "target-3"}}}}
	first, second := "direct-proof-first", "direct-proof-second"
	state := storageClusterState{Operations: map[string]storageClusterOperation{"setup": {RequestID: "setup", Config: config, Status: "awaiting_fencing"}}, FenceRequests: map[string]storageFenceRequest{first: {RequestID: first, RepositoryID: config.RepositoryID, TargetHostID: "host-2", ConfigDigest: config.Digest(), Status: "verifying"}}}
	if err := saveStorageClusters(s, state); err != nil {
		t.Fatal(err)
	}
	old := verifyStorageClusterFenceNative
	var n atomic.Int32
	verifyStorageClusterFenceNative = func(_ context.Context, _ string, c protocol.StorageClusterConfig, host string) (protocol.StorageClusterFenceStatus, error) {
		return protocol.StorageClusterFenceStatus{HostID: host, TargetID: "target-2", Status: "passed", ExclusionVerified: true, ObservedAt: time.Unix(100+int64(n.Add(1)), 0), ConfigGeneration: c.Generation, ConfigDigest: c.Digest()}, nil
	}
	t.Cleanup(func() { verifyStorageClusterFenceNative = old })
	if err := verifyStorageClusterFence(context.Background(), s, first, config.RepositoryID, "host-2"); err != nil {
		t.Fatal(err)
	}
	// Read the receipt through a new Store, matching the crash window between
	// direct proof persistence and the asynchronous request-result observer.
	dir := s.dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	storageClusterMu.Lock()
	state, err = loadStorageClusters(s)
	if err == nil && state.FenceRequests[first].Status != "succeeded" {
		err = errors.New("direct fence receipt remained pending after proof persistence")
	}
	if err == nil {
		state.FenceRequests[second] = storageFenceRequest{RequestID: second, RepositoryID: config.RepositoryID, TargetHostID: "host-2", ConfigDigest: config.Digest(), Status: "verifying"}
		err = saveStorageClusters(s, state)
	}
	storageClusterMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyStorageClusterFence(context.Background(), s, second, config.RepositoryID, "host-2"); err != nil {
		t.Fatal(err)
	}
	storageClusterMu.Lock()
	state, err = loadStorageClusters(s)
	storageClusterMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if state.FenceRequests[first].Proof == nil || state.FenceRequests[first].Proof.RequestID != first || !state.FenceRequests[first].Proof.ObservedAt.Equal(time.Unix(101, 0)) {
		t.Fatalf("first direct proof was not retained: %#v", state.FenceRequests[first])
	}
	if state.FenceRequests[second].Proof == nil || state.FenceRequests[second].Proof.RequestID != second {
		t.Fatalf("second direct proof was not recorded: %#v", state.FenceRequests[second])
	}
	current := state.Operations["setup"].Config.FenceProofs
	if len(current) != 1 || current[0].RequestID != second {
		t.Fatalf("current proof was not replaced by re-verification: %#v", current)
	}
}

func TestMemberClusterReconcileStartsLocalServicesWithoutCIBApply(t *testing.T) {
	oldGate := installClusterStorageBootGate
	installClusterStorageBootGate = func(*Store) error { return nil }
	t.Cleanup(func() { installClusterStorageBootGate = oldGate })
	master := peerStore(t, "master-1", "pool-1")
	member := peerStore(t, "member-1", "other-pool")
	other := peerStore(t, "member-2", "other-pool")
	joinPeerMember(t, master, member)
	joinPeerMember(t, master, other)
	poolPeerMu.Lock()
	masterState, err := loadPoolPeer(master)
	if err == nil {
		memberState, memberErr := loadPoolPeer(member)
		if memberErr != nil {
			err = memberErr
		} else {
			memberState.Membership = masterState.Membership
			err = savePoolPeer(member, memberState)
		}
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	registration := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Name: "shared", Path: "/var/lib/nex-storage/repo-1", Kind: protocol.StorageKindISCSI, Generation: 1}
	registration.Source.ISCSI = &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:shared", LUN: 1, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "12345678-1234-1234-1234-123456789abc", MountPath: registration.Path, Mode: "clustered_gfs2", ClusterID: "cluster-1", LockTable: "cluster-1:repo-1", EligibleHostIDs: []string{"master-1", "member-1", "member-2"}}
	registration.Identity.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: registration.Source.ISCSI.TargetIQN, LUN: 1, WWID: registration.Source.ISCSI.WWID, FilesystemUUID: registration.Source.ISCSI.FilesystemUUID, FilesystemType: "gfs2", GFS2LockTable: "cluster-1:repo-1", GFS2Journals: 3}
	config := protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: registration.ID, ClusterID: "cluster-1", Generation: 1, LockTable: "cluster-1:repo-1", DevicePath: "/dev/disk/by-id/scsi-3600508b400105e210000900000490000", MountPath: registration.Path}
	for i, host := range []string{"master-1", "member-1", "member-2"} {
		config.Nodes = append(config.Nodes, protocol.StorageClusterNode{HostID: host, CorosyncNodeID: uint32(i + 1), CorosyncAddress: "core" + string(rune('1'+i)) + ".example.test", Fence: protocol.StorageClusterFence{Kind: protocol.FenceVirsh, Endpoint: "fence.example.test", TargetID: "11111111-2222-3333-4444-55555555555" + string(rune('1'+i)), CredentialRef: "credential" + string(rune('1'+i)), SSHHostKeyRef: "hostkey" + string(rune('1'+i)), SSHLogin: "root", SSHPort: 22, VirshURI: "qemu:///system"}})
	}
	for _, node := range config.Nodes {
		config.FenceProofs = append(config.FenceProofs, protocol.StorageClusterFenceStatus{HostID: node.HostID, TargetID: node.Fence.TargetID, Status: "passed", ExclusionVerified: true, ObservedAt: time.Now(), ConfigGeneration: config.Generation, ConfigDigest: config.Digest()})
	}
	op := storageClusterOperation{RequestID: "setup-1", Digest: "durable-member-intent", MembershipGeneration: masterState.Membership.Generation, Registration: registration, Config: config, Status: "applying"}
	if err := saveStorageClusters(master, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolStorageClusterHandler(master))
	server.TLS = tlsConfig
	server.StartTLS()
	defer server.Close()
	poolPeerMu.Lock()
	memberState, err := loadPoolPeer(member)
	if err == nil {
		memberState.MasterURL = server.URL
		err = savePoolPeer(member, memberState)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	oldConnect, oldStart, oldApply, oldValidate, oldHealth, oldKeyPath := connectStorageCluster, startNativeStorageCluster, applyNativeStorageCluster, validateNativeStorageClusterAccess, observeStorageClusterHealth, clusterCorosyncAuthKeyPath
	t.Cleanup(func() {
		connectStorageCluster, startNativeStorageCluster, applyNativeStorageCluster, validateNativeStorageClusterAccess, observeStorageClusterHealth, clusterCorosyncAuthKeyPath = oldConnect, oldStart, oldApply, oldValidate, oldHealth, oldKeyPath
	})
	clusterCorosyncAuthKeyPath = t.TempDir() + "/authkey"
	var connected, started, applied, validated atomic.Int32
	sequence := []string{}
	connectStorageCluster = func(_ context.Context, _ string, r protocol.StorageRegistration, _ protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		connected.Add(1)
		sequence = append(sequence, "connect")
		return r, nil
	}
	startNativeStorageCluster = func(context.Context, string, protocol.StorageClusterConfig) error {
		started.Add(1)
		sequence = append(sequence, "start")
		return nil
	}
	validateNativeStorageClusterAccess = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		validated.Add(1)
		sequence = append(sequence, "validate")
		return nil
	}
	observeStorageClusterHealth = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
		return protocol.StorageClusterStatus{RepositoryID: registration.ID, ClusterID: config.ClusterID, Healthy: true, Quorate: true, Mounted: true, FencingVerified: true, Status: "healthy"}, nil
	}
	applyNativeStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		applied.Add(1)
		return nil
	}
	if err := ReconcileStorageClustersFromMaster(context.Background(), member); err != nil {
		t.Fatalf("healthy post-start validation failed: %v", err)
	}
	if connected.Load() != 1 || started.Load() != 1 || validated.Load() != 1 || applied.Load() != 0 || strings.Join(sequence, ",") != "connect,start,validate" {
		t.Fatalf("member native sequence=%v connect=%d start=%d validate=%d apply=%d", sequence, connected.Load(), started.Load(), validated.Load(), applied.Load())
	}
	validateNativeStorageClusterAccess = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		validated.Add(1)
		sequence = append(sequence, "validate-fail")
		return errors.New("QEMU access denied")
	}
	if err := ReconcileStorageClustersFromMaster(context.Background(), member); err == nil || !strings.Contains(err.Error(), "clustered iSCSI preparation failed") {
		t.Fatalf("post-start access-validation failure = %v", err)
	}
	if validated.Load() != 2 || strings.Join(sequence, ",") != "connect,start,validate,connect,start,validate-fail" {
		t.Fatalf("post-start denial bypassed validator: sequence=%v validate=%d", sequence, validated.Load())
	}
}

func TestMasterPostApplyValidationFailureRemainsNeedsAttention(t *testing.T) {
	s, op := bootProjection(t, "queued")
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), s.dir+"/managed-vms", op.Registration); err != nil {
		t.Fatal(err)
	}
	if err := SaveClusterCorosyncAuthKey(s, s.State.PoolID, make([]byte, 128)); err != nil {
		t.Fatal(err)
	}
	for _, node := range op.Config.Nodes {
		op.Members = append(op.Members, clusterMemberSessionMonitorReport(node.HostID, op.Config, mustMembership(t, s), true))
	}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	oldGate, oldConnect, oldApply, oldValidate, oldHealth, oldMonitor, oldKeyPath := installClusterStorageBootGate, connectStorageCluster, applyNativeStorageCluster, validateNativeStorageClusterAccess, observeStorageClusterHealth, validateStorageClusterSessionMonitor, clusterCorosyncAuthKeyPath
	t.Cleanup(func() {
		installClusterStorageBootGate, connectStorageCluster, applyNativeStorageCluster, validateNativeStorageClusterAccess, observeStorageClusterHealth, validateStorageClusterSessionMonitor, clusterCorosyncAuthKeyPath = oldGate, oldConnect, oldApply, oldValidate, oldHealth, oldMonitor, oldKeyPath
	})
	clusterCorosyncAuthKeyPath = t.TempDir() + "/authkey"
	installClusterStorageBootGate = func(*Store) error { return nil }
	sequence := []string{}
	connectStorageCluster = func(_ context.Context, _ string, registration protocol.StorageRegistration, _ protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		sequence = append(sequence, "connect")
		return registration, nil
	}
	applyNativeStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		sequence = append(sequence, "apply")
		return nil
	}
	validateNativeStorageClusterAccess = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
		sequence = append(sequence, "validate")
		return errors.New("QEMU access denied")
	}
	observeStorageClusterHealth = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
		return protocol.StorageClusterStatus{RepositoryID: op.Config.RepositoryID, ClusterID: op.Config.ClusterID, Healthy: true, Quorate: true, Mounted: true, FencingVerified: true, Status: "healthy"}, nil
	}
	validateStorageClusterSessionMonitor = func(context.Context, string, string, string) error { return nil }
	ResumeStorageClusterOperations(context.Background(), s)
	state, err := loadStorageClusters(s)
	if err != nil {
		t.Fatal(err)
	}
	got := state.Operations[op.RequestID]
	if strings.Join(sequence, ",") != "connect,apply,validate" || got.Status != "needs_attention" || got.Error != "local cluster health is not confirmed" {
		t.Fatalf("post-apply validation bypassed failure guard: sequence=%v operation=%#v", sequence, got)
	}
}

func TestStorageClusterProofsQueueResumeOnlyWhenAllCurrentProofsMatch(t *testing.T) {
	s := clusterRecoveryStore(t)
	c := protocol.StorageClusterConfig{ClusterID: "cluster-1", RepositoryID: "repo-123456", Generation: 1, Nodes: []protocol.StorageClusterNode{{HostID: "master-1"}, {HostID: "member-1"}, {HostID: "member-2"}}}
	for _, n := range c.Nodes {
		c.FenceProofs = append(c.FenceProofs, protocol.StorageClusterFenceStatus{HostID: n.HostID, Status: "passed", TargetID: n.Fence.TargetID, ExclusionVerified: true, ObservedAt: time.Now(), ConfigGeneration: 1, ConfigDigest: c.Digest()})
	}
	op := storageClusterOperation{RequestID: "cluster-request-1", MembershipGeneration: 1, Config: c, Status: "awaiting_fencing"}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	pullClusterFenceProofs(context.Background(), s, &op)
	got := StorageClusterStatus(s)[0]
	if got.Status != "queued" {
		t.Fatalf("proof resume status %q", got.Status)
	}
}

func TestStorageClusterCandidateIsCoreAllocatedAndPathFree(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	request := StorageClusterRequest{Registration: protocol.StorageRegistration{Name: "shared", Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:shared", WWID: "naa.1234", FilesystemUUID: "12345678-1234-1234-1234-123456789abc"}}}, Config: protocol.StorageClusterConfig{Nodes: []protocol.StorageClusterNode{{HostID: "master-1"}, {HostID: "member-1"}, {HostID: "member-2"}}}}
	if err := clusterCandidate(s, &request); err != nil {
		t.Fatal(err)
	}
	if request.Registration.ID == "" || request.Registration.ID != request.Config.RepositoryID || request.Registration.Path != "" || request.Registration.Source.ISCSI.MountPath != "" || request.Config.DevicePath != "" || request.Config.MountPath != "" {
		t.Fatalf("unsafe candidate %#v", request)
	}
}

func TestStorageClusterRequestReplayUsesDurableResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	request := StorageClusterRequest{RequestID: "cluster-request-1"}
	state := storageClusterState{Operations: map[string]storageClusterOperation{request.RequestID: {RequestID: request.RequestID, Digest: clusterDigest(request), Status: "needs_attention"}}}
	if err = saveStorageClusters(s, state); err != nil {
		t.Fatal(err)
	}
	got, err := ApplyStorageCluster(context.Background(), s, request)
	if err != nil || got.Status != "needs_attention" {
		t.Fatalf("durable replay = %#v, %v", got, err)
	}
	changed := request
	changed.ExpectedGeneration = 1
	if _, err = ApplyStorageCluster(context.Background(), s, changed); err == nil {
		t.Fatal("changed replay input was accepted")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err = ApplyStorageCluster(context.Background(), s, request); err != nil || got.Status != "needs_attention" {
		t.Fatalf("restart replay = %#v, %v", got, err)
	}
}

func TestStorageClusterConfigLookupRejectsUnknownRepository(t *testing.T) {
	s := peerStore(t, "member-1", "pool-1")
	if _, err := StorageClusterConfigForRepository(s, "missing"); err == nil {
		t.Fatal("missing config accepted")
	}
}

func TestStorageClusterRelayRejectsAnotherHostScope(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	result := ExecuteStorageClusterRequest(context.Background(), s, StorageClusterRequest{RequestID: "cluster-request-2", Action: "status", PoolID: "pool-1", HostID: "member-1"})
	if result.Status != "failed" {
		t.Fatalf("foreign host scope result = %#v", result)
	}
}

func TestStorageClusterEmptyProofListIsJSONArray(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	member := peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	server := httptest.NewUnstartedServer(PoolStorageClusterHandler(member))
	config, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	poolPeerMu.Lock()
	masterState, err := loadPoolPeer(master)
	if err == nil {
		for i := range masterState.Membership.Members {
			if masterState.Membership.Members[i].HostID == member.State.HostID {
				masterState.Membership.Members[i].PeerOrigin = server.URL
			}
		}
		err = savePoolPeer(master, masterState)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	client, err := PoolMemberPeerClient(master, server.URL, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL + poolStorageClusterPath + "/proof/repository-1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body json.RawMessage
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" || string(body) != "[]" {
		t.Fatalf("empty proofs = %d %q %s", response.StatusCode, response.Header.Get("Content-Type"), body)
	}
}
