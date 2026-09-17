// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

// Bundled VirtIO Windows drivers ISO, staged by the Core installer under
// /usr/share/nex/media and published into the host ISO library on first enroll.
// Fedora's public virtio-win build ships BSD drivers plus GPL/LGPL guest tools;
// pin version, digest and retained corresponding source in the ISO builder and
// docs/LICENSING.md together.
const (
	BundledVirtIOWinPath    = "/usr/share/nex/media/virtio-win.iso"
	BundledVirtIOWinName    = "virtio-win.iso"
	BundledVirtIOWinVersion = "0.1.302"
)

// Digests are vars so unit tests can seed a miniature fixture without shipping 837 MiB.
var (
	BundledVirtIOWinSHA256 = "303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d"
	BundledVirtIOWinSize   = int64(877373440)
)

// SeedBundledMedia copies the installer-shipped virtio-win ISO into the host's
// default local media library when the package file is present and no matching
// library entry exists yet. Idempotent; missing package file is a no-op so
// package-only or older installs stay quiet. Durable state is a normal
// succeeded iso-import journal entry the Controller adopts from inventory.
func (e *ManagedEngine) SeedBundledMedia(ctx context.Context, pool, host string) error {
	return e.seedBundledMedia(ctx, pool, host, DefaultStoragePath)
}

// seedBundledMedia takes the default storage path so tests use a temporary tree while the
// production path stays a constant.
func (e *ManagedEngine) seedBundledMedia(ctx context.Context, pool, host, defaultPath string) error {
	if !identifier.MatchString(host) || !identifier.MatchString(pool) {
		return errors.New("bundled media needs the enrolled host and pool identity")
	}
	src := BundledVirtIOWinPath
	if override := strings.TrimSpace(os.Getenv("NEX_BUNDLED_VIRTIO_WIN")); override != "" {
		src = override
	}
	info, err := os.Lstat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != BundledVirtIOWinSize {
		return errors.New("bundled virtio-win ISO is missing or the wrong size")
	}
	js, err := e.journals()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	storageID, storagePath := bundledMediaTarget(js, defaultPath)
	if storageID == "" {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	dir := filepath.Join(storagePath, "nex-media")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	id, op := hostID(), hostID()
	path := filepath.Join(dir, id+".iso")
	if err := publishBundledISO(ctx, src, path); err != nil {
		return err
	}
	m := protocol.Media{
		ID: id, PoolID: pool, HostID: host, StorageID: storageID,
		Name: BundledVirtIOWinName, SizeBytes: BundledVirtIOWinSize, SHA256: BundledVirtIOWinSHA256,
		OperationID: op, Status: "succeeded",
	}
	if err := verifyISO(path, m); err != nil {
		_ = os.Remove(path)
		return err
	}
	// The copy and hash run unlocked so commands are never delayed; the decision to journal
	// is retaken under the operations lock, so a storage detach or another import that landed
	// meanwhile cannot leave media journaled against a registration that no longer exists.
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	defer unlock(lock)
	if js, err = e.journals(); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(path)
		return err
	}
	if id, p := bundledMediaTarget(js, defaultPath); id != storageID || p != storagePath {
		return os.Remove(path)
	}
	return e.save(managedJournal{
		Command:   protocol.Command{OperationID: op, Kind: "iso-import", Media: &m},
		Result:    protocol.Result{OperationID: op, Status: "succeeded", ObservedAt: time.Now().UTC()},
		Stage:     "published",
		MediaPath: path,
	})
}

// bundledMediaTarget returns the live default storage that should receive the bundled ISO,
// or nothing when it is already imported or no live registration holds the default path.
// Resolving through the storage projection never chooses a detached or superseded
// registration; the lowest live ID keeps the choice deterministic.
func bundledMediaTarget(js []managedJournal, defaultPath string) (storageID, storagePath string) {
	for _, j := range js {
		if j.Command.Kind != "iso-import" || j.Command.Media == nil {
			continue
		}
		m := journalMedia(j)
		if j.Result.Status == "succeeded" && m.SHA256 == BundledVirtIOWinSHA256 {
			return "", ""
		}
	}
	for id, s := range storageProjection(js) {
		if s.Path == defaultPath && (storageID == "" || id < storageID) {
			storageID, storagePath = id, storageRegistrationPath(s)
		}
	}
	if storagePath == "" {
		return "", ""
	}
	return storageID, storagePath
}

func publishBundledISO(ctx context.Context, src, dest string) error {
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return errors.New("ISO destination already exists or cannot be inspected")
	}
	if err := os.Link(src, dest); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	partial := dest + ".partial"
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(partial)
	defer out.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), &managedContextReader{ctx: ctx, r: in}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != BundledVirtIOWinSHA256 {
		return errors.New("bundled virtio-win ISO SHA-256 mismatch")
	}
	if err := out.Chmod(0444); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(partial, dest); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
