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

// Rollout states. A rollout is "running" or "paused" until it ends as "finalized" or "aborted".
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
)

// PoolUpdateRequest is the body of the pool-update command. "update-rollout" starts (or, with
// DryRun, only checks) a rollout; "update-control" carries Control for an existing one.
type PoolUpdateRequest struct {
	Sequence   uint64 `json:"sequence,omitempty"`
	RunningVMs string `json:"running_vms,omitempty"` // refuse (default), shutdown; evacuate is refused
	DryRun     bool   `json:"dry_run,omitempty"`
	RolloutID  string `json:"rollout_id,omitempty"`
	Control    string `json:"control,omitempty"` // pause, resume, abort, rollback_host
	HostID     string `json:"host_id,omitempty"`
}

func (r PoolUpdateRequest) Valid(action string) error {
	if len(r.RolloutID) > 128 || len(r.HostID) > 128 {
		return errors.New("pool update identity is too long")
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
	// Stage runs: pending, staged, gate, maintenance, drain, activate, boot, confirm, release,
	// restart, soak, done.
	Stage string `json:"stage"`
	// StoppedByUpdate lists the guests this rollout shut down on this host. It is written before
	// the shutdown is requested, so a crash can never lose which guests to restart.
	StoppedByUpdate []string  `json:"stopped_by_update,omitempty"`
	StepStartedAt   time.Time `json:"step_started_at"`
	DeadlineAt      time.Time `json:"deadline_at"`
	SoakUntil       time.Time `json:"soak_until"`
	Error           string    `json:"error,omitempty"`
	// Attempt counts how often the rollout has sent this host through staging again (a resume
	// after a rollback). It is part of every step identity, so a new attempt is never a replay
	// of the engine operations the earlier one left behind.
	Attempt int `json:"attempt,omitempty"`
}

type PoolUpdatePause struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// PoolUpdateRollout is the pool master's durable rollout record (pool-update.json), replicated as
// the authority section update_rollout. A promoted master finds it paused master_changed.
type PoolUpdateRollout struct {
	ID                   string           `json:"id"`
	PoolID               string           `json:"pool_id"`
	Sequence             uint64           `json:"sequence"`
	RunningVMs           string           `json:"running_vms"`
	State                string           `json:"state"`
	Phase                string           `json:"phase"` // staging, hosts, finalize
	Pause                *PoolUpdatePause `json:"pause,omitempty"`
	PauseRequested       bool             `json:"pause_requested,omitempty"`
	MembershipGeneration uint64           `json:"membership_generation"`
	Hosts                []PoolUpdateHost `json:"hosts"`
	FinalizedSequence    uint64           `json:"finalized_sequence,omitempty"`
	CreatedAt            time.Time        `json:"created_at"`
	UpdatedAt            time.Time        `json:"updated_at"`
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
	return nil
}

// PoolUpdateStep is the body of POST /api/v1/pool/updates/step. StepID is deterministic per
// (rollout, host, stage, attempt) and is a valid engine request ID (8 to 64 letters, digits, -
// or _), so a repeated step is the same step and a new attempt is a new one.
type PoolUpdateStep struct {
	RolloutID  string   `json:"rollout_id"`
	StepID     string   `json:"step_id"`
	Action     string   `json:"action"` // check, stage, drain, activate, confirm, undrain, rollback
	Sequence   uint64   `json:"sequence"`
	RunningVMs string   `json:"running_vms,omitempty"`
	VMIDs      []string `json:"vm_ids,omitempty"` // drain: guests to shut down; undrain: guests to start
}

func (s PoolUpdateStep) Valid() error {
	if s.RolloutID == "" || s.StepID == "" || len(s.RolloutID) > 128 || len(s.StepID) > 64 || s.Sequence == 0 || len(s.VMIDs) > 1024 {
		return errors.New("pool update step is invalid")
	}
	switch s.Action {
	case "check", "stage", "drain", "activate", "confirm", "undrain", "rollback":
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
	RunningVMs      []string          `json:"running_vms,omitempty"` // VM IDs not shut off
	Busy            bool              `json:"busy,omitempty"`        // a non-terminal local update operation
	Failure         string            `json:"failure,omitempty"`     // stable code of the last host failure
	ObservedAt      time.Time         `json:"observed_at"`
}
