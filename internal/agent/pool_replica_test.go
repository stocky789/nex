// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// serveMemberPeer starts the member's own peer listener and records it on the master as
// that member's advertised origin, which is the only endpoint a master will contact.
func serveMemberPeer(t *testing.T, master, member *Store) *httptest.Server {
	t.Helper()
	config, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(member))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	setMemberPeerOrigin(t, master, member.State.HostID, server.URL)
	t.Cleanup(func() { ClearPoolReplicaStateForTest(master); ClearPoolReplicaStateForTest(member) })
	return server
}

func TestPoolReplicaCheckPublishesFreshHealthyWhenMasterHasNoReadyMembers(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	recordPoolReplicaAck(master, member.State.HostID, protocol.PoolReplicaStatus{Error: "replica delivery failed"})
	if checks := PoolReplicaChecks(master, time.Minute); len(checks) != 1 || checks[0].Status != "failed" {
		t.Fatalf("member replica failure = %#v", checks)
	}
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
	checks := PoolReplicaChecks(master, time.Minute)
	if len(checks) != 1 || checks[0].Name != poolReplicaCheck || checks[0].Status != "healthy" || checks[0].Freshness != "fresh" || checks[0].ObservedAt.IsZero() || checks[0].Error != "" {
		t.Fatalf("solo master replica clear = %#v", checks)
	}
}

func TestPoolReplicaCheckUsesItsFirstValidatedMembershipSnapshot(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	previous := poolReadyMembership
	poolReadyMembership = func(*Store) (protocol.PoolMembership, error) {
		return protocol.PoolMembership{}, errors.New("second membership read failed")
	}
	t.Cleanup(func() { poolReadyMembership = previous })
	checks := PoolReplicaChecks(master, time.Minute)
	if len(checks) != 1 || checks[0].Name != poolReplicaCheck || checks[0].Status != "unknown" || checks[0].Freshness != "unknown" {
		t.Fatalf("first membership snapshot was discarded: %#v", checks)
	}
}

func setMemberPeerOrigin(t *testing.T, master *Store, hostID, origin string) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID == hostID {
			state.Membership.Members[i].PeerOrigin = origin
		}
	}
	if err = savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
}

func setMemberStatus(t *testing.T, master *Store, hostID, status string) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID == hostID {
			state.Membership.Members[i].Status = status
		}
	}
	if err = savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
}

func memberPeerKeyPEM(t *testing.T, s *Store) string {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		t.Fatal(err)
	}
	return state.KeyPEM
}

func storedReplica(t *testing.T, s *Store) *poolReplicaRecord {
	t.Helper()
	record, err := loadPoolReplica(s)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// A full push: the master signs its own durable authority, the member verifies it against
// the pool CA it already holds, stores it privately, and changes nothing because of it.
func serveMemberPeerCountingPosts(t *testing.T, master, member *Store, posts *atomic.Int32) *httptest.Server {
	t.Helper()
	config, err := PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	inner := PoolPeerHandler(member)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == poolReplicaPath {
			posts.Add(1)
		}
		inner.ServeHTTP(w, r)
	})
	server := httptest.NewUnstartedServer(handler)
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	setMemberPeerOrigin(t, master, member.State.HostID, server.URL)
	t.Cleanup(func() { ClearPoolReplicaStateForTest(master); ClearPoolReplicaStateForTest(member) })
	return server
}

// POST is skipped only after this master has a successful POST ack for the member, never
// because the member's GET alone reports a matching generation.
func TestPoolReplicaPushShortCircuitsOnPostAckOnly(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	var posts atomic.Int32
	serveMemberPeerCountingPosts(t, master, member, &posts)
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("first push POST count = %d, want 1", posts.Load())
	}
	// A restart clears POST acks while the member still holds the replica and GET matches.
	ClearPoolReplicaPostAcksForTest(master)
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("push after restart: %v", err)
	}
	if posts.Load() != 2 {
		t.Fatalf("push after restart POST count = %d, want 2", posts.Load())
	}
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("idempotent push: %v", err)
	}
	if posts.Load() != 2 {
		t.Fatalf("idempotent push POST count = %d, want 2", posts.Load())
	}
}

func TestPoolAuthorityReplicaRoundTrip(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	serveMemberPeer(t, master, member)
	vlan := protocol.NetworkRegistration{ID: "vlan-100", PoolID: "pool-1", HostID: master.State.HostID, Name: "Servers", Kind: "vlan", Source: protocol.GuestBridge("nex1", 100), Uplink: "nex1", VLAN: 100, Generation: 1}
	writeAppliedPoolNetwork(t, master, vlan)

	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("push: %v", err)
	}
	record := storedReplica(t, member)
	if record == nil || !record.Verified || record.Replica.Generation != 1 {
		t.Fatalf("stored replica = %#v", record)
	}
	if record.Replica.MemberHostID != member.State.HostID || record.Replica.MasterHostID != master.State.HostID || record.Replica.PoolID != "pool-1" {
		t.Fatalf("replica is not addressed to this member: %#v", record.Replica)
	}
	if len(record.Replica.Networks) != 1 || record.Replica.Networks[0].ID != vlan.ID {
		t.Fatalf("replica networks = %#v", record.Replica.Networks)
	}
	if record.Replica.CAKeyWrapped == nil || record.Replica.CAKeyWrapped.Ciphertext == "" {
		t.Fatal("replica carries no wrapped pool CA key")
	}
	if strings.Contains(record.Replica.CAKeyWrapped.Ciphertext, "PRIVATE KEY") {
		t.Fatal("pool CA key is not sealed")
	}
	info, err := os.Stat(poolReplicaFile(member))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("replica file mode = %v (%v)", info, err)
	}

	// The member acted on nothing: its own membership, pool VM records and networks are
	// exactly what they were, and the replica is not an authority for any of them.
	membership, err := Membership(member)
	if err != nil || membership.MasterHostID != master.State.HostID || len(membership.Members) != 2 {
		t.Fatalf("member membership changed: %#v (%v)", membership, err)
	}
	vms, err := loadPoolVMs(member)
	if err != nil || len(vms.VMs) != 0 {
		t.Fatalf("member adopted pool VM records: %#v (%v)", vms.VMs, err)
	}
	applied, err := vmhost.NewManaged(filepath.Join(member.dir, "managed-vms")).AppliedNetworks()
	if err != nil || len(applied) != 0 {
		t.Fatalf("member applied a replicated network: %#v (%v)", applied, err)
	}

	// Both sides report it as observed state, and the member's health check is honest.
	status := PoolReplicaReport(member)
	if status == nil || status.Generation != 1 || status.Digest != record.Digest || !status.Verified {
		t.Fatalf("member replica report = %#v", status)
	}
	reported := PoolReplicaMembership(master, &membership)
	found := false
	for _, m := range reported.Members {
		if m.HostID == member.State.HostID {
			found = m.ReplicaGeneration == 1 && m.ReplicaDigest == record.Digest && !m.ReplicaObservedAt.IsZero()
		}
	}
	if !found {
		t.Fatalf("master did not report the member's acknowledged replica: %#v", reported.Members)
	}
	for _, check := range PoolReplicaChecks(member, time.Minute) {
		if check.Name != poolReplicaCheck || check.Status != "healthy" {
			t.Fatalf("member replica check = %#v", check)
		}
	}
	for _, check := range PoolReplicaChecks(master, time.Minute) {
		if check.Status != "healthy" {
			t.Fatalf("master replica check = %#v", check)
		}
	}

	// Re-pushing an unchanged authority is idempotent: the generation does not move and
	// the member keeps exactly the same content digest.
	if err = PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("second push: %v", err)
	}
	again := storedReplica(t, member)
	if again.Replica.Generation != 1 || again.Digest != record.Digest {
		t.Fatalf("idempotent push changed the replica: %#v", again)
	}

	// A durable authority write advances the generation on the next push.
	writeAppliedPoolNetwork(t, master, protocol.NetworkRegistration{ID: "vlan-200", PoolID: "pool-1", HostID: master.State.HostID, Name: "Storage", Kind: "vlan", Source: protocol.GuestBridge("nex1", 200), Uplink: "nex1", VLAN: 200, Generation: 1})
	if err = PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("third push: %v", err)
	}
	next := storedReplica(t, member)
	if next.Replica.Generation != 2 || next.Digest == record.Digest || len(next.Replica.Networks) != 2 {
		t.Fatalf("authority change did not advance the replica: %#v", next.Replica)
	}
}

// Tampering with any replicated record invalidates the signature, and a section that is
// dropped or rewritten is refused rather than stored as a shorter authority.
func TestPoolAuthorityReplicaTamperIsRefused(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	serveMemberPeer(t, master, member)
	writeAppliedPoolNetwork(t, master, protocol.NetworkRegistration{ID: "vlan-100", PoolID: "pool-1", HostID: master.State.HostID, Name: "Servers", Kind: "vlan", Source: protocol.GuestBridge("nex1", 100), Uplink: "nex1", VLAN: 100, Generation: 1})
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("push: %v", err)
	}
	good := storedReplica(t, member).Replica
	caller := protocol.PoolMember{HostID: master.State.HostID}

	tampered := good
	tampered.Networks = append([]protocol.NetworkRegistration(nil), good.Networks...)
	tampered.Networks[0].Uplink = "attacker0"
	sections, err := tampered.SectionDigests()
	if err != nil {
		t.Fatal(err)
	}
	tampered.Sections = sections // even with honest digests, the CA signature fails.
	if _, err := acceptPoolAuthorityReplica(member, caller, tampered); err == nil || !strings.Contains(err.Error(), "not signed by this pool's CA") {
		t.Fatalf("tampered record accepted: %v", err)
	}

	rewritten := good
	rewritten.Sections = append([]protocol.PoolReplicaSection(nil), good.Sections...)
	rewritten.Sections[1].SHA256 = strings.Repeat("0", 64)
	if _, err := acceptPoolAuthorityReplica(member, caller, rewritten); err == nil || !strings.Contains(err.Error(), "section digest does not match") {
		t.Fatalf("rewritten section digest accepted: %v", err)
	}

	truncated := good
	truncated.Sections = good.Sections[:len(good.Sections)-1]
	if _, err := acceptPoolAuthorityReplica(member, caller, truncated); err == nil || !strings.Contains(err.Error(), "does not declare every section") {
		t.Fatalf("truncated replica accepted: %v", err)
	}

	foreign := good
	foreign.MemberHostID = "someone-else"
	if _, err := acceptPoolAuthorityReplica(member, caller, foreign); err == nil || !strings.Contains(err.Error(), "addressed to another host") {
		t.Fatalf("replica for another host accepted: %v", err)
	}

	if stored := storedReplica(t, member); stored.Digest == "" || stored.Replica.Networks[0].Uplink != "nex1" {
		t.Fatalf("a refusal replaced the stored replica: %#v", stored.Replica.Networks)
	}
	status := PoolReplicaReport(member)
	if status == nil || status.Generation != good.Generation {
		t.Fatalf("member replica report after refusals = %#v", status)
	}
}

// The wrapped pool CA key opens only on the member it was addressed to.
func TestPoolAuthorityReplicaWrapIsMemberOnly(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	first, second := peerStore(t, "member-1", "other-pool"), peerStore(t, "member-2", "another-pool")
	joinPeerMember(t, master, first)
	joinPeerMember(t, master, second)
	serveMemberPeer(t, master, first)
	serveMemberPeer(t, master, second)
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("push: %v", err)
	}
	firstReplica, secondReplica := storedReplica(t, first).Replica, storedReplica(t, second).Replica
	if firstReplica.CAKeyWrapped.Ciphertext == secondReplica.CAKeyWrapped.Ciphertext {
		t.Fatal("both members received the same wrapped key")
	}
	poolPeerMu.Lock()
	masterState, err := loadPoolPeer(master)
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	opened, err := unwrapPoolCAKey(memberPeerKeyPEM(t, first), firstReplica)
	if err != nil || opened != masterState.CAKeyPEM {
		t.Fatalf("intended member could not open its wrapped pool CA key: %v", err)
	}
	if _, err = unwrapPoolCAKey(memberPeerKeyPEM(t, second), firstReplica); err == nil {
		t.Fatal("another member opened a wrapped pool CA key")
	}
	// The same member cannot replay another generation's wrap either: the key schedule
	// is bound to pool, member and generation.
	replayed := firstReplica
	replayed.Generation++
	if _, err = unwrapPoolCAKey(memberPeerKeyPEM(t, first), replayed); err == nil {
		t.Fatal("a wrapped pool CA key opened at another generation")
	}
}

// Generation rules: a lower generation is refused, an equal generation with different
// contents is refused and reported, and an equal generation with the same contents is an
// idempotent acknowledgement.
func TestPoolAuthorityReplicaGenerationRules(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	serveMemberPeer(t, master, member)
	writeAppliedPoolNetwork(t, master, protocol.NetworkRegistration{ID: "vlan-100", PoolID: "pool-1", HostID: master.State.HostID, Name: "Servers", Kind: "vlan", Source: protocol.GuestBridge("nex1", 100), Uplink: "nex1", VLAN: 100, Generation: 1})
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("first push: %v", err)
	}
	writeAppliedPoolNetwork(t, master, protocol.NetworkRegistration{ID: "vlan-200", PoolID: "pool-1", HostID: master.State.HostID, Name: "Storage", Kind: "vlan", Source: protocol.GuestBridge("nex1", 200), Uplink: "nex1", VLAN: 200, Generation: 1})
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("second push: %v", err)
	}
	current := storedReplica(t, member)
	if current.Replica.Generation != 2 {
		t.Fatalf("stored generation = %d", current.Replica.Generation)
	}
	caller := protocol.PoolMember{HostID: master.State.HostID}

	signer, err := poolCASigner(master)
	if err != nil {
		t.Fatal(err)
	}
	older := current.Replica
	older.Generation = 1
	older.Networks = older.Networks[:1]
	if older.Sections, err = older.SectionDigests(); err != nil {
		t.Fatal(err)
	}
	if err = signPoolAuthorityReplica(signer, &older); err != nil {
		t.Fatal(err)
	}
	if _, err = acceptPoolAuthorityReplica(member, caller, older); err == nil || !strings.Contains(err.Error(), "older than the stored replica") {
		t.Fatalf("rollback accepted: %v", err)
	}

	conflicting := current.Replica
	conflicting.Networks = append([]protocol.NetworkRegistration(nil), conflicting.Networks...)
	conflicting.Networks[0].Name = "Renamed elsewhere"
	if conflicting.Sections, err = conflicting.SectionDigests(); err != nil {
		t.Fatal(err)
	}
	if err = signPoolAuthorityReplica(signer, &conflicting); err != nil {
		t.Fatal(err)
	}
	if _, err = acceptPoolAuthorityReplica(member, caller, conflicting); err == nil || !strings.Contains(err.Error(), "already stored with different contents") {
		t.Fatalf("conflicting same-generation replica accepted: %v", err)
	}

	ack, err := acceptPoolAuthorityReplica(member, caller, current.Replica)
	if err != nil || ack.Generation != 2 || ack.Digest != current.Digest || !ack.Verified {
		t.Fatalf("idempotent re-push = %#v (%v)", ack, err)
	}
	if stored := storedReplica(t, member); stored.Replica.Generation != 2 || stored.Digest != current.Digest {
		t.Fatalf("stored replica changed: %#v", stored)
	}
}

// A master that crashes after numbering its authority and before pushing resumes with the
// same generation. The generation never goes backwards and never renumbers unchanged work.
func TestPoolAuthorityGenerationSurvivesCrashBeforePush(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	_, digest, err := buildPoolAuthorityReplica(master)
	if err != nil {
		t.Fatal(err)
	}
	first, err := advancePoolAuthorityGeneration(master, digest)
	if err != nil || first != 1 {
		t.Fatalf("first generation = %d (%v)", first, err)
	}
	// The process is gone here: nothing was pushed. A new one loads the persisted value.
	if err = master.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenStore(master.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	reloaded.State.HostID, reloaded.State.PoolID = master.State.HostID, master.State.PoolID
	_, sameDigest, err := buildPoolAuthorityReplica(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := advancePoolAuthorityGeneration(reloaded, sameDigest)
	if err != nil || resumed != first {
		t.Fatalf("resumed generation = %d, want %d (%v)", resumed, first, err)
	}
	writeAppliedPoolNetwork(t, reloaded, protocol.NetworkRegistration{ID: "vlan-100", PoolID: "pool-1", HostID: master.State.HostID, Name: "Servers", Kind: "vlan", Source: protocol.GuestBridge("nex1", 100), Uplink: "nex1", VLAN: 100, Generation: 1})
	_, changed, err := buildPoolAuthorityReplica(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	next, err := advancePoolAuthorityGeneration(reloaded, changed)
	if err != nil || next != first+1 {
		t.Fatalf("changed generation = %d (%v)", next, err)
	}
	stored, storedDigest, err := PoolAuthorityGeneration(master)
	if err != nil || stored != next || storedDigest != changed {
		t.Fatalf("persisted generation = %d/%s (%v)", stored, storedDigest, err)
	}
}

// A member that lost its replica, and a member that was unreachable for a push, are both
// caught up from the next tick without the generation moving.
func TestPoolAuthorityReplicaLaggingMemberCatchesUp(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	server := serveMemberPeer(t, master, member)
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := os.Remove(poolReplicaFile(member)); err != nil {
		t.Fatal(err)
	}
	// The master is unreachable for one pass; the failure is reported, not fatal.
	server.CloseClientConnections()
	setMemberPeerOrigin(t, master, member.State.HostID, "https://127.0.0.1:1")
	if err := PushPoolAuthorityReplicas(context.Background(), master, time.Second); err == nil {
		t.Fatal("unreachable member reported a successful push")
	}
	setMemberPeerOrigin(t, master, member.State.HostID, server.URL)
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("catch-up push: %v", err)
	}
	after := storedReplica(t, member)
	generation, digest, err := PoolAuthorityGeneration(master)
	if err != nil {
		t.Fatal(err)
	}
	if after == nil || !after.Verified || after.Replica.Generation != generation || after.Digest != digest {
		t.Fatalf("catch-up replica = %#v, want generation %d", after, generation)
	}
}

// Only ready members hold a replica. A pending, failed or ejected host is never sent one.
func TestPoolAuthorityReplicaSkipsMembersThatAreNotReady(t *testing.T) {
	for _, status := range []string{"pending", "failed", "ejected"} {
		t.Run(status, func(t *testing.T) {
			master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
			joinPeerMember(t, master, member)
			serveMemberPeer(t, master, member)
			setMemberStatus(t, master, member.State.HostID, status)
			if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
				t.Fatalf("push: %v", err)
			}
			if record := storedReplica(t, member); record != nil {
				t.Fatalf("%s member received a replica: %#v", status, record.Replica)
			}
		})
	}
}

// The replica is bounded on the wire and on disk: a body over the limit is refused rather
// than truncated into a shorter authority.
func TestPoolAuthorityReplicaSizeIsBounded(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	server := serveMemberPeer(t, master, member)
	client, err := PoolMemberPeerClient(master, server.URL, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	body := append(bytes.Repeat([]byte("a"), protocol.PoolReplicaLimit+1024), '"', '}')
	request, err := http.NewRequest(http.MethodPost, server.URL+poolReplicaPath, bytes.NewReader(append([]byte(`{"pool_id":"`), body...)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized replica status = %d", response.StatusCode)
	}
	if record := storedReplica(t, member); record != nil {
		t.Fatalf("oversized replica was stored: %#v", record)
	}
	oversized := poolReplicaRecord{Replica: protocol.PoolAuthorityReplica{VMs: json.RawMessage(append(append([]byte(`"`), bytes.Repeat([]byte("a"), protocol.PoolReplicaLimit)...), '"'))}}
	if err = savePoolReplica(member, oversized); err == nil {
		t.Fatal("an oversized replica was stored")
	}
}

// Concurrent authority writes and pushes must not race, and the member must end holding
// one consistent, verified replica.
func TestPoolAuthorityReplicaConcurrentPushes(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	serveMemberPeer(t, master, member)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			writeAppliedPoolNetwork(t, master, protocol.NetworkRegistration{ID: "vlan-" + string(rune('a'+i)), PoolID: "pool-1", HostID: master.State.HostID, Name: "Net", Kind: "vlan", Source: protocol.GuestBridge("nex1", 100+i), Uplink: "nex1", VLAN: 100 + i, Generation: 1})
		}()
		go func() {
			defer wg.Done()
			_ = PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second)
		}()
	}
	wg.Wait()
	if err := PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("final push: %v", err)
	}
	record := storedReplica(t, member)
	generation, digest, err := PoolAuthorityGeneration(master)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || !record.Verified || record.Replica.Generation != generation || record.Digest != digest {
		t.Fatalf("member replica = %#v, master at %d/%s", record, generation, digest)
	}
}

// A stored replica grants nothing. A member holding a replica that names it as the owner
// of a VM still has no durable execution record of its own, so no start is admitted and no
// permit exists: the replica is a recoverable copy, never an authority.
func TestPoolAuthorityReplicaGrantsNoExecution(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	serveMemberPeer(t, master, member)
	vmID := "12345678-1234-4234-8234-123456789012"
	spec := poolVMTestSpec(vmID, member.State.HostID)
	poolVMMu.Lock()
	state, err := loadPoolVMs(master)
	if err == nil {
		state.VMs[vmID] = poolVMRecord{PoolID: "pool-1", VMID: vmID, HostID: member.State.HostID, State: "stopped", Epoch: 4, Spec: *spec}
		err = savePoolVMs(master, state)
	}
	poolVMMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("push: %v", err)
	}
	record := storedReplica(t, member)
	if record == nil || !strings.Contains(string(record.Replica.VMs), vmID) {
		t.Fatalf("replica does not carry the VM ownership record: %#v", record)
	}
	vms, err := loadPoolVMs(member)
	if err != nil || len(vms.VMs) != 0 {
		t.Fatalf("member adopted the replicated owner record: %#v (%v)", vms.VMs, err)
	}
	permit := "permit"
	if err = poolVMLocal(member, vmID, "start", &permit, false, nil, nil); err == nil {
		t.Fatal("a replica admitted a start on the member")
	}
	if _, err = os.Stat(filepath.Join(member.dir, "pool-vms.json")); !os.IsNotExist(err) {
		t.Fatalf("member wrote a pool VM record from a replica: %v", err)
	}
}

// Defect 5: owner records are pruned only from their owner's own delete tombstones, never
// from absence, and never while an update is unresolved or another host still defines it.
func TestPrunePoolVMOwnersUsesDeleteTombstonesOnly(t *testing.T) {
	deleted := "11111111-1111-4111-8111-111111111111"
	kept := "22222222-2222-4222-8222-222222222222"
	pending := "33333333-3333-4333-8333-333333333333"
	elsewhere := "44444444-4444-4444-8444-444444444444"
	state := poolVMState{
		VMs: map[string]poolVMRecord{
			deleted:   {PoolID: "pool-1", VMID: deleted, HostID: "member-1", State: "stopped"},
			kept:      {PoolID: "pool-1", VMID: kept, HostID: "member-1", State: "running"},
			pending:   {PoolID: "pool-1", VMID: pending, HostID: "member-1", State: "stopped", PendingUpdate: &poolVMPendingUpdate{OperationID: "op-1"}},
			elsewhere: {PoolID: "pool-1", VMID: elsewhere, HostID: "member-2", State: "running"},
		},
		Inventory: map[string]poolVMInventory{
			"member-1": {HostID: "member-1", Generation: 2, Deleted: []string{deleted, pending, elsewhere}},
			"member-2": {HostID: "member-2", Generation: 2, VMs: []protocol.VMCreateSpec{{ID: elsewhere}}},
		},
	}
	pruned := prunePoolVMOwners(&state, "member-1")
	if len(pruned) != 1 || pruned[0] != deleted {
		t.Fatalf("pruned = %#v", pruned)
	}
	if _, ok := state.VMs[deleted]; ok {
		t.Fatal("a deleted VM kept its owner record")
	}
	for _, id := range []string{kept, pending, elsewhere} {
		if _, ok := state.VMs[id]; !ok {
			t.Fatalf("owner record %s was pruned", id)
		}
	}
	// A host cannot prune a record it does not own, and absence alone prunes nothing.
	state.Inventory["member-2"] = poolVMInventory{HostID: "member-2", Generation: 2}
	if pruned = prunePoolVMOwners(&state, "member-2"); len(pruned) != 0 || len(state.VMs) != 3 {
		t.Fatalf("absence pruned records: %#v / %#v", pruned, state.VMs)
	}
}

// Owner decision D4: the storage peer list fails closed on an incomplete journal, exactly
// as the network and ISO lists do, and a member detaches nothing when it receives that.
func TestPoolStoragePeerListFailsClosedOnIncompleteJournal(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: master.State.HostID, Name: "shared", Path: t.TempDir(), Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1}
	writeAppliedPoolStorage(t, master, storage)
	if err := os.WriteFile(filepath.Join(master.dir, "managed-vms", "unreadable.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := PoolPeerClient(member, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	for _, path := range []string{"/api/v1/pool/storage", "/api/v1/pool/storage/repo-1"} {
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "managed journal is incomplete") {
			t.Fatalf("%s status = %d (%s)", path, response.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	// The member aborts at the master list, so nothing is reconciled and nothing detached.
	poolPeerMu.Lock()
	state, err := loadPoolPeer(member)
	if err == nil {
		state.MasterURL = server.URL
		err = savePoolPeer(member, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	writeAppliedPoolStorage(t, member, storage)
	err = reconcilePoolStorageFromMaster(context.Background(), member, 5*time.Second)
	if err == nil || poolReconcileErrorStage(err) != "master-list" {
		t.Fatalf("member reconcile = %v (stage %s)", err, poolReconcileErrorStage(err))
	}
	applied, checks := vmhost.NewManaged(filepath.Join(member.dir, "managed-vms")).AppliedStorage()
	if !managedVMChecksReady(checks) {
		t.Fatal("member journal became unreadable")
	}
	for _, entry := range applied {
		if entry.ID == storage.ID && entry.Tombstone {
			t.Fatal("member detached a repository after an incomplete master list")
		}
	}
}
