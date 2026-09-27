// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestDetectedOSSurvivesCoreRestartAndGuestUnavailability(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	facts := protocol.DetectedOS{OS: "Debian GNU/Linux 13", OSID: "debian", ObservedAt: time.Now().UTC().Truncate(time.Second)}
	vms := []protocol.VMCreateSpec{vm}
	detected := map[string]protocol.DetectedOS{vm.ID: facts}
	if _, err := retainLocalDetectedOS(s, vms, detected); err != nil {
		t.Fatal(err)
	}
	// Reopen local durable state without any Controller or master connection.
	s = &Store{dir: s.dir, State: s.State}
	for _, unavailable := range []map[string]protocol.DetectedOS{nil, {vm.ID: {}}, {vm.ID: {OS: "old", ObservedAt: facts.ObservedAt.Add(-time.Hour)}}} {
		got, err := retainLocalDetectedOS(s, vms, unavailable)
		if err != nil || !reflect.DeepEqual(got, detected) {
			t.Fatalf("retained facts = %#v, %v", got, err)
		}
	}
	// A later probe with the same OS must not rewrite ObservedAt (durable churn).
	same := facts
	same.ObservedAt = facts.ObservedAt.Add(time.Minute)
	got, err := retainLocalDetectedOS(s, vms, map[string]protocol.DetectedOS{vm.ID: same})
	if err != nil || got[vm.ID].ObservedAt != facts.ObservedAt || got[vm.ID].OS != facts.OS {
		t.Fatalf("unchanged OS rewrote ObservedAt: %#v, %v", got, err)
	}
	membership, err := Membership(s)
	if err != nil {
		t.Fatal(err)
	}
	inv := poolVMInventory{HostID: "host-a", Generation: membership.Generation, VMs: vms, Observed: map[string]string{vm.ID: "running"}, DetectedOS: detected}
	if err := recordPoolVMInventory(s, membership, inv); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"paused", "shut off", "running"} {
		inv.Observed[vm.ID], inv.DetectedOS = status, nil
		if err := recordPoolVMInventory(s, membership, inv); err != nil {
			t.Fatal(err)
		}
		got, err := poolDetectedOS(s)
		if err != nil || !reflect.DeepEqual(got, detected) {
			t.Fatalf("%s lost master facts: %#v, %v", status, got, err)
		}
	}
	exported, err := poolReplicaVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]poolVMRecord
	if err := json.Unmarshal(exported, &restored); err != nil || restored[vm.ID].DetectedOS == nil || *restored[vm.ID].DetectedOS != facts {
		t.Fatalf("authority export lost facts: %s, %v", exported, err)
	}
	target := peerStore(t, "host-b", "pool-1")
	if err := adoptPoolVMAuthority(target, protocol.PoolAuthorityReplica{VMs: exported}); err != nil {
		t.Fatal(err)
	}
	adopted, err := loadPoolVMs(target)
	if err != nil || adopted.VMs[vm.ID].DetectedOS == nil || *adopted.VMs[vm.ID].DetectedOS != facts {
		t.Fatalf("master adoption lost OS: %#v, %v", adopted, err)
	}
	// A new successful reading replaces the old OS; retries cannot roll it back.
	newer := facts
	newer.OS, newer.ObservedAt = "Debian GNU/Linux 14", facts.ObservedAt.Add(time.Second)
	inv.DetectedOS = map[string]protocol.DetectedOS{vm.ID: newer}
	if err := recordPoolVMInventory(s, membership, inv); err != nil {
		t.Fatal(err)
	}
	inv.DetectedOS = detected
	if err := recordPoolVMInventory(s, membership, inv); err != nil {
		t.Fatal(err)
	}
	got, _ = poolDetectedOS(s)
	if got[vm.ID] != newer {
		t.Fatal("older retry replaced newer detection")
	}
	inv.VMs, inv.DetectedOS, inv.Deleted = nil, nil, []string{vm.ID}
	if err := recordPoolVMInventory(s, membership, inv); err != nil {
		t.Fatal(err)
	}
	got, err = poolDetectedOS(s)
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted VM retained master OS: %#v, %v", got, err)
	}
	got, err = retainLocalDetectedOS(s, nil, detected)
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted VM retained local OS: %#v, %v", got, err)
	}
}

func TestDetectedOSRejectsWrongOwnerAndInvalidFacts(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", "host-a")
	membership, _ := Membership(s)
	facts := protocol.DetectedOS{OS: "Debian", ObservedAt: time.Now().UTC()}
	for _, invalid := range []protocol.DetectedOS{{}, {OS: strings.Repeat("x", 10000), ObservedAt: facts.ObservedAt}} {
		inv := poolVMInventory{HostID: "host-a", Generation: membership.Generation, VMs: []protocol.VMCreateSpec{vm}, DetectedOS: map[string]protocol.DetectedOS{vm.ID: invalid}}
		if recordPoolVMInventory(s, membership, inv) == nil {
			t.Fatal("invalid facts accepted")
		}
	}
	vm.HostID = "host-b"
	detected := map[string]protocol.DetectedOS{vm.ID: facts}
	inv := poolVMInventory{HostID: "host-a", Generation: membership.Generation, VMs: []protocol.VMCreateSpec{vm}, DetectedOS: detected}
	if recordPoolVMInventory(s, membership, inv) == nil {
		t.Fatal("other owner's facts accepted")
	}
	got, err := retainLocalDetectedOS(s, []protocol.VMCreateSpec{vm}, detected)
	if err != nil || len(got) != 0 {
		t.Fatalf("local retention accepted other owner: %#v, %v", got, err)
	}
}

func TestDetectedOSRequiresControllerAcknowledgement(t *testing.T) {
	facts := protocol.DetectedOS{OS: "Debian", ObservedAt: time.Now().UTC()}
	managed := &protocol.ManagedState{DetectedOS: map[string]protocol.DetectedOS{"vm": facts}}
	raw, _ := json.Marshal(managedExtensionsForController(managed, false, false))
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var old struct {
		Outcomes []protocol.ManagedOutcome `json:"outcomes,omitempty"`
	}
	if err := decoder.Decode(&old); err != nil {
		t.Fatalf("old Controller rejects initial report: %v", err)
	}
	if len(managed.DetectedOS) != 1 || len(managedExtensionsForController(managed, false, true).DetectedOS) != 1 {
		t.Fatal("projection destroyed retained OS")
	}
	reports := 0
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if !contains(request.Capabilities, protocol.DetectedOSCapability) {
			t.Error("detected OS extension not offered")
		}
		if got := len(request.Inventory.Managed.DetectedOS); (reports == 1) != (got == 1) {
			t.Errorf("report %d retained facts = %d", reports, got)
		}
		reports++
		ack := reports == 1
		json.NewEncoder(w).Encode(protocol.ReportResponse{DetectedOS: &ack})
	}))
	s := testStore(t)
	s.State = identity
	s.State.PoolID = "pool-1"
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	id := "11111111-1111-4111-8111-111111111111"
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{id: {PoolID: s.State.PoolID, VMID: id, HostID: s.State.HostID, DetectedOS: &facts}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		if s.State.DetectedOSReady != (i == 0) {
			t.Fatal("incorrect acknowledgement state")
		}
	}
	if got, err := poolDetectedOS(s); err != nil || len(got) != 1 {
		t.Fatal("negative ack cleared Core history")
	}
	s.State.DetectedOSReady = true
	if err := resetReportExtensions(s); err != nil || s.State.DetectedOSReady {
		t.Fatal("acknowledgement survived restart")
	}
}

func TestDetectedOSPeerPublicationRetriesWithoutGuestOrController(t *testing.T) {
	ctx := context.Background()
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	var unavailable atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		PoolPeerHandler(master).ServeHTTP(w, r)
	}))
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err := JoinPoolPeer(ctx, member, server.URL, ca, offer); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", member.State.HostID)
	dir := filepath.Join(member.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeManagedJournal(t, dir, "create", protocol.Command{OperationID: "create", Kind: "vm-create", VM: &vm})
	autostartVirsh(t, vm.ID) // Simulated stopped guest; no QGA or Controller exists.
	facts := protocol.DetectedOS{OS: "Debian", OSID: "debian", ObservedAt: time.Now().UTC()}
	if _, err := retainLocalDetectedOS(member, []protocol.VMCreateSpec{vm}, map[string]protocol.DetectedOS{vm.ID: facts}); err != nil {
		t.Fatal(err)
	}
	if err := PublishPoolVMInventory(ctx, master); !errors.Is(err, errPoolVMInventoryIncomplete) {
		t.Fatalf("master inventory = %v", err)
	}
	unavailable.Store(true)
	if err := PublishPoolVMInventory(ctx, member); err == nil {
		t.Fatal("unavailable master unexpectedly accepted publication")
	}
	member = &Store{dir: member.dir, State: member.State}
	unavailable.Store(false)
	if err := PublishPoolVMInventory(ctx, member); err != nil {
		t.Fatal(err)
	}
	reopenedMaster := &Store{dir: master.dir, State: master.State}
	got, err := poolDetectedOS(reopenedMaster)
	if err != nil || got[vm.ID] != facts {
		t.Fatalf("wire/restart lost detected OS: %#v, %v", got, err)
	}
	if err := PublishPoolVMInventory(ctx, member); err != nil {
		t.Fatal(err)
	}
	// An authenticated member cannot publish under a different host's identity.
	membership, _ := Membership(member)
	inv := poolVMInventory{HostID: master.State.HostID, Generation: membership.Generation, VMs: []protocol.VMCreateSpec{vm}, DetectedOS: map[string]protocol.DetectedOS{vm.ID: facts}}
	body, _ := json.Marshal(inv)
	client, err := PoolPeerClient(member, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	response, err := client.Post(server.URL+poolVMPath+"inventory", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged host inventory status = %d", response.StatusCode)
	}
}

func TestDetectedOSSkipsCorruptHistory(t *testing.T) {
	s := peerStore(t, "host-a", "pool-1")
	if err := EnsurePoolMembership(s); err != nil {
		t.Fatal(err)
	}
	goodID := "11111111-1111-4111-8111-111111111111"
	badID := "22222222-2222-4222-8222-222222222222"
	good := protocol.DetectedOS{OS: "Debian", OSID: "debian", ObservedAt: time.Now().UTC().Truncate(time.Second)}
	bad := protocol.DetectedOS{OS: "", ObservedAt: good.ObservedAt}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{
		goodID: {PoolID: s.State.PoolID, VMID: goodID, HostID: s.State.HostID, DetectedOS: &good},
		badID:  {PoolID: s.State.PoolID, VMID: badID, HostID: s.State.HostID, DetectedOS: &bad},
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := poolDetectedOS(s)
	if err == nil || len(got) != 1 || got[goodID] != good {
		t.Fatalf("corrupt entry cleared valid history: %#v, %v", got, err)
	}
}
