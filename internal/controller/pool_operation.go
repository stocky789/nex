// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
)

// poolPromotionConflicts are Core refusals that arrive as a failed pool-promote operation
// because membership or the confirmed fence-host set changed since the operator acted.
var poolPromotionConflicts = []string{
	"pool membership changed since this request was composed; review the pool and try again",
	"pool membership is unavailable",
	"this promotion no longer needs to power off any host; review the pool and try again",
}

const poolPromotionFenceMismatch = "; confirm the hosts this pool reports and try again"

func isPoolPromotionConflict(status, err string) bool {
	if status != "failed" {
		return false
	}
	if slices.Contains(poolPromotionConflicts, err) {
		return true
	}
	return strings.HasPrefix(err, "this promotion must power off ") && strings.HasSuffix(err, poolPromotionFenceMismatch)
}

func lastPoolOperation(tx *sql.Tx, pool, kind, defaultAction string) (*poolTransferOperation, error) {
	rows, err := tx.Query("SELECT r.payload,o.id,o.status,o.error FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind=? AND r.pool_id=? ORDER BY o.rowid", kind, pool)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out *poolTransferOperation
	for rows.Next() {
		var raw string
		var v poolTransferOperation
		if err = rows.Scan(&raw, &v.OperationID, &v.Status, &v.Error); err != nil {
			return nil, err
		}
		v.Action = defaultAction
		if kind == "pool-transfer" {
			var payload poolTransferPayload
			if json.Unmarshal([]byte(raw), &payload) == nil && payload.Action != "" {
				v.Action = payload.Action
			}
		}
		switch kind {
		case "pool-transfer":
			if v.Status == "failed" {
				v.Conflict = slices.Contains(poolTransferConflicts, v.Error) || strings.HasSuffix(v.Error, poolEjectForwardOnly)
				v.Retryable = v.Error == poolAuthorityFrozen
			}
			if v.Status == "succeeded" {
				v.Reason, v.Error = v.Error, ""
			}
		case "pool-promote":
			v.Conflict = isPoolPromotionConflict(v.Status, v.Error)
		}
		found := v
		out = &found
	}
	return out, rows.Err()
}

func lastPoolTransferOperation(tx *sql.Tx, pool string) (*poolTransferOperation, error) {
	return lastPoolOperation(tx, pool, "pool-transfer", "")
}

func lastPoolPromotionOperation(tx *sql.Tx, pool string) (*poolTransferOperation, error) {
	return lastPoolOperation(tx, pool, "pool-promote", "promote")
}
