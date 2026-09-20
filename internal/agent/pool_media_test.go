// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func writeAppliedPoolMedia(t *testing.T, s *Store, media protocol.Media) {
	t.Helper()
	writeAppliedPoolMediaAt(t, s, media, "/srv/pool/nex-media/"+media.ID+".iso")
}

// writeAppliedPoolMediaAt journals the same import with a chosen published path, so a test can
// make the native unlink fail. The operation ID is not the shared-adoption one, so the entry is
// an import this host performed and its removal does unlink the file.
func writeAppliedPoolMediaAt(t *testing.T, s *Store, media protocol.Media, path string) {
	t.Helper()
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	media.MemberAccess, media.OperationID, media.Status, media.Error, media.URL = nil, "", "", "", ""
	op := "media-" + media.ID
	journal, err := json.Marshal(map[string]any{
		"command":    protocol.Command{OperationID: op, Kind: "iso-import", Media: &media},
		"result":     protocol.Result{OperationID: op, Status: "succeeded"},
		"media_path": path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, op+".json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
}

// The shared-ISO adoption path has neither half of the pool VLAN apply defect, and this holds
// it there. AdoptSharedMedia verifies the published file on every pass and journals only on
// success, so a member that cannot adopt yet mints no journal entry at all, re-evaluates the
// cause on the next probe tick and reports a freshly observed error to the master. Only the
// media *removal* loop uses an hour-pinned operation ID, and it is gated by CheckMediaRemoval
// exactly as the network removal loop is.
func TestPoolMediaAdoptionFailureIsReEvaluatedEveryTickAndJournalsNothing(t *testing.T) {
	member := peerStore(t, "member-1", "pool-1")
	storage := protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: member.State.HostID, Name: "shared", Path: t.TempDir(),
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1,
	}
	media := protocol.Media{
		ID: "iso-1", PoolID: "pool-1", HostID: member.State.HostID, StorageID: storage.ID,
		Name: "debian.iso", SizeBytes: 40000, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	// The repository has not been registered on this member yet: the adoption is refused, and
	// nothing is written, so the cause is re-read rather than replayed on the next pass.
	for pass := 0; pass < 3; pass++ {
		if err := adoptPoolMediaNative(context.Background(), member, media); err == nil {
			t.Fatalf("pass %d adopted an ISO on an unregistered repository", pass)
		}
		if n := poolJournalEntries(t, member); n != 0 {
			t.Fatalf("pass %d: a refused adoption journalled %d entries", pass, n)
		}
	}
	// The operator registers the repository. The next pass gets past the repository and fails
	// on the ISO itself, so the reported error tracks the current cause without waiting an hour.
	writeAppliedPoolStorage(t, member, storage)
	err := adoptPoolMediaNative(context.Background(), member, media)
	if err == nil || !strings.Contains(err.Error(), "ISO") {
		t.Fatalf("adoption error after the repository was registered = %v", err)
	}
	if n := poolJournalEntries(t, member); n != 1 {
		t.Fatalf("a failing adoption journalled a media entry: %d entries", n)
	}
}

func TestPoolMediaPeerListAndMemberReconcile(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	storage := protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", HostID: master.State.HostID, Name: "shared", Path: t.TempDir(),
		Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1,
	}
	writeAppliedPoolStorage(t, master, storage)
	media := protocol.Media{
		ID: "iso-1", PoolID: "pool-1", HostID: master.State.HostID, StorageID: storage.ID,
		Name: "debian.iso", SizeBytes: 40000, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	localMedia := protocol.Media{
		ID: "local-iso", PoolID: "pool-1", HostID: master.State.HostID, StorageID: "local-repo",
		Name: "local.iso", SizeBytes: 40000, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	writeAppliedPoolMedia(t, master, media)
	writeAppliedPoolMedia(t, master, localMedia)

	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := PoolPeerClient(member, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)

	resp, err := client.Get(server.URL + "/api/v1/pool/media")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var listed []protocol.Media
	if err = json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != media.ID || listed[0].HostID != member.State.HostID {
		t.Fatalf("peer media list = %#v", listed)
	}

	adopted := false
	previous := adoptPoolMedia
	t.Cleanup(func() { adoptPoolMedia = previous })
	adoptPoolMedia = func(_ context.Context, s *Store, m protocol.Media) error {
		adopted = true
		writeAppliedPoolMedia(t, s, m)
		return nil
	}
	writeAppliedPoolStorage(t, member, storage)
	if err = reconcilePoolMediaFromMaster(context.Background(), member, time.Second); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !adopted {
		t.Fatal("member did not adopt shared media")
	}
	reported := PoolMediaMemberAccess(master, []protocol.Media{media, localMedia}, []protocol.StorageRegistration{storage})
	if len(reported[0].MemberAccess) != 1 || reported[0].MemberAccess[0].HostID != member.State.HostID || reported[0].MemberAccess[0].SHA256 != media.SHA256 {
		t.Fatalf("member media access = %#v", reported[0].MemberAccess)
	}
	if len(reported[1].MemberAccess) != 0 {
		t.Fatalf("local media carried member access: %#v", reported[1].MemberAccess)
	}
}
