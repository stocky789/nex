// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// labFenceChecks makes every host's management controller answer the way the table says,
// keyed by the host being checked. A host with no entry answers "ok".
func labFenceChecks(t *testing.T, byTarget map[string]string) {
	t.Helper()
	previous := checkPoolFenceTarget
	checkPoolFenceTarget = func(_ context.Context, _ string, f *protocol.StorageClusterFence, _, secret string) (string, string, string) {
		if secret == "" {
			// The stub must never hide the credential plumbing again: a pass that
			// could not open the target's password is a failure, not an ok.
			t.Errorf("the check of %s ran with no fence credential", f.Endpoint)
		}
		result, ok := byTarget[f.Endpoint]
		if !ok || result == protocol.PoolFenceCheckOK {
			return protocol.PoolFenceCheckOK, "", "on"
		}
		return result, "the lab controller at " + f.Endpoint + " answered " + result, ""
	}
	t.Cleanup(func() { checkPoolFenceTarget = previous })
}

// labFencedPool is a master and one member, each with the fence device its own Core created
// and an identity the pool recorded, which is what a check has to have to run at all.
func labFencedPool(t *testing.T) *transferLab {
	t.Helper()
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
	for host, address := range map[*Store]string{master: "192.0.2.11", member: "192.0.2.12"} {
		if err := ReportPoolSystemUUID(context.Background(), host); err != nil {
			t.Fatal(err)
		}
		labInBandBMC(t, address)
		if err := EnsureLocalBMCFence(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	// A member checks from the copy of the membership it holds, so the pool has to have
	// replicated the fence devices it just recorded before anybody can read one.
	if err := PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, host := range lab.hosts {
		t.Cleanup(func() { ClearPoolFenceChecksForTest(host) })
	}
	return lab
}

// readiness is what the master reports about one host, which is the whole of what a fresh
// Controller reads: the overlay on the membership, never the durable record.
func readiness(t *testing.T, master *Store, hostID string) protocol.PoolFenceMethod {
	t.Helper()
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	reported := PoolFenceCheckMembership(master, &membership)
	for _, member := range reported.Members {
		if member.HostID != hostID {
			continue
		}
		// The fence device is the first method; the watchdog beside it is asserted by
		// the watchdog tests. Readiness is "any method ready", and in this slice a
		// watchdog is never one, so the device's own state still decides it.
		if member.FenceReadiness == nil || len(member.FenceReadiness.Methods) == 0 {
			t.Fatalf("%s reports no fencing readiness: %#v", hostID, member.FenceReadiness)
		}
		if member.FenceReadiness.Ready != (member.FenceReadiness.Methods[0].State == protocol.PoolFenceMethodReady) {
			t.Fatalf("%s is ready without a ready method: %#v", hostID, member.FenceReadiness)
		}
		return member.FenceReadiness.Methods[0]
	}
	t.Fatalf("no row for %s", hostID)
	return protocol.PoolFenceMethod{}
}

// The slice in one test: two hosts read each other's management controller without powering
// anything off, the master keeps what they found, and a fresh Controller reads readiness out
// of the master's reported membership. The durable record never moves.
func TestEachHostChecksTheOthersAndTheMasterReportsReadiness(t *testing.T) {
	lab := labFencedPool(t)
	master, member := lab.host("master-1"), lab.host("member-1")
	labFenceChecks(t, nil)

	// Before anything has been checked, nothing is ready and the master says why.
	if method := readiness(t, master, "member-1"); method.State != protocol.PoolFenceMethodUnknown {
		t.Fatalf("an unchecked host was not reported as unchecked: %#v", method)
	}

	if err := RunPoolFenceChecks(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if err := RunPoolFenceChecks(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"master-1", "member-1"} {
		method := readiness(t, master, host)
		if method.State != protocol.PoolFenceMethodReady || method.Kind != protocol.FenceIPMI {
			t.Fatalf("%s is not fence-ready after both hosts checked: %#v", host, method)
		}
		if len(method.Checks) != 1 || method.Checks[0].Result != protocol.PoolFenceCheckOK || method.Checks[0].Stale {
			t.Fatalf("%s does not name the host that checked it: %#v", host, method.Checks)
		}
	}
	// The member's own row names the master as the host that checked it, and the master's
	// names the member: a host is never its own evidence.
	if checker := readiness(t, master, "member-1").Checks[0].HostID; checker != "master-1" {
		t.Fatalf("the member's check was credited to %s", checker)
	}
	if checker := readiness(t, master, "master-1").Checks[0].HostID; checker != "member-1" {
		t.Fatalf("the master's check was credited to %s", checker)
	}
}

// Every way a fence device can be broken has to stop readiness and say which host found it.
// A host that cannot be shut out must never read as ready, whatever else is recorded for it.
func TestABrokenFenceDeviceIsNeverReadyAndNamesTheHostThatFoundIt(t *testing.T) {
	for _, result := range []string{protocol.PoolFenceCheckUnreachable, protocol.PoolFenceCheckRejected,
		protocol.PoolFenceCheckIdentity, protocol.PoolFenceCheckCertificate, protocol.PoolFenceCheckToolMissing} {
		t.Run(result, func(t *testing.T) {
			lab := labFencedPool(t)
			master, member := lab.host("master-1"), lab.host("member-1")
			labFenceChecks(t, map[string]string{"192.0.2.12": result})
			if err := RunPoolFenceChecks(context.Background(), master); err != nil {
				t.Fatal(err)
			}
			if err := RunPoolFenceChecks(context.Background(), member); err != nil {
				t.Fatal(err)
			}
			method := readiness(t, master, "member-1")
			if method.State == protocol.PoolFenceMethodReady {
				t.Fatalf("a %s fence device was reported ready: %#v", result, method)
			}
			if method.Reason == "" || !strings.Contains(method.Reason, "master-1") {
				t.Fatalf("the reason does not name the host that found it: %q", method.Reason)
			}
			// The other host is unaffected: readiness is per target, not per pool.
			if other := readiness(t, master, "master-1"); other.State != protocol.PoolFenceMethodReady {
				t.Fatalf("one broken controller made another host unready: %#v", other)
			}
		})
	}
}

// A check that stopped being confirmed is not a check. The acceptance gate is exactly this:
// stale is shown as stale and never as ready.
func TestAStaleCheckIsNeverReadiness(t *testing.T) {
	lab := labFencedPool(t)
	master := lab.host("master-1")
	labFenceChecks(t, nil)
	if err := RunPoolFenceChecks(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if method := readiness(t, master, "member-1"); method.State != protocol.PoolFenceMethodReady {
		t.Fatalf("a fresh check was not readiness: %#v", method)
	}
	// Age the one check past what the master is willing to vouch for.
	poolFenceCheckMu.Lock()
	check := poolFenceChecks[master.dir]["master-1"]["member-1"]
	check.ObservedAt = time.Now().Add(-poolFenceCheckStaleAfter() - time.Minute)
	poolFenceChecks[master.dir]["master-1"]["member-1"] = check
	poolFenceCheckMu.Unlock()

	method := readiness(t, master, "member-1")
	if method.State == protocol.PoolFenceMethodReady {
		t.Fatal("a stale check was reported as readiness")
	}
	if len(method.Checks) != 1 || !method.Checks[0].Stale || !strings.Contains(method.Reason, "stale") {
		t.Fatalf("a stale check was not shown as stale: %#v / %q", method.Checks, method.Reason)
	}
}

// Checks are observations, and an observation that repeats must not move the pool's signed
// authority: otherwise every pool re-signs and re-pushes its whole authority replica for
// ever because a check ran. The durable membership is compared byte for byte, which is the
// assertion that actually does the work.
func TestManyCheckingPassesMoveNothingDurable(t *testing.T) {
	lab := labFencedPool(t)
	master, member := lab.host("master-1"), lab.host("member-1")
	labFenceChecks(t, nil)
	before := peerState(t, master)
	durable, err := os.ReadFile(filepath.Join(master.dir, "pool-peer.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		// The interval is what stops a pass; the test is about what a pass costs, so
		// each one is forced.
		poolFenceCheckMu.Lock()
		delete(poolFenceCheckDue, master.dir)
		delete(poolFenceCheckDue, member.dir)
		poolFenceCheckMu.Unlock()
		if err = RunPoolFenceChecks(context.Background(), master); err != nil {
			t.Fatal(err)
		}
		if err = RunPoolFenceChecks(context.Background(), member); err != nil {
			t.Fatal(err)
		}
	}
	after := peerState(t, master)
	if after.AuthorityGeneration != before.AuthorityGeneration || after.AuthorityDigest != before.AuthorityDigest {
		t.Fatalf("checking moved the pool's authority from %d/%s to %d/%s",
			before.AuthorityGeneration, before.AuthorityDigest, after.AuthorityGeneration, after.AuthorityDigest)
	}
	now, err := os.ReadFile(filepath.Join(master.dir, "pool-peer.json"))
	if err != nil || string(now) != string(durable) {
		t.Fatalf("checking rewrote the pool master's durable record (%v)", err)
	}
	// And the readiness a fresh Controller reads is still there, from memory alone.
	if method := readiness(t, master, "member-1"); method.State != protocol.PoolFenceMethodReady {
		t.Fatalf("twenty passes left the pool unready: %#v", method)
	}
}

// A master that restarts has forgotten every observation, and must say so rather than
// reporting the last thing it knew or nothing at all. One interval later it is back.
func TestARestartedMasterReportsNotCheckedYetRatherThanReady(t *testing.T) {
	lab := labFencedPool(t)
	master, member := lab.host("master-1"), lab.host("member-1")
	labFenceChecks(t, nil)
	if err := RunPoolFenceChecks(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if err := RunPoolFenceChecks(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if method := readiness(t, master, "member-1"); method.State != protocol.PoolFenceMethodReady {
		t.Fatalf("the pool was not ready before the restart: %#v", method)
	}

	ClearPoolFenceChecksForTest(master) // The whole of what a master restart loses.
	method := readiness(t, master, "member-1")
	if method.State != protocol.PoolFenceMethodUnknown || method.Reason == "" {
		t.Fatalf("a restarted master did not say it had not checked yet: %#v", method)
	}
	if method.State == protocol.PoolFenceMethodReady {
		t.Fatal("a restarted master reported readiness it no longer has")
	}
	if err := RunPoolFenceChecks(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if method = readiness(t, master, "member-1"); method.State != protocol.PoolFenceMethodReady {
		t.Fatalf("one pass did not re-establish readiness: %#v", method)
	}
}

// A host does not get to say whether it can be shut out — the same rule slice 1 set for
// system_uuid and slice 2 for the fence device, applied to this door.
func TestAMemberCannotReportAFenceCheckAboutItself(t *testing.T) {
	lab := labFencedPool(t)
	master, member := lab.host("master-1"), lab.host("member-1")
	// The member's own mTLS peer request, carrying a flattering claim about itself. Both
	// results are stamped with the profile the pool really records, so nothing but the
	// self-report rule can be what drops the first one.
	if err := postPoolFenceChecks(context.Background(), member, []protocol.PoolFenceCheck{
		{HostID: "member-1", Result: protocol.PoolFenceCheckOK, Power: "on",
			Profile: protocol.PoolFenceProfileStamp(memberRow(t, master, "member-1"))},
		{HostID: "master-1", Result: protocol.PoolFenceCheckUnreachable, Detail: "the member says so",
			Profile: protocol.PoolFenceProfileStamp(memberRow(t, master, "master-1"))},
	}); err != nil {
		t.Fatal(err)
	}
	if method := readiness(t, master, "member-1"); method.State == protocol.PoolFenceMethodReady {
		t.Fatalf("a member made itself fence-ready by saying so: %#v", method)
	}
	if method := readiness(t, master, "master-1"); method.State == protocol.PoolFenceMethodReady {
		t.Fatalf("the member's report about the master was dropped: %#v", method)
	}
}

// The reason table, which is what an operator reads on the row. Every provisioning state
// slice 2 can leave a host in has to arrive here as its own sentence, and none of them is
// ever ready.
func TestReadinessFoldsInEveryProvisioningState(t *testing.T) {
	uuid := "4c4c4544-0037-3810-8057-b4c04f435331"
	fence := &protocol.StorageClusterFence{Kind: protocol.FenceIPMI, Endpoint: "192.0.2.11", TargetID: "1",
		Username: vmhost.BMCFenceAccount, Cipher: 17, ProvisionedByCore: true, CredentialRef: "ref-1"}
	for name, row := range map[string]struct {
		member protocol.PoolMember
		want   string
	}{
		"no device at all":         {protocol.PoolMember{SystemUUID: uuid}, "no fence device is recorded"},
		"withdrawn by an operator": {protocol.PoolMember{SystemUUID: uuid, BMCProvisioningDisabled: true}, "an operator withdrew"},
		"no local interface": {protocol.PoolMember{SystemUUID: uuid, BMC: &protocol.PoolHostBMC{
			State: protocol.PoolBMCNoInterface, Detail: "this host has no local IPMI interface"}}, "no local IPMI interface"},
		"in-band user management refused": {protocol.PoolMember{SystemUUID: uuid, BMC: &protocol.PoolHostBMC{
			State: protocol.PoolBMCUserRefused, Detail: "this controller refuses in-band user management"}}, "refuses in-band user management"},
		"an account somebody else made": {protocol.PoolMember{SystemUUID: uuid, BMC: &protocol.PoolHostBMC{
			State: protocol.PoolBMCNameConflict, Detail: "an account under the Nex name already exists"}}, "already exists"},
		"the master has not recorded it yet": {protocol.PoolMember{SystemUUID: uuid, BMC: &protocol.PoolHostBMC{
			State: protocol.PoolBMCPendingMaster, Detail: "the pool master has not recorded it yet"}}, "not recorded it yet"},
		"two hosts claiming one identity":    {protocol.PoolMember{SystemUUIDConflictHostID: "member-2"}, "member-2"},
		"a device with no hardware identity": {protocol.PoolMember{Fence: fence}, "unique hardware identity"},
	} {
		t.Run(name, func(t *testing.T) {
			out := poolFenceReadiness(t.TempDir(), row.member, nil, 2, true, vmhost.PoolQuorum{})
			if out.Ready || out.Methods[0].State == protocol.PoolFenceMethodReady {
				t.Fatalf("%s was reported fence-ready: %#v", name, out)
			}
			if !strings.Contains(out.Methods[0].Reason, row.want) {
				t.Fatalf("reason = %q, wanted it to mention %q", out.Methods[0].Reason, row.want)
			}
		})
	}
}

// labIPMIController is a fake ipmitool that answers both commands a check issues, for every
// address the table names, and records every invocation with its environment. It exists to
// prove the two things a wholesale stub of the check cannot: that the password reaches the
// tool without ever entering the process list, and that nothing but a read is ever issued.
type labIPMIController struct{ log string }

func labIPMIControllers(t *testing.T, byAddress map[string]string) *labIPMIController {
	t.Helper()
	lab := &labIPMIController{log: filepath.Join(t.TempDir(), "invocations")}
	answers := t.TempDir()
	for address, uuid := range byAddress {
		if err := os.WriteFile(filepath.Join(answers, address), []byte("System GUID  : "+uuid+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s|%s\\n' \"$*\" \"$IPMI_PASSWORD\" >> " + lab.log + "\n" +
		"address=\nwhile [ $# -gt 0 ]; do if [ \"$1\" = \"-H\" ]; then address=$2; fi; " +
		"if [ \"$1\" = chassis ]; then printf 'Chassis Power is on\\n'; exit 0; fi; shift; done\n" +
		"exec cat " + filepath.Join(answers, "$address") + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ipmitool"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return lab
}

func (l *labIPMIController) invocations(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(l.log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(body), "\n"), "\n")
}

// The test the first round of this slice did not have: a whole checking pass with nothing
// stubbed but the management controllers themselves, on a member, against the pool master's
// controller. A member holds no other host's fence credential on disk — only the sealed copy
// inside its own authority replica — so this is the path that decides whether the pool master,
// the one host the whole feature exists for, can ever be reported as protected.
// (Reported by independent review with a working demonstration.)
func TestAMemberChecksTheMastersControllerWithTheCredentialItsReplicaCarries(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	masterUUID, memberUUID := "4c4c4544-0037-3810-8057-b4c04f435331", "5b5b5655-0148-4921-9168-c5d15f546442"
	labSystemUUIDs(t, map[string]string{"master-1": masterUUID, "member-1": memberUUID})
	ipmi := labIPMIControllers(t, map[string]string{"192.0.2.11": masterUUID})
	for _, host := range []*Store{master, member} {
		if err := ReportPoolSystemUUID(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	// The master's own device, configured in-band exactly as slice 2 does it.
	labInBandBMC(t, "192.0.2.11")
	if err := EnsureLocalBMCFence(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	// The member's device is a Redfish one an operator recorded, so one pass covers both
	// protocols: the member reads IPMI, the master reads Redfish.
	redfish := labRedfishBMC(t, memberUUID)
	writeLabBMCCredential(t, master, "cred-member")
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("op-redfish", "master-1", "member-1",
		labBMCProfile(redfish, "cred-member"))); result.Status != "succeeded" {
		t.Fatalf("the member's controller was not recorded: %#v", result)
	}
	if err := PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ClearPoolFenceChecksForTest(master); ClearPoolFenceChecksForTest(member) })

	for _, host := range []*Store{member, master} {
		if err := RunPoolFenceChecks(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"master-1", "member-1"} {
		method := readiness(t, master, host)
		if method.State != protocol.PoolFenceMethodReady {
			t.Fatalf("%s was not checked successfully by the other host: %#v", host, method)
		}
		if len(method.Checks) != 1 || method.Checks[0].Power != "on" {
			t.Fatalf("%s: the power state was not read: %#v", host, method.Checks)
		}
	}
	if summary := poolSummary(t, master); summary.Ready != 2 || summary.Total != 2 {
		t.Fatalf("the pool master did not report both hosts as protected: %#v", summary)
	}
	// Never on disk: the member holds no credential of the master's, before or after.
	if _, err := os.Stat(filepath.Join(member.dir, "storage-cluster", "fence-credentials",
		memberRow(t, master, "master-1").Fence.CredentialRef)); !os.IsNotExist(err) {
		t.Fatalf("checking wrote another host's fence credential onto a member (%v)", err)
	}
	// Never in the process list, and never anything but a read.
	ran := ipmi.invocations(t)
	if len(ran) < 2 {
		t.Fatalf("the member did not issue both reads: %v", ran)
	}
	for _, line := range ran {
		args, password, _ := strings.Cut(line, "|")
		if password != "lab-bmc-password" {
			t.Fatalf("the fence account's password did not reach ipmitool through its environment: %q", line)
		}
		if strings.Contains(args, "lab-bmc-password") {
			t.Fatalf("the fence account's password was in the process list: %q", args)
		}
		if command := args[strings.LastIndex(args, " -E ")+4:]; command != "mc guid" && command != "chassis power status" {
			t.Fatalf("a check issued something other than a read: %q", args)
		}
	}
}

// The same guarantee across a whole pass rather than one call, over both protocols and past a
// laboratory virsh device: nothing a pass does can change a machine's power.
// (The per-call form of this assertion is in internal/vmhost.)
func TestAWholeCheckingPassIssuesOnlyReads(t *testing.T) {
	lab := newTransferLab(t, 3)
	master := lab.host("master-1")
	uuids := map[string]string{
		"master-1": "4c4c4544-0037-3810-8057-b4c04f435331",
		"member-1": "5b5b5655-0148-4921-9168-c5d15f546442",
		"member-2": "6c6c6766-0259-5a32-a279-d6e26a657553",
		"member-3": "7d7d7877-036a-6b43-b38a-e7f37b768664",
	}
	labSystemUUIDs(t, uuids)
	ipmi := labIPMIControllers(t, map[string]string{"192.0.2.12": uuids["member-1"]})
	for _, id := range []string{"master-1", "member-1", "member-2", "member-3"} {
		if err := ReportPoolSystemUUID(context.Background(), lab.host(id)); err != nil {
			t.Fatal(err)
		}
	}
	// One IPMI device, one Redfish device, and one laboratory virsh device, all read by
	// the master in a single pass.
	labInBandBMC(t, "192.0.2.12")
	if err := EnsureLocalBMCFence(context.Background(), lab.host("member-1")); err != nil {
		t.Fatal(err)
	}
	requests := []string{}
	redfish := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if user, password, ok := r.BasicAuth(); !ok || user != "nex-fence" || password != "lab-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/redfish/v1/Systems/system-1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Id":"system-1","UUID":"` + uuids["member-2"] + `","PowerState":"On"}`))
	}))
	t.Cleanup(redfish.Close)
	writeLabBMCCredential(t, master, "cred-2")
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("op-2", "master-1", "member-2",
		labBMCProfile(redfish, "cred-2"))); result.Status != "succeeded" {
		t.Fatalf("the Redfish device was not recorded: %#v", result)
	}
	virsh := &protocol.StorageClusterFence{Kind: protocol.FenceVirsh, Endpoint: "lab.example.test",
		TargetID: "9f1d5f6e-6a7b-4c8d-9e0f-1a2b3c4d5e6f", VirshURI: "qemu:///system",
		SSHLogin: "fence", SSHPort: 22, CredentialRef: "cred-virsh", SSHHostKeyRef: "cred-virsh"}
	setMemberFence(t, master, "member-3", virsh)
	t.Cleanup(func() { ClearPoolFenceChecksForTest(master) })

	requests = requests[:0]
	if err := RunPoolFenceChecks(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	if len(requests) == 0 {
		t.Fatal("the pass never reached the Redfish controller")
	}
	for _, request := range requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("a checking pass sent a state-changing Redfish request: %q", request)
		}
	}
	for _, line := range ipmi.invocations(t) {
		if command := line[strings.LastIndex(line, " -E ")+4:]; !strings.HasPrefix(command, "mc guid") && !strings.HasPrefix(command, "chassis power status") {
			t.Fatalf("a checking pass issued %q", line)
		}
	}
	// A virsh device is somebody else's libvirt. A pass never reaches for one at all, so
	// no fence agent and no ssh runs for it: the master reports it usable from the
	// membership alone and says plainly that it is not continuously checked.
	if checks := readiness(t, master, "member-3").Checks; len(checks) != 0 {
		t.Fatalf("a checking pass contacted a laboratory virsh device: %#v", checks)
	}
	if method := readiness(t, master, "member-3"); method.Kind != protocol.FenceVirsh ||
		!strings.Contains(method.Reason, "does not check continuously") {
		t.Fatalf("the virsh device was not reported as the lab device it is: %#v", method)
	}
}

// setMemberFence records a profile straight into the master's membership, for a device the
// recording path would refuse to prove in a unit test (a laboratory virsh device names a
// libvirt host that does not exist here).
func setMemberFence(t *testing.T, master *Store, hostID string, fence *protocol.StorageClusterFence) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID == hostID {
			state.Membership.Members[i].Fence = fence
		}
	}
	if err = savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
}

// poolSummary is the pool's own count, setting and prerequisite checklist, which the pool
// master computes and reports with its membership. Nothing adds up rows outside Core.
func poolSummary(t *testing.T, master *Store) protocol.PoolFenceSummary {
	t.Helper()
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	reported := PoolFenceCheckMembership(master, &membership)
	if reported.FenceSummary == nil {
		t.Fatalf("the pool master reports no pool fencing summary: %#v", reported)
	}
	return *reported.FenceSummary
}

// A check is evidence about the device it was taken against and nothing else. Replacing a
// host's fence device must not let the new one inherit the old one's success, or the column
// reads "checked from 2 hosts" for a controller nobody has ever reached.
// (Reported by independent review.)
func TestReplacingAFenceDeviceDropsTheChecksTakenAgainstTheOldOne(t *testing.T) {
	lab := labFencedPool(t)
	master := lab.host("master-1")
	labFenceChecks(t, nil)
	for _, host := range []*Store{master, lab.host("member-1")} {
		if err := RunPoolFenceChecks(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	if method := readiness(t, master, "member-1"); method.State != protocol.PoolFenceMethodReady {
		t.Fatalf("the member was not ready to begin with: %#v", method)
	}
	if summary := poolSummary(t, master); summary.Ready != 2 {
		t.Fatalf("both hosts should be protected to begin with: %#v", summary)
	}

	// The operator records a different controller for that host — a new address, which is
	// what a re-racked machine or a corrected typo looks like.
	replaced := *memberRow(t, master, "member-1").Fence
	replaced.Endpoint = "192.0.2.99"
	setMemberFence(t, master, "member-1", &replaced)

	method := readiness(t, master, "member-1")
	if method.State == protocol.PoolFenceMethodReady {
		t.Fatalf("a replaced fence device inherited the old one's check: %#v", method)
	}
	if len(method.Checks) != 0 || !strings.Contains(method.Reason, "checked") {
		t.Fatalf("the replaced device was not reported as unchecked: %#v / %q", method.Checks, method.Reason)
	}
	summary := poolSummary(t, master)
	if summary.Ready != 1 || len(summary.Unready) != 1 || summary.Unready[0] != "member-1" {
		t.Fatalf("the pool count still counted the replaced device: %#v", summary)
	}

	// The narrow case: the same controller, the same account, a rotated password. Every
	// other field is identical, so only the credential in the stamp separates a check
	// taken with the old password from the one nobody has used yet.
	// (Reported by independent review.)
	for _, host := range []*Store{master, lab.host("member-1")} {
		poolFenceCheckMu.Lock()
		delete(poolFenceCheckDue, host.dir)
		poolFenceCheckMu.Unlock()
		if err := RunPoolFenceChecks(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	if method := readiness(t, master, "member-1"); method.State != protocol.PoolFenceMethodReady {
		t.Fatalf("the replaced device was not confirmed by the next pass: %#v", method)
	}
	rotated := *memberRow(t, master, "member-1").Fence
	setMemberFenceCredentialVersion(t, master, "member-1", rotated, 2)
	if method := readiness(t, master, "member-1"); method.State == protocol.PoolFenceMethodReady {
		t.Fatalf("a rotated fence credential inherited the old password's check: %#v", method)
	}
}

// setMemberFenceCredentialVersion rotates the private material behind an otherwise unchanged
// profile, which is what the master's own credential counter records.
func setMemberFenceCredentialVersion(t *testing.T, master *Store, hostID string, fence protocol.StorageClusterFence, version uint64) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID == hostID {
			state.Membership.Members[i].Fence = &fence
			state.Membership.Members[i].FenceCredentialVersion = version
		}
	}
	if err = savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
}

// A pool of one host has nobody to be checked from, ever, and nothing to promote over either.
// Telling the operator to wait for a check that cannot happen would be untrue.
// (Reported by independent review.)
func TestASingleHostPoolSaysThereIsNoOtherHostToCheckFrom(t *testing.T) {
	lab := newTransferLab(t, 0)
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
	t.Cleanup(func() { ClearPoolFenceChecksForTest(master) })

	method := readiness(t, master, "master-1")
	if method.State != protocol.PoolFenceMethodNoPeer {
		t.Fatalf("a single-host pool was reported as waiting for a check: %#v", method)
	}
	if strings.Contains(method.Reason, "yet") {
		t.Fatalf("the reason reads as a pending state: %q", method.Reason)
	}
	if summary := poolSummary(t, master); summary.Total != 1 {
		t.Fatalf("the pool count is wrong for one host: %#v", summary)
	}
}

// An ejected host is a tombstone in the membership, not a member. Counting it for ever as a
// host that cannot be shut out is a wrong number on the operator's screen.
// (Reported by independent review.)
func TestThePoolCountIgnoresEjectedAndJoiningHosts(t *testing.T) {
	lab := labFencedPool(t)
	master := lab.host("master-1")
	labFenceChecks(t, nil)
	for _, host := range []*Store{master, lab.host("member-1")} {
		if err := RunPoolFenceChecks(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	if summary := poolSummary(t, master); summary.Ready != 2 || summary.Total != 2 {
		t.Fatalf("both hosts should be protected: %#v", summary)
	}

	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		for i := range state.Membership.Members {
			if state.Membership.Members[i].HostID == "member-1" {
				state.Membership.Members[i].Status = "ejected"
			}
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	summary := poolSummary(t, master)
	if summary.Total != 1 || slices.Contains(summary.Unready, "member-1") {
		t.Fatalf("an ejected host is still counted as one of this pool's hosts: %#v", summary)
	}
}

// A pass the pass timeout cuts short posts only the targets it reached. Keeping just those
// would delete this checker's earlier results for the rest, so a host that was checked and
// found broken would read as never checked instead of ageing to stale.
func TestATruncatedPassKeepsTheResultsItDidNotReach(t *testing.T) {
	dir := t.TempDir()
	recordPoolFenceChecks(dir, "host-a", []protocol.PoolFenceCheck{
		{HostID: "host-b", Result: protocol.PoolFenceCheckOK},
		{HostID: "host-c", Result: protocol.PoolFenceCheckRejected},
	})
	recordPoolFenceChecks(dir, "host-a", []protocol.PoolFenceCheck{{HostID: "host-b", Result: protocol.PoolFenceCheckOK}})
	poolFenceCheckMu.Lock()
	kept := poolFenceChecks[dir]["host-a"]["host-c"]
	poolFenceCheckMu.Unlock()
	if kept.Result != protocol.PoolFenceCheckRejected {
		t.Fatalf("a target the truncated pass did not reach lost its result: %#v", kept)
	}
}
