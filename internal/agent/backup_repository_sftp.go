// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const sftpManifestName = "manifest.json"

var syncSFTPFile = func(file *sftp.File) error { return file.Sync() }
var syncSFTPDir = syncSFTPDirectory
var sftpSyncSupported = func(client *sftp.Client) bool {
	version, ok := client.HasExtension("fsync@openssh.com")
	return ok && version == "1"
}

func syncSFTPDirectory(client *sftp.Client, directory string) error {
	file, err := client.Open(directory)
	if err != nil {
		return err
	}
	syncErr := syncSFTPFile(file)
	closeErr := file.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

type sftpBackupStorage struct {
	s           *Store
	repo        protocol.BackupRepository
	credentials protocol.StorageCredentials
	stateDir    string
}

func newSFTPBackupStorage(s *Store, repo protocol.BackupRepository) (backupRepositoryStorage, error) {
	if repo.Kind != protocol.BackupRepoKindSFTP || repo.SFTPSource == nil || repo.Valid() != nil {
		return nil, ErrBackupInvalid
	}
	credentials, err := ResolveBackupCredential(s, repo)
	if err != nil {
		return nil, err
	}
	if credentials.Username == "" || credentials.SSHPrivateKey == "" || strings.ContainsAny(credentials.Username, "\x00\r\n") {
		return nil, errors.New("backup SFTP credentials are incomplete")
	}
	if repo.RemoteIdentity == nil || repo.RemoteIdentity.Fingerprint != sftpIdentity(repo.SFTPSource, credentials.Username) {
		return nil, errors.New("backup SFTP remote identity is not established")
	}
	return sftpBackupStorage{s: s, repo: repo, credentials: credentials, stateDir: s.dir}, nil
}

// prepareSFTPBackupRepository claims only the selected root and records its
// exact pinned endpoint/account identity after a fresh authenticated probe.
func prepareSFTPBackupRepository(ctx context.Context, repo protocol.BackupRepository, stateDir string, credentials protocol.StorageCredentials) (protocol.BackupRepository, error) {
	if repo.Kind != protocol.BackupRepoKindSFTP || repo.SFTPSource == nil || repo.Valid() != nil ||
		credentials.Username == "" || credentials.SSHPrivateKey == "" || strings.ContainsAny(credentials.Username, "\x00\r\n") {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	storage := sftpBackupStorage{repo: repo, credentials: credentials, stateDir: stateDir}
	client, closeAll, err := storage.connect(ctx)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	defer closeAll()
	root := storage.root()
	canonical, err := client.RealPath(root)
	if err != nil || canonical != root {
		return protocol.BackupRepository{}, errors.New("backup SFTP root identity changed")
	}
	info, err := client.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return protocol.BackupRepository{}, errors.New("backup SFTP root is unavailable")
	}
	if !sftpSyncSupported(client) {
		return protocol.BackupRepository{}, errors.New("backup SFTP server does not support durable file sync")
	}
	if err := storage.claimOwner(client); err != nil {
		return protocol.BackupRepository{}, err
	}
	observedIdentity := sftpIdentity(repo.SFTPSource, credentials.Username)
	if repo.RemoteIdentity != nil && repo.RemoteIdentity.Fingerprint != observedIdentity {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	repo.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: observedIdentity}
	storage.repo = repo
	if err := storage.verify(client); err != nil {
		return protocol.BackupRepository{}, err
	}
	if err := storage.Probe(ctx); err != nil {
		return protocol.BackupRepository{}, err
	}
	return repo, nil
}

func (b sftpBackupStorage) claimOwner(client *sftp.Client) error {
	root := b.root()
	ownerName := path.Join(root, "owner.json")
	owner, err := b.readFile(client, ownerName, 4096)
	if errors.Is(err, os.ErrNotExist) {
		if b.repo.RemoteIdentity != nil {
			return ErrBackupConflict
		}
		owner, _ = json.Marshal(struct {
			Repository string `json:"repository"`
			Pool       string `json:"pool"`
		}{b.repo.ID, b.repo.PoolID})
		if writeErr := b.writeExclusive(client, ownerName, bytes.NewReader(owner)); writeErr != nil {
			observed, readErr := b.readFile(client, ownerName, 4096)
			if errors.Is(readErr, os.ErrNotExist) {
				return writeErr
			}
			if readErr != nil {
				return readErr
			}
			if !bytes.Equal(observed, owner) {
				return ErrBackupConflict
			}
		}
	} else if err != nil {
		return err
	}
	var marker struct {
		Repository string `json:"repository"`
		Pool       string `json:"pool"`
	}
	if json.Unmarshal(owner, &marker) != nil || marker.Repository != b.repo.ID || marker.Pool != b.repo.PoolID {
		return ErrBackupConflict
	}
	if err := syncSFTPDir(client, root); err != nil {
		return err
	}
	return nil
}

func (b sftpBackupStorage) requireWritableAuthority() error {
	if b.s == nil {
		return ErrBackupUnavailable
	}
	state, err := loadPoolPeer(b.s)
	if err != nil {
		return err
	}
	return poolAuthorityWritable(state)
}

func (b sftpBackupStorage) connect(ctx context.Context) (*sftp.Client, func(), error) {
	key, err := ssh.ParsePrivateKey([]byte(b.credentials.SSHPrivateKey))
	if err != nil {
		return nil, nil, errors.New("backup SFTP private key is invalid")
	}
	port := b.repo.SFTPSource.Port
	if port == 0 {
		port = 22
	}
	address := net.JoinHostPort(b.repo.SFTPSource.Host, fmt.Sprint(port))
	config := &ssh.ClientConfig{
		User:            b.credentials.Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: sftpPinnedHostKey(b.repo.SFTPSource.HostKeySHA256),
		Timeout:         15 * time.Second,
	}
	connection, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			connection.Close()
		case <-done:
		}
	}()
	sshConnection, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		close(done)
		connection.Close()
		return nil, nil, err
	}
	sshClient := ssh.NewClient(sshConnection, channels, requests)
	client, err := sftp.NewClient(sshClient, sftp.UseConcurrentWrites(false))
	if err != nil {
		close(done)
		sshClient.Close()
		return nil, nil, err
	}
	closeAll := func() { close(done); client.Close(); sshClient.Close() }
	return client, closeAll, nil
}

func sftpPinnedHostKey(fingerprint string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != fingerprint {
			return errors.New("backup SFTP host key differs from pinned key")
		}
		return nil
	}
}

func (b sftpBackupStorage) root() string           { return b.repo.SFTPSource.Root }
func (b sftpBackupStorage) point(id string) string { return path.Join(b.root(), id) }

func (b sftpBackupStorage) verify(client *sftp.Client) error {
	root := b.root()
	canonical, err := client.RealPath(root)
	if err != nil || canonical != root {
		return errors.New("backup SFTP root identity changed")
	}
	info, err := client.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("backup SFTP root is unavailable")
	}
	data, err := b.readFile(client, path.Join(root, "owner.json"), 4096)
	if err != nil {
		return err
	}
	var owner struct {
		Repository string `json:"repository"`
		Pool       string `json:"pool"`
	}
	if json.Unmarshal(data, &owner) != nil || owner.Repository != b.repo.ID || owner.Pool != b.repo.PoolID {
		return errors.New("backup SFTP owner differs from selected repository")
	}
	fingerprint := sftpIdentity(b.repo.SFTPSource, b.credentials.Username)
	if b.repo.RemoteIdentity == nil || b.repo.RemoteIdentity.Fingerprint != fingerprint {
		return errors.New("backup SFTP remote identity changed")
	}
	if !sftpSyncSupported(client) {
		return errors.New("backup SFTP server does not support durable file sync")
	}
	return nil
}

func sftpIdentity(source *protocol.BackupSFTPSource, user string) string {
	port := source.Port
	if port == 0 {
		port = 22
	}
	sum := sha256.Sum256([]byte(net.JoinHostPort(source.Host, fmt.Sprint(port)) + "\x00" + source.HostKeySHA256 + "\x00" + source.Root + "\x00" + user))
	return hex.EncodeToString(sum[:])
}

func (b sftpBackupStorage) Verify(ctx context.Context) error {
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer closeAll()
	return b.verify(client)
}

func (b sftpBackupStorage) Probe(ctx context.Context) (probeErr error) {
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return err
	}
	name := path.Join(b.root(), ".nex-probe-"+fmt.Sprint(time.Now().UnixNano()))
	renamed := name + "-renamed"
	collision := name + "-collision"
	if err := b.writeExclusive(client, name, strings.NewReader("probe")); err != nil {
		return err
	}
	defer func() {
		for _, candidate := range []string{name, renamed, collision} {
			if removeErr := client.Remove(candidate); removeErr == nil {
				if syncErr := syncSFTPDir(client, b.root()); probeErr == nil {
					probeErr = syncErr
				}
			} else if !errors.Is(removeErr, os.ErrNotExist) && probeErr == nil {
				probeErr = removeErr
			}
		}
	}()
	if err := b.writeExclusive(client, name, strings.NewReader("collision")); err == nil {
		return errors.New("backup SFTP server ignored exclusive create")
	}
	data, err := b.readFile(client, name, 16)
	if err != nil || string(data) != "probe" {
		return errors.New("backup SFTP exclusive create changed existing data")
	}
	if err := client.Rename(name, renamed); err != nil {
		return err
	}
	if err := syncSFTPDir(client, b.root()); err != nil {
		return err
	}
	data, err = b.readFile(client, renamed, 16)
	if err != nil || string(data) != "probe" {
		return errors.New("backup SFTP rename did not preserve data")
	}
	entries, err := client.ReadDir(b.root())
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if entry.Name() == path.Base(renamed) {
			found = true
		}
	}
	if !found {
		return errors.New("backup SFTP probe listing omitted renamed file")
	}
	if err := b.writeExclusive(client, collision, strings.NewReader("occupied")); err != nil {
		return err
	}
	if err := client.Rename(renamed, collision); err == nil {
		return errors.New("backup SFTP server overwrote an existing rename target")
	}
	data, err = b.readFile(client, collision, 16)
	if err != nil || string(data) != "occupied" {
		return errors.New("backup SFTP rename changed existing target")
	}
	data, err = b.readFile(client, renamed, 16)
	if err != nil || string(data) != "probe" {
		return errors.New("backup SFTP rename lost source after collision")
	}
	if err := client.Remove(collision); err != nil {
		return err
	}
	if err := syncSFTPDir(client, b.root()); err != nil {
		return err
	}
	if err := client.Remove(renamed); err != nil {
		return err
	}
	if err := syncSFTPDir(client, b.root()); err != nil {
		return err
	}
	return ctx.Err()
}

func (b sftpBackupStorage) CaptureWorkDir(operationID string, online bool) string {
	if online {
		return vmhost.OnlineBackupWorkDir(operationID)
	}
	return filepath.Join(b.stateDir, "backup-staging", operationID)
}

func (b sftpBackupStorage) readFile(client *sftp.Client, name string, limit int64) ([]byte, error) {
	info, err := client.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("backup SFTP file is not regular or exceeds limit")
	}
	file, err := client.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("backup SFTP file read failed")
	}
	return data, nil
}

func (b sftpBackupStorage) writeExclusive(client *sftp.Client, name string, source io.Reader) error {
	file, err := client.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	_, writeErr := io.Copy(file, source)
	if writeErr == nil {
		writeErr = syncSFTPFile(file)
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return syncSFTPDir(client, path.Dir(name))
}

func (b sftpBackupStorage) readPoint(client *sftp.Client, id string) (protocol.BackupManifest, string, error) {
	if !protocol.ValidSnapshotOperationID(id) {
		return protocol.BackupManifest{}, "", ErrBackupInvalid
	}
	if _, err := client.Lstat(path.Join(b.root(), ".nex-abort-"+id)); err == nil {
		if _, finalErr := client.Lstat(path.Join(b.point(id), sftpManifestName)); finalErr == nil {
			return protocol.BackupManifest{}, "", ErrBackupConflict
		} else if !errors.Is(finalErr, os.ErrNotExist) {
			return protocol.BackupManifest{}, "", finalErr
		}
		return protocol.BackupManifest{}, "", os.ErrNotExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return protocol.BackupManifest{}, "", err
	}
	if _, deleting, err := b.readDeleteRecord(client, id); err != nil {
		return protocol.BackupManifest{}, "", err
	} else if deleting {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	}
	info, err := client.Lstat(b.point(id))
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return protocol.BackupManifest{}, "", errors.New("backup SFTP point is unavailable")
	}
	raw, err := b.readFile(client, path.Join(b.point(id), sftpManifestName), 1<<20)
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	var manifest protocol.BackupManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Valid() != nil || manifest.RecoveryPointID != id || manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return protocol.BackupManifest{}, "", errors.New("backup SFTP manifest is invalid")
	}
	for _, disk := range manifest.Disks {
		if err := b.checkArtifact(client, path.Join(b.point(id), disk.Filename), disk.Size, disk.SHA256); err != nil {
			return protocol.BackupManifest{}, "", err
		}
	}
	for _, firmware := range manifest.Firmware {
		if err := b.checkArtifact(client, path.Join(b.point(id), firmware.Filename), firmware.Size, firmware.SHA256); err != nil {
			return protocol.BackupManifest{}, "", err
		}
	}
	sum := sha256.Sum256(raw)
	return manifest, hex.EncodeToString(sum[:]), nil
}

func (b sftpBackupStorage) checkArtifact(client *sftp.Client, name string, size int64, digest string) error {
	info, err := client.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("backup SFTP artifact is missing or changed")
	}
	file, err := client.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	actual, err := io.Copy(hash, file)
	if err != nil || actual != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.New("backup SFTP artifact digest differs")
	}
	return nil
}

func (b sftpBackupStorage) ReadPoint(ctx context.Context, id string) (protocol.BackupManifest, string, error) {
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	return b.readPoint(client, id)
}

func sameSFTPManifest(a, b protocol.BackupManifest) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

func (b sftpBackupStorage) Publish(ctx context.Context, manifest protocol.BackupManifest, files map[string]string) (string, error) {
	if err := manifest.Valid(); err != nil {
		return "", err
	}
	if manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return "", ErrBackupConflict
	}
	required := make(map[string]struct{}, len(manifest.Disks)+len(manifest.Firmware))
	for _, disk := range manifest.Disks {
		required[disk.Filename] = struct{}{}
	}
	for _, fw := range manifest.Firmware {
		required[fw.Filename] = struct{}{}
	}
	if len(files) != len(required) {
		return "", ErrBackupInvalid
	}
	for name := range files {
		if _, ok := required[name]; !ok {
			return "", ErrBackupInvalid
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil || len(raw) > 1<<20 {
		return "", ErrBackupInvalid
	}
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return "", err
	}
	if err := b.verify(client); err != nil {
		closeAll()
		return "", err
	}
	digest, err := b.publishOnClient(ctx, client, manifest, files, raw)
	closeAll()
	if err != nil {
		return "", err
	}
	// A lost write reply is settled only after a new authenticated connection reads all bytes.
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	if err := b.requireWritableAuthority(); err != nil {
		return "", err
	}
	existing, observed, err := b.ReadPoint(ctx, manifest.RecoveryPointID)
	if err != nil || !sameSFTPManifest(existing, manifest) || observed != digest {
		return "", errors.New("backup SFTP publication could not be verified after reconnect")
	}
	return digest, nil
}

// publishOnClient binds an incomplete directory to one exact manifest before
// creating it. A retry can replace only incomplete regular artifacts for that
// intent; the final manifest appears only through an exclusive atomic rename.
func (b sftpBackupStorage) publishOnClient(ctx context.Context, client *sftp.Client, manifest protocol.BackupManifest, files map[string]string, raw []byte) (string, error) {
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	id := manifest.RecoveryPointID
	if _, err := client.Lstat(path.Join(b.root(), ".nex-abort-"+id)); err == nil {
		return "", ErrBackupConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, deleted, err := b.readDeleteRecord(client, id); err != nil {
		return "", err
	} else if deleted {
		return "", ErrBackupConflict
	}
	intentName := path.Join(b.root(), ".nex-intent-"+id)
	intent, err := b.readFile(client, intentName, 128)
	if errors.Is(err, os.ErrNotExist) {
		if _, pointErr := client.Lstat(b.point(id)); pointErr == nil {
			return "", ErrBackupConflict
		} else if !errors.Is(pointErr, os.ErrNotExist) {
			return "", pointErr
		}
		if writeErr := b.writeExclusive(client, intentName, strings.NewReader(digest)); writeErr != nil {
			intent, err = b.readFile(client, intentName, 128)
			if errors.Is(err, os.ErrNotExist) {
				return "", writeErr
			}
			if err != nil {
				return "", err
			}
			if string(intent) != digest {
				return "", ErrBackupConflict
			}
		}
	} else if err != nil {
		return "", err
	} else if string(intent) != digest {
		return "", ErrBackupConflict
	}
	if err := syncSFTPDir(client, b.root()); err != nil {
		return "", err
	}
	point := b.point(id)
	if mkdirErr := client.Mkdir(point); mkdirErr != nil {
		info, statErr := client.Lstat(point)
		if errors.Is(statErr, os.ErrNotExist) {
			return "", mkdirErr
		}
		if statErr != nil {
			return "", statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrBackupConflict
		}
	}
	if err := syncSFTPDir(client, b.root()); err != nil {
		return "", err
	}
	finalName := path.Join(point, sftpManifestName)
	tempName := path.Join(point, ".nex-manifest")
	if _, err := client.Lstat(finalName); err == nil {
		poolPeerMu.Lock()
		defer poolPeerMu.Unlock()
		if err := b.requireWritableAuthority(); err != nil {
			return "", err
		}
		if _, deleted, err := b.readDeleteRecord(client, id); err != nil {
			return "", err
		} else if deleted {
			return "", ErrBackupConflict
		}
		if err := syncSFTPDir(client, point); err != nil {
			return "", err
		}
		existing, observed, readErr := b.readPoint(client, id)
		if readErr != nil {
			return "", readErr
		}
		if observed != digest || !sameSFTPManifest(existing, manifest) {
			return "", ErrBackupConflict
		}
		return digest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	allowed := map[string]bool{path.Base(tempName): true}
	for name := range files {
		allowed[name] = true
	}
	entries, err := client.ReadDir(point)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || !entry.Mode().IsRegular() {
			return "", ErrBackupConflict
		}
	}
	// The staged manifest binds every incomplete artifact to this intent, even
	// when the upload fails before the final manifest can be published.
	if err := b.stageManifest(client, tempName, point, raw); err != nil {
		return "", err
	}
	for name, source := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		remoteName := path.Join(point, name)
		local, err := os.Open(source)
		if err != nil {
			return "", err
		}
		info, err := local.Stat()
		if err != nil || !info.Mode().IsRegular() {
			local.Close()
			return "", ErrBackupInvalid
		}
		local.Close()
		var wantSize int64
		var wantSHA string
		for _, disk := range manifest.Disks {
			if disk.Filename == name {
				wantSize, wantSHA = disk.Size, disk.SHA256
			}
		}
		for _, fw := range manifest.Firmware {
			if fw.Filename == name {
				wantSize, wantSHA = fw.Size, fw.SHA256
			}
		}
		if _, err := client.Lstat(remoteName); err == nil {
			if b.checkArtifact(client, remoteName, wantSize, wantSHA) == nil {
				continue
			}
			if err := client.Remove(remoteName); err != nil {
				return "", err
			}
			if err := syncSFTPDir(client, point); err != nil {
				return "", err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		local, err = os.Open(source)
		if err != nil {
			return "", err
		}
		err = b.writeExclusive(client, remoteName, local)
		local.Close()
		if err != nil {
			return "", err
		}
		if err := b.checkArtifact(client, remoteName, wantSize, wantSHA); err != nil {
			return "", err
		}
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	if err := b.requireWritableAuthority(); err != nil {
		return "", err
	}
	if _, err := client.Lstat(path.Join(b.root(), ".nex-abort-"+id)); err == nil {
		return "", ErrBackupConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, deleted, err := b.readDeleteRecord(client, id); err != nil {
		return "", err
	} else if deleted {
		return "", ErrBackupConflict
	}
	if err := client.Rename(tempName, finalName); err != nil {
		if _, observed, readErr := b.readPoint(client, id); readErr != nil || observed != digest {
			return "", err
		}
	}
	if err := syncSFTPDir(client, point); err != nil {
		return "", err
	}
	existing, observed, err := b.readPoint(client, id)
	if err != nil || observed != digest || !sameSFTPManifest(existing, manifest) {
		return "", errors.New("backup SFTP final manifest verification failed")
	}
	return digest, nil
}

func (b sftpBackupStorage) stageManifest(client *sftp.Client, tempName, point string, raw []byte) error {
	tempRaw, err := b.readFile(client, tempName, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		if err := b.writeExclusive(client, tempName, bytes.NewReader(raw)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !bytes.Equal(tempRaw, raw) {
		if len(tempRaw) >= len(raw) || !bytes.Equal(tempRaw, raw[:len(tempRaw)]) {
			return ErrBackupConflict
		}
		if err := client.Remove(tempName); err != nil {
			return err
		}
		if err := syncSFTPDir(client, point); err != nil {
			return err
		}
		if err := b.writeExclusive(client, tempName, bytes.NewReader(raw)); err != nil {
			return err
		}
	}
	if observed, err := b.readFile(client, tempName, 1<<20); err != nil {
		return err
	} else if !bytes.Equal(observed, raw) {
		return errors.New("backup SFTP staged manifest is incomplete")
	}
	return syncSFTPDir(client, point)
}

// AbortIncomplete removes only an unpublished upload whose staged manifest is
// bound to its intent. The abort record makes interrupted removal resumable.
func (b sftpBackupStorage) AbortIncomplete(ctx context.Context, id string) error {
	if !protocol.ValidSnapshotOperationID(id) {
		return ErrBackupInvalid
	}
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return err
	}
	return b.abortIncompleteOnClient(ctx, client, id)
}

func (b sftpBackupStorage) abortIncompleteOnClient(ctx context.Context, client *sftp.Client, id string) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	if err := b.requireWritableAuthority(); err != nil {
		return err
	}
	point := b.point(id)
	if info, err := client.Lstat(point); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrBackupConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	final := path.Join(point, sftpManifestName)
	if _, err := client.Lstat(final); err == nil {
		return ErrBackupConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, deleting, err := b.readDeleteRecord(client, id); err != nil {
		return err
	} else if deleting {
		return ErrBackupConflict
	}
	if _, err := getCatalogPoint(b.s, b.repo.ID, id); err == nil {
		return ErrBackupConflict
	} else if !errors.Is(err, ErrBackupNotFound) {
		return err
	}
	intentName := path.Join(b.root(), ".nex-intent-"+id)
	abortName := path.Join(b.root(), ".nex-abort-"+id)
	intent, intentErr := b.readFile(client, intentName, 128)
	if intentErr != nil && !errors.Is(intentErr, os.ErrNotExist) {
		return intentErr
	}
	abort, abortErr := b.readFile(client, abortName, 1<<20)
	if abortErr != nil && !errors.Is(abortErr, os.ErrNotExist) {
		return abortErr
	}
	if errors.Is(intentErr, os.ErrNotExist) && errors.Is(abortErr, os.ErrNotExist) {
		if _, err := client.Lstat(point); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return ErrBackupConflict
	}
	stagedName := path.Join(point, ".nex-manifest")
	claimedName := path.Join(point, ".nex-abort-manifest")
	staged, stagedErr := b.readFile(client, stagedName, 1<<20)
	if stagedErr != nil && !errors.Is(stagedErr, os.ErrNotExist) {
		return stagedErr
	}
	claimed, claimedErr := b.readFile(client, claimedName, 1<<20)
	if claimedErr != nil && !errors.Is(claimedErr, os.ErrNotExist) {
		return claimedErr
	}
	if stagedErr == nil && claimedErr == nil || abortErr != nil && claimedErr == nil {
		return ErrBackupConflict
	}
	if stagedErr != nil && claimedErr == nil {
		staged, stagedErr = claimed, nil
	}
	var stagedManifest protocol.BackupManifest
	stagedValid := stagedErr == nil && json.Unmarshal(staged, &stagedManifest) == nil && stagedManifest.Valid() == nil
	emptyAbort := abortErr == nil && (len(abort) == 0 && !stagedValid || len(abort) > 0 && bytes.HasPrefix([]byte("empty:"+string(intent)), abort))
	if emptyAbort || abortErr != nil && !stagedValid {
		return b.abortEmptyUpload(ctx, client, point, intentName, abortName, stagedName, claimedName, staged, intent, intentErr, abort, abortErr)
	}
	if abortErr == nil && stagedErr == nil && !bytes.Equal(abort, staged) {
		if len(abort) >= len(staged) || !bytes.Equal(abort, staged[:len(abort)]) {
			return ErrBackupConflict
		}
		if err := client.Remove(abortName); err != nil {
			return err
		}
		if err := syncSFTPDir(client, b.root()); err != nil {
			return err
		}
		if err := b.writeExclusive(client, abortName, bytes.NewReader(staged)); err != nil {
			return err
		}
		abort = staged
	}
	if abortErr != nil {
		if stagedErr != nil { // Legacy hash-only partials need inspected cleanup.
			return ErrBackupConflict
		}
		abort = staged
	}
	var manifest protocol.BackupManifest
	if json.Unmarshal(abort, &manifest) != nil || manifest.Valid() != nil ||
		manifest.RecoveryPointID != id || manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return ErrBackupConflict
	}
	sum := sha256.Sum256(abort)
	digest := hex.EncodeToString(sum[:])
	if intentErr == nil && string(intent) != digest {
		return ErrBackupConflict
	}
	if abortErr != nil && intentErr != nil {
		return ErrBackupConflict
	}
	allowed := map[string]bool{".nex-manifest": true, ".nex-abort-manifest": true}
	for _, disk := range manifest.Disks {
		allowed[disk.Filename] = true
	}
	for _, fw := range manifest.Firmware {
		allowed[fw.Filename] = true
	}
	entries, err := client.ReadDir(point)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		for _, entry := range entries {
			if !allowed[entry.Name()] || !entry.Mode().IsRegular() {
				return ErrBackupConflict
			}
		}
	}
	if abortErr != nil {
		if err := b.writeExclusive(client, abortName, bytes.NewReader(abort)); err != nil {
			return err
		}
	}
	if err == nil {
		if stagedErr == nil {
			if err := b.claimAbortManifest(client, stagedName, claimedName, final, staged); err != nil {
				return err
			}
		} else if len(entries) > 0 {
			return ErrBackupConflict
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Name() == ".nex-manifest" || entry.Name() == ".nex-abort-manifest" {
				continue
			}
			if err := client.Remove(path.Join(point, entry.Name())); err != nil {
				return err
			}
			if err := syncSFTPDir(client, point); err != nil {
				return err
			}
		}
		if stagedErr == nil {
			if err := client.Remove(claimedName); err != nil {
				return err
			}
			if err := syncSFTPDir(client, point); err != nil {
				return err
			}
		}
		if err := client.RemoveDirectory(point); err != nil {
			return err
		}
		if err := syncSFTPDir(client, b.root()); err != nil {
			return err
		}
	}
	if intentErr == nil {
		if err := client.Remove(intentName); err != nil {
			return err
		}
		if err := syncSFTPDir(client, b.root()); err != nil {
			return err
		}
	}
	if err := client.Remove(abortName); err != nil {
		return err
	}
	return syncSFTPDir(client, b.root())
}

func (b sftpBackupStorage) claimAbortManifest(client *sftp.Client, stagedName, claimedName, finalName string, expected []byte) error {
	if err := client.Rename(stagedName, claimedName); err != nil {
		if _, finalErr := client.Lstat(finalName); finalErr == nil {
			return ErrBackupConflict
		} else if !errors.Is(finalErr, os.ErrNotExist) {
			return finalErr
		}
		claimed, readErr := b.readFile(client, claimedName, 1<<20)
		if readErr != nil || !bytes.Equal(claimed, expected) {
			return err
		}
	}
	if err := syncSFTPDir(client, path.Dir(claimedName)); err != nil {
		return err
	}
	if _, err := client.Lstat(finalName); err == nil {
		return ErrBackupConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	claimed, err := b.readFile(client, claimedName, 1<<20)
	if err != nil || !bytes.Equal(claimed, expected) {
		return ErrBackupConflict
	}
	return nil
}

func (b sftpBackupStorage) abortEmptyUpload(ctx context.Context, client *sftp.Client, point, intentName, abortName, stagedName, claimedName string, staged, intent []byte, intentErr error, abort []byte, abortErr error) error {
	if intentErr != nil || len(intent) != 64 {
		return ErrBackupConflict
	}
	if decoded, err := hex.DecodeString(string(intent)); err != nil || len(decoded) != sha256.Size {
		return ErrBackupConflict
	}
	marker := []byte("empty:" + string(intent))
	if abortErr == nil && !bytes.Equal(abort, marker) {
		if len(abort) >= len(marker) || !bytes.Equal(abort, marker[:len(abort)]) {
			return ErrBackupConflict
		}
		if err := client.Remove(abortName); err != nil {
			return err
		}
		if err := syncSFTPDir(client, b.root()); err != nil {
			return err
		}
		abortErr = os.ErrNotExist
	}
	entries, err := client.ReadDir(point)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(entries) > 1 || len(entries) == 1 &&
		(entries[0].Name() != ".nex-manifest" && entries[0].Name() != ".nex-abort-manifest" || !entries[0].Mode().IsRegular()) {
		return ErrBackupConflict // Hash-only partials with artifacts need inspected cleanup.
	}
	if abortErr != nil {
		if err := b.writeExclusive(client, abortName, bytes.NewReader(marker)); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(entries) == 1 {
		if err := b.claimAbortManifest(client, stagedName, claimedName, path.Join(point, sftpManifestName), staged); err != nil {
			return err
		}
		if err := client.Remove(claimedName); err != nil {
			return err
		}
		if err := syncSFTPDir(client, point); err != nil {
			return err
		}
	}
	if err == nil {
		if err := client.RemoveDirectory(point); err != nil {
			return err
		}
		if err := syncSFTPDir(client, b.root()); err != nil {
			return err
		}
	}
	if err := client.Remove(abortName); err != nil {
		return err
	}
	if err := syncSFTPDir(client, b.root()); err != nil {
		return err
	}
	if err := client.Remove(intentName); err != nil {
		return err
	}
	return syncSFTPDir(client, b.root())
}

func (b sftpBackupStorage) CopyArtifacts(ctx context.Context, id, localDir string, manifest protocol.BackupManifest) error {
	stored, _, err := b.ReadPoint(ctx, id)
	if err != nil || !sameSFTPManifest(stored, manifest) {
		return ErrBackupConflict
	}
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return err
	}
	if err := os.MkdirAll(localDir, 0700); err != nil {
		return err
	}
	copyOne := func(name, digest string, size int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		localPath := filepath.Join(localDir, name)
		if existing, err := os.Open(localPath); err == nil {
			info, statErr := existing.Stat()
			hash := sha256.New()
			_, readErr := io.Copy(hash, existing)
			existing.Close()
			if statErr == nil && info.Mode().IsRegular() && readErr == nil && info.Size() == size && hex.EncodeToString(hash.Sum(nil)) == digest {
				return nil
			}
			if err := os.Remove(localPath); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		remote, err := client.Open(path.Join(b.point(id), name))
		if err != nil {
			return err
		}
		defer remote.Close()
		local, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(local, io.TeeReader(remote, hash))
		if copyErr == nil {
			copyErr = local.Sync()
		}
		closeErr := local.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != size || hex.EncodeToString(hash.Sum(nil)) != digest {
			return errors.New("backup SFTP restore copy digest differs")
		}
		return nil
	}
	for _, disk := range manifest.Disks {
		if err := copyOne(disk.Filename, disk.SHA256, disk.Size); err != nil {
			return err
		}
	}
	for _, fw := range manifest.Firmware {
		if err := copyOne(fw.Filename, fw.SHA256, fw.Size); err != nil {
			return err
		}
	}
	return nil
}

func (b sftpBackupStorage) Entries(ctx context.Context) ([]string, error) {
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return nil, err
	}
	entries, err := client.ReadDir(b.root())
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if id, ok := strings.CutPrefix(entry.Name(), ".nex-abort-"); ok && protocol.ValidSnapshotOperationID(id) {
			return nil, ErrBackupConflict
		}
		if id, ok := strings.CutPrefix(entry.Name(), ".nex-delete-"); ok && protocol.ValidSnapshotOperationID(id) {
			settled, err := b.deleteSettled(client, id)
			if err != nil {
				return nil, err
			}
			if !settled {
				return nil, ErrBackupConflict
			}
		}
		if protocol.ValidSnapshotOperationID(entry.Name()) {
			if _, deleted, err := b.readDeleteRecord(client, entry.Name()); err != nil {
				return nil, err
			} else if deleted {
				return nil, ErrBackupConflict // An interrupted prune must settle before rebuild.
			}
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func (b sftpBackupStorage) Scan(ctx context.Context) ([]protocol.BackupRecoveryPoint, error) {
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return nil, err
	}
	return b.scanOnClient(ctx, client)
}

func (b sftpBackupStorage) scanOnClient(ctx context.Context, client *sftp.Client) ([]protocol.BackupRecoveryPoint, error) {
	entries, err := client.ReadDir(b.root())
	if err != nil {
		return nil, err
	}
	points := []protocol.BackupRecoveryPoint{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := entry.Name()
		if pointID, ok := strings.CutPrefix(id, ".nex-abort-"); ok && protocol.ValidSnapshotOperationID(pointID) {
			return nil, ErrBackupConflict
		}
		if pointID, ok := strings.CutPrefix(id, ".nex-delete-"); ok && protocol.ValidSnapshotOperationID(pointID) {
			settled, err := b.deleteSettled(client, pointID)
			if err != nil {
				return nil, err
			}
			if !settled {
				return nil, ErrBackupConflict
			}
		}
		if !protocol.ValidSnapshotOperationID(id) {
			continue
		}
		if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			return nil, ErrBackupConflict
		}
		if _, deleted, err := b.readDeleteRecord(client, id); err != nil {
			return nil, err
		} else if deleted {
			return nil, ErrBackupConflict // Do not discard the catalog while deletion is incomplete.
		}
		_, err := client.Lstat(path.Join(b.point(id), sftpManifestName))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} // No final manifest: upload is incomplete.
		if err != nil {
			return nil, err
		}
		manifest, digest, err := b.readPoint(client, id)
		if err != nil {
			return nil, err
		} // A published point with corrupt/missing data blocks rebuild.
		protected, err := b.protectedOnClient(client, id)
		if err != nil {
			return nil, err
		}
		points = append(points, protocol.BackupRecoveryPoint{ID: id, RepositoryID: b.repo.ID, PoolID: b.repo.PoolID, SourceVMID: manifest.SourceVMID, SourceVMName: manifest.SourceVMName, CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency, ManifestSHA256: digest, Protected: protected})
	}
	return points, nil
}

func (b sftpBackupStorage) protectedOnClient(client *sftp.Client, id string) (bool, error) {
	data, err := b.readFile(client, path.Join(b.point(id), ".nex-protected"), 64)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if string(data) != "protected\n" {
		return false, ErrBackupConflict
	}
	return true, nil
}

func (b sftpBackupStorage) Protected(ctx context.Context, id string) (bool, error) {
	if !protocol.ValidSnapshotOperationID(id) {
		return false, ErrBackupInvalid
	}
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return false, err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return false, err
	}
	if _, deleted, err := b.readDeleteRecord(client, id); err != nil {
		return false, err
	} else if deleted {
		return false, nil
	}
	if _, _, err := b.readPoint(client, id); err != nil {
		return false, err
	}
	return b.protectedOnClient(client, id)
}

func (b sftpBackupStorage) SetProtected(ctx context.Context, id string, protected bool) error {
	if _, _, err := b.ReadPoint(ctx, id); err != nil {
		return err
	}
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return err
	}
	return b.setProtectedOnClient(client, id, protected)
}

func (b sftpBackupStorage) setProtectedOnClient(client *sftp.Client, id string, protected bool) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	if err := b.requireWritableAuthority(); err != nil {
		return err
	}
	if _, deleted, err := b.readDeleteRecord(client, id); err != nil {
		return err
	} else if deleted {
		return ErrBackupConflict
	}
	name := path.Join(b.point(id), ".nex-protected")
	if protected {
		if err := b.writeExclusive(client, name, strings.NewReader("protected\n")); err != nil {
			if data, readErr := b.readFile(client, name, 64); readErr == nil && string(data) == "protected\n" {
				return syncSFTPDir(client, b.point(id))
			}
			return err
		}
		return nil
	}
	marked, err := b.protectedOnClient(client, id)
	if err != nil {
		return err
	}
	if !marked {
		return nil
	}
	if err := client.Remove(name); err != nil {
		return err
	}
	return syncSFTPDir(client, b.point(id))
}

type sftpDeleteRecord struct {
	Manifest protocol.BackupManifest `json:"manifest"`
	Digest   string                  `json:"digest"`
}

func (b sftpBackupStorage) deleteRecordName(id string) string {
	return path.Join(b.root(), ".nex-delete-"+id)
}

func (b sftpBackupStorage) deleteSettled(client *sftp.Client, id string) (bool, error) {
	record, exists, err := b.readDeleteRecord(client, id)
	if err != nil || !exists {
		return false, err
	}
	doneName := path.Join(b.root(), ".nex-deleted-"+id)
	done, err := b.readFile(client, doneName, 64)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if string(done) != record.Digest {
		if len(done) < len(record.Digest) && strings.HasPrefix(record.Digest, string(done)) {
			return false, nil
		}
		return false, ErrBackupConflict
	}
	if _, err := client.Lstat(b.point(id)); err == nil {
		return false, ErrBackupConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	file, err := client.Open(doneName)
	if err != nil {
		return false, err
	}
	syncErr := syncSFTPFile(file)
	closeErr := file.Close()
	if syncErr != nil {
		return false, syncErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := syncSFTPDir(client, b.root()); err != nil {
		return false, err
	}
	return true, nil
}

func (b sftpBackupStorage) readDeleteRecord(client *sftp.Client, id string) (sftpDeleteRecord, bool, error) {
	var record sftpDeleteRecord
	raw, err := b.readFile(client, b.deleteRecordName(id), 2<<20)
	if errors.Is(err, os.ErrNotExist) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	if json.Unmarshal(raw, &record) != nil || record.Manifest.Valid() != nil ||
		record.Manifest.RecoveryPointID != id || record.Manifest.RepositoryID != b.repo.ID || record.Manifest.PoolID != b.repo.PoolID {
		return record, false, ErrBackupConflict
	}
	canonical, err := json.Marshal(record.Manifest)
	if err != nil {
		return record, false, err
	}
	sum := sha256.Sum256(canonical)
	if record.Digest != hex.EncodeToString(sum[:]) {
		return record, false, ErrBackupConflict
	}
	return record, true, nil
}

func (b sftpBackupStorage) publishDeleteRecord(client *sftp.Client, id string, raw []byte) error {
	stage := path.Join(b.root(), ".nex-delete-stage-"+id)
	final := b.deleteRecordName(id)
	present, err := b.readFile(client, stage, 2<<20)
	if errors.Is(err, os.ErrNotExist) {
		if err := b.writeExclusive(client, stage, bytes.NewReader(raw)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !bytes.Equal(present, raw) {
		if len(present) >= len(raw) || !bytes.Equal(present, raw[:len(present)]) {
			return ErrBackupConflict
		}
		if err := client.Remove(stage); err != nil {
			return err
		}
		if err := syncSFTPDir(client, b.root()); err != nil {
			return err
		}
		if err := b.writeExclusive(client, stage, bytes.NewReader(raw)); err != nil {
			return err
		}
	}
	if observed, err := b.readFile(client, stage, 2<<20); err != nil || !bytes.Equal(observed, raw) {
		return ErrBackupConflict
	}
	if err := client.Rename(stage, final); err != nil {
		if observed, readErr := b.readFile(client, final, 2<<20); readErr != nil || !bytes.Equal(observed, raw) {
			return err
		}
	}
	return syncSFTPDir(client, b.root())
}

// Delete removes only the files named by one validated catalog manifest. The
// durable record survives interrupted removal so an exact retry can finish it.
func (b sftpBackupStorage) Delete(ctx context.Context, id string) error {
	if !protocol.ValidSnapshotOperationID(id) {
		return ErrBackupInvalid
	}
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return err
	}
	return b.deleteOnClient(ctx, client, id)
}

func (b sftpBackupStorage) deleteOnClient(ctx context.Context, client *sftp.Client, id string) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	if err := b.requireWritableAuthority(); err != nil {
		return err
	}
	pins, err := ActiveBackupRestorePins(b.s)
	if err != nil {
		return err
	}
	if pins[id] {
		return ErrBackupConflict
	}
	catalog, err := getCatalogPoint(b.s, b.repo.ID, id)
	if err != nil || catalog.PoolID != b.repo.PoolID || catalog.Protected {
		return ErrBackupConflict
	}
	record, exists, err := b.readDeleteRecord(client, id)
	if err != nil {
		return err
	}
	if exists && record.Digest != catalog.ManifestSHA256 {
		return ErrBackupConflict
	}
	if exists {
		settled, err := b.deleteSettled(client, id)
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
	}
	point := b.point(id)
	if !exists {
		manifest, digest, err := b.readPoint(client, id)
		if err != nil || digest != catalog.ManifestSHA256 {
			return ErrBackupConflict
		}
		protected, err := b.protectedOnClient(client, id)
		if err != nil || protected {
			return ErrBackupConflict
		}
		record = sftpDeleteRecord{Manifest: manifest, Digest: digest}
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := b.publishDeleteRecord(client, id, raw); err != nil {
			return err
		}
	}
	if err := syncSFTPDir(client, b.root()); err != nil {
		return err
	}
	allowed := map[string]bool{sftpManifestName: true}
	for _, disk := range record.Manifest.Disks {
		allowed[disk.Filename] = true
	}
	for _, fw := range record.Manifest.Firmware {
		allowed[fw.Filename] = true
	}
	entries, err := client.ReadDir(point)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		for _, entry := range entries {
			if !allowed[entry.Name()] || !entry.Mode().IsRegular() {
				return ErrBackupConflict
			}
		}
		for name := range allowed {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := client.Remove(path.Join(point, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := syncSFTPDir(client, point); err != nil {
				return err
			}
		}
		if err := client.RemoveDirectory(point); err != nil {
			return err
		}
		if err := syncSFTPDir(client, b.root()); err != nil {
			return err
		}
	}
	intent := path.Join(b.root(), ".nex-intent-"+id)
	intentRaw, err := b.readFile(client, intent, 128)
	if err == nil {
		if string(intentRaw) != record.Digest {
			return ErrBackupConflict
		}
		if err := client.Remove(intent); err != nil {
			return err
		}
		if err := syncSFTPDir(client, b.root()); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	done := path.Join(b.root(), ".nex-deleted-"+id)
	if err := b.writeExclusive(client, done, strings.NewReader(record.Digest)); err != nil {
		stored, readErr := b.readFile(client, done, 64)
		if readErr != nil {
			return err
		}
		if string(stored) == record.Digest {
			settled, err := b.deleteSettled(client, id)
			if err != nil {
				return err
			}
			if !settled {
				return ErrBackupConflict
			}
			return nil
		}
		if len(stored) >= len(record.Digest) || !strings.HasPrefix(record.Digest, string(stored)) {
			return ErrBackupConflict
		}
		if err := client.Remove(done); err != nil {
			return err
		}
		if err := syncSFTPDir(client, b.root()); err != nil {
			return err
		}
		if err := b.writeExclusive(client, done, strings.NewReader(record.Digest)); err != nil {
			return err
		}
	}
	return syncSFTPDir(client, b.root())
}
