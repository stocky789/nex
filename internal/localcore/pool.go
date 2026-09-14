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
	managed, _ := vmhost.NewManaged(s.cfg.AgentStateDir + "/managed-vms").Applied(r.Context())
	if len(managed.VMs) != 0 {
		fail(w, http.StatusConflict, "joining a pool with managed VMs is not supported; migrate or remove workloads first")
		return
	}
	for _, storage := range managed.Storage {
		if storage.StorageKind() != protocol.StorageKindLocal {
			fail(w, http.StatusConflict, "joining a pool with existing remote storage is not supported; detach it first")
			return
		}
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
	if v.PeerOrigin != "" {
		_ = agent.AdvertisePoolPeerOrigin(r.Context(), a.store, v.PeerOrigin)
	}
	if err = vmhost.RehomePoolScope(s.cfg.AgentStateDir+"/managed-vms", a.store.State.PoolID, a.store.State.HostID); err != nil {
		handoffAgents.Lock()
		startHandoffAgent(s.ctx, s.cfg.AgentStateDir, a)
		handoffAgents.Unlock()
		fail(w, http.StatusConflict, "joined pool but local default resource scope needs manual recovery: "+err.Error())
		return
	}
	handoffAgents.Lock()
	startHandoffAgent(s.ctx, s.cfg.AgentStateDir, a)
	handoffAgents.Unlock()
	reply(w, http.StatusOK, map[string]string{"host_id": a.store.State.HostID, "pool_id": a.store.State.PoolID})
}
