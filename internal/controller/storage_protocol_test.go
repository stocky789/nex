// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

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
	if v2.StorageClusters == nil || v2.StorageClusters.Capability != protocol.StorageClusterCapability {
		t.Fatalf("missing clustered-storage v2 preflight: %+v", v2.StorageClusters)
	}
	// A strict older Core still receives the unchanged support object: the
	// extension is negotiated by capability value, never by an added response
	// field that its decoder could reject.
	type oldSupport struct {
		Capability string `json:"capability"`
	}
	type oldResponse struct {
		Commands               any         `json:"commands"`
		AcknowledgedResults    any         `json:"acknowledged_results"`
		Consoles               any         `json:"consoles,omitempty"`
		CredentialOffers       any         `json:"credential_offers,omitempty"`
		StorageRepositories    any         `json:"storage_repositories,omitempty"`
		StorageClusters        *oldSupport `json:"storage_clusters_support,omitempty"`
		StorageDiscoveries     any         `json:"storage_discoveries,omitempty"`
		StorageClusterRequests any         `json:"storage_clusters,omitempty"`
	}
	raw, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	var old oldResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&old); err != nil || old.StorageClusters == nil || old.StorageClusters.Capability != protocol.StorageClusterCapability {
		t.Fatalf("strict older Core could not decode base acknowledgement: %#v %v", old, err)
	}
	monitor := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 3, Capabilities: []string{protocol.StorageClusterSessionMonitorCapability}})
	if monitor.StorageClusters == nil || monitor.StorageClusters.Capability != protocol.StorageClusterSessionMonitorCapability {
		t.Fatalf("missing explicit session-monitor acknowledgement: %+v", monitor.StorageClusters)
	}
}

func TestZFSCoreReceivesCapabilityAcknowledgementWithoutNewResponseFields(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	response := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ZFSProvisionCapability}})
	if response.StorageRepositories == nil || response.StorageRepositories.ProtocolVersion != protocol.Version || response.StorageRepositories.Capability != protocol.ZFSProvisionCapability {
		t.Fatalf("missing ZFS capability acknowledgement: %+v", response.StorageRepositories)
	}
	// The support object predates ZFS. Its shape remains unchanged, so a strict Core
	// can decode it while capability value selects the feature it understands.
	type oldSupport struct {
		ProtocolVersion int    `json:"protocol_version"`
		Capability      string `json:"capability"`
	}
	raw, err := json.Marshal(response.StorageRepositories)
	if err != nil {
		t.Fatal(err)
	}
	var old oldSupport
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&old); err != nil {
		t.Fatal(err)
	}
	if old.Capability != protocol.ZFSProvisionCapability || old.ProtocolVersion != protocol.Version {
		t.Fatalf("ZFS support object changed shape: %+v", old)
	}
	replace := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: []string{protocol.ZFSProvisionCapability, protocol.ZFSReplaceCapability}})
	if replace.StorageRepositories == nil || replace.StorageRepositories.Capability != protocol.ZFSReplaceCapability {
		t.Fatalf("missing ZFS replace capability acknowledgement: %+v", replace.StorageRepositories)
	}
}

func TestClusteredStorageV2AcknowledgementDispatchesAndAcceptsProofRequestID(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	// The first v2 report receives only the explicit support acknowledgement.
	preflight := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.StorageClusterSessionMonitorCapability}})
	if preflight.StorageClusters == nil || preflight.StorageClusters.Capability != protocol.StorageClusterSessionMonitorCapability {
		t.Fatalf("missing clustered storage support: %+v", preflight.StorageClusters)
	}
	request := protocol.StorageClusterRequest{RequestID: "cluster-v2-1", PoolID: host.PoolID, HostID: host.HostID, Action: "status"}
	result := make(chan protocol.StorageClusterResult, 1)
	b := a.c.clusterBroker()
	b.mu.Lock()
	b.pending[request.RequestID] = storageClusterPending{request: request, result: result}
	b.mu.Unlock()
	response := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: []string{protocol.StorageClusterCapability, protocol.StorageClusterSessionMonitorCapability}})
	if len(response.StorageClusterRequests) != 1 || response.StorageClusterRequests[0].RequestID != request.RequestID {
		t.Fatalf("v2 cluster request was not dispatched: %+v", response.StorageClusterRequests)
	}
	proof := protocol.StorageClusterFenceStatus{RequestID: "fence-v2-1", HostID: host.HostID, Status: "passed", ExclusionVerified: true, ObservedAt: time.Now().UTC(), ConfigGeneration: 1, ConfigDigest: "digest"}
	config := protocol.StorageClusterConfig{ClusterID: "cluster-1", PoolID: host.PoolID, RepositoryID: "repository-1", Generation: 1, Nodes: []protocol.StorageClusterNode{{HostID: host.HostID}}}
	agentResult := protocol.StorageClusterResult{
		RequestID: request.RequestID, PoolID: host.PoolID, HostID: host.HostID, Status: "accepted", ObservedAt: time.Now().UTC(),
		Operations: []protocol.StorageClusterOperation{{
			RequestID: request.RequestID, MembershipGeneration: 1, Config: config, Status: "verifying_fence",
			Members: []protocol.StorageClusterMemberReport{{
				HostID: host.HostID, Generation: 1, MembershipGeneration: 1, ClusterID: "cluster-1",
				ConfigDigest: config.Digest(),
				Stage:        "health", UpdatedAt: time.Now().UTC(),
				Status: protocol.StorageClusterStatus{Fences: []protocol.StorageClusterFenceStatus{proof}},
			}},
		}},
	}
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 3, Capabilities: []string{protocol.StorageClusterCapability, protocol.StorageClusterSessionMonitorCapability}, StorageClusters: []protocol.StorageClusterResult{agentResult}})
	select {
	case got := <-result:
		if got.Operations[0].Members[0].HostID != host.HostID || got.Operations[0].Members[0].Status.Fences[0].RequestID != proof.RequestID {
			t.Fatalf("proof request ID was not retained: %+v", got)
		}
	default:
		t.Fatal("valid v2 proof result was not completed")
	}
}

func TestClusteredStorageSessionMonitorReportsRequireAcknowledgementCapability(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	config := protocol.StorageClusterConfig{ClusterID: "cluster-1", PoolID: host.PoolID, RepositoryID: "repository-1", Generation: 1, Nodes: []protocol.StorageClusterNode{{HostID: host.HostID}}}
	result := protocol.StorageClusterResult{RequestID: "cluster-monitor-1", PoolID: host.PoolID, HostID: host.HostID, Status: "accepted", ObservedAt: time.Now().UTC(), Operations: []protocol.StorageClusterOperation{{
		RequestID: "cluster-monitor-1", MembershipGeneration: 1, Config: config, Status: "applying", UpdatedAt: time.Now().UTC(), Members: []protocol.StorageClusterMemberReport{{
			HostID: host.HostID, Generation: 1, MembershipGeneration: 1, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "monitor", UpdatedAt: time.Now().UTC(),
			Status: protocol.StorageClusterStatus{ClusterID: config.ClusterID, RepositoryID: config.RepositoryID, SessionMonitorReady: true, Status: "pending"},
		}},
	}}}
	// A Core cannot merely send the new member field; the Controller must have
	// explicitly acknowledged the extension first.
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: protocol.Version, Sequence: 1, StorageClusters: []protocol.StorageClusterResult{result}}, cert, 400)
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.StorageClusterSessionMonitorCapability}, StorageClusters: []protocol.StorageClusterResult{result}})
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
