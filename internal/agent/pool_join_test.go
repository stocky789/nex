// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testMemberOrigin = "https://member.example.test:9444"

// poolJoinLab is a master serving its real peer listener and a joining host holding a fresh
// Controller-style invitation for it.
func poolJoinLab(t *testing.T) (*Store, *Store, *httptest.Server, protocol.PoolJoinInvitation) {
	t.Helper()
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "member-pool")
	for _, s := range []*Store{master, member} {
		if err := EnsurePoolMembership(s); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(s.dir, "managed-vms"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// EnsurePoolMembership already minted the master CA the listener takes its TLS
	// configuration from; the first invitation is issued for the running listener so the
	// master records its real peer origin.
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	invitation, err := CreatePoolJoinInvitation(master, server.URL, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	return master, member, server, invitation
}

func poolJoinCommand(operationID string, member *Store, invitation protocol.PoolJoinInvitation) protocol.Command {
	return protocol.Command{OperationID: operationID, Kind: "pool-join", Pool: &protocol.PoolCommand{RequestID: "request-" + operationID, Action: "join", MemberHostID: member.State.HostID, MasterURL: invitation.MasterURL, PoolCAPEM: invitation.PoolCAPEM, Invitation: invitation.Invitation, PeerOrigin: testMemberOrigin}}
}

func poolMember(t *testing.T, master *Store, hostID string) protocol.PoolMember {
	t.Helper()
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	found := protocol.PoolMember{}
	for _, member := range membership.Members {
		if member.HostID == hostID {
			if found.HostID != "" {
				t.Fatalf("host %s has more than one membership row", hostID)
			}
			found = member
		}
	}
	return found
}

func TestPoolJoinCommandCompletesEveryStage(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	result, changed := ExecutePoolJoin(context.Background(), member, poolJoinCommand("join-1", member, invitation))
	if result.Status != "succeeded" || result.Error != "" || !changed {
		t.Fatalf("join result = %+v, identity changed = %v", result, changed)
	}
	if member.State.PoolID != "pool-1" {
		t.Fatalf("member pool identity = %q", member.State.PoolID)
	}
	row := poolMember(t, master, "member-1")
	if row.Status != "ready" || row.PeerOrigin != testMemberOrigin {
		t.Fatalf("membership row = %+v", row)
	}
	status := PoolJoinReport(member)
	if status == nil || status.Stage != "ready" || status.Status != "succeeded" || status.OperationID != "join-1" {
		t.Fatalf("join report = %+v", status)
	}
	// An exact redelivery of the same command is idempotent and admits no second member.
	if result, _ = ExecutePoolJoin(context.Background(), member, poolJoinCommand("join-1", member, invitation)); result.Status != "succeeded" {
		t.Fatalf("redelivered join result = %+v", result)
	}
	if row = poolMember(t, master, "member-1"); row.Status != "ready" {
		t.Fatalf("membership row after redelivery = %+v", row)
	}
}

// A master of a single-host pool never advertises itself, so the first brokered invitation
// records the master_url it was issued for; a later one can neither move nor re-ask for it.
func TestPoolJoinInvitationRecordsMasterPeerOrigin(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	if row := poolMember(t, master, "master-1"); row.PeerOrigin != "" {
		t.Fatalf("master row before any invitation = %+v", row)
	}
	if _, err := CreatePoolJoinInvitation(master, "http://192.0.2.11:9444", "member-1"); err == nil {
		t.Fatal("invitation minted for a non-HTTPS master origin")
	}
	if row := poolMember(t, master, "master-1"); row.PeerOrigin != "" {
		t.Fatalf("refused origin recorded: %+v", row)
	}
	before := poolMember(t, master, "master-1").Generation
	first, err := CreatePoolJoinInvitation(master, "https://192.0.2.11:9444/", "member-1")
	if err != nil {
		t.Fatal(err)
	}
	row := poolMember(t, master, "master-1")
	if row.PeerOrigin != "https://192.0.2.11:9444" || first.MasterURL != "https://192.0.2.11:9444" {
		t.Fatalf("first invitation = %q, master row = %+v", first.MasterURL, row)
	}
	// The advertise path records an observation, not a membership change: no generation bump.
	if row.Generation != before {
		t.Fatalf("generation moved from %d to %d", before, row.Generation)
	}
	second, err := CreatePoolJoinInvitation(master, "https://192.0.2.99:9444", "member-2")
	if err != nil {
		t.Fatal(err)
	}
	if row = poolMember(t, master, "master-1"); row.PeerOrigin != "https://192.0.2.11:9444" || second.MasterURL != "https://192.0.2.11:9444" {
		t.Fatalf("second invitation = %q, master row = %+v", second.MasterURL, row)
	}
}

// TestPoolJoinInvitationWorksOutMasterPeerOrigin: with no master_url supplied, the master
// records and issues the address its own peer listener is bound to. A wildcard bind names
// no address, so only then does it fall back to the default route — and it never invents a
// port the listener does not answer on, because that origin is recorded for good.
func TestPoolJoinInvitationWorksOutMasterPeerOrigin(t *testing.T) {
	master := peerStore(t, "master-1", "pool-1")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	own, ownErr := ownPeerOrigin()
	invitation, err := CreatePoolJoinInvitation(master, "", "member-1")
	if ownErr != nil {
		if err == nil || err.Error() != ownErr.Error() {
			t.Fatalf("no default route: err = %v, want %v", err, ownErr)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if own.Port() != "9444" || own.Hostname() == "" || invitation.MasterURL != own.String() || poolMember(t, master, "master-1").PeerOrigin != own.String() {
		t.Fatalf("own origin %q, invitation %q, master row %+v", own, invitation.MasterURL, poolMember(t, master, "master-1"))
	}

	// An operator-chosen listen address is the one a joining host must dial.
	previous := PoolPeerListen
	t.Cleanup(func() { PoolPeerListen = previous })
	PoolPeerListen = "192.0.2.50:9500"
	bound := peerStore(t, "master-2", "pool-2")
	if err = EnsurePoolMembership(bound); err != nil {
		t.Fatal(err)
	}
	invitation, err = CreatePoolJoinInvitation(bound, "", "member-1")
	if err != nil {
		t.Fatal(err)
	}
	if invitation.MasterURL != "https://192.0.2.50:9500" || poolMember(t, bound, "master-2").PeerOrigin != invitation.MasterURL {
		t.Fatalf("invitation %q, master row %+v", invitation.MasterURL, poolMember(t, bound, "master-2"))
	}

	// Every spelling of a wildcard bind names no address, so each falls back to the default
	// route on the configured port. This machine may have no IPv6 route, so what the fallback
	// resolved to cannot be asserted — only that it is not the wildcard and keeps the port.
	for i, listen := range []string{"[::]:9444", "[::0]:9444"} {
		PoolPeerListen = listen
		wildcard := peerStore(t, fmt.Sprintf("master-w%d", i), fmt.Sprintf("pool-w%d", i))
		if err = EnsurePoolMembership(wildcard); err != nil {
			t.Fatal(err)
		}
		if invitation, err = CreatePoolJoinInvitation(wildcard, "", "member-1"); err != nil {
			t.Fatal(err)
		}
		u, parseErr := url.Parse(invitation.MasterURL)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if u.Port() != "9444" || u.Hostname() == "" || net.ParseIP(u.Hostname()).IsUnspecified() {
			t.Fatalf("a %s bind gave %q, want the default-route address on port 9444", listen, invitation.MasterURL)
		}
	}

	// A literal IPv6 listen address is the one a joining host must dial, brackets and all.
	PoolPeerListen = "[2001:db8::1]:9444"
	literal := peerStore(t, "master-6", "pool-6")
	if err = EnsurePoolMembership(literal); err != nil {
		t.Fatal(err)
	}
	if invitation, err = CreatePoolJoinInvitation(literal, "", "member-1"); err != nil {
		t.Fatal(err)
	}
	if invitation.MasterURL != "https://[2001:db8::1]:9444" {
		t.Fatalf("invitation %q, want the configured IPv6 listen address", invitation.MasterURL)
	}
}

func TestPoolJoinInvitationBindsCSRAtRedemptionAndReplays(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	decoded, err := decodePeerOffer(invitation.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CSRHash != "" || decoded.MemberHostID != "member-1" || decoded.PoolID != "pool-1" {
		t.Fatalf("invitation = %+v", decoded)
	}
	if until := time.Until(invitation.ExpiresAt); until <= 14*time.Minute || until > 15*time.Minute {
		t.Fatalf("invitation expires in %s", until)
	}
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	request := peerJoinRequest{Offer: decoded.Token, HostID: "member-1", CSRPEM: csr}
	first, err := acceptPeerJoin(master, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := acceptPeerJoin(master, request)
	if err != nil || replay.CertificatePEM != first.CertificatePEM {
		t.Fatalf("replay = %v, same certificate = %v", err, replay.CertificatePEM == first.CertificatePEM)
	}
	if row := poolMember(t, master, "member-1"); row.Status != "ready" {
		t.Fatalf("membership row = %+v", row)
	}
	// Once bound, the invitation admits that CSR and no other.
	_, other, err := peerKeyCSR("member-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptPeerJoin(master, peerJoinRequest{Offer: decoded.Token, HostID: "member-1", CSRPEM: other}); err == nil {
		t.Fatal("bound invitation accepted a replacement key")
	}
}

func TestPoolJoinInvitationIsSingleUseAndExpires(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	decoded, err := decodePeerOffer(invitation.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	// The invitation names one member host; no other host can redeem it.
	other := peerStore(t, "member-2", "other-pool")
	otherCSR, err := PoolJoinCSR(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptPeerJoin(master, peerJoinRequest{Offer: decoded.Token, HostID: "member-2", CSRPEM: otherCSR}); err == nil {
		t.Fatal("invitation redeemed by another host")
	}
	// An invitation cannot be minted for a host that is already a member.
	if _, err = acceptPeerJoin(master, peerJoinRequest{Offer: decoded.Token, HostID: "member-1", CSRPEM: csr}); err != nil {
		t.Fatal(err)
	}
	if _, err = CreatePoolJoinInvitation(master, "https://master.example.test", "member-1"); err == nil {
		t.Fatal("invitation minted for an existing member")
	}
	// An expired invitation is refused even with the right host and CSR.
	expired, err := CreatePoolJoinInvitation(master, "https://master.example.test", "member-3")
	if err != nil {
		t.Fatal(err)
	}
	expiredOffer, err := decodePeerOffer(expired.Invitation)
	if err != nil {
		t.Fatal(err)
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		for id, offer := range state.Offers {
			offer.Expires = time.Now().Add(-time.Minute)
			state.Offers[id] = offer
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	third := peerStore(t, "member-3", "other-pool")
	thirdCSR, err := PoolJoinCSR(third)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptPeerJoin(master, peerJoinRequest{Offer: expiredOffer.Token, HostID: "member-3", CSRPEM: thirdCSR}); err == nil {
		t.Fatal("expired invitation was redeemed")
	}
}

func writeManagedJournal(t *testing.T, dir, operationID string, cmd protocol.Command) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"command": cmd, "result": protocol.Result{OperationID: operationID, Status: "succeeded"}, "stage": "defined"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, operationID+".json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPoolJoinPrecheckRefusals(t *testing.T) {
	ctx := context.Background()
	if err := PoolJoinPrecheck(ctx, peerStore(t, "", ""), nil); err == nil {
		t.Fatal("unenrolled Core passed the precheck")
	}
	host := peerStore(t, "member-1", "member-pool")
	if err := EnsurePoolMembership(host); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(host.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := PoolJoinPrecheck(ctx, host, nil); err != nil {
		t.Fatalf("clean single-host master refused: %v", err)
	}
	vm := protocol.VMCreateSpec{ID: "12345678-1234-4234-8234-123456789012", PoolID: "member-pool", HostID: "member-1", Name: "Guest",
		CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: "storage", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}},
		Boot:  protocol.VMBoot{Firmware: "bios"}}
	writeManagedJournal(t, dir, "vm-1", protocol.Command{OperationID: "vm-1", Kind: "vm-create", VM: &vm})
	if err := PoolJoinPrecheck(ctx, host, nil); err == nil {
		t.Fatal("a host with a managed VM passed the precheck")
	}
	if err := os.Remove(filepath.Join(dir, "vm-1.json")); err != nil {
		t.Fatal(err)
	}
	writeManagedJournal(t, dir, "storage-1", protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "storage-1", Name: "shared", Kind: protocol.StorageKindNFS, PoolID: "member-pool", HostID: "member-1", Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports", Directory: "guests"}}, Generation: 1}})
	if err := PoolJoinPrecheck(ctx, host, nil); err == nil {
		t.Fatal("a host with remote storage passed the precheck")
	}
	if err := os.Remove(filepath.Join(dir, "storage-1.json")); err != nil {
		t.Fatal(err)
	}
	// A host that already belongs to another pool cannot join a second one.
	master, member, _, invitation := poolJoinLab(t)
	if result, _ := ExecutePoolJoin(ctx, member, poolJoinCommand("join-1", member, invitation)); result.Status != "succeeded" {
		t.Fatalf("join result = %+v", result)
	}
	if err := PoolJoinPrecheck(ctx, member, nil); err == nil {
		t.Fatal("an existing member passed the precheck")
	}
	if err := PoolJoinPrecheck(ctx, master, nil); err == nil {
		t.Fatal("a master with members passed the precheck")
	}
}

func TestPoolJoinResumesFromEveryPersistedStage(t *testing.T) {
	for _, stage := range poolJoinStages[:len(poolJoinStages)-1] {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			master, member, _, invitation := poolJoinLab(t)
			joinedFirst := stage == "rehomed" || stage == "advertised"
			if stage == "joined" {
				// The csr stage persists the CSR before the stage advances, so a record
				// resumed at "joined" always has one to present.
				if _, err := PoolJoinCSR(member); err != nil {
					t.Fatal(err)
				}
			}
			if joinedFirst {
				// These stages are only reachable on a host the master already admitted.
				csr, err := PoolJoinCSR(member)
				if err != nil {
					t.Fatal(err)
				}
				bound, err := bindPoolJoinInvitation(invitation.Invitation, csr)
				if err != nil {
					t.Fatal(err)
				}
				if err = JoinPoolPeer(ctx, member, invitation.MasterURL, invitation.PoolCAPEM, bound); err != nil {
					t.Fatal(err)
				}
			}
			pending := &poolPendingJoin{OperationID: "join-" + stage, MasterURL: invitation.MasterURL, CAPEM: invitation.PoolCAPEM, Invitation: invitation.Invitation, PeerOrigin: testMemberOrigin, Stage: stage}
			if err := savePendingJoin(member, pending); err != nil {
				t.Fatal(err)
			}
			if got := PoolJoinReport(member); got == nil || got.Stage != stage {
				t.Fatalf("persisted stage = %+v", got)
			}
			if changed := ResumePoolJoin(ctx, member); changed == joinedFirst {
				t.Fatalf("identity change reported = %v at stage %s", changed, stage)
			}
			status := PoolJoinReport(member)
			if status == nil || status.Stage != "ready" || status.Status != "succeeded" {
				t.Fatalf("resumed join = %+v", status)
			}
			if member.State.PoolID != "pool-1" {
				t.Fatalf("member pool identity = %q", member.State.PoolID)
			}
			if row := poolMember(t, master, "member-1"); row.Status != "ready" || row.PeerOrigin != testMemberOrigin {
				t.Fatalf("membership row = %+v", row)
			}
		})
	}
}

// A joined host whose rehome has not run reports that it is a member and needs attention:
// the caller must never read it as "not joined" (the defect this slice fixes).
func TestPoolJoinReportsNeedsAttentionWhileJoinedAndNotRehomed(t *testing.T) {
	ctx := context.Background()
	_, member, _, invitation := poolJoinLab(t)
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindPoolJoinInvitation(invitation.Invitation, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(ctx, member, invitation.MasterURL, invitation.PoolCAPEM, bound); err != nil {
		t.Fatal(err)
	}
	if err = savePendingJoin(member, &poolPendingJoin{OperationID: "join-1", Stage: "rehomed", PeerOrigin: testMemberOrigin}); err != nil {
		t.Fatal(err)
	}
	status := PoolJoinReport(member)
	if status == nil || status.Stage != "rehomed" || status.Status != "needs_attention" {
		t.Fatalf("interrupted join report = %+v", status)
	}
}

func TestConcurrentPoolJoinAttemptsAdmitOneMember(t *testing.T) {
	master, member, _, invitation := poolJoinLab(t)
	cmd := poolJoinCommand("join-1", member, invitation)
	var wg sync.WaitGroup
	results := make([]protocol.Result, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = ExecutePoolJoin(context.Background(), member, cmd)
		}(i)
	}
	wg.Wait()
	for i, result := range results {
		if result.Status != "succeeded" {
			t.Fatalf("concurrent attempt %d = %+v", i, result)
		}
	}
	if row := poolMember(t, master, "member-1"); row.Status != "ready" {
		t.Fatalf("membership row = %+v", row)
	}
	membership, err := Membership(master)
	if err != nil || len(membership.Members) != 2 {
		t.Fatalf("membership = %+v, %v", membership, err)
	}
}

func TestPoolRequestInviteAnswersOnlyItsOwnMaster(t *testing.T) {
	master, member, _, _ := poolJoinLab(t)
	result := ExecutePoolRequest(master, protocol.PoolRequest{RequestID: "request-1", PoolID: "pool-1", HostID: "master-1", Action: "invite", MemberHostID: member.State.HostID, MasterURL: "https://master.example.test"})
	if result.Status != "succeeded" || result.Invitation == nil || result.Invitation.Invitation == "" || result.Invitation.PoolCAPEM == "" {
		t.Fatalf("invite result = %+v", result)
	}
	for _, request := range []protocol.PoolRequest{
		{RequestID: "request-2", PoolID: "pool-1", HostID: "other-host", Action: "invite", MemberHostID: "member-1", MasterURL: "https://master.example.test"},
		{RequestID: "request-3", PoolID: "other-pool", HostID: "master-1", Action: "invite", MemberHostID: "member-1", MasterURL: "https://master.example.test"},
		{RequestID: "request-4", PoolID: "pool-1", HostID: "master-1", Action: "eject", MemberHostID: "member-1", MasterURL: "https://master.example.test"},
		{RequestID: "", PoolID: "pool-1", HostID: "master-1", Action: "invite", MemberHostID: "member-1", MasterURL: "https://master.example.test"},
		{RequestID: "request-5", PoolID: "pool-1", HostID: "master-1", Action: "invite", MemberHostID: "member-1", MasterURL: "http://master.example.test"},
	} {
		if result = ExecutePoolRequest(master, request); result.Status != "failed" || result.Invitation != nil {
			t.Fatalf("request %+v = %+v", request, result)
		}
	}
}

// C-10. The master commits the membership before its reply is delivered. If that reply is
// lost, a host the master already admitted must never end up reporting "failed": the two
// ends would disagree with no way back, because minting and redeeming both refuse an
// existing member. Only the stages before admission are bounded by an attempt count.
func TestJoinNeverReportsFailedAfterTheMasterMayHaveAdmittedIt(t *testing.T) {
	ctx := context.Background()
	master, member, _, invitation := poolJoinLab(t)
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindPoolJoinInvitation(invitation.Invitation, csr)
	if err != nil {
		t.Fatal(err)
	}
	// The master admits the host; its reply never reaches the joining Core, which stays at
	// stage "joined" with the invitation it was given.
	if _, err = acceptPeerJoin(master, peerJoinRequest{Offer: mustDecodeOffer(t, bound).Token, HostID: member.State.HostID, CSRPEM: csr}); err != nil {
		t.Fatal(err)
	}
	if row := poolMember(t, master, member.State.HostID); row.Status != "ready" {
		t.Fatalf("the master did not admit the host: %+v", row)
	}
	pending := &poolPendingJoin{OperationID: "join-lost", MasterURL: invitation.MasterURL, CAPEM: invitation.PoolCAPEM, Invitation: invitation.Invitation, Stage: "joined", Attempts: poolJoinMaxAttempts, LastError: "pool master did not answer"}
	if err = savePendingJoin(member, pending); err != nil {
		t.Fatal(err)
	}
	if status := PoolJoinReport(member); status == nil || status.Status == "failed" {
		t.Fatalf("an admitted host reported %+v", status)
	}
	// And the truth is still learnable: the same invitation replays past its own expiry,
	// because the certificate it already issued cannot admit anything new.
	expirePoolJoinOffers(t, master)
	if status := advancePoolJoin(ctx, member, pending); status.Status != "succeeded" || pending.Stage != "ready" {
		t.Fatalf("the join could not be completed after the reply was lost: %+v (%s)", status, pending.Stage)
	}
}

func mustDecodeOffer(t *testing.T, invitation string) peerOffer {
	t.Helper()
	decoded, err := decodePeerOffer(invitation)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func expirePoolJoinOffers(t *testing.T, master *Store) {
	t.Helper()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	for id, offer := range state.Offers {
		offer.Expires = time.Now().Add(-time.Minute)
		state.Offers[id] = offer
	}
	if err = savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
}

// P-4. The invitation is a bearer credential: it must not stay at rest on the joining host
// once the stage that needs it has completed.
func TestJoinInvitationIsScrubbedOnceRedeemed(t *testing.T) {
	ctx := context.Background()
	_, member, _, invitation := poolJoinLab(t)
	cmd := poolJoinCommand("join-scrub", member, invitation)
	if result, _ := ExecutePoolJoin(ctx, member, cmd); result.Status != "succeeded" {
		t.Fatalf("join = %+v", result)
	}
	pending, err := loadPendingJoin(member)
	if err != nil || pending == nil {
		t.Fatalf("pending join = %+v (%v)", pending, err)
	}
	if pending.Invitation != "" {
		t.Fatal("the redeemed invitation is still stored on the joining host")
	}
	b, err := os.ReadFile(poolStatePath(member))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), invitation.Invitation) {
		t.Fatal("the invitation is still at rest in pool-peer.json")
	}
}

// P-7. The host-local path must not discard a Controller-driven join's durable record: it
// is the only trace of that operation.
func TestCompletePoolJoinStagesKeepsAnExistingJoinRecord(t *testing.T) {
	ctx := context.Background()
	master, member, _, invitation := poolJoinLab(t)
	existing := &poolPendingJoin{OperationID: "join-controller", MasterURL: invitation.MasterURL, CAPEM: invitation.PoolCAPEM, Stage: "csr", Status: "pending"}
	if err := savePendingJoin(member, existing); err != nil {
		t.Fatal(err)
	}
	// The host-local path has already completed the exchange by the time it runs.
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindPoolJoinInvitation(invitation.Invitation, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(ctx, member, invitation.MasterURL, invitation.PoolCAPEM, bound); err != nil {
		t.Fatal(err)
	}
	status := CompletePoolJoinStages(ctx, member, testMemberOrigin)
	if status.OperationID != "join-controller" {
		t.Fatalf("the host-local path discarded the Controller-driven join record: %+v", status)
	}
	pending, err := loadPendingJoin(member)
	if err != nil || pending == nil || pending.OperationID != "join-controller" {
		t.Fatalf("stored join record = %+v (%v)", pending, err)
	}
	if row := poolMember(t, master, member.State.HostID); row.Status != "ready" {
		t.Fatalf("membership row = %+v", row)
	}
}

// P-8. The redemption lock is a process mutex; it is safe only because one process owns an
// agent state directory. That is enforced, not merely documented.
func TestOneProcessOwnsTheAgentStateDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	first, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err = OpenStore(dir); err == nil {
		t.Fatal("a second agent opened the same state directory")
	} else if !strings.Contains(err.Error(), "another agent process owns the state directory") {
		t.Fatalf("second open = %v", err)
	}
}

// Test gap 8. Refusals a deletion would leave green.
func TestPoolJoinRefusalsThatNothingCovered(t *testing.T) {
	ctx := context.Background()
	master, member, _, invitation := poolJoinLab(t)
	// A master cannot mint an invitation for itself.
	if _, err := CreatePoolJoinInvitation(master, "https://master.example.test", master.State.HostID); err == nil {
		t.Fatal("a master minted a join invitation for itself")
	}
	// A join command addressed to another host is refused before anything is recorded.
	elsewhere := poolJoinCommand("join-elsewhere", member, invitation)
	elsewhere.Pool.MemberHostID = "somebody-else"
	if result, changed := ExecutePoolJoin(ctx, member, elsewhere); result.Status != "failed" || changed || !strings.Contains(result.Error, "does not address this host") {
		t.Fatalf("join addressed elsewhere = %+v (changed %v)", result, changed)
	}
	if pending, err := loadPendingJoin(member); err != nil || pending != nil {
		t.Fatalf("a refused join recorded state: %+v (%v)", pending, err)
	}
	// A second join for a different operation is refused while the first is in progress.
	if err := savePendingJoin(member, &poolPendingJoin{OperationID: "join-first", Stage: "csr", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	second := poolJoinCommand("join-second", member, invitation)
	if result, _ := ExecutePoolJoin(ctx, member, second); result.Status != "failed" || !strings.Contains(result.Error, "already in progress") {
		t.Fatalf("second concurrent join = %+v", result)
	}
	if pending, err := loadPendingJoin(member); err != nil || pending == nil || pending.OperationID != "join-first" {
		t.Fatalf("the in-progress record was replaced: %+v (%v)", pending, err)
	}
}

// BUG-0355: a detached repository projects into the applied storage list as a tombstone, and
// a tombstone has no kind at all, so the precheck read it as existing remote storage and
// refused every join for ever — while the Controller's preview, whose mirror row the detach
// had removed, reported the same host eligible.
func TestPoolJoinPrecheckIgnoresADetachedRepository(t *testing.T) {
	ctx := context.Background()
	host := peerStore(t, "member-1", "member-pool")
	if err := EnsurePoolMembership(host); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(host.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	repo := protocol.StorageRegistration{ID: "repo-1", Name: "shared", Kind: protocol.StorageKindNFS, PoolID: "member-pool", HostID: "member-1",
		Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports", Directory: "guests"}}, Generation: 1}
	writeManagedJournal(t, dir, "storage-1", protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &repo})
	if err := PoolJoinPrecheck(ctx, host, nil); err == nil {
		t.Fatal("a host holding a live remote repository passed the precheck")
	}
	writeManagedJournal(t, dir, "storage-1-detach", protocol.Command{OperationID: "storage-1-detach", Kind: "storage-detach",
		StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: repo.ID, PoolID: repo.PoolID, HostID: repo.HostID, ExpectedGeneration: 1}})
	if err := PoolJoinPrecheck(ctx, host, nil); err != nil {
		t.Fatalf("a host that has detached its only remote repository is still refused: %v", err)
	}
	// One rule for both callers: Core's precheck and the Controller's preview read the same
	// predicate, so a detached repository can never be eligible on one side and not the other.
	if protocol.StorageBlocksPoolJoin(protocol.StorageRegistration{ID: repo.ID, Generation: 2, Tombstone: true}) {
		t.Fatal("a tombstoned registration blocks a join")
	}
	if !protocol.StorageBlocksPoolJoin(repo) {
		t.Fatal("a live NFS registration does not block a join")
	}
	if protocol.StorageBlocksPoolJoin(protocol.StorageRegistration{ID: "local", Path: "/srv/local"}) {
		t.Fatal("a local repository blocks a join")
	}
}
