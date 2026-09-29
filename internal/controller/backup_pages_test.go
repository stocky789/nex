// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestOversizedBackupReportUsesCorePages(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	cert := certificate(t, master.CertificatePEM)
	membership := &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	runs := make([]protocol.BackupRun, 270)
	for i := range runs {
		runs[i] = protocol.BackupRun{OperationID: fmt.Sprintf("run-%04d", i), Kind: protocol.BackupKindBackupNow, PoolID: pool,
			RepositoryID: "repo-1", Stage: "failed", Status: "failed", Error: strings.Repeat("x", 65500), CreatedAt: time.Now().UTC()}
	}
	legacy := protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability, protocol.BackupCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Membership: membership, BackupRuns: runs, BackupRunsReported: true}}}
	raw, _ := json.Marshal(legacy)
	if len(raw) <= 16<<20 {
		t.Fatalf("fixture is only %d bytes", len(raw))
	}
	a.call("POST", "/api/v1/agent/report", legacy, cert, 400)
	legacy.Inventory.Managed.BackupRuns = runs[:1]
	a.report(cert, legacy)
	var mirrored []protocol.BackupRun
	read(t, a.call("GET", "/api/v1/pools/"+pool+"/backup-runs", nil, nil, 200), &mirrored)
	if len(mirrored) != 1 {
		t.Fatalf("legacy mirror has %d runs", len(mirrored))
	}
	compact := protocol.ReportRequest{Version: protocol.Version, Sequence: 2,
		Capabilities: []string{protocol.ManagedVMCapability, protocol.BackupCapability, protocol.BackupInventoryPagesCapability},
		Inventory:    &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Membership: membership}}}
	response := a.report(cert, compact)
	if response.BackupInventoryPages == nil || !*response.BackupInventoryPages {
		t.Fatal("Controller did not acknowledge paged Core history")
	}
	fresh, capable := a.c.hostAgent(master.HostID, protocol.BackupInventoryPagesCapability, false)
	if !fresh || !capable {
		t.Fatalf("compact report not adopted: fresh=%v capable=%v", fresh, capable)
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
	if body := a.call("GET", "/api/v1/pools/"+pool+"/backup-runs", nil, nil, 503).Body.String(); strings.Contains(body, "run-0000") {
		t.Fatalf("served stale legacy history after compact report: %s", body)
	}
}

func TestBackupPageValidationRejectsForeignAndDuplicateRows(t *testing.T) {
	page := protocol.BackupRunPage{Generation: strings.Repeat("a", 64), Total: 2, Items: []protocol.BackupRun{
		{OperationID: "run-1", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: time.Now().UTC()},
		{OperationID: "run-1", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: time.Now().UTC()},
	}}
	b, _ := json.Marshal(page)
	if validBackupRelayPage(b, "pool-1", false, "") {
		t.Fatal("accepted duplicate backup run")
	}
	page.Items[1].OperationID = "run-2"
	page.Items[1].PoolID = "other-pool"
	b, _ = json.Marshal(page)
	if validBackupRelayPage(b, "pool-1", false, "") {
		t.Fatal("accepted foreign-pool backup run")
	}
}
