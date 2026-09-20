// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"crypto/x509"
	"net/http"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// poolJoinRequest is deliberately delivered to the joining Core's local authenticated
// endpoint. The Controller may display a one-use offer but does not retain it or perform
// the peer exchange.
type poolJoinRequest struct {
	MasterURL  string `json:"master_url"`
	PoolCAPEM  string `json:"pool_ca_pem"`
	Offer      string `json:"offer"`
	PeerOrigin string `json:"peer_origin,omitempty"`
}

type poolJoinOfferRequest struct {
	MemberHostID string `json:"member_host_id"`
	MemberCSR    string `json:"member_csr_pem"`
	MasterURL    string `json:"master_url"`
}

// poolJoinCSR creates the member's private key/CSR before the master issues an offer.
// The key remains in the Core state directory and is never returned.
func (s *Service) poolJoinCSR(w http.ResponseWriter, r *http.Request) {
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil || a.store.State.HostID == "" {
		fail(w, http.StatusConflict, "enroll this Core before requesting a pool join")
		return
	}
	if err = agent.PoolJoinPrecheck(r.Context(), a.store, vmhost.NewManaged(s.cfg.AgentStateDir+"/managed-vms")); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	csr, err := agent.PoolJoinCSR(a.store)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	reply(w, http.StatusOK, map[string]string{"host_id": a.store.State.HostID, "csr_pem": csr})
}

// poolJoinOffer runs only on the selected master. It binds the one-use offer to the
// member host and CSR; the Controller may relay this response but cannot redeem it.
func (s *Service) poolJoinOffer(w http.ResponseWriter, r *http.Request) {
	var v poolJoinOfferRequest
	if !decode(w, r, &v) || !validHTTPSOrigin(v.MasterURL) || v.MemberHostID == "" || len(v.MemberHostID) > 128 || len(v.MemberCSR) > 16384 {
		fail(w, http.StatusBadRequest, "member host, CSR and master URL are required")
		return
	}
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil {
		fail(w, http.StatusConflict, "master Core state unavailable")
		return
	}
	ca, offer, err := agent.CreatePoolJoinOffer(a.store, v.MasterURL, v.MemberHostID, v.MemberCSR)
	if err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	reply(w, http.StatusOK, map[string]string{"pool_ca_pem": ca, "offer": offer})
}

func (s *Service) joinPool(w http.ResponseWriter, r *http.Request) {
	var v poolJoinRequest
	if !decode(w, r, &v) {
		return
	}
	if !validHTTPSOrigin(v.MasterURL) || (v.PeerOrigin != "" && !validHTTPSOrigin(v.PeerOrigin)) || !x509.NewCertPool().AppendCertsFromPEM([]byte(v.PoolCAPEM)) || agent.ValidatePoolJoinOffer(v.Offer) != nil {
		fail(w, http.StatusBadRequest, "master_url, pool_ca_pem and one-use offer are required")
		return
	}
	s.enrollMu.Lock()
	defer s.enrollMu.Unlock()
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil || a.store.State.HostID == "" {
		fail(w, http.StatusConflict, "enroll this Core before joining a pool")
		return
	}
	// The same guard the Controller command path uses. Refusing here, before the agent is
	// stopped, keeps a refused join free of any side effect.
	if err = agent.PoolJoinPrecheck(r.Context(), a.store, vmhost.NewManaged(s.cfg.AgentStateDir+"/managed-vms")); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	handoffAgents.Lock()
	stop, done := a.stop, a.done
	handoffAgents.Unlock()
	if stop != nil {
		stop()
		<-done
	}
	err = agent.JoinPoolPeer(r.Context(), a.store, v.MasterURL, v.PoolCAPEM, v.Offer)
	if err != nil {
		if stop != nil {
			handoffAgents.Lock()
			startHandoffAgent(s.ctx, s.cfg.AgentStateDir, a)
			handoffAgents.Unlock()
		}
		fail(w, http.StatusConflict, err.Error())
		return
	}
	// The host is a member from here on. The remaining stages are durable and retried by
	// the agent, so the response reports the stage reached instead of a conflict that would
	// tell a caller the join did not happen.
	status := agent.CompletePoolJoinStages(r.Context(), a.store, v.PeerOrigin)
	handoffAgents.Lock()
	startHandoffAgent(s.ctx, s.cfg.AgentStateDir, a)
	handoffAgents.Unlock()
	reply(w, http.StatusOK, map[string]any{"host_id": a.store.State.HostID, "pool_id": a.store.State.PoolID, "join": status})
}

// pool reports this Core's own membership so a host-local recovery procedure does not
// depend on the Controller to answer which pool this host belongs to.
func (s *Service) pool(w http.ResponseWriter, r *http.Request) {
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil || a.store.State.HostID == "" {
		fail(w, http.StatusConflict, "enroll this Core before reading its pool")
		return
	}
	membership, err := agent.Membership(a.store)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "pool membership is unavailable")
		return
	}
	role := "member"
	if membership.MasterHostID == a.store.State.HostID {
		role = "master"
	}
	members := membership.Members
	if members == nil {
		members = []protocol.PoolMember{}
	}
	// Restricted is the answer to "why does nothing start here", and it is exactly the
	// question an operator at the host console asks when no Controller answers.
	reply(w, http.StatusOK, map[string]any{"pool_id": membership.PoolID, "pool_name": a.store.State.PoolName, "role": role, "master_host_id": membership.MasterHostID, "members": members, "join": agent.PoolJoinReport(a.store), "restricted": agent.PoolRestrictedReport(a.store)})
}
