// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
	"strings"
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
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
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
	}}, credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: string(pem.EncodeToMemory(block))}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err = storage.connect(ctx)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("stalled SSH handshake ignored cancellation: %v", err)
	}
}

func TestSFTPManifestRetryRequiresExactPayload(t *testing.T) {
	manifest := protocol.BackupManifest{RecoveryPointID: "point-1", RepositoryID: "repo-1", PoolID: "pool-1"}
	if !sameSFTPManifest(manifest, manifest) {
		t.Fatal("same manifest rejected")
	}
	changed := manifest
	changed.SourceVMName = "changed"
	if sameSFTPManifest(manifest, changed) {
		t.Fatal("changed manifest accepted")
	}
	changed = manifest
	changed.Complete = true
	if sameSFTPManifest(manifest, changed) {
		t.Fatal("changed completion state accepted")
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

func testSFTPClient(t *testing.T) *sftp.Client {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	server, err := sftp.NewServer(serverConn)
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

func TestSFTPPublishReportsReadOnlyRoot(t *testing.T) {
	client := testSFTPClient(t)
	oldSync := syncSFTPDir
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	t.Cleanup(func() { syncSFTPDir = oldSync })
	root := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	storage := sftpBackupStorage{repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	if err := storage.claimOwner(client); err == nil || errors.Is(err, ErrBackupConflict) || !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatalf("read-only owner claim returned %v instead of a permission failure", err)
	}
	manifest := protocol.BackupManifest{RecoveryPointID: "point-1"}
	raw := []byte("{}")
	_, err := storage.publishOnClient(context.Background(), client, manifest, nil, raw)
	if err == nil || errors.Is(err, ErrBackupConflict) || !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatalf("read-only root returned %v instead of a permission failure", err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if err := os.WriteFile(filepath.Join(root, ".nex-intent-point-1"), []byte(hex.EncodeToString(sum[:])), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	_, err = storage.publishOnClient(context.Background(), client, manifest, nil, raw)
	if err == nil || errors.Is(err, ErrBackupConflict) || !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Fatalf("read-only retry returned %v instead of a permission failure", err)
	}
}

func TestSFTPPublishResumesExactIncompletePoint(t *testing.T) {
	client := testSFTPClient(t) // In-process protocol mock; native OpenSSH durability remains untested.
	oldSync := syncSFTPFile
	syncSFTPFile = func(*sftp.File) error { return nil }
	t.Cleanup(func() { syncSFTPFile = oldSync })
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
	oldFileSync := syncSFTPFile
	syncSFTPFile = func(*sftp.File) error { return nil }
	t.Cleanup(func() { syncSFTPFile = oldFileSync })
	oldSync := syncSFTPDir
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	t.Cleanup(func() { syncSFTPDir = oldSync })
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
	oldFileSync := syncSFTPFile
	syncSFTPFile = func(*sftp.File) error { return nil }
	t.Cleanup(func() { syncSFTPFile = oldFileSync })
	oldSync := syncSFTPDir
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	t.Cleanup(func() { syncSFTPDir = oldSync })
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
	oldFileSync, oldDirSync := syncSFTPFile, syncSFTPDir
	syncSFTPFile = func(*sftp.File) error { return nil }
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	t.Cleanup(func() { syncSFTPFile, syncSFTPDir = oldFileSync, oldDirSync })
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
	oldFileSync, oldDirSync := syncSFTPFile, syncSFTPDir
	syncSFTPFile = func(*sftp.File) error { return nil }
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	t.Cleanup(func() { syncSFTPFile, syncSFTPDir = oldFileSync, oldDirSync })
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
	oldFileSync, oldDirSync := syncSFTPFile, syncSFTPDir
	oldSyncSupported := sftpSyncSupported
	syncSFTPFile = func(*sftp.File) error { return nil }
	syncSFTPDir = func(*sftp.Client, string) error { return nil }
	sftpSyncSupported = func(*sftp.Client) bool { return true }
	t.Cleanup(func() { syncSFTPFile, syncSFTPDir, sftpSyncSupported = oldFileSync, oldDirSync, oldSyncSupported })
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientBlock, err := ssh.MarshalPrivateKey(clientKey, "")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	serverConfig.AddHostKey(hostSigner)
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
				go ssh.DiscardRequests(requests)
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

	store := seedBackupMaster(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "owner.json"), []byte(`{"repository":"repo-1","pool":"pool-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := saveBackupCredential(store, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "sftp-reconcile-credential", RegistrationRequestID: "backup-credential:sftp:reconcile-register",
		PoolID: store.State.PoolID, HostID: store.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "backup", SSHPrivateKey: string(pem.EncodeToMemory(clientBlock))}})
	if err != nil {
		t.Fatal(err)
	}
	source := &protocol.BackupSFTPSource{Host: "127.0.0.1", Port: uint16(listener.Addr().(*net.TCPAddr).Port), Root: root, HostKeySHA256: ssh.FingerprintSHA256(hostSigner.PublicKey())}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: store.State.PoolID, Name: "Primary", Kind: protocol.BackupRepoKindSFTP,
		SFTPSource: source, RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: sftpIdentity(source, "backup")}, CredentialRef: receipt.Receipt, Generation: 1}
	if err := savePoolBackupRepositories(store, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: repo}}); err != nil {
		t.Fatal(err)
	}
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
	if _, _, err := backend.ReadPoint(context.Background(), cmd.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete point became readable during abort", err)
	}
	if err := os.WriteFile(filepath.Join(point, sftpManifestName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.ReadPoint(context.Background(), cmd.OperationID); !errors.Is(err, ErrBackupConflict) {
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
	oldSync := syncSFTPFile
	syncSFTPFile = func(*sftp.File) error { return nil }
	t.Cleanup(func() { syncSFTPFile = oldSync })
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
	if err := syncSFTPDirectory(client, root); err == nil {
		t.Fatal("unsupported directory fsync was accepted")
	}
	oldSync := syncSFTPFile
	syncSFTPFile = func(*sftp.File) error { return nil }
	t.Cleanup(func() { syncSFTPFile = oldSync })
	if err := syncSFTPDirectory(client, root); err != nil {
		t.Fatal("directory OPEN/close failed:", err)
	}
}

func TestSFTPOwnerClaimRejectsForeignOwner(t *testing.T) {
	client := testSFTPClient(t) // In-process protocol mock; native account confinement is separate.
	oldSync := syncSFTPFile
	syncSFTPFile = func(*sftp.File) error { return nil }
	t.Cleanup(func() { syncSFTPFile = oldSync })
	root := t.TempDir()
	storage := sftpBackupStorage{repo: protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", SFTPSource: &protocol.BackupSFTPSource{Root: root}}}
	if err := storage.claimOwner(client); err != nil {
		t.Fatal(err)
	}
	if err := storage.claimOwner(client); err != nil {
		t.Fatal("exact claim retry:", err)
	}
	foreign := storage
	foreign.repo.PoolID = "pool-2"
	if err := foreign.claimOwner(client); err == nil {
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
	if err := storage.claimOwner(client); err == nil {
		t.Fatal("adoption claimed a missing SFTP owner")
	}
	if _, err := client.Lstat(filepath.Join(root, "owner.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("adoption left an owner marker: %v", err)
	}
}

func TestSFTPRegistrationRefusesMemberBeforePreparation(t *testing.T) {
	lab := newTransferLab(t, 1)
	member := lab.host("member-1")
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: member.State.PoolID, Name: "Test", Kind: protocol.BackupRepoKindSFTP,
		SFTPSource:    &protocol.BackupSFTPSource{Host: "sftp.example.test", Root: "/backups", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		CredentialRef: "credential-1"}
	if _, err := prepareBackupRepositoryForRegistration(context.Background(), member, repo, "register-1"); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("member reached SFTP preparation: %v", err)
	}
}

func TestSFTPRegistrationPreparationSerializesWithFreeze(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	repo := testPeerSFTPRepository(t, master)
	repo.RemoteIdentity, repo.Generation = nil, 0
	previous := prepareBackupRepositorySFTPNative
	t.Cleanup(func() { prepareBackupRepositorySFTPNative = previous })
	started, release := make(chan struct{}), make(chan struct{})
	prepareBackupRepositorySFTPNative = func(_ context.Context, got protocol.BackupRepository, _ string, _ protocol.StorageCredentials) (protocol.BackupRepository, error) {
		close(started)
		<-release
		got.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)}
		return got, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := prepareBackupRepositoryForRegistration(context.Background(), master, repo, "register-1")
		done <- err
	}()
	<-started
	locked := !poolBackupAuthorityMu.TryLock()
	if !locked {
		poolBackupAuthorityMu.Unlock()
	}
	freezeDone := make(chan error, 1)
	go func() {
		poolBackupAuthorityMu.Lock()
		state, err := loadPoolPeer(master)
		if err == nil {
			state.Transfer = &poolTransferRecord{Frozen: true}
			err = savePoolPeer(master, state)
		}
		poolBackupAuthorityMu.Unlock()
		freezeDone <- err
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("registration preparation failed: %v", err)
	}
	if err := <-freezeDone; err != nil {
		t.Fatalf("freeze failed: %v", err)
	}
	if !locked {
		t.Fatal("transfer freeze lock was not held through SFTP preparation")
	}
	if _, err := prepareBackupRepositoryForRegistration(context.Background(), master, repo, "register-2"); err == nil {
		t.Fatal("frozen master prepared a new SFTP registration")
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
		RemoteIdentity: &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("a", 64)}, CredentialRef: receipt.Receipt, Generation: 1}
}

func TestPeerSFTPPreflightRequiresPrivateReceiptExactIdentityAndGeneration(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	repo := testPeerSFTPRepository(t, master)
	if _, err := preparePeerSFTPRepository(context.Background(), member, repo); err == nil {
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
	prepared, err := preparePeerSFTPRepository(context.Background(), member, repo)
	if err != nil || !preparedBackupRepositoryMatches(repo, prepared) {
		t.Fatalf("member SFTP capability: %v", err)
	}
	wrong := repo
	wrong.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("b", 64)}
	if _, err := preparePeerSFTPRepository(context.Background(), member, wrong); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("wrong identity accepted: %v", err)
	}
	stale := repo
	stale.Generation = 2
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{repo.ID: stale}}); err != nil {
		t.Fatal(err)
	}
	if _, err := preparePeerSFTPRepository(context.Background(), member, repo); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("stale generation accepted: %v", err)
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
		if got.Generation != repo.Generation || stateDir != member.dir || credentials.SSHPrivateKey != "private-example-key" {
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
	oldSync := syncSFTPFile
	syncSFTPFile = func(*sftp.File) error { return nil }
	t.Cleanup(func() { syncSFTPFile = oldSync })
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
	recordRaw, err := json.Marshal(sftpDeleteRecord{Manifest: manifest, Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	stageRecord := filepath.Join(root, ".nex-delete-stage-point-1")
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
	if err := prunePublishedBackupPayloads(member, run.RepositoryID, run.VMID, *run.Retention); !errors.Is(err, ErrBackupUnavailable) {
		t.Fatalf("member entered retention path: %v", err)
	}
	if err := savePoolBackupRun(master, run); err != nil {
		t.Fatal(err)
	}
	if err := SettlePoolBackupRun(master, settled, &point); !errors.Is(err, ErrBackupRepoNotFound) {
		t.Fatalf("master skipped retention path: %v", err)
	}
}
