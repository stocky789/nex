// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/rootfs"
)

const mdTestByID = "/dev/disk/by-id/scsi-newdisk"

func mdTestRequest() protocol.SystemDiskReplaceRequest {
	return protocol.SystemDiskReplaceRequest{
		OperationID: "op-replace-1", HostID: "host-1", Array: "md0",
		Device: protocol.ZFSProvisionDevice{ByID: mdTestByID, Serial: "newdisk-serial", SizeBytes: 64 << 30, Fingerprint: strings.Repeat("a", 64)},
	}
}

// mdTestSysfs builds the /sys/class/block tree the replacement reads: a disk of 32 GiB with
// an ESP as partition 1 and the array member as partition 2, plus any extra partitions named.
func mdTestSysfs(t *testing.T, disks map[string][]string) string {
	t.Helper()
	root := t.TempDir()
	class := filepath.Join(root, "class/block")
	if err := os.MkdirAll(class, 0o755); err != nil {
		t.Fatal(err)
	}
	for disk, partitions := range disks {
		device := filepath.Join(root, "devices", disk)
		if err := os.MkdirAll(device, 0o755); err != nil {
			t.Fatal(err)
		}
		// 32 GiB in 512-byte sectors.
		if err := os.WriteFile(filepath.Join(device, "size"), []byte("67108864\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(device, filepath.Join(class, disk)); err != nil {
			t.Fatal(err)
		}
		for number, partition := range partitions {
			part := filepath.Join(device, partition)
			if err := os.MkdirAll(part, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(part, "partition"), []byte(string(rune('1'+number))+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(part, filepath.Join(class, partition)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return class
}

// mdTestLab stubs everything the replacement observes and runs, and records the commands. The
// stubbed host is BIOS unless a test points mdFirmwareEFI at a real directory. noESP makes
// lsblk report no EFI system partition, for the UEFI refusal path.
var noESP bool

func mdTestLab(t *testing.T, arrays protocol.HardwareMDArrays, candidate ZFSDeviceCandidate) *[]string {
	t.Helper()
	commands := &[]string{}
	noESP = false
	t.Cleanup(func() {
		mdSysBlockRoot, mdFirmwareEFI, mdBootEFIRoot, noESP = "/sys/class/block", "/sys/firmware/efi", "/boot/efi", false
		mdOSReleasePath = "/usr/lib/os-release"
		mdSyncESPHelper = "/usr/local/sbin/nex-sync-esp"
		mdDiscoverDevice, mdReadArrays = DiscoverZFSDevices, func() protocol.HardwareMDArrays { return ReadMDArrays("/") }
		systemDiskRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return zfsProvisionRun(ctx, name, args...)
		}
	})
	mdOSReleasePath = filepath.Join(t.TempDir(), "no-os-release") // a classic host unless a test writes one
	mdTestRootOptions(t, "rw,relatime")
	mdSysBlockRoot = mdTestSysfs(t, map[string][]string{"sda": {"sda1", "sda2"}, "sdb": {"sdb1", "sdb2"}, "scsi-newdisk": {"scsi-newdisk-part1", "scsi-newdisk-part2"}})
	mdFirmwareEFI = filepath.Join(t.TempDir(), "no-efi") // BIOS firmware
	bootEFI := filepath.Join(t.TempDir(), "boot-efi")
	if err := os.MkdirAll(filepath.Join(bootEFI, "EFI"), 0o755); err != nil {
		t.Fatal(err)
	}
	mdBootEFIRoot = bootEFI
	mdReadArrays = func() protocol.HardwareMDArrays { return arrays }
	mdDiscoverDevice = func(context.Context) ([]ZFSDeviceCandidate, error) { return []ZFSDeviceCandidate{candidate}, nil }
	systemDiskRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		*commands = append(*commands, strings.Join(append([]string{name}, args...), " "))
		if name == "findmnt" {
			return []byte(mdTestRoot + "\n"), nil
		}
		if name == "lsblk" {
			// util-linux only nests the partitions when NAME is among the columns; a flat
			// list would hide the ESP, so the stub answers only a request that asks for it.
			if !strings.Contains(strings.Join(args, " "), "NAME") {
				return []byte(`{"blockdevices":[{"path":"/dev/sda"}]}`), nil
			}
			if noESP {
				// UEFI host whose surviving disk has no ESP to copy.
				return []byte(`{"blockdevices":[{"path":"/dev/sda","children":[{"path":"/dev/sda1","parttype":"21686148-6449-6e6f-744e-656564454649"},{"path":"/dev/sda2","parttype":"0fc63daf-8483-4772-8e79-3d69d8477de4"}]}]}`), nil
			}
			return []byte(`{"blockdevices":[{"path":"/dev/sda","children":[{"path":"/dev/sda1","parttype":"` + espPartitionType + `"},{"path":"/dev/sda2","parttype":"0fc63daf-8483-4772-8e79-3d69d8477de4"}]}]}`), nil
		}
		return nil, nil
	}
	return commands
}

// mdTestRoot is what findmnt reports for / on the stubbed host; the system manager that runs
// the commands is where the root's state is read, not the agent's own sandboxed view.
var mdTestRoot = "rw,relatime"

func mdTestRootOptions(t *testing.T, options string) {
	t.Helper()
	mdTestRoot = options
	t.Cleanup(func() { mdTestRoot = "rw,relatime" })
}

func mdTestArray(degraded int, members ...protocol.MDMember) protocol.HardwareMDArrays {
	return protocol.HardwareMDArrays{Arrays: []protocol.MDArray{{
		Name: "md0", Device: "/dev/md0", Level: "raid1", RaidDisks: 2, Degraded: degraded, Members: members,
	}}}
}

func mdTestCandidate() ZFSDeviceCandidate {
	return ZFSDeviceCandidate{ByID: mdTestByID, Serial: "newdisk-serial", SizeBytes: 64 << 30, Eligible: true, Fingerprint: strings.Repeat("a", 64)}
}

var mdSurvivingMember = protocol.MDMember{Name: "sda2", Device: "/dev/sda2", Slot: "0", State: "in_sync"}

func TestReplaceSystemDiskRebuildsDegradedMirror(t *testing.T) {
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
	replacer := NewSystemDiskReplacer(t.TempDir())
	if err := replacer.Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	for _, want := range []string{
		"wipefs --all " + mdTestByID,
		"sgdisk --replicate=" + mdTestByID + " /dev/sda",
		"sgdisk -G " + mdTestByID,
		"mdadm --add /dev/md0 " + mdTestByID + "-part2",
		"grub-install --target=i386-pc " + mdTestByID,
	} {
		if !containsCommand(*commands, want) {
			t.Fatalf("missing %q in %v", want, *commands)
		}
	}
	// A redelivered command replays its outcome instead of wiping the disk again.
	before := len(*commands)
	if err := replacer.Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(*commands) != before {
		t.Fatalf("a replayed operation ran %v", (*commands)[before:])
	}
}

// The replacement copies the table and re-adds the mirror members; it does not lay down the image
// edition's root and verity slots. On an image host it must refuse before touching the journal or any disk,
// and only a host whose os-release says IMAGE_ID=nex-core is refused.
func TestReplaceSystemDiskRefusesOnTheImageEdition(t *testing.T) {
	for name, tc := range map[string]struct {
		osRelease string
		refused   bool
	}{
		"image edition":        {"ID=nex-core\nIMAGE_ID=\"nex-core\"\nBUILD_ID=20261001.1\n", true},
		"unquoted":             {"IMAGE_ID=nex-core\n", true},
		"classic debian":       {"ID=debian\nVERSION_ID=\"13\"\n", false},
		"other image":          {"ID=debian\nIMAGE_ID=other\n", false},
		"id only, no image id": {"ID=nex-core\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
			if err := os.WriteFile(mdOSReleasePath, []byte(tc.osRelease), 0o644); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(t.TempDir(), "system-disk-replace")
			err := NewSystemDiskReplacer(state).Replace(context.Background(), mdTestRequest())
			if !tc.refused {
				if err != nil {
					t.Fatalf("a host that is not the image edition was refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "not available on the image edition") {
				t.Fatalf("expected an image-edition refusal, got %v", err)
			}
			if len(*commands) != 0 {
				t.Fatalf("a refused replacement ran %v", *commands)
			}
			if _, statErr := os.Stat(state); !os.IsNotExist(statErr) {
				t.Fatalf("a refused replacement left state behind: %v", statErr)
			}
		})
	}
}

func TestReplaceSystemDiskRefusesHealthyArray(t *testing.T) {
	commands := mdTestLab(t, mdTestArray(0, mdSurvivingMember, protocol.MDMember{Name: "sdb2", Device: "/dev/sdb2", Slot: "1", State: "in_sync"}), mdTestCandidate())
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "not degraded") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if containsCommand(*commands, "wipefs --all "+mdTestByID) {
		t.Fatalf("a refused replacement wiped a disk: %v", *commands)
	}
}

// A second Replace while recover/resync/reshape is running would fight the kernel's copy.
// Adoption of a member already on the array still proceeds (bootloader) during recover.
func TestReplaceSystemDiskRefusesRunningRebuild(t *testing.T) {
	for _, action := range []string{"recover", "resync", "reshape"} {
		arrays := mdTestArray(1, mdSurvivingMember)
		arrays.Arrays[0].SyncAction = action
		commands := mdTestLab(t, arrays, mdTestCandidate())
		err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
		if err == nil || !strings.Contains(err.Error(), "wait for it to finish") {
			t.Fatalf("%s: expected a rebuild refusal, got %v", action, err)
		}
		if containsCommand(*commands, "wipefs --all "+mdTestByID) {
			t.Fatalf("%s: a refused replacement wiped a disk: %v", action, *commands)
		}
	}
	arrays := mdTestArray(1, mdSurvivingMember, protocol.MDMember{Name: "scsi-newdisk-part2", Device: "/dev/scsi-newdisk-part2", Slot: "1", State: "spare"})
	arrays.Arrays[0].SyncAction = "recover"
	commands := mdTestLab(t, arrays, mdTestCandidate())
	if err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("adopting during recover: %v", err)
	}
	if containsCommand(*commands, "wipefs --all "+mdTestByID) {
		t.Fatalf("adoption wiped the disk already being rebuilt onto: %v", *commands)
	}
}

func TestReplaceSystemDiskRefusesIneligibleDevice(t *testing.T) {
	candidate := mdTestCandidate()
	candidate.Eligible, candidate.Reason = false, "mounted"
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), candidate)
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "changed since it was selected, or is in use") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if containsCommand(*commands, "wipefs --all "+mdTestByID) {
		t.Fatalf("a refused replacement wiped a disk: %v", *commands)
	}
}

func TestReplaceSystemDiskRefusesUnconfirmedErase(t *testing.T) {
	candidate := mdTestCandidate()
	candidate.RequiresErase, candidate.Reason = true, "contains existing data"
	mdTestLab(t, mdTestArray(1, mdSurvivingMember), candidate)
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "not confirmed for erasure") {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

func TestReplaceSystemDiskRefusesSmallerDisk(t *testing.T) {
	candidate := mdTestCandidate()
	candidate.SizeBytes = 1 << 30
	mdTestLab(t, mdTestArray(1, mdSurvivingMember), candidate)
	request := mdTestRequest()
	request.Device.SizeBytes = 1 << 30
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "smaller than the remaining system disk") {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

func TestReplaceSystemDiskRefusesUnknownArray(t *testing.T) {
	mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
	request := mdTestRequest()
	request.Array = "md9"
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "no array md9") {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

// An agent interrupted between the wipe and mdadm --add retries the same operation. The
// wiped disk can no longer match its fingerprint, so the journal's wipe-started stage is
// what allows the retry to finish instead of refusing a disk it wiped itself.
func TestReplaceSystemDiskResumesAfterInterruptedWipe(t *testing.T) {
	state := t.TempDir()
	candidate := mdTestCandidate()
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), candidate)
	failing := *commands
	systemDiskRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		failing = append(failing, strings.Join(append([]string{name}, args...), " "))
		*commands = failing
		if name == "sgdisk" {
			return nil, errors.New("interrupted")
		}
		if name == "lsblk" {
			return []byte(`{"blockdevices":[{"path":"/dev/sda","children":[{"path":"/dev/sda1","parttype":"` + espPartitionType + `"}]}]}`), nil
		}
		return nil, nil
	}
	replacer := NewSystemDiskReplacer(state)
	if err := replacer.Replace(context.Background(), mdTestRequest()); err == nil {
		t.Fatal("expected the interrupted step to fail")
	}
	journal, err := readSystemDiskJournal(filepath.Join(state, "op-replace-1.json"))
	if err != nil || journal.Stage != "wipe-started" || journal.Status != "needs_attention" {
		t.Fatalf("expected a wipe-started journal needing attention, got %+v (%v)", journal, err)
	}
	// The wiped disk now reports a different fingerprint, as a real one would.
	wiped := mdTestCandidate()
	wiped.Fingerprint = strings.Repeat("b", 64)
	mdDiscoverDevice = func(context.Context) ([]ZFSDeviceCandidate, error) { return []ZFSDeviceCandidate{wiped}, nil }
	mdTestRun(t, commands)
	if err := replacer.Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !containsCommand(*commands, "mdadm --add /dev/md0 "+mdTestByID+"-part2") {
		t.Fatalf("the retry did not add the member: %v", *commands)
	}
	journal, err = readSystemDiskJournal(filepath.Join(state, "op-replace-1.json"))
	if err != nil || journal.Status != "succeeded" || journal.Error != "" {
		t.Fatalf("expected a succeeded journal with no prior error, got %+v (%v)", journal, err)
	}
}

// Picking the disk that is still carrying the array is refused before anything is touched:
// it is the disk the rebuild copies from, not the one that failed.
func TestReplaceSystemDiskRefusesTheSurvivingDisk(t *testing.T) {
	member := protocol.MDMember{Name: "scsi-newdisk-part2", Device: "/dev/scsi-newdisk-part2", Slot: "0", State: "in_sync"}
	commands := mdTestLab(t, mdTestArray(1, member), mdTestCandidate())
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "already holds a working member") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if containsCommand(*commands, "wipefs --all "+mdTestByID) {
		t.Fatalf("a refused replacement wiped the surviving disk: %v", *commands)
	}
}

// An array that already has the replacement as a member is adopted: the disk is never wiped
// again, and only the bootloader work is repeated.
func TestReplaceSystemDiskAdoptsExistingMember(t *testing.T) {
	member := protocol.MDMember{Name: "scsi-newdisk-part2", Device: "/dev/scsi-newdisk-part2", Slot: "1", State: "spare"}
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember, member), mdTestCandidate())
	if err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if containsCommand(*commands, "wipefs --all "+mdTestByID) {
		t.Fatalf("an adopted replacement wiped the disk again: %v", *commands)
	}
	if !containsCommand(*commands, "grub-install --target=i386-pc "+mdTestByID) {
		t.Fatalf("an adopted replacement left the disk unbootable: %v", *commands)
	}
}

// mdTestRun restores the recording stub after a test replaced it to simulate a failure.
func mdTestRun(t *testing.T, commands *[]string) {
	t.Helper()
	systemDiskRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		*commands = append(*commands, strings.Join(append([]string{name}, args...), " "))
		if name == "findmnt" {
			return []byte(mdTestRoot + "\n"), nil
		}
		if name == "lsblk" {
			return []byte(`{"blockdevices":[{"path":"/dev/sda","children":[{"path":"/dev/sda1","parttype":"` + espPartitionType + `"}]}]}`), nil
		}
		return nil, nil
	}
}

func containsCommand(commands []string, want string) bool {
	for _, command := range commands {
		if command == want {
			return true
		}
	}
	return false
}

// On UEFI firmware the replacement's ESP is populated the way the installer does it, rather
// than with a grub-multi-install Debian does not ship: the host's own copier if it is there,
// and otherwise the same copy of what GRUB maintains in /boot/efi.
func TestReplaceSystemDiskPopulatesTheNewESPOnUEFI(t *testing.T) {
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
	efi := t.TempDir()
	mdFirmwareEFI = efi // a machine booted by UEFI firmware exposes this directory
	if err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if containsCommand(*commands, "grub-install --target=i386-pc "+mdTestByID) {
		t.Fatalf("a UEFI host was given a BIOS bootloader: %v", *commands)
	}
	for _, want := range []string{"mkfs.vfat -F 32 " + mdTestByID + "-part1", mdSyncESPHelper} {
		if !containsCommand(*commands, want) {
			t.Fatalf("missing %q in %v", want, *commands)
		}
	}
	for _, command := range *commands {
		if strings.Contains(command, "grub-multi-install") || strings.Contains(command, "debconf") {
			t.Fatalf("the replacement used a mechanism Debian does not ship: %v", command)
		}
	}
}

// A disk another array or pool claimed while the agent was down is refused on the retry too.
// The wipe-started stage relaxes only the fingerprint and the erase confirmation, which the
// wipe itself invalidated; eligibility, serial and size are still checked.
func TestReplaceSystemDiskRetryRefusesAClaimedDisk(t *testing.T) {
	state := t.TempDir()
	claimed := mdTestCandidate()
	claimed.Eligible, claimed.Reason = false, "member of an imported zpool"
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), claimed)
	journal := systemDiskJournal{Request: mdTestRequest(), Stage: "wipe-started", Status: "needs_attention"}
	if err := saveSystemDiskJournal(filepath.Join(state, "op-replace-1.json"), journal); err != nil {
		t.Fatal(err)
	}
	err := NewSystemDiskReplacer(state).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "changed since it was selected, or is in use") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	if containsCommand(*commands, "wipefs --all "+mdTestByID) || containsCommand(*commands, "mdadm --add /dev/md0 "+mdTestByID+"-part2") {
		t.Fatalf("a claimed disk was written to: %v", *commands)
	}
}

// A UEFI host whose surviving disk has no ESP must refuse after the member is already added,
// and leave the journal at member-added so a later retry can finish the bootloader work.
func TestReplaceSystemDiskRefusesUEFIWithoutAnESP(t *testing.T) {
	state := t.TempDir()
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
	mdFirmwareEFI = t.TempDir()
	noESP = true
	err := NewSystemDiskReplacer(state).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "no EFI system partition") {
		t.Fatalf("expected a UEFI refusal for a missing ESP, got %v", err)
	}
	if !containsCommand(*commands, "mdadm --add /dev/md0 "+mdTestByID+"-part2") {
		t.Fatalf("the member should have been added before the bootloader refusal: %v", *commands)
	}
	if containsCommand(*commands, "mkfs.vfat -F 32 "+mdTestByID+"-part1") {
		t.Fatalf("a missing ESP must not be formatted: %v", *commands)
	}
	journal, err := readSystemDiskJournal(filepath.Join(state, "op-replace-1.json"))
	if err != nil || journal.Stage != "member-added" || journal.Status != "needs_attention" {
		t.Fatalf("expected a member-added journal needing attention, got %+v (%v)", journal, err)
	}
}

// The installer's own copier is preferred, but a copier that reports it copied nothing must not
// count as a populated ESP: the copy is then done here, and a failure of that fails the operation.
func TestReplaceSystemDiskFallsBackWhenTheESPCopierFails(t *testing.T) {
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
	mdFirmwareEFI = t.TempDir()
	mdSyncESPHelper = "/usr/local/sbin/nex-sync-esp"
	run := systemDiskRun
	copyFails := false
	systemDiskRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == mdSyncESPHelper || (copyFails && name == "sh") {
			*commands = append(*commands, name)
			return nil, errors.New("copied nothing")
		}
		return run(ctx, name, args...)
	}
	if err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !containsCommand(*commands, mdSyncESPHelper) {
		t.Fatalf("the host's own copier was not tried: %v", *commands)
	}
	copied := false
	for _, command := range *commands {
		if strings.HasPrefix(command, "sh -c ") && strings.Contains(command, "cp -a") {
			copied = true
		}
	}
	if !copied {
		t.Fatalf("the copier's failure was taken as success: %v", *commands)
	}
	copyFails = true
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "could not be made bootable") {
		t.Fatalf("expected the operation to fail when the copy fails, got %v", err)
	}
}

// The wipe-started journal must reach stable storage: a hand-rolled WriteFile+rename without
// fsync can leave a truncated journal after power loss and block every later resume.
func TestSaveSystemDiskJournalUsesAtomicWrite(t *testing.T) {
	source, err := os.ReadFile("md_replace.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	start := strings.Index(body, "func saveSystemDiskJournal")
	if start < 0 {
		t.Fatal("saveSystemDiskJournal not found")
	}
	end := strings.Index(body[start:], "\nfunc ")
	if end < 0 {
		end = len(body) - start
	}
	fn := body[start : start+end]
	if strings.Contains(fn, "WriteFile") || strings.Contains(fn, ".tmp") || !strings.Contains(fn, "atomicWrite(path, raw)") {
		t.Fatalf("saveSystemDiskJournal must call atomicWrite, got:\n%s", fn)
	}
	path := filepath.Join(t.TempDir(), "op-1.json")
	want := systemDiskJournal{Stage: "wipe-started", Status: "needs_attention", Error: "interrupted"}
	if err := saveSystemDiskJournal(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := readSystemDiskJournal(path)
	if err != nil || got.Stage != want.Stage || got.Status != want.Status || got.Error != want.Error {
		t.Fatalf("round-trip: got %+v (%v)", got, err)
	}
}

// A resume after the bootloader failed must still finish once the member is in_sync: the
// member-added journal stage drives adoption so the "already holds a working member" guard
// does not block forever.
func TestReplaceSystemDiskResumesBootloaderAfterMemberInSync(t *testing.T) {
	state := t.TempDir()
	member := protocol.MDMember{Name: "scsi-newdisk-part2", Device: "/dev/scsi-newdisk-part2", Slot: "1", State: "in_sync"}
	commands := mdTestLab(t, mdTestArray(0, mdSurvivingMember, member), mdTestCandidate())
	journal := systemDiskJournal{Request: mdTestRequest(), Stage: "member-added", Status: "needs_attention", Error: "GRUB could not be installed"}
	if err := saveSystemDiskJournal(filepath.Join(state, "op-replace-1.json"), journal); err != nil {
		t.Fatal(err)
	}
	if err := NewSystemDiskReplacer(state).Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("resume after in_sync: %v", err)
	}
	if containsCommand(*commands, "wipefs --all "+mdTestByID) || containsCommand(*commands, "mdadm --add /dev/md0 "+mdTestByID+"-part2") {
		t.Fatalf("resume wiped or re-added the member: %v", *commands)
	}
	if !containsCommand(*commands, "grub-install --target=i386-pc "+mdTestByID) {
		t.Fatalf("resume skipped the bootloader: %v", *commands)
	}
	got, err := readSystemDiskJournal(filepath.Join(state, "op-replace-1.json"))
	if err != nil || got.Stage != "bootable" || got.Status != "succeeded" || got.Error != "" {
		t.Fatalf("expected a bootable succeeded journal, got %+v (%v)", got, err)
	}
}

// Picking the failed disk while it still shows as a faulty member must not be treated as an
// already-added replacement.
func TestReplaceSystemDiskRefusesFaultyMemberOnChosenDisk(t *testing.T) {
	faulty := protocol.MDMember{Name: "scsi-newdisk-part2", Device: "/dev/scsi-newdisk-part2", Slot: "1", State: "faulty"}
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember, faulty), mdTestCandidate())
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "still holds a faulty member") {
		t.Fatalf("expected a faulty-member refusal, got %v", err)
	}
	if containsCommand(*commands, "wipefs --all "+mdTestByID) || containsCommand(*commands, "grub-install") {
		t.Fatalf("a faulty member was treated as adopted: %v", *commands)
	}
}

// When /boot/efi has no EFI directory (the failed disk was the one fstab mounts there), the
// replacement copies from the surviving member's ESP and names that source if the copy fails.
func TestReplaceSystemDiskCopiesESPFromSurvivingMemberWhenBootEFIMissing(t *testing.T) {
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
	mdFirmwareEFI = t.TempDir()
	mdBootEFIRoot = filepath.Join(t.TempDir(), "empty-boot-efi")
	if err := os.MkdirAll(mdBootEFIRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mdSyncESPHelper = "/usr/local/sbin/nex-sync-esp"
	run := systemDiskRun
	systemDiskRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == mdSyncESPHelper {
			*commands = append(*commands, name)
			return nil, errors.New("copied nothing")
		}
		return run(ctx, name, args...)
	}
	if err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	copiedFromSurviving := false
	for _, command := range *commands {
		if strings.HasPrefix(command, "sh -c ") && strings.Contains(command, "/dev/sda1") {
			copiedFromSurviving = true
		}
	}
	if !copiedFromSurviving {
		t.Fatalf("expected a copy from the surviving ESP, got %v", *commands)
	}
	// A failure of that copy must name both missing sources.
	systemDiskRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == mdSyncESPHelper || name == "sh" {
			*commands = append(*commands, name)
			return nil, errors.New("copied nothing")
		}
		return run(ctx, name, args...)
	}
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), mdBootEFIRoot) || !strings.Contains(err.Error(), "/dev/sda1") {
		t.Fatalf("expected an error naming %s and /dev/sda1, got %v", mdBootEFIRoot, err)
	}
}

// System-disk replace and ZFS replace share one exclusive flock on the host state directory,
// so concurrent lanes cannot wipe the same disk.
func TestDiskOpsShareExclusiveLockAcrossReplacers(t *testing.T) {
	host := t.TempDir()
	mdDir := filepath.Join(host, "system-disk-replace")
	zfsDir := filepath.Join(host, "zfs-replace")
	if err := os.MkdirAll(mdDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(zfsDir, 0700); err != nil {
		t.Fatal(err)
	}
	unlock, err := withExclusiveDiskLock(mdDir, errors.New("locked"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(host, ".disk-ops.lock"), os.O_RDWR, 0600)
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		unlock()
		t.Fatal("ZFS replace's sibling took the host disk-ops lock while system-disk replace held it")
	}
	_ = lock.Close()
	unlock()
	unlock2, err := withExclusiveDiskLock(zfsDir, errors.New("locked"))
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
}

// The installer builds one array for / and one for /var from the same disks. Replacing a
// disk repairs both in one operation: the replacement is no longer blank for a second one,
// so a second request would have nothing left to wipe. A healthy array is not touched.
func TestReplaceSystemDiskRepairsEveryDegradedArrayOnTheDisk(t *testing.T) {
	root := protocol.MDArray{Name: "md0", Device: "/dev/md0", Level: "raid1", RaidDisks: 2, Degraded: 1,
		Members: []protocol.MDMember{mdSurvivingMember}}
	data := protocol.MDArray{Name: "md1", Device: "/dev/md1", Level: "raid1", RaidDisks: 2, Degraded: 1,
		Members: []protocol.MDMember{{Name: "sda3", Device: "/dev/sda3", Slot: "0", State: "in_sync"}}}
	other := protocol.MDArray{Name: "md2", Device: "/dev/md2", Level: "raid1", RaidDisks: 2, Degraded: 0,
		Members: []protocol.MDMember{{Name: "sda4", Device: "/dev/sda4", State: "in_sync"}, {Name: "sdc4", Device: "/dev/sdc4", State: "in_sync"}}}
	commands := mdTestLab(t, protocol.HardwareMDArrays{Arrays: []protocol.MDArray{root, data, other}}, mdTestCandidate())
	mdSysBlockRoot = mdTestSysfs(t, map[string][]string{
		"sda": {"sda1", "sda2", "sda3", "sda4"}, "sdc": {"sdc1", "sdc2", "sdc3", "sdc4"},
		"scsi-newdisk": {"scsi-newdisk-part1", "scsi-newdisk-part2", "scsi-newdisk-part3", "scsi-newdisk-part4"}})
	if err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	for _, want := range []string{"mdadm --add /dev/md0 " + mdTestByID + "-part2", "mdadm --add /dev/md1 " + mdTestByID + "-part3"} {
		if !containsCommand(*commands, want) {
			t.Fatalf("missing %q in %v", want, *commands)
		}
	}
	for _, command := range *commands {
		if strings.HasPrefix(command, "mdadm --add") && strings.Contains(command, "md2") {
			t.Fatalf("a healthy array was changed: %v", *commands)
		}
	}
	// GRUB must be on the disk after both arrays have their member, not before.
	last, grub := -1, -1
	for i, command := range *commands {
		if strings.HasPrefix(command, "mdadm --add") {
			last = i
		}
		if strings.HasPrefix(command, "grub-install") {
			grub = i
		}
	}
	if grub < last {
		t.Fatalf("GRUB installed before the arrays were repaired: %v", *commands)
	}
}

// On a read-only root, grub-install (which writes /boot/grub) runs only inside a window
// that is closed again afterwards, even when GRUB fails.
func TestReplaceSystemDiskInstallsGRUBInsideARootWindow(t *testing.T) {
	for _, grubFails := range []bool{false, true} {
		commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
		mdTestRootOptions(t, "ro,relatime")
		run := systemDiskRun
		systemDiskRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			out, _ := run(ctx, name, args...)
			if grubFails && name == "grub-install" {
				return out, errors.New("exit status 1")
			}
			return out, nil
		}
		err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
		if (err != nil) != grubFails {
			t.Fatalf("grubFails=%v: %v", grubFails, err)
		}
		var order []string
		for _, command := range *commands {
			switch {
			case command == "mount -o remount,rw /":
				order = append(order, "rw")
			case strings.HasPrefix(command, "grub-install"):
				order = append(order, "grub")
			case command == "mount -o remount,ro /":
				order = append(order, "ro")
			}
		}
		if got := strings.Join(order, ","); got != "rw,grub,ro" {
			t.Fatalf("grubFails=%v: order %q in %v", grubFails, got, *commands)
		}
	}
}

// mdTestTwoArrays is a host whose / and /var mirrors lost different disks' members, each
// array's own member list given by the caller. md0 is the one the operator names.
func mdTestTwoArrays(t *testing.T, md0, md1 []protocol.MDMember) *[]string {
	t.Helper()
	arrays := protocol.HardwareMDArrays{Arrays: []protocol.MDArray{
		{Name: "md0", Device: "/dev/md0", Level: "raid1", RaidDisks: 2, Degraded: 1, Members: md0},
		{Name: "md1", Device: "/dev/md1", Level: "raid1", RaidDisks: 2, Degraded: 1, Members: md1},
	}}
	commands := mdTestLab(t, arrays, mdTestCandidate())
	mdSysBlockRoot = mdTestSysfs(t, map[string][]string{
		"sda": {"sda1", "sda2", "sda3"}, "sdb": {"sdb1", "sdb2", "sdb3"},
		"scsi-newdisk": {"scsi-newdisk-part1", "scsi-newdisk-part2", "scsi-newdisk-part3"}})
	return commands
}

func mdTestMember(name, state string) protocol.MDMember {
	return protocol.MDMember{Name: name, Device: "/dev/" + name, State: state}
}

func mdTestWiped(commands []string) bool {
	return containsCommand(commands, "wipefs --all "+mdTestByID)
}

// The /var array's only working member may sit on a different disk than the named array's:
// its partition number still comes from that member, and /var is repaired, not skipped.
func TestReplaceSystemDiskRepairsASiblingWhoseSurvivorIsOnAnotherDisk(t *testing.T) {
	commands := mdTestTwoArrays(t,
		[]protocol.MDMember{mdTestMember("sda2", "in_sync"), mdTestMember("sdb2", "faulty")},
		[]protocol.MDMember{mdTestMember("sda3", "faulty"), mdTestMember("sdb3", "in_sync")})
	if err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	for _, want := range []string{"mdadm --add /dev/md0 " + mdTestByID + "-part2", "mdadm --add /dev/md1 " + mdTestByID + "-part3"} {
		if !containsCommand(*commands, want) {
			t.Fatalf("missing %q in %v", want, *commands)
		}
	}
}

// A sibling that cannot be repaired stops the operation before the disk is erased; success is
// never reported with /var left degraded.
func TestReplaceSystemDiskRefusesBeforeWipingWhenASiblingHasNoWorkingMember(t *testing.T) {
	commands := mdTestTwoArrays(t,
		[]protocol.MDMember{mdTestMember("sda2", "in_sync")},
		[]protocol.MDMember{mdTestMember("sda3", "faulty")})
	state := t.TempDir()
	err := NewSystemDiskReplacer(state).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "/dev/md1 has no working member to copy from") {
		t.Fatalf("expected a refusal naming /dev/md1, got %v", err)
	}
	if mdTestWiped(*commands) || containsCommand(*commands, "mdadm --add /dev/md0 "+mdTestByID+"-part2") {
		t.Fatalf("the disk was changed although /var could not be repaired: %v", *commands)
	}
}

// A replacement that already carries a member of the /var array (data about to be wiped) is
// refused up front, like one that carries a member of the named array.
func TestReplaceSystemDiskRefusesBeforeWipingADiskHoldingASiblingMember(t *testing.T) {
	commands := mdTestTwoArrays(t,
		[]protocol.MDMember{mdTestMember("sda2", "in_sync")},
		[]protocol.MDMember{mdTestMember("sda3", "in_sync"), mdTestMember("scsi-newdisk-part3", "spare")})
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "already holds a member of /dev/md1") {
		t.Fatalf("expected a refusal naming /dev/md1, got %v", err)
	}
	if mdTestWiped(*commands) {
		t.Fatalf("a disk holding /var data was wiped: %v", *commands)
	}
}

// An array whose only listed member is on the replacement disk shares no other disk with the
// named array, yet wiping the disk would still destroy that member: refuse, never skip.
func TestReplaceSystemDiskRefusesBeforeWipingADiskHoldingTheOnlyMemberOfAnotherArray(t *testing.T) {
	commands := mdTestTwoArrays(t,
		[]protocol.MDMember{mdTestMember("sda2", "in_sync")},
		[]protocol.MDMember{mdTestMember("scsi-newdisk-part3", "in_sync")})
	err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest())
	if err == nil || !strings.Contains(err.Error(), "already holds a member of /dev/md1") {
		t.Fatalf("expected a refusal naming /dev/md1, got %v", err)
	}
	if mdTestWiped(*commands) {
		t.Fatalf("a disk holding another array's only member was wiped: %v", *commands)
	}
}

// After the disk was wiped and added, a member that has since failed (or vanished) is not a
// repaired mirror: no bootloader step, no success, and the journal keeps asking for attention.
func TestReplaceSystemDiskResumeNeverReportsSuccessForABrokenReplacementMember(t *testing.T) {
	good := mdTestMember("scsi-newdisk-part2", "in_sync")
	cases := map[string]struct {
		md0, md1 []protocol.MDMember
		want     string
	}{
		"faulty named member": {
			md0:  []protocol.MDMember{mdTestMember("sda2", "in_sync"), mdTestMember("scsi-newdisk-part2", "faulty")},
			md1:  []protocol.MDMember{mdTestMember("sda3", "in_sync")},
			want: "the replacement disk's member of /dev/md0 has failed",
		},
		"missing named member": {
			md0:  []protocol.MDMember{mdTestMember("sda2", "in_sync")},
			md1:  []protocol.MDMember{mdTestMember("sda3", "in_sync")},
			want: "the replacement disk is no longer a member of /dev/md0",
		},
		"faulty sibling member": {
			md0:  []protocol.MDMember{mdTestMember("sda2", "in_sync"), good},
			md1:  []protocol.MDMember{mdTestMember("sda3", "in_sync"), mdTestMember("scsi-newdisk-part3", "faulty")},
			want: "the replacement disk's member of /dev/md1 has failed",
		},
	}
	for name, c := range cases {
		state := t.TempDir()
		commands := mdTestTwoArrays(t, c.md0, c.md1)
		journal := systemDiskJournal{Request: mdTestRequest(), Stage: "member-added", Status: "needs_attention"}
		path := filepath.Join(state, "op-replace-1.json")
		if err := saveSystemDiskJournal(path, journal); err != nil {
			t.Fatal(err)
		}
		err := NewSystemDiskReplacer(state).Replace(context.Background(), mdTestRequest())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: expected %q, got %v", name, c.want, err)
		}
		for _, command := range *commands {
			if strings.HasPrefix(command, "grub-install") || strings.HasPrefix(command, "mkfs.vfat") {
				t.Fatalf("%s: made a broken disk bootable: %v", name, *commands)
			}
		}
		got, readErr := readSystemDiskJournal(path)
		if readErr != nil || got.Status != "needs_attention" || got.Stage == "bootable" {
			t.Fatalf("%s: journal %+v (%v)", name, got, readErr)
		}
	}
}

// A classic host with a writable root is never remounted, whatever the agent's own sandboxed
// view of / looks like: the state is read through the system manager that runs the commands.
func TestReplaceSystemDiskLeavesAWritableRootAlone(t *testing.T) {
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
	if err := NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest()); err != nil {
		t.Fatalf("replace: %v", err)
	}
	for _, command := range *commands {
		if strings.HasPrefix(command, "mount ") {
			t.Fatalf("remounted a writable root: %v", *commands)
		}
	}
}

// The BIOS bootloader write waits for any other root window (a software update's package
// install) instead of reading that window's writable root as the normal state, and only
// writes GRUB after the other window has closed.
func TestReplaceSystemDiskWaitsForAnotherRootWindowBeforeGRUB(t *testing.T) {
	commands := mdTestLab(t, mdTestArray(1, mdSurvivingMember), mdTestCandidate())
	mdTestRootOptions(t, "ro,relatime")
	closeOther, err := rootfs.OpenWritable(context.Background(), func(context.Context, string, ...string) ([]byte, error) {
		return []byte("ro,relatime"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Bool
	var early atomic.Bool
	run := systemDiskRun
	systemDiskRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if (name == "grub-install" || name == "mount") && !closed.Load() {
			early.Store(true)
		}
		return run(ctx, name, args...)
	}
	done := make(chan error, 1)
	go func() { done <- NewSystemDiskReplacer(t.TempDir()).Replace(context.Background(), mdTestRequest()) }()
	select {
	case err := <-done:
		t.Fatalf("GRUB was written inside another writer's window (%v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	closed.Store(true)
	closeOther()
	if err := <-done; err != nil {
		t.Fatalf("replace: %v", err)
	}
	if early.Load() {
		t.Fatal("the root was remounted or GRUB written while another window held the root")
	}
	if !containsCommand(*commands, "mount -o remount,rw /") || !containsCommand(*commands, "mount -o remount,ro /") {
		t.Fatalf("no window of its own: %v", *commands)
	}
}
