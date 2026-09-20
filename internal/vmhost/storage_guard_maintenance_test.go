// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func writeGuardPoolReplica(t *testing.T, dir string, membership protocol.PoolMembership, verified bool) {
	t.Helper()
	record := poolReplicaRecord{
		Verified: verified,
		Replica: protocol.PoolAuthorityReplica{
			PoolID:       membership.PoolID,
			MasterHostID: membership.MasterHostID,
			Membership:   membership,
		},
	}
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, poolReplicaFileName), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func writeGuardPoolRecords(t *testing.T, dir, hostID string, membership protocol.PoolMembership) {
	t.Helper()
	b, err := json.Marshal(map[string]string{"host_id": hostID})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "state.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(map[string]any{"Membership": membership})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "pool-peer.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

// C-9. libvirt starts its autostart domains at boot, before Core's agent runs, so no
// agent-side admission sees them. This hook is the only early gate they pass.
func TestPoolHostDrainedRefusesManagedStart(t *testing.T) {
	dir := t.TempDir()
	if err := poolHostDrained(dir); err != nil {
		t.Fatalf("a host that is in no pool was refused: %v", err)
	}
	running := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 1, Members: []protocol.PoolMember{
		{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
	}}
	writeGuardPoolRecords(t, dir, "host-a", running)
	if err := poolHostDrained(dir); err != nil {
		t.Fatalf("a host that is not drained was refused: %v", err)
	}
	drained := running
	drained.Members = []protocol.PoolMember{{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready", Maintenance: true}}
	writeGuardPoolRecords(t, dir, "host-a", drained)
	err := poolHostDrained(dir)
	if err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("drained refusal = %v", err)
	}
	// Another host's drain is not this host's.
	writeGuardPoolRecords(t, dir, "host-b", drained)
	if err = poolHostDrained(dir); err != nil {
		t.Fatalf("a host was refused for another host's drain: %v", err)
	}
	// A present but unreadable membership record fails closed.
	if err = os.WriteFile(filepath.Join(dir, "pool-peer.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = poolHostDrained(dir); err == nil {
		t.Fatal("an unreadable pool membership record allowed a managed start")
	}
}

// A member's own pool-peer.json membership is stale after join; the libvirt hook must read
// the verified authority replica for the drain flag.
func TestPoolHostDrainedReadsMemberReplica(t *testing.T) {
	dir := t.TempDir()
	peerMembership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 4, Members: []protocol.PoolMember{
		{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 4, Status: "ready"},
		{HostID: "host-b", CertificateSHA256: strings.Repeat("b", 64), Generation: 4, Status: "ready"},
	}}
	writeGuardPoolRecords(t, dir, "host-b", peerMembership)
	replicaMembership := peerMembership
	replicaMembership.Members[1].Maintenance = true
	writeGuardPoolReplica(t, dir, replicaMembership, true)
	if err := poolHostDrained(dir); err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("member replica drain refusal = %v", err)
	}
	// An unverified replica does not override the member's own stale membership.
	if err := os.Remove(filepath.Join(dir, poolReplicaFileName)); err != nil {
		t.Fatal(err)
	}
	writeGuardPoolReplica(t, dir, replicaMembership, false)
	if err := poolHostDrained(dir); err != nil {
		t.Fatalf("an unverified replica overrode peer membership: %v", err)
	}
}

// The whole point of the hook: a managed local-storage domain, the shape the shared-storage
// gate above it never sees, is refused on a drained host.
func TestStorageGuardRefusesAManagedGuestOnADrainedHost(t *testing.T) {
	dir := t.TempDir()
	const vm = "11111111-1111-4111-8111-111111111111"
	writeGuardPoolRecords(t, dir, "host-a", protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 1, Members: []protocol.PoolMember{
		{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready", Maintenance: true},
	}})
	domain := `<domain><uuid>` + vm + `</uuid><devices><disk type="file" device="disk"><source file="/var/lib/storage/nex-vms/` + vm + `/disk.qcow2"/></disk></devices></domain>`
	err := StorageGuard(t.Context(), dir, strings.NewReader(domain))
	if err == nil || !strings.Contains(err.Error(), "maintenance mode") {
		t.Fatalf("a drained host started a managed guest: %v", err)
	}
}

// A restricted host has lost the authority needed to start any managed guest.
// Local storage does not need an execution permit, but it must not bypass the
// hook's authority gate on a direct virsh start or libvirt autostart.
func TestStorageGuardRefusesRestrictedLocalManagedGuestAndLeavesApplianceAlone(t *testing.T) {
	dir := t.TempDir()
	const vm = "11111111-1111-4111-8111-111111111111"
	if err := os.WriteFile(filepath.Join(dir, "pool-peer.json"), []byte(`{"Restricted":{"Reason":"this host's pool authority was superseded"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	localManaged := `<domain><uuid>` + vm + `</uuid><devices><disk type="file" device="disk"><source file="/var/lib/storage/nex-vms/` + vm + `/disk.qcow2"/></disk></devices></domain>`
	if err := StorageGuard(t.Context(), dir, strings.NewReader(localManaged)); err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("a restricted host started a local managed guest: %v", err)
	}
	// The hook does not take ownership of an independent appliance merely because
	// this host is restricted for pool work.
	appliance := `<domain><uuid>appliance</uuid><devices><disk type="file" device="disk"><source file="/var/lib/libvirt/images/appliance/controller.qcow2"/></disk></devices></domain>`
	if err := StorageGuard(t.Context(), dir, strings.NewReader(appliance)); err != nil {
		t.Fatalf("a restricted host refused an unmanaged appliance: %v", err)
	}
}

// The suppression never becomes the desired value: it is applied on top of the definition
// Nex already holds, so clearing it restores exactly what the operator asked for.
func TestSetManagedVMAutostartSuppressesWithoutRewritingTheDefinition(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "virsh-calls")
	fakeCommand(t, "virsh", `echo "$@" >> `+calls+`; exit 0`)
	e := NewManaged(filepath.Join(dir, "managed-vms"))
	if err := os.MkdirAll(filepath.Join(dir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	v := protocol.VMCreateSpec{ID: "11111111-1111-4111-8111-111111111111", Autostart: true}
	if err := e.SetManagedVMAutostart(t.Context(), v, true); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	if err := e.SetManagedVMAutostart(t.Context(), v, false); err != nil {
		t.Fatalf("restore: %v", err)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "autostart --disable") || strings.Contains(lines[1], "--disable") {
		t.Fatalf("virsh calls = %q", lines)
	}
	if !v.Autostart {
		t.Fatal("the suppression rewrote the caller's definition")
	}
}

// BUG-0381. Libvirt starts enabled local guests before the agent can persist an
// unconfirmed restriction. A potentially fence-promotable master therefore needs a
// confirmation bound to this OS boot, not merely yesterday's unrestricted record.
func TestPoolHostRestrictedRequiresCurrentBootAuthorityConfirmation(t *testing.T) {
	dir := t.TempDir()
	previous := poolHostBootID
	poolHostBootID = func() (string, error) { return "11111111-1111-4111-8111-111111111111", nil }
	t.Cleanup(func() { poolHostBootID = previous })
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-a", Generation: 1, Members: []protocol.PoolMember{
		{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: "host-b", CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"},
	}}
	write := func(marker any) {
		t.Helper()
		identity, err := json.Marshal(map[string]string{"host_id": "host-a"})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "state.json"), identity, 0600); err != nil {
			t.Fatal(err)
		}
		peer, err := json.Marshal(map[string]any{"Membership": membership, "SelfFence": map[string]any{}, "BootAuthority": marker})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "pool-peer.json"), peer, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(nil)
	if err := poolHostRestricted(dir); err == nil || !strings.Contains(err.Error(), "this boot confirms") {
		t.Fatalf("missing current-boot confirmation admitted startup: %v", err)
	}
	managed := `<domain><uuid>11111111-1111-4111-8111-111111111111</uuid><devices><disk type="file" device="disk"><source file="/var/lib/storage/nex-vms/11111111-1111-4111-8111-111111111111/disk.qcow2"/></disk></devices></domain>`
	if err := StorageGuard(t.Context(), dir, strings.NewReader(managed)); err == nil || !strings.Contains(err.Error(), "this boot confirms") {
		t.Fatalf("storage hook admitted a fresh boot before confirmation: %v", err)
	}
	write(map[string]string{"PoolID": "pool-1", "HostID": "host-a", "BootID": "22222222-2222-4222-8222-222222222222"})
	if err := poolHostRestricted(dir); err == nil || !strings.Contains(err.Error(), "this boot confirms") {
		t.Fatalf("stale confirmation admitted startup: %v", err)
	}
	write(map[string]string{"PoolID": "pool-1", "HostID": "host-a", "BootID": "11111111-1111-4111-8111-111111111111"})
	if err := poolHostRestricted(dir); err != nil {
		t.Fatalf("current bound confirmation refused startup: %v", err)
	}
	// A member, a single-host pool, and an unpooled Core retain their existing behavior.
	membership.MasterHostID = "host-b"
	write(nil)
	if err := poolHostRestricted(dir); err != nil {
		t.Fatalf("member refused: %v", err)
	}
	membership.MasterHostID, membership.Members = "host-a", membership.Members[:1]
	write(nil)
	if err := poolHostRestricted(dir); err != nil {
		t.Fatalf("single-host master refused: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "pool-peer.json")); err != nil {
		t.Fatal(err)
	}
	if err := poolHostRestricted(dir); err != nil {
		t.Fatalf("unpooled Core refused: %v", err)
	}
}
