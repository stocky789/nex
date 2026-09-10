// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"nex.local/nex/internal/protocol"
	"slices"
)

type mediaChangeRequest struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	MediaID   string `json:"media_id"`
}

// mediaVM queues a durable insert or eject of ISO media for the VM's assigned host.
func (c *Controller) mediaVM(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v mediaChangeRequest
	if !decode(w, r, &v) {
		return
	}
	if !nameValid(v.RequestID) || !protocol.ValidMediaAction(v.Action) {
		fail(w, 400, "stable request_id and action insert or eject required")
		return
	}
	if v.Action == "insert" && !identifierValid(v.MediaID) {
		fail(w, 400, "insert requires media_id")
		return
	}
	if v.Action == "eject" && v.MediaID != "" {
		fail(w, 400, "eject must not name media_id")
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
	payload := map[string]string{"vm_id": id, "request_id": v.RequestID, "action": v.Action, "media_id": v.MediaID}
	if c.replayResource(w, tx, v.RequestID, "vm-media", payload) {
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
	if !slices.Contains(caps, protocol.MediaCapability) {
		fail(w, 409, "the VM's host agent does not support central media changes; upgrade Nex Core")
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='vm-media' AND r.name=? AND o.status IN ('queued','dispatched')", id).Scan(&pending); err != nil {
		fail(w, 500, "media request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a media request for this VM is still pending; wait for its task")
		return
	}
	if v.Action == "insert" {
		media, e := readyResource(tx, v.MediaID, "iso-import", pool, host)
		if e != nil {
			fail(w, 404, "the install ISO is not registered and verified on the selected host and pool")
			return
		}
		var cmdRaw string
		if tx.QueryRow("SELECT command FROM managed_resources WHERE id=? AND kind='vm-create'", id).Scan(&cmdRaw) != nil {
			fail(w, 404, "VM not found")
			return
		}
		var create protocol.Command
		if json.Unmarshal([]byte(cmdRaw), &create) != nil || create.VM == nil {
			fail(w, 500, "VM definition unavailable")
			return
		}
		on := false
		for _, d := range create.VM.Disks {
			on = on || (media.Media != nil && media.Media.StorageID == d.StorageID)
		}
		if !on {
			fail(w, 409, "the install ISO must be on one of the VM's disk storage targets")
			return
		}
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: id, PoolID: pool, HostID: host, Action: v.Action, MediaID: v.MediaID}}
	op, err := c.saveResource(tx, user, v.RequestID, pool, host, id, opID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "media request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}

// mirrorVMMedia updates the durable vm-create mirror after a successful host media change.
func (c *Controller) mirrorVMMedia(tx *sql.Tx, opID string) error {
	var raw string
	if err := tx.QueryRow("SELECT command FROM managed_resources WHERE operation_id=?", opID).Scan(&raw); err != nil {
		return err
	}
	var cmd protocol.Command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil || cmd.VMMedia == nil {
		return err
	}
	req := cmd.VMMedia
	var createRaw string
	if err := tx.QueryRow("SELECT command FROM managed_resources WHERE id=? AND kind='vm-create'", req.VMID).Scan(&createRaw); err != nil {
		return err
	}
	var create protocol.Command
	if err := json.Unmarshal([]byte(createRaw), &create); err != nil || create.VM == nil {
		return err
	}
	v := protocol.ApplyVMMedia(*create.VM, req.Action, req.MediaID)
	create.VM = &v
	updated, err := json.Marshal(create)
	if err != nil {
		return err
	}
	// The stored request payload backs idempotent replay of the original create request_id
	// (replayResource compares it byte for byte) and must stay exactly what that request sent;
	// only the command mirror, which reflects the VM's current state, is updated here.
	_, err = tx.Exec("UPDATE managed_resources SET command=? WHERE id=? AND kind='vm-create'", string(updated), req.VMID)
	return err
}
