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
	// Drive is "" or "install" for the install drive, or "drivers" for the driver disc drive.
	Drive string `json:"drive"`
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
	if !protocol.ValidMediaDrive(v.Drive) {
		fail(w, 400, "drive must be install or drivers")
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
	// Only a named drive joins the payload, so replays stored before drives existed still match.
	if v.Drive != "" {
		payload["drive"] = v.Drive
	}
	if c.replayResource(w, tx, v.RequestID, "vm-media", payload) {
		return
	}
	if err = c.placementHost(tx, pool, host); err != nil {
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
	if v.Drive == protocol.DriversDrive && !slices.Contains(caps, protocol.DriversDriveCapability) {
		fail(w, 409, "the VM's host agent does not support a driver disc drive; upgrade Nex Core")
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind IN ('vm-media','vm-update') AND r.name=? AND o.status IN ('queued','dispatched')", id).Scan(&pending); err != nil {
		fail(w, 500, "media request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a media request for this VM or a change to it is still pending; wait for its task")
		return
	}
	if v.Action == "insert" {
		what := "the install ISO"
		if v.Drive == protocol.DriversDrive {
			what = "the driver disc ISO"
		}
		if _, e := c.readyMediaForHost(tx, v.MediaID, pool, host); e != nil {
			fail(w, 404, what+" is not registered and verified on the VM's host and pool")
			return
		}
		// The other drive must not hold the same ISO: the resulting spec would fail ValidateVMSpec.
		var createRaw string
		var create protocol.Command
		if tx.QueryRow("SELECT command FROM managed_resources WHERE id=? AND kind='vm-create'", id).Scan(&createRaw) == nil && json.Unmarshal([]byte(createRaw), &create) == nil && create.VM != nil {
			other := create.VM.DriversMediaID
			if v.Drive == protocol.DriversDrive {
				other = create.VM.MediaID
			}
			if other == v.MediaID {
				fail(w, 400, "the driver disc must differ from the install media")
				return
			}
		}
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: id, PoolID: pool, HostID: host, Action: v.Action, Drive: v.Drive, MediaID: v.MediaID}}
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
	v := protocol.ApplyVMMedia(*create.VM, req.Action, req.Drive, req.MediaID)
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
