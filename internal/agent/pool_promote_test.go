// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Every test here runs a real pool of in-process hosts over the real mTLS peer transport,
// with real certificates, real replicas and real signatures. The one thing that is faked is
// the fence itself: poolFenceVerify is replaced, so no test ever runs a fence agent against
// anything. Each test asserts the two invariants after every step: no instant has two hosts
// that would write pool authority, and no instant has two hosts that would start one guest.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func labFenceProfile() *protocol.StorageClusterFence {
	return &protocol.StorageClusterFence{
		Kind: protocol.FenceVirsh, Endpoint: "fence.example.test",
		TargetID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", CredentialRef: "lab-fence",
		VirshURI: "qemu:///system", SSHLogin: "fence", SSHPort: 22, SSHHostKeyRef: "lab-fence-host",
	}
}

func promoteCommand(operationID, candidate string) protocol.Command {
	return protocol.Command{OperationID: operationID, Kind: "pool-promote", Pool: &protocol.PoolCommand{
		RequestID: "request-" + operationID, Action: "promote", MemberHostID: candidate, FenceConfirmed: true,
		// What the operator confirmed powering off, as Core's preflight named it. Most labs
		// have one host to exclude; a test with more overrides this.
		FenceHostIDs: []string{"master-1"},
	}}
}

func fenceProfileCommand(operationID, master, member string, fence *protocol.StorageClusterFence) protocol.Command {
	return protocol.Command{OperationID: operationID, Kind: "pool-fence-profile", Pool: &protocol.PoolCommand{
		RequestID: "request-" + operationID, Action: "fence-profile", MemberHostID: member,
		Membership: protocol.PoolMembership{MasterHostID: master}, Fence: fence,
	}}
}

// fakeFence replaces the native fencing seam for one test. Fences never run here.
func fakeFence(t *testing.T, answer func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error)) {
	t.Helper()
	previous := poolFenceVerify
	poolFenceVerify = func(_ context.Context, _ string, _ *protocol.StorageClusterFence, _, hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
		return answer(hostID, operationID, digest, generation)
	}
	t.Cleanup(func() { poolFenceVerify = previous })
}

// observedPowerOff is what a real fence produces only after a separate observation found the
// target off: the one shape that is proof.
func observedPowerOff(age time.Duration) func(string, string, string, uint64) (protocol.StorageClusterFenceStatus, error) {
	return func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
		return protocol.StorageClusterFenceStatus{
			RequestID: operationID, HostID: hostID, Status: "passed", TargetID: labFenceProfile().TargetID,
			ExclusionVerified: true, ObservedAt: time.Now().UTC().Add(-age),
			ConfigGeneration: generation, ConfigDigest: digest,
		}, nil
	}
}

// promotionLab is a pool whose master has a recorded fence profile and whose members hold a
// current, verified authority replica.
func promotionLab(t *testing.T, members int) *transferLab {
	t.Helper()
	lab := newTransferLab(t, members)
	master := lab.host("master-1")
	// The credential handoff has already delivered the master's own fence material; the
	// profile cannot be recorded without it.
	writeLabFenceCredential(t, master, labFenceProfile().CredentialRef, labFenceIdentity)
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-1", "master-1", "master-1", labFenceProfile())); result.Status != "succeeded" {
		t.Fatalf("fence profile = %#v", result)
	}
	// Every member converges on the master's current membership the way it does in a running
	// pool: one probe, one signed notice. Without it a member that joined first would hold a
	// membership that predates the member that joined after it.
	for _, host := range lab.hosts[1:] {
		ConvergePoolAuthority(context.Background(), host, 5*time.Second)
	}
	return lab
}

func pushReplicas(t *testing.T, master *Store) {
	t.Helper()
	if err := PushPoolAuthorityReplicas(context.Background(), master, 20*time.Second); err != nil {
		t.Fatalf("replica push: %v", err)
	}
}

// powerOff models the fence: the host stops answering and stops executing.
func (l *transferLab) powerOff(hostID string) {
	l.t.Helper()
	l.off[hostID] = true
	l.drop(hostID, "!*")
}

func (l *transferLab) powerOn(hostID string) {
	l.t.Helper()
	l.off[hostID] = false
	l.drop(hostID, "")
}

// permitWriters counts the hosts that would issue or receive a start permit for one shared
// VM right now: the host must not be restricted, the host it follows must be a master that
// still accepts authority writes, and that master's durable record must name this host as
// the VM's owner. Two hosts here would be two writers of one disk.
func (l *transferLab) permitWriters(vmID string) []string {
	l.t.Helper()
	out := []string{}
	for _, host := range l.hosts {
		if l.off[host.State.HostID] {
			continue
		}
		if requirePoolStartPath(host) != nil {
			continue
		}
		poolPeerMu.Lock()
		state, err := loadPoolPeer(host)
		poolPeerMu.Unlock()
		if err != nil || state.Membership.Valid() != nil {
			continue
		}
		master := l.host(state.Membership.MasterHostID)
		if l.off[master.State.HostID] {
			continue
		}
		poolPeerMu.Lock()
		masterState, masterErr := loadPoolPeer(master)
		poolPeerMu.Unlock()
		if masterErr != nil || masterState.Membership.MasterHostID != master.State.HostID || poolAuthorityWritable(masterState) != nil {
			continue
		}
		if requirePoolVMAuthorityWritable(master, "start", false) != nil {
			continue
		}
		record, ok := poolVMRecords(l.t, master)[vmID]
		if !ok || record.HostID != host.State.HostID {
			continue
		}
		out = append(out, host.State.HostID)
	}
	return out
}

func (l *transferLab) requireAtMostOneWriter(step, vmID string) {
	l.t.Helper()
	if writers := l.permitWriters(vmID); len(writers) > 1 {
		l.t.Fatalf("%s: hosts that would start %s = %v, want at most one", step, vmID, writers)
	}
}

func labSpec(id, host string) protocol.VMCreateSpec {
	spec := *poolVMTestSpec(id, host)
	spec.PoolID = "pool-1"
	return spec
}

func labOwnedVM(id, host string) poolVMRecord {
	spec := labSpec(id, host)
	return poolVMRecord{PoolID: "pool-1", VMID: id, HostID: host, State: "stopped", Epoch: 7, Spec: spec}
}

const labVMA = "11111111-1111-4111-8111-111111111111"
const labVMB = "22222222-2222-4222-8222-222222222222"

func promotionVerdict(t *testing.T, candidate *Store) protocol.PoolPromotionVerdict {
	t.Helper()
	return PoolPromotionPreflight(context.Background(), candidate, "", 5*time.Second)
}

func verdictCheck(t *testing.T, verdict protocol.PoolPromotionVerdict, name string) protocol.PoolPromotionCheck {
	t.Helper()
	for _, check := range verdict.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("verdict has no %s check: %#v", name, verdict.Checks)
	return protocol.PoolPromotionCheck{}
}

// The preflight verdict is the whole proof, computed on Core and changing nothing. Each case
// breaks exactly one check and expects that check, and only that check, to block.
func TestPoolPromotionPreflightVerdictMatrix(t *testing.T) {
	t.Run("no replica blocks and no fence can substitute", func(t *testing.T) {
		lab := promotionLab(t, 1)
		candidate := lab.host("member-1")
		lab.powerOff("master-1")
		verdict := promotionVerdict(t, candidate)
		if verdict.CanPromote {
			t.Fatal("a host with no authority replica was allowed to promote itself")
		}
		if check := verdictCheck(t, verdict, "replica-integrity"); check.Status != "blocked" {
			t.Fatalf("replica-integrity = %#v", check)
		}
	})

	t.Run("a tampered replica is refused", func(t *testing.T) {
		lab := promotionLab(t, 1)
		master, candidate := lab.host("master-1"), lab.host("member-1")
		pushReplicas(t, master)
		poolReplicaMu.Lock()
		record, err := loadPoolReplica(candidate)
		if err == nil && record != nil {
			record.Replica.Membership.PoolID = record.Replica.PoolID
			record.Replica.Generation++
			err = savePoolReplica(candidate, *record)
		}
		poolReplicaMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		lab.powerOff("master-1")
		verdict := promotionVerdict(t, candidate)
		if verdict.CanPromote {
			t.Fatal("a tampered replica was accepted")
		}
		if check := verdictCheck(t, verdict, "replica-integrity"); check.Status != "blocked" {
			t.Fatalf("replica-integrity = %#v", check)
		}
	})

	t.Run("a reachable healthy old master is sent to the planned master change", func(t *testing.T) {
		lab := promotionLab(t, 1)
		master, candidate := lab.host("master-1"), lab.host("member-1")
		pushReplicas(t, master)
		verdict := promotionVerdict(t, candidate)
		if verdict.CanPromote {
			t.Fatal("a promotion was allowed while the old master was healthy")
		}
		check := verdictCheck(t, verdict, "old-master-exclusion")
		if check.Status != "blocked" || check.Reason == "" {
			t.Fatalf("old-master-exclusion = %#v", check)
		}
	})

	t.Run("a reachable member holding a higher generation is named instead", func(t *testing.T) {
		lab := promotionLab(t, 2)
		master, candidate, other := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
		pushReplicas(t, master)
		// member-2 is ahead: it holds the newer replica the candidate never received.
		poolReplicaMu.Lock()
		record, err := loadPoolReplica(other)
		if err == nil && record != nil {
			record.Replica.Generation += 5
			err = savePoolReplica(other, *record)
		}
		poolReplicaMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		lab.powerOff("master-1")
		verdict := promotionVerdict(t, candidate)
		if verdict.CanPromote {
			t.Fatal("a promotion was allowed over a member holding a newer authority")
		}
		check := verdictCheck(t, verdict, "replica-supremacy")
		if check.Status != "blocked" || len(check.Hosts) != 1 || check.Hosts[0] != "member-2" {
			t.Fatalf("replica-supremacy = %#v", check)
		}
	})

	t.Run("a reachable owner that disagrees blocks the promotion", func(t *testing.T) {
		lab := promotionLab(t, 2)
		master, candidate := lab.host("master-1"), lab.host("member-1")
		// member-2 is recorded as owning a VM it does not define.
		writePoolVMRecord(t, master, labOwnedVM(labVMA, "member-2"))
		pushReplicas(t, master)
		lab.powerOff("master-1")
		verdict := promotionVerdict(t, candidate)
		if verdict.CanPromote {
			t.Fatal("a promotion was allowed while a reachable owner disagreed with the replica")
		}
		check := verdictCheck(t, verdict, "accepted-ownership")
		if check.Status != "blocked" || len(check.VMs) != 1 || check.VMs[0] != labVMA {
			t.Fatalf("accepted-ownership = %#v", check)
		}
		if len(verdict.VMs) != 1 || verdict.VMs[0].State != "blocked" {
			t.Fatalf("VM classification = %#v", verdict.VMs)
		}
	})

	t.Run("every other check passing leaves only the fence, and it is unknown not passed", func(t *testing.T) {
		lab := promotionLab(t, 1)
		master, candidate := lab.host("master-1"), lab.host("member-1")
		writePoolVMRecord(t, master, labOwnedVM(labVMA, "master-1"))
		pushReplicas(t, master)
		lab.powerOff("master-1")
		verdict := promotionVerdict(t, candidate)
		if verdict.CanPromote {
			t.Fatal("a promotion with no fence proof was allowed")
		}
		if !verdict.FenceRequired {
			t.Fatal("the verdict did not require a fence")
		}
		for _, name := range []string{"replica-integrity", "replica-supremacy", "accepted-ownership"} {
			if check := verdictCheck(t, verdict, name); check.Status != "passed" {
				t.Fatalf("%s = %#v", name, check)
			}
		}
		if check := verdictCheck(t, verdict, "old-master-exclusion"); check.Status != "unknown" {
			t.Fatalf("old-master-exclusion = %#v", check)
		}
		// The unreachable old master's guest stays visible and is not startable.
		if len(verdict.VMs) != 1 || verdict.VMs[0].State != "blocked" || verdict.VMs[0].VMID != labVMA {
			t.Fatalf("VM classification = %#v", verdict.VMs)
		}
	})

	t.Run("a host with no recorded fence profile says so honestly", func(t *testing.T) {
		lab := newTransferLab(t, 1)
		master, candidate := lab.host("master-1"), lab.host("member-1")
		pushReplicas(t, master)
		lab.powerOff("master-1")
		verdict := promotionVerdict(t, candidate)
		check := verdictCheck(t, verdict, "old-master-exclusion")
		if check.Status != "blocked" || verdict.CanPromote {
			t.Fatalf("old-master-exclusion = %#v", check)
		}
	})
}

// Nothing but an observed power-off, bound to this exact promotion, is accepted.
func TestPoolPromotionRefusesEveryFenceThatIsNotProof(t *testing.T) {
	cases := []struct {
		name   string
		answer func(string, string, string, uint64) (protocol.StorageClusterFenceStatus, error)
	}{
		{"a fence that only acknowledged the request", func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			// The agent returned success; no separate observation found the target off.
			return protocol.StorageClusterFenceStatus{
				RequestID: operationID, HostID: hostID, Status: "uncertain", TargetID: labFenceProfile().TargetID,
				ExclusionVerified: false, ObservedAt: time.Now().UTC(), ConfigGeneration: generation, ConfigDigest: digest,
				Error: "target exclusion was not independently confirmed",
			}, fmt.Errorf("target exclusion was not independently confirmed")
		}},
		{"a proof older than the freshness window", observedPowerOff(protocol.PoolPromotionFenceWindow + time.Minute)},
		{"a proof bound to another operation", func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			proof, _ := observedPowerOff(0)(hostID, "another-operation", digest, generation)
			return proof, nil
		}},
		{"a proof bound to another host", func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			proof, _ := observedPowerOff(0)("another-host", operationID, digest, generation)
			return proof, nil
		}},
		{"a proof bound to another generation", func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			proof, _ := observedPowerOff(0)(hostID, operationID, digest, generation+1)
			return proof, nil
		}},
		{"a proof whose digest names another promotion", func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
			proof, _ := observedPowerOff(0)(hostID, operationID, protocol.PoolPromotionFenceDigest("pool-1", hostID, "other", generation), generation)
			return proof, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lab := promotionLab(t, 1)
			master, candidate := lab.host("master-1"), lab.host("member-1")
			pushReplicas(t, master)
			lab.powerOff("master-1")
			fakeFence(t, tc.answer)
			result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-fence", "member-1"))
			if result.Status == "succeeded" {
				t.Fatalf("a promotion succeeded on %s", tc.name)
			}
			if peerState(t, candidate).Membership.MasterHostID == "member-1" {
				t.Fatal("the candidate promoted itself without a verified fence proof")
			}
			if member, ok := poolMemberByID(peerState(t, candidate).Membership, "master-1"); ok && member.ReplicaAckExcluded {
				t.Fatal("an invalid fence proof marked the old master as ACK-excluded")
			}
			lab.requireNoWriter("after a refused promotion")
		})
	}
}

func TestFencedOfflineFormerMasterDoesNotBlockSnapshotReservation(t *testing.T) {
	lab := promotionLab(t, 2)
	oldMaster, candidate := lab.host("master-1"), lab.host("member-2")
	pushReplicas(t, oldMaster)
	lab.powerOff(oldMaster.State.HostID)
	fakeFence(t, observedPowerOff(0))
	result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("offline-master-promotion", candidate.State.HostID))
	if result.Status != "succeeded" {
		t.Fatalf("promotion with a proven powered-off master = %#v", result)
	}

	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", candidate.State.HostID)
	state := poolVMState{VMs: map[string]poolVMRecord{
		vm.ID: {PoolID: candidate.State.PoolID, VMID: vm.ID, HostID: vm.HostID, Epoch: 1, State: "stopped", Spec: vm},
	}}
	if err := savePoolVMs(candidate, state); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{
		OperationID: "11111111-2222-4333-8444-555555555555", Kind: "snapshot-create", VM: &vm,
		Snapshot: &protocol.VMSnapshotRequest{Name: "before-change"},
	}
	if _, err := ReservePoolVMSnapshot(context.Background(), candidate, command, 1, strings.Repeat("a", 64), time.Now().UTC()); err != nil {
		t.Fatalf("verified survivor did not acknowledge snapshot reservation while the fenced former master was offline: %v", err)
	}
}

func TestUnprovenOfflineReadyMemberStillBlocksSnapshotReservation(t *testing.T) {
	lab := promotionLab(t, 2)
	master := lab.host("master-1")
	pushReplicas(t, master)
	// This member is merely unreachable. No promotion or fresh fence proof has
	// established that it can no longer act on the old authority.
	lab.powerOff("member-2")
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", master.State.HostID)
	state := poolVMState{VMs: map[string]poolVMRecord{
		vm.ID: {PoolID: master.State.PoolID, VMID: vm.ID, HostID: vm.HostID, Epoch: 1, State: "stopped", Spec: vm},
	}}
	if err := savePoolVMs(master, state); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{
		OperationID: "11111111-2222-4333-8444-555555555555", Kind: "snapshot-create", VM: &vm,
		Snapshot: &protocol.VMSnapshotRequest{Name: "before-change"},
	}
	if _, err := ReservePoolVMSnapshot(context.Background(), master, command, 1, strings.Repeat("a", 64), time.Now().UTC()); err == nil {
		t.Fatal("an unreachable ready member without a verified promotion fence was excluded from snapshot reservation")
	}
	member, ok := poolMemberByID(peerState(t, master).Membership, "member-2")
	if !ok || member.ReplicaAckExcluded {
		t.Fatalf("unproven unreachable member was marked ACK-excluded: %#v", member)
	}
}

func TestReturningFencedMemberRejoinsBeforeReplicaAckExclusionClears(t *testing.T) {
	lab := promotionLab(t, 1)
	oldMaster, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, oldMaster)
	lab.powerOff(oldMaster.State.HostID)
	fakeFence(t, observedPowerOff(0))
	if result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("returning-member-promotion", candidate.State.HostID)); result.Status != "succeeded" {
		t.Fatalf("promotion = %#v", result)
	}
	member, ok := poolMemberByID(peerState(t, candidate).Membership, oldMaster.State.HostID)
	if !ok || !member.ReplicaAckExcluded {
		t.Fatalf("verified-fenced former master was not retained with explicit replica ACK exclusion: %#v", member)
	}

	// The former master first adopts the new signed authority while remaining
	// restricted. Merely returning or receiving the replica does not clear exclusion.
	lab.powerOn(oldMaster.State.HostID)
	if err := EnsurePoolBootRestriction(oldMaster); err != nil {
		t.Fatal(err)
	}
	ReconcilePoolRestriction(context.Background(), oldMaster, 5*time.Second)
	member, ok = poolMemberByID(peerState(t, candidate).Membership, oldMaster.State.HostID)
	if !ok || !member.ReplicaAckExcluded {
		t.Fatal("repointing the returning host alone cleared replica ACK exclusion")
	}

	pushReplicas(t, candidate)
	storedReplica, err := loadPoolReplica(oldMaster)
	if err != nil || storedReplica == nil {
		t.Fatalf("returning member did not durably receive the promoted master's replica: %v", err)
	}
	storedMember, ok := poolMemberByID(storedReplica.Replica.Membership, oldMaster.State.HostID)
	if !ok || !storedMember.ReplicaAckExcluded {
		t.Fatal("the verified-fence ACK exclusion was not preserved in the signed replica delivered to the returning member")
	}
	if err := PublishPoolVMInventory(context.Background(), candidate); err != nil && !errors.Is(err, errPoolVMInventoryIncomplete) {
		t.Fatalf("current master's initial inventory = %v", err)
	}
	if err := PublishPoolVMInventory(context.Background(), oldMaster); err != nil {
		t.Fatalf("returning host current-generation inventory: %v", err)
	}
	member, ok = poolMemberByID(peerState(t, candidate).Membership, oldMaster.State.HostID)
	if !ok || member.ReplicaAckExcluded {
		t.Fatalf("accepted inventory and exact current replica ACK did not restore mandatory ACK eligibility: %#v", member)
	}
	// The clear changes signed authority content. A subsequent normal push must
	// deliver and acknowledge that changed replica before further quorum work.
	pushReplicas(t, candidate)
	lab.powerOff(oldMaster.State.HostID)
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", candidate.State.HostID)
	state, err := loadPoolVMs(candidate)
	if err != nil {
		t.Fatal(err)
	}
	state.VMs[vm.ID] = poolVMRecord{PoolID: candidate.State.PoolID, VMID: vm.ID, HostID: vm.HostID, Epoch: 1, State: "stopped", Spec: vm}
	if err = savePoolVMs(candidate, state); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{
		OperationID: "11111111-3333-4333-8444-555555555555", Kind: "snapshot-create", VM: &vm,
		Snapshot: &protocol.VMSnapshotRequest{Name: "after-rejoin"},
	}
	if _, err = ReservePoolVMSnapshot(context.Background(), candidate, command, 1, strings.Repeat("b", 64), time.Now().UTC()); err == nil {
		t.Fatal("the returned host was still treated as ACK-excluded after current inventory adoption")
	}
}

// An operator acknowledgement is not evidence, and without one nothing is even attempted.
func TestPoolPromotionRequiresTheDestructiveConfirmation(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	lab.powerOff("master-1")
	fakeFence(t, func(string, string, string, uint64) (protocol.StorageClusterFenceStatus, error) {
		t.Fatal("a fence ran without the operator's destructive confirmation")
		return protocol.StorageClusterFenceStatus{}, nil
	})
	command := promoteCommand("op-unconfirmed", "member-1")
	command.Pool.FenceConfirmed = false
	if result := ExecutePoolPromotion(context.Background(), candidate, command); result.Status == "succeeded" {
		t.Fatalf("an unconfirmed promotion ran: %#v", result)
	}
}

func requirePromoted(t *testing.T, lab *transferLab, candidate *Store, before protocol.PoolMembership) {
	t.Helper()
	after := peerState(t, candidate).Membership
	if after.PoolID != before.PoolID || candidate.State.PoolID != before.PoolID {
		t.Fatalf("the pool identity changed: %q then %q", before.PoolID, after.PoolID)
	}
	if after.MasterHostID != candidate.State.HostID || after.Generation != before.Generation+1 {
		t.Fatalf("membership after promotion = %#v", after)
	}
	if len(after.Members) != len(before.Members) {
		t.Fatalf("the promotion lost a host: %#v", after.Members)
	}
	for _, was := range before.Members {
		is, ok := poolMemberByID(after, was.HostID)
		if !ok || is.CertificateSHA256 != was.CertificateSHA256 || is.PeerOrigin != was.PeerOrigin {
			t.Fatalf("member %s identity changed: %#v then %#v", was.HostID, was, is)
		}
	}
}

// The whole promotion, on a two-host pool and on a three-host pool: the pool, its VM records,
// epochs and specs survive byte for byte, and exactly one host ends up master.
func TestPoolPromotionPreservesEveryIdentity(t *testing.T) {
	for _, members := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d-host pool", members+1), func(t *testing.T) {
			lab := promotionLab(t, members)
			master, candidate := lab.host("master-1"), lab.host("member-1")
			writePoolVMRecord(t, master, labOwnedVM(labVMA, "master-1"))
			writePoolVMRecord(t, master, labOwnedVM(labVMB, "member-1"))
			writeAppliedPoolVMSpec(t, candidate, labSpec(labVMB, "member-1"))
			pushReplicas(t, master)
			before := peerState(t, master).Membership
			recorded := poolVMRecords(t, master)
			lab.requireSingleMaster("before the promotion", "master-1")

			lab.powerOff("master-1")
			lab.requireNoWriter("while the master is fenced")
			fakeFence(t, observedPowerOff(0))
			result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-promote", "member-1"))
			if result.Status != "succeeded" {
				t.Fatalf("promotion = %#v", result)
			}
			lab.requireSingleMaster("after the promotion", "member-1")
			lab.requireAtMostOneWriter("after the promotion", labVMA)
			lab.requireAtMostOneWriter("after the promotion", labVMB)
			requirePromoted(t, lab, candidate, before)

			adopted := poolVMRecords(t, candidate)
			if len(adopted) != len(recorded) {
				t.Fatalf("the new master adopted %d VM records, want %d", len(adopted), len(recorded))
			}
			for id, was := range recorded {
				wasJSON, _ := json.Marshal(was)
				isJSON, _ := json.Marshal(adopted[id])
				if string(wasJSON) != string(isJSON) {
					t.Fatalf("VM %s record is not byte-identical:\n%s\n%s", id, wasJSON, isJSON)
				}
			}
			if members == 2 {
				// The bystander follows the new master, from its signed notice alone.
				if got := peerState(t, lab.host("member-2")).Membership.MasterHostID; got != "member-1" {
					t.Fatalf("the bystander follows %q", got)
				}
			}
			// Health is not derived from the replica: the new master reports what it has
			// actually re-collected, which at this instant is nothing.
			if status := PoolPromotionReport(candidate); status == nil || status.Status != "succeeded" {
				t.Fatalf("promotion report = %#v", status)
			}
		})
	}
}

// Interruption at every stage on the candidate, in the same pool, resumes to exactly one
// master and never to two.
func TestPoolPromotionResumesFromEveryStage(t *testing.T) {
	for _, stage := range []string{"preflight", "fence", "activate", "repoint"} {
		t.Run("interrupted at "+stage, func(t *testing.T) {
			lab := promotionLab(t, 2)
			master, candidate := lab.host("master-1"), lab.host("member-1")
			writePoolVMRecord(t, master, labOwnedVM(labVMA, "master-1"))
			pushReplicas(t, master)
			before := peerState(t, master).Membership
			lab.powerOff("master-1")
			fakeFence(t, observedPowerOff(0))

			// Crash the candidate at this stage: its durable record is rewound to the stage
			// and the process "restarts" into the tick.
			result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-"+stage, "member-1"))
			lab.requireAtMostOneWriter("after the first pass at "+stage, labVMA)
			if stage != "repoint" && result.Status == "failed" {
				t.Fatalf("promotion failed at %s: %#v", stage, result)
			}
			for pass := 0; pass < 4; pass++ {
				AdvancePoolAuthorityTransfer(context.Background(), candidate, 5*time.Second)
				lab.requireAtMostOneWriter("resuming at "+stage, labVMA)
			}
			lab.requireSingleMaster("after resuming from "+stage, "member-1")
			requirePromoted(t, lab, candidate, before)
		})
	}
}

func TestPoolPromotionJournalRecoversAfterDurableCompletion(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	lab.powerOff("master-1")
	operationID := "op-recovered-receipt"
	command := promoteCommand(operationID, candidate.State.HostID)
	attempts := 0
	fakeFence(t, func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
		attempts++
		if attempts == 1 {
			return protocol.StorageClusterFenceStatus{}, errors.New("temporary fencer failure")
		}
		return observedPowerOff(0)(hostID, operationID, digest, generation)
	})
	if result := ExecutePoolPromotion(context.Background(), candidate, command); result.Status != "needs_attention" {
		t.Fatalf("first promotion = %+v", result)
	}
	candidate.State.Journal[operationID] = JournalEntry{Command: &command, Status: "needs_attention", Error: "temporary fencer failure", Acknowledged: true}
	completeJournal(candidate, nil, time.Second)
	if entry := candidate.State.Journal[operationID]; entry.Status != "needs_attention" || !entry.Acknowledged {
		t.Fatalf("unproved promotion was reconciled: %+v", entry)
	}
	AdvancePoolAuthorityTransfer(context.Background(), candidate, 5*time.Second)
	if status := PoolPromotionReport(candidate); status == nil || status.Status != "succeeded" {
		t.Fatalf("durable promotion = %+v", status)
	}
	completeJournal(candidate, nil, time.Second)
	if entry := candidate.State.Journal[operationID]; entry.Status != "succeeded" || entry.Error != "" || entry.Acknowledged {
		t.Fatalf("recovered promotion receipt = %+v", entry)
	}
	if result := ExecutePoolPromotion(context.Background(), candidate, command); result.Status != "succeeded" {
		t.Fatalf("exact replay = %+v", result)
	}
}

// An interrupted promotion must not treat an old power-off observation as permanent
// evidence. Once it expires, the same durable operation goes back to fencing and replaces
// the proof before it can activate authority.
func TestPoolPromotionRefencesExpiredProofBeforeActivation(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	lab.powerOff("master-1")
	replica, err := loadPoolReplica(candidate)
	if err != nil || replica == nil {
		t.Fatalf("candidate replica = %#v, %v", replica, err)
	}
	operationID := "op-expired-fence"
	digest := protocol.PoolPromotionFenceDigest(replica.Replica.PoolID, "master-1", operationID, replica.Replica.Generation)
	record := &poolTransferRecord{
		OperationID: operationID, Action: "promote", Role: "candidate", TargetHostID: "member-1",
		Stage: "activate", Status: "pending", AuthorityGeneration: replica.Replica.Generation,
		FenceAgreed: []string{"master-1"},
		Fences: []protocol.StorageClusterFenceStatus{{
			RequestID: operationID, HostID: "master-1", Status: "passed", TargetID: labFenceProfile().TargetID,
			ExclusionVerified: true, ObservedAt: time.Now().UTC().Add(-protocol.PoolPromotionFenceWindow - time.Minute),
			ConfigGeneration: replica.Replica.Generation, ConfigDigest: digest,
		}},
	}
	if err := savePoolTransfer(candidate, record); err != nil {
		t.Fatal(err)
	}
	var fences int
	fakeFence(t, func(hostID, gotOperationID, gotDigest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
		fences++
		return observedPowerOff(0)(hostID, gotOperationID, gotDigest, generation)
	})

	AdvancePoolAuthorityTransfer(context.Background(), candidate, 5*time.Second)
	if fences != 1 {
		t.Fatalf("fresh fence calls = %d, want 1", fences)
	}
	lab.requireSingleMaster("after replacing an expired fence proof", "member-1")
	updated, err := loadPoolTransfer(candidate)
	if err != nil || updated == nil {
		t.Fatalf("updated transfer = %#v, %v", updated, err)
	}
	proof := poolPromotionProofFor(updated.Fences, "master-1")
	member, ok := poolMemberByID(replica.Replica.Membership, "master-1")
	if !ok || protocol.PoolPromotionFenceVerified(proof, member, replica.Replica.PoolID, "master-1", operationID, replica.Replica.Generation, time.Now().UTC()) != nil {
		t.Fatalf("updated proof is not a fresh bound exclusion proof: %#v", proof)
	}
}

func TestPoolPromotionDoesNotRefenceMalformedExpiredProof(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	lab.powerOff("master-1")
	replica, err := loadPoolReplica(candidate)
	if err != nil || replica == nil {
		t.Fatalf("candidate replica = %#v, %v", replica, err)
	}
	record := &poolTransferRecord{
		OperationID: "op-malformed-expired-fence", Action: "promote", Role: "candidate", TargetHostID: "member-1",
		Stage: "activate", Status: "pending", AuthorityGeneration: replica.Replica.Generation,
		FenceAgreed: []string{"master-1"},
		Fences: []protocol.StorageClusterFenceStatus{{
			RequestID: "op-malformed-expired-fence", HostID: "master-1", Status: "passed", TargetID: labFenceProfile().TargetID,
			ExclusionVerified: true, ObservedAt: time.Now().UTC().Add(-protocol.PoolPromotionFenceWindow - time.Minute),
			ConfigGeneration: replica.Replica.Generation, ConfigDigest: "not-a-bound-proof",
		}},
	}
	if err := savePoolTransfer(candidate, record); err != nil {
		t.Fatal(err)
	}
	var fences int
	fakeFence(t, func(string, string, string, uint64) (protocol.StorageClusterFenceStatus, error) {
		fences++
		return protocol.StorageClusterFenceStatus{}, nil
	})

	AdvancePoolAuthorityTransfer(context.Background(), candidate, 5*time.Second)
	if fences != 0 {
		t.Fatalf("malformed proof triggered %d new fences", fences)
	}
	if peerState(t, candidate).Membership.MasterHostID != "master-1" {
		t.Fatal("candidate activated authority from a malformed proof")
	}
	updated, err := loadPoolTransfer(candidate)
	if err != nil || updated == nil || updated.Stage != "activate" {
		t.Fatalf("malformed proof transfer = %#v, %v", updated, err)
	}
}

func TestPoolPromotionRefencesOnlyExpiredHostProof(t *testing.T) {
	lab := promotionLab(t, 2)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	writeLabFenceCredential(t, master, labFenceProfile().CredentialRef, labFenceIdentity)
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-member-2", "master-1", "member-2", labFenceProfile())); result.Status != "succeeded" {
		t.Fatalf("member-2 fence profile = %#v", result)
	}
	pushReplicas(t, master)
	lab.powerOff("master-1")
	lab.powerOff("member-2")
	replica, err := loadPoolReplica(candidate)
	if err != nil || replica == nil {
		t.Fatalf("candidate replica = %#v, %v", replica, err)
	}
	operationID := "op-one-expired-fence"
	proof := func(hostID string, age time.Duration) protocol.StorageClusterFenceStatus {
		return protocol.StorageClusterFenceStatus{RequestID: operationID, HostID: hostID, Status: "passed", TargetID: labFenceProfile().TargetID,
			ExclusionVerified: true, ObservedAt: time.Now().UTC().Add(-age), ConfigGeneration: replica.Replica.Generation,
			ConfigDigest: protocol.PoolPromotionFenceDigest(replica.Replica.PoolID, hostID, operationID, replica.Replica.Generation)}
	}
	freshMaster := proof("master-1", 0)
	record := &poolTransferRecord{OperationID: operationID, Action: "promote", Role: "candidate", TargetHostID: "member-1", Stage: "activate", Status: "pending",
		AuthorityGeneration: replica.Replica.Generation, FenceAgreed: []string{"master-1", "member-2"}, Fences: []protocol.StorageClusterFenceStatus{freshMaster, proof("member-2", protocol.PoolPromotionFenceWindow+time.Minute)}}
	if err := savePoolTransfer(candidate, record); err != nil {
		t.Fatal(err)
	}
	var fenced []string
	fakeFence(t, func(hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
		fenced = append(fenced, hostID)
		return observedPowerOff(0)(hostID, operationID, digest, generation)
	})
	AdvancePoolAuthorityTransfer(context.Background(), candidate, 5*time.Second)
	if len(fenced) != 1 || fenced[0] != "member-2" {
		updated, _ := loadPoolTransfer(candidate)
		t.Fatalf("fenced hosts = %v, want only expired member-2; transfer=%#v", fenced, updated)
	}
	updated, _ := loadPoolTransfer(candidate)
	if got := poolPromotionProofFor(updated.Fences, "master-1"); got == nil || !got.ObservedAt.Equal(freshMaster.ObservedAt) {
		t.Fatalf("fresh master proof was replaced: %#v", got)
	}
}

func TestPoolPromotionDoesNotRefenceExpiredProofWhenSupremacyFails(t *testing.T) {
	lab := promotionLab(t, 2)
	master, candidate, other := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	pushReplicas(t, master)
	poolReplicaMu.Lock()
	otherReplica, err := loadPoolReplica(other)
	if err == nil && otherReplica != nil {
		otherReplica.Replica.Generation++
		err = savePoolReplica(other, *otherReplica)
	}
	poolReplicaMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	lab.powerOff("master-1")
	replica, _ := loadPoolReplica(candidate)
	operationID := "op-expired-supremacy"
	record := &poolTransferRecord{OperationID: operationID, Action: "promote", Role: "candidate", TargetHostID: "member-1", Stage: "activate", Status: "pending",
		AuthorityGeneration: replica.Replica.Generation, FenceAgreed: []string{"master-1"}, Fences: []protocol.StorageClusterFenceStatus{{RequestID: operationID, HostID: "master-1", Status: "passed", TargetID: labFenceProfile().TargetID, ExclusionVerified: true, ObservedAt: time.Now().UTC().Add(-protocol.PoolPromotionFenceWindow - time.Minute), ConfigGeneration: replica.Replica.Generation, ConfigDigest: protocol.PoolPromotionFenceDigest(replica.Replica.PoolID, "master-1", operationID, replica.Replica.Generation)}}}
	if err := savePoolTransfer(candidate, record); err != nil {
		t.Fatal(err)
	}
	var fences int
	fakeFence(t, func(string, string, string, uint64) (protocol.StorageClusterFenceStatus, error) {
		fences++
		return protocol.StorageClusterFenceStatus{}, nil
	})
	AdvancePoolAuthorityTransfer(context.Background(), candidate, 5*time.Second)
	if fences != 0 || peerState(t, candidate).Membership.MasterHostID != "master-1" {
		t.Fatalf("supremacy failure fenced=%d master=%s", fences, peerState(t, candidate).Membership.MasterHostID)
	}
}

// The boot rule: a master with members that restarts cannot know it was not fenced and
// promoted away, so it starts nothing until its members confirm it.
func TestPoolMasterWithMembersIsRestrictedUntilItsMembersConfirmIt(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	if requirePoolStartPath(master) == nil {
		t.Fatal("a restarted master with members exposed a start path before confirming its authority")
	}
	lab.requireNoWriter("a restarted master before it has confirmed anything")
	if status := PoolRestrictedReport(master); status == nil || status.Reason == "" {
		t.Fatalf("restricted report = %#v", status)
	}
	if checks := PoolRestrictedCheck(master); len(checks) != 1 || checks[0].Status != "failed" {
		t.Fatalf("restricted check = %#v", checks)
	}
	// Its member confirms it, so it is the master again — from an answer, not a timeout.
	ReconcilePoolRestriction(context.Background(), master, 5*time.Second)
	if err := requirePoolStartPath(master); err != nil {
		t.Fatalf("a confirmed master is still restricted: %v", err)
	}
	lab.requireSingleMaster("after every member confirmed it", "master-1")
}

// A clear restriction is a fresh Core observation, not an omitted check. Consumers retain
// omitted checks to survive partial inventory reports, so an explicit healthy sample is what
// replaces an earlier failed restriction after this host has re-established authority.
func TestPoolRestrictedCheckPublishesFreshHealthyClear(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	failed := PoolRestrictedCheck(master)
	if len(failed) != 1 || failed[0].Status != "failed" || failed[0].Freshness != "fresh" {
		t.Fatalf("restricted check = %#v", failed)
	}
	clearPoolRestriction(master, poolRestrictedUnconfirmed)
	if status := PoolRestrictedReport(master); status != nil {
		t.Fatalf("cleared restriction report = %#v", status)
	}
	cleared := PoolRestrictedCheck(master)
	if len(cleared) != 1 || cleared[0].Name != poolRestrictedCheck || cleared[0].Status != "healthy" || cleared[0].Freshness != "fresh" || cleared[0].ObservedAt.IsZero() || cleared[0].Error != "" {
		t.Fatalf("cleared restriction check = %#v", cleared)
	}
}

func TestPoolPromotionCheckPublishesFreshHealthyAfterSuccess(t *testing.T) {
	lab := promotionLab(t, 1)
	candidate := lab.host("member-1")
	record := &poolTransferRecord{OperationID: "promotion-clear", Action: "promote", Role: "candidate", TargetHostID: candidate.State.HostID, Stage: "preflight", LastError: "promotion refused"}
	if err := savePoolTransfer(candidate, record); err != nil {
		t.Fatal(err)
	}
	failed := PoolPromotionCheck(candidate)
	if len(failed) != 1 || failed[0].Status != "failed" || failed[0].Freshness != "fresh" {
		t.Fatalf("failed promotion check = %#v", failed)
	}
	record.Stage, record.LastError = "done", ""
	if err := savePoolTransfer(candidate, record); err != nil {
		t.Fatal(err)
	}
	cleared := PoolPromotionCheck(candidate)
	if len(cleared) != 1 || cleared[0].Name != poolPromotionCheck || cleared[0].Status != "healthy" || cleared[0].Freshness != "fresh" || cleared[0].ObservedAt.IsZero() || cleared[0].Error != "" {
		t.Fatalf("successful promotion check = %#v", cleared)
	}
}

func TestPoolPromotionCheckPublishesFreshHealthyWhenCandidateRoleEnds(t *testing.T) {
	lab := promotionLab(t, 1)
	host := lab.host("member-1")
	if err := savePoolTransfer(host, &poolTransferRecord{OperationID: "promotion-old", Action: "promote", Role: "candidate", TargetHostID: host.State.HostID, Stage: "activate"}); err != nil {
		t.Fatal(err)
	}
	if check := PoolPromotionCheck(host); len(check) != 1 || check[0].Status != "unknown" {
		t.Fatalf("candidate promotion check = %#v", check)
	}
	if err := savePoolTransfer(host, &poolTransferRecord{OperationID: "historical-eject", Action: "eject", Role: "source", TargetHostID: "other", Stage: "done"}); err != nil {
		t.Fatal(err)
	}
	cleared := PoolPromotionCheck(host)
	if len(cleared) != 1 || cleared[0].Name != poolPromotionCheck || cleared[0].Status != "healthy" || cleared[0].Freshness != "fresh" || cleared[0].ObservedAt.IsZero() || cleared[0].Error != "" {
		t.Fatalf("historical non-candidate promotion check = %#v", cleared)
	}
}

func TestPoolPromotionCheckPublishesFreshHealthyWhenNoPromotionRecordExists(t *testing.T) {
	lab := promotionLab(t, 1)
	host := lab.host("member-1")
	cleared := PoolPromotionCheck(host)
	if len(cleared) != 1 || cleared[0].Name != poolPromotionCheck || cleared[0].Status != "healthy" || cleared[0].Freshness != "fresh" || cleared[0].ObservedAt.IsZero() || cleared[0].Error != "" {
		t.Fatalf("absent promotion check = %#v", cleared)
	}
}

// An unreachable member is exactly the host that could have been promoted while this one was
// down, so silence never clears the restriction.
func TestRestartedMasterStaysRestrictedWhileAMemberIsUnreachable(t *testing.T) {
	lab := promotionLab(t, 2)
	master := lab.host("master-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	lab.powerOff("member-2")
	for pass := 0; pass < 3; pass++ {
		ReconcilePoolRestriction(context.Background(), master, 2*time.Second)
	}
	if requirePoolStartPath(master) == nil {
		t.Fatal("a restarted master cleared its restriction while a member was unreachable")
	}
	check := PoolRestrictedCheck(master)
	if len(check) != 1 || check[0].Status != "failed" || !strings.Contains(check[0].Error, "member-2") {
		t.Fatalf("restricted check does not name the unreachable host: %#v", check)
	}
	// A single-host pool has nobody who could have promoted anything and is never restricted.
	solo := peerStore(t, "solo-1", "solo-pool")
	if err := EnsurePoolMembership(solo); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePoolBootRestriction(solo); err != nil {
		t.Fatal(err)
	}
	if err := requirePoolStartPath(solo); err != nil {
		t.Fatalf("the master of a single-host pool was restricted: %v", err)
	}
}

func recordMasterRestartFenceProof(t *testing.T, master *Store, unreachableHost, operationID string, observedAt time.Time, digestOverride string) {
	t.Helper()
	state := peerState(t, master)
	generation := state.AuthorityGeneration
	if generation == 0 {
		pushReplicas(t, master)
		state = peerState(t, master)
		generation = state.AuthorityGeneration
	}
	member, ok := poolMemberByID(state.Membership, unreachableHost)
	if !ok || member.Fence == nil {
		t.Fatalf("no fence profile for %s", unreachableHost)
	}
	digest := digestOverride
	if digest == "" {
		digest = protocol.PoolPromotionFenceDigest(state.Membership.PoolID, unreachableHost, operationID, generation)
	}
	record := &poolTransferRecord{
		OperationID: operationID, Action: "promote",
		Fences: []protocol.StorageClusterFenceStatus{{
			RequestID: operationID, HostID: unreachableHost, Status: "passed", TargetID: member.Fence.TargetID,
			ExclusionVerified: true, ObservedAt: observedAt,
			ConfigGeneration: generation, ConfigDigest: digest,
		}},
	}
	if err := savePoolTransfer(master, record); err != nil {
		t.Fatal(err)
	}
}

func recordMemberFenceProfile(t *testing.T, master *Store, memberID string) {
	t.Helper()
	writeLabFenceCredential(t, master, memberID+"-fence", labFenceIdentity)
	profile := labFenceProfile()
	profile.CredentialRef = memberID + "-fence"
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-"+memberID, "master-1", memberID, profile)); result.Status != "succeeded" {
		t.Fatalf("recording %s's fence profile = %#v", memberID, result)
	}
}

// A fresh fence proof already on this host's transfer record excludes an unreachable member
// the same way a promotion does; reachable members must still confirm.
func TestRestartedMasterClearsWhenUnreachablePeerHasValidFenceProof(t *testing.T) {
	lab := promotionLab(t, 2)
	master := lab.host("master-1")
	recordMemberFenceProfile(t, master, "member-2")
	pushReplicas(t, master)
	recordMasterRestartFenceProof(t, master, "member-2", "op-restart-clearance", time.Now().UTC(), "")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	lab.powerOff("member-2")
	ReconcilePoolRestriction(context.Background(), master, 5*time.Second)
	if err := requirePoolStartPath(master); err != nil {
		t.Fatalf("a restarted master stayed restricted with a valid fence proof: %v", err)
	}
	lab.requireSingleMaster("after an unreachable member was excluded by fence proof", "master-1")
}

// Stale or unbound fence proofs are not evidence for restart clearance.
func TestRestartedMasterStaysRestrictedOnInvalidRestartFenceProof(t *testing.T) {
	cases := []struct {
		name       string
		observedAt time.Time
		digest     string
	}{
		{"stale observation", time.Now().UTC().Add(-protocol.PoolPromotionFenceWindow - time.Minute), ""},
		{"unbound digest", time.Now().UTC(), "not-a-bound-proof"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lab := promotionLab(t, 2)
			master := lab.host("master-1")
			recordMemberFenceProfile(t, master, "member-2")
			pushReplicas(t, master)
			recordMasterRestartFenceProof(t, master, "member-2", "op-restart-stale", tc.observedAt, tc.digest)
			if err := EnsurePoolBootRestriction(master); err != nil {
				t.Fatal(err)
			}
			lab.powerOff("member-2")
			for pass := 0; pass < 3; pass++ {
				ReconcilePoolRestriction(context.Background(), master, 2*time.Second)
			}
			if requirePoolStartPath(master) == nil {
				t.Fatal("a restarted master cleared its restriction on an invalid fence proof")
			}
		})
	}
}

// The returning old master: restricted before it contacts anyone, refusing every start path,
// converging to member on signed evidence, and clearing only after it has reconciled.
func TestReturningOldMasterRejoinsSafely(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	writePoolVMRecord(t, master, labOwnedVM(labVMA, "master-1"))
	pushReplicas(t, master)
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	if result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-rejoin", "member-1")); result.Status != "succeeded" {
		t.Fatalf("promotion = %#v", result)
	}

	// The old master comes back. Before any peer contact it restricts itself.
	lab.powerOn("master-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	lab.requireSingleMaster("the old master is back but restricted", "member-1")
	if requirePoolStartPath(master) == nil {
		t.Fatal("a returning old master exposed a start path before contacting anyone")
	}
	if _, err := advancePoolAuthorityGeneration(master, "some-digest"); err == nil {
		t.Fatal("a restricted host numbered its own pool authority")
	}
	// Stopping a guest is never blocked: a restricted host must be able to put one down.
	// (The admission path itself needs a live libvirt observation, which this in-process
	// lab has none of, so the guard is asserted where it is decided.)
	if err := requirePoolVMAuthorityWritable(master, "stop", false); err != nil {
		t.Fatalf("a restricted host refused to stop a guest: %v", err)
	}
	if err := requirePoolVMAuthorityWritable(master, "start", false); err == nil {
		t.Fatal("a restricted host would still record a start")
	}

	// It converges to member on the new master's signed notice, and only then reconciles.
	ReconcilePoolRestriction(context.Background(), master, 5*time.Second)
	if got := peerState(t, master).Membership.MasterHostID; got != "member-1" {
		t.Fatalf("the returning master follows %q", got)
	}
	if requirePoolStartPath(master) == nil {
		t.Fatal("the returning master cleared its restriction merely by re-pointing")
	}
	lab.requireSingleMaster("after the old master rejoined as a member", "member-1")
	lab.requireAtMostOneWriter("after the old master rejoined as a member", labVMA)

	pushReplicas(t, candidate)
	for pass := 0; pass < 3; pass++ {
		ReconcilePoolRestriction(context.Background(), master, 5*time.Second)
	}
	if err := requirePoolStartPath(master); err != nil {
		t.Fatalf("a reconciled host is still restricted: %v", err)
	}
	lab.requireSingleMaster("after reconciliation", "member-1")
}

// A guest the current master records elsewhere is never started here: the local definition
// stays, the host stays restricted, and the conflict is named.
func TestReturningOldMasterStaysRestrictedOnAnOwnershipConflict(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	// The VM is defined on the old master but the authority records it on the candidate:
	// this is the "recovered elsewhere" case.
	writeAppliedPoolVMSpec(t, master, labSpec(labVMA, "master-1"))
	writeAppliedPoolStorage(t, master, labSharedStorage(t))
	writePoolVMRecord(t, master, labOwnedVM(labVMA, "member-1"))
	writeAppliedPoolVMSpec(t, candidate, labSpec(labVMA, "member-1"))
	pushReplicas(t, master)
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	if result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-conflict", "member-1")); result.Status != "succeeded" {
		t.Fatalf("promotion = %#v", result)
	}
	lab.powerOn("master-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 4; pass++ {
		// The new master can only replicate to this host once it follows it; the
		// restriction cannot clear before that either.
		ReconcilePoolRestriction(context.Background(), master, 5*time.Second)
		_ = PushPoolAuthorityReplicas(context.Background(), candidate, 5*time.Second)
	}
	if requirePoolStartPath(master) == nil {
		t.Fatal("a host with a VM recovered elsewhere cleared its restriction")
	}
	status := PoolRestrictedReport(master)
	if status == nil || len(status.Conflicts) != 1 || status.Conflicts[0] != labVMA {
		t.Fatalf("restricted report = %#v", status)
	}
	// The local definition is fenced off, not deleted.
	if _, err := os.Stat(filepath.Join(master.dir, "managed-vms", "vm-"+labVMA+".json")); err != nil {
		t.Fatalf("the local VM record was removed: %v", err)
	}
	lab.requireSingleMaster("with an unresolved ownership conflict", "member-1")
	lab.requireAtMostOneWriter("with an unresolved ownership conflict", labVMA)
}

func labSharedStorage(t *testing.T) protocol.StorageRegistration {
	t.Helper()
	return protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Name: "shared", Path: t.TempDir(),
		Kind: protocol.StorageKindNFS, Generation: 1,
		Source:   protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}},
		Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/exports", FilesystemID: "nfs-fs-1"}},
	}
}

// Every start path on a restricted host refuses, in Core's own words, before any storage or
// inventory work: the admission a Controller-driven start takes, the boot autostart pass and
// the durable record that would issue the permit.
func TestRestrictedHostRefusesEveryStartPath(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	writeAppliedPoolStorage(t, master, labSharedStorage(t))
	writeAppliedPoolVMSpec(t, master, labSpec(labVMA, "master-1"))
	writePoolVMRecord(t, master, labOwnedVM(labVMA, "master-1"))
	start := protocol.Command{OperationID: "op-start", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: labVMA, PoolID: "pool-1", HostID: "master-1", Action: "start"}}
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	_, err := AdmitPoolVMExecution(context.Background(), master, start)
	if err == nil || !strings.Contains(err.Error(), "confirmed its authority") {
		t.Fatalf("restricted start admission = %v", err)
	}
	if err = requirePoolVMAuthorityWritable(master, "start", false); err == nil {
		t.Fatal("a restricted host would still record a start")
	}
	if err = requirePoolVMAuthorityWritable(master, "define", true); err == nil {
		t.Fatal("a restricted host would still record a new definition")
	}
	if err = requirePoolVMAuthorityWritable(master, "update-reserve", false); err == nil {
		t.Fatal("a restricted host would still reserve a definition transition")
	}
	if err = requirePoolVMAuthorityWritable(master, "stop", false); err != nil {
		t.Fatalf("a restricted host refused to record a stop: %v", err)
	}
	// The boot autostart pass starts nothing and leaves no permit behind.
	ReconcilePoolVMAutostart(context.Background(), master)
	if _, statErr := os.Stat(filepath.Join(master.dir, "pool-vm-permits", labVMA+".json")); statErr == nil {
		t.Fatal("a restricted host wrote a start permit during autostart")
	}
}

// The boot rule reads durable evidence, not an assumption. A master no member could ever
// have fenced cannot have been promoted away behind its back, so it comes back as the master
// it was and its guests start — even with a member that is never coming back.
func TestBootRestrictionFollowsTheFenceEvidence(t *testing.T) {
	t.Run("no usable fence profile for itself, a dead member, and it still starts", func(t *testing.T) {
		lab := newTransferLab(t, 2)
		master := lab.host("master-1")
		lab.powerOff("member-2")
		if err := EnsurePoolBootRestriction(master); err != nil {
			t.Fatal(err)
		}
		if err := requirePoolStartPath(master); err != nil {
			t.Fatalf("a master no member could fence was restricted: %v", err)
		}
		lab.requireSingleMaster("a master that could never have been promoted away", "master-1")
	})

	t.Run("a usable fence profile for itself restricts it", func(t *testing.T) {
		lab := promotionLab(t, 1)
		master := lab.host("master-1")
		if err := EnsurePoolBootRestriction(master); err != nil {
			t.Fatal(err)
		}
		if requirePoolStartPath(master) == nil {
			t.Fatal("a fence-promotable master started without confirming its authority")
		}
	})

	t.Run("a profile a member may still hold keeps it restricted after the profile is cleared", func(t *testing.T) {
		lab := promotionLab(t, 1)
		master := lab.host("master-1")
		pushReplicas(t, master)
		if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-clear", "master-1", "master-1", nil)); result.Status != "succeeded" {
			t.Fatalf("clearing the fence profile = %#v", result)
		}
		// The member never acknowledges a replica built after the clearing, so it may still
		// hold the profile it was replicated earlier.
		lab.powerOff("member-1")
		if err := EnsurePoolBootRestriction(master); err != nil {
			t.Fatal(err)
		}
		if requirePoolStartPath(master) == nil {
			t.Fatal("a master whose member may still hold its fence profile started unconfirmed")
		}
	})

	t.Run("once every member holds a replica without the profile it is no longer restricted", func(t *testing.T) {
		lab := promotionLab(t, 1)
		master := lab.host("master-1")
		pushReplicas(t, master)
		if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-clear", "master-1", "master-1", nil)); result.Status != "succeeded" {
			t.Fatalf("clearing the fence profile = %#v", result)
		}
		pushReplicas(t, master)
		if peerState(t, master).SelfFence != nil {
			t.Fatal("the fence exposure survived a replica every member acknowledged")
		}
		lab.powerOff("member-1")
		if err := EnsurePoolBootRestriction(master); err != nil {
			t.Fatal(err)
		}
		if err := requirePoolStartPath(master); err != nil {
			t.Fatalf("a master whose profile no member still holds was restricted: %v", err)
		}
	})

	t.Run("a profile this build can never prove anything with is refused, not recorded", func(t *testing.T) {
		lab := newTransferLab(t, 1)
		master := lab.host("master-1")
		// This host reports no unique hardware identity, so no management controller could
		// ever be proved to be its own and the profile is refused rather than recorded.
		bmc := &protocol.StorageClusterFence{Kind: protocol.FenceRedfish, Endpoint: "bmc.example.test", TargetID: "system-1", Username: "nex-fence", CredentialRef: "cred-1"}
		result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-bmc", "master-1", "master-1", bmc))
		if result.Status == "succeeded" || !strings.Contains(result.Error, "does not report a unique hardware identity") {
			t.Fatalf("recording an unverifiable fence profile = %#v", result)
		}
		if peerState(t, master).SelfFence != nil {
			t.Fatal("a refused fence profile left this host fence-exposed")
		}
	})
}

// The deadlock the escape would have to solve, recorded as it actually behaves: a restricted
// master cannot eject the dead member, because an eject is an authority write. No
// timeout-based or assurance-based escape is offered; see the report.
func TestRestrictedMasterCannotEjectADeadMember(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	pushReplicas(t, master)
	lab.powerOff("member-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-eject", "eject", "member-1", "master-1"))
	if result.Status == "succeeded" {
		t.Fatal("a restricted master performed an authority write")
	}
	if !strings.Contains(result.Error, "confirmed its authority") {
		t.Fatalf("eject refusal = %q", result.Error)
	}
	lab.requireNoWriter("a restricted master with a dead member")
}

// Three hosts: the member that was promoted re-pointed the bystander before it died. The
// returning master learns it was superseded from the bystander, not from the dead host.
func TestRestartedMasterLearnsItWasSupersededFromABystander(t *testing.T) {
	lab := promotionLab(t, 2)
	master, candidate, bystander := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	pushReplicas(t, master)
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	if result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-3host", "member-1")); result.Status != "succeeded" {
		t.Fatalf("promotion = %#v", result)
	}
	if got := peerState(t, bystander).Membership.MasterHostID; got != "member-1" {
		t.Fatalf("the bystander follows %q", got)
	}
	// The promoted master dies; the old one comes back and can only reach the bystander.
	lab.powerOn("master-1")
	lab.powerOff("member-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	ReconcilePoolRestriction(context.Background(), master, 5*time.Second)
	status := PoolRestrictedReport(master)
	if status == nil || !strings.Contains(status.Reason, "superseded") {
		t.Fatalf("restricted report = %#v", status)
	}
	if requirePoolStartPath(master) == nil {
		t.Fatal("a superseded master started guests")
	}
	lab.requireNoWriter("a superseded master and an unreachable new master")
}

// A restricted host is refused, not silenced: it keeps reporting, it keeps publishing its
// own VM inventory, and its reason and conflicts reach both the Controller inventory and the
// host-local pool view through the same report.
func TestRestrictedHostKeepsReportingAndStopping(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	status := PoolRestrictedReport(master)
	if status == nil || !strings.Contains(status.Reason, "confirmed its authority") || status.Since.IsZero() {
		t.Fatalf("restricted report = %#v", status)
	}
	if checks := PoolRestrictedCheck(master); len(checks) != 1 || checks[0].Status != "failed" || checks[0].Error != status.Reason {
		t.Fatalf("restricted check = %#v", checks)
	}
	// Reporting its own inventory is how the pool reconciles it, so the restriction never
	// blocks it. (A pool whose other member has not published yet answers "incomplete",
	// which is the ordinary seeding rule and not a refusal.)
	// A restricted host still records the snapshot; what it defers is the owner-record
	// projection, which is an authority write it must not perform.
	if err := PublishPoolVMInventory(context.Background(), master); err != nil && !errors.Is(err, errPoolVMOwnersDeferred) && !errors.Is(err, errPoolVMInventoryIncomplete) {
		t.Fatalf("a restricted host was blocked from publishing its own VM inventory: %v", err)
	}
	// Stops are decided by the same durable record that refuses starts, and are allowed.
	for _, action := range []string{"stop", "stopped", "running"} {
		if err := requirePoolVMAuthorityWritable(master, action, false); err != nil {
			t.Fatalf("a restricted host refused %q: %v", action, err)
		}
	}
	// Conflicts ride the same report.
	recordPoolRestrictionConflicts(master, []string{labVMA})
	if status = PoolRestrictedReport(master); status == nil || len(status.Conflicts) != 1 || status.Conflicts[0] != labVMA {
		t.Fatalf("restricted report with a conflict = %#v", status)
	}
}

// The structural fields a caller offers the promotion on, so nothing has to read Core's
// sentences: ready_to_fence is true exactly when confirming now would proceed to the fence.
func TestPromotionVerdictReportsReadinessStructurally(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)

	// A reachable, healthy old master is not a fencing question at all.
	verdict := promotionVerdict(t, candidate)
	if verdict.ReadyToFence || verdict.CanPromote {
		t.Fatalf("a healthy pool offered a promotion: %#v", verdict)
	}
	if !verdict.FenceRecorded || !verdict.FenceUsable || verdict.FenceUnusableReason != "" {
		t.Fatalf("the recorded, usable profile was not reported: %#v", verdict)
	}

	// The master is gone: every other check passes, the exclusion is open only because
	// nothing has been fenced yet, and this host could fence.
	lab.powerOff("master-1")
	verdict = promotionVerdict(t, candidate)
	if !verdict.ReadyToFence {
		t.Fatalf("a promotion that would proceed to the fence was not offered: %#v", verdict)
	}
	if verdict.CanPromote {
		t.Fatal("can_promote was true before anything was fenced")
	}
	if check := verdictCheck(t, verdict, "old-master-exclusion"); check.Status != "unknown" {
		t.Fatalf("old-master-exclusion = %#v", check)
	}

	// A host with no profile at all reports that structurally too.
	plain := newTransferLab(t, 1)
	pushReplicas(t, plain.host("master-1"))
	plain.powerOff("master-1")
	verdict = promotionVerdict(t, plain.host("member-1"))
	if verdict.FenceRecorded || verdict.FenceUsable || verdict.ReadyToFence {
		t.Fatalf("a pool with no fence profile offered a promotion: %#v", verdict)
	}
	if !strings.Contains(verdict.FenceUnusableReason, "no fence profile is recorded") {
		t.Fatalf("fence_unusable_reason = %q", verdict.FenceUnusableReason)
	}
}

// The restriction's kind is on the wire wherever the restriction is, so no caller matches on
// the sentence to tell a restarted master from a superseded one.
func TestRestrictionReportsItsKind(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	status := PoolRestrictedReport(master)
	if status == nil || status.Kind != "unconfirmed" {
		t.Fatalf("restricted report = %#v", status)
	}
	markPoolAuthoritySuperseded(master)
	if status = PoolRestrictedReport(master); status == nil || status.Kind != "superseded" {
		t.Fatalf("restricted report after supersession = %#v", status)
	}
	// The authority probe carries the same record, so a peer reads the kind too.
	probe, err := probePoolAuthority(context.Background(), lab.host("member-1"), lab.origin["master-1"], "master-1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Restricted == nil || probe.Restricted.Kind != "superseded" {
		t.Fatalf("probe restriction = %#v", probe.Restricted)
	}
}

func TestFenceProfileFirstExposureBindsCurrentBootAuthority(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	writeLabFenceCredential(t, master, labFenceProfile().CredentialRef, labFenceIdentity)
	previous := poolBootAuthorityID
	poolBootAuthorityID = func() (string, error) { return "11111111-1111-4111-8111-111111111111", nil }
	t.Cleanup(func() { poolBootAuthorityID = previous })
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-current-boot", "master-1", "master-1", labFenceProfile())); result.Status != "succeeded" {
		t.Fatalf("recording first self fence = %#v", result)
	}
	if err := requirePoolStartPath(master); err != nil {
		t.Fatalf("first exposure lost its durable no-prior-fence authority proof: %v", err)
	}
	if got := peerState(t, master).BootAuthority; got == nil || got.PoolID != master.State.PoolID || got.HostID != master.State.HostID || got.BootID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("first exposure boot marker = %#v", got)
	}

	// A profile already existed, so a rotation has no proof that a peer could not have
	// fence-promoted this master. It must not create a marker if the existing one is gone.
	state := peerState(t, master)
	state.BootAuthority = nil
	if err := savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
	rotated := *labFenceProfile()
	rotated.Endpoint = "rotated-fence.example.test"
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-current-boot-rotated", "master-1", "master-1", &rotated)); result.Status != "succeeded" {
		t.Fatalf("rotating self fence = %#v", result)
	}
	if got := peerState(t, master).BootAuthority; got != nil {
		t.Fatalf("rotated exposure manufactured a boot marker: %#v", got)
	}
}

func TestFenceProfileFirstExposureDoesNotCommitWithoutBootAuthorityIdentity(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	writeLabFenceCredential(t, master, labFenceProfile().CredentialRef, labFenceIdentity)
	previous := poolBootAuthorityID
	poolBootAuthorityID = func() (string, error) { return "", errors.New("boot identity unavailable") }
	t.Cleanup(func() { poolBootAuthorityID = previous })
	if result := ExecutePoolFenceProfile(master, fenceProfileCommand("fp-no-boot-id", "master-1", "master-1", labFenceProfile())); result.Status != "needs_attention" {
		t.Fatalf("recording first self fence without boot identity = %#v", result)
	}
	state := peerState(t, master)
	if state.BootAuthority != nil || state.SelfFence != nil {
		t.Fatalf("failed first exposure persisted authority state: %#v", state)
	}
	if member, ok := poolMemberByID(state.Membership, master.State.HostID); !ok || member.Fence != nil {
		t.Fatalf("failed first exposure persisted profile: %#v", state.Membership)
	}
}

func TestPoolRestrictionClearRetainsRefusalWhenBootIdentityIsUnavailable(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	if err := EnsurePoolBootRestriction(master); err != nil {
		t.Fatal(err)
	}
	before := peerState(t, master).BootAuthority
	previous := poolBootAuthorityID
	poolBootAuthorityID = func() (string, error) { return "", errors.New("boot identity unavailable") }
	t.Cleanup(func() { poolBootAuthorityID = previous })
	clearPoolRestriction(master, poolRestrictedUnconfirmed)
	if err := requirePoolStartPath(master); err == nil {
		t.Fatal("boot identity failure cleared a start refusal")
	}
	if got := peerState(t, master).BootAuthority; !reflect.DeepEqual(got, before) {
		t.Fatalf("boot identity failure changed marker: before %#v after %#v", before, got)
	}
}
