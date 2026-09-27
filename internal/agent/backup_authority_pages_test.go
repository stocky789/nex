// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if len(manifest.PageSHA256) < 2 {
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
	previousWrite := writePoolBackupPage
	t.Cleanup(func() { writePoolBackupPage = previousWrite })
	writePoolBackupPage = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Base(path) == "0000" {
			return fmt.Errorf("already present prefix page was resent")
		}
		return previousWrite(path, data, mode)
	}
	if err := PushPoolAuthorityReplicas(context.Background(), master, 20*time.Second); err != nil {
		t.Fatalf("resumed page push: %v", err)
	}
	writePoolBackupPage = previousWrite
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
	root, rollback, err := stagePoolBackupAuthorityActivation(s, auth, 7)
	if err != nil {
		t.Fatal(err)
	}
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

func TestPoolBackupAuthorityNearTransferLimit(t *testing.T) {
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	auth := &protocol.PoolBackupAuthority{}
	for i := 0; i < 1000; i++ {
		auth.History = append(auth.History, protocol.BackupRun{
			OperationID: fmt.Sprintf("run-%05d", i), Kind: protocol.BackupKindBackupNow,
			PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed",
			Error: strings.Repeat("x", 64<<10), CreatedAt: stamp,
		})
	}
	manifest, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.TotalBytes < 60<<20 || manifest.TotalBytes > protocol.PoolBackupBundleLimit || len(manifest.PageBytes) < 200 {
		t.Fatalf("near-limit manifest is not bounded as expected: bytes=%d pages=%d", manifest.TotalBytes, len(manifest.PageBytes))
	}
	again, err := encodePoolBackupAuthority(auth, nil)
	if err != nil || again.SHA256 != manifest.SHA256 {
		t.Fatalf("near-limit digest changed: %v", err)
	}
	for i := 1000; i < 1050; i++ {
		auth.History = append(auth.History, protocol.BackupRun{
			OperationID: fmt.Sprintf("run-%05d", i), Kind: protocol.BackupKindBackupNow,
			PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed",
			Error: strings.Repeat("x", 64<<10), CreatedAt: stamp,
		})
	}
	if _, err := encodePoolBackupAuthority(auth, nil); err == nil {
		t.Fatal("over-limit retained history was silently accepted")
	}
}

func TestPoolBackupAuthoritySparsePagesRemainBelowPageLimit(t *testing.T) {
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	run := protocol.BackupRun{OperationID: "run-00000", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed",
		Error: strings.Repeat("x", 128<<10), CreatedAt: stamp}
	body, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	frameBytes := len(body) + 5
	if frameBytes <= protocol.PoolBackupPageLimit/2 {
		t.Fatal("fixture does not force one record per page")
	}
	count := protocol.PoolBackupBundleLimit / frameBytes
	auth := &protocol.PoolBackupAuthority{History: make([]protocol.BackupRun, count)}
	for i := range auth.History {
		run.OperationID = fmt.Sprintf("run-%05d", i)
		auth.History[i] = run
	}
	manifest, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.PageBytes) != count || len(manifest.PageBytes) > 511 {
		t.Fatalf("sparse authority used %d pages for %d records", len(manifest.PageBytes), count)
	}
	run.OperationID = fmt.Sprintf("run-%05d", count)
	auth.History = append(auth.History, run)
	if _, err := encodePoolBackupAuthority(auth, nil); err == nil {
		t.Fatal("bundle bytes did not refuse the next sparse page")
	}
}

func TestBackupAuthorityCapacityRefusesNewRunBeforeReplicaGoesStale(t *testing.T) {
	lab := promotionLab(t, 1)
	master, candidate := lab.host("master-1"), lab.host("member-1")
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	first := protocol.BackupRun{OperationID: "run-first", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: stamp}
	if err := savePoolBackupRun(master, first); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	stored, err := loadPoolReplica(candidate)
	if err != nil || stored == nil || stored.Replica.BackupAuthority == nil {
		t.Fatalf("candidate lacks backup authority: %v", err)
	}
	auth, err := PoolBackupAuthorityRefs(master)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = manifest.TotalBytes
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	second := first
	second.OperationID = "run-second"
	if err := savePoolBackupRun(master, second); err == nil {
		t.Fatal("unreplicable run was committed")
	}
	if _, err := loadPoolBackupRun(master, second.OperationID); !os.IsNotExist(err) {
		t.Fatalf("refused run persisted: %v", err)
	}
	_, digest, err := buildPoolAuthorityReplica(master)
	if err != nil || digest != stored.Digest {
		t.Fatalf("refused write left promotable replica stale: current=%s candidate=%s error=%v", digest, stored.Digest, err)
	}
}

func TestBackupAuthorityCapacityChecksRunAndCatalogBeforeSettlement(t *testing.T) {
	s := seedBackupMaster(t)
	stamp := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	run := protocol.BackupRun{OperationID: "run-capacity", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", RecoveryPointID: "run-capacity", VMID: "vm-1",
		Stage: "reserved", Status: "pending", CreatedAt: stamp}
	if _, err := ReservePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	auth, err := PoolBackupAuthorityRefs(s)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := encodePoolBackupAuthority(auth, nil)
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
	if err := SettlePoolBackupRun(s, run, &point); err == nil {
		t.Fatal("unreplicable settlement was committed")
	}
	stored, err := loadPoolBackupRun(s, run.OperationID)
	if err != nil || stored.Status != "pending" {
		t.Fatalf("failed settlement changed run: %#v %v", stored, err)
	}
	if _, err := getCatalogPoint(s, point.RepositoryID, point.ID); err == nil {
		t.Fatal("failed settlement published a catalog point")
	}
}

func TestBackupAuthorityCapacityReservesRunAndPointBeforeEffect(t *testing.T) {
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
	if err := savePoolBackupRun(s, other); err == nil {
		t.Fatal("another run consumed terminal backup headroom")
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

func TestBackupAuthorityCapacityReservesMetadataOutcomeBeforeEffect(t *testing.T) {
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
	if err := savePoolBackupRun(s, run); err == nil {
		t.Fatal("run consumed space reserved for metadata outcome")
	}
	if _, err := loadPoolBackupRun(s, run.OperationID); !os.IsNotExist(err) {
		t.Fatalf("refused run was retained: %v", err)
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

func TestBackupAuthorityCapacityRefusesProtectBeforeRepositoryMarker(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "point-protect")
	if _, err := ImportPoolBackupCatalog(s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	auth, err := PoolBackupAuthorityRefs(s)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := poolBackupBundleLimit
	poolBackupBundleLimit = manifest.TotalBytes
	t.Cleanup(func() { poolBackupBundleLimit = previous })
	if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-protect", Protected: true,
	}); err == nil {
		t.Fatal("unreplicable protect change was accepted")
	}
	if vmhost.ReadBackupProtectMarker(filepath.Join(mount, "point-protect")) {
		t.Fatal("refused protect changed repository marker")
	}
	point, err := getCatalogPoint(s, "repo-1", "point-protect")
	if err != nil || point.Protected {
		t.Fatalf("refused protect changed catalog: %#v %v", point, err)
	}
}
