// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestSMBRepositoryReplacementFailsBeforePreparationAndDurableCommit(t *testing.T) {
	s := seedBackupMaster(t)
	existing := protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		SMBIdentity:   &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)},
		CredentialRef: "old-receipt", MountPath: t.TempDir(),
	}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{existing.ID: existing}}); err != nil {
		t.Fatal(err)
	}
	previous := prepareBackupRepositorySMBNative
	t.Cleanup(func() { prepareBackupRepositorySMBNative = previous })
	prepareCalls := 0
	prepareBackupRepositorySMBNative = func(context.Context, protocol.BackupRepository, protocol.StorageCredentials) (protocol.BackupRepository, error) {
		prepareCalls++
		return protocol.BackupRepository{}, nil
	}
	for _, change := range []func(*protocol.BackupRepository){
		func(r *protocol.BackupRepository) {
			r.SMBSource = &protocol.SMBStorageSource{Server: "other.example.test", Share: "backups"}
		},
	} {
		requested := existing
		requested.MountPath, requested.SMBIdentity = "", nil
		change(&requested)
		if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, requested, "register-new"); !errors.Is(err, ErrBackupUnavailable) || prepareCalls != 0 {
			t.Fatalf("unsafe replacement reached native prepare: %v, calls=%d", err, prepareCalls)
		}
		prepared := requested
		prepared.MountPath, prepared.SMBIdentity = t.TempDir(), existing.SMBIdentity
		if prepared.SMBSource.Server != existing.SMBSource.Server {
			prepared.SMBIdentity = &protocol.SMBStorageIdentity{Server: prepared.SMBSource.Server, Share: prepared.SMBSource.Share, MarkerID: strings.Repeat("a", 32)}
		}
		if _, err := upsertPoolBackupRepositoryPrepared(s, requested, prepared); !errors.Is(err, ErrBackupUnavailable) {
			t.Fatalf("unsafe replacement committed from prepared result: %v", err)
		}
	}
	requested := existing
	requested.MountPath, requested.SMBIdentity = "", nil
	prepared := requested
	prepared.MountPath = t.TempDir()
	prepared.SMBIdentity = &protocol.SMBStorageIdentity{Server: existing.SMBSource.Server, Share: existing.SMBSource.Share, MarkerID: strings.Repeat("b", 32)}
	if _, err := upsertPoolBackupRepositoryPrepared(s, requested, prepared); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("changed SMB marker committed under the existing repository: %v", err)
	}
	current, err := GetPoolBackupRepository(s, existing.ID)
	if err != nil || current.CredentialRef != existing.CredentialRef || current.SMBIdentity.MarkerID != existing.SMBIdentity.MarkerID {
		t.Fatalf("failed replacement changed durable repository: %+v, %v", current, err)
	}
}
