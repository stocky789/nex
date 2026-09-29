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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// backupRepositoryStorage is the Core boundary between backup operations and a
// repository's publication, read, protection and deletion semantics.
type backupRepositoryStorage interface {
	Verify(context.Context) error
	Probe(context.Context) error
	CaptureWorkDir(operationID string, online bool) string
	// StatPoint checks the manifest, markers and artifact sizes without reading
	// artifact content. A manifest is published only after its artifacts verify.
	StatPoint(context.Context, string) (protocol.BackupManifest, string, error)
	Publish(context.Context, protocol.BackupManifest, map[string]string) (string, error)
	CopyArtifacts(context.Context, string, string, protocol.BackupManifest) error
	Scan(context.Context) ([]protocol.BackupRecoveryPoint, error)
	Entries(context.Context) ([]string, error)
	Protected(context.Context, string) (bool, error)
	SetProtected(context.Context, string, bool) error
	Delete(context.Context, string) error
}

type filesystemBackupStorage struct {
	repo     protocol.BackupRepository
	stateDir string
	s        *Store
}

var publishFilesystemBackupPoint = vmhost.PublishBackupRecoveryPoint

type filesystemDeleteIntent struct {
	Manifest []byte `json:"manifest"`
	Digest   string `json:"digest"`
}

func filesystemAbortMarker(id string) string { return "nex-backup-abort:" + id + "\n" }

func openBackupRepositoryStorage(s *Store, repo protocol.BackupRepository) (backupRepositoryStorage, error) {
	if repo.Kind == protocol.BackupRepoKindS3 {
		return newS3BackupRepositoryStorage(s, repo)
	}
	if repo.Kind == protocol.BackupRepoKindSFTP {
		return newSFTPBackupStorage(s, repo)
	}
	if repo.MountPath == "" {
		return nil, errors.New("backup repository backend is unavailable")
	}
	switch repo.Kind {
	case protocol.BackupRepoKindNFS:
		if repo.Identity == nil {
			return nil, errors.New("backup repository NFS identity is unavailable")
		}
	case protocol.BackupRepoKindSMB3:
		if repo.SMBIdentity == nil || repo.SMBIdentity.MarkerID == "" {
			return nil, errors.New("backup repository SMB identity is unavailable")
		}
	default:
		return nil, errors.New("backup repository backend is unavailable")
	}
	return filesystemBackupStorage{repo: repo, stateDir: s.dir, s: s}, nil
}

// Verify gates writes. SMB reads use the already authenticated mount, but writes still need
// the repository's current credential.
func (b filesystemBackupStorage) Verify(ctx context.Context) error {
	if b.repo.Kind == protocol.BackupRepoKindSMB3 {
		if _, err := ResolveBackupCredential(b.s, b.repo); err != nil {
			return err
		}
	}
	return verifyBackupRepository(ctx, b.repo)
}

func (b filesystemBackupStorage) verifyRead(ctx context.Context) error {
	return verifyBackupRepositoryRead(ctx, b.repo)
}

func (b filesystemBackupStorage) Probe(ctx context.Context) (probeErr error) {
	if err := b.Verify(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp(b.repo.MountPath, ".nex-health-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		if err := os.Remove(f.Name()); err != nil && probeErr == nil {
			probeErr = err
		}
	}()
	const payload = "nex repository health\n"
	if _, err = io.WriteString(f, payload); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(len(payload)+1)))
	if err != nil || string(data) != payload {
		return errors.New("backup repository probe read failed")
	}
	entries, err := os.ReadDir(b.repo.MountPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(f.Name()) {
			return ctx.Err()
		}
	}
	return errors.New("backup repository probe is missing from listing")
}

func (b filesystemBackupStorage) CaptureWorkDir(operationID string, online bool) string {
	if online {
		return filepath.Join(b.repo.MountPath, ".nex-backup-work", operationID)
	}
	return filepath.Join(b.stateDir, "backup-staging", operationID)
}

func (b filesystemBackupStorage) StatPoint(ctx context.Context, pointID string) (protocol.BackupManifest, string, error) {
	if !protocol.ValidSnapshotOperationID(pointID) {
		return protocol.BackupManifest{}, "", ErrBackupInvalid
	}
	if err := b.verifyRead(ctx); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	pointDir := filepath.Join(b.repo.MountPath, pointID)
	info, err := os.Lstat(pointDir)
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if !info.IsDir() {
		if fenced, _ := filesystemAbortFence(pointDir, pointID); fenced {
			return protocol.BackupManifest{}, "", os.ErrNotExist
		}
		return protocol.BackupManifest{}, "", ErrBackupConflict
	}
	if _, err := os.Lstat(b.deleteIntentPath(pointID)); err == nil {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return protocol.BackupManifest{}, "", err
	}
	manifest, digest, err := vmhost.ReadBackupManifest(pointDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return protocol.BackupManifest{}, "", errors.New("backup recovery point is incomplete")
		}
		return protocol.BackupManifest{}, "", err
	}
	if manifest.RecoveryPointID != pointID || manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	}
	if err := vmhost.CheckPublishedBackupArtifactSizes(pointDir, manifest); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	return manifest, digest, nil
}

func (b filesystemBackupStorage) Publish(ctx context.Context, manifest protocol.BackupManifest, stageFiles map[string]string) (string, error) {
	if err := b.Verify(ctx); err != nil {
		return "", err
	}
	return publishFilesystemBackupPoint(b.repo.MountPath, manifest, stageFiles)
}

func (b filesystemBackupStorage) AbortIncomplete(ctx context.Context, pointID string) error {
	if !protocol.ValidSnapshotOperationID(pointID) {
		return ErrBackupInvalid
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(b.s)
	if err != nil {
		return err
	}
	if err := poolAuthorityWritable(state); err != nil {
		return err
	}
	if err := b.Verify(ctx); err != nil {
		return err
	}
	if _, err := getCatalogPoint(b.s, b.repo.ID, pointID); err == nil {
		return ErrBackupConflict
	} else if !errors.Is(err, ErrBackupNotFound) {
		return err
	}
	final := filepath.Join(b.repo.MountPath, pointID)
	f, err := os.OpenFile(final, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		var fd int
		fd, err = syscall.Open(final, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err == nil {
			f = os.NewFile(uintptr(fd), final)
		}
	}
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(final)
	opened, openErr := f.Stat()
	marker := filesystemAbortMarker(pointID)
	if statErr != nil || openErr != nil || !info.Mode().IsRegular() || info.Size() > int64(len(marker)) || !os.SameFile(info, opened) {
		_ = f.Close()
		return ErrBackupConflict
	}
	prior := make([]byte, info.Size())
	if _, err := f.ReadAt(prior, 0); err != nil || !bytes.HasPrefix([]byte(marker), prior) {
		_ = f.Close()
		return ErrBackupConflict
	}
	if _, err := f.WriteAt([]byte(marker[len(prior):]), int64(len(prior))); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// With the fence in place a delayed rename of the staging directory fails, so a
	// staging copy left by a failed publish can go.
	if err := os.RemoveAll(filepath.Join(b.repo.MountPath, ".staging-"+pointID)); err != nil {
		return err
	}
	dir, err := os.Open(b.repo.MountPath)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (b filesystemBackupStorage) CopyArtifacts(ctx context.Context, pointID, localDir string, manifest protocol.BackupManifest) error {
	if err := b.verifyRead(ctx); err != nil {
		return err
	}
	return vmhost.CopyBackupArtifactsLocal(ctx, filepath.Join(b.repo.MountPath, pointID), localDir, manifest)
}

func (b filesystemBackupStorage) Scan(ctx context.Context) ([]protocol.BackupRecoveryPoint, error) {
	if err := b.verifyRead(ctx); err != nil {
		return nil, err
	}
	pending, err := b.pendingDeletes()
	if err != nil {
		return nil, err
	}
	points, err := vmhost.ScanBackupRepositoryPoints(b.repo.MountPath, b.repo.ID, b.repo.PoolID)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(points, func(point protocol.BackupRecoveryPoint) bool {
		_, deleting := pending[point.ID]
		return deleting
	}), nil
}

func (b filesystemBackupStorage) Entries(ctx context.Context) ([]string, error) {
	if err := b.verifyRead(ctx); err != nil {
		return nil, err
	}
	pending, err := b.pendingDeletes()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(b.repo.MountPath)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, deleting := pending[entry.Name()]; deleting {
			continue
		}
		if protocol.ValidSnapshotOperationID(entry.Name()) {
			fenced, err := filesystemAbortFence(filepath.Join(b.repo.MountPath, entry.Name()), entry.Name())
			if err != nil {
				return nil, err
			}
			if fenced {
				continue
			}
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func filesystemAbortFence(path, id string) (bool, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ELOOP) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	marker := filesystemAbortMarker(id)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(marker)) {
		return false, err
	}
	data := make([]byte, len(marker))
	if _, err := io.ReadFull(f, data); err != nil {
		return false, err
	}
	return string(data) == marker, nil
}

// maxBackupProtectMarker bounds a marker read: "protected\n" + a 64-character
// point ID + "\n" + a 26-character nonce + "\n" is 102 bytes.
const maxBackupProtectMarker = 128

// newBackupProtectMarker is unique per protect, so a conditional removal or
// replacement can tell it apart from a newer marker for the same point.
func newBackupProtectMarker(pointID string) []byte {
	return []byte("protected\n" + pointID + "\n" + rand.Text() + "\n")
}

func (b filesystemBackupStorage) Protected(ctx context.Context, pointID string) (bool, error) {
	if !protocol.ValidSnapshotOperationID(pointID) {
		return false, ErrBackupInvalid
	}
	if err := b.verifyRead(ctx); err != nil {
		return false, err
	}
	pointDir := filepath.Join(b.repo.MountPath, pointID)
	if vmhost.ReadBackupProtectMarker(pointDir) {
		return true, nil
	}
	_, err := os.Stat(filepath.Join(pointDir, ".nex-protected"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (b filesystemBackupStorage) SetProtected(ctx context.Context, pointID string, protected bool) error {
	if _, _, err := b.StatPoint(ctx, pointID); err != nil {
		return err
	}
	return writeImportedBackupProtectMarker(filepath.Join(b.repo.MountPath, pointID), protected)
}

func (b filesystemBackupStorage) Delete(ctx context.Context, pointID string) error {
	if !protocol.ValidSnapshotOperationID(pointID) {
		return ErrBackupInvalid
	}
	if err := b.Verify(ctx); err != nil {
		return err
	}
	intent, pins, err := b.recordDeleteIntent(ctx, pointID)
	if err != nil {
		return err
	}
	// Removal runs outside poolPeerMu: a recorded intent is finished on any retry.
	b.sweepDeleteIntents(pins)
	return b.finishDelete(pointID, intent)
}

// recordDeleteIntent checks authority, pins and protection under poolPeerMu and
// durably records what the delete will remove.
func (b filesystemBackupStorage) recordDeleteIntent(ctx context.Context, pointID string) (filesystemDeleteIntent, map[string]bool, error) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(b.s)
	if err != nil {
		return filesystemDeleteIntent{}, nil, err
	}
	if err := poolAuthorityWritable(state); err != nil {
		return filesystemDeleteIntent{}, nil, err
	}
	pins, err := ActiveBackupRestorePins(b.s)
	if err != nil {
		return filesystemDeleteIntent{}, nil, err
	}
	if pins[pointID] {
		return filesystemDeleteIntent{}, nil, ErrBackupConflict
	}
	catalog, err := getCatalogPoint(b.s, b.repo.ID, pointID)
	if err != nil || catalog.PoolID != b.repo.PoolID || catalog.Protected {
		return filesystemDeleteIntent{}, nil, ErrBackupConflict
	}
	protected, err := b.Protected(ctx, pointID)
	if err != nil {
		return filesystemDeleteIntent{}, nil, err
	}
	if protected {
		return filesystemDeleteIntent{}, nil, ErrBackupConflict
	}
	intent, exists, err := b.readDeleteIntent(pointID)
	if err != nil {
		return filesystemDeleteIntent{}, nil, err
	}
	if exists {
		if intent.Digest != catalog.ManifestSHA256 {
			return filesystemDeleteIntent{}, nil, ErrBackupConflict
		}
		return intent, pins, nil
	}
	pointDir := filepath.Join(b.repo.MountPath, pointID)
	manifest, digest, err := b.StatPoint(ctx, pointID)
	if err != nil || digest != catalog.ManifestSHA256 {
		return filesystemDeleteIntent{}, nil, ErrBackupConflict
	}
	root, _, err := openFilesystemDeleteDir(pointDir, manifest)
	if err != nil {
		return filesystemDeleteIntent{}, nil, err
	}
	if root == nil {
		return filesystemDeleteIntent{}, nil, ErrBackupConflict
	}
	root.Close()
	manifestRaw, err := os.ReadFile(filepath.Join(pointDir, "manifest.json"))
	if err != nil {
		return filesystemDeleteIntent{}, nil, err
	}
	sum := sha256.Sum256(manifestRaw)
	if hex.EncodeToString(sum[:]) != digest {
		return filesystemDeleteIntent{}, nil, ErrBackupConflict
	}
	intent = filesystemDeleteIntent{Manifest: manifestRaw, Digest: digest}
	raw, err := json.Marshal(intent)
	if err != nil {
		return filesystemDeleteIntent{}, nil, err
	}
	if err := vmhost.AtomicWrite(b.deleteIntentPath(pointID), raw, 0600); err != nil {
		return filesystemDeleteIntent{}, nil, err
	}
	return intent, pins, nil
}

// finishDelete removes a point recorded by its delete intent. It is safe to repeat.
func (b filesystemBackupStorage) finishDelete(pointID string, intent filesystemDeleteIntent) error {
	var manifest protocol.BackupManifest
	if err := json.Unmarshal(intent.Manifest, &manifest); err != nil {
		return ErrBackupConflict
	}
	pointDir := filepath.Join(b.repo.MountPath, pointID)
	root, litter, err := openFilesystemDeleteDir(pointDir, manifest)
	if err != nil {
		return err
	}
	if root != nil {
		defer root.Close()
		current, err := root.Open("manifest.json")
		if err == nil {
			info, statErr := current.Stat()
			if statErr != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
				current.Close()
				return ErrBackupConflict
			}
			raw, readErr := io.ReadAll(io.LimitReader(current, 1<<20+1))
			current.Close()
			if readErr != nil || !bytes.Equal(raw, intent.Manifest) {
				return ErrBackupConflict
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, name := range append(filesystemDeleteNames(manifest), litter...) {
			if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := syncBackupDirectoryRoot(root); err != nil {
			return err
		}
	}
	if err := os.Remove(pointDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncBackupDirectory(b.repo.MountPath)
}

// sweepDeleteIntents finishes deletions whose catalog entries are already gone and drops their
// intents, so intents do not accumulate and a lost catalog entry cannot strand a half-deleted
// point. Failures are left for the next delete.
func (b filesystemBackupStorage) sweepDeleteIntents(pins map[string]bool) {
	pending, err := b.pendingDeletes()
	if err != nil {
		return
	}
	for id, intent := range pending {
		if pins[id] {
			continue
		}
		if _, err := getCatalogPoint(b.s, b.repo.ID, id); !errors.Is(err, ErrBackupNotFound) {
			continue
		}
		if b.finishDelete(id, intent) == nil {
			_ = os.Remove(b.deleteIntentPath(id))
		}
	}
}

// pendingDeletes returns the recorded delete intents; their points must not be read or listed.
func (b filesystemBackupStorage) pendingDeletes() (map[string]filesystemDeleteIntent, error) {
	entries, err := os.ReadDir(b.repo.MountPath)
	if err != nil {
		return nil, err
	}
	pending := map[string]filesystemDeleteIntent{}
	for _, entry := range entries {
		id, ok := strings.CutPrefix(entry.Name(), ".nex-delete-")
		if !ok {
			continue
		}
		if !protocol.ValidSnapshotOperationID(id) {
			return nil, ErrBackupConflict
		}
		intent, exists, err := b.readDeleteIntent(id)
		if err != nil {
			return nil, err
		}
		if exists {
			pending[id] = intent
		}
	}
	return pending, nil
}

func (b filesystemBackupStorage) deleteIntentPath(id string) string {
	return filepath.Join(b.repo.MountPath, ".nex-delete-"+id)
}

func (b filesystemBackupStorage) readDeleteIntent(id string) (filesystemDeleteIntent, bool, error) {
	var intent filesystemDeleteIntent
	path := b.deleteIntentPath(id)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return intent, false, nil
	}
	if err != nil {
		return intent, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return intent, false, ErrBackupConflict
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return intent, false, err
	}
	var manifest protocol.BackupManifest
	if json.Unmarshal(raw, &intent) != nil || json.Unmarshal(intent.Manifest, &manifest) != nil ||
		manifest.Valid() != nil || manifest.RecoveryPointID != id ||
		manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return intent, false, ErrBackupConflict
	}
	digest := sha256.Sum256(intent.Manifest)
	if intent.Digest != hex.EncodeToString(digest[:]) {
		return intent, false, ErrBackupConflict
	}
	return intent, true, nil
}

func filesystemDeleteNames(manifest protocol.BackupManifest) []string {
	names := []string{"manifest.json"}
	for _, disk := range manifest.Disks {
		names = append(names, disk.Filename)
	}
	for _, fw := range manifest.Firmware {
		names = append(names, fw.Filename)
	}
	return names
}

// openFilesystemDeleteDir opens a point for deletion and returns the file-manager and
// interrupted-write litter it may also remove. Any other entry refuses the delete by name.
func openFilesystemDeleteDir(pointDir string, manifest protocol.BackupManifest) (*os.Root, []string, error) {
	info, err := os.Lstat(pointDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, ErrBackupConflict
	}
	root, err := os.OpenRoot(pointDir)
	if err != nil {
		return nil, nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, nil, ErrBackupConflict
	}
	dir, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	allowed := map[string]bool{}
	for _, name := range filesystemDeleteNames(manifest) {
		allowed[name] = true
	}
	var litter []string
	for _, entry := range entries {
		name := entry.Name()
		litterName := name == ".DS_Store" || name == "Thumbs.db" || name == "desktop.ini" ||
			strings.HasPrefix(name, "._") || strings.HasPrefix(name, ".write-")
		if !entry.Type().IsRegular() || !allowed[name] && !litterName {
			root.Close()
			return nil, nil, fmt.Errorf("%w: recovery point %s contains unexpected entry %q", ErrBackupConflict, filepath.Base(pointDir), name)
		}
		if litterName {
			litter = append(litter, name)
		}
	}
	return root, litter, nil
}

func syncBackupDirectory(path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	return syncBackupDirectoryRoot(root)
}

func syncBackupDirectoryRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func backupRepositoryEntryCouldBePoint(name string) bool {
	return !strings.HasPrefix(name, ".") && protocol.ValidSnapshotOperationID(name)
}
