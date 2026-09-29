// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

type createBackupRepositoryRequest struct {
	RequestID           string                     `json:"request_id"`
	ID                  string                     `json:"id,omitempty"`
	Name                string                     `json:"name"`
	Kind                string                     `json:"kind"`
	Source              protocol.NFSStorageSource  `json:"source"`
	SMBSource           *protocol.SMBStorageSource `json:"smb_source,omitempty"`
	S3Source            *protocol.BackupS3Source   `json:"s3_source,omitempty"`
	SFTPSource          *protocol.BackupSFTPSource `json:"sftp_source,omitempty"`
	CredentialRef       string                     `json:"credential_ref,omitempty"`
	CredentialRequestID string                     `json:"credential_request_id,omitempty"`
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
	c.listPoolBackupInventoryWithFreshness(w, r.PathValue("id"), "backup_repositories", func(m *protocol.ManagedState, masterFresh bool) (bool, any) {
		if !m.BackupRepositoriesReported {
			return false, nil
		}
		out := append([]protocol.BackupRepository{}, m.BackupRepositories...)
		for i := range out {
			// Core ages health on its own clock; the Controller only masks a stale master.
			health := &out[i].Health
			if !masterFresh || health.Status == "" {
				health.Status, health.Error = "unknown", ""
			}
		}
		return true, out
	})
}

func (c *Controller) listBackupCatalog(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.relayBackupInventoryPage(w, r, true)
}

func (c *Controller) listBackupRuns(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.relayBackupInventoryPage(w, r, false)
}

var backupPageGenerationValid = regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString

// relayBackupInventoryPage reads Core-owned history over the enrolled host's
// outbound channel.
func (c *Controller) relayBackupInventoryPage(w http.ResponseWriter, r *http.Request, catalog bool) {
	pool := r.PathValue("id")
	var master string
	var revoked bool
	if !nameValid(pool) {
		fail(w, 404, "pool not found")
		return
	}
	if err := c.db.QueryRow("SELECT p.master_host_id,coalesce(h.revoked,1) FROM pools p LEFT JOIN hosts h ON h.id=p.master_host_id WHERE p.id=?", pool).Scan(&master, &revoked); err != nil || master == "" {
		failPoolMaster(w, err)
		return
	}
	if fresh, _ := c.hostAgent(master, "", revoked); !fresh {
		fail(w, 503, "pool master is not freshly connected")
		return
	}
	query := r.URL.Query()
	allowed := []string{"cursor", "generation"}
	path := "/backup-runs"
	if catalog {
		path = "/backup-catalog"
		allowed = append(allowed, "vm_id", "repository_id", "search")
	} else {
		allowed = append(allowed, "recovery_point_id")
	}
	for key, values := range query {
		limit := 128
		if key == "search" {
			limit = 200
		}
		if !slices.Contains(allowed, key) || len(values) != 1 || len(values[0]) > limit {
			fail(w, 400, "invalid backup page filter")
			return
		}
	}
	if cursor := query.Get("cursor"); query.Has("cursor") {
		if n, err := strconv.Atoi(cursor); err != nil || n < 0 || !backupPageGenerationValid(query.Get("generation")) {
			fail(w, 400, "invalid backup page cursor")
			return
		}
	} else if query.Has("generation") {
		fail(w, 400, "invalid backup page cursor")
		return
	}
	status, body, _, ok := c.askAgent(w, r, master, path, url.Values(query), "backup history page")
	if !ok {
		return
	}
	switch {
	case status == 409 && query.Has("generation"):
		fail(w, 409, "backup history changed; restart from first page")
		return
	case status == 409:
		fail(w, 503, "pool master is not serving backup history; retry after the master change settles")
		return
	case status == 413:
		fail(w, 413, "a backup history record exceeds the page limit")
		return
	case status == 400:
		fail(w, 400, "invalid backup page cursor or filter")
		return
	case status == 503:
		fail(w, 503, "backup history is unavailable from the pool master")
		return
	}
	if status != 200 || len(body) > backupRelayPageBytes || !validBackupRelayPage(body, pool, catalog, query.Get("cursor")) {
		fail(w, 502, "pool master returned an invalid backup history page")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

const backupRelayPageBytes = 2 << 20

func validBackupRelayPage(body []byte, pool string, catalog bool, cursor string) bool {
	start := 0
	if cursor != "" {
		start, _ = strconv.Atoi(cursor)
	}
	valid := func(generation, next string, total, count int) bool {
		if !backupPageGenerationValid(generation) || total < 0 || count > 64 || start+count > total {
			return false
		}
		if next == "" {
			return start+count == total
		}
		n, err := strconv.Atoi(next)
		return err == nil && n == start+count && n < total && count > 0
	}
	if catalog {
		var page protocol.BackupCatalogPage
		if json.Unmarshal(body, &page) != nil || !valid(page.Generation, page.NextCursor, page.Total, len(page.Items)) || page.UntestedCount < 0 {
			return false
		}
		seen := map[string]bool{}
		for _, point := range page.Items {
			if point.PoolID != pool || point.Valid() != nil || seen[point.ID] {
				return false
			}
			seen[point.ID] = true
		}
		return true
	}
	var page protocol.BackupRunPage
	if json.Unmarshal(body, &page) != nil || !valid(page.Generation, page.NextCursor, page.Total, len(page.Items)) || page.AttentionCount < 0 {
		return false
	}
	seen := map[string]bool{}
	for _, run := range page.Items {
		if run.PoolID != pool || run.Valid() != nil || seen[run.OperationID] {
			return false
		}
		seen[run.OperationID] = true
	}
	return true
}

// failPoolMaster answers a failed pool-master lookup: an unknown pool is 404.
func failPoolMaster(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "pool not found")
		return
	}
	fail(w, 503, "pool master is unavailable")
}

func (c *Controller) listPoolBackupInventory(w http.ResponseWriter, pool, label string, pick func(*protocol.ManagedState) (bool, any)) {
	c.listPoolBackupInventoryWithFreshness(w, pool, label, func(m *protocol.ManagedState, _ bool) (bool, any) { return pick(m) })
}

func (c *Controller) listPoolBackupInventoryWithFreshness(w http.ResponseWriter, pool, label string, pick func(*protocol.ManagedState, bool) (bool, any)) {
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
	if err = tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil || master == "" {
		failPoolMaster(w, err)
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
	c.mu.Lock()
	seen := c.seen[master]
	c.mu.Unlock()
	ok, out := pick(inv.Managed, !seen.IsZero() && time.Since(seen) <= c.cfg.StaleAfter)
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
	if repoID == "" && body.Kind != protocol.BackupRepoKindNFS {
		// The credential receipt is already bound to the caller's repository ID.
		fail(w, 400, "id is required for a destination with a credential")
		return
	}
	if repoID == "" {
		repoID = random()
	}
	repo := protocol.BackupRepository{
		ID: repoID, PoolID: pool, Name: body.Name, Kind: body.Kind, Source: body.Source,
		SMBSource: body.SMBSource, S3Source: body.S3Source, SFTPSource: body.SFTPSource,
		CredentialRef: body.CredentialRef,
	}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || repo.Valid() != nil ||
		(repo.Kind == protocol.BackupRepoKindNFS && (body.CredentialRef != "" || body.CredentialRequestID != "")) ||
		(repo.Kind != protocol.BackupRepoKindNFS && (body.CredentialRef == "" || !protocol.ValidSnapshotOperationID(body.CredentialRequestID))) {
		fail(w, 400, "stable request_id and valid backup repository required")
		return
	}
	c.enqueueBackupRepository(w, pool, user, body.RequestID, "backup-repository-register", &repo, body)
}

func (c *Controller) backupDestinationHost(tx *sql.Tx, pool, master, kind string) error {
	if err := c.backupHost(tx, pool, master); err != nil {
		return err
	}
	if kind == protocol.BackupRepoKindNFS {
		return nil
	}
	var raw string
	if err := tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", master).Scan(&raw); err != nil {
		return err
	}
	var capabilities []string
	if json.Unmarshal([]byte(raw), &capabilities) != nil || !contains(capabilities, "backup-destination-"+kind+"-v1") {
		return fmt.Errorf("pool master does not support %s backup destinations", kind)
	}
	return nil
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
	if err = tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil || master == "" {
		failPoolMaster(w, err)
		return
	}
	if kind == "backup-repository-register" {
		err = c.backupDestinationHost(tx, pool, master, repo.Kind)
	} else {
		err = c.backupHost(tx, pool, master)
	}
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	if kind == "backup-repository-register" && repo.Kind != protocol.BackupRepoKindNFS {
		body := payload.(createBackupRepositoryRequest)
		binding := "backup-credential:" + repo.Kind + ":" + requestID
		receipt, ok := c.credentials.receipt(master, body.CredentialRequestID, pool, repo.ID, binding)
		if !ok || receipt.Receipt != repo.CredentialRef {
			fail(w, 409, "backup credential receipt does not match this repository registration")
			return
		}
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
