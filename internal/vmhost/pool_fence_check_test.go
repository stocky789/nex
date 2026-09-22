// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// withIPMIGUID makes an IPMI controller answer Get System GUID for one machine, the way
// the identity tests do, so a check can reach the power-status read behind it.
func withIPMIGUID(t *testing.T, uuid string, ran *[]string) {
	t.Helper()
	fakeCommand(t, "ipmitool", "exit 0")
	previous := storageFenceEnvRun
	storageFenceEnvRun = func(_ context.Context, _ []string, _, name string, args ...string) ([]byte, error) {
		*ran = append(*ran, name+" "+strings.Join(args, " "))
		if len(args) > 0 && args[len(args)-1] == "status" {
			return []byte("Chassis Power is on\n"), nil
		}
		return []byte("System GUID  : " + uuid + "\n"), nil
	}
	t.Cleanup(func() { storageFenceEnvRun = previous })
}

// The whole safety claim of one check, asserted structurally over both protocols: a
// controller that would refuse nothing still gets no request that could change its machine's
// power. The same property over a whole pass, past a laboratory virsh device, is
// TestAWholeCheckingPassIssuesOnlyReads in internal/agent. A check runs against every host in
// the pool every few minutes, so this is the one property that must not be able to rot.
func TestAContinuousCheckCannotPowerAnythingOff(t *testing.T) {
	posts := []string{}
	lab := newBMCLab(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			posts = append(posts, r.Method+" "+r.URL.Path)
		}
		labRedfishSystem(labSystemUUID)(w, r)
	})
	result, detail, power := CheckPoolFenceTarget(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "lab-secret")
	if result != protocol.PoolFenceCheckOK || detail != "" {
		t.Fatalf("check = %q %q", result, detail)
	}
	if power != "on" {
		t.Fatalf("the check did not read the machine's power state: %q", power)
	}
	if len(posts) != 0 {
		t.Fatalf("a non-destructive check sent a state-changing request: %v", posts)
	}

	// The same for IPMI, which asks its own controller over the session the identity was
	// proved on rather than over HTTP.
	profile := lab.fence
	profile.Kind, profile.Endpoint, profile.TargetID, profile.TLSFingerprint = protocol.FenceIPMI, "192.0.2.11", "1", ""
	issued := []string{}
	withIPMIGUID(t, labSystemUUID, &issued)
	result, detail, power = CheckPoolFenceTarget(context.Background(), lab.stateDir, &profile, labSystemUUID, "lab-secret")
	if result != protocol.PoolFenceCheckOK || power != "on" {
		t.Fatalf("IPMI check = %q %q %q", result, detail, power)
	}
	// The IPMI read goes through ipmitool on the session the identity was proved over, so
	// no fence agent runs at all — and a coarse search for "off" is not the assertion: an
	// address or an account may contain it.
	if len(*lab.ran) != 0 {
		t.Fatalf("a check ran a fence agent: %v", *lab.ran)
	}
	for _, ran := range issued {
		if !strings.HasSuffix(ran, "mc guid") && !strings.HasSuffix(ran, "chassis power status") {
			t.Fatalf("a check issued something other than a read: %q", ran)
		}
	}
}

// Every way a fence device can be broken has to arrive as its own reason, because the reason
// is what an operator acts on. A wrong machine and a changed certificate in particular must
// never read as "not reachable": they are not network faults.
func TestAContinuousCheckNamesWhyAFenceDeviceWouldNotWork(t *testing.T) {
	t.Run("identity mismatch", func(t *testing.T) {
		lab := newBMCLab(t, labRedfishSystem("5b5b5655-0148-4921-9168-c5d15f546442"))
		if result, _, _ := CheckPoolFenceTarget(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "lab-secret"); result != protocol.PoolFenceCheckIdentity {
			t.Fatalf("result = %q", result)
		}
	})
	t.Run("credentials rejected", func(t *testing.T) {
		lab := newBMCLab(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusUnauthorized) })
		if result, _, _ := CheckPoolFenceTarget(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "lab-secret"); result != protocol.PoolFenceCheckRejected {
			t.Fatalf("result = %q", result)
		}
	})
	t.Run("certificate changed", func(t *testing.T) {
		lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
		lab.fence.TLSFingerprint = strings.Repeat("ab", 32)
		if result, _, _ := CheckPoolFenceTarget(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "lab-secret"); result != protocol.PoolFenceCheckCertificate {
			t.Fatalf("result = %q", result)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
		lab.server.Close()
		if result, _, _ := CheckPoolFenceTarget(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "lab-secret"); result != protocol.PoolFenceCheckUnreachable {
			t.Fatalf("result = %q", result)
		}
	})
	t.Run("no profile", func(t *testing.T) {
		if result, _, _ := CheckPoolFenceTarget(context.Background(), t.TempDir(), nil, labSystemUUID, "lab-secret"); result != protocol.PoolFenceCheckNoProfile {
			t.Fatalf("result = %q", result)
		}
	})
	t.Run("no hardware identity", func(t *testing.T) {
		lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
		if result, _, _ := CheckPoolFenceTarget(context.Background(), lab.stateDir, &lab.fence, "", "lab-secret"); result != protocol.PoolFenceCheckUnusable {
			t.Fatalf("result = %q", result)
		}
	})
	t.Run("a lab virsh device is not checked over the network", func(t *testing.T) {
		lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
		virsh := protocol.StorageClusterFence{Kind: protocol.FenceVirsh, Endpoint: "lab.example.test",
			TargetID: labSystemUUID, VirshURI: "qemu:///system", SSHLogin: "fence", SSHPort: 22,
			CredentialRef: "cred-1", SSHHostKeyRef: "cred-1"}
		if result, _, _ := CheckPoolFenceTarget(context.Background(), lab.stateDir, &virsh, labSystemUUID, "lab-secret"); result != protocol.PoolFenceCheckUnusable {
			t.Fatalf("result = %q", result)
		}
	})
}

// A fence credential is data, and the one place it comes from that another host wrote is a
// member's own authority replica, opened on every host every few minutes with nobody present.
// Executing those bytes would turn a compromised pool master into root on every member, so
// the format is parsed and anything that is not that format is refused rather than run.
// (Reported by independent review.)
func TestAFenceCredentialIsParsedAndNeverExecuted(t *testing.T) {
	for _, secret := range []string{"lab-secret", "it's \"quoted\"", "$(id) `id` ${HOME}"} {
		got, err := FenceCredentialSecretFromScript(FencePasswordScript(secret))
		if err != nil || got != secret {
			t.Fatalf("round trip of %q = %q, %v", secret, got, err)
		}
	}
	// Each of these produces a password when run through a shell and nothing when parsed.
	for name, script := range map[string]string{
		"a command in place of the password": "#!/bin/sh\nid > /tmp/pwned; printf '%s\\n' 'secret'\n",
		"a shell that only prints":           "#!/bin/sh\necho secret\n",
		"an unterminated value":              "#!/bin/sh\nprintf '%s\\n' 'secret\n",
		"an empty file":                      "",
	} {
		if got, err := FenceCredentialSecretFromScript([]byte(script)); err == nil {
			t.Fatalf("%s was accepted as a fence credential and returned %q", name, got)
		}
	}
}
