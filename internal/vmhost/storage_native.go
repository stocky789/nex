// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Native repository work stays here and in the backend-specific files.  The managed
// journal calls these helpers after it has recorded intent; they never make a missing
// mountpoint look like usable storage.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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
	ZFSPool          *protocol.ZFSPoolStatus
}

// observedStorageAccess is the transient result every report, placement check and monitor
// pass publishes from one native inspection.
func observedStorageAccess(a storageAccess, at time.Time) protocol.StorageAccess {
	available := a.Available
	status := a.Health
	if status == "" {
		status = "healthy"
	}
	return protocol.StorageAccess{Status: status, Readable: true, Writable: !a.ReadOnly, TotalBytes: a.Total, AvailableBytes: &available, ObservedAt: at, ZFSPool: a.ZFSPool}
}

// storageServesData accepts a ZFS pool that lost redundancy: "degraded" still reads and
// writes, and refusing it would take working VMs down over a disk that has a replica.
func storageServesData(health string) bool { return health == "healthy" || health == "degraded" }

var (
	nativeRun          = run
	qemuStorageStatfs  = syscall.Statfs
	qemuStorageFstatfs = syscall.Fstatfs
	qemuMachineID      = os.ReadFile
	qemuProbeRemove    = os.Remove
	qemuGFSRootTrusted = trustedManagedPath
	qemuProbeOwner     = func(uid uint32) bool { return uid == 0 }
	qemuProbeMountFile = qemuProbeMountIDFile
	qemuProbeStat      = (*os.File).Stat
	qemuProbeChmod     = (*os.File).Chmod
	qemuRunInDir       = func(ctx context.Context, dir *os.File, name string, args ...string) ([]byte, error) {
		return runWithDir(ctx, dir, name, args...)
	}
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
	if got.Path != want.Path || got.Identity != want.Identity || got.ReadOnly || !storageServesData(got.Health) {
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

const (
	gfs2SuperMagic = 0x01161970
	nfsSuperMagic  = 0x6969 // NFS_SUPER_MAGIC from linux/magic.h.
)

var machineIDHex = regexp.MustCompile(`^[0-9a-f]{32}$`)

var errQEMUProbeLeafOwner = errors.New("QEMU probe directory owner is unsuitable")

type qemuGFSProofKey struct {
	mount string
	dev   uint64
	ino   uint64
	uid   int
}

type qemuGFSProofFlight struct {
	done chan struct{}
	err  error
}

var qemuGFSProofFlights = struct {
	sync.Mutex
	byKey map[qemuGFSProofKey]*qemuGFSProofFlight
}{byKey: map[qemuGFSProofKey]*qemuGFSProofFlight{}}

// qemuGFSProbeDirectory returns an opened private per-Core leaf only for GFS2 or NFS.
// ponytail: isolation requires a trusted ancestry above the mount root; all other
// paths retain the original full root proof.
func qemuGFSProbeDirectory(ctx context.Context, path string) (*os.File, bool, error) {
	var rootFS syscall.Statfs_t
	if err := qemuStorageStatfs(path, &rootFS); err != nil || !qemuProbeFilesystem(uint64(rootFS.Type)) {
		return nil, false, nil
	}
	if err := qemuGFSRootTrusted(filepath.Dir(path)); err != nil {
		return nil, false, nil
	}
	root, err := qemuOpenProbeDirectory(path)
	if err != nil {
		return nil, true, errors.New("GFS2 QEMU probe root is unavailable")
	}
	defer root.Close()
	var heldFS syscall.Statfs_t
	if err := qemuStorageFstatfs(int(root.Fd()), &heldFS); err != nil || uint64(heldFS.Type) != uint64(rootFS.Type) {
		return nil, true, errors.New("network QEMU probe root changed during validation")
	}
	rootMount, err := qemuProbeMountFile(root)
	if err != nil {
		return nil, true, err
	}
	machineID, err := qemuMachineID("/etc/machine-id")
	if err != nil {
		return nil, true, errors.New("GFS2 QEMU probe machine identity is unavailable")
	}
	id := strings.TrimSpace(string(machineID))
	if !machineIDHex.MatchString(id) || id == strings.Repeat("0", 32) {
		return nil, true, errors.New("GFS2 QEMU probe machine identity is invalid")
	}
	mac := hmac.New(sha256.New, []byte("nex-qemu-probe-directory-v1"))
	_, _ = mac.Write([]byte(id))
	leafName := ".nex-qemu-probe-" + hex.EncodeToString(mac.Sum(nil))
	leaf, err := qemuOpenGFSProbeLeaf(ctx, root, leafName)
	if err != nil {
		// A root-squashed NFS export may map Core away from UID 0. Keep its
		// established full-root proof instead of accepting a non-root leaf.
		if uint64(rootFS.Type) == nfsSuperMagic && errors.Is(err, errQEMUProbeLeafOwner) {
			return nil, false, nil
		}
		return nil, true, err
	}
	leafMount, err := qemuProbeMountFile(leaf)
	if err != nil || rootMount != leafMount {
		leaf.Close()
		return nil, true, errors.New("GFS2 QEMU probe directory is outside the repository mount")
	}
	return leaf, true, nil
}

func qemuProbeFilesystem(fsType uint64) bool {
	return fsType == gfs2SuperMagic || fsType == nfsSuperMagic
}

func qemuOpenProbeDirectory(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		f.Close()
		return nil, errors.New("GFS2 QEMU probe root is unavailable")
	}
	return f, nil
}

func qemuOpenGFSProbeLeaf(ctx context.Context, root *os.File, leafName string) (*os.File, error) {
	fd, err := syscall.Openat(int(root.Fd()), leafName, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, mkdirErr := qemuRunInDir(ctx, root, "mkdir", "--mode=0711", "--", leafName); mkdirErr != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// A concurrent creator may have completed the leaf. It is never modified
			// here; the anchored descriptor validation below accepts it or fails closed.
		}
		fd, err = syscall.Openat(int(root.Fd()), leafName, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, errors.New("GFS2 QEMU probe directory is unavailable")
	}
	leaf := os.NewFile(uintptr(fd), leafName)
	info, statErr := leaf.Stat()
	if statErr != nil || info == nil {
		leaf.Close()
		return nil, errors.New("GFS2 QEMU probe directory is unsafe")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0711 {
		leaf.Close()
		return nil, errors.New("GFS2 QEMU probe directory is unsafe")
	}
	if !qemuProbeOwner(st.Uid) {
		leaf.Close()
		return nil, errQEMUProbeLeafOwner
	}
	return leaf, nil
}

func qemuProbeMountIDFile(dir *os.File) (string, error) {
	info, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(int(dir.Fd())))
	if err != nil {
		return "", errors.New("GFS2 QEMU probe mount is unavailable")
	}
	for _, line := range strings.Split(string(info), "\n") {
		if value, ok := strings.CutPrefix(line, "mnt_id:\t"); ok && value != "" {
			return value, nil
		}
	}
	return "", errors.New("GFS2 QEMU probe mount is unavailable")
}

func qemuCreateTemp(dir *os.File) (*os.File, string, error) {
	base := "/proc/self/fd/" + strconv.Itoa(int(dir.Fd()))
	f, err := os.CreateTemp(base, ".nex-qemu-access-")
	if err != nil {
		return nil, "", err
	}
	return f, filepath.Base(f.Name()), nil
}

func qemuRemoveTemp(dir *os.File, name string) error {
	return qemuProbeRemove("/proc/self/fd/" + strconv.Itoa(int(dir.Fd())) + "/" + name)
}

// qemuGFSProofKeyFor identifies an already-opened GFS2 or NFS leaf. A failed
// extra observation leaves the existing independent proof unchanged.
func qemuGFSProofKeyFor(dir *os.File, uid int) (qemuGFSProofKey, bool) {
	var fs syscall.Statfs_t
	if err := qemuStorageFstatfs(int(dir.Fd()), &fs); err != nil || (uint64(fs.Type) != gfs2SuperMagic && uint64(fs.Type) != nfsSuperMagic) {
		return qemuGFSProofKey{}, false
	}
	mount, err := qemuProbeMountFile(dir)
	if err != nil {
		return qemuGFSProofKey{}, false
	}
	info, err := dir.Stat()
	if err != nil {
		return qemuGFSProofKey{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return qemuGFSProofKey{}, false
	}
	return qemuGFSProofKey{mount: mount, dev: uint64(st.Dev), ino: st.Ino, uid: uid}, true
}

// qemuGFSStorageAccess shares only a currently-running proof of the same held
// GFS2 or NFS leaf. It never retains a completed health result.
func qemuGFSStorageAccess(ctx context.Context, dir *os.File, uid int, key qemuGFSProofKey) error {
	qemuGFSProofFlights.Lock()
	flight := qemuGFSProofFlights.byKey[key]
	if flight == nil {
		flight = &qemuGFSProofFlight{done: make(chan struct{})}
		qemuGFSProofFlights.byKey[key] = flight
		qemuGFSProofFlights.Unlock()

		flight.err = qemuStorageAccessProbe(ctx, dir, "", uid)
		if flight.err == nil && ctx.Err() != nil {
			flight.err = ctx.Err()
		}

		qemuGFSProofFlights.Lock()
		delete(qemuGFSProofFlights.byKey, key)
		close(flight.done)
		qemuGFSProofFlights.Unlock()
		return flight.err
	}
	qemuGFSProofFlights.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-flight.done:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if flight.err == nil {
		return nil
	}
	// A failed leader is never a shared proof. A live waiter gets one fresh
	// proof using its own held descriptor and existing context.
	return qemuStorageAccessProbe(ctx, dir, "", uid)
}

func qemuStorageAccessProbe(ctx context.Context, probeDir *os.File, path string, uid int) (retErr error) {
	var f *os.File
	var probe, probeName string
	var err error
	if probeDir != nil {
		f, probeName, err = qemuCreateTemp(probeDir)
		probe = probeName
	} else {
		f, err = os.CreateTemp(path, ".nex-qemu-access-")
		if err == nil {
			probe = f.Name()
		}
	}
	if err != nil {
		return errors.New("storage cannot create a QEMU access probe")
	}
	if probeDir != nil {
		defer func() {
			if err := qemuRemoveTemp(probeDir, probeName); err != nil && retErr == nil {
				retErr = errors.New("cannot remove GFS2 QEMU access probe")
			}
		}()
	} else {
		defer os.Remove(probe)
	}
	if err = f.Chown(uid, -1); err == nil {
		info, statErr := qemuProbeStat(f)
		st, statOK := (*syscall.Stat_t)(nil), false
		if statErr == nil && info != nil {
			st, statOK = info.Sys().(*syscall.Stat_t)
		}
		if statErr != nil || info == nil || !statOK || st == nil || info.Mode() != 0600 || st.Uid != uint32(uid) {
			err = qemuProbeChmod(f, 0600)
		}
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
		if probeDir != nil {
			_, err = qemuRunInDir(ctx, probeDir, "runuser", "-u", "libvirt-qemu", "--", "test", option, probe)
		} else {
			_, err = nativeRun(ctx, "runuser", "-u", "libvirt-qemu", "--", "test", option, probe)
		}
		if err != nil {
			return fmt.Errorf("storage probe is not accessible to libvirt-qemu: %w", err)
		}
	}
	return nil
}

func qemuStorageAccess(ctx context.Context, path string) error {
	// QEMU normally writes root-created, QEMU-owned disk files; it need not create a directory
	// entry itself. Probe that real access model without weakening root-controlled ancestors.
	if _, err := nativeRun(ctx, "runuser", "-u", "libvirt-qemu", "--", "test", "-x", path); err != nil {
		return fmt.Errorf("storage is not traversable by libvirt-qemu: %w", err)
	}
	probeDir, isolated, err := qemuGFSProbeDirectory(ctx, path)
	if err != nil {
		return err
	}
	if isolated {
		defer probeDir.Close()
	}
	uidOut, err := nativeRun(ctx, "id", "-u", "libvirt-qemu")
	if err != nil {
		return fmt.Errorf("cannot resolve libvirt-qemu identity: %w", err)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(uidOut)))
	if err != nil || uid < 0 {
		return errors.New("invalid libvirt-qemu identity")
	}
	if isolated {
		if key, ok := qemuGFSProofKeyFor(probeDir, uid); ok {
			return qemuGFSStorageAccess(ctx, probeDir, uid, key)
		}
		return qemuStorageAccessProbe(ctx, probeDir, "", uid)
	}
	return qemuStorageAccessProbe(ctx, nil, path, uid)
}
