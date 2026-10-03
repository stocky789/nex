// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func backupAuthorityCleanupFixture(count int) protocol.BackupOrphanCleanup {
	cleanup := protocol.BackupOrphanCleanup{
		OperationID: "cleanup-large", Status: "failed", Error: "some recovery points could not be removed",
		UpdatedAt: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		Request: protocol.OrphanCleanupRequest{RepositoryID: "repo-1", VMIDs: []string{"vm-gone"},
			RecoveryPoints: count, ProtectedPoints: count / 3, IncludeProtected: true,
			Confirmation: protocol.OrphanCleanupConfirmation(count, count/3, true)},
		Points: make([]protocol.BackupOrphanPointResult, count),
	}
	for i := range cleanup.Points {
		cleanup.Points[i] = protocol.BackupOrphanPointResult{RecoveryPointID: fmt.Sprintf("point-%05d", i), VMID: "vm-gone"}
		switch i % 3 {
		case 0:
			cleanup.Points[i].Status = "deleted"
		case 1:
			cleanup.Points[i].Status, cleanup.Points[i].Reason = "skipped", "unfinished restore still pins this recovery point"
		case 2:
			cleanup.Points[i].Status, cleanup.Points[i].Reason = "failed", "repository could not remove the recovery point"
		}
	}
	return cleanup
}

func TestBackupAuthorityFiveThousandCleanupPointsTransferAndAdopt(t *testing.T) {
	lab := promotionLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	cleanup := backupAuthorityCleanupFixture(5000)
	if err := saveOrphanCleanup(master, cleanup); err != nil {
		t.Fatal(err)
	}
	pushReplicas(t, master)
	stored, err := loadPoolReplica(member)
	if err != nil || stored == nil || stored.Replica.BackupManifest == nil {
		t.Fatalf("cleanup replica = %#v, %v", stored, err)
	}
	manifest := stored.Replica.BackupManifest
	if manifest.OrphanCleanups != 1 || manifest.PageCount < 2 {
		t.Fatalf("cleanup was not split across signed pages: %+v", manifest)
	}
	for index := range manifest.PageCount {
		if page, err := readPoolBackupPage(member, manifest, index); err != nil || len(page) > protocol.PoolBackupPageLimit {
			t.Fatalf("cleanup page %d: size=%d err=%v", index, len(page), err)
		}
	}
	recovered, err := loadPoolBackupAuthorityPages(member, manifest)
	if err != nil || len(recovered.OrphanCleanups) != 1 || !reflect.DeepEqual(recovered.OrphanCleanups[0].Points, cleanup.Points) {
		t.Fatalf("member did not retain all point outcomes: %v", err)
	}
	wrong := *manifest
	wrong.OrphanCleanups++
	if err := walkPoolBackupAuthorityPages(member, &wrong, nil); err == nil {
		t.Fatal("mismatched cleanup record count was accepted")
	}
	run := protocol.BackupRun{OperationID: "run-after-cleanup", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatalf("persisted cleanup blocked further admission: %v", err)
	}
	pushReplicas(t, master)
	lab.powerOff("master-1")
	fakeFence(t, observedPowerOff(0))
	result := ExecutePoolPromotion(context.Background(), member, promoteCommand("op-large-cleanup-promote", "member-1"))
	if result.Status != "succeeded" {
		t.Fatalf("cleanup promotion = %+v", result)
	}
	adopted, err := loadOrphanCleanup(member, cleanup.OperationID)
	if err != nil || adopted.OperationID != cleanup.OperationID || adopted.Status != cleanup.Status || adopted.Error != cleanup.Error ||
		!reflect.DeepEqual(adopted.Request, cleanup.Request) || !reflect.DeepEqual(adopted.Points, cleanup.Points) {
		t.Fatalf("adoption changed cleanup outcomes: %v", err)
	}
	run.OperationID = "run-after-adoption"
	if err := savePoolBackupRun(member, run); err != nil {
		t.Fatalf("adopted cleanup blocked further admission: %v", err)
	}
}

func TestBackupAuthorityCleanupRejectsInvalidFrames(t *testing.T) {
	cleanup := backupAuthorityCleanupFixture(1)
	point := cleanup.Points[0]
	cleanup.Points = nil
	header := poolBackupOrphanCleanupHeader{Cleanup: cleanup, PointCount: 1}
	run := protocol.BackupRun{OperationID: "run-interleaved", Kind: protocol.BackupKindBackupNow,
		PoolID: "pool-1", RepositoryID: "repo-1", Stage: "failed", Status: "failed", CreatedAt: cleanup.UpdatedAt}
	type frame struct {
		kind byte
		body any
	}
	invalidPoint := point
	invalidPoint.Status = "unknown"
	inline := header
	inline.Cleanup.Points = []protocol.BackupOrphanPointResult{point}
	negative := header
	negative.PointCount = -1
	huge := header
	huge.PointCount = int(^uint(0) >> 1) // No preallocation may trust an unverified header count.
	for name, frames := range map[string][]frame{
		"missing point":        {{7, header}},
		"extra point":          {{7, header}, {8, point}, {8, point}},
		"point without header": {{8, point}},
		"interleaved record":   {{7, header}, {3, run}},
		"inline points":        {{7, inline}},
		"negative count":       {{7, negative}},
		"missing count":        {{7, map[string]any{"cleanup": cleanup}}},
		"untrusted huge count": {{7, huge}, {8, point}},
		"invalid point":        {{7, header}, {8, invalidPoint}},
		"duplicate cleanup":    {{7, header}, {8, point}, {7, header}, {8, point}},
		"old kind-7 format":    {{7, backupAuthorityCleanupFixture(1)}},
	} {
		t.Run(name, func(t *testing.T) {
			s := seedBackupMaster(t)
			var pages [][]byte
			encoder := &poolBackupEncoder{whole: sha256.New(), emit: func(page []byte) error {
				pages = append(pages, append([]byte(nil), page...))
				return nil
			}}
			for _, frame := range frames {
				if err := encoder.frame(frame.kind, frame.body); err != nil {
					t.Fatal(err)
				}
			}
			manifest, err := encoder.finish()
			if err != nil {
				t.Fatal(err)
			}
			for index, page := range pages {
				path := poolBackupPageFile(s, manifest.SHA256, index)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := vmhost.AtomicWrite(path, page, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := walkPoolBackupAuthorityPages(s, manifest, nil); err == nil {
				t.Fatal("malformed cleanup with a matching hash and byte count was accepted")
			}
			if _, _, _, err := stagePoolBackupAuthorityActivationPages(s, manifest, 2); err == nil {
				t.Fatal("malformed cleanup was adopted")
			}
		})
	}
}

func TestBackupAuthorityCleanupPreservesEmptyAndMaximumPointOutcomes(t *testing.T) {
	s := seedBackupMaster(t)
	cleanup := backupAuthorityCleanupFixture(1)
	cleanup.Points[0].Reason = strings.Repeat("\x00", 4096)
	empty := cleanup
	empty.OperationID, empty.Points = "cleanup-empty", nil
	auth := &protocol.PoolBackupAuthority{OrphanCleanups: []protocol.BackupOrphanCleanup{empty, cleanup}}
	manifest, err := stagePoolBackupAuthority(s, auth)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadPoolBackupAuthorityPages(s, manifest)
	if err != nil || !reflect.DeepEqual(got.OrphanCleanups, auth.OrphanCleanups) {
		t.Fatalf("cleanup boundary outcomes changed: %v", err)
	}
}
