// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testManagedIdentity(t *testing.T, handler http.Handler) (*httptest.Server, State) {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	key, err := x509.MarshalPKCS8PrivateKey(srv.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return srv, State{Controller: srv.URL, TrustPEM: cert, CertificatePEM: cert, KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})), HostID: "host-1", Journal: map[string]JournalEntry{}}
}

func TestManagedCommandJournalDoesNotCompleteFromInventoryOrReplayChangedInput(t *testing.T) {
	s := testStore(t)
	cmd := protocol.Command{OperationID: "managed-1", Kind: "vm-create", VM: &protocol.VMCreateSpec{ID: "vm-1", HostID: "host-1", CPU: protocol.VMCPU{Count: 2}}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	requested := s.State.Journal[cmd.OperationID].RequestedAt
	completeJournal(s, &protocol.Inventory{ObservedAt: requested.Add(time.Hour)}, time.Millisecond)
	if s.State.Journal[cmd.OperationID].Status != "pending" {
		t.Fatal("inventory acknowledged a mutation")
	}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved State
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Journal[cmd.OperationID].Command.VM.CPU.Count != 2 {
		t.Fatal("full command not persisted before dispatch")
	}
	changed := cmd
	spec := *cmd.VM
	spec.CPU.Count = 3
	changed.VM = &spec
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{changed}}); err == nil {
		t.Fatal("changed payload replay accepted")
	}
	s.State.Journal["legacy"] = JournalEntry{Status: "succeeded"}
	cmd.OperationID = "legacy"
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err == nil {
		t.Fatal("legacy refresh ID reused for mutation")
	}
}

func TestManagedMediaUsesIdentityAndRefusesRedirectAndTraversal(t *testing.T) {
	var calls atomic.Int32
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || len(r.TLS.PeerCertificates) != 1 {
			t.Error("media fetch missing authenticated GET")
		}
		switch r.URL.Path {
		case "/api/v1/agent/media/good":
			io.WriteString(w, "ISO")
		case "/api/v1/agent/media/redirect":
			http.Redirect(w, r, "/api/v1/agent/media/good", http.StatusFound)
		default:
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
	fetch := mediaFetcher(identity)
	body, err := fetch(context.Background(), "good")
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(b) != "ISO" {
		t.Fatalf("read %q: %v", b, err)
	}
	if _, err = fetch(context.Background(), "redirect"); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("redirect followed")
	}
	for _, id := range []string{"", "../secret", "%2fsecret", "x?secret", "x#fragment", "x\\path"} {
		if _, err = fetch(context.Background(), id); err == nil {
			t.Fatalf("invalid identity %q accepted", id)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("invalid identity reached network")
	}
}

func TestManagedWorkerRejectsDifferentHostWithoutEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands := make(chan protocol.Command, 1)
	results := make(chan managedCompletion, 1)
	done := make(chan struct{})
	dir := t.TempDir()
	go func() { defer close(done); managedWorker(ctx, dir, State{HostID: "host-1"}, commands, results) }()
	commands <- protocol.Command{OperationID: "wrong-host", Kind: "storage-register", Storage: &protocol.StorageRegistration{HostID: "host-2", Path: "/var/lib/libvirt/images"}}
	select {
	case result := <-results:
		if result.err != nil || result.result.Status != "failed" || !strings.Contains(result.result.Error, "identity") {
			t.Fatalf("unexpected completion: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("worker blocked")
	}
	cancel()
	<-done
	// The worker registers the host's own defaults; the refused command left no journal entry.
	if _, err := os.Stat(filepath.Join(dir, "managed-vms", "wrong-host.json")); !os.IsNotExist(err) {
		t.Fatal("wrong-host command created engine state")
	}
}

func TestMemberSuccessfulEjectAndDiskRemovalReleaseUnusedReservations(t *testing.T) {
	membership := protocol.PoolMembership{MasterHostID: "master-1"}
	for _, command := range []protocol.Command{
		{Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: "vm-1", HostID: "member-1", Action: "eject"}},
		{Kind: "vm-update", VM: &protocol.VMCreateSpec{ID: "vm-1", HostID: "member-1"}},
	} {
		if !shouldReleaseUnusedReservation(membership, nil, "member-1", true, nil, "succeeded") {
			t.Fatalf("successful %s did not trigger stale-reservation cleanup", command.Kind)
		}
	}
	if shouldReleaseUnusedReservation(membership, nil, "member-1", false, nil, "succeeded") || shouldReleaseUnusedReservation(membership, nil, "member-1", true, errors.New("failed"), "failed") {
		t.Fatal("undurable operation triggered cleanup")
	}
}

func TestTransientStorageDiscoveryResultsDoNotLoseConcurrentCompletions(t *testing.T) {
	s := &Store{}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			appendStorageDiscoveries(s, []protocol.StorageDiscoveryResult{{RequestID: fmt.Sprintf("discover-%d", i)}})
		}(i)
	}
	wg.Wait()
	got := drainStorageDiscoveries(s)
	if len(got) != 2 {
		t.Fatalf("discovery results = %+v", got)
	}
	if more := drainStorageDiscoveries(s); len(more) != 0 {
		t.Fatalf("drain retained results: %+v", more)
	}
}

func TestStorageDiscoveryRequiresThisCoreHostAndPool(t *testing.T) {
	state := State{HostID: "host-1", PoolID: "pool-1"}
	if !storageDiscoveryScopeMatches(state, protocol.StorageDiscoveryRequest{HostID: "host-1", PoolID: "pool-1"}) {
		t.Fatal("matching discovery request rejected")
	}
	if storageDiscoveryScopeMatches(state, protocol.StorageDiscoveryRequest{HostID: "host-2", PoolID: "pool-1"}) || storageDiscoveryScopeMatches(state, protocol.StorageDiscoveryRequest{HostID: "host-1", PoolID: "pool-2"}) {
		t.Fatal("cross-Core discovery request accepted")
	}
}

func TestStorageClusterRequiresThisCoreHostAndPool(t *testing.T) {
	state := State{HostID: "host-1", PoolID: "pool-1"}
	if !storageClusterScopeMatches(state, protocol.StorageClusterRequest{HostID: "host-1", PoolID: "pool-1", Action: "verify_fence"}) {
		t.Fatal("matching clustered storage request rejected")
	}
	if storageClusterScopeMatches(state, protocol.StorageClusterRequest{HostID: "host-2", PoolID: "pool-1", Action: "status"}) || storageClusterScopeMatches(state, protocol.StorageClusterRequest{HostID: "host-1", PoolID: "pool-2", Action: "apply"}) || storageClusterScopeMatches(state, protocol.StorageClusterRequest{HostID: "host-1", PoolID: "pool-1", Action: "unknown"}) {
		t.Fatal("cross-Core or unknown clustered storage request accepted")
	}
}

func TestManagedSlowCommandKeepsReportingAndCancels(t *testing.T) {
	bin := t.TempDir()
	started := filepath.Join(bin, "started")
	script := "#!/bin/sh\ncase \"$*\" in\n *net-list*) touch \"$NEX_TEST_STARTED\"; sleep 2; echo default ;;\n *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("NEX_TEST_STARTED", started)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var reports, whileRunning atomic.Int32
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if !slices.Contains(req.Capabilities, protocol.ManagedVMCapability) || !slices.Contains(req.Capabilities, protocol.ConsoleCapability) || !slices.Contains(req.Capabilities, protocol.PowerCapability) || !slices.Contains(req.Capabilities, protocol.MediaCapability) || !slices.Contains(req.Capabilities, protocol.ISODownloadCapability) {
			t.Error("managed capability missing")
		}
		count := reports.Add(1)
		if _, err := os.Stat(started); err == nil {
			if whileRunning.Add(1) >= 3 {
				cancel()
			}
		}
		response := protocol.ReportResponse{}
		if count == 1 {
			response.Commands = []protocol.Command{{OperationID: "slow-network", Kind: "network-register", Network: &protocol.NetworkRegistration{ID: "net-1", PoolID: "pool-1", HostID: "host-1", Name: "Default", Kind: "network", Source: "default"}}}
		}
		json.NewEncoder(w).Encode(response)
	}))
	s := testStore(t)
	s.State = identity
	start := time.Now()
	if err := Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: time.Second, ProbeTimeout: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if whileRunning.Load() < 3 {
		t.Fatalf("heartbeat blocked during command: %d reports", whileRunning.Load())
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("worker did not cancel promptly")
	}
	if s.State.Journal["slow-network"].Status == "succeeded" {
		t.Fatal("cancelled command was incorrectly completed")
	}
}

func TestClusteredISCSISharedStorageRequiresMembershipAndMasterRegistration(t *testing.T) {
	shared := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "member-1", Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2"}}}
	cmd := protocol.Command{Kind: "storage-register", Storage: &shared}
	s := peerStore(t, "member-1", "pool-1")
	if !commandReferencesRemoteStorage(context.Background(), s, cmd) {
		t.Fatal("clustered iSCSI did not require membership for a shared storage command")
	}
	membership := protocol.PoolMembership{MasterHostID: "master-1"}
	if !poolMemberStorageRegistrationDenied(membership, "member-1", &shared) {
		t.Fatal("member direct clustered iSCSI registration was not refused")
	}
	if poolMemberStorageRegistrationDenied(membership, "master-1", &shared) {
		t.Fatal("pool master clustered iSCSI registration was refused")
	}
}

func TestReportBoundsManagedLibvirtObservation(t *testing.T) {
	bin := t.TempDir()
	started := filepath.Join(bin, "virsh-started")
	script := "#!/bin/sh\ncase \"$*\" in\n *'list --all --uuid'*) touch \"$NEX_TEST_VIRSH_STARTED\"; exec sleep 30 ;;\n *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("NEX_TEST_VIRSH_STARTED", started)

	reports := make(chan protocol.ReportRequest, 1)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		reports <- req
		json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	vm := protocol.VMCreateSpec{
		ID: "12345678-1234-4234-8234-123456789012", PoolID: "pool-1", HostID: "host-1", Name: "guest", GuestOS: "linux",
		CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: "storage-1", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}},
		Boot:  protocol.VMBoot{Firmware: "uefi"},
	}
	journal := struct {
		Command protocol.Command `json:"command"`
		Result  protocol.Result  `json:"result"`
		Stage   string           `json:"stage"`
	}{
		Command: protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &vm},
		Result:  protocol.Result{OperationID: "create-1", Status: "succeeded"},
		Stage:   "defined",
	}
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	managedDir := filepath.Join(s.dir, "managed-vms")
	if err = os.MkdirAll(managedDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(managedDir, "create-1.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err = report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("report waited for libvirt observation: %v", elapsed)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("blocking virsh was not invoked: %v", err)
	}
	req := <-reports
	if req.Inventory.Managed == nil || len(req.Inventory.Managed.MissingVMs) != 0 {
		t.Fatalf("unverified libvirt state fabricated missing VMs: %+v", req.Inventory.Managed)
	}
	for _, check := range req.Inventory.Checks {
		if check.Name == "managed-vms" {
			if check.Status != "unknown" {
				t.Fatalf("managed VM check = %+v, want unknown", check)
			}
			return
		}
	}
	t.Fatalf("managed VM check absent: %+v", req.Inventory.Checks)
}

func TestResultBacklogDrainsInControllerSizedBatches(t *testing.T) {
	batches := make(chan int, 2)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		batches <- len(req.Results)
		if len(req.Results) > 100 {
			http.Error(w, "invalid operation result count", 400)
			return
		}
		response := protocol.ReportResponse{}
		for _, result := range req.Results {
			response.AcknowledgedResults = append(response.AcknowledgedResults, result.OperationID)
		}
		json.NewEncoder(w).Encode(response)
	}))
	s := testStore(t)
	s.State = identity
	for i := 0; i < 150; i++ {
		s.State.Journal[fmt.Sprintf("op-%d", i)] = JournalEntry{Status: "succeeded", ObservedAt: time.Now().UTC()}
	}
	m := NewMonitor(time.Second, nil)
	for _, want := range []int{100, 50} {
		if err := report(context.Background(), s, m, RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		if got := <-batches; got != want {
			t.Fatalf("batch %d, want %d", got, want)
		}
	}
	for id, result := range s.State.Journal {
		if !result.Acknowledged {
			t.Fatalf("result %s lost from acknowledgment", id)
		}
	}
}

func TestOversizedResultErrorDoesNotBlockHeartbeat(t *testing.T) {
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if len(req.Results) != 1 || len(req.Results[0].Error) > 4096 {
			http.Error(w, "invalid operation result", 400)
			return
		}
		if req.Results[0].Error != "a"+strings.Repeat("🙂", 1023) {
			t.Error("result error not UTF-8-safe bounded prefix")
		}
		json.NewEncoder(w).Encode(protocol.ReportResponse{AcknowledgedResults: []string{"oversized"}})
	}))
	s := testStore(t)
	s.State = identity
	original := "a" + strings.Repeat("🙂", 1200)
	s.State.Journal["oversized"] = JournalEntry{Status: "failed", Error: original, ObservedAt: time.Now().UTC()}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if result := s.State.Journal["oversized"]; !result.Acknowledged || result.Error != original {
		t.Fatal("full local error not retained/acknowledged")
	}
}

func TestSingleHostCoreIsItsPoolMasterForRemoteStorage(t *testing.T) {
	s := peerStore(t, "host-1", "pool-1")
	nfs := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}}
	cmd := protocol.Command{Kind: "storage-register", Storage: &nfs}
	if _, err := Membership(s); err == nil || !commandReferencesRemoteStorage(context.Background(), s, cmd) {
		t.Fatal("a Core without a membership record must refuse remote storage")
	}
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != "pool-1" || membership.MasterHostID != "host-1" || len(membership.Members) != 1 {
		t.Fatalf("single-host Core is not its pool master: %#v, %v", membership, err)
	}
	if poolMemberStorageRegistrationDenied(membership, "host-1", &nfs) {
		t.Fatal("single-host pool master NFS registration was refused")
	}
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	if again, _ := Membership(s); again.Members[0].CertificateSHA256 != membership.Members[0].CertificateSHA256 {
		t.Fatal("startup replaced an existing membership record")
	}
}
