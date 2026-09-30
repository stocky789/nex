// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func orphanCommand(id string, req protocol.OrphanCleanupRequest) protocol.Command {
	return protocol.Command{OperationID: id, Kind: "backup-orphan-cleanup", OrphanCleanup: &req}
}

func orphanRequest(vms []string, points, protected int, include bool) protocol.OrphanCleanupRequest {
	return protocol.OrphanCleanupRequest{RepositoryID: "repo-1", VMIDs: vms, IncludeProtected: include, RecoveryPoints: points,
		ProtectedPoints: protected, Confirmation: protocol.OrphanCleanupConfirmation(points, protected, include)}
}

func TestBackupOrphanQueryAndCleanup(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	if _, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	}); err != nil {
		t.Fatal(err)
	}
	for id, vm := range map[string]string{"live-1": "vm-live", "gone-1": "vm-gone", "gone-2": "vm-gone", "gone-3": "vm-gone", "other-1": "vm-other"} {
		seedPublishedPoint(t, mount, "repo-1", "pool-1", id, vm)
	}
	if _, err := importPoolBackupCatalog(context.Background(), master, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetPoolBackupPointProtected(master, protocol.ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "gone-2", Protected: true}); err != nil {
		t.Fatal(err)
	}
	if err := putBackupRestorePin(master, protocol.BackupRestorePin{RecoveryPointID: "gone-3", OperationID: "restore-1",
		Kind: protocol.BackupPinKindNew, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	// Without a VM inventory nothing can be called an orphan.
	if err := os.Remove(poolVMFile(master)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := queryBackupOrphans(context.Background(), master, "repo-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("query without inventory: %v", err)
	}
	// vm-live sits on a member that is not reachable; its record still says it exists.
	vms := map[string]poolVMRecord{}
	for _, id := range []string{"vm-live"} {
		vm := poolVMTestSpec(id, "member-1")
		vms[id] = poolVMRecord{PoolID: vm.PoolID, VMID: id, HostID: "member-1", State: "running", Spec: *vm}
	}
	if err := savePoolVMs(master, poolVMState{VMs: vms}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.VMIDs = []string{"vm-live"}
	if _, err := UpsertPoolBackupPolicy(master, policy); err != nil {
		t.Fatal(err)
	}
	// The job keeps listing a VM that has since been deleted from the pool.
	state, _ := loadPoolBackupPolicies(master)
	policy = state.Policies[policy.ID]
	policy.VMIDs = append(policy.VMIDs, "vm-gone")
	state.Policies[policy.ID] = policy
	if err := savePoolBackupPolicies(master, state); err != nil {
		t.Fatal(err)
	}

	query, err := queryBackupOrphans(context.Background(), master, "repo-1")
	if err != nil || len(query.Groups) != 2 || query.Groups[0].VMID != "vm-gone" || query.Groups[1].VMID != "vm-other" {
		t.Fatalf("query %+v %v", query, err)
	}
	gone := query.Groups[0]
	if gone.RecoveryPoints != 3 || gone.ProtectedPoints != 1 || !gone.InUse || gone.SizeBytes != 3*int64(len("disk-bytes-for-restore")) ||
		gone.VMName != "app" || gone.Oldest.After(gone.Newest) {
		t.Fatalf("group %+v", gone)
	}
	if len(query.Warnings) != 1 || !strings.Contains(query.Warnings[0], "vm-gone") {
		t.Fatalf("warnings %v", query.Warnings)
	}
	if _, err := queryBackupOrphans(context.Background(), member, "repo-1"); err == nil {
		t.Fatal("a member answered an orphan query")
	}

	run := func(id string, req protocol.OrphanCleanupRequest) protocol.Result {
		t.Helper()
		response := acceptBackupRequest(context.Background(), master, BackupRequest{Command: ptr(orphanCommand(id, req))})
		if response.Err != nil || response.Result == nil {
			t.Fatalf("%s: %+v", id, response)
		}
		return *response.Result
	}
	refused := func(id string, req protocol.OrphanCleanupRequest, want string) {
		t.Helper()
		if result := run(id, req); result.Status != "failed" || !strings.Contains(result.Error, want) {
			t.Fatalf("%s: %+v, want refusal containing %q", id, result, want)
		}
	}
	present := func(id string) bool {
		_, err := os.Stat(filepath.Join(mount, id))
		return err == nil
	}
	refused("wrong-text", protocol.OrphanCleanupRequest{RepositoryID: "repo-1", VMIDs: []string{"vm-gone"}, RecoveryPoints: 3, ProtectedPoints: 1, Confirmation: "yes"}, "to confirm")
	refused("wrong-counts", orphanRequest([]string{"vm-gone"}, 2, 1, false), "now hold 3 orphaned recovery points (1 protected)")
	refused("live-vm", orphanRequest([]string{"vm-live"}, 1, 0, false), "no orphaned recovery points")
	if !present("gone-1") {
		t.Fatal("a refusal deleted data")
	}

	ok := run("clean-1", orphanRequest([]string{"vm-gone"}, 3, 1, false))
	if ok.Status != "succeeded" {
		t.Fatalf("cleanup %+v", ok)
	}
	rec, err := loadOrphanCleanup(master, "clean-1")
	if err != nil || rec.Status != "succeeded" {
		t.Fatalf("record %+v %v", rec, err)
	}
	got := map[string]string{}
	for _, point := range rec.Points {
		got[point.RecoveryPointID] = point.Status + ":" + point.Reason
	}
	if got["gone-1"] != "deleted:" || got["gone-2"] != "skipped:protected" || !strings.HasPrefix(got["gone-3"], "skipped:in use") {
		t.Fatalf("results %v", got)
	}
	if present("gone-1") || !present("gone-2") || !present("gone-3") || !present("other-1") || !present("live-1") {
		t.Fatal("destination data does not match the results")
	}
	if _, err := getCatalogPoint(master, "repo-1", "gone-1"); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("deleted point is still cataloged: %v", err)
	}
	// The recorded outcome is replayed, not run again; the same id with other parameters conflicts.
	if replay := run("clean-1", orphanRequest([]string{"vm-gone"}, 3, 1, false)); replay.Status != "succeeded" {
		t.Fatalf("replay %+v", replay)
	}
	if response := acceptBackupRequest(context.Background(), master, BackupRequest{Command: ptr(orphanCommand("clean-1", orphanRequest([]string{"vm-other"}, 1, 0, false)))}); !errors.Is(response.Err, ErrBackupConflict) {
		t.Fatalf("changed payload: %+v", response)
	}

	// Protected points go only when asked, and the pin still holds.
	refused("no-protected-count", protocol.OrphanCleanupRequest{RepositoryID: "repo-1", VMIDs: []string{"vm-gone"}, IncludeProtected: true, RecoveryPoints: 2, ProtectedPoints: 1,
		Confirmation: protocol.OrphanCleanupConfirmation(2, 0, true)}, "to confirm")
	if result := run("clean-2", orphanRequest([]string{"vm-gone"}, 2, 1, true)); result.Status != "succeeded" {
		t.Fatalf("include protected %+v", result)
	}
	if present("gone-2") || !present("gone-3") {
		t.Fatal("protected point should be deleted and the pinned one kept")
	}

	// An interrupted run continues from its recorded plan: a point added since is not in it,
	// and a VM that exists again is left alone.
	if _, err := planOrphanCleanup(master, "resume-1", orphanRequest([]string{"vm-other"}, 1, 0, false)); err != nil {
		t.Fatal(err)
	}
	seedPublishedPoint(t, mount, "repo-1", "pool-1", "other-2", "vm-other")
	if _, err := importPoolBackupCatalog(context.Background(), master, "repo-1"); err != nil {
		t.Fatal(err)
	}
	if err := reserveBackupMetadataReceipt(master, orphanCommand("resume-1", orphanRequest([]string{"vm-other"}, 1, 0, false)), false); err != nil {
		t.Fatal(err)
	}
	if result := run("resume-1", orphanRequest([]string{"vm-other"}, 1, 0, false)); result.Status != "succeeded" {
		t.Fatalf("resume %+v", result)
	}
	if present("other-1") || !present("other-2") {
		t.Fatal("resume must delete exactly the planned point")
	}
	if _, err := planOrphanCleanup(master, "recreated-1", orphanRequest([]string{"vm-other"}, 1, 0, false)); err != nil {
		t.Fatal(err)
	}
	vm := poolVMTestSpec("vm-other", "master-1")
	vms["vm-other"] = poolVMRecord{PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: *vm}
	if err := savePoolVMs(master, poolVMState{VMs: vms}); err != nil {
		t.Fatal(err)
	}
	if result := run("recreated-1", orphanRequest([]string{"vm-other"}, 1, 0, false)); result.Status != "succeeded" || !present("other-2") {
		t.Fatalf("a re-created VM lost its points: %+v", result)
	}
	if rec, _ := loadOrphanCleanup(master, "recreated-1"); rec.Points[0].Reason != "the VM exists again" {
		t.Fatalf("recreated %+v", rec)
	}
}
