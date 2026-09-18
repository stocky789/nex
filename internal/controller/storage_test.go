// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"net/http"
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestStorageRepositoryIsCoreAllocatedAndAdopted(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	report := func(sequence uint64, storage []protocol.StorageRegistration, outcomes []protocol.ManagedOutcome, results ...protocol.Result) protocol.ReportResponse {
		return a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: sequence,
			Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability},
			Inventory:    &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Storage: storage, Outcomes: outcomes}}, Results: results})
	}
	report(1, nil, nil)
	req := storageRegisterRequest{RequestID: "nfs-register", PoolID: pool, HostID: host.HostID, Name: "NFS", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}}}
	var pending protocol.StorageRegistration
	read(t, a.call("POST", "/api/v1/storage", req, nil, http.StatusCreated), &pending)
	if pending.ID != "" || pending.OperationID == "" || pending.Status != "queued" {
		t.Fatalf("Controller allocated repository identity: %+v", pending)
	}
	commands := report(2, nil, nil)
	if len(commands.Commands) != 1 || commands.Commands[0].Storage == nil || commands.Commands[0].Storage.ID != "" {
		t.Fatalf("register command %#v", commands.Commands)
	}
	// Applied storage deliberately clears its operation fields; Core's retained outcome binds
	// the allocated repository ID to the pending Controller operation.
	repository := protocol.StorageRegistration{ID: "repository-1", Name: "NFS", Path: "/var/lib/nex-storage/repository-1", Kind: protocol.StorageKindNFS, Source: req.Source, Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/vms"}}, Generation: 1, Capabilities: protocol.StorageCapabilities{VMDisk: true, ISO: true}, Access: protocol.StorageAccess{Status: "ready", Readable: true, Writable: true}}
	report(3, []protocol.StorageRegistration{repository}, []protocol.ManagedOutcome{{OperationID: pending.OperationID, Kind: "storage-register", ID: repository.ID, Name: repository.Name, Status: "succeeded"}}, protocol.Result{OperationID: pending.OperationID, Status: "succeeded"})
	var replay protocol.StorageRegistration
	read(t, a.call("POST", "/api/v1/storage", req, nil, http.StatusOK), &replay)
	if replay.ID != repository.ID || replay.OperationID != pending.OperationID {
		t.Fatalf("original registration did not replay after adoption: %+v", replay)
	}
	var repositories []protocol.StorageRegistration
	read(t, a.call("GET", "/api/v1/storage", nil, nil, http.StatusOK), &repositories)
	if len(repositories) != 1 || repositories[0].ID != repository.ID || repositories[0].Path != repository.Path || repositories[0].Generation != 1 || repositories[0].Identity.NFS == nil || !repositories[0].Access.Writable {
		t.Fatalf("Core storage projection %+v", repositories)
	}
	if err := storageReadyForWrite(protocol.Command{Storage: &repositories[0]}); err != nil {
		t.Fatalf("ready repository rejected: %v", err)
	}
	repositories[0].Access.Writable = false
	if err := storageReadyForWrite(protocol.Command{Storage: &repositories[0]}); err == nil {
		t.Fatal("unwritable repository accepted for VM disks")
	}

	// The Controller cannot choose a repository ID, even if a client tries to supply one.
	a.call("POST", "/api/v1/storage", map[string]any{"request_id": "caller-id", "pool_id": pool, "host_id": host.HostID, "name": "bad", "id": "caller-owned", "kind": "nfs", "source": req.Source}, nil, http.StatusBadRequest)

	var lifecycle map[string]any
	read(t, a.call("POST", "/api/v1/storage/"+repository.ID+"/recheck", storageLifecycleAPIRequest{RequestID: "nfs-recheck", PoolID: pool, HostID: host.HostID, ExpectedGeneration: 1}, nil, http.StatusAccepted), &lifecycle)
	if lifecycle["repository_id"] != repository.ID || lifecycle["action"] != "recheck" {
		t.Fatalf("lifecycle response %#v", lifecycle)
	}
}

func TestCredentialRetirementQueuesOnlyCoreCleanup(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability}})
	body := storageCredentialRetireAPIRequest{RequestID: "retire-credential", PoolID: pool, HostID: host.HostID}
	var response map[string]any
	read(t, a.call("POST", "/api/v1/storage/credentials/receipt-1/retire", body, nil, http.StatusAccepted), &response)
	if response["receipt"] != "receipt-1" || response["action"] != "retire" {
		t.Fatalf("retire response %#v", response)
	}
	commands := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability}})
	if len(commands.Commands) != 1 || commands.Commands[0].CredentialRetire == nil || commands.Commands[0].CredentialRetire.Receipt != "receipt-1" {
		t.Fatalf("credential cleanup command %#v", commands.Commands)
	}
	// Retrying the same request replays its operation instead of creating another cleanup.
	a.call("POST", "/api/v1/storage/credentials/receipt-1/retire", body, nil, http.StatusOK)
	// The URL receipt is part of the durable intent: a reused request ID cannot retarget it.
	a.call("POST", "/api/v1/storage/credentials/receipt-2/retire", body, nil, http.StatusConflict)
}

func TestStorageDiscoveryResultMustMatchOriginalHostAndPool(t *testing.T) {
	c := &Controller{}
	b := c.discoveryBroker()
	request := protocol.StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: protocol.StorageKindLocal, Credentials: &protocol.StorageCredentials{Username: "operator", Secret: "not-retained"}}
	ch := make(chan protocol.StorageDiscoveryResult, 1)
	b.pending[request.RequestID] = storageDiscoveryPending{request: request, result: ch}
	if got := c.takeStorageDiscoveries("host-1"); len(got) != 1 || got[0].RequestID != request.RequestID {
		t.Fatalf("request was not scoped for its host: %+v", got)
	} else if got[0].Credentials == nil {
		t.Fatal("outbound discovery lost credential handoff")
	}
	if retained := b.pending[request.RequestID].request.Credentials; retained != nil {
		t.Fatalf("controller retained discovery credentials after dispatch: %+v", retained)
	}
	c.completeStorageDiscoveries("host-2", []protocol.StorageDiscoveryResult{{RequestID: request.RequestID, PoolID: request.PoolID, HostID: "host-2"}})
	c.completeStorageDiscoveries("host-1", []protocol.StorageDiscoveryResult{{RequestID: request.RequestID, PoolID: "other-pool", HostID: "host-1"}})
	select {
	case <-ch:
		t.Fatal("cross-scope result accepted")
	default:
	}
	c.completeStorageDiscoveries("host-1", []protocol.StorageDiscoveryResult{{RequestID: request.RequestID, PoolID: request.PoolID, HostID: "host-1", Status: "succeeded"}})
	select {
	case got := <-ch:
		if got.Status != "succeeded" {
			t.Fatalf("valid result lost: %+v", got)
		}
	default:
		t.Fatal("valid scoped result was not delivered")
	}
}

func TestStorageClusterResultMustMatchOriginalMasterAndPool(t *testing.T) {
	c := &Controller{}
	b := c.clusterBroker()
	request := protocol.StorageClusterRequest{RequestID: "cluster-1", PoolID: "pool-1", HostID: "master-1", Action: "status"}
	result := make(chan protocol.StorageClusterResult, 1)
	b.pending[request.RequestID] = storageClusterPending{request: request, result: result}
	if got := c.takeStorageClusters("master-1"); len(got) != 1 || got[0].RequestID != request.RequestID {
		t.Fatalf("request was not scoped for its master: %+v", got)
	}
	c.completeStorageClusters("member-1", []protocol.StorageClusterResult{{RequestID: request.RequestID, PoolID: request.PoolID, HostID: "member-1", Status: "succeeded"}})
	c.completeStorageClusters("master-1", []protocol.StorageClusterResult{{RequestID: request.RequestID, PoolID: "other-pool", HostID: "master-1", Status: "succeeded"}})
	select {
	case <-result:
		t.Fatal("cross-scope clustered result accepted")
	default:
	}
	c.completeStorageClusters("master-1", []protocol.StorageClusterResult{{RequestID: request.RequestID, PoolID: request.PoolID, HostID: "master-1", Status: "succeeded"}})
	select {
	case got := <-result:
		if got.Status != "succeeded" {
			t.Fatalf("valid scoped result lost: %+v", got)
		}
	default:
		t.Fatal("valid scoped clustered result was not delivered")
	}
}

func TestStorageClusterCredentialRefsAreBoundAndBounded(t *testing.T) {
	valid := []protocol.StorageClusterCredentialRef{{RequestID: "credential-1", Kind: "chap", TargetHostID: "host-1"}}
	if !clusterCredentialRefsValid(valid) {
		t.Fatal("valid scoped credential reference rejected")
	}
	if clusterCredentialRefsValid([]protocol.StorageClusterCredentialRef{{RequestID: "credential-1", Kind: "chap", TargetHostID: "host-1"}, {RequestID: "credential-1", Kind: "fence_bmc", TargetHostID: "host-2"}}) {
		t.Fatal("reused credential request accepted")
	}
	if clusterCredentialRefsValid([]protocol.StorageClusterCredentialRef{{RequestID: "credential-2", Kind: "unknown", TargetHostID: "host-1"}}) {
		t.Fatal("unknown credential kind accepted")
	}
}

func TestStorageClusterRelayDeliversCoreLifecycleStates(t *testing.T) {
	for _, state := range []string{"queued", "awaiting_fencing", "verifying_fence", "applying", "healthy", "degraded", "succeeded", "failed", "needs_attention"} {
		t.Run(state, func(t *testing.T) {
			result := protocol.StorageClusterResult{Status: "succeeded", Operations: []protocol.StorageClusterOperation{{RequestID: "cluster-1", Status: state}}}
			if !clusterResultValid(result) {
				t.Fatalf("Core lifecycle state %q would be silently discarded", state)
			}
		})
	}
	if clusterResultValid(protocol.StorageClusterResult{Status: "succeeded", Operations: []protocol.StorageClusterOperation{{RequestID: "cluster-1", Status: "invented"}}}) {
		t.Fatal("unknown Core lifecycle state accepted")
	}
}

func TestStorageClusterRelayPassesObservedDegradedWithoutPrivateReceipt(t *testing.T) {
	// A Core report is decoded into the public protocol shape before the transient
	// broker sees it. Unknown/private fields must not survive a Controller relay.
	var result protocol.StorageClusterResult
	if err := json.Unmarshal([]byte(`{"request_id":"cluster-1","pool_id":"pool-1","host_id":"master-1","status":"succeeded","operations":[{"request_id":"cluster-1","status":"degraded","activation":"private-core-state"}]}`), &result); err != nil {
		t.Fatal(err)
	}
	if !clusterResultValid(result) {
		t.Fatalf("observed degraded result was rejected: %+v", result)
	}
	c := &Controller{}
	ch := make(chan protocol.StorageClusterResult, 1)
	c.clusterBroker().pending[result.RequestID] = storageClusterPending{request: protocol.StorageClusterRequest{RequestID: result.RequestID, PoolID: result.PoolID, HostID: result.HostID, Action: "status"}, result: ch}
	c.completeStorageClusters(result.HostID, []protocol.StorageClusterResult{result})
	select {
	case result = <-ch:
		if len(result.Operations) != 1 || result.Operations[0].Status != "degraded" {
			t.Fatalf("degraded observation was not relayed: %+v", result)
		}
	default:
		t.Fatal("degraded observation was discarded")
	}
	public, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(public) == "" || strings.Contains(string(public), "activation") || strings.Contains(string(public), "private-core-state") {
		t.Fatalf("private Core receipt leaked through relay payload: %s", public)
	}
}

func TestStorageClusterMemberReportsKeepThreeIdentitiesAndRejectMalformedReferences(t *testing.T) {
	config := protocol.StorageClusterConfig{ClusterID: "cluster-1", PoolID: "pool-1", RepositoryID: "repository-1", Generation: 7, Nodes: []protocol.StorageClusterNode{{HostID: "host-1"}, {HostID: "host-2"}, {HostID: "host-3"}}}
	member := func(host string) protocol.StorageClusterMemberReport {
		return protocol.StorageClusterMemberReport{HostID: host, Status: protocol.StorageClusterStatus{ClusterID: config.ClusterID, RepositoryID: config.RepositoryID, Status: "healthy", Healthy: true, Quorate: true, Mounted: true, FencingVerified: true}, Generation: config.Generation, MembershipGeneration: 9, ClusterID: config.ClusterID, ConfigDigest: config.Digest(), Stage: "health", UpdatedAt: time.Now().UTC()}
	}
	result := protocol.StorageClusterResult{Status: "accepted", Operations: []protocol.StorageClusterOperation{{RequestID: "cluster-request-1", MembershipGeneration: 9, Config: config, Status: "healthy", Members: []protocol.StorageClusterMemberReport{member("host-1"), member("host-2"), member("host-3")}}}}
	if !clusterResultValid(result) {
		t.Fatalf("three typed member reports rejected: %+v", result)
	}
	result.Operations[0].Members[2].HostID = "host-2"
	if clusterResultValid(result) {
		t.Fatal("duplicate member report identity accepted")
	}
	result.Operations[0].Members[2] = member("unknown-host")
	if clusterResultValid(result) {
		t.Fatal("member report outside configured nodes accepted")
	}
}

func TestFreshControllerAdoptsCoreResolvedRemoteStorage(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	storages := []protocol.StorageRegistration{
		{ID: "nfs-repository", Name: "NFS", Kind: protocol.StorageKindNFS, Path: "/var/lib/nex-storage/nfs-repository", Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}}, Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/vms", FilesystemID: "nfs-fsid"}}, Generation: 1, Capabilities: protocol.StorageCapabilities{VMDisk: true}, Access: protocol.StorageAccess{Status: "ready", Readable: true, Writable: true}},
		{ID: "smb-repository", Name: "SMB", Kind: protocol.StorageKindSMB, Path: "/var/lib/nex-storage/smb-repository", Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "vms"}}, Identity: protocol.StorageIdentity{SMB: &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "vms", FilesystemID: "smb-fsid"}}, Generation: 1, Capabilities: protocol.StorageCapabilities{VMDisk: true}, Access: protocol.StorageAccess{Status: "ready", Readable: true, Writable: true}},
		{ID: "iscsi-repository", Name: "iSCSI", Kind: protocol.StorageKindISCSI, Path: "/var/lib/nex-storage/iscsi-repository", Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "iscsi.example.test", TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef", MountPath: "/var/lib/nex-storage/iscsi-repository", ExclusiveTarget: true}}, Identity: protocol.StorageIdentity{ISCSI: &protocol.ISCSIStorageIdentity{TargetIQN: "iqn.2026-01.example:vm", WWID: "3600508b400105e210000900000490000", FilesystemUUID: "01234567-89ab-cdef-0123-456789abcdef"}}, Generation: 1, Capabilities: protocol.StorageCapabilities{VMDisk: true}, Access: protocol.StorageAccess{Status: "ready", Readable: true, Writable: true}},
	}
	a.report(certificate(t, host.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Storage: storages}}})
	var got []protocol.StorageRegistration
	read(t, a.call("GET", "/api/v1/storage", nil, nil, http.StatusOK), &got)
	if len(got) != len(storages) {
		t.Fatalf("fresh adoption lost remote repositories: %+v", got)
	}
	for _, storage := range got {
		if storage.PoolID != pool || storage.HostID != host.HostID || storage.ID == "" || storage.Path == "" || storage.Generation != 1 || !storage.Access.Writable {
			t.Fatalf("invalid adopted repository %+v", storage)
		}
	}
}
