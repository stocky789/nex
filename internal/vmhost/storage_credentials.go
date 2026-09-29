// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

const credentialDirectory = "storage-credentials"

// The receipt file holds metadata only; the secret lives solely in the sealed file.
type storedCredential = protocol.StorageCredentialReceipt

func credentialPath(stateDir, requestID string) string {
	h := sha256.Sum256([]byte(requestID))
	return filepath.Join(stateDir, credentialDirectory, hex.EncodeToString(h[:])+".json")
}

func credentialDir(stateDir string) (string, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return "", errors.New("credential state directory is invalid")
	}
	dir := filepath.Join(stateDir, credentialDirectory)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("credential directory must be private")
	}
	return dir, nil
}

func receiptID() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validCredentialHandoff(h protocol.StorageCredentialHandoff) error {
	if len(h.Offer.RequestID) == 0 || len(h.Offer.RequestID) > 128 || len(h.Offer.RegistrationRequestID) > 128 || len(h.Offer.PoolID) == 0 || len(h.Offer.PoolID) > 128 || len(h.Offer.HostID) == 0 || len(h.Offer.HostID) > 128 || len(h.Offer.RepositoryID) > 128 || (h.Offer.RepositoryID == "" && h.Offer.RegistrationRequestID == "") || len(h.Credentials.Username) > 4096 || len(h.Credentials.Secret) == 0 || len(h.Credentials.Secret) > 65536 || len(h.Credentials.Domain) > 253 || strings.Contains(h.Credentials.Username, "=") || strings.Contains(h.Credentials.Domain, "=") {
		return errors.New("invalid storage credential handoff")
	}
	for _, v := range []string{h.Offer.RequestID, h.Offer.RegistrationRequestID, h.Offer.PoolID, h.Offer.HostID, h.Offer.RepositoryID, h.Credentials.Username, h.Credentials.Secret, h.Credentials.Domain} {
		if strings.ContainsAny(v, "\x00\r\n") {
			return errors.New("invalid storage credential handoff")
		}
	}
	return nil
}

func smbAuthFileBody(username, secret, domain string) (string, error) {
	if strings.Contains(username, "=") || strings.Contains(domain, "=") {
		return "", errors.New("storage credential cannot be used by the native mount")
	}
	contents := "password=" + secret + "\n"
	if username != "" {
		contents = "username=" + username + "\n" + contents
	}
	if domain != "" {
		contents += "domain=" + domain + "\n"
	}
	return contents, nil
}

func parseSMBAuthFile(body string) (c protocol.StorageCredentials) {
	for _, line := range strings.Split(body, "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "username":
			c.Username = value
		case "password":
			c.Secret = value
		case "domain":
			c.Domain = value
		}
	}
	return c
}

// mountCredentialName binds a sealed credential to the mount units that load it.
const mountCredentialName = "nex-smb"

// sealMountCredential encrypts a mount.cifs credentials body with systemd-creds (TPM2 when
// the host has one, the host key otherwise). The plaintext only travels over stdin, and the
// mount unit decrypts the file itself with LoadCredentialEncrypted=, boot included.
func sealMountCredential(ctx context.Context, path, body string) error {
	return SealCredential(ctx, mountCredentialName, path, []byte(body))
}

// SealCredential writes data to path encrypted by systemd-creds under the credential name.
func SealCredential(ctx context.Context, name, path string, data []byte) error {
	tmp := path + ".new"
	defer os.Remove(tmp)
	if _, err := runSystemdCreds(ctx, data, "systemd-creds", "--no-ask-password", "encrypt", "--name="+name, "-", tmp); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		return err
	}
	f, err := os.Open(tmp)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func openMountCredential(ctx context.Context, path string) (string, error) {
	out, err := OpenCredential(ctx, mountCredentialName, path)
	return string(out), err
}

// OpenCredential decrypts a file written by SealCredential.
func OpenCredential(ctx context.Context, name, path string) ([]byte, error) {
	return runSystemdCreds(ctx, nil, "systemd-creds", "--no-ask-password", "decrypt", "--name="+name, path, "-")
}

// materializeMountCredential seals the credential once per receipt. The file is addressed by
// the receipt; an existing one must decrypt to the same credential, which is how a replayed
// handoff with a different secret is refused.
func materializeMountCredential(stateDir, receipt string, c protocol.StorageCredentials) error {
	if !storageID.MatchString(receipt) {
		return errors.New("storage credential cannot be used by the native mount")
	}
	root, err := credentialDir(stateDir)
	if err != nil {
		return err
	}
	body, err := smbAuthFileBody(c.Username, c.Secret, c.Domain)
	if err != nil {
		return err
	}
	path := filepath.Join(root, receipt)
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600 {
		// A file that decrypts to a different credential is a conflicting replay; anything
		// unreadable is damage, repaired below by sealing again.
		if sealed, err := openMountCredential(context.Background(), path); err == nil && parseSMBAuthFile(sealed).Secret != "" {
			if subtle.ConstantTimeCompare([]byte(sealed), []byte(body)) != 1 {
				return errors.New("storage credential replay conflicts with existing receipt")
			}
			return nil
		}
	}
	return sealMountCredential(context.Background(), path, body)
}

// SaveStorageCredential is the only path that persists an offered credential. The receipt is
// durable before it is returned, so retrying the same handoff does not make a second secret.
func SaveStorageCredential(stateDir string, h protocol.StorageCredentialHandoff) (protocol.StorageCredentialReceipt, error) {
	var empty protocol.StorageCredentialReceipt
	if err := validCredentialHandoff(h); err != nil {
		return empty, err
	}
	if _, err := credentialDir(stateDir); err != nil {
		return empty, err
	}
	path := credentialPath(stateDir, h.Offer.RequestID)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return empty, errors.New("credential receipt must be a private regular file")
		}
		b, err := readSmall(path, 1<<20)
		if err != nil {
			return empty, err
		}
		var old storedCredential
		if err = json.Unmarshal(b, &old); err != nil || old.RequestID != h.Offer.RequestID || old.PoolID != h.Offer.PoolID || old.HostID != h.Offer.HostID || old.RepositoryID != h.Offer.RepositoryID || old.RegistrationRequestID != h.Offer.RegistrationRequestID {
			return empty, errors.New("storage credential replay conflicts with existing receipt")
		}
		if old.Status != "ready" {
			return empty, errors.New("storage credential receipt was retired; use a new request ID")
		}
		if err = materializeMountCredential(stateDir, old.Receipt, h.Credentials); err != nil {
			return empty, err
		}
		return old, nil
	} else if !os.IsNotExist(err) {
		return empty, err
	}
	id, err := receiptID()
	if err != nil {
		return empty, err
	}
	receipt := protocol.StorageCredentialReceipt{Receipt: id, RequestID: h.Offer.RequestID, PoolID: h.Offer.PoolID, HostID: h.Offer.HostID, RepositoryID: h.Offer.RepositoryID, RegistrationRequestID: h.Offer.RegistrationRequestID, CreatedAt: time.Now().UTC(), Status: "ready"}
	b, err := json.Marshal(receipt)
	if err != nil {
		return empty, err
	}
	// The sealed secret lands first: a crash before the receipt leaves only an orphaned
	// ciphertext, never a receipt without its credential.
	if err = materializeMountCredential(stateDir, id, h.Credentials); err != nil {
		return empty, err
	}
	if err = atomicWrite(path, b); err != nil {
		return empty, err
	}
	return receipt, nil
}

// credentialReceiptValid authorizes a normal storage command without exposing its secret.
func credentialReceiptValid(stateDir, receipt, repositoryID, poolID, hostID string) error {
	if receipt == "" || repositoryID == "" || poolID == "" || hostID == "" {
		return errors.New("storage credential receipt is required")
	}
	dir, err := credentialDir(stateDir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := readSmall(filepath.Join(dir, entry.Name()), 1<<20)
		if err != nil {
			return err
		}
		var stored storedCredential
		if err = json.Unmarshal(b, &stored); err != nil {
			return err
		}
		if stored.Receipt == receipt && stored.RepositoryID == repositoryID && stored.PoolID == poolID && stored.HostID == hostID && stored.Status == "ready" {
			return nil
		}
	}
	return fmt.Errorf("storage credential receipt is unavailable")
}

// bindCredentialReceipt attaches a new-registration receipt to the ID Core just allocated.
// It is idempotent, but never allows one receipt to move between repositories.
func bindCredentialReceipt(stateDir, receipt, repositoryID, poolID, hostID, registrationRequestID string) error {
	if receipt == "" || repositoryID == "" || poolID == "" || hostID == "" || registrationRequestID == "" {
		return errors.New("storage credential receipt binding is invalid")
	}
	dir, err := credentialDir(stateDir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, err := readSmall(path, 1<<20)
		if err != nil {
			return err
		}
		var stored storedCredential
		if err = json.Unmarshal(b, &stored); err != nil {
			return err
		}
		if stored.Receipt != receipt {
			continue
		}
		if stored.PoolID != poolID || stored.HostID != hostID || stored.RegistrationRequestID != registrationRequestID || stored.Status != "ready" || (stored.RepositoryID != "" && stored.RepositoryID != repositoryID) {
			return errors.New("storage credential receipt cannot bind this repository")
		}
		if stored.RepositoryID == repositoryID {
			return nil
		}
		stored.RepositoryID = repositoryID
		updated, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		return atomicWrite(path, updated)
	}
	return errors.New("storage credential receipt is unavailable")
}

// StorageCredentialReceipts exposes only durable, opaque receipt metadata for adoption after a
// Controller replacement. Credential material and keyed replay proofs remain Core-local.
func StorageCredentialReceipts(stateDir string) ([]protocol.StorageCredentialReceipt, error) {
	dir, err := credentialDir(stateDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.StorageCredentialReceipt, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := readSmall(filepath.Join(dir, entry.Name()), 1<<20)
		if err != nil {
			return nil, err
		}
		var stored storedCredential
		if err = json.Unmarshal(b, &stored); err != nil || stored.Receipt == "" || stored.RequestID == "" || stored.PoolID == "" || stored.HostID == "" {
			return nil, errors.New("credential receipt is invalid")
		}
		out = append(out, stored)
	}
	return out, nil
}

// releaseStorageCredential deletes the Core-local secret for a repository that is being
// detached or whose credentials were replaced. Unlike storage-credential-retire, callers
// pass the storage row being released; attachment is not re-checked against the journal
// because that row still names the receipt until the detach or update is journaled.
func releaseStorageCredential(stateDir string, s protocol.StorageRegistration) error {
	if s.CredentialReceipt == "" {
		return nil
	}
	return retireStorageCredential(stateDir, s.CredentialReceipt, s.PoolID, s.HostID, false)
}

// retireStorageCredential removes an unreferenced Core-local secret. The managed storage
// projection supplies attached=true while a repository still names the receipt, so a browser
// request can never detach credentials from an active repository.
func retireStorageCredential(stateDir, receipt, poolID, hostID string, attached bool) error {
	if !storageID.MatchString(receipt) || poolID == "" || hostID == "" {
		return errors.New("storage credential receipt is invalid")
	}
	if attached {
		return errors.New("storage credential receipt is still attached to a repository")
	}
	dir, err := credentialDir(stateDir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, err := readSmall(path, 1<<20)
		if err != nil {
			return err
		}
		var stored storedCredential
		if err = json.Unmarshal(b, &stored); err != nil {
			return err
		}
		if stored.Receipt != receipt {
			continue
		}
		if stored.PoolID != poolID || stored.HostID != hostID {
			return errors.New("storage credential receipt does not belong to this pool and host")
		}
		if stored.Status == "ready" {
			stored.Status = "retired"
			retired, err := json.Marshal(stored)
			if err != nil {
				return err
			}
			if err = atomicWrite(path, retired); err != nil {
				return err
			}
		}
		found = true
		break
	}
	if !found {
		return errors.New("storage credential receipt is unavailable")
	}
	if err = os.Remove(filepath.Join(dir, receipt)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if found {
		if d, err := os.Open(dir); err == nil {
			err = d.Sync()
			d.Close()
			return err
		}
	}
	return nil
}
