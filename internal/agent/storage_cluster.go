// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Clustered iSCSI is a Core control plane, not a Controller job.  The pool
// master keeps this small desired-state record and members pull it over the
// already pinned Core peer connection.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolStorageClusterPath = "/api/v1/pool/storage-cluster"

var clusterCorosyncAuthKeyPath = "/etc/corosync/authkey"
var bootstrapStorageCluster = vmhost.BootstrapClusteredISCSI
var observeStorageClusterHealth = vmhost.StorageClusterHealthAt
var connectStorageCluster = vmhost.ConnectClusteredISCSI
var startNativeStorageCluster = vmhost.StartStorageClusterAt
var applyNativeStorageCluster = vmhost.ApplyStorageClusterWithRegistrationAt
var validateNativeStorageClusterAccess = func(ctx context.Context, stateDir string, registration protocol.StorageRegistration, config protocol.StorageClusterConfig) error {
	_, err := vmhost.ValidateClusteredISCSIAt(ctx, stateDir, registration, config)
	return err
}
var reconcileNativeStorageClusterFencing = vmhost.ReconcileStorageClusterFencing
var reconcileNativeStorageClusterTransport = vmhost.ReconcileStorageClusterTransport
var verifyStorageClusterFenceNative = vmhost.VerifyStorageClusterFence
var installClusterStorageBootGate = InstallClusterStorageBootGate
var validateStorageClusterSessionMonitor = vmhost.ValidateStorageClusterSessionMonitor

const clusterBootGateInstallFailure = "cluster boot gate installation failed"
const clusterNativeReceiptUnavailable = "managed clustered storage receipt is unavailable"
const clusterFencingPolicyReconcileFailure = "cluster fencing policy reconciliation failed"
const clusterTransportMonitorReconcileFailure = "cluster transport monitor reconciliation failed"

// StorageClusterRequest has no credential bytes. Fence credential references resolve only
// in root-only native Core state.
type StorageClusterRequest = protocol.StorageClusterRequest

type storageClusterOperation struct {
	RequestID            string                        `json:"request_id"`
	Digest               string                        `json:"digest"`
	InputDigest          string                        `json:"input_digest"`
	MembershipGeneration uint64                        `json:"membership_generation"`
	Config               protocol.StorageClusterConfig `json:"config"`
	Registration         protocol.StorageRegistration  `json:"registration"`
	Status               string                        `json:"status"`
	Error                string                        `json:"error,omitempty"`
	UpdatedAt            time.Time                     `json:"updated_at"`
	Members              []clusterMemberStatus         `json:"members,omitempty"`
	// Activation is Core-private, durable evidence that this exact desired
	// configuration completed its initial all-member admission barrier. It is
	// deliberately not a Controller-visible status field: later runtime health
	// can degrade without erasing the fact that initial activation completed.
	Activation *storageClusterActivation `json:"activation,omitempty"`
}
type storageClusterActivation struct {
	RequestID            string    `json:"request_id"`
	OperationDigest      string    `json:"operation_digest"`
	RepositoryID         string    `json:"repository_id"`
	Generation           uint64    `json:"generation"`
	ConfigDigest         string    `json:"config_digest"`
	MembershipGeneration uint64    `json:"membership_generation"`
	CommittedAt          time.Time `json:"committed_at"`
}
type clusterMemberStatus struct {
	HostID               string                        `json:"host_id"`
	Status               protocol.StorageClusterStatus `json:"status"`
	Generation           uint64                        `json:"generation"`
	MembershipGeneration uint64                        `json:"membership_generation"`
	ClusterID            string                        `json:"cluster_id"`
	ConfigDigest         string                        `json:"config_digest"`
	Stage                string                        `json:"stage"`
	UpdatedAt            time.Time                     `json:"updated_at"`
}
type storageClusterState struct {
	Operations    map[string]storageClusterOperation `json:"operations"`
	FenceRequests map[string]storageFenceRequest     `json:"fence_requests"`
}
type storageFenceRequest struct {
	RequestID, Digest, RepositoryID, TargetHostID, Status, Error string
	ConfigDigest, ExecutorHostID                                 string
	Proof                                                        *protocol.StorageClusterFenceStatus
	UpdatedAt                                                    time.Time
}
type storageClusterProjection struct {
	Operation    storageClusterOperation          `json:"operation"`
	Registration protocol.StorageRegistration     `json:"registration"`
	AuthKey      []byte                           `json:"auth_key,omitempty"`
	Secrets      []StorageClusterSecretProjection `json:"secrets,omitempty"`
}
type clusterFencePeerRequest struct {
	RequestID    string                        `json:"request_id"`
	Config       protocol.StorageClusterConfig `json:"config"`
	TargetHostID string                        `json:"target_host_id"`
}

var storageClusterMu sync.Mutex

func storageClusterFile(s *Store) string { return filepath.Join(s.dir, "pool-storage-cluster.json") }
func loadStorageClusters(s *Store) (storageClusterState, error) {
	out := storageClusterState{Operations: map[string]storageClusterOperation{}, FenceRequests: map[string]storageFenceRequest{}}
	b, err := os.ReadFile(storageClusterFile(s))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if json.Unmarshal(b, &out) != nil || out.Operations == nil {
		return out, errors.New("invalid clustered storage state")
	}
	if out.FenceRequests == nil {
		out.FenceRequests = map[string]storageFenceRequest{}
	}
	return out, nil
}
func saveStorageClusters(s *Store, state storageClusterState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(storageClusterFile(s), b, 0600)
}

func clusterRegistration(ctx context.Context, s *Store, id string) (protocol.StorageRegistration, error) {
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	for _, check := range checks {
		if check.Status == "failed" {
			return protocol.StorageRegistration{}, errors.New("clustered repository inventory is unavailable")
		}
	}
	for _, entry := range managed.Storage {
		if entry.ID == id && !entry.Tombstone {
			return entry, nil
		}
	}
	return protocol.StorageRegistration{}, errors.New("clustered repository is unavailable")
}
func validClusterMembership(s *Store, config protocol.StorageClusterConfig) (protocol.PoolMembership, error) {
	membership, err := Membership(s)
	if err != nil {
		return membership, err
	}
	if membership.MasterHostID != s.State.HostID {
		return membership, errors.New("only the pool master may configure clustered storage")
	}
	if len(membership.Members) < 3 || len(membership.Members) > 5 {
		return membership, errors.New("clustered storage needs three to five ready pool members")
	}
	ready := map[string]bool{}
	for _, member := range membership.Members {
		if member.Status == "ready" {
			ready[member.HostID] = true
		}
	}
	if len(ready) != len(membership.Members) || len(config.Nodes) != len(ready) {
		return membership, errors.New("all clustered storage members must be ready")
	}
	for _, node := range config.Nodes {
		if !ready[node.HostID] {
			return membership, errors.New("cluster configuration membership is stale")
		}
	}
	return membership, nil
}
func promoteClusterOperation(s *Store, op *storageClusterOperation) {
	if !op.Config.FencingVerified() {
		op.Status, op.Error = "awaiting_fencing", "native activation waits for exact Core-recorded fence proofs"
		return
	}
	membership, err := Membership(s)
	if err != nil || !clusterOperationMembershipCurrent(*op, membership) {
		op.Status, op.Error = "needs_attention", "cluster membership is no longer current"
		return
	}
	if clusterAllMembersHealthy(*op, membership) {
		if op.Activation == nil {
			if membership.MasterHostID != s.State.HostID {
				op.Status, op.Error = "needs_attention", "only the current pool master may record initial cluster activation"
				return
			}
			op.Activation = &storageClusterActivation{RequestID: op.RequestID, OperationDigest: op.Digest, RepositoryID: op.Config.RepositoryID, Generation: op.Config.Generation, ConfigDigest: op.Config.Digest(), MembershipGeneration: membership.Generation, CommittedAt: time.Now().UTC()}
		} else if !clusterOperationActivated(*op, membership, op.Config) {
			op.Status, op.Error = "needs_attention", "cluster activation receipt no longer matches current durable intent"
			return
		}
		op.Status, op.Error = "healthy", ""
		return
	}
	if !clusterOperationActivated(*op, membership, op.Config) {
		op.Status, op.Error = "applying", "awaiting initial current healthy member confirmation"
		return
	}
	if clusterFreshHealthyMajorityIncludingMaster(*op, membership) {
		op.Status, op.Error = "degraded", "cluster is active but not all current members are healthy"
		return
	}
	op.Status, op.Error = "needs_attention", "cluster is active but has no fresh healthy majority including master"
}

func clusterOperationMembershipCurrent(op storageClusterOperation, membership protocol.PoolMembership) bool {
	if membership.PoolID != op.Config.PoolID || membership.Generation != op.MembershipGeneration || len(membership.Members) < 3 || len(membership.Members) > 5 || len(membership.Members) != len(op.Config.Nodes) {
		return false
	}
	nodes := make(map[string]bool, len(op.Config.Nodes))
	for _, node := range op.Config.Nodes {
		if node.HostID == "" || nodes[node.HostID] {
			return false
		}
		nodes[node.HostID] = true
	}
	for _, member := range membership.Members {
		if member.HostID == "" || !nodes[member.HostID] {
			return false
		}
		delete(nodes, member.HostID)
	}
	return len(nodes) == 0
}

func clusterMemberReportHealthy(report clusterMemberStatus, config protocol.StorageClusterConfig, membership protocol.PoolMembership) bool {
	// Peer timestamps are never trusted without a bounded skew window. The
	// receiving master records the timestamp in production, but this also makes
	// durable/replayed input fail closed if it is malformed or implausibly future.
	age := time.Since(report.UpdatedAt)
	return !report.UpdatedAt.IsZero() && age >= -2*time.Minute && age <= 2*time.Minute && report.Generation == config.Generation && report.MembershipGeneration == membership.Generation && report.ClusterID == config.ClusterID && report.ConfigDigest == config.Digest() && report.Status.RepositoryID == config.RepositoryID && report.Status.ClusterID == config.ClusterID && report.Status.Status == "healthy" && report.Status.Healthy && report.Status.Quorate && report.Status.Mounted && report.Status.FencingVerified && clusterMemberReportAcknowledgesCurrentFence(report, config)
}

func clusterReportsForCurrentMembers(op storageClusterOperation, membership protocol.PoolMembership) (map[string]clusterMemberStatus, bool) {
	allowed := make(map[string]bool, len(membership.Members))
	for _, member := range membership.Members {
		if member.HostID == "" || allowed[member.HostID] {
			return nil, false
		}
		allowed[member.HostID] = true
	}
	seen := make(map[string]clusterMemberStatus, len(op.Members))
	for _, report := range op.Members {
		if report.HostID == "" || !allowed[report.HostID] {
			return nil, false
		}
		if _, duplicate := seen[report.HostID]; duplicate {
			return nil, false
		}
		seen[report.HostID] = report
	}
	return seen, true
}

func clusterAllMembersHealthy(op storageClusterOperation, membership protocol.PoolMembership) bool {
	seen, valid := clusterReportsForCurrentMembers(op, membership)
	if !valid {
		return false
	}
	for _, member := range membership.Members {
		report, ok := seen[member.HostID]
		if member.Status != "ready" || !ok || !clusterMemberReportHealthy(report, op.Config, membership) {
			return false
		}
	}
	return true
}

// clusterAllMembersSessionMonitorReady is deliberately independent of aggregate
// cluster health. It is an authenticated, current-generation barrier for the
// monitor-only transport resource: it proves the fixed monitor and an exact
// live local iSCSI session, while a newly installed monitor can be ready before
// the CIB which will run it exists. Do not fold Healthy, Quorate, or Mounted
// into this test or an old CIB would make its own safe upgrade impossible.
func clusterAllMembersSessionMonitorReady(op storageClusterOperation, membership protocol.PoolMembership) bool {
	seen, valid := clusterReportsForCurrentMembers(op, membership)
	if !valid {
		return false
	}
	for _, member := range membership.Members {
		report, ok := seen[member.HostID]
		if member.Status != "ready" || !ok {
			return false
		}
		age := time.Since(report.UpdatedAt)
		if report.Generation != op.Config.Generation || report.MembershipGeneration != membership.Generation || report.ClusterID != op.Config.ClusterID || report.ConfigDigest != op.Config.Digest() || report.Status.RepositoryID != op.Config.RepositoryID || report.Status.ClusterID != op.Config.ClusterID || !report.Status.SessionMonitorReady || report.UpdatedAt.IsZero() || age < -2*time.Minute || age > 2*time.Minute {
			return false
		}
	}
	return true
}

func clusterSessionMonitorReady(ctx context.Context, registration protocol.StorageRegistration) bool {
	if registration.Source.ISCSI == nil {
		return false
	}
	portal, sessionID, err := vmhost.StorageISCSISessionParameters(registration.Source.ISCSI.Portal, registration.Source.ISCSI.TargetIQN)
	if err != nil {
		return false
	}
	return validateStorageClusterSessionMonitor(ctx, portal, registration.Source.ISCSI.TargetIQN, sessionID) == nil
}

func clusterMemberSessionMonitorReport(hostID string, config protocol.StorageClusterConfig, membership protocol.PoolMembership, ready bool) clusterMemberStatus {
	status, message := "pending", "awaiting cluster session monitor admission"
	if !ready {
		status, message = "blocked", "cluster session monitor is unavailable"
	}
	return clusterMemberStatus{HostID: hostID, Status: protocol.StorageClusterStatus{RepositoryID: config.RepositoryID, ClusterID: config.ClusterID, SessionMonitorReady: ready, Status: status, Error: message}, Generation: config.Generation, MembershipGeneration: membership.Generation, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "monitor", UpdatedAt: time.Now().UTC()}
}

func replaceClusterMemberReport(op *storageClusterOperation, report clusterMemberStatus) {
	for i := range op.Members {
		if op.Members[i].HostID == report.HostID {
			// A live session check is not aggregate cluster health. Preserve an older
			// healthy report and its timestamp rather than refreshing it merely from
			// transport readiness; refreshClusterMasterHealth and member
			// reconciliation publish a joined health+monitor observation.
			if op.Members[i].Status.Healthy || op.Members[i].Status.Status == "healthy" {
				op.Members[i].Status.SessionMonitorReady = report.Status.SessionMonitorReady
				return
			}
			op.Members[i] = report
			return
		}
	}
	op.Members = append(op.Members, report)
}

func clusterFreshHealthyMajorityIncludingMaster(op storageClusterOperation, membership protocol.PoolMembership) bool {
	seen, valid := clusterReportsForCurrentMembers(op, membership)
	if !valid {
		return false
	}
	healthy := 0
	masterHealthy := false
	for _, member := range membership.Members {
		report, ok := seen[member.HostID]
		if member.Status != "ready" || !ok || !clusterMemberReportHealthy(report, op.Config, membership) {
			continue
		}
		healthy++
		masterHealthy = masterHealthy || member.HostID == membership.MasterHostID
	}
	return masterHealthy && healthy >= len(membership.Members)/2+1
}

func clusterOperationActivated(op storageClusterOperation, membership protocol.PoolMembership, config protocol.StorageClusterConfig) bool {
	a := op.Activation
	return a != nil && !a.CommittedAt.IsZero() && a.RequestID == op.RequestID && a.OperationDigest == op.Digest && a.RepositoryID == config.RepositoryID && a.Generation == config.Generation && a.ConfigDigest == config.Digest() && a.MembershipGeneration == membership.Generation && op.Config.RepositoryID == config.RepositoryID && op.Config.Generation == config.Generation && op.Config.Digest() == config.Digest() && op.MembershipGeneration == membership.Generation && config.FencingVerified() && clusterOperationMembershipCurrent(op, membership)
}

func clusterCurrentMember(hostID string, membership protocol.PoolMembership, config protocol.StorageClusterConfig) bool {
	for _, member := range membership.Members {
		if member.HostID != hostID || member.Status != "ready" {
			continue
		}
		for _, node := range config.Nodes {
			if node.HostID == hostID {
				return true
			}
		}
	}
	return false
}
func clusterDigest(request StorageClusterRequest) string {
	b, _ := json.Marshal(request)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// clusterCandidate gives a browser request no authority over Core identifiers, paths, or
// lock-table names. The master allocates the first repository identity durably with its
// request replay record; a retry with the same request ID returns that candidate.
func clusterCandidate(s *Store, request *StorageClusterRequest) error {
	if request.Registration.ID != "" || request.Config.RepositoryID != "" {
		return nil
	}
	if request.Config.DevicePath != "" || request.Config.MountPath != "" || request.Registration.Path != "" || request.Registration.Source.ISCSI == nil || request.Registration.Source.ISCSI.MountPath != "" {
		return errors.New("cluster candidate must not include Core paths")
	}
	id := newUUID()
	config := request.Config
	config.RepositoryID, config.PoolID, config.Generation = id, s.State.PoolID, 1
	// Existing GFS2 owns the lock table. Bootstrap observes it after the exact LUN
	// login and rejects a foreign/nonmatching table; never manufacture one here.
	config.ClusterID, config.LockTable = "", ""
	eligible := make([]string, 0, len(config.Nodes))
	for _, node := range config.Nodes {
		eligible = append(eligible, node.HostID)
	}
	source := *request.Registration.Source.ISCSI
	source.Mode, source.ClusterID, source.LockTable, source.EligibleHostIDs = "clustered_gfs2", "", "", eligible
	request.Registration = protocol.StorageRegistration{ID: id, PoolID: s.State.PoolID, HostID: s.State.HostID, Name: request.Registration.Name, Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &source}, Generation: 1, CredentialReceipt: request.Registration.CredentialReceipt, CredentialRequestID: request.Registration.CredentialRequestID}
	request.Config = config
	request.ExpectedGeneration = 1
	return nil
}
func installClusterAuthKey(key []byte) error {
	if len(key) != 128 {
		return errors.New("invalid Corosync auth key")
	}
	if err := os.MkdirAll(filepath.Dir(clusterCorosyncAuthKeyPath), 0700); err != nil {
		return err
	}
	return writePrivate(clusterCorosyncAuthKeyPath, key, 0600)
}
func installClusterCorosyncAuthKey(s *Store) error {
	key, err := ClusterCorosyncAuthKey(s, s.State.PoolID)
	if err != nil {
		return err
	}
	return installClusterAuthKey(key)
}

// ApplyStorageCluster is restart-safe: the same request ID and input returns its durable
// result; a reused ID with changed input is refused before native work runs.
func ApplyStorageCluster(ctx context.Context, s *Store, request StorageClusterRequest) (storageClusterOperation, error) {
	return applyStorageCluster(ctx, s, request, false)
}

// Recovery uses only the previously accepted Core record, not reconstructed client
// input whose digest would differ after Core allocated identities and credentials.
func applyStorageCluster(ctx context.Context, s *Store, request StorageClusterRequest, resume bool) (storageClusterOperation, error) {
	if request.RequestID == "" || len(request.RequestID) > 128 {
		return storageClusterOperation{}, errors.New("cluster request ID is required")
	}
	storageClusterWorkers.Lock()
	active := storageClusterWorkers.active[s]
	if active == nil {
		active = map[string]bool{}
		storageClusterWorkers.active[s] = active
	}
	if active[request.RequestID] {
		storageClusterWorkers.Unlock()
		return storageClusterOperation{}, errors.New("cluster operation is already running")
	}
	active[request.RequestID] = true
	storageClusterWorkers.Unlock()
	defer func() {
		storageClusterWorkers.Lock()
		delete(storageClusterWorkers.active[s], request.RequestID)
		storageClusterWorkers.Unlock()
	}()
	storageClusterMu.Lock()
	state, err := loadStorageClusters(s)
	if err != nil {
		storageClusterMu.Unlock()
		return storageClusterOperation{}, err
	}
	digest := clusterDigest(request)
	inputDigest := digest
	// A retry after an observed runtime fault is allowed to return the durable
	// operation to queued before replaying its native work.  That transition is
	// not a new initial admission: retain the master-issued receipt and let its
	// exact binding be checked against the replayed configuration below.
	var priorActivation *storageClusterActivation
	var priorMembers []clusterMemberStatus
	if old, ok := state.Operations[request.RequestID]; ok {
		if old.InputDigest != "" {
			inputDigest = old.InputDigest
		}
		if resume {
			request.Config, request.Registration, request.ExpectedGeneration, digest = old.Config, old.Registration, old.Registration.Generation, old.Digest
		}
		if old.Status != "queued" {
			if old.Digest != digest {
				storageClusterMu.Unlock()
				return old, errors.New("cluster request ID was reused with different input")
			}
			storageClusterMu.Unlock()
			return old, nil
		}
		if old.Digest != digest {
			storageClusterMu.Unlock()
			return old, errors.New("cluster request ID was reused with different input")
		}
		if resume && old.Activation != nil {
			priorActivation = &storageClusterActivation{}
			*priorActivation = *old.Activation
		}
		priorMembers = append([]clusterMemberStatus(nil), old.Members...)
	} else if resume {
		storageClusterMu.Unlock()
		return storageClusterOperation{}, errors.New("accepted cluster operation is unavailable")
	}
	config := request.Config
	registration, registrationErr := clusterRegistration(ctx, s, config.RepositoryID)
	if registrationErr != nil || config.ClusterID == "" {
		// Initial clustered iSCSI admission is deliberately distinct from normal
		// storage registration: Core first proves the existing LUN/GFS2 identity,
		// derives its own paths, then publishes the resolved desired projection.
		if request.Registration.ID != config.RepositoryID || request.Registration.PoolID != s.State.PoolID || request.Registration.Path != "" || request.Registration.Source.ISCSI == nil || request.Registration.Source.ISCSI.MountPath != "" || config.DevicePath != "" || config.MountPath != "" {
			storageClusterMu.Unlock()
			return storageClusterOperation{}, errors.New("cluster bootstrap needs a path-free selected iSCSI registration")
		}
		registration, config, registrationErr = bootstrapStorageCluster(ctx, s.dir, request.Registration, config)
		if registrationErr != nil {
			storageClusterMu.Unlock()
			return storageClusterOperation{}, errors.New("clustered iSCSI bootstrap failed")
		}
		if registrationErr = vmhost.CommitClusteredStorageRegistration(ctx, filepath.Join(s.dir, "managed-vms"), registration); registrationErr != nil {
			storageClusterMu.Unlock()
			return storageClusterOperation{}, errors.New("clustered storage registration persistence failed")
		}
		// Persist the resolved bootstrap projection immediately after the managed
		// journal commit. A restart must resume this same Core identity, never the
		// path-free browser candidate.
		if pending, ok := state.Operations[request.RequestID]; ok {
			pending.Config, pending.Registration, pending.UpdatedAt = config, registration, time.Now().UTC()
			state.Operations[request.RequestID] = pending
			if registrationErr = saveStorageClusters(s, state); registrationErr != nil {
				storageClusterMu.Unlock()
				return storageClusterOperation{}, errors.New("cluster bootstrap projection persistence failed")
			}
		}
	}
	if request.ExpectedGeneration != registration.Generation || config.Generation != registration.Generation || config.ValidFor(registration) != nil {
		storageClusterMu.Unlock()
		return storageClusterOperation{}, errors.New("invalid clustered storage configuration")
	}
	storageClusterMu.Unlock()
	if clusterConfigConflict(s, config, request.RequestID) {
		return storageClusterOperation{}, errors.New("cluster repository generation conflicts with durable configuration")
	}
	storageClusterMu.Lock()
	membership, err := validClusterMembership(s, config)
	if err != nil {
		storageClusterMu.Unlock()
		return storageClusterOperation{}, err
	}
	status, message := "applying", ""
	if !config.FencingVerified() {
		status, message = "awaiting_fencing", "native activation waits for exact Core-recorded fence proofs"
	}
	op := storageClusterOperation{RequestID: request.RequestID, Digest: digest, InputDigest: inputDigest, MembershipGeneration: membership.Generation, Config: config, Registration: registration, Status: status, Error: message, UpdatedAt: time.Now().UTC(), Members: priorMembers, Activation: priorActivation}
	state.Operations[request.RequestID] = op
	if err = saveStorageClusters(s, state); err != nil {
		storageClusterMu.Unlock()
		return storageClusterOperation{}, err
	}
	storageClusterMu.Unlock()
	if !config.FencingVerified() {
		return op, nil
	}
	// Native setup can wait for quorum or a remote device; never make status or peer
	// bootstrap wait behind it.
	// The gate is a durable boot dependency, not a native activation step. It
	// must be installable during an upgrade even if this node's LUN is briefly
	// absent; its own boot execution remains fail-closed on Connect.
	if err = validClusterBootGateIntent(s, membership, op, registration); err != nil {
		op.Status, op.Error = "needs_attention", "cluster boot gate intent is unavailable"
	} else if err = installClusterStorageBootGate(s); err != nil {
		op.Status, op.Error = "failed", "cluster boot gate installation failed"
	} else if err = installClusterCorosyncAuthKey(s); err != nil {
		op.Status, op.Error = "needs_attention", "Corosync auth key is unavailable"
	} else if _, err = connectStorageCluster(ctx, s.dir, registration, config); err != nil {
		op.Status, op.Error, op.UpdatedAt = "failed", "clustered iSCSI preparation failed", time.Now().UTC()
	} else if ready := clusterSessionMonitorReady(ctx, registration); !ready {
		replaceClusterMemberReport(&op, clusterMemberSessionMonitorReport(s.State.HostID, config, membership, false))
		op.Status, op.Error, op.UpdatedAt = "needs_attention", "local cluster session monitor is unavailable", time.Now().UTC()
	} else if replaceClusterMemberReport(&op, clusterMemberSessionMonitorReport(s.State.HostID, config, membership, true)); !clusterAllMembersSessionMonitorReady(op, membership) {
		// A missing flag is an older Core, not an implied capability.  The
		// monitor can be live before its CIB resource exists, so aggregate health
		// is intentionally not part of this all-member upgrade barrier.
		op.Status, op.Error, op.UpdatedAt = "applying", "awaiting current member session monitor support", time.Now().UTC()
	} else if err = applyNativeStorageCluster(ctx, s.dir, registration, config); err != nil {
		op.Status, op.Error, op.UpdatedAt = "failed", "native cluster apply failed", time.Now().UTC()
	} else {
		validateErr := validateNativeStorageClusterAccess(ctx, s.dir, registration, config)
		status, healthErr := observeStorageClusterHealth(ctx, s.dir, registration, config)
		status.SessionMonitorReady = clusterSessionMonitorReady(ctx, registration)
		op.Members = []clusterMemberStatus{{HostID: s.State.HostID, Status: status, Generation: registration.Generation, MembershipGeneration: membership.Generation, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "health", UpdatedAt: time.Now().UTC()}}
		op.UpdatedAt = time.Now().UTC()
		// A locally healthy master is not a multi-host cluster.  Stay applying until every
		// current member posts an authenticated exact-generation health report.
		if validateErr != nil || healthErr != nil || !status.Healthy || !status.Quorate || !status.Mounted {
			op.Status, op.Error = "needs_attention", "local cluster health is not confirmed"
		} else {
			promoteClusterOperation(s, &op)
		}
	}
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err = loadStorageClusters(s)
	if err != nil {
		return op, err
	}
	if current, ok := state.Operations[request.RequestID]; ok && current.Digest == op.Digest {
		// Peer reports and fence failures can arrive while native setup runs. Keep
		// newer evidence rather than replacing it with this older completion.
		mergeFenceProofs(&op, current.Config)
		for _, report := range current.Members {
			found := false
			for i := range op.Members {
				if op.Members[i].HostID == report.HostID {
					if report.UpdatedAt.After(op.Members[i].UpdatedAt) {
						op.Members[i] = report
					}
					found = true
				}
			}
			if !found {
				op.Members = append(op.Members, report)
			}
		}
		if current.Status == "needs_attention" && current.UpdatedAt.After(op.UpdatedAt) {
			op.Status, op.Error = current.Status, current.Error
		}
		// Native work may resume after initial activation. Never discard the
		// master-issued admission receipt while merging newer peer evidence.
		if current.Activation != nil {
			op.Activation = current.Activation
		}
		invalidateClusterMemberHealthForCurrentFences(&op)
	}
	state.Operations[request.RequestID] = op
	if err := saveStorageClusters(s, state); err != nil {
		return op, err
	}
	return op, nil
}

func StorageClusterStatus(s *Store) []storageClusterOperation {
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		return nil
	}
	out := make([]storageClusterOperation, 0, len(state.Operations))
	for _, op := range state.Operations {
		out = append(out, op)
	}
	return out
}

func latestClusterOperation(state storageClusterState, repositoryID string) (string, storageClusterOperation) {
	// Reject any divergent durable generation before selecting the newest. A
	// newer record must never hide conflicting historical intent merely because
	// Go ranged the map in a different order.
	generationDigests := map[uint64]string{}
	for _, candidate := range state.Operations {
		if candidate.Config.RepositoryID != repositoryID {
			continue
		}
		if digest, ok := generationDigests[candidate.Config.Generation]; ok && digest != candidate.Config.Digest() {
			return "", storageClusterOperation{}
		}
		generationDigests[candidate.Config.Generation] = candidate.Config.Digest()
	}
	var key string
	var latest storageClusterOperation
	for id, candidate := range state.Operations {
		if candidate.Config.RepositoryID != repositoryID {
			continue
		}
		if key == "" || candidate.Config.Generation > latest.Config.Generation || candidate.Config.Generation == latest.Config.Generation && (candidate.UpdatedAt.After(latest.UpdatedAt) || candidate.UpdatedAt.Equal(latest.UpdatedAt) && id > key) {
			key, latest = id, candidate
		}
	}
	return key, latest
}

// mergeFenceProofs accepts only newer proof observations. A newly accepted
// successful fence proves that its target was excluded, so any health report
// from at or before that observation is no longer evidence the target is
// healthy. The activation receipt remains immutable; promotion then honestly
// reports degraded until the target supplies a fresh, proof-aware health view.
func mergeFenceProofs(op *storageClusterOperation, incoming protocol.StorageClusterConfig) {
	current := &op.Config
	if current.Generation != incoming.Generation || current.Digest() != incoming.Digest() {
		return
	}
	for _, proof := range incoming.FenceProofs {
		acceptClusterFenceProof(op, proof)
	}
}

func acceptClusterFenceProof(op *storageClusterOperation, proof protocol.StorageClusterFenceStatus) bool {
	for i := range op.Config.FenceProofs {
		if op.Config.FenceProofs[i].HostID != proof.HostID {
			continue
		}
		if !proof.ObservedAt.After(op.Config.FenceProofs[i].ObservedAt) {
			return false
		}
		op.Config.FenceProofs[i] = proof
		invalidateClusterMemberHealthForFence(op, proof)
		return true
	}
	op.Config.FenceProofs = append(op.Config.FenceProofs, proof)
	invalidateClusterMemberHealthForFence(op, proof)
	return true
}

func invalidateClusterMemberHealthForFence(op *storageClusterOperation, proof protocol.StorageClusterFenceStatus) {
	if proof.HostID == "" || proof.ObservedAt.IsZero() || proof.Status != "passed" || !proof.ExclusionVerified {
		return
	}
	for i := range op.Members {
		if op.Members[i].HostID == proof.HostID && !op.Members[i].UpdatedAt.After(proof.ObservedAt) {
			op.Members = append(op.Members[:i], op.Members[i+1:]...)
			return
		}
	}
}

func invalidateClusterMemberHealthForCurrentFences(op *storageClusterOperation) {
	for _, proof := range op.Config.FenceProofs {
		invalidateClusterMemberHealthForFence(op, proof)
	}
}

func clusterMemberReportAcknowledgesCurrentFence(report clusterMemberStatus, config protocol.StorageClusterConfig) bool {
	for _, proof := range config.FenceProofs {
		if proof.HostID != report.HostID || proof.Status != "passed" || !proof.ExclusionVerified {
			continue
		}
		acknowledged := false
		for _, reported := range report.Status.Fences {
			if reported.HostID == proof.HostID && reported.RequestID == proof.RequestID && reported.TargetID == proof.TargetID && reported.Status == proof.Status && reported.ExclusionVerified == proof.ExclusionVerified && reported.ConfigGeneration == proof.ConfigGeneration && reported.ConfigDigest == proof.ConfigDigest && reported.ObservedAt.Equal(proof.ObservedAt) {
				acknowledged = true
				break
			}
		}
		if !acknowledged {
			return false
		}
	}
	return true
}
func reduceFenceOperation(op *storageClusterOperation) {
	// Polling proofs must not turn a raw candidate or already-active cluster back
	// into a fencing stage.
	if op.Config.ClusterID == "" || op.Status == "healthy" || op.Status == "applying" || (op.Activation != nil && op.Config.FencingVerified()) {
		return
	}
	pending := false
	for _, proof := range op.Config.FenceProofs {
		if proof.Status == "failed" || proof.Status == "uncertain" {
			op.Status, op.Error = "needs_attention", "fence test did not prove exclusion"
			return
		}
		pending = pending || proof.Status == "unverified"
	}
	if op.Config.FencingVerified() {
		op.Status, op.Error = "queued", ""
	} else if pending {
		op.Status, op.Error = "verifying_fence", ""
	} else {
		op.Status, op.Error = "awaiting_fencing", "native activation waits for remaining fence proofs"
	}
}

func clusterConfigConflict(s *Store, config protocol.StorageClusterConfig, except string) bool {
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		return true
	}
	for id, op := range state.Operations {
		if id != except && op.Config.RepositoryID == config.RepositoryID && op.Config.Generation == config.Generation && op.Config.Digest() != config.Digest() {
			return true
		}
	}
	return false
}

func clusterOperationsPublic(s *Store) []protocol.StorageClusterOperation {
	private := StorageClusterStatus(s)
	out := make([]protocol.StorageClusterOperation, 0, len(private))
	for _, op := range private {
		members := make([]protocol.StorageClusterMemberReport, 0, len(op.Members))
		for _, member := range op.Members {
			members = append(members, protocol.StorageClusterMemberReport{
				HostID:               member.HostID,
				Status:               member.Status,
				Generation:           member.Generation,
				MembershipGeneration: member.MembershipGeneration,
				ClusterID:            member.ClusterID,
				ConfigDigest:         member.ConfigDigest,
				Stage:                member.Stage,
				UpdatedAt:            member.UpdatedAt,
			})
		}
		out = append(out, protocol.StorageClusterOperation{RequestID: op.RequestID, MembershipGeneration: op.MembershipGeneration, Config: op.Config, Status: op.Status, Error: op.Error, UpdatedAt: op.UpdatedAt, Members: members})
	}
	return out
}

var storageClusterWorkers = struct {
	sync.Mutex
	active map[*Store]map[string]bool
}{active: map[*Store]map[string]bool{}}

// ExecuteStorageClusterRequest is the transient Controller/Core report boundary. It starts
// durable work independently of the HTTP/report context, so an ambiguous relay timeout is
// observable as applying state rather than cancellation. Controller never retains that state.
func ExecuteStorageClusterRequest(_ context.Context, s *Store, request protocol.StorageClusterRequest) protocol.StorageClusterResult {
	result := protocol.StorageClusterResult{RequestID: request.RequestID, PoolID: s.State.PoolID, HostID: s.State.HostID, ObservedAt: time.Now().UTC()}
	if request.RequestID == "" || request.HostID != s.State.HostID || request.PoolID != s.State.PoolID || (request.Action != "apply" && request.Action != "status" && request.Action != "verify_fence") {
		result.Status, result.Error = "failed", "invalid cluster request scope"
		return result
	}
	// A clustered-storage configuration is a replicated authority section, so an apply or a
	// fence request is an authority write and takes the same guard every other registry
	// write takes: a frozen, handed-over or restricted master starts none, because it would
	// move content a pinned generation already names. "status" is a read and is unaffected,
	// and work already in flight is never interrupted — this is the entry point, not the
	// resume. The refusal is the retryable one, so the Controller can simply try again once
	// the master change completes.
	if request.Action != "status" {
		if err := requirePoolAuthorityWritable(s); err != nil {
			result.Status, result.Error = "failed", err.Error()
			return result
		}
	}
	rawDigest := clusterDigest(request)
	if request.Action == "apply" {
		if len(request.Config.FenceProofs) != 0 {
			result.Status, result.Error = "failed", "fence evidence is recorded only by Core verification"
			return result
		}
		// Replay is resolved before allocating a Core ID. The original browser input
		// digest, not a generated repository UUID, is the idempotency key.
		storageClusterMu.Lock()
		state, loadErr := loadStorageClusters(s)
		if loadErr == nil {
			if old, ok := state.Operations[request.RequestID]; ok {
				storageClusterMu.Unlock()
				if old.InputDigest != "" && old.InputDigest != rawDigest {
					result.Status, result.Error = "failed", "cluster request ID was reused with different input"
					return result
				}
				result.Status, result.Operations = "accepted", clusterOperationsPublic(s)
				return result
			}
		}
		storageClusterMu.Unlock()
		if err := clusterCandidate(s, &request); err != nil {
			result.Status, result.Error = "failed", err.Error()
			return result
		}
		if err := ResolveStorageClusterCredentials(s, &request); err != nil {
			result.Status, result.Error = "failed", "cluster credentials are unavailable"
			return result
		}
	}
	if request.Action == "status" {
		result.Status, result.Operations = "accepted", clusterOperationsPublic(s)
		return result
	}
	if request.Action == "verify_fence" {
		if request.FenceHostID == "" || request.FenceConfirmation != "FENCE "+request.FenceHostID || request.Config.RepositoryID == "" {
			result.Status, result.Error = "failed", "exact fence target confirmation is required"
			return result
		}
		created := false
		storageClusterMu.Lock()
		state, err := loadStorageClusters(s)
		if err == nil {
			if old, ok := state.FenceRequests[request.RequestID]; ok {
				if old.Digest != rawDigest {
					err = errors.New("fence request ID was reused with different input")
				}
			} else {
				key, selected := latestClusterOperation(state, request.Config.RepositoryID)
				for _, pending := range state.FenceRequests {
					if pending.RepositoryID == request.Config.RepositoryID && pending.TargetHostID == request.FenceHostID && pending.Status == "verifying" {
						err = errors.New("a fence request for this target needs reconciliation")
					}
				}
				if err != nil {
					// Never overlap destructive requests for the same target.
				} else if key == "" {
					err = errors.New("cluster configuration is unavailable")
				} else if selected.Config.ClusterID == "" || selected.Config.LockTable == "" || selected.Config.DevicePath == "" || selected.Config.MountPath == "" {
					err = errors.New("cluster bootstrap must resolve storage identity before fencing")
				} else {
					created = true
					state.FenceRequests[request.RequestID] = storageFenceRequest{RequestID: request.RequestID, Digest: rawDigest, RepositoryID: request.Config.RepositoryID, TargetHostID: request.FenceHostID, ConfigDigest: selected.Config.Digest(), Status: "verifying", UpdatedAt: time.Now().UTC()}
					pending := protocol.StorageClusterFenceStatus{RequestID: request.RequestID, HostID: request.FenceHostID, Status: "unverified", Error: "fence verification is pending", ObservedAt: time.Now().UTC(), ConfigGeneration: selected.Config.Generation, ConfigDigest: selected.Config.Digest()}
					for _, node := range selected.Config.Nodes {
						if node.HostID == request.FenceHostID {
							pending.TargetID = node.Fence.TargetID
						}
					}
					replaced := false
					for i := range selected.Config.FenceProofs {
						if selected.Config.FenceProofs[i].HostID == pending.HostID {
							selected.Config.FenceProofs[i], replaced = pending, true
						}
					}
					if !replaced {
						selected.Config.FenceProofs = append(selected.Config.FenceProofs, pending)
					}
					selected.Status, selected.Error, selected.UpdatedAt = "verifying_fence", "", pending.ObservedAt
					state.Operations[key] = selected
					err = saveStorageClusters(s, state)
				}
			}
		}
		storageClusterMu.Unlock()
		if err != nil {
			result.Status, result.Error = "failed", "fence request could not be persisted"
			return result
		}
		if !created {
			result.Status, result.Operations = "accepted", clusterOperationsPublic(s)
			return result
		}
		go func() {
			work, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			err := verifyStorageClusterFence(work, s, request.RequestID, request.Config.RepositoryID, request.FenceHostID)
			storageClusterMu.Lock()
			state, loadErr := loadStorageClusters(s)
			if loadErr == nil {
				intent := state.FenceRequests[request.RequestID]
				intent.UpdatedAt = time.Now().UTC()
				if err != nil {
					intent.Status, intent.Error = "needs_attention", "fence exclusion was not confirmed"
					for id, op := range state.Operations {
						if op.Config.RepositoryID != intent.RepositoryID || op.Config.Digest() != intent.ConfigDigest {
							continue
						}
						for i := range op.Config.FenceProofs {
							proof := &op.Config.FenceProofs[i]
							if proof.RequestID == request.RequestID && proof.Status == "unverified" {
								proof.Status, proof.Error, proof.ObservedAt = "uncertain", intent.Error, intent.UpdatedAt
								op.Status, op.Error, op.UpdatedAt = "needs_attention", intent.Error, intent.UpdatedAt
							}
						}
						state.Operations[id] = op
					}
				} else {
					intent.Status, intent.Error = "succeeded", ""
					for id, op := range state.Operations {
						if op.Config.RepositoryID != intent.RepositoryID || op.Config.Digest() != intent.ConfigDigest {
							continue
						}
						reduceFenceOperation(&op)
						op.UpdatedAt = intent.UpdatedAt
						state.Operations[id] = op
					}
				}
				state.FenceRequests[request.RequestID] = intent
				_ = saveStorageClusters(s, state)
			}
			storageClusterMu.Unlock()
		}()
		result.Status, result.Operations = "accepted", clusterOperationsPublic(s)
		return result
	}
	if _, err := validClusterMembership(s, request.Config); err != nil {
		result.Status, result.Error = "failed", err.Error()
		return result
	}
	// Commit acceptance before launching work so a lost report response or restart has a
	// truthful, durable queued operation rather than a phantom success.
	storageClusterMu.Lock()
	state, err := loadStorageClusters(s)
	digest := clusterDigest(request)
	if err == nil {
		if old, ok := state.Operations[request.RequestID]; ok && old.Digest != digest {
			err = errors.New("cluster request ID was reused with different input")
		} else if !ok {
			membership, memberErr := Membership(s)
			if memberErr != nil {
				err = memberErr
			} else {
				state.Operations[request.RequestID] = storageClusterOperation{RequestID: request.RequestID, Digest: digest, InputDigest: rawDigest, MembershipGeneration: membership.Generation, Config: request.Config, Registration: request.Registration, Status: "queued", UpdatedAt: time.Now().UTC()}
				err = saveStorageClusters(s, state)
			}
		}
	}
	storageClusterMu.Unlock()
	if err != nil {
		result.Status, result.Error = "failed", "cluster operation could not be persisted"
		return result
	}
	go func() {
		work, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_, _ = ApplyStorageCluster(work, s, request)
	}()
	result.Status, result.Operations = "accepted", clusterOperationsPublic(s)
	return result
}

func verifyStorageClusterFence(ctx context.Context, s *Store, requestID, repositoryID, hostID string) error {
	storageClusterMu.Lock()
	state, err := loadStorageClusters(s)
	key, op := latestClusterOperation(state, repositoryID)
	storageClusterMu.Unlock()
	if err != nil {
		return err
	}
	if key == "" {
		return errors.New("cluster configuration is unavailable")
	}
	if intent, ok := state.FenceRequests[requestID]; !ok || intent.ConfigDigest != op.Config.Digest() {
		return errors.New("fence request configuration changed")
	}
	if hostID == s.State.HostID {
		membership, _ := Membership(s)
		for _, node := range op.Config.Nodes {
			if node.HostID != hostID {
				origin := ""
				for _, member := range membership.Members {
					if member.HostID == node.HostID {
						origin = member.PeerOrigin
					}
				}
				if origin == "" {
					continue
				}
				client, e := PoolMemberPeerClient(s, origin, node.HostID)
				if e != nil {
					continue
				}
				// Once dispatched, an ambiguous reply must not cause another executor
				// to repeat a destructive action. Record the sole survivor first.
				storageClusterMu.Lock()
				current, saveErr := loadStorageClusters(s)
				if saveErr == nil {
					intent := current.FenceRequests[requestID]
					intent.ExecutorHostID = node.HostID
					current.FenceRequests[requestID] = intent
					saveErr = saveStorageClusters(s, current)
				}
				storageClusterMu.Unlock()
				if saveErr != nil {
					return saveErr
				}
				body, _ := json.Marshal(clusterFencePeerRequest{RequestID: requestID, Config: op.Config, TargetHostID: hostID})
				request, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(origin, "/")+poolStorageClusterPath+"/verify", strings.NewReader(string(body)))
				request.Header.Set("Content-Type", "application/json")
				response, e := client.Do(request)
				client.CloseIdleConnections()
				if e == nil {
					code := response.StatusCode
					response.Body.Close()
					if code == http.StatusNoContent {
						return nil
					}
				}
				return errors.New("survivor fence outcome needs reconciliation")
			}
		}
		return errors.New("no surviving peer could record master fence verification")
	}
	proof, verifyErr := verifyStorageClusterFenceNative(ctx, s.dir, op.Config, hostID)
	proof.RequestID = requestID
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err = loadStorageClusters(s)
	if err != nil {
		return err
	}
	op = state.Operations[key]
	intent, ok := state.FenceRequests[requestID]
	if !ok || intent.RepositoryID != op.Config.RepositoryID || intent.TargetHostID != hostID || intent.ConfigDigest != op.Config.Digest() || proof.ConfigDigest != intent.ConfigDigest || proof.ConfigGeneration != op.Config.Generation {
		return errors.New("fence proof no longer matches its durable request")
	}
	// Keep the immutable result with its request receipt. The active profile has
	// one current proof per target and may later be replaced by a re-verification,
	// but that must not erase the prior destructive action's audit evidence.
	intent.Proof = &proof
	intent.UpdatedAt = time.Now().UTC()
	if verifyErr != nil {
		intent.Status, intent.Error = "needs_attention", "fence exclusion was not confirmed"
	} else {
		intent.Status, intent.Error = "succeeded", ""
	}
	state.FenceRequests[requestID] = intent
	acceptClusterFenceProof(&op, proof)
	op.UpdatedAt = time.Now().UTC()
	reduceFenceOperation(&op)
	if op.Config.FencingVerified() {
		promoteClusterOperation(s, &op)
	}
	state.Operations[key] = op
	if err = saveStorageClusters(s, state); err != nil {
		return err
	}
	if verifyErr == nil && op.Config.FencingVerified() {
		go func(op storageClusterOperation) {
			work, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			_, _ = applyStorageCluster(work, s, StorageClusterRequest{RequestID: op.RequestID}, true)
		}(op)
	}
	return verifyErr
}

// ResumeStorageClusterOperations resumes accepted identity discovery without mounting.
// Native cluster activation still requires the durable, exact fencing proofs.
func ResumeStorageClusterOperations(ctx context.Context, s *Store) {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return
	}
	for _, op := range StorageClusterStatus(s) {
		if op.Config.ClusterID == "" && op.Registration.Source.ISCSI != nil {
			resumeClusterOperation(ctx, s, op.RequestID, false)
			continue
		}
		// A prior gate-only failure is deliberately retried without Connect or a
		// CIB apply. The durable native receipt remains the boundary that keeps
		// this recovery path from turning an upgrade into a storage reapply.
		if op.Status == "needs_attention" && op.Error == clusterBootGateInstallFailure {
			if reconcileClusterBootGate(s, membership, op) == nil {
				restoreClusterBootGateOperation(s, op)
			}
			continue
		}
		if op.Status == "needs_attention" && op.Error == clusterNativeReceiptUnavailable {
			missing, receiptErr := clusterMissingNativeReceipt(s, op)
			if receiptErr != nil || missing {
				recordClusterReceiptUnavailable(s, op)
				continue
			}
			restoreClusterReceiptOperation(s, op)
			continue
		}
		// This has its own retry path because a master upgraded from the former
		// default Pacemaker fencing action must repair the Core-owned CIB policy
		// without falling through to Connect, resource creation, or a mount.
		if op.Status == "needs_attention" && op.Error == clusterFencingPolicyReconcileFailure {
			if reconcileClusterFencingPolicy(ctx, s, op) == nil {
				restoreClusterFencingPolicyOperation(ctx, s, op)
			}
			continue
		}
		if op.Status == "needs_attention" && op.Error == clusterTransportMonitorReconcileFailure {
			// A prior monitor upgrade failure must not freeze an old master
			// observation forever. Refreshing uses the real native health probe and
			// monitor together; the monitor probe alone never renews Healthy.
			refreshClusterMasterHealth(ctx, s, op)
			if current, ok := currentStorageClusterOperation(s, op); ok {
				_ = reconcileClusterTransportMonitor(ctx, s, current)
			}
			continue
		}
		pullClusterFenceProofs(ctx, s, &op)
		if !op.Config.FencingVerified() {
			continue
		}
		missingReceipt, receiptErr := clusterMissingNativeReceipt(s, op)
		if receiptErr != nil {
			recordClusterReceiptUnavailable(s, op)
			continue
		}
		// An activated operation can be needs_attention solely because its prior
		// master health report expired. Refresh and reconcile that live state
		// below; replaying Apply here would discard the upgrade boundary and can
		// never obtain the fresh report it needs.
		if op.Status == "queued" || op.Status == "failed" || missingReceipt || (op.Status == "needs_attention" && !clusterOperationActivated(op, membership, op.Config)) {
			resumeClusterOperation(ctx, s, op.RequestID, missingReceipt)
		} else {
			if reconcileClusterBootGate(s, membership, op) != nil {
				continue
			}
			// Establish a fresh joined health+monitor report before testing the
			// all-member upgrade barrier. A capability-only monitor observation is
			// never allowed to refresh an older healthy timestamp.
			refreshClusterMasterHealth(ctx, s, op)
			current, ok := currentStorageClusterOperation(s, op)
			if !ok {
				continue
			}
			if reconcileClusterTransportMonitor(ctx, s, current) != nil {
				continue
			}
			if reconcileClusterFencingPolicy(ctx, s, current) != nil {
				continue
			}
		}
	}
}

func currentStorageClusterOperation(s *Store, snapshot storageClusterOperation) (storageClusterOperation, bool) {
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		return storageClusterOperation{}, false
	}
	current, ok := state.Operations[snapshot.RequestID]
	return current, ok && current.Digest == snapshot.Digest
}

// reconcileClusterTransportMonitor verifies, without changing the CIB, the
// monitor-only iSCSI transport resource of a previously admitted current master
// operation. It is separate from Apply so a failed check never reruns connect,
// mount, resource creation, or fencing.
func reconcileClusterTransportMonitor(ctx context.Context, s *Store, snapshot storageClusterOperation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return errors.New("cluster transport monitor master intent is unavailable")
	}
	storageClusterMu.Lock()
	state, loadErr := loadStorageClusters(s)
	if loadErr != nil {
		storageClusterMu.Unlock()
		return loadErr
	}
	current, ok := state.Operations[snapshot.RequestID]
	storageClusterMu.Unlock()
	if !ok || current.Digest != snapshot.Digest {
		return recordClusterTransportMonitorUnavailable(s, snapshot)
	}
	// Initial activation owns CIB creation through Apply. This check is only
	// meaningful after the immutable activation receipt exists.
	if !clusterOperationActivated(current, membership, current.Config) {
		return nil
	}
	if !clusterAllMembersSessionMonitorReady(current, membership) {
		return recordClusterTransportMonitorUnavailable(s, snapshot)
	}
	if err := validClusterBootGateIntent(s, membership, current, current.Registration); err != nil {
		return recordClusterTransportMonitorUnavailable(s, snapshot)
	}
	missing, receiptErr := clusterMissingNativeReceipt(s, current)
	if receiptErr != nil || missing {
		return recordClusterTransportMonitorUnavailable(s, snapshot)
	}
	if err := reconcileNativeStorageClusterTransport(ctx, s.dir, current.Registration, current.Config); err != nil {
		return recordClusterTransportMonitorUnavailable(s, snapshot)
	}
	return ctx.Err()
}

func recordClusterTransportMonitorUnavailable(s *Store, snapshot storageClusterOperation) error {
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		return err
	}
	current, ok := state.Operations[snapshot.RequestID]
	if !ok || current.Digest != snapshot.Digest {
		return errors.New("cluster transport monitor intent changed")
	}
	current.Status, current.Error, current.UpdatedAt = "needs_attention", clusterTransportMonitorReconcileFailure, time.Now().UTC()
	state.Operations[snapshot.RequestID] = current
	if err := saveStorageClusters(s, state); err != nil {
		return err
	}
	return errors.New(clusterTransportMonitorReconcileFailure)
}

// reconcileClusterFencingPolicy updates only cluster-wide Pacemaker safety
// properties for a current master operation that already has a valid native
// receipt. It deliberately does not call Connect or the shared-CIB resource
// renderer.
func reconcileClusterFencingPolicy(ctx context.Context, s *Store, snapshot storageClusterOperation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return errors.New("cluster fencing policy master intent is unavailable")
	}
	storageClusterMu.Lock()
	state, loadErr := loadStorageClusters(s)
	if loadErr != nil {
		storageClusterMu.Unlock()
		return loadErr
	}
	current, ok := state.Operations[snapshot.RequestID]
	if !ok || current.Digest != snapshot.Digest {
		storageClusterMu.Unlock()
		return errors.New("cluster fencing policy intent changed")
	}
	storageClusterMu.Unlock()
	if (current.Status != "applying" && current.Status != "healthy") && (current.Status != "needs_attention" || current.Error != clusterFencingPolicyReconcileFailure) && !clusterOperationActivated(current, membership, current.Config) {
		return errors.New("cluster fencing policy intent is unavailable")
	}
	if err := validClusterBootGateIntent(s, membership, current, current.Registration); err != nil {
		return err
	}
	missingReceipt, receiptErr := clusterMissingNativeReceipt(s, current)
	if receiptErr != nil {
		return receiptErr
	}
	if missingReceipt {
		return errors.New("cluster fencing policy native receipt is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := reconcileNativeStorageClusterFencing(ctx, s.dir, current.Registration, current.Config); err == nil {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, loadErr = loadStorageClusters(s)
	if loadErr != nil {
		return loadErr
	}
	current, ok = state.Operations[snapshot.RequestID]
	if !ok || current.Digest != snapshot.Digest {
		return errors.New("cluster fencing policy intent changed")
	}
	current.Status, current.Error, current.UpdatedAt = "needs_attention", clusterFencingPolicyReconcileFailure, time.Now().UTC()
	state.Operations[snapshot.RequestID] = current
	if saveErr := saveStorageClusters(s, state); saveErr != nil {
		return saveErr
	}
	return errors.New(clusterFencingPolicyReconcileFailure)
}

func restoreClusterFencingPolicyOperation(ctx context.Context, s *Store, snapshot storageClusterOperation) {
	if ctx.Err() != nil {
		return
	}
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		return
	}
	current, ok := state.Operations[snapshot.RequestID]
	if !ok || current.Digest != snapshot.Digest || current.Status != "needs_attention" || current.Error != clusterFencingPolicyReconcileFailure {
		return
	}
	if ctx.Err() != nil {
		return
	}
	current.Status, current.Error, current.UpdatedAt = "applying", "", time.Now().UTC()
	state.Operations[snapshot.RequestID] = current
	_ = saveStorageClusters(s, state)
}

// reconcileClusterBootGate is safe for an already admitted master operation:
// it only validates Core's durable intent and installs the systemd dependency.
// It never touches iSCSI, Pacemaker, or the shared CIB.
func reconcileClusterBootGate(s *Store, membership protocol.PoolMembership, snapshot storageClusterOperation) error {
	if err := validClusterBootGateIntent(s, membership, snapshot, snapshot.Registration); err != nil {
		return err
	}
	if err := installClusterStorageBootGate(s); err == nil {
		return nil
	}
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, loadErr := loadStorageClusters(s)
	if loadErr != nil {
		return loadErr
	}
	current, ok := state.Operations[snapshot.RequestID]
	if !ok || current.Digest != snapshot.Digest {
		return errors.New("cluster boot gate intent changed")
	}
	current.Status, current.Error, current.UpdatedAt = "needs_attention", clusterBootGateInstallFailure, time.Now().UTC()
	state.Operations[snapshot.RequestID] = current
	if saveErr := saveStorageClusters(s, state); saveErr != nil {
		return saveErr
	}
	return errors.New(clusterBootGateInstallFailure)
}

func restoreClusterBootGateOperation(s *Store, snapshot storageClusterOperation) {
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		return
	}
	current, ok := state.Operations[snapshot.RequestID]
	if !ok || current.Digest != snapshot.Digest || current.Status != "needs_attention" || current.Error != clusterBootGateInstallFailure {
		return
	}
	// A local health receipt means activation already happened. Return to
	// applying so the normal health observer, rather than a native reapply,
	// determines whether all members remain healthy.
	current.Status, current.Error, current.UpdatedAt = "applying", "", time.Now().UTC()
	state.Operations[snapshot.RequestID] = current
	_ = saveStorageClusters(s, state)
}

func resumeClusterOperation(ctx context.Context, s *Store, id string, missingReceipt bool) {
	storageClusterWorkers.Lock()
	active := storageClusterWorkers.active[s][id]
	storageClusterWorkers.Unlock()
	if active {
		return
	}
	storageClusterMu.Lock()
	state, err := loadStorageClusters(s)
	if err == nil {
		op, ok := state.Operations[id]
		if !ok || (op.Config.ClusterID != "" && (!op.Config.FencingVerified() || (op.Status != "queued" && op.Status != "needs_attention" && op.Status != "failed" && !missingReceipt))) {
			storageClusterMu.Unlock()
			return
		}
		op.Status, op.Error = "queued", ""
		state.Operations[id] = op
		err = saveStorageClusters(s, state)
	}
	storageClusterMu.Unlock()
	if err == nil {
		_, err = applyStorageCluster(ctx, s, StorageClusterRequest{RequestID: id}, true)
	}
	if err != nil {
		storageClusterMu.Lock()
		defer storageClusterMu.Unlock()
		state, loadErr := loadStorageClusters(s)
		if loadErr == nil {
			op := state.Operations[id]
			// applyStorageCluster returns fixed, secret-free stage errors, never
			// native command output. Keep the stage actionable across restarts.
			op.Status, op.Error, op.UpdatedAt = "needs_attention", err.Error(), time.Now().UTC()
			state.Operations[id] = op
			_ = saveStorageClusters(s, state)
		}
	}
}

func clusterMissingNativeReceipt(s *Store, op storageClusterOperation) (bool, error) {
	// A health report alone is not a native receipt: it must be tied to the
	// local managed journal's immutable registration before it can suppress a
	// crash-recovery apply.
	actual, found, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).ClusteredStorageReceipt(op.Config.RepositoryID)
	if err != nil {
		return false, err
	}
	if !found || !sameClusterNativeReceipt(actual, op.Registration) || op.Config.ValidFor(actual) != nil {
		return true, nil
	}
	for _, report := range op.Members {
		// A pre-CIB monitor-support report has Stage=monitor and is intentionally
		// not a native receipt. It proves an upgraded member but cannot suppress
		// the crash-recovery apply which creates the shared resources.
		if report.HostID == s.State.HostID && report.Stage == "health" && report.Generation == op.Config.Generation && report.ConfigDigest == op.Config.Digest() {
			return false, nil
		}
	}
	return true, nil
}

func recordClusterReceiptUnavailable(s *Store, snapshot storageClusterOperation) {
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		return
	}
	current, ok := state.Operations[snapshot.RequestID]
	if !ok || current.Digest != snapshot.Digest {
		return
	}
	current.Status, current.Error, current.UpdatedAt = "needs_attention", clusterNativeReceiptUnavailable, time.Now().UTC()
	state.Operations[snapshot.RequestID] = current
	_ = saveStorageClusters(s, state)
}

func restoreClusterReceiptOperation(s *Store, snapshot storageClusterOperation) {
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		return
	}
	current, ok := state.Operations[snapshot.RequestID]
	if !ok || current.Digest != snapshot.Digest || current.Status != "needs_attention" || current.Error != clusterNativeReceiptUnavailable {
		return
	}
	current.Status, current.Error, current.UpdatedAt = "applying", "", time.Now().UTC()
	state.Operations[snapshot.RequestID] = current
	_ = saveStorageClusters(s, state)
}

func sameClusterNativeReceipt(actual, expected protocol.StorageRegistration) bool {
	if actual.ID != expected.ID || actual.PoolID != expected.PoolID || actual.HostID != expected.HostID || actual.Generation != expected.Generation || actual.Kind != expected.Kind || actual.Path != expected.Path || actual.Source.ISCSI == nil || expected.Source.ISCSI == nil || actual.Identity.ISCSI == nil || expected.Identity.ISCSI == nil {
		return false
	}
	return reflect.DeepEqual(*actual.Source.ISCSI, *expected.Source.ISCSI) && reflect.DeepEqual(*actual.Identity.ISCSI, *expected.Identity.ISCSI)
}

func refreshClusterMasterHealth(ctx context.Context, s *Store, snapshot storageClusterOperation) {
	status, err := observeStorageClusterHealth(ctx, s.dir, snapshot.Registration, snapshot.Config)
	if err != nil {
		status.Healthy = false
	}
	// Publish capability only with this real local health observation. A
	// packaging probe by itself must not refresh an older healthy report.
	status.SessionMonitorReady = clusterSessionMonitorReady(ctx, snapshot.Registration)
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, loadErr := loadStorageClusters(s)
	if loadErr != nil {
		return
	}
	op, ok := state.Operations[snapshot.RequestID]
	if !ok || op.Config.Digest() != snapshot.Config.Digest() {
		return
	}
	report := clusterMemberStatus{HostID: s.State.HostID, Status: status, Generation: op.Config.Generation, MembershipGeneration: op.MembershipGeneration, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: time.Now().UTC()}
	replaced := false
	for i := range op.Members {
		if op.Members[i].HostID == s.State.HostID {
			op.Members[i], replaced = report, true
		}
	}
	if !replaced {
		op.Members = append(op.Members, report)
	}
	op.UpdatedAt = report.UpdatedAt
	promoteClusterOperation(s, &op)
	state.Operations[op.RequestID] = op
	_ = saveStorageClusters(s, state)
}
func pullClusterFenceProofs(ctx context.Context, s *Store, op *storageClusterOperation) {
	for _, node := range op.Config.Nodes {
		if node.HostID == s.State.HostID {
			continue
		}
		membership, _ := Membership(s)
		origin := ""
		for _, member := range membership.Members {
			if member.HostID == node.HostID {
				origin = member.PeerOrigin
			}
		}
		if origin == "" {
			continue
		}
		client, err := PoolMemberPeerClient(s, origin, node.HostID)
		if err != nil {
			continue
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+poolStorageClusterPath+"/proof/"+url.PathEscape(op.Config.RepositoryID), nil)
		if err != nil {
			client.CloseIdleConnections()
			continue
		}
		response, err := client.Do(request)
		if err != nil || response.StatusCode != http.StatusOK {
			if response != nil {
				response.Body.Close()
			}
			client.CloseIdleConnections()
			continue
		}
		var proofs []protocol.StorageClusterFenceStatus
		err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&proofs)
		response.Body.Close()
		client.CloseIdleConnections()
		if err != nil {
			continue
		}
		for _, proof := range proofs {
			if proof.ConfigDigest != op.Config.Digest() || proof.ConfigGeneration != op.Config.Generation {
				continue
			}
			storageClusterMu.Lock()
			state, readErr := loadStorageClusters(s)
			intent, known := state.FenceRequests[proof.RequestID]
			if readErr != nil || !known || intent.ExecutorHostID != node.HostID || intent.TargetHostID != proof.HostID || intent.ConfigDigest != proof.ConfigDigest {
				storageClusterMu.Unlock()
				continue
			}
			intent.Proof, intent.UpdatedAt = &proof, time.Now().UTC()
			intent.Status = "needs_attention"
			if proof.Status == "passed" && proof.ExclusionVerified {
				intent.Status, intent.Error = "succeeded", ""
			}
			state.FenceRequests[proof.RequestID] = intent
			writeErr := saveStorageClusters(s, state)
			storageClusterMu.Unlock()
			if writeErr != nil {
				continue
			}
			acceptClusterFenceProof(op, proof)
		}
	}
	storageClusterMu.Lock()
	state, err := loadStorageClusters(s)
	if err == nil {
		current, ok := state.Operations[op.RequestID]
		if !ok {
			storageClusterMu.Unlock()
			return
		}
		mergeFenceProofs(&current, op.Config)
		reduceFenceOperation(&current)
		if current.Config.FencingVerified() && current.Status != "queued" {
			promoteClusterOperation(s, &current)
		}
		state.Operations[op.RequestID] = current
		_ = saveStorageClusters(s, state)
		*op = current
	}
	storageClusterMu.Unlock()
}

// StorageClusterConfigForRepository lets the config-aware member storage reconciler use
// only the master-approved durable configuration; there is no Controller fallback.
func StorageClusterConfigForRepository(s *Store, repositoryID string) (protocol.StorageClusterConfig, error) {
	membership, membershipErr := Membership(s)
	if membershipErr != nil {
		return protocol.StorageClusterConfig{}, errors.New("cluster membership is unavailable")
	}
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err := loadStorageClusters(s)
	if err != nil {
		return protocol.StorageClusterConfig{}, err
	}
	key, latest := latestClusterOperation(state, repositoryID)
	if key != "" && clusterCurrentMember(s.State.HostID, membership, latest.Config) && clusterOperationActivated(latest, membership, latest.Config) {
		return latest.Config, nil
	}
	return protocol.StorageClusterConfig{}, errors.New("cluster configuration is unavailable")
}

// PoolStorageClusterHandler is a peer-only pull endpoint. It sends no passwords: profiles
// carry opaque credential references, resolved independently on each root-owned Core.
func PoolStorageClusterHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		memberRoute := (r.Method == http.MethodPost && r.URL.Path == poolStorageClusterPath+"/verify") || (r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, poolStorageClusterPath+"/proof/"))
		var member protocol.PoolMember
		var err error
		if memberRoute {
			member, err = AuthenticatePoolMaster(s, r)
		} else {
			member, err = AuthenticatePoolPeer(s, r)
		}
		if err != nil {
			http.Error(w, "peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == poolStorageClusterPath+"/status" {
			var report clusterMemberStatus
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&report) != nil || report.HostID != member.HostID || report.Generation == 0 || report.ConfigDigest == "" || report.ClusterID == "" || report.MembershipGeneration == 0 {
				http.Error(w, "invalid cluster report", http.StatusBadRequest)
				return
			}
			// Freshness is measured by the receiving master, not a peer's clock.
			report.UpdatedAt = time.Now().UTC()
			storageClusterMu.Lock()
			state, e := loadStorageClusters(s)
			if e == nil {
				for id, op := range state.Operations {
					if op.Config.RepositoryID == report.Status.RepositoryID && op.Config.Generation == report.Generation && op.Config.ClusterID == report.ClusterID && op.Config.Digest() == report.ConfigDigest && op.MembershipGeneration == report.MembershipGeneration {
						if !clusterMemberReportAcknowledgesCurrentFence(report, op.Config) {
							continue
						}
						found := false
						for i := range op.Members {
							if op.Members[i].HostID == member.HostID {
								op.Members[i], found = report, true
							}
						}
						if !found {
							op.Members = append(op.Members, report)
						}
						op.UpdatedAt = time.Now().UTC()
						promoteClusterOperation(s, &op)
						state.Operations[id] = op
					}
				}
				e = saveStorageClusters(s, state)
			}
			storageClusterMu.Unlock()
			if e != nil {
				http.Error(w, "cluster report unavailable", 500)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == poolStorageClusterPath+"/verify" {
			var request clusterFencePeerRequest
			membership, membershipErr := Membership(s)
			if membershipErr != nil || member.HostID != membership.MasterHostID || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request) != nil || request.TargetHostID != membership.MasterHostID || request.TargetHostID == s.State.HostID || request.Config.PoolID != s.State.PoolID || request.RequestID == "" || len(request.RequestID) > 128 {
				http.Error(w, "invalid surviving-peer fence request", http.StatusBadRequest)
				return
			}
			if err := verifyClusterFenceOnSurvivor(s, request); err != nil {
				http.Error(w, "fence proof failed", 409)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, poolStorageClusterPath+"/proof/") {
			membership, membershipErr := Membership(s)
			repositoryID := strings.TrimPrefix(r.URL.Path, poolStorageClusterPath+"/proof/")
			if membershipErr != nil || member.HostID != membership.MasterHostID || repositoryID == "" || strings.ContainsAny(repositoryID, "/\\") {
				http.Error(w, "cluster proof unavailable", http.StatusConflict)
				return
			}
			storageClusterMu.Lock()
			state, e := loadStorageClusters(s)
			proofs := []protocol.StorageClusterFenceStatus{}
			if e == nil {
				for _, intent := range state.FenceRequests {
					if intent.RepositoryID == repositoryID && intent.ExecutorHostID == s.State.HostID && intent.Proof != nil {
						proofs = append(proofs, *intent.Proof)
					}
				}
			}
			storageClusterMu.Unlock()
			if e != nil {
				http.Error(w, "cluster proof unavailable", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(proofs)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != poolStorageClusterPath {
			http.NotFound(w, r)
			return
		}
		if membership, err := Membership(s); err != nil || membership.MasterHostID != s.State.HostID || member.HostID == s.State.HostID {
			http.Error(w, "cluster state unavailable", http.StatusConflict)
			return
		}
		key, keyErr := ClusterCorosyncAuthKey(s, s.State.PoolID)
		if keyErr != nil {
			http.Error(w, "cluster key unavailable", 409)
			return
		}
		operations := StorageClusterStatus(s)
		out := make([]storageClusterProjection, 0, len(operations))
		for _, op := range operations {
			registration, e := clusterRegistration(r.Context(), s, op.Config.RepositoryID)
			if e != nil {
				registration = op.Registration
				if registration.ID != op.Config.RepositoryID || registration.PoolID != s.State.PoolID {
					continue
				}
			}
			{
				registration.CredentialReceipt, registration.CredentialRequestID = "", ""
				secrets, secretErr := ExportStorageClusterSecrets(s, s.State.PoolID, op.RequestID, op.Config.RepositoryID, member.HostID)
				if secretErr != nil {
					http.Error(w, "cluster secret projection unavailable", http.StatusConflict)
					return
				}
				out = append(out, storageClusterProjection{Operation: op, Registration: registration, AuthKey: key, Secrets: secrets})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}

// Accepted survivor work outlives its requesting master. A lost reply or restart
// never repeats a destructive action: the durable receipt is the replay boundary.
func verifyClusterFenceOnSurvivor(s *Store, request clusterFencePeerRequest) error {
	digest := request.Config.Digest() + ":" + request.TargetHostID
	storageClusterMu.Lock()
	state, err := loadStorageClusters(s)
	if err != nil {
		storageClusterMu.Unlock()
		return err
	}
	if old, ok := state.FenceRequests[request.RequestID]; ok {
		storageClusterMu.Unlock()
		if old.Digest == digest && old.Status == "succeeded" && old.Proof != nil {
			return nil
		}
		return errors.New("fence request already recorded; reconcile its outcome")
	}
	key, accepted := latestClusterOperation(state, request.Config.RepositoryID)
	if key == "" || accepted.Config.Digest() != request.Config.Digest() {
		storageClusterMu.Unlock()
		return errors.New("survivor has not received this cluster configuration")
	}
	state.FenceRequests[request.RequestID] = storageFenceRequest{RequestID: request.RequestID, Digest: digest, ConfigDigest: request.Config.Digest(), RepositoryID: request.Config.RepositoryID, TargetHostID: request.TargetHostID, ExecutorHostID: s.State.HostID, Status: "verifying", UpdatedAt: time.Now().UTC()}
	err = saveStorageClusters(s, state)
	storageClusterMu.Unlock()
	if err != nil {
		return err
	}
	work, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	proof, verifyErr := verifyStorageClusterFenceNative(work, s.dir, request.Config, request.TargetHostID)
	cancel()
	proof.RequestID = request.RequestID
	storageClusterMu.Lock()
	defer storageClusterMu.Unlock()
	state, err = loadStorageClusters(s)
	if err != nil {
		return err
	}
	intent := state.FenceRequests[request.RequestID]
	intent.Proof, intent.UpdatedAt = &proof, time.Now().UTC()
	intent.Status = "succeeded"
	if verifyErr != nil {
		intent.Status, intent.Error = "needs_attention", "fence exclusion not confirmed"
	}
	state.FenceRequests[request.RequestID] = intent
	if err := saveStorageClusters(s, state); err != nil {
		return err
	}
	return verifyErr
}

// ReconcileStorageClustersFromMaster applies the durable master projection on a member.
// It never starts a VM and never treats a missing master as permission to proceed.
func ReconcileStorageClustersFromMaster(ctx context.Context, s *Store) error {
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		return nil
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(master, "/")+poolStorageClusterPath, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("pool master cluster state unavailable")
	}
	var projections []storageClusterProjection
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&projections) != nil {
		return errors.New("invalid pool master cluster state")
	}
	for _, projection := range projections {
		op, registration := projection.Operation, projection.Registration
		if op.Config.PoolID != s.State.PoolID || op.MembershipGeneration != membership.Generation {
			return errors.New("pool master cluster membership is stale")
		}
		included := false
		for _, node := range op.Config.Nodes {
			included = included || node.HostID == s.State.HostID
		}
		if !included {
			continue
		}
		bindings, importErr := ImportStorageClusterSecrets(s, projection.Secrets)
		if importErr != nil {
			return importErr
		}
		if bindings.CHAPReceipt != "" {
			registration.CredentialReceipt, registration.CredentialRequestID = bindings.CHAPReceipt, op.RequestID
		}
		for i := range op.Config.Nodes {
			target := op.Config.Nodes[i].HostID
			if ref := bindings.FenceBMC[target]; ref != "" {
				op.Config.Nodes[i].Fence.CredentialRef = ref
			}
			if ref := bindings.FenceSSH[target]; ref != "" {
				op.Config.Nodes[i].Fence.CredentialRef, op.Config.Nodes[i].Fence.SSHHostKeyRef = ref, ref
			}
		}
		if err = op.Config.ValidFor(registration); err != nil {
			return err
		}
		if err = SaveClusterCorosyncAuthKey(s, s.State.PoolID, projection.AuthKey); err != nil {
			return err
		}
		if err = installClusterAuthKey(projection.AuthKey); err != nil {
			return err
		}
		storageClusterMu.Lock()
		state, saveErr := loadStorageClusters(s)
		if saveErr == nil {
			state.Operations[op.RequestID] = op
			saveErr = saveStorageClusters(s, state)
		}
		storageClusterMu.Unlock()
		if saveErr != nil {
			return saveErr
		}
		// A currently applying/healthy fenced projection is sufficient to add
		// the boot gate. Do this before Connect so an upgrade records the boot
		// dependency even while an iSCSI path is temporarily unavailable.
		if err = validClusterBootGateIntent(s, membership, op, registration); err != nil {
			return err
		}
		if err = installClusterStorageBootGate(s); err != nil {
			return errors.New("cluster boot gate installation failed")
		}
		_, err = connectStorageCluster(ctx, s.dir, registration, op.Config)
		if err != nil {
			return errors.New("clustered iSCSI preparation failed")
		}
		// Every eligible member may project its own Corosync configuration, but
		// only the durable pool master reconciles the shared Pacemaker CIB.
		if err = startNativeStorageCluster(ctx, s.dir, op.Config); err != nil {
			return errors.New("local cluster start failed")
		}
		if !op.Config.FencingVerified() {
			return errors.New("native activation waits for exact Core-recorded fence proofs")
		}
		err := validateNativeStorageClusterAccess(ctx, s.dir, registration, op.Config)
		status, healthErr := observeStorageClusterHealth(ctx, s.dir, registration, op.Config)
		// This is a live local transport attestation, not aggregate cluster
		// health. A pre-monitor CIB can correctly report unhealthy while still
		// proving the fixed monitor wrapper and exact iSCSI session are usable.
		status.SessionMonitorReady = clusterSessionMonitorReady(ctx, registration)
		report := clusterMemberStatus{HostID: s.State.HostID, Status: status, Generation: registration.Generation, MembershipGeneration: membership.Generation, ClusterID: op.Config.ClusterID, ConfigDigest: op.Config.Digest(), Stage: "health", UpdatedAt: time.Now().UTC()}
		_ = postClusterMemberStatus(ctx, s, master, report)
		if err != nil || healthErr != nil || !status.Healthy || !status.Quorate || !status.Mounted {
			return errors.New("clustered iSCSI preparation failed")
		}
	}
	return nil
}

func postClusterMemberStatus(ctx context.Context, s *Store, master string, report clusterMemberStatus) error {
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	b, err := json.Marshal(report)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolStorageClusterPath+"/status", strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("pool master rejected cluster report")
	}
	return nil
}
