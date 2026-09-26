// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolStoragePath = "/api/v1/pool/storage/"
const poolResourceMutationPrefix = "@resource/"

const (
	poolProbeModeChallengeByte = "challenge-byte-v1"
	poolProbeModeHeader        = "X-Nex-Pool-Probe-Mode"
	poolProbeLegacyMode        = "legacy-byte-0"
	// poolProbeLeaseLimit bounds only concurrent, challenge-addressed peer
	// probes. Each lease is ephemeral (ten seconds) and is never VM ownership.
	poolProbeLeaseLimit = 64
)

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
	at    time.Time
	err   error
	stage string
}

type poolReconcileStageError struct {
	stage string
	err   error
}

var poolReconcileStages = map[string]bool{
	"membership": true, "master-url": true, "peer-client": true, "master-list": true,
	"repository": true, "local-observation": true, "orphan-detach": true, "observation": true,
}

func (e *poolReconcileStageError) Error() string { return e.err.Error() }
func (e *poolReconcileStageError) Unwrap() error { return e.err }
func poolReconcileStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &poolReconcileStageError{stage: stage, err: err}
}
func poolReconcileErrorStage(err error) string {
	var staged *poolReconcileStageError
	if errors.As(err, &staged) && poolReconcileStages[staged.stage] {
		return staged.stage
	}
	return "observation"
}

func poolStorageEligibleForMember(storage protocol.StorageRegistration, hostID string) bool {
	if !protocol.IsPoolSharedStorage(storage) {
		return false
	}
	if !protocol.IsClusteredISCSI(storage) {
		return true
	}
	for _, eligible := range storage.Source.ISCSI.EligibleHostIDs {
		if eligible == hostID {
			return true
		}
	}
	return false
}

// Concurrent leases on one repository share one open description. On the SMB
// client/server stack, closing one description released another description's
// still-held OFD range (BUG-0173), so the description closes only after its
// last held range is unlocked.
var poolProbeLocks = struct {
	sync.Mutex
	files  map[string]poolProbeLock
	shared map[string]*poolProbeFile
}{files: map[string]poolProbeLock{}, shared: map[string]*poolProbeFile{}}

type poolProbeFile struct {
	file    *os.File
	offsets map[int64]bool
}

type poolProbeLock struct {
	path   string
	offset int64
}

// reconcilePoolStorage is the periodic, member-local reconciliation boundary.
// Cluster projection refresh runs independently of the repository child deadline;
// this path only uses its already durable Core record.
var reconcilePoolStorage = reconcilePoolStorageWithKnownCluster
var reconcileStorageClusters = ReconcileStorageClustersFromMaster
var validatePoolStorageAccess = vmhost.ValidatePoolStorageAccess
var probePoolStorageLock = crossCoreLockProbe

// reconcilePoolStorageLeaseSelection records the exact NFS repositories all ready
// members have proved accessible. The record is pool authority, never a client choice.
func reconcilePoolStorageLeaseSelection(s *Store) error {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return nil
	}
	ready := make([]protocol.PoolMember, 0, 2)
	for _, member := range membership.Members {
		if member.Status == "ready" {
			ready = append(ready, member)
		}
	}
	var selected *protocol.PoolStorageLeaseConfig
	if len(ready) == 2 && !membership.HostFencingDisabled {
		self, ok := poolMemberByID(membership, s.State.HostID)
		if !ok || requirePoolStorageLeaseReset(context.Background(), self) != nil {
			return setPoolStorageLeaseSelection(s, membership, nil)
		}
		registrations, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedStorage()
		if managedVMChecksReady(checks) {
			registrations = PoolStorageMemberAccess(s, registrations)
			for _, registration := range registrations {
				if protocol.IsClusteredISCSI(registration) {
					return setPoolStorageLeaseSelection(s, membership, nil)
				}
			}
			repositories := make([]protocol.PoolStorageLeaseRepository, 0)
			watchdogBound := 60
			for _, member := range ready {
				if member.Watchdog == nil || member.Watchdog.Kind != protocol.PoolWatchdogHardware || member.Watchdog.TimeoutSeconds <= 0 {
					repositories = nil
					break
				}
				if bound := member.Watchdog.TimeoutSeconds * 2; bound > watchdogBound {
					watchdogBound = bound
				}
			}
			if repositories != nil {
				for _, registration := range registrations {
					if registration.Tombstone || !protocol.IsPoolSharedStorage(registration) || registration.Kind != protocol.StorageKindNFS ||
						!poolNFSIdentity(registration.Identity) || !registration.Access.Readable || !registration.Access.Writable {
						continue
					}
					allReady := true
					for _, member := range ready {
						if member.HostID == s.State.HostID {
							continue
						}
						accessOK := false
						for _, access := range registration.MemberAccess {
							if access.HostID != member.HostID || access.RepositoryID != registration.ID || access.Generation != registration.Generation ||
								!access.Readable || !access.Writable || !access.QEMUWritable || !access.Locking || access.Identity != registration.Identity.String() {
								continue
							}
							accessOK = true
							break
						}
						allReady = allReady && accessOK
					}
					if allReady {
						repositories = append(repositories, protocol.PoolStorageLeaseRepository{
							RepositoryID: registration.ID, RepositoryGeneration: registration.Generation,
							RepositoryIdentity: registration.Identity,
							Lockspace:          protocol.PoolStorageLeaseLockspaceName(membership.PoolID, registration.ID),
						})
					}
				}
			}
			if len(repositories) != 0 {
				slices.SortFunc(repositories, func(a, b protocol.PoolStorageLeaseRepository) int {
					return strings.Compare(a.RepositoryID, b.RepositoryID)
				})
				selected = &protocol.PoolStorageLeaseConfig{
					Repositories: repositories, AuthorityRepositoryID: repositories[0].RepositoryID,
					IOTimeoutSeconds: 10, HostDeadSeconds: 60, FenceTimeoutSeconds: watchdogBound,
				}
			}
		}
	}
	return setPoolStorageLeaseSelection(s, membership, selected)
}

func poolNFSIdentity(identity protocol.StorageIdentity) bool {
	return identity.NFS != nil && identity.Local == nil && identity.ZFS == nil && identity.SMB == nil && identity.ISCSI == nil &&
		identity.NFS.Server != "" && identity.NFS.Export != "" && identity.NFS.FilesystemID != ""
}

func requirePoolStorageLeaseReset(ctx context.Context, member protocol.PoolMember) error {
	armed := member.WatchdogArmed
	if member.Watchdog == nil || armed == nil || !armed.Armed || armed.Kind != protocol.PoolWatchdogHardware ||
		armed.FencingGeneration != member.Generation || armed.Device != member.Watchdog.Device ||
		armed.TimeoutSeconds != member.Watchdog.TimeoutSeconds {
		return errors.New("pool storage-lease hardware watchdog is not armed at the current generation")
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, err := vmhost.CheckSanlockWatchdogReady(checkCtx, *member.Watchdog)
	if err != nil || !status.ResetVerified {
		return errors.New("pool storage-lease hardware reset is not independently verified")
	}
	return nil
}

// preparePoolStorageLeaseHost joins every exact configured lockspace before this host
// accepts a replica or uses a shared repository. Only the master initializes a missing
// lockspace file; members never create a parallel one.
func preparePoolStorageLeaseHost(ctx context.Context, s *Store, membership protocol.PoolMembership, initialize bool) (uint64, error) {
	lease := membership.StorageLease
	if lease == nil {
		return 0, nil
	}
	if err := lease.Valid(membership.PoolID); err != nil {
		return 0, errors.New("pool storage-lease configuration is invalid")
	}
	self, ok := poolMemberByID(membership, s.State.HostID)
	if !ok || self.Status != "ready" || self.SanlockHostID == 0 || (initialize && membership.MasterHostID != s.State.HostID) {
		return 0, errors.New("this host is not authorized to prepare the pool storage lease")
	}
	if err := requirePoolStorageLeaseReset(ctx, self); err != nil {
		return 0, err
	}
	ids := make([]uint64, 0, len(membership.Members))
	for _, member := range membership.Members {
		if member.Status == "ready" {
			if member.SanlockHostID == 0 {
				return 0, errors.New("a ready pool member has no sanlock host ID")
			}
			ids = append(ids, member.SanlockHostID)
		}
	}
	slices.Sort(ids)
	registrations, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedStorage()
	if !managedVMChecksReady(checks) {
		return 0, errors.New("managed storage state is unavailable")
	}
	byID := make(map[string]protocol.StorageRegistration, len(registrations))
	for _, storage := range registrations {
		byID[storage.ID] = storage
	}
	var authorityGeneration uint64
	for _, repository := range lease.Repositories {
		storage, ok := byID[repository.RepositoryID]
		if !ok || storage.Tombstone || storage.Generation != repository.RepositoryGeneration ||
			storage.Identity.String() != repository.RepositoryIdentity.String() || !poolNFSIdentity(storage.Identity) || !protocol.IsPoolSharedStorage(storage) {
			return 0, errors.New("a configured pool lease repository does not match this host's applied NFS registration")
		}
		storage, err := vmhost.ValidatePoolStorageAccess(ctx, storage)
		if err != nil || storage.Identity.String() != repository.RepositoryIdentity.String() {
			return 0, errors.New("a configured pool lease repository is not mounted with the recorded NFS identity")
		}
		path, err := vmhost.SanlockLockspacePath(storage.Path)
		if err != nil {
			return 0, errors.New("a configured pool lease repository has no safe lease path")
		}
		create := false
		if initialize {
			_, statErr := os.Lstat(path)
			if os.IsNotExist(statErr) {
				create = true
			} else if statErr != nil {
				return 0, errors.New("pool lease file state is unavailable")
			}
		}
		status, err := vmhost.EnsureSanlockLockspace(ctx, vmhost.SanlockLockspaceConfig{
			Lockspace: repository.Lockspace, LeasePath: path, LocalHostID: self.SanlockHostID,
			HostIDs: ids, Initialize: create, IsMaster: membership.MasterHostID == s.State.HostID,
			IOTimeout: uint32(lease.IOTimeoutSeconds),
		})
		if err != nil || status.HostID != self.SanlockHostID || status.HostState != "HOST_LIVE" {
			return 0, errors.New("this host has not joined every configured sanlock lockspace")
		}
		if repository.RepositoryID == lease.AuthorityRepositoryID {
			authorityGeneration = status.Generation
		}
	}
	if authorityGeneration == 0 {
		return 0, errors.New("authority sanlock host generation is unavailable")
	}
	return authorityGeneration, nil
}

func setPoolStorageLeaseSelection(s *Store, observed protocol.PoolMembership, selected *protocol.PoolStorageLeaseConfig) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Generation != observed.Generation || state.Membership.MasterHostID != s.State.HostID {
		return errors.New("pool membership changed while selecting the storage lease")
	}
	if state.Membership.StorageLease != nil && selected != nil {
		unprepared := *state.Membership.StorageLease
		unprepared.PreparedGeneration = 0
		if reflect.DeepEqual(&unprepared, selected) {
			return nil
		}
	} else if state.Membership.StorageLease == selected {
		return nil
	}
	if err = poolAuthorityWritable(state); err != nil {
		return err
	}
	if selected != nil {
		selected.PreparedGeneration = 0
	}
	state.Membership.StorageLease = selected
	return savePoolPeer(s, state)
}

// F_OFD_SETLK is the Linux open-file-description byte-range lock command.
// QEMU uses OFD/POSIX image locks; unlike flock, OFD locks participate in the
// same kernel lock family and remain associated with this open description.
// Unsupported filesystems or kernels are rejected rather than downgraded.
const fOFDSetLK = 37

func poolProbeOFDLockAt(f *os.File, lockType int16, offset int64) error {
	if offset < 0 {
		return errors.New("invalid shared-storage probe offset")
	}
	return syscall.FcntlFlock(f.Fd(), fOFDSetLK, &syscall.Flock_t{Type: lockType, Whence: int16(0), Start: offset, Len: 1})
}

func poolProbeOffset(challenge string) (int64, error) {
	b, err := hex.DecodeString(challenge)
	if err != nil || len(b) != 16 {
		return 0, errors.New("invalid shared-storage probe challenge")
	}
	digest := sha256.Sum256(b)
	// Byte zero remains the legacy rolling-upgrade range. A collision between two
	// challenge hashes can only fail a concurrent probe; it cannot report health.
	return int64(binary.BigEndian.Uint64(digest[:8])&uint64((1<<63)-2)) + 1, nil
}

func poolProbeModeOffset(challenge, mode string) (string, int64, error) {
	if mode == "" {
		if _, err := poolProbeOffset(challenge); err != nil {
			return "", 0, err
		}
		return poolProbeLegacyMode, 0, nil
	}
	if mode != poolProbeModeChallengeByte {
		return "", 0, errors.New("unsupported shared-storage probe mode")
	}
	offset, err := poolProbeOffset(challenge)
	if err != nil {
		return "", 0, err
	}
	return mode, offset, nil
}

func poolProbeLockKey(hostID, repositoryID, generation, challenge, mode string) string {
	return hostID + "\x00" + repositoryID + "\x00" + generation + "\x00" + challenge + "\x00" + mode
}

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
	// Member access is a process-local observation. Older files may contain
	// health reports from a prior agent process; never rehydrate those as current.
	state.Access = map[string]map[string]protocol.PoolStorageAccess{}
	if state.Reservations == nil {
		state.Reservations = map[string]map[string]string{}
	}
	if state.Mutations == nil {
		state.Mutations = map[string]string{}
	}
	return state, nil
}

func poolStorageAccess(s *Store) map[string]map[string]protocol.PoolStorageAccess {
	if s.poolStorageAccess == nil {
		s.poolStorageAccess = map[string]map[string]protocol.PoolStorageAccess{}
	}
	return s.poolStorageAccess
}

func PoolStorageMemberAccess(s *Store, storage []protocol.StorageRegistration) []protocol.StorageRegistration {
	poolStorageMu.Lock()
	defer poolStorageMu.Unlock()
	accesses := poolStorageAccess(s)
	if _, err := loadPoolStorage(s); err != nil {
		// A corrupt durable reservation/mutation record must not be masked by a
		// prior process-local healthy observation.
		s.poolStorageAccess = map[string]map[string]protocol.PoolStorageAccess{}
		accesses = s.poolStorageAccess
	}
	membership, _ := Membership(s)
	out := append([]protocol.StorageRegistration(nil), storage...)
	for i := range out {
		// Member access is this process's observation, never a journaled or
		// caller-provided health cache. The copy above keeps the caller untouched.
		out[i].MemberAccess = nil
		if protocol.IsPoolSharedStorage(out[i]) {
			for _, member := range membership.Members {
				if member.HostID != s.State.HostID && member.Status == "ready" && poolStorageEligibleForMember(out[i], member.HostID) && accesses[out[i].ID][member.HostID].HostID == "" {
					out[i].MemberAccess = append(out[i].MemberAccess, protocol.PoolStorageAccess{RepositoryID: out[i].ID, HostID: member.HostID, Generation: out[i].Generation, Error: "awaiting member reconciliation"})
				}
			}
		}
		for _, access := range accesses[out[i].ID] {
			out[i].MemberAccess = append(out[i].MemberAccess, access)
		}
	}
	return out
}
func savePoolStorage(s *Store, state poolStorageState) error {
	// Access observations are not durable state: a restarted Core must wait for
	// another authenticated native observation rather than reuse an old healthy one.
	state.Access = map[string]map[string]protocol.PoolStorageAccess{}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(poolStorageFile(s), b, 0600)
}

// poolPeerStorageList is the master's durable repository set for peers. It fails closed
// when this host cannot read its own managed journal whole, exactly as the network and ISO
// peer lists already do: a member reads absence from these lists as removal, so a short
// list would detach a repository that is still registered. Owner decision D4, 2026-09-19.
func poolPeerStorageList(s *Store) ([]protocol.StorageRegistration, error) {
	storage, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedStorage()
	if !managedVMChecksReady(checks) {
		return nil, vmhost.ErrManagedJournalIncomplete
	}
	return storage, nil
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
				if storage.ID == id && !storage.Tombstone && storage.Generation == request.Generation && poolStorageEligibleForMember(storage, member.HostID) {
					valid = true
					break
				}
			}
			if !valid {
				http.Error(w, "repository generation unavailable", http.StatusConflict)
				return
			}
			poolVMMu.Lock()
			owners, ownerErr := loadPoolVMs(s)
			if ownerErr == nil {
				for _, vm := range owners.VMs {
					if poolVMRecoveryUsesStorage(vm, id) {
						ownerErr = errors.New("repository has a VM snapshot recovery operation pending")
						break
					}
				}
			}
			if ownerErr != nil {
				poolVMMu.Unlock()
				http.Error(w, "reservation refused", http.StatusConflict)
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
			poolVMMu.Unlock()
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
			storage, storageErr := poolPeerStorageList(s)
			if storageErr != nil {
				http.Error(w, poolJournalIncomplete, http.StatusServiceUnavailable)
				return
			}
			valid := false
			for _, entry := range storage {
				if entry.ID == id && !entry.Tombstone && entry.Generation == access.Generation && poolStorageEligibleForMember(entry, member.HostID) {
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
			_, e := loadPoolStorage(s)
			if e == nil {
				accesses := poolStorageAccess(s)
				if accesses[id] == nil {
					accesses[id] = map[string]protocol.PoolStorageAccess{}
				}
				accesses[id][member.HostID] = access
			} else {
				s.poolStorageAccess = map[string]map[string]protocol.PoolStorageAccess{}
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
			generation, challenge, requestedMode := r.URL.Query().Get("generation"), r.URL.Query().Get("challenge"), r.URL.Query().Get("probe_mode")
			mode, offset, modeErr := poolProbeModeOffset(challenge, requestedMode)
			if id == "" || generation == "" || modeErr != nil {
				http.NotFound(w, r)
				return
			}
			lockKey := poolProbeLockKey(member.HostID, id, generation, challenge, mode)
			if r.Method == http.MethodDelete {
				releasePoolProbeLock(lockKey)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			storage, storageErr := poolPeerStorageList(s)
			if storageErr != nil {
				http.Error(w, poolJournalIncomplete, http.StatusServiceUnavailable)
				return
			}
			for _, entry := range storage {
				if entry.ID == id && poolStorageEligibleForMember(entry, member.HostID) {
					if strconv.FormatUint(entry.Generation, 10) != generation {
						http.Error(w, "storage generation changed", http.StatusConflict)
						return
					}
					if err := holdPoolProbeLock(lockKey, entry.Path, offset); err != nil {
						http.Error(w, "lock unavailable", http.StatusConflict)
						return
					}
					if mode == poolProbeModeChallengeByte {
						w.Header().Set(poolProbeModeHeader, mode)
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == strings.TrimSuffix(poolStoragePath, "/") {
			storage, storageErr := poolPeerStorageList(s)
			if storageErr != nil {
				http.Error(w, poolJournalIncomplete, http.StatusServiceUnavailable)
				return
			}
			out := []protocol.StorageRegistration{}
			for _, entry := range storage {
				if !entry.Tombstone && poolStorageEligibleForMember(entry, member.HostID) {
					entry.MemberAccess = nil
					entry.CredentialReceipt = ""
					out = append(out, entry)
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
		storage, storageErr := poolPeerStorageList(s)
		if storageErr != nil {
			http.Error(w, poolJournalIncomplete, http.StatusServiceUnavailable)
			return
		}
		for _, entry := range storage {
			if entry.ID == id && !entry.Tombstone {
				if !poolStorageEligibleForMember(entry, member.HostID) {
					http.Error(w, "repository is not pool-shareable", http.StatusConflict)
					return
				}
				transfer := poolStorageTransfer{Storage: entry}
				transfer.Storage.MemberAccess = nil
				if entry.StorageKind() == protocol.StorageKindSMB {
					c, e := vmhost.PoolStorageCredential(s.dir, entry.CredentialReceipt)
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
	return reconcilePoolStorageWithCluster(ctx, s, masterURL, repositoryID, true)
}

// reconcilePoolStorageWithKnownCluster keeps the periodic repository probe bounded
// to repository work. The periodic cluster reconciler owns its separate durable
// projection refresh; repeating that full native setup here consumed this probe's
// deadline without adding a safety check.
func reconcilePoolStorageWithKnownCluster(ctx context.Context, s *Store, masterURL, repositoryID string) error {
	return reconcilePoolStorageWithCluster(ctx, s, masterURL, repositoryID, false)
}

func reconcilePoolStorageWithCluster(ctx context.Context, s *Store, masterURL, repositoryID string, refreshCluster bool) error {
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
	if storage.ID != repositoryID || storage.PoolID != s.State.PoolID || !poolStorageEligibleForMember(storage, s.State.HostID) {
		return errors.New("pool storage response does not match membership")
	}
	storage.HostID = s.State.HostID
	expectedIdentity := storage.Identity
	executionCtx := vmhost.WithPoolStorageExpectation(ctx, expectedIdentity, storage.Generation)
	if protocol.IsClusteredISCSI(storage) {
		// Explicit reconciliation refreshes the durable cluster projection first;
		// periodic reconciliation uses that same record. A missing or unhealthy
		// profile is never a reason to use ordinary exclusive-iSCSI preparation.
		if refreshCluster {
			if err := reconcileStorageClusters(ctx, s); err != nil {
				return err
			}
		}
		config, err := StorageClusterConfigForRepository(s, storage.ID)
		if err != nil || config.ValidFor(storage) != nil {
			return errors.New("clustered iSCSI configuration is unavailable or does not match repository")
		}
		executionCtx = vmhost.WithStorageClusterConfig(executionCtx, config)
		executionCtx = vmhost.WithStorageClusterStateDir(executionCtx, s.dir)
	}
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
	applied, _ := engine.AppliedStorage()
	operation := poolStorageOperation("apply", storage.ID, storage.Generation)
	command := protocol.Command{OperationID: operation, Kind: "storage-register", Storage: &storage}
	alreadyCurrent := false
	for _, current := range applied {
		if current.ID == storage.ID && !current.Tombstone {
			if current.Generation == storage.Generation {
				storage, err = validatePoolStorageAccess(executionCtx, current)
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
		membership, membershipErr := Membership(s)
		if membershipErr != nil {
			return membershipErr
		}
		reapply, reapplyErr := engine.EjectCleanupReapplyAvailable(command, s.State.PoolID, s.State.HostID, membership.Generation)
		if reapplyErr != nil {
			return poolConvergeRefusalf("pool repository %s cannot be reapplied: %s", storage.ID, reapplyErr)
		}
		var result protocol.Result
		if reapply {
			result, err = engine.ExecuteEjectCleanupReapply(executionCtx, command, s.State.PoolID, s.State.HostID, membership.Generation)
		} else {
			result, err = engine.Execute(executionCtx, command, nil)
		}
		if err != nil || result.Status != "succeeded" {
			if err == nil {
				err = errors.New(result.Error)
			}
			_ = postPoolStorageAccess(ctx, s, masterURL, repositoryID, protocol.PoolStorageAccess{RepositoryID: repositoryID, HostID: s.State.HostID, Generation: storage.Generation, Error: err.Error()})
			return err
		}
		applied, _ = engine.AppliedStorage()
		for _, candidate := range applied {
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
		return reconcilePoolStorageWithCluster(ctx, s, masterURL, repositoryID, refreshCluster)
	}
	if storage.Generation > transfer.Storage.Generation {
		return errors.New("member repository generation is newer than the master")
	}
	if err = probePoolStorageLock(ctx, s, masterURL, storage); err != nil {
		_ = postPoolStorageAccess(ctx, s, masterURL, repositoryID, protocol.PoolStorageAccess{RepositoryID: repositoryID, HostID: s.State.HostID, Generation: storage.Generation, Error: err.Error()})
		return err
	}
	identity, _ := json.Marshal(storage.Identity)
	return postPoolStorageAccess(ctx, s, masterURL, repositoryID, protocol.PoolStorageAccess{RepositoryID: repositoryID, HostID: s.State.HostID, Generation: storage.Generation, Identity: string(identity), Readable: true, Writable: true, QEMUWritable: true, Locking: true, ObservedAt: storage.Access.ObservedAt})
}

func poolMemberStorageIntent(storage protocol.StorageRegistration) protocol.StorageRegistration {
	storage.Path, storage.Identity, storage.Capabilities, storage.Access = "", protocol.StorageIdentity{}, protocol.StorageCapabilities{}, protocol.StorageAccess{}
	if storage.Source.ISCSI != nil {
		// The master reports its resolved mount path, but member import is still a
		// client-shaped command. The member's ManagedEngine restores its own
		// deterministic Core-owned path before native cluster preparation.
		source := *storage.Source.ISCSI
		source.MountPath = ""
		storage.Source.ISCSI = &source
	}
	storage.Status, storage.Error, storage.OperationID = "", "", ""
	return storage
}

func poolStorageOperation(action, id string, generation uint64) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(action+"\x00"+id+"\x00"+strconv.FormatUint(generation, 10)+"\x00"+strconv.FormatInt(time.Now().UnixNano(), 10))))
}

func holdPoolProbeLock(id, path string, offset int64) error {
	poolProbeLocks.Lock()
	defer poolProbeLocks.Unlock()
	if _, held := poolProbeLocks.files[id]; held {
		return nil
	}
	if len(poolProbeLocks.files) >= poolProbeLeaseLimit {
		return errors.New("too many active shared-storage probe leases")
	}
	name := filepath.Join(path, ".nex-peer-lock")
	shared := poolProbeLocks.shared[name]
	if shared != nil && shared.offsets[offset] {
		// One description re-locking its own range would succeed silently; separate
		// leases must never share a held range.
		return errors.New("shared-storage probe range is already leased")
	}
	if shared == nil {
		f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		shared = &poolProbeFile{file: f, offsets: map[int64]bool{}}
	}
	if err := poolProbeOFDLockAt(shared.file, syscall.F_WRLCK, offset); err != nil {
		if len(shared.offsets) == 0 {
			shared.file.Close()
		}
		return fmt.Errorf("shared-storage OFD locking is unavailable: %w", err)
	}
	shared.offsets[offset] = true
	poolProbeLocks.shared[name] = shared
	poolProbeLocks.files[id] = poolProbeLock{path: name, offset: offset}
	// A peer crash or lost DELETE must not leave a probe lock as a denial of service.
	// The member's conflicting probe runs immediately; this bounded lease is not a VM fence.
	time.AfterFunc(10*time.Second, func() { releasePoolProbeLock(id) })
	return nil
}
func releasePoolProbeLock(id string) {
	poolProbeLocks.Lock()
	defer poolProbeLocks.Unlock()
	lock, held := poolProbeLocks.files[id]
	if !held {
		return
	}
	delete(poolProbeLocks.files, id)
	shared := poolProbeLocks.shared[lock.path]
	_ = poolProbeOFDLockAt(shared.file, syscall.F_UNLCK, lock.offset)
	delete(shared.offsets, lock.offset)
	if len(shared.offsets) == 0 {
		_ = shared.file.Close()
		delete(poolProbeLocks.shared, lock.path)
	}
}

func poolProbeExpectedExclusion(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES)
}

func releasePoolProbeLease(c *http.Client, lockURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release, err := http.NewRequestWithContext(ctx, http.MethodDelete, lockURL, nil)
	if err != nil {
		return err
	}
	released, err := c.Do(release)
	if err != nil {
		return err
	}
	defer released.Body.Close()
	if released.StatusCode != http.StatusNoContent {
		return errors.New("master could not release shared-storage lock")
	}
	return nil
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
	challenge := hex.EncodeToString(challengeBytes)
	lockURL := strings.TrimRight(masterURL, "/") + poolStoragePath + storage.ID + "/lock?generation=" + strconv.FormatUint(storage.Generation, 10) + "&challenge=" + challenge + "&probe_mode=" + poolProbeModeChallengeByte
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, lockURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	mode := resp.Header.Get(poolProbeModeHeader)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("master could not hold shared-storage lock")
	}
	offset := int64(0)
	switch mode {
	case "": // An older master held the legacy byte-zero lock.
	case poolProbeModeChallengeByte:
		if offset, err = poolProbeOffset(challenge); err != nil {
			_ = releasePoolProbeLease(c, lockURL)
			return err
		}
	default:
		_ = releasePoolProbeLease(c, lockURL)
		return errors.New("master returned an unsupported shared-storage probe mode")
	}
	leased := true
	defer func() {
		if leased {
			_ = releasePoolProbeLease(c, lockURL)
		}
	}()
	f, err := os.OpenFile(filepath.Join(storage.Path, ".nex-peer-lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = poolProbeOFDLockAt(f, syscall.F_WRLCK, offset); err == nil {
		_ = poolProbeOFDLockAt(f, syscall.F_UNLCK, offset)
		return errors.New("shared storage OFD lock is not exclusive across Cores")
	}
	if !poolProbeExpectedExclusion(err) {
		return fmt.Errorf("shared storage OFD lock probe failed: %w", err)
	}
	if err = releasePoolProbeLease(c, lockURL); err != nil {
		return errors.New("cannot release master shared-storage lock")
	}
	leased = false
	if err = poolProbeOFDLockAt(f, syscall.F_WRLCK, offset); err != nil {
		return errors.New("shared storage OFD lock did not recover after master release")
	}
	_ = poolProbeOFDLockAt(f, syscall.F_UNLCK, offset)
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
			if storage.ID == id && poolStorageEligibleForMember(storage, s.State.HostID) {
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
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	managed, checks := engine.Applied(ctx)
	ids, safe := poolStorageUnusedIDs(managed, checks, engine.MediaAdoptedHere)
	if !safe {
		return
	}
	releasePoolStorageIDs(ctx, s, master, ids, "", true)
}

func poolStorageUnusedIDs(managed *protocol.ManagedState, checks []protocol.Check, adopted func(string) bool) ([]string, bool) {
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
		for _, mediaID := range []string{vm.MediaID, vm.DriversMediaID} {
			if mediaID == "" {
				continue
			}
			for _, media := range managed.Media {
				if media.ID == mediaID {
					used[media.StorageID] = true
				}
			}
		}
	}
	for _, media := range managed.Media {
		if adopted != nil && adopted(media.ID) {
			continue
		}
		used[media.StorageID] = true
	}
	ids := []string{}
	for _, storage := range managed.Storage {
		if used[storage.ID] || !poolStorageEligibleForMember(storage, storage.HostID) {
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
	var current *protocol.StorageRegistration
	for _, storage := range managed.Storage {
		if storage.ID == id && !storage.Tombstone && protocol.IsPoolSharedStorage(storage) {
			remote = true
			copy := storage
			current = &copy
			break
		}
	}
	if !remote {
		return func(bool) {}, nil
	}
	sourceMutation := command.Kind == "storage-detach" || current == nil || command.StorageLifecycle.Registration == nil ||
		current.Path != command.StorageLifecycle.Registration.Path || current.Kind != command.StorageLifecycle.Registration.Kind ||
		!reflect.DeepEqual(current.Source, command.StorageLifecycle.Registration.Source) ||
		!reflect.DeepEqual(current.Identity, command.StorageLifecycle.Registration.Identity)
	poolVMMu.Lock()
	owners, ownerErr := loadPoolVMs(s)
	if ownerErr != nil {
		poolVMMu.Unlock()
		return nil, errors.New("pool VM recovery state is unavailable")
	}
	for _, vm := range owners.VMs {
		if poolVMRecoveryUsesStorage(vm, id) {
			poolVMMu.Unlock()
			return nil, errors.New("pool repository has a VM snapshot recovery operation pending")
		}
		if sourceMutation && poolVMSnapshotUsesResource(vm, "storage", id) {
			poolVMMu.Unlock()
			return nil, errors.New("pool repository is required by a retained VM snapshot")
		}
	}
	poolStorageMu.Lock()
	state, err := loadPoolStorage(s)
	if err == nil && state.Mutations[id] == command.OperationID {
		// A restarted master may safely replay its own retained operation; another
		// operation remains fenced until its outcome is made definite.
		poolStorageMu.Unlock()
		poolVMMu.Unlock()
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
	poolVMMu.Unlock()
	if err != nil {
		return nil, err
	}
	return func(definitive bool) {
		finishPoolStorageMutation(s, id, command.OperationID, definitive)
	}, nil
}

func poolVMRecoveryUsesStorage(vm poolVMRecord, repositoryID string) bool {
	if vm.PendingRecovery == nil || repositoryID == "" {
		return false
	}
	for _, disk := range vm.Spec.Disks {
		if disk.StorageID == repositoryID {
			return true
		}
	}
	return false
}

func finishPoolStorageMutation(s *Store, id, operation string, definitive bool) {
	if definitive {
		clearPoolStorageMutation(s, id, operation)
	}
}

func poolResourceMutationKey(resource, id string) string {
	return poolResourceMutationPrefix + resource + "/" + id
}

func poolResourceMutationPending(state poolStorageState) bool {
	for key := range state.Mutations {
		if strings.HasPrefix(key, poolResourceMutationPrefix) {
			return true
		}
	}
	return false
}

func poolResourceMutationForSpec(state poolStorageState, spec protocol.VMCreateSpec) string {
	for _, nic := range spec.NICs {
		if operation := state.Mutations[poolResourceMutationKey("network", nic.NetworkID)]; operation != "" {
			return operation
		}
	}
	for _, id := range []string{spec.MediaID, spec.DriversMediaID} {
		if id != "" {
			if operation := state.Mutations[poolResourceMutationKey("media", id)]; operation != "" {
				return operation
			}
		}
	}
	return ""
}

func poolResourceMutationRefusal(s *Store, spec protocol.VMCreateSpec) error {
	poolStorageMu.Lock()
	defer poolStorageMu.Unlock()
	state, err := loadPoolStorage(s)
	if err != nil {
		return errors.New("pool resource mutation state is unavailable")
	}
	if poolResourceMutationForSpec(state, spec) != "" {
		return errors.New("VM definition uses a network or media resource with removal pending")
	}
	return nil
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
// projection. It discovers only already-authorized pool-shareable repository IDs; the master
// owns configuration, generations and outcomes, while the member owns its native mount state.
// Direct calls retain the normal independent storage probe deadline per repository.
func ReconcilePoolStorageFromMaster(ctx context.Context, s *Store) error {
	return reconcilePoolStorageFromMaster(ctx, s, 3*time.Second)
}

// reconcilePoolStorageFromMaster reconciles one master list without allowing a
// slow earlier repository to poison the deadline of a later one. A native
// filesystem call can still be uninterruptible after its own deadline; the
// caller remains single-flight in that case rather than starting another pass.
func reconcilePoolStorageFromMaster(ctx context.Context, s *Store, perRepositoryTimeout time.Duration) error {
	if perRepositoryTimeout <= 0 {
		return errors.New("pool storage reconciliation timeout is required")
	}
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
		result = poolReconcileStage("master-url", err)
		return result
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		result = poolReconcileStage("peer-client", err)
		return result
	}
	defer c.CloseIdleConnections()
	listCtx, cancelList := context.WithTimeout(ctx, perRepositoryTimeout)
	defer cancelList()
	req, err := http.NewRequestWithContext(listCtx, http.MethodGet, strings.TrimRight(master, "/")+strings.TrimSuffix(poolStoragePath, "/"), nil)
	if err != nil {
		result = poolReconcileStage("master-list", err)
		return result
	}
	resp, err := c.Do(req)
	if err != nil {
		result = poolReconcileStage("master-list", err)
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		result = poolReconcileStage("master-list", errors.New("pool master repository list unavailable"))
		return result
	}
	var storage []protocol.StorageRegistration
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&storage) != nil {
		result = poolReconcileStage("master-list", errors.New("invalid pool master repository list"))
		return result
	}
	if err := listCtx.Err(); err != nil {
		result = poolReconcileStage("master-list", err)
		return result
	}
	cancelList()
	for _, entry := range storage {
		if entry.PoolID != s.State.PoolID {
			result = poolReconcileStage("master-list", errors.New("pool master returned another pool repository"))
			return result
		}
	}
	desired, last := reconcilePoolStorageEntries(ctx, s, master, storage, perRepositoryTimeout)
	if err := ctx.Err(); err != nil {
		result = poolReconcileStage("repository", err)
		return result
	}
	localCtx, cancelLocal := context.WithTimeout(ctx, perRepositoryTimeout)
	local, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(localCtx)
	if err := localCtx.Err(); err != nil {
		cancelLocal()
		result = poolReconcileStage("local-observation", err)
		return result
	}
	cancelLocal()
	if err := ctx.Err(); err != nil {
		result = poolReconcileStage("local-observation", err)
		return result
	}
	for _, entry := range local.Storage {
		if err := ctx.Err(); err != nil {
			result = poolReconcileStage("orphan-detach", err)
			return result
		}
		if !desired[entry.ID] && protocol.IsPoolSharedStorage(entry) {
			engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
			// Precheck before Execute, exactly as the network and media orphan loops do: a
			// repository a local VM disk or an imported ISO still holds would otherwise mint
			// a journal entry on every probe tick and reach the retained-entry cap in a day.
			if err := engine.CheckStorageDetach(ctx, entry.ID); err != nil {
				last = poolReconcileStage("orphan-detach", poolConvergeRefusalf("pool repository %s cannot be detached on this host: %s", entry.ID, err))
				continue
			}
			work, cancel := context.WithTimeout(ctx, perRepositoryTimeout)
			// Deterministic within the UTC hour: an unexpected native failure replays one
			// stored entry instead of writing a new file on every probe tick, and retries
			// for real next hour.
			detachResult, detachErr := engine.Execute(work, protocol.Command{OperationID: poolConvergeOperation("storage-detach", entry.ID, entry.Generation), Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: entry.ID, PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: entry.Generation}}, nil)
			deadlineErr := work.Err()
			cancel()
			if deadlineErr != nil {
				last = poolReconcileStage("orphan-detach", deadlineErr)
				if ctx.Err() != nil {
					result = poolReconcileStage("orphan-detach", deadlineErr)
					return result
				}
				continue
			}
			if detachErr != nil || detachResult.Status != "succeeded" {
				last = poolReconcileStage("orphan-detach", errors.New("pool storage detach did not converge"))
			}
		}
	}
	result = last
	return result
}

func reconcilePoolStorageEntries(ctx context.Context, s *Store, master string, storage []protocol.StorageRegistration, perRepositoryTimeout time.Duration) (map[string]bool, error) {
	desired := map[string]bool{}
	var last error
	for _, entry := range storage {
		if err := ctx.Err(); err != nil {
			return desired, err
		}
		desired[entry.ID] = true
		work, cancel := context.WithTimeout(ctx, perRepositoryTimeout)
		err := reconcilePoolStorage(work, s, master, entry.ID)
		deadlineErr := work.Err()
		cancel()
		if deadlineErr != nil {
			last = poolReconcileStage("repository", deadlineErr)
			continue
		}
		if err != nil {
			last = poolReconcileStage("repository", err)
		}
	}
	return desired, last
}

func recordPoolReconcile(s *Store, err error) {
	poolReconcileStatus.Lock()
	defer poolReconcileStatus.Unlock()
	poolReconcileStatus.byDir[s.dir] = poolReconcileObservation{at: time.Now().UTC(), err: err, stage: poolReconcileErrorStage(err)}
}
func PoolStorageReconcileCheck(s *Store) *protocol.Check {
	membership, err := Membership(s)
	if err != nil {
		return nil
	}
	if membership.MasterHostID == s.State.HostID {
		return &protocol.Check{Name: "pool-storage-reconciliation", Status: "healthy", Freshness: "fresh", ObservedAt: time.Now().UTC()}
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
		outcome := "failed"
		if errors.Is(observed.err, context.DeadlineExceeded) {
			outcome = "deadline"
		}
		if errors.Is(observed.err, context.Canceled) {
			outcome = "cancelled"
		}
		return &protocol.Check{Name: "pool-storage-reconciliation", Status: "failed", Freshness: "fresh", ObservedAt: observed.at, Error: "pool storage reconciliation " + observed.stage + " " + outcome}
	}
	return &protocol.Check{Name: "pool-storage-reconciliation", Status: "healthy", Freshness: "fresh", ObservedAt: observed.at}
}
