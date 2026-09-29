// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestPeerBackupPreparationAcceptsRetainedRemoteIdentity(t *testing.T) {
	s := seedBackupMaster(t)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s.State.Controller, s.State.TrustPEM = identity.Controller, identity.TrustPEM
	s.State.CertificatePEM, s.State.KeyPEM = identity.CertificatePEM, identity.KeyPEM
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, s, RunConfig{Heartbeat: time.Hour, ProbeInterval: time.Hour, ProbeTimeout: 20 * time.Millisecond})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Core Run stopped with error: %v", err)
		}
	}()
	for _, kind := range []string{protocol.BackupRepoKindSFTP, protocol.BackupRepoKindS3} {
		repo := protocol.BackupRepository{
			ID: "repo-" + kind, PoolID: s.State.PoolID, Name: "Primary", Kind: kind,
			CredentialRef:  "credential-1",
			RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)},
		}
		if kind == protocol.BackupRepoKindSFTP {
			repo.SFTPSource = &protocol.BackupSFTPSource{Host: "sftp.example.test", Root: "/backups", HostKeySHA256: "SHA256:" + strings.Repeat("A", 43)}
		} else {
			repo.S3Source = &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups"}
		}
		if err := repo.Valid(); err != nil {
			t.Fatal(err)
		}
		cmd := protocol.Command{OperationID: "restore-1", Kind: protocol.BackupKindRestoreNew}
		// The Run loop's peer path must reach adoption, not registration.
		response := runLoopPeerRequest(t, ctx, s, done, BackupRequest{Command: &cmd, PeerRepository: &repo})
		if response.Err == nil || errors.Is(response.Err, ErrBackupInvalid) || !strings.Contains(response.Err.Error(), "credential receipt is unavailable") {
			t.Fatalf("%s peer request through Run did not reach retained credential lookup: %+v", kind, response)
		}
		_, err := prepareBackupRequestRepository(context.Background(), s, BackupRequest{Command: &cmd, PeerRepository: &repo}, repo)
		if err == nil || errors.Is(err, ErrBackupInvalid) || !strings.Contains(err.Error(), "credential receipt is unavailable") {
			t.Fatalf("%s peer request did not reach retained credential lookup: %v", kind, err)
		}
		_, err = prepareBackupRequestRepository(context.Background(), s, BackupRequest{Command: &cmd}, repo)
		if !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("%s registration accepted a retained remote identity: %v", kind, err)
		}
	}
}

// runLoopPeerRequest sends a peer backup request through a running Core Run
// loop, retrying until Run has registered its peer queue.
func runLoopPeerRequest(t *testing.T, ctx context.Context, s *Store, done <-chan error, request BackupRequest) BackupResponse {
	t.Helper()
	for {
		response, err := peerBackupRequest(ctx, s, request)
		if err == nil {
			return response
		}
		if !errors.Is(err, ErrBackupUnavailable) {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			t.Fatalf("Core Run stopped: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
