// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

const nexMountMarker = "# Managed by Nex Core; do not edit\n"

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

func prepareNFS(ctx context.Context, s protocol.StorageRegistration) (storageAccess, error) {
	if err := protocol.ValidateStoredStorageRegistration(s, false); err != nil || s.StorageKind() != protocol.StorageKindNFS {
		return storageAccess{}, errors.New("invalid NFS storage registration")
	}
	v := s.Source.NFS
	if s.Identity.NFS != nil && (s.Identity.NFS.Server != v.Server || s.Identity.NFS.Export != v.Export) {
		return storageAccess{}, errors.New("NFS identity does not match registration")
	}
	source := v.Server + ":" + v.Export
	path, err := prepareRemote(ctx, s, source, "nfs4", "vers=4,hard,_netdev")
	if err != nil {
		return storageAccess{}, err
	}
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
	if err := protocol.ValidateStoredStorageRegistration(s, false); err != nil || s.StorageKind() != protocol.StorageKindSMB {
		return storageAccess{}, errors.New("invalid SMB storage registration")
	}
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
	options := smbMountOptions(credential, uid, gid)
	unit, unitPath := remoteUnitPath(s)
	oldUnit, oldUnitErr := os.ReadFile(unitPath)
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
	if err := verifyRemoteMount(ctx, path, source, "cifs", smbRequiredMountOptions(uid, gid)); err != nil {
		return storageAccess{}, err
	}
	access, err = smbPathAccess(path, source)
	if err != nil {
		return storageAccess{}, err
	}
	if err = qemuStorageAccess(ctx, path); err != nil {
		return storageAccess{}, err
	}
	return smbIdentity(s, v, path, source, access)
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
	return smbAccess(s, v, path, source)
}

func smbMountOptions(credential, uid, gid string) string {
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
	return nfsIdentity(s, v, source, a, fsid)
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
	a.Identity = "nfs:" + source + ":fsid=" + fsid
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

func smbPathAccess(path, source string) (storageAccess, error) {
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

func prepareRemote(ctx context.Context, s protocol.StorageRegistration, source, fstype, options string) (string, error) {
	if !storageID.MatchString(s.ID) || source == "" || strings.ContainsAny(source, "%\x00\r\n") {
		return "", errors.New("invalid remote storage source")
	}
	path := remoteMountPath(s)
	if err := os.MkdirAll(path, 0755); err != nil {
		return "", err
	}
	unit, unitPath := remoteUnitPath(s)
	content := remoteUnit(source, path, fstype, options)
	allowed, _ := ctx.Value(storageRemountAllowedKey{}).(bool)
	changed, old, err := replaceManagedMountUnit(unitPath, content, allowed)
	if err != nil {
		return "", err
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
		if _, err := nativeRun(ctx, "systemctl", "enable", "--now", unit); err != nil {
			return "", err
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
// for rollback; callers must not restart it until Core has blocked repository dependencies.
func replaceManagedMountUnit(path string, content []byte, allowRemount bool) (bool, []byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, nil, err
	}
	old, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil, writeManagedMountUnit(path, content)
	}
	if err != nil {
		return false, nil, err
	}
	if !strings.HasPrefix(string(old), nexMountMarker) {
		return false, nil, errors.New("refusing to replace an existing mount unit")
	}
	if string(old) == string(content) {
		return false, nil, nil
	}
	if !allowRemount {
		return false, nil, errors.New("storage mount change requires Core to block repository dependencies")
	}
	if err := atomicWrite(path, content); err != nil {
		return false, nil, err
	}
	return true, old, nil
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
