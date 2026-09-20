// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"fmt"
	"net/http"
)

func poolMasterOf(tx *sql.Tx, pool string) (master string, status int, err error) {
	if err = tx.QueryRow("SELECT master_host_id FROM pools WHERE id=?", pool).Scan(&master); err != nil || master == "" {
		return "", http.StatusNotFound, fmt.Errorf("pool not found")
	}
	return master, 0, nil
}

func hostKnown(tx *sql.Tx, host string) (status int, err error) {
	var id string
	if err = tx.QueryRow("SELECT id FROM hosts WHERE id=?", host).Scan(&id); err != nil {
		return http.StatusNotFound, fmt.Errorf("host not found")
	}
	return 0, nil
}

func (c *Controller) freshPoolMaster(tx *sql.Tx, pool, purpose string) (master string, status int, err error) {
	master, status, err = poolMasterOf(tx, pool)
	if err != nil {
		return "", status, err
	}
	if err = c.managedHost(tx, pool, master); err != nil {
		return "", http.StatusConflict, fmt.Errorf("the pool master must be fresh and active to %s", purpose)
	}
	return master, 0, nil
}
