// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http"
	"strings"

	"nex.local/nex/internal/protocol"
)

type createBackupRepositoryRequest struct {
	RequestID string                    `json:"request_id"`
	ID        string                    `json:"id,omitempty"`
	Name      string                    `json:"name"`
	Kind      string                    `json:"kind"`
	Source    protocol.NFSStorageSource `json:"source"`
}

type testBackupRepositoryRequest struct {
	RequestID string `json:"request_id"`
}

type backupNowAPIRequest struct {
	RequestID    string `json:"request_id"`
	RepositoryID string `json:"repository_id"`
	Consistency  string `json:"consistency,omitempty"`
}

type restoreAsNewAPIRequest struct {
	RequestID       string `json:"request_id"`
	HostID          string `json:"host_id"`
	RepositoryID    string `json:"repository_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	NewVMID         string `json:"new_vm_id"`
	NewVMName       string `json:"new_vm_name"`
	StorageID       string `json:"storage_id"`
}

func (c *Controller) listBackupRepositories(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.listPoolBackupInventory(w, r.PathValue("id"), "backup_repositories", func(m *protocol.ManagedState) (bool, any) {
		if !m.BackupRepositoriesReported {
			return false, nil
		}
		out := append([]protocol.BackupRepository{}, m.BackupRepositories...)
		return true, out
	})
}

func (c *Controller) listBackupCatalog(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.listPoolBackupInventory(w, r.PathValue("id"), "backup catalog", func(m *protocol.ManagedState) (bool, any) {
		if !m.BackupCatalogReported {
			return false, nil
		}
		out := append([]protocol.BackupRecoveryPoint{}, m.BackupCatalog...)
		return true, out
	})
}

func (c *Controller) listBackupRuns(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.listPoolBackupInventory(w, r.PathValue("id"), "backup runs", func(m *protocol.ManagedState) (bool, any) {
		if !m.BackupRunsReported {
			return false, nil
		}
		out := append([]protocol.BackupRun{}, m.BackupRuns...)
		return true, out
	})
}

func (c *Controller) listPoolBackupInventory(w http.ResponseWriter, pool, label string, pick func(*protocol.ManagedState) (bool, any)) {
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
	var master string
	if tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master) != nil || master == "" {
		fail(w, 503, "pool master is unavailable")
		return
	}
	inv, err := hostInventory(tx, master)
	if err != nil {
		fail(w, 500, "inventory lookup failed")
		return
	}
	if inv.Managed == nil {
		fail(w, 503, label+" is unavailable from the pool master")
		return
	}
	ok, out := pick(inv.Managed)
	if !ok {
		fail(w, 503, label+" is unavailable from the pool master")
		return
	}
	write(w, 200, out)
}

func (c *Controller) createBackupRepository(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body createBackupRepositoryRequest
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	repoID := body.ID
	if repoID == "" {
		repoID = random()
	}
	repo := protocol.BackupRepository{
		ID: repoID, PoolID: pool, Name: body.Name, Kind: body.Kind, Source: body.Source,
	}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || repo.Valid() != nil {
		fail(w, 400, "stable request_id and valid NFS backup repository required")
		return
	}
	c.enqueueBackupRepository(w, pool, user, body.RequestID, "backup-repository-register", &repo, body)
}

func (c *Controller) testBackupRepository(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool, repoID := r.PathValue("id"), r.PathValue("repo_id")
	var body testBackupRepositoryRequest
	if !decode(w, r, &body) {
		return
	}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || !protocol.ValidSnapshotOperationID(repoID) ||
		body.RequestID == repoID {
		fail(w, 400, "stable request_id distinct from repository id required")
		return
	}
	repo := protocol.BackupRepository{ID: repoID, PoolID: pool}
	c.enqueueBackupRepository(w, pool, user, body.RequestID, "backup-repository-test", &repo, body)
}

func (c *Controller) enqueueBackupRepository(w http.ResponseWriter, pool, user, requestID, kind string, repo *protocol.BackupRepository, payload any) {
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
	if kind == "backup-repository-test" {
		scope = append(scope, repo.ID)
	}
	if c.replayResource(w, tx, requestID, kind, payload, scope...) {
		return
	}
	var master string
	if tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master) != nil || master == "" {
		fail(w, 503, "pool master is unavailable")
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
	rowID := "backup:" + requestID
	name := repo.Name
	if name == "" {
		name = repo.ID
	}
	cmd := protocol.Command{OperationID: requestID, Kind: kind, BackupRepository: repo}
	op, err := c.saveResource(tx, user, requestID, pool, master, name, rowID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "backup repository request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}

func (c *Controller) backupVMNow(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body backupNowAPIRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.BackupNowRequest{RepositoryID: body.RepositoryID, Consistency: body.Consistency}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || req.Valid() != nil {
		fail(w, 400, "stable request_id and repository_id required")
		return
	}
	c.enqueueVMBackup(w, r.PathValue("id"), user, body.RequestID, "backup-now", &req, body)
}

func (c *Controller) restoreBackupAsNew(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body restoreAsNewAPIRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.RestoreAsNewRequest{
		HostID:       body.HostID,
		RepositoryID: body.RepositoryID, RecoveryPointID: body.RecoveryPointID,
		NewVMID: body.NewVMID, NewVMName: strings.TrimSpace(body.NewVMName), StorageID: body.StorageID,
	}
	if !nameValid(pool) || !protocol.ValidSnapshotOperationID(body.RequestID) || !nameValid(body.HostID) ||
		req.Valid() != nil || body.RequestID == body.RecoveryPointID {
		fail(w, 400, "stable request_id, host_id and restore-as-new fields required")
		return
	}
	c.enqueueRestoreAsNew(w, pool, body.HostID, user, body.RequestID, &req, body)
}

func (c *Controller) enqueueVMBackup(w http.ResponseWriter, vmID, user, requestID, kind string, backup *protocol.BackupNowRequest, payload any) {
	host, pool, _, _, err := c.vmPlacement(vmID)
	if err != nil {
		fail(w, 404, "VM not found")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, requestID, kind, payload, pool, vmID) {
		return
	}
	master, err := c.backupMasterHost(tx, pool, host)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.name=? AND o.status IN ('queued','dispatched') AND r.kind IN ('vm-update','vm-power','vm-media','vm-delete','snapshot-create','snapshot-revert','snapshot-delete','snapshot-metadata','backup-now','backup-restore-as-new','backup-restore-replace','backup-test-restore')", vmID).Scan(&pending); err != nil {
		fail(w, 500, "pending request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a change, power, media, delete, snapshot or backup request for this VM is still pending; wait for its task")
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
	vm, err := c.currentManagedVMSpec(tx, host, vmID)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	if vm.HostID != host || vm.PoolID != pool || vm.ID != vmID {
		fail(w, 409, "recorded VM placement does not match the selected host")
		return
	}
	rowID := "backup:" + requestID
	cmd := protocol.Command{OperationID: requestID, Kind: kind, VM: &vm, Backup: backup}
	op, err := c.saveResource(tx, user, requestID, pool, master, vmID, rowID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "backup request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}

func (c *Controller) enqueueRestoreAsNew(w http.ResponseWriter, pool, host, user, requestID string, restore *protocol.RestoreAsNewRequest, payload any) {
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, requestID, "backup-restore-as-new", payload, pool) {
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
	cmd := protocol.Command{OperationID: requestID, Kind: "backup-restore-as-new", Restore: restore}
	op, err := c.saveResource(tx, user, requestID, pool, master, restore.NewVMID, rowID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "restore request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}
