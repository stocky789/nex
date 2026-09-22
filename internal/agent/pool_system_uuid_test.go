// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// labSystemUUIDs makes each lab host answer with its own machine identity, the way one
// physical machine answers for itself.
func labSystemUUIDs(t *testing.T, byHost map[string]string) {
	t.Helper()
	previous := localSystemUUID
	localSystemUUID = func(s *Store) (string, error) {
		// The stub normalizes exactly as the real reader does, so what the tests assert
		// is what a host actually sends.
		uuid := vmhost.NormalizedSystemUUID(byHost[s.State.HostID])
		if uuid == "" {
			return "", errStubNoSystemUUID
		}
		return uuid, nil
	}
	t.Cleanup(func() { localSystemUUID = previous })
}

var errStubNoSystemUUID = errStub("this host does not expose an SMBIOS system UUID")

type errStub string

func (e errStub) Error() string { return string(e) }

// Which machine each host is has to reach the master's membership record while the host is
// still alive, because that is where another host reads it when this one is gone. A pool
// that was formed before this existed picks it up on the next reconcile pass — no re-join,
// no operator input — and recording it is an observation, so no generation moves.
func TestPoolSystemUUIDReachesTheMastersMembership(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	labSystemUUIDs(t, map[string]string{
		"master-1": "4C4C4544-0037-3810-8057-B4C04F435331",
		"member-1": "5b5b5655-0148-4921-9168-c5d15f546442",
	})
	before := peerState(t, master).Membership.Generation

	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatalf("the master could not record its own machine identity: %v", err)
	}
	if err := ReportPoolSystemUUID(context.Background(), member); err != nil {
		t.Fatalf("the member could not report its machine identity: %v", err)
	}

	recorded := map[string]string{}
	state := peerState(t, master)
	for _, row := range state.Membership.Members {
		recorded[row.HostID] = row.SystemUUID
	}
	// One spelling, whatever firmware and tools print.
	if recorded["master-1"] != "4c4c4544003738108057b4c04f435331" {
		t.Fatalf("the master's own machine identity = %q", recorded["master-1"])
	}
	if recorded["member-1"] != "5b5b5655014849219168c5d15f546442" {
		t.Fatalf("the member's machine identity = %q", recorded["member-1"])
	}
	if state.Membership.Generation != before {
		t.Fatalf("recording a machine identity moved the membership generation from %d to %d", before, state.Membership.Generation)
	}

	// Repeating it changes nothing, so the reconcile pass can run it every tick.
	if err := ReportPoolSystemUUID(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if peerState(t, master).Membership.Generation != before {
		t.Fatal("a repeated report moved the membership generation")
	}

	// A host too old to report one leaves the record as it was; it simply cannot be given a
	// management-controller fence device.
	labSystemUUIDs(t, map[string]string{})
	if err := ReportPoolSystemUUID(context.Background(), member); err == nil {
		t.Fatal("a host with no machine identity reported one anyway")
	}
	if peerState(t, master).Membership.Members[1].SystemUUID == "" {
		t.Fatal("a host that stopped reporting erased what the master already knew")
	}
}

// labRedfishBMC is a management controller that answers for one machine over TLS, the way a
// real one does: a self-signed certificate with no usable name, which is why Core pins the
// exact certificate rather than verifying a chain.
func labRedfishBMC(t *testing.T, uuid string) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "nex-fence" || password != "lab-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/redfish/v1/Systems/system-1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Id":"system-1","UUID":"` + uuid + `","PowerState":"On"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// writeLabBMCCredential puts the password where the credential handoff puts it: an executable
// script, which is the only interface the private store and the fence agents share.
func writeLabBMCCredential(t *testing.T, s *Store, ref string) string {
	t.Helper()
	dir := filepath.Join(s.dir, "storage-cluster", "fence-credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ref)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' 'lab-secret'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func labBMCProfile(server *httptest.Server, ref string) *protocol.StorageClusterFence {
	return &protocol.StorageClusterFence{
		Kind: protocol.FenceRedfish, Endpoint: strings.TrimPrefix(server.URL, "https://"),
		TargetID: "system-1", Username: "nex-fence", CredentialRef: ref,
	}
}

// Recording a management controller is not a form submission: Core connects, pins the exact
// certificate the controller presents, proves over that connection that it manages this
// host's machine, and only then writes the profile. A host that has not reported which
// machine it is cannot get one, and the password delivered for a refused profile does not
// stay behind on the master.
func TestManagementControllerProfileIsPinnedAndProvedBeforeItIsRecorded(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	server := labRedfishBMC(t, "4c4c4544-0037-3810-8057-b4c04f435331")
	bmc := labBMCProfile(server, "lab-bmc")
	credential := writeLabBMCCredential(t, master, "lab-bmc")

	labSystemUUIDs(t, map[string]string{})
	result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-bmc-early", "master-1", "master-1", bmc))
	if result.Status == "succeeded" || !strings.Contains(result.Error, "does not report a unique hardware identity") {
		t.Fatalf("a controller was recorded for a host of unknown identity: %#v", result)
	}
	// The password reached this master before the command did; a profile that is not
	// recorded must not leave it behind.
	if _, err := os.Stat(credential); !os.IsNotExist(err) {
		t.Fatalf("a refused profile left its password on the master: %v", err)
	}

	credential = writeLabBMCCredential(t, master, "lab-bmc")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if result = ExecutePoolFenceProfile(master, fenceProfileCommand("fp-bmc", "master-1", "master-1", bmc)); result.Status != "succeeded" {
		t.Fatalf("recording a management controller = %#v", result)
	}
	member, ok := poolMemberByID(peerState(t, master).Membership, "master-1")
	if !ok || member.Fence == nil || member.Fence.Username != "nex-fence" || member.FenceCredentialVersion == 0 {
		t.Fatalf("the recorded profile = %#v", member.Fence)
	}
	pin := sha256.Sum256(server.Certificate().Raw)
	if member.Fence.TLSFingerprint != hex.EncodeToString(pin[:]) {
		t.Fatalf("the profile was not pinned to the certificate the controller presented: %q", member.Fence.TLSFingerprint)
	}
	// The master holds the material, so it can seal it for the members that may one day
	// have to fence with it. Reading it at the wrong mode is what this would fail on.
	if files, err := vmhost.ReadPoolFenceCredential(master.dir, *member.Fence); err != nil || len(files[""]) == 0 {
		t.Fatalf("the master cannot replicate the controller password: %v", err)
	}

	// A controller that answers for another machine is never recorded, and again leaves no
	// password behind.
	other := labRedfishBMC(t, "5b5b5655-0148-4921-9168-c5d15f546442")
	wrong := labBMCProfile(other, "lab-bmc-wrong")
	credential = writeLabBMCCredential(t, master, "lab-bmc-wrong")
	result = ExecutePoolFenceProfile(master, fenceProfileCommand("fp-bmc-wrong", "master-1", "master-1", wrong))
	if result.Status == "succeeded" || !strings.Contains(result.Error, "reports a different machine") {
		t.Fatalf("a controller for another machine was recorded: %#v", result)
	}
	if _, err := os.Stat(credential); !os.IsNotExist(err) {
		t.Fatalf("a refused profile left its password on the master: %v", err)
	}
}

// A host must not be able to veto its own fencing. Once a management controller is recorded
// against the machine identity a host reported, that host cannot replace the identity: it
// would make every proof for itself fail for ever, and a compromised host refusing to be
// excluded is exactly what fencing exists to stop. Clearing the device is the path a
// motherboard replacement takes.
func TestAHostCannotChangeItsMachineIdentityUnderARecordedController(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	server := labRedfishBMC(t, "5b5b5655-0148-4921-9168-c5d15f546442")
	writeLabBMCCredential(t, master, "lab-bmc")

	labSystemUUIDs(t, map[string]string{"member-1": "5b5b5655-0148-4921-9168-c5d15f546442"})
	if err := ReportPoolSystemUUID(context.Background(), lab.host("member-1")); err != nil {
		t.Fatal(err)
	}
	bmc := labBMCProfile(server, "lab-bmc")
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-bmc", "master-1", "member-1", bmc)); result.Status != "succeeded" {
		t.Fatalf("recording a controller for the member = %#v", result)
	}

	labSystemUUIDs(t, map[string]string{"member-1": "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"})
	if err := ReportPoolSystemUUID(context.Background(), lab.host("member-1")); err != nil {
		t.Fatal(err)
	}
	member, _ := poolMemberByID(peerState(t, master).Membership, "member-1")
	if member.SystemUUID != "5b5b5655014849219168c5d15f546442" {
		t.Fatalf("a host replaced the machine identity its fence device depends on: %q", member.SystemUUID)
	}

	// Clearing the device releases it, which is the motherboard-replacement path.
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-clear", "master-1", "member-1", nil)); result.Status != "succeeded" {
		t.Fatalf("clearing the controller = %#v", result)
	}
	if err := ReportPoolSystemUUID(context.Background(), lab.host("member-1")); err != nil {
		t.Fatal(err)
	}
	if member, _ = poolMemberByID(peerState(t, master).Membership, "member-1"); member.SystemUUID != "aaaaaaaabbbb4ccc8dddeeeeeeeeeeee" {
		t.Fatalf("a replaced motherboard could not report its new identity: %q", member.SystemUUID)
	}
}

// An identity two hosts both report is not an identity: each would satisfy the other's check,
// so a mistyped controller address between them powers off the wrong machine. The second host
// to report it is refused, and stays unfenceable rather than wrongly fenceable.
func TestADuplicateMachineIdentityIsNotRecorded(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{
		"master-1": "4c4c4544-0037-3810-8057-b4c04f435331",
		"member-1": "4c4c4544-0037-3810-8057-b4c04f435331",
	})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if err := ReportPoolSystemUUID(context.Background(), lab.host("member-1")); err != nil {
		t.Fatal(err)
	}
	member, _ := poolMemberByID(peerState(t, master).Membership, "member-1")
	if member.SystemUUID != "" {
		t.Fatalf("two hosts of one pool hold the same machine identity: %q", member.SystemUUID)
	}
	if err := vmhost.PoolFenceUsable(&protocol.StorageClusterFence{
		Kind: protocol.FenceRedfish, Endpoint: "bmc.example.test", TargetID: "system-1",
		Username: "nex-fence", CredentialRef: "cred-1",
	}, member.SystemUUID); err == nil {
		t.Fatal("a host with no unique identity was judged fenceable")
	}
}

// The one line the whole feature rests on: a member advertises only about itself. Nothing a
// host says can move another host's machine identity, because that is the field a promotion
// reads before it powers that other host off.
func TestAMemberCannotAdvertiseAnotherMembersMachineIdentity(t *testing.T) {
	lab := newTransferLab(t, 2)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{
		"member-1": "5b5b5655-0148-4921-9168-c5d15f546442",
		"member-2": "6c6c6766-0259-4032-a279-d6e26f657553",
	})
	for _, host := range []string{"member-1", "member-2"} {
		if err := ReportPoolSystemUUID(context.Background(), lab.host(host)); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := poolMemberByID(peerState(t, master).Membership, "member-2")

	// member-1's own authenticated peer client, posting member-2's identity.
	client, err := PoolPeerClient(lab.host("member-1"), lab.origin["master-1"])
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	body := `{"system_uuid":"6c6c6766-0259-4032-a279-d6e26f657553"}`
	resp, err := client.Post(lab.origin["master-1"]+"/api/v1/pool/advertise", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	after, _ := poolMemberByID(peerState(t, master).Membership, "member-2")
	if after.SystemUUID != before.SystemUUID {
		t.Fatalf("a member wrote another member's machine identity: %q then %q", before.SystemUUID, after.SystemUUID)
	}
	// The only row it may write is its own, and there the value is a duplicate of what
	// member-2 already reports, so it is refused there too and member-1 keeps its own.
	own, _ := poolMemberByID(peerState(t, master).Membership, "member-1")
	if own.SystemUUID != "5b5b5655014849219168c5d15f546442" {
		t.Fatalf("member-1's own identity = %q", own.SystemUUID)
	}
}

// An ejected row is not a member. The machine it names may be the very one that was
// re-imaged and re-enrolled under a new host ID, and if its retained identity still counts
// as a duplicate the replacement host can never record one — so it can never be given a
// management controller, and the operator is told to eject a host that is already ejected.
func TestAnEjectedRowDoesNotHoldAMachineIdentityForEver(t *testing.T) {
	lab := newTransferLab(t, 2)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{"member-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), lab.host("member-1")); err != nil {
		t.Fatal(err)
	}
	ejectMember(t, lab, "op-eject-identity")

	// The same machine, re-imaged, comes back with a new host ID and the same SMBIOS UUID.
	labSystemUUIDs(t, map[string]string{"member-2": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), lab.host("member-2")); err != nil {
		t.Fatal(err)
	}
	member, _ := poolMemberByID(peerState(t, master).Membership, "member-2")
	if member.SystemUUID != "4c4c4544003738108057b4c04f435331" || member.SystemUUIDConflictHostID != "" {
		t.Fatalf("an ejected host still holds the machine's identity: %q, conflict %q", member.SystemUUID, member.SystemUUIDConflictHostID)
	}
}
