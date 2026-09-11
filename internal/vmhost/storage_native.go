// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Native repository work stays here and in the backend-specific files.  The managed
// journal calls these helpers after it has recorded intent; they never make a missing
// mountpoint look like usable storage.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"nex.local/nex/internal/protocol"
)

type storageAccess struct {
	Path             string
	Identity         string
	Observed         protocol.StorageIdentity
	FilesystemID     string
	Total, Available uint64
	ReadOnly         bool
	Health           string
}

var (
	nativeRun = run
	// VM repositories must sit outside Core's 0700 state directory: libvirt-qemu needs to
	// traverse them. Credentials and journals remain below the private agent state directory.
	storageMountRoot      = "/var/lib/nex-storage"
	storageCredentialRoot = "/var/lib/nex-core-agent/storage-credentials" // legacy direct-helper default
	storageUnitRoot       = "/etc/systemd/system"
)

// ErrStorageRollbackUncertain means a storage mutation failed and Nex could not prove
// restoration of the previous usable repository. Callers must retain needs_attention.
var ErrStorageRollbackUncertain = errors.New("storage rollback is uncertain")

var storageID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

const statfsReadOnly = 1 // Linux ST_RDONLY; syscall does not expose it on every Go target.

func storagePrepare(ctx context.Context, s protocol.StorageRegistration) (storageAccess, error) {
	if !storageID.MatchString(s.ID) {
		return storageAccess{}, errors.New("invalid storage ID")
	}
	switch s.StorageKind() {
	case protocol.StorageKindLocal:
		return prepareLocal(s)
	case protocol.StorageKindNFS:
		return prepareNFS(ctx, s)
	case protocol.StorageKindZFS:
		return prepareZFS(ctx, s)
	case protocol.StorageKindSMB:
		return prepareSMB(ctx, s)
	case protocol.StorageKindISCSI:
		return prepareISCSI(ctx, s)
	default:
		return storageAccess{}, errors.New("unsupported storage kind")
	}
}

type storageCredentialRootKey struct{}
type storagePreviousKey struct{}
type storageOwnerRootKey struct{}

// storagePrepareAt binds credential lookup to this Core engine's state directory.  It avoids
// process-global roots when tests or multiple Core engines use distinct state directories.
func storagePrepareAt(ctx context.Context, s protocol.StorageRegistration, credentialRoot string) (storageAccess, error) {
	if !filepath.IsAbs(credentialRoot) || filepath.Clean(credentialRoot) != credentialRoot {
		return storageAccess{}, errors.New("invalid Core credential directory")
	}
	ctx = context.WithValue(ctx, storageCredentialRootKey{}, credentialRoot)
	return storagePrepare(context.WithValue(ctx, storageOwnerRootKey{}, filepath.Join(filepath.Dir(credentialRoot), "iscsi-owners")), s)
}

// storagePrepareWithPrevious carries the durable pre-update record across a retry. It lets
// iSCSI finish a credential reconnect after a crash that already changed the node file.
func storagePrepareWithPrevious(ctx context.Context, s protocol.StorageRegistration, credentialRoot string, previous protocol.StorageRegistration) (storageAccess, error) {
	ctx = context.WithValue(ctx, storagePreviousKey{}, previous)
	return storagePrepareAt(ctx, s, credentialRoot)
}

func storageVerify(ctx context.Context, s protocol.StorageRegistration, want storageAccess) error {
	got, err := storageInspect(ctx, s)
	if err != nil {
		return err
	}
	if got.Path != want.Path || got.Identity != want.Identity || got.ReadOnly || got.Health != "healthy" {
		return errors.New("storage access changed or is unavailable")
	}
	return qemuStorageAccess(ctx, got.Path)
}

// storageInspect is deliberately read-only.  Guest starts and libvirt autostart call it to
// reject a lost or replaced repository without reconnecting a target or creating a mount.
func storageInspect(ctx context.Context, s protocol.StorageRegistration) (storageAccess, error) {
	switch s.StorageKind() {
	case protocol.StorageKindLocal:
		return prepareLocal(s)
	case protocol.StorageKindNFS:
		return inspectNFS(ctx, s)
	case protocol.StorageKindZFS:
		return inspectZFS(ctx, s)
	case protocol.StorageKindSMB:
		return inspectSMB(ctx, s)
	case protocol.StorageKindISCSI:
		return inspectISCSI(ctx, s)
	default:
		return storageAccess{}, errors.New("unsupported storage kind")
	}
}

func storageDetach(ctx context.Context, s protocol.StorageRegistration) error {
	switch s.StorageKind() {
	case protocol.StorageKindLocal:
		return nil
	case protocol.StorageKindZFS:
		return detachZFSBind(ctx, s)
	case protocol.StorageKindNFS, protocol.StorageKindSMB:
		return detachRemote(ctx, s)
	case protocol.StorageKindISCSI:
		return detachISCSI(ctx, s)
	default:
		return errors.New("unsupported storage kind")
	}
}

// storageDetachAt uses the same per-engine private state root as preparation so
// iSCSI ownership records remain discoverable after a Core restart or custom state path.
func storageDetachAt(ctx context.Context, s protocol.StorageRegistration, credentialRoot string) error {
	if !filepath.IsAbs(credentialRoot) || filepath.Clean(credentialRoot) != credentialRoot {
		return errors.New("invalid Core credential directory")
	}
	ctx = context.WithValue(ctx, storageOwnerRootKey{}, filepath.Join(filepath.Dir(credentialRoot), "iscsi-owners"))
	return storageDetach(ctx, s)
}

// storageCleanupContext keeps Core-private credential and iSCSI owner roots after a
// cancelled or replaced request context. Failed preparation rollback must still find the
// receipts it wrote under the engine state directory.
func storageCleanupContext(ctx context.Context) context.Context {
	cleanup := context.Background()
	if root, _ := ctx.Value(storageCredentialRootKey{}).(string); root != "" {
		cleanup = context.WithValue(cleanup, storageCredentialRootKey{}, root)
	}
	if root, _ := ctx.Value(storageOwnerRootKey{}).(string); root != "" {
		cleanup = context.WithValue(cleanup, storageOwnerRootKey{}, root)
	}
	if previous, ok := ctx.Value(storagePreviousKey{}).(protocol.StorageRegistration); ok {
		cleanup = context.WithValue(cleanup, storagePreviousKey{}, previous)
	}
	return cleanup
}

func prepareLocal(s protocol.StorageRegistration) (storageAccess, error) {
	if s.Path == "" {
		return storageAccess{}, errors.New("storage path is required")
	}
	if err := localStorage(s.Path, 0); err != nil {
		return storageAccess{}, err
	}
	return storagePathAccess(s.Path, "local:"+s.Path, false)
}

func storagePathAccess(path, identity string, readonly bool) (storageAccess, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return storageAccess{}, errors.New("storage path must be a clean absolute path")
	}
	// The mountpoint itself may carry server-side ownership (for example NFS root-squash).
	// Its parent chain is still Core-controlled; access to the mounted leaf is tested below as
	// libvirt-qemu with a disposable file.
	if err := trustedManagedPath(filepath.Dir(path)); err != nil {
		return storageAccess{}, err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return storageAccess{}, errors.New("storage mountpoint is unavailable")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return storageAccess{}, err
	}
	fsid := fmt.Sprintf("%08x%08x", uint32(stat.Fsid.X__val[0]), uint32(stat.Fsid.X__val[1]))
	identity += ":fsid=" + fsid
	return storageAccess{Path: path, Identity: identity, FilesystemID: fsid, Total: stat.Blocks * uint64(stat.Bsize), Available: stat.Bavail * uint64(stat.Bsize), ReadOnly: readonly || stat.Flags&statfsReadOnly != 0, Health: "healthy"}, nil
}

func remoteSubpathAccess(mountRoot, directory string, base storageAccess) (storageAccess, error) {
	if directory == "" || filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "." || directory == ".." || strings.HasPrefix(directory, "../") {
		return storageAccess{}, errors.New("unsafe remote storage directory")
	}
	path := mountRoot
	for _, part := range strings.Split(directory, "/") {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return storageAccess{}, errors.New("remote storage directory is unavailable or unsafe")
		}
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return storageAccess{}, err
	}
	base.Path, base.Total, base.Available = path, stat.Blocks*uint64(stat.Bsize), stat.Bavail*uint64(stat.Bsize)
	base.ReadOnly = base.ReadOnly || stat.Flags&statfsReadOnly != 0
	return base, nil
}

// credentialFile only returns Core-owned, receipt-addressed files. Credentials never reach a
// command line or the Controller journal.
func credentialFile(ctx context.Context, s protocol.StorageRegistration) (string, error) {
	if !storageID.MatchString(s.CredentialReceipt) {
		return "", errors.New("storage credentials are unavailable")
	}
	root, _ := ctx.Value(storageCredentialRootKey{}).(string)
	if root == "" {
		root = storageCredentialRoot
	}
	p := filepath.Join(root, s.CredentialReceipt)
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", errors.New("storage credential file is unavailable")
	}
	return p, nil
}

func qemuStorageAccess(ctx context.Context, path string) error {
	// QEMU normally writes root-created, QEMU-owned disk files; it need not create a directory
	// entry itself. Probe that real access model without weakening root-controlled ancestors.
	if _, err := nativeRun(ctx, "runuser", "-u", "libvirt-qemu", "--", "test", "-x", path); err != nil {
		return fmt.Errorf("storage is not traversable by libvirt-qemu: %w", err)
	}
	uidOut, err := nativeRun(ctx, "id", "-u", "libvirt-qemu")
	if err != nil {
		return fmt.Errorf("cannot resolve libvirt-qemu identity: %w", err)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(uidOut)))
	if err != nil || uid < 0 {
		return errors.New("invalid libvirt-qemu identity")
	}
	f, err := os.CreateTemp(path, ".nex-qemu-access-")
	if err != nil {
		return errors.New("storage cannot create a QEMU access probe")
	}
	probe := f.Name()
	defer os.Remove(probe)
	if err = f.Chown(uid, -1); err == nil {
		err = f.Chmod(0600)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("cannot prepare QEMU access probe: %w", err)
	}
	for _, option := range []string{"-r", "-w"} {
		if _, err := nativeRun(ctx, "runuser", "-u", "libvirt-qemu", "--", "test", option, probe); err != nil {
			return fmt.Errorf("storage probe is not accessible to libvirt-qemu: %w", err)
		}
	}
	return nil
}
