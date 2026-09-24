// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"path/filepath"
	"strings"
	"time"
)

const poolJoinPath = "/api/v1/pool/join"

type peerOffer struct{ URL, CAPEM, PoolID, PoolName, MasterHostID, MemberHostID, CSRHash, Token string }
type peerJoinRequest struct{ Offer, HostID, CSRPEM string }
type peerJoinResponse struct {
	CertificatePEM string                  `json:"certificate_pem"`
	Membership     protocol.PoolMembership `json:"membership"`
}

// initPoolMaster makes this Core the ready master of its own pool. A Core that has not
// joined another pool is always its pool's master; there is no separate standalone mode.
func initPoolMaster(s *Store, state *poolPeerState) error {
	var err error
	state.CAPEM, state.CAKeyPEM, state.CertificatePEM, state.KeyPEM, err = newPoolCA(s.State.HostID)
	if err != nil {
		return err
	}
	hash, err := certificateHash(state.CertificatePEM)
	if err != nil {
		return err
	}
	state.Membership = protocol.PoolMembership{PoolID: s.State.PoolID, MasterHostID: s.State.HostID, Generation: 1, NextSanlockHostID: 2, Members: []protocol.PoolMember{{HostID: s.State.HostID, SanlockHostID: 1, CertificateSHA256: hash, Generation: 1, Status: "ready", ObservedAt: time.Now().UTC()}}}
	return nil
}

// EnsurePoolMembership records an enrolled Core that has not joined another pool as the
// master of its own single-host pool. Core startup calls it before any pool work runs.
func EnsurePoolMembership(s *Store) error {
	if s.State.HostID == "" || s.State.PoolID == "" {
		return nil
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.PoolID != "" {
		return err
	}
	if err = initPoolMaster(s, &state); err != nil {
		return err
	}
	return savePoolPeer(s, state)
}

// CreatePoolJoinOffer initializes a master-owned pool CA on first use and returns the
// CA and a short-lived, single-use offer. masterURL must be the bare HTTPS peer origin.
func CreatePoolJoinOffer(s *Store, masterURL, memberHostID, memberCSRPEM string) (poolCAPEM, offer string, err error) {
	u, err := peerOrigin(masterURL)
	if err != nil {
		return "", "", err
	}
	if s.State.HostID == "" || s.State.PoolID == "" {
		return "", "", errors.New("pool master identity is unavailable")
	}
	if memberHostID == "" || len(memberHostID) > 128 {
		return "", "", errors.New("member host identity is required")
	}
	if _, err = parsePeerCSR(memberCSRPEM, memberHostID); err != nil {
		return "", "", err
	}
	csrHash := fmt.Sprintf("%x", sha256.Sum256([]byte(memberCSRPEM)))
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return "", "", err
	}
	if state.Membership.PoolID == "" {
		if err = initPoolMaster(s, &state); err != nil {
			return "", "", err
		}
	} else if state.Membership.PoolID != s.State.PoolID || state.Membership.MasterHostID != s.State.HostID || state.CAKeyPEM == "" {
		return "", "", errors.New("pool peer state does not match this master")
	}
	state.MasterURL = u.String()
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	digest := offerDigest(token, state.Membership.PoolID, state.Membership.MasterHostID, memberHostID, csrHash)
	for id, old := range state.Offers {
		if time.Now().After(old.Expires) {
			delete(state.Offers, id)
		}
	}
	state.Offers[digest] = poolJoinOffer{Digest: digest, PoolID: state.Membership.PoolID, Master: state.Membership.MasterHostID, HostID: memberHostID, CSRHash: csrHash, Expires: time.Now().Add(15 * time.Minute).UTC()}
	if err = savePoolPeer(s, state); err != nil {
		return "", "", err
	}
	b, err := json.Marshal(peerOffer{URL: u.String(), CAPEM: state.CAPEM, PoolID: state.Membership.PoolID, PoolName: s.State.PoolName, MasterHostID: state.Membership.MasterHostID, MemberHostID: memberHostID, CSRHash: csrHash, Token: token})
	if err != nil {
		return "", "", err
	}
	return state.CAPEM, base64.RawURLEncoding.EncodeToString(b), nil
}

// Membership returns Core's durable membership record. A fresh Controller adopts this
// record from the master rather than retaining a separate pool membership model.
func Membership(s *Store) (protocol.PoolMembership, error) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return protocol.PoolMembership{}, err
	}
	if err = state.Membership.Valid(); err != nil {
		return protocol.PoolMembership{}, errors.New("pool membership is unavailable")
	}
	return state.Membership, nil
}

// PoolMasterURL returns the private peer origin used only for Core reconciliation.
// It is deliberately absent from membership and Controller-facing inventory.
func PoolMasterURL(s *Store) (string, error) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return "", err
	}
	if state.Membership.Valid() != nil || state.MasterURL == "" {
		return "", errors.New("pool master endpoint is unavailable")
	}
	u, err := peerOrigin(state.MasterURL)
	if err != nil {
		return "", errors.New("pool master endpoint is invalid")
	}
	return u.String(), nil
}

// PoolPeerHandler serves the one-time CSR exchange at POST /api/v1/pool/join.
func PoolPeerHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/pool/advertise" {
			member, err := AuthenticatePoolPeer(s, r)
			if err != nil {
				http.Error(w, "peer rejected", 401)
				return
			}
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			// A member advertises five things about itself: where it listens, which
			// machine it is, what its own Core found on its management controller, the
			// watchdog that machine has, and whether it has armed it. Each is sent
			// whenever it is out of date and each is recorded on its own, so an old
			// member that reports none of the later ones still advertises its origin
			// exactly as before.
			var v struct {
				Origin                string                      `json:"origin,omitempty"`
				SystemUUID            string                      `json:"system_uuid,omitempty"`
				BMC                   *protocol.PoolHostBMC       `json:"bmc,omitempty"`
				Watchdog              *protocol.PoolHostWatchdog  `json:"watchdog,omitempty"`
				WatchdogArmed         *protocol.PoolWatchdogArmed `json:"watchdog_armed,omitempty"`
				SanlockHostGeneration uint64                      `json:"sanlock_host_generation,omitempty"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&v) != nil {
				http.Error(w, "invalid peer origin", 400)
				return
			}
			origin, err := peerOrigin(v.Origin)
			uuid := vmhost.NormalizedSystemUUID(v.SystemUUID)
			if err != nil && (v.Origin != "" || uuid == "" && v.BMC == nil && v.Watchdog == nil && v.WatchdogArmed == nil && v.SanlockHostGeneration == 0) {
				http.Error(w, "invalid peer origin", 400)
				return
			}
			if v.BMC != nil && v.BMC.Valid() != nil {
				http.Error(w, "invalid management controller report", 400)
				return
			}
			if v.Watchdog != nil && v.Watchdog.Valid() != nil {
				http.Error(w, "invalid watchdog report", 400)
				return
			}
			if v.WatchdogArmed != nil && v.WatchdogArmed.Valid() != nil {
				http.Error(w, "invalid watchdog arming report", 400)
				return
			}
			poolPeerMu.Lock()
			state, e := loadPoolPeer(s)
			// A member's advertised endpoint is part of the membership, which is a
			// replicated section, so recording it is an authority write and takes the same
			// guard inside the same lock section. A refusal is deferred, not dropped: the
			// member re-advertises from its own retry, and the pool reaches the same place
			// once the transfer is over.
			if e == nil {
				e = poolAuthorityWritable(state)
			}
			if e == nil {
				for i := range state.Membership.Members {
					if state.Membership.Members[i].HostID != member.HostID {
						continue
					}
					record := &state.Membership.Members[i]
					changed := false
					if err == nil && record.PeerOrigin != origin.String() {
						record.PeerOrigin, changed = origin.String(), true
					}
					if uuid != "" && record.SystemUUID != uuid {
						accepted, holder := acceptPoolSystemUUID(state.Membership, *record, uuid)
						if accepted {
							record.SystemUUID, changed = uuid, true
						}
						if record.SystemUUIDConflictHostID != holder {
							record.SystemUUIDConflictHostID, changed = holder, true
						}
					}
					if v.BMC != nil {
						// An observation about this host's own controller, and
						// nothing more: it cannot change what the pool decided about
						// this host. applyPoolBMCReport saves the state itself, and
						// saves nothing when the finding has not changed — so
						// `changed` is deliberately left alone here: an origin or
						// identity change sent in the same request must still be
						// saved by the caller below.
						e = applyPoolBMCReport(s, &state, member.HostID, *v.BMC)
					}
					if v.Watchdog != nil && e == nil {
						// The same door and the same rule: a host describes its own
						// hardware and decides nothing. applyPoolWatchdogReport saves
						// the state itself and saves nothing when the capability has
						// not changed, so `changed` is left alone here too.
						e = applyPoolWatchdogReport(s, &state, member.HostID, *v.Watchdog)
					}
					if v.WatchdogArmed != nil && e == nil {
						// A host is the only thing that can say whether it armed its
						// own device, and it still decides nothing: the pool decided
						// that it should arm, and this is the host reporting that it
						// did. Saved by the same rule, on change only.
						e = applyPoolWatchdogArmedReport(s, &state, member.HostID, *v.WatchdogArmed)
					}
					if v.SanlockHostGeneration != 0 && e == nil {
						lease := state.Membership.StorageLease
						if record.SanlockHostID == 0 || lease == nil || lease.PreparedGeneration == 0 || v.SanlockHostGeneration < record.SanlockHostGeneration {
							e = errors.New("invalid sanlock host generation report")
						} else if record.SanlockHostGeneration != v.SanlockHostGeneration {
							record.SanlockHostGeneration, changed = v.SanlockHostGeneration, true
						}
					}
					if changed && e == nil {
						e = savePoolPeer(s, state)
					}
					break
				}
			}
			poolPeerMu.Unlock()
			if e != nil {
				http.Error(w, "peer origin unavailable", 409)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == poolFenceProvisionPath {
			PoolFenceProvisionHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == poolFenceCheckPath {
			PoolFenceCheckHandler(s).ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, poolCPUPath) {
			PoolCPUHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == poolReplicaPath {
			PoolReplicaHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == poolTransferPath {
			PoolTransferHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == poolAuthorityPath {
			PoolAuthorityHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == poolPlacementPath {
			PoolPlacementHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == poolZFSLifecyclePath {
			PoolZFSLifecycleHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == poolZFSReplacePath {
			PoolZFSReplaceHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == poolZFSPath || strings.HasPrefix(r.URL.Path, poolZFSPath+"/") {
			PoolZFSHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == poolStorageClusterPath || strings.HasPrefix(r.URL.Path, poolStorageClusterPath+"/") {
			PoolStorageClusterHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == strings.TrimSuffix(poolVMPath, "/") || strings.HasPrefix(r.URL.Path, poolVMPath) {
			PoolVMHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == strings.TrimSuffix(poolStoragePath, "/") || strings.HasPrefix(r.URL.Path, poolStoragePath) {
			PoolStorageHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == strings.TrimSuffix(poolNetworkPath, "/") || strings.HasPrefix(r.URL.Path, poolNetworkPath) {
			PoolNetworkHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path == strings.TrimSuffix(poolMediaPath, "/") || strings.HasPrefix(r.URL.Path, poolMediaPath) {
			PoolMediaHandler(s).ServeHTTP(w, r)
			return
		}
		if r.URL.Path != poolJoinPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request peerJoinRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
			http.Error(w, "invalid join request", http.StatusBadRequest)
			return
		}
		response, err := acceptPeerJoin(s, request)
		if err != nil {
			http.Error(w, "join refused", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
}

// ReportPoolSanlockHostGeneration reports this host's exact live generation in the
// configured authority lockspace. A query of any other ID/state is not evidence.
func ReportPoolSanlockHostGeneration(ctx context.Context, s *Store) error {
	membership, err := Membership(s)
	if err != nil || membership.StorageLease == nil || membership.StorageLease.PreparedGeneration == 0 {
		return nil
	}
	member, ok := poolMemberByID(membership, s.State.HostID)
	if !ok || member.Status != "ready" || member.SanlockHostID == 0 {
		return errors.New("this host has no prepared storage-lease membership")
	}
	var authority protocol.PoolStorageLeaseRepository
	found := false
	for _, repository := range membership.StorageLease.Repositories {
		if repository.RepositoryID == membership.StorageLease.AuthorityRepositoryID {
			authority, found = repository, true
			break
		}
	}
	if !found {
		return errors.New("the authority lease repository is unavailable")
	}
	registrations, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedStorage()
	if !managedVMChecksReady(checks) {
		return errors.New("managed storage state is unavailable")
	}
	for _, storage := range registrations {
		if storage.ID != authority.RepositoryID {
			continue
		}
		if storage.Tombstone || storage.Generation != authority.RepositoryGeneration || storage.Identity.String() != authority.RepositoryIdentity.String() || !poolNFSIdentity(storage.Identity) {
			return errors.New("authority lease repository identity changed")
		}
		status, queryErr := vmhost.ReadSanlockHostStatus(ctx, authority.Lockspace, member.SanlockHostID)
		if queryErr != nil || status.HostState != "HOST_LIVE" || status.Generation == 0 {
			return errors.New("authority sanlock host lease is not live")
		}
		if membership.MasterHostID == s.State.HostID {
			poolPeerMu.Lock()
			state, stateErr := loadPoolPeer(s)
			if stateErr == nil {
				for i := range state.Membership.Members {
					if state.Membership.Members[i].HostID == s.State.HostID && state.Membership.Members[i].SanlockHostGeneration != status.Generation {
						if stateErr = poolAuthorityWritable(state); stateErr == nil {
							state.Membership.Members[i].SanlockHostGeneration = status.Generation
							stateErr = savePoolPeer(s, state)
						}
						break
					}
				}
			}
			poolPeerMu.Unlock()
			return stateErr
		}
		return postPoolAdvertise(ctx, s, map[string]any{"sanlock_host_generation": status.Generation})
	}
	return errors.New("authority lease repository is not applied on this host")
}

func acceptPeerJoin(s *Store, request peerJoinRequest) (peerJoinResponse, error) {
	if request.HostID == "" || len(request.HostID) > 128 || len(request.Offer) > 4096 {
		return peerJoinResponse{}, errors.New("invalid join identity")
	}
	// Check-then-bind is one critical section under this lock, which is why two concurrent
	// redemptions cannot both admit a member. That is only sound because one process owns
	// an agent state directory, and that is enforced rather than assumed: OpenStore takes
	// an exclusive flock on <state dir>/lock and refuses to start otherwise (state.go), so
	// the local service and the standalone agent unit cannot serve this handler at once
	// even if their systemd Conflicts= were ever dropped.
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return peerJoinResponse{}, err
	}
	if state.CAKeyPEM == "" || state.Membership.Valid() != nil {
		return peerJoinResponse{}, errors.New("not a pool master")
	}
	// The membership write itself, checked against the bytes it is about to rewrite: an
	// invitation issued before a master change must not admit a member during one.
	if err = poolAuthorityWritable(state); err != nil {
		return peerJoinResponse{}, err
	}
	csrHash := fmt.Sprintf("%x", sha256.Sum256([]byte(request.CSRPEM)))
	digest := offerDigest(request.Offer, state.Membership.PoolID, state.Membership.MasterHostID, request.HostID, csrHash)
	offer, ok := state.Offers[digest]
	if !ok {
		// A Controller-relayed invitation is issued with no CSR hash: this master binds
		// the presented CSR at first redemption and keeps it under the unbound digest, so
		// the same invitation still returns the same certificate and never a second one.
		digest = offerDigest(request.Offer, state.Membership.PoolID, state.Membership.MasterHostID, request.HostID, "")
		offer, ok = state.Offers[digest]
	}
	// An offer that has already issued a certificate is past its expiry only for *new*
	// admissions: replaying it returns that same certificate to that same host and public
	// key and admits nothing. Refusing the replay is what makes a lost reply unrecoverable
	// — the master holds a ready membership row the joining host cannot learn about, and
	// both minting and redeeming then refuse it as an existing member. So the TTL bounds
	// the first redemption, not the replay of one that already happened.
	issued := offer.CertificatePEM != ""
	if !ok || offer.Digest != digest || offer.PoolID != state.Membership.PoolID || offer.Master != state.Membership.MasterHostID || offer.HostID != request.HostID || (offer.CSRHash != "" && offer.CSRHash != csrHash) || (!issued && time.Now().After(offer.Expires)) {
		return peerJoinResponse{}, errors.New("invalid or expired offer")
	}
	csr, err := parsePeerCSR(request.CSRPEM, request.HostID)
	if err != nil {
		return peerJoinResponse{}, err
	}
	if offer.CertificatePEM != "" {
		hash, hashErr := certificateHash(offer.CertificatePEM)
		if hashErr != nil {
			return peerJoinResponse{}, hashErr
		}
		for _, member := range state.Membership.Members {
			if member.HostID == request.HostID && member.CertificateSHA256 == hash && member.Status == "ready" {
				return peerJoinResponse{CertificatePEM: offer.CertificatePEM, Membership: state.Membership}, nil
			}
		}
		return peerJoinResponse{}, errors.New("issued membership is unavailable")
	}
	cert, err := signPeerCSR(state, csr)
	if err != nil {
		return peerJoinResponse{}, err
	}
	hash, err := certificateHash(cert)
	if err != nil {
		return peerJoinResponse{}, err
	}
	replace := -1
	for i, member := range state.Membership.Members {
		if member.HostID == request.HostID && member.Status != "ejected" {
			return peerJoinResponse{}, errors.New("host is already a pool member")
		}
		if member.HostID == request.HostID {
			replace = i
		}
	}
	if err = ensurePoolSanlockHostIDs(&state.Membership); err != nil {
		return peerJoinResponse{}, err
	}
	state.Membership.Generation++
	if state.Membership.StorageLease != nil {
		state.Membership.StorageLease.PreparedGeneration = 0
	}
	for i := range state.Membership.Members {
		state.Membership.Members[i].Generation = state.Membership.Generation
	}
	hostLeaseID, idErr := nextPoolSanlockHostID(&state.Membership)
	if idErr != nil {
		return peerJoinResponse{}, idErr
	}
	member := protocol.PoolMember{HostID: request.HostID, SanlockHostID: hostLeaseID, CertificateSHA256: hash, Generation: state.Membership.Generation, Status: "ready", ObservedAt: time.Now().UTC()}
	if replace >= 0 {
		state.Membership.Members[replace] = member
	} else {
		state.Membership.Members = append(state.Membership.Members, member)
	}
	// Retain this exact certificate, and the CSR an invitation bound here, for a retry
	// after a lost response. It cannot admit a different member or public key, and the
	// private record still expires.
	offer.CertificatePEM, offer.CSRHash = cert, csrHash
	state.Offers[digest] = offer
	if err = savePoolPeer(s, state); err != nil {
		return peerJoinResponse{}, err
	}
	return peerJoinResponse{CertificatePEM: cert, Membership: state.Membership}, nil
}

func nextPoolSanlockHostID(membership *protocol.PoolMembership) (uint64, error) {
	used := make(map[uint64]bool, len(membership.Members))
	for _, member := range membership.Members {
		if member.SanlockHostID != 0 {
			used[member.SanlockHostID] = true
		}
	}
	id := membership.NextSanlockHostID
	if id == 0 {
		id = 1
		for used[id] && id <= protocol.PoolSanlockMaxHostID {
			id++
		}
	}
	for ; id <= protocol.PoolSanlockMaxHostID; id++ {
		if !used[id] {
			membership.NextSanlockHostID = id + 1
			return id, nil
		}
	}
	return 0, errors.New("this pool has no available sanlock host IDs")
}

func ensurePoolSanlockHostIDs(membership *protocol.PoolMembership) error {
	used := map[uint64]bool{}
	for i := range membership.Members {
		id := membership.Members[i].SanlockHostID
		if id == 0 {
			continue
		}
		if id > protocol.PoolSanlockMaxHostID || used[id] {
			return errors.New("pool membership has a duplicate or out-of-range sanlock host ID")
		}
		used[id] = true
	}
	if membership.NextSanlockHostID == 0 {
		membership.NextSanlockHostID = 1
		for used[membership.NextSanlockHostID] && membership.NextSanlockHostID <= protocol.PoolSanlockMaxHostID {
			membership.NextSanlockHostID++
		}
	}
	for i := range membership.Members {
		if membership.Members[i].SanlockHostID != 0 {
			continue
		}
		id := membership.NextSanlockHostID
		for used[id] && id <= protocol.PoolSanlockMaxHostID {
			id++
		}
		if id > protocol.PoolSanlockMaxHostID {
			return errors.New("this pool has no available sanlock host IDs")
		}
		membership.Members[i].SanlockHostID = id
		used[id] = true
		membership.NextSanlockHostID = id + 1
	}
	return nil
}

// JoinPoolPeer joins the master named by the offer. masterURL and poolCAPEM are supplied
// separately by the local authenticated handoff and must exactly match the pinned offer.
func PoolJoinCSR(s *Store) (string, error) {
	if s.State.HostID == "" {
		return "", errors.New("host identity is unavailable")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return "", err
	}
	if state.PendingCSRPEM == "" {
		state.PendingKeyPEM, state.PendingCSRPEM, err = peerKeyCSR(s.State.HostID)
		if err != nil {
			return "", err
		}
		if err = savePoolPeer(s, state); err != nil {
			return "", err
		}
	}
	return state.PendingCSRPEM, nil
}

// PoolPeerClient returns an mTLS client for the current member. It pins the pool
// CA and the durable master host identity, rather than trusting the endpoint name.
func PoolPeerClient(s *Store, masterURL string) (*http.Client, error) {
	return poolPeerClient(s, masterURL, "")
}

// PoolMemberPeerClient opens a master-to-member mTLS connection. The endpoint is
// accepted only when it is the current ready member's advertised peer origin, and
// the server certificate is pinned to that exact membership record. It is for the
// few Core-master workflows which must contact a survivor directly; members must
// continue to use PoolPeerClient when contacting their master.
func PoolMemberPeerClient(s *Store, memberURL, memberHostID string) (*http.Client, error) {
	return poolPeerClient(s, memberURL, memberHostID)
}

func poolMTLSClient(state poolPeerState, expectedHost, expectedHash string) (*http.Client, error) {
	if state.CAPEM == "" || state.CertificatePEM == "" || state.KeyPEM == "" {
		return nil, errors.New("pool peer identity is unavailable")
	}
	cert, err := tls.X509KeyPair([]byte(state.CertificatePEM), []byte(state.KeyPEM))
	if err != nil {
		return nil, errors.New("pool peer identity is invalid")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(state.CAPEM)) {
		return nil, errors.New("invalid pool CA")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true, VerifyConnection: func(c tls.ConnectionState) error {
		return verifyPoolPeer(c, roots, expectedHost, expectedHash)
	}}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: config}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("peer redirects refused") }}, nil
}

func poolPeerClient(s *Store, endpoint, memberHostID string) (*http.Client, error) {
	u, err := peerOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return nil, err
	}
	if err = state.Membership.Valid(); err != nil {
		return nil, errors.New("pool peer identity is unavailable")
	}
	expectedHost, expectedHash := state.Membership.MasterHostID, ""
	if memberHostID != "" {
		if s.State.HostID != state.Membership.MasterHostID {
			return nil, errors.New("only the pool master can contact a member peer")
		}
		expectedHost = memberHostID
		memberFound := false
		for _, member := range state.Membership.Members {
			if member.HostID == memberHostID {
				memberFound = member.Status == "ready" && member.PeerOrigin != ""
				expectedHash = member.CertificateSHA256
				memberOrigin, originErr := peerOrigin(member.PeerOrigin)
				if originErr != nil || memberOrigin.String() != u.String() {
					memberFound = false
				}
				break
			}
		}
		if !memberFound {
			return nil, errors.New("member peer is not a current ready membership origin")
		}
	}
	return poolMTLSClient(state, expectedHost, expectedHash)
}

// AdvertisePoolPeerOrigin records a member's validated peer listener on its master.
// It is authenticated by the member certificate; Controller input cannot name a member.
func AdvertisePoolPeerOrigin(ctx context.Context, s *Store, origin string) error {
	if _, err := peerOrigin(origin); err != nil {
		return err
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	if err == nil {
		state.PendingPeerOrigin = origin
		err = savePoolPeer(s, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		return err
	}
	// The host's own machine identity rides along, so a member that is advertising anyway
	// never needs a second round trip for it.
	uuid, _ := localSystemUUID(s)
	if err = postPoolAdvertise(ctx, s, map[string]any{"origin": origin, "system_uuid": uuid}); err != nil {
		return err
	}
	poolPeerMu.Lock()
	state, err = loadPoolPeer(s)
	if err == nil {
		state.PendingPeerOrigin = ""
		err = savePoolPeer(s, state)
	}
	poolPeerMu.Unlock()
	return nil
}

// postPoolAdvertise sends one member-to-master advertisement over the pool's own mTLS.
func postPoolAdvertise(ctx context.Context, s *Store, body map[string]any) error {
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+"/api/v1/pool/advertise", bytes.NewReader(b))
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
		return errors.New("pool master rejected peer advertisement")
	}
	return nil
}

// acceptPoolSystemUUID decides whether the master may write a member's reported machine
// identity into its row. The certificate proves which *host* is calling; it says nothing
// about which machine that host is running on, so two refusals stand between a host and its
// own fencing record:
//
//   - a host may not replace a machine identity it already reported while a management
//     controller is recorded against it. Otherwise a member that a promotion must exclude
//     could advertise a random identity and make every BMC proof for itself fail for ever,
//     vetoing its own fencing — which is exactly what fencing exists to prevent. Replacing a
//     motherboard is the same operator action either way: clear the fence device, let the
//     host report, record the new controller's access.
//   - an identity another member already holds is not an identity. Firmware placeholders
//     ship identical across whole batches, and two hosts reporting the same UUID each
//     satisfy the other's identity check, so a mistyped controller address between them
//     would power off the wrong machine with a passing proof. An ejected row is not a
//     member: the machine it named may be the one re-imaged and re-enrolled under a new
//     host ID, and nothing would ever release the identity for it.
//
// The second refusal also returns the member that already holds the identity. An operator
// who sees only "this host does not report a unique hardware identity" has nowhere to go;
// one who is told which host claims it can decide whether that host is theirs and eject it.
func acceptPoolSystemUUID(membership protocol.PoolMembership, record protocol.PoolMember, uuid string) (bool, string) {
	if record.SystemUUID != "" && record.Fence != nil &&
		(record.Fence.Kind == protocol.FenceRedfish || record.Fence.Kind == protocol.FenceIPMI) {
		return false, ""
	}
	for _, other := range membership.Members {
		if other.HostID != record.HostID && other.Status != "ejected" && other.SystemUUID == uuid {
			return false, other.HostID
		}
	}
	return true, ""
}

// localSystemUUID is this host's own machine identity. It takes the store so a lab can run
// several hosts in one process; production has exactly one machine to ask.
var localSystemUUID = func(*Store) (string, error) { return vmhost.LocalSystemUUID() }

// ReportPoolSystemUUID keeps this host's own SMBIOS system UUID in the master's membership
// record, which is where another host later reads it to prove a management controller really
// belongs to this machine before it powers it off. The master writes its own record; a member
// advertises to the master. It runs on every reconcile pass, so a pool formed before this
// existed gains the identity of every host that is still alive, without re-joining anything.
func ReportPoolSystemUUID(ctx context.Context, s *Store) error {
	uuid, err := localSystemUUID(s)
	if err != nil {
		return err
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	master, recorded := "", ""
	if err == nil && state.Membership.Valid() == nil {
		master = state.Membership.MasterHostID
		if member, ok := poolMemberByID(state.Membership, s.State.HostID); ok {
			recorded = member.SystemUUID
		}
		if member, ok := poolMemberByID(state.Membership, s.State.HostID); master == s.State.HostID && recorded != uuid && ok {
			accepted, holder := acceptPoolSystemUUID(state.Membership, member, uuid)
			// Like a peer origin this is a recorded observation, not a membership change,
			// so it bumps no generation and waits out a handover rather than writing into
			// one.
			if err = poolAuthorityWritable(state); err == nil && (accepted || member.SystemUUIDConflictHostID != holder) {
				for i := range state.Membership.Members {
					if state.Membership.Members[i].HostID == s.State.HostID {
						if accepted {
							state.Membership.Members[i].SystemUUID = uuid
						}
						state.Membership.Members[i].SystemUUIDConflictHostID = holder
						err = savePoolPeer(s, state)
					}
				}
			}
		}
	}
	poolPeerMu.Unlock()
	if err != nil || master == "" || master == s.State.HostID || recorded == uuid {
		return err
	}
	return postPoolAdvertise(ctx, s, map[string]any{"system_uuid": uuid})
}

func RetryPoolPeerOrigin(ctx context.Context, s *Store) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	origin := state.PendingPeerOrigin
	poolPeerMu.Unlock()
	if err != nil || origin == "" {
		return err
	}
	return AdvertisePoolPeerOrigin(ctx, s, origin)
}

// AuthenticatePoolPeer proves that this TLS request came from a current ready
// member. Resource handlers must call it before reading an opaque offer or credential.
func AuthenticatePoolPeer(s *Store, r *http.Request) (protocol.PoolMember, error) {
	if r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return protocol.PoolMember{}, errors.New("pool peer client certificate is required")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return protocol.PoolMember{}, err
	}
	if state.CAKeyPEM == "" || state.Membership.Valid() != nil || state.Membership.MasterHostID != s.State.HostID {
		return protocol.PoolMember{}, errors.New("this Core is not the pool master")
	}
	leaf := r.TLS.PeerCertificates[0]
	hash := fmt.Sprintf("%x", sha256.Sum256(leaf.Raw))
	for _, member := range state.Membership.Members {
		if member.HostID == leaf.Subject.CommonName && member.CertificateSHA256 == hash && member.Status == "ready" {
			return member, nil
		}
	}
	return protocol.PoolMember{}, errors.New("pool peer is not an admitted member")
}

// AuthenticatePoolMaster admits the current certificate-pinned master to the
// narrow member-serving paths. It must not be used by master-owned handlers:
// those continue to admit only current ready members through AuthenticatePoolPeer.
func AuthenticatePoolMaster(s *Store, r *http.Request) (protocol.PoolMember, error) {
	if r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return protocol.PoolMember{}, errors.New("pool master certificate is required")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return protocol.PoolMember{}, err
	}
	if state.Membership.Valid() != nil || state.Membership.MasterHostID == s.State.HostID {
		return protocol.PoolMember{}, errors.New("this Core does not serve master peer requests")
	}
	leaf := r.TLS.PeerCertificates[0]
	hash := fmt.Sprintf("%x", sha256.Sum256(leaf.Raw))
	for _, member := range state.Membership.Members {
		if member.HostID == state.Membership.MasterHostID && member.HostID == leaf.Subject.CommonName && member.CertificateSHA256 == hash && member.Status == "ready" {
			return member, nil
		}
	}
	return protocol.PoolMember{}, errors.New("pool master is not the current admitted master")
}

func JoinPoolPeer(ctx context.Context, s *Store, masterURL, poolCAPEM, offer string) error {
	u, err := peerOrigin(masterURL)
	if err != nil {
		return err
	}
	if s.State.HostID == "" {
		return errors.New("host identity is unavailable")
	}
	decoded, err := decodePeerOffer(offer)
	if err != nil {
		return err
	}
	if decoded.URL != u.String() || decoded.CAPEM != poolCAPEM || decoded.PoolID == "" || decoded.MasterHostID == "" || decoded.MemberHostID != s.State.HostID || decoded.CSRHash == "" {
		return errors.New("join offer does not match pinned master")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(poolCAPEM)) {
		return errors.New("invalid pool CA")
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return err
	}
	if state.PendingKeyPEM == "" || state.PendingCSRPEM == "" {
		return errors.New("prepare member CSR before requesting a join offer")
	}
	key, csrPEM := state.PendingKeyPEM, state.PendingCSRPEM
	if fmt.Sprintf("%x", sha256.Sum256([]byte(csrPEM))) != decoded.CSRHash {
		return errors.New("join offer is bound to a different member key")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(c tls.ConnectionState) error { return verifyMaster(c, roots, decoded.MasterHostID) }}
	c := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: config}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("peer redirects refused") }}
	defer c.CloseIdleConnections()
	body, _ := json.Marshal(peerJoinRequest{Offer: decoded.Token, HostID: s.State.HostID, CSRPEM: csrPEM})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(masterURL, "/")+poolJoinPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("pool master connection failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pool master rejected join (HTTP %d)", resp.StatusCode)
	}
	var joined peerJoinResponse
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&joined); err != nil {
		return errors.New("invalid pool master response")
	}
	if err = joined.Membership.Valid(); err != nil || joined.Membership.PoolID != decoded.PoolID || joined.Membership.MasterHostID != decoded.MasterHostID {
		return errors.New("invalid joined membership")
	}
	if err = verifyMemberCertificate(joined.CertificatePEM, key, roots, s.State.HostID); err != nil {
		return err
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err = loadPoolPeer(s)
	if err != nil {
		return err
	}
	// A Core that is still the only host of its own pool may join another; a master with
	// members, or a member of a different pool, may not.
	solo := state.Membership.MasterHostID == s.State.HostID && len(state.Membership.Members) == 1
	if state.Membership.PoolID != "" && !solo && (state.Membership.PoolID != joined.Membership.PoolID || state.Membership.MasterHostID != joined.Membership.MasterHostID) {
		return errors.New("host already belongs to another pool")
	}
	state.CAPEM, state.CertificatePEM, state.KeyPEM, state.Membership, state.MasterURL = poolCAPEM, joined.CertificatePEM, key, joined.Membership, u.String()
	state.CAKeyPEM, state.Offers = "", map[string]poolJoinOffer{}
	state.PendingKeyPEM, state.PendingCSRPEM = "", ""
	state.PendingRehome = &poolRehome{PoolID: joined.Membership.PoolID, PoolName: decoded.PoolName}
	if err = savePoolPeer(s, state); err != nil {
		return err
	}
	poolPeerMu.Unlock()
	err = ResumePoolRehome(ctx, s)
	poolPeerMu.Lock()
	return err
}

// ResumePoolRehome completes a persisted Core-owned rehome after a crash or write
// failure. Call it during Core startup and after JoinPoolPeer; it retries until ctx ends.
func ResumePoolRehome(ctx context.Context, s *Store) error {
	for {
		poolPeerMu.Lock()
		state, err := loadPoolPeer(s)
		if err != nil {
			poolPeerMu.Unlock()
			return err
		}
		pending := state.PendingRehome
		poolPeerMu.Unlock()
		if pending == nil {
			return nil
		}
		s.State.PoolID, s.State.PoolName = pending.PoolID, pending.PoolName
		if err = s.Save(); err == nil {
			// A joining host's former single-host owner projection belongs to the pool it
			// just left. PoolJoinPrecheck has already refused live managed VMs, so retaining
			// it could only later leak stale authority into a replica after promotion.
			poolVMMu.Lock()
			vms, vmErr := loadPoolVMs(s)
			if vmErr == nil && (len(vms.VMs) != 0 || len(vms.Inventory) != 0) {
				vms.VMs, vms.Inventory = map[string]poolVMRecord{}, map[string]poolVMInventory{}
				vmErr = savePoolVMs(s, vms)
			}
			poolVMMu.Unlock()
			if vmErr != nil {
				err = errors.New("the previous pool's VM ownership records could not be cleared")
			}
			if err == nil {
				poolPeerMu.Lock()
				state, err = loadPoolPeer(s)
				if err == nil && state.PendingRehome != nil && state.PendingRehome.PoolID == pending.PoolID && state.PendingRehome.PoolName == pending.PoolName {
					state.PendingRehome = nil
					err = savePoolPeer(s, state)
				}
				poolPeerMu.Unlock()
				if err == nil {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func peerOrigin(origin string) (*url.URL, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("pool master must be an HTTPS origin")
	}
	u.Path = ""
	return u, nil
}
func offerDigest(token, pool, master, host, csrHash string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(token+"\x00"+pool+"\x00"+master+"\x00"+host+"\x00"+csrHash)))
}
func decodePeerOffer(offer string) (peerOffer, error) {
	var v peerOffer
	b, err := base64.RawURLEncoding.DecodeString(offer)
	if err != nil || len(b) > 8192 || json.Unmarshal(b, &v) != nil || v.Token == "" {
		return v, errors.New("invalid join offer")
	}
	return v, nil
}

// ValidatePoolJoinOffer performs only bounded structural validation at the local API
// boundary. Redemption remains authenticated against the selected master in JoinPoolPeer.
func ValidatePoolJoinOffer(offer string) error {
	_, err := decodePeerOffer(offer)
	return err
}
func peerKeyCSR(host string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: host}}, key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: request})), nil
}
func parsePeerCSR(data, host string) (*x509.CertificateRequest, error) {
	b, _ := pem.Decode([]byte(data))
	if b == nil || b.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("invalid member CSR")
	}
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != host {
		return nil, errors.New("invalid member CSR")
	}
	return csr, nil
}
func signPeerCSR(state poolPeerState, csr *x509.CertificateRequest) (string, error) {
	caBlock, _ := pem.Decode([]byte(state.CAPEM))
	keyBlock, _ := pem.Decode([]byte(state.CAKeyPEM))
	if caBlock == nil || keyBlock == nil {
		return "", errors.New("invalid pool CA")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return "", err
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return "", err
	}
	signer, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("invalid pool CA key")
	}
	now := time.Now().UTC()
	cert := &x509.Certificate{SerialNumber: serial(), Subject: csr.Subject, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(2, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, csr.PublicKey, signer)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}
func verifyMaster(c tls.ConnectionState, roots *x509.CertPool, host string) error {
	return verifyPoolPeer(c, roots, host, "")
}

func verifyPoolPeer(c tls.ConnectionState, roots *x509.CertPool, host, certificateSHA256 string) error {
	if len(c.PeerCertificates) == 0 {
		return errors.New("pool peer did not present a certificate")
	}
	leaf := c.PeerCertificates[0]
	if leaf.Subject.CommonName != host {
		return errors.New("pool peer identity does not match membership")
	}
	if certificateSHA256 != "" && fmt.Sprintf("%x", sha256.Sum256(leaf.Raw)) != certificateSHA256 {
		return errors.New("pool peer certificate does not match membership")
	}
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates(c.PeerCertificates[1:]), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}
func verifyMemberCertificate(certPEM, keyPEM string, roots *x509.CertPool, host string) error {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return errors.New("pool master returned a certificate for another key")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.Subject.CommonName != host {
		return errors.New("pool master returned a certificate for another host")
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err
}
func intermediates(certs []*x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, cert := range certs {
		pool.AddCert(cert)
	}
	return pool
}
