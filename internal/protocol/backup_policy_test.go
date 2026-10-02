// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestBackupPolicyRequiresValidCaptureAndIANA(t *testing.T) {
	p := BackupPolicy{
		ID: "pol-1", ScheduleID: "sch-1", PoolID: "pool-1", Name: "Nightly", Enabled: true,
		RepositoryID: "repo-1", CaptureMode: BackupCaptureStopped, TimeZone: "UTC",
		Rule:  BackupScheduleRule{Frequency: BackupScheduleDaily, Hour: 2, Minute: 30},
		VMIDs: []string{"vm-1"}, Retention: BackupRetention{KeepLatest: 7}, Revision: 1,
	}
	if err := p.Valid(); err != nil {
		t.Fatal(err)
	}
	p.CaptureMode = BackupCaptureOnline
	if err := p.Valid(); err != nil {
		t.Fatal(err)
	}
	p.CaptureMode = "online"
	if err := p.Valid(); err == nil {
		t.Fatal("alias online is not a capture mode")
	}
	p.CaptureMode = BackupCaptureStopped
	p.TimeZone = "Not/AZone"
	if err := p.Valid(); err == nil {
		t.Fatal("invalid time zone must be refused")
	}
}

func TestBackupPolicyValidAllowsOmittedPoolID(t *testing.T) {
	// Core-local upsert omits pool_id; agent fills from enrollment before durable write.
	p := BackupPolicy{
		ID: "pol-1", ScheduleID: "sch-1", Name: "phase6-lab-policy", Enabled: true, Paused: true,
		RepositoryID: "repo-1", CaptureMode: BackupCaptureStopped, TimeZone: "UTC",
		Rule:  BackupScheduleRule{Frequency: BackupScheduleDaily, Hour: 3, Minute: 15},
		VMIDs: []string{"6199dc4e-dd2a-45c2-a5d6-9c9f76304b43"}, Retention: BackupRetention{KeepLatest: 3}, Revision: 1,
	}
	if err := p.Valid(); err != nil {
		t.Fatalf("omitted pool_id must be accepted on request shape: %v", err)
	}
	p.PoolID = strings.Repeat("x", 129)
	if err := p.Valid(); err == nil {
		t.Fatal("oversized pool_id must be refused")
	}
}

func TestBackupAuthorityPinsPermitSamePointButRequireUniqueOperations(t *testing.T) {
	stamp := time.Now().UTC()
	first := BackupRestorePin{RecoveryPointID: "point-1", OperationID: "restore-1", Kind: BackupPinKindNew, CreatedAt: stamp}
	second := BackupRestorePin{RecoveryPointID: "point-1", OperationID: "restore-2", Kind: BackupPinKindTest, CreatedAt: stamp}
	authority := PoolBackupAuthority{Pins: []BackupRestorePin{first, second}}
	if err := authority.Valid(); err != nil {
		t.Fatalf("distinct restores sharing a point were rejected: %v", err)
	}
	authority.Pins[1].OperationID = first.OperationID
	if err := authority.Valid(); err == nil {
		t.Fatal("duplicate restore operation was accepted")
	}
}

func TestBackupAuthorityRejectsMalformedOrphanCleanup(t *testing.T) {
	stamp := time.Now().UTC()
	valid := BackupOrphanCleanup{
		OperationID: "orph-1",
		Request: OrphanCleanupRequest{
			RepositoryID: "repo-1", VMIDs: []string{"vm-gone"}, Confirmation: "delete 1 recovery points",
			RecoveryPoints: 1,
		},
		Status: "succeeded", Points: []BackupOrphanPointResult{
			{RecoveryPointID: "point-1", VMID: "vm-gone", Status: "deleted"},
		},
		UpdatedAt: stamp,
	}
	if err := (PoolBackupAuthority{OrphanCleanups: []BackupOrphanCleanup{valid}}).Valid(); err != nil {
		t.Fatalf("valid orphan cleanup refused: %v", err)
	}
	for name, mutate := range map[string]func(*BackupOrphanCleanup){
		"empty request":          func(c *BackupOrphanCleanup) { c.Request = OrphanCleanupRequest{} },
		"bad status":             func(c *BackupOrphanCleanup) { c.Status = "pending" },
		"oversized error":        func(c *BackupOrphanCleanup) { c.Error = strings.Repeat("e", 4097) },
		"zero updated_at":        func(c *BackupOrphanCleanup) { c.UpdatedAt = time.Time{} },
		"bad point status":       func(c *BackupOrphanCleanup) { c.Points[0].Status = "running" },
		"missing point vmid":     func(c *BackupOrphanCleanup) { c.Points[0].VMID = "" },
		"oversized point reason": func(c *BackupOrphanCleanup) { c.Points[0].Reason = strings.Repeat("r", 4097) },
	} {
		cleanup := valid
		cleanup.Points = append([]BackupOrphanPointResult(nil), valid.Points...)
		mutate(&cleanup)
		if err := (PoolBackupAuthority{OrphanCleanups: []BackupOrphanCleanup{cleanup}}).Valid(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	dup := PoolBackupAuthority{OrphanCleanups: []BackupOrphanCleanup{valid, valid}}
	if err := dup.Valid(); err == nil {
		t.Fatal("duplicate orphan cleanup operation was accepted")
	}
}

func TestBackupScheduleDSTGapIsSkippedAndRepeatRunsOnce(t *testing.T) {
	// America/New_York 2026-03-08 spring forward: 02:30 local does not exist.
	rule := BackupScheduleRule{Frequency: BackupScheduleDaily, Hour: 2, Minute: 30}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	gapDay := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
	if _, ok := BackupScheduleWallTime(rule, loc, gapDay); ok {
		t.Fatal("DST gap must not invent a wall time")
	}
	from := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	next, err := NextBackupOccurrence(rule, "America/New_York", from)
	if err != nil {
		t.Fatal(err)
	}
	// Next after 7th noon should be 7th 02:30 local (=07:30 UTC in EST), already passed from noon UTC on 7th?
	// 7 Mar 2026 02:30 EST = 07:30 UTC. from is 12:00 UTC on 7th, so next is 8th — but 8th is gap, so 9th.
	want := time.Date(2026, 3, 9, 2, 30, 0, 0, loc).UTC()
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s (gap day skipped)", next, want)
	}

	// Fall back 2026-11-01: 01:30 occurs twice; BackupScheduleWallTime returns one UTC.
	fallDay := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	first, ok := BackupScheduleWallTime(rule, loc, fallDay)
	if !ok {
		t.Fatal("fall-back day must produce one occurrence")
	}
	// Go's Date in the fall-back ambiguity picks the earlier (EDT) or later depending on version;
	// we only require a single stable UTC for that calendar day.
	again, ok := BackupScheduleWallTime(rule, loc, fallDay)
	if !ok || !again.Equal(first) {
		t.Fatalf("repeated local time must run once: %s vs %s", first, again)
	}
}

func TestBackupOccurrenceIDIsStableAndUnique(t *testing.T) {
	at := time.Date(2026, 9, 27, 2, 30, 0, 0, time.UTC)
	a := BackupOccurrenceID("sch-1", "vm-1", at)
	b := BackupOccurrenceID("sch-1", "vm-1", at)
	c := BackupOccurrenceID("sch-1", "vm-2", at)
	if a != b || !ValidSnapshotOperationID(a) {
		t.Fatalf("occurrence id unstable or invalid: %q", a)
	}
	if a == c {
		t.Fatal("different VMs must not share occurrence id")
	}
}

func TestRetentionEligiblePruneProtectsPinsAndAge(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	points := []BackupRecoveryPoint{
		{ID: "p1", SourceVMID: "vm-1", CapturedAt: now.Add(-24 * time.Hour), Protected: true},
		{ID: "p2", SourceVMID: "vm-1", CapturedAt: now.Add(-48 * time.Hour)},
		{ID: "p3", SourceVMID: "vm-1", CapturedAt: now.Add(-72 * time.Hour)},
		{ID: "p4", SourceVMID: "vm-1", CapturedAt: now.Add(-96 * time.Hour)},
		{ID: "p5", SourceVMID: "vm-1", CapturedAt: now.Add(-1 * time.Hour)},
	}
	ret := BackupRetention{KeepLatest: 1, ProtectNewerThan: 2 * time.Hour}
	pinned := map[string]bool{"p3": true}
	keep, prune := RetentionEligiblePrune(points, "vm-1", ret, now, pinned)
	kept := map[string]bool{}
	for _, p := range keep {
		kept[p.ID] = true
	}
	if !kept["p1"] || !kept["p3"] || !kept["p5"] {
		t.Fatalf("protected/pinned/age must stay: keep=%v", keep)
	}
	// Among unprotected older: p2 newest eligible after age-protected p5; keep_latest=1 keeps p2, prunes p4.
	if !kept["p2"] {
		t.Fatalf("keep_latest should retain newest eligible: keep=%v", keep)
	}
	pruned := map[string]bool{}
	for _, p := range prune {
		pruned[p.ID] = true
	}
	if !pruned["p4"] || pruned["p1"] || pruned["p3"] || pruned["p5"] {
		t.Fatalf("prune set wrong: %v", prune)
	}
}

func TestBackupRunAcceptsScheduledMissedStatus(t *testing.T) {
	run := BackupRun{
		OperationID: "bso-abc", Kind: BackupKindScheduled, PoolID: "pool-1", RepositoryID: "repo-1",
		OccurrenceID: "bso-abc", Stage: "missed", Status: "missed", CreatedAt: time.Now().UTC(),
	}
	if err := run.Valid(); err != nil {
		t.Fatal(err)
	}
}

// Retention rules are a union: a point survives if any rule keeps it, and
// age-protected points do not count toward keep_latest.
func TestRetentionRulesAreAUnion(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	point := func(id string, age time.Duration, protected bool) BackupRecoveryPoint {
		return BackupRecoveryPoint{ID: id, SourceVMID: "vm-1", CapturedAt: now.Add(-age), Protected: protected}
	}
	points := []BackupRecoveryPoint{
		point("fresh", time.Hour, false), point("newest-old", 48*time.Hour, false),
		point("older", 72*time.Hour, false), point("marked", 96*time.Hour, true), point("oldest", 120*time.Hour, false),
	}
	_, prune := RetentionEligiblePrune(points, "vm-1", BackupRetention{KeepLatest: 1, ProtectNewerThan: 24 * time.Hour}, now, nil)
	var ids []string
	for _, p := range prune {
		ids = append(ids, p.ID)
	}
	if strings.Join(ids, ",") != "older,oldest" {
		t.Fatalf("pruned %v; want the two unprotected points beyond keep_latest, which skips the age-protected one", ids)
	}
}

func TestPoolBackupManifestBoundsRecordCountsByBytes(t *testing.T) {
	const totalBytes = 500_000_000
	valid := PoolBackupManifest{SHA256: strings.Repeat("a", 64), TotalBytes: totalBytes, PageCount: totalBytes/PoolBackupPageLimit + 1}
	// Retained histories are not capped at a fixed number of records.
	valid.History, valid.Catalog = 150_000, 150_000
	if err := valid.Valid(); err != nil {
		t.Fatalf("large retained record counts were refused: %v", err)
	}
	for name, edit := range map[string]func(*PoolBackupManifest){
		"history beyond the bytes": func(m *PoolBackupManifest) { m.History = m.TotalBytes/5 + 1 },
		"counts that sum past bytes": func(m *PoolBackupManifest) {
			m.History, m.Catalog, m.Pins = m.TotalBytes/10, m.TotalBytes/10, m.TotalBytes/10
		},
		"negative orphan cleanups":     func(m *PoolBackupManifest) { m.OrphanCleanups = -1 },
		"more repositories than cap":   func(m *PoolBackupManifest) { m.Repositories = PoolBackupMaxRepositories + 1 },
		"more definitions than cap":    func(m *PoolBackupManifest) { m.Definitions = PoolBackupMaxDefinitions + 1 },
		"pages larger than page limit": func(m *PoolBackupManifest) { m.PageCount = 1 },
	} {
		m := valid
		edit(&m)
		if m.Valid() == nil {
			t.Fatalf("%s: manifest accepted: %+v", name, m)
		}
	}
}
