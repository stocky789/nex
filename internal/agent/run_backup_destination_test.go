// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestReportRestoreMissingPointFailsBeforeReservation(t *testing.T) {
	s := seedBackupMaster(t)
	if _, err := upsertTestBackupRepository(s, protocol.BackupRepository{
		ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"}, MountPath: t.TempDir(),
	}); err != nil {
		t.Fatal(err)
	}
	cmd := protocol.Command{OperationID: "restore-missing-point", Kind: "backup-restore-as-new",
		Restore: &protocol.RestoreAsNewRequest{RepositoryID: "repo-1", RecoveryPointID: "point-missing",
			NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "restored", StorageID: "storage-1"}}
	// A failed reservation with no durable run has not admitted native work.
	// This differs from an already admitted run whose outcome became unknown.
	vm := *poolVMTestSpec("vm-1", s.State.HostID)
	uncertain := protocol.Command{OperationID: "backup-reserve-uncertain", Kind: protocol.BackupKindBackupNow, VM: &vm,
		Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1", Consistency: protocol.BackupCaptureStopped}}
	previousReserve := reserveBackupRun
	reserveBackupRun = func(*Store, protocol.BackupRun) (protocol.BackupRun, error) {
		return protocol.BackupRun{}, errors.New("run record write outcome unknown")
	}
	t.Cleanup(func() { reserveBackupRun = previousReserve })
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd, uncertain}}); err != nil {
		t.Fatal(err)
	}
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan error, 1)
	defer cancel()
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: 100 * time.Millisecond, ProbeInterval: time.Second, ProbeTimeout: 20 * time.Millisecond})
	}()
	for {
		raw, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
		var observed State
		if err == nil && json.Unmarshal(raw, &observed) == nil &&
			observed.Journal[cmd.OperationID].Status == "failed" && observed.Journal[uncertain.OperationID].Status == "failed" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Core Run stopped before refusal: %v", err)
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("Core Run did not settle the pre-admission failures")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	entry := s.State.Journal[cmd.OperationID]
	if entry.Status != "failed" || !strings.Contains(entry.Error, ErrBackupNotFound.Error()) {
		t.Fatalf("missing point journal: %+v", entry)
	}
	if entry := s.State.Journal[uncertain.OperationID]; entry.Status != "failed" {
		t.Fatalf("failed reservation journal: %+v", entry)
	}
	if _, err := loadPoolBackupRun(s, uncertain.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed reservation created a run: %v", err)
	}
	if _, err := loadPoolBackupRun(s, cmd.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing point created a run: %v", err)
	}
	if pins, err := loadPoolBackupPins(s); err != nil || len(pins.Pins) != 0 {
		t.Fatalf("missing point created a pin: pins=%+v err=%v", pins, err)
	}
	if vms, err := loadPoolVMs(s); err != nil || len(vms.VMs) != 0 {
		t.Fatalf("missing point created a VM: vms=%+v err=%v", vms, err)
	}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil || s.State.Journal[cmd.OperationID].Status != "failed" {
		t.Fatalf("exact replay changed refusal: entry=%+v err=%v", s.State.Journal[cmd.OperationID], err)
	}
}

func TestRunRegistrationShutdownRetainsReservationUntilNativeWorkStops(t *testing.T) {
	s := seedBackupMaster(t)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/backups"}}
	first := protocol.Command{OperationID: "register-first", Kind: "backup-repository-register", BackupRepository: &repo}
	second := protocol.Command{OperationID: "register-second", Kind: first.Kind, BackupRepository: &repo}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	previous := prepareBackupRepositoryNative
	prepareBackupRepositoryNative = func(context.Context, protocol.BackupRepository) (protocol.BackupRepository, error) {
		close(entered)
		<-release // Model native work that has not stopped when cancellation is requested.
		return protocol.BackupRepository{}, errors.New("native preparation stopped")
	}
	defer func() { prepareBackupRepositoryNative = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	requests, done := make(chan BackupRequest), make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: time.Hour, ProbeInterval: time.Hour,
			ProbeTimeout: 20 * time.Millisecond, BackupRequests: requests})
	}()
	stopped := false
	defer func() {
		unblock()
		cancel()
		if !stopped {
			if err := <-done; err != nil {
				t.Errorf("Core shutdown: %v", err)
			}
		}
	}()
	wait, stopWait := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopWait()
	select {
	case requests <- BackupRequest{Command: &first, Reply: make(chan BackupResponse, 1)}:
	case <-wait.Done():
		t.Fatal("registration was not admitted")
	}
	select {
	case <-entered:
	case <-wait.Done():
		t.Fatal("native preparation did not start")
	}
	cancel()
	if response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &second}); !errors.Is(response.Err, ErrBackupConflict) {
		t.Fatalf("shutdown released a still-running native preparation: %+v", response)
	}
	unblock()
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatal(err)
		}
	case <-wait.Done():
		t.Fatal("Core did not stop after native work stopped")
	}
	if receipt, err := loadPoolBackupMetadataReceipt(s, first.OperationID); err != nil || receipt.Status != "failed" {
		t.Fatalf("stopped preparation did not retain its proven failure: %+v %v", receipt, err)
	}
	prepareBackupRepositoryNative = func(context.Context, protocol.BackupRepository) (protocol.BackupRepository, error) {
		return protocol.BackupRepository{}, errors.New("later native refusal")
	}
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &second})
	if response.Result == nil || response.Result.Status != "failed" || !strings.Contains(response.Result.Error, "later native refusal") {
		t.Fatalf("shutdown leaked repository reservation: %+v", response)
	}
}

func TestRunRegistrationReplaysReceiptBeforeJournalSettlement(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "needs_attention"} {
		t.Run(status, func(t *testing.T) {
			s := seedBackupMaster(t)
			repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Share", Kind: protocol.BackupRepoKindSMB3,
				SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
				CredentialRef: readableRemoteCredential(t, s, protocol.BackupRepoKindSMB3)}
			cmd := protocol.Command{OperationID: "register-restart", Kind: "backup-repository-register", BackupRepository: &repo}
			if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{cmd}}); err != nil {
				t.Fatal(err)
			}
			if err := reserveBackupMetadataReceipt(s, cmd, false); err != nil {
				t.Fatal(err)
			}
			if status == "succeeded" {
				if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			outcome := BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID, Status: status, Error: "durable outcome", ObservedAt: time.Now().UTC()}}
			if response := settleBackupMetadataResponse(s, cmd, receipt, outcome); response.Err != nil {
				t.Fatal(response.Err)
			}
			_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
			}))
			s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
			s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
			if err := s.Save(); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reloaded, err := OpenStore(s.dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reloaded.Close()
			prior := prepareBackupRepositorySMBNative
			calls := 0
			prepareBackupRepositorySMBNative = func(context.Context, protocol.BackupRepository, protocol.StorageCredentials) (protocol.BackupRepository, error) {
				calls++
				return protocol.BackupRepository{}, errors.New("receipt replay must not prepare")
			}
			defer func() { prepareBackupRepositorySMBNative = prior }()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, reloaded, RunConfig{Heartbeat: time.Hour, ProbeInterval: time.Hour, ProbeTimeout: 20 * time.Millisecond})
			}()
			for {
				raw, readErr := os.ReadFile(filepath.Join(s.dir, "state.json"))
				var observed State
				if readErr == nil && json.Unmarshal(raw, &observed) == nil && observed.Journal[cmd.OperationID].Status == status {
					break
				}
				select {
				case <-ctx.Done():
					cancel()
					<-done
					t.Fatal("receipt was not replayed")
				case <-time.After(20 * time.Millisecond):
				}
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("terminal receipt reran native preparation %d times", calls)
			}
			if status == "succeeded" {
				if err := savePoolBackupRepositories(reloaded, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{}}); err != nil {
					t.Fatal(err)
				}
				response, settled := terminalBackupMetadataResponse(reloaded, BackupRequest{Command: &cmd})
				if !settled || response.Err != nil || response.Repository != nil || response.Result == nil || response.Result.Status != "succeeded" {
					t.Fatalf("forgotten registration lost durable replay: %+v %v", response, settled)
				}
			}
			changed := cmd
			changed.ReplaceRepository = repo.ID
			if response, settled := terminalBackupMetadataResponse(reloaded, BackupRequest{Command: &changed}); !settled || !errors.Is(response.Err, ErrBackupConflict) {
				t.Fatalf("changed intent replay: %+v %v", response, settled)
			}
		})
	}
}

func TestRunRegistrationShutdownPersistsBlockedRollbackUncertainty(t *testing.T) {
	t.Run("durable-uncertainty", func(t *testing.T) {
		testRunRegistrationShutdownRollback(t, false)
	})
	t.Run("receipt-write-refused", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permission refusal")
		}
		testRunRegistrationShutdownRollback(t, true)
	})
}

func testRunRegistrationShutdownRollback(t *testing.T, refuseReceiptWrite bool) {
	s := seedBackupMaster(t)
	oldRef := readableRemoteCredential(t, s, protocol.BackupRepoKindSMB3)
	previous := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Share", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		SMBIdentity:   &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)},
		CredentialRef: oldRef, MountPath: t.TempDir()}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{previous.ID: previous}}); err != nil {
		t.Fatal(err)
	}
	credential, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
		Offer: protocol.StorageCredentialOffer{RequestID: "credential-new", RegistrationRequestID: "backup-credential:smb3:register-new",
			PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: previous.ID, ExpiresAt: time.Now().Add(time.Minute)},
		Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "new-example-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requested := previous
	requested.MountPath, requested.SMBIdentity, requested.CredentialRef = "", nil, credential.Receipt
	cmd := protocol.Command{OperationID: "register-new", Kind: "backup-repository-register", BackupRepository: &requested, ReplaceRepository: previous.ID}
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	priorPrepare, priorRemove := prepareBackupRepositorySMBNative, removeBackupSMBCredentialNative
	defer func() { prepareBackupRepositorySMBNative, removeBackupSMBCredentialNative = priorPrepare, priorRemove }()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	prepareBackupRepositorySMBNative = func(_ context.Context, repo protocol.BackupRepository, _ protocol.StorageCredentials) (protocol.BackupRepository, error) {
		if repo.CredentialRef == requested.CredentialRef {
			peer, err := loadPoolPeer(s)
			if err != nil {
				return protocol.BackupRepository{}, err
			}
			peer.Transfer = &poolTransferRecord{Frozen: true}
			if err := savePoolPeer(s, peer); err != nil {
				return protocol.BackupRepository{}, err
			}
			repo.MountPath, repo.SMBIdentity = previous.MountPath, previous.SMBIdentity
			return repo, nil
		}
		close(entered)
		<-release
		return protocol.BackupRepository{}, vmhost.ErrStorageRollbackUncertain
	}
	removed := false
	removeBackupSMBCredentialNative = func(string) error { removed = true; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	requests, done := make(chan BackupRequest), make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: time.Hour, ProbeInterval: time.Hour, ProbeTimeout: 20 * time.Millisecond, BackupRequests: requests})
	}()
	stopped := false
	defer func() {
		unblock()
		cancel()
		if !stopped {
			<-done
		}
	}()
	wait, stopWait := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopWait()
	select {
	case requests <- BackupRequest{Command: &cmd, Reply: make(chan BackupResponse, 1)}:
	case <-wait.Done():
		t.Fatal("registration was not admitted")
	}
	select {
	case <-entered:
	case <-wait.Done():
		t.Fatal("rollback did not start")
	}
	cancel()
	poolBackupAuthorityMu.Lock()
	mutation, reserveErr := reserveBackupRepositoryMutation(s, previous.ID)
	poolBackupAuthorityMu.Unlock()
	if reserveErr == nil {
		mutation.release()
		t.Fatal("shutdown released ownership before rollback stopped")
	}
	select {
	case <-done:
		stopped = true
		t.Fatal("Run stopped while rollback remained active")
	default:
	}
	pending, pendingErr := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if pendingErr != nil || pending.Status != "pending" {
		t.Fatalf("in-flight rollback exposed a terminal receipt: %+v %v", pending, pendingErr)
	}
	if refuseReceiptWrite {
		root := poolBackupReceiptsPath(s)
		t.Cleanup(func() { _ = os.Chmod(root, 0700) })
		if err := os.Chmod(root, 0500); err != nil {
			t.Fatal(err)
		}
	}
	unblock()
	select {
	case err := <-done:
		stopped = true
		var persistErr persistenceError
		if refuseReceiptWrite {
			if !errors.As(err, &persistErr) {
				t.Fatalf("receipt refusal was not reported as persistence failure: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	case <-wait.Done():
		t.Fatal("Run did not wait for rollback completion")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	receipt, err := loadPoolBackupMetadataReceipt(reloaded, cmd.OperationID)
	wantStatus := "needs_attention"
	if refuseReceiptWrite {
		wantStatus = "pending"
	}
	if err != nil || receipt.Status != wantStatus {
		t.Fatalf("shutdown lost uncertainty: %+v %v", receipt, err)
	}
	for _, reference := range []string{oldRef, credential.Receipt} {
		handled, err := RetireUnusedBackupCredential(reloaded, reference, s.State.PoolID, s.State.HostID)
		if !handled || err == nil {
			t.Fatalf("uncertain credential became retireable: %q %v %v", reference, handled, err)
		}
	}
	if removed {
		t.Fatal("uncertain rollback deleted plaintext credentials")
	}
	poolBackupAuthorityMu.Lock()
	mutation, reserveErr = reserveBackupRepositoryMutation(s, previous.ID)
	poolBackupAuthorityMu.Unlock()
	if reserveErr != nil {
		t.Fatalf("completed rollback leaked ownership: %v", reserveErr)
	}
	mutation.release()
}

func TestRunSMBCredentialRepairCommitRefusalRetainsReplacement(t *testing.T) {
	s := seedBackupMaster(t)
	previous := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Share", Kind: protocol.BackupRepoKindSMB3,
		SMBSource:     &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"},
		SMBIdentity:   &protocol.SMBStorageIdentity{Server: "smb.example.test", Share: "backups", MarkerID: strings.Repeat("a", 32)},
		CredentialRef: readableRemoteCredential(t, s, protocol.BackupRepoKindSMB3), MountPath: t.TempDir()}
	if err := savePoolBackupRepositories(s, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{previous.ID: previous}}); err != nil {
		t.Fatal(err)
	}
	oldPath, err := backupCredentialPath(s, "old")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	credential, err := saveBackupCredential(s, protocol.StorageCredentialHandoff{
		Offer: protocol.StorageCredentialOffer{RequestID: "credential-repair", RegistrationRequestID: "backup-credential:smb3:register-repair",
			PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: previous.ID, ExpiresAt: time.Now().Add(time.Minute)},
		Credentials: protocol.StorageCredentials{Username: "example-user", Secret: "repair-example-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requested := previous
	requested.MountPath, requested.SMBIdentity, requested.CredentialRef = "", nil, credential.Receipt
	cmd := protocol.Command{OperationID: "register-repair", Kind: "backup-repository-register", BackupRepository: &requested}
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	priorPrepare, priorRemove := prepareBackupRepositorySMBNative, removeBackupSMBCredentialNative
	defer func() { prepareBackupRepositorySMBNative, removeBackupSMBCredentialNative = priorPrepare, priorRemove }()
	prepared, removed := false, false
	prepareBackupRepositorySMBNative = func(_ context.Context, repo protocol.BackupRepository, _ protocol.StorageCredentials) (protocol.BackupRepository, error) {
		prepared = true
		peer, err := loadPoolPeer(s)
		if err != nil {
			return protocol.BackupRepository{}, err
		}
		peer.Transfer = &poolTransferRecord{Frozen: true}
		if err := savePoolPeer(s, peer); err != nil {
			return protocol.BackupRepository{}, err
		}
		repo.MountPath, repo.SMBIdentity = previous.MountPath, previous.SMBIdentity
		return repo, nil
	}
	removeBackupSMBCredentialNative = func(string) error { removed = true; return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	requests, done := make(chan BackupRequest), make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: time.Hour, ProbeInterval: time.Hour, ProbeTimeout: 20 * time.Millisecond, BackupRequests: requests})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	reply := make(chan BackupResponse, 1)
	select {
	case requests <- BackupRequest{Command: &cmd, Reply: reply}:
	case <-ctx.Done():
		t.Fatal("credential repair not admitted")
	}
	select {
	case response := <-reply:
		if response.Result == nil || response.Result.Status != "needs_attention" {
			t.Fatalf("unreadable prior credential lost rollback uncertainty: %+v", response)
		}
	case <-ctx.Done():
		t.Fatal("credential repair did not settle")
	}
	if !prepared || removed {
		t.Fatalf("repair native outcome: prepared=%v removed=%v", prepared, removed)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil || receipt.Status != "needs_attention" {
		t.Fatalf("repair receipt: %+v %v", receipt, err)
	}
	if handled, err := RetireUnusedBackupCredential(s, credential.Receipt, s.State.PoolID, s.State.HostID); !handled || err == nil {
		t.Fatalf("mounted replacement became retireable: %v %v", handled, err)
	}
}
