// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolStoragePath = "/api/v1/pool/storage/"

type poolStorageTransfer struct {
	Storage     protocol.StorageRegistration `json:"storage"`
	Credentials *protocol.StorageCredentials `json:"credentials,omitempty"`
}

type poolStorageState struct {
	Storage      map[string]protocol.StorageRegistration          `json:"storage"`
	Access       map[string]map[string]protocol.PoolStorageAccess `json:"access"`
	Reservations map[string]map[string]string                     `json:"reservations"`
	Mutations    map[string]string                                `json:"mutations"`
}

var poolStorageMu sync.Mutex
var poolReconcileStatus = struct {
	sync.Mutex
	byDir map[string]poolReconcileObservation
}{byDir: map[string]poolReconcileObservation{}}

type poolReconcileObservation struct {
	at  time.Time
	err error
}

var poolProbeLocks = struct {
	sync.Mutex
	files map[string]*os.File
}{files: map[string]*os.File{}}

func poolStorageFile(s *Store) string { return filepath.Join(s.dir, "pool-storage.json") }
func loadPoolStorage(s *Store) (poolStorageState, error) {
	state := poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Access: map[string]map[string]protocol.PoolStorageAccess{}, Reservations: map[string]map[string]string{}, Mutations: map[string]string{}}
	b, err := os.ReadFile(poolStorageFile(s))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(b, &state); err != nil || state.Storage == nil {
		return state, errors.New("invalid pool storage state")
	}
	if state.Access == nil {
		state.Access = map[string]map[string]protocol.PoolStorageAccess{}
	}
	if state.Reservations == nil {
		state.Reservations = map[string]map[string]string{}
	}
	if state.Mutations == nil {
		state.Mutations = map[string]string{}
	}
	return state, nil
}

func PoolStorageMemberAccess(s *Store, storage []protocol.StorageRegistration) []protocol.StorageRegistration {
	poolStorageMu.Lock()
	defer poolStorageMu.Unlock()
	state, err := loadPoolStorage(s)
	if err != nil {
		return storage
	}
	membership, _ := Membership(s)
	out := append([]protocol.StorageRegistration(nil), storage...)
	for i := range out {
		if out[i].StorageKind() == protocol.StorageKindNFS || out[i].StorageKind() == protocol.StorageKindSMB {
			for _, member := range membership.Members {
				if member.HostID != s.State.HostID && member.Status == "ready" && state.Access[out[i].ID][member.HostID].HostID == "" {
					out[i].MemberAccess = append(out[i].MemberAccess, protocol.PoolStorageAccess{RepositoryID: out[i].ID, HostID: member.HostID, Generation: out[i].Generation, Error: "awaiting member reconciliation"})
				}
			}
		}
		for _, access := range state.Access[out[i].ID] {
			out[i].MemberAccess = append(out[i].MemberAccess, access)
		}
	}
	return out
}
func savePoolStorage(s *Store, state poolStorageState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".pool-storage-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(name, 0600); err != nil {
		return err
	}
	if err = os.Rename(name, poolStorageFile(s)); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// PoolStorageHandler runs only on the dedicated peer listener. The mTLS member identity
// determines who receives a resource/secret; client input cannot select another member.
func PoolStorageHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		member, err := AuthenticatePoolPeer(s, r)
		if err != nil {
			http.Error(w, "peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reserve") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, poolStoragePath), "/reserve")
			var request struct {
				OperationID string `json:"operation_id"`
				Generation  uint64 `json:"generation"`
			}
			if id == "" || json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&request) != nil || request.OperationID == "" {
				http.Error(w, "invalid reservation", http.StatusBadRequest)
				return
			}
			managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(r.Context())
			valid := false
			for _, storage := range managed.Storage {
				if storage.ID == id && !storage.Tombstone && storage.Generation == request.Generation && (storage.StorageKind() == protocol.StorageKindNFS || storage.StorageKind() == protocol.StorageKindSMB) {
					valid = true
					break
				}
			}
			if !valid {
				http.Error(w, "repository generation unavailable", http.StatusConflict)
				return
			}
			poolStorageMu.Lock()
			state, e := loadPoolStorage(s)
			if e == nil {
				if state.Mutations[id] != "" {
					e = errors.New("repository mutation pending")
				} else {
					if state.Reservations[id] == nil {
						state.Reservations[id] = map[string]string{}
					}
					if recordPoolStorageReservation(&state, id, member.HostID, request.OperationID) {
						e = savePoolStorage(s, state)
					}
				}
			}
			poolStorageMu.Unlock()
			if e != nil {
				http.Error(w, "reservation refused", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/reserve") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, poolStoragePath), "/reserve")
			operation, unused := r.URL.Query().Get("operation_id"), r.URL.Query().Get("release_unused") == "1"
			if id == "" || (!unused && operation == "") {
				http.Error(w, "invalid reservation release", http.StatusBadRequest)
				return
			}
			poolStorageMu.Lock()
			state, e := loadPoolStorage(s)
			if e == nil && (unused || state.Reservations[id][member.HostID] == operation) {
				delete(state.Reservations[id], member.HostID)
				if len(state.Reservations[id]) == 0 {
					delete(state.Reservations, id)
				}
				e = savePoolStorage(s, state)
			}
			poolStorageMu.Unlock()
			if e != nil {
				http.Error(w, "reservation release failed", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, poolStoragePath), "/access")
			var access protocol.PoolStorageAccess
			if id == "" || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&access) != nil || access.RepositoryID != id || access.HostID != member.HostID {
				http.Error(w, "invalid member access", http.StatusBadRequest)
				return
			}
			managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(r.Context())
			valid := false
			for _, storage := range managed.Storage {
				if storage.ID == id && !storage.Tombstone && storage.Generation == access.Generation && (storage.StorageKind() == protocol.StorageKindNFS || storage.StorageKind() == protocol.StorageKindSMB) {
					valid = true
					break
				}
			}
			if !valid {
				http.Error(w, "repository generation is unavailable", http.StatusConflict)
				return
			}
			access.ObservedAt = time.Now().UTC()
			poolStorageMu.Lock()
			state, e := loadPoolStorage(s)
			if e == nil {
				if state.Access[id] == nil {
					state.Access[id] = map[string]protocol.PoolStorageAccess{}
				}
				state.Access[id][member.HostID] = access
				e = savePoolStorage(s, state)
			}
			poolStorageMu.Unlock()
			if e != nil {
				http.Error(w, "access unavailable", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if (r.Method == http.MethodPost || r.Method == http.MethodDelete) && strings.HasSuffix(r.URL.Path, "/lock") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, poolStoragePath), "/lock")
			generation, challenge := r.URL.Query().Get("generation"), r.URL.Query().Get("challenge")
			if id == "" || generation == "" || len(challenge) != 32 {
				http.NotFound(w, r)
				return
			}
			lockKey := id + ":" + generation + ":" + challenge
			if r.Method == http.MethodDelete {
				releasePoolProbeLock(lockKey)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(r.Context())
			for _, storage := range managed.Storage {
				if storage.ID == id && (storage.StorageKind() == protocol.StorageKindNFS || storage.StorageKind() == protocol.StorageKindSMB) {
					if strconv.FormatUint(storage.Generation, 10) != generation {
						http.Error(w, "storage generation changed", http.StatusConflict)
						return
					}
					if err := holdPoolProbeLock(lockKey, storage.Path); err != nil {
						http.Error(w, "lock unavailable", http.StatusConflict)
						return
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == strings.TrimSuffix(poolStoragePath, "/") {
			managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(r.Context())
			out := []protocol.StorageRegistration{}
			for _, storage := range managed.Storage {
				if !storage.Tombstone && (storage.StorageKind() == protocol.StorageKindNFS || storage.StorageKind() == protocol.StorageKindSMB) {
					storage.MemberAccess = nil
					storage.CredentialReceipt = ""
					out = append(out, storage)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, poolStoragePath) {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, poolStoragePath)
		if id == "" || strings.ContainsAny(id, "/\\") {
			http.NotFound(w, r)
			return
		}
		managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(r.Context())
		for _, storage := range managed.Storage {
			if storage.ID == id && !storage.Tombstone {
				if storage.StorageKind() != protocol.StorageKindNFS && storage.StorageKind() != protocol.StorageKindSMB {
					http.Error(w, "repository is not pool-shareable", http.StatusConflict)
					return
				}
				transfer := poolStorageTransfer{Storage: storage}
				transfer.Storage.MemberAccess = nil
				if storage.StorageKind() == protocol.StorageKindSMB {
					c, e := vmhost.PoolStorageCredential(s.dir, storage.CredentialReceipt)
					if e != nil {
						http.Error(w, "credential unavailable", http.StatusConflict)
						return
					}
					transfer.Credentials = &c
				}
				transfer.Storage.HostID = member.HostID
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(transfer)
				return
			}
		}
		http.NotFound(w, r)
	})
}

// ReconcilePoolStorage fetches exactly one master repository over pinned mTLS, persists
// any SMB secret directly to the member credential store, then prepares/mounts it locally.
func ReconcilePoolStorage(ctx context.Context, s *Store, masterURL, repositoryID string) error {
	if repositoryID == "" || strings.ContainsAny(repositoryID, "/\\") {
		return errors.New("invalid pool repository ID")
	}
	c, err := PoolPeerClient(s, masterURL)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(masterURL, "/")+poolStoragePath+repositoryID, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("pool master refused repository reconciliation")
	}
	var transfer poolStorageTransfer
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&transfer) != nil {
		return errors.New("invalid pool storage response")
	}
	storage := transfer.Storage
	if storage.ID != repositoryID || storage.PoolID != s.State.PoolID || (storage.StorageKind() != protocol.StorageKindNFS && storage.StorageKind() != protocol.StorageKindSMB) {
		return errors.New("pool storage response does not match membership")
	}
	storage.HostID = s.State.HostID
	expectedIdentity := storage.Identity
	if storage.StorageKind() == protocol.StorageKindSMB {
		if transfer.Credentials == nil {
			return errors.New("pool SMB credential is unavailable")
		}
		receipt, err := vmhost.SavePoolStorageCredential(s.dir, s.State.PoolID, s.State.HostID, storage.ID, storage.Generation, *transfer.Credentials)
		if err != nil {
			return err
		}
		storage.CredentialReceipt = receipt
		storage.CredentialRequestID = vmhost.PoolStorageCredentialRequestID(storage.ID, storage.Generation)
	}
	// Peer transfer carries master observations for identity comparison, but a member
	// ManagedEngine accepts only client-shaped configuration and must observe its own
	// mount path/access. Never loosen the public command validator for this transport.
	storage = poolMemberStorageIntent(storage)
	// Execute journals intent before native mount/session side effects and supplies the
	// existing recovery/tombstone/dependency behavior to member-applied storage too.
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	applied, _ := engine.Applied(ctx)
	operation := poolStorageOperation("apply", storage.ID, storage.Generation)
	command := protocol.Command{OperationID: operation, Kind: "storage-register", Storage: &storage}
	alreadyCurrent := false
	for _, current := range applied.Storage {
		if current.ID == storage.ID && !current.Tombstone {
			if current.Generation == storage.Generation {
				storage, err = vmhost.ValidatePoolStorageAccess(ctx, current)
				if err != nil {
					_ = postPoolStorageAccess(ctx, s, masterURL, repositoryID, protocol.PoolStorageAccess{RepositoryID: repositoryID, HostID: s.State.HostID, Generation: current.Generation, Error: err.Error()})
					return err
				}
				alreadyCurrent = true
				break
			}
			command.Kind = "storage-update"
			command.Storage = nil
			command.StorageLifecycle = &protocol.StorageLifecycleRequest{Action: "update", RepositoryID: storage.ID, PoolID: storage.PoolID, HostID: storage.HostID, ExpectedGeneration: current.Generation, Registration: &storage}
			break
		}
	}
	if !alreadyCurrent {
		result, err := engine.Execute(vmhost.WithPoolStorageExpectation(ctx, expectedIdentity, storage.Generation), command, nil)
		if err != nil || result.Status != "succeeded" {
			if err == nil {
				err = errors.New(result.Error)
			}
			_ = postPoolStorageAccess(ctx, s, masterURL, repositoryID, protocol.PoolStorageAccess{RepositoryID: repositoryID, HostID: s.State.HostID, Generation: storage.Generation, Error: err.Error()})
			return err
		}
		applied, _ = engine.Applied(ctx)
		for _, candidate := range applied.Storage {
			if candidate.ID == storage.ID {
				storage = candidate
				break
			}
		}
	}
	if !vmhost.StorageIdentityMatchesExpected(expectedIdentity, storage.Identity) {
		err = errors.New("member repository identity does not match the master observation")
		_ = postPoolStorageAccess(ctx, s, masterURL, repositoryID, protocol.PoolStorageAccess{RepositoryID: repositoryID, HostID: s.State.HostID, Generation: storage.Generation, Error: err.Error()})
		return err
	}
	if storage.Generation < transfer.Storage.Generation {
		return ReconcilePoolStorage(ctx, s, masterURL, repositoryID)
	}
	if storage.Generation > transfer.Storage.Generation {
		return errors.New("member repository generation is newer than the master")
	}
	if err = crossCoreLockProbe(ctx, s, masterURL, storage); err != nil {
		_ = postPoolStorageAccess(ctx, s, masterURL, repositoryID, protocol.PoolStorageAccess{RepositoryID: repositoryID, HostID: s.State.HostID, Generation: storage.Generation, Error: err.Error()})
		return err
	}
	identity, _ := json.Marshal(storage.Identity)
	return postPoolStorageAccess(ctx, s, masterURL, repositoryID, protocol.PoolStorageAccess{RepositoryID: repositoryID, HostID: s.State.HostID, Generation: storage.Generation, Identity: string(identity), Readable: true, Writable: true, QEMUWritable: true, Locking: true, ObservedAt: storage.Access.ObservedAt})
}

func poolMemberStorageIntent(storage protocol.StorageRegistration) protocol.StorageRegistration {
	storage.Path, storage.Identity, storage.Capabilities, storage.Access = "", protocol.StorageIdentity{}, protocol.StorageCapabilities{}, protocol.StorageAccess{}
	storage.Status, storage.Error, storage.OperationID = "", "", ""
	return storage
}

func poolStorageOperation(action, id string, generation uint64) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(action+"\x00"+id+"\x00"+strconv.FormatUint(generation, 10)+"\x00"+strconv.FormatInt(time.Now().UnixNano(), 10))))
}

func holdPoolProbeLock(id, path string) error {
	poolProbeLocks.Lock()
	defer poolProbeLocks.Unlock()
	if poolProbeLocks.files[id] != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(path, ".nex-peer-lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return err
	}
	poolProbeLocks.files[id] = f
	// A peer crash or lost DELETE must not leave a probe lock as a denial of service.
	// The member's conflicting probe runs immediately; this bounded lease is not a VM fence.
	time.AfterFunc(10*time.Second, func() { releasePoolProbeLock(id) })
	return nil
}
func releasePoolProbeLock(id string) {
	poolProbeLocks.Lock()
	defer poolProbeLocks.Unlock()
	if f := poolProbeLocks.files[id]; f != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		delete(poolProbeLocks.files, id)
	}
}
func crossCoreLockProbe(ctx context.Context, s *Store, masterURL string, storage protocol.StorageRegistration) error {
	c, err := PoolPeerClient(s, masterURL)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	challengeBytes := make([]byte, 16)
	if _, err := rand.Read(challengeBytes); err != nil {
		return err
	}
	lockURL := strings.TrimRight(masterURL, "/") + poolStoragePath + storage.ID + "/lock?generation=" + strconv.FormatUint(storage.Generation, 10) + "&challenge=" + hex.EncodeToString(challengeBytes)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, lockURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("master could not hold shared-storage lock")
	}
	f, err := os.OpenFile(filepath.Join(storage.Path, ".nex-peer-lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return errors.New("shared storage lock is not exclusive across Cores")
	}
	release, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, lockURL, nil)
	if release == nil {
		return errors.New("cannot release master shared-storage lock")
	}
	released, releaseErr := c.Do(release)
	if releaseErr != nil || released.StatusCode != http.StatusNoContent {
		if released != nil {
			released.Body.Close()
		}
		return errors.New("cannot release master shared-storage lock")
	}
	released.Body.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("shared storage lock did not recover after master release")
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return nil
}

func postPoolStorageAccess(ctx context.Context, s *Store, masterURL, repositoryID string, access protocol.PoolStorageAccess) error {
	c, err := PoolPeerClient(s, masterURL)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	b, err := json.Marshal(access)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(masterURL, "/")+poolStoragePath+repositoryID+"/access", strings.NewReader(string(b)))
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
		return errors.New("pool master rejected member access")
	}
	return nil
}

// ReservePoolStorage records a member VM/media operation with the master before local
// execution. The managed worker is its single caller, so a reservation is never created
// concurrently with another local mutation.
func ReservePoolStorage(ctx context.Context, s *Store, command protocol.Command) error {
	ids := poolStorageCommandIDs(ctx, s, command)
	if len(ids) == 0 {
		return nil
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	local, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	reserved := []string{}
	for _, id := range ids {
		for _, storage := range local.Storage {
			if storage.ID == id && (storage.StorageKind() == protocol.StorageKindNFS || storage.StorageKind() == protocol.StorageKindSMB) {
				c, err := PoolPeerClient(s, master)
				if err != nil {
					releasePoolStorageIDs(ctx, s, master, reserved, command.OperationID, false)
					return err
				}
				b, _ := json.Marshal(map[string]any{"operation_id": command.OperationID, "generation": storage.Generation})
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolStoragePath+id+"/reserve", strings.NewReader(string(b)))
				req.Header.Set("Content-Type", "application/json")
				resp, err := c.Do(req)
				c.CloseIdleConnections()
				if err != nil {
					releasePoolStorageIDs(ctx, s, master, reserved, command.OperationID, false)
					return err
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusNoContent {
					releasePoolStorageIDs(ctx, s, master, reserved, command.OperationID, false)
					return errors.New("master refused pool storage reservation")
				}
				reserved = append(reserved, id)
			}
		}
	}
	return nil
}

// ReleasePoolStorageReservations removes reservations for an operation which never
// reached a durable local VM/media definition. Successful definitions retain their
// reservation until their dependency disappears, so a master mutation cannot race it.
func ReleasePoolStorageReservations(ctx context.Context, s *Store, command protocol.Command) {
	ids := poolStorageCommandIDs(ctx, s, command)
	master, err := PoolMasterURL(s)
	if err != nil {
		return
	}
	releasePoolStorageIDs(ctx, s, master, ids, command.OperationID, false)
}

func poolStorageCommandIDs(ctx context.Context, s *Store, command protocol.Command) []string {
	seen, ids := map[string]bool{}, []string{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if command.VM != nil {
		for _, disk := range command.VM.Disks {
			add(disk.StorageID)
		}
	}
	if command.Media != nil {
		add(command.Media.StorageID)
	}
	if command.VMMedia != nil && command.VMMedia.MediaID != "" {
		local, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
		for _, media := range local.Media {
			if media.ID == command.VMMedia.MediaID {
				add(media.StorageID)
				break
			}
		}
	}
	return ids
}

func releasePoolStorageIDs(ctx context.Context, s *Store, master string, ids []string, operation string, unused bool) {
	if len(ids) == 0 {
		return
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		return
	}
	defer c.CloseIdleConnections()
	for _, id := range ids {
		query := "operation_id=" + url.QueryEscape(operation)
		if unused {
			query = "release_unused=1"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(master, "/")+poolStoragePath+id+"/reserve?"+query, nil)
		if err != nil {
			continue
		}
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}
}

// ReleaseUnusedPoolStorageReservations is called after every successful serialized member
// mutation. The authenticated member is the authority for its local durable journal: once
// that journal contains no VM disk or ISO reference to a remote repository, no stale
// reservation may keep a master mutation fenced forever.
func ReleaseUnusedPoolStorageReservations(ctx context.Context, s *Store) {
	master, err := PoolMasterURL(s)
	if err != nil {
		return
	}
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	ids, safe := poolStorageUnusedIDs(managed, checks)
	if !safe {
		return
	}
	releasePoolStorageIDs(ctx, s, master, ids, "", true)
}

func poolStorageUnusedIDs(managed *protocol.ManagedState, checks []protocol.Check) ([]string, bool) {
	for _, check := range checks {
		if check.Status != "passed" && check.Status != "healthy" {
			return nil, false
		}
	}
	used := map[string]bool{}
	for _, vm := range managed.VMs {
		for _, disk := range vm.Disks {
			used[disk.StorageID] = true
		}
		if vm.MediaID != "" {
			for _, media := range managed.Media {
				if media.ID == vm.MediaID {
					used[media.StorageID] = true
				}
			}
		}
	}
	for _, media := range managed.Media {
		used[media.StorageID] = true
	}
	ids := []string{}
	for _, storage := range managed.Storage {
		if used[storage.ID] || (storage.StorageKind() != protocol.StorageKindNFS && storage.StorageKind() != protocol.StorageKindSMB) {
			continue
		}
		ids = append(ids, storage.ID)
	}
	return ids, true
}

// BeginPoolStorageMutation is the master-side fence for a remote repository source,
// credential or detach operation. Durable member reservations reject conflicting
// mutations; the marker then rejects new admissions until the outcome is definite.
func BeginPoolStorageMutation(ctx context.Context, s *Store, command protocol.Command) (func(bool), error) {
	if command.StorageLifecycle == nil || (command.Kind != "storage-update" && command.Kind != "storage-detach") {
		return func(bool) {}, nil
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return func(bool) {}, nil
	}
	id := command.StorageLifecycle.RepositoryID
	managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	remote := false
	for _, storage := range managed.Storage {
		if storage.ID == id && !storage.Tombstone && (storage.StorageKind() == protocol.StorageKindNFS || storage.StorageKind() == protocol.StorageKindSMB) {
			remote = true
			break
		}
	}
	if !remote {
		return func(bool) {}, nil
	}
	poolStorageMu.Lock()
	state, err := loadPoolStorage(s)
	if err == nil && state.Mutations[id] == command.OperationID {
		// A restarted master may safely replay its own retained operation; another
		// operation remains fenced until its outcome is made definite.
		poolStorageMu.Unlock()
		return func(definitive bool) {
			finishPoolStorageMutation(s, id, command.OperationID, definitive)
		}, nil
	}
	if err == nil {
		err = beginPoolStorageMutationState(&state, membership, s.State.HostID, id, command.OperationID)
	}
	if err == nil {
		err = savePoolStorage(s, state)
	}
	poolStorageMu.Unlock()
	if err != nil {
		return nil, err
	}
	return func(definitive bool) {
		finishPoolStorageMutation(s, id, command.OperationID, definitive)
	}, nil
}

func finishPoolStorageMutation(s *Store, id, operation string, definitive bool) {
	if definitive {
		clearPoolStorageMutation(s, id, operation)
	}
}

func clearPoolStorageMutation(s *Store, id, operation string) {
	poolStorageMu.Lock()
	defer poolStorageMu.Unlock()
	state, err := loadPoolStorage(s)
	if err == nil && state.Mutations[id] == operation {
		delete(state.Mutations, id)
		_ = savePoolStorage(s, state)
	}
}

func beginPoolStorageMutationState(state *poolStorageState, membership protocol.PoolMembership, hostID, id, operation string) error {
	if state.Mutations[id] != "" {
		return errors.New("pool repository mutation already pending")
	}
	if len(state.Reservations[id]) != 0 {
		return errors.New("pool repository is reserved by a member workload")
	}
	state.Mutations[id] = operation
	return nil
}

// recordPoolStorageReservation retains the first live operation for a member/repository.
// A later operation needs no second fence, but must never overwrite the first: failed
// cleanup of the later operation is then unable to release an active VM's protection.
func recordPoolStorageReservation(state *poolStorageState, id, hostID, operation string) bool {
	if state.Reservations[id] == nil {
		state.Reservations[id] = map[string]string{}
	}
	if state.Reservations[id][hostID] != "" {
		return false
	}
	state.Reservations[id][hostID] = operation
	return true
}

// ReconcilePoolStorageFromMaster is member pull transport for the master's durable desired
// projection. It discovers only already-authorized NFS/SMB repository IDs; the master still
// owns configuration, generations and outcomes, while the member owns its native mount state.
func ReconcilePoolStorageFromMaster(ctx context.Context, s *Store) error {
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		return nil
	}
	var result error
	defer func() { recordPoolReconcile(s, result) }()
	master, err := PoolMasterURL(s)
	if err != nil {
		result = err
		return result
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		result = err
		return result
	}
	defer c.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(master, "/")+strings.TrimSuffix(poolStoragePath, "/"), nil)
	if err != nil {
		result = err
		return result
	}
	resp, err := c.Do(req)
	if err != nil {
		result = err
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		result = errors.New("pool master repository list unavailable")
		return result
	}
	var storage []protocol.StorageRegistration
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&storage) != nil {
		result = errors.New("invalid pool master repository list")
		return result
	}
	var last error
	desired := map[string]bool{}
	for _, entry := range storage {
		desired[entry.ID] = true
		if entry.PoolID != s.State.PoolID {
			result = errors.New("pool master returned another pool repository")
			return result
		}
		if err := ReconcilePoolStorage(ctx, s, master, entry.ID); err != nil {
			last = err
		}
	}
	local, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	for _, entry := range local.Storage {
		if !desired[entry.ID] && (entry.StorageKind() == protocol.StorageKindNFS || entry.StorageKind() == protocol.StorageKindSMB) {
			result, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Execute(ctx, protocol.Command{OperationID: poolStorageOperation("detach", entry.ID, entry.Generation), Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: entry.ID, PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: entry.Generation}}, nil)
			if err != nil || result.Status != "succeeded" {
				last = errors.New("pool storage detach did not converge")
			}
		}
	}
	result = last
	return result
}

func recordPoolReconcile(s *Store, err error) {
	poolReconcileStatus.Lock()
	defer poolReconcileStatus.Unlock()
	poolReconcileStatus.byDir[s.dir] = poolReconcileObservation{at: time.Now().UTC(), err: err}
}
func PoolStorageReconcileCheck(s *Store) *protocol.Check {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID == s.State.HostID {
		return nil
	}
	poolReconcileStatus.Lock()
	observed, ok := poolReconcileStatus.byDir[s.dir]
	poolReconcileStatus.Unlock()
	if !ok {
		return &protocol.Check{Name: "pool-storage-reconciliation", Status: "unknown", Freshness: "unknown", ObservedAt: time.Now().UTC(), Error: "awaiting first member reconciliation"}
	}
	if time.Since(observed.at) > 2*time.Minute {
		return &protocol.Check{Name: "pool-storage-reconciliation", Status: "unknown", Freshness: "stale", ObservedAt: observed.at, Error: "pool storage reconciliation observation is stale"}
	}
	if observed.err != nil {
		return &protocol.Check{Name: "pool-storage-reconciliation", Status: "failed", Freshness: "fresh", ObservedAt: observed.at, Error: "pool storage reconciliation failed; inspect member Core"}
	}
	return &protocol.Check{Name: "pool-storage-reconciliation", Status: "healthy", Freshness: "fresh", ObservedAt: observed.at}
}
