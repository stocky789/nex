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
	"os"
	"path/filepath"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// backupRefusal is a refusal the operator can act on; its text is the command's failure.
type backupRefusal string

func (e backupRefusal) Error() string { return string(e) }

type peerForgetRepository struct {
	RepositoryID string `json:"repository_id"`
}

type peerRetainRepositories struct {
	RepositoryIDs []string `json:"repository_ids"`
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
	repo, ok := repos.Repositories[req.RepositoryID]
	if !ok {
		// Record already gone: still finish a pending detach left by a crash mid-forget.
		return protocol.BackupRepository{}, false, finishPendingBackupDetach(ctx, s, req.RepositoryID)
	}
	if req.Confirmation != repo.Name {
		return repo, true, backupRefusal(fmt.Sprintf("type the repository name %q to confirm", repo.Name))
	}
	policies, err := loadPoolBackupPolicies(s)
	if err != nil {
		return repo, true, err
	}
	jobs := 0
	for _, policy := range policies.Policies {
		if policy.RepositoryID == repo.ID {
			jobs++
		}
	}
	if jobs > 0 {
		return repo, true, backupRefusal(fmt.Sprintf("%d backup jobs use this repository; change their repository first", jobs))
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return repo, true, err
	}
	var repoRuns []protocol.BackupRun
	for _, run := range runs {
		if run.RepositoryID != repo.ID {
			continue
		}
		if run.Status == "pending" {
			return repo, true, backupRefusal("a backup, restore, test restore or catalog operation is running on this repository; wait for it to finish")
		}
		repoRuns = append(repoRuns, run)
	}
	catalog, err := loadPoolBackupCatalog(s)
	if err != nil {
		return repo, true, err
	}
	pins, err := loadPoolBackupPins(s)
	if err != nil {
		return repo, true, err
	}
	var points []protocol.BackupRecoveryPoint
	protected := 0
	for _, point := range catalog {
		if point.RepositoryID != repo.ID {
			continue
		}
		for _, pin := range pins.Pins {
			if pin.RecoveryPointID == point.ID {
				return repo, true, backupRefusal(pinRefusal(s, pin))
			}
		}
		if poolBackupPruning[point.ID] {
			return repo, true, backupRefusal("retention is deleting recovery points from this repository; try again shortly")
		}
		if point.Protected {
			protected++
		}
		points = append(points, point)
	}
	if len(points) != req.RecoveryPoints || protected != req.ProtectedPoints {
		return repo, true, backupRefusal(fmt.Sprintf("this repository now holds %d recovery points (%d protected), not the %d (%d protected) confirmed; review and confirm again",
			len(points), protected, req.RecoveryPoints, req.ProtectedPoints))
	}
	// Detach first and remember the mount: a failed unmount leaves the record, and a crash
	// after the record is gone still retries from the pending detach.
	// ponytail: holds the authority lock for the 20 s detach bound when the file server is dead.
	if err := rememberPendingBackupDetach(s, repo); err != nil {
		return repo, true, err
	}
	if err := detachBackupRepositoryHost(ctx, repo); err != nil {
		return repo, true, fmt.Errorf("the mount of %q could not be removed on this host; try again: %w", repo.Name, err)
	}
	if err := retireBackupRepositoryCredential(s, repo); err != nil {
		return repo, true, err
	}
	for _, point := range points {
		if err := removeIfPresent(filepath.Join(poolBackupCatalogPath(s), point.ID+".json")); err != nil {
			return repo, true, err
		}
	}
	for _, run := range repoRuns {
		if err := removeIfPresent(filepath.Join(poolBackupRunsPath(s), run.OperationID+".json")); err != nil {
			return repo, true, err
		}
	}
	delete(repos.Repositories, repo.ID)
	if err := savePoolBackupRepositories(s, repos); err != nil {
		return repo, true, err
	}
	return repo, true, clearPendingBackupDetach(s, repo.ID)
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

func removeIfPresent(path string) error {
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
	return postPeerBackupRoute(ctx, s, member, "/forget", peerForgetRepository{RepositoryID: id})
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

// retainBackupRepositoryOnMembers drops the records a member still holds for repositories this
// master no longer has, such as a forget the member missed. Members keep no unique state, and
// a member refuses to forget a repository its own journal still uses.
func retainBackupRepositoryOnMembers(ctx context.Context, s *Store, membership protocol.PoolMembership) {
	for _, member := range poolConfirmationPeers(membership, s.State.HostID) {
		if member.PeerOrigin == "" {
			continue
		}
		// Read again for each member: a repository registered during a slow sweep must not
		// be missing from the list a later member receives.
		repos, err := ListPoolBackupRepositories(s)
		if err != nil {
			return
		}
		held := peerRetainRepositories{RepositoryIDs: []string{}}
		for _, repo := range repos {
			held.RepositoryIDs = append(held.RepositoryIDs, repo.ID)
		}
		if err := postPeerBackupRoute(ctx, s, member, "/retain", held); err != nil {
			log.Printf("member %s did not drop stale backup repositories: %v", member.HostID, err)
		}
	}
}

// The caller authenticated the current master through the pinned peer channel.
func handlePeerForgetRepository(s *Store, w http.ResponseWriter, r *http.Request) {
	var body peerForgetRepository
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if r.Method != http.MethodPost || decoder.Decode(&body) != nil || !protocol.ValidSnapshotOperationID(body.RepositoryID) {
		http.Error(w, "invalid forget request", http.StatusBadRequest)
		return
	}
	if membership, err := Membership(s); err != nil || membership.MasterHostID == s.State.HostID {
		http.Error(w, "not a pool member", http.StatusConflict)
		return
	}
	if forgetMemberBackupRepository(r.Context(), s, body.RepositoryID) != nil {
		http.Error(w, "backup repository could not be forgotten", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The caller authenticated the current master through the pinned peer channel.
func handlePeerRetainRepositories(s *Store, w http.ResponseWriter, r *http.Request) {
	var body peerRetainRepositories
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if r.Method != http.MethodPost || decoder.Decode(&body) != nil || len(body.RepositoryIDs) > 256 {
		http.Error(w, "invalid retain request", http.StatusBadRequest)
		return
	}
	if membership, err := Membership(s); err != nil || membership.MasterHostID == s.State.HostID {
		http.Error(w, "not a pool member", http.StatusConflict)
		return
	}
	held := map[string]bool{}
	for _, id := range body.RepositoryIDs {
		if !protocol.ValidSnapshotOperationID(id) {
			http.Error(w, "invalid retain request", http.StatusBadRequest)
			return
		}
		held[id] = true
	}
	poolBackupAuthorityMu.Lock()
	repos, err := loadPoolBackupRepositories(s)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		http.Error(w, "backup repositories unavailable", http.StatusConflict)
		return
	}
	for id := range repos.Repositories {
		if !held[id] {
			err = errors.Join(err, forgetMemberBackupRepository(r.Context(), s, id))
		}
	}
	if err != nil {
		http.Error(w, "backup repository could not be forgotten", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func forgetMemberBackupRepository(ctx context.Context, s *Store, id string) error {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	repo, ok := repos.Repositories[id]
	if !ok {
		return finishPendingBackupDetach(ctx, s, id)
	}
	if opID := memberJournalUsesBackupRepository(s, id); opID != "" {
		return fmt.Errorf("operation %s on this host still uses backup repository %s", opID, id)
	}
	// Detach before the record goes, so a failed unmount is retried by the next sweep.
	if err := rememberPendingBackupDetach(s, repo); err != nil {
		return err
	}
	if err := detachBackupRepositoryHost(ctx, repo); err != nil {
		return err
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
