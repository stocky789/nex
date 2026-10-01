// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"

	"nex.local/nex/internal/protocol"
)

// A pool rollout is a durable job the pool master's Core runs (pool-update.json, replicated as the
// authority section update_rollout): it stages everywhere, then updates one host at a time. The
// Controller asks, through one pool-update command per request, and reads the master's record back
// from inventory. It schedules nothing, polls nothing and keeps no rollout state: delete this
// Controller and a fresh one reads the same rollout from the master.

type poolUpdateAPIRequest struct {
	RequestID  string `json:"request_id"`
	Sequence   uint64 `json:"sequence,omitempty"`
	RunningVMs string `json:"running_vms,omitempty"`
}

type poolUpdateControlAPIRequest struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	HostID    string `json:"host_id,omitempty"`
}

type poolUpdatePayload struct {
	PoolID    string `json:"pool_id"`
	Kind      string `json:"kind"`
	RolloutID string `json:"rollout_id,omitempty"`
	poolUpdateAPIRequest
	Action string `json:"action,omitempty"`
	HostID string `json:"host_id,omitempty"`
}

// poolUpdateView is GET /api/v1/pools/{id}/updates: the master's own rollout record, unchanged.
type poolUpdateView struct {
	PoolID     string                      `json:"pool_id"`
	Connection string                      `json:"connection"`
	Supported  bool                        `json:"supported"`
	Rollout    *protocol.PoolUpdateRollout `json:"rollout,omitempty"`
}

// poolUpdateSupported is true only when the master and every host its membership lists advertise
// pool-update-rollout-v1; a mixed pool answers 409 "upgrade Nex Core" instead.
func poolUpdateSupported(tx *sql.Tx, master string, membership *protocol.PoolMembership) (bool, string) {
	if !hostSupportsCapability(tx, master, protocol.PoolUpdateRolloutCapability) {
		return false, master
	}
	if membership == nil {
		return false, master
	}
	for _, member := range membership.Members {
		if member.Status != "ejected" && !hostSupportsCapability(tx, member.HostID, protocol.PoolUpdateRolloutCapability) {
			return false, member.HostID
		}
	}
	return true, ""
}

// poolUpdates implements GET /api/v1/pools/{id}/updates.
func (c *Controller) poolUpdates(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	if !nameValid(pool) {
		fail(w, http.StatusBadRequest, "pool is required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	master, status, err := poolMasterOf(tx, pool)
	if err != nil {
		fail(w, status, err.Error())
		return
	}
	inv, err := hostInventory(tx, master)
	if err != nil {
		fail(w, http.StatusInternalServerError, "inventory unavailable")
		return
	}
	row, err := c.loadHostRow(tx, master)
	if err != nil {
		fail(w, http.StatusNotFound, "host not found")
		return
	}
	view := poolUpdateView{PoolID: pool, Connection: row.connection}
	view.Supported, _ = poolUpdateSupported(tx, master, reportedMembership(inv))
	if inv.Managed != nil && slices.Contains(row.capabilities, protocol.ImageUpdateCapability) {
		view.Rollout = inv.Managed.UpdateRollout
	}
	write(w, http.StatusOK, view)
}

// relayPoolUpdate implements the three POST routes: check (a dry run), rollouts and a control.
func (c *Controller) relayPoolUpdate(kind string) func(http.ResponseWriter, *http.Request, string, string) {
	return func(w http.ResponseWriter, r *http.Request, user, csrf string) {
		pool, rollout := r.PathValue("id"), r.PathValue("rollout_id")
		var request protocol.PoolUpdateRequest
		var requestID string
		payload := poolUpdatePayload{PoolID: pool, Kind: kind, RolloutID: rollout}
		action := "update-rollout"
		if kind == "control" {
			var body poolUpdateControlAPIRequest
			if !decode(w, r, &body) {
				return
			}
			requestID, action = body.RequestID, "update-control"
			request = protocol.PoolUpdateRequest{RolloutID: rollout, Control: body.Action, HostID: body.HostID}
			payload.Action, payload.HostID = body.Action, body.HostID
		} else {
			var body poolUpdateAPIRequest
			if !decode(w, r, &body) {
				return
			}
			requestID, payload.poolUpdateAPIRequest = body.RequestID, body
			request = protocol.PoolUpdateRequest{Sequence: body.Sequence, RunningVMs: body.RunningVMs, DryRun: kind == "check"}
		}
		if !nameValid(pool) || !nameValid(requestID) || (rollout != "" && !nameValid(rollout)) {
			fail(w, http.StatusBadRequest, "pool and a stable request_id are required")
			return
		}
		if err := request.Valid(action); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		tx, err := c.db.Begin()
		if err != nil {
			fail(w, http.StatusInternalServerError, "storage unavailable")
			return
		}
		defer tx.Rollback()
		if c.replayResource(w, tx, requestID, "pool-update", payload) {
			return
		}
		master, status, err := c.freshPoolMaster(tx, pool, "update the pool")
		if err != nil {
			fail(w, status, err.Error())
			return
		}
		inv, err := hostInventory(tx, master)
		if err != nil {
			fail(w, http.StatusInternalServerError, "inventory unavailable")
			return
		}
		if ok, host := poolUpdateSupported(tx, master, reportedMembership(inv)); !ok {
			fail(w, http.StatusConflict, fmt.Sprintf("a pool rollout needs %s on the master and every member; upgrade Nex Core on %s", protocol.PoolUpdateRolloutCapability, host))
			return
		}
		opID := random()
		cmd := protocol.Command{OperationID: opID, Kind: "pool-update", Pool: &protocol.PoolCommand{
			RequestID: requestID, Action: action, Membership: protocol.PoolMembership{MasterHostID: master}, Update: &request}}
		op, err := c.saveResource(tx, user, requestID, pool, master, master, "pool-update:"+pool+":"+opID, cmd, payload)
		if err != nil || c.audit(tx, user, "pool.update."+kind, pool) != nil || tx.Commit() != nil {
			fail(w, http.StatusInternalServerError, "pool update request storage failed")
			return
		}
		write(w, http.StatusAccepted, resourceResponse(cmd, op))
	}
}
