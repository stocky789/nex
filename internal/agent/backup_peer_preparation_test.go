// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestPeerBackupPreparationAcceptsRetainedRemoteIdentity(t *testing.T) {
	s := seedBackupMaster(t)
	for _, kind := range []string{protocol.BackupRepoKindSFTP, protocol.BackupRepoKindS3} {
		repo := protocol.BackupRepository{
			ID: "repo-" + kind, PoolID: s.State.PoolID, Name: "Primary", Kind: kind,
			CredentialRef: "credential-1", Generation: 1,
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
		cmd := protocol.Command{OperationID: "restore-1"}
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
