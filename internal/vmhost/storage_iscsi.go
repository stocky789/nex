// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

var iscsiUUID = regexp.MustCompile(`^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$`)
var iscsiWWID = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

func canonicalWWID(value string) string {
	return strings.TrimPrefix(strings.ToLower(value), "0x")
}

const nexISCSINodeMarker = "# Managed by Nex Core; remove through Nex storage detach.\n"

var (
	ErrISCSIUnmanagedSession            = errors.New("refusing to change an unmanaged iSCSI session")
	ErrISCSICredentialRotationReconnect = errors.New("iSCSI credential rotation requires a safe reconnect")
	ErrISCSIRollbackUncertain           = ErrStorageRollbackUncertain
)

// Open-iSCSI has no password-file flag. Nex writes CHAP only into a private node record,
// never into iscsiadm arguments where process inspection can expose it.
var iscsiNodeRoot = "/var/lib/iscsi/nodes"
var iscsiOwnerRoot = filepath.Join(os.TempDir(), "nex-iscsi-owners") // direct-helper/test fallback; Core supplies its state root.

type iscsiBlock struct {
	Path     string       `json:"path"`
	Type     string       `json:"type"`
	UUID     string       `json:"uuid"`
	WWN      string       `json:"wwn"`
	FSType   string       `json:"fstype"`
	HCTL     string       `json:"hctl"`
	Children []iscsiBlock `json:"children"`
}

type iscsiBlockList struct {
	BlockDevices []iscsiBlock `json:"blockdevices"`
}

func validISCSI(s protocol.StorageRegistration) (*protocol.ISCSIStorageSource, error) {
	v := s.Source.ISCSI
	if s.StorageKind() != protocol.StorageKindISCSI || v == nil || s.Source.NFS != nil || s.Source.ZFS != nil || s.Source.SMB != nil || (s.Path != "" && s.Path != v.MountPath) || v.MountPath != remoteMountPath(s) ||
		v.Portal == "" || len(v.Portal) > 253 || strings.Count(v.Portal, ":") > 1 || strings.ContainsAny(v.Portal, "/\\\x00 \t\r\n") ||
		!(strings.HasPrefix(v.TargetIQN, "iqn.") || strings.HasPrefix(v.TargetIQN, "eui.") || strings.HasPrefix(v.TargetIQN, "naa.")) || len(v.TargetIQN) > 223 || strings.ContainsAny(v.TargetIQN, "\x00 \t\r\n") ||
		!iscsiWWID.MatchString(v.WWID) || !iscsiUUID.MatchString(v.FilesystemUUID) || !v.ExclusiveTarget || !filepath.IsAbs(v.MountPath) || filepath.Clean(v.MountPath) != v.MountPath || v.MountPath == "/" || strings.ContainsAny(v.MountPath, "\x00\r\n") {
		return nil, errors.New("invalid iSCSI storage source")
	}
	if s.Identity.ISCSI != nil {
		i := s.Identity.ISCSI
		if i.TargetIQN != v.TargetIQN || i.LUN != v.LUN || canonicalWWID(i.WWID) != canonicalWWID(v.WWID) || i.FilesystemUUID != v.FilesystemUUID {
			return nil, errors.New("iSCSI storage identity does not match source")
		}
	}
	return v, nil
}

// prepareISCSI logs into an existing, single-host target and mounts its existing ext4 or XFS
// filesystem. It never formats a LUN or uses an unstable /dev/sdX name as mount source.
func prepareISCSI(ctx context.Context, s protocol.StorageRegistration) (access storageAccess, retErr error) {
	v, err := validISCSI(s)
	if err != nil {
		return storageAccess{}, err
	}
	if prior, ok := ctx.Value(storagePreviousKey{}).(protocol.StorageRegistration); ok && prior.CredentialReceipt != s.CredentialReceipt {
		// Before changing CHAP, a still-mounted prior repository must prove its
		// full identity. A bad replacement mount is ambiguous, not safe to disrupt.
		if mounted, findErr := nativeRun(ctx, "findmnt", "-n", "-o", "TARGET", "--target", v.MountPath); findErr == nil && strings.TrimSpace(string(mounted)) == v.MountPath {
			if _, inspectErr := inspectISCSI(ctx, s); inspectErr != nil {
				return storageAccess{}, fmt.Errorf("%w: prior iSCSI mount cannot be verified before credential rotation: %v", ErrStorageRollbackUncertain, inspectErr)
			}
		}
	}
	// A replay must observe an already mounted, identity-matching repository rather than
	// issuing another login/mount. Credential rotations use the explicit remount context.
	if !storageRemountAllowed(ctx) {
		if access, inspectErr := inspectISCSI(ctx, s); inspectErr == nil {
			return access, nil
		}
	}
	change, err := configureISCSICHAP(ctx, s, v)
	if err != nil {
		if rollbackErr := change.rollback(context.Background(), v); rollbackErr != nil {
			return storageAccess{}, errors.Join(err, fmt.Errorf("restore iSCSI node configuration: %w", rollbackErr))
		}
		return storageAccess{}, err
	}
	var previous iscsiFilesystemInfo
	loggedIn := false
	completed, unitCreated := false, false
	defer func() {
		if completed {
			return
		}
		if change.reconnect {
			if rollbackErr := restoreISCSIReconnect(context.Background(), s, change, previous, unitCreated); rollbackErr != nil {
				retErr = errors.Join(retErr, rollbackErr)
			}
			return
		}
		if rollbackErr := rollbackISCSI(context.Background(), s, unitCreated, loggedIn, change.nodeCreated); rollbackErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("%w: clean up failed iSCSI preparation: %v", ErrISCSIRollbackUncertain, rollbackErr))
		}
		if !change.nodeCreated {
			if rollbackErr := change.rollback(context.Background(), v); rollbackErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("%w: restore iSCSI node configuration: %v", ErrISCSIRollbackUncertain, rollbackErr))
			}
		}
	}()
	if change.reconnect {
		if mounted, findErr := nativeRun(ctx, "findmnt", "-n", "-o", "SOURCE,TARGET,FSTYPE", "--target", v.MountPath); findErr == nil {
			parts := strings.Fields(strings.TrimSpace(string(mounted)))
			if len(parts) == 3 && parts[1] == v.MountPath {
				filesystem, filesystemErr := waitISCSIFilesystem(ctx, v)
				if filesystemErr != nil {
					return storageAccess{}, fmt.Errorf("verify mounted iSCSI LUN before reconnect: %w", filesystemErr)
				}
				if parts[0] != filesystem.Path {
					return storageAccess{}, errors.New("refusing to reconnect an iSCSI mount with an unexpected source")
				}
				previous = filesystem
				previous.FSType = parts[2]
			}
		}
		if previous.FSType != "" {
			unit, _ := iscsiUnitPath(s)
			if _, err = nativeRun(ctx, "systemctl", "stop", unit); err != nil {
				return storageAccess{}, fmt.Errorf("prepare iSCSI credential reconnect: %w", err)
			}
		}
		active, activeErr := iscsiSessionActive(ctx, v)
		if activeErr != nil {
			return storageAccess{}, fmt.Errorf("query iSCSI session before reconnect: %w", activeErr)
		}
		if active {
			_, err = nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--logout")
		}
		if err != nil {
			return storageAccess{}, fmt.Errorf("prepare iSCSI credential reconnect: %w", err)
		}
	}
	active, sessionErr := iscsiSessionActive(ctx, v)
	if sessionErr != nil {
		return storageAccess{}, fmt.Errorf("query iSCSI session before login: %w", sessionErr)
	}
	if !active {
		if _, err = nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--login"); err != nil {
			// iscsid can establish the automatic node session between the preflight
			// query and login. Its documented session-exists exit 15 is acceptable only when an immediate exact
			// session query proves this target is now connected; later LUN/mount
			// identity checks still decide whether it is usable.
			if !iscsiSessionExistsError(err) {
				return storageAccess{}, fmt.Errorf("iSCSI login failed: %w", err)
			}
			active, sessionErr = iscsiSessionActive(ctx, v)
			if sessionErr != nil || !active {
				if sessionErr != nil {
					return storageAccess{}, fmt.Errorf("iSCSI login failed and session is ambiguous: %w", sessionErr)
				}
				return storageAccess{}, fmt.Errorf("iSCSI login failed: %w", err)
			}
		} else {
			loggedIn = true
		}
	}
	if _, err = nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--op", "update", "-n", "node.startup", "-v", "automatic"); err != nil {
		return storageAccess{}, fmt.Errorf("configure iSCSI startup: %w", err)
	}
	filesystem, err := waitISCSIFilesystem(ctx, v)
	if err != nil {
		return storageAccess{}, err
	}
	if canonicalWWID(filesystem.WWID) != canonicalWWID(v.WWID) || (s.Identity.ISCSI != nil && canonicalWWID(s.Identity.ISCSI.WWID) != canonicalWWID(filesystem.WWID)) {
		return storageAccess{}, errors.New("iSCSI LUN WWID does not match registered identity")
	}
	if err = os.MkdirAll(v.MountPath, 0711); err != nil {
		return storageAccess{}, err
	}
	// The filesystem UUID identifies the partition as well as the filesystem. The WWID is
	// still verified above, but mounting its parent disk would corrupt a partitioned LUN.
	device := filepath.Join("/dev/disk/by-uuid", v.FilesystemUUID)
	if unitCreated, err = prepareISCSIMountUnit(ctx, s, device, filesystem.FSType); err != nil {
		return storageAccess{}, err
	}
	unit, _ := iscsiUnitPath(s)
	if _, err = nativeRun(ctx, "systemctl", "start", unit); err != nil {
		return storageAccess{}, fmt.Errorf("iSCSI mount failed: %w", err)
	}
	a, err := storagePathAccess(v.MountPath, "iscsi:"+v.TargetIQN+":"+fmt.Sprint(v.LUN)+":"+filesystem.WWID, false)
	if err != nil {
		return storageAccess{}, err
	}
	a.Observed.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: v.TargetIQN, LUN: v.LUN, WWID: filesystem.WWID, FilesystemUUID: v.FilesystemUUID}
	completed = true
	return a, nil
}

func iscsiSessionExistsError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 15
}

// restoreISCSIReconnect puts a failed credential rotation back on its prior node record,
// session, and mount. Any failure leaves the repository's active state unknown to Core.
func restoreISCSIReconnect(ctx context.Context, s protocol.StorageRegistration, change iscsiNodeChange, previous iscsiFilesystemInfo, removeUnit bool) error {
	v := s.Source.ISCSI
	var errs []error
	if err := rollbackISCSI(ctx, s, removeUnit, true, false); err != nil {
		errs = append(errs, fmt.Errorf("disconnect changed iSCSI session: %w", err))
	}
	if err := change.rollback(ctx, v); err != nil {
		errs = append(errs, fmt.Errorf("restore iSCSI node configuration: %w", err))
	}
	if _, err := nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--login"); err != nil {
		errs = append(errs, fmt.Errorf("restore iSCSI session: %w", err))
	}
	if previous.FSType != "" {
		unit, _ := iscsiUnitPath(s)
		if _, err := nativeRun(ctx, "systemctl", "start", unit); err != nil {
			errs = append(errs, fmt.Errorf("restore iSCSI mount: %w", err))
		}
		if _, err := inspectISCSI(ctx, s); err != nil {
			errs = append(errs, fmt.Errorf("verify restored iSCSI mount: %w", err))
		} else if err := qemuStorageAccess(ctx, v.MountPath); err != nil {
			errs = append(errs, fmt.Errorf("verify restored iSCSI QEMU access: %w", err))
		}
	} else {
		errs = append(errs, errors.New("prior iSCSI mount identity was not established"))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrISCSIRollbackUncertain, errors.Join(errs...))
}

// waitISCSIFilesystem gives udev a bounded chance to publish the stable by-UUID path after
// login, then still requires the unique expected UUID/WWID/HCTL tuple before mounting.
func waitISCSIFilesystem(ctx context.Context, v *protocol.ISCSIStorageSource) (iscsiFilesystemInfo, error) {
	var last error
	for attempt := 0; attempt < 12; attempt++ {
		if _, err := nativeRun(ctx, "udevadm", "settle", "--timeout=2"); err != nil {
			last = fmt.Errorf("iSCSI device discovery did not settle: %w", err)
			continue
		}
		out, err := nativeRun(ctx, "lsblk", "--json", "--output", "PATH,TYPE,UUID,WWN,FSTYPE,HCTL")
		if err != nil {
			last = err
			continue
		}
		filesystem, err := iscsiFilesystemForLUNInfo(out, v.FilesystemUUID, v.LUN)
		if err == nil {
			return filesystem, nil
		}
		last = err
		if attempt < 11 {
			// settle drains the current queue; it does not wait for an iSCSI login's
			// later device event. Keep the retry bounded and cancellable.
			select {
			case <-ctx.Done():
				return iscsiFilesystemInfo{}, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return iscsiFilesystemInfo{}, last
}

type iscsiNodeChange struct {
	nodeCreated bool
	reconnect   bool
	path        string
	previous    []byte
}

func (c iscsiNodeChange) rollback(ctx context.Context, v *protocol.ISCSIStorageSource) error {
	if c.nodeCreated {
		return deleteISCSINode(ctx, v)
	} else if c.previous != nil {
		return atomicWrite(c.path, c.previous)
	}
	return nil
}

func configureISCSICHAP(ctx context.Context, s protocol.StorageRegistration, v *protocol.ISCSIStorageSource) (iscsiNodeChange, error) {
	if err := migrateLegacyISCSIOwner(ctx, v); err != nil {
		return iscsiNodeChange{}, err
	}
	if !s.CredentialPresent {
		if s.CredentialReceipt != "" {
			return iscsiNodeChange{}, errors.New("iSCSI credential receipt is not authorized")
		}
	}
	var username, secret string
	if s.CredentialPresent {
		credential, err := credentialFile(ctx, s)
		if err != nil {
			return iscsiNodeChange{}, err
		}
		info, err := os.Lstat(credential)
		if err != nil || info.Mode().Perm() != 0600 {
			return iscsiNodeChange{}, errors.New("iSCSI credential file is unavailable")
		}
		b, err := readSmall(credential, 1<<16)
		if err != nil {
			return iscsiNodeChange{}, err
		}
		username, secret, err = iscsiCHAPFields(string(b))
		if err != nil {
			return iscsiNodeChange{}, err
		}
	}
	path := iscsiNodePath(v)
	previous, rotating := ctx.Value(storagePreviousKey{}).(protocol.StorageRegistration)
	rotating = rotating && previous.CredentialReceipt != s.CredentialReceipt
	if _, err := os.Lstat(path); err == nil {
		config, readErr := readSmall(path, 1<<20)
		if readErr != nil || (!strings.Contains(string(config), nexISCSINodeMarker) && !nexISCSIOwner(ctx, v)) {
			return iscsiNodeChange{}, errors.New("refusing to overwrite an existing iSCSI node configuration")
		}
		updated := nexISCSINodeConfig(config, username, secret)
		if string(updated) == string(config) && !rotating {
			return iscsiNodeChange{}, nil
		}
		if rotating && string(updated) == string(config) {
			oldCredential, oldErr := credentialFile(ctx, previous)
			if oldErr != nil {
				return iscsiNodeChange{}, oldErr
			}
			oldBody, oldErr := readSmall(oldCredential, 1<<16)
			if oldErr != nil {
				return iscsiNodeChange{}, oldErr
			}
			oldUser, oldSecret, oldErr := iscsiCHAPFields(string(oldBody))
			if oldErr != nil {
				return iscsiNodeChange{}, oldErr
			}
			config = nexISCSINodeConfig(config, oldUser, oldSecret)
			updated = nexISCSINodeConfig(config, username, secret)
		}
		active, err := iscsiSessionActive(ctx, v)
		if err != nil {
			return iscsiNodeChange{}, err
		} else if active && !storageRemountAllowed(ctx) {
			return iscsiNodeChange{}, ErrISCSICredentialRotationReconnect
		}
		if err := atomicWrite(path, updated); err != nil {
			return iscsiNodeChange{}, err
		}
		change := iscsiNodeChange{path: path, previous: config, reconnect: active || rotating}
		if err := writeNexISCSIOwner(ctx, v); err != nil {
			return change, err
		}
		return change, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return iscsiNodeChange{}, err
	}
	if active, err := iscsiSessionActive(ctx, v); err != nil {
		return iscsiNodeChange{}, err
	} else if active {
		return iscsiNodeChange{}, ErrISCSIUnmanagedSession
	}
	if _, err := nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--op", "new"); err != nil {
		return iscsiNodeChange{}, fmt.Errorf("create iSCSI node: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return iscsiNodeChange{nodeCreated: true}, errors.New("created iSCSI node configuration is unavailable")
	}
	config, err := readSmall(path, 1<<20)
	if err != nil {
		return iscsiNodeChange{nodeCreated: true}, err
	}
	if err := atomicWrite(path, nexISCSINodeConfig(config, username, secret)); err != nil {
		return iscsiNodeChange{nodeCreated: true}, err
	}
	if err := writeNexISCSIOwner(ctx, v); err != nil {
		return iscsiNodeChange{nodeCreated: true}, err
	}
	return iscsiNodeChange{nodeCreated: true}, nil
}

func nexISCSINodeConfig(config []byte, username, secret string) []byte {
	if i := strings.Index(string(config), nexISCSINodeMarker); i >= 0 {
		config = config[:i]
	}
	// Keep the caller's bytes intact: credential rotation needs them to restore a node
	// after login validation fails.
	config = append([]byte(nil), config...)
	config = append(config, '\n')
	config = append(config, nexISCSINodeMarker...)
	if username != "" {
		config = append(config, []byte("node.session.auth.authmethod = CHAP\nnode.session.auth.username = "+username+"\nnode.session.auth.password = "+secret+"\n")...)
	}
	return config
}

func iscsiSessionActive(ctx context.Context, v *protocol.ISCSIStorageSource) (bool, error) {
	out, err := nativeRun(ctx, "iscsiadm", "-m", "session", "-P", "1")
	if err != nil {
		// open-iscsi uses this exact diagnostic when session mode has no sessions.
		// Any other failure is ambiguous and must stop mutations; `-T`/`-p` are not
		// accepted filters in Debian's session mode.
		if iscsiNoActiveSessionsError(err) {
			return false, nil
		}
		return false, fmt.Errorf("query iSCSI sessions: %w", err)
	}
	return iscsiSessionListed(out, v), nil
}

func iscsiNoActiveSessionsError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 21
}

func iscsiSessionListed(out []byte, v *protocol.ISCSIStorageSource) bool {
	var target string
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(raw)
		if value, ok := strings.CutPrefix(line, "Target:"); ok {
			target = strings.TrimSpace(value)
			target = strings.TrimSuffix(target, " (non-flash)")
			target = strings.TrimSuffix(target, " (flash)")
			continue
		}
		if value, ok := strings.CutPrefix(line, "Current Portal:"); ok && target == v.TargetIQN {
			portal := iscsiSessionPortal(strings.TrimSpace(value))
			if portal == iscsiPortalAddress(v.Portal) {
				return true
			}
		}
	}
	return false
}

func iscsiSessionPortal(value string) string {
	// Debian open-iscsi prints `host:port,tpgt`; TPGT is transport metadata.
	cut := strings.LastIndex(value, ",")
	if cut < 1 || !iscsiDecimal(value[cut+1:]) {
		return ""
	}
	value = value[:cut]
	colon := strings.LastIndex(value, ":")
	if colon < 1 || !iscsiPort(value[colon+1:]) {
		return ""
	}
	return value
}

func iscsiPort(value string) bool {
	if !iscsiDecimal(value) {
		return false
	}
	n, err := strconv.ParseUint(value, 10, 16)
	return err == nil && n > 0 && n <= 65535
}

func iscsiPortalAddress(value string) string {
	if !strings.Contains(value, ":") {
		return value + ":3260"
	}
	return value
}

func iscsiDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func iscsiCHAPFields(raw string) (string, string, error) {
	var username, secret string
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsAny(value, "\r\n") {
			return "", "", errors.New("invalid iSCSI credential file")
		}
		switch key {
		case "username":
			if username != "" {
				return "", "", errors.New("invalid iSCSI credential file")
			}
			username = value
		case "password":
			if secret != "" {
				return "", "", errors.New("invalid iSCSI credential file")
			}
			secret = value
		default:
			return "", "", errors.New("invalid iSCSI credential file")
		}
	}
	if username == "" || secret == "" || strings.ContainsAny(username, " \t") {
		return "", "", errors.New("invalid iSCSI credential file")
	}
	return username, secret, nil
}

func iscsiNodePath(v *protocol.ISCSIStorageSource) string {
	portal := v.Portal
	if !strings.Contains(portal, ":") {
		portal += ",3260"
	} else {
		portal = strings.Replace(portal, ":", ",", 1)
	}
	// Debian open-iscsi writes the node record directly at <iqn>/<portal>, not the
	// older iface/default nesting. Keep the path derived solely from validated source fields.
	return filepath.Join(iscsiNodeRoot, v.TargetIQN, portal)
}

// Owner records must not sit in open-iscsi's node tree: Debian scans every sibling
// there as a node record. The state-root directory is private to the Core service.
func iscsiOwnerPath(ctx context.Context, v *protocol.ISCSIStorageSource) string {
	root, _ := ctx.Value(storageOwnerRootKey{}).(string)
	if root == "" {
		root = iscsiOwnerRoot
	}
	sum := sha256.Sum256([]byte(v.TargetIQN + "\x00" + v.Portal + "\x00" + canonicalWWID(v.WWID) + "\x00" + v.FilesystemUUID + "\x00" + fmt.Sprint(v.LUN)))
	return filepath.Join(root, fmt.Sprintf("%x", sum[:]))
}

func legacyScopedISCSIOwnerPath(ctx context.Context, v *protocol.ISCSIStorageSource) string {
	root, _ := ctx.Value(storageOwnerRootKey{}).(string)
	if root == "" {
		root = iscsiOwnerRoot
	}
	// v11/v12 keyed only target+portal. Preserve it only when the content below
	// binds the same WWID/UUID/LUN, then migrate to the stronger key.
	sum := sha256.Sum256([]byte(v.TargetIQN + "\x00" + v.Portal))
	return filepath.Join(root, fmt.Sprintf("%x", sum[:]))
}

func legacyISCSIOwnerPath(v *protocol.ISCSIStorageSource) string {
	return iscsiNodePath(v) + ".nex-owner"
}

func iscsiOwnerRecord(v *protocol.ISCSIStorageSource) string {
	return "nex-iscsi-owner-v1\nwwid=" + canonicalWWID(v.WWID) + "\nuuid=" + v.FilesystemUUID + "\nlun=" + fmt.Sprint(v.LUN) + "\n"
}

func writeNexISCSIOwner(ctx context.Context, v *protocol.ISCSIStorageSource) error {
	path := iscsiOwnerPath(ctx, v)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return atomicWrite(path, []byte(iscsiOwnerRecord(v)))
}

func nexISCSIOwner(ctx context.Context, v *protocol.ISCSIStorageSource) bool {
	b, err := readSmall(iscsiOwnerPath(ctx, v), 1<<12)
	return err == nil && string(b) == iscsiOwnerRecord(v)
}

func migrateLegacyISCSIOwner(ctx context.Context, v *protocol.ISCSIStorageSource) error {
	legacyScoped := legacyScopedISCSIOwnerPath(ctx, v)
	if legacyScoped != iscsiOwnerPath(ctx, v) {
		b, err := readSmall(legacyScoped, 1<<12)
		if err == nil {
			if string(b) != iscsiOwnerRecord(v) {
				return errors.New("legacy iSCSI owner does not match registered LUN")
			}
			if err := writeNexISCSIOwner(ctx, v); err != nil {
				return err
			}
			if err := os.Remove(legacyScoped); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	path := legacyISCSIOwnerPath(v)
	b, err := readSmall(path, 1<<12)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || string(b) != iscsiOwnerRecord(v) {
		return errors.New("unexpected iSCSI node-tree ownership file")
	}
	if err := writeNexISCSIOwner(ctx, v); err != nil {
		return err
	}
	return os.Remove(path)
}

func removeNexISCSINode(ctx context.Context, v *protocol.ISCSIStorageSource) error {
	if !nexISCSINode(ctx, v) {
		return nil
	}
	return deleteISCSINode(ctx, v)
}

func deleteISCSINode(ctx context.Context, v *protocol.ISCSIStorageSource) error {
	_, err := nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--op", "delete")
	if err != nil {
		return err
	}
	if err := os.Remove(iscsiOwnerPath(ctx, v)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func prepareISCSIMountUnit(ctx context.Context, s protocol.StorageRegistration, device, fstype string) (bool, error) {
	unit, path := iscsiUnitPath(s)
	_, statErr := os.Lstat(path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return false, statErr
	}
	created := errors.Is(statErr, os.ErrNotExist)
	content := []byte(nexMountMarker + "[Unit]\nAfter=network-online.target open-iscsi.service\nWants=network-online.target open-iscsi.service\nBefore=libvirtd.service\n\n[Mount]\nWhat=" + device + "\nWhere=" + s.Source.ISCSI.MountPath + "\nType=" + fstype + "\nOptions=rw,nosuid,nodev,_netdev\n\n[Install]\nWantedBy=multi-user.target\n")
	if err := writeManagedMountUnit(path, content); err != nil {
		return false, err
	}
	if _, err := nativeRun(ctx, "systemctl", "daemon-reload"); err != nil {
		return created, err
	}
	if _, err := nativeRun(ctx, "systemctl", "enable", unit); err != nil {
		return created, err
	}
	return created, nil
}

func rollbackISCSI(ctx context.Context, s protocol.StorageRegistration, removeUnit, logout, removeNode bool) error {
	v := s.Source.ISCSI
	if v == nil {
		return nil
	}
	var errs []error
	if removeUnit {
		unit, path := iscsiUnitPath(s)
		if _, err := nativeRun(ctx, "systemctl", "stop", unit); err != nil {
			errs = append(errs, err)
		}
		if _, err := nativeRun(ctx, "systemctl", "disable", unit); err != nil {
			errs = append(errs, err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		if _, err := nativeRun(ctx, "systemctl", "daemon-reload"); err != nil {
			errs = append(errs, err)
		}
	}
	if logout {
		if _, err := nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--op", "update", "-n", "node.startup", "-v", "manual"); err != nil {
			errs = append(errs, err)
		}
		if _, err := nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--logout"); err != nil {
			errs = append(errs, err)
		}
	}
	if removeNode {
		if err := deleteISCSINode(ctx, v); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func iscsiUnitPath(s protocol.StorageRegistration) (string, string) {
	name := systemdMountUnit(s.Source.ISCSI.MountPath)
	return name, filepath.Join(storageUnitRoot, name)
}

// inspectISCSI only observes an already-established iSCSI mount. Guest starts call this path:
// a lost mount/session must block the guest, never cause Core to log in or mount during a start.
func inspectISCSI(ctx context.Context, s protocol.StorageRegistration) (storageAccess, error) {
	v, err := validISCSI(s)
	if err != nil {
		return storageAccess{}, err
	}
	if s.Identity.ISCSI == nil {
		return storageAccess{}, errors.New("iSCSI storage has no recorded identity")
	}
	out, err := nativeRun(ctx, "lsblk", "--json", "--output", "PATH,TYPE,UUID,WWN,FSTYPE,HCTL")
	if err != nil {
		return storageAccess{}, err
	}
	filesystem, err := iscsiFilesystemForLUNInfo(out, v.FilesystemUUID, v.LUN)
	if err != nil {
		return storageAccess{}, err
	}
	if canonicalWWID(filesystem.WWID) != canonicalWWID(v.WWID) || canonicalWWID(filesystem.WWID) != canonicalWWID(s.Identity.ISCSI.WWID) {
		return storageAccess{}, errors.New("iSCSI LUN WWID does not match registered identity")
	}
	if out, err = nativeRun(ctx, "findmnt", "-n", "-o", "SOURCE,UUID,FSTYPE,OPTIONS,TARGET", "--target", v.MountPath); err != nil {
		return storageAccess{}, errors.New("iSCSI filesystem is not mounted at its registered path")
	}
	parts, mountErr := iscsiMountFields(out)
	if mountErr != nil {
		return storageAccess{}, mountErr
	}
	if len(parts) != 5 || parts[0] != filesystem.Path || !strings.EqualFold(parts[1], v.FilesystemUUID) || parts[2] != filesystem.FSType || strings.Contains(","+parts[3]+",", ",ro,") || parts[4] != v.MountPath {
		return storageAccess{}, errors.New("iSCSI filesystem mount does not match its registered LUN")
	}
	a, err := storagePathAccess(v.MountPath, "iscsi:"+v.TargetIQN+":"+fmt.Sprint(v.LUN)+":"+filesystem.WWID, false)
	if err == nil {
		a.Observed.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: v.TargetIQN, LUN: v.LUN, WWID: filesystem.WWID, FilesystemUUID: v.FilesystemUUID}
	}
	return a, err
}

func iscsiMountFields(out []byte) ([]string, error) {
	var fields []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		row := strings.Fields(line)
		if len(row) != 5 {
			return nil, errors.New("iSCSI filesystem mount does not match its registered LUN")
		}
		if fields == nil {
			fields = row
			continue
		}
		if strings.Join(fields, "\x00") != strings.Join(row, "\x00") {
			return nil, errors.New("iSCSI filesystem mount does not match its registered LUN")
		}
	}
	return fields, nil
}

// iscsiFilesystem finds the unique existing filesystem with the recorded UUID. lsblk supplies
// a parent's WWN for partition filesystems, which is the stable SCSI WWID used for mounting.
func iscsiFilesystem(raw []byte, uuid string) (string, string, error) {
	return iscsiFilesystemForLUN(raw, uuid, ^uint64(0))
}

func iscsiFilesystemForLUN(raw []byte, uuid string, lun uint64) (string, string, error) {
	info, err := iscsiFilesystemForLUNInfo(raw, uuid, lun)
	return info.WWID, info.FSType, err
}

type iscsiFilesystemInfo struct {
	Path   string
	WWID   string
	FSType string
}

func iscsiFilesystemForLUNInfo(raw []byte, uuid string, lun uint64) (iscsiFilesystemInfo, error) {
	var list iscsiBlockList
	if err := json.Unmarshal(raw, &list); err != nil {
		return iscsiFilesystemInfo{}, errors.New("invalid lsblk output")
	}
	var found []iscsiFilesystemInfo
	var walk func(iscsiBlock, string, string)
	walk = func(b iscsiBlock, parentWWN, parentHCTL string) {
		wwid := b.WWN
		if wwid == "" {
			wwid = parentWWN
		}
		hctl := b.HCTL
		if hctl == "" {
			hctl = parentHCTL
		}
		if b.UUID == uuid && b.Path != "" && (b.FSType == "ext4" || b.FSType == "xfs") && iscsiWWID.MatchString(wwid) && (lun == ^uint64(0) || iscsiHCTLMatches(hctl, lun)) {
			found = append(found, iscsiFilesystemInfo{Path: b.Path, WWID: wwid, FSType: b.FSType})
		}
		for _, child := range b.Children {
			walk(child, wwid, hctl)
		}
	}
	for _, b := range list.BlockDevices {
		walk(b, "", "")
	}
	if len(found) != 1 {
		return iscsiFilesystemInfo{}, errors.New("iSCSI filesystem UUID must identify exactly one ext4 or XFS LUN")
	}
	return found[0], nil
}

func iscsiHCTLMatches(hctl string, lun uint64) bool {
	parts := strings.Split(hctl, ":")
	if len(parts) != 4 {
		return false
	}
	var got uint64
	for _, r := range parts[3] {
		if r < '0' || r > '9' {
			return false
		}
		digit := uint64(r - '0')
		if got > (^uint64(0)-digit)/10 {
			return false
		}
		got = got*10 + digit
	}
	return got == lun
}

func detachISCSI(ctx context.Context, s protocol.StorageRegistration) error {
	v, err := validISCSI(s)
	if err != nil {
		return err
	}
	if err := migrateLegacyISCSIOwner(ctx, v); err != nil {
		return err
	}
	unit, unitPath := iscsiUnitPath(s)
	content, err := os.ReadFile(unitPath)
	if err == nil {
		if !strings.HasPrefix(string(content), nexMountMarker) {
			return errors.New("refusing to remove an administrator mount unit")
		}
		if _, err = nativeRun(ctx, "systemctl", "disable", unit); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A detach replay may run after the mount was removed but before the journal tombstone
	// persisted. Only unmount when it still exists, then continue Core-owned cleanup.
	if mount, findErr := nativeRun(ctx, "findmnt", "-n", "-o", "TARGET,SOURCE", "--target", v.MountPath); findErr == nil {
		parts := strings.Fields(strings.TrimSpace(string(mount)))
		if len(parts) == 2 && parts[0] == v.MountPath {
			out, lookupErr := nativeRun(ctx, "lsblk", "--json", "--output", "PATH,TYPE,UUID,WWN,FSTYPE,HCTL")
			if lookupErr != nil {
				return fmt.Errorf("verify mounted iSCSI LUN before detach: %w", lookupErr)
			}
			filesystem, lookupErr := iscsiFilesystemForLUNInfo(out, v.FilesystemUUID, v.LUN)
			if lookupErr != nil {
				return fmt.Errorf("verify mounted iSCSI LUN before detach: %w", lookupErr)
			}
			if parts[1] != filesystem.Path {
				return errors.New("refusing to unmount an iSCSI mount with an unexpected source")
			}
			if _, err = nativeRun(ctx, "systemctl", "stop", unit); err != nil {
				return err
			}
		}
	}
	if nexISCSINode(ctx, v) {
		if _, nodeErr := os.Stat(iscsiNodePath(v)); errors.Is(nodeErr, os.ErrNotExist) {
			// Crash replay after successful iscsiadm delete but before owner cleanup.
			if err := os.Remove(iscsiOwnerPath(ctx, v)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			goto detachedNode
		} else if nodeErr != nil {
			return nodeErr
		}
		// Migrate marker-only records before iscsiadm rewrites the node body.
		if !nexISCSIOwner(ctx, v) {
			if err := writeNexISCSIOwner(ctx, v); err != nil {
				return err
			}
		}
		if _, err = nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--op", "update", "-n", "node.startup", "-v", "manual"); err != nil {
			return err
		}
		active, activeErr := iscsiSessionActive(ctx, v)
		if activeErr != nil {
			return activeErr
		}
		if active {
			if _, err = nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--logout"); err != nil {
				return err
			}
		}
		if err := removeNexISCSINode(ctx, v); err != nil {
			return err
		}
	}
detachedNode:
	if err = os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err = nativeRun(ctx, "systemctl", "daemon-reload")
	return err
}

func nexISCSINode(ctx context.Context, v *protocol.ISCSIStorageSource) bool {
	if nexISCSIOwner(ctx, v) {
		return true
	}
	b, err := readSmall(iscsiNodePath(v), 1<<20)
	return err == nil && strings.Contains(string(b), nexISCSINodeMarker)
}
