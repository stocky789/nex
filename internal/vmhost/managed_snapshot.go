// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

const (
	maxManagedSnapshotFirmwareFiles = 256
	maxManagedSnapshotFirmwareBytes = 256 << 20
	managedSnapshotJournalLimit     = 1 << 20
)

type managedSnapshotRecord struct {
	SnapshotID          string                               `json:"snapshot_id"`
	Name                string                               `json:"name"`
	Description         string                               `json:"description,omitempty"`
	CreatedAt           time.Time                            `json:"created_at"`
	BeforeDomain        []byte                               `json:"before_domain"`
	Before              managedSnapshotObservation           `json:"before"`
	ExpectedAfter       managedSnapshotObservation           `json:"expected_after"`
	ObservedAfter       *managedSnapshotObservation          `json:"observed_after,omitempty"`
	NativeXMLSHA256     string                               `json:"native_xml_sha256,omitempty"`
	Firmware            []managedSnapshotFirmwareArtifact    `json:"firmware,omitempty"`
	StorageDependencies []managedSnapshotStorageDependency   `json:"storage_dependencies"`
	Dependencies        *protocol.PoolVMSnapshotDependencies `json:"dependencies,omitempty"`
}

type managedSnapshotFirmwareSource struct {
	Kind     string                      `json:"kind"`
	Path     string                      `json:"path"`
	Manifest managedSnapshotFileManifest `json:"manifest"`
}

type managedSnapshotObservation struct {
	DomainState      string                           `json:"domain_state"`
	AutostartEnabled bool                             `json:"autostart_enabled"`
	DefinitionSHA256 string                           `json:"definition_sha256"`
	Disks            []managedSnapshotDiskObservation `json:"disks"`
}

type managedSnapshotDiskObservation struct {
	Target                string   `json:"target"`
	StorageID             string   `json:"storage_id"`
	StorageGeneration     uint64   `json:"storage_generation"`
	StorageIdentitySHA256 string   `json:"storage_identity_sha256"`
	Source                string   `json:"source"`
	Layers                []string `json:"layers"`
}

type managedSnapshotStorageDependency struct {
	StorageID            string `json:"storage_id"`
	Generation           uint64 `json:"generation"`
	IdentitySHA256       string `json:"identity_sha256"`
	SnapshotArtifactPath string `json:"snapshot_artifact_path"`
}

type managedSnapshotFirmwareArtifact struct {
	Kind             string                      `json:"kind"`
	SourcePath       string                      `json:"source_path"`
	SnapshotPath     string                      `json:"snapshot_path"`
	SourceManifest   managedSnapshotFileManifest `json:"source_manifest"`
	SnapshotManifest managedSnapshotFileManifest `json:"snapshot_manifest"`
}

type managedSnapshotFileManifest struct {
	Exists bool                          `json:"exists"`
	SHA256 string                        `json:"sha256,omitempty"`
	Files  []managedSnapshotFileMetadata `json:"files,omitempty"`
}

type managedSnapshotFileMetadata struct {
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256,omitempty"`
	Size         int64  `json:"size"`
	Mode         uint32 `json:"mode"`
	UID          uint32 `json:"uid"`
	GID          uint32 `json:"gid"`
	Directory    bool   `json:"directory,omitempty"`
}

// managedSnapshotIntentDigest hashes only immutable pre-reservation fields. ObservedAfter
// and SnapshotManifest are progress/effect evidence and must not change the digest on replay.
func managedSnapshotIntentDigest(command protocol.Command, record managedSnapshotRecord) (string, error) {
	if err := validateManagedSnapshotRequest(command); err != nil {
		return "", err
	}
	if record.Dependencies != nil {
		want, err := protocol.PoolVMSnapshotDependenciesForSpec(*command.VM)
		if err != nil || !reflect.DeepEqual(*record.Dependencies, want) {
			return "", errors.New("snapshot resource dependencies differ from the captured VM definition")
		}
	}
	if record.CreatedAt.IsZero() || validateSnapshotBeforeDomain(record.BeforeDomain, record.Before) != nil ||
		validateManagedSnapshotObservation(record.Before) != nil ||
		validateManagedSnapshotObservation(record.ExpectedAfter) != nil || validateSnapshotIntentStorage(command, record) != nil {
		return "", errors.New("snapshot creation intent is incomplete")
	}
	firmwareSources := make([]managedSnapshotFirmwareSource, 0, len(record.Firmware))
	for _, firmware := range record.Firmware {
		if (firmware.Kind != "nvram" && firmware.Kind != "swtpm") || !filepath.IsAbs(firmware.SourcePath) ||
			filepath.Clean(firmware.SourcePath) != firmware.SourcePath || !filepath.IsAbs(firmware.SnapshotPath) ||
			filepath.Clean(firmware.SnapshotPath) != firmware.SnapshotPath || !validManagedSnapshotManifest(firmware.SourceManifest) {
			return "", errors.New("snapshot firmware source intent is invalid")
		}
		firmwareSources = append(firmwareSources, managedSnapshotFirmwareSource{Kind: firmware.Kind, Path: firmware.SourcePath, Manifest: firmware.SourceManifest})
	}
	sort.Slice(firmwareSources, func(i, j int) bool { return firmwareSources[i].Kind < firmwareSources[j].Kind })
	storageDependencies := append([]managedSnapshotStorageDependency(nil), record.StorageDependencies...)
	sort.Slice(storageDependencies, func(i, j int) bool { return storageDependencies[i].StorageID < storageDependencies[j].StorageID })
	intent := struct {
		Command             protocol.Command                     `json:"command"`
		CreatedAt           time.Time                            `json:"created_at"`
		BeforeDomain        []byte                               `json:"before_domain"`
		Before              managedSnapshotObservation           `json:"before"`
		ExpectedAfter       managedSnapshotObservation           `json:"expected_after"`
		FirmwareSources     []managedSnapshotFirmwareSource      `json:"firmware_sources,omitempty"`
		StorageDependencies []managedSnapshotStorageDependency   `json:"storage_dependencies"`
		Dependencies        *protocol.PoolVMSnapshotDependencies `json:"dependencies,omitempty"`
	}{command, record.CreatedAt, record.BeforeDomain, record.Before, record.ExpectedAfter, firmwareSources, storageDependencies, record.Dependencies}
	b, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

func validateManagedSnapshotRequest(c protocol.Command) error {
	if c.Kind != "snapshot-create" || c.VM == nil || c.Snapshot == nil || !identifier.MatchString(c.OperationID) ||
		c.Snapshot.Valid() != nil || protocol.ValidateVMSpec(*c.VM) != nil ||
		c.Storage != nil || c.Network != nil || c.Media != nil || c.Power != nil || c.Delete != nil ||
		c.VMMedia != nil || c.StorageLifecycle != nil || c.ResourceLifecycle != nil || c.CredentialRetire != nil ||
		c.ZFSProvision != nil || c.ZFSReplace != nil || c.Pool != nil || c.ManagementNetwork != nil || c.SystemDiskReplace != nil ||
		c.Backup != nil || c.Restore != nil || c.BackupRepository != nil {
		return errors.New("invalid snapshot creation request")
	}
	return nil
}

func validateSnapshotIntentStorage(command protocol.Command, record managedSnapshotRecord) error {
	if len(record.StorageDependencies) == 0 || len(record.Before.Disks) != len(record.ExpectedAfter.Disks) ||
		len(record.Before.Disks) == 0 || len(command.VM.Disks) != len(record.Before.Disks) {
		return errors.New("snapshot storage dependencies are incomplete")
	}
	dependencies := make(map[string]managedSnapshotStorageDependency, len(record.StorageDependencies))
	for _, dependency := range record.StorageDependencies {
		if dependency.StorageID == "" || dependency.Generation == 0 || !lowerSnapshotHash(dependency.IdentitySHA256) ||
			!filepath.IsAbs(dependency.SnapshotArtifactPath) || filepath.Clean(dependency.SnapshotArtifactPath) != dependency.SnapshotArtifactPath {
			return errors.New("snapshot storage dependency is invalid")
		}
		if _, exists := dependencies[dependency.StorageID]; exists {
			return errors.New("snapshot storage dependency is duplicated")
		}
		dependencies[dependency.StorageID] = dependency
	}
	usedDependencies := map[string]bool{}
	for i := range record.Before.Disks {
		before, after := record.Before.Disks[i], record.ExpectedAfter.Disks[i]
		if command.VM.Disks[i].StorageID != before.StorageID || before.Target != after.Target || before.StorageID != after.StorageID ||
			before.StorageGeneration != after.StorageGeneration || before.StorageIdentitySHA256 != after.StorageIdentitySHA256 ||
			before.Source != before.Layers[0] || after.Source != after.Layers[0] ||
			!sameSnapshotDiskSuffix(before.Layers, after.Layers) {
			return errors.New("snapshot expected disk projection does not preserve every prior layer")
		}
		dependency, exists := dependencies[before.StorageID]
		if !exists || dependency.Generation != before.StorageGeneration || dependency.IdentitySHA256 != before.StorageIdentitySHA256 {
			return errors.New("snapshot storage dependency is missing a writable disk repository")
		}
		usedDependencies[before.StorageID] = true
	}
	if len(usedDependencies) != len(dependencies) {
		return errors.New("snapshot storage dependency does not belong to a writable disk")
	}
	return nil
}

func sameSnapshotDiskSuffix(before, after []string) bool {
	if len(after) != len(before)+1 || after[1] != before[0] {
		return false
	}
	for i := range before {
		if after[i+1] != before[i] {
			return false
		}
	}
	return true
}

func lowerSnapshotHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateManagedSnapshotRecord(operationID string, record managedSnapshotRecord) error {
	if operationID == "" || record.SnapshotID != operationID || record.Name == "" ||
		(protocol.VMSnapshotRequest{Name: record.Name, Description: record.Description}).Valid() != nil ||
		record.CreatedAt.IsZero() || validateSnapshotBeforeDomain(record.BeforeDomain, record.Before) != nil ||
		validateManagedSnapshotObservation(record.Before) != nil ||
		validateManagedSnapshotObservation(record.ExpectedAfter) != nil {
		return errors.New("managed snapshot journal record is invalid")
	}
	if record.ObservedAfter != nil && validateManagedSnapshotObservation(*record.ObservedAfter) != nil {
		return errors.New("managed snapshot observation is invalid")
	}
	if record.NativeXMLSHA256 != "" && !lowerSnapshotHash(record.NativeXMLSHA256) {
		return errors.New("managed snapshot native metadata digest is invalid")
	}
	if record.Dependencies != nil && record.Dependencies.Valid() != nil {
		return errors.New("managed snapshot resource dependencies are invalid")
	}
	if len(record.Firmware) > 2 {
		return errors.New("managed snapshot firmware capture is invalid")
	}
	seen := map[string]bool{}
	for _, artifact := range record.Firmware {
		if (artifact.Kind != "nvram" && artifact.Kind != "swtpm") || seen[artifact.Kind] ||
			!filepath.IsAbs(artifact.SourcePath) || filepath.Clean(artifact.SourcePath) != artifact.SourcePath ||
			!filepath.IsAbs(artifact.SnapshotPath) || filepath.Clean(artifact.SnapshotPath) != artifact.SnapshotPath ||
			!validManagedSnapshotManifest(artifact.SourceManifest) || !validManagedSnapshotManifest(artifact.SnapshotManifest) {
			return errors.New("managed snapshot firmware capture is invalid")
		}
		seen[artifact.Kind] = true
	}
	seenStorage := map[string]bool{}
	for _, storage := range record.StorageDependencies {
		if storage.StorageID == "" || storage.Generation == 0 || !lowerSnapshotHash(storage.IdentitySHA256) ||
			seenStorage[storage.StorageID] || !filepath.IsAbs(storage.SnapshotArtifactPath) ||
			filepath.Clean(storage.SnapshotArtifactPath) != storage.SnapshotArtifactPath {
			return errors.New("managed snapshot storage dependency is invalid")
		}
		seenStorage[storage.StorageID] = true
	}
	return nil
}

func validateSnapshotBeforeDomain(raw []byte, before managedSnapshotObservation) error {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return errors.New("snapshot before-domain definition is missing or oversized")
	}
	var domain managedDomain
	if err := xml.Unmarshal(raw, &domain); err != nil || domain.UUID == "" {
		return errors.New("snapshot before-domain definition is invalid")
	}
	digest, err := snapshotDefinitionDigest(raw)
	if err != nil || digest != before.DefinitionSHA256 {
		return errors.New("snapshot before-domain definition differs from its observation")
	}
	targets, disks := make([]string, len(before.Disks)), make([]string, len(before.Disks))
	for i, disk := range before.Disks {
		targets[i], disks[i] = disk.Target, disk.Source
	}
	return verifyManagedDiskHeads(domain, targets, disks)
}

func validateManagedSnapshotObservation(observation managedSnapshotObservation) error {
	switch observation.DomainState {
	case "shut off", "running":
	default:
		return errors.New("managed snapshot requires a stopped or running VM with native autostart disabled")
	}
	if observation.AutostartEnabled || len(observation.DefinitionSHA256) != 64 || len(observation.Disks) == 0 {
		return errors.New("managed snapshot requires a stopped or running VM with native autostart disabled")
	}
	seenTargets, seenSources := map[string]bool{}, map[string]bool{}
	for _, disk := range observation.Disks {
		if disk.Target == "" || disk.StorageID == "" || disk.StorageGeneration == 0 || len(disk.StorageIdentitySHA256) != 64 ||
			!filepath.IsAbs(disk.Source) || filepath.Clean(disk.Source) != disk.Source ||
			len(disk.Layers) == 0 || len(disk.Layers) > maxManagedDiskChain ||
			seenTargets[disk.Target] || seenSources[disk.Source] || disk.Layers[0] != disk.Source {
			return errors.New("managed snapshot disk observation is incomplete or ambiguous")
		}
		seenTargets[disk.Target], seenSources[disk.Source] = true, true
		seenLayers := map[string]bool{}
		for _, layer := range disk.Layers {
			if !filepath.IsAbs(layer) || filepath.Clean(layer) != layer || seenLayers[layer] {
				return errors.New("managed snapshot disk chain is invalid")
			}
			seenLayers[layer] = true
		}
	}
	return nil
}

func validManagedSnapshotManifest(manifest managedSnapshotFileManifest) bool {
	if !manifest.Exists {
		return manifest.SHA256 == "" && len(manifest.Files) == 0
	}
	if !lowerSnapshotHash(manifest.SHA256) || len(manifest.Files) == 0 {
		return false
	}
	if len(manifest.Files) > maxManagedSnapshotFirmwareFiles {
		return false
	}
	previous := ""
	totalBytes := int64(0)
	for _, file := range manifest.Files {
		if file.RelativePath == "" || filepath.IsAbs(file.RelativePath) || filepath.Clean(file.RelativePath) != file.RelativePath ||
			file.RelativePath == ".." || strings.HasPrefix(file.RelativePath, ".."+string(filepath.Separator)) ||
			len(file.RelativePath) > 256 || file.RelativePath <= previous || file.Size < 0 ||
			(file.Directory && file.Size != 0) || (!file.Directory && !lowerSnapshotHash(file.SHA256)) {
			return false
		}
		totalBytes += file.Size
		if totalBytes > maxManagedSnapshotFirmwareBytes {
			return false
		}
		previous = file.RelativePath
	}
	return true
}

// copyManagedSnapshotFirmware copies only regular files and directories. Libvirt-owned
// firmware state must not contain symlinks or special files; preserving ownership and
// mode is required for a later restore to the native NVRAM or swtpm location.
func copyManagedSnapshotFirmware(ctx context.Context, source, destination string) (managedSnapshotFileManifest, error) {
	if err := ctx.Err(); err != nil {
		return managedSnapshotFileManifest{}, err
	}
	info, err := os.Lstat(source)
	if errors.Is(err, fs.ErrNotExist) {
		if _, destinationErr := os.Lstat(destination); !errors.Is(destinationErr, fs.ErrNotExist) {
			if destinationErr == nil {
				return managedSnapshotFileManifest{}, errors.New("snapshot firmware destination exists for missing source state")
			}
			return managedSnapshotFileManifest{}, destinationErr
		}
		return managedSnapshotFileManifest{}, nil
	}
	if err != nil {
		return managedSnapshotFileManifest{}, err
	}
	sourceManifest, err := managedSnapshotManifest(ctx, source)
	if err != nil {
		return managedSnapshotFileManifest{}, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return managedSnapshotFileManifest{}, errors.New("VM firmware state contains an unsupported file type")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			return managedSnapshotFileManifest{}, errors.New("snapshot firmware destination already exists")
		}
		return managedSnapshotFileManifest{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return managedSnapshotFileManifest{}, err
	}
	if err := copySnapshotNode(ctx, source, destination, info); err != nil {
		return managedSnapshotFileManifest{}, err
	}
	if err := syncManagedDir(filepath.Dir(destination)); err != nil {
		return managedSnapshotFileManifest{}, err
	}
	manifest, err := managedSnapshotManifest(ctx, destination)
	if err != nil {
		return managedSnapshotFileManifest{}, err
	}
	sourceAfter, err := managedSnapshotManifest(ctx, source)
	if err != nil {
		return managedSnapshotFileManifest{}, err
	}
	if !snapshotManifestsEqual(sourceManifest, sourceAfter) || !snapshotManifestsEqual(sourceAfter, manifest) {
		return managedSnapshotFileManifest{}, errors.New("VM firmware snapshot copy differs from its source")
	}
	return manifest, nil
}

func copySnapshotNode(ctx context.Context, source, destination string, info os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			from, to := filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())
			child, err := os.Lstat(from)
			if err != nil || child.Mode()&os.ModeSymlink != 0 || (!child.IsDir() && !child.Mode().IsRegular()) {
				return errors.New("VM firmware state contains an unsupported file type")
			}
			if err := copySnapshotNode(ctx, from, to, child); err != nil {
				return err
			}
		}
		if err := copySnapshotMetadata(destination, info); err != nil {
			return err
		}
		return syncManagedDir(destination)
	}
	return copySnapshotFile(source, destination, info)
}

func snapshotManifestsEqual(left, right managedSnapshotFileManifest) bool {
	if left.Exists != right.Exists || left.SHA256 != right.SHA256 || len(left.Files) != len(right.Files) {
		return false
	}
	for i := range left.Files {
		if left.Files[i] != right.Files[i] {
			return false
		}
	}
	return true
}

func copySnapshotFile(source, destination string, info os.FileInfo) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return copySnapshotMetadata(destination, info)
}

func copySnapshotMetadata(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot preserve VM firmware ownership")
	}
	if err := os.Chown(path, int(stat.Uid), int(stat.Gid)); err != nil {
		return err
	}
	if err := os.Chmod(path, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func managedSnapshotManifest(ctx context.Context, root string) (managedSnapshotFileManifest, error) {
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return managedSnapshotFileManifest{}, nil
	}
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || (!rootInfo.IsDir() && !rootInfo.Mode().IsRegular()) {
		return managedSnapshotFileManifest{}, errors.New("VM firmware state is not a regular file or directory")
	}
	files := []managedSnapshotFileMetadata{}
	var totalBytes int64
	add := func(path, relative string, info os.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) ||
			(!info.IsDir() && stat.Nlink != 1) {
			return errors.New("VM firmware state contains an unsupported file type")
		}
		size := info.Size()
		if info.IsDir() {
			size = 0
		}
		entry := managedSnapshotFileMetadata{RelativePath: relative, Size: size, Mode: uint32(info.Mode().Perm()), UID: stat.Uid, GID: stat.Gid, Directory: info.IsDir()}
		if !info.IsDir() {
			digest, err := snapshotFileDigest(path)
			if err != nil {
				return err
			}
			entry.SHA256 = digest
		}
		files = append(files, entry)
		if len(files) > maxManagedSnapshotFirmwareFiles {
			return errors.New("VM firmware state contains too many files")
		}
		totalBytes += entry.Size
		if totalBytes > maxManagedSnapshotFirmwareBytes {
			return errors.New("VM firmware state exceeds the snapshot capture limit")
		}
		return nil
	}
	if rootInfo.IsDir() {
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if relative == "." {
				relative = "root"
			}
			return add(path, relative, info)
		})
	} else {
		err = add(root, "root", rootInfo)
	}
	if err != nil {
		return managedSnapshotFileManifest{}, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	encoded, err := json.Marshal(files)
	if err != nil {
		return managedSnapshotFileManifest{}, err
	}
	digest := sha256.Sum256(encoded)
	return managedSnapshotFileManifest{Exists: true, SHA256: hex.EncodeToString(digest[:]), Files: files}, nil
}

func snapshotFileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func snapshotObservationDigest(observation managedSnapshotObservation) (string, error) {
	if err := validateManagedSnapshotObservation(observation); err != nil {
		return "", err
	}
	b, err := json.Marshal(observation)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

func managedSnapshotDescriptor(command protocol.Command, reservation protocol.PoolVMRecoveryReservation, record managedSnapshotRecord) (protocol.PoolVMSnapshot, error) {
	if reservation.Kind != "snapshot-create" || reservation.OperationID != record.SnapshotID ||
		validateManagedSnapshotReservation(command, record, reservation) != nil ||
		validateManagedSnapshotRecord(reservation.OperationID, record) != nil {
		return protocol.PoolVMSnapshot{}, errors.New("snapshot catalog descriptor does not match its local journal")
	}
	descriptor := protocol.PoolVMSnapshot{
		ID: reservation.OperationID, VMID: reservation.VMID, Name: record.Name, Description: record.Description,
		CreatedAt: record.CreatedAt, SpecRevision: reservation.SpecRevision,
		SpecSHA256: reservation.SpecSHA256, DiskIdentitySHA256: reservation.DiskIdentitySHA256, Dependencies: cloneSnapshotDependencies(record.Dependencies),
	}
	if err := descriptor.Valid(); err != nil {
		return protocol.PoolVMSnapshot{}, err
	}
	return descriptor, nil
}

func validateManagedSnapshotReservation(command protocol.Command, record managedSnapshotRecord, reservation protocol.PoolVMRecoveryReservation) error {
	if err := reservation.Valid(); err != nil {
		return err
	}
	intentSHA256, err := managedSnapshotIntentDigest(command, record)
	if err != nil {
		return err
	}
	specJSON, err := json.Marshal(command.VM)
	if err != nil {
		return err
	}
	disksJSON, err := json.Marshal(command.VM.Disks)
	if err != nil {
		return err
	}
	specDigest, diskDigest := sha256.Sum256(specJSON), sha256.Sum256(disksJSON)
	if reservation.Kind != "snapshot-create" || reservation.OperationID != command.OperationID ||
		reservation.PoolID != command.VM.PoolID || reservation.VMID != command.VM.ID ||
		reservation.OwnerHostID != command.VM.HostID || reservation.SpecRevision != command.VM.Revision ||
		reservation.SpecSHA256 != hex.EncodeToString(specDigest[:]) ||
		reservation.DiskIdentitySHA256 != hex.EncodeToString(diskDigest[:]) || reservation.IntentSHA256 != intentSHA256 {
		return errors.New("master snapshot reservation does not match the local snapshot intent")
	}
	return nil
}

func managedSnapshotAdmissionRefusal(reason string) error {
	switch reason {
	case "name-in-use":
		return errors.New("snapshot name is already in use on the pool VM")
	case "catalog-full":
		return errors.New("pool VM snapshot catalog is full")
	default:
		return errors.New("snapshot master returned an unsupported admission refusal")
	}
}

func managedSnapshotNativeName(operationID string) string {
	return "nex-" + operationID
}

func managedSnapshotExternalPath(directory, operationID, target string) (string, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || strings.ContainsRune(directory, ',') || !identifier.MatchString(operationID) || !managedSource.MatchString(target) {
		return "", errors.New("invalid managed snapshot disk destination")
	}
	return filepath.Join(directory, "snapshot-"+operationID+"-"+target+".qcow2"), nil
}

func snapshotDefinitionDigest(raw []byte) (string, error) {
	var domain managedDomain
	if err := xml.Unmarshal(raw, &domain); err != nil || domain.UUID == "" {
		return "", errors.New("cannot inspect VM definition for snapshot capture")
	}
	for i := range domain.Devices.Disks {
		if domain.Devices.Disks[i].Readonly == nil && domain.Devices.Disks[i].Source != nil {
			domain.Devices.Disks[i].Source.File = ""
		}
	}
	b, err := xml.Marshal(domain)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

func snapshotStorageIdentityDigest(storage protocol.StorageRegistration, access storageAccess) (string, error) {
	var identity any
	if storage.StorageKind() == protocol.StorageKindLocal {
		if !filepath.IsAbs(storage.Path) || filepath.Clean(storage.Path) != storage.Path || access.Path != storage.Path ||
			access.FilesystemID == "" || access.Identity != "local:"+storage.Path+":fsid="+access.FilesystemID ||
			!reflect.DeepEqual(storage.Identity, protocol.StorageIdentity{}) || !reflect.DeepEqual(access.Observed, protocol.StorageIdentity{}) {
			return "", errors.New("registered local VM storage identity is unavailable")
		}
		identity = access.Identity
	} else {
		if !StorageIdentityMatchesExpected(storage.Identity, access.Observed) {
			return "", errors.New("registered VM storage identity changed")
		}
		observed := access.Observed
		if observed.SMB != nil && observed.SMB.MarkerID != "" {
			stable := *observed.SMB
			stable.FilesystemID = ""
			observed.SMB = &stable
		}
		identity = observed
	}
	b, err := json.Marshal(identity)
	if err != nil {
		return "", errors.New("registered VM storage identity is unavailable")
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

func (e *ManagedEngine) observeManagedSnapshot(ctx context.Context, js []managedJournal, current managedJournal, mode string) (managedSnapshotObservation, managedDomain, []byte, error) {
	vm := current.Command.VM
	if vm == nil || current.Result.Status != "succeeded" || len(current.Disks) != len(vm.Disks) || len(current.Targets) != len(current.Disks) {
		return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("current managed VM disk evidence is incomplete")
	}
	if !validSnapshotObservationMode(mode) {
		return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("snapshot observation mode is invalid")
	}
	stateOut, err := managedVirsh(ctx, "domstate", vm.ID)
	state := strings.TrimSpace(string(stateOut))
	running := protocol.SnapshotModeRunning(mode)
	switch {
	case err != nil:
		if running {
			return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("VM must be confirmed running before running snapshot creation")
		}
		return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("VM must be confirmed stopped before snapshot creation")
	case running && state == "running":
	case !running && state == "shut off":
	default:
		if running {
			return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("VM must be confirmed running before running snapshot creation")
		}
		return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("VM must be confirmed stopped before snapshot creation")
	}
	info, err := managedVirsh(ctx, "dominfo", vm.ID)
	if err != nil || !autostartDisabled(info) {
		return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("VM native autostart must be confirmed disabled before snapshot creation")
	}
	dumpArgs := []string{"dumpxml", vm.ID}
	if !running {
		dumpArgs = append(dumpArgs, "--inactive")
	}
	raw, err := managedVirsh(ctx, dumpArgs...)
	if err != nil {
		return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("persistent VM definition is unavailable for snapshot creation")
	}
	if !running {
		if err = validateManagedDefinition(raw, current.Domain); err != nil {
			return managedSnapshotObservation{}, managedDomain{}, nil, err
		}
	}
	var domain managedDomain
	if err = xml.Unmarshal(raw, &domain); err != nil {
		return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("persistent VM definition is invalid")
	}
	if running && domain.OS.Firmware == "efi" {
		return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("running snapshots are not supported for UEFI guests in this development slice")
	}
	if err = verifyManagedDiskHeads(domain, current.Targets, current.Disks); err != nil {
		return managedSnapshotObservation{}, managedDomain{}, nil, err
	}
	chains, err := e.diskChains(ctx, js, current)
	if err != nil {
		return managedSnapshotObservation{}, managedDomain{}, nil, err
	}
	if len(chains) != len(current.Disks) {
		return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("current VM backing-chain evidence is incomplete")
	}
	definitionDigest, err := snapshotDefinitionDigest(raw)
	if err != nil {
		return managedSnapshotObservation{}, managedDomain{}, nil, err
	}
	observation := managedSnapshotObservation{DomainState: state, DefinitionSHA256: definitionDigest}
	for i, disk := range current.Disks {
		storage, access, err := managedStorageByID(ctx, js, vm.Disks[i].StorageID, 0)
		if err != nil {
			return managedSnapshotObservation{}, managedDomain{}, nil, err
		}
		identityDigest, err := snapshotStorageIdentityDigest(storage, access)
		if err != nil {
			return managedSnapshotObservation{}, managedDomain{}, nil, err
		}
		observed, err := observeManagedDiskChain(ctx, disk)
		if err != nil || !slices.Equal(observed, chains[i]) {
			return managedSnapshotObservation{}, managedDomain{}, nil, errors.New("VM backing chain changed during snapshot preflight")
		}
		observation.Disks = append(observation.Disks, managedSnapshotDiskObservation{
			Target: current.Targets[i], StorageID: storage.ID, StorageGeneration: effectiveStorageGeneration(storage),
			StorageIdentitySHA256: identityDigest, Source: disk, Layers: observed,
		})
	}
	return observation, domain, raw, nil
}

func effectiveStorageGeneration(storage protocol.StorageRegistration) uint64 {
	if storage.Generation == 0 {
		return 1
	}
	return storage.Generation
}

func snapshotFirmwareSources(ctx context.Context, domain managedDomain, vmID, firstDiskRoot, operationID string) ([]managedSnapshotFirmwareArtifact, error) {
	artifacts := []managedSnapshotFirmwareArtifact{}
	if domain.OS.Firmware == "efi" {
		if domain.OS.NVRAM == nil || domain.OS.NVRAM.Path == "" {
			return nil, errors.New("UEFI VM has no inspectable NVRAM state")
		}
		if err := firmwareVariablesFile(domain.OS.NVRAM.Path); err != nil {
			return nil, err
		}
		destination := filepath.Join(firstDiskRoot, "snapshot-"+operationID+"-nvram")
		manifest, err := managedSnapshotManifest(ctx, domain.OS.NVRAM.Path)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, managedSnapshotFirmwareArtifact{Kind: "nvram", SourcePath: domain.OS.NVRAM.Path, SnapshotPath: destination, SourceManifest: manifest})
	}
	if domain.Devices.TPM != nil && domain.Devices.TPM.Backend.Type == "emulator" {
		source, err := managedTPMStateSource(domain, vmID)
		if err != nil {
			return nil, err
		}
		manifest, err := managedSnapshotManifest(ctx, source)
		if err != nil {
			return nil, err
		}
		destination := filepath.Join(firstDiskRoot, "snapshot-"+operationID+"-swtpm")
		artifacts = append(artifacts, managedSnapshotFirmwareArtifact{Kind: "swtpm", SourcePath: source, SnapshotPath: destination, SourceManifest: manifest})
	}
	return artifacts, nil
}

type managedNativeSnapshotXML struct {
	Name         string `xml:"name"`
	Description  string `xml:"description"`
	State        string `xml:"state"`
	CreationTime string `xml:"creationTime"`
	Parent       struct {
		Name string `xml:"name"`
	} `xml:"parent"`
	Memory struct {
		Snapshot string `xml:"snapshot,attr"`
	} `xml:"memory"`
	Disks []struct {
		Name     string `xml:"name,attr"`
		Snapshot string `xml:"snapshot,attr"`
		Type     string `xml:"type,attr"`
		Driver   struct {
			Type string `xml:"type,attr"`
		} `xml:"driver"`
		Source struct {
			File string `xml:"file,attr"`
		} `xml:"source"`
	} `xml:"disks>disk"`
	RevertDisks []struct {
		Name     string `xml:"name,attr"`
		Snapshot string `xml:"snapshot,attr"`
		Type     string `xml:"type,attr"`
		Driver   struct {
			Type string `xml:"type,attr"`
		} `xml:"driver"`
		Source struct {
			File string `xml:"file,attr"`
		} `xml:"source"`
	} `xml:"revertDisks>disk"`
	Domain struct {
		Type  string `xml:"type,attr"`
		Inner string `xml:",innerxml"`
	} `xml:"domain"`
}

func readManagedNativeSnapshotXML(ctx context.Context, vmID, name string) (managedNativeSnapshotXML, []byte, error) {
	b, err := managedVirsh(ctx, "snapshot-dumpxml", vmID, name)
	if err != nil {
		return managedNativeSnapshotXML{}, nil, err
	}
	var snapshot managedNativeSnapshotXML
	if err = xml.Unmarshal(b, &snapshot); err != nil || snapshot.Name != name || snapshot.Memory.Snapshot != "no" || len(snapshot.Disks) == 0 {
		return managedNativeSnapshotXML{}, nil, errors.New("native snapshot metadata does not match the stopped VM")
	}
	// shutoff is a stopped-domain capture; disk-snapshot is libvirt's running disk-only capture.
	switch snapshot.State {
	case "shutoff", "disk-snapshot":
	default:
		return managedNativeSnapshotXML{}, nil, errors.New("native snapshot metadata does not match the stopped VM")
	}
	return snapshot, b, nil
}

func managedSnapshotXMLDigest(ctx context.Context, vmID, name string, record managedSnapshotRecord) (string, error) {
	snapshot, b, err := readManagedNativeSnapshotXML(ctx, vmID, name)
	if err != nil {
		return "", err
	}
	if len(snapshot.Disks) == 0 {
		return "", errors.New("native snapshot metadata does not match the stopped VM")
	}
	want := make(map[string]string, len(record.ExpectedAfter.Disks))
	for _, disk := range record.ExpectedAfter.Disks {
		want[disk.Target] = disk.Source
	}
	savedDomainXML := []byte("<domain type=\"" + snapshot.Domain.Type + "\">" + snapshot.Domain.Inner + "</domain>")
	var savedDomain managedDomain
	if snapshot.Domain.Type == "" || len(snapshot.Domain.Inner) == 0 || xml.Unmarshal(savedDomainXML, &savedDomain) != nil || savedDomain.UUID == "" {
		return "", errors.New("native snapshot metadata omits its saved VM definition")
	}
	savedDefinitionDigest, err := snapshotDefinitionDigest(savedDomainXML)
	if err != nil {
		return "", errors.New("native snapshot saved VM definition differs from the captured before-state")
	}
	if savedDefinitionDigest != record.Before.DefinitionSHA256 {
		// Running disk-only captures store a libvirt-normalized nested domain that can
		// differ from the live dumpxml hashed at intent time. Exact before disk heads
		// below remain the binding for those captures.
		if snapshot.State != "disk-snapshot" || record.Before.DomainState != "running" {
			return "", errors.New("native snapshot saved VM definition differs from the captured before-state")
		}
	}
	beforeTargets, beforeDisks := make([]string, len(record.Before.Disks)), make([]string, len(record.Before.Disks))
	for i, disk := range record.Before.Disks {
		beforeTargets[i], beforeDisks[i] = disk.Target, disk.Source
	}
	if err := verifyManagedDiskHeads(savedDomain, beforeTargets, beforeDisks); err != nil {
		return "", errors.New("native snapshot saved VM disks differ from the captured before-state")
	}
	readonlyTargets := map[string]bool{}
	for _, disk := range savedDomain.Devices.Disks {
		if disk.Readonly != nil || disk.Device == "cdrom" {
			readonlyTargets[disk.Target.Dev] = true
		}
	}
	seen := map[string]bool{}
	for _, disk := range snapshot.Disks {
		path, expectedDisk := want[disk.Name]
		if expectedDisk {
			if disk.Snapshot != "external" || disk.Source.File != path || seen[disk.Name] {
				return "", errors.New("native snapshot metadata has an unexpected writable disk projection")
			}
			seen[disk.Name] = true
		} else if !readonlyTargets[disk.Name] || disk.Snapshot != "no" {
			return "", errors.New("native snapshot metadata contains an unmanaged disk or snapshots read-only media")
		}
	}
	if len(seen) != len(want) {
		return "", errors.New("native snapshot metadata omits a writable disk")
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

func managedSnapshotAbsent(ctx context.Context, vmID, name string) (bool, error) {
	b, err := managedVirsh(ctx, "snapshot-list", vmID, "--name")
	if err != nil {
		return false, err
	}
	for _, existing := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(existing) == name {
			return false, nil
		}
	}
	return true, nil
}

func validSnapshotObservationMode(mode string) bool {
	switch mode {
	case "", "stopped", "crash", "quiesced":
		return true
	default:
		return false
	}
}

func (e *ManagedEngine) snapshotIntent(ctx context.Context, command protocol.Command, js []managedJournal, current managedJournal, createdAt time.Time) (managedSnapshotRecord, managedDomain, []byte, error) {
	mode := ""
	if command.Snapshot != nil {
		mode = command.Snapshot.Mode
	}
	before, domain, raw, err := e.observeManagedSnapshot(ctx, js, current, mode)
	if err != nil {
		return managedSnapshotRecord{}, managedDomain{}, nil, err
	}
	if !reflect.DeepEqual(current.Command.VM, command.VM) {
		return managedSnapshotRecord{}, managedDomain{}, nil, errors.New("snapshot request does not match the current managed VM definition")
	}
	dependencies, err := protocol.PoolVMSnapshotDependenciesForSpec(*command.VM)
	if err != nil {
		return managedSnapshotRecord{}, managedDomain{}, nil, err
	}
	record := managedSnapshotRecord{SnapshotID: command.OperationID, Name: command.Snapshot.Name, Description: command.Snapshot.Description, CreatedAt: createdAt, BeforeDomain: append([]byte(nil), raw...), Before: before, Dependencies: &dependencies}
	byStorage := map[string]managedSnapshotStorageDependency{}
	for i, disk := range command.VM.Disks {
		storage, access, err := managedStorageByID(ctx, js, disk.StorageID, 0)
		if err != nil {
			return managedSnapshotRecord{}, managedDomain{}, nil, err
		}
		vmDir := filepath.Join(access.Path, "nex-vms", command.VM.ID)
		if err := managedRepositorySubdir(ctx, js, storage.ID, vmDir); err != nil {
			return managedSnapshotRecord{}, managedDomain{}, nil, err
		}
		if !strings.HasPrefix(current.Disks[i], vmDir+string(os.PathSeparator)) {
			return managedSnapshotRecord{}, managedDomain{}, nil, errors.New("managed disk is outside its registered VM storage directory")
		}
		identity, err := snapshotStorageIdentityDigest(storage, access)
		if err != nil || identity != before.Disks[i].StorageIdentitySHA256 || effectiveStorageGeneration(storage) != before.Disks[i].StorageGeneration {
			return managedSnapshotRecord{}, managedDomain{}, nil, errors.New("VM storage registration changed during snapshot preflight")
		}
		dep, ok := byStorage[storage.ID]
		if ok && (dep.IdentitySHA256 != identity || dep.Generation != effectiveStorageGeneration(storage) || dep.SnapshotArtifactPath != vmDir) {
			return managedSnapshotRecord{}, managedDomain{}, nil, errors.New("VM storage registration resolves to inconsistent paths")
		}
		byStorage[storage.ID] = managedSnapshotStorageDependency{StorageID: storage.ID, Generation: effectiveStorageGeneration(storage), IdentitySHA256: identity, SnapshotArtifactPath: vmDir}
		newHead, err := managedSnapshotExternalPath(vmDir, command.OperationID, current.Targets[i])
		if err != nil {
			return managedSnapshotRecord{}, managedDomain{}, nil, err
		}
		if _, err := os.Lstat(newHead); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				err = errors.New("snapshot disk destination already exists")
			}
			return managedSnapshotRecord{}, managedDomain{}, nil, err
		}
	}
	for _, dependency := range byStorage {
		record.StorageDependencies = append(record.StorageDependencies, dependency)
	}
	sort.Slice(record.StorageDependencies, func(i, j int) bool {
		return record.StorageDependencies[i].StorageID < record.StorageDependencies[j].StorageID
	})
	_, firstAccess, err := managedStorageByID(ctx, js, command.VM.Disks[0].StorageID, 0)
	if err != nil {
		return managedSnapshotRecord{}, managedDomain{}, nil, err
	}
	firstRoot := filepath.Join(firstAccess.Path, "nex-vms", command.VM.ID)
	record.Firmware, err = snapshotFirmwareSources(ctx, domain, command.VM.ID, firstRoot, command.OperationID)
	if err != nil {
		return managedSnapshotRecord{}, managedDomain{}, nil, err
	}
	record.ExpectedAfter = managedSnapshotObservation{DomainState: before.DomainState, AutostartEnabled: before.AutostartEnabled, DefinitionSHA256: before.DefinitionSHA256}
	for _, disk := range before.Disks {
		vmDir := byStorage[disk.StorageID].SnapshotArtifactPath
		head, err := managedSnapshotExternalPath(vmDir, command.OperationID, disk.Target)
		if err != nil {
			return managedSnapshotRecord{}, managedDomain{}, nil, err
		}
		record.ExpectedAfter.Disks = append(record.ExpectedAfter.Disks, managedSnapshotDiskObservation{
			Target: disk.Target, StorageID: disk.StorageID, StorageGeneration: disk.StorageGeneration,
			StorageIdentitySHA256: disk.StorageIdentitySHA256, Source: head,
			Layers: append([]string{head}, disk.Layers...),
		})
	}
	if err := validateManagedSnapshotRecord(command.OperationID, record); err != nil {
		return managedSnapshotRecord{}, managedDomain{}, nil, err
	}
	if _, err := managedSnapshotIntentDigest(command, record); err != nil {
		return managedSnapshotRecord{}, managedDomain{}, nil, err
	}
	return record, domain, raw, nil
}

func managedSnapshotOperationLock(ctx context.Context, stateDir, vmID string) (*os.File, error) {
	if !identifier.MatchString(vmID) {
		return nil, errors.New("invalid VM identity for snapshot lock")
	}
	digest := sha256.Sum256([]byte(vmID))
	path := filepath.Join(stateDir, "snapshot-"+hex.EncodeToString(digest[:12])+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func managedSnapshotUnlock(f *os.File) {
	if f != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}

func (e *ManagedEngine) saveSnapshotJournal(j managedJournal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if len(b) > managedSnapshotJournalLimit {
		return errors.New("snapshot recovery journal exceeds its durable record limit")
	}
	return atomicWrite(filepath.Join(e.stateDir, j.Command.OperationID+".json"), b)
}

func managedSnapshotPoints(js []managedJournal, operationID string) (managedJournal, bool) {
	for _, j := range js {
		if j.Command.OperationID == operationID {
			return j, true
		}
	}
	return managedJournal{}, false
}

func (e *ManagedEngine) updateSnapshotCurrentProjection(ctx context.Context, j managedJournal, raw []byte) error {
	if j.Command.VM == nil || j.Snapshot == nil || j.Snapshot.ObservedAfter == nil {
		return errors.New("completed snapshot has no current disk projection")
	}
	js, err := e.journals()
	if err != nil {
		return err
	}
	cur, err := managedCurrent(js, j.Command.VM.ID)
	if err != nil || !reflect.DeepEqual(cur.Command.VM, j.Command.VM) {
		return errors.New("managed VM current definition changed during snapshot creation")
	}
	cur.Disks = make([]string, len(j.Snapshot.ObservedAfter.Disks))
	cur.DiskChains = make([][]string, len(cur.Disks))
	for i, disk := range j.Snapshot.ObservedAfter.Disks {
		cur.Disks[i] = disk.Source
		cur.DiskChains[i] = append([]string(nil), disk.Layers...)
	}
	cur.Domain = append([]byte(nil), raw...)
	return e.save(cur)
}

func (e *ManagedEngine) CreateStoppedSnapshot(
	ctx context.Context,
	command protocol.Command,
	reserve func(context.Context, protocol.Command, string, time.Time) (protocol.PoolVMRecoveryReservation, error),
	settle func(context.Context, protocol.PoolVMRecoveryEvidence) error,
) (protocol.Result, error) {
	result := protocol.Result{OperationID: command.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	if err := validateManagedSnapshotRequest(command); err != nil || reserve == nil || settle == nil || e == nil || e.stateDir == "" {
		return result, errors.New("invalid stopped snapshot operation")
	}
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		return result, err
	}
	real, err := filepath.EvalSymlinks(e.stateDir)
	if err != nil || real != e.stateDir {
		return result, errors.New("managed state must not use symlinks")
	}
	vmID := command.VM.ID
	vmLock, err := managedSnapshotOperationLock(ctx, e.stateDir, vmID)
	if err != nil {
		return result, err
	}
	defer managedSnapshotUnlock(vmLock)
	inFlight.Lock()
	if inFlight.ids[command.OperationID] {
		inFlight.Unlock()
		return result, errors.New("snapshot operation is already running")
	}
	inFlight.ids[command.OperationID] = true
	inFlight.Unlock()
	defer func() {
		inFlight.Lock()
		delete(inFlight.ids, command.OperationID)
		inFlight.Unlock()
	}()

	commandJSON, _ := json.Marshal(command)
	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return result, err
	}
	js, err := e.journals()
	if err != nil {
		unlock(global)
		return result, err
	}
	j, exists := managedSnapshotPoints(js, command.OperationID)
	if exists {
		oldJSON, _ := json.Marshal(j.Command)
		if !reflect.DeepEqual(commandJSON, oldJSON) {
			unlock(global)
			result.Error = "operation ID already belongs to different input"
			return result, nil
		}
		if j.RecoverySettled {
			unlock(global)
			return j.Result, nil
		}
	} else {
		if err = recoveryReservationRefusal(js, command); err != nil {
			unlock(global)
			result.Error = err.Error()
			return result, nil
		}
		current, currentErr := managedCurrent(js, vmID)
		if currentErr != nil || !reflect.DeepEqual(current.Command.VM, command.VM) {
			unlock(global)
			result.Error = "snapshot request does not match the current managed VM definition"
			return result, nil
		}
		createdAt := time.Now().UTC()
		record := managedSnapshotRecord{SnapshotID: command.OperationID, Name: command.Snapshot.Name, Description: command.Snapshot.Description, CreatedAt: createdAt}
		j = managedJournal{Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending", ObservedAt: createdAt}, Stage: "snapshot-preflight", RecoveryStage: "preflight", Snapshot: &record}
		if err = e.saveSnapshotJournal(j); err != nil {
			unlock(global)
			if removeErr := e.removePreflightSnapshot(ctx, command.OperationID); removeErr != nil {
				result.Status, result.Error = "needs_attention", errors.Join(err, removeErr).Error()
				return result, nil
			}
			return result, err
		}
	}
	unlock(global)

	if j.RecoveryStage == "preflight" {
		js, err = e.journals()
		if err == nil {
			current, currentErr := managedCurrent(js, vmID)
			if currentErr != nil || !reflect.DeepEqual(current.Command.VM, command.VM) {
				err = errors.New("snapshot request no longer matches the current managed VM definition")
			} else {
				record, _, currentXML, intentErr := e.snapshotIntent(ctx, command, js, current, j.Snapshot.CreatedAt)
				j.Snapshot = &record
				err = intentErr
				if err == nil {
					j.Domain = currentXML
				}
			}
		}
		if err != nil {
			return e.failSnapshotPreflight(ctx, result, command.OperationID, err)
		}
		j.RecoveryStage, j.Stage = "local-intent", "snapshot-local-intent"
		j.RecoveryOutcome = ""
		j.Disks, j.DiskChains = snapshotProjection(*j.Snapshot, false)
		// Size the complete post-effect record before any master or native effect. The
		// source/copy manifests are required to match, and the XML digest is fixed-width.
		sized := j
		sized.Snapshot = cloneManagedSnapshotRecord(j.Snapshot)
		for i := range sized.Snapshot.Firmware {
			sized.Snapshot.Firmware[i].SnapshotManifest = sized.Snapshot.Firmware[i].SourceManifest
		}
		sized.Snapshot.ObservedAfter = cloneSnapshotObservation(&sized.Snapshot.ExpectedAfter)
		sized.Snapshot.NativeXMLSHA256 = strings.Repeat("0", 64)
		sized.RecoveryReservation = &protocol.PoolVMRecoveryReservation{
			OperationID: command.OperationID, Kind: "snapshot-create", PoolID: command.VM.PoolID,
			VMID: command.VM.ID, OwnerHostID: command.VM.HostID, Epoch: ^uint64(0),
			SpecRevision: command.VM.Revision, SpecSHA256: strings.Repeat("0", 64),
			DiskIdentitySHA256: strings.Repeat("0", 64), IntentSHA256: strings.Repeat("0", 64),
			OwnerOperationOrder: ^uint64(0),
		}
		sized.RecoveryStage, sized.RecoveryOutcome = "completed", "completed"
		sized.Disks, sized.DiskChains = snapshotProjection(*sized.Snapshot, true)
		// libvirt may expand device addresses and emit backingStore XML; reserve a
		// conservative 64 KiB above the observed definition before any side effects.
		sized.Domain = make([]byte, len(j.Domain)+(64<<10))
		if b, marshalErr := json.Marshal(sized); marshalErr != nil || len(b) > managedSnapshotJournalLimit {
			if marshalErr != nil {
				err = marshalErr
			} else {
				err = errors.New("snapshot recovery journal exceeds its durable record limit")
			}
			return e.failSnapshotPreflight(ctx, result, command.OperationID, err)
		}
		if err = e.saveSnapshotUnderLock(ctx, j, false); err != nil {
			return e.failSnapshotPreflight(ctx, result, command.OperationID, err)
		}
	} else if j.Snapshot == nil || validateManagedSnapshotRecord(command.OperationID, *j.Snapshot) != nil {
		return result, errors.New("snapshot journal intent is invalid")
	}
	if j.RecoveryStage == "completed" || j.RecoveryStage == "failed-before-effects" {
		if j.RecoveryStage == "completed" {
			_, raw, _, observed, observeErr := e.reconcileSnapshotHeads(ctx, j)
			match, digestErr := snapshotObservationDigestsMatch(observed, j.Snapshot.ExpectedAfter)
			if observeErr != nil || digestErr != nil || !match {
				return e.snapshotAttention(ctx, j, errors.Join(observeErr, digestErr, errors.New("completed snapshot heads cannot be re-observed")))
			}
			nativeHash, metadataErr := managedSnapshotXMLDigest(ctx, vmID, managedSnapshotNativeName(command.OperationID), *j.Snapshot)
			if metadataErr != nil || nativeHash != j.Snapshot.NativeXMLSHA256 {
				return e.snapshotAttention(ctx, j, errors.New("completed native snapshot metadata differs from its journal"))
			}
			if err := e.publishSnapshotCurrent(ctx, j, raw); err != nil {
				return e.snapshotAttention(ctx, j, err)
			}
		}
		return e.settleSnapshot(ctx, j, settle)
	}

	intentDigest, err := managedSnapshotIntentDigest(command, *j.Snapshot)
	if err != nil {
		return result, err
	}
	if j.RecoveryReservation == nil {
		reservation, reserveErr := reserve(ctx, command, intentDigest, j.Snapshot.CreatedAt)
		if reserveErr != nil {
			return e.abortSnapshotLocalIntent(ctx, j, reserveErr)
		}
		// Master reservation succeeded: never abort/delete the journal. A mismatched
		// reservation cannot be bound (readJournal would reject it); keep local-intent
		// and raise needs_attention so the operator reconciles the master hold.
		if err := validateManagedSnapshotReservation(command, *j.Snapshot, reservation); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
		j.RecoveryReservation, j.RecoveryStage, j.Stage = &reservation, "reserved", "snapshot-reserved"
		j.Result.Status, j.Result.Error = "pending", ""
		if err := e.saveSnapshotUnderLock(ctx, j, true); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
	} else if err := validateManagedSnapshotReservation(command, *j.Snapshot, *j.RecoveryReservation); err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	if j.RecoveryReservation.AdmissionRefusal != "" {
		if j.RecoveryStage != "reserved" {
			return e.snapshotAttention(ctx, j, errors.New("master admission refusal conflicts with the saved native snapshot stage"))
		}
		return e.failSnapshotBeforeEffects(ctx, j, settle, managedSnapshotAdmissionRefusal(j.RecoveryReservation.AdmissionRefusal))
	}

	_, currentRaw, _, observed, err := e.reconcileSnapshotHeads(ctx, j)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	beforeHash, beforeErr := snapshotObservationDigest(j.Snapshot.Before)
	afterHash, afterErr := snapshotObservationDigest(j.Snapshot.ExpectedAfter)
	observedHash, observedErr := snapshotObservationDigest(observed)
	if beforeErr != nil || afterErr != nil || observedErr != nil {
		return e.snapshotAttention(ctx, j, errors.Join(beforeErr, afterErr, observedErr))
	}
	present, presenceErr := managedSnapshotXMLDigest(ctx, vmID, managedSnapshotNativeName(command.OperationID), *j.Snapshot)
	absent, absenceErr := managedSnapshotAbsent(ctx, vmID, managedSnapshotNativeName(command.OperationID))
	outputErr := snapshotOutputHeadsAbsent(*j.Snapshot)
	metadataAbsent := absenceErr == nil && absent
	metadataPresent := presenceErr == nil
	action := managedSnapshotResumeAction(j.RecoveryStage, beforeHash == observedHash, afterHash == observedHash, metadataPresent, metadataAbsent, outputErr == nil)
	if action == "complete" {
		j.Snapshot.ObservedAfter = cloneSnapshotObservation(&observed)
		j.Snapshot.NativeXMLSHA256 = present
		j.Disks, j.DiskChains = snapshotProjection(*j.Snapshot, true)
		j.Domain = append([]byte(nil), currentRaw...)
		j.RecoveryStage, j.RecoveryOutcome, j.Stage = "completed", "completed", "snapshot-completed"
		j.Result.Status, j.Result.Error = "pending", ""
		if err = e.saveSnapshotUnderLock(ctx, j, true); err != nil {
			return result, err
		}
		if err = e.publishSnapshotCurrent(ctx, j, currentRaw); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
		return e.settleSnapshot(ctx, j, settle)
	}
	if action == "needs-attention" {
		if outputErr != nil {
			return e.snapshotAttention(ctx, j, outputErr)
		}
		if presenceErr != nil && absenceErr != nil {
			return e.snapshotAttention(ctx, j, errors.New("native snapshot metadata state cannot be established"))
		}
		if presenceErr == nil || !metadataAbsent {
			return e.snapshotAttention(ctx, j, errors.New("native snapshot metadata exists but disk state does not match its reserved result"))
		}
		return e.snapshotAttention(ctx, j, errors.New("VM disk state differs from both the before and expected snapshot projections"))
	}
	if action == "failed-before-effects" {
		if err = e.removeSnapshotFirmwareCopies(ctx, *j.Snapshot); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
		return e.failSnapshotBeforeEffects(ctx, j, settle, errors.New("interrupted native snapshot has no observable effects and was not replayed"))
	}
	if action != "create" {
		return e.snapshotAttention(ctx, j, errors.New("snapshot resume decision is not safe"))
	}
	if j.RecoveryStage != "native" {
		j.RecoveryStage, j.Stage = "native", "snapshot-native"
		j.Result.Status, j.Result.Error = "pending", ""
		if err = e.saveSnapshotUnderLock(ctx, j, true); err != nil {
			return result, err
		}
	}
	if err = e.ensureSnapshotFirmwareCopies(ctx, &j); err != nil {
		if cleanupErr := e.removeSnapshotFirmwareCopies(ctx, *j.Snapshot); cleanupErr != nil {
			return e.snapshotAttention(ctx, j, errors.Join(err, cleanupErr))
		}
		return e.failSnapshotBeforeEffects(ctx, j, settle, errors.New("VM firmware snapshot capture failed"))
	}
	if len(j.Snapshot.Firmware) > 0 {
		if err = e.saveSnapshotUnderLock(ctx, j, true); err != nil {
			return result, err
		}
	}
	if err = e.confirmSnapshotSources(ctx, j); err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	if err = e.runNativeSnapshot(ctx, j); err != nil {
		// The command crossed libvirt's boundary. Only an exact unchanged before-state
		// and absent native metadata prove that no snapshot effect occurred.
		_, _, _, latestObservation, observeErr := e.reconcileSnapshotHeads(ctx, j)
		stillAbsent, absentErr := managedSnapshotAbsent(ctx, vmID, managedSnapshotNativeName(command.OperationID))
		if observeErr == nil && absentErr == nil && stillAbsent {
			got, digestErr := snapshotObservationDigest(latestObservation)
			if digestErr == nil && got == beforeHash {
				if cleanupErr := e.removeSnapshotFirmwareCopies(ctx, *j.Snapshot); cleanupErr != nil {
					return e.snapshotAttention(ctx, j, errors.Join(err, cleanupErr))
				}
				return e.failSnapshotBeforeEffects(ctx, j, settle, err)
			}
			if digestErr != nil {
				return e.snapshotAttention(ctx, j, errors.Join(err, digestErr))
			}
		}
		blocked := errors.Join(observeErr, absentErr)
		if absentErr == nil && !stillAbsent {
			blocked = errors.Join(blocked, errors.New("native snapshot metadata is present after create failure"))
		}
		if blocked != nil {
			return e.snapshotAttention(ctx, j, errors.Join(err, blocked))
		}
		return e.snapshotAttention(ctx, j, err)
	}
	_, currentRaw, _, observed, err = e.reconcileSnapshotHeads(ctx, j)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	match, digestErr := snapshotObservationMatchesHash(observed, afterHash)
	if digestErr != nil || !match {
		if digestErr != nil {
			return e.snapshotAttention(ctx, j, digestErr)
		}
		return e.snapshotAttention(ctx, j, errors.New("native snapshot disk heads differ from the atomic expected projection"))
	}
	nativeHash, err := managedSnapshotXMLDigest(ctx, vmID, managedSnapshotNativeName(command.OperationID), *j.Snapshot)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	j.Snapshot.ObservedAfter = cloneSnapshotObservation(&observed)
	j.Snapshot.NativeXMLSHA256 = nativeHash
	j.Disks, j.DiskChains = snapshotProjection(*j.Snapshot, true)
	j.Domain = append([]byte(nil), currentRaw...)
	j.RecoveryStage, j.RecoveryOutcome, j.Stage = "completed", "completed", "snapshot-completed"
	j.Result.Status, j.Result.Error = "pending", ""
	if err = e.saveSnapshotUnderLock(ctx, j, true); err != nil {
		return result, err
	}
	if err = e.publishSnapshotCurrent(ctx, j, currentRaw); err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	return e.settleSnapshot(ctx, j, settle)
}

func (e *ManagedEngine) removePreflightSnapshot(ctx context.Context, operationID string) error {
	bounded, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(bounded)
	if err != nil {
		return err
	}
	defer unlock(global)
	path := filepath.Join(e.stateDir, operationID+".json")
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return syncManagedDir(e.stateDir)
	} else if err != nil {
		return err
	}
	j, err := e.readJournal(operationID + ".json")
	if err != nil || j.RecoveryStage != "preflight" || j.RecoveryReservation != nil {
		return errors.New("snapshot preflight fence is no longer safely removable")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncManagedDir(e.stateDir)
}

func (e *ManagedEngine) failSnapshotPreflight(ctx context.Context, result protocol.Result, operationID string, cause error) (protocol.Result, error) {
	if cleanupErr := e.removePreflightSnapshot(ctx, operationID); cleanupErr != nil {
		result.Status, result.Error = "needs_attention", errors.Join(cause, cleanupErr).Error()
	} else {
		result.Error = cause.Error()
	}
	return result, nil
}

// abortSnapshotLocalIntent removes a local-intent journal that never received a master
// reservation. Leaving needs_attention here permanently fences the VM; no native effects
// exist yet, so the operator can submit a new request after the refusal is reported.
func (e *ManagedEngine) abortSnapshotLocalIntent(ctx context.Context, j managedJournal, cause error) (protocol.Result, error) {
	result := protocol.Result{OperationID: j.Command.OperationID, Status: "failed", Error: cause.Error(), ObservedAt: time.Now().UTC()}
	if j.RecoveryReservation != nil || j.RecoverySettled || j.RecoveryStage != "local-intent" {
		return e.snapshotAttention(ctx, j, errors.Join(cause, errors.New("snapshot journal is not a removable local intent")))
	}
	if j.Command.Kind == "snapshot-create" {
		if j.Snapshot == nil {
			return e.snapshotAttention(ctx, j, errors.Join(cause, errors.New("snapshot local intent lacks its record")))
		}
		absent, err := managedSnapshotAbsent(ctx, j.Command.VM.ID, managedSnapshotNativeName(j.Command.OperationID))
		if err != nil || !absent {
			if err == nil {
				err = errors.New("native snapshot presence is ambiguous")
			}
			return e.snapshotAttention(ctx, j, errors.Join(cause, err))
		}
		if err := snapshotOutputHeadsAbsent(*j.Snapshot); err != nil {
			return e.snapshotAttention(ctx, j, errors.Join(cause, err))
		}
	}
	path := filepath.Join(e.stateDir, j.Command.OperationID+".json")
	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return e.snapshotAttention(ctx, j, errors.Join(cause, err))
	}
	defer unlock(global)
	latest, err := e.readJournal(j.Command.OperationID + ".json")
	if err != nil {
		return e.snapshotAttention(ctx, j, errors.Join(cause, err))
	}
	if latest.RecoveryReservation != nil || latest.RecoverySettled || latest.RecoveryStage != "local-intent" {
		return e.snapshotAttention(ctx, j, errors.Join(cause, errors.New("snapshot local intent changed before abort")))
	}
	if err := os.Remove(path); err != nil {
		return e.snapshotAttention(ctx, j, errors.Join(cause, err))
	}
	if err := syncManagedDir(e.stateDir); err != nil {
		result.Status, result.Error = "needs_attention", errors.Join(cause, err).Error()
		return result, nil
	}
	return result, nil
}

func snapshotProjection(record managedSnapshotRecord, after bool) ([]string, [][]string) {
	observation := record.Before
	if after {
		observation = record.ExpectedAfter
	}
	disks := make([]string, len(observation.Disks))
	chains := make([][]string, len(observation.Disks))
	for i, disk := range observation.Disks {
		disks[i] = disk.Source
		chains[i] = append([]string(nil), disk.Layers...)
	}
	return disks, chains
}

func snapshotOutputHeadsAbsent(record managedSnapshotRecord) error {
	for _, disk := range record.ExpectedAfter.Disks {
		if _, err := os.Lstat(disk.Source); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				return errors.New("snapshot output disk exists without matching native metadata; inspect it on the host")
			}
			return err
		}
	}
	return nil
}

func managedSnapshotResumeAction(stage string, before, after, metadataPresent, metadataAbsent, outputHeadsAbsent bool) string {
	if after && metadataPresent {
		return "complete"
	}
	if before && metadataAbsent && outputHeadsAbsent {
		switch stage {
		case "reserved":
			return "create"
		case "native":
			return "failed-before-effects"
		}
	}
	return "needs-attention"
}

func cloneSnapshotObservation(observation *managedSnapshotObservation) *managedSnapshotObservation {
	if observation == nil {
		return nil
	}
	clone := *observation
	clone.Disks = make([]managedSnapshotDiskObservation, len(observation.Disks))
	for i, disk := range observation.Disks {
		clone.Disks[i] = disk
		clone.Disks[i].Layers = append([]string(nil), disk.Layers...)
	}
	return &clone
}

func cloneManagedSnapshotRecord(record *managedSnapshotRecord) *managedSnapshotRecord {
	if record == nil {
		return nil
	}
	clone := *record
	clone.Before = *cloneSnapshotObservation(&record.Before)
	clone.ExpectedAfter = *cloneSnapshotObservation(&record.ExpectedAfter)
	clone.ObservedAfter = cloneSnapshotObservation(record.ObservedAfter)
	clone.Firmware = append([]managedSnapshotFirmwareArtifact(nil), record.Firmware...)
	clone.StorageDependencies = append([]managedSnapshotStorageDependency(nil), record.StorageDependencies...)
	clone.Dependencies = cloneSnapshotDependencies(record.Dependencies)
	for i := range clone.Firmware {
		clone.Firmware[i].SourceManifest = cloneManagedSnapshotManifest(record.Firmware[i].SourceManifest)
		clone.Firmware[i].SnapshotManifest = cloneManagedSnapshotManifest(record.Firmware[i].SnapshotManifest)
	}
	return &clone
}

func cloneSnapshotDependencies(value *protocol.PoolVMSnapshotDependencies) *protocol.PoolVMSnapshotDependencies {
	if value == nil {
		return nil
	}
	clone := *value
	clone.StorageIDs = append([]string{}, value.StorageIDs...)
	clone.NetworkIDs = append([]string{}, value.NetworkIDs...)
	clone.MediaIDs = append([]string{}, value.MediaIDs...)
	return &clone
}

func cloneManagedSnapshotManifest(manifest managedSnapshotFileManifest) managedSnapshotFileManifest {
	manifest.Files = append([]managedSnapshotFileMetadata(nil), manifest.Files...)
	return manifest
}

func (e *ManagedEngine) saveSnapshotUnderLock(ctx context.Context, j managedJournal, checkFence bool) error {
	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(global)
	js, err := e.journals()
	if err != nil {
		return err
	}
	old, exists := managedSnapshotPoints(js, j.Command.OperationID)
	if !exists || !reflect.DeepEqual(old.Command, j.Command) || old.RecoverySettled {
		return errors.New("snapshot journal changed during operation")
	}
	if checkFence {
		if err = recoveryReservationRefusal(js, j.Command); err != nil {
			return err
		}
	}
	return e.saveSnapshotJournal(j)
}

func (e *ManagedEngine) reconcileSnapshotHeads(ctx context.Context, j managedJournal) (managedJournal, []byte, managedDomain, managedSnapshotObservation, error) {
	js, err := e.journals()
	if err != nil {
		return managedJournal{}, nil, managedDomain{}, managedSnapshotObservation{}, err
	}
	current, err := managedCurrent(js, j.Command.VM.ID)
	if err != nil || !reflect.DeepEqual(current.Command.VM, j.Command.VM) {
		return managedJournal{}, nil, managedDomain{}, managedSnapshotObservation{}, errors.New("managed VM definition changed during snapshot recovery")
	}
	// Use the before or after projection only to validate whichever exact head state is
	// currently visible; a third projection remains ambiguous and is never native-mutated.
	for _, projection := range []managedSnapshotObservation{j.Snapshot.Before, j.Snapshot.ExpectedAfter} {
		candidate := current
		candidate.Disks = make([]string, len(projection.Disks))
		candidate.DiskChains = make([][]string, len(projection.Disks))
		candidate.Domain = append([]byte(nil), current.Domain...)
		for i, disk := range projection.Disks {
			candidate.Disks[i], candidate.DiskChains[i] = disk.Source, append([]string(nil), disk.Layers...)
		}
		dumpArgs := []string{"dumpxml", j.Command.VM.ID}
		mode := ""
		if j.Command.Snapshot != nil {
			mode = j.Command.Snapshot.Mode
		}
		if !protocol.SnapshotModeRunning(mode) {
			dumpArgs = append(dumpArgs, "--inactive")
		}
		raw, dumpErr := managedVirsh(ctx, dumpArgs...)
		if dumpErr != nil {
			return managedJournal{}, nil, managedDomain{}, managedSnapshotObservation{}, errors.New("persistent VM definition cannot be read during snapshot reconciliation")
		}
		var domain managedDomain
		if xml.Unmarshal(raw, &domain) != nil || verifyManagedDiskHeads(domain, candidate.Targets, candidate.Disks) != nil {
			continue
		}
		candidate.Domain = append([]byte(nil), raw...)
		observation, observedDomain, observedRaw, observeErr := e.observeManagedSnapshot(ctx, js, candidate, mode)
		if observeErr == nil && observation.DefinitionSHA256 == projection.DefinitionSHA256 {
			return candidate, observedRaw, observedDomain, observation, nil
		}
	}
	return managedJournal{}, nil, managedDomain{}, managedSnapshotObservation{}, errors.New("VM disk state differs from both the before and expected snapshot projections")
}

func (e *ManagedEngine) confirmSnapshotSources(ctx context.Context, j managedJournal) error {
	if err := snapshotOutputHeadsAbsent(*j.Snapshot); err != nil {
		return err
	}
	_, _, _, observation, err := e.reconcileSnapshotHeads(ctx, j)
	if err != nil {
		return err
	}
	before, err := snapshotObservationDigest(j.Snapshot.Before)
	if err != nil {
		return err
	}
	got, err := snapshotObservationDigest(observation)
	if err != nil {
		return err
	}
	if before != got {
		return errors.New("VM disk state changed immediately before native snapshot creation")
	}
	for _, artifact := range j.Snapshot.Firmware {
		manifest, err := managedSnapshotManifest(ctx, artifact.SourcePath)
		if err != nil || !snapshotManifestsEqual(manifest, artifact.SourceManifest) {
			return errors.New("VM firmware state changed immediately before native snapshot creation")
		}
	}
	return nil
}

func (e *ManagedEngine) ensureSnapshotFirmwareCopies(ctx context.Context, j *managedJournal) error {
	for i := range j.Snapshot.Firmware {
		artifact := &j.Snapshot.Firmware[i]
		manifest, err := managedSnapshotManifest(ctx, artifact.SnapshotPath)
		if err != nil {
			return err
		}
		if manifest.Exists {
			if !snapshotManifestsEqual(manifest, artifact.SourceManifest) {
				return errors.New("existing firmware snapshot copy differs from its source")
			}
			artifact.SnapshotManifest = manifest
			continue
		}
		copy, err := copyManagedSnapshotFirmware(ctx, artifact.SourcePath, artifact.SnapshotPath)
		if err != nil || !snapshotManifestsEqual(copy, artifact.SourceManifest) {
			if err != nil {
				return err
			}
			return errors.New("firmware snapshot copy differs from its source")
		}
		artifact.SnapshotManifest = copy
	}
	return nil
}

func (e *ManagedEngine) removeSnapshotFirmwareCopies(ctx context.Context, record managedSnapshotRecord) error {
	for _, artifact := range record.Firmware {
		manifest, err := managedSnapshotManifest(ctx, artifact.SnapshotPath)
		if err != nil {
			return err
		}
		if !manifest.Exists {
			continue
		}
		if !snapshotManifestsEqual(manifest, artifact.SourceManifest) {
			return errors.New("snapshot firmware artifact differs from its reserved source")
		}
		if err = os.RemoveAll(artifact.SnapshotPath); err != nil {
			return err
		}
		if err = syncManagedDir(filepath.Dir(artifact.SnapshotPath)); err != nil {
			return err
		}
	}
	return nil
}

func (e *ManagedEngine) runNativeSnapshot(ctx context.Context, j managedJournal) error {
	args := []string{"snapshot-create-as", j.Command.VM.ID, managedSnapshotNativeName(j.Command.OperationID)}
	if j.Command.Snapshot.Description != "" {
		args = append(args, "--description", j.Command.Snapshot.Description)
	}
	args = append(args, "--disk-only", "--atomic")
	if j.Command.Snapshot.Mode == "quiesced" {
		args = append(args, "--quiesce")
	}
	for _, disk := range j.Snapshot.ExpectedAfter.Disks {
		args = append(args, "--diskspec", disk.Target+",snapshot=external,file="+disk.Source)
	}
	writable := make(map[string]bool, len(j.Snapshot.ExpectedAfter.Disks))
	for _, disk := range j.Snapshot.ExpectedAfter.Disks {
		writable[disk.Target] = true
	}
	for _, target := range snapshotReadonlyDiskTargets(j.Snapshot.BeforeDomain, writable) {
		args = append(args, "--diskspec", target+",snapshot=no")
	}
	_, err := managedVirsh(ctx, args...)
	return err
}

// snapshotReadonlyDiskTargets returns CD-ROM and other non-writable disk targets from the
// captured before-domain so native create forces snapshot=no for them.
func snapshotReadonlyDiskTargets(beforeDomain []byte, writable map[string]bool) []string {
	var domain managedDomain
	if xml.Unmarshal(beforeDomain, &domain) != nil {
		return nil
	}
	targets := []string{}
	seen := map[string]bool{}
	for _, disk := range domain.Devices.Disks {
		if disk.Target.Dev == "" || seen[disk.Target.Dev] || writable[disk.Target.Dev] ||
			(disk.Readonly == nil && disk.Device != "cdrom") {
			continue
		}
		seen[disk.Target.Dev] = true
		targets = append(targets, disk.Target.Dev)
	}
	return targets
}

func snapshotObservationDigestsMatch(left, right managedSnapshotObservation) (bool, error) {
	leftDigest, err := snapshotObservationDigest(left)
	if err != nil {
		return false, err
	}
	rightDigest, err := snapshotObservationDigest(right)
	if err != nil {
		return false, err
	}
	return leftDigest == rightDigest, nil
}

func snapshotObservationMatchesHash(observation managedSnapshotObservation, want string) (bool, error) {
	got, err := snapshotObservationDigest(observation)
	if err != nil {
		return false, err
	}
	return got == want, nil
}

func (e *ManagedEngine) publishSnapshotCurrent(ctx context.Context, j managedJournal, raw []byte) error {
	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(global)
	js, err := e.journals()
	if err != nil {
		return err
	}
	if err = recoveryReservationRefusal(js, j.Command); err != nil {
		return err
	}
	return e.updateSnapshotCurrentProjection(ctx, j, raw)
}

func (e *ManagedEngine) snapshotAttention(ctx context.Context, j managedJournal, cause error) (protocol.Result, error) {
	result := protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: cause.Error(), ObservedAt: time.Now().UTC()}
	if j.RecoveryStage == "completed" || j.RecoveryStage == "failed-before-effects" {
		// Terminal journal bytes are the master's settlement receipt identity. Keep them
		// immutable until an exact settlement ACK; changing even Result.Error would make
		// a lost-reply retry hash to a different receipt.
		return result, nil
	}
	j.Result = result
	bounded, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ctx.Err() == nil {
		bounded = ctx
	}
	if err := e.saveSnapshotUnderLock(bounded, j, j.RecoveryReservation != nil); err != nil {
		return j.Result, errors.Join(cause, err)
	}
	return j.Result, nil
}

func (e *ManagedEngine) failSnapshotBeforeEffects(ctx context.Context, j managedJournal, settle func(context.Context, protocol.PoolVMRecoveryEvidence) error, cause error) (protocol.Result, error) {
	absent, err := managedSnapshotAbsent(ctx, j.Command.VM.ID, managedSnapshotNativeName(j.Command.OperationID))
	if err != nil || !absent {
		if err == nil {
			err = errors.New("native snapshot presence is ambiguous")
		}
		return e.snapshotAttention(ctx, j, err)
	}
	if err := snapshotOutputHeadsAbsent(*j.Snapshot); err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	for _, artifact := range j.Snapshot.Firmware {
		manifest, err := managedSnapshotManifest(ctx, artifact.SnapshotPath)
		if err != nil || manifest.Exists {
			return e.snapshotAttention(ctx, j, errors.New("failed snapshot left firmware artifacts that require host reconciliation"))
		}
	}
	_, _, _, observation, err := e.reconcileSnapshotHeads(ctx, j)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	match, digestErr := snapshotObservationDigestsMatch(observation, j.Snapshot.Before)
	if digestErr != nil || !match {
		return e.snapshotAttention(ctx, j, errors.Join(digestErr, errors.New("VM disk state is not unchanged after failed snapshot creation")))
	}
	j.Disks, j.DiskChains = snapshotProjection(*j.Snapshot, false)
	j.Snapshot.ObservedAfter = nil
	j.Snapshot.NativeXMLSHA256 = ""
	j.RecoveryStage, j.RecoveryOutcome, j.Stage = "failed-before-effects", "failed-before-effects", "snapshot-failed-before-effects"
	j.Result.Status, j.Result.Error = "pending", cause.Error()
	if err := e.saveSnapshotUnderLock(ctx, j, true); err != nil {
		return j.Result, err
	}
	return e.settleSnapshot(ctx, j, settle)
}

func (e *ManagedEngine) settleSnapshot(ctx context.Context, j managedJournal, settle func(context.Context, protocol.PoolVMRecoveryEvidence) error) (protocol.Result, error) {
	if j.RecoveryReservation == nil || j.RecoveryStage != "completed" && j.RecoveryStage != "failed-before-effects" {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot has no terminal master reservation"}, nil
	}
	evidence, err := e.PoolVMRecoveryEvidence(ctx, *j.RecoveryReservation)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	return e.settleSnapshotEvidence(ctx, j, evidence, settle)
}

func (e *ManagedEngine) settleSnapshotEvidence(ctx context.Context, j managedJournal, evidence protocol.PoolVMRecoveryEvidence, settle func(context.Context, protocol.PoolVMRecoveryEvidence) error) (protocol.Result, error) {
	if j.RecoveryReservation == nil || settle == nil || evidence.Reservation != *j.RecoveryReservation || evidence.Outcome != j.RecoveryOutcome || evidence.Valid() != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot settlement evidence does not match its journal"}, nil
	}
	raw, err := readSmall(filepath.Join(e.stateDir, j.Command.OperationID+".json"), managedSnapshotJournalLimit)
	digest := sha256.Sum256(raw)
	if err != nil || hex.EncodeToString(digest[:]) != evidence.JournalSHA256 {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot settlement receipt is not for the current immutable journal"}, nil
	}
	if err := settle(ctx, evidence); err != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "master snapshot settlement is not acknowledged"}, nil
	}
	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: err.Error()}, nil
	}
	defer unlock(global)
	js, err := e.journals()
	if err != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: err.Error()}, nil
	}
	latest, ok := managedSnapshotPoints(js, j.Command.OperationID)
	if !ok || latest.RecoveryReservation == nil || *latest.RecoveryReservation != *j.RecoveryReservation || latest.RecoveryStage != j.RecoveryStage {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot journal changed before settlement acknowledgement"}, nil
	}
	latest.RecoverySettled = true
	latest.Result.Status = "succeeded"
	if latest.RecoveryOutcome == "failed-before-effects" {
		latest.Result.Status = "failed"
	} else {
		latest.Result.Error = ""
	}
	latest.Result.ObservedAt = time.Now().UTC()
	if err = e.saveSnapshotJournal(latest); err != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: err.Error()}, nil
	}
	return latest.Result, nil
}
