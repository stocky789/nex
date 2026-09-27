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
const poolBackupPagePath = "/api/v1/pool/replica/backup-page"
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
var poolReplicaPushing = map[string]bool{}                                       // dir -> single flight
var poolReplicaMembership = Membership                                           // test seam: one validated snapshot per check
var writePoolBackupPage = vmhost.AtomicWrite
var errPoolAuthorityReplicaBusy = errors.New("pool authority replica push already in progress")

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
	record, err := loadPoolReplicaUnchecked(s)
	if err != nil || record == nil {
		return record, err
	}
	if record.Replica.BackupManifest != nil {
		if err := verifyPoolBackupAuthorityPages(s, record.Replica.BackupManifest); err != nil {
			return nil, err
		}
	}
	return record, nil
}

func loadPoolReplicaUnchecked(s *Store) (*poolReplicaRecord, error) {
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

func poolReplicaResourceMutations(s *Store) ([]protocol.PoolResourceMutation, error) {
	poolStorageMu.Lock()
	defer poolStorageMu.Unlock()
	state, err := loadPoolStorage(s)
	if err != nil {
		return nil, err
	}
	return poolResourceMutationsFromStorage(state)
}

func poolResourceMutationsFromStorage(state poolStorageState) ([]protocol.PoolResourceMutation, error) {
	out := make([]protocol.PoolResourceMutation, 0)
	for key, operationID := range state.Mutations {
		if !strings.HasPrefix(key, poolResourceMutationPrefix) {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(key, poolResourceMutationPrefix), "/")
		if len(parts) != 2 {
			return nil, errors.New("pool resource mutation key is invalid")
		}
		mutation := protocol.PoolResourceMutation{Resource: parts[0], ResourceID: parts[1], OperationID: operationID}
		if mutation.Valid() != nil {
			return nil, errors.New("pool resource mutation record is invalid")
		}
		out = append(out, mutation)
	}
	slices.SortFunc(out, func(a, b protocol.PoolResourceMutation) int {
		if c := strings.Compare(a.Resource, b.Resource); c != 0 {
			return c
		}
		return strings.Compare(a.ResourceID, b.ResourceID)
	})
	return out, protocol.ValidatePoolResourceMutations(out)
}

// buildPoolAuthorityReplica assembles the master's current authority. It returns the
// replica without its per-member fields, wrap or signature, plus the content digest that
// identifies this authority regardless of which member it is later addressed to.
func buildPoolAuthorityReplica(s *Store) (protocol.PoolAuthorityReplica, string, error) {
	vmSection, err := poolReplicaVMs(s)
	if err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	return buildPoolAuthorityReplicaWithVMs(s, vmSection)
}

// buildPoolAuthorityReplicaWithVMs builds the same authority projection with
// a caller's prospective VM section, used to reject writes that would make the
// signed replica unrepresentable before native work starts.
func buildPoolAuthorityReplicaWithVMs(s *Store, vmSection json.RawMessage) (protocol.PoolAuthorityReplica, string, error) {
	membership, err := Membership(s)
	if err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	return buildPoolAuthorityReplicaForMembership(s, membership, vmSection)
}

func buildPoolAuthorityReplicaForMembership(s *Store, membership protocol.PoolMembership, vmSection json.RawMessage) (protocol.PoolAuthorityReplica, string, error) {
	if membership.MasterHostID != s.State.HostID {
		return protocol.PoolAuthorityReplica{}, "", errors.New("only the pool master builds an authority replica")
	}
	var err error
	replica := protocol.PoolAuthorityReplica{PoolID: membership.PoolID, MasterHostID: membership.MasterHostID, Membership: membership}
	replica.VMs = vmSection
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
	if replica.ResourceMutations, err = poolReplicaResourceMutations(s); err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	if replica.BackupAuthority, err = PoolBackupAuthorityRefs(s); err != nil {
		return protocol.PoolAuthorityReplica{}, "", err
	}
	if replica.BackupAuthority != nil {
		if replica.BackupManifest, err = stagePoolBackupAuthority(s, replica.BackupAuthority); err != nil {
			return protocol.PoolAuthorityReplica{}, "", err
		}
		if replica.BackupManifest.TotalBytes <= protocol.PoolBackupPageLimit && len(replica.BackupAuthority.MetadataReceipts) == 0 {
			// Preserve the v2 inline format for small, receipt-free authorities.
			inline, marshalErr := json.Marshal(replica.BackupAuthority)
			if marshalErr != nil {
				return protocol.PoolAuthorityReplica{}, "", marshalErr
			}
			if len(inline) <= protocol.PoolBackupPageLimit {
				replica.BackupManifest = nil
			}
		}
		if replica.BackupManifest != nil {
			replica.BackupAuthority = nil
		}
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

// poolAuthorityReplicaFitsAddressedBodies checks the exact JSON size shape sent
// to every ready member. Wrapped ciphertext lengths are deterministic for a
// plaintext, so dummy base64 values preserve the real encoded size; the maximum
// P-256 ASN.1 signature length leaves the check conservative.
func poolAuthorityReplicaFitsAddressedBodies(s *Store, replica protocol.PoolAuthorityReplica, caKeyPEM string) error {
	baseBody, err := json.Marshal(replica)
	if err != nil || len(baseBody) > protocol.PoolReplicaLimit {
		return errors.New("pool authority replica would exceed the peer body limit")
	}
	material := poolFenceCredentialMaterial(s, replica.Membership)
	var witnessBundle []byte
	if witness := replica.Membership.Witness; witness != nil && witness.CredentialsReady {
		witnessBundle, err = vmhost.PoolWitnessBundle(s.dir, witness.FencingGeneration)
		if err != nil {
			return errors.New("pool witness credential is unavailable for a bounded replica")
		}
	}
	wrapped := func(plaintext []byte) *protocol.PoolWrappedKey {
		return &protocol.PoolWrappedKey{
			EphemeralPublicKey: strings.Repeat("A", base64.StdEncoding.EncodedLen(65)),
			Nonce:              strings.Repeat("A", base64.StdEncoding.EncodedLen(12)),
			Ciphertext:         strings.Repeat("A", base64.StdEncoding.EncodedLen(len(plaintext)+16)),
		}
	}
	for _, member := range replica.Membership.Members {
		if member.HostID == s.State.HostID || member.Status != "ready" || member.PeerOrigin == "" {
			continue
		}
		addressed := replica
		addressed.MemberHostID = member.HostID
		addressed.Generation = ^uint64(0)
		addressed.CreatedAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
		addressed.CAKeyWrapped = wrapped([]byte(caKeyPEM))
		addressed.FenceCredentials = nil
		for _, fenced := range replica.Membership.Members {
			files, ok := material[fenced.HostID]
			if !ok || fenced.Fence == nil {
				continue
			}
			plaintext, err := json.Marshal(files)
			if err != nil || len(plaintext) > protocol.PoolReplicaLimit/4 {
				return errors.New("pool fence credential is too large for a bounded replica")
			}
			addressed.FenceCredentials = append(addressed.FenceCredentials, protocol.PoolWrappedFenceCredential{
				HostID: fenced.HostID, CredentialRef: fenced.Fence.CredentialRef,
				Version: fenced.FenceCredentialVersion, Wrapped: wrapped(plaintext),
			})
		}
		addressed.WitnessCredentials = nil
		if witness := replica.Membership.Witness; witness != nil && witness.CredentialsReady {
			addressed.WitnessCredentials = []protocol.PoolWrappedWitnessCredential{{
				HostID: member.HostID, FencingGeneration: witness.FencingGeneration, Wrapped: wrapped(witnessBundle),
			}}
		}
		// P-256 ECDSA ASN.1 signatures are at most 72 bytes, or 96 base64 chars.
		addressed.Signature = strings.Repeat("A", base64.StdEncoding.EncodedLen(72))
		body, err := json.Marshal(addressed)
		if err != nil || len(body) > protocol.PoolReplicaLimit {
			return errors.New("pool authority replica would exceed the peer body limit")
		}
	}
	return nil
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

func poolWitnessCredentialContext(poolID, memberHostID string, generation uint64) string {
	return "nex pool qdevice witness credential v1\x00" + poolID + "\x00" + memberHostID + "\x00" + strconv.FormatUint(generation, 10)
}

func wrapPoolWitnessCredential(bundle []byte, poolID, memberHostID string, generation uint64, member *ecdsa.PublicKey) (*protocol.PoolWrappedKey, error) {
	if len(bundle) == 0 || len(bundle) > 1<<20 {
		return nil, errors.New("qdevice witness credential is empty or too large")
	}
	return sealForMember(bundle, poolWitnessCredentialContext(poolID, memberHostID, generation), member)
}

func unwrapPoolWitnessCredential(keyPEM string, replica protocol.PoolAuthorityReplica, credential protocol.PoolWrappedWitnessCredential) ([]byte, error) {
	if credential.Wrapped == nil || credential.HostID != replica.MemberHostID || credential.FencingGeneration == 0 || replica.Membership.Witness == nil || credential.FencingGeneration != replica.Membership.Witness.FencingGeneration {
		return nil, errors.New("pool authority replica carries no matching qdevice witness credential")
	}
	opened, err := openForMember(keyPEM, *credential.Wrapped, poolWitnessCredentialContext(replica.PoolID, replica.MemberHostID, replica.Generation))
	if err != nil || len(opened) == 0 {
		return nil, errors.New("qdevice witness credential is not sealed for this host")
	}
	return opened, nil
}

func poolWitnessCredentialFor(replica protocol.PoolAuthorityReplica, hostID string) *protocol.PoolWrappedWitnessCredential {
	for i, credential := range replica.WitnessCredentials {
		if credential.HostID == hostID && credential.Wrapped != nil {
			return &replica.WitnessCredentials[i]
		}
	}
	return nil
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
		files, err := vmhost.ReadPoolFenceCredential(s.dir, *member.Fence)
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
	if replica.BackupManifest != nil {
		if _, err = loadPoolBackupAuthorityPages(s, replica.BackupManifest); err != nil {
			return protocol.PoolReplicaAck{}, errors.New("pool authority replica backup pages are incomplete or invalid")
		}
	}
	var localSanlockGeneration uint64
	if replica.Membership.StorageLease != nil {
		localSanlockGeneration, err = preparePoolStorageLeaseHost(context.Background(), s, replica.Membership, false)
		if err != nil {
			return protocol.PoolReplicaAck{}, err
		}
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
	if witness := replica.Membership.Witness; witness != nil && witness.CredentialsReady {
		credential := poolWitnessCredentialFor(replica, s.State.HostID)
		if credential == nil {
			return protocol.PoolReplicaAck{}, errors.New("pool authority replica omitted the qdevice credential addressed to this member")
		}
		bundle, openErr := unwrapPoolWitnessCredential(state.KeyPEM, replica, *credential)
		if openErr != nil {
			return protocol.PoolReplicaAck{}, openErr
		}
		if err = poolWitnessImportMember(context.Background(), s.dir, replica.PoolID, *witness, bundle); err != nil {
			return protocol.PoolReplicaAck{}, err
		}
	}
	record := poolReplicaRecord{Replica: replica, Digest: digest, ReceivedAt: time.Now().UTC(), Verified: true}
	if err = savePoolReplica(s, record); err != nil {
		return protocol.PoolReplicaAck{}, errors.New("pool authority replica could not be stored")
	}
	delete(poolReplicaRefusals, s.dir)
	poolReplicaPolls[s.dir] = record.ReceivedAt
	return protocol.PoolReplicaAck{HostID: s.State.HostID, Generation: replica.Generation, Digest: digest,
		SanlockHostGeneration: localSanlockGeneration, Verified: true}, nil
}

// PoolBackupPageHandler stages a signed replica's bounded backup page. Pages are
// untrusted until the final replica verifies the complete manifest and is saved.
func PoolBackupPageHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		master, err := AuthenticatePoolMaster(s, r)
		if err != nil {
			http.Error(w, "replica peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var upload poolBackupPageUpload
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.PoolReplicaLimit+2*protocol.PoolBackupPageLimit))
		if decoder.Decode(&upload) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid backup authority page", http.StatusBadRequest)
			return
		}
		poolPeerMu.Lock()
		state, err := loadPoolPeer(s)
		poolPeerMu.Unlock()
		if err != nil || state.Membership.Valid() != nil || state.CAPEM == "" ||
			upload.Replica.PoolID != state.Membership.PoolID ||
			upload.Replica.MasterHostID != state.Membership.MasterHostID ||
			upload.Replica.MasterHostID != master.HostID || upload.Replica.MemberHostID != s.State.HostID ||
			upload.Replica.Valid() != nil || verifyPoolAuthorityReplica(state.CAPEM, upload.Replica) != nil {
			http.Error(w, "backup authority page replica is not authorized", http.StatusConflict)
			return
		}
		m := upload.Replica.BackupManifest
		if m == nil || upload.Index < -1 || upload.Index >= len(m.PageSHA256) {
			http.Error(w, "backup authority page index is invalid", http.StatusBadRequest)
			return
		}
		if upload.Index >= 0 && (len(upload.Data) != m.PageBytes[upload.Index] || poolBackupHash(upload.Data) != m.PageSHA256[upload.Index]) {
			http.Error(w, "backup authority page digest does not match", http.StatusBadRequest)
			return
		}
		poolReplicaMu.Lock()
		current, err := loadPoolReplicaUnchecked(s)
		poolReplicaMu.Unlock()
		if err != nil || (current != nil && upload.Replica.Generation < current.Replica.Generation) {
			http.Error(w, "backup authority page generation is stale", http.StatusConflict)
			return
		}
		if current != nil && upload.Replica.Generation == current.Replica.Generation {
			digest, digestErr := upload.Replica.ContentDigest()
			if digestErr != nil || digest != current.Digest {
				http.Error(w, "backup authority page generation conflicts with the stored replica", http.StatusConflict)
				return
			}
		}
		if upload.Index == -1 {
			if len(upload.Data) != 0 {
				http.Error(w, "backup authority progress request has data", http.StatusBadRequest)
				return
			}
			next := len(m.PageSHA256)
			for index := range m.PageSHA256 {
				if _, err := readPoolBackupPage(s, m, index); err != nil {
					next = index
					break
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(poolBackupPageProgress{NextIndex: next})
			return
		}
		path := poolBackupPageFile(s, m.SHA256, upload.Index)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err == nil {
			err = writePoolBackupPage(path, upload.Data, 0600)
		}
		if err != nil {
			http.Error(w, "backup authority page could not be stored", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
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

// poolAuthorityReplicaAckRequired applies to snapshot reservation/settlement and
// ordinary replica-push failure aggregation. Destructive resource removal and
// storage-lease preparation retain their stricter all-ready-host gates.
func poolAuthorityReplicaAckRequired(member protocol.PoolMember) bool {
	return member.Status == "ready" && !member.ReplicaAckExcluded
}

// PushPoolAuthorityReplicas advances and persists this master's authority generation, then
// pushes the current replica to every ready member that is behind. It is single-flight per
// store, safe to call from every probe tick, and safe to retry: a member acknowledges the
// same generation idempotently.
func PushPoolAuthorityReplicas(ctx context.Context, s *Store, timeout time.Duration) error {
	poolReplicaMu.Lock()
	if poolReplicaPushing[s.dir] {
		poolReplicaMu.Unlock()
		return errPoolAuthorityReplicaBusy
	}
	poolReplicaPushing[s.dir] = true
	poolReplicaMu.Unlock()
	defer func() {
		poolReplicaMu.Lock()
		delete(poolReplicaPushing, s.dir)
		poolReplicaMu.Unlock()
	}()
	if err := reconcilePoolStorageLeaseSelection(s); err != nil {
		return err
	}
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.StorageLease != nil {
		if _, err = preparePoolStorageLeaseHost(ctx, s, membership, true); err != nil {
			return err
		}
	}
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
			if poolAuthorityReplicaAckRequired(member) {
				failed = err
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if failed == nil {
		if err = markPoolStorageLeasePrepared(s, generation, digest); err != nil {
			failed = err
		}
	}
	if failed == nil {
		if err = holdPoolStorageAuthorityLease(ctx, s); err != nil {
			failed = err
		}
	}
	// Once every ready member holds a replica built after this host's own fence profile was
	// cleared, no member can still fence-promote it, and its restart no longer has to wait
	// for anyone.
	ReleasePoolSelfFenceExposure(s)
	return failed
}

// holdPoolStorageAuthorityLease acquires the single authority resource only after the
// exact lockspaces and membership IDs have been acknowledged. Native watchdog readiness
// is not reset proof; until that proof is independently available this path remains inert.
func holdPoolStorageAuthorityLease(ctx context.Context, s *Store) error {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID || membership.StorageLease == nil || membership.StorageLease.PreparedGeneration == 0 {
		return nil
	}
	lease := membership.StorageLease
	self, ok := poolMemberByID(membership, s.State.HostID)
	if !ok || self.Watchdog == nil {
		return errors.New("pool storage authority watchdog configuration is unavailable")
	}
	watchdog, err := vmhost.CheckSanlockWatchdogReady(ctx, *self.Watchdog)
	if err != nil || !watchdog.ResetVerified {
		return errors.New("pool storage authority lease is gated on verified hardware reset")
	}
	var repository *protocol.PoolStorageLeaseRepository
	for i := range lease.Repositories {
		if lease.Repositories[i].RepositoryID == lease.AuthorityRepositoryID {
			repository = &lease.Repositories[i]
			break
		}
	}
	if repository == nil {
		return errors.New("pool storage authority repository is unavailable")
	}
	registrations, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedStorage()
	if !managedVMChecksReady(checks) {
		return errors.New("pool storage authority repository state is unavailable")
	}
	for _, storage := range registrations {
		if storage.ID != repository.RepositoryID || storage.Tombstone || storage.Generation != repository.RepositoryGeneration ||
			storage.Identity.String() != repository.RepositoryIdentity.String() || !poolNFSIdentity(storage.Identity) || !protocol.IsPoolSharedStorage(storage) {
			continue
		}
		storage, err = vmhost.ValidatePoolStorageAccess(ctx, storage)
		if err != nil || storage.Identity.String() != repository.RepositoryIdentity.String() {
			return errors.New("pool storage authority repository is not mounted with the recorded NFS identity")
		}
		resource, err := vmhost.SanlockAuthorityLease(membership.PoolID, repository.Lockspace, storage.Path)
		if err != nil {
			return errors.New("pool storage authority resource path is invalid")
		}
		initialize := false
		if _, statErr := os.Lstat(resource.Path); os.IsNotExist(statErr) {
			initialize = true
		} else if statErr != nil {
			return errors.New("pool storage authority resource state is unavailable")
		}
		if err = vmhost.EnsureSanlockResource(ctx, vmhost.SanlockResourceConfig{
			SanlockResource: resource, Initialize: initialize, MasterAuthorized: initialize,
		}); err != nil {
			return err
		}
		status, err := vmhost.SetSanlockAuthorityLease(ctx, resource, true)
		if err != nil || status.State != vmhost.SanlockAuthorityHeld {
			return errors.New("pool storage authority lease is not confirmed held")
		}
		return nil
	}
	return errors.New("pool storage authority repository does not match this host's applied registration")
}

// markPoolStorageLeasePrepared enables the durable lease record only after every ready
// member has acknowledged the exact authority replica containing its IDs and lockspace.
func markPoolStorageLeasePrepared(s *Store, generation uint64, digest string) error {
	_, currentDigest, err := buildPoolAuthorityReplica(s)
	if err != nil {
		return err
	}
	if currentDigest != digest {
		// A member reported a new native host generation during this push. That
		// durable observation advances authority; all ready members must ACK it first.
		return nil
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil || state.Membership.MasterHostID != s.State.HostID {
		return errors.New("pool membership is unavailable for storage-lease preparation")
	}
	lease := state.Membership.StorageLease
	if lease == nil || lease.PreparedGeneration != 0 {
		return nil
	}
	for _, member := range state.Membership.Members {
		if member.Status != "ready" {
			continue
		}
		if member.SanlockHostID == 0 || member.SanlockHostID > protocol.PoolSanlockMaxHostID {
			return errors.New("storage-lease host IDs are not assigned to every ready member")
		}
		if member.HostID == s.State.HostID {
			continue
		}
		if member.PeerOrigin == "" || !poolReplicaPostAckMatches(s, member.HostID, generation, digest) {
			return errors.New("every ready member must acknowledge the storage-lease host IDs and lockspace before activation")
		}
	}
	if err = poolAuthorityWritable(state); err != nil {
		return err
	}
	lease.PreparedGeneration = generation
	return savePoolPeer(s, state)
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
	if poolReplicaPostAckMatches(s, member.HostID, replica.Generation, digest) &&
		status.Verified && status.Error == "" && status.Generation == replica.Generation && status.Digest == digest {
		// The member already holds this generation; this read is the fresh acknowledgement,
		// or the master's check goes stale a minute after every push (BUG-0542).
		recordPoolReplicaAck(s, member.HostID, status)
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
	addressed.WitnessCredentials = nil
	if witness := replica.Membership.Witness; witness != nil && witness.CredentialsReady {
		bundle, bundleErr := vmhost.PoolWitnessBundle(s.dir, witness.FencingGeneration)
		if bundleErr != nil {
			return bundleErr
		}
		wrapped, wrapErr := wrapPoolWitnessCredential(bundle, replica.PoolID, member.HostID, replica.Generation, key)
		if wrapErr != nil {
			return wrapErr
		}
		addressed.WitnessCredentials = []protocol.PoolWrappedWitnessCredential{{HostID: member.HostID, FencingGeneration: witness.FencingGeneration, Wrapped: wrapped}}
	}
	if err = signPoolAuthorityReplica(signer, &addressed); err != nil {
		return err
	}
	if addressed.BackupManifest != nil {
		probe, probeErr := json.Marshal(poolBackupPageUpload{Replica: addressed, Index: -1})
		if probeErr != nil {
			return probeErr
		}
		probeRequest, probeErr := http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolBackupPagePath, bytes.NewReader(probe))
		if probeErr != nil {
			return probeErr
		}
		probeRequest.Header.Set("Content-Type", "application/json")
		probeResponse, probeErr := client.Do(probeRequest)
		if probeErr != nil {
			return probeErr
		}
		var progress poolBackupPageProgress
		if probeResponse.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(probeResponse.Body, 1<<12)).Decode(&progress) != nil {
			_ = probeResponse.Body.Close()
			return errors.New("pool member did not report backup authority page progress or does not support " + protocol.PoolBackupPagedAuthorityCapability)
		}
		_ = probeResponse.Body.Close()
		if progress.NextIndex < 0 || progress.NextIndex > len(addressed.BackupManifest.PageSHA256) {
			return errors.New("pool member reported invalid backup authority page progress")
		}
		for index := progress.NextIndex; index < len(addressed.BackupManifest.PageSHA256); index++ {
			page, pageErr := readPoolBackupPage(s, addressed.BackupManifest, index)
			if pageErr != nil {
				return pageErr
			}
			payload, pageErr := json.Marshal(poolBackupPageUpload{Replica: addressed, Index: index, Data: page})
			if pageErr != nil {
				return pageErr
			}
			pageRequest, pageErr := http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolBackupPagePath, bytes.NewReader(payload))
			if pageErr != nil {
				return pageErr
			}
			pageRequest.Header.Set("Content-Type", "application/json")
			pageResponse, pageErr := client.Do(pageRequest)
			if pageErr != nil {
				return pageErr
			}
			_ = pageResponse.Body.Close()
			if pageResponse.StatusCode != http.StatusNoContent {
				return errors.New("pool member refused a backup authority page or does not support " + protocol.PoolBackupPagedAuthorityCapability)
			}
		}
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
	if replica.Membership.StorageLease != nil {
		if ack.SanlockHostGeneration == 0 {
			return errors.New("pool member acknowledged the replica without joining the authority sanlock lockspace")
		}
		if err = recordPoolSanlockHostGeneration(s, member.HostID, ack.SanlockHostGeneration); err != nil {
			return err
		}
	}
	postAck := protocol.PoolReplicaStatus{
		Generation: ack.Generation, Digest: ack.Digest, MasterHostID: s.State.HostID,
		SignedAt: addressed.CreatedAt, Verified: true,
	}
	recordPoolReplicaPostAck(s, member.HostID, postAck)
	recordPoolReplicaAck(s, member.HostID, postAck)
	return nil
}

func recordPoolSanlockHostGeneration(s *Store, hostID string, generation uint64) error {
	if generation == 0 {
		return errors.New("sanlock host generation is unavailable")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.MasterHostID != s.State.HostID {
		return errors.New("pool master membership is unavailable")
	}
	for i := range state.Membership.Members {
		member := &state.Membership.Members[i]
		if member.HostID != hostID {
			continue
		}
		if member.SanlockHostID == 0 || generation < member.SanlockHostGeneration {
			return errors.New("pool member reported an invalid sanlock host generation")
		}
		if member.SanlockHostGeneration == generation {
			return nil
		}
		if err = poolAuthorityWritable(state); err != nil {
			return err
		}
		member.SanlockHostGeneration = generation
		return savePoolPeer(s, state)
	}
	return errors.New("sanlock host generation names a nonmember")
}
