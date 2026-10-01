// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func readableRemoteCredential(t *testing.T, s *Store, kind string) string {
	t.Helper()
	creds := protocol.StorageCredentials{Username: "example-user", Secret: "example-secret"}
	if kind == protocol.BackupRepoKindSFTP {
		creds = protocol.StorageCredentials{Username: "example-user", SSHPrivateKey: "example-key"}
	}
	receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
		Offer: protocol.StorageCredentialOffer{RequestID: "old", RegistrationRequestID: "backup-credential:" + kind + ":register-old",
			PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute)},
		Credentials: creds,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt.Receipt
}

func TestRemoteRepositoryReplacementPreservesRetainedPoints(t *testing.T) {
	for _, kind := range []string{protocol.BackupRepoKindS3, protocol.BackupRepoKindSFTP} {
		for _, change := range []string{"source", "credential"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				s := seedBackupMaster(t)
				// A readable credential is kept; only an unreadable one may be replaced (BUG-0773).
				existing := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: kind,
					CredentialRef: readableRemoteCredential(t, s, kind), RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)}}
				if kind == protocol.BackupRepoKindS3 {
					existing.S3Source = &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups", Prefix: "old"}
				} else {
					existing.SFTPSource = &protocol.BackupSFTPSource{Host: "sftp.example.test", Root: "/old", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
				}
				if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{existing.ID: existing}}); err != nil {
					t.Fatal(err)
				}
				point := protocol.BackupRecoveryPoint{ID: "point-1", RepositoryID: existing.ID, PoolID: s.State.PoolID,
					SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped, ManifestSHA256: strings.Repeat("b", 64)}
				if err := savePoolBackupCatalogEntry(s, point); err != nil {
					t.Fatal(err)
				}
				requested := existing
				requested.RemoteIdentity = nil
				if change == "credential" {
					requested.CredentialRef = "new-receipt"
				} else if kind == protocol.BackupRepoKindS3 {
					requested.S3Source = &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups", Prefix: "new"}
				} else {
					requested.SFTPSource = &protocol.BackupSFTPSource{Host: "sftp.example.test", Root: "/new", HostKeySHA256: existing.SFTPSource.HostKeySHA256}
				}
				prepared := requested
				prepared.RemoteIdentity = existing.RemoteIdentity
				if _, err := upsertPoolBackupRepositoryPrepared(s, requested, prepared, false); !errors.Is(err, ErrBackupUnavailable) && !errors.Is(err, ErrBackupCredentialInUse) {
					t.Fatalf("same-ID replacement committed: %v", err)
				}
				saved, err := GetPoolBackupRepository(s, existing.ID)
				if err != nil || !reflect.DeepEqual(saved.S3Source, existing.S3Source) || !reflect.DeepEqual(saved.SFTPSource, existing.SFTPSource) || saved.CredentialRef != existing.CredentialRef {
					t.Fatalf("durable destination changed: %+v, %v", saved, err)
				}
				if retained, err := getCatalogPoint(s, existing.ID, point.ID); err != nil || retained.ID != point.ID {
					t.Fatalf("retained point lost: %+v, %v", retained, err)
				}
				if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, requested, "register-new", ""); !errors.Is(err, ErrBackupUnavailable) && !errors.Is(err, ErrBackupCredentialInUse) {
					t.Fatalf("same-ID replacement reached remote preparation: %v", err)
				}
			})
		}
	}
}

func TestNFSRepositoryReplacementPreservesRetainedPoints(t *testing.T) {
	for _, change := range []string{"source", "filesystem"} {
		t.Run(change, func(t *testing.T) {
			s := seedBackupMaster(t)
			existing := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary",
				Kind: protocol.BackupRepoKindNFS, Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups"},
				Identity:  &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/backups", FilesystemID: "fs-1"},
				MountPath: t.TempDir()}
			if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{existing.ID: existing}}); err != nil {
				t.Fatal(err)
			}
			point := protocol.BackupRecoveryPoint{ID: "point-1", RepositoryID: existing.ID, PoolID: s.State.PoolID,
				SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped, ManifestSHA256: strings.Repeat("b", 64)}
			if err := savePoolBackupCatalogEntry(s, point); err != nil {
				t.Fatal(err)
			}
			requested := existing
			requested.Identity, requested.MountPath = nil, ""
			if change == "source" {
				requested.Source.Server = "other.example.test"
				if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, requested, "register-new", ""); !errors.Is(err, ErrBackupUnavailable) && !errors.Is(err, ErrBackupCredentialInUse) {
					t.Fatalf("source replacement reached mount preparation: %v", err)
				}
			} else {
				previous := prepareBackupRepositoryNative
				t.Cleanup(func() { prepareBackupRepositoryNative = previous })
				prepareBackupRepositoryNative = func(_ context.Context, got protocol.BackupRepository) (protocol.BackupRepository, error) {
					if !reflect.DeepEqual(got.Identity, existing.Identity) || got.MountPath != existing.MountPath {
						t.Fatal("re-registration did not pin the prior filesystem before owner claim")
					}
					return protocol.BackupRepository{}, ErrBackupUnavailable
				}
				if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, requested, "register-new", ""); !errors.Is(err, ErrBackupUnavailable) && !errors.Is(err, ErrBackupCredentialInUse) {
					t.Fatalf("changed filesystem reached owner claim: %v", err)
				}
			}
			prepared := requested
			identity := *existing.Identity
			if change == "filesystem" {
				identity.FilesystemID = "fs-2"
			} else {
				identity.Server = requested.Source.Server
			}
			prepared.Identity, prepared.MountPath = &identity, existing.MountPath
			if _, err := upsertPoolBackupRepositoryPrepared(s, requested, prepared, false); !errors.Is(err, ErrBackupUnavailable) {
				t.Fatalf("same-ID replacement committed: %v", err)
			}
			saved, err := GetPoolBackupRepository(s, existing.ID)
			if err != nil || !reflect.DeepEqual(saved.Source, existing.Source) || !reflect.DeepEqual(saved.Identity, existing.Identity) {
				t.Fatalf("durable destination changed: %+v, %v", saved, err)
			}
			if retained, err := getCatalogPoint(s, existing.ID, point.ID); err != nil || retained.ID != point.ID {
				t.Fatalf("retained point lost: %+v, %v", retained, err)
			}
		})
	}
}

func TestPeerNFSAdoptionPinsMasterFilesystem(t *testing.T) {
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source:   protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups"},
		Identity: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/backups", FilesystemID: "nfs4:7f000001:801:1:2"}}
	peer := portablePeerRepository(repo)
	if !reflect.DeepEqual(peer.Identity, repo.Identity) || peer.MountPath != "" {
		t.Fatal("peer request lost the master's NFS identity")
	}
	previous := prepareBackupRepositoryNative
	t.Cleanup(func() { prepareBackupRepositoryNative = previous })
	prepareBackupRepositoryNative = func(_ context.Context, got protocol.BackupRepository) (protocol.BackupRepository, error) {
		if !reflect.DeepEqual(got.Identity, repo.Identity) {
			t.Fatal("member did not pin the master's filesystem before owner claim")
		}
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	if _, err := prepareBackupRepositoryForAdoption(context.Background(), nil, peer); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("member preparation did not fail closed: %v", err)
	}
	missing := peer
	missing.Identity = nil
	if _, err := prepareBackupRepositoryForAdoption(context.Background(), nil, missing); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("member accepted missing master identity: %v", err)
	}
	changed := peer
	changed.Identity = &protocol.NFSStorageIdentity{Server: repo.Source.Server, Export: repo.Source.Export, FilesystemID: "nfs4:7f000001:801:1:3"}
	prepared := changed
	prepared.MountPath = "/mnt/repository"
	if preparedBackupRepositoryMatches(peer, prepared) {
		t.Fatal("member accepted a different filesystem identity")
	}
}

// A confirmed same-ID registration re-pins an SFTP host key: it must verify, never claim, the
// destination, and keeps the repository's identity and creation time.
func TestSFTPRegistrationReplacesPinOnlyWithConfirmation(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	existing := testPeerSFTPRepository(t, master)
	existing.CreatedAt = time.Now().Add(-time.Hour).UTC()
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{existing.ID: existing}}); err != nil {
		t.Fatal(err)
	}
	requested := existing
	requested.RemoteIdentity = nil
	requested.SFTPSource = &protocol.BackupSFTPSource{Host: "sftp.example.test", Root: "/backups", HostKeySHA256: "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}
	var seen *protocol.BackupRemoteIdentity
	previous := prepareBackupRepositorySFTPNative
	t.Cleanup(func() { prepareBackupRepositorySFTPNative = previous })
	prepareBackupRepositorySFTPNative = func(_ context.Context, got protocol.BackupRepository, _ string, _ protocol.StorageCredentials) (protocol.BackupRepository, error) {
		seen = got.RemoteIdentity
		got.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: sftpIdentity(got.SFTPSource, "backup")}
		return got, nil
	}
	ctx := context.Background()
	if _, err := prepareBackupRepositoryForRegistration(ctx, master, requested, "register-1", ""); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("unconfirmed host-key change was not refused: %v", err)
	}
	if _, err := prepareBackupRepositoryForRegistration(ctx, master, requested, "register-1", "other-repo"); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("wrong confirmation accepted: %v", err)
	}
	moved := requested
	moved.SFTPSource = &protocol.BackupSFTPSource{Host: "other.example.test", Root: "/backups", HostKeySHA256: requested.SFTPSource.HostKeySHA256}
	if _, err := prepareBackupRepositoryForRegistration(ctx, master, moved, "register-1", existing.ID); !errors.Is(err, ErrBackupUnavailable) || !strings.Contains(err.Error(), "different destination") {
		t.Fatalf("replacement moved the destination without naming why: %v", err)
	}
	unknown := requested
	unknown.ID = "repo-unknown"
	if _, err := prepareBackupRepositoryForRegistration(ctx, master, unknown, "register-1", unknown.ID); !errors.Is(err, ErrBackupRepoNotFound) {
		t.Fatalf("replacement of an unknown repository: %v", err)
	}
	prepared, err := prepareBackupRepositoryForRegistration(ctx, master, requested, "register-1", existing.ID)
	if err != nil {
		t.Fatalf("confirmed replacement failed: %v", err)
	}
	if seen == nil {
		t.Fatal("replacement would claim the destination instead of verifying it")
	}
	if _, err := upsertPoolBackupRepositoryPrepared(master, requested, prepared, false); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("unconfirmed upsert accepted the new pin: %v", err)
	}
	saved, err := upsertPoolBackupRepositoryPrepared(master, requested, prepared, true)
	if err != nil {
		t.Fatalf("confirmed upsert failed: %v", err)
	}
	if saved.SFTPSource.HostKeySHA256 != requested.SFTPSource.HostKeySHA256 || saved.ID != existing.ID || !saved.CreatedAt.Equal(existing.CreatedAt) {
		t.Fatalf("saved repository lost its identity: %+v", saved)
	}
}
