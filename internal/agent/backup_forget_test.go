// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func forgetCommand(id, repo, name string, points, protected int) protocol.Command {
	return protocol.Command{OperationID: id, Kind: "backup-repository-forget", ForgetRepository: &protocol.ForgetRepositoryRequest{
		RepositoryID: repo, Confirmation: name, RecoveryPoints: points, ProtectedPoints: protected,
	}}
}

func startBackupForgetOwner(t *testing.T, s *Store) {
	t.Helper()
	queue := make(chan BackupRequest)
	done := make(chan struct{})
	peerBackupRequests.Store(s, queue)
	go func() {
		defer close(done)
		for request := range queue {
			request.Reply <- acceptBackupRequest(context.Background(), s, request)
		}
	}()
	t.Cleanup(func() {
		peerBackupRequests.Delete(s)
		close(queue)
		<-done
	})
}

func TestForgetBackupRepository(t *testing.T) {
	var detached []string
	previous := detachBackupRepositoryNative
	detachBackupRepositoryNative = func(_ context.Context, repo protocol.BackupRepository) error {
		detached = append(detached, repo.ID)
		return nil
	}
	t.Cleanup(func() { detachBackupRepositoryNative = previous })

	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	startBackupForgetOwner(t, member)
	mounts := map[string]string{"repo-1": t.TempDir(), "repo-2": t.TempDir()}
	for id, mount := range mounts {
		repo, err := upsertTestBackupRepository(master, protocol.BackupRepository{
			ID: id, PoolID: "pool-1", Name: "Repo " + id, Kind: protocol.BackupRepoKindNFS,
			Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/" + id}, MountPath: mount,
		})
		if err != nil {
			t.Fatal(err)
		}
		if id == "repo-1" {
			if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{id: repo}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	payload := filepath.Join(mounts["repo-1"], "payload")
	if err := os.WriteFile(payload, []byte("recovery data"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, point := range []protocol.BackupRecoveryPoint{
		{ID: "point-1", RepositoryID: "repo-1", Protected: true},
		{ID: "point-2", RepositoryID: "repo-1"},
		{ID: "point-3", RepositoryID: "repo-2"},
	} {
		point.PoolID, point.SourceVMID, point.CapturedAt = "pool-1", "vm-1", time.Now().UTC()
		point.Consistency, point.ManifestSHA256 = protocol.BackupCaptureStopped, strings.Repeat("a", 64)
		if err := savePoolBackupCatalogEntry(master, point); err != nil {
			t.Fatal(err)
		}
	}
	for id, repo := range map[string]string{"run-1": "repo-1", "run-3": "repo-2"} {
		if err := savePoolBackupRun(master, protocol.BackupRun{OperationID: id, Kind: protocol.BackupKindBackupNow, PoolID: "pool-1",
			VMID: "vm-1", RepositoryID: repo, Stage: "settled", Status: "succeeded", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	forget := func(id, name string, points, protected int) protocol.Result {
		t.Helper()
		response := acceptBackupRequest(context.Background(), master, BackupRequest{Command: ptr(forgetCommand(id, "repo-1", name, points, protected))})
		if response.Err != nil || response.Result == nil {
			t.Fatalf("%s: %+v", id, response)
		}
		return *response.Result
	}
	refused := func(id, name string, points, protected int, want string) {
		t.Helper()
		if result := forget(id, name, points, protected); result.Status != "failed" || !strings.Contains(result.Error, want) {
			t.Fatalf("%s: %+v, want refusal containing %q", id, result, want)
		}
		if _, err := GetPoolBackupRepository(master, "repo-1"); err != nil {
			t.Fatalf("%s refused but repository is gone: %v", id, err)
		}
	}

	refused("forget-name", "wrong", 2, 1, "type the repository name")
	refused("forget-counts", "Repo repo-1", 1, 0, "now holds 2 recovery points (1 protected)")

	vm := poolVMTestSpec("vm-1", "master-1")
	if err := savePoolVMs(master, poolVMState{VMs: map[string]poolVMRecord{vm.ID: {PoolID: vm.PoolID, VMID: vm.ID, HostID: vm.HostID, State: "stopped", Spec: *vm}}}); err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	if _, err := UpsertPoolBackupPolicy(master, policy); err != nil {
		t.Fatal(err)
	}
	refused("forget-policy", "Repo repo-1", 2, 1, "1 backup jobs use this repository")
	state, _ := loadPoolBackupPolicies(master)
	delete(state.Policies, policy.ID)
	if err := savePoolBackupPolicies(master, state); err != nil {
		t.Fatal(err)
	}

	if err := savePoolBackupRun(master, protocol.BackupRun{OperationID: "run-2", Kind: protocol.BackupKindBackupNow, PoolID: "pool-1",
		VMID: "vm-1", RepositoryID: "repo-1", Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	refused("forget-running", "Repo repo-1", 2, 1, "is running")
	if err := os.Remove(filepath.Join(poolBackupRunsPath(master), "run-2.json")); err != nil {
		t.Fatal(err)
	}

	if err := putBackupRestorePin(master, protocol.BackupRestorePin{RecoveryPointID: "point-2", OperationID: "restore-1",
		Kind: protocol.BackupPinKindNew, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRun(master, protocol.BackupRun{OperationID: "restore-1", Kind: protocol.BackupKindTestRestore, PoolID: "pool-1",
		RepositoryID: "repo-1", RecoveryPointID: "point-2", NewVMID: vm.ID, Stage: "registered", Status: "succeeded", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	refused("forget-pinned", "Repo repo-1", 2, 1, "in use by an unfinished restore")
	refused("forget-pinned-names", "Repo repo-1", 2, 1, "operation restore-1, test VM vm-1 (Shared guest)")
	if err := os.Remove(filepath.Join(poolBackupRunsPath(master), "restore-1.json")); err != nil {
		t.Fatal(err)
	}
	if err := clearBackupRestorePin(master, "point-2", "restore-1"); err != nil {
		t.Fatal(err)
	}
	if len(detached) != 0 {
		t.Fatalf("refusals detached mounts: %v", detached)
	}

	if result := forget("forget-ok", "Repo repo-1", 2, 1); result.Status != "succeeded" {
		t.Fatalf("forget: %+v", result)
	}
	if _, err := GetPoolBackupRepository(master, "repo-1"); err == nil {
		t.Fatal("repository survived forget")
	}
	if _, err := GetPoolBackupRepository(member, "repo-1"); err == nil {
		t.Fatal("member kept the repository record")
	}
	if _, err := GetPoolBackupRepository(master, "repo-2"); err != nil {
		t.Fatalf("other repository forgotten: %v", err)
	}
	catalog, _ := loadPoolBackupCatalog(master)
	runs, _ := listPoolBackupRuns(master)
	if len(catalog) != 1 || catalog[0].ID != "point-3" || len(runs) != 1 || runs[0].OperationID != "run-3" {
		t.Fatalf("records outside the repository changed: %+v %+v", catalog, runs)
	}
	if data, err := os.ReadFile(payload); err != nil || string(data) != "recovery data" {
		t.Fatalf("destination data touched: %q %v", data, err)
	}
	if len(detached) != 2 {
		t.Fatalf("mount detached on %v, want master and member", detached)
	}

	// The same request replays its recorded outcome without running again; a new request
	// for a repository that is already gone succeeds.
	if result := forget("forget-ok", "Repo repo-1", 2, 1); result.Status != "succeeded" {
		t.Fatalf("replay: %+v", result)
	}
	if result := forget("forget-again", "", 0, 0); result.Status != "succeeded" || len(detached) != 2 {
		t.Fatalf("already forgotten: %+v, detached %v", result, detached)
	}
	// The recorded refusal is replayed too, not re-evaluated.
	if result := forget("forget-name", "wrong", 2, 1); result.Status != "failed" {
		t.Fatalf("refusal replay: %+v", result)
	}
}

func TestMasterDropsMemberRecordsItNoLongerHolds(t *testing.T) {
	previous := detachBackupRepositoryNative
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error { return nil }
	t.Cleanup(func() { detachBackupRepositoryNative = previous })

	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	startBackupForgetOwner(t, member)
	held := map[string]protocol.BackupRepository{}
	for _, id := range []string{"repo-kept", "repo-stale"} {
		repo := protocol.BackupRepository{ID: id, PoolID: "pool-1", Name: id, Kind: protocol.BackupRepoKindNFS,
			Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/" + id}, MountPath: t.TempDir()}
		held[id] = repo
		if id == "repo-kept" {
			if _, err := upsertTestBackupRepository(master, repo); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: held}); err != nil {
		t.Fatal(err)
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	retainBackupRepositoryOnMembers(context.Background(), master, membership)
	if _, err := GetPoolBackupRepository(member, "repo-stale"); err == nil {
		t.Fatal("member kept a repository the master does not hold")
	}
	if _, err := GetPoolBackupRepository(member, "repo-kept"); err != nil {
		t.Fatalf("member lost a repository the master holds: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestForgetBackupRepositoryRetiresSealedCredential(t *testing.T) {
	s := seedBackupMaster(t)
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "private-example-secret"}}
	receipt, err := saveBackupCredential(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Cloud", Kind: protocol.BackupRepoKindS3,
		CredentialRef: receipt.Receipt, S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups"},
		RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)}}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	if err := forgetPoolBackupRepository(context.Background(), s, protocol.ForgetRepositoryRequest{RepositoryID: "repo-1", Confirmation: "Cloud"}); err != nil {
		t.Fatal(err)
	}
	path, err := backupCredentialPath(s, handoff.Offer.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || strings.Contains(string(data), handoff.Credentials.Secret) {
		t.Fatalf("forgotten repository kept its secret: %v", err)
	}
	if _, err := resolveBackupCredential(s, repo, ""); err == nil {
		t.Fatal("retired credential still resolves")
	}
}

// A crash after the repository record is gone must still retry detach from the pending record.
func TestForgetRetriesPendingDetachWhenRecordGone(t *testing.T) {
	var detached []string
	previous := detachBackupRepositoryNative
	detachBackupRepositoryNative = func(_ context.Context, repo protocol.BackupRepository) error {
		detached = append(detached, repo.ID)
		return nil
	}
	t.Cleanup(func() { detachBackupRepositoryNative = previous })

	s := seedBackupMaster(t)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir()}
	if err := rememberPendingBackupDetach(s, repo); err != nil {
		t.Fatal(err)
	}
	if err := forgetPoolBackupRepository(context.Background(), s, protocol.ForgetRepositoryRequest{RepositoryID: "repo-1"}); err != nil {
		t.Fatal(err)
	}
	if len(detached) != 1 || detached[0] != "repo-1" {
		t.Fatalf("pending detach was not retried: %v", detached)
	}
	if _, err := os.Lstat(pendingBackupDetachPath(s, "repo-1")); !os.IsNotExist(err) {
		t.Fatalf("pending detach left behind: %v", err)
	}
}

func TestRetainDoesNotDeleteNewlyAdoptedRegistration(t *testing.T) {
	previous := detachBackupRepositoryNative
	detached := false
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error {
		detached = true
		return nil
	}
	t.Cleanup(func() { detachBackupRepositoryNative = previous })
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	startBackupForgetOwner(t, member)
	stale := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "stale", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/stale"}, MountPath: t.TempDir()}
	fresh := stale
	fresh.Name = "adopted"
	fresh.UpdatedAt = time.Now().UTC()
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{fresh.ID: fresh}}); err != nil {
		t.Fatal(err)
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	peer := poolConfirmationPeers(membership, master.State.HostID)[0]
	if err := postPeerBackupRoute(context.Background(), master, peer, "/retain", peerRetainRepositories{Repositories: []protocol.BackupRepository{stale}}); err != nil {
		t.Fatal(err)
	}
	got, err := GetPoolBackupRepository(member, fresh.ID)
	if err != nil || !sameBackupRepositoryRegistration(got, fresh) || detached {
		t.Fatalf("stale snapshot changed adopted repository: %+v %v detached=%v", got, err, detached)
	}
	// Even the exact current member snapshot must not be swept once the master holds it.
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{fresh.ID: fresh}}); err != nil {
		t.Fatal(err)
	}
	if err := forgetStalePeerRepository(context.Background(), master, peer, fresh, "/retain"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetPoolBackupRepository(member, fresh.ID); err != nil || detached {
		t.Fatalf("current master registration was swept: %v detached=%v", err, detached)
	}
}

func TestMemberForgetRefusesJournaledRegister(t *testing.T) {
	previous := detachBackupRepositoryNative
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error { return nil }
	t.Cleanup(func() { detachBackupRepositoryNative = previous })
	member := newTransferLab(t, 1).host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir()}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	member.State.Journal = map[string]JournalEntry{"register-1": {Status: "pending", Command: &protocol.Command{
		OperationID: "register-1", Kind: "backup-repository-register", BackupRepository: &repo}}}
	if err := forgetMemberBackupRepository(context.Background(), member, "repo-1", &repo); err == nil {
		t.Fatal("member forgot a repository its journal still registers")
	}
	member.State.Journal["register-1"] = JournalEntry{Status: "failed", Command: member.State.Journal["register-1"].Command}
	if err := forgetMemberBackupRepository(context.Background(), member, "repo-1", &repo); err != nil {
		t.Fatal(err)
	}
}

func TestForgetRefusesNeedsAttentionRun(t *testing.T) {
	s := seedBackupMaster(t)
	repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups"}, MountPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	run := protocol.BackupRun{OperationID: "backup-1", Kind: protocol.BackupKindBackupNow, PoolID: repo.PoolID, VMID: "vm-1", RepositoryID: repo.ID,
		Status: "needs_attention", Stage: "publishing", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := savePoolBackupRun(s, run); err != nil {
		t.Fatal(err)
	}
	previous := detachBackupRepositoryNative
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error {
		t.Error("unresolved publishing run detached")
		return nil
	}
	t.Cleanup(func() { detachBackupRepositoryNative = previous })
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: ptr(forgetCommand("forget-1", repo.ID, repo.Name, 0, 0))})
	if response.Err != nil || response.Result == nil || response.Result.Status != "failed" {
		t.Fatalf("forget unresolved run: %+v", response)
	}
	if _, err := GetPoolBackupRepository(s, repo.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := loadPoolBackupRun(s, run.OperationID); err != nil || got.Status != "needs_attention" {
		t.Fatalf("unresolved run removed or settled: %+v %v", got, err)
	}
}

func TestForgetExactRetryResumesDetachAndReceipt(t *testing.T) {
	for _, crashAfterDelete := range []bool{false, true} {
		t.Run(map[bool]string{false: "detach_failure", true: "receipt_failure"}[crashAfterDelete], func(t *testing.T) {
			s := seedBackupMaster(t)
			repo, err := upsertTestBackupRepository(s, protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
				Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups"}, MountPath: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			previousDetach, previousWrite := detachBackupRepositoryNative, writeBackupMetadataReceipt
			attempts := 0
			detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error {
				attempts++
				if !crashAfterDelete && attempts == 1 {
					return errors.New("temporary detach failure")
				}
				return nil
			}
			t.Cleanup(func() { detachBackupRepositoryNative, writeBackupMetadataReceipt = previousDetach, previousWrite })
			writes := 0
			writeBackupMetadataReceipt = func(path string, data []byte, mode os.FileMode) error {
				writes++
				if crashAfterDelete && writes == 2 {
					return errors.New("receipt persistence interrupted")
				}
				return previousWrite(path, data, mode)
			}
			request := BackupRequest{Command: ptr(forgetCommand("forget-1", repo.ID, repo.Name, 0, 0))}
			first := acceptBackupRequest(context.Background(), s, request)
			if first.Err == nil {
				t.Fatalf("expected interrupted forget: %+v", first)
			}
			second := acceptBackupRequest(context.Background(), s, request)
			if second.Err != nil || second.Result == nil || second.Result.Status != "succeeded" {
				t.Fatalf("exact retry did not settle: %+v", second)
			}
			if _, err := GetPoolBackupRepository(s, repo.ID); !errors.Is(err, ErrBackupRepoNotFound) {
				t.Fatalf("repository survived retry: %v", err)
			}
			receipt, err := loadPoolBackupMetadataReceipt(s, "forget-1")
			if err != nil || receipt.Status != "succeeded" {
				t.Fatalf("receipt did not settle: %+v %v", receipt, err)
			}
		})
	}
}

func TestPeerForgetRequiresOwnerAndChecksJournal(t *testing.T) {
	member := newTransferLab(t, 1).host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups"}, MountPath: t.TempDir()}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	member.State.Journal = map[string]JournalEntry{"backup-1": {Status: "needs_attention", Command: &protocol.Command{OperationID: "backup-1",
		Kind: protocol.BackupKindBackupNow, Backup: &protocol.BackupNowRequest{RepositoryID: repo.ID}}}}
	body, err := json.Marshal(peerForgetRepository{Repository: repo})
	if err != nil {
		t.Fatal(err)
	}
	call := func() int {
		recorder := httptest.NewRecorder()
		handlePeerForgetRepository(member, recorder, httptest.NewRequest(http.MethodPost, "/forget", bytes.NewReader(body)))
		return recorder.Code
	}
	if status := call(); status != http.StatusServiceUnavailable {
		t.Fatalf("missing Store owner returned %d", status)
	}
	startBackupForgetOwner(t, member)
	if status := call(); status != http.StatusConflict {
		t.Fatalf("unresolved owner journal returned %d", status)
	}
	if _, err := GetPoolBackupRepository(member, repo.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRetainResumesMemberPendingDetachWithoutRepositoryRecord(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	startBackupForgetOwner(t, member)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups"}, MountPath: t.TempDir()}
	if err := rememberPendingBackupDetach(member, repo); err != nil {
		t.Fatal(err)
	}
	previous := detachBackupRepositoryNative
	detached := 0
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error {
		detached++
		return nil
	}
	t.Cleanup(func() { detachBackupRepositoryNative = previous })
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	retainBackupRepositoryOnMembers(context.Background(), master, membership)
	if detached != 1 {
		t.Fatalf("pending member detach was not resumed: %d", detached)
	}
	if _, err := os.Stat(pendingBackupDetachPath(member, repo.ID)); !os.IsNotExist(err) {
		t.Fatalf("pending member detach survived settlement: %v", err)
	}
}

func TestPeerRepositorySnapshotsPageStaleInventory(t *testing.T) {
	for _, healthBytes := range []int{0, 17 << 10} {
		t.Run(fmt.Sprintf("health-bytes-%d", healthBytes), func(t *testing.T) {
			lab := newTransferLab(t, 1)
			master, member := lab.host("master-1"), lab.host("member-1")
			startBackupForgetOwner(t, member)
			current := make(map[string]protocol.BackupRepository)
			mirrors := make(map[string]protocol.BackupRepository)
			for i := range 256 {
				id := fmt.Sprintf("repo-%03d", i)
				repo := protocol.BackupRepository{ID: id, PoolID: "pool-1", Name: id, Kind: protocol.BackupRepoKindNFS,
					Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups/" + id}}
				current[id] = repo
				repo.Health.Error = strings.Repeat("x", healthBytes)
				mirrors[id] = repo
			}
			for _, id := range []string{"z-stale", "z-targeted"} {
				mirrors[id] = protocol.BackupRepository{ID: id, PoolID: "pool-1", Name: id, Kind: protocol.BackupRepoKindNFS,
					Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups/" + id}}
			}
			pending := protocol.BackupRepository{ID: "z-pending", PoolID: "pool-1", Name: "pending", Kind: protocol.BackupRepoKindNFS,
				Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups/pending"}}
			if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: current}); err != nil {
				t.Fatal(err)
			}
			if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: mirrors}); err != nil {
				t.Fatal(err)
			}
			if err := rememberPendingBackupDetach(member, pending); err != nil {
				t.Fatal(err)
			}
			membership, err := Membership(master)
			if err != nil {
				t.Fatal(err)
			}
			peer := poolConfirmationPeers(membership, master.State.HostID)[0]
			client, err := poolTransferClient(master, peer.PeerOrigin, peer.HostID)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			seen := make(map[string]protocol.BackupRepository)
			after, pages, aggregateBytes := "", 0, 0
			for {
				endpoint := peer.PeerOrigin + poolBackupPeerPath + "/retain"
				if after != "" {
					endpoint += "?after_id=" + after
				}
				response, err := client.Get(endpoint)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
				response.Body.Close()
				if err != nil || response.StatusCode != http.StatusOK || len(raw) > 4<<20 {
					t.Fatalf("unbounded/refused page: status=%d bytes=%d err=%v", response.StatusCode, len(raw), err)
				}
				var page peerRepositorySnapshots
				if err := json.Unmarshal(raw, &page); err != nil {
					t.Fatal(err)
				}
				if len(page.Repositories) > 256 || len(page.Repositories) == 0 {
					t.Fatalf("invalid page count: %d", len(page.Repositories))
				}
				if healthBytes != 0 && pages == 0 && len(page.Repositories) >= 256 {
					t.Fatal("page did not split at the byte boundary")
				}
				for _, repo := range page.Repositories {
					if repo.ID <= after {
						t.Fatalf("page did not advance: %q after %q", repo.ID, after)
					}
					after = repo.ID
					seen[repo.ID] = repo
				}
				pages++
				aggregateBytes += len(raw)
				if page.NextAfterID == "" {
					break
				}
				if string(page.NextAfterID) != after {
					t.Fatalf("cursor %q does not identify last row %q", page.NextAfterID, after)
				}
			}
			want := make(map[string]protocol.BackupRepository)
			for id, repo := range mirrors {
				want[id] = repo
			}
			want[pending.ID] = pending
			if !reflect.DeepEqual(seen, want) || pages < 2 || (healthBytes != 0 && aggregateBytes <= 4<<20) {
				t.Fatalf("incomplete inventory: pages=%d bytes=%d got=%d want=%d", pages, aggregateBytes, len(seen), len(want))
			}
			snapshots, err := getPeerBackupRepositorySnapshots(context.Background(), master, peer)
			if err != nil || len(snapshots) != len(want) {
				t.Fatalf("client rejected complete stale inventory: rows=%d err=%v", len(snapshots), err)
			}
			detached := make(map[string]int)
			previous := detachBackupRepositoryNative
			detachBackupRepositoryNative = func(_ context.Context, repo protocol.BackupRepository) error {
				detached[repo.ID]++
				return nil
			}
			t.Cleanup(func() { detachBackupRepositoryNative = previous })
			if err := deliverPeerForgetRepository(context.Background(), master, peer, "z-targeted"); err != nil {
				t.Fatal(err)
			}
			if _, err := GetPoolBackupRepository(member, "z-targeted"); !errors.Is(err, ErrBackupRepoNotFound) {
				t.Fatalf("targeted stale row survived: %v", err)
			}
			retainBackupRepositoryOnMembers(context.Background(), master, membership)
			remaining, err := loadPoolBackupRepositories(member)
			if err != nil || len(remaining.Repositories) != len(current) {
				t.Fatalf("sweep did not preserve current inventory: rows=%d err=%v", len(remaining.Repositories), err)
			}
			for id, repo := range current {
				if !sameBackupRepositoryRegistration(remaining.Repositories[id], repo) {
					t.Fatalf("current repository %s changed", id)
				}
			}
			if !reflect.DeepEqual(detached, map[string]int{"z-targeted": 1, "z-stale": 1, "z-pending": 1}) {
				t.Fatalf("wrong mounts detached: %v", detached)
			}
			if _, err := os.Stat(pendingBackupDetachPath(member, pending.ID)); !os.IsNotExist(err) {
				t.Fatalf("recordless pending detach survived: %v", err)
			}
		})
	}
}

func TestPeerRepositoryPagingPreservesConcurrentRegistration(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	startBackupForgetOwner(t, member)
	current := make(map[string]protocol.BackupRepository)
	mirrors := make(map[string]protocol.BackupRepository)
	for i := range 256 {
		id := fmt.Sprintf("c-current-%03d", i)
		repo := protocol.BackupRepository{ID: id, PoolID: "pool-1", Name: id, Kind: protocol.BackupRepoKindNFS,
			Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups/" + id}}
		current[id], mirrors[id] = repo, repo
	}
	old := protocol.BackupRepository{ID: "a-stale", PoolID: "pool-1", Name: "old", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups/old"}}
	mirrors[old.ID] = old
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: current}); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: mirrors}); err != nil {
		t.Fatal(err)
	}
	fresh := old
	fresh.Name = "new registration"
	inserted := protocol.BackupRepository{ID: "b-inserted", PoolID: "pool-1", Name: "inserted", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups/inserted"}}
	changed := false
	lab.intercept.Store(func(hostID string, r *http.Request) {
		if hostID != member.State.HostID || r.Method != http.MethodGet || r.URL.Path != poolBackupPeerPath+"/retain" ||
			r.URL.Query().Get("after_id") == "" || changed {
			return
		}
		changed = true
		poolBackupAuthorityMu.Lock()
		defer poolBackupAuthorityMu.Unlock()
		mirrors[fresh.ID], mirrors[inserted.ID] = fresh, inserted
		if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: mirrors}); err != nil {
			t.Error(err)
		}
	})
	previous := detachBackupRepositoryNative
	detached := false
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error {
		detached = true
		return nil
	}
	t.Cleanup(func() { detachBackupRepositoryNative = previous })
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	retainBackupRepositoryOnMembers(context.Background(), master, membership)
	remaining, err := loadPoolBackupRepositories(member)
	if err != nil || !changed || detached || len(remaining.Repositories) != 258 {
		t.Fatalf("concurrent adoption was swept: rows=%d changed=%v detached=%v err=%v", len(remaining.Repositories), changed, detached, err)
	}
	if !sameBackupRepositoryRegistration(remaining.Repositories[fresh.ID], fresh) ||
		!sameBackupRepositoryRegistration(remaining.Repositories[inserted.ID], inserted) {
		t.Fatal("changed or newly inserted registration was removed")
	}
	for id, repo := range current {
		if !sameBackupRepositoryRegistration(remaining.Repositories[id], repo) {
			t.Fatalf("current repository %s changed", id)
		}
	}
}

func TestPeerRepositorySnapshotClientRejectsBrokenPages(t *testing.T) {
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary"}
	row, err := json.Marshal(repo)
	if err != nil {
		t.Fatal(err)
	}
	first := `{"repositories":[` + string(row) + `],"next_after_id":"repo-1"}`
	tests := []struct {
		name   string
		pages  []string
		refuse bool
	}{
		{name: "unknown-field", pages: []string{`{"repositories":[],"held":[]}`}},
		{name: "trailing-json", pages: []string{`{"repositories":[]} {}`}},
		{name: "oversized-whitespace", pages: []string{`{"repositories":[]}` + strings.Repeat(" ", 4<<20)}},
		{name: "missing-inventory", pages: []string{`{}`}},
		{name: "null-inventory", pages: []string{`{"repositories":null}`}},
		{name: "over-count", pages: []string{`{"repositories":[` + strings.TrimSuffix(strings.Repeat(string(row)+",", 257), ",") + `]}`}},
		{name: "duplicate-id", pages: []string{`{"repositories":[` + string(row) + `,` + string(row) + `]}`}},
		{name: "unordered-id", pages: []string{`{"repositories":[{"id":"repo-2","pool_id":"pool-1"},` + string(row) + `]}`}},
		{name: "invalid-id", pages: []string{`{"repositories":[{"id":"../repo","pool_id":"pool-1"}]}`}},
		{name: "wrong-pool", pages: []string{`{"repositories":[{"id":"repo-1","pool_id":"other-pool"}]}`}},
		{name: "empty-nonterminal-page", pages: []string{`{"repositories":[],"next_after_id":"repo-1"}`}},
		{name: "cursor-not-last-id", pages: []string{`{"repositories":[` + string(row) + `],"next_after_id":"repo-2"}`}},
		{name: "invalid-cursor", pages: []string{`{"repositories":[` + string(row) + `],"next_after_id":"../repo"}`}},
		{name: "null-cursor", pages: []string{`{"repositories":[` + string(row) + `],"next_after_id":null}`}},
		{name: "empty-cursor", pages: []string{`{"repositories":[` + string(row) + `],"next_after_id":""}`}},
		{name: "nonstring-cursor", pages: []string{`{"repositories":[` + string(row) + `],"next_after_id":123}`}},
		{name: "nonprogress-page", pages: []string{first, first}},
		{name: "backwards-page", pages: []string{first, `{"repositories":[{"id":"repo-0","pool_id":"pool-1"}]}`}},
		{name: "later-page-refused", pages: []string{first}, refuse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lab := newTransferLab(t, 1)
			master, member := lab.host("master-1"), lab.host("member-1")
			config, err := PoolPeerTLSConfig(member)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := AuthenticatePoolMaster(member, r); err != nil {
					http.Error(w, "master rejected", http.StatusUnauthorized)
					return
				}
				if calls > 0 && r.URL.Query().Get("after_id") != "repo-1" {
					t.Errorf("client did not send last observed cursor: %s", r.URL.RawQuery)
				}
				index := calls
				calls++
				if tt.refuse && index >= len(tt.pages) {
					http.Error(w, "inventory unavailable", http.StatusServiceUnavailable)
					return
				}
				if index >= len(tt.pages) {
					t.Error("client continued after a broken page")
					http.Error(w, "unexpected page", http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(w, tt.pages[index])
			}))
			server.TLS = config
			server.StartTLS()
			t.Cleanup(server.Close)
			peer := protocol.PoolMember{HostID: member.State.HostID, PeerOrigin: server.URL}
			got, err := getPeerBackupRepositorySnapshots(context.Background(), master, peer)
			if err == nil || got != nil {
				t.Fatalf("partial/broken inventory accepted: rows=%d err=%v", len(got), err)
			}
			if !tt.refuse && !errors.Is(err, ErrBackupInvalid) {
				t.Fatalf("wrong malformed-page error: %v", err)
			}
			if calls != len(tt.pages) && !tt.refuse {
				t.Fatalf("unexpected page requests: got=%d want=%d", calls, len(tt.pages))
			}
			if tt.refuse && (calls != 2 || !strings.Contains(err.Error(), "status 503")) {
				t.Fatalf("refusal lost after a valid first page: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestPeerRepositorySnapshotGETValidatesCursorAndOwner(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	client, err := poolTransferClient(master, lab.origin[member.State.HostID], member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	endpoint := lab.origin[member.State.HostID] + poolBackupPeerPath + "/retain"
	for _, query := range []string{"after_id=", "after_id=..", "after_id=%2Frepo", "after_id=repo-1&after_id=repo-2",
		"after_id=" + strings.Repeat("a", 65), "unknown=repo-1", "after_id=%zz", "after_id=repo-1;unknown=repo-2"} {
		response, err := client.Get(endpoint + "?" + query)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid query %q returned %d", query, response.StatusCode)
		}
	}
	response, err := client.Get(endpoint + "?after_id=repo-1")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("snapshot bypassed missing Store owner: %d", response.StatusCode)
	}
	recorder := httptest.NewRecorder()
	PoolBackupPeerHandler(member).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, poolBackupPeerPath+"/retain", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated inventory was exposed: %d", recorder.Code)
	}
	startBackupForgetOwner(t, member)
	response, err = client.Get(endpoint + "?after_id=repo-1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body peerRepositorySnapshots
	if err := decodePeerRepositoryBody(response.Body, 4<<20, &body); err != nil ||
		response.StatusCode != http.StatusOK || body.Repositories == nil || len(body.Repositories) != 0 || body.NextAfterID != "" {
		t.Fatalf("valid terminal cursor did not produce empty inventory: %+v status=%d err=%v", body, response.StatusCode, err)
	}
}

func TestPeerRepositorySnapshotOversizedRowFailsClosed(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	startBackupForgetOwner(t, member)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary",
		Health: protocol.BackupRepositoryHealth{Error: strings.Repeat("x", 4<<20)}}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	peer := protocol.PoolMember{HostID: member.State.HostID, PeerOrigin: lab.origin[member.State.HostID]}
	got, err := getPeerBackupRepositorySnapshots(context.Background(), master, peer)
	if err == nil || got != nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("oversized snapshot was omitted or accepted: rows=%d err=%v", len(got), err)
	}
	if _, err := GetPoolBackupRepository(member, repo.ID); err != nil {
		t.Fatalf("oversized snapshot modified registration: %v", err)
	}
}

func TestPeerRepositoryMutationBodiesRemainBounded(t *testing.T) {
	member := newTransferLab(t, 1).host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary"}
	forget, err := json.Marshal(peerForgetRepository{Repository: repo})
	if err != nil {
		t.Fatal(err)
	}
	repos := make([]protocol.BackupRepository, 257)
	for i := range repos {
		repos[i] = protocol.BackupRepository{ID: fmt.Sprintf("repo-%03d", i), PoolID: "pool-1", Name: "Primary"}
	}
	retain, err := json.Marshal(peerRetainRepositories{Repositories: repos})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		body    string
		handler func(*Store, http.ResponseWriter, *http.Request)
	}{
		{name: "forget-bytes", body: string(forget) + strings.Repeat(" ", 64<<10), handler: handlePeerForgetRepository},
		{name: "forget-trailing", body: string(forget) + `{}`, handler: handlePeerForgetRepository},
		{name: "retain-bytes", body: `{"repositories":[]}` + strings.Repeat(" ", 4<<20), handler: handlePeerRetainRepositories},
		{name: "retain-count", body: string(retain), handler: handlePeerRetainRepositories},
		{name: "retain-trailing", body: `{"repositories":[]} {}`, handler: handlePeerRetainRepositories},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			tt.handler(member, response, httptest.NewRequest(http.MethodPost, "/retain", strings.NewReader(tt.body)))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("unbounded/non-exact body returned %d", response.Code)
			}
		})
	}
}

func TestPeerRepositoryTargetedForgetOversizedBodyFailsClosed(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	startBackupForgetOwner(t, member)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Health: protocol.BackupRepositoryHealth{Error: strings.Repeat("x", 64<<10)}}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	previous := detachBackupRepositoryNative
	detached := 0
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error {
		detached++
		return nil
	}
	t.Cleanup(func() { detachBackupRepositoryNative = previous })
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	peer := poolConfirmationPeers(membership, master.State.HostID)[0]
	if err := deliverPeerForgetRepository(context.Background(), master, peer, repo.ID); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("oversized targeted forget did not fail closed: %v", err)
	}
	if got, err := GetPoolBackupRepository(member, repo.ID); err != nil || !sameBackupRepositoryRegistration(got, repo) || detached != 0 {
		t.Fatalf("refused targeted forget modified repository: err=%v detached=%d", err, detached)
	}
	retainBackupRepositoryOnMembers(context.Background(), master, membership)
	if _, err := GetPoolBackupRepository(member, repo.ID); !errors.Is(err, ErrBackupRepoNotFound) || detached != 1 {
		t.Fatalf("bounded retain could not remove observed stale registration: err=%v detached=%d", err, detached)
	}
}
