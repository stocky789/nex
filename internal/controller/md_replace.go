// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"fmt"
	"net/http"
	"slices"

	"nex.local/nex/internal/protocol"
)

// Replacing a failed system disk is entirely the host's work: it revalidates the disk, copies
// the partition table, adds the member and installs the bootloader, and the kernel rebuilds
// the mirror. The Controller submits one durable command and keeps nothing but the task it
// keeps for every host operation; the mirror's state is read back from the hardware report.

type systemDiskAPIRequest struct {
	RequestID string                      `json:"request_id"`
	Array     string                      `json:"array"`
	Device    protocol.ZFSProvisionDevice `json:"device"`
}

type systemDiskPayload struct {
	HostID string `json:"host_id"`
	systemDiskAPIRequest
}

// replaceSystemDisk implements POST /api/v1/hosts/{id}/system-disk.
func (c *Controller) replaceSystemDisk(w http.ResponseWriter, r *http.Request, user, csrf string) {
	host := r.PathValue("id")
	var body systemDiskAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(host) || !nameValid(body.RequestID) {
		fail(w, http.StatusBadRequest, "host and a stable request_id are required")
		return
	}
	opID := random()
	request := protocol.SystemDiskReplaceRequest{OperationID: opID, HostID: host, Array: body.Array, Device: body.Device}
	if err := request.Valid(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "host-system-disk-replace", systemDiskPayload{HostID: host, systemDiskAPIRequest: body}) {
		return
	}
	row, err := c.loadHostRow(tx, host)
	if err != nil {
		fail(w, http.StatusNotFound, "host not found")
		return
	}
	// Core checks the array, the disk and the confirmation again against its own observation;
	// these are only the refusals the Controller can already see from this host's reports.
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
	cmd := protocol.Command{OperationID: opID, Kind: "host-system-disk-replace", SystemDiskReplace: &request}
	op, err := c.saveResource(tx, user, body.RequestID, row.poolID, host, body.Array, "host-system-disk-replace:"+host+":"+opID, cmd, systemDiskPayload{HostID: host, systemDiskAPIRequest: body})
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "system disk replacement storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}
