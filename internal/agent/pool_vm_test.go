// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func poolVMTestSpec(id, host string) *protocol.VMCreateSpec {
	return &protocol.VMCreateSpec{
		ID: id, PoolID: "pool-1", HostID: host, Name: "Shared guest", GuestOS: "linux",
		CPU: protocol.VMCPU{Count: 1, Model: "host-model"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: "repo-1", Name: "disk", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}, Boot: protocol.VMBoot{Firmware: "bios"},
	}
}

func TestManagedVMChecksReady(t *testing.T) {
	for _, tc := range []struct {
		name   string
		checks []protocol.Check
		want   bool
	}{
		{name: "empty", want: false},
		{name: "healthy", checks: []protocol.Check{{Status: "healthy"}}, want: true},
		{name: "passed", checks: []protocol.Check{{Status: "passed"}}, want: true},
		{name: "unknown", checks: []protocol.Check{{Status: "unknown"}}, want: false},
		{name: "failed", checks: []protocol.Check{{Status: "failed"}}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := managedVMChecksReady(tc.checks); got != tc.want {
				t.Fatalf("managedVMChecksReady(%#v) = %v, want %v", tc.checks, got, tc.want)
			}
		})
	}
}

func TestClaimPoolVMAutostartAttemptIsDurablePerBoot(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	const vmID = "11111111-1111-4111-8111-111111111111"
	setPoolVMAutostartBootID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")

	if claimed, err := ClaimPoolVMAutostartAttempt(stateDir, vmID); err != nil || !claimed {
		t.Fatalf("first boot claim = %v, %v", claimed, err)
	}
	// A new agent process gets no in-memory state. The durable boot marker must
	// still suppress the same-boot reconciliation.
	if claimed, err := ClaimPoolVMAutostartAttempt(stateDir, vmID); err != nil || claimed {
		t.Fatalf("same boot restart claim = %v, %v", claimed, err)
	}
	setPoolVMAutostartBootID(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if claimed, err := ClaimPoolVMAutostartAttempt(stateDir, vmID); err != nil || !claimed {
		t.Fatalf("new boot claim = %v, %v", claimed, err)
	}
}

func TestClaimPoolVMAutostartAttemptConcurrentOnlyOneClaims(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	const vmID = "11111111-1111-4111-8111-111111111111"
	setPoolVMAutostartBootID(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc")

	const callers = 16
	results := make(chan bool, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := ClaimPoolVMAutostartAttempt(stateDir, vmID)
			results <- claimed
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent claim: %v", err)
		}
	}
	if claims != 1 {
		t.Fatalf("concurrent claims = %d, want 1", claims)
	}
}

func TestClaimPoolVMAutostartAttemptRefusesUnsafeState(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	const vmID = "11111111-1111-4111-8111-111111111111"
	const bootID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	setPoolVMAutostartBootID(t, bootID)
	if claimed, err := ClaimPoolVMAutostartAttempt(stateDir, "not-a-uuid"); err == nil || claimed {
		t.Fatalf("invalid VM claim = %v, %v", claimed, err)
	}
	dir := filepath.Join(stateDir, "pool-vm-autostart-attempts")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, vmID+"-"+bootID+".json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if claimed, err := ClaimPoolVMAutostartAttempt(stateDir, vmID); err == nil || claimed {
		t.Fatalf("malformed private claim = %v, %v", claimed, err)
	}
	if err := os.WriteFile(path, []byte(`{"vm_id":"`+vmID+`","boot_id":"`+bootID+`"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if claimed, err := ClaimPoolVMAutostartAttempt(stateDir, vmID); err == nil || claimed {
		t.Fatalf("non-private claim = %v, %v", claimed, err)
	}
}

func TestClaimPoolVMAutostartAttemptRefusesForeignOwner(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.Chmod(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	const vmID = "11111111-1111-4111-8111-111111111111"
	const bootID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	setPoolVMAutostartBootID(t, bootID)
	if claimed, err := ClaimPoolVMAutostartAttempt(stateDir, vmID); err != nil || !claimed {
		t.Fatalf("initial claim = %v, %v", claimed, err)
	}
	path := filepath.Join(stateDir, "pool-vm-autostart-attempts", vmID+"-"+bootID+".json")
	if os.Geteuid() == 0 {
		if err := os.Chown(path, 1, -1); err != nil {
			t.Fatal(err)
		}
		if claimed, err := ClaimPoolVMAutostartAttempt(stateDir, vmID); err == nil || claimed {
			t.Fatalf("foreign marker claim = %v, %v", claimed, err)
		}
		return
	}
	// Non-root test processes cannot chown to a foreign UID. Exercise the same
	// ownership predicate used for both Lstat and Fstat instead of skipping it.
	setPoolVMAutostartOwner(t, func(uint32) bool { return false })
	if _, err := poolVMAutostartAttemptExists(path, vmID, bootID); err == nil {
		t.Fatal("controlled foreign marker owner was accepted")
	}
	if claimed, err := ClaimPoolVMAutostartAttempt(stateDir, vmID); err == nil || claimed {
		t.Fatalf("controlled foreign state directory claim = %v, %v", claimed, err)
	}
}

func setPoolVMAutostartBootID(t *testing.T, bootID string) {
	t.Helper()
	previous := poolVMAutostartBootID
	poolVMAutostartBootID = func() (string, error) { return bootID, nil }
	t.Cleanup(func() { poolVMAutostartBootID = previous })
}

func setPoolVMAutostartOwner(t *testing.T, owner func(uint32) bool) {
	t.Helper()
	previous := poolVMAutostartOwner
	poolVMAutostartOwner = owner
	t.Cleanup(func() { poolVMAutostartOwner = previous })
}

func TestPoolVMExecutionRetainsOwnerAcrossStopAndRejectsSecondHost(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	const vm = "11111111-1111-4111-8111-111111111111"
	if err := poolVMLocal(s, vm, "define", nil, true, poolVMTestSpec(vm, "host-a"), nil); err != nil {
		t.Fatal(err)
	}
	permit := ""
	epoch := uint64(0)
	if err := poolVMLocal(s, vm, "start", &permit, false, nil, &epoch); err != nil || len(permit) != 64 || epoch != 2 {
		t.Fatalf("first start = %q epoch %d, %v", permit, epoch, err)
	}
	if err := poolVMLocal(s, vm, "start", nil, false, nil, nil); err == nil {
		t.Fatal("second start admitted while first is unresolved")
	}
	if err := poolVMLocal(s, vm, "stopped", nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Simulate a stale second Core reaching the same durable master state.  A confirmed
	// stop changes execution state only; it never transfers placement to a new host.
	s.State.HostID = "host-b"
	if err := poolVMLocal(s, vm, "define", nil, true, poolVMTestSpec(vm, "host-b"), nil); err == nil {
		t.Fatal("second host claimed stopped VM")
	}
	if err := poolVMLocal(s, vm, "start", nil, false, nil, nil); err == nil {
		t.Fatal("second host started stopped VM")
	}
}

func TestPoolVMUpdateReservationCommitsOnlyExactJournalEvidence(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	const vm = "11111111-1111-4111-8111-111111111111"
	previous := poolVMTestSpec(vm, "host-a")
	if err := poolVMLocal(s, vm, "define", nil, true, previous, nil); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocal(s, vm, "start", nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocal(s, vm, "running", nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	next := *previous
	next.Revision, next.Autostart = 1, true
	invalid := next
	invalid.Disks = append([]protocol.VMDisk(nil), next.Disks...)
	invalid.Disks = append(invalid.Disks, protocol.VMDisk{StorageID: "bad/id", Name: "bad", SizeGiB: 1, Bus: "virtio"})
	if err := poolVMLocalUpdate(s, vm, "update-reserve", "update-invalid", previous, &invalid, nil); err == nil {
		t.Fatal("pre-journal invalid disk reference reserved a pool update")
	}
	state, err := loadPoolVMs(s)
	if err != nil || state.VMs[vm].PendingUpdate != nil {
		t.Fatalf("invalid update left a pending owner record: %#v %v", state.VMs[vm], err)
	}
	if err := poolVMLocalUpdate(s, vm, "update-reserve", "update-1", previous, &next, nil); err != nil {
		t.Fatalf("reserve exact next revision: %v", err)
	}
	state, err = loadPoolVMs(s)
	if err != nil || state.VMs[vm].PendingUpdate == nil || !poolVMRecordMatchesSpec(state.VMs[vm], *previous) {
		t.Fatalf("reservation changed committed definition: %#v %v", state.VMs[vm], err)
	}
	if err := poolVMLocal(s, vm, "stop", nil, false, nil, nil); err == nil {
		t.Fatal("pending update allowed a power transition")
	}
	changed := next
	changed.Description = "different"
	if err := poolVMLocalUpdate(s, vm, "update-reserve", "update-2", previous, &changed, nil); err == nil {
		t.Fatal("second update was admitted while exact prior outcome is pending")
	}
	bad := vmhost.VMUpdateEvidence{OperationID: "update-1", Status: "succeeded", Previous: *previous, Current: changed}
	if err := poolVMLocalUpdate(s, vm, "update-commit", "update-1", nil, nil, &bad); err == nil {
		t.Fatal("mismatched journal evidence committed a pending update")
	}
	good := vmhost.VMUpdateEvidence{OperationID: "update-1", Status: "succeeded", Previous: *previous, Current: next}
	if err := poolVMLocalUpdate(s, vm, "update-commit", "update-1", nil, nil, &good); err != nil {
		t.Fatalf("matching journal evidence did not commit: %v", err)
	}
	state, err = loadPoolVMs(s)
	if err != nil || !poolVMRecordMatchesSpec(state.VMs[vm], next) || !state.VMs[vm].Autostart || state.VMs[vm].PendingUpdate != nil {
		t.Fatalf("committed update state = %#v %v", state.VMs[vm], err)
	}
	if err := poolVMLocal(s, vm, "reboot", nil, false, nil, nil); err != nil {
		t.Fatalf("owner reboot was not admitted: %v", err)
	}
	if err := poolVMLocal(s, vm, "force-reboot", nil, false, nil, nil); err != nil {
		t.Fatalf("owner force-reboot was not admitted: %v", err)
	}
	// BUG-0273: waking a guest asleep in ACPI S3 is not a second execution; the running
	// record keeps its state and epoch, and a stopped record has nothing to wake.
	epochBefore := mustLoadPoolVMs(t, s).VMs[vm].Epoch
	if err := poolVMLocal(s, vm, "wake", nil, false, nil, nil); err != nil {
		t.Fatalf("owner wake was not admitted: %v", err)
	}
	if after := mustLoadPoolVMs(t, s).VMs[vm]; after.State != "running" || after.Epoch != epochBefore {
		t.Fatalf("wake changed the execution record: %+v", after)
	}
	if err := poolVMLocal(s, vm, "media", nil, false, nil, nil); err != nil {
		t.Fatalf("owner media action was not admitted: %v", err)
	}
	failed := next
	failed.Revision = 2
	if err := poolVMLocalUpdate(s, vm, "update-reserve", "update-failed", &next, &failed, nil); err != nil {
		t.Fatal(err)
	}
	// A precise failed/unapplied journal proof releases the reservation; a
	// timeout-style ambiguous outcome does not.
	failedEvidence := vmhost.VMUpdateEvidence{OperationID: "update-failed", Status: "failed", Previous: next, Current: failed}
	if err := poolVMLocalUpdate(s, vm, "update-abort", "update-failed", nil, nil, &failedEvidence); err != nil {
		t.Fatalf("definitely failed update did not release pending owner state: %v", err)
	}
	if err := poolVMLocal(s, vm, "stop", nil, false, nil, nil); err != nil {
		t.Fatalf("definitely failed update left power blocked: %v", err)
	}
	if err := poolVMLocalUpdate(s, vm, "update-reserve", "update-ambiguous", &next, &failed, nil); err != nil {
		t.Fatal(err)
	}
	ambiguous := failedEvidence
	ambiguous.OperationID, ambiguous.Status = "update-ambiguous", "needs_attention"
	if err := poolVMLocalUpdate(s, vm, "update-abort", "update-ambiguous", nil, nil, &ambiguous); err == nil {
		t.Fatal("ambiguous update outcome released pending owner state")
	}
	if err := poolVMLocal(s, vm, "stop", nil, false, nil, nil); err == nil {
		t.Fatal("ambiguous update outcome allowed power transition")
	}
	s.State.HostID = "host-b"
	if err := poolVMLocal(s, vm, "reboot", nil, false, nil, nil); err == nil {
		t.Fatal("foreign Core reboot was admitted")
	}
	if err := poolVMLocal(s, vm, "force-reboot", nil, false, nil, nil); err == nil {
		t.Fatal("foreign Core force-reboot was admitted")
	}
	if err := poolVMLocal(s, vm, "media", nil, false, nil, nil); err == nil {
		t.Fatal("foreign Core media action was admitted")
	}
}

func TestPoolVMUpdateOutcomeRequiresMasterConfirmationAfterLocalSuccess(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	const vmID = "11111111-1111-4111-8111-111111111111"
	previous := poolVMTestSpec(vmID, "host-a")
	if err := poolVMLocal(s, vmID, "define", nil, true, previous, nil); err != nil {
		t.Fatal(err)
	}
	next := *previous
	next.Revision, next.Autostart = 1, true
	if err := poolVMLocalUpdate(s, vmID, "update-reserve", "update-1", previous, &next, nil); err != nil {
		t.Fatal(err)
	}
	evidence := vmhost.VMUpdateEvidence{OperationID: "update-1", Status: "succeeded", Previous: *previous, Current: next}
	if err := poolVMLocalUpdate(s, vmID, "update-commit", "update-1", nil, nil, &evidence); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: "update-1", Kind: "vm-update", VM: &next}
	s.State.Journal[command.OperationID] = JournalEntry{Command: &command, Status: "needs_attention"}
	sharedStorage := []protocol.StorageRegistration{{ID: "repo-1", Kind: protocol.StorageKindNFS}}
	got := confirmPoolVMUpdateOutcomes(context.Background(), s, []protocol.ManagedOutcome{{OperationID: command.OperationID, Kind: command.Kind, ID: vmID, Status: "succeeded"}}, sharedStorage)
	if got[0].Status != "succeeded" || s.State.Journal[command.OperationID].Status != "succeeded" {
		t.Fatalf("exact durable master update did not settle local outcome: %+v %#v", got, s.State.Journal[command.OperationID])
	}
	// The same native journal success is never enough by itself. A record whose
	// exact revision is no longer master-owned remains visible as uncertain.
	other := *previous
	other.Revision = 2
	command.OperationID, command.VM = "update-2", &other
	s.State.Journal[command.OperationID] = JournalEntry{Command: &command, Status: "pending"}
	got = confirmPoolVMUpdateOutcomes(context.Background(), s, []protocol.ManagedOutcome{{OperationID: command.OperationID, Kind: command.Kind, ID: vmID, Status: "succeeded"}}, sharedStorage)
	if got[0].Status != "needs_attention" || !strings.Contains(got[0].Error, "confirmation") {
		t.Fatalf("unconfirmed local update became successful: %+v", got)
	}
	if s.State.Journal[command.OperationID].Status != "pending" {
		t.Fatal("unconfirmed report altered the durable command result")
	}
	// A local-only update has no pool owner record. A crash between its native
	// success and managed-worker completion must not manufacture a peer check or
	// turn it into a pool-level needs_attention outcome.
	local := *previous
	local.ID, local.Revision = "22222222-1111-4111-8111-111111111111", 1
	local.Disks[0].StorageID = "local-repo"
	command = protocol.Command{OperationID: "local-update", Kind: "vm-update", VM: &local}
	s.State.Journal[command.OperationID] = JournalEntry{Command: &command, Status: "pending"}
	got = confirmPoolVMUpdateOutcomes(context.Background(), s, []protocol.ManagedOutcome{{OperationID: command.OperationID, Kind: command.Kind, ID: local.ID, Status: "succeeded"}}, sharedStorage)
	if got[0].Status != "succeeded" || s.State.Journal[command.OperationID].Status != "pending" {
		t.Fatalf("local-only update was incorrectly pool-gated: %+v %#v", got, s.State.Journal[command.OperationID])
	}
}

func TestPoolVMFailedSharedToLocalOutcomeAbortsPendingReservation(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	previous := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	attempted := *previous
	attempted.Revision = 1
	attempted.Disks = append([]protocol.VMDisk(nil), previous.Disks...)
	attempted.Disks[0].StorageID = "local-repo"
	if err := poolVMLocal(s, previous.ID, "define", nil, true, previous, nil); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocalUpdate(s, previous.ID, "update-reserve", "update-failed", previous, &attempted, nil); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, journal := range map[string]any{
		"create.json":        map[string]any{"command": protocol.Command{OperationID: "create", Kind: "vm-create", VM: previous}, "result": protocol.Result{OperationID: "create", Status: "succeeded"}},
		"update-failed.json": map[string]any{"command": protocol.Command{OperationID: "update-failed", Kind: "vm-update", VM: &attempted}, "result": protocol.Result{OperationID: "update-failed", Status: "failed"}, "stage": "validating"},
	} {
		b, err := json.Marshal(journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := protocol.Command{OperationID: "update-failed", Kind: "vm-update", VM: &attempted}
	s.State.Journal[command.OperationID] = JournalEntry{Command: &command, Status: "pending"}
	sharedStorage := []protocol.StorageRegistration{{ID: "repo-1", Kind: protocol.StorageKindNFS}}
	if evidence, err := vmhost.NewManaged(dir).FailedVMUpdateEvidence(context.Background(), attempted.ID, command.OperationID, attempted); err != nil {
		t.Fatalf("failed evidence setup: %v", err)
	} else if !reflect.DeepEqual(evidence.Previous, *previous) {
		t.Fatalf("failed evidence predecessor = %#v", evidence)
	}
	got := confirmPoolVMUpdateOutcomes(context.Background(), s, []protocol.ManagedOutcome{{OperationID: command.OperationID, Kind: command.Kind, ID: attempted.ID, Status: "failed"}}, sharedStorage)
	if got[0].Status != "failed" || s.State.Journal[command.OperationID].Status != "failed" {
		t.Fatalf("failed shared-to-local update remained unconfirmed: %+v %#v", got, s.State.Journal[command.OperationID])
	}
	state, err := loadPoolVMs(s)
	if err != nil || state.VMs[attempted.ID].PendingUpdate != nil || !poolVMRecordMatchesSpec(state.VMs[attempted.ID], *previous) {
		t.Fatalf("failed shared-to-local update changed master definition or retained pending state: %#v %v", state.VMs[attempted.ID], err)
	}
}

func TestPoolVMRecoveryDefineBindsExactDurableSpec(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	const vm = "11111111-1111-4111-8111-111111111111"
	spec := poolVMTestSpec(vm, "host-a")
	if err := poolVMLocal(s, vm, "define", nil, true, spec, nil); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocal(s, vm, "define", nil, false, spec, nil); err != nil {
		t.Fatalf("same durable spec rejected: %v", err)
	}
	changedRevision := *spec
	changedRevision.Revision = 1
	if err := poolVMLocal(s, vm, "define", nil, false, &changedRevision, nil); err == nil {
		t.Fatal("changed revision matched durable owner record")
	}
	changedDisk := *spec
	changedDisk.Disks = append([]protocol.VMDisk(nil), spec.Disks...)
	changedDisk.Disks[0].StorageID = "repo-other"
	if err := poolVMLocal(s, vm, "define", nil, false, &changedDisk, nil); err == nil {
		t.Fatal("changed disk matched durable owner record")
	}
	state, err := loadPoolVMs(s)
	if err != nil || !poolVMRecordMatchesSpec(state.VMs[vm], *spec) {
		t.Fatalf("rejected recovery changed durable owner: %+v err=%v", state.VMs[vm], err)
	}
}

func TestPoolVMRecoveryPeerRejectsChangedDurableSpec(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
		t.Fatalf("join peer: %v", err)
	}
	const vm = "11111111-1111-4111-8111-111111111111"
	spec := poolVMTestSpec(vm, member.State.HostID)
	if err = poolVMRequestPeer(context.Background(), member, vm, "define", nil, true, spec, nil); err != nil {
		t.Fatal(err)
	}
	if err = poolVMRequestPeer(context.Background(), member, vm, "define", nil, false, spec, nil); err != nil {
		t.Fatalf("matching peer recovery spec rejected: %v", err)
	}
	changed := *spec
	changed.Disks = append([]protocol.VMDisk(nil), spec.Disks...)
	changed.Disks[0].StorageID = "repo-other"
	if err = poolVMRequestPeer(context.Background(), member, vm, "define", nil, false, &changed, nil); err == nil {
		t.Fatal("peer recovery accepted changed durable disk spec")
	}
}

func TestPoolVMUpdatePeerTransitionRequiresExactJournalEvidence(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
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
	const vm = "11111111-1111-4111-8111-111111111111"
	previous := poolVMTestSpec(vm, member.State.HostID)
	if err = poolVMRequestPeer(context.Background(), member, vm, "define", nil, true, previous, nil); err != nil {
		t.Fatal(err)
	}
	next := *previous
	next.Revision, next.Autostart = 1, true
	if err = poolVMUpdateRequestPeer(context.Background(), member, vm, "update-reserve", "update-peer-1", previous, &next, nil); err != nil {
		t.Fatalf("peer reserve: %v", err)
	}
	state, err := loadPoolVMs(master)
	if err != nil || state.VMs[vm].PendingUpdate == nil || !poolVMRecordMatchesSpec(state.VMs[vm], *previous) {
		t.Fatalf("master reserve state = %#v %v", state.VMs[vm], err)
	}
	bad := vmhost.VMUpdateEvidence{OperationID: "update-peer-1", Status: "succeeded", Previous: *previous, Current: *previous}
	if err = poolVMUpdateRequestPeer(context.Background(), member, vm, "update-commit", "update-peer-1", nil, nil, &bad); err == nil {
		t.Fatal("peer committed mismatched update evidence")
	}
	good := vmhost.VMUpdateEvidence{OperationID: "update-peer-1", Status: "succeeded", Previous: *previous, Current: next}
	if err = poolVMUpdateRequestPeer(context.Background(), member, vm, "update-commit", "update-peer-1", nil, nil, &good); err != nil {
		t.Fatalf("peer commit: %v", err)
	}
	state, err = loadPoolVMs(master)
	if err != nil || !poolVMRecordMatchesSpec(state.VMs[vm], next) || state.VMs[vm].PendingUpdate != nil {
		t.Fatalf("master committed state = %#v %v", state.VMs[vm], err)
	}
	failed := next
	failed.Revision = 2
	if err = poolVMUpdateRequestPeer(context.Background(), member, vm, "update-reserve", "update-peer-failed", &next, &failed, nil); err != nil {
		t.Fatalf("peer reserve for abort: %v", err)
	}
	failure := vmhost.VMUpdateEvidence{OperationID: "update-peer-failed", Status: "failed", Previous: next, Current: failed}
	if err = poolVMUpdateRequestPeer(context.Background(), member, vm, "update-abort", "update-peer-failed", nil, nil, &failure); err != nil {
		t.Fatalf("peer exact failed abort: %v", err)
	}
	// A lost abort response may be retried; the master has already cleared the
	// pending record but returns the same exact acknowledgement, not a bare 200.
	if err = poolVMUpdateRequestPeer(context.Background(), member, vm, "update-abort", "update-peer-failed", nil, nil, &failure); err != nil {
		t.Fatalf("peer lost-response abort retry: %v", err)
	}
	wrongFailure := failure
	wrongFailure.OperationID = "different-failed-operation"
	if err = poolVMUpdateRequestPeer(context.Background(), member, vm, "update-abort", wrongFailure.OperationID, nil, nil, &wrongFailure); err == nil {
		t.Fatal("different failed update replay was accepted after abort")
	}
	state, err = loadPoolVMs(master)
	if err != nil || state.VMs[vm].PendingUpdate != nil || !poolVMRecordMatchesSpec(state.VMs[vm], next) {
		t.Fatalf("master abort state = %#v %v", state.VMs[vm], err)
	}
}

func TestPoolVMFirstLocalToSharedUpdateSeedsOnlyExactMemberInventory(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
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
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	const vmID = "11111111-1111-4111-8111-111111111111"
	previous := poolVMTestSpec(vmID, member.State.HostID)
	previous.Disks[0].StorageID = "local-repo"
	inv := poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{
		master.State.HostID: {HostID: master.State.HostID, Generation: membership.Generation, Observed: map[string]string{}},
		member.State.HostID: {HostID: member.State.HostID, Generation: membership.Generation, VMs: []protocol.VMCreateSpec{*previous}, Observed: map[string]string{vmID: "shut off"}},
	}}
	if err = savePoolVMs(master, inv); err != nil {
		t.Fatal(err)
	}
	next := *previous
	next.Revision, next.Disks = 1, append([]protocol.VMDisk(nil), previous.Disks...)
	next.Disks[0].StorageID = "repo-1"
	if err = poolVMUpdateRequestPeer(context.Background(), member, vmID, "update-reserve", "first-shared", previous, &next, nil); err != nil {
		t.Fatalf("exact current member inventory did not seed first shared update: %v", err)
	}
	state, err := loadPoolVMs(master)
	if err != nil || state.VMs[vmID].PendingUpdate == nil || !poolVMRecordMatchesSpec(state.VMs[vmID], *previous) {
		t.Fatalf("first shared seed state = %#v %v", state.VMs[vmID], err)
	}
	wrong := *previous
	wrong.Description = "unattested"
	if err = poolVMUpdateRequestPeer(context.Background(), member, "22222222-1111-4111-8111-111111111111", "update-reserve", "bad-seed", &wrong, &next, nil); err == nil {
		t.Fatal("unattested prior definition seeded a pool owner record")
	}
}

func TestPoolVMLocalFirstLocalToSharedUpdateSeedsExactInventory(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	previous := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "master-1")
	previous.Disks[0].StorageID = "local-repo"
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{"master-1": {HostID: "master-1", Generation: 1, VMs: []protocol.VMCreateSpec{*previous}, Observed: map[string]string{previous.ID: "shut off"}}}}); err != nil {
		t.Fatal(err)
	}
	next := *previous
	next.Revision, next.Disks = 1, append([]protocol.VMDisk(nil), previous.Disks...)
	next.Disks[0].StorageID = "repo-1"
	if err := poolVMLocalUpdate(s, previous.ID, "update-reserve", "first-local-shared", previous, &next, nil); err != nil {
		t.Fatalf("master-local first shared update did not seed exact inventory: %v", err)
	}
	state, err := loadPoolVMs(s)
	if err != nil || state.VMs[previous.ID].PendingUpdate == nil || !poolVMRecordMatchesSpec(state.VMs[previous.ID], *previous) {
		t.Fatalf("local seed state = %#v %v", state.VMs[previous.ID], err)
	}
}

func TestPoolVMUpdatePeerRejectsEmptyMasterAcknowledgement(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler = PoolPeerHandler(master)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
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
	const vm = "11111111-1111-4111-8111-111111111111"
	previous := poolVMTestSpec(vm, member.State.HostID)
	if err = poolVMRequestPeer(context.Background(), member, vm, "define", nil, true, previous, nil); err != nil {
		t.Fatal(err)
	}
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	next := *previous
	next.Revision = 1
	if err = poolVMUpdateRequestPeer(context.Background(), member, vm, "update-reserve", "update-empty-ack", previous, &next, nil); err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("empty master acknowledgement was accepted: %v", err)
	}
}

func TestPoolVMRecoveryPeerRefusesOldMasterWithoutSpecAck(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	joinHandler := PoolPeerHandler(master)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, poolVMPath) {
			if _, err := AuthenticatePoolPeer(master, r); err != nil {
				http.Error(w, "peer rejected", http.StatusUnauthorized)
				return
			}
			// Old peers returned a generic success and did not bind an optional VM.
			_ = json.NewEncoder(w).Encode(poolVMRequest{VMID: "11111111-1111-4111-8111-111111111111", Action: "define"})
			return
		}
		joinHandler.ServeHTTP(w, r)
	}))
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
	const vm = "11111111-1111-4111-8111-111111111111"
	if err = poolVMRequestPeer(context.Background(), member, vm, "define", nil, false, poolVMTestSpec(vm, member.State.HostID), nil); err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("old master acknowledgement accepted: %v", err)
	}
}

func TestSingleHostSharedVMStillUsesDurableOwnerAndPermit(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	const vm = "11111111-1111-4111-8111-111111111111"
	if err := poolVMRequestPeer(context.Background(), s, vm, "define", nil, true, poolVMTestSpec(vm, "host-a"), nil); err != nil {
		t.Fatal(err)
	}
	permit := ""
	if err := poolVMRequestPeer(context.Background(), s, vm, "start", &permit, false, nil, nil); err != nil || len(permit) != 64 {
		t.Fatalf("single-host start permit = %q, %v", permit, err)
	}
	if err := vmhost.WritePoolVMStartPermit(s.dir, vm, permit); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(poolVMFile(s)); err != nil {
		t.Fatalf("durable owner missing: %v", err)
	}
	if err := os.WriteFile(poolStatePath(s), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := poolVMRequestPeer(context.Background(), s, vm, "stopped", nil, false, nil, nil); err == nil {
		t.Fatal("corrupt peer state bypassed execution fence")
	}
}

func TestSharedVMCreateAcceptsRealHealthyManagedObservations(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-a", Name: "shared", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports", Directory: "guests"}}, Generation: 1}
	journal := map[string]any{"command": protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &storage}, "result": protocol.Result{OperationID: "register-1", Status: "succeeded"}, "applied_storage": storage}
	b, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dir, "managed-vms")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "register-1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	managed, checks := vmhost.NewManaged(dir).Applied(context.Background())
	if !managedVMChecksReady(checks) || len(managed.Storage) != 1 {
		t.Fatalf("real managed observations are not ready: storage=%#v checks=%#v", managed.Storage, checks)
	}
	if _, err = AdmitPoolVMExecution(context.Background(), s, protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: vm}); err != nil {
		t.Fatalf("shared VM create rejected healthy observations: %v", err)
	}
}

func TestClusteredVMStopOnlyBypassesLostQuorumWithoutPermitOrOwnerChange(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-a", Name: "shared", Path: "/var/lib/nex-storage/repo-1", Kind: protocol.StorageKindISCSI, Generation: 1}
	storage.Source.ISCSI = &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:shared", LUN: 1, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "12345678-1234-1234-1234-123456789abc", MountPath: storage.Path, Mode: "clustered_gfs2", ClusterID: "cluster-1", LockTable: "cluster-1:repo-1", EligibleHostIDs: []string{"host-a", "host-b", "host-c"}}
	storage.Identity.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: storage.Source.ISCSI.TargetIQN, LUN: storage.Source.ISCSI.LUN, WWID: storage.Source.ISCSI.WWID, FilesystemUUID: storage.Source.ISCSI.FilesystemUUID, FilesystemType: "gfs2", GFS2LockTable: storage.Source.ISCSI.LockTable, GFS2Journals: 3}
	dir := filepath.Join(s.dir, "managed-vms")
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), dir, storage); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	vm.Disks[0].StorageID = storage.ID
	virshDir := t.TempDir()
	virsh := filepath.Join(virshDir, "virsh")
	if err := os.WriteFile(virsh, []byte("#!/bin/sh\nprintf '%s\\n' '"+vm.ID+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", virshDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	b, err := json.Marshal(map[string]any{"command": protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: vm}, "result": protocol.Result{OperationID: "create-1", Status: "succeeded"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "create-1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocal(s, vm.ID, "define", nil, true, vm, nil); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocal(s, vm.ID, "start", nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocal(s, vm.ID, "running", nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	before, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}

	stop := protocol.Command{OperationID: "stop-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, PoolID: "pool-1", HostID: "host-a", Action: "force-stop"}}
	for _, action := range []string{"shutdown", "force-stop"} {
		command := stop
		command.OperationID = action + "-1"
		command.Power = &protocol.VMPowerRequest{VMID: vm.ID, PoolID: "pool-1", HostID: "host-a", Action: action}
		finish, err := AdmitPoolVMExecution(context.Background(), s, command)
		if err != nil {
			t.Fatalf("lost-quorum %s admission: %v", action, err)
		}
		if err := finish(protocol.Result{OperationID: command.OperationID, Status: "succeeded"}, nil); err != nil {
			t.Fatalf("lost-quorum %s completion: %v", action, err)
		}
		ctx, err := poolVMCommandExecutionContext(context.Background(), s, command)
		if err != nil {
			t.Fatalf("lost-quorum %s context: %v", action, err)
		}
		if _, configured := vmhost.StorageClusterConfig(ctx); configured {
			t.Fatalf("%s received clustered execution context", action)
		}
	}
	if _, err := os.Stat(filepath.Join(s.dir, "pool-vm-start-permits", vm.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("stop-only admission wrote a start permit: %v", err)
	}
	after, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("stop-only admission changed durable pool ownership: before=%#v after=%#v", before, after)
	}

	for _, action := range []string{"start", "reboot", "force-reboot"} {
		command := stop
		command.OperationID = action + "-1"
		command.Power = &protocol.VMPowerRequest{VMID: vm.ID, PoolID: "pool-1", HostID: "host-a", Action: action}
		if _, err := AdmitPoolVMExecution(context.Background(), s, command); err == nil {
			t.Fatalf("%s bypassed unavailable clustered storage", action)
		}
	}
	for _, mismatch := range []protocol.VMPowerRequest{
		{VMID: vm.ID, PoolID: "other-pool", HostID: "host-a", Action: "force-stop"},
		{VMID: vm.ID, PoolID: "pool-1", HostID: "host-b", Action: "force-stop"},
		{VMID: "22222222-1111-4111-8111-111111111111", PoolID: "pool-1", HostID: "host-a", Action: "force-stop"},
	} {
		command := stop
		command.OperationID = "mismatch-" + mismatch.VMID[:8]
		command.Power = &mismatch
		if err := poolVMStopOnly(s, command, *vm); err == nil {
			t.Fatalf("mismatched stop received the exception: %#v", mismatch)
		}
	}
}

// sharedMediaVMStore is a single-host pool master owning one VM on pool-shared storage whose
// definition names an ISO.  Applied verifies durable VM definitions against libvirt, so a
// minimal private virsh stand-in keeps these admission-path tests from weakening the
// production fail-closed observation requirement.
func sharedMediaVMStore(t *testing.T) (*Store, *protocol.VMCreateSpec) {
	t.Helper()
	s := peerStore(t, "host-a", "pool-1")
	if err := savePoolPeer(s, poolPeerState{Membership: protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 1, Members: []protocol.PoolMember{{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}}); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-a", Name: "shared", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports", Directory: "guests"}}, Generation: 1}
	appliedStorage := storage
	appliedStorage.Identity.NFS = &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/exports", FilesystemID: "fs-test"}
	vm := poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	// Install-media-first, the shape the create wizard produces when an ISO is chosen: the CD
	// drive boots before the disk, so an eject that renumbered it would need a stop (BUG-0188).
	vm.MediaID, vm.Boot.MediaBootOrder, vm.Disks[0].BootOrder = "media-1", 1, 2
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	virshDir := t.TempDir()
	virsh := filepath.Join(virshDir, "virsh")
	if err := os.WriteFile(virsh, []byte("#!/bin/sh\ncase \"$*\" in\n  *domstate*) printf '%s\\n' 'shut off' ;;\n  *) printf '%s\\n' '"+vm.ID+"' ;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", virshDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, journal := range map[string]any{
		"register-1.json": map[string]any{"command": protocol.Command{OperationID: "register-1", Kind: "storage-register", Storage: &storage}, "result": protocol.Result{OperationID: "register-1", Status: "succeeded"}, "applied_storage": appliedStorage},
		"create-1.json":   map[string]any{"command": protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: vm}, "result": protocol.Result{OperationID: "create-1", Status: "succeeded"}},
	} {
		b, err := json.Marshal(journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := poolVMLocal(s, vm.ID, "define", nil, true, vm, nil); err != nil {
		t.Fatal(err)
	}
	return s, vm
}

func sharedMediaCommand(vm *protocol.VMCreateSpec, op, action, media string) protocol.Command {
	return protocol.Command{OperationID: op, Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: action, MediaID: media}}
}

// A media command carries its VM identity in VMMedia rather than Command.VM.
// It must therefore take the same shared-storage ownership path before the
// managed engine can change a guest's persisted media definition.  A change to
// the durable definition needs the master's prior→next transition, so a raw
// vm-media command that would rewrite it stays fail-closed here.
func TestSharedVMMediaUsesOwnerAdmissionBeforeExecution(t *testing.T) {
	s, vm := sharedMediaVMStore(t)
	next := *vm
	next.Revision = 1
	if err := poolVMLocalUpdate(s, vm.ID, "update-reserve", "update-1", vm, &next, nil); err != nil {
		t.Fatal(err)
	}
	command := sharedMediaCommand(vm, "media-1", "eject", "")
	if _, shared, err := poolSharedVM(context.Background(), s, command); err != nil || !shared {
		t.Fatalf("media did not resolve the managed shared VM: shared=%v err=%v", shared, err)
	}
	if _, err := AdmitPoolVMExecution(context.Background(), s, command); err == nil {
		t.Fatalf("shared media bypassed pending owner admission: %v", err)
	}
}

// BUG-0185: a console eject or library insert on a shared VM is applied as the same durable
// prior→next definition transition vm-update uses, so the master reserves the exact next
// revision and its committed Spec cannot drift from the host's definition.
func TestSharedVMMediaChangeBecomesDurableDefinitionUpdate(t *testing.T) {
	s, vm := sharedMediaVMStore(t)
	command := sharedMediaCommand(vm, "media-1", "eject", "")
	update := PoolVMMediaCommand(context.Background(), s, command)
	if update.Kind != "vm-update" || update.OperationID != command.OperationID || update.VM == nil {
		t.Fatalf("media change was not expressed as a definition transition: %+v", update)
	}
	if update.VM.Revision != vm.Revision+1 || update.VM.MediaID != "" || update.VM.Boot.MediaBootOrder != vm.Boot.MediaBootOrder || update.VM.ID != vm.ID {
		t.Fatalf("unexpected next definition: %+v", update.VM)
	}
	// The transition must stay applicable to a running guest: ejecting a disc is not a reason
	// to stop a VM, and Core's update path refuses any change NeedsStop reports (BUG-0188).
	if stops, err := protocol.NeedsStop(*vm, *update.VM); err != nil || len(stops) != 0 {
		t.Fatalf("eject of a running shared VM classified stop-first: %v %v", stops, err)
	}
	finish, err := AdmitPoolVMExecution(context.Background(), s, update)
	if err != nil || finish == nil {
		t.Fatalf("durable media transition refused: %v", err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[vm.ID]
	if record.PendingUpdate == nil || record.PendingUpdate.OperationID != command.OperationID || !reflect.DeepEqual(record.PendingUpdate.Next, *update.VM) {
		t.Fatalf("master did not reserve the media revision: %+v", record.PendingUpdate)
	}
	if record.Spec.MediaID != "media-1" {
		t.Fatalf("committed Spec changed before the host proved the update: %+v", record.Spec)
	}
}

// Re-inserting the ISO the definition already names changes no durable state: it reconnects a
// tray after a guest ejected the disc (BUG-0184), so it keeps the vm-media path and needs only
// the owner admission every other execution action takes.
func TestSharedVMMediaReinsertKeepsDefinitionAndIsAdmitted(t *testing.T) {
	s, vm := sharedMediaVMStore(t)
	command := sharedMediaCommand(vm, "media-2", "insert", "media-1")
	if same := PoolVMMediaCommand(context.Background(), s, command); !reflect.DeepEqual(same, command) {
		t.Fatalf("a reconnect of the recorded ISO must not become a definition update: %+v", same)
	}
	finish, err := AdmitPoolVMExecution(context.Background(), s, command)
	if err != nil || finish == nil {
		t.Fatalf("reconnecting the recorded ISO was refused: %v", err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if record := state.VMs[vm.ID]; record.PendingUpdate != nil || record.Spec.MediaID != "media-1" {
		t.Fatalf("tray reconnect changed the durable owner record: %+v", record)
	}
}

// Execute compares a redelivered command byte for byte against the journal, so the transition
// must be reused exactly, not derived again from a definition the first attempt already
// changed.  Otherwise one operation ID would mean two different commands.
func TestSharedVMMediaRedeliveryReusesJournaledCommand(t *testing.T) {
	s, vm := sharedMediaVMStore(t)
	command := sharedMediaCommand(vm, "media-1", "eject", "")
	update := PoolVMMediaCommand(context.Background(), s, command)
	if update.VM == nil {
		t.Fatalf("media change was not expressed as a definition transition: %+v", update)
	}
	applied := *update.VM
	b, err := json.Marshal(map[string]any{"command": update, "result": protocol.Result{OperationID: update.OperationID, Status: "succeeded"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.dir, "managed-vms", "media-1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	again := PoolVMMediaCommand(context.Background(), s, command)
	if again.Kind != "vm-update" || again.VM == nil || !reflect.DeepEqual(*again.VM, applied) {
		t.Fatalf("redelivered media operation was not reused as journaled: %+v", again.VM)
	}
}

// BUG-0192: one shared VM update left needs_attention after its native define must block only
// that VM. Before the fix it made every pool VM inventory publication fail, which refused
// every shared-storage VM operation in the pool.
func TestSharedVMUnresolvedUpdateDoesNotBlockPoolInventory(t *testing.T) {
	ctx := context.Background()
	s, vm := sharedMediaVMStore(t)
	dir := filepath.Join(s.dir, "managed-vms")
	writeJournal := func(op string, next *protocol.VMCreateSpec, status, stage string) {
		t.Helper()
		b, err := json.Marshal(map[string]any{"command": protocol.Command{OperationID: op, Kind: "vm-update", VM: next}, "result": protocol.Result{OperationID: op, Status: status}, "stage": stage})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, op+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	rev1 := *vm
	rev1.Revision, rev1.MediaID = 1, ""
	if err := poolVMLocalUpdate(s, vm.ID, "update-reserve", "update-1", vm, &rev1, nil); err != nil {
		t.Fatal(err)
	}
	writeJournal("update-1", &rev1, "succeeded", "defined")
	evidence, err := vmhost.NewManaged(dir).SuccessfulVMUpdateEvidence(ctx, vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = poolVMLocalUpdate(s, vm.ID, "update-commit", "update-1", nil, nil, &evidence); err != nil {
		t.Fatal(err)
	}
	rev2 := rev1
	rev2.Revision, rev2.MediaID = 2, "media-1"
	if err = poolVMLocalUpdate(s, vm.ID, "update-reserve", "update-2", &rev1, &rev2, nil); err != nil {
		t.Fatal(err)
	}
	writeJournal("update-2", &rev2, "needs_attention", "defining")
	if err = PublishPoolVMInventory(ctx, s); err != nil {
		t.Fatalf("an unresolved update of one shared VM blocked pool inventory: %v", err)
	}
	start := protocol.Command{OperationID: "start-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "start"}}
	if _, err = AdmitPoolVMExecution(ctx, s, start); err == nil {
		t.Fatal("VM with an unresolved update was admitted to start")
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if record := state.VMs[vm.ID]; record.PendingUpdate == nil || record.PendingUpdate.OperationID != "update-2" || !reflect.DeepEqual(record.Spec, rev1) {
		t.Fatalf("inventory changed the unresolved owner record: %+v", record)
	}
}

func TestStorageRecheckReceivesCoreOwnedClusterContext(t *testing.T) {
	s := clusterRecoveryStore(t)
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Name: "shared", Path: "/var/lib/nex-storage/repo-1", Kind: protocol.StorageKindISCSI, Generation: 1}
	storage.Source.ISCSI = &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:shared", LUN: 1, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "12345678-1234-1234-1234-123456789abc", MountPath: storage.Path, Mode: "clustered_gfs2", ClusterID: "cluster-1", LockTable: "cluster-1:repo-1", EligibleHostIDs: []string{"master-1", "member-1", "member-2"}}
	storage.Identity.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: storage.Source.ISCSI.TargetIQN, LUN: 1, WWID: storage.Source.ISCSI.WWID, FilesystemUUID: storage.Source.ISCSI.FilesystemUUID, FilesystemType: "gfs2", GFS2LockTable: "cluster-1:repo-1", GFS2Journals: 3}
	config := protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: storage.ID, ClusterID: "cluster-1", Generation: 1, LockTable: "cluster-1:repo-1", DevicePath: "/dev/disk/by-id/scsi-3600508b400105e210000900000490000", MountPath: storage.Path}
	for i, host := range storage.Source.ISCSI.EligibleHostIDs {
		config.Nodes = append(config.Nodes, protocol.StorageClusterNode{HostID: host, CorosyncNodeID: uint32(i + 1), CorosyncAddress: "core" + string(rune('1'+i)) + ".example.test", Fence: protocol.StorageClusterFence{Kind: protocol.FenceVirsh, Endpoint: "fence.example.test", TargetID: "11111111-2222-3333-4444-55555555555" + string(rune('1'+i)), CredentialRef: "credential" + string(rune('1'+i)), SSHHostKeyRef: "hostkey" + string(rune('1'+i)), SSHLogin: "root", SSHPort: 22, VirshURI: "qemu:///system"}})
	}
	for _, node := range config.Nodes {
		config.FenceProofs = append(config.FenceProofs, protocol.StorageClusterFenceStatus{HostID: node.HostID, TargetID: node.Fence.TargetID, Status: "passed", ExclusionVerified: true, ObservedAt: time.Now().UTC(), ConfigGeneration: config.Generation, ConfigDigest: config.Digest()})
	}
	op := storageClusterOperation{RequestID: "cluster-setup", MembershipGeneration: 1, Registration: storage, Config: config, Status: "healthy", Activation: &storageClusterActivation{RequestID: "cluster-setup", RepositoryID: storage.ID, Generation: config.Generation, ConfigDigest: config.Digest(), MembershipGeneration: 1, CommittedAt: time.Now().UTC()}}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dir, "managed-vms")
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), dir, storage); err != nil {
		t.Fatal(err)
	}
	if _, checks := vmhost.NewManaged(dir).Applied(context.Background()); !managedVMChecksReady(checks) {
		t.Fatalf("clustered registration journal is not applied: %#v", checks)
	}
	ctx, err := poolVMCommandExecutionContext(context.Background(), s, protocol.Command{Kind: "storage-recheck", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: storage.ID, PoolID: "pool-1", HostID: "master-1"}})
	if err != nil {
		t.Fatalf("clustered storage recheck context: %v", err)
	}
	got, ok := vmhost.StorageClusterConfig(ctx)
	if !ok || got.Digest() != config.Digest() {
		t.Fatalf("storage recheck did not receive durable cluster config: %#v", got)
	}
	legacy := op
	legacy.Activation = nil
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: legacy}}); err != nil {
		t.Fatal(err)
	}
	if _, err := poolVMCommandExecutionContext(context.Background(), s, protocol.Command{Kind: "storage-recheck", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: storage.ID, PoolID: "pool-1", HostID: "master-1"}}); err == nil {
		t.Fatal("legacy healthy operation bypassed initial activation receipt")
	}
	vm := *poolVMTestSpec("22222222-1111-4111-8111-111111111111", "master-1")
	vm.Disks[0].StorageID = storage.ID
	if _, err := poolVMExecutionContext(context.Background(), s, vm); err == nil {
		t.Fatal("VM execution path bypassed the missing initial activation receipt")
	}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}

	unverified := op
	unverified.Config.FenceProofs[0].Status = "unverified"
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: unverified}}); err != nil {
		t.Fatal(err)
	}
	if _, err := poolVMCommandExecutionContext(context.Background(), s, protocol.Command{Kind: "storage-recheck", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: storage.ID, PoolID: "pool-1", HostID: "master-1"}}); err == nil {
		t.Fatal("clustered storage recheck accepted an unverified fence profile")
	}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	peerState, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	peerState.Membership.Generation++
	if err := savePoolPeer(s, peerState); err != nil {
		t.Fatal(err)
	}
	if _, err := poolVMCommandExecutionContext(context.Background(), s, protocol.Command{Kind: "storage-recheck", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "recheck", RepositoryID: storage.ID, PoolID: "pool-1", HostID: "master-1"}}); err == nil {
		t.Fatal("clustered storage recheck accepted a stale membership generation")
	}
}

func TestSingleHostPoolMasterPublishesSharedVMInventory(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if PublishPoolVMInventory(context.Background(), s) == nil {
		t.Fatal("a Core without a membership record published pool VM inventory")
	}
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	if err := PublishPoolVMInventory(context.Background(), s); err != nil {
		t.Fatalf("single-host pool master refused shared VM power: %v", err)
	}
}

// BUG-0192: a console media change on a shared VM is journaled by the agent as vm-media while
// the managed engine executes it as a vm-update under the same operation ID. Once Core settles
// that update from native observation, the report path must confirm it with the master exactly
// as it does for a vm-update entry instead of pinning it at needs_attention.
func TestSharedVMRewrittenMediaOutcomeConfirmsWithPoolOwner(t *testing.T) {
	sharedStorage := []protocol.StorageRegistration{{ID: "repo-1", Kind: protocol.StorageKindNFS}}
	setup := func(t *testing.T, managed protocol.Command, status, stage string, unapplied bool) (*Store, *protocol.VMCreateSpec, protocol.Command) {
		t.Helper()
		s, vm := sharedMediaVMStore(t)
		media := sharedMediaCommand(vm, "media-op", "eject", "")
		next := *vm
		next.Revision, next.MediaID = 1, ""
		if err := poolVMLocalUpdate(s, vm.ID, "update-reserve", media.OperationID, vm, &next, nil); err != nil {
			t.Fatal(err)
		}
		if managed.VM == nil && managed.Kind == "vm-update" {
			managed.VM = &next
		}
		b, err := json.Marshal(map[string]any{"command": managed, "result": protocol.Result{OperationID: media.OperationID, Status: status}, "stage": stage, "unapplied_observed": unapplied})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(s.dir, "managed-vms", media.OperationID+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		s.State.Journal[media.OperationID] = JournalEntry{Command: &media, Status: "needs_attention"}
		return s, vm, media
	}
	t.Run("applied", func(t *testing.T) {
		s, vm, media := setup(t, protocol.Command{OperationID: "media-op", Kind: "vm-update"}, "succeeded", "defined", false)
		evidence, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).SuccessfulVMUpdateEvidence(context.Background(), vm.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = poolVMLocalUpdate(s, vm.ID, "update-commit", media.OperationID, nil, nil, &evidence); err != nil {
			t.Fatal(err)
		}
		got := confirmPoolVMUpdateOutcomes(context.Background(), s, []protocol.ManagedOutcome{{OperationID: media.OperationID, Kind: "vm-update", ID: vm.ID, Status: "succeeded"}}, sharedStorage)
		if got[0].Status != "succeeded" || s.State.Journal[media.OperationID].Status != "succeeded" {
			t.Fatalf("settled media update was not confirmed with the pool owner: %+v %#v", got, s.State.Journal[media.OperationID])
		}
		if got[0].Kind != "vm-media" {
			t.Fatalf("settled media outcome must keep the requested kind the Controller tracks: %+v", got)
		}
		if entry := s.State.Journal[media.OperationID]; entry.Command.Kind != "vm-media" {
			t.Fatalf("confirmation rewrote the agent journal command: %+v", entry.Command)
		}
	})
	t.Run("unapplied", func(t *testing.T) {
		s, vm, media := setup(t, protocol.Command{OperationID: "media-op", Kind: "vm-update"}, "failed", "defining", true)
		got := confirmPoolVMUpdateOutcomes(context.Background(), s, []protocol.ManagedOutcome{{OperationID: media.OperationID, Kind: "vm-update", ID: vm.ID, Status: "failed"}}, sharedStorage)
		if got[0].Status != "failed" || s.State.Journal[media.OperationID].Status != "failed" {
			t.Fatalf("proven-unapplied media update was not aborted at the pool owner: %+v %#v", got, s.State.Journal[media.OperationID])
		}
		if got[0].Kind != "vm-media" {
			t.Fatalf("aborted media outcome must keep the requested kind the Controller tracks: %+v", got)
		}
		state, err := loadPoolVMs(s)
		if err != nil || state.VMs[vm.ID].PendingUpdate != nil || !poolVMRecordMatchesSpec(state.VMs[vm.ID], *vm) {
			t.Fatalf("abort left the owner record pending or changed: %+v %v", state.VMs[vm.ID], err)
		}
	})
	t.Run("mismatched managed command fails closed", func(t *testing.T) {
		other := *poolVMTestSpec("22222222-1111-4111-8111-111111111111", "host-a")
		other.Revision = 1
		for name, managed := range map[string]protocol.Command{
			"vm-media":   {OperationID: "media-op", Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: "11111111-1111-4111-8111-111111111111", Action: "eject"}},
			"foreign VM": {OperationID: "media-op", Kind: "vm-update", VM: &other},
		} {
			s, vm, media := setup(t, managed, "succeeded", "defined", false)
			got := confirmPoolVMUpdateOutcomes(context.Background(), s, []protocol.ManagedOutcome{{OperationID: media.OperationID, Kind: "vm-update", ID: vm.ID, Status: "succeeded"}}, sharedStorage)
			if got[0].Status != "needs_attention" || s.State.Journal[media.OperationID].Status != "needs_attention" {
				t.Fatalf("%s: unmatched managed command was confirmed: %+v", name, got)
			}
			if got[0].Kind != "vm-media" {
				t.Fatalf("%s: unmatched media outcome must stay on the Controller's vm-media task: %+v", name, got)
			}
		}
	})
}

func mustLoadPoolVMs(t *testing.T, s *Store) poolVMState {
	t.Helper()
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// BUG-0273: a shared VM whose guest put itself to sleep (virsh domstate pmsuspended) made
// every pool VM inventory publication fail, which refused every VM operation on the host.
// The sleeping domain is now observed as its running owner, and a Start of it is admitted
// as a wake: no start permit, no epoch change, the master record stays running.
func TestSharedVMAsleepIsAdmittedAsWake(t *testing.T) {
	ctx := context.Background()
	s, vm := sharedMediaVMStore(t)
	virsh := filepath.Join(strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0], "virsh")
	if err := os.WriteFile(virsh, []byte("#!/bin/sh\ncase \"$*\" in\n  *domstate*) printf '%s\\n' \"$NEX_TEST_STATE\" ;;\n  *) printf '%s\\n' '"+vm.ID+"' ;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	permit, epoch := "", uint64(0)
	if err := poolVMLocal(s, vm.ID, "start", &permit, false, nil, &epoch); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocal(s, vm.ID, "running", nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEX_TEST_STATE", "pmsuspended")
	if err := PublishPoolVMInventory(ctx, s); err != nil {
		t.Fatalf("a sleeping guest blocked pool inventory: %v", err)
	}
	start := protocol.Command{OperationID: "wake-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "start"}}
	finish, err := AdmitPoolVMExecution(ctx, s, start)
	if err != nil || finish == nil {
		t.Fatalf("start of a sleeping guest was refused: %v", err)
	}
	if record := mustLoadPoolVMs(t, s).VMs[vm.ID]; record.State != "running" || record.Epoch != epoch {
		t.Fatalf("wake changed the durable owner record: %+v", record)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.dir, "pool-vm-permits")); len(entries) != 0 {
		t.Fatal("wake wrote a start permit, which would admit a second execution")
	}
	if err := finish(protocol.Result{OperationID: "wake-1", Status: "succeeded"}, nil); err != nil {
		t.Fatal(err)
	}
	if record := mustLoadPoolVMs(t, s).VMs[vm.ID]; record.State != "running" || record.Epoch != epoch {
		t.Fatalf("wake completion changed the durable owner record: %+v", record)
	}
}
