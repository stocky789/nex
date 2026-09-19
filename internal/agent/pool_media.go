// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolMediaPath = "/api/v1/pool/media/"

var poolMediaMu sync.Mutex
var poolMediaAccess = map[string]map[string]map[string]protocol.PoolMediaAccess{} // dir -> media -> host -> access

// adoptPoolMedia verifies and journals a shared ISO on this member. Tests may stub it.
var adoptPoolMedia = adoptPoolMediaNative

func poolMediaAccessFor(s *Store) map[string]map[string]protocol.PoolMediaAccess {
	if poolMediaAccess[s.dir] == nil {
		poolMediaAccess[s.dir] = map[string]map[string]protocol.PoolMediaAccess{}
	}
	return poolMediaAccess[s.dir]
}

// PoolMediaMemberAccess overlays process-local member ISO observations onto the master's
// library. Only media whose storage is pool-shared carries member rows.
func PoolMediaMemberAccess(s *Store, media []protocol.Media, storage []protocol.StorageRegistration) []protocol.Media {
	poolMediaMu.Lock()
	defer poolMediaMu.Unlock()
	accesses := poolMediaAccessFor(s)
	shared := map[string]bool{}
	for _, repo := range storage {
		if protocol.IsPoolSharedStorage(repo) && !repo.Tombstone {
			shared[repo.ID] = true
		}
	}
	members := readyPoolMembers(s)
	out := append([]protocol.Media(nil), media...)
	for i := range out {
		out[i].MemberAccess = nil
		if !shared[out[i].StorageID] {
			continue
		}
		for _, hostID := range members {
			if access, ok := accesses[out[i].ID][hostID]; ok && access.HostID != "" {
				out[i].MemberAccess = append(out[i].MemberAccess, access)
				continue
			}
			out[i].MemberAccess = append(out[i].MemberAccess, protocol.PoolMediaAccess{
				MediaID: out[i].ID, HostID: hostID, StorageID: out[i].StorageID,
				Error: "awaiting member reconciliation",
			})
		}
	}
	return out
}

// PoolMediaHandler serves master-owned ISO metadata for media on pool-shared storage.
func PoolMediaHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		member, err := AuthenticatePoolPeer(s, r)
		if err != nil {
			http.Error(w, "peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, poolMediaPath), "/access")
			var access protocol.PoolMediaAccess
			if id == "" || json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&access) != nil || access.HostID != member.HostID || access.MediaID != id {
				http.Error(w, "invalid access report", http.StatusBadRequest)
				return
			}
			media, err := poolSharedMedia(s)
			if err != nil {
				http.Error(w, poolJournalIncomplete, http.StatusServiceUnavailable)
				return
			}
			found := false
			for _, m := range media {
				if m.ID == id && m.StorageID == access.StorageID {
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "media unavailable", http.StatusConflict)
				return
			}
			poolMediaMu.Lock()
			accesses := poolMediaAccessFor(s)
			if accesses[id] == nil {
				accesses[id] = map[string]protocol.PoolMediaAccess{}
			}
			accesses[id][member.HostID] = access
			poolMediaMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// As for networks: a short list is indistinguishable from a removal, so an
		// incomplete journal answers 503 and every member keeps what it holds.
		shared, err := poolSharedMedia(s)
		if err != nil {
			http.Error(w, poolJournalIncomplete, http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == strings.TrimSuffix(poolMediaPath, "/") {
			out := shared
			for i := range out {
				out[i].HostID = member.HostID
				out[i].MemberAccess = nil
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, poolMediaPath) {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, poolMediaPath)
		if id == "" || strings.ContainsAny(id, "/\\") {
			http.NotFound(w, r)
			return
		}
		for _, m := range shared {
			if m.ID == id {
				m.HostID = member.HostID
				m.MemberAccess = nil
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(m)
				return
			}
		}
		http.NotFound(w, r)
	})
}

// poolSharedMedia projects the ISOs on this host's pool-shared repositories. It fails rather
// than shortening the list when either projection is built from an incomplete journal: a
// member reads absence from this list as removal.
func poolSharedMedia(s *Store) ([]protocol.Media, error) {
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	storage, checks := engine.AppliedStorage()
	for _, check := range checks {
		if check.Status == "failed" {
			return nil, vmhost.ErrManagedJournalIncomplete
		}
	}
	shared := map[string]bool{}
	for _, repo := range storage {
		if protocol.IsPoolSharedStorage(repo) && !repo.Tombstone {
			shared[repo.ID] = true
		}
	}
	media, err := engine.AppliedMedia()
	if err != nil {
		return nil, err
	}
	out := []protocol.Media{}
	for _, m := range media {
		// A removed ISO projects as a tombstone row with no storage, so it leaves the
		// peer list the way a detached repository does: members infer removal from absence.
		if shared[m.StorageID] && !m.Tombstone {
			out = append(out, m)
		}
	}
	return out, nil
}

// ReconcilePoolMediaFromMaster pulls ISO library metadata for pool-shared repositories.
func ReconcilePoolMediaFromMaster(ctx context.Context, s *Store) error {
	return reconcilePoolMediaFromMaster(ctx, s, 3*time.Second)
}

func reconcilePoolMediaFromMaster(ctx context.Context, s *Store, perMediaTimeout time.Duration) error {
	if perMediaTimeout <= 0 {
		return errors.New("pool media reconciliation timeout is required")
	}
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		return nil
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	listCtx, cancelList := context.WithTimeout(ctx, perMediaTimeout)
	defer cancelList()
	req, err := http.NewRequestWithContext(listCtx, http.MethodGet, strings.TrimRight(master, "/")+strings.TrimSuffix(poolMediaPath, "/"), nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("pool master media list unavailable")
	}
	var media []protocol.Media
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&media) != nil {
		return errors.New("invalid pool master media list")
	}
	// Authoritative-list gate, as for repositories and networks: an ISO is removed here
	// only because a list that arrived whole, in time, and entirely from this pool stopped
	// naming it. Every other shape returns before the orphan loop below.
	if err := listCtx.Err(); err != nil {
		return err
	}
	cancelList()
	for _, entry := range media {
		if entry.PoolID != s.State.PoolID || entry.Tombstone {
			return errors.New("pool master returned another pool media entry")
		}
	}
	desired := map[string]bool{}
	var last error
	for _, entry := range media {
		if err := ctx.Err(); err != nil {
			return err
		}
		desired[entry.ID] = true
		work, cancel := context.WithTimeout(ctx, perMediaTimeout)
		err := reconcilePoolMedia(work, s, c, master, entry.ID)
		cancel()
		if err != nil {
			last = err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := removeOrphanPoolMedia(ctx, s, desired, perMediaTimeout); err != nil {
		return err
	}
	return last
}

// removeOrphanPoolMedia removes the shared ISOs this member still holds that the master's
// authoritative list no longer contains. Eligibility is the same as the peer list's: an ISO
// on pool-shared storage of the pool this host is currently a member of, never a host-local
// import and never one left over from a former pool.
func removeOrphanPoolMedia(ctx context.Context, s *Store, desired map[string]bool, timeout time.Duration) error {
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	shared, err := poolSharedMedia(s)
	if err != nil {
		return err
	}
	var last error
	for _, entry := range shared {
		if err := ctx.Err(); err != nil {
			return err
		}
		if desired[entry.ID] || entry.PoolID != s.State.PoolID {
			continue
		}
		// Precheck before Execute: a persistent refusal — an ISO a local VM still holds in
		// a CD drive, say — must not mint a journal entry on every probe tick.
		if err := engine.CheckMediaRemoval(ctx, entry.ID); err != nil {
			last = poolConvergeRefusalf("pool ISO %s cannot be removed on this host: %s", entry.ID, err)
			continue
		}
		generation := protocol.MediaGeneration(entry)
		work, cancel := context.WithTimeout(ctx, timeout)
		// Deterministic within the UTC hour, like the network orphan loop: an unexpected
		// native failure replays one stored entry instead of writing one per probe tick.
		err := executePoolResourceLifecycle(work, engine, protocol.Command{
			OperationID: poolConvergeOperation("media-remove", entry.ID, generation),
			Kind:        "media-remove",
			ResourceLifecycle: &protocol.ResourceLifecycleRequest{
				Resource: "media", Action: "remove", ResourceID: entry.ID,
				PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: generation,
			},
		})
		cancel()
		if err != nil {
			last = errors.New("pool media removal did not converge")
		}
	}
	return last
}

func reconcilePoolMedia(ctx context.Context, s *Store, c *http.Client, masterURL, mediaID string) error {
	if mediaID == "" || strings.ContainsAny(mediaID, "/\\") {
		return errors.New("invalid pool media ID")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(masterURL, "/")+poolMediaPath+mediaID, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("pool master refused media reconciliation")
	}
	var media protocol.Media
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&media) != nil {
		return errors.New("invalid pool media response")
	}
	if media.ID != mediaID || media.PoolID != s.State.PoolID {
		return errors.New("pool media response does not match membership")
	}
	media.HostID = s.State.HostID
	media.MemberAccess, media.OperationID, media.Status, media.Error, media.URL = nil, "", "", "", ""
	access := protocol.PoolMediaAccess{
		MediaID: media.ID, HostID: s.State.HostID, StorageID: media.StorageID, ObservedAt: time.Now().UTC(),
	}
	if err := adoptPoolMedia(ctx, s, media); err != nil {
		access.Error = err.Error()
		_ = postPoolMediaAccess(ctx, c, masterURL, media.ID, access)
		return err
	}
	access.SHA256 = media.SHA256
	return postPoolMediaAccess(ctx, c, masterURL, media.ID, access)
}

func adoptPoolMediaNative(ctx context.Context, s *Store, media protocol.Media) error {
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	// A removal is permanent here: the tombstone keeps the ISO out of this host's library
	// whatever is re-adopted, so re-adopting it would only report healthy access for an ISO
	// no VM could attach. Refuse instead, and say what the operator must do on the master.
	removed, err := engine.MediaRemoved(media.ID)
	if err != nil {
		return err
	}
	if removed {
		return poolConvergeRefusalf("pool ISO %s was removed on this host and is listed again by the master; remove it on the master and add it again", media.ID)
	}
	return engine.AdoptSharedMedia(ctx, media)
}

func postPoolMediaAccess(ctx context.Context, c *http.Client, masterURL, mediaID string, access protocol.PoolMediaAccess) error {
	b, err := json.Marshal(access)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(masterURL, "/")+poolMediaPath+mediaID+"/access", strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("pool master refused media access report")
	}
	return nil
}

// ClearPoolMediaAccessForTest resets process-local media access observations.
func ClearPoolMediaAccessForTest(s *Store) {
	poolMediaMu.Lock()
	defer poolMediaMu.Unlock()
	delete(poolMediaAccess, s.dir)
}
