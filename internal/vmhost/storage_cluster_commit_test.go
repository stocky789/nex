// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"nex.local/nex/internal/protocol"
	"testing"
)

func TestCommitClusteredStorageRegistrationPersistsBootstrapProjection(t *testing.T) {
	s := protocol.StorageRegistration{ID: "repo-123456", PoolID: "pool-123", HostID: "host-123", Name: "shared", Path: "/var/lib/nex/storage/repo-1", Kind: protocol.StorageKindISCSI, Generation: 1, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:shared", LUN: 1, WWID: "naa.1234", FilesystemUUID: "12345678-1234-1234-1234-123456789abc", MountPath: "/var/lib/nex/storage/repo-1", Mode: "clustered_gfs2", ClusterID: "cluster-1", LockTable: "cluster-1:repo", EligibleHostIDs: []string{"host-123", "host-234", "host-345"}}}, Identity: protocol.StorageIdentity{ISCSI: &protocol.ISCSIStorageIdentity{TargetIQN: "iqn.2026-01.test:shared", LUN: 1, WWID: "naa.1234", FilesystemUUID: "12345678-1234-1234-1234-123456789abc", FilesystemType: "gfs2", GFS2LockTable: "cluster-1:repo", GFS2Journals: 3}}}
	dir := t.TempDir()
	if err := CommitClusteredStorageRegistration(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	changed := s
	changed.Generation = 2
	if err := CommitClusteredStorageRegistration(context.Background(), dir, changed); err == nil {
		t.Fatal("same repository ID with another generation replaced the immutable bootstrap projection")
	}
	changed = s
	changedSource := *s.Source.ISCSI
	changedSource.Portal = "other-san.example.test"
	changed.Source.ISCSI = &changedSource
	if err := CommitClusteredStorageRegistration(context.Background(), dir, changed); err == nil {
		t.Fatal("same repository ID with changed immutable source replaced the bootstrap projection")
	}
	if err := CommitClusteredStorageRegistration(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	got, _ := NewManaged(dir).Applied(context.Background())
	if len(got.Storage) != 1 || got.Storage[0].ID != "repo-123456" {
		t.Fatalf("projection %#v", got.Storage)
	}
}
