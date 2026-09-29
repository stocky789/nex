// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
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

type pendingPeerBackup struct {
	store     *Store
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

type peerBackupCredential struct {
	Repository      protocol.BackupRepository    `json:"repository"`
	Receipt         backupCredentialReceipt      `json:"receipt"`
	RecipientHostID string                       `json:"recipient_host_id"`
	Credentials     protocol.StorageCredentials  `json:"credentials"`
	Retired         []backupCredentialRetirement `json:"retired,omitempty"`
}

type backupCredentialRetirement struct {
	Reference string `json:"reference"`
	Kind      string `json:"kind"`
}

func exportPeerBackupCredential(s *Store, repo protocol.BackupRepository, recipientHostID string) (peerBackupCredential, error) {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return peerBackupCredential{}, ErrBackupUnavailable
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return peerBackupCredential{}, ErrBackupUnavailable
	}
	member, ok := poolMemberByID(membership, recipientHostID)
	if !ok || member.Status != "ready" || recipientHostID == s.State.HostID {
		return peerBackupCredential{}, ErrBackupUnavailable
	}
	projection, err := localBackupCredential(s, repo)
	projection.RecipientHostID = recipientHostID
	return projection, err
}

func localBackupCredential(s *Store, repo protocol.BackupRepository) (peerBackupCredential, error) {
	var empty peerBackupCredential
	if !backupCredentialKind(repo.Kind) || repo.PoolID != s.State.PoolID || repo.Generation == 0 {
		return empty, ErrBackupInvalid
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	key, err := clusterSecretKey(s)
	if err != nil {
		return empty, err
	}
	dir, err := backupCredentialDir(s)
	if err != nil {
		return empty, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return empty, err
	}
	var projection peerBackupCredential
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := readClusterSecret(filepath.Join(dir, entry.Name()))
		if err != nil {
			return empty, err
		}
		var stored storedBackupCredential
		if json.Unmarshal(raw, &stored) != nil {
			return empty, errors.New("backup credential receipt is corrupt")
		}
		r := stored.Receipt
		if r.PoolID == repo.PoolID && r.HostID == s.State.HostID && r.RepositoryID == repo.ID &&
			r.Reference != repo.CredentialRef && !r.RetiredAt.IsZero() {
			if !hmac.Equal([]byte(stored.Proof), []byte(backupCredentialProof(key, r, stored.Credentials))) {
				return empty, errors.New("backup credential retirement is invalid")
			}
			projection.Retired = append(projection.Retired, backupCredentialRetirement{Reference: r.Reference, Kind: r.Kind})
		}
		if r.Reference != repo.CredentialRef {
			continue
		}
		if r.PoolID != repo.PoolID || r.HostID != s.State.HostID || r.RepositoryID != repo.ID || r.Kind != repo.Kind || !r.RetiredAt.IsZero() ||
			!hmac.Equal([]byte(stored.Proof), []byte(backupCredentialProof(key, r, stored.Credentials))) {
			return empty, errors.New("backup credential receipt scope is invalid")
		}
		projection.Repository, projection.Receipt, projection.Credentials = portablePeerRepository(repo), r, stored.Credentials
	}
	if projection.Receipt.Reference == "" {
		return empty, errors.New("backup credential receipt is unavailable")
	}
	return projection, nil
}

func importPeerBackupCredential(s *Store, projection peerBackupCredential) error {
	repo, receipt := projection.Repository, projection.Receipt
	if repo.Valid() != nil || !backupCredentialKind(repo.Kind) || repo.Generation == 0 || repo.PoolID != s.State.PoolID ||
		repo.CredentialRef != receipt.Reference || repo.ID != receipt.RepositoryID || repo.Kind != receipt.Kind ||
		receipt.PoolID != repo.PoolID || receipt.HostID == s.State.HostID || projection.RecipientHostID != s.State.HostID || !receipt.RetiredAt.IsZero() ||
		!protocol.ValidSnapshotOperationID(receipt.RequestID) || !protocol.ValidSnapshotOperationID(receipt.OperationID) {
		return ErrBackupInvalid
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID == s.State.HostID {
		return ErrBackupUnavailable
	}
	self, ok := poolMemberByID(membership, s.State.HostID)
	if !ok || self.Status != "ready" {
		return ErrBackupUnavailable
	}
	current, err := GetPoolBackupRepository(s, repo.ID)
	if err == nil && (current.Generation > repo.Generation || current.Generation == repo.Generation &&
		(current.Kind != repo.Kind || current.CredentialRef != repo.CredentialRef || repo.Kind == protocol.BackupRepoKindS3 &&
			(!reflect.DeepEqual(current.S3Source, repo.S3Source) || !reflect.DeepEqual(current.RemoteIdentity, repo.RemoteIdentity)))) {
		return ErrBackupConflict
	}
	if err != nil && !errors.Is(err, ErrBackupRepoNotFound) {
		return err
	}
	// Recheck the material through the same scope validator used by controller handoff.
	local := receipt
	local.HostID = s.State.HostID
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: local.RequestID, RegistrationRequestID: "backup-credential:" + local.Kind + ":" + local.OperationID,
		PoolID: local.PoolID, HostID: local.HostID, RepositoryID: local.RepositoryID,
		ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: projection.Credentials}
	if _, err := backupCredentialScope(s, handoff); err != nil {
		return ErrBackupInvalid
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	key, err := clusterSecretKey(s)
	if err != nil {
		return err
	}
	path, err := backupCredentialPath(s, local.RequestID)
	if err != nil {
		return err
	}
	if raw, err := readClusterSecret(path); err == nil {
		var existing storedBackupCredential
		if json.Unmarshal(raw, &existing) != nil || existing.Receipt != local ||
			!hmac.Equal([]byte(existing.Proof), []byte(backupCredentialProof(key, existing.Receipt, existing.Credentials))) ||
			!hmac.Equal([]byte(existing.Proof), []byte(backupCredentialProof(key, local, projection.Credentials))) {
			return ErrBackupConflict
		}
		return retireProjectedBackupCredentialsLocked(s, key, repo, projection.Retired)
	} else if !os.IsNotExist(err) {
		return err
	}
	stored := storedBackupCredential{Receipt: local, Credentials: projection.Credentials}
	stored.Proof = backupCredentialProof(key, local, projection.Credentials)
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	if err := writePrivate(path, raw, 0600); err != nil {
		return err
	}
	return retireProjectedBackupCredentialsLocked(s, key, repo, projection.Retired)
}

func retireProjectedBackupCredentialsLocked(s *Store, key []byte, repo protocol.BackupRepository, retired []backupCredentialRetirement) error {
	if len(retired) == 0 {
		return nil
	}
	wanted := make(map[string]string, len(retired))
	for _, old := range retired {
		if !protocol.ValidSnapshotOperationID(old.Reference) || old.Reference == repo.CredentialRef || !backupCredentialKind(old.Kind) ||
			(wanted[old.Reference] != "" && wanted[old.Reference] != old.Kind) {
			return ErrBackupInvalid
		}
		wanted[old.Reference] = old.Kind
	}
	dir, err := backupCredentialDir(s)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := readClusterSecret(path)
		if err != nil {
			return err
		}
		var stored storedBackupCredential
		if json.Unmarshal(raw, &stored) != nil {
			return errors.New("backup credential receipt is corrupt")
		}
		r := stored.Receipt
		kind, ok := wanted[r.Reference]
		if !ok {
			continue
		}
		if r.PoolID != repo.PoolID || r.HostID != s.State.HostID || r.RepositoryID != repo.ID || r.Kind != kind ||
			!hmac.Equal([]byte(stored.Proof), []byte(backupCredentialProof(key, r, stored.Credentials))) {
			return ErrBackupConflict
		}
		if r.RetiredAt.IsZero() {
			stored.Receipt.RetiredAt = time.Now().UTC()
			stored.Credentials = protocol.StorageCredentials{}
			stored.Proof = backupCredentialProof(key, stored.Receipt, stored.Credentials)
			encoded, err := json.Marshal(stored)
			if err != nil {
				return err
			}
			if err := writePrivate(path, encoded, 0600); err != nil {
				return err
			}
		}
		if kind == protocol.BackupRepoKindSMB3 {
			if err := vmhost.RetireBackupRepositorySMBCredential(s.dir, r.Reference); err != nil {
				return err
			}
		}
	}
	return nil
}

// The caller authenticates the current master through the pinned peer channel.
func handlePeerBackupCredential(s *Store, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var projection peerBackupCredential
	decoder := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&projection) != nil || importPeerBackupCredential(s, projection) != nil {
		http.Error(w, "backup credential unavailable", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handlePeerBackupCredentials(s *Store, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	member, err := AuthenticatePoolPeer(s, r)
	if err != nil {
		http.Error(w, "pool member rejected", http.StatusUnauthorized)
		return
	}
	state, err := loadPoolBackupRepositories(s)
	if err != nil {
		http.Error(w, "backup credentials unavailable", http.StatusConflict)
		return
	}
	projections := []peerBackupCredential{}
	for _, repo := range state.Repositories {
		if !backupCredentialKind(repo.Kind) {
			continue
		}
		projection, err := exportPeerBackupCredential(s, repo, member.HostID)
		if err != nil {
			http.Error(w, "backup credentials unavailable", http.StatusConflict)
			return
		}
		projections = append(projections, projection)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(projections)
}

func pullPeerBackupCredentials(ctx context.Context, s *Store, masterURL string) error {
	client, err := PoolPeerClient(s, masterURL)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(masterURL, "/")+poolBackupPeerPath+"/credentials", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("pool backup credentials unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("pool backup credentials unavailable")
	}
	var projections []peerBackupCredential
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&projections) != nil {
		return errors.New("pool backup credentials are invalid")
	}
	for _, projection := range projections {
		if err := importPeerBackupCredential(s, projection); err != nil {
			return errors.New("pool backup credentials are invalid")
		}
	}
	return nil
}

func deliverPeerBackupCredential(ctx context.Context, s *Store, member protocol.PoolMember, repo protocol.BackupRepository) error {
	projection, err := exportPeerBackupCredential(s, repo, member.HostID)
	if err != nil {
		return err
	}
	client, err := poolTransferClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	work, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolBackupPeerPath+"/credential", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("member backup credential delivery unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized ||
			response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound ||
			response.StatusCode == http.StatusMethodNotAllowed {
			return permanentPeerBackupError{errors.New("member backup credential delivery refused")}
		}
		return errors.New("member backup credential delivery refused")
	}
	return nil
}

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
	if localRepo.Kind != protocol.BackupRepoKindNFS && localRepo.Kind != protocol.BackupRepoKindSMB3 && localRepo.Kind != protocol.BackupRepoKindSFTP && localRepo.Kind != protocol.BackupRepoKindS3 {
		return pendingPeerBackup{}, ErrBackupUnavailable
	}
	if localRepo.Kind == protocol.BackupRepoKindSMB3 {
		if err := requirePoolAuthorityWritable(s); err != nil || !validPeerSMBIdentity(portablePeerRepository(localRepo), localRepo.SMBIdentity) {
			return pendingPeerBackup{}, ErrBackupUnavailable
		}
	}
	if localRepo.Kind == protocol.BackupRepoKindSFTP {
		if err := requirePoolAuthorityWritable(s); err != nil || !validPeerSFTPIdentity(portablePeerRepository(localRepo)) {
			return pendingPeerBackup{}, ErrBackupUnavailable
		}
	}
	if localRepo.Kind == protocol.BackupRepoKindS3 {
		if err := requirePoolAuthorityWritable(s); err != nil || !validPeerS3Identity(portablePeerRepository(localRepo)) {
			return pendingPeerBackup{}, ErrBackupUnavailable
		}
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
	return pendingPeerBackup{store: s, run: run, member: member, command: cmd, repo: repo, localRepo: localRepo, point: point, client: client}, nil
}

func portablePeerRepository(repo protocol.BackupRepository) protocol.BackupRepository {
	repo.MountPath = ""
	if repo.Kind != protocol.BackupRepoKindNFS {
		repo.Identity = nil
	}
	repo.SMBIdentity = nil
	repo.Access = protocol.StorageAccess{}
	repo.MemberAccess = nil
	return repo
}

func validPeerSFTPIdentity(repo protocol.BackupRepository) bool {
	if repo.Kind != protocol.BackupRepoKindSFTP || repo.Valid() != nil || repo.Generation == 0 || repo.CredentialRef == "" ||
		repo.SFTPSource == nil || repo.MountPath != "" || repo.RemoteIdentity == nil || len(repo.RemoteIdentity.Fingerprint) != 64 {
		return false
	}
	_, err := hex.DecodeString(repo.RemoteIdentity.Fingerprint)
	return err == nil && strings.ToLower(repo.RemoteIdentity.Fingerprint) == repo.RemoteIdentity.Fingerprint
}

func validPeerS3Identity(repo protocol.BackupRepository) bool {
	if repo.Kind != protocol.BackupRepoKindS3 || repo.Valid() != nil || repo.Generation == 0 || repo.CredentialRef == "" ||
		repo.S3Source == nil || repo.MountPath != "" || repo.RemoteIdentity == nil || len(repo.RemoteIdentity.Fingerprint) != 64 {
		return false
	}
	_, err := hex.DecodeString(repo.RemoteIdentity.Fingerprint)
	return err == nil && strings.ToLower(repo.RemoteIdentity.Fingerprint) == repo.RemoteIdentity.Fingerprint
}

func preparePeerS3Repository(ctx context.Context, s *Store, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	if !validPeerS3Identity(repo) {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID == s.State.HostID {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	self, ok := poolMemberByID(membership, s.State.HostID)
	if !ok || self.Status != "ready" {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	current, err := GetPoolBackupRepository(s, repo.ID)
	if err == nil && (current.Generation > repo.Generation || current.Generation == repo.Generation &&
		(current.Kind != repo.Kind || current.CredentialRef != repo.CredentialRef || !reflect.DeepEqual(current.S3Source, repo.S3Source) ||
			!reflect.DeepEqual(current.RemoteIdentity, repo.RemoteIdentity))) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	if err != nil && !errors.Is(err, ErrBackupRepoNotFound) {
		return protocol.BackupRepository{}, err
	}
	prepared, err := prepareBackupRepositoryForAdoption(ctx, s, repo)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	if !preparedBackupRepositoryMatches(repo, prepared) || !reflect.DeepEqual(prepared.RemoteIdentity, repo.RemoteIdentity) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	return prepared, nil
}

func preflightPeerS3Repository(ctx context.Context, work pendingPeerBackup) error {
	if err := requirePoolAuthorityWritable(work.store); err != nil {
		return permanentPeerBackupError{ErrBackupUnavailable}
	}
	current, err := GetPoolBackupRepository(work.store, work.localRepo.ID)
	if err != nil || current.Generation != work.localRepo.Generation || current.CredentialRef != work.localRepo.CredentialRef ||
		!reflect.DeepEqual(current.S3Source, work.localRepo.S3Source) || !reflect.DeepEqual(current.RemoteIdentity, work.localRepo.RemoteIdentity) {
		return permanentPeerBackupError{ErrBackupConflict}
	}
	body, err := json.Marshal(work.repo)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(work.member.PeerOrigin, "/")+poolBackupPeerPath+"/capability/s3", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := work.client.Do(request)
	if err != nil {
		return errors.New("member S3 backup capability unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusBadRequest ||
		response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return permanentPeerBackupError{errors.New("member S3 backup capability is unsupported or refused")}
	}
	if response.StatusCode != http.StatusOK {
		return errors.New("member S3 backup capability unavailable")
	}
	var reply peerS3CapabilityResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&reply) != nil || reply.Generation != work.repo.Generation || reply.CredentialRef != work.repo.CredentialRef ||
		work.localRepo.RemoteIdentity == nil || reply.Fingerprint != work.localRepo.RemoteIdentity.Fingerprint {
		return permanentPeerBackupError{errors.New("member S3 backup capability identity changed")}
	}
	return nil
}

func preparePeerSFTPRepository(ctx context.Context, s *Store, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	if !validPeerSFTPIdentity(repo) {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID == s.State.HostID {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	self, ok := poolMemberByID(membership, s.State.HostID)
	if !ok || self.Status != "ready" {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	current, err := GetPoolBackupRepository(s, repo.ID)
	if err == nil && (current.Generation > repo.Generation || current.Generation == repo.Generation &&
		(current.Kind != repo.Kind || current.CredentialRef != repo.CredentialRef || !reflect.DeepEqual(current.SFTPSource, repo.SFTPSource) ||
			!reflect.DeepEqual(current.RemoteIdentity, repo.RemoteIdentity))) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	if err != nil && !errors.Is(err, ErrBackupRepoNotFound) {
		return protocol.BackupRepository{}, err
	}
	prepared, err := prepareBackupRepositoryForAdoption(ctx, s, repo)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	if !preparedBackupRepositoryMatches(repo, prepared) || !reflect.DeepEqual(prepared.RemoteIdentity, repo.RemoteIdentity) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	return prepared, nil
}

func preflightPeerSFTPRepository(ctx context.Context, work pendingPeerBackup) error {
	if err := requirePoolAuthorityWritable(work.store); err != nil {
		return permanentPeerBackupError{ErrBackupUnavailable}
	}
	current, err := GetPoolBackupRepository(work.store, work.localRepo.ID)
	if err != nil || current.Generation != work.localRepo.Generation || current.CredentialRef != work.localRepo.CredentialRef ||
		!reflect.DeepEqual(current.SFTPSource, work.localRepo.SFTPSource) || !reflect.DeepEqual(current.RemoteIdentity, work.localRepo.RemoteIdentity) {
		return permanentPeerBackupError{ErrBackupConflict}
	}
	body, err := json.Marshal(work.repo)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(work.member.PeerOrigin, "/")+poolBackupPeerPath+"/capability/sftp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := work.client.Do(request)
	if err != nil {
		return errors.New("member SFTP backup capability unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusBadRequest ||
		response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return permanentPeerBackupError{errors.New("member SFTP backup capability is unsupported or refused")}
	}
	if response.StatusCode != http.StatusOK {
		return errors.New("member SFTP backup capability unavailable")
	}
	var reply peerSFTPCapabilityResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&reply) != nil || reply.Generation != work.repo.Generation || reply.CredentialRef != work.repo.CredentialRef ||
		work.localRepo.RemoteIdentity == nil || reply.Fingerprint != work.localRepo.RemoteIdentity.Fingerprint {
		return permanentPeerBackupError{errors.New("member SFTP backup capability identity changed")}
	}
	return nil
}

func validPeerSMBIdentity(repo protocol.BackupRepository, identity *protocol.SMBStorageIdentity) bool {
	if repo.Kind != protocol.BackupRepoKindSMB3 || repo.Valid() != nil || repo.Generation == 0 || repo.CredentialRef == "" ||
		repo.SMBSource == nil || repo.MountPath != "" || repo.SMBIdentity != nil || identity == nil ||
		identity.Server != repo.SMBSource.Server || identity.Share != repo.SMBSource.Share || len(identity.MarkerID) != 32 {
		return false
	}
	_, err := hex.DecodeString(identity.MarkerID)
	return err == nil && strings.ToLower(identity.MarkerID) == identity.MarkerID
}

func samePeerSMBIdentity(a, b *protocol.SMBStorageIdentity) bool {
	return a != nil && b != nil && a.Server == b.Server && a.Share == b.Share && a.MarkerID == b.MarkerID
}

func preparePeerSMBRepository(ctx context.Context, s *Store, repo protocol.BackupRepository, identity *protocol.SMBStorageIdentity) (protocol.BackupRepository, error) {
	if !validPeerSMBIdentity(repo, identity) {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID == s.State.HostID {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	self, ok := poolMemberByID(membership, s.State.HostID)
	if !ok || self.Status != "ready" {
		return protocol.BackupRepository{}, ErrBackupUnavailable
	}
	current, err := GetPoolBackupRepository(s, repo.ID)
	if err == nil && (current.Generation > repo.Generation || current.Generation == repo.Generation &&
		(current.Kind != repo.Kind || current.CredentialRef != repo.CredentialRef || !reflect.DeepEqual(current.SMBSource, repo.SMBSource) ||
			!samePeerSMBIdentity(identity, current.SMBIdentity))) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	if err != nil && !errors.Is(err, ErrBackupRepoNotFound) {
		return protocol.BackupRepository{}, err
	}
	prepared, err := prepareBackupRepositoryForAdoption(ctx, s, repo)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	if !preparedBackupRepositoryMatches(repo, prepared) || !samePeerSMBIdentity(identity, prepared.SMBIdentity) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	return prepared, nil
}

func preflightPeerSMBRepository(ctx context.Context, work pendingPeerBackup) error {
	if err := requirePoolAuthorityWritable(work.store); err != nil {
		return permanentPeerBackupError{ErrBackupUnavailable}
	}
	current, err := GetPoolBackupRepository(work.store, work.localRepo.ID)
	if err != nil || current.Generation != work.localRepo.Generation || current.CredentialRef != work.localRepo.CredentialRef ||
		!samePeerSMBIdentity(current.SMBIdentity, work.localRepo.SMBIdentity) ||
		!reflect.DeepEqual(current.SMBSource, work.localRepo.SMBSource) {
		return permanentPeerBackupError{ErrBackupConflict}
	}
	requestBody, err := json.Marshal(peerSMBCapabilityRequest{Repository: work.repo, ExpectedIdentity: *work.localRepo.SMBIdentity})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(work.member.PeerOrigin, "/")+poolBackupPeerPath+"/capability/smb3", bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := work.client.Do(request)
	if err != nil {
		return errors.New("member SMB backup capability unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusBadRequest ||
		response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return permanentPeerBackupError{errors.New("member SMB backup capability is unsupported or refused")}
	}
	if response.StatusCode != http.StatusOK {
		return errors.New("member SMB backup capability unavailable")
	}
	var reply peerSMBCapabilityResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&reply) != nil || reply.Generation != work.repo.Generation || reply.CredentialRef != work.repo.CredentialRef ||
		!samePeerSMBIdentity(&reply.Identity, work.localRepo.SMBIdentity) {
		return permanentPeerBackupError{errors.New("member SMB backup capability identity changed")}
	}
	return nil
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
	var status int
	result.reply, status, result.err = doPeerBackup(work.client, request)
	if status == http.StatusNotFound {
		result.err = permanentPeerBackupError{errors.New("member backup operation is absent; outcome needs manual reconciliation")}
		return result
	}
	if result.err == nil {
		result.err = validatePeerBackupReply(ctx, work, result.reply)
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
		if entry, ok := s.State.Journal[run.OperationID]; ok && entry.Command != nil &&
			(entry.Command.Kind == protocol.BackupKindBackupNow || entry.Command.Kind == protocol.BackupKindPolicyRunNow) {
			continue // The Controller-owned worker dispatches this run.
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
		if repo.Kind != protocol.BackupRepoKindNFS && repo.Kind != protocol.BackupRepoKindSMB3 && repo.Kind != protocol.BackupRepoKindSFTP && repo.Kind != protocol.BackupRepoKindS3 {
			return pendingPeerBackup{}, false, ErrBackupUnavailable
		}
		if repo.Kind == protocol.BackupRepoKindSMB3 && !validPeerSMBIdentity(portablePeerRepository(repo), repo.SMBIdentity) {
			return pendingPeerBackup{}, false, ErrBackupUnavailable
		}
		if repo.Kind == protocol.BackupRepoKindSFTP && !validPeerSFTPIdentity(portablePeerRepository(repo)) {
			return pendingPeerBackup{}, false, ErrBackupUnavailable
		}
		if repo.Kind == protocol.BackupRepoKindS3 && !validPeerS3Identity(portablePeerRepository(repo)) {
			return pendingPeerBackup{}, false, ErrBackupUnavailable
		}
		repo = portablePeerRepository(repo)
		client, err := poolTransferClient(s, member.PeerOrigin, member.HostID)
		if err != nil {
			return pendingPeerBackup{}, false, err
		}
		return pendingPeerBackup{
			store: s, run: run, member: member, repo: repo, localRepo: localRepo, client: client,
			command: protocol.Command{OperationID: run.OperationID, Kind: "backup-now", VM: &vm,
				Backup: &protocol.BackupNowRequest{RepositoryID: run.RepositoryID, Consistency: run.CaptureMode}},
		}, true, nil
	}
	return pendingPeerBackup{}, false, nil
}

func callPeerBackup(ctx context.Context, work pendingPeerBackup) peerBackupCompletion {
	defer work.client.CloseIdleConnections()
	result := peerBackupCompletion{run: work.run}
	if backupCredentialKind(work.repo.Kind) && (work.command.OperationID != work.run.OperationID || work.repo.ID != work.run.RepositoryID ||
		work.repo.PoolID != work.run.PoolID || work.repo.Generation != work.localRepo.Generation ||
		work.repo.CredentialRef != work.localRepo.CredentialRef) {
		result.err = permanentPeerBackupError{ErrBackupConflict}
		return result
	}
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
		result.err = validatePeerBackupReply(ctx, work, reply)
		return result
	}
	if status != http.StatusConflict && status != http.StatusNotFound {
		if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
			err = permanentPeerBackupError{err}
		}
		result.err = err
		return result
	}
	if backupCredentialKind(work.repo.Kind) {
		if err := deliverPeerBackupCredential(requestCtx, work.store, work.member, work.localRepo); err != nil {
			result.err = err
			return result
		}
	}
	if work.repo.Kind == protocol.BackupRepoKindSMB3 {
		if err := preflightPeerSMBRepository(requestCtx, work); err != nil {
			result.err = err
			return result
		}
	}
	if work.repo.Kind == protocol.BackupRepoKindSFTP {
		if err := preflightPeerSFTPRepository(requestCtx, work); err != nil {
			result.err = err
			return result
		}
	}
	if work.repo.Kind == protocol.BackupRepoKindS3 {
		if err := preflightPeerS3Repository(requestCtx, work); err != nil {
			result.err = err
			return result
		}
	}
	body, err := json.Marshal(poolBackupPeerRequest{Command: work.command, Repository: work.repo, CatalogPoint: work.point, SMBIdentity: work.localRepo.SMBIdentity})
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
		result.err = validatePeerBackupReply(ctx, work, result.reply)
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
	// ponytail: bound full artifact verification; retry if a large or slow destination exceeds this ceiling.
	verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return verifyPeerBackupPoint(verifyCtx, work, reply)
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
		backend, err := openBackupRepositoryStorage(work.store, work.localRepo)
		if err != nil {
			return peerBackupVerificationError(err, "member restore point failed master catalog verification")
		}
		manifest, digest, err := backend.ReadPoint(ctx, work.run.RecoveryPointID)
		if err != nil {
			return peerBackupVerificationError(err, "member restore point failed master catalog verification")
		}
		if vmhost.ValidateBackupFirmwareForRestore(manifest) != nil || digest != work.point.ManifestSHA256 {
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
	backend, err := openBackupRepositoryStorage(work.store, work.localRepo)
	if err != nil {
		return peerBackupVerificationError(err, "member backup point failed master repository verification")
	}
	manifest, digest, err := backend.ReadPoint(ctx, point.ID)
	if err != nil {
		return peerBackupVerificationError(err, "member backup point failed master repository verification")
	}
	if manifest.RecoveryPointID != point.ID || manifest.RepositoryID != point.RepositoryID ||
		manifest.PoolID != point.PoolID || digest != point.ManifestSHA256 || manifest.SourceVMID != point.SourceVMID ||
		manifest.Consistency != point.Consistency {
		return permanentPeerBackupError{errors.New("member backup point failed master repository verification")}
	}
	return nil
}

func peerBackupVerificationError(err error, message string) error {
	if errors.Is(err, ErrBackupConflict) || errors.Is(err, ErrBackupInvalid) {
		return permanentPeerBackupError{errors.New(message)}
	}
	return err
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
