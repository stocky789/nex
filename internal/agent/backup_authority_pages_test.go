// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestPoolBackupPagedAuthorityRetainsMoreThanFiveThousandRuns(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	if err := os.MkdirAll(poolBackupRunsPath(master), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(poolBackupCatalogPath(master), 0700); err != nil {
		t.Fatal(err)
	}
	pins := poolBackupPinsState{Pins: map[string]protocol.BackupRestorePin{}}
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5001; i++ {
		run := protocol.BackupRun{
			OperationID: fmt.Sprintf("run-%05d", i), Kind: protocol.BackupKindBackupNow,
			PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed",
			CreatedAt: stamp,
		}
		if run.Valid() != nil {
			t.Fatal("test run is invalid")
		}
		body, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(poolBackupRunsPath(master), run.OperationID+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		pointID := fmt.Sprintf("point-%05d", i)
		point := protocol.BackupRecoveryPoint{ID: pointID, RepositoryID: "repo-1", PoolID: "pool-1", SourceVMID: "vm-1",
			CapturedAt: stamp, Consistency: protocol.BackupCaptureStopped, ManifestSHA256: fmt.Sprintf("%064x", i+1)}
		body, err = json.Marshal(point)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(poolBackupCatalogPath(master), pointID+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		pins.Pins[run.OperationID] = protocol.BackupRestorePin{RecoveryPointID: pointID, OperationID: run.OperationID,
			Kind: protocol.BackupPinKindTest, CreatedAt: stamp}
	}
	if err := savePoolBackupPins(master, pins); err != nil {
		t.Fatal(err)
	}
	receipt := protocol.BackupMetadataReceipt{OperationID: "receipt-large", Kind: "backup-policy-upsert",
		IntentSHA256: fmt.Sprintf("%064x", 3), Status: "succeeded", ObservedAt: stamp}
	if err := savePoolBackupMetadataReceipt(master, receipt); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	stored, err := loadPoolReplica(candidate)
	if err != nil || stored == nil || stored.Replica.BackupManifest == nil {
		t.Fatalf("paged replica: %#v %v", stored, err)
	}
	if body, err := json.Marshal(stored.Replica); err != nil || len(body) > protocol.PoolReplicaLimit {
		t.Fatalf("replica body length %d: %v", len(body), err)
	}
	auth, err := loadPoolBackupAuthorityPages(candidate, stored.Replica.BackupManifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(auth.History) != 5001 || auth.History[0].OperationID != "run-00000" || auth.History[5000].OperationID != "run-05000" ||
		len(auth.Catalog) != 5001 || len(auth.Pins) != 5001 || auth.Pins[5000].RecoveryPointID != "point-05000" ||
		len(auth.MetadataReceipts) != 1 || auth.MetadataReceipts[0].OperationID != receipt.OperationID {
		t.Fatalf("paged authority incomplete: runs=%d catalog=%d pins=%d receipts=%d", len(auth.History), len(auth.Catalog), len(auth.Pins), len(auth.MetadataReceipts))
	}
	manifest := stored.Replica.BackupManifest
	wrongCounts := *manifest
	wrongCounts.History--
	if _, err := loadPoolBackupAuthorityPages(candidate, &wrongCounts); err == nil {
		t.Fatal("mismatched manifest counts were accepted")
	}
	first := poolBackupPageFile(candidate, manifest.SHA256, 0)
	page, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPoolReplica(candidate); err == nil {
		t.Fatal("missing page left replica verified")
	}
	caller, ok := poolMemberByID(peerState(t, candidate).Membership, master.State.HostID)
	if !ok {
		t.Fatal("master missing from membership")
	}
	if _, err := acceptPoolAuthorityReplica(candidate, caller, stored.Replica); err == nil {
		t.Fatal("missing page was acknowledged")
	}
	pushReplicas(t, master)
	if _, err := loadPoolReplica(candidate); err != nil {
		t.Fatalf("retry did not repair missing page: %v", err)
	}
	if manifest.PageCount < 2 {
		t.Fatal("expected multiple pages")
	}
	second := poolBackupPageFile(candidate, manifest.SHA256, 1)
	other, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	if err := PushPoolAuthorityReplicas(context.Background(), master, 20*time.Second); err != nil {
		t.Fatalf("resumed page push: %v", err)
	}
	if restored, err := os.ReadFile(second); err != nil || !bytes.Equal(restored, other) {
		t.Fatalf("missing middle page was not restored: %v", err)
	}
	if err := os.WriteFile(first, other, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPoolReplica(candidate); err == nil {
		t.Fatal("out-of-order page left replica verified")
	}
	page[0] ^= 1
	if err := os.WriteFile(first, page, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPoolReplica(candidate); err == nil {
		t.Fatal("tampered page left replica verified")
	}
}

func TestPoolBackupAuthorityPagesRetainRestoreManifestDigest(t *testing.T) {
	s := seedBackupMaster(t)
	run := protocol.BackupRun{OperationID: "restore-bound", Kind: protocol.BackupKindRestoreNew,
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1",
		NewVMID: "22222222-2222-4222-8222-222222222222", ManifestSHA256: strings.Repeat("a", 64),
		Stage: "placement", Status: "needs_attention", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	auth, err := PoolBackupAuthorityRefs(s)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := stagePoolBackupAuthority(s, auth)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := loadPoolBackupAuthorityPages(s, manifest)
	if err != nil || len(recovered.History) != 1 || recovered.History[0].ManifestSHA256 != run.ManifestSHA256 {
		t.Fatalf("paged takeover lost immutable restore digest: %+v err=%v", recovered, err)
	}
}

func TestPoolBackupActivationSwitchesAllStoresWithPeerRecord(t *testing.T) {
	s := seedBackupMaster(t)
	old := testPolicy()
	old.ID = "old-policy"
	if err := savePoolBackupPolicies(s, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{old.ID: old}}); err != nil {
		t.Fatal(err)
	}
	newPolicy := testPolicy()
	stamp := time.Now().UTC()
	run := protocol.BackupRun{OperationID: "run-new", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1",
		RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: stamp}
	point := protocol.BackupRecoveryPoint{ID: "point-new", RepositoryID: "repo-1", PoolID: "pool-1", SourceVMID: "vm-1",
		CapturedAt: stamp, Consistency: protocol.BackupCaptureStopped, ManifestSHA256: fmt.Sprintf("%064x", 2)}
	pin := protocol.BackupRestorePin{RecoveryPointID: point.ID, OperationID: "restore-new", Kind: protocol.BackupPinKindTest, CreatedAt: stamp}
	secondPin := protocol.BackupRestorePin{RecoveryPointID: point.ID, OperationID: "restore-other", Kind: protocol.BackupPinKindNew, CreatedAt: stamp}
	receipt := protocol.BackupMetadataReceipt{
		OperationID: "receipt-1", Kind: "backup-policy-upsert", IntentSHA256: fmt.Sprintf("%064x", 1),
		Status: "succeeded", ObservedAt: time.Now().UTC(),
	}
	auth := &protocol.PoolBackupAuthority{Definitions: []protocol.BackupPolicy{newPolicy}, History: []protocol.BackupRun{run},
		Catalog: []protocol.BackupRecoveryPoint{point}, Pins: []protocol.BackupRestorePin{pin, secondPin}, MetadataReceipts: []protocol.BackupMetadataReceipt{receipt}}
	root, rollback, release, err := stagePoolBackupAuthorityActivation(s, auth, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before, err := loadPoolBackupPolicies(s)
	if err != nil || before.Policies[old.ID].ID != old.ID {
		t.Fatalf("old tree disappeared before peer commit: %#v %v", before, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	crashedBefore, err := OpenStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	s = crashedBefore
	beforeRestart, err := loadPoolBackupPolicies(s)
	if err != nil || beforeRestart.Policies[old.ID].ID != old.ID {
		t.Fatalf("restart before commit lost old tree: %#v %v", beforeRestart, err)
	}
	peer := peerState(t, s)
	peer.BackupActiveRoot = root
	if err := savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	after, err := loadPoolBackupPolicies(s)
	if err != nil || len(after.Policies) != 1 || after.Policies[newPolicy.ID].ID != newPolicy.ID {
		t.Fatalf("new tree unavailable after peer commit: %#v %v", after, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	crashedAfter, err := OpenStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	s = crashedAfter
	t.Cleanup(func() { _ = s.Close() })
	afterRestart, err := loadPoolBackupPolicies(s)
	if err != nil || afterRestart.Policies[newPolicy.ID].ID != newPolicy.ID {
		t.Fatalf("restart after commit lost new tree: %#v %v", afterRestart, err)
	}
	receipts, err := listPoolBackupMetadataReceipts(s)
	if err != nil || len(receipts) != 1 || receipts[0].OperationID != receipt.OperationID {
		t.Fatalf("receipt missing after commit: %#v %v", receipts, err)
	}
	if root != peer.BackupActiveRoot {
		t.Fatal("peer root changed")
	}
	if runs, err := listPoolBackupRuns(s); err != nil || len(runs) != 1 || runs[0].OperationID != run.OperationID {
		t.Fatalf("run missing after commit: %#v %v", runs, err)
	}
	if points, err := loadPoolBackupCatalog(s); err != nil || len(points) != 1 || points[0].ID != point.ID {
		t.Fatalf("catalog missing after commit: %#v %v", points, err)
	}
	if storedPins, err := loadPoolBackupPins(s); err != nil || len(storedPins.Pins) != 2 ||
		storedPins.Pins[pin.OperationID].RecoveryPointID != point.ID || storedPins.Pins[secondPin.OperationID].RecoveryPointID != point.ID {
		t.Fatalf("same-point pins missing after commit: %#v %v", storedPins, err)
	}
	rollback() // Simulates a late save error after the peer file was renamed.
	if _, err := os.Stat(filepath.Join(s.dir, root)); err != nil {
		t.Fatalf("active tree was deleted after late error: %v", err)
	}
	peer.BackupActiveRoot = ""
	if err := savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	rollback()
	restored, err := loadPoolBackupPolicies(s)
	if err != nil || restored.Policies[old.ID].ID != old.ID {
		t.Fatalf("old tree unavailable after rollback: %#v %v", restored, err)
	}
}

func TestPoolBackupAuthorityManifestDoesNotCapRetainedRecordCount(t *testing.T) {
	// 400,004 records of at least five bytes each, in the ten pages that hold them.
	m := protocol.PoolBackupManifest{SHA256: strings.Repeat("a", 64), PageCount: 10,
		TotalBytes: 2_500_000, History: 100001, Catalog: 100001, Pins: 100001, MetadataReceipts: 100001}
	if err := m.Valid(); err != nil {
		t.Fatalf("retained record count was capped: %v", err)
	}
}

func TestPoolBackupAuthorityPageReadIsBounded(t *testing.T) {
	s := seedBackupMaster(t)
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = 1
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	run := protocol.BackupRun{OperationID: "run-page-bound", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: time.Now().UTC()}
	manifest, err := stagePoolBackupAuthority(s, &protocol.PoolBackupAuthority{History: []protocol.BackupRun{run}})
	if err != nil {
		t.Fatalf("v2 fixture failed: %+v %v", manifest, err)
	}
	if err := os.Truncate(poolBackupPageFile(s, manifest.SHA256, 0), 1<<30); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := readPoolBackupPage(s, manifest, 0); err == nil {
		t.Fatal("oversized sparse page was accepted")
	}
	runtime.ReadMemStats(&after)
	if after.TotalAlloc-before.TotalAlloc > 2<<20 {
		t.Fatalf("oversized page allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
	}
}

func TestPoolBackupAuthorityV2PeerTransferAndCorruptRetry(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	if err := os.MkdirAll(poolBackupRunsPath(master), 0700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 1050; i++ {
		run := protocol.BackupRun{OperationID: fmt.Sprintf("run-%05d", i), Kind: protocol.BackupKindBackupNow,
			PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed",
			Error: strings.Repeat("x", 64<<10), CreatedAt: stamp}
		body, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(poolBackupRunsPath(master), run.OperationID+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	pushReplicas(t, master)
	runtime.ReadMemStats(&after)
	t.Logf("v2 transfer elapsed=%s allocated=%d bytes", time.Since(started), after.TotalAlloc-before.TotalAlloc)
	record, err := loadPoolReplica(candidate)
	if err != nil || record == nil || record.Replica.BackupManifest == nil {
		t.Fatalf("candidate did not acknowledge v2 authority: %#v %v", record, err)
	}
	manifest := record.Replica.BackupManifest
	if manifest.TotalBytes <= protocol.PoolBackupBundleLimit || manifest.PageCount <= 0 {
		t.Fatalf("authority did not cross v1 bound: %+v", manifest)
	}
	auth, err := loadPoolBackupAuthorityPages(candidate, manifest)
	if err != nil || auth == nil || len(auth.History) != 1050 {
		t.Fatalf("candidate lost retained history: %#v %v", auth, err)
	}
	wrong := *manifest
	wrong.History--
	if _, err := loadPoolBackupAuthorityPages(candidate, &wrong); err == nil {
		t.Fatal("v2 mismatched record count was accepted")
	}
	wrong = *manifest
	wrong.TotalBytes--
	if _, err := loadPoolBackupAuthorityPages(candidate, &wrong); err == nil {
		t.Fatal("v2 mismatched byte count was accepted")
	}
	wrong = *manifest
	wrong.PageCount++
	if _, err := loadPoolBackupAuthorityPages(candidate, &wrong); err == nil {
		t.Fatal("v2 missing final page was accepted")
	}
	page := poolBackupPageFile(candidate, manifest.SHA256, 0)
	original, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), original...)
	corrupt[0] ^= 1
	if err := os.WriteFile(page, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPoolReplica(candidate); err == nil {
		t.Fatal("corrupt v2 page remained promotable")
	}
	if err := PushPoolAuthorityReplicas(context.Background(), master, 20*time.Second); err != nil {
		t.Fatalf("corrupt staging retry failed: %v", err)
	}
	if repaired, err := os.ReadFile(page); err != nil || !bytes.Equal(repaired, original) {
		t.Fatalf("corrupt page was not repaired: %v", err)
	}
	beforeBytes := poolBackupBlobBytes(t, candidate)
	for i := 1050; i < 1052; i++ {
		beforeWireBytes := lab.pageDataBytes.Load()
		run := protocol.BackupRun{OperationID: fmt.Sprintf("run-%05d", i), Kind: protocol.BackupKindBackupNow,
			PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed",
			Error: strings.Repeat("x", 64<<10), CreatedAt: stamp}
		body, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(poolBackupRunsPath(master), run.OperationID+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		pushReplicas(t, master)
		// Records are packed, so an append resends at most the final page.
		if sent := lab.pageDataBytes.Load() - beforeWireBytes; sent == 0 || sent > protocol.PoolBackupPageLimit {
			t.Fatalf("one appended run transferred %d page bytes", sent)
		}
		nextBytes := poolBackupBlobBytes(t, candidate)
		if nextBytes-beforeBytes <= 0 || nextBytes-beforeBytes > protocol.PoolBackupPageLimit {
			t.Fatalf("one appended run stored %d new blob bytes", nextBytes-beforeBytes)
		}
		beforeBytes = nextBytes
	}
	latest, err := loadPoolReplica(candidate)
	if err != nil || latest == nil || latest.Replica.BackupManifest == nil || latest.Replica.BackupManifest.History != 1052 {
		t.Fatalf("incremental replica lost history: %#v %v", latest, err)
	}
	if dirs := poolBackupBundleDirs(t, candidate); dirs > 2 {
		t.Fatalf("old authority bundles were not collected: %d", dirs)
	}
	beforeMembership := peerState(t, master).Membership
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-v2-large-promote", "member-1"))
	if result.Status != "succeeded" {
		t.Fatalf("incremental authority promotion = %#v", result)
	}
	requirePromoted(t, lab, candidate, beforeMembership)
	runs, err := listPoolBackupRuns(candidate)
	if err != nil || len(runs) != 1052 || runs[0].OperationID != "run-00000" || runs[len(runs)-1].OperationID != "run-01051" {
		t.Fatalf("promoted authority lost retained runs: %d %v", len(runs), err)
	}
}

func TestBackupAuthorityV2AdmitsRepositoryReceiptAfterLargeHistory(t *testing.T) {
	lab := promotionLab(t, 2)
	master := lab.host("master-1")
	pushReplicas(t, master)
	if err := os.MkdirAll(poolBackupRunsPath(master), 0700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 1052; i++ {
		run := protocol.BackupRun{OperationID: fmt.Sprintf("run-%05d", i), Kind: protocol.BackupKindBackupNow,
			PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed",
			Error: strings.Repeat("x", 64<<10), CreatedAt: stamp}
		body, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(poolBackupRunsPath(master), run.OperationID+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pushReplicas(t, master)
	for _, id := range []string{"member-1", "member-2"} {
		record, err := loadPoolReplica(lab.host(id))
		if err != nil || record == nil || record.Replica.BackupManifest == nil ||
			record.Replica.BackupManifest.TotalBytes <= protocol.PoolBackupBundleLimit {
			t.Fatalf("%s did not verify >64 MiB v2 history: %#v %v", id, record, err)
		}
	}
	poolReplicaMu.Lock()
	for _, id := range []string{"member-1", "member-2"} {
		status := poolReplicaObservations[master.dir][id]
		status.ReceivedAt = time.Now().Add(-2 * time.Minute)
		poolReplicaObservations[master.dir][id] = status
	}
	poolReplicaMu.Unlock()
	receipt := protocol.BackupMetadataReceipt{OperationID: "repo-register-large", Kind: "backup-repository-register",
		IntentSHA256: fmt.Sprintf("%064x", 1), Status: "pending", ObservedAt: stamp}
	poolBackupAuthorityMu.Lock()
	err := savePoolBackupMetadataReceipt(master, receipt)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		t.Fatalf("v2 repository receipt admission refused: %v", err)
	}
	lab.drop("member-2", "!"+poolReplicaPath)
	receipt.OperationID = "repo-register-immediate-disconnect"
	if err := savePoolBackupMetadataReceipt(master, receipt); err == nil {
		t.Fatal("fresh cached observation allowed v2 admission after member disconnect")
	}
	if _, err := loadPoolBackupMetadataReceipt(master, receipt.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disconnected member receipt was persisted: %v", err)
	}
	lab.drop("member-2", "")
	poolReplicaMu.Lock()
	delete(poolReplicaObservations, master.dir) // Model a master agent restart after the v2 ACKs.
	poolReplicaMu.Unlock()
	receipt.OperationID = "repo-register-restart"
	if err := savePoolBackupMetadataReceipt(master, receipt); err != nil {
		t.Fatalf("v2 repository receipt after restart refused: %v", err)
	}
	poolReplicaMu.Lock()
	for _, id := range []string{"member-1", "member-2"} {
		status := poolReplicaObservations[master.dir][id]
		status.ReceivedAt = time.Now().Add(-2 * time.Minute)
		poolReplicaObservations[master.dir][id] = status
	}
	poolReplicaMu.Unlock()
	lab.drop("member-2", "!"+poolReplicaPath)
	receipt.OperationID = "repo-register-unreachable"
	if err := savePoolBackupMetadataReceipt(master, receipt); err == nil {
		t.Fatal("unreachable member allowed v2 receipt admission")
	}
	if _, err := loadPoolBackupMetadataReceipt(master, receipt.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused receipt was persisted: %v", err)
	}
}

func TestBackupAuthorityV2RefusesMemberBehindMasterGeneration(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	first := protocol.BackupRun{OperationID: "run-first", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(master, first); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = 1
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	pushReplicas(t, master)
	second := first
	second.OperationID = "run-second"
	if err := savePoolBackupRun(master, second); err != nil {
		t.Fatal(err)
	}
	lab.drop("member-1", "POST:!"+poolReplicaPath)
	if err := PushPoolAuthorityReplicas(context.Background(), master, 20*time.Second); err == nil {
		t.Fatal("interrupted replica push unexpectedly succeeded")
	}
	lab.drop("member-1", "")
	masterGeneration, _, err := PoolAuthorityGeneration(master)
	if err != nil {
		t.Fatal(err)
	}
	member, err := loadPoolReplica(candidate)
	if err != nil || member == nil || member.Replica.Generation >= masterGeneration {
		t.Fatalf("member did not remain behind master: %#v %v", member, err)
	}
	receipt := protocol.BackupMetadataReceipt{OperationID: "repo-register-lagging", Kind: "backup-repository-register",
		IntentSHA256: fmt.Sprintf("%064x", 1), Status: "pending", ObservedAt: time.Now().UTC()}
	if err := savePoolBackupMetadataReceipt(master, receipt); err == nil {
		t.Fatal("lagging v2 member allowed new metadata receipt")
	}
	if _, err := loadPoolBackupMetadataReceipt(master, receipt.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lagging member receipt was persisted: %v", err)
	}
}

// Admission proves every member holds the current replica, so a commit or
// membership change during those checks must refuse the growth.
func TestBackupAuthorityAdmissionRechecksAfterMemberProof(t *testing.T) {
	lab := promotionLab(t, 1)
	master := lab.host("master-1")
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = 1
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	pushReplicas(t, master)
	for _, change := range []struct {
		name  string
		apply func() error
	}{
		{"authority", func() error {
			_, err := advancePoolAuthorityGeneration(master, "concurrent-commit")
			return err
		}},
		{"membership", func() error {
			poolPeerMu.Lock()
			defer poolPeerMu.Unlock()
			state, err := loadPoolPeer(master)
			if err == nil {
				state.Membership.Generation++
				for i := range state.Membership.Members {
					state.Membership.Members[i].Generation = state.Membership.Generation
				}
				err = savePoolPeer(master, state)
			}
			return err
		}},
	} {
		pushReplicas(t, master)
		var once sync.Once
		lab.intercept.Store(func(hostID string, r *http.Request) {
			if hostID == "member-1" && r.Method == http.MethodGet && r.URL.Path == poolReplicaPath {
				once.Do(func() {
					if err := change.apply(); err != nil {
						t.Error(err)
					}
				})
			}
		})
		receipt := protocol.BackupMetadataReceipt{OperationID: "repo-register-" + change.name, Kind: "backup-repository-register",
			IntentSHA256: fmt.Sprintf("%064x", 1), Status: "pending", ObservedAt: time.Now().UTC()}
		err := savePoolBackupMetadataReceipt(master, receipt)
		lab.intercept.Store(func(string, *http.Request) {})
		if err == nil {
			t.Fatalf("%s change during member proof allowed backup authority growth", change.name)
		}
		if _, err := loadPoolBackupMetadataReceipt(master, receipt.OperationID); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s change: refused receipt was persisted: %v", change.name, err)
		}
	}
}

func poolBackupBlobBytes(t *testing.T, s *Store) int64 {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.dir, poolBackupBlobDir))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	return total
}

func poolBackupBundleDirs(t *testing.T, s *Store) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(s.dir, poolBackupPageDir))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".stage-") {
			count++
		}
	}
	return count
}

func TestBackupAuthorityAbandonedStageIsCollected(t *testing.T) {
	s := seedBackupMaster(t)
	first, err := newPoolBackupStage(s)
	if err != nil {
		t.Fatal(err)
	}
	page := []byte("abandoned page")
	poolBackupBlobMu.Lock()
	hash, err := storePoolBackupBlob(s, page)
	if err == nil {
		err = os.Link(poolBackupBlobFile(s, hash), filepath.Join(first, "0000"))
	}
	poolBackupBlobMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Model restart: the on-disk stage survives, but the active-process list does not.
	poolBackupBlobMu.Lock()
	delete(activePoolBackupStages, first)
	poolBackupBlobMu.Unlock()
	second, err := newPoolBackupStage(s)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePoolBackupStage(second)
	if _, err := os.Stat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned stage survived restart: %v", err)
	}
	if _, err := os.Stat(poolBackupBlobFile(s, hash)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned blob survived restart: %v", err)
	}
	third, err := newPoolBackupStage(s)
	if err != nil {
		t.Fatal(err)
	}
	defer releasePoolBackupStage(third)
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("active stage was collected: %v", err)
	}
}

func TestSignedBackupAuthorityWaitsForCatalogRunTransaction(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	pushReplicas(t, master)
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	point := protocol.BackupRecoveryPoint{ID: "point-transaction", RepositoryID: "repo-1", PoolID: "pool-1",
		SourceVMID: "vm-1", CapturedAt: stamp, Consistency: protocol.BackupCaptureStopped, ManifestSHA256: fmt.Sprintf("%064x", 1)}
	run := protocol.BackupRun{OperationID: "run-transaction", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: stamp}
	if err := os.MkdirAll(poolBackupCatalogPath(master), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(poolBackupRunsPath(master), 0700); err != nil {
		t.Fatal(err)
	}
	poolBackupAuthorityMu.Lock()
	locked := true
	defer func() {
		if locked {
			poolBackupAuthorityMu.Unlock()
		}
	}()
	body, _ := json.Marshal(point)
	if err := os.WriteFile(filepath.Join(poolBackupCatalogPath(master), point.ID+".json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- PushPoolAuthorityReplicas(context.Background(), master, 20*time.Second) }()
	select {
	case err := <-done:
		t.Fatalf("signed push completed inside catalog/run transaction: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	body, _ = json.Marshal(run)
	if err := os.WriteFile(filepath.Join(poolBackupRunsPath(master), run.OperationID+".json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	poolBackupAuthorityMu.Unlock()
	locked = false
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	replica, err := loadPoolReplica(candidate)
	if err != nil || replica == nil {
		t.Fatalf("replica after settlement: %v", err)
	}
	auth, err := loadPoolBackupAuthorityPages(candidate, replica.Replica.BackupManifest)
	if err != nil || auth == nil || len(auth.Catalog) != 1 || len(auth.History) != 1 {
		t.Fatalf("signed authority has a mixed catalog/run state: %#v %v", auth, err)
	}
}

func TestPoolBackupAuthorityV2PromotionStagesBeforeMasterFlip(t *testing.T) {
	s := seedBackupMaster(t)
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = 1
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	run := protocol.BackupRun{OperationID: "run-inherited", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	point := protocol.BackupRecoveryPoint{ID: "point-inherited", RepositoryID: "repo-1", PoolID: "pool-1",
		SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		ManifestSHA256: strings.Repeat("a", 64)}
	auth := &protocol.PoolBackupAuthority{History: []protocol.BackupRun{run}, Catalog: []protocol.BackupRecoveryPoint{point}}
	manifest, err := stagePoolBackupAuthority(s, auth)
	if err != nil {
		t.Fatalf("v2 staging: %+v %v", manifest, err)
	}
	oldRoot := peerState(t, s).BackupActiveRoot
	page := poolBackupPageFile(s, manifest.SHA256, 0)
	original, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), original...)
	corrupt[len(corrupt)-1] ^= 1
	if err := os.WriteFile(page, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := stagePoolBackupAuthorityActivationPages(s, manifest, 9); err == nil {
		t.Fatal("corrupt authority activated")
	}
	if peerState(t, s).BackupActiveRoot != oldRoot {
		t.Fatal("failed staging changed active backup root")
	}
	if err := os.WriteFile(page, original, 0600); err != nil {
		t.Fatal(err)
	}
	root, rollback, release, err := stagePoolBackupAuthorityActivationPages(s, manifest, 9)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if peerState(t, s).BackupActiveRoot != oldRoot {
		t.Fatal("staging activated before peer commit")
	}
	peer := peerState(t, s)
	peer.BackupActiveRoot = root
	if err := savePoolPeer(s, peer); err != nil {
		t.Fatal(err)
	}
	rollback()
	retained, err := loadPoolBackupRun(s, run.OperationID)
	if err != nil || retained.Status != "needs_attention" {
		t.Fatalf("inherited run not retained safely: %#v %v", retained, err)
	}
	if got, err := getCatalogPoint(s, point.RepositoryID, point.ID); err != nil || got.ID != point.ID {
		t.Fatalf("inherited catalog missing: %#v %v", got, err)
	}
}

func TestPoolBackupAuthorityV2PromotionRetainsHistory(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	first := protocol.BackupRun{OperationID: "run-v1-seed", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(master, first); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = 1
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	run := protocol.BackupRun{OperationID: "run-v2-promote", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	replica, err := loadPoolReplica(candidate)
	if err != nil || replica == nil || replica.Replica.BackupManifest == nil {
		t.Fatalf("candidate does not hold v2 authority: %#v %v", replica, err)
	}
	before := peerState(t, master).Membership
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	result := ExecutePoolPromotion(context.Background(), candidate, promoteCommand("op-v2-promote", "member-1"))
	if result.Status != "succeeded" {
		t.Fatalf("v2 promotion = %#v", result)
	}
	requirePromoted(t, lab, candidate, before)
	retained, err := loadPoolBackupRun(candidate, run.OperationID)
	if err != nil || retained.OperationID != run.OperationID || retained.Status != run.Status {
		t.Fatalf("promoted master lost v2 run: %#v %v", retained, err)
	}
}

func TestBackupAuthorityV2RequiresCurrentMemberSupport(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	first := protocol.BackupRun{OperationID: "run-first", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: stamp}
	if err := savePoolBackupRun(master, first); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	manifest, err := encodePoolBackupAuthorityFromStore(master, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = manifest.TotalBytes
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	second := first
	second.OperationID = "run-second"
	lab.drop(candidate.State.HostID, "!"+poolReplicaPath)
	if err := savePoolBackupRun(master, second); err == nil {
		t.Fatal("unconfirmed candidate allowed v2 growth")
	}
	if _, err := loadPoolBackupRun(master, second.OperationID); !os.IsNotExist(err) {
		t.Fatalf("refused run persisted: %v", err)
	}
	lab.drop(candidate.State.HostID, "")
	if err := savePoolBackupRun(master, second); err != nil {
		t.Fatalf("current candidate blocked growth: %v", err)
	}
}

func TestBackupAuthorityV2AllowsRunAndCatalogSettlement(t *testing.T) {
	s := seedBackupMaster(t)
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	run := protocol.BackupRun{OperationID: "run-capacity", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", RecoveryPointID: "run-capacity", VMID: "vm-1",
		Stage: "reserved", Status: "pending", CreatedAt: stamp}
	if _, err := ReservePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	manifest, err := encodePoolBackupAuthorityFromStore(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = manifest.TotalBytes
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	point := protocol.BackupRecoveryPoint{ID: run.RecoveryPointID, RepositoryID: run.RepositoryID,
		PoolID: run.PoolID, SourceVMID: run.VMID, CapturedAt: stamp,
		Consistency: protocol.BackupCaptureStopped, ManifestSHA256: strings.Repeat("a", 64)}
	run.Status, run.Stage = "succeeded", "published"
	if err := SettlePoolBackupRun(s, run, &point); err != nil {
		t.Fatalf("v2 settlement failed: %v", err)
	}
	stored, err := loadPoolBackupRun(s, run.OperationID)
	if err != nil || stored.Status != "succeeded" {
		t.Fatalf("v2 settlement lost run: %#v %v", stored, err)
	}
	if _, err := getCatalogPoint(s, point.RepositoryID, point.ID); err != nil {
		t.Fatalf("v2 settlement lost catalog point: %v", err)
	}
}

func TestBackupAuthorityV2AllowsOtherRunsBeforeSettlement(t *testing.T) {
	s := seedBackupMaster(t)
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = 600 << 10
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	run := protocol.BackupRun{OperationID: "run-reserved", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", RecoveryPointID: "run-reserved", VMID: "vm-1",
		Stage: "reserved", Status: "pending", CreatedAt: stamp}
	if _, err := ReservePoolBackupRun(s, run); err != nil {
		t.Fatalf("backup reservation: %v", err)
	}
	other := run
	other.OperationID, other.RecoveryPointID = "run-consuming-headroom", "run-consuming-headroom"
	other.Stage, other.Status, other.Error = "failed", "failed", strings.Repeat("x", 100<<10)
	if err := savePoolBackupRun(s, other); err != nil {
		t.Fatalf("v2 refused another retained run: %v", err)
	}
	point := protocol.BackupRecoveryPoint{ID: run.RecoveryPointID, RepositoryID: run.RepositoryID,
		PoolID: run.PoolID, SourceVMID: run.VMID, CapturedAt: stamp,
		Consistency: protocol.BackupCaptureStopped, ManifestSHA256: strings.Repeat("a", 64)}
	run.Stage, run.Status, run.Error = "published", "succeeded", strings.Repeat("\x00", 4096)
	if err := SettlePoolBackupRun(s, run, &point); err != nil {
		t.Fatalf("reserved terminal backup did not fit: %v", err)
	}
	stored, err := loadPoolBackupRun(s, run.OperationID)
	if err != nil || stored.Status != "succeeded" {
		t.Fatalf("terminal backup was not durable: %#v %v", stored, err)
	}
	if _, err := getCatalogPoint(s, point.RepositoryID, point.ID); err != nil {
		t.Fatalf("reserved catalog point was not durable: %v", err)
	}
}

func TestBackupAuthorityV2AllowsMetadataOutcomeAfterGrowth(t *testing.T) {
	s := seedBackupMaster(t)
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = 30 << 10
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	receipt := protocol.BackupMetadataReceipt{OperationID: "metadata-boundary", Kind: "backup-policy-upsert",
		IntentSHA256: strings.Repeat("a", 64), Status: "pending", ObservedAt: stamp}
	if err := savePoolBackupMetadataReceipt(s, receipt); err != nil {
		t.Fatalf("pending outcome reservation: %v", err)
	}
	run := protocol.BackupRun{OperationID: "run-consuming-receipt-room", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed",
		Error: strings.Repeat("x", 6<<10), CreatedAt: stamp}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatalf("v2 refused another retained run: %v", err)
	}
	receipt.Status, receipt.Error = "needs_attention", strings.Repeat("\x00", 4096)
	if err := savePoolBackupMetadataReceipt(s, receipt); err != nil {
		t.Fatalf("maximum escaped terminal outcome did not fit: %v", err)
	}
	stored, err := loadPoolBackupMetadataReceipt(s, receipt.OperationID)
	if err != nil || stored.Status != receipt.Status || stored.Error != receipt.Error {
		t.Fatalf("terminal metadata outcome was not durable: %#v %v", stored, err)
	}
}

func TestBackupAuthorityPendingRunMustFitTerminalFrame(t *testing.T) {
	s := seedBackupMaster(t)
	run := protocol.BackupRun{OperationID: "run-terminal-frame", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "reserved", Status: "pending",
		VMID: strings.Repeat("x", protocol.PoolBackupPageLimit-3000), CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err == nil {
		t.Fatal("pending run with no terminal frame room was committed")
	}
	if _, err := loadPoolBackupRun(s, run.OperationID); !os.IsNotExist(err) {
		t.Fatalf("refused pending run persisted: %v", err)
	}
}

func TestBackupAuthorityV2ProtectsRepositoryAndCatalog(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-protect")
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	manifest, err := encodePoolBackupAuthorityFromStore(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = manifest.TotalBytes
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-protect", Protected: true,
	}); err != nil {
		t.Fatalf("v2 protect failed: %v", err)
	}
	if !vmhost.ReadBackupProtectMarker(filepath.Join(mount, "point-protect")) {
		t.Fatal("v2 protect omitted repository marker")
	}
	point, err := getCatalogPoint(s, "repo-1", "point-protect")
	if err != nil || !point.Protected {
		t.Fatalf("v2 protect omitted catalog flag: %#v %v", point, err)
	}
}

// stagePoolBackupAuthority writes an authority held in memory as staged pages.
func stagePoolBackupAuthority(s *Store, auth *protocol.PoolBackupAuthority) (*protocol.PoolBackupManifest, error) {
	manifest, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		return nil, err
	}
	index := 0
	_, err = encodePoolBackupAuthority(auth, func(page []byte) error {
		path := poolBackupPageFile(s, manifest.SHA256, index)
		index++
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		return vmhost.AtomicWrite(path, page, 0600)
	})
	return manifest, err
}

// stagePoolBackupAuthorityActivation stages an in-memory authority as pages,
// then prepares its activation like a promotion does; nil is an empty authority.
func stagePoolBackupAuthorityActivation(s *Store, auth *protocol.PoolBackupAuthority, generation uint64) (string, func(), func(), error) {
	if auth == nil {
		return stagePoolBackupAuthorityActivationPages(s, nil, generation)
	}
	manifest, err := stagePoolBackupAuthority(s, auth)
	if err != nil {
		return "", nil, nil, err
	}
	return stagePoolBackupAuthorityActivationPages(s, manifest, generation)
}

func loadPoolBackupAuthorityPages(s *Store, m *protocol.PoolBackupManifest) (*protocol.PoolBackupAuthority, error) {
	auth := &protocol.PoolBackupAuthority{}
	err := walkPoolBackupAuthorityPages(s, m, func(page *protocol.PoolBackupAuthority) error {
		auth.Repositories = append(auth.Repositories, page.Repositories...)
		auth.Definitions = append(auth.Definitions, page.Definitions...)
		auth.History = append(auth.History, page.History...)
		auth.Catalog = append(auth.Catalog, page.Catalog...)
		auth.Pins = append(auth.Pins, page.Pins...)
		auth.MetadataReceipts = append(auth.MetadataReceipts, page.MetadataReceipts...)
		auth.OrphanCleanups = append(auth.OrphanCleanups, page.OrphanCleanups...)
		return nil
	})
	return auth, err
}

func TestBackupAuthorityStageRetainsRepositoryUntilPureCommitRelease(t *testing.T) {
	s := seedBackupMaster(t)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source:   protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups"},
		Identity: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/backups", FilesystemID: "test-nfs"}, MountPath: t.TempDir()}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	previousPrepare, previousVerify := prepareBackupRepositoryNative, verifyBackupRepository
	defer func() { prepareBackupRepositoryNative, verifyBackupRepository = previousPrepare, previousVerify }()
	calls := 0
	prepareBackupRepositoryNative = func(_ context.Context, requested protocol.BackupRepository) (protocol.BackupRepository, error) {
		calls++
		requested.MountPath, requested.Identity = repo.MountPath, repo.Identity
		return requested, nil
	}
	verifyBackupRepository = func(context.Context, protocol.BackupRepository) error { return nil }
	root, _, release, err := stagePoolBackupAuthorityActivation(s, &protocol.PoolBackupAuthority{Repositories: []protocol.BackupRepository{repo}}, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	requested := repo
	requested.Name, requested.MountPath, requested.Identity = "Renamed", "", nil
	command := protocol.Command{OperationID: "register-after-stage", Kind: "backup-repository-register", BackupRepository: &requested}
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &command}); !errors.Is(response.Err, ErrBackupConflict) {
		t.Fatalf("uncommitted activation admitted competing registration: %+v", response)
	}
	if _, err := prepareBackupRepositoryForAdoption(context.Background(), s, portablePeerRepository(repo), nil); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("live-host adoption bypassed staging reservation: %v", err)
	}
	if calls != 1 {
		t.Fatalf("competing request reached native mount: calls=%d", calls)
	}
	// An ambiguous activation releases only admission, never the staged tree.
	release()
	if _, err := os.Stat(filepath.Join(s.dir, root)); err != nil {
		t.Fatalf("pure release destroyed staged authority: %v", err)
	}
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &command})
	if response.Err != nil || response.Result == nil || response.Result.Status != "succeeded" {
		t.Fatalf("released stage still excluded registration: %+v", response)
	}
	saved, err := GetPoolBackupRepository(s, repo.ID)
	if err != nil || saved.Name != requested.Name {
		t.Fatalf("registration did not commit after release: %+v %v", saved, err)
	}
}
