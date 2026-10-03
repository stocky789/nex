// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestISCSIInspectionCapabilityRequiresStorageHandshakeAndNativeRuntime(t *testing.T) {
	available := true
	calls := 0
	resetISCSIInspectionProbe(t, func(context.Context) bool { calls++; return available })
	now := time.Unix(1000, 0)
	iscsiInspectionNow = func() time.Time { return now }
	reports := make(chan protocol.ReportRequest, 3)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		reports <- request
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	cfg := RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}
	for _, test := range []struct{ ready, runtime, want bool }{
		{false, true, false}, {true, false, false}, {true, true, true},
	} {
		s.State.StorageProtocolReady = test.ready
		available = test.runtime
		now = now.Add(iscsiInspectionProbeTTL)
		if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err != nil {
			t.Fatal(err)
		}
		got := <-reports
		if !test.ready && hasCapability(got.Capabilities, protocol.StorageDiscoveryPurposeCapability) {
			t.Fatal("pre-handshake Core advertised storage discovery capabilities")
		}
		if test.ready && !hasCapability(got.Capabilities, protocol.StorageDiscoveryPurposeCapability) {
			t.Fatalf("storage-ready Core did not advertise backup-purpose discovery: %v", got.Capabilities)
		}
		if hasCapability(got.Capabilities, protocol.ISCSIInspectCapability) != test.want {
			t.Fatalf("ready=%v runtime=%v capabilities=%v", test.ready, test.runtime, got.Capabilities)
		}
		if !test.ready && calls != 0 {
			t.Fatal("pre-handshake Core performed native inspection capability work")
		}
	}
}

func resetISCSIInspectionProbe(t *testing.T, probe func(context.Context) bool) {
	t.Helper()
	oldProbe, oldNow := iscsiInspectionProbe, iscsiInspectionNow
	iscsiInspectionProbe = probe
	iscsiInspectionMu.Lock()
	iscsiInspectionAt, iscsiInspectionOK = time.Time{}, false
	iscsiInspectionMu.Unlock()
	t.Cleanup(func() {
		iscsiInspectionProbe, iscsiInspectionNow = oldProbe, oldNow
		iscsiInspectionMu.Lock()
		iscsiInspectionAt, iscsiInspectionOK = time.Time{}, false
		iscsiInspectionMu.Unlock()
	})
}

func TestISCSIInspectionProbeIsCachedUntilItsTTLAndTracksTransitions(t *testing.T) {
	available, calls := true, 0
	resetISCSIInspectionProbe(t, func(context.Context) bool { calls++; return available })
	now := time.Unix(1000, 0)
	iscsiInspectionNow = func() time.Time { return now }
	ctx := context.Background()
	if !storageISCSIInspectionAvailable(ctx) || calls != 1 {
		t.Fatal("first report did not probe")
	}
	available = false
	now = now.Add(iscsiInspectionProbeTTL - time.Nanosecond)
	if !storageISCSIInspectionAvailable(ctx) || calls != 1 {
		t.Fatal("probe was forked again inside its TTL")
	}
	now = now.Add(time.Nanosecond)
	if storageISCSIInspectionAvailable(ctx) || calls != 2 {
		t.Fatal("lost runtime was not noticed after the TTL")
	}
	available = true
	now = now.Add(iscsiInspectionProbeTTL)
	if !storageISCSIInspectionAvailable(ctx) || calls != 3 {
		t.Fatal("restored runtime was not noticed after the TTL")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	available = false
	now = now.Add(iscsiInspectionProbeTTL)
	if !storageISCSIInspectionAvailable(cancelled) {
		t.Fatal("a cancelled probe overwrote the cached answer")
	}
	if storageISCSIInspectionAvailable(ctx) {
		t.Fatal("cancelled probe was cached as fresh")
	}
}

func TestSelectedISCSIInspectionRequiresThisNegotiatedCoreScope(t *testing.T) {
	state := State{HostID: "host-1", PoolID: "pool-1"}
	request := protocol.StorageDiscoveryRequest{HostID: state.HostID, PoolID: state.PoolID, Kind: protocol.StorageKindISCSI, TargetIQN: "iqn.2026-09.test:chosen"}
	if storageDiscoveryScopeMatches(state, request) {
		t.Fatal("pre-handshake Core admitted selected-target work")
	}
	state.StorageProtocolReady = true
	if !storageDiscoveryScopeMatches(state, request) {
		t.Fatal("negotiated Core rejected its own scope")
	}
	request.HostID = "other-host"
	if storageDiscoveryScopeMatches(state, request) {
		t.Fatal("Core admitted another initiator's observation")
	}
	request.HostID, request.PoolID = state.HostID, "other-pool"
	if storageDiscoveryScopeMatches(state, request) {
		t.Fatal("Core admitted another pool's observation")
	}
}
