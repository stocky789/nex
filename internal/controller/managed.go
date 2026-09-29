// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"nex.local/nex/internal/protocol"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func (c *Controller) initManaged() error {
	_, err := c.db.Exec(`CREATE TABLE IF NOT EXISTS host_capabilities(host_id TEXT PRIMARY KEY REFERENCES hosts(id),capabilities TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS managed_resources(id TEXT PRIMARY KEY,kind TEXT NOT NULL,pool_id TEXT NOT NULL REFERENCES pools(id),host_id TEXT NOT NULL REFERENCES hosts(id),name TEXT NOT NULL,request_id TEXT NOT NULL UNIQUE,payload TEXT NOT NULL,command TEXT NOT NULL,operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id));`)
	if err != nil {
		return err
	}
	return c.initMedia()
}
func (c *Controller) managedHost(tx *sql.Tx, pool, host string) error {
	var actual string
	var revoked bool
	var caps string
	if err := tx.QueryRow("SELECT h.pool_id,h.revoked,coalesce(c.capabilities,'[]') FROM hosts h LEFT JOIN host_capabilities c ON c.host_id=h.id WHERE h.id=?", host).Scan(&actual, &revoked, &caps); err != nil {
		return fmt.Errorf("host not found")
	}
	var capabilities []string
	json.Unmarshal([]byte(caps), &capabilities)
	c.mu.Lock()
	seen := c.seen[host]
	c.mu.Unlock()
	if actual != pool || revoked || time.Since(seen) > c.cfg.StaleAfter || !slices.Contains(capabilities, protocol.ManagedVMCapability) {
		return fmt.Errorf("host must be fresh, active, in the selected pool, and support managed-vm-v1")
	}
	return nil
}

func (c *Controller) backupHost(tx *sql.Tx, pool, host string) error {
	if err := c.managedHost(tx, pool, host); err != nil {
		return err
	}
	var master string
	if err := tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil {
		return err
	}
	if master != host {
		return c.managedHost(tx, pool, master)
	}
	return nil
}

func (c *Controller) backupMasterHost(tx *sql.Tx, pool, target string) (string, error) {
	if err := c.backupHost(tx, pool, target); err != nil {
		return "", err
	}
	var master string
	if err := tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil {
		return "", err
	}
	return master, nil
}

// placementHost only verifies that the target host is managed in the pool. Resource
// availability and VM admission are Core's decision.
func (c *Controller) placementHost(tx *sql.Tx, pool, host string) error {
	if err := c.managedHost(tx, pool, host); err != nil {
		return err
	}
	var master string
	if err := tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil {
		return fmt.Errorf("selected pool not found")
	}
	// Presentation only: a host the master reports as drained is not offered as a placement
	// target, so the operator sees the reason here instead of waiting for it. The Controller
	// decides nothing about maintenance; the master refuses this placement again.
	return c.maintenanceRefusal(tx, pool, master, host)
}
func (c *Controller) saveResource(tx *sql.Tx, user, requestID, pool, host, name, id string, cmd protocol.Command, payload any) (protocol.Operation, error) {
	return c.saveResourceStatus(tx, user, requestID, pool, host, name, id, "queued", cmd, payload)
}
func (c *Controller) saveResourceStatus(tx *sql.Tx, user, requestID, pool, host, name, id, status string, cmd protocol.Command, payload any) (protocol.Operation, error) {
	now := time.Now().UTC()
	op := protocol.Operation{ID: cmd.OperationID, HostID: host, Kind: cmd.Kind, RequestID: requestID, Status: status, CreatedAt: now, UpdatedAt: now, Actor: user}
	b, err := json.Marshal(payload)
	if err != nil {
		return op, err
	}
	command, err := json.Marshal(cmd)
	if err != nil {
		return op, err
	}
	_, err = tx.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", op.ID, host, op.Kind, requestID, op.Status, "", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), user)
	if err == nil {
		_, err = tx.Exec("INSERT INTO managed_resources VALUES(?,?,?,?,?,?,?,?,?)", id, cmd.Kind, pool, host, name, requestID, string(b), string(command), op.ID)
	}
	if err == nil {
		// Requests on an existing VM (vm-power, vm-media, vm-update, vm-delete) pass the VM ID
		// as name and a per-request row id; audit the VM so its activity can be found by VM ID.
		target := id
		if (strings.HasPrefix(cmd.Kind, "vm-") && cmd.Kind != "vm-create") || strings.HasPrefix(cmd.Kind, "snapshot-") {
			target = name
		}
		err = c.audit(tx, user, cmd.Kind, target)
	}
	return op, err
}
func (c *Controller) replayResource(w http.ResponseWriter, tx *sql.Tx, requestID, kind string, payload any, pool ...string) bool {
	var raw, command, storedPool string
	var opID string
	err := tx.QueryRow("SELECT payload,command,operation_id,pool_id FROM managed_resources WHERE request_id=?", requestID).Scan(&raw, &command, &opID, &storedPool)
	if err == sql.ErrNoRows {
		var existing int
		if e := tx.QueryRow("SELECT count(*) FROM operations WHERE request_id=?", requestID).Scan(&existing); e != nil {
			fail(w, 500, "operation lookup failed")
			return true
		}
		if existing > 0 {
			fail(w, 409, "request_id already used for another operation")
			return true
		}
		return false
	}
	if err != nil {
		fail(w, 500, "resource lookup failed")
		return true
	}
	b, _ := json.Marshal(payload)
	var cmd protocol.Command
	json.Unmarshal([]byte(command), &cmd)
	targetMatches := true
	if len(pool) > 1 {
		switch {
		case cmd.BackupPolicy != nil:
			targetMatches = cmd.BackupPolicy.ID == pool[1]
		case cmd.BackupRepository != nil:
			targetMatches = cmd.BackupRepository.ID == pool[1]
		case cmd.VM != nil:
			targetMatches = cmd.VM.ID == pool[1]
		default:
			targetMatches = false
		}
	}
	if raw != string(b) || cmd.Kind != kind || (len(pool) > 0 && storedPool != pool[0]) || !targetMatches {
		fail(w, 409, "request_id already used with another payload")
		return true
	}
	op, err := scanOperation(tx.QueryRow("SELECT * FROM operations WHERE id=?", opID))
	if err != nil {
		fail(w, 500, "operation lookup failed")
		return true
	}
	write(w, 200, resourceResponse(cmd, op))
	return true
}
func resourceResponse(cmd protocol.Command, op protocol.Operation) any {
	switch {
	case cmd.Storage != nil:
		v := *cmd.Storage
		v.Status = op.Status
		v.Error = op.Error
		return v
	case cmd.Network != nil:
		v := *cmd.Network
		v.Status = op.Status
		v.Error = op.Error
		return v
	case cmd.Media != nil:
		v := *cmd.Media
		v.URL = ""
		v.Status = op.Status
		v.Error = op.Error
		return v
	case cmd.Power != nil:
		return map[string]any{"vm_id": cmd.Power.VMID, "action": cmd.Power.Action, "operation": op}
	case cmd.Delete != nil:
		return map[string]any{"vm_id": cmd.Delete.VMID, "delete_disks": cmd.Delete.DeleteDisks, "operation": op}
	case cmd.VMMedia != nil:
		return map[string]any{"vm_id": cmd.VMMedia.VMID, "action": cmd.VMMedia.Action, "media_id": cmd.VMMedia.MediaID, "operation": op}
	case cmd.StorageLifecycle != nil:
		return map[string]any{"repository_id": cmd.StorageLifecycle.RepositoryID, "action": cmd.StorageLifecycle.Action, "operation": op}
	case cmd.ResourceLifecycle != nil:
		return map[string]any{"resource": cmd.ResourceLifecycle.Resource, "resource_id": cmd.ResourceLifecycle.ResourceID, "action": cmd.ResourceLifecycle.Action, "operation": op}
	case cmd.CredentialRetire != nil:
		return map[string]any{"receipt": cmd.CredentialRetire.Receipt, "action": "retire", "operation": op}
	case cmd.Pool != nil:
		if cmd.Pool.Action == "maintenance" {
			return map[string]any{"host_id": cmd.Pool.MemberHostID, "enabled": cmd.Pool.Enabled, "operation": op}
		}
		// A master change, an eject and a cancel answer with the subject host and the
		// action that was asked for; the transfer's stage is reported by the hosts.
		// A promotion answers with the candidate it was addressed to; a fence profile with
		// the host the profile belongs to. Neither response carries the profile itself, so
		// no credential reference is ever echoed back to a client.
		if cmd.Pool.Action == "master-change" || cmd.Pool.Action == "eject" || cmd.Pool.Action == "transfer-cancel" || cmd.Pool.Action == "promote" || cmd.Pool.Action == "fence-profile" {
			return map[string]any{"host_id": cmd.Pool.MemberHostID, "action": cmd.Pool.Action, "operation": op}
		}
		// Never the invitation, the pool CA or the master URL: a join response carries only
		// who was asked to join and the durable operation that asked.
		return map[string]any{"host_id": cmd.Pool.MemberHostID, "peer_origin": cmd.Pool.PeerOrigin, "operation": op}
	case cmd.ManagementNetwork != nil:
		return map[string]any{"host_id": cmd.ManagementNetwork.HostID, "action": cmd.ManagementNetwork.Action, "operation": op}
	case cmd.SystemDiskReplace != nil:
		return map[string]any{"host_id": cmd.SystemDiskReplace.HostID, "array": cmd.SystemDiskReplace.Array, "operation": op}
	case cmd.ZFSReplace != nil:
		return map[string]any{"repository_id": cmd.ZFSReplace.RepositoryID, "host_id": cmd.ZFSReplace.HostID, "old_device": cmd.ZFSReplace.OldDevice, "operation": op}
	case cmd.ZFSProvision != nil:
		response := map[string]any{"name": cmd.ZFSProvision.Name, "pool_id": cmd.ZFSProvision.PoolID, "scope": cmd.ZFSProvision.Scope, "hosts": cmd.ZFSProvision.Hosts, "operation": op}
		if cmd.ZFSProvision.RepositoryID != "" {
			response["repository_id"] = cmd.ZFSProvision.RepositoryID
		}
		return response
	case cmd.BackupRepository != nil:
		return map[string]any{"repository_id": cmd.BackupRepository.ID, "name": cmd.BackupRepository.Name, "operation": op}
	case cmd.BackupPolicy != nil:
		out := map[string]any{"policy_id": cmd.BackupPolicy.ID, "name": cmd.BackupPolicy.Name, "operation": op}
		if cmd.VM != nil {
			out["vm_id"] = cmd.VM.ID
		}
		return out
	case cmd.Backup != nil && cmd.VM != nil:
		return map[string]any{"vm_id": cmd.VM.ID, "repository_id": cmd.Backup.RepositoryID, "operation": op}
	case cmd.Restore != nil:
		return map[string]any{
			"repository_id": cmd.Restore.RepositoryID, "recovery_point_id": cmd.Restore.RecoveryPointID,
			"new_vm_id": cmd.Restore.NewVMID, "new_vm_name": cmd.Restore.NewVMName,
			"storage_id": cmd.Restore.StorageID, "operation": op,
		}
	case cmd.RestoreReplace != nil:
		return map[string]any{
			"repository_id": cmd.RestoreReplace.RepositoryID, "recovery_point_id": cmd.RestoreReplace.RecoveryPointID,
			"original_vm_id": cmd.RestoreReplace.OriginalVMID, "storage_id": cmd.RestoreReplace.StorageID,
			"operation": op,
		}
	case cmd.TestRestore != nil:
		return map[string]any{
			"repository_id": cmd.TestRestore.RepositoryID, "recovery_point_id": cmd.TestRestore.RecoveryPointID,
			"new_vm_id": cmd.TestRestore.NewVMID, "new_vm_name": cmd.TestRestore.NewVMName,
			"storage_id": cmd.TestRestore.StorageID, "operation": op,
		}
	default:
		if cmd.VM == nil {
			return map[string]any{"operation": op}
		}
		return map[string]any{"vm_id": cmd.VM.ID, "operation": op}
	}
}

// resourceRowResponse projects a stored registration, filling in the Controller's own row key
// for one that failed without a Core-allocated ID. A typed repository's identity is Core's to
// mint, so a registration still in flight is honestly answered with an empty id — but a
// registration the host refused will never get one, and with an empty id it could not be
// addressed at all, not even to discard it.
func resourceRowResponse(rowID string, cmd protocol.Command, op protocol.Operation) any {
	if op.Status == "failed" && cmd.Storage != nil && cmd.Storage.ID == "" {
		s := *cmd.Storage
		s.ID = rowID
		cmd.Storage = &s
	}
	return resourceResponse(cmd, op)
}

// registrationApplied reports whether a stored registration command is the mirror of
// something a host has reported as applied. The mirroring passes rewrite the command's
// status, so this is the Controller's own evidence that the resource exists on a host —
// independent of the operation status, which keeps the original request's outcome.
func registrationApplied(raw string) bool {
	var cmd protocol.Command
	if json.Unmarshal([]byte(raw), &cmd) != nil {
		return true // Unreadable: never treat it as a discardable request record.
	}
	switch {
	case cmd.Network != nil:
		return cmd.Network.Status == "succeeded"
	case cmd.Storage != nil:
		return cmd.Storage.Status == "succeeded"
	}
	return true
}

// discardFailedRegistration answers a delete of a registration the host refused, and reports
// whether it answered. A failed registration is Controller-scoped operation history, not pool
// state: no host holds a record of it, nothing can be asked to undo it, and a fresh Controller
// would never recreate it. So the discard deletes this Controller's own request row and relays
// nothing — the same model the failed ISO import already has in deleteMedia. The operation and
// its audit history stay, and the call takes no body.
//
// The guard is narrow on purpose: only a row whose operation failed *and* whose command no host
// has reported as applied is discardable, so this can never delete the projection of a live
// pool resource. Anything else is left to the caller's ordinary relayed-removal path.
func (c *Controller) discardFailedRegistration(w http.ResponseWriter, user, id, kind string) bool {
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return true
	}
	defer tx.Rollback()
	var status, raw string
	if tx.QueryRow("SELECT o.status,r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind=?", id, kind).Scan(&status, &raw) != nil || status != "failed" {
		return false
	}
	if registrationApplied(raw) {
		fail(w, http.StatusConflict, "a host reports this registration as applied, so it is not a failed request record; remove it on the host instead of discarding it")
		return true
	}
	if _, err = tx.Exec("DELETE FROM managed_resources WHERE id=? AND kind=?", id, kind); err != nil {
		fail(w, http.StatusInternalServerError, "discarding the failed registration failed")
		return true
	}
	if c.audit(tx, user, strings.TrimSuffix(kind, "-register")+".discard", id) != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "discarding the failed registration failed")
		return true
	}
	write(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
	return true
}

// failedVMCreateRecordID is an internal primary key for a failed create's retained replay
// record. Valid guest IDs cannot contain a colon, so this cannot be a guest identity.
func failedVMCreateRecordID(operationID string) string { return "failed-vm-create:" + operationID }

func moveFailedVMCreateRecord(tx *sql.Tx, id string) error {
	_, err := tx.Exec("UPDATE managed_resources SET id='failed-vm-create:' || operation_id WHERE id=? AND kind='vm-create' AND operation_id IN (SELECT id FROM operations WHERE status='failed')", id)
	return err
}
func (c *Controller) listResources(w http.ResponseWriter, kind string) {
	rows, err := c.db.Query("SELECT r.id,r.command,o.id,o.host_id,o.kind,o.request_id,o.status,o.error,o.created_at,o.updated_at,o.actor FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind=? ORDER BY r.name,r.id", kind)
	if err != nil {
		fail(w, 500, "resource lookup failed")
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var rowID, raw, created, updated string
		var op protocol.Operation
		if rows.Scan(&rowID, &raw, &op.ID, &op.HostID, &op.Kind, &op.RequestID, &op.Status, &op.Error, &created, &updated, &op.Actor) != nil {
			fail(w, 500, "resource decode failed")
			return
		}
		var cmd protocol.Command
		if json.Unmarshal([]byte(raw), &cmd) != nil {
			fail(w, 500, "resource decode failed")
			return
		}
		out = append(out, resourceRowResponse(rowID, cmd, op))
	}
	if rows.Err() != nil {
		fail(w, 500, "resource read failed")
		return
	}
	write(w, 200, out)
}

type registrationRequest struct {
	RequestID string `json:"request_id"`
	PoolID    string `json:"pool_id"`
	HostID    string `json:"host_id"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Source    string `json:"source,omitempty"`
	Uplink    string `json:"uplink,omitempty"`
	VLAN      int    `json:"vlan,omitempty"`
}

var networkSource = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
var identifierValid = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString

func storagePathValid(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && len(path) <= 4096 && !strings.ContainsAny(path, "\x00\r\n")
}

// networkShapeValid accepts an applied registration: a VLAN with its derived bridge, or a
// bridge/libvirt network source with an optional uplink on bridges only.
func networkShapeValid(kind, source, uplink string, vlan int) bool {
	if kind == "vlan" {
		return protocol.ValidateVLAN(protocol.NetworkRegistration{Kind: "vlan", Uplink: uplink, VLAN: vlan, Source: source}) == nil
	}
	return (kind == "bridge" || kind == "network") && networkSource.MatchString(source) && vlan == 0 && (uplink == "" || (kind == "bridge" && protocol.ValidUplink(uplink)))
}

// networkKey names the host resource a registration claims; VLAN and plain bridge
// registrations share the bridge namespace.
func networkKey(kind, source string) string {
	if kind == "network" {
		return "network:" + source
	}
	return "bridge:" + source
}

func (c *Controller) storage(w http.ResponseWriter, r *http.Request, user, csrf string) {
	if r.Method == "GET" {
		c.listResources(w, "storage-register")
		return
	}
	c.registerStorage(w, r, user)
}
func (c *Controller) networks(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.registerResource(w, r, user, "network-register")
}
func (c *Controller) registerResource(w http.ResponseWriter, r *http.Request, user, kind string) {
	if r.Method == "GET" {
		c.listResources(w, kind)
		return
	}
	var v registrationRequest
	if !decode(w, r, &v) {
		return
	}
	if !nameValid(v.Name) || !nameValid(v.RequestID) || !nameValid(v.HostID) || !nameValid(v.PoolID) {
		fail(w, 400, "name, pool_id, host_id and stable request_id required")
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	if kind == "storage-register" {
		if !storagePathValid(v.Path) || v.Kind != "" || v.Source != "" || v.Uplink != "" || v.VLAN != 0 {
			fail(w, 400, "clean absolute existing storage path required")
			return
		}
	} else if v.Kind == "vlan" {
		// The bridge name is derived, so identical requests replay identically.
		if v.Path != "" || v.Source != "" {
			fail(w, 400, "VLAN networks take an uplink NIC and VLAN ID; the bridge name is derived")
			return
		}
		v.Source = protocol.GuestBridge(v.Uplink, v.VLAN)
		if err := protocol.ValidateVLAN(protocol.NetworkRegistration{Kind: "vlan", Uplink: v.Uplink, VLAN: v.VLAN, Source: v.Source}); err != nil {
			fail(w, 400, err.Error())
			return
		}
	} else if v.Path != "" || v.Uplink != "" || v.VLAN != 0 || (v.Kind != "bridge" && v.Kind != "network") || !networkSource.MatchString(v.Source) {
		fail(w, 400, "existing bridge or libvirt network source required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	// The stored payload is the request exactly as received, so an identical retry replays
	// even though a pool VLAN is re-homed onto the master below.
	received := v
	if c.replayResource(w, tx, v.RequestID, kind, received) {
		return
	}
	if err = c.managedHost(tx, v.PoolID, v.HostID); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if v.Kind == "vlan" {
		var master string
		if err = tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", v.PoolID).Scan(&master); err != nil || master == "" {
			fail(w, 409, "pool master is unavailable")
			return
		}
		// Pool VLANs are master-owned desired state. Members pull and materialize them.
		v.HostID = master
		inv, err := hostInventory(tx, v.HostID)
		if err != nil {
			fail(w, 500, "inventory unavailable")
			return
		}
		if err = uplinkObserved(inv, v.Uplink); err != nil {
			fail(w, 409, err.Error())
			return
		}
	}
	id, opID := random(), random()
	cmd := protocol.Command{OperationID: opID, Kind: kind}
	if kind == "storage-register" {
		cmd.Storage = &protocol.StorageRegistration{ID: id, PoolID: v.PoolID, HostID: v.HostID, Name: v.Name, Path: v.Path, OperationID: opID, Status: "queued"}
	} else {
		cmd.Network = &protocol.NetworkRegistration{ID: id, PoolID: v.PoolID, HostID: v.HostID, Name: v.Name, Kind: v.Kind, Source: v.Source, Uplink: v.Uplink, VLAN: v.VLAN, OperationID: opID, Status: "queued"}
		if v.Kind == "vlan" {
			cmd.Network.Generation = 1
		}
	}
	op, err := c.saveResource(tx, user, v.RequestID, v.PoolID, v.HostID, v.Name, id, cmd, received)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "registration storage failed")
		return
	}
	write(w, 201, resourceResponse(cmd, op))
}

// hostSupportsCapability reports whether the host's last report advertised a capability.
func hostSupportsCapability(tx *sql.Tx, host, capability string) bool {
	var raw string
	var caps []string
	return tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", host).Scan(&raw) == nil && json.Unmarshal([]byte(raw), &caps) == nil && slices.Contains(caps, capability)
}

// vmTagsSupported reports whether both the VM's host and its pool master journal tags; an
// older Core would drop them silently.
func vmTagsSupported(tx *sql.Tx, pool, host string) bool {
	master, _, err := poolMasterOf(tx, pool)
	return err == nil && hostSupportsCapability(tx, host, protocol.VMTagsCapability) && hostSupportsCapability(tx, master, protocol.VMTagsCapability)
}

type resourceLifecycleAPIRequest struct {
	RequestID          string `json:"request_id"`
	PoolID             string `json:"pool_id"`
	ExpectedGeneration uint64 `json:"expected_generation,omitempty"`
	Name               string `json:"name,omitempty"`
}

func (c *Controller) removeNetwork(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.networkLifecycle(w, r, user, "remove")
}
func (c *Controller) renameNetwork(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.networkLifecycle(w, r, user, "update")
}

// networkLifecycle relays a pool VLAN removal or rename to the pool master's Core, which does
// the work, journals the tombstone or the new generation, and propagates it to members. The
// Controller keeps nothing: the mirror row disappears only when the master reports the
// tombstone, and a fresh Controller rebuilds the same picture from that report alone.
func (c *Controller) networkLifecycle(w http.ResponseWriter, r *http.Request, user, action string) {
	id := r.PathValue("id")
	// A registration the master refused published nothing on any host, so discarding it is
	// local and takes no body. Answered before the removal body is even read, exactly as
	// deleteMedia answers a failed import.
	if action == "remove" && identifierValid(id) && c.discardFailedRegistration(w, user, id, "network-register") {
		return
	}
	var body resourceLifecycleAPIRequest
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if !identifierValid(id) || !nameValid(body.RequestID) || !nameValid(body.PoolID) {
		fail(w, http.StatusBadRequest, "network, pool_id and stable request_id required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	// The payload is stored exactly as received, so an identical retry replays and a
	// different body under the same request_id is refused.
	if c.replayResource(w, tx, body.RequestID, "network-"+action, body) {
		return
	}
	// The pool master owns pool VLANs; a client-named host is never trusted.
	var master string
	if err = tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", body.PoolID).Scan(&master); err != nil || master == "" {
		fail(w, http.StatusConflict, "pool master is unavailable")
		return
	}
	if err = c.managedHost(tx, body.PoolID, master); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	var raw, status string
	if err = tx.QueryRow("SELECT r.command,o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='network-register' AND r.pool_id=? AND r.host_id=?", id, body.PoolID, master).Scan(&raw, &status); err != nil || status != "succeeded" {
		fail(w, http.StatusNotFound, "network is not registered on this pool's master")
		return
	}
	var registered protocol.Command
	if json.Unmarshal([]byte(raw), &registered) != nil || registered.Network == nil {
		fail(w, http.StatusNotFound, "network is not registered on this pool's master")
		return
	}
	if !protocol.IsPoolSharedNetwork(*registered.Network) {
		fail(w, http.StatusConflict, "only pool VLAN networks can be removed")
		return
	}
	if protocol.NetworkGeneration(*registered.Network) != body.ExpectedGeneration {
		fail(w, http.StatusConflict, "network generation is no longer current; refresh and retry")
		return
	}
	v := protocol.ResourceLifecycleRequest{Resource: "network", Action: action, ResourceID: id, PoolID: body.PoolID, HostID: master, ExpectedGeneration: body.ExpectedGeneration, Name: body.Name}
	if err = protocol.ValidateResourceLifecycle(v); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "network-" + action, ResourceLifecycle: &v}
	op, err := c.saveResource(tx, user, body.RequestID, body.PoolID, master, registered.Network.Name, "network-operation-"+opID, cmd, body)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "network operation failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}

// adoptManaged is the managed step of a host report, run inside the report transaction after
// its results have been applied. It mirrors the pool-scoped configuration the host reports as
// applied from its journal, so a replaced Controller recovers a pool by connecting its master.
// Adopted entries keep their IDs and names, get the current pool/host, and are recorded as
// succeeded without a command: re-running them on the host would fail as already registered.
// The host's Controller appliance deployments become appliances rows, and journal entries that
// ended failed or needs_attention become operations with that status. An entry that is already
// held is silently current; one that is invalid, conflicting, unresolved or missing from libvirt
// is recorded once as a needs_attention operation plus an audit entry naming the reason.
func (c *Controller) adoptManaged(tx *sql.Tx, host string, inv *protocol.Inventory) error {
	m := inv.Managed
	if m == nil {
		return nil
	}
	var pool string
	if err := tx.QueryRow("SELECT pool_id FROM hosts WHERE id=?", host).Scan(&pool); err != nil {
		return err
	}
	taken := map[string]bool{}
	rows, err := tx.Query("SELECT r.kind,r.name,r.payload FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE (r.host_id=? AND r.kind IN ('storage-register','network-register') AND o.status!='failed') OR (r.pool_id=? AND r.kind='vm-create' AND o.status!='failed')", host, pool)
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind, name, raw string
		if err = rows.Scan(&kind, &name, &raw); err != nil {
			rows.Close()
			return err
		}
		var old registrationRequest
		json.Unmarshal([]byte(raw), &old)
		switch kind {
		case "storage-register":
			var storageOld storageRegisterRequest
			if json.Unmarshal([]byte(raw), &storageOld) == nil && (storageOld.Kind != "" || storageOld.Path != "") {
				taken[storageRegistrationKey(protocol.StorageRegistration{Path: storageOld.Path, Kind: storageOld.Kind, Source: storageOld.Source})] = true
			} else {
				taken["path:"+old.Path] = true
			}
		case "network-register":
			taken[networkKey(old.Kind, old.Source)] = true
		default:
			taken["vm:"+name] = true
		}
	}
	rows.Close()
	// Core clears operation fields from its steady-state repository projection. The matching
	// successful journal outcome carries the Controller operation ID and Core-allocated ID.
	storageOperation := map[string]string{}
	for _, outcome := range m.Outcomes {
		if outcome.Kind == "storage-register" && outcome.Status == "succeeded" && identifierValid(outcome.ID) && identifierValid(outcome.OperationID) {
			storageOperation[outcome.ID] = outcome.OperationID
		}
	}
	// A Core-provisioned ZFS repository has one canonical master projection even
	// when its scope selects only a member. If the master no longer reports that
	// registration, its durable Core record did not complete or was removed; do
	// not keep a successful Controller mirror from an earlier report.
	if err = c.reconcileZFSStorageProjections(tx, pool, host, m.Storage, inv.Checks); err != nil {
		return err
	}
	count := func(query string, args ...any) (int, error) {
		var n int
		err := tx.QueryRow(query, args...).Scan(&n)
		return n, err
	}
	// held reports an entry this Controller already knows under the same ID and kind: the
	// steady state of every report after adoption, which needs no record.
	held := func(id, kind string) (bool, error) {
		n, err := count("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind=? AND o.status!='failed'", id, kind)
		return n > 0, err
	}
	free := func(id, key string) (bool, error) {
		if !identifierValid(id) || taken[key] {
			return false, nil
		}
		n, err := count("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND o.status!='failed'", id)
		if err != nil || n > 0 {
			return false, err
		}
		taken[key] = true
		return true, nil
	}
	// attention records, once per host and entry, why the entry was not adopted.
	attention := func(kind, opKind, id, name, reason string) error {
		requestID := clip("adopt-attention:"+host+":"+kind+":"+id, 200)
		n, err := count("SELECT count(*) FROM operations WHERE request_id=?", requestID)
		if err != nil || n > 0 {
			return err
		}
		now := stamp()
		if _, err = tx.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", random(), host, opKind, requestID, "needs_attention", clip("not adopted from host report: "+reason, 4096), now, now, "system"); err != nil {
			return err
		}
		return auditDetail(tx, "adopt.attention", clip(id, 200), clip(opKind+" "+name+": "+reason, 4096))
	}
	// resolved closes the attention record of an entry that has since been adopted.
	resolved := func(kind, id string) error {
		_, err := tx.Exec("UPDATE operations SET status='succeeded',error='',updated_at=? WHERE request_id=? AND status='needs_attention'", stamp(), clip("adopt-attention:"+host+":"+kind+":"+id, 200))
		return err
	}
	save := func(kind, name, id string, cmd protocol.Command, payload any) error {
		if _, err := c.saveResourceStatus(tx, "system", "adopt-"+kind+":"+host+":"+id, pool, host, name, id, "succeeded", cmd, payload); err != nil {
			return err
		}
		return resolved(kind, id)
	}
	for _, s := range m.Storage {
		// A remote repository's configuration belongs to its master. Members may report
		// their own access while reconnecting, but that observation must never replace
		// the master's mirrored record or manufacture a second repository row.
		if m.Membership != nil && m.Membership.Valid() == nil && m.Membership.PoolID == pool && m.Membership.MasterHostID != host && (protocol.IsPoolSharedStorage(s) || s.ZFSProvisioned) && !s.Tombstone {
			continue
		}
		if s.OperationID == "" {
			s.OperationID = storageOperation[s.ID]
		}
		if s.Tombstone {
			if err = c.mirrorStorageProjection(tx, pool, host, s); err != nil {
				return err
			}
			continue
		}
		if ok, err := held(s.ID, "storage-register"); err != nil || ok {
			if err != nil {
				return err
			}
			if err = c.mirrorStorageProjection(tx, pool, host, s); err != nil {
				return err
			}
			continue
		}
		// A Controller-created registration has no repository ID until Core journals it.
		// Match that pending operation before source-conflict checks, then replace the
		// synthetic row with Core's durable identity.
		if s.OperationID != "" {
			var pending int
			if err = tx.QueryRow("SELECT count(*) FROM managed_resources WHERE operation_id=? AND kind='storage-register'", s.OperationID).Scan(&pending); err != nil {
				return err
			}
			if pending > 0 {
				if err = c.mirrorStorageProjection(tx, pool, host, s); err != nil {
					return err
				}
				continue
			}
		}
		reason := ""
		if !nameValid(s.Name) {
			reason = "invalid storage name"
		} else if err := protocol.ValidateStoredStorageRegistration(s, false); err != nil && (s.Kind != "" || !storagePathValid(s.Path)) {
			reason = "invalid storage registration"
		} else if ok, err := free(s.ID, storageRegistrationKey(s)); err != nil {
			return err
		} else if !ok {
			reason = "storage ID or source already registered"
		}
		if reason != "" {
			if err = attention("storage", "storage-register", s.ID, s.Name, reason); err != nil {
				return err
			}
			continue
		}
		if err = c.mirrorStorageProjection(tx, pool, host, s); err != nil {
			return err
		}
	}
	for _, n := range m.Networks {
		// Pool VLAN configuration belongs to the master. Members materialize bridges
		// locally and report access; they must never replace the master's mirrored row.
		if m.Membership != nil && m.Membership.Valid() == nil && m.Membership.PoolID == pool && m.Membership.MasterHostID != host && protocol.IsPoolSharedNetwork(n) {
			continue
		}
		// A host's own membership claim is not the authority: a member reporting without a
		// valid membership must not re-home, rename or remove a pool VLAN that the recorded
		// master of its pool already mirrors.
		if protocol.IsPoolSharedNetwork(n) || n.Tombstone {
			var owner string
			err := tx.QueryRow("SELECT p.master_host_id FROM managed_resources r JOIN pools p ON p.id=r.pool_id WHERE r.id=? AND r.kind='network-register'", n.ID).Scan(&owner)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if owner != "" && owner != host {
				continue
			}
		}
		// A removed network carries only its identity, so it is answered before any shape
		// check. The mirror is dropped only for the reporting host's own row, which keeps a
		// member's tombstone from deleting the master's pool VLAN.
		if n.Tombstone {
			if err = c.mirrorNetworkProjection(tx, pool, host, n); err != nil {
				return err
			}
			continue
		}
		if ok, err := held(n.ID, "network-register"); err != nil || ok {
			if err != nil {
				return err
			}
			// One unusable held row must not fail the whole report: record it the same way
			// an unadoptable entry is recorded and keep the held mirror. The projection keeps
			// the original operation and request, and follows a joined host into its new pool.
			if !nameValid(n.Name) || !networkShapeValid(n.Kind, n.Source, n.Uplink, n.VLAN) {
				if err = attention("network", "network-register", n.ID, n.Name, "invalid network name or shape"); err != nil {
					return err
				}
				continue
			}
			if err = c.mirrorNetworkProjection(tx, pool, host, n); err != nil {
				return err
			}
			if err = resolved("network", n.ID); err != nil {
				return err
			}
			continue
		}
		reason := ""
		if !nameValid(n.Name) || !networkShapeValid(n.Kind, n.Source, n.Uplink, n.VLAN) {
			reason = "invalid network name or shape"
		} else if ok, err := free(n.ID, networkKey(n.Kind, n.Source)); err != nil {
			return err
		} else if !ok {
			reason = "network ID or source already registered"
		}
		if reason != "" {
			if err = attention("network", "network-register", n.ID, n.Name, reason); err != nil {
				return err
			}
			continue
		}
		if err = c.mirrorNetworkProjection(tx, pool, host, n); err != nil {
			return err
		}
		// A network the host has since repaired closes its earlier attention record.
		if err = resolved("network", n.ID); err != nil {
			return err
		}
	}
	for _, md := range m.Media {
		// ISO library metadata on pool-shared storage belongs to the master. Members
		// verify the file on their mount and report access without minting a second row.
		if m.Membership != nil && m.Membership.Valid() == nil && m.Membership.PoolID == pool && m.Membership.MasterHostID != host {
			var raw string
			if err := tx.QueryRow("SELECT r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='storage-register' AND r.pool_id=? AND o.status='succeeded'", md.StorageID, pool).Scan(&raw); err == nil {
				var cmd protocol.Command
				if json.Unmarshal([]byte(raw), &cmd) == nil && cmd.Storage != nil && protocol.IsPoolSharedStorage(*cmd.Storage) {
					continue
				}
			}
		}
		// A removed ISO carries only its identity; the host_id predicate in the mirror keeps
		// a member's tombstone from deleting the owning host's row.
		if md.Tombstone {
			if err = c.mirrorMediaProjection(tx, pool, host, md); err != nil {
				return err
			}
			continue
		}
		if err := mirrorDownloadedMedia(tx, host, md); err != nil {
			return err
		}
		if ok, err := held(md.ID, "iso-import"); err != nil || ok {
			if err != nil {
				return err
			}
			// Only the owning host may refresh the mirrored command (including member_access).
			var owner string
			if err = tx.QueryRow("SELECT host_id FROM managed_resources WHERE id=? AND kind='iso-import'", md.ID).Scan(&owner); err != nil {
				return err
			}
			if owner == host {
				if err = c.mirrorMediaProjection(tx, pool, host, md); err != nil {
					return err
				}
			}
			continue
		}
		reason := ""
		if !nameValid(md.Name) || filepath.Base(md.Name) != md.Name || !strings.HasSuffix(strings.ToLower(md.Name), ".iso") || md.SizeBytes < 32774 || !sha256Hex(md.SHA256) {
			reason = "invalid ISO name, size or SHA-256"
		} else if _, e := c.readyStorageForHost(tx, md.StorageID, pool, host); e != nil {
			reason = "storage " + clip(md.StorageID, 64) + " is not adopted on this host"
		} else if ok, err := free(md.ID, "id:"+md.ID); err != nil {
			return err
		} else if !ok {
			reason = "ISO ID already held by another resource"
		}
		if reason != "" {
			if err = attention("media", "iso-import", md.ID, md.Name, reason); err != nil {
				return err
			}
			continue
		}
		v := mediaRequest{RequestID: "adopt-media:" + host + ":" + md.ID, StorageID: md.StorageID, Name: md.Name, SizeBytes: md.SizeBytes, SHA256: md.SHA256}
		opID := random()
		cmd := protocol.Command{OperationID: opID, Kind: "iso-import", Media: &protocol.Media{ID: md.ID, PoolID: pool, HostID: host, StorageID: md.StorageID, Name: md.Name, SizeBytes: md.SizeBytes, SHA256: md.SHA256, OperationID: opID, Status: "succeeded"}}
		if err = save("media", md.Name, md.ID, cmd, v); err != nil {
			return err
		}
	}
	missing := map[string]bool{}
	for _, id := range m.MissingVMs {
		missing[id] = true
	}
	for _, tomb := range m.DeletedVMs {
		if err = c.mirrorVMTombstone(tx, pool, host, tomb); err != nil {
			return err
		}
	}
	for _, s := range m.VMs {
		if ok, err := held(s.ID, "vm-create"); err != nil || ok {
			if err != nil {
				return err
			}
			// Held: the host's current definition is the truth, so the mirror follows it.
			if err = c.refreshVM(tx, pool, host, s); err != nil {
				return err
			}
			continue
		}
		reason := ""
		if protocol.ValidateVMSpec(s) != nil || s.CPU.Count > 64 || s.Memory.SizeMiB > 262144 {
			reason = "invalid VM name or specification"
		} else if missing[s.ID] {
			reason = "VM definition is missing from libvirt on the host"
		}
		for _, d := range s.Disks {
			if _, e := c.readyStorageForHost(tx, d.StorageID, pool, host); e != nil && reason == "" {
				reason = "storage " + clip(d.StorageID, 64) + " is not adopted on this host"
			}
		}
		for _, n := range s.NICs {
			if _, e := c.readyNetworkForHost(tx, n.NetworkID, pool, host); e != nil && reason == "" {
				reason = "network " + clip(n.NetworkID, 64) + " is not adopted on this host"
			}
		}
		for _, id := range []string{s.MediaID, s.DriversMediaID} {
			if _, e := c.readyMediaForHost(tx, id, pool, host); id != "" && e != nil && reason == "" {
				reason = "ISO " + clip(id, 64) + " is not adopted on this host"
			}
		}
		if reason == "" {
			if err = moveFailedVMCreateRecord(tx, s.ID); err != nil {
				return err
			}
			if ok, err := free(s.ID, "vm:"+s.Name); err != nil {
				return err
			} else if !ok {
				reason = "VM ID or name already held in the pool"
			}
		}
		if reason != "" {
			if err = attention("vm", "vm-create", s.ID, s.Name, reason); err != nil {
				return err
			}
			continue
		}
		spec := s
		spec.PoolID, spec.HostID = pool, host
		// Start after creation is a one-shot creation action, not part of the VM definition:
		// the creation it belonged to already finished, so a new Controller never repeats it.
		spec.StartAfterCreate = false
		payload := spec
		payload.ID = ""
		v := createVMRequest{RequestID: "adopt-vm:" + host + ":" + s.ID, VMCreateSpec: payload}
		cmd := protocol.Command{OperationID: random(), Kind: "vm-create", VM: &spec}
		if err = save("vm", s.Name, s.ID, cmd, v); err != nil {
			return err
		}
	}
	if err = c.adoptAppliances(tx, host, pool, inv, attention, resolved); err != nil {
		return err
	}
	// The host's operation ID is the Controller's when this Controller issued it; only a
	// replaced Controller lacks the row. Snapshot IDs that exist only in Core history use a
	// deterministic host-scoped Controller ID so two hosts cannot hide each other's rows.
	known := map[string]bool{}
	for start := 0; start < len(m.Outcomes); start += 10000 {
		batch := m.Outcomes[start:min(start+10000, len(m.Outcomes))]
		ids, requests := make([]any, 0, len(batch)*2), make([]any, 0, len(batch))
		for _, o := range batch {
			ids = append(ids, o.OperationID)
			if snapshotOutcomeKind(o.Kind) {
				ids = append(ids, adoptedSnapshotOperationID(host, o.OperationID))
			}
			if backupOutcomeKind(o.Kind) {
				ids = append(ids, adoptedBackupOperationID(host, o.OperationID))
			}
			requests = append(requests, "adopt-outcome:"+host+":"+o.OperationID)
		}
		idMarks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		requestMarks := strings.TrimSuffix(strings.Repeat("?,", len(requests)), ",")
		rows, err := tx.Query("SELECT id,request_id FROM operations WHERE id IN ("+idMarks+") OR request_id IN ("+requestMarks+")", append(ids, requests...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, request string
			if err = rows.Scan(&id, &request); err != nil {
				rows.Close()
				return err
			}
			known[id], known[request] = true, true
		}
		rows.Close()
	}
	// An adopted needs_attention outcome the host no longer reports was settled there (an
	// interrupted entry re-observed, or a pending command the restarted agent finished).
	// Snapshot work requires explicit owner settlement; a missing observation proves nothing.
	settled := []any{stamp(), host, "adopt-outcome:" + host + ":%"}
	for _, o := range m.Outcomes {
		settled = append(settled, "adopt-outcome:"+host+":"+o.OperationID)
	}
	if _, err := tx.Exec("UPDATE operations SET status='succeeded',error='',updated_at=? WHERE host_id=? AND status='needs_attention' AND kind NOT IN ('snapshot-create','snapshot-revert','snapshot-delete','snapshot-metadata','backup-now','backup-restore-as-new','backup-restore-replace','backup-test-restore','backup-repository-register','backup-repository-test','backup-policy-upsert','backup-policy-pause','backup-policy-run-now','backup-protect','backup-catalog-import','backup-catalog-rebuild','backup-test-verify','backup-test-cleanup') AND request_id LIKE ? AND request_id NOT IN ("+strings.TrimSuffix(strings.Repeat("?,", len(m.Outcomes)), ",")+")", settled...); err != nil {
		return err
	}
	for _, o := range m.Outcomes {
		// A creation's automatic start reports as <operation>:start.
		if !identifierValid(strings.TrimSuffix(o.OperationID, ":start")) || !managedKind(o.Kind) || (o.Status != "queued" && o.Status != "succeeded" && o.Status != "failed" && o.Status != "needs_attention") {
			continue
		}
		requestID := "adopt-outcome:" + host + ":" + o.OperationID
		knownOwnOperation := known[o.OperationID]
		// Snapshot and backup projection IDs can equal another host's or kind's
		// operation ID. Only this host's matching Controller command is its own;
		// otherwise the outcome needs a separate adopted mirror.
		if (snapshotOutcomeKind(o.Kind) || backupOutcomeKind(o.Kind)) && knownOwnOperation {
			var owned int
			if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations op ON op.id=r.operation_id WHERE r.operation_id=? AND r.host_id=? AND r.kind=? AND op.host_id=? AND op.kind=?", o.OperationID, host, o.Kind, host, o.Kind).Scan(&owned); err != nil {
				return err
			}
			knownOwnOperation = owned > 0
		}
		// A Core recovery outcome for this Controller's own operation is authoritative too.
		// Restrict the update to its immutable id/kind/host and nonterminal states so a
		// stale or cross-host report cannot alter a completed operation. Snapshot kinds use
		// the same path when this Controller issued the operation id.
		if knownOwnOperation && o.Status != "queued" {
			if _, err = tx.Exec("UPDATE operations SET status=?,error=?,updated_at=? WHERE id=? AND host_id=? AND kind=? AND status IN ('queued','dispatched','needs_attention')", o.Status, clip(o.Error, 4096), stamp(), o.OperationID, host, o.Kind); err != nil {
				return err
			}
		}
		// Adopted tasks are passive mirrors: only Core moves them, and they are never
		// dispatched by this Controller. Terminal history must not regress to queued.
		if known[requestID] && o.Status != "queued" {
			if _, err = tx.Exec("UPDATE operations SET status=?,error=?,updated_at=? WHERE host_id=? AND request_id=? AND status IN ('queued','dispatched','needs_attention')", o.Status, clip(o.Error, 4096), stamp(), host, requestID); err != nil {
				return err
			}
		}
		if knownOwnOperation || known[requestID] {
			continue
		}
		controllerID := o.OperationID
		if snapshotOutcomeKind(o.Kind) {
			controllerID = adoptedSnapshotOperationID(host, o.OperationID)
			if known[controllerID] {
				if err = attention("snapshot", o.Kind, o.OperationID, o.Name, "adopted snapshot operation ID conflicts with existing history"); err != nil {
					return err
				}
				continue
			}
		}
		if backupOutcomeKind(o.Kind) {
			controllerID = adoptedBackupOperationID(host, o.OperationID)
			if known[controllerID] {
				if err = attention("backup", o.Kind, o.OperationID, o.Name, "adopted backup operation ID conflicts with existing history"); err != nil {
					return err
				}
				continue
			}
		}
		now := stamp()
		if _, err = tx.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", controllerID, host, o.Kind, requestID, o.Status, clip(o.Error, 4096), now, now, "system"); err != nil {
			return err
		}
		known[controllerID], known[requestID] = true, true
		if err = auditDetail(tx, "adopt.outcome", clip(o.ID, 200), clip(o.Kind+" "+o.Name+" "+o.Status+": "+o.Error, 4096)); err != nil {
			return err
		}
	}
	return nil
}

func adoptedSnapshotOperationID(host, coreOperationID string) string {
	digest := sha256.Sum256([]byte(host + "\x00" + coreOperationID))
	return "snapshot-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func adoptedBackupOperationID(host, coreOperationID string) string {
	digest := sha256.Sum256([]byte(host + "\x00backup\x00" + coreOperationID))
	return "backup-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func snapshotOutcomeKind(kind string) bool {
	return kind == "snapshot-create" || kind == "snapshot-revert" || kind == "snapshot-delete" || kind == "snapshot-metadata"
}

func backupOutcomeKind(kind string) bool {
	return kind == "backup-now" || kind == "backup-restore-as-new" ||
		kind == "backup-restore-replace" || kind == "backup-test-restore" ||
		kind == "backup-repository-register" || kind == "backup-repository-test" ||
		kind == "backup-policy-upsert" || kind == "backup-policy-pause" || kind == "backup-policy-run-now" ||
		kind == "backup-protect" || kind == "backup-catalog-import" || kind == "backup-catalog-rebuild" ||
		kind == "backup-test-verify" || kind == "backup-test-cleanup"
}

// adoptAppliances rebuilds the appliances rows from the host's deploy journal. A row is
// inserted when the host observes the VM through healthy libvirt; a row held by a revoked
// host moves to the reporting host. POST /api/v1/appliances/adopt stays the handoff's path
// and replays as already adopted once the report has inserted the row.
func (c *Controller) adoptAppliances(tx *sql.Tx, host, pool string, inv *protocol.Inventory, attention func(kind, opKind, id, name, reason string) error, resolved func(kind, id string) error) error {
	libvirt := false
	for _, check := range inv.Checks {
		if check.Name == "libvirt" && check.Status == "healthy" {
			libvirt = true
		}
	}
	observed := map[string]bool{}
	for _, vm := range inv.VMs {
		observed[strings.ToLower(vm.ID)] = true
	}
	for _, ap := range inv.Managed.Appliances {
		reason := ""
		if !applianceUUID(ap.ID) || !nameValid(ap.Name) || !nameValid(ap.RequestID) || ap.Role != "controller" {
			reason = "invalid appliance record"
		} else if !observed[ap.ID] {
			if !libvirt {
				continue // unknown, not absent: the next healthy observation decides
			}
			reason = "deploy record present but libvirt does not list the VM"
		} else if n := 0; tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND o.status!='failed'", ap.ID).Scan(&n) == nil && n > 0 {
			reason = "VM belongs to a managed creation operation"
		}
		if reason != "" {
			if err := attention("appliance", "vm-create", ap.ID, ap.Name, "Controller appliance: "+reason); err != nil {
				return err
			}
			continue
		}
		var oldID, oldHost string
		var oldRevoked bool
		err := tx.QueryRow("SELECT a.id,a.host_id,h.revoked FROM appliances a JOIN hosts h ON h.id=a.host_id WHERE a.id=? OR a.request_id=? LIMIT 1", ap.ID, ap.RequestID).Scan(&oldID, &oldHost, &oldRevoked)
		switch {
		case err == sql.ErrNoRows:
			if _, err = tx.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", ap.ID, pool, host, strings.TrimSpace(ap.Name), ap.RequestID); err != nil {
				return err
			}
			if err = c.audit(tx, "system", "appliance.adopt", ap.ID); err != nil {
				return err
			}
		case err != nil:
			return err
		case oldID != ap.ID:
			if err = attention("appliance", "vm-create", ap.ID, ap.Name, "Controller appliance: request_id already belongs to another appliance"); err != nil {
				return err
			}
			continue
		case oldHost != host && !oldRevoked:
			if err = attention("appliance", "vm-create", ap.ID, ap.Name, "Controller appliance: owned by another active host"); err != nil {
				return err
			}
			continue
		case oldHost != host:
			if _, err = tx.Exec("UPDATE appliances SET pool_id=?,host_id=? WHERE id=?", pool, host, ap.ID); err != nil {
				return err
			}
			if err = c.audit(tx, "system", "appliance.adopt", ap.ID); err != nil {
				return err
			}
		}
		if err = resolved("appliance", ap.ID); err != nil {
			return err
		}
	}
	return nil
}

func managedKind(kind string) bool {
	switch kind {
	case "storage-register", "storage-update", "storage-recheck", "storage-detach", "storage-credential-retire", "network-register", "network-remove", "network-update", "iso-import", "media-remove", "vm-create", "vm-update", "snapshot-create", "snapshot-revert", "snapshot-delete", "snapshot-metadata", "backup-now", "backup-restore-as-new", "backup-restore-replace", "backup-test-restore", "backup-repository-register", "backup-repository-test", "backup-policy-upsert", "backup-policy-pause", "backup-policy-run-now", "backup-protect", "backup-catalog-import", "backup-catalog-rebuild", "backup-test-verify", "backup-test-cleanup", "vm-power", "vm-delete", "vm-media", "pool-join", "pool-maintenance", "pool-transfer", "pool-promote", "pool-fence-profile", "management-network":
		return true
	}
	return false
}

// clip bounds host-supplied text stored in Controller rows.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
func auditDetail(tx *sql.Tx, action, id, detail string) error {
	_, err := tx.Exec("INSERT INTO audit(actor,action,resource_id,created_at,detail) VALUES('system',?,?,?,?)", action, id, stamp(), detail)
	return err
}

// hostInventory returns the last observed inventory; an absent report decodes as empty.
func hostInventory(tx *sql.Tx, host string) (protocol.Inventory, error) {
	var raw sql.NullString
	var inv protocol.Inventory
	if err := tx.QueryRow("SELECT inventory FROM hosts WHERE id=?", host).Scan(&raw); err != nil {
		return inv, err
	}
	if raw.Valid {
		json.Unmarshal([]byte(raw.String), &inv)
	}
	return inv, nil
}

// uplinkObserved refuses uplinks the host has not reported as a physical NIC or bond, so a
// typo fails immediately instead of as a queued host task.
func uplinkObserved(inv protocol.Inventory, uplink string) error {
	for _, nic := range inv.NICs {
		if nic.Name == uplink {
			if nic.Kind == "" || nic.Kind == "physical" || nic.Kind == "bond" {
				return nil
			}
			return fmt.Errorf("uplink %s is a %s interface; choose a physical NIC or bond", uplink, nic.Kind)
		}
	}
	return fmt.Errorf("uplink %s has not been observed on this host", uplink)
}
func readyResource(tx *sql.Tx, id, kind, pool, host string) (protocol.Command, error) {
	var raw, status string
	var cmd protocol.Command
	err := tx.QueryRow("SELECT r.command,o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind=? AND r.pool_id=? AND r.host_id=?", id, kind, pool, host).Scan(&raw, &status)
	if err != nil || status != "succeeded" {
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	err = json.Unmarshal([]byte(raw), &cmd)
	return cmd, err
}

// readyStorageForHost accepts a pool remote repository on a member only when the
// master has just reported that member's matching, QEMU-safe access.  The row remains
// a disposable mirror of the master's record; this is not a Controller placement grant.
func (c *Controller) readyStorageForHost(tx *sql.Tx, id, pool, host string) (protocol.Command, error) {
	var raw, status string
	var cmd protocol.Command
	if err := tx.QueryRow("SELECT r.command,o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='storage-register' AND r.pool_id=?", id, pool).Scan(&raw, &status); err != nil || status != "succeeded" || json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Storage == nil {
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	s := cmd.Storage
	if s.ZFSProvisioned {
		now := time.Now()
		for _, access := range s.ZFSHostAccess {
			if access.HostID == host && access.Readable && access.Writable && access.Error == "" && access.Path != "" && !access.ObservedAt.IsZero() && !access.ObservedAt.After(now) && now.Sub(access.ObservedAt) <= c.cfg.StaleAfter {
				return cmd, nil
			}
		}
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	if s.HostID == host {
		return cmd, nil
	}
	if !protocol.IsPoolSharedStorage(*s) {
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	if protocol.IsClusteredISCSI(*s) {
		eligible := false
		for _, member := range s.Source.ISCSI.EligibleHostIDs {
			eligible = eligible || member == host
		}
		if !eligible {
			return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
		}
	}
	now := time.Now()
	for _, access := range s.MemberAccess {
		if access.RepositoryID == s.ID && access.HostID == host && access.Generation == s.Generation && access.Identity != "" && access.Readable && access.Writable && access.QEMUWritable && access.Locking && access.Error == "" && !access.ObservedAt.IsZero() && !access.ObservedAt.After(now) && now.Sub(access.ObservedAt) <= c.cfg.StaleAfter {
			return cmd, nil
		}
	}
	return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
}

// readyNetworkForHost accepts a pool VLAN on a member when the master reported that
// member's successful apply. Host-local bridges still require a same-host registration.
func (c *Controller) readyNetworkForHost(tx *sql.Tx, id, pool, host string) (protocol.Command, error) {
	if cmd, err := readyResource(tx, id, "network-register", pool, host); err == nil {
		return cmd, nil
	}
	var raw, status string
	var cmd protocol.Command
	if err := tx.QueryRow("SELECT r.command,o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='network-register' AND r.pool_id=?", id, pool).Scan(&raw, &status); err != nil || status != "succeeded" || json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Network == nil {
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	n := cmd.Network
	if n.HostID == host || !protocol.IsPoolSharedNetwork(*n) {
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	generation := protocol.NetworkGeneration(*n)
	now := time.Now()
	for _, access := range n.MemberAccess {
		if access.NetworkID == n.ID && access.HostID == host && access.Generation == generation && access.UplinkPresent && access.Bridge == n.Source && access.Error == "" && !access.ObservedAt.IsZero() && !access.ObservedAt.After(now) && now.Sub(access.ObservedAt) <= c.cfg.StaleAfter {
			return cmd, nil
		}
	}
	return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
}

// readyMediaForHost accepts an ISO on a member when it is registered on that host, or when
// it lives on pool-shared storage and the master reported that member's verified digest.
func (c *Controller) readyMediaForHost(tx *sql.Tx, id, pool, host string) (protocol.Command, error) {
	if id == "" {
		return protocol.Command{}, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	if cmd, err := readyResource(tx, id, "iso-import", pool, host); err == nil {
		return cmd, nil
	}
	var raw, status string
	var cmd protocol.Command
	if err := tx.QueryRow("SELECT r.command,o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='iso-import' AND r.pool_id=?", id, pool).Scan(&raw, &status); err != nil || status != "succeeded" || json.Unmarshal([]byte(raw), &cmd) != nil || cmd.Media == nil {
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	md := cmd.Media
	if md.HostID == host {
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	if _, err := c.readyStorageForHost(tx, md.StorageID, pool, host); err != nil {
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	now := time.Now()
	for _, access := range md.MemberAccess {
		if access.MediaID == md.ID && access.HostID == host && access.StorageID == md.StorageID && access.SHA256 == md.SHA256 && access.Error == "" && !access.ObservedAt.IsZero() && !access.ObservedAt.After(now) && now.Sub(access.ObservedAt) <= c.cfg.StaleAfter {
			return cmd, nil
		}
	}
	return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
}

// mirrorNetworkProjection refreshes the Controller's disposable network projection from Core.
func (c *Controller) mirrorNetworkProjection(tx *sql.Tx, pool, host string, network protocol.NetworkRegistration) error {
	// Core durably removed this network: drop the disposable mirror. Nothing recreates it,
	// because adoption only ever mirrors what a host still reports.
	if network.Tombstone {
		_, err := tx.Exec("DELETE FROM managed_resources WHERE id=? AND kind='network-register' AND pool_id=? AND host_id=?", network.ID, pool, host)
		return err
	}
	if !nameValid(network.Name) || !networkShapeValid(network.Kind, network.Source, network.Uplink, network.VLAN) {
		return fmt.Errorf("invalid network projection")
	}
	network.PoolID, network.HostID = pool, host
	if protocol.IsPoolSharedNetwork(network) && network.Generation == 0 {
		network.Generation = 1
	}
	var opID string
	err := tx.QueryRow("SELECT operation_id FROM managed_resources WHERE id=? AND kind='network-register'", network.ID).Scan(&opID)
	if err == nil {
		network.OperationID, network.Status, network.Error = opID, "succeeded", ""
		command, commandErr := json.Marshal(protocol.Command{OperationID: opID, Kind: "network-register", Network: &network})
		if commandErr != nil {
			return fmt.Errorf("network projection encoding failed")
		}
		_, err = tx.Exec("UPDATE managed_resources SET pool_id=?,host_id=?,name=?,command=? WHERE id=? AND kind='network-register'", pool, host, network.Name, string(command), network.ID)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	opID = random()
	network.OperationID, network.Status, network.Error = opID, "succeeded", ""
	cmd := protocol.Command{OperationID: opID, Kind: "network-register", Network: &network}
	payload := registrationRequest{RequestID: "adopt-network:" + host + ":" + network.ID, PoolID: pool, HostID: host, Name: strings.TrimSpace(network.Name), Kind: network.Kind, Source: network.Source, Uplink: network.Uplink, VLAN: network.VLAN}
	_, err = c.saveResourceStatus(tx, "system", payload.RequestID, pool, host, network.Name, network.ID, "succeeded", cmd, payload)
	return err
}

// mirrorMediaProjection refreshes media rows, including member_access from the master.
func (c *Controller) mirrorMediaProjection(tx *sql.Tx, pool, host string, media protocol.Media) error {
	if media.Tombstone {
		_, err := tx.Exec("DELETE FROM managed_resources WHERE id=? AND kind='iso-import' AND pool_id=? AND host_id=?", media.ID, pool, host)
		return err
	}
	media.PoolID, media.HostID = pool, host
	var opID string
	err := tx.QueryRow("SELECT operation_id FROM managed_resources WHERE id=? AND kind='iso-import'", media.ID).Scan(&opID)
	if err != nil {
		return nil
	}
	media.OperationID, media.Status, media.Error, media.URL = opID, "succeeded", "", ""
	command, commandErr := json.Marshal(protocol.Command{OperationID: opID, Kind: "iso-import", Media: &media})
	if commandErr != nil {
		return fmt.Errorf("media projection encoding failed")
	}
	_, err = tx.Exec("UPDATE managed_resources SET pool_id=?,host_id=?,name=?,command=? WHERE id=? AND kind='iso-import'", pool, host, media.Name, string(command), media.ID)
	return err
}

type createVMRequest struct {
	RequestID string `json:"request_id"`
	protocol.VMCreateSpec
}

func availableCPUModel(inventory *protocol.Inventory, model string) bool {
	return model == "host-passthrough" || model == "host-model" || slices.Contains(inventory.CPUModels, model)
}

// Explicit CPU definitions are interpreted by Core, not filtered against model-name
// inventory: a native common baseline can include required and disabled feature overrides.
func cpuFeaturesSupported(tx *sql.Tx, spec protocol.VMCreateSpec) error {
	if len(spec.CPU.Features) == 0 {
		return nil
	}
	var raw string
	var caps []string
	if err := tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", spec.HostID).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &caps) != nil || !slices.Contains(caps, protocol.CPUBaselineCapability) {
		return fmt.Errorf("the selected host does not support fixed CPU features; upgrade Nex Core")
	}
	return nil
}

// vmCreateWarnings names the shared pool resources this Controller's mirror does not
// currently show the selected host able to use. It is advisory and nothing else: Core owns
// placement (see createVM below), refuses again on its own evidence, and may well accept a
// request warned about here once the host reports working access. The warnings exist so an
// operator can tell a request that is merely slow from one aimed at a host whose access to a
// required repository, network or ISO the master last reported as failed or stale.
func (c *Controller) vmCreateWarnings(tx *sql.Tx, spec protocol.VMCreateSpec) []string {
	var out []string
	seen := map[string]bool{}
	warn := func(kind, id string) {
		if id == "" || seen[kind+id] {
			return
		}
		seen[kind+id] = true
		out = append(out, fmt.Sprintf("the pool master's last report does not show this host's access to %s %s as current; the request may sit dispatched until it does", kind, id))
	}
	for _, disk := range spec.Disks {
		if _, err := c.readyStorageForHost(tx, disk.StorageID, spec.PoolID, spec.HostID); err != nil {
			warn("storage repository", disk.StorageID)
		}
	}
	for _, nic := range spec.NICs {
		if _, err := c.readyNetworkForHost(tx, nic.NetworkID, spec.PoolID, spec.HostID); err != nil {
			warn("network", nic.NetworkID)
		}
	}
	for _, media := range []string{spec.MediaID, spec.DriversMediaID} {
		if media == "" {
			continue
		}
		if _, err := c.readyMediaForHost(tx, media, spec.PoolID, spec.HostID); err != nil {
			warn("ISO", media)
		}
	}
	return out
}

func (c *Controller) createVM(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v createVMRequest
	if !decode(w, r, &v) {
		return
	}
	if v.ID != "" || !nameValid(v.RequestID) {
		fail(w, 400, "valid name and stable request_id required; the Controller assigns the VM identity")
		return
	}
	slices.Sort(v.Tags)
	v.Tags = slices.Compact(v.Tags)
	if err := protocol.ValidateVMSpec(v.VMCreateSpec); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if v.CPU.Count > 64 || v.Memory.SizeMiB > 262144 {
		fail(w, 400, "a VM allows at most 64 vCPUs and 262144 MiB of memory")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, v.RequestID, "vm-create", v) {
		return
	}
	if err = c.placementHost(tx, v.PoolID, v.HostID); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if err := cpuFeaturesSupported(tx, v.VMCreateSpec); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if len(v.Tags) > 0 && !vmTagsSupported(tx, v.PoolID, v.HostID) {
		fail(w, 409, "the selected host or its pool master does not support VM tags; upgrade Nex Core")
		return
	}
	if v.DriversMediaID != "" {
		var raw string
		var caps []string
		tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", v.HostID).Scan(&raw)
		json.Unmarshal([]byte(raw), &caps)
		if !slices.Contains(caps, protocol.DriversDriveCapability) {
			fail(w, 409, "the selected host agent does not support a driver disc drive; upgrade Nex Core")
			return
		}
	}
	// Core validates resources, capacity, CPU compatibility, identity and admission from
	// its master-owned state. Controller mirrors are presentation only.
	spec := v.VMCreateSpec
	b := random()[:32]
	spec.ID = fmt.Sprintf("%s-%s-4%s-a%s-%s", b[:8], b[8:12], b[13:16], b[17:20], b[20:])
	cmd := protocol.Command{OperationID: random(), Kind: "vm-create", VM: &spec}
	warnings := c.vmCreateWarnings(tx, spec)
	op, err := c.saveResource(tx, user, v.RequestID, v.PoolID, v.HostID, v.Name, spec.ID, cmd, v)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "VM operation storage failed")
		return
	}
	response := resourceResponse(cmd, op)
	if body, ok := response.(map[string]any); ok && len(warnings) > 0 {
		body["warnings"] = warnings
	}
	write(w, 202, response)
}

// refreshVM makes the Controller's mirror of a VM it already holds match the definition its
// host reports as current, so an edit's result is the host's word and a Controller that missed
// it converges on the next report. The row's payload keeps the original request for replay.
func (c *Controller) refreshVM(tx *sql.Tx, pool, host string, reported protocol.VMCreateSpec) error {
	if protocol.ValidateVMSpec(reported) != nil {
		return nil
	}
	var raw string
	err := tx.QueryRow("SELECT r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='vm-create' AND r.id=? AND r.host_id=? AND o.status='succeeded'", reported.ID, host).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var cmd protocol.Command
	if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.VM == nil {
		return nil
	}
	next := reported
	next.PoolID, next.HostID, next.StartAfterCreate = pool, host, false
	held := *cmd.VM
	held.StartAfterCreate = false
	a, _ := json.Marshal(held)
	b, _ := json.Marshal(next)
	if string(a) == string(b) {
		return nil
	}
	cmd.VM = &next
	command, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE managed_resources SET pool_id=?,host_id=?,command=?,name=? WHERE id=? AND kind='vm-create'", pool, host, string(command), next.Name, next.ID)
	return err
}

type updateVMRequest struct {
	RequestID string `json:"request_id"`
	protocol.VMCreateSpec
}

// vmDefinition returns the mirrored current definition of a successfully created guest VM.
func vmDefinition(tx *sql.Tx, id string) (protocol.VMCreateSpec, error) {
	var raw string
	var cmd protocol.Command
	err := tx.QueryRow("SELECT r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='vm-create' AND r.id=? AND o.status='succeeded'", id).Scan(&raw)
	if err != nil {
		return protocol.VMCreateSpec{}, err
	}
	if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.VM == nil {
		return protocol.VMCreateSpec{}, fmt.Errorf("VM definition invalid")
	}
	return *cmd.VM, nil
}

// updateVM queues one revision of a VM's definition for its assigned host. It validates the
// shape the way creation does, refuses what no edit may change and what the observed state
// cannot take (so the operator hears it before a task is queued), and freezes every automatic
// MAC into the definition so interfaces keep their identity across later edits. Name and MAC
// uniqueness against other VMs and dedicated-memory admission are the host's, as for creation;
// Core's own observation of the guest remains the authority on what applies live.
func (c *Controller) updateVM(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v updateVMRequest
	if !decode(w, r, &v) {
		return
	}
	id := r.PathValue("id")
	if (v.ID != "" && v.ID != id) || !nameValid(v.RequestID) {
		fail(w, 400, "valid name and stable request_id required; id must match the path or be omitted")
		return
	}
	if v.StartAfterCreate {
		fail(w, 400, "start_after_create applies to creation only; use the power controls")
		return
	}
	slices.Sort(v.Tags)
	v.Tags = slices.Compact(v.Tags)
	if err := protocol.ValidateVMSpec(v.VMCreateSpec); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if v.CPU.Count > 64 || v.Memory.SizeMiB > 262144 {
		fail(w, 400, "a VM allows at most 64 vCPUs and 262144 MiB of memory")
		return
	}
	payload := v
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, v.RequestID, "vm-update", payload) {
		return
	}
	var appliance int
	if tx.QueryRow("SELECT count(*) FROM appliances WHERE id=?", id).Scan(&appliance) == nil && appliance > 0 {
		fail(w, 409, "the Controller appliance is managed from its host's local page, not edited here")
		return
	}
	cur, err := vmDefinition(tx, id)
	if err != nil {
		fail(w, 404, "VM not found")
		return
	}
	if v.PoolID != cur.PoolID || v.HostID != cur.HostID {
		fail(w, 400, "editing does not move a VM to another pool or host")
		return
	}
	spec := v.VMCreateSpec
	// Frozen addresses go into the dispatched definition, never into the replay payload.
	spec.NICs = append([]protocol.VMNIC{}, spec.NICs...)
	spec.ID, spec.Revision = id, cur.Revision+1
	// An unnamed interface is matched to the current one at its position. That is unambiguous only
	// for an unchanged interface when none is removed, or for one appended past the current count;
	// otherwise (removal, reorder, remove-plus-add, reconfiguration) the interface must carry its MAC.
	// A request that names any current interface by its MAC (as the bundled editor does for all
	// of them) identifies interfaces by address, so an unnamed interface in it is new.
	taken := map[string]bool{}
	for _, nic := range spec.NICs {
		taken[nic.MAC] = true
	}
	named := false
	for i, n := range cur.NICs {
		named = named || taken[protocol.NICMAC(cur.ID, i, n)]
	}
	for i, n := range spec.NICs {
		if named || n.MAC != "" || i >= len(cur.NICs) {
			continue
		}
		was := cur.NICs[i]
		was.MAC = ""
		if len(spec.NICs) < len(cur.NICs) || n != was {
			fail(w, 400, "when removing, reordering or changing a network interface, give every existing interface its MAC address")
			return
		}
		// The unchanged interface at this position keeps its address.
		spec.NICs[i].MAC = protocol.NICMAC(cur.ID, i, cur.NICs[i])
		taken[spec.NICs[i].MAC] = true
	}
	// Interfaces keep their identity by address: every automatic MAC is frozen into the
	// definition here, drawn from the VM's derived range at the first index above the highest one
	// the current definition uses, skipping any address the request or current definition holds.
	// An interface removed from a running VM in an earlier save can stay live until the guest
	// acknowledges the unplug, which the Controller cannot see, so refilling the lowest free index
	// would hand that address out again. Once no index above is free (the highest-index interface
	// was removed, or index 255 is used) the lowest free index is taken; Core refuses an address
	// that is still live. Freezing precedes the stop check so the Controller judges the definition
	// it dispatches. Uniqueness against other VMs is the host's check.
	next := 0
	for i, nic := range cur.NICs {
		mac := protocol.NICMAC(cur.ID, i, nic)
		taken[mac] = true
		for k := next; k < 256; k++ {
			if protocol.DerivedMAC(id, k) == mac {
				next = k + 1
			}
		}
	}
	for i := range spec.NICs {
		if spec.NICs[i].MAC != "" {
			continue
		}
		// Indexes next..255 first, then 0..next-1: the lowest free index once none above is free.
		for n := 0; n < 256; n++ {
			if mac := protocol.DerivedMAC(id, (next+n)%256); !taken[mac] {
				spec.NICs[i].MAC, taken[mac] = mac, true
				break
			}
		}
		if spec.NICs[i].MAC == "" {
			fail(w, 409, "could not derive a free MAC address for this VM; set it manually")
			return
		}
	}
	stops, err := protocol.NeedsStop(cur, spec)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err = c.placementHost(tx, cur.PoolID, cur.HostID); err != nil {
		fail(w, 409, err.Error())
		return
	}
	var raw string
	var caps []string
	tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", cur.HostID).Scan(&raw)
	json.Unmarshal([]byte(raw), &caps)
	if !slices.Contains(caps, protocol.UpdateCapability) {
		fail(w, 409, "the VM's host agent does not support editing VMs; upgrade Nex Core")
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind IN ('vm-update','vm-power','vm-media','vm-delete') AND r.name=? AND o.status IN ('queued','dispatched')", id).Scan(&pending); err != nil {
		fail(w, 500, "pending request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a change, power or media request for this VM is still pending; wait for its task")
		return
	}
	var revoked bool
	var inventory sql.NullString
	if tx.QueryRow("SELECT revoked,inventory FROM hosts WHERE id=?", cur.HostID).Scan(&revoked, &inventory) == nil {
		if state, _, _, _ := c.observeVM(cur.HostID, id, revoked, inventory); state == "running" && len(stops) > 0 {
			names := make([]string, len(stops))
			for i, s := range stops {
				names[i] = string(s)
			}
			fail(w, 409, "stop the VM first to change "+strings.Join(names, ", "))
			return
		}
	}
	if err := cpuFeaturesSupported(tx, spec); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if spec.DriversMediaID != "" && !slices.Contains(caps, protocol.DriversDriveCapability) {
		fail(w, 409, "the VM's host agent does not support a driver disc drive; upgrade Nex Core")
		return
	}
	if len(spec.Tags) > 0 && !vmTagsSupported(tx, cur.PoolID, cur.HostID) {
		fail(w, 409, "the VM's host or its pool master does not support VM tags; upgrade Nex Core")
		return
	}
	cmd := protocol.Command{OperationID: random(), Kind: "vm-update", VM: &spec}
	op, err := c.saveResource(tx, user, v.RequestID, cur.PoolID, cur.HostID, id, random(), cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "VM operation storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}
