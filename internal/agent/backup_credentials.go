// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
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
}

// sealedBackupCredential is the only form a receipt takes on disk. The scope fields stay
// readable so a former pool's receipt can be found, and they are bound to the ciphertext
// as authenticated data, so editing them or moving the file to another receipt fails to open.
type sealedBackupCredential struct {
	PoolID     string `json:"pool_id"`
	HostID     string `json:"host_id"`
	Reference  string `json:"reference"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

var errBackupCredentialUnencrypted = errors.New("backup credential receipt is not encrypted")

var errBackupCredentialKeyMissing = errors.New("backup credential key is missing")

// backupCredentialKeyName is the systemd credential name of the receipt key. The unit
// loads the sealed file with LoadCredentialEncrypted=, so a running Core finds the
// decrypted key in $CREDENTIALS_DIRECTORY.
const backupCredentialKeyName = "nex-backup-key"

// backupCredentialKey returns the AES key, sealed at rest by systemd-creds (host key or
// TPM2) in its own root-only directory apart from the receipts it protects. Order: the
// credential systemd handed the service; else decrypt the sealed file (a key created after
// the service started, so the credential was not there to load); else, only when writing a
// receipt, create and seal a new key. A key lost while receipts remain is reported, not
// replaced by one that would leave every receipt unreadable. The caller holds clusterSecretMu.
func backupCredentialKey(s *Store, create bool) ([]byte, error) {
	if s == nil || !filepath.IsAbs(s.dir) || filepath.Clean(s.dir) != s.dir {
		return nil, errors.New("invalid backup credential key directory")
	}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		if key, err := os.ReadFile(filepath.Join(dir, backupCredentialKeyName)); err == nil {
			if len(key) != 32 {
				return nil, errors.New("backup credential key is invalid")
			}
			return key, nil
		}
	}
	dir := filepath.Join(s.dir, "backup-credential-key")
	path := filepath.Join(dir, "key.cred")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("backup credential key must be private")
		}
		key, err := vmhost.OpenCredential(ctx, backupCredentialKeyName, path)
		if err != nil || len(key) != 32 {
			return nil, errors.New("backup credential key is invalid")
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if !create {
		return nil, errBackupCredentialKeyMissing
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("backup credential key directory must be private")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, vmhost.SealCredential(ctx, backupCredentialKeyName, path, key)
}

func backupCredentialAEAD(s *Store, create bool) (cipher.AEAD, error) {
	key, err := backupCredentialKey(s, create)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func backupCredentialAAD(poolID, hostID, reference string) []byte {
	return []byte(strings.Join([]string{"nex-backup-credential", poolID, hostID, reference}, "\x00"))
}

// sealBackupCredential encrypts a receipt with AES-256-GCM under the host's random key
// (backupCredentialKey). The caller holds clusterSecretMu.
func sealBackupCredential(s *Store, stored storedBackupCredential) ([]byte, error) {
	aead, err := backupCredentialAEAD(s, true)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	r := stored.Receipt
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return json.Marshal(sealedBackupCredential{PoolID: r.PoolID, HostID: r.HostID, Reference: r.Reference, Nonce: nonce,
		Ciphertext: aead.Seal(nil, nonce, plain, backupCredentialAAD(r.PoolID, r.HostID, r.Reference))})
}

// openBackupCredential refuses a plaintext, tampered, or foreign-key receipt. The caller
// holds clusterSecretMu.
func openBackupCredential(s *Store, raw []byte) (storedBackupCredential, error) {
	var stored storedBackupCredential
	var sealed sealedBackupCredential
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return stored, err
	}
	if len(sealed.Ciphertext) == 0 {
		return stored, errBackupCredentialUnencrypted
	}
	aead, err := backupCredentialAEAD(s, false)
	if err != nil {
		return stored, err
	}
	if len(sealed.Nonce) != aead.NonceSize() {
		return stored, errors.New("backup credential receipt is damaged")
	}
	plain, err := aead.Open(nil, sealed.Nonce, sealed.Ciphertext, backupCredentialAAD(sealed.PoolID, sealed.HostID, sealed.Reference))
	if err != nil {
		return stored, errors.New("backup credential receipt cannot be decrypted")
	}
	if json.Unmarshal(plain, &stored) != nil {
		return storedBackupCredential{}, errors.New("backup credential receipt is damaged")
	}
	r := stored.Receipt
	if r.PoolID != sealed.PoolID || r.HostID != sealed.HostID || r.Reference != sealed.Reference {
		return storedBackupCredential{}, errors.New("backup credential receipt is damaged")
	}
	return stored, nil
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
	if c.Username == "" || len(c.Username) > 4096 || len(c.Secret) > 65536 || len(c.SSHPrivateKey) > 65536 || !protocol.ValidSMBDomain(c.Domain) || c.SSHKnownHosts != "" ||
		strings.ContainsAny(c.Username+c.Secret+c.SSHPrivateKey, "\x00") || strings.ContainsAny(c.Username+c.Secret, "\r\n") {
		return backupCredentialReceipt{}, errors.New("invalid backup credential material")
	}
	if parts[1] == protocol.BackupRepoKindSMB3 && strings.Contains(c.Username, "=") || parts[1] != protocol.BackupRepoKindSMB3 && c.Domain != "" {
		return backupCredentialReceipt{}, errors.New("invalid backup credential username or domain")
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

type backupCredentialFile struct {
	path   string
	stored storedBackupCredential
}

var errBackupCredentialDamaged = errors.New("a backup credential receipt is unreadable, tampered with, or not encrypted; the others are still reported")

// readBackupCredentials lists every receipt in dir. The caller holds clusterSecretMu. A
// file that cannot be read or parsed is counted in damaged and skipped, so one bad file
// cannot disable every other credential. Every writer here holds clusterSecretMu, so a
// temp file seen now was left by a crashed write: remove it.
func readBackupCredentials(s *Store, dir string) (files []backupCredentialFile, damaged int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if strings.HasPrefix(entry.Name(), ".write-") {
			_ = os.Remove(path)
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := readClusterSecret(path)
		if err != nil {
			damaged++
			continue
		}
		stored, err := openBackupCredential(s, b)
		if errors.Is(err, errBackupCredentialKeyMissing) {
			return nil, 0, err
		}
		if err != nil {
			damaged++
			continue
		}
		files = append(files, backupCredentialFile{path, stored})
	}
	return files, damaged, nil
}

// eraseForeignBackupCredentials removes every receipt, retired or not, that belongs to a pool or host this Core no longer is. Call it after a pool
// change: a host that joined another pool or was ejected keeps no old pool's secret.
func eraseForeignBackupCredentials(s *Store) error {
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	if _, err := os.Lstat(filepath.Join(s.dir, clusterSecretDirectory, "backup")); os.IsNotExist(err) {
		return nil
	}
	dir, err := backupCredentialDir(s)
	if err != nil {
		return err
	}
	files, _, err := readBackupCredentials(s, dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		r := file.stored.Receipt
		if r.PoolID == s.State.PoolID && r.HostID == s.State.HostID {
			continue
		}
		if err := os.Remove(file.path); err != nil {
			return err
		}
	}
	return nil
}

// eraseForeignBackupRepositories unmounts and forgets every backup repository record of a
// pool this Core no longer belongs to. Call it with eraseForeignBackupCredentials.
func eraseForeignBackupRepositories(ctx context.Context, s *Store) error {
	poolBackupAuthorityMu.Lock()
	state, err := loadPoolBackupRepositories(s)
	poolBackupAuthorityMu.Unlock()
	if err != nil {
		return err
	}
	var foreign []string
	for id, repo := range state.Repositories {
		if repo.PoolID == s.State.PoolID {
			continue
		}
		// Unmounted outside the lock: a dead server can hold a detach until ctx ends.
		if err := detachBackupRepositoryNative(ctx, repo); err != nil {
			return err
		}
		foreign = append(foreign, id)
	}
	if len(foreign) == 0 {
		return nil
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	state, err = loadPoolBackupRepositories(s)
	if err != nil {
		return err
	}
	for _, id := range foreign {
		delete(state.Repositories, id)
	}
	return savePoolBackupRepositories(s, state)
}

func saveBackupCredential(s *Store, h protocol.StorageCredentialHandoff) (protocol.StorageCredentialReceipt, error) {
	var empty protocol.StorageCredentialReceipt
	receipt, err := backupCredentialScope(s, h)
	if err != nil {
		return empty, err
	}
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	path, err := backupCredentialPath(s, receipt.RequestID)
	if err != nil {
		return empty, err
	}
	if b, err := readClusterSecret(path); err == nil {
		old, err := openBackupCredential(s, b)
		if err != nil {
			return empty, err
		}
		if old.Receipt.Reference == "" || old.Receipt.RequestID != receipt.RequestID ||
			old.Receipt.OperationID != receipt.OperationID || old.Receipt.PoolID != receipt.PoolID || old.Receipt.HostID != receipt.HostID ||
			old.Receipt.RepositoryID != receipt.RepositoryID || old.Receipt.Kind != receipt.Kind {
			return empty, errors.New("backup credential replay conflicts with receipt")
		}
		// A retired tombstone holds no material, so check it before comparing credentials.
		if !old.Receipt.RetiredAt.IsZero() {
			return empty, errors.New("backup credential receipt was retired")
		}
		if old.Credentials != h.Credentials {
			return empty, errors.New("backup credential replay conflicts with receipt")
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
		b, err := sealBackupCredential(s, storedBackupCredential{Receipt: receipt, Credentials: h.Credentials})
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
// registration. A stale authority or superseded reference cannot reuse old material.
func ResolveBackupCredential(s *Store, repo protocol.BackupRepository) (protocol.StorageCredentials, error) {
	if s == nil {
		return protocol.StorageCredentials{}, errors.New("backup credential scope is invalid")
	}
	current, err := GetPoolBackupRepository(s, repo.ID)
	if err != nil || current.PoolID != repo.PoolID || current.Kind != repo.Kind || current.CredentialRef != repo.CredentialRef {
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
// host; the caller supplies the authoritative repository registration.
func ResolveBackupCredentialForAdoption(s *Store, repo protocol.BackupRepository) (protocol.StorageCredentials, error) {
	if s == nil || repo.Valid() != nil {
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
	files, _, err := readBackupCredentials(s, dir)
	if err != nil {
		return empty, err
	}
	for _, file := range files {
		stored := file.stored
		r := stored.Receipt
		if r.Reference != repo.CredentialRef {
			continue
		}
		if r.PoolID != repo.PoolID || r.HostID != s.State.HostID || r.RepositoryID != repo.ID || r.Kind != repo.Kind ||
			(operationID != "" && r.OperationID != operationID) ||
			!r.RetiredAt.IsZero() || !protocol.ValidSnapshotOperationID(r.RequestID) || !protocol.ValidSnapshotOperationID(r.OperationID) {
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

// BackupCredentialReceipts runs on every heartbeat, so it never waits on the pool backup
// authority: the repository file is replaced atomically, and RetireEligible is only a hint
// that RetireUnusedBackupCredential rechecks under that lock. A damaged receipt is skipped
// and reported as errBackupCredentialDamaged alongside the others.
func BackupCredentialReceipts(s *Store) ([]protocol.StorageCredentialReceipt, error) {
	if s == nil {
		return nil, errors.New("backup credential scope is invalid")
	}
	if _, err := os.Lstat(filepath.Join(s.dir, clusterSecretDirectory, "backup")); os.IsNotExist(err) {
		return []protocol.StorageCredentialReceipt{}, nil
	} else if err != nil {
		return nil, err
	}
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return nil, err
	}
	backupCredentialPreparationMu.Lock()
	defer backupCredentialPreparationMu.Unlock()
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	dir, err := backupCredentialDir(s)
	if err != nil {
		return nil, err
	}
	files, damaged, err := readBackupCredentials(s, dir)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.StorageCredentialReceipt, 0, len(files))
	for _, file := range files {
		stored := file.stored
		r := stored.Receipt
		if r.PoolID != s.State.PoolID || r.HostID != s.State.HostID {
			continue // a former pool's receipt; eraseForeignBackupCredentials removes it
		}
		if !backupCredentialKind(r.Kind) ||
			!protocol.ValidSnapshotOperationID(r.Reference) || !protocol.ValidSnapshotOperationID(r.RequestID) ||
			!protocol.ValidSnapshotOperationID(r.RepositoryID) || !protocol.ValidSnapshotOperationID(r.OperationID) {
			damaged++
			continue
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
	if damaged != 0 {
		return out, errBackupCredentialDamaged
	}
	return out, nil
}

// RetireUnusedBackupCredential returns handled=false for an ordinary storage
// receipt. A retired backup receipt remains as the exact-replay tombstone.
func RetireUnusedBackupCredential(s *Store, reference, poolID, hostID string) (bool, error) {
	if s == nil || poolID != s.State.PoolID || hostID != s.State.HostID || !protocol.ValidSnapshotOperationID(reference) {
		return false, errors.New("backup credential retirement scope is invalid")
	}
	// This runs on the Run loop: an ordinary storage receipt must not wait on the backup authority.
	clusterSecretMu.Lock()
	file, err := findBackupCredential(s, reference)
	clusterSecretMu.Unlock()
	if file == nil || err != nil {
		return false, err
	}
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	backupCredentialPreparationMu.Lock()
	defer backupCredentialPreparationMu.Unlock()
	clusterSecretMu.Lock()
	defer clusterSecretMu.Unlock()
	file, err = findBackupCredential(s, reference)
	if file == nil || err != nil {
		return false, err
	}
	path, stored := file.path, file.stored
	r := stored.Receipt
	if err := masterBackupPool(s, poolID); err != nil {
		return true, err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return true, err
	}
	if r.PoolID != poolID || r.HostID != hostID {
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
	return true, retireBackupCredentialFile(s, path, stored)
}

// retireBackupCredentialFile replaces a receipt with an encrypted tombstone that holds no
// material and still answers an exact replay. The caller holds clusterSecretMu.
func retireBackupCredentialFile(s *Store, path string, stored storedBackupCredential) error {
	stored.Receipt.RetiredAt = time.Now().UTC()
	stored.Credentials = protocol.StorageCredentials{}
	retired, err := sealBackupCredential(s, stored)
	if err != nil {
		return err
	}
	// The sealed mount credential holds the same secret. It goes first, so a failure
	// leaves the receipt live and the retirement is retried.
	if stored.Receipt.Kind == protocol.BackupRepoKindSMB3 {
		if err := removeBackupSMBCredentialNative(stored.Receipt.Reference); err != nil {
			return err
		}
	}
	return writePrivate(path, retired, 0600)
}

// findBackupCredential returns the backup receipt with reference, or nil when there is
// none. The caller holds clusterSecretMu.
func findBackupCredential(s *Store, reference string) (*backupCredentialFile, error) {
	if _, err := os.Lstat(filepath.Join(s.dir, clusterSecretDirectory, "backup")); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	dir, err := backupCredentialDir(s)
	if err != nil {
		return nil, err
	}
	files, _, err := readBackupCredentials(s, dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.stored.Receipt.Reference == reference {
			return &file, nil
		}
	}
	return nil, nil
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
	if err != nil || current.PoolID != newRepo.PoolID || current.Kind != newRepo.Kind || current.CredentialRef != newRepo.CredentialRef {
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
	files, _, err := readBackupCredentials(s, dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		path, stored := file.path, file.stored
		r := stored.Receipt
		if r.Reference != oldRepo.CredentialRef {
			continue
		}
		if r.PoolID != oldRepo.PoolID || r.HostID != s.State.HostID || r.RepositoryID != oldRepo.ID || r.Kind != oldRepo.Kind {
			return errors.New("backup credential retirement scope is invalid")
		}
		if !r.RetiredAt.IsZero() {
			return nil
		}
		return retireBackupCredentialFile(s, path, stored)
	}
	return nil // this Core never held the prior secret, or it was already removed
}
