// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func testStorageCredentialRequest() storageCredentialRequest {
	return storageCredentialRequest{RequestID: "credential-1", RegistrationRequestID: "storage-1", PoolID: "pool-1", HostID: "host-1", Credentials: protocol.StorageCredentials{Username: "operator", Secret: "secret-not-in-offer"}}
}

func TestCredentialBrokerScopesAndAcknowledgesCoreReceipt(t *testing.T) {
	b := newCredentialBroker()
	v := testStorageCredentialRequest()
	offer, err := b.offer(v)
	if err != nil || offer.Handle == "" || offer.RepositoryID != "" {
		t.Fatalf("offer %#v: %v", offer, err)
	}
	encoded, err := json.Marshal(offer)
	if err != nil || string(encoded) == "" || strings.Contains(string(encoded), v.Credentials.Secret) {
		t.Fatalf("offer serializes a secret: %s, %v", encoded, err)
	}
	if _, err = b.redeem(offer.Handle, "other-host"); err == nil {
		t.Fatal("wrong host redeemed credential")
	}
	handoff, err := b.redeem(offer.Handle, v.HostID)
	if err != nil || handoff.Offer != offer || handoff.Credentials != v.Credentials {
		t.Fatalf("handoff %#v: %v", handoff.Offer, err)
	}
	if _, err = b.redeem(offer.Handle, v.HostID); err == nil {
		t.Fatal("delivered offer still exposed credentials before acknowledgement")
	}
	bad := protocol.StorageCredentialReceipt{Receipt: "receipt-1", RequestID: v.RequestID, RegistrationRequestID: "other", PoolID: v.PoolID, HostID: v.HostID, Status: "ready"}
	if err = b.acknowledge(offer.Handle, v.HostID, bad); err == nil {
		t.Fatal("receipt with a different registration request accepted")
	}
	receipt := protocol.StorageCredentialReceipt{Receipt: "receipt-1", RequestID: v.RequestID, RegistrationRequestID: v.RegistrationRequestID, PoolID: v.PoolID, HostID: v.HostID, Status: "ready"}
	if err = b.acknowledge(offer.Handle, v.HostID, receipt); err != nil {
		t.Fatal(err)
	}
	if got, ok := b.wait(v.RequestID); !ok || got != receipt {
		t.Fatalf("wait %#v %v", got, ok)
	}
	if _, err = b.redeem(offer.Handle, v.HostID); err == nil {
		t.Fatal("acknowledged offer still exposed credentials")
	}
	if got := b.entries[v.RequestID].credentials; got.Secret != "" || got.Username != "" {
		t.Fatalf("acknowledged credentials retained %#v", got)
	}
	if _, err = b.offer(v); err != nil {
		t.Fatalf("same acknowledged request did not recover receipt: %v", err)
	}
}

func TestCredentialBrokerReportsCoreRefusalAndAllowsResend(t *testing.T) {
	b := newCredentialBroker()
	v := testStorageCredentialRequest()
	offer, err := b.offer(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.redeem(offer.Handle, v.HostID); err != nil {
		t.Fatal(err)
	}
	refusal := protocol.StorageCredentialReceipt{RequestID: v.RequestID, RegistrationRequestID: v.RegistrationRequestID, PoolID: v.PoolID, HostID: v.HostID, Status: "failed", Error: "send it again with a new request id"}
	if err = b.acknowledge(offer.Handle, v.HostID, refusal); err != nil {
		t.Fatal(err)
	}
	if got, ok := b.wait(v.RequestID); ok || got.Error != refusal.Error {
		t.Fatalf("wait %#v %v", got, ok)
	}
	if next, err := b.offer(v); err != nil || next.Handle == offer.Handle {
		t.Fatalf("refused request id not offered afresh: %#v %v", next, err)
	}
}

func TestCredentialOfferTravelsThroughAuthenticatedReportAndAck(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	v := testStorageCredentialRequest()
	v.PoolID, v.HostID = a.poolOf(host.HostID), host.HostID
	offer, err := a.c.credentials.offer(v)
	if err != nil {
		t.Fatal(err)
	}
	response := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.StorageRepositoryCapability}})
	if len(response.CredentialOffers) != 1 || response.CredentialOffers[0] != offer {
		t.Fatalf("report offers %#v", response.CredentialOffers)
	}
	var handoff protocol.StorageCredentialHandoff
	read(t, a.call("GET", "/api/v1/agent/storage/credentials/"+offer.Handle, nil, cert, 200), &handoff)
	if handoff.Offer != offer || handoff.Credentials != v.Credentials {
		t.Fatalf("handoff %#v", handoff)
	}
	receipt := protocol.StorageCredentialReceipt{Receipt: "receipt-1", RequestID: v.RequestID, RegistrationRequestID: v.RegistrationRequestID, PoolID: v.PoolID, HostID: v.HostID, Status: "ready"}
	a.call("POST", "/api/v1/agent/storage/credentials/"+offer.Handle+"/ack", receipt, cert, 200)
	if got := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: []string{protocol.StorageRepositoryCapability}}); len(got.CredentialOffers) != 0 {
		t.Fatalf("acknowledged offer replayed %#v", got.CredentialOffers)
	}
}

func TestCredentialStatusRecoversCoreReportedReceipt(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	receipt := protocol.StorageCredentialReceipt{Receipt: "receipt-1", RequestID: "credential-1", RegistrationRequestID: "storage-1", PoolID: pool, HostID: host.HostID, Status: "ready", RetireEligible: true}
	a.report(certificate(t, host.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{CredentialReceipts: []protocol.StorageCredentialReceipt{receipt}}}})
	var got protocol.StorageCredentialReceipt
	read(t, a.call("GET", "/api/v1/storage/credentials/credential-1?pool_id="+pool+"&host_id="+host.HostID+"&registration_request_id=storage-1", nil, nil, 200), &got)
	if got != receipt {
		t.Fatalf("recovered receipt %#v", got)
	}
	var receipts []protocol.StorageCredentialReceipt
	read(t, a.call("GET", "/api/v1/storage/credentials?pool_id="+pool+"&host_id="+host.HostID, nil, nil, 200), &receipts)
	if len(receipts) != 1 || receipts[0] != receipt {
		t.Fatalf("eligible receipts %#v", receipts)
	}
}

func TestCredentialReceiptObservationReplacesOnlyReportingHost(t *testing.T) {
	b := newCredentialBroker()
	first := protocol.StorageCredentialReceipt{Receipt: "receipt-1", RequestID: "request-1", PoolID: "pool-1", HostID: "host-1", Status: "ready", RetireEligible: true}
	other := protocol.StorageCredentialReceipt{Receipt: "receipt-2", RequestID: "request-2", PoolID: "pool-2", HostID: "host-2", Status: "ready", RetireEligible: true}
	b.observeReceipts("host-1", []protocol.StorageCredentialReceipt{first})
	b.observeReceipts("host-2", []protocol.StorageCredentialReceipt{other})
	if got, ok := b.eligibleReceipts("pool-1", "host-1"); !ok || len(got) != 1 {
		t.Fatalf("initial eligible receipts %#v %v", got, ok)
	}
	retired := first
	retired.Status, retired.RetireEligible = "retired", false
	b.observeReceipts("host-1", []protocol.StorageCredentialReceipt{retired})
	if got, ok := b.eligibleReceipts("pool-1", "host-1"); !ok || len(got) != 0 {
		t.Fatalf("retired receipt remained eligible %#v %v", got, ok)
	}
	if got, ok := b.receipt("host-1", "request-1", "pool-1", "", ""); !ok || got.Status != "retired" {
		t.Fatalf("retired receipt %#v %v", got, ok)
	}
	b.observeReceipts("host-1", nil)
	if _, ok := b.receipt("host-1", "request-1", "pool-1", "", ""); ok {
		t.Fatal("empty Core report retained stale receipt")
	}
	if got, ok := b.eligibleReceipts("pool-2", "host-2"); !ok || len(got) != 1 || got[0] != other {
		t.Fatalf("other host receipt was changed %#v %v", got, ok)
	}
}

func TestCredentialReceiptMirrorBoundIsPerHostAndKeepsLiveReceipts(t *testing.T) {
	b := newCredentialBroker()
	receipts := func(host string, n int, status string) []protocol.StorageCredentialReceipt {
		out := make([]protocol.StorageCredentialReceipt, n)
		for i := range out {
			out[i] = protocol.StorageCredentialReceipt{Receipt: fmt.Sprintf("%s-%s-%d", host, status, i), RequestID: fmt.Sprintf("%s-%d", status, i), PoolID: "pool-1", HostID: host, Status: status}
		}
		return out
	}
	b.observeReceipts("host-1", receipts("host-1", 1024, "ready"))
	live := protocol.StorageCredentialReceipt{Receipt: "live", RequestID: "live-request", PoolID: "pool-1", HostID: "host-2", Status: "ready"}
	b.observeReceipts("host-2", append(receipts("host-2", 1100, "retired"), live))
	if _, ok := b.receipt("host-2", "live-request", "pool-1", "", ""); !ok {
		t.Fatal("tombstones or another host's receipts crowded out a live receipt")
	}
	if _, ok := b.receipt("host-1", "ready-1023", "pool-1", "", ""); !ok {
		t.Fatal("another host's report evicted this host's receipts")
	}
}

// The credential receipt is write-only: a registration response must not echo it.
func TestStorageRegistrationResponseOmitsCredentialReceipt(t *testing.T) {
	s := protocol.StorageRegistration{CredentialReceipt: "receipt-1", CredentialRequestID: "request-1"}
	b, err := json.Marshal(resourceResponse(protocol.Command{Storage: &s}, protocol.Operation{Status: "queued"}))
	if err != nil || strings.Contains(string(b), "credential_") {
		t.Fatalf("response echoes the receipt: %s, %v", b, err)
	}
}

func TestDeliveredCredentialOfferIsNotOfferedAgain(t *testing.T) {
	b := newCredentialBroker()
	offer, err := b.offer(storageCredentialRequest{RequestID: "req-1", RegistrationRequestID: "reg-1", PoolID: "pool", HostID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.offers("host"); len(got) != 1 {
		t.Fatalf("fresh offer not listed: %v", got)
	}
	if _, err := b.redeem(offer.Handle, "host"); err != nil {
		t.Fatal(err)
	}
	if got := b.offers("host"); len(got) != 0 {
		t.Fatalf("redeemed offer listed again, so an unacknowledged handoff is retried and refused every report: %v", got)
	}
}
