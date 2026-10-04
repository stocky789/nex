// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"nex.local/nex/internal/protocol"
)

func TestSFTPHostKeyPinRejectsWrongKey(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ssh.FingerprintSHA256(public)
	if err := sftpPinnedHostKey(fingerprint)("", nil, public); err != nil {
		t.Fatal(err)
	}
	if err := sftpPinnedHostKey("SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")("", nil, public); err == nil {
		t.Fatal("wrong host key was accepted")
	}
}

func TestSFTPConnectCancelsStalledHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	storage := sftpBackupStorage{repo: protocol.BackupRepository{SFTPSource: &protocol.BackupSFTPSource{
		Host: "127.0.0.1", Port: port,
	}}, credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: testSSHPrivateKey(t)}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err = storage.connect(ctx)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("stalled SSH handshake ignored cancellation: %v", err)
	}
}

func TestSFTPIdentityBindsDestinationAndAccount(t *testing.T) {
	source := &protocol.BackupSFTPSource{Host: "backup.example.test", Root: "/backup", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	fingerprint := sftpIdentity(source, "backup")
	if len(fingerprint) != hex.EncodedLen(sha256.Size) {
		t.Fatal("invalid identity fingerprint")
	}
	changed := *source
	changed.Root = "/other"
	if fingerprint == sftpIdentity(&changed, "backup") {
		t.Fatal("root change retained identity")
	}
	if fingerprint == sftpIdentity(source, "other") {
		t.Fatal("account change retained identity")
	}
}

// stubSFTPSync replaces durable file sync, and directory sync when dirs is set,
// with no-ops for the in-process protocol mock.
func stubSFTPSync(t *testing.T, dirs bool) {
	t.Helper()
	oldFile, oldDir := syncSFTPFile, syncSFTPDir
	syncSFTPFile = func(*sftp.File) error { return nil }
	if dirs {
		syncSFTPDir = func(*sftp.Client, string) error { return nil }
	}
	t.Cleanup(func() { syncSFTPFile, syncSFTPDir = oldFile, oldDir })
}

func testSFTPClient(t *testing.T, options ...sftp.ServerOption) *sftp.Client {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	server, err := sftp.NewServer(serverConn, options...)
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client
}

// The server refuses every write itself: a chmod would not stop root, which is who CI runs as.
func TestSFTPPublishReportsReadOnlyRoot(t *testing.T) {
	client := testSFTPClient(t, sftp.ReadOnly())
	oldSync := syncSFTPDir
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	t.Cleanup(func() { syncSFTPDir = oldSync })
	root := t.TempDir()
	storage := sftpBackupStorage{repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	if _, err := storage.claimOwner(client); err == nil || errors.Is(err, ErrBackupConflict) || !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatalf("read-only owner claim returned %v instead of a permission failure", err)
	}
	manifest := protocol.BackupManifest{RecoveryPointID: "point-1"}
	raw := []byte("{}")
	_, err := storage.publishOnClient(context.Background(), client, manifest, nil, raw)
	if err == nil || errors.Is(err, ErrBackupConflict) || !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatalf("read-only root returned %v instead of a permission failure", err)
	}
	sum := sha256.Sum256(raw)
	if err := os.WriteFile(filepath.Join(root, ".nex-intent-point-1"), []byte(hex.EncodeToString(sum[:])), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = storage.publishOnClient(context.Background(), client, manifest, nil, raw)
	if err == nil || errors.Is(err, ErrBackupConflict) || !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatalf("read-only retry returned %v instead of a permission failure", err)
	}
}

func TestSFTPPublishResumesExactIncompletePoint(t *testing.T) {
	client := testSFTPClient(t) // In-process protocol mock; native OpenSSH durability remains untested.
	stubSFTPSync(t, false)
	root := t.TempDir()
	artifact := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(artifact, []byte("complete disk"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("complete disk"))
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1",
		SourceVMID: "vm-1", CapturedAt: time.Now(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("0", 64), DiskIdentitySHA256: strings.Repeat("0", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: int64(len("complete disk")), SHA256: hex.EncodeToString(sum[:])}},
		Complete: true, Success: 1,
	}
	if err := manifest.Valid(); err != nil {
		t.Fatal(err)
	}
	storage := sftpBackupStorage{s: seedBackupMaster(t), repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	intentSum := sha256.Sum256(raw)
	if err := storage.writeExclusive(client, filepath.Join(root, ".nex-intent-point-1"), strings.NewReader(hex.EncodeToString(intentSum[:]))); err != nil {
		t.Fatal(err)
	}
	if err := client.Mkdir(filepath.Join(root, "point-1")); err != nil {
		t.Fatal(err)
	}
	if err := storage.writeExclusive(client, filepath.Join(root, "point-1", "disk.qcow2"), strings.NewReader("partial")); err != nil {
		t.Fatal(err)
	}
	oldDirSync := syncSFTPDir
	failedFinalSync := false
	syncSFTPDir = func(client *sftp.Client, directory string) error {
		if directory == filepath.Join(root, "point-1") && !failedFinalSync {
			if _, err := os.Stat(filepath.Join(directory, sftpManifestName)); err == nil {
				failedFinalSync = true
				return errors.New("injected directory sync failure")
			}
		}
		return oldDirSync(client, directory)
	}
	t.Cleanup(func() { syncSFTPDir = oldDirSync })
	if _, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": artifact}, raw); err == nil || !failedFinalSync {
		t.Fatal("final directory sync failure was ignored")
	}
	syncSFTPDir = oldDirSync
	digest, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": artifact}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if digest != hex.EncodeToString(intentSum[:]) {
		t.Fatal("wrong published digest")
	}
	marker := filepath.Join(root, "point-1", ".nex-protected")
	if err := storage.writeExclusive(client, marker, strings.NewReader("protected\n")); err != nil {
		t.Fatal(err)
	}
	points, err := storage.scanOnClient(context.Background(), client)
	if err != nil || len(points) != 1 || !points[0].Protected {
		t.Fatal("scan lost protection", err)
	}
	if err := client.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := syncSFTPDir(client, filepath.Join(root, "point-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": artifact}, raw); err != nil {
		t.Fatal("exact retry:", err)
	}
	poolPeerMu.Lock()
	peer, peerErr := loadPoolPeer(storage.s)
	if peerErr == nil {
		peer.Restricted = &poolRestrictedRecord{Kind: poolRestrictedUnconfirmed, Reason: poolUnconfirmedReason, Since: time.Now().UTC()}
		peerErr = savePoolPeer(storage.s, peer)
	}
	poolPeerMu.Unlock()
	if peerErr != nil {
		t.Fatal(peerErr)
	}
	if _, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": artifact}, raw); err == nil {
		t.Fatal("restricted writer settled published point")
	}
	if err := storage.setProtectedOnClient(client, manifest.RecoveryPointID, true); err == nil {
		t.Fatal("restricted writer changed protection")
	}
	poolPeerMu.Lock()
	peer.Restricted = nil
	peerErr = savePoolPeer(storage.s, peer)
	poolPeerMu.Unlock()
	if peerErr != nil {
		t.Fatal(peerErr)
	}
	final := filepath.Join(root, "point-1", sftpManifestName)
	temp := filepath.Join(root, "point-1", ".nex-manifest")
	if err := client.Remove(final); err != nil {
		t.Fatal(err)
	}
	if err := storage.writeExclusive(client, temp, strings.NewReader(string(raw[:len(raw)/2]))); err != nil {
		t.Fatal(err)
	}
	if points, err := storage.scanOnClient(context.Background(), client); err != nil || len(points) != 0 {
		t.Fatal("staged manifest was cataloged", err)
	}
	if _, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": artifact}, raw); err != nil {
		t.Fatal("partial staged manifest retry:", err)
	}
	if err := client.Remove(final); err != nil {
		t.Fatal(err)
	}
	if err := storage.writeExclusive(client, temp, strings.NewReader("unrelated")); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": artifact}, raw); err == nil {
		t.Fatal("foreign staged manifest was overwritten")
	}

	changed := manifest
	changed.SourceVMName = "different"
	changedRaw, _ := json.Marshal(changed)
	if _, err := storage.publishOnClient(context.Background(), client, changed, map[string]string{"disk.qcow2": artifact}, changedRaw); err == nil {
		t.Fatal("different operation adopted point")
	}
	orphan := manifest
	orphan.RecoveryPointID = "point-2"
	orphanRaw, _ := json.Marshal(orphan)
	if err := client.Mkdir(filepath.Join(root, "point-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.publishOnClient(context.Background(), client, orphan, map[string]string{"disk.qcow2": artifact}, orphanRaw); err == nil {
		t.Fatal("unclaimed existing directory was adopted")
	}
}

func TestSFTPIncompletePublicationAbort(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	storage := sftpBackupStorage{s: seedBackupMaster(t), repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1",
		SourceVMID: "vm-1", CapturedAt: time.Now(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("0", 64), DiskIdentitySHA256: strings.Repeat("0", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: 8, SHA256: strings.Repeat("0", 64)}},
		Complete: true, Success: 1,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// A full target can fail after creating the point. The manifest must already
	// be durable before any artifact can be partially written.
	if _, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": filepath.Join(root, "missing-source")}, raw); err == nil {
		t.Fatal("missing source unexpectedly published")
	}
	staged := filepath.Join(root, "point-1", ".nex-manifest")
	if got, err := os.ReadFile(staged); err != nil || !strings.EqualFold(string(got), string(raw)) {
		t.Fatal("staged manifest was not durable before artifact write", err)
	}
	if err := os.WriteFile(filepath.Join(root, "point-1", "disk.qcow2"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "point-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete point remained", err)
	}
	for _, marker := range []string{".nex-intent-point-1", ".nex-abort-point-1"} {
		if _, err := os.Stat(filepath.Join(root, marker)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("incomplete marker remained", marker, err)
		}
	}
}

func TestSFTPAbortIncompleteResumesAndRefusesConflicts(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	storage := sftpBackupStorage{s: seedBackupMaster(t), repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1",
		SourceVMID: "vm-1", CapturedAt: time.Now(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("0", 64), DiskIdentitySHA256: strings.Repeat("0", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: 8, SHA256: strings.Repeat("0", 64)}},
		Complete: true, Success: 1,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	point := filepath.Join(root, "point-1")
	if err := os.Mkdir(point, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nex-intent-point-1"), []byte(hex.EncodeToString(sum[:])), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, ".nex-manifest"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(point, "disk.qcow2")
	if err := os.WriteFile(artifact, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "point-target")
	if err := os.Rename(point, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, point); err != nil {
		t.Fatal(err)
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("symlink point was followed", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "disk.qcow2")); err != nil || string(got) != "partial" {
		t.Fatalf("symlink point changed target artifact: %q %v", got, err)
	}
	if err := os.Remove(point); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, point); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(point, "foreign")
	if err := os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("unknown file was removed", err)
	}
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(artifact, foreign); err != nil {
		t.Fatal(err)
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("symlink was followed or removed", err)
	}
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, ".nex-protected"), []byte("protected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("protected marker was removed", err)
	}
	if err := os.Remove(filepath.Join(point, ".nex-protected")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, sftpManifestName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("ambiguous published manifest was removed", err)
	}
	if err := os.Remove(filepath.Join(point, sftpManifestName)); err != nil {
		t.Fatal(err)
	}
	finalWon := false
	syncSFTPDir = func(client *sftp.Client, directory string) error {
		if directory == root && !finalWon {
			if _, err := os.Stat(filepath.Join(root, ".nex-abort-point-1")); err == nil {
				if err := client.Rename(filepath.Join(point, ".nex-manifest"), filepath.Join(point, sftpManifestName)); err != nil {
					return err
				}
				finalWon = true
			}
		}
		return nil
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) || !finalWon {
		t.Fatal("delayed final rename lost its conflict", err)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatal("abort removed an artifact after final publication won", err)
	}
	if _, err := os.Stat(filepath.Join(point, sftpManifestName)); err != nil {
		t.Fatal("abort removed the final manifest", err)
	}
	if err := os.Remove(filepath.Join(point, sftpManifestName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, ".nex-manifest"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	failed := false
	syncSFTPDir = func(_ *sftp.Client, directory string) error {
		if directory == point && !failed {
			failed = true
			return errors.New("interrupted cleanup")
		}
		return nil
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); err == nil || !failed {
		t.Fatal("interrupted cleanup was ignored", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".nex-abort-point-1")); err != nil {
		t.Fatal("durable abort record missing", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nex-abort-point-1"), raw[:1], 0600); err != nil {
		t.Fatal(err)
	}
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); err != nil {
		t.Fatal("exact cleanup retry:", err)
	}
	if _, err := os.Stat(point); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial point remained after retry", err)
	}
}

func TestSFTPAbortEmptyInterruptedManifestOnly(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	storage := sftpBackupStorage{s: seedBackupMaster(t), repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	point := filepath.Join(root, "point-1")
	if err := os.Mkdir(point, 0700); err != nil {
		t.Fatal(err)
	}
	intent := strings.Repeat("a", 64)
	if err := os.WriteFile(filepath.Join(root, ".nex-intent-point-1"), []byte(intent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, ".nex-manifest"), []byte(`{"version":`), 0600); err != nil {
		t.Fatal(err)
	}
	failed := false
	syncSFTPDir = func(_ *sftp.Client, directory string) error {
		if directory == point && !failed {
			failed = true
			return errors.New("interrupted empty cleanup")
		}
		return nil
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); err == nil || !failed {
		t.Fatal("empty cleanup interruption was ignored", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".nex-abort-point-1")); err != nil {
		t.Fatal("empty cleanup lost its durable record", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nex-abort-point-1"), []byte("empty:"), 0600); err != nil {
		t.Fatal(err)
	}
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); err != nil {
		t.Fatal("manifest-only cleanup:", err)
	}
	if _, err := os.Stat(point); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("manifest-only point remained", err)
	}
	if err := os.Mkdir(point, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nex-intent-point-1"), []byte(intent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, "disk.qcow2"), []byte("legacy partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("legacy artifact without manifest was removed", err)
	}
	if _, err := os.Stat(filepath.Join(point, "disk.qcow2")); err != nil {
		t.Fatal("legacy partial artifact changed", err)
	}
}

func TestSFTPAbortWinsDelayedFinalRename(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	storage := sftpBackupStorage{s: seedBackupMaster(t), repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	disk := []byte("complete disk")
	sum := sha256.Sum256(disk)
	local := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(local, disk, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.BackupManifest{Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1",
		SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("0", 64), DiskIdentitySHA256: strings.Repeat("0", 64), Complete: true, Success: 1,
		Disks: []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: int64(len(disk)), SHA256: hex.EncodeToString(sum[:])}}}
	control := manifest
	control.RecoveryPointID = "control-1"
	controlRaw, err := json.Marshal(control)
	if err != nil {
		t.Fatal(err)
	}
	controlSHA, err := storage.publishOnClient(context.Background(), client, control, map[string]string{"disk.qcow2": local}, controlRaw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	intent := sha256.Sum256(raw)
	point := filepath.Join(root, manifest.RecoveryPointID)
	if err := os.Mkdir(point, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nex-intent-point-1"), []byte(hex.EncodeToString(intent[:])), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, ".nex-manifest"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, "disk.qcow2"), disk, 0600); err != nil {
		t.Fatal(err)
	}
	claimed, release := make(chan struct{}), make(chan struct{})
	paused := false
	syncSFTPDir = func(_ *sftp.Client, directory string) error {
		if directory == point && !paused {
			paused = true
			close(claimed)
			<-release // Hold abort before its first artifact removal.
		}
		return nil
	}
	aborted := make(chan error, 1)
	go func() {
		aborted <- storage.abortIncompleteOnClient(context.Background(), client, manifest.RecoveryPointID)
	}()
	select {
	case <-claimed:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("abort did not reach the manifest claim")
	}
	if _, err := os.Stat(filepath.Join(point, ".nex-abort-manifest")); err != nil {
		close(release)
		t.Fatal("abort did not claim the staged manifest before deletion", err)
	}
	if _, err := os.Stat(filepath.Join(point, "disk.qcow2")); err != nil {
		close(release)
		t.Fatal("abort removed artifact before manifest claim", err)
	}
	renameErr := client.Rename(filepath.Join(point, ".nex-manifest"), filepath.Join(point, sftpManifestName))
	close(release)
	if renameErr == nil {
		t.Fatal("delayed final Rename published an aborted point")
	}
	if err := <-aborted; err != nil {
		t.Fatal("abort did not settle after winning claim:", err)
	}
	if _, err := os.Stat(filepath.Join(point, sftpManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("aborted point has a final manifest", err)
	}
	if _, observed, err := storage.readPoint(client, control.RecoveryPointID); err != nil || observed != controlSHA {
		t.Fatal("valid published control changed", err)
	}
}

func TestSFTPReconcileResumesZeroByteAbortMarker(t *testing.T) {
	root := t.TempDir()
	store, repo := testSFTPRepository(t, root)
	cmd := protocol.Command{OperationID: "point-1", Kind: protocol.BackupKindBackupNow,
		VM: &protocol.VMCreateSpec{ID: "vm-1", HostID: store.State.HostID}, Backup: &protocol.BackupNowRequest{RepositoryID: repo.ID}}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := savePoolBackupRun(store, protocol.BackupRun{OperationID: cmd.OperationID, Kind: cmd.Kind, PoolID: store.State.PoolID,
		VMID: cmd.VM.ID, HostID: store.State.HostID, RepositoryID: repo.ID, RecoveryPointID: cmd.OperationID,
		IntentSHA256: intent, Stage: "publication", Status: "needs_attention", Error: "no space left on device", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.BackupManifest{Version: protocol.BackupManifestVersion, RecoveryPointID: cmd.OperationID, RepositoryID: repo.ID, PoolID: repo.PoolID,
		SourceVMID: cmd.VM.ID, CapturedAt: now, Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("0", 64), DiskIdentitySHA256: strings.Repeat("0", 64), Complete: true, Success: 1,
		Disks: []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: 8, SHA256: strings.Repeat("0", 64)}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	point := filepath.Join(root, cmd.OperationID)
	if err := os.Mkdir(point, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	for name, content := range map[string][]byte{
		".nex-intent-point-1": []byte(hex.EncodeToString(sum[:])), ".nex-abort-point-1": {},
	} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(point, ".nex-manifest"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, "disk.qcow2"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	backend, err := openBackupRepositoryStorage(store, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.StatPoint(context.Background(), cmd.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete point became readable during abort", err)
	}
	if err := os.WriteFile(filepath.Join(point, sftpManifestName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.StatPoint(context.Background(), cmd.OperationID); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("final manifest plus abort was accepted", err)
	}
	if err := os.Remove(filepath.Join(point, sftpManifestName)); err != nil {
		t.Fatal(err)
	}
	result := reconcilePublishedBackupNow(context.Background(), store, cmd, time.Now().UTC())
	if result.Status != "failed" {
		t.Fatalf("zero-byte abort did not settle through Core reconciliation: %+v", result)
	}
	if run, err := loadPoolBackupRun(store, cmd.OperationID); err != nil || run.Status != "failed" {
		t.Fatalf("Core run remained uncertain: %+v, %v", run, err)
	}
	if _, err := os.Stat(point); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial point remained", err)
	}
}

func TestSFTPScanRejectsPublishedCorruption(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, false)
	root := t.TempDir()
	storage := sftpBackupStorage{repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	if _, _, err := storage.readPoint(client, "point-missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing point error lost os.ErrNotExist:", err)
	}
	if err := client.Mkdir(filepath.Join(root, "point-1")); err != nil {
		t.Fatal(err)
	}
	points, err := storage.scanOnClient(context.Background(), client)
	if err != nil || len(points) != 0 {
		t.Fatalf("incomplete point: %v, %d", err, len(points))
	}
	if err := storage.writeExclusive(client, filepath.Join(root, "point-1", sftpManifestName), strings.NewReader("corrupt")); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.scanOnClient(context.Background(), client); err == nil {
		t.Fatal("published corrupt manifest was skipped")
	}
	if err := client.Remove(filepath.Join(root, "point-1", sftpManifestName)); err != nil {
		t.Fatal(err)
	}
	manifest := protocol.BackupManifest{
		Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1",
		SourceVMID: "vm-1", CapturedAt: time.Now(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("0", 64), DiskIdentitySHA256: strings.Repeat("0", 64),
		Disks:    []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: 1, SHA256: strings.Repeat("0", 64)}},
		Complete: true, Success: 1,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.writeExclusive(client, filepath.Join(root, "point-1", sftpManifestName), strings.NewReader(string(raw))); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.scanOnClient(context.Background(), client); err == nil {
		t.Fatal("published missing artifact was skipped")
	}
}

func TestSFTPDirectorySyncRefusesUnsupportedServer(t *testing.T) {
	client := testSFTPClient(t) // In-process server does not advertise fsync@openssh.com.
	root := t.TempDir()
	if err := syncSFTPPath(client, root); err == nil {
		t.Fatal("unsupported directory fsync was accepted")
	}
	stubSFTPSync(t, false)
	if err := syncSFTPPath(client, root); err != nil {
		t.Fatal("directory OPEN/close failed:", err)
	}
}

func TestSFTPOwnerClaimRejectsForeignOwner(t *testing.T) {
	client := testSFTPClient(t) // In-process protocol mock; native account confinement is separate.
	stubSFTPSync(t, false)
	root := t.TempDir()
	storage := sftpBackupStorage{repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	if _, err := storage.claimOwner(client); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.claimOwner(client); err != nil {
		t.Fatal("exact claim retry:", err)
	}
	foreign := storage
	foreign.repo.PoolID = "pool-2"
	if _, err := foreign.claimOwner(client); err == nil {
		t.Fatal("foreign pool claimed repository")
	}
	owner, err := storage.readFile(client, filepath.Join(root, "owner.json"), 4096)
	if err != nil || !strings.Contains(string(owner), `"pool":"pool-1"`) {
		t.Fatal("owner marker changed")
	}
}

func TestSFTPAdoptionDoesNotClaimMissingOwner(t *testing.T) {
	client := testSFTPClient(t)
	root := t.TempDir()
	storage := sftpBackupStorage{repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1",
		SFTPSource: &protocol.BackupSFTPSource{Root: root}, RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)}}}
	if _, err := storage.claimOwner(client); err == nil {
		t.Fatal("adoption claimed a missing SFTP owner")
	}
	if _, err := client.Lstat(filepath.Join(root, "owner.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adoption left an owner marker: %v", err)
	}
}

// Refused registration must release owner.json so a corrected retry under a new
// repository ID can claim the same root.
func TestSFTPRefusedRegistrationReleasesOwner(t *testing.T) {
	oldFileSync, oldDirSync, oldSyncSupported := syncSFTPFile, syncSFTPDir, sftpSyncSupported
	syncSFTPFile = func(*sftp.File) error { return nil }
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	var rootChecks int
	sftpSyncSupported = func(*sftp.Client) bool {
		rootChecks++
		// Succeed checkRoot before the claim; fail verify's checkRoot afterward.
		return rootChecks == 1
	}
	t.Cleanup(func() { syncSFTPFile, syncSFTPDir, sftpSyncSupported = oldFileSync, oldDirSync, oldSyncSupported })

	hostKey := testSSHSigner(t)
	port := startTestSFTPServer(t, hostKey)
	root := t.TempDir()
	cred := protocol.StorageCredentials{Username: "backup", SSHPrivateKey: testSSHPrivateKey(t)}
	source := &protocol.BackupSFTPSource{Host: "127.0.0.1", Port: port, Root: root, HostKeySHA256: ssh.FingerprintSHA256(hostKey.PublicKey())}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindSFTP, SFTPSource: source}
	if _, err := prepareSFTPBackupRepository(context.Background(), repo, t.TempDir(), cred); err == nil {
		t.Fatal("registration without durable sync proof succeeded")
	}
	if _, err := os.Lstat(filepath.Join(root, "owner.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused registration kept its owner claim")
	}

	// The in-process server cannot prove rename non-overwrite, so the corrected
	// retry is checked at the claim that would otherwise remain blocked.
	sftpSyncSupported = func(*sftp.Client) bool { return true }
	repo.ID = "repo-2"
	storage := sftpBackupStorage{repo: repo, credentials: cred, stateDir: t.TempDir()}
	client, closeAll, err := storage.connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	if _, err := storage.claimOwner(client); err != nil {
		t.Fatalf("corrected claim refused: %v", err)
	}
}

func TestSFTPRegistrationRefusesMemberBeforePreparation(t *testing.T) {
	lab := newTransferLab(t, 1)
	member := lab.host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: member.State.PoolID, Name: "Test", Kind: protocol.BackupRepoKindSFTP,
		SFTPSource:    &protocol.BackupSFTPSource{Host: "sftp.example.test", Root: "/backups", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		CredentialRef: "credential-1"}
	if _, err := prepareBackupRepositoryForRegistration(context.Background(), member, repo, "register-1", "", nil); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("member reached SFTP preparation: %v", err)
	}
}

func TestSFTPPreparedIdentityMatchesExactSource(t *testing.T) {
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Test", Kind: protocol.BackupRepoKindSFTP,
		SFTPSource: &protocol.BackupSFTPSource{Host: "sftp.example.test", Root: "/backups", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}
	prepared := repo
	prepared.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: sftpIdentity(repo.SFTPSource, "backup")}
	if !preparedBackupRepositoryMatches(repo, prepared) {
		t.Fatal("prepared SFTP identity rejected")
	}
	changed := repo
	changed.SFTPSource = &protocol.BackupSFTPSource{Host: repo.SFTPSource.Host, Root: "/other", HostKeySHA256: repo.SFTPSource.HostKeySHA256}
	if preparedBackupRepositoryMatches(changed, prepared) {
		t.Fatal("changed SFTP source accepted")
	}
	adopted := prepared
	adopted.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("0", 64)}
	if preparedBackupRepositoryMatches(prepared, adopted) {
		t.Fatal("wrong adopted identity accepted")
	}
}

func testPeerSFTPRepository(t *testing.T, master *Store) protocol.BackupRepository {
	t.Helper()
	receipt, err := saveBackupCredential(master, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "sftp-credential-1", RegistrationRequestID: "backup-credential:sftp:register-1",
		PoolID: master.State.PoolID, HostID: master.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: "private-example-key"}})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.BackupRepository{ID: "repo-1", PoolID: master.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindSFTP,
		SFTPSource:     &protocol.BackupSFTPSource{Host: "sftp.example.test", Root: "/backups", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)}, CredentialRef: receipt.Receipt}
}

func TestPeerSFTPPreflightRequiresPrivateReceiptExactIdentity(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerSFTPRepository(t, master)
	if _, err := preparePeerRepository(context.Background(), member, repo, nil); err == nil {
		t.Fatal("member prepared SFTP without private receipt")
	}
	projection, err := exportPeerBackupCredential(master, repo, member.State.HostID)
	if err != nil || importPeerBackupCredential(member, projection) != nil {
		t.Fatalf("private projection: %v", err)
	}
	previous := prepareBackupRepositorySFTPNative
	t.Cleanup(func() { prepareBackupRepositorySFTPNative = previous })
	prepareBackupRepositorySFTPNative = func(_ context.Context, got protocol.BackupRepository, stateDir string, credentials protocol.StorageCredentials) (protocol.BackupRepository, error) {
		if got.ID != repo.ID || stateDir != member.dir || credentials.Username != "backup" || credentials.SSHPrivateKey != "private-example-key" {
			t.Fatal("wrong member SFTP credential scope")
		}
		got.RemoteIdentity = repo.RemoteIdentity
		return got, nil
	}
	prepared, err := preparePeerRepository(context.Background(), member, repo, nil)
	if err != nil || !preparedBackupRepositoryMatches(repo, prepared) {
		t.Fatalf("member SFTP capability: %v", err)
	}
	wrong := repo
	wrong.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("b", 64)}
	if _, err := preparePeerRepository(context.Background(), member, wrong, nil); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("wrong identity accepted: %v", err)
	}
}

func TestPeerSFTPPreflightRefusesOldMemberAndFencedMaster(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerSFTPRepository(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	oldMember := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; http.NotFound(w, r) }))
	defer oldMember.Close()
	work := pendingPeerBackup{store: master, repo: repo, localRepo: repo,
		member: protocol.PoolMember{HostID: member.State.HostID, PeerOrigin: oldMember.URL}, client: oldMember.Client()}
	if err := preflightPeerSFTPRepository(context.Background(), work); err == nil {
		t.Fatal("old member without SFTP capability endpoint accepted")
	} else {
		var permanent permanentPeerBackupError
		if !errors.As(err, &permanent) {
			t.Fatalf("old member refusal retried: %v", err)
		}
	}
	state := peerState(t, master)
	state.Transfer = &poolTransferRecord{Frozen: true}
	if err := savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
	if err := preflightPeerSFTPRepository(context.Background(), work); err == nil || requests != 1 {
		t.Fatalf("fenced master contacted member: %v, requests=%d", err, requests)
	}
}

func TestPeerSFTPPreflightAuthenticatedLostReplyRetry(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerSFTPRepository(t, master)
	if err := savePoolBackupRepositories(master, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	peer, ok := poolMemberByID(membership, member.State.HostID)
	if !ok {
		t.Fatal("member missing")
	}
	if err := deliverPeerBackupCredential(context.Background(), master, peer, repo); err != nil {
		t.Fatal(err)
	}
	previous := prepareBackupRepositorySFTPNative
	t.Cleanup(func() { prepareBackupRepositorySFTPNative = previous })
	prepareCalls := 0
	prepareBackupRepositorySFTPNative = func(_ context.Context, got protocol.BackupRepository, stateDir string, credentials protocol.StorageCredentials) (protocol.BackupRepository, error) {
		prepareCalls++
		if got.ID != repo.ID || stateDir != member.dir || credentials.SSHPrivateKey != "private-example-key" {
			t.Fatal("wrong SFTP retry scope")
		}
		return got, nil
	}
	client, err := poolTransferClient(master, peer.PeerOrigin, peer.HostID)
	if err != nil {
		t.Fatal(err)
	}
	work := pendingPeerBackup{store: master, repo: repo, localRepo: repo, member: peer, client: client}
	lab.drop(member.State.HostID, "POST:"+poolBackupPeerPath+"/capability/sftp")
	if err := preflightPeerSFTPRepository(context.Background(), work); err == nil || prepareCalls != 1 {
		t.Fatalf("lost preflight reply accepted/skipped: %v calls=%d", err, prepareCalls)
	}
	lab.drop(member.State.HostID, "")
	if err := preflightPeerSFTPRepository(context.Background(), work); err != nil || prepareCalls != 2 {
		t.Fatalf("exact preflight retry failed: %v calls=%d", err, prepareCalls)
	}
}

func TestSFTPDeleteResumesExactInterruptedPoint(t *testing.T) {
	client := testSFTPClient(t) // In-process protocol mock; native crash gate is separate.
	stubSFTPSync(t, false)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	artifact := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(artifact, []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("disk"))
	manifest := protocol.BackupManifest{Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1", PoolID: store.State.PoolID,
		SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("0", 64), DiskIdentitySHA256: strings.Repeat("0", 64), Complete: true, Success: 1,
		Disks: []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: 4, SHA256: hex.EncodeToString(sum[:])}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": artifact}, raw)
	if err != nil {
		t.Fatal(err)
	}
	point := protocol.BackupRecoveryPoint{ID: manifest.RecoveryPointID, RepositoryID: manifest.RepositoryID, PoolID: manifest.PoolID,
		SourceVMID: manifest.SourceVMID, CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency, ManifestSHA256: digest}
	if err := savePoolBackupCatalogEntry(store, point); err != nil {
		t.Fatal(err)
	}
	pin := protocol.BackupRestorePin{OperationID: "restore-1", RecoveryPointID: "point-1", Kind: protocol.BackupPinKindNew, CreatedAt: time.Now().UTC()}
	if err := savePoolBackupPins(store, poolBackupPinsState{Pins: map[string]protocol.BackupRestorePin{"restore-1": pin}}); err != nil {
		t.Fatal(err)
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); err == nil {
		t.Fatal("pinned point deleted")
	}
	if err := savePoolBackupPins(store, poolBackupPinsState{Pins: map[string]protocol.BackupRestorePin{}}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "point-1", ".nex-protected")
	if err := storage.writeExclusive(client, marker, strings.NewReader("protected\n")); err != nil {
		t.Fatal(err)
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); err == nil {
		t.Fatal("protected point deleted")
	}
	if err := client.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := syncSFTPDir(client, filepath.Join(root, "point-1")); err != nil {
		t.Fatal(err)
	}
	recordRaw, err := json.Marshal(sftpDeleteRecord{Manifest: raw, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	stageRecord := filepath.Join(root, ".nex-delete-point-1.stage")
	if err := storage.writeExclusive(client, stageRecord, strings.NewReader(string(recordRaw[:len(recordRaw)/2]))); err != nil {
		t.Fatal(err)
	}
	if err := storage.publishDeleteRecord(client, "point-1", recordRaw); err != nil {
		t.Fatal("partial delete record retry:", err)
	}
	if _, _, err := storage.readPoint(client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("point with pending deletion remained readable: %v", err)
	}
	if err := client.Remove(filepath.Join(root, "point-1", sftpManifestName)); err != nil {
		t.Fatal(err)
	}
	if err := syncSFTPDir(client, filepath.Join(root, "point-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.scanOnClient(context.Background(), client); err == nil {
		t.Fatal("rebuild accepted incomplete prune")
	}
	if err := client.Remove(filepath.Join(root, "point-1", "disk.qcow2")); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveDirectory(filepath.Join(root, "point-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.scanOnClient(context.Background(), client); err == nil {
		t.Fatal("scan hid prune after point directory removal")
	}
	if settled, err := storage.deleteSettled(client, "point-1"); err != nil || settled {
		t.Fatalf("interrupted prune appeared settled: %t %v", settled, err)
	}
	done := filepath.Join(root, ".nex-deleted-point-1")
	failDoneSync := true
	syncSFTPFile = func(file *sftp.File) error {
		if file.Name() == done && failDoneSync {
			return errors.New("injected marker file sync failure")
		}
		return nil
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); err == nil {
		t.Fatal("failed marker file sync reported settled deletion")
	}
	if _, err := storage.scanOnClient(context.Background(), client); err == nil {
		t.Fatal("rebuild accepted marker with failed file sync")
	}
	failDoneSync = false
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); err != nil {
		t.Fatal("full completion marker sync retry:", err)
	}
	if err := client.Remove(done); err != nil {
		t.Fatal(err)
	}
	if err := storage.writeExclusive(client, done, strings.NewReader("invalid")); err != nil {
		t.Fatal(err)
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("unrelated completion marker was replaced: %v", err)
	}
	if err := client.Remove(done); err != nil {
		t.Fatal(err)
	}
	if err := storage.writeExclusive(client, done, strings.NewReader(digest[:len(digest)/2])); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.scanOnClient(context.Background(), client); err == nil {
		t.Fatal("partial completion marker unblocked rebuild")
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); err != nil {
		t.Fatal("partial completion marker retry:", err)
	}
	if _, err := client.Lstat(filepath.Join(root, "point-1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("point directory remains:", err)
	}
	if _, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": artifact}, raw); err == nil {
		t.Fatal("deleted point republished")
	}
	if points, err := storage.scanOnClient(context.Background(), client); err != nil || len(points) != 0 {
		t.Fatal("deleted point cataloged", err)
	}
	if settled, err := storage.deleteSettled(client, "point-1"); err != nil || !settled {
		t.Fatalf("completed prune lacks durable settlement: %t %v", settled, err)
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); err != nil {
		t.Fatal("completed prune retry:", err)
	}
}

func TestMemberBackupSettlementLeavesRetentionToMaster(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	now := time.Now().UTC()
	run := protocol.BackupRun{OperationID: "point-1", Kind: protocol.BackupKindBackupNow,
		PoolID: member.State.PoolID, HostID: member.State.HostID, RepositoryID: "repo-1", RecoveryPointID: "point-1", VMID: "vm-1",
		Status: "pending", Stage: "reserved", CreatedAt: now, Retention: &protocol.BackupRetention{KeepLatest: 1}}
	point := protocol.BackupRecoveryPoint{ID: "point-1", RepositoryID: "repo-1", PoolID: member.State.PoolID,
		SourceVMID: "vm-1", CapturedAt: now, Consistency: protocol.BackupCaptureStopped, ManifestSHA256: strings.Repeat("a", 64)}
	if err := savePoolBackupRun(member, run); err != nil {
		t.Fatal(err)
	}
	settled := run
	settled.Status, settled.Stage = "succeeded", "published"
	if err := SettlePoolBackupRun(member, settled, &point); err != nil {
		t.Fatal("member performed retention or refused settlement:", err)
	}
	if saved, err := loadPoolBackupRun(member, run.OperationID); err != nil || saved.Status != "succeeded" {
		t.Fatal("member run not settled", err)
	}
	if err := prunePublishedBackupPayloads(context.Background(), member, run.RepositoryID, run.VMID, "policy-1", *run.Retention, ""); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("member entered retention path: %v", err)
	}
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	if err := SettlePoolBackupRun(master, settled, &point); err != nil {
		t.Fatal("retention failure blocked master settlement:", err)
	}
	if saved, err := loadPoolBackupRun(master, run.OperationID); err != nil || saved.Status != "succeeded" ||
		!strings.Contains(saved.Error, ErrBackupRepoNotFound.Error()) {
		t.Fatalf("master skipped retention path: %+v %v", saved, err)
	}
}

// Regression for BUG-0716: a server that stops answering during or after the
// SSH handshake must fail the connection without relying on a caller deadline.
func TestSFTPConnectTimesOutStalledServer(t *testing.T) {
	oldTimeout := sftpIOTimeout
	sftpIOTimeout = 200 * time.Millisecond
	t.Cleanup(func() { sftpIOTimeout = oldTimeout })
	hostSigner := testSSHSigner(t)
	for _, afterHandshake := range []bool{false, true} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		go func() {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			defer conn.Close()
			if afterHandshake {
				config := &ssh.ServerConfig{NoClientAuth: true}
				config.AddHostKey(hostSigner)
				if _, _, _, err := ssh.NewServerConn(conn, config); err != nil {
					return
				}
				// Never service channels or global requests: a hung server.
			}
			_, _ = io.Copy(io.Discard, conn)
		}()
		storage := sftpBackupStorage{repo: protocol.BackupRepository{SFTPSource: &protocol.BackupSFTPSource{
			Host: "127.0.0.1", Port: uint16(listener.Addr().(*net.TCPAddr).Port), HostKeySHA256: ssh.FingerprintSHA256(hostSigner.PublicKey()),
		}}, credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: testSSHPrivateKey(t)}}
		started := time.Now()
		_, closeAll, err := storage.connect(context.Background())
		if err == nil {
			closeAll()
		}
		if err == nil || time.Since(started) > 3*time.Second {
			t.Fatalf("stalled server (after handshake %t) was not bounded: %v after %s", afterHandshake, err, time.Since(started))
		}
	}
}

// Regression for BUG-0740: keepalive replies queued behind a busy link must not
// close a connection that is still receiving; a silent one still fails.
func TestSFTPKeepsProgressingConnectionAndClosesSilentOne(t *testing.T) {
	oldTimeout, oldRequests := sftpIOTimeout, testSFTPServerRequests
	sftpIOTimeout = 100 * time.Millisecond
	// A server whose keepalive replies never arrive.
	testSFTPServerRequests = func(requests <-chan *ssh.Request) {
		for range requests {
		}
	}
	t.Cleanup(func() { sftpIOTimeout, testSFTPServerRequests = oldTimeout, oldRequests })
	hostKey := testSSHSigner(t)
	storage := sftpBackupStorage{repo: protocol.BackupRepository{SFTPSource: &protocol.BackupSFTPSource{
		Host: "127.0.0.1", Port: startTestSFTPServer(t, hostKey), HostKeySHA256: ssh.FingerprintSHA256(hostKey.PublicKey()),
	}}, credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: testSSHPrivateKey(t)}}
	client, closeAll, err := storage.connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	for until := time.Now().Add(time.Second); time.Now().Before(until); time.Sleep(20 * time.Millisecond) {
		if _, err := client.Getwd(); err != nil {
			t.Fatal("progressing connection was closed:", err)
		}
	}
	time.Sleep(time.Second)
	if _, err := client.Getwd(); err == nil {
		t.Fatal("silent connection stayed open")
	}
}

// Regression for BUG-0716: prune, protection and rebuild check the manifest and
// artifact sizes; only restore paths hash artifact content.
func TestSFTPMetadataReadsSkipArtifactContent(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, false)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	artifact := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(artifact, []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("disk"))
	manifest := protocol.BackupManifest{Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1", PoolID: store.State.PoolID,
		SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("0", 64), DiskIdentitySHA256: strings.Repeat("0", 64), Complete: true, Success: 1,
		Disks: []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: 4, SHA256: hex.EncodeToString(sum[:])}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": artifact}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupCatalogEntry(store, protocol.BackupRecoveryPoint{ID: "point-1", RepositoryID: "repo-1", PoolID: manifest.PoolID,
		SourceVMID: manifest.SourceVMID, CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency, ManifestSHA256: digest}); err != nil {
		t.Fatal(err)
	}
	// Same-size content change: only a hashing read can detect it.
	if err := os.WriteFile(filepath.Join(root, "point-1", "disk.qcow2"), []byte("DISK"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyBackupPoint(t, storage, "point-1"); err == nil {
		t.Fatal("restore copy accepted changed artifact content")
	}
	if points, err := storage.scanOnClient(context.Background(), client); err != nil || len(points) != 1 {
		t.Fatalf("rebuild hashed artifact content: %v", err)
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); err != nil {
		t.Fatalf("prune hashed artifact content: %v", err)
	}
}

// testSFTPServerRequests handles the test server's global requests, keepalives included.
var testSFTPServerRequests = ssh.DiscardRequests

// startTestSFTPServer serves the in-process SFTP subsystem over SSH with the
// given host keys and accepts any client key.
func startTestSFTPServer(t *testing.T, hostKeys ...ssh.Signer) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	for _, key := range hostKeys {
		serverConfig.AddHostKey(key)
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				server, channels, requests, err := ssh.NewServerConn(connection, serverConfig)
				if err != nil {
					connection.Close()
					return
				}
				defer server.Close()
				go testSFTPServerRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for request := range requests {
							if request.Type != "subsystem" {
								request.Reply(false, nil)
								continue
							}
							request.Reply(true, nil)
							service, err := sftp.NewServer(channel)
							if err == nil {
								service.Serve()
								service.Close()
							}
							return
						}
					}()
				}
			}()
		}
	}()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}

func testSSHSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func testSSHPrivateKey(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

// testSFTPRepository registers an SFTP repository at root on a seeded master,
// served over real SSH by the in-process server. Durable sync is stubbed.
func testSFTPRepository(t *testing.T, root string) (*Store, protocol.BackupRepository) {
	t.Helper()
	oldFileSync, oldDirSync, oldSyncSupported := syncSFTPFile, syncSFTPDir, sftpSyncSupported
	syncSFTPFile = func(*sftp.File) error { return nil }
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	sftpSyncSupported = func(*sftp.Client) bool { return true }
	t.Cleanup(func() { syncSFTPFile, syncSFTPDir, sftpSyncSupported = oldFileSync, oldDirSync, oldSyncSupported })
	hostKey := testSSHSigner(t)
	port := startTestSFTPServer(t, hostKey)
	store := seedBackupMaster(t)
	if err := os.WriteFile(filepath.Join(root, "owner.json"), []byte(`{"repository":"repo-1","pool":"`+store.State.PoolID+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := saveBackupCredential(store, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "sftp-test-credential", RegistrationRequestID: "backup-credential:sftp:test-register",
		PoolID: store.State.PoolID, HostID: store.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: testSSHPrivateKey(t)}})
	if err != nil {
		t.Fatal(err)
	}
	source := &protocol.BackupSFTPSource{Host: "127.0.0.1", Port: port, Root: root, HostKeySHA256: ssh.FingerprintSHA256(hostKey.PublicKey())}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindSFTP,
		SFTPSource: source, RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: sftpIdentity(source, "backup")}, CredentialRef: receipt.Receipt}
	if err := savePoolBackupRepositories(store, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
	return store, repo
}

// testSFTPPoint returns a valid one-disk manifest and its local artifact.
func testSFTPPoint(t *testing.T, poolID, id string) (protocol.BackupManifest, map[string]string) {
	t.Helper()
	artifact := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(artifact, []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("disk"))
	return protocol.BackupManifest{Version: protocol.BackupManifestVersion, RecoveryPointID: id, RepositoryID: "repo-1", PoolID: poolID,
		SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("0", 64), DiskIdentitySHA256: strings.Repeat("0", 64), Complete: true, Success: 1,
		Disks: []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: 4, SHA256: hex.EncodeToString(sum[:])}}}, map[string]string{"disk.qcow2": artifact}
}

// publishTestSFTPPoint publishes raw as the point's manifest and catalogs it.
func publishTestSFTPPoint(t *testing.T, storage sftpBackupStorage, client *sftp.Client, manifest protocol.BackupManifest, files map[string]string, raw []byte) {
	t.Helper()
	digest, err := storage.publishOnClient(context.Background(), client, manifest, files, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupCatalogEntry(storage.s, protocol.BackupRecoveryPoint{ID: manifest.RecoveryPointID, RepositoryID: manifest.RepositoryID, PoolID: manifest.PoolID,
		SourceVMID: manifest.SourceVMID, CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency, ManifestSHA256: digest}); err != nil {
		t.Fatal(err)
	}
}

// Regression for BUG-0717: a prune record must verify against the exact
// manifest bytes, its stage name must not be another point's record, and
// settled records are retired once their catalog entries are gone.
func TestSFTPPruneRecordsSurviveSchemaChangeAndRetire(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, false)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	// A manifest written by another Core version carries a field this one drops.
	older, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"retired_field":true}`)...)
	publishTestSFTPPoint(t, storage, client, older, files, raw)
	// "stage-point-2" has the record name the old stage name gave "point-2".
	for _, id := range []string{"stage-point-2", "point-2"} {
		manifest, files := testSFTPPoint(t, store.State.PoolID, id)
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		publishTestSFTPPoint(t, storage, client, manifest, files, raw)
	}
	for _, id := range []string{"point-1", "stage-point-2", "point-2"} {
		if err := storage.deleteOnClient(context.Background(), client, id); err != nil {
			t.Fatalf("prune %s: %v", id, err)
		}
	}
	if points, err := storage.scanOnClient(context.Background(), client); err != nil || len(points) != 0 {
		t.Fatalf("settled prunes blocked rebuild: %d %v", len(points), err)
	}
	if err := os.Remove(filepath.Join(poolBackupCatalogPath(store), "point-1.json")); err != nil {
		t.Fatal(err)
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-2"); err != nil {
		t.Fatal("settled prune retry:", err)
	}
	for _, name := range []string{".nex-delete-point-1", ".nex-deleted-point-1"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("settled prune record %s was not retired: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".nex-delete-point-2")); err != nil {
		t.Fatal("record of a still-cataloged prune was retired:", err)
	}
}

// Regression for BUG-0717: an empty marker left by a failed write (a full
// server) is finished by the next attempt instead of blocking as a conflict.
func TestSFTPEmptyMarkersRecover(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, false)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	for _, name := range []string{"owner.json", ".nex-intent-point-1", ".nex-intent-point-2"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storage.claimOwner(client); err != nil {
		t.Fatal("empty owner claim was not finished:", err)
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-2"); err != nil {
		t.Fatal("empty intent was not aborted:", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".nex-intent-point-2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("aborted empty intent remained:", err)
	}
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	intentName := filepath.Join(root, ".nex-intent-point-1")
	syncSFTPFile = func(file *sftp.File) error {
		if file.Name() == intentName {
			return errors.New("injected no space left on device")
		}
		return nil
	}
	if _, err := storage.publishOnClient(context.Background(), client, manifest, files, raw); err == nil || !strings.Contains(err.Error(), "no space") {
		t.Fatalf("failed intent write was not reported: %v", err)
	}
	syncSFTPFile = func(*sftp.File) error { return nil }
	publishTestSFTPPoint(t, storage, client, manifest, files, raw)
	marker := filepath.Join(root, "point-1", ".nex-protected")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if points, err := storage.scanOnClient(context.Background(), client); err != nil || len(points) != 1 || !points[0].Protected {
		t.Fatalf("empty protection marker blocked rebuild or lost protection: %v", err)
	}
	if err := storage.setProtectedOnClient(client, "point-1", true); err != nil {
		t.Fatal("empty protection marker was not finished:", err)
	}
	if got, err := os.ReadFile(marker); err != nil || !strings.HasPrefix(string(got), "protected\npoint-1\n") {
		t.Fatalf("protection marker %q: %v", got, err)
	}
	if err := os.WriteFile(marker, []byte("prot"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.setProtectedOnClient(client, "point-1", false); err != nil {
		t.Fatal("partial protection marker could not be cleared:", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("protection marker remained:", err)
	}
}

// Regression for BUG-0717: a delayed final rename that lands after the abort
// record but before the abort's manifest claim leaves a whole, readable point.
func TestSFTPAbortLosingToDelayedRenameUnblocksPoint(t *testing.T) {
	client := testSFTPClient(t)
	oldFileSync, oldDirSync := syncSFTPFile, syncSFTPDir
	syncSFTPFile = func(*sftp.File) error { return nil }
	t.Cleanup(func() { syncSFTPFile, syncSFTPDir = oldFileSync, oldDirSync })
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	intent := sha256.Sum256(raw)
	point := filepath.Join(root, "point-1")
	if err := os.Mkdir(point, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(files["disk.qcow2"])
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		filepath.Join(root, ".nex-intent-point-1"): []byte(hex.EncodeToString(intent[:])),
		filepath.Join(point, ".nex-manifest"):      raw, filepath.Join(point, "disk.qcow2"): disk,
	} {
		if err := os.WriteFile(name, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	abortRecord := filepath.Join(root, ".nex-abort-point-1")
	syncSFTPDir = func(_ *sftp.Client, directory string) error {
		if _, err := os.Stat(abortRecord); directory == root && err == nil {
			// The dead publisher's queued rename lands now.
			_ = os.Rename(filepath.Join(point, ".nex-manifest"), filepath.Join(point, sftpManifestName))
		}
		return nil
	}
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("abort that lost its claim returned %v", err)
	}
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	if err := storage.abortIncompleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("abort of a published point returned %v", err)
	}
	if _, observed, err := storage.readPoint(client, "point-1"); err != nil || observed != hex.EncodeToString(intent[:]) {
		t.Fatalf("published point stayed blocked by a lost abort: %v", err)
	}
	if _, err := storage.scanOnClient(context.Background(), client); err != nil {
		t.Fatal("lost abort blocked rebuild:", err)
	}
}

// Regression for BUG-0717: the client offers one plain host-key type at a
// time, so a pinned Ed25519 or ECDSA key works on a server that also has
// other keys and host certificates.
func TestSFTPConnectFindsPinnedHostKeyType(t *testing.T) {
	plain := testSSHSigner(t)
	ca := testSSHSigner(t)
	cert := &ssh.Certificate{Key: plain.PublicKey(), CertType: ssh.HostCert, ValidPrincipals: []string{"127.0.0.1"}, ValidBefore: ssh.CertTimeInfinity}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	certSigner, err := ssh.NewCertSigner(cert, plain)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaSigner, err := ssh.NewSignerFromKey(ecdsaKey)
	if err != nil {
		t.Fatal(err)
	}
	port := startTestSFTPServer(t, ecdsaSigner, plain, certSigner)
	privateKey := testSSHPrivateKey(t)
	for i, pinned := range []ssh.PublicKey{plain.PublicKey(), ecdsaSigner.PublicKey(), ca.PublicKey()} {
		storage := sftpBackupStorage{repo: protocol.BackupRepository{SFTPSource: &protocol.BackupSFTPSource{
			Host: "127.0.0.1", Port: port, HostKeySHA256: ssh.FingerprintSHA256(pinned),
		}}, credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: privateKey}}
		_, closeAll, err := storage.connect(context.Background())
		if i == 2 {
			if err == nil || !strings.Contains(err.Error(), "pinned") {
				t.Fatalf("unpinned host key accepted or misreported: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("pinned %s host key refused: %v", pinned.Type(), err)
		}
		closeAll()
	}
}

// Regression for BUG-0717: a health probe refuses without pool authority and
// removes its probe file when the first write fails.
func TestSFTPProbeChecksAuthorityAndCleansUp(t *testing.T) {
	root := t.TempDir()
	store, repo := testSFTPRepository(t, root)
	backend, err := openBackupRepositoryStorage(store, repo)
	if err != nil {
		t.Fatal(err)
	}
	probes := func() []string {
		matches, _ := filepath.Glob(filepath.Join(root, ".nex-probe-*"))
		return matches
	}
	syncSFTPFile = func(*sftp.File) error { return errors.New("injected sync failure") }
	if err := backend.Probe(context.Background()); err == nil || len(probes()) != 0 {
		t.Fatalf("failed probe write left %v: %v", probes(), err)
	}
	syncSFTPFile = func(*sftp.File) error { return nil }
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(store)
	if err == nil {
		peer.Restricted = &poolRestrictedRecord{Kind: poolRestrictedUnconfirmed, Reason: poolUnconfirmedReason, Since: time.Now().UTC()}
		err = savePoolPeer(store, peer)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	want := poolAuthorityWritable(peer)
	if err := backend.Probe(context.Background()); err == nil || err.Error() != want.Error() {
		t.Fatalf("restricted host probed the repository: %v", err)
	}
}

// Regression for BUG-0717: restore copy keeps the real read failure, and the
// public wrappers (Protected, SetProtected, Entries, CopyArtifacts) work over SSH.
func TestSFTPPublicWrappersOverSSH(t *testing.T) {
	root := t.TempDir()
	store, repo := testSFTPRepository(t, root)
	backend, err := openBackupRepositoryStorage(store, repo)
	if err != nil {
		t.Fatal(err)
	}
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	if _, err := backend.Publish(context.Background(), manifest, files); err != nil {
		t.Fatal(err)
	}
	restore := t.TempDir()
	missing := manifest
	missing.RecoveryPointID = "point-2"
	if err := backend.CopyArtifacts(context.Background(), "point-2", restore, missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing point copy returned %v", err)
	}
	if err := backend.CopyArtifacts(context.Background(), "point-1", restore, manifest); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(restore, "disk.qcow2")); err != nil || string(got) != "disk" {
		t.Fatalf("restored artifact %q: %v", got, err)
	}
	protector := backend.(sftpBackupStorage)
	if err := protector.SetProtected(context.Background(), "point-1", true); err != nil {
		t.Fatal(err)
	}
	if protected, err := protector.Protected(context.Background(), "point-1"); err != nil || !protected {
		t.Fatalf("protection not reported: %t %v", protected, err)
	}
	if err := protector.SetProtected(context.Background(), "point-1", false); err != nil {
		t.Fatal(err)
	}
	if protected, err := protector.Protected(context.Background(), "point-1"); err != nil || protected {
		t.Fatalf("protection not cleared: %t %v", protected, err)
	}
	entries, err := protector.Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(entries, "point-1") {
		t.Fatalf("entries omitted the point: %v", entries)
	}
}

// A stalled unprotect that read an older marker must not remove a newer one.
func TestSFTPStaleUnprotectKeepsNewerMarker(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	if err := os.Mkdir(filepath.Join(root, "point-1"), 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(root, "point-1", ".nex-protected")
	if err := storage.writeMarker(client, name, newBackupProtectMarker("point-1")); err != nil {
		t.Fatal(err)
	}
	stale, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	// The newer request protects again before the stalled unprotect resumes.
	if err := storage.setProtectedOnClient(client, "point-1", true); err != nil {
		t.Fatal(err)
	}
	newer, err := os.ReadFile(name)
	if err != nil || string(newer) == string(stale) {
		t.Fatalf("protect did not write a unique marker: %v", err)
	}
	if err := storage.removeMarkerIfUnchanged(client, name, stale); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("stale unprotect removed or ignored the newer marker:", err)
	}
	if got, err := os.ReadFile(name); err != nil || string(got) != string(newer) {
		t.Fatalf("newer marker was lost: %q %v", got, err)
	}
	if err := storage.setProtectedOnClient(client, "point-1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unprotect left the marker:", err)
	}
}

// Regression for BUG-0751: a maximum-length point ID makes a 102-byte marker,
// which the marker read cap must still accept.
func TestSFTPProtectsMaximumLengthPointID(t *testing.T) {
	root := t.TempDir()
	store, repo := testSFTPRepository(t, root)
	backend, err := openBackupRepositoryStorage(store, repo)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	manifest, files := testSFTPPoint(t, store.State.PoolID, id)
	if _, err := backend.Publish(context.Background(), manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := backend.SetProtected(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	if protected, err := backend.Protected(context.Background(), id); err != nil || !protected {
		t.Fatalf("maximum-length marker unreadable: %t %v", protected, err)
	}
	if err := backend.SetProtected(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
}

// Regression for BUG-0751: a marker the unprotect cannot judge is put back, not deleted.
func TestSFTPUnprotectKeepsUnreadableMarker(t *testing.T) {
	client := testSFTPClient(t)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	name := filepath.Join(root, "point-1", ".nex-protected")
	if err := os.Mkdir(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	oversized := []byte("protected\n" + strings.Repeat("x", 200))
	if err := os.WriteFile(name, oversized, 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.removeMarkerIfUnchanged(client, name, []byte("protected\nold\n")); err == nil {
		t.Fatal("unreadable marker was treated as unchanged")
	}
	if got, err := os.ReadFile(name); err != nil || !bytes.Equal(got, oversized) {
		t.Fatalf("marker was lost: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(name)); len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

// A crash between rename-aside and restore leaves the only copy of a marker as .rm-*;
// the sweep restores it when the point has no marker.
func TestSFTPSweepRestoresLeftoverMarker(t *testing.T) {
	client := testSFTPClient(t)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	point := filepath.Join(root, "point-1")
	if err := os.Mkdir(point, 0700); err != nil {
		t.Fatal(err)
	}
	marker := []byte("protected\nnonce\n")
	if err := os.WriteFile(filepath.Join(point, ".nex-protected.rm-a"), marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.sweepMarkers(client, "point-1"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(point, ".nex-protected")); err != nil || !bytes.Equal(got, marker) {
		t.Fatalf("marker not restored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(point, ".nex-protected.rm-b"), marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(point, ".nex-protected.rm-c"), []byte("protected\nother\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.sweepMarkers(client, "point-1"); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(point, ".nex-protected*")); len(left) != 2 {
		t.Fatalf("leftovers: %v", left)
	}
}

func testSFTPReviewPoint(t *testing.T) (sftpBackupStorage, *sftp.Client, protocol.BackupManifest, map[string]string, []byte) {
	t.Helper()
	client := testSFTPClient(t)
	stubSFTPSync(t, false)
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID,
		SFTPSource: &protocol.BackupSFTPSource{Root: t.TempDir()}}}
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return storage, client, manifest, files, raw
}

func TestSFTPResumedArtifactRetriesFailedSync(t *testing.T) {
	storage, client, manifest, files, raw := testSFTPReviewPoint(t)
	target := filepath.Join(storage.point("point-1"), manifest.Disks[0].Filename)
	syncFailure := errors.New("injected artifact sync failure")
	syncSFTPFile = func(file *sftp.File) error {
		if file.Name() == target {
			return syncFailure
		}
		return nil
	}
	for attempt := range 2 {
		if _, err := storage.publishOnClient(context.Background(), client, manifest, files, raw); !errors.Is(err, syncFailure) {
			t.Fatalf("attempt %d accepted an artifact without durable sync: %v", attempt, err)
		}
		if _, err := os.Lstat(filepath.Join(storage.point("point-1"), sftpManifestName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed artifact sync published a manifest:", err)
		}
		if got, err := os.ReadFile(target); err != nil || string(got) != "disk" {
			t.Fatalf("exact reusable artifact was lost: %q %v", got, err)
		}
	}
	syncSFTPFile = func(*sftp.File) error { return nil }
	digest, err := storage.publishOnClient(context.Background(), client, manifest, files, raw)
	if err != nil || digest != sha256Hex(raw) {
		t.Fatalf("durable retry failed: %q %v", digest, err)
	}
}

func TestSFTPProtectionRefreshFailuresStayProtected(t *testing.T) {
	for _, outcome := range []string{"sync-failure", "empty", "partial", "mixed", "malformed"} {
		t.Run(outcome, func(t *testing.T) {
			storage, client, manifest, files, raw := testSFTPReviewPoint(t)
			publishTestSFTPPoint(t, storage, client, manifest, files, raw)
			if err := storage.setProtectedOnClient(client, "point-1", true); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(storage.point("point-1"), ".nex-protected")
			stale, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(marker)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("injected refresh failure")
			syncSFTPFile = func(file *sftp.File) error {
				if file.Name() != marker {
					return nil
				}
				// Simulate the bytes a failed rewrite/crash can leave on disk.
				leftovers := map[string][]byte{"empty": {}, "partial": []byte("prot"),
					"mixed": []byte("protected\nold-and-new\n"), "malformed": []byte("invalid")}
				if leftover, ok := leftovers[outcome]; ok {
					if err := os.WriteFile(marker, leftover, 0600); err != nil {
						t.Fatal(err)
					}
				}
				return failure
			}
			if err := storage.setProtectedOnClient(client, "point-1", true); !errors.Is(err, failure) {
				t.Fatal("refresh did not report failure:", err)
			}
			after, err := os.Stat(marker)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("refresh lost the durable protection entry:", err)
			}
			protected, err := storage.protectedOnClient(client, "point-1")
			if err == nil && !protected {
				t.Fatal("failed refresh classified point as unprotected")
			}
			points, err := storage.scanOnClient(context.Background(), client)
			if err == nil && (len(points) != 1 || !points[0].Protected) {
				t.Fatalf("rebuild imported failed protection as pruneable: %#v", points)
			}
			if err := storage.deleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
				t.Fatal("prune did not refuse failed protection:", err)
			}
			syncSFTPFile = func(*sftp.File) error { return nil }
			if err := storage.removeMarkerIfUnchanged(client, marker, stale); !errors.Is(err, ErrBackupConflict) {
				t.Fatal("stale unprotect accepted rewritten marker:", err)
			}
			if _, err := os.Lstat(marker); err != nil {
				t.Fatal("stale unprotect lost failed-refresh protection:", err)
			}
		})
	}
}

func TestSFTPResumedDeleteRejectsPointSymlink(t *testing.T) {
	storage, client, manifest, files, raw := testSFTPReviewPoint(t)
	publishTestSFTPPoint(t, storage, client, manifest, files, raw)
	record, err := json.Marshal(sftpDeleteRecord{Manifest: raw, Digest: sha256Hex(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.publishDeleteRecord(client, "point-1", record); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Rename(storage.point("point-1"), outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, storage.point("point-1")); err != nil {
		t.Fatal(err)
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("resumed delete followed a point symlink:", err)
	}
	for name, want := range map[string][]byte{sftpManifestName: raw, manifest.Disks[0].Filename: []byte("disk")} {
		if got, err := os.ReadFile(filepath.Join(outside, name)); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("unrelated target %s changed: %q %v", name, got, err)
		}
	}
	if got, err := os.ReadFile(storage.deleteRecordName("point-1")); err != nil || !bytes.Equal(got, record) {
		t.Fatalf("resumable delete record changed: %q %v", got, err)
	}
}

func TestSFTPFailedOwnerClaimReleasesOnlyItsMarker(t *testing.T) {
	for _, failurePoint := range []string{"file-sync", "directory-sync", "foreign-owner"} {
		t.Run(failurePoint, func(t *testing.T) {
			stubSFTPSync(t, true)
			oldSupported := sftpSyncSupported
			sftpSyncSupported = func(*sftp.Client) bool { return true }
			t.Cleanup(func() { sftpSyncSupported = oldSupported })
			hostKey := testSSHSigner(t)
			port := startTestSFTPServer(t, hostKey)
			root := t.TempDir()
			ownerName := filepath.Join(root, "owner.json")
			foreign := []byte(`{"repository":"foreign-repo","pool":"foreign-pool"}`)
			cred := protocol.StorageCredentials{Username: "backup", SSHPrivateKey: testSSHPrivateKey(t)}
			source := &protocol.BackupSFTPSource{Host: "127.0.0.1", Port: port, Root: root, HostKeySHA256: ssh.FingerprintSHA256(hostKey.PublicKey())}
			repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Primary", Kind: protocol.BackupRepoKindSFTP, SFTPSource: source}
			failure := errors.New("injected owner sync failure")
			syncSFTPFile = func(file *sftp.File) error {
				if file.Name() == ownerName && failurePoint != "directory-sync" {
					if failurePoint == "foreign-owner" {
						if err := os.WriteFile(ownerName, foreign, 0600); err != nil {
							t.Fatal(err)
						}
					}
					return failure
				}
				return nil
			}
			failedDir := false
			syncSFTPDir = func(_ *sftp.Client, dir string) error {
				if dir == root && failurePoint == "directory-sync" && !failedDir {
					failedDir = true
					return failure
				}
				return nil
			}
			if _, err := prepareSFTPBackupRepository(context.Background(), repo, t.TempDir(), cred); !errors.Is(err, failure) {
				t.Fatal("failed owner sync was not reported:", err)
			}
			if failurePoint == "foreign-owner" {
				if got, err := os.ReadFile(ownerName); err != nil || !bytes.Equal(got, foreign) {
					t.Fatalf("registration cleanup removed foreign ownership: %q %v", got, err)
				}
				return
			}
			if _, err := os.Lstat(ownerName); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("refused registration kept owner claim:", err)
			}
			syncSFTPFile = func(*sftp.File) error { return nil }
			repo.ID = "repo-2"
			storage := sftpBackupStorage{repo: repo, credentials: cred, stateDir: t.TempDir()}
			client, closeAll, err := storage.connect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer closeAll()
			// The mock server cannot prove non-overwriting rename admission.
			// Exercise the new-ID claim that the refused registration blocked.
			if _, err := storage.claimOwner(client); err != nil {
				t.Fatal("corrected registration could not claim root:", err)
			}
		})
	}
}

// stallSFTPDirSync blocks the first directory sync of dir after skip earlier
// ones, which is how a stalled server holds a removal step.
func stallSFTPDirSync(t *testing.T, dir string, skip int32) (entered, release chan struct{}) {
	t.Helper()
	entered, release = make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	syncSFTPDir = func(_ *sftp.Client, name string) error {
		if name == dir && calls.Add(1) == skip+1 {
			close(entered)
			<-release
		}
		return nil
	}
	return entered, release
}

func requireSFTPMembershipFree(t *testing.T, s *Store, entered, release chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the operation never reached the stalled step")
	}
	member := make(chan struct{})
	go func() { Membership(s); close(member) }()
	select {
	case <-member:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Membership blocked on poolPeerMu during SFTP I/O")
	}
}

func TestSFTPDeleteRemovesFilesWithoutPoolPeerLock(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	publishTestSFTPPoint(t, storage, client, manifest, files, raw)
	entered, release := stallSFTPDirSync(t, filepath.Join(root, "point-1"), 0)
	done := make(chan error, 1)
	go func() { done <- storage.deleteOnClient(context.Background(), client, "point-1") }()
	requireSFTPMembershipFree(t, store, entered, release)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSFTPDeleteStopsRemovingAfterAuthorityLoss(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	publishTestSFTPPoint(t, storage, client, manifest, files, raw)
	entered, release := stallSFTPDirSync(t, filepath.Join(root, "point-1"), 0)
	done := make(chan error, 1)
	go func() { done <- storage.deleteOnClient(context.Background(), client, "point-1") }()
	requireSFTPMembershipFree(t, store, entered, release)
	restrictPoolAuthority(t, store)
	close(release)
	if err := <-done; err == nil {
		t.Fatal("delete finished after authority was lost")
	}
	if _, err := os.Lstat(filepath.Join(root, "point-1")); err != nil {
		t.Fatalf("point directory removed after authority was lost: %v", err)
	}
}

func TestSFTPAbortRemovalRunsWithoutPoolPeerLock(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	manifest, _ := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.publishOnClient(context.Background(), client, manifest, map[string]string{"disk.qcow2": filepath.Join(root, "missing-source")}, raw); err == nil {
		t.Fatal("missing source unexpectedly published")
	}
	if err := os.WriteFile(filepath.Join(root, "point-1", "disk.qcow2"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	// The manifest claim syncs the point directory under the lock; the removals that follow do not.
	entered, release := stallSFTPDirSync(t, filepath.Join(root, "point-1"), 1)
	done := make(chan error, 1)
	go func() { done <- storage.abortIncompleteOnClient(context.Background(), client, "point-1") }()
	requireSFTPMembershipFree(t, store, entered, release)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// stallingConn blocks the next write once armed, holding the request that follows.
type stallingConn struct {
	net.Conn
	armed            atomic.Bool
	entered, release chan struct{}
}

func (c *stallingConn) Write(p []byte) (int, error) {
	if c.armed.CompareAndSwap(true, false) {
		close(c.entered)
		<-c.release
	}
	return c.Conn.Write(p)
}

func TestSFTPSetProtectedReadsOutsidePoolPeerLock(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	server, err := sftp.NewServer(serverConn)
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	conn := &stallingConn{Conn: clientConn, entered: make(chan struct{}), release: make(chan struct{})}
	client, err := sftp.NewClientPipe(conn, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	stubSFTPSync(t, true)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	publishTestSFTPPoint(t, storage, client, manifest, files, raw)
	conn.armed.Store(true)
	done := make(chan error, 1)
	go func() { done <- storage.setProtectedOnClient(client, "point-1", true) }()
	requireSFTPMembershipFree(t, store, conn.entered, conn.release)
	close(conn.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if protected, err := storage.protectedOnClient(client, "point-1"); err != nil || !protected {
		t.Fatalf("point not protected: %v %v", protected, err)
	}
}

func TestSFTPDeleteIgnoresAnotherPointsDamagedMarker(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	publishTestSFTPPoint(t, storage, client, manifest, files, raw)
	// Another point's prune record whose completion marker names a different digest.
	other, _ := testSFTPPoint(t, store.State.PoolID, "point-2")
	otherRaw, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(sftpDeleteRecord{Manifest: otherRaw, Digest: sha256Hex(otherRaw)})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.publishDeleteRecord(client, "point-2", record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nex-deleted-point-2"), []byte(strings.Repeat("f", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := storage.deleteOnClient(context.Background(), client, "point-1"); err != nil {
		t.Fatalf("another point's damaged marker blocked the delete: %v", err)
	}
}

func TestSFTPSweepDeleteMarkersVerifiesRepositoryAndAuthority(t *testing.T) {
	root := t.TempDir()
	store, repo := testSFTPRepository(t, root)
	backend, err := openBackupRepositoryStorage(store, repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	digest, err := backend.Publish(ctx, manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupCatalogEntry(store, protocol.BackupRecoveryPoint{ID: manifest.RecoveryPointID, RepositoryID: manifest.RepositoryID,
		PoolID: manifest.PoolID, SourceVMID: manifest.SourceVMID, CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency, ManifestSHA256: digest}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Delete(ctx, "point-1"); err != nil {
		t.Fatal(err)
	}
	// The last prune removed the catalog entry, so its markers may now retire.
	if err := os.Remove(filepath.Join(poolBackupCatalogPath(store), "point-1.json")); err != nil {
		t.Fatal(err)
	}
	markers := []string{".nex-delete-point-1", ".nex-deleted-point-1"}
	present := func() bool {
		for _, name := range markers {
			if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
				return false
			}
		}
		return true
	}
	if !present() {
		t.Fatal("prune left no markers to retire")
	}
	sweeper := backend.(interface{ SweepDeleteMarkers(context.Context) })
	owner, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "owner.json"), []byte(`{"repository":"other","pool":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	sweeper.SweepDeleteMarkers(ctx)
	if !present() {
		t.Fatal("markers retired from a repository that no longer verifies")
	}
	if err := os.WriteFile(filepath.Join(root, "owner.json"), owner, 0600); err != nil {
		t.Fatal(err)
	}
	restrictPoolAuthority(t, store)
	sweeper.SweepDeleteMarkers(ctx)
	if !present() {
		t.Fatal("a host without authority retired markers")
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(store)
	if err == nil {
		state.Restricted = nil
		err = savePoolPeer(store, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	sweeper.SweepDeleteMarkers(ctx)
	if present() {
		t.Fatal("markers were not retired once verification and authority held")
	}
}

func TestSFTPScanLeavesSetAsideMarkerAlone(t *testing.T) {
	client := testSFTPClient(t)
	stubSFTPSync(t, true)
	root := t.TempDir()
	store := seedBackupMaster(t)
	storage := sftpBackupStorage{s: store, repo: protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	publishTestSFTPPoint(t, storage, client, manifest, files, raw)
	leftover := filepath.Join(root, "point-1", ".nex-protected.rm-crashed")
	if err := os.WriteFile(leftover, []byte("protected\npoint-1\nnonce\n"), 0600); err != nil {
		t.Fatal(err)
	}
	points, err := storage.scanOnClient(context.Background(), client)
	if err != nil || len(points) != 1 || !points[0].Protected {
		t.Fatalf("set-aside marker did not keep the point protected: %+v %v", points, err)
	}
	if _, err := os.Lstat(leftover); err != nil {
		t.Fatalf("scan moved the leftover: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "point-1", ".nex-protected")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scan restored the marker: %v", err)
	}
	if err := storage.setProtectedOnClient(client, "point-1", false); err != nil {
		t.Fatal(err)
	}
	if protected, err := storage.protectedOnClient(client, "point-1"); err != nil || protected {
		t.Fatalf("unprotect did not settle the leftover: %v %v", protected, err)
	}
}

func TestSFTPVerifyArtifactsHashesStoredContent(t *testing.T) {
	root := t.TempDir()
	store, repo := testSFTPRepository(t, root)
	backend, err := openBackupRepositoryStorage(store, repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	manifest, files := testSFTPPoint(t, store.State.PoolID, "point-1")
	if _, err := backend.Publish(ctx, manifest, files); err != nil {
		t.Fatal(err)
	}
	if err := backend.VerifyArtifacts(ctx, "point-1", manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "point-1", "disk.qcow2"), []byte("dusk"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := backend.VerifyArtifacts(ctx, "point-1", manifest); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("same-size corruption not detected: %v", err)
	}
}

// A server that offers only a host key type Core never negotiates has not
// presented a different key, so the error must not say the pin differs.
func TestSFTPConnectReportsUnsupportedHostKeyTypes(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := ssh.NewSignerWithAlgorithms(signer.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSA})
	if err != nil {
		t.Fatal(err)
	}
	port := startTestSFTPServer(t, legacy)
	storage := sftpBackupStorage{repo: protocol.BackupRepository{SFTPSource: &protocol.BackupSFTPSource{
		Host: "127.0.0.1", Port: port, HostKeySHA256: ssh.FingerprintSHA256(legacy.PublicKey()),
	}}, credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: testSSHPrivateKey(t)}}
	_, closeAll, err := storage.connect(context.Background())
	if err == nil {
		closeAll()
		t.Fatal("a server with only an unsupported host key type connected")
	}
	if strings.Contains(err.Error(), "differs") || !strings.Contains(err.Error(), "host key type") {
		t.Fatalf("unsupported host key type misreported: %v", err)
	}
}
