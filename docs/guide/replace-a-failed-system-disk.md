# Replace a failed system disk

## Status

Development build. A Nex Core host installed onto a **mirrored system disk** keeps running when one
of the two disks fails: the Linux kernel carries on with the surviving disk, the host stays
connected, and its virtual machines are not interrupted. This page covers replacing the failed disk
from Nex Controller so the mirror is rebuilt and the host can boot from either disk again.

The host page's **Stats** tab shows a **System disk** panel above the Hardware sections. It names
the array, its level and its state — **Healthy**, **Degraded** or **Rebuilding** with a progress
bar and the kernel's copy rate — and one sentence saying what, if anything, to do about it. Below
it, each disk the array still has is listed with its size and the kernel's own state for it, and a
failed disk is marked **Failed**. **Replace disk…** appears on a degraded RAID1 array when no
rebuild is running. A host whose system disk is not mirrored says so instead, and one whose Nex
Core is too old to report its arrays says that.

Known limits:

- Only a RAID1 mirror is rebuilt this way, and one disk at a time; a mirror of three or more
  disks is rebuilt one replacement per run. Nex never builds a system mirror after installation;
  the mirror is chosen when Nex Core is installed.
- The replacement disk must be at least as large as the remaining system disk, and must be a whole
  disk Nex Core reports as usable — not mounted, not in another array, not in a ZFS pool.
- The mirror protects the host's own system disk. It is not a backup of your virtual machines,
  whose disks live on [storage repositories](storage-repositories.md).
- Replacing a disk does not remove the failed one from the machine. Pull the failed disk out after
  the rebuild has finished.

## Prerequisites

- A Nex Controller administrator session, and a host that is connected (**Fresh**).
- Nex Core on that host reporting `host-system-disk-replace-v1`. An older Core shows the mirror but
  offers no **Replace disk…** button.
- A replacement disk fitted to the host and visible to it, at least as large as the surviving
  system disk. It may be blank or may hold old data; old data is erased, and erasing it needs a
  typed confirmation.
- The failed disk may still be physically present or already removed. Either way, do not remove the
  **surviving** disk: it holds the only copy of the host's system while the array is degraded.

## Steps

1. Open **Hosts**, select the host, and open its **Stats** tab.
2. Read the **System disk mirror** table. A failed disk shows as **Degraded**, with fewer disks
   listed than the array has slots, or with a member whose state is `faulty`.
3. Select **Replace disk…**. Nex Core is asked for the host's disks; this takes a moment.
4. Pick the replacement disk. The disks are listed largest first, each with its size, whether it is
   an SSD or a hard disk, its model or serial, and whether it is blank or **Has old data — will be
   erased**. A disk smaller than the remaining system disk cannot be picked and says so. Disks Nex
   Core cannot use at all are listed, with the reason, under **unavailable disks**.
5. If the disk holds old data, type its `/dev/disk/by-id/...` name exactly, as shown under the
   field, to confirm the erasure. A blank disk needs no confirmation.
6. Select **Erase _disk_ and rebuild the mirror**, which names the disk you picked.

## Expected result

The request is accepted and appears in the **Tasks** strip at the bottom of the host page
(select it to expand; it shows the last five tasks and a count of running and failed ones).
The host then, by itself:
erases the replacement, copies the partition layout from the surviving disk, adds the new partition
to the mirror, and makes the new disk bootable — on a UEFI host by formatting its EFI system
partition and copying the boot files onto it (from `/boot/efi` when that mount has them, or from
the surviving disk's own EFI system partition when the failed disk was the one mounted there), and
on a BIOS host by installing the bootloader to the disk, which is all a BIOS mirror needs.

The panel follows it: it shows the task's outcome when the host reports it, and while a rebuild
runs it rereads the mirror by itself every few seconds, showing **Rebuilding** with a progress bar,
the percentage and the copy rate, then **Healthy** with both disks listed when the rebuild has
finished. **Retry** on the Hardware panel rereads it immediately. A rebuild takes as long as it takes to copy the disk;
the host and its virtual machines keep running throughout, and nothing depends on your browser, on
this page staying open, or on Nex Controller staying reachable.

When the array is healthy again, the host boots from either disk on its own. On a UEFI host the
copies on both disks' EFI system partitions are kept in step after later GRUB updates with no
further action.

## Troubleshooting

- **No System disk mirror table.** This host's system disk is not mirrored, or its Nex Core is too
  old to report its arrays. A single-disk host shows nothing here.
- **No Replace disk… button on a degraded array.** Wait if the panel shows **Rebuilding**,
  **Resyncing** or **Reshaping** — the rebuild must finish first. Otherwise the host's Nex Core
  is older than this feature and the panel says so; see [Check and apply software updates](software-updates.md).
- **"the host must be freshly connected before its system disk is replaced".** The host is not
  reporting right now. Reconnect it and retry; nothing was queued.
- **No usable disk in the picker.** Nex Core offers only whole disks that are not mounted, in swap,
  in another array, in an LVM volume group or in a ZFS pool. The dialog says which case it is —
  every disk in use, or every spare disk too small — and the listed reason per disk says why. Fit a
  disk at least as large as the remaining system disk and select **Refresh disks**.
- **The task fails with a reason from the host.** Nex Core checks everything again before it
  touches a disk, and refuses rather than acting when the array is not degraded, when it is still
  rebuilding, when it is not a RAID1 mirror, when the disk changed since you picked it or is in
  use, when its old data was not confirmed, or when it is smaller than the remaining system disk.
  Fix what the reason names and start again; nothing was erased.
- **The task fails after the disk was erased.** The replacement is journaled on the host. Submit the
  replacement again for the same disk: the host resumes from where it stopped, and a disk that is
  already a member of the array is adopted rather than erased a second time. That includes a retry
  after the bootloader step failed, even once the mirror has finished rebuilding onto the new disk.
- **The rebuild seems stuck.** Read the array again rather than assuming: the percentage comes from
  the kernel. A rebuild competes with the host's own work and can take hours on a large disk.
- **The host stops in emergency mode after a disk is replaced.** This applies to mirrored hosts
  installed before this build and to hand-built mirrors. A host whose `/etc/fstab` mounts
  `/boot/efi` from the disk that failed waits for a disk that is no longer there, even though the
  mirror itself is healthy. Add `nofail` to that line's mount options from the host console, or
  point it at the surviving disk's EFI system partition, and reboot. Installs from this build
  already mount `/boot/efi` with `nofail` and a short device timeout; if such a host still stops
  in emergency mode, look for another fstab or boot problem — missing `nofail` is not the cause.
  The installer-side `nofail` mount has not yet been seen on a booted host.
- **Destructive action.** Everything on the disk you pick is erased. Nex Core never touches any disk
  but the one named in the request, and never the surviving system disk.

## Verification

See the [system disk replacement QA record](../qa/2026-09-22-system-disk-replace.md) for the build,
the environment and exactly which steps were exercised, including the refusals and the interrupted
retry, and for the limits that remain untested.
