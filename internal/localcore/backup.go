// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"errors"
	"net/http"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

type registerBackupRepositoryRequest struct {
	OperationID string                    `json:"operation_id"`
	Repository  protocol.BackupRepository `json:"repository"`
	// ReplaceConfirmation is the existing repository's ID, typed to replace its credential or SFTP host key.
	ReplaceConfirmation string `json:"replace_confirmation,omitempty"`
}

type testBackupRepositoryRequest struct {
	OperationID string `json:"operation_id"`
}

type forgetBackupRepositoryRequest struct {
	OperationID     string `json:"operation_id"`
	Confirmation    string `json:"confirmation"`
	RecoveryPoints  int    `json:"recovery_points"`
	ProtectedPoints int    `json:"protected_points"`
}

type releaseRetainedRequest struct {
	OperationID  string `json:"operation_id"`
	Confirmation string `json:"confirmation"`
}

type orphanCleanupRequest struct {
	OperationID      string   `json:"operation_id"`
	VMIDs            []string `json:"vm_ids"`
	IncludeProtected bool     `json:"include_protected"`
	Confirmation     string   `json:"confirmation"`
	RecoveryPoints   int      `json:"recovery_points"`
	ProtectedPoints  int      `json:"protected_points"`
}

type backupNowRequest struct {
	OperationID  string `json:"operation_id"`
	RepositoryID string `json:"repository_id"`
	Consistency  string `json:"consistency,omitempty"`
}

type restoreAsNewRequest struct {
	OperationID     string `json:"operation_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	NewVMID         string `json:"new_vm_id"`
	NewVMName       string `json:"new_vm_name"`
	StorageID       string `json:"storage_id"`
}

var errBackupNotEnrolled = errors.New("Core is not enrolled")

func (s *Service) listBackupRepositories(w http.ResponseWriter, r *http.Request) {
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{List: "repositories"})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	reply(w, http.StatusOK, append([]protocol.BackupRepository{}, response.Repositories...))
}

func (s *Service) registerBackupRepository(w http.ResponseWriter, r *http.Request) {
	var body registerBackupRepositoryRequest
	if !decode(w, r, &body) {
		return
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || body.Repository.Valid() != nil ||
		body.ReplaceConfirmation != "" && body.ReplaceConfirmation != body.Repository.ID {
		fail(w, http.StatusBadRequest, "invalid backup repository request")
		return
	}
	request := protocol.Command{
		OperationID: body.OperationID, Kind: "backup-repository-register", BackupRepository: &body.Repository,
		ReplaceRepository: body.ReplaceConfirmation,
	}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusServiceUnavailable, "backup repository result unavailable")
		return
	}
	if response.Repository == nil {
		reply(w, http.StatusOK, response.Result)
		return
	}
	reply(w, http.StatusOK, response.Repository)
}

// releaseRetainedOriginals deletes the disks and vTPM state restore replace kept beside a VM on this host.
func (s *Service) releaseRetainedOriginals(w http.ResponseWriter, r *http.Request) {
	var body releaseRetainedRequest
	if !decode(w, r, &body) {
		return
	}
	// The agent fills in this host and pool.
	req := protocol.VMReleaseRetainedRequest{VMID: r.PathValue("id"), PoolID: "-", HostID: "-", Confirmation: body.Confirmation}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid release retained originals request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: "vm-release-retained", ReleaseRetained: &req}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusServiceUnavailable, "release result unavailable")
		return
	}
	reply(w, http.StatusAccepted, response.Result)
}

func (s *Service) testBackupRepository(w http.ResponseWriter, r *http.Request) {
	var body testBackupRepositoryRequest
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if !protocol.ValidSnapshotOperationID(id) || !protocol.ValidSnapshotOperationID(body.OperationID) {
		fail(w, http.StatusBadRequest, "invalid backup repository or operation ID")
		return
	}
	request := protocol.Command{
		OperationID: body.OperationID, Kind: "backup-repository-test",
		BackupRepository: &protocol.BackupRepository{ID: id},
	}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Repository == nil {
		if response.Result != nil {
			reply(w, http.StatusOK, response.Result)
			return
		}
		fail(w, http.StatusServiceUnavailable, "backup repository result unavailable")
		return
	}
	reply(w, http.StatusOK, response.Repository)
}

func (s *Service) forgetBackupRepository(w http.ResponseWriter, r *http.Request) {
	var body forgetBackupRepositoryRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.ForgetRepositoryRequest{
		RepositoryID: r.PathValue("id"), Confirmation: body.Confirmation,
		RecoveryPoints: body.RecoveryPoints, ProtectedPoints: body.ProtectedPoints,
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid forget backup repository request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: "backup-repository-forget", ForgetRepository: &req}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	reply(w, http.StatusOK, response.Result)
}

func (s *Service) listBackupOrphans(w http.ResponseWriter, r *http.Request) {
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{List: "orphans", RepositoryID: r.PathValue("id")})
	if err == nil {
		err = response.Err
	}
	if err != nil {
		writeBackupError(w, err)
		return
	}
	reply(w, http.StatusOK, response.Orphans)
}

func (s *Service) orphanCleanupResult(w http.ResponseWriter, r *http.Request) {
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{List: "orphan-cleanup", OperationID: r.PathValue("operation_id")})
	if err == nil {
		err = response.Err
	}
	if err != nil {
		writeBackupError(w, err)
		return
	}
	reply(w, http.StatusOK, response.Cleanup)
}

func (s *Service) cleanupOrphanedBackups(w http.ResponseWriter, r *http.Request) {
	var body orphanCleanupRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.OrphanCleanupRequest{
		RepositoryID: r.PathValue("id"), VMIDs: body.VMIDs, IncludeProtected: body.IncludeProtected,
		Confirmation: body.Confirmation, RecoveryPoints: body.RecoveryPoints, ProtectedPoints: body.ProtectedPoints,
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid orphaned backup cleanup request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: "backup-orphan-cleanup", OrphanCleanup: &req}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err == nil {
		err = response.Err
	}
	if err != nil {
		writeBackupError(w, err)
		return
	}
	reply(w, http.StatusOK, response.Result)
}

func (s *Service) listBackupCatalog(w http.ResponseWriter, r *http.Request) {
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{List: "catalog"})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	reply(w, http.StatusOK, append([]protocol.BackupRecoveryPoint{}, response.Catalog...))
}

func (s *Service) listBackupRuns(w http.ResponseWriter, r *http.Request) {
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{List: "runs"})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	reply(w, http.StatusOK, append([]protocol.BackupRun{}, response.Runs...))
}

func (s *Service) listBackupPolicies(w http.ResponseWriter, r *http.Request) {
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{List: "policies"})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	reply(w, http.StatusOK, append([]protocol.BackupPolicy{}, response.Policies...))
}

type upsertBackupPolicyLocalRequest struct {
	OperationID string                `json:"operation_id"`
	Policy      protocol.BackupPolicy `json:"policy"`
}

type pauseBackupPolicyLocalRequest struct {
	OperationID string `json:"operation_id"`
	Paused      bool   `json:"paused"`
}

type runNowBackupPolicyLocalRequest struct {
	OperationID string `json:"operation_id"`
	VMID        string `json:"vm_id"`
}

func (s *Service) upsertBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var body upsertBackupPolicyLocalRequest
	if !decode(w, r, &body) {
		return
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || body.Policy.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid backup policy request")
		return
	}
	request := protocol.Command{
		OperationID: body.OperationID, Kind: "backup-policy-upsert", BackupPolicy: &body.Policy,
	}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Policy == nil {
		if response.Result != nil {
			reply(w, http.StatusOK, response.Result)
			return
		}
		fail(w, http.StatusServiceUnavailable, "backup policy result unavailable")
		return
	}
	reply(w, http.StatusOK, response.Policy)
}

func (s *Service) pauseBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var body pauseBackupPolicyLocalRequest
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if !protocol.ValidSnapshotOperationID(id) || !protocol.ValidSnapshotOperationID(body.OperationID) {
		fail(w, http.StatusBadRequest, "invalid backup policy or operation ID")
		return
	}
	request := protocol.Command{
		OperationID: body.OperationID, Kind: "backup-policy-pause",
		BackupPolicy: &protocol.BackupPolicy{ID: id, Paused: body.Paused},
	}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Policy == nil {
		if response.Result != nil {
			reply(w, http.StatusOK, response.Result)
			return
		}
		fail(w, http.StatusServiceUnavailable, "backup policy result unavailable")
		return
	}
	reply(w, http.StatusOK, response.Policy)
}

func (s *Service) deleteBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OperationID  string `json:"operation_id"`
		Confirmation string `json:"confirmation"`
	}
	if !decode(w, r, &body) {
		return
	}
	req := protocol.DeleteBackupPolicyRequest{PolicyID: r.PathValue("id"), Confirmation: body.Confirmation}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil || body.Confirmation == "" {
		fail(w, http.StatusBadRequest, "invalid delete backup policy request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: "backup-policy-delete", PolicyDelete: &req}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	reply(w, http.StatusOK, response.Result)
}

func (s *Service) runNowBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var body runNowBackupPolicyLocalRequest
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if !protocol.ValidSnapshotOperationID(id) || !protocol.ValidSnapshotOperationID(body.OperationID) ||
		body.VMID == "" || len(body.VMID) > 128 {
		fail(w, http.StatusBadRequest, "invalid backup policy run-now request")
		return
	}
	request := protocol.Command{
		OperationID: body.OperationID, Kind: "backup-policy-run-now",
		BackupPolicy: &protocol.BackupPolicy{ID: id},
		VM:           &protocol.VMCreateSpec{ID: body.VMID},
	}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusServiceUnavailable, "backup policy run-now result unavailable")
		return
	}
	reply(w, http.StatusAccepted, response.Result)
}

func (s *Service) backupNow(w http.ResponseWriter, r *http.Request) {
	var body backupNowRequest
	if !decode(w, r, &body) {
		return
	}
	vmID := r.PathValue("id")
	if !validID.MatchString(vmID) || !protocol.ValidSnapshotOperationID(body.OperationID) ||
		(protocol.BackupNowRequest{RepositoryID: body.RepositoryID, Consistency: body.Consistency}).Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid VM or backup-now request")
		return
	}
	request := protocol.Command{
		OperationID: body.OperationID, Kind: "backup-now",
		VM:     &protocol.VMCreateSpec{ID: vmID},
		Backup: &protocol.BackupNowRequest{RepositoryID: body.RepositoryID, Consistency: body.Consistency},
	}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusServiceUnavailable, "backup operation result unavailable")
		return
	}
	reply(w, http.StatusAccepted, response.Result)
}

func (s *Service) restoreBackupAsNew(w http.ResponseWriter, r *http.Request) {
	var body restoreAsNewRequest
	if !decode(w, r, &body) {
		return
	}
	restore := protocol.RestoreAsNewRequest{
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID,
		NewVMID: body.NewVMID, NewVMName: body.NewVMName, StorageID: body.StorageID,
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || restore.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid restore-as-new request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: "backup-restore-as-new", Restore: &restore}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusServiceUnavailable, "restore operation result unavailable")
		return
	}
	reply(w, http.StatusAccepted, response.Result)
}

func (s *Service) backupOperation(w http.ResponseWriter, r *http.Request) {
	operationID := r.PathValue("operation_id")
	if !protocol.ValidSnapshotOperationID(operationID) {
		fail(w, http.StatusBadRequest, "invalid operation ID")
		return
	}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{OperationID: operationID})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Result == nil {
		fail(w, http.StatusNotFound, "backup operation not found")
		return
	}
	reply(w, http.StatusOK, response.Result)
}

func (s *Service) requestBackup(ctx context.Context, request agent.BackupRequest) (agent.BackupResponse, error) {
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	if err == nil && a.hostID != "" {
		startHandoffAgent(s.ctx, s.cfg.AgentStateDir, a)
	}
	if err == nil && a.hostID == "" {
		err = errBackupNotEnrolled
	}
	if err != nil {
		handoffAgents.Unlock()
		return agent.BackupResponse{}, err
	}
	done, requests, hostID := a.done, a.backupRequests, a.hostID
	handoffAgents.Unlock()
	if request.Command != nil {
		cmd := request.Command
		if cmd.Kind == "backup-now" && cmd.VM != nil {
			cmd.VM.HostID = hostID
		}
		if cmd.Restore != nil {
			cmd.Restore.HostID = hostID
		}
		if cmd.RestoreReplace != nil {
			cmd.RestoreReplace.HostID = hostID
		}
		if cmd.TestRestore != nil {
			cmd.TestRestore.HostID = hostID
		}
	}

	request.Reply = make(chan agent.BackupResponse, 1)
	select {
	case requests <- request:
	case <-done:
		return agent.BackupResponse{}, errors.New("Core agent stopped")
	case <-ctx.Done():
		return agent.BackupResponse{}, ctx.Err()
	}
	select {
	case response := <-request.Reply:
		return response, nil
	case <-done:
		return agent.BackupResponse{}, errors.New("Core agent stopped")
	case <-ctx.Done():
		return agent.BackupResponse{}, ctx.Err()
	}
}

func writeBackupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBackupNotEnrolled):
		fail(w, http.StatusConflict, "enroll this Core before using backup repositories")
	case errors.Is(err, agent.ErrBackupInvalid):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, agent.ErrBackupConflict):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, agent.ErrBackupNotFound), errors.Is(err, agent.ErrBackupRepoNotFound), errors.Is(err, agent.ErrBackupPolicyNotFound):
		fail(w, http.StatusNotFound, err.Error())
	default:
		fail(w, http.StatusServiceUnavailable, "backup operation unavailable")
	}
}
