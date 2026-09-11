// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestReportKeepsV1ShapeAndPreflightsV2Storage(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	legacy := a.report(cert, protocol.ReportRequest{Version: protocol.LegacyVersion, Sequence: 1})
	if legacy.StorageRepositories != nil {
		t.Fatal("v1 report received v2 storage fields")
	}
	v2 := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2})
	if v2.StorageRepositories == nil || v2.StorageRepositories.ProtocolVersion != protocol.Version || v2.StorageRepositories.Capability != protocol.StorageRepositoryCapability {
		t.Fatalf("missing v2 storage preflight: %+v", v2.StorageRepositories)
	}
}

func TestReportOffersCredentialsOnlyToActivatedV2Host(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	if _, err := a.c.credentials.offer(storageCredentialRequest{RequestID: "credential-1", RegistrationRequestID: "register-1", PoolID: host.PoolID, HostID: host.HostID, Credentials: protocol.StorageCredentials{Secret: "test-secret"}}); err != nil {
		t.Fatal(err)
	}
	legacy := a.report(cert, protocol.ReportRequest{Version: protocol.LegacyVersion, Sequence: 1})
	if len(legacy.CredentialOffers) != 0 {
		t.Fatal("v1 report received credential offer")
	}
	preflight := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2})
	if len(preflight.CredentialOffers) != 0 {
		t.Fatal("unactivated v2 report received credential offer")
	}
	active := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 3, Capabilities: []string{protocol.StorageRepositoryCapability}})
	if len(active.CredentialOffers) != 1 || active.CredentialOffers[0].RequestID != "credential-1" {
		t.Fatalf("credential offer missing from activated report: %+v", active.CredentialOffers)
	}
}

func TestReportAcceptsNamelessStorageTombstone(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	a.report(certificate(t, host.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Storage: []protocol.StorageRegistration{{ID: "repo-1", PoolID: host.PoolID, HostID: host.HostID, Generation: 2, Tombstone: true}}}}})
}
