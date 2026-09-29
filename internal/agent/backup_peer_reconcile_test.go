// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestPeerBackupReconciliationStopsOnMissingExactOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != poolBackupPeerPath+"/old-restore" {
			t.Errorf("unexpected peer request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, "backup operation not found", http.StatusNotFound)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	work := pendingPeerBackup{
		run: protocol.BackupRun{OperationID: "old-restore"},
		member: protocol.PoolMember{PeerOrigin: server.URL}, client: server.Client(),
	}
	completion := waitPeerBackupReconciled(ctx, work)
	var permanent permanentPeerBackupError
	if !errors.As(completion.err, &permanent) || completion.run.OperationID != work.run.OperationID {
		t.Fatalf("missing exact operation must stop reconciliation without replay: %+v", completion)
	}
}

func TestPeerBackupMissingOperationReturnsNotFound(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	queue := make(chan BackupRequest)
	peerBackupRequests.Store(member, queue)
	defer peerBackupRequests.Delete(member)
	go func() {
		request := <-queue
		request.Reply <- acceptBackupRequest(context.Background(), member, request)
	}()
	client, err := poolTransferClient(master, lab.origin[member.State.HostID], member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	request, err := http.NewRequest(http.MethodGet, lab.origin[member.State.HostID]+poolBackupPeerPath+"/old-restore", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, status, err := doPeerBackup(client, request)
	if status != http.StatusNotFound || err == nil {
		t.Fatalf("authenticated missing operation: status=%d err=%v", status, err)
	}
}
