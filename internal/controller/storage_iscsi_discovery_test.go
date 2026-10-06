// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestSelectedISCSIInspectionRequiresPermissionScopeAndNewCore(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	caps := []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.StorageDiscoveryCapability}
	a.report(certificate(t, host.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	body := protocol.StorageDiscoveryRequest{RequestID: "inspect-1", PoolID: pool, HostID: host.HostID, Kind: protocol.StorageKindISCSI, Server: "iscsi.example.test", TargetIQN: "iqn.2026-09.test:chosen"}
	cookie, csrf := a.cookie, a.csrf
	a.cookie = nil
	a.call(http.MethodPost, "/api/v1/storage/discover", body, nil, http.StatusUnauthorized)
	a.cookie, a.csrf = cookie, ""
	a.call(http.MethodPost, "/api/v1/storage/discover", body, nil, http.StatusForbidden)
	a.csrf = csrf
	a.call(http.MethodPost, "/api/v1/storage/discover", body, nil, http.StatusConflict)
	other := a.enroll(a.token(a.site(), ""), csr(t))
	body.HostID = other.HostID
	a.call(http.MethodPost, "/api/v1/storage/discover", body, nil, http.StatusConflict)
	body.HostID, body.TargetIQN = host.HostID, "iqn.invalid\nvalue"
	a.call(http.MethodPost, "/api/v1/storage/discover", body, nil, http.StatusBadRequest)
	body.TargetIQN = "iqn.2026-09.test:chosen"
	body.Credentials = &protocol.StorageCredentials{Username: "chap", Secret: strings.Repeat("s", 256)}
	a.call(http.MethodPost, "/api/v1/storage/discover", body, nil, http.StatusBadRequest)
	// Target listing never logs in, so CHAP there is refused rather than silently ignored.
	body.TargetIQN, body.Credentials = "", &protocol.StorageCredentials{Username: "chap", Secret: "fixture-secret"}
	a.call(http.MethodPost, "/api/v1/storage/discover", body, nil, http.StatusBadRequest)
	broker := a.c.discoveryBroker()
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.pending) != 0 {
		t.Fatal("rejected inspection queued native work")
	}
}

func TestSelectedISCSIInspectionRelaysCoreObservationsWithoutDurableState(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	caps := []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.StorageDiscoveryCapability, protocol.ISCSIInspectCapability}
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	for i, observations := range [][]protocol.ISCSILUNDiscovery{
		{}, {{LUN: 0, WWID: "5001020304050607", FilesystemType: "ext4", FilesystemUUID: "12345678-1234-4234-8234-123456789012", SizeBytes: 104857600}},
		{{LUN: 0, Error: "No filesystem signature detected: blank or unsupported media"}, {LUN: 7, FilesystemType: "xfs"}},
	} {
		body := protocol.StorageDiscoveryRequest{RequestID: "inspect-" + strings.Repeat("x", i+1), PoolID: pool, HostID: host.HostID, Kind: protocol.StorageKindISCSI, Server: "iscsi.example.test", TargetIQN: "iqn.2026-09.test:chosen", Credentials: &protocol.StorageCredentials{Username: "fixture-chap", Secret: "fixture-secret"}}
		raw, _ := json.Marshal(body)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r := httptest.NewRequest(http.MethodPost, "https://nex.test/api/v1/storage/discover", bytes.NewReader(raw)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", a.csrf)
		r.AddCookie(a.cookie)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { w := httptest.NewRecorder(); a.c.Handler().ServeHTTP(w, r); done <- w }()
		var dispatched []protocol.StorageDiscoveryRequest
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			dispatched = a.c.takeStorageDiscoveries(host.HostID)
			if len(dispatched) > 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if len(dispatched) != 1 || dispatched[0].TargetIQN != body.TargetIQN || dispatched[0].Credentials == nil {
			cancel()
			t.Fatal("selected-target inspection did not reach only the chosen Core")
		}
		result := protocol.StorageDiscoveryResult{RequestID: body.RequestID, PoolID: pool, HostID: host.HostID, Status: "succeeded", ISCSILUNs: observations, ObservedAt: time.Now().UTC()}
		a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: uint64(i + 2), Capabilities: caps, StorageDiscoveries: []protocol.StorageDiscoveryResult{result}})
		w := <-done
		cancel()
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "fixture-secret") {
			t.Fatalf("unsafe/failed inspection response: %d %s", w.Code, w.Body.String())
		}
		var got protocol.StorageDiscoveryResult
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Status != "succeeded" || len(got.ISCSILUNs) != len(observations) || (len(observations) > 0 && !reflect.DeepEqual(got.ISCSILUNs, observations)) {
			t.Fatalf("Controller altered Core's LUN observations: %+v %v", got, err)
		}
		var durable int
		if err := a.c.db.QueryRow("SELECT count(*) FROM operations WHERE request_id=?", body.RequestID).Scan(&durable); err != nil || durable != 0 {
			t.Fatalf("inspection was journaled: count=%d error=%v", durable, err)
		}
	}
}

func TestCancelledSelectedISCSIRequestDropsBrokerAndLateResult(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	caps := []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.StorageDiscoveryCapability, protocol.ISCSIInspectCapability}
	a.report(certificate(t, host.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	body := protocol.StorageDiscoveryRequest{RequestID: "inspect-cancel", PoolID: pool, HostID: host.HostID, Kind: protocol.StorageKindISCSI, Server: "iscsi.example.test", TargetIQN: "iqn.2026-09.test:chosen"}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "https://nex.test/api/v1/storage/discover", bytes.NewReader(raw)).WithContext(ctx)
	r.AddCookie(a.cookie)
	r.Header.Set("X-CSRF-Token", a.csrf)
	done := make(chan struct{})
	go func() { a.c.Handler().ServeHTTP(httptest.NewRecorder(), r); close(done) }()
	deadline := time.Now().Add(time.Second)
	var requests []protocol.StorageDiscoveryRequest
	for time.Now().Before(deadline) {
		requests = a.c.takeStorageDiscoveries(host.HostID)
		if len(requests) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(requests) != 1 {
		t.Fatal("inspection never queued")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("cancelled client kept the broker request alive")
	}
	a.c.completeStorageDiscoveries(host.HostID, []protocol.StorageDiscoveryResult{{RequestID: body.RequestID, PoolID: pool, HostID: host.HostID, Status: "succeeded", ISCSILUNs: []protocol.ISCSILUNDiscovery{{LUN: 0}}}})
	broker := a.c.discoveryBroker()
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.pending) != 0 {
		t.Fatal("late observation recreated cancelled broker work")
	}
}

// startDiscovery posts a discovery request and reports whether it reached the broker for the
// host, or was refused first. The returned cancel ends the held request.
func startDiscovery(t *testing.T, a *testClient, host string, body protocol.StorageDiscoveryRequest) (dispatched []protocol.StorageDiscoveryRequest, code int) {
	t.Helper()
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "https://nex.test/api/v1/storage/discover", bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", a.csrf)
	r.AddCookie(a.cookie)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); a.c.Handler().ServeHTTP(w, r); done <- w }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if dispatched = a.c.takeStorageDiscoveries(host); len(dispatched) > 0 {
			cancel()
			<-done
			return dispatched, 0
		}
		select {
		case w := <-done:
			return nil, w.Code
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	return nil, 0
}

func TestPlainISCSIListingStillWorksOnACoreWithoutSelectedTargetInspection(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	caps := []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.StorageDiscoveryCapability}
	a.report(certificate(t, host.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	listing := protocol.StorageDiscoveryRequest{RequestID: "listing-1", PoolID: pool, HostID: host.HostID, Kind: protocol.StorageKindISCSI, Server: "iscsi.example.test"}
	if dispatched, code := startDiscovery(t, a, host.HostID, listing); len(dispatched) != 1 || code != 0 {
		t.Fatalf("plain iSCSI listing was refused on a Core without selected-target inspection: dispatched=%d code=%d", len(dispatched), code)
	}
}

func TestBackupPurposeDiscoveryNeedsACoreThatHonoursIt(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	caps := []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.StorageDiscoveryCapability}
	cert := certificate(t, host.CertificatePEM)
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	body := protocol.StorageDiscoveryRequest{RequestID: "purpose-1", PoolID: pool, HostID: host.HostID, Kind: protocol.StorageKindSMB, Server: "files.example.test", Purpose: protocol.StorageDiscoveryPurposeBackup}
	w := a.call(http.MethodPost, "/api/v1/storage/discover", body, nil, http.StatusConflict)
	if strings.Contains(w.Body.String(), "iscsi") {
		t.Fatalf("unexpected refusal: %s", w.Body.String())
	}
	plain := body
	plain.Purpose, plain.RequestID = "", "purpose-plain"
	if dispatched, _ := startDiscovery(t, a, host.HostID, plain); len(dispatched) != 1 {
		t.Fatal("purpose-less discovery was refused on a Core without the purpose capability")
	}
	if dispatched := a.c.takeStorageDiscoveries(host.HostID); len(dispatched) != 0 {
		t.Fatal("refused purpose request was queued")
	}
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: append(caps, protocol.StorageDiscoveryPurposeCapability)})
	body.RequestID = "purpose-2"
	if dispatched, code := startDiscovery(t, a, host.HostID, body); len(dispatched) != 1 || dispatched[0].Purpose != protocol.StorageDiscoveryPurposeBackup || code != 0 {
		t.Fatalf("purpose request did not reach a Core that honours it: %+v code=%d", dispatched, code)
	}
	body.RequestID, body.Kind = "purpose-3", protocol.StorageKindISCSI
	a.call(http.MethodPost, "/api/v1/storage/discover", body, nil, http.StatusBadRequest)
}

func TestInvalidISCSIObservationsBecomeAFailedResult(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	b := a.c.discoveryBroker()
	ch := make(chan protocol.StorageDiscoveryResult, 1)
	b.mu.Lock()
	b.pending["bad-result"] = storageDiscoveryPending{request: protocol.StorageDiscoveryRequest{RequestID: "bad-result", PoolID: pool, HostID: host.HostID}, result: ch}
	b.mu.Unlock()
	a.c.completeStorageDiscoveries(host.HostID, []protocol.StorageDiscoveryResult{{RequestID: "bad-result", PoolID: pool, HostID: host.HostID, Status: "succeeded", ISCSILUNs: []protocol.ISCSILUNDiscovery{{LUN: 1, Error: "bad\nline"}}}})
	select {
	case got := <-ch:
		if got.Status != "failed" || len(got.ISCSILUNs) != 0 {
			t.Fatalf("invalid observations reached the operator: %+v", got)
		}
	default:
		t.Fatal("invalid result was dropped without answering the waiting request")
	}
}
