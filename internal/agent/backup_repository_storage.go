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
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

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
	// VerifyArtifacts streams every artifact of the point from the repository
	// and checks its size and SHA-256 against the manifest. It copies nothing.
	VerifyArtifacts(context.Context, string, protocol.BackupManifest) error
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
	if err := requirePoolAuthorityWritable(b.s); err != nil {
		return err
	}
	// Mount inspection and the publication probe can stall on a hung share, so they
	// run before the authority lock; only the fence write is the commit under it.
	if err := b.Verify(ctx); err != nil {
		return err
	}
	if err := b.writeAbortFence(pointID); err != nil {
		return err
	}
	// With the fence in place a delayed rename of the staging directory fails, so a
	// staging copy left by a failed publish can go.
	if err := os.RemoveAll(filepath.Join(b.repo.MountPath, ".staging-"+pointID)); err != nil {
		return err
	}
	return syncBackupDirectory(b.repo.MountPath)
}

// writeAbortFence commits the abort fence under poolPeerMu once authority and
// the catalog still allow it.
func (b filesystemBackupStorage) writeAbortFence(pointID string) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(b.s)
	if err != nil {
		return err
	}
	if err := poolAuthorityWritable(state); err != nil {
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
		f, err = openNoFollow(final, syscall.O_RDWR|syscall.O_NONBLOCK)
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
	return f.Close()
}

func (b filesystemBackupStorage) CopyArtifacts(ctx context.Context, pointID, localDir string, manifest protocol.BackupManifest) error {
	if err := b.verifyRead(ctx); err != nil {
		return err
	}
	return vmhost.CopyBackupArtifactsLocal(ctx, filepath.Join(b.repo.MountPath, pointID), localDir, manifest)
}

func (b filesystemBackupStorage) VerifyArtifacts(ctx context.Context, pointID string, manifest protocol.BackupManifest) error {
	stored, _, err := b.StatPoint(ctx, pointID)
	if err != nil {
		return err
	}
	if err := requireSameManifest(stored, manifest); err != nil {
		return err
	}
	for _, artifact := range manifestArtifacts(manifest) {
		open := func() (io.ReadCloser, error) {
			f, err := openNoFollow(filepath.Join(b.repo.MountPath, pointID, artifact.Filename), syscall.O_RDONLY|syscall.O_NONBLOCK)
			if err != nil {
				return nil, err
			}
			if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
				f.Close()
				return nil, errors.Join(ErrBackupConflict, err)
			}
			return f, nil
		}
		if err := verifyArtifactStream(ctx, open, artifact.Size, artifact.SHA256); err != nil {
			return err
		}
	}
	return nil
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
	f, err := openNoFollow(path, syscall.O_RDONLY|syscall.O_NONBLOCK)
	if errors.Is(err, syscall.ELOOP) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
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
	if vmhost.ReadBackupProtectMarker(pointDir) || vmhost.BackupProtectMarkerLeftover(pointDir) {
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

// recordDeleteIntent durably records what the delete will remove. The mount is
// read before poolPeerMu is taken, so a hung share cannot hold the pool-wide
// lock; under it only authority, pins and the catalog are re-checked and the
// intent is written.
func (b filesystemBackupStorage) recordDeleteIntent(ctx context.Context, pointID string) (filesystemDeleteIntent, map[string]bool, error) {
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
	if !exists {
		if intent, err = b.planDeleteIntent(ctx, pointID); err != nil {
			return filesystemDeleteIntent{}, nil, err
		}
	}
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
	if err != nil || catalog.PoolID != b.repo.PoolID || catalog.Protected || intent.Digest != catalog.ManifestSHA256 {
		return filesystemDeleteIntent{}, nil, ErrBackupConflict
	}
	if !exists {
		raw, err := json.Marshal(intent)
		if err != nil {
			return filesystemDeleteIntent{}, nil, err
		}
		if err := vmhost.AtomicWrite(b.deleteIntentPath(pointID), raw, 0600); err != nil {
			return filesystemDeleteIntent{}, nil, err
		}
	}
	return intent, pins, nil
}

// planDeleteIntent reads the published point and returns the intent that
// would remove exactly it.
func (b filesystemBackupStorage) planDeleteIntent(ctx context.Context, pointID string) (filesystemDeleteIntent, error) {
	pointDir := filepath.Join(b.repo.MountPath, pointID)
	manifest, digest, err := b.StatPoint(ctx, pointID)
	if err != nil {
		return filesystemDeleteIntent{}, ErrBackupConflict
	}
	root, _, err := openFilesystemDeleteDir(pointDir, manifest)
	if err != nil {
		return filesystemDeleteIntent{}, err
	}
	if root == nil {
		return filesystemDeleteIntent{}, ErrBackupConflict
	}
	root.Close()
	manifestRaw, err := readBoundedBackupFile(filepath.Join(pointDir, "manifest.json"), 1<<20)
	if err != nil {
		return filesystemDeleteIntent{}, err
	}
	if sha256Hex(manifestRaw) != digest {
		return filesystemDeleteIntent{}, ErrBackupConflict
	}
	return filesystemDeleteIntent{Manifest: manifestRaw, Digest: digest}, nil
}

// readBoundedBackupFile reads a regular file of at most limit bytes without
// following a symlink, so a share writer cannot swap in a huge file or device.
func readBoundedBackupFile(path string, limit int64) ([]byte, error) {
	f, err := openNoFollow(path, syscall.O_RDONLY|syscall.O_NONBLOCK)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(ErrBackupConflict, err)
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, ErrBackupConflict
	}
	return raw, nil
}

// finishDelete removes a point recorded by its delete intent. It is safe to repeat.
func (b filesystemBackupStorage) finishDelete(pointID string, intent filesystemDeleteIntent) error {
	var manifest protocol.BackupManifest
	if err := json.Unmarshal(intent.Manifest, &manifest); err != nil {
		return ErrBackupConflict
	}
	// Removal runs outside poolPeerMu, so authority is re-checked before each step.
	authority := func() error { return requirePoolAuthorityWritable(b.s) }
	if err := authority(); err != nil {
		return err
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
			if err := authority(); err != nil {
				return err
			}
			if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := syncBackupDirectory(pointDir); err != nil {
			return err
		}
	}
	if err := authority(); err != nil {
		return err
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
		if b.finishDelete(id, intent) == nil && requirePoolAuthorityWritable(b.s) == nil {
			_ = os.Remove(b.deleteIntentPath(id))
		}
	}
}

// SweepDeleteMarkers drops the intents of points the last delete removed from the catalog;
// Delete itself runs before the catalog entry goes, so they would wait for the next delete.
func (b filesystemBackupStorage) SweepDeleteMarkers(context.Context) {
	if pins, err := ActiveBackupRestorePins(b.s); err == nil {
		b.sweepDeleteIntents(pins)
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
	raw, err := readBoundedBackupFile(path, 2<<20)
	if err != nil {
		return intent, false, err
	}
	var manifest protocol.BackupManifest
	if json.Unmarshal(raw, &intent) != nil || json.Unmarshal(intent.Manifest, &manifest) != nil ||
		manifest.Valid() != nil || manifest.RecoveryPointID != id ||
		manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return intent, false, ErrBackupConflict
	}
	if intent.Digest != sha256Hex(intent.Manifest) {
		return intent, false, ErrBackupConflict
	}
	return intent, true, nil
}

func filesystemDeleteNames(manifest protocol.BackupManifest) []string {
	names := []string{"manifest.json"}
	for _, artifact := range manifestArtifacts(manifest) {
		names = append(names, artifact.Filename)
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
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// openNoFollow opens path without following a final symlink. A symlink fails
// with the raw ELOOP errno.
func openNoFollow(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// hashLimited hashes at most size+1 bytes of r, so callers can tell a stream
// longer than size (n > size) from a match.
func hashLimited(r io.Reader, size int64) (n int64, digest string, err error) {
	h := sha256.New()
	n, err = io.Copy(h, io.LimitReader(r, size+1))
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// requireSameManifest compares encodings: a manifest read back from storage went
// through JSON, so DeepEqual would fail on time.Time.
func requireSameManifest(stored, manifest protocol.BackupManifest) error {
	left, _ := json.Marshal(stored)
	right, _ := json.Marshal(manifest)
	if !bytes.Equal(left, right) {
		return ErrBackupConflict
	}
	return nil
}

// ctxReader stops a long read once its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// verifyArtifactStream hashes one stored artifact without keeping a copy.
func verifyArtifactStream(ctx context.Context, open func() (io.ReadCloser, error), size int64, digest string) error {
	r, err := open()
	if err != nil {
		return err
	}
	defer r.Close()
	n, sum, err := hashLimited(ctxReader{ctx, r}, size)
	if err != nil {
		return err
	}
	if n != size || sum != digest {
		return ErrBackupConflict
	}
	return nil
}

// manifestArtifacts lists the disk and firmware artifacts of a manifest by
// filename, size and digest.
func manifestArtifacts(manifest protocol.BackupManifest) []protocol.BackupDiskArtifact {
	artifacts := slices.Clone(manifest.Disks)
	for _, fw := range manifest.Firmware {
		artifacts = append(artifacts, protocol.BackupDiskArtifact{Filename: fw.Filename, Size: fw.Size, SHA256: fw.SHA256})
	}
	return artifacts
}

// copyVerifiedToLocal makes localPath an exact copy of the remote object. An
// existing file with the right size and digest is kept; anything else there is
// replaced. A failed or mismatched copy leaves no file behind.
func copyVerifiedToLocal(open func() (io.ReadCloser, error), localPath string, size int64, digest string) (err error) {
	if info, statErr := os.Lstat(localPath); statErr == nil {
		if info.Mode().IsRegular() && info.Size() == size {
			if local, openErr := os.Open(localPath); openErr == nil {
				n, sum, hashErr := hashLimited(local, size)
				local.Close()
				if hashErr == nil && n == size && sum == digest {
					return nil
				}
			}
		}
		if err := os.Remove(localPath); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	remote, err := open()
	if err != nil {
		return err
	}
	defer remote.Close()
	f, err := os.OpenFile(localPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(localPath)
		}
	}()
	n, sum, err := hashLimited(io.TeeReader(remote, f), size)
	if err != nil {
		return err
	}
	if n != size || sum != digest {
		return ErrBackupConflict
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// progressConn renews the deadline on every read and write, so a connection
// fails only after timeout without any progress. A response is read only after
// the whole request body is sent; the reader waiting for it stays alive for as
// long as the upload makes progress.
type progressConn struct {
	net.Conn
	timeout time.Duration
}

func (c progressConn) renew() error { return c.SetDeadline(time.Now().Add(c.timeout)) }

func (c progressConn) Read(p []byte) (int, error) {
	if err := c.renew(); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func (c progressConn) Write(p []byte) (int, error) {
	if err := c.renew(); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}
