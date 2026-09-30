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
)

// forgetRefusal is a refusal the operator can act on; its text is the command's failure.
type forgetRefusal string

func (e forgetRefusal) Error() string { return string(e) }

type peerForgetRepository struct {
	RepositoryID string `json:"repository_id"`
}

// forgetPoolBackupRepository drops the repository's record, recovery points and run
// history on the master, then its mount and sealed credential on every host. It never
// touches the destination, so registering the same destination again and rebuilding the
// catalog brings the recovery points back. An unknown repository is already forgotten.
func forgetPoolBackupRepository(ctx context.Context, s *Store, req protocol.ForgetRepositoryRequest) error {
	repo, found, err := dropPoolBackupRepositoryRecords(s, req)
	if err != nil {
		return err
	}
	if found {
		if err := detachBackupRepositoryHost(ctx, repo); err != nil {
			log.Printf("forgotten backup repository %s left its mount on this host: %v", repo.ID, err)
		}
	}
	forgetBackupRepositoryOnMembers(ctx, s, req.RepositoryID)
	return nil
}

func dropPoolBackupRepositoryRecords(s *Store, req protocol.ForgetRepositoryRequest) (protocol.BackupRepository, bool, error) {
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
		return protocol.BackupRepository{}, false, nil
	}
	if req.Confirmation != repo.Name {
		return repo, true, forgetRefusal(fmt.Sprintf("type the repository name %q to confirm", repo.Name))
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
		return repo, true, forgetRefusal(fmt.Sprintf("%d backup jobs use this repository; change their repository first", jobs))
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
			return repo, true, forgetRefusal("a backup, restore, test restore or catalog operation is running on this repository; wait for it to finish")
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
		if backupPointPinned(pins, point.ID) {
			return repo, true, forgetRefusal("a recovery point of this repository is in use by an unfinished restore or test restore; finish or clean it up first")
		}
		if poolBackupPruning[point.ID] {
			return repo, true, forgetRefusal("retention is deleting recovery points from this repository; try again shortly")
		}
		if point.Protected {
			protected++
		}
		points = append(points, point)
	}
	if len(points) != req.RecoveryPoints || protected != req.ProtectedPoints {
		return repo, true, forgetRefusal(fmt.Sprintf("this repository now holds %d recovery points (%d protected), not the %d (%d protected) confirmed; review and confirm again",
			len(points), protected, req.RecoveryPoints, req.ProtectedPoints))
	}
	// Secret first: if it cannot be retired nothing is forgotten and the request can be retried.
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
	return repo, true, savePoolBackupRepositories(s, repos)
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
	if file == nil || err != nil || !file.stored.Receipt.RetiredAt.IsZero() {
		return err
	}
	return retireBackupCredentialFile(s, file.path, file.stored)
}

// detachBackupRepositoryHost is bounded because a dead file server can hold an unmount.
func detachBackupRepositoryHost(ctx context.Context, repo protocol.BackupRepository) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return detachBackupRepositoryNative(ctx, repo)
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
	client, err := poolTransferClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(peerForgetRepository{RepositoryID: id})
	if err != nil {
		return err
	}
	work, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolBackupPeerPath+"/forget", bytes.NewReader(body))
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
	poolBackupAuthorityMu.Lock()
	repos, err := loadPoolBackupRepositories(s)
	repo, ok := repos.Repositories[body.RepositoryID]
	if err == nil && ok {
		if err = retireBackupRepositoryCredential(s, repo); err == nil {
			delete(repos.Repositories, repo.ID)
			err = savePoolBackupRepositories(s, repos)
		}
	}
	poolBackupAuthorityMu.Unlock()
	if err == nil && ok {
		err = detachBackupRepositoryHost(r.Context(), repo)
	}
	if err != nil {
		http.Error(w, "backup repository could not be forgotten", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
