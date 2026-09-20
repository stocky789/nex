// SPDX-License-Identifier: AGPL-3.0-only
package agent

// The member authority replica is the pool master's signed, read-only copy of its own
// durable pool authority on every ready member. It exists so a pool survives the permanent
// loss of its master: a promotion reads it, and nothing else does. It is never an
// independent authority, never health evidence and never a Controller record.
//
// Signing key: the pool CA private key (pool-peer.json CAKeyPEM), ECDSA P-256 over
// SHA-256 of the replica's canonical JSON minus its own signature. Every member already
// holds the pool CA certificate it pinned at join, and it is the same root that admits
// every peer certificate, so a member can verify a replica offline, indefinitely, after
// the master that signed it is gone. No second PKI is introduced.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolReplicaPath = "/api/v1/pool/replica"
const poolReplicaFileName = "pool-replica.json"
const poolReplicaCheck = "pool-authority-replica"

// poolReplicaRecord is the member's stored file. It keeps the replica exactly as received
// together with the member's own observation of when it arrived, so freshness survives a
// restart instead of restarting from an empty memory map.
type poolReplicaRecord struct {
	Replica    protocol.PoolAuthorityReplica `json:"replica"`
	Digest     string                        `json:"digest"`
	ReceivedAt time.Time                     `json:"received_at"`
	Verified   bool                          `json:"verified"`
}

// Everything below the mutex is a process-local observation, in the manner of member
// access (BUG-0158): acknowledgements a master collected, the last refusal a member
// reported and the last time its master spoke to it about the replica. None of it is
// durable, none of it is authority, and a restart starts it honestly empty.
var poolReplicaMu sync.Mutex
var poolReplicaAcks = map[string]map[string]protocol.PoolReplicaStatus{}         // dir -> member -> last POST ack or push failure
var poolReplicaObservations = map[string]map[string]protocol.PoolReplicaStatus{} // dir -> member -> last GET observation
var poolReplicaPostAcks = map[string]map[string]protocol.PoolReplicaStatus{}     // dir -> member -> last successful POST ack
var poolReplicaRefusals = map[string]protocol.PoolReplicaStatus{}                // dir -> member's last refusal
var poolReplicaPolls = map[string]time.Time{}                                    // dir -> master last confirmed
var poolReplicaPushing = map[string]bool{}                                      // dir -> single flight
var poolReplicaMembership = Membership                                   // test seam: one validated snapshot per check

// ClearPoolReplicaStateForTest drops the process-local replica observations for one store.
func ClearPoolReplicaStateForTest(s *Store) {
	poolReplicaMu.Lock()
	defer poolReplicaMu.Unlock()
	delete(poolReplicaAcks, s.dir)
	delete(poolReplicaObservations, s.dir)
	delete(poolReplicaPostAcks, s.dir)
	delete(poolReplicaRefusals, s.dir)
	delete(poolReplicaPolls, s.dir)
}

// ClearPoolReplicaPostAcksForTest drops only the in-memory POST acknowledgements for one
// master store, simulating a process restart while members may still hold replicas.
func ClearPoolReplicaPostAcksForTest(s *Store) {
	poolReplicaMu.Lock()
	defer poolReplicaMu.Unlock()
	delete(poolReplicaPostAcks, s.dir)
}

func poolReplicaFile(s *Store) string { return filepath.Join(s.dir, poolReplicaFileName) }

func loadPoolReplica(s *Store) (*poolReplicaRecord, error) {
	b, err := os.ReadFile(poolReplicaFile(s))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > protocol.PoolReplicaLimit {
		return nil, errors.New("stored pool authority replica is too large")
	}
	var record poolReplicaRecord
	if json.Unmarshal(b, &record) != nil {
		return nil, errors.New("stored pool authority replica is invalid")
	}
	return &record, nil
}

// savePoolReplica writes the member's copy atomically at 0600 in the already-0700 state
// directory. An interrupted write leaves the previous replica intact.
func savePoolReplica(s *Store, record poolReplicaRecord) error {
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(b) > protocol.PoolReplicaLimit {
		return errors.New("pool authority replica is too large to store")
	}
	return vmhost.AtomicWrite(poolReplicaFile(s), b, 0600)
}

// poolReplicaStorage is the master's durable desired repository set for the replica. It
// fails closed on an incomplete journal for the same reason the peer list does: a replica
// built from a short list is worse than no replica at all.
func poolReplicaStorage(s *Store) ([]protocol.StorageRegistration, error) {
	storage, err := poolPeerStorageList(s)
	if err != nil {
		return nil, err
	}
	out := []protocol.StorageRegistration{}
	for _, entry := range storage {
		if entry.Tombstone || !protocol.IsPoolSharedStorage(entry) {
			continue
		}
		entry.MemberAccess, entry.CredentialReceipt = nil, ""
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b protocol.StorageRegistration) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func poolReplicaNetworks(s *Store) ([]protocol.NetworkRegistration, error) {
	networks, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedNetworks()
	if err != nil {
		return nil, err
	}
	out := []protocol.NetworkRegistration{}
	for _, entry := range networks {
		if entry.Tombstone || !protocol.IsPoolSharedNetwork(entry) {
			continue
		}
		entry.MemberAccess = nil
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b protocol.NetworkRegistration) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func poolReplicaMedia(s *Store) ([]protocol.Media, error) {
	media, err := poolSharedMedia(s)
	if err != nil {
		return nil, err
	}
	out := append([]protocol.Media{}, media...)
	for i := range out {
		out[i].MemberAccess = nil
	}
	slices.SortFunc(out, func(a, b protocol.Media) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func poolReplicaClusters(s *Store) ([]protocol.StorageClusterConfig, error) {
	state, err := loadStorageClusters(s)
	if err != nil {
		return nil, err
	}
	out := []protocol.StorageClusterConfig{}
	for _, operation := range state.Operations {
		if operation.Config.RepositoryID == "" {
			continue
		}
		out = append(out, operation.Config)
	}
	slices.SortFunc(out, func(a, b protocol.StorageClusterConfig) int {
		if c := strings.Compare(a.RepositoryID, b.RepositoryID); c != 0 {
			return c
		}
		return strings.Compare(a.ClusterID, b.ClusterID)
	})
	return out, nil
}

// poolReplicaVMs is the pool-vms.json owner projection: ownership, epochs, specs and
// pending updates. The per-member published inventory snapshots in the same file are
// observations, not authority, and are deliberately not replicated; a promoted master
// re-collects fresh ones (BUG-0158).
func poolReplicaVMs(s *Store) (json.RawMessage, error) {
	poolVMMu.Lock()
	state, err := loadPoolVMs(s)
	poolVMMu.Unlock()
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(state.VMs)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// buildPoolAuthorityReplica assembles the master's current authority. It returns the
// replica without its per-member fields, wrap or signature, plus the content digest that
// identifies this authority regardless of which member it is later addressed to.
func buildPoolAuthorityReplica(s *Store) (protocol.PoolAuthorityReplica, string, error) {
	membership, err := Membership(s)
	if err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	if membership.MasterHostID != s.State.HostID {
		return protocol.PoolAuthorityReplica{}, "", errors.New("only the pool master builds an authority replica")
	}
	replica := protocol.PoolAuthorityReplica{PoolID: membership.PoolID, MasterHostID: membership.MasterHostID, Membership: membership}
	if replica.VMs, err = poolReplicaVMs(s); err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	if replica.Storage, err = poolReplicaStorage(s); err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	if replica.Networks, err = poolReplicaNetworks(s); err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	if replica.Media, err = poolReplicaMedia(s); err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	if replica.StorageClusters, err = poolReplicaClusters(s); err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	if replica.Sections, err = replica.SectionDigests(); err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	digest, err := replica.ContentDigest()
	if err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	return replica, digest, nil
}

// advancePoolAuthorityGeneration persists the generation before anything is pushed, so a
// master that crashes between the write and the push simply pushes the same generation on
// the next tick and a generation never goes backwards. The generation advances when the
// content digest changes, which is what a durable authority write produces; generations
// are allowed to skip, and an unchanged authority keeps its generation.
func advancePoolAuthorityGeneration(s *Store, digest string) (uint64, error) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return 0, err
	}
	// A frozen authority is a numbered one: while a master change is in flight the
	// generation is pinned, so the outgoing master cannot produce the N+1 the target is
	// about to activate at. A host that has handed its authority over numbers nothing.
	if state.Transfer != nil && state.Transfer.HandedOff {
		return 0, errors.New(poolAuthorityHandedOffRefusal)
	}
	// A restricted host numbers nothing: an authority generation it minted while it could
	// not prove it is still the master is exactly the number a stale host would use to
	// out-rank the real one.
	if state.Restricted != nil {
		return 0, errors.New(poolRestrictedReason(state.Restricted))
	}
	if state.Transfer != nil && state.Transfer.Frozen && state.AuthorityGeneration != 0 {
		return state.AuthorityGeneration, nil
	}
	if state.AuthorityGeneration != 0 && state.AuthorityDigest == digest {
		return state.AuthorityGeneration, nil
	}
	state.AuthorityGeneration, state.AuthorityDigest = state.AuthorityGeneration+1, digest
	if err = savePoolPeer(s, state); err != nil {
		return 0, err
	}
	return state.AuthorityGeneration, nil
}

// PoolAuthorityGeneration reports the master's persisted authority generation.
func PoolAuthorityGeneration(s *Store) (uint64, string, error) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return 0, "", err
	}
	return state.AuthorityGeneration, state.AuthorityDigest, nil
}

// PoolAuthorityReport is the master's own durable authority number, for inventory. A member
// reports nothing here: it holds a replica of an authority, never an authority.
func PoolAuthorityReport(s *Store) *protocol.PoolAuthorityRecord {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil || state.Membership.MasterHostID != s.State.HostID {
		return nil
	}
	return &protocol.PoolAuthorityRecord{Generation: state.AuthorityGeneration, Digest: state.AuthorityDigest, ObservedAt: time.Now().UTC()}
}

func poolCASigner(s *Store) (*ecdsa.PrivateKey, error) {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return nil, err
	}
	return parsePoolCAKey(state.CAKeyPEM)
}

func parsePeerSigningKey(keyPEM, unavailable string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New(unavailable)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New(unavailable)
	}
	signer, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New(unavailable)
	}
	return signer, nil
}

func parsePoolCAKey(keyPEM string) (*ecdsa.PrivateKey, error) {
	return parsePeerSigningKey(keyPEM, "pool CA key is unavailable")
}

func signPoolCADigest(signer *ecdsa.PrivateKey, signingBytes func() ([]byte, error)) (string, error) {
	b, err := signingBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	signature, err := ecdsa.SignASN1(rand.Reader, signer, sum[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func verifyPoolCASignature(caPEM, signatureB64 string, signingBytes func() ([]byte, error), invalidMsg string) error {
	key, err := poolCAPublicKey(caPEM)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil || len(signature) == 0 {
		return errors.New(invalidMsg)
	}
	b, err := signingBytes()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if !ecdsa.VerifyASN1(key, sum[:], signature) {
		return errors.New(invalidMsg)
	}
	return nil
}

func poolCAPublicKey(caPEM string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(caPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("pool CA is unavailable")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("pool CA is unavailable")
	}
	key, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("pool CA is unavailable")
	}
	return key, nil
}

func signPoolAuthorityReplica(signer *ecdsa.PrivateKey, replica *protocol.PoolAuthorityReplica) error {
	sig, err := signPoolCADigest(signer, replica.SigningBytes)
	if err != nil {
		return err
	}
	replica.Signature = sig
	return nil
}

func verifyPoolAuthorityReplica(caPEM string, replica protocol.PoolAuthorityReplica) error {
	return verifyPoolCASignature(caPEM, replica.Signature, replica.SigningBytes, "pool authority replica is not signed by this pool's CA")
}

// poolReplicaKeyContext binds a wrap to exactly one pool, member and generation. It is the
// HKDF info and the AES-GCM additional data, so a replica wrapped for member A cannot be
// opened by member B, nor replayed at another generation.
func poolReplicaKeyContext(poolID, memberHostID string, generation uint64) string {
	return "nex pool authority replica v1\x00" + poolID + "\x00" + memberHostID + "\x00" + strconv.FormatUint(generation, 10)
}

// wrapPoolCAKey seals the pool CA private key for one member with a fresh ephemeral key.
// Nothing here is ever logged: the caller carries the sealed bytes only.
func wrapPoolCAKey(caKeyPEM, poolID, memberHostID string, generation uint64, member *ecdsa.PublicKey) (*protocol.PoolWrappedKey, error) {
	return sealForMember([]byte(caKeyPEM), poolReplicaKeyContext(poolID, memberHostID, generation), member)
}

// sealForMember is the one sealing implementation: ECDH from a fresh ephemeral P-256 key to
// the member's certified peer key, HKDF-SHA256 bound to the caller's context, then AES-256-GCM
// with the same context as its additional data. The context is what makes a sealed value
// openable by exactly one member, at exactly one generation, for exactly one purpose.
func sealForMember(plaintext []byte, context string, member *ecdsa.PublicKey) (*protocol.PoolWrappedKey, error) {
	peer, err := member.ECDH()
	if err != nil {
		return nil, errors.New("member peer key cannot receive a wrapped pool CA key")
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := ephemeral.ECDH(peer)
	if err != nil {
		return nil, errors.New("member peer key cannot receive a wrapped pool CA key")
	}
	key, err := hkdf.Key(sha256.New, shared, nil, context, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plaintext, []byte(context))
	return &protocol.PoolWrappedKey{
		EphemeralPublicKey: base64.StdEncoding.EncodeToString(ephemeral.PublicKey().Bytes()),
		Nonce:              base64.StdEncoding.EncodeToString(nonce),
		Ciphertext:         base64.StdEncoding.EncodeToString(sealed),
	}, nil
}

// unwrapPoolCAKey opens a wrapped pool CA key with this host's own peer private key. It is
// deliberately not called by anything in this slice: only a promotion opens the key.
func unwrapPoolCAKey(keyPEM string, replica protocol.PoolAuthorityReplica) (string, error) {
	if replica.CAKeyWrapped == nil {
		return "", errors.New("pool authority replica carries no wrapped pool CA key")
	}
	context := poolReplicaKeyContext(replica.PoolID, replica.MemberHostID, replica.Generation)
	opened, err := openForMember(keyPEM, *replica.CAKeyWrapped, context)
	if err != nil {
		return "", err
	}
	return string(opened), nil
}

// openForMember opens what sealForMember sealed, with this host's own peer private key.
func openForMember(keyPEM string, wrapped protocol.PoolWrappedKey, context string) ([]byte, error) {
	signer, err := parsePeerSigningKey(keyPEM, "this host's peer key is unavailable")
	if err != nil {
		return nil, err
	}
	private, err := signer.ECDH()
	if err != nil {
		return nil, errors.New("this host's peer key is unavailable")
	}
	ephemeralBytes, err := base64.StdEncoding.DecodeString(wrapped.EphemeralPublicKey)
	if err != nil {
		return nil, errors.New("wrapped pool CA key is invalid")
	}
	ephemeral, err := ecdh.P256().NewPublicKey(ephemeralBytes)
	if err != nil {
		return nil, errors.New("wrapped pool CA key is invalid")
	}
	shared, err := private.ECDH(ephemeral)
	if err != nil {
		return nil, errors.New("wrapped pool CA key is not addressed to this host")
	}
	key, err := hkdf.Key(sha256.New, shared, nil, context, 32)
	if err != nil {
		return nil, err
	}
	aesBlock, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(aesBlock)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(wrapped.Nonce)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return nil, errors.New("wrapped pool CA key is invalid")
	}
	sealed, err := base64.StdEncoding.DecodeString(wrapped.Ciphertext)
	if err != nil {
		return nil, errors.New("wrapped pool CA key is invalid")
	}
	opened, err := gcm.Open(nil, nonce, sealed, []byte(context))
	if err != nil {
		return nil, errors.New("wrapped pool CA key is not addressed to this host")
	}
	return opened, nil
}

// poolFenceCredentialContext binds a sealed fence credential to one pool, one member, one
// generation, one fenced host and one credential reference, under its own label. It shares
// nothing with the pool CA key's context, so neither sealed value can be presented as the
// other.
func poolFenceCredentialContext(poolID, memberHostID string, generation uint64, fencedHostID, ref string) string {
	return "nex pool fence credential v1\x00" + poolID + "\x00" + memberHostID + "\x00" +
		strconv.FormatUint(generation, 10) + "\x00" + fencedHostID + "\x00" + ref
}

// poolFenceCredentialFor finds the sealed material for one host in a replica.
func poolFenceCredentialFor(replica protocol.PoolAuthorityReplica, hostID string) *protocol.PoolWrappedFenceCredential {
	for i, credential := range replica.FenceCredentials {
		if credential.HostID == hostID && credential.Wrapped != nil {
			return &replica.FenceCredentials[i]
		}
	}
	return nil
}

// wrapPoolFenceCredential seals one host's fence material for one member. The plaintext is a
// small JSON object of the private files that profile owns; nothing about it is logged.
func wrapPoolFenceCredential(files map[string][]byte, poolID, memberHostID string, generation uint64, fencedHostID, ref string, member *ecdsa.PublicKey) (*protocol.PoolWrappedKey, error) {
	plaintext, err := json.Marshal(files)
	if err != nil {
		return nil, err
	}
	if len(plaintext) > protocol.PoolReplicaLimit/4 {
		return nil, errors.New("fence credential material is too large to replicate")
	}
	return sealForMember(plaintext, poolFenceCredentialContext(poolID, memberHostID, generation, fencedHostID, ref), member)
}

// unwrapPoolFenceCredential opens material addressed to this host at exactly this replica's
// generation. It is called by a promotion and by the preflight that reports whether a
// promotion could fence at all — never while this host is simply a member.
func unwrapPoolFenceCredential(keyPEM string, replica protocol.PoolAuthorityReplica, credential protocol.PoolWrappedFenceCredential) (map[string][]byte, error) {
	if credential.Wrapped == nil {
		return nil, errors.New("this pool authority replica carries no fence credential for that host")
	}
	context := poolFenceCredentialContext(replica.PoolID, replica.MemberHostID, replica.Generation, credential.HostID, credential.CredentialRef)
	opened, err := openForMember(keyPEM, *credential.Wrapped, context)
	if err != nil {
		return nil, errors.New("the replicated fence credential is not addressed to this host")
	}
	var files map[string][]byte
	if json.Unmarshal(opened, &files) != nil || len(files[""]) == 0 {
		return nil, errors.New("the replicated fence credential is unreadable")
	}
	return files, nil
}

// poolFenceCredentialMaterial reads the material of every recorded profile from this master's
// own private store. A profile whose material this host does not hold is skipped: the member
// then honestly reports that it holds no credential for that host, instead of the master
// promising one it cannot deliver.
func poolFenceCredentialMaterial(s *Store, membership protocol.PoolMembership) map[string]map[string][]byte {
	out := map[string]map[string][]byte{}
	for _, member := range membership.Members {
		if member.Fence == nil || member.Fence.CredentialRef == "" || member.Status == "ejected" {
			continue
		}
		files, err := vmhost.ReadPoolFenceCredential(s.dir, member.Fence.CredentialRef)
		if err != nil {
			continue
		}
		out[member.HostID] = files
	}
	return out
}

// acceptPoolAuthorityReplica stores one verified replica and returns the member's
// acknowledgement. It never acts on the contents: no membership, VM, storage, network or
// media record on this host is read from or changed by a replica.
func acceptPoolAuthorityReplica(s *Store, master protocol.PoolMember, replica protocol.PoolAuthorityReplica) (protocol.PoolReplicaAck, error) {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return protocol.PoolReplicaAck{}, errors.New("pool peer state is unavailable")
	}
	membership := state.Membership
	if membership.Valid() != nil || state.CAPEM == "" {
		return protocol.PoolReplicaAck{}, errors.New("pool membership is unavailable")
	}
	if replica.PoolID != membership.PoolID {
		return protocol.PoolReplicaAck{}, errors.New("pool authority replica names another pool")
	}
	if replica.MasterHostID != membership.MasterHostID || replica.MasterHostID != master.HostID {
		return protocol.PoolReplicaAck{}, errors.New("pool authority replica is not from the current pool master")
	}
	if replica.MemberHostID != s.State.HostID {
		return protocol.PoolReplicaAck{}, errors.New("pool authority replica is addressed to another host")
	}
	if err = replica.Valid(); err != nil {
		return protocol.PoolReplicaAck{}, err
	}
	if err = verifyPoolAuthorityReplica(state.CAPEM, replica); err != nil {
		return protocol.PoolReplicaAck{}, err
	}
	digest, err := replica.ContentDigest()
	if err != nil {
		return protocol.PoolReplicaAck{}, err
	}
	poolReplicaMu.Lock()
	defer poolReplicaMu.Unlock()
	current, err := loadPoolReplica(s)
	if err != nil {
		return protocol.PoolReplicaAck{}, errors.New("stored pool authority replica is unavailable")
	}
	if current != nil {
		if replica.Generation < current.Replica.Generation {
			return protocol.PoolReplicaAck{}, errors.New("pool authority replica generation is older than the stored replica")
		}
		if replica.Generation == current.Replica.Generation && digest != current.Digest {
			return protocol.PoolReplicaAck{}, errors.New("pool authority replica generation was already stored with different contents")
		}
	}
	record := poolReplicaRecord{Replica: replica, Digest: digest, ReceivedAt: time.Now().UTC(), Verified: true}
	if err = savePoolReplica(s, record); err != nil {
		return protocol.PoolReplicaAck{}, errors.New("pool authority replica could not be stored")
	}
	delete(poolReplicaRefusals, s.dir)
	poolReplicaPolls[s.dir] = record.ReceivedAt
	return protocol.PoolReplicaAck{HostID: s.State.HostID, Generation: replica.Generation, Digest: digest, Verified: true}, nil
}

// boundPoolPeerText bounds peer-facing text that crosses the wire or enters a check.
func boundPoolPeerText(limit int, fallback, message string) string {
	message = strings.TrimSpace(message)
	if len(message) > limit {
		message = message[:limit]
	}
	if strings.ContainsAny(message, "\r\n\x00") {
		return fallback
	}
	return message
}

// poolReplicaError bounds every refusal that crosses the wire or enters a check.
func poolReplicaError(err error) string {
	return boundPoolPeerText(200, "pool authority replica refused", err.Error())
}

// PoolReplicaHandler serves the member side: GET reports what this host holds, POST
// accepts one replica. Only the current certificate-pinned master is admitted.
func PoolReplicaHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		master, err := AuthenticatePoolMaster(s, r)
		if err != nil {
			http.Error(w, "replica peer rejected", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			status := PoolReplicaReport(s)
			if status == nil {
				status = &protocol.PoolReplicaStatus{}
			}
			poolReplicaMu.Lock()
			poolReplicaPolls[s.dir] = time.Now().UTC()
			poolReplicaMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(status)
		case http.MethodPost:
			var replica protocol.PoolAuthorityReplica
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.PoolReplicaLimit))
			if decoder.Decode(&replica) != nil || decoder.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid pool authority replica", http.StatusBadRequest)
				return
			}
			ack, err := acceptPoolAuthorityReplica(s, master, replica)
			if err != nil {
				message := poolReplicaError(err)
				poolReplicaMu.Lock()
				poolReplicaRefusals[s.dir] = protocol.PoolReplicaStatus{Generation: replica.Generation, MasterHostID: master.HostID, ReceivedAt: time.Now().UTC(), Error: message}
				poolReplicaPolls[s.dir] = time.Now().UTC()
				poolReplicaMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(protocol.PoolReplicaAck{HostID: s.State.HostID, Generation: replica.Generation, Error: message})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ack)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// PoolReplicaReport is this host's own observed replica state for inventory.
func PoolReplicaReport(s *Store) *protocol.PoolReplicaStatus {
	poolReplicaMu.Lock()
	record, err := loadPoolReplica(s)
	refusal, refused := poolReplicaRefusals[s.dir]
	poolReplicaMu.Unlock()
	if err != nil {
		return &protocol.PoolReplicaStatus{Error: "stored pool authority replica is unavailable"}
	}
	if record == nil {
		if refused {
			return &refusal
		}
		return nil
	}
	status := protocol.PoolReplicaStatus{
		Generation: record.Replica.Generation, Digest: record.Digest, MasterHostID: record.Replica.MasterHostID,
		SignedAt: record.Replica.CreatedAt, ReceivedAt: record.ReceivedAt, Verified: record.Verified,
	}
	if refused {
		status.Error = refusal.Error
	}
	return &status
}

// PoolReplicaMembership overlays the master's acknowledgements onto the membership copy it
// reports. The durable membership record is never written with them.
func PoolReplicaMembership(s *Store, membership *protocol.PoolMembership) *protocol.PoolMembership {
	if membership == nil || membership.MasterHostID != s.State.HostID {
		return membership
	}
	poolReplicaMu.Lock()
	acks := map[string]protocol.PoolReplicaStatus{}
	for host, ack := range poolReplicaAcks[s.dir] {
		acks[host] = ack
	}
	poolReplicaMu.Unlock()
	out := *membership
	out.Members = append([]protocol.PoolMember(nil), membership.Members...)
	for i := range out.Members {
		ack, ok := acks[out.Members[i].HostID]
		if !ok {
			continue
		}
		out.Members[i].ReplicaGeneration = ack.Generation
		out.Members[i].ReplicaDigest = ack.Digest
		out.Members[i].ReplicaError = ack.Error
		out.Members[i].ReplicaObservedAt = ack.ReceivedAt
	}
	return &out
}

// PoolReplicaChecks reports replica freshness honestly on both sides. A push that has not
// landed within two probe intervals is unknown and stale, never healthy, and the replica
// never contributes to any other health judgement.
func PoolReplicaChecks(s *Store, probeInterval time.Duration) []protocol.Check {
	membership, err := poolReplicaMembership(s)
	if err != nil {
		return nil
	}
	window := 2 * probeInterval
	if window < time.Minute {
		window = time.Minute
	}
	now := time.Now().UTC()
	if membership.MasterHostID != s.State.HostID {
		poolReplicaMu.Lock()
		polled := poolReplicaPolls[s.dir]
		poolReplicaMu.Unlock()
		status := PoolReplicaReport(s)
		if status == nil {
			return []protocol.Check{{Name: poolReplicaCheck, Status: "unknown", Freshness: "unknown", ObservedAt: now, Error: "awaiting the pool master's first authority replica"}}
		}
		if status.Error != "" {
			return []protocol.Check{{Name: poolReplicaCheck, Status: "failed", Freshness: "fresh", ObservedAt: status.ReceivedAt, Error: status.Error}}
		}
		observed := status.ReceivedAt
		if polled.After(observed) {
			observed = polled
		}
		if now.Sub(observed) > window {
			return []protocol.Check{{Name: poolReplicaCheck, Status: "unknown", Freshness: "stale", ObservedAt: status.ReceivedAt, Error: "pool authority replica observation is stale"}}
		}
		return []protocol.Check{{Name: poolReplicaCheck, Status: "healthy", Freshness: "fresh", ObservedAt: status.ReceivedAt}}
	}
	generation, _, err := PoolAuthorityGeneration(s)
	if err != nil {
		return []protocol.Check{{Name: poolReplicaCheck, Status: "failed", Freshness: "fresh", ObservedAt: now, Error: "pool authority generation is unavailable"}}
	}
	// Use the membership already read above. Re-reading it here would turn a concurrent
	// read failure into an empty member list and falsely publish a healthy clear.
	members := make([]string, 0, len(membership.Members))
	for _, member := range poolConfirmationPeers(membership, s.State.HostID) {
		members = append(members, member.HostID)
	}
	if len(members) == 0 {
		return []protocol.Check{{Name: poolReplicaCheck, Status: "healthy", Freshness: "fresh", ObservedAt: now}}
	}
	poolReplicaMu.Lock()
	acks := poolReplicaAcks[s.dir]
	out := []protocol.Check{}
	for _, host := range members {
		ack, ok := acks[host]
		switch {
		case !ok:
			out = append(out, protocol.Check{Name: poolReplicaCheck, Status: "unknown", Freshness: "unknown", ObservedAt: now, Error: "a pool member has not yet acknowledged an authority replica"})
		case ack.Error != "":
			out = append(out, protocol.Check{Name: poolReplicaCheck, Status: "failed", Freshness: "fresh", ObservedAt: ack.ReceivedAt, Error: ack.Error})
		case now.Sub(ack.ReceivedAt) > window || ack.Generation != generation:
			out = append(out, protocol.Check{Name: poolReplicaCheck, Status: "unknown", Freshness: "stale", ObservedAt: ack.ReceivedAt, Error: "a pool member's authority replica acknowledgement is stale"})
		}
	}
	poolReplicaMu.Unlock()
	if len(out) == 0 {
		return []protocol.Check{{Name: poolReplicaCheck, Status: "healthy", Freshness: "fresh", ObservedAt: now}}
	}
	return out[:1]
}

func recordPoolReplicaAck(s *Store, host string, ack protocol.PoolReplicaStatus) {
	poolReplicaMu.Lock()
	defer poolReplicaMu.Unlock()
	if poolReplicaAcks[s.dir] == nil {
		poolReplicaAcks[s.dir] = map[string]protocol.PoolReplicaStatus{}
	}
	ack.ReceivedAt = time.Now().UTC()
	poolReplicaAcks[s.dir][host] = ack
}

func recordPoolReplicaObservation(s *Store, host string, status protocol.PoolReplicaStatus) {
	poolReplicaMu.Lock()
	defer poolReplicaMu.Unlock()
	if poolReplicaObservations[s.dir] == nil {
		poolReplicaObservations[s.dir] = map[string]protocol.PoolReplicaStatus{}
	}
	status.ReceivedAt = time.Now().UTC()
	poolReplicaObservations[s.dir][host] = status
}

func recordPoolReplicaPostAck(s *Store, host string, ack protocol.PoolReplicaStatus) {
	poolReplicaMu.Lock()
	defer poolReplicaMu.Unlock()
	if poolReplicaPostAcks[s.dir] == nil {
		poolReplicaPostAcks[s.dir] = map[string]protocol.PoolReplicaStatus{}
	}
	ack.ReceivedAt = time.Now().UTC()
	poolReplicaPostAcks[s.dir][host] = ack
}

func poolReplicaPostAckMatches(s *Store, host string, generation uint64, digest string) bool {
	poolReplicaMu.Lock()
	defer poolReplicaMu.Unlock()
	ack, ok := poolReplicaPostAcks[s.dir][host]
	return ok && ack.Error == "" && ack.Verified && ack.Generation == generation && ack.Digest == digest
}

// PushPoolAuthorityReplicas advances and persists this master's authority generation, then
// pushes the current replica to every ready member that is behind. It is single-flight per
// store, safe to call from every probe tick, and safe to retry: a member acknowledges the
// same generation idempotently.
func PushPoolAuthorityReplicas(ctx context.Context, s *Store, timeout time.Duration) error {
	poolReplicaMu.Lock()
	if poolReplicaPushing[s.dir] {
		poolReplicaMu.Unlock()
		return nil
	}
	poolReplicaPushing[s.dir] = true
	poolReplicaMu.Unlock()
	defer func() {
		poolReplicaMu.Lock()
		delete(poolReplicaPushing, s.dir)
		poolReplicaMu.Unlock()
	}()
	replica, digest, err := buildPoolAuthorityReplica(s)
	if err != nil {
		return err
	}
	generation, err := advancePoolAuthorityGeneration(s, digest)
	if err != nil {
		return err
	}
	replica.Generation = generation
	signer, err := poolCASigner(s)
	if err != nil {
		return err
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return err
	}
	caKeyPEM := state.CAKeyPEM
	// The private material behind every recorded fence profile, read once per push from this
	// master's own store and sealed separately for each member below. A member that may one
	// day have to fence a host cannot be given the material after that host is gone.
	material := poolFenceCredentialMaterial(s, replica.Membership)
	var failed error
	for _, member := range replica.Membership.Members {
		if member.HostID == s.State.HostID || member.Status != "ready" || member.PeerOrigin == "" {
			continue
		}
		if err = pushPoolReplicaToMember(ctx, s, member, replica, digest, caKeyPEM, material, signer, timeout); err != nil {
			recordPoolReplicaAck(s, member.HostID, protocol.PoolReplicaStatus{Error: poolReplicaError(err)})
			failed = err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	// Once every ready member holds a replica built after this host's own fence profile was
	// cleared, no member can still fence-promote it, and its restart no longer has to wait
	// for anyone.
	ReleasePoolSelfFenceExposure(s)
	return failed
}

func pushPoolReplicaToMember(ctx context.Context, s *Store, member protocol.PoolMember, replica protocol.PoolAuthorityReplica, digest, caKeyPEM string, material map[string]map[string][]byte, signer *ecdsa.PrivateKey, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client, err := PoolMemberPeerClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	work, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// The status read is also how this master learns the member's certified public key
	// for the wrap: the transport already pinned that certificate to this exact
	// membership record, so nothing else has to be trusted to address the sealed key.
	request, err := http.NewRequestWithContext(work, http.MethodGet, strings.TrimRight(member.PeerOrigin, "/")+poolReplicaPath, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	var status protocol.PoolReplicaStatus
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&status)
	code := response.StatusCode
	peerCertificates := response.TLS
	response.Body.Close()
	if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
		return errors.New("pool member does not support " + protocol.PoolAuthorityReplicaCapability + "; upgrade it before relying on its replica")
	}
	if code != http.StatusOK || decodeErr != nil || peerCertificates == nil || len(peerCertificates.PeerCertificates) == 0 {
		return errors.New("pool member did not report its authority replica")
	}
	recordPoolReplicaObservation(s, member.HostID, status)
	if poolReplicaPostAckMatches(s, member.HostID, replica.Generation, digest) {
		return nil
	}
	key, ok := peerCertificates.PeerCertificates[0].PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("pool member peer certificate cannot receive a wrapped pool CA key")
	}
	addressed := replica
	addressed.MemberHostID, addressed.CreatedAt = member.HostID, time.Now().UTC()
	if addressed.CAKeyWrapped, err = wrapPoolCAKey(caKeyPEM, replica.PoolID, member.HostID, replica.Generation, key); err != nil {
		return err
	}
	addressed.FenceCredentials = nil
	for _, fenced := range replica.Membership.Members {
		files, ok := material[fenced.HostID]
		if !ok || fenced.Fence == nil {
			continue
		}
		wrapped, wrapErr := wrapPoolFenceCredential(files, replica.PoolID, member.HostID, replica.Generation, fenced.HostID, fenced.Fence.CredentialRef, key)
		if wrapErr != nil {
			return wrapErr
		}
		addressed.FenceCredentials = append(addressed.FenceCredentials, protocol.PoolWrappedFenceCredential{
			HostID: fenced.HostID, CredentialRef: fenced.Fence.CredentialRef,
			Version: fenced.FenceCredentialVersion, Wrapped: wrapped,
		})
	}
	if err = signPoolAuthorityReplica(signer, &addressed); err != nil {
		return err
	}
	body, err := json.Marshal(addressed)
	if err != nil {
		return err
	}
	if len(body) > protocol.PoolReplicaLimit {
		return errors.New("pool authority replica exceeds the peer body limit")
	}
	request, err = http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolReplicaPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err = client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var ack protocol.PoolReplicaAck
	if json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&ack) != nil {
		return errors.New("pool member did not acknowledge the authority replica")
	}
	if response.StatusCode != http.StatusOK || !ack.Verified || ack.Generation != replica.Generation || ack.Digest != digest {
		refusal := errors.New("pool member refused the authority replica")
		if ack.Error != "" {
			refusal = errors.New(ack.Error)
		}
		return refusal
	}
	postAck := protocol.PoolReplicaStatus{
		Generation: ack.Generation, Digest: ack.Digest, MasterHostID: s.State.HostID,
		SignedAt: addressed.CreatedAt, Verified: true,
	}
	recordPoolReplicaPostAck(s, member.HostID, postAck)
	recordPoolReplicaAck(s, member.HostID, postAck)
	return nil
}
