// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestBackupPageRelayValidatesQueriesBeforeAskingCore(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	cert := certificate(t, master.CertificatePEM)
	membership := &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	compact := protocol.ReportRequest{Version: protocol.Version, Sequence: 1,
		Capabilities: []string{protocol.ManagedVMCapability},
		Inventory:    &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Membership: membership}}}
	a.report(cert, compact)
	if fresh, _ := a.c.hostAgent(master.HostID, "", false); !fresh {
		t.Fatal("compact report not adopted")
	}
	for _, path := range []string{
		"/api/v1/pools/" + pool + "/backup-runs?cursor=",
		"/api/v1/pools/" + pool + "/backup-runs?cursor=0",
		"/api/v1/pools/" + pool + "/backup-catalog?vm_id=" + strings.Repeat("x", 129),
		"/api/v1/pools/" + pool + "/backup-catalog?generation=" + strings.Repeat("a", 64),
	} {
		a.call("GET", path, nil, nil, 400)
	}
	oldWait := statsAgentWait
	statsAgentWait = 10 * time.Millisecond
	t.Cleanup(func() { statsAgentWait = oldWait })
	a.call("GET", "/api/v1/pools/"+pool+"/backup-runs", nil, nil, 503)
}

func TestBackupPageValidationRejectsForeignAndDuplicateRows(t *testing.T) {
	page := protocol.BackupRunPage{Generation: strings.Repeat("a", 64), Total: 2, Items: []protocol.BackupRun{
		{OperationID: "run-1", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: time.Now().UTC()},
		{OperationID: "run-1", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: time.Now().UTC()},
	}}
	b, _ := json.Marshal(page)
	if validBackupRelayPage(b, "pool-1", false) {
		t.Fatal("accepted duplicate backup run")
	}
	page.Items[1].OperationID = "run-2"
	page.Items[1].PoolID = "other-pool"
	b, _ = json.Marshal(page)
	if validBackupRelayPage(b, "pool-1", false) {
		t.Fatal("accepted foreign-pool backup run")
	}
}

// TestBackupPageRelayMapsCoreStatuses drives relayed pages through a real stats channel.
func TestBackupPageRelayMapsCoreStatuses(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	cert := certificate(t, master.CertificatePEM)
	sequence := uint64(0)
	report := func() protocol.ReportResponse {
		sequence++
		return a.report(cert, protocol.ReportRequest{Version: protocol.Version, Sequence: sequence,
			Capabilities: []string{protocol.ManagedVMCapability, protocol.UsageHistoryCapability},
			Inventory:    &protocol.Inventory{Hostname: "core", Managed: hostDefaults(master.HostID)}})
	}
	report()
	a.call("GET", "/api/v1/pools/no-such-pool/backup-catalog", nil, nil, 404)
	a.call("GET", "/api/v1/pools/no-such-pool/backup-repositories", nil, nil, 404)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/agent/stats/{token}", func(w http.ResponseWriter, r *http.Request) { a.c.agentStats(w, r, master.HostID) })
	server := httptest.NewServer(mux)
	defer server.Close()
	query := func(p string) chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest("GET", "https://nex.test"+p, nil)
			r.AddCookie(a.cookie)
			w := httptest.NewRecorder()
			a.c.Handler().ServeHTTP(w, r)
			done <- w
		}()
		return done
	}
	base := "/api/v1/pools/" + pool + "/backup-catalog"
	done := query(base + "?vm_id=vm-1")
	var token string
	for i := 0; i < 100 && token == ""; i++ {
		if got := report().StatsChannels; len(got) == 1 {
			token = got[0].Token
		}
		time.Sleep(10 * time.Millisecond)
	}
	if token == "" {
		t.Fatal("stats channel never requested")
	}
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Minute))
	fmt.Fprintf(conn, "POST /api/v1/agent/stats/%s HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: nex-stats\r\nContent-Length: 0\r\n\r\n", token)
	reader := bufio.NewReader(conn)
	if resp, err := http.ReadResponse(reader, nil); err != nil || resp.StatusCode != 101 {
		t.Fatalf("stats upgrade %v %v", resp, err)
	}
	answer := func(status int, body string) *http.Request {
		req, err := http.ReadRequest(reader)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "HTTP/1.1 %d X\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", status, len(body), body)
		return req
	}
	result := func(done chan *httptest.ResponseRecorder, want int) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case w := <-done:
			if w.Code != want {
				t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
			}
			return w
		case <-time.After(5 * time.Second):
			t.Fatal("query never finished")
		}
		return nil
	}
	point := protocol.BackupRecoveryPoint{ID: "point-1", RepositoryID: "repo-1", PoolID: pool, SourceVMID: "vm-1", CapturedAt: time.Now().UTC(),
		Consistency: "stopped", ManifestSHA256: strings.Repeat("b", 64)}
	generation := strings.Repeat("a", 64)
	body, _ := json.Marshal(protocol.BackupCatalogPage{Generation: generation, Total: 2, Items: []protocol.BackupRecoveryPoint{point}, NextCursor: "1", BackupDays14: []string{}})
	if req := answer(200, string(body)); req.URL.Query().Get("vm_id") != "vm-1" {
		t.Fatalf("filter not relayed: %s", req.URL)
	}
	var page protocol.BackupCatalogPage
	read(t, result(done, 200), &page)
	if len(page.Items) != 1 || page.NextCursor != "1" || page.Total != 2 {
		t.Fatalf("relayed page %#v", page)
	}
	for _, c := range []struct {
		path       string
		core, want int
	}{
		{base, 503, 503}, // catalog unreadable is not an invalid page
		{base, 409, 503}, // a first page cannot conflict: the host is no longer master
		{base + "?cursor=1&generation=" + generation, 409, 409}, // generation changed
		{base + "?cursor=1&generation=" + generation, 400, 400},
	} {
		done = query(c.path)
		answer(c.core, `backup history`)
		result(done, c.want)
	}
	a.c.mu.Lock()
	a.c.seen[master.HostID] = time.Now().Add(-2 * a.c.cfg.StaleAfter)
	a.c.mu.Unlock()
	if body := a.call("GET", base, nil, nil, 503).Body.String(); strings.Contains(body, "changed") {
		t.Fatalf("stale master reported as changed history: %s", body)
	}
}

func TestBackupPolicyUpdateNeedsKnownPool(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	cert := certificate(t, master.CertificatePEM)
	policy := protocol.BackupPolicy{ID: "policy-1", ScheduleID: "schedule-1", PoolID: pool, Name: "Nightly", Enabled: true, RepositoryID: "repo-1",
		CaptureMode: protocol.BackupCaptureStopped, TimeZone: "UTC", Rule: protocol.BackupScheduleRule{Frequency: "daily", Hour: 2},
		VMIDs: []string{"vm-1"}, Retention: protocol.BackupRetention{KeepLatest: 7}, Revision: 3}
	managed := &protocol.ManagedState{BackupPolicies: []protocol.BackupPolicy{policy}, BackupPoliciesReported: true,
		Membership: &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
			{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}}
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: managed}})
	body := map[string]any{"request_id": "policy-update-1", "expected_revision": 3, "name": "Nightly", "enabled": true, "repository_id": "repo-2",
		"time_zone": "UTC", "rule": map[string]any{"frequency": "daily", "hour": 2, "minute": 0}, "vm_ids": []string{"vm-1"}, "retention": map[string]any{"keep_latest": 7}}
	path := "/api/v1/pools/" + pool + "/backup-policies/policy-1"
	a.call("PUT", "/api/v1/pools/no-such-pool/backup-policies/policy-1", body, nil, 404)
	a.call("PUT", path, body, nil, 202)
}
