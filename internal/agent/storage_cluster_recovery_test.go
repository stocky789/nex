// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestClusterOriginalInputDigestSurvivesDurableRoundTrip(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	op := storageClusterOperation{
		RequestID: "cluster-original-request", InputDigest: "original-path-free-input",
		Digest: "core-resolved-input", Status: "awaiting_fencing",
	}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStorageClusters(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Operations[op.RequestID].InputDigest; got != op.InputDigest {
		t.Fatalf("original request digest was lost across disk round trip: got %q", got)
	}
}

func TestClusterProofPollingCannotSkipQueuedBootstrap(t *testing.T) {
	s := clusterRecoveryStore(t)
	op := storageClusterOperation{RequestID: "fresh-bootstrap", MembershipGeneration: 1, Status: "queued",
		Config: protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: "repo-123456", Generation: 1,
			Nodes: []protocol.StorageClusterNode{{HostID: "master-1"}, {HostID: "member-1"}, {HostID: "member-2"}}}}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	pullClusterFenceProofs(context.Background(), s, &op)
	state, err := loadStorageClusters(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Operations[op.RequestID].Status; got != "queued" {
		t.Fatalf("proof polling skipped pending bootstrap by changing status to %q", got)
	}
}

func TestClusterSessionMonitorReadinessRequiresEveryFreshCurrentMember(t *testing.T) {
	s := clusterRecoveryStore(t)
	config := protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: "repo-monitor", ClusterID: "cluster-monitor", Generation: 1,
		Nodes: []protocol.StorageClusterNode{{HostID: "master-1"}, {HostID: "member-1"}, {HostID: "member-2"}}}
	makeReport := func(host string) clusterMemberStatus {
		return clusterMemberStatus{HostID: host, Status: protocol.StorageClusterStatus{RepositoryID: config.RepositoryID, ClusterID: config.ClusterID, SessionMonitorReady: true}, Generation: config.Generation, MembershipGeneration: 1, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "health", UpdatedAt: time.Now().UTC()}
	}
	op := storageClusterOperation{RequestID: "monitor-ready", MembershipGeneration: 1, Config: config}
	for _, node := range config.Nodes {
		op.Members = append(op.Members, makeReport(node.HostID))
	}
	if !clusterAllMembersSessionMonitorReady(op, mustMembership(t, s)) {
		t.Fatal("all current monitor-ready members did not pass")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*storageClusterOperation)
	}{
		{"old member omitted readiness", func(op *storageClusterOperation) { op.Members[1].Status.SessionMonitorReady = false }},
		{"stale report", func(op *storageClusterOperation) { op.Members[1].UpdatedAt = time.Now().Add(-3 * time.Minute) }},
		{"wrong config generation", func(op *storageClusterOperation) { op.Members[1].Generation++ }},
		{"wrong membership generation", func(op *storageClusterOperation) { op.Members[1].MembershipGeneration++ }},
		{"foreign member", func(op *storageClusterOperation) { op.Members[1].HostID = "foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := op
			candidate.Members = append([]clusterMemberStatus(nil), op.Members...)
			tc.mutate(&candidate)
			if clusterAllMembersSessionMonitorReady(candidate, mustMembership(t, s)) {
				t.Fatal("unsafe readiness report enabled monitor")
			}
		})
	}
}

func TestClusterSessionMonitorReadinessRequiresNativeAttestation(t *testing.T) {
	s, op := bootProjection(t, "applying")
	_ = s
	old := validateStorageClusterSessionMonitor
	t.Cleanup(func() { validateStorageClusterSessionMonitor = old })
	calls := 0
	validateStorageClusterSessionMonitor = func(_ context.Context, portal, target, sessionID string) error {
		calls++
		if portal != "san.example.test:3260" || target != op.Registration.Source.ISCSI.TargetIQN || sessionID == "" {
			t.Fatalf("monitor capability did not use canonical fixed transport parameters: %q %q %q", portal, target, sessionID)
		}
		return errors.New("monitor unavailable")
	}
	if clusterSessionMonitorReady(context.Background(), op.Registration) {
		t.Fatal("registration metadata inferred monitor capability")
	}
	validateStorageClusterSessionMonitor = func(context.Context, string, string, string) error { return nil }
	if !clusterSessionMonitorReady(context.Background(), op.Registration) || calls != 1 {
		t.Fatalf("native monitor attestation was not required: calls=%d", calls)
	}
}

func TestLatestClusterOperationRejectsHistoricalGenerationConflict(t *testing.T) {
	_, op := bootProjection(t, "healthy")
	older := op
	older.RequestID, older.UpdatedAt = "older", time.Now().Add(-time.Minute)
	conflict := older
	conflict.RequestID, conflict.Config.LockTable = "older-conflict", "cluster-boot:conflict"
	newer := op
	newer.RequestID, newer.Config.Generation, newer.Registration.Generation = "newer", 2, 2
	state := storageClusterState{Operations: map[string]storageClusterOperation{
		older.RequestID: older, conflict.RequestID: conflict, newer.RequestID: newer,
	}}
	for i := 0; i < 32; i++ {
		if key, _ := latestClusterOperation(state, op.Config.RepositoryID); key != "" {
			t.Fatalf("historical conflict selected %q on pass %d", key, i)
		}
	}
}

func TestClusterFenceReducerPreservesPartialAndBadProofStates(t *testing.T) {
	config := protocol.StorageClusterConfig{ClusterID: "cluster-1", RepositoryID: "repo-123456", Generation: 1, Nodes: []protocol.StorageClusterNode{{HostID: "master-1"}, {HostID: "member-1"}, {HostID: "member-2"}}}
	proof := func(host, status string) protocol.StorageClusterFenceStatus {
		return protocol.StorageClusterFenceStatus{HostID: host, Status: status, ObservedAt: time.Now(), ConfigGeneration: 1, ConfigDigest: config.Digest()}
	}
	partial := storageClusterOperation{Status: "verifying_fence", Config: config}
	partial.Config.FenceProofs = []protocol.StorageClusterFenceStatus{proof("master-1", "passed")}
	reduceFenceOperation(&partial)
	if partial.Status != "awaiting_fencing" {
		t.Fatalf("partial proof status %q", partial.Status)
	}
	pending := partial
	pending.Config.FenceProofs = append(pending.Config.FenceProofs, proof("member-1", "unverified"))
	reduceFenceOperation(&pending)
	if pending.Status != "verifying_fence" {
		t.Fatalf("pending proof status %q", pending.Status)
	}
	bad := pending
	bad.Config.FenceProofs = append(bad.Config.FenceProofs, proof("member-2", "uncertain"))
	reduceFenceOperation(&bad)
	if bad.Status != "needs_attention" {
		t.Fatalf("survivor uncertain status %q", bad.Status)
	}
}

func TestAcceptedFenceProofInvalidatesOnlyPreFenceTargetHealth(t *testing.T) {
	s := clusterRecoveryStore(t)
	now := time.Now().UTC().Round(0)
	config := protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: "repo-fence-health", ClusterID: "cluster-fence-health", Generation: 1, LockTable: "cluster-fence-health:repo-fence-health"}
	for i, host := range []string{"master-1", "member-1", "member-2"} {
		config.Nodes = append(config.Nodes, protocol.StorageClusterNode{HostID: host, CorosyncNodeID: uint32(i + 1), Fence: protocol.StorageClusterFence{TargetID: "target-" + host}})
	}
	for _, node := range config.Nodes {
		config.FenceProofs = append(config.FenceProofs, protocol.StorageClusterFenceStatus{RequestID: "initial-" + node.HostID, HostID: node.HostID, TargetID: node.Fence.TargetID, Status: "passed", ExclusionVerified: true, ObservedAt: now.Add(-time.Minute), ConfigGeneration: config.Generation, ConfigDigest: config.Digest()})
	}
	health := func(fences []protocol.StorageClusterFenceStatus) protocol.StorageClusterStatus {
		return protocol.StorageClusterStatus{RepositoryID: config.RepositoryID, ClusterID: config.ClusterID, Status: "healthy", Healthy: true, Quorate: true, Mounted: true, FencingVerified: true, Fences: append([]protocol.StorageClusterFenceStatus(nil), fences...)}
	}
	op := storageClusterOperation{RequestID: "fence-health", Digest: "durable-fence-health", MembershipGeneration: 1, Config: config, Status: "healthy"}
	op.Activation = &storageClusterActivation{RequestID: op.RequestID, OperationDigest: op.Digest, RepositoryID: config.RepositoryID, Generation: config.Generation, ConfigDigest: config.Digest(), MembershipGeneration: 1, CommittedAt: now.Add(-2 * time.Minute)}
	for _, node := range config.Nodes {
		op.Members = append(op.Members, clusterMemberStatus{HostID: node.HostID, Status: health(config.FenceProofs), Generation: config.Generation, MembershipGeneration: 1, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "health", UpdatedAt: now.Add(-30 * time.Second)})
	}
	promoteClusterOperation(s, &op)
	if op.Status != "healthy" {
		t.Fatalf("activated baseline health = %#v", op)
	}

	proof := protocol.StorageClusterFenceStatus{RequestID: "refence-member-2", HostID: "member-2", TargetID: "target-member-2", Status: "passed", ExclusionVerified: true, ObservedAt: now, ConfigGeneration: config.Generation, ConfigDigest: config.Digest()}
	// The direct verifier used to persist this proof, run the fencing reducer
	// (which intentionally leaves an activated operation alone), and return
	// without re-promoting. Exercise that production path: it must persist a
	// degraded operation before any asynchronous apply worker can run.
	direct := op
	direct.RequestID, direct.Digest = "direct-fence-health", "durable-direct-fence-health"
	direct.Activation = &storageClusterActivation{RequestID: direct.RequestID, OperationDigest: direct.Digest, RepositoryID: config.RepositoryID, Generation: config.Generation, ConfigDigest: config.Digest(), MembershipGeneration: 1, CommittedAt: now.Add(-2 * time.Minute)}
	requestID := "direct-refence-member-2"
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{direct.RequestID: direct}, FenceRequests: map[string]storageFenceRequest{requestID: {RequestID: requestID, RepositoryID: config.RepositoryID, TargetHostID: proof.HostID, ConfigDigest: config.Digest(), Status: "verifying"}}}); err != nil {
		t.Fatal(err)
	}
	oldVerify := verifyStorageClusterFenceNative
	verifyStorageClusterFenceNative = func(context.Context, string, protocol.StorageClusterConfig, string) (protocol.StorageClusterFenceStatus, error) {
		return proof, nil
	}
	t.Cleanup(func() { verifyStorageClusterFenceNative = oldVerify })
	storageClusterWorkers.Lock()
	if storageClusterWorkers.active[s] == nil {
		storageClusterWorkers.active[s] = map[string]bool{}
	}
	storageClusterWorkers.active[s][direct.RequestID] = true
	storageClusterWorkers.Unlock()
	t.Cleanup(func() {
		storageClusterWorkers.Lock()
		delete(storageClusterWorkers.active[s], direct.RequestID)
		storageClusterWorkers.Unlock()
	})
	if err := verifyStorageClusterFence(context.Background(), s, requestID, config.RepositoryID, proof.HostID); err != nil {
		t.Fatal(err)
	}
	state, err := loadStorageClusters(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Operations[direct.RequestID]; got.Status != "degraded" || len(got.Members) != 2 {
		t.Fatalf("direct accepted proof did not immediately degrade target health: %#v", got)
	}

	if !acceptClusterFenceProof(&op, proof) {
		t.Fatal("newer passed proof was not accepted")
	}
	promoteClusterOperation(s, &op)
	if op.Status != "degraded" || op.Activation == nil || len(op.Members) != 2 {
		t.Fatalf("fence did not invalidate stale target health: %#v", op)
	}

	// A report already sent before the fence can reach the master after the
	// proof. Its old fence view must not restore a healthy operation.
	inflight := clusterMemberStatus{HostID: "member-2", Status: health(config.FenceProofs[:2]), Generation: config.Generation, MembershipGeneration: 1, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "health", UpdatedAt: now.Add(time.Second)}
	if clusterMemberReportAcknowledgesCurrentFence(inflight, op.Config) {
		t.Fatalf("pre-fence report acknowledged a newer proof: %#v", inflight)
	}

	// A target that has received the accepted proof can post a new health
	// observation and restore all-member health.
	fresh := inflight
	fresh.Status = health(op.Config.FenceProofs)
	fresh.UpdatedAt = now.Add(2 * time.Second)
	if !clusterMemberReportAcknowledgesCurrentFence(fresh, op.Config) {
		t.Fatalf("fresh target report did not acknowledge accepted proof: %#v", fresh)
	}
	op.Members = append(op.Members, fresh)
	promoteClusterOperation(s, &op)
	if op.Status != "healthy" {
		t.Fatalf("fresh target health did not restore healthy status: %#v", op)
	}

	stale := proof
	stale.ObservedAt = now.Add(-time.Second)
	if acceptClusterFenceProof(&op, stale) {
		t.Fatal("older proof replaced current target proof")
	}
	promoteClusterOperation(s, &op)
	if op.Status != "healthy" || len(op.Members) != 3 {
		t.Fatalf("older proof changed newer health: %#v", op)
	}
}

func clusterRecoveryStore(t *testing.T) *Store {
	t.Helper()
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1}
	for i, id := range []string{"master-1", "member-1", "member-2"} {
		membership.Members = append(membership.Members, protocol.PoolMember{HostID: id, Hostname: id, Generation: 1, Status: "ready", CertificateSHA256: strings.Repeat(string(rune('a'+i)), 64)})
	}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if _, err := Membership(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClusterRestartResumesOriginalCandidateWithoutReconstructedDigest(t *testing.T) {
	original := bootstrapStorageCluster
	t.Cleanup(func() { bootstrapStorageCluster = original })
	for _, stage := range []string{"queued", "applying"} {
		t.Run(stage, func(t *testing.T) {
			s := clusterRecoveryStore(t)
			calls := 0
			bootstrapStorageCluster = func(_ context.Context, _ string, registration protocol.StorageRegistration, config protocol.StorageClusterConfig) (protocol.StorageRegistration, protocol.StorageClusterConfig, error) {
				calls++
				if registration.ID != "repo-123456" || config.RepositoryID != registration.ID || registration.Path != "" {
					t.Fatalf("recovery changed accepted candidate: %#v", registration)
				}
				return registration, config, errors.New("native fixture unavailable")
			}
			op := storageClusterOperation{RequestID: "original-request", Digest: "digest-of-original-accepted-command", InputDigest: "digest-of-original-browser-input", MembershipGeneration: 1, Status: stage,
				Config:       protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: "repo-123456", Generation: 1},
				Registration: protocol.StorageRegistration{ID: "repo-123456", PoolID: "pool-1", Generation: 1, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{}}}}
			if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
				t.Fatal(err)
			}
			ResumeStorageClusterOperations(context.Background(), s)
			state, err := loadStorageClusters(s)
			if err != nil || calls != 1 || len(state.Operations) != 1 || state.Operations[op.RequestID].Config.RepositoryID != "repo-123456" {
				t.Fatalf("restart failed to resume accepted identity: calls=%d state=%#v err=%v", calls, state, err)
			}
		})
	}
}

func TestClusterMasterHealthRefreshReplacesExpiredOwnObservation(t *testing.T) {
	s := clusterRecoveryStore(t)
	config := protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: "repo-123456", ClusterID: "cluster-1", Generation: 1,
		Nodes: []protocol.StorageClusterNode{{HostID: "master-1"}, {HostID: "member-1"}, {HostID: "member-2"}}}
	op := storageClusterOperation{RequestID: "healthy-operation", MembershipGeneration: 1, Config: config, Status: "healthy"}
	health := protocol.StorageClusterStatus{RepositoryID: config.RepositoryID, ClusterID: config.ClusterID, Healthy: true, Quorate: true, Mounted: true, FencingVerified: true, Status: "healthy"}
	for _, node := range config.Nodes {
		op.Config.FenceProofs = append(op.Config.FenceProofs, protocol.StorageClusterFenceStatus{HostID: node.HostID, Status: "passed", ExclusionVerified: true, ObservedAt: time.Now(), ConfigGeneration: config.Generation, ConfigDigest: config.Digest()})
	}
	health.Fences = append([]protocol.StorageClusterFenceStatus(nil), op.Config.FenceProofs...)
	for _, node := range config.Nodes {
		op.Members = append(op.Members, clusterMemberStatus{HostID: node.HostID, Status: health, Generation: 1, MembershipGeneration: 1, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), UpdatedAt: time.Now()})
	}
	op.Members[0].UpdatedAt = time.Now().Add(-time.Hour)
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	original := observeStorageClusterHealth
	t.Cleanup(func() { observeStorageClusterHealth = original })
	var observedStateDir string
	observeStorageClusterHealth = func(_ context.Context, stateDir string, _ protocol.StorageRegistration, _ protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
		observedStateDir = stateDir
		return health, nil
	}
	refreshClusterMasterHealth(context.Background(), s, op)
	if observedStateDir != s.dir {
		t.Fatalf("health state root = %q, want %q", observedStateDir, s.dir)
	}
	state, err := loadStorageClusters(s)
	if err != nil || state.Operations[op.RequestID].Status != "healthy" || time.Since(state.Operations[op.RequestID].Members[0].UpdatedAt) > time.Minute {
		t.Fatalf("master health did not refresh: %#v %v", state, err)
	}
	observeStorageClusterHealth = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
		return health, errors.New("native observation unavailable")
	}
	refreshClusterMasterHealth(context.Background(), s, op)
	state, err = loadStorageClusters(s)
	if err != nil || state.Operations[op.RequestID].Status == "healthy" {
		t.Fatalf("failed native observation retained readiness: %#v %v", state, err)
	}
}

func TestClusterActivationReceiptSeparatesInitialAdmissionFromDegradedRuntime(t *testing.T) {
	s := clusterRecoveryStore(t)
	config := protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: "repo-activation", ClusterID: "cluster-activation", Generation: 1, LockTable: "cluster-activation:repo-activation"}
	for i, host := range []string{"master-1", "member-1", "member-2"} {
		config.Nodes = append(config.Nodes, protocol.StorageClusterNode{HostID: host, CorosyncNodeID: uint32(i + 1), Fence: protocol.StorageClusterFence{TargetID: "target-" + host}})
	}
	for _, node := range config.Nodes {
		config.FenceProofs = append(config.FenceProofs, protocol.StorageClusterFenceStatus{HostID: node.HostID, TargetID: node.Fence.TargetID, Status: "passed", ExclusionVerified: true, ObservedAt: time.Now().UTC(), ConfigGeneration: config.Generation, ConfigDigest: config.Digest()})
	}
	health := protocol.StorageClusterStatus{RepositoryID: config.RepositoryID, ClusterID: config.ClusterID, Status: "healthy", Healthy: true, Quorate: true, Mounted: true, FencingVerified: true, Fences: append([]protocol.StorageClusterFenceStatus(nil), config.FenceProofs...)}
	op := storageClusterOperation{RequestID: "activate-once", Digest: "durable-operation", MembershipGeneration: 1, Config: config, Status: "applying"}
	for _, node := range config.Nodes {
		op.Members = append(op.Members, clusterMemberStatus{HostID: node.HostID, Status: health, Generation: config.Generation, MembershipGeneration: 1, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "health", UpdatedAt: time.Now().UTC()})
	}
	promoteClusterOperation(s, &op)
	if op.Status != "healthy" || !clusterOperationActivated(op, mustMembership(t, s), config) || op.Activation == nil {
		t.Fatalf("all-member initial admission did not create durable activation: %#v", op)
	}
	activated := op
	activated.Members = append([]clusterMemberStatus(nil), op.Members...)
	activated.Activation = &storageClusterActivation{}
	*activated.Activation = *op.Activation
	committed := op.Activation.CommittedAt
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	if got, err := StorageClusterConfigForRepository(s, config.RepositoryID); err != nil || got.Digest() != config.Digest() {
		t.Fatalf("activated configuration unavailable: %#v %v", got, err)
	}
	originalHost := s.State.HostID
	s.State.HostID = "outside-current-cluster"
	if _, err := StorageClusterConfigForRepository(s, config.RepositoryID); err == nil {
		t.Fatal("configuration lookup admitted a Core outside the current cluster membership")
	}
	s.State.HostID = originalHost
	peerBeforePending, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	peerPending := peerBeforePending
	peerPending.Membership.Members = append([]protocol.PoolMember(nil), peerBeforePending.Membership.Members...)
	peerPending.Membership.Members[1].Status = "pending"
	if err := savePoolPeer(s, peerPending); err != nil {
		t.Fatal(err)
	}
	s.State.HostID = "member-1"
	if _, err := StorageClusterConfigForRepository(s, config.RepositoryID); err == nil {
		t.Fatal("configuration lookup admitted a configured but non-ready Core")
	}
	s.State.HostID = originalHost
	if err := savePoolPeer(s, peerBeforePending); err != nil {
		t.Fatal(err)
	}

	// A missing third report is a degraded runtime observation, not evidence
	// that the already completed initial admission never happened.
	op.Members[2].UpdatedAt = time.Now().Add(-3 * time.Minute)
	promoteClusterOperation(s, &op)
	if op.Status != "degraded" || op.Activation == nil || !op.Activation.CommittedAt.Equal(committed) {
		t.Fatalf("degraded runtime erased initial activation: %#v", op)
	}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	if got, err := StorageClusterConfigForRepository(s, config.RepositoryID); err != nil || got.Digest() != config.Digest() {
		t.Fatalf("degraded activated configuration unavailable for native validation: %#v %v", got, err)
	}
	if !poolVMClusterOperationActivated(s, mustMembership(t, s), config) {
		t.Fatal("VM admission did not recognize exact completed initial activation")
	}
	// A majority without the master is not an honest degraded state. It must be
	// surfaced as attention while retaining the immutable initial receipt.
	op.Members[2].UpdatedAt = time.Now().UTC()
	op.Members[0].UpdatedAt = time.Now().Add(-3 * time.Minute)
	promoteClusterOperation(s, &op)
	if op.Status != "needs_attention" || op.Activation == nil {
		t.Fatalf("missing master health did not block degraded status: %#v", op)
	}

	// No durable receipt may be inferred from the status or historical
	// member reports. Such a record must requalify with all fresh reports.
	unreceipted := op
	unreceipted.Activation = nil
	unreceipted.Status = "healthy"
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{unreceipted.RequestID: unreceipted}}); err != nil {
		t.Fatal(err)
	}
	if _, err := StorageClusterConfigForRepository(s, config.RepositoryID); err == nil || poolVMClusterOperationActivated(s, mustMembership(t, s), config) {
		t.Fatal("healthy state without receipt bypassed initial activation qualification")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*storageClusterOperation)
	}{
		{name: "duplicate report", mutate: func(candidate *storageClusterOperation) {
			candidate.Members = append(candidate.Members, candidate.Members[0])
		}},
		{name: "foreign report", mutate: func(candidate *storageClusterOperation) {
			candidate.Members = append(candidate.Members, clusterMemberStatus{HostID: "foreign", Status: health, Generation: config.Generation, MembershipGeneration: 1, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "health", UpdatedAt: time.Now().UTC()})
		}},
		{name: "future report", mutate: func(candidate *storageClusterOperation) {
			candidate.Members[1].UpdatedAt = time.Now().Add(3 * time.Minute)
		}},
		{name: "nested blocked status", mutate: func(candidate *storageClusterOperation) { candidate.Members[1].Status.Status = "blocked" }},
		{name: "mismatched durable receipt", mutate: func(candidate *storageClusterOperation) { candidate.Activation.ConfigDigest = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := activated
			candidate.Members = append([]clusterMemberStatus(nil), activated.Members...)
			candidate.Activation = &storageClusterActivation{}
			*candidate.Activation = *activated.Activation
			tc.mutate(&candidate)
			promoteClusterOperation(s, &candidate)
			if candidate.Status == "healthy" {
				t.Fatalf("unsafe report/receipt qualified initial activation: %#v", candidate)
			}
		})
	}

	changed := config
	changed.Generation++
	if clusterOperationActivated(op, mustMembership(t, s), changed) {
		t.Fatal("activation receipt was reused for a different configuration generation")
	}
	peer, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	peer.Membership.Generation++
	for i := range peer.Membership.Members {
		peer.Membership.Members[i].Generation = peer.Membership.Generation
	}
	if err = savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	if clusterOperationActivated(op, mustMembership(t, s), config) {
		t.Fatal("activation receipt was reused for a changed membership generation")
	}

}

func mustMembership(t *testing.T, s *Store) protocol.PoolMembership {
	t.Helper()
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	return membership
}

func TestClusterSurvivorFenceReplaySurvivesRestart(t *testing.T) {
	original := verifyStorageClusterFenceNative
	t.Cleanup(func() { verifyStorageClusterFenceNative = original })
	for _, succeeded := range []bool{true, false} {
		t.Run(map[bool]string{true: "passed", false: "uncertain"}[succeeded], func(t *testing.T) {
			s := peerStore(t, "survivor-1", "pool-1")
			request := clusterFencePeerRequest{RequestID: "destructive-request", TargetHostID: "master-1", Config: protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: "repo-123456", Generation: 1}}
			if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{"setup": {RequestID: "setup", Config: request.Config}}}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			verifyStorageClusterFenceNative = func(ctx context.Context, _ string, c protocol.StorageClusterConfig, host string) (protocol.StorageClusterFenceStatus, error) {
				calls++
				if ctx.Err() != nil {
					t.Fatal("survivor inherited a canceled requester")
				}
				proof := protocol.StorageClusterFenceStatus{HostID: host, ConfigDigest: c.Digest(), ConfigGeneration: c.Generation, ObservedAt: time.Now().UTC(), Status: "passed", ExclusionVerified: succeeded}
				if !succeeded {
					proof.Status = "uncertain"
					return proof, context.Canceled
				}
				return proof, nil
			}
			if err := verifyClusterFenceOnSurvivor(s, request); (err == nil) != succeeded {
				t.Fatalf("initial proof outcome: %v", err)
			}
			if err := s.Save(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(s.dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := verifyClusterFenceOnSurvivor(reopened, request); (err == nil) != succeeded {
				t.Fatalf("replayed proof outcome: %v", err)
			}
			changed := request
			changed.TargetHostID = "other-host"
			if err := verifyClusterFenceOnSurvivor(reopened, changed); err == nil {
				t.Fatal("changed destructive request accepted")
			}
			state, err := loadStorageClusters(reopened)
			if err != nil || calls != 1 || len(state.Operations) != 1 || state.FenceRequests[request.RequestID].Proof.RequestID != request.RequestID {
				t.Fatalf("survivor repeated or lost durable proof: calls=%d err=%v", calls, err)
			}
			pending := state.FenceRequests[request.RequestID]
			pending.RequestID, pending.Status, pending.Proof = "interrupted-request", "verifying", nil
			state.FenceRequests[pending.RequestID] = pending
			if err := saveStorageClusters(reopened, state); err != nil {
				t.Fatal(err)
			}
			request.RequestID = pending.RequestID
			if err := verifyClusterFenceOnSurvivor(reopened, request); err == nil || calls != 1 {
				t.Fatal("interrupted intent repeated destructive action")
			}
		})
	}
}
