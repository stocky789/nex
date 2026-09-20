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
		{"BMC identity verification is not implemented", &protocol.StorageClusterFence{
			Kind: protocol.FenceRedfish, Endpoint: "bmc.example.test", TargetID: "system-1", CredentialRef: "cred-1",
		}, "BMC fencing identity verification is not implemented"},
		{"IPMI is refused for the same reason", &protocol.StorageClusterFence{
			Kind: protocol.FenceIPMI, Endpoint: "bmc.example.test", TargetID: "plug-1", CredentialRef: "cred-1",
		}, "BMC fencing identity verification is not implemented"},
		{"SCSI persistent reservations are unsupported", &protocol.StorageClusterFence{
			Kind: protocol.FenceSCSIPR, DevicePath: "/dev/disk/by-id/scsi-lab",
		}, "SCSI persistent-reservation fencing is unsupported"},
		{"an invalid profile is refused before anything runs", &protocol.StorageClusterFence{Kind: "telnet"}, "configured fence target was refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proof, err := VerifyPoolHostFence(context.Background(), t.TempDir(), tc.fence, "master-1", "op-1", "digest-1", 3)
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
	if _, err := VerifyPoolHostFence(context.Background(), t.TempDir(), fence, "master-1", "", "digest-1", 3); err == nil {
		t.Fatal("an unbound fence proof was produced")
	}
	if _, err := VerifyPoolHostFence(context.Background(), t.TempDir(), fence, "master-1", "op-1", "digest-1", 0); err == nil {
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
