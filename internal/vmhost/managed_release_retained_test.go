// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// releaseRetainedLab is a VM on local storage whose second revision detached an original disk.
type releaseRetainedLab struct {
	e              *ManagedEngine
	vm             protocol.VMCreateSpec
	live, original string
}

func newReleaseRetainedLab(t *testing.T, domstate string) releaseRetainedLab {
	t.Helper()
	storageRoot := t.TempDir()
	vm := managedTestVM()
	vmDir := filepath.Join(storageRoot, "nex-vms", vm.ID)
	if err := os.MkdirAll(vmDir, 0700); err != nil {
		t.Fatal(err)
	}
	live, original := filepath.Join(vmDir, "live.qcow2"), filepath.Join(vmDir, "original.qcow2")
	for _, path := range []string{live, original} {
		if err := os.WriteFile(path, []byte("qcow2"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	previousReady := managedStorageReadyFn
	managedStorageReadyFn = func(_ context.Context, _ protocol.StorageRegistration, _ uint64) (storageAccess, error) {
		return storageAccess{Path: storageRoot, Total: 1 << 40, Available: 1 << 40}, nil
	}
	t.Cleanup(func() { managedStorageReadyFn = previousReady })
	t.Setenv("VMHOST_STATE", domstate)
	t.Setenv("VMHOST_VM_ID", vm.ID)
	fakeCommand(t, "virsh", `case "$3" in domstate) echo "$VMHOST_STATE";; list) printf '%s\n' "$VMHOST_VM_ID";; *) exit 2;; esac`)
	e := managedTestEngine(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "s", PoolID: vm.PoolID, HostID: vm.HostID, Path: storageRoot, Kind: protocol.StorageKindLocal}
	first, second := vm, vm
	first.Revision, second.Revision = 0, 1
	for _, j := range []managedJournal{
		{Command: protocol.Command{OperationID: "storage-1", Kind: "storage-register", Storage: &storage}, Result: protocol.Result{Status: "succeeded"}, AppliedStorage: &storage},
		{Command: protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &first}, Result: protocol.Result{Status: "succeeded"}, Stage: "defined",
			Disks: []string{original}, DiskChains: [][]string{{original}}, Targets: []string{"vda"}},
		{Command: protocol.Command{OperationID: "replace-1", Kind: "vm-update", VM: &second}, Result: protocol.Result{Status: "succeeded"}, Stage: "defined",
			Disks: []string{live}, DiskChains: [][]string{{live}}, Targets: []string{"vda"}, Retained: []string{original, live}},
	} {
		j.Result.OperationID = j.Command.OperationID
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	return releaseRetainedLab{e: e, vm: vm, live: live, original: original}
}

func (l releaseRetainedLab) release(t *testing.T, op, confirmation string) protocol.Result {
	t.Helper()
	r, err := l.e.Execute(context.Background(), protocol.Command{OperationID: op, Kind: "vm-release-retained",
		ReleaseRetained: &protocol.VMReleaseRetainedRequest{VMID: l.vm.ID, PoolID: l.vm.PoolID, HostID: l.vm.HostID, Confirmation: confirmation}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fileExists(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestReleaseRetainedOriginalsDeletesOnlyDetachedFiles(t *testing.T) {
	l := newReleaseRetainedLab(t, "shut off")
	if r := l.release(t, "release-wrong-name", "other"); r.Status != "failed" || !strings.Contains(r.Error, "VM's name") || !fileExists(l.original) {
		t.Fatalf("wrong confirmation: %+v", r)
	}
	if r := l.release(t, "release-1", l.vm.Name); r.Status != "succeeded" {
		t.Fatalf("release failed: %+v", r)
	}
	if fileExists(l.original) || !fileExists(l.live) {
		t.Fatalf("original present=%v live present=%v", fileExists(l.original), fileExists(l.live))
	}
	if again := l.release(t, "release-1", l.vm.Name); again.Status != "succeeded" {
		t.Fatalf("replay of the same request changed: %+v", again)
	}
	if r := l.release(t, "release-2", l.vm.Name); r.Status != "failed" || !strings.Contains(r.Error, "no retained") {
		t.Fatalf("second release with nothing left: %+v", r)
	}
}

func TestReleaseRetainedOriginalsRefusesRunningVM(t *testing.T) {
	l := newReleaseRetainedLab(t, "running")
	if r := l.release(t, "release-running", l.vm.Name); r.Status != "failed" || !strings.Contains(r.Error, "stop the VM") || !fileExists(l.original) {
		t.Fatalf("running VM: %+v", r)
	}
}

func addReleaseRetainedTPM(t *testing.T, l releaseRetainedLab) (retained, current, unrelated string) {
	t.Helper()
	previousRoot := backupTPMStateRoot
	backupTPMStateRoot = t.TempDir()
	t.Cleanup(func() { backupTPMStateRoot = previousRoot })
	retained = restoreTPMPath(l.vm.ID, "replace-1")
	current = filepath.Join(backupTPMStateRoot, l.vm.ID)
	unrelated = restoreTPMPath("22222222-2222-4222-8222-222222222222", "replace-1")
	for _, dir := range []string{retained, current, unrelated} {
		if err := os.MkdirAll(filepath.Join(dir, "tpm2"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"tpm2-00.permall", "extra-state"} {
			if err := os.WriteFile(filepath.Join(dir, "tpm2", name), []byte(dir+name), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest, err := managedSnapshotManifest(context.Background(), retained)
	if err != nil {
		t.Fatal(err)
	}
	js, err := l.e.journals()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range js {
		if j.Command.OperationID == "replace-1" {
			j.BackupTPMPreviousSHA256 = manifest.SHA256
			if err := l.e.save(j); err != nil {
				t.Fatal(err)
			}
		}
	}
	return retained, current, unrelated
}

func interruptRetainedTPMRemoval(t *testing.T, retained string) {
	t.Helper()
	previousRemove := removeRetainedTPM
	t.Cleanup(func() { removeRetainedTPM = previousRemove })
	removeRetainedTPM = func(dir string) error {
		if dir != retained {
			t.Fatalf("unexpected TPM deletion target %q", dir)
		}
		if err := os.Remove(filepath.Join(dir, "tpm2", "extra-state")); err != nil {
			return err
		}
		return errors.New("interrupted retained TPM removal")
	}
}

func TestReleaseRetainedTPMResumesPartialRemovalWithoutWideningIntent(t *testing.T) {
	l := newReleaseRetainedLab(t, "shut off")
	retained, current, unrelated := addReleaseRetainedTPM(t, l)
	currentBefore, err := managedSnapshotManifest(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedBefore, err := managedSnapshotManifest(context.Background(), unrelated)
	if err != nil {
		t.Fatal(err)
	}
	interruptRetainedTPMRemoval(t, retained)
	if r := l.release(t, "release-partial", l.vm.Name); r.Status != "needs_attention" {
		t.Fatalf("partial removal: %+v", r)
	}
	if fileExists(filepath.Join(retained, "tpm2", "extra-state")) || !fileExists(filepath.Join(retained, "tpm2", "tpm2-00.permall")) {
		t.Fatal("fixture did not leave an interrupted partial TPM tree")
	}
	js, err := l.e.journals()
	if err != nil {
		t.Fatal(err)
	}
	later := restoreTPMPath(l.vm.ID, "replace-later")
	if err := os.MkdirAll(filepath.Join(later, "tpm2"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(later, "tpm2", "tpm2-00.permall"), []byte("later original"), 0600); err != nil {
		t.Fatal(err)
	}
	laterBefore, err := managedSnapshotManifest(context.Background(), later)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range js {
		if j.Command.OperationID == "replace-1" {
			next := *j.Command.VM
			next.Revision++
			j.Command.OperationID, j.Command.VM = "replace-later", &next
			j.Result.OperationID = "replace-later"
			j.BackupTPMPreviousSHA256 = laterBefore.SHA256
			j.Retained = nil
			if err := l.e.save(j); err != nil {
				t.Fatal(err)
			}
		}
	}
	removeRetainedTPM = os.RemoveAll
	l.e = managedTestEngine(l.e.stateDir)
	if r := l.release(t, "release-partial", l.vm.Name); r.Status != "succeeded" {
		t.Fatalf("restart failed to finish partial removal: %+v", r)
	}
	if fileExists(retained) || fileExists(l.original) || !fileExists(l.live) {
		t.Fatal("restart did not remove only the selected retained artifacts")
	}
	for _, check := range []struct {
		path   string
		before managedSnapshotFileManifest
	}{{current, currentBefore}, {unrelated, unrelatedBefore}, {later, laterBefore}} {
		after, err := managedSnapshotManifest(context.Background(), check.path)
		if err != nil || !snapshotManifestsEqual(after, check.before) {
			t.Fatalf("protected TPM tree changed: %s err=%v", check.path, err)
		}
	}
}

func TestReleaseRetainedTPMRefusesMutationBeforeAdmission(t *testing.T) {
	for _, mutation := range []string{"changed file", "missing file", "current tree symlink"} {
		t.Run(mutation, func(t *testing.T) {
			l := newReleaseRetainedLab(t, "shut off")
			retained, current, unrelated := addReleaseRetainedTPM(t, l)
			switch mutation {
			case "changed file":
				if err := os.WriteFile(filepath.Join(retained, "tpm2", "tpm2-00.permall"), []byte("mutated"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing file":
				if err := os.Remove(filepath.Join(retained, "tpm2", "extra-state")); err != nil {
					t.Fatal(err)
				}
			case "current tree symlink":
				if err := os.Rename(retained, retained+".kept"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(current, retained); err != nil {
					t.Fatal(err)
				}
			}
			if r := l.release(t, "release-mutated", l.vm.Name); r.Status != "failed" || !strings.Contains(r.Error, "TPM state changed") {
				t.Fatalf("mutated retained tree admitted: %+v", r)
			}
			for _, path := range []string{l.original, l.live, retained, current, unrelated} {
				if !fileExists(path) {
					t.Fatalf("artifact deleted before TPM validation: %s", path)
				}
			}
		})
	}
}

func TestReleaseRetainedTPMRetryRefusesChangedRemainingTree(t *testing.T) {
	for _, mutation := range []string{"changed file", "added file", "current tree symlink"} {
		t.Run(mutation, func(t *testing.T) {
			l := newReleaseRetainedLab(t, "shut off")
			retained, current, unrelated := addReleaseRetainedTPM(t, l)
			interruptRetainedTPMRemoval(t, retained)
			if r := l.release(t, "release-partial", l.vm.Name); r.Status != "needs_attention" {
				t.Fatalf("partial removal: %+v", r)
			}
			switch mutation {
			case "changed file":
				if err := os.WriteFile(filepath.Join(retained, "tpm2", "tpm2-00.permall"), []byte("mutated"), 0600); err != nil {
					t.Fatal(err)
				}
			case "added file":
				if err := os.WriteFile(filepath.Join(retained, "tpm2", "new-state"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			case "current tree symlink":
				if err := os.Rename(retained, retained+".kept"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(current, retained); err != nil {
					t.Fatal(err)
				}
			}
			removeRetainedTPM = os.RemoveAll
			l.e = managedTestEngine(l.e.stateDir)
			if r := l.release(t, "release-partial", l.vm.Name); r.Status != "needs_attention" || !strings.Contains(r.Error, "TPM state changed") {
				t.Fatalf("changed tree removed on retry: %+v", r)
			}
			for _, path := range []string{retained, current, unrelated, l.live} {
				if !fileExists(path) {
					t.Fatalf("protected tree disappeared: %s", path)
				}
			}
		})
	}
}

func TestReleaseRetainedTPMSettlesAfterInterruptedCompletedRemoval(t *testing.T) {
	l := newReleaseRetainedLab(t, "shut off")
	retained, current, unrelated := addReleaseRetainedTPM(t, l)
	previousRemove := removeRetainedTPM
	t.Cleanup(func() { removeRetainedTPM = previousRemove })
	removeRetainedTPM = func(dir string) error {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		return errors.New("interrupted before settlement")
	}
	if r := l.release(t, "release-complete", l.vm.Name); r.Status != "needs_attention" {
		t.Fatalf("interrupted settlement: %+v", r)
	}
	if fileExists(retained) || fileExists(l.original) {
		t.Fatal("fixture left artifacts instead of only an unsettled journal")
	}
	removeRetainedTPM = os.RemoveAll
	l.e = managedTestEngine(l.e.stateDir)
	if r := l.release(t, "release-complete", l.vm.Name); r.Status != "succeeded" {
		t.Fatalf("removed targets did not settle after restart: %+v", r)
	}
	for _, path := range []string{current, unrelated, l.live} {
		if !fileExists(path) {
			t.Fatalf("protected artifact disappeared: %s", path)
		}
	}
}

func TestReleaseRetainedTPMRetryRejectsUnboundWipingIntent(t *testing.T) {
	for _, mutation := range []string{"producer", "manifest", "missing TPM intent"} {
		t.Run(mutation, func(t *testing.T) {
			l := newReleaseRetainedLab(t, "shut off")
			retained, current, unrelated := addReleaseRetainedTPM(t, l)
			interruptRetainedTPMRemoval(t, retained)
			if r := l.release(t, "release-partial", l.vm.Name); r.Status != "needs_attention" {
				t.Fatalf("partial removal: %+v", r)
			}
			js, err := l.e.journals()
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range js {
				if j.Command.OperationID != "release-partial" {
					continue
				}
				switch mutation {
				case "producer":
					j.RetainedTPM[0].OperationID = "unrelated-operation"
				case "missing TPM intent":
					j.RetainedTPM = nil
				case "manifest":
					// Removing the old file from the admitted manifest cannot
					// legitimize a changed target while retaining its old digest.
					j.RetainedTPM[0].Manifest.Files = j.RetainedTPM[0].Manifest.Files[:1]
				}
				if err := l.e.save(j); err != nil {
					t.Fatal(err)
				}
			}
			removeRetainedTPM = os.RemoveAll
			l.e = managedTestEngine(l.e.stateDir)
			if r := l.release(t, "release-partial", l.vm.Name); r.Status != "needs_attention" || !strings.Contains(r.Error, "wiping intent") {
				t.Fatalf("unbound wiping intent accepted: %+v", r)
			}
			for _, path := range []string{retained, current, unrelated, l.live} {
				if !fileExists(path) {
					t.Fatalf("protected artifact disappeared: %s", path)
				}
			}
		})
	}
}

func TestReleaseRetainedOriginalsResumesValidatedEmptyTPMIntent(t *testing.T) {
	l := newReleaseRetainedLab(t, "shut off")
	retained, current, unrelated := addReleaseRetainedTPM(t, l)
	if err := os.RemoveAll(retained); err != nil {
		t.Fatal(err)
	}
	if r := l.release(t, "release-disks", l.vm.Name); r.Status != "succeeded" {
		t.Fatalf("disk-only release with old TPM already absent: %+v", r)
	}
	js, err := l.e.journals()
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range js {
		if j.Command.OperationID == "release-disks" {
			// Simulate interruption after deletion but before terminal settlement.
			j.Result.Status = "pending"
			if err := l.e.save(j); err != nil {
				t.Fatal(err)
			}
		}
	}
	l.e = managedTestEngine(l.e.stateDir)
	if r := l.release(t, "release-disks", l.vm.Name); r.Status != "succeeded" {
		t.Fatalf("validated empty TPM intent confused with missing admission: %+v", r)
	}
	for _, path := range []string{current, unrelated, l.live} {
		if !fileExists(path) {
			t.Fatalf("protected artifact disappeared: %s", path)
		}
	}
}

func TestReleaseRetainedTPMRefusesOversizedAdmissionBeforeDeletion(t *testing.T) {
	l := newReleaseRetainedLab(t, "shut off")
	_, current, unrelated := addReleaseRetainedTPM(t, l)
	js, err := l.e.journals()
	if err != nil {
		t.Fatal(err)
	}
	var producer managedJournal
	for _, j := range js {
		if j.Command.OperationID == "replace-1" {
			producer = j
		}
	}
	var trees []string
	longName := strings.Repeat("x", 240)
	for i := range 16 {
		op := fmt.Sprintf("replace-large-%02d", i)
		dir := restoreTPMPath(l.vm.ID, op)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for n := range 250 {
			name := fmt.Sprintf("%03d-%s", n, longName)
			if err := os.WriteFile(filepath.Join(dir, name), []byte("retained TPM"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		manifest, err := managedSnapshotManifest(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		j := producer
		next := *producer.Command.VM
		next.Revision += i + 1
		j.Command.OperationID, j.Command.VM = op, &next
		j.Result.OperationID = op
		j.BackupTPMPreviousSHA256 = manifest.SHA256
		j.Retained = nil
		if err := l.e.save(j); err != nil {
			t.Fatal(err)
		}
		trees = append(trees, dir)
	}
	r := l.release(t, "release-large", l.vm.Name)
	if r.Status != "failed" || !strings.Contains(r.Error, "journal") {
		t.Fatalf("oversized wiping intent admitted: %+v", r)
	}
	for _, path := range append(trees, l.original, l.live, current, unrelated) {
		if !fileExists(path) {
			t.Fatalf("refused admission removed an artifact: %s", path)
		}
	}
	j, err := l.e.readJournal("release-large.json")
	if err != nil || j.Stage == "wiping" || j.RetainedTPM != nil {
		t.Fatalf("refusal left an unreadable or admitted journal: stage=%s TPM=%d err=%v", j.Stage, len(j.RetainedTPM), err)
	}
	if _, err := l.e.journals(); err != nil {
		t.Fatalf("refusal blocked later managed work: %v", err)
	}
}

func (l releaseRetainedLab) delete(t *testing.T, op string, wipe bool) protocol.Result {
	t.Helper()
	r, err := l.e.Execute(context.Background(), protocol.Command{OperationID: op, Kind: "vm-delete",
		Delete: &protocol.VMDeleteRequest{VMID: l.vm.ID, PoolID: l.vm.PoolID, HostID: l.vm.HostID, DeleteDisks: wipe}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The vTPM state a restore replace retained is key material of the VM: deleting the VM removes
// it whether or not the disks are kept, and never touches the live or another VM's tree.
func TestDeleteRemovesRetainedTPMTrees(t *testing.T) {
	for _, wipe := range []bool{true, false} {
		t.Run(fmt.Sprintf("wipe=%t", wipe), func(t *testing.T) {
			l := newReleaseRetainedLab(t, "shut off")
			retained, current, unrelated := addReleaseRetainedTPM(t, l)
			fakeCommand(t, "virsh", `case "$3" in domstate) echo "shut off";; list) ;; undefine) exit 0;; *) exit 2;; esac`)
			if r := l.delete(t, "delete-1", wipe); r.Status != "succeeded" {
				t.Fatalf("delete: %+v", r)
			}
			if fileExists(retained) {
				t.Fatal("retained vTPM tree survived the VM")
			}
			if !fileExists(current) || !fileExists(unrelated) {
				t.Fatal("delete removed a vTPM tree that is not this VM's retained original")
			}
			if fileExists(l.original) == wipe {
				t.Fatalf("original disk present=%v with wipe=%t", fileExists(l.original), wipe)
			}
		})
	}
}

// A retained tree that no longer matches what its edit recorded refuses the delete before the
// domain is undefined, so no half-deleted VM keeps unverifiable key material.
func TestDeleteRefusesChangedRetainedTPMTree(t *testing.T) {
	l := newReleaseRetainedLab(t, "shut off")
	retained, _, _ := addReleaseRetainedTPM(t, l)
	if err := os.WriteFile(filepath.Join(retained, "tpm2", "tpm2-00.permall"), []byte("mutated"), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "virsh.log")
	t.Setenv("VMHOST_LOG", log)
	fakeCommand(t, "virsh", `echo "$3" >> "$VMHOST_LOG"; case "$3" in domstate) echo "shut off";; list) ;; undefine) exit 0;; *) exit 2;; esac`)
	if r := l.delete(t, "delete-mutated", true); r.Status != "failed" || !strings.Contains(r.Error, "TPM state changed") {
		t.Fatalf("mutated retained tree: %+v", r)
	}
	if calls, _ := os.ReadFile(log); strings.Contains(string(calls), "undefine") {
		t.Fatalf("domain undefined despite an unverifiable retained tree: %q", calls)
	}
	if !fileExists(retained) || !fileExists(l.original) {
		t.Fatal("refused delete removed retained state")
	}
}

// Release enforces the VM's pool and host and refuses while a change of the VM is unfinished,
// as delete does, rather than relying on the pool's admission alone.
func TestReleaseRetainedRefusesForeignRequestAndUnresolvedChange(t *testing.T) {
	l := newReleaseRetainedLab(t, "shut off")
	foreign := protocol.Command{OperationID: "release-foreign", Kind: "vm-release-retained",
		ReleaseRetained: &protocol.VMReleaseRetainedRequest{VMID: l.vm.ID, PoolID: l.vm.PoolID, HostID: "other-host", Confirmation: l.vm.Name}}
	if r, err := l.e.Execute(context.Background(), foreign, nil); err != nil || r.Status != "failed" || !strings.Contains(r.Error, "pool or host") || !fileExists(l.original) {
		t.Fatalf("foreign host release: %+v %v", r, err)
	}
	pending := l.vm
	pending.Revision = 2
	if err := l.e.save(managedJournal{Command: protocol.Command{OperationID: "edit-pending", Kind: "vm-update", VM: &pending},
		Result: protocol.Result{OperationID: "edit-pending", Status: "needs_attention"}, Stage: "defining"}); err != nil {
		t.Fatal(err)
	}
	if r := l.release(t, "release-blocked", l.vm.Name); r.Status != "failed" || !strings.Contains(r.Error, "still unresolved") || !fileExists(l.original) {
		t.Fatalf("release beside an unfinished edit: %+v", r)
	}
}
