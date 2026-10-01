// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"errors"
	"net/http"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/nexupdate"
)

// The image edition updates through nexupdate.Engine (signed metadata, slots, boot counting); the
// classic edition keeps its apt path until it is retired. Edition decides once, at Open: a host never
// switches between them while running.

// newImageUpdateEngine builds the engine of an image-edition host, or returns nil on a classic host.
func (s *Service) newImageUpdateEngine() *nexupdate.Engine {
	if s.cfg.ImageUpdate != nil {
		return s.cfg.ImageUpdate
	}
	if nexupdate.Edition() != "image" {
		return nil
	}
	cfg := nexupdate.HostConfig()
	cfg.Platform = nexupdate.HostPlatform()
	cfg.Reboot = nexupdate.SystemReboot
	// A pool member prefers the master's blob cache; every blob is still verified against the
	// signed manifest, and a master that lacks it, or a host with no pool, reads the channel.
	origin := cfg.Source
	if origin == nil {
		origin = func(base string) nexupdate.Source { return nexupdate.HTTPSource{Base: base} }
	}
	cfg.Source = func(base string) nexupdate.Source {
		return nexupdate.PeerFirstSource{Peer: agent.PoolUpdatePeerSource(s.agentStore), Origin: origin(base)}
	}
	return nexupdate.NewEngine(cfg)
}

// agentStore is the enrolled agent's store, or nil before this host is enrolled.
func (s *Service) agentStore() *agent.Store {
	handoffAgents.Lock()
	defer handoffAgents.Unlock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	if err != nil || a.store.State.HostID == "" {
		return nil
	}
	return a.store
}

// updateRefused answers an engine refusal with its stable code and literal message.
func updateRefused(w http.ResponseWriter, err error) {
	var re *nexupdate.Error
	if !errors.As(err, &re) {
		reply(w, http.StatusInternalServerError, map[string]string{"code": "internal", "error": "the update engine failed"})
		return
	}
	if re.Status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}
	status := re.Status
	if status < 400 {
		status = http.StatusConflict
	}
	reply(w, status, map[string]string{"code": re.Code, "error": re.Message})
}

func notImageEdition(w http.ResponseWriter) {
	updateRefused(w, &nexupdate.Error{Status: http.StatusConflict, Code: "unsupported_edition", Message: "this host does not run the image edition"})
}

func (s *Service) updates(w http.ResponseWriter, r *http.Request) {
	if s.imageUpdate != nil {
		st, err := s.imageUpdate.Status(r.Context())
		if err != nil {
			updateRefused(w, err)
			return
		}
		reply(w, 200, st)
		return
	}
	st, err := nexupdate.Check(r.Context(), nexupdate.DefaultRepoURL(), "nex-core-local")
	if err != nil {
		fail(w, 500, "update check failed")
		return
	}
	nexupdate.MergeProgress(&st, s.cfg.DataDir)
	reply(w, 200, st)
}

func (s *Service) checkUpdates(w http.ResponseWriter, r *http.Request) {
	if s.imageUpdate == nil {
		notImageEdition(w)
		return
	}
	st, err := s.imageUpdate.Check(r.Context())
	if err != nil {
		updateRefused(w, err)
		return
	}
	reply(w, 200, st)
}

func (s *Service) applyUpdate(w http.ResponseWriter, r *http.Request) {
	if s.imageUpdate != nil {
		var req nexupdate.ApplyRequest
		if !decode(w, r, &req) {
			return
		}
		op, replayed, err := s.imageUpdate.Apply(r.Context(), req)
		replyOperation(w, op, replayed, err)
		return
	}
	result, err := nexupdate.StartApplyLocal(nexupdate.DefaultRepoURL(), "nex-core-local", s.cfg.DataDir)
	status := 202
	if result.Status == "failed" || err != nil {
		status = 409
		if result.Message == "" && err != nil {
			result = nexupdate.ApplyResult{Status: "failed", Message: err.Error()}
		}
	}
	reply(w, status, result)
}

// replyOperation answers a mutation: 202 for work started or armed, 200 for the exact replay of an
// earlier request.
func replyOperation(w http.ResponseWriter, op nexupdate.Operation, replayed bool, err error) {
	replyOperationAs(w, op, replayed, err, http.StatusAccepted)
}

func replyOperationAs(w http.ResponseWriter, op nexupdate.Operation, replayed bool, err error, started int) {
	if err != nil {
		updateRefused(w, err)
		return
	}
	status := started
	if replayed {
		status = http.StatusOK
	}
	reply(w, status, op)
}

func (s *Service) updateAction(started int, call func(*nexupdate.Engine, context.Context, nexupdate.ActionRequest) (nexupdate.Operation, bool, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.imageUpdate == nil {
			notImageEdition(w)
			return
		}
		var req nexupdate.ActionRequest
		if !decode(w, r, &req) {
			return
		}
		op, replayed, err := call(s.imageUpdate, r.Context(), req)
		replyOperationAs(w, op, replayed, err, started)
	}
}

func (s *Service) activateUpdate() http.HandlerFunc {
	return s.updateAction(http.StatusAccepted, (*nexupdate.Engine).Activate)
}
func (s *Service) rollbackUpdate() http.HandlerFunc {
	return s.updateAction(http.StatusAccepted, (*nexupdate.Engine).Rollback)
}
func (s *Service) cancelUpdate() http.HandlerFunc {
	return s.updateAction(http.StatusOK, (*nexupdate.Engine).Cancel)
}

// updateOperation is GET /updates/operations/{id}: one recorded operation of the engine (the last 20).
func (s *Service) updateOperation(w http.ResponseWriter, r *http.Request) {
	if s.imageUpdate == nil {
		notImageEdition(w)
		return
	}
	op, found, err := s.imageUpdate.Operation(r.PathValue("id"))
	if err != nil {
		updateRefused(w, err)
		return
	}
	if !found {
		updateRefused(w, &nexupdate.Error{Status: http.StatusNotFound, Code: "unknown_operation", Message: "no such update operation"})
		return
	}
	reply(w, http.StatusOK, op)
}
