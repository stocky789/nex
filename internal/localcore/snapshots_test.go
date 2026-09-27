// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"net/http"
	"testing"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

func snapshotIngressChannel(t *testing.T, s *Service) <-chan agent.SnapshotRequest {
	t.Helper()
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	if err != nil {
		handoffAgents.Unlock()
		t.Fatal(err)
	}
	a.hostID, a.started, a.done = "host-0001", true, make(chan struct{})
	a.snapshotRequests = make(chan agent.SnapshotRequest)
	requests := a.snapshotRequests
	handoffAgents.Unlock()
	t.Cleanup(func() {
		handoffAgents.Lock()
		a.started = false
		handoffAgents.Unlock()
	})
	return requests
}

func TestPoolSnapshotRoutesRequireSessionAndCSRF(t *testing.T) {
	s, _ := testService(t)
	if w := callLocal(s, http.MethodGet, "/api/local/v1/pool/vms/vm-12345/snapshots", nil, "", "", "https://core.test"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated catalog = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/vm-12345/snapshots/snapshot-001/revert", map[string]string{"operation_id": "revert-001"}, "", "", "https://core.test"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated revert = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/vm-12345/snapshots/snapshot-001/delete", map[string]string{"operation_id": "delete-001"}, "", "", "https://core.test"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated delete = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPatch, "/api/local/v1/pool/vms/vm-12345/snapshots/snapshot-001", map[string]string{"name": "renamed"}, "", "", "https://core.test"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metadata = %d", w.Code)
	}
	cookie, csrf := loginLocal(t, s)
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/vm-12345/snapshots", map[string]string{"operation_id": "snapshot-001", "name": "before-upgrade"}, cookie, "", "https://core.test"); w.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF token = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/vm-12345/snapshots/snapshot-001/revert", map[string]string{"operation_id": "revert-001"}, cookie, "", "https://core.test"); w.Code != http.StatusForbidden {
		t.Fatalf("revert missing CSRF token = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/vm-12345/snapshots/snapshot-001/delete", map[string]string{"operation_id": "delete-001"}, cookie, "", "https://core.test"); w.Code != http.StatusForbidden {
		t.Fatalf("delete missing CSRF token = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPatch, "/api/local/v1/pool/vms/vm-12345/snapshots/snapshot-001", map[string]string{"name": "renamed"}, cookie, "", "https://core.test"); w.Code != http.StatusForbidden {
		t.Fatalf("metadata missing CSRF token = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/vm-12345/snapshots", map[string]string{"operation_id": "snapshot-001", "name": "before-upgrade"}, cookie, csrf, "https://evil.test"); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin create = %d", w.Code)
	}
}

func TestPoolSnapshotRoutesRejectInvalidIDsAndUnknownInput(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	if w := callLocal(s, http.MethodGet, "/api/local/v1/pool/vms/bad/snapshots", nil, cookie, csrf, "https://core.test"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid VM ID = %d", w.Code)
	}
	if w := callLocal(s, http.MethodGet, "/api/local/v1/pool/vms/vm-12345/snapshot-operations/bad$id", nil, cookie, csrf, "https://core.test"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid operation ID = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/vm-12345/snapshots", map[string]string{"operation_id": "snapshot-001", "name": "before-upgrade", "host_id": "caller-selected"}, cookie, csrf, "https://core.test"); w.Code != http.StatusBadRequest {
		t.Fatalf("caller supplied ownership input = %d", w.Code)
	}
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/vm-12345/snapshots", map[string]string{"operation_id": "bad/id", "name": "before-upgrade"}, cookie, csrf, "https://core.test"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid operation ID = %d", w.Code)
	}
	base := "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/snapshot-001/revert"
	for _, body := range []map[string]string{
		{"operation_id": "bad/id"},
		{"operation_id": "revert-001", "host_id": "caller-selected"},
	} {
		if w := callLocal(s, http.MethodPost, base, body, cookie, csrf, "https://core.test"); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid revert input %v = %d %s", body, w.Code, w.Body)
		}
	}
	if w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/bad$id/revert", map[string]string{"operation_id": "revert-001"}, cookie, csrf, "https://core.test"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid target snapshot ID = %d", w.Code)
	}
	deleteBase := "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/snapshot-001/delete"
	for _, body := range []map[string]string{
		{"operation_id": "bad/id"},
		{"operation_id": "delete-001", "host_id": "caller-selected"},
	} {
		if w := callLocal(s, http.MethodPost, deleteBase, body, cookie, csrf, "https://core.test"); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid delete input %v = %d %s", body, w.Code, w.Body)
		}
	}
}

func TestPoolSnapshotRevertUsesDistinctOperationAndTargetIDs(t *testing.T) {
	s, _ := testService(t)
	requests := snapshotIngressChannel(t, s)
	cookie, csrf := loginLocal(t, s)
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := <-requests
		if request.VMID != "11111111-1111-4111-8111-111111111111" || request.Command == nil ||
			request.Command.Kind != "snapshot-revert" || request.Command.OperationID != "revert-001" ||
			request.Command.Snapshot == nil || request.Command.Snapshot.SnapshotID != "snapshot-001" || request.Command.Snapshot.Target != nil {
			t.Errorf("revert request = %#v", request)
		}
		request.Reply <- agent.SnapshotResponse{Result: &protocol.Result{OperationID: "revert-001", Status: "pending"}}
	}()
	w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/snapshot-001/revert", map[string]string{"operation_id": "revert-001"}, cookie, csrf, "https://core.test")
	<-done
	if w.Code != http.StatusAccepted {
		t.Fatalf("revert = %d %s", w.Code, w.Body)
	}
}

func TestPoolSnapshotDeleteUsesDistinctOperationAndTargetIDs(t *testing.T) {
	s, _ := testService(t)
	requests := snapshotIngressChannel(t, s)
	cookie, csrf := loginLocal(t, s)
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := <-requests
		if request.VMID != "11111111-1111-4111-8111-111111111111" || request.Command == nil ||
			request.Command.Kind != "snapshot-delete" || request.Command.OperationID != "delete-001" ||
			request.Command.Snapshot == nil || request.Command.Snapshot.SnapshotID != "snapshot-001" || request.Command.Snapshot.Target != nil {
			t.Errorf("delete request = %#v", request)
		}
		request.Reply <- agent.SnapshotResponse{Result: &protocol.Result{OperationID: "delete-001", Status: "pending"}}
	}()
	w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/snapshot-001/delete", map[string]string{"operation_id": "delete-001"}, cookie, csrf, "https://core.test")
	<-done
	if w.Code != http.StatusAccepted {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
}

func TestPoolSnapshotCreateRequiresEnrollment(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots", map[string]string{"operation_id": "snapshot-001", "name": "before-upgrade"}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusConflict {
		t.Fatalf("un-enrolled Core create = %d %s", w.Code, w.Body)
	}
	w = callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/snapshot-001/revert", map[string]string{"operation_id": "revert-001"}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusConflict {
		t.Fatalf("un-enrolled Core revert = %d %s", w.Code, w.Body)
	}
	w = callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/snapshot-001/delete", map[string]string{"operation_id": "delete-001"}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusConflict {
		t.Fatalf("un-enrolled Core delete = %d %s", w.Code, w.Body)
	}
}

func TestPoolSnapshotRoutesUseScopedRunRequests(t *testing.T) {
	s, _ := testService(t)
	requests := snapshotIngressChannel(t, s)
	cookie, csrf := loginLocal(t, s)
	go func() {
		for range 3 {
			request := <-requests
			if request.VMID != "11111111-1111-4111-8111-111111111111" {
				t.Errorf("request VM scope = %q", request.VMID)
			}
			switch {
			case request.Command != nil:
				if request.Command.Kind != "snapshot-create" || request.Command.OperationID != "snapshot-001" || request.Command.Snapshot == nil || request.Command.Snapshot.Name != "before-upgrade" || request.Command.VM != nil {
					t.Errorf("create request = %#v", request.Command)
				}
			case request.OperationID != "":
				if request.OperationID != "snapshot-001" {
					t.Errorf("operation scope = %q", request.OperationID)
				}
			default:
				if request.Command != nil {
					t.Errorf("catalog request carried a command: %#v", request.Command)
				}
			}
			response := agent.SnapshotResponse{Result: &protocol.Result{OperationID: "snapshot-001", Status: "pending"}}
			if request.Command == nil && request.OperationID == "" {
				response.Result = nil
			}
			request.Reply <- response
		}
	}()
	base := "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111"
	if w := callLocal(s, http.MethodPost, base+"/snapshots", map[string]string{"operation_id": "snapshot-001", "name": "before-upgrade"}, cookie, csrf, "https://core.test"); w.Code != http.StatusAccepted {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	if w := callLocal(s, http.MethodGet, base+"/snapshot-operations/snapshot-001", nil, cookie, csrf, "https://core.test"); w.Code != http.StatusOK {
		t.Fatalf("operation lookup = %d %s", w.Code, w.Body)
	}
	if w := callLocal(s, http.MethodGet, base+"/snapshots", nil, cookie, csrf, "https://core.test"); w.Code != http.StatusOK || w.Body.String() != "[]\n" {
		t.Fatalf("empty catalog = %d %s", w.Code, w.Body)
	}
}

func TestPoolSnapshotRoutesMapConflictsAndBoundDisconnectedReplies(t *testing.T) {
	s, _ := testService(t)
	requests := snapshotIngressChannel(t, s)
	cookie, csrf := loginLocal(t, s)
	go func() {
		for _, err := range []error{agent.ErrSnapshotConflict, agent.ErrSnapshotNotFound, agent.ErrSnapshotTargetNotFound} {
			request := <-requests
			request.Reply <- agent.SnapshotResponse{Err: err}
		}
	}()
	w := callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots", map[string]string{"operation_id": "snapshot-001", "name": "before-upgrade"}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusConflict {
		t.Fatalf("conflict = %d %s", w.Code, w.Body)
	}
	w = callLocal(s, http.MethodGet, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshot-operations/snapshot-001", nil, cookie, csrf, "https://core.test")
	if w.Code != http.StatusNotFound {
		t.Fatalf("scoped missing operation = %d %s", w.Code, w.Body)
	}
	w = callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/snapshot-missing/revert", map[string]string{"operation_id": "revert-001"}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing revert target = %d %s", w.Code, w.Body)
	}
	w = callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/snapshot-001/revert", map[string]string{"operation_id": "snapshot-001"}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("operation_id equal to snapshot_id = %d %s", w.Code, w.Body)
	}
	w = callLocal(s, http.MethodPost, "/api/local/v1/pool/vms/11111111-1111-4111-8111-111111111111/snapshots/snapshot-001/delete", map[string]string{"operation_id": "snapshot-001"}, cookie, csrf, "https://core.test")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("delete operation_id equal to snapshot_id = %d %s", w.Code, w.Body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	requestDone := make(chan agent.SnapshotRequest, 1)
	go func() {
		requestDone <- <-requests
	}()
	done := make(chan error, 1)
	go func() {
		_, err := s.requestSnapshot(ctx, agent.SnapshotRequest{VMID: "11111111-1111-4111-8111-111111111111"})
		done <- err
	}()
	request := <-requestDone
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("disconnected request = %v", err)
	}
	select {
	case request.Reply <- agent.SnapshotResponse{}:
	default:
		t.Fatal("Run reply channel would block after the caller disconnected")
	}
}
