# System disk mirror and disk replacement verification

Date: 2026-09-22. Scope: the Core and Controller half of
[slice 3 of the software RAID plan](../plans/2026-09-22-software-raid.md#slice-3--mirrored-system-disk-in-the-installer)
— `md_arrays` in the host hardware report, the `host-system-disk-replace-v1` operation, and the
host page's **System disk mirror** panel with its **Replace disk…** dialog. The installer's own
mirrored layout is a separate lane and is **not** covered here.

**The lab host was a hand-built simulation of the installer's layout, not an installer-produced
host.** Its two 64 GiB virtual disks were partitioned, mirrored, and made bootable by hand inside
a disposable QEMU guest (BIOS firmware, virtio disks), so nothing here is evidence that the
installer produces a correct mirrored root, and nothing here was observed on physical hardware or
on a real disk. Every disk was a qcow2 file in the lab directory.

**This run was stopped part way.** The workstation was overloaded by concurrent lab virtual
machines and a stop order ended all lab work; the items under
[Outstanding](#outstanding) were not finished or not re-verified after the stop. What is reported
under [Executed checks](#executed-checks) was observed before that, and each item says what was
actually seen.

## Build under test

Working tree of 2026-09-22, uncommitted, alongside in-flight work from other sessions. Go 1.27.1
from the repository's own toolchain directory; the UI bundled with the repository's esbuild
options into the lab's own output directory, so `web/dist` was never written. Lab Controller on
`127.0.0.1:18543` with its own state directory; the disposable guest reached it as
`https://10.0.2.2:18543` over QEMU user networking.

## Executed checks

### Unit and contract checks (repeatable, no lab needed)

- `go vet ./...` — passed, no diagnostics.
- `go test ./internal/...` — passed. `internal/vmhost`: the sysfs reader against fixture trees for
  a healthy mirror, a degraded one, a rebuilding one (that fixture's file set and
  values are the lab capture's own, with the device names changed) and a machine with no sysfs at all; the
  replacement's full command sequence, the replay of a redelivered operation, and the refusals for
  a healthy array, a disk that is ineligible, a disk whose old data was not confirmed, a disk
  smaller than the remaining system disk, an unknown array, and the disk that is still carrying
  the array; the adoption of an array that already has the replacement as a member; the retry
  after an interrupted wipe; and that the retry still refuses a disk another array or pool claimed
  in the meantime (this last check fails against the code without its guard, which is how it was
  verified). `internal/controller`: the relay to the right host, the exact replay, the
  changed-payload conflict, the CSRF requirement, the old-Core 409 and five invalid-input
  refusals.
- `python3 -m unittest discover -s tests -p 'test_api_contract.py'` — passed, including the
  generated reference being current, the `SystemDiskReplaceRequest` payload matching the Go
  struct, and `host-system-disk-replace` being in the command and operation kind enums.
- `python3 scripts/generate_api.py --check` — reference current.
- `npm run typecheck` and `npm test` in `web/` — passed, `md.test.mjs` included.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'` — passed.
- `python3 scripts/check_docs.py --worktree` — passed.

### Disposable-host run (before the stop)

One disposable guest, root on `/dev/md0` (RAID1, metadata 1.2) across two virtio disks, each with
a 1 MiB BIOS-boot partition, a 512 MiB EFI system partition and the array member; a third blank
64 GiB disk was attached as the replacement. Nex Core was the working tree's own build, enrolled
with the lab Controller as the master of its own pool.

- **The report shows the array.** `GET /api/v1/hosts/{id}/hardware` returned
  `md_arrays.arrays[0]` as `md0`, `/dev/md0`, `raid1`, `raid_disks` 2, `degraded` 0,
  `sync_action` `idle`, with members `/dev/vda3` (slot 1, `in_sync`) and `/dev/vdb3` (slot 0,
  `in_sync`). The same values were captured straight from `/sys/block/md0/md/` on the host for the
  unit-test fixtures.
- **Refused on a healthy array.** A replacement submitted against the healthy `md0` was accepted
  as a task and failed on the host with `/dev/md0 is not degraded, so there is no member to
  replace`. Nothing was wiped.
- **Degraded is reported.** After `mdadm --fail` and `mdadm --remove` of one member, the report
  showed `degraded` 1 with the single remaining member, and `/proc/mdstat` showed `[2/1] [U_]`.
- **Refused on the disk that is still carrying the array.** Selecting the surviving member's own
  disk failed with `/dev/disk/by-id/virtio-NEX-RAID-A already holds a working member of
  /dev/md0`, and nothing was wiped. The first version of the code answered this case with a
  misleading "no working member to copy from"; it was fixed and the refusal above is the fixed
  behaviour, now covered by a unit test.
- **Refused on an ineligible disk.** Selecting a disk Nex Core reported as `mounted` failed with
  `the replacement disk changed since it was selected, or is in use`, without touching it. An
  earlier version reached the surviving disk's partition table first and answered with the wrong
  reason; the order was fixed so the disk is revalidated before anything else is read.
- **Replacement onto the blank disk.** Submitted through the Controller API against the degraded
  array with the blank third disk. The task succeeded in about two minutes. On the host: the new
  disk carried a replica of the surviving disk's partition table with fresh identifiers
  (`virtio-NEX-RAID-C-part1/2/3`, ESP `PARTUUID` different from the source's), GRUB's boot image
  was written to the disk's first sector with `grub-install --target=i386-pc`, and the kernel
  began the rebuild. The lab host was BIOS firmware; the shipped BIOS bootloader path does not
  run `mkfs.vfat` on the replicated ESP (an earlier pre-rewrite observation that the ESP was
  formatted `vfat` does not apply to the current code). The report then showed `sync_action`
  `recover` with `sync_percent` 25.0, the new member as `spare` in slot 1, and the array
  `degraded` 1. The UEFI ESP format-and-copy path remains unit-tested only.
- **The rebuild completed.** `/proc/mdstat` reached `[2/2] [UU]` with `vda3` and `vdc3`, and
  `mdadm --detail` showed both as `active sync`. The report then showed **Healthy** with both
  members `in_sync`. The host stayed connected and reporting throughout.
- **The host boots from the replacement disk alone** — with one correction, below. Restarted with
  only the replacement disk attached, the guest assembled `/dev/md0` degraded from it, mounted
  root and accepted SSH (`findmnt /` → `/dev/md0`, `lsblk` showing only `NEX-RAID-C`).
  **Finding for the installer lane:** before that boot succeeded it stopped in emergency mode,
  because `/etc/fstab` mounts `/boot/efi` from one named ESP and that ESP was on the disk that had
  been replaced: `Timed out waiting for device /dev/disk/by-uuid/...`, `Dependency failed for
  boot-efi.mount`, `Dependency failed for local-fs.target`. Adding `nofail` (with a short
  `x-systemd.device-timeout`) to that fstab entry made the same boot succeed. A mirrored install
  must mount the ESP with `nofail`, or losing the disk whose ESP fstab names drops the host into
  emergency mode even though the mirror is healthy. This is the installer's fstab line, not this
  operation's, and is reported to that lane.
- **Interrupted agent, then a retry.** With `sgdisk` replaced by a long sleep, a replacement was
  submitted and the agent was stopped while the operation was in flight, without reporting a
  result. The host-side journal held `"stage":"wipe-started"` and the replacement disk's
  signatures were gone, as expected after the wipe. `sgdisk` was restored and the agent restarted:
  it re-executed the same operation ID, revalidated the disk on the identity that survives a wipe
  (the fingerprint no longer matched, by design), and carried on — the journal reached
  `"stage":"member-added"` and `/proc/mdstat` showed the new member added and rebuilding. The
  final task status was not observed before the stop; see below.
- **The typed erase confirmation was exercised** in that run: the disk was reported
  `erase_required` and the request carried the confirmation naming the same `by_id` path.
- **Browser review** of the host **Stats** tab against the live host, at 1400×1000: the
  **System disk mirror** table above the Hardware sections showed `/dev/md0`, `raid1`,
  `Rebuilding · 43.8%` in the attention colour and both members with their state and slot, with
  **Replace disk…** present; after the rebuild it showed `Healthy` with both members `in_sync`
  and no **Replace disk…** button. The dialog opened from that button, showed the erase warning,
  asked the host for its disks, and — with every disk on that host in use at that moment —
  showed its empty state (*"This host reports no disk that can be used…"*), the unavailable-disk
  disclosure listing the three disks with Core's own reasons, and the danger button disabled.
  Cancel closed it. Captures: `mirror-rebuilding.png`, `mirror-repaired.png`,
  `dialog-rebuilding.png` (untracked, in the lab directory). One defect was found and fixed here:
  the danger button read *"Submitting…"* while the dialog was still listing disks.
  **Annotation (SR-005 / SR-015, 2026-09-22):** at the time of this walkthrough the dialog branch
  used `usable.every(small)` while `usable` was empty, so Array.every was vacuously true and the
  shipped empty state always showed the *"Every spare disk … is smaller …"* sentence instead of
  the *"no disk that can be used"* text recorded above. The intended distinction (and this QA
  wording) matches the fixed picker; the captures were not re-taken after the fix.

## Outstanding

- **A replacement submitted through the dialog itself.** The dialog was opened, exercised and
  cancelled; every submitted replacement in this report went through the Controller API. The
  disk-selection and typed-confirmation states of the dialog with a usable disk offered were not
  captured.
- **The interrupted retry's final task status**, and a repeat of the whole replacement after the
  code fixes made during the run (the disk-that-is-still-carrying-the-array refusal, the
  revalidation order, and the retry's eligibility guard). Those three are covered by unit tests;
  they have not been re-observed on a host.
- **UEFI firmware.** The lab guest booted BIOS, so `grub-install --target=i386-pc` is what ran.
  The UEFI path — the installer's own `/usr/local/sbin/nex-sync-esp` copier, or the same copy of
  `/boot/efi/EFI` onto the new ESP — has not been executed on any host; it is covered by a unit
  test only. It was rewritten after this run: the first version used `grub-multi-install` and
  Debian's `grub-efi/install_devices`, which Debian 13's `grub-efi-amd64` does not ship, and it
  now matches what the installer lane does.
- **A VM running on the host during the replacement.** No guest was running on the lab host, so
  "the host keeps running and its VMs are not interrupted" is verified only for the host itself.
- **Controller replacement during a rebuild**, a host with more than one array, a mirror of more
  than two members, and any real disk, real firmware or real disk failure.
- **The installer's own mirrored layout**, including the `nofail` ESP mount above, belongs to the
  installer lane and is not verified here.
