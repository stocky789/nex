// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Replacing a failed disk of a provisioned ZFS pool. ZFS owns everything durable: the pool
// records its own device set and retires the old disk when the resilver finishes. The
// journal below only keeps this host from erasing a disk twice after an interruption, and
// the resilver an operator watches is the ordinary pool observation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

type zfsReplaceJournal struct {
	Request    protocol.ZFSReplaceRequest `json:"request"`
	Stage      string                     `json:"stage,omitempty"`
	Status     string                     `json:"status"`
	Error      string                     `json:"error,omitempty"`
	ObservedAt time.Time                  `json:"observed_at"`
}

// ZFSReplacer performs one host's pool disk replacements, one at a time.
type ZFSReplacer struct {
	stateDir string
}

func NewZFSReplacer(stateDir string) *ZFSReplacer { return &ZFSReplacer{stateDir: stateDir} }

// zfsReplaceStatus is a seam for tests; the live path is the same observation the
// repository report already makes.
var zfsReplaceStatus = zfsPoolStatus

// saveZFSReplaceJournal is a var so tests can observe stage order against wipefs.
var saveZFSReplaceJournal = func(path string, journal zfsReplaceJournal) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return atomicWrite(path, raw)
}

// Replace swaps request.OldDevice for request.Device in this repository's pool. A
// redelivered operation ID replays its recorded outcome, and a pool that is already
// resilvering onto the replacement is adopted rather than acted on again.
func (r *ZFSReplacer) Replace(ctx context.Context, request protocol.ZFSReplaceRequest) error {
	if err := request.Valid(); err != nil {
		return err
	}
	if err := os.MkdirAll(r.stateDir, 0700); err != nil {
		return errors.New("Nex Core could not open its ZFS disk replacement journal")
	}
	unlock, err := withExclusiveDiskLock(r.stateDir, errors.New("the ZFS disk replacement journal is locked"))
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(r.stateDir, request.OperationID+".json")
	journal, err := readZFSReplaceJournal(path)
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return errors.New("the ZFS disk replacement journal is unreadable")
	case err == nil && (journal.Request.RepositoryID != request.RepositoryID || journal.Request.OldDevice != request.OldDevice || journal.Request.Device.ByID != request.Device.ByID):
		return errors.New("this operation was already used for a different ZFS disk replacement")
	case err == nil && journal.Status == "succeeded":
		return nil
	}
	journal.Request, journal.Status = request, "running"
	replaceErr := r.replace(ctx, path, &journal)
	journal.Status, journal.Error, journal.ObservedAt = "succeeded", "", time.Now().UTC()
	if replaceErr != nil {
		journal.Status, journal.Error = "needs_attention", replaceErr.Error()
	}
	if err := saveZFSReplaceJournal(path, journal); err != nil && replaceErr == nil {
		return errors.New("the disk was replaced but its journal could not be written")
	}
	return replaceErr
}

func (r *ZFSReplacer) replace(ctx context.Context, path string, journal *zfsReplaceJournal) error {
	request := journal.Request
	pool := "nex-" + request.RepositoryID
	status := zfsReplaceStatus(ctx, pool)
	if status == nil {
		return fmt.Errorf("Nex Core cannot read the pool of repository %s on this host", request.RepositoryID)
	}
	if zfsPoolHoldsDisk(status, request.Device.ByID) {
		// Adopt only when this request's outcome is already visible: OldDevice gone
		// (ZFS retired it) or still present beside a running RESILVER onto the replacement.
		// A healthy member that happens to share the pool must not rewrite the failed leaf.
		failed := zfsPoolLeaf(status, request.OldDevice)
		resilvering := status.Scan != nil && status.Scan.Function == "RESILVER" && status.Scan.State == "SCANNING"
		if failed == nil || resilvering {
			journal.Stage = "replaced"
			return nil
		}
		return errors.New("this disk is already part of the pool")
	}
	failed := zfsPoolLeaf(status, request.OldDevice)
	if failed == nil {
		return fmt.Errorf("%s is no longer part of this pool; check the pool before replacing a disk", request.OldDevice)
	}
	if failed.Parent == "replacing" || failed.Parent == "spare" {
		return fmt.Errorf("%s is already being replaced; wait for the resilver", request.OldDevice)
	}
	if failed.State == "ONLINE" {
		return fmt.Errorf("%s is online, so there is nothing to replace", request.OldDevice)
	}
	if min, ok := zfsReplaceMinSize(ctx, status, request.OldDevice); ok && request.Device.SizeBytes < min {
		return fmt.Errorf("the replacement disk is smaller than the failed disk (%d bytes against %d)", request.Device.SizeBytes, min)
	}
	// After the wipe the fingerprint cannot match any more, so past that point the
	// identity that survives it is checked instead. Erase confirmation still applies:
	// a disk that gained data since wipe-started was journalled must not be cleared
	// without a typed confirmation.
	if err := checkZFSReplaceDevice(ctx, request.Device, journal.Stage != "wipe-started"); err != nil {
		return err
	}
	journal.Stage = "wipe-started"
	if err := saveZFSReplaceJournal(path, *journal); err != nil {
		return errors.New("the ZFS disk replacement journal could not be written")
	}
	if _, err := zfsProvisionRun(ctx, "wipefs", "--all", request.Device.ByID); err != nil {
		return errors.New("the replacement disk could not be cleared")
	}
	out, err := zfsProvisionRun(ctx, "zpool", "replace", pool, request.OldDevice, request.Device.ByID)
	if err != nil {
		if reason := strings.TrimSpace(string(out)); reason != "" {
			return fmt.Errorf("%s could not be replaced with %s: %s", request.OldDevice, request.Device.ByID, reason)
		}
		return fmt.Errorf("%s could not be replaced with %s", request.OldDevice, request.Device.ByID)
	}
	journal.Stage = "replaced"
	return nil
}

// checkZFSReplaceDevice revalidates the chosen disk. requireFingerprint is false only
// after wipe-started; erase confirmation is always enforced when the live disk needs it.
func checkZFSReplaceDevice(ctx context.Context, device protocol.ZFSProvisionDevice, requireFingerprint bool) error {
	if err := checkSystemDiskDevice(ctx, device, requireFingerprint); err != nil {
		return err
	}
	if requireFingerprint {
		return nil
	}
	candidates, err := mdDiscoverDevice(ctx)
	if err != nil {
		return errors.New("the replacement disk cannot be safely observed")
	}
	for _, candidate := range candidates {
		if candidate.ByID != device.ByID {
			continue
		}
		if candidate.RequiresErase && device.EraseConfirmation != device.ByID {
			return errors.New("the replacement disk holds old data and was not confirmed for erasure")
		}
		return nil
	}
	return errors.New("this host no longer reports the selected replacement disk")
}

// zfsReplaceMinSize is the whole-disk size the failed leaf needs from discovery: the
// failed disk itself when still listed, otherwise an ONLINE data sibling. Unknown size
// skips the check rather than blocking a replace Core cannot measure.
func zfsReplaceMinSize(ctx context.Context, status *protocol.ZFSPoolStatus, oldDevice string) (uint64, bool) {
	candidates, err := mdDiscoverDevice(ctx)
	if err != nil {
		return 0, false
	}
	sizeOf := func(name string) uint64 {
		for _, c := range candidates {
			if name == c.ByID || strings.HasPrefix(name, c.ByID+"-part") {
				return c.SizeBytes
			}
		}
		return 0
	}
	if n := sizeOf(oldDevice); n > 0 {
		return n, true
	}
	for _, leaf := range status.Devices {
		if leaf.State != "ONLINE" || leaf.Parent == "replacing" || leaf.Parent == "spare" {
			continue
		}
		name := leaf.Path
		if name == "" {
			name = leaf.Name
		}
		if n := sizeOf(name); n > 0 {
			return n, true
		}
	}
	return 0, false
}

// zfsPoolLeaf finds one leaf of the observed pool, named exactly as the observation reports
// it. That is what the operator picked from the failed devices Core named.
func zfsPoolLeaf(status *protocol.ZFSPoolStatus, device string) *protocol.ZFSPoolDevice {
	for i, leaf := range status.Devices {
		if leaf.Path == device || leaf.Name == device {
			return &status.Devices[i]
		}
	}
	return nil
}

// zfsPoolHoldsDisk reports whether the pool already holds this whole disk, either as a leaf
// of its own or as the partition ZFS creates on a disk it is given. A pool resilvering onto
// the replacement reports it inside a replacing- vdev, which the observation flattens, so
// this is matched before anything else is decided about the failed leaf.
func zfsPoolHoldsDisk(status *protocol.ZFSPoolStatus, byID string) bool {
	base := filepath.Base(byID)
	for _, leaf := range status.Devices {
		for _, name := range []string{leaf.Path, leaf.Name} {
			if name == byID || name == base || strings.HasPrefix(name, byID+"-part") || strings.HasPrefix(name, base+"-part") {
				return true
			}
		}
	}
	return false
}

func readZFSReplaceJournal(path string) (zfsReplaceJournal, error) {
	var journal zfsReplaceJournal
	raw, err := os.ReadFile(path)
	if err != nil {
		return journal, err
	}
	if err := json.Unmarshal(raw, &journal); err != nil {
		return journal, errors.New("unreadable ZFS disk replacement journal")
	}
	return journal, nil
}
