// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"os"
	"path/filepath"
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

// A retain sweep refreshes the held ID list per member so a repo registered mid-sweep is kept.
func TestRetainSweepRefreshesHeldRepositories(t *testing.T) {
	previous := detachBackupRepositoryNative
	detachBackupRepositoryNative = func(context.Context, protocol.BackupRepository) error { return nil }
	t.Cleanup(func() { detachBackupRepositoryNative = previous })

	lab := newTransferLab(t, 2)
	master := lab.host("master-1")
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	peers := poolConfirmationPeers(membership, master.State.HostID)
	if len(peers) < 2 {
		t.Fatalf("need two ready members, got %d", len(peers))
	}
	stale := protocol.BackupRepository{ID: "repo-stale", PoolID: "pool-1", Name: "stale", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/stale"}, MountPath: t.TempDir()}
	for _, peer := range peers {
		if err := savePoolBackupRepositories(lab.host(peer.HostID), poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{stale.ID: stale}}); err != nil {
			t.Fatal(err)
		}
	}
	// First member sees an empty hold list (as if the sweep started before any registration).
	if err := postPeerBackupRoute(context.Background(), master, peers[0], "/retain", peerRetainRepositories{RepositoryIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := GetPoolBackupRepository(lab.host(peers[0].HostID), "repo-stale"); err == nil {
		t.Fatal("first member kept a stale repository")
	}
	fresh, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-new", PoolID: "pool-1", Name: "new", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/new"}, MountPath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRepositories(lab.host(peers[1].HostID), poolBackupRepositoriesState{
		Repositories: map[string]protocol.BackupRepository{stale.ID: stale, fresh.ID: fresh},
	}); err != nil {
		t.Fatal(err)
	}
	// Second member is retained against a refreshed list that includes the mid-sweep registration.
	if err := postPeerBackupRoute(context.Background(), master, peers[1], "/retain", peerRetainRepositories{RepositoryIDs: []string{fresh.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := GetPoolBackupRepository(lab.host(peers[1].HostID), "repo-stale"); err == nil {
		t.Fatal("second member kept a stale repository")
	}
	if _, err := GetPoolBackupRepository(lab.host(peers[1].HostID), "repo-new"); err != nil {
		t.Fatalf("second member dropped a repository registered mid-sweep: %v", err)
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
	if err := forgetMemberBackupRepository(context.Background(), member, "repo-1"); err == nil {
		t.Fatal("member forgot a repository its journal still registers")
	}
	member.State.Journal["register-1"] = JournalEntry{Status: "failed", Command: member.State.Journal["register-1"].Command}
	if err := forgetMemberBackupRepository(context.Background(), member, "repo-1"); err != nil {
		t.Fatal(err)
	}
}
