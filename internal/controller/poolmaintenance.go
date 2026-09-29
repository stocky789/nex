// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"nex.local/nex/internal/protocol"
)

// Maintenance mode is a durable flag inside the membership record the pool master already
// owns. The Controller relays one command to the master and projects what the master
// reports; it never records a drain of its own, and the refusal that makes the flag mean
// anything is the master's.

// poolMaintenanceConflicts are the master's membership-conflict refusals. They arrive as a
// failed operation, not as an HTTP status, so the projection marks them for the UI.
var poolMaintenanceConflicts = []string{
	"pool membership changed since this request was composed; review the pool and try again",
	"pool membership is unavailable",
}

type poolMaintenanceAPIRequest struct {
	RequestID       string `json:"request_id"`
	Enabled         bool   `json:"enabled"`
	ExpectedVersion uint64 `json:"expected_version,omitempty"`
}

// poolMaintenancePayload is the replay record: the client request plus the pool and host it
// addressed, so the same request_id aimed elsewhere is a changed payload.
type poolMaintenancePayload struct {
	PoolID string `json:"pool_id"`
	HostID string `json:"host_id"`
	poolMaintenanceAPIRequest
}

// setPoolMaintenance implements POST /api/v1/pools/{id}/members/{host_id}/maintenance.
func (c *Controller) setPoolMaintenance(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool, host := r.PathValue("id"), r.PathValue("host_id")
	var body poolMaintenanceAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(pool) || !nameValid(host) || !nameValid(body.RequestID) {
		fail(w, http.StatusBadRequest, "pool, host and a stable request_id are required")
		return
	}
	payload := poolMaintenancePayload{PoolID: pool, HostID: host, poolMaintenanceAPIRequest: body}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, body.RequestID, "pool-maintenance", payload) {
		return
	}
	master, status, err := c.poolMaintenanceTarget(tx, pool, host, body.ExpectedVersion)
	if err != nil {
		fail(w, status, err.Error())
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "pool-maintenance", Pool: &protocol.PoolCommand{
		RequestID:       body.RequestID,
		Action:          "maintenance",
		Membership:      protocol.PoolMembership{MasterHostID: master},
		MemberHostID:    host,
		Enabled:         body.Enabled,
		ExpectedVersion: body.ExpectedVersion,
	}}
	op, err := c.saveResource(tx, user, body.RequestID, pool, master, host, "pool-maintenance:"+host+":"+opID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, http.StatusInternalServerError, "maintenance storage failed")
		return
	}
	write(w, http.StatusAccepted, resourceResponse(cmd, op))
}

// poolMaintenanceTarget returns the master the command must be addressed to. Every refusal
// here is what the Controller can already see from host reports; Core refuses again.
func (c *Controller) poolMaintenanceTarget(tx *sql.Tx, pool, host string, expected uint64) (string, int, error) {
	if status, err := hostKnown(tx, host); err != nil {
		return "", status, err
	}
	master, status, err := c.freshPoolMaster(tx, pool, "record maintenance")
	if err != nil {
		return "", status, err
	}
	inv, err := hostInventory(tx, master)
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Errorf("inventory unavailable")
	}
	membership := reportedMembership(inv)
	if membership == nil {
		return "", http.StatusConflict, fmt.Errorf("the pool master has not reported its membership; maintenance cannot be recorded yet")
	}
	if maintenanceMember(membership, host) == nil {
		return "", http.StatusConflict, fmt.Errorf("this host is not a member of the selected pool")
	}
	if expected != 0 && expected != membership.Generation {
		return "", http.StatusConflict, fmt.Errorf("the pool master reports membership generation %d; reload the pool and retry", membership.Generation)
	}
	return master, 0, nil
}

func maintenanceMember(membership *protocol.PoolMembership, host string) *protocol.PoolMember {
	if membership == nil {
		return nil
	}
	for i, member := range membership.Members {
		if member.HostID == host && member.Status != "ejected" {
			return &membership.Members[i]
		}
	}
	return nil
}

// poolMemberGuests is the drained host's own reported guest list, for the confirmation
// dialog. A host that is not reporting has no guest list: that is unknown, not none.
type poolMemberGuests struct {
	Known   bool     `json:"known"`
	Total   int      `json:"total"`
	Running int      `json:"running"`
	Names   []string `json:"names"`
}

// memberGuests projects one host's mirrored inventory.vms.
func memberGuests(host hostRow) poolMemberGuests {
	out := poolMemberGuests{Names: []string{}}
	if host.id == "" || host.connection != "fresh" {
		return out
	}
	out.Known = true
	for _, vm := range host.inventory.VMs {
		out.Total++
		if vm.State == "running" {
			out.Running++
		}
		if len(out.Names) < 50 {
			out.Names = append(out.Names, vm.Name)
		}
	}
	return out
}

// memberOperation is the latest command of one kind this Controller holds for a host, so an
// asynchronous Core refusal is visible where the operator acted. Conflict marks the refusals
// the synchronous routes would have answered with 409.
type memberOperation struct {
	OperationID string `json:"operation_id"`
	Action      string `json:"action,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	Conflict    bool   `json:"conflict"`
}

// lastMaintenanceOperations returns the newest pool-maintenance operation per drained host
// in one pool. managed_resources.name is the member the command named.
func lastMaintenanceOperations(tx *sql.Tx, pool string) (map[string]memberOperation, error) {
	rows, err := tx.Query("SELECT r.name,o.id,o.status,o.error FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='pool-maintenance' AND r.pool_id=? ORDER BY o.rowid", pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]memberOperation{}
	for rows.Next() {
		var name string
		var v memberOperation
		if err = rows.Scan(&name, &v.OperationID, &v.Status, &v.Error); err != nil {
			return nil, err
		}
		v.Conflict = isPoolMaintenanceConflict(v.Status, v.Error)
		out[name] = v
	}
	return out, rows.Err()
}

func isPoolMaintenanceConflict(status, err string) bool {
	if status != "failed" {
		return false
	}
	for _, text := range poolMaintenanceConflicts {
		if err == text {
			return true
		}
	}
	return false
}

// maintenanceSince renders the master's record; the zero time means "not drained".
func maintenanceSince(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// maintenanceRefusal reports the mirrored membership's drain flag for a placement target, so
// the operator is not offered a host the master will refuse. Only a fresh master's record is
// used: a stale mirror is unknown, and Core decides either way.
func (c *Controller) maintenanceRefusal(tx *sql.Tx, pool, master, host string) error {
	if c.managedHost(tx, pool, master) != nil {
		return nil
	}
	inv, err := hostInventory(tx, master)
	if err != nil {
		return nil
	}
	if member := maintenanceMember(reportedMembership(inv), host); member != nil && member.Maintenance {
		return fmt.Errorf("the selected host is in maintenance mode; take it out of maintenance before placing or starting VMs on it")
	}
	return nil
}
