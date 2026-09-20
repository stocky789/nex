// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"nex.local/nex/internal/protocol"
)

// A staged management-network edit is entirely the host's: it renders, applies, times and
// rolls back its own configuration. The Controller submits one durable command per step and
// shows what the host reports. Nothing here is secret, so the command is stored like any
// other; the only thing the Controller must get right is that it never claims to know the
// state of a host that is not reporting.

type hostNetworkAPIRequest struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	// ChangeID is optional: the staged change the operator was looking at. The Controller
	// always takes the id it sends from the host's own latest report, never from memory; a
	// body that names a different one is refused rather than retargeted.
	ChangeID         string                         `json:"change_id,omitempty"`
	Spec             protocol.ManagementNetworkSpec `json:"spec,omitempty"`
	ExpectedRevision uint64                         `json:"expected_revision,omitempty"`
	WindowSeconds    int                            `json:"window_seconds,omitempty"`
	Confirmation     string                         `json:"confirmation,omitempty"`
}

type hostNetworkPayload struct {
	HostID string `json:"host_id"`
	hostNetworkAPIRequest
}

// reportedHostname is the name the operator must type to apply a change: the host's own
// reported hostname, falling back to the enrolled one before a first report.
func (v hostRow) reportedHostname() string {
	if v.inventory.Hostname != "" {
		return v.inventory.Hostname
	}
	return v.hostname
}

// changeHostNetwork implements POST /api/v1/hosts/{id}/network.
func (c *Controller) changeHostNetwork(w http.ResponseWriter, r *http.Request, user, csrf string) {
	host := r.PathValue("id")
	var body hostNetworkAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(host) || !nameValid(body.RequestID) {
		fail(w, http.StatusBadRequest, "host and a stable request_id are required")
		return
	}
	opID := random()
	request := protocol.ManagementNetworkRequest{
		OperationID:      opID,
		HostID:           host,
		Action:           body.Action,
		Spec:             body.Spec,
		ExpectedRevision: body.ExpectedRevision,
		WindowSeconds:    body.WindowSeconds,
		Confirmation:     body.Confirmation,
	}
	// Everything but the change this action targets can be judged before a host is looked up.
	// The id itself comes from the host's own report below, so the early check stands a
	// placeholder in for it; for "stage" that is the command's own id, which is what it uses.
	check := request
	check.ChangeID = opID
	if err := check.Valid(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	payload := hostNetworkPayload{HostID: host, hostNetworkAPIRequest: body}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "management-network", payload) {
		return
	}
	row, err := c.loadHostRow(tx, host)
	if err != nil {
		fail(w, http.StatusNotFound, "host not found")
		return
	}
	if status, err := hostNetworkRefusal(row, body); err != nil {
		fail(w, status, err.Error())
		return
	}
	if request.ChangeID, err = stagedChangeID(row, body, opID); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	cmd := protocol.Command{OperationID: opID, Kind: "management-network", ManagementNetwork: &request}
	op, err := c.saveResource(tx, user, body.RequestID, row.poolID, host, body.Action, "management-network:"+host+":"+opID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "management network storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}

// stagedChangeID is the change an apply, confirm or cancel acts on: the one the host itself
// last reported. The Controller keeps no copy of it — a replacement Controller reads it back
// from the host's inventory — and the command's own operation ID stays unique, because that is
// the journal key a redelivered command replays against. A body may name the change the
// operator was looking at; if the host has moved on, that is a conflict, not a retarget.
func stagedChangeID(row hostRow, body hostNetworkAPIRequest, opID string) (string, error) {
	if body.Action == "stage" {
		return opID, nil // staging creates the change, and names it after its own command
	}
	record := row.inventory.ManagementNetwork
	if record == nil || record.OperationID == "" {
		return "", fmt.Errorf("this host reports no management network change to %s; reload the host network page", body.Action)
	}
	if body.ChangeID != "" && body.ChangeID != record.OperationID {
		return "", fmt.Errorf("this host reports management network change %s, not the one you asked to %s; it changed since you loaded it — reload the host network page and retry", record.OperationID, body.Action)
	}
	return record.OperationID, nil
}

// hostNetworkRefusal holds what the Controller can see from this host's own reports. Core
// checks all of it again, including the confirmation, which it alone can bind to itself.
func hostNetworkRefusal(row hostRow, body hostNetworkAPIRequest) (int, error) {
	if row.revoked {
		return http.StatusConflict, fmt.Errorf("this host is revoked")
	}
	if !slices.Contains(row.capabilities, protocol.ManagementNetworkCapability) {
		return http.StatusConflict, fmt.Errorf("staged management network edits need %s on this host; upgrade Nex Core", protocol.ManagementNetworkCapability)
	}
	fresh := row.connection == "fresh"
	switch body.Action {
	case "stage", "apply":
		if !fresh {
			return http.StatusConflict, fmt.Errorf("the host must be freshly connected before a management network change is staged or applied")
		}
	case "confirm":
		// A host that has reached this Controller again is a host whose new configuration
		// carries traffic, which is the evidence Core requires. So confirm is possible the
		// moment it reports, from its old address or its new one, and not before.
		if !fresh {
			return http.StatusConflict, fmt.Errorf("the host has not reported since the change was applied; it cannot be confirmed until it reaches this Controller again")
		}
	case "cancel":
		// Cancel is the one step a quiet host must still be able to receive: it is durable
		// and runs on the host's next report, which is how a staged change is undone.
	}
	if body.Action == "apply" {
		name := row.reportedHostname()
		if !strings.EqualFold(strings.TrimSpace(body.Confirmation), strings.TrimSpace(name)) {
			return http.StatusBadRequest, fmt.Errorf("the confirmation must be the host name %q", name)
		}
	}
	if body.ExpectedRevision != 0 {
		var revision uint64
		if row.inventory.ManagementNetwork != nil {
			revision = row.inventory.ManagementNetwork.Revision
		}
		if body.ExpectedRevision != revision {
			return http.StatusConflict, fmt.Errorf("this host reports management network revision %d; reload the host network page and retry", revision)
		}
	}
	return 0, nil
}

type hostNetworkCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}
type hostNetworkView struct {
	HostID        string                            `json:"host_id"`
	Hostname      string                            `json:"hostname"`
	Connection    string                            `json:"connection"`
	Supported     bool                              `json:"supported"`
	State         string                            `json:"state"`
	Record        *protocol.ManagementNetworkStatus `json:"management_network"`
	NICs          []protocol.NIC                    `json:"nics"`
	Check         *hostNetworkCheck                 `json:"check"`
	LastOperation *memberOperation                  `json:"last_operation"`
}

// hostNetwork implements GET /api/v1/hosts/{id}/network. Every field is a host report; when
// the host is not fresh the state is "unknown" and the record is kept as last observed, so a
// running rollback deadline stays visible instead of reading as success or failure.
func (c *Controller) hostNetwork(w http.ResponseWriter, r *http.Request, user, csrf string) {
	host := r.PathValue("id")
	if !nameValid(host) {
		fail(w, http.StatusBadRequest, "host required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	row, err := c.loadHostRow(tx, host)
	if err != nil {
		fail(w, http.StatusNotFound, "host not found")
		return
	}
	out := hostNetworkView{
		HostID:     row.id,
		Hostname:   row.reportedHostname(),
		Connection: row.connection,
		Supported:  slices.Contains(row.capabilities, protocol.ManagementNetworkCapability),
		State:      "unknown",
		Record:     row.inventory.ManagementNetwork,
		NICs:       []protocol.NIC{},
	}
	if row.inventory.NICs != nil {
		out.NICs = row.inventory.NICs
	}
	if row.connection == "fresh" {
		out.State = "none"
		if out.Record != nil {
			out.State = out.Record.Stage
		}
		for _, check := range row.inventory.Checks {
			if check.Name == "management-network" {
				out.Check = &hostNetworkCheck{Name: check.Name, Status: check.Status, Detail: check.Error}
			}
		}
	}
	if last, err := lastHostNetworkOperation(tx, host); err != nil {
		fail(w, http.StatusInternalServerError, "operation lookup failed")
		return
	} else if last != nil {
		out.LastOperation = last
	}
	write(w, http.StatusOK, out)
}

func lastHostNetworkOperation(tx *sql.Tx, host string) (*memberOperation, error) {
	rows, err := tx.Query("SELECT r.name,o.id,o.status,o.error FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='management-network' AND r.host_id=? ORDER BY o.rowid", host)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out *memberOperation
	for rows.Next() {
		var v memberOperation
		if err = rows.Scan(&v.Action, &v.OperationID, &v.Status, &v.Error); err != nil {
			return nil, err
		}
		v.Conflict = isManagementNetworkConflict(v.Status, v.Error)
		found := v
		out = &found
	}
	return out, rows.Err()
}

// isManagementNetworkConflict is the single mapping rule Core defined: every conflict
// refusal ends with protocol.ManagementNetworkConflict, and nothing else is a conflict.
func isManagementNetworkConflict(status, err string) bool {
	return status == "failed" && strings.HasSuffix(err, protocol.ManagementNetworkConflict)
}
