// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"nex.local/nex/internal/protocol"
	"slices"
)

type deleteVMRequest struct {
	RequestID   string `json:"request_id"`
	DeleteDisks bool   `json:"delete_disks"`
}

// deleteVM queues removal of a stopped guest VM on its assigned host. Disk files are kept
// unless delete_disks is true. The Controller appliance cannot be deleted here.
func (c *Controller) deleteVM(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v deleteVMRequest
	if !decode(w, r, &v) {
		return
	}
	if !nameValid(v.RequestID) {
		fail(w, 400, "stable request_id required")
		return
	}
	id := r.PathValue("id")
	host, pool, revoked, inventory, err := c.vmPlacement(id)
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
	payload := v
	if c.replayResource(w, tx, v.RequestID, "vm-delete", payload) {
		return
	}
	var appliance int
	if tx.QueryRow("SELECT count(*) FROM appliances WHERE id=?", id).Scan(&appliance) == nil && appliance > 0 {
		fail(w, 409, "the Controller appliance is managed from its host's local page, not deleted here")
		return
	}
	if _, err = vmDefinition(tx, id); err != nil {
		fail(w, 404, "VM not found")
		return
	}
	if err = c.managedHost(tx, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	var raw string
	var caps []string
	tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", host).Scan(&raw)
	json.Unmarshal([]byte(raw), &caps)
	if !slices.Contains(caps, protocol.DeleteCapability) {
		fail(w, 409, "the VM's host agent does not support deleting VMs; upgrade Nex Core")
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind IN ('vm-update','vm-power','vm-media','vm-delete') AND r.name=? AND o.status IN ('queued','dispatched')", id).Scan(&pending); err != nil {
		fail(w, 500, "pending request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a change, power, media or delete request for this VM is still pending; wait for its task")
		return
	}
	state, freshness, _, _ := c.observeVM(host, id, revoked, inventory)
	if freshness != "fresh" {
		fail(w, 409, "current VM state is unknown; wait for a fresh host report")
		return
	}
	if state != "shut off" {
		fail(w, 409, "stop the VM before deleting it")
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: id, PoolID: pool, HostID: host, DeleteDisks: v.DeleteDisks}}
	op, err := c.saveResource(tx, user, v.RequestID, pool, host, id, opID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "delete request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}

// mirrorVMTombstone drops the Controller mirror when Core reports an intentional delete.
func (c *Controller) mirrorVMTombstone(tx *sql.Tx, pool, host string, tomb protocol.VMTombstone) error {
	if !tomb.Tombstone || tomb.ID == "" {
		return nil
	}
	_, err := tx.Exec("DELETE FROM managed_resources WHERE id=? AND kind='vm-create' AND pool_id=? AND host_id=?", tomb.ID, pool, host)
	return err
}
