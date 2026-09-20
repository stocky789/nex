// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// TestAbandonedStorageClusterRequestIsNotSuccessAndReleasesBroker: relayStorageCluster wrote
// nothing when the client hung up while it waited on the broker, so net/http emitted a bare
// 200 for a clustered-storage request no Core ever answered. Same defect, and same fix, as
// relayPoolRequest's abandoned join.
func TestAbandonedStorageClusterRequestIsNotSuccessAndReleasesBroker(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	a.report(certificate(t, host.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1,
		Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageClusterCapability, protocol.StorageClusterSessionMonitorCapability}})

	raw, err := json.Marshal(storageClusterAPIRequest{RequestID: "abandoned-cluster", PoolID: pool})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "https://nex.test/api/v1/storage/cluster/apply", strings.NewReader(string(raw))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", a.csrf)
	r.AddCookie(a.cookie)
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); a.c.Handler().ServeHTTP(w, r) }()

	b := a.c.clusterBroker()
	for deadline := time.Now().Add(20 * time.Second); ; {
		b.mu.Lock()
		parked := len(b.pending)
		b.mu.Unlock()
		if parked > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cluster request never reached the broker")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if w.Code/100 == 2 {
		t.Fatalf("an abandoned clustered-storage request was answered with success: %d %s", w.Code, w.Body.String())
	}

	// The broker entry must be released, so the exact retry is accepted rather than
	// refused as already active.
	retry := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		retry <- callRaw(a, http.MethodPost, "/api/v1/storage/cluster/apply", storageClusterAPIRequest{RequestID: "abandoned-cluster", PoolID: pool})
	}()
	var request protocol.StorageClusterRequest
	for deadline := time.Now().Add(20 * time.Second); request.RequestID == ""; {
		if requests := a.c.takeStorageClusters(host.HostID); len(requests) == 1 {
			request = requests[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the retry of an abandoned request_id never reached Core")
		}
		time.Sleep(5 * time.Millisecond)
	}
	a.c.completeStorageClusters(host.HostID, []protocol.StorageClusterResult{{RequestID: request.RequestID, PoolID: pool, HostID: host.HostID, Status: "accepted"}})
	if got := <-retry; got.Code != http.StatusOK {
		t.Fatalf("retry of an abandoned request_id: %d %s", got.Code, got.Body.String())
	}
}
