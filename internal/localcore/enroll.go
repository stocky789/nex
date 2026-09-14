// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"crypto/x509"
	"net/http"

	"nex.local/nex/internal/agent"
)

type enrollmentRequest struct {
	ControllerURL string `json:"controller_url"`
	CAPEM         string `json:"ca_pem"`
	Token         string `json:"token"`
	ReplaceHost   string `json:"replace_host,omitempty"`
}

// enrollmentState reports the local agent's current identity for GET /api/local/v1/host.
// Empty strings mean not enrolled; a store that cannot be opened is treated the same way.
func (s *Service) enrollmentState() (hostID, controllerURL string) {
	handoffAgents.Lock()
	defer handoffAgents.Unlock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	if err != nil {
		return "", ""
	}
	return a.hostID, a.controller
}

func validEnrollmentToken(token string) bool {
	if token == "" || len(token) > 256 {
		return false
	}
	for _, b := range []byte(token) {
		if b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

// reconcileEnrollmentCache makes the registry reflect the Store after every
// enrollment outcome. The caller holds handoffAgents.Lock.
func reconcileEnrollmentCache(a *runningAgent) {
	a.hostID = a.store.State.HostID
	if a.hostID == "" {
		a.controller, a.trust = "", ""
		return
	}
	a.controller = a.store.State.Controller
	a.trust = a.store.State.TrustPEM
}

// enrollment lets the Controller push a single-use enrollment token to this host over
// its existing local session, replacing the manual `nex-core-agent enroll` step. It
// never logs the token or CA, and serializes attempts so a retried Add-host click
// cannot race the embedded agent's own state.
func (s *Service) enrollment(w http.ResponseWriter, r *http.Request) {
	var v enrollmentRequest
	if !decode(w, r, &v) {
		return
	}
	if !validHTTPSOrigin(v.ControllerURL) {
		fail(w, 400, "controller_url must be an HTTPS origin without credentials, path, query or fragment")
		return
	}
	if !x509.NewCertPool().AppendCertsFromPEM([]byte(v.CAPEM)) {
		fail(w, 400, "ca_pem must contain at least one certificate")
		return
	}
	if !validEnrollmentToken(v.Token) {
		fail(w, 400, "token must be 1-256 bytes with no control characters")
		return
	}
	if len(v.ReplaceHost) > 128 {
		fail(w, 400, "replace_host must be at most 128 characters")
		return
	}

	// ponytail: one enrollment in flight per host; a genuinely concurrent retry queues
	// behind this lock rather than racing the embedded agent's on-disk state.
	s.enrollMu.Lock()
	defer s.enrollMu.Unlock()

	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil {
		fail(w, 502, "local agent state unavailable")
		return
	}
	sameController := a.store.State.Controller == v.ControllerURL && a.store.State.TrustPEM == v.CAPEM
	if a.hostID != "" && v.ReplaceHost != a.hostID && sameController {
		reply(w, 409, map[string]string{"error": "already enrolled", "host_id": a.hostID})
		return
	}

	// A replace_host re-enrollment can target the identity this process is actively
	// running under (e.g. a host revoked on the Controller while still locally up).
	// Pause that agent before mutating its store, so Enroll's writes never race the
	// report loop's own reads of the same in-memory state, and always resume it
	// afterward. Enroll itself keeps a replacement's active credential intact until
	// its new credential has been durably accepted; its pending CSR is deliberately
	// retained on a failed or lost response so the same request can be retried.
	handoffAgents.Lock()
	stop, done := a.stop, a.done
	handoffAgents.Unlock()
	if stop != nil {
		stop()
		<-done
	}

	if err := agent.Enroll(r.Context(), a.store, v.ControllerURL, v.CAPEM, v.Token, v.ReplaceHost); err != nil {
		handoffAgents.Lock()
		// Keep the registry's observable enrolled identity aligned with Store on
		// every error. A pre-publication replacement failure leaves the old
		// identity here; a post-rename sync failure leaves the new identity here
		// and still returns 502 for Controller-side reconciliation. Unenrolled
		// pending first attempts remain visibly unenrolled.
		reconcileEnrollmentCache(a)
		if stop != nil {
			startHandoffAgent(s.ctx, s.cfg.AgentStateDir, a)
		}
		handoffAgents.Unlock()
		fail(w, 502, err.Error())
		return
	}
	handoffAgents.Lock()
	a.hostID = a.store.State.HostID
	a.controller = a.store.State.Controller
	a.trust = a.store.State.TrustPEM
	startHandoffAgent(s.ctx, s.cfg.AgentStateDir, a)
	handoffAgents.Unlock()
	reply(w, 200, map[string]string{"host_id": a.hostID})
}
