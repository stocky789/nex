// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"net/http"

	"nex.local/nex/internal/nexupdate"
)

func (s *Service) updates(w http.ResponseWriter, r *http.Request) {
	st, err := nexupdate.Check(r.Context(), nexupdate.DefaultRepoURL(), "nex-core-local")
	if err != nil {
		fail(w, 500, "update check failed")
		return
	}
	nexupdate.MergeProgress(&st, s.cfg.DataDir)
	reply(w, 200, st)
}

func (s *Service) applyUpdate(w http.ResponseWriter, r *http.Request) {
	result, err := nexupdate.StartApplyLocal(nexupdate.DefaultRepoURL(), "nex-core-local", s.cfg.DataDir)
	status := 202
	if result.Status == "failed" || err != nil {
		status = 409
		if result.Message == "" && err != nil {
			result = nexupdate.ApplyResult{Status: "failed", Message: err.Error()}
		}
	}
	reply(w, status, result)
}
