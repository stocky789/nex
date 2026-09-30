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
	refused("forget-pinned", "Repo repo-1", 2, 1, "in use by an unfinished restore")
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
