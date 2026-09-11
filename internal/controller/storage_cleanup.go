// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http"

	"nex.local/nex/internal/protocol"
)

type storageCredentialRetireAPIRequest struct {
	RequestID string `json:"request_id"`
	PoolID    string `json:"pool_id"`
	HostID    string `json:"host_id"`
}

// storageCredentialRetireIntent includes the path target so request_id replay cannot retarget
// a previously accepted cleanup operation to another Core-owned receipt.
type storageCredentialRetireIntent struct {
	RequestID string `json:"request_id"`
	PoolID    string `json:"pool_id"`
	HostID    string `json:"host_id"`
	Receipt   string `json:"receipt"`
}

// retireStorageCredential only queues Core's cleanup decision. Core owns receipt references
// and rejects a receipt still attached to a repository.
func (c *Controller) retireStorageCredential(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body storageCredentialRetireAPIRequest
	if !decode(w, r, &body) {
		return
	}
	v := protocol.StorageCredentialRetireRequest{Receipt: r.PathValue("receipt"), PoolID: body.PoolID, HostID: body.HostID}
	intent := storageCredentialRetireIntent{RequestID: body.RequestID, PoolID: body.PoolID, HostID: body.HostID, Receipt: v.Receipt}
	if !nameValid(body.RequestID) {
		fail(w, http.StatusBadRequest, "stable request_id required")
		return
	}
	if err := protocol.ValidateStorageCredentialRetire(v); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "storage-credential-retire", intent) {
		return
	}
	if err = c.storageRepositoryHost(tx, v.PoolID, v.HostID); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "storage-credential-retire", CredentialRetire: &v}
	op, err := c.saveResource(tx, user, body.RequestID, v.PoolID, v.HostID, "credential cleanup", "credential-retire-"+opID, cmd, intent)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "credential cleanup storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}
