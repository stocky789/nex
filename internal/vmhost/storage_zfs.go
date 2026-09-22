// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"nex.local/nex/internal/protocol"
)

var nativeZFSName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*(/[A-Za-z0-9][A-Za-z0-9_.:-]*)*$`)

// prepareZFS only admits an already imported and mounted filesystem dataset.  In
// particular it must not make an unavailable mountpoint look like a directory
// suitable for VM disks.
func prepareZFS(ctx context.Context, s protocol.StorageRegistration) (storageAccess, error) {
	a, err := inspectZFS(ctx, s)
	if err != nil {
		return storageAccess{}, err
	}
	if s.Path == "" {
		alias := remoteMountPath(s)
		dataset := s.Source.ZFS.Dataset
		if dataset != s.Source.ZFS.Pool && !strings.HasPrefix(dataset, s.Source.ZFS.Pool+"/") {
			dataset = s.Source.ZFS.Pool + "/" + dataset
		}
		if err := prepareZFSBind(ctx, s, dataset, a.Path, alias); err != nil {
			return storageAccess{}, err
		}
		a.Path = alias
	}
	path, err := storagePathAccess(a.Path, a.Identity, false)
	if err != nil {
		return storageAccess{}, err
	}
	// Dataset available includes its quota and reservation constraints; Statfs alone
	// can report free space in the pool that this dataset cannot consume.
	path.Available = a.Available
	path.Observed = a.Observed
	path.Health = a.Health
	path.ZFSPool = a.ZFSPool
	return path, nil
}

// inspectZFS reads the currently imported dataset and pool only.  It is safe to
// call before guest start: it never imports a pool, mounts a dataset, or changes
// any ZFS property.
func inspectZFS(ctx context.Context, s protocol.StorageRegistration) (storageAccess, error) {
	if s.Source.ZFS == nil {
		return storageAccess{}, errors.New("ZFS storage source is required")
	}
	v := s.Source.ZFS
	if !nativeZFSName.MatchString(v.Pool) || !nativeZFSName.MatchString(v.Dataset) || !protocolStoragePath(v.MountPath) || strings.ContainsAny(v.MountPath, "%\\") || strings.IndexFunc(v.MountPath, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return storageAccess{}, errors.New("invalid ZFS storage source")
	}
	dataset := v.Dataset
	if dataset != v.Pool && !strings.HasPrefix(dataset, v.Pool+"/") {
		dataset = v.Pool + "/" + dataset
	}
	poolGUID, datasetGUID, available, mounted, mountpoint, health, err := zfsProbe(ctx, v.Pool, dataset)
	if err != nil {
		return storageAccess{}, err
	}
	if mounted != "yes" || mountpoint != v.MountPath {
		return storageAccess{}, errors.New("ZFS dataset is not mounted at its registered mount path")
	}
	// A DEGRADED pool has lost redundancy but still serves every read and write, so the
	// repository stays in service until the failed device is replaced.  FAULTED, UNAVAIL,
	// SUSPENDED and REMOVED do not serve data and remain inaccessible.
	switch health {
	case "ONLINE":
		health = "healthy"
	case "DEGRADED":
		health = "degraded"
	default:
		return storageAccess{}, fmt.Errorf("ZFS pool health is %s", health)
	}
	if want := s.Identity.ZFS; want != nil && ((want.PoolGUID != "" && want.PoolGUID != poolGUID) || (want.DatasetGUID != "" && want.DatasetGUID != datasetGUID)) {
		return storageAccess{}, errors.New("ZFS dataset identity does not match its registration")
	}
	mount, err := nativeRun(ctx, "findmnt", "--noheadings", "--output", "SOURCE,FSTYPE", "--target", mountpoint)
	if err != nil || strings.TrimSpace(string(mount)) != dataset+" zfs" {
		return storageAccess{}, errors.New("ZFS mount source does not match its dataset")
	}
	path := mountpoint
	if s.Path != "" {
		alias := remoteMountPath(s)
		if s.Path == alias {
			if err := verifyZFSBind(ctx, dataset, alias); err != nil {
				return storageAccess{}, err
			}
			path = alias
		} else if s.Path != mountpoint {
			return storageAccess{}, errors.New("ZFS storage path is neither its dataset mount nor Core alias")
		}
	}
	a, err := storagePathAccess(path, "zfs:"+poolGUID+":"+datasetGUID, false)
	if err != nil {
		return storageAccess{}, err
	}
	a.Available = available
	a.Observed = protocol.StorageIdentity{ZFS: &protocol.ZFSStorageIdentity{PoolGUID: poolGUID, DatasetGUID: datasetGUID}}
	a.Health = health
	a.ZFSPool = zfsPoolStatus(ctx, v.Pool)
	return a, nil
}

// New registrations bind an existing dataset into Core's writable, host-visible root.
// Stored legacy paths are never changed: their existing VM disk references stay honest.
func prepareZFSBind(ctx context.Context, s protocol.StorageRegistration, dataset, source, alias string) error {
	if err := os.MkdirAll(alias, 0755); err != nil {
		return err
	}
	unit := systemdMountUnit(alias)
	path := filepath.Join(storageUnitRoot, unit)
	_, statErr := os.Lstat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return statErr
	}
	content := zfsBindUnit(source, alias)
	if err := writeManagedMountUnit(path, content); err != nil {
		return err
	}
	if _, err := nativeRun(ctx, "systemctl", "daemon-reload"); err != nil {
		if created {
			if rollbackErr := rollbackZFSBind(ctx, unit, path); rollbackErr != nil {
				return fmt.Errorf("%w: %v", ErrStorageRollbackUncertain, rollbackErr)
			}
		}
		return err
	}
	if _, err := nativeRun(ctx, "systemctl", "enable", "--now", unit); err != nil {
		if created {
			if rollbackErr := rollbackZFSBind(ctx, unit, path); rollbackErr != nil {
				return fmt.Errorf("%w: %v", ErrStorageRollbackUncertain, rollbackErr)
			}
		}
		_, _ = nativeRun(ctx, "systemctl", "daemon-reload")
		return err
	}
	if err := verifyZFSBind(ctx, dataset, alias); err != nil {
		if created {
			if rollbackErr := rollbackZFSBind(ctx, unit, path); rollbackErr != nil {
				return fmt.Errorf("%w: %v", ErrStorageRollbackUncertain, rollbackErr)
			}
		}
		_, _ = nativeRun(ctx, "systemctl", "daemon-reload")
		return err
	}
	return nil
}

func rollbackZFSBind(ctx context.Context, unit, path string) error {
	var errs []error
	if _, err := nativeRun(ctx, "systemctl", "disable", "--now", unit); err != nil {
		errs = append(errs, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	if _, err := nativeRun(ctx, "systemctl", "daemon-reload"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func zfsBindUnit(source, alias string) []byte {
	return []byte(nexMountMarker + "[Unit]\nAfter=zfs-mount.service\nRequiresMountsFor=" + source + "\nBefore=libvirtd.service\n\n[Mount]\nWhat=" + source + "\nWhere=" + alias + "\nType=none\nOptions=bind\n\n[Install]\nWantedBy=multi-user.target\n")
}

func verifyZFSBind(ctx context.Context, dataset, alias string) error {
	out, err := nativeRun(ctx, "findmnt", "--noheadings", "--output", "SOURCE,FSTYPE,TARGET", "--target", alias)
	var parts []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		row := strings.Fields(line)
		if len(row) != 3 || (parts != nil && strings.Join(parts, "\x00") != strings.Join(row, "\x00")) {
			return errors.New("ZFS Core alias does not match its dataset mount")
		}
		parts = row
	}
	if err != nil || len(parts) != 3 || (parts[0] != dataset && parts[0] != dataset+"[/]") || parts[1] != "zfs" || parts[2] != alias {
		return errors.New("ZFS Core alias does not match its dataset mount")
	}
	return nil
}

func detachZFSBind(ctx context.Context, s protocol.StorageRegistration) error {
	alias := remoteMountPath(s)
	if s.Path != alias {
		return nil // legacy dataset mount; Nex never owned it.
	}
	unit := systemdMountUnit(alias)
	path := filepath.Join(storageUnitRoot, unit)
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.Source.ZFS == nil {
		return errors.New("ZFS storage source is required")
	}
	expected := zfsBindUnit(s.Source.ZFS.MountPath, alias)
	if string(content) != string(expected) {
		return errors.New("refusing to remove an administrator ZFS alias unit")
	}
	if _, err := nativeRun(ctx, "systemctl", "disable", "--now", unit); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	_, err = nativeRun(ctx, "systemctl", "daemon-reload")
	return err
}

func zfsProbe(ctx context.Context, pool, dataset string) (poolGUID, datasetGUID string, available uint64, mounted, mountpoint, health string, err error) {
	values, err := nativeRun(ctx, "zfs", "get", "-H", "-p", "-o", "value", "guid,available,mounted,mountpoint", dataset)
	if err != nil {
		return "", "", 0, "", "", "", fmt.Errorf("read ZFS dataset: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(values)), "\n")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return "", "", 0, "", "", "", errors.New("invalid ZFS dataset properties")
	}
	available, err = strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return "", "", 0, "", "", "", errors.New("invalid ZFS available capacity")
	}
	poolOut, err := nativeRun(ctx, "zpool", "get", "-H", "-p", "-o", "value", "guid", pool)
	if err != nil || strings.TrimSpace(string(poolOut)) == "" {
		if err != nil {
			return "", "", 0, "", "", "", fmt.Errorf("read ZFS pool: %w", err)
		}
		return "", "", 0, "", "", "", errors.New("invalid ZFS pool GUID")
	}
	healthOut, err := nativeRun(ctx, "zpool", "list", "-H", "-o", "health", pool)
	if err != nil {
		return "", "", 0, "", "", "", fmt.Errorf("read ZFS pool health: %w", err)
	}
	health = strings.TrimSpace(string(healthOut))
	if health == "" || strings.ContainsAny(health, " \t\r\n") {
		return "", "", 0, "", "", "", errors.New("invalid ZFS pool health")
	}
	return strings.TrimSpace(string(poolOut)), parts[0], available, parts[2], parts[3], health, nil
}

// protocolStoragePath duplicates the protocol's intentionally private syntax check at
// the Core trust boundary; protocol validation must not be the only defence here.
func protocolStoragePath(path string) bool {
	return filepath.IsAbs(path) && path != "/" && filepath.Clean(path) == path
}
