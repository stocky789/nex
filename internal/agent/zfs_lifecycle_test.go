// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// TestZFSLifecycleLocalTranslatesAggregateGenerationAndReplaysLostDetachReply
// exercises the member handler against the real managed journal.  A master
// rename advances its aggregate generation to two while this member's local
// registration is still generation one.  A detach reply may be lost after the
// local tombstone is durable; retrying the aggregate command must use that
// exact local receipt, rather than treating an absent registration as proof.
func TestZFSLifecycleLocalTranslatesAggregateGenerationAndReplaysLostDetachReply(t *testing.T) {
	s := peerStore(t, "member-1", "pool-1")
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	registration := vmhost.ZFSProvisionStorage("zfs-1", s.State.PoolID, s.State.HostID, "fast")
	registration.Generation = 1
	journal := struct {
		Command        protocol.Command              `json:"command"`
		Result         protocol.Result               `json:"result"`
		Stage          string                        `json:"stage"`
		AppliedStorage *protocol.StorageRegistration `json:"applied_storage,omitempty"`
	}{
		Command:        protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &registration},
		Result:         protocol.Result{OperationID: "register-1", Status: "succeeded"},
		Stage:          "verified",
		AppliedStorage: &registration,
	}
	b, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "register-1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	aggregate := protocol.Command{OperationID: "detach-1", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{
		Action: "detach", RepositoryID: registration.ID, PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: 2,
	}}
	if result, err := executeZFSLifecycleLocal(context.Background(), s, "execute", aggregate); err != nil || result.Status != "succeeded" {
		t.Fatalf("translated local detach = %#v, %v", result, err)
	}
	if aggregate.StorageLifecycle.ExpectedGeneration != 2 {
		t.Fatalf("aggregate command mutated to generation %d", aggregate.StorageLifecycle.ExpectedGeneration)
	}
	// A lost peer response leaves the master aggregate intact.  The local
	// tombstone carries generation two, which maps back to the original local
	// generation one so ManagedEngine returns its exact durable receipt.
	if result, err := executeZFSLifecycleLocal(context.Background(), s, "execute", aggregate); err != nil || result.Status != "succeeded" {
		t.Fatalf("lost-reply detach replay = %#v, %v", result, err)
	}
	var detached struct {
		Command protocol.Command `json:"command"`
	}
	b, err = os.ReadFile(filepath.Join(dir, "detach-1.json"))
	if err != nil || json.Unmarshal(b, &detached) != nil || detached.Command.StorageLifecycle == nil || detached.Command.StorageLifecycle.ExpectedGeneration != 1 {
		t.Fatalf("local durable detach command = %s, err=%v", b, err)
	}
}

func TestPoolZFSLifecycleRejectsStaleMasterGenerationBeforeMemberDispatch(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	op := zfsPoolOperation{Request: protocol.ZFSProvisionRequest{RepositoryID: "zfs-1", PoolID: s.State.PoolID, Name: "fast", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{{HostID: s.State.HostID}}}, RepositoryID: "zfs-1", Outcomes: map[string]protocol.ZFSProvisionHostResult{s.State.HostID: {RepositoryID: "zfs-1", HostID: s.State.HostID, Status: "succeeded"}}}
	poolZFSMu.Lock()
	err := savePoolZFS(s, zfsPoolState{Operations: map[string]zfsPoolOperation{"provision-1": op}})
	poolZFSMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zfsLifecycleStateFile(s), []byte(`{"generations":{"zfs-1":2}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "detach-stale", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "zfs-1", PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: 1}}
	old := zfsLifecycleDispatch
	defer func() { zfsLifecycleDispatch = old }()
	zfsLifecycleDispatch = func(context.Context, *Store, protocol.PoolMembership, string, string, protocol.Command) (protocol.Result, error) {
		t.Fatal("stale master generation dispatched to a member")
		return protocol.Result{}, nil
	}
	if result, handled, err := ExecutePoolZFSLifecycle(context.Background(), s, cmd); !handled || err != nil || result.Status != "failed" {
		t.Fatalf("stale detach = %#v handled=%t err=%v", result, handled, err)
	}
}

func TestZFSLifecycleFailureDoesNotRewriteProvisionInventory(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	op := zfsPoolOperation{Request: protocol.ZFSProvisionRequest{RepositoryID: "zfs-1", PoolID: s.State.PoolID, Name: "fast", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{{HostID: s.State.HostID}}}, RepositoryID: "zfs-1", Outcomes: map[string]protocol.ZFSProvisionHostResult{s.State.HostID: {RepositoryID: "zfs-1", HostID: s.State.HostID, Status: "succeeded"}}}
	poolZFSMu.Lock()
	err := savePoolZFS(s, zfsPoolState{Operations: map[string]zfsPoolOperation{"provision-1": op}})
	poolZFSMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	failed := protocol.Result{OperationID: "detach-1", Status: "needs_attention", Error: "member timed out"}
	if err := persistZFSLifecycleOutcome(s, "detach-1", "zfs-1", s.State.HostID, failed); err != nil {
		t.Fatal(err)
	}
	poolZFSMu.Lock()
	state, err := loadPoolZFS(s)
	poolZFSMu.Unlock()
	if err != nil || state.Operations["provision-1"].Outcomes[s.State.HostID].Status != "succeeded" {
		t.Fatalf("lifecycle failure rewrote provision outcome: %#v, %v", state, err)
	}
	lifecycle, err := loadZFSLifecycleState(s)
	if err != nil || lifecycle.Operations["detach-1"].Outcomes[s.State.HostID].Status != "needs_attention" {
		t.Fatalf("durable lifecycle outcome = %#v, %v", lifecycle, err)
	}
}

// TestZFSLifecycleDispatchFailureKeepsCauseAndDoesNotPoisonJournal drives the
// real dispatcher against a configured host that is not a reachable member. The
// transport failure returns a bare Result, which must neither replace the real
// cause with a generic command failure nor be journalled under an empty
// operation ID, where it would fence every later repository out of the journal.
func TestZFSLifecycleDispatchFailureKeepsCauseAndDoesNotPoisonJournal(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	operations := map[string]zfsPoolOperation{}
	for _, repositoryID := range []string{"zfs-a", "zfs-b"} {
		operations["provision-"+repositoryID] = zfsPoolOperation{
			Request:      protocol.ZFSProvisionRequest{RepositoryID: repositoryID, PoolID: s.State.PoolID, Name: "fast", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{{HostID: "ghost-1"}}},
			RepositoryID: repositoryID,
			Outcomes:     map[string]protocol.ZFSProvisionHostResult{"ghost-1": {RepositoryID: repositoryID, HostID: "ghost-1", Status: "succeeded"}},
		}
	}
	poolZFSMu.Lock()
	err := savePoolZFS(s, zfsPoolState{Operations: operations})
	poolZFSMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, repositoryID := range []string{"zfs-a", "zfs-b"} {
		cmd := protocol.Command{OperationID: "recheck-" + repositoryID, Kind: "storage-recheck", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: repositoryID, PoolID: s.State.PoolID, HostID: "ghost-1"}}
		result, handled, err := ExecutePoolZFSLifecycle(context.Background(), s, cmd)
		if !handled || err == nil || result.Status != "needs_attention" || result.Error != "configured ZFS host is unreachable" {
			t.Fatalf("%s recheck = %#v handled=%t err=%v", repositoryID, result, handled, err)
		}
		lifecycle, loadErr := loadZFSLifecycleState(s)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if _, poisoned := lifecycle.Operations[""]; poisoned {
			t.Fatalf("dispatch failure journalled under an empty operation ID: %#v", lifecycle.Operations)
		}
		if lifecycle.Operations[cmd.OperationID].RepositoryID != repositoryID || lifecycle.Operations[cmd.OperationID].Outcomes["ghost-1"].Status != "needs_attention" {
			t.Fatalf("%s durable lifecycle outcome = %#v", repositoryID, lifecycle.Operations)
		}
	}
}

// TestZFSLifecycleLocalCompletesDetachFromDurableTombstone covers a detach that
// failed on another host: the retry carries a new operation ID, so there is no
// receipt to replay, and only this host's durable tombstone proves it already
// unregistered the repository.
func TestZFSLifecycleLocalCompletesDetachFromDurableTombstone(t *testing.T) {
	s := peerStore(t, "member-1", "pool-1")
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	registration := vmhost.ZFSProvisionStorage("zfs-1", s.State.PoolID, s.State.HostID, "fast")
	registration.Generation = 1
	journal := struct {
		Command        protocol.Command              `json:"command"`
		Result         protocol.Result               `json:"result"`
		Stage          string                        `json:"stage"`
		AppliedStorage *protocol.StorageRegistration `json:"applied_storage,omitempty"`
	}{
		Command:        protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &registration},
		Result:         protocol.Result{OperationID: "register-1", Status: "succeeded"},
		Stage:          "verified",
		AppliedStorage: &registration,
	}
	b, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "register-1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	detach := protocol.Command{OperationID: "detach-1", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: registration.ID, PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: 1}}
	if result, err := executeZFSLifecycleLocal(context.Background(), s, "execute", detach); err != nil || result.Status != "succeeded" {
		t.Fatalf("local detach = %#v, %v", result, err)
	}
	retry := detach
	lifecycle := *detach.StorageLifecycle
	retry.OperationID, lifecycle.ExpectedGeneration, retry.StorageLifecycle = "detach-2", 2, &lifecycle
	for _, phase := range []string{"preflight", "execute"} {
		if result, err := executeZFSLifecycleLocal(context.Background(), s, phase, retry); err != nil || result.Status != "succeeded" {
			t.Fatalf("%s retry under a new operation ID = %#v, %v", phase, result, err)
		}
	}
	// Absence alone still proves nothing: another repository must fail closed.
	other := retry
	unknown := lifecycle
	unknown.RepositoryID = "zfs-absent"
	other.StorageLifecycle = &unknown
	if result, err := executeZFSLifecycleLocal(context.Background(), s, "execute", other); err == nil || result.Status == "succeeded" {
		t.Fatalf("unknown repository detach = %#v, %v", result, err)
	}
}

func TestPoolZFSLifecycleReplaysLostMemberRepliesAndAggregateRemovalCrash(t *testing.T) {
	master, member, _, _ := cpuPeerPair(t)
	writeRegistration := func(s *Store) {
		t.Helper()
		dir := filepath.Join(s.dir, "managed-vms")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		registration := vmhost.ZFSProvisionStorage("zfs-1", s.State.PoolID, s.State.HostID, "fast")
		registration.Generation = 1
		journal := struct {
			Command        protocol.Command              `json:"command"`
			Result         protocol.Result               `json:"result"`
			Stage          string                        `json:"stage"`
			AppliedStorage *protocol.StorageRegistration `json:"applied_storage,omitempty"`
		}{Command: protocol.Command{OperationID: "register-" + s.State.HostID, Kind: "storage-register", Storage: &registration}, Result: protocol.Result{OperationID: "register-" + s.State.HostID, Status: "succeeded"}, Stage: "verified", AppliedStorage: &registration}
		b, err := json.Marshal(journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, journal.Command.OperationID+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeRegistration(master)
	writeRegistration(member)
	for _, s := range []*Store{master, member} {
		if managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(context.Background()); len(managed.Storage) != 1 {
			t.Fatalf("%s local registration not projected: %#v checks=%#v", s.State.HostID, managed.Storage, checks)
		}
	}
	op := zfsPoolOperation{Request: protocol.ZFSProvisionRequest{RepositoryID: "zfs-1", PoolID: master.State.PoolID, Name: "fast", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{{HostID: master.State.HostID}, {HostID: member.State.HostID}}}, RepositoryID: "zfs-1", Outcomes: map[string]protocol.ZFSProvisionHostResult{
		master.State.HostID: {RepositoryID: "zfs-1", HostID: master.State.HostID, Status: "succeeded"},
		member.State.HostID: {RepositoryID: "zfs-1", HostID: member.State.HostID, Status: "succeeded"},
	}}
	if err := savePoolZFS(master, zfsPoolState{Operations: map[string]zfsPoolOperation{"provision-1": op}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zfsLifecycleStateFile(master), []byte(`{"generations":{"zfs-1":2}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "detach-1", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "zfs-1", PoolID: master.State.PoolID, HostID: master.State.HostID, ExpectedGeneration: 2}}
	oldDispatch, oldRemove := zfsLifecycleDispatch, removePoolZFSAggregate
	t.Cleanup(func() { zfsLifecycleDispatch, removePoolZFSAggregate = oldDispatch, oldRemove })
	zfsLifecycleDispatch = func(ctx context.Context, _ *Store, _ protocol.PoolMembership, hostID, phase string, command protocol.Command) (protocol.Result, error) {
		if hostID == master.State.HostID {
			return executeZFSLifecycleLocal(ctx, master, phase, command)
		}
		return executeZFSLifecycleLocal(ctx, member, phase, command)
	}
	removePoolZFSAggregate = func(*Store, string) error { return errors.New("simulated crash before aggregate removal") }
	if result, handled, err := ExecutePoolZFSLifecycle(context.Background(), master, cmd); !handled || err == nil || result.Status != "needs_attention" {
		t.Fatalf("interrupted detach = %#v handled=%t err=%v", result, handled, err)
	}
	removePoolZFSAggregate = func(s *Store, repositoryID string) error {
		if err := removePoolZFS(s, repositoryID); err != nil {
			return err
		}
		return errors.New("simulated crash after aggregate removal")
	}
	if result, handled, err := ExecutePoolZFSLifecycle(context.Background(), master, cmd); !handled || err == nil || result.Status != "needs_attention" {
		t.Fatalf("post-removal crash = %#v handled=%t err=%v", result, handled, err)
	}
	removePoolZFSAggregate = removePoolZFS
	// The aggregate is already gone, so this must close the exact operation
	// from its ready receipt without dispatching or touching disks again.
	if result, handled, err := ExecutePoolZFSLifecycle(context.Background(), master, cmd); !handled || err != nil || result.Status != "succeeded" {
		t.Fatalf("post-removal completion replay = %#v handled=%t err=%v", result, handled, err)
	}
}

func TestZFSLifecycleRepairsOnlyGUIDProvenHistoricalProvisionOutcome(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	registration := vmhost.ZFSProvisionStorage("zfs-1", s.State.PoolID, s.State.HostID, "fast")
	registration.Generation, registration.Path = 1, registration.Source.ZFS.MountPath
	registration.Identity.ZFS = &protocol.ZFSStorageIdentity{PoolGUID: "pool-guid", DatasetGUID: "dataset-guid"}
	registration.Access = protocol.StorageAccess{Readable: true, Writable: true}
	request := registration
	request.Identity, request.Path, request.Access = protocol.StorageIdentity{}, "", protocol.StorageAccess{}
	journal := struct {
		Command        protocol.Command              `json:"command"`
		Result         protocol.Result               `json:"result"`
		AppliedStorage *protocol.StorageRegistration `json:"applied_storage,omitempty"`
	}{Command: protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &request}, Result: protocol.Result{OperationID: "register-1", Status: "succeeded"}, AppliedStorage: &registration}
	b, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "register-1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	poisoned := protocol.ZFSProvisionHostResult{RepositoryID: "zfs-1", HostID: s.State.HostID, Status: "needs_attention", Error: "old lifecycle error", PoolGUID: "pool-guid", DatasetGUID: "dataset-guid"}
	op := zfsPoolOperation{RepositoryID: "zfs-1", Outcomes: map[string]protocol.ZFSProvisionHostResult{s.State.HostID: poisoned}}
	if err := savePoolZFS(s, zfsPoolState{Operations: map[string]zfsPoolOperation{"provision-1": op}}); err != nil {
		t.Fatal(err)
	}
	if !zfsLifecycleCurrent(context.Background(), s, op, []string{s.State.HostID}) {
		managed, checks := vmhost.NewManaged(dir).Applied(context.Background())
		t.Fatalf("matching current GUID registration did not repair historical provision outcome: storage=%#v checks=%#v", managed.Storage, checks)
	}
	poolZFSMu.Lock()
	state, err := loadPoolZFS(s)
	poolZFSMu.Unlock()
	if err != nil || state.Operations["provision-1"].Outcomes[s.State.HostID].Status != "succeeded" {
		t.Fatalf("repaired durable outcome = %#v, %v", state, err)
	}
	// Matching a repository ID alone is insufficient: a different native pool
	// GUID must leave the historical failure fenced.
	state.Operations["provision-1"] = op
	if err := savePoolZFS(s, state); err != nil {
		t.Fatal(err)
	}
	registration.Identity.ZFS.PoolGUID = "different-pool"
	journal.AppliedStorage = &registration
	b, err = json.Marshal(journal)
	if err != nil || os.WriteFile(filepath.Join(dir, "register-1.json"), b, 0600) != nil {
		t.Fatalf("rewrite mismatched registration: %v", err)
	}
	if zfsLifecycleCurrent(context.Background(), s, op, []string{s.State.HostID}) {
		t.Fatal("mismatched native GUID repaired historical provision outcome")
	}
}

func TestPoolZFSLifecycleBlocksUnavailableAndDependenciesWithoutDestroyingZpool(t *testing.T) {
	s, member, _, _ := cpuPeerPair(t)
	if membership, err := Membership(s); err != nil || membership.MasterHostID != s.State.HostID {
		t.Fatalf("membership = %#v, %v", membership, err)
	}
	op := zfsPoolOperation{Request: protocol.ZFSProvisionRequest{RepositoryID: "zfs-1", PoolID: s.State.PoolID, Name: "fast", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{{HostID: s.State.HostID}, {HostID: member.State.HostID}}}, RepositoryID: "zfs-1", Outcomes: map[string]protocol.ZFSProvisionHostResult{
		s.State.HostID:      {RepositoryID: "zfs-1", HostID: s.State.HostID, Status: "succeeded"},
		member.State.HostID: {RepositoryID: "zfs-1", HostID: member.State.HostID, Status: "succeeded"},
	}}
	poolZFSMu.Lock()
	err := savePoolZFS(s, zfsPoolState{Operations: map[string]zfsPoolOperation{"provision-1": op}})
	poolZFSMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "detach-1", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "zfs-1", PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: 1}}
	old := zfsLifecycleDispatch
	defer func() { zfsLifecycleDispatch = old }()
	calls := 0
	zfsLifecycleDispatch = func(_ context.Context, _ *Store, _ protocol.PoolMembership, host, phase string, command protocol.Command) (protocol.Result, error) {
		calls++
		if command.StorageLifecycle == nil || command.StorageLifecycle.HostID != host {
			t.Fatalf("member lifecycle target = %#v, want %q", command.StorageLifecycle, host)
		}
		if command.Kind != "storage-detach" { // lifecycle never offers a zpool command.
			t.Fatalf("unexpected destructive command %q", command.Kind)
		}
		if host == member.State.HostID {
			return protocol.Result{}, errors.New("configured ZFS host is unreachable")
		}
		return zfsLifecycleResult(command, "succeeded", nil), nil
	}
	if result, handled, err := ExecutePoolZFSLifecycle(context.Background(), s, cmd); !handled || err != nil || result.Status != "failed" || calls != 2 {
		t.Fatalf("unavailable detach = %#v handled=%t err=%v calls=%d", result, handled, err, calls)
	}
	calls = 0
	zfsLifecycleDispatch = func(_ context.Context, _ *Store, _ protocol.PoolMembership, host, phase string, command protocol.Command) (protocol.Result, error) {
		calls++
		if command.StorageLifecycle == nil || command.StorageLifecycle.HostID != host {
			t.Fatalf("member lifecycle target = %#v, want %q", command.StorageLifecycle, host)
		}
		if phase == "preflight" && host == member.State.HostID {
			return protocol.Result{}, errors.New("storage cannot detach while used by VM vm-1")
		}
		if phase == "execute" {
			t.Fatalf("detach executed despite a dependency")
		}
		return zfsLifecycleResult(command, "succeeded", nil), nil
	}
	if result, handled, err := ExecutePoolZFSLifecycle(context.Background(), s, cmd); !handled || err != nil || result.Status != "failed" || calls != 2 {
		t.Fatalf("dependent detach = %#v handled=%t err=%v calls=%d", result, handled, err, calls)
	}
}

func TestPoolZFSLifecycleOnlyRenamesItsMasterRecord(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	op := zfsPoolOperation{Request: protocol.ZFSProvisionRequest{RepositoryID: "zfs-1", PoolID: s.State.PoolID, Name: "old", Scope: "pool", Hosts: []protocol.ZFSProvisionHost{{HostID: s.State.HostID}}}, RepositoryID: "zfs-1", Outcomes: map[string]protocol.ZFSProvisionHostResult{s.State.HostID: {RepositoryID: "zfs-1", HostID: s.State.HostID, Status: "succeeded"}}}
	poolZFSMu.Lock()
	err := savePoolZFS(s, zfsPoolState{Operations: map[string]zfsPoolOperation{"provision-1": op}})
	poolZFSMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	registration := vmhost.ZFSProvisionStorage("zfs-1", s.State.PoolID, s.State.HostID, "new")
	// This is the generic Controller edit body: Core retains its provision marker
	// and scope rather than requiring the client to mirror either host-owned field.
	registration.ZFSProvisioned, registration.ZFSScope = false, ""
	cmd := protocol.Command{OperationID: "rename-1", Kind: "storage-update", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "update", RepositoryID: "zfs-1", PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: 1, Registration: &registration}}
	if result, handled, err := ExecutePoolZFSLifecycle(context.Background(), s, cmd); !handled || err != nil || result.Status != "succeeded" {
		t.Fatalf("rename = %#v handled=%t err=%v", result, handled, err)
	}
	// Simulate a crash after durable rename/generation writes but before the
	// agent recorded the command completion: exact replay remains idempotent.
	if result, handled, err := ExecutePoolZFSLifecycle(context.Background(), s, cmd); !handled || err != nil || result.Status != "succeeded" {
		t.Fatalf("rename replay = %#v handled=%t err=%v", result, handled, err)
	}
	changed := registration
	changed.Source.ZFS = &protocol.ZFSStorageSource{Pool: "other", Dataset: "other/vms", MountPath: registration.Source.ZFS.MountPath}
	cmd.OperationID, cmd.StorageLifecycle.ExpectedGeneration, cmd.StorageLifecycle.Registration = "rename-2", 2, &changed
	if result, handled, err := ExecutePoolZFSLifecycle(context.Background(), s, cmd); !handled || err != nil || result.Status != "failed" {
		t.Fatalf("changed source rename = %#v handled=%t err=%v", result, handled, err)
	}
	member := peerStore(t, "member-1", "pool-1")
	ordinary := protocol.Command{OperationID: "ordinary-1", Kind: "storage-recheck", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: "existing-zfs", PoolID: member.State.PoolID, HostID: member.State.HostID}}
	if _, handled, err := ExecutePoolZFSLifecycle(context.Background(), member, ordinary); handled || err != nil {
		t.Fatalf("ordinary local ZFS lifecycle was intercepted: handled=%t err=%v", handled, err)
	}
}

func TestPoolZFSLifecycleEndpointRoutesToMember(t *testing.T) {
	master, member, _, server := cpuPeerPair(t)
	client, err := PoolMemberPeerClient(master, server.URL, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	cmd := protocol.Command{OperationID: "recheck-1", Kind: "storage-recheck", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: "zfs-1", PoolID: member.State.PoolID, HostID: member.State.HostID}}
	body, err := json.Marshal(zfsLifecyclePeerRequest{Phase: "execute", Command: cmd})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+poolZFSLifecyclePath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result protocol.Result
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&result) != nil || result.Status != "failed" || result.Error != "configured ZFS host has no repository registration" {
		t.Fatalf("lifecycle endpoint = status %d result %#v", response.StatusCode, result)
	}
}
