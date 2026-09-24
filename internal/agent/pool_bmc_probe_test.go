// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const labProbeRef = "probe-cred-1"

// labProbeCredential puts a handed-over password where the credential handoff puts it.
func labProbeCredential(t *testing.T, s *Store) string {
	t.Helper()
	fence := protocol.StorageClusterFence{Kind: protocol.FenceRedfish, CredentialRef: labProbeRef}
	if err := vmhost.WritePoolFenceCredential(s.dir, fence, map[string][]byte{"": vmhost.FencePasswordScript("lab-secret")}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(s.dir, "storage-cluster", "fence-credentials", labProbeRef)
}

func labProbeHandoff(t *testing.T, s *Store, kind string) (string, string) {
	t.Helper()
	binding := "cluster-credential:" + hex.EncodeToString([]byte("detect-1")) + ":" + kind + ":" + hex.EncodeToString([]byte("member-1"))
	receipt, err := saveCredentialHandoff(s, protocol.StorageCredentialHandoff{
		Offer:       protocol.StorageCredentialOffer{RequestID: "probe-cred-1", RegistrationRequestID: binding, PoolID: s.State.PoolID, HostID: s.State.HostID},
		Credentials: protocol.StorageCredentials{Username: "nex-fence", Secret: "lab-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt.Receipt, filepath.Join(s.dir, "storage-cluster", "fence-credentials", receipt.Receipt)
}

func assertProbeSecretGone(t *testing.T, s *Store, script string) {
	t.Helper()
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Fatalf("probe left a materialized password: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, clusterSecretDirectory))
	if err != nil || len(entries) != 1 { // only the receipt key remains
		t.Fatalf("probe left its persisted password receipt: %v, %v", entries, err)
	}
}

type labProbeCall struct{ endpoint, username, secret, uuid string }

// labProbe stands in for the management controller and records what the master asked it.
func labProbe(t *testing.T, answer protocol.PoolBMCProbeResult, err error) *[]labProbeCall {
	t.Helper()
	calls := []labProbeCall{}
	previous := probeBMC
	probeBMC = func(_ context.Context, endpoint, username, secret, uuid string) (protocol.PoolBMCProbeResult, error) {
		calls = append(calls, labProbeCall{endpoint, username, secret, uuid})
		return answer, err
	}
	t.Cleanup(func() { probeBMC = previous })
	return &calls
}

func bmcProbeRequest(s *Store) protocol.PoolRequest {
	return protocol.PoolRequest{RequestID: "detect-1", PoolID: s.State.PoolID, HostID: s.State.HostID, Action: "bmc-probe",
		MemberHostID: "member-1", Endpoint: "192.0.2.12", Username: "nex-fence", CredentialRef: labProbeRef}
}

func membershipJSON(t *testing.T, s *Store) string {
	t.Helper()
	b, err := json.Marshal(peerState(t, s).Membership)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestABMCProbeAnswersFromTheMasterRecordsNothingAndRetiresItsCredential(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	labSystemUUIDs(t, map[string]string{"member-1": "5b5b5655-0148-4921-9168-c5d15f546442"})
	if err := ReportPoolSystemUUID(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	answer := protocol.PoolBMCProbeResult{
		Methods: []protocol.PoolBMCProbeMethod{{Kind: protocol.FenceRedfish, Reachable: true, TargetIDs: []string{"System.Embedded.1"}}},
		Recommended: &protocol.PoolBMCProbeChoice{Kind: protocol.FenceRedfish, TargetID: "System.Embedded.1",
			Reason: "this system reports this host's hardware identity"},
	}
	calls := labProbe(t, answer, nil)
	ref, script := labProbeHandoff(t, master, "bmc_probe")
	request := bmcProbeRequest(master)
	request.CredentialRef = ref
	before := membershipJSON(t, master)

	result := ExecutePoolBMCProbe(context.Background(), master, request)
	if result.Status != "succeeded" || result.BMCProbe == nil || result.BMCProbe.Recommended.TargetID != "System.Embedded.1" {
		t.Fatalf("result = %#v", result)
	}
	if len(*calls) != 1 || (*calls)[0] != (labProbeCall{"192.0.2.12", "nex-fence", "lab-secret", "5b5b5655014849219168c5d15f546442"}) {
		t.Fatalf("probe calls = %#v", *calls)
	}
	if after := membershipJSON(t, master); after != before {
		t.Fatalf("a probe changed the membership:\n%s\n%s", before, after)
	}
	assertProbeSecretGone(t, master, script)

	// A probe that finds nothing still retires what it was handed.
	labProbe(t, protocol.PoolBMCProbeResult{}, errors.New("no management controller answered"))
	ref, script = labProbeHandoff(t, master, "bmc_probe")
	request.CredentialRef = ref
	result = ExecutePoolBMCProbe(context.Background(), master, request)
	if result.Status != "failed" || result.Error != "no management controller answered" {
		t.Fatalf("result = %#v", result)
	}
	assertProbeSecretGone(t, master, script)
}

func TestUndeliveredBMCProbeCredentialExpiresWithoutTouchingRecordedFenceSecrets(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	_, script := labProbeHandoff(t, master, "bmc_probe")
	_, permanent := labProbeHandoff(t, master, "fence_bmc")
	if err := retireExpiredProbeSecrets(master, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("an active probe credential expired early: %v", err)
	}
	if err := retireExpiredProbeSecrets(master, time.Now().Add(probeSecretTTL+time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Fatalf("expired probe left its password script: %v", err)
	}
	if _, err := os.Stat(permanent); err != nil {
		t.Fatalf("expiry removed a permanent fence secret: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(master.dir, clusterSecretDirectory))
	if err != nil || len(entries) != 2 { // receipt key and the permanent credential
		t.Fatalf("expiry kept a probe receipt or removed a permanent one: %v, %v", entries, err)
	}
}

func TestABMCProbeRunsOnlyOnTheMaster(t *testing.T) {
	lab := newTransferLab(t, 1)
	member := lab.host("member-1")
	calls := labProbe(t, protocol.PoolBMCProbeResult{}, nil)
	ref, script := labProbeHandoff(t, member, "bmc_probe")
	request := bmcProbeRequest(member)
	request.CredentialRef = ref
	before := membershipJSON(t, member)

	result := ExecutePoolBMCProbe(context.Background(), member, request)
	if result.Status != "failed" || !strings.Contains(result.Error, "only the pool master") {
		t.Fatalf("result = %#v", result)
	}
	if len(*calls) != 0 {
		t.Fatal("a host that is not the master contacted a management controller")
	}
	if membershipJSON(t, member) != before {
		t.Fatal("a refused probe changed the membership")
	}
	assertProbeSecretGone(t, member, script)
}

// A probe may not borrow a recorded fence device's password: it would be sent to whatever
// address the probe names. The recorded material is left exactly where it is.
func TestABMCProbeRefusesARecordedFenceCredential(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	calls := labProbe(t, protocol.PoolBMCProbeResult{}, nil)
	script := labProbeCredential(t, master)
	recordLabFenceProfileWithoutMaterial(t, master, "member-1", &protocol.StorageClusterFence{
		Kind: protocol.FenceRedfish, Endpoint: "192.0.2.12", TargetID: "1", Username: "nex-fence", CredentialRef: labProbeRef})

	result := ExecutePoolBMCProbe(context.Background(), master, bmcProbeRequest(master))
	if result.Status != "failed" || !strings.Contains(result.Error, "recorded fence device") || len(*calls) != 0 {
		t.Fatalf("result = %#v, calls = %v", result, *calls)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("a refused probe retired a recorded device's password: %v", err)
	}
}

func TestABMCProbeRefusesAnUnrecordedPermanentCredential(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	calls := labProbe(t, protocol.PoolBMCProbeResult{}, nil)
	ref, script := labProbeHandoff(t, master, "fence_bmc")
	request := bmcProbeRequest(master)
	request.RequestID = "detect-1"
	request.CredentialRef = ref

	result := ExecutePoolBMCProbe(context.Background(), master, request)
	if result.Status != "failed" || len(*calls) != 0 {
		t.Fatalf("result = %#v, calls = %v", result, *calls)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("probe retired a permanent credential: %v", err)
	}
}

func TestABMCProbeCredentialIsBoundToItsRequestAndTarget(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	calls := labProbe(t, protocol.PoolBMCProbeResult{}, nil)
	ref, script := labProbeHandoff(t, master, "bmc_probe")
	request := bmcProbeRequest(master)
	request.CredentialRef = ref
	for _, change := range []func(*protocol.PoolRequest){
		func(r *protocol.PoolRequest) { r.RequestID = "another-detect" },
		func(r *protocol.PoolRequest) { r.MemberHostID = "another-member" },
	} {
		wrong := request
		change(&wrong)
		if result := ExecutePoolBMCProbe(context.Background(), master, wrong); result.Status != "failed" || len(*calls) != 0 {
			t.Fatalf("out-of-scope probe = %#v, calls = %v", result, *calls)
		}
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("out-of-scope probe retired the rightful credential: %v", err)
	}
}

func TestExpiredBMCProbeCredentialSurvivesUnrelatedCorruptReceipt(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	_, script := labProbeHandoff(t, master, "bmc_probe")
	path := filepath.Join(master.dir, clusterSecretDirectory, "000.json")
	if err := os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := retireExpiredProbeSecrets(master, time.Now().Add(probeSecretTTL+time.Second)); err == nil {
		t.Fatal("damaged unrelated receipt should be reported")
	}
	if _, err := os.Stat(script); !os.IsNotExist(err) {
		t.Fatalf("damaged unrelated receipt prevented probe expiry: %v", err)
	}
}
