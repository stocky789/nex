// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"fmt"
	"net/http"
	"slices"

	"nex.local/nex/internal/protocol"
)

// Replacing a failed disk of a provisioned ZFS repository is the pool's own work: the
// master owns the repository record and forwards the command to the host that holds the
// pool, which revalidates both disks and runs zpool replace. The Controller submits one
// durable command and keeps nothing but the task; the resilver is read back from the
// repository's ordinary pool observation.

type zfsReplaceAPIRequest struct {
	RequestID    string                      `json:"request_id"`
	RepositoryID string                      `json:"repository_id"`
	PoolID       string                      `json:"pool_id"`
	HostID       string                      `json:"host_id"`
	OldDevice    string                      `json:"old_device"`
	Device       protocol.ZFSProvisionDevice `json:"device"`
}

// replaceZFSDisk implements POST /api/v1/storage/zfs/replace.
func (c *Controller) replaceZFSDisk(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body zfsReplaceAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(body.RequestID) {
		fail(w, http.StatusBadRequest, "a stable request_id is required")
		return
	}
	opID := random()
	request := protocol.ZFSReplaceRequest{OperationID: opID, RepositoryID: body.RepositoryID, PoolID: body.PoolID, HostID: body.HostID, OldDevice: body.OldDevice, Device: body.Device}
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
	if c.replayResource(w, tx, body.RequestID, "storage-zfs-replace", body) {
		return
	}
	master, err := c.zfsProvisionMaster(tx, body.PoolID)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	// Core checks the pool, the failed disk and the replacement again against its own
	// observation; these are only the refusals the Controller can already see.
	for _, host := range []string{master, body.HostID} {
		row, err := c.loadHostRow(tx, host)
		if err != nil {
			fail(w, http.StatusNotFound, "host not found")
			return
		}
		if !slices.Contains(row.capabilities, protocol.ZFSReplaceCapability) {
			fail(w, http.StatusConflict, fmt.Sprintf("replacing a ZFS disk needs %s on the pool master and the selected host; upgrade Nex Core", protocol.ZFSReplaceCapability))
			return
		}
	}
	if err := c.managedHost(tx, body.PoolID, body.HostID); err != nil {
		fail(w, http.StatusConflict, "the selected host must be fresh, active and in the selected pool")
		return
	}
	cmd := protocol.Command{OperationID: opID, Kind: "storage-zfs-replace", ZFSReplace: &request}
	// This row is only the Controller's idempotent request/operation relay. The pool
	// master's journal and the pool itself remain the sole record of its disks.
	op, err := c.saveResource(tx, user, body.RequestID, body.PoolID, master, body.RepositoryID, "zfs-replace-"+opID, cmd, body)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "ZFS disk replacement request storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}
