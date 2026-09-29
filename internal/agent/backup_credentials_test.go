// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
}

// Receipts are AES-GCM ciphertext bound to pool, host and reference: no plaintext at rest,
// and a flipped byte, a rewritten header, a replaced key file or a plaintext file is refused.
func TestBackupCredentialsAreEncryptedAtRest(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:smb3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret", Domain: "EXAMPLE"}}
	receipt, err := saveCredentialHandoff(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Kind: protocol.BackupRepoKindSMB3, CredentialRef: receipt.Receipt}
	if got, err := ResolveBackupCredentialForRegistration(s, repo, "register-1"); err != nil || got != handoff.Credentials {
		t.Fatalf("resolve: %+v, %v", got, err)
	}
	path, err := backupCredentialPath(s, "credential-1")
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"private-example-secret", "example-user", "EXAMPLE"} {
		if strings.Contains(string(original), plain) {
			t.Fatalf("receipt holds plaintext %q: %s", plain, original)
		}
	}
	var sealed sealedBackupCredential
	if err := json.Unmarshal(original, &sealed); err != nil {
		t.Fatal(err)
	}
	refuse := func(name string, raw []byte, want error) {
		t.Helper()
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveBackupCredentialForRegistration(s, repo, "register-1"); err == nil {
			t.Fatalf("%s: receipt resolved", name)
		}
		if _, err := saveCredentialHandoff(s, handoff); err == nil || want != nil && !errors.Is(err, want) || strings.Contains(err.Error(), "private-example-secret") {
			t.Fatalf("%s: replay = %v", name, err)
		}
	}
	flipped := sealed
	flipped.Ciphertext = append([]byte(nil), sealed.Ciphertext...)
	flipped.Ciphertext[0] ^= 1
	raw, _ := json.Marshal(flipped)
	refuse("flipped ciphertext", raw, nil)
	moved := sealed
	moved.Reference = "other-reference"
	raw, _ = json.Marshal(moved)
	refuse("rewritten header", raw, nil)
	plaintext, _ := json.Marshal(storedBackupCredential{Receipt: backupCredentialReceipt{Reference: receipt.Receipt}, Credentials: handoff.Credentials})
	refuse("plaintext receipt", plaintext, errBackupCredentialUnencrypted)
	listed, err := BackupCredentialReceipts(s)
	if err == nil || !strings.Contains(err.Error(), "not encrypted") || len(listed) != 0 {
		t.Fatalf("plaintext receipt listing = %+v, %v", listed, err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBackupCredentialForRegistration(s, repo, "register-1"); err != nil {
		t.Fatalf("restored receipt: %v", err)
	}
	keyPath := filepath.Join(s.dir, "backup-credential-key", "key.cred")
	if err := os.WriteFile(keyPath, append([]byte("sealed:"), make([]byte, 32)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBackupCredentialForRegistration(s, repo, "register-1"); err == nil {
		t.Fatal("receipt opened under a different key")
	}
	// A lost key is reported, never replaced by a new one.
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBackupCredentialForRegistration(s, repo, "register-1"); !errors.Is(err, errBackupCredentialKeyMissing) {
		t.Fatalf("lost key error = %v", err)
	}
	if _, err := BackupCredentialReceipts(s); !errors.Is(err, errBackupCredentialKeyMissing) {
		t.Fatalf("lost key listing error = %v", err)
	}
	if _, err := os.Lstat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("a read path recreated the key: %v", err)
	}
}

func TestBackupCredentialDomainIsSMBOnlyAndValidated(t *testing.T) {
	s := seedBackupMaster(t)
	for kind, cases := range map[string]map[string]bool{
		"smb3": {"EXAMPLE": true, "corp.example.test": true, "EX,AMPLE": false, "EX=AMPLE": false, "EX\nAMPLE": false, "EX\\AMPLE": false},
		"s3":   {"EXAMPLE": false},
	} {
		for domain, ok := range cases {
			id := kind + "-" + domain
			_, err := backupCredentialScope(s, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
				RequestID: "credential-1", RegistrationRequestID: "backup-credential:" + kind + ":register-1",
				PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
			}, Credentials: protocol.StorageCredentials{Username: "user", Secret: "secret", Domain: domain}})
			if (err == nil) != ok {
				t.Fatalf("%s: accepted=%v want %v (%v)", id, err == nil, ok, err)
			}
		}
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

// The receipt key is sealed by systemd-creds (the fake runner here); no plaintext key file
// exists, and a service that was handed the credential reads it from $CREDENTIALS_DIRECTORY
// without decrypting anything.
func TestBackupCredentialKeyIsSealedAndLoadedFromServiceCredential(t *testing.T) {
	s := seedBackupMaster(t)
	key, err := backupCredentialKey(s, true)
	if err != nil || len(key) != 32 {
		t.Fatalf("create: %v", err)
	}
	dir := filepath.Join(s.dir, "backup-credential-key")
	if _, err := os.Lstat(filepath.Join(dir, "key")); !os.IsNotExist(err) {
		t.Fatalf("plaintext key file exists: %v", err)
	}
	sealed, err := os.ReadFile(filepath.Join(dir, "key.cred"))
	if err != nil || !bytes.HasPrefix(sealed, []byte("sealed:")) {
		t.Fatalf("key is not sealed at rest: %v", err)
	}
	if again, err := backupCredentialKey(s, false); err != nil || !bytes.Equal(again, key) {
		t.Fatalf("reopen: %v", err)
	}
	creds := t.TempDir()
	served := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(filepath.Join(creds, backupCredentialKeyName), served, 0400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", creds)
	if got, err := backupCredentialKey(s, false); err != nil || !bytes.Equal(got, served) {
		t.Fatalf("service credential not used: %v", err)
	}
}
