// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestPoolAuthorityPushNumbersBackupSnapshotBeforeNextCommit(t *testing.T) {
	s := seedBackupMaster(t)
	previous := advancePoolAuthorityGenerationForPush
	entered := make(chan string, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unpause := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() {
		unpause()
		advancePoolAuthorityGenerationForPush = previous
	}()
	advancePoolAuthorityGenerationForPush = func(store *Store, digest string) (uint64, error) {
		entered <- digest
		<-release
		return previous(store, digest)
	}
	pushDone := make(chan error, 1)
	go func() { pushDone <- PushPoolAuthorityReplicas(context.Background(), s, time.Second) }()
	var snapshotDigest string
	select {
	case snapshotDigest = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("authority push did not reach generation advance")
	}
	// The push must hold the backup authority lock until the snapshot is numbered.
	if poolBackupAuthorityMu.TryLock() {
		poolBackupAuthorityMu.Unlock()
		t.Fatal("backup metadata could commit before snapshot generation")
	}
	unpause()
	if err := <-pushDone; err != nil {
		t.Fatal(err)
	}
	poolBackupAuthorityMu.Lock()
	err := savePoolBackupMetadataReceipt(s, protocol.BackupMetadataReceipt{
		OperationID: "receipt-after-snapshot", Kind: "backup-policy-upsert",
		IntentSHA256: fmt.Sprintf("%064x", 1), Status: "succeeded", ObservedAt: time.Now().UTC(),
	})
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	_, numberedDigest, err := PoolAuthorityGeneration(s)
	if err != nil || numberedDigest != snapshotDigest {
		t.Fatalf("numbered digest = %q, snapshot digest = %q: %v", numberedDigest, snapshotDigest, err)
	}
	_, currentDigest, err := buildPoolAuthorityReplica(s)
	if err != nil || currentDigest == snapshotDigest {
		t.Fatalf("backup commit did not change the authority digest: %v", err)
	}
}

// Storage-lease preparation can hang on a mount that ctx cannot interrupt, so it must run
// before the backup authority lock is taken.
func TestPoolAuthorityPushPreparesStorageLeaseOutsideBackupLock(t *testing.T) {
	s := seedBackupMaster(t)
	previous := preparePoolStorageLeaseForPush
	t.Cleanup(func() { preparePoolStorageLeaseForPush = previous })
	prepared := false
	preparePoolStorageLeaseForPush = func(context.Context, *Store) error {
		prepared = true
		if !poolBackupAuthorityMu.TryLock() {
			return errors.New("storage lease preparation ran under the backup authority lock")
		}
		poolBackupAuthorityMu.Unlock()
		return nil
	}
	if err := PushPoolAuthorityReplicas(context.Background(), s, time.Second); err != nil {
		t.Fatal(err)
	}
	if !prepared {
		t.Fatal("storage lease was not prepared")
	}
}

// Only one push per store waits for the backup lock: the others are turned away at once, and
// the waiting one gives up when its context ends, so a writer stuck in the lock leaves no
// queue of blocked pushes behind the probe ticks.
func TestPoolAuthorityPushWaitsForBackupLockWithoutQueueing(t *testing.T) {
	s := seedBackupMaster(t)
	poolBackupAuthorityMu.Lock()
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			poolBackupAuthorityMu.Unlock()
		}
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan error, 1)
	go func() { waiting <- PushPoolAuthorityReplicas(ctx, s, time.Minute) }()
	deadline := time.Now().Add(time.Minute)
	for {
		poolReplicaMu.Lock()
		inFlight := poolReplicaPushing[s.dir]
		poolReplicaMu.Unlock()
		if inFlight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("push never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := PushPoolAuthorityReplicas(context.Background(), s, time.Second); !errors.Is(err, errPoolAuthorityReplicaBusy) {
		t.Fatalf("a second push queued behind the first: %v", err)
	}
	cancel()
	select {
	case err := <-waiting:
		if err == nil {
			t.Fatal("cancelled push reported success without the lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("push kept waiting for the backup lock after its context ended")
	}
	unlock()
	if err := PushPoolAuthorityReplicas(context.Background(), s, time.Second); err != nil {
		t.Fatalf("push after the lock was released: %v", err)
	}
}

// An unchanged authority is not re-read in full on every tick, and a failed push makes the next
// one verify the whole root again and restage what is wrong.
func TestPoolAuthorityPushRestagesAfterFailureNotOnEveryTick(t *testing.T) {
	s := seedBackupMaster(t)
	seedLargeBackupRuns(t, s, 3)
	if err := PushPoolAuthorityReplicas(context.Background(), s, time.Second); err != nil {
		t.Fatal(err)
	}
	replica, _, err := buildPoolAuthorityReplica(s)
	if err != nil || replica.BackupManifest == nil {
		t.Fatalf("manifest = %#v %v", replica.BackupManifest, err)
	}
	manifest := replica.BackupManifest
	page := poolBackupPageFile(s, manifest.SHA256, 0)
	good, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	rotten := append([]byte(nil), good...)
	rotten[len(rotten)-1] ^= 1 // same size, so only a full read notices
	if err := os.WriteFile(page, rotten, 0600); err != nil {
		t.Fatal(err)
	}
	if err := PushPoolAuthorityReplicas(context.Background(), s, time.Second); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(page); string(after) != string(rotten) {
		t.Fatal("an unchanged root was re-read and restaged on a quiet tick")
	}
	forgetStagedPoolBackupRoot(s) // what a refused or unreadable push does
	if err := PushPoolAuthorityReplicas(context.Background(), s, time.Second); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(page); string(after) != string(good) {
		t.Fatal("the staged root was not verified and restaged after a failed push")
	}
}
