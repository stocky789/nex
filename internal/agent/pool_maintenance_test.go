// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func maintenanceMaster(t *testing.T, s *Store) {
	t.Helper()
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
}

func maintenanceMembership(t *testing.T, s *Store, master string, members ...protocol.PoolMember) protocol.PoolMembership {
	t.Helper()
	maintenanceMaster(t, s)
	membership := protocol.PoolMembership{PoolID: s.State.PoolID, MasterHostID: master, Generation: 4, Members: members}
	if err := membership.Valid(); err != nil {
		t.Fatalf("fixture membership is invalid: %v", err)
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	state.Membership = membership
	if err = savePoolPeer(s, state); err != nil {
		t.Fatal(err)
	}
	return membership
}

func maintenanceMember(host string, maintenance bool) protocol.PoolMember {
	return protocol.PoolMember{HostID: host, Hostname: host, CertificateSHA256: strings.Repeat("a", 64), Generation: 4, Status: "ready", Maintenance: maintenance}
}

func maintenanceCommand(master, member string, enabled bool, expected uint64) protocol.Command {
	return protocol.Command{OperationID: "op-1", Kind: "pool-maintenance", Pool: &protocol.PoolCommand{
		RequestID: "req-1", Action: "maintenance", MemberHostID: member, Enabled: enabled, ExpectedVersion: expected,
		Membership: protocol.PoolMembership{MasterHostID: master},
	}}
}

func memberRecord(t *testing.T, s *Store, host string) protocol.PoolMember {
	t.Helper()
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range membership.Members {
		if member.HostID == host {
			return member
		}
	}
	t.Fatalf("no membership record for %s", host)
	return protocol.PoolMember{}
}

// The flag is durable on the master, survives a reload, is idempotent in both directions and
// never disturbs the membership generation or its validity rule.
func TestPoolMaintenanceFlagIsDurableAndIdempotent(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	maintenanceMembership(t, master, "master-1", maintenanceMember("master-1", false), maintenanceMember("member-2", false))
	if result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "member-2", true, 4)); result.Status != "succeeded" {
		t.Fatalf("enable: %+v", result)
	}
	drained := memberRecord(t, master, "member-2")
	if !drained.Maintenance || drained.MaintenanceSince.IsZero() {
		t.Fatalf("the flag was not recorded: %+v", drained)
	}
	// Re-read pool-peer.json, which is what a restarted master does: Membership loads the
	// file every time, and the reported membership is how a fresh Controller re-adopts it.
	reloaded, err := Membership(master)
	if err != nil || reloaded.Generation != 4 || reloaded.Valid() != nil {
		t.Fatalf("membership after reload: %+v %v", reloaded, err)
	}
	if !reloaded.Members[1].Maintenance || !reloaded.Members[1].MaintenanceSince.Equal(drained.MaintenanceSince) {
		t.Fatalf("the flag did not survive a reload: %+v", reloaded.Members[1])
	}
	if reloaded.Members[0].Maintenance {
		t.Fatal("draining one host drained another")
	}
	// An exact retry of the same request is a success, not a conflict, and does not move
	// the recorded time: that is what makes a lost reply safe to retry.
	if result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "member-2", true, 4)); result.Status != "succeeded" {
		t.Fatalf("repeat enable: %+v", result)
	}
	if again := memberRecord(t, master, "member-2"); !again.MaintenanceSince.Equal(drained.MaintenanceSince) {
		t.Fatalf("a repeated enable moved the recorded time: %v -> %v", drained.MaintenanceSince, again.MaintenanceSince)
	}
	if result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "member-2", false, 4)); result.Status != "succeeded" {
		t.Fatalf("disable: %+v", result)
	}
	cleared := memberRecord(t, master, "member-2")
	if cleared.Maintenance || !cleared.MaintenanceSince.IsZero() {
		t.Fatalf("the flag was not cleared: %+v", cleared)
	}
	if result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "member-2", false, 4)); result.Status != "succeeded" {
		t.Fatalf("repeat disable: %+v", result)
	}
	// A single-host pool is still a pool, and its one host is its master: draining it is
	// allowed, and it is the master's own record that refuses work on it afterwards.
	single := peerStore(t, "solo-1", "pool-9")
	maintenanceMembership(t, single, "solo-1", maintenanceMember("solo-1", false))
	if result := ExecutePoolMaintenance(single, maintenanceCommand("solo-1", "solo-1", true, 0)); result.Status != "succeeded" {
		t.Fatalf("single-host drain: %+v", result)
	}
	if !memberRecord(t, single, "solo-1").Maintenance {
		t.Fatal("a single-host pool did not record its own drain")
	}
}

func TestPoolMaintenanceRefusals(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	maintenanceMembership(t, master, "master-1", maintenanceMember("master-1", false), maintenanceMember("member-2", false))
	for name, tc := range map[string]struct {
		cmd      protocol.Command
		store    *Store
		contains string
	}{
		"no request":          {cmd: protocol.Command{OperationID: "op-1", Kind: "pool-maintenance"}, contains: "carries no maintenance request"},
		"no member":           {cmd: maintenanceCommand("master-1", "", true, 4), contains: "requires a member host"},
		"no master":           {cmd: maintenanceCommand("", "member-2", true, 4), contains: "requires the pool master"},
		"unknown member":      {cmd: maintenanceCommand("master-1", "member-9", true, 4), contains: "no such member of this pool"},
		"stale generation":    {cmd: maintenanceCommand("master-1", "member-2", true, 3), contains: "membership changed"},
		"addressed elsewhere": {cmd: maintenanceCommand("member-2", "member-2", true, 4), contains: "only the pool master"},
	} {
		t.Run(name, func(t *testing.T) {
			store := master
			if tc.store != nil {
				store = tc.store
			}
			result := ExecutePoolMaintenance(store, tc.cmd)
			if result.Status != "failed" || !strings.Contains(result.Error, tc.contains) {
				t.Fatalf("result %+v does not report %q", result, tc.contains)
			}
		})
	}
	// A member is not an authority on its own drain state, even for itself.
	member := peerStore(t, "member-2", "pool-1")
	maintenanceMembership(t, member, "master-1", maintenanceMember("master-1", false), maintenanceMember("member-2", false))
	if result := ExecutePoolMaintenance(member, maintenanceCommand("master-1", "member-2", true, 4)); result.Status != "failed" || !strings.Contains(result.Error, "only the pool master") {
		t.Fatalf("a member recorded its own maintenance: %+v", result)
	}
	if memberRecord(t, member, "member-2").Maintenance {
		t.Fatal("a member wrote a drain flag into its membership copy")
	}
	// The command is routed to the master, not to the host being drained, and is admitted
	// as a durable managed command.
	if !managedCommand("pool-maintenance") {
		t.Fatal("pool-maintenance is not an admitted managed command")
	}
	if host := commandHost(maintenanceCommand("master-1", "member-2", true, 4)); host != "master-1" {
		t.Fatalf("command host is %q, want the master", host)
	}
}

// New placements and new starts are refused on a drained host; everything that keeps a
// running guest recoverable is not.
func TestPoolMaintenanceRefusesPlacementAndStartButNeverStop(t *testing.T) {
	ctx := context.Background()
	const vm = "11111111-1111-4111-8111-111111111111"
	s := peerStore(t, "host-a", "pool-1")
	membership := maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false), maintenanceMember("member-2", true))
	// Define and start while this host is not drained, so the refusals below are the flag
	// and not the execution record.
	if err := poolVMLocal(s, vm, "define", nil, true, poolVMTestSpec(vm, "host-a"), nil); err != nil {
		t.Fatal(err)
	}
	permit, epoch := "", uint64(0)
	if err := poolVMLocalBound(s, vm, "start", "maintenance-start-1", 0, &permit, false, nil, &epoch); err != nil {
		t.Fatalf("start before maintenance: %v", err)
	}
	if err := poolVMLocalBound(s, vm, "running", "maintenance-start-1", epoch, nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	// A member the master has drained is refused before any peer request is attempted:
	// the unreachable origin below would fail differently.
	drained := protocol.PoolMember{HostID: "member-2", Generation: 4, Status: "ready", PeerOrigin: "https://127.0.0.1:1"}
	create := protocol.Command{OperationID: "op-place", Kind: "vm-create", VM: poolVMTestSpec(vm, "member-2")}
	if _, err := poolVMPlacement(ctx, s, membership, drained, create); err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("placement onto a drained member: %v", err)
	}
	// Now drain this host and check its own paths.
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", true), maintenanceMember("member-2", true))
	local := protocol.Command{OperationID: "op-local", Kind: "vm-create", VM: poolVMTestSpec(vm, "host-a")}
	if _, err := localPoolVMPlacement(ctx, s, 4, "op-local", local); err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("local placement preflight on a drained host: %v", err)
	}
	if err := poolHostInMaintenance(s, "host-a"); err == nil {
		t.Fatal("the guard the autostart pass uses does not refuse a drained host")
	}
	// Stopping is never blocked: an operator draining a host is usually about to power
	// guests off, and a running guest must always be stoppable.
	if err := poolVMLocal(s, vm, "stop", nil, false, nil, nil); err != nil {
		t.Fatalf("stop was refused during maintenance: %v", err)
	}
	if err := poolVMLocal(s, vm, "stopped", nil, false, nil, nil); err != nil {
		t.Fatalf("a stop could not be recorded during maintenance: %v", err)
	}
	// Only now, with the guest stopped, is a start refused.
	if err := poolVMLocal(s, vm, "start", nil, false, nil, nil); err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("start on a drained host: %v", err)
	}
	// Leaving maintenance restores starts with no other operator action.
	if result := ExecutePoolMaintenance(s, maintenanceCommand("host-a", "host-a", false, 4)); result.Status != "succeeded" {
		t.Fatalf("exit maintenance: %+v", result)
	}
	if err := poolVMLocalBound(s, vm, "start", "maintenance-start-2", 0, &permit, false, nil, &epoch); err != nil {
		t.Fatalf("start after maintenance: %v", err)
	}
}

// Draining a host must not disturb the membership rules every peer check depends on.
func TestPoolMaintenanceDoesNotAffectMembershipValidity(t *testing.T) {
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 7, Members: []protocol.PoolMember{
		{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 7, Status: "ready"},
		{HostID: "member-2", Hostname: "member-2", CertificateSHA256: strings.Repeat("b", 64), Generation: 7, Status: "ready", Maintenance: true, MaintenanceSince: time.Now().UTC()},
	}}
	if err := membership.Valid(); err != nil {
		t.Fatalf("a drained member invalidated the membership: %v", err)
	}
	if err := poolMaintenanceRefusal(membership, "master-1"); err != nil {
		t.Fatalf("a host that is not drained was refused: %v", err)
	}
	if err := poolMaintenanceRefusal(membership, "member-2"); err == nil {
		t.Fatal("a drained host was not refused")
	}
	if err := poolMaintenanceRefusal(membership, "member-9"); err != nil {
		t.Fatalf("an unknown host was treated as drained: %v", err)
	}
}

// C-8. A guest whose disks are all local storage leaves AdmitPoolVMExecution before any
// master contact, so every maintenance guard downstream of that round trip misses it. The
// refusal has to sit at the same choke point as the restriction one.
func TestMaintenanceRefusesStartingALocalStorageGuest(t *testing.T) {
	ctx := context.Background()
	const vm = "11111111-1111-4111-8111-111111111111"
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", true))
	power := func(action string) protocol.Command {
		return protocol.Command{OperationID: "op-" + action, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm, PoolID: "pool-1", HostID: "host-a", Action: action}}
	}
	if _, err := AdmitPoolVMExecution(ctx, s, power("start")); err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("a drained host admitted a local-storage start: %v", err)
	}
	// A create that asks for the guest to be started begins a new execution too.
	create := protocol.Command{OperationID: "op-create", Kind: "vm-create", VM: poolVMTestSpec(vm, "host-a")}
	create.VM.StartAfterCreate = true
	if _, err := AdmitPoolVMExecution(ctx, s, create); err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("a drained host admitted a local-storage create-and-start: %v", err)
	}
	// Nothing that reduces or keeps execution is refused by the drain: an operator draining
	// a host is usually about to power guests off, and a reboot of a guest already running
	// here is not a new placement. These fail for their own reasons; never for the drain.
	for _, action := range []string{"shutdown", "force-stop", "reboot", "force-reboot"} {
		if _, err := AdmitPoolVMExecution(ctx, s, power(action)); err != nil && strings.Contains(err.Error(), "maintenance mode") {
			t.Fatalf("a drained host refused %s: %v", action, err)
		}
	}
	// Leaving maintenance restores the start with no other operator action.
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	if _, err := AdmitPoolVMExecution(ctx, s, power("start")); err != nil && strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("a host out of maintenance was still refused: %v", err)
	}
}

// P-5. A start the engine turns into an S3 wake is still an operator pressing Start.
func TestMaintenanceRefusesWakingAGuestOnADrainedHost(t *testing.T) {
	const vm = "11111111-1111-4111-8111-111111111111"
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	if err := poolVMLocal(s, vm, "define", nil, true, poolVMTestSpec(vm, "host-a"), nil); err != nil {
		t.Fatal(err)
	}
	permit, epoch := "", uint64(0)
	if err := poolVMLocalBound(s, vm, "start", "wake-setup", 0, &permit, false, nil, &epoch); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocalBound(s, vm, "running", "wake-setup", epoch, nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", true))
	if err := poolVMLocal(s, vm, "wake", nil, false, nil, nil); err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("a drained host woke a suspended guest: %v", err)
	}
	// A reboot of a guest that is already executing here is allowed, by decision.
	for _, action := range []string{"reboot", "force-reboot"} {
		if err := poolVMLocal(s, vm, action, nil, false, nil, nil); err != nil {
			t.Fatalf("a drained host refused %s of a running guest: %v", action, err)
		}
	}
}

// Test gap 7. The master-side peer guard is the one that covers members, which is the
// primary case for maintenance, and nothing drove it.
func TestMaintenanceRefusesAMemberStartThroughThePeerHandler(t *testing.T) {
	ctx := context.Background()
	master, member, _, invitation := poolJoinLab(t)
	joinLabMember(t, member, invitation)
	placementPeer(t, master, member)
	const vm = "11111111-1111-4111-8111-111111111111"
	spec := poolVMTestSpec(vm, member.State.HostID)
	if err := poolVMDefineRequestPeer(ctx, member, *spec, "op-define"); err != nil {
		t.Fatal(err)
	}
	memberPeerServer(t, master, member)
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	drain := maintenanceCommand("master-1", member.State.HostID, true, membership.Generation)
	if result := ExecutePoolMaintenance(master, drain); result.Status != "succeeded" {
		t.Fatalf("drain the member: %+v", result)
	}
	permit := ""
	// The member never learns the master's sentence (the peer reply is deliberately
	// bounded), so the evidence is the master's own record: nothing moved to "starting"
	// and no permit was issued.
	if err := poolVMRequestPeer(ctx, member, vm, "start", &permit, false, nil, nil); err == nil {
		t.Fatal("the master admitted a start on a drained member")
	}
	if permit != "" {
		t.Fatal("a start permit was issued to a drained member")
	}
	if record := mustLoadPoolVMs(t, master).VMs[vm]; record.State != "defined" {
		t.Fatalf("the master moved the execution record to %q on a drained member", record.State)
	}
	// Stop is never refused, whoever asks.
	if err := poolVMRequestPeer(ctx, member, vm, "stop", nil, false, nil, nil); err != nil {
		t.Fatalf("a drained member could not stop its guest: %v", err)
	}
}

// C-11. A member's own pool-peer.json membership is written only at join and promotion, so
// the drain flag it carries never changes. Its freshest verified copy is the authority
// replica the master pushes, and the member-side guard has to read that or it is dead code.
func TestMemberReadsItsDrainFlagFromTheAuthorityReplica(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	joinLabMember(t, member, invitation)
	memberPeerServer(t, master, member)
	if err := poolHostInMaintenance(member, member.State.HostID); err != nil {
		t.Fatalf("a member that is not drained was refused: %v", err)
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	drain := maintenanceCommand("master-1", member.State.HostID, true, membership.Generation)
	if result := ExecutePoolMaintenance(master, drain); result.Status != "succeeded" {
		t.Fatalf("drain the member: %+v", result)
	}
	if err := poolHostInMaintenance(member, member.State.HostID); err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("the member did not read its own drain from the replica: %v", err)
	}
	// The member's own membership copy is untouched: the replica is read, never merged.
	own, err := Membership(member)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range own.Members {
		if row.HostID == member.State.HostID && row.Maintenance {
			t.Fatal("the replica was merged into the member's own membership record")
		}
	}
	// And it clears again when the master takes the host out of maintenance.
	membership, err = Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	if result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", member.State.HostID, false, membership.Generation)); result.Status != "succeeded" {
		t.Fatalf("undrain the member: %+v", result)
	}
	if err := poolHostInMaintenance(member, member.State.HostID); err != nil {
		t.Fatalf("the member stayed drained after the master cleared it: %v", err)
	}
}

// A durable maintenance change must replicate promptly; members must not wait for the next
// probe tick to learn the drain flag.
func TestMaintenanceDrainPushesReplicaPromptly(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	joinLabMember(t, member, invitation)
	memberPeerServer(t, master, member)
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", member.State.HostID, true, membership.Generation))
	if result.Status != "succeeded" {
		t.Fatalf("drain the member: %+v", result)
	}
	record := storedReplica(t, member)
	if record == nil || !record.Verified {
		t.Fatalf("the member did not receive a verified replica: %#v", record)
	}
	drained := false
	for _, row := range record.Replica.Membership.Members {
		if row.HostID == member.State.HostID {
			drained = row.Maintenance
		}
	}
	if !drained {
		t.Fatalf("the replica did not carry the drain flag: %#v", record.Replica.Membership.Members)
	}
	if err := poolHostInMaintenance(member, member.State.HostID); err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("the member did not refuse from the pushed replica: %v", err)
	}
}

func TestMaintenanceIdempotentSuccessSkipsReplicaPush(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	joinLabMember(t, member, invitation)
	memberPeerServer(t, master, member)
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	cmd := maintenanceCommand("master-1", member.State.HostID, true, membership.Generation)
	if result := ExecutePoolMaintenance(master, cmd); result.Status != "succeeded" {
		t.Fatalf("first drain: %+v", result)
	}
	if err := os.Remove(poolReplicaFile(member)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	previous := pushPoolAuthorityReplicasAfterMaintenance
	pushPoolAuthorityReplicasAfterMaintenance = func(ctx context.Context, s *Store, timeout time.Duration) error {
		calls++
		return previous(ctx, s, timeout)
	}
	t.Cleanup(func() { pushPoolAuthorityReplicasAfterMaintenance = previous })
	if result := ExecutePoolMaintenance(master, cmd); result.Status != "succeeded" {
		t.Fatalf("repeat drain: %+v", result)
	}
	if calls != 0 {
		t.Fatalf("idempotent maintenance invoked replica push %d times", calls)
	}
	if record := storedReplica(t, member); record != nil {
		t.Fatalf("idempotent maintenance pushed a replica: %#v", record)
	}
}

// A drain is done when the master's own record says so. A member that was down for the
// prompt push does not make the operator's request an unfinished one; it catches up from the
// next tick, as it does for every other authority write.
func TestMaintenanceSucceedsWhenAMemberIsUnreachableForThePush(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	joinLabMember(t, member, invitation)
	memberPeerServer(t, master, member)
	setMemberPeerOrigin(t, master, member.State.HostID, "https://127.0.0.1:1")
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", member.State.HostID, true, membership.Generation))
	if result.Status != "succeeded" || result.Error != "" {
		t.Fatalf("unreachable member push = %+v", result)
	}
	if !memberRecord(t, master, member.State.HostID).Maintenance {
		t.Fatal("the master did not record the drain durably")
	}
	if record := storedReplica(t, member); record != nil && record.Verified {
		t.Fatalf("the member received a replica from a failed push: %#v", record.Replica.Membership.Members)
	}
	if err := poolHostInMaintenance(member, member.State.HostID); err != nil {
		t.Fatalf("the member refused from stale state after a failed push: %v", err)
	}
	// The drain is in force where it is decided, whatever the member holds.
	recorded, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	if err := poolMaintenanceRefusal(recorded, member.State.HostID); err == nil {
		t.Fatal("the master did not refuse work on the host it recorded as drained")
	}
}

// Test gap 7. Deleting the guard inside ReconcilePoolVMAutostart failed nothing.
func TestAutostartPassReturnsEarlyOnADrainedHost(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", true))
	if reason := ReconcilePoolVMAutostart(context.Background(), s); !strings.Contains(reason, "maintenance mode") {
		t.Fatalf("the autostart pass on a drained host reported %q", reason)
	}
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	if reason := ReconcilePoolVMAutostart(context.Background(), s); strings.Contains(reason, "maintenance mode") {
		t.Fatalf("the autostart pass was skipped as drained on a host that is not: %q", reason)
	}
}

// Test gap 8. An ejected host has no maintenance state.
func TestMaintenanceRefusesAnEjectedHost(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	ejected := maintenanceMember("member-2", false)
	ejected.Status = "ejected"
	maintenanceMembership(t, master, "master-1", maintenanceMember("master-1", false), ejected)
	result := ExecutePoolMaintenance(master, maintenanceCommand("master-1", "member-2", true, 4))
	if result.Status != "failed" || !strings.Contains(result.Error, "ejected host has no maintenance state") {
		t.Fatalf("draining an ejected host = %+v", result)
	}
}

// joinLabMember completes the real CSR/join/rehome exchange, so the master holds a ready
// membership row for the member and the member holds the master's CA and pool identity.
func joinLabMember(t *testing.T, member *Store, invitation protocol.PoolJoinInvitation) {
	t.Helper()
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindPoolJoinInvitation(invitation.Invitation, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, invitation.MasterURL, invitation.PoolCAPEM, bound); err != nil {
		t.Fatal(err)
	}
	if err = ResumePoolRehome(context.Background(), member); err != nil {
		t.Fatal(err)
	}
}

// memberPeerServer runs the member's own peer listener and advertises it, so the master's
// authority replica push actually reaches it.
func memberPeerServer(t *testing.T, master, member *Store) {
	t.Helper()
	config, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(member))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	if err = AdvertisePoolPeerOrigin(context.Background(), member, server.URL); err != nil {
		t.Fatal(err)
	}
	setMemberPeerOrigin(t, master, member.State.HostID, server.URL)
}
