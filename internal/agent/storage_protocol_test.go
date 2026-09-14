// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestStorageRepositoryCommandRequiresPreflight(t *testing.T) {
	s := testStore(t)
	cmd := protocol.Command{OperationID: "storage-nfs", Kind: "storage-register", Storage: &protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Name: "nfs", Kind: protocol.StorageKindNFS,
		Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example", Export: "/images"}},
	}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err == nil {
		t.Fatal("repository command accepted before preflight")
	}
	s.State.StorageProtocolReady = true
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	if got := s.State.Journal[cmd.OperationID].Command; got == nil || got.Storage.StorageKind() != protocol.StorageKindNFS {
		t.Fatalf("repository command was not durably admitted: %+v", got)
	}
}

func TestCredentialRetirementRequiresPreflight(t *testing.T) {
	s := testStore(t)
	cmd := protocol.Command{OperationID: "retire-1", Kind: "storage-credential-retire", CredentialRetire: &protocol.StorageCredentialRetireRequest{Receipt: "receipt-1", PoolID: "pool-1", HostID: "host-1"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err == nil {
		t.Fatal("credential retirement accepted before preflight")
	}
	s.State.StorageProtocolReady = true
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	if got := s.State.Journal[cmd.OperationID].Command; got == nil || got.CredentialRetire == nil || got.CredentialRetire.Receipt != "receipt-1" {
		t.Fatalf("credential retirement was not durably admitted: %+v", got)
	}
}

func TestStoragePreflightFallsBackToStrictV1WhenSafe(t *testing.T) {
	var reports []protocol.ReportRequest
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		reports = append(reports, request)
		if request.Version == protocol.Version {
			http.Error(w, "unsupported protocol version", http.StatusConflict)
			return
		}
		if request.Version != protocol.LegacyVersion || request.Inventory.Managed != nil {
			t.Errorf("unsafe legacy fallback: %+v", request)
		}
		json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if !s.State.StorageProtocolLegacy || s.State.StorageProtocolReady || len(reports) != 2 {
		t.Fatalf("preflight did not make one safe legacy retry: state=%+v reports=%d", s.State, len(reports))
	}
}

func TestStoragePreflightDoesNotFallbackWithRepositoryState(t *testing.T) {
	state := State{Journal: map[string]JournalEntry{"repo": {Command: &protocol.Command{Kind: "storage-register", Storage: &protocol.StorageRegistration{Kind: protocol.StorageKindNFS}}}}}
	if legacyStorageSafe(state, &protocol.ManagedState{}, true) {
		t.Fatal("repository state allowed legacy fallback")
	}
	if legacyStorageSafe(State{}, &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{{Kind: "storage-register", Status: "failed"}}}, true) {
		t.Fatal("storage history allowed legacy fallback")
	}
}

func TestClusteredStoragePreflightProtectsStrictOlderControllerAndReconnect(t *testing.T) {
	type oldStatus struct {
		ClusterID       string `json:"cluster_id"`
		RepositoryID    string `json:"repository_id"`
		Healthy         bool   `json:"healthy"`
		Quorate         bool   `json:"quorate"`
		Mounted         bool   `json:"mounted"`
		Error           string `json:"error,omitempty"`
		Status          string `json:"status"`
		FencingVerified bool   `json:"fencing_verified"`
	}
	type oldMember struct {
		HostID               string    `json:"host_id"`
		Status               oldStatus `json:"status"`
		Generation           uint64    `json:"generation"`
		MembershipGeneration uint64    `json:"membership_generation"`
		ClusterID            string    `json:"cluster_id"`
		ConfigDigest         string    `json:"config_digest"`
		Stage                string    `json:"stage"`
		UpdatedAt            time.Time `json:"updated_at"`
	}
	type oldOperation struct {
		RequestID            string      `json:"request_id"`
		MembershipGeneration uint64      `json:"membership_generation"`
		Config               any         `json:"config"`
		Status               string      `json:"status"`
		UpdatedAt            time.Time   `json:"updated_at"`
		Members              []oldMember `json:"members,omitempty"`
	}
	type oldClusterResult struct {
		RequestID  string         `json:"request_id"`
		PoolID     string         `json:"pool_id"`
		HostID     string         `json:"host_id"`
		Status     string         `json:"status"`
		ObservedAt time.Time      `json:"observed_at"`
		Operations []oldOperation `json:"operations,omitempty"`
	}
	type oldReportRequest struct {
		Capabilities    []string           `json:"capabilities,omitempty"`
		Version         int                `json:"version"`
		Sequence        uint64             `json:"sequence"`
		Inventory       any                `json:"inventory,omitempty"`
		Results         any                `json:"results"`
		StorageClusters []oldClusterResult `json:"storage_clusters,omitempty"`
	}
	result := protocol.StorageClusterResult{RequestID: "cluster-1", PoolID: "pool-1", HostID: "host-1", Status: "accepted", ObservedAt: time.Now().UTC(), Operations: []protocol.StorageClusterOperation{{RequestID: "cluster-1", MembershipGeneration: 1, Status: "applying", UpdatedAt: time.Now().UTC(), Members: []protocol.StorageClusterMemberReport{
		{HostID: "host-1", Generation: 1, MembershipGeneration: 1, ClusterID: "cluster-1", ConfigDigest: "digest", Stage: "health", UpdatedAt: time.Now().UTC(), Status: protocol.StorageClusterStatus{ClusterID: "cluster-1", RepositoryID: "repo-1", SessionMonitorReady: true, Status: "healthy"}},
		{HostID: "host-1", Generation: 1, MembershipGeneration: 1, ClusterID: "cluster-1", ConfigDigest: "digest", Stage: "monitor", UpdatedAt: time.Now().UTC(), Status: protocol.StorageClusterStatus{ClusterID: "cluster-1", RepositoryID: "repo-1", SessionMonitorReady: true, Status: "pending"}},
	}}}}
	// This is the actual old strict-decoder failure: the additive field is
	// rejected inside the nested member status, not by a generic JSON parser.
	raw, err := json.Marshal(protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Results: []protocol.Result{}, StorageClusters: []protocol.StorageClusterResult{result}})
	if err != nil {
		t.Fatal(err)
	}
	var rejected oldReportRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rejected); err == nil || !strings.Contains(err.Error(), "session_monitor_ready") {
		t.Fatalf("old strict schema did not reject the additive field: %v", err)
	}
	projected := storageClusterResultsForController([]protocol.StorageClusterResult{result}, false)
	if got := result.Operations[0].Members; len(got) != 2 || !got[0].Status.SessionMonitorReady || got[1].Stage != "monitor" {
		t.Fatalf("report projection mutated Core-owned member state: %+v", got)
	}
	if len(projected[0].Operations[0].Members) != 1 || projected[0].Operations[0].Members[0].Status.SessionMonitorReady {
		t.Fatalf("legacy projection retained unsupported member data: %+v", projected)
	}
	raw, err = json.Marshal(protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Results: []protocol.Result{}, StorageClusters: projected})
	if err != nil {
		t.Fatal(err)
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rejected); err != nil {
		t.Fatalf("old strict schema rejected legacy projection: %v", err)
	}
	oldReports := 0
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request oldReportRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			t.Errorf("strict older Controller received an incompatible report: %v", err)
			return
		}
		oldReports++
		if oldReports == 1 {
			if contains(request.Capabilities, protocol.StorageClusterCapability) || !contains(request.Capabilities, protocol.StorageClusterSessionMonitorCapability) || len(request.StorageClusters) != 0 {
				t.Fatalf("new Core did not make an extension preflight: %+v", request)
			}
			json.NewEncoder(w).Encode(protocol.ReportResponse{StorageClusters: &protocol.StorageClusterSupport{Capability: protocol.StorageClusterCapability}})
			return
		}
		if !contains(request.Capabilities, protocol.StorageClusterCapability) || len(request.StorageClusters) != 1 || len(request.StorageClusters[0].Operations[0].Members) != 1 || request.StorageClusters[0].Operations[0].Members[0].Stage != "health" {
			t.Fatalf("old Controller received unprojected clustered result: %+v", request.StorageClusters)
		}
		json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	s.State.StorageProtocolReady = true
	s.State.StorageClusterProtocolReady = true // persisted acknowledgement from a replaced Controller
	result.PoolID, result.HostID = s.State.PoolID, s.State.HostID
	appendStorageClusters(s, []protocol.StorageClusterResult{result})
	if err := resetStorageClusterProtocol(s); err != nil {
		t.Fatal(err)
	}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if oldReports != 1 || !s.State.StorageClusterProtocolReady || s.State.StorageClusterSessionMonitorProtocolReady {
		t.Fatalf("strict Controller acknowledgement was not recorded safely: reports=%d state=%+v", oldReports, s.State)
	}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if oldReports != 2 || !s.State.StorageClusterProtocolReady || s.State.StorageClusterSessionMonitorProtocolReady {
		t.Fatalf("old Controller acknowledgement was not safely retained: reports=%d state=%+v", oldReports, s.State)
	}
	// The withheld result remains available for a matching Controller after reconnect.
	if err := resetStorageClusterProtocol(s); err != nil {
		t.Fatal(err)
	}
	appendStorageClusters(s, []protocol.StorageClusterResult{result})
	matchingReports := 0
	_, matchingIdentity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		matchingReports++
		if matchingReports == 1 {
			if len(request.StorageClusters) != 0 || contains(request.Capabilities, protocol.StorageClusterCapability) || !contains(request.Capabilities, protocol.StorageClusterSessionMonitorCapability) {
				t.Fatalf("matching Controller did not receive clean clustered preflight: %+v", request)
			}
			json.NewEncoder(w).Encode(protocol.ReportResponse{StorageClusters: &protocol.StorageClusterSupport{Capability: protocol.StorageClusterSessionMonitorCapability}})
			return
		}
		if matchingReports == 2 && (!contains(request.Capabilities, protocol.StorageClusterCapability) || len(request.StorageClusters) != 1 || len(request.StorageClusters[0].Operations[0].Members) != 2 || !request.StorageClusters[0].Operations[0].Members[0].Status.SessionMonitorReady) {
			t.Fatalf("matching Controller did not receive v2 cluster result after acknowledgement: %+v", request)
		}
		if matchingReports == 2 {
			// A valid base acknowledgement is an explicit downgrade: the Core
			// must stop serializing the additive member fields immediately.
			json.NewEncoder(w).Encode(protocol.ReportResponse{StorageClusters: &protocol.StorageClusterSupport{Capability: protocol.StorageClusterCapability}})
			return
		}
		if matchingReports != 3 || len(request.StorageClusters) != 1 || len(request.StorageClusters[0].Operations[0].Members) != 1 || request.StorageClusters[0].Operations[0].Members[0].Status.SessionMonitorReady {
			t.Fatalf("Core did not apply base acknowledgement downgrade: %+v", request)
		}
		json.NewEncoder(w).Encode(protocol.ReportResponse{StorageClusters: &protocol.StorageClusterSupport{Capability: protocol.StorageClusterCapability}})
	}))
	s.State.Controller = matchingIdentity.Controller
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if !s.State.StorageClusterProtocolReady || !s.State.StorageClusterSessionMonitorProtocolReady {
		t.Fatal("matching Controller acknowledgement was not persisted")
	}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if !s.State.StorageClusterProtocolReady || s.State.StorageClusterSessionMonitorProtocolReady {
		t.Fatalf("base acknowledgement did not clear extension state: %+v", s.State)
	}
	appendStorageClusters(s, []protocol.StorageClusterResult{result})
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
}

func TestReportRedeemsCredentialOfferBeforeCommandAdmission(t *testing.T) {
	offer := protocol.StorageCredentialOffer{Handle: "offer-1", RequestID: "credential-1", RegistrationRequestID: "register-1", PoolID: "pool-1", HostID: "host-1", ExpiresAt: time.Now().Add(time.Minute).UTC()}
	acknowledged := false
	reports := 0
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/report":
			var request protocol.ReportRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			if request.Version != protocol.Version || !contains(request.Capabilities, protocol.StorageRepositoryCapability) {
				t.Errorf("credential offer sent before activated v2 report: %+v", request)
			}
			reports++
			if reports == 2 && (request.Inventory.Managed == nil || len(request.Inventory.Managed.CredentialReceipts) != 1 || !request.Inventory.Managed.CredentialReceipts[0].RetireEligible) {
				t.Errorf("Core cleanup projection was lost in report: %+v", request.Inventory.Managed)
			}
			response := protocol.ReportResponse{}
			if reports == 1 {
				response.CredentialOffers = []protocol.StorageCredentialOffer{offer}
			}
			json.NewEncoder(w).Encode(response)
		case "/api/v1/agent/storage/credentials/offer-1":
			json.NewEncoder(w).Encode(protocol.StorageCredentialHandoff{Offer: offer, Credentials: protocol.StorageCredentials{Username: "operator", Secret: "test-secret"}})
		case "/api/v1/agent/storage/credentials/offer-1/ack":
			var receipt protocol.StorageCredentialReceipt
			if err := json.NewDecoder(r.Body).Decode(&receipt); err != nil || receipt.RequestID != offer.RequestID || receipt.Receipt == "" {
				t.Errorf("invalid credential acknowledgement: %+v %v", receipt, err)
				return
			}
			acknowledged = true
			json.NewEncoder(w).Encode(receipt)
		default:
			http.NotFound(w, r)
		}
	}))
	s := testStore(t)
	s.State = identity
	s.State.PoolID = offer.PoolID
	s.State.StorageProtocolReady = true
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	receipts, err := vmhost.StorageCredentialReceipts(s.dir)
	if err != nil || !acknowledged || len(receipts) != 1 || receipts[0].RequestID != offer.RequestID {
		t.Fatalf("credential handoff did not persist then acknowledge: receipts=%+v ack=%v err=%v", receipts, acknowledged, err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
