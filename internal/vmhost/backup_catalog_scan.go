// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"nex.local/nex/internal/protocol"
)

const backupProtectMarkerName = ".nex-protected"

// WriteBackupProtectMarker persists deletion protection beside the recovery-point payload.
func WriteBackupProtectMarker(pointDir string, protected bool) error {
	path := filepath.Join(pointDir, backupProtectMarkerName)
	if !protected {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := json.Marshal(map[string]bool{"protected": true})
	if err != nil {
		return err
	}
	return AtomicWrite(path, raw, 0644)
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
// returns catalog entries. Incomplete or corrupt points are skipped with no catalog invent.
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
		if err := validatePublishedBackupPoint(pointDir, manifest); err != nil {
			continue
		}
		out = append(out, protocol.BackupRecoveryPoint{
			ID: manifest.RecoveryPointID, RepositoryID: repositoryID, PoolID: manifest.PoolID,
			SourceVMID: manifest.SourceVMID, SourceVMName: manifest.SourceVMName,
			CapturedAt: manifest.CapturedAt, Consistency: manifest.Consistency,
			ManifestSHA256: digest, Protected: ReadBackupProtectMarker(pointDir),
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

func validatePublishedBackupPoint(pointDir string, manifest protocol.BackupManifest) error {
	for _, disk := range manifest.Disks {
		sum, size, err := fileSHA256(filepath.Join(pointDir, disk.Filename))
		if err != nil || sum != disk.SHA256 || size != disk.Size {
			return errors.New("published backup disk digest mismatch")
		}
	}
	for _, fw := range manifest.Firmware {
		sum, size, err := fileSHA256(filepath.Join(pointDir, fw.Filename))
		if err != nil || sum != fw.SHA256 || size != fw.Size {
			return errors.New("published backup firmware digest mismatch")
		}
	}
	return nil
}

// VerifyPublishedBackupRecoveryPoint validates every artifact named by a
// previously read manifest before an interrupted publication is settled.
func VerifyPublishedBackupRecoveryPoint(pointDir string, manifest protocol.BackupManifest) error {
	if err := manifest.Valid(); err != nil || filepath.Base(filepath.Clean(pointDir)) != manifest.RecoveryPointID {
		return errors.New("backup recovery point manifest does not match its directory")
	}
	return validatePublishedBackupPoint(pointDir, manifest)
}
