// SPDX-License-Identifier: AGPL-3.0-only
package agent

// ZFS lifecycle keeps the pool-wide logical repository on the master. Each
// host still owns its local zpool; detach only unregisters its Core alias.

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
	"sort"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolZFSLifecyclePath = poolZFSPath + "/lifecycle"

type zfsLifecyclePeerRequest struct {
	Phase   string           `json:"phase"`
	Command protocol.Command `json:"command"`
}

type zfsLifecycleState struct {
	Generations map[string]uint64                          `json:"generations"`
	Operations  map[string]zfsLifecycleOperationCompletion `json:"operations,omitempty"`
}

// zfsLifecycleOperationCompletion is deliberately separate from the provision
// journal.  Lifecycle failures must never rewrite a successful disk-provision
// outcome: the latter is the durable evidence used for inventory/adoption.
type zfsLifecycleOperationCompletion struct {
	RepositoryID string                     `json:"repository_id"`
	PoolID       string                     `json:"pool_id,omitempty"`
	HostID       string                     `json:"host_id,omitempty"`
	Generation   uint64                     `json:"generation,omitempty"`
	Outcomes     map[string]protocol.Result `json:"outcomes"`
	DetachReady  bool                       `json:"detach_ready,omitempty"`
	Completed    bool                       `json:"completed,omitempty"`
}

var zfsLifecycleDispatch = dispatchZFSLifecycle
var removePoolZFSAggregate = removePoolZFS

var poolZFSLifecycleRunMu sync.Mutex

// ExecutePoolZFSLifecycle handles only repositories created by the pool ZFS
// provisioner. handled is false for ordinary storage registrations, which keep
// their existing lifecycle path.
func ExecutePoolZFSLifecycle(ctx context.Context, s *Store, cmd protocol.Command) (result protocol.Result, handled bool, err error) {
	v := cmd.StorageLifecycle
	if v == nil || (cmd.Kind != "storage-update" && cmd.Kind != "storage-recheck" && cmd.Kind != "storage-detach") {
		return result, false, nil
	}
	if err := protocol.ValidateStorageLifecycle(*v); err != nil {
		return zfsLifecycleResult(cmd, "failed", err), true, nil
	}
	poolZFSLifecycleRunMu.Lock()
	defer poolZFSLifecycleRunMu.Unlock()
	// Identify Core-created ZFS before enforcing master authority. Ordinary ZFS
	// registrations keep the normal local lifecycle path, including on members.
	op, hosts, found, lookupErr := zfsLifecycleOperation(s, v.RepositoryID)
	if lookupErr != nil {
		return zfsLifecycleResult(cmd, "failed", lookupErr), true, lookupErr
	}
	if !found {
		if v.Action == "detach" && zfsLifecycleDetachReady(s, v.RepositoryID, cmd.OperationID) {
			membership, membershipErr := Membership(s)
			if membershipErr == nil && membership.MasterHostID == s.State.HostID && v.PoolID == s.State.PoolID {
				if err := completeZFSLifecycleDetach(s, v.RepositoryID, cmd.OperationID); err != nil {
					return zfsLifecycleResult(cmd, "needs_attention", err), true, err
				}
				return zfsLifecycleResult(cmd, "succeeded", nil), true, nil
			}
		}
		return result, false, nil
	}
	membership, membershipErr := Membership(s)
	if membershipErr != nil || membership.MasterHostID != s.State.HostID {
		return zfsLifecycleResult(cmd, "failed", errors.New("only the pool master may change pool ZFS storage")), true, nil
	}
	if v.PoolID != s.State.PoolID {
		return zfsLifecycleResult(cmd, "failed", errors.New("ZFS lifecycle targets another pool")), true, nil
	}
	if !zfsLifecycleCurrent(ctx, s, op, hosts) {
		return zfsLifecycleResult(cmd, "failed", errors.New("ZFS repository has hosts that are not currently configured")), true, nil
	}

	switch v.Action {
	case "update":
		if v.HostID != s.State.HostID || !zfsRenameOnly(op, *v) {
			return zfsLifecycleResult(cmd, "failed", errors.New("ZFS repository update may only rename the master aggregate")), true, nil
		}
		if err := renamePoolZFS(s, v.RepositoryID, v.ExpectedGeneration, v.Registration.Name); err != nil {
			return zfsLifecycleResult(cmd, "failed", err), true, err
		}
		return zfsLifecycleResult(cmd, "succeeded", nil), true, nil
	case "recheck":
		targets := []string{v.HostID}
		if !containsZFSHost(hosts, v.HostID) {
			if v.HostID != s.State.HostID {
				return zfsLifecycleResult(cmd, "failed", errors.New("ZFS recheck targets a host not configured for this repository")), true, nil
			}
			// The master owns the aggregate but need not have a local zpool.
			targets = hosts
		}
		for _, hostID := range targets {
			result, err = zfsLifecycleDispatch(ctx, s, membership, hostID, "execute", zfsLifecycleCommandForHost(cmd, hostID))
			result = zfsLifecycleDispatched(cmd, result, err)
			if persistErr := persistZFSLifecycleOutcome(s, cmd.OperationID, v.RepositoryID, hostID, result); persistErr != nil {
				return zfsLifecycleResult(cmd, "needs_attention", persistErr), true, persistErr
			}
			if err != nil || result.Status != "succeeded" {
				return result, true, err
			}
		}
		return result, true, err
	case "detach":
		if v.HostID != s.State.HostID {
			return zfsLifecycleResult(cmd, "failed", errors.New("ZFS detach must target the pool master aggregate")), true, nil
		}
		if generation := zfsLifecycleGeneration(s, v.RepositoryID); generation != v.ExpectedGeneration {
			return zfsLifecycleResult(cmd, "failed", errors.New("storage generation is no longer current; refresh storage and retry")), true, nil
		}
		// Preflight every configured host before the first alias is removed. This
		// is deliberately separate from execution because zpool destruction is
		// never a lifecycle action.
		for _, hostID := range hosts {
			if _, err := zfsLifecycleDispatch(ctx, s, membership, hostID, "preflight", zfsLifecycleCommandForHost(cmd, hostID)); err != nil {
				return zfsLifecycleResult(cmd, "failed", err), true, nil
			}
		}
		for _, hostID := range hosts {
			result, err = zfsLifecycleDispatch(ctx, s, membership, hostID, "execute", zfsLifecycleCommandForHost(cmd, hostID))
			result = zfsLifecycleDispatched(cmd, result, err)
			if persistErr := persistZFSLifecycleOutcome(s, cmd.OperationID, v.RepositoryID, hostID, result); persistErr != nil {
				return zfsLifecycleResult(cmd, "needs_attention", persistErr), true, persistErr
			}
			if err != nil || result.Status != "succeeded" {
				return result, true, err
			}
		}
		if err := markZFSLifecycleDetachReady(s, *v, cmd.OperationID); err != nil {
			return zfsLifecycleResult(cmd, "needs_attention", err), true, err
		}
		if err := removePoolZFSAggregate(s, v.RepositoryID); err != nil {
			return zfsLifecycleResult(cmd, "needs_attention", err), true, err
		}
		if err := completeZFSLifecycleDetach(s, v.RepositoryID, cmd.OperationID); err != nil {
			return zfsLifecycleResult(cmd, "needs_attention", err), true, err
		}
		return zfsLifecycleResult(cmd, "succeeded", nil), true, nil
	}
	return zfsLifecycleResult(cmd, "failed", errors.New("invalid ZFS lifecycle action")), true, nil
}

func zfsLifecycleResult(cmd protocol.Command, status string, err error) protocol.Result {
	r := protocol.Result{OperationID: cmd.OperationID, Status: status, ObservedAt: time.Now().UTC()}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// zfsLifecycleDispatched keeps the real dispatch failure ("configured ZFS host
// is unreachable", a member dependency) instead of the bare Result a transport
// failure returns, so the operator sees the cause and the journal never records
// an outcome under an empty operation ID.
func zfsLifecycleDispatched(cmd protocol.Command, result protocol.Result, err error) protocol.Result {
	if result.OperationID != "" {
		return result
	}
	if err == nil {
		err = errors.New("configured ZFS host returned no lifecycle result")
	}
	return zfsLifecycleResult(cmd, "needs_attention", err)
}

func zfsLifecycleOperation(s *Store, repositoryID string) (zfsPoolOperation, []string, bool, error) {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	state, err := loadPoolZFS(s)
	if err != nil {
		return zfsPoolOperation{}, nil, false, err
	}
	var matched zfsPoolOperation
	found := false
	for _, op := range state.Operations {
		if op.RepositoryID != repositoryID {
			continue
		}
		if !found {
			matched = zfsPoolOperation{Request: op.Request, RepositoryID: op.RepositoryID, Outcomes: map[string]protocol.ZFSProvisionHostResult{}}
			found = true
		}
		for _, host := range op.Request.Hosts {
			if !containsZFSHostRequest(matched.Request.Hosts, host.HostID) {
				matched.Request.Hosts = append(matched.Request.Hosts, host)
			}
		}
		for host, outcome := range op.Outcomes {
			matched.Outcomes[host] = outcome
		}
	}
	if !found {
		return zfsPoolOperation{}, nil, false, nil
	}
	hosts := make([]string, 0, len(matched.Request.Hosts))
	for _, host := range matched.Request.Hosts {
		hosts = append(hosts, host.HostID)
	}
	sort.Strings(hosts)
	return matched, hosts, true, nil
}

func containsZFSHostRequest(hosts []protocol.ZFSProvisionHost, hostID string) bool {
	for _, host := range hosts {
		if host.HostID == hostID {
			return true
		}
	}
	return false
}

func zfsLifecycleCurrent(ctx context.Context, s *Store, op zfsPoolOperation, hosts []string) bool {
	if len(hosts) == 0 || len(op.Outcomes) != len(hosts) {
		return false
	}
	managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	for _, hostID := range hosts {
		outcome, ok := op.Outcomes[hostID]
		if !ok || outcome.RepositoryID != op.RepositoryID {
			return false
		}
		if outcome.Status == "succeeded" {
			continue
		}
		// An earlier development build incorrectly replaced this provision result with a
		// lifecycle failure.  Restore it only when the saved native GUIDs match
		// an authenticated current Core registration on that exact host.
		observed := observePoolZFSAccess(ctx, s, hostID, op.RepositoryID, managed.Storage)
		if outcome.PoolGUID == "" || outcome.DatasetGUID == "" || observed.PoolGUID != outcome.PoolGUID || observed.DatasetGUID != outcome.DatasetGUID || observed.Path == "" || observed.Error != "" || !repairPoisonedZFSProvisionOutcome(s, op.RepositoryID, hostID, outcome) {
			return false
		}
	}
	return true
}

func repairPoisonedZFSProvisionOutcome(s *Store, repositoryID, hostID string, expected protocol.ZFSProvisionHostResult) bool {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	state, err := loadPoolZFS(s)
	if err != nil {
		return false
	}
	updated := false
	for operationID, operation := range state.Operations {
		if operation.RepositoryID != repositoryID {
			continue
		}
		outcome, ok := operation.Outcomes[hostID]
		if !ok || outcome.Status == "succeeded" || outcome.PoolGUID != expected.PoolGUID || outcome.DatasetGUID != expected.DatasetGUID {
			continue
		}
		outcome.Status, outcome.Error = "succeeded", ""
		operation.Outcomes[hostID] = outcome
		state.Operations[operationID] = operation
		updated = true
	}
	return updated && savePoolZFS(s, state) == nil
}

func containsZFSHost(hosts []string, hostID string) bool {
	i := sort.SearchStrings(hosts, hostID)
	return i < len(hosts) && hosts[i] == hostID
}

func zfsRenameOnly(op zfsPoolOperation, v protocol.StorageLifecycleRequest) bool {
	if v.Registration == nil || v.Registration.StorageKind() != protocol.StorageKindZFS || v.Registration.Name == "" {
		return false
	}
	r := *v.Registration
	// The Controller's generic editor sends name/kind/source. Core owns these
	// flags, so omitted flags preserve the durable provisioned record; an
	// explicit conflicting flag is refused.
	want := vmhost.ZFSProvisionStorage(v.RepositoryID, v.PoolID, v.HostID, r.Name)
	if r.ID != "" && r.ID != want.ID || r.PoolID != "" && r.PoolID != want.PoolID || r.HostID != "" && r.HostID != want.HostID || r.Path != want.Path || !reflect.DeepEqual(r.Source, want.Source) {
		return false
	}
	return (!r.ZFSProvisioned || r.ZFSProvisioned == want.ZFSProvisioned) && (r.ZFSScope == "" || r.ZFSScope == op.Request.Scope)
}

func zfsLifecycleStateFile(s *Store) string { return filepath.Join(s.dir, "zfs-lifecycle.json") }

func loadZFSLifecycleState(s *Store) (zfsLifecycleState, error) {
	state := zfsLifecycleState{Generations: map[string]uint64{}, Operations: map[string]zfsLifecycleOperationCompletion{}}
	b, err := os.ReadFile(zfsLifecycleStateFile(s))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil || json.Unmarshal(b, &state) != nil || state.Generations == nil {
		return state, errors.New("invalid ZFS lifecycle journal")
	}
	if state.Operations == nil {
		state.Operations = map[string]zfsLifecycleOperationCompletion{}
	}
	// An earlier development build persisted a transport failure under the empty
	// operation ID of a bare Result. That entry belongs to no operation and would
	// otherwise fence every later repository out of the journal; drop it.
	delete(state.Operations, "")
	return state, nil
}

func zfsLifecycleGeneration(s *Store, repositoryID string) uint64 {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	state, err := loadZFSLifecycleState(s)
	if err != nil || state.Generations[repositoryID] == 0 {
		return 1
	}
	return state.Generations[repositoryID]
}

func renamePoolZFS(s *Store, repositoryID string, expected uint64, name string) error {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	lifecycle, err := loadZFSLifecycleState(s)
	if err != nil {
		return err
	}
	generation := lifecycle.Generations[repositoryID]
	if generation == 0 {
		generation = 1
	}
	state, err := loadPoolZFS(s)
	if err != nil {
		return err
	}
	if generation != expected {
		// A crash after the two durable rename writes but before the agent
		// command completion is recorded must replay idempotently. A later,
		// different rename still fails closed on its stale generation.
		if generation == expected+1 && zfsPoolNameIs(state, repositoryID, name) {
			return nil
		}
		return errors.New("storage generation is no longer current; refresh storage and retry")
	}
	updated := false
	for operationID, op := range state.Operations {
		if op.RepositoryID != repositoryID {
			continue
		}
		op.Request.Name = name
		for hostID, outcome := range op.Outcomes {
			outcome.Name = name
			op.Outcomes[hostID] = outcome
		}
		state.Operations[operationID] = op
		updated = true
	}
	if !updated {
		return errors.New("ZFS repository master record disappeared")
	}
	if err := savePoolZFS(s, state); err != nil {
		return err
	}
	lifecycle.Generations[repositoryID] = generation + 1
	b, err := json.Marshal(lifecycle)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(zfsLifecycleStateFile(s), b, 0600)
}

func zfsPoolNameIs(state zfsPoolState, repositoryID, name string) bool {
	found := false
	for _, operation := range state.Operations {
		if operation.RepositoryID != repositoryID {
			continue
		}
		found = true
		if operation.Request.Name != name {
			return false
		}
	}
	return found
}

// persistZFSLifecycleOutcome keys the receipt on the lifecycle command, never on
// the reported Result: a transport failure carries no operation ID of its own.
func persistZFSLifecycleOutcome(s *Store, operationID, repositoryID, hostID string, result protocol.Result) error {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	if operationID == "" {
		return errors.New("ZFS lifecycle outcome has no operation ID")
	}
	state, err := loadZFSLifecycleState(s)
	if err != nil {
		return err
	}
	op := state.Operations[operationID]
	if op.RepositoryID != "" && op.RepositoryID != repositoryID {
		return errors.New("ZFS lifecycle operation ID belongs to another repository")
	}
	op.RepositoryID = repositoryID
	if op.Outcomes == nil {
		op.Outcomes = map[string]protocol.Result{}
	}
	op.Outcomes[hostID] = result
	state.Operations[operationID] = op
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(zfsLifecycleStateFile(s), b, 0600)
}

func markZFSLifecycleDetachReady(s *Store, request protocol.StorageLifecycleRequest, operationID string) error {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	state, err := loadZFSLifecycleState(s)
	if err != nil {
		return err
	}
	op, ok := state.Operations[operationID]
	if !ok || op.RepositoryID != request.RepositoryID || len(op.Outcomes) == 0 {
		return errors.New("ZFS detach has no durable member completion receipts")
	}
	for _, outcome := range op.Outcomes {
		if outcome.Status != "succeeded" {
			return errors.New("ZFS detach has an incomplete member result")
		}
	}
	op.DetachReady = true
	op.PoolID, op.HostID, op.Generation = request.PoolID, request.HostID, request.ExpectedGeneration+1
	state.Operations[operationID] = op
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(zfsLifecycleStateFile(s), b, 0600)
}

func zfsLifecycleDetachReady(s *Store, repositoryID, operationID string) bool {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	state, err := loadZFSLifecycleState(s)
	if err != nil {
		return false
	}
	op := state.Operations[operationID]
	return op.RepositoryID == repositoryID && op.DetachReady
}

func completeZFSLifecycleDetach(s *Store, repositoryID, operationID string) error {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	state, err := loadZFSLifecycleState(s)
	if err != nil {
		return err
	}
	op, ok := state.Operations[operationID]
	if !ok || op.RepositoryID != repositoryID || !op.DetachReady {
		return errors.New("ZFS detach completion is not durably proven")
	}
	op.Completed = true
	state.Operations[operationID] = op
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(zfsLifecycleStateFile(s), b, 0600)
}

func removePoolZFS(s *Store, repositoryID string) error {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	state, err := loadPoolZFS(s)
	if err != nil {
		return err
	}
	removed := false
	for operationID, op := range state.Operations {
		if op.RepositoryID == repositoryID {
			delete(state.Operations, operationID)
			removed = true
		}
	}
	if !removed {
		return errors.New("ZFS repository master record disappeared")
	}
	if err := savePoolZFS(s, state); err != nil {
		return err
	}
	lifecycle, err := loadZFSLifecycleState(s)
	if err != nil {
		return err
	}
	delete(lifecycle.Generations, repositoryID)
	b, err := json.Marshal(lifecycle)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(zfsLifecycleStateFile(s), b, 0600)
}

func dispatchZFSLifecycle(ctx context.Context, s *Store, membership protocol.PoolMembership, hostID, phase string, cmd protocol.Command) (protocol.Result, error) {
	// The aggregate command is addressed to the master. Each member must receive
	// an exact local target, otherwise its authenticated handler correctly
	// rejects the master HostID as a cross-host lifecycle request.
	memberCommand := zfsLifecycleCommandForHost(cmd, hostID)
	if hostID == s.State.HostID {
		return executeZFSLifecycleLocal(ctx, s, phase, memberCommand)
	}
	var member protocol.PoolMember
	for _, candidate := range membership.Members {
		if candidate.HostID == hostID {
			member = candidate
			break
		}
	}
	if member.HostID == "" || member.Status != "ready" || member.Generation != membership.Generation || member.PeerOrigin == "" {
		return protocol.Result{}, errors.New("configured ZFS host is unreachable")
	}
	client, err := PoolMemberPeerClient(s, member.PeerOrigin, hostID)
	if err != nil {
		return protocol.Result{}, errors.New("configured ZFS host is unreachable")
	}
	defer client.CloseIdleConnections()
	body, _ := json.Marshal(zfsLifecyclePeerRequest{Phase: phase, Command: memberCommand})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolZFSLifecyclePath, bytes.NewReader(body))
	if err != nil {
		return protocol.Result{}, errors.New("configured ZFS host is unreachable")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return protocol.Result{}, errors.New("configured ZFS host is unreachable")
	}
	defer resp.Body.Close()
	var result protocol.Result
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, poolZFSLimit)).Decode(&result) != nil || result.OperationID != memberCommand.OperationID {
		return protocol.Result{}, errors.New("configured ZFS host refused lifecycle request")
	}
	if result.Status != "succeeded" {
		if result.Error == "" {
			result.Error = "configured ZFS host rejected lifecycle request"
		}
		return result, errors.New(result.Error)
	}
	return result, nil
}

func zfsLifecycleCommandForHost(cmd protocol.Command, hostID string) protocol.Command {
	memberCommand := cmd
	if cmd.StorageLifecycle != nil {
		lifecycle := *cmd.StorageLifecycle
		lifecycle.HostID = hostID
		memberCommand.StorageLifecycle = &lifecycle
	}
	return memberCommand
}

func executeZFSLifecycleLocal(ctx context.Context, s *Store, phase string, cmd protocol.Command) (protocol.Result, error) {
	v := cmd.StorageLifecycle
	if v == nil || v.HostID != s.State.HostID || v.PoolID != s.State.PoolID {
		return protocol.Result{}, errors.New("ZFS lifecycle host identity does not match")
	}
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	state, _ := engine.Applied(ctx)
	// A pool-wide rename advances the master's aggregate generation, but must
	// not invalidate the member's native registration generation.  Keep the
	// member fence for both a live registration and its detach tombstone: the
	// latter is the durable proof needed when a successful detach reply was
	// lost before the master removed the aggregate record.
	tombstoned := false
	for _, registration := range state.Storage {
		if registration.ID != v.RepositoryID || registration.Generation == 0 {
			continue
		}
		localGeneration := registration.Generation
		if registration.Tombstone {
			tombstoned = true
			if localGeneration == 1 {
				continue
			}
			localGeneration--
		}
		local := *v
		local.ExpectedGeneration = localGeneration
		cmd.StorageLifecycle = &local
		v = cmd.StorageLifecycle
		break
	}
	if !containsZFSRegistration(state.Storage, v.RepositoryID) {
		// A successful local detach removes its own registration. If the reply
		// was lost before the master removed its aggregate record, replay the
		// exact durable managed command so its receipt, rather than absence alone,
		// proves completion.
		if (phase == "preflight" || phase == "execute") && v.Action == "detach" {
			// The host's own durable tombstone proves it unregistered this
			// repository, including for a retry under a new operation ID, which
			// has no receipt to replay. Absence alone still proves nothing.
			if tombstoned {
				return zfsLifecycleResult(cmd, "succeeded", nil), nil
			}
			result, err := engine.Execute(ctx, cmd, nil)
			if err == nil && result.Status == "succeeded" {
				return result, nil
			}
		}
		return protocol.Result{}, errors.New("configured ZFS host has no repository registration")
	}
	if phase == "preflight" {
		if v.Action != "detach" {
			return protocol.Result{}, errors.New("invalid ZFS lifecycle preflight")
		}
		if err := zfsLifecycleDependencies(state, v.RepositoryID); err != nil {
			return protocol.Result{}, err
		}
		return zfsLifecycleResult(cmd, "succeeded", nil), nil
	}
	if phase != "execute" {
		return protocol.Result{}, errors.New("invalid ZFS lifecycle phase")
	}
	if v.Action == "detach" {
		if err := zfsLifecycleDependencies(state, v.RepositoryID); err != nil {
			return zfsLifecycleResult(cmd, "failed", err), nil
		}
	}
	result, err := engine.Execute(ctx, cmd, nil)
	return result, err
}

func containsZFSRegistration(storage []protocol.StorageRegistration, repositoryID string) bool {
	for _, registration := range storage {
		if registration.ID == repositoryID && registration.StorageKind() == protocol.StorageKindZFS && !registration.Tombstone {
			return true
		}
	}
	return false
}

func zfsLifecycleDependencies(state *protocol.ManagedState, repositoryID string) error {
	for _, vm := range state.VMs {
		for _, disk := range vm.Disks {
			if disk.StorageID == repositoryID {
				return errors.New("storage cannot detach while used by VM " + vm.ID)
			}
		}
	}
	for _, media := range state.Media {
		if media.StorageID == repositoryID {
			return errors.New("storage cannot detach while used by ISO " + media.ID)
		}
	}
	return nil
}

// PoolZFSLifecycleHandler is the member-only endpoint. Add its exact path to
// PoolPeerHandler before poolZFSPath's provision route.
func PoolZFSLifecycleHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := AuthenticatePoolMaster(s, r); err != nil {
			http.Error(w, "ZFS peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request zfsLifecyclePeerRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, poolZFSLimit))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Command.StorageLifecycle == nil || protocol.ValidateStorageLifecycle(*request.Command.StorageLifecycle) != nil || request.Command.StorageLifecycle.PoolID != s.State.PoolID || request.Command.StorageLifecycle.HostID != s.State.HostID || (request.Phase != "preflight" && request.Phase != "execute") {
			http.Error(w, "invalid ZFS lifecycle", http.StatusBadRequest)
			return
		}
		result, err := executeZFSLifecycleLocal(r.Context(), s, request.Phase, request.Command)
		if err != nil {
			result = zfsLifecycleResult(request.Command, "failed", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
