// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// Leave a completely uploaded newer root without accepting its final replica.
func prepareBackupAuthorityReplacement(t *testing.T, lab *transferLab) (*poolReplicaRecord, protocol.PoolAuthorityReplica) {
	t.Helper()
	master, member := lab.host("master-1"), lab.host("member-1")
	seedBackupRunFiles(t, master, 1)
	pushReplicas(t, master)
	old, err := loadPoolReplica(member)
	if err != nil || old == nil || old.Replica.BackupManifest == nil {
		t.Fatalf("initial replica = %#v, %v", old, err)
	}
	posts := recordPagePosts(lab)
	record := lab.intercept.Load().(func(string, *http.Request))
	lab.intercept.Store(func(hostID string, r *http.Request) {
		record(hostID, r)
		// The lab's drop matcher uses substrings; refusing /replica up front
		// would also refuse /replica/backup-page before any pages arrive.
		if hostID == "member-1" && r.Method == http.MethodPost && r.URL.Path == poolReplicaPath {
			lab.fail[hostID].Store("POST:!" + poolReplicaPath)
		}
	})
	seedBackupRunFiles(t, master, 2)
	if err := PushPoolAuthorityReplicas(context.Background(), master, 5*time.Second); err == nil {
		t.Fatal("interrupted final replica POST unexpectedly succeeded")
	}
	lab.drop("member-1", "")
	lab.intercept.Store(record)
	posts.mu.Lock()
	defer posts.mu.Unlock()
	if len(posts.opens) != 1 {
		t.Fatalf("expected one replacement push session, got %d", len(posts.opens))
	}
	var upload poolBackupPageUpload
	if err := json.Unmarshal(posts.opens[0], &upload); err != nil || upload.Replica == nil {
		t.Fatalf("replacement session = %#v, %v", upload, err)
	}
	newer := *upload.Replica
	if newer.Generation <= old.Replica.Generation || newer.BackupManifest.SHA256 == old.Replica.BackupManifest.SHA256 {
		t.Fatal("replacement did not advance the signed authority")
	}
	if err := walkPoolBackupAuthorityPages(member, newer.BackupManifest, nil); err != nil {
		t.Fatalf("replacement pages were not uploaded: %v", err)
	}
	return old, newer
}

func TestBackupAuthorityCollectionKeepsLiveUploadRoot(t *testing.T) {
	lab := promotionLab(t, 1)
	old, newer := prepareBackupAuthorityReplacement(t, lab)
	member := lab.host("member-1")
	if err := gcPoolBackupPages(member, old.Replica.BackupManifest.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := walkPoolBackupAuthorityPages(member, newer.BackupManifest, nil); err != nil {
		t.Fatalf("an older handler collected the live upload: %v", err)
	}
	poolBackupPushSessionMu.Lock()
	session := poolBackupPushSessions[member.dir]
	session.expires = time.Now().Add(-time.Second)
	poolBackupPushSessions[member.dir] = session
	poolBackupPushSessionMu.Unlock()
	if err := gcPoolBackupPages(member, old.Replica.BackupManifest.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(poolBackupPageFile(member, newer.BackupManifest.SHA256, 0)); !os.IsNotExist(err) {
		t.Fatalf("an expired unacknowledged upload was retained: %v", err)
	}
	stored, err := loadPoolReplica(member)
	if err != nil || stored == nil || !stored.Verified ||
		stored.Replica.Generation != old.Replica.Generation || stored.Digest != old.Digest {
		t.Fatalf("collection damaged the acknowledged replica: %#v, %v", stored, err)
	}
}

func TestBackupAuthorityCollectionSerializesWithReplicaReplacement(t *testing.T) {
	lab := promotionLab(t, 1)
	old, newer := prepareBackupAuthorityReplacement(t, lab)
	member := lab.host("member-1")
	obsolete := filepath.Join(member.dir, poolBackupPageDir, "000-obsolete")
	if err := os.MkdirAll(obsolete, 0700); err != nil {
		t.Fatal(err)
	}
	paused, resume := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(resume) }) }
	previous := removePoolBackupPageRoot
	removePoolBackupPageRoot = func(path string) error {
		if path == obsolete {
			close(paused)
			<-resume
		}
		return previous(path)
	}
	collected := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		collected <- gcPoolBackupPages(member, old.Replica.BackupManifest.SHA256)
		close(finished)
	}()
	defer func() {
		release()
		<-finished
		removePoolBackupPageRoot = previous
	}()
	select {
	case <-paused:
	case <-time.After(time.Minute):
		t.Fatal("old collector did not reach its paused removal")
	}
	master, ok := poolMemberByID(peerState(t, member).Membership, "master-1")
	if !ok {
		t.Fatal("current master was not in member membership")
	}
	started, accepted := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := acceptPoolAuthorityReplica(member, master, newer)
		accepted <- err
	}()
	<-started
	var early error
	didAccept := false
	select {
	case early = <-accepted:
		didAccept = true
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-collected; err != nil {
		t.Fatal(err)
	}
	if didAccept {
		t.Fatalf("new replica finished while an old collector could still delete its pages: %v", early)
	}
	if err := <-accepted; err != nil {
		t.Fatalf("replacement after collection was refused: %v", err)
	}
	stored, err := loadPoolReplica(member)
	if err != nil || stored == nil || stored.Replica.Generation != newer.Generation || !stored.Verified {
		t.Fatalf("acknowledged replacement was not intact: %#v, %v", stored, err)
	}
	if err := gcPoolBackupPages(member, old.Replica.BackupManifest.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := walkPoolBackupAuthorityPages(member, newer.BackupManifest, nil); err != nil {
		t.Fatalf("late old-handler collection removed acknowledged pages: %v", err)
	}
}
