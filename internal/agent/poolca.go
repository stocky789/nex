// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const poolPeerStateFile = "pool-peer.json"

// A single agent process owns an agent state directory. This lock additionally serializes
// peer HTTP handlers, whose requests can arrive concurrently in that process.
var poolPeerMu sync.Mutex

type poolPeerState struct {
	CAPEM, CAKeyPEM, CertificatePEM, KeyPEM string
	PendingKeyPEM, PendingCSRPEM            string
	MasterURL                               string
	PendingPeerOrigin                       string
	PendingRehome                           *poolRehome
	PendingJoin                             *poolPendingJoin
	Membership                              protocol.PoolMembership
	Offers                                  map[string]poolJoinOffer
	// AuthorityGeneration is the pool master's own monotonic counter for the signed
	// authority replica it pushes to its members, and AuthorityDigest the content it last
	// numbered. It is persisted before any replica is pushed, so it never goes backwards
	// after a crash; generations may skip. A member leaves both empty.
	AuthorityGeneration uint64
	AuthorityDigest     string
	// Transfer is this host's durable part of a planned master change or a member eject.
	// It carries the authority freeze: while Frozen is set every authority write on this
	// host is refused, and while HandedOff is set they are refused for good, because this
	// host has signed the generation away. Both outlive a restart, a reboot and the loss of
	// the Controller, which is the only reason two hosts can never both write.
	Transfer *poolTransferRecord
	// Restricted is this host's own durable refusal to act on pool authority while it
	// cannot prove it holds the current one: after a restart it has not confirmed with its
	// members, or it has found a higher authority elsewhere. It is set before any start
	// path is exposed and is read by Core alone, including by the libvirt start hook, so it
	// holds through a reboot and through the complete loss of the Controller.
	Restricted *poolRestrictedRecord
	// BootAuthority is written only after this boot has confirmed the authority which can
	// start managed guests. The libvirt hook binds it to this host and pool before allowing
	// an autostart that could otherwise beat the agent process at boot.
	BootAuthority *poolBootAuthorityConfirmation
	// SelfFence records that a fence profile for THIS host, of a kind a promotion could
	// actually turn into a proof, has been part of this pool's replicated authority. It is
	// the durable evidence the boot rule reads: a master no member could ever have fenced
	// cannot have been promoted away behind its back. It outlives the profile itself,
	// because a member holding an older replica still holds the profile that replica
	// carried.
	SelfFence *poolSelfFenceExposure
	// FenceCredentials is the master's private digest of the material behind each recorded
	// fence profile, keyed by the host the profile fences. It never leaves this file: it
	// exists so the master can tell rotated material from the same material re-recorded,
	// and move the replicated credential version only when the material actually changed.
	FenceCredentials map[string]poolFenceCredentialRecord
}

// poolFenceCredentialRecord is a digest and a counter, never material.
type poolFenceCredentialRecord struct {
	Ref     string
	Digest  string
	Version uint64
}

// poolSelfFenceExposure is present exactly while this host may still be fence-promotable by
// somebody. ClearedAt is the authority generation from which the replicated authority no
// longer carries a usable profile for this host; it is zero while the profile is still
// recorded. The whole record is dropped once every ready member has acknowledged a replica
// at or above ClearedAt, because no member can then still hold the profile.
type poolSelfFenceExposure struct{ ClearedAt uint64 }

// poolBootAuthorityConfirmation is private Core state. It is never Controller input or
// inventory: it only binds an authority confirmation to the local kernel boot identity.
type poolBootAuthorityConfirmation struct {
	PoolID string
	HostID string
	BootID string
}

type poolRehome struct{ PoolID, PoolName string }

type poolJoinOffer struct {
	Digest, PoolID, Master, HostID, CSRHash string
	CertificatePEM                          string
	Expires                                 time.Time
}

func poolStatePath(s *Store) string { return filepath.Join(s.dir, poolPeerStateFile) }

func loadPoolPeer(s *Store) (poolPeerState, error) {
	var state poolPeerState
	b, err := os.ReadFile(poolStatePath(s))
	if os.IsNotExist(err) {
		state.Offers = map[string]poolJoinOffer{}
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("invalid pool peer state: %w", err)
	}
	if state.Offers == nil {
		state.Offers = map[string]poolJoinOffer{}
	}
	return state, nil
}

func savePoolPeer(s *Store, state poolPeerState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".pool-peer-")
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
	if err = os.Rename(name, poolStatePath(s)); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func newPoolCA(master string) (caPEM, keyPEM, certPEM, hostKeyPEM string, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	ca := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Nex pool " + master}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return
	}
	caPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	hostKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		err = e
		return
	}
	hostDER, e := x509.MarshalPKCS8PrivateKey(hostKey)
	if e != nil {
		err = e
		return
	}
	hostKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: hostDER}))
	host := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: master}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(2, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	hostCert, e := x509.CreateCertificate(rand.Reader, host, ca, &hostKey.PublicKey, caKey)
	if e != nil {
		err = e
		return
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hostCert}))
	return
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err)
	}
	return n
}

func certificateHash(certPEM string) (string, error) {
	b, _ := pem.Decode([]byte(certPEM))
	if b == nil || b.Type != "CERTIFICATE" {
		return "", errors.New("invalid certificate")
	}
	return fmt.Sprintf("%x", sha256.Sum256(b.Bytes)), nil
}

func poolTLSConfig(s *Store) (*tls.Config, error) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return nil, err
	}
	if state.CertificatePEM == "" || state.KeyPEM == "" || state.CAPEM == "" {
		return nil, errors.New("pool peer identity is unavailable")
	}
	cert, err := tls.X509KeyPair([]byte(state.CertificatePEM), []byte(state.KeyPEM))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(state.CAPEM)) {
		return nil, errors.New("invalid pool CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: roots}, nil
}

// PoolPeerTLSConfig is the TLS configuration for a pool peer listener. It requests and
// verifies a peer certificate when one exists; the one-time join endpoint is the only
// unauthenticated exception and additionally requires an unredeemed offer.
func PoolPeerTLSConfig(s *Store) (*tls.Config, error) { return poolTLSConfig(s) }
