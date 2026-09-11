// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
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
