// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

const labSystemUUID = "4c4c4544-0037-3810-8057-b4c04f435331"

// bmcLab stands up a Redfish endpoint and the private credential the fence profile names,
// and records every fence agent this host would have run. Nothing real is ever powered off.
type bmcLab struct {
	stateDir string
	server   *httptest.Server
	fence    protocol.StorageClusterFence
	ran      *[]string
}

func newBMCLab(t *testing.T, handler http.HandlerFunc) bmcLab {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	credentials := filepath.Join(stateDir, "storage-cluster", "fence-credentials")
	if err = os.MkdirAll(credentials, 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(credentials, "cred-1")
	if err = os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' 'lab-secret'\n"), 0700); err != nil {
		t.Fatal(err)
	}

	ran := []string{}
	previousRun := storageFenceRun
	storageFenceRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == script {
			return []byte("lab-secret\n"), nil
		}
		ran = append(ran, name+" "+strings.Join(args, " "))
		return []byte("Status: off\n"), nil
	}
	t.Cleanup(func() { storageFenceRun = previousRun })

	// The profile is pinned to the exact certificate this endpoint presents, as a recorded
	// profile always is.
	pin := sha256.Sum256(server.Certificate().Raw)
	return bmcLab{
		stateDir: stateDir,
		server:   server,
		fence: protocol.StorageClusterFence{
			Kind: protocol.FenceRedfish, Endpoint: endpoint.Host, TargetID: "system-1",
			Username: "nex-fence", TLSFingerprint: hex.EncodeToString(pin[:]), CredentialRef: "cred-1",
		},
		ran: &ran,
	}
}

// redfishSystem is a controller that answers for one machine and really powers it off: the
// PowerState it reports afterwards is what the separate observed-off read sees.
func labRedfishSystem(uuid string) http.HandlerFunc {
	power := "On"
	return func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "nex-fence" || password != "lab-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/redfish/v1/Systems/system-1/Actions/ComputerSystem.Reset":
			var action struct{ ResetType string }
			_ = json.NewDecoder(r.Body).Decode(&action)
			if r.Method != http.MethodPost || action.ResetType != "ForceOff" {
				http.Error(w, "bad reset", http.StatusBadRequest)
				return
			}
			power = "Off"
			w.WriteHeader(http.StatusNoContent)
		case "/redfish/v1/Systems/system-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"system-1","UUID":"` + uuid + `","PowerState":"` + power + `"}`))
		default:
			http.NotFound(w, r)
		}
	}
}

// The whole point of the identity check: a management controller that answers for another
// machine is never powered off, whatever it reports about its power state.
func TestBMCFenceRefusesAMachineThatIsNotTheRecordedHost(t *testing.T) {
	lab := newBMCLab(t, labRedfishSystem("ffffffff-1111-4111-8111-111111111111"))
	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err == nil {
		t.Fatal("a management controller reporting another machine produced a proof")
	}
	if proof.Status != "failed" || proof.ExclusionVerified {
		t.Fatalf("proof = %#v", proof)
	}
	if !strings.Contains(proof.Error, "reports a different machine") {
		t.Fatalf("proof error = %q", proof.Error)
	}
	if len(*lab.ran) != 0 {
		t.Fatalf("a fence agent ran against the wrong machine: %v", *lab.ran)
	}
}

func TestBMCFencePowersOffOnlyTheRecordedMachine(t *testing.T) {
	lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err != nil {
		t.Fatalf("the recorded machine was not fenced: %v (%#v)", err, proof)
	}
	if proof.Status != "passed" || !proof.ExclusionVerified {
		t.Fatalf("proof = %#v", proof)
	}
	// Redfish never goes through fence_redfish: the agent cannot be bound to the pinned
	// certificate, so Core does the power-off itself.
	if len(*lab.ran) != 0 {
		t.Fatalf("a fence agent ran for a pinned Redfish controller: %v", *lab.ran)
	}
}

// labSettlingRedfishSystem is the controller a real rack has: ComputerSystem.Reset is
// accepted and answered straight away, and the machine goes on reporting "On" for a while
// afterwards. refuseReset makes it answer 409 to the Reset as several vendors do for a
// machine that is already off.
func labSettlingRedfishSystem(reads int, refuseReset bool, power *string) http.HandlerFunc {
	left := reads
	return func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "nex-fence" || password != "lab-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/redfish/v1/Systems/system-1/Actions/ComputerSystem.Reset":
			if refuseReset {
				http.Error(w, "already off", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "/redfish/v1/Systems/system-1":
			if *power == "On" && left <= 0 {
				*power = "Off"
			}
			left--
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"system-1","UUID":"` + labSystemUUID + `","PowerState":"` + *power + `"}`))
		default:
			http.NotFound(w, r)
		}
	}
}

// A controller does not power a machine off while it answers the request. Core must keep
// reading its PowerState until the machine is really off, or a working fence reports
// "uncertain" and leaves the promotion that needed it stuck.
func TestBMCFenceWaitsForTheMachineToActuallyGoOff(t *testing.T) {
	bmcSettleInterval = time.Millisecond
	t.Cleanup(func() { bmcSettleInterval = 2 * time.Second })
	power := "On"
	lab := newBMCLab(t, labSettlingRedfishSystem(4, false, &power))
	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err != nil || proof.Status != "passed" || !proof.ExclusionVerified {
		t.Fatalf("a controller that settles after a few reads was not proved: %v (%#v)", err, proof)
	}
}

// The case a pool fence exists for: the old master is already off. Several controllers
// refuse ForceOff for a machine that is already off, and that refusal must not fail the
// fence — Core's own reading of the power state is what decides.
func TestBMCFenceProvesATargetThatIsAlreadyOff(t *testing.T) {
	bmcSettleInterval = time.Millisecond
	t.Cleanup(func() { bmcSettleInterval = 2 * time.Second })
	power := "Off"
	lab := newBMCLab(t, labSettlingRedfishSystem(0, true, &power))
	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err != nil || proof.Status != "passed" || !proof.ExclusionVerified {
		t.Fatalf("a target that was already off was not proved excluded: %v (%#v)", err, proof)
	}
}

// A refused Reset is still not a pass: identity matched, the controller refused, and the
// machine stayed on. The proof is uncertain and nothing pretends otherwise.
func TestBMCFenceIsUncertainWhenTheMachineNeverGoesOff(t *testing.T) {
	bmcSettleInterval = time.Millisecond
	previousTimeout := bmcSettleTimeout
	bmcSettleTimeout = 5 * time.Millisecond
	t.Cleanup(func() { bmcSettleInterval, bmcSettleTimeout = 2*time.Second, previousTimeout })
	power := "On"
	lab := newBMCLab(t, labSettlingRedfishSystem(1<<30, true, &power))
	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err == nil || proof.Status != "uncertain" || proof.ExclusionVerified {
		t.Fatalf("a machine that never went off produced %#v (%v)", proof, err)
	}
}

// Unreachable, credential-rejected and silent controllers are refusals, never passes, and
// none of them powers anything off.
func TestBMCFenceRefusesWhenIdentityCannotBeRead(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"the account is rejected", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}, "rejected the recorded account"},
		{"the controller reports no machine", labRedfishSystem(""), "did not report the machine it manages"},
		{"the controller answers with an error", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "busy", http.StatusServiceUnavailable)
		}, "did not report the machine it manages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lab := newBMCLab(t, tc.handler)
			proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
			if err == nil || proof.Status == "passed" || proof.ExclusionVerified {
				t.Fatalf("proof = %#v, err = %v", proof, err)
			}
			if !strings.Contains(proof.Error, tc.want) {
				t.Fatalf("proof error = %q, want it to mention %q", proof.Error, tc.want)
			}
			if len(*lab.ran) != 0 {
				t.Fatalf("a fence agent ran without a confirmed identity: %v", *lab.ran)
			}
		})
	}

	t.Run("the controller does not answer at all", func(t *testing.T) {
		lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
		lab.fence.Endpoint = "127.0.0.1:1" // nothing listens here
		proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
		if err == nil || !strings.Contains(proof.Error, "did not answer") {
			t.Fatalf("proof = %#v, err = %v", proof, err)
		}
		if len(*lab.ran) != 0 {
			t.Fatalf("a fence agent ran against an unreachable controller: %v", *lab.ran)
		}
	})

	t.Run("the host does not report a unique machine identity", func(t *testing.T) {
		lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
		proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, "", "master-1", "op-1", "digest-1", 3)
		if err == nil || !strings.Contains(proof.Error, "does not report a unique hardware identity") {
			t.Fatalf("proof = %#v, err = %v", proof, err)
		}
		if len(*lab.ran) != 0 {
			t.Fatalf("a fence agent ran for a host with no recorded identity: %v", *lab.ran)
		}
	})
}

// A UUID is a machine identity only when it is one: placeholders firmware writes are not.
func TestNormalizedSystemUUIDRejectsPlaceholders(t *testing.T) {
	for _, raw := range []string{"", "not-a-uuid", "00000000-0000-0000-0000-000000000000", "FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF", "03000200-0400-0500-0006-000700080009", "4c4c4544-0037-3810-8057-b4c04f43533"} {
		if got := NormalizedSystemUUID(raw); got != "" {
			t.Fatalf("NormalizedSystemUUID(%q) = %q", raw, got)
		}
	}
	if got := NormalizedSystemUUID("  4C4C4544-0037-3810-8057-B4C04F435331\n"); got != labSystemUUID[:8]+labSystemUUID[9:13]+labSystemUUID[14:18]+labSystemUUID[19:23]+labSystemUUID[24:] {
		t.Fatalf("NormalizedSystemUUID did not normalize case and separators: %q", got)
	}
}

// An IPMI profile is refused with a sentence an operator can act on where ipmitool is not
// installed, rather than silently falling back to something that is not an identity check.
func TestIPMIIdentityRefusesWithoutItsTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
	lab.fence.Kind = protocol.FenceIPMI
	lab.fence.TargetID = "plug-1"
	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err == nil || !strings.Contains(proof.Error, "no ipmitool") {
		t.Fatalf("proof = %#v, err = %v", proof, err)
	}
	if len(*lab.ran) != 0 {
		t.Fatalf("a fence agent ran without an identity check: %v", *lab.ran)
	}
}

// The IPMI path is the same discipline through ipmitool's Get System GUID.
func TestIPMIIdentityReadsTheSystemGUID(t *testing.T) {
	lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
	lab.fence.Kind = protocol.FenceIPMI
	lab.fence.TargetID = "plug-1"
	fakeCommand(t, "ipmitool", "exit 0")

	var env, args []string
	previous := storageFenceEnvRun
	storageFenceEnvRun = func(_ context.Context, e []string, _, _ string, a ...string) ([]byte, error) {
		env, args = e, a
		return []byte("System GUID  : 3153434f-c0b4-5780-1038-370044454c4c\nTimestamp    : unspecified\n"), nil
	}
	t.Cleanup(func() { storageFenceEnvRun = previous })

	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err != nil {
		t.Fatalf("the recorded machine was not fenced: %v (%#v)", err, proof)
	}
	if strings.Join(args, " ") != "-I lanplus -H "+lab.fence.Endpoint+" -U nex-fence -E mc guid" {
		t.Fatalf("ipmitool arguments = %v", args)
	}
	// The password is never an argument: every process on this host can read those.
	if len(env) != 1 || env[0] != "IPMI_PASSWORD=lab-secret" || strings.Contains(strings.Join(args, " "), "lab-secret") {
		t.Fatalf("the IPMI password was not kept out of the process list: env = %v, args = %v", env, args)
	}
}

// A controller that redirects the identity read makes Core read one machine's UUID while the
// power-off goes to the recorded address — a passing proof for a machine that was never
// identified. Redirects are therefore never followed. (Reported by independent review with a
// working demonstration against the first implementation of this slice.)
func TestBMCFenceNeverFollowsAnIdentityRedirect(t *testing.T) {
	// The machine that really holds the recorded identity. It is not the fence target.
	innocent := httptest.NewTLSServer(labRedfishSystem(labSystemUUID))
	defer innocent.Close()

	lab := newBMCLab(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, innocent.URL+r.URL.Path, http.StatusFound)
	})
	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err == nil || proof.Status == "passed" || proof.ExclusionVerified {
		t.Fatalf("a redirected identity read produced a proof: %#v", proof)
	}
	if !strings.Contains(proof.Error, "did not report the machine it manages") {
		t.Fatalf("proof error = %q", proof.Error)
	}
	if len(*lab.ran) != 0 {
		t.Fatalf("a fence agent ran against a redirecting controller: %v", *lab.ran)
	}
}

// The password and the power-off go to one certificate and no other. A controller that
// presents a different one is refused with its own sentence, not with "did not answer": the
// endpoint answered, and it is not the machine whose access was recorded.
func TestBMCFenceRefusesAChangedCertificate(t *testing.T) {
	lab := newBMCLab(t, labRedfishSystem(labSystemUUID))
	// The profile was recorded against another certificate; this endpoint now presents a
	// different one. (httptest reuses one certificate for every server, so the pin is
	// changed rather than a second server started.)
	lab.fence.TLSFingerprint = strings.Repeat("ab", 32)

	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err == nil || proof.Status == "passed" {
		t.Fatalf("a substituted certificate produced a proof: %#v", proof)
	}
	if !strings.Contains(proof.Error, "presented a different certificate") {
		t.Fatalf("proof error = %q, want the certificate sentence rather than a network one", proof.Error)
	}
	if len(*lab.ran) != 0 {
		t.Fatalf("a fence agent ran against an unpinned controller: %v", *lab.ran)
	}

	// A profile with no certificate recorded at all is refused the same way, never opened
	// to whatever answers.
	lab.fence.TLSFingerprint = ""
	if proof, err = VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3); err == nil {
		t.Fatalf("an unpinned profile produced a proof: %#v", proof)
	}
	if !strings.Contains(proof.Error, "no certificate is recorded") {
		t.Fatalf("proof error = %q", proof.Error)
	}
}

// BMCCertificateFingerprint is what a recording operator compares with the controller's own
// page, and what every later connection is held to.
func TestBMCCertificateFingerprintIsTheLeafItWillBePinnedTo(t *testing.T) {
	server := httptest.NewTLSServer(labRedfishSystem(labSystemUUID))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := BMCCertificateFingerprint(context.Background(), endpoint.Host)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(server.Certificate().Raw)
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("fingerprint = %q", got)
	}
	if _, err = BMCCertificateFingerprint(context.Background(), "127.0.0.1:1"); err == nil {
		t.Fatal("a fingerprint was returned for a controller that does not answer")
	}
}

// The byte-order reversal exists for the IPMI wire format. A Redfish ComputerSystem.UUID is
// an RFC 4122 string with one spelling, so accepting its reversal would double the set of
// machines that count as this one for nothing.
func TestRedfishIdentityIsAnExactMatchAndIPMIAcceptsBothByteOrders(t *testing.T) {
	const reversed = "3153434f-c0b4-5780-1038-370044454c4c"

	lab := newBMCLab(t, labRedfishSystem(reversed))
	proof, err := VerifyPoolHostFence(context.Background(), lab.stateDir, &lab.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3)
	if err == nil || !strings.Contains(proof.Error, "reports a different machine") {
		t.Fatalf("Redfish accepted a reversed UUID: %#v, err %v", proof, err)
	}

	ipmi := newBMCLab(t, labRedfishSystem(labSystemUUID))
	ipmi.fence.Kind, ipmi.fence.TargetID, ipmi.fence.TLSFingerprint = protocol.FenceIPMI, "plug-1", ""
	fakeCommand(t, "ipmitool", "exit 0")
	previous := storageFenceEnvRun
	storageFenceEnvRun = func(context.Context, []string, string, string, ...string) ([]byte, error) {
		return []byte("System GUID  : " + reversed + "\n"), nil
	}
	t.Cleanup(func() { storageFenceEnvRun = previous })
	if proof, err = VerifyPoolHostFence(context.Background(), ipmi.stateDir, &ipmi.fence, labSystemUUID, "master-1", "op-1", "digest-1", 3); err != nil {
		t.Fatalf("IPMI refused the same GUID in the other documented byte order: %v (%#v)", err, proof)
	}
}
