// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"fmt"
	"net/http"
	"slices"

	"nex.local/nex/internal/protocol"
)

// Replacing a failed system disk is entirely the host's work: it lays the partition table, copies the
// verified slots, installs the boot loader and adds the data mirror member, and its own durable record
// keeps the operation. The Controller submits one durable command and keeps nothing but the task it
// keeps for every host operation; the outcome is read back from the hardware report (system_disk).

// systemDiskAPIRequest is the body of POST /api/v1/hosts/{id}/system-disk.
type systemDiskAPIRequest struct {
	RequestID string                      `json:"request_id"`
	Device    protocol.ZFSProvisionDevice `json:"device"`
}

type systemDiskPayload struct {
	HostID string `json:"host_id"`
	systemDiskAPIRequest
}

// replaceSystemDisk implements POST /api/v1/hosts/{id}/system-disk. Core revalidates the disk, the
// confirmation and its own array against a fresh observation; these are only the refusals the
// Controller can already see.
func (c *Controller) replaceSystemDisk(w http.ResponseWriter, r *http.Request, user, csrf string) {
	host := r.PathValue("id")
	var body systemDiskAPIRequest
	if !decode(w, r, &body) {
		return
	}
	request := protocol.SystemDiskReplaceRequest{HostID: host, RequestID: body.RequestID, Device: body.Device}
	if err := request.Valid(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	payload := systemDiskPayload{HostID: host, systemDiskAPIRequest: body}
	opID := random()
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, protocol.SystemDiskReplaceCommandKind, payload) {
		return
	}
	row, err := c.loadHostRow(tx, host)
	if err != nil {
		fail(w, http.StatusNotFound, "host not found")
		return
	}
	if row.revoked {
		fail(w, http.StatusConflict, "this host is revoked")
		return
	}
	if !slices.Contains(row.capabilities, protocol.SystemDiskReplaceCapability) {
		fail(w, http.StatusConflict, fmt.Sprintf("replacing a system disk needs %s on this host; upgrade Nex Core", protocol.SystemDiskReplaceCapability))
		return
	}
	if row.connection != "fresh" {
		fail(w, http.StatusConflict, "the host must be freshly connected before its system disk is replaced")
		return
	}
	cmd := protocol.Command{OperationID: opID, Kind: protocol.SystemDiskReplaceCommandKind, SystemDiskReplace: &request}
	op, err := c.saveResource(tx, user, body.RequestID, row.poolID, host, host, protocol.SystemDiskReplaceCommandKind+":"+host+":"+opID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "system disk replacement storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}
