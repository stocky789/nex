// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"net/http"

	"nex.local/nex/internal/agent"
)

// storageClusterApply is deliberately local Core authority. A Controller relay may carry
// this authenticated operator request transiently, but does not become a job owner.
func (s *Service) storageClusterApply(w http.ResponseWriter, r *http.Request) {
	var request agent.StorageClusterRequest
	if !decode(w, r, &request) {
		return
	}
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil || a.store.State.HostID == "" {
		fail(w, http.StatusConflict, "enroll this Core before configuring clustered storage")
		return
	}
	// Local recovery uses the identical scoped request as the transient Controller relay.
	request.HostID, request.PoolID, request.Action = a.store.State.HostID, a.store.State.PoolID, "apply"
	reply(w, http.StatusAccepted, agent.ExecuteStorageClusterRequest(r.Context(), a.store, request))
}

func (s *Service) storageClusterStatus(w http.ResponseWriter, r *http.Request) {
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil || a.store.State.HostID == "" {
		fail(w, http.StatusConflict, "Core cluster state unavailable")
		return
	}
	reply(w, http.StatusOK, agent.StorageClusterStatus(a.store))
}
