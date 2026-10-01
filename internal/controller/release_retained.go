// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http"

	"nex.local/nex/internal/protocol"
)

type releaseRetainedRequest struct {
	RequestID    string `json:"request_id"`
	Confirmation string `json:"confirmation"`
}

// releaseRetainedOriginals queues deletion of the original disks and vTPM state that restore
// replace kept beside a VM. Its host checks the confirmation, that the VM is shut off, and that
// no live disk chain or snapshot uses the files.
func (c *Controller) releaseRetainedOriginals(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v releaseRetainedRequest
	if !decode(w, r, &v) {
		return
	}
	id := r.PathValue("id")
	host, pool, _, _, err := c.vmPlacement(id)
	if err != nil {
		fail(w, 404, "VM not found")
		return
	}
	req := protocol.VMReleaseRetainedRequest{VMID: id, PoolID: pool, HostID: host, Confirmation: v.Confirmation}
	if !nameValid(v.RequestID) || req.Valid() != nil {
		fail(w, 400, "stable request_id and the VM name as confirmation required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, v.RequestID, "vm-release-retained", v) {
		return
	}
	if err = c.managedHost(tx, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind IN ('vm-update','vm-power','vm-media','vm-delete','vm-release-retained','backup-restore-replace','backup-test-restore') AND r.name=? AND o.status IN ('queued','dispatched')", id).Scan(&pending); err != nil {
		fail(w, 500, "pending request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a change, power, media, delete, release or restore request for this VM is still pending; wait for its task")
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "vm-release-retained", ReleaseRetained: &req}
	op, err := c.saveResource(tx, user, v.RequestID, pool, host, id, opID, cmd, v)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "release request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}
