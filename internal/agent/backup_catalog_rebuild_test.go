// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestCatalogImportPreservesProtectionChangedDuringCapacityAdmission(t *testing.T) {
	for _, protected := range []bool{true, false} {
		t.Run(fmt.Sprintf("protected-%t", protected), func(t *testing.T) {
			s := seedBackupMaster(t)
			mount := seedFlowRepository(t, s, "point-1")
			req := protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: !protected}
			if _, err := SetPoolBackupPointProtected(s, req); err != nil {
				t.Fatal(err)
			}
			previous := checkImportedBackupCatalogPointCapacity
			checkImportedBackupCatalogPointCapacity = func(store *Store, points ...protocol.BackupRecoveryPoint) error {
				req.Protected = protected
				if _, err := SetPoolBackupPointProtected(store, req); err != nil {
					return err
				}
				return previous(store, points...)
			}
			t.Cleanup(func() { checkImportedBackupCatalogPointCapacity = previous })
			imported, err := importPoolBackupCatalog(context.Background(), s, "repo-1")
			if err != nil {
				t.Fatal(err)
			}
			point, err := getCatalogPoint(s, "repo-1", "point-1")
			if err != nil || point.Protected != protected || len(imported) != 1 || imported[0].Protected != protected {
				t.Fatalf("latest protection overwritten: catalog=%+v imported=%+v err=%v", point, imported, err)
			}
			if got := vmhost.ReadBackupProtectMarker(filepath.Join(mount, "point-1")); got != protected {
				t.Fatalf("latest marker overwritten: protected=%t want=%t", got, protected)
			}
		})
	}
}

func seedCatalogVerificationPlan(t *testing.T, s *Store) testRestoreVerificationPlan {
	t.Helper()
	vm := *poolVMTestSpec("11111111-1111-4111-8111-111111111111", s.State.HostID)
	run := protocol.BackupRun{OperationID: "test-1", Kind: protocol.BackupKindTestRestore, PoolID: s.State.PoolID,
		RepositoryID: "repo-1", RecoveryPointID: "point-1", NewVMID: vm.ID, HostID: vm.HostID,
		Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {
		PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, CreateOperationID: run.OperationID, State: "stopped", Spec: vm,
	}}}); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareTestRestoreVerification(s, protocol.TestRestoreVerifyRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: vm.ID, Result: protocol.BackupTestResultPassed,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan.VerifiedAt = time.Now().UTC()
	return plan
}

func TestPointMarkerUpdatesPreserveConcurrentVerification(t *testing.T) {
	for _, operation := range []string{"import", "protect", "unprotect"} {
		t.Run(operation, func(t *testing.T) {
			s := seedBackupMaster(t)
			seedFlowRepository(t, s, "point-1")
			if operation != "protect" {
				if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{
					RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: true,
				}); err != nil {
					t.Fatal(err)
				}
			}
			plan := seedCatalogVerificationPlan(t, s)
			previous := writeImportedBackupProtectMarker
			var verified protocol.BackupRecoveryPoint
			writeImportedBackupProtectMarker = func(path string, protected bool) error {
				if !poolBackupAuthorityMu.TryLock() {
					return errors.New("marker update holds the authority mutex")
				}
				poolBackupAuthorityMu.Unlock()
				var err error
				verified, err = RecordTestRestoreVerification(s, plan)
				if err != nil {
					return err
				}
				return previous(path, protected)
			}
			t.Cleanup(func() { writeImportedBackupProtectMarker = previous })
			var err error
			if operation == "import" {
				_, err = importPoolBackupCatalog(context.Background(), s, "repo-1")
			} else {
				_, err = SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{
					RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: operation == "protect",
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			point, err := getCatalogPoint(s, "repo-1", "point-1")
			if err != nil || point.LastTestResult != protocol.BackupTestResultPassed ||
				point.LastTestVMID != plan.Request.TestVMID || !point.LastTestedAt.Equal(verified.LastTestedAt) {
				t.Fatalf("verification overwritten: point=%+v verified=%+v err=%v", point, verified, err)
			}
		})
	}
}

func TestCatalogMarkerAdmissionRefusesOverlappingUnprotectAndPrune(t *testing.T) {
	for _, operation := range []string{"import", "protect"} {
		t.Run(operation, func(t *testing.T) {
			s := seedBackupMaster(t)
			mount := seedFlowRepository(t, s, "point-1")
			if operation == "import" {
				if _, err := SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{
					RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: true,
				}); err != nil {
					t.Fatal(err)
				}
			}
			repo, err := GetPoolBackupRepository(s, "repo-1")
			if err != nil {
				t.Fatal(err)
			}
			backend, err := openBackupRepositoryStorage(s, repo)
			if err != nil {
				t.Fatal(err)
			}
			cmd := protocol.Command{OperationID: "unprotect-during-marker", Kind: "backup-protect",
				BackupProtect: &protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: false}}
			request := BackupRequest{Command: &cmd}
			previous := writeImportedBackupProtectMarker
			entered := false
			writeImportedBackupProtectMarker = func(path string, protected bool) error {
				if entered {
					return previous(path, protected)
				}
				entered = true
				if !poolBackupAuthorityMu.TryLock() {
					return errors.New("marker update holds the authority mutex")
				}
				poolBackupAuthorityMu.Unlock()
				response := acceptBackupRequest(context.Background(), s, request)
				if !errors.Is(response.Err, ErrBackupConflict) {
					return fmt.Errorf("overlapping unprotect admitted: %+v", response)
				}
				if operation == "protect" {
					// Neither the catalog nor the destination is protected yet.
					if err := pruneRecoveryPoint(context.Background(), s, backend, "repo-1", "point-1"); err != nil {
						return err
					}
					if _, err := os.Stat(filepath.Join(mount, "point-1", "manifest.json")); err != nil {
						return fmt.Errorf("reserved point was pruned: %w", err)
					}
				}
				return previous(path, protected)
			}
			t.Cleanup(func() { writeImportedBackupProtectMarker = previous })
			if operation == "import" {
				_, err = importPoolBackupCatalog(context.Background(), s, "repo-1")
			} else {
				_, err = SetPoolBackupPointProtected(s, protocol.ProtectRecoveryPointRequest{
					RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: true,
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if !entered {
				t.Fatal("point mutation did not write its marker")
			}
			writeImportedBackupProtectMarker = previous
			response := acceptBackupRequest(context.Background(), s, request)
			if response.Err != nil || response.Result == nil || response.Result.Status != "succeeded" {
				t.Fatalf("exact unprotect retry did not settle: %+v", response)
			}
			point, err := getCatalogPoint(s, "repo-1", "point-1")
			if err != nil || point.Protected || vmhost.ReadBackupProtectMarker(filepath.Join(mount, "point-1")) {
				t.Fatalf("unprotect retry was overwritten: %+v err=%v", point, err)
			}
		})
	}
}

func TestCatalogRebuildKeepsPointPublishedAndSettledAfterListing(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s, "point-stale")
	if err := os.RemoveAll(filepath.Join(mount, "point-stale")); err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: "new-backup", Kind: protocol.BackupKindBackupNow,
		PoolID: s.State.PoolID, HostID: s.State.HostID, VMID: "vm-1", RepositoryID: "repo-1",
		RecoveryPointID: "point-new", Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	previous := listBackupCatalogRepositoryEntries
	listBackupCatalogRepositoryEntries = func(ctx context.Context, backend backupRepositoryStorage) ([]string, error) {
		entries, err := previous(ctx, backend)
		if err != nil {
			return nil, err
		}
		seedPublishedPoint(t, mount, "repo-1", s.State.PoolID, "point-new")
		manifest, digest, err := backend.StatPoint(ctx, "point-new")
		if err != nil {
			return nil, err
		}
		point := protocol.BackupRecoveryPoint{ID: manifest.RecoveryPointID, RepositoryID: manifest.RepositoryID,
			PoolID: manifest.PoolID, SourceVMID: manifest.SourceVMID, SourceVMName: manifest.SourceVMName,
			CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency, ManifestSHA256: digest}
		run.Stage, run.Status = "published", "succeeded"
		if _, err := commitPoolBackupRun(ctx, s, run, &point); err != nil {
			return nil, err
		}
		return entries, nil
	}
	t.Cleanup(func() { listBackupCatalogRepositoryEntries = previous })
	if _, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-new"); err != nil {
		t.Fatalf("newly settled point was removed: %v", err)
	}
	if settled, err := loadPoolBackupRun(s, run.OperationID); err != nil || settled.Status != "succeeded" {
		t.Fatalf("publication did not settle: %+v err=%v", settled, err)
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-stale"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("unchanged stale point was retained: %v", err)
	}
}

func TestPointMetadataReservationReleasedAfterMarkerFailure(t *testing.T) {
	for _, operation := range []string{"import", "protect"} {
		t.Run(operation, func(t *testing.T) {
			s := seedBackupMaster(t)
			mount := seedFlowRepository(t, s, "point-1")
			req := protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: true}
			if operation == "import" {
				if _, err := SetPoolBackupPointProtected(s, req); err != nil {
					t.Fatal(err)
				}
			}
			mutate := func() error {
				if operation == "import" {
					_, err := importPoolBackupCatalog(context.Background(), s, "repo-1")
					return err
				}
				_, err := SetPoolBackupPointProtected(s, req)
				return err
			}
			previous := writeImportedBackupProtectMarker
			markerErr := errors.New("repository marker write interrupted")
			writeImportedBackupProtectMarker = func(string, bool) error { return markerErr }
			t.Cleanup(func() { writeImportedBackupProtectMarker = previous })
			if err := mutate(); !errors.Is(err, markerErr) {
				t.Fatalf("marker failure was hidden: %v", err)
			}
			writeImportedBackupProtectMarker = previous
			if err := mutate(); err != nil {
				t.Fatalf("failed marker mutation blocked its retry: %v", err)
			}
			point, err := getCatalogPoint(s, "repo-1", "point-1")
			if err != nil || !point.Protected || !vmhost.ReadBackupProtectMarker(filepath.Join(mount, "point-1")) {
				t.Fatalf("marker retry did not preserve protection: %+v err=%v", point, err)
			}
		})
	}
}

// An unchanged reimport must not walk the store once per point. Process read
// counters include unrelated goroutines, so the test compares two sizes: linear
// growth multiplies reads by the size ratio, a per-point walk by far more.
func TestCatalogReimportReadsGrowLinearly(t *testing.T) {
	readCalls := func() uint64 {
		t.Helper()
		raw, err := os.ReadFile("/proc/self/io")
		if errors.Is(err, os.ErrNotExist) {
			t.Skip("process I/O counters are unavailable")
		}
		if err != nil {
			t.Fatal(err)
		}
		var chars, writes, reads uint64
		if _, err := fmt.Sscanf(string(raw), "rchar: %d\nwchar: %d\nsyscr: %d", &chars, &writes, &reads); err != nil {
			t.Fatal(err)
		}
		return reads
	}
	reimportReads := func(count int) uint64 {
		t.Helper()
		s := seedBackupMaster(t)
		mount := seedFlowRepository(t, s)
		for i := range count {
			seedPublishedPoint(t, mount, "repo-1", s.State.PoolID, fmt.Sprintf("point-%03d", i))
		}
		first, err := importPoolBackupCatalog(context.Background(), s, "repo-1")
		if err != nil || len(first) != count {
			t.Fatalf("initial import: %d points, %v", len(first), err)
		}
		before := readCalls()
		second, err := importPoolBackupCatalog(context.Background(), s, "repo-1")
		reads := readCalls() - before
		if err != nil || len(second) != len(first) {
			t.Fatalf("reimport: %d points, %v", len(second), err)
		}
		for i := range first {
			if second[i] != first[i] {
				t.Fatalf("reimport changed point: got %+v want %+v", second[i], first[i])
			}
		}
		return reads
	}
	small, large := reimportReads(16), reimportReads(64)
	t.Logf("unchanged reimport: %d read calls for 16 points, %d for 64", small, large)
	if large > 8*small {
		t.Fatalf("unchanged reimport reads grow faster than linearly: %d for 16 points, %d for 64", small, large)
	}
}

func TestCatalogImportReadmitsNewAndChangedPointsBeforeMarker(t *testing.T) {
	for _, change := range []string{"new", "changed", "concurrent-protection"} {
		t.Run(change, func(t *testing.T) {
			s := seedBackupMaster(t)
			mount := seedFlowRepository(t, s, "point-1")
			req := protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: true}
			point, err := SetPoolBackupPointProtected(s, req)
			if err != nil {
				t.Fatal(err)
			}
			point.PolicyID = ""
			if change == "changed" {
				point.SourceVMName = "Outdated name"
			}
			if err := savePoolBackupCatalogEntry(s, point); err != nil {
				t.Fatal(err)
			}
			if change == "new" {
				if err := os.Remove(filepath.Join(poolBackupCatalogPath(s), point.ID+".json")); err != nil {
					t.Fatal(err)
				}
			}
			previous := checkImportedBackupCatalogPointCapacity
			checkImportedBackupCatalogPointCapacity = func(store *Store, points ...protocol.BackupRecoveryPoint) error {
				if err := previous(store, points...); err != nil {
					return err
				}
				if change == "concurrent-protection" {
					req.Protected = false
					if _, err := SetPoolBackupPointProtected(store, req); err != nil {
						return err
					}
				}
				if err := os.MkdirAll(poolBackupRunsPath(store), 0700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(poolBackupRunsPath(store), "corrupt-run.json"), []byte("{"), 0600)
			}
			t.Cleanup(func() { checkImportedBackupCatalogPointCapacity = previous })
			previousMarker := writeImportedBackupProtectMarker
			markerWritten := false
			writeImportedBackupProtectMarker = func(path string, protected bool) error {
				if path == filepath.Join(mount, point.ID) {
					markerWritten = true
				}
				return previousMarker(path, protected)
			}
			t.Cleanup(func() { writeImportedBackupProtectMarker = previousMarker })
			_, err = importPoolBackupCatalog(context.Background(), s, "repo-1")
			if err == nil {
				t.Fatal("new or changed record bypassed full authority admission")
			}
			if _, err := os.Stat(filepath.Join(poolBackupRunsPath(s), "corrupt-run.json")); err != nil {
				t.Fatalf("bulk admission did not complete before rejection: %v", err)
			}
			if change != "concurrent-protection" && markerWritten {
				t.Fatal("repository marker was changed before fresh admission")
			}
			poolBackupAuthorityMu.Lock()
			busy := backupPointMetadataBusy(s, "repo-1", point.ID)
			poolBackupAuthorityMu.Unlock()
			if busy {
				t.Fatal("failed admission retained the point reservation")
			}
		})
	}
}

func TestCatalogPointLookupValidatesSelectedRecord(t *testing.T) {
	for _, corruption := range []string{"json", "id", "pool", "oversize", "unrelated"} {
		t.Run(corruption, func(t *testing.T) {
			s := seedBackupMaster(t)
			seedFlowRepository(t, s, "point-1")
			point, err := getCatalogPoint(s, "repo-1", "point-1")
			if err != nil {
				t.Fatal(err)
			}
			original := point
			if corruption == "id" {
				point.ID = "point-other"
			}
			if corruption == "pool" {
				point.PoolID = "pool-other"
			}
			raw, err := json.Marshal(point)
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "json":
				raw = []byte("{")
			case "oversize":
				for len(raw) <= protocol.PoolBackupPageLimit {
					raw = append(raw, ' ')
				}
			case "unrelated":
				if err := os.WriteFile(filepath.Join(poolBackupCatalogPath(s), "point-unrelated.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(poolBackupCatalogPath(s), "point-1.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := getCatalogPoint(s, "repo-1", "point-1")
			if corruption == "unrelated" {
				if err != nil || got != original {
					t.Fatalf("selected lookup read unrelated record: %+v err=%v", got, err)
				}
			} else if err == nil {
				t.Fatalf("accepted corrupt selected record: %+v", got)
			}
			if _, err := getCatalogPoint(s, "repo-1", "../point-1"); !errors.Is(err, ErrBackupInvalid) {
				t.Fatalf("unsafe record path accepted: %v", err)
			}
		})
	}
}

// Protection is written to the repository before the catalog, so a marker the
// catalog does not know belongs to a protect that never finished.
func TestCatalogImportAdoptsRepositoryProtectionMarker(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s, "point-1")
	if point, err := getCatalogPoint(s, "repo-1", "point-1"); err != nil || point.Protected {
		t.Fatalf("seed point already protected: %+v %v", point, err)
	}
	if err := vmhost.WriteBackupProtectMarker(filepath.Join(mount, "point-1"), true); err != nil {
		t.Fatal(err)
	}
	imported, err := importPoolBackupCatalog(context.Background(), s, "repo-1")
	if err != nil {
		t.Fatal(err)
	}
	point, err := getCatalogPoint(s, "repo-1", "point-1")
	if err != nil || !point.Protected || len(imported) != 1 || !imported[0].Protected {
		t.Fatalf("repository marker not adopted: catalog=%+v imported=%+v err=%v", point, imported, err)
	}
	if !vmhost.ReadBackupProtectMarker(filepath.Join(mount, "point-1")) {
		t.Fatal("adopting the marker removed it")
	}
}

// An unprotect that completes between the scan and the import must win over the
// marker the scan saw.
func TestCatalogImportKeepsUnprotectCompletedAfterScan(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s, "point-1")
	req := protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: true}
	if _, err := SetPoolBackupPointProtected(s, req); err != nil {
		t.Fatal(err)
	}
	previous := checkImportedBackupCatalogPointCapacity
	checkImportedBackupCatalogPointCapacity = func(store *Store, points ...protocol.BackupRecoveryPoint) error {
		req.Protected = false
		if _, err := SetPoolBackupPointProtected(store, req); err != nil {
			return err
		}
		return previous(store, points...)
	}
	t.Cleanup(func() { checkImportedBackupCatalogPointCapacity = previous })
	if _, err := importPoolBackupCatalog(context.Background(), s, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if point, err := getCatalogPoint(s, "repo-1", "point-1"); err != nil || point.Protected {
		t.Fatalf("import reverted an unprotect made after its scan: %+v %v", point, err)
	}
	if vmhost.ReadBackupProtectMarker(filepath.Join(mount, "point-1")) {
		t.Fatal("import restored a removed marker")
	}
}

func TestCatalogRebuildAcceptsPointPublishedAfterScan(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s, "point-1")
	previous := listBackupCatalogRepositoryEntries
	published := false
	listBackupCatalogRepositoryEntries = func(ctx context.Context, backend backupRepositoryStorage) ([]string, error) {
		if !published {
			published = true
			seedPublishedPoint(t, mount, "repo-1", s.State.PoolID, "point-2")
		}
		return previous(ctx, backend)
	}
	t.Cleanup(func() { listBackupCatalogRepositoryEntries = previous })
	imported, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1")
	if err != nil {
		t.Fatalf("a point published after the scan failed the rebuild: %v", err)
	}
	if len(imported) != 2 {
		t.Fatalf("rebuild missed the new point: %+v", imported)
	}
	if _, err := getCatalogPoint(s, "repo-1", "point-2"); err != nil {
		t.Fatalf("new point not cataloged: %v", err)
	}
}

func TestCatalogRebuildStillReportsUnreadablePoint(t *testing.T) {
	s := seedBackupMaster(t)
	mount := seedFlowRepository(t, s, "point-1")
	if err := os.MkdirAll(filepath.Join(mount, "point-broken"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := RebuildPoolBackupCatalogFromRepository(context.Background(), s, "repo-1")
	if err == nil || !strings.Contains(err.Error(), "point-broken") {
		t.Fatalf("unreadable point not reported by name: %v", err)
	}
}
