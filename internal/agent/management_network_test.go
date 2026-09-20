// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// writeManagementRecord plants the durable record the vmhost engine owns. The state machine
// itself is covered in internal/vmhost; these tests cover the agent wiring around it, which
// must work without reaching the real /etc.
func writeManagementRecord(t *testing.T, dir string, rec vmhost.ManagementNetworkRecord) {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "management-network.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func readManagementRecord(t *testing.T, dir string) vmhost.ManagementNetworkRecord {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "management-network.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec vmhost.ManagementNetworkRecord
	if err = json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func managementSpec() protocol.ManagementNetworkSpec {
	return protocol.ManagementNetworkSpec{Interfaces: []protocol.ManagementNIC{{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "static", Address: "192.0.2.10/24", Gateway: "192.0.2.1", Management: true}}}
}

func TestManagementNetworkCommandRoutingAndAdmission(t *testing.T) {
	if !managedCommand("management-network") {
		t.Fatal("management-network is not an admitted managed command")
	}
	cmd := protocol.Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-1", HostID: "host-9", Action: "confirm"}}
	if host := commandHost(cmd); host != "host-9" {
		t.Fatalf("command host is %q, want host-9", host)
	}
	if host := commandHost(protocol.Command{OperationID: "op-1", Kind: "management-network"}); host != "" {
		t.Fatalf("a request-less command must address no host, got %q", host)
	}
}

// Every refusal must come back as a definite failed result with Core's own sentence, and a
// conflict must stay recognizable as one: the Controller maps exactly that suffix to 409.
func TestManagementNetworkRefusalsAreDefiniteResults(t *testing.T) {
	dir := t.TempDir()
	osHostname = func() (string, error) { return "core1.example.test", nil }
	t.Cleanup(func() { osHostname = os.Hostname })
	for name, tc := range map[string]struct {
		cmd      protocol.Command
		contains string
		conflict bool
	}{
		"no request": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "management-network"},
			contains: "carries no request",
		},
		"request for another operation": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-2", HostID: "host-1", Action: "confirm"}},
			contains: "does not match its command",
		},
		"unknown action": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: "reboot"}},
			contains: "invalid management network action",
		},
		"apply without confirmation": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: "apply"}},
			contains: "requires the host name as confirmation",
		},
		"apply naming another host": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: "apply", ChangeID: "change-7", Confirmation: "core2.example.test"}},
			contains: "does not name this host",
		},
		"apply naming no change": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: "apply", Confirmation: "core1.example.test"}},
			contains: "must name the staged change it targets",
		},
		// The engine matches the change the request names, not the command's own operation
		// id: every durable command has a unique one, and only the change is shared.
		"confirm with no record": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: "confirm", ChangeID: "change-7"}},
			contains: "no management network change is applied as change-7",
			conflict: true,
		},
		"cancel with no record": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: "cancel", ChangeID: "change-7"}},
			contains: "no management network change is staged as change-7",
			conflict: true,
		},
		"cancel naming no change": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: "cancel"}},
			contains: "must name the staged change it targets",
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := ExecuteManagementNetwork(context.Background(), dir, tc.cmd)
			if result.Status != "failed" || result.OperationID != "op-1" {
				t.Fatalf("result is %+v, want a failed op-1", result)
			}
			if !strings.Contains(result.Error, tc.contains) {
				t.Fatalf("error %q does not contain %q", result.Error, tc.contains)
			}
			if got := strings.HasSuffix(result.Error, protocol.ManagementNetworkConflict); got != tc.conflict {
				t.Fatalf("conflict marker is %v, want %v, in %q", got, tc.conflict, result.Error)
			}
		})
	}
	// The hostname check accepts the host's own name whatever case it is typed in; the
	// refusal above proves it is not simply skipped.
	cmd := protocol.Command{OperationID: "op-9", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-9", HostID: "host-1", Action: "apply", ChangeID: "change-7", Confirmation: " CORE1.example.TEST "}}
	if result := ExecuteManagementNetwork(context.Background(), dir, cmd); strings.Contains(result.Error, "does not name this host") {
		t.Fatalf("the host's own name was refused: %+v", result)
	}
}

// A confirm is refused until the host has observed its own connectivity, and a successful
// authenticated Controller report is one of the two observations that count.
func TestReportRecordsManagementConnectivityAndProjectsStatus(t *testing.T) {
	reports := make(chan protocol.ReportRequest, 2)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		reports <- request
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	applied := time.Now().UTC().Add(-time.Minute)
	writeManagementRecord(t, s.dir, vmhost.ManagementNetworkRecord{
		OperationID: "op-1", Revision: 3, Stage: protocol.ManagementNetworkApplied, Spec: managementSpec(),
		WindowSeconds: 600, AppliedAt: applied, Deadline: applied.Add(10 * time.Minute), UpdatedAt: applied,
	})
	cfg := RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err != nil {
		t.Fatal(err)
	}
	request := <-reports
	status := request.Inventory.ManagementNetwork
	if status == nil || status.Stage != protocol.ManagementNetworkApplied || status.Revision != 3 || status.OperationID != "op-1" {
		t.Fatalf("management network status was not projected: %#v", status)
	}
	if status.WindowSeconds != 600 {
		t.Fatalf("the recorded rollback window is not reported: %#v", status)
	}
	if status.SecondsRemaining <= 0 || status.SecondsRemaining > 600 {
		t.Fatalf("seconds remaining is %d, want a running deadline", status.SecondsRemaining)
	}
	if status.Spec == nil || len(status.Spec.Interfaces) != 1 || status.Spec.Interfaces[0].Name != "nex0" {
		t.Fatalf("the reported spec is %#v", status.Spec)
	}
	if !hasCapability(request.Capabilities, protocol.ManagementNetworkCapability) {
		t.Fatalf("capabilities omit %s: %v", protocol.ManagementNetworkCapability, request.Capabilities)
	}
	for _, check := range request.Inventory.Checks {
		if check.Name == "management-network" {
			t.Fatalf("an applied change must not report a failing check: %+v", check)
		}
	}
	// A report that reached the Controller proves nothing on its own: this one went out over
	// loopback, which is not an address the applied spec puts on the management bridge, so the
	// host must not treat it as having carried management traffic. That is the whole point of
	// the observation — a confirmed change drops its rollback. (An observation that does count
	// cannot be produced in process; see the native lab checklist.)
	rec := readManagementRecord(t, s.dir)
	if !rec.ConnectivityAt.IsZero() {
		t.Fatalf("a report over loopback was recorded as connectivity over the new configuration: %+v", rec)
	}
	confirmRefused := protocol.Command{OperationID: "op-early", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-early", HostID: identity.HostID, Action: "confirm", ChangeID: "op-1"}}
	if result := ExecuteManagementNetwork(context.Background(), s.dir, confirmRefused); result.Status != "failed" || !strings.Contains(result.Error, "has not yet been reached") {
		t.Fatalf("confirm must be refused without an observation on the bridge: %+v", result)
	}
	// With the observation recorded, the confirmation the engine refuses without it succeeds,
	// and the rolled-back outcome of a later change is reported as a failing check instead.
	if err := vmhost.ManagementConnectivityForTests(s.dir); err != nil {
		t.Fatal(err)
	}
	// The confirming command is its own operation, and names the change the host staged.
	confirm := protocol.Command{OperationID: "op-confirm", Kind: "management-network", ManagementNetwork: &protocol.ManagementNetworkRequest{OperationID: "op-confirm", HostID: identity.HostID, Action: "confirm", ChangeID: "op-1"}}
	if result := ExecuteManagementNetwork(context.Background(), s.dir, confirm); result.Status != "succeeded" {
		t.Fatalf("confirm after an observed report: %+v", result)
	}
	writeManagementRecord(t, s.dir, vmhost.ManagementNetworkRecord{
		OperationID: "op-2", Revision: 4, Stage: protocol.ManagementNetworkRolledBack, Spec: managementSpec(),
		Error: "the change was not confirmed before the rollback deadline", UpdatedAt: time.Now().UTC(),
	})
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err != nil {
		t.Fatal(err)
	}
	request = <-reports
	found := false
	for _, check := range request.Inventory.Checks {
		if check.Name == "management-network" {
			found = true
			if check.Status != "failed" || !strings.Contains(check.Error, "rollback deadline") {
				t.Fatalf("rollback check is %+v", check)
			}
		}
	}
	if !found {
		t.Fatal("an automatic rollback reported no management-network check")
	}
	// The same stage reached the other way: the operator cancelled an applied change and the
	// host restored the previous configuration because they asked. That is the outcome they
	// chose, not a failure, so it is reported as the record's own reason and no check sends
	// anyone looking for something to fix.
	writeManagementRecord(t, s.dir, vmhost.ManagementNetworkRecord{
		OperationID: "op-3", Revision: 4, Stage: protocol.ManagementNetworkRolledBack, Spec: managementSpec(),
		Error: vmhost.ManagementRollbackRequested, RequestedRollback: true, UpdatedAt: time.Now().UTC(),
	})
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err != nil {
		t.Fatal(err)
	}
	request = <-reports
	if status := request.Inventory.ManagementNetwork; status == nil || !status.RequestedRollback || status.LastError != vmhost.ManagementRollbackRequested {
		t.Fatalf("the operator's own rollback was not reported as such: %#v", status)
	}
	for _, check := range request.Inventory.Checks {
		if check.Name == "management-network" {
			t.Fatalf("a rollback the operator asked for must not report a failing check: %+v", check)
		}
	}
	// The one state that means "this host tried to save itself and could not": the record is
	// still applied because the restore has not finished, and it carries the reason. Without a
	// check of its own it renders as an ordinary applied change whose countdown has run out —
	// including when the operator asked for the rollback, where the stage alone says nothing.
	writeManagementRecord(t, s.dir, vmhost.ManagementNetworkRecord{
		OperationID: "op-4", Revision: 4, Stage: protocol.ManagementNetworkApplied, Spec: managementSpec(),
		Error: vmhost.ManagementRollbackRequested, RequestedRollback: true, UpdatedAt: time.Now().UTC(),
	})
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err != nil {
		t.Fatal(err)
	}
	request = <-reports
	found = false
	for _, check := range request.Inventory.Checks {
		if check.Name == "management-network" {
			found = true
			if check.Status != "failed" || !strings.Contains(check.Error, "has not completed") {
				t.Fatalf("a failed rollback must report why: %+v", check)
			}
		}
	}
	if !found {
		t.Fatal("a rollback that could not complete reported no management-network check")
	}
}

func hasCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}
