// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

const nexMountMarker = "# Managed by Nex Core; do not edit\n"

var errStorageNotMounted = errors.New("remote storage is not mounted")

// keyedLock is one waiter-at-a-time lock per key whose wait ends with the caller's context.
// A holder stuck on a hung server blocks only callers of the same key. The key set is
// bounded by the validated registration and repository IDs.
type keyedLock struct{ slots sync.Map }

func (k *keyedLock) lock(ctx context.Context, key string) (unlock func(), err error) {
	v, _ := k.slots.LoadOrStore(key, make(chan struct{}, 1))
	slot := v.(chan struct{})
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// remotePreparation keeps one mount's preparation and rollback ordered. It is per mount, not
// global, so a hung share cannot stall every other NFS or SMB preparation on the host.
var remotePreparation keyedLock

const nfsVolumesPath = "/proc/fs/nfsfs/volumes"

var (
	readNFSVolumes = os.ReadFile
	statNFSPath    = os.Stat
)

// storageRemountAllowedKey is set only by the managed update path after it has proved no VM
// still uses the repository.  Replacing a CIFS credentials file in a unit needs a restart, so
// doing it while a guest is using the mount could corrupt its disks.
type storageRemountAllowedKey struct{}

func allowStorageRemount(ctx context.Context) context.Context {
	return context.WithValue(ctx, storageRemountAllowedKey{}, true)
}

func storageRemountAllowed(ctx context.Context) bool {
	ok, _ := ctx.Value(storageRemountAllowedKey{}).(bool)
	return ok
}

func remoteMountPath(s protocol.StorageRegistration) string {
	return filepath.Join(storageMountRoot, s.ID)
}

func remoteUnitPath(s protocol.StorageRegistration) (string, string) {
	name := systemdMountUnit(remoteMountPath(s))
	return name, filepath.Join(storageUnitRoot, name)
}

// A .mount unit name is the escaped Where= path.  Do not use a friendly alias: systemd rejects
// it, and an invalid unit would leave the managed mountpoint empty.
func systemdMountUnit(path string) string {
	var b strings.Builder
	for i, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if i > 0 {
			b.WriteByte('-')
		}
		for _, c := range []byte(part) {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, `\x%02x`, c)
			}
		}
	}
	return b.String() + ".mount"
}

func prepareNFS(ctx context.Context, s protocol.StorageRegistration, extraOptions string) (access storageAccess, retErr error) {
	if err := protocol.ValidateStoredStorageRegistration(s, false); err != nil || s.StorageKind() != protocol.StorageKindNFS {
		return storageAccess{}, errors.New("invalid NFS storage registration")
	}
	unlock, err := remotePreparation.lock(ctx, s.ID)
	if err != nil {
		return storageAccess{}, err
	}
	defer unlock()
	v := s.Source.NFS
	if s.Identity.NFS != nil && (s.Identity.NFS.Server != v.Server || s.Identity.NFS.Export != v.Export) {
		return storageAccess{}, errors.New("NFS identity does not match registration")
	}
	source := v.Server + ":" + v.Export
	_, unitPath := remoteUnitPath(s)
	_, unitAlreadyPresent := os.Lstat(unitPath)
	path, err := prepareRemote(ctx, s, source, "nfs4", "vers=4,hard,_netdev"+extraOptions)
	if err != nil {
		return storageAccess{}, err
	}
	defer func() {
		// A brand-new mount that fails validation is not a usable registration. The
		// managed engine repeats this cleanup defensively, but preparation is also
		// used directly by recovery paths, so do not leave a live unit behind here.
		if retErr == nil || !errors.Is(unitAlreadyPresent, os.ErrNotExist) {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(storageCleanupContext(ctx), 20*time.Second)
		defer cancel()
		if cleanupErr := detachRemote(cleanupCtx, s); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("%w: clean up rejected NFS mount: %v", ErrStorageRollbackUncertain, cleanupErr))
		}
	}()
	if err := verifyRemoteMount(ctx, path, source, "nfs4", []string{"hard"}); err != nil {
		return storageAccess{}, err
	}
	return nfsAccess(s, v, path, source)
}

// inspectNFS deliberately does not invoke systemd or create a mount. It is used before guest
// starts, so a boot-time recovery check cannot turn an unavailable repository into a new mount.
func inspectNFS(ctx context.Context, s protocol.StorageRegistration) (storageAccess, error) {
	if err := protocol.ValidateStoredStorageRegistration(s, false); err != nil || s.StorageKind() != protocol.StorageKindNFS {
		return storageAccess{}, errors.New("invalid NFS storage registration")
	}
	v := s.Source.NFS
	if s.Identity.NFS != nil && (s.Identity.NFS.Server != v.Server || s.Identity.NFS.Export != v.Export) {
		return storageAccess{}, errors.New("NFS identity does not match registration")
	}
	source, path := v.Server+":"+v.Export, remoteMountPath(s)
	if err := verifyRemoteMount(ctx, path, source, "nfs4", []string{"hard"}); err != nil {
		return storageAccess{}, err
	}
	return nfsAccess(s, v, path, source)
}

func prepareSMB(ctx context.Context, s protocol.StorageRegistration) (access storageAccess, retErr error) {
	return prepareSMBWithAdmission(ctx, s, "", nil)
}

// prepareSMBWithAdmission runs admit on the selected directory only after the share marker
// matches, so a wrong share is never claimed, and inside the rollback of a new mount.
func prepareSMBWithAdmission(ctx context.Context, s protocol.StorageRegistration, extraOptions string, admit func(string) error) (access storageAccess, retErr error) {
	if err := protocol.ValidateStoredStorageRegistration(s, false); err != nil || s.StorageKind() != protocol.StorageKindSMB {
		return storageAccess{}, errors.New("invalid SMB storage registration")
	}
	unlock, err := remotePreparation.lock(ctx, s.ID)
	if err != nil {
		return storageAccess{}, err
	}
	defer unlock()
	credential, err := credentialFile(ctx, s)
	if err != nil {
		return storageAccess{}, err
	}
	v := s.Source.SMB
	if s.Identity.SMB != nil && (s.Identity.SMB.Server != v.Server || s.Identity.SMB.Share != v.Share) {
		return storageAccess{}, errors.New("SMB identity does not match registration")
	}
	source := "//" + v.Server + "/" + v.Share
	uid, gid, err := libvirtQEMUIdentity(ctx)
	if err != nil {
		return storageAccess{}, err
	}
	// Map all server files to the account that libvirt runs QEMU as. This also makes disks
	// created by Core visible to QEMU without widening the share to every local account.
	options := smbMountOptions(uid, gid, credential) + extraOptions
	unit, unitPath := remoteUnitPath(s)
	oldUnit, oldUnitErr := os.ReadFile(unitPath)
	_, unitAlreadyPresent := os.Lstat(unitPath)
	remountAllowed, _ := ctx.Value(storageRemountAllowedKey{}).(bool)
	defer func() {
		// A credential update is not committed until the new mount has passed all
		// identity and QEMU access checks. Restore the old owned unit on any later
		// failure, not only when systemctl restart itself fails.
		if retErr == nil || !remountAllowed || oldUnitErr != nil {
			return
		}
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		current, readErr := os.ReadFile(unitPath)
		if readErr != nil || string(current) != string(oldUnit) {
			if rollbackErr := restoreManagedMountUnit(recoveryCtx, unitPath, oldUnit, unit, true); rollbackErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("%w: restore previous SMB mount after failed update: %v", ErrStorageRollbackUncertain, rollbackErr))
				return
			}
		}
		if _, rollbackErr := inspectSMB(recoveryCtx, s); rollbackErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("%w: verify restored SMB mount: %v", ErrStorageRollbackUncertain, rollbackErr))
			return
		}
		if rollbackErr := qemuStorageAccess(recoveryCtx, remoteMountPath(s)); rollbackErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("%w: restore previous SMB mount after failed update: %v", ErrStorageRollbackUncertain, rollbackErr))
		}
	}()
	path, err := prepareRemote(ctx, s, source, "cifs", options)
	if err != nil {
		return storageAccess{}, err
	}
	defer func() {
		// Do not retain a newly-created mount when its identity, selected directory,
		// or QEMU probe fails. Existing mounts are handled by the credential-rotation
		// rollback above; a failed first registration has no prior state to retain.
		if retErr == nil || !errors.Is(unitAlreadyPresent, os.ErrNotExist) {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(storageCleanupContext(ctx), 20*time.Second)
		defer cancel()
		if cleanupErr := detachRemote(cleanupCtx, s); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("%w: clean up rejected SMB mount: %v", ErrStorageRollbackUncertain, cleanupErr))
		}
	}()
	if err := verifyRemoteMount(ctx, path, source, "cifs", smbRequiredMountOptions(uid, gid)); err != nil {
		return storageAccess{}, err
	}
	access, err = smbPathAccess(path, source)
	if err != nil {
		return storageAccess{}, err
	}
	access, err = smbIdentity(s, v, path, source, access) // identity/marker always belongs to the share root.
	if err != nil {
		return storageAccess{}, err
	}
	dataPath := filepath.Join(path, v.Directory)
	if v.Directory != "" {
		data, e := remoteSubpathAccess(path, v.Directory, access)
		if e != nil {
			return storageAccess{}, e
		}
		access.Path, access.Total, access.Available, access.ReadOnly = dataPath, data.Total, data.Available, data.ReadOnly
	}
	if admit != nil {
		if err := admit(dataPath); err != nil {
			return storageAccess{}, err
		}
	}
	if err = qemuStorageAccess(ctx, dataPath); err != nil {
		return storageAccess{}, err
	}
	return access, nil
}

// inspectSMB is the read-only counterpart to prepareSMB; it never replays credentials or mounts.
func inspectSMB(ctx context.Context, s protocol.StorageRegistration) (storageAccess, error) {
	if err := protocol.ValidateStoredStorageRegistration(s, false); err != nil || s.StorageKind() != protocol.StorageKindSMB {
		return storageAccess{}, errors.New("invalid SMB storage registration")
	}
	v := s.Source.SMB
	if s.Identity.SMB != nil && (s.Identity.SMB.Server != v.Server || s.Identity.SMB.Share != v.Share) {
		return storageAccess{}, errors.New("SMB identity does not match registration")
	}
	uid, gid, err := libvirtQEMUIdentity(ctx)
	if err != nil {
		return storageAccess{}, err
	}
	source, path := "//"+v.Server+"/"+v.Share, remoteMountPath(s)
	if err := verifyRemoteMount(ctx, path, source, "cifs", smbRequiredMountOptions(uid, gid)); err != nil {
		return storageAccess{}, err
	}
	access, err := smbPathAccess(path, source)
	if err != nil {
		return storageAccess{}, err
	}
	access, err = smbIdentity(s, v, path, source, access)
	if err != nil {
		return storageAccess{}, err
	}
	if v.Directory != "" {
		dataPath := filepath.Join(path, v.Directory)
		data, e := remoteSubpathAccess(path, v.Directory, access)
		if e != nil {
			return storageAccess{}, e
		}
		access.Path, access.Total, access.Available, access.ReadOnly = dataPath, data.Total, data.Available, data.ReadOnly
	}
	return access, nil
}

func smbMountOptions(uid, gid, credential string) string {
	return "vers=3.1.1,seal,cache=strict,credentials=" + credential + ",uid=" + uid + ",gid=" + gid + ",file_mode=0600,dir_mode=0700,_netdev"
}

func smbRequiredMountOptions(uid, gid string) []string {
	return []string{"vers=3.1.1", "seal", "cache=strict", "uid=" + uid, "gid=" + gid, "file_mode=0600", "dir_mode=0700"}
}

// libvirtQEMUIdentity accepts only the decimal uid/gid output that id produces.  Mount options
// are rendered into a systemd unit, so treating command output as an opaque string is unsafe.
func libvirtQEMUIdentity(ctx context.Context) (string, string, error) {
	uid, err := qemuID(ctx, "-u")
	if err != nil {
		return "", "", err
	}
	gid, err := qemuID(ctx, "-g")
	if err != nil {
		return "", "", err
	}
	return uid, gid, nil
}

func qemuID(ctx context.Context, flag string) (string, error) {
	out, err := nativeRun(ctx, "id", flag, "libvirt-qemu")
	if err != nil {
		return "", fmt.Errorf("cannot resolve libvirt-qemu identity: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || strconv.FormatUint(n, 10) != raw {
		return "", errors.New("invalid libvirt-qemu identity")
	}
	return raw, nil
}

func nfsAccess(s protocol.StorageRegistration, v *protocol.NFSStorageSource, path, source string) (storageAccess, error) {
	a, err := storagePathAccess(path, "nfs:"+source, false)
	if err != nil {
		return storageAccess{}, err
	}
	fsid, err := nfsFilesystemID(path)
	if err != nil {
		return storageAccess{}, err
	}
	a, err = nfsIdentity(s, v, source, a, fsid)
	if err != nil || v.Directory == "" {
		return a, err
	}
	data, err := remoteSubpathAccess(path, v.Directory, a)
	if err != nil {
		return storageAccess{}, err
	}
	// The selected subdirectory has its own access/capacity view, while identity remains
	// bound to the mounted export's protocol FSID and explicit relative directory.
	return data, nil
}

func nfsIdentity(s protocol.StorageRegistration, v *protocol.NFSStorageSource, source string, a storageAccess, fsid string) (storageAccess, error) {
	if want := s.Identity.NFS; want != nil {
		// A persisted NFS identity must be the kernel NFSv4 server FSID profile.
		// In particular, do not upgrade legacy statfs values: statfs f_fsid is
		// allowed to be zero or undefined, so doing so would hide a replacement.
		if !validNFSFilesystemID(want.FilesystemID) || want.FilesystemID != fsid {
			return storageAccess{}, errors.New("NFS filesystem identity does not match registration")
		}
	}
	a.FilesystemID = fsid
	a.Identity = "nfs:" + source + ":directory=" + v.Directory + ":fsid=" + fsid
	a.Observed.NFS = &protocol.NFSStorageIdentity{Server: v.Server, Export: v.Export, FilesystemID: fsid}
	return a, nil
}

// nfsFilesystemID obtains the NFS server's protocol filesystem identifier from
// the kernel's current-network-namespace volume table. statfs.f_fsid is not a
// suitable NFS repository identity: Linux permits it to be zero or undefined.
// The table's DEV field is server->s_dev, so it is bound to the mounted path by
// stat(path).Dev rather than to a source string that may be an alias.
func nfsFilesystemID(path string) (string, error) {
	info, err := statNFSPath(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("NFS mount device is unavailable")
	}
	data, err := readNFSVolumes(nfsVolumesPath)
	if err != nil {
		return "", errors.New("NFS kernel filesystem identity is unavailable")
	}
	return nfsFilesystemIDForDevice(string(data), linuxDevice(stat.Dev))
}

func linuxDevice(dev uint64) string {
	major := ((dev >> 8) & 0xfff) | ((dev >> 32) & 0xfffff000)
	minor := (dev & 0xff) | ((dev >> 12) & 0xffffff00)
	return strconv.FormatUint(major, 10) + ":" + strconv.FormatUint(minor, 10)
}

func nfsFilesystemIDForDevice(volumes, device string) (string, error) {
	var identity string
	header := false
	for _, line := range strings.Split(volumes, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if !header {
			if len(fields) != 6 || strings.Join(fields, " ") != "NV SERVER PORT DEV FSID FSC" {
				return "", errors.New("NFS kernel filesystem identity is malformed")
			}
			header = true
			continue
		}
		if len(fields) != 6 || !validNFSVolumeVersion(fields[0]) || !validNFSServer(fields[1]) || !validNFSPort(fields[2]) || !validNFSDevice(fields[3]) || !validNFSFSID(fields[4]) || (fields[5] != "yes" && fields[5] != "no") {
			return "", errors.New("NFS kernel filesystem identity is malformed")
		}
		if fields[3] != device {
			continue
		}
		if fields[0] != "v4" {
			return "", errors.New("NFS repository is not mounted with NFSv4")
		}
		if identity != "" {
			return "", errors.New("NFS kernel filesystem identity is ambiguous")
		}
		identity = "nfs4:" + fields[1] + ":" + fields[2] + ":" + fields[4]
	}
	if !header || identity == "" {
		return "", errors.New("NFS kernel filesystem identity is unavailable")
	}
	return identity, nil
}

func validNFSFilesystemID(raw string) bool {
	parts := strings.Split(raw, ":")
	return len(parts) == 5 && parts[0] == "nfs4" && validNFSServer(parts[1]) && validNFSPort(parts[2]) && validNFSHex(parts[3], 16) && validNFSHex(parts[4], 16)
}

func validNFSVolumeVersion(raw string) bool {
	return raw == "v2" || raw == "v3" || raw == "v4"
}

func validNFSHex(raw string, limit int) bool {
	if raw == "" || len(raw) > limit {
		return false
	}
	for _, c := range raw {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validNFSServer(raw string) bool {
	return (len(raw) == 8 || len(raw) == 32) && validNFSHex(raw, 32)
}

func validNFSPort(raw string) bool {
	if !validNFSHex(raw, 4) {
		return false
	}
	n, err := strconv.ParseUint(raw, 16, 16)
	return err == nil && n != 0
}

func validNFSDevice(raw string) bool {
	parts := strings.Split(raw, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return false
		}
	}
	return true
}

func validNFSFSID(raw string) bool {
	parts := strings.Split(raw, ":")
	return len(parts) == 2 && validNFSHex(parts[0], 16) && validNFSHex(parts[1], 16)
}

func smbAccess(s protocol.StorageRegistration, v *protocol.SMBStorageSource, path, source string) (storageAccess, error) {
	a, err := smbPathAccess(path, source)
	if err != nil {
		return storageAccess{}, err
	}
	return smbIdentity(s, v, path, source, a)
}

var smbPathAccess = func(path, source string) (storageAccess, error) {
	return storagePathAccess(path, "smb:"+source, false)
}

func smbIdentity(s protocol.StorageRegistration, v *protocol.SMBStorageSource, path, source string, a storageAccess) (storageAccess, error) {
	identity := &protocol.SMBStorageIdentity{Server: v.Server, Share: v.Share, FilesystemID: a.FilesystemID}
	if want := s.Identity.SMB; want != nil && want.MarkerID != "" {
		marker, err := smbMarkerAt(path, false)
		if err != nil || marker != want.MarkerID {
			return storageAccess{}, errors.New("SMB repository marker does not match registration")
		}
		identity.MarkerID = marker
		// CIFS fsid is diagnostic only for marker-backed repositories: its volume create
		// time changes across reconnects. The marker is the durable alias-collision key.
		a.FilesystemID = "smb-marker:" + marker
		a.Identity = "smb:" + source + ":marker=" + marker
	} else if want := s.Identity.SMB; want != nil && want.FilesystemID != "" {
		if want.FilesystemID != a.FilesystemID {
			return storageAccess{}, errors.New("SMB filesystem identity does not match registration")
		}
	} else if s.Identity.SMB == nil {
		marker, err := smbMarker(path)
		if err != nil {
			return storageAccess{}, err
		}
		identity.MarkerID = marker
		a.FilesystemID = "smb-marker:" + marker
		a.Identity = "smb:" + source + ":marker=" + marker
	} else {
		return storageAccess{}, errors.New("SMB legacy identity is incomplete")
	}
	a.Observed.SMB = identity
	return a, nil
}

func prepareRemote(ctx context.Context, s protocol.StorageRegistration, source, fstype, options string) (path string, retErr error) {
	if !storageID.MatchString(s.ID) || source == "" || strings.ContainsAny(source, "%\x00\r\n") {
		return "", errors.New("invalid remote storage source")
	}
	path = remoteMountPath(s)
	if err := ensureStorageMountRoot(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", err
	}
	unit, unitPath := remoteUnitPath(s)
	content := remoteUnit(source, path, fstype, options)
	allowed, _ := ctx.Value(storageRemountAllowedKey{}).(bool)
	old, created, err := replaceManagedMountUnit(unitPath, content, allowed)
	changed := old != nil
	if err != nil {
		return "", err
	}
	if created {
		defer func() {
			if retErr == nil {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(storageCleanupContext(ctx), 20*time.Second)
			defer cancel()
			if cleanupErr := detachRemote(cleanupCtx, s); cleanupErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("%w: clean up failed remote mount: %v", ErrStorageRollbackUncertain, cleanupErr))
			}
		}()
	}
	if _, err := nativeRun(ctx, "systemctl", "daemon-reload"); err != nil {
		if changed {
			if rollbackErr := restoreManagedMountUnit(ctx, unitPath, old, unit, false); rollbackErr != nil {
				return "", errors.Join(err, fmt.Errorf("%w: restore remote mount unit: %v", ErrStorageRollbackUncertain, rollbackErr))
			}
		}
		return "", err
	}
	if !changed {
		started := time.Now()
		if _, err := nativeRun(ctx, "systemctl", "enable", "--now", unit); err != nil {
			return "", withMountJournal(ctx, unit, started, err)
		}
		return path, nil
	}
	if _, err := nativeRun(ctx, "systemctl", "enable", unit); err != nil {
		if rollbackErr := restoreManagedMountUnit(ctx, unitPath, old, unit, false); rollbackErr != nil {
			return "", errors.Join(err, fmt.Errorf("%w: restore remote mount unit: %v", ErrStorageRollbackUncertain, rollbackErr))
		}
		return "", err
	}
	if _, err := nativeRun(ctx, "systemctl", "restart", unit); err != nil {
		if rollbackErr := restoreManagedMountUnit(ctx, unitPath, old, unit, true); rollbackErr != nil {
			return "", errors.Join(err, fmt.Errorf("%w: restore remote mount unit: %v", ErrStorageRollbackUncertain, rollbackErr))
		}
		return "", err
	}
	return path, nil
}

// withMountJournal adds the mount helper's own message to a failed unit start; systemctl
// itself only says "Job failed". Only lines since this attempt began are used, so an earlier
// attempt's messages are never reported as this one's, and the text is bounded.
func withMountJournal(ctx context.Context, unit string, since time.Time, err error) error {
	out, _ := nativeRun(ctx, "journalctl", "-u", unit, "--since", "@"+strconv.FormatInt(since.Unix(), 10), "-n", "5", "--no-pager", "-o", "cat")
	tail := strings.TrimSpace(string(out))
	if len(tail) > mountJournalLimit {
		tail = strings.ToValidUTF8(tail[:mountJournalLimit], "") + "…"
	}
	if tail != "" {
		return fmt.Errorf("%w: %s", err, tail)
	}
	return err
}

const mountJournalLimit = 1024

// ensureStorageMountRoot repairs only Nex's configured local mount-root. It never chmods a
// mounted repository leaf or administrator path; service UMask=0077 must not hide mounts from QEMU.
func ensureStorageMountRoot() error {
	if !filepath.IsAbs(storageMountRoot) || filepath.Clean(storageMountRoot) != storageMountRoot {
		return errors.New("storage mount root is invalid")
	}
	if err := os.Mkdir(storageMountRoot, 0711); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	// This is Nex's fixed, local mount root, never a repository leaf. Open it
	// without following links and repair only this root-owned directory through
	// the descriptor, so the agent's UMask=0077 cannot make every remote mount
	// unreachable to QEMU.
	fd, err := syscall.Open(storageMountRoot, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("storage mount root is unsafe")
	}
	f := os.NewFile(uintptr(fd), storageMountRoot)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	// The installed service is root; accepting the effective Core owner also keeps
	// the same safety property in an explicitly unprivileged test/development
	// service without treating another account's directory as ours.
	if !info.IsDir() || !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return errors.New("storage mount root is not Core-owned and safe")
	}
	if err := f.Chmod(0711); err != nil {
		return err
	}
	atPath, err := os.Lstat(storageMountRoot)
	if err != nil || atPath.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, atPath) {
		return errors.New("storage mount root changed during repair")
	}
	return nil
}

// restoreManagedMountUnit restores the exact known-good Nex unit after a failed credential
// rotation. It deliberately leaves administrator units alone: replaceManagedMountUnit rejects
// them before any write is made.
func restoreManagedMountUnit(ctx context.Context, path string, old []byte, unit string, restart bool) error {
	if err := atomicWrite(path, old); err != nil {
		return err
	}
	if _, err := nativeRun(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if restart {
		_, err := nativeRun(ctx, "systemctl", "restart", unit)
		return err
	}
	return nil
}

// remoteUnit renders a mount unit.
func remoteUnit(source, path, fstype, options string) []byte {
	return []byte(nexMountMarker + "[Unit]\nAfter=network-online.target\nWants=network-online.target\nBefore=libvirtd.service\n\n[Mount]\nWhat=" + source + "\nWhere=" + path + "\nType=" + fstype + "\nOptions=" + options + "\n\n[Install]\nWantedBy=multi-user.target\n")
}

// writeManagedMountUnit never adopts an administrator unit. An exact prior Nex unit is safe to
// reuse, which makes registration retries idempotent without opening an overwrite race.
func writeManagedMountUnit(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		if string(old) != string(content) || !strings.HasPrefix(string(old), nexMountMarker) {
			return errors.New("refusing to replace an existing mount unit")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return writeManagedMountUnit(path, content)
		}
		return err
	}
	if _, err = f.Write(content); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// replaceManagedMountUnit updates only an existing Nex unit. It returns its previous contents
// for rollback, non-nil only when it rewrote the unit; callers must not restart it until Core has blocked repository dependencies.
func replaceManagedMountUnit(path string, content []byte, allowRemount bool) (old []byte, created bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, false, err
	}
	old, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := writeManagedMountUnit(path, content); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !strings.HasPrefix(string(old), nexMountMarker) {
		return nil, false, errors.New("refusing to replace an existing mount unit")
	}
	if string(old) == string(content) {
		return nil, false, nil
	}
	if !allowRemount {
		return nil, false, errors.New("storage mount change requires Core to block repository dependencies")
	}
	if err := atomicWrite(path, content); err != nil {
		return nil, false, err
	}
	return old, false, nil
}

func verifyRemoteMount(ctx context.Context, path, source, fstype string, required []string) error {
	out, err := nativeRun(ctx, "findmnt", "--noheadings", "--output", "SOURCE,FSTYPE,OPTIONS,TARGET", "--target", path)
	if err != nil {
		return errors.New("storage mount is unavailable")
	}
	var parts []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return errors.New("storage mount source does not match registration")
		}
		if parts == nil {
			parts = fields
			continue
		}
		// A service mount namespace can report the same host mount twice. Accept
		// exact duplicates, never a second divergent source/options/target row.
		if strings.Join(parts, "\x00") != strings.Join(fields, "\x00") {
			return errors.New("storage mount source does not match registration")
		}
	}
	if len(parts) == 4 && parts[3] != path {
		// findmnt --target reports the parent filesystem when nothing is mounted here.
		return errStorageNotMounted
	}
	if len(parts) != 4 || parts[0] != source || parts[1] != fstype || parts[3] != path {
		return errors.New("storage mount source does not match registration")
	}
	options := "," + parts[2] + ","
	if strings.Contains(options, ",ro,") {
		return errors.New("storage mount is read-only")
	}
	if (fstype == "nfs4" && strings.Contains(options, ",soft,")) || (fstype == "cifs" && (strings.Contains(options, ",cache=loose,") || strings.Contains(options, ",nobrl,") || strings.Contains(options, ",nostrictsync,") || strings.Contains(options, ",noperm,"))) {
		return errors.New("storage mount uses unsafe consistency options")
	}
	for _, option := range required {
		if !strings.Contains(options, ","+option+",") {
			return fmt.Errorf("storage mount is missing required option %s", option)
		}
	}
	return nil
}

func detachRemote(ctx context.Context, s protocol.StorageRegistration) error {
	if s.StorageKind() != protocol.StorageKindNFS && s.StorageKind() != protocol.StorageKindSMB {
		return errors.New("storage is not a remote filesystem repository")
	}
	unit, unitPath := remoteUnitPath(s)
	content, err := os.ReadFile(unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(content), nexMountMarker) {
		return errors.New("refusing to remove an administrator mount unit")
	}
	if _, err = nativeRun(ctx, "systemctl", "disable", "--now", unit); err != nil {
		return err
	}
	if err = os.Remove(unitPath); err != nil {
		return err
	}
	_, err = nativeRun(ctx, "systemctl", "daemon-reload")
	return err
}

// storageMountStale reports the one mount failure Core can repair by itself. It is a seam so
// the decision either side of it is testable without an NFS server.
var storageMountStale = func(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, syscall.ESTALE)
}

// repairStaleRemoteMount recovers a repository whose backing filesystem was replaced out of
// band: every client is left holding a stale NFS file handle, which no amount of re-reading
// fixes while the mount unit is still active, so a recheck could only report the mountpoint
// unavailable for ever. Cycling the generated unit re-resolves the handle.
//
// It is attempted only when no current VM disk, imported ISO, or retained disk path uses the
// repository. Historical snapshot references alone do not block re-establishing this same
// registration; source changes and detach remain guarded separately. When a current reference
// exists, it is named and the operator decides. Nothing else is repaired: any other cause
// leaves the mount exactly as it was and ordinary verification reports it.
func repairStaleRemoteMount(ctx context.Context, js []managedJournal, s protocol.StorageRegistration) error {
	if k := s.StorageKind(); k != protocol.StorageKindNFS && k != protocol.StorageKindSMB {
		return nil
	}
	if !storageMountStale(remoteMountPath(s)) {
		return nil
	}
	if deps := storageContentDependencies(js, s.ID); len(deps) > 0 {
		return errors.New("storage has a stale file handle from a change on the file server and cannot be remounted while used by " + strings.Join(deps, ", ") + "; stop them and recheck again")
	}
	unit, unitPath := remoteUnitPath(s)
	if b, err := os.ReadFile(unitPath); err != nil || !strings.HasPrefix(string(b), nexMountMarker) {
		return nil
	}
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := nativeRun(work, "systemctl", "restart", unit); err != nil {
		return errors.New("storage has a stale file handle from a change on the file server and the mount could not be restarted; check the file server, then recheck again")
	}
	return nil
}

// startRemoteMount starts the existing, unchanged Nex NFS/SMB mount unit only when systemd
// reports it inactive or failed. It never restarts, rewrites, reloads, enables or reads
// credentials; the read-only verification that follows decides whether the repository is usable.
// systemctl is-active talks only to PID 1, so a hung hard NFS mount cannot block it.
//
// Recheck passes no flags and waits (bounded) so the operator sees the real result. The periodic
// lane passes --no-block: systemd queues the mount job and returns at once, so an unreachable
// server never holds the engine lock while commands wait.
func startRemoteMount(ctx context.Context, s protocol.StorageRegistration, flags ...string) {
	if k := s.StorageKind(); k != protocol.StorageKindNFS && k != protocol.StorageKindSMB {
		return
	}
	unit, unitPath := remoteUnitPath(s)
	if b, err := os.ReadFile(unitPath); err != nil || !strings.HasPrefix(string(b), nexMountMarker) {
		return
	}
	if _, err := nativeRun(ctx, "systemctl", "is-active", "--quiet", unit); err == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, _ = nativeRun(ctx, "systemctl", append(append([]string{"start"}, flags...), unit)...)
}

// StartRemoteMounts retries registered NFS/SMB mounts that failed or were never started, for
// example when Core booted before the file server. A repository with an unsettled storage
// operation is left to ReconcileStorage.
func (e *ManagedEngine) StartRemoteMounts(ctx context.Context) {
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return
	}
	defer unlock(lock)
	js, err := e.journals()
	if err != nil {
		return
	}
	busy := map[string]bool{}
	for _, j := range js {
		if (j.Result.Status != "pending" && j.Result.Status != "needs_attention") || !strings.HasPrefix(j.Command.Kind, "storage-") {
			continue
		}
		if v := j.Command.StorageLifecycle; v != nil {
			busy[v.RepositoryID] = true
		}
		if v := j.Command.Storage; v != nil {
			busy[v.ID] = true
		}
	}
	projection := storageProjection(js)
	ids := make([]string, 0, len(projection))
	for id := range projection {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !busy[id] {
			startRemoteMount(ctx, projection[id], "--no-block")
		}
	}
}
