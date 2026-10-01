// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/rand"
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
var syncSFTPDir = syncSFTPPath
var sftpSyncSupported = func(client *sftp.Client) bool {
	version, ok := client.HasExtension("fsync@openssh.com")
	return ok && version == "1"
}

// sftpIOTimeout is the keepalive interval. A connection that receives nothing
// for three intervals fails, so a stalled server cannot hold an operation
// forever while a slow but progressing transfer survives missed keepalives.
var sftpIOTimeout = 30 * time.Second

func syncSFTPPath(client *sftp.Client, name string) error {
	file, err := client.Open(name)
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
	if !validSFTPCredentials(credentials) {
		return nil, errors.New("backup SFTP credentials are incomplete")
	}
	if repo.RemoteIdentity == nil || repo.RemoteIdentity.Fingerprint != sftpIdentity(repo.SFTPSource, credentials.Username) {
		return nil, errors.New("backup SFTP remote identity is not established")
	}
	return sftpBackupStorage{s: s, repo: repo, credentials: credentials, stateDir: s.dir}, nil
}

func validSFTPCredentials(c protocol.StorageCredentials) bool {
	return c.Username != "" && c.SSHPrivateKey != "" && !strings.ContainsAny(c.Username, "\x00\r\n")
}

// prepareSFTPBackupRepository claims only the selected root and records its
// exact pinned endpoint/account identity after a fresh authenticated probe.
func prepareSFTPBackupRepository(ctx context.Context, repo protocol.BackupRepository, stateDir string, credentials protocol.StorageCredentials) (protocol.BackupRepository, error) {
	if repo.Kind != protocol.BackupRepoKindSFTP || repo.SFTPSource == nil || repo.Valid() != nil || !validSFTPCredentials(credentials) {
		return protocol.BackupRepository{}, ErrBackupInvalid
	}
	storage := sftpBackupStorage{repo: repo, credentials: credentials, stateDir: stateDir}
	client, closeAll, err := storage.connect(ctx)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	defer closeAll()
	if err := storage.checkRoot(client); err != nil {
		return protocol.BackupRepository{}, err
	}
	claimed, err := storage.claimOwner(client)
	if err != nil {
		if claimed {
			err = errors.Join(err, storage.releaseOwner(client))
		}
		return protocol.BackupRepository{}, err
	}
	releaseClaim := func(cause error) error {
		if !claimed {
			return cause
		}
		// The retry carries a new repository ID, so a kept claim would refuse it.
		return errors.Join(cause, storage.releaseOwner(client))
	}
	observedIdentity := sftpIdentity(repo.SFTPSource, credentials.Username)
	if repo.RemoteIdentity != nil && repo.RemoteIdentity.Fingerprint != observedIdentity {
		return protocol.BackupRepository{}, releaseClaim(ErrBackupConflict)
	}
	repo.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: observedIdentity}
	storage.repo = repo
	if err := storage.verify(client); err != nil {
		return protocol.BackupRepository{}, releaseClaim(err)
	}
	if err := storage.probeOnClient(ctx, client); err != nil {
		return protocol.BackupRepository{}, releaseClaim(err)
	}
	return repo, nil
}

// sftpOwner is the owner.json content binding a root to one repository.
type sftpOwner struct {
	Repository string `json:"repository"`
	Pool       string `json:"pool"`
}

// claimOwner binds the root to this repository. claimed reports that this call
// wrote owner.json, so a later registration failure can release it.
func (b sftpBackupStorage) claimOwner(client *sftp.Client) (claimed bool, err error) {
	root := b.root()
	ownerName := path.Join(root, "owner.json")
	want, _ := json.Marshal(sftpOwner{b.repo.ID, b.repo.PoolID})
	owner, err := b.readFile(client, ownerName, 4096)
	// Only a new registration claims the root, finishing an interrupted claim.
	if b.repo.RemoteIdentity == nil && (errors.Is(err, os.ErrNotExist) || err == nil && len(owner) < len(want) && bytes.HasPrefix(want, owner)) {
		if err := b.writeMarker(client, ownerName, want); err != nil {
			return false, err
		}
		owner, err, claimed = want, nil, true
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, ErrBackupConflict
	} else if err != nil {
		return false, err
	}
	var marker sftpOwner
	if json.Unmarshal(owner, &marker) != nil || marker.Repository != b.repo.ID || marker.Pool != b.repo.PoolID {
		return false, ErrBackupConflict
	}
	return claimed, syncSFTPDir(client, root)
}

// releaseOwner removes this repository's owner.json so a corrected registration
// under a new ID is not blocked by a claim left from a refused attempt.
func (b sftpBackupStorage) releaseOwner(client *sftp.Client) error {
	ownerName := path.Join(b.root(), "owner.json")
	want, _ := json.Marshal(sftpOwner{b.repo.ID, b.repo.PoolID})
	owner, err := b.readFile(client, ownerName, 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(owner, want) {
		return ErrBackupConflict
	}
	if err := client.Remove(ownerName); err != nil {
		return err
	}
	return syncSFTPDir(client, b.root())
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
	var connection net.Conn
	var sshConnection ssh.Conn
	var channels <-chan ssh.NewChannel
	var requests <-chan *ssh.Request
	var stopCancel func() bool
	for _, algorithms := range sftpHostKeyAlgorithms {
		pinned, offered := false, false
		config := &ssh.ClientConfig{
			User:              b.credentials.Username,
			Auth:              []ssh.AuthMethod{ssh.PublicKeys(key)},
			HostKeyAlgorithms: algorithms,
			HostKeyCallback: func(host string, remote net.Addr, key ssh.PublicKey) error {
				offered = true
				err := sftpPinnedHostKey(b.repo.SFTPSource.HostKeySHA256)(host, remote, key)
				pinned = err == nil
				return err
			},
		}
		conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, nil, err
		}
		stopCancel = context.AfterFunc(ctx, func() { conn.Close() })
		// NewClientConn ignores ClientConfig.Timeout; the read deadline bounds the handshake too.
		conn = progressConn{conn, 3 * sftpIOTimeout}
		sshConnection, channels, requests, err = ssh.NewClientConn(conn, address, config)
		if err == nil {
			connection = conn
			break
		}
		stopCancel()
		conn.Close()
		var negotiation *ssh.AlgorithmNegotiationError
		if pinned || ctx.Err() != nil || !offered && !errors.As(err, &negotiation) {
			return nil, nil, err
		}
	}
	if connection == nil {
		return nil, nil, errors.New("backup SFTP host key differs from pinned key")
	}
	done := make(chan struct{})
	sshClient := ssh.NewClient(sshConnection, channels, requests)
	go func() {
		ticker := time.NewTicker(sftpIOTimeout)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			// Replies keep an idle connection reading. A reply queued behind a
			// busy link may be late; only silence fails the connection.
			if _, _, err := sshClient.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				return
			}
		}
	}()
	client, err := sftp.NewClient(sshClient, sftp.UseConcurrentWrites(false))
	if err != nil {
		close(done)
		stopCancel()
		sshClient.Close()
		return nil, nil, err
	}
	closeAll := func() { close(done); stopCancel(); client.Close(); sshClient.Close() }
	return client, closeAll, nil
}

// sftpHostKeyAlgorithms offers one plain host-key type per handshake. Only one
// key is pinned, and Go would otherwise negotiate a certificate or the server's
// ECDSA or RSA key ahead of the Ed25519 key an operator usually pins.
var sftpHostKeyAlgorithms = [][]string{
	{ssh.KeyAlgoED25519},
	{ssh.KeyAlgoECDSA256},
	{ssh.KeyAlgoECDSA384},
	{ssh.KeyAlgoECDSA521},
	{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256},
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

// checkRoot confirms the root is the configured real directory on a server
// with durable file sync.
func (b sftpBackupStorage) checkRoot(client *sftp.Client) error {
	root := b.root()
	canonical, err := client.RealPath(root)
	if err != nil || canonical != root {
		return errors.New("backup SFTP root identity changed")
	}
	info, err := client.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("backup SFTP root is unavailable")
	}
	if !sftpSyncSupported(client) {
		return errors.New("backup SFTP server does not support durable file sync")
	}
	return nil
}

func (b sftpBackupStorage) verify(client *sftp.Client) error {
	if err := b.checkRoot(client); err != nil {
		return err
	}
	data, err := b.readFile(client, path.Join(b.root(), "owner.json"), 4096)
	if err != nil {
		return err
	}
	var owner sftpOwner
	if json.Unmarshal(data, &owner) != nil || owner.Repository != b.repo.ID || owner.Pool != b.repo.PoolID {
		return errors.New("backup SFTP owner differs from selected repository")
	}
	fingerprint := sftpIdentity(b.repo.SFTPSource, b.credentials.Username)
	if b.repo.RemoteIdentity == nil || b.repo.RemoteIdentity.Fingerprint != fingerprint {
		return errors.New("backup SFTP remote identity changed")
	}
	return nil
}

func sftpIdentity(source *protocol.BackupSFTPSource, user string) string {
	port := source.Port
	if port == 0 {
		port = 22
	}
	return sha256Hex([]byte(net.JoinHostPort(source.Host, fmt.Sprint(port)) + "\x00" + source.HostKeySHA256 + "\x00" + source.Root + "\x00" + user))
}

func (b sftpBackupStorage) Verify(ctx context.Context) error {
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer closeAll()
	return b.verify(client)
}

func (b sftpBackupStorage) Probe(ctx context.Context) error {
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return err
	}
	defer closeAll()
	if err := b.verify(client); err != nil {
		return err
	}
	// Preparation has no store; its caller checks authority under the lock.
	if b.s != nil {
		if err := requirePoolAuthorityWritable(b.s); err != nil {
			return err
		}
	}
	return b.probeOnClient(ctx, client)
}

// probeOnClient proves the server honors exclusive create and refuses to
// overwrite a rename target, both durably synced.
func (b sftpBackupStorage) probeOnClient(ctx context.Context, client *sftp.Client) (probeErr error) {
	name := path.Join(b.root(), ".nex-probe-"+fmt.Sprint(time.Now().UnixNano()))
	renamed := name + "-renamed"
	collision := name + "-collision"
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
	if err := b.writeExclusive(client, name, strings.NewReader("probe")); err != nil {
		return err
	}
	if err := b.writeExclusive(client, name, strings.NewReader("collision")); err == nil {
		return errors.New("backup SFTP server ignored exclusive create")
	}
	if err := client.Rename(name, renamed); err != nil {
		return err
	}
	if err := syncSFTPDir(client, b.root()); err != nil {
		return err
	}
	if err := b.writeExclusive(client, collision, strings.NewReader("occupied")); err != nil {
		return err
	}
	if err := client.Rename(renamed, collision); err == nil {
		return errors.New("backup SFTP server overwrote an existing rename target")
	}
	for file, want := range map[string]string{collision: "occupied", renamed: "probe"} {
		if data, err := b.readFile(client, file, 16); err != nil || string(data) != want {
			return errors.New("backup SFTP rename changed existing data")
		}
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

// writeMarker makes name hold exactly want. A strict prefix, such as the empty
// file a failed write leaves on a full server, is replaced; other content is a
// conflict, and a failed write reports its own error rather than a conflict.
func (b sftpBackupStorage) writeMarker(client *sftp.Client, name string, want []byte) error {
	present, err := b.readFile(client, name, 2<<20)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	case bytes.Equal(present, want):
		// An earlier attempt may have failed before syncing the file.
		if err := syncSFTPPath(client, name); err != nil {
			return err
		}
		return syncSFTPDir(client, path.Dir(name))
	case len(present) < len(want) && bytes.HasPrefix(want, present):
		if err := client.Remove(name); err != nil {
			return err
		}
		if err := syncSFTPDir(client, path.Dir(name)); err != nil {
			return err
		}
	default:
		return ErrBackupConflict
	}
	return b.writeExclusive(client, name, bytes.NewReader(want))
}

// parseManifest decodes raw and requires it to be a valid manifest of point id
// in this repository.
func (b sftpBackupStorage) parseManifest(raw []byte, id string) (protocol.BackupManifest, bool) {
	var manifest protocol.BackupManifest
	ok := json.Unmarshal(raw, &manifest) == nil && manifest.Valid() == nil &&
		manifest.RecoveryPointID == id && manifest.RepositoryID == b.repo.ID && manifest.PoolID == b.repo.PoolID
	return manifest, ok
}

// readPoint checks the published manifest and artifact sizes without reading
// artifact content.
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
	manifest, ok := b.parseManifest(raw, id)
	if !ok {
		return protocol.BackupManifest{}, "", errors.New("backup SFTP manifest is invalid")
	}
	for _, artifact := range manifestArtifacts(manifest) {
		if err := b.statArtifact(client, path.Join(b.point(id), artifact.Filename), artifact.Size); err != nil {
			return protocol.BackupManifest{}, "", err
		}
	}
	return manifest, sha256Hex(raw), nil
}

func (b sftpBackupStorage) statArtifact(client *sftp.Client, name string, size int64) error {
	info, err := client.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("backup SFTP artifact is missing or changed")
	}
	return nil
}

func (b sftpBackupStorage) checkArtifact(client *sftp.Client, name string, size int64, digest string) error {
	if err := b.statArtifact(client, name, size); err != nil {
		return err
	}
	file, err := client.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	actual, sum, err := hashLimited(file, size)
	if err != nil || actual != size || sum != digest {
		return errors.New("backup SFTP artifact digest differs")
	}
	return nil
}

func (b sftpBackupStorage) StatPoint(ctx context.Context, id string) (protocol.BackupManifest, string, error) {
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

func (b sftpBackupStorage) Publish(ctx context.Context, manifest protocol.BackupManifest, files map[string]string) (string, error) {
	if err := manifest.Valid(); err != nil {
		return "", err
	}
	if manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return "", ErrBackupConflict
	}
	artifacts := manifestArtifacts(manifest)
	if len(files) != len(artifacts) {
		return "", ErrBackupInvalid
	}
	for _, artifact := range artifacts {
		if _, ok := files[artifact.Filename]; !ok {
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
	// A lost write reply is settled only after a new authenticated connection
	// observes the committed manifest; its artifacts were hashed before commit.
	if _, observed, err := b.StatPoint(ctx, manifest.RecoveryPointID); err != nil || observed != digest {
		return "", errors.New("backup SFTP publication could not be verified after reconnect")
	}
	return digest, nil
}

// publishOnClient binds an incomplete directory to one exact manifest before
// creating it. A retry can replace only incomplete regular artifacts for that
// intent; the final manifest appears only through an exclusive atomic rename.
func (b sftpBackupStorage) publishOnClient(ctx context.Context, client *sftp.Client, manifest protocol.BackupManifest, files map[string]string, raw []byte) (string, error) {
	digest := sha256Hex(raw)
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
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// The point directory is created only after its intent is complete.
	if string(intent) != digest {
		if _, pointErr := client.Lstat(b.point(id)); pointErr == nil {
			return "", ErrBackupConflict
		} else if !errors.Is(pointErr, os.ErrNotExist) {
			return "", pointErr
		}
		if err := b.writeMarker(client, intentName, []byte(digest)); err != nil {
			return "", err
		}
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
		_, observed, readErr := b.readPoint(client, id)
		if readErr != nil {
			return "", readErr
		}
		if observed != digest {
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
	for _, artifact := range manifestArtifacts(manifest) {
		name, source := artifact.Filename, files[artifact.Filename]
		wantSize, wantSHA := artifact.Size, artifact.SHA256
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
	if _, observed, err := b.readPoint(client, id); err != nil || observed != digest {
		return "", errors.New("backup SFTP final manifest verification failed")
	}
	return digest, nil
}

func (b sftpBackupStorage) stageManifest(client *sftp.Client, tempName, point string, raw []byte) error {
	if err := b.writeMarker(client, tempName, raw); err != nil {
		return err
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
	info, pointErr := client.Lstat(point)
	if pointErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrBackupConflict
		}
	} else if !errors.Is(pointErr, os.ErrNotExist) {
		return pointErr
	}
	intentName := path.Join(b.root(), ".nex-intent-"+id)
	abortName := path.Join(b.root(), ".nex-abort-"+id)
	final := path.Join(point, sftpManifestName)
	if _, err := client.Lstat(final); err == nil {
		// A delayed final rename beat this abort's manifest claim. Nothing is
		// removed before that claim, so drop the abort record: the point is whole.
		if _, err := client.Lstat(path.Join(point, ".nex-abort-manifest")); !errors.Is(err, os.ErrNotExist) {
			return ErrBackupConflict
		}
		if err := client.Remove(abortName); err == nil {
			if err := syncSFTPDir(client, b.root()); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
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
	intent, intentErr := b.readFile(client, intentName, 128)
	if intentErr != nil && !errors.Is(intentErr, os.ErrNotExist) {
		return intentErr
	}
	abort, abortErr := b.readFile(client, abortName, 1<<20)
	if abortErr != nil && !errors.Is(abortErr, os.ErrNotExist) {
		return abortErr
	}
	if intentErr == nil && len(intent) < 64 && abortErr != nil && pointErr != nil {
		// An interrupted intent write stops before the point directory exists.
		if err := client.Remove(intentName); err != nil {
			return err
		}
		return syncSFTPDir(client, b.root())
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
		if stagedErr != nil {
			return ErrBackupConflict
		}
		abort = staged
	}
	manifest, ok := b.parseManifest(abort, id)
	if !ok {
		return ErrBackupConflict
	}
	if intentErr == nil && string(intent) != sha256Hex(abort) {
		return ErrBackupConflict
	}
	if abortErr != nil && intentErr != nil {
		return ErrBackupConflict
	}
	allowed := map[string]bool{".nex-manifest": true, ".nex-abort-manifest": true}
	for _, artifact := range manifestArtifacts(manifest) {
		allowed[artifact.Filename] = true
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
	// copyOne verifies each digest while copying.
	stored, _, err := b.StatPoint(ctx, id)
	if err != nil {
		return err
	}
	// The stored manifest went through JSON, so compare encodings, not values.
	left, _ := json.Marshal(stored)
	right, _ := json.Marshal(manifest)
	if !bytes.Equal(left, right) {
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
	for _, artifact := range manifestArtifacts(manifest) {
		if err := ctx.Err(); err != nil {
			return err
		}
		open := func() (io.ReadCloser, error) { return client.Open(path.Join(b.point(id), artifact.Filename)) }
		if err := copyVerifiedToLocal(open, filepath.Join(localDir, artifact.Filename), artifact.Size, artifact.SHA256); err != nil {
			return err
		}
	}
	return syncBackupDirectory(localDir)
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
		if err := b.checkRootEntry(client, entry.Name()); err != nil {
			return nil, err
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

// checkRootEntry refuses a rebuild while an abort or prune is unsettled.
func (b sftpBackupStorage) checkRootEntry(client *sftp.Client, name string) error {
	if id, ok := strings.CutPrefix(name, ".nex-abort-"); ok && protocol.ValidSnapshotOperationID(id) {
		return ErrBackupConflict
	}
	if id, ok := strings.CutPrefix(name, ".nex-delete-"); ok && protocol.ValidSnapshotOperationID(id) {
		settled, err := b.deleteSettled(client, id)
		if err != nil {
			return err
		}
		if !settled {
			return ErrBackupConflict
		}
	}
	if protocol.ValidSnapshotOperationID(name) {
		if _, deleted, err := b.readDeleteRecord(client, name); err != nil {
			return err
		} else if deleted {
			return ErrBackupConflict
		}
	}
	return nil
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
		if err := b.checkRootEntry(client, id); err != nil {
			return nil, err
		}
		if !protocol.ValidSnapshotOperationID(id) {
			continue
		}
		if !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			return nil, ErrBackupConflict
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
		} // A published point with missing or resized data blocks rebuild.
		_ = b.sweepMarkers(client, id) // Best effort: unprotect reports a failure.
		protected, err := b.protectedOnClient(client, id)
		if err != nil {
			return nil, err
		}
		points = append(points, protocol.BackupRecoveryPoint{ID: id, RepositoryID: b.repo.ID, PoolID: b.repo.PoolID, SourceVMID: manifest.SourceVMID, SourceVMName: manifest.SourceVMName, PolicyID: manifest.PolicyID, CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency, ManifestSHA256: digest, Protected: protected})
	}
	return points, nil
}

// readProtectMarker returns the marker's bytes, or nil when the point is unprotected.
func (b sftpBackupStorage) readProtectMarker(client *sftp.Client, id string) ([]byte, error) {
	data, err := b.readFile(client, path.Join(b.point(id), ".nex-protected"), maxBackupProtectMarker)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// A marker cut short by a failed write still keeps the point from pruning.
	if !strings.HasPrefix("protected\n", string(data)) && !strings.HasPrefix(string(data), "protected\n") {
		return nil, ErrBackupConflict
	}
	return data, nil
}

func (b sftpBackupStorage) protectedOnClient(client *sftp.Client, id string) (bool, error) {
	data, err := b.readProtectMarker(client, id)
	return data != nil, err
}

// removeMarkerIfUnchanged removes name only while it still holds want. The
// rename is atomic, so a marker written after want was read is put back.
func (b sftpBackupStorage) removeMarkerIfUnchanged(client *sftp.Client, name string, want []byte) error {
	claimed := name + ".rm-" + rand.Text()
	if err := client.Rename(name, claimed); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	got, readErr := b.readFile(client, claimed, maxBackupProtectMarker)
	if readErr == nil && bytes.Equal(got, want) {
		if err := client.Remove(claimed); err != nil {
			return err
		}
		return syncSFTPDir(client, path.Dir(name))
	}
	// A newer marker is put back and never deleted; if that fails it stays as .rm-*.
	if _, err := client.Lstat(name); err == nil && readErr == nil {
		if err := client.Remove(claimed); err != nil {
			return err
		}
	} else if err := client.Rename(claimed, name); err != nil {
		return errors.Join(readErr, err)
	}
	if readErr != nil {
		return readErr
	}
	return ErrBackupConflict
}

// sweepMarkers settles .rm-* files left by a crash between the rename-aside and the restore
// in removeMarkerIfUnchanged: put back when the point has no marker, removed only when
// byte-identical to the current marker, otherwise left for an operator.
func (b sftpBackupStorage) sweepMarkers(client *sftp.Client, id string) error {
	dir := b.point(id)
	entries, err := client.ReadDir(dir)
	if err != nil {
		return err
	}
	name := path.Join(dir, ".nex-protected")
	current, err := b.readProtectMarker(client, id)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".nex-protected.rm-") {
			continue
		}
		leftover := path.Join(dir, entry.Name())
		if current == nil {
			return client.Rename(leftover, name)
		}
		if got, err := b.readFile(client, leftover, maxBackupProtectMarker); err == nil && bytes.Equal(got, current) {
			if err := client.Remove(leftover); err != nil {
				return err
			}
		}
	}
	return nil
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
	if _, _, err := b.StatPoint(ctx, id); err != nil {
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
	if err := b.sweepMarkers(client, id); err != nil {
		return err
	}
	current, err := b.readProtectMarker(client, id)
	if err != nil {
		return err
	}
	if current != nil {
		// Protecting again replaces the marker, so a stalled unprotect that read
		// the old one cannot remove it.
		if err := b.removeMarkerIfUnchanged(client, name, current); err != nil || !protected {
			return err
		}
	}
	if !protected {
		return nil
	}
	return b.writeMarker(client, name, newBackupProtectMarker(id))
}

// sftpDeleteRecord keeps the exact published manifest bytes, so its digest
// still verifies after a Core upgrade changes how manifests marshal.
type sftpDeleteRecord struct {
	Manifest []byte `json:"manifest"`
	Digest   string `json:"digest"`
	manifest protocol.BackupManifest
}

func (b sftpBackupStorage) deleteRecordName(id string) string {
	return path.Join(b.root(), ".nex-delete-"+id)
}

func (b sftpBackupStorage) deleteSettled(client *sftp.Client, id string) (bool, error) {
	record, exists, err := b.readDeleteRecord(client, id)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil // Retired after it settled.
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
	if err := syncSFTPPath(client, doneName); err != nil {
		return false, err
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
	var ok bool
	if json.Unmarshal(raw, &record) != nil {
		return record, false, ErrBackupConflict
	}
	if record.manifest, ok = b.parseManifest(record.Manifest, id); !ok || record.Digest != sha256Hex(record.Manifest) {
		return record, false, ErrBackupConflict
	}
	return record, true, nil
}

func (b sftpBackupStorage) publishDeleteRecord(client *sftp.Client, id string, raw []byte) error {
	// Point IDs never contain a dot, so this cannot be another point's record.
	stage := b.deleteRecordName(id) + ".stage"
	final := b.deleteRecordName(id)
	if err := b.writeMarker(client, stage, raw); err != nil {
		return err
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

// SweepDeleteMarkers retires the delete records of points the last delete removed from the catalog.
func (b sftpBackupStorage) SweepDeleteMarkers(ctx context.Context) {
	client, closeAll, err := b.connect(ctx)
	if err != nil {
		return
	}
	defer closeAll()
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	_ = b.retireSettledDeletes(client)
}

func (b sftpBackupStorage) deleteOnClient(ctx context.Context, client *sftp.Client, id string) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	if err := b.requireWritableAuthority(); err != nil {
		return err
	}
	if err := b.retireSettledDeletes(client); err != nil {
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
		manifestRaw, err := b.readFile(client, path.Join(point, sftpManifestName), 1<<20)
		if err != nil {
			return err
		}
		if sha256Hex(manifestRaw) != digest {
			return ErrBackupConflict
		}
		record = sftpDeleteRecord{Manifest: manifestRaw, Digest: digest, manifest: manifest}
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
	for _, artifact := range manifestArtifacts(record.manifest) {
		allowed[artifact.Filename] = true
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
	return b.writeMarker(client, path.Join(b.root(), ".nex-deleted-"+id), []byte(record.Digest))
}

// retireSettledDeletes removes settled prune records once their catalog entries
// are gone. A record is needed only until then, to let an interrupted prune
// retry Delete; kept forever, every rebuild would read each one.
func (b sftpBackupStorage) retireSettledDeletes(client *sftp.Client) error {
	catalog, err := loadPoolBackupCatalog(b.s)
	if err != nil {
		return err
	}
	cataloged := make(map[string]bool, len(catalog))
	for _, point := range catalog {
		if point.RepositoryID == b.repo.ID {
			cataloged[point.ID] = true
		}
	}
	entries, err := client.ReadDir(b.root())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id, ok := strings.CutPrefix(entry.Name(), ".nex-deleted-")
		if !ok || !protocol.ValidSnapshotOperationID(id) || cataloged[id] {
			continue
		}
		settled, err := b.deleteSettled(client, id)
		if err != nil {
			return err
		}
		if !settled {
			continue
		}
		// The record goes first: a lone completion marker is inert.
		for _, name := range []string{b.deleteRecordName(id), path.Join(b.root(), entry.Name())} {
			if err := client.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := syncSFTPDir(client, b.root()); err != nil {
				return err
			}
		}
	}
	return nil
}
