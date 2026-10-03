// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestOrdinaryStorageCredentialRetirementSkipsBackupStore(t *testing.T) {
	s := seedBackupMaster(t)
	if handled, err := RetireUnusedBackupCredential(s, "ordinary-receipt", s.State.PoolID, s.State.HostID); handled || err != nil {
		t.Fatalf("ordinary receipt intercepted: %t, %v", handled, err)
	}
	if _, err := os.Lstat(filepath.Join(s.dir, clusterSecretDirectory, "backup")); !os.IsNotExist(err) {
		t.Fatalf("ordinary retirement created backup store: %v", err)
	}
}

func TestUnusedBackupCredentialRetirementKeepsReplayTombstone(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret"}}
	receipt, err := saveBackupCredential(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := BackupCredentialReceipts(s)
	if err != nil || len(listed) != 1 || !listed[0].RetireEligible || listed[0].Receipt != receipt.Receipt {
		t.Fatalf("unused handoff inventory: %+v, %v", listed, err)
	}
	if handled, err := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, "wrong-host"); handled || err == nil {
		t.Fatal("wrong host retired backup credential")
	}
	if handled, err := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, s.State.HostID); !handled || err != nil {
		t.Fatalf("retire: %t, %v", handled, err)
	}
	if handled, err := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, s.State.HostID); !handled || err != nil {
		t.Fatalf("retire replay: %t, %v", handled, err)
	}
	path, err := backupCredentialPath(s, handoff.Offer.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), handoff.Credentials.Secret) {
		t.Fatalf("retired receipt retained secret: %v", err)
	}
	if _, err := saveBackupCredential(s, handoff); err == nil {
		t.Fatal("exact handoff replay revived retired credential")
	}
	if handled, err := RetireUnusedBackupCredential(s, "unrelated-receipt", s.State.PoolID, s.State.HostID); handled || err != nil {
		t.Fatalf("ordinary storage receipt intercepted: %t, %v", handled, err)
	}
	listed, err = BackupCredentialReceipts(s)
	if err != nil || len(listed) != 1 || listed[0].Status != "retired" || listed[0].RetireEligible {
		t.Fatalf("retired receipt inventory: %+v, %v", listed, err)
	}
}

func TestBackupCredentialRetirementRejectsCommittedAndUnresolvedRegistration(t *testing.T) {
	for _, status := range []string{"committed", "pending", "needs_attention", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			s := seedBackupMaster(t)
			handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
				RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
				PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
			}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret"}}
			receipt, err := saveBackupCredential(s, handoff)
			if err != nil {
				t.Fatal(err)
			}
			repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindS3,
				CredentialRef: receipt.Receipt, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups"}}
			if status == "committed" {
				if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
					t.Fatal(err)
				}
			} else {
				cmd := protocol.Command{OperationID: "register-1", Kind: "backup-repository-register", BackupRepository: &repo}
				s.State.Journal[cmd.OperationID] = JournalEntry{Command: &cmd, Status: status}
			}
			listed, err := BackupCredentialReceipts(s)
			if err != nil || len(listed) != 1 || listed[0].RetireEligible {
				t.Fatalf("unsafe receipt eligible: %+v, %v", listed, err)
			}
			if handled, err := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, s.State.HostID); !handled || err == nil {
				t.Fatalf("unsafe retirement accepted: %t, %v", handled, err)
			}
			if _, err := ResolveBackupCredentialForRegistration(s, repo, "register-1"); err != nil {
				t.Fatalf("refused retirement changed material: %v", err)
			}
		})
	}
}

func TestFailedBackupRegistrationCredentialMayBeRetired(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret"}}
	receipt, err := saveBackupCredential(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindS3,
		CredentialRef: receipt.Receipt, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups"}}
	cmd := protocol.Command{OperationID: "register-1", Kind: "backup-repository-register", BackupRepository: &repo}
	s.State.Journal[cmd.OperationID] = JournalEntry{Command: &cmd, Status: "failed"}
	listed, err := BackupCredentialReceipts(s)
	if err != nil || len(listed) != 1 || !listed[0].RetireEligible {
		t.Fatalf("failed registration inventory: %+v, %v", listed, err)
	}
	if handled, err := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, s.State.HostID); !handled || err != nil {
		t.Fatalf("failed registration retirement: %t, %v", handled, err)
	}
}

func TestPendingBackupMetadataReceiptBlocksCredentialRetirement(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret"}}
	receipt, err := saveBackupCredential(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	metadata := protocol.BackupMetadataReceipt{OperationID: "register-1", Kind: "backup-repository-register",
		IntentSHA256: strings.Repeat("a", 64), Status: "pending", ObservedAt: time.Now().UTC()}
	if err := savePoolBackupMetadataReceipt(s, metadata); err != nil {
		t.Fatal(err)
	}
	listed, err := BackupCredentialReceipts(s)
	if err != nil || len(listed) != 1 || listed[0].RetireEligible {
		t.Fatalf("pending metadata receipt eligible: %+v, %v", listed, err)
	}
	if handled, err := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, s.State.HostID); !handled || err == nil {
		t.Fatalf("pending metadata retirement accepted: %t, %v", handled, err)
	}
}

func TestBackupCredentialRetirementWaitsForPreparation(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:smb3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret"}}
	receipt, err := saveBackupCredential(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	finish := beginBackupCredentialPreparation(s, receipt.Receipt)
	listed, err := BackupCredentialReceipts(s)
	if err != nil || len(listed) != 1 || listed[0].RetireEligible {
		t.Fatalf("in-flight receipt eligible: %+v, %v", listed, err)
	}
	if handled, err := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, s.State.HostID); !handled || err == nil {
		t.Fatalf("in-flight retirement accepted: %t, %v", handled, err)
	}
	finish()
	if handled, err := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, s.State.HostID); !handled || err != nil {
		t.Fatalf("SMB retirement: %t, %v", handled, err)
	}
	if _, err := ResolveBackupCredentialForAdoption(s, protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Kind: protocol.BackupRepoKindSMB3, CredentialRef: receipt.Receipt,
		SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"}, Name: "Backup"}); err == nil {
		t.Fatal("retired receipt kept its material")
	}
}
