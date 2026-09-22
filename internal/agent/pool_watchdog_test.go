// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// labWatchdogs makes each host report the watchdog the table gives it. A host with no entry
// reports none, which is what a machine without a watchdog device does.
func labWatchdogs(t *testing.T, byHost map[string]protocol.PoolHostWatchdog) {
	t.Helper()
	previous := localWatchdog
	localWatchdog = func(s *Store) protocol.PoolHostWatchdog {
		if w, ok := byHost[s.State.HostID]; ok {
			return w
		}
		return protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogNone}
	}
	t.Cleanup(func() { localWatchdog = previous })
}

// advertiseAs posts one advertisement over a host's own authenticated peer client, which is
// how the master learns which host is speaking.
func advertiseAs(t *testing.T, lab *transferLab, host *Store, body map[string]any) (int, string) {
	t.Helper()
	client, err := PoolPeerClient(host, lab.origin["master-1"])
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	b, _ := json.Marshal(body)
	resp, err := client.Post(lab.origin["master-1"]+"/api/v1/pool/advertise", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return resp.StatusCode, string(out)
}

func hardwareWatchdog() protocol.PoolHostWatchdog {
	return protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogHardware, Device: "watchdog0",
		Driver: "iTCO_wdt", TimeoutSeconds: 30}
}

// watchdogMethod is the second method on a host's row: what the master says about that host
// shutting itself out.
func watchdogMethod(t *testing.T, master *Store, hostID string) protocol.PoolFenceMethod {
	t.Helper()
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range PoolFenceCheckMembership(master, &membership).Members {
		if member.HostID != hostID || member.FenceReadiness == nil {
			continue
		}
		for _, method := range member.FenceReadiness.Methods {
			if method.Kind == protocol.PoolFenceWatchdog {
				return method
			}
		}
	}
	t.Fatalf("no watchdog method for %s", hostID)
	return protocol.PoolFenceMethod{}
}

// A watchdog is on the machine it would reset, so the host reports it and the master records
// it — while the host is alive, because that is when it can still say. A capability that has
// not changed is not written at all: the membership is signed and replicated, and a settled
// pool must not re-push its authority because a pass ran.
func TestAHostsWatchdogReachesTheMastersMembership(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{
		"master-1": hardwareWatchdog(),
		"member-1": {Kind: protocol.PoolWatchdogSoftware, Device: "watchdog0", Driver: "softdog", TimeoutSeconds: 60},
	})
	before := peerState(t, master).Membership.Generation

	for _, host := range []*Store{master, member} {
		if err := ReportPoolWatchdog(context.Background(), host); err != nil {
			t.Fatalf("%s could not report its watchdog: %v", host.State.HostID, err)
		}
	}
	if got := memberRow(t, master, "master-1").Watchdog; got == nil || *got != hardwareWatchdog() {
		t.Fatalf("the master's own watchdog = %#v", got)
	}
	if got := memberRow(t, master, "member-1").Watchdog; got == nil || got.Kind != protocol.PoolWatchdogSoftware {
		t.Fatalf("the member's watchdog = %#v", got)
	}
	if peerState(t, master).Membership.Generation != before {
		t.Fatal("recording a watchdog moved the membership generation")
	}

	// Twenty more passes with the same finding write nothing at all.
	digest := peerState(t, master).AuthorityDigest
	for i := 0; i < 20; i++ {
		for _, host := range []*Store{master, member} {
			if err := ReportPoolWatchdog(context.Background(), host); err != nil {
				t.Fatal(err)
			}
		}
	}
	if state := peerState(t, master); state.Membership.Generation != before || state.AuthorityDigest != digest {
		t.Fatal("unchanged watchdog reports moved the pool's authority")
	}

	// A host that gains a real watchdog — a BIOS setting turned on, a module loaded — is
	// recorded the next time it says so.
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{"master-1": hardwareWatchdog(), "member-1": hardwareWatchdog()})
	if err := ReportPoolWatchdog(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if got := memberRow(t, master, "member-1").Watchdog; got == nil || got.Kind != protocol.PoolWatchdogHardware {
		t.Fatalf("a changed watchdog was not recorded: %#v", got)
	}
}

// A member cannot make itself look protected by describing itself, and a host may not report
// on another host's behalf: the peer certificate says who is calling and the report is
// recorded against that host. The same door slice 1 and slice 2 established.
func TestAMemberCannotAdvertiseAnInvalidOrForeignWatchdog(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	if status, _ := advertiseAs(t, lab, member, map[string]any{"watchdog": map[string]any{"kind": "titanium"}}); status != 400 {
		t.Fatalf("an unsupported watchdog kind was accepted with %d", status)
	}
	if memberRow(t, master, "member-1").Watchdog != nil {
		t.Fatal("a refused watchdog report was recorded anyway")
	}
	// A valid report from the member is credited to the member, whatever it names.
	if status, body := advertiseAs(t, lab, member, map[string]any{"watchdog": hardwareWatchdog()}); status != 204 {
		t.Fatalf("a member could not advertise its own watchdog: %d %s", status, body)
	}
	if got := memberRow(t, master, "member-1").Watchdog; got == nil || got.Kind != protocol.PoolWatchdogHardware {
		t.Fatalf("the member's own report was not recorded: %#v", got)
	}
	if memberRow(t, master, "master-1").Watchdog != nil {
		t.Fatal("a member's report was written onto another host's row")
	}
}

// Host fencing is a pool decision, so it lives where every other pool decision lives: the
// master's durable, replicated membership. It defaults to on, it survives a restart, it
// reaches every member, and only the master may write it.
func TestHostFencingIsADurableReplicatedPoolSetting(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	if peerState(t, master).Membership.HostFencingDisabled {
		t.Fatal("host fencing was not on for a pool that was never told otherwise")
	}
	if summary := poolSummary(t, master); !summary.HostFencing {
		t.Fatalf("the master reports host fencing off by default: %#v", summary)
	}

	// Only the master keeps it: a member asked to record it refuses and changes nothing.
	if result := ExecutePoolHostFencing(member, hostFencingCommand(false)); result.Status != "failed" ||
		!strings.Contains(result.Error, "only the pool master") {
		t.Fatalf("a member recorded the pool's setting: %#v", result)
	}

	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatalf("the master could not turn host fencing off: %#v", result)
	}
	if !peerState(t, master).Membership.HostFencingDisabled {
		t.Fatal("turning host fencing off was not recorded durably")
	}
	// A desired state, not a transition: the same request again is a success.
	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatalf("re-sending the same setting was a conflict: %#v", result)
	}
	// And a command that is not one is refused rather than guessed at.
	if result := ExecutePoolHostFencing(master, protocol.Command{OperationID: "x"}); result.Status != "failed" {
		t.Fatalf("a command with no host fencing request succeeded: %#v", result)
	}

	// Every member receives it with the pool's records, which is what a member reads.
	if err := PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	stored, err := loadPoolReplica(member)
	if err != nil || stored == nil || !stored.Verified {
		t.Fatalf("the member holds no verified replica: %v", err)
	}
	if !stored.Replica.Membership.HostFencingDisabled {
		t.Fatal("the setting did not reach the member's copy of the pool records")
	}

	// A master change is in flight: the authority is frozen, so the setting waits out the
	// transfer rather than writing into the generation somebody else is about to hold.
	freezeAuthority(t, master, true)
	if result := ExecutePoolHostFencing(master, hostFencingCommand(true)); result.Status != "failed" {
		t.Fatalf("a frozen authority accepted a setting change: %#v", result)
	}
	if !peerState(t, master).Membership.HostFencingDisabled {
		t.Fatal("a refused change was written anyway")
	}
	freezeAuthority(t, master, false)

	// A fresh Controller recovers it from the master's reported membership and from
	// nowhere else.
	if summary := poolSummary(t, master); summary.HostFencing {
		t.Fatalf("the master reports host fencing on after it was turned off: %#v", summary)
	}
	if result := ExecutePoolHostFencing(master, hostFencingCommand(true)); result.Status != "succeeded" {
		t.Fatalf("host fencing could not be turned back on: %#v", result)
	}
	if peerState(t, master).Membership.HostFencingDisabled {
		t.Fatal("turning host fencing back on was not recorded")
	}
}

func hostFencingCommand(enabled bool) protocol.Command {
	return protocol.Command{OperationID: "op-" + map[bool]string{true: "on", false: "off"}[enabled],
		Kind: "pool-host-fencing", Pool: &protocol.PoolCommand{RequestID: "req", Action: "host-fencing",
			Membership: protocol.PoolMembership{MasterHostID: "master-1"}, Enabled: enabled}}
}

// What the watchdog method may say in this slice, and the one thing it may never say. Nothing
// arms a watchdog yet, so no host is ever ready through one — a pool that counted a host as
// protected by an unarmed watchdog would be counting a machine that keeps running through a
// partition.
func TestAWatchdogIsNeverReadinessYetAndSaysWhy(t *testing.T) {
	lab := newTransferLab(t, 2)
	master := lab.host("master-1")
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{
		"master-1": hardwareWatchdog(),
		"member-1": {Kind: protocol.PoolWatchdogSoftware, Device: "watchdog0", Driver: "softdog"},
		"member-2": {Kind: protocol.PoolWatchdogUnknown, Device: "watchdog0", Driver: "acme_wdt"},
	})
	for _, host := range lab.hosts {
		if err := ReportPoolWatchdog(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	for host, want := range map[string]string{
		"master-1": "has not said whether it armed",
		"member-1": "does not count",
		"member-2": "acme_wdt",
	} {
		method := watchdogMethod(t, master, host)
		if method.State == protocol.PoolFenceMethodReady {
			t.Fatalf("%s was reported ready through a watchdog nothing arms: %#v", host, method)
		}
		if !strings.Contains(method.Reason, want) {
			t.Fatalf("%s: reason = %q, wanted it to mention %q", host, method.Reason, want)
		}
	}
	// The pool's count is unchanged by any of it: no host gained a method that is ready.
	if summary := poolSummary(t, master); summary.Ready != 0 {
		t.Fatalf("a watchdog made hosts count as protected: %#v", summary)
	}

	// Turning the setting off is reported as the reason, not as a broken host.
	if result := ExecutePoolHostFencing(master, hostFencingCommand(false)); result.Status != "succeeded" {
		t.Fatal(result.Error)
	}
	if method := watchdogMethod(t, master, "master-1"); !strings.Contains(method.Reason, "turned off") {
		t.Fatalf("with host fencing off the reason is %q", method.Reason)
	}

	// A host too old to report a watchdog is "not reported", never "none".
	if err := ExecutePoolHostFencing(master, hostFencingCommand(true)); err.Status != "succeeded" {
		t.Fatal(err.Error)
	}
	clearWatchdogRow(t, master, "member-1")
	method := watchdogMethod(t, master, "member-1")
	if method.State != protocol.PoolFenceMethodUnknown || !strings.Contains(method.Reason, "upgrade") {
		t.Fatalf("an older host's watchdog was not reported as unknown: %#v", method)
	}
}

// Two hosts have no majority, so no watchdog proof can ever be valid there. That is not a
// pending state and not something an operator can fix, and it must not read as either.
func TestATwoHostPoolSaysAWatchdogDoesNotApply(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{"master-1": hardwareWatchdog(), "member-1": hardwareWatchdog()})
	for _, host := range lab.hosts {
		if err := ReportPoolWatchdog(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"master-1", "member-1"} {
		method := watchdogMethod(t, master, host)
		if method.State != protocol.PoolFenceMethodNotApplicable {
			t.Fatalf("%s: a two-host pool reported a watchdog state of %q", host, method.State)
		}
		if strings.Contains(method.Reason, "yet") || !strings.Contains(method.Reason, "three or more hosts") {
			t.Fatalf("%s: reason = %q", host, method.Reason)
		}
	}
	// And the checklist says the same thing in the pool's own words.
	prerequisite := poolPrerequisite(t, master, protocol.PoolFencePrerequisiteHosts)
	if prerequisite.State != protocol.PoolFencePrerequisiteUnmet || !strings.Contains(prerequisite.Detail, "management controllers") {
		t.Fatalf("the hosts prerequisite = %#v", prerequisite)
	}
}

// The checklist is Core's, computed on the master and reported with the membership, so an
// operator meets it whether or not they ever open the setting. Quorum is honestly unknown
// until a pool cluster exists: claiming it is met would read as though arming were one switch
// away.
func TestTheMasterReportsThePrerequisiteChecklist(t *testing.T) {
	lab := newTransferLab(t, 2)
	master := lab.host("master-1")
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{
		"master-1": hardwareWatchdog(),
		"member-1": hardwareWatchdog(),
		"member-2": {Kind: protocol.PoolWatchdogSoftware, Device: "watchdog0", Driver: "softdog"},
	})
	for _, host := range lab.hosts {
		if err := ReportPoolWatchdog(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	if got := poolPrerequisite(t, master, protocol.PoolFencePrerequisiteHosts); got.State != protocol.PoolFencePrerequisiteMet {
		t.Fatalf("three hosts did not meet the hosts prerequisite: %#v", got)
	}
	watchdogs := poolPrerequisite(t, master, protocol.PoolFencePrerequisiteWatchdogs)
	if watchdogs.State != protocol.PoolFencePrerequisiteUnmet || !slices.Contains(watchdogs.Hosts, "member-2") {
		t.Fatalf("the watchdogs prerequisite does not name the host without one: %#v", watchdogs)
	}
	if got := poolPrerequisite(t, master, protocol.PoolFencePrerequisiteBMC); got.State != protocol.PoolFencePrerequisiteUnmet {
		t.Fatalf("hosts with no confirmed fence device met the BMC prerequisite: %#v", got)
	}
	quorum := poolPrerequisite(t, master, protocol.PoolFencePrerequisiteQuorum)
	if quorum.State != protocol.PoolFencePrerequisiteUnknown || !strings.Contains(quorum.Detail, "not running a Corosync cluster") {
		t.Fatalf("the quorum prerequisite claims something Nex Core cannot observe: %#v", quorum)
	}

	// Every host gaining a hardware watchdog closes that line and nothing else.
	labWatchdogs(t, map[string]protocol.PoolHostWatchdog{
		"master-1": hardwareWatchdog(), "member-1": hardwareWatchdog(), "member-2": hardwareWatchdog(),
	})
	if err := ReportPoolWatchdog(context.Background(), lab.host("member-2")); err != nil {
		t.Fatal(err)
	}
	if got := poolPrerequisite(t, master, protocol.PoolFencePrerequisiteWatchdogs); got.State != protocol.PoolFencePrerequisiteMet {
		t.Fatalf("the watchdogs prerequisite is still unmet: %#v", got)
	}
}

func poolPrerequisite(t *testing.T, master *Store, name string) protocol.PoolFencePrerequisite {
	t.Helper()
	for _, prerequisite := range poolSummary(t, master).Prerequisites {
		if prerequisite.Name == name {
			return prerequisite
		}
	}
	t.Fatalf("the master reports no %q prerequisite", name)
	return protocol.PoolFencePrerequisite{}
}

func clearWatchdogRow(t *testing.T, master *Store, hostID string) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID == hostID {
			state.Membership.Members[i].Watchdog = nil
		}
	}
	if err = savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
}

// freezeAuthority is the state a master change puts a host in: authority writes are refused
// until the transfer is over.
func freezeAuthority(t *testing.T, s *Store, frozen bool) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	state.Transfer = nil
	if frozen {
		state.Transfer = &poolTransferRecord{OperationID: "op-freeze", Action: "promote", Role: "source", Stage: "freeze", Frozen: true}
	}
	if err = savePoolPeer(s, state); err != nil {
		t.Fatal(err)
	}
}
