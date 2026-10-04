// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestPlacementErrorIsBoundedAndSingleLine(t *testing.T) {
	if got := placementError(errors.New("disk has no space\nsecret")); got != "selected host placement check failed" {
		t.Fatalf("unsafe placement error %q", got)
	}
	if got := placementError(errors.New(strings.Repeat("x", 300))); len(got) != 240 {
		t.Fatalf("placement error length %d", len(got))
	}
}

func TestPoolPlacementPeerBindsExactReplayToCurrentMember(t *testing.T) {
	master, member, _, _ := cpuPeerPair(t)
	member.State.PoolID = master.State.PoolID
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", member.State.HostID)
	vm.PoolID = master.State.PoolID
	command := protocol.Command{OperationID: "placement-replay", Kind: "vm-create", VM: vm}
	dir := filepath.Join(member.dir, "managed-vms")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"command": command, "result": protocol.Result{OperationID: command.OperationID, Status: "needs_attention"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, command.OperationID+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var selected protocol.PoolMember
	for _, candidate := range membership.Members {
		if candidate.HostID == member.State.HostID {
			selected = candidate
		}
	}
	reply, err := poolVMPlacement(context.Background(), master, membership, selected, command)
	if err != nil || !reply.Accepted || !reply.Replay || reply.OperationID != command.OperationID || reply.VMID != vm.ID || !reflect.DeepEqual(reply.VM, *vm) {
		t.Fatalf("placement replay = %+v, %v", reply, err)
	}
	changed := *vm
	changed.Name = "different"
	if _, err = poolVMPlacement(context.Background(), master, membership, selected, protocol.Command{OperationID: command.OperationID, Kind: command.Kind, VM: &changed}); err == nil {
		t.Fatal("same operation accepted different placement definition")
	}
}

// BUG-0405: an invalid definition was refused without saying what was wrong. On the master
// it read "VM placement preflight is invalid"; on a member the peer handler answered in
// plain text, so the master could not decode a reason and the operator was told only
// "selected VM host refused placement observation" — which does not even say the cause was
// validation. Both paths must now name the field.
func TestPoolPlacementRefusalNamesTheInvalidField(t *testing.T) {
	master, member, _, _ := cpuPeerPair(t)
	member.State.PoolID = master.State.PoolID
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", member.State.HostID)
	vm.PoolID = master.State.PoolID
	// Revision must be 0 at creation; Core refuses any other value. This is the exact shape
	// that produced the unusable message in the lab.
	vm.Revision = 7
	command := protocol.Command{OperationID: "placement-invalid", Kind: "vm-create", VM: vm}

	direct := placementCommandError(command)
	if direct == nil {
		t.Fatal("an invalid definition was accepted by the placement validator")
	}
	if strings.TrimSpace(direct.Error()) == "" {
		t.Fatal("the placement validator gave an empty reason")
	}

	var selected protocol.PoolMember
	for _, candidate := range membership.Members {
		if candidate.HostID == member.State.HostID {
			selected = candidate
		}
	}
	_, err = poolVMPlacement(context.Background(), master, membership, selected, command)
	if err == nil {
		t.Fatal("an invalid definition was placed on a member")
	}
	// The whole point: the member's reason has to survive the peer hop.
	if strings.Contains(err.Error(), "refused placement observation") {
		t.Fatalf("member refusal lost its reason across the peer hop: %v", err)
	}
	if !strings.Contains(err.Error(), "revision") {
		t.Fatalf("member refusal does not name the invalid field: %v", err)
	}
}

func TestPoolPlacementRejectsUnauthenticatedCaller(t *testing.T) {
	_, _, _, memberServer := cpuPeerPair(t)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	defer client.CloseIdleConnections()
	response, err := client.Post(memberServer.URL+poolPlacementPath, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated placement status %d", response.StatusCode)
	}
}

func TestManagedWorkerKeepsExactPlacementReplayAmbiguousWhenMasterIsOffline(t *testing.T) {
	master, member, masterServer, _ := cpuPeerPair(t)
	member.State.PoolID = master.State.PoolID
	membership, err := Membership(member)
	if err != nil || membership.MasterHostID != master.State.HostID {
		t.Fatalf("member membership = %+v, %v", membership, err)
	}
	masterServer.Close() // The existing journal must not become a new definite failure on outage.
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", member.State.HostID)
	vm.PoolID = master.State.PoolID
	command := protocol.Command{OperationID: "placement-offline-replay", Kind: "vm-create", VM: vm}
	dir := filepath.Join(member.dir, "managed-vms")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"command": command, "result": protocol.Result{OperationID: command.OperationID, Status: "needs_attention", Error: "interrupted"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, command.OperationID+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands := make(chan protocol.Command, 1)
	results := make(chan managedCompletion, 1)
	done := make(chan struct{})
	go func() { defer close(done); managedWorker(ctx, member.dir, member.State, commands, results) }()
	commands <- command
	select {
	case completion := <-results:
		if completion.err != nil || completion.result.Status != "needs_attention" || completion.result.Error == "" {
			t.Fatalf("offline exact replay = %+v", completion)
		}
	case <-time.After(time.Minute):
		t.Fatal("worker did not settle exact replay")
	}
	cancel()
	<-done
}

func TestManagedWorkerDoesNotReconcileClusteredReplayAfterAdmissionRefusal(t *testing.T) {
	master, member, masterServer, _ := cpuPeerPair(t)
	member.State.PoolID = master.State.PoolID
	masterServer.Close()
	storage := protocol.StorageRegistration{ID: "cluster-repo", PoolID: member.State.PoolID, HostID: member.State.HostID, Name: "cluster", Path: "/var/lib/nex-storage/cluster", Kind: protocol.StorageKindISCSI, Generation: 1, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:cluster", LUN: 1, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "12345678-1234-1234-1234-123456789abc", MountPath: "/var/lib/nex-storage/cluster", Mode: "clustered_gfs2", ClusterID: "cluster-1", LockTable: "cluster-1:cluster", EligibleHostIDs: []string{member.State.HostID, "host-b", "host-c"}}}}
	storage.Identity.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: storage.Source.ISCSI.TargetIQN, LUN: storage.Source.ISCSI.LUN, WWID: storage.Source.ISCSI.WWID, FilesystemUUID: storage.Source.ISCSI.FilesystemUUID, FilesystemType: "gfs2", GFS2LockTable: storage.Source.ISCSI.LockTable, GFS2Journals: 3}
	vm := poolVMTestSpec("22222222-1111-4111-8111-111111111111", member.State.HostID)
	vm.PoolID, vm.Disks[0].StorageID = member.State.PoolID, storage.ID
	command := protocol.Command{OperationID: "cluster-placement-replay", Kind: "vm-create", VM: vm}
	dir := filepath.Join(member.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), dir, storage); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"command": command, "result": protocol.Result{OperationID: command.OperationID, Status: "needs_attention", Error: "interrupted"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, command.OperationID+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	virshDir := t.TempDir()
	virshSentinel := filepath.Join(t.TempDir(), "virsh-invoked")
	if err = os.WriteFile(filepath.Join(virshDir, "virsh"), []byte("#!/bin/sh\n: > \"$NEX_TEST_VIRSH_SENTINEL\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEX_TEST_VIRSH_SENTINEL", virshSentinel)
	t.Setenv("PATH", virshDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands, results := make(chan protocol.Command, 1), make(chan managedCompletion, 1)
	done := make(chan struct{})
	go func() { defer close(done); managedWorker(ctx, member.dir, member.State, commands, results) }()
	commands <- command
	select {
	case completion := <-results:
		if completion.err != nil || completion.result.Status != "needs_attention" {
			t.Fatalf("clustered replay = %+v", completion)
		}
	case <-time.After(time.Minute):
		t.Fatal("worker did not preserve clustered replay")
	}
	// The journal remains unresolved; no native create/reconciliation result was written.
	raw, err = os.ReadFile(filepath.Join(dir, command.OperationID+".json"))
	if err != nil || !strings.Contains(string(raw), "needs_attention") || strings.Contains(string(raw), "succeeded") {
		t.Fatalf("clustered replay journal changed: %s %v", raw, err)
	}
	if _, err = os.Stat(virshSentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clustered replay invoked native virsh: %v", err)
	}
	cancel()
	<-done
}

func placementMembership(t *testing.T, s *Store, generation uint64) protocol.PoolMembership {
	t.Helper()
	membership := protocol.PoolMembership{PoolID: s.State.PoolID, MasterHostID: s.State.HostID, Generation: generation, Members: []protocol.PoolMember{
		{HostID: s.State.HostID, Hostname: s.State.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: generation, Status: "ready"},
		{HostID: "member-b", Hostname: "member-b", CertificateSHA256: strings.Repeat("b", 64), Generation: generation, Status: "ready"},
	}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	return membership
}

func TestLocalPlacementRejectsStaleMembershipWithoutOwnerRecord(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	placementMembership(t, s, 2) // The selected master is deliberately not the last member.
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	err := poolVMLocalPlacement(s, vm.ID, "define", nil, true, vm, nil, &poolPlacementReply{Generation: 1, VMID: vm.ID, VM: *vm, Accepted: true})
	if err == nil || !strings.Contains(err.Error(), "membership changed") {
		t.Fatalf("stale placement admission = %v", err)
	}
	state, err := loadPoolVMs(s)
	if err != nil || len(state.VMs) != 0 {
		t.Fatalf("stale placement wrote owner state: %#v, %v", state, err)
	}
}

func TestLocalPlacementReplayRequiresExistingOwnerOrReservation(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	membership := placementMembership(t, s, 1)
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	placement := &poolPlacementReply{Generation: membership.Generation, VMID: vm.ID, VM: *vm, Accepted: true, Replay: true}
	if err := poolVMLocalPlacement(s, vm.ID, "define", nil, true, vm, nil, placement); err == nil {
		t.Fatal("replayed create without durable owner was accepted")
	}
	state := poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: vm.PoolID, VMID: vm.ID, HostID: s.State.HostID, State: "stopped", Spec: *vm}}}
	if err := savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	next := *vm
	next.Revision = vm.Revision + 1
	if err := poolVMLocalUpdatePlacement(s, vm.ID, "update-reserve", "replay-update", vm, &next, nil, placement); err == nil {
		t.Fatal("replayed update without matching durable reservation was accepted")
	}
	got, err := loadPoolVMs(s)
	if err != nil || got.VMs[vm.ID].PendingUpdate != nil {
		t.Fatalf("replayed update changed owner state: %#v, %v", got, err)
	}
}

// This exercises final master admission after preflight, without pretending to
// verify native storage. Native preflight is covered in vmhost/placement_test.go.
func TestLocalPlacementPersistsExactOwnerAndUpdateReservation(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	membership := placementMembership(t, s, 1)
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	placement := &poolPlacementReply{Generation: membership.Generation, VMID: vm.ID, VM: *vm, Accepted: true}
	if err := poolVMLocalPlacement(s, vm.ID, "define", nil, true, vm, nil, placement); err != nil {
		t.Fatal(err)
	}
	initial, err := loadPoolVMs(s)
	if err != nil || len(initial.VMs) != 1 || !reflect.DeepEqual(initial.VMs[vm.ID].Spec, *vm) || initial.VMs[vm.ID].HostID != s.State.HostID {
		t.Fatalf("owner record = %+v, %v", initial, err)
	}
	placement.Replay = true
	if err := poolVMLocalPlacement(s, vm.ID, "define", nil, true, vm, nil, placement); err != nil {
		t.Fatal(err)
	}
	changed := *vm
	changed.Name = "changed-retry"
	if err := poolVMLocalPlacement(s, vm.ID, "define", nil, true, &changed, nil, placement); err == nil {
		t.Fatal("changed create replay accepted")
	}
	replayed, err := loadPoolVMs(s)
	if err != nil || !reflect.DeepEqual(initial, replayed) {
		t.Fatalf("create retry changed state: %+v, %v", replayed, err)
	}

	next := *vm
	next.Revision++
	next.Name = "updated-vm"
	placement.VM, placement.Replay = next, false
	if err := poolVMLocalUpdatePlacement(s, vm.ID, "update-reserve", "update-local", vm, &next, nil, placement); err != nil {
		t.Fatal(err)
	}
	reserved, err := loadPoolVMs(s)
	if err != nil || !reflect.DeepEqual(reserved.VMs[vm.ID].Spec, *vm) || !placementReservationMatches(reserved.VMs[vm.ID], "update-local", &next) {
		t.Fatalf("reservation = %+v, %v", reserved, err)
	}
	placement.Replay = true
	if err := poolVMLocalUpdatePlacement(s, vm.ID, "update-reserve", "update-local", vm, &next, nil, placement); err != nil {
		t.Fatal(err)
	}
	changed = next
	changed.Name = "changed-update-retry"
	if err := poolVMLocalUpdatePlacement(s, vm.ID, "update-reserve", "update-local", vm, &changed, nil, placement); err == nil {
		t.Fatal("changed update replay accepted")
	}
	placementMembership(t, s, 2)
	if err := poolVMLocalUpdatePlacement(s, vm.ID, "update-reserve", "update-local", vm, &next, nil, placement); err == nil {
		t.Fatal("stale update preflight accepted")
	}
	final, err := loadPoolVMs(s)
	if err != nil || !reflect.DeepEqual(reserved, final) {
		t.Fatalf("update retries changed durable state: %+v, %v", final, err)
	}
}
