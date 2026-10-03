// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestExistingS3RegistrationNeverReclaimsMissingOwner(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact_retry", true: "confirmed_replacement"}[replace], func(t *testing.T) {
			s := seedBackupMaster(t)
			fake := &fakeS3{objects: map[string][]byte{}}
			server := httptest.NewTLSServer(fake)
			t.Cleanup(server.Close)
			previous := newBackupS3Client
			newBackupS3Client = func(options s3.Options) *s3.Client {
				options.HTTPClient = server.Client()
				return s3.New(options)
			}
			t.Cleanup(func() { newBackupS3Client = previous })
			receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
				Offer: protocol.StorageCredentialOffer{RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
					PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute)},
				Credentials: protocol.StorageCredentials{Username: "test-key", Secret: "test-secret"},
			})
			if err != nil {
				t.Fatal(err)
			}
			repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Cloud", Kind: protocol.BackupRepoKindS3,
				S3Source: &protocol.BackupS3Source{Endpoint: server.URL, Bucket: "backups", Prefix: "pool-1"}, CredentialRef: receipt.Receipt}
			prepared, err := prepareBackupRepositoryForRegistration(context.Background(), s, repo, "register-1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := upsertPoolBackupRepositoryPrepared(s, repo, prepared, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			delete(fake.objects, "pool-1/"+s3BackupOwnerKey)
			fake.mu.Unlock()
			confirmation := ""
			if replace {
				confirmation = repo.ID
			}
			if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, repo, "register-1", confirmation, nil); err == nil {
				t.Fatal("existing registration claimed a missing destination owner")
			}
			fake.mu.Lock()
			_, reclaimed := fake.objects["pool-1/"+s3BackupOwnerKey]
			fake.mu.Unlock()
			if reclaimed {
				t.Fatal("missing owner marker was recreated")
			}
			got, err := GetPoolBackupRepository(s, saved.ID)
			if err != nil || !sameBackupRepositoryRegistration(got, saved) {
				t.Fatalf("failed verify changed durable registration: %+v %v", got, err)
			}
			if _, err := localBackupCredential(s, saved); err != nil {
				t.Fatalf("active credential retired: %v", err)
			}
		})
	}
}

func TestPreparedRegistrationCommitDoesNotProbeRepository(t *testing.T) {
	s := seedBackupMaster(t)
	requested := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups"}}
	prepared := requested
	prepared.MountPath = t.TempDir()
	prepared.Identity = &protocol.NFSStorageIdentity{Server: requested.Source.Server, Export: requested.Source.Export, FilesystemID: "test-nfs"}
	prepared.Health = protocol.BackupRepositoryHealth{Status: "offline", Error: "preparation probe failed", ObservedAt: time.Now().UTC()}
	prepared.Access = protocol.StorageAccess{Status: "failed", Error: "preparation probe failed", ObservedAt: prepared.Health.ObservedAt}
	previous := verifyBackupRepository
	verifyBackupRepository = func(context.Context, protocol.BackupRepository) error {
		t.Error("owner commit repeated repository I/O")
		return errors.New("unexpected probe")
	}
	t.Cleanup(func() { verifyBackupRepository = previous })
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &protocol.Command{OperationID: "register-1",
		Kind: "backup-repository-register", BackupRepository: &requested}, PreparedRepository: &prepared})
	if response.Err != nil || response.Result == nil || response.Result.Status != "succeeded" {
		t.Fatalf("prepared commit failed: %+v", response)
	}
	got, err := GetPoolBackupRepository(s, requested.ID)
	if err != nil || got.Health.Status != "offline" || got.Health.Error != prepared.Health.Error || !got.Health.ObservedAt.Equal(prepared.Health.ObservedAt) {
		t.Fatalf("prepared health was lost or fabricated: %+v %v", got.Health, err)
	}
}

func TestRetainSnapshotPreservesActiveMemberCredential(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	startBackupForgetOwner(t, member)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Cloud", Kind: protocol.BackupRepoKindS3,
		S3Source:       &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups"},
		RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)}}
	repo.CredentialRef = peerTestBackupCredential(t, master, "credential-old", "register-old", "old-example-secret")
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	if failed := projectBackupCredentialToMembers(context.Background(), master, membership, repo); len(failed) != 0 {
		t.Fatal(failed)
	}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	stale := repo
	repo.CredentialRef = peerTestBackupCredential(t, master, "credential-new", "register-new", "new-example-secret")
	repo.UpdatedAt = time.Now().UTC()
	if failed := projectBackupCredentialToMembers(context.Background(), master, membership, repo); len(failed) != 0 {
		t.Fatal(failed)
	}
	poolBackupAuthorityMu.Lock()
	err = adoptPeerBackupRepository(member, BackupRequest{}, repo, repo)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	peer := poolConfirmationPeers(membership, master.State.HostID)[0]
	if err := postPeerBackupRoute(context.Background(), master, peer, "/retain", peerRetainRepositories{Repositories: []protocol.BackupRepository{stale}}); err != nil {
		t.Fatal(err)
	}
	if got, err := localBackupCredential(member, repo); err != nil || got.Credentials.Secret != "new-example-secret" {
		t.Fatalf("stale sweep retired active adopted credential: %v", err)
	}
	if got, err := GetPoolBackupRepository(member, repo.ID); err != nil || got.CredentialRef != repo.CredentialRef {
		t.Fatalf("stale sweep removed adoption: %+v %v", got, err)
	}
}

func TestSMBPreparationFreezeRefusesCommitAndRestoresPriorMount(t *testing.T) {
	s := seedBackupMaster(t)
	oldRef := readableRemoteCredential(t, s, protocol.BackupRepoKindSMB3)
	previous := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Share", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		SMBIdentity:   &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)},
		CredentialRef: oldRef, MountPath: t.TempDir()}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{previous.ID: previous}}); err != nil {
		t.Fatal(err)
	}
	receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
		Offer: protocol.StorageCredentialOffer{RequestID: "credential-new", RegistrationRequestID: "backup-credential:smb3:register-new",
			PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: previous.ID, ExpiresAt: time.Now().Add(time.Minute)},
		Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "new-example-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requested := previous
	requested.MountPath, requested.SMBIdentity, requested.CredentialRef = "", nil, receipt.Receipt
	priorPrepare, priorVerify, priorRemove := prepareBackupRepositorySMBNative, verifyBackupRepository, removeBackupSMBCredentialNative
	t.Cleanup(func() {
		prepareBackupRepositorySMBNative, verifyBackupRepository, removeBackupSMBCredentialNative = priorPrepare, priorVerify, priorRemove
	})
	started, release := make(chan struct{}), make(chan struct{})
	activeRef := previous.CredentialRef
	prepareBackupRepositorySMBNative = func(_ context.Context, repo protocol.BackupRepository, credentials protocol.StorageCredentials) (protocol.BackupRepository, error) {
		if repo.CredentialRef == requested.CredentialRef {
			close(started)
			<-release
		}
		activeRef = repo.CredentialRef
		repo.MountPath, repo.SMBIdentity = previous.MountPath, previous.SMBIdentity
		return repo, nil
	}
	verifyBackupRepository = func(context.Context, protocol.BackupRepository) error { return nil }
	removed := ""
	removeBackupSMBCredentialNative = func(reference string) error {
		removed = reference
		return nil
	}
	done := make(chan BackupResponse, 1)
	go func() {
		prepared, err := prepareBackupRepositoryForRegistration(context.Background(), s, requested, "register-new", previous.ID, nil)
		done <- BackupResponse{Repository: &prepared, Err: err}
	}()
	<-started
	poolBackupAuthorityMu.Lock()
	state, err := loadPoolPeer(s)
	if err == nil {
		state.Transfer = &poolTransferRecord{Frozen: true}
		err = savePoolPeer(s, state)
	}
	poolBackupAuthorityMu.Unlock()
	close(release)
	completion := <-done
	if err != nil || completion.Err != nil {
		t.Fatalf("freeze/preparation failed: %v %v", err, completion.Err)
	}
	request := BackupRequest{Command: &protocol.Command{OperationID: "register-new", Kind: "backup-repository-register",
		BackupRepository: &requested, ReplaceRepository: previous.ID}, PreparedRepository: completion.Repository, PreviousRepository: &previous}
	if response := acceptBackupRequest(context.Background(), s, request); response.Err == nil {
		t.Fatalf("frozen master committed prepared replacement: %+v", response)
	}
	if err := rollbackBackupRepositoryRegistration(context.Background(), s, request); err != nil {
		t.Fatal(err)
	}
	if activeRef != oldRef || removed != requested.CredentialRef {
		t.Fatalf("rollback did not restore prior mount/remove replacement plaintext: active=%s removed=%s", activeRef, removed)
	}
	got, err := GetPoolBackupRepository(s, previous.ID)
	if err != nil || !sameBackupRepositoryRegistration(got, previous) {
		t.Fatalf("frozen registration changed durable row: %+v %v", got, err)
	}
	if _, err := localBackupCredential(s, previous); err != nil {
		t.Fatalf("rollback retired active prior credential: %v", err)
	}
}

func TestSMBRegistrationRollbackUncertaintyRetainsCredentials(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		uncertain     bool
		commitFailure bool
	}{
		{name: "native-definite-rollback"},
		{name: "native-uncertain-rollback", uncertain: true},
		{name: "commit-definite-rollback", commitFailure: true},
		{name: "commit-uncertain-rollback", commitFailure: true, uncertain: true},
	} {
		uncertain := scenario.uncertain
		wantStatus := "failed"
		if uncertain {
			wantStatus = "needs_attention"
		}
		t.Run(scenario.name, func(t *testing.T) {
			s := seedBackupMaster(t)
			oldRef := readableRemoteCredential(t, s, protocol.BackupRepoKindSMB3)
			previous := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Share", Kind: protocol.BackupRepoKindSMB3,
				SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
				SMBIdentity: &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups",
					MarkerID: strings.Repeat("a", 32)}, CredentialRef: oldRef, MountPath: t.TempDir()}
			if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{previous.ID: previous}}); err != nil {
				t.Fatal(err)
			}
			receipt, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
				Offer: protocol.StorageCredentialOffer{RequestID: "credential-new", RegistrationRequestID: "backup-credential:smb3:register-new",
					PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: previous.ID, ExpiresAt: time.Now().Add(time.Minute)},
				Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "new-example-secret"},
			})
			if err != nil {
				t.Fatal(err)
			}
			requested := previous
			requested.MountPath, requested.SMBIdentity, requested.CredentialRef = "", nil, receipt.Receipt
			cmd := protocol.Command{OperationID: "register-new", Kind: "backup-repository-register",
				BackupRepository: &requested, ReplaceRepository: previous.ID}
			if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
				t.Fatal(err)
			}
			priorPrepare, priorRemove := prepareBackupRepositorySMBNative, removeBackupSMBCredentialNative
			defer func() {
				prepareBackupRepositorySMBNative, removeBackupSMBCredentialNative = priorPrepare, priorRemove
			}()
			activeRef := oldRef
			prepareBackupRepositorySMBNative = func(_ context.Context, repo protocol.BackupRepository, _ protocol.StorageCredentials) (protocol.BackupRepository, error) {
				if scenario.commitFailure && repo.CredentialRef == requested.CredentialRef {
					peer, err := loadPoolPeer(s)
					if err != nil {
						return protocol.BackupRepository{}, err
					}
					peer.Transfer = &poolTransferRecord{Frozen: true}
					if err := savePoolPeer(s, peer); err != nil {
						return protocol.BackupRepository{}, err
					}
					activeRef = repo.CredentialRef
					repo.MountPath, repo.SMBIdentity = previous.MountPath, previous.SMBIdentity
					return repo, nil
				}
				if scenario.commitFailure && !uncertain {
					activeRef = repo.CredentialRef
					return repo, nil
				}
				err := errors.New("native mount rollback failed")
				if uncertain {
					err = errors.Join(err, vmhost.ErrStorageRollbackUncertain)
				}
				return protocol.BackupRepository{}, err
			}
			removed := ""
			removeBackupSMBCredentialNative = func(reference string) error {
				removed = reference
				return nil
			}
			response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
			if response.Result == nil || response.Result.Status != wantStatus {
				t.Fatalf("registration outcome lost rollback certainty: %+v", response)
			}
			if scenario.commitFailure {
				if !uncertain && activeRef != oldRef || uncertain && activeRef != requested.CredentialRef {
					t.Fatalf("rollback did not preserve observed mount outcome: active=%q", activeRef)
				}
				peer, err := loadPoolPeer(s)
				if err != nil {
					t.Fatal(err)
				}
				peer.Transfer = nil
				if err := savePoolPeer(s, peer); err != nil {
					t.Fatal(err)
				}
				pending, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
				if err != nil {
					t.Fatal(err)
				}
				response = settleBackupMetadataResponse(s, cmd, pending, response)
			}
			if err := completeBackupMetadataRequest(s, cmd.OperationID, cmd, response, nil); err != nil {
				t.Fatal(err)
			}
			if entry := s.State.Journal[cmd.OperationID]; entry.Status != wantStatus {
				t.Fatalf("registration journal lost rollback certainty: %+v", entry)
			}
			metadata, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
			if err != nil || metadata.Status != wantStatus {
				t.Fatalf("registration receipt lost rollback certainty: %+v %v", metadata, err)
			}
			listed, err := BackupCredentialReceipts(s)
			if err != nil {
				t.Fatal(err)
			}
			eligible := map[string]bool{}
			for _, receipt := range listed {
				eligible[receipt.Receipt] = receipt.RetireEligible
			}
			if eligible[oldRef] || eligible[receipt.Receipt] == uncertain {
				t.Fatalf("credential eligibility ignored rollback certainty: %+v", listed)
			}
			handled, retireErr := RetireUnusedBackupCredential(s, receipt.Receipt, s.State.PoolID, s.State.HostID)
			if !handled || uncertain && retireErr == nil || !uncertain && retireErr != nil {
				t.Fatalf("replacement credential retirement ignored rollback certainty: handled=%v err=%v", handled, retireErr)
			}
			if uncertain && removed != "" || !uncertain && removed != receipt.Receipt {
				t.Fatalf("plaintext credential removal ignored rollback certainty: removed=%q", removed)
			}
			saved, err := GetPoolBackupRepository(s, previous.ID)
			if err != nil || !sameBackupRepositoryRegistration(saved, previous) {
				t.Fatalf("failed replacement changed registration: %+v %v", saved, err)
			}
			if _, err := localBackupCredential(s, previous); err != nil {
				t.Fatalf("prior credential was lost: %v", err)
			}
			if uncertain {
				if _, err := ResolveBackupCredentialForRegistration(s, requested, cmd.OperationID); err != nil {
					t.Fatalf("uncertain replacement credential was lost: %v", err)
				}
			}
		})
	}
}
