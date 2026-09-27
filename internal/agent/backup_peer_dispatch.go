// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

type pendingPeerBackup struct {
	run       protocol.BackupRun
	member    protocol.PoolMember
	command   protocol.Command
	repo      protocol.BackupRepository
	localRepo protocol.BackupRepository
	point     *protocol.BackupRecoveryPoint
	client    *http.Client
}

type peerBackupCompletion struct {
	run   protocol.BackupRun
	reply poolBackupPeerResponse
	err   error
}

type permanentPeerBackupError struct{ error }

func peerBackupWorkForCommand(s *Store, cmd protocol.Command, run protocol.BackupRun) (pendingPeerBackup, error) {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID || run.HostID == "" || run.HostID == s.State.HostID {
		return pendingPeerBackup{}, ErrBackupUnavailable
	}
	member, ok := poolMemberByID(membership, run.HostID)
	if !ok || member.Status != "ready" || member.PeerOrigin == "" {
		return pendingPeerBackup{}, ErrBackupUnavailable
	}
	localRepo, err := GetPoolBackupRepository(s, run.RepositoryID)
	if err != nil {
		return pendingPeerBackup{}, err
	}
	repo := portablePeerRepository(localRepo)
	var point *protocol.BackupRecoveryPoint
	if restoreBackupCommand(cmd.Kind) {
		if run.ManifestSHA256 == "" {
			return pendingPeerBackup{}, ErrBackupConflict
		}
		catalogPoint, pointErr := getCatalogPoint(s, run.RepositoryID, run.RecoveryPointID)
		if pointErr != nil || catalogPoint.PoolID != run.PoolID || catalogPoint.ManifestSHA256 != run.ManifestSHA256 {
			return pendingPeerBackup{}, ErrBackupConflict
		}
		point = &catalogPoint
	}
	client, err := poolTransferClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return pendingPeerBackup{}, err
	}
	return pendingPeerBackup{run: run, member: member, command: cmd, repo: repo, localRepo: localRepo, point: point, client: client}, nil
}

func portablePeerRepository(repo protocol.BackupRepository) protocol.BackupRepository {
	repo.MountPath = ""
	repo.Identity = nil
	repo.Access = protocol.StorageAccess{}
	repo.MemberAccess = nil
	return repo
}

func waitPeerBackupTerminal(ctx context.Context, work pendingPeerBackup) peerBackupCompletion {
	for {
		result := callPeerBackup(ctx, work)
		var permanent permanentPeerBackupError
		if errors.As(result.err, &permanent) {
			return result
		}
		if result.err == nil && (result.reply.Result.Status == "succeeded" || result.reply.Result.Status == "failed" || result.reply.Result.Status == "needs_attention") {
			return result
		}
		select {
		case <-ctx.Done():
			return peerBackupCompletion{run: work.run, err: ctx.Err()}
		case <-time.After(2 * time.Second):
		}
	}
}

func waitPeerBackupReconciled(ctx context.Context, work pendingPeerBackup) peerBackupCompletion {
	for {
		result := getPeerBackup(ctx, work)
		var permanent permanentPeerBackupError
		if errors.As(result.err, &permanent) || result.err == nil &&
			(result.reply.Result.Status == "succeeded" || result.reply.Result.Status == "failed") {
			return result
		}
		select {
		case <-ctx.Done():
			return peerBackupCompletion{run: work.run, err: ctx.Err()}
		case <-time.After(2 * time.Second):
		}
	}
}

func getPeerBackup(ctx context.Context, work pendingPeerBackup) peerBackupCompletion {
	result := peerBackupCompletion{run: work.run}
	workCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	url := strings.TrimRight(work.member.PeerOrigin, "/") + poolBackupPeerPath + "/" + work.run.OperationID
	request, err := http.NewRequestWithContext(workCtx, http.MethodGet, url, nil)
	if err != nil {
		result.err = err
		return result
	}
	result.reply, _, result.err = doPeerBackup(work.client, request)
	if result.err == nil {
		result.err = validatePeerBackupReply(workCtx, work, result.reply)
	}
	return result
}

// nextPendingPeerBackup selects one durable member occurrence per pass. A failed
// peer call leaves the reservation pending for the next pass or a restarted master.
func nextPendingPeerBackup(s *Store) (pendingPeerBackup, bool, error) {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return pendingPeerBackup{}, false, err
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return pendingPeerBackup{}, false, err
	}
	for _, run := range runs {
		if run.Status != "pending" ||
			(run.Kind != protocol.BackupKindScheduled && run.Kind != protocol.BackupKindPolicyRunNow) {
			continue
		}
		vm, err := lookupPoolVMSpec(s, run.VMID)
		if err != nil || vm.HostID == s.State.HostID {
			continue
		}
		member, ok := poolMemberByID(membership, vm.HostID)
		if !ok || member.Status != "ready" || member.PeerOrigin == "" {
			continue
		}
		repo, err := GetPoolBackupRepository(s, run.RepositoryID)
		if err != nil {
			return pendingPeerBackup{}, false, err
		}
		localRepo := repo
		repo = portablePeerRepository(repo)
		client, err := poolTransferClient(s, member.PeerOrigin, member.HostID)
		if err != nil {
			return pendingPeerBackup{}, false, err
		}
		return pendingPeerBackup{
			run: run, member: member, repo: repo, localRepo: localRepo, client: client,
			command: protocol.Command{OperationID: run.OperationID, Kind: "backup-now", VM: &vm,
				Backup: &protocol.BackupNowRequest{RepositoryID: run.RepositoryID, Consistency: run.CaptureMode}},
		}, true, nil
	}
	return pendingPeerBackup{}, false, nil
}

func callPeerBackup(ctx context.Context, work pendingPeerBackup) peerBackupCompletion {
	defer work.client.CloseIdleConnections()
	result := peerBackupCompletion{run: work.run}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	base := strings.TrimRight(work.member.PeerOrigin, "/") + poolBackupPeerPath
	get, err := http.NewRequestWithContext(requestCtx, http.MethodGet, base+"/"+work.run.OperationID, nil)
	if err != nil {
		result.err = err
		return result
	}
	reply, status, err := doPeerBackup(work.client, get)
	if err == nil {
		result.reply = reply
		result.err = validatePeerBackupReply(requestCtx, work, reply)
		return result
	}
	if status != http.StatusConflict && status != http.StatusNotFound {
		if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
			err = permanentPeerBackupError{err}
		}
		result.err = err
		return result
	}
	body, err := json.Marshal(poolBackupPeerRequest{Command: work.command, Repository: work.repo, CatalogPoint: work.point})
	if err != nil {
		result.err = err
		return result
	}
	var post *http.Request
	if restoreBackupCommand(work.command.Kind) {
		post, err = http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(work.member.PeerOrigin, "/")+poolBackupPeerPath+"/restore", bytes.NewReader(body))
	} else {
		post, err = http.NewRequestWithContext(requestCtx, http.MethodPost, base, bytes.NewReader(body))
	}
	if err != nil {
		result.err = err
		return result
	}
	post.Header.Set("Content-Type", "application/json")
	result.reply, status, result.err = doPeerBackup(work.client, post)
	if result.err != nil && (status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden ||
		status == http.StatusNotFound || status == http.StatusMethodNotAllowed) {
		result.err = permanentPeerBackupError{result.err}
	}
	if result.err == nil {
		result.err = validatePeerBackupReply(requestCtx, work, result.reply)
	}
	return result
}

func validatePeerBackupReply(ctx context.Context, work pendingPeerBackup, reply poolBackupPeerResponse) error {
	if !peerBackupReplyMatches(work, reply) {
		return permanentPeerBackupError{errors.New("member backup peer returned a different operation")}
	}
	if reply.Run.Status != reply.Result.Status {
		if reply.Result.Status == "pending" &&
			(reply.Run.Status == "succeeded" || reply.Run.Status == "failed" || reply.Run.Status == "needs_attention") ||
			reply.Result.Status == "needs_attention" && reply.Run.Status == "succeeded" {
			return errors.New("member backup result is settling")
		}
		return permanentPeerBackupError{errors.New("member backup peer returned inconsistent status")}
	}
	return verifyPeerBackupPoint(ctx, work, reply)
}

func peerBackupReplyMatches(work pendingPeerBackup, reply poolBackupPeerResponse) bool {
	run := reply.Run
	if run == nil || reply.Result.OperationID != work.run.OperationID ||
		reply.IntentSHA256 == "" || reply.IntentSHA256 != work.run.IntentSHA256 ||
		run.OperationID != work.run.OperationID || run.IntentSHA256 != work.run.IntentSHA256 ||
		run.PoolID != work.run.PoolID || run.HostID != work.member.HostID ||
		run.ManifestSHA256 != work.run.ManifestSHA256 ||
		run.RepositoryID != work.run.RepositoryID || run.RecoveryPointID != work.run.RecoveryPointID ||
		run.VMID != work.run.VMID || run.NewVMID != work.run.NewVMID {
		return false
	}
	if restoreBackupCommand(work.command.Kind) && (work.point == nil || reply.CatalogManifestSHA256 != work.point.ManifestSHA256) {
		return false
	}
	if work.run.Kind == protocol.BackupKindScheduled || work.run.Kind == protocol.BackupKindPolicyRunNow {
		return run.Kind == protocol.BackupKindBackupNow || run.Kind == work.run.Kind
	}
	return run.Kind == work.run.Kind
}

func verifyPeerBackupPoint(ctx context.Context, work pendingPeerBackup, reply poolBackupPeerResponse) error {
	if restoreBackupCommand(work.command.Kind) && reply.Result.Status == "succeeded" {
		if work.point == nil {
			return permanentPeerBackupError{errors.New("member restore has no master catalog point")}
		}
		_, digest, err := validatedBackupManifest(ctx, work.localRepo, work.run.RecoveryPointID)
		if err != nil || digest != work.point.ManifestSHA256 {
			return permanentPeerBackupError{errors.New("member restore point failed master catalog verification")}
		}
		return nil
	}
	if work.command.Kind != protocol.BackupKindBackupNow || reply.Result.Status != "succeeded" {
		return nil
	}
	point := reply.Point
	if point == nil || point.ID != work.run.OperationID || point.RepositoryID != work.run.RepositoryID ||
		point.PoolID != work.run.PoolID || point.SourceVMID != work.run.VMID {
		return permanentPeerBackupError{errors.New("member backup point does not match the reserved run")}
	}
	if err := verifyBackupRepository(ctx, work.localRepo); err != nil {
		return permanentPeerBackupError{errors.New("member backup point failed master repository verification")}
	}
	manifest, digest, err := vmhost.ReadBackupManifest(filepath.Join(work.localRepo.MountPath, point.ID))
	if err != nil || manifest.RecoveryPointID != point.ID || manifest.RepositoryID != point.RepositoryID ||
		manifest.PoolID != point.PoolID || digest != point.ManifestSHA256 || manifest.SourceVMID != point.SourceVMID ||
		vmhost.VerifyPublishedBackupRecoveryPoint(filepath.Join(work.localRepo.MountPath, point.ID), manifest) != nil {
		return permanentPeerBackupError{errors.New("member backup point failed master repository verification")}
	}
	return nil
}

func doPeerBackup(client *http.Client, request *http.Request) (poolBackupPeerResponse, int, error) {
	response, err := client.Do(request)
	if err != nil {
		return poolBackupPeerResponse{}, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return poolBackupPeerResponse{}, response.StatusCode, errors.New("member backup peer refused the request")
	}
	var reply poolBackupPeerResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&reply); err != nil {
		return poolBackupPeerResponse{}, response.StatusCode, errors.New("member backup peer returned an invalid result")
	}
	return reply, response.StatusCode, nil
}
