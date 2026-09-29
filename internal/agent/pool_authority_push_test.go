// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"fmt"
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
	writerStarted := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		close(writerStarted)
		poolBackupAuthorityMu.Lock()
		defer poolBackupAuthorityMu.Unlock()
		writerDone <- savePoolBackupMetadataReceipt(s, protocol.BackupMetadataReceipt{
			OperationID: "receipt-after-snapshot", Kind: "backup-policy-upsert",
			IntentSHA256: fmt.Sprintf("%064x", 1), Status: "succeeded", ObservedAt: time.Now().UTC(),
		})
	}()
	<-writerStarted
	earlyCommit := false
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatal(err)
		}
		earlyCommit = true
	case <-time.After(100 * time.Millisecond):
	}
	unpause()
	if err := <-pushDone; err != nil {
		t.Fatal(err)
	}
	if !earlyCommit {
		if err := <-writerDone; err != nil {
			t.Fatal(err)
		}
	}
	if earlyCommit {
		t.Fatal("backup metadata committed before snapshot generation")
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
