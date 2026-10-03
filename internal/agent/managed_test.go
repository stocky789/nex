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
	commands := make(chan protocol.Command, 1)
	results := make(chan managedCompletion, 1)
	done := make(chan struct{})
	dir := t.TempDir()
	go func() { defer close(done); managedWorker(ctx, dir, State{HostID: "host-1"}, commands, results) }()
	defer func() { cancel(); <-done }()
	commands <- protocol.Command{OperationID: "wrong-host", Kind: "storage-register", Storage: &protocol.StorageRegistration{HostID: "host-2", Path: "/var/lib/libvirt/images"}}
	select {
	case result := <-results:
		if result.err != nil || result.result.Status != "failed" || !strings.Contains(result.result.Error, "identity") {
			t.Fatalf("unexpected completion: %+v", result)
		}
	case <-time.After(10 * time.Second):
		// Identity rejection has no latency contract; allow native NIC discovery and
		// scheduling under the race-enabled CI suite before declaring a deadlock.
		t.Fatal("worker blocked")
	}
	cancel()
	<-done
	// The worker registers the host's own defaults; the refused command left no journal entry.
	if _, err := os.Stat(filepath.Join(dir, "managed-vms", "wrong-host.json")); !os.IsNotExist(err) {
		t.Fatal("wrong-host command created engine state")
	}
}

func TestManagedWorkerBackgroundCompletionSendCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan managedCompletion, 1)
	completed <- managedCompletion{result: protocol.Result{OperationID: "occupied"}}
	done := make(chan bool, 1)
	go func() {
		select {
		case completed <- managedCompletion{result: protocol.Result{OperationID: "recovered"}, background: true}:
			done <- true
		case <-ctx.Done():
			done <- false
		}
	}()
	select {
	case <-done:
		t.Fatal("background completion bypassed the full channel")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case sent := <-done:
		if sent {
			t.Fatal("canceled background completion was sent")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked background completion did not exit on cancellation")
	}
	if got := <-completed; got.result.OperationID != "occupied" {
		t.Fatal("cancellation replaced the queued completion")
	}
}

func TestManagedWorkerBackgroundCompletionPreservesForeground(t *testing.T) {
	s := &Store{dir: t.TempDir(), State: State{Journal: map[string]JournalEntry{
		"foreground": {Command: &protocol.Command{OperationID: "foreground", Kind: "vm-start"}, Status: "pending"},
		"recovered":  {Command: &protocol.Command{OperationID: "recovered", Kind: "storage-zfs-provision"}, Status: "needs_attention", Error: "member was offline", Acknowledged: true},
	}}}
	before := s.State.Journal["foreground"]
	completion := managedCompletion{result: protocol.Result{OperationID: "recovered", Status: "succeeded", ObservedAt: time.Now().UTC()}, background: true}
	active, err := completeManagedCommand(s, "foreground", completion, nil)
	if err != nil || active != "foreground" || s.State.Journal["foreground"] != before {
		t.Fatalf("background recovery changed foreground: active=%q error=%v entry=%+v", active, err, s.State.Journal["foreground"])
	}
	var persisted State
	data, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil || json.Unmarshal(data, &persisted) != nil {
		t.Fatalf("completion was not persisted: %v", err)
	}
	entry := persisted.Journal["recovered"]
	if entry.Status != "succeeded" || entry.Error != "" || entry.Acknowledged || persisted.Journal["foreground"].Status != "pending" {
		t.Fatalf("incorrect durable completion: %+v", persisted.Journal)
	}
	entry.Acknowledged = true
	s.State.Journal["recovered"] = entry
	s.beforePublish = func() error { return errors.New("duplicate completion must not rewrite state") }
	active, err = completeManagedCommand(s, active, completion, nil)
	if err != nil || active != "foreground" || !s.State.Journal["recovered"].Acknowledged {
		t.Fatalf("settled recovery was republished: active=%q err=%v", active, err)
	}
	s.beforePublish = nil
	active, err = completeManagedCommand(s, active, managedCompletion{result: protocol.Result{OperationID: "foreground", Status: "succeeded", ObservedAt: time.Now().UTC()}}, nil)
	if err != nil || active != "" || s.State.Journal["foreground"].Status != "succeeded" {
		t.Fatalf("foreground completion was lost: active=%q err=%v", active, err)
	}
}

func TestManagedWorkerAcceptsRecoveryReplayNeedsAttention(t *testing.T) {
	s := &Store{dir: t.TempDir(), State: State{Journal: map[string]JournalEntry{
		"snapshot-op": {Command: &protocol.Command{OperationID: "snapshot-op", Kind: "vm-snapshot-create"}, Status: "pending"},
	}}}
	done := managedCompletion{result: protocol.Result{
		OperationID: "snapshot-op", Status: "needs_attention", Error: "snapshot recovery reservation is unresolved",
		ObservedAt: time.Now().UTC(),
	}}
	active, err := completeManagedCommand(s, "snapshot-op", done, nil)
	if err != nil || active != "" {
		t.Fatalf("needs_attention replay was rejected: active=%q err=%v", active, err)
	}
	entry := s.State.Journal["snapshot-op"]
	if entry.Status != "needs_attention" || entry.Error != done.result.Error {
		t.Fatalf("recovery replay result was not preserved: %+v", entry)
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
			transientStorageDiscoveries.append(s, []protocol.StorageDiscoveryResult{{RequestID: fmt.Sprintf("discover-%d", i)}})
		}(i)
	}
	wg.Wait()
	got := transientStorageDiscoveries.drain(s)
	if len(got) != 2 {
		t.Fatalf("discovery results = %+v", got)
	}
	if more := transientStorageDiscoveries.drain(s); len(more) != 0 {
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
	// Replace the shell so cancellation kills the blocking process itself, without
	// an orphan child holding stdout open; it cannot finish during the run budget.
	script := "#!/bin/sh\ncase \"$*\" in\n *net-list*) : > \"$NEX_TEST_STARTED\"; exec sleep 30 ;;\n *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("NEX_TEST_STARTED", started)
	// The budget only has to outlast a contended runner getting the command started;
	// the run ends on the third heartbeat seen during it, not on this deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var reports, whileRunning atomic.Int32
	var cancelledAt atomic.Int64
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
			for _, result := range req.Results {
				if result.OperationID == "slow-network" {
					t.Errorf("slow command completed before cancellation: %+v", result)
				}
			}
			if whileRunning.Add(1) >= 3 {
				cancelledAt.CompareAndSwap(0, time.Now().UnixNano())
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
	if err := Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: time.Second, ProbeTimeout: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	// Separate failures: a command that never ran says nothing about the heartbeat.
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("the slow command never started: %v", err)
	}
	if whileRunning.Load() < 3 {
		t.Fatalf("heartbeat blocked during command: %d reports", whileRunning.Load())
	}
	// Run joins the worker and native probes. Allow scheduler/probe cleanup, but
	// return well before the uncancelled 20-second virsh deadline or 30-second sleep.
	if since := time.Since(time.Unix(0, cancelledAt.Load())); since > 10*time.Second {
		t.Fatalf("agent did not stop after cancellation: %v", since)
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
	// Mark the file before sleeping so a cancelled ProbeTimeout still proves virsh started.
	// Match both full "$*" and $3=list forms used by managedVirsh (--connect URI list ...).
	script := "#!/bin/sh\n: > \"$NEX_TEST_VIRSH_STARTED\"\ncase \"$*\" in\n *'list --all --uuid'*|*' list --all --uuid'*) exec sleep 30 ;;\n esac\ncase \"$3\" in list) exec sleep 30 ;; esac\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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

	// 500ms still far below the blocking sleep, with headroom for -race CI load so virsh
	// is actually started before the deadline cancels the child (BUG-0086 regression).
	start := time.Now()
	if err = report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: 500 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	// Well under the 30-second virsh: the claim is that report does not wait for the
	// observation, not that a loaded runner finishes the rest of a report quickly.
	if elapsed := time.Since(start); elapsed > 10*time.Second {
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

func TestMemberPeerBackupResultDoesNotBlockControllerReport(t *testing.T) {
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		for _, result := range req.Results {
			if result.OperationID == "peer-backup" {
				http.Error(w, "result operation does not belong to host", http.StatusBadRequest)
				return
			}
		}
		if req.Inventory.Managed != nil {
			for _, outcome := range req.Inventory.Managed.Outcomes {
				if outcome.OperationID == "peer-backup" {
					http.Error(w, "peer operation duplicated in outcomes", http.StatusBadRequest)
					return
				}
			}
		}
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	s.State.PoolID = "pool-1"
	if err := savePoolPeer(s, poolPeerState{Membership: protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1"}}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "peer-backup", Kind: "backup-now", VM: &protocol.VMCreateSpec{ID: "vm-1", HostID: identity.HostID}, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	s.State.Journal[cmd.OperationID] = JournalEntry{Status: "succeeded", Command: &cmd, ObservedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: "pool-1", VMID: "vm-1", HostID: identity.HostID, RepositoryID: "repo-1", RecoveryPointID: cmd.OperationID, IntentSHA256: strings.Repeat("a", 64), Stage: "published", Status: "succeeded", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if !s.State.Journal[cmd.OperationID].PeerDispatched {
		t.Fatal("legacy peer operation was not durably classified")
	}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatalf("peer result blocked the next heartbeat: %v", err)
	}
}

func TestFormerMasterKeepsControllerOwnedBackupResult(t *testing.T) {
	s := testStore(t)
	s.State.HostID, s.State.PoolID = "former-master", "pool-1"
	if err := savePoolPeer(s, poolPeerState{Membership: protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "new-master"}, Transfer: &poolTransferRecord{Role: "source"}}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "master-backup", Kind: "backup-now"}
	s.State.Journal[cmd.OperationID] = JournalEntry{Status: "succeeded", Command: &cmd}
	if err := savePoolBackupRun(s, protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: "pool-1", VMID: "vm-1", HostID: s.State.HostID, RepositoryID: "repo-1", RecoveryPointID: cmd.OperationID, IntentSHA256: strings.Repeat("a", 64), Stage: "published", Status: "succeeded", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := markDemotedMasterBackupJournal(s); err != nil {
		t.Fatal(err)
	}
	if s.State.Journal[cmd.OperationID].PeerDispatched {
		t.Fatal("former master's Controller result was reclassified as peer work")
	}
}

func TestPeerBackupAdmissionRetainsJournalForExactReplay(t *testing.T) {
	s := testStore(t)
	cmd := protocol.Command{OperationID: "peer-backup", Kind: "backup-now", VM: &protocol.VMCreateSpec{ID: "vm-1", HostID: "member-1"}, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	response := protocol.ReportResponse{Commands: []protocol.Command{cmd}}
	if err := applyResponseWithPeer(s, response, true); err != nil {
		t.Fatal(err)
	}
	if _, err := completeManagedCommand(s, cmd.OperationID, managedCompletion{result: protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()}, background: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := applyResponseWithPeer(s, response, true); err != nil {
		t.Fatalf("exact peer retry refused: %v", err)
	}
	entry := s.State.Journal[cmd.OperationID]
	if !entry.PeerDispatched || entry.Status != "succeeded" || len(commandOutcomes(s.State.Journal, nil)) != 0 {
		t.Fatalf("peer replay or projection lost origin: %+v", entry)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil || !state.Journal[cmd.OperationID].PeerDispatched {
		t.Fatalf("peer origin was not durable: %v", err)
	}
}

func TestSettledMemberRestoreDoesNotBlockNextLocalRestore(t *testing.T) {
	s := testStore(t)
	member := protocol.Command{OperationID: "restore-member", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{NewVMID: "vm-member"}}
	local := protocol.Command{OperationID: "restore-local", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{NewVMID: "vm-local"}}
	s.State.Journal = map[string]JournalEntry{
		member.OperationID: {Order: 1, Status: "pending", Command: &member},
		local.OperationID:  {Order: 2, Status: "pending", Command: &local},
	}
	if err := completeBackgroundManagedCommand(s, managedCompletion{result: protocol.Result{OperationID: member.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()}, background: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := nextManagedCommand(s.State.Journal, ""); got != local.OperationID {
		t.Fatalf("settled member restore kept the local restore queued: got %q", got)
	}
}

func TestAttentionRestoreReleasesGateButFencesSameTarget(t *testing.T) {
	s := testStore(t)
	uncertain := protocol.Command{OperationID: "restore-v3", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{NewVMID: "vm-v3"}}
	distinct := protocol.Command{OperationID: "restore-v4", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{NewVMID: "vm-v4"}}
	sameTarget := protocol.Command{OperationID: "restore-v3-again", Kind: protocol.BackupKindRestoreNew,
		Restore: &protocol.RestoreAsNewRequest{NewVMID: "vm-v3"}}
	s.State.Journal = map[string]JournalEntry{
		uncertain.OperationID:  {Order: 1, Status: "pending", Command: &uncertain},
		distinct.OperationID:   {Order: 2, Status: "pending", Command: &distinct},
		sameTarget.OperationID: {Order: 3, Status: "pending", Command: &sameTarget},
	}
	if err := completeBackgroundManagedCommand(s, managedCompletion{result: protocol.Result{OperationID: uncertain.OperationID, Status: "needs_attention", Error: "placement needs inspection", ObservedAt: time.Now().UTC()}, background: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := nextManagedCommand(s.State.Journal, ""); got != distinct.OperationID {
		t.Fatalf("attention on a different VM blocked restore: got %q", got)
	}
	s.State.Journal[distinct.OperationID] = JournalEntry{Order: 2, Status: "succeeded", Command: &distinct}
	if got := nextManagedCommand(s.State.Journal, ""); got != "" {
		t.Fatalf("attention on the same VM allowed a second restore: %q", got)
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

// The first bundled media seed copies and hashes an ~877 MB ISO; it must not run on the
// worker goroutine that receives Controller commands, and only one seed may run at a time.
func TestBundledMediaSeedRunsOffWorkerOneAtATime(t *testing.T) {
	var running atomic.Bool
	release, started := make(chan struct{}), make(chan struct{}, 2)
	seed := func() { started <- struct{}{}; <-release }
	returned := make(chan struct{})
	go func() { startBundledSeed(&running, seed); startBundledSeed(&running, seed); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("starting a bundled media seed blocked the worker")
	}
	<-started
	close(release)
	select {
	case <-started:
		t.Fatal("a second bundled media seed started while one was running")
	case <-time.After(50 * time.Millisecond):
	}
	for deadline := time.Now().Add(5 * time.Second); running.Load(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("finished seed did not clear its running flag")
		}
	}
}

func TestManagedPoolControlRecoversSameJournalOperationAfterReplicaFailure(t *testing.T) {
	lab := newUpdateLab(t, 3)
	ctx := context.Background()
	if err := lab.start("managed-control", "refuse"); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "managed-abort", Kind: "pool-update",
		Pool: &protocol.PoolCommand{RequestID: "managed-abort-request", Action: "update-control",
			Membership: protocol.PoolMembership{MasterHostID: "master-1"},
			Update:     &protocol.PoolUpdateRequest{RolloutID: "managed-control", Control: "abort"}}}
	s := lab.pool.master
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
		t.Fatal(err)
	}
	fail := true
	pushPoolAuthorityReplicasAfterUpdate = func(context.Context, *Store, time.Duration) error {
		if fail {
			return errors.New("replica unavailable")
		}
		return nil
	}
	result := ExecutePoolUpdate(ctx, s, cmd)
	if result.Status != "needs_attention" {
		t.Fatalf("accepted unresolved control became terminal: %+v", result)
	}
	if _, err := completeManagedCommand(s, cmd.OperationID, managedCompletion{result: result}, nil); err != nil {
		t.Fatal(err)
	}
	entry := s.State.Journal[cmd.OperationID]
	entry.Acknowledged, entry.ObservedAt = true, time.Time{}
	s.State.Journal[cmd.OperationID] = entry
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	var persisted State
	raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil || json.Unmarshal(raw, &persisted) != nil {
		t.Fatalf("unresolved command was not persisted: %v", err)
	}
	reopened := &Store{dir: s.dir, State: persisted}
	lab.pool.master, lab.c = reopened, NewPoolUpdateCoordinator(reopened)
	if next := nextManagedCommand(reopened.State.Journal, ""); next != cmd.OperationID {
		t.Fatalf("acknowledged unresolved operation cannot heal locally: %q", next)
	}
	fail = false
	if err = lab.c.Advance(ctx); err != nil || lab.rollout().State != protocol.PoolUpdateAborted {
		t.Fatalf("Core continuation required a Controller retry: %v", err)
	}
	result = ExecutePoolUpdate(ctx, reopened, cmd)
	if result.Status != "succeeded" {
		t.Fatalf("same command did not replay recovered outcome: %+v", result)
	}
	if _, err = completeManagedCommand(reopened, cmd.OperationID, managedCompletion{result: result}, nil); err != nil {
		t.Fatal(err)
	}
	entry = reopened.State.Journal[cmd.OperationID]
	if entry.Status != "succeeded" || entry.Acknowledged || nextManagedCommand(reopened.State.Journal, "") != "" {
		t.Fatalf("recovered operation was not published once as terminal: %+v", entry)
	}
}
