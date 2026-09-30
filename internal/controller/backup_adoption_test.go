// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestFreshControllerRetainsMasterBackupInventory(t *testing.T) {
	a := newClient(t)
	site := a.site()
	master := a.enroll(a.token(site, ""), csr(t))
	member := a.enroll(a.token(site, ""), csr(t))
	pool := a.poolOf(master.HostID)
	membership := &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
	}}
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: pool, Name: "Primary NFS", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
	}
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{
		Membership:         membership,
		BackupRepositories: []protocol.BackupRepository{repo}, BackupRepositoriesReported: true,
	}}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})

	var repos []protocol.BackupRepository
	read(t, a.call("GET", "/api/v1/pools/"+pool+"/backup-repositories", nil, nil, 200), &repos)
	if len(repos) != 1 || repos[0].ID != "repo-1" || repos[0].Name != "Primary NFS" {
		t.Fatalf("retained repositories %#v", repos)
	}

	// Member cannot report master backup authority under its own certificate.
	a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv}, certificate(t, member.CertificatePEM), 400)
}

func TestBackupInventoryMissingReportedIsUnavailable(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	membership := &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
	}}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Membership: membership}},
	})
	if body := a.call("GET", "/api/v1/pools/"+pool+"/backup-repositories", nil, nil, 503).Body.String(); !strings.Contains(body, "unavailable") {
		t.Fatalf("missing reported flag: %s", body)
	}

	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{
		Membership: membership,
		BackupRepositories: []protocol.BackupRepository{{
			ID: "repo-1", PoolID: pool, Name: "Primary NFS", Kind: protocol.BackupRepoKindNFS,
			Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
		}},
	}}
	if validateInventory(inv) == nil {
		t.Fatal("accepted nonempty backup repositories without reported flag")
	}
}

func TestControllerBackupNowRelayAndConflict(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	var created createdVM
	read(t, l.create("create-backup-vm", l.spec("backup-guest"), 202), &created)
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	var queued map[string]any
	read(t, l.a.call("POST", "/api/v1/vms/"+created.VMID+"/backup", map[string]string{
		"request_id": "ctrl-backup-1", "repository_id": "repo-1",
	}, nil, 202), &queued)
	op := queued["operation"].(map[string]any)
	if op["id"] != "ctrl-backup-1" || op["kind"] != "backup-now" || op["status"] != "queued" {
		t.Fatalf("queued backup %#v", queued)
	}
	if queued["repository_id"] != "repo-1" || queued["vm_id"] != created.VMID {
		t.Fatalf("backup response payload %#v", queued)
	}
	if w := l.a.call("POST", "/api/v1/vms/"+created.VMID+"/backup", map[string]string{
		"request_id": "ctrl-backup-2", "repository_id": "repo-1",
	}, nil, 409); !strings.Contains(w.Body.String(), "still pending") {
		t.Fatalf("pending backup gate: %s", w.Body.String())
	}

	l.seq++
	defaults := hostDefaults(l.host.HostID)
	defaults.VMs = []protocol.VMCreateSpec{applied}
	defaults.Outcomes = []protocol.ManagedOutcome{{
		OperationID: "ctrl-backup-1", Kind: "backup-now", ID: created.VMID, Status: "queued",
	}}
	inv := &protocol.Inventory{Hostname: "core", CPUCount: 8, MemoryBytes: 16 << 30,
		NICs:    []protocol.NIC{{Name: "nex0", Kind: "physical", Master: "br0"}, {Name: "br0", Kind: "bridge"}},
		Managed: defaults, Checks: []protocol.Check{{Name: "libvirt", Status: "healthy", ObservedAt: time.Now().UTC()}},
		VMs: []protocol.VM{{ID: created.VMID, Name: "backup-guest", State: "shut off"}}}
	response := l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv})
	if len(response.Commands) != 1 || response.Commands[0].OperationID != "ctrl-backup-1" || response.Commands[0].Kind != "backup-now" {
		t.Fatalf("report commands %#v", response.Commands)
	}
	if response.Commands[0].Backup == nil || response.Commands[0].Backup.RepositoryID != "repo-1" || response.Commands[0].VM == nil {
		t.Fatalf("backup command payload %#v", response.Commands[0])
	}
}

func TestBackupResultRecoversControllerAttention(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	var created createdVM
	read(t, l.create("create-recovery-vm", l.spec("recovery-guest"), 202), &created)
	command := l.report().Commands[0]
	vm := *command.VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{vm}}, protocol.Result{OperationID: command.OperationID, Status: "succeeded"})

	other := l.a.enroll(l.a.token(l.a.site(), ""), csr(t))
	otherCert := certificate(t, other.CertificatePEM)
	for _, final := range []string{"succeeded", "failed"} {
		id := "backup-recover-" + final
		l.a.call("POST", "/api/v1/vms/"+created.VMID+"/backup", map[string]string{
			"request_id": id, "repository_id": "repo-1",
		}, nil, 202)
		if commands := l.report().Commands; len(commands) != 1 || commands[0].OperationID != id {
			t.Fatalf("backup %s was not dispatched: %+v", id, commands)
		}
		l.report(protocol.Result{OperationID: id, Status: "needs_attention", Error: "outcome uncertain"})
		if got := backupOperationStatus(t, l, id); got != "needs_attention" {
			t.Fatalf("backup %s attention: %q", id, got)
		}
		l.a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 1,
			Capabilities: []string{protocol.ManagedVMCapability}, Results: []protocol.Result{{OperationID: id, Status: final}}}, otherCert, 400)
		if got := backupOperationStatus(t, l, id); got != "needs_attention" {
			t.Fatalf("other host changed backup %s: %q", id, got)
		}
		if ack := l.report(protocol.Result{OperationID: id, Status: final}); len(ack.AcknowledgedResults) != 1 || ack.AcknowledgedResults[0] != id {
			t.Fatalf("backup %s final result was not acknowledged: %+v", id, ack)
		}
		if got := backupOperationStatus(t, l, id); got != final {
			t.Fatalf("backup %s recovered as %q, want %q", id, got, final)
		}
	}
}

func backupOperationStatus(t *testing.T, l *managedLab, id string) string {
	t.Helper()
	for _, op := range l.operations() {
		if op.ID == id {
			return op.Status
		}
	}
	return ""
}

func TestBackupOutcomeRecoversAcknowledgedControllerAttention(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	id := "restore-recover-from-outcome"
	targetID := "22222222-2222-4222-8222-222222222222"
	l.a.call("POST", "/api/v1/pools/"+l.pool+"/backup-restore", map[string]string{
		"request_id": id, "repository_id": "repo-1", "recovery_point_id": "point-1",
		"new_vm_id": targetID, "new_vm_name": "Recovered", "storage_id": l.storage, "host_id": l.host.HostID,
	}, nil, 202)
	if commands := l.report().Commands; len(commands) != 1 || commands[0].OperationID != id {
		t.Fatalf("restore was not dispatched: %+v", commands)
	}
	l.report(protocol.Result{OperationID: id, Status: "needs_attention", Error: "outcome uncertain"})
	if got := backupOperationStatus(t, l, id); got != "needs_attention" {
		t.Fatalf("restore attention: %q", got)
	}
	l.seq++
	managed := hostDefaults(l.host.HostID)
	managed.Outcomes = []protocol.ManagedOutcome{{OperationID: id, Kind: "backup-restore-as-new", ID: targetID, Name: "Recovered", Status: "succeeded"}}
	l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps,
		Inventory: &protocol.Inventory{Hostname: "core", Managed: managed}})
	if got := backupOperationStatus(t, l, id); got != "succeeded" {
		t.Fatalf("durable Core outcome did not recover acknowledged restore: %q", got)
	}
}

func TestBackupMetadataOutcomeRecoversAcknowledgedControllerAttention(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	id := "verify-recover-from-outcome"
	testVMID := "33333333-3333-4333-8333-333333333333"
	l.a.call("POST", "/api/v1/pools/"+l.pool+"/backup-test-verify", map[string]string{
		"request_id": id, "repository_id": "repo-1", "recovery_point_id": "point-1",
		"test_vm_id": testVMID, "result": "passed",
	}, nil, 202)
	if commands := l.report().Commands; len(commands) != 1 || commands[0].OperationID != id {
		t.Fatalf("verification was not dispatched: %+v", commands)
	}
	l.report(protocol.Result{OperationID: id, Status: "needs_attention", Error: "outcome uncertain"})
	l.seq++
	managed := hostDefaults(l.host.HostID)
	managed.Outcomes = []protocol.ManagedOutcome{{OperationID: id, Kind: "backup-test-verify", ID: testVMID, Status: "succeeded"}}
	l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps,
		Inventory: &protocol.Inventory{Hostname: "core", Managed: managed}})
	if got := backupOperationStatus(t, l, id); got != "succeeded" {
		t.Fatalf("durable Core verification outcome did not recover task: %q", got)
	}
}

func TestBackupOutcomeAdoptionSurvivesOperationIDCollisions(t *testing.T) {
	a := newClient(t)
	site := a.site()
	first := a.enroll(a.token(site, ""), csr(t))
	second := a.enroll(a.token(site, ""), csr(t))
	for _, row := range []struct {
		id, host, pool string
	}{
		{"shared-with-other-host", first.HostID, a.poolOf(first.HostID)},
		{"shared-with-other-kind", second.HostID, a.poolOf(second.HostID)},
	} {
		tx, err := a.c.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		command := protocol.Command{OperationID: row.id, Kind: "backup-now"}
		if _, err := a.c.saveResourceStatus(tx, "operator", row.id, row.pool, row.host,
			"source-vm", "backup:"+row.id, "needs_attention", command, map[string]string{"request_id": row.id}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	a.report(certificate(t, second.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1,
		Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "second", Managed: &protocol.ManagedState{
			Outcomes: []protocol.ManagedOutcome{
				{OperationID: "shared-with-other-host", Kind: "backup-now", ID: "other-vm", Status: "succeeded"},
				{OperationID: "shared-with-other-kind", Kind: "backup-policy-upsert", ID: "policy-1", Status: "succeeded"},
			},
		}}})
	for _, id := range []string{"shared-with-other-host", "shared-with-other-kind"} {
		var kind, status string
		if err := a.c.db.QueryRow("SELECT kind,status FROM operations WHERE request_id=?", "adopt-outcome:"+second.HostID+":"+id).Scan(&kind, &status); err != nil || status != "succeeded" {
			t.Fatalf("colliding outcome %s was suppressed: %q %q %v", id, kind, status, err)
		}
	}
}

func TestBackupMemberOperationsRouteThroughMaster(t *testing.T) {
	a := newClient(t)
	site := a.site()
	master := a.enroll(a.token(site, ""), csr(t))
	member := a.enroll(a.token(site, ""), csr(t))
	pool := a.poolOf(master.HostID)
	if _, err := a.c.db.Exec("UPDATE hosts SET pool_id=? WHERE id=?", pool, member.HostID); err != nil {
		t.Fatal(err)
	}
	caps := []string{protocol.ManagedVMCapability}
	membership := &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: member.HostID, Hostname: member.HostID, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"},
	}}
	masterCert, memberCert := certificate(t, master.CertificatePEM), certificate(t, member.CertificatePEM)
	masterState := hostDefaults(master.HostID)
	masterState.Membership = membership
	a.report(masterCert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: caps,
		Inventory: &protocol.Inventory{Hostname: "master", Managed: masterState}})
	vmID := "11111111-1111-4111-8111-111111111111"
	vm := vmSpec(pool, member.HostID, "member-guest", "member-storage", "member-network", "")
	vm.ID = vmID
	memberState := hostDefaults(member.HostID)
	memberState.Storage[0].ID = "member-storage"
	memberState.Storage[0].PoolID = pool
	memberState.Networks[0].ID = "member-network"
	memberState.Networks[0].PoolID = pool
	memberState.VMs = []protocol.VMCreateSpec{vm}
	a.report(memberCert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: caps,
		Inventory: &protocol.Inventory{Hostname: "member", Managed: memberState}})

	backupPath := "/api/v1/vms/" + vmID + "/backup"
	backupBody := map[string]string{"request_id": "member-backup", "repository_id": "repo-1"}
	a.call("POST", backupPath, backupBody, nil, 202)
	a.call("POST", backupPath, backupBody, nil, 200)
	var addressed string
	if err := a.c.db.QueryRow("SELECT host_id FROM operations WHERE id='member-backup'").Scan(&addressed); err != nil || addressed != master.HostID {
		t.Fatalf("member backup addressed to %q: %v", addressed, err)
	}
	backupCommands := a.report(masterCert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: caps,
		Inventory: &protocol.Inventory{Hostname: "master", Managed: masterState}}).Commands
	if len(backupCommands) != 1 || backupCommands[0].Kind != "backup-now" || backupCommands[0].VM == nil || backupCommands[0].VM.HostID != member.HostID {
		t.Fatalf("master backup command: %+v", backupCommands)
	}
	if _, err := a.c.db.Exec("UPDATE operations SET status='succeeded' WHERE id='member-backup'"); err != nil {
		t.Fatal(err)
	}

	base := "/api/v1/pools/" + pool
	for _, tc := range []struct {
		path string
		body map[string]any
		kind string
	}{
		{base + "/backup-restore", map[string]any{"request_id": "member-restore-new", "host_id": member.HostID, "repository_id": "repo-1", "recovery_point_id": "point-1", "new_vm_id": "22222222-2222-4222-8222-222222222222", "new_vm_name": "Recovered", "storage_id": "member-storage"}, "backup-restore-as-new"},
		{base + "/backup-test-restore", map[string]any{"request_id": "member-test-restore", "host_id": member.HostID, "repository_id": "repo-1", "recovery_point_id": "point-1", "new_vm_id": "33333333-3333-4333-8333-333333333333", "new_vm_name": "Test recovered", "storage_id": "member-storage"}, protocol.BackupKindTestRestore},
		{base + "/backup-restore-replace", map[string]any{"request_id": "member-restore-replace", "host_id": member.HostID, "repository_id": "repo-1", "recovery_point_id": "point-1", "original_vm_id": vmID, "storage_id": "member-storage", "confirm_replace": true}, protocol.BackupKindRestoreReplace},
	} {
		a.call("POST", tc.path, tc.body, nil, 202)
		a.call("POST", tc.path, tc.body, nil, 200)
		var raw string
		if err := a.c.db.QueryRow("SELECT host_id,command FROM managed_resources WHERE request_id=?", tc.body["request_id"]).Scan(&addressed, &raw); err != nil || addressed != master.HostID {
			t.Fatalf("%s addressed to %q: %v", tc.kind, addressed, err)
		}
		var command protocol.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil || command.Kind != tc.kind {
			t.Fatalf("%s command: %+v, %v", tc.kind, command, err)
		}
		var target string
		switch tc.kind {
		case "backup-restore-as-new":
			target = command.Restore.HostID
		case protocol.BackupKindTestRestore:
			target = command.TestRestore.HostID
		case protocol.BackupKindRestoreReplace:
			target = command.RestoreReplace.HostID
		}
		if target != member.HostID {
			t.Fatalf("%s target host = %q", tc.kind, target)
		}
		var listed []protocol.Operation
		read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &listed)
		for _, op := range listed {
			if op.RequestID == tc.body["request_id"] && op.HostID != member.HostID {
				t.Fatalf("%s task history shows host %q, want executing host %q", tc.kind, op.HostID, member.HostID)
			}
		}
	}
	commands := a.report(masterCert, protocol.ReportRequest{Version: 1, Sequence: 3, Capabilities: caps,
		Inventory: &protocol.Inventory{Hostname: "master", Managed: masterState}}).Commands
	if len(commands) != 3 {
		t.Fatalf("master commands: %+v", commands)
	}
	if got := a.report(memberCert, protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: caps,
		Inventory: &protocol.Inventory{Hostname: "member", Managed: memberState}}).Commands; len(got) != 0 {
		t.Fatalf("member received master-owned work: %+v", got)
	}
	a.c.mu.Lock()
	a.c.seen[master.HostID] = time.Now().Add(-2 * a.c.cfg.StaleAfter)
	a.c.mu.Unlock()
	a.call("POST", backupPath, backupBody, nil, 200)
	a.call("POST", backupPath, map[string]string{"request_id": "master-unavailable", "repository_id": "repo-1"}, nil, 409)
}

func TestControllerBackupNowPendingIncludesReplace(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	var created createdVM
	read(t, l.create("create-backup-replace-gate", l.spec("backup-replace-gate"), 202), &created)
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})

	pool := l.a.poolOf(l.host.HostID)
	var queued map[string]any
	read(t, l.a.call("POST", "/api/v1/pools/"+pool+"/backup-restore-replace", map[string]any{
		"request_id": "ctrl-replace-1", "host_id": l.host.HostID, "repository_id": "repo-1",
		"recovery_point_id": "point-1", "original_vm_id": created.VMID, "storage_id": l.storage,
		"confirm_replace": true,
	}, nil, 202), &queued)
	if queued["operation"].(map[string]any)["kind"] != "backup-restore-replace" {
		t.Fatalf("queued replace %#v", queued)
	}
	if w := l.a.call("POST", "/api/v1/vms/"+created.VMID+"/backup", map[string]string{
		"request_id": "ctrl-backup-after-replace", "repository_id": "repo-1",
	}, nil, 409); !strings.Contains(w.Body.String(), "still pending") {
		t.Fatalf("pending replace must block backup-now: %s", w.Body.String())
	}
}

func TestControllerBackupRepositoryRelay(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	cert := certificate(t, master.CertificatePEM)
	a.report(cert, protocol.ReportRequest{
		Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: hostDefaults(master.HostID)},
	})

	var queued map[string]any
	read(t, a.call("POST", "/api/v1/pools/"+pool+"/backup-repositories", map[string]any{
		"request_id": "repo-reg-1", "id": "repo-1", "name": "Primary NFS", "kind": "nfs",
		"source": map[string]string{"server": "nfs.example.test", "export": "/export/backups"},
	}, nil, 202), &queued)
	op := queued["operation"].(map[string]any)
	if op["kind"] != "backup-repository-register" || queued["repository_id"] != "repo-1" {
		t.Fatalf("queued repository %#v", queued)
	}
	a.call("POST", "/api/v1/pools/"+pool+"/backup-repositories", map[string]any{
		"request_id": "repo-reg-1", "id": "repo-1", "name": "Primary NFS", "kind": "nfs",
		"source": map[string]string{"server": "nfs.example.test", "export": "/export/backups"},
	}, nil, 200)

	read(t, a.call("POST", "/api/v1/pools/"+pool+"/backup-repositories/repo-1/test", map[string]string{
		"request_id": "repo-test-1",
	}, nil, 202), &queued)
	op = queued["operation"].(map[string]any)
	if op["kind"] != "backup-repository-test" {
		t.Fatalf("queued test %#v", queued)
	}
}

func TestControllerForgetBackupRepositoryRoute(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: hostDefaults(master.HostID)},
	})
	path := "/api/v1/pools/" + pool + "/backup-repositories/repo-1/forget"
	body := map[string]any{"request_id": "forget-1", "confirmation": "Primary NFS", "recovery_points": 3, "protected_points": 1}
	csrf, cookie := a.csrf, a.cookie
	a.csrf = ""
	a.call("POST", path, body, nil, 403)
	a.csrf, a.cookie = csrf, nil
	a.call("POST", path, body, nil, 401)
	a.cookie = cookie
	a.call("POST", path, map[string]any{"request_id": "repo-1", "confirmation": "Primary NFS"}, nil, 400)
	a.call("POST", path, map[string]any{"request_id": "forget-neg", "recovery_points": -1}, nil, 400)

	var queued struct {
		Operation protocol.Operation `json:"operation"`
	}
	read(t, a.call("POST", path, body, nil, 202), &queued)
	if queued.Operation.ID != "forget-1" || queued.Operation.Kind != "backup-repository-forget" {
		t.Fatalf("queued forget %+v", queued.Operation)
	}
	a.call("POST", path, body, nil, 200)
	body["recovery_points"] = 4
	a.call("POST", path, body, nil, 409)
	for _, command := range a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability}}).Commands {
		if command.Kind == "backup-repository-forget" && command.ForgetRepository != nil && command.ForgetRepository.RepositoryID == "repo-1" &&
			command.ForgetRepository.Confirmation == "Primary NFS" && command.ForgetRepository.RecoveryPoints == 3 && command.ForgetRepository.ProtectedPoints == 1 {
			return
		}
	}
	t.Fatal("forget command was not delivered to the pool master")
}

func TestBackupMasterActionsReturnOperationAndReplay(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: hostDefaults(master.HostID)},
	})
	protectPath := "/api/v1/pools/" + pool + "/backup-protect"
	csrf := a.csrf
	a.csrf = ""
	a.call("POST", protectPath, map[string]any{"request_id": "auth-op", "repository_id": "repo-1", "recovery_point_id": "point-1", "protected": true}, nil, 403)
	a.csrf = csrf
	a.call("POST", protectPath, map[string]any{"request_id": "invalid-op", "repository_id": ""}, nil, 400)
	for _, tc := range []struct {
		path string
		body map[string]any
	}{
		{"backup-protect", map[string]any{"repository_id": "repo-1", "recovery_point_id": "point-1", "protected": true}},
		{"backup-test-verify", map[string]any{"repository_id": "repo-1", "recovery_point_id": "point-1", "test_vm_id": "vm-1", "result": "passed"}},
		{"backup-test-cleanup", map[string]any{"repository_id": "repo-1", "recovery_point_id": "point-1", "test_vm_id": "vm-1"}},
		{"backup-catalog/import", map[string]any{"repository_id": "repo-1"}},
		{"backup-catalog/rebuild", map[string]any{"repository_id": "repo-1"}},
	} {
		body := tc.body
		body["request_id"] = "op-" + strings.ReplaceAll(tc.path, "/", "-")
		path := "/api/v1/pools/" + pool + "/" + tc.path
		for _, want := range []int{202, 200} {
			var reply struct {
				Operation protocol.Operation `json:"operation"`
			}
			read(t, a.call("POST", path, body, nil, want), &reply)
			if reply.Operation.ID != body["request_id"] {
				t.Fatalf("%s operation: %+v", tc.path, reply.Operation)
			}
		}
	}
	a.call("POST", protectPath, map[string]any{
		"request_id": "op-backup-protect", "repository_id": "repo-1", "recovery_point_id": "point-1", "protected": false,
	}, nil, 409)
	other := a.enroll(a.token(a.site(), ""), csr(t))
	otherPool := a.poolOf(other.HostID)
	a.call("POST", "/api/v1/pools/"+otherPool+"/backup-protect", map[string]any{
		"request_id": "op-backup-protect", "repository_id": "repo-1", "recovery_point_id": "point-1", "protected": true,
	}, nil, 409)
}

func TestBackupRestoreRejectsUnsafeVMID(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	a.call("POST", "/api/v1/pools/"+a.poolOf(master.HostID)+"/backup-restore", map[string]any{
		"request_id": "restore-unsafe", "host_id": master.HostID,
		"repository_id": "repo-1", "recovery_point_id": "point-1", "new_vm_id": "../../outside",
		"new_vm_name": "Restored", "storage_id": "storage-1",
	}, nil, 400)
}

func TestBackupPolicyCreateAndUpdatePreserveScheduleID(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	membership := &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	existing := protocol.BackupPolicy{
		ID: "existing-policy", ScheduleID: "existing-schedule", PoolID: pool, Name: "Existing", Enabled: true,
		RepositoryID: "repo-1", CaptureMode: protocol.BackupCaptureStopped, TimeZone: "UTC",
		Rule:  protocol.BackupScheduleRule{Frequency: protocol.BackupScheduleDaily, Hour: 3},
		VMIDs: []string{"vm-1"}, Retention: protocol.BackupRetention{KeepLatest: 2}, Revision: 1,
	}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{
			Membership: membership, BackupPoliciesReported: true, BackupPolicies: []protocol.BackupPolicy{existing},
		}},
	})
	body := map[string]any{
		"name": "Policy", "enabled": true, "repository_id": "repo-1", "capture_mode": "stopped",
		"time_zone": "UTC", "rule": map[string]any{"frequency": "daily", "hour": 3},
		"vm_ids": []string{"vm-1"}, "retention": map[string]any{"keep_latest": 2},
	}
	for _, tc := range []struct{ method, path, requestID, wantSchedule string }{
		{"POST", "/api/v1/pools/" + pool + "/backup-policies", "create-policy", "new-policy"},
		{"PUT", "/api/v1/pools/" + pool + "/backup-policies/existing-policy", "update-policy", "existing-schedule"},
	} {
		body["request_id"] = tc.requestID
		if tc.method == "POST" {
			body["id"] = "new-policy"
			delete(body, "expected_revision")
		} else {
			delete(body, "id")
			body["expected_revision"] = 1
		}
		a.call(tc.method, tc.path, body, nil, 202)
		var raw string
		if err := a.c.db.QueryRow("SELECT command FROM managed_resources WHERE request_id=?", tc.requestID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var command protocol.Command
		if err := json.Unmarshal([]byte(raw), &command); err != nil || command.BackupPolicy == nil || command.BackupPolicy.ScheduleID != tc.wantSchedule {
			t.Fatalf("%s schedule: %+v, %v", tc.requestID, command.BackupPolicy, err)
		}
	}
	delete(body, "expected_revision")
	body["request_id"] = "missing-revision"
	a.call("PUT", "/api/v1/pools/"+pool+"/backup-policies/existing-policy", body, nil, 400)
	body["expected_revision"] = 0
	body["request_id"] = "zero-revision"
	a.call("PUT", "/api/v1/pools/"+pool+"/backup-policies/existing-policy", body, nil, 400)
	body["expected_revision"] = 1
	body["request_id"] = "update-policy"
	a.call("PUT", "/api/v1/pools/"+pool+"/backup-policies/other-policy", body, nil, 409)
	if _, err := a.c.db.Exec("UPDATE hosts SET inventory='{}' WHERE id=?", master.HostID); err != nil {
		t.Fatal(err)
	}
	body["request_id"] = "update-policy"
	a.call("PUT", "/api/v1/pools/"+pool+"/backup-policies/existing-policy", body, nil, 200)
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{
			Membership: membership, BackupPoliciesReported: true, BackupPolicies: []protocol.BackupPolicy{existing},
			Outcomes: []protocol.ManagedOutcome{{OperationID: "create-policy", Kind: "backup-policy-upsert", ID: "new-policy", Status: "succeeded"}},
		}},
	})
	var status string
	if err := a.c.db.QueryRow("SELECT status FROM operations WHERE id='create-policy'").Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("policy outcome without direct result: %q, %v", status, err)
	}
}

func TestBackupPolicyOutcomeAdoption(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	for i, status := range []string{"needs_attention", "succeeded"} {
		outcomes := []protocol.ManagedOutcome{{OperationID: "policy-core-1", Kind: "backup-policy-upsert", ID: "policy-1", Status: status}}
		response := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: uint64(i + 1), Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Outcomes: outcomes}}})
		if len(response.Commands) != 0 {
			t.Fatal("adopted policy outcome was dispatched")
		}
		var op protocol.Operation
		if err := a.c.db.QueryRow("SELECT id,kind,request_id,status FROM operations WHERE request_id=?", "adopt-outcome:"+host.HostID+":policy-core-1").Scan(&op.ID, &op.Kind, &op.RequestID, &op.Status); err != nil {
			t.Fatal(err)
		}
		if op.ID != adoptedBackupOperationID(host.HostID, "policy-core-1") || op.Kind != "backup-policy-upsert" || op.Status != status {
			t.Fatalf("policy outcome %+v", op)
		}
	}
}

func TestBackupInventoryRejectsForeignPoolRecords(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	membership := &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	foreign := "other-pool"
	for _, managed := range []*protocol.ManagedState{
		{BackupRepositoriesReported: true, BackupRepositories: []protocol.BackupRepository{{ID: "repo-1", PoolID: foreign, Name: "Other", Kind: "nfs", Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}}}},
		{BackupPoliciesReported: true, BackupPolicies: []protocol.BackupPolicy{{ID: "policy-1", ScheduleID: "schedule-1", PoolID: foreign, Name: "Other", RepositoryID: "repo-1", CaptureMode: "stopped", TimeZone: "UTC", Rule: protocol.BackupScheduleRule{Frequency: "daily"}, VMIDs: []string{"vm-1"}, Retention: protocol.BackupRetention{KeepLatest: 1}, Revision: 1}}},
	} {
		managed.Membership = membership
		a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: &protocol.Inventory{Hostname: "core", Managed: managed}}, certificate(t, master.CertificatePEM), 400)
	}
}

func TestCoreBackupOutcomeAdoptionRequiresExplicitSettlement(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{}}
	for i, status := range []string{"needs_attention", "", "succeeded", "needs_attention"} {
		inv.Managed.Outcomes = nil
		if status != "" {
			inv.Managed.Outcomes = []protocol.ManagedOutcome{{
				OperationID: "backup-1", Kind: "backup-now", ID: "11111111-1111-4111-8111-111111111111", Status: status,
			}}
		}
		response := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: uint64(i + 1), Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
		if len(response.Commands) != 0 {
			t.Fatal("Controller redispatched a Core-owned backup")
		}
		var operations []protocol.Operation
		read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
		want := "needs_attention"
		if i >= 2 {
			want = "succeeded"
		}
		found := false
		for _, operation := range operations {
			if operation.RequestID == "adopt-outcome:"+host.HostID+":backup-1" {
				found = true
				if operation.ID != adoptedBackupOperationID(host.HostID, "backup-1") || operation.Kind != "backup-now" || operation.Status != want {
					t.Fatalf("step %d backup adoption: %#v; want %s", i, operation, want)
				}
			}
		}
		if !found {
			t.Fatal("Core backup history was not adopted")
		}
	}
}

func TestQueuedAdoptedBackupOutcomesAreNotDispatched(t *testing.T) {
	a := newClient(t)
	host := a.enroll(a.token(a.site(), ""), csr(t))
	cert := certificate(t, host.CertificatePEM)
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{Outcomes: []protocol.ManagedOutcome{{
		OperationID: "core-queued-backup", Kind: "backup-now", ID: "11111111-1111-4111-8111-111111111111", Status: "queued",
	}}}}
	response := a.report(cert, protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	if len(response.Commands) != 0 {
		t.Fatalf("adopted queued backup was dispatched: %#v", response.Commands)
	}
	var operations []protocol.Operation
	read(t, a.call("GET", "/api/v1/operations", nil, nil, 200), &operations)
	found := false
	for _, operation := range operations {
		if operation.RequestID == "adopt-outcome:"+host.HostID+":core-queued-backup" {
			found = true
			if operation.Status != "queued" || operation.ID != adoptedBackupOperationID(host.HostID, "core-queued-backup") {
				t.Fatalf("adopted queued backup %#v", operation)
			}
		}
	}
	if !found {
		t.Fatal("queued Core backup was not adopted")
	}
}

func TestBackupAuthorityObservationIsRetainedInInventory(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	membership := &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
	}}
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: pool, Name: "Primary NFS", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
	}
	inv := &protocol.Inventory{Hostname: "core", Managed: &protocol.ManagedState{
		Membership: membership, BackupRepositories: []protocol.BackupRepository{repo}, BackupRepositoriesReported: true,
	}}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability}, Inventory: inv})
	var raw string
	if err := a.c.db.QueryRow("SELECT inventory FROM hosts WHERE id=?", master.HostID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved protocol.Inventory
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Managed == nil || !saved.Managed.BackupRepositoriesReported || len(saved.Managed.BackupRepositories) != 1 || saved.Managed.BackupRepositories[0].ID != "repo-1" {
		t.Fatalf("fresh Controller lost Core backup repository observation: %#v", saved.Managed)
	}
}
