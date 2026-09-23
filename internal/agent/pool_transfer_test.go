// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// A transfer lab is a pool of in-process hosts, each serving its own pool peer listener, so
// every step of a master change runs over the real mTLS peer transport with real
// certificates, real membership pinning and real signatures.
type transferLab struct {
	t      *testing.T
	hosts  []*Store
	origin map[string]string
	fail   map[string]*atomic.Value
	// off are the hosts this lab has powered off. A fenced host is not executing, so it
	// neither writes authority nor starts a guest, and the invariant counters below must
	// not count its on-disk state as a running writer.
	off map[string]bool
}

func TestSignedEjectCleanupProvenanceBindsTheLeavingMember(t *testing.T) {
	s := peerStore(t, "member-1", "pool-1")
	base := &poolTransferRecord{Action: "eject", Role: "ejected", MembershipGeneration: 7, Notice: &protocol.PoolAuthorityNotice{PoolID: "pool-1", TargetHostID: "member-1", Membership: protocol.PoolMembership{Generation: 7}}}
	poolID, hostID, generation, ok := signedEjectCleanupProvenance(s, base)
	if !ok || poolID != s.State.PoolID || hostID != s.State.HostID || generation != 7 {
		t.Fatalf("signed eject provenance = %q %q %d %v", poolID, hostID, generation, ok)
	}
	for _, mutate := range []func(*poolTransferRecord){
		func(r *poolTransferRecord) { r.Notice.Membership.Generation = 6 },
		func(r *poolTransferRecord) { r.Notice.PoolID = "other-pool" },
		func(r *poolTransferRecord) { r.Notice.TargetHostID = "other-host" },
	} {
		copy := *base
		notice := *base.Notice
		copy.Notice = &notice
		mutate(&copy)
		if _, _, _, ok := signedEjectCleanupProvenance(s, &copy); ok {
			t.Fatal("mismatched leave received cleanup provenance")
		}
	}
}

// drop injects an interruption on one host's peer listener. An empty path clears it; "*"
// matches every request; a path prefixed with "!" is refused before the handler runs, and
// any other path is handled first and only its reply is lost — the two interruptions that
// look identical to the caller and completely different to the pool.
func (l *transferLab) drop(hostID, path string) {
	l.t.Helper()
	l.fail[hostID].Store(path)
}

func newTransferLab(t *testing.T, members int) *transferLab {
	t.Helper()
	lab := &transferLab{t: t, origin: map[string]string{}, fail: map[string]*atomic.Value{}, off: map[string]bool{}}
	master := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	lab.hosts = append(lab.hosts, master)
	for i := 1; i <= members; i++ {
		member := peerStore(t, fmt.Sprintf("member-%d", i), fmt.Sprintf("solo-%d", i))
		joinPeerMember(t, master, member)
		lab.hosts = append(lab.hosts, member)
	}
	for _, host := range lab.hosts {
		lab.serve(host)
	}
	// Every host's advertised origin is recorded on the master, including the master's own:
	// a new master must be able to reach its predecessor.
	for _, host := range lab.hosts {
		setMemberPeerOrigin(t, master, host.State.HostID, lab.origin[host.State.HostID])
	}
	masterURL := lab.origin[master.State.HostID]
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.MasterURL = masterURL
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range lab.hosts[1:] {
		poolPeerMu.Lock()
		state, err := loadPoolPeer(host)
		if err == nil {
			state.MasterURL = masterURL
			err = savePoolPeer(host, state)
		}
		poolPeerMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ClearPoolReplicaStateForTest(host) })
	}
	for _, host := range lab.hosts {
		writePoolVMInventory(t, master, host.State.HostID, nil)
	}
	t.Cleanup(func() { ClearPoolReplicaStateForTest(master) })
	return lab
}

// serve starts one host's peer listener behind a switch that can swallow the reply of an
// otherwise completed request: that is how an interruption between "the peer did the work"
// and "the caller learned about it" is injected at any stage.
func (l *transferLab) serve(host *Store) {
	l.t.Helper()
	config, err := PoolPeerTLSConfig(host)
	if err != nil {
		l.t.Fatal(err)
	}
	drop := &atomic.Value{}
	drop.Store("")
	l.fail[host.State.HostID] = drop
	handler := PoolPeerHandler(host)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, _ := drop.Load().(string)
		refuse := strings.HasPrefix(path, "!")
		match := path != "" && (strings.TrimPrefix(path, "!") == "*" || strings.Contains(r.URL.Path, strings.TrimPrefix(path, "!")))
		if match && refuse {
			http.Error(w, "peer unavailable", http.StatusServiceUnavailable)
			return
		}
		if match {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			handler.ServeHTTP(httptest.NewRecorder(), r)
			http.Error(w, "reply lost", http.StatusBadGateway)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	server.TLS = config
	server.StartTLS()
	l.t.Cleanup(server.Close)
	l.origin[host.State.HostID] = server.URL
}

func (l *transferLab) host(id string) *Store {
	l.t.Helper()
	for _, host := range l.hosts {
		if host.State.HostID == id {
			return host
		}
	}
	l.t.Fatalf("no such host in this lab: %s", id)
	return nil
}

// masters counts the hosts that would accept an authority write right now. It is asserted
// after every step of every test: the pool may never have two, and after a completed
// transfer it may never have none.
func (l *transferLab) masters() []string {
	l.t.Helper()
	out := []string{}
	for _, host := range l.hosts {
		if l.off[host.State.HostID] {
			continue
		}
		poolPeerMu.Lock()
		state, err := loadPoolPeer(host)
		poolPeerMu.Unlock()
		if err != nil || state.Membership.Valid() != nil {
			continue
		}
		if state.Membership.MasterHostID == host.State.HostID && poolAuthorityWritable(state) == nil {
			out = append(out, host.State.HostID)
		}
	}
	return out
}

func (l *transferLab) requireSingleMaster(step, expected string) {
	l.t.Helper()
	masters := l.masters()
	if len(masters) != 1 || masters[0] != expected {
		l.t.Fatalf("%s: hosts accepting authority writes = %v, want exactly [%s]", step, masters, expected)
	}
}

func (l *transferLab) requireNoWriter(step string) {
	l.t.Helper()
	if masters := l.masters(); len(masters) != 0 {
		l.t.Fatalf("%s: hosts accepting authority writes = %v, want none", step, masters)
	}
}

func transferCommand(id, action, target, master string) protocol.Command {
	return protocol.Command{OperationID: id, Kind: "pool-transfer", Pool: &protocol.PoolCommand{
		RequestID: "request-" + id, Action: action, MemberHostID: target,
		Membership: protocol.PoolMembership{MasterHostID: master},
	}}
}

func writePoolVMRecord(t *testing.T, s *Store, record poolVMRecord) {
	t.Helper()
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	if state.VMs == nil {
		state.VMs = map[string]poolVMRecord{}
	}
	state.VMs[record.VMID] = record
	if err = savePoolVMs(s, state); err != nil {
		t.Fatal(err)
	}
}

func poolVMRecords(t *testing.T, s *Store) map[string]poolVMRecord {
	t.Helper()
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		t.Fatal(err)
	}
	return state.VMs
}

func labVM(pool, id, host string) poolVMRecord {
	spec := *poolVMTestSpec(id, host)
	spec.PoolID, spec.Revision = pool, 3
	return poolVMRecord{PoolID: pool, VMID: id, HostID: host, State: "stopped", Epoch: 7, Spec: spec}
}

func peerState(t *testing.T, s *Store) poolPeerState {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// peerVMRequest is a member asking its recorded master for a durable VM execution decision.
// It is how the tests prove which host is actually serving authority, and what a member
// sees while that authority is frozen.
func peerVMRequest(t *testing.T, member *Store, request poolVMRequest) (int, string) {
	t.Helper()
	master, err := PoolMasterURL(member)
	if err != nil {
		t.Fatal(err)
	}
	client, err := PoolPeerClient(member, master)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	body, _ := json.Marshal(request)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+request.VMID, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("peer VM request: %v", err)
	}
	defer response.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	return response.StatusCode, string(out)
}

// The whole planned master change, on a three-host pool, over the real peer transport:
// every identity survives, every host ends up following the new master, and at no point do
// two hosts accept an authority write.
func TestPoolMasterChangeMovesAuthorityOnce(t *testing.T) {
	lab := newTransferLab(t, 2)
	master, target, bystander := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	before := peerState(t, master).Membership
	writePoolVMRecord(t, master, labVM("pool-1", "11111111-1111-4111-8111-111111111111", "member-1"))
	writePoolVMRecord(t, master, labVM("pool-1", "22222222-2222-4222-8222-222222222222", "member-2"))
	vlan := protocol.NetworkRegistration{ID: "vlan-100", PoolID: "pool-1", HostID: master.State.HostID, Name: "Servers", Kind: "vlan", Source: protocol.GuestBridge("nex1", 100), Uplink: "nex1", VLAN: 100, Generation: 4}
	writeAppliedPoolNetwork(t, master, vlan)
	vms := poolVMRecords(t, master)
	lab.requireSingleMaster("before the transfer", "master-1")

	result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-1", "master-change", "member-1", "master-1"))
	if result.Status != "succeeded" {
		t.Fatalf("master change = %#v", result)
	}
	lab.requireSingleMaster("after the hand-off and demotion", "member-1")

	// The new master re-points every remaining host; until it has, the bystander still
	// follows the old master, which refuses to write anything.
	AdvancePoolAuthorityTransfer(context.Background(), target, 20*time.Second)
	lab.requireSingleMaster("after the re-point", "member-1")

	// Identity: the pool, its VM records, epochs, specs, names and the VLAN registration
	// are what they were, on the new master, byte for byte.
	after := peerState(t, target).Membership
	if after.PoolID != before.PoolID || target.State.PoolID != "pool-1" || master.State.PoolID != "pool-1" {
		t.Fatalf("pool identity changed: %q then %q", before.PoolID, after.PoolID)
	}
	if after.Generation != before.Generation+1 {
		t.Fatalf("membership generation = %d, want %d", after.Generation, before.Generation+1)
	}
	if len(after.Members) != len(before.Members) {
		t.Fatalf("membership lost a host: %#v", after.Members)
	}
	for _, was := range before.Members {
		is, ok := poolMemberByID(after, was.HostID)
		if !ok || is.CertificateSHA256 != was.CertificateSHA256 || is.PeerOrigin != was.PeerOrigin {
			t.Fatalf("member %s identity changed: %#v then %#v", was.HostID, was, is)
		}
	}
	adopted := poolVMRecords(t, target)
	if len(adopted) != len(vms) {
		t.Fatalf("new master adopted %d VM records, want %d", len(adopted), len(vms))
	}
	for id, was := range vms {
		is, ok := adopted[id]
		if !ok {
			t.Fatalf("VM %s was lost by the master change", id)
		}
		if was.HostID != is.HostID || was.Epoch != is.Epoch || was.State != is.State {
			t.Fatalf("VM %s ownership changed: %#v then %#v", id, was, is)
		}
		wasJSON, _ := json.Marshal(was)
		isJSON, _ := json.Marshal(is)
		if string(wasJSON) != string(isJSON) {
			t.Fatalf("VM %s record is not byte-identical:\n%s\n%s", id, wasJSON, isJSON)
		}
	}

	// The demoted master kept its identity, its certificate and its membership row, and
	// gave up exactly one thing: the key that signs this pool's authority.
	demoted := peerState(t, master)
	if demoted.CAKeyPEM != "" {
		t.Fatal("the demoted master kept the pool CA key")
	}
	if demoted.Membership.MasterHostID != "member-1" || demoted.MasterURL != lab.origin["member-1"] {
		t.Fatalf("the demoted master follows %q at %q", demoted.Membership.MasterHostID, demoted.MasterURL)
	}
	if demoted.CertificatePEM == "" || demoted.AuthorityGeneration != 0 {
		t.Fatalf("the demoted master's own identity changed: %#v", demoted.AuthorityGeneration)
	}
	if peerState(t, bystander).Membership.MasterHostID != "member-1" {
		t.Fatal("the bystander still follows the old master")
	}

	// Master-owned work now happens on the new master: it admits a start for a VM it owns,
	// records maintenance, pushes the replica and serves the member pull.
	status, body := peerVMRequest(t, bystander, poolVMRequest{VMID: "22222222-2222-4222-8222-222222222222", Action: "start"})
	if status != http.StatusOK || !strings.Contains(body, "\"permit\"") {
		t.Fatalf("new master refused a start permit: %d %s", status, body)
	}
	drain := ExecutePoolMaintenance(target, protocol.Command{OperationID: "op-drain", Kind: "pool-maintenance", Pool: &protocol.PoolCommand{
		RequestID: "r", Action: "maintenance", MemberHostID: "member-2", Enabled: true,
		Membership: protocol.PoolMembership{MasterHostID: "member-1"},
	}})
	if drain.Status != "succeeded" {
		t.Fatalf("maintenance on the new master = %#v", drain)
	}
	if err := PushPoolAuthorityReplicas(context.Background(), target, 20*time.Second); err != nil {
		t.Fatalf("replica push from the new master: %v", err)
	}
	for _, host := range []*Store{master, bystander} {
		record := storedReplica(t, host)
		if record == nil || !record.Verified || record.Replica.MasterHostID != "member-1" {
			t.Fatalf("%s holds no replica from the new master: %#v", host.State.HostID, record)
		}
	}
	if origin, err := PoolMasterURL(bystander); err != nil || origin != lab.origin["member-1"] {
		t.Fatalf("member pull still points at %q (%v)", origin, err)
	}
	// And the old master serves nothing: its peer authority is refused by its own record.
	if _, err := CreatePoolJoinInvitation(master, lab.origin["master-1"], "new-host"); err == nil {
		t.Fatal("the demoted master still issues pool join invitations")
	}
	lab.requireSingleMaster("at the end", "member-1")
}

// Authority writes are refused as soon as the transfer record exists at the freeze stage,
// before replicate begins, so there is no writable window between recording and freeze.
func TestFrozenAuthorityAtTransferRecordCreation(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	record := &poolTransferRecord{
		OperationID: "op-gap", Action: "promote", Role: "source", TargetHostID: "member-1",
		Stage: "freeze", Status: "pending", Frozen: true,
	}
	if err := savePoolTransfer(master, record); err != nil {
		t.Fatal(err)
	}
	lab.requireNoWriter("at freeze stage before replicate")
	if err := poolRegistryCommandWritable(master, protocol.Command{Kind: "network-register"}); err == nil || err.Error() != poolAuthorityFrozenRefusal {
		t.Fatalf("freeze stage write = %v", err)
	}
}

// Every authority write is refused while the authority is frozen, with the one sentence a
// Controller maps to a retryable conflict; a stop is never refused by the freeze.
func TestFrozenPoolAuthorityRefusesEveryAuthorityWrite(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, target := lab.host("master-1"), lab.host("member-1")
	vmID := "33333333-3333-4333-8333-333333333333"
	writePoolVMRecord(t, master, labVM("pool-1", vmID, "member-1"))
	// Freeze by driving the machine with the target unreachable: the freeze is durable,
	// the replicate step is not reached, and the pool has no second writer.
	lab.drop("member-1", "!*")
	result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-frozen", "master-change", "member-1", "master-1"))
	if result.Status != "needs_attention" {
		t.Fatalf("interrupted master change = %#v", result)
	}
	record := peerState(t, master).Transfer
	if record == nil || !record.Frozen || record.HandedOff || record.Stage != "replicate" {
		t.Fatalf("freeze record = %#v", record)
	}
	lab.requireNoWriter("while frozen")

	if status, body := peerVMRequest(t, target, poolVMRequest{VMID: vmID, Action: "start"}); status != http.StatusConflict || !strings.Contains(body, poolAuthorityFrozenRefusal) {
		t.Fatalf("frozen start = %d %s", status, body)
	}
	if status, _ := peerVMRequest(t, target, poolVMRequest{VMID: vmID, Action: "stopped"}); status != http.StatusOK {
		t.Fatalf("a frozen pool refused a stop: %d", status)
	}
	drain := ExecutePoolMaintenance(master, protocol.Command{OperationID: "op-drain", Kind: "pool-maintenance", Pool: &protocol.PoolCommand{
		RequestID: "r", Action: "maintenance", MemberHostID: "member-1", Enabled: true,
		Membership: protocol.PoolMembership{MasterHostID: "master-1"},
	}})
	if drain.Status != "failed" || drain.Error != poolAuthorityFrozenRefusal {
		t.Fatalf("frozen maintenance = %#v", drain)
	}
	if _, err := CreatePoolJoinInvitation(master, lab.origin["master-1"], "member-2"); err == nil || err.Error() != poolAuthorityFrozenRefusal {
		t.Fatalf("frozen join invitation = %v", err)
	}
	if err := poolRegistryCommandWritable(master, protocol.Command{Kind: "network-register"}); err == nil || err.Error() != poolAuthorityFrozenRefusal {
		t.Fatalf("frozen network registration = %v", err)
	}
	if err := poolRegistryCommandWritable(master, protocol.Command{Kind: "vm-power"}); err != nil {
		t.Fatalf("the freeze blocked a power command: %v", err)
	}

	// Reachable again, the same operation completes from the durable record alone.
	lab.drop("member-1", "")
	AdvancePoolAuthorityTransfer(context.Background(), master, 20*time.Second)
	AdvancePoolAuthorityTransfer(context.Background(), target, 20*time.Second)
	lab.requireSingleMaster("after the interrupted transfer resumed", "member-1")
}

// An interruption at every stage, on each side, converges to exactly one master from the
// durable records alone, with no Controller in the loop.
func TestPoolMasterChangeResumesFromEveryStage(t *testing.T) {
	for _, stage := range []string{"freeze", "replicate", "activate", "repoint"} {
		t.Run(stage, func(t *testing.T) {
			lab := newTransferLab(t, 2)
			master := lab.host("master-1")
			writePoolVMRecord(t, master, labVM("pool-1", "44444444-4444-4444-8444-444444444444", "member-2"))
			switch stage {
			case "freeze", "replicate":
				// The target never answers, so the machine stops with a durable freeze.
				lab.drop("member-1", "!*")
			case "activate":
				// The target does the work and the reply is lost: the outgoing master has
				// signed the hand-off and does not know the outcome.
				lab.drop("member-1", poolTransferPath)
			case "repoint":
				// The transfer completes; the third host is unreachable for the re-point.
				lab.drop("member-2", "!*")
			}
			if stage == "activate" {
				// Push the replica first, so the target can activate when it is asked.
				if err := PushPoolAuthorityReplicas(context.Background(), master, 20*time.Second); err != nil {
					t.Fatalf("replica push: %v", err)
				}
			}
			result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-"+stage, "master-change", "member-1", "master-1"))
			if stage == "repoint" && result.Status != "succeeded" {
				t.Fatalf("%s: master change = %#v", stage, result)
			}
			if stage != "repoint" && result.Status == "succeeded" {
				t.Fatalf("%s: interrupted master change reported success: %#v", stage, result)
			}
			if masters := lab.masters(); len(masters) > 1 {
				t.Fatalf("%s: two hosts accept authority writes: %v", stage, masters)
			}
			// Recover: the peers answer again, and every host resumes its own record.
			for id := range lab.fail {
				lab.drop(id, "")
			}
			for pass := 0; pass < 4; pass++ {
				for _, host := range lab.hosts {
					AdvancePoolAuthorityTransfer(context.Background(), host, 20*time.Second)
					ConvergePoolAuthority(context.Background(), host, 20*time.Second)
					if masters := lab.masters(); len(masters) > 1 {
						t.Fatalf("%s: two hosts accept authority writes during recovery: %v", stage, masters)
					}
				}
			}
			expected := "member-1"
			if stage == "freeze" || stage == "replicate" {
				// Nothing was handed over: the outgoing master is still the master. It
				// resumes the transfer instead, and finishes it.
				expected = "member-1"
			}
			lab.requireSingleMaster(stage+": after recovery", expected)
			for _, host := range lab.hosts {
				if got := peerState(t, host).Membership.MasterHostID; got != expected {
					t.Fatalf("%s: host %s follows %q, want %q", stage, host.State.HostID, got, expected)
				}
			}
		})
	}
}

// A host that handed its authority over and then restarted refuses to act as a master
// before it knows the outcome, and converges to a member from the new master's signed
// evidence. It never starts a guest or grants a placement meanwhile.
func TestHandedOverMasterRefusesAuthorityAndConverges(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, target := lab.host("master-1"), lab.host("member-1")
	vmID := "55555555-5555-4555-8555-555555555555"
	writePoolVMRecord(t, master, labVM("pool-1", vmID, "member-1"))
	if err := PushPoolAuthorityReplicas(context.Background(), master, 20*time.Second); err != nil {
		t.Fatalf("replica push: %v", err)
	}
	lab.drop("member-1", poolTransferPath) // the activation lands; its reply does not.
	result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-lost", "master-change", "member-1", "master-1"))
	if result.Status == "succeeded" {
		t.Fatalf("a lost activation reply reported success: %#v", result)
	}
	record := peerState(t, master).Transfer
	if record == nil || !record.HandedOff {
		t.Fatalf("hand-off record = %#v", record)
	}
	if peerState(t, target).Membership.MasterHostID != "member-1" {
		t.Fatal("the target did not activate")
	}
	lab.requireSingleMaster("after a lost activation reply", "member-1")

	// The old master is restarted: nothing is in memory, and it still refuses. A start
	// permit, a placement and a membership write are all refused for good, not retryably.
	if err := requirePoolVMAuthorityWritable(master, "start", false); err == nil || err.Error() != poolAuthorityHandedOffRefusal {
		t.Fatalf("handed-over start = %v", err)
	}
	if err := requirePoolVMAuthorityWritable(master, "stopped", false); err == nil || err.Error() != poolAuthorityHandedOffRefusal {
		t.Fatalf("a handed-over master still records execution transitions: %v", err)
	}
	if _, _, err := PoolAuthorityGeneration(master); err != nil {
		t.Fatalf("authority generation is unreadable: %v", err)
	}
	if _, err := advancePoolAuthorityGeneration(master, "anything"); err == nil {
		t.Fatal("a handed-over master still numbers a new authority generation")
	}

	lab.drop("member-1", "")
	AdvancePoolAuthorityTransfer(context.Background(), master, 20*time.Second)
	if state := peerState(t, master); state.Membership.MasterHostID != "member-1" || state.CAKeyPEM != "" || state.Transfer.Stage != "done" {
		t.Fatalf("the returning master did not converge: %#v", state.Transfer)
	}
	lab.requireSingleMaster("after the returning master converged", "member-1")
}

// A member that was unreachable for the re-point catches up later by asking the host it
// still believes is its master, which sends it on to the host that is master now. Nothing
// is adopted without a signature at a higher generation.
func TestUnreachableMemberCatchesUpFromEitherHost(t *testing.T) {
	lab := newTransferLab(t, 2)
	master, target, late := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	lab.drop("member-2", "!*")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-late", "master-change", "member-1", "master-1")); result.Status != "succeeded" {
		t.Fatalf("master change = %#v", result)
	}
	AdvancePoolAuthorityTransfer(context.Background(), target, 20*time.Second)
	if peerState(t, late).Membership.MasterHostID != "master-1" {
		t.Fatal("the unreachable member was re-pointed anyway")
	}
	lab.requireSingleMaster("while a member is behind", "member-1")

	lab.drop("member-2", "")
	ConvergePoolAuthority(context.Background(), late, 20*time.Second)
	state := peerState(t, late)
	if state.Membership.MasterHostID != "member-1" || state.MasterURL != lab.origin["member-1"] {
		t.Fatalf("the returning member follows %q at %q", state.Membership.MasterHostID, state.MasterURL)
	}
	lab.requireSingleMaster("after the late member caught up", "member-1")
}

// A stale master can never call a host back: a re-point at an older or equal membership
// generation is refused, and so is one that is not signed by the pool CA.
func TestRepointRefusesStaleAndUnsignedNotices(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	state := peerState(t, master)
	good := protocol.PoolAuthorityNotice{
		PoolID: "pool-1", OperationID: "op-stale", Action: "promote", FromHostID: "master-1",
		ToHostID: "member-1", TargetHostID: "master-1", AuthorityGeneration: 1,
		Membership: state.Membership, IssuedAt: time.Now().UTC(),
	}
	if err := signPoolAuthorityNotice(state.CAKeyPEM, &good); err != nil {
		t.Fatal(err)
	}
	caller, _ := poolMemberByID(state.Membership, "master-1")
	if _, err := applyPoolRepoint(member, caller, protocol.PoolTransferRequest{OperationID: "op-stale", Action: "repoint", Notice: good}); err != nil {
		t.Fatalf("a notice naming the current master at the current generation must be a no-op, got %v", err)
	}
	stale := good
	stale.Membership.Generation = state.Membership.Generation - 1
	if err := signPoolAuthorityNotice(state.CAKeyPEM, &stale); err != nil {
		t.Fatal(err)
	}
	if _, err := applyPoolRepoint(member, caller, protocol.PoolTransferRequest{OperationID: "op-stale", Action: "repoint", Notice: stale}); err == nil {
		t.Fatal("a re-point at a lower membership generation was accepted")
	}
	forged := good
	forged.Membership.Generation = state.Membership.Generation + 5
	forged.Signature = good.Signature
	if _, err := applyPoolRepoint(member, caller, protocol.PoolTransferRequest{OperationID: "op-stale", Action: "repoint", Notice: forged}); err == nil {
		t.Fatal("an unsigned re-point was accepted")
	}
	if peerState(t, member).Membership.Generation != state.Membership.Generation {
		t.Fatal("a refused re-point changed the membership anyway")
	}
}

// Cancelling before the hand-off unfreezes the pool and keeps the current master; after the
// hand-off it is allowed only because the target proves it never activated, and that
// decision is durable, so the same activation can never be completed later.
func TestPoolTransferAbortRule(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, target := lab.host("master-1"), lab.host("member-1")

	lab.drop("member-1", "!*")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-abort", "master-change", "member-1", "master-1")); result.Status == "succeeded" {
		t.Fatalf("interrupted master change = %#v", result)
	}
	lab.requireNoWriter("while frozen before the hand-off")
	lab.drop("member-1", "")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-abort", "transfer-cancel", "member-1", "master-1")); result.Status != "succeeded" {
		t.Fatalf("cancel = %#v", result)
	}
	lab.requireSingleMaster("after cancelling before the hand-off", "master-1")

	// Now a hand-off the target never receives: the replica is pushed and acknowledged, the
	// master signs its authority away, and the activation itself never arrives.
	lab.drop("member-1", "!"+poolTransferPath)
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-abort-2", "master-change", "member-1", "master-1")); result.Status == "succeeded" {
		t.Fatalf("a master change whose activation never arrived succeeded: %#v", result)
	}
	lab.drop("member-1", "")
	record := peerState(t, master).Transfer
	if record == nil || !record.HandedOff {
		t.Fatalf("hand-off record = %#v", record)
	}
	lab.requireNoWriter("after a hand-off the target could not activate")

	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-abort-2", "transfer-cancel", "member-1", "master-1")); result.Status != "succeeded" {
		t.Fatalf("cancel after the hand-off = %#v", result)
	}
	lab.requireSingleMaster("after cancelling a hand-off the target never activated", "master-1")
	// The target durably refuses that operation for good, so the abort and an activation
	// can never both succeed.
	if targetRecord := peerState(t, target).Transfer; targetRecord == nil || targetRecord.Stage != "aborted" {
		t.Fatalf("the target did not record the abort: %#v", targetRecord)
	}
	if err := PushPoolAuthorityReplicas(context.Background(), master, 20*time.Second); err != nil {
		t.Fatalf("replica push after the abort: %v", err)
	}
	notice := protocol.PoolAuthorityNotice{
		PoolID: "pool-1", OperationID: "op-abort-2", Action: "promote", FromHostID: "master-1",
		ToHostID: "member-1", TargetHostID: "member-1", AuthorityGeneration: storedReplica(t, target).Replica.Generation,
		IssuedAt: time.Now().UTC(),
	}
	if err := signPoolAuthorityNotice(peerState(t, master).CAKeyPEM, &notice); err != nil {
		t.Fatal(err)
	}
	caller, _ := poolMemberByID(peerState(t, target).Membership, "master-1")
	if _, err := activatePoolAuthority(target, caller, protocol.PoolTransferRequest{OperationID: "op-abort-2", Action: "activate", Notice: notice}); err == nil {
		t.Fatal("an aborted activation was performed later")
	}
	lab.requireSingleMaster("at the end", "master-1")
}

func TestForwardOnlyMasterChangeNamesTheMasterChange(t *testing.T) {
	record := &poolTransferRecord{Action: "promote", Role: "source", TargetHostID: "member-1", HandedOff: true}
	if got := poolTransferForwardOnly(record); !strings.Contains(got, "master change") || strings.Contains(got, "eject") {
		t.Fatalf("forward-only master-change message = %q", got)
	}
}

// A cancel can arrive after the target has activated or after the source has learned its
// demotion. Both participants retain the completed transfer, so both must return the same
// forward-only result instead of pretending no transfer exists or that the old source may
// start another transfer.
func TestExecutePoolTransferReportsForwardOnlyAfterActivationOnBothParticipants(t *testing.T) {
	lab := newTransferLab(t, 1)
	source, target := lab.host("master-1"), lab.host("member-1")
	if result := ExecutePoolTransfer(context.Background(), source, transferCommand("op-forward-only", "master-change", target.State.HostID, source.State.HostID)); result.Status != "succeeded" {
		t.Fatalf("master change = %#v", result)
	}
	if got := peerState(t, target).BootAuthority; got == nil || got.PoolID != target.State.PoolID || got.HostID != target.State.HostID || got.BootID == "" {
		t.Fatalf("activated target has no bound current-boot authority marker: %#v", got)
	}
	for _, host := range []*Store{source, target} {
		cmd := transferCommand("cancel-forward-only-"+host.State.HostID, "transfer-cancel", target.State.HostID, target.State.HostID)
		cmd.Pool.ExpectedVersion = 1 // A Controller can retain the pre-handoff generation.
		result := ExecutePoolTransfer(context.Background(), host, cmd)
		if result.Status != "failed" || !strings.Contains(result.Error, "target has already activated") || !strings.Contains(result.Error, "can only go forward") {
			t.Fatalf("cancel through %s = %#v transfer=%#v", host.State.HostID, result, peerState(t, host).Transfer)
		}
	}
	wrongTarget := transferCommand("cancel-forward-only-wrong-target", "transfer-cancel", "another-member", target.State.HostID)
	wrongTarget.Pool.ExpectedVersion = peerState(t, target).Membership.Generation
	if result := ExecutePoolTransfer(context.Background(), target, wrongTarget); result.Status != "failed" || !strings.Contains(result.Error, "another target") {
		t.Fatalf("irreversible wrong-target cancel = %#v", result)
	}
}

func TestCancelKeepsAuthorityAndVersionGuardsUntilTheTransferIsIrreversible(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, target := lab.host("master-1"), lab.host("member-1")
	lab.drop(target.State.HostID, "!*")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-stale-cancel", "master-change", target.State.HostID, master.State.HostID)); result.Status == "succeeded" {
		t.Fatalf("interrupted master change = %#v", result)
	}
	stale := transferCommand("cancel-stale", "transfer-cancel", target.State.HostID, master.State.HostID)
	stale.Pool.ExpectedVersion = peerState(t, master).Membership.Generation - 1
	if result := ExecutePoolTransfer(context.Background(), master, stale); result.Status != "failed" || !strings.Contains(result.Error, "membership changed") {
		t.Fatalf("cancellable stale cancel = %#v", result)
	}
	wrongTarget := transferCommand("cancel-wrong-target", "transfer-cancel", "another-member", master.State.HostID)
	wrongTarget.Pool.ExpectedVersion = peerState(t, master).Membership.Generation
	if result := ExecutePoolTransfer(context.Background(), master, wrongTarget); result.Status != "failed" || !strings.Contains(result.Error, "another target") {
		t.Fatalf("cancellable wrong-target cancel = %#v", result)
	}
}

func TestIrreversibleMasterChangeReceiptExcludesUnactivatedPromotionCandidate(t *testing.T) {
	if irreversibleMasterChangeReceipt(&poolTransferRecord{Action: "promote", Role: "candidate", TargetHostID: "member-1", Stage: "fence"}, "member-1") {
		t.Fatal("an unactivated promotion candidate was treated as an activated master-change target")
	}
	if !irreversibleMasterChangeReceipt(&poolTransferRecord{Action: "promote", Role: "target", TargetHostID: "member-1", Stage: "repoint"}, "member-1") {
		t.Fatal("activated master-change target was not recognized")
	}
}

// Ejecting a member removes it at the next membership generation, tells it to leave, and
// leaves it the master of a new single-host pool of its own with the same host ID.
func TestPoolEjectReinitializesTheEjectedHost(t *testing.T) {
	lab := newTransferLab(t, 2)
	master, ejected, kept := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	writePoolVMRecord(t, master, labVM("pool-1", "66666666-6666-4666-8666-666666666666", "member-2"))
	before := peerState(t, master).Membership.Generation

	result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-eject", "eject", "member-1", "master-1"))
	if result.Status != "succeeded" {
		t.Fatalf("eject = %#v", result)
	}
	lab.requireSingleMaster("after the eject", "master-1")
	membership := peerState(t, master).Membership
	if membership.Generation != before+1 {
		t.Fatalf("membership generation = %d, want %d", membership.Generation, before+1)
	}
	if member, _ := poolMemberByID(membership, "member-1"); member.Status != "ejected" {
		t.Fatalf("ejected member row = %#v", member)
	}
	if peerState(t, kept).Membership.Generation != before+1 {
		t.Fatal("the remaining member was not re-pointed at the new membership")
	}
	// The ejected host recorded the eject but performs it on the goroutine that owns its
	// Store, exactly as a completed join does.
	if record := peerState(t, ejected).Transfer; record == nil || record.Role != "ejected" || record.Stage != "leave" {
		t.Fatalf("ejected host record = %#v", peerState(t, ejected).Transfer)
	}
	// Its certificate is no longer admitted by the pool it left: membership pinning, not a
	// revocation list, is what rejects it.
	if status, _ := peerVMRequest(t, ejected, poolVMRequest{VMID: "66666666-6666-4666-8666-666666666666", Action: "stopped"}); status != http.StatusUnauthorized {
		t.Fatalf("the pool still admits the ejected host's certificate: %d", status)
	}
	oldPool := ejected.State.PoolID
	if !ResumePoolLeave(context.Background(), ejected) {
		t.Fatal("the ejected host did not re-initialize")
	}
	state := peerState(t, ejected)
	if ejected.State.PoolID == oldPool || ejected.State.PoolID == "" {
		t.Fatalf("the ejected host kept its old pool ID %q", ejected.State.PoolID)
	}
	if ejected.State.HostID != "member-1" {
		t.Fatal("the ejected host changed its host ID")
	}
	if state.Membership.MasterHostID != "member-1" || state.Membership.PoolID != ejected.State.PoolID || len(state.Membership.Members) != 1 {
		t.Fatalf("the ejected host is not the master of its own new pool: %#v", state.Membership)
	}
	if state.CAKeyPEM == "" || state.CAPEM == "" {
		t.Fatal("the ejected host has no pool CA of its own")
	}
	if loaded, err := loadPoolReplica(ejected); err != nil || loaded != nil {
		t.Fatalf("the ejected host kept the old pool's authority replica: %#v (%v)", loaded, err)
	}
	if vms := poolVMRecords(t, ejected); len(vms) != 0 {
		t.Fatalf("the ejected host kept the old pool's VM ownership records: %#v", vms)
	}
}

// An eject is refused while the pool still expects that host to run or hold something.
func TestPoolEjectRefusesAHostThatStillOwnsSomething(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	vmID := "77777777-7777-4777-8777-777777777777"
	writePoolVMRecord(t, master, labVM("pool-1", vmID, "member-1"))
	result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-refuse", "eject", "member-1", "master-1"))
	if result.Status != "failed" || !strings.Contains(result.Error, "still owns pool VM records") {
		t.Fatalf("eject with a guest = %#v", result)
	}
	lab.requireSingleMaster("after a refused eject", "master-1")
	if record := peerState(t, master).Transfer; record == nil || record.Frozen {
		t.Fatalf("a refused eject left the authority frozen: %#v", record)
	}

	// Remove the VM record and hold a storage reservation instead.
	poolVMMu.Lock()
	state, err := loadPoolVMs(master)
	if err == nil {
		delete(state.VMs, vmID)
		err = savePoolVMs(master, state)
	}
	poolVMMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	poolStorageMu.Lock()
	storage, storageErr := loadPoolStorage(master)
	if storageErr == nil {
		if storage.Reservations == nil {
			storage.Reservations = map[string]map[string]string{}
		}
		storage.Reservations["repo-1"] = map[string]string{"member-1": "op-x"}
		storageErr = savePoolStorage(master, storage)
	}
	poolStorageMu.Unlock()
	if storageErr != nil {
		t.Fatal(storageErr)
	}
	result = ExecutePoolTransfer(context.Background(), master, transferCommand("op-refuse-2", "eject", "member-1", "master-1"))
	if result.Status != "failed" || !strings.Contains(result.Error, "storage reservation") {
		t.Fatalf("eject with a reservation = %#v", result)
	}
	lab.requireSingleMaster("after the second refused eject", "master-1")
}

// A host ejected while it was unreachable does not keep acting as a member when it returns:
// the pool it followed tells it, over its own pinned connection, that its certificate is no
// longer admitted at a newer membership generation.
func TestEjectedHostLearnsOnReturn(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, ejected := lab.host("master-1"), lab.host("member-1")
	lab.drop("member-1", "!*")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-gone", "eject", "member-1", "master-1")); result.Status != "succeeded" {
		t.Fatalf("eject = %#v", result)
	}
	if record := peerState(t, ejected).Transfer; record != nil {
		t.Fatalf("the unreachable host was told anyway: %#v", record)
	}
	lab.drop("member-1", "")
	ConvergePoolAuthority(context.Background(), ejected, 20*time.Second)
	record := peerState(t, ejected).Transfer
	if record == nil || record.Role != "ejected" || record.Stage != "leave" {
		t.Fatalf("the returning host did not learn it was ejected: %#v", record)
	}
	if !ResumePoolLeave(context.Background(), ejected) {
		t.Fatal("the returning host did not re-initialize")
	}
	if peerState(t, ejected).Membership.MasterHostID != "member-1" {
		t.Fatal("the returning host is not the master of its own pool")
	}
}

// A terminal eject receipt is historical after this host has rejoined the pool. It must not
// hide a later eject that was missed because the master's direct leave notice could not
// reach this host; the authenticated authority probe is the recovery path in that case.
func TestRejoinedHostLearnsLaterMissedEjectDespiteHistoricalTerminalReceipt(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	poolPeerMu.Lock()
	state, err := loadPoolPeer(member)
	if err == nil {
		state.Transfer = &poolTransferRecord{
			OperationID: "historical-eject", Action: "eject", Role: "ejected", TargetHostID: member.State.HostID,
			Stage: "done", Status: "succeeded", MembershipGeneration: state.Membership.Generation - 1,
		}
		err = savePoolPeer(member, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// The stale peer endpoint/missed packet affects the optional direct notice only; the
	// master membership change remains durable and the returning member must learn it later.
	lab.drop("member-1", "!*")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("later-eject", "eject", "member-1", "master-1")); result.Status != "succeeded" {
		t.Fatalf("eject = %#v", result)
	}
	lab.drop("member-1", "")
	ConvergePoolAuthority(context.Background(), member, 20*time.Second)
	record := peerState(t, member).Transfer
	if record == nil || record.OperationID != "master-1-eject-member-1" || record.Role != "ejected" || record.Stage != "leave" {
		t.Fatalf("later missed eject did not replace historical receipt: %#v", record)
	}
	if !ResumePoolLeave(context.Background(), member) {
		t.Fatal("member did not re-initialize after the later missed eject")
	}
	if peerState(t, member).Membership.MasterHostID != "member-1" {
		t.Fatal("member was not re-initialized as its own pool master")
	}
}

func TestPoolTransferCheckPublishesFreshHealthyAfterSuccess(t *testing.T) {
	lab := newTransferLab(t, 1)
	host := lab.host("master-1")
	record := &poolTransferRecord{OperationID: "transfer-clear", Action: "eject", Role: "source", TargetHostID: "member-1", Stage: "freeze", LastError: "transfer refused"}
	if err := savePoolTransfer(host, record); err != nil {
		t.Fatal(err)
	}
	failed := PoolTransferCheck(host)
	if len(failed) != 1 || failed[0].Status != "failed" || failed[0].Freshness != "fresh" {
		t.Fatalf("failed transfer check = %#v", failed)
	}
	record.Stage, record.LastError = "done", ""
	if err := savePoolTransfer(host, record); err != nil {
		t.Fatal(err)
	}
	cleared := PoolTransferCheck(host)
	if len(cleared) != 1 || cleared[0].Name != poolTransferCheck || cleared[0].Status != "healthy" || cleared[0].Freshness != "fresh" || cleared[0].ObservedAt.IsZero() || cleared[0].Error != "" {
		t.Fatalf("successful transfer check = %#v", cleared)
	}
}

func TestPoolTransferCheckPublishesFreshHealthyWhenNoTransferRemains(t *testing.T) {
	lab := newTransferLab(t, 1)
	host := lab.host("master-1")
	if err := savePoolTransfer(host, &poolTransferRecord{OperationID: "transfer-old", Action: "promote", Role: "candidate", TargetHostID: "member-1", Stage: "activate"}); err != nil {
		t.Fatal(err)
	}
	if check := PoolTransferCheck(host); len(check) != 1 || check[0].Status != "unknown" {
		t.Fatalf("active transfer check = %#v", check)
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(host)
	if err == nil {
		state.Transfer = nil
		err = savePoolPeer(host, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cleared := PoolTransferCheck(host)
	if len(cleared) != 1 || cleared[0].Name != poolTransferCheck || cleared[0].Status != "healthy" || cleared[0].Freshness != "fresh" || cleared[0].ObservedAt.IsZero() || cleared[0].Error != "" {
		t.Fatalf("absent transfer check = %#v", cleared)
	}
}

func TestPoolOperationChecksOmitUnreadablePeerState(t *testing.T) {
	lab := newTransferLab(t, 1)
	host := lab.host("master-1")
	if err := os.WriteFile(poolStatePath(host), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if check := PoolTransferCheck(host); check != nil {
		t.Fatalf("unreadable transfer check = %#v", check)
	}
	if check := PoolPromotionCheck(host); check != nil {
		t.Fatalf("unreadable promotion check = %#v", check)
	}
	if check := PoolStorageReconcileCheck(host); check != nil {
		t.Fatalf("unreadable storage reconciliation check = %#v", check)
	}
	if checks := PoolConvergeChecks(host); checks != nil {
		t.Fatalf("unreadable converge checks = %#v", checks)
	}
	if checks := PoolReplicaChecks(host, time.Minute); checks != nil {
		t.Fatalf("unreadable replica checks = %#v", checks)
	}
}

// A transfer is refused outright when another one is in flight, when the caller is not the
// master, or when the target is not a ready member with a peer endpoint.
func TestPoolTransferRefusals(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	if result := ExecutePoolTransfer(context.Background(), member, transferCommand("op-x", "master-change", "master-1", "member-1")); result.Status != "failed" || !strings.Contains(result.Error, "only the pool master") {
		t.Fatalf("a member started a master change: %#v", result)
	}
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-y", "master-change", "nobody", "master-1")); result.Status != "failed" || !strings.Contains(result.Error, "no such member") {
		t.Fatalf("unknown target = %#v", result)
	}
	setMemberStatus(t, master, "member-1", "pending")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-z", "master-change", "member-1", "master-1")); result.Status != "failed" || !strings.Contains(result.Error, "ready member") {
		t.Fatalf("pending target = %#v", result)
	}
	lab.requireSingleMaster("after every refusal", "master-1")
	setMemberStatus(t, master, "member-1", "ready")
	lab.drop("member-1", "!*")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-1", "master-change", "member-1", "master-1")); result.Status == "succeeded" {
		t.Fatalf("interrupted transfer = %#v", result)
	}
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-2", "eject", "member-1", "master-1")); result.Status != "failed" || !strings.Contains(result.Error, "already in progress") {
		t.Fatalf("a second transfer started: %#v", result)
	}
}

// Authority writes attempted concurrently with a freeze are all refused with the one
// retryable sentence, and none of them slips into the frozen authority: the guard is read
// inside the same lock section that would perform the write.
func TestConcurrentAuthorityWritesDuringAFreezeAreRefused(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, target := lab.host("master-1"), lab.host("member-1")
	vmID := "88888888-8888-4888-8888-888888888888"
	writePoolVMRecord(t, master, labVM("pool-1", vmID, "member-1"))
	lab.drop("member-1", "!*")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-race", "master-change", "member-1", "master-1")); result.Status == "succeeded" {
		t.Fatalf("the transfer completed with an unreachable target: %#v", result)
	}
	lab.requireNoWriter("while frozen")

	var wg sync.WaitGroup
	refusals := make(chan string, 24)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, body := peerVMRequest(t, target, poolVMRequest{VMID: vmID, Action: "start"})
			refusals <- fmt.Sprintf("%d:%s", status, strings.TrimSpace(body))
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := CreatePoolJoinInvitation(master, lab.origin["master-1"], "member-9")
			refusals <- fmt.Sprintf("invite:%v", err)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			refusals <- fmt.Sprintf("registry:%v", poolRegistryCommandWritable(master, protocol.Command{Kind: "network-register"}))
		}()
	}
	wg.Wait()
	close(refusals)
	for refusal := range refusals {
		if !strings.Contains(refusal, poolAuthorityFrozenRefusal) {
			t.Fatalf("an authority write was not refused with the retryable sentence: %s", refusal)
		}
	}
	// The frozen authority is unchanged: no epoch moved, no member was admitted.
	record := poolVMRecords(t, master)[vmID]
	if record.Epoch != 7 || record.State != "stopped" {
		t.Fatalf("a frozen pool changed a VM record: %#v", record)
	}
	membership := peerState(t, master).Membership
	if len(membership.Members) != 2 {
		t.Fatalf("a frozen pool admitted a member: %#v", membership.Members)
	}
	lab.requireNoWriter("after every refused write")
	lab.drop("member-1", "")
	AdvancePoolAuthorityTransfer(context.Background(), master, 20*time.Second)
	AdvancePoolAuthorityTransfer(context.Background(), target, 20*time.Second)
	lab.requireSingleMaster("after the freeze was lifted by completing the transfer", "member-1")
}

// Defect 7, Core side: a pool master that holds an ISO only because it adopted the file
// from shared storage while it was a member refuses to remove it, and says where the file
// actually lives, instead of dropping the record and leaving the file behind.
func TestPoolMasterRefusesToRemoveAnAdoptedISO(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	media := adoptSharedMediaForTest(t, master, "media-adopted-1")
	command := protocol.Command{OperationID: "remove-1", Kind: "media-remove", ResourceLifecycle: &protocol.ResourceLifecycleRequest{
		Action: "remove", Resource: "media", ResourceID: media.ID, PoolID: "pool-1", HostID: "master-1", ExpectedGeneration: protocol.MediaGeneration(media),
	}}
	err := BeginPoolResourceRemoval(context.Background(), master, command)
	if err == nil || !strings.Contains(err.Error(), "adopted this ISO from shared storage") {
		t.Fatalf("adopted ISO removal = %v", err)
	}
	// An ISO this host did not adopt is judged by the ordinary dependency rules instead.
	other := command
	other.ResourceLifecycle = &protocol.ResourceLifecycleRequest{Action: "remove", Resource: "media", ResourceID: "media-imported-1", PoolID: "pool-1", HostID: "master-1"}
	if err = BeginPoolResourceRemoval(context.Background(), master, other); err != nil && strings.Contains(err.Error(), "adopted this ISO") {
		t.Fatalf("an ISO this host did not adopt was refused as adopted: %v", err)
	}
}

// adoptSharedMediaForTest publishes an ISO on registered storage and records it the way a
// member's own convergence pass does: an adoption of a file another host imported.
func adoptSharedMediaForTest(t *testing.T, s *Store, mediaID string) protocol.Media {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := json.Marshal(map[string]any{
		"command": protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "storage-1", PoolID: "pool-1", HostID: s.State.HostID, Name: "shared", Path: repo}},
		"result":  protocol.Result{OperationID: "storage-1", Status: "succeeded"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "storage-1.json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 40<<10)
	copy(body[32769:], "CD001")
	if err = os.MkdirAll(filepath.Join(repo, "nex-media"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "nex-media", mediaID+".iso"), body, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	media := protocol.Media{ID: mediaID, PoolID: "pool-1", HostID: s.State.HostID, StorageID: "storage-1", Name: "shared.iso", SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	if err = vmhost.NewManaged(dir).AdoptSharedMedia(context.Background(), media); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	return media
}

// A cancel used to be accepted after an eject was already durable: the master recorded
// stage "aborted" without restoring the membership, so the ejected host was never told to
// leave and the remaining members were left to converge on the probe alone. An eject is
// irreversible from the membership write on, so the cancel is refused and the eject
// completes on the next tick.
func TestPoolEjectCannotBeCancelledOnceTheMembershipIsWritten(t *testing.T) {
	lab := newTransferLab(t, 2)
	master, ejected, kept := lab.host("master-1"), lab.host("member-1"), lab.host("member-2")
	before := peerState(t, master).Membership.Generation

	// The remaining member is unreachable, so the eject stalls at the re-point with the
	// membership removal already written at the next generation.
	lab.drop("member-2", "!*")
	if result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-eject-cancel", "eject", "member-1", "master-1")); result.Status == "succeeded" {
		t.Fatalf("interrupted eject = %#v", result)
	}
	if generation := peerState(t, master).Membership.Generation; generation != before+1 {
		t.Fatalf("membership generation = %d, want %d", generation, before+1)
	}
	if record := peerState(t, master).Transfer; record == nil || record.Stage != "repoint" {
		t.Fatalf("eject record = %#v", record)
	}

	result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-eject-cancel", "transfer-cancel", "member-1", "master-1"))
	if !strings.Contains(result.Error, "can only go forward") {
		t.Fatalf("cancel after the membership removal = %#v", result)
	}
	// Nothing the cancel touched: the eject is still in progress and the membership still
	// excludes the ejected host, so the pool is not left with an aborted record and a
	// removed member at once.
	record := peerState(t, master).Transfer
	if record == nil || record.Stage != "repoint" {
		t.Fatalf("the cancel changed a durable eject: %#v", record)
	}
	if member, _ := poolMemberByID(peerState(t, master).Membership, "member-1"); member.Status != "ejected" {
		t.Fatalf("membership row after the cancel = %#v", member)
	}
	lab.requireSingleMaster("after the refused cancel", "master-1")

	lab.drop("member-2", "")
	AdvancePoolAuthorityTransfer(context.Background(), master, 20*time.Second)
	if record = peerState(t, master).Transfer; record == nil || record.Stage != "done" {
		t.Fatalf("the eject did not complete on the next tick: %#v", record)
	}
	lab.requireSingleMaster("after the eject completed", "master-1")
	if peerState(t, kept).Membership.Generation != before+1 {
		t.Fatal("the remaining member was not re-pointed at the new membership")
	}
	if record = peerState(t, ejected).Transfer; record == nil || record.Role != "ejected" || record.Stage != "leave" {
		t.Fatalf("the ejected host was not told to leave: %#v", record)
	}
}

// Cancellability is one predicate, reported on the wire and enforced by the cancel itself,
// so no caller has to infer it from stage names and the two can never disagree.
func TestPoolTransferCancellableMatchesTheCancelAtEveryStage(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	cases := []struct {
		name        string
		record      poolTransferRecord
		cancellable bool
	}{
		{"master-change freeze", poolTransferRecord{Action: "promote", Role: "source", Stage: "freeze", Frozen: true}, true},
		{"master-change replicate", poolTransferRecord{Action: "promote", Role: "source", Stage: "replicate", Frozen: true}, true},
		{"master-change activate", poolTransferRecord{Action: "promote", Role: "source", Stage: "activate", Frozen: true}, true},
		{"master-change handed off", poolTransferRecord{Action: "promote", Role: "source", Stage: "activate", Frozen: true, HandedOff: true}, true},
		{"master-change done", poolTransferRecord{Action: "promote", Role: "source", Stage: "done"}, false},
		{"master-change aborted", poolTransferRecord{Action: "promote", Role: "source", Stage: "aborted"}, false},
		{"eject freeze", poolTransferRecord{Action: "eject", Role: "source", Stage: "freeze", Frozen: true}, true},
		{"eject remove", poolTransferRecord{Action: "eject", Role: "source", Stage: "remove", Frozen: true}, true},
		{"eject repoint", poolTransferRecord{Action: "eject", Role: "source", Stage: "repoint"}, false},
		{"eject leave", poolTransferRecord{Action: "eject", Role: "source", Stage: "leave"}, false},
		{"eject done", poolTransferRecord{Action: "eject", Role: "source", Stage: "done"}, false},
		{"promotion preflight", poolTransferRecord{Action: "promote", Role: "candidate", Stage: "preflight"}, false},
		{"promotion fence", poolTransferRecord{Action: "promote", Role: "candidate", Stage: "fence"}, false},
		{"promotion activate", poolTransferRecord{Action: "promote", Role: "candidate", Stage: "activate"}, false},
		{"promotion repoint", poolTransferRecord{Action: "promote", Role: "candidate", Stage: "repoint"}, false},
	}
	for _, c := range cases {
		record := c.record
		record.OperationID, record.TargetHostID = "op-"+c.name, "member-1"
		status := poolTransferStatus(&record)
		if status.Cancellable != c.cancellable {
			t.Fatalf("%s: reported cancellable = %v, want %v", c.name, status.Cancellable, c.cancellable)
		}
		// The irreversible step is reported too, so a caller reads it instead of inferring
		// it from the stage or from a refusal sentence.
		if status.HandedOff != c.record.HandedOff {
			t.Fatalf("%s: reported handed_off = %v, want %v", c.name, status.HandedOff, c.record.HandedOff)
		}
		if err := savePoolTransfer(master, &record); err != nil {
			t.Fatal(err)
		}
		_, err := cancelPoolTransfer(context.Background(), master, record.OperationID, "member-1", nil)
		if (err == nil) != c.cancellable {
			t.Fatalf("%s: cancel error = %v, reported cancellable = %v", c.name, err, c.cancellable)
		}
		if err != nil && !c.record.HandedOff && !poolTransferTerminal(c.record.Stage) && !strings.Contains(err.Error(), "can only go forward") {
			t.Fatalf("%s: cancel refusal = %v", c.name, err)
		}
	}
}

// If the peer-state write that completes activation fails after VM adoption, the target
// must not remain half-adopted without the master role.
func TestAdoptPoolAuthorityRollsBackVMsWhenPeerSaveFails(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, target := lab.host("master-1"), lab.host("member-1")
	localID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	replicatedID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	writePoolVMRecord(t, target, labVM("pool-1", localID, "member-1"))
	writePoolVMRecord(t, master, labVM("pool-1", replicatedID, "member-1"))
	pushReplicas(t, master)
	replica := storedReplica(t, target)
	record := &poolTransferRecord{
		OperationID: "op-adopt-rollback", Action: "promote", Role: "target",
		TargetHostID: "member-1", Stage: "activate",
	}
	poolTransferFailSavePeerAfterVMAdopt = func() error {
		return errors.New("test injected save failure")
	}
	t.Cleanup(func() { poolTransferFailSavePeerAfterVMAdopt = nil })
	if _, err := adoptPoolAuthorityFromReplica(target, replica.Replica, record); err == nil {
		t.Fatal("activation should fail when the peer-state write fails")
	}
	after := poolVMRecords(t, target)
	if _, ok := after[localID]; !ok {
		t.Fatal("the pre-adopt VM record was not restored")
	}
	if _, ok := after[replicatedID]; ok {
		t.Fatal("the adopted VM record was not rolled back")
	}
	if peerState(t, target).Membership.MasterHostID != "master-1" {
		t.Fatal("the target recorded itself master without a durable peer write")
	}
}

// After the hand-off is durable, a failed activation must say authority was handed off and
// name the only recovery paths: cancel or fence-backed resume.
func TestHandedOffActivateFailureNamesRecovery(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, target := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	lab.drop(target.State.HostID, "!"+poolTransferPath)
	result := ExecutePoolTransfer(context.Background(), master, transferCommand("op-activate-msg", "master-change", "member-1", "master-1"))
	if result.Status == "succeeded" {
		t.Fatalf("activation refused = %#v", result)
	}
	record := peerState(t, master).Transfer
	if record == nil || !record.HandedOff {
		t.Fatalf("hand-off record = %#v", record)
	}
	if record.LastError == "" || !strings.Contains(record.LastError, "handed to member-1") || !strings.Contains(record.LastError, "cancel the transfer") || !strings.Contains(record.LastError, "powering off") {
		t.Fatalf("handed-off activation error = %q", record.LastError)
	}
}

// TestNextPoolMembershipKeepsJoinTimes: the Controller reports a member's observed_at as
// when it joined, which holds only while a master change or eject leaves it alone.
func TestNextPoolMembershipKeepsJoinTimes(t *testing.T) {
	joined := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	current := protocol.PoolMembership{PoolID: "pool-a", MasterHostID: "a", Generation: 4, Members: []protocol.PoolMember{
		{HostID: "a", Status: "ready", ObservedAt: joined}, {HostID: "b", Status: "ready", ObservedAt: joined.Add(time.Hour)}}}
	next := nextPoolMembership(current, "b", "a")
	if !next.Members[0].ObservedAt.Equal(joined) || !next.Members[1].ObservedAt.Equal(joined.Add(time.Hour)) {
		t.Fatalf("join times moved: %+v", next.Members)
	}
}
