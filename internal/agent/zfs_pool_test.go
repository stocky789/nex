// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func zfsPoolCommand(hosts ...string) protocol.Command {
	selected := make([]protocol.ZFSProvisionHost, 0, len(hosts))
	for _, host := range hosts {
		selected = append(selected, protocol.ZFSProvisionHost{HostID: host, Layout: "single", Devices: []protocol.ZFSProvisionDevice{{ByID: "/dev/disk/by-id/" + host, Serial: host, SizeBytes: 1, Fingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}})
	}
	return protocol.Command{OperationID: "zfs-pool-op", Kind: "storage-zfs-provision", ZFSProvision: &protocol.ZFSProvisionRequest{PoolID: "zfs-pool", Name: "fast", Scope: "pool", Hosts: selected}}
}

func TestPoolZFSJournalRetriesExactSelectionAndSerializes(t *testing.T) {
	s := peerStore(t, "zfs-master", "zfs-pool")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	old := poolZFSExecuteLocal
	defer func() { poolZFSExecuteLocal = old }()
	var mu sync.Mutex
	calls, active, peak := 0, 0, 0
	poolZFSExecuteLocal = func(_ context.Context, _ *Store, _ string, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		mu.Lock()
		calls++
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return protocol.ZFSProvisionHostResult{RepositoryID: repositoryID, PoolID: request.PoolID, Name: request.Name, Scope: request.Scope, HostID: host.HostID, Status: "succeeded"}
	}
	cmd := zfsPoolCommand(s.State.HostID)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result, err := ExecutePoolZFSProvision(context.Background(), s, cmd); err != nil || result.Status != "succeeded" {
				t.Errorf("result: %+v %v", result, err)
			}
		}()
	}
	wg.Wait()
	if peak != 1 || calls != 1 {
		t.Fatalf("concurrent dispatch peak=%d calls=%d", peak, calls)
	}
	poolZFSMu.Lock()
	journal, err := loadPoolZFS(s)
	poolZFSMu.Unlock()
	if err != nil || journal.Operations[cmd.OperationID].RepositoryID != zfsRepositoryID(cmd.OperationID) || len(journal.Operations[cmd.OperationID].Outcomes) != 1 {
		t.Fatalf("journal: %#v %v", journal, err)
	}
	unknown := zfsPoolCommand(s.State.HostID)
	unknown.OperationID, unknown.ZFSProvision.RepositoryID = "zfs-pool-unknown", "zfs-not-owned"
	if result, err := ExecutePoolZFSProvision(context.Background(), s, unknown); err != nil || result.Status != "failed" || calls != 1 {
		t.Fatalf("unowned repository result: %+v %v calls=%d", result, err, calls)
	}
	configure := zfsPoolCommand("member-2")
	configure.OperationID, configure.ZFSProvision.RepositoryID = "zfs-configure", zfsRepositoryID(cmd.OperationID)
	configure.ZFSProvision.Name = "renamed"
	if result, err := ExecutePoolZFSProvision(context.Background(), s, configure); err != nil || result.Status != "failed" || calls != 1 {
		t.Fatalf("configure renamed durable repository: %+v %v calls=%d", result, err, calls)
	}
	configure.ZFSProvision.Name = cmd.ZFSProvision.Name
	configure.ZFSProvision.Hosts = cmd.ZFSProvision.Hosts
	if result, err := ExecutePoolZFSProvision(context.Background(), s, configure); err != nil || result.Status != "failed" || calls != 1 {
		t.Fatalf("configure replaced existing host: %+v %v calls=%d", result, err, calls)
	}
}

func TestPoolZFSMemberPeerRequiresMasterAndCanRetryAfterUnreachable(t *testing.T) {
	master, member, _, memberServer := cpuPeerPair(t)
	old := poolZFSExecuteLocal
	defer func() { poolZFSExecuteLocal = old }()
	calls := 0
	poolZFSExecuteLocal = func(_ context.Context, _ *Store, _ string, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		calls++
		return protocol.ZFSProvisionHostResult{RepositoryID: repositoryID, PoolID: request.PoolID, Name: request.Name, Scope: request.Scope, HostID: host.HostID, Status: "succeeded"}
	}
	cmd := zfsPoolCommand(member.State.HostID)
	cmd.ZFSProvision.PoolID = master.State.PoolID
	// A member with no advertised endpoint is retained as attention, never run locally.
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		for i := range state.Membership.Members {
			if state.Membership.Members[i].HostID == member.State.HostID {
				state.Membership.Members[i].PeerOrigin = ""
			}
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if result, err := ExecutePoolZFSProvision(context.Background(), master, cmd); err != nil || result.Status != "needs_attention" || calls != 0 {
		t.Fatalf("unreachable result: %+v %v calls=%d", result, err, calls)
	}
	if err = AdvertisePoolPeerOrigin(context.Background(), member, memberServer.URL); err != nil {
		t.Fatal(err)
	}
	if result, err := ExecutePoolZFSProvision(context.Background(), master, cmd); err != nil || result.Status != "succeeded" || calls != 1 {
		t.Fatalf("retry result: %+v %v calls=%d", result, err, calls)
	}
	// The member endpoint is mTLS master-only.
	anonymous := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	defer anonymous.CloseIdleConnections()
	body, _ := json.Marshal(zfsPoolPeerRequest{OperationID: cmd.OperationID, RepositoryID: zfsRepositoryID(cmd.OperationID), Request: protocol.ZFSProvisionRequest{RepositoryID: zfsRepositoryID(cmd.OperationID), PoolID: cmd.ZFSProvision.PoolID, Name: cmd.ZFSProvision.Name, Scope: cmd.ZFSProvision.Scope, Hosts: cmd.ZFSProvision.Hosts}, Host: cmd.ZFSProvision.Hosts[0]})
	req, _ := http.NewRequest(http.MethodPost, memberServer.URL+poolZFSPath, bytes.NewReader(body))
	response, err := anonymous.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous peer status=%d", response.StatusCode)
	}
}

func TestPoolZFSProjectionMergesConfigureHostOperations(t *testing.T) {
	s := peerStore(t, "zfs-master", "zfs-pool")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	repositoryID := "zfs-owned"
	device := func(host string) protocol.ZFSProvisionHost {
		return protocol.ZFSProvisionHost{HostID: host, Layout: "single", Devices: []protocol.ZFSProvisionDevice{{ByID: "/dev/disk/by-id/" + host, Serial: host, SizeBytes: 1, Fingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}}
	}
	result := func(host string) protocol.ZFSProvisionHostResult {
		return protocol.ZFSProvisionHostResult{RepositoryID: repositoryID, PoolID: s.State.PoolID, Name: "fast", Scope: "pool", HostID: host, Status: "succeeded"}
	}
	state := zfsPoolState{Operations: map[string]zfsPoolOperation{
		"first":  {RepositoryID: repositoryID, Request: protocol.ZFSProvisionRequest{RepositoryID: repositoryID, PoolID: s.State.PoolID, Name: "fast", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{device(s.State.HostID)}}, Outcomes: map[string]protocol.ZFSProvisionHostResult{s.State.HostID: result(s.State.HostID)}},
		"second": {RepositoryID: repositoryID, Request: protocol.ZFSProvisionRequest{RepositoryID: repositoryID, PoolID: s.State.PoolID, Name: "fast", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{device("member-2")}}, Outcomes: map[string]protocol.ZFSProvisionHostResult{"member-2": result("member-2")}},
	}}
	if err := savePoolZFS(s, state); err != nil {
		t.Fatal(err)
	}
	// The projection must publish the durable lifecycle generation, not a default.
	if err := os.WriteFile(zfsLifecycleStateFile(s), []byte(`{"generations":{"`+repositoryID+`":3}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if inventory := PoolZFSInventory(s); len(inventory) != 2 || inventory[0].HostID != "member-2" || inventory[1].HostID != s.State.HostID {
		t.Fatalf("inventory lost or duplicated configured hosts: %#v", inventory)
	}
	for range 3 {
		projected := PoolZFSStorageProjection(context.Background(), s, nil)
		if len(projected) != 1 || len(projected[0].ZFSHostAccess) != 2 || !projected[0].ZFSProvisioned || projected[0].Generation != 3 {
			t.Fatalf("projection lost configured hosts: %#v", projected)
		}
		// An unobservable host reports its error, never a borrowed mount path.
		for _, access := range projected[0].ZFSHostAccess {
			if access.Error == "" || access.Path != "" {
				t.Fatalf("unobservable host access = %#v", access)
			}
		}
	}
}

// TestPoolZFSAccessProbeFencesMembersAndReleasesConnections covers the member
// /access endpoint: it is master-only, answers 409 for a repository this host
// does not hold, and the master's probe must not leave the peer connection
// open. A member that is not a ready current member is never probed at all.
func TestPoolZFSAccessProbeFencesMembersAndReleasesConnections(t *testing.T) {
	master, member, _, _ := cpuPeerPair(t)
	// The member must hold the repository, so the probe reads a real response
	// body: an unread body closes its connection regardless.
	dir := filepath.Join(member.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	registration := vmhost.ZFSProvisionStorage("zfs-1", member.State.PoolID, member.State.HostID, "fast")
	registration.Generation, registration.Path = 1, registration.Source.ZFS.MountPath
	registration.Identity.ZFS = &protocol.ZFSStorageIdentity{PoolGUID: "pool-guid", DatasetGUID: "dataset-guid"}
	registration.Access = protocol.StorageAccess{Readable: true, Writable: true}
	request := registration
	request.Identity, request.Path, request.Access = protocol.StorageIdentity{}, "", protocol.StorageAccess{}
	b, err := json.Marshal(struct {
		Command        protocol.Command              `json:"command"`
		Result         protocol.Result               `json:"result"`
		Stage          string                        `json:"stage"`
		AppliedStorage *protocol.StorageRegistration `json:"applied_storage,omitempty"`
	}{Command: protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &request}, Result: protocol.Result{OperationID: "register-1", Status: "succeeded"}, Stage: "verified", AppliedStorage: &registration})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "register-1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	open, probes := 0, 0
	peer := PoolPeerHandler(member)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		probes++
		mu.Unlock()
		peer.ServeHTTP(w, r)
	}))
	tlsConfig, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = tlsConfig
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		switch state {
		case http.StateNew:
			open++
		case http.StateClosed, http.StateHijacked:
			open--
		}
	}
	server.StartTLS()
	defer server.Close()
	if err := AdvertisePoolPeerOrigin(context.Background(), member, server.URL); err != nil {
		t.Fatal(err)
	}
	access := observePoolZFSAccess(context.Background(), master, member.State.HostID, registration.ID, nil)
	if access.HostID != member.State.HostID || access.Error != "" || access.PoolGUID != "pool-guid" || !access.Writable {
		t.Fatalf("member repository access = %#v", access)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		remaining := open
		mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe left %d peer connection(s) open", remaining)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// With Run's monitor present the member answers with its live observation, never the
	// access frozen in the journal at registration: unobserved means unknown, not writable.
	member.storageMonitor.Store(vmhost.NewStorageMonitor(time.Second))
	if live := observePoolZFSAccess(context.Background(), master, member.State.HostID, registration.ID, nil); live.Writable || live.Error == "" {
		t.Fatalf("member served journalled access instead of its live observation: %#v", live)
	}
	member.storageMonitor.Store(nil)
	// The endpoint itself is mTLS master-only.
	anonymous := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	defer anonymous.CloseIdleConnections()
	response, err := anonymous.Get(server.URL + poolZFSPath + "/zfs-absent/access")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous access status = %d", response.StatusCode)
	}
	client, err := PoolMemberPeerClient(master, server.URL, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	response, err = client.Get(server.URL + poolZFSPath + "/zfs-absent/access")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("absent repository status = %d", response.StatusCode)
	}
	// One projection pass probes each configured host once, not once per check.
	if err := savePoolZFS(master, zfsPoolState{Operations: map[string]zfsPoolOperation{"provision-1": {
		RepositoryID: registration.ID,
		Request:      protocol.ZFSProvisionRequest{RepositoryID: registration.ID, PoolID: master.State.PoolID, Name: "fast", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{{HostID: member.State.HostID}}},
		Outcomes:     map[string]protocol.ZFSProvisionHostResult{member.State.HostID: {RepositoryID: registration.ID, HostID: member.State.HostID, Status: "needs_attention"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	probes = 0
	mu.Unlock()
	if projected := PoolZFSStorageProjection(context.Background(), master, nil); len(projected) != 1 || projected[0].ID != registration.ID {
		t.Fatalf("member-observed projection = %#v", projected)
	}
	mu.Lock()
	passProbes := probes
	mu.Unlock()
	if passProbes != 1 {
		t.Fatalf("projection pass probed the member %d times", passProbes)
	}
	// A member that is no longer a ready current member is not an authority on
	// this repository, so it must not be probed at all.
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		for i := range state.Membership.Members {
			if state.Membership.Members[i].HostID == member.State.HostID {
				state.Membership.Members[i].Status = "ejected"
			}
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if access := observePoolZFSAccess(context.Background(), master, member.State.HostID, "zfs-absent", nil); access.Error != "member peer endpoint is unavailable" {
		t.Fatalf("ejected member access = %#v", access)
	}
}

func TestResumePoolZFSOperationsCompletesDurableAttentionWithoutControllerReplay(t *testing.T) {
	s := peerStore(t, "zfs-master", "zfs-pool")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	cmd := zfsPoolCommand(s.State.HostID)
	cmd.ZFSProvision.RepositoryID = "zfs-resume"
	operation := zfsPoolOperation{Request: *cmd.ZFSProvision, RepositoryID: "zfs-resume", Outcomes: map[string]protocol.ZFSProvisionHostResult{s.State.HostID: {RepositoryID: "zfs-resume", HostID: s.State.HostID, Status: "needs_attention", Error: "registration unavailable"}}}
	if err := savePoolZFS(s, zfsPoolState{Operations: map[string]zfsPoolOperation{cmd.OperationID: operation}}); err != nil {
		t.Fatal(err)
	}
	s.State.Journal[cmd.OperationID] = JournalEntry{Command: &cmd, Status: "needs_attention"}
	old := poolZFSExecuteLocal
	defer func() { poolZFSExecuteLocal = old }()
	oldReceipt := poolZFSSucceededReceipt
	defer func() { poolZFSSucceededReceipt = oldReceipt }()
	poolZFSSucceededReceipt = func(context.Context, *Store, string, string, protocol.ZFSProvisionRequest, protocol.ZFSProvisionHost) bool {
		return true
	}
	calls := 0
	poolZFSExecuteLocal = func(_ context.Context, _ *Store, _ string, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		calls++
		return protocol.ZFSProvisionHostResult{RepositoryID: repositoryID, PoolID: request.PoolID, Name: request.Name, Scope: request.Scope, HostID: host.HostID, Status: "succeeded"}
	}
	resumed, err := ResumePoolZFSOperations(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(resumed) != 1 || resumed[0].Status != "succeeded" {
		t.Fatalf("Core did not resume durable ZFS operation: calls=%d results=%#v", calls, resumed)
	}
}

func TestPoolZFSProjectionRequiresSucceededHostAndSupportsMemberOnlyRepository(t *testing.T) {
	s := peerStore(t, "zfs-master", "zfs-pool")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	request := protocol.ZFSProvisionRequest{RepositoryID: "member-only", PoolID: s.State.PoolID, Name: "fast", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{{HostID: "member-1"}}}
	failed := zfsPoolOperation{Request: request, RepositoryID: "failed-intent", Outcomes: map[string]protocol.ZFSProvisionHostResult{s.State.HostID: {RepositoryID: "failed-intent", HostID: s.State.HostID, Status: "needs_attention"}}}
	succeeded := zfsPoolOperation{Request: request, RepositoryID: "member-only", Outcomes: map[string]protocol.ZFSProvisionHostResult{"member-1": {RepositoryID: "member-only", PoolID: s.State.PoolID, Name: "fast", Scope: "pool", HostID: "member-1", Status: "succeeded"}}}
	if err := savePoolZFS(s, zfsPoolState{Operations: map[string]zfsPoolOperation{"failed": failed, "member": succeeded}}); err != nil {
		t.Fatal(err)
	}
	projected := PoolZFSStorageProjection(context.Background(), s, nil)
	if len(projected) != 1 || projected[0].ID != "member-only" || projected[0].Status != "unknown" || projected[0].Capabilities.ISO || len(projected[0].ZFSHostAccess) != 1 || projected[0].ZFSHostAccess[0].HostID != "member-1" {
		t.Fatalf("member-only projection = %#v", projected)
	}
}

func TestPoolZFSProjectionPublishesCompletedDetachTombstoneWithoutMasterDataset(t *testing.T) {
	s := peerStore(t, "zfs-master", "zfs-pool")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	lifecycle := zfsLifecycleState{Generations: map[string]uint64{}, Operations: map[string]zfsLifecycleOperationCompletion{
		"detach-1": {RepositoryID: "member-only", PoolID: s.State.PoolID, HostID: s.State.HostID, Generation: 3, DetachReady: true, Completed: true, Outcomes: map[string]protocol.Result{"member-1": {OperationID: "detach-1", Status: "succeeded"}}},
	}}
	b, err := json.Marshal(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zfsLifecycleStateFile(s), b, 0600); err != nil {
		t.Fatal(err)
	}
	projected := PoolZFSStorageProjection(context.Background(), s, nil)
	if len(projected) != 1 || projected[0].ID != "member-only" || !projected[0].Tombstone || !projected[0].ZFSProvisioned || projected[0].Generation != 3 {
		t.Fatalf("completed pool detach tombstone = %#v", projected)
	}
	newer := protocol.StorageRegistration{ID: "member-only", PoolID: s.State.PoolID, HostID: s.State.HostID, Name: "replacement", Kind: protocol.StorageKindZFS, Generation: 4, Path: "/var/lib/nex-storage/replacement"}
	projected = PoolZFSStorageProjection(context.Background(), s, []protocol.StorageRegistration{newer})
	if len(projected) != 1 || projected[0].Tombstone || projected[0].Generation != 4 {
		t.Fatalf("completed detach hid newer registration: %#v", projected)
	}
}

func TestResumePoolZFSOperationsDoesNotRetryUnprovenFailedCreate(t *testing.T) {
	s := peerStore(t, "zfs-master", "zfs-pool")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	cmd := zfsPoolCommand(s.State.HostID)
	cmd.ZFSProvision.RepositoryID = "failed-create"
	if err := savePoolZFS(s, zfsPoolState{Operations: map[string]zfsPoolOperation{cmd.OperationID: {Request: *cmd.ZFSProvision, RepositoryID: "failed-create", Outcomes: map[string]protocol.ZFSProvisionHostResult{s.State.HostID: {RepositoryID: "failed-create", HostID: s.State.HostID, Status: "needs_attention"}}}}}); err != nil {
		t.Fatal(err)
	}
	oldReceipt, oldExecute := poolZFSSucceededReceipt, poolZFSExecuteLocal
	defer func() { poolZFSSucceededReceipt, poolZFSExecuteLocal = oldReceipt, oldExecute }()
	poolZFSSucceededReceipt = func(context.Context, *Store, string, string, protocol.ZFSProvisionRequest, protocol.ZFSProvisionHost) bool {
		return false
	}
	calls := 0
	poolZFSExecuteLocal = func(context.Context, *Store, string, string, protocol.ZFSProvisionRequest, protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		calls++
		return protocol.ZFSProvisionHostResult{}
	}
	resumed, err := ResumePoolZFSOperations(context.Background(), s)
	if err != nil || calls != 0 || len(resumed) != 0 {
		t.Fatalf("unproven failed create resumed: calls=%d results=%#v err=%v", calls, resumed, err)
	}
}

func TestResumePoolZFSOperationsRepublishesCompletedAggregateWithoutDispatch(t *testing.T) {
	s := peerStore(t, "zfs-master", "zfs-pool")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	cmd := zfsPoolCommand(s.State.HostID)
	if err := savePoolZFS(s, zfsPoolState{Operations: map[string]zfsPoolOperation{cmd.OperationID: {Request: *cmd.ZFSProvision, RepositoryID: "zfs-complete", Outcomes: map[string]protocol.ZFSProvisionHostResult{s.State.HostID: {RepositoryID: "zfs-complete", HostID: s.State.HostID, Status: "succeeded"}}}}}); err != nil {
		t.Fatal(err)
	}
	s.State.Journal[cmd.OperationID] = JournalEntry{Command: &cmd, Status: "needs_attention", Acknowledged: true}
	old := poolZFSExecuteLocal
	defer func() { poolZFSExecuteLocal = old }()
	poolZFSExecuteLocal = func(context.Context, *Store, string, string, protocol.ZFSProvisionRequest, protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		t.Fatal("completed aggregate dispatched")
		return protocol.ZFSProvisionHostResult{}
	}
	results, err := ResumePoolZFSOperations(context.Background(), s)
	if err != nil || len(results) != 1 || results[0].OperationID != cmd.OperationID || results[0].Status != "succeeded" {
		t.Fatalf("completed aggregate result = %#v, %v", results, err)
	}
}
