// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// ZFS device discovery is an observation only. Provisioning must repeat these
// checks immediately before it wipes or creates anything.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type ZFSDeviceCandidate struct {
	Path          string `json:"path,omitempty"`
	ByID          string `json:"by_id,omitempty"`
	Model         string `json:"model,omitempty"`
	Serial        string `json:"serial,omitempty"`
	SizeBytes     uint64 `json:"size_bytes"`
	Rotational    bool   `json:"rotational"`
	Eligible      bool   `json:"eligible"`
	RequiresErase bool   `json:"requires_erase,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Fingerprint   string `json:"fingerprint"`
}

type zfsDiscoveryBlock struct {
	Path        string              `json:"path"`
	Type        string              `json:"type"`
	Model       string              `json:"model"`
	Serial      string              `json:"serial"`
	Size        uint64              `json:"size"`
	Rotational  bool                `json:"rota"`
	ReadOnly    bool                `json:"ro"`
	FSType      string              `json:"fstype"`
	Partition   string              `json:"pttype"`
	Mountpoints []string            `json:"mountpoints"`
	Children    []zfsDiscoveryBlock `json:"children"`
}

type zfsDiscoveryBlockList struct {
	BlockDevices []zfsDiscoveryBlock `json:"blockdevices"`
}

type zfsDeviceSignature struct {
	Offset   string `json:"offset"`
	Type     string `json:"type"`
	UUID     string `json:"uuid"`
	PartType string `json:"parttype"`
}

type zfsDeviceSignatureList struct {
	Signatures []zfsDeviceSignature `json:"signatures"`
}

var (
	zfsDiscoveryReadDir      = os.ReadDir
	zfsDiscoveryReadFile     = os.ReadFile
	zfsDiscoveryHolders      = os.ReadDir
	zfsDiscoveryEvalSymlinks = filepath.EvalSymlinks
)

// DiscoverZFSDevices lists whole physical disks and why each one may or may
// not be selected for a new ZFS repository. It never changes disk state.
func DiscoverZFSDevices(ctx context.Context) ([]ZFSDeviceCandidate, error) {
	// --tree is required: without a NAME column current util-linux emits a flat list, so a
	// disk would show no partitions and its pool, mount, swap and RAID use would go unseen.
	raw, err := nativeRun(ctx, "lsblk", "--json", "--tree", "--bytes", "--output", "PATH,TYPE,MODEL,SERIAL,SIZE,ROTA,RO,FSTYPE,PTTYPE,MOUNTPOINTS")
	if err != nil {
		return nil, err
	}
	var list zfsDiscoveryBlockList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("invalid lsblk output")
	}
	// A partition is never a top-level device in a tree. Seeing one means the nesting every
	// in-use check depends on is missing, so no disk may be offered.
	for _, block := range list.BlockDevices {
		if block.Type == "part" {
			return nil, errors.New("lsblk did not report partitions under their disks")
		}
	}
	zpool, err := nativeRun(ctx, "zpool", "status", "-P")
	// OpenZFS may return a non-zero status on a new host with no imported pools;
	// that is the expected blank-disk case, not a reason to hide all devices.
	// A failed command yields no stdout, so the reason is only ever in the error.
	if err != nil && !strings.Contains(err.Error(), "no pools available") {
		return nil, err
	}
	members := zfsPoolMembers(string(zpool))
	swaps, swapErr := zfsActiveSwapDevices()
	aliases := zfsByIDAliases()
	var out []ZFSDeviceCandidate
	for _, disk := range list.BlockDevices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if disk.Type != "disk" || disk.Path == "" {
			continue
		}
		candidate := ZFSDeviceCandidate{Path: disk.Path, ByID: aliases[resolvedDevicePath(disk.Path)], Model: strings.TrimSpace(disk.Model), Serial: strings.TrimSpace(disk.Serial), SizeBytes: disk.Size, Rotational: disk.Rotational}
		candidate.Eligible, candidate.RequiresErase, candidate.Reason = zfsDeviceEligibility(disk, members, swaps)
		if swapErr != nil {
			candidate.Eligible, candidate.RequiresErase, candidate.Reason = false, false, "cannot inspect active swap"
		} else if held, err := zfsBlockHasHolders(disk); err != nil {
			candidate.Eligible, candidate.RequiresErase, candidate.Reason = false, false, "cannot inspect device holders"
		} else if held {
			candidate.Eligible, candidate.RequiresErase, candidate.Reason = false, false, "used by another block device"
		}
		signatures, signatureErr := zfsDeviceSignatures(ctx, disk.Path)
		if signatureErr != nil {
			candidate.Eligible, candidate.RequiresErase, candidate.Reason = false, false, "cannot inspect device signatures"
		} else if candidate.Eligible && len(signatures) != 0 {
			candidate.RequiresErase, candidate.Reason = true, "contains existing data"
		}
		if candidate.ByID == "" {
			candidate.Eligible = false
			candidate.RequiresErase = false
			candidate.Reason = "no stable by-id path"
		} else if candidate.Eligible && (candidate.Serial == "" || candidate.SizeBytes == 0) {
			candidate.Eligible = false
			candidate.RequiresErase = false
			candidate.Reason = "device identity is unavailable"
		} else if candidate.Eligible && disk.ReadOnly {
			candidate.Eligible = false
			candidate.RequiresErase = false
			candidate.Reason = "read-only device"
		}
		candidate.Fingerprint = zfsDeviceFingerprint(disk, signatures, candidate)
		out = append(out, candidate)
	}
	slices.SortFunc(out, func(a, b ZFSDeviceCandidate) int { return strings.Compare(a.ByID, b.ByID) })
	return out, nil
}

// zfsDeviceFingerprint binds the exact observed topology/signature metadata to
// the selection. It is checked again before the first wipe; a journaled
// wipe-started boundary remains authoritative after any destructive step.
func zfsDeviceFingerprint(block zfsDiscoveryBlock, signatures []zfsDeviceSignature, candidate ZFSDeviceCandidate) string {
	b, _ := json.Marshal(struct {
		Block                zfsDiscoveryBlock
		Signatures           []zfsDeviceSignature
		ByID, Serial, Reason string
		Size                 uint64
		Eligible, Erase      bool
	}{block, signatures, candidate.ByID, candidate.Serial, candidate.Reason, candidate.SizeBytes, candidate.Eligible, candidate.RequiresErase})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func zfsDeviceSignatures(ctx context.Context, device string) ([]zfsDeviceSignature, error) {
	// Do not request a column list here. Debian's supported wipefs columns vary
	// by util-linux release; an unsupported optional column made a successful
	// blank-disk scan look like an inspection failure. The JSON fields we use
	// are present when signatures exist, and an empty signatures array is the
	// authoritative blank-disk result.
	raw, err := nativeRun(ctx, "wipefs", "--json", device)
	if err != nil {
		return nil, err
	}
	var result zfsDeviceSignatureList
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, errors.New("invalid wipefs output")
	}
	return result.Signatures, nil
}

func zfsDeviceEligibility(disk zfsDiscoveryBlock, poolMembers, swaps map[string]bool) (eligible, requiresErase bool, reason string) {
	if zfsBlockInPool(disk, poolMembers) {
		return false, false, "member of an imported zpool"
	}
	var walk func(zfsDiscoveryBlock) (bool, bool, bool, bool)
	walk = func(b zfsDiscoveryBlock) (mounted, inUse, activeSwap, oldData bool) {
		mounted = slices.ContainsFunc(b.Mountpoints, func(v string) bool { return v != "" })
		inUse = b.Type == "raid" || b.Type == "lvm" || b.FSType == "LVM2_member" || b.FSType == "linux_raid_member"
		activeSwap = b.Path != "" && swaps[resolvedDevicePath(b.Path)]
		oldData = b.FSType != "" || b.Partition != ""
		for _, child := range b.Children {
			cm, ci, cs, cd := walk(child)
			mounted, inUse, activeSwap, oldData = mounted || cm, inUse || ci, activeSwap || cs, oldData || cd
		}
		return
	}
	mounted, inUse, activeSwap, oldData := walk(disk)
	if mounted {
		return false, false, "mounted"
	}
	if activeSwap {
		return false, false, "active swap"
	}
	if inUse {
		return false, false, "member of md or LVM"
	}
	if oldData {
		return true, true, "contains existing data"
	}
	return true, false, ""
}

func zfsActiveSwapDevices() (map[string]bool, error) {
	raw, err := zfsDiscoveryReadFile("/proc/swaps")
	if err != nil {
		return nil, err
	}
	devices := map[string]bool{}
	for i, line := range strings.Split(string(raw), "\n") {
		if i == 0 || line == "" {
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 2 && strings.HasPrefix(fields[0], "/dev/") {
			devices[resolvedDevicePath(fields[0])] = true
		}
	}
	return devices, nil
}

func zfsBlockHasHolders(block zfsDiscoveryBlock) (bool, error) {
	if block.Path != "" {
		entries, err := zfsDiscoveryHolders(filepath.Join("/sys/class/block", filepath.Base(block.Path), "holders"))
		if err != nil {
			return false, err
		}
		if len(entries) != 0 {
			return true, nil
		}
	}
	for _, child := range block.Children {
		held, err := zfsBlockHasHolders(child)
		if err != nil || held {
			return held, err
		}
	}
	return false, nil
}

func zfsBlockInPool(block zfsDiscoveryBlock, members map[string]bool) bool {
	if block.Path != "" && members[resolvedDevicePath(block.Path)] {
		return true
	}
	return slices.ContainsFunc(block.Children, func(child zfsDiscoveryBlock) bool { return zfsBlockInPool(child, members) })
}

func zfsByIDAliases() map[string]string {
	entries, err := zfsDiscoveryReadDir(diskByIDPath)
	if err != nil {
		return nil
	}
	aliases := map[string]string{}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "-part") {
			continue
		}
		path := filepath.Join(diskByIDPath, entry.Name())
		resolved, err := zfsDiscoveryEvalSymlinks(path)
		if err != nil || resolved == "" {
			continue
		}
		resolved = resolvedDevicePath(resolved)
		if prior, ok := aliases[resolved]; !ok || path < prior {
			aliases[resolved] = path
		}
	}
	return aliases
}

func zfsPoolMembers(status string) map[string]bool {
	members := map[string]bool{}
	for _, field := range strings.Fields(status) {
		if strings.HasPrefix(field, "/dev/") {
			members[resolvedDevicePath(field)] = true
		}
	}
	return members
}

func resolvedDevicePath(path string) string {
	resolved, err := zfsDiscoveryEvalSymlinks(path)
	if err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}
