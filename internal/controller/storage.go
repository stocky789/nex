// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"nex.local/nex/internal/protocol"
	"strings"
)

type storageRegisterRequest struct {
	RequestID           string                 `json:"request_id"`
	PoolID              string                 `json:"pool_id"`
	HostID              string                 `json:"host_id"`
	Name                string                 `json:"name"`
	Path                string                 `json:"path,omitempty"`
	Kind                string                 `json:"kind,omitempty"`
	Source              protocol.StorageSource `json:"source,omitempty"`
	CredentialReceipt   string                 `json:"credential_receipt,omitempty"`
	CredentialRequestID string                 `json:"credential_request_id,omitempty"`
}

// storageRepositoryHost keeps new storage work on a Core that explicitly negotiated v2.
func (c *Controller) storageRepositoryHost(tx *sql.Tx, pool, host string) error {
	if err := c.managedHost(tx, pool, host); err != nil {
		return err
	}
	var raw string
	if err := tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", host).Scan(&raw); err != nil {
		return fmt.Errorf("host does not support storage repositories")
	}
	var caps []string
	if json.Unmarshal([]byte(raw), &caps) != nil || !contains(caps, protocol.StorageRepositoryCapability) {
		return fmt.Errorf("host does not support storage repositories")
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func storageRegistrationKey(storage protocol.StorageRegistration) string {
	if storage.StorageKind() == protocol.StorageKindLocal {
		return "path:" + storage.Path
	}
	identitySet := storage.Identity.Local != nil || storage.Identity.NFS != nil || storage.Identity.ZFS != nil || storage.Identity.SMB != nil || storage.Identity.ISCSI != nil
	b, _ := json.Marshal(storage.Identity)
	if !identitySet {
		b, _ = json.Marshal(storage.Source)
	}
	return "storage:" + storage.StorageKind() + ":" + string(b)
}

// storageReadyForWrite keeps v1 local registrations usable while refusing a typed repository
// Core currently reports as unavailable or incapable of holding VM disks.
func storageReadyForWrite(cmd protocol.Command) error {
	if cmd.Storage == nil || cmd.Storage.Kind == "" {
		return nil
	}
	s := cmd.Storage
	if s.Tombstone || !s.Access.Writable || !s.Capabilities.VMDisk {
		return fmt.Errorf("storage repository is not writable for VM disks")
	}
	return nil
}

func (c *Controller) registerStorage(w http.ResponseWriter, r *http.Request, user string) {
	var v storageRegisterRequest
	if !decode(w, r, &v) {
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	if !nameValid(v.RequestID) || !nameValid(v.PoolID) || !nameValid(v.HostID) || !nameValid(v.Name) || (v.CredentialReceipt != "" && !identifierValid(v.CredentialReceipt)) || (v.CredentialRequestID != "" && !nameValid(v.CredentialRequestID)) {
		fail(w, http.StatusBadRequest, "name, pool_id, host_id and stable request_id required")
		return
	}
	if (v.CredentialReceipt == "") != (v.CredentialRequestID == "") || (v.CredentialRequestID != "" && v.CredentialRequestID != v.RequestID) {
		fail(w, http.StatusBadRequest, "credential receipt must be bound to this registration request_id")
		return
	}
	s := protocol.StorageRegistration{PoolID: v.PoolID, HostID: v.HostID, Name: v.Name, Path: v.Path, Kind: v.Kind, Source: v.Source, CredentialReceipt: v.CredentialReceipt, CredentialRequestID: v.CredentialRequestID}
	if err := protocol.ValidateStorageRegistration(s, true); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, v.RequestID, "storage-register", v) {
		return
	}
	// Path-only is the v1 local-directory contract. Keep it for existing Core journals;
	// every typed repository, including an explicit local kind, uses Core-created identity.
	if v.Kind == "" && v.Path != "" && v.Source == (protocol.StorageSource{}) && v.CredentialReceipt == "" && v.CredentialRequestID == "" {
		if err = c.managedHost(tx, v.PoolID, v.HostID); err != nil {
			fail(w, http.StatusConflict, err.Error())
			return
		}
		id, opID := random(), random()
		s.ID, s.OperationID, s.Status = id, opID, "queued"
		cmd := protocol.Command{OperationID: opID, Kind: "storage-register", Storage: &s}
		op, err := c.saveResource(tx, user, v.RequestID, v.PoolID, v.HostID, v.Name, id, cmd, v)
		if err != nil || tx.Commit() != nil {
			fail(w, http.StatusInternalServerError, "registration storage failed")
			return
		}
		write(w, http.StatusCreated, resourceResponse(cmd, op))
		return
	}
	if err = c.storageRepositoryHost(tx, v.PoolID, v.HostID); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	opID := random()
	s.OperationID, s.Status = opID, "queued"
	cmd := protocol.Command{OperationID: opID, Kind: "storage-register", Storage: &s}
	// Core allocates the repository ID. This synthetic row only retains the request until
	// the Core journal projection arrives and is never returned as a repository.
	op, err := c.saveResource(tx, user, v.RequestID, v.PoolID, v.HostID, v.Name, "pending-storage-"+opID, cmd, v)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "registration storage failed")
		return
	}
	write(w, http.StatusCreated, resourceResponse(cmd, op))
}

type storageLifecycleAPIRequest struct {
	RequestID          string                        `json:"request_id"`
	PoolID             string                        `json:"pool_id"`
	HostID             string                        `json:"host_id"`
	ExpectedGeneration uint64                        `json:"expected_generation,omitempty"`
	Registration       *protocol.StorageRegistration `json:"registration,omitempty"`
	CredentialReceipt  string                        `json:"credential_receipt,omitempty"`
}

func (c *Controller) updateStorage(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.storageLifecycle(w, r, user, "update")
}
func (c *Controller) recheckStorage(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.storageLifecycle(w, r, user, "recheck")
}
func (c *Controller) detachStorage(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.storageLifecycle(w, r, user, "detach")
}

func (c *Controller) storageLifecycle(w http.ResponseWriter, r *http.Request, user, action string) {
	var body storageLifecycleAPIRequest
	if !decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	if !identifierValid(id) || !nameValid(body.RequestID) || !nameValid(body.PoolID) || !nameValid(body.HostID) || (body.CredentialReceipt != "" && !identifierValid(body.CredentialReceipt)) {
		fail(w, http.StatusBadRequest, "repository, pool_id, host_id and stable request_id required")
		return
	}
	if body.Registration != nil {
		body.Registration.ID = "" // repository identity always stays Core-owned.
		body.Registration.PoolID, body.Registration.HostID = body.PoolID, body.HostID
	}
	v := protocol.StorageLifecycleRequest{Action: action, RepositoryID: id, PoolID: body.PoolID, HostID: body.HostID, ExpectedGeneration: body.ExpectedGeneration, Registration: body.Registration, CredentialReceipt: body.CredentialReceipt}
	if err := protocol.ValidateStorageLifecycle(v); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "storage-"+action, body) {
		return
	}
	if err = c.storageRepositoryHost(tx, body.PoolID, body.HostID); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	var raw, status string
	if err = tx.QueryRow("SELECT r.command,o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='storage-register' AND r.pool_id=? AND r.host_id=?", id, body.PoolID, body.HostID).Scan(&raw, &status); err != nil || status != "succeeded" {
		fail(w, http.StatusNotFound, "storage repository is not adopted on this host")
		return
	}
	var registered protocol.Command
	if json.Unmarshal([]byte(raw), &registered) != nil || registered.Storage == nil || registered.Storage.Generation != body.ExpectedGeneration {
		fail(w, http.StatusConflict, "storage repository generation changed; recheck and retry")
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "storage-" + action, StorageLifecycle: &v}
	op, err := c.saveResource(tx, user, body.RequestID, body.PoolID, body.HostID, registered.Storage.Name, "storage-operation-"+opID, cmd, body)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "storage operation failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}

// mirrorStorageProjection refreshes the Controller's disposable projection from Core's journal.
func (c *Controller) mirrorStorageProjection(tx *sql.Tx, pool, host string, storage protocol.StorageRegistration) error {
	if storage.Tombstone {
		_, err := tx.Exec("DELETE FROM managed_resources WHERE id=? AND kind='storage-register' AND pool_id=? AND host_id=?", storage.ID, pool, host)
		return err
	}
	storage.PoolID, storage.HostID = pool, host
	storage.CredentialReceipt, storage.CredentialRequestID = "", ""
	if err := protocol.ValidateStoredStorageRegistration(storage, false); err != nil {
		// Version-one local records have no generation and remain supported during adoption.
		if storage.Kind != "" || !storagePathValid(storage.Path) || !nameValid(storage.Name) {
			return err
		}
	}
	var opID string
	err := tx.QueryRow("SELECT operation_id FROM managed_resources WHERE id=? AND kind='storage-register'", storage.ID).Scan(&opID)
	if err == sql.ErrNoRows && storage.OperationID != "" {
		// A successful registration replaces its Controller-only pending request row.
		err = tx.QueryRow("SELECT operation_id FROM managed_resources WHERE operation_id=? AND kind='storage-register'", storage.OperationID).Scan(&opID)
		if err == nil {
			if _, err = tx.Exec("UPDATE managed_resources SET id=? WHERE operation_id=?", storage.ID, opID); err != nil {
				return err
			}
		}
	}
	if err == nil {
		storage.OperationID, storage.Status, storage.Error = opID, "succeeded", ""
		cmd, payload := protocol.Command{OperationID: opID, Kind: "storage-register", Storage: &storage}, storageRegisterRequest{RequestID: "adopt-storage:" + host + ":" + storage.ID, PoolID: pool, HostID: host, Name: storage.Name, Path: storage.Path, Kind: storage.Kind, Source: storage.Source}
		command, commandErr := json.Marshal(cmd)
		payloadJSON, payloadErr := json.Marshal(payload)
		if commandErr != nil || payloadErr != nil {
			return fmt.Errorf("storage projection encoding failed")
		}
		_, err = tx.Exec("UPDATE managed_resources SET pool_id=?,host_id=?,name=?,payload=?,command=? WHERE id=? AND kind='storage-register'", pool, host, storage.Name, string(payloadJSON), string(command), storage.ID)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	opID = random()
	storage.OperationID, storage.Status, storage.Error = opID, "succeeded", ""
	cmd := protocol.Command{OperationID: opID, Kind: "storage-register", Storage: &storage}
	payload := storageRegisterRequest{RequestID: "adopt-storage:" + host + ":" + storage.ID, PoolID: pool, HostID: host, Name: storage.Name, Path: storage.Path, Kind: storage.Kind, Source: storage.Source}
	_, err = c.saveResourceStatus(tx, "system", payload.RequestID, pool, host, storage.Name, storage.ID, "succeeded", cmd, payload)
	return err
}
