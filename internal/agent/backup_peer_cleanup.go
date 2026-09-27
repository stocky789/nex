// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

type peerCleanupWork struct {
	command  protocol.Command
	ownerRun protocol.BackupRun
	member   protocol.PoolMember
	repo     protocol.BackupRepository
	intent   string
	client   *http.Client
}

func memberCleanupWork(s *Store, cmd protocol.Command) (peerCleanupWork, bool, error) {
	if cmd.Kind != "backup-test-cleanup" || cmd.TestRestoreClean == nil {
		return peerCleanupWork{}, false, ErrBackupInvalid
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return peerCleanupWork{}, false, err
	}
	var target protocol.BackupRun
	for _, run := range runs {
		if run.Kind == protocol.BackupKindTestRestore && run.Status == "succeeded" &&
			run.NewVMID == cmd.TestRestoreClean.TestVMID && run.RepositoryID == cmd.TestRestoreClean.RepositoryID &&
			run.RecoveryPointID == cmd.TestRestoreClean.RecoveryPointID {
			if target.OperationID != "" {
				return peerCleanupWork{}, false, ErrBackupConflict
			}
			target = run
		}
	}
	if target.OperationID == "" {
		return peerCleanupWork{}, false, ErrBackupNotFound
	}
	if target.HostID == s.State.HostID {
		return peerCleanupWork{}, false, nil
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return peerCleanupWork{}, false, ErrBackupUnavailable
	}
	member, ok := poolMemberByID(membership, target.HostID)
	if !ok || member.Status != "ready" || member.PeerOrigin == "" {
		return peerCleanupWork{}, false, ErrBackupUnavailable
	}
	repo, err := GetPoolBackupRepository(s, target.RepositoryID)
	if err != nil {
		return peerCleanupWork{}, false, err
	}
	client, err := poolTransferClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return peerCleanupWork{}, false, err
	}
	intent, err := backupMetadataIntent(cmd)
	if err != nil {
		return peerCleanupWork{}, false, err
	}
	return peerCleanupWork{command: cmd, ownerRun: target, member: member, repo: portablePeerRepository(repo), intent: intent, client: client}, true, nil
}

func waitPeerCleanup(ctx context.Context, work peerCleanupWork) (protocol.Result, error) {
	return waitPeerCleanupMode(ctx, work, true)
}

func waitPeerCleanupReadOnly(ctx context.Context, work peerCleanupWork) (protocol.Result, error) {
	return waitPeerCleanupMode(ctx, work, false)
}

func waitPeerCleanupMode(ctx context.Context, work peerCleanupWork, allowPost bool) (protocol.Result, error) {
	defer work.client.CloseIdleConnections()
	base := strings.TrimRight(work.member.PeerOrigin, "/") + poolBackupPeerPath
	var nextPost time.Time
	for {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		request, err := http.NewRequestWithContext(callCtx, http.MethodGet, base+"/"+work.command.OperationID, nil)
		if err != nil {
			cancel()
			return protocol.Result{}, err
		}
		reply, status, err := doPeerBackup(work.client, request)
		cancel()
		pending := false
		if err == nil {
			if reply.Result.OperationID != work.command.OperationID || reply.IntentSHA256 != work.intent {
				return protocol.Result{}, permanentPeerBackupError{errors.New("member cleanup receipt does not match the requested operation")}
			}
			if reply.Result.Status == "succeeded" || reply.Result.Status == "failed" ||
				allowPost && reply.Result.Status == "needs_attention" {
				return reply.Result, nil
			}
			pending = reply.Result.Status == "pending"
		}
		if allowPost && !time.Now().Before(nextPost) && (pending || status == http.StatusConflict || status == http.StatusNotFound) {
			nextPost = time.Now().Add(30 * time.Second)
			body, marshalErr := json.Marshal(poolBackupPeerRequest{Command: work.command, Repository: work.repo})
			if marshalErr != nil {
				return protocol.Result{}, marshalErr
			}
			postCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			post, makeErr := http.NewRequestWithContext(postCtx, http.MethodPost, base, bytes.NewReader(body))
			if makeErr != nil {
				stop()
				return protocol.Result{}, makeErr
			}
			post.Header.Set("Content-Type", "application/json")
			reply, status, err = doPeerBackup(work.client, post)
			stop()
			if err == nil {
				if reply.Result.OperationID != work.command.OperationID || reply.IntentSHA256 != work.intent {
					return protocol.Result{}, permanentPeerBackupError{errors.New("member cleanup receipt does not match the requested operation")}
				}
				if reply.Result.Status == "succeeded" || reply.Result.Status == "failed" || reply.Result.Status == "needs_attention" {
					return reply.Result, nil
				}
			} else if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
				return protocol.Result{}, permanentPeerBackupError{errors.New("member refused cleanup admission")}
			}
		} else if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
			return protocol.Result{}, permanentPeerBackupError{errors.New("member refused cleanup observation")}
		}
		select {
		case <-ctx.Done():
			return protocol.Result{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
