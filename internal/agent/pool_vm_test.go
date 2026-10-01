// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// placementPeer supplies the selected member's authenticated native-preflight
// endpoint for owner-record tests. The endpoint echoes only the exact request;
// native validation itself is covered by placement_test and vmhost tests.
func placementPeer(t *testing.T, master, member *Store) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := AuthenticatePoolMaster(member, r); err != nil {
			http.Error(w, "master rejected", http.StatusUnauthorized)
			return
		}
		var request poolPlacementRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Command.VM == nil {
			http.Error(w, "bad placement", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(poolPlacementReply{Generation: request.Generation, OperationID: request.OperationID, VMID: request.Command.VM.ID, VM: *request.Command.VM, Accepted: true})
	}))
	config, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	if err = AdvertisePoolPeerOrigin(context.Background(), member, server.URL); err != nil {
		t.Fatal(err)
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

// autostartFixture writes one local-storage and one shared-storage managed VM, each with the
// requested autostart intent, into a real managed journal this host's own projection reads.
func autostartFixture(t *testing.T, s *Store, localAutostart, sharedAutostart bool) (local, shared protocol.VMCreateSpec) {
	t.Helper()
	t.Cleanup(vmhost.ManagedStorageReadyForTests())
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) {
		t.Helper()
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	localRepo := protocol.StorageRegistration{ID: "local-1", PoolID: s.State.PoolID, HostID: s.State.HostID, Name: "local", Path: t.TempDir(), Kind: protocol.StorageKindLocal, Generation: 1}
	sharedRepo := protocol.StorageRegistration{ID: "repo-1", PoolID: s.State.PoolID, HostID: s.State.HostID, Name: "shared", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports", Directory: "guests"}}, Generation: 1}
	for _, repo := range []protocol.StorageRegistration{localRepo, sharedRepo} {
		write("register-"+repo.ID, map[string]any{
			"command":         protocol.Command{OperationID: "register-" + repo.ID, Kind: "storage-register", Storage: &repo},
			"result":          protocol.Result{OperationID: "register-" + repo.ID, Status: "succeeded"},
			"applied_storage": repo,
		})
	}
	local = *poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	local.PoolID, local.Name, local.Autostart = s.State.PoolID, "Local guest", localAutostart
	local.Disks[0].StorageID = localRepo.ID
	shared = *poolVMTestSpec("22222222-2222-4222-8222-222222222222", s.State.HostID)
	shared.PoolID, shared.Autostart = s.State.PoolID, sharedAutostart
	shared.Disks[0].StorageID = sharedRepo.ID
	for _, vm := range []protocol.VMCreateSpec{local, shared} {
		write("create-"+vm.ID, map[string]any{
			"command": protocol.Command{OperationID: "create-" + vm.ID, Kind: "vm-create", VM: &vm},
			"result":  protocol.Result{OperationID: "create-" + vm.ID, Status: "succeeded"},
		})
	}
	return local, shared
}

// autostartVirsh stands in for libvirt: both fixture domains are defined and shut off. It
// records each invocation without the leading --connect pair, so a test can read back exactly
// which guest Core asked libvirt to start rather than which flag it set.
func autostartVirsh(t *testing.T, ids ...string) func() []string {
	return autostartVirshWithFlag(t, "Autostart: disable", ids...)
}

func autostartVirshWithFlag(t *testing.T, dominfo string, ids ...string) func() []string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "virsh.log")
	script := "#!/bin/sh\nshift 2\nprintf '%s\\n' \"$*\" >> " + log + "\ncase \"$1\" in\n  list) printf '%s\\n' " + strings.Join(ids, " ") + " ;;\n  domstate) printf '%s\\n' \"${NEX_TEST_STATE:-shut off}\" ;;\n  dominfo) printf '%s\\n' '" + dominfo + "' ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(log)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func autostartAttempted(t *testing.T, s *Store, vmID, bootID string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(s.dir, "pool-vm-autostart-attempts", vmID+"-"+bootID+".json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

// Local-storage autostart must use the same durable owner admission as API starts;
// a pending recovery blocks it before the native start reaches libvirt.
func TestAutostartPassRequiresOwnerAdmissionForLocalStorageGuests(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	local, shared := autostartFixture(t, s, true, false)
	virshLog := autostartVirsh(t, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	record := poolVMRecord{PoolID: s.State.PoolID, VMID: local.ID, HostID: s.State.HostID, State: "stopped", Epoch: 1, Spec: local}
	reservation, err := poolVMRecoveryReservationFor(record, "recovery-1", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		t.Fatal(err)
	}
	descriptor := snapshotDescriptorForTest(reservation, "before-upgrade", "")
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(local)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Dependencies = &dependencies
	record.PendingSnapshot = &descriptor
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{local.ID: record}}); err != nil {
		t.Fatal(err)
	}
	const boot = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	setPoolVMAutostartBootID(t, boot)

	if reason := ReconcilePoolVMAutostart(context.Background(), s); reason != "" {
		t.Fatalf("the autostart pass did not run: %q", reason)
	}
	if !autostartAttempted(t, s, local.ID, boot) {
		t.Fatal("the pass skipped a local-storage autostart guest, so a reboot leaves it shut off")
	}
	if slices.Contains(virshLog(), "start "+local.ID) {
		t.Fatal("a local-storage autostart crossed pending recovery authority")
	}
	if autostartAttempted(t, s, shared.ID, boot) {
		t.Fatal("the pass claimed a boot attempt for a guest that did not ask for autostart")
	}
	// Autostart is an on-boot intent, not a keepalive policy: the same boot claims once.
	if reason := ReconcilePoolVMAutostart(context.Background(), s); reason != "" {
		t.Fatalf("the second pass did not run: %q", reason)
	}
	if slices.Contains(virshLog(), "start "+local.ID) {
		t.Fatal("a second autostart pass bypassed the owner recovery fence")
	}
}

func TestAutostartPassSkipsVMWhenNativeSuppressionIsUnconfirmed(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	local, shared := autostartFixture(t, s, true, false)
	if err := poolVMLocal(s, local.ID, "define", nil, true, &local, nil); err != nil {
		t.Fatal(err)
	}
	virshLog := autostartVirshWithFlag(t, "Autostart: enable", local.ID, shared.ID)
	const boot = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	setPoolVMAutostartBootID(t, boot)
	if reason := ReconcilePoolVMAutostart(context.Background(), s); reason != "" {
		t.Fatalf("the autostart pass did not run: %q", reason)
	}
	if slices.Contains(virshLog(), "start "+local.ID) {
		t.Fatal("Core started a VM whose native autostart suppression was not confirmed")
	}
	if autostartAttempted(t, s, local.ID, boot) {
		t.Fatal("failed suppression spent the VM's boot attempt")
	}
}

func TestPoolAdmissionDoesNotClassifyUnmanagedAppliancePowerAsPoolVM(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	command := protocol.Command{OperationID: "appliance-start", Kind: "vm-power", Power: &protocol.VMPowerRequest{
		VMID: "33333333-3333-4333-8333-333333333333", PoolID: s.State.PoolID, HostID: s.State.HostID, Action: "start",
	}}
	if _, err := AdmitPoolVMExecution(context.Background(), s, command); err != nil {
		t.Fatalf("unmanaged appliance power was sent through pool VM owner admission: %v", err)
	}
}

func TestLocalStorageStartUsesRecoveryAdmissionWhileEmergencyStopSkipsMaster(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	local, shared := autostartFixture(t, s, false, false)
	virshLog := autostartVirsh(t, local.ID, shared.ID)
	record := poolVMRecord{PoolID: s.State.PoolID, VMID: local.ID, HostID: s.State.HostID, State: "stopped", Epoch: 2, Spec: local}
	reservation, err := poolVMRecoveryReservationFor(record, "snapshot-1", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePoolVMRecovery(&record, reservation); err != nil {
		t.Fatal(err)
	}
	descriptor := snapshotDescriptorForTest(reservation, "before-upgrade", "")
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(local)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Dependencies = &dependencies
	record.PendingSnapshot = &descriptor
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{local.ID: record}}); err != nil {
		t.Fatal(err)
	}
	start := protocol.Command{OperationID: "start-1", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: local.ID, PoolID: s.State.PoolID, HostID: s.State.HostID, Action: "start"}}
	if _, err := AdmitPoolVMExecution(context.Background(), s, start); err == nil {
		t.Fatalf("local-storage start bypassed pending recovery admission: %v", err)
	}
	if slices.Contains(virshLog(), "start "+local.ID) {
		t.Fatal("pending recovery reached native VM start")
	}
	// The same host must still be able to reduce power when the master is gone.
	maintenanceMembership(t, s, "host-b",
		maintenanceMember("host-a", false),
		protocol.PoolMember{HostID: "host-b", Hostname: "host-b", CertificateSHA256: strings.Repeat("b", 64), Generation: 4, Status: "ready", PeerOrigin: "https://127.0.0.1:1"},
	)
	stop := start
	stop.OperationID, stop.Power.Action = "stop-1", "force-stop"
	if _, err := AdmitPoolVMExecution(context.Background(), s, stop); err != nil {
		t.Fatalf("emergency stop depended on an unreachable master: %v", err)
	}
}

// The window this must never reopen is BUG-0381's: while the host is drained or has not
// confirmed this boot's pool authority, no attempt is made and no boot claim is spent, so the
// attempt is still available once the restriction clears.
func TestAutostartPassMakesNoLocalAttemptWhileDrainedOrRestricted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(*testing.T, *Store)
		reason  string
	}{
		{"drained", func(t *testing.T, s *Store) {
			maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", true))
		}, "maintenance mode"},
		{"restricted", func(t *testing.T, s *Store) {
			maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
			poolPeerMu.Lock()
			defer poolPeerMu.Unlock()
			state, err := loadPoolPeer(s)
			if err != nil {
				t.Fatal(err)
			}
			state.Restricted = &poolRestrictedRecord{Kind: poolRestrictedUnconfirmed, Reason: poolUnconfirmedReason, Since: time.Now().UTC()}
			if err = savePoolPeer(s, state); err != nil {
				t.Fatal(err)
			}
		}, "restricted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := peerStore(t, "host-a", "pool-1")
			tc.arrange(t, s)
			local, _ := autostartFixture(t, s, true, false)
			virshLog := autostartVirsh(t, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
			const boot = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			setPoolVMAutostartBootID(t, boot)

			if reason := ReconcilePoolVMAutostart(context.Background(), s); !strings.Contains(reason, tc.reason) {
				t.Fatalf("the pass reported %q", reason)
			}
			if autostartAttempted(t, s, local.ID, boot) {
				t.Fatal("a local-storage guest's boot attempt was spent while the host could not start it")
			}
			if slices.Contains(virshLog(), "start "+local.ID) {
				t.Fatalf("a guest was started on a host that must start none: %q", virshLog())
			}
		})
	}
}

func TestPoolVMOnSharedStorageSeparatesLocalFromShared(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	local, shared := autostartFixture(t, s, true, true)
	autostartVirsh(t, "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(context.Background())
	if !managedVMChecksReady(checks) {
		t.Fatalf("managed observations are not ready: %#v", checks)
	}
	if poolVMOnSharedStorage(managed, local) {
		t.Fatal("a guest on a local repository was classed as shared, so it would wait for a master permit")
	}
	if !poolVMOnSharedStorage(managed, shared) {
		t.Fatal("a guest on an NFS repository was classed as local, so it would start with no master admission")
	}
}

func TestPoolVMExecutionRetainsOwnerAcrossStopAndRejectsSecondHost(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	const vm = "11111111-1111-4111-8111-111111111111"
	if err := poolVMLocal(s, vm, "define", nil, true, poolVMTestSpec(vm, "host-a"), nil); err != nil {
		t.Fatal(err)
	}
	permit := ""
	epoch := uint64(0)
	if err := poolVMLocalBound(s, vm, "start", "start-1", 0, &permit, false, nil, &epoch); err != nil || len(permit) != 64 || epoch != 2 {
		t.Fatalf("first start = %q epoch %d, %v", permit, epoch, err)
	}
	if err := poolVMLocalBound(s, vm, "start", "start-2", 0, nil, false, nil, nil); err == nil {
		t.Fatal("second start admitted while first is unresolved")
	}
	if err := poolVMLocalBound(s, vm, "start", "start-1", 0, nil, false, nil, nil); err == nil {
		t.Fatal("replayed start admission issued a second permit while the first outcome was unresolved")
	}
	if err := poolVMLocalBound(s, vm, "stopped", "start-1", epoch, nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	// Simulate a stale second Core reaching the same durable master state.  A confirmed
	// stop changes execution state only; it never transfers placement to a new host.
	s.State.HostID = "host-b"
	if err := poolVMLocal(s, vm, "define", nil, true, poolVMTestSpec(vm, "host-b"), nil); err == nil {
		t.Fatal("second host claimed stopped VM")
	}
	if err := poolVMLocalBound(s, vm, "start", "start-3", 0, nil, false, nil, nil); err == nil {
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
	if err := poolVMLocalBound(s, vm, "start", "start-4", 0, nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocalBound(s, vm, "running", "start-4", 2, nil, false, nil, nil); err != nil {
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
	placementPeer(t, master, member)
	const vm = "11111111-1111-4111-8111-111111111111"
	spec := poolVMTestSpec(vm, member.State.HostID)
	if err = poolVMDefineRequestPeer(context.Background(), member, *spec, "create-recovery-peer"); err != nil {
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

func TestRestorePlacementPeerReleaseRequiresExactOwner(t *testing.T) {
	lab := newTransferLab(t, 2)
	master, owner, other := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", owner.State.HostID)
	op := "restore-peer-release"
	run := protocol.BackupRun{OperationID: op, Kind: protocol.BackupKindRestoreNew, PoolID: spec.PoolID,
		HostID: spec.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: spec.ID,
		IntentSHA256: strings.Repeat("a", 64), Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	if _, err := ReservePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{spec.ID: {
		PoolID: spec.PoolID, VMID: spec.ID, HostID: spec.HostID, CreateOperationID: op,
		State: "defined", Epoch: 1, Spec: spec,
	}}}); err != nil {
		t.Fatal(err)
	}
	send := func(peer *Store, request poolVMRequest) int {
		t.Helper()
		client, err := PoolPeerClient(peer, lab.origin[master.State.HostID])
		if err != nil {
			t.Fatal(err)
		}
		defer client.CloseIdleConnections()
		body, _ := json.Marshal(request)
		httpRequest, err := http.NewRequest(http.MethodPost, lab.origin[master.State.HostID]+poolVMPath+spec.ID, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		httpRequest.Header.Set("Content-Type", "application/json")
		response, err := client.Do(httpRequest)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	request := poolVMRequest{VMID: spec.ID, Action: "release-restore-placement", OperationID: op, VM: &spec, PlacementAbsent: true}
	withoutProof := request
	withoutProof.PlacementAbsent = false
	if got := send(owner, withoutProof); got != http.StatusBadRequest {
		t.Fatalf("release without absence proof = %d", got)
	}
	if got := send(other, request); got != http.StatusBadRequest {
		t.Fatalf("different member released owner = %d", got)
	}
	changed := spec
	changed.Name = "different guest"
	wrongSpec := request
	wrongSpec.VM = &changed
	if got := send(owner, wrongSpec); got != http.StatusConflict {
		t.Fatalf("changed spec released owner = %d", got)
	}
	wrongOperation := request
	wrongOperation.OperationID = "other-restore"
	if got := send(owner, wrongOperation); got != http.StatusConflict {
		t.Fatalf("different operation released owner = %d", got)
	}
	state, err := loadPoolVMs(master)
	if err != nil {
		t.Fatal(err)
	}
	legacy := state.VMs[spec.ID]
	legacy.CreateOperationID = ""
	state.VMs[spec.ID] = legacy
	if err := savePoolVMs(master, state); err != nil {
		t.Fatal(err)
	}
	if got := send(owner, request); got != http.StatusConflict {
		t.Fatalf("legacy owner without creator proof was released = %d", got)
	}
	legacy.CreateOperationID = op
	state.VMs[spec.ID] = legacy
	if err := savePoolVMs(master, state); err != nil {
		t.Fatal(err)
	}
	if got := send(owner, request); got != http.StatusOK {
		t.Fatalf("exact release = %d", got)
	}
	if got := send(owner, request); got != http.StatusOK {
		t.Fatalf("exact retry = %d", got)
	}
	state, err = loadPoolVMs(master)
	if err != nil || len(state.VMs) != 0 {
		t.Fatalf("owner survived proven release: %+v err=%v", state.VMs, err)
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
	placementPeer(t, master, member)
	const vm = "11111111-1111-4111-8111-111111111111"
	previous := poolVMTestSpec(vm, member.State.HostID)
	if err = poolVMDefineRequestPeer(context.Background(), member, *previous, "create-update-peer"); err != nil {
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
	placementPeer(t, master, member)
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
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", Hostname: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
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
	placementPeer(t, master, member)
	const vm = "11111111-1111-4111-8111-111111111111"
	previous := poolVMTestSpec(vm, member.State.HostID)
	if err = poolVMDefineRequestPeer(context.Background(), member, *previous, "create-ack-peer"); err != nil {
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

func TestPoolVMStartRejectsMalformedOrLegacyMasterAcknowledgement(t *testing.T) {
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
	const vmID = "11111111-1111-4111-8111-111111111111"
	var reply poolVMRequest
	joinHandler := PoolPeerHandler(master)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, poolVMPath) {
			if _, err := AuthenticatePoolPeer(master, r); err != nil {
				http.Error(w, "peer rejected", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(reply)
			return
		}
		joinHandler.ServeHTTP(w, r)
	}))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
		t.Fatal(err)
	}

	valid := poolVMRequest{VMID: vmID, Action: "start", OperationID: "start-remote", Permit: strings.Repeat("a", 64), Epoch: 4}
	for _, tc := range []struct {
		name  string
		reply poolVMRequest
	}{
		{name: "legacy empty 200", reply: poolVMRequest{}},
		{name: "wrong VM", reply: poolVMRequest{VMID: "22222222-2222-4222-8222-222222222222", Action: "start", Permit: valid.Permit, Epoch: valid.Epoch}},
		{name: "wrong action", reply: poolVMRequest{VMID: vmID, Action: "wake", Permit: valid.Permit, Epoch: valid.Epoch}},
		{name: "missing permit", reply: poolVMRequest{VMID: vmID, Action: "start", Epoch: valid.Epoch}},
		{name: "zero epoch", reply: poolVMRequest{VMID: vmID, Action: "start", Permit: valid.Permit}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply = tc.reply
			permit, epoch := "", uint64(0)
			if err := poolVMRequestPeerBound(context.Background(), member, vmID, "start", "start-remote", 0, &permit, false, nil, &epoch); err == nil {
				t.Fatalf("invalid start acknowledgement was accepted: %#v", tc.reply)
			}
		})
	}
	reply = valid
	permit, epoch := "", uint64(0)
	if err := poolVMRequestPeerBound(context.Background(), member, vmID, "start", "start-remote", 0, &permit, false, nil, &epoch); err != nil || permit != valid.Permit || epoch != valid.Epoch {
		t.Fatalf("valid start acknowledgement = permit %q epoch %d err %v", permit, epoch, err)
	}
}

func TestPoolVMDeleteAdmissionRefusesRetainedSnapshotsForLocalAndSharedVMs(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, deleteDisks := range []bool{false, true} {
			name := "local"
			if shared {
				name = "shared"
			}
			t.Run(name+"/delete_disks="+strconv.FormatBool(deleteDisks), func(t *testing.T) {
				s := peerStore(t, "host-a", "pool-1")
				maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
				local, sharedVM := autostartFixture(t, s, false, false)
				autostartVirsh(t, local.ID, sharedVM.ID)
				vm := local
				if shared {
					vm = sharedVM
				}
				if err := poolVMLocal(s, vm.ID, "define", nil, true, &vm, nil); err != nil {
					t.Fatal(err)
				}
				state, err := loadPoolVMs(s)
				if err != nil {
					t.Fatal(err)
				}
				reservation, err := poolVMRecoveryReservationFor(state.VMs[vm.ID], "retained-point", "snapshot-create", strings.Repeat("d", 64), 1)
				if err != nil {
					t.Fatal(err)
				}
				record := state.VMs[vm.ID]
				record.Snapshots = []protocol.PoolVMSnapshot{snapshotDescriptorForTest(reservation, "before-change", "")}
				state.VMs[vm.ID] = record
				wantCatalog := slices.Clone(record.Snapshots)
				if err = savePoolVMs(s, state); err != nil {
					t.Fatal(err)
				}
				command := protocol.Command{OperationID: "delete-retained", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{
					VMID: vm.ID, PoolID: s.State.PoolID, HostID: s.State.HostID, DeleteDisks: deleteDisks,
				}}
				if _, err := AdmitPoolVMExecution(context.Background(), s, command); err == nil || !strings.Contains(err.Error(), "retained snapshots") {
					t.Fatalf("delete passed the retained snapshot gate: %v", err)
				}
				state, err = loadPoolVMs(s)
				if err != nil || !reflect.DeepEqual(state.VMs[vm.ID].Snapshots, wantCatalog) || state.VMs[vm.ID].PendingRecovery != nil {
					t.Fatalf("refused delete changed the owner record or catalog: %#v (%v)", state.VMs[vm.ID], err)
				}
				if got := state.Inventory[s.State.HostID].Deleted; slices.Contains(got, vm.ID) {
					t.Fatalf("refused delete published a tombstone: %v", got)
				}
			})
		}
	}
}

func TestPoolVMDeleteAdmissionRefusesPendingSnapshotRecovery(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	local, shared := autostartFixture(t, s, false, false)
	autostartVirsh(t, local.ID, shared.ID)
	if err := poolVMLocal(s, local.ID, "define", nil, true, &local, nil); err != nil {
		t.Fatal(err)
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[local.ID]
	reservation, err := poolVMRecoveryReservationFor(record, "pending-snapshot", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := snapshotDescriptorForTest(reservation, "before-change", "")
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(record.Spec)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.Dependencies = &dependencies
	record.PendingRecovery, record.PendingSnapshot = &reservation, &descriptor
	state.VMs[local.ID] = record
	if err = savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: "delete-pending", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{
		VMID: local.ID, PoolID: s.State.PoolID, HostID: s.State.HostID,
	}}
	if _, err = AdmitPoolVMExecution(context.Background(), s, command); err == nil {
		t.Fatal("delete was admitted while snapshot recovery remained pending")
	}
	state, err = loadPoolVMs(s)
	if err != nil || state.VMs[local.ID].PendingRecovery == nil || *state.VMs[local.ID].PendingRecovery != reservation {
		t.Fatalf("refused delete cleared pending recovery: %#v (%v)", state.VMs[local.ID], err)
	}
}

func TestPoolVMDeleteCheckRejectsMalformedLegacyAndMismatchedAcknowledgements(t *testing.T) {
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
	var reply poolVMRequest
	joinHandler := PoolPeerHandler(master)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, poolVMPath) {
			joinHandler.ServeHTTP(w, r)
			return
		}
		if _, err := AuthenticatePoolPeer(master, r); err != nil {
			http.Error(w, "peer rejected", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
		t.Fatal(err)
	}
	const vmID = "11111111-1111-4111-8111-111111111111"
	vm := poolVMTestSpec(vmID, member.State.HostID)
	for _, invalid := range []poolVMRequest{
		{},
		{VMID: vmID, Action: "define", SpecMatch: true},
		{VMID: vmID, Action: "delete-check"},
		{VMID: "22222222-2222-4222-8222-222222222222", Action: "delete-check", SpecMatch: true},
	} {
		reply = invalid
		if err := poolVMRequestPeer(context.Background(), member, vmID, "delete-check", nil, false, vm, nil); err == nil {
			t.Fatalf("invalid delete acknowledgement accepted: %#v", invalid)
		}
	}
	reply = poolVMRequest{VMID: vmID, Action: "delete-check", SpecMatch: true}
	if err := poolVMRequestPeer(context.Background(), member, vmID, "delete-check", nil, false, vm, nil); err != nil {
		t.Fatalf("exact delete acknowledgement rejected: %v", err)
	}
}

func TestAuthenticatedPoolMasterRefusesDeleteWithRetainedSnapshot(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	const vmID = "11111111-1111-4111-8111-111111111111"
	spec := *poolVMTestSpec(vmID, member.State.HostID)
	state := poolVMState{VMs: map[string]poolVMRecord{}}
	record := poolVMRecord{PoolID: master.State.PoolID, VMID: vmID, HostID: member.State.HostID, State: "stopped", Epoch: 1, Spec: spec}
	reservation, err := poolVMRecoveryReservationFor(record, "snapshot-point-1", "snapshot-create", strings.Repeat("d", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	record.Snapshots = []protocol.PoolVMSnapshot{snapshotDescriptorForTest(reservation, "before-change", "")}
	state.VMs[vmID] = record
	if err = savePoolVMs(master, state); err != nil {
		t.Fatal(err)
	}
	if err = poolVMRequestPeer(context.Background(), member, vmID, "delete-check", nil, false, &spec, nil); err == nil || !strings.Contains(err.Error(), "retained snapshots") {
		t.Fatalf("authenticated master allowed delete with retained snapshot: %v", err)
	}
	state, err = loadPoolVMs(master)
	if err != nil || !reflect.DeepEqual(state.VMs[vmID].Snapshots, record.Snapshots) {
		t.Fatalf("master delete refusal changed the retained catalog: %#v (%v)", state.VMs[vmID], err)
	}
}

func TestAuthenticatedPoolMasterAllowsDeleteCheckOnlyWithoutSnapshotCatalog(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	const vmID = "11111111-1111-4111-8111-111111111111"
	spec := *poolVMTestSpec(vmID, member.State.HostID)
	record := poolVMRecord{PoolID: master.State.PoolID, VMID: vmID, HostID: member.State.HostID, State: "stopped", Epoch: 1, Spec: spec}
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vmID: record}}); err != nil {
		t.Fatal(err)
	}
	if err := poolVMRequestPeer(context.Background(), member, vmID, "delete-check", nil, false, &spec, nil); err != nil {
		t.Fatalf("authenticated master refused exact delete check with no snapshots: %v", err)
	}
}

func TestPoolVMInventoryTombstoneCannotErasePendingOrRetainedSnapshotState(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "retained-catalog"
		if pending {
			name = "pending-recovery"
		}
		t.Run(name, func(t *testing.T) {
			s := peerStore(t, "host-a", "pool-1")
			membership := maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
			const vmID = "11111111-1111-4111-8111-111111111111"
			spec := *poolVMTestSpec(vmID, s.State.HostID)
			record := poolVMRecord{PoolID: s.State.PoolID, VMID: vmID, HostID: s.State.HostID, State: "stopped", Epoch: 1, Spec: spec}
			reservation, err := poolVMRecoveryReservationFor(record, "snapshot-point-1", "snapshot-create", strings.Repeat("d", 64), 1)
			if err != nil {
				t.Fatal(err)
			}
			descriptor := snapshotDescriptorForTest(reservation, "before-change", "")
			if pending {
				record.PendingRecovery = &reservation
				record.PendingSnapshot = &descriptor
			} else {
				record.Snapshots = []protocol.PoolVMSnapshot{descriptor}
			}
			if err = savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vmID: record}}); err != nil {
				t.Fatal(err)
			}
			inv := poolVMInventory{HostID: s.State.HostID, Generation: membership.Generation, Deleted: []string{vmID}}
			if err = recordPoolVMInventory(s, membership, inv); err == nil || !strings.Contains(err.Error(), "snapshot state") {
				t.Fatalf("unsafe owner tombstone was accepted: %v", err)
			}
			state, loadErr := loadPoolVMs(s)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			got, ok := state.VMs[vmID]
			if !ok || !reflect.DeepEqual(got.Snapshots, record.Snapshots) || !reflect.DeepEqual(got.PendingRecovery, record.PendingRecovery) || !reflect.DeepEqual(got.PendingSnapshot, record.PendingSnapshot) {
				t.Fatalf("rejected tombstone erased snapshot state: %#v", got)
			}
			if accepted := state.Inventory[s.State.HostID]; slices.Contains(accepted.Deleted, vmID) {
				t.Fatalf("rejected tombstone was persisted: %#v", accepted)
			}
		})
	}
}

func TestPoolVMDeleteCheckRefusesFrozenAuthorityWithoutBlockingEmergencyStop(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(s)
	if err != nil {
		poolPeerMu.Unlock()
		t.Fatal(err)
	}
	peer.Transfer = &poolTransferRecord{Frozen: true}
	err = savePoolPeer(s, peer)
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := requirePoolVMAuthorityWritable(s, "delete-check", false); err == nil {
		t.Fatal("snapshot delete-check was allowed after authority freeze")
	}
	if err := requirePoolVMAuthorityWritable(s, "stop", false); err != nil {
		t.Fatalf("emergency stop was blocked by authority freeze: %v", err)
	}
}

func TestReservePoolVMSnapshotRequiresExactAuthenticatedMasterAcknowledgement(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	for _, store := range []*Store{master, member} {
		if err := EnsurePoolMembership(store); err != nil {
			t.Fatal(err)
		}
	}
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	masterHandler := PoolPeerHandler(master)
	var loseSettlementReply atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, poolVMPath) && loseSettlementReply.CompareAndSwap(true, false) {
			recorded := httptest.NewRecorder()
			masterHandler.ServeHTTP(recorded, r)
			http.Error(w, "snapshot settlement acknowledgement was lost", http.StatusServiceUnavailable)
			return
		}
		masterHandler.ServeHTTP(w, r)
	}))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
		t.Fatal(err)
	}
	memberConfig, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	memberHandler := PoolPeerHandler(member)
	var loseReplicaReply atomic.Bool
	memberServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == poolReplicaPath && loseReplicaReply.CompareAndSwap(true, false) {
			recorded := httptest.NewRecorder()
			memberHandler.ServeHTTP(recorded, r)
			http.Error(w, "replica acknowledgement was lost", http.StatusServiceUnavailable)
			return
		}
		memberHandler.ServeHTTP(w, r)
	}))
	memberServer.TLS = memberConfig
	memberServer.StartTLS()
	t.Cleanup(memberServer.Close)
	setMemberPeerOrigin(t, master, member.State.HostID, memberServer.URL)
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", member.State.HostID)
	record := poolVMRecord{PoolID: master.State.PoolID, VMID: spec.ID, HostID: member.State.HostID, Epoch: 7, State: "stopped", Spec: spec}
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{spec.ID: record}}); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: "snapshot_member_1", Kind: "snapshot-create", VM: &spec, Snapshot: &protocol.VMSnapshotRequest{Name: "before-upgrade"}}
	createdAt := time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC)
	loseReplicaReply.Store(true)
	if _, err := ReservePoolVMSnapshot(context.Background(), member, command, 1, strings.Repeat("e", 64), createdAt); err == nil {
		t.Fatal("snapshot reservation returned after the exact member commit acknowledgement was lost")
	}
	state, err := loadPoolVMs(master)
	if err != nil || state.VMs[spec.ID].PendingRecovery == nil || state.VMs[spec.ID].PendingRecovery.OperationID != command.OperationID {
		t.Fatalf("lost reservation ACK did not retain the exact durable master reservation: %#v (%v)", state.VMs[spec.ID], err)
	}
	reservation, err := ReservePoolVMSnapshot(context.Background(), member, command, 1, strings.Repeat("e", 64), createdAt)
	if err != nil {
		t.Fatal(err)
	}
	reservedReplica := storedReplica(t, member)
	var reservedVMs map[string]poolVMRecord
	if reservedReplica == nil || json.Unmarshal(reservedReplica.Replica.VMs, &reservedVMs) != nil ||
		reservedVMs[spec.ID].PendingRecovery == nil || reservedVMs[spec.ID].PendingRecovery.OperationID != command.OperationID {
		t.Fatalf("snapshot reservation returned before its exact authority replica was stored by the owner: replica=%+v vmstate=%+v", reservedReplica, reservedVMs)
	}
	if reservation.OwnerHostID != member.State.HostID || reservation.PoolID != master.State.PoolID || reservation.IntentSHA256 != strings.Repeat("e", 64) {
		t.Fatalf("member received a mismatched master reservation: %#v", reservation)
	}
	state, err = loadPoolVMs(master)
	if err != nil || state.VMs[spec.ID].PendingRecovery == nil || *state.VMs[spec.ID].PendingRecovery != reservation {
		t.Fatalf("master did not persist member reservation: %#v (%v)", state.VMs[spec.ID], err)
	}
	record = state.VMs[spec.ID]
	descriptor := *record.PendingSnapshot
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("f", 64), Snapshot: &descriptor}
	masterPeer, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	masterPeer.Transfer = &poolTransferRecord{Frozen: true}
	if err := savePoolPeer(master, masterPeer); err != nil {
		t.Fatal(err)
	}
	if err := poolVMSnapshotSettlementPeer(context.Background(), member, evidence); err == nil {
		t.Fatal("snapshot settlement mutated a frozen pool authority")
	}
	state, err = loadPoolVMs(master)
	if err != nil || state.VMs[spec.ID].PendingRecovery == nil {
		t.Fatalf("frozen settlement did not retain its pending reservation: %#v (%v)", state.VMs[spec.ID], err)
	}
	masterPeer.Transfer = nil
	if err := savePoolPeer(master, masterPeer); err != nil {
		t.Fatal(err)
	}
	changed := evidence
	changedDescriptor := descriptor
	changedDescriptor.Name = "different-name"
	changed.Snapshot = &changedDescriptor
	if err := poolVMSnapshotSettlementPeer(context.Background(), member, changed); err == nil {
		t.Fatal("master accepted terminal evidence for a different snapshot descriptor")
	}
	loseSettlementReply.Store(true)
	if err := poolVMSnapshotSettlementPeer(context.Background(), member, evidence); err == nil {
		t.Fatal("owner observed a settlement success after the HTTP acknowledgement was lost")
	}
	settledReplica := storedReplica(t, member)
	var settledVMs map[string]poolVMRecord
	if settledReplica == nil || json.Unmarshal(settledReplica.Replica.VMs, &settledVMs) != nil ||
		settledVMs[spec.ID].LastSettledRecovery == nil || !reflect.DeepEqual(*settledVMs[spec.ID].LastSettledRecovery, evidence) {
		t.Fatal("lost settlement response preceded exact replica acknowledgement")
	}
	if err := poolVMSnapshotSettlementPeer(context.Background(), member, evidence); err != nil {
		t.Fatalf("master did not acknowledge the exact terminal evidence: %v", err)
	}
	if err := poolVMSnapshotSettlementPeer(context.Background(), member, evidence); err != nil {
		t.Fatalf("exact settlement replay after a lost acknowledgement was not idempotent: %v", err)
	}
	settledReplica = storedReplica(t, member)
	settledVMs = nil
	if settledReplica == nil || json.Unmarshal(settledReplica.Replica.VMs, &settledVMs) != nil ||
		settledVMs[spec.ID].LastSettledRecovery == nil || !reflect.DeepEqual(*settledVMs[spec.ID].LastSettledRecovery, evidence) {
		t.Fatal("snapshot settlement returned before its exact terminal receipt was stored by the owner")
	}
	state, err = loadPoolVMs(master)
	if err != nil || state.VMs[spec.ID].LastSettledRecovery == nil || !reflect.DeepEqual(*state.VMs[spec.ID].LastSettledRecovery, evidence) {
		t.Fatalf("master did not durably store the exact settlement: %#v (%v)", state.VMs[spec.ID], err)
	}
	listed, err := ListPoolVMSnapshots(context.Background(), member, spec.ID)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], descriptor) {
		t.Fatalf("member list did not read the master catalog: %#v (%v)", listed, err)
	}
}

func TestSnapshotReservationAcknowledgementBindsOwnerOrderAndDenial(t *testing.T) {
	spec := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	request := poolVMRequest{
		VMID: spec.ID, Action: "snapshot-reserve", OperationID: "snapshot_ack_1", VM: &spec,
		Snapshot:          &protocol.VMSnapshotRequest{Name: "before-upgrade"},
		SnapshotCreatedAt: time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC),
		IntentSHA256:      strings.Repeat("a", 64), OwnerOperationOrder: 9,
	}
	record := poolVMRecord{PoolID: "pool-1", VMID: spec.ID, HostID: "host-a", Epoch: 4, Spec: spec}
	accepted, err := poolVMRecoveryReservationFor(record, request.OperationID, "snapshot-create", request.IntentSHA256, request.OwnerOperationOrder)
	if err != nil {
		t.Fatal(err)
	}
	ack := snapshotReservationAck(request, accepted)
	if !validPoolVMSnapshotReservationAck(ack, request, "pool-1", "host-a") {
		t.Fatal("exact accepted reservation acknowledgement was rejected")
	}

	changed := ack
	changedReservation := *ack.SnapshotReservation
	changedReservation.OwnerOperationOrder++
	changed.SnapshotReservation = &changedReservation
	if validPoolVMSnapshotReservationAck(changed, request, "pool-1", "host-a") {
		t.Fatal("acknowledgement with a different owner journal order was accepted")
	}

	denied := accepted
	denied.AdmissionRefusal = "name-in-use"
	denialAck := snapshotReservationAck(request, denied)
	if !validPoolVMSnapshotReservationAck(denialAck, request, "pool-1", "host-a") {
		t.Fatal("exact definitive denial acknowledgement was rejected")
	}
	denialAck.SnapshotAccepted = true
	if validPoolVMSnapshotReservationAck(denialAck, request, "pool-1", "host-a") {
		t.Fatal("denial acknowledgement also authorized snapshot creation")
	}
	denied.AdmissionRefusal = "storage-unavailable"
	denialAck = snapshotReservationAck(request, denied)
	if validPoolVMSnapshotReservationAck(denialAck, request, "pool-1", "host-a") {
		t.Fatal("unsupported refusal code was accepted")
	}

	zeroOrder := request
	zeroOrder.OwnerOperationOrder = 0
	if validPoolVMSnapshotReservationAck(ack, zeroOrder, "pool-1", "host-a") {
		t.Fatal("snapshot acknowledgement without an owner order was accepted")
	}
}

func TestSingleHostPoolVMLocalFenceUsesDurableOwnerAndPermit(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	const vm = "11111111-1111-4111-8111-111111111111"
	if err := poolVMLocal(s, vm, "define", nil, true, poolVMTestSpec(vm, "host-a"), nil); err != nil {
		t.Fatal(err)
	}
	permit := ""
	if err := poolVMRequestPeerBound(context.Background(), s, vm, "start", "single-host-start", 0, &permit, false, nil, nil); err != nil || len(permit) != 64 {
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

func TestSharedVMCreateRejectsUnmountedNativeStoragePreflight(t *testing.T) {
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
	if _, err = AdmitPoolVMExecution(context.Background(), s, protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: vm}); err == nil || !strings.Contains(err.Error(), "storage") {
		t.Fatalf("unmounted shared storage passed native placement preflight: %v", err)
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
	if err := poolVMLocalBound(s, vm.ID, "start", "start-before-update", 0, nil, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocalBound(s, vm.ID, "running", "start-before-update", 2, nil, false, nil, nil); err != nil {
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
	if err := savePoolPeer(s, poolPeerState{Membership: protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 1, Members: []protocol.PoolMember{{HostID: "host-a", Hostname: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}}); err != nil {
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
	// This fixture intentionally has no mountable NFS export. Model the exact
	// interrupted update replay so native placement does not retry a side effect;
	// the master must retain the matching durable reservation.
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record := state.VMs[vm.ID]
	if err = reservePoolVMUpdate(&record, s.State.HostID, command.OperationID, *update.VM); err != nil {
		t.Fatal(err)
	}
	state.VMs[vm.ID] = record
	if err = savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"command": update, "result": protocol.Result{OperationID: update.OperationID, Status: "needs_attention"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.dir, "managed-vms", update.OperationID+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	finish, err := AdmitPoolVMExecution(context.Background(), s, update)
	if err != nil || finish == nil {
		t.Fatalf("durable media transition refused: %v", err)
	}
	state, err = loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	record = state.VMs[vm.ID]
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
	changed := sharedMediaCommand(vm, "media-1", "insert", "media-1")
	if got := PoolVMMediaCommand(context.Background(), s, changed); !reflect.DeepEqual(got, changed) {
		t.Fatalf("changed media payload reused a different journaled transition: %+v", got)
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
	if err := poolVMLocalBound(s, vm.ID, "start", "wake-setup", 0, &permit, false, nil, &epoch); err != nil {
		t.Fatal(err)
	}
	if err := poolVMLocalBound(s, vm.ID, "running", "wake-setup", epoch, nil, false, nil, nil); err != nil {
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

func TestFailedStartPreflightReleasesExactPoolAdmission(t *testing.T) {
	ctx := context.Background()
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	local, _ := autostartFixture(t, s, false, false)
	autostartVirsh(t, local.ID, "22222222-2222-4222-8222-222222222222")
	if err := poolVMLocal(s, local.ID, "define", nil, true, &local, nil); err != nil {
		t.Fatal(err)
	}
	command := protocol.Command{OperationID: "start-memory-preflight", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: local.ID, PoolID: local.PoolID, HostID: local.HostID, Action: "start"}}
	if applied, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx); !managedVMChecksReady(checks) {
		t.Fatalf("fixture managed state unavailable: %#v %#v", applied, checks)
	}
	finish, err := AdmitPoolVMExecution(ctx, s, command)
	if err != nil || finish == nil {
		t.Fatalf("start admission failed: %v", err)
	}
	if record := mustLoadPoolVMs(t, s).VMs[local.ID]; record.State != "starting" {
		t.Fatalf("start admission state = %+v", record)
	}
	if err := finish(protocol.Result{OperationID: command.OperationID, Status: "failed", Error: "insufficient available memory"}, nil); err != nil {
		t.Fatal(err)
	}
	if record := mustLoadPoolVMs(t, s).VMs[local.ID]; record.State != "stopped" || record.PendingStartOperationID != "" {
		t.Fatalf("definite preflight failure left a shutoff VM reserved as starting: %+v", record)
	}
}

func TestAutomaticCreateStartCompletionRequiresDefinitiveFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		start     string
		outcome   string
		observed  string
		wantState string
		pending   bool
	}{
		{name: "preflight failure", start: "insufficient available memory to start this VM including 2 GiB host reserve", outcome: "failed-before-request", observed: "shut off", wantState: "stopped"},
		{name: "request outcome uncertain", start: "libvirt did not confirm the start request; inspect the VM on its host", outcome: "uncertain", observed: "shut off", wantState: "starting", pending: true},
		{name: "interrupted start", start: "interrupted before the automatic start completed", outcome: "uncertain", observed: "shut off", wantState: "starting", pending: true},
		{name: "legacy reason", start: "libvirt refused the start request; inspect the VM on its host", observed: "shut off", wantState: "starting", pending: true},
		{name: "uncertain but currently running", start: "libvirt did not confirm the start request; inspect the VM on its host", outcome: "uncertain", observed: "running", wantState: "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := peerStore(t, "host-a", "pool-1")
			if err := EnsurePoolMembership(s); err != nil {
				t.Fatal(err)
			}
			local, shared := autostartFixture(t, s, false, false)
			local.StartAfterCreate = true
			t.Setenv("NEX_TEST_STATE", tc.observed)
			autostartVirsh(t, local.ID, shared.ID)
			command := protocol.Command{OperationID: "create-" + local.ID, Kind: "vm-create", VM: &local}
			journalPath := filepath.Join(s.dir, "managed-vms", command.OperationID+".json")
			journal, err := json.Marshal(map[string]any{
				"command":       command,
				"result":        protocol.Result{OperationID: command.OperationID, Status: "succeeded"},
				"stage":         "started",
				"start":         "started",
				"start_outcome": "started",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(journalPath, journal, 0600); err != nil {
				t.Fatal(err)
			}
			if err := poolVMLocal(s, local.ID, "define", nil, true, &local, nil); err != nil {
				t.Fatal(err)
			}
			finish, err := AdmitPoolVMExecution(context.Background(), s, command)
			if err != nil || finish == nil {
				t.Fatalf("automatic create-start admission: %v", err)
			}
			journal, err = json.Marshal(map[string]any{
				"command":       command,
				"result":        protocol.Result{OperationID: command.OperationID, Status: "succeeded"},
				"stage":         "started",
				"start":         tc.start,
				"start_outcome": tc.outcome,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(journalPath, journal, 0600); err != nil {
				t.Fatal(err)
			}
			if err := finish(protocol.Result{OperationID: command.OperationID, Status: "succeeded"}, nil); err != nil {
				t.Fatalf("automatic create-start completion: %v", err)
			}
			record := mustLoadPoolVMs(t, s).VMs[local.ID]
			if record.State != tc.wantState || (record.PendingStartOperationID != "") != tc.pending {
				t.Fatalf("automatic start completion = %+v, want state %s pending=%v", record, tc.wantState, tc.pending)
			}
		})
	}
}

func TestPoolVMStartTransitionRequiresExactCurrentOperationAndEpoch(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	const vmID = "11111111-1111-4111-8111-111111111111"
	if err := poolVMLocal(s, vmID, "define", nil, true, poolVMTestSpec(vmID, "host-a"), nil); err != nil {
		t.Fatal(err)
	}
	permit, epoch := "", uint64(0)
	if err := poolVMLocalBound(s, vmID, "start", "start-current", 0, &permit, false, nil, &epoch); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		op    string
		epoch uint64
	}{
		{name: "stale operation", op: "start-old", epoch: epoch},
		{name: "stale epoch", op: "start-current", epoch: epoch - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := poolVMLocalBound(s, vmID, "stopped", tc.op, tc.epoch, nil, false, nil, nil); err == nil {
				t.Fatal("stale start outcome cleared the current admission")
			}
			pending := mustLoadPoolVMs(t, s).VMs[vmID]
			if pending.State != "starting" || pending.PendingStartOperationID != "start-current" || pending.Epoch != epoch {
				t.Fatalf("stale outcome changed current start: %+v", pending)
			}
		})
	}
	if err := poolVMLocalBound(s, vmID, "running", "start-current", epoch, nil, false, nil, nil); err != nil {
		t.Fatalf("exact successful start transition: %v", err)
	}
	settled := mustLoadPoolVMs(t, s).VMs[vmID]
	if settled.State != "running" || settled.PendingStartOperationID != "" || settled.Epoch != epoch {
		t.Fatalf("exact start completion did not clear the pending binding: %+v", settled)
	}
}

// P-6. `define` with Create=false has two real callers — reconcileRemoteVMCreateStopped
// (managed.go) and the local vm-update outcome confirmation (run.go) — so it is reachable,
// not unreachable as the plan's correction assumed. It is an idempotent owner *check*: with
// no record it refuses, and with a matching record it writes nothing at all, which is why it
// needs no placement or start guard and why the authority freeze deliberately allows it.
func TestDefineWithoutCreateOnlyChecksTheOwnerRecord(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	const vm = "11111111-1111-4111-8111-111111111111"
	spec := poolVMTestSpec(vm, "host-a")
	if err := poolVMLocal(s, vm, "define", nil, false, spec, nil); err == nil {
		t.Fatal("define without create seeded an owner record that did not exist")
	}
	if _, err := os.Stat(poolVMFile(s)); err == nil {
		t.Fatal("a refused owner check wrote the durable record")
	}
	if err := poolVMLocal(s, vm, "define", nil, true, spec, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(poolVMFile(s))
	if err != nil {
		t.Fatal(err)
	}
	if err = poolVMLocal(s, vm, "define", nil, false, spec, nil); err != nil {
		t.Fatalf("matching owner check refused: %v", err)
	}
	after, err := os.ReadFile(poolVMFile(s))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("an owner check rewrote the durable record:\n%s\n%s", before, after)
	}
	// It grants nothing, so a drained host does not refuse it either.
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", true))
	if err = poolVMLocal(s, vm, "define", nil, false, spec, nil); err != nil {
		t.Fatalf("a drained host refused an owner check: %v", err)
	}
}

// Tags ride in the definition the master commits, so a replacement Controller re-adopts them.
func TestPoolVMUpdateCommitKeepsTagsInMasterRecord(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	const vm = "11111111-1111-4111-8111-111111111112"
	previous := poolVMTestSpec(vm, "host-a")
	if err := poolVMLocal(s, vm, "define", nil, true, previous, nil); err != nil {
		t.Fatal(err)
	}
	next := *previous
	next.Revision, next.Tags = 1, []string{"prod", "web"}
	if err := poolVMLocalUpdate(s, vm, "update-reserve", "update-tags", previous, &next, nil); err != nil {
		t.Fatal(err)
	}
	evidence := vmhost.VMUpdateEvidence{OperationID: "update-tags", Status: "succeeded", Previous: *previous, Current: next}
	if err := poolVMLocalUpdate(s, vm, "update-commit", "update-tags", nil, nil, &evidence); err != nil {
		t.Fatal(err)
	}
	if got := mustLoadPoolVMs(t, s).VMs[vm].Spec.Tags; !slices.Equal(got, next.Tags) {
		t.Fatalf("master record tags = %v", got)
	}
}

func TestUnacknowledgedMasterReservationStaysRetryable(t *testing.T) {
	saved := &http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader("snapshot state was saved but is not yet acknowledged by every ready member"))}
	if err := snapshotReservationRefusal(saved, "refused"); !errors.Is(err, vmhost.ErrSnapshotReservationUnconfirmed) {
		t.Fatalf("saved-but-unacknowledged reservation must stay retryable: %v", err)
	}
	lost := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("reply lost"))}
	if err := snapshotReservationRefusal(lost, "refused"); !errors.Is(err, vmhost.ErrSnapshotReservationUnconfirmed) {
		t.Fatalf("a server error may follow a saved reservation and must stay retryable: %v", err)
	}
	refused := &http.Response{StatusCode: http.StatusConflict, Body: io.NopCloser(strings.NewReader("another snapshot recovery operation is pending"))}
	if err := snapshotReservationRefusal(refused, "refused"); errors.Is(err, vmhost.ErrSnapshotReservationUnconfirmed) {
		t.Fatalf("a plain refusal must not be retryable: %v", err)
	}
}
