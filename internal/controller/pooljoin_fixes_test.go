// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// quietMasterMembers brings a pool to "the master reported a member and a failing pool VLAN,
// then went quiet", and returns the projection a client gets in that state.
func quietMasterMembers(t *testing.T, l *joinLab) poolMembersView {
	t.Helper()
	membership := selfMembership(l.master.id, l.master.pool)
	membership.Generation = 2
	membership.Members = append(membership.Members, protocol.PoolMember{HostID: l.member.id, CertificateSHA256: labCertificateHash, Generation: 2, Status: "ready", ObservedAt: time.Now().UTC(), PeerOrigin: "https://192.0.2.12:9444"})
	l.master.inv.Managed.Membership = membership
	l.poolLabResources(l.member.id, nil,
		[]protocol.PoolNetworkAccess{{NetworkID: "pool-vlan", HostID: l.member.id, Generation: 1, ObservedAt: time.Now().UTC(), Error: "uplink lab0 does not exist on this host"}}, nil)
	l.send(&l.master, nil)
	l.send(&l.member, nil)
	l.a.c.mu.Lock()
	delete(l.a.c.seen, l.master.id)
	l.a.c.mu.Unlock()
	var view poolMembersView
	read(t, l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200), &view)
	if view.Authoritative || len(view.Members) != 2 {
		t.Fatalf("expected a non-authoritative two-member projection: %+v", view)
	}
	return view
}

func memberRow(t *testing.T, view poolMembersView, host string) poolMemberView {
	t.Helper()
	for _, entry := range view.Members {
		if entry.HostID == host {
			return entry
		}
	}
	t.Fatalf("no row for %s: %+v", host, view.Members)
	return poolMemberView{}
}

// TestPoolMembersKeepsPeerOriginWhenMasterIsNotFresh: peer_origin is the master's durable
// membership record, like the host name and the role, not an observation of it. Blanking it
// loses the master_url prefill exactly when an operator needs it.
func TestPoolMembersKeepsPeerOriginWhenMasterIsNotFresh(t *testing.T) {
	l := newJoinLab(t)
	view := quietMasterMembers(t, l)
	member := memberRow(t, view, l.member.id)
	if member.PeerOrigin != "https://192.0.2.12:9444" || member.Role != "member" || member.Name != l.member.inv.Hostname {
		t.Fatalf("a quiet master must not erase the membership record: %+v", member)
	}
	if member.Status != "unknown" || member.Access != (poolMemberAccessCounts{}) || member.Maintenance != nil || member.Fence != nil {
		t.Fatalf("observational fields must still be unknown: %+v", member)
	}
	// S6: the member's own health checks are its own report, not the master's observation,
	// so they survive a quiet master. This member reports none, so all of them are unknown.
	for _, check := range member.Checks {
		if check.Status != "unknown" {
			t.Fatalf("unreported check must be unknown: %+v", check)
		}
	}
	if memberRow(t, view, l.master.id).Role != "master" {
		t.Fatalf("master role lost: %+v", view.Members)
	}
}

// TestPoolMembersReportsNoFailuresWhenMasterIsNotFresh: failure rows are observations of the
// master's mirror, so they cannot be shown as current while the master is not fresh.
func TestPoolMembersReportsNoFailuresWhenMasterIsNotFresh(t *testing.T) {
	l := newJoinLab(t)
	view := quietMasterMembers(t, l)
	for _, entry := range view.Members {
		if len(entry.Failures) != 0 {
			t.Fatalf("a quiet master reported failure rows as current: %+v", entry.Failures)
		}
	}
	// The same rows are reported while the master is fresh, so the test proves withholding,
	// not an empty fixture.
	l.send(&l.master, nil)
	read(t, l.a.call("GET", "/api/v1/pools/"+l.master.pool+"/members", nil, nil, 200), &view)
	if !view.Authoritative || len(memberRow(t, view, l.member.id).Failures) == 0 {
		t.Fatalf("a fresh master must still report the failing pool VLAN: %+v", view.Members)
	}
}

// TestPoolJoinTerseCoreRefusalIsReadable: a failed PoolResult with no error text must not
// become 409 {"error":""}.
func TestPoolJoinTerseCoreRefusalIsReadable(t *testing.T) {
	previous := poolJoinWait
	poolJoinWait = 2 * time.Second
	t.Cleanup(func() { poolJoinWait = previous })
	l := newJoinLab(t)
	w := l.addMember(l.joinRequest("terse-refusal"), func(request protocol.PoolRequest) protocol.PoolResult {
		return protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "failed", ObservedAt: time.Now().UTC()}
	})
	var body struct {
		Error string `json:"error"`
	}
	read(t, w, &body)
	if w.Code != http.StatusConflict || body.Error == "" {
		t.Fatalf("terse Core refusal: %d %q", w.Code, body.Error)
	}
}

// TestPoolJoinAbandonedRequestIsNotSuccessAndReleasesBroker: a client that hangs up mid-wait
// must not be answered 200, and its broker entry must be released so the same request_id can
// be retried.
func TestPoolJoinAbandonedRequestIsNotSuccessAndReleasesBroker(t *testing.T) {
	l := newJoinLab(t)
	raw, err := json.Marshal(l.joinRequest("abandoned"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "https://nex.test/api/v1/pools/"+l.master.pool+"/members", strings.NewReader(string(raw))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", l.a.csrf)
	r.AddCookie(l.a.cookie)
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); l.a.c.Handler().ServeHTTP(w, r) }()
	// Hang up once the request is parked on the broker, which is the window the bug lived in.
	b := l.a.c.joinBroker()
	for deadline := time.Now().Add(20 * time.Second); ; {
		b.mu.Lock()
		parked := len(b.pending)
		b.mu.Unlock()
		if parked > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the join request never reached the broker")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if w.Code/100 == 2 {
		t.Fatalf("an abandoned join was answered with success: %d %s", w.Code, w.Body.String())
	}
	if retry := l.addMember(l.joinRequest("abandoned"), labInvitation); retry.Code != http.StatusAccepted {
		t.Fatalf("retry of an abandoned request_id: %d %s", retry.Code, retry.Body.String())
	}
}

// TestPoolJoinInvitationScrubbedOnControllerStart: the scrub is scheduled with a timer that
// dies with the process, so a restarted Controller that is never asked about pools again
// would otherwise keep an expired invitation in managed_resources.command.
func TestPoolJoinInvitationScrubbedOnControllerStart(t *testing.T) {
	l := newJoinLab(t)
	if w := l.addMember(l.joinRequest("restart"), labInvitation); w.Code != http.StatusAccepted {
		t.Fatalf("join: %d %s", w.Code, w.Body.String())
	}
	if stored := l.storedCommand(t); stored.Pool.Invitation != labInvitationToken {
		t.Fatalf("expected the queued command to still carry the invitation: %+v", stored.Pool)
	}
	expired := time.Now().Add(-poolJoinInvitationTTL - time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := l.a.c.db.Exec("UPDATE operations SET created_at=? WHERE id IN (SELECT operation_id FROM managed_resources WHERE kind='pool-join')", expired); err != nil {
		t.Fatal(err)
	}
	dir := l.a.c.cfg.DataDir
	if err := l.a.c.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	l.a.c = restarted
	// No request is made: the start itself must have scrubbed it.
	if stored := l.storedCommand(t); stored.Pool.Invitation != "" || stored.Pool.PoolCAPEM != "" {
		t.Fatalf("restart left an expired invitation stored: %+v", stored.Pool)
	}
}

// TestPoolJoinOriginAcceptsTrailingSlashLikeTheHostLocalAPI: internal/localcore/handoff.go's
// validHTTPSOrigin accepts a single trailing "/", so the Controller must too; it normalises
// it away, which also makes the two spellings one request for an exact retry.
func TestPoolJoinOriginAcceptsTrailingSlashLikeTheHostLocalAPI(t *testing.T) {
	l := newJoinLab(t)
	base := "/api/v1/pools/" + l.master.pool + "/members"
	request := l.joinRequest("slash")
	request["peer_origin"] = "https://192.0.2.12:9444/"
	request["master_url"] = "https://192.0.2.11:9444/"
	w := l.addMember(request, labInvitation)
	if w.Code != http.StatusAccepted {
		t.Fatalf("trailing-slash origin refused: %d %s", w.Code, w.Body.String())
	}
	commands := l.send(&l.member, nil).Commands
	if len(commands) != 1 || commands[0].Pool == nil || commands[0].Pool.PeerOrigin != "https://192.0.2.12:9444" || commands[0].Pool.MasterURL != "https://192.0.2.11:9444" {
		t.Fatalf("origins were not normalised before forwarding: %+v", commands)
	}
	// Same request_id, same pool, the other spelling: one request, so this replays.
	var replay struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, l.a.call("POST", base, l.joinRequest("slash"), nil, 200), &replay)
	if replay.Operation.Kind != "pool-join" {
		t.Fatalf("the unslashed spelling was not treated as the same request: %+v", replay)
	}
	// A real path is still not an origin.
	path := l.joinRequest("slash-path")
	path["peer_origin"] = "https://192.0.2.12:9444/join"
	l.a.call("POST", base, path, nil, 400)
}
