// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// A pool fence proof is refused for every profile that cannot independently prove identity
// and exclusion, and for every binding that is not this promotion's. No fence agent runs.
func TestVerifyPoolHostFenceRefusesUnsupportedAndUnconfiguredProfiles(t *testing.T) {
	cases := []struct {
		name  string
		fence *protocol.StorageClusterFence
		want  string
	}{
		{"no recorded profile", nil, "no recorded fence profile"},
		{"a BMC profile with no account to sign in with", &protocol.StorageClusterFence{
			Kind: protocol.FenceRedfish, Endpoint: "bmc.example.test", TargetID: "system-1", CredentialRef: "cred-1",
		}, "needs the account Nex Core signs in with"},
		{"a BMC profile for a host with no unique hardware identity", &protocol.StorageClusterFence{
			Kind: protocol.FenceIPMI, Endpoint: "bmc.example.test", TargetID: "plug-1", Username: "nex-fence", CredentialRef: "cred-1",
		}, "does not report a unique hardware identity"},
		{"SCSI persistent reservations are unsupported", &protocol.StorageClusterFence{
			Kind: protocol.FenceSCSIPR, DevicePath: "/dev/disk/by-id/scsi-lab",
		}, "SCSI persistent-reservation fencing is unsupported"},
		{"an invalid profile is refused before anything runs", &protocol.StorageClusterFence{Kind: "telnet"}, "configured fence target was refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proof, err := VerifyPoolHostFence(context.Background(), t.TempDir(), tc.fence, "", "master-1", "op-1", "digest-1", 3)
			if err == nil {
				t.Fatal("an unsupported or unconfigured fence profile produced a proof")
			}
			if proof.Status == "passed" || proof.ExclusionVerified {
				t.Fatalf("proof = %#v", proof)
			}
			if !strings.Contains(proof.Error, tc.want) {
				t.Fatalf("proof error = %q, want it to mention %q", proof.Error, tc.want)
			}
		})
	}
}

// A proof must be bound to a host, an operation and a generation before the fence is even
// attempted: an unbound proof could be presented at another promotion.
func TestVerifyPoolHostFenceRequiresItsBinding(t *testing.T) {
	fence := &protocol.StorageClusterFence{
		Kind: protocol.FenceVirsh, Endpoint: "fence.example.test", TargetID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		CredentialRef: "cred-1", VirshURI: "qemu:///system", SSHLogin: "fence", SSHPort: 22, SSHHostKeyRef: "key-1",
	}
	if _, err := VerifyPoolHostFence(context.Background(), t.TempDir(), fence, "", "master-1", "", "digest-1", 3); err == nil {
		t.Fatal("an unbound fence proof was produced")
	}
	if _, err := VerifyPoolHostFence(context.Background(), t.TempDir(), fence, "", "master-1", "op-1", "digest-1", 0); err == nil {
		t.Fatal("a fence proof was produced for no authority generation")
	}
}

// The libvirt start hook is the last gate a direct virsh start or a libvirt autostart passes.
func TestPoolHostRestrictedRefusesManagedSharedStart(t *testing.T) {
	dir := t.TempDir()
	if err := poolHostRestricted(dir); err != nil {
		t.Fatalf("a host that is in no pool was refused: %v", err)
	}
	state := map[string]any{"Restricted": map[string]any{"Reason": "this host's pool authority was superseded"}}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "pool-peer.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	err = poolHostRestricted(dir)
	if err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("restricted refusal = %v", err)
	}
	// A record that cannot be read at all fails closed.
	if err = os.WriteFile(filepath.Join(dir, "pool-peer.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = poolHostRestricted(dir); err == nil {
		t.Fatal("an unreadable pool authority record allowed a managed shared start")
	}
	// An unrestricted host is unaffected.
	if err = os.WriteFile(filepath.Join(dir, "pool-peer.json"), []byte(`{"Membership":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = poolHostRestricted(dir); err != nil {
		t.Fatalf("an unrestricted host was refused: %v", err)
	}
}

// F1. The file this writes is mode 0700 and both fence_ipmilan (--password-script=) and
// Pacemaker (password_script) execute it as root. The bytes arrive unsealed from another
// host's authority replica, so a compromised or malicious pool master could otherwise seal
// arbitrary shell into a member and have it run during a promotion. Only bytes the one
// producer wrote may reach that path.
// (Reported by independent review of slice 4a; regression added after slice 4b's review.)
func TestOnlyProducerFormatFenceCredentialBytesAreWritten(t *testing.T) {
	dir := t.TempDir()
	fence := protocol.StorageClusterFence{Kind: protocol.FenceIPMI, CredentialRef: "cred-1"}
	path := filepath.Join(dir, "storage-cluster", "fence-credentials", "cred-1")
	for name, payload := range map[string][]byte{
		"a shell script somebody else wrote": []byte("#!/bin/sh\nid > /tmp/pwned\n"),
		"a command before the printf":        []byte("#!/bin/sh\nid\nprintf '%s\\n' 'hunter2'\n"),
		"an unterminated value":              []byte("#!/bin/sh\nprintf '%s\\n' 'hunter2\n"),
		"an empty file":                      {},
	} {
		t.Run(name, func(t *testing.T) {
			err := WritePoolFenceCredential(dir, fence, map[string][]byte{"": payload})
			if err == nil {
				t.Fatal("bytes that are not what Nex Core writes were accepted into an executable file")
			}
			if !strings.Contains(err.Error(), "not in the format Nex Core writes") {
				t.Fatalf("refused for the wrong reason: %v", err)
			}
			if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
				t.Fatalf("a refused credential was written anyway: %v", statErr)
			}
		})
	}
	// What the one producer wrote is accepted, so the refusal is about the format and not
	// about refusing everything.
	if err := WritePoolFenceCredential(dir, fence, map[string][]byte{"": FencePasswordScript("hunter2")}); err != nil {
		t.Fatalf("a credential Nex Core produced was refused: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("an accepted credential was not written: %v", err)
	}
}

// F2. fence_ipmilan runs the password script and strips the whitespace around what it printed,
// so a password with a leading or trailing space authenticates every check Nex Core makes
// itself — the host reads "checked from 2 hosts" — and fails the one power-off that matters.
// Core refuses it while the operator is still there to correct it.
// (Reported by independent review of slice 4a; regression added after slice 4b's review.)
func TestAFencePasswordAFenceAgentWouldAlterIsRefused(t *testing.T) {
	for _, secret := range []string{"hunter2 ", " hunter2", "hunter2\t", "\thunter2", "hunter2\v", ""} {
		if err := FenceSecretUsable(secret); err == nil {
			t.Fatalf("a password a fence agent would alter was accepted: %q", secret)
		}
	}
	for _, secret := range []string{"hunter2", "hun ter2", "p@ss'w\"ord", "a"} {
		if err := FenceSecretUsable(secret); err != nil {
			t.Fatalf("a usable password was refused: %q: %v", secret, err)
		}
	}
}
