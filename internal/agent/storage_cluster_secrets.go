// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Cluster secrets are deliberately outside the managed-storage journal and
// inventory. They are root-private local material received over an authenticated
// transient handoff; this file only records opaque scope and replay proof.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const clusterSecretDirectory = "storage-cluster/secrets"
const probeSecretTTL = 10 * time.Minute

var clusterSecretMu sync.Mutex

// ClusterSecretReceipt is safe to retain only as an opaque reference. Its scope
// binds a secret to one local Core recipient and one cluster request.
type ClusterSecretReceipt struct {
	Reference         string    `json:"reference"`
	RequestID         string    `json:"request_id"`
	ClusterRequestID  string    `json:"cluster_request_id"`
	PoolID            string    `json:"pool_id"`
	HostID            string    `json:"host_id"`
	RecipientHostID   string    `json:"recipient_host_id"`
	FenceTargetHostID string    `json:"fence_target_host_id,omitempty"`
	RepositoryID      string    `json:"repository_id,omitempty"`
	Kind              string    `json:"kind"` // chap, fence_bmc, fence_ssh, bmc_probe
	CreatedAt         time.Time `json:"created_at"`
}

// ClusterSecretMaterial is accepted only on the transient Core handoff. It is
// never returned by receipt/status/inventory helpers.
type ClusterSecretMaterial struct {
	Credentials   *protocol.StorageCredentials `json:"credentials,omitempty"`
	SSHPrivateKey string                       `json:"ssh_private_key,omitempty"`
	SSHKnownHosts string                       `json:"ssh_known_hosts,omitempty"`
}

// StorageClusterSecretProjection is private peer payload. It must never cross
// the Controller report/API boundary or be added to durable cluster operations.
type StorageClusterSecretProjection struct {
	Receipt  ClusterSecretReceipt  `json:"receipt"`
	Material ClusterSecretMaterial `json:"material"`
}

// StorageClusterSecretBindings are local opaque references after a member has
// accepted a private peer projection.
type StorageClusterSecretBindings struct {
	CHAPReceipt string
	FenceBMC    map[string]string
	FenceSSH    map[string]string
}

type storedClusterSecret struct {
	Receipt  ClusterSecretReceipt  `json:"receipt"`
	Material ClusterSecretMaterial `json:"material"`
	Proof    string                `json:"proof"`
}

func clusterSecretID(v string) bool {
	return v != "" && len(v) <= 128 && !strings.ContainsAny(v, "/\\\x00\r\n")
}

func validClusterSecret(s *Store, r ClusterSecretReceipt, m ClusterSecretMaterial) error {
	if s == nil || s.State.PoolID == "" || s.State.HostID == "" || r.PoolID != s.State.PoolID || r.HostID != s.State.HostID || !clusterSecretID(r.RecipientHostID) || !clusterSecretID(r.RequestID) || !clusterSecretID(r.ClusterRequestID) || (r.RepositoryID != "" && !clusterSecretID(r.RepositoryID)) {
		return errors.New("invalid cluster secret scope")
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != r.PoolID {
		return errors.New("cluster secret recipient is not an admitted member")
	}
	ready, targetReady := false, r.Kind == "chap"
	for _, member := range membership.Members {
		ready = ready || member.HostID == r.RecipientHostID && member.Status == "ready"
		targetReady = targetReady || member.HostID == r.FenceTargetHostID && member.Status == "ready"
	}
	if !ready || !targetReady {
		return errors.New("cluster secret recipient is not an admitted member")
	}
	if r.RepositoryID == "" && r.ClusterRequestID == "" {
		return errors.New("unbound cluster secret is invalid")
	}
	if m.Credentials != nil && (len(m.Credentials.Username) > 4096 || len(m.Credentials.Secret) == 0 || len(m.Credentials.Secret) > 65536 || len(m.Credentials.Domain) > 253 || strings.ContainsAny(m.Credentials.Username+m.Credentials.Secret+m.Credentials.Domain, "\x00\r\n")) {
		return errors.New("invalid cluster credential")
	}
	switch r.Kind {
	case "chap", "fence_bmc", "bmc_probe":
		if m.Credentials == nil || m.SSHPrivateKey != "" || m.SSHKnownHosts != "" {
			return errors.New("invalid typed cluster credential")
		}
		if r.Kind == "fence_bmc" || r.Kind == "bmc_probe" {
			// Core decides this, not the Controller: the password is about to become the
			// script a fence agent runs, and the agent would strip whitespace Core keeps.
			if err := vmhost.FenceSecretUsable(m.Credentials.Secret); err != nil {
				return err
			}
		}
	case "fence_ssh":
		if m.Credentials != nil || len(m.SSHPrivateKey) == 0 || len(m.SSHPrivateKey) > 65536 || len(m.SSHKnownHosts) == 0 || len(m.SSHKnownHosts) > 65536 || strings.ContainsAny(m.SSHPrivateKey+m.SSHKnownHosts, "\x00") {
			return errors.New("invalid typed cluster credential")
		}
	default:
		return errors.New("unsupported cluster credential")
	}
	if r.Kind == "chap" && r.FenceTargetHostID != "" || r.Kind != "chap" && !clusterSecretID(r.FenceTargetHostID) {
		return errors.New("invalid fence credential target")
	}
	return nil
}

func clusterSecretDir(s *Store) (string, error) {
	if s == nil || !filepath.IsAbs(s.dir) || filepath.Clean(s.dir) != s.dir {
		return "", errors.New("invalid cluster secret directory")
	}
	dir := filepath.Join(s.dir, clusterSecretDirectory)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("cluster secret directory must be private")
	}
	return dir, nil
}

func clusterSecretKey(s *Store) ([]byte, error) {
	dir, err := clusterSecretDir(s)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "receipt-key")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("cluster receipt key must be private")
		}
		key, err := os.ReadFile(path)
		if err != nil || len(key) != 32 {
			return nil, errors.New("cluster receipt key is invalid")
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := writePrivate(path, key, 0600); err != nil {
		return nil, err
	}
	return key, nil
}

func clusterSecretPath(s *Store, r ClusterSecretReceipt) (string, error) {
	dir, err := clusterSecretDir(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(r.RequestID + "\x00" + r.ClusterRequestID + "\x00" + r.Kind + "\x00" + r.RecipientHostID))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
}

func readClusterSecret(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("cluster secret receipt is unsafe")
	}
	return os.ReadFile(path)
}

func clusterSecretProof(key []byte, r ClusterSecretReceipt, m ClusterSecretMaterial) string {
	b, _ := json.Marshal(struct {
		Receipt  ClusterSecretReceipt  `json:"receipt"`
		Material ClusterSecretMaterial `json:"material"`
	}{r, m})
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func privateReference() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writePrivate(path string, data []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != mode {
			return errors.New("cluster secret path is unsafe")
		}
		if old, readErr := os.ReadFile(path); readErr == nil && string(old) == string(data) {
			return nil
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return vmhost.AtomicWrite(path, data, mode)
}

func materializeClusterSecret(s *Store, r ClusterSecretReceipt, m ClusterSecretMaterial) error {
	if r.Kind == "chap" {
		return nil
	} // Native iSCSI receives it through the existing credential receipt flow.
	dir := filepath.Join(s.dir, "storage-cluster", "fence-credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("fence credential directory must be private")
	}
	if r.Kind == "fence_ssh" {
		if err := writePrivate(filepath.Join(dir, r.Reference), []byte(m.SSHPrivateKey), 0600); err != nil {
			return err
		}
		return writePrivate(filepath.Join(dir, r.Reference+".known_hosts"), []byte(materializedKnownHosts(m.SSHKnownHosts)), 0600)
	}
	return writePrivate(filepath.Join(dir, r.Reference), vmhost.FencePasswordScript(m.Credentials.Secret), 0700)
}

// materializedKnownHosts bridges OpenSSH's default-port lookup: it looks up
// host while operators often supplied the equivalent [host]:22 form. Leave
// every ambiguous form untouched, including IPv6, aliases and hashed entries.
func materializedKnownHosts(in string) string {
	lines := strings.Split(in, "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.Contains(fields[0], ",") || !strings.HasPrefix(fields[0], "[") || !strings.HasSuffix(fields[0], "]:22") {
			continue
		}
		host := strings.TrimSuffix(strings.TrimPrefix(fields[0], "["), "]:22")
		if host == "" || strings.Contains(host, ":") || !knownHostName(host) {
			continue
		}
		lines[i] = strings.Replace(line, fields[0], host, 1)
	}
	return strings.Join(lines, "\n")
}

func knownHostName(host string) bool {
	for _, r := range host {
		if r != '.' && r != '-' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// SaveClusterSecret atomically persists a transient secret and returns only its
// opaque receipt. Retrying the same scope with different bytes is rejected.
func SaveClusterSecret(s *Store, r ClusterSecretReceipt, m ClusterSecretMaterial) (ClusterSecretReceipt, error) {
	var empty ClusterSecretReceipt
	if err := validClusterSecret(s, r, m); err != nil {
		return empty, err
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	key, err := clusterSecretKey(s)
	if err != nil {
		return empty, err
	}
	path, err := clusterSecretPath(s, r)
	if err != nil {
		return empty, err
	}
	if b, err := readClusterSecret(path); err == nil {
		var old storedClusterSecret
		if json.Unmarshal(b, &old) != nil || old.Receipt.Reference == "" || old.Proof == "" || !hmac.Equal([]byte(old.Proof), []byte(clusterSecretProof(key, old.Receipt, old.Material))) || old.Receipt.RequestID != r.RequestID || old.Receipt.ClusterRequestID != r.ClusterRequestID || old.Receipt.PoolID != r.PoolID || old.Receipt.HostID != r.HostID || old.Receipt.RecipientHostID != r.RecipientHostID || old.Receipt.Kind != r.Kind || (r.RepositoryID != "" && old.Receipt.RepositoryID != r.RepositoryID) || !hmac.Equal([]byte(old.Proof), []byte(clusterSecretProof(key, old.Receipt, m))) {
			return empty, errors.New("cluster secret replay conflicts with receipt")
		}
		return old.Receipt, materializeClusterSecret(s, old.Receipt, old.Material)
	} else if !os.IsNotExist(err) {
		return empty, err
	}
	if r.Kind == "chap" {
		// Reuse the existing private credential store so iSCSI can consume the
		// receipt without duplicating its native password-file mechanism.
		generic, saveErr := vmhost.SaveStorageCredential(s.dir, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{RequestID: r.RequestID, RegistrationRequestID: r.ClusterRequestID, PoolID: r.PoolID, HostID: r.HostID, RepositoryID: r.RepositoryID}, Credentials: *m.Credentials})
		if saveErr != nil {
			return empty, saveErr
		}
		r.Reference = generic.Receipt
	} else {
		if r.Reference == "" {
			r.Reference, err = privateReference()
			if err != nil {
				return empty, err
			}
		}
	}
	r.CreatedAt = time.Now().UTC()
	stored := storedClusterSecret{Receipt: r, Material: m}
	stored.Proof = clusterSecretProof(key, r, m)
	b, err := json.Marshal(stored)
	if err != nil {
		return empty, err
	}
	if err = writePrivate(path, b, 0600); err != nil {
		return empty, err
	}
	if err = materializeClusterSecret(s, r, m); err != nil {
		return empty, err
	}
	return r, nil
}

// A probe receipt is never attached to a pool record. Retire both its private receipt and
// its materialized fence script; expiry covers a handoff whose brokered probe never ran.
func retireProbeSecret(s *Store, reference string) error {
	if reference == "" {
		return errors.New("management controller probe credential reference is missing")
	}
	return retireProbeSecrets(s, reference, time.Time{})
}

func retireExpiredProbeSecrets(s *Store, now time.Time) error {
	return retireProbeSecrets(s, "", now.Add(-probeSecretTTL))
}

// readProbeSecret accepts only a signed, probe-scoped handoff for this exact
// request and target. A fence credential script alone is not authority to probe.
func readProbeSecret(s *Store, request protocol.PoolRequest) (string, error) {
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	dir, err := clusterSecretDir(s)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	key, err := clusterSecretKey(s)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readClusterSecret(filepath.Join(dir, entry.Name()))
		if err != nil {
			return "", err
		}
		var stored storedClusterSecret
		if json.Unmarshal(b, &stored) != nil || stored.Receipt.Reference != request.CredentialRef {
			continue
		}
		r := stored.Receipt
		if r.Kind != "bmc_probe" || r.PoolID != request.PoolID || r.HostID != request.HostID || r.RecipientHostID != request.HostID || r.FenceTargetHostID != request.MemberHostID || r.ClusterRequestID != request.RequestID || r.CreatedAt.IsZero() || time.Since(r.CreatedAt) > probeSecretTTL || stored.Material.Credentials == nil || !hmac.Equal([]byte(stored.Proof), []byte(clusterSecretProof(key, r, stored.Material))) {
			return "", errors.New("management controller probe credential scope is invalid")
		}
		return stored.Material.Credentials.Secret, nil
	}
	return "", errors.New("management controller probe credential is unavailable")
}

func retireProbeSecrets(s *Store, reference string, before time.Time) error {
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	dir, err := clusterSecretDir(s)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	key, err := clusterSecretKey(s)
	if err != nil {
		return err
	}
	var firstErr error
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, err := readClusterSecret(path)
		if err != nil {
			if reference == "" && firstErr == nil {
				firstErr = err
			}
			continue
		}
		var stored storedClusterSecret
		if err := json.Unmarshal(b, &stored); err != nil {
			if reference == "" && firstErr == nil {
				firstErr = err
			}
			continue
		}
		r := stored.Receipt
		if r.Kind != "bmc_probe" || reference != "" && r.Reference != reference || !before.IsZero() && r.CreatedAt.After(before) {
			continue
		}
		if r.Reference == "" || !hmac.Equal([]byte(stored.Proof), []byte(clusterSecretProof(key, r, stored.Material))) {
			if firstErr == nil {
				firstErr = errors.New("management controller probe credential receipt is invalid")
			}
			continue
		}
		if err := vmhost.RetirePoolFenceCredential(s.dir, r.Reference); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// ValidateClusterSecretReceipt is the native-control gate: an opaque receipt
// must still match its original local recipient, request, type and repository.
func ValidateClusterSecretReceipt(s *Store, reference, poolID, hostID, requestID, clusterRequestID, repositoryID, kind string) error {
	if s == nil || !clusterSecretID(reference) || !clusterSecretID(repositoryID) || !clusterSecretID(requestID) || !clusterSecretID(clusterRequestID) || (kind != "chap" && kind != "fence_bmc" && kind != "fence_ssh") || poolID != s.State.PoolID || hostID != s.State.HostID {
		return errors.New("invalid cluster secret receipt")
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	key, err := clusterSecretKey(s)
	if err != nil {
		return err
	}
	dir, err := clusterSecretDir(s)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readClusterSecret(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		var stored storedClusterSecret
		if json.Unmarshal(b, &stored) != nil || !hmac.Equal([]byte(stored.Proof), []byte(clusterSecretProof(key, stored.Receipt, stored.Material))) {
			return errors.New("cluster secret receipt is invalid")
		}
		r := stored.Receipt
		if r.Reference == reference && r.PoolID == poolID && r.HostID == hostID && r.RequestID == requestID && r.ClusterRequestID == clusterRequestID && r.RepositoryID == repositoryID && r.Kind == kind {
			return nil
		}
	}
	return errors.New("cluster secret receipt is unavailable")
}

// BindClusterSecretRepository makes a pre-allocation receipt usable only for
// the repository Core allocated. Binding it to another repository is refused.
func BindClusterSecretRepository(s *Store, reference, poolID, hostID, requestID, clusterRequestID, repositoryID string) error {
	if !clusterSecretID(reference) || !clusterSecretID(repositoryID) || s == nil || poolID != s.State.PoolID || hostID != s.State.HostID {
		return errors.New("invalid cluster secret binding")
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	key, err := clusterSecretKey(s)
	if err != nil {
		return err
	}
	// A receipt reference is unguessable; find only this request's typed receipt.
	dir, err := clusterSecretDir(s)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, e := readClusterSecret(filepath.Join(dir, entry.Name()))
		if e != nil {
			return e
		}
		var stored storedClusterSecret
		if json.Unmarshal(b, &stored) != nil || !hmac.Equal([]byte(stored.Proof), []byte(clusterSecretProof(key, stored.Receipt, stored.Material))) {
			return errors.New("cluster secret receipt is invalid")
		}
		if stored.Receipt.Reference != reference {
			continue
		}
		if stored.Receipt.PoolID != poolID || stored.Receipt.HostID != hostID || stored.Receipt.RequestID != requestID || stored.Receipt.ClusterRequestID != clusterRequestID {
			return errors.New("cluster secret receipt scope mismatch")
		}
		if stored.Receipt.RepositoryID != "" && stored.Receipt.RepositoryID != repositoryID {
			return errors.New("cluster secret is already bound")
		}
		stored.Receipt.RepositoryID = repositoryID
		stored.Proof = clusterSecretProof(key, stored.Receipt, stored.Material)
		out, e := json.Marshal(stored)
		if e != nil {
			return e
		}
		return writePrivate(filepath.Join(dir, entry.Name()), out, 0600)
	}
	return errors.New("cluster secret receipt is unavailable")
}

// ClusterSecretReference resolves a transient credential request to its local
// opaque receipt so the Core operation can bind it after allocating the
// repository. It deliberately exposes no credential material.
func ClusterSecretReference(s *Store, poolID, hostID, recipientHostID, targetHostID, requestID, clusterRequestID, kind string) (string, error) {
	if s == nil || poolID != s.State.PoolID || hostID != s.State.HostID || !clusterSecretID(recipientHostID) || !clusterSecretID(requestID) || !clusterSecretID(clusterRequestID) || (kind != "chap" && kind != "fence_bmc" && kind != "fence_ssh") {
		return "", errors.New("cluster secret receipt is unavailable")
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	key, err := clusterSecretKey(s)
	if err != nil {
		return "", err
	}
	dir, err := clusterSecretDir(s)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readClusterSecret(filepath.Join(dir, entry.Name()))
		if err != nil {
			return "", err
		}
		var stored storedClusterSecret
		if json.Unmarshal(b, &stored) != nil || !hmac.Equal([]byte(stored.Proof), []byte(clusterSecretProof(key, stored.Receipt, stored.Material))) {
			return "", errors.New("cluster secret receipt is invalid")
		}
		r := stored.Receipt
		if r.PoolID == poolID && r.HostID == hostID && r.RequestID == requestID && r.ClusterRequestID == clusterRequestID && r.Kind == kind && ((kind == "chap" && r.RecipientHostID == recipientHostID) || (kind != "chap" && r.RecipientHostID == hostID && r.FenceTargetHostID == targetHostID)) {
			return r.Reference, nil
		}
	}
	return "", errors.New("cluster secret receipt is unavailable")
}

// ResolveStorageClusterCredentials binds transient offers to the Core-allocated
// repository and writes only opaque local refs into the master request.
func ResolveStorageClusterCredentials(s *Store, request *StorageClusterRequest) error {
	if s == nil || request == nil || request.RequestID == "" || request.PoolID != s.State.PoolID || request.HostID != s.State.HostID || request.Config.RepositoryID == "" || request.Registration.ID != request.Config.RepositoryID {
		return errors.New("invalid cluster credential request")
	}
	seen := map[string]bool{}
	for _, ref := range request.CredentialRequests {
		if seen[ref.RequestID] || !clusterSecretID(ref.RequestID) || !clusterSecretID(ref.TargetHostID) || (ref.Kind != "chap" && ref.Kind != "fence_bmc" && ref.Kind != "fence_ssh") {
			return errors.New("invalid cluster credential request")
		}
		seen[ref.RequestID] = true
		secret, err := ClusterSecretReference(s, request.PoolID, s.State.HostID, ref.TargetHostID, ref.TargetHostID, ref.RequestID, request.RequestID, ref.Kind)
		if err != nil || BindClusterSecretRepository(s, secret, request.PoolID, s.State.HostID, ref.RequestID, request.RequestID, request.Config.RepositoryID) != nil {
			return errors.New("cluster credential is unavailable")
		}
		if ValidateClusterSecretReceipt(s, secret, request.PoolID, s.State.HostID, ref.RequestID, request.RequestID, request.Config.RepositoryID, ref.Kind) != nil {
			return errors.New("cluster credential is unavailable")
		}
		switch ref.Kind {
		case "chap":
			if ref.TargetHostID == s.State.HostID {
				if request.Registration.CredentialReceipt != "" && request.Registration.CredentialReceipt != secret {
					return errors.New("multiple master CHAP credentials")
				}
				request.Registration.CredentialReceipt, request.Registration.CredentialRequestID = secret, request.RequestID
			}
		case "fence_bmc", "fence_ssh":
			found := false
			for i := range request.Config.Nodes {
				if request.Config.Nodes[i].HostID != ref.TargetHostID {
					continue
				}
				found = true
				request.Config.Nodes[i].Fence.CredentialRef = secret
				if ref.Kind == "fence_ssh" {
					request.Config.Nodes[i].Fence.SSHHostKeyRef = secret
				}
			}
			if !found {
				return errors.New("cluster credential target is not a configured member")
			}
		}
	}
	return nil
}

// ExportStorageClusterSecrets selects only material whose durable receipt is
// bound to this repository and addressed to the requesting ready peer.
func ExportStorageClusterSecrets(s *Store, poolID, clusterRequestID, repositoryID, recipientHostID string) ([]StorageClusterSecretProjection, error) {
	if s == nil || poolID != s.State.PoolID || !clusterSecretID(clusterRequestID) || !clusterSecretID(repositoryID) || !clusterSecretID(recipientHostID) {
		return nil, errors.New("invalid cluster secret projection")
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return nil, errors.New("only the pool master may export cluster secrets")
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	key, err := clusterSecretKey(s)
	if err != nil {
		return nil, err
	}
	dir, err := clusterSecretDir(s)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []StorageClusterSecretProjection{}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readClusterSecret(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var stored storedClusterSecret
		if json.Unmarshal(b, &stored) != nil || !hmac.Equal([]byte(stored.Proof), []byte(clusterSecretProof(key, stored.Receipt, stored.Material))) {
			return nil, errors.New("cluster secret receipt is invalid")
		}
		r := stored.Receipt
		if r.PoolID == poolID && r.HostID == s.State.HostID && r.ClusterRequestID == clusterRequestID && r.RepositoryID == repositoryID && (r.Kind != "chap" || r.RecipientHostID == recipientHostID) {
			if err := materializeClusterSecret(s, r, stored.Material); err != nil {
				return nil, err
			}
			out = append(out, StorageClusterSecretProjection{Receipt: r, Material: stored.Material})
		}
	}
	return out, nil
}

// ImportStorageClusterSecrets makes private peer material local to one member
// and returns the member-local refs to install into its config/registration.
func ImportStorageClusterSecrets(s *Store, projections []StorageClusterSecretProjection) (StorageClusterSecretBindings, error) {
	out := StorageClusterSecretBindings{FenceBMC: map[string]string{}, FenceSSH: map[string]string{}}
	membership, err := Membership(s)
	if err != nil {
		return out, errors.New("cluster secret projection scope mismatch")
	}
	for _, projection := range projections {
		r := projection.Receipt
		if r.PoolID != s.State.PoolID || r.RepositoryID == "" || (r.Kind == "chap" && r.RecipientHostID != s.State.HostID) || (r.Kind != "chap" && r.RecipientHostID != membership.MasterHostID) {
			return out, errors.New("cluster secret projection scope mismatch")
		}
		r.HostID, r.RecipientHostID = s.State.HostID, s.State.HostID
		stored, err := SaveClusterSecret(s, r, projection.Material)
		if err != nil {
			return out, err
		}
		switch stored.Kind {
		case "chap":
			if out.CHAPReceipt != "" && out.CHAPReceipt != stored.Reference {
				return out, errors.New("multiple member CHAP credentials")
			}
			out.CHAPReceipt = stored.Reference
		case "fence_bmc":
			out.FenceBMC[stored.FenceTargetHostID] = stored.Reference
		case "fence_ssh":
			out.FenceSSH[stored.FenceTargetHostID] = stored.Reference
		}
	}
	return out, nil
}

// ClusterCorosyncAuthKey creates one random pool key on the Core that owns this
// state. Peer delivery is authenticated separately and never uses inventory.
func ClusterCorosyncAuthKey(s *Store, poolID string) ([]byte, error) {
	if s == nil || poolID == "" || poolID != s.State.PoolID {
		return nil, errors.New("invalid Corosync key scope")
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != poolID || membership.MasterHostID != s.State.HostID {
		return nil, errors.New("only the pool master may create the Corosync key")
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	return clusterCorosyncAuthKey(s, nil)
}

// SaveClusterCorosyncAuthKey persists a master-delivered key on an admitted
// peer. Peer mTLS authentication belongs to the caller; differing key material
// is never allowed to replace an existing pool key.
func SaveClusterCorosyncAuthKey(s *Store, poolID string, key []byte) error {
	if s == nil || poolID == "" || poolID != s.State.PoolID || len(key) != 128 {
		return errors.New("invalid Corosync key scope")
	}
	membership, err := Membership(s)
	if err != nil || membership.PoolID != poolID {
		return errors.New("Corosync key recipient is not an admitted member")
	}
	ready := false
	for _, member := range membership.Members {
		ready = ready || member.HostID == s.State.HostID && member.Status == "ready"
	}
	if !ready {
		return errors.New("Corosync key recipient is not an admitted member")
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	_, err = clusterCorosyncAuthKey(s, key)
	return err
}

func clusterCorosyncAuthKey(s *Store, supplied []byte) ([]byte, error) {
	dir := filepath.Join(s.dir, "storage-cluster")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("cluster state directory must be private")
	}
	path := filepath.Join(dir, "corosync-authkey")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return nil, errors.New("Corosync key must be private")
		}
		key, err := os.ReadFile(path)
		if err != nil || len(key) != 128 || len(supplied) != 0 && !hmac.Equal(key, supplied) {
			return nil, errors.New("Corosync key is invalid")
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := supplied
	if len(key) == 0 {
		key = make([]byte, 128)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
	}
	if err := writePrivate(path, key, 0600); err != nil {
		return nil, err
	}
	return key, nil
}

// ClusterFenceSecretPaths returns fixed, private paths for native fencing only.
func ClusterFenceSecretPaths(s *Store, reference, kind string) (string, string, error) {
	if s == nil || !clusterSecretID(reference) || (kind != "fence_bmc" && kind != "fence_ssh") {
		return "", "", errors.New("invalid fence secret reference")
	}
	dir := filepath.Join(s.dir, "storage-cluster", "fence-credentials")
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", "", errors.New("fence credential is unavailable")
	}
	credential := filepath.Join(dir, reference)
	mode := os.FileMode(0700)
	knownHosts := ""
	if kind == "fence_ssh" {
		mode, knownHosts = 0600, credential+".known_hosts"
	}
	if info, err := os.Lstat(credential); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return "", "", errors.New("fence credential is unavailable")
	}
	if knownHosts != "" {
		if info, err := os.Lstat(knownHosts); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return "", "", errors.New("fence host key is unavailable")
		}
	}
	return credential, knownHosts, nil
}
