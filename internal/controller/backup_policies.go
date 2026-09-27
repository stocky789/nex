// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"net/http"
	"strings"

	"nex.local/nex/internal/protocol"
)

type upsertBackupPolicyRequest struct {
	RequestID    string                            `json:"request_id"`
	ID           string                            `json:"id,omitempty"`
	ScheduleID   string                            `json:"schedule_id,omitempty"`
	Name         string                            `json:"name"`
	Enabled      bool                              `json:"enabled"`
	Paused       bool                              `json:"paused"`
	RepositoryID string                            `json:"repository_id"`
	CaptureMode  string                            `json:"capture_mode"`
	TimeZone     string                            `json:"time_zone"`
	Rule         protocol.BackupScheduleRule       `json:"rule"`
	VMIDs        []string                          `json:"vm_ids"`
	Retention    protocol.BackupRetention          `json:"retention"`
	Limits       protocol.BackupPolicyLimits       `json:"limits,omitempty"`
	Overrides    []protocol.BackupPolicyVMOverride `json:"overrides,omitempty"`
}

type pauseBackupPolicyRequest struct {
	RequestID string `json:"request_id"`
	Paused    bool   `json:"paused"`
}

type runNowBackupPolicyRequest struct {
	RequestID string `json:"request_id"`
	VMID      string `json:"vm_id"`
}

func (c *Controller) listBackupPolicies(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.listPoolBackupInventory(w, r.PathValue("id"), "backup policies", func(m *protocol.ManagedState) (bool, any) {
		if !m.BackupPoliciesReported {
			return false, nil
		}
		out := append([]protocol.BackupPolicy{}, m.BackupPolicies...)
		return true, out
	})
}

func (c *Controller) createBackupPolicy(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.PathValue("id")
	var body upsertBackupPolicyRequest
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	id, scheduleID := body.ID, body.ScheduleID
	if id == "" {
		id = random()
	}
	if scheduleID == "" {
		scheduleID = id
	}
	capture := body.CaptureMode
	if capture == "" {
		capture = protocol.BackupCaptureStopped
	}
	policy := protocol.BackupPolicy{
		ID: id, ScheduleID: scheduleID, PoolID: pool, Name: body.Name,
		Enabled: body.Enabled, Paused: body.Paused, RepositoryID: body.RepositoryID,
		CaptureMode: capture, TimeZone: body.TimeZone, Rule: body.Rule, VMIDs: body.VMIDs,
		Retention: body.Retention, Limits: body.Limits, Overrides: body.Overrides, Revision: 1,
	}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || policy.Valid() != nil {
		fail(w, 400, "stable request_id and valid backup policy required")
		return
	}
	c.enqueueBackupPolicy(w, pool, user, body.RequestID, "backup-policy-upsert", &policy, nil, body, "")
}

func (c *Controller) updateBackupPolicy(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool, policyID := r.PathValue("id"), r.PathValue("policy_id")
	var body upsertBackupPolicyRequest
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	capture := body.CaptureMode
	if capture == "" {
		capture = protocol.BackupCaptureStopped
	}
	policy := protocol.BackupPolicy{
		ID: policyID, ScheduleID: body.ScheduleID, PoolID: pool, Name: body.Name,
		Enabled: body.Enabled, Paused: body.Paused, RepositoryID: body.RepositoryID,
		CaptureMode: capture, TimeZone: body.TimeZone, Rule: body.Rule, VMIDs: body.VMIDs,
		Retention: body.Retention, Limits: body.Limits, Overrides: body.Overrides, Revision: 1,
	}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || !protocol.ValidSnapshotOperationID(policyID) {
		fail(w, 400, "stable request_id and valid backup policy required")
		return
	}
	c.enqueueBackupPolicy(w, pool, user, body.RequestID, "backup-policy-upsert", &policy, nil, body, policyID)
}

func reportedBackupScheduleID(tx *sql.Tx, master, policyID string) (string, bool) {
	inv, err := hostInventory(tx, master)
	if err != nil || inv.Managed == nil || !inv.Managed.BackupPoliciesReported {
		return "", false
	}
	for _, policy := range inv.Managed.BackupPolicies {
		if policy.ID == policyID {
			return policy.ScheduleID, true
		}
	}
	return "", false
}

func (c *Controller) pauseBackupPolicy(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool, policyID := r.PathValue("id"), r.PathValue("policy_id")
	var body pauseBackupPolicyRequest
	if !decode(w, r, &body) {
		return
	}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || !protocol.ValidSnapshotOperationID(policyID) {
		fail(w, 400, "stable request_id and policy id required")
		return
	}
	policy := protocol.BackupPolicy{ID: policyID, PoolID: pool, Paused: body.Paused}
	c.enqueueBackupPolicy(w, pool, user, body.RequestID, "backup-policy-pause", &policy, nil, body, policyID)
}

func (c *Controller) runNowBackupPolicy(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool, policyID := r.PathValue("id"), r.PathValue("policy_id")
	var body runNowBackupPolicyRequest
	if !decode(w, r, &body) {
		return
	}
	if !protocol.ValidSnapshotOperationID(body.RequestID) || !protocol.ValidSnapshotOperationID(policyID) ||
		body.VMID == "" || len(body.VMID) > 128 || body.RequestID == policyID {
		fail(w, 400, "stable request_id, policy id and vm_id required")
		return
	}
	policy := protocol.BackupPolicy{ID: policyID, PoolID: pool}
	vm := protocol.VMCreateSpec{ID: body.VMID}
	c.enqueueBackupPolicy(w, pool, user, body.RequestID, "backup-policy-run-now", &policy, &vm, body, policyID)
}

func (c *Controller) enqueueBackupPolicy(w http.ResponseWriter, pool, user, requestID, kind string, policy *protocol.BackupPolicy, vm *protocol.VMCreateSpec, payload any, targetID string) {
	if !nameValid(pool) {
		fail(w, 404, "pool not found")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	scope := []string{pool}
	if targetID != "" {
		scope = append(scope, targetID)
	}
	if c.replayResource(w, tx, requestID, kind, payload, scope...) {
		return
	}
	var master string
	if tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master) != nil || master == "" {
		fail(w, 503, "pool master is unavailable")
		return
	}
	if err = c.backupHost(tx, pool, master); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if kind == "backup-policy-upsert" {
		if policy.ScheduleID == "" {
			var ok bool
			policy.ScheduleID, ok = reportedBackupScheduleID(tx, master, policy.ID)
			if !ok {
				fail(w, 503, "backup policy is unavailable from the pool master")
				return
			}
		}
		if policy.Valid() != nil {
			fail(w, 400, "stable request_id and valid backup policy required")
			return
		}
	}
	var held int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources WHERE id=? OR operation_id=?", requestID, requestID).Scan(&held); err != nil {
		fail(w, 500, "backup identity lookup failed")
		return
	}
	if held > 0 {
		fail(w, 409, "request_id already used for another operation")
		return
	}
	rowID := "backup-policy:" + requestID
	name := policy.Name
	if name == "" {
		name = policy.ID
	}
	cmd := protocol.Command{OperationID: requestID, Kind: kind, BackupPolicy: policy, VM: vm}
	op, err := c.saveResource(tx, user, requestID, pool, master, name, rowID, cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "backup policy request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}
