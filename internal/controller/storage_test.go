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

func TestZFSProvisionUsesMasterCommandAndReplays(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	cert := certificate(t, master.CertificatePEM)
	caps := []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.StorageDiscoveryCapability, protocol.ZFSProvisionCapability}
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	body := zfsProvisionAPIRequest{RequestID: "zfs-create-1", Name: "Fast disks", PoolID: pool, Scope: "host", Hosts: []protocol.ZFSProvisionHost{{HostID: master.HostID, Layout: "single", Devices: []protocol.ZFSProvisionDevice{{ByID: "/dev/disk/by-id/scsi-test", Serial: "test-serial", Fingerprint: strings.Repeat("a", 64), SizeBytes: 1 << 30}}}}}
	var queued map[string]any
	read(t, a.call(http.MethodPost, "/api/v1/storage/zfs/provision", body, nil, http.StatusAccepted), &queued)
	if queued["repository_id"] != nil || queued["name"] != body.Name {
		t.Fatalf("Controller exposed or allocated a repository ID: %#v", queued)
	}
	commands := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: caps})
	if len(commands.Commands) != 1 || commands.Commands[0].Kind != "storage-zfs-provision" || commands.Commands[0].ZFSProvision == nil || commands.Commands[0].ZFSProvision.PoolID != pool || commands.Commands[0].ZFSProvision.Hosts[0].HostID != master.HostID {
		t.Fatalf("ZFS provision was not sent as a master command: %#v", commands.Commands)
	}
	firstOperationID := commands.Commands[0].OperationID
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", body, nil, http.StatusOK)
	configure := body
	configure.RequestID, configure.RepositoryID = "zfs-configure-host", "core-repository-1"
	var configureQueued map[string]any
	read(t, a.call(http.MethodPost, "/api/v1/storage/zfs/provision", configure, nil, http.StatusAccepted), &configureQueued)
	if configureQueued["repository_id"] != configure.RepositoryID {
		t.Fatalf("Core-owned repository ID was not retained for host configuration: %#v", configureQueued)
	}
	commands = a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 3, Capabilities: caps, Results: []protocol.Result{{OperationID: firstOperationID, Status: "succeeded"}}})
	if len(commands.Commands) != 1 || commands.Commands[0].ZFSProvision == nil || commands.Commands[0].ZFSProvision.RepositoryID != configure.RepositoryID {
		t.Fatalf("host configuration lost Core repository ID: %#v", commands.Commands)
	}
	bad := body
	bad.RequestID = "zfs-bad"
	bad.Hosts[0].Devices[0].SizeBytes = 0
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", bad, nil, http.StatusBadRequest)
}

func TestZFSProvisionRecoveryCompletesOriginalAttentionOperation(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	cert := certificate(t, master.CertificatePEM)
	caps := []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.ZFSProvisionCapability}
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	body := zfsProvisionAPIRequest{RequestID: "zfs-recover-original", Name: "Recovered disks", PoolID: pool, Scope: "host", Hosts: []protocol.ZFSProvisionHost{{HostID: master.HostID, Layout: "single", Devices: []protocol.ZFSProvisionDevice{{ByID: "/dev/disk/by-id/scsi-recover", Serial: "recover-serial", Fingerprint: strings.Repeat("c", 64), SizeBytes: 1 << 30}}}}}
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", body, nil, http.StatusAccepted)
	first := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: caps})
	if len(first.Commands) != 1 {
		t.Fatalf("ZFS command was not dispatched: %#v", first.Commands)
	}
	opID := first.Commands[0].OperationID
	attention := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 3, Capabilities: caps, Results: []protocol.Result{{OperationID: opID, Status: "needs_attention", Error: "interrupted before registration"}}})
	if len(attention.AcknowledgedResults) != 1 || attention.AcknowledgedResults[0] != opID {
		t.Fatalf("attention result was not acknowledged: %#v", attention)
	}
	// The later report represents Core's durable recovery after the Controller was
	// disconnected. It supplies the original operation result; no storage inventory
	// is used to infer success.
	recovered := a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 4, Capabilities: caps, Results: []protocol.Result{{OperationID: opID, Status: "succeeded"}}})
	if len(recovered.AcknowledgedResults) != 1 || recovered.AcknowledgedResults[0] != opID {
		t.Fatalf("recovered result was not acknowledged: %#v", recovered)
	}
	var operations []protocol.Operation
	read(t, a.call(http.MethodGet, "/api/v1/operations", nil, nil, http.StatusOK), &operations)
	for _, operation := range operations {
		if operation.ID == opID {
			if operation.Status != "succeeded" || operation.Error != "" {
				t.Fatalf("Core recovery did not settle original operation: %#v", operation)
			}
			return
		}
	}
	t.Fatalf("original ZFS operation %q was not retained", opID)
}

func TestZFSProvisionRequiresCurrentCoreCapability(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.StorageDiscoveryCapability}})
	body := zfsProvisionAPIRequest{RequestID: "zfs-old-core", Name: "Fast disks", PoolID: pool, Scope: "host", Hosts: []protocol.ZFSProvisionHost{{HostID: host.HostID, Layout: "single", Devices: []protocol.ZFSProvisionDevice{{ByID: "/dev/disk/by-id/scsi-test", Serial: "test-serial", Fingerprint: strings.Repeat("a", 64), SizeBytes: 1 << 30}}}}}
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", body, nil, http.StatusConflict)
	a.call(http.MethodPost, "/api/v1/storage/discover", protocol.StorageDiscoveryRequest{RequestID: "zfs-discover-old", PoolID: pool, HostID: host.HostID, Kind: "zfs-device"}, nil, http.StatusConflict)
}

func TestZFSProvisionRefusesSparesAndWidenedLayoutsWithoutReplaceCapability(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(host.HostID)
	cert := certificate(t, host.CertificatePEM)
	caps := []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.StorageDiscoveryCapability, protocol.ZFSProvisionCapability}
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	device := func(name string) protocol.ZFSProvisionDevice {
		return protocol.ZFSProvisionDevice{ByID: "/dev/disk/by-id/" + name, Serial: name, Fingerprint: strings.Repeat("a", 64), SizeBytes: 1 << 30}
	}
	spare := zfsProvisionAPIRequest{RequestID: "zfs-spare-old", Name: "With spare", PoolID: pool, Scope: "host", Hosts: []protocol.ZFSProvisionHost{{
		HostID: host.HostID, Layout: "mirror", Devices: []protocol.ZFSProvisionDevice{device("d1"), device("d2")}, Spares: []protocol.ZFSProvisionDevice{device("s1")},
	}}}
	if body := a.call(http.MethodPost, "/api/v1/storage/zfs/provision", spare, nil, http.StatusConflict).Body.String(); !strings.Contains(body, protocol.ZFSReplaceCapability) {
		t.Fatalf("spare provision without replace capability: %s", body)
	}
	three := zfsProvisionAPIRequest{RequestID: "zfs-mirror3-old", Name: "Three mirror", PoolID: pool, Scope: "host", Hosts: []protocol.ZFSProvisionHost{{
		HostID: host.HostID, Layout: "mirror", Devices: []protocol.ZFSProvisionDevice{device("d1"), device("d2"), device("d3")},
	}}}
	if body := a.call(http.MethodPost, "/api/v1/storage/zfs/provision", three, nil, http.StatusConflict).Body.String(); !strings.Contains(body, protocol.ZFSReplaceCapability) {
		t.Fatalf("three-disk mirror without replace capability: %s", body)
	}
	withReplace := append(append([]string{}, caps...), protocol.ZFSReplaceCapability)
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: withReplace})
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", spare, nil, http.StatusAccepted)
}

func TestZFSProvisionInventoryRemainsCoreOwnedPerHostReadModel(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	cert := certificate(t, master.CertificatePEM)
	results := []protocol.ZFSProvisionHostResult{
		{RepositoryID: "core-repository-1", PoolID: pool, Name: "Fast disks", Scope: "pool", HostID: master.HostID, Layout: "mirror", Devices: []protocol.ZFSProvisionDevice{{ByID: "/dev/disk/by-id/master-a", Serial: "master-a", Fingerprint: strings.Repeat("a", 64), SizeBytes: 1 << 30}}, Status: "succeeded", PoolGUID: "pool-guid-a", DatasetGUID: "dataset-guid-a"},
		{RepositoryID: "core-repository-1", PoolID: pool, Name: "Fast disks", Scope: "pool", HostID: "member-core-1", Layout: "single", Devices: []protocol.ZFSProvisionDevice{{ByID: "/dev/disk/by-id/member-a", Serial: "member-a", Fingerprint: strings.Repeat("b", 64), SizeBytes: 1 << 30}}, Status: "needs_attention", Error: "member unreachable"},
	}
	a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.ZFSProvisionCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{ZFSProvisions: results}}})
	for _, host := range a.hosts() {
		if host.ID != master.HostID {
			continue
		}
		if host.Inventory.Managed == nil || len(host.Inventory.Managed.ZFSProvisions) != len(results) || host.Inventory.Managed.ZFSProvisions[0].HostID != master.HostID || host.Inventory.Managed.ZFSProvisions[1].HostID != "member-core-1" {
			t.Fatalf("Controller did not preserve Core's per-host ZFS read model: %+v", host.Inventory.Managed)
		}
		return
	}
	t.Fatal("master host missing")
}

func TestMemberPoolLocalZFSReportCannotOverwriteMasterRepository(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	const member = "zfs-member-1"
	if _, err := a.c.db.Exec("INSERT INTO hosts(id,pool_id,hostname) VALUES(?,?,?)", member, pool, "member"); err != nil {
		t.Fatal(err)
	}
	membership := protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}, {HostID: member, Hostname: member, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"}}}
	observed := time.Now().UTC()
	masterStorage := protocol.StorageRegistration{ID: "core-zfs-pool", PoolID: pool, HostID: master.HostID, Name: "Pool local ZFS", Path: "/var/lib/nex-storage/core-zfs-pool", Kind: protocol.StorageKindZFS, Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "nex-core-zfs-pool", Dataset: "vms", MountPath: "/var/lib/nex-storage/core-zfs-pool"}}, ZFSProvisioned: true, ZFSScope: "pool", Capabilities: protocol.StorageCapabilities{VMDisk: true, Shared: false}, Access: protocol.StorageAccess{Status: "healthy", Readable: true, Writable: true}, ZFSHostAccess: []protocol.ZFSHostAccess{{HostID: master.HostID, Path: "/var/lib/nex-storage/core-zfs-pool", Readable: true, Writable: true, TotalBytes: 1 << 30, ObservedAt: observed}, {HostID: member, Path: "/var/lib/nex-storage/member-local", Readable: true, Writable: true, TotalBytes: 2 << 30, ObservedAt: observed}}}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{masterStorage}}}})
	memberStorage := masterStorage
	memberStorage.HostID, memberStorage.Path = member, "/var/lib/nex-storage/member-local"
	memberStorage.Access.TotalBytes = 9 << 30
	tx, err := a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.c.adoptManaged(tx, member, &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{memberStorage}}}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var repositories []protocol.StorageRegistration
	read(t, a.call(http.MethodGet, "/api/v1/storage", nil, nil, http.StatusOK), &repositories)
	if len(repositories) != 1 || repositories[0].HostID != master.HostID || repositories[0].Path != masterStorage.Path || len(repositories[0].ZFSHostAccess) != 2 || repositories[0].Capabilities.Shared {
		t.Fatalf("member ZFS report overwrote the master projection: %+v", repositories)
	}
	tx, err = a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = a.c.readyStorageForHost(tx, masterStorage.ID, pool, member); err != nil {
		t.Fatalf("fresh master-reported local ZFS access was unusable on member: %v", err)
	}
}

func TestHostScopeZFSUsesMasterProjectionAndTargetAccess(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	const member = "zfs-member-host-scope"
	if _, err := a.c.db.Exec("INSERT INTO hosts(id,pool_id,hostname) VALUES(?,?,?)", member, pool, "member"); err != nil {
		t.Fatal(err)
	}
	membership := protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}, {HostID: member, Hostname: member, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"}}}
	storage := protocol.StorageRegistration{ID: "core-zfs-host", PoolID: pool, HostID: master.HostID, Name: "Member ZFS", Path: "/var/lib/nex-storage/core-zfs-host", Kind: protocol.StorageKindZFS, Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "nex-core-zfs-host", Dataset: "vms", MountPath: "/var/lib/nex-storage/core-zfs-host"}}, ZFSProvisioned: true, ZFSScope: "host", Capabilities: protocol.StorageCapabilities{VMDisk: true}, ZFSHostAccess: []protocol.ZFSHostAccess{{HostID: master.HostID, Error: "local ZFS repository is not observed", ObservedAt: time.Now().UTC()}, {HostID: member, Path: "/var/lib/nex-storage/member-zfs", Readable: true, Writable: true, ObservedAt: time.Now().UTC()}}}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{storage}}}})
	memberStorage := storage
	memberStorage.HostID, memberStorage.Path = member, "/var/lib/nex-storage/member-zfs"
	tx, err := a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.c.adoptManaged(tx, member, &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{memberStorage}}}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var repositories []protocol.StorageRegistration
	read(t, a.call(http.MethodGet, "/api/v1/storage", nil, nil, http.StatusOK), &repositories)
	if len(repositories) != 1 || repositories[0].HostID != master.HostID || repositories[0].Path != storage.Path {
		t.Fatalf("member host-scope report overwrote master projection: %+v", repositories)
	}
	tx, err = a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, master.HostID); err == nil {
		t.Fatal("master without a native ZFS access observation was accepted")
	}
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, member); err != nil {
		t.Fatalf("member with a fresh native ZFS access observation was rejected: %v", err)
	}
}

func TestCoreStorageReportRepairsOrphanAdoptionProjection(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	membership := protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	storage := protocol.StorageRegistration{ID: "zfs-orphan-repair", PoolID: pool, HostID: master.HostID, Name: "Repair ZFS", Path: "/var/lib/nex-storage/zfs-orphan-repair", Kind: protocol.StorageKindZFS, Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "nex-orphan-repair", Dataset: "vms", MountPath: "/var/lib/nex-storage/zfs-orphan-repair"}}, ZFSProvisioned: true, ZFSScope: "pool", Capabilities: protocol.StorageCapabilities{VMDisk: true}, ZFSHostAccess: []protocol.ZFSHostAccess{{HostID: master.HostID, Path: "/var/lib/nex-storage/zfs-orphan-repair", Readable: true, Writable: true, ObservedAt: time.Now().UTC()}}}
	report := func(sequence uint64) {
		a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: sequence, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{storage}}, Checks: []protocol.Check{{Name: "managed-journal", Status: "healthy", ObservedAt: time.Now().UTC()}}}})
	}
	report(1)
	var originalOperation string
	if err := a.c.db.QueryRow("SELECT operation_id FROM managed_resources WHERE id=?", storage.ID).Scan(&originalOperation); err != nil || originalOperation == "" {
		t.Fatalf("initial Core adoption missing: %q %v", originalOperation, err)
	}
	if _, err := a.c.db.Exec("DELETE FROM managed_resources WHERE id=?", storage.ID); err != nil {
		t.Fatal(err)
	}
	report(2)
	var restoredOperation string
	if err := a.c.db.QueryRow("SELECT operation_id FROM managed_resources WHERE id=?", storage.ID).Scan(&restoredOperation); err != nil || restoredOperation != originalOperation {
		t.Fatalf("orphan adoption was not reconnected to its original operation: got=%q want=%q err=%v", restoredOperation, originalOperation, err)
	}
}

func TestMasterReportMarksStaleZFSProjectionUnusableUntilCoreReregisters(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	membership := protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	storage := protocol.StorageRegistration{ID: "zfs-failed-create", PoolID: pool, HostID: master.HostID, Name: "Failed ZFS", Path: "/var/lib/nex-storage/zfs-failed-create", Kind: protocol.StorageKindZFS, Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "nex-failed", Dataset: "vms", MountPath: "/var/lib/nex-storage/zfs-failed-create"}}, ZFSProvisioned: true, ZFSScope: "pool", Capabilities: protocol.StorageCapabilities{VMDisk: true}}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{storage}}}})
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{
		Managed: &protocol.ManagedState{Membership: &membership, ZFSProvisions: []protocol.ZFSProvisionHostResult{{RepositoryID: storage.ID, PoolID: pool, Name: storage.Name, Scope: "pool", HostID: master.HostID, Status: "needs_attention", Error: "managed journal is unavailable"}}},
		Checks:  []protocol.Check{{Name: "managed-journal", Status: "failed", ObservedAt: time.Now().UTC(), Error: "managed journal is unreadable"}},
	}})
	var repositories []protocol.StorageRegistration
	read(t, a.call(http.MethodGet, "/api/v1/storage", nil, nil, http.StatusOK), &repositories)
	if len(repositories) != 1 {
		t.Fatalf("unreadable Core journal erased a prior ZFS projection: %+v", repositories)
	}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 3, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{
		Managed: &protocol.ManagedState{Membership: &membership, ZFSProvisions: []protocol.ZFSProvisionHostResult{{RepositoryID: storage.ID, PoolID: pool, Name: storage.Name, Scope: "pool", HostID: master.HostID, Status: "needs_attention", Error: "registration unavailable"}}},
		Checks:  []protocol.Check{{Name: "managed-journal", Status: "healthy", ObservedAt: time.Now().UTC()}},
	}})
	read(t, a.call(http.MethodGet, "/api/v1/storage", nil, nil, http.StatusOK), &repositories)
	if len(repositories) != 1 || repositories[0].Status != "needs_attention" || repositories[0].Capabilities.VMDisk || repositories[0].Access.Writable || len(repositories[0].ZFSHostAccess) != 0 {
		t.Fatalf("stale ZFS mirror remained eligible after Core stopped reporting storage: %+v", repositories)
	}
	priorOperation := repositories[0].OperationID
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 4, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{
		Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{storage}},
		Checks:  []protocol.Check{{Name: "managed-journal", Status: "healthy", ObservedAt: time.Now().UTC()}},
	}})
	read(t, a.call(http.MethodGet, "/api/v1/storage", nil, nil, http.StatusOK), &repositories)
	if len(repositories) != 1 || repositories[0].Status != "succeeded" || !repositories[0].Capabilities.VMDisk || repositories[0].OperationID != priorOperation {
		t.Fatalf("Core re-registration did not restore the retained ZFS projection: %+v", repositories)
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

// ZFS provisioning is an administrator mutation over selected disks: it needs a session and
// CSRF token, and every selected host must be one Core can actually do the work on.
func TestZFSProvisionRefusesUnauthorizedAndUnusableSelectedHosts(t *testing.T) {
	a := newClient(t)
	site := a.site()
	master := a.enroll(a.token(site, ""), csr(t))
	pool := a.poolOf(master.HostID)
	caps := []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability, protocol.ZFSProvisionCapability}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	host := func(id string) protocol.ZFSProvisionHost {
		return protocol.ZFSProvisionHost{HostID: id, Layout: "single", Devices: []protocol.ZFSProvisionDevice{{ByID: "/dev/disk/by-id/scsi-auth", Serial: "auth-serial", Fingerprint: strings.Repeat("d", 64), SizeBytes: 1 << 30}}}
	}
	body := zfsProvisionAPIRequest{RequestID: "zfs-auth", Name: "Fast disks", PoolID: pool, Scope: "host", Hosts: []protocol.ZFSProvisionHost{host(master.HostID)}}
	cookie, csrf := a.cookie, a.csrf
	a.cookie = nil
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", body, nil, http.StatusUnauthorized)
	a.cookie, a.csrf = cookie, ""
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", body, nil, http.StatusForbidden)
	a.csrf = csrf
	// A member of this pool that Core has not reported is neither fresh nor known to
	// support provisioning: refuse the request instead of queueing work for it.
	const member = "zfs-unreported-member"
	if _, err := a.c.db.Exec("INSERT INTO hosts(id,pool_id,hostname) VALUES(?,?,?)", member, pool, "member"); err != nil {
		t.Fatal(err)
	}
	stale := body
	stale.RequestID, stale.Scope, stale.Hosts = "zfs-stale-member", "pool", []protocol.ZFSProvisionHost{host(master.HostID), host(member)}
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", stale, nil, http.StatusConflict)
	// A host of another pool is never a selectable target of this pool's request.
	foreign := a.enroll(a.token(site, ""), csr(t))
	a.report(certificate(t, foreign.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps})
	crossPool := body
	crossPool.RequestID, crossPool.Scope, crossPool.Hosts = "zfs-cross-pool", "pool", []protocol.ZFSProvisionHost{host(master.HostID), host(foreign.HostID)}
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", crossPool, nil, http.StatusConflict)
	// The request that names only usable hosts still reaches Core.
	a.call(http.MethodPost, "/api/v1/storage/zfs/provision", body, nil, http.StatusAccepted)
}
