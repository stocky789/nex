// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBackupRepositoryPreservesNFSAndRejectsLocal(t *testing.T) {
	repo := BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary NFS", Kind: BackupRepoKindNFS,
		Source: NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
	}
	if err := repo.Valid(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(repo)
	if err != nil || !strings.Contains(string(raw), `"source":`) {
		t.Fatalf("legacy NFS source omitted: %s, %v", raw, err)
	}
	var decoded BackupRepository
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Valid() != nil {
		t.Fatalf("legacy NFS round trip: %v, %v", err, decoded.Valid())
	}
	repo.Kind = "local"
	if err := repo.Valid(); err == nil {
		t.Fatal("local destination must be refused")
	}
}

func TestBackupRepositoryTypedSources(t *testing.T) {
	base := BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Offsite"}
	cases := []struct {
		kind string
		set  func(*BackupRepository)
	}{
		{BackupRepoKindSMB3, func(r *BackupRepository) {
			r.SMBSource = &SMBStorageSource{Server: "smb.example.test", Share: "backups"}
		}},
		{BackupRepoKindS3, func(r *BackupRepository) {
			r.S3Source = &BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups", Prefix: "pool-1"}
		}},
		{BackupRepoKindSFTP, func(r *BackupRepository) {
			r.SFTPSource = &BackupSFTPSource{Host: "sftp.example.test", Root: "/backups", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			repo := base
			repo.Kind = tc.kind
			tc.set(&repo)
			if err := repo.Valid(); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(repo)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), `"source":`) {
				t.Fatalf("legacy NFS source emitted for %s: %s", tc.kind, raw)
			}
			var decoded BackupRepository
			if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Valid() != nil {
				t.Fatalf("round trip: %v, %v", err, decoded.Valid())
			}
			repo.Source = NFSStorageSource{Server: "nfs.example.test", Export: "/backup"}
			if repo.Valid() == nil {
				t.Fatal("mixed source kinds must be refused")
			}
		})
	}
}

func TestBackupRepositoryRejectsUnsafeRemoteSources(t *testing.T) {
	base := BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Offsite"}
	s3 := base
	s3.Kind = BackupRepoKindS3
	s3.S3Source = &BackupS3Source{Endpoint: "http://s3.example.test", Bucket: "backups"}
	if s3.Valid() == nil {
		t.Fatal("S3 endpoint must use TLS")
	}
	s3.S3Source.Endpoint = "https://s3.example.test"
	s3.S3Source.Prefix = "../escape"
	if s3.Valid() == nil {
		t.Fatal("S3 prefix traversal must be refused")
	}
	sftp := base
	sftp.Kind = BackupRepoKindSFTP
	sftp.SFTPSource = &BackupSFTPSource{Host: "sftp.example.test", Root: "/backups", HostKeySHA256: ""}
	if sftp.Valid() == nil {
		t.Fatal("SFTP host key pin is required")
	}
}

func TestBackupRunManifestDigestValidation(t *testing.T) {
	run := BackupRun{OperationID: "restore-1", Kind: BackupKindRestoreNew, PoolID: "pool-1",
		RepositoryID: "repo-1", RecoveryPointID: "point-1", Stage: "reserved", Status: "pending",
		CreatedAt: time.Now().UTC(), ManifestSHA256: strings.Repeat("a", 64)}
	if err := run.Valid(); err != nil {
		t.Fatal(err)
	}
	run.ManifestSHA256 = "not-a-digest"
	if err := run.Valid(); err == nil {
		t.Fatal("invalid retained manifest digest was accepted")
	}
	run.ManifestSHA256 = ""
	if err := run.Valid(); err != nil {
		t.Fatalf("legacy run must remain readable for fail-closed reconciliation: %v", err)
	}
}

func TestBackupManifestRefusesIncompleteAndCorrupt(t *testing.T) {
	manifest := BackupManifest{
		Version: BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1",
		PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: BackupCaptureStopped,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		Disks:    []BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: 1, SHA256: strings.Repeat("c", 64)}},
		Complete: true, Success: 1,
	}
	if err := manifest.Valid(); err != nil {
		t.Fatal(err)
	}
	incomplete := manifest
	incomplete.Complete = false
	if err := incomplete.Valid(); err == nil {
		t.Fatal("incomplete manifest must be refused")
	}
	failed := manifest
	failed.Success = 0
	if err := failed.Valid(); err == nil {
		t.Fatal("success=0 must be refused even when files look present")
	}
	corrupt := manifest
	corrupt.Disks[0].Filename = "../escape.qcow2"
	if err := corrupt.Valid(); err == nil {
		t.Fatal("path traversal must be refused")
	}
}

func TestBackupNowAndRestoreRequestsAreBounded(t *testing.T) {
	if err := (BackupNowRequest{RepositoryID: "repo-1"}).Valid(); err != nil {
		t.Fatal(err)
	}
	if err := (BackupNowRequest{RepositoryID: "repo-1", Consistency: BackupCaptureOnline}).Valid(); err != nil {
		t.Fatal(err)
	}
	if err := (BackupNowRequest{RepositoryID: "repo-1", Consistency: "online"}).Valid(); err == nil {
		t.Fatal("literal online must use crash/quiesced values")
	}
	restore := RestoreAsNewRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1",
		NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1",
	}
	if err := restore.Valid(); err != nil {
		t.Fatal(err)
	}
	restore.NewVMName = ""
	if err := restore.Valid(); err == nil {
		t.Fatal("empty restore name must be refused")
	}
}

func TestBackupManifestAcceptsOnlineConsistency(t *testing.T) {
	manifest := BackupManifest{
		Version: BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1",
		PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: BackupCaptureOnline,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		Disks:    []BackupDiskArtifact{{Target: "vda", Filename: "vda.qcow2", Format: "qcow2", Size: 1, SHA256: strings.Repeat("c", 64)}},
		Complete: true, Success: 1,
	}
	if err := manifest.Valid(); err != nil {
		t.Fatal(err)
	}
}
