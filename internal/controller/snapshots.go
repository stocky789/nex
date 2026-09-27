// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"nex.local/nex/internal/protocol"
)

type createSnapshotRequest struct {
	RequestID   string `json:"request_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

type snapshotOperationRequest struct {
	RequestID string `json:"request_id"`
}

type updateSnapshotMetadataRequest struct {
	RequestID   string `json:"request_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// listVMSnapshots returns the pool master's compact catalog for one VM.
func (c *Controller) listVMSnapshots(w http.ResponseWriter, r *http.Request, user, csrf string) {
	vmID := r.PathValue("id")
	_, pool, _, _, err := c.vmPlacement(vmID)
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
	if inv.Managed == nil || !inv.Managed.PoolVMSnapshotsReported {
		fail(w, 503, "snapshot catalog is unavailable from the pool master")
		return
	}
	out := make([]protocol.PoolVMSnapshot, 0)
	for _, snapshot := range inv.Managed.PoolVMSnapshots {
		if snapshot.VMID == vmID {
			out = append(out, snapshot)
		}
	}
	write(w, 200, out)
}

func (c *Controller) createVMSnapshot(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body createSnapshotRequest
	if !decode(w, r, &body) {
		return
	}
	req := protocol.VMSnapshotRequest{Name: body.Name, Description: body.Description, Mode: body.Mode}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || req.ValidFor("snapshot-create") != nil {
		fail(w, 400, "stable request_id and snapshot name required")
		return
	}
	c.enqueueVMSnapshot(w, r.PathValue("id"), user, body.RequestID, body.RequestID, "snapshot-create", &req, body)
}

func (c *Controller) revertVMSnapshot(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body snapshotOperationRequest
	if !decode(w, r, &body) {
		return
	}
	snapshotID := r.PathValue("snapshot_id")
	req := protocol.VMSnapshotRequest{SnapshotID: snapshotID}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || body.RequestID == snapshotID ||
		!protocol.ValidSnapshotOperationID(snapshotID) {
		fail(w, 400, "stable request_id distinct from snapshot_id required")
		return
	}
	c.enqueueVMSnapshot(w, r.PathValue("id"), user, body.RequestID, body.RequestID, "snapshot-revert", &req, body)
}

func (c *Controller) deleteVMSnapshot(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body snapshotOperationRequest
	if !decode(w, r, &body) {
		return
	}
	snapshotID := r.PathValue("snapshot_id")
	req := protocol.VMSnapshotRequest{SnapshotID: snapshotID}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || body.RequestID == snapshotID ||
		!protocol.ValidSnapshotOperationID(snapshotID) {
		fail(w, 400, "stable request_id distinct from snapshot_id required")
		return
	}
	c.enqueueVMSnapshot(w, r.PathValue("id"), user, body.RequestID, body.RequestID, "snapshot-delete", &req, body)
}

func (c *Controller) updateVMSnapshotMetadata(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body updateSnapshotMetadataRequest
	if !decode(w, r, &body) {
		return
	}
	vmID, snapshotID := r.PathValue("id"), r.PathValue("snapshot_id")
	req := protocol.VMSnapshotRequest{SnapshotID: snapshotID, Name: body.Name, Description: body.Description}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || !protocol.ValidSnapshotOperationID(snapshotID) ||
		req.ValidFor("snapshot-metadata") != nil {
		fail(w, 400, "stable request_id, snapshot_id and name required")
		return
	}
	c.enqueueVMSnapshot(w, vmID, user, body.RequestID, body.RequestID, "snapshot-metadata", &req, body)
}

func (c *Controller) enqueueVMSnapshot(w http.ResponseWriter, vmID, user, requestID, operationID, kind string, snapshot *protocol.VMSnapshotRequest, payload any) {
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
	if c.replayResource(w, tx, requestID, kind, payload) {
		return
	}
	if err = c.managedHost(tx, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.name=? AND o.status IN ('queued','dispatched') AND r.kind IN ('vm-update','vm-power','vm-media','vm-delete','snapshot-create','snapshot-revert','snapshot-delete','snapshot-metadata')", vmID).Scan(&pending); err != nil {
		fail(w, 500, "pending request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a change, power, media, delete or snapshot request for this VM is still pending; wait for its task")
		return
	}
	// Client request_id is the Core operation id; keep a distinct managed_resources row id so
	// it cannot collide with a VM or other resource primary key that shares the same string.
	var held int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources WHERE id=? OR operation_id=?", operationID, operationID).Scan(&held); err != nil {
		fail(w, 500, "snapshot identity lookup failed")
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
	rowID := "snapshot:" + requestID
	cmd := protocol.Command{OperationID: operationID, Kind: kind, VM: &vm, Snapshot: snapshot}
	op, err := c.saveResource(tx, user, requestID, pool, host, vmID, rowID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "snapshot request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}

func (c *Controller) currentManagedVMSpec(tx *sql.Tx, host, vmID string) (protocol.VMCreateSpec, error) {
	inv, err := hostInventory(tx, host)
	if err == nil && inv.Managed != nil {
		for _, vm := range inv.Managed.VMs {
			if vm.ID == vmID {
				return vm, nil
			}
		}
	}
	var command string
	err = tx.QueryRow("SELECT r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='vm-create' AND r.id=? AND o.status='succeeded'", vmID).Scan(&command)
	if err == sql.ErrNoRows {
		return protocol.VMCreateSpec{}, errors.New("current VM definition is unavailable")
	}
	if err != nil {
		return protocol.VMCreateSpec{}, err
	}
	var cmd protocol.Command
	if json.Unmarshal([]byte(command), &cmd) != nil || cmd.VM == nil || cmd.VM.ID != vmID {
		return protocol.VMCreateSpec{}, errors.New("current VM definition is unavailable")
	}
	return *cmd.VM, nil
}
