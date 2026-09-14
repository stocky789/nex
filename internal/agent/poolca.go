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
	Membership                              protocol.PoolMembership
	Offers                                  map[string]poolJoinOffer
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
