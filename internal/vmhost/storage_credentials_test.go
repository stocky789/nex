// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func testCredentialHandoff() protocol.StorageCredentialHandoff {
	return protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{Handle: "opaque", RequestID: "credential-1", RegistrationRequestID: "storage-register-1", PoolID: "pool-1", HostID: "host-1", ExpiresAt: time.Now().Add(time.Minute)}, Credentials: protocol.StorageCredentials{Username: "operator", Secret: "not-in-a-command"}}
}

func TestStorageCredentialReceiptIsDurableAndScoped(t *testing.T) {
	state := t.TempDir()
	handoff := testCredentialHandoff()
	first, err := SaveStorageCredential(state, handoff)
	if err != nil || first.Receipt == "" || first.RepositoryID != "" {
		t.Fatalf("first receipt %#v: %v", first, err)
	}
	second, err := SaveStorageCredential(state, handoff)
	if err != nil || second.Receipt != first.Receipt {
		t.Fatalf("replay receipt %#v: %v", second, err)
	}
	changed := handoff
	changed.Credentials.Secret = "different"
	if _, err = SaveStorageCredential(state, changed); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	if err = credentialReceiptValid(state, first.Receipt, "repo-1", "pool-1", "host-1"); err == nil {
		t.Fatal("unbound receipt accepted")
	}
	if err = bindCredentialReceipt(state, first.Receipt, "repo-1", "pool-1", "host-1", "storage-register-1"); err != nil {
		t.Fatal(err)
	}
	if err = credentialReceiptValid(state, first.Receipt, "repo-1", "pool-1", "host-1"); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(state, credentialDirectory, first.Receipt)); err != nil {
		t.Fatal(err)
	}
	if err = bindCredentialReceipt(state, first.Receipt, "repo-1", "pool-1", "host-1", "storage-register-1"); err != nil {
		t.Fatal(err)
	}
	if err = bindCredentialReceipt(state, first.Receipt, "repo-2", "pool-1", "host-1", "storage-register-1"); err == nil {
		t.Fatal("receipt moved between repositories")
	}
	if err = retireStorageCredential(state, first.Receipt, "pool-1", "host-1", true); err == nil {
		t.Fatal("attached receipt retired")
	}
	if err = retireStorageCredential(state, first.Receipt, "other-pool", "host-1", false); err == nil {
		t.Fatal("cross-pool receipt retirement accepted")
	}
	if err = retireStorageCredential(state, first.Receipt, "pool-1", "other-host", false); err == nil {
		t.Fatal("cross-host receipt retirement accepted")
	}
	b, err := os.ReadFile(credentialPath(state, handoff.Offer.RequestID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), handoff.Credentials.Secret) {
		t.Fatal("credential was not persisted on Core")
	}
	mountBody, err := os.ReadFile(filepath.Join(state, credentialDirectory, first.Receipt))
	if err != nil || !strings.Contains(string(mountBody), handoff.Credentials.Secret) {
		t.Fatalf("native credential materialization %q: %v", mountBody, err)
	}
	info, err := os.Stat(filepath.Join(state, credentialDirectory))
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("credential directory permissions %v: %v", info.Mode(), err)
	}
	if err = retireStorageCredential(state, first.Receipt, "pool-1", "host-1", false); err != nil {
		t.Fatal(err)
	}
	retired, err := os.ReadFile(credentialPath(state, handoff.Offer.RequestID))
	if err != nil || strings.Contains(string(retired), handoff.Credentials.Secret) || !strings.Contains(string(retired), `"status":"retired"`) {
		t.Fatalf("receipt retirement did not retain a redacted tombstone %q: %v", retired, err)
	}
	if _, err = SaveStorageCredential(state, handoff); err == nil {
		t.Fatal("retired receipt accepted a replay")
	}
	if _, err = os.Stat(filepath.Join(state, credentialDirectory, first.Receipt)); !os.IsNotExist(err) {
		t.Fatalf("mount credential remains: %v", err)
	}
	if err = retireStorageCredential(state, first.Receipt, "pool-1", "host-1", false); err != nil {
		t.Fatalf("retirement replay: %v", err)
	}
}

func TestStorageCredentialRejectsInvalidScope(t *testing.T) {
	handoff := testCredentialHandoff()
	handoff.Offer.HostID = ""
	if _, err := SaveStorageCredential(t.TempDir(), handoff); err == nil {
		t.Fatal("hostless handoff accepted")
	}
}
