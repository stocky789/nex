// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"nex.local/nex/internal/protocol"
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
		mdSyncESPHelper = "/usr/local/sbin/nex-sync-esp"
		mdDiscoverDevice, mdReadArrays = DiscoverZFSDevices, func() protocol.HardwareMDArrays { return ReadMDArrays("/") }
		systemDiskRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return zfsProvisionRun(ctx, name, args...)
		}
	})
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
