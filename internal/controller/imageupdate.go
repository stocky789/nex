// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http"
	"slices"

	"nex.local/nex/internal/protocol"
)

// Image-edition hosts update themselves: the host's engine fetches and verifies the release,
// writes the inactive slot, arms the boot entry and rolls back on a failed boot, all from its own
// durable record (/var/lib/nex/image/state.json). The Controller asks for one action at a time and
// shows what the host reports in Inventory.image; it stores no update state of its own and holds no
// operation open (a Controller that disappears mid-update changes nothing on the host).

// imageUpdateAPIRequest is the body of POST /api/v1/hosts/{id}/updates/{action}.
type imageUpdateAPIRequest struct {
	RequestID  string `json:"request_id"`
	Sequence   int    `json:"sequence,omitempty"`
	Activate   string `json:"activate,omitempty"`
	RunningVMs string `json:"running_vms,omitempty"`
	Channel    string `json:"channel,omitempty"`   // settings (required), import, fetch
	BaseURL    string `json:"base_url,omitempty"`  // settings
	Media      string `json:"media,omitempty"`     // import (required), fetch
	Component  string `json:"component,omitempty"` // fetch (required)
}

type imageUpdatePayload struct {
	HostID string `json:"host_id"`
	Action string `json:"action"`
	imageUpdateAPIRequest
}

// imageUpdateView is GET /api/v1/hosts/{id}/updates: the host's own report, unchanged.
type imageUpdateView struct {
	HostID     string                `json:"host_id"`
	Connection string                `json:"connection"`
	Supported  bool                  `json:"supported"`
	Image      *protocol.ImageStatus `json:"image,omitempty"`
}

// hostImageUpdates implements GET /api/v1/hosts/{id}/updates. A classic host, or a Core that has
// not yet been acknowledged for image-update-v1, reports no image: that is "unsupported", not an error.
func (c *Controller) hostImageUpdates(w http.ResponseWriter, r *http.Request, user, csrf string) {
	id := r.PathValue("id")
	if !nameValid(id) {
		fail(w, http.StatusBadRequest, "host is required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	row, err := c.loadHostRow(tx, id)
	if err != nil {
		fail(w, http.StatusNotFound, "host not found")
		return
	}
	write(w, http.StatusOK, imageUpdateView{
		HostID:     id,
		Connection: row.connection,
		Supported:  slices.Contains(row.capabilities, protocol.ImageUpdateCapability),
		Image:      row.inventory.Image,
	})
}

// relayImageUpdate implements POST /api/v1/hosts/{id}/updates/{action} for check, apply, activate,
// rollback, cancel and the host-scoped settings, media, import and fetch. It queues one command to the host and returns the task; the host's engine
// refuses with its own stable code, which arrives as the failed task's error.
func (c *Controller) relayImageUpdate(w http.ResponseWriter, r *http.Request, user, csrf string) {
	host, action := r.PathValue("id"), r.PathValue("action")
	var body imageUpdateAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(host) || !nameValid(body.RequestID) {
		fail(w, http.StatusBadRequest, "host and a stable request_id are required")
		return
	}
	opID := random()
	request := protocol.ImageUpdateRequest{HostID: host, Action: action, RequestID: body.RequestID, Sequence: body.Sequence, Activate: body.Activate, RunningVMs: body.RunningVMs,
		Channel: body.Channel, BaseURL: body.BaseURL, Media: body.Media, Component: body.Component}
	if err := request.Valid(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	payload := imageUpdatePayload{HostID: host, Action: action, imageUpdateAPIRequest: body}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, protocol.ImageUpdateCommandKind, payload) {
		return
	}
	row, err := c.loadHostRow(tx, host)
	if err != nil {
		fail(w, http.StatusNotFound, "host not found")
		return
	}
	// Core refuses again on its own record; these are only the refusals the Controller can see.
	if row.revoked {
		fail(w, http.StatusConflict, "this host is revoked")
		return
	}
	if !slices.Contains(row.capabilities, protocol.ImageUpdateCapability) {
		fail(w, http.StatusConflict, "this host does not run the image edition or does not support image updates; upgrade Nex Core")
		return
	}
	if row.connection != "fresh" {
		fail(w, http.StatusConflict, "the host must be freshly connected before an update action is sent")
		return
	}
	cmd := protocol.Command{OperationID: opID, Kind: protocol.ImageUpdateCommandKind, ImageUpdate: &request}
	op, err := c.saveResource(tx, user, body.RequestID, row.poolID, host, action, protocol.ImageUpdateCommandKind+":"+host+":"+opID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "image update storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}
