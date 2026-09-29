// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// BackupScheduleTickResult is one scheduler pass outcome for tests and dispatch.
type BackupScheduleTickResult struct {
	Missed   []protocol.BackupRun
	Dispatch []protocol.BackupRun
	Skipped  []protocol.BackupRun
	Policy   protocol.BackupPolicy
}

// EvaluateBackupScheduleTick advances one policy through due occurrences without
// Controller. Past-due slots are recorded missed (bounded); only the current due
// slot may dispatch. Duplicate occurrence IDs are refused.
func EvaluateBackupScheduleTick(policy protocol.BackupPolicy, now time.Time, existing map[string]protocol.BackupRun, pendingVM map[string]bool) (BackupScheduleTickResult, protocol.BackupPolicy, error) {
	out := BackupScheduleTickResult{Policy: policy}
	if !policy.Enabled || policy.Paused {
		return out, policy, nil
	}
	if err := policy.Valid(); err != nil {
		return out, policy, ErrBackupInvalid
	}
	now = now.UTC()
	after := policy.LastDispatchAt
	if after.IsZero() && !policy.NextDispatchAt.IsZero() {
		// First wake: only consider from just before next, not unbounded history.
		after = policy.NextDispatchAt.Add(-time.Second)
	}
	if after.IsZero() {
		after = now.Add(-time.Second)
	}
	slots, err := protocol.BackupOccurrenceDueSlots(policy.Rule, policy.TimeZone, after, now, protocol.BackupMissedCatchUpCap)
	if err != nil {
		return out, policy, err
	}
	if len(slots) == 0 {
		if policy.NextDispatchAt.IsZero() || !policy.NextDispatchAt.After(now) {
			next, err := protocol.NextBackupOccurrence(policy.Rule, policy.TimeZone, now.Add(time.Second))
			if err != nil {
				return out, policy, err
			}
			policy.NextDispatchAt = next
		}
		out.Policy = policy
		return out, policy, nil
	}
	// All but the latest due slot are missed (no catch-up storm). Latest may dispatch.
	dispatchSlot := slots[len(slots)-1]
	for _, slot := range slots[:len(slots)-1] {
		for _, vmID := range policy.VMIDs {
			run, err := buildOccurrenceRun(policy, vmID, slot, "missed", "missed", "scheduler downtime; occurrence not catch-up dispatched")
			if err != nil {
				return out, policy, err
			}
			if _, ok := existing[run.OccurrenceID]; ok {
				continue
			}
			out.Missed = append(out.Missed, run)
			existing[run.OccurrenceID] = run
		}
	}
	for _, vmID := range policy.VMIDs {
		occID := protocol.BackupOccurrenceID(policy.ScheduleID, vmID, dispatchSlot)
		if prev, ok := existing[occID]; ok {
			out.Skipped = append(out.Skipped, prev)
			continue
		}
		if pendingVM[vmID] {
			run, err := buildOccurrenceRun(policy, vmID, dispatchSlot, "overlap", "skipped_overlap", "prior backup still unsettled for this VM")
			if err != nil {
				return out, policy, err
			}
			out.Skipped = append(out.Skipped, run)
			existing[occID] = run
			continue
		}
		run, err := buildOccurrenceRun(policy, vmID, dispatchSlot, "reserved", "pending", "")
		if err != nil {
			return out, policy, err
		}
		out.Dispatch = append(out.Dispatch, run)
		existing[occID] = run
		pendingVM[vmID] = true
	}
	policy.LastDispatchAt = dispatchSlot
	next, err := protocol.NextBackupOccurrence(policy.Rule, policy.TimeZone, dispatchSlot.Add(time.Second))
	if err != nil {
		return out, policy, err
	}
	policy.NextDispatchAt = next
	policy.UpdatedAt = now
	out.Policy = policy
	return out, policy, nil
}

func buildOccurrenceRun(policy protocol.BackupPolicy, vmID string, slot time.Time, stage, status, errMsg string) (protocol.BackupRun, error) {
	occID := protocol.BackupOccurrenceID(policy.ScheduleID, vmID, slot)
	now := time.Now().UTC()
	retention := policy.EffectiveRetention(vmID)
	run := protocol.BackupRun{
		OperationID: occID, Kind: protocol.BackupKindScheduled, PoolID: policy.PoolID,
		VMID: vmID, RepositoryID: policy.RepositoryID, PolicyID: policy.ID, ScheduleID: policy.ScheduleID,
		OccurrenceID: occID, PolicyRevision: policy.Revision, OccurrenceAt: slot,
		CaptureMode: policy.EffectiveCaptureMode(vmID), Retention: &retention,
		Stage: stage, Status: status, Error: errMsg, CreatedAt: now, UpdatedAt: now,
	}
	if err := run.Valid(); err != nil {
		return protocol.BackupRun{}, err
	}
	return run, nil
}

// ApplyBackupScheduleTick persists evaluate results on the master. Controller absence is fine.
func ApplyBackupScheduleTick(s *Store, now time.Time) ([]BackupScheduleTickResult, error) {
	if err := masterBackupPool(s, ""); err != nil {
		return nil, nil
	}
	policies, err := ListPoolBackupPolicies(s)
	if err != nil {
		return nil, err
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return nil, err
	}
	existing := map[string]protocol.BackupRun{}
	pendingVM := map[string]bool{}
	activeHost := map[string]int{}
	activeRepo := map[string]int{}
	for _, r := range runs {
		if r.OccurrenceID != "" {
			existing[r.OccurrenceID] = r
		}
		existing[r.OperationID] = r
		if (r.Status == "pending" || r.Status == "needs_attention") && r.VMID != "" {
			pendingVM[r.VMID] = true
			activeRepo[r.RepositoryID]++
			if vm, err := lookupPoolVMSpec(s, r.VMID); err == nil {
				activeHost[vm.HostID]++
			}
		}
	}
	var results []BackupScheduleTickResult
	state, err := loadPoolBackupPolicies(s)
	if err != nil {
		return nil, err
	}
	for _, policy := range policies {
		tick, updated, err := EvaluateBackupScheduleTick(policy, now, existing, pendingVM)
		if err != nil {
			return results, err
		}
		for _, run := range tick.Missed {
			if err := saveScheduledBackupRun(s, run); err != nil {
				return results, err
			}
		}
		for _, run := range tick.Skipped {
			if run.Status == "skipped_overlap" {
				if err := saveScheduledBackupRun(s, run); err != nil {
					return results, err
				}
			}
		}
		for _, run := range tick.Dispatch {
			vm, err := lookupPoolVMSpec(s, run.VMID)
			if err != nil {
				return results, err
			}
			if policy.Limits.MaxConcurrentPerHost > 0 && activeHost[vm.HostID] >= policy.Limits.MaxConcurrentPerHost ||
				policy.Limits.MaxConcurrentPerRepo > 0 && activeRepo[run.RepositoryID] >= policy.Limits.MaxConcurrentPerRepo {
				run.Stage, run.Status, run.Error = "limit", "skipped_overlap", "backup policy concurrency limit reached"
				if err := saveScheduledBackupRun(s, run); err != nil {
					return results, err
				}
				continue
			}
			run, cmd, err := prepareScheduledBackupRun(s, run)
			if err != nil {
				return results, err
			}
			if _, err := ReservePoolBackupRun(s, run); err != nil {
				if errors.Is(err, ErrBackupConflict) {
					continue
				}
				return results, err
			}
			activeHost[vm.HostID]++
			activeRepo[run.RepositoryID]++
			if vm.HostID == s.State.HostID {
				err = applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}})
			}
			if err != nil {
				run.Status, run.Stage, run.Error = "needs_attention", "dispatch", err.Error()
				run.UpdatedAt = time.Now().UTC()
				_ = saveScheduledBackupRun(s, run)
			}
		}
		state.Policies[updated.ID] = updated
		results = append(results, tick)
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := requirePoolAuthorityWritable(s); err != nil {
		return results, err
	}
	if err := savePoolBackupPolicies(s, state); err != nil {
		return results, err
	}
	return results, nil
}

func saveScheduledBackupRun(s *Store, run protocol.BackupRun) error {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := masterBackupPool(s, run.PoolID); err != nil {
		return err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	return savePoolBackupRun(s, run)
}

// prepareScheduledBackupRun binds the exact command intent before the reservation.
func prepareScheduledBackupRun(s *Store, run protocol.BackupRun) (protocol.BackupRun, protocol.Command, error) {
	vm, err := lookupPoolVMSpec(s, run.VMID)
	if err != nil {
		return protocol.BackupRun{}, protocol.Command{}, err
	}
	cmd := protocol.Command{
		OperationID: run.OperationID, Kind: "backup-now",
		VM:     &vm,
		Backup: &protocol.BackupNowRequest{RepositoryID: run.RepositoryID, Consistency: run.CaptureMode},
	}
	run.HostID = vm.HostID
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		return protocol.BackupRun{}, protocol.Command{}, err
	}
	run.IntentSHA256 = intent
	run.RecoveryPointID = run.OperationID
	return run, cmd, nil
}

func lookupPoolVMSpec(s *Store, vmID string) (protocol.VMCreateSpec, error) {
	if vm, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).VMDefinition(vmID); err == nil {
		return vm, nil
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return protocol.VMCreateSpec{}, err
	}
	rec, ok := state.VMs[vmID]
	if !ok {
		return protocol.VMCreateSpec{}, ErrBackupNotFound
	}
	return rec.Spec, nil
}

// Run now from a policy: new operation identity, does not move next_dispatch.
func ReservePolicyRunNow(s *Store, policyID, vmID, operationID string) (protocol.BackupRun, error) {
	if !protocol.ValidSnapshotOperationID(operationID) || vmID == "" {
		return protocol.BackupRun{}, ErrBackupInvalid
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return protocol.BackupRun{}, err
	}
	if existing, err := loadPoolBackupRun(s, operationID); err == nil {
		if existing.Kind != protocol.BackupKindPolicyRunNow || existing.PolicyID != policyID || existing.VMID != vmID {
			return protocol.BackupRun{}, ErrBackupConflict
		}
		if existing.Status == "pending" {
			prepared, cmd, err := prepareScheduledBackupRun(s, existing)
			if err != nil || prepared.IntentSHA256 != existing.IntentSHA256 ||
				(existing.HostID != "" && existing.HostID != prepared.HostID) {
				return existing, ErrBackupConflict
			}
			if existing.HostID == "" {
				entry, ok := s.State.Journal[operationID]
				if !ok || entry.Status != "pending" || entry.Command == nil || entry.Command.Kind != protocol.BackupKindPolicyRunNow {
					return existing, ErrBackupConflict
				}
				existing.HostID = prepared.HostID
				if err := saveScheduledBackupRun(s, existing); err != nil {
					return existing, err
				}
			}
			if err := queuePolicyRunNowBackup(s, cmd, policyID, vmID); err != nil {
				return existing, err
			}
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return protocol.BackupRun{}, err
	}
	policy, err := GetPoolBackupPolicy(s, policyID)
	if err != nil {
		return protocol.BackupRun{}, err
	}
	if vmID == "" || !containsString(policy.VMIDs, vmID) {
		return protocol.BackupRun{}, ErrBackupInvalid
	}
	vm, err := lookupPoolVMSpec(s, vmID)
	if err != nil {
		return protocol.BackupRun{}, err
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return protocol.BackupRun{}, err
	}
	activeHost, activeRepo := 0, 0
	for _, prior := range runs {
		if prior.Status != "pending" && prior.Status != "needs_attention" {
			continue
		}
		if prior.VMID == vmID {
			return protocol.BackupRun{}, ErrBackupConflict
		}
		if prior.RepositoryID == policy.RepositoryID {
			activeRepo++
		}
		if priorVM, err := lookupPoolVMSpec(s, prior.VMID); err == nil && priorVM.HostID == vm.HostID {
			activeHost++
		}
	}
	if policy.Limits.MaxConcurrentPerHost > 0 && activeHost >= policy.Limits.MaxConcurrentPerHost ||
		policy.Limits.MaxConcurrentPerRepo > 0 && activeRepo >= policy.Limits.MaxConcurrentPerRepo {
		return protocol.BackupRun{}, ErrBackupConflict
	}
	now := time.Now().UTC()
	retention := policy.EffectiveRetention(vmID)
	run := protocol.BackupRun{
		OperationID: operationID, Kind: protocol.BackupKindPolicyRunNow, PoolID: policy.PoolID,
		VMID: vmID, RepositoryID: policy.RepositoryID, PolicyID: policy.ID, ScheduleID: policy.ScheduleID,
		PolicyRevision: policy.Revision, RecoveryPointID: operationID,
		CaptureMode: policy.EffectiveCaptureMode(vmID), Retention: &retention,
		Stage: "reserved", Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
	run, cmd, err := prepareScheduledBackupRun(s, run)
	if err != nil {
		return protocol.BackupRun{}, err
	}
	reserved, err := ReservePoolBackupRun(s, run)
	if err != nil {
		return protocol.BackupRun{}, err
	}
	if err := queuePolicyRunNowBackup(s, cmd, policyID, vmID); err != nil {
		return reserved, err
	}
	return reserved, nil
}

func queuePolicyRunNowBackup(s *Store, cmd protocol.Command, policyID, vmID string) error {
	if entry, ok := s.State.Journal[cmd.OperationID]; ok && entry.Command != nil && entry.Command.Kind == protocol.BackupKindPolicyRunNow {
		if entry.Status != "pending" || entry.Command.BackupPolicy == nil || entry.Command.BackupPolicy.ID != policyID ||
			entry.Command.VM == nil || entry.Command.VM.ID != vmID {
			return ErrBackupConflict
		}
		entry.Command = &cmd
		s.State.Journal[cmd.OperationID] = entry
		return s.Save()
	}
	if cmd.VM.HostID != s.State.HostID {
		return nil // Core-local member runs use the master's peer scheduler.
	}
	return applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}})
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
