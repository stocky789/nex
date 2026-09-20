// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestMemberZFSTombstoneDropsItsProjection(t *testing.T) {
	l := newTransferLab(t)
	storage := protocol.StorageRegistration{ID: "member-zfs", PoolID: l.master.pool, HostID: l.member.id, Name: "Member ZFS", Path: "/var/lib/nex-storage/member-zfs", Kind: protocol.StorageKindZFS, Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: "nex-member", Dataset: "vms", MountPath: "/var/lib/nex-storage/member-zfs"}}, ZFSProvisioned: true, ZFSScope: "pool"}
	tx, err := l.a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = l.a.c.mirrorStorageProjection(tx, l.master.pool, l.member.id, storage); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tombstone := protocol.StorageRegistration{ID: storage.ID, PoolID: l.master.pool, HostID: l.member.id, Generation: 2, Tombstone: true, Kind: protocol.StorageKindZFS, ZFSProvisioned: true, ZFSScope: "pool"}
	tx, err = l.a.c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = l.a.c.adoptManaged(tx, l.member.id, &protocol.Inventory{Managed: &protocol.ManagedState{Membership: l.member.inv.Managed.Membership, Storage: []protocol.StorageRegistration{tombstone}}}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = l.a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE id=? AND kind='storage-register'", storage.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("member ZFS tombstone left a storage projection")
	}
}
