// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"errors"
	"net/http"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

// The pool rollout routes. They exist on the pool master only: the rollout is the master's own
// durable job (pool-update.json), driven by its coordinator with no Controller connected. Each
// route calls the same coordinator the relayed pool-update command calls.

type poolRolloutRequest struct {
	RequestID  string `json:"request_id"`
	Sequence   uint64 `json:"sequence"`
	RunningVMs string `json:"running_vms,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`
}

type poolRolloutControl struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	HostID    string `json:"host_id,omitempty"`
}

func poolUpdateRefused(w http.ResponseWriter, err error) {
	var refusal *agent.PoolUpdateError
	if !errors.As(err, &refusal) {
		reply(w, http.StatusInternalServerError, map[string]string{"code": "internal", "error": "the rollout coordinator failed"})
		return
	}
	status := http.StatusConflict
	switch refusal.Code {
	case "invalid_request":
		status = http.StatusBadRequest
	case "rollout_not_found":
		status = http.StatusNotFound
	}
	reply(w, status, map[string]string{"code": refusal.Code, "error": refusal.Message})
}

func (s *Service) poolUpdateCoordinator(w http.ResponseWriter) *agent.Store {
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil || a.store.State.HostID == "" {
		fail(w, http.StatusConflict, "enroll this Core before updating its pool")
		return nil
	}
	return a.store
}

func (s *Service) poolUpdates(w http.ResponseWriter, r *http.Request) {
	store := s.poolUpdateCoordinator(w)
	if store == nil {
		return
	}
	membership, err := agent.Membership(store)
	if err != nil || membership.MasterHostID != store.State.HostID {
		poolUpdateRefused(w, &agent.PoolUpdateError{Code: "not_pool_master", Message: "only the pool master runs a rollout"})
		return
	}
	rollout, err := agent.LoadPoolUpdate(store)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "the rollout record is unavailable")
		return
	}
	reply(w, http.StatusOK, map[string]any{"rollout": rollout})
}

func (s *Service) poolUpdateStart(w http.ResponseWriter, r *http.Request) {
	store := s.poolUpdateCoordinator(w)
	if store == nil {
		return
	}
	var body poolRolloutRequest
	if !decode(w, r, &body) {
		return
	}
	rollout, plan, err := agent.NewPoolUpdateCoordinator(store).Start(r.Context(), body.RequestID, protocol.PoolUpdateRequest{
		Sequence: body.Sequence, RunningVMs: body.RunningVMs, DryRun: body.DryRun})
	if err != nil {
		poolUpdateRefused(w, err)
		return
	}
	if body.DryRun {
		reply(w, http.StatusOK, map[string]any{"dry_run": true, "plan": plan})
		return
	}
	reply(w, http.StatusAccepted, map[string]any{"rollout": rollout})
}

func (s *Service) poolUpdateRollout(w http.ResponseWriter, r *http.Request) {
	store := s.poolUpdateCoordinator(w)
	if store == nil {
		return
	}
	rollout, err := agent.LoadPoolUpdate(store)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "the rollout record is unavailable")
		return
	}
	if rollout == nil || rollout.ID != r.PathValue("id") {
		poolUpdateRefused(w, &agent.PoolUpdateError{Code: "rollout_not_found", Message: "no such rollout"})
		return
	}
	reply(w, http.StatusOK, map[string]any{"rollout": rollout})
}

func (s *Service) poolUpdateControl(w http.ResponseWriter, r *http.Request) {
	store := s.poolUpdateCoordinator(w)
	if store == nil {
		return
	}
	var body poolRolloutControl
	if !decode(w, r, &body) {
		return
	}
	rollout, err := agent.NewPoolUpdateCoordinator(store).Control(r.Context(), protocol.PoolUpdateRequest{
		RolloutID: r.PathValue("id"), Control: body.Action, HostID: body.HostID})
	if err != nil {
		poolUpdateRefused(w, err)
		return
	}
	reply(w, http.StatusOK, map[string]any{"rollout": rollout})
}
