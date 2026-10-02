// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"errors"
	"net/http"
	"time"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

// poolRolloutControlTimeout bounds a control request once it has been accepted: abort and
// rollback_host reach every host the rollout touched, so the bound covers a few unreachable ones.
const poolRolloutControlTimeout = 10 * time.Minute

// poolRolloutControlContext is the context a control runs under. An abort restarts the guests the
// rollout stopped and clears maintenance, and a rollback_host reboots a host: a client that
// disconnects or a proxy that gives up must not leave either half done, so the work is not tied
// to the request.
func poolRolloutControlContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), poolRolloutControlTimeout)
}

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
	// Force ends an abort whose cleanup is still pending (abort_cleanup_pending).
	Force bool `json:"force,omitempty"`
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

// poolMasterStore is the agent store of this host when it is enrolled and the pool master, or a
// refusal has been written.
func (s *Service) poolMasterStore(w http.ResponseWriter) *agent.Store {
	store := s.poolUpdateCoordinator(w)
	if store == nil {
		return nil
	}
	membership, err := agent.Membership(store)
	if err != nil || membership.MasterHostID != store.State.HostID {
		poolUpdateRefused(w, &agent.PoolUpdateError{Code: "not_pool_master", Message: "only the pool master runs a rollout"})
		return nil
	}
	return store
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
	store := s.poolMasterStore(w)
	if store == nil {
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
	store := s.poolMasterStore(w)
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
	if body.RequestID == "" || len(body.RequestID) > 128 {
		poolUpdateRefused(w, &agent.PoolUpdateError{Code: "invalid_request", Message: "a rollout control needs a request_id of at most 128 characters"})
		return
	}
	ctx, cancel := poolRolloutControlContext(r)
	defer cancel()
	rollout, err := agent.NewPoolUpdateCoordinator(store).Control(ctx, protocol.PoolUpdateRequest{
		RolloutID: r.PathValue("id"), Control: body.Action, HostID: body.HostID, Force: body.Force})
	if err != nil {
		poolUpdateRefused(w, err)
		return
	}
	reply(w, http.StatusOK, map[string]any{"rollout": rollout})
}
