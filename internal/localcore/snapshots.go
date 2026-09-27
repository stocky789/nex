// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"errors"
	"net/http"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

type createPoolSnapshotRequest struct {
	OperationID string `json:"operation_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

type revertPoolSnapshotRequest struct {
	OperationID string `json:"operation_id"`
}

type deletePoolSnapshotRequest struct {
	OperationID string `json:"operation_id"`
}

type updatePoolSnapshotRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

var errSnapshotNotEnrolled = errors.New("Core is not enrolled")

func (s *Service) createPoolSnapshot(w http.ResponseWriter, r *http.Request) {
	var body createPoolSnapshotRequest
	if !decode(w, r, &body) {
		return
	}
	if !validID.MatchString(r.PathValue("id")) || !protocol.ValidSnapshotOperationID(body.OperationID) ||
		(protocol.VMSnapshotRequest{Name: body.Name, Description: body.Description, Mode: body.Mode}).ValidFor("snapshot-create") != nil {
		fail(w, http.StatusBadRequest, "invalid VM or operation ID")
		return
	}
	request := protocol.Command{
		OperationID: body.OperationID,
		Kind:        "snapshot-create",
		Snapshot:    &protocol.VMSnapshotRequest{Name: body.Name, Description: body.Description, Mode: body.Mode},
	}
	response, err := s.requestSnapshot(r.Context(), agent.SnapshotRequest{VMID: r.PathValue("id"), Command: &request})
	if err != nil {
		writeSnapshotError(w, err)
		return
	}
	if response.Err != nil {
		writeSnapshotError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusServiceUnavailable, "snapshot operation result unavailable")
		return
	}
	reply(w, http.StatusAccepted, response.Result)
}

func (s *Service) poolSnapshots(w http.ResponseWriter, r *http.Request) {
	vmID := r.PathValue("id")
	if !validID.MatchString(vmID) {
		fail(w, http.StatusBadRequest, "invalid VM ID")
		return
	}
	response, err := s.requestSnapshot(r.Context(), agent.SnapshotRequest{VMID: vmID})
	if err != nil {
		writeSnapshotError(w, err)
		return
	}
	if response.Err != nil {
		writeSnapshotError(w, response.Err)
		return
	}
	reply(w, http.StatusOK, append([]protocol.PoolVMSnapshot{}, response.Snapshots...))
}

func (s *Service) poolSnapshotOperation(w http.ResponseWriter, r *http.Request) {
	vmID, operationID := r.PathValue("id"), r.PathValue("operation_id")
	if !validID.MatchString(vmID) || !protocol.ValidSnapshotOperationID(operationID) {
		fail(w, http.StatusBadRequest, "invalid VM or operation ID")
		return
	}
	response, err := s.requestSnapshot(r.Context(), agent.SnapshotRequest{VMID: vmID, OperationID: operationID})
	if err != nil {
		writeSnapshotError(w, err)
		return
	}
	if response.Err != nil {
		writeSnapshotError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusNotFound, "snapshot operation not found")
		return
	}
	reply(w, http.StatusOK, response.Result)
}

func (s *Service) revertPoolSnapshot(w http.ResponseWriter, r *http.Request) {
	vmID, snapshotID := r.PathValue("id"), r.PathValue("snapshot_id")
	var body revertPoolSnapshotRequest
	if !decode(w, r, &body) {
		return
	}
	if !validID.MatchString(vmID) || !protocol.ValidSnapshotOperationID(snapshotID) ||
		!protocol.ValidSnapshotOperationID(body.OperationID) || body.OperationID == snapshotID {
		fail(w, http.StatusBadRequest, "invalid VM, snapshot, or operation ID")
		return
	}
	request := protocol.Command{
		OperationID: body.OperationID,
		Kind:        "snapshot-revert",
		Snapshot:    &protocol.VMSnapshotRequest{SnapshotID: snapshotID},
	}
	response, err := s.requestSnapshot(r.Context(), agent.SnapshotRequest{VMID: vmID, Command: &request})
	if err != nil {
		writeSnapshotError(w, err)
		return
	}
	if response.Err != nil {
		writeSnapshotError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusServiceUnavailable, "snapshot revert operation result unavailable")
		return
	}
	reply(w, http.StatusAccepted, response.Result)
}

func (s *Service) deletePoolSnapshot(w http.ResponseWriter, r *http.Request) {
	vmID, snapshotID := r.PathValue("id"), r.PathValue("snapshot_id")
	var body deletePoolSnapshotRequest
	if !decode(w, r, &body) {
		return
	}
	if !validID.MatchString(vmID) || !protocol.ValidSnapshotOperationID(snapshotID) ||
		!protocol.ValidSnapshotOperationID(body.OperationID) || body.OperationID == snapshotID {
		fail(w, http.StatusBadRequest, "invalid VM, snapshot, or operation ID")
		return
	}
	request := protocol.Command{
		OperationID: body.OperationID,
		Kind:        "snapshot-delete",
		Snapshot:    &protocol.VMSnapshotRequest{SnapshotID: snapshotID},
	}
	response, err := s.requestSnapshot(r.Context(), agent.SnapshotRequest{VMID: vmID, Command: &request})
	if err != nil {
		writeSnapshotError(w, err)
		return
	}
	if response.Err != nil {
		writeSnapshotError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusServiceUnavailable, "snapshot delete operation result unavailable")
		return
	}
	reply(w, http.StatusAccepted, response.Result)
}

func (s *Service) updatePoolSnapshot(w http.ResponseWriter, r *http.Request) {
	vmID, snapshotID := r.PathValue("id"), r.PathValue("snapshot_id")
	var body updatePoolSnapshotRequest
	if !decode(w, r, &body) {
		return
	}
	if !validID.MatchString(vmID) || !protocol.ValidSnapshotOperationID(snapshotID) ||
		(protocol.VMSnapshotRequest{SnapshotID: snapshotID, Name: body.Name, Description: body.Description}).ValidFor("snapshot-metadata") != nil {
		fail(w, http.StatusBadRequest, "invalid VM, snapshot, or metadata")
		return
	}
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	if err == nil && a.hostID != "" {
		startHandoffAgent(s.ctx, s.cfg.AgentStateDir, a)
	}
	if err == nil && a.hostID == "" {
		err = errSnapshotNotEnrolled
	}
	var store *agent.Store
	if err == nil {
		store = a.store
	}
	handoffAgents.Unlock()
	if err != nil {
		writeSnapshotError(w, err)
		return
	}
	updated, err := agent.UpdatePoolVMSnapshotMetadata(r.Context(), store, vmID, snapshotID, body.Name, body.Description)
	if err != nil {
		writeSnapshotError(w, err)
		return
	}
	reply(w, http.StatusOK, updated)
}

func (s *Service) requestSnapshot(ctx context.Context, request agent.SnapshotRequest) (agent.SnapshotResponse, error) {
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	if err == nil && a.hostID != "" {
		startHandoffAgent(s.ctx, s.cfg.AgentStateDir, a)
	}
	if err == nil && a.hostID == "" {
		err = errSnapshotNotEnrolled
	}
	if err != nil {
		handoffAgents.Unlock()
		return agent.SnapshotResponse{}, err
	}
	done, requests := a.done, a.snapshotRequests
	handoffAgents.Unlock()

	request.Reply = make(chan agent.SnapshotResponse, 1)
	select {
	case requests <- request:
	case <-done:
		return agent.SnapshotResponse{}, errors.New("Core agent stopped")
	case <-ctx.Done():
		return agent.SnapshotResponse{}, ctx.Err()
	}
	select {
	case response := <-request.Reply:
		return response, nil
	case <-done:
		return agent.SnapshotResponse{}, errors.New("Core agent stopped")
	case <-ctx.Done():
		return agent.SnapshotResponse{}, ctx.Err()
	}
}

func writeSnapshotError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSnapshotNotEnrolled):
		fail(w, http.StatusConflict, "enroll this Core before using managed VM snapshots")
	case errors.Is(err, agent.ErrSnapshotInvalid):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, agent.ErrSnapshotConflict):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, agent.ErrSnapshotNotFound):
		fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, agent.ErrSnapshotTargetNotFound):
		fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, agent.ErrSnapshotMetadataNotFound):
		fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, agent.ErrSnapshotMetadataConflict), errors.Is(err, agent.ErrSnapshotMetadataPending):
		fail(w, http.StatusConflict, err.Error())
	default:
		fail(w, http.StatusServiceUnavailable, "snapshot operation unavailable")
	}
}
