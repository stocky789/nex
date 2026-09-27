// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"nex.local/nex/internal/protocol"
)

var backupTPMOwner = func() (int, int, error) {
	account, err := user.Lookup("tss")
	if err != nil {
		return 0, 0, errors.New("host swtpm account is unavailable")
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(account.Gid)
	return uid, gid, err
}

// Libvirt's AppArmor profile grants swtpm access only to this root's UUID
// directory. Replace uses a separate staging tree until its stopped cutover.
var backupTPMStateRoot = "/var/lib/libvirt/swtpm"

func restoreTPMPath(vmID, operationID string) string {
	return filepath.Join(backupTPMStateRoot, "nex-restore-"+vmID+"-"+operationID)
}

func managedTPMStateSource(domain managedDomain, vmID string) (string, error) {
	if !uuidPattern.MatchString(vmID) || domain.Devices.TPM == nil || domain.Devices.TPM.Backend.Type != "emulator" {
		return "", errors.New("VM TPM definition is invalid")
	}
	source := domain.Devices.TPM.Backend.Source
	if source == nil {
		return filepath.Join(backupTPMStateRoot, vmID), nil
	}
	if source.Type != "dir" || filepath.Clean(source.Path) != source.Path || !filepath.IsAbs(source.Path) || filepath.Base(source.Path) != "tpm2" {
		return "", errors.New("VM TPM source is not a managed restore state directory")
	}
	base := filepath.Dir(source.Path)
	if base != filepath.Join(backupTPMStateRoot, vmID) {
		return "", errors.New("VM TPM source differs from libvirt's UUID state directory")
	}
	return base, nil
}

// installRestoreTPM unpacks the captured state into libvirt's UUID path for a
// new VM, or a separate same-filesystem staging tree for stopped replace.
func installRestoreTPM(ctx context.Context, spec protocol.VMCreateSpec, operationID, localDir string, firmware []protocol.BackupFirmwareArtifact, retry, replace, repairRoot bool) (string, string, error) {
	var artifact *protocol.BackupFirmwareArtifact
	for i := range firmware {
		if firmware[i].Kind == "swtpm" {
			artifact = &firmware[i]
		}
	}
	if !spec.Boot.TPMEnabled() {
		if artifact != nil {
			return "", "", errors.New("restore has unexpected TPM state")
		}
		return "", "", nil
	}
	if artifact == nil || artifact.Valid() != nil || !uuidPattern.MatchString(spec.ID) || !protocol.ValidSnapshotOperationID(operationID) {
		return "", "", errors.New("restore TPM placement is invalid")
	}
	source := filepath.Join(localDir, artifact.Filename)
	sum, size, err := fileSHA256(source)
	if err != nil || sum != artifact.SHA256 || size != artifact.Size {
		return "", "", errors.New("restore TPM archive digest mismatch")
	}
	uid, gid, err := backupTPMOwner()
	if err != nil {
		return "", "", err
	}
	if err := backupTPMRootAccessible(uid, gid, repairRoot); err != nil {
		return "", "", err
	}
	base := filepath.Join(backupTPMStateRoot, spec.ID)
	if replace {
		base = restoreTPMPath(spec.ID, operationID)
	}
	staging, err := os.MkdirTemp(backupTPMStateRoot, ".nex-tpm-"+spec.ID+"-"+operationID+"-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(staging)
	if err := unpackRestoreTPM(ctx, source, staging, uid, gid); err != nil {
		return "", "", err
	}
	want, err := managedSnapshotManifest(ctx, staging)
	if err != nil || !want.Exists {
		return "", "", errors.New("restored TPM staging tree cannot be verified")
	}
	if info, err := os.Lstat(base); err == nil {
		if !retry || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("restore TPM placement already exists")
		}
		existing, inspectErr := managedSnapshotManifest(ctx, base)
		if inspectErr != nil || !snapshotManifestsEqual(existing, want) {
			return "", "", errors.New("reserved TPM tree differs from captured state; inspect before retry")
		}
		return filepath.Join(base, "tpm2"), existing.SHA256, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if err := os.Rename(staging, base); err != nil {
		return "", "", err
	}
	if err := syncManagedDir(backupTPMStateRoot); err != nil {
		return "", "", err
	}
	manifest, err := managedSnapshotManifest(ctx, base)
	if err != nil || !manifest.Exists {
		return "", "", errors.New("restored TPM tree cannot be verified")
	}
	return filepath.Join(base, "tpm2"), manifest.SHA256, nil
}

func backupTPMRootAccessible(uid, gid int, repair bool) error {
	if !filepath.IsAbs(backupTPMStateRoot) || filepath.Clean(backupTPMStateRoot) != backupTPMStateRoot {
		return errors.New("libvirt TPM state root is invalid")
	}
	for dir := filepath.Dir(backupTPMStateRoot); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("libvirt TPM state path is unavailable or unsafe")
		}
		if dir == "/" {
			break
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !(stat.Uid == uint32(uid) && info.Mode().Perm()&0100 != 0 ||
			stat.Gid == uint32(gid) && info.Mode().Perm()&0010 != 0 || info.Mode().Perm()&0001 != 0) {
			return errors.New("libvirt TPM state path is not traversable by swtpm")
		}
	}
	// libvirt creates this directory lazily on a host's first TPM VM. A restore
	// may be the first such VM, so establish only this exact native state root.
	if _, err := os.Lstat(backupTPMStateRoot); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(backupTPMStateRoot, 0711); err != nil {
			return err
		}
		if err := os.Chmod(backupTPMStateRoot, 0711); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if repair {
		info, err := os.Lstat(backupTPMStateRoot)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("libvirt TPM state path is unavailable or unsafe")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm() == 0700 {
			entries, readErr := os.ReadDir(backupTPMStateRoot)
			if readErr != nil || len(entries) != 0 {
				return errors.New("libvirt TPM state root repair requires an empty directory")
			}
			if err := os.Chmod(backupTPMStateRoot, 0711); err != nil {
				return err
			}
		}
	}
	for dir := backupTPMStateRoot; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("libvirt TPM state path is unavailable or unsafe")
		}
		if dir == "/" {
			break
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !(stat.Uid == uint32(uid) && info.Mode().Perm()&0100 != 0 ||
			stat.Gid == uint32(gid) && info.Mode().Perm()&0010 != 0 || info.Mode().Perm()&0001 != 0) {
			return errors.New("libvirt TPM state path is not traversable by swtpm")
		}
	}
	return nil
}

// exchangeReplaceTPM swaps two directories on the same filesystem in one
// kernel operation. The old UUID state stays at the operation staging path.
func exchangeReplaceTPM(ctx context.Context, vmID, operationID, oldDigest, restoredDigest string) error {
	if !uuidPattern.MatchString(vmID) || !protocol.ValidSnapshotOperationID(operationID) || oldDigest == "" || restoredDigest == "" {
		return errors.New("replace TPM exchange arguments are invalid")
	}
	current := filepath.Join(backupTPMStateRoot, vmID)
	staged := restoreTPMPath(vmID, operationID)
	oldManifest, err := managedSnapshotManifest(ctx, current)
	if err != nil || !oldManifest.Exists || oldManifest.SHA256 != oldDigest {
		return errors.New("original TPM state changed before replace cutover")
	}
	newManifest, err := managedSnapshotManifest(ctx, staged)
	if err != nil || !newManifest.Exists || newManifest.SHA256 != restoredDigest {
		return errors.New("staged TPM state differs from captured backup")
	}
	oldPath, err := syscall.BytePtrFromString(current)
	if err != nil {
		return err
	}
	newPath, err := syscall.BytePtrFromString(staged)
	if err != nil {
		return err
	}
	const renameat2AMD64 = 316
	const renameExchange = 2
	_, _, errno := syscall.RawSyscall6(renameat2AMD64, ^uintptr(99), uintptr(unsafe.Pointer(oldPath)), ^uintptr(99), uintptr(unsafe.Pointer(newPath)), renameExchange, 0)
	if errno != 0 {
		return errno
	}
	return syncManagedDir(backupTPMStateRoot)
}

func unpackRestoreTPM(ctx context.Context, archivePath, destination string, uid, gid int) error {
	f, err := openRegularNoFollow(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := tar.NewReader(f)
	files, bytes := 0, int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		hdr, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		if name != path.Clean(name) || (name != "swtpm-tree" && name != "swtpm-tree/tpm2" && !strings.HasPrefix(name, "swtpm-tree/tpm2/")) {
			return errors.New("restore TPM archive path is unsafe")
		}
		if hdr.Typeflag != tar.TypeDir && hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			return errors.New("restore TPM archive contains unsupported file type")
		}
		if name == "swtpm-tree" && hdr.Typeflag != tar.TypeDir {
			return errors.New("restore TPM archive root is not a directory")
		}
		files++
		bytes += hdr.Size
		if files > maxManagedSnapshotFirmwareFiles || bytes > maxManagedSnapshotFirmwareBytes || hdr.Size < 0 {
			return errors.New("restore TPM archive exceeds limits")
		}
		relative := strings.TrimPrefix(name, "swtpm-tree/")
		if name == "swtpm-tree" {
			continue
		}
		target := filepath.Join(destination, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(out, reader, hdr.Size)
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil {
			return errors.Join(copyErr, syncErr, closeErr)
		}
	}
	stateDir := filepath.Join(destination, "tpm2")
	state, err := os.Lstat(stateDir)
	if err != nil || !state.IsDir() {
		return errors.New("restore TPM archive lacks tpm2 state")
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil || len(entries) == 0 {
		return errors.New("restore TPM archive has empty tpm2 state")
	}
	directories := []string{}
	if err := filepath.Walk(destination, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == destination {
			directories = append(directories, path)
			return os.Chmod(path, 0711)
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
		if info.IsDir() {
			directories = append(directories, path)
			return os.Chmod(path, 0700)
		}
		if err := os.Chmod(path, 0600); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		return errors.Join(err, f.Close())
	}); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncManagedDir(directories[i]); err != nil {
			return err
		}
	}
	return nil
}
