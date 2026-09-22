// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func testVirshFenceConfig(t *testing.T, root string) protocol.StorageClusterConfig {
	t.Helper()
	p := filepath.Join(root, "storage-cluster", "fence-credentials", "lab-key")
	hosts := filepath.Join(root, "storage-cluster", "fence-credentials", "lab-hosts.known_hosts")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("private-key-material"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hosts, []byte("lab.example.test ssh-ed25519 example"), 0600); err != nil {
		t.Fatal(err)
	}
	fence := protocol.StorageClusterFence{Kind: protocol.FenceVirsh, Endpoint: "lab.example.test", VirshURI: "qemu+ssh://fence@lab.example.test:2222/system", SSHLogin: "fence", SSHPort: 2222, CredentialRef: "lab-key", SSHHostKeyRef: "lab-hosts"}
	return protocol.StorageClusterConfig{Generation: 7, Nodes: []protocol.StorageClusterNode{{HostID: "host-a", Fence: withFenceTarget(fence, "01234567-89ab-cdef-0123-456789abcdef")}, {HostID: "host-b", Fence: withFenceTarget(fence, "11234567-89ab-cdef-0123-456789abcdef")}, {HostID: "host-c", Fence: withFenceTarget(fence, "21234567-89ab-cdef-0123-456789abcdef")}}}
}

func withFenceTarget(f protocol.StorageClusterFence, target string) protocol.StorageClusterFence {
	f.TargetID = target
	return f
}

func TestVerifyStorageClusterFenceRequiresActualVirshPowerOff(t *testing.T) {
	oldRun := storageFenceRun
	defer func() { storageFenceRun = oldRun }()
	root := t.TempDir()
	c := testVirshFenceConfig(t, root)
	var calls [][]string
	storageFenceRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "ssh" {
			return []byte("Id: 2\nName: example\nUUID: 01234567-89ab-cdef-0123-456789abcdef\nState: shut off\n"), nil
		}
		if strings.Contains(strings.Join(args, " "), "--action=status") && len(calls) == 2 {
			return []byte("Status: ON\n"), nil
		}
		if strings.Contains(strings.Join(args, " "), "--action=status") {
			return []byte("Status: OFF\n"), errors.New("off status exit")
		}
		return nil, nil
	}
	proof, err := VerifyStorageClusterFence(context.Background(), root, c, "host-a")
	if err != nil || proof.Status != "passed" || !proof.ExclusionVerified || proof.ConfigGeneration != 7 || proof.ConfigDigest != c.Digest() {
		t.Fatalf("proof = %#v, %v", proof, err)
	}
	if len(calls) != 4 || calls[0][0] != "ssh" || calls[1][0] != "fence_virsh" || !strings.Contains(strings.Join(calls[1], " "), "--action=status") || !strings.Contains(strings.Join(calls[2], " "), "--action=off") || !strings.Contains(strings.Join(calls[2], " "), c.Nodes[0].Fence.TargetID) {
		t.Fatalf("unexpected native calls: %#v", calls)
	}
	if identity := strings.Join(calls[0], " "); !strings.Contains(identity, "virsh --connect qemu:///system dominfo") {
		t.Fatalf("identity check did not select the system daemon: %q", identity)
	}
	if args := strings.Join(calls[1], " "); !strings.Contains(args, "StrictHostKeyChecking=yes") || strings.Contains(args, "private-key-material") {
		t.Fatalf("un-pinned or leaked fence arguments: %q", args)
	}
	args := strings.Join(calls[1], " ")
	for _, want := range []string{"--username=fence", "--ssh", "--identity-file=", "--action=status", "PS1=\\\\[EXPECT\\\\]#"} {
		if !strings.Contains(args, want) {
			t.Fatalf("fence_virsh arguments missing %q: %q", want, args)
		}
	}
	if strings.Contains(args, "--login=") {
		t.Fatalf("unsupported fence_virsh login flag: %q", args)
	}
}

func TestVirshDOMInfoMatchesExactUUID(t *testing.T) {
	if !virshDOMInfoMatchesUUID([]byte("UUID: 01234567-89ab-cdef-0123-456789abcdef\n"), "01234567-89ab-cdef-0123-456789abcdef") {
		t.Fatal("matching dominfo UUID was refused")
	}
	if virshDOMInfoMatchesUUID([]byte("UUID: 11234567-89ab-cdef-0123-456789abcdef\n"), "01234567-89ab-cdef-0123-456789abcdef") {
		t.Fatal("mismatched dominfo UUID was accepted")
	}
}

func TestVerifyStorageClusterFenceRefusesMisdirectedAndIncapableProfiles(t *testing.T) {
	oldRun := storageFenceRun
	defer func() { storageFenceRun = oldRun }()
	called := false
	storageFenceRun = func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	root := t.TempDir()
	c := testVirshFenceConfig(t, root)
	if proof, err := VerifyStorageClusterFence(context.Background(), root, c, "host-x"); err == nil || proof.Status != "failed" || called {
		t.Fatalf("misdirected proof = %#v, %v, called=%v", proof, err, called)
	}
	c.Nodes[0].Fence = protocol.StorageClusterFence{Kind: protocol.FenceSCSIPR, DevicePath: "/dev/disk/by-id/lab-lun"}
	if proof, err := VerifyStorageClusterFence(context.Background(), root, c, "host-a"); err == nil || proof.Status != "failed" || called {
		t.Fatalf("unsupported proof = %#v, %v, called=%v", proof, err, called)
	}
	c = testVirshFenceConfig(t, root)
	c.Nodes[0].Fence.TargetID = "mutable-name"
	if proof, err := VerifyStorageClusterFence(context.Background(), root, c, "host-a"); err == nil || proof.Status != "failed" || called {
		t.Fatalf("mutable target proof = %#v, %v, called=%v", proof, err, called)
	}
}

func TestVerifyStorageClusterFenceRejectsFailedAndAmbiguousFence(t *testing.T) {
	oldRun := storageFenceRun
	defer func() { storageFenceRun = oldRun }()
	root := t.TempDir()
	c := testVirshFenceConfig(t, root)
	storageFenceRun = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("secret device output")
	}
	if proof, err := VerifyStorageClusterFence(context.Background(), root, c, "host-a"); err == nil || proof.Status != "failed" || strings.Contains(proof.Error, "secret") {
		t.Fatalf("failed proof = %#v, %v", proof, err)
	}
	storageFenceRun = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), "--action=") {
			return []byte("UUID: 01234567-89ab-cdef-0123-456789abcdef\n"), nil
		}
		if strings.Contains(strings.Join(args, " "), "--action=status") {
			return []byte("Status: ON\n"), nil
		}
		return nil, nil
	}
	if proof, err := VerifyStorageClusterFence(context.Background(), root, c, "host-a"); err == nil || proof.Status != "uncertain" || proof.ExclusionVerified {
		t.Fatalf("ambiguous proof = %#v, %v", proof, err)
	}
}

func TestFencePowerStateRejectsContradictoryOutput(t *testing.T) {
	if got := fencePowerState([]byte("Status: ON\nStatus: OFF\n")); got != "" {
		t.Fatalf("contradictory status = %q", got)
	}
}

func TestVerifyStorageClusterFenceRejectsBMCWithoutIdentityVerification(t *testing.T) {
	oldRun := storageFenceRun
	defer func() { storageFenceRun = oldRun }()
	called := false
	storageFenceRun = func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	fence := protocol.StorageClusterFence{Kind: protocol.FenceRedfish, Endpoint: "bmc.example.test", CredentialRef: "bmc-secret"}
	c := protocol.StorageClusterConfig{Generation: 1, Nodes: []protocol.StorageClusterNode{{HostID: "host-a", Fence: withFenceTarget(fence, "system-a")}, {HostID: "host-b", Fence: withFenceTarget(fence, "system-b")}, {HostID: "host-c", Fence: withFenceTarget(fence, "system-c")}}}
	if proof, err := VerifyStorageClusterFence(context.Background(), t.TempDir(), c, "host-a"); err == nil || proof.Status != "failed" || called {
		t.Fatalf("BMC proof = %#v, %v, called=%v", proof, err, called)
	}
}

// Every identity read and readiness check Core makes signs in over RMCP+ (ipmitool -I
// lanplus -C <suite>), so the power-off must too: fence_ipmilan defaults to IPMI 1.5, which
// a controller running 2.0 often refuses, and --cipher means nothing without --lanplus — the
// "checked from 2 hosts but cannot be fenced" failure, first reached at a promotion.
func TestIPMIFenceRunsOverTheSameRMCPSessionTheIdentityWasProvedOver(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "storage-cluster", "fence-credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bmc-secret"), FencePasswordScript("hunter2"), 0700); err != nil {
		t.Fatal(err)
	}
	fence := protocol.StorageClusterFence{Kind: protocol.FenceIPMI, Endpoint: "bmc.example.test", TargetID: "1",
		Username: "nexfence", Cipher: 17, CredentialRef: "bmc-secret"}
	agent, args, err := fenceAgentArgs(root, fence, "off")
	if err != nil || agent != "fence_ipmilan" {
		t.Fatalf("IPMI fence command = %q, %v", agent, err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--lanplus", "--cipher=17"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("IPMI fence arguments omit %q: %v", want, args)
		}
	}
	// Pacemaker fences through the same parameters, so the stonith resource renders the
	// valueless flag as lanplus=1 rather than refusing the argument.
	cib, err := fenceCIBCreateArgs(root, protocol.StorageClusterNode{HostID: "host-a", Fence: fence})
	if err != nil {
		t.Fatalf("CIB arguments for an IPMI fence device: %v", err)
	}
	if !strings.Contains(strings.Join(cib, "\n"), "lanplus=1") {
		t.Fatalf("stonith resource omits lanplus=1: %v", cib)
	}
}
