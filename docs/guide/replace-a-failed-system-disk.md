# Replace a failed system disk

## Status

Development build. A Nex Core host installed on **two disks** keeps a complete copy of its system on each
disk (partition table, boot partition with its boot loader, and both system image slots) and mirrors its
**data volume** (`nex-data`: settings, the host's `/etc` changes, virtual machine disks on the default
storage, downloaded payloads) across them. When one disk fails the host keeps running from the other: it
stays connected and its virtual machines are not interrupted. This page covers replacing the failed disk
from Nex Controller. Nex Core lays the same partition table on the new disk, copies the verified system
image from the disk that still works, makes the new disk able to start the host on its own (on UEFI and on
BIOS hosts alike) and adds it to the data mirror, which the kernel then rebuilds in the background.

The host panel's **Storage** tab shows a **System disk mirror** block below the host's disks. It names
the data array, its level and its state — **Healthy**, **Degraded** or **Rebuilding** with a progress bar
and the kernel's copy rate — and one sentence saying what, if anything, to do about it. Each member the
array still has is listed with its size and the kernel's own state for it; a failed member is marked
**Failed**, and the disk the running system started from is marked **running system** (the host disk list
shows the same mark). **Replace disk…** appears on a degraded array when no rebuild is running and the host's
Nex Core offers `host-system-disk-replace-v1`. After a replacement a **Disk replacement** line shows what
Nex Core recorded about it, whichever Nex Controller submitted it.

Known limits:

- Only a host with a mirrored data volume (two or more disks, chosen when Nex Core is installed) can
  replace a disk this way. Nex never builds a mirror after installation. A single-disk host has no
  second copy to rebuild from; the panel says "Single system disk, no redundancy".
- One disk is replaced at a time, and only while the other disk still works and the host runs from it.
  Nex Core never writes to the disk the host runs from, and refuses it if it is selected.
- The replacement must be a whole disk Nex Core reports as usable and large enough for the layout of the
  disk that still works (in practice at least as large). Extra space on a larger disk stays unused.
- The mirror protects the host's own system and data volume. It is not a backup of your virtual machines;
  see [storage repositories](storage-repositories.md) and the backup pages.
- Replacing a disk does not remove the failed one from the machine. Pull it out once the replacement has been
  accepted and, ideally, after the data mirror has rebuilt. Do not fit it into a Nex Core host again
  without erasing it.
- **Not run on real disks.** The procedure is covered by simulated disks and a fake block layer in unit
  tests only; see Verification.

## Prerequisites

- A Nex Controller administrator session, and a host that is connected (**Fresh**).
- Nex Core on that host reporting `host-system-disk-replace-v1`. An older Core shows the mirror but offers no
  **Replace disk…** button.
- A replacement disk fitted to the host and visible to it. It may be blank or may hold old data; old data is
  erased, and erasing it needs a typed confirmation.
- The host has started and been running for a few minutes, so that its boot entry is blessed (Nex Core refuses
  while a freshly installed update is still on trial boots, and says so). Do not remove the **surviving**
  disk: it holds the only copy of the host's system and data while the array is degraded.

## Steps

1. Select **Hosts** in the rail, select the host's row to open its panel, and select the **Storage** tab.
2. Read the **System disk mirror** block. A failed disk shows as **Degraded**, with fewer disks listed than
   the array has slots, or with a member whose state is `faulty`.
3. Select **Replace disk…**. Nex Core is asked for the host's disks; this takes a moment.
4. Pick the replacement disk. The disks are listed largest first, each with its size, whether it is an SSD or
   a hard disk, its model or serial, and whether it is blank or **Has old data — will be erased**. A disk
   smaller than the surviving disk cannot be picked and says so. Disks Nex Core cannot use at all, including
   the disk the host runs from, are listed with the reason under **unavailable disks**.
5. If the disk holds old data, type its `/dev/disk/by-id/...` name exactly, as shown under the field, to
   confirm the erasure. A blank disk needs no confirmation.
6. Select **Erase _disk_ and rebuild the mirror**, which names the disk you picked.

## Expected result

The request is accepted and appears under **Tasks** in the rail. The host then, by itself and in this order:
checks the disk again and refuses if anything changed; verifies the system image on the disk that still works
by reading its whole hash tree; writes the partition table; copies each verified image slot and verifies the
copy by reading it back; formats the new boot partition and fills it from the working disk's, installing the
boot loader on the new disk (a firmware boot entry on UEFI, GRUB in the disk's boot sector on BIOS); and
adds the new data partition to the mirror.

The **Disk replacement** line follows it in words — **Writing the partition table**, **Copying the system
image**, **Making the disk bootable**, **Adding the disk to the data mirror** — and then **Done**. The task
succeeds when the disk has been added to the data mirror. While the data mirror rebuilds, the panel shows
**Rebuilding** with a progress bar, the percentage and the copy rate, and rereads it by itself every few
seconds; **Healthy** with both disks listed means the host is whole again. The task may instead show
**needs attention** with text starting `still_running`: that is not a failure, the copy simply takes longer
than the host waits to answer, and the **Disk replacement** line stays accurate. The host and its virtual
machines keep running throughout, and nothing depends on your browser, on this page staying open, or on Nex
Controller staying reachable: a restart of Nex Core, a lost connection or a replaced Controller leaves the
work running or lets the host continue it by itself, and another Controller shows it from the host's own
record.

When the host is whole again it can start from either disk. On a BIOS host check that the firmware's boot
order offers the new disk too.

## Troubleshooting

- **No System disk mirror block, or "Single system disk".** This host's data volume is not mirrored, or its Nex
  Core is too old to report its arrays.
- **No Replace disk… button on a degraded array.** Wait if the panel shows **Rebuilding**, **Resyncing** or
  **Reshaping**. Otherwise the host's Nex Core
  is older than this feature and the panel says so; see [Check and apply software updates](software-updates.md).
- **"the host must be freshly connected before its system disk is replaced".** The host is not reporting right
  now. Reconnect it and retry; nothing was queued.
- **No usable disk in the picker.** Nex Core offers only whole disks that are not mounted, in swap, in another
  array or in a ZFS pool. The dialog says which case it is and gives a reason per disk. Fit a disk at least as large
  as the surviving disk and select **Refresh disks**.
- **The task fails with a reason from the host.** Nex Core checks everything again before it writes, and a
  refusal ends with "this attempt changed nothing". The reasons are, for example: the disk changed since you picked
  it or is in use; its old data was not confirmed; it is the disk the running system started from; it holds a partition
  table or data and needs the typed confirmation; the data mirror is not degraded or is still rebuilding; the
  disk is too small; the running system image on the surviving disk does not verify; the running boot entry is not
  blessed yet (wait a few minutes and retry). Fix what the reason names and start again.
- **The task fails after the disk was written.** Nex Core recorded where it stopped (the failed **Disk
  replacement** line shows the step and the reason). Select **Replace disk…** again and pick the same disk: the host
  continues from its own record and from what the disk already holds, without writing the partition table again, formatting
  the boot partition again or adding the disk to the mirror twice. A disk that the host itself laid is recognised by its
  partition table and needs no erase confirmation the second time.
- **"a replacement onto … is still running".** An earlier replacement has not finished and its disk is still
  fitted. Select **Replace disk…** and pick that same disk to let it continue, or wait until the **Disk
  replacement** line is done. If that disk has been pulled, the host gives the unfinished replacement up by itself
  when you start one for another disk.
- **"a system disk operation (an update step or a disk replacement) is running".** A software update step met a
  running replacement (or the other way round) and gave up after ten seconds. Let the replacement finish, then retry the
  update step.
- **The rebuild seems stuck.** Read the array again rather than assuming: the percentage comes from the kernel. A
  rebuild competes with the host's own work and can take hours on a large disk.
- **Destructive action.** Everything on the disk you pick is erased. Nex Core never touches any disk but the one
  named in the request, and never the disk the host runs from.

## Verification

Covered by Go unit tests on file-backed disks with a fake block layer, for both boot methods (UEFI and BIOS):
the full rebuild; the copy of the previous system slot and the skipping of one that does not verify; a power cut at
every named step followed by a retry with the same and with a new request; the refusal of the disk the host runs from
(also under another name), of non-blank disks without the erase confirmation, of too-small disks, of a healthy,
rebuilding or missing mirror, of a corrupt running slot, of a boot entry that is not blessed and of a wrong root
hash; the recorded failure and its continuation; and the lock between a replacement and the update steps. The
Controller route is tested for relay, replay, refusals and CSRF, and the host's agent for admission, definite
refusals, the full run, replay, the "still running" answer, resumption after a restart and the report. See the
[API reference](../API.md#replacing-a-failed-system-disk-post-apiv1hostsidsystem-disk).

**Not verified:** no real disk, no real md array, no firmware boot, no real `grub-install` and no browser
walkthrough of the dialog has been run. Until a disposable lab host with two disks has done this on UEFI and on BIOS,
treat this procedure as unverified on hardware and do not rely on it for a production host.
