// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"encoding/json"
	"net/http"
	"testing"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

func TestBackupMetadataReceiptReplay(t *testing.T) {
	s, _ := testService(t)
	requests := backupIngressChannel(t, s)
	cookie, csrf := loginLocal(t, s)
	for _, tc := range []struct {
		kind string
		path string
		body map[string]any
	}{
		{"backup-repository-register", "/api/local/v1/pool/backup-repositories", map[string]any{"repository": map[string]any{"id": "repo-1", "name": "Primary", "kind": "nfs", "source": map[string]string{"server": "nfs.example.test", "export": "/export/backups"}}}},
		{"backup-repository-test", "/api/local/v1/pool/backup-repositories/repo-1/test", nil},
		{"backup-policy-upsert", "/api/local/v1/pool/backup-policies", map[string]any{"policy": map[string]any{"id": "pol-1", "schedule_id": "sch-1", "name": "Daily", "enabled": true, "paused": true, "repository_id": "repo-1", "capture_mode": "stopped", "time_zone": "UTC", "rule": map[string]any{"frequency": "daily", "hour": 3, "minute": 15}, "vm_ids": []string{"6199dc4e-dd2a-45c2-a5d6-9c9f76304b43"}, "retention": map[string]any{"keep_latest": 3}, "revision": 1}}},
		{"backup-policy-pause", "/api/local/v1/pool/backup-policies/pol-1/pause", map[string]any{"paused": true}},
		{"backup-protect", "/api/local/v1/pool/backup-protect", map[string]any{"repository_id": "repo-1", "recovery_point_id": "point-1", "protected": true}},
		{"backup-test-verify", "/api/local/v1/pool/backup-test-verify", map[string]any{"repository_id": "repo-1", "recovery_point_id": "point-1", "test_vm_id": "11111111-1111-4111-8111-111111111111", "result": "passed"}},
		{"backup-catalog-import", "/api/local/v1/pool/backup-catalog/import", map[string]any{"repository_id": "repo-1"}},
		{"backup-catalog-rebuild", "/api/local/v1/pool/backup-catalog/rebuild", map[string]any{"repository_id": "repo-1"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			operationID := "receipt-" + tc.kind
			body := map[string]any{"operation_id": operationID}
			for key, value := range tc.body {
				body[key] = value
			}
			seen := make(chan string, 1)
			go func() {
				request := <-requests
				seen <- request.Command.Kind
				request.Reply <- agent.BackupResponse{Result: &protocol.Result{OperationID: operationID, Status: "succeeded"}}
			}()
			w := callLocal(s, http.MethodPost, tc.path, body, cookie, csrf, "https://core.test")
			if w.Code != http.StatusOK {
				t.Fatalf("receipt replay: %d %s", w.Code, w.Body.String())
			}
			if kind := <-seen; kind != tc.kind {
				t.Fatalf("command kind = %q", kind)
			}
			var result protocol.Result
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.OperationID != operationID || result.Status != "succeeded" {
				t.Fatalf("receipt replay response: %s: %v", w.Body.String(), err)
			}
		})
	}
}

func TestBackupCatalogPendingResponseAccepted(t *testing.T) {
	s, _ := testService(t)
	requests := backupIngressChannel(t, s)
	cookie, csrf := loginLocal(t, s)
	for _, path := range []string{"/api/local/v1/pool/backup-catalog/import", "/api/local/v1/pool/backup-catalog/rebuild"} {
		go func() {
			request := <-requests
			request.Reply <- agent.BackupResponse{Result: &protocol.Result{OperationID: request.Command.OperationID, Status: "pending"}}
		}()
		w := callLocal(s, http.MethodPost, path, map[string]any{"operation_id": "catalog-pending", "repository_id": "repo-1"}, cookie, csrf, "https://core.test")
		if w.Code != http.StatusAccepted {
			t.Fatalf("pending catalog response: %d %s", w.Code, w.Body.String())
		}
	}
}

func backupIngressChannel(t *testing.T, s *Service) <-chan agent.BackupRequest {
	t.Helper()
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	if err != nil {
		handoffAgents.Unlock()
		t.Fatal(err)
	}
	a.hostID, a.started, a.done = "host-0001", true, make(chan struct{})
	a.backupRequests = make(chan agent.BackupRequest)
	requests := a.backupRequests
	handoffAgents.Unlock()
	t.Cleanup(func() {
		handoffAgents.Lock()
		a.started = false
		handoffAgents.Unlock()
	})
	return requests
}

func TestBackupRoutesRequireSessionAndCSRF(t *testing.T) {
	s, _ := testService(t)
	if w := callLocal(s, http.MethodGet, "/api/local/v1/pool/backup-repositories", nil, "", "", "https://core.test"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list = %d", w.Code)
	}
	cookie, csrf := loginLocal(t, s)
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/backup-repositories", map[string]any{
		"operation_id": "repo-op-1",
		"repository": map[string]any{
			"id": "repo-1", "name": "Primary", "kind": "nfs",
			"source": map[string]string{"server": "nfs.example.test", "export": "/export/backups"},
		},
	}, cookie, "", "https://core.test"); w.Code != http.StatusForbidden {
		t.Fatalf("register without csrf = %d", w.Code)
	}
	_ = csrf
}

func TestBackupNowRouteAcceptsExactShape(t *testing.T) {
	s, _ := testService(t)
	requests := backupIngressChannel(t, s)
	cookie, csrf := loginLocal(t, s)
	seen := make(chan string, 1)
	go func() {
		req := <-requests
		seen <- req.Command.VM.HostID
		req.Reply <- agent.BackupResponse{Result: &protocol.Result{OperationID: "backup-1", Status: "pending"}}
	}()
	w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/backup", map[string]string{
		"operation_id": "backup-1", "repository_id": "repo-1",
	}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusAccepted {
		t.Fatalf("backup-now: %d %s", w.Code, w.Body.String())
	}
	if host := <-seen; host != "host-0001" {
		t.Fatalf("backup target host = %q", host)
	}
}

func TestLocalRestoreRoutesTargetEnrolledHost(t *testing.T) {
	s, _ := testService(t)
	requests := backupIngressChannel(t, s)
	cookie, csrf := loginLocal(t, s)
	for _, tc := range []struct {
		path string
		body map[string]any
		kind string
	}{
		{"/api/local/v1/pool/backup-restore", map[string]any{"operation_id": "restore-new", "repository_id": "repo-1", "recovery_point_id": "point-1", "new_vm_id": "11111111-1111-4111-8111-111111111111", "new_vm_name": "Restored", "storage_id": "storage-1"}, "backup-restore-as-new"},
		{"/api/local/v1/pool/backup-restore-replace", map[string]any{"operation_id": "restore-replace", "repository_id": "repo-1", "recovery_point_id": "point-1", "original_vm_id": "11111111-1111-4111-8111-111111111111", "storage_id": "storage-1", "confirm_replace": true}, protocol.BackupKindRestoreReplace},
		{"/api/local/v1/pool/backup-test-restore", map[string]any{"operation_id": "test-restore", "repository_id": "repo-1", "recovery_point_id": "point-1", "new_vm_id": "22222222-2222-4222-8222-222222222222", "new_vm_name": "Test restored", "storage_id": "storage-1"}, protocol.BackupKindTestRestore},
	} {
		seen := make(chan protocol.Command, 1)
		go func() {
			req := <-requests
			seen <- *req.Command
			req.Reply <- agent.BackupResponse{Result: &protocol.Result{OperationID: req.Command.OperationID, Status: "pending"}}
		}()
		w := callLocal(s, http.MethodPost, tc.path, tc.body, cookie, csrf, "https://core.test")
		if w.Code != http.StatusAccepted {
			t.Fatalf("%s: %d %s", tc.kind, w.Code, w.Body.String())
		}
		command := <-seen
		var host string
		switch tc.kind {
		case "backup-restore-as-new":
			host = command.Restore.HostID
		case protocol.BackupKindRestoreReplace:
			host = command.RestoreReplace.HostID
		case protocol.BackupKindTestRestore:
			host = command.TestRestore.HostID
		}
		if host != "host-0001" {
			t.Fatalf("%s target host = %q", tc.kind, host)
		}
	}
}

func TestBackupPolicyUpsertAcceptsOmittedPoolID(t *testing.T) {
	// Phase 6 product lab: Core-local upsert without pool_id returned 400 invalid
	// policy shape. pool_id is derived from enrollment like backup repositories.
	s, _ := testService(t)
	requests := backupIngressChannel(t, s)
	cookie, csrf := loginLocal(t, s)
	seen := make(chan protocol.BackupPolicy, 1)
	go func() {
		req := <-requests
		pol := protocol.BackupPolicy{}
		if req.Command != nil && req.Command.BackupPolicy != nil {
			pol = *req.Command.BackupPolicy
		}
		seen <- pol
		if pol.ID == "" {
			req.Reply <- agent.BackupResponse{Err: agent.ErrBackupInvalid}
			return
		}
		pol.PoolID = "pool-from-enrollment"
		req.Reply <- agent.BackupResponse{
			Result: &protocol.Result{OperationID: "polu-lab1", Status: "succeeded"},
			Policy: &pol,
		}
	}()
	w := callLocal(s, http.MethodPost, "/api/local/v1/pool/backup-policies", map[string]any{
		"operation_id": "polu-lab1",
		"policy": map[string]any{
			"id": "pol-lab1", "schedule_id": "sch-lab1", "name": "phase6-lab-policy",
			"enabled": true, "paused": true, "repository_id": "repo-lab1",
			"capture_mode": "stopped", "time_zone": "UTC",
			"rule":      map[string]any{"frequency": "daily", "hour": 3, "minute": 15},
			"vm_ids":    []string{"6199dc4e-dd2a-45c2-a5d6-9c9f76304b43"},
			"retention": map[string]any{"keep_latest": 3}, "revision": 1,
		},
	}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusOK {
		t.Fatalf("policy upsert without pool_id: %d %s", w.Code, w.Body.String())
	}
	pol := <-seen
	if pol.PoolID != "" {
		t.Fatalf("request must omit pool_id; got %q", pol.PoolID)
	}
	if err := pol.Valid(); err != nil {
		t.Fatalf("lab policy shape must validate: %v", err)
	}
}

func TestBackupRestoreRejectsUnsafeVMID(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	w := callLocal(s, http.MethodPost, "/api/local/v1/pool/backup-restore", map[string]string{
		"operation_id": "restore-unsafe", "repository_id": "repo-1", "recovery_point_id": "point-1",
		"new_vm_id": "../../outside", "new_vm_name": "Restored", "storage_id": "storage-1",
	}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe restore ID: %d %s", w.Code, w.Body.String())
	}
}

func TestOrphanedBackupRoutes(t *testing.T) {
	s, _ := testService(t)
	requests := backupIngressChannel(t, s)
	base := "/api/local/v1/pool/backup-repositories/repo-1/"
	body := map[string]any{"operation_id": "orphans-1", "vm_ids": []string{"vm-gone"}, "confirmation": "delete 2 recovery points", "recovery_points": 2}
	for _, path := range []string{base + "orphans", "/api/local/v1/pool/backup-orphan-cleanups/orphans-1"} {
		if w := callLocal(s, http.MethodGet, path, nil, "", "", "https://core.test"); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s = %d", path, w.Code)
		}
	}
	cookie, csrf := loginLocal(t, s)
	if w := callLocal(s, http.MethodPost, base+"orphan-cleanups", body, cookie, "", "https://core.test"); w.Code != http.StatusForbidden {
		t.Fatalf("cleanup without csrf = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPost, base+"orphan-cleanups", map[string]any{"operation_id": "orphans-1", "recovery_points": 2}, cookie, csrf, "https://core.test"); w.Code != http.StatusBadRequest {
		t.Fatalf("cleanup without VMs = %d", w.Code)
	}
	seen := make(chan agent.BackupRequest, 3)
	go func() {
		for i := 0; i < 3; i++ {
			request := <-requests
			seen <- request
			request.Reply <- agent.BackupResponse{Result: &protocol.Result{OperationID: "orphans-1", Status: "succeeded"},
				Orphans: &protocol.BackupOrphanQuery{RepositoryID: "repo-1"}, Cleanup: &protocol.BackupOrphanCleanup{OperationID: "orphans-1", Status: "succeeded"}}
		}
	}()
	if w := callLocal(s, http.MethodPost, base+"orphan-cleanups", body, cookie, csrf, "https://core.test"); w.Code != http.StatusOK {
		t.Fatalf("cleanup = %d %s", w.Code, w.Body.String())
	}
	if request := <-seen; request.Command.Kind != "backup-orphan-cleanup" || request.Command.OrphanCleanup.RepositoryID != "repo-1" || request.Command.OrphanCleanup.VMIDs[0] != "vm-gone" {
		t.Fatalf("cleanup command %+v", request.Command)
	}
	if w := callLocal(s, http.MethodGet, base+"orphans", nil, cookie, "", "https://core.test"); w.Code != http.StatusOK {
		t.Fatalf("orphans = %d %s", w.Code, w.Body.String())
	}
	if request := <-seen; request.List != "orphans" || request.RepositoryID != "repo-1" {
		t.Fatalf("orphans request %+v", request)
	}
	if w := callLocal(s, http.MethodGet, "/api/local/v1/pool/backup-orphan-cleanups/orphans-1", nil, cookie, "", "https://core.test"); w.Code != http.StatusOK {
		t.Fatalf("cleanup result = %d %s", w.Code, w.Body.String())
	}
	if request := <-seen; request.List != "orphan-cleanup" || request.OperationID != "orphans-1" {
		t.Fatalf("result request %+v", request)
	}
}
