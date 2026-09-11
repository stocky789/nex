// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"nex.local/nex/internal/protocol"
	"slices"
)

type powerRequest struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
}

// powerVM queues a durable start/shutdown/reboot/force-stop request for the VM's assigned
// host. Shutdown and reboot are ACPI requests the guest may ignore; the observed state,
// not the task result, establishes what happened.
func (c *Controller) powerVM(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v powerRequest
	if !decode(w, r, &v) {
		return
	}
	if !nameValid(v.RequestID) || !protocol.ValidPowerAction(v.Action) {
		fail(w, 400, "stable request_id and action start, shutdown, reboot or force-stop required")
		return
	}
	id := r.PathValue("id")
	host, pool, _, _, err := c.vmPlacement(id)
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
	payload := map[string]string{"vm_id": id, "request_id": v.RequestID, "action": v.Action}
	if c.replayResource(w, tx, v.RequestID, "vm-power", payload) {
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
	if !slices.Contains(caps, protocol.PowerCapability) {
		fail(w, 409, "the VM's host agent does not support central power requests; upgrade Nex Core")
		return
	}
	// Ask Core to run after the latest pending request for this VM. Core owns the
	// durable queue and checks that prerequisite before any effects.
	var after string
	err = tx.QueryRow("SELECT o.id FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind IN ('vm-power','vm-update','vm-media') AND r.name=? AND o.status IN ('queued','dispatched') ORDER BY o.rowid DESC LIMIT 1", id).Scan(&after)
	if err != nil && err != sql.ErrNoRows {
		fail(w, 500, "power request lookup failed")
		return
	}
	if after != "" && !slices.Contains(caps, protocol.PowerQueueCapability) {
		fail(w, 409, "a power request for this VM or a change to it is still pending; upgrade Nex Core to queue power requests")
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: id, PoolID: pool, HostID: host, Action: v.Action, AfterOperationID: after}}
	op, err := c.saveResource(tx, user, v.RequestID, pool, host, id, opID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "power request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}
