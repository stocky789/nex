// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// backupRefusal is a refusal the operator can act on; its text is the command's failure.
type backupRefusal string

func (e backupRefusal) Error() string { return string(e) }

type peerForgetRepository struct {
	Repository protocol.BackupRepository `json:"repository"`
}

type peerRetainRepositories struct {
	Repositories []protocol.BackupRepository `json:"repositories"`
}

const (
	peerRepositoryPageLimit = 256
	peerRepositoryBodyLimit = 4 << 20
)

type peerRepositorySnapshots struct {
	Repositories []protocol.BackupRepository `json:"repositories"`
	NextAfterID  peerRepositoryCursor        `json:"next_after_id,omitempty"`
}

type peerRepositoryCursor string

func (cursor *peerRepositoryCursor) UnmarshalJSON(raw []byte) error {
	var id string
	if json.Unmarshal(raw, &id) != nil || !protocol.ValidSnapshotOperationID(id) {
		return ErrBackupInvalid
	}
	*cursor = peerRepositoryCursor(id)
	return nil
}

func decodePeerRepositoryBody(reader io.Reader, limit int64, body any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return ErrBackupInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		return ErrBackupInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrBackupInvalid
	}
	return nil
}

type backupRepositoryMutation struct{ key string }

var backupRepositoryMutations = map[string]*backupRepositoryMutation{} // guarded by poolBackupAuthorityMu

// The caller holds poolBackupAuthorityMu.
func backupRepositoryMutationBusy(s *Store, id string, owner *backupRepositoryMutation) bool {
	if s == nil {
		return false
	}
	key := s.dir + "\x00" + id
	current := backupRepositoryMutations[key]
	if owner != nil {
		return owner.key != key || current != owner
	}
	return current != nil
}

// The caller holds poolBackupAuthorityMu.
func reserveBackupRepositoryMutation(s *Store, id string) (*backupRepositoryMutation, error) {
	key := s.dir + "\x00" + id
	if backupRepositoryMutations[key] != nil {
		return nil, ErrBackupConflict
	}
	owner := &backupRepositoryMutation{key: key}
	backupRepositoryMutations[key] = owner
	return owner, nil
}

func (owner *backupRepositoryMutation) releaseLocked() {
	if backupRepositoryMutations[owner.key] == owner {
		delete(backupRepositoryMutations, owner.key)
	}
}

func (owner *backupRepositoryMutation) release() {
	poolBackupAuthorityMu.Lock()
	owner.releaseLocked()
	poolBackupAuthorityMu.Unlock()
}

// forgetPoolBackupRepository drops the repository's record, recovery points and run
// history on the master, then its mount and sealed credential on every host. It never
// touches the destination, so registering the same destination again and rebuilding the
// catalog brings the recovery points back. An unknown repository is already forgotten.
func forgetPoolBackupRepository(ctx context.Context, s *Store, req protocol.ForgetRepositoryRequest) error {
	if _, _, err := dropPoolBackupRepositoryRecords(ctx, s, req); err != nil {
		return err
	}
	forgetBackupRepositoryOnMembers(ctx, s, req.RepositoryID)
	return nil
}

// backupForgetScope is what forgetting a repository removes from the master, and the first
// reason it may not proceed.
type backupForgetScope struct {
	points    []protocol.BackupRecoveryPoint
	runs      []protocol.BackupRun
	protected int
	blocker   string
}

// scopeBackupForget reads the jobs, runs, pins and recovery points that name repo. The
// caller holds poolBackupAuthorityMu.
func scopeBackupForget(s *Store, repo protocol.BackupRepository) (backupForgetScope, error) {
	var scope backupForgetScope
	block := func(reason string) {
		if scope.blocker == "" {
			scope.blocker = reason
		}
	}
	policies, err := loadPoolBackupPolicies(s)
	if err != nil {
		return scope, err
	}
	jobs := 0
	for _, policy := range policies.Policies {
		if policy.RepositoryID == repo.ID {
			jobs++
		}
	}
	if jobs > 0 {
		block(fmt.Sprintf("%d backup jobs use this repository; change their repository first", jobs))
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return scope, err
	}
	for _, run := range runs {
		if run.RepositoryID != repo.ID {
			continue
		}
		// Missed, skipped and settled runs are history that goes with the repository; only a
		// run that may still touch it blocks.
		switch run.Status {
		case "pending":
			block("a backup, restore or test restore is running on this repository; wait for it to finish")
		case "needs_attention":
			block(fmt.Sprintf("operation %s on this repository needs attention (its outcome is uncertain); resolve it first", run.OperationID))
		}
		scope.runs = append(scope.runs, run)
	}
	catalog, err := loadPoolBackupCatalog(s)
	if err != nil {
		return scope, err
	}
	pins, err := loadPoolBackupPins(s)
	if err != nil {
		return scope, err
	}
	for _, point := range catalog {
		if point.RepositoryID != repo.ID {
			continue
		}
		for _, pin := range pins.Pins {
			if pin.RecoveryPointID == point.ID {
				block(pinRefusal(s, pin))
			}
		}
		if poolBackupPruning[point.ID] {
			block("retention is deleting recovery points from this repository; try again shortly")
		}
		if point.Protected {
			scope.protected++
		}
		scope.points = append(scope.points, point)
	}
	return scope, nil
}

func pendingBackupForgetPath(s *Store, id string) string {
	return filepath.Join(poolBackupStoreDir(s), "pool-backup-pending-forgets", id+".json")
}

// backupForgetPending reports a forget that passed its checks and began removing records;
// a retry finishes it instead of checking the counts again.
func backupForgetPending(s *Store, id string) bool {
	_, err := os.Stat(pendingBackupForgetPath(s, id))
	return err == nil
}

// backupRepositoryRemoving reports a repository that is being changed or forgotten, so new
// runs and jobs must not start on it. The caller holds poolBackupAuthorityMu.
func backupRepositoryRemoving(s *Store, id string) bool {
	return backupRepositoryMutationBusy(s, id, nil) || backupForgetPending(s, id)
}

func rememberPendingBackupForget(s *Store, id string) error {
	if err := os.MkdirAll(filepath.Dir(pendingBackupForgetPath(s, id)), 0700); err != nil {
		return err
	}
	return vmhost.AtomicWrite(pendingBackupForgetPath(s, id), []byte("{}"), 0600)
}

func dropPoolBackupRepositoryRecords(ctx context.Context, s *Store, req protocol.ForgetRepositoryRequest) (protocol.BackupRepository, bool, error) {
	// An orphan cleanup holds this for its whole run; forgetting under it would let it
	// report points deleted that the destination still holds.
	if !poolBackupRetentionMu.TryLock() {
		return protocol.BackupRepository{}, false, backupRefusal("an orphaned backup cleanup or retention pass is running on this pool; wait for it to finish")
	}
	defer poolBackupRetentionMu.Unlock()
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return protocol.BackupRepository{}, false, err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return protocol.BackupRepository{}, false, err
	}
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return protocol.BackupRepository{}, false, err
	}
	mutation, err := reserveBackupRepositoryMutation(s, req.RepositoryID)
	if err != nil {
		return protocol.BackupRepository{}, false, err
	}
	defer mutation.releaseLocked()
	repo, ok := repos.Repositories[req.RepositoryID]
	if !ok {
		// Record already gone: still finish a pending detach left by a crash mid-forget.
		poolBackupAuthorityMu.Unlock()
		err := finishPendingBackupDetach(ctx, s, req.RepositoryID)
		poolBackupAuthorityMu.Lock()
		if err == nil {
			err = removeIfPresent(pendingBackupForgetPath(s, req.RepositoryID))
		}
		return protocol.BackupRepository{}, false, err
	}
	if backupRepositoryPointMetadataBusy(s, repo.ID) {
		return repo, true, backupRefusal("a recovery point metadata operation is running on this repository; wait for it to finish")
	}
	if req.Confirmation != repo.Name {
		return repo, true, backupRefusal(fmt.Sprintf("type the repository name %q to confirm", repo.Name))
	}
	scope, err := scopeBackupForget(s, repo)
	if err != nil {
		return repo, true, err
	}
	if !backupForgetPending(s, repo.ID) {
		if scope.blocker != "" {
			return repo, true, backupRefusal(scope.blocker)
		}
		if len(scope.points) != req.RecoveryPoints || scope.protected != req.ProtectedPoints {
			return repo, true, backupRefusal(fmt.Sprintf("this repository now holds %d recovery points (%d protected), not the %d (%d protected) confirmed; review and confirm again",
				len(scope.points), scope.protected, req.RecoveryPoints, req.ProtectedPoints))
		}
		// Detach first and remember the mount: a failed unmount leaves the record, and a crash
		// after the record is gone still retries from the pending detach.
		// Journal admission stays on the Store owner; repository I/O does not hold authority.
		if err := rememberPendingBackupDetach(s, repo); err != nil {
			return repo, true, err
		}
		poolBackupAuthorityMu.Unlock()
		detachErr := detachBackupRepositoryHost(ctx, repo)
		poolBackupAuthorityMu.Lock()
		if detachErr != nil {
			return repo, true, fmt.Errorf("the mount of %q could not be removed on this host; try again: %w", repo.Name, detachErr)
		}
		repos, err = loadPoolBackupRepositories(s)
		if err != nil {
			return repo, true, err
		}
		if current, ok := repos.Repositories[repo.ID]; !ok || !sameBackupRepositoryRegistration(current, repo) {
			return repo, true, ErrBackupConflict
		}
		// The lock was open during the unmount: jobs, pins, runs and imported points may
		// have arrived, and the operator confirmed only what they saw.
		settled, err := scopeBackupForget(s, repo)
		if err != nil {
			return repo, true, err
		}
		if settled.blocker != "" {
			return repo, true, backupRefusal(settled.blocker)
		}
		if len(settled.points) != len(scope.points) || settled.protected != scope.protected {
			return repo, true, backupRefusal("this repository's recovery points changed while its mount was removed; review and confirm again")
		}
		scope = settled
		if err := rememberPendingBackupForget(s, repo.ID); err != nil {
			return repo, true, err
		}
	}
	if err := retireBackupRepositoryCredential(s, repo); err != nil {
		return repo, true, err
	}
	for _, point := range scope.points {
		if err := removeIfPresent(filepath.Join(poolBackupCatalogPath(s), point.ID+".json")); err != nil {
			return repo, true, err
		}
	}
	for _, run := range scope.runs {
		if err := removeIfPresent(filepath.Join(poolBackupRunsPath(s), run.OperationID+".json")); err != nil {
			return repo, true, err
		}
	}
	delete(repos.Repositories, repo.ID)
	if err := savePoolBackupRepositories(s, repos); err != nil {
		return repo, true, err
	}
	if err := clearPendingBackupDetach(s, repo.ID); err != nil {
		return repo, true, err
	}
	return repo, true, removeIfPresent(pendingBackupForgetPath(s, repo.ID))
}

// pinRefusal names the operation, and the test VM when there is one, that holds a recovery
// point so the operator can find and finish or clean it up.
func pinRefusal(s *Store, pin protocol.BackupRestorePin) string {
	holder := "operation " + pin.OperationID
	if run, err := loadPoolBackupRun(s, pin.OperationID); err == nil && run.NewVMID != "" {
		holder += ", test VM " + run.NewVMID
		if vms, err := loadPoolVMs(s); err == nil && vms.VMs[run.NewVMID].Spec.Name != "" {
			holder += " (" + vms.VMs[run.NewVMID].Spec.Name + ")"
		}
	}
	return "a recovery point of this repository is in use by an unfinished restore or test restore (" + holder + "); finish or clean it up first"
}

// A variable so a test can interrupt a forget part-way; a directory permission would not stop root.
var removeIfPresent = func(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// retireBackupRepositoryCredential drops the sealed secret and leaves the tombstone that
// answers an exact replay of the registration.
func retireBackupRepositoryCredential(s *Store, repo protocol.BackupRepository) error {
	if !backupCredentialKind(repo.Kind) {
		return nil
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	file, err := findBackupCredential(s, repo.CredentialRef)
	if errors.Is(err, errBackupCredentialKeyMissing) {
		return dropUnreadableBackupCredential(s, repo.CredentialRef)
	}
	if file == nil || err != nil || !file.stored.Receipt.RetiredAt.IsZero() {
		return err
	}
	return retireBackupCredentialFile(s, file.path, file.stored)
}

// dropUnreadableBackupCredential is the operator's way out of a lost credential key: the
// receipt can be neither opened nor retired, so forgetting the repository removes it. The
// reference is readable without the key.
func dropUnreadableBackupCredential(s *Store, reference string) error {
	dir, err := backupCredentialDir(s)
	if err != nil {
		return err
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, path := range paths {
		raw, err := readClusterSecret(path)
		var sealed sealedBackupCredential
		if err != nil || json.Unmarshal(raw, &sealed) != nil || sealed.Reference != reference {
			continue
		}
		if err := removeBackupSMBCredentialNative(reference); err != nil {
			return err
		}
		return os.Remove(path)
	}
	return nil
}

// detachBackupRepositoryHost is bounded because a dead file server can hold an unmount.
func detachBackupRepositoryHost(ctx context.Context, repo protocol.BackupRepository) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return detachBackupRepositoryNative(ctx, repo)
}

func backupRepositoryNeedsDetach(repo protocol.BackupRepository) bool {
	return repo.Kind == protocol.BackupRepoKindNFS || repo.Kind == protocol.BackupRepoKindSMB3
}

func pendingBackupDetachPath(s *Store, id string) string {
	return filepath.Join(poolBackupStoreDir(s), "pool-backup-pending-detaches", id+".json")
}

// rememberPendingBackupDetach stores the mount identity before the repository record is
// deleted, so a crash mid-forget can still unmount on retry.
func rememberPendingBackupDetach(s *Store, repo protocol.BackupRepository) error {
	if !backupRepositoryNeedsDetach(repo) {
		return nil
	}
	raw, err := json.Marshal(repo)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pendingBackupDetachPath(s, repo.ID)), 0700); err != nil {
		return err
	}
	return vmhost.AtomicWrite(pendingBackupDetachPath(s, repo.ID), raw, 0600)
}

func clearPendingBackupDetach(s *Store, id string) error {
	return removeIfPresent(pendingBackupDetachPath(s, id))
}

func finishPendingBackupDetach(ctx context.Context, s *Store, id string) error {
	raw, err := os.ReadFile(pendingBackupDetachPath(s, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var repo protocol.BackupRepository
	if json.Unmarshal(raw, &repo) != nil || repo.ID != id {
		return errors.New("pending backup detach record is corrupt")
	}
	if err := detachBackupRepositoryHost(ctx, repo); err != nil {
		return fmt.Errorf("the mount of %q could not be removed on this host; try again: %w", repo.Name, err)
	}
	return clearPendingBackupDetach(s, id)
}

// memberJournalUsesBackupRepository reports an unsettled journal operation that still names
// the repository, including register/forget/orphan/catalog commands that
// backupCommandRepositoryID does not cover.
func memberJournalUsesBackupRepository(s *Store, id string) string {
	for opID, entry := range s.State.Journal {
		if entry.Command == nil || entry.Status == "succeeded" || entry.Status == "failed" {
			continue
		}
		cmd := *entry.Command
		if backupCommandRepositoryID(cmd) == id {
			return opID
		}
		switch {
		case cmd.BackupRepository != nil && cmd.BackupRepository.ID == id:
			return opID
		case cmd.ForgetRepository != nil && cmd.ForgetRepository.RepositoryID == id:
			return opID
		case cmd.OrphanCleanup != nil && cmd.OrphanCleanup.RepositoryID == id:
			return opID
		case cmd.CatalogImport != nil && cmd.CatalogImport.RepositoryID == id:
			return opID
		}
	}
	return ""
}

// Members that miss this keep an inert record and mount; the master repeats the sweep when
// asked to forget the same repository ID again.
func forgetBackupRepositoryOnMembers(ctx context.Context, s *Store, id string) {
	membership, err := Membership(s)
	if err != nil {
		return
	}
	for _, member := range membership.Members {
		if member.HostID == s.State.HostID || member.Status != "ready" || member.PeerOrigin == "" {
			continue
		}
		if err := deliverPeerForgetRepository(ctx, s, member, id); err != nil {
			log.Printf("member %s did not forget backup repository %s: %v", member.HostID, id, err)
		}
	}
}

func deliverPeerForgetRepository(ctx context.Context, s *Store, member protocol.PoolMember, id string) error {
	repos, err := getPeerBackupRepositorySnapshots(ctx, s, member)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if repo.ID == id {
			return forgetStalePeerRepository(ctx, s, member, repo, "/forget")
		}
	}
	return nil
}

func postPeerBackupRoute(ctx context.Context, s *Store, member protocol.PoolMember, route string, payload any) error {
	client, err := poolTransferClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	limit := peerRepositoryBodyLimit
	if route == "/forget" {
		limit = 64 << 10
	}
	if len(body) > limit {
		return ErrBackupInvalid
	}
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolBackupPeerPath+route, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("member unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("member refused with status %d", response.StatusCode)
	}
	return nil
}

// Sweeps delete only exact member records observed before verifying master absence.
// A registration adopted while a request is in flight no longer matches the record.
func retainBackupRepositoryOnMembers(ctx context.Context, s *Store, membership protocol.PoolMembership) {
	for _, member := range poolConfirmationPeers(membership, s.State.HostID) {
		if member.PeerOrigin == "" {
			continue
		}
		repos, err := getPeerBackupRepositorySnapshots(ctx, s, member)
		if err != nil {
			continue
		}
		for _, repo := range repos {
			if err := forgetStalePeerRepository(ctx, s, member, repo, "/retain"); err != nil {
				log.Printf("member %s did not drop stale backup repository %s: %v", member.HostID, repo.ID, err)
			}
		}
	}
}

func getPeerBackupRepositorySnapshots(ctx context.Context, s *Store, member protocol.PoolMember) ([]protocol.BackupRepository, error) {
	client, err := poolTransferClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(member.PeerOrigin, "/") + poolBackupPeerPath + "/retain"
	var repos []protocol.BackupRepository
	after := ""
	for {
		pageURL := endpoint
		if after != "" {
			pageURL += "?" + url.Values{"after_id": {after}}.Encode()
		}
		request, err := http.NewRequestWithContext(work, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, errors.New("member unreachable")
		}
		var body peerRepositorySnapshots
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("member refused with status %d", response.StatusCode)
		}
		err = decodePeerRepositoryBody(response.Body, peerRepositoryBodyLimit, &body)
		response.Body.Close()
		if err != nil || body.Repositories == nil || len(body.Repositories) > peerRepositoryPageLimit {
			return nil, ErrBackupInvalid
		}
		last := after
		for _, repo := range body.Repositories {
			if !validPeerForgetSnapshot(s, repo) || repo.ID <= last {
				return nil, ErrBackupInvalid
			}
			last = repo.ID
		}
		next := string(body.NextAfterID)
		if next != "" && (len(body.Repositories) == 0 || next != last || next <= after) {
			return nil, ErrBackupInvalid
		}
		repos = append(repos, body.Repositories...)
		if next == "" {
			return repos, nil
		}
		after = next
	}
}

func forgetStalePeerRepository(ctx context.Context, s *Store, member protocol.PoolMember, repo protocol.BackupRepository, route string) error {
	poolBackupAuthorityMu.Lock()
	_, err := GetPoolBackupRepository(s, repo.ID)
	poolBackupAuthorityMu.Unlock()
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrBackupRepoNotFound) {
		return err
	}
	if route == "/forget" {
		return postPeerBackupRoute(ctx, s, member, route, peerForgetRepository{Repository: repo})
	}
	return postPeerBackupRoute(ctx, s, member, route, peerRetainRepositories{Repositories: []protocol.BackupRepository{repo}})
}

// The caller authenticated the current master through the pinned peer channel.
func handlePeerForgetRepository(s *Store, w http.ResponseWriter, r *http.Request) {
	var body peerForgetRepository
	if r.Method != http.MethodPost || decodePeerRepositoryBody(r.Body, 64<<10, &body) != nil || !validPeerForgetSnapshot(s, body.Repository) {
		http.Error(w, "invalid forget request", http.StatusBadRequest)
		return
	}
	response, err := peerBackupRequest(r.Context(), s, BackupRequest{PeerForgetRepositoryID: body.Repository.ID, PeerForgetRepository: &body.Repository})
	if err == nil {
		err = response.Err
	}
	if err != nil {
		writePeerForgetError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The caller authenticated the current master through the pinned peer channel.
func handlePeerRetainRepositories(s *Store, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		query, err := url.ParseQuery(r.URL.RawQuery)
		after := ""
		if err == nil {
			for key, values := range query {
				if key != "after_id" || len(values) != 1 || !protocol.ValidSnapshotOperationID(values[0]) {
					err = ErrBackupInvalid
					break
				}
				after = values[0]
			}
		}
		if err != nil {
			http.Error(w, "invalid retain cursor", http.StatusBadRequest)
			return
		}
		response, err := peerBackupRequest(r.Context(), s, BackupRequest{PeerRepositorySnapshots: true})
		if err == nil {
			err = response.Err
		}
		if err != nil {
			writePeerForgetError(w, err)
			return
		}
		body, err := encodePeerRepositoryPage(s, response.Repositories, after)
		if err != nil {
			writePeerForgetError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
		return
	}
	var body peerRetainRepositories
	if r.Method != http.MethodPost || decodePeerRepositoryBody(r.Body, peerRepositoryBodyLimit, &body) != nil || len(body.Repositories) > peerRepositoryPageLimit {
		http.Error(w, "invalid retain request", http.StatusBadRequest)
		return
	}
	for _, repo := range body.Repositories {
		if !validPeerForgetSnapshot(s, repo) {
			http.Error(w, "invalid retain request", http.StatusBadRequest)
			return
		}
	}
	for _, repo := range body.Repositories {
		response, err := peerBackupRequest(r.Context(), s, BackupRequest{PeerForgetRepositoryID: repo.ID, PeerForgetRepository: &repo})
		if err == nil {
			err = response.Err
		}
		if err != nil {
			writePeerForgetError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// The Store owner supplies a detached snapshot; sorting and paging never touch live state.
func encodePeerRepositoryPage(s *Store, repos []protocol.BackupRepository, after string) ([]byte, error) {
	sort.Slice(repos, func(i, j int) bool { return repos[i].ID < repos[j].ID })
	for i, repo := range repos {
		if !validPeerForgetSnapshot(s, repo) || (i > 0 && repo.ID == repos[i-1].ID) {
			return nil, ErrBackupInvalid
		}
	}
	start := sort.Search(len(repos), func(i int) bool { return repos[i].ID > after })
	var body bytes.Buffer
	body.WriteString(`{"repositories":[`)
	suffix := `]}`
	count := 0
	for i := start; i < len(repos) && count < peerRepositoryPageLimit; i++ {
		row, err := json.Marshal(repos[i])
		if err != nil {
			return nil, err
		}
		nextSuffix := `]}`
		if i+1 < len(repos) {
			// Valid repository IDs contain no JSON escape characters.
			nextSuffix = `],"next_after_id":"` + repos[i].ID + `"}`
		}
		comma := 0
		if count > 0 {
			comma = 1
		}
		if body.Len()+comma+len(row)+len(nextSuffix) > peerRepositoryBodyLimit {
			if count == 0 {
				return nil, ErrBackupUnavailable
			}
			break
		}
		if comma != 0 {
			body.WriteByte(',')
		}
		body.Write(row)
		suffix = nextSuffix
		count++
	}
	body.WriteString(suffix)
	return body.Bytes(), nil
}

func validPeerForgetSnapshot(s *Store, repo protocol.BackupRepository) bool {
	return protocol.ValidSnapshotOperationID(repo.ID) && repo.PoolID == s.State.PoolID
}

func writePeerForgetError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, ErrBackupUnavailable) {
		status = http.StatusServiceUnavailable
	}
	http.Error(w, "backup repository could not be forgotten", status)
}

// acceptPeerRepositoryForget runs on the Store owner, including journal inspection.
func acceptPeerRepositoryForget(ctx context.Context, s *Store, request BackupRequest) BackupResponse {
	if membership, err := Membership(s); err != nil || membership.MasterHostID == s.State.HostID {
		return BackupResponse{Err: ErrBackupUnavailable}
	}
	if request.PeerRepositorySnapshots {
		poolBackupAuthorityMu.Lock()
		defer poolBackupAuthorityMu.Unlock()
		state, err := loadPoolBackupRepositories(s)
		if err != nil {
			return BackupResponse{Err: err}
		}
		repos := make([]protocol.BackupRepository, 0, len(state.Repositories))
		for _, repo := range state.Repositories {
			repos = append(repos, repo)
		}
		entries, err := os.ReadDir(filepath.Dir(pendingBackupDetachPath(s, "unused")))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return BackupResponse{Err: err}
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			if _, exists := state.Repositories[id]; exists {
				continue
			}
			raw, err := os.ReadFile(pendingBackupDetachPath(s, id))
			var repo protocol.BackupRepository
			if err != nil || json.Unmarshal(raw, &repo) != nil || repo.ID != id {
				return BackupResponse{Err: errors.New("pending backup detach record is corrupt")}
			}
			repos = append(repos, repo)
		}
		return BackupResponse{Repositories: repos}
	}
	if request.PeerForgetRepository == nil || request.PeerForgetRepository.ID != request.PeerForgetRepositoryID ||
		!validPeerForgetSnapshot(s, *request.PeerForgetRepository) {
		return BackupResponse{Err: ErrBackupInvalid}
	}
	return BackupResponse{Err: forgetMemberBackupRepository(ctx, s, request.PeerForgetRepositoryID, request.PeerForgetRepository)}
}

func sameBackupRepositoryRegistration(a, b protocol.BackupRepository) bool {
	a.Access, b.Access = protocol.StorageAccess{}, protocol.StorageAccess{}
	a.Health, b.Health = protocol.BackupRepositoryHealth{}, protocol.BackupRepositoryHealth{}
	a.MemberAccess, b.MemberAccess = nil, nil
	a.LastHealthTransition, b.LastHealthTransition = nil, nil
	return reflect.DeepEqual(a, b)
}

func forgetMemberBackupRepository(ctx context.Context, s *Store, id string, expected *protocol.BackupRepository) error {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	repo, ok := repos.Repositories[id]
	if !ok {
		raw, err := os.ReadFile(pendingBackupDetachPath(s, id))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var pending protocol.BackupRepository
		if json.Unmarshal(raw, &pending) != nil || pending.ID != id {
			return errors.New("pending backup detach record is corrupt")
		}
		if expected == nil || !sameBackupRepositoryRegistration(pending, *expected) {
			return nil
		}
		if opID := memberJournalUsesBackupRepository(s, id); opID != "" {
			return ErrBackupConflict
		}
		mutation, err := reserveBackupRepositoryMutation(s, id)
		if err != nil {
			return err
		}
		defer mutation.releaseLocked()
		poolBackupAuthorityMu.Unlock()
		err = finishPendingBackupDetach(ctx, s, id)
		poolBackupAuthorityMu.Lock()
		return err
	}
	if expected == nil || !sameBackupRepositoryRegistration(repo, *expected) {
		return nil
	}
	if opID := memberJournalUsesBackupRepository(s, id); opID != "" {
		return fmt.Errorf("operation %s on this host still uses backup repository %s", opID, id)
	}
	mutation, err := reserveBackupRepositoryMutation(s, id)
	if err != nil {
		return err
	}
	defer mutation.releaseLocked()
	if err := rememberPendingBackupDetach(s, repo); err != nil {
		return err
	}
	poolBackupAuthorityMu.Unlock()
	detachErr := detachBackupRepositoryHost(ctx, repo)
	poolBackupAuthorityMu.Lock()
	if detachErr != nil {
		return detachErr
	}
	repos, err = loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	if current, ok := repos.Repositories[id]; !ok || !sameBackupRepositoryRegistration(current, *expected) {
		return nil
	}
	if err := retireBackupRepositoryCredential(s, repo); err != nil {
		return err
	}
	delete(repos.Repositories, repo.ID)
	if err := savePoolBackupRepositories(s, repos); err != nil {
		return err
	}
	return clearPendingBackupDetach(s, id)
}
