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

// A missing host observation must never settle an adopted needs_attention backup outcome, for
// every backup kind (the settle list is built from backupOutcomeKinds so it cannot drift).
func TestMissingObservationNeverSettlesBackupOutcomes(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	seq := uint64(0)
	for _, kind := range backupOutcomeKinds {
		seq++
		a.report(cert, protocol.ReportRequest{Version: 1, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{
			Hostname: "core", Managed: &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{{OperationID: "op-" + kind, Kind: kind, ID: "11111111-1111-4111-8111-111111111111", Status: "needs_attention"}}},
		}})
	}
	seq++
	a.report(cert, protocol.ReportRequest{Version: 1, Sequence: seq, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{}}})
	for _, kind := range backupOutcomeKinds {
		var status string
		if err := a.c.db.QueryRow("SELECT status FROM operations WHERE request_id=?", "adopt-outcome:"+host.HostID+":op-"+kind).Scan(&status); err != nil || status != "needs_attention" {
			t.Errorf("%s: status %q err %v; want needs_attention", kind, status, err)
		}
	}
}

// Forget and orphan cleanup carry the repository only in the URL: a request_id replayed
// against another repository is a payload conflict, not an accepted replay.
func TestForgetAndOrphanCleanupReplayIsScopedToRepository(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: hostDefaults(master.HostID)},
	})
	base := "/api/v1/pools/" + pool + "/backup-repositories/"
	cleanup := map[string]any{"request_id": "scope-clean", "vm_ids": []string{"vm-gone"}, "confirmation": protocol.OrphanCleanupConfirmation(2, 0, false), "recovery_points": 2}
	a.call("POST", base+"repo-a/orphan-cleanups", cleanup, nil, 202)
	a.call("POST", base+"repo-a/orphan-cleanups", cleanup, nil, 200)
	a.call("POST", base+"repo-b/orphan-cleanups", cleanup, nil, 409)
	forget := map[string]any{"request_id": "scope-forget", "confirmation": "Same name", "recovery_points": 1}
	a.call("POST", base+"repo-a/forget", forget, nil, 202)
	a.call("POST", base+"repo-a/forget", forget, nil, 200)
	a.call("POST", base+"repo-b/forget", forget, nil, 409)
	a.call("POST", "/api/v1/pools/no-such-pool/backup-repositories/repo-a/forget", map[string]any{"request_id": "scope-none", "confirmation": "Same name", "recovery_points": 1}, nil, 404)

	for _, id := range []string{"scope-clean", "scope-forget"} {
		var payload string
		if err := a.c.db.QueryRow("SELECT payload FROM managed_resources WHERE request_id=?", id).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(payload, `"repository_id":"repo-a"`) {
			t.Fatalf("%s payload omitted path repository: %s", id, payload)
		}
	}
}

func TestValidBackupOrphanListRejectsMismatchedRepository(t *testing.T) {
	ok, _ := json.Marshal(protocol.BackupOrphanQuery{RepositoryID: "repo-a", Groups: []protocol.BackupOrphanGroup{}})
	if !validBackupOrphanList(ok, "repo-a") {
		t.Fatal("rejected matching orphan list")
	}
	bad, _ := json.Marshal(protocol.BackupOrphanQuery{RepositoryID: "repo-b", Groups: []protocol.BackupOrphanGroup{}})
	if validBackupOrphanList(bad, "repo-a") {
		t.Fatal("accepted orphan list for another repository")
	}
	if validBackupOrphanList([]byte(`{`), "repo-a") {
		t.Fatal("accepted invalid orphan list JSON")
	}
}

// Orphan list and cleanup-record GETs must not relay a Core body that names another repository.
func TestOrphanRelayRejectsMismatchedRepositoryID(t *testing.T) {
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
	base := "/api/v1/pools/" + pool + "/backup-repositories/repo-a/"
	done := query(base + "orphans")
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
	conn.SetDeadline(time.Now().Add(10 * time.Second))
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

	matchList, _ := json.Marshal(protocol.BackupOrphanQuery{RepositoryID: "repo-a", Groups: []protocol.BackupOrphanGroup{}})
	if req := answer(200, string(matchList)); req.URL.Query().Get("repository_id") != "repo-a" {
		t.Fatalf("list filter not relayed: %s", req.URL)
	}
	result(done, 200)

	done = query(base + "orphans")
	foreignList, _ := json.Marshal(protocol.BackupOrphanQuery{RepositoryID: "repo-b", Groups: []protocol.BackupOrphanGroup{}})
	answer(200, string(foreignList))
	result(done, 502)

	matchCleanup, _ := json.Marshal(protocol.BackupOrphanCleanup{
		OperationID: "clean-1", Status: "succeeded",
		Request: protocol.OrphanCleanupRequest{RepositoryID: "repo-a", VMIDs: []string{"vm-gone"}, Confirmation: "delete 1 recovery points", RecoveryPoints: 1},
		Points:  []protocol.BackupOrphanPointResult{}, UpdatedAt: time.Now().UTC(),
	})
	done = query(base + "orphan-cleanups/clean-1")
	if req := answer(200, string(matchCleanup)); req.URL.Query().Get("request_id") != "clean-1" {
		t.Fatalf("cleanup filter not relayed: %s", req.URL)
	}
	result(done, 200)

	foreignCleanup, _ := json.Marshal(protocol.BackupOrphanCleanup{
		OperationID: "clean-1", Status: "succeeded",
		Request: protocol.OrphanCleanupRequest{RepositoryID: "repo-b", VMIDs: []string{"vm-gone"}, Confirmation: "delete 1 recovery points", RecoveryPoints: 1},
		Points:  []protocol.BackupOrphanPointResult{}, UpdatedAt: time.Now().UTC(),
	})
	done = query(base + "orphan-cleanups/clean-1")
	answer(200, string(foreignCleanup))
	result(done, 404)
}

// A backup operation whose command carries an empty host_id (a run-now task) is shown on the
// host that queued it, not on a blank one.
func TestOperationsKeepHostWhenCommandHostIsEmpty(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := a.c.db.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", "op-run-now", master.HostID, "backup-policy-run-now", "req-run-now", "queued", "", now, now, "root"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.c.db.Exec("INSERT INTO managed_resources VALUES(?,?,?,?,?,?,?,?,?)", "res-run-now", "backup-policy-run-now", pool, master.HostID, "run", "req-run-now", "{}", `{"vm":{"host_id":""},"restore":{"host_id":""}}`, "op-run-now"); err != nil {
		t.Fatal(err)
	}
	var ops []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &ops)
	if len(ops) != 1 || ops[0].HostID != master.HostID {
		t.Fatalf("run-now task lost its host: %+v", ops)
	}
}

func TestAgentDecodeNamesUnknownFieldForUpgrade(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	body := a.call("POST", "/api/v1/agent/report", map[string]any{"version": 1, "sequence": 1, "removed_field": true}, certificate(t, master.CertificatePEM), 400).Body.String()
	if !strings.Contains(body, "removed_field") || !strings.Contains(body, "upgrade or restart Nex Core") {
		t.Fatalf("unknown field not named for the operator: %s", body)
	}
}
