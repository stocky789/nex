// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Pool-wide ZFS provisioning has one durable master receipt and one local receipt
// per host.  The master never guesses a member's devices or retries a destructive
// action after an ambiguous member result.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const (
	poolZFSPath  = "/api/v1/pool/zfs-provision"
	poolZFSLimit = 128 << 10
)

type zfsPoolOperation struct {
	Request      protocol.ZFSProvisionRequest               `json:"request"`
	RepositoryID string                                     `json:"repository_id"`
	Outcomes     map[string]protocol.ZFSProvisionHostResult `json:"outcomes"`
}
type zfsPoolState struct {
	Operations map[string]zfsPoolOperation `json:"operations"`
}
type zfsPoolPeerRequest struct {
	OperationID  string                       `json:"operation_id"`
	RepositoryID string                       `json:"repository_id"`
	Request      protocol.ZFSProvisionRequest `json:"request"`
	Host         protocol.ZFSProvisionHost    `json:"host"`
}

var poolZFSMu sync.Mutex

// ponytail: serializes pool provisioning in one Core process; use per-operation
// locks only if independently selected disks need concurrent provisioning.
var poolZFSRunMu sync.Mutex

// poolZFSExecuteLocal is a seam for the peer and aggregate checks. The real
// worker remains the only code that touches disks.
var poolZFSExecuteLocal = executePoolZFSLocal
var poolZFSSucceededReceipt = func(ctx context.Context, s *Store, operationID, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) bool {
	return vmhost.NewZFSProvisioner(filepath.Join(s.dir, "zfs-provision")).CanResumeZFSProvision(ctx, operationID, repositoryID, request, host)
}

func poolZFSFile(s *Store) string { return filepath.Join(s.dir, "zfs-pool.json") }
func loadPoolZFS(s *Store) (zfsPoolState, error) {
	state := zfsPoolState{Operations: map[string]zfsPoolOperation{}}
	b, err := os.ReadFile(poolZFSFile(s))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if json.Unmarshal(b, &state) != nil || state.Operations == nil {
		return state, errors.New("invalid ZFS pool journal")
	}
	return state, nil
}
func savePoolZFS(s *Store, state zfsPoolState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(poolZFSFile(s), b, 0600)
}

// PoolZFSInventory is the master-owned adoption projection. A member reports
// its normal local storage registration, never a competing aggregate record.
func PoolZFSInventory(s *Store) []protocol.ZFSProvisionHostResult {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return nil
	}
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	state, err := loadPoolZFS(s)
	if err != nil {
		return nil
	}
	// Project one latest durable outcome per repository/host so a fresh
	// Controller never sees duplicate or map-order-dependent rows.
	aggregates := foldPoolZFSOperations(state)
	repositories := make([]string, 0, len(aggregates))
	for repositoryID := range aggregates {
		repositories = append(repositories, repositoryID)
	}
	slices.Sort(repositories)
	var out []protocol.ZFSProvisionHostResult
	for _, repositoryID := range repositories {
		outcomes := aggregates[repositoryID].Outcomes
		hosts := make([]string, 0, len(outcomes))
		for host := range outcomes {
			hosts = append(hosts, host)
		}
		slices.Sort(hosts)
		for _, host := range hosts {
			out = append(out, outcomes[host])
		}
	}
	return out
}

// foldPoolZFSOperations merges every durable operation for a repository in
// deterministic operation order. A configure-host call appends another intent
// for the same canonical repository, and a later one must never hide an earlier
// successful host from a fresh Controller.
func foldPoolZFSOperations(state zfsPoolState) map[string]zfsPoolOperation {
	ids := make([]string, 0, len(state.Operations))
	for id := range state.Operations {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	aggregates := map[string]zfsPoolOperation{}
	for _, id := range ids {
		op := state.Operations[id]
		merged, found := aggregates[op.RepositoryID]
		if !found {
			merged = zfsPoolOperation{Request: op.Request, RepositoryID: op.RepositoryID, Outcomes: map[string]protocol.ZFSProvisionHostResult{}}
		}
		if op.Request.Name != "" {
			merged.Request.Name, merged.Request.Scope = op.Request.Name, op.Request.Scope
		}
		for host, outcome := range op.Outcomes {
			merged.Outcomes[host] = outcome
		}
		aggregates[op.RepositoryID] = merged
	}
	return aggregates
}

// PoolZFSStorageProjection publishes one master-owned logical registration per
// repository with explicit local-host observations. It does not make the
// repository shared or copy a member identity onto the master.
func PoolZFSStorageProjection(ctx context.Context, s *Store, storage []protocol.StorageRegistration) []protocol.StorageRegistration {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return storage
	}
	poolZFSMu.Lock()
	state, err := loadPoolZFS(s)
	poolZFSMu.Unlock()
	if err != nil {
		return storage
	}
	out := append([]protocol.StorageRegistration(nil), storage...)
	aggregates := foldPoolZFSOperations(state)
	repositories := make([]string, 0, len(aggregates))
	for id := range aggregates {
		repositories = append(repositories, id)
	}
	slices.Sort(repositories)
	for _, repositoryID := range repositories {
		op := aggregates[repositoryID]
		// Each configured host is probed over the peer API at most once per
		// repository per projection pass.
		probed := map[string]protocol.ZFSHostAccess{}
		observe := func(hostID string) protocol.ZFSHostAccess {
			access, seen := probed[hostID]
			if !seen {
				access = observePoolZFSAccess(ctx, s, hostID, op.RepositoryID, storage)
				probed[hostID] = access
			}
			return access
		}
		generation := zfsLifecycleGeneration(s, repositoryID)
		hasSucceededHost := false
		for _, outcome := range op.Outcomes {
			if outcome.Status == "succeeded" {
				hasSucceededHost = true
				break
			}
		}
		index := -1
		for i := range out {
			if out[i].ID == op.RepositoryID {
				index = i
				break
			}
		}
		// A durable intent alone is not a repository.  Older builds mistakenly
		// overwrote a successful provision outcome with a later lifecycle error.
		// Repair only that historical journal shape when a configured host still
		// reports the Core-provisioned registration with both native GUIDs and a
		// path.  This is an authenticated current native observation, never an
		// inferred success from a failed lifecycle operation.
		if index < 0 && !hasSucceededHost && !zfsPoolHasObservedRegistration(op, observe) {
			continue
		}
		synthetic := index < 0
		if index < 0 {
			out = append(out, vmhost.ZFSProvisionStorage(op.RepositoryID, op.Request.PoolID, s.State.HostID, op.Request.Name))
			index = len(out) - 1
			out[index].ZFSScope = op.Request.Scope
			out[index].Generation = generation
			out[index].Capabilities = protocol.StorageCapabilities{VMDisk: true, Thin: true}
			out[index].Status = "succeeded"
			out[index].Error = ""
		}
		out[index].Generation = generation
		access := make([]protocol.ZFSHostAccess, 0, len(op.Outcomes))
		liveHost := false
		hosts := make([]string, 0, len(op.Outcomes))
		for host := range op.Outcomes {
			hosts = append(hosts, host)
		}
		slices.Sort(hosts)
		for _, host := range hosts {
			v := op.Outcomes[host]
			observed := observe(host)
			if observed.PoolGUID == "" {
				observed.PoolGUID = v.PoolGUID
			}
			if observed.DatasetGUID == "" {
				observed.DatasetGUID = v.DatasetGUID
			}
			// An unobservable host has no path: back-filling the repository mount
			// path there would claim access this host is not reporting.
			if observed.Path == "" && observed.Error == "" && out[index].Source.ZFS != nil {
				observed.Path = out[index].Source.ZFS.MountPath
			}
			liveHost = liveHost || (observed.Readable && observed.Writable && observed.Path != "" && observed.Error == "")
			access = append(access, observed)
		}
		if synthetic && !liveHost {
			// A stale aggregate outcome cannot resurrect a repository after its
			// native receipt disappeared. Keep the host-owned intent visible, but
			// make it ineligible until an authenticated current host observation.
			out[index].Status = "unknown"
			out[index].Error = "repository has no current host observation"
		}
		out[index].ZFSProvisioned, out[index].ZFSScope, out[index].ZFSHostAccess = true, op.Request.Scope, access
	}
	// Pool detach removes the master aggregate record only after every member
	// has durably unregistered its local alias.  The master may have no local
	// dataset of its own, so retain its completed lifecycle receipt as the
	// canonical tombstone a replacement Controller needs to remove the mirror.
	for _, tombstone := range poolZFSDetachTombstones(s) {
		newerRegistration := false
		for _, entry := range out {
			if entry.ID == tombstone.ID && !entry.Tombstone && entry.Generation > tombstone.Generation {
				newerRegistration = true
				break
			}
		}
		if newerRegistration {
			continue
		}
		filtered := out[:0]
		for _, entry := range out {
			if entry.ID != tombstone.ID {
				filtered = append(filtered, entry)
			}
		}
		out = append(filtered, tombstone)
	}
	return out
}

func poolZFSDetachTombstones(s *Store) []protocol.StorageRegistration {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	lifecycle, err := loadZFSLifecycleState(s)
	if err != nil {
		return nil
	}
	var out []protocol.StorageRegistration
	for _, completion := range lifecycle.Operations {
		if !completion.DetachReady || (!completion.Completed && zfsPoolRepositoryExists(s, completion.RepositoryID)) || completion.RepositoryID == "" || completion.PoolID == "" || completion.HostID == "" || completion.Generation == 0 {
			continue
		}
		out = append(out, protocol.StorageRegistration{ID: completion.RepositoryID, PoolID: completion.PoolID, HostID: completion.HostID, Generation: completion.Generation, Tombstone: true, Kind: protocol.StorageKindZFS, ZFSProvisioned: true, ZFSScope: "pool"})
	}
	slices.SortFunc(out, func(a, b protocol.StorageRegistration) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func zfsPoolRepositoryExists(s *Store, repositoryID string) bool {
	state, err := loadPoolZFS(s)
	if err != nil {
		return true // unreadable state must never manufacture a deletion.
	}
	for _, operation := range state.Operations {
		if operation.RepositoryID == repositoryID {
			return true
		}
	}
	return false
}

func zfsPoolHasObservedRegistration(op zfsPoolOperation, observe func(string) protocol.ZFSHostAccess) bool {
	for hostID := range op.Outcomes {
		access := observe(hostID)
		if access.PoolGUID != "" && access.DatasetGUID != "" && access.Path != "" && access.Error == "" {
			return true
		}
	}
	return false
}

// zfsHostAccess is the one shape a host's ZFS repository observation takes, whether this
// host reports its own or answers the master's peer probe.
func zfsHostAccess(hostID string, entry protocol.StorageRegistration) protocol.ZFSHostAccess {
	return protocol.ZFSHostAccess{HostID: hostID, PoolGUID: entry.Identity.ZFS.PoolGUID, DatasetGUID: entry.Identity.ZFS.DatasetGUID, Path: entry.Path, Readable: entry.Access.Readable, Writable: entry.Access.Writable, TotalBytes: entry.Access.TotalBytes, AvailableBytes: entry.Access.AvailableBytes, ObservedAt: entry.Access.ObservedAt, Error: entry.Access.Error, Pool: entry.Access.ZFSPool}
}

func observePoolZFSAccess(ctx context.Context, s *Store, hostID, repositoryID string, storage []protocol.StorageRegistration) protocol.ZFSHostAccess {
	if hostID == s.State.HostID {
		for _, entry := range storage {
			if entry.ID == repositoryID && entry.ZFSProvisioned && entry.Identity.ZFS != nil {
				return zfsHostAccess(hostID, entry)
			}
		}
		return protocol.ZFSHostAccess{HostID: hostID, Error: "local ZFS repository is not observed"}
	}
	membership, err := Membership(s)
	if err != nil {
		return protocol.ZFSHostAccess{HostID: hostID, Error: "pool membership is unavailable"}
	}
	for _, member := range membership.Members {
		// The same fence the lifecycle dispatcher applies: a member that is not
		// a ready current member is not an authority on this repository.
		if member.HostID != hostID || member.PeerOrigin == "" || member.Status != "ready" || member.Generation != membership.Generation {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		client, err := PoolMemberPeerClient(s, member.PeerOrigin, hostID)
		if err != nil {
			cancel()
			return protocol.ZFSHostAccess{HostID: hostID, Error: "member access is unavailable"}
		}
		defer client.CloseIdleConnections()
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, strings.TrimRight(member.PeerOrigin, "/")+poolZFSPath+"/"+repositoryID+"/access", nil)
		if err != nil {
			cancel()
			return protocol.ZFSHostAccess{HostID: hostID, Error: "member access is unavailable"}
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			return protocol.ZFSHostAccess{HostID: hostID, Error: "member access is unavailable"}
		}
		defer func() { resp.Body.Close(); cancel() }()
		var access protocol.ZFSHostAccess
		if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, poolZFSLimit)).Decode(&access) != nil || access.HostID != hostID {
			return protocol.ZFSHostAccess{HostID: hostID, Error: "member access is unavailable"}
		}
		return access
	}
	return protocol.ZFSHostAccess{HostID: hostID, Error: "member peer endpoint is unavailable"}
}

func zfsPoolRequest(cmd protocol.Command, identity State) (protocol.ZFSProvisionRequest, error) {
	if cmd.OperationID == "" || cmd.ZFSProvision == nil || protocol.ValidateZFSProvision(*cmd.ZFSProvision) != nil {
		return protocol.ZFSProvisionRequest{}, errors.New("invalid ZFS provision command")
	}
	r := *cmd.ZFSProvision
	if r.PoolID != identity.PoolID {
		return r, errors.New("ZFS provision targets another pool")
	}
	return r, nil
}

// ExecutePoolZFSProvision is the master-side aggregate. It persists exact device
// selections before contacting any member, so a retry can only replay them.
func ExecutePoolZFSProvision(ctx context.Context, s *Store, cmd protocol.Command) (protocol.Result, error) {
	poolZFSRunMu.Lock()
	defer poolZFSRunMu.Unlock()
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	request, err := zfsPoolRequest(cmd, s.State)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		result.Error = "only the pool master may provision ZFS storage"
		return result, nil
	}
	members := map[string]protocol.PoolMember{}
	for _, member := range membership.Members {
		members[member.HostID] = member
	}
	for _, host := range request.Hosts {
		member, ok := members[host.HostID]
		if !ok || member.Status != "ready" || member.Generation != membership.Generation {
			result.Error = "selected ZFS host is not a ready current member"
			return result, nil
		}
	}
	poolZFSMu.Lock()
	state, err := loadPoolZFS(s)
	invalidInput := false
	if err == nil {
		op, found := state.Operations[cmd.OperationID]
		if found {
			compare := request
			compare.RepositoryID = op.RepositoryID
			if !reflect.DeepEqual(op.Request, compare) {
				err = errors.New("ZFS operation ID has different input")
			}
			request.RepositoryID = op.RepositoryID
		} else {
			if request.RepositoryID == "" {
				request.RepositoryID = zfsRepositoryID(cmd.OperationID)
			} else {
				exists := false
				var canonical protocol.ZFSProvisionRequest
				configuredHosts := map[string]bool{}
				for _, prior := range state.Operations {
					if prior.RepositoryID == request.RepositoryID {
						exists = true
						if canonical.PoolID == "" {
							canonical = prior.Request
						}
						for _, host := range prior.Request.Hosts {
							configuredHosts[host.HostID] = true
						}
					}
				}
				if !exists {
					err = errors.New("ZFS repository ID is not a Core-owned repository")
					invalidInput = true
				} else if canonical.Scope != "pool" || request.Scope != "pool" || request.Name != canonical.Name {
					err = errors.New("existing ZFS repository may only configure additional hosts with its durable pool scope and name")
					invalidInput = true
				} else {
					for _, host := range request.Hosts {
						if configuredHosts[host.HostID] {
							err = errors.New("existing ZFS host configuration must be replayed exactly, not replaced")
							invalidInput = true
							break
						}
					}
				}
			}
			if err == nil {
				op = zfsPoolOperation{Request: request, RepositoryID: request.RepositoryID, Outcomes: map[string]protocol.ZFSProvisionHostResult{}}
				state.Operations[cmd.OperationID] = op
				err = savePoolZFS(s, state)
			}
		}
	}
	poolZFSMu.Unlock()
	if err != nil {
		result.Error = err.Error()
		if invalidInput {
			return result, nil
		}
		return result, err
	}
	for _, host := range request.Hosts {
		poolZFSMu.Lock()
		current, loadErr := loadPoolZFS(s)
		prior := current.Operations[cmd.OperationID].Outcomes[host.HostID]
		poolZFSMu.Unlock()
		if loadErr != nil {
			result.Error = "ZFS pool journal is unavailable"
			return result, loadErr
		}
		if prior.Status == "succeeded" {
			continue
		}
		outcome := dispatchPoolZFSHost(ctx, s, membership, cmd.OperationID, request, host)
		poolZFSMu.Lock()
		state, err = loadPoolZFS(s)
		if err == nil {
			op := state.Operations[cmd.OperationID]
			op.Outcomes[host.HostID] = outcome
			state.Operations[cmd.OperationID] = op
			err = savePoolZFS(s, state)
		}
		poolZFSMu.Unlock()
		if err != nil {
			result.Error = "ZFS pool outcome was not persisted"
			return result, err
		}
	}
	poolZFSMu.Lock()
	state, err = loadPoolZFS(s)
	op := state.Operations[cmd.OperationID]
	poolZFSMu.Unlock()
	if err != nil {
		result.Error = "ZFS pool journal is unavailable"
		return result, err
	}
	result.Status = "succeeded"
	for _, host := range request.Hosts {
		if op.Outcomes[host.HostID].Status != "succeeded" {
			result.Status = "needs_attention"
			result.Error = "one or more selected ZFS hosts need attention"
			break
		}
	}
	result.ObservedAt = time.Now().UTC()
	return result, nil
}

// ResumePoolZFSOperations continues durable incomplete pool operations on the
// master. It is invoked by Core's worker, not a Controller retry: the exact
// request/devices were saved before dispatch and completed hosts are skipped.
func ResumePoolZFSOperations(ctx context.Context, s *Store) ([]protocol.Result, error) {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return nil, err
	}
	poolZFSMu.Lock()
	state, err := loadPoolZFS(s)
	poolZFSMu.Unlock()
	if err != nil {
		return nil, err
	}
	operationIDs := make([]string, 0, len(state.Operations))
	for operationID := range state.Operations {
		operationIDs = append(operationIDs, operationID)
	}
	slices.Sort(operationIDs)
	results := make([]protocol.Result, 0, len(operationIDs))
	for _, operationID := range operationIDs {
		operation := state.Operations[operationID]
		complete := len(operation.Request.Hosts) != 0
		for _, host := range operation.Request.Hosts {
			if operation.Outcomes[host.HostID].Status != "succeeded" {
				complete = false
				break
			}
		}
		if complete {
			results = append(results, protocol.Result{OperationID: operationID, Status: "succeeded", ObservedAt: time.Now().UTC()})
			continue
		}
		request := operation.Request
		request.RepositoryID = operation.RepositoryID
		// A local failed host may be a create that never reached a tagged pool.
		// Resume only after the native receipt and live pool/dataset verify; a
		// failed storage registration by itself is never proof of a repository.
		localProof := true
		for _, host := range request.Hosts {
			if host.HostID == s.State.HostID && operation.Outcomes[host.HostID].Status != "succeeded" && !poolZFSSucceededReceipt(ctx, s, operationID, operation.RepositoryID, request, host) {
				localProof = false
				break
			}
		}
		if !localProof {
			continue
		}
		result, executeErr := ExecutePoolZFSProvision(ctx, s, protocol.Command{OperationID: operationID, Kind: "storage-zfs-provision", ZFSProvision: &request})
		results = append(results, result)
		if executeErr != nil {
			return results, executeErr
		}
	}
	return results, nil
}

func dispatchPoolZFSHost(ctx context.Context, s *Store, membership protocol.PoolMembership, operationID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
	if host.HostID == s.State.HostID {
		return poolZFSExecuteLocal(ctx, s, operationID, request.RepositoryID, request, host)
	}
	member, ok := poolMemberByID(membership, host.HostID)
	if !ok || member.PeerOrigin == "" {
		return zfsPoolFailure(request, host, "selected ZFS host is unreachable")
	}
	client, err := PoolMemberPeerClient(s, member.PeerOrigin, host.HostID)
	if err != nil {
		return zfsPoolFailure(request, host, "selected ZFS host is unreachable")
	}
	defer client.CloseIdleConnections()
	// Hot spares and the widened layouts need storage-zfs-replace-v1. An older
	// member's strict peer decoder rejects those fields with an opaque 400; refuse
	// here with an upgrade sentence before any destructive selection is posted.
	if protocol.ZFSProvisionNeedsReplaceCapability(protocol.ZFSProvisionRequest{Hosts: []protocol.ZFSProvisionHost{host}}) {
		supported, probeErr := poolZFSMemberSupportsReplace(ctx, client, member.PeerOrigin)
		if probeErr != nil {
			return zfsPoolFailure(request, host, "selected ZFS host is unreachable")
		}
		if !supported {
			return zfsPoolFailure(request, host, "hot spares and widened ZFS layouts need "+protocol.ZFSReplaceCapability+" on every selected host; upgrade Nex Core")
		}
	}
	body, _ := json.Marshal(zfsPoolPeerRequest{OperationID: operationID, RepositoryID: request.RepositoryID, Request: request, Host: host})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolZFSPath, bytes.NewReader(body))
	if err != nil {
		return zfsPoolFailure(request, host, "selected ZFS host is unreachable")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return zfsPoolFailure(request, host, "selected ZFS host is unreachable")
	}
	defer resp.Body.Close()
	var outcome protocol.ZFSProvisionHostResult
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, poolZFSLimit)).Decode(&outcome) != nil || outcome.HostID != host.HostID || outcome.RepositoryID != request.RepositoryID {
		return zfsPoolFailure(request, host, "selected ZFS host refused provisioning")
	}
	return outcome
}

// poolZFSMemberSupportsReplace probes the member's replace peer route with an empty
// body. A Core that advertises storage-zfs-replace-v1 answers "invalid ZFS disk
// replacement"; an older Core falls through to the provision handler and answers
// "invalid ZFS provision". The probe never reaches wipe or zpool.
var poolZFSMemberSupportsReplace = func(ctx context.Context, client *http.Client, origin string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(origin, "/")+poolZFSReplacePath, bytes.NewReader([]byte("{}")))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "disk replacement"), nil
}

func zfsPoolFailure(request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost, message string) protocol.ZFSProvisionHostResult {
	return protocol.ZFSProvisionHostResult{RepositoryID: request.RepositoryID, PoolID: request.PoolID, Name: request.Name, Scope: request.Scope, HostID: host.HostID, Layout: protocol.ZFSLayout(host.Layout, len(host.Devices)), Devices: host.Devices, Status: "needs_attention", Error: message}
}

func executePoolZFSLocal(ctx context.Context, s *Store, operationID, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
	outcome, _ := vmhost.NewZFSProvisioner(filepath.Join(s.dir, "zfs-provision")).Provision(ctx, operationID, repositoryID, request, host)
	if outcome.Status == "succeeded" {
		registration := vmhost.ZFSProvisionStorage(repositoryID, request.PoolID, s.State.HostID, request.Name)
		registration.ZFSScope = request.Scope
		registered, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Execute(vmhost.WithZFSProvisionRegistrationRetry(ctx), protocol.Command{OperationID: zfsRegistrationOperationID(repositoryID), Kind: "storage-register", Storage: &registration}, nil)
		if err != nil || registered.Status != "succeeded" {
			outcome.Status, outcome.Error = "needs_attention", "ZFS dataset was created but storage registration did not complete"
		}
	}
	return outcome
}

// PoolZFSHandler is member-only and accepts only the current master certificate.
func PoolZFSHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := AuthenticatePoolMaster(s, r); err != nil {
			http.Error(w, "ZFS peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/access") {
			repositoryID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, poolZFSPath+"/"), "/access")
			if repositoryID == "" || strings.Contains(repositoryID, "/") {
				http.Error(w, "invalid ZFS repository", http.StatusBadRequest)
				return
			}
			managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(r.Context())
			// The journal holds access as it was at registration. Answer with the live
			// observation, as this host's own report does, or the master sees it go stale.
			if monitor := s.storageMonitor.Load(); monitor != nil {
				managed.Storage = monitor.Overlay(managed.Storage)
			}
			for _, entry := range managed.Storage {
				if entry.ID == repositoryID && entry.ZFSProvisioned && entry.Identity.ZFS != nil {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(zfsHostAccess(s.State.HostID, entry))
					return
				}
			}
			http.Error(w, "ZFS repository unavailable", http.StatusConflict)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request zfsPoolPeerRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, poolZFSLimit))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.OperationID == "" || request.RepositoryID == "" || protocol.ValidateZFSProvision(request.Request) != nil || request.Request.RepositoryID != request.RepositoryID || request.Request.PoolID != s.State.PoolID || request.Host.HostID != s.State.HostID {
			http.Error(w, "invalid ZFS provision", http.StatusBadRequest)
			return
		}
		matched := false
		for _, host := range request.Request.Hosts {
			if reflect.DeepEqual(host, request.Host) {
				matched = true
				break
			}
		}
		if !matched {
			http.Error(w, "selected ZFS host does not match request", http.StatusBadRequest)
			return
		}
		outcome := poolZFSExecuteLocal(r.Context(), s, request.OperationID, request.RepositoryID, request.Request, request.Host)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(outcome)
	})
}
