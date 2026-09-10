// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http"
	"path/filepath"

	"nex.local/nex/internal/nexupdate"
)

func (c *Controller) updates(w http.ResponseWriter, r *http.Request, user, csrf string) {
	st, err := nexupdate.CheckWithCache(r.Context(), nexupdate.DefaultRepoURL(), "nex-controller", filepath.Join(c.cfg.DataDir, "apt"))
	if err != nil {
		fail(w, 500, "update check failed")
		return
	}
	nexupdate.MergeProgress(&st, c.cfg.DataDir)
	write(w, 200, st)
}

func (c *Controller) applyUpdate(w http.ResponseWriter, r *http.Request, user, csrf string) {
	if err := nexupdate.ControllerApplyReady(); err != nil {
		fail(w, 503, err.Error())
		return
	}
	repo := nexupdate.DefaultRepoURL()
	if err := nexupdate.RequestApply(c.cfg.DataDir, repo, "nex-controller"); err != nil {
		fail(w, 500, err.Error())
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "persistence failure")
		return
	}
	defer tx.Rollback()
	if err := c.audit(tx, user, "update.apply", "nex-controller"); err != nil || tx.Commit() != nil {
		fail(w, 500, "persistence failure")
		return
	}
	write(w, 202, nexupdate.ApplyResult{
		Status:    "scheduled",
		Message:   "update requested; privileged helper will apply and restart",
		Operation: "pending-update",
	})
}
