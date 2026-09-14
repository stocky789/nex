// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"nex.local/nex/internal/protocol"
	"strings"
	"testing"
	"time"
)

func TestPoolRemoteStoragePlacementUsesFreshCoreMemberObservation(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	const member = "member-core"
	if _, err := a.c.db.Exec("INSERT INTO hosts(id,pool_id,hostname) VALUES(?,?,?)", member, pool, "member"); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{
		ID: "pool-nfs", PoolID: pool, HostID: master.HostID, Name: "Pool NFS", Kind: protocol.StorageKindNFS,
		Generation: 3, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/guests"}},
		Access:       protocol.StorageAccess{Status: "healthy", Readable: true, Writable: true},
		MemberAccess: []protocol.PoolStorageAccess{{RepositoryID: "pool-nfs", HostID: member, Generation: 3, Identity: "nfs-fsid", Readable: true, Writable: true, QEMUWritable: true, Locking: true, ObservedAt: time.Now().UTC()}},
	}
	command, err := json.Marshal(protocol.Command{OperationID: "pool-nfs-op", Kind: "storage-register", Storage: &storage})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.c.db.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", "pool-nfs-op", master.HostID, "storage-register", "pool-nfs-request", "succeeded", "", stamp(), stamp(), "system"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.c.db.Exec("INSERT INTO managed_resources VALUES(?,?,?,?,?,?,?,?,?)", storage.ID, "storage-register", pool, master.HostID, storage.Name, "pool-nfs-request", string(command), string(command), "pool-nfs-op"); err != nil {
		t.Fatal(err)
	}
	tx, err := a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, member); err != nil {
		t.Fatalf("fresh Core member observation was not usable: %v", err)
	}
	storage.MemberAccess[0].ObservedAt = time.Now().Add(-a.c.cfg.StaleAfter - time.Second)
	command, _ = json.Marshal(protocol.Command{OperationID: "pool-nfs-op", Kind: "storage-register", Storage: &storage})
	if _, err = tx.Exec("UPDATE managed_resources SET command=? WHERE id=?", string(command), storage.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, member); err == nil {
		t.Fatal("stale member access permitted placement")
	}
	storage.Kind, storage.MemberAccess[0].ObservedAt = protocol.StorageKindISCSI, time.Now().UTC()
	command, _ = json.Marshal(protocol.Command{OperationID: "pool-nfs-op", Kind: "storage-register", Storage: &storage})
	if _, err = tx.Exec("UPDATE managed_resources SET command=? WHERE id=?", string(command), storage.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, member); err == nil {
		t.Fatal("iSCSI member placement permitted")
	}
	storage.Source = protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2", EligibleHostIDs: []string{master.HostID, member, "member-three"}}}
	command, _ = json.Marshal(protocol.Command{OperationID: "pool-nfs-op", Kind: "storage-register", Storage: &storage})
	if _, err = tx.Exec("UPDATE managed_resources SET command=? WHERE id=?", string(command), storage.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, member); err != nil {
		t.Fatalf("fresh eligible clustered iSCSI member observation was not usable: %v", err)
	}
	replace := func() {
		command, _ = json.Marshal(protocol.Command{OperationID: "pool-nfs-op", Kind: "storage-register", Storage: &storage})
		if _, err = tx.Exec("UPDATE managed_resources SET command=? WHERE id=?", string(command), storage.ID); err != nil {
			t.Fatal(err)
		}
	}
	storage.MemberAccess[0].Identity = ""
	replace()
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, member); err == nil {
		t.Fatal("identity-less clustered member access permitted")
	}
	storage.MemberAccess[0].Identity, storage.MemberAccess[0].Error = "gfs2:cluster-1", "native check failed"
	replace()
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, member); err == nil {
		t.Fatal("errored clustered member access permitted")
	}
	storage.MemberAccess[0].Error, storage.MemberAccess[0].ObservedAt = "", time.Now().Add(time.Minute)
	replace()
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, member); err == nil {
		t.Fatal("future clustered member access permitted")
	}
	storage.MemberAccess[0].ObservedAt = time.Now().UTC()
	storage.Source.ISCSI.EligibleHostIDs = []string{master.HostID, "member-three", "member-four"}
	replace()
	if _, err = a.c.readyStorageForHost(tx, storage.ID, pool, member); err == nil {
		t.Fatal("noneligible clustered member placement permitted")
	}
}

func TestFreshControllerAdoptsMemberVMUsingMasterRemoteStorage(t *testing.T) {
	a := newClient(t)
	site := a.site()
	master := a.enroll(a.token(site, ""), csr(t))
	member := a.enroll(a.token(site, ""), csr(t))
	pool := a.poolOf(master.HostID)
	membership := protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: member.HostID, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"},
	}}
	remote := protocol.StorageRegistration{
		ID: "pool-nfs", PoolID: pool, HostID: master.HostID, Name: "Pool NFS", Kind: protocol.StorageKindNFS,
		Generation: 1, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/guests"}},
		MemberAccess: []protocol.PoolStorageAccess{{RepositoryID: "pool-nfs", HostID: member.HostID, Generation: 1, Identity: "nfs-fsid", Readable: true, Writable: true, QEMUWritable: true, Locking: true, ObservedAt: time.Now().UTC()}},
	}
	// The master owns the repository definition. A replacement Controller must
	// retain that single row while accepting the member's Core-defined VM.
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{remote}}}})
	guest := vmSpec(pool, member.HostID, "member-guest", remote.ID, "member-net", "")
	guest.ID = "2f1c2a3e-9b7d-4c5e-8a1b-0d2e3f4a5b6c"
	memberShadow := remote
	memberShadow.Name, memberShadow.Path = "member overwrite attempt", "/member-only-path"
	a.report(certificate(t, member.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.StorageRepositoryCapability}, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{memberShadow}, Networks: []protocol.NetworkRegistration{{ID: "member-net", PoolID: pool, HostID: member.HostID, Name: "member network", Kind: "bridge", Source: "br0", Uplink: "nex0"}}, VMs: []protocol.VMCreateSpec{guest}}}})
	var vms []managedAppliance
	read(t, a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || vms[0].ID != guest.ID || vms[0].PoolID != pool || vms[0].HostID != member.HostID || vms[0].Status != "succeeded" {
		t.Fatalf("member VM backed by master remote storage was not adopted: %+v", vms)
	}
	// Clustered GFS2 is also pool-shared. Exercise adoption directly with the
	// member's conflicting projection; it must be ignored before it can replace
	// the master row (ordinary iSCSI does not take this path).
	clusteredShadow := memberShadow
	clusteredShadow.Kind = protocol.StorageKindISCSI
	clusteredShadow.Source = protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2", EligibleHostIDs: []string{master.HostID, member.HostID, "member-three"}}}
	tx, err := a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.c.adoptManaged(tx, member.HostID, &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership, Storage: []protocol.StorageRegistration{clusteredShadow}}}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var storages []protocol.StorageRegistration
	read(t, a.call("GET", "/api/v1/storage", nil, nil, 200), &storages)
	if len(storages) != 1 || storages[0].HostID != master.HostID || storages[0].Name != remote.Name || storages[0].Path != remote.Path {
		t.Fatalf("member report overwrote master-owned remote storage: %+v", storages)
	}
}
