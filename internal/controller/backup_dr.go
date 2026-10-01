// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http"
	"strings"

	"nex.local/nex/internal/protocol"
)

type restoreReplaceAPIRequest struct {
	RequestID       string `json:"request_id"`
	HostID          string `json:"host_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	OriginalVMID    string `json:"original_vm_id"`
	StorageID       string `json:"storage_id"`
	ConfirmReplace  bool   `json:"confirm_replace"`
}

type protectPointAPIRequest struct {
	RequestID       string `json:"request_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	Protected       bool   `json:"protected"`
}

type testRestoreAPIRequest struct {
	RequestID       string `json:"request_id"`
	HostID          string `json:"host_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	NewVMID         string `json:"new_vm_id"`
	NewVMName       string `json:"new_vm_name"`
	StorageID       string `json:"storage_id"`
}

type testRestoreVerifyAPIRequest struct {
	RequestID       string `json:"request_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	TestVMID        string `json:"test_vm_id"`
	Result          string `json:"result"`
}

type testRestoreCleanupAPIRequest struct {
	RequestID       string `json:"request_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	TestVMID        string `json:"test_vm_id"`
}

type catalogImportAPIRequest struct {
	RequestID    string `json:"request_id"`
	RepositoryID string `json:"repository_id"`
}

func (c *Controller) restoreBackupReplace(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body restoreReplaceAPIRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.RestoreReplaceRequest{
		HostID:       body.HostID,
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID,
		OriginalVMID: body.OriginalVMID, StorageID: body.StorageID, ConfirmReplace: body.ConfirmReplace,
	}
	if !nameValid(pool) || !protocol.ValidSnapshotOperationID(body.RequestID) || !nameValid(body.HostID) ||
		req.Valid() != nil || body.RequestID == body.RecoveryPointID {
		fail(w, 400, "stable request_id, host_id and restore-replace fields required")
		return
	}
	c.enqueueRestoreReplace(w, pool, body.HostID, user, body.RequestID, &req, body)
}

func (c *Controller) enqueueRestoreReplace(w http.ResponseWriter, pool, host, user, requestID string, restore *protocol.RestoreReplaceRequest, payload any) {
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, requestID, protocol.BackupKindRestoreReplace, payload, pool) {
		return
	}
	master, err := c.backupMasterHost(tx, pool, host)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.name=? AND o.status IN ('queued','dispatched') AND r.kind IN ('vm-create','vm-update','vm-power','vm-media','vm-delete','snapshot-create','snapshot-revert','snapshot-delete','snapshot-metadata','backup-now','backup-restore-as-new','backup-restore-replace','backup-test-restore')", restore.OriginalVMID).Scan(&pending); err != nil {
		fail(w, 500, "pending request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a change or recovery request for this VM is still pending; wait for its task")
		return
	}
	var held int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources WHERE id=? OR operation_id=?", requestID, requestID).Scan(&held); err != nil {
		fail(w, 500, "backup identity lookup failed")
		return
	}
	if held > 0 {
		fail(w, 409, "request_id already used for another operation")
		return
	}
	if _, err = c.readyStorageForHost(tx, restore.StorageID, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	rowID := "backup:" + requestID
	cmd := protocol.Command{OperationID: requestID, Kind: protocol.BackupKindRestoreReplace, RestoreReplace: restore}
	op, err := c.saveResource(tx, user, requestID, pool, master, restore.OriginalVMID, rowID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "restore-replace request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}

func (c *Controller) protectBackupPoint(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body protectPointAPIRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.ProtectRecoveryPointRequest{
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID, Protected: body.Protected,
	}
	if !nameValid(pool) || !protocol.ValidSnapshotOperationID(body.RequestID) || req.Valid() != nil {
		fail(w, 400, "stable request_id and protect fields required")
		return
	}
	c.enqueueBackupMasterSync(w, pool, user, body.RequestID, "backup-protect", protocol.Command{
		OperationID: body.RequestID, Kind: "backup-protect", BackupProtect: &req,
	}, body, body.RecoveryPointID)
}

func (c *Controller) testRestoreBackup(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body testRestoreAPIRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.TestRestoreRequest{
		HostID:       body.HostID,
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID,
		NewVMID: body.NewVMID, NewVMName: strings.TrimSpace(body.NewVMName), StorageID: body.StorageID,
	}
	if !nameValid(pool) || !protocol.ValidSnapshotOperationID(body.RequestID) || !nameValid(body.HostID) ||
		req.Valid() != nil || body.RequestID == body.RecoveryPointID {
		fail(w, 400, "stable request_id, host_id and test-restore fields required")
		return
	}
	c.enqueueTestRestore(w, pool, body.HostID, user, body.RequestID, &req, body)
}

func (c *Controller) enqueueTestRestore(w http.ResponseWriter, pool, host, user, requestID string, restore *protocol.TestRestoreRequest, payload any) {
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, requestID, protocol.BackupKindTestRestore, payload, pool) {
		return
	}
	master, err := c.backupMasterHost(tx, pool, host)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.name=? AND o.status IN ('queued','dispatched') AND r.kind IN ('vm-create','vm-update','vm-power','vm-media','vm-delete','snapshot-create','snapshot-revert','snapshot-delete','snapshot-metadata','backup-now','backup-restore-as-new','backup-restore-replace','backup-test-restore')", restore.NewVMID).Scan(&pending); err != nil {
		fail(w, 500, "pending request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a create or recovery request for this new VM id is still pending; wait for its task")
		return
	}
	var held int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources WHERE id=? OR operation_id=? OR id=?", requestID, requestID, restore.NewVMID).Scan(&held); err != nil {
		fail(w, 500, "backup identity lookup failed")
		return
	}
	if held > 0 {
		fail(w, 409, "request_id or new_vm_id already used for another operation")
		return
	}
	if _, err = c.readyStorageForHost(tx, restore.StorageID, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	rowID := "backup:" + requestID
	cmd := protocol.Command{OperationID: requestID, Kind: protocol.BackupKindTestRestore, TestRestore: restore}
	op, err := c.saveResource(tx, user, requestID, pool, master, restore.NewVMID, rowID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "test-restore request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}

func (c *Controller) verifyTestRestore(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body testRestoreVerifyAPIRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.TestRestoreVerifyRequest{
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID,
		TestVMID: body.TestVMID, Result: body.Result,
	}
	if !nameValid(pool) || !protocol.ValidSnapshotOperationID(body.RequestID) || req.Valid() != nil {
		fail(w, 400, "stable request_id and test-restore verify fields required")
		return
	}
	c.enqueueBackupMasterSync(w, pool, user, body.RequestID, "backup-test-verify", protocol.Command{
		OperationID: body.RequestID, Kind: "backup-test-verify", TestRestoreVerify: &req,
	}, body, body.RecoveryPointID)
}

func (c *Controller) cleanupTestRestore(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body testRestoreCleanupAPIRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.TestRestoreCleanupRequest{
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID, TestVMID: body.TestVMID,
	}
	if !nameValid(pool) || !protocol.ValidSnapshotOperationID(body.RequestID) || req.Valid() != nil {
		fail(w, 400, "stable request_id and test-restore cleanup fields required")
		return
	}
	c.enqueueBackupMasterSync(w, pool, user, body.RequestID, "backup-test-cleanup", protocol.Command{
		OperationID: body.RequestID, Kind: "backup-test-cleanup", TestRestoreClean: &req,
	}, body, body.TestVMID)
}

func (c *Controller) importBackupCatalog(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.catalogImportOrRebuild(w, r, user, "backup-catalog-import")
}

func (c *Controller) rebuildBackupCatalog(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.catalogImportOrRebuild(w, r, user, "backup-catalog-rebuild")
}

func (c *Controller) catalogImportOrRebuild(w http.ResponseWriter, r *http.Request, user, kind string) {
	pool := r.PathValue("id")
	var body catalogImportAPIRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.CatalogImportRequest{RepositoryID: body.RepositoryID}
	if !nameValid(pool) || !protocol.ValidSnapshotOperationID(body.RequestID) || req.Valid() != nil {
		fail(w, 400, "stable request_id and repository_id required")
		return
	}
	c.enqueueBackupMasterSync(w, pool, user, body.RequestID, kind, protocol.Command{
		OperationID: body.RequestID, Kind: kind, CatalogImport: &req,
	}, body, body.RepositoryID)
}

func (c *Controller) enqueueBackupMasterSync(w http.ResponseWriter, pool, user, requestID, kind string, cmd protocol.Command, payload any, name string) {
	if !nameValid(pool) {
		fail(w, 404, "pool not found")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	scope := []string{pool}
	switch {
	case cmd.ForgetRepository != nil:
		scope = append(scope, cmd.ForgetRepository.RepositoryID)
	case cmd.OrphanCleanup != nil:
		scope = append(scope, cmd.OrphanCleanup.RepositoryID)
	case cmd.PolicyDelete != nil:
		scope = append(scope, cmd.PolicyDelete.PolicyID)
	}
	if c.replayResource(w, tx, requestID, kind, payload, scope...) {
		return
	}
	var master string
	if err = tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil || master == "" {
		failPoolMaster(w, err)
		return
	}
	if err = c.backupHost(tx, pool, master); err != nil {
		fail(w, 409, err.Error())
		return
	}
	var held int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources WHERE id=? OR operation_id=?", requestID, requestID).Scan(&held); err != nil {
		fail(w, 500, "backup identity lookup failed")
		return
	}
	if held > 0 {
		fail(w, 409, "request_id already used for another operation")
		return
	}
	if name == "" {
		name = requestID
	}
	rowID := "backup:" + requestID
	op, err := c.saveResource(tx, user, requestID, pool, master, name, rowID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "backup request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}
