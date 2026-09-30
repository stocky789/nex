// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost/vmhosttest"
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
	if err != nil || strings.Contains(string(b), handoff.Credentials.Secret) {
		t.Fatalf("receipt must carry no secret: %q, %v", b, err)
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

func TestStorageCredentialMountFileIncludesDomain(t *testing.T) {
	state := t.TempDir()
	handoff := testCredentialHandoff()
	handoff.Credentials.Domain = "truenas"
	receipt, err := SaveStorageCredential(state, handoff)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := os.ReadFile(filepath.Join(state, credentialDirectory, receipt.Receipt))
	if err != nil {
		t.Fatal(err)
	}
	if string(mount) != "username=operator\npassword=not-in-a-command\ndomain=truenas\n" {
		t.Fatalf("mount credential %q", mount)
	}
	replay := handoff
	replay.Credentials.Domain = "other"
	if _, err = SaveStorageCredential(state, replay); err == nil {
		t.Fatal("domain change accepted as the same receipt")
	}
}

func TestStorageCredentialRejectsDomainWithEquals(t *testing.T) {
	handoff := testCredentialHandoff()
	handoff.Credentials.Domain = "tru=enas"
	if _, err := SaveStorageCredential(t.TempDir(), handoff); err == nil {
		t.Fatal("unsafe domain accepted")
	}
}

// This fails before BUG-0170 because an identical replay always atomically
// rewrites the already-correct mount credential.
func TestStorageCredentialReplayDoesNotRewriteVerifiedMountCredential(t *testing.T) {
	state := t.TempDir()
	handoff := testCredentialHandoff()
	first, err := SaveStorageCredential(state, handoff)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, credentialDirectory, first.Receipt)
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SaveStorageCredential(state, handoff)
	if err != nil || second.Receipt != first.Receipt {
		t.Fatalf("replay receipt = %#v, %v", second, err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, beforeOK := before.Sys().(*syscall.Stat_t)
	afterStat, afterOK := after.Sys().(*syscall.Stat_t)
	if !beforeOK || !afterOK || beforeStat.Ino != afterStat.Ino {
		t.Fatal("matching replay rewrote the mount credential")
	}
}

func TestStorageCredentialReplayRepairsMountCredentialArtifacts(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string, []byte)
		fails bool
	}{
		{
			name: "missing",
			setup: func(t *testing.T, path string, _ []byte) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt",
			setup: func(t *testing.T, path string, _ []byte) {
				t.Helper()
				if err := os.WriteFile(path, []byte("corrupt\n"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mode",
			setup: func(t *testing.T, path string, _ []byte) {
				t.Helper()
				if err := os.Chmod(path, 0640); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "special-mode",
			setup: func(t *testing.T, path string, _ []byte) {
				t.Helper()
				if err := os.Chmod(path, 04600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, path string, _ []byte) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("outside\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				if got, err := os.ReadFile(target); err != nil || string(got) != "outside\n" {
					t.Fatalf("symlink target changed before replay: %q, %v", got, err)
				}
			},
		},
		{
			name: "hardlink",
			setup: func(t *testing.T, path string, want []byte) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, want, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(outside, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory-error",
			setup: func(t *testing.T, path string, _ []byte) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			},
			fails: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := t.TempDir()
			handoff := testCredentialHandoff()
			receipt, err := SaveStorageCredential(state, handoff)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(state, credentialDirectory, receipt.Receipt)
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			test.setup(t, path, want)
			_, err = SaveStorageCredential(state, handoff)
			if test.fails {
				if err == nil {
					t.Fatal("unsafe directory was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode() != 0600 {
				t.Fatalf("repaired credential = %v, %v", info, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(want) {
				t.Fatalf("repaired bytes = %q, %v", got, err)
			}
		})
	}
}

func TestStorageCredentialReplayRepairsWrongOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing credential ownership requires root")
	}
	state := t.TempDir()
	handoff := testCredentialHandoff()
	receipt, err := SaveStorageCredential(state, handoff)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, credentialDirectory, receipt.Receipt)
	if err := os.Chown(path, 1, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveStorageCredential(state, handoff); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode() != 0600 {
		t.Fatalf("repaired owner/mode = %#v", info)
	}
}

// systemd 257 fills no credentials directory for .mount units, so the unit names a root-only
// credentials file instead of loading a credential; the secret never appears in the unit.
func TestStorageCredentialIsRootOnlyFileNamedByMountUnit(t *testing.T) {
	stateDir := t.TempDir()
	handoff := protocol.StorageCredentialHandoff{
		Offer:       protocol.StorageCredentialOffer{RequestID: "req-1", RegistrationRequestID: "req-1", PoolID: "pool-1", HostID: "host-1"},
		Credentials: protocol.StorageCredentials{Username: "operator", Secret: "private-example-secret", Domain: "EXAMPLE"},
	}
	receipt, err := SaveStorageCredential(stateDir, handoff)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(stateDir, credentialDirectory, receipt.Receipt)
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credentials file: %v, %v", info, err)
	}
	// A retried handoff finds the same receipt; a different secret for it is refused.
	if again, err := SaveStorageCredential(stateDir, handoff); err != nil || again.Receipt != receipt.Receipt {
		t.Fatalf("replay: %+v, %v", again, err)
	}
	handoff.Credentials.Secret = "another-secret"
	if _, err := SaveStorageCredential(stateDir, handoff); err == nil {
		t.Fatal("replay with a different secret was accepted")
	}
	got, err := PoolStorageCredential(stateDir, receipt.Receipt)
	if err != nil || got.Username != "operator" || got.Secret != "private-example-secret" || got.Domain != "EXAMPLE" {
		t.Fatalf("pool credential: %+v, %v", got, err)
	}
	unit := string(remoteUnit("//smb.example.test/vms", "/var/lib/nex-storage/vms", "cifs", smbMountOptions("1000", "1000", file)))
	if !strings.Contains(unit, ",credentials="+file+",") || strings.Contains(unit, "LoadCredential") || strings.Contains(unit, "private-example-secret") {
		t.Errorf("unit does not just name the credentials file:\n%s", unit)
	}
}

// Regression: systemd 257 (Debian 13) has no --no-ask-password; the fake rejects it like the real tool.
func TestSealCredentialUsesOnlyOptionsSystemd257Accepts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.cred")
	if err := SealCredential(context.Background(), "nex-test", path, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenCredential(context.Background(), "nex-test", path); err != nil || string(got) != "secret" {
		t.Fatalf("open: %q, %v", got, err)
	}
	if _, err := vmhosttest.FakeSystemdCreds(context.Background(), nil, "systemd-creds", "encrypt", "-", "-"); err == nil {
		t.Fatal("fake accepts an option systemd 257 rejects")
	}
}

func TestRealSystemdCredsIsRefusedInTests(t *testing.T) {
	if _, err := realSystemdCreds(context.Background(), nil, "systemd-creds", "encrypt", "-", "-"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("real systemd-creds was not refused: %v", err)
	}
}

func TestStorageCredentialDecryptFailureIsRefused(t *testing.T) {
	stateDir := t.TempDir()
	handoff := protocol.StorageCredentialHandoff{
		Offer:       protocol.StorageCredentialOffer{RequestID: "req-1", RegistrationRequestID: "req-1", PoolID: "pool-1", HostID: "host-1"},
		Credentials: protocol.StorageCredentials{Username: "operator", Secret: "private-example-secret"},
	}
	receipt, err := SaveStorageCredential(stateDir, handoff)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, credentialDirectory, receipt.Receipt), []byte("garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := PoolStorageCredential(stateDir, receipt.Receipt); err == nil {
		t.Fatalf("undecryptable credential was read: %+v", got)
	}
}
