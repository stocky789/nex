// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func deletePolicyCommand(id, policy, name string) protocol.Command {
	return protocol.Command{OperationID: id, Kind: "backup-policy-delete", PolicyDelete: &protocol.DeleteBackupPolicyRequest{PolicyID: policy, Confirmation: name}}
}

func TestDeleteBackupPolicy(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("vm-1", member.State.HostID)
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm}}}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.LastDispatchAt = time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC)
	if _, err := UpsertPoolBackupPolicy(master, policy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	point := protocol.BackupRecoveryPoint{ID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: now,
		Consistency: protocol.BackupCaptureStopped, ManifestSHA256: strings.Repeat("a", 64)}
	if err := savePoolBackupCatalogEntry(master, point); err != nil {
		t.Fatal(err)
	}
	saveRun := func(id, status string) {
		t.Helper()
		if err := savePoolBackupRun(master, protocol.BackupRun{OperationID: id, Kind: protocol.BackupKindBackupNow, PoolID: "pool-1", VMID: "vm-1",
			RepositoryID: "repo-1", PolicyID: policy.ID, Stage: "reserved", Status: status, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	del := func(id, name string, reserveOnly bool) BackupResponse {
		return acceptBackupRequest(context.Background(), master, BackupRequest{Command: ptr(deletePolicyCommand(id, "pol-1", name)), ReserveOnly: reserveOnly})
	}
	stillThere := func(label string) {
		t.Helper()
		if _, err := GetPoolBackupPolicy(master, "pol-1"); err != nil {
			t.Fatalf("%s: policy is gone: %v", label, err)
		}
	}
	refused := func(id, name, want string) {
		t.Helper()
		response := del(id, name, false)
		if response.Err != nil || response.Result == nil || response.Result.Status != "failed" || !strings.Contains(response.Result.Error, want) {
			t.Fatalf("%s: %+v, want refusal containing %q", id, response, want)
		}
		stillThere(id)
	}

	refused("delete-name", "wrong", `type the backup job name "Nightly"`)
	saveRun("run-active", "pending")
	refused("delete-running", "Nightly", "run-active")
	saveRun("run-active", "succeeded")

	response := del("delete-1", "Nightly", false)
	if response.Err != nil || response.Result == nil || response.Result.Status != "succeeded" {
		t.Fatalf("delete: %+v", response)
	}
	if _, err := GetPoolBackupPolicy(master, "pol-1"); !errors.Is(err, ErrBackupPolicyNotFound) {
		t.Fatalf("policy survived delete: %v", err)
	}
	if _, err := loadPoolBackupRun(master, "run-active"); err != nil {
		t.Fatalf("run history was removed: %v", err)
	}
	if points, err := loadPoolBackupCatalog(master); err != nil || len(points) != 1 {
		t.Fatalf("recovery points changed: %v, %v", points, err)
	}
	before, _ := listPoolBackupRuns(master)
	if _, err := ApplyBackupScheduleTick(master, time.Date(2026, 9, 28, 2, 5, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if after, _ := listPoolBackupRuns(master); len(after) != len(before) {
		t.Fatalf("scheduler still runs the deleted policy: %d runs, was %d", len(after), len(before))
	}

	if replay := del("delete-1", "Nightly", false); replay.Err != nil || replay.Result == nil || replay.Result.Status != "succeeded" {
		t.Fatalf("replay: %+v", replay)
	}
	if changed := del("delete-1", "Other", false); !errors.Is(changed.Err, ErrBackupConflict) {
		t.Fatalf("changed payload: %+v", changed)
	}
	if absent := del("delete-2", "Nightly", false); !errors.Is(absent.Err, ErrBackupPolicyNotFound) {
		t.Fatalf("absent policy under a new request_id: %+v", absent)
	}

	// A crash after the delete but before the receipt settled: the retry succeeds.
	if _, err := UpsertPoolBackupPolicy(master, policy); err != nil {
		t.Fatal(err)
	}
	if reserved := del("delete-3", "Nightly", true); reserved.Err != nil || reserved.Result == nil || reserved.Result.Status != "pending" {
		t.Fatalf("reserve: %+v", reserved)
	}
	if err := deletePoolBackupPolicy(master, *deletePolicyCommand("delete-3", "pol-1", "Nightly").PolicyDelete); err != nil {
		t.Fatal(err)
	}
	if retry := del("delete-3", "Nightly", false); retry.Err != nil || retry.Result == nil || retry.Result.Status != "succeeded" {
		t.Fatalf("retry after crash: %+v", retry)
	}
}

// A run reserved for a policy a delete already removed is refused, and a delete racing with a
// reservation either refuses or leaves no pending run of the policy.
func TestPolicyDeleteRacesRunReservation(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	vm := *poolVMTestSpec("vm-1", member.State.HostID)
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: vm}}}); err != nil {
		t.Fatal(err)
	}
	reserve := func(policyID, id string) error {
		_, err := ReservePoolBackupRun(master, protocol.BackupRun{OperationID: id, Kind: protocol.BackupKindPolicyRunNow, PoolID: "pool-1", VMID: "vm-1",
			RepositoryID: "repo-1", PolicyID: policyID, RecoveryPointID: id, CaptureMode: protocol.BackupCaptureStopped,
			Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC()})
		return err
	}
	if err := reserve("pol-0", "run-00000000"); !errors.Is(err, ErrBackupPolicyNotFound) {
		t.Fatalf("reserve for a missing policy: %v", err)
	}
	for i := 1; i <= 20; i++ {
		policy := testPolicy()
		policy.ID, policy.ScheduleID = fmt.Sprintf("pol-%d", i), fmt.Sprintf("sch-%d", i)
		if _, err := UpsertPoolBackupPolicy(master, policy); err != nil {
			t.Fatal(err)
		}
		runID := fmt.Sprintf("run-race-%02d", i)
		reserved := make(chan error)
		go func() { reserved <- reserve(policy.ID, runID) }()
		deleteErr := deletePoolBackupPolicy(master, protocol.DeleteBackupPolicyRequest{PolicyID: policy.ID, Confirmation: "Nightly"})
		reserveErr := <-reserved
		_, getErr := GetPoolBackupPolicy(master, policy.ID)
		if deleteErr == nil && (!errors.Is(getErr, ErrBackupPolicyNotFound) || reserveErr == nil) {
			t.Fatalf("round %d: delete succeeded but policy %v, reserve %v", i, getErr, reserveErr)
		}
		if deleteErr != nil && (getErr != nil || reserveErr != nil) {
			t.Fatalf("round %d: delete refused (%v) but policy %v, reserve %v", i, deleteErr, getErr, reserveErr)
		}
	}
}

// A delete of a policy that does not exist writes no pending receipt, so a crash-retry can
// only find a policy gone because the delete removed it.
func TestDeleteMissingPolicyLeavesNoReceipt(t *testing.T) {
	master := newTransferLab(t, 0).host("master-1")
	response := acceptBackupRequest(context.Background(), master, BackupRequest{Command: ptr(deletePolicyCommand("delete-9", "pol-9", "x")), ReserveOnly: true})
	if !errors.Is(response.Err, ErrBackupPolicyNotFound) {
		t.Fatalf("reserve: %+v", response)
	}
	if _, err := loadPoolBackupMetadataReceipt(master, "delete-9"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt written for a missing policy: %v", err)
	}
}

// A run of uncertain outcome can still settle as a success and then apply the policy's
// retention, so it holds the policy like a pending run does.
func TestPolicyDeleteRefusedWhileRunNeedsAttention(t *testing.T) {
	master := seedBackupMaster(t)
	policy := testPolicy()
	if err := savePoolBackupPolicies(master, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{policy.ID: policy}}); err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: "run-1", Kind: protocol.BackupKindScheduled, PoolID: "pool-1", VMID: "vm-1", RepositoryID: "repo-1",
		PolicyID: policy.ID, Stage: "publishing", Status: "needs_attention", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	req := protocol.DeleteBackupPolicyRequest{PolicyID: policy.ID, Confirmation: policy.Name}
	var refusal backupRefusal
	if err := deletePoolBackupPolicy(master, req); !errors.As(err, &refusal) {
		t.Fatalf("delete with an unresolved run: %v", err)
	}
	if _, err := GetPoolBackupPolicy(master, policy.ID); err != nil {
		t.Fatalf("policy removed under an unresolved run: %v", err)
	}
	run.Status, run.Stage = "failed", "failed"
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	if err := deletePoolBackupPolicy(master, req); err != nil {
		t.Fatalf("delete after the run settled: %v", err)
	}
}
