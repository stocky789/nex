// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolBackupPeerPath = "/api/v1/pool/backup"

type poolBackupPeerRequest struct {
	Command      protocol.Command              `json:"command"`
	Repository   protocol.BackupRepository     `json:"repository"`
	CatalogPoint *protocol.BackupRecoveryPoint `json:"catalog_point,omitempty"`
}

type poolBackupPeerResponse struct {
	Result                protocol.Result               `json:"result"`
	Point                 *protocol.BackupRecoveryPoint `json:"point,omitempty"`
	Run                   *protocol.BackupRun           `json:"run,omitempty"`
	IntentSHA256          string                        `json:"intent_sha256"`
	CatalogManifestSHA256 string                        `json:"catalog_manifest_sha256,omitempty"`
}

// PoolBackupPeerHandler accepts work and reports owner evidence to the pool master.
func PoolBackupPeerHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := AuthenticatePoolMaster(s, r); err != nil {
			http.Error(w, "pool master rejected", http.StatusUnauthorized)
			return
		}
		var request BackupRequest
		switch {
		case r.Method == http.MethodPost && r.URL.Path == poolBackupPeerPath+"/verify-test":
			var plan testRestoreVerificationPlan
			if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&plan) != nil ||
				plan.Request.Valid() != nil || plan.Run.Kind != protocol.BackupKindTestRestore || plan.Run.Status != "succeeded" ||
				plan.Run.HostID != s.State.HostID || plan.Run.PoolID != s.State.PoolID ||
				plan.Run.RepositoryID != plan.Request.RepositoryID || plan.Run.RecoveryPointID != plan.Request.RecoveryPointID ||
				plan.Run.NewVMID != plan.Request.TestVMID || plan.VM.ID != plan.Run.NewVMID ||
				plan.VM.HostID != s.State.HostID || plan.VM.PoolID != s.State.PoolID {
				http.Error(w, "invalid test verification", http.StatusBadRequest)
				return
			}
			stored, err := loadPoolBackupRun(s, plan.Run.OperationID)
			if err != nil || !sameTestRestoreRun(stored, plan.Run) ||
				verifyTestRestoredVM(vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")), r.Context(), stored.OperationID, plan.VM,
					plan.Request.Result == protocol.BackupTestResultPassed) != nil {
				http.Error(w, "test restore cannot be verified", http.StatusConflict)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(poolBackupPeerResponse{
				Result: protocol.Result{OperationID: stored.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()},
				Run:    &stored, IntentSHA256: stored.IntentSHA256,
			})
			return
		case r.Method == http.MethodPost && (r.URL.Path == poolBackupPeerPath || r.URL.Path == poolBackupPeerPath+"/restore"):
			var body poolBackupPeerRequest
			if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body) != nil ||
				(!backupCommand(body.Command.Kind) && body.Command.Kind != "backup-test-cleanup") ||
				body.Repository.ID != backupCommandRepositoryID(body.Command) || body.Repository.PoolID != s.State.PoolID ||
				(restoreBackupCommand(body.Command.Kind) != (r.URL.Path == poolBackupPeerPath+"/restore")) ||
				(restoreBackupCommand(body.Command.Kind) && (body.CatalogPoint == nil ||
					body.CatalogPoint.ID != backupCommandRecoveryPointID(body.Command) ||
					body.CatalogPoint.RepositoryID != body.Repository.ID || body.CatalogPoint.PoolID != s.State.PoolID)) {
				http.Error(w, "invalid backup request", http.StatusBadRequest)
				return
			}
			request.Command, request.PeerRepository, request.PeerCatalogPoint = &body.Command, &body.Repository, body.CatalogPoint
		case r.Method == http.MethodGet && len(r.URL.Path) > len(poolBackupPeerPath)+1 &&
			strings.HasPrefix(r.URL.Path, poolBackupPeerPath+"/"):
			request.OperationID = strings.TrimPrefix(r.URL.Path, poolBackupPeerPath+"/")
			request.PeerStatus = true
			if !protocol.ValidSnapshotOperationID(request.OperationID) {
				http.Error(w, "invalid operation", http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		request.Reply = make(chan BackupResponse, 1)
		response, err := peerBackupRequest(r.Context(), s, request)
		if err != nil || response.Err != nil {
			if errors.Is(response.Err, ErrBackupConflict) || errors.Is(response.Err, ErrBackupInvalid) {
				http.Error(w, "backup request conflicts with retained work", http.StatusBadRequest)
				return
			}
			http.Error(w, "backup request unavailable", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		peerResponse := poolBackupPeerResponse{Result: *response.Result, Point: response.Point}
		if stored, err := loadPoolBackupRun(s, response.Result.OperationID); err == nil {
			peerResponse.Run, peerResponse.IntentSHA256 = &stored, stored.IntentSHA256
			if restoreBackupCommand(stored.Kind) {
				peerResponse.CatalogManifestSHA256 = stored.ManifestSHA256
			}
		} else if receipt, err := loadPoolBackupMetadataReceipt(s, response.Result.OperationID); err == nil {
			peerResponse.IntentSHA256 = receipt.IntentSHA256
		}
		if peerResponse.IntentSHA256 == "" {
			http.Error(w, "backup operation evidence unavailable", http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(peerResponse)
	})
}

// BackupRequest enters the same Store owner as Controller commands.
type BackupRequest struct {
	Command              *protocol.Command
	PeerRepository       *protocol.BackupRepository
	PeerCatalogPoint     *protocol.BackupRecoveryPoint
	PreparedRepository   *protocol.BackupRepository
	PreparedCleanup      *testRestoreCleanupPlan
	PreparedVerification *testRestoreVerificationPlan
	ReserveOnly          bool
	PeerStatus           bool
	OperationID          string
	List                 string // repositories | catalog | runs | policies
	PolicyID             string
	Pause                *bool
	RunNowVMID           string
	Reply                chan BackupResponse
}

type BackupResponse struct {
	Result       *protocol.Result
	Point        *protocol.BackupRecoveryPoint
	Repositories []protocol.BackupRepository
	Catalog      []protocol.BackupRecoveryPoint
	Runs         []protocol.BackupRun
	Policies     []protocol.BackupPolicy
	Repository   *protocol.BackupRepository
	Policy       *protocol.BackupPolicy
	Err          error
}

var reserveBackupRun = ReservePoolBackupRun
var requestBackupRestorePlacement = poolVMDefineRequestPeer
var registerBackupRestoredAsNew = func(ctx context.Context, engine *vmhost.ManagedEngine, operationID string, spec protocol.VMCreateSpec, localDir, manifestSHA string, artifacts []protocol.BackupDiskArtifact, firmware ...protocol.BackupFirmwareArtifact) error {
	return engine.RegisterRestoredAsNew(ctx, operationID, spec, localDir, manifestSHA, artifacts, firmware...)
}

var restoreAsNewAllocating = (*vmhost.ManagedEngine).RestoreAsNewAllocating
var bindLegacyRestoreAsNewAllocating = (*vmhost.ManagedEngine).BindLegacyRestoreAsNewAllocating
var reconcileRestoredAsNew = (*vmhost.ManagedEngine).ReconcileRestoredAsNew

func backupCommand(kind string) bool {
	return kind == "backup-now" || kind == "backup-restore-as-new" ||
		kind == protocol.BackupKindRestoreReplace || kind == protocol.BackupKindTestRestore
}

func backupMetadataCommand(kind string) bool {
	switch kind {
	case "backup-repository-register", "backup-repository-test", "backup-policy-upsert", "backup-policy-pause",
		"backup-policy-run-now", "backup-protect", "backup-catalog-import", "backup-catalog-rebuild",
		"backup-test-verify", "backup-test-cleanup":
		return true
	}
	return false
}

func backupMetadataIntent(cmd protocol.Command) (string, error) {
	raw, err := json.Marshal(cmd)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func backupCommandRepositoryID(cmd protocol.Command) string {
	switch cmd.Kind {
	case protocol.BackupKindBackupNow:
		if cmd.Backup != nil {
			return cmd.Backup.RepositoryID
		}
	case protocol.BackupKindRestoreNew:
		if cmd.Restore != nil {
			return cmd.Restore.RepositoryID
		}
	case protocol.BackupKindRestoreReplace:
		if cmd.RestoreReplace != nil {
			return cmd.RestoreReplace.RepositoryID
		}
	case protocol.BackupKindTestRestore:
		if cmd.TestRestore != nil {
			return cmd.TestRestore.RepositoryID
		}
	case "backup-test-cleanup":
		if cmd.TestRestoreClean != nil {
			return cmd.TestRestoreClean.RepositoryID
		}
	}
	return ""
}

func restoreBackupCommand(kind string) bool {
	return kind == protocol.BackupKindRestoreNew || kind == protocol.BackupKindRestoreReplace || kind == protocol.BackupKindTestRestore
}

func backupCommandRecoveryPointID(cmd protocol.Command) string {
	switch cmd.Kind {
	case protocol.BackupKindRestoreNew:
		if cmd.Restore != nil {
			return cmd.Restore.RecoveryPointID
		}
	case protocol.BackupKindRestoreReplace:
		if cmd.RestoreReplace != nil {
			return cmd.RestoreReplace.RecoveryPointID
		}
	case protocol.BackupKindTestRestore:
		if cmd.TestRestore != nil {
			return cmd.TestRestore.RecoveryPointID
		}
	}
	return ""
}

func backupCommandTargetHost(cmd protocol.Command) string {
	switch cmd.Kind {
	case protocol.BackupKindBackupNow:
		if cmd.VM != nil {
			return cmd.VM.HostID
		}
	case protocol.BackupKindRestoreNew:
		if cmd.Restore != nil {
			return cmd.Restore.HostID
		}
	case protocol.BackupKindRestoreReplace:
		if cmd.RestoreReplace != nil {
			return cmd.RestoreReplace.HostID
		}
	case protocol.BackupKindTestRestore:
		if cmd.TestRestore != nil {
			return cmd.TestRestore.HostID
		}
	}
	return ""
}

func readyBackupTarget(s *Store, hostID string) error {
	if hostID == "" {
		return ErrBackupInvalid
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != s.State.PoolID {
		return ErrBackupUnavailable
	}
	for _, member := range membership.Members {
		if member.HostID == hostID && member.Status == "ready" {
			return nil
		}
	}
	return ErrBackupUnavailable
}

func backupResult(id string, entry JournalEntry) *protocol.Result {
	return &protocol.Result{OperationID: id, Status: entry.Status, Error: entry.Error, ObservedAt: entry.ObservedAt}
}

func hasUnsettledBackup(journal map[string]JournalEntry, vmID string) bool {
	for _, entry := range journal {
		if entry.Command == nil || !backupCommand(entry.Command.Kind) {
			continue
		}
		if entry.Status != "pending" && entry.Status != "needs_attention" {
			continue
		}
		if vmID == "" {
			return true
		}
		if entry.Command.VM != nil && entry.Command.VM.ID == vmID {
			return true
		}
		if entry.Command.Restore != nil && entry.Command.Restore.NewVMID == vmID {
			return true
		}
		if entry.Command.RestoreReplace != nil && entry.Command.RestoreReplace.OriginalVMID == vmID {
			return true
		}
		if entry.Command.TestRestore != nil && entry.Command.TestRestore.NewVMID == vmID {
			return true
		}
	}
	return false
}

func restoreTargetBusy(s *Store, vmID, operationID string) (bool, error) {
	if hasUnsettledBackup(s.State.Journal, vmID) {
		return true, nil
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if run.OperationID != operationID && (run.Status == "pending" || run.Status == "needs_attention") &&
			(run.VMID == vmID || run.NewVMID == vmID) {
			return true, nil
		}
	}
	return false, nil
}

func reserveRestoreRunWithPin(s *Store, run protocol.BackupRun, kind string) error {
	if existing, err := loadPoolBackupRun(s, run.OperationID); err == nil {
		if existing.Kind != run.Kind || existing.IntentSHA256 != run.IntentSHA256 ||
			existing.RepositoryID != run.RepositoryID || existing.RecoveryPointID != run.RecoveryPointID ||
			existing.VMID != run.VMID || existing.NewVMID != run.NewVMID || existing.HostID != run.HostID ||
			existing.ManifestSHA256 != run.ManifestSHA256 {
			return ErrBackupConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	pins, err := loadPoolBackupPins(s)
	if err != nil {
		return err
	}
	_, priorPin := pins.Pins[run.OperationID]
	if err := putBackupRestorePin(s, protocol.BackupRestorePin{
		RecoveryPointID: run.RecoveryPointID, OperationID: run.OperationID,
		Kind: kind, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	if _, err := reserveBackupRun(s, run); err != nil {
		if !priorPin {
			if persisted, readErr := loadPoolBackupRun(s, run.OperationID); errors.Is(readErr, os.ErrNotExist) ||
				readErr == nil && persisted.IntentSHA256 != run.IntentSHA256 {
				_ = clearBackupRestorePin(s, run.RecoveryPointID, run.OperationID)
			}
		}
		return err
	}
	return nil
}

// acceptBackupRequest runs only on Run's Store-owning goroutine.
func acceptBackupRequest(ctx context.Context, s *Store, request BackupRequest) BackupResponse {
	if request.Command == nil || !backupMetadataCommand(request.Command.Kind) {
		return acceptBackupRequestUnrecorded(ctx, s, request)
	}
	cmd := *request.Command
	if !protocol.ValidSnapshotOperationID(cmd.OperationID) || request.OperationID != "" {
		return BackupResponse{Err: ErrBackupInvalid}
	}
	if cmd.Kind == "backup-test-cleanup" && (cmd.TestRestoreClean == nil || cmd.TestRestoreClean.Valid() != nil) {
		return BackupResponse{Err: ErrBackupInvalid}
	}
	intent, err := backupMetadataIntent(cmd)
	if err != nil {
		return BackupResponse{Err: err}
	}
	poolBackupAuthorityMu.Lock()
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err == nil {
		poolBackupAuthorityMu.Unlock()
		if receipt.Kind != cmd.Kind || receipt.IntentSHA256 != intent {
			return BackupResponse{Err: ErrBackupConflict}
		}
		if receipt.Status == "pending" && (request.PreparedCleanup != nil || request.PreparedVerification != nil) && !request.ReserveOnly {
			return finishBackupMetadataRequest(ctx, s, request, receipt)
		}
		if receipt.Status == "pending" && cmd.Kind == "backup-test-cleanup" && request.PeerRepository == nil && !request.ReserveOnly {
			if _, queued := s.State.Journal[cmd.OperationID]; !queued {
				if run, runErr := matchingTestRestoreRun(s, *cmd.TestRestoreClean); runErr == nil && run.HostID != "" && run.HostID != s.State.HostID {
					return finishBackupMetadataRequest(ctx, s, request, receipt)
				}
			}
		}
		status := receipt.Status
		if status == "pending" && !request.ReserveOnly {
			status = "needs_attention"
		}
		return BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID, Status: status, Error: receipt.Error, ObservedAt: receipt.ObservedAt}}
	}
	if !errors.Is(err, os.ErrNotExist) {
		poolBackupAuthorityMu.Unlock()
		return BackupResponse{Err: err}
	}
	if entry, ok := s.State.Journal[cmd.OperationID]; ok {
		if entry.Command == nil || !reflect.DeepEqual(*entry.Command, cmd) || entry.Status != "pending" {
			poolBackupAuthorityMu.Unlock()
			return BackupResponse{Err: ErrBackupConflict}
		}
	}
	if _, err := loadPoolBackupRun(s, cmd.OperationID); err == nil {
		poolBackupAuthorityMu.Unlock()
		return BackupResponse{Err: ErrBackupConflict}
	} else if !errors.Is(err, os.ErrNotExist) {
		poolBackupAuthorityMu.Unlock()
		return BackupResponse{Err: err}
	}
	memberCleanup := cmd.Kind == "backup-test-cleanup" && request.PeerRepository != nil
	if !memberCleanup {
		if err := masterBackupPool(s, s.State.PoolID); err != nil {
			poolBackupAuthorityMu.Unlock()
			return BackupResponse{Err: err}
		}
	} else if membership, err := Membership(s); err != nil || membership.MasterHostID == s.State.HostID {
		poolBackupAuthorityMu.Unlock()
		return BackupResponse{Err: ErrBackupUnavailable}
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		poolBackupAuthorityMu.Unlock()
		return BackupResponse{Err: err}
	}
	receipt = protocol.BackupMetadataReceipt{OperationID: cmd.OperationID, Kind: cmd.Kind,
		IntentSHA256: intent, Status: "pending", ObservedAt: time.Now().UTC()}
	err = savePoolBackupMetadataReceipt(s, receipt)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		return BackupResponse{Err: err}
	}
	if request.ReserveOnly {
		return BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID, Status: "pending", ObservedAt: receipt.ObservedAt}}
	}
	return finishBackupMetadataRequest(ctx, s, request, receipt)
}

func finishBackupMetadataRequest(ctx context.Context, s *Store, request BackupRequest, receipt protocol.BackupMetadataReceipt) BackupResponse {
	response := acceptBackupRequestUnrecorded(ctx, s, request)
	if response.Result == nil {
		if errors.Is(response.Err, ErrBackupInvalid) || errors.Is(response.Err, ErrBackupNotFound) ||
			request.Command.Kind == "backup-test-verify" && errors.Is(response.Err, ErrBackupConflict) {
			poolBackupAuthorityMu.Lock()
			if writeErr := requirePoolAuthorityWritable(s); writeErr == nil {
				receipt.Status, receipt.Error, receipt.ObservedAt = "failed", response.Err.Error(), time.Now().UTC()
				writeErr = savePoolBackupMetadataReceipt(s, receipt)
			}
			poolBackupAuthorityMu.Unlock()
		}
		return response
	}
	poolBackupAuthorityMu.Lock()
	err := requirePoolAuthorityWritable(s)
	if err == nil {
		receipt.Status, receipt.Error, receipt.ObservedAt = response.Result.Status, response.Result.Error, response.Result.ObservedAt
		err = savePoolBackupMetadataReceipt(s, receipt)
	}
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		return BackupResponse{Err: err}
	}
	return response
}

func reserveBackupMetadataReceipt(s *Store, cmd protocol.Command, peer bool) error {
	request := BackupRequest{Command: &cmd, ReserveOnly: true}
	if peer {
		request.PeerRepository = &protocol.BackupRepository{}
	}
	response := acceptBackupRequest(context.Background(), s, request)
	if response.Err != nil {
		return response.Err
	}
	if response.Result == nil || response.Result.Status != "pending" {
		return ErrBackupConflict
	}
	return nil
}

func acceptBackupRequestUnrecorded(ctx context.Context, s *Store, request BackupRequest) BackupResponse {
	if request.Command == nil {
		switch request.List {
		case "repositories":
			repos, err := ListPoolBackupRepositories(s)
			if err != nil {
				return BackupResponse{Err: err}
			}
			return BackupResponse{Repositories: repos}
		case "catalog":
			points, err := loadPoolBackupCatalog(s)
			if err != nil {
				return BackupResponse{Err: ErrBackupUnavailable}
			}
			return BackupResponse{Catalog: points}
		case "runs":
			runs, err := listPoolBackupRuns(s)
			if err != nil {
				return BackupResponse{Err: ErrBackupUnavailable}
			}
			return BackupResponse{Runs: runs}
		case "policies":
			policies, err := ListPoolBackupPolicies(s)
			if err != nil {
				return BackupResponse{Err: err}
			}
			return BackupResponse{Policies: policies}
		case "":
			if request.OperationID == "" {
				return BackupResponse{Err: ErrBackupInvalid}
			}
			entry, ok := s.State.Journal[request.OperationID]
			if !ok || entry.Command == nil || !backupCommand(entry.Command.Kind) {
				receipt, err := loadPoolBackupMetadataReceipt(s, request.OperationID)
				if err != nil {
					return BackupResponse{Err: ErrBackupNotFound}
				}
				status := receipt.Status
				if status == "pending" && !request.PeerStatus {
					status = "needs_attention"
				}
				return BackupResponse{Result: &protocol.Result{OperationID: receipt.OperationID, Status: status, Error: receipt.Error, ObservedAt: receipt.ObservedAt}}
			}
			response := BackupResponse{Result: backupResult(request.OperationID, entry)}
			if entry.Status == "succeeded" && entry.Command.Kind == protocol.BackupKindBackupNow {
				if point, err := getCatalogPoint(s, entry.Command.Backup.RepositoryID, request.OperationID); err == nil {
					response.Point = &point
				}
			}
			return response
		default:
			return BackupResponse{Err: ErrBackupInvalid}
		}
	}
	cmd := request.Command
	if request.OperationID != "" || !protocol.ValidSnapshotOperationID(cmd.OperationID) {
		return BackupResponse{Err: ErrBackupInvalid}
	}
	if request.PeerRepository != nil {
		repo := *request.PeerRepository
		membership, err := Membership(s)
		if err != nil || membership.MasterHostID == s.State.HostID ||
			repo.Valid() != nil || repo.PoolID != s.State.PoolID ||
			backupCommandRepositoryID(*cmd) != repo.ID {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		if cmd.Kind != "backup-test-cleanup" {
			if restoreBackupCommand(cmd.Kind) {
				point := request.PeerCatalogPoint
				if point == nil || point.Valid() != nil || point.ID != backupCommandRecoveryPointID(*cmd) ||
					point.RepositoryID != repo.ID || point.PoolID != repo.PoolID {
					return BackupResponse{Err: ErrBackupInvalid}
				}
			}
			if !backupCommand(cmd.Kind) || backupCommandTargetHost(*cmd) != s.State.HostID ||
				(cmd.VM != nil && cmd.VM.PoolID != s.State.PoolID) {
				return BackupResponse{Err: ErrBackupInvalid}
			}
			prepared := protocol.BackupRepository{}
			if request.PreparedRepository != nil {
				prepared = *request.PreparedRepository
			} else {
				prepared, err = prepareBackupRepository(ctx, repo)
				if err != nil {
					return BackupResponse{Err: err}
				}
			}
			if prepared.ID != repo.ID || prepared.PoolID != repo.PoolID ||
				!reflect.DeepEqual(prepared.Source, repo.Source) || prepared.MountPath == "" || prepared.Identity == nil {
				return BackupResponse{Err: ErrBackupInvalid}
			}
			state, err := loadPoolBackupRepositories(s)
			if err != nil {
				return BackupResponse{Err: err}
			}
			state.Repositories[repo.ID] = prepared
			if err := savePoolBackupRepositories(s, state); err != nil {
				return BackupResponse{Err: err}
			}
			if restoreBackupCommand(cmd.Kind) {
				if pointErr := savePeerRestoreCatalogPoint(s, *request.PeerCatalogPoint); pointErr != nil {
					return BackupResponse{Err: pointErr}
				}
			}
		}
	} else if backupCommand(cmd.Kind) {
		membership, err := Membership(s)
		if err != nil || membership.MasterHostID != s.State.HostID {
			return BackupResponse{Err: ErrBackupUnavailable}
		}
	}
	switch cmd.Kind {
	case "backup-repository-register":
		if cmd.BackupRepository == nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		repo := *cmd.BackupRepository
		if repo.PoolID == "" {
			repo.PoolID = s.State.PoolID
		}
		if repo.Valid() != nil || repo.PoolID == "" {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		prepared := request.PreparedRepository
		if prepared == nil {
			result, err := prepareBackupRepository(ctx, repo)
			if err != nil {
				return BackupResponse{Err: err}
			}
			prepared = &result
		}
		saved, err := upsertPoolBackupRepositoryPrepared(s, repo, *prepared)
		if err != nil {
			return BackupResponse{Err: err}
		}
		now := time.Now().UTC()
		return BackupResponse{
			Result:     &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: now},
			Repository: &saved,
		}
	case "backup-repository-test":
		if cmd.BackupRepository == nil || !protocol.ValidSnapshotOperationID(cmd.BackupRepository.ID) {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		repo := protocol.BackupRepository{}
		var err error
		if request.PreparedRepository != nil {
			repo = *request.PreparedRepository
			err = savePoolBackupRepositoryAccess(s, repo)
		} else {
			repo, err = TestPoolBackupRepositoryAccess(s, cmd.BackupRepository.ID)
		}
		if err != nil {
			return BackupResponse{Err: err}
		}
		now := time.Now().UTC()
		status, errText := "succeeded", ""
		if repo.Access.Status != "ok" {
			status, errText = "failed", repo.Access.Error
		}
		return BackupResponse{
			Result:     &protocol.Result{OperationID: cmd.OperationID, Status: status, Error: errText, ObservedAt: now},
			Repository: &repo,
		}
	case "backup-policy-upsert":
		if cmd.BackupPolicy == nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		pol := *cmd.BackupPolicy
		if pol.PoolID == "" {
			pol.PoolID = s.State.PoolID
		}
		if pol.Valid() != nil || pol.PoolID == "" {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		saved, err := UpsertPoolBackupPolicy(s, pol)
		if err != nil {
			return BackupResponse{Err: err}
		}
		return BackupResponse{
			Result: &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()},
			Policy: &saved,
		}
	case "backup-policy-pause":
		if cmd.BackupPolicy == nil || !protocol.ValidSnapshotOperationID(cmd.BackupPolicy.ID) {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		saved, err := SetPoolBackupPolicyPaused(s, cmd.BackupPolicy.ID, cmd.BackupPolicy.Paused)
		if err != nil {
			return BackupResponse{Err: err}
		}
		return BackupResponse{
			Result: &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()},
			Policy: &saved,
		}
	case "backup-policy-run-now":
		if cmd.BackupPolicy == nil || cmd.VM == nil || !protocol.ValidSnapshotOperationID(cmd.BackupPolicy.ID) {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		run, err := ReservePolicyRunNow(s, cmd.BackupPolicy.ID, cmd.VM.ID, cmd.OperationID)
		if err != nil {
			return BackupResponse{Err: err}
		}
		pol := *cmd.BackupPolicy
		if entry, ok := s.State.Journal[cmd.OperationID]; ok {
			return BackupResponse{Result: backupResult(cmd.OperationID, entry), Policy: &pol}
		}
		return BackupResponse{
			Result: &protocol.Result{OperationID: run.OperationID, Status: run.Status, ObservedAt: time.Now().UTC()},
			Policy: &pol,
		}
	case "backup-protect":
		if cmd.BackupProtect == nil || cmd.BackupProtect.Valid() != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		point, err := SetPoolBackupPointProtected(s, *cmd.BackupProtect)
		if err != nil {
			return BackupResponse{Err: err}
		}
		return BackupResponse{
			Result:  &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()},
			Catalog: []protocol.BackupRecoveryPoint{point},
		}
	case "backup-catalog-import", "backup-catalog-rebuild":
		if cmd.CatalogImport == nil || cmd.CatalogImport.Valid() != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		var (
			imported []protocol.BackupRecoveryPoint
			err      error
		)
		if cmd.Kind == "backup-catalog-rebuild" {
			imported, err = RebuildPoolBackupCatalogFromRepository(s, cmd.CatalogImport.RepositoryID)
		} else {
			imported, err = ImportPoolBackupCatalog(s, cmd.CatalogImport.RepositoryID)
		}
		if err != nil {
			return BackupResponse{Err: err}
		}
		return BackupResponse{
			Result:  &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()},
			Catalog: imported,
		}
	case "backup-test-verify":
		if cmd.TestRestoreVerify == nil || cmd.TestRestoreVerify.Valid() != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		if request.PreparedVerification == nil || !reflect.DeepEqual(request.PreparedVerification.Request, *cmd.TestRestoreVerify) {
			return BackupResponse{Err: ErrBackupConflict}
		}
		point, err := RecordTestRestoreVerification(s, *request.PreparedVerification)
		if err != nil {
			return BackupResponse{Err: err}
		}
		return BackupResponse{
			Result:  &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()},
			Catalog: []protocol.BackupRecoveryPoint{point},
		}
	case "backup-test-cleanup":
		if cmd.TestRestoreClean == nil || cmd.TestRestoreClean.Valid() != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		if request.PeerRepository == nil {
			run, err := matchingTestRestoreRun(s, *cmd.TestRestoreClean)
			if err != nil {
				return BackupResponse{Err: err}
			}
			if run.HostID != "" && run.HostID != s.State.HostID {
				poolVMMu.Lock()
				_, ownerErr := testRestoreOwner(s, run)
				poolVMMu.Unlock()
				if ownerErr != nil {
					return BackupResponse{Err: ownerErr}
				}
				if err := readyBackupTarget(s, run.HostID); err != nil {
					return BackupResponse{Err: err}
				}
				if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{*cmd}}); err != nil {
					return BackupResponse{Err: persistenceError{err}}
				}
				return BackupResponse{Result: backupResult(cmd.OperationID, s.State.Journal[cmd.OperationID])}
			}
		}
		var err error
		if request.PreparedCleanup != nil {
			if !reflect.DeepEqual(request.PreparedCleanup.Request, *cmd.TestRestoreClean) {
				return BackupResponse{Err: ErrBackupConflict}
			}
			err = finishTestRestoreCleanup(s, *request.PreparedCleanup)
		} else {
			err = CleanupTestRestoreVM(s, *cmd.TestRestoreClean)
		}
		if err != nil {
			return BackupResponse{Err: err}
		}
		return BackupResponse{
			Result: &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()},
		}
	}
	if !backupCommand(cmd.Kind) {
		return BackupResponse{Err: ErrBackupInvalid}
	}
	if old, ok := s.State.Journal[cmd.OperationID]; ok {
		if old.Command == nil || old.Command.Kind != cmd.Kind || !sameBackupRequest(old.Command, cmd) {
			return BackupResponse{Err: ErrBackupConflict}
		}
		if request.PeerRepository != nil && !old.PeerDispatched {
			old.PeerDispatched = true
			s.State.Journal[cmd.OperationID] = old
			if err := s.Save(); err != nil {
				return BackupResponse{Err: persistenceError{err}}
			}
		}
		return BackupResponse{Result: backupResult(cmd.OperationID, old)}
	}
	if request.PeerRepository == nil && (cmd.Kind == protocol.BackupKindRestoreNew ||
		cmd.Kind == protocol.BackupKindRestoreReplace || cmd.Kind == protocol.BackupKindTestRestore) {
		if run, err := loadPoolBackupRun(s, cmd.OperationID); err == nil {
			intent, intentErr := backupIntentSHA256(*cmd)
			if intentErr != nil || run.Kind != cmd.Kind || run.IntentSHA256 != intent {
				return BackupResponse{Err: ErrBackupConflict}
			}
			status, detail := run.Status, run.Error
			if status == "pending" {
				status, detail = "needs_attention", "retained restore run requires observation before retry"
			}
			return BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID,
				Status: status, Error: detail, ObservedAt: run.UpdatedAt}}
		} else if !errors.Is(err, os.ErrNotExist) {
			return BackupResponse{Err: err}
		}
	}
	manifestSHA := ""
	if restoreBackupCommand(cmd.Kind) {
		var err error
		manifestSHA, err = restoreCatalogManifestSHA(s, backupCommandRepositoryID(*cmd), backupCommandRecoveryPointID(*cmd))
		if err != nil {
			return BackupResponse{Err: err}
		}
	}
	switch cmd.Kind {
	case "backup-now":
		if cmd.Backup == nil || cmd.Backup.Valid() != nil || cmd.VM == nil || cmd.Restore != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		if pendingVMCommand(s.State.Journal, cmd.VM.ID) != "" || hasUnsettledBackup(s.State.Journal, cmd.VM.ID) || hasUnsettledSnapshot(s.State.Journal, cmd.VM.ID) {
			return BackupResponse{Err: ErrBackupConflict}
		}
		var vm protocol.VMCreateSpec
		var err error
		if cmd.VM.HostID != s.State.HostID {
			if request.PeerRepository != nil || readyBackupTarget(s, cmd.VM.HostID) != nil {
				return BackupResponse{Err: ErrBackupUnavailable}
			}
			vm, err = lookupPoolVMSpec(s, cmd.VM.ID)
		} else {
			vm, err = vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).VMDefinition(cmd.VM.ID)
		}
		if err != nil || vm.HostID != cmd.VM.HostID || vm.PoolID != s.State.PoolID || s.State.HostID == "" {
			return BackupResponse{Err: ErrBackupUnavailable}
		}
		if _, err := GetPoolBackupRepository(s, cmd.Backup.RepositoryID); err != nil {
			return BackupResponse{Err: err}
		}
		if vm.HostID == s.State.HostID {
			state, stateErr := vmhost.ManagedVMState(ctx, vm.ID)
			if stateErr != nil {
				return BackupResponse{Err: ErrBackupUnavailable}
			}
			if reason := vmhost.RefuseOnlineBackupAdmission(cmd.Backup.EffectiveConsistency(), vm, state); reason != "" {
				return BackupResponse{Err: fmt.Errorf("%w: %s", ErrBackupInvalid, reason)}
			}
		}
		accepted := protocol.Command{OperationID: cmd.OperationID, Kind: cmd.Kind, VM: &vm, Backup: cmd.Backup}
		intent, err := backupIntentSHA256(accepted)
		if err != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		if _, err := reserveBackupRun(s, protocol.BackupRun{
			OperationID: cmd.OperationID, Kind: protocol.BackupKindBackupNow, PoolID: s.State.PoolID,
			VMID: vm.ID, HostID: vm.HostID, RepositoryID: cmd.Backup.RepositoryID, RecoveryPointID: cmd.OperationID,
			IntentSHA256: intent, Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC(),
		}); err != nil {
			return BackupResponse{Err: err}
		}
		if err := applyResponseWithPeer(s, protocol.ReportResponse{Commands: []protocol.Command{accepted}}, request.PeerRepository != nil); err != nil {
			return BackupResponse{Err: persistenceError{err}}
		}
		return BackupResponse{Result: backupResult(cmd.OperationID, s.State.Journal[cmd.OperationID])}
	case "backup-restore-as-new":
		if cmd.Restore == nil || cmd.Restore.Valid() != nil || cmd.Backup != nil || cmd.RestoreReplace != nil || cmd.TestRestore != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		if readyBackupTarget(s, cmd.Restore.HostID) != nil {
			return BackupResponse{Err: ErrBackupUnavailable}
		}
		if busy, err := restoreTargetBusy(s, cmd.Restore.NewVMID, cmd.OperationID); err != nil {
			return BackupResponse{Err: err}
		} else if busy {
			return BackupResponse{Err: ErrBackupConflict}
		}
		if _, err := GetPoolBackupRepository(s, cmd.Restore.RepositoryID); err != nil {
			return BackupResponse{Err: err}
		}
		accepted := protocol.Command{OperationID: cmd.OperationID, Kind: cmd.Kind, Restore: cmd.Restore}
		intent, err := backupIntentSHA256(accepted)
		if err != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		run := protocol.BackupRun{
			OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreNew, PoolID: s.State.PoolID,
			RepositoryID: cmd.Restore.RepositoryID, RecoveryPointID: cmd.Restore.RecoveryPointID,
			NewVMID: cmd.Restore.NewVMID, HostID: cmd.Restore.HostID, IntentSHA256: intent, ManifestSHA256: manifestSHA, Stage: "reserved", Status: "pending",
			CreatedAt: time.Now().UTC(),
		}
		if request.PeerRepository == nil {
			err = reserveRestoreRunWithPin(s, run, protocol.BackupPinKindNew)
		} else {
			_, err = reserveBackupRun(s, run)
		}
		if err != nil {
			return BackupResponse{Err: err}
		}
		if err := applyResponseWithPeer(s, protocol.ReportResponse{Commands: []protocol.Command{accepted}}, request.PeerRepository != nil); err != nil {
			return BackupResponse{Err: persistenceError{err}}
		}
		return BackupResponse{Result: backupResult(cmd.OperationID, s.State.Journal[cmd.OperationID])}
	case protocol.BackupKindRestoreReplace:
		if cmd.RestoreReplace == nil || cmd.RestoreReplace.Valid() != nil || cmd.Backup != nil || cmd.Restore != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		if readyBackupTarget(s, cmd.RestoreReplace.HostID) != nil {
			return BackupResponse{Err: ErrBackupUnavailable}
		}
		if request.PeerRepository == nil {
			owner, err := lookupPoolVMSpec(s, cmd.RestoreReplace.OriginalVMID)
			if err != nil || owner.HostID != cmd.RestoreReplace.HostID {
				return BackupResponse{Err: ErrBackupUnavailable}
			}
		}
		if busy, err := restoreTargetBusy(s, cmd.RestoreReplace.OriginalVMID, cmd.OperationID); err != nil {
			return BackupResponse{Err: err}
		} else if busy {
			return BackupResponse{Err: ErrBackupConflict}
		}
		if _, err := GetPoolBackupRepository(s, cmd.RestoreReplace.RepositoryID); err != nil {
			return BackupResponse{Err: err}
		}
		accepted := protocol.Command{OperationID: cmd.OperationID, Kind: cmd.Kind, RestoreReplace: cmd.RestoreReplace}
		intent, err := backupIntentSHA256(accepted)
		if err != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		run := protocol.BackupRun{
			OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreReplace, PoolID: s.State.PoolID,
			VMID: cmd.RestoreReplace.OriginalVMID, HostID: cmd.RestoreReplace.HostID, RepositoryID: cmd.RestoreReplace.RepositoryID,
			RecoveryPointID: cmd.RestoreReplace.RecoveryPointID, IntentSHA256: intent, ManifestSHA256: manifestSHA,
			Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC(),
		}
		if request.PeerRepository == nil {
			err = reserveRestoreRunWithPin(s, run, protocol.BackupPinKindReplace)
		} else {
			_, err = reserveBackupRun(s, run)
		}
		if err != nil {
			return BackupResponse{Err: err}
		}
		if err := applyResponseWithPeer(s, protocol.ReportResponse{Commands: []protocol.Command{accepted}}, request.PeerRepository != nil); err != nil {
			return BackupResponse{Err: persistenceError{err}}
		}
		return BackupResponse{Result: backupResult(cmd.OperationID, s.State.Journal[cmd.OperationID])}
	case protocol.BackupKindTestRestore:
		if cmd.TestRestore == nil || cmd.TestRestore.Valid() != nil || cmd.Backup != nil || cmd.Restore != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		if readyBackupTarget(s, cmd.TestRestore.HostID) != nil {
			return BackupResponse{Err: ErrBackupUnavailable}
		}
		if busy, err := restoreTargetBusy(s, cmd.TestRestore.NewVMID, cmd.OperationID); err != nil {
			return BackupResponse{Err: err}
		} else if busy {
			return BackupResponse{Err: ErrBackupConflict}
		}
		if _, err := GetPoolBackupRepository(s, cmd.TestRestore.RepositoryID); err != nil {
			return BackupResponse{Err: err}
		}
		accepted := protocol.Command{OperationID: cmd.OperationID, Kind: cmd.Kind, TestRestore: cmd.TestRestore}
		intent, err := backupIntentSHA256(accepted)
		if err != nil {
			return BackupResponse{Err: ErrBackupInvalid}
		}
		run := protocol.BackupRun{
			OperationID: cmd.OperationID, Kind: protocol.BackupKindTestRestore, PoolID: s.State.PoolID,
			RepositoryID: cmd.TestRestore.RepositoryID, RecoveryPointID: cmd.TestRestore.RecoveryPointID,
			NewVMID: cmd.TestRestore.NewVMID, HostID: cmd.TestRestore.HostID, IntentSHA256: intent, ManifestSHA256: manifestSHA, Stage: "reserved", Status: "pending",
			CreatedAt: time.Now().UTC(),
		}
		if request.PeerRepository == nil {
			err = reserveRestoreRunWithPin(s, run, protocol.BackupPinKindTest)
		} else {
			_, err = reserveBackupRun(s, run)
		}
		if err != nil {
			return BackupResponse{Err: err}
		}
		if err := applyResponseWithPeer(s, protocol.ReportResponse{Commands: []protocol.Command{accepted}}, request.PeerRepository != nil); err != nil {
			return BackupResponse{Err: persistenceError{err}}
		}
		return BackupResponse{Result: backupResult(cmd.OperationID, s.State.Journal[cmd.OperationID])}
	default:
		return BackupResponse{Err: ErrBackupInvalid}
	}
}

func sameBackupRequest(stored, incoming *protocol.Command) bool {
	if stored == nil || incoming == nil || stored.Kind != incoming.Kind {
		return false
	}
	switch stored.Kind {
	case "backup-now":
		return stored.VM != nil && incoming.VM != nil && stored.VM.ID == incoming.VM.ID &&
			reflect.DeepEqual(stored.Backup, incoming.Backup)
	case "backup-restore-as-new":
		return reflect.DeepEqual(stored.Restore, incoming.Restore)
	case protocol.BackupKindRestoreReplace:
		return reflect.DeepEqual(stored.RestoreReplace, incoming.RestoreReplace)
	case protocol.BackupKindTestRestore:
		return reflect.DeepEqual(stored.TestRestore, incoming.TestRestore)
	default:
		return false
	}
}

func executeBackupCommand(ctx context.Context, dir string, identity State, cmd protocol.Command) (protocol.Result, error) {
	store := &Store{dir: dir, State: identity}
	observed := time.Now().UTC()
	switch cmd.Kind {
	case "backup-now":
		return executeBackupNow(ctx, store, cmd, observed)
	case "backup-restore-as-new":
		return executeRestoreAsNew(ctx, store, cmd, observed)
	case protocol.BackupKindRestoreReplace:
		return executeRestoreReplace(ctx, store, cmd, observed)
	case protocol.BackupKindTestRestore:
		return executeTestRestore(ctx, store, cmd, observed)
	default:
		return protocol.Result{OperationID: cmd.OperationID}, errors.New("unsupported backup operation")
	}
}

// ensureBackupRunReserved writes the durable pool-backup-runs/ record before native
// effects. Controller-dispatched backup commands land in the journal via applyResponse
// without acceptBackupRequest, so execution must reserve (idempotently) before settle.
func ensureBackupRunReserved(store *Store, cmd protocol.Command) error {
	membership, err := Membership(store)
	if err != nil {
		return ErrBackupUnavailable
	}
	if membership.MasterHostID != store.State.HostID {
		if existing, err := loadPoolBackupRun(store, cmd.OperationID); err != nil || existing.Kind != cmd.Kind {
			return ErrBackupUnavailable
		}
	}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		return ErrBackupInvalid
	}
	run := protocol.BackupRun{
		OperationID: cmd.OperationID, PoolID: store.State.PoolID, HostID: backupCommandTargetHost(cmd), IntentSHA256: intent,
		Stage: "reserved", Status: "pending", CreatedAt: time.Now().UTC(),
	}
	switch cmd.Kind {
	case "backup-now":
		if cmd.VM == nil || cmd.Backup == nil {
			return ErrBackupInvalid
		}
		run.Kind = protocol.BackupKindBackupNow
		run.VMID = cmd.VM.ID
		run.RepositoryID = cmd.Backup.RepositoryID
		run.RecoveryPointID = cmd.OperationID
	case "backup-restore-as-new":
		if cmd.Restore == nil {
			return ErrBackupInvalid
		}
		run.Kind = protocol.BackupKindRestoreNew
		run.RepositoryID = cmd.Restore.RepositoryID
		run.RecoveryPointID = cmd.Restore.RecoveryPointID
		run.NewVMID = cmd.Restore.NewVMID
	case protocol.BackupKindRestoreReplace:
		if cmd.RestoreReplace == nil {
			return ErrBackupInvalid
		}
		run.Kind = protocol.BackupKindRestoreReplace
		run.VMID = cmd.RestoreReplace.OriginalVMID
		run.RepositoryID = cmd.RestoreReplace.RepositoryID
		run.RecoveryPointID = cmd.RestoreReplace.RecoveryPointID
	case protocol.BackupKindTestRestore:
		if cmd.TestRestore == nil {
			return ErrBackupInvalid
		}
		run.Kind = protocol.BackupKindTestRestore
		run.RepositoryID = cmd.TestRestore.RepositoryID
		run.RecoveryPointID = cmd.TestRestore.RecoveryPointID
		run.NewVMID = cmd.TestRestore.NewVMID
	default:
		return ErrBackupInvalid
	}
	if restoreBackupCommand(cmd.Kind) {
		run.ManifestSHA256, err = restoreCatalogManifestSHA(store, run.RepositoryID, run.RecoveryPointID)
		if err != nil {
			return err
		}
	}
	if cmd.Kind == protocol.BackupKindBackupNow || membership.MasterHostID != store.State.HostID {
		_, err = reserveBackupRun(store, run)
		return err
	}
	pinKind := protocol.BackupPinKindNew
	if cmd.Kind == protocol.BackupKindRestoreReplace {
		pinKind = protocol.BackupPinKindReplace
	} else if cmd.Kind == protocol.BackupKindTestRestore {
		pinKind = protocol.BackupPinKindTest
	}
	return reserveRestoreRunWithPin(store, run, pinKind)
}

func executeBackupNow(ctx context.Context, store *Store, cmd protocol.Command, observed time.Time) (protocol.Result, error) {
	if cmd.VM == nil || cmd.Backup == nil || cmd.Backup.Valid() != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: "invalid backup-now command", ObservedAt: observed}, nil
	}
	if err := ensureBackupRunReserved(store, cmd); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	consistency := cmd.Backup.EffectiveConsistency()
	repo, err := GetPoolBackupRepository(store, cmd.Backup.RepositoryID)
	if err != nil {
		return failBackupRun(store, cmd, observed, err.Error()), nil
	}
	if repo.MountPath == "" {
		return failBackupRun(store, cmd, observed, "backup repository mount path is missing"), nil
	}
	if err := vmhost.VerifyBackupRepository(ctx, repo); err != nil {
		return failBackupRun(store, cmd, observed, err.Error()), nil
	}
	pointDir := filepath.Join(repo.MountPath, cmd.OperationID)
	if _, statErr := os.Stat(pointDir); statErr == nil {
		persisted, digest, readErr := vmhost.ReadBackupManifest(pointDir)
		if readErr != nil || persisted.RecoveryPointID != cmd.OperationID || persisted.RepositoryID != repo.ID ||
			persisted.PoolID != store.State.PoolID || persisted.SourceVMID != cmd.VM.ID ||
			persisted.Consistency != consistency || vmhost.VerifyPublishedBackupRecoveryPoint(pointDir, persisted) != nil {
			return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: "published recovery point cannot be reconciled", ObservedAt: observed}, nil
		}
		point := protocol.BackupRecoveryPoint{
			ID: cmd.OperationID, RepositoryID: repo.ID, PoolID: store.State.PoolID,
			SourceVMID: persisted.SourceVMID, SourceVMName: persisted.SourceVMName,
			CapturedAt: persisted.CapturedAt, Consistency: persisted.Consistency, ManifestSHA256: digest,
		}
		intent, _ := backupIntentSHA256(cmd)
		if err := SettlePoolBackupRun(store, protocol.BackupRun{
			OperationID: cmd.OperationID, Kind: protocol.BackupKindBackupNow, PoolID: store.State.PoolID,
			VMID: cmd.VM.ID, RepositoryID: repo.ID, RecoveryPointID: cmd.OperationID,
			IntentSHA256: intent, Stage: "published", Status: "succeeded", CreatedAt: observed,
		}, &point); err != nil {
			return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
		}
		return protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: observed}, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: statErr.Error(), ObservedAt: observed}, nil
	}
	engine := vmhost.NewManaged(filepath.Join(store.dir, "managed-vms"))
	vm, err := engine.VMDefinition(cmd.VM.ID)
	if err != nil {
		return failBackupRun(store, cmd, observed, "source VM definition is unavailable"), nil
	}
	state, stateErr := vmhost.ManagedVMState(ctx, vm.ID)
	if stateErr != nil {
		return failBackupRun(store, cmd, observed, "source VM state is unavailable"), nil
	}
	if reason := vmhost.RefuseOnlineBackupAdmission(consistency, vm, state); reason != "" {
		return failBackupRun(store, cmd, observed, reason), nil
	}
	disks, err := engine.BackupDiskSources(vm.ID)
	if err != nil {
		return failBackupRun(store, cmd, observed, err.Error()), nil
	}
	specDigest, diskDigest, err := vmhost.BackupSpecAndDiskDigests(vm, disks)
	if err != nil {
		return failBackupRun(store, cmd, observed, err.Error()), nil
	}

	workRoot := filepath.Join(store.dir, "backup-staging", cmd.OperationID)
	if protocol.BackupConsistencyRunning(consistency) {
		// QEMU writes as libvirt-qemu; keep push-mode outputs on the repository mount.
		workRoot = filepath.Join(repo.MountPath, ".nex-backup-work", cmd.OperationID)
	}
	var workErr error
	if protocol.BackupConsistencyRunning(consistency) {
		workErr = vmhost.PrepareOnlineBackupWorkDir(ctx, workRoot)
	} else {
		workErr = os.MkdirAll(workRoot, 0750)
	}
	if workErr != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: workErr.Error(), ObservedAt: observed}, nil
	}
	cleanupWork := false
	defer func() {
		if cleanupWork {
			_ = os.RemoveAll(workRoot)
		}
	}()

	stageFiles := map[string]string{}
	artifacts := make([]protocol.BackupDiskArtifact, 0, len(disks))
	if protocol.BackupConsistencyRunning(consistency) {
		destinations := map[string]string{}
		for _, disk := range disks {
			name := disk.Target + ".qcow2"
			dest := filepath.Join(workRoot, name)
			if err := vmhost.PrepareOnlineBackupTarget(ctx, vm.ID, disk.Target, dest); err != nil {
				return failBackupRun(store, cmd, observed, err.Error()), nil
			}
			destinations[disk.Target] = dest
		}
		if err := vmhost.CaptureOnlineDisks(ctx, vm.ID, disks, destinations); err != nil {
			if inactiveErr := vmhost.OnlineBackupJobInactive(context.Background(), vm.ID); inactiveErr != nil {
				return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: inactiveErr.Error(), ObservedAt: observed}, nil
			}
			return failBackupRun(store, cmd, observed, err.Error()), nil
		}
		for _, disk := range disks {
			name := disk.Target + ".qcow2"
			dest := destinations[disk.Target]
			sum, size, sumErr := backupFileDigest(dest)
			if sumErr != nil {
				return failBackupRun(store, cmd, observed, sumErr.Error()), nil
			}
			artifacts = append(artifacts, protocol.BackupDiskArtifact{Target: disk.Target, Filename: name, Format: "qcow2", Size: size, SHA256: sum})
			stageFiles[name] = dest
		}
	} else {
		for _, disk := range disks {
			name := disk.Target + ".qcow2"
			dest := filepath.Join(workRoot, name)
			if err := vmhost.ConvertStoppedDiskStandalone(ctx, disk.Source, dest); err != nil {
				return failBackupRun(store, cmd, observed, err.Error()), nil
			}
			sum, size, sumErr := backupFileDigest(dest)
			if sumErr != nil {
				return failBackupRun(store, cmd, observed, sumErr.Error()), nil
			}
			artifacts = append(artifacts, protocol.BackupDiskArtifact{Target: disk.Target, Filename: name, Format: "qcow2", Size: size, SHA256: sum})
			stageFiles[name] = dest
		}
	}

	firmware, fwErr := vmhost.CaptureBackupFirmwareArtifacts(ctx, vm.ID, workRoot, protocol.BackupConsistencyRunning(consistency))
	if fwErr != nil {
		return failBackupRun(store, cmd, observed, fwErr.Error()), nil
	}
	for _, fw := range firmware {
		stageFiles[fw.Filename] = filepath.Join(workRoot, fw.Filename)
	}

	manifest, err := vmhost.BuildBackupManifest(cmd.OperationID, repo.ID, store.State.PoolID, vm.ID, vm.Name, consistency, specDigest, diskDigest, vm, artifacts, firmware)
	if err != nil {
		return failBackupRun(store, cmd, observed, err.Error()), nil
	}
	manifestSHA, err := vmhost.PublishBackupRecoveryPoint(repo.MountPath, manifest, stageFiles)
	if err != nil {
		return failBackupRun(store, cmd, observed, err.Error()), nil
	}
	cleanupWork = true
	persisted, persistedSHA, err := vmhost.ReadBackupManifest(filepath.Join(repo.MountPath, cmd.OperationID))
	if err != nil || persistedSHA != manifestSHA || persisted.RepositoryID != repo.ID || persisted.PoolID != store.State.PoolID {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: "published backup manifest cannot be reconciled", ObservedAt: observed}, nil
	}
	point := protocol.BackupRecoveryPoint{
		ID: cmd.OperationID, RepositoryID: repo.ID, PoolID: store.State.PoolID, SourceVMID: vm.ID,
		SourceVMName: persisted.SourceVMName, CapturedAt: persisted.CapturedAt, Consistency: persisted.Consistency,
		ManifestSHA256: persistedSHA,
	}
	intent, _ := backupIntentSHA256(cmd)
	if err := SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindBackupNow, PoolID: store.State.PoolID,
		VMID: vm.ID, RepositoryID: repo.ID, RecoveryPointID: cmd.OperationID, IntentSHA256: intent,
		Stage: "published", Status: "succeeded", CreatedAt: observed, UpdatedAt: observed,
	}, &point); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	return protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: observed}, nil
}

func executeRestoreAsNew(ctx context.Context, store *Store, cmd protocol.Command, observed time.Time) (protocol.Result, error) {
	if cmd.Restore == nil || cmd.Restore.Valid() != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: "invalid restore-as-new command", ObservedAt: observed}, nil
	}
	if err := ensureBackupRunReserved(store, cmd); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	repo, err := GetPoolBackupRepository(store, cmd.Restore.RepositoryID)
	if err != nil {
		return failRestoreRun(store, cmd, observed, err.Error()), nil
	}
	if repo.MountPath == "" {
		return failRestoreRun(store, cmd, observed, "backup repository mount path is missing"), nil
	}
	pointDir := filepath.Join(repo.MountPath, cmd.Restore.RecoveryPointID)
	run, err := loadPoolBackupRun(store, cmd.OperationID)
	if err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	manifest, manifestSHA, err := validatedCatalogBackupManifest(ctx, store, repo, cmd.Restore.RecoveryPointID, run.ManifestSHA256)
	if err != nil {
		if !restorePlacementHasNoEffects(store, cmd.OperationID, cmd.Restore.NewVMID) {
			return attentionRestoreRun(store, cmd, observed, err.Error()), nil
		}
		return failRestoreRun(store, cmd, observed, err.Error()), nil
	}
	if manifest.CapturedSpec == nil {
		return failRestoreRun(store, cmd, observed, "recovery point lacks a captured VM specification; re-backup before restore-as-new"), nil
	}
	localDir := filepath.Join(store.dir, "backup-restore", cmd.OperationID)
	if err := vmhost.CopyBackupArtifactsLocal(ctx, pointDir, localDir, manifest); err != nil {
		return failRestoreRun(store, cmd, observed, err.Error()), nil
	}
	spec, err := vmhost.BuildRestoreAsNewSpec(*manifest.CapturedSpec, *cmd.Restore, store.State.PoolID, store.State.HostID)
	if err != nil {
		return failRestoreRun(store, cmd, observed, err.Error()), nil
	}
	engine := vmhost.NewManaged(filepath.Join(store.dir, "managed-vms"))
	if err := requestBackupRestorePlacement(ctx, store, spec, cmd.OperationID); err != nil {
		if restorePlacementHasNoEffects(store, cmd.OperationID, spec.ID) {
			return failRestoreRun(store, cmd, observed, err.Error()), nil
		}
		return attentionRestoreRun(store, cmd, observed, err.Error()), nil
	}
	if err := registerBackupRestoredAsNew(ctx, engine, cmd.OperationID, spec, localDir, manifestSHA, manifest.Disks, manifest.Firmware...); err != nil {
		if !errors.Is(err, vmhost.ErrRestorePlacementUncertain) && releaseProvenFailedRestorePlacement(ctx, store, spec, cmd.OperationID) == nil {
			return failRestoreRun(store, cmd, observed, err.Error()), nil
		}
		return attentionRestoreRun(store, cmd, observed, err.Error()), nil
	}
	intent, _ := backupIntentSHA256(cmd)
	if err := SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreNew, PoolID: store.State.PoolID,
		RepositoryID: repo.ID, RecoveryPointID: cmd.Restore.RecoveryPointID, NewVMID: cmd.Restore.NewVMID,
		IntentSHA256: intent, Stage: "registered", Status: "succeeded", CreatedAt: observed, UpdatedAt: observed,
	}, nil); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}, nil
	}
	_ = clearBackupRestorePin(store, cmd.Restore.RecoveryPointID, cmd.OperationID)
	return protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: observed}, nil
}

func failBackupRun(store *Store, cmd protocol.Command, observed time.Time, message string) protocol.Result {
	intent, _ := backupIntentSHA256(cmd)
	_ = SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindBackupNow, PoolID: store.State.PoolID,
		VMID: cmd.VM.ID, RepositoryID: cmd.Backup.RepositoryID, RecoveryPointID: cmd.OperationID,
		IntentSHA256: intent, Stage: "failed", Status: "failed", Error: message, CreatedAt: observed, UpdatedAt: observed,
	}, nil)
	return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: message, ObservedAt: observed}
}

func failRestoreRun(store *Store, cmd protocol.Command, observed time.Time, message string) protocol.Result {
	intent, _ := backupIntentSHA256(cmd)
	if err := SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreNew, PoolID: store.State.PoolID,
		RepositoryID: cmd.Restore.RepositoryID, RecoveryPointID: cmd.Restore.RecoveryPointID, NewVMID: cmd.Restore.NewVMID,
		IntentSHA256: intent, Stage: "failed", Status: "failed", Error: message, CreatedAt: observed, UpdatedAt: observed,
	}, nil); err != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: err.Error(), ObservedAt: observed}
	}
	_ = clearBackupRestorePin(store, cmd.Restore.RecoveryPointID, cmd.OperationID)
	return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: message, ObservedAt: observed}
}

// A missing exact native journal and missing master owner record prove a local
// placement refusal happened before disk allocation or definition. Member
// placement cannot use this local proof because its owner record is remote.
func restorePlacementHasNoEffects(store *Store, operationID, vmID string) bool {
	membership, err := Membership(store)
	if err != nil || membership.MasterHostID != store.State.HostID {
		return false
	}
	pool, err := loadPoolVMs(store)
	if err != nil {
		return false
	}
	if _, exists := pool.VMs[vmID]; exists {
		return false
	}
	_, err = os.Lstat(filepath.Join(store.dir, "managed-vms", operationID+".json"))
	return errors.Is(err, os.ErrNotExist)
}

func attentionRestoreRun(store *Store, cmd protocol.Command, observed time.Time, message string) protocol.Result {
	intent, _ := backupIntentSHA256(cmd)
	_ = SettlePoolBackupRun(store, protocol.BackupRun{
		OperationID: cmd.OperationID, Kind: protocol.BackupKindRestoreNew, PoolID: store.State.PoolID,
		RepositoryID: cmd.Restore.RepositoryID, RecoveryPointID: cmd.Restore.RecoveryPointID,
		NewVMID: cmd.Restore.NewVMID, IntentSHA256: intent, Stage: "placement", Status: "needs_attention",
		Error: message, CreatedAt: observed, UpdatedAt: observed,
	}, nil)
	return protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: message, ObservedAt: observed}
}

func reconcileRestoreAsNewAttention(ctx context.Context, dir string, identity State, cmd protocol.Command) (protocol.Result, error) {
	return reconcileNewVMRestoreAttention(ctx, dir, identity, cmd)
}

func reconcileTestRestoreAttention(ctx context.Context, dir string, identity State, cmd protocol.Command) (protocol.Result, error) {
	return reconcileNewVMRestoreAttention(ctx, dir, identity, cmd)
}

// reconcileNewVMRestoreAttention observes retained placement and resumes only
// an exact allocating journal; later native stages remain observation-only.
func reconcileNewVMRestoreAttention(ctx context.Context, dir string, identity State, cmd protocol.Command) (protocol.Result, error) {
	observed := time.Now().UTC()
	result := protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", ObservedAt: observed}
	store := &Store{dir: dir, State: identity}
	run, err := loadPoolBackupRun(store, cmd.OperationID)
	if err != nil || run.Status != "needs_attention" || run.Kind != cmd.Kind {
		result.Error = "restore run has no retained uncertain state"
		return result, nil
	}
	result.Error = run.Error
	var req protocol.RestoreAsNewRequest
	switch cmd.Kind {
	case protocol.BackupKindRestoreNew:
		if cmd.Restore == nil {
			result.Error = "restore request is missing"
			return result, nil
		}
		req = *cmd.Restore
	case protocol.BackupKindTestRestore:
		if cmd.TestRestore == nil {
			result.Error = "test restore request is missing"
			return result, nil
		}
		req = protocol.RestoreAsNewRequest{RepositoryID: cmd.TestRestore.RepositoryID,
			RecoveryPointID: cmd.TestRestore.RecoveryPointID, NewVMID: cmd.TestRestore.NewVMID,
			NewVMName: cmd.TestRestore.NewVMName, StorageID: cmd.TestRestore.StorageID, HostID: cmd.TestRestore.HostID}
	default:
		result.Error = "invalid restore reconciliation"
		return result, nil
	}
	if run.RepositoryID != req.RepositoryID || run.RecoveryPointID != req.RecoveryPointID || run.NewVMID != req.NewVMID || run.HostID != req.HostID {
		result.Error = "restore run differs from its command"
		return result, nil
	}
	intent, err := backupIntentSHA256(cmd)
	if err != nil || run.IntentSHA256 != intent {
		result.Error = "restore run intent differs from its command"
		return result, nil
	}
	repo, err := GetPoolBackupRepository(store, req.RepositoryID)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	manifest, manifestSHA, err := validatedCatalogBackupManifest(ctx, store, repo, req.RecoveryPointID, run.ManifestSHA256)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	if manifest.CapturedSpec == nil {
		result.Error = "restore manifest is unavailable for reconciliation"
		return result, nil
	}
	spec, err := vmhost.BuildRestoreAsNewSpec(*manifest.CapturedSpec, req, identity.PoolID, identity.HostID)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	engine := vmhost.NewManaged(filepath.Join(dir, "managed-vms"))
	allocating, err := restoreAsNewAllocating(engine, cmd.OperationID, spec, manifestSHA, manifest.Disks, manifest.Firmware)
	if errors.Is(err, vmhost.ErrRestoreManifestUnbound) {
		localDir := filepath.Join(dir, "backup-restore", cmd.OperationID)
		if bindErr := bindLegacyRestoreAsNewAllocating(engine, ctx, cmd.OperationID, spec, localDir, manifestSHA, manifest.Disks, manifest.Firmware); bindErr != nil {
			result.Error = bindErr.Error()
			return result, nil
		}
		allocating, err = restoreAsNewAllocating(engine, cmd.OperationID, spec, manifestSHA, manifest.Disks, manifest.Firmware)
	}
	if err != nil {
		if !errors.Is(err, vmhost.ErrRestoreAllocationJournalUnavailable) || result.Error == "" {
			result.Error = err.Error()
		}
		return result, nil
	}
	if allocating {
		localDir := filepath.Join(dir, "backup-restore", cmd.OperationID)
		if err := vmhost.CopyBackupArtifactsLocal(ctx, filepath.Join(repo.MountPath, req.RecoveryPointID), localDir, manifest); err != nil {
			result.Error = err.Error()
			return result, nil
		}
		if cmd.Kind == protocol.BackupKindTestRestore {
			err = registerTestRestoredAsNew(ctx, engine, cmd.OperationID, spec, localDir, manifestSHA, manifest.Disks, manifest.Firmware...)
		} else {
			err = registerBackupRestoredAsNew(ctx, engine, cmd.OperationID, spec, localDir, manifestSHA, manifest.Disks, manifest.Firmware...)
		}
		if err != nil {
			result.Error = err.Error()
			return result, nil
		}
	}
	native, err := reconcileRestoredAsNew(engine, ctx, cmd.OperationID, spec)
	if err != nil || native.Status != "succeeded" {
		if err != nil {
			result.Error = err.Error()
		} else if native.Error != "" {
			result.Error = native.Error
		} else if result.Error == "" {
			result.Error = "native restored VM outcome remains unresolved"
		}
		return result, nil
	}
	if membership, err := Membership(store); err != nil {
		result.Error = err.Error()
		return result, nil
	} else if membership.MasterHostID == identity.HostID {
		pool, err := loadPoolVMs(store)
		record, ok := pool.VMs[spec.ID]
		if err != nil || !ok || record.HostID != spec.HostID || record.PoolID != spec.PoolID ||
			record.CreateOperationID != cmd.OperationID ||
			!reflect.DeepEqual(record.Spec, spec) || record.PendingRecovery != nil || record.PendingUpdate != nil {
			result.Error = "pool VM owner record does not prove restored placement"
			return result, nil
		}
	}
	if err := SettlePoolBackupRun(store, protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind,
		PoolID: identity.PoolID, HostID: req.HostID, RepositoryID: req.RepositoryID,
		RecoveryPointID: req.RecoveryPointID, NewVMID: req.NewVMID, IntentSHA256: intent,
		Stage: "registered", Status: "succeeded", CreatedAt: run.CreatedAt, UpdatedAt: observed,
	}, nil); err != nil {
		result.Error = err.Error()
		return result, nil
	}
	if cmd.Kind != protocol.BackupKindTestRestore {
		_ = clearBackupRestorePin(store, req.RecoveryPointID, cmd.OperationID)
	}
	return protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: observed}, nil
}

func backupFileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
