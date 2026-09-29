// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestBackupMasterAccessFollowsPeriodicProbe(t *testing.T) {
	s := seedBackupMaster(t)
	mount := t.TempDir()
	_, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	probe := func() protocol.StorageAccess {
		t.Helper()
		if err := probePoolBackupRepositories(context.Background(), s, time.Second); err != nil {
			t.Fatal(err)
		}
		repos, err := ListPoolBackupRepositories(s)
		if err != nil {
			t.Fatal(err)
		}
		return repos[0].Access
	}
	first := probe()
	if first.Status != "ok" || !first.Readable || !first.Writable || first.ObservedAt.IsZero() {
		t.Fatalf("initial master access = %+v", first)
	}
	if err := os.Remove(filepath.Join(mount, "owner.json")); err != nil {
		t.Fatal(err)
	}
	if err := vmhost.ClaimBackupRepositoryOwner(mount, "other-repo", "other-pool"); err != nil {
		t.Fatal(err)
	}
	failed := probe()
	if failed.Status != "failed" || failed.Readable || failed.Writable || failed.Error == "" || failed.ObservedAt.Before(first.ObservedAt) {
		t.Fatalf("failed master access retained success: %+v", failed)
	}
	if err := os.Remove(filepath.Join(mount, "owner.json")); err != nil {
		t.Fatal(err)
	}
	if err := vmhost.ClaimBackupRepositoryOwner(mount, "repo-1", s.State.PoolID); err != nil {
		t.Fatal(err)
	}
	recovered := probe()
	if recovered.Status != "ok" || !recovered.Readable || !recovered.Writable || recovered.Error != "" || recovered.ObservedAt.Before(failed.ObservedAt) {
		t.Fatalf("recovered master access = %+v", recovered)
	}
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		t.Fatal(err)
	}
	repo := state.Repositories["repo-1"]
	repo.Access.ObservedAt = time.Now().Add(-backupHealthFreshness - time.Second)
	state.Repositories[repo.ID] = repo
	if err := savePoolBackupRepositories(s, state); err != nil {
		t.Fatal(err)
	}
	if fresh := probe(); fresh.Status != "ok" || time.Since(fresh.ObservedAt) > backupHealthFreshness {
		t.Fatalf("periodic probe left master access stale: %+v", fresh)
	}
}

func TestBackupMemberProbeAdoptsRemoteCredentialBeforeCheckingAccess(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerS3Repository(t, master)
	projection, err := exportPeerBackupCredential(master, repo, member.State.HostID)
	if err != nil || importPeerBackupCredential(member, projection) != nil {
		t.Fatalf("member credential delivery: %v", err)
	}
	access := probePeerBackupRepository(context.Background(), member, repo)
	if access.Error != "" || !access.Readable || !access.Writable {
		t.Fatalf("new member S3 access = %+v", access)
	}
	local, err := GetPoolBackupRepository(member, repo.ID)
	if err != nil || !sameBackupProbeRepository(repo, local) {
		t.Fatalf("member repository adoption = %+v, %v", local, err)
	}
}

func TestBackupMemberProbeUsesStableSMBIdentity(t *testing.T) {
	expected := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:   &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		SMBIdentity: &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32), FilesystemID: "master-client"}, Generation: 1}
	local := expected
	identity := *expected.SMBIdentity
	identity.FilesystemID = "member-client"
	local.SMBIdentity = &identity
	if !sameBackupProbeRepository(expected, local) {
		t.Fatal("same SMB share marker was rejected across clients")
	}
	identity.MarkerID = strings.Repeat("b", 32)
	if sameBackupProbeRepository(expected, local) {
		t.Fatal("different SMB share marker was accepted")
	}
}

func TestBackupMemberAccessProbeOfflineExpiryAndRestart(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	mount := t.TempDir()
	_, err := upsertTestBackupRepository(master, protocol.BackupRepository{
		ID: "repo-1", PoolID: master.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: mount,
	})
	if err != nil {
		t.Fatal(err)
	}
	repos, err := ListPoolBackupRepositories(master)
	if err != nil || len(repos) != 1 || len(repos[0].MemberAccess) != 1 || repos[0].MemberAccess[0].Error != "awaiting member probe" {
		t.Fatalf("before actual member probe: %+v, %v", repos, err)
	}
	client, err := PoolMemberPeerClient(master, lab.origin[member.State.HostID], member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	unsupported := repos[0]
	unsupported.Kind = "unsupported"
	body, _ := json.Marshal(unsupported)
	request, err := http.NewRequest(http.MethodPost, lab.origin[member.State.HostID]+poolBackupPeerPath+"/access", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil || response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported member probe: response=%v error=%v", response, err)
	}
	response.Body.Close()
	client.CloseIdleConnections()
	previousPrepare := prepareBackupRepositoryNative
	t.Cleanup(func() { prepareBackupRepositoryNative = previousPrepare })
	prepareBackupRepositoryNative = func(_ context.Context, portable protocol.BackupRepository) (protocol.BackupRepository, error) {
		portable.MountPath = mount
		portable.Identity = repos[0].Identity
		return portable, nil
	}
	probe := func() protocol.PoolStorageAccess {
		t.Helper()
		if err := probePoolBackupRepositories(context.Background(), master, time.Second); err != nil {
			t.Fatal(err)
		}
		listed, err := ListPoolBackupRepositories(master)
		if err != nil {
			t.Fatal(err)
		}
		return listed[0].MemberAccess[0]
	}
	if access := probe(); !access.Readable || !access.Writable || access.Error != "" || access.ObservedAt.IsZero() {
		t.Fatalf("member probe success: %+v", access)
	}
	stored, err := loadPoolBackupRepositories(master)
	if err != nil || len(stored.Repositories["repo-1"].MemberAccess) != 0 {
		t.Fatalf("transient member access became durable: %+v, %v", stored, err)
	}
	memberRepo := repos[0]
	memberRepo.Generation++
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{memberRepo.ID: memberRepo}}); err != nil {
		t.Fatal(err)
	}
	if access := probe(); access.Readable || access.Writable || access.Error == "" {
		t.Fatalf("old member repository generation retained success: %+v", access)
	}
	memberRepo.Generation--
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{memberRepo.ID: memberRepo}}); err != nil {
		t.Fatal(err)
	}
	lab.drop(member.State.HostID, "!/access")
	if access := probe(); access.Readable || access.Writable || access.Error == "" || access.ObservedAt.IsZero() {
		t.Fatalf("offline member retained success: %+v", access)
	}
	lab.drop(member.State.HostID, "")
	if access := probe(); !access.Readable || !access.Writable {
		t.Fatalf("member recovery: %+v", access)
	}
	backupMemberAccessMu.Lock()
	access := backupMemberAccess[master]["repo-1"][member.State.HostID]
	access.ObservedAt = time.Now().Add(-backupHealthFreshness - time.Second)
	backupMemberAccess[master]["repo-1"][member.State.HostID] = access
	backupMemberAccessMu.Unlock()
	repos, err = ListPoolBackupRepositories(master)
	if err != nil || repos[0].MemberAccess[0].Readable || repos[0].MemberAccess[0].Error != "awaiting member probe" {
		t.Fatalf("expired success: %+v, %v", repos, err)
	}
	backupMemberAccessMu.Lock()
	access.ObservedAt = time.Now().Add(time.Hour)
	backupMemberAccess[master]["repo-1"][member.State.HostID] = access
	backupMemberAccessMu.Unlock()
	repos, err = ListPoolBackupRepositories(master)
	if err != nil || repos[0].MemberAccess[0].Readable || repos[0].MemberAccess[0].Error != "awaiting member probe" {
		t.Fatalf("future member clock revived success: %+v, %v", repos, err)
	}
	restarted := &Store{dir: master.dir, State: master.State}
	repos, err = ListPoolBackupRepositories(restarted)
	if err != nil || repos[0].MemberAccess[0].Readable || repos[0].MemberAccess[0].Error != "awaiting member probe" {
		t.Fatalf("restarted Core reused success: %+v, %v", repos, err)
	}
}
