// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	BackupKindScheduled    = "backup-scheduled"
	BackupKindPolicyRunNow = "backup-policy-run-now"
	BackupScheduleDaily    = "daily"
	BackupScheduleWeekly   = "weekly"
	BackupMissedCatchUpCap = 8
)

// BackupScheduleRule is one local-time cadence for a policy schedule.
type BackupScheduleRule struct {
	Frequency string `json:"frequency"`          // daily | weekly
	Hour      int    `json:"hour"`               // 0-23 local
	Minute    int    `json:"minute"`             // 0-59 local
	Weekdays  []int  `json:"weekdays,omitempty"` // 0=Sun..6=Sat; required for weekly
}

func (r BackupScheduleRule) Valid() error {
	if r.Frequency != BackupScheduleDaily && r.Frequency != BackupScheduleWeekly {
		return errors.New("backup schedule frequency is invalid")
	}
	if r.Hour < 0 || r.Hour > 23 || r.Minute < 0 || r.Minute > 59 {
		return errors.New("backup schedule time is invalid")
	}
	if r.Frequency == BackupScheduleWeekly {
		if len(r.Weekdays) == 0 || len(r.Weekdays) > 7 {
			return errors.New("backup schedule weekdays are required")
		}
		seen := map[int]bool{}
		for _, d := range r.Weekdays {
			if d < 0 || d > 6 || seen[d] {
				return errors.New("backup schedule weekdays are invalid")
			}
			seen[d] = true
		}
	} else if len(r.Weekdays) != 0 {
		return errors.New("daily backup schedule must not set weekdays")
	}
	return nil
}

// BackupRetention keeps successful points per VM; age protection is optional.
type BackupRetention struct {
	KeepLatest       int           `json:"keep_latest"`
	ProtectNewerThan time.Duration `json:"protect_newer_than,omitempty"`
}

func (r BackupRetention) Valid() error {
	if r.KeepLatest < 1 || r.KeepLatest > 365 {
		return errors.New("backup retention keep_latest must be 1-365")
	}
	if r.ProtectNewerThan < 0 || r.ProtectNewerThan > 365*24*time.Hour {
		return errors.New("backup retention protect_newer_than is invalid")
	}
	return nil
}

// BackupPolicyLimits are optional advanced throughput bounds.
type BackupPolicyLimits struct {
	MaxConcurrentPerHost int `json:"max_concurrent_per_host,omitempty"`
	MaxConcurrentPerRepo int `json:"max_concurrent_per_repo,omitempty"`
}

func (l BackupPolicyLimits) Valid() error {
	if l.MaxConcurrentPerHost < 0 || l.MaxConcurrentPerHost > 32 ||
		l.MaxConcurrentPerRepo < 0 || l.MaxConcurrentPerRepo > 32 {
		return errors.New("backup policy limits are invalid")
	}
	return nil
}

// BackupPolicyVMOverride adjusts retention or capture for one VM in a policy.
type BackupPolicyVMOverride struct {
	VMID        string           `json:"vm_id"`
	Retention   *BackupRetention `json:"retention,omitempty"`
	CaptureMode string           `json:"capture_mode,omitempty"`
}

func (o BackupPolicyVMOverride) Valid() error {
	if o.VMID == "" || len(o.VMID) > 128 {
		return errors.New("backup policy VM override identity is invalid")
	}
	if o.Retention != nil {
		if err := o.Retention.Valid(); err != nil {
			return err
		}
	}
	if o.CaptureMode != "" && !ValidBackupConsistency(o.CaptureMode) {
		return errors.New("backup policy override capture_mode is invalid")
	}
	return nil
}

// BackupPolicy is one master-owned job/schedule definition under pool-backup-policies.json.
type BackupPolicy struct {
	ID               string                   `json:"id"`
	ScheduleID       string                   `json:"schedule_id"`
	PoolID           string                   `json:"pool_id"`
	Name             string                   `json:"name"`
	Enabled          bool                     `json:"enabled"`
	Paused           bool                     `json:"paused"`
	RepositoryID     string                   `json:"repository_id"`
	CaptureMode      string                   `json:"capture_mode"`
	TimeZone         string                   `json:"time_zone"`
	Rule             BackupScheduleRule       `json:"rule"`
	VMIDs            []string                 `json:"vm_ids"`
	Retention        BackupRetention          `json:"retention"`
	Limits           BackupPolicyLimits       `json:"limits,omitempty"`
	Overrides        []BackupPolicyVMOverride `json:"overrides,omitempty"`
	Revision         uint64                   `json:"revision"`
	ExpectedRevision *uint64                  `json:"expected_revision,omitempty"`
	NextDispatchAt   time.Time                `json:"next_dispatch_at,omitempty"`
	LastDispatchAt   time.Time                `json:"last_dispatch_at,omitempty"`
	CreatedAt        time.Time                `json:"created_at,omitempty"`
	UpdatedAt        time.Time                `json:"updated_at,omitempty"`
}

func (p BackupPolicy) Valid() error {
	// pool_id may be omitted on create/update requests; Core fills it from the
	// enrolled master identity before durable write (same as BackupRepository).
	if !ValidSnapshotOperationID(p.ID) || !ValidSnapshotOperationID(p.ScheduleID) ||
		(p.PoolID != "" && len(p.PoolID) > 128) || !ValidResourceName(p.Name) ||
		!ValidSnapshotOperationID(p.RepositoryID) || p.Revision == 0 {
		return errors.New("backup policy identity is invalid")
	}
	if !ValidBackupConsistency(p.CaptureMode) {
		return errors.New("backup policy capture_mode is invalid")
	}
	if _, err := time.LoadLocation(p.TimeZone); err != nil || p.TimeZone == "" || len(p.TimeZone) > 64 {
		return errors.New("backup policy time_zone must be a valid IANA name")
	}
	if err := p.Rule.Valid(); err != nil {
		return err
	}
	if err := p.Retention.Valid(); err != nil {
		return err
	}
	if err := p.Limits.Valid(); err != nil {
		return err
	}
	if len(p.VMIDs) == 0 || len(p.VMIDs) > 256 {
		return errors.New("backup policy must select 1-256 VMs")
	}
	seen := map[string]bool{}
	for _, id := range p.VMIDs {
		if id == "" || len(id) > 128 || seen[id] {
			return errors.New("backup policy VM selection is invalid")
		}
		seen[id] = true
	}
	seenOverride := map[string]bool{}
	for _, o := range p.Overrides {
		if o.Valid() != nil || seenOverride[o.VMID] || !seen[o.VMID] {
			return errors.New("backup policy override is invalid")
		}
		seenOverride[o.VMID] = true
	}
	return nil
}

// BackupOccurrenceID is the durable unique schedule-occurrence/VM operation identity.
func BackupOccurrenceID(scheduleID, vmID string, occurrenceUTC time.Time) string {
	sum := sha256.Sum256([]byte(scheduleID + "\x00" + vmID + "\x00" + occurrenceUTC.UTC().Format(time.RFC3339)))
	return "bso-" + hex.EncodeToString(sum[:14])
}

// BackupScheduleWallTime is the intended local wall clock for a rule.
func BackupScheduleWallTime(rule BackupScheduleRule, loc *time.Location, day time.Time) (time.Time, bool) {
	y, m, d := day.In(loc).Date()
	local := time.Date(y, m, d, rule.Hour, rule.Minute, 0, 0, loc)
	// Nonexistent local time (DST spring-forward gap): wall clock does not exist.
	if local.Hour() != rule.Hour || local.Minute() != rule.Minute {
		return time.Time{}, false
	}
	return local.UTC(), true
}

func weekdayAllowed(rule BackupScheduleRule, day time.Weekday) bool {
	if rule.Frequency == BackupScheduleDaily {
		return true
	}
	for _, d := range rule.Weekdays {
		if time.Weekday(d) == day {
			return true
		}
	}
	return false
}

// NextBackupOccurrence returns the next UTC occurrence at or after from (inclusive when exact).
// A nonexistent local time is skipped (caller records missed when that slot was due).
// A repeated local time (fall-back) yields one UTC instant.
func NextBackupOccurrence(rule BackupScheduleRule, timeZone string, from time.Time) (time.Time, error) {
	if err := rule.Valid(); err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		return time.Time{}, err
	}
	from = from.UTC()
	cursor := from.In(loc)
	startDay := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 400; i++ {
		day := startDay.AddDate(0, 0, i)
		if !weekdayAllowed(rule, day.Weekday()) {
			continue
		}
		occ, ok := BackupScheduleWallTime(rule, loc, day)
		if !ok {
			continue
		}
		if !occ.Before(from) {
			return occ, nil
		}
	}
	return time.Time{}, errors.New("backup schedule next occurrence not found")
}

// BackupOccurrenceDueSlots lists due UTC occurrences in (after, until] for catch-up accounting.
// Cap limits how many trailing slots are returned so downtime never storms.
func BackupOccurrenceDueSlots(rule BackupScheduleRule, timeZone string, after, until time.Time, cap int) ([]time.Time, error) {
	if cap <= 0 {
		cap = BackupMissedCatchUpCap
	}
	until = until.UTC()
	after = after.UTC()
	if !until.After(after) {
		return nil, nil
	}
	// Only the latest bounded window matters: older occurrences were missed during
	// downtime and must not be dispatched in a later scheduler pass.
	windowStart := until.AddDate(0, 0, -7*(cap+1))
	if after.Before(windowStart) {
		after = windowStart
	}
	var all []time.Time
	cursor := after.Add(time.Second)
	for {
		next, err := NextBackupOccurrence(rule, timeZone, cursor)
		if err != nil {
			return nil, err
		}
		if next.After(until) {
			break
		}
		all = append(all, next)
		cursor = next.Add(time.Second)
	}
	if len(all) > cap {
		all = all[len(all)-cap:]
	}
	return all, nil
}

// RetentionEligiblePrune lists points that would be pruned under the policy for one VM.
// Protected points, active restore pins and points newer than ProtectNewerThan stay.
// Failed new backups must not call this for prior points.
func RetentionEligiblePrune(points []BackupRecoveryPoint, vmID string, retention BackupRetention, now time.Time, pinned map[string]bool) (keep, prune []BackupRecoveryPoint) {
	var forVM []BackupRecoveryPoint
	for _, p := range points {
		if p.SourceVMID == vmID {
			forVM = append(forVM, p)
		}
	}
	sort.Slice(forVM, func(i, j int) bool {
		if !forVM[i].CapturedAt.Equal(forVM[j].CapturedAt) {
			return forVM[i].CapturedAt.After(forVM[j].CapturedAt)
		}
		return forVM[i].ID < forVM[j].ID
	})
	var protected, eligible []BackupRecoveryPoint
	for _, p := range forVM {
		ageProtected := retention.ProtectNewerThan > 0 && !p.CapturedAt.IsZero() && now.Sub(p.CapturedAt) < retention.ProtectNewerThan
		if p.Protected || pinned[p.ID] || ageProtected {
			protected = append(protected, p)
			continue
		}
		eligible = append(eligible, p)
	}
	keep = append(keep, protected...)
	for i, p := range eligible {
		if i < retention.KeepLatest {
			keep = append(keep, p)
		} else {
			prune = append(prune, p)
		}
	}
	return keep, prune
}

// PoolBackupAuthority is the bounded replica section for master takeover of backup
// repositories, job/schedule definitions, retained runs, and catalog index entries.
// Full NFS recovery-point payloads are never embedded; catalog carries index only.
type PoolBackupAuthority struct {
	Repositories     []BackupRepository      `json:"repositories,omitempty"`
	Definitions      []BackupPolicy          `json:"definitions,omitempty"`
	History          []BackupRun             `json:"history,omitempty"`
	Catalog          []BackupRecoveryPoint   `json:"catalog,omitempty"`
	Pins             []BackupRestorePin      `json:"pins,omitempty"`
	MetadataReceipts []BackupMetadataReceipt `json:"metadata_receipts,omitempty"`
	// Compact refs stay for observation and older readers; derived from Definitions/History.
	Policies []BackupPolicyRef `json:"policies,omitempty"`
	Runs     []BackupRunRef    `json:"runs,omitempty"`
}

const PoolBackupPageLimit = 256 << 10
const PoolBackupBundleLimit = 64 << 20

// Core refuses more repositories or policies than these.
const (
	PoolBackupMaxRepositories = 256
	PoolBackupMaxDefinitions  = 1000
)

// PoolBackupManifest binds every byte of a paged authority to the signed replica.
type PoolBackupManifest struct {
	Version          int      `json:"version,omitempty"`
	SHA256           string   `json:"sha256"`
	PageCount        int      `json:"page_count,omitempty"`
	PageSHA256       []string `json:"page_sha256"`
	PageBytes        []int    `json:"page_bytes"`
	TotalBytes       int      `json:"total_bytes"`
	Repositories     int      `json:"repositories"`
	Definitions      int      `json:"definitions"`
	History          int      `json:"history"`
	Catalog          int      `json:"catalog"`
	Pins             int      `json:"pins"`
	MetadataReceipts int      `json:"metadata_receipts"`
}

func (m PoolBackupManifest) Valid() error {
	if !lowerSHA256(m.SHA256) || m.TotalBytes <= 0 ||
		m.Repositories < 0 || m.Repositories > PoolBackupMaxRepositories || m.Definitions < 0 || m.Definitions > PoolBackupMaxDefinitions ||
		m.History < 0 || m.Catalog < 0 || m.Pins < 0 || m.MetadataReceipts < 0 {
		return errors.New("backup authority manifest exceeds bound or is invalid")
	}
	if m.Version == 2 {
		if len(m.PageSHA256) != 0 || len(m.PageBytes) != 0 || m.PageCount <= 0 ||
			m.PageCount > m.TotalBytes/5+1 || m.TotalBytes/m.PageCount > PoolBackupPageLimit ||
			(m.TotalBytes/m.PageCount == PoolBackupPageLimit && m.TotalBytes%m.PageCount != 0) {
			return errors.New("backup authority v2 manifest is invalid")
		}
		return nil
	}
	if m.Version != 0 || m.PageCount != 0 || m.TotalBytes > PoolBackupBundleLimit ||
		m.History > 100000 || m.Catalog > 100000 || m.Pins > 100000 || m.MetadataReceipts > 100000 ||
		len(m.PageSHA256) == 0 || len(m.PageSHA256) != len(m.PageBytes) || len(m.PageSHA256) > 1024 {
		return errors.New("backup authority manifest exceeds bound or is invalid")
	}
	total := 0
	for i, hash := range m.PageSHA256 {
		if !lowerSHA256(hash) || m.PageBytes[i] <= 0 || m.PageBytes[i] > PoolBackupPageLimit {
			return errors.New("backup authority page digest is invalid")
		}
		total += m.PageBytes[i]
	}
	if total != m.TotalBytes {
		return errors.New("backup authority page sizes do not match")
	}
	return nil
}

// BackupPolicyRef is a compact committed policy/schedule reference for observation.
type BackupPolicyRef struct {
	ID         string    `json:"id"`
	ScheduleID string    `json:"schedule_id"`
	Revision   uint64    `json:"revision"`
	Enabled    bool      `json:"enabled"`
	Paused     bool      `json:"paused"`
	Name       string    `json:"name"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// BackupRunRef is a compact committed or unresolved run reference for observation.
type BackupRunRef struct {
	OperationID  string    `json:"operation_id"`
	OccurrenceID string    `json:"occurrence_id,omitempty"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	PolicyID     string    `json:"policy_id,omitempty"`
	VMID         string    `json:"vm_id,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

func (a PoolBackupAuthority) Valid() error {
	if len(a.Repositories) > PoolBackupMaxRepositories || len(a.Definitions) > PoolBackupMaxDefinitions || len(a.Policies) > PoolBackupMaxDefinitions {
		return errors.New("backup authority exceeds bound")
	}
	seenRepo, seenDef, seenHist, seenCat := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, repo := range a.Repositories {
		if repo.Valid() != nil || seenRepo[repo.ID] {
			return errors.New("backup authority repository is invalid")
		}
		seenRepo[repo.ID] = true
	}
	for _, p := range a.Definitions {
		if p.Valid() != nil || seenDef[p.ID] {
			return errors.New("backup authority policy definition is invalid")
		}
		seenDef[p.ID] = true
	}
	for _, r := range a.History {
		if r.Valid() != nil || seenHist[r.OperationID] {
			return errors.New("backup authority run history is invalid")
		}
		seenHist[r.OperationID] = true
	}
	for _, point := range a.Catalog {
		if point.Valid() != nil || seenCat[point.ID] {
			return errors.New("backup authority catalog index is invalid")
		}
		seenCat[point.ID] = true
	}
	seenPin := map[string]bool{}
	for _, pin := range a.Pins {
		if pin.Valid() != nil || seenPin[pin.OperationID] {
			return errors.New("backup authority restore pin is invalid")
		}
		seenPin[pin.OperationID] = true
	}
	seenReceipt := map[string]bool{}
	for _, receipt := range a.MetadataReceipts {
		if receipt.Valid() != nil || seenReceipt[receipt.OperationID] {
			return errors.New("backup authority metadata receipt is invalid")
		}
		seenReceipt[receipt.OperationID] = true
	}
	seenP, seenR := map[string]bool{}, map[string]bool{}
	for _, p := range a.Policies {
		if !ValidSnapshotOperationID(p.ID) || !ValidSnapshotOperationID(p.ScheduleID) || p.Revision == 0 || seenP[p.ID] {
			return errors.New("backup policy ref is invalid")
		}
		seenP[p.ID] = true
	}
	for _, r := range a.Runs {
		if !ValidSnapshotOperationID(r.OperationID) || r.Kind == "" || r.Status == "" || seenR[r.OperationID] {
			return errors.New("backup run ref is invalid")
		}
		if r.OccurrenceID != "" && !ValidSnapshotOperationID(r.OccurrenceID) {
			return errors.New("backup run occurrence ref is invalid")
		}
		seenR[r.OperationID] = true
	}
	return nil
}

// EffectiveRetention returns the per-VM retention or the policy default.
func (p BackupPolicy) EffectiveRetention(vmID string) BackupRetention {
	for _, o := range p.Overrides {
		if o.VMID == vmID && o.Retention != nil {
			return *o.Retention
		}
	}
	return p.Retention
}

func (p BackupPolicy) EffectiveCaptureMode(vmID string) string {
	for _, o := range p.Overrides {
		if o.VMID == vmID && o.CaptureMode != "" {
			return o.CaptureMode
		}
	}
	return p.CaptureMode
}

func FormatBackupOccurrence(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func BackupPolicySummary(p BackupPolicy) string {
	return fmt.Sprintf("%s rev=%d paused=%v next=%s", p.Name, p.Revision, p.Paused, FormatBackupOccurrence(p.NextDispatchAt))
}
