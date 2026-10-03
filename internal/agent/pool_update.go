// SPDX-License-Identifier: AGPL-3.0-only
package agent

// The pool rollout coordinator. A rollout is one durable job on the pool master
// (pool-update.json beside pool-peer.json, replicated as the authority section update_rollout):
// stage the release everywhere, then update one host at a time, members first and the master
// last. The Controller only asks (the pool-update command) and reads the record back from
// inventory; the coordinator needs neither it nor a browser to finish. Each pass of Advance
// moves the record at most one step and persists before it acts, so a coordinator that crashes
// or a master that reboots resumes from the file. Hosts' own progress lives in their
// state.json, and every step is idempotent on its StepID.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolUpdateStateFile = "pool-update.json"

// Rollout step names, in the order a host passes through them.
const (
	stagePending    = "pending"
	stageStaged     = "staged"
	stageTeardown   = "teardown"
	stageDrain      = "drain"
	stageActivate   = "activate"
	stageBoot       = "boot"
	stageConfirm    = "confirm"
	stageRelease    = "release"
	stageRestart    = "restart"
	stageSoak       = "soak"
	stageDone       = "done"
	stageRolledBack = "rolled_back"
)

// PoolUpdateError is a refusal with a stable code and one literal message.
type PoolUpdateError struct{ Code, Message string }

func (e *PoolUpdateError) Error() string { return e.Code + ": " + e.Message }

func updateRefusal(code, format string, args ...any) *PoolUpdateError {
	return &PoolUpdateError{Code: code, Message: fmt.Sprintf(format, args...)}
}

var (
	// poolUpdateMu serializes the coordinator and its writes to pool-update.json.
	poolUpdateMu sync.Mutex

	// There is no HA setting yet (HA is planned and unavailable); the seam is where its
	// maintenance capability will be read, and a rollout refuses while it is true.
	poolUpdateHAEnabled = func(*Store) bool { return false }
	// The Controller's api_epoch arrives with ReportResponse.controller (controller-epoch-v1),
	// which does not exist yet: until it does the epoch is unknown.
	poolUpdateControllerEpoch = func(*Store) (int, bool) { return 0, false }

	pushPoolAuthorityReplicasAfterUpdate = PushPoolAuthorityReplicas

	poolUpdateStepTimeout  = 6 * time.Hour
	poolUpdateBootDeadline = 30 * time.Minute
	poolUpdateMinSoak      = 60 * time.Second
	poolUpdateMaintenance  = poolUpdateSetMaintenance
	poolUpdateNow          = func() time.Time { return time.Now().UTC() }
	poolUpdatePeerFactory  = func(s *Store) poolUpdatePeers { return livePoolUpdatePeers{s} }
	// Admission projection seam lets tests overlap competing authority writes.
	poolUpdateAuthorityProjection = buildPoolAuthorityReplicaWithVMs
	poolUpdateTickInterval        = 5 * time.Second
	poolUpdateReplicaPushTO       = 10 * time.Second
	// poolUpdateRetryPause is how long a guest step that the host reported as failed waits before
	// the coordinator asks again under a new step identity.
	poolUpdateRetryPause = 15 * time.Second
	// poolUpdateRetryWindow bounds how long a guest step that fails for a reason the host may get
	// over by itself (libvirt not up after a reboot, a mount not there yet) is asked again before
	// the rollout pauses needs_attention with the host's words.
	poolUpdateRetryWindow = 5 * time.Minute
	// poolUpdateAbortWindow bounds the coordinator's own retries of an abort's cleanup on one host
	// (a host that is still rebooting is the usual reason); afterwards only an operator's abort
	// tries again.
	poolUpdateAbortWindow = 30 * time.Minute
	// poolUpdatePassTimeout bounds one pass of Advance, which holds the lock a pause or an abort
	// waits for, so a pool of unreachable hosts cannot hold it for long.
	poolUpdatePassTimeout = 3 * time.Minute
)

// poolUpdatePeers is how the coordinator reaches a host, itself included.
type poolUpdatePeers interface {
	Status(ctx context.Context, member protocol.PoolMember) (protocol.PoolUpdateHostStatus, error)
	Step(ctx context.Context, member protocol.PoolMember, step protocol.PoolUpdateStep) (protocol.PoolUpdateStepResult, error)
}

func poolUpdatePath(s *Store) string { return filepath.Join(s.dir, poolUpdateStateFile) }

// LoadPoolUpdate returns the master's rollout record, or nil when no rollout was ever started.
func LoadPoolUpdate(s *Store) (*protocol.PoolUpdateRollout, error) {
	b, err := os.ReadFile(poolUpdatePath(s))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r protocol.PoolUpdateRollout
	if err = json.Unmarshal(b, &r); err != nil || r.Valid() != nil {
		return nil, errors.New("pool update record is invalid")
	}
	return &r, nil
}

func savePoolUpdate(s *Store, r *protocol.PoolUpdateRollout) error {
	if err := r.Valid(); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(poolUpdatePath(s), b, 0600)
}

// poolUpdateActive reports whether a rollout still owns the pool.
func poolUpdateActive(r *protocol.PoolUpdateRollout) bool {
	return r != nil && (r.State == protocol.PoolUpdateRunning || r.State == protocol.PoolUpdatePaused)
}

type poolUpdateCoordinator struct {
	s     *Store
	peers poolUpdatePeers
	// replicatedFor names the guest list the pool's members last acknowledged; it is only an
	// optimisation (a fresh coordinator replicates once more), never a record.
	replicatedFor string
}

// NewPoolUpdateCoordinator is the coordinator of this host's store; it does work only while the
// host is the pool master.
func NewPoolUpdateCoordinator(s *Store) *poolUpdateCoordinator {
	return &poolUpdateCoordinator{s: s, peers: poolUpdatePeerFactory(s)}
}

// RunPoolUpdateCoordinator advances the rollout until ctx ends. It is cheap while none exists.
func RunPoolUpdateCoordinator(ctx context.Context, s *Store) {
	// NEX_POOL_UPDATE_STEP_TIMEOUT shortens the six-hour step timeout (a Go duration, one second or
	// more) so a lab can watch a refuse-policy pause; the unit's environment is root's to set.
	if v := os.Getenv("NEX_POOL_UPDATE_STEP_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
			poolUpdateStepTimeout = d
		}
	}
	c := NewPoolUpdateCoordinator(s)
	ticker := time.NewTicker(poolUpdateTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.Advance(ctx)
		}
	}
}

// masterMembership is the master's own writable membership, or a refusal.
func (c *poolUpdateCoordinator) masterMembership() (protocol.PoolMembership, error) {
	membership, err := Membership(c.s)
	if err != nil {
		return membership, updateRefusal("pool_unavailable", "pool membership is unavailable")
	}
	if membership.MasterHostID != c.s.State.HostID {
		return membership, updateRefusal("not_pool_master", "only the pool master runs a rollout")
	}
	if err = requirePoolAuthorityWritable(c.s); err != nil {
		return membership, updateRefusal("authority_unavailable", "%s", err.Error())
	}
	return membership, nil
}

// PoolUpdatePlan is a successful preflight: the order the hosts would be updated in, and the
// hosts that already run and have committed the release, which the rollout leaves alone.
type PoolUpdatePlan struct {
	Sequence uint64   `json:"sequence"`
	Order    []string `json:"order"`
	Current  []string `json:"current,omitempty"`
}

// Start validates the whole pool, then (unless dryRun) records the rollout. The rollout ID is the
// request ID: an exact replay returns the rollout it already made, a different request while one
// is active is refused.
func (c *poolUpdateCoordinator) Start(ctx context.Context, requestID string, req protocol.PoolUpdateRequest) (*protocol.PoolUpdateRollout, *PoolUpdatePlan, error) {
	poolUpdateMu.Lock()
	defer poolUpdateMu.Unlock()
	if requestID == "" || len(requestID) > 128 {
		return nil, nil, updateRefusal("invalid_request", "a rollout needs a request_id")
	}
	if err := req.Valid("update-rollout"); err != nil {
		return nil, nil, updateRefusal("invalid_request", "%s", err.Error())
	}
	existing, err := LoadPoolUpdate(c.s)
	if err != nil {
		return nil, nil, updateRefusal("pool_unavailable", "%s", err.Error())
	}
	if existing != nil && existing.ID == requestID && !req.DryRun {
		if existing.Sequence != req.Sequence || poolUpdateVMsPolicy(protocol.PoolUpdateRequest{RunningVMs: existing.RunningVMs}) != poolUpdateVMsPolicy(req) {
			return nil, nil, updateRefusal("request_conflict", "this request_id started a different rollout")
		}
		return existing, nil, nil
	}
	if poolUpdatePendingControl(existing) != nil {
		return nil, nil, updateRefusal("control_pending", "retry the pending rollout control before starting another rollout")
	}
	membership, err := c.masterMembership()
	if err != nil {
		return nil, nil, err
	}
	plan, err := c.preflight(ctx, membership, existing, req)
	if err != nil || req.DryRun {
		return nil, plan, err
	}
	now := poolUpdateNow()
	r := &protocol.PoolUpdateRollout{ID: requestID, PoolID: membership.PoolID, Sequence: req.Sequence,
		RunningVMs: poolUpdateVMsPolicy(req), State: protocol.PoolUpdateRunning, Phase: "staging",
		MembershipGeneration: membership.Generation, CreatedAt: now, UpdatedAt: now}
	if existing != nil {
		r.Controls = existing.Controls
	}
	// A host that already runs the release (a member that joined on it, a host an aborted rollout
	// had already updated) has nothing left to do; it stays in the record so finalize reads it.
	for _, id := range plan.Current {
		r.Hosts = append(r.Hosts, protocol.PoolUpdateHost{HostID: id, Stage: stageDone})
	}
	for _, id := range plan.Order {
		r.Hosts = append(r.Hosts, protocol.PoolUpdateHost{HostID: id, Stage: stagePending})
	}
	if err = savePoolUpdateAdmitted(c.s, r); err != nil {
		return nil, nil, err
	}
	c.pushReplica(ctx)
	return r, plan, nil
}

func poolUpdateVMsPolicy(req protocol.PoolUpdateRequest) string {
	if req.RunningVMs == "" {
		return "refuse"
	}
	return req.RunningVMs
}

// preflight is every check that can refuse a rollout before anything changes. It asks each host
// to verify the release (a read of signed metadata) and records nothing.
func (c *poolUpdateCoordinator) preflight(ctx context.Context, membership protocol.PoolMembership, existing *protocol.PoolUpdateRollout, req protocol.PoolUpdateRequest) (*PoolUpdatePlan, error) {
	if poolUpdateActive(existing) {
		state := existing.State
		if poolUpdateAborting(existing) {
			state = "being aborted (the abort has not yet put everything back)"
		}
		return nil, updateRefusal("rollout_in_progress", "rollout %s is still %s; finish or abort it first", existing.ID, state)
	}
	if poolUpdateHAEnabled(c.s) {
		return nil, updateRefusal(protocol.PoolUpdateHAEnabled, "HA is enabled and has no maintenance capability yet; turn HA off before updating the pool")
	}
	policy := poolUpdateVMsPolicy(req)
	if policy == "evacuate" {
		return nil, updateRefusal("evacuation_unavailable", "live migration and evacuation do not exist yet; use refuse or shutdown")
	}
	type view struct {
		member  protocol.PoolMember
		status  protocol.PoolUpdateHostStatus
		current bool // already runs and has committed the release
	}
	var views []view
	var master protocol.PoolUpdateHostStatus
	for _, member := range membership.Members {
		if member.Status == "ejected" {
			continue
		}
		if member.Status != "ready" {
			return nil, updateRefusal("member_not_ready", "host %s is %s; every member must be ready before a rollout", member.HostID, member.Status)
		}
		if member.Maintenance {
			return nil, updateRefusal(protocol.PoolUpdateHostBusy, "host %s is in maintenance mode; take it out before a rollout", member.HostID)
		}
		check, err := c.peers.Step(ctx, member, protocol.PoolUpdateStep{RolloutID: "preflight", StepID: poolUpdateStepID("preflight", member.HostID, "check", 0), Action: "check", Sequence: req.Sequence})
		if errors.Is(err, errPoolUpdateUnsupported) {
			return nil, updateRefusal("upgrade_required", "host %s does not support %s; upgrade it first", member.HostID, protocol.PoolUpdateRolloutCapability)
		}
		if err != nil {
			return nil, updateRefusal("host_unreachable", "host %s cannot be reached for the update check", member.HostID)
		}
		if check.Code != "" {
			return nil, updateRefusal(check.Code, "host %s: %s", member.HostID, check.Message)
		}
		status, err := c.peers.Status(ctx, member)
		if errors.Is(err, errPoolUpdateUnsupported) {
			return nil, updateRefusal("upgrade_required", "host %s does not support %s; upgrade it first", member.HostID, protocol.PoolUpdateRolloutCapability)
		}
		if err != nil {
			return nil, updateRefusal("host_unreachable", "host %s did not report its update status", member.HostID)
		}
		views = append(views, view{member: member, status: status})
		if member.HostID == membership.MasterHostID {
			master = status
		}
	}
	if len(views) == 0 {
		return nil, updateRefusal("pool_unavailable", "the pool has no host to update")
	}
	epoch, known := poolUpdateControllerEpoch(c.s)
	for i := range views {
		v := &views[i]
		st := v.status
		switch {
		case !slices.Contains(st.Capabilities, protocol.PoolUpdateRolloutCapability):
			return nil, updateRefusal("upgrade_required", "host %s does not advertise %s; upgrade it first", v.member.HostID, protocol.PoolUpdateRolloutCapability)
		case st.Channel != master.Channel:
			return nil, updateRefusal("channel_mismatch", "host %s follows channel %q, the master follows %q", v.member.HostID, st.Channel, master.Channel)
		case st.Busy:
			return nil, updateRefusal(protocol.PoolUpdateHostBusy, "host %s is running another update operation", v.member.HostID)
		case st.RunningSequence == req.Sequence && poolUpdateUnconfirmed(st.Phase):
			return nil, updateRefusal(protocol.PoolUpdateUnconfirmedHost, "host %s runs release %d but has not committed it; let that update finish first", v.member.HostID, req.Sequence)
		case st.RunningSequence == req.Sequence:
			// The engine offers no release a host already runs, so there is nothing to check.
			v.current = true
			continue
		case st.Target == nil || st.Target.Sequence != req.Sequence:
			return nil, updateRefusal("release_unavailable", "host %s does not offer release %d", v.member.HostID, req.Sequence)
		case st.Target.StateFormat < st.StateFormat:
			return nil, updateRefusal("state_format_downgrade", "release %d writes state format %d; host %s already runs format %d", req.Sequence, st.Target.StateFormat, v.member.HostID, st.StateFormat)
		case st.FreeBytes < st.Target.RequiredBytes:
			return nil, updateRefusal("insufficient_space", "host %s has %d bytes free; the release needs %d", v.member.HostID, st.FreeBytes, st.Target.RequiredBytes)
		case !st.Target.MixedPool && policy != "shutdown":
			return nil, updateRefusal("mixed_pool_requires_shutdown", "release %d cannot run mixed with the current version; send running_vms shutdown to accept guest downtime", req.Sequence)
		case st.Target.MinControllerAPIEpoch > 0 && !known:
			return nil, updateRefusal("controller_unknown", "the Controller's api_epoch is unknown; release %d needs at least %d", req.Sequence, st.Target.MinControllerAPIEpoch)
		case st.Target.MinControllerAPIEpoch > 0 && epoch < st.Target.MinControllerAPIEpoch:
			return nil, updateRefusal("controller_too_old", "the Controller's api_epoch is %d; release %d needs at least %d", epoch, req.Sequence, st.Target.MinControllerAPIEpoch)
		}
	}
	// Members first, fewest running guests first, host ID as the tie break; the master last.
	order := make([]view, 0, len(views))
	var last *view
	for i := range views {
		if views[i].current {
			continue
		}
		if views[i].member.HostID == membership.MasterHostID {
			last = &views[i]
			continue
		}
		order = append(order, views[i])
	}
	sort.SliceStable(order, func(i, j int) bool {
		if a, b := len(order[i].status.RunningVMs), len(order[j].status.RunningVMs); a != b {
			return a < b
		}
		return order[i].member.HostID < order[j].member.HostID
	})
	if last != nil {
		order = append(order, *last)
	}
	plan := &PoolUpdatePlan{Sequence: req.Sequence}
	for _, v := range order {
		plan.Order = append(plan.Order, v.member.HostID)
	}
	for _, v := range views {
		if v.current {
			plan.Current = append(plan.Current, v.member.HostID)
		}
	}
	return plan, nil
}

// Control durably binds a request identity before applying pause, resume, abort or rollback_host.
// Its result is retained across rollouts and promotion, and returned before inspecting live state.
func (c *poolUpdateCoordinator) Control(ctx context.Context, requestID string, req protocol.PoolUpdateRequest) (*protocol.PoolUpdateRollout, error) {
	poolUpdateMu.Lock()
	defer poolUpdateMu.Unlock()
	if requestID == "" || len(requestID) > 128 {
		return nil, updateRefusal("invalid_request", "a rollout control needs a request_id of at most 128 characters")
	}
	if err := req.Valid("update-control"); err != nil {
		return nil, updateRefusal("invalid_request", "%s", err.Error())
	}
	req = poolUpdateControlIntent(req)
	membership, err := c.masterMembership()
	if err != nil {
		return nil, err
	}
	r, err := LoadPoolUpdate(c.s)
	if err != nil {
		return nil, updateRefusal("pool_unavailable", "%s", err.Error())
	}
	var receipt *protocol.PoolUpdateControlResult
	if r != nil {
		for i := range r.Controls {
			if r.Controls[i].RequestID == requestID {
				receipt = &r.Controls[i]
				break
			}
		}
	}
	if receipt != nil {
		if receipt.Request != req {
			return nil, updateRefusal("request_conflict", "this request_id names a different rollout control")
		}
	} else {
		if r == nil || r.ID != req.RolloutID {
			return nil, updateRefusal("rollout_not_found", "no such rollout")
		}
		if poolUpdatePendingControl(r) != nil {
			return nil, updateRefusal("control_pending", "retry the pending rollout control before sending another control")
		}
		r.Controls = append(r.Controls, protocol.PoolUpdateControlResult{RequestID: requestID, Request: req})
		receipt = &r.Controls[len(r.Controls)-1]
		reserve, reserveErr := poolUpdateResultReservation(r, membership, req)
		if reserveErr != nil {
			return nil, reserveErr
		}
		receipt.ReservedResultBytes = reserve
		if err = savePoolUpdateAdmitted(c.s, r); err != nil {
			return nil, err
		}
		c.pushReplica(ctx)
	}
	return c.continueControl(ctx, r, membership, receipt)
}

func (c *poolUpdateCoordinator) continueControl(ctx context.Context, r *protocol.PoolUpdateRollout, membership protocol.PoolMembership, receipt *protocol.PoolUpdateControlResult) (*protocol.PoolUpdateRollout, error) {
	if receipt.Rollout == nil && receipt.Code == "" {
		result, controlErr := c.control(ctx, r, membership, receipt)
		if controlErr != nil {
			var refusal *PoolUpdateError
			if !errors.As(controlErr, &refusal) || refusal.Code == "replica_pending" || refusal.Code == "rollback_pending" {
				return nil, controlErr
			}
			receipt.Code, receipt.Message = resultError(refusal.Code), resultError(refusal.Message)
		} else {
			snapshot := *result
			snapshot.Controls = nil
			receipt.Rollout = &snapshot
		}
		receipt.ReservedResultBytes = 0
		receipt.RollbackStarted = false
		receipt.ReplicationPending = receipt.Request.Control == "abort" || receipt.Request.Control == "rollback_host"
		if err := savePoolUpdate(c.s, r); err != nil {
			return nil, updateRefusal("record_failed", "the rollout control result could not be recorded durably")
		}
		if !receipt.ReplicationPending {
			c.pushReplica(ctx)
		}
	}
	if receipt.ReplicationPending {
		if err := c.replicate(ctx); err != nil {
			return nil, updateRefusal("replica_pending", "Core is waiting for acknowledgement of this rollout control result")
		}
		receipt.ReplicationPending = false
		if err := savePoolUpdate(c.s, r); err != nil {
			return nil, updateRefusal("record_failed", "the rollout control acknowledgement could not be recorded durably")
		}
	}
	if receipt.Code != "" {
		return nil, updateRefusal(receipt.Code, "%s", receipt.Message)
	}
	return receipt.Rollout, nil
}

func poolUpdateControlIntent(req protocol.PoolUpdateRequest) protocol.PoolUpdateRequest {
	intent := protocol.PoolUpdateRequest{RolloutID: req.RolloutID, Control: req.Control, Force: req.Force}
	if req.Control == "rollback_host" {
		intent.HostID = req.HostID
	}
	return intent
}

func poolUpdatePendingControl(r *protocol.PoolUpdateRollout) *protocol.PoolUpdateControlResult {
	if r != nil {
		for i := range r.Controls {
			control := &r.Controls[i]
			if control.Rollout == nil && control.Code == "" || control.ReplicationPending {
				return control
			}
		}
	}
	return nil
}

// Reserve the full terminal snapshot and the possible growth of the live record. Error text
// uses the same 4096-byte bound as managed results, with worst-case JSON escaping.
func poolUpdateResultReservation(r *protocol.PoolUpdateRollout, membership protocol.PoolMembership, req protocol.PoolUpdateRequest) (int, error) {
	snapshot := *r
	snapshot.Controls = nil
	before, err := json.Marshal(snapshot)
	if err != nil {
		return 0, updateRefusal("record_failed", "the rollout result reservation could not be encoded")
	}
	snapshot.Hosts = append([]protocol.PoolUpdateHost(nil), r.Hosts...)
	maxTime := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	snapshot.UpdatedAt = maxTime
	maxAttempt := int(^uint(0) >> 1)
	var ids []string
	for i := range snapshot.Hosts {
		host := &snapshot.Hosts[i]
		ids = append(ids, host.HostID)
		host.StepStartedAt, host.DeadlineAt = maxTime, maxTime
		host.Attempt = maxAttempt
		switch req.Control {
		case "abort":
			member, ok := poolMemberByID(membership, host.HostID)
			if poolUpdateOwes(host, member, ok) {
				host.Error += strings.Join(host.StoppedByUpdate, ", ") + strings.Repeat("\x00", 4096) + strings.Repeat("x", 256)
			}
		case "rollback_host":
			if host.HostID == req.HostID {
				host.Stage = stageRolledBack
				host.Error += "rolled back by the operator"
			}
		}
	}
	snapshot.State = protocol.PoolUpdateFinalized
	snapshot.Pause = &protocol.PoolUpdatePause{Code: strings.Repeat("x", 64),
		Message: strings.Join(ids, ", ") + strings.Repeat("x", 512)}
	if r.Controller != nil {
		controller := *r.Controller
		controller.StartedAt, controller.Attempt = maxTime, maxAttempt
		snapshot.Controller = &controller
	}
	after, err := json.Marshal(snapshot)
	if err != nil {
		return 0, updateRefusal("record_failed", "the rollout result reservation could not be encoded")
	}
	if len(before) > protocol.PoolReplicaLimit || len(after) > protocol.PoolReplicaLimit {
		return 0, updateRefusal("authority_capacity", "the rollout control has no room for its durable terminal result")
	}
	reserved := len(after) + max(0, len(after)-len(before)) + 2*6*4096 + 512
	if reserved > protocol.PoolReplicaLimit {
		return 0, updateRefusal("authority_capacity", "the rollout control has no room for its durable terminal result")
	}
	return reserved, nil
}

// savePoolUpdateAdmitted publishes the reservation under the same VM authority lock
// used by snapshot admission and settlement. Callers already hold poolUpdateMu;
// release poolVMMu before any peer RPC or rollout effects.
func savePoolUpdateAdmitted(s *Store, r *protocol.PoolUpdateRollout) error {
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		return updateRefusal("pool_unavailable", "the pool VM authority cannot be read for update admission")
	}
	vmSection, err := poolVMRecoverySettlementProjection(state)
	if err != nil {
		return updateRefusal("authority_capacity", "the pool's accepted snapshot results cannot fit in the update authority")
	}
	if vmSection == nil {
		vmSection, err = json.Marshal(state.VMs)
		if err != nil {
			return updateRefusal("record_failed", "the pool VM authority could not be encoded")
		}
	}
	replica, _, err := poolUpdateAuthorityProjection(s, vmSection)
	if err != nil {
		return updateRefusal("pool_unavailable", "the pool authority cannot be read for update admission")
	}
	replica.UpdateRollout = r
	if replica.Sections, err = replica.SectionDigests(); err != nil {
		return updateRefusal("record_failed", "the rollout authority could not be encoded")
	}
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return updateRefusal("pool_unavailable", "the pool authority is unavailable for update admission")
	}
	if err = poolAuthorityReplicaFitsAddressedBodies(s, replica, peer.CAKeyPEM); err != nil {
		return updateRefusal("authority_capacity", "the rollout record and reserved terminal result exceed the pool authority limit")
	}
	if err = savePoolUpdate(s, r); err != nil {
		return updateRefusal("record_failed", "the rollout record could not be recorded durably")
	}
	return nil
}

func (c *poolUpdateCoordinator) control(ctx context.Context, r *protocol.PoolUpdateRollout, membership protocol.PoolMembership, receipt *protocol.PoolUpdateControlResult) (*protocol.PoolUpdateRollout, error) {
	var err error
	req := receipt.Request
	now := poolUpdateNow()
	switch req.Control {
	case "pause":
		if r.State != protocol.PoolUpdateRunning {
			return r, nil
		}
		r.PauseRequested = true
		if poolUpdateBetweenHosts(r) {
			poolUpdatePause(r, protocol.PoolUpdatePausedByOperator, "paused by the operator")
		}
	case "resume":
		if r.State == protocol.PoolUpdateRunning {
			return r, nil
		}
		if poolUpdateAborting(r) {
			return nil, updateRefusal(protocol.PoolUpdateAbortCleanupPending, "the rollout is being aborted and cannot resume; abort again to retry the cleanup, or abort with force")
		}
		if r.State != protocol.PoolUpdatePaused {
			return nil, updateRefusal("rollout_finished", "the rollout is %s", r.State)
		}
		if poolUpdateHAEnabled(c.s) {
			return nil, updateRefusal(protocol.PoolUpdateHAEnabled, "HA is enabled; turn it off before resuming")
		}
		for _, host := range r.Hosts {
			if member, ok := poolMemberByID(membership, host.HostID); !ok || member.Status != "ready" && host.Stage != stageBoot {
				return nil, updateRefusal(protocol.PoolUpdateMembershipChanged, "host %s is no longer a ready member; review the pool first", host.HostID)
			}
		}
		poolUpdateOrderHosts(r, membership.MasterHostID, membership.Members)
		// The gates a host passed before the pause are not remembered: the pool may have changed
		// while it waited, so the host that is next is gated again as at its first entry.
		if next := poolUpdateNextHost(r); next != nil && poolUpdateBeforeReboot(next.Stage) {
			if err = c.gate(ctx, membership, next.HostID); err != nil {
				return nil, err
			}
		}
		r.State, r.Pause, r.PauseRequested = protocol.PoolUpdateRunning, nil, false
		r.MembershipGeneration = membership.Generation
		for i := range r.Hosts {
			host := &r.Hosts[i]
			switch {
			case host.Stage == stageRolledBack:
				// The rollback gave up the staged slot and the engine remembers the earlier
				// attempt's request IDs, so the host is staged again under a new attempt.
				host.Stage, host.Error = stagePending, ""
				host.Attempt++
			case host.Error != "":
				// A step the host refused or failed is journaled by its engine under that step's
				// request ID and answered with the same failure on every repeat, so the retry the
				// operator asked for is a new attempt.
				host.Error = ""
				host.Attempt++
			}
			// A wait that ran out its deadline starts a fresh one: the operator looked.
			host.StepStartedAt = now
			if host.Stage == stageBoot {
				host.DeadlineAt = now.Add(poolUpdateBootDeadline)
			}
		}
		if ctl := r.Controller; ctl != nil && ctl.Stage == protocol.PoolUpdateControllerSwap {
			// A failed swap rolled the Controller back; resuming asks for a new swap, not a replay of the failed one.
			if ctl.Error != "" {
				ctl.Attempt++
			}
			ctl.Error, ctl.StartedAt = "", now
		}
	case "abort":
		if !poolUpdateActive(r) {
			return nil, updateRefusal("rollout_finished", "the rollout is %s", r.State)
		}
		switch {
		case req.Force && !poolUpdateAborting(r):
			return nil, updateRefusal("abort_not_pending", "force ends an abort that is still putting things back; abort the rollout first")
		case req.Force:
			// The operator takes over what is left: the hosts' errors and stopped_by_update stay in
			// the record as the list of what has to be done by hand.
			r.State, r.Pause, r.PauseRequested = protocol.PoolUpdateAborted, nil, false
		default:
			// An abort that cannot confirm its cleanup stays open (and is retried) instead of ending
			// with guests nobody owes a restart.
			if err = c.replicate(ctx); err != nil {
				return nil, updateRefusal("replica_pending", "retry this request_id after the rollout control record is acknowledged by the pool's members")
			}
			c.finishAbort(ctx, r, now, true)
		}
	case "rollback_host":
		if poolUpdateAborting(r) {
			return nil, updateRefusal(protocol.PoolUpdateAbortCleanupPending, "the rollout is being aborted; abort again to retry the cleanup, or abort with force")
		}
		if r.State != protocol.PoolUpdatePaused {
			return nil, updateRefusal("rollout_running", "pause the rollout before rolling a host back")
		}
		host := poolUpdateHostRecord(r, req.HostID)
		member, ok := poolMemberByID(membership, req.HostID)
		if host == nil || !ok {
			return nil, updateRefusal("host_not_found", "host %s is not part of this rollout", req.HostID)
		}
		if host.Stage != stageBoot && host.Stage != stageConfirm && host.Stage != stageRelease && host.Stage != stageRestart && host.Stage != stageSoak && host.Stage != stageDone {
			return nil, updateRefusal("host_not_activated", "host %s has not activated the release", req.HostID)
		}
		// The rollback arms the earlier slot and reboots the host: that is a host leaving service
		// like any other, so the same gates apply.
		if err = c.gate(ctx, membership, req.HostID); err != nil {
			if receipt.RollbackStarted {
				return nil, updateRefusal("rollback_pending", "Core is waiting to reconcile the rollback of host %s safely", req.HostID)
			}
			return nil, err
		}
		if !receipt.RollbackStarted {
			receipt.RollbackStarted = true
			if err = savePoolUpdate(c.s, r); err != nil {
				return nil, updateRefusal("record_failed", "the rollback intent could not be recorded durably")
			}
		}
		if err = c.replicate(ctx); err != nil {
			return nil, updateRefusal("replica_pending", "retry this request_id after the rollout control record is acknowledged by the pool's members")
		}
		res, stepErr := c.peers.Step(ctx, member, protocol.PoolUpdateStep{RolloutID: r.ID, StepID: poolUpdateStepID(r.ID, req.HostID, stageRolledBack, host.Attempt), Action: "rollback", Sequence: r.Sequence})
		if stepErr != nil {
			return nil, updateRefusal("rollback_pending", "Core is reconciling the rollback of host %s", req.HostID)
		}
		if res.Code != "" {
			return nil, updateRefusal("rollback_failed", "host %s: %s: %s", req.HostID, res.Code, res.Message)
		}
		host.Stage, host.Error = stageRolledBack, "rolled back by the operator"
		poolUpdatePause(r, protocol.PoolUpdateRolledBack, "host "+req.HostID+" was rolled back; resume to stage it again or abort")
	}
	r.UpdatedAt = now
	return r, nil
}

// poolUpdateAborting is a rollout whose abort has not yet been confirmed: paused, with the cleanup
// the coordinator keeps retrying.
func poolUpdateAborting(r *protocol.PoolUpdateRollout) bool {
	return r != nil && r.State == protocol.PoolUpdatePaused && r.Pause != nil && r.Pause.Code == protocol.PoolUpdateAbortCleanupPending
}

// poolUpdateUnconfirmed is whether a host that runs the target release still owes a commit: it
// booted into the release (or is armed for it) and the engine has not blessed it yet. Every other
// phase, a host that never updated (idle) included, has nothing left to confirm.
func poolUpdateUnconfirmed(phase string) bool {
	return phase == nexupdate.PhaseArmed || phase == nexupdate.PhaseBooted
}

// poolUpdateRetryStart is when the host's open retry window began. A record written before
// RetryStartedAt existed carries only StepStartedAt, which then stands in for it.
func poolUpdateRetryStart(host *protocol.PoolUpdateHost) time.Time {
	if host.RetryStartedAt.IsZero() {
		return host.StepStartedAt
	}
	return host.RetryStartedAt
}

// poolUpdateOwes is whether an abort still owes this host something: it started on the host
// (a host the rollout never took out of service has nothing to put back), and the host is still in
// maintenance mode or still has guests the rollout stopped. A host that is gone or ejected owes
// nothing that can be done.
func poolUpdateOwes(host *protocol.PoolUpdateHost, member protocol.PoolMember, ok bool) bool {
	switch host.Stage {
	case stageStaged, stageTeardown, stageDrain, stageActivate, stageBoot, stageConfirm, stageRelease, stageRestart, stageRolledBack:
	default:
		return false
	}
	return ok && member.Status != "ejected" && (member.Maintenance || len(host.StoppedByUpdate) > 0)
}

// abortCleanup is one try at putting back what the rollout took from every host it had started
// on: maintenance mode and the guests it stopped, then a check that those guests run. A host that
// cannot be put back yet (it is rebooting, a guest will not start) keeps its StoppedByUpdate list
// and an Error naming what is still owed. An operator's try (manual) goes at once and opens a new
// window; the coordinator's own tries wait out each host's retry pause and stop for a host once
// poolUpdateAbortWindow has passed since the abort began on it.
func (c *poolUpdateCoordinator) abortCleanup(ctx context.Context, r *protocol.PoolUpdateRollout, now time.Time, manual bool) {
	for i := range r.Hosts {
		host := &r.Hosts[i]
		member, ok := c.member(host.HostID)
		if !poolUpdateOwes(host, member, ok) {
			continue
		}
		switch {
		case manual:
			host.StepStartedAt, host.DeadlineAt = now, time.Time{}
		case now.Before(host.DeadlineAt) || now.Sub(host.StepStartedAt) >= poolUpdateAbortWindow:
			continue
		}
		var owed []string
		if member.Maintenance && poolUpdateMaintenance(c.s, host.HostID, false) != nil {
			owed = append(owed, "maintenance mode could not be cleared; clear it by hand")
		}
		if len(host.StoppedByUpdate) > 0 {
			reason, journaled := c.restartStopped(ctx, r, host, member)
			if reason == "" {
				host.StoppedByUpdate = nil
			} else {
				owed = append(owed, "guests "+strings.Join(host.StoppedByUpdate, ", ")+" were stopped by the rollout and are not running again ("+resultError(reason)+"); start them by hand")
				if journaled {
					// The engine replays a failed start under the same operation ID.
					host.Attempt++
				}
			}
		}
		host.Error = strings.Join(owed, "; ")
		if len(owed) > 0 {
			host.DeadlineAt = now.Add(poolUpdateRetryPause)
		}
	}
}

// finishAbort tries the cleanup, then settles the record: aborted when no host owes anything, or
// paused abort_cleanup_pending naming the hosts that do. The coordinator carries on with it
// until poolUpdateAbortWindow has passed on every one of them; after that only an operator's
// abort (a new try, or Force) moves it.
func (c *poolUpdateCoordinator) finishAbort(ctx context.Context, r *protocol.PoolUpdateRollout, now time.Time, manual bool) {
	c.abortCleanup(ctx, r, now, manual)
	var owing []string
	retrying := false
	for i := range r.Hosts {
		host := &r.Hosts[i]
		member, ok := c.member(host.HostID)
		if poolUpdateOwes(host, member, ok) {
			owing = append(owing, host.HostID)
			retrying = retrying || now.Sub(host.StepStartedAt) < poolUpdateAbortWindow
		}
	}
	if len(owing) == 0 {
		r.State, r.Pause, r.PauseRequested = protocol.PoolUpdateAborted, nil, false
		return
	}
	message := "the abort has not yet put back " + strings.Join(owing, ", ") + " (maintenance mode or stopped guests; see each host's error)"
	if retrying {
		message += "; the coordinator keeps retrying"
	} else {
		message += "; automatic retries stopped: fix the cause and abort again to retry, or abort with force to end the rollout and finish by hand"
	}
	poolUpdatePause(r, protocol.PoolUpdateAbortCleanupPending, message)
}

// restartStopped asks the host to start the guests the rollout stopped and reads back that they run.
// It returns why they do not, or "" when they all do; journaled says the host reported a failed
// start, which its engine will answer again under the same operation ID.
func (c *poolUpdateCoordinator) restartStopped(ctx context.Context, r *protocol.PoolUpdateRollout, host *protocol.PoolUpdateHost, member protocol.PoolMember) (reason string, journaled bool) {
	res, err := c.peers.Step(ctx, member, protocol.PoolUpdateStep{RolloutID: r.ID, StepID: poolUpdateStepID(r.ID, host.HostID, "abort", host.Attempt), Action: "undrain", Sequence: r.Sequence, VMIDs: host.StoppedByUpdate})
	switch {
	case err != nil:
		return "the host cannot be reached", false
	case res.Code != "":
		return res.Code + ": " + res.Message, false
	}
	status, err := c.peers.Status(ctx, member)
	if err != nil {
		return "the host cannot be reached to confirm", false
	}
	for _, id := range host.StoppedByUpdate {
		if !slices.Contains(status.RunningVMs, id) {
			if res.Message != "" {
				return res.Message, true
			}
			return "guest " + id + " is not running", false
		}
	}
	return "", false
}

func (c *poolUpdateCoordinator) member(hostID string) (protocol.PoolMember, bool) {
	membership, err := Membership(c.s)
	if err != nil {
		return protocol.PoolMember{}, false
	}
	return poolMemberByID(membership, hostID)
}

func poolUpdateHostRecord(r *protocol.PoolUpdateRollout, hostID string) *protocol.PoolUpdateHost {
	for i := range r.Hosts {
		if r.Hosts[i].HostID == hostID {
			return &r.Hosts[i]
		}
	}
	return nil
}

// poolUpdateStepID is the engine request ID of one step of one host: "ru-" and 40 hex digits of a
// SHA-256 over (rollout, host, stage, attempt). It is valid whatever the rollout and host IDs look
// like (the engine takes 8 to 64 letters, digits, - or _), bounded, the same on every repeat of a
// step, and different for each attempt, so a host sent through staging again is not answered with
// the operations of the attempt before.
func poolUpdateStepID(rolloutID, hostID, stage string, attempt int) string {
	sum := sha256.Sum256([]byte(rolloutID + "\x00" + hostID + "\x00" + stage + "\x00" + strconv.Itoa(attempt)))
	return "ru-" + hex.EncodeToString(sum[:20])
}

// poolUpdateNextHost is the first host whose update is not finished.
func poolUpdateNextHost(r *protocol.PoolUpdateRollout) *protocol.PoolUpdateHost {
	for i := range r.Hosts {
		if r.Hosts[i].Stage != stageDone {
			return &r.Hosts[i]
		}
	}
	return nil
}

// Keep an in-flight host first until it is put back in service. Only untouched unfinished
// hosts can move around it; the current master's turn comes after all other such hosts.
func poolUpdateOrderHosts(r *protocol.PoolUpdateRollout, masterID string, members []protocol.PoolMember) {
	start := 0
	for i := range r.Hosts {
		for _, member := range members {
			if member.HostID == r.Hosts[i].HostID && member.Maintenance {
				start = i + 1
			}
		}
		switch r.Hosts[i].Stage {
		case stagePending, stageStaged, stageDone:
		default:
			start = i + 1
		}
	}
	for i := start; i < len(r.Hosts); i++ {
		if r.Hosts[i].HostID == masterID && r.Hosts[i].Stage != stageDone {
			master := r.Hosts[i]
			copy(r.Hosts[i:], r.Hosts[i+1:])
			r.Hosts[len(r.Hosts)-1] = master
			return
		}
	}
}

// poolUpdateBeforeReboot is true for the stages that have not yet armed the host's next boot,
// the ones that take a host out of service and so need the gates.
func poolUpdateBeforeReboot(stage string) bool {
	switch stage {
	case stagePending, stageStaged, stageTeardown, stageDrain, stageActivate, stageRolledBack:
		return true
	}
	return false
}

func poolUpdateRefusalCode(err error) string {
	var refusal *PoolUpdateError
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return protocol.PoolUpdateNeedsAttention
}

func poolUpdatePause(r *protocol.PoolUpdateRollout, code, message string) {
	r.State, r.Pause, r.PauseRequested = protocol.PoolUpdatePaused, &protocol.PoolUpdatePause{Code: code, Message: message}, false
}

// poolUpdateBetweenHosts is true when no host is partway through its own update.
func poolUpdateBetweenHosts(r *protocol.PoolUpdateRollout) bool {
	for _, host := range r.Hosts {
		switch host.Stage {
		case stagePending, stageStaged, stageDone:
		default:
			return false
		}
	}
	return true
}

// poolUpdateProgress names where the phase and every host stand, so a pass that moved any of them
// is told to the members that could take the rollout over.
func poolUpdateProgress(r *protocol.PoolUpdateRollout) string {
	var b strings.Builder
	b.WriteString(r.Phase)
	if r.Controller != nil {
		b.WriteString("|" + r.Controller.Stage)
	}
	for _, host := range r.Hosts {
		b.WriteString("|" + host.HostID + "=" + host.Stage)
	}
	return b.String()
}

// pushReplica puts the record on the members that could take the rollout over. It is best
// effort: the probe loop pushes the pool's replica again on every tick and each member's
// acknowledgement or failure is part of the pool's replica status, so a miss delays a promoted
// master's view and never blocks the rollout. It is not silent, though; the miss is logged.
func (c *poolUpdateCoordinator) pushReplica(ctx context.Context) {
	if err := pushPoolAuthorityReplicasAfterUpdate(ctx, c.s, poolUpdateReplicaPushTO); err != nil && !errors.Is(err, errPoolAuthorityReplicaBusy) && ctx.Err() == nil {
		log.Printf("pool rollout record was not acknowledged by every member")
	}
}

// Advance moves the rollout at most one step. It is safe to call from any number of ticks and
// after any restart: the record decides where it stands, and a host's step is idempotent. A pass
// is bounded by poolUpdatePassTimeout, so a pool of unreachable hosts cannot hold the lock a
// pause or an abort is waiting for.
func (c *poolUpdateCoordinator) Advance(ctx context.Context) error {
	poolUpdateMu.Lock()
	defer poolUpdateMu.Unlock()
	r, err := LoadPoolUpdate(c.s)
	aborting := poolUpdateAborting(r)
	if err != nil || r == nil {
		return err
	}
	membership, err := c.masterMembership()
	if err != nil {
		// A host that is not (or cannot prove it is) the master changes nothing: the record
		// is left exactly as the authority that owns it last wrote it.
		return nil
	}
	pass, cancel := context.WithTimeout(ctx, poolUpdatePassTimeout)
	defer cancel()
	if receipt := poolUpdatePendingControl(r); receipt != nil {
		_, err = c.continueControl(pass, r, membership, receipt)
		return err
	}
	if r.State != protocol.PoolUpdateRunning && !aborting {
		return nil
	}
	before, _ := json.Marshal(r)
	progress := poolUpdateProgress(r)
	switch {
	case aborting:
		c.finishAbort(pass, r, poolUpdateNow(), false)
	case membership.Generation != r.MembershipGeneration:
		poolUpdatePause(r, protocol.PoolUpdateMembershipChanged, "the pool membership changed; review the pool and resume")
	case poolUpdateHAEnabled(c.s):
		poolUpdatePause(r, protocol.PoolUpdateHAEnabled, "HA was enabled during the rollout; turn it off and resume")
	default:
		c.step(pass, r, membership)
	}
	after, _ := json.Marshal(r)
	if bytes.Equal(before, after) {
		return nil
	}
	r.UpdatedAt = poolUpdateNow()
	if err = savePoolUpdate(c.s, r); err != nil {
		return err
	}
	if r.State != protocol.PoolUpdateRunning || poolUpdateProgress(r) != progress {
		c.pushReplica(ctx)
	}
	return nil
}

func (c *poolUpdateCoordinator) needsAttention(r *protocol.PoolUpdateRollout, host *protocol.PoolUpdateHost, code, message string) {
	message = code + ": " + message
	if host != nil {
		host.Error = message
		message = host.HostID + ": " + message
	}
	poolUpdatePause(r, protocol.PoolUpdateNeedsAttention, message)
	if code == protocol.PoolUpdateRolledBack {
		r.Pause.Code = code
	}
}

// step does the next unit of work of a running rollout.
func (c *poolUpdateCoordinator) step(ctx context.Context, r *protocol.PoolUpdateRollout, membership protocol.PoolMembership) {
	now := poolUpdateNow()
	if r.PauseRequested && poolUpdateBetweenHosts(r) {
		poolUpdatePause(r, protocol.PoolUpdatePausedByOperator, "paused by the operator")
		return
	}
	if r.Phase == "controller" {
		c.controllerFirst(ctx, r, membership)
		return
	}
	if r.Phase == "staging" {
		c.stageEverywhere(ctx, r, membership)
		return
	}
	poolUpdateOrderHosts(r, membership.MasterHostID, membership.Members)
	host := poolUpdateNextHost(r)
	if host == nil {
		c.finalize(ctx, r, membership)
		return
	}
	member, ok := poolMemberByID(membership, host.HostID)
	if !ok {
		poolUpdatePause(r, protocol.PoolUpdateMembershipChanged, "host "+host.HostID+" is no longer a member")
		return
	}
	if host.StepStartedAt.IsZero() {
		host.StepStartedAt = now
	}
	timedOut := func(code, message string) bool {
		if now.Sub(host.StepStartedAt) < poolUpdateStepTimeout {
			return false
		}
		poolUpdatePause(r, code, host.HostID+": "+message)
		return true
	}
	next := func(stage string) {
		host.Stage, host.StepStartedAt, host.DeadlineAt, host.Error = stage, now, time.Time{}, ""
	}
	stepOn := func(stage, action string, vms []string) (protocol.PoolUpdateStepResult, bool) {
		res, err := c.peers.Step(ctx, member, protocol.PoolUpdateStep{RolloutID: r.ID, StepID: poolUpdateStepID(r.ID, host.HostID, stage, host.Attempt),
			Action: action, Sequence: r.Sequence, RunningVMs: r.RunningVMs, VMIDs: vms})
		return res, err == nil
	}
	// A definite refusal of a guest step stops for an operator with the host's own words. A guest
	// inventory that cannot be listed yet is the one code the host may recover from by itself.
	refused := func(res protocol.PoolUpdateStepResult) bool {
		if res.Code == "" || res.Code == "vm_inventory_unavailable" {
			return false
		}
		c.needsAttention(r, host, res.Code, res.Message)
		return true
	}
	// why is what the host said about a guest step that is not finished.
	why := func(res protocol.PoolUpdateStepResult) string {
		switch {
		case res.Code != "" && res.Message != "":
			return ": " + res.Code + ": " + res.Message
		case res.Code != "":
			return ": " + res.Code
		case res.Message != "":
			return ": " + res.Message
		}
		return ""
	}
	// A guest step the host cannot finish yet (libvirt not up after a reboot, a mount that is not
	// there yet, admission refusing while the master reboots) is asked again, but bounded: the
	// first failure opens a window of poolUpdateRetryWindow, and a failure still there when it
	// closes pauses the rollout needs_attention with the host's own words. A shutdown or start
	// the host reports as failed is journaled by its engine under its operation ID and replayed
	// unchanged, so each retry is a new step identity, and waits a moment instead of failing
	// again on every tick. An answer that is not a failure closes the window.
	retrying := func() bool { return now.Before(host.DeadlineAt) }
	lastWhy := func() string {
		if host.Error == "" {
			return ""
		}
		return ": " + host.Error
	}
	transient := func(what string, res protocol.PoolUpdateStepResult) (paused bool) {
		inventory := res.Code == "vm_inventory_unavailable"
		failed := res.Code == "" && res.Message != ""
		if !inventory && !failed {
			host.Error = ""
			return false
		}
		text := res.Message
		if text == "" {
			text = "the running virtual machines cannot be listed"
		}
		if host.Error != "" && now.Sub(poolUpdateRetryStart(host)) >= poolUpdateRetryWindow {
			if inventory {
				what = res.Code
			}
			c.needsAttention(r, host, what, text)
			return true
		}
		if host.Error == "" {
			host.RetryStartedAt = now
		}
		host.Error = text
		if failed {
			host.Attempt++
			host.DeadlineAt = now.Add(poolUpdateRetryPause)
		}
		return false
	}
	switch host.Stage {
	case stagePending:
		// stageEverywhere staged this host already. A host sent back here by a resume after a
		// rollback is staged again under its new attempt, and only then gated.
		res, ok := stepOn("stage", "stage", nil)
		switch {
		case !ok:
			timedOut(protocol.PoolUpdateStepTimeout, "the host cannot be reached")
		case res.Code != "":
			c.needsAttention(r, host, res.Code, res.Message)
		case res.Done:
			next(stageStaged)
		}
	case stageStaged:
		// The gates, the host's own readiness and the maintenance flip are one decision: the pool
		// is read again here, under the same lock, so nothing that changed since this pass began
		// can slip between the check and the flip.
		if err := c.regate(ctx, r, host.HostID); err != nil {
			poolUpdatePause(r, poolUpdateRefusalCode(err), err.Error())
			return
		}
		if status, err := c.peers.Status(ctx, member); err != nil || status.Busy {
			if err != nil {
				timedOut(protocol.PoolUpdateStepTimeout, "the host cannot be reached: "+err.Error())
			} else {
				poolUpdatePause(r, protocol.PoolUpdateHostBusy, host.HostID+" is running another update operation")
			}
			return
		}
		if err := poolUpdateMaintenance(c.s, host.HostID, true); err != nil {
			c.needsAttention(r, host, "maintenance_failed", err.Error())
			return
		}
		next(stageTeardown)
	case stageTeardown:
		// While the pool still wants a cluster, only the host being drained leaves it: the others
		// keep feeding their watchdogs for as long as the pool needs them. A pool that stops
		// wanting one with this host in maintenance tears it down on every host, and the rollout
		// waits for all of them.
		_, wanted, _ := poolClusterIntent(c.s, membership)
		for _, m := range membership.Members {
			if m.Status == "ready" && poolWatchdogFeeding(m) && (!wanted || m.HostID == host.HostID) {
				timedOut(protocol.PoolUpdateStepTimeout, "the pool's watchdogs did not stop in time")
				return
			}
		}
		next(stageDrain)
	case stageDrain:
		status, err := c.peers.Status(ctx, member)
		if err != nil {
			timedOut(protocol.PoolUpdateStepTimeout, "the host cannot be reached")
			return
		}
		if len(host.StoppedByUpdate) == 0 && len(status.RunningVMs) > 0 {
			if r.RunningVMs != "shutdown" {
				timedOut(protocol.PoolUpdateRunningGuests, "running guests block the update: "+strings.Join(status.RunningVMs, ", "))
				return
			}
			// Recorded before the first shutdown is requested, so a crash between the two can
			// never forget which guests this rollout owes a restart.
			host.StoppedByUpdate = append([]string(nil), status.RunningVMs...)
			return
		}
		if len(host.StoppedByUpdate) > 0 {
			// The guest list is on the members that could take over before the first guest is
			// asked to stop: a master lost after that point must not leave guests nobody owes a
			// restart.
			if key := r.ID + "|" + host.HostID + "|" + strings.Join(host.StoppedByUpdate, ","); c.replicatedFor != key {
				if err := c.replicate(ctx); err != nil {
					timedOut(protocol.PoolUpdateStepTimeout, "the rollout record is not yet on the pool's members, so no guest is stopped: "+err.Error())
					return
				}
				c.replicatedFor = key
			}
			if retrying() {
				timedOut(protocol.PoolUpdateStepTimeout, "guests did not shut down in time"+lastWhy())
				return
			}
			res, ok := stepOn(stageDrain, "drain", host.StoppedByUpdate)
			if ok && refused(res) {
				return
			}
			if !ok || !res.Done {
				if ok && transient("vm_shutdown_failed", res) {
					return
				}
				timedOut(protocol.PoolUpdateStepTimeout, "guests did not shut down in time"+why(res))
				return
			}
			status, err = c.peers.Status(ctx, member)
			if err != nil || len(status.RunningVMs) > 0 {
				timedOut(protocol.PoolUpdateStepTimeout, "guests did not shut down in time")
				return
			}
		}
		next(stageActivate)
	case stageActivate:
		// A host that already runs the release (the master after its own reboot) skips ahead.
		status, statusErr := c.peers.Status(ctx, member)
		if statusErr != nil {
			// Not knowing whether the host is armed or already runs the release is no licence to
			// reboot it: the gates below need that answer, so nothing is sent until it can be read.
			timedOut(protocol.PoolUpdateStepTimeout, "the host's update status cannot be read: "+statusErr.Error())
			return
		}
		if status.RunningSequence == r.Sequence {
			next(stageConfirm)
			return
		}
		// Not yet armed: this is the last moment before the host goes down, so the gates are run
		// again on the pool as it is now. An armed host's repeat of the step changes nothing.
		if status.Phase != "armed" {
			if err := c.regate(ctx, r, host.HostID); err != nil {
				poolUpdatePause(r, poolUpdateRefusalCode(err), err.Error())
				return
			}
		}
		res, ok := stepOn(stageActivate, "activate", nil)
		switch {
		case !ok:
			// The step may have armed and rebooted before its answer was lost: the boot stage
			// would be right, the activate stage repeats safely. Wait for the next pass.
			timedOut(protocol.PoolUpdateStepTimeout, "the host cannot be reached")
		case res.Code != "":
			c.needsAttention(r, host, res.Code, res.Message)
		case res.Done:
			next(stageBoot)
			host.DeadlineAt = now.Add(poolUpdateBootDeadline)
		}
	case stageBoot:
		status, err := c.peers.Status(ctx, member)
		if err == nil && status.Phase == "rolled_back" {
			c.needsAttention(r, host, protocol.PoolUpdateRolledBack, "the host fell back to its previous release ("+status.Failure+")")
			host.Stage = stageRolledBack
			return
		}
		if err == nil && status.RunningSequence == r.Sequence {
			next(stageConfirm)
			return
		}
		if now.After(host.DeadlineAt) {
			c.needsAttention(r, host, protocol.PoolUpdateNeedsAttention, "the host did not return on the new release in time; nothing was restarted elsewhere and no guest is assumed stopped")
		}
	case stageConfirm:
		res, ok := stepOn(stageConfirm, "confirm", nil)
		switch {
		case !ok:
			timedOut(protocol.PoolUpdateUnconfirmedHost, "the host cannot be reached to confirm the release")
		case res.Code == protocol.PoolUpdateRolledBack:
			c.needsAttention(r, host, res.Code, res.Message)
			host.Stage = stageRolledBack
		case res.Code != "":
			c.needsAttention(r, host, res.Code, res.Message)
		case res.Done && member.Status == "ready":
			next(stageRelease)
		default:
			timedOut(protocol.PoolUpdateUnconfirmedHost, "the host has not confirmed the release")
		}
	case stageRelease:
		if err := poolUpdateMaintenance(c.s, host.HostID, false); err != nil {
			c.needsAttention(r, host, "maintenance_failed", err.Error())
			return
		}
		next(stageRestart)
	case stageRestart:
		if len(host.StoppedByUpdate) > 0 {
			if retrying() {
				timedOut(protocol.PoolUpdateStepTimeout, "guests did not restart in time"+lastWhy())
				return
			}
			res, ok := stepOn(stageRestart, "undrain", host.StoppedByUpdate)
			if ok && refused(res) {
				return
			}
			if !ok || !res.Done {
				if ok && transient("vm_start_failed", res) {
					return
				}
				timedOut(protocol.PoolUpdateStepTimeout, "guests did not restart in time"+why(res))
				return
			}
		}
		next(stageSoak)
		host.SoakUntil = now.Add(poolUpdateMinSoak)
	case stageSoak:
		if now.Before(host.SoakUntil) {
			return
		}
		next(stageDone)
	case stageRolledBack:
		poolUpdatePause(r, protocol.PoolUpdateRolledBack, "host "+host.HostID+" is rolled back; resume to stage it again or abort")
	}
}

// stageEverywhere asks every host to stage the release, in the same pass: staging disrupts
// nothing and never arms the next boot.
func (c *poolUpdateCoordinator) stageEverywhere(ctx context.Context, r *protocol.PoolUpdateRollout, membership protocol.PoolMembership) {
	now := poolUpdateNow()
	all := true
	for i := range r.Hosts {
		host := &r.Hosts[i]
		if host.Stage != stagePending {
			continue
		}
		member, ok := poolMemberByID(membership, host.HostID)
		if !ok {
			poolUpdatePause(r, protocol.PoolUpdateMembershipChanged, "host "+host.HostID+" is no longer a member")
			return
		}
		if host.StepStartedAt.IsZero() {
			host.StepStartedAt = now
		}
		res, err := c.peers.Step(ctx, member, protocol.PoolUpdateStep{RolloutID: r.ID, StepID: poolUpdateStepID(r.ID, host.HostID, "stage", host.Attempt), Action: "stage", Sequence: r.Sequence})
		switch {
		case err == nil && res.Code != "":
			c.needsAttention(r, host, res.Code, res.Message)
			return
		case err == nil && res.Done:
			host.Stage, host.StepStartedAt = stageStaged, time.Time{}
			continue
		}
		all = false
		if now.Sub(host.StepStartedAt) >= poolUpdateStepTimeout {
			poolUpdatePause(r, protocol.PoolUpdateStepTimeout, host.HostID+": the host did not finish staging the release in time")
			return
		}
	}
	if all {
		r.Phase = "controller"
	}
}

// controllerFirst is the controller_first step: before any host activates, the host that runs the
// Controller VM replaces its appliance image, so the Controller is upgraded before Core. A release
// without a Controller image, or a pool with no Controller appliance on any host, goes straight on
// (a Controller outside the pool is updated on its own host with scope "controller"). The swap itself
// is the host's journaled operation; this only asks and waits.
func (c *poolUpdateCoordinator) controllerFirst(ctx context.Context, r *protocol.PoolUpdateRollout, membership protocol.PoolMembership) {
	now := poolUpdateNow()
	if r.Controller == nil {
		r.Controller = &protocol.PoolUpdateController{Stage: protocol.PoolUpdateControllerScan, StartedAt: now}
	}
	ctl := r.Controller
	waiting := func(what string) {
		if now.Sub(ctl.StartedAt) >= poolUpdateStepTimeout {
			poolUpdatePause(r, protocol.PoolUpdateStepTimeout, what)
		}
	}
	switch ctl.Stage {
	case protocol.PoolUpdateControllerScan:
		for _, host := range r.Hosts {
			member, ok := poolMemberByID(membership, host.HostID)
			if !ok {
				poolUpdatePause(r, protocol.PoolUpdateMembershipChanged, "host "+host.HostID+" is no longer a member")
				return
			}
			status, err := c.peers.Status(ctx, member)
			if err == nil && status.RunningSequence == r.Sequence {
				continue // it runs the release already, so its Controller was swapped with it
			}
			if err != nil || status.ControllerVM && (status.Target == nil || status.Target.Sequence != r.Sequence) {
				waiting(host.HostID + ": the host cannot be reached to look for the Controller")
				return
			}
			if status.ControllerVM && status.Target.ControllerImage {
				ctl.Stage, ctl.HostID, ctl.StartedAt = protocol.PoolUpdateControllerSwap, host.HostID, now
				return
			}
		}
		ctl.Stage, r.Phase = protocol.PoolUpdateControllerDone, "hosts"
	case protocol.PoolUpdateControllerSwap:
		member, ok := poolMemberByID(membership, ctl.HostID)
		if !ok {
			poolUpdatePause(r, protocol.PoolUpdateMembershipChanged, "host "+ctl.HostID+" is no longer a member")
			return
		}
		res, err := c.peers.Step(ctx, member, protocol.PoolUpdateStep{RolloutID: r.ID, StepID: poolUpdateStepID(r.ID, ctl.HostID, "controller", ctl.Attempt),
			Action: "controller", Sequence: r.Sequence})
		switch {
		case err != nil:
			waiting(ctl.HostID + ": the host cannot be reached")
		case res.Code != "":
			// controller_rolled_back (the old image and its data are back) or controller_needs_attention:
			// either way an operator decides; nothing else of the pool has been touched yet.
			ctl.Error = res.Code + ": " + res.Message
			poolUpdatePause(r, protocol.PoolUpdateNeedsAttention, ctl.HostID+": "+ctl.Error)
		case res.Done:
			ctl.Stage, r.Phase = protocol.PoolUpdateControllerDone, "hosts"
		default:
			waiting(ctl.HostID + ": the Controller swap did not finish in time")
		}
	}
}

// finalize runs once every host reports done: it re-reads each host, and only a pool whose hosts
// all run and have committed the release records finalized_sequence.
func (c *poolUpdateCoordinator) finalize(ctx context.Context, r *protocol.PoolUpdateRollout, membership protocol.PoolMembership) {
	for _, host := range r.Hosts {
		member, ok := poolMemberByID(membership, host.HostID)
		if !ok {
			poolUpdatePause(r, protocol.PoolUpdateUnconfirmedHost, "host "+host.HostID+" is no longer a member")
			return
		}
		status, err := c.peers.Status(ctx, member)
		if err != nil || status.RunningSequence != r.Sequence || poolUpdateUnconfirmed(status.Phase) {
			poolUpdatePause(r, protocol.PoolUpdateUnconfirmedHost, "host "+host.HostID+" is not confirmed on the release")
			return
		}
	}
	r.Phase, r.State, r.FinalizedSequence = "finalize", protocol.PoolUpdateFinalized, r.Sequence
}

// replicate puts the rollout record on every member that could take the pool over, and returns
// only once they have acknowledged it.
func (c *poolUpdateCoordinator) replicate(ctx context.Context) error {
	return pushPoolAuthorityReplicasAfterUpdate(ctx, c.s, poolUpdateReplicaPushTO)
}

// regate reads the pool again and runs the gate on it, so a decision to take a host out of
// service is made on the pool as it is at that moment and not as it was when the pass began.
func (c *poolUpdateCoordinator) regate(ctx context.Context, r *protocol.PoolUpdateRollout, hostID string) error {
	fresh, err := c.masterMembership()
	if err != nil {
		return err
	}
	if fresh.Generation != r.MembershipGeneration {
		return updateRefusal(protocol.PoolUpdateMembershipChanged, "the pool membership changed; review the pool and resume")
	}
	return c.gate(ctx, fresh, hostID)
}

// gate is every condition that must hold before a host may be taken out of service: it is ready,
// every other host is ready and out of maintenance, and the pool's live quorum survives its stop.
func (c *poolUpdateCoordinator) gate(ctx context.Context, membership protocol.PoolMembership, hostID string) error {
	if self, ok := poolMemberByID(membership, hostID); !ok || self.Status != "ready" {
		return updateRefusal(protocol.PoolUpdateQuorumMargin, "host %s is not a ready member of this pool", hostID)
	}
	for _, member := range membership.Members {
		switch {
		case member.Status == "ejected" || member.HostID == hostID:
		case member.Status != "ready":
			return updateRefusal(protocol.PoolUpdateQuorumMargin, "host %s is %s; every other host must stay ready while %s updates", member.HostID, member.Status, hostID)
		case member.Maintenance:
			return updateRefusal(protocol.PoolUpdateHostBusy, "host %s is already in maintenance mode", member.HostID)
		}
	}
	return poolUpdateQuorumGate(ctx, c.s, membership, hostID)
}

// poolUpdateQuorumGate refuses when stopping hostID would leave the pool without a majority, read
// from the master's own Corosync as it is now: the votes present (a voting witness included)
// against the votes a majority needs, and every other host of the nodelist in the quorate
// partition. Membership rows say what the pool intends, not who is voting. A pool that runs no
// cluster and forms none has no quorum to lose.
func poolUpdateQuorumGate(ctx context.Context, s *Store, membership protocol.PoolMembership, hostID string) error {
	nodes, want, _ := poolClusterIntent(s, membership)
	clustered := poolHasClusteredStorage(s)
	q, err := localQuorum(ctx)
	// A host with no corosync-quorumtool has no cluster software (an image without the nex-cluster
	// layer): the pool's cluster intent cannot form, nothing votes and no watchdog is fed, so there
	// is no quorum to lose. Found by the three-host lab: without this a three-host image pool could
	// never leave quorum_margin.
	if errors.Is(err, exec.ErrNotFound) && !clustered {
		return nil
	}
	if err != nil || !q.Observed {
		if want || clustered {
			return updateRefusal(protocol.PoolUpdateQuorumMargin, "this pool keeps a Corosync cluster and the master cannot read its quorum, so it cannot tell what stopping %s would cost", hostID)
		}
		return nil
	}
	switch {
	case !q.Quorate:
		return updateRefusal(protocol.PoolUpdateQuorumMargin, "this pool's Corosync cluster is not quorate as the master sees it")
	case q.TotalVotes == 0 || q.Quorum == 0:
		return updateRefusal(protocol.PoolUpdateQuorumMargin, "Corosync did not report this pool's votes, so the margin cannot be checked")
	case clustered && q.ExpectedVotes < 3:
		return updateRefusal(protocol.PoolUpdateQuorumMargin, "a pool with clustered storage needs at least 3 voters to update a host; its cluster expects %d", q.ExpectedVotes)
	}
	for _, node := range nodes {
		if node.HostID != hostID && !slices.Contains(q.Hosts, node.HostID) {
			return updateRefusal(protocol.PoolUpdateQuorumMargin, "host %s is ready but not voting in this pool's quorate partition", node.HostID)
		}
	}
	remaining := q.TotalVotes
	if slices.Contains(q.Hosts, hostID) {
		remaining--
	}
	if remaining < q.Quorum {
		return updateRefusal(protocol.PoolUpdateQuorumMargin, "stopping %s would leave %d live votes, and %d are needed for a majority", hostID, remaining, q.Quorum)
	}
	return nil
}

// poolUpdateSetMaintenance turns a host's maintenance flag through the existing pool-maintenance
// command, so the drain intent is the same durable membership field an operator sets.
func poolUpdateSetMaintenance(s *Store, hostID string, on bool) error {
	result := ExecutePoolMaintenance(s, protocol.Command{OperationID: "pool-update-maintenance-" + hostID, Kind: "pool-maintenance",
		Pool: &protocol.PoolCommand{RequestID: "pool-update-maintenance-" + hostID, Action: "maintenance", MemberHostID: hostID, Enabled: on,
			Membership: protocol.PoolMembership{MasterHostID: s.State.HostID}}})
	if result.Status != "succeeded" {
		return errors.New(result.Error)
	}
	return nil
}

// PoolUpdateForPool is the rollout as the master reports it in inventory.
func PoolUpdateForPool(s *Store) *protocol.PoolUpdateRollout {
	r, err := LoadPoolUpdate(s)
	if err != nil {
		return nil
	}
	return r
}

// AdoptPoolUpdateOnPromotion is the promoted master's side: a rollout it inherits is paused
// master_changed whatever it was doing, so an operator decides resume or abort; an abort whose
// cleanup is still pending stays as it is, and the new master carries on with it. It is written
// before the host is recorded as master, so a master that exists never lacks the record; the
// returned function puts the earlier file back should the promotion not complete.
//
// What the host inherits is the authority, so it also replaces a record this host kept from an
// earlier time as master: demotion does not clear the file, and a rollout that another master
// since finished or aborted must not come back as the pool's rollout.
func AdoptPoolUpdateOnPromotion(s *Store, inherited *protocol.PoolUpdateRollout) (undo func(), err error) {
	undo = func() {}
	local, _ := LoadPoolUpdate(s)
	var r protocol.PoolUpdateRollout
	switch {
	case poolUpdateActive(inherited):
		r = *inherited
		if !poolUpdateAborting(&r) {
			poolUpdatePause(&r, protocol.PoolUpdateMasterChanged, "the pool master changed during the rollout; resume or abort it")
		}
	case inherited != nil && (poolUpdateActive(local) || len(inherited.Controls) != 0):
		r = *inherited
	case poolUpdateActive(local):
		r = *local
		r.State, r.Pause, r.PauseRequested = protocol.PoolUpdateAborted, nil, false
	default:
		return undo, nil
	}
	r.Hosts = append([]protocol.PoolUpdateHost(nil), r.Hosts...)
	previous, readErr := os.ReadFile(poolUpdatePath(s))
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return undo, readErr
	}
	r.UpdatedAt = poolUpdateNow()
	if err = savePoolUpdate(s, &r); err != nil {
		return undo, err
	}
	return func() {
		if readErr != nil {
			_ = os.Remove(poolUpdatePath(s))
			return
		}
		_ = vmhost.AtomicWrite(poolUpdatePath(s), previous, 0600)
	}, nil
}

// ---- the live peer transport ----

type livePoolUpdatePeers struct{ s *Store }

func (p livePoolUpdatePeers) Status(ctx context.Context, member protocol.PoolMember) (protocol.PoolUpdateHostStatus, error) {
	var out protocol.PoolUpdateHostStatus
	if member.HostID == p.s.State.HostID {
		return PoolUpdateHostStatusFor(ctx, p.s)
	}
	client, err := poolTransferClient(p.s, member.PeerOrigin, member.HostID)
	if err != nil {
		return out, err
	}
	defer client.CloseIdleConnections()
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(work, http.MethodGet, strings.TrimRight(member.PeerOrigin, "/")+poolUpdateStatusPath, nil)
	if err != nil {
		return out, err
	}
	err = doPoolUpdate(client, req, member.HostID, &out)
	return out, err
}

func (p livePoolUpdatePeers) Step(ctx context.Context, member protocol.PoolMember, step protocol.PoolUpdateStep) (protocol.PoolUpdateStepResult, error) {
	var out protocol.PoolUpdateStepResult
	if member.HostID == p.s.State.HostID {
		return PoolUpdateHostStep(ctx, p.s, step), nil
	}
	client, err := poolTransferClient(p.s, member.PeerOrigin, member.HostID)
	if err != nil {
		return out, err
	}
	defer client.CloseIdleConnections()
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(step)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolUpdateStepPath, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	err = doPoolUpdate(client, req, member.HostID, &out)
	return out, err
}

func doPoolUpdate(client *http.Client, req *http.Request, hostID string, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("pool peer " + hostID + " is unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return errPoolUpdateUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("pool peer " + hostID + " refused the update request")
	}
	return decodePoolUpdateJSON(resp.Body, out)
}

func decodePoolUpdateJSON(r io.Reader, out any) error {
	return json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(out)
}

func writePoolUpdateJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ExecutePoolUpdate runs a relayed pool-update command (update-rollout or update-control) on
// the master. A refusal is a definite failed result carrying its stable code.
func ExecutePoolUpdate(ctx context.Context, s *Store, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	v := cmd.Pool
	if v == nil || v.Update == nil || v.Valid() != nil {
		result.Error = "pool update command carries no valid request"
		return result
	}
	c := NewPoolUpdateCoordinator(s)
	var err error
	if v.Action == "update-rollout" {
		_, _, err = c.Start(ctx, v.RequestID, *v.Update)
	} else {
		_, err = c.Control(ctx, v.RequestID, *v.Update)
	}
	if err != nil {
		result.Error = err.Error()
		if v.Action == "update-control" {
			r, loadErr := LoadPoolUpdate(s)
			if loadErr == nil && r != nil {
				for _, receipt := range r.Controls {
					if receipt.RequestID == v.RequestID && receipt.Request == poolUpdateControlIntent(*v.Update) &&
						(receipt.Rollout == nil && receipt.Code == "" || receipt.ReplicationPending) {
						result.Status = "needs_attention"
						break
					}
				}
			}
		}
		return result
	}
	result.Status = "succeeded"
	return result
}
