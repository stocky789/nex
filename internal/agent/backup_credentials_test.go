// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestBackupCredentialHandoffScopeReplayRotationAndPrivacy(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{
		Offer: protocol.StorageCredentialOffer{
			RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
			PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
		},
		Credentials: protocol.StorageCredentials{Username: "example-access-key", Secret: "private-example-secret"},
	}
	receipt, err := saveCredentialHandoff(s, handoff)
	if err != nil || receipt.Receipt == "" {
		t.Fatalf("save handoff: %+v, %v", receipt, err)
	}
	if replay, err := saveCredentialHandoff(s, handoff); err != nil || replay.Receipt != receipt.Receipt {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Kind: protocol.BackupRepoKindS3, CredentialRef: receipt.Receipt}
	got, err := ResolveBackupCredentialForRegistration(s, repo, "register-1")
	if err != nil || got != handoff.Credentials {
		t.Fatalf("resolve: %+v, %v", got, err)
	}
	if _, err := ResolveBackupCredential(s, repo); err == nil {
		t.Fatal("routine resolver accepted an uncommitted repository")
	}
	if _, err := ResolveBackupCredentialForRegistration(s, repo, "register-other"); err == nil {
		t.Fatal("registration resolver accepted a different operation")
	}
	for _, changed := range []protocol.StorageCredentialHandoff{
		func() protocol.StorageCredentialHandoff { v := handoff; v.Credentials.Secret = "changed"; return v }(),
		func() protocol.StorageCredentialHandoff { v := handoff; v.Offer.RepositoryID = "repo-2"; return v }(),
		func() protocol.StorageCredentialHandoff {
			v := handoff
			v.Offer.RegistrationRequestID = "backup-credential:unsupported:register-1"
			return v
		}(),
		func() protocol.StorageCredentialHandoff {
			v := handoff
			v.Offer.RegistrationRequestID = "backup-credential:s3:register-2"
			return v
		}(),
	} {
		if _, err := saveCredentialHandoff(s, changed); err == nil || strings.Contains(err.Error(), "private-example-secret") || strings.Contains(err.Error(), "changed") {
			t.Fatalf("conflicting replay was accepted or leaked secret: %v", err)
		}
	}
	for _, changed := range []protocol.BackupRepository{
		func() protocol.BackupRepository { v := repo; v.ID = "repo-2"; return v }(),
		func() protocol.BackupRepository { v := repo; v.PoolID = "pool-2"; return v }(),
		func() protocol.BackupRepository { v := repo; v.Kind = "unsupported"; return v }(),
		func() protocol.BackupRepository { v := repo; v.CredentialRef = "other"; return v }(),
	} {
		if _, err := ResolveBackupCredentialForRegistration(s, changed, "register-1"); err == nil {
			t.Fatalf("wrong scope resolved: %+v", changed)
		}
	}
	handoff.Offer.RequestID = "credential-2"
	handoff.Offer.RegistrationRequestID = "backup-credential:s3:register-2"
	handoff.Credentials.Secret = "rotated-private-secret"
	rotated, err := saveCredentialHandoff(s, handoff)
	if err != nil || rotated.Receipt == receipt.Receipt {
		t.Fatalf("rotation: %+v, %v", rotated, err)
	}
	repo.CredentialRef = rotated.Receipt
	if got, err := ResolveBackupCredentialForRegistration(s, repo, "register-2"); err != nil || got.Secret != "rotated-private-secret" {
		t.Fatalf("rotated resolver: %+v, %v", got, err)
	}
	public, err := json.Marshal(struct {
		Receipt protocol.StorageCredentialReceipt `json:"receipt"`
		Repo    protocol.BackupRepository         `json:"repo"`
		Command protocol.Command                  `json:"command"`
	}{rotated, repo, protocol.Command{Kind: "backup-repository-register", BackupRepository: &repo}})
	if err != nil || strings.Contains(string(public), "private-secret") {
		t.Fatalf("public data exposed backup material: %s, %v", public, err)
	}
	dir, err := backupCredentialDir(s)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("private receipts: %v, %v", entries, err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("receipt mode = %v, %v", info, err)
		}
	}
	path, err := backupCredentialPath(s, handoff.Offer.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "rotated-private-secret", "tampered-private-secret", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBackupCredentialForRegistration(s, repo, "register-2"); err == nil || strings.Contains(err.Error(), "tampered-private-secret") {
		t.Fatalf("tampered receipt resolved or leaked material: %v", err)
	}
}

func TestBackupCredentialRejectsWrongRecipientAndMaterial(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:sftp:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: "private\nkey"}}
	if _, err := saveCredentialHandoff(s, handoff); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []protocol.StorageCredentialHandoff{
		func() protocol.StorageCredentialHandoff { v := handoff; v.Offer.HostID = "other-host"; return v }(),
		func() protocol.StorageCredentialHandoff { v := handoff; v.Offer.PoolID = "other-pool"; return v }(),
		func() protocol.StorageCredentialHandoff {
			v := handoff
			v.Offer.ExpiresAt = time.Now().Add(-time.Second)
			return v
		}(),
		func() protocol.StorageCredentialHandoff { v := handoff; v.Credentials.Secret = "password"; return v }(),
	} {
		if _, err := saveCredentialHandoff(s, changed); err == nil {
			t.Fatalf("invalid handoff accepted: %+v", changed.Offer)
		}
	}
}

func TestBackupCredentialRotationRetiresOldReceipt(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-access-key", Secret: "old-private-secret"}}
	oldReceipt, err := saveCredentialHandoff(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	oldRepo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindS3,
		S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups"}, CredentialRef: oldReceipt.Receipt}
	handoff.Offer.RequestID = "credential-2"
	handoff.Offer.RegistrationRequestID = "backup-credential:s3:register-2"
	handoff.Credentials.Secret = "new-private-secret"
	newReceipt, err := saveCredentialHandoff(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	newRepo := oldRepo
	newRepo.CredentialRef = newReceipt.Receipt
	if err := RetireBackupCredential(s, oldRepo, newRepo); err == nil {
		t.Fatal("retired old receipt before the new registration was durable")
	}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{newRepo.ID: newRepo}}); err != nil {
		t.Fatal(err)
	}
	if err := RetireBackupCredential(s, oldRepo, newRepo); err != nil {
		t.Fatal(err)
	}
	if err := RetireBackupCredential(s, oldRepo, newRepo); err != nil {
		t.Fatalf("retirement retry: %v", err)
	}
	if _, err := ResolveBackupCredential(s, oldRepo); err == nil {
		t.Fatal("old receipt remained usable after rotation")
	}
	previous := handoff
	previous.Offer.RequestID = "credential-1"
	previous.Offer.RegistrationRequestID = "backup-credential:s3:register-1"
	previous.Credentials.Secret = "old-private-secret"
	if _, err := saveCredentialHandoff(s, previous); err == nil {
		t.Fatal("retired receipt was revived by exact handoff retry")
	}
	if got, err := ResolveBackupCredential(s, newRepo); err != nil || got.Secret != "new-private-secret" {
		t.Fatalf("new receipt unavailable: %+v, %v", got, err)
	}
	stale := newRepo
	stale.Generation++
	if _, err := ResolveBackupCredential(s, stale); err == nil {
		t.Fatal("routine resolver accepted a stale repository generation")
	}
	if replay, err := saveCredentialHandoff(s, handoff); err != nil || replay.Receipt != newReceipt.Receipt {
		t.Fatalf("new handoff exact retry: %+v, %v", replay, err)
	}
	oldPath, err := backupCredentialPath(s, oldReceipt.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(oldPath)
	if err != nil || strings.Contains(string(data), "old-private-secret") {
		t.Fatalf("retired receipt retained material: %v", err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if err := RetireBackupCredential(s, oldRepo, newRepo); err != nil {
		t.Fatalf("already removed receipt blocked rotation retry: %v", err)
	}
}
