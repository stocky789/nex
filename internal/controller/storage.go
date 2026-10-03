// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"nex.local/nex/internal/protocol"
	"strings"
	"sync"
	"time"
)

type storageDiscoveryPending struct {
	request    protocol.StorageDiscoveryRequest
	result     chan protocol.StorageDiscoveryResult
	dispatched bool
}
type storageDiscoveryBroker struct {
	mu      sync.Mutex
	pending map[string]storageDiscoveryPending
}

var storageDiscoveryBrokers sync.Map // map[*Controller]*storageDiscoveryBroker; transient only
func (c *Controller) discoveryBroker() *storageDiscoveryBroker {
	v, _ := storageDiscoveryBrokers.LoadOrStore(c, &storageDiscoveryBroker{pending: map[string]storageDiscoveryPending{}})
	return v.(*storageDiscoveryBroker)
}
func (c *Controller) takeStorageDiscoveries(host string) []protocol.StorageDiscoveryRequest {
	b := c.discoveryBroker()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []protocol.StorageDiscoveryRequest
	for id, pending := range b.pending {
		if pending.request.HostID == host && !pending.dispatched {
			request := pending.request // the only outbound copy may carry credentials
			pending.request.Credentials = nil
			out = append(out, request)
			pending.dispatched = true
			b.pending[id] = pending
		}
	}
	return out
}
func (c *Controller) completeStorageDiscoveries(host string, results []protocol.StorageDiscoveryResult) {
	b := c.discoveryBroker()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, result := range results {
		pending, ok := b.pending[result.RequestID]
		if !ok || result.HostID != host || result.HostID != pending.request.HostID || result.PoolID != pending.request.PoolID {
			continue
		}
		if protocol.ValidateStorageDiscoveryResult(result) != nil {
			result = protocol.StorageDiscoveryResult{RequestID: result.RequestID, PoolID: result.PoolID, HostID: result.HostID, Status: "failed", Error: "storage discovery is unavailable", ObservedAt: result.ObservedAt}
		}
		if ch := pending.result; ch != nil {
			select {
			case ch <- result:
			default:
			}
			delete(b.pending, result.RequestID)
		}
	}
}
func (c *Controller) discoverStorage(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var request protocol.StorageDiscoveryRequest
	if !decode(w, r, &request) {
		return
	}
	if err := protocol.ValidateStorageDiscovery(request); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if err = c.storageRepositoryHost(tx, request.PoolID, request.HostID); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	var raw string
	var capabilities []string
	if err = tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", request.HostID).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &capabilities) != nil || !contains(capabilities, protocol.StorageDiscoveryCapability) {
		fail(w, http.StatusConflict, "host does not support storage discovery")
		return
	}
	if request.Purpose != "" && !contains(capabilities, protocol.StorageDiscoveryPurposeCapability) {
		fail(w, http.StatusConflict, "host does not support backup-purpose discovery; upgrade Nex Core")
		return
	}
	if request.TargetIQN != "" && !contains(capabilities, protocol.ISCSIInspectCapability) {
		fail(w, http.StatusConflict, "host does not support selected-target iSCSI inspection; upgrade Nex Core")
		return
	}
	if request.Kind == "zfs-device" && !contains(capabilities, protocol.ZFSProvisionCapability) {
		fail(w, http.StatusConflict, "host does not support ZFS provisioning; upgrade Nex Core")
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	b := c.discoveryBroker()
	result := make(chan protocol.StorageDiscoveryResult, 1)
	b.mu.Lock()
	if _, exists := b.pending[request.RequestID]; exists {
		b.mu.Unlock()
		fail(w, http.StatusConflict, "storage discovery request is already active")
		return
	}
	b.pending[request.RequestID] = storageDiscoveryPending{request: request, result: result}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, request.RequestID)
		b.mu.Unlock()
	}()
	select {
	case found := <-result:
		write(w, http.StatusOK, found)
	case <-r.Context().Done():
		return
	case <-time.After(30 * time.Second):
		write(w, http.StatusGatewayTimeout, protocol.StorageDiscoveryResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "stale", Error: "Core did not complete discovery before the deadline", ObservedAt: time.Now().UTC()})
	}
}

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
	// the Core journal projection arrives and is never returned as a repository. If Core
	// refuses the request the row stays only as the replay record and is not listed.
	rowID := "pending-storage-" + opID
	op, err := c.saveResource(tx, user, v.RequestID, v.PoolID, v.HostID, v.Name, rowID, cmd, v)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "registration storage failed")
		return
	}
	write(w, http.StatusCreated, resourceResponse(cmd, op))
}

type zfsProvisionAPIRequest struct {
	RequestID    string                      `json:"request_id"`
	RepositoryID string                      `json:"repository_id,omitempty"`
	Name         string                      `json:"name"`
	PoolID       string                      `json:"pool_id"`
	Scope        string                      `json:"scope"`
	Hosts        []protocol.ZFSProvisionHost `json:"hosts"`
}

// zfsProvisionMaster sends the durable request only to the pool master. Core owns the
// repository ID and executes selected member work over its authenticated peer channel.
func (c *Controller) zfsProvisionMaster(tx *sql.Tx, pool string) (string, error) {
	var host, raw string
	if err := tx.QueryRow(`SELECT p.master_host_id,coalesce(c.capabilities,'[]') FROM pools p LEFT JOIN host_capabilities c ON c.host_id=p.master_host_id WHERE p.id=?`, pool).Scan(&host, &raw); err != nil {
		return "", fmt.Errorf("pool not found")
	}
	if err := c.managedHost(tx, pool, host); err != nil {
		return "", err
	}
	var capabilities []string
	if json.Unmarshal([]byte(raw), &capabilities) != nil || !contains(capabilities, protocol.ZFSProvisionCapability) {
		return "", fmt.Errorf("pool master does not support ZFS provisioning; upgrade Nex Core")
	}
	return host, nil
}

func (c *Controller) provisionZFSStorage(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body zfsProvisionAPIRequest
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	request := protocol.ZFSProvisionRequest{RepositoryID: body.RepositoryID, Name: body.Name, PoolID: body.PoolID, Scope: body.Scope, Hosts: body.Hosts}
	if !nameValid(body.RequestID) || !nameValid(body.Name) || !nameValid(body.PoolID) {
		fail(w, http.StatusBadRequest, "name, pool_id and stable request_id required")
		return
	}
	if err := protocol.ValidateZFSProvision(request); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "storage-zfs-provision", body) {
		return
	}
	master, err := c.zfsProvisionMaster(tx, body.PoolID)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	needsReplace := protocol.ZFSProvisionNeedsReplaceCapability(request)
	if needsReplace {
		var raw string
		var capabilities []string
		if err := tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", master).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &capabilities) != nil || !contains(capabilities, protocol.ZFSReplaceCapability) {
			fail(w, http.StatusConflict, fmt.Sprintf("hot spares and widened ZFS layouts need %s on the pool master and every selected host; upgrade Nex Core", protocol.ZFSReplaceCapability))
			return
		}
	}
	// Core still decides the provisioning itself; refusing a stale or incapable selected
	// host here only turns a delayed needs_attention into an immediate answer.
	for _, selected := range request.Hosts {
		if err := c.managedHost(tx, request.PoolID, selected.HostID); err != nil {
			fail(w, http.StatusConflict, "selected host must be fresh, active and in the selected pool")
			return
		}
		var raw string
		var capabilities []string
		if err := tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", selected.HostID).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &capabilities) != nil || !contains(capabilities, protocol.ZFSProvisionCapability) {
			fail(w, http.StatusConflict, "selected host does not support ZFS provisioning; upgrade Nex Core")
			return
		}
		if needsReplace && !contains(capabilities, protocol.ZFSReplaceCapability) {
			fail(w, http.StatusConflict, fmt.Sprintf("hot spares and widened ZFS layouts need %s on the pool master and every selected host; upgrade Nex Core", protocol.ZFSReplaceCapability))
			return
		}
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "storage-zfs-provision", ZFSProvision: &request}
	// This row is only the Controller's idempotent request/operation relay. Core's master
	// journal and its inventory projection remain the sole repository record.
	op, err := c.saveResource(tx, user, body.RequestID, body.PoolID, master, body.Name, "pending-zfs-provision-"+opID, cmd, body)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "ZFS provisioning request storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
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
		command, commandErr := json.Marshal(protocol.Command{OperationID: opID, Kind: "storage-register", Storage: &storage})
		if commandErr != nil {
			return fmt.Errorf("storage projection encoding failed")
		}
		if _, err = tx.Exec("UPDATE managed_resources SET pool_id=?,host_id=?,name=?,command=? WHERE id=? AND kind='storage-register'", pool, host, storage.Name, string(command), storage.ID); err != nil {
			return err
		}
		// A fresh Core registration restores this disposable projection. This is
		// the registration mirror's status, distinct from any prior provision
		// operation result, which remains Core-owned under its original ID.
		_, err = tx.Exec("UPDATE operations SET status='succeeded',error='',updated_at=? WHERE id=? AND status='needs_attention'", stamp(), opID)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	// Older Controller projections could be removed while retaining their
	// successful adopt-storage operation. A later Core report must reconnect that
	// exact host-scoped operation rather than inserting the same request ID again.
	adoptRequestID := "adopt-storage:" + host + ":" + storage.ID
	if err = tx.QueryRow("SELECT id FROM operations WHERE request_id=? AND host_id=? AND kind='storage-register'", adoptRequestID, host).Scan(&opID); err == nil {
		storage.OperationID, storage.Status, storage.Error = opID, "succeeded", ""
		command, commandErr := json.Marshal(protocol.Command{OperationID: opID, Kind: "storage-register", Storage: &storage})
		if commandErr != nil {
			return fmt.Errorf("storage projection encoding failed")
		}
		payload := storageRegisterRequest{RequestID: adoptRequestID, PoolID: pool, HostID: host, Name: storage.Name, Path: storage.Path, Kind: storage.Kind, Source: storage.Source}
		body, payloadErr := json.Marshal(payload)
		if payloadErr != nil {
			return fmt.Errorf("storage projection encoding failed")
		}
		if _, err = tx.Exec("INSERT INTO managed_resources VALUES(?,?,?,?,?,?,?,?,?)", storage.ID, "storage-register", pool, host, storage.Name, adoptRequestID, string(body), string(command), opID); err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE operations SET status='succeeded',error='',updated_at=? WHERE id=?", stamp(), opID)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	opID = random()
	storage.OperationID, storage.Status, storage.Error = opID, "succeeded", ""
	cmd := protocol.Command{OperationID: opID, Kind: "storage-register", Storage: &storage}
	payload := storageRegisterRequest{RequestID: adoptRequestID, PoolID: pool, HostID: host, Name: storage.Name, Path: storage.Path, Kind: storage.Kind, Source: storage.Source}
	_, err = c.saveResourceStatus(tx, "system", payload.RequestID, pool, host, storage.Name, storage.ID, "succeeded", cmd, payload)
	return err
}

// reconcileZFSStorageProjections marks only disposable master mirrors that a
// current master inventory no longer claims as unusable. ZFS provision outcomes
// alone are not a registration: a failed create can retain an outcome while no
// usable dataset or managed storage record exists on Core. Keep the mirror row
// so a later authoritative registration can refresh it without colliding with
// its retained adoption request/operation history.
func (c *Controller) reconcileZFSStorageProjections(tx *sql.Tx, pool, host string, observed []protocol.StorageRegistration, checks []protocol.Check) error {
	var master string
	if err := tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil || master != host {
		return err
	}
	// An empty projection proves removal only when Core could read its complete
	// managed journal. A failed or unavailable journal can omit valid records;
	// keep the prior mirror in that case, where target admission still requires
	// a fresh native access observation.
	journalHealthy := false
	for _, check := range checks {
		if check.Name == "managed-journal" && (check.Status == "healthy" || check.Status == "passed") {
			journalHealthy = true
			break
		}
	}
	if !journalHealthy {
		return nil
	}
	present := make(map[string]bool, len(observed))
	for _, storage := range observed {
		present[storage.ID] = true
	}
	rows, err := tx.Query("SELECT r.id,r.command FROM managed_resources r WHERE r.pool_id=? AND r.kind='storage-register'", pool)
	if err != nil {
		return err
	}
	defer rows.Close()
	type staleProjection struct {
		id      string
		command protocol.Command
	}
	stale := []staleProjection{}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			return err
		}
		var command protocol.Command
		if json.Unmarshal([]byte(raw), &command) == nil && command.Storage != nil && command.Storage.ZFSProvisioned && !present[id] {
			stale = append(stale, staleProjection{id: id, command: command})
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, stale := range stale {
		storage := stale.command.Storage
		storage.Status, storage.Error = "unknown", "Core has no current ZFS repository registration"
		storage.Capabilities.VMDisk, storage.Capabilities.ISO = false, false
		storage.Access.Readable, storage.Access.Writable = false, false
		storage.Access.Error = storage.Error
		storage.ZFSHostAccess = nil
		command, marshalErr := json.Marshal(stale.command)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.Exec("UPDATE managed_resources SET command=? WHERE id=? AND kind='storage-register' AND pool_id=?", string(command), stale.id, pool); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE operations SET status='needs_attention',error=?,updated_at=? WHERE id=(SELECT operation_id FROM managed_resources WHERE id=? AND kind='storage-register' AND pool_id=?)", storage.Error, stamp(), stale.id, pool); err != nil {
			return err
		}
	}
	return nil
}
