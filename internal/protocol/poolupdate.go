// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"errors"
	"time"
)

// PoolUpdateRolloutCapability gates the pool rollout coordinator: the pool-update command, the
// master's replicated update_rollout record and the peer step routes. The Controller refuses
// rollout routes unless the master and every member advertise it.
const PoolUpdateRolloutCapability = "pool-update-rollout-v1"

// Rollout states. A rollout is "running" or "paused" until it ends as "finalized" or "aborted". An
// abort whose cleanup is unconfirmed (a host not back in service, a guest not running again) does
// not end: it stays "paused" with the code abort_cleanup_pending until the cleanup is confirmed or
// an operator forces it.
const (
	PoolUpdateRunning   = "running"
	PoolUpdatePaused    = "paused"
	PoolUpdateFinalized = "finalized"
	PoolUpdateAborted   = "aborted"
)

// Pause codes: why a rollout stopped and waits for an operator or for a precondition.
const (
	PoolUpdatePausedByOperator  = "operator_paused"
	PoolUpdateMasterChanged     = "master_changed"
	PoolUpdateNeedsAttention    = "needs_attention"
	PoolUpdateRunningGuests     = "running_vms"
	PoolUpdateStepTimeout       = "step_timeout"
	PoolUpdateRolledBack        = "rolled_back"
	PoolUpdateUnconfirmedHost   = "unconfirmed"
	PoolUpdateQuorumMargin      = "quorum_margin"
	PoolUpdateHostBusy          = "host_busy"
	PoolUpdateHAEnabled         = "ha_enabled"
	PoolUpdateMembershipChanged = "membership_changed"
	// PoolUpdateAbortCleanupPending is an abort that has not yet put back every host's
	// maintenance mode and stopped guests. The coordinator keeps retrying it (bounded); the
	// rollout becomes aborted only once that is confirmed, or by an abort with Force.
	PoolUpdateAbortCleanupPending = "abort_cleanup_pending"
)

// PoolUpdateRequest is the body of the pool-update command. "update-rollout" starts (or, with
// DryRun, only checks) a rollout; "update-control" carries Control for an existing one.
type PoolUpdateRequest struct {
	Sequence   uint64 `json:"sequence,omitempty"`
	RunningVMs string `json:"running_vms,omitempty"` // refuse (default), shutdown; evacuate is refused
	DryRun     bool   `json:"dry_run,omitempty"`
	RolloutID  string `json:"rollout_id,omitempty"`
	Control    string `json:"control,omitempty"` // pause, resume, abort, rollback_host
	// Force ends an abort whose cleanup is still pending (pause code abort_cleanup_pending) without
	// confirming it: guests the rollout stopped may stay off and maintenance mode may stay on, as
	// the hosts' error fields say. Accepted only with control abort on such a rollout.
	Force  bool   `json:"force,omitempty"`
	HostID string `json:"host_id,omitempty"`
}

func (r PoolUpdateRequest) Valid(action string) error {
	if len(r.RolloutID) > 128 || len(r.HostID) > 128 {
		return errors.New("pool update identity is too long")
	}
	if r.Force && (action != "update-control" || r.Control != "abort") {
		return errors.New("force applies to a rollout abort only")
	}
	switch action {
	case "update-rollout":
		if r.Sequence == 0 {
			return errors.New("a pool rollout requires a release sequence")
		}
		if r.RunningVMs != "" && r.RunningVMs != "refuse" && r.RunningVMs != "shutdown" && r.RunningVMs != "evacuate" {
			return errors.New("running_vms must be refuse, shutdown or evacuate")
		}
		return nil
	case "update-control":
		switch r.Control {
		case "pause", "resume", "abort":
		case "rollback_host":
			if r.HostID == "" {
				return errors.New("rollback_host requires a host")
			}
		default:
			return errors.New("control must be pause, resume, abort or rollback_host")
		}
		if r.RolloutID == "" {
			return errors.New("a rollout control names its rollout")
		}
	}
	return nil
}

// PoolUpdateHost is the master's record of one host's progress. Stage is the next unfinished
// step of that host; the host's own state.json holds the image truth.
type PoolUpdateHost struct {
	HostID string `json:"host_id"`
	// Stage runs: pending, staged, teardown, drain, activate, boot, confirm, release, restart,
	// soak, done; rolled_back after a fallback or an operator rollback, until a resume.
	Stage string `json:"stage"`
	// StoppedByUpdate lists the guests this rollout shut down on this host. It is written before
	// the shutdown is requested, so a crash can never lose which guests to restart. An abort
	// empties it once the host confirms those guests run again; a non-empty list on an aborted
	// rollout is what a forced abort left for the operator to start by hand.
	StoppedByUpdate []string  `json:"stopped_by_update,omitempty"`
	StepStartedAt   time.Time `json:"step_started_at"`
	// RetryStartedAt opens the retry window of a guest step that failed; StepStartedAt, which
	// bounds the whole step, is not moved by it. Records written before it existed load as zero.
	RetryStartedAt time.Time `json:"retry_started_at,omitzero"`
	DeadlineAt     time.Time `json:"deadline_at"`
	SoakUntil      time.Time `json:"soak_until"`
	Error          string    `json:"error,omitempty"`
	// Attempt counts how often the rollout has sent this host through a step again (a resume
	// after a rollback or a failed step, or a guest start retried after the host reported it
	// failed). It is part of every step identity, so a new attempt is never a replay of the
	// engine operations the earlier one left behind.
	Attempt int `json:"attempt,omitempty"`
}

type PoolUpdatePause struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Stages of PoolUpdateController.
const (
	PoolUpdateControllerScan = "scan"
	PoolUpdateControllerSwap = "swap"
	PoolUpdateControllerDone = "done"
)

// PoolUpdateController records the controller_first step. Stage scan finds the host that runs the
// Controller VM, swap waits for that host's journaled appliance swap, done means no Controller is left to
// update first. The host's own vmhost journal holds the swap truth.
type PoolUpdateController struct {
	Stage     string    `json:"stage"`
	HostID    string    `json:"host_id,omitempty"`
	StartedAt time.Time `json:"started_at"`
	// Attempt is part of the swap's step identity: a resume after a rolled-back swap is a new swap.
	Attempt int    `json:"attempt,omitempty"`
	Error   string `json:"error,omitempty"`
}

// PoolUpdateControlResult binds a control identity to its normalized intent and original result.
// A nil Rollout with no Code is pending; its exact retry completes the same control. Result
// snapshots omit Controls so the durable, replicated history never recursively embeds itself.
type PoolUpdateControlResult struct {
	RequestID string             `json:"request_id"`
	Request   PoolUpdateRequest  `json:"request"`
	Rollout   *PoolUpdateRollout `json:"rollout,omitempty"`
	Code      string             `json:"code,omitempty"`
	Message   string             `json:"message,omitempty"`
	// ReplicationPending keeps the terminal result immutable while its replica ACK is outstanding.
	ReplicationPending bool `json:"replication_pending,omitempty"`
	// ReservedResultBytes reserves addressed authority room until the terminal result is recorded.
	ReservedResultBytes int `json:"reserved_result_bytes,omitempty"`
	// RollbackStarted binds an uncertain transport outcome to the unchanged host attempt.
	RollbackStarted bool `json:"rollback_started,omitempty"`
}

// PoolUpdateRollout is the pool master's durable rollout record (pool-update.json), replicated as
// the authority section update_rollout. A promoted master finds it paused master_changed.
type PoolUpdateRollout struct {
	ID                   string           `json:"id"`
	PoolID               string           `json:"pool_id"`
	Sequence             uint64           `json:"sequence"`
	RunningVMs           string           `json:"running_vms"`
	State                string           `json:"state"`
	Phase                string           `json:"phase"` // staging, controller, hosts, finalize
	Pause                *PoolUpdatePause `json:"pause,omitempty"`
	PauseRequested       bool             `json:"pause_requested,omitempty"`
	MembershipGeneration uint64           `json:"membership_generation"`
	Hosts                []PoolUpdateHost `json:"hosts"`
	// Controller is the controller_first step: the host that owns the Controller VM swaps its
	// appliance image before any host activates. nil until the phase starts.
	Controller        *PoolUpdateController `json:"controller,omitempty"`
	FinalizedSequence uint64                `json:"finalized_sequence,omitempty"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
	// Controls survive subsequent rollouts and promotion, so a delayed retry cannot act again.
	Controls []PoolUpdateControlResult `json:"controls,omitempty"`
}

func (r PoolUpdateRollout) Valid() error {
	if r.ID == "" || len(r.ID) > 128 || r.PoolID == "" || r.Sequence == 0 || len(r.Hosts) > 64 {
		return errors.New("pool update rollout is invalid")
	}
	switch r.State {
	case PoolUpdateRunning, PoolUpdatePaused, PoolUpdateFinalized, PoolUpdateAborted:
	default:
		return errors.New("pool update rollout state is invalid")
	}
	seen := make(map[string]bool, len(r.Controls))
	pending := false
	for _, control := range r.Controls {
		if control.RequestID == "" || len(control.RequestID) > 128 || seen[control.RequestID] || control.Request.Valid("update-control") != nil {
			return errors.New("pool update control record is invalid")
		}
		seen[control.RequestID] = true
		if control.ReservedResultBytes < 0 || control.ReservedResultBytes > PoolReplicaLimit ||
			(control.Rollout != nil || control.Code != "") && control.ReservedResultBytes != 0 {
			return errors.New("pool update control reservation is invalid")
		}
		if control.RollbackStarted && (control.Request.Control != "rollback_host" || control.Rollout != nil || control.Code != "") {
			return errors.New("pool update rollback intent is invalid")
		}
		if control.ReplicationPending && (control.Request.Control != "abort" && control.Request.Control != "rollback_host" ||
			control.Rollout == nil && control.Code == "") {
			return errors.New("pool update result acknowledgement is invalid")
		}
		if control.Rollout == nil && control.Code == "" || control.ReplicationPending {
			if pending || control.Request.RolloutID != r.ID || control.Rollout == nil && control.Code == "" && control.Message != "" {
				return errors.New("pool update pending control is invalid")
			}
			pending = true
		}
		if control.Rollout != nil {
			if len(control.Rollout.Controls) != 0 || control.Code != "" || control.Rollout.ID != control.Request.RolloutID || control.Rollout.Valid() != nil {
				return errors.New("pool update control result is invalid")
			}
		}
	}
	return nil
}

// PoolUpdateStep is the body of POST /api/v1/pool/updates/step. StepID is deterministic per
// (rollout, host, stage, attempt) and is a valid engine request ID (8 to 64 letters, digits, -
// or _), so a repeated step is the same step and a new attempt is a new one.
type PoolUpdateStep struct {
	RolloutID  string   `json:"rollout_id"`
	StepID     string   `json:"step_id"`
	Action     string   `json:"action"` // check, stage, controller, drain, activate, confirm, undrain, rollback
	Sequence   uint64   `json:"sequence"`
	RunningVMs string   `json:"running_vms,omitempty"`
	VMIDs      []string `json:"vm_ids,omitempty"` // drain: guests to shut down; undrain: guests to start
}

func (s PoolUpdateStep) Valid() error {
	if s.RolloutID == "" || s.StepID == "" || len(s.RolloutID) > 128 || len(s.StepID) > 64 || s.Sequence == 0 || len(s.VMIDs) > 1024 {
		return errors.New("pool update step is invalid")
	}
	switch s.Action {
	case "check", "stage", "controller", "drain", "activate", "confirm", "undrain", "rollback":
		return nil
	}
	return errors.New("pool update step action is invalid")
}

// PoolUpdateStepResult says whether a step is finished. Done=false with no Code means "still
// running; ask again with the same StepID". Code is a stable refusal code.
type PoolUpdateStepResult struct {
	Done    bool   `json:"done"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// PoolUpdateTarget is what a host verified about the release it was asked to install.
type PoolUpdateTarget struct {
	Sequence              uint64 `json:"sequence"`
	StateFormat           int    `json:"state_format"`
	MinControllerAPIEpoch int    `json:"min_controller_api_epoch"`
	MixedPool             bool   `json:"mixed_pool"`
	RequiredBytes         uint64 `json:"required_bytes"`
	// ControllerImage is true when the release is controller_first and carries a Controller image.
	ControllerImage bool `json:"controller_image,omitempty"`
}

// PoolUpdateHostStatus is GET /api/v1/pool/updates/status: this host's own observation.
type PoolUpdateHostStatus struct {
	HostID          string            `json:"host_id"`
	Capabilities    []string          `json:"capabilities,omitempty"`
	Channel         string            `json:"channel,omitempty"`
	Phase           string            `json:"phase"` // the host engine's lifecycle phase
	RunningSequence uint64            `json:"running_sequence"`
	StagedSequence  uint64            `json:"staged_sequence,omitempty"`
	StateFormat     int               `json:"state_format"`
	FreeBytes       uint64            `json:"free_bytes"`
	Target          *PoolUpdateTarget `json:"target,omitempty"`
	RunningVMs      []string          `json:"running_vms,omitempty"`   // VM IDs not shut off
	Busy            bool              `json:"busy,omitempty"`          // a non-terminal local update operation
	Failure         string            `json:"failure,omitempty"`       // stable code of the last host failure
	ControllerVM    bool              `json:"controller_vm,omitempty"` // this host runs a Controller appliance it can swap
	ObservedAt      time.Time         `json:"observed_at"`
}
