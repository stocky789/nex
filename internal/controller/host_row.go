// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"

	"nex.local/nex/internal/protocol"
)

// hostRow is one enrolled host's last report. Every field is a mirror of a host report;
// nothing here is Controller-owned state.
type hostRow struct {
	id, poolID, hostname string
	connection, seen     string
	inventory            protocol.Inventory
	revoked              bool
	capabilities         []string
}

func (c *Controller) scanHostRow(id, poolID, hostname string, revoked bool, seen sql.NullString, inv sql.NullString, caps string) hostRow {
	v := hostRow{id: id, poolID: poolID, hostname: hostname, revoked: revoked}
	if inv.Valid {
		json.Unmarshal([]byte(inv.String), &v.inventory)
	}
	json.Unmarshal([]byte(caps), &v.capabilities)
	v.seen = seen.String
	v.connection = c.connection(v.id, seen, v.revoked)
	return v
}

func (c *Controller) loadHostRow(tx *sql.Tx, host string) (hostRow, error) {
	var v hostRow
	var seen, inv sql.NullString
	var caps string
	err := tx.QueryRow("SELECT h.id,h.pool_id,h.hostname,h.revoked,h.last_seen,h.inventory,coalesce(c.capabilities,'[]') FROM hosts h LEFT JOIN host_capabilities c ON c.host_id=h.id WHERE h.id=?", host).Scan(&v.id, &v.poolID, &v.hostname, &v.revoked, &seen, &inv, &caps)
	if err != nil {
		return v, err
	}
	return c.scanHostRow(v.id, v.poolID, v.hostname, v.revoked, seen, inv, caps), nil
}

func (c *Controller) poolHosts(tx *sql.Tx) (map[string]hostRow, error) {
	rows, err := tx.Query("SELECT h.id,h.pool_id,h.hostname,h.revoked,h.last_seen,h.inventory,coalesce(c.capabilities,'[]') FROM hosts h LEFT JOIN host_capabilities c ON c.host_id=h.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]hostRow{}
	for rows.Next() {
		var id, poolID, hostname string
		var revoked bool
		var seen, inv sql.NullString
		var caps string
		if err = rows.Scan(&id, &poolID, &hostname, &revoked, &seen, &inv, &caps); err != nil {
			return nil, err
		}
		out[id] = c.scanHostRow(id, poolID, hostname, revoked, seen, inv, caps)
	}
	return out, rows.Err()
}
