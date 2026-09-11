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
		state.CAPEM, state.CAKeyPEM, state.CertificatePEM, state.KeyPEM, err = newPoolCA(s.State.HostID)
		if err != nil {
			return "", "", err
		}
		hash, hashErr := certificateHash(state.CertificatePEM)
		if hashErr != nil {
			return "", "", hashErr
		}
		state.Membership = protocol.PoolMembership{PoolID: s.State.PoolID, MasterHostID: s.State.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: s.State.HostID, CertificateSHA256: hash, Generation: 1, Status: "ready", ObservedAt: time.Now().UTC()}}}
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
		if r.URL.Path == strings.TrimSuffix(poolStoragePath, "/") || strings.HasPrefix(r.URL.Path, poolStoragePath) {
			PoolStorageHandler(s).ServeHTTP(w, r)
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

func acceptPeerJoin(s *Store, request peerJoinRequest) (peerJoinResponse, error) {
	if request.HostID == "" || len(request.HostID) > 128 || len(request.Offer) > 4096 {
		return peerJoinResponse{}, errors.New("invalid join identity")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return peerJoinResponse{}, err
	}
	if state.CAKeyPEM == "" || state.Membership.Valid() != nil {
		return peerJoinResponse{}, errors.New("not a pool master")
	}
	csrHash := fmt.Sprintf("%x", sha256.Sum256([]byte(request.CSRPEM)))
	digest := offerDigest(request.Offer, state.Membership.PoolID, state.Membership.MasterHostID, request.HostID, csrHash)
	offer, ok := state.Offers[digest]
	if !ok || offer.Digest != digest || offer.PoolID != state.Membership.PoolID || offer.Master != state.Membership.MasterHostID || offer.HostID != request.HostID || offer.CSRHash != csrHash || time.Now().After(offer.Expires) {
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
	for _, member := range state.Membership.Members {
		if member.HostID == request.HostID {
			return peerJoinResponse{}, errors.New("host is already a pool member")
		}
	}
	state.Membership.Generation++
	for i := range state.Membership.Members {
		state.Membership.Members[i].Generation = state.Membership.Generation
	}
	state.Membership.Members = append(state.Membership.Members, protocol.PoolMember{HostID: request.HostID, CertificateSHA256: hash, Generation: state.Membership.Generation, Status: "ready", ObservedAt: time.Now().UTC()})
	// Retain this exact certificate for a retry after a lost response. It cannot
	// admit a different member or public key, and the private record still expires.
	offer.CertificatePEM = cert
	state.Offers[digest] = offer
	if err = savePoolPeer(s, state); err != nil {
		return peerJoinResponse{}, err
	}
	return peerJoinResponse{CertificatePEM: cert, Membership: state.Membership}, nil
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
	if _, err := peerOrigin(masterURL); err != nil {
		return nil, err
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return nil, err
	}
	if err = state.Membership.Valid(); err != nil || state.CAPEM == "" || state.CertificatePEM == "" || state.KeyPEM == "" {
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
		return verifyMaster(c, roots, state.Membership.MasterHostID)
	}}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: config}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("peer redirects refused") }}, nil
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
	if state.Membership.PoolID != "" && (state.Membership.PoolID != joined.Membership.PoolID || state.Membership.MasterHostID != joined.Membership.MasterHostID) {
		return errors.New("host already belongs to another pool")
	}
	state.CAPEM, state.CertificatePEM, state.KeyPEM, state.Membership, state.MasterURL = poolCAPEM, joined.CertificatePEM, key, joined.Membership, u.String()
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
	if len(c.PeerCertificates) == 0 {
		return errors.New("pool master did not present a certificate")
	}
	leaf := c.PeerCertificates[0]
	if leaf.Subject.CommonName != host {
		return errors.New("pool master identity does not match offer")
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
