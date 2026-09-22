// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// labBMCAddresses gives each lab host its own management controller address, and stands up a
// fake ipmitool that answers Get System GUID for each of them. The master really runs the
// identity check before it records a profile, so without this nothing would be recorded.
func labBMCAddresses(t *testing.T, byAddress map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for address, uuid := range byAddress {
		if err := os.WriteFile(filepath.Join(dir, address), []byte("System GUID  : "+uuid+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	script := "#!/bin/sh\naddress=\nwhile [ $# -gt 0 ]; do if [ \"$1\" = \"-H\" ]; then address=$2; fi; shift; done\n" +
		"exec cat " + filepath.Join(dir, "$address") + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ipmitool"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

// labBMC stands in for one host's own management controller: it counts the accounts it was
// asked to create and the ones it was asked to remove, and can be told to refuse either.
type labBMC struct {
	made, removed          int
	refuseWrite, refuseOff error
}

func labInBandBMC(t *testing.T, address string) *labBMC {
	t.Helper()
	lab := &labBMC{}
	previousPlan, previousConfigure, previousRemove := planLocalBMCAccount, configureLocalBMCAccount, removeLocalBMCAccount
	planLocalBMCAccount = func(context.Context, vmhost.LocalBMCAccount) vmhost.LocalBMCResult {
		return vmhost.LocalBMCResult{Address: address, Channel: 1, Cipher: 17, Slot: 3, Account: vmhost.BMCFenceAccount}
	}
	configureLocalBMCAccount = func(context.Context, vmhost.LocalBMCResult) (string, error) {
		if lab.refuseWrite != nil {
			return "", lab.refuseWrite
		}
		lab.made++
		return "lab-bmc-password", nil
	}
	removeLocalBMCAccount = func(context.Context, uint8, uint8) error {
		if lab.refuseOff != nil {
			return lab.refuseOff
		}
		lab.removed++
		return nil
	}
	t.Cleanup(func() {
		planLocalBMCAccount, configureLocalBMCAccount, removeLocalBMCAccount = previousPlan, previousConfigure, previousRemove
	})
	return lab
}

// clearFence is the operator's own action, through the one route that clears a profile.
func clearFence(t *testing.T, master *Store, hostID, requestID string) protocol.Result {
	t.Helper()
	return ExecutePoolFenceProfile(master, fenceProfileCommand(requestID, "master-1", hostID, nil))
}

func memberRow(t *testing.T, s *Store, hostID string) protocol.PoolMember {
	t.Helper()
	for _, member := range peerState(t, s).Membership.Members {
		if member.HostID == hostID {
			return member
		}
	}
	t.Fatalf("no row for %s", hostID)
	return protocol.PoolMember{}
}

// The whole point of the slice: a host finds its own management controller, creates its own
// account on it and the pool ends up with a fence device nobody typed in. Running the pass
// again changes nothing — no second account, no new password, no new record.
func TestAHostConfiguresItsOwnFenceDeviceAndTheMasterRecordsIt(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	labSystemUUIDs(t, map[string]string{
		"master-1": "4c4c4544-0037-3810-8057-b4c04f435331",
		"member-1": "5b5b5655-0148-4921-9168-c5d15f546442",
	})
	labBMCAddresses(t, map[string]string{
		"192.0.2.11": "4c4c4544-0037-3810-8057-b4c04f435331",
		"192.0.2.12": "5b5b5655-0148-4921-9168-c5d15f546442",
	})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if err := ReportPoolSystemUUID(context.Background(), member); err != nil {
		t.Fatal(err)
	}

	masterBMC := labInBandBMC(t, "192.0.2.11")
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatalf("the master could not configure its own controller: %v", err)
	}
	row := memberRow(t, master, "master-1")
	if row.Fence == nil || row.Fence.Kind != protocol.FenceIPMI || !row.Fence.ProvisionedByCore {
		t.Fatalf("the master recorded no fence device of its own: %#v", row.Fence)
	}
	if row.Fence.Endpoint != "192.0.2.11" || row.Fence.Username != vmhost.BMCFenceAccount || row.Fence.Cipher != 17 {
		t.Fatalf("fence = %#v", row.Fence)
	}
	if row.FenceCredentialVersion == 0 {
		t.Fatal("the master recorded a profile with no material behind it")
	}
	if row.BMC == nil || row.BMC.State != protocol.PoolBMCProvisioned || row.BMC.Address != "192.0.2.11" {
		t.Fatalf("the master reported nothing about its own controller: %#v", row.BMC)
	}
	// The password never reaches a report, a membership record or anything replicated.
	if replica, err := json.Marshal(peerState(t, master).Membership); err != nil || strings.Contains(string(replica), "lab-bmc-password") {
		t.Fatalf("the fence account's password is in the pool's membership record (%v)", err)
	}

	// A member does the same over its own peer connection to the master.
	memberBMC := labInBandBMC(t, "192.0.2.12")
	if err := EnsureLocalBMCFence(context.Background(), member); err != nil {
		t.Fatalf("the member could not configure its own controller: %v", err)
	}
	if row = memberRow(t, master, "member-1"); row.Fence == nil || !row.Fence.ProvisionedByCore || row.Fence.Endpoint != "192.0.2.12" {
		t.Fatalf("the master did not record the member's own fence device: %#v", row.Fence)
	}

	// Idempotency: another pass, and another after a restart that forgets nothing durable,
	// creates no second account and moves no credential version.
	version := row.FenceCredentialVersion
	for i := 0; i < 3; i++ {
		if err := EnsureLocalBMCFence(context.Background(), member); err != nil {
			t.Fatal(err)
		}
	}
	if memberBMC.made != 1 || masterBMC.made != 1 {
		t.Fatalf("a repeated pass created another account: master %d, member %d", masterBMC.made, memberBMC.made)
	}
	if memberRow(t, master, "member-1").FenceCredentialVersion != version {
		t.Fatal("a repeated pass rotated the fence account's password")
	}

	// A master change moves the pool's record, not the account. The host that owns the
	// controller has nothing left to do, whichever host is master afterwards.
	poolPeerMu.Lock()
	moved, err := loadPoolPeer(member)
	if err == nil {
		moved.Membership.MasterHostID = "member-1"
		err = savePoolPeer(member, moved)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = EnsureLocalBMCFence(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if memberBMC.made != 1 {
		t.Fatalf("a master change made the host configure its controller again: %d accounts", memberBMC.made)
	}
}

// An operator-entered profile is the operator's decision. A host that configures its own
// controller reports what it found and leaves that profile exactly as it is.
func TestAnOperatorEnteredProfileIsNeverReplacedByAutomaticProvisioning(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	server := labRedfishBMC(t, "4c4c4544-0037-3810-8057-b4c04f435331")
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	writeLabBMCCredential(t, master, "cred-manual")
	manual := protocol.StorageClusterFence{Kind: protocol.FenceRedfish, Endpoint: strings.TrimPrefix(server.URL, "https://"),
		TargetID: "system-1", Username: "nex-fence", CredentialRef: "cred-manual"}
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-manual", "master-1", "master-1", &manual)); result.Status != "succeeded" {
		t.Fatalf("the operator's own profile was not recorded: %#v", result)
	}

	bmc := labInBandBMC(t, "192.0.2.11")
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	row := memberRow(t, master, "master-1")
	if row.Fence == nil || row.Fence.Kind != protocol.FenceRedfish || row.Fence.ProvisionedByCore {
		t.Fatalf("automatic provisioning took over an operator's fence device: %#v", row.Fence)
	}
	if bmc.made != 0 {
		t.Fatal("a host with an operator-entered profile still went to its management controller")
	}
	if row.BMC == nil || row.BMC.State != protocol.PoolBMCManualProfile {
		t.Fatalf("the host did not report why it left the controller alone: %#v", row.BMC)
	}
}

// Clearing a fence device Nex Core provisioned is one operator action with one meaning: the
// account comes off the controller, the master keeps the decision, and the host does not make
// another until the operator says so. The master must not be left self-fence-exposed by it,
// or it comes back restricted after every restart.
func TestClearingAProvisionedFenceDeviceRemovesTheAccountAndStopsProvisioning(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	labBMCAddresses(t, map[string]string{"192.0.2.11": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	bmc := labInBandBMC(t, "192.0.2.11")
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}

	if result := clearFence(t, master, "master-1", "req-clear"); result.Status != "succeeded" {
		t.Fatalf("the operator could not clear the fence device: %#v", result)
	}
	row := memberRow(t, master, "master-1")
	if row.Fence != nil || row.FenceCredentialVersion != 0 || !row.BMCProvisioningDisabled {
		t.Fatalf("clearing did not withdraw the device: %#v, disabled=%v", row.Fence, row.BMCProvisioningDisabled)
	}
	// A master that has no fence device any host could use must not stay exposed, or it is
	// restricted after every restart with no way out.
	if peerState(t, master).SelfFence != nil && peerState(t, master).SelfFence.ClearedAt == 0 {
		t.Fatal("the master was left permanently self-fence-exposed")
	}

	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if bmc.removed != 1 {
		t.Fatalf("the account was not removed from the controller: %d", bmc.removed)
	}
	row = memberRow(t, master, "master-1")
	if row.BMC == nil || row.BMC.State != protocol.PoolBMCRemoved {
		t.Fatalf("the removal was not reported: %#v", row.BMC)
	}

	// And it stays removed, however many passes run.
	for i := 0; i < 3; i++ {
		if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
			t.Fatal(err)
		}
	}
	if bmc.made != 1 || memberRow(t, master, "master-1").Fence != nil {
		t.Fatalf("a removed fence account came back by itself: %d accounts made", bmc.made)
	}

	// Until the operator asks for it again.
	enable := protocol.Command{OperationID: "op-enable", Kind: "pool-bmc-provisioning", Pool: &protocol.PoolCommand{
		RequestID: "req-enable", Action: "bmc-provisioning", MemberHostID: "master-1",
		Membership: protocol.PoolMembership{MasterHostID: "master-1"}}}
	if result := ExecutePoolBMCProvisioning(master, enable); result.Status != "succeeded" {
		t.Fatalf("automatic provisioning could not be turned back on: %#v", result)
	}
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if bmc.made != 2 || memberRow(t, master, "master-1").Fence == nil {
		t.Fatalf("the host did not configure its controller again: %d accounts made", bmc.made)
	}
}

// A controller that will not take the account off must not leave the operator stuck. The
// decision is the master's and has already been made; the host says the account may still be
// there and keeps trying.
func TestAControllerThatRefusesRemovalIsReportedHonestlyAndRetried(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	labBMCAddresses(t, map[string]string{"192.0.2.11": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	bmc := labInBandBMC(t, "192.0.2.11")
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if result := clearFence(t, master, "master-1", "req-clear"); result.Status != "succeeded" {
		t.Fatalf("%#v", result)
	}
	bmc.refuseOff = errors.New("this host's management controller refused to remove the fence account")
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	row := memberRow(t, master, "master-1")
	if row.BMC == nil || row.BMC.State != protocol.PoolBMCRemoved || !strings.Contains(row.BMC.Detail, "still on the management controller") {
		t.Fatalf("a refused removal was not reported honestly: %#v", row.BMC)
	}
	if row.Fence != nil {
		t.Fatal("the fence device survived the operator's decision")
	}
	bmc.refuseOff = nil
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if bmc.removed != 1 {
		t.Fatalf("the removal was not retried once the controller recovered: %d", bmc.removed)
	}
}

// A member must not be able to delete the fence device the pool recorded for it by describing
// itself. Withdrawing one is an operator decision, authorised at the Controller and recorded
// by the master. (Reported by independent review.)
func TestAMemberCannotDeleteItsOwnFenceDeviceByAdvertising(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	labSystemUUIDs(t, map[string]string{"member-1": "5b5b5655-0148-4921-9168-c5d15f546442"})
	labBMCAddresses(t, map[string]string{"192.0.2.12": "5b5b5655-0148-4921-9168-c5d15f546442"})
	if err := ReportPoolSystemUUID(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	labInBandBMC(t, "192.0.2.12")
	if err := EnsureLocalBMCFence(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if memberRow(t, master, "member-1").Fence == nil {
		t.Fatal("the member has no fence device to try to delete")
	}
	if err := postPoolAdvertise(context.Background(), member, map[string]any{"bmc": protocol.PoolHostBMC{
		State: protocol.PoolBMCRemoved, Detail: "I no longer wish to be fenceable", ObservedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	row := memberRow(t, master, "member-1")
	if row.Fence == nil || row.FenceCredentialVersion == 0 {
		t.Fatal("a member deleted its own fence device by advertising a report")
	}
	if row.BMC == nil || row.BMC.State != protocol.PoolBMCRemoved {
		t.Fatalf("the member's own report was not recorded as the observation it is: %#v", row.BMC)
	}
}

// An observation is not a decision, so repeating one must not move the pool's signed
// authority. Otherwise every settled pool re-signs and re-pushes its whole authority replica
// every ten seconds, for ever, because of a timestamp. (Reported by independent review.)
func TestRepeatingAnUnchangedReportMovesNothing(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	// The commonest steady state of all: a host with no management controller.
	previous := planLocalBMCAccount
	planLocalBMCAccount = func(context.Context, vmhost.LocalBMCAccount) vmhost.LocalBMCResult {
		return vmhost.LocalBMCResult{State: protocol.PoolBMCNoInterface, Detail: "this host has no local IPMI interface"}
	}
	t.Cleanup(func() { planLocalBMCAccount = previous })

	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	before := peerState(t, master)
	// The assertion that does the work: a single-host pool pushes no replica, so its
	// authority generation would sit still either way. What must not change is the durable
	// record itself.
	durable, err := os.ReadFile(filepath.Join(master.dir, "pool-peer.json"))
	if err != nil {
		t.Fatal(err)
	}
	observed := memberRow(t, master, "master-1").BMC
	if observed == nil || observed.State != protocol.PoolBMCNoInterface {
		t.Fatalf("the first report was not recorded: %#v", observed)
	}
	for i := 0; i < 5; i++ {
		if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
			t.Fatal(err)
		}
	}
	after := peerState(t, master)
	if after.AuthorityGeneration != before.AuthorityGeneration || after.AuthorityDigest != before.AuthorityDigest {
		t.Fatalf("repeating one unchanged report moved the pool's authority from %d/%s to %d/%s",
			before.AuthorityGeneration, before.AuthorityDigest, after.AuthorityGeneration, after.AuthorityDigest)
	}
	if !memberRow(t, master, "master-1").BMC.ObservedAt.Equal(observed.ObservedAt) {
		t.Fatal("an unchanged report was rewritten with a new timestamp")
	}
	if now, readErr := os.ReadFile(filepath.Join(master.dir, "pool-peer.json")); readErr != nil || string(now) != string(durable) {
		t.Fatalf("repeating one unchanged report rewrote the pool master's durable record (%v)", readErr)
	}
}

// A host whose master cannot be reached has really created an account, so it must not forget
// that — and it must not leave a fence device that looks usable either. It reports the truth
// and finishes the job on a later pass.
func TestAnUnreachableMasterLeavesAPendingHostAndNoSecondAccount(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	labSystemUUIDs(t, map[string]string{
		"master-1": "4c4c4544-0037-3810-8057-b4c04f435331",
		"member-1": "5b5b5655-0148-4921-9168-c5d15f546442",
	})
	labBMCAddresses(t, map[string]string{"192.0.2.12": "5b5b5655-0148-4921-9168-c5d15f546442"})
	if err := ReportPoolSystemUUID(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	bmc := labInBandBMC(t, "192.0.2.12")
	lab.powerOff("master-1")
	if err := EnsureLocalBMCFence(context.Background(), member); err == nil {
		t.Fatal("a member reported success while its master was unreachable")
	}
	state := peerState(t, member)
	if state.LocalBMC == nil || !state.LocalBMC.Created || state.LocalBMC.Slot != 3 || state.LocalBMC.Pending == nil {
		t.Fatalf("the host forgot the account it had just created: %#v", state.LocalBMC)
	}
	if memberRow(t, master, "member-1").Fence != nil {
		t.Fatal("a profile was recorded although the master never accepted it")
	}
	// A later pass offers the same account and the same password again. Writing a new
	// password to the controller's flash every ten seconds for as long as the master is
	// away is what this must not do. (Reported by independent review.)
	for i := 0; i < 3; i++ {
		if err := EnsureLocalBMCFence(context.Background(), member); err == nil {
			t.Fatal("a later attempt reported success while the master was unreachable")
		}
	}
	if bmc.made != 1 {
		t.Fatalf("the host reset its controller password %d times while its master was away", bmc.made)
	}
	// And when the master comes back, the same profile is recorded with no new account.
	lab.powerOn("master-1")
	if err := EnsureLocalBMCFence(context.Background(), member); err != nil {
		t.Fatalf("the pending offer was not completed: %v", err)
	}
	if bmc.made != 1 || memberRow(t, master, "member-1").Fence == nil {
		t.Fatalf("accounts made = %d", bmc.made)
	}
}

// A refusal an operator can act on: when a host's hardware identity is refused because another
// member already reports it, the master records which member that is. Without it the operator is
// told their host has no unique identity and has nothing to do about it.
func TestARefusedHardwareIdentityNamesTheMemberThatHoldsIt(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	const shared = "4c4c4544-0037-3810-8057-b4c04f435331"
	labSystemUUIDs(t, map[string]string{"master-1": shared, "member-1": shared})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if err := ReportPoolSystemUUID(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	row := memberRow(t, master, "member-1")
	if row.SystemUUID != "" {
		t.Fatalf("a duplicate hardware identity was recorded: %q", row.SystemUUID)
	}
	if row.SystemUUIDConflictHostID != "master-1" {
		t.Fatalf("the member that holds the identity was not named: %q", row.SystemUUIDConflictHostID)
	}
}

// Only the master records a fence profile, so only the master has any business connecting to a
// management controller and sending it a password.
func TestAHostThatIsNotTheMasterNeverContactsAManagementController(t *testing.T) {
	lab := newTransferLab(t, 1)
	member := lab.host("member-1")
	asked := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		http.NotFound(w, r)
	}))
	defer server.Close()
	fence := protocol.StorageClusterFence{Kind: protocol.FenceRedfish, Endpoint: strings.TrimPrefix(server.URL, "https://"),
		TargetID: "system-1", Username: "nex-fence", CredentialRef: "cred-1"}
	_, err := pinBMCFenceProfile(member, fence, "member-1")
	if err == nil || err.Error() != "only the pool master records a fence profile" {
		t.Fatalf("a member pinned a profile: %v", err)
	}
	if asked != 0 {
		t.Fatalf("a host that is not the master contacted a management controller %d times", asked)
	}
}

// A controller that refuses in the middle of provisioning used to strand a `nexfence` account
// nothing vouched for, and the host then reported a name conflict against its own account for
// ever. The slot is recorded before anything is written, so the next pass finishes the job.
// (Reported by independent review with a working demonstration.)
func TestAHostFinishesAnAccountAControllerRefusedHalfway(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	labBMCAddresses(t, map[string]string{"192.0.2.11": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	bmc := labInBandBMC(t, "192.0.2.11")
	bmc.refuseWrite = errors.New("this host's management controller refused to set the fence account's password")
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	// The slot the host was using is durable, so the account it may have left behind is one
	// it still knows is its own.
	record := peerState(t, master).LocalBMC
	if record == nil || !record.Created || record.Slot != 3 {
		t.Fatalf("the host forgot the slot it was writing to: %#v", record)
	}
	row := memberRow(t, master, "master-1")
	if row.BMC == nil || row.BMC.State != protocol.PoolBMCUserRefused || row.Fence != nil {
		t.Fatalf("a refused write was not reported as one: %#v", row.BMC)
	}

	bmc.refuseWrite = nil
	// A refused write holds the controller's flash off for one check interval; this test
	// is about the pass that follows that wait.
	record.RetryAfter = time.Time{}
	if err := saveLocalBMCRecord(master, record); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	row = memberRow(t, master, "master-1")
	if row.Fence == nil || !row.Fence.ProvisionedByCore {
		t.Fatalf("the next pass did not finish the account: %#v (%#v)", row.Fence, row.BMC)
	}
	if peerState(t, master).LocalBMC.Slot != 3 {
		t.Fatal("the next pass used a different slot")
	}
}

// A host's record of what it did to its own controller outlives the pool it did it in: the
// account is still on that controller and still this host's to replace. Nothing the old pool
// decided outlives it, so a re-joined host configures its controller again — once, without a
// second account and without reporting a conflict against itself. (Reported by independent
// review of slice 2.)
func TestARejoinedHostConfiguresItsControllerAgainWithoutASecondAccount(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	labBMCAddresses(t, map[string]string{"192.0.2.11": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	bmc := labInBandBMC(t, "192.0.2.11")
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if memberRow(t, master, "master-1").Fence == nil {
		t.Fatal("nothing was recorded to leave behind")
	}

	// Leaving and joining elsewhere: a new pool, which has recorded nothing about this
	// host, while the account on its controller is exactly where it left it.
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Membership.PoolID = "pool-2"
		for i := range state.Membership.Members {
			state.Membership.Members[i].Fence = nil
			state.Membership.Members[i].BMC = nil
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if err = EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	row := memberRow(t, master, "master-1")
	if row.Fence == nil || !row.Fence.ProvisionedByCore {
		t.Fatalf("a re-joined host never configured its controller again: %#v (%#v)", row.Fence, row.BMC)
	}
	if row.BMC == nil || row.BMC.State != protocol.PoolBMCProvisioned {
		t.Fatalf("the new pool was told something other than a working device: %#v", row.BMC)
	}
	// The account it already owned is the account it keeps: no second one, and never a
	// conflict against itself.
	if bmc.made != 2 {
		t.Fatalf("the password was not set on the account this host already owned: %d writes", bmc.made)
	}
	if record := peerState(t, master).LocalBMC; record == nil || record.PoolID != "pool-2" || !record.Created {
		t.Fatalf("the record was not re-stamped with the pool it now belongs to: %#v", record)
	}
}

// Clearing a Core device and then setting one by hand must not leave the host reporting that
// its fence device was withdrawn while a configured device sits beside it on the same row.
// (Reported by independent review of slice 2.)
func TestAManualProfileWinsOverAWithdrawnAutomaticOne(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	labBMCAddresses(t, map[string]string{"192.0.2.11": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	labInBandBMC(t, "192.0.2.11")
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if result := clearFence(t, master, "master-1", "req-clear"); result.Status != "succeeded" {
		t.Fatalf("the operator could not clear the fence device: %#v", result)
	}
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}

	// The operator now records one by hand, which is exactly what Set BMC access… does.
	manual := protocol.StorageClusterFence{Kind: protocol.FenceIPMI, Endpoint: "192.0.2.11", TargetID: "1",
		Username: "operator-fence", CredentialRef: "manual-ref"}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		for i := range state.Membership.Members {
			if state.Membership.Members[i].HostID == "master-1" {
				state.Membership.Members[i].Fence = &manual
			}
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if err = EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	row := memberRow(t, master, "master-1")
	if row.BMC == nil || row.BMC.State != protocol.PoolBMCManualProfile {
		t.Fatalf("a host with a configured manual device reported %#v", row.BMC)
	}
	if row.Fence == nil || row.Fence.Username != "operator-fence" {
		t.Fatalf("the manual device was disturbed: %#v", row.Fence)
	}
}

// The password of an account that no longer exists has nothing left to open, and a root-only
// file holding a live-looking secret that nothing names is not something to leave behind.
// The record names the script whether or not the master ever recorded the profile: a host
// that cleared Pending once the master accepted it used to forget the only name the file had.
// (Reported by independent review of slice 2, finding V4; extended after the slice 4 review.)
func TestRemovingTheAccountRetiresThePasswordItOpened(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		name := "the master never recorded the profile"
		if recorded {
			name = "the master recorded the profile"
		}
		t.Run(name, func(t *testing.T) {
			lab := newTransferLab(t, 1)
			member := lab.host("member-1")
			labSystemUUIDs(t, map[string]string{"member-1": "5b5b5655-0148-4921-9168-c5d15f546442"})
			labBMCAddresses(t, map[string]string{"192.0.2.12": "5b5b5655-0148-4921-9168-c5d15f546442"})
			if err := ReportPoolSystemUUID(context.Background(), member); err != nil {
				t.Fatal(err)
			}
			labInBandBMC(t, "192.0.2.12")
			if !recorded {
				// The master is away, so the host keeps a pending record.
				lab.drop("master-1", poolFenceProvisionPath)
			}
			if err := EnsureLocalBMCFence(context.Background(), member); err != nil {
				t.Fatal(err)
			}
			record := peerState(t, member).LocalBMC
			if record == nil || record.Ref == "" || record.Pending != nil != !recorded {
				t.Fatalf("the host kept no name for the password it set: %#v", record)
			}
			script := filepath.Join(member.dir, "storage-cluster", "fence-credentials", record.Ref)
			if _, err := os.Stat(script); err != nil {
				t.Fatalf("the host did not keep the password it set: %v", err)
			}

			// The operator withdraws the device; the host removes the account on its
			// next pass.
			poolPeerMu.Lock()
			state, err := loadPoolPeer(member)
			if err == nil {
				for i := range state.Membership.Members {
					if state.Membership.Members[i].HostID == "member-1" {
						state.Membership.Members[i].BMCProvisioningDisabled = true
					}
				}
				err = savePoolPeer(member, state)
			}
			poolPeerMu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err = EnsureLocalBMCFence(context.Background(), member); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(script); !os.IsNotExist(err) {
				t.Fatalf("the password of a removed account is still on this host (%v)", err)
			}
		})
	}
}

// Provisioning writes the password script and then records the profile that names it. A host
// interrupted between the two used to lose the only name the file had and write a second one
// on its next pass, leaving a root-only file holding a live-looking secret for ever.
// (Reported by independent review of slice 4.)
func TestAnInterruptedProvisioningLeavesNoSecondPasswordFile(t *testing.T) {
	lab := newTransferLab(t, 1)
	member := lab.host("member-1")
	labSystemUUIDs(t, map[string]string{"member-1": "5b5b5655-0148-4921-9168-c5d15f546442"})
	labBMCAddresses(t, map[string]string{"192.0.2.12": "5b5b5655-0148-4921-9168-c5d15f546442"})
	if err := ReportPoolSystemUUID(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	labInBandBMC(t, "192.0.2.12")
	lab.drop("master-1", poolFenceProvisionPath)
	if err := EnsureLocalBMCFence(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	// The record the host would hold if the save that names the offer never happened: the
	// account and its password are on the controller, and the pass is starting again.
	record := peerState(t, member).LocalBMC
	record.Pending = nil
	if err := saveLocalBMCRecord(member, record); err != nil {
		t.Fatal(err)
	}
	if err := EnsureLocalBMCFence(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(member.dir, "storage-cluster", "fence-credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("this host holds %d fence password scripts for one account", len(files))
	}
}

// A management controller that refuses a later step of account configuration refuses it
// again. Provisioning starts by writing the account name and a new password, so retrying on
// every ten-second reconcile pass would wear out the flash of a controller that will never
// accept the account. (Reported by independent review of slice 4.)
func TestAControllerThatKeepsRefusingIsNotRewrittenEveryPass(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	labBMCAddresses(t, map[string]string{"192.0.2.11": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	bmc := labInBandBMC(t, "192.0.2.11")
	bmc.refuseWrite = errors.New("this host's management controller refused to set the fence account's password")
	written, configure := 0, configureLocalBMCAccount
	configureLocalBMCAccount = func(ctx context.Context, plan vmhost.LocalBMCResult) (string, error) {
		written++
		return configure(ctx, plan)
	}
	for range 5 {
		if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
			t.Fatal(err)
		}
	}
	if written != 1 {
		t.Fatalf("a refusing controller was written to %d times", written)
	}
	if row := memberRow(t, master, "master-1"); row.BMC == nil || row.BMC.State != protocol.PoolBMCUserRefused {
		t.Fatalf("the refusal stopped being reported: %#v", row.BMC)
	}
}

// A member advertises three things about itself and each is recorded on its own. An
// unchanged management-controller report is deliberately not written, and that must not
// swallow a peer origin or a hardware identity sent in the same request.
// (Reported by independent review of slice 2, finding V6.)
func TestAnUnchangedControllerReportDoesNotSwallowAnOriginChange(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	labSystemUUIDs(t, map[string]string{"member-1": "5b5b5655-0148-4921-9168-c5d15f546442"})
	report := protocol.PoolHostBMC{State: protocol.PoolBMCNoInterface,
		Detail: "this host has no local IPMI interface", ObservedAt: time.Now().UTC()}
	if err := postPoolAdvertise(context.Background(), member, map[string]any{"bmc": report}); err != nil {
		t.Fatal(err)
	}
	if row := memberRow(t, master, "member-1"); row.BMC == nil || row.BMC.State != protocol.PoolBMCNoInterface {
		t.Fatalf("the first report was not recorded: %#v", row.BMC)
	}

	// The same finding again, with a new address and a new hardware identity beside it.
	moved := "https://192.0.2.77:9444"
	if err := postPoolAdvertise(context.Background(), member, map[string]any{
		"origin": moved, "system_uuid": "5b5b5655-0148-4921-9168-c5d15f546442", "bmc": report,
	}); err != nil {
		t.Fatal(err)
	}
	row := memberRow(t, master, "member-1")
	if row.PeerOrigin != moved {
		t.Fatalf("an unchanged controller report swallowed the peer origin: %q", row.PeerOrigin)
	}
	if row.SystemUUID != vmhost.NormalizedSystemUUID("5b5b5655-0148-4921-9168-c5d15f546442") {
		t.Fatalf("an unchanged controller report swallowed the hardware identity: %q", row.SystemUUID)
	}
}

// fence_ipmilan strips the whitespace around what the password script prints, so a padded
// password authenticates every check Core makes with ipmitool and fails the one power-off
// that matters. The operator-entered boundary refuses it; the peer boundary, where a modified
// member offers a profile its own Core supposedly made, must refuse it too.
// (Reported by independent review of slice 4.)
func TestAPaddedPasswordAPeerOffersIsRefused(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	fence := protocol.StorageClusterFence{Kind: protocol.FenceIPMI, Endpoint: "192.0.2.11", TargetID: "1",
		Username: vmhost.BMCFenceAccount, Cipher: 17, ProvisionedByCore: true, CredentialRef: "peer-offered"}
	err := AcceptProvisionedFence(master, "member-1", fence, "lab-bmc-password ")
	if err == nil || !strings.Contains(err.Error(), "not a fence device a host configured for itself") {
		t.Fatalf("a password the fence agent would alter was accepted: %v", err)
	}
	script := filepath.Join(master.dir, "storage-cluster", "fence-credentials", fence.CredentialRef)
	if _, err = os.Stat(script); !os.IsNotExist(err) {
		t.Fatalf("a refused password was written anyway (%v)", err)
	}
}
