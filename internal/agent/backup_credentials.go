// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"crypto/hmac"
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

type backupCredentialReceipt struct {
	Reference    string    `json:"reference"`
	RequestID    string    `json:"request_id"`
	OperationID  string    `json:"operation_id"`
	PoolID       string    `json:"pool_id"`
	HostID       string    `json:"host_id"`
	RepositoryID string    `json:"repository_id"`
	Kind         string    `json:"kind"`
	CreatedAt    time.Time `json:"created_at"`
	RetiredAt    time.Time `json:"retired_at,omitempty"`
}

type storedBackupCredential struct {
	Receipt     backupCredentialReceipt     `json:"receipt"`
	Credentials protocol.StorageCredentials `json:"credentials"`
	Proof       string                      `json:"proof"`
}

var backupCredentialPreparationMu sync.Mutex
var backupCredentialPreparations = map[string]int{}

func beginBackupCredentialPreparation(s *Store, reference string) func() {
	key := s.dir + "\x00" + reference
	backupCredentialPreparationMu.Lock()
	backupCredentialPreparations[key]++
	backupCredentialPreparationMu.Unlock()
	return func() {
		backupCredentialPreparationMu.Lock()
		backupCredentialPreparations[key]--
		if backupCredentialPreparations[key] == 0 {
			delete(backupCredentialPreparations, key)
		}
		backupCredentialPreparationMu.Unlock()
	}
}

func backupCredentialKind(kind string) bool {
	return kind == protocol.BackupRepoKindSMB3 || kind == protocol.BackupRepoKindS3 || kind == protocol.BackupRepoKindSFTP
}

func backupCredentialScope(s *Store, h protocol.StorageCredentialHandoff) (backupCredentialReceipt, error) {
	o := h.Offer
	parts := strings.Split(o.RegistrationRequestID, ":")
	if s == nil || len(parts) != 3 || parts[0] != "backup-credential" || !backupCredentialKind(parts[1]) ||
		!protocol.ValidSnapshotOperationID(parts[2]) || !protocol.ValidSnapshotOperationID(o.RequestID) ||
		!protocol.ValidSnapshotOperationID(o.RepositoryID) || o.PoolID != s.State.PoolID || o.HostID != s.State.HostID ||
		o.PoolID == "" || o.HostID == "" || o.ExpiresAt.IsZero() || time.Now().After(o.ExpiresAt) {
		return backupCredentialReceipt{}, errors.New("invalid backup credential scope")
	}
	c := h.Credentials
	if c.Username == "" || len(c.Username) > 4096 || len(c.Secret) > 65536 || len(c.SSHPrivateKey) > 65536 || c.Domain != "" || c.SSHKnownHosts != "" ||
		strings.ContainsAny(c.Username+c.Secret+c.SSHPrivateKey, "\x00") || strings.ContainsAny(c.Username+c.Secret, "\r\n") {
		return backupCredentialReceipt{}, errors.New("invalid backup credential material")
	}
	if parts[1] == protocol.BackupRepoKindSFTP {
		if c.SSHPrivateKey == "" || c.Secret != "" {
			return backupCredentialReceipt{}, errors.New("invalid SFTP backup credential")
		}
	} else if c.Secret == "" || c.SSHPrivateKey != "" {
		return backupCredentialReceipt{}, errors.New("invalid backup credential material")
	}
	return backupCredentialReceipt{RequestID: o.RequestID, OperationID: parts[2], PoolID: o.PoolID, HostID: o.HostID, RepositoryID: o.RepositoryID, Kind: parts[1]}, nil
}

func backupCredentialDir(s *Store) (string, error) {
	root, err := clusterSecretDir(s)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "backup")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("backup credential directory must be private")
	}
	return dir, nil
}

func backupCredentialPath(s *Store, requestID string) (string, error) {
	dir, err := backupCredentialDir(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(requestID))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
}

func backupCredentialProof(key []byte, r backupCredentialReceipt, c protocol.StorageCredentials) string {
	b, _ := json.Marshal(struct {
		Receipt     backupCredentialReceipt     `json:"receipt"`
		Credentials protocol.StorageCredentials `json:"credentials"`
	}{r, c})
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func saveBackupCredential(s *Store, h protocol.StorageCredentialHandoff) (protocol.StorageCredentialReceipt, error) {
	var empty protocol.StorageCredentialReceipt
	receipt, err := backupCredentialScope(s, h)
	if err != nil {
		return empty, err
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	key, err := clusterSecretKey(s)
	if err != nil {
		return empty, err
	}
	path, err := backupCredentialPath(s, receipt.RequestID)
	if err != nil {
		return empty, err
	}
	if b, err := readClusterSecret(path); err == nil {
		var old storedBackupCredential
		if json.Unmarshal(b, &old) != nil || old.Receipt.Reference == "" || old.Receipt.RequestID != receipt.RequestID ||
			old.Receipt.OperationID != receipt.OperationID || old.Receipt.PoolID != receipt.PoolID || old.Receipt.HostID != receipt.HostID ||
			old.Receipt.RepositoryID != receipt.RepositoryID || old.Receipt.Kind != receipt.Kind ||
			!hmac.Equal([]byte(old.Proof), []byte(backupCredentialProof(key, old.Receipt, old.Credentials))) ||
			!hmac.Equal([]byte(old.Proof), []byte(backupCredentialProof(key, old.Receipt, h.Credentials))) {
			return empty, errors.New("backup credential replay conflicts with receipt")
		}
		if !old.Receipt.RetiredAt.IsZero() {
			return empty, errors.New("backup credential receipt was retired")
		}
		receipt = old.Receipt
	} else if !os.IsNotExist(err) {
		return empty, err
	} else {
		receipt.Reference, err = privateReference()
		if err != nil {
			return empty, err
		}
		receipt.CreatedAt = time.Now().UTC()
		stored := storedBackupCredential{Receipt: receipt, Credentials: h.Credentials}
		stored.Proof = backupCredentialProof(key, receipt, h.Credentials)
		b, err := json.Marshal(stored)
		if err != nil {
			return empty, err
		}
		if err := writePrivate(path, b, 0600); err != nil {
			return empty, err
		}
	}
	return protocol.StorageCredentialReceipt{Receipt: receipt.Reference, RequestID: receipt.RequestID, PoolID: receipt.PoolID, HostID: receipt.HostID,
		RepositoryID: receipt.RepositoryID, RegistrationRequestID: h.Offer.RegistrationRequestID, Status: "ready", CreatedAt: receipt.CreatedAt}, nil
}

// ResolveBackupCredential accepts only the exact durable local repository
// generation. A stale authority or rotated reference cannot reuse old material.
func ResolveBackupCredential(s *Store, repo protocol.BackupRepository) (protocol.StorageCredentials, error) {
	if s == nil {
		return protocol.StorageCredentials{}, errors.New("backup credential scope is invalid")
	}
	current, err := GetPoolBackupRepository(s, repo.ID)
	if err != nil || current.PoolID != repo.PoolID || current.Kind != repo.Kind ||
		current.Generation != repo.Generation || current.CredentialRef != repo.CredentialRef {
		return protocol.StorageCredentials{}, errors.New("backup credential repository is stale")
	}
	return resolveBackupCredential(s, repo, "")
}

// ResolveBackupCredentialForRegistration is limited to an authenticated
// registration operation before its repository record is committed.
func ResolveBackupCredentialForRegistration(s *Store, repo protocol.BackupRepository, operationID string) (protocol.StorageCredentials, error) {
	if !protocol.ValidSnapshotOperationID(operationID) {
		return protocol.StorageCredentials{}, errors.New("backup credential registration is invalid")
	}
	return resolveBackupCredential(s, repo, operationID)
}

// ResolveBackupCredentialForAdoption is used only while an authenticated peer
// request or verified authority transfer prepares a replica before its row is
// durable. The imported private receipt still has to match this exact ref and
// host; the caller supplies the authoritative repository generation.
func ResolveBackupCredentialForAdoption(s *Store, repo protocol.BackupRepository) (protocol.StorageCredentials, error) {
	if s == nil || repo.Generation == 0 || repo.Valid() != nil {
		return protocol.StorageCredentials{}, errors.New("backup credential adoption scope is invalid")
	}
	return resolveBackupCredential(s, repo, "")
}

func resolveBackupCredential(s *Store, repo protocol.BackupRepository, operationID string) (protocol.StorageCredentials, error) {
	var empty protocol.StorageCredentials
	if s == nil || repo.CredentialRef == "" || !protocol.ValidSnapshotOperationID(repo.CredentialRef) ||
		!protocol.ValidSnapshotOperationID(repo.ID) || repo.PoolID != s.State.PoolID || !backupCredentialKind(repo.Kind) {
		return empty, errors.New("backup credential scope is invalid")
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	dir, err := backupCredentialDir(s)
	if err != nil {
		return empty, err
	}
	key, err := clusterSecretKey(s)
	if err != nil {
		return empty, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return empty, err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readClusterSecret(filepath.Join(dir, entry.Name()))
		if err != nil {
			return empty, err
		}
		var stored storedBackupCredential
		if json.Unmarshal(b, &stored) != nil {
			return empty, errors.New("backup credential receipt is corrupt")
		}
		r := stored.Receipt
		if r.Reference != repo.CredentialRef {
			continue
		}
		if r.PoolID != repo.PoolID || r.HostID != s.State.HostID || r.RepositoryID != repo.ID || r.Kind != repo.Kind ||
			(operationID != "" && r.OperationID != operationID) ||
			!r.RetiredAt.IsZero() || !protocol.ValidSnapshotOperationID(r.RequestID) || !protocol.ValidSnapshotOperationID(r.OperationID) ||
			!hmac.Equal([]byte(stored.Proof), []byte(backupCredentialProof(key, r, stored.Credentials))) {
			return empty, errors.New("backup credential receipt scope is invalid")
		}
		return stored.Credentials, nil
	}
	return empty, errors.New("backup credential receipt is unavailable")
}

// backupCredentialUnused checks Core's repository and operation records before
// exposing or retiring a handoff that may still complete after a lost reply.
func backupCredentialUnused(s *Store, receipt backupCredentialReceipt, repos poolBackupRepositoriesState) (bool, error) {
	if masterBackupPool(s, receipt.PoolID) != nil || requirePoolAuthorityWritable(s) != nil {
		return false, nil
	}
	for _, repo := range repos.Repositories {
		if repo.CredentialRef == receipt.Reference {
			return false, nil
		}
	}
	entry, journaled := s.State.Journal[receipt.OperationID]
	if journaled {
		if entry.Command == nil || entry.Command.Kind != "backup-repository-register" || entry.Command.BackupRepository == nil ||
			entry.Command.BackupRepository.ID != receipt.RepositoryID || entry.Command.BackupRepository.CredentialRef != receipt.Reference {
			return false, nil
		}
		if entry.Status != "failed" {
			return false, nil
		}
	}
	metadata, err := loadPoolBackupMetadataReceipt(s, receipt.OperationID)
	if err == nil {
		if metadata.Kind != "backup-repository-register" || metadata.Status != "failed" {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

func BackupCredentialReceipts(s *Store) ([]protocol.StorageCredentialReceipt, error) {
	if s == nil {
		return nil, errors.New("backup credential scope is invalid")
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	backupCredentialPreparationMu.Lock()
	defer backupCredentialPreparationMu.Unlock()
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(s.dir, clusterSecretDirectory, "backup")); os.IsNotExist(err) {
		return []protocol.StorageCredentialReceipt{}, nil
	} else if err != nil {
		return nil, err
	}
	dir, err := backupCredentialDir(s)
	if err != nil {
		return nil, err
	}
	key, err := clusterSecretKey(s)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.StorageCredentialReceipt, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readClusterSecret(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var stored storedBackupCredential
		if json.Unmarshal(b, &stored) != nil || !hmac.Equal([]byte(stored.Proof), []byte(backupCredentialProof(key, stored.Receipt, stored.Credentials))) {
			return nil, errors.New("backup credential receipt is corrupt")
		}
		r := stored.Receipt
		if r.PoolID != s.State.PoolID || r.HostID != s.State.HostID || !backupCredentialKind(r.Kind) ||
			!protocol.ValidSnapshotOperationID(r.Reference) || !protocol.ValidSnapshotOperationID(r.RequestID) ||
			!protocol.ValidSnapshotOperationID(r.RepositoryID) || !protocol.ValidSnapshotOperationID(r.OperationID) {
			return nil, errors.New("backup credential receipt scope is invalid")
		}
		unused, err := backupCredentialUnused(s, r, repos)
		if err != nil {
			return nil, err
		}
		status := "ready"
		if !r.RetiredAt.IsZero() {
			status = "retired"
		}
		out = append(out, protocol.StorageCredentialReceipt{Receipt: r.Reference, RequestID: r.RequestID,
			PoolID: r.PoolID, HostID: r.HostID, RepositoryID: r.RepositoryID,
			RegistrationRequestID: "backup-credential:" + r.Kind + ":" + r.OperationID,
			Status:                status, CreatedAt: r.CreatedAt,
			RetireEligible: status == "ready" && unused && backupCredentialPreparations[s.dir+"\x00"+r.Reference] == 0})
	}
	return out, nil
}

// RetireUnusedBackupCredential returns handled=false for an ordinary storage
// receipt. A retired backup receipt remains as the exact-replay tombstone.
func RetireUnusedBackupCredential(s *Store, reference, poolID, hostID string) (bool, error) {
	if s == nil || poolID != s.State.PoolID || hostID != s.State.HostID || !protocol.ValidSnapshotOperationID(reference) {
		return false, errors.New("backup credential retirement scope is invalid")
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	backupCredentialPreparationMu.Lock()
	defer backupCredentialPreparationMu.Unlock()
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	if _, err := os.Lstat(filepath.Join(s.dir, clusterSecretDirectory, "backup")); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	dir, err := backupCredentialDir(s)
	if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, err := readClusterSecret(path)
		if err != nil {
			return false, err
		}
		var stored storedBackupCredential
		if json.Unmarshal(b, &stored) != nil {
			continue // an unrelated corrupt receipt cannot block ordinary storage cleanup
		}
		r := stored.Receipt
		if r.Reference != reference {
			continue
		}
		if err := masterBackupPool(s, poolID); err != nil {
			return true, err
		}
		if err := requirePoolAuthorityWritable(s); err != nil {
			return true, err
		}
		key, err := clusterSecretKey(s)
		if err != nil {
			return true, err
		}
		if r.PoolID != poolID || r.HostID != hostID || !hmac.Equal([]byte(stored.Proof), []byte(backupCredentialProof(key, r, stored.Credentials))) {
			return true, errors.New("backup credential retirement scope is invalid")
		}
		if !r.RetiredAt.IsZero() {
			return true, nil
		}
		repos, err := loadPoolBackupRepositories(s)
		if err != nil {
			return true, err
		}
		unused, err := backupCredentialUnused(s, r, repos)
		if err != nil {
			return true, err
		}
		if !unused || backupCredentialPreparations[s.dir+"\x00"+reference] != 0 {
			return true, errors.New("backup credential is still referenced by repository registration")
		}
		if r.Kind == protocol.BackupRepoKindSMB3 {
			if err := vmhost.RetireBackupRepositorySMBCredential(s.dir, reference); err != nil {
				return true, err
			}
		}
		stored.Receipt.RetiredAt = time.Now().UTC()
		stored.Credentials = protocol.StorageCredentials{}
		stored.Proof = backupCredentialProof(key, stored.Receipt, stored.Credentials)
		retired, err := json.Marshal(stored)
		if err != nil {
			return true, err
		}
		if err := writePrivate(path, retired, 0600); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
}

// RetireBackupCredential clears the prior private material only after Core has
// durably selected and can resolve a replacement receipt for this repository.
func RetireBackupCredential(s *Store, oldRepo, newRepo protocol.BackupRepository) error {
	if s == nil || oldRepo.ID != newRepo.ID || oldRepo.PoolID != newRepo.PoolID || oldRepo.PoolID != s.State.PoolID ||
		!backupCredentialKind(oldRepo.Kind) || !backupCredentialKind(newRepo.Kind) ||
		!protocol.ValidSnapshotOperationID(oldRepo.CredentialRef) || !protocol.ValidSnapshotOperationID(newRepo.CredentialRef) {
		return errors.New("backup credential rotation scope is invalid")
	}
	if oldRepo.CredentialRef == newRepo.CredentialRef {
		return nil // exact registration retry
	}
	current, err := GetPoolBackupRepository(s, newRepo.ID)
	if err != nil || current.PoolID != newRepo.PoolID || current.Kind != newRepo.Kind || current.Generation != newRepo.Generation || current.CredentialRef != newRepo.CredentialRef {
		return errors.New("backup credential replacement is not durable")
	}
	if _, err := resolveBackupCredential(s, newRepo, ""); err != nil {
		return err
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	dir, err := backupCredentialDir(s)
	if err != nil {
		return err
	}
	key, err := clusterSecretKey(s)
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
		path := filepath.Join(dir, entry.Name())
		b, err := readClusterSecret(path)
		if err != nil {
			return err
		}
		var stored storedBackupCredential
		if json.Unmarshal(b, &stored) != nil {
			return errors.New("backup credential receipt is corrupt")
		}
		r := stored.Receipt
		if r.Reference != oldRepo.CredentialRef {
			continue
		}
		if r.PoolID != oldRepo.PoolID || r.HostID != s.State.HostID || r.RepositoryID != oldRepo.ID || r.Kind != oldRepo.Kind ||
			!hmac.Equal([]byte(stored.Proof), []byte(backupCredentialProof(key, r, stored.Credentials))) {
			return errors.New("backup credential retirement scope is invalid")
		}
		if !r.RetiredAt.IsZero() {
			return nil
		}
		stored.Receipt.RetiredAt = time.Now().UTC()
		stored.Credentials = protocol.StorageCredentials{}
		stored.Proof = backupCredentialProof(key, stored.Receipt, stored.Credentials)
		retired, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		return writePrivate(path, retired, 0600)
	}
	return nil // this Core never held the prior secret, or it was already removed
}
