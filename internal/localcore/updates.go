// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"errors"
	"net/http"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/nexupdate"
)

// newImageUpdateEngine builds the update engine of this host: signed metadata, slots, boot counting.
func (s *Service) newImageUpdateEngine() *nexupdate.Engine {
	if s.cfg.ImageUpdate != nil {
		return s.cfg.ImageUpdate
	}
	cfg := nexupdate.HostConfig()
	cfg.Platform = nexupdate.HostPlatform()
	cfg.Reboot = nexupdate.SystemReboot
	cfg.Appliance = s.engine
	cfg.ControllerProbe = s.controllerProbe
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

func (s *Service) updates(w http.ResponseWriter, r *http.Request) {
	st, err := s.imageUpdate.Status(r.Context())
	if err != nil {
		updateRefused(w, err)
		return
	}
	reply(w, 200, st)
}

func (s *Service) checkUpdates(w http.ResponseWriter, r *http.Request) {
	st, err := s.imageUpdate.Check(r.Context())
	if err != nil {
		updateRefused(w, err)
		return
	}
	reply(w, 200, st)
}

func (s *Service) applyUpdate(w http.ResponseWriter, r *http.Request) {
	var req nexupdate.ApplyRequest
	if !decode(w, r, &req) {
		return
	}
	op, replayed, err := s.imageUpdate.Apply(r.Context(), req)
	replyOperation(w, op, replayed, err)
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

// updateSettings is PUT /updates/settings: the release channel and base URL this host updates from.
func (s *Service) updateSettings(w http.ResponseWriter, r *http.Request) {
	var req nexupdate.Settings
	if !decode(w, r, &req) {
		return
	}
	got, err := s.imageUpdate.UpdateSettings(r.Context(), req)
	if err != nil {
		updateRefused(w, err)
		return
	}
	reply(w, http.StatusOK, got)
}

// updateMedia is GET /updates/media: offline bundles this host can import, read-only.
func (s *Service) updateMedia(w http.ResponseWriter, r *http.Request) {
	media, err := s.imageUpdate.Media(r.Context())
	if err != nil {
		updateRefused(w, err)
		return
	}
	reply(w, http.StatusOK, map[string]any{"media": media})
}

// importUpdate is POST /updates/import: stage a release from media through the same pipeline as apply.
func (s *Service) importUpdate(w http.ResponseWriter, r *http.Request) {
	var req nexupdate.ImportRequest
	if !decode(w, r, &req) {
		return
	}
	op, replayed, err := s.imageUpdate.Import(r.Context(), req)
	replyOperation(w, op, replayed, err)
}

// fetchComponent is POST /updates/components/{id}/fetch: place an on_demand payload under the payload directory.
func (s *Service) fetchComponent(w http.ResponseWriter, r *http.Request) {
	var req nexupdate.FetchRequest
	if !decode(w, r, &req) {
		return
	}
	op, replayed, err := s.imageUpdate.FetchComponent(r.Context(), r.PathValue("id"), req)
	replyOperation(w, op, replayed, err)
}
