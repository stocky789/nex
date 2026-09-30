// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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
	observe   bool // the run needs attention: read the member's outcome, never start it
}

type peerBackupCompletion struct {
	run   protocol.BackupRun
	reply poolBackupPeerResponse
	err   error
}

type permanentPeerBackupError struct{ error }

// peerBackupRetry spaces repeated observations of one member run. While the member
// keeps giving the same answer the spacing doubles up to peerBackupRetryMax. The cap stays
// short so a member that becomes able to serve the run again is noticed within minutes.
// A run the member never started settles failed once peerBackupStartLimit has passed,
// so a member that cannot serve a repository cannot hold it indefinitely.
const (
	peerBackupRetry       = 30 * time.Second
	peerBackupRetryMax    = 5 * time.Minute
	peerBackupStartLimit  = 10 * time.Minute
	peerBackupUnreachable = "member unreachable"
)

// peerBackoff holds each run's next observation time in memory. Losing it on
// restart only means one extra observation before it backs off again.
type peerBackoff map[string]peerBackoffEntry

type peerBackoffEntry struct {
	next   time.Time
	delay  time.Duration
	answer string
}

func (b peerBackoff) waiting(id string, now time.Time) bool { return now.Before(b[id].next) }

// observed schedules the next observation. An empty answer, or one that differs
// from the last, restarts the spacing at peerBackupRetry.
func (b peerBackoff) observed(id, answer string, now time.Time) {
	e := b[id]
	if answer == "" || answer != e.answer || e.delay == 0 {
		e.delay = peerBackupRetry
	} else {
		e.delay = min(2*e.delay, peerBackupRetryMax)
	}
	e.answer, e.next = answer, now.Add(e.delay)
	b[id] = e
}

// skipping adds the runs still waiting to the runs already skipped.
func (b peerBackoff) skipping(skip map[string]bool, now time.Time) map[string]bool {
	merged := map[string]bool{}
	for id := range skip {
		merged[id] = true
	}
	for id := range b {
		if b.waiting(id, now) {
			merged[id] = true
		}
	}
	return merged
}

// peerBackupAnswer classifies what the member gave for a run: "" when it answered
// normally, peerBackupUnreachable for a transport error (its text carries ports and
// timings, so it is never compared), else the member's own error text.
func peerBackupAnswer(c peerBackupCompletion) string {
	if c.err != nil {
		var permanent permanentPeerBackupError
		if !errors.As(c.err, &permanent) {
			return peerBackupUnreachable
		}
		return c.err.Error()
	}
	if c.reply.Result.Status == "needs_attention" {
		return c.reply.Result.Error
	}
	return ""
}

// errPeerBackupNotStarted marks a member that has no record of the operation. A
// member journals every operation before running it, so the run can settle failed.
var errPeerBackupNotStarted = errors.New("member never started this operation")

func peerBackupNotStarted(err error) bool {
	var permanent permanentPeerBackupError
	return errors.As(err, &permanent) && errors.Is(permanent.error, errPeerBackupNotStarted)
}

type peerBackupCredential struct {
	Repository      protocol.BackupRepository   `json:"repository"`
	Receipt         backupCredentialReceipt     `json:"receipt"`
	RecipientHostID string                      `json:"recipient_host_id"`
	Credentials     protocol.StorageCredentials `json:"credentials"`
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
	if !backupCredentialKind(repo.Kind) || repo.PoolID != s.State.PoolID {
		return empty, ErrBackupInvalid
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	dir, err := backupCredentialDir(s)
	if err != nil {
		return empty, err
	}
	files, _, err := readBackupCredentials(s, dir)
	if err != nil {
		return empty, err
	}
	var projection peerBackupCredential
	for _, file := range files {
		stored := file.stored
		r := stored.Receipt
		if r.Reference != repo.CredentialRef {
			continue
		}
		if r.PoolID != repo.PoolID || r.HostID != s.State.HostID || r.RepositoryID != repo.ID || r.Kind != repo.Kind || !r.RetiredAt.IsZero() {
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
	if repo.Valid() != nil || !backupCredentialKind(repo.Kind) || repo.PoolID != s.State.PoolID ||
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
	if err == nil && (current.Kind != repo.Kind || repo.Kind == protocol.BackupRepoKindS3 &&
		(!reflect.DeepEqual(current.S3Source, repo.S3Source) || !reflect.DeepEqual(current.RemoteIdentity, repo.RemoteIdentity))) {
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
	path, err := backupCredentialPath(s, local.RequestID)
	if err != nil {
		return err
	}
	if raw, err := readClusterSecret(path); err == nil {
		existing, err := openBackupCredential(s, raw)
		if err != nil || existing.Receipt != local || existing.Credentials != projection.Credentials {
			return ErrBackupConflict
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, err := sealBackupCredential(s, storedBackupCredential{Receipt: local, Credentials: projection.Credentials})
	if err != nil {
		return err
	}
	return writePrivate(path, raw, 0600)
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
		if errors.Is(err, ErrBackupUnavailable) {
			http.Error(w, "backup credentials unavailable", http.StatusConflict)
			return
		}
		if err != nil {
			// One unreadable receipt must not block the join; dispatch delivers per repository.
			log.Printf("backup credential for repository %s cannot be exported: %v", repo.ID, err)
			continue
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
	// Up to 256 repositories, each within the 128 KiB single-credential delivery bound.
	decoder := json.NewDecoder(io.LimitReader(response.Body, 256*128<<10))
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
		if permanentPeerStatus(response.StatusCode) {
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

// permanentPeerStatus reports whether a member's refusal will not change on retry.
func permanentPeerStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	}
	return false
}

// repo.Valid checks the remote identity fingerprint format.
func validPeerRemoteIdentity(repo protocol.BackupRepository) bool {
	return repo.Valid() == nil && repo.CredentialRef != "" && repo.MountPath == "" && repo.RemoteIdentity != nil
}

func validPeerSFTPIdentity(repo protocol.BackupRepository) bool {
	return repo.Kind == protocol.BackupRepoKindSFTP && repo.SFTPSource != nil && validPeerRemoteIdentity(repo)
}

func validPeerS3Identity(repo protocol.BackupRepository) bool {
	return repo.Kind == protocol.BackupRepoKindS3 && repo.S3Source != nil && validPeerRemoteIdentity(repo)
}

// samePeerRepository reports whether two records name the same remote destination and identity.
func samePeerRepository(a, b protocol.BackupRepository) bool {
	return a.Kind == b.Kind && a.CredentialRef == b.CredentialRef && reflect.DeepEqual(a.S3Source, b.S3Source) &&
		reflect.DeepEqual(a.SFTPSource, b.SFTPSource) && reflect.DeepEqual(a.SMBSource, b.SMBSource) &&
		reflect.DeepEqual(a.RemoteIdentity, b.RemoteIdentity) &&
		(a.SMBIdentity == nil && b.SMBIdentity == nil || samePeerSMBIdentity(a.SMBIdentity, b.SMBIdentity))
}

// preparePeerRepository adopts a credentialed destination the pool master shares
// with this member; identity is the SMB share's, nil for other kinds.
func preparePeerRepository(ctx context.Context, s *Store, repo protocol.BackupRepository, identity *protocol.SMBStorageIdentity) (protocol.BackupRepository, error) {
	valid := false
	switch repo.Kind {
	case protocol.BackupRepoKindS3:
		valid = validPeerS3Identity(repo) && identity == nil
	case protocol.BackupRepoKindSFTP:
		valid = validPeerSFTPIdentity(repo) && identity == nil
	case protocol.BackupRepoKindSMB3:
		valid = validPeerSMBIdentity(repo, identity)
	}
	if !valid {
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
	adopt := repo
	adopt.SMBIdentity = identity
	current, err := GetPoolBackupRepository(s, repo.ID)
	if err == nil && !samePeerRepository(current, adopt) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	if err != nil && !errors.Is(err, ErrBackupRepoNotFound) {
		return protocol.BackupRepository{}, err
	}
	prepared, err := prepareBackupRepositoryForAdoption(ctx, s, adopt)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	if !preparedBackupRepositoryMatches(repo, prepared) || !samePeerRepository(adopt, prepared) {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	return prepared, nil
}

// preflightPeerSource refuses a run whose destination changed since it was reserved.
func preflightPeerSource(work pendingPeerBackup) error {
	if err := requirePoolAuthorityWritable(work.store); err != nil {
		return permanentPeerBackupError{ErrBackupUnavailable}
	}
	current, err := GetPoolBackupRepository(work.store, work.localRepo.ID)
	if err != nil || !samePeerRepository(current, work.localRepo) {
		return permanentPeerBackupError{ErrBackupConflict}
	}
	return nil
}

// postPeerCapability asks the member to prepare the destination and decodes its reply.
func postPeerCapability(ctx context.Context, work pendingPeerBackup, name, route string, payload, reply any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(work.member.PeerOrigin, "/")+poolBackupPeerPath+route, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := work.client.Do(request)
	if err != nil {
		return fmt.Errorf("member %s backup capability unavailable", name)
	}
	defer response.Body.Close()
	if permanentPeerStatus(response.StatusCode) {
		return permanentPeerBackupError{fmt.Errorf("member %s backup capability is unsupported or refused", name)}
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("member %s backup capability unavailable", name)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(reply) != nil {
		return peerCapabilityChanged(name)
	}
	return nil
}

func peerCapabilityChanged(name string) error {
	return permanentPeerBackupError{fmt.Errorf("member %s backup capability identity changed", name)}
}

func preflightPeerS3Repository(ctx context.Context, work pendingPeerBackup) error {
	if err := preflightPeerSource(work); err != nil {
		return err
	}
	var reply peerS3CapabilityResponse
	if err := postPeerCapability(ctx, work, "S3", "/capability/s3", work.repo, &reply); err != nil {
		return err
	}
	if reply.CredentialRef != work.repo.CredentialRef || work.localRepo.RemoteIdentity == nil ||
		reply.Fingerprint != work.localRepo.RemoteIdentity.Fingerprint {
		return peerCapabilityChanged("S3")
	}
	return nil
}

func preflightPeerSFTPRepository(ctx context.Context, work pendingPeerBackup) error {
	if err := preflightPeerSource(work); err != nil {
		return err
	}
	var reply peerSFTPCapabilityResponse
	if err := postPeerCapability(ctx, work, "SFTP", "/capability/sftp", work.repo, &reply); err != nil {
		return err
	}
	if reply.CredentialRef != work.repo.CredentialRef || work.localRepo.RemoteIdentity == nil ||
		reply.Fingerprint != work.localRepo.RemoteIdentity.Fingerprint {
		return peerCapabilityChanged("SFTP")
	}
	return nil
}

func validPeerSMBIdentity(repo protocol.BackupRepository, identity *protocol.SMBStorageIdentity) bool {
	if repo.Kind != protocol.BackupRepoKindSMB3 || repo.Valid() != nil || repo.CredentialRef == "" ||
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

func preflightPeerSMBRepository(ctx context.Context, work pendingPeerBackup) error {
	if err := preflightPeerSource(work); err != nil {
		return err
	}
	var reply peerSMBCapabilityResponse
	if err := postPeerCapability(ctx, work, "SMB", "/capability/smb3", peerSMBCapabilityRequest{Repository: work.repo, ExpectedIdentity: *work.localRepo.SMBIdentity}, &reply); err != nil {
		return err
	}
	if reply.CredentialRef != work.repo.CredentialRef || !samePeerSMBIdentity(&reply.Identity, work.localRepo.SMBIdentity) {
		return peerCapabilityChanged("SMB")
	}
	return nil
}

func waitPeerBackupTerminal(ctx context.Context, work pendingPeerBackup) peerBackupCompletion {
	for {
		result := callPeerBackup(ctx, work)
		// Any error returns: the run stays pending and the Run loop redispatches it after
		// its backoff. Waiting here would hold the pool's one backup slot for other repositories.
		if status := result.reply.Result.Status; result.err != nil || status == "succeeded" || status == "failed" || status == "needs_attention" {
			return result
		}
		select {
		case <-ctx.Done():
			return peerBackupCompletion{run: work.run, err: ctx.Err()}
		case <-time.After(2 * time.Second):
		}
	}
}

// nextPendingPeerBackup selects one durable member occurrence per pass. A failed
// peer call leaves the reservation pending for the next pass or a restarted master.
// A member run that needs attention and has no local journal (a scheduled run, or one
// inherited by promotion) is re-observed until the member settles it.
func nextPendingPeerBackup(s *Store, notBefore peerBackoff, now time.Time) (pendingPeerBackup, bool) {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return pendingPeerBackup{}, false
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		log.Printf("member backup runs are unavailable: %v", err)
		return pendingPeerBackup{}, false
	}
	for _, run := range runs {
		if notBefore.waiting(run.OperationID, now) {
			continue
		}
		scheduled := run.Kind == protocol.BackupKindScheduled || run.Kind == protocol.BackupKindPolicyRunNow
		if !(run.Status == "pending" && scheduled ||
			run.Status == "needs_attention" && (scheduled || run.Kind == protocol.BackupKindBackupNow)) {
			delete(notBefore, run.OperationID)
			continue
		}
		if entry, ok := s.State.Journal[run.OperationID]; ok && entry.Command != nil &&
			(entry.Command.Kind == protocol.BackupKindBackupNow || entry.Command.Kind == protocol.BackupKindPolicyRunNow) {
			continue // The Controller-owned worker dispatches and reconciles this run.
		}
		cmd := protocol.Command{OperationID: run.OperationID, Kind: protocol.BackupKindBackupNow,
			Backup: &protocol.BackupNowRequest{RepositoryID: run.RepositoryID, Consistency: run.CaptureMode, PolicyID: run.PolicyID}}
		if vm, err := lookupPoolVMSpec(s, run.VMID); err == nil {
			cmd.VM = &vm
		}
		work, err := peerBackupWorkForCommand(s, cmd, run)
		if err != nil {
			if run.HostID != "" && run.HostID != s.State.HostID {
				log.Printf("member backup %s cannot be dispatched yet: %v", run.OperationID, err)
			}
			notBefore.observed(run.OperationID, err.Error(), now)
			continue
		}
		work.observe = run.Status == "needs_attention"
		return work, true
	}
	return pendingPeerBackup{}, false
}

// settlePeerBackupCompletion records a scheduled or inherited member run's observed
// outcome on the master. A member with no record of the run never started it.
func settlePeerBackupCompletion(ctx context.Context, s *Store, completion peerBackupCompletion) {
	if peerBackupNotStarted(completion.err) {
		completion.reply = poolBackupPeerResponse{Result: protocol.Result{Status: "failed", Error: completion.err.Error()}}
		completion.err = nil
	}
	if completion.err != nil {
		var permanent permanentPeerBackupError
		if !errors.As(completion.err, &permanent) {
			log.Printf("member backup occurrence remains unsettled: %v", completion.err)
		} else if completion.run.Status != "needs_attention" {
			// Recorded on the run once; re-observing it does not log again.
			run := completion.run
			run.Status, run.Stage, run.Error = "needs_attention", "peer_verification", completion.err.Error()
			if settleErr := settlePoolBackupRun(ctx, s, run, nil); settleErr != nil {
				log.Printf("member backup occurrence remains pending: %v", settleErr)
			}
		}
		return
	}
	status := completion.reply.Result.Status
	if status != "succeeded" && status != "failed" && (status != "needs_attention" || completion.run.Status == status) {
		return
	}
	run := completion.run
	run.Status, run.Error = status, completion.reply.Result.Error
	if completion.reply.Run != nil {
		run.Stage = completion.reply.Run.Stage
	} else {
		run.Stage = status
	}
	if err := settlePoolBackupRun(ctx, s, run, completion.reply.Point); err != nil {
		log.Printf("member backup occurrence remains pending: %v", err)
	}
}

// callPeerBackup reads the member's record of the run and, unless work.observe is
// set, starts the operation when the member has none.
func callPeerBackup(ctx context.Context, work pendingPeerBackup) peerBackupCompletion {
	defer work.client.CloseIdleConnections()
	result := peerBackupCompletion{run: work.run}
	if backupCredentialKind(work.repo.Kind) && (work.command.OperationID != work.run.OperationID || work.repo.ID != work.run.RepositoryID ||
		work.repo.PoolID != work.run.PoolID || work.repo.CredentialRef != work.localRepo.CredentialRef) {
		result.err = permanentPeerBackupError{ErrBackupConflict}
		return result
	}
	getCtx, cancelGet := context.WithTimeout(ctx, 10*time.Second)
	defer cancelGet()
	base := strings.TrimRight(work.member.PeerOrigin, "/") + poolBackupPeerPath
	get, err := http.NewRequestWithContext(getCtx, http.MethodGet, base+"/"+work.run.OperationID, nil)
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
	if status != http.StatusConflict && status != http.StatusNotFound || work.observe {
		if status == http.StatusNotFound {
			err = permanentPeerBackupError{errPeerBackupNotStarted}
		} else if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
			err = permanentPeerBackupError{err}
		}
		result.err = err
		return result
	}
	// The member journals an operation before it runs, so after a 404 a refusal here
	// means nothing ran and the reservation can settle failed.
	notStarted := func(err error) error {
		var permanent permanentPeerBackupError
		if status == http.StatusNotFound && errors.As(err, &permanent) {
			return permanentPeerBackupError{fmt.Errorf("%w: %v", errPeerBackupNotStarted, permanent.error)}
		}
		// Only failures before the start request reach here as retryable errors.
		if status == http.StatusNotFound && time.Since(work.run.CreatedAt) > peerBackupStartLimit {
			return permanentPeerBackupError{fmt.Errorf("%w for %v: %v", errPeerBackupNotStarted, peerBackupStartLimit, err)}
		}
		return err
	}
	if backupCommandTargetHost(work.command) != work.member.HostID {
		result.err = notStarted(permanentPeerBackupError{errors.New("the VM moved off the reserved host before its backup started")})
		return result
	}
	// The member prepares the destination for the preflight and again for the backup.
	requestCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if backupCredentialKind(work.repo.Kind) {
		if err := deliverPeerBackupCredential(requestCtx, work.store, work.member, work.localRepo); err != nil {
			result.err = notStarted(err)
			return result
		}
	}
	switch work.repo.Kind {
	case protocol.BackupRepoKindSMB3:
		err = preflightPeerSMBRepository(requestCtx, work)
	case protocol.BackupRepoKindSFTP:
		err = preflightPeerSFTPRepository(requestCtx, work)
	case protocol.BackupRepoKindS3:
		err = preflightPeerS3Repository(requestCtx, work)
	default:
		err = nil
	}
	if err != nil {
		result.err = notStarted(err)
		return result
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
	var postStatus int
	result.reply, postStatus, result.err = doPeerBackup(work.client, post)
	if result.err != nil && permanentPeerStatus(postStatus) {
		result.err = notStarted(permanentPeerBackupError{result.err})
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
	// A member journal that needs attention is authoritative while its run record settles.
	if reply.Run.Status != reply.Result.Status && reply.Result.Status != "needs_attention" {
		if reply.Result.Status == "pending" &&
			(reply.Run.Status == "succeeded" || reply.Run.Status == "failed" || reply.Run.Status == "needs_attention") {
			return errors.New("member backup result is settling")
		}
		return permanentPeerBackupError{errors.New("member backup peer returned inconsistent status")}
	}
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
		const message = "member restore point failed master catalog verification"
		backend, err := openBackupRepositoryStorage(work.store, work.localRepo)
		if err != nil {
			return peerBackupVerificationError(ctx, nil, err, message)
		}
		manifest, digest, err := backend.StatPoint(ctx, work.run.RecoveryPointID)
		if err != nil {
			return peerBackupVerificationError(ctx, backend, err, message)
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
	const message = "member backup point failed master repository verification"
	backend, err := openBackupRepositoryStorage(work.store, work.localRepo)
	if err != nil {
		return peerBackupVerificationError(ctx, nil, err, message)
	}
	manifest, digest, err := backend.StatPoint(ctx, point.ID)
	if err != nil {
		return peerBackupVerificationError(ctx, backend, err, message)
	}
	if manifest.RecoveryPointID != point.ID || manifest.RepositoryID != point.RepositoryID ||
		manifest.PoolID != point.PoolID || digest != point.ManifestSHA256 || manifest.SourceVMID != point.SourceVMID ||
		manifest.Consistency != point.Consistency {
		return permanentPeerBackupError{errors.New("member backup point failed master repository verification")}
	}
	return nil
}

// peerBackupVerificationError treats a point the master cannot find or read back in a
// reachable repository as a content failure. Only an unreachable repository is retried.
// The member only publishes a manifest after its artifacts verify, so the master's
// check reads the manifest and artifact sizes, never the artifact content.
func peerBackupVerificationError(ctx context.Context, backend backupRepositoryStorage, err error, message string) error {
	if errors.Is(err, ErrBackupConflict) || errors.Is(err, ErrBackupInvalid) ||
		backend != nil && ctx.Err() == nil && backend.Verify(ctx) == nil {
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
		reason, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return poolBackupPeerResponse{}, response.StatusCode, fmt.Errorf("member backup peer refused the request: %s", bytes.TrimSpace(reason))
	}
	var reply poolBackupPeerResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&reply); err != nil {
		return poolBackupPeerResponse{}, response.StatusCode, errors.New("member backup peer returned an invalid result")
	}
	return reply, response.StatusCode, nil
}
