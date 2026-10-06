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
		t.Fatalf("NFS source omitted: %s, %v", raw, err)
	}
	var decoded BackupRepository
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Valid() != nil {
		t.Fatalf("NFS round trip: %v, %v", err, decoded.Valid())
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
				t.Fatalf("NFS source emitted for %s: %s", tc.kind, raw)
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
	for name, mutate := range map[string]func(*BackupRepository){
		"S3 port out of range": func(r *BackupRepository) { r.S3Source.Endpoint = "https://s3.example.test:99999" },
		"S3 control character": func(r *BackupRepository) { r.S3Source.Bucket = "backups\r\nx" },
		"S3 dot prefix":        func(r *BackupRepository) { r.S3Source.Prefix = "." },
		"S3 oversized prefix":  func(r *BackupRepository) { r.S3Source.Prefix = strings.Repeat("p", 1025) },
		"S3 bad fingerprint":   func(r *BackupRepository) { r.RemoteIdentity = &BackupRemoteIdentity{Fingerprint: "NOT-HEX"} },
		"S3 oversized health":  func(r *BackupRepository) { r.Health.Error = strings.Repeat("e", 4097) },
		"SMB shell metachar in server": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.SMBSource = BackupRepoKindSMB3, nil, &SMBStorageSource{Server: "evil;rm", Share: "backups"}
		},
		"SMB backtick in server": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.SMBSource = BackupRepoKindSMB3, nil, &SMBStorageSource{Server: "host`id`", Share: "backups"}
		},
		"SMB pipe in server": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.SMBSource = BackupRepoKindSMB3, nil, &SMBStorageSource{Server: "a|b", Share: "backups"}
		},
		"SMB dollar paren in server": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.SMBSource = BackupRepoKindSMB3, nil, &SMBStorageSource{Server: "x$(y)", Share: "backups"}
		},
		"SMB control in share": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.SMBSource = BackupRepoKindSMB3, nil, &SMBStorageSource{Server: "smb.example.test", Share: "b\x1b"}
		},
		"SMB slash in share": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.SMBSource = BackupRepoKindSMB3, nil, &SMBStorageSource{Server: "smb.example.test", Share: "vms/share"}
		},
		"SMB quote in share": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.SMBSource = BackupRepoKindSMB3, nil, &SMBStorageSource{Server: "smb.example.test", Share: `VM "Files$"`}
		},
		"SMB leading space share": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.SMBSource = BackupRepoKindSMB3, nil, &SMBStorageSource{Server: "smb.example.test", Share: " backups"}
		},
		"NFS with credential": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.Source = BackupRepoKindNFS, nil, NFSStorageSource{Server: "nfs.example.test", Export: "/b"}
		},
		"SFTP root control char": func(r *BackupRepository) {
			r.Kind, r.S3Source, r.SFTPSource = BackupRepoKindSFTP, nil, &BackupSFTPSource{Host: "sftp.example.test", Root: "/b\n", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
		},
	} {
		repo := base
		repo.Kind, repo.CredentialRef = BackupRepoKindS3, "credential-1"
		repo.S3Source = &BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups"}
		if repo.Valid() != nil {
			t.Fatalf("%s: base repository is invalid: %v", name, repo.Valid())
		}
		mutate(&repo)
		if repo.Valid() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	smb := base
	smb.Kind, smb.SMBSource = BackupRepoKindSMB3, &SMBStorageSource{Server: "smb.example.test", Share: "VM Files$"}
	if err := smb.Valid(); err != nil {
		t.Fatalf("safe SMB share with space and $ refused: %v", err)
	}
	ipv6 := base
	ipv6.Kind, ipv6.SFTPSource = BackupRepoKindSFTP, &BackupSFTPSource{Host: "fd00::1", Root: "/b", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	if err := ipv6.Valid(); err != nil {
		t.Fatalf("IPv6 host refused: %v", err)
	}
}

func TestValidPoolHostnameRefusesSpoofingCharacters(t *testing.T) {
	for _, name := range []string{"", " host", "host ", "host\tname", "host\x1bname", "host\u202ename", "host\u0085name", strings.Repeat("h", 201)} {
		if ValidPoolHostname(name) {
			t.Errorf("hostname %q was accepted", name)
		}
	}
	for _, name := range []string{"host-a.example.test", "rack 2 host", "hôte"} {
		if !ValidPoolHostname(name) {
			t.Errorf("hostname %q was refused", name)
		}
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
		t.Fatalf("run without a manifest digest must remain readable for fail-closed reconciliation: %v", err)
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

func TestS3RegionIsValidated(t *testing.T) {
	for region, want := range map[string]bool{"": true, "eu-west-2": true, "US East": false, "eu_west": false, strings.Repeat("a", 33): false} {
		if got := validBackupS3(BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups", Region: region}); got != want {
			t.Errorf("region %q valid=%v, want %v", region, got, want)
		}
	}
}
