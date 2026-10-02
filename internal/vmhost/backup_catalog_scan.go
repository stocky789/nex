// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"nex.local/nex/internal/protocol"
)

const backupProtectMarkerName = ".nex-protected"

// WriteBackupProtectMarker persists deletion protection beside the recovery-point payload.
// Each protect writes a unique marker, and unprotect removes only the marker it read,
// so a stalled call cannot undo a newer one.
func WriteBackupProtectMarker(pointDir string, protected bool) error {
	path := filepath.Join(pointDir, backupProtectMarkerName)
	if !protected {
		if err := sweepBackupProtectMarkers(pointDir); err != nil {
			return err
		}
		current, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return removeBackupProtectMarkerIfUnchanged(path, current)
	}
	if err := sweepBackupProtectMarkers(pointDir); err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]any{"protected": true, "nonce": rand.Text()})
	if err != nil {
		return err
	}
	return AtomicWrite(path, raw, 0644)
}

// removeBackupProtectMarkerIfUnchanged removes path only while it still holds want.
// The rename is atomic. A newer marker caught by it is renamed back (not linked, so
// shares without hard links work) and is never deleted: if it cannot be restored it
// stays beside the point as .rm-* and the error is returned.
func removeBackupProtectMarkerIfUnchanged(path string, want []byte) error {
	claimed := path + ".rm-" + rand.Text()
	if err := os.Rename(path, claimed); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	got, readErr := os.ReadFile(claimed)
	if readErr == nil && bytes.Equal(got, want) {
		return os.Remove(claimed)
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(claimed, path); err != nil {
			return errors.Join(readErr, err)
		}
	} else if readErr == nil {
		// An even newer marker was written meanwhile; the captured one is superseded.
		if err := os.Remove(claimed); err != nil {
			return err
		}
	}
	if readErr != nil {
		return readErr
	}
	return errors.New("backup protection marker changed during removal")
}

// sweepBackupProtectMarkers settles .rm-* files left by a crash between the rename-aside
// and the restore in removeBackupProtectMarkerIfUnchanged. Without a marker the file is
// the point's protection and is put back; beside a marker it is removed only when
// byte-identical to it, otherwise it is left for an operator.
func sweepBackupProtectMarkers(pointDir string) error {
	path := filepath.Join(pointDir, backupProtectMarkerName)
	leftovers, err := filepath.Glob(path + ".rm-*")
	if err != nil || len(leftovers) == 0 {
		return err
	}
	current, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.Rename(leftovers[0], path)
	} else if err != nil {
		return err
	}
	for _, name := range leftovers {
		if got, err := os.ReadFile(name); err == nil && bytes.Equal(got, current) {
			if err := os.Remove(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// BackupProtectMarkerLeftover reports a marker an unprotect set aside and did not
// finish removing, with no marker in its place. Until a write settles it the point
// stays protected. Read-only.
func BackupProtectMarkerLeftover(pointDir string) bool {
	path := filepath.Join(pointDir, backupProtectMarkerName)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	leftovers, _ := filepath.Glob(path + ".rm-*")
	return len(leftovers) > 0
}

// ReadBackupProtectMarker reports whether the repository payload is protected from prune.
func ReadBackupProtectMarker(pointDir string) bool {
	raw, err := os.ReadFile(filepath.Join(pointDir, backupProtectMarkerName))
	if err != nil {
		return false
	}
	var marker struct {
		Protected bool `json:"protected"`
	}
	if json.Unmarshal(raw, &marker) != nil {
		return false
	}
	return marker.Protected
}

// ScanBackupRepositoryPoints validates published manifests under a repository root and
// returns catalog entries. Incomplete points or missing/resized artifacts are skipped with
// no catalog invent; artifact digests are verified when the point is restored. It only
// reads: leftover protection markers are settled by the next protection write.
func ScanBackupRepositoryPoints(repoRoot, repositoryID, poolID string) ([]protocol.BackupRecoveryPoint, error) {
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return nil, errors.New("backup repository root is invalid")
	}
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.BackupRecoveryPoint, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, backupStagingPrefix) {
			continue
		}
		if !protocol.ValidSnapshotOperationID(name) {
			continue
		}
		pointDir := filepath.Join(repoRoot, name)
		manifest, digest, err := ReadBackupManifest(pointDir)
		if err != nil {
			continue
		}
		if name != manifest.RecoveryPointID {
			continue
		}
		if manifest.RepositoryID != repositoryID || (poolID != "" && manifest.PoolID != poolID) {
			continue
		}
		if err := CheckPublishedBackupArtifactSizes(pointDir, manifest); err != nil {
			continue
		}
		out = append(out, protocol.BackupRecoveryPoint{
			ID: manifest.RecoveryPointID, RepositoryID: repositoryID, PoolID: manifest.PoolID,
			SourceVMID: manifest.SourceVMID, SourceVMName: manifest.SourceVMName, PolicyID: manifest.PolicyID,
			CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency,
			ManifestSHA256: digest, Protected: ReadBackupProtectMarker(pointDir) || BackupProtectMarkerLeftover(pointDir),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CapturedAt.Equal(out[j].CapturedAt) {
			return out[i].CapturedAt.After(out[j].CapturedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// backupArtifacts lists the disk and firmware artifacts a manifest names.
func backupArtifacts(manifest protocol.BackupManifest) []protocol.BackupDiskArtifact {
	artifacts := slices.Clone(manifest.Disks)
	for _, fw := range manifest.Firmware {
		artifacts = append(artifacts, protocol.BackupDiskArtifact{Filename: fw.Filename, Size: fw.Size, SHA256: fw.SHA256})
	}
	return artifacts
}

func validatePublishedBackupPoint(pointDir string, manifest protocol.BackupManifest) error {
	for _, artifact := range backupArtifacts(manifest) {
		sum, size, err := fileSHA256(filepath.Join(pointDir, artifact.Filename))
		if err != nil || sum != artifact.SHA256 || size != artifact.Size {
			return errors.New("published backup artifact digest mismatch")
		}
	}
	return nil
}

// CheckPublishedBackupArtifactSizes cheaply confirms every artifact named by the
// manifest is a regular file of the recorded size. Only a published manifest,
// written after its artifacts were verified, may rely on it; restore still hashes.
func CheckPublishedBackupArtifactSizes(pointDir string, manifest protocol.BackupManifest) error {
	for _, artifact := range backupArtifacts(manifest) {
		info, err := os.Lstat(filepath.Join(pointDir, artifact.Filename))
		if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Size {
			return errors.New("published backup artifact is missing or changed size")
		}
	}
	return nil
}
