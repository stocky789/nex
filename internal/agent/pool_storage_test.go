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
	"regexp"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestPoolStorageMutationFenceRejectsReservation(t *testing.T) {
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 2, Members: []protocol.PoolMember{
		{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 2, Status: "ready"},
		{HostID: "member-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 2, Status: "ready"},
	}}
	state := poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Access: map[string]map[string]protocol.PoolStorageAccess{}, Reservations: map[string]map[string]string{}, Mutations: map[string]string{}}
	state.Reservations["repo-1"] = map[string]string{"member-1": "vm-1"}
	if err := beginPoolStorageMutationState(&state, membership, "master-1", "repo-1", "update-1"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("durable member reservation did not fence mutation: %v", err)
	}
	delete(state.Reservations, "repo-1")
	if err := beginPoolStorageMutationState(&state, membership, "master-1", "repo-1", "update-1"); err != nil {
		t.Fatalf("drained repository could not acquire mutation fence: %v", err)
	}
	if state.Mutations["repo-1"] != "update-1" {
		t.Fatalf("mutation was not retained: %#v", state.Mutations)
	}
}

func TestPoolReservationKeepsLiveOperationWhenLaterOperationFails(t *testing.T) {
	state := poolStorageState{Reservations: map[string]map[string]string{}, Mutations: map[string]string{}}
	if !recordPoolStorageReservation(&state, "repo-1", "member-1", "vm-live") {
		t.Fatal("initial reservation was not recorded")
	}
	if recordPoolStorageReservation(&state, "repo-1", "member-1", "vm-failed") {
		t.Fatal("later operation replaced the live reservation")
	}
	// This is the master DELETE rule for a failed second operation: only its own
	// reservation may be deleted, so vm-live remains durably fenced.
	if state.Reservations["repo-1"]["member-1"] == "vm-failed" {
		delete(state.Reservations["repo-1"], "member-1")
	}
	if state.Reservations["repo-1"]["member-1"] != "vm-live" {
		t.Fatalf("live reservation lost: %#v", state.Reservations)
	}
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	if err := beginPoolStorageMutationState(&state, membership, "master-1", "repo-1", "detach"); err == nil {
		t.Fatal("mutation ignored surviving live reservation")
	}
}

func TestPoolMasterDoesNotReconcileItsOwnStorage(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if err := ReconcilePoolStorageFromMaster(context.Background(), s); err != nil {
		t.Fatalf("master reconcile was not a no-op: %v", err)
	}
}

func TestMemberReconcileCheckReportsFailureThenFreshRecovery(t *testing.T) {
	s := peerStore(t, "member-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}, {HostID: "member-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if check := PoolStorageReconcileCheck(s); check == nil || check.Status != "unknown" {
		t.Fatalf("initial check = %#v", check)
	}
	recordPoolReconcile(s, errors.New("transport failed"))
	if check := PoolStorageReconcileCheck(s); check == nil || check.Status != "failed" || check.Error == "transport failed" {
		t.Fatalf("failure check = %#v", check)
	}
	recordPoolReconcile(s, nil)
	if check := PoolStorageReconcileCheck(s); check == nil || check.Status != "healthy" {
		t.Fatalf("recovery check = %#v", check)
	}
}

func TestPoolStorageOperationIsManagedCommandIdentifier(t *testing.T) {
	operation := poolStorageOperation("apply", "repo-1", 7)
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(operation) {
		t.Fatalf("invalid generated operation ID %q", operation)
	}
}

func TestUnusedReservationSelectionWaitsForJournalReferencesToDisappear(t *testing.T) {
	managed := &protocol.ManagedState{Storage: []protocol.StorageRegistration{{ID: "repo-1", Kind: protocol.StorageKindNFS}}, VMs: []protocol.VMCreateSpec{{ID: "vm-1", Disks: []protocol.VMDisk{{StorageID: "repo-1"}}}}}
	if ids, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "healthy"}}); !safe || len(ids) != 0 {
		t.Fatalf("live disk released reservation: %v %v", ids, safe)
	}
	managed.VMs[0].Disks = nil // Durable vm-update removed the last disk.
	if ids, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "healthy"}}); !safe || len(ids) != 1 || ids[0] != "repo-1" {
		t.Fatalf("removed disk did not release: %v %v", ids, safe)
	}
	managed.VMs = []protocol.VMCreateSpec{{ID: "vm-1", MediaID: "iso-1"}}
	managed.Media = []protocol.Media{{ID: "iso-1", StorageID: "repo-1"}}
	if ids, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "healthy"}}); !safe || len(ids) != 0 {
		t.Fatalf("attached media released reservation: %v %v", ids, safe)
	}
	managed.VMs[0].MediaID = "" // Durable media-eject no longer refers to the ISO.
	managed.Media = nil
	if ids, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "healthy"}}); !safe || len(ids) != 1 {
		t.Fatalf("ejected media did not release: %v %v", ids, safe)
	}
	if _, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "unknown"}}); safe {
		t.Fatal("unhealthy journal observation released a reservation")
	}
}

func TestPoolReservationReleaseUsesAuthenticatedPeerDelete(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "member-1", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export"}}, Generation: 1}
	journal := map[string]any{"command": protocol.Command{OperationID: "memberstorage1", Kind: "storage-register", Storage: &storage}, "result": protocol.Result{OperationID: "memberstorage1", Status: "succeeded"}, "applied_storage": storage}
	b, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(member.dir, "managed-vms")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "memberstorage1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	vmID := "12345678-1234-4234-8234-123456789012"
	if err = os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\necho "+vmID+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	vm := protocol.VMCreateSpec{ID: vmID, PoolID: "pool-1", HostID: "member-1", Disks: []protocol.VMDisk{{StorageID: "repo-1", Name: "disk", SizeGiB: 1}}}
	vmJournal := map[string]any{"command": protocol.Command{OperationID: "membervm001", Kind: "vm-create", VM: &vm}, "result": protocol.Result{OperationID: "membervm001", Status: "succeeded"}}
	b, err = json.Marshal(vmJournal)
	if err != nil {
		t.Fatal(err)
	}
	vmPath := filepath.Join(dir, "membervm001.json")
	if err = os.WriteFile(vmPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = savePoolStorage(master, poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Access: map[string]map[string]protocol.PoolStorageAccess{}, Reservations: map[string]map[string]string{"repo-1": {"member-1": "vm-live"}}, Mutations: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	// A durable VM disk reference suppresses the authenticated release.
	ReleaseUnusedPoolStorageReservations(context.Background(), member)
	state, err := loadPoolStorage(master)
	if err != nil {
		t.Fatal(err)
	}
	if state.Reservations["repo-1"]["member-1"] != "vm-live" {
		t.Fatalf("live journal reference was released: %#v", state.Reservations)
	}
	if err = os.Remove(vmPath); err != nil {
		t.Fatal(err)
	}
	// Actual durable journal projection plus the mTLS peer DELETE—not a helper-only path.
	ReleaseUnusedPoolStorageReservations(context.Background(), member)
	state, err = loadPoolStorage(master)
	if err != nil {
		t.Fatal(err)
	}
	if state.Reservations["repo-1"]["member-1"] != "" {
		t.Fatalf("mTLS release did not remove member reservation: %#v", state.Reservations)
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	if err = beginPoolStorageMutationState(&state, membership, "master-1", "repo-1", "detach-1"); err != nil {
		t.Fatalf("released reservation still fenced master mutation: %v", err)
	}
}

func TestPoolMemberStorageIntentStripsMasterObservationsForEngineValidation(t *testing.T) {
	master := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Kind: protocol.StorageKindNFS, Generation: 1, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export"}}, Path: "/var/lib/nex-storage/master", Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export"}}, Access: protocol.StorageAccess{Status: "healthy"}}
	if err := protocol.ValidateStorageRegistration(master, false); err == nil {
		t.Fatal("master observation unexpectedly accepted as member command")
	}
	member := poolMemberStorageIntent(master)
	member.HostID = "member-1"
	if err := protocol.ValidateStorageRegistration(member, false); err != nil {
		t.Fatalf("client-shaped member intent rejected: %v", err)
	}
	if member.Path != "" || member.Identity.NFS != nil || member.Access.Status != "" {
		t.Fatalf("master observation leaked into intent: %#v", member)
	}
}

func TestReportProjectsCoreMembershipForControllerAdoption(t *testing.T) {
	reports := make(chan protocol.ReportRequest, 1)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		reports <- request
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	s.State.PoolID = "pool-1"
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: identity.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: identity.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready", ObservedAt: time.Now().UTC()}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	request := <-reports
	if request.Inventory.Managed == nil || request.Inventory.Managed.Membership == nil || request.Inventory.Managed.Membership.PoolID != "pool-1" {
		t.Fatalf("membership was not projected: %#v", request.Inventory.Managed)
	}
}

func TestPoolStorageMutationFenceRetainsUncertainOutcome(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{CAKeyPEM: "master-key", Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if err := savePoolStorage(s, poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Access: map[string]map[string]protocol.PoolStorageAccess{}, Reservations: map[string]map[string]string{}, Mutations: map[string]string{"repo-1": "detach-1"}}); err != nil {
		t.Fatal(err)
	}
	finishPoolStorageMutation(s, "repo-1", "detach-1", false)
	state, err := loadPoolStorage(s)
	if err != nil || state.Mutations["repo-1"] != "detach-1" {
		t.Fatalf("uncertain outcome cleared fence: %#v %v", state.Mutations, err)
	}
	finishPoolStorageMutation(s, "repo-1", "detach-1", true)
	state, err = loadPoolStorage(s)
	if err != nil || state.Mutations["repo-1"] != "" {
		t.Fatalf("definitive outcome retained fence: %#v %v", state.Mutations, err)
	}
}
