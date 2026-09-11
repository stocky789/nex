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
	"time"
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
//
// Concurrent creators race on O_EXCL for the final name. The loser retries a short read loop
// so it observes the winner's fully written marker instead of an empty provisional file or a
// bare "unavailable" from the lost create.
func smbMarkerAt(path string, allowCreate bool) (string, error) {
	marker := filepath.Join(path, smbMarkerName)
	const attempts = 64
	for i := 0; i < attempts; i++ {
		id, state, err := inspectSMBMarker(marker)
		switch state {
		case smbMarkerOK:
			return id, nil
		case smbMarkerUnsafe:
			return "", err
		case smbMarkerIncomplete:
			if !allowCreate {
				return "", errors.New("SMB repository marker is unavailable")
			}
		case smbMarkerAbsent:
			if !allowCreate {
				return "", errors.New("SMB repository marker is unavailable")
			}
			if err := createSMBMarker(path, marker); err != nil {
				if !errors.Is(err, os.ErrExist) {
					return "", err
				}
				// Lost the create race; read the winner below.
			} else {
				continue
			}
		}
		if i+1 < attempts {
			time.Sleep(time.Millisecond)
		}
	}
	return "", errors.New("SMB repository marker is unavailable")
}

type smbMarkerState int

const (
	smbMarkerOK smbMarkerState = iota
	smbMarkerAbsent
	smbMarkerIncomplete
	smbMarkerUnsafe
)

func inspectSMBMarker(marker string) (string, smbMarkerState, error) {
	f, err := os.OpenFile(marker, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "", smbMarkerAbsent, err
	}
	if err != nil {
		return "", smbMarkerUnsafe, errors.New("SMB repository marker is unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", smbMarkerUnsafe, errors.New("SMB repository marker is invalid")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return "", smbMarkerUnsafe, errors.New("SMB repository marker is not exclusively linked")
	}
	// Empty or short files are the create/write window of another registration attempt.
	if info.Size() < 32 {
		return "", smbMarkerIncomplete, errors.New("SMB repository marker is unavailable")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", smbMarkerUnsafe, errors.New("SMB repository marker is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || !smbMarkerID.Match(data) {
		return "", smbMarkerUnsafe, errors.New("SMB repository marker is invalid")
	}
	entry, err := lstatSMBMarker(marker)
	if err != nil {
		return "", smbMarkerUnsafe, errors.New("SMB repository marker changed during inspection")
	}
	entrySt, entryOK := entry.Sys().(*syscall.Stat_t)
	if entry.Mode()&os.ModeSymlink != 0 || !entry.Mode().IsRegular() || !entryOK || entrySt.Dev != st.Dev || entrySt.Ino != st.Ino || entrySt.Nlink != st.Nlink {
		return "", smbMarkerUnsafe, errors.New("SMB repository marker changed during inspection")
	}
	return string(data), smbMarkerOK, nil
}

func createSMBMarker(path, marker string) error {
	f, err := os.OpenFile(marker, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	var random [16]byte
	var writeErr error
	if _, writeErr = rand.Read(random[:]); writeErr == nil {
		_, writeErr = f.WriteString(hex.EncodeToString(random[:]))
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if writeErr == nil {
		// SMB must support syncing the marker and its containing directory. Unsupported
		// directory-sync errors are tolerated only where CIFS does not implement it; the
		// exact marker is re-inspected by the caller and any later absence fails closed.
		writeErr = syncSMBMarkerDir(path)
		if errors.Is(writeErr, syscall.EINVAL) || errors.Is(writeErr, syscall.EOPNOTSUPP) {
			writeErr = nil
		}
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(marker)
		if writeErr != nil {
			return errors.New("SMB repository marker cannot be persisted")
		}
		return errors.New("SMB repository marker is unavailable")
	}
	return nil
}
