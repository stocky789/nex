// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Rebuilding a degraded system-disk mirror onto a replacement disk. Everything durable is
// the host's own: the array superblocks the kernel assembles from, /etc/mdadm/mdadm.conf
// written by the installer (the array's UUID does not change, so nothing rewrites it), and
// the bootloader on each disk. The journal below only keeps this host from repeating a
// destructive step after an interruption.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/rootfs"
)

type systemDiskJournal struct {
	Request    protocol.SystemDiskReplaceRequest `json:"request"`
	Stage      string                            `json:"stage"`
	Status     string                            `json:"status"`
	Error      string                            `json:"error,omitempty"`
	ObservedAt time.Time                         `json:"observed_at"`
}

// SystemDiskReplacer performs one host's system-disk replacements, one at a time.
type SystemDiskReplacer struct {
	stateDir string
}

func NewSystemDiskReplacer(stateDir string) *SystemDiskReplacer {
	return &SystemDiskReplacer{stateDir: stateDir}
}

// Commands run in a transient system-manager service for the same reason ZFS provisioning
// does: the agent's own mount namespace is private, and grub-install writes to the ESP.
var systemDiskRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return zfsProvisionRun(ctx, name, args...)
}

var (
	mdSysBlockRoot   = "/sys/class/block"
	mdFirmwareEFI    = "/sys/firmware/efi"
	mdDiscoverDevice = DiscoverZFSDevices
	mdReadArrays     = func() protocol.HardwareMDArrays { return ReadMDArrays("/") }
	mdBootEFIRoot    = "/boot/efi"
)

// espPartitionType is the GPT type GUID of an EFI System Partition, as lsblk reports it.
const espPartitionType = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"

// Replace rebuilds request.Array onto request.Device. A redelivered operation ID replays its
// recorded outcome; an interrupted one resumes, and an array that already has the
// replacement as a member is adopted rather than wiped again.
func (r *SystemDiskReplacer) Replace(ctx context.Context, request protocol.SystemDiskReplaceRequest) error {
	if err := request.Valid(); err != nil {
		return err
	}
	if err := os.MkdirAll(r.stateDir, 0700); err != nil {
		return errors.New("Nex Core could not open its system disk replacement journal")
	}
	unlock, err := withExclusiveDiskLock(r.stateDir, errors.New("the system disk replacement journal is locked"))
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(r.stateDir, request.OperationID+".json")
	journal, err := readSystemDiskJournal(path)
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return errors.New("the system disk replacement journal is unreadable")
	case err == nil && journal.Request.Array != request.Array:
		return errors.New("this operation was already used for a different system disk replacement")
	case err == nil && journal.Request.Device.ByID != request.Device.ByID:
		return errors.New("this operation was already used for a different replacement disk")
	case err == nil && journal.Status == "succeeded":
		return nil
	}
	journal.Request, journal.Status = request, "running"
	replaceErr := r.rebuild(ctx, path, &journal)
	journal.Status, journal.Error, journal.ObservedAt = "succeeded", "", time.Now().UTC()
	if replaceErr != nil {
		journal.Status, journal.Error = "needs_attention", replaceErr.Error()
	}
	if err := saveSystemDiskJournal(path, journal); err != nil && replaceErr == nil {
		return errors.New("the system disk was rebuilt but its journal could not be written")
	}
	return replaceErr
}

func (r *SystemDiskReplacer) rebuild(ctx context.Context, path string, journal *systemDiskJournal) error {
	request := journal.Request
	arrays := mdReadArrays()
	if arrays.Unavailable {
		return errors.New("Nex Core cannot read this host's RAID arrays")
	}
	var array protocol.MDArray
	for _, candidate := range arrays.Arrays {
		if candidate.Name == request.Array {
			array = candidate
		}
	}
	if array.Name == "" {
		return fmt.Errorf("this host has no array %s", request.Array)
	}
	if array.Level != "raid1" {
		return fmt.Errorf("%s is a %s array; only a RAID1 mirror is rebuilt this way", array.Device, array.Level)
	}
	// Stages past wipe drive adoption so a resume after mdadm --add (or a finished rebuild)
	// still reaches the idempotent bootloader step. A first attempt with an empty stage still
	// refuses an in-sync member on the chosen disk — that is the surviving disk, not a repair.
	journalAdopted := journal.Stage == "member-added" || journal.Stage == "bootable"
	adopted := journalAdopted
	replacement := filepath.Base(resolvedDevicePath(request.Device.ByID))
	survivingDisk, memberPartition, holdsMember := "", 0, false
	// A member whose disk the kernel no longer exposes is the failure being repaired: it is
	// neither the replacement nor a disk to copy from, so it is passed over rather than
	// turned into a refusal.
	for _, member := range array.Members {
		disk, partition, err := mdMemberDisk(member.Name)
		if err != nil {
			continue
		}
		if disk == replacement {
			switch {
			case mdMemberFaulty(member) && journalAdopted:
				// The disk was added and then failed, or the rebuild onto it was lost: it is
				// not repaired, so neither the bootloader step nor success may follow.
				return fmt.Errorf("the replacement disk's member of %s has failed; choose a different replacement disk", array.Device)
			case mdMemberFaulty(member):
				return fmt.Errorf("%s still holds a faulty member of %s; choose a different replacement disk", request.Device.ByID, array.Device)
			case mdMemberInSync(member) && !journalAdopted:
				// The disk the operator picked is carrying the array's data, not failing:
				// wiping it would destroy the mirror rather than repair it.
				return fmt.Errorf("%s already holds a working member of %s", request.Device.ByID, array.Device)
			case mdMemberAdoptable(member) || mdMemberInSync(member):
				adopted, holdsMember = true, true
			}
			continue
		}
		if mdMemberInSync(member) && survivingDisk == "" {
			survivingDisk, memberPartition = disk, partition
		}
	}
	if survivingDisk == "" {
		return fmt.Errorf("%s has no working member to copy from; restore this host instead", array.Device)
	}
	if journalAdopted && !holdsMember {
		return fmt.Errorf("the replacement disk is no longer a member of %s; choose a different replacement disk", array.Device)
	}
	if !adopted && array.Degraded == 0 {
		return fmt.Errorf("%s is not degraded, so there is no member to replace", array.Device)
	}
	// A second replacement while the kernel is still copying or rearranging would fight the
	// running recover/resync/reshape. Resuming an already-added member (adopted) still needs
	// the bootloader step and must not be refused for the recover that add started.
	if !adopted {
		switch array.SyncAction {
		case "recover", "resync", "reshape":
			return fmt.Errorf("%s is still %s; wait for it to finish before replacing a disk", array.Device, array.SyncAction)
		}
	}
	if size, err := mdDiskSize(survivingDisk); err != nil {
		return errors.New("Nex Core cannot read the size of the remaining system disk")
	} else if request.Device.SizeBytes < size {
		return fmt.Errorf("the replacement disk is smaller than the remaining system disk (%d bytes against %d)", request.Device.SizeBytes, size)
	}
	if !adopted {
		// The fingerprint binds the whole observed device; after an interruption past the
		// wipe it cannot match any more, so the identity that survives a wipe is checked
		// instead and nothing else about the selection is relaxed. This comes before
		// anything is read from the surviving disk, so an ineligible selection is refused
		// with its own reason.
		if err := checkSystemDiskDevice(ctx, request.Device, journal.Stage != "wipe-started"); err != nil {
			return err
		}
	}
	// The other mirrors of this system disk are decided before anything is erased: a repair
	// that cannot finish must refuse, not wipe the disk and leave /var unrepaired.
	siblings, err := mdSiblings(arrays.Arrays, array, replacement, adopted)
	if err != nil {
		return err
	}
	newMember := fmt.Sprintf("%s-part%d", request.Device.ByID, memberPartition)
	if !adopted {
		journal.Stage = "wipe-started"
		if err := saveSystemDiskJournal(path, *journal); err != nil {
			return errors.New("the system disk replacement journal could not be written")
		}
		if _, err := systemDiskRun(ctx, "wipefs", "--all", request.Device.ByID); err != nil {
			return errors.New("the replacement disk could not be cleared")
		}
		if _, err := systemDiskRun(ctx, "sgdisk", "--replicate="+request.Device.ByID, "/dev/"+survivingDisk); err != nil {
			return errors.New("the partition table could not be copied to the replacement disk")
		}
		if _, err := systemDiskRun(ctx, "sgdisk", "-G", request.Device.ByID); err != nil {
			return errors.New("the replacement disk's partition identifiers could not be made unique")
		}
		if _, err := systemDiskRun(ctx, "udevadm", "settle"); err != nil {
			return errors.New("the replacement disk's new partitions did not appear")
		}
		if _, err := systemDiskRun(ctx, "mdadm", "--add", array.Device, newMember); err != nil {
			return fmt.Errorf("%s could not be added to %s", newMember, array.Device)
		}
		journal.Stage = "member-added"
		if err := saveSystemDiskJournal(path, *journal); err != nil {
			return errors.New("the system disk replacement journal could not be written")
		}
	}
	for _, sibling := range siblings {
		member := fmt.Sprintf("%s-part%d", request.Device.ByID, sibling.partition)
		if _, err := systemDiskRun(ctx, "mdadm", "--add", sibling.device, member); err != nil {
			return fmt.Errorf("%s could not be added to %s", member, sibling.device)
		}
	}
	// Making the disk bootable is repeated on a retry: every step of it is idempotent, and a
	// disk the kernel is already rebuilding onto still has to be bootable.
	if err := mdInstallBootloader(ctx, request.Device.ByID, survivingDisk); err != nil {
		return err
	}
	journal.Stage = "bootable"
	return nil
}

// mdSibling is another array of the same system disk and the partition number its members
// have on every disk of the layout.
type mdSibling struct {
	device    string
	partition int
}

// mdSiblings lists the other degraded RAID1 arrays on the requested array's disks that still
// need the replacement's partition. The installer builds one array for / and one for /var from
// the same disks, and a second request for this disk would find nothing blank to wipe, so one
// operation repairs them all or refuses. Each sibling's partition number comes from its own
// working member, on whichever disk that is, because every disk carries the same layout.
// resuming says the replacement was already wiped, so a member of it there is the repair
// under way rather than data about to be erased.
func mdSiblings(arrays []protocol.MDArray, requested protocol.MDArray, replacement string, resuming bool) ([]mdSibling, error) {
	onSystemDisk := map[string]bool{}
	for _, member := range requested.Members {
		if disk, _, err := mdMemberDisk(member.Name); err == nil && disk != replacement {
			onSystemDisk[disk] = true
		}
	}
	var siblings []mdSibling
	for _, candidate := range arrays {
		if candidate.Name == requested.Name || candidate.Level != "raid1" || candidate.Degraded == 0 {
			continue
		}
		partition, related, held, heldFaulty := 0, false, false, false
		for _, member := range candidate.Members {
			disk, number, err := mdMemberDisk(member.Name)
			switch {
			case err != nil:
			case disk == replacement:
				// Sharing the replacement disk is relation enough: wiping it destroys that member.
				related, held, heldFaulty = true, true, heldFaulty || mdMemberFaulty(member)
			case onSystemDisk[disk]:
				related = true
				if mdMemberInSync(member) && partition == 0 {
					partition = number
				}
			}
		}
		switch {
		case !related:
		case held && !resuming:
			return nil, fmt.Errorf("the replacement disk already holds a member of %s; choose a different replacement disk", candidate.Device)
		case heldFaulty:
			return nil, fmt.Errorf("the replacement disk's member of %s has failed; choose a different replacement disk", candidate.Device)
		case held:
		case partition == 0:
			return nil, fmt.Errorf("%s has no working member to copy from; restore this host instead", candidate.Device)
		default:
			siblings = append(siblings, mdSibling{candidate.Device, partition})
		}
	}
	return siblings, nil
}

// mdMemberAdoptable reports a member the kernel is rebuilding onto (or holding as a spare),
// not a faulty leftover of the failed disk.
func mdMemberAdoptable(member protocol.MDMember) bool {
	for _, state := range strings.Split(member.State, ",") {
		switch strings.TrimSpace(state) {
		case "spare", "rebuilding", "replacement":
			return true
		}
	}
	return false
}

func mdMemberFaulty(member protocol.MDMember) bool {
	for _, state := range strings.Split(member.State, ",") {
		if strings.TrimSpace(state) == "faulty" {
			return true
		}
	}
	return false
}

// mdInstallBootloader makes the replacement disk bootable the same way the installer does.
// On BIOS firmware that is GRUB on the disk itself, and nothing else: a BIOS mirror has no EFI
// system partition to copy, so looking for one would refuse every BIOS replacement. On UEFI,
// Debian's grub-efi-amd64 writes only the one mounted ESP, so the installer copies the EFI
// directory onto every other member's ESP and repeats it from an APT hook; this runs that same
// helper first and does the copy itself when the helper is absent or copied nothing, so both
// mechanisms maintain the same files.
func mdInstallBootloader(ctx context.Context, disk, survivingDisk string) error {
	if _, err := os.Stat(mdFirmwareEFI); err != nil {
		// grub-install writes /boot/grub on the root file system, which is read-only.
		restoreRoot, err := rootfs.OpenWritable(ctx, systemDiskRun)
		if err != nil {
			return errors.New("GRUB could not be installed to the replacement disk: " + err.Error())
		}
		defer restoreRoot()
		if _, err := systemDiskRun(ctx, "grub-install", "--target=i386-pc", disk); err != nil {
			return errors.New("GRUB could not be installed to the replacement disk")
		}
		return nil
	}
	espPartition, survivingESP, err := mdESPPartition(ctx, "/dev/"+survivingDisk)
	if err != nil {
		return err
	}
	esp := fmt.Sprintf("%s-part%d", disk, espPartition)
	if _, err := systemDiskRun(ctx, "mkfs.vfat", "-F", "32", esp); err != nil {
		return errors.New("the replacement disk's EFI system partition could not be formatted")
	}
	if _, err := systemDiskRun(ctx, mdSyncESPHelper); err == nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(mdBootEFIRoot, "EFI")); err == nil {
		if _, err := systemDiskRun(ctx, "sh", "-c", mdCopyESP, "sh", esp, mdBootEFIRoot); err != nil {
			return errors.New("the replacement disk's EFI system partition could not be made bootable")
		}
		return nil
	}
	// /boot/efi is often the failed disk's ESP. Copy from the surviving member's ESP instead.
	if _, err := systemDiskRun(ctx, "sh", "-c", mdCopyESPFromDevice, "sh", esp, survivingESP); err != nil {
		return fmt.Errorf("the replacement disk's EFI system partition could not be made bootable: EFI boot files are missing from %s and could not be copied from %s", mdBootEFIRoot, survivingESP)
	}
	return nil
}

// mdSyncESPHelper is the copier the installer leaves on a mirrored host, which its APT hook
// re-runs after every GRUB update. A host that does not have it, or whose copier reports that it
// copied nothing, falls back to mdCopyESP.
var mdSyncESPHelper = "/usr/local/sbin/nex-sync-esp"

// mdCopyESP copies what GRUB maintains under the mounted EFI root ($2, normally /boot/efi)
// onto the new ESP, including the removable fallback path the installer seeds so either
// disk boots on its own.
const mdCopyESP = `set -eu
src="$2"
[ -d "$src/EFI" ] || exit 1
mount=$(mktemp -d)
trap 'umount "$mount" 2>/dev/null || true; rmdir "$mount" 2>/dev/null || true' EXIT
mount "$1" "$mount"
mkdir -p "$mount/EFI"
cp -a "$src/EFI/." "$mount/EFI/"
`

// mdCopyESPFromDevice mounts a surviving member's ESP and copies its EFI directory onto the
// replacement when /boot/efi is empty (the failed disk was the one fstab mounts there).
const mdCopyESPFromDevice = `set -eu
dest="$1"
srcdev="$2"
src=$(mktemp -d)
mount=$(mktemp -d)
trap 'umount "$mount" 2>/dev/null || true; umount "$src" 2>/dev/null || true; rmdir "$mount" "$src" 2>/dev/null || true' EXIT
mount "$srcdev" "$src"
[ -d "$src/EFI" ] || exit 1
mount "$dest" "$mount"
mkdir -p "$mount/EFI"
cp -a "$src/EFI/." "$mount/EFI/"
`

// checkSystemDiskDevice repeats the discovery listing's own eligibility rules against a fresh
// observation, so a device that became busy, changed or lost its typed erase confirmation
// since the operator picked it is refused rather than wiped.
func checkSystemDiskDevice(ctx context.Context, device protocol.ZFSProvisionDevice, requireFingerprint bool) error {
	candidates, err := mdDiscoverDevice(ctx)
	if err != nil {
		return errors.New("the replacement disk cannot be safely observed")
	}
	for _, candidate := range candidates {
		if candidate.ByID != device.ByID {
			continue
		}
		if !candidate.Eligible || candidate.Serial != device.Serial || candidate.SizeBytes != device.SizeBytes {
			return errors.New("the replacement disk changed since it was selected, or is in use")
		}
		// After the wipe the fingerprint cannot match any more, and the disk this host
		// wiped itself has no old data left to confirm. Everything else still holds: a
		// disk another pool or array has claimed since is refused, not written to again.
		if !requireFingerprint {
			return nil
		}
		if candidate.Fingerprint != device.Fingerprint {
			return errors.New("the replacement disk changed since it was selected, or is in use")
		}
		if candidate.RequiresErase && device.EraseConfirmation != device.ByID {
			return errors.New("the replacement disk holds old data and was not confirmed for erasure")
		}
		return nil
	}
	return errors.New("this host no longer reports the selected replacement disk")
}

// mdMemberDisk names the whole disk an array member's partition sits on, and that partition's
// number, from sysfs rather than by trimming digits off a device name.
func mdMemberDisk(member string) (string, int, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(mdSysBlockRoot, member))
	if err != nil {
		return "", 0, err
	}
	number, err := os.ReadFile(filepath.Join(resolved, "partition"))
	if err != nil {
		return "", 0, err
	}
	partition, err := strconv.Atoi(strings.TrimSpace(string(number)))
	if err != nil {
		return "", 0, err
	}
	return filepath.Base(filepath.Dir(resolved)), partition, nil
}

func mdDiskSize(disk string) (uint64, error) {
	raw, err := os.ReadFile(filepath.Join(mdSysBlockRoot, disk, "size"))
	if err != nil {
		return 0, err
	}
	sectors, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return sectors * 512, err
}

// mdESPPartition finds the EFI system partition of the surviving system disk, so the
// replacement's copy of the same table is formatted and made bootable.
func mdESPPartition(ctx context.Context, disk string) (int, string, error) {
	// NAME and --tree are both required: without them current util-linux emits a flat list
	// and the disk's partitions would not be seen at all.
	raw, err := systemDiskRun(ctx, "lsblk", "--json", "--tree", "--output", "NAME,PATH,PARTTYPE", disk)
	if err != nil {
		return 0, "", errors.New("Nex Core cannot read the remaining system disk's partitions")
	}
	var list struct {
		BlockDevices []struct {
			Path     string `json:"path"`
			PartType string `json:"parttype"`
			Children []struct {
				Path     string `json:"path"`
				PartType string `json:"parttype"`
			} `json:"children"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, "", errors.New("Nex Core cannot read the remaining system disk's partitions")
	}
	for _, block := range list.BlockDevices {
		for _, child := range block.Children {
			if !strings.EqualFold(child.PartType, espPartitionType) {
				continue
			}
			_, partition, err := mdMemberDisk(filepath.Base(child.Path))
			if err != nil {
				return 0, "", errors.New("Nex Core cannot read the remaining system disk's EFI system partition")
			}
			return partition, child.Path, nil
		}
	}
	return 0, "", errors.New("the remaining system disk has no EFI system partition to copy")
}

func readSystemDiskJournal(path string) (systemDiskJournal, error) {
	var journal systemDiskJournal
	raw, err := os.ReadFile(path)
	if err != nil {
		return journal, err
	}
	if err := json.Unmarshal(raw, &journal); err != nil {
		return journal, errors.New("unreadable system disk replacement journal")
	}
	return journal, nil
}

func saveSystemDiskJournal(path string, journal systemDiskJournal) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return atomicWrite(path, raw)
}
