// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"testing"
	"time"
)

func TestStorageClusterCapabilityNegotiationIsRetryableWithoutDispatch(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	// This is Core's safe first report after reconnect: it offers the v2
	// extension but withholds the work capability until acknowledgement.
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageClusterSessionMonitorCapability}})
	a.call(http.MethodGet, "/api/v1/storage/cluster?pool_id="+pool, nil, nil, http.StatusServiceUnavailable)
	a.call(http.MethodPost, "/api/v1/storage/cluster/credentials", storageClusterCredentialAPIRequest{RequestID: "credential-request", ClusterRequestID: "cluster-request", PoolID: pool, TargetHostID: host.HostID, Kind: "chap", Credentials: protocol.StorageCredentials{Username: "chap", Secret: "secret"}}, nil, http.StatusServiceUnavailable)
	if len(a.c.clusterBroker().pending) != 0 {
		t.Fatal("negotiating Core received queued native work")
	}
	// After the acknowledged follow-up report the same master is immediately
	// eligible; no stored fallback capability was needed.
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageClusterCapability, protocol.StorageClusterSessionMonitorCapability}})
	// The acknowledged route now enters the broker and is completed by the
	// matching Core report, rather than returning the negotiation 503.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest(http.MethodGet, "https://nex.test/api/v1/storage/cluster?pool_id="+pool, nil)
		r.AddCookie(a.cookie)
		r.Header.Set("X-CSRF-Token", a.csrf)
		w := httptest.NewRecorder()
		a.c.Handler().ServeHTTP(w, r)
		done <- w
	}()
	var request protocol.StorageClusterRequest
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		requests := a.c.takeStorageClusters(host.HostID)
		if len(requests) == 1 {
			request = requests[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	if request.RequestID == "" {
		t.Fatal("negotiated status did not reach the Core broker")
	}
	a.c.completeStorageClusters(host.HostID, []protocol.StorageClusterResult{{RequestID: request.RequestID, PoolID: pool, HostID: host.HostID, Status: "accepted"}})
	w := <-done
	if w.Code != http.StatusOK {
		t.Fatalf("negotiated status got %d: %s", w.Code, w.Body.String())
	}
	var result protocol.StorageClusterResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.RequestID != request.RequestID {
		t.Fatalf("negotiated status response %v: %s", err, w.Body.String())
	}
}

func TestStorageClusterCapabilityAbsentRemainsConflict(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	a.report(certificate(t, host.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}})
	a.call(http.MethodGet, "/api/v1/storage/cluster?pool_id="+pool, nil, nil, http.StatusConflict)
}
