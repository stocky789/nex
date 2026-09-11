// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

const smbMarkerName = ".nex-storage-marker"

var smbMarkerID = regexp.MustCompile(`^[0-9a-f]{32}$`)

var syncSMBMarkerDir = func(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

var lstatSMBMarker = os.Lstat

// smbMarker returns the share-root marker after proving it is one regular, unlinked file.
// A CIFS fsid includes server metadata that can change across a reconnect, so only the marker
// identifies a new SMB repository. Marker failures are deliberately admission failures.
func smbMarker(path string) (string, error) { return smbMarkerAt(path, true) }

// smbMarkerAt reads an existing marker without mutation unless creation is explicitly
// authorized for a new repository registration.
func smbMarkerAt(path string, allowCreate bool) (string, error) {
	marker := filepath.Join(path, smbMarkerName)
	f, err := os.OpenFile(marker, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	created := false
	if errors.Is(err, os.ErrNotExist) && allowCreate {
		f, err = os.OpenFile(marker, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		created = err == nil
	}
	if err != nil {
		return "", errors.New("SMB repository marker is unavailable")
	}
	defer func() { _ = f.Close() }()
	reopen := false
	if created {
		var random [16]byte
		if _, err = rand.Read(random[:]); err == nil {
			_, err = f.WriteString(hex.EncodeToString(random[:]))
		}
		if err == nil {
			err = f.Sync()
		}
		if err == nil {
			// SMB must support syncing the marker and its containing directory. Unsupported
			// directory-sync errors are tolerated only where CIFS does not implement it; the
			// exact marker is reopened below and any later absence fails closed.
			err = syncSMBMarkerDir(path)
			if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EOPNOTSUPP) {
				err = nil
				reopen = true
			}
		}
		if err != nil {
			return "", errors.New("SMB repository marker cannot be persisted")
		}
	}
	if reopen {
		if err = f.Close(); err != nil {
			return "", errors.New("SMB repository marker is unavailable")
		}
		f, err = os.OpenFile(marker, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return "", errors.New("SMB repository marker is unavailable")
		}
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", errors.New("SMB repository marker is unavailable")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("SMB repository marker is invalid")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return "", errors.New("SMB repository marker is not exclusively linked")
	}
	data, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || !smbMarkerID.Match(data) {
		return "", errors.New("SMB repository marker is invalid")
	}
	entry, err := lstatSMBMarker(marker)
	if err != nil {
		return "", errors.New("SMB repository marker changed during inspection")
	}
	entrySt, entryOK := entry.Sys().(*syscall.Stat_t)
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() || !entryOK || entrySt.Dev != st.Dev || entrySt.Ino != st.Ino || entrySt.Nlink != st.Nlink {
		return "", errors.New("SMB repository marker changed during inspection")
	}
	return string(data), nil
}
