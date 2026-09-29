// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"net/http"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

type restoreReplaceLocalRequest struct {
	OperationID     string `json:"operation_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	OriginalVMID    string `json:"original_vm_id"`
	StorageID       string `json:"storage_id"`
	ConfirmReplace  bool   `json:"confirm_replace"`
}

type protectPointLocalRequest struct {
	OperationID     string `json:"operation_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	Protected       bool   `json:"protected"`
}

type testRestoreLocalRequest struct {
	OperationID     string `json:"operation_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	NewVMID         string `json:"new_vm_id"`
	NewVMName       string `json:"new_vm_name"`
	StorageID       string `json:"storage_id"`
}

type testRestoreVerifyLocalRequest struct {
	OperationID     string `json:"operation_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	TestVMID        string `json:"test_vm_id"`
	Result          string `json:"result"`
}

type testRestoreCleanupLocalRequest struct {
	OperationID     string `json:"operation_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	TestVMID        string `json:"test_vm_id"`
}

type catalogImportLocalRequest struct {
	OperationID  string `json:"operation_id"`
	RepositoryID string `json:"repository_id"`
}

func (s *Service) restoreBackupReplace(w http.ResponseWriter, r *http.Request) {
	var body restoreReplaceLocalRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.RestoreReplaceRequest{
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID,
		OriginalVMID: body.OriginalVMID, StorageID: body.StorageID, ConfirmReplace: body.ConfirmReplace,
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid restore-replace request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: protocol.BackupKindRestoreReplace, RestoreReplace: &req}
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
		fail(w, http.StatusServiceUnavailable, "restore-replace result unavailable")
		return
	}
	reply(w, http.StatusAccepted, response.Result)
}

func (s *Service) protectBackupPoint(w http.ResponseWriter, r *http.Request) {
	var body protectPointLocalRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.ProtectRecoveryPointRequest{
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID, Protected: body.Protected,
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid protect request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: "backup-protect", BackupProtect: &req}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if len(response.Catalog) == 0 {
		if response.Result != nil {
			reply(w, http.StatusOK, response.Result)
			return
		}
		fail(w, http.StatusServiceUnavailable, "protect result unavailable")
		return
	}
	reply(w, http.StatusOK, response.Catalog[0])
}

func (s *Service) testRestoreBackup(w http.ResponseWriter, r *http.Request) {
	var body testRestoreLocalRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.TestRestoreRequest{
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID,
		NewVMID: body.NewVMID, NewVMName: body.NewVMName, StorageID: body.StorageID,
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid test-restore request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: protocol.BackupKindTestRestore, TestRestore: &req}
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
		fail(w, http.StatusServiceUnavailable, "test-restore result unavailable")
		return
	}
	reply(w, http.StatusAccepted, response.Result)
}

func (s *Service) verifyTestRestore(w http.ResponseWriter, r *http.Request) {
	var body testRestoreVerifyLocalRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.TestRestoreVerifyRequest{
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID,
		TestVMID: body.TestVMID, Result: body.Result,
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid test-restore verify request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: "backup-test-verify", TestRestoreVerify: &req}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if len(response.Catalog) == 0 {
		if response.Result != nil {
			reply(w, http.StatusOK, response.Result)
			return
		}
		fail(w, http.StatusServiceUnavailable, "test-restore verify result unavailable")
		return
	}
	reply(w, http.StatusOK, response.Catalog[0])
}

func (s *Service) cleanupTestRestore(w http.ResponseWriter, r *http.Request) {
	var body testRestoreCleanupLocalRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.TestRestoreCleanupRequest{
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID, TestVMID: body.TestVMID,
	}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid test-restore cleanup request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: "backup-test-cleanup", TestRestoreClean: &req}
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

func (s *Service) importBackupCatalog(w http.ResponseWriter, r *http.Request) {
	s.catalogImportOrRebuild(w, r, "backup-catalog-import")
}

func (s *Service) rebuildBackupCatalog(w http.ResponseWriter, r *http.Request) {
	s.catalogImportOrRebuild(w, r, "backup-catalog-rebuild")
}

func (s *Service) catalogImportOrRebuild(w http.ResponseWriter, r *http.Request, kind string) {
	var body catalogImportLocalRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.CatalogImportRequest{RepositoryID: body.RepositoryID}
	if !protocol.ValidSnapshotOperationID(body.OperationID) || req.Valid() != nil {
		fail(w, http.StatusBadRequest, "invalid catalog import request")
		return
	}
	request := protocol.Command{OperationID: body.OperationID, Kind: kind, CatalogImport: &req}
	response, err := s.requestBackup(r.Context(), agent.BackupRequest{Command: &request})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if response.Err != nil {
		writeBackupError(w, response.Err)
		return
	}
	if response.Catalog == nil && response.Result != nil {
		status := http.StatusOK
		if response.Result.Status == "pending" {
			status = http.StatusAccepted
		}
		reply(w, status, response.Result)
		return
	}
	reply(w, http.StatusOK, append([]protocol.BackupRecoveryPoint{}, response.Catalog...))
}

func (s *Service) backupStatus(w http.ResponseWriter, r *http.Request) {
	repos, err := s.requestBackup(r.Context(), agent.BackupRequest{List: "repositories"})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if repos.Err != nil {
		writeBackupError(w, repos.Err)
		return
	}
	catalog, err := s.requestBackup(r.Context(), agent.BackupRequest{List: "catalog"})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if catalog.Err != nil {
		writeBackupError(w, catalog.Err)
		return
	}
	runs, err := s.requestBackup(r.Context(), agent.BackupRequest{List: "runs"})
	if err != nil {
		writeBackupError(w, err)
		return
	}
	if runs.Err != nil {
		writeBackupError(w, runs.Err)
		return
	}
	reply(w, http.StatusOK, map[string]any{
		"repositories": append([]protocol.BackupRepository{}, repos.Repositories...),
		"catalog":      append([]protocol.BackupRecoveryPoint{}, catalog.Catalog...),
		"runs":         append([]protocol.BackupRun{}, runs.Runs...),
	})
}
