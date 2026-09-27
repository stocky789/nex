// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"errors"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestBackupPolicyRefusesUnknownAndForeignVMs(t *testing.T) {
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	foreign := poolVMTestSpec("foreign-vm", "host-a")
	foreign.PoolID = "other-pool"
	if err := savePoolVMs(s, poolVMState{VMs: map[string]poolVMRecord{
		foreign.ID: {PoolID: foreign.PoolID, VMID: foreign.ID, HostID: foreign.HostID, State: "stopped", Spec: *foreign},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, vmID := range []string{"missing-vm", foreign.ID} {
		policy := testPolicy()
		policy.VMIDs = []string{vmID}
		if _, err := UpsertPoolBackupPolicy(s, policy); !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("VM %q: want invalid policy, got %v", vmID, err)
		}
	}
	policies, err := ListPoolBackupPolicies(s)
	if err != nil || len(policies) != 0 {
		t.Fatalf("rejected policies persisted: %v, %v", policies, err)
	}
}

func TestBackupScheduleHonorsRepositoryConcurrencyLimit(t *testing.T) {
	s := seedBackupMaster(t)
	vms := poolVMState{VMs: map[string]poolVMRecord{}}
	for _, id := range []string{"vm-1", "vm-2"} {
		vm := poolVMTestSpec(id, "host-a")
		vms.VMs[id] = poolVMRecord{PoolID: vm.PoolID, VMID: id, HostID: vm.HostID, State: "stopped", Epoch: 1, Spec: *vm}
	}
	if err := savePoolVMs(s, vms); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.VMIDs = []string{"vm-1", "vm-2"}
	policy.Limits.MaxConcurrentPerRepo = 1
	policy.LastDispatchAt = time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC)
	if err := savePoolBackupPolicies(s, poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{policy.ID: policy}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBackupScheduleTick(s, time.Date(2026, 9, 27, 2, 5, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %v, %v", runs, err)
	}
	statuses := map[string]int{}
	for _, run := range runs {
		statuses[run.Status]++
	}
	if statuses["pending"] != 1 || statuses["skipped_overlap"] != 1 {
		t.Fatalf("repository limit was not enforced: %v", statuses)
	}
}
