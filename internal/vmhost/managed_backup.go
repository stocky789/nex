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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

const (
	backupOwnerMarkerName  = "owner.json"
	backupManifestName     = "manifest.json"
	backupStagingPrefix    = ".staging-"
	maxBackupManifestBytes = 1 << 20
	maxBackupDiskArtifacts = 32
	backupNFSHardeningOpts = ",nosuid,nodev,noexec"
)

var ErrBackupPublishUncertain = errors.New("backup publication outcome is uncertain")

type backupRepositoryOwner struct {
	Repository string `json:"repository"`
	Pool       string `json:"pool"`
}

// prepareBackupSMB claims and probes the repository inside SMB preparation, so a failed first
// registration removes its new mount unit. Backup repositories hold only data files.
var prepareBackupSMB = func(ctx context.Context, registration protocol.StorageRegistration, repositoryID string) (storageAccess, error) {
	return prepareSMBWithAdmission(ctx, registration, backupNFSHardeningOpts, func(root string) error {
		if err := claimBackupRepositoryOwnerExact(root, repositoryID, registration.PoolID); err != nil {
			return err
		}
		return probeBackupPublication(root)
	})
}
var inspectBackupSMB = inspectSMB
var probeBackupQEMUAccess = qemuStorageAccess
var probeBackupPublication = probeBackupFilesystemPublication

// nfsRootSquash explains the one refusal an operator cannot otherwise diagnose: Core writes as
// root, so an export that squashes root denies every write.
func nfsRootSquash(err error) error {
	if err != nil && (errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), "permission denied")) {
		return errors.New("the NFS export denied access to the repository directory: root_squash is not supported, export it with no_root_squash (" + err.Error() + ")")
	}
	return err
}

// allowBackupNFSHardeningRemount lets Prepare rewrite a pre-hardening NFS backup unit when the
// only Options change is adding nosuid,nodev,noexec. Other mount changes still require the
// dependency-safe remount path.
func allowBackupNFSHardeningRemount(ctx context.Context, s protocol.StorageRegistration) context.Context {
	if s.Source.NFS == nil {
		return ctx
	}
	_, unitPath := remoteUnitPath(s)
	old, err := os.ReadFile(unitPath)
	if err != nil {
		return ctx
	}
	source := s.Source.NFS.Server + ":" + s.Source.NFS.Export
	path := remoteMountPath(s)
	legacy := remoteUnit(source, path, "nfs4", "vers=4,hard,_netdev")
	if string(old) != string(legacy) {
		return ctx
	}
	return allowStorageRemount(ctx)
}

func backupStorageRegistration(repo protocol.BackupRepository) protocol.StorageRegistration {
	sum := sha256.Sum256([]byte(repo.ID))
	registration := protocol.StorageRegistration{
		ID: "backup-" + hex.EncodeToString(sum[:16]), PoolID: repo.PoolID,
		Name: repo.Name, Kind: protocol.StorageKindNFS,
		Source: protocol.StorageSource{NFS: &repo.Source},
	}
	if repo.Kind == protocol.BackupRepoKindSMB3 {
		registration.Kind = protocol.StorageKindSMB
		registration.Source = protocol.StorageSource{SMB: repo.SMBSource}
		registration.Identity.SMB = repo.SMBIdentity
		registration.CredentialReceipt = repo.CredentialRef
	} else if repo.Identity != nil {
		registration.Identity.NFS = repo.Identity
	}
	return registration
}

// PrepareBackupRepository mounts the registered filesystem source and claims
// only the selected data directory for this pool.
func PrepareBackupRepository(ctx context.Context, repo protocol.BackupRepository) (protocol.BackupRepository, error) {
	if err := repo.Valid(); err != nil {
		return protocol.BackupRepository{}, err
	}
	registration := backupStorageRegistration(repo)
	var access storageAccess
	var err error
	switch repo.Kind {
	case protocol.BackupRepoKindNFS:
		ctx = allowBackupNFSHardeningRemount(ctx, registration)
		access, err = prepareNFS(ctx, registration, backupNFSHardeningOpts)
		err = nfsRootSquash(err)
	case protocol.BackupRepoKindSMB3:
		if repo.CredentialRef == "" {
			return protocol.BackupRepository{}, errors.New("backup repository SMB credentials are unavailable")
		}
		root := filepath.Join(remoteMountPath(registration), repo.SMBSource.Directory)
		if err := checkExistingBackupRepositoryOwner(root, repo.ID, repo.PoolID); err != nil {
			return protocol.BackupRepository{}, err
		}
		access, err = prepareBackupSMB(ctx, registration, repo.ID)
	default:
		return protocol.BackupRepository{}, errors.New("backup repository kind is unsupported")
	}
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	if repo.MountPath != "" && repo.MountPath != access.Path {
		return protocol.BackupRepository{}, errors.New("backup repository mount path differs from Core's mounted source")
	}
	repo.MountPath = access.Path
	if repo.Kind == protocol.BackupRepoKindSMB3 {
		repo.SMBIdentity = access.Observed.SMB
		if repo.SMBIdentity == nil || repo.SMBIdentity.MarkerID == "" {
			return protocol.BackupRepository{}, errors.New("backup repository SMB identity is incomplete")
		}
	} else {
		repo.Identity = access.Observed.NFS
	}
	if err := claimBackupRepositoryOwnerExact(repo.MountPath, repo.ID, repo.PoolID); err != nil {
		if repo.Kind == protocol.BackupRepoKindNFS {
			err = nfsRootSquash(err)
		}
		return protocol.BackupRepository{}, err
	}
	return repo, nil
}

// DetachBackupRepository stops and removes the mount of a filesystem backup
// repository. The repository's data on the share is left untouched.
func DetachBackupRepository(ctx context.Context, repo protocol.BackupRepository) error {
	if repo.Kind != protocol.BackupRepoKindNFS && repo.Kind != protocol.BackupRepoKindSMB3 {
		return nil
	}
	registration := backupStorageRegistration(repo)
	if err := detachRemote(ctx, registration); err != nil {
		return err
	}
	// The unit is gone, so the mountpoint is an empty directory; a non-empty one is left alone.
	_ = os.Remove(remoteMountPath(registration))
	if repo.Kind == protocol.BackupRepoKindSMB3 {
		return RemoveBackupSMBCredential(repo.CredentialRef)
	}
	return nil
}

func checkExistingBackupRepositoryOwner(root, repositoryID, poolID string) error {
	ownerRepo, ownerPool, err := ReadBackupRepositoryOwner(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || ownerRepo != repositoryID || ownerPool != poolID {
		return errors.New("backup repository is owned by another pool")
	}
	return nil
}

func claimBackupRepositoryOwnerExact(root, repositoryID, poolID string) error {
	if err := ClaimBackupRepositoryOwner(root, repositoryID, poolID); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		return checkExistingBackupRepositoryOwner(root, repositoryID, poolID)
	}
	return nil
}

// VerifyBackupRepository proves the mounted source is writable for publication.
func VerifyBackupRepository(ctx context.Context, repo protocol.BackupRepository) error {
	return verifyBackupRepository(ctx, repo, true)
}

// VerifyBackupRepositoryRead proves the mounted source and owner for read access.
func VerifyBackupRepositoryRead(ctx context.Context, repo protocol.BackupRepository) error {
	return verifyBackupRepository(ctx, repo, false)
}

func verifyBackupRepository(ctx context.Context, repo protocol.BackupRepository, writable bool) error {
	if err := repo.Valid(); err != nil || repo.MountPath == "" {
		return errors.New("backup repository mount identity is incomplete")
	}
	registration := backupStorageRegistration(repo)
	var access storageAccess
	var err error
	switch repo.Kind {
	case protocol.BackupRepoKindNFS:
		if repo.Identity == nil {
			return errors.New("backup repository mount identity is incomplete")
		}
		access, err = inspectNFS(ctx, registration)
	case protocol.BackupRepoKindSMB3:
		if repo.SMBIdentity == nil || repo.SMBIdentity.MarkerID == "" || repo.CredentialRef == "" {
			return errors.New("backup repository SMB identity is incomplete")
		}
		access, err = inspectBackupSMB(ctx, registration)
	default:
		return errors.New("backup repository kind is unsupported")
	}
	if err != nil {
		return err
	}
	if access.Path != repo.MountPath {
		return errors.New("backup repository mount path changed")
	}
	ownerRepo, ownerPool, err := ReadBackupRepositoryOwner(repo.MountPath)
	if err != nil || ownerRepo != repo.ID || ownerPool != repo.PoolID {
		return errors.New("backup repository owner marker does not match")
	}
	if repo.Kind == protocol.BackupRepoKindSMB3 {
		observed := access.Observed.SMB
		if observed == nil || observed.Server != repo.SMBIdentity.Server || observed.Share != repo.SMBIdentity.Share || observed.MarkerID != repo.SMBIdentity.MarkerID {
			return errors.New("backup repository SMB identity changed")
		}
		if writable {
			if err := probeBackupQEMUAccess(ctx, repo.MountPath); err != nil {
				return err
			}
			if err := probeBackupPublication(repo.MountPath); err != nil {
				return err
			}
		}
	} else if access.Observed.NFS == nil || *access.Observed.NFS != *repo.Identity {
		return errors.New("backup repository NFS identity changed")
	}
	return nil
}

// ClaimBackupRepositoryOwner creates the durable NFS owner marker with
// O_CREAT|O_EXCL|O_NOFOLLOW, then syncs the marker and parent directory.
func ClaimBackupRepositoryOwner(repoRoot, repositoryID, poolID string) error {
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot ||
		!protocol.ValidSnapshotOperationID(repositoryID) || poolID == "" {
		return errors.New("backup repository owner claim is invalid")
	}
	if err := os.MkdirAll(repoRoot, 0755); err != nil {
		return err
	}
	path := filepath.Join(repoRoot, backupOwnerMarkerName)
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0644)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	payload, err := json.Marshal(backupRepositoryOwner{Repository: repositoryID, Pool: poolID})
	if err != nil {
		return err
	}
	if _, err = f.Write(payload); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return syncManagedDir(repoRoot)
}

// ReadBackupRepositoryOwner returns the durable owner marker for a repository root.
func ReadBackupRepositoryOwner(repoRoot string) (repositoryID, poolID string, err error) {
	path := filepath.Join(repoRoot, backupOwnerMarkerName)
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", "", err
	}
	var owner backupRepositoryOwner
	if err := json.Unmarshal(raw, &owner); err != nil || !protocol.ValidSnapshotOperationID(owner.Repository) || owner.Pool == "" {
		return "", "", errors.New("backup repository owner marker is corrupt")
	}
	return owner.Repository, owner.Pool, nil
}

// ConvertStoppedDiskStandalone runs qemu-img convert to a standalone qcow2 with no backing link.
func ConvertStoppedDiskStandalone(ctx context.Context, source, destination string) error {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source ||
		!filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return errors.New("backup disk conversion paths are invalid")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("backup disk destination already exists")
		}
		return err
	}
	if _, err := run(ctx, "qemu-img", "convert", "-O", "qcow2", source, destination); err != nil {
		return err
	}
	chain, err := observeManagedDiskChain(ctx, destination)
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	if len(chain) != 1 || chain[0] != destination {
		_ = os.Remove(destination)
		return errors.New("converted backup image retained a backing link")
	}
	if _, err = run(ctx, "qemu-img", "check", destination); err != nil {
		_ = os.Remove(destination)
		return errors.New("converted backup image failed qemu-img check")
	}
	return nil
}

// PublishBackupRecoveryPoint writes artifacts into a unique staging directory, validates the
// complete manifest, then publishes with sync → rename → sync parent. Incomplete points are never published.
func PublishBackupRecoveryPoint(repoRoot string, manifest protocol.BackupManifest, stageFiles map[string]string) (manifestSHA256 string, err error) {
	if err := manifest.Valid(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return "", errors.New("backup repository root is invalid")
	}
	ownerRepo, ownerPool, err := ReadBackupRepositoryOwner(repoRoot)
	if err != nil || ownerRepo != manifest.RepositoryID || ownerPool != manifest.PoolID {
		return "", errors.New("backup repository owner does not match the manifest")
	}
	published := filepath.Join(repoRoot, manifest.RecoveryPointID)
	if info, statErr := os.Lstat(published); !errors.Is(statErr, os.ErrNotExist) {
		if statErr != nil {
			return "", statErr
		}
		if !info.IsDir() {
			return "", errors.New("backup recovery point already exists")
		}
		// Exact same-key retry after a published point: settle from the existing
		// complete manifest without re-staging. A corrupt or incomplete directory
		// stays a hard conflict so operators inspect before overwrite.
		existing, digest, readErr := ReadBackupManifest(published)
		if readErr != nil {
			return "", errors.New("backup recovery point already exists")
		}
		if existing.RepositoryID != manifest.RepositoryID || existing.PoolID != manifest.PoolID ||
			existing.SourceVMID != manifest.SourceVMID || existing.SourceVMName != manifest.SourceVMName ||
			existing.Consistency != manifest.Consistency || existing.SpecSHA256 != manifest.SpecSHA256 ||
			existing.DiskIdentitySHA256 != manifest.DiskIdentitySHA256 ||
			(existing.CapturedSpec == nil) != (manifest.CapturedSpec == nil) ||
			!slices.Equal(existing.Disks, manifest.Disks) || !slices.Equal(existing.Firmware, manifest.Firmware) {
			return "", errors.New("backup recovery point already exists")
		}
		if err := validatePublishedBackupPoint(published, existing); err != nil {
			return "", err
		}
		return digest, nil
	}
	staging := filepath.Join(repoRoot, backupStagingPrefix+manifest.RecoveryPointID)
	if err := os.RemoveAll(staging); err != nil {
		return "", err
	}
	if err := os.Mkdir(staging, 0755); err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(staging)
		}
	}()

	required := map[string]bool{backupManifestName: true}
	for _, disk := range manifest.Disks {
		required[disk.Filename] = true
	}
	for _, fw := range manifest.Firmware {
		required[fw.Filename] = true
	}
	if len(stageFiles) != len(required)-1 { // manifest written separately below
		return "", errors.New("backup staging file set does not match the manifest")
	}
	for name, source := range stageFiles {
		if name == backupManifestName || !required[name] || !safeBackupStageName(name) {
			return "", errors.New("backup staging filename is unsafe or unexpected")
		}
		if err := copyRegularFileExclusive(source, filepath.Join(staging, name)); err != nil {
			return "", err
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if len(raw) > maxBackupManifestBytes {
		return "", errors.New("backup manifest exceeds size limit")
	}
	if err := AtomicWrite(filepath.Join(staging, backupManifestName), raw, 0644); err != nil {
		return "", err
	}
	if err := validateBackupStaging(staging, manifest); err != nil {
		return "", err
	}
	if err := syncManagedDir(staging); err != nil {
		return "", err
	}
	if err := os.Rename(staging, published); err != nil {
		return "", errors.Join(ErrBackupPublishUncertain, err)
	}
	if err := syncManagedDir(repoRoot); err != nil {
		return "", errors.Join(ErrBackupPublishUncertain, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func safeBackupStageName(name string) bool {
	return name != "" && name != backupManifestName && !strings.Contains(name, "/") &&
		!strings.Contains(name, `\`) && !strings.ContainsRune(name, '\x00') &&
		filepath.Clean(name) == name && name != ".."
}

func copyRegularFileExclusive(source, destination string) (err error) {
	in, err := openRegularNoFollow(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("backup source is not a regular file")
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	defer func() {
		if err != nil {
			_ = os.Remove(destination)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

// openRegularNoFollow opens a path without following a final-component symlink, matching
// the backup owner marker's O_NOFOLLOW posture for artifact copy and digests.
func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func validateBackupStaging(staging string, manifest protocol.BackupManifest) error {
	raw, err := readBackupManifestBytes(filepath.Join(staging, backupManifestName))
	if err != nil {
		return errors.New("backup staging manifest is unreadable")
	}
	var stored protocol.BackupManifest
	if err := json.Unmarshal(raw, &stored); err != nil || stored.Valid() != nil {
		return errors.New("backup staging manifest is corrupt or incomplete")
	}
	if stored.RecoveryPointID != manifest.RecoveryPointID || stored.RepositoryID != manifest.RepositoryID ||
		stored.Success != 1 || !stored.Complete {
		return errors.New("backup staging manifest does not match the publication intent")
	}
	for _, disk := range stored.Disks {
		path := filepath.Join(staging, disk.Filename)
		sum, size, err := fileSHA256(path)
		if err != nil || sum != disk.SHA256 || size != disk.Size {
			return errors.New("backup staging disk digest mismatch")
		}
	}
	for _, fw := range stored.Firmware {
		path := filepath.Join(staging, fw.Filename)
		sum, size, err := fileSHA256(path)
		if err != nil || sum != fw.SHA256 || size != fw.Size {
			return errors.New("backup staging firmware digest mismatch")
		}
	}
	return nil
}

// ReadBackupManifest loads and validates a published recovery-point manifest.
// The directory basename must equal RecoveryPointID; a renamed or relocated directory is refused.
func ReadBackupManifest(pointDir string) (protocol.BackupManifest, string, error) {
	path := filepath.Join(pointDir, backupManifestName)
	raw, err := readBackupManifestBytes(path)
	if err != nil {
		return protocol.BackupManifest{}, "", err
	}
	var manifest protocol.BackupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return protocol.BackupManifest{}, "", errors.New("backup manifest is corrupt")
	}
	if err := manifest.Valid(); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if filepath.Base(filepath.Clean(pointDir)) != manifest.RecoveryPointID {
		return protocol.BackupManifest{}, "", errors.New("backup recovery point directory does not match the manifest id")
	}
	sum := sha256.Sum256(raw)
	return manifest, hex.EncodeToString(sum[:]), nil
}

func readBackupManifestBytes(path string) ([]byte, error) {
	f, err := openRegularNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("backup manifest is not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBackupManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBackupManifestBytes {
		return nil, errors.New("backup manifest exceeds size limit")
	}
	return raw, nil
}

// CopyBackupArtifactsLocal copies published recovery-point files into a local directory
// for restore. Restore never opens NFS exports in place as VM disks.
func CopyBackupArtifactsLocal(ctx context.Context, pointDir, localDir string, manifest protocol.BackupManifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := manifest.Valid(); err != nil {
		return err
	}
	if filepath.Base(filepath.Clean(pointDir)) != manifest.RecoveryPointID {
		return errors.New("backup recovery point directory does not match the manifest id")
	}
	if err := os.MkdirAll(localDir, 0700); err != nil {
		return err
	}
	copyOne := func(name, wantSHA string, wantSize int64) error {
		source := filepath.Join(pointDir, name)
		destination := filepath.Join(localDir, name)
		if _, err := os.Lstat(destination); err == nil {
			if sum, size, checkErr := fileSHA256(destination); checkErr == nil && sum == wantSHA && size == wantSize {
				return nil
			}
			if err := os.Remove(destination); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return copyRegularFileExclusive(source, destination)
	}
	for _, disk := range manifest.Disks {
		if err := copyOne(disk.Filename, disk.SHA256, disk.Size); err != nil {
			return err
		}
		local := filepath.Join(localDir, disk.Filename)
		sum, size, err := fileSHA256(local)
		if err != nil || sum != disk.SHA256 || size != disk.Size {
			return errors.New("local restore copy digest mismatch")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	for _, fw := range manifest.Firmware {
		if err := copyOne(fw.Filename, fw.SHA256, fw.Size); err != nil {
			return err
		}
		sum, size, err := fileSHA256(filepath.Join(localDir, fw.Filename))
		if err != nil || sum != fw.SHA256 || size != fw.Size {
			return errors.New("local restore firmware digest mismatch")
		}
	}
	return nil
}

// ValidateBackupFirmwareForRestore requires a complete, supported firmware payload.
func ValidateBackupFirmwareForRestore(manifest protocol.BackupManifest) error {
	if manifest.CapturedSpec == nil {
		return errors.New("backup captured VM specification is missing")
	}
	want := map[string]bool{}
	if manifest.CapturedSpec.Boot.Firmware == "uefi" {
		want["nvram"] = true
	}
	if manifest.CapturedSpec.Boot.TPMEnabled() {
		want["swtpm"] = true
	}
	if len(manifest.Firmware) != len(want) {
		return errors.New("backup firmware state is incomplete")
	}
	for _, artifact := range manifest.Firmware {
		if !want[artifact.Kind] || artifact.Valid() != nil {
			return errors.New("backup firmware state does not match the VM")
		}
		delete(want, artifact.Kind)
	}
	if len(want) != 0 {
		return errors.New("backup firmware state is incomplete")
	}
	return nil
}

func fileSHA256(path string) (string, int64, error) {
	f, err := openRegularNoFollow(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, errors.New("backup artifact is not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), info.Size(), nil
}

// BuildBackupManifest constructs a complete backup manifest after validated capture.
// captured must be the VM definition at capture time; SpecSHA256 alone cannot reconstruct a restore.
func BuildBackupManifest(pointID, repositoryID, poolID, vmID, vmName, consistency, specSHA256, diskIdentitySHA256 string, captured protocol.VMCreateSpec, disks []protocol.BackupDiskArtifact, firmware []protocol.BackupFirmwareArtifact) (protocol.BackupManifest, error) {
	if consistency == "" {
		consistency = protocol.BackupCaptureStopped
	}
	if captured.ID != vmID {
		return protocol.BackupManifest{}, errors.New("backup captured spec identity does not match the source VM")
	}
	spec := captured
	manifest := protocol.BackupManifest{
		Version:            protocol.BackupManifestVersion,
		RecoveryPointID:    pointID,
		RepositoryID:       repositoryID,
		PoolID:             poolID,
		SourceVMID:         vmID,
		SourceVMName:       vmName,
		CapturedAt:         time.Now().UTC(),
		Consistency:        consistency,
		SpecSHA256:         specSHA256,
		DiskIdentitySHA256: diskIdentitySHA256,
		CapturedSpec:       &spec,
		Disks:              disks,
		Firmware:           firmware,
		Complete:           true,
		Success:            1,
	}
	if err := manifest.Valid(); err != nil {
		return protocol.BackupManifest{}, err
	}
	if len(disks) > maxBackupDiskArtifacts {
		return protocol.BackupManifest{}, errors.New("backup disk set exceeds limit")
	}
	return manifest, nil
}

// BuildStoppedBackupManifest constructs a complete stopped-backup manifest after conversion.
func BuildStoppedBackupManifest(pointID, repositoryID, poolID, vmID, vmName, specSHA256, diskIdentitySHA256 string, captured protocol.VMCreateSpec, disks []protocol.BackupDiskArtifact, firmware []protocol.BackupFirmwareArtifact) (protocol.BackupManifest, error) {
	return BuildBackupManifest(pointID, repositoryID, poolID, vmID, vmName, protocol.BackupCaptureStopped, specSHA256, diskIdentitySHA256, captured, disks, firmware)
}

// BackupDiskSource is one writable managed disk head used for stopped convert or online backup-begin.
type BackupDiskSource struct {
	Target string
	Source string
}

// BackupDiskSources returns current journaled writable disk heads, the same projection snapshots use.
func (e *ManagedEngine) BackupDiskSources(vmID string) ([]BackupDiskSource, error) {
	js, err := e.journals()
	if err != nil {
		return nil, err
	}
	if managedDeletedVMs(js)[vmID] {
		return nil, errors.New("VM is not defined on this host")
	}
	cur, err := managedCurrent(js, vmID)
	if err != nil {
		return nil, err
	}
	if cur.Command.VM == nil || len(cur.Command.VM.Disks) == 0 {
		return nil, errors.New("backup source VM has no disks")
	}
	if len(cur.Disks) == 0 || len(cur.Targets) != len(cur.Disks) || len(cur.Disks) != len(cur.Command.VM.Disks) {
		return nil, errors.New("managed VM disk heads are unavailable for backup")
	}
	out := make([]BackupDiskSource, 0, len(cur.Disks))
	seen := map[string]bool{}
	for i, source := range cur.Disks {
		target := strings.TrimSpace(cur.Targets[i])
		if target == "" || !filepath.IsAbs(source) || filepath.Clean(source) != source || seen[target] {
			return nil, errors.New("managed VM disk heads are incomplete or ambiguous")
		}
		seen[target] = true
		out = append(out, BackupDiskSource{Target: target, Source: source})
	}
	return out, nil
}

// BackupSpecAndDiskDigests hashes the current VM definition and ordered disk heads for the manifest.
func BackupSpecAndDiskDigests(vm protocol.VMCreateSpec, disks []BackupDiskSource) (specSHA256, diskIdentitySHA256 string, err error) {
	raw, err := json.Marshal(vm)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	specSHA256 = hex.EncodeToString(sum[:])
	h := sha256.New()
	for _, disk := range disks {
		_, _ = io.WriteString(h, disk.Target)
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, disk.Source)
		_, _ = h.Write([]byte{0})
	}
	diskIdentitySHA256 = hex.EncodeToString(h.Sum(nil))
	return specSHA256, diskIdentitySHA256, nil
}

// CaptureBackupFirmwareArtifacts copies matching NVRAM/swtpm state beside disk artifacts.
// Online UEFI is refused before this runs; BIOS guests return an empty set.
func CaptureBackupFirmwareArtifacts(ctx context.Context, vmID, workRoot string, running bool) ([]protocol.BackupFirmwareArtifact, error) {
	if !uuidPattern.MatchString(vmID) || !filepath.IsAbs(workRoot) {
		return nil, errors.New("backup firmware capture arguments are invalid")
	}
	args := []string{"dumpxml", vmID}
	if !running {
		args = append(args, "--inactive")
	}
	raw, err := managedVirsh(ctx, args...)
	if err != nil {
		return nil, errors.New("VM definition is unavailable for backup firmware capture")
	}
	var domain managedDomain
	if err := xml.Unmarshal(raw, &domain); err != nil {
		return nil, errors.New("VM definition is invalid for backup firmware capture")
	}
	sources, err := snapshotFirmwareSources(ctx, domain, vmID, workRoot, "backup")
	if err != nil {
		return nil, err
	}
	out := make([]protocol.BackupFirmwareArtifact, 0, len(sources))
	for _, source := range sources {
		switch source.Kind {
		case "nvram":
			dest := filepath.Join(workRoot, "nvram")
			if _, err := copyManagedSnapshotFirmware(ctx, source.SourcePath, dest); err != nil {
				return nil, err
			}
			sum, size, err := fileSHA256(dest)
			if err != nil {
				return nil, err
			}
			out = append(out, protocol.BackupFirmwareArtifact{Kind: "nvram", Filename: "nvram", SHA256: sum, Size: size})
		case "swtpm":
			tree := filepath.Join(workRoot, "swtpm-tree")
			if _, err := copyManagedSnapshotFirmware(ctx, source.SourcePath, tree); err != nil {
				return nil, err
			}
			tarPath := filepath.Join(workRoot, "swtpm.tar")
			if _, err := run(ctx, "tar", "-C", workRoot, "-cf", tarPath, "swtpm-tree"); err != nil {
				_ = os.RemoveAll(tree)
				return nil, err
			}
			_ = os.RemoveAll(tree)
			sum, size, err := fileSHA256(tarPath)
			if err != nil {
				return nil, err
			}
			out = append(out, protocol.BackupFirmwareArtifact{Kind: "swtpm", Filename: "swtpm.tar", SHA256: sum, Size: size})
		default:
			return nil, errors.New("unsupported backup firmware kind")
		}
	}
	return out, nil
}
