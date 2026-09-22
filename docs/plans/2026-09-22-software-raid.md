# Software RAID: redundant VM storage that stays usable, and a mirrored system disk

Status: decided 2026-09-22, implementation in progress in the working tree. It extends the
[ZFS repository provisioning plan](2026-09-19-zfs-repository-provisioning.md) and the
[installer section](2026-09-08-nex-platform.md#3-nex-core-packaging-and-installer-iso) of the
platform plan. It follows established infrastructure-management conventions for host-local
redundancy; the design below is Nex's own and is checked against the
[architecture invariant](../../AGENTS.md#the-architecture-invariant-nex-core-does-the-work-nex-controller-only-asks).

## What exists today

- **VM storage.** The ZFS wizard already builds redundant zpools from selected disks: `single`,
  `mirror` (exactly two disks), `raidz1`, `raidz2` and `striped_mirrors`
  (`protocol.ValidZFSLayout`, `zfsVdevArgs`). That is software RAID for VM storage, and it is
  Core-owned, journaled, re-adoptable and proven in the disposable lab.
- **It does not survive the failure it exists for.** `inspectZFS` treats every pool health other
  than `ONLINE` as inaccessible, so a mirror or RAIDZ pool with one failed disk (`DEGRADED`) is
  reported unreadable and unwritable while ZFS is in fact still serving every VM on it. The
  2026-09-19 QA record observed exactly this and recorded it as the accepted behaviour. That
  makes the redundancy cosmetic: an operator who loses one disk of a mirror loses the
  repository in Nex's eyes until it is repaired.
- **No repair path.** Core has no operation to replace a failed pool member, nothing reports
  which disk failed, and nothing reports resilver progress. Device discovery
  (`DiscoverZFSDevices`) already knows which disks are blank, in use, or need an erase
  confirmation, so a replacement wizard can reuse it unchanged.
- **System disk.** The installer installs one whole disk with ext4 (`partman-auto` `atomic`
  recipe). A host whose system disk dies is reinstalled; its VM data on separate ZFS disks
  survives, but the host is down until then. `recheck.sh` already stops and zeroes leftover md
  arrays on the selected disk and marks partman's md/LVM scan done (BUG-0413).
- **Btrfs.** Registering an existing Btrfs path as a repository is allowed by the filesystem
  allowlist. Nothing provisions Btrfs, and no plan asks for it.

## Decisions

Decision: project owner, 2026-09-22 — all three recommendations below were accepted, with
three-way mirrors, RAIDZ3 and hot spares added to slice 2 and SMART reporting kept out (a later
plan). Implementation of every slice was authorised the same day.

1. **Btrfs RAID provisioning: no.** ZFS already covers every redundant layout Nex
   needs, with per-device health, resilver reporting and a stable replace procedure. Btrfs
   multi-device profiles do not auto-mount degraded, RAID5/6 are unsafe upstream, and qcow2
   files on Btrfs need `nodatacow`, which discards the checksums that justify Btrfs. Building a
   second provisioning engine, health parser and repair path would double the work for a
   worse result. Keep "register an existing Btrfs path" as it is; add nothing.
2. **System-disk redundancy: a Linux md RAID1 mirror, not ZFS or Btrfs root.** The
   Debian installer already creates md arrays from a preseed (`partman-auto-raid`), the
   Debian kernel and initramfs assemble them at boot with no Nex code, and a degraded mirror
   boots. ZFS root is not supported by the Debian installer and would replace the whole
   partitioning step with Nex-owned scripts, plus a module-signing dependency the ZFS plan
   already lists as open. A Btrfs root mirror does not boot degraded without manual kernel
   arguments. Only a mirror is offered: RAID0, 5, 6 and 10 add nothing for a small system disk.
3. **Degraded means usable.** A ZFS repository whose pool is `DEGRADED` stays readable and
   writable and is shown as needing attention. Only `FAULTED`, `UNAVAIL`, `SUSPENDED` and
   `REMOVED` make it inaccessible. This changes an accepted QA outcome, so it is recorded here
   as a decision rather than slipped in as a fix.

The slices below are ordered by value and by the Core-first rule; slice 1 unblocks slice 2's
replace operation, and slice 3 is independent of both.

## Slice 1 — degraded pools stay in service, and Core reports why

Status 2026-09-22: implemented in the working tree and verified in the disposable ZFS lab
([evidence](../qa/2026-09-22-zfs-degraded.md), BUG-0443). `zpool replace` remains slice 2.

**Core behaviour.** `zfsProbe` returns the health string it already reads. `inspectZFS` maps
it: `ONLINE` → healthy; `DEGRADED` → `degraded`, access unchanged; anything else →
inaccessible with the health in the error, as today. Pool detail is read once per observation
with `zpool status --json` (OpenZFS 2.3, which Nex Core ships) so no hand-written parser is
needed: pool state, `status`/`action` sentences, scan (scrub or resilver) progress, and the
vdev tree with each leaf's by-id path, state and read/write/checksum error counts. The
leaves are reported without their vdev class, so a hot spare, log or cache device is not
separately identified; labelling them needs a captured `zpool status --json` of a pool with
a spare and is outstanding.

**Durable state.** None new. Pool health and the vdev tree are transient observations. The
repository record on the master, the ZFS `nex:repository` property and each host's journal are
unchanged. `state.owner: core`, existing records.

**Host API.** *Changed contract*: the `zfs_host_access` observation and `StorageAccess.status`
gain the value `degraded`; the per-host ZFS observation gains a `pool` object carrying the
detail above. Controller passes it through and stores none of it. Old Controllers see an
unknown status string; the existing display falls back to showing the status text, which is
acceptable and must be checked in the browser.

**Outage and recovery.** Nothing schedules. A pool that degrades while the Controller is away is
reported on the next observation. A monthly scrub of every imported pool is already run by the
`zfsutils-linux` package's cron entry on Nex Core; the plan reuses it and shows its result in
the pool detail instead of adding a Nex scheduler.

**Controller UI and guide.** The repository page shows per-host pool state, the failed
device by id, and resilver or scrub progress. The storage guide describes what **Degraded**
means for a ZFS repository, that VMs keep running, and that a replacement should be done
promptly. QA repeats the 2026-09-19 offline-device check and now expects the VM to keep
reading and writing.

Checks: Go unit tests for the health mapping and the JSON status decoding from captured
`zpool status --json` output; a disposable-host run with one mirror member offlined while a VM
writes.

## Slice 2 — replace a failed pool member

Status 2026-09-22: implemented in the working tree — the widened layouts and hot spares, the
`storage-zfs-replace-v1` operation with its master aggregate, peer route and host worker, the
[API reference](../API.md#replacing-a-failed-pool-disk-post-apiv1storagezfsreplace) and the
[guide workflow](../guide/storage-repositories.md#replace-a-failed-disk), exercised in the
disposable ZFS lab and in the browser ([evidence](../qa/2026-09-22-zfs-replace.md)). The slice
is **not closed**: independent review found that a retry would have cleared the disk the pool
was resilvering onto (BUG-0445, fixed with unit checks), and the lab work that record lists as
outstanding — the interrupted retry rerun with the interruption after `zpool replace` starts,
retained output for the three-way mirror and hot-spare checks, RAIDZ3 provisioning, automatic
hot-spare engagement, and a capture that would let Core label spare, log and cache devices —
has not been run since the lab was stopped.

**Core behaviour.** A new durable master operation, `storage-zfs-replace-v1`, names one
repository, one host, the failed leaf vdev (by-id path as reported in slice 1) and one
replacement device from the same discovery listing the wizard uses, with the same erase
confirmation and fingerprint rules. Core revalidates both at execution time: the failed vdev
must still be in the pool and not `ONLINE`; the replacement must be eligible now. It runs
`wipefs --all` on the confirmed replacement and `zpool replace <pool> <old> <new>`. Resilver
progress arrives through slice 1's observation, so the operation completes when `zpool
replace` returns and the operator watches the pool detail until the scan finishes. Nothing
detaches the old device by hand: ZFS removes it when the resilver completes.

Layouts widen while this code is open: `mirror` accepts two or three devices (a three-way
mirror is what a replacement needs when the operator wants to add a disk before removing a
suspect one), and `raidz3` is accepted at five or more devices. Each host's disk list also
gains an optional **Spare** tick: any number of spare devices, passed as `spare` vdevs at
creation with `autoreplace=on`, so ZFS resilvers onto a spare by itself and the Replace action
then retires the failed disk. Spares count toward nothing in the layout rule; the data disks
alone must satisfy it. No dRAID, no vdev additions, no pool growth; the ZFS plan's remaining
out-of-scope list stands.

**Durable state.** `state.owner: core`. The replace operation is journaled on the executing
host like provisioning, keyed on the operation ID, so a retry after an interruption inspects
before acting: a pool already resilvering onto the replacement is adopted as success; a
replacement device that is no longer exactly as observed is `needs_attention` and never
erased. The master records the outcome on the repository record as it does for provision
results. A fresh Controller learns nothing from this operation that it needs: the pool's
current device set is what `zpool status` reports.

**Host API.** *New contract*, capability `storage-zfs-replace-v1`. Administrator session and
CSRF at the Controller; master peer authentication when the master forwards it to a member.
Invalid input, an old Core without the capability, an `ONLINE` "failed" vdev, and an
ineligible replacement are refused with reasons. Retries key on the operation ID.

**Controller UI and guide.** From the degraded repository's host row: **Replace disk** →
choose the replacement from the same picker → typed erase confirmation when needed → confirm.
The guide gains a "Replace a failed disk" workflow with the resilver expectation and the rule
that the old disk is removed only after ZFS finishes.

Checks: contract tests for authorization, invalid input and refusal paths; a disposable-host
run that fails a mirror member, replaces it, waits for resilver and verifies data with a VM
still running; interruption of the executing agent between `wipefs` and `zpool replace`.

## Slice 3 — mirrored system disk in the installer

Status 2026-09-22: the Core and Controller half is implemented in the working tree — `md_arrays`
in the host hardware report, the `host-system-disk-replace-v1` operation, the host page's System
disk mirror with **Replace disk…**, the [API reference](../API.md#replacing-a-failed-system-disk-post-apiv1hostsidsystem-disk),
the [guide page](../guide/replace-a-failed-system-disk.md) and the
[QA record](../qa/2026-09-22-system-disk-replace.md), whose disposable-host run reached a
completed replacement, rebuild and boot from the replacement disk before it was stopped part way;
its Outstanding section is the remaining acceptance work. That run also found that the installer
must mount the EFI system partition with `nofail`: a host whose fstab names the replaced disk's
ESP stops in emergency mode even with a healthy mirror.

Status 2026-09-22 (installer): implemented in the working tree, lab acceptance OUTSTANDING --
the multiple-disk Disk step in both frontends, per-disk validation with the 10 % rule, the partman
RAID recipe, GRUB on every member, `mdadm` in the image, `installer/check.sh` assertions, the
[install guide](../guide/install-nex-core.md) and the
[ISO QA record](../qa/iso/2026-09-22-installer-mirror.md). Source checks and the wizard screens
passed and two-disk installs reached the base system, but no mirrored host was booted: the boot,
disk-detach, leftover-array and single-disk end-to-end checks are listed as outstanding in that
record, so this slice is not complete. Three findings changed the design above: partman-md's
`/var/lib/partman/md` marker gates only its own scan, assembly and removal, so BUG-0413's guard
does not stop `partman-auto-raid` creating the new array (no change needed); Debian 13's
`grub-efi-amd64` has no multi-ESP support (no `grub-efi/install_devices`, no `grub-multi-install`
-- that is Ubuntu's), so the installer writes the other members' ESPs itself by copying the ESP
GRUB maintains, repeated by an APT `DPkg::Post-Invoke` hook; and the mirrored layout has no swap,
so the preseed answers partman's swap warning instead of interrupting the operator.

**Installer behaviour.** The Disk step lets the operator pick one disk, as now, or two or
more disks for a mirror (md RAID1 mirrors any number of members; the common case is two).
Disks are offered under the current rules (whole, writable, nonremovable, at least 16 GiB, not
the installation medium), and every mirror member must be within 10 % of the smallest
selected disk's capacity so the array is not silently shrunk to a much smaller disk. Both the GTK wizard and the text frontend ask the same question; `nex-config.c`
validates the identity, size and serial of every selected disk, not just one, and the erase
confirmation names both disks.

Layout, per selected disk (GPT on both firmwares): a 1 MiB BIOS boot (`biosgrub`) partition, a
538 MB EFI System Partition, and the md RAID1 member as partition 3 (`metadata=1.2`) holding a
single ext4 root. `recheck.sh` clears signatures on every selected disk. The preseed generated by
`nex-config.c` switches from `partman-auto/method regular` to `raid` with a
`partman-auto-raid/recipe` and an expert recipe for those three partitions; BUG-0413's guard that
`partman-md/device_remove_md` is never seeded stays, so arrays on unselected disks are not
touched. Whether partman's md initialisation still runs when `recheck.sh` marks the md scan
done is the first thing the lab must establish; the marker exists only to keep leftover
arrays out of partman, and the recipe creates a new array.

Boot: GRUB is installed on every selected disk. Debian 13's `grub-efi-amd64` has no multi-ESP
support (`grub-efi/install_devices` / `grub-multi-install` are Ubuntu's), so Nex copies the ESP
GRUB maintains onto the other members with `/usr/local/sbin/nex-sync-esp` and an APT
`DPkg::Post-Invoke` hook, and legacy BIOS runs `grub-install --target=i386-pc` on each disk.
Either disk alone is intended to boot the host; the lab must still prove that by detaching each
disk in turn (see the installer status above).

**Superseded 2026-09-22:** an earlier draft of this section described a two-partition layout (ESP
plus md member only) and claimed Debian's `grub-efi-amd64` kept several ESPs in step with no Nex
code. That design was disproved on Debian 13 and replaced by the three-partition GPT layout and
ESP-copy approach above.

**Core behaviour.** The host hardware report (`readHardware`, from the
[hardware plan](2026-09-22-host-hardware-and-sensors.md)) gains the md arrays it finds in
`/sys/block/md*/md/` — level, degraded flag, sync action and progress, member devices and
their states — so a failed system-disk member appears on the host page beside the disk it
was. A new operation `host-system-disk-replace-v1` takes one replacement device from the
existing discovery listing, copies the partition table from the surviving member with
`sgdisk --replicate` and randomises its GUIDs, adds the new member partition with
`mdadm --add`, formats the new ESP and makes it bootable the way the installer does — on BIOS
`grub-install`, and on UEFI the host's own ESP copier, since Debian's `grub-efi-amd64` writes only
the mounted ESP. Rebuild progress is read from sysfs.

**Durable state.** `state.owner: core`, host-local: `/etc/mdadm/mdadm.conf` and the array
superblocks, written by the installer and read by the initramfs. Nothing is pool-scoped and
nothing lives in the Controller. A fresh Controller sees the mirror in the host hardware
report.

**Host API.** *Changed contract*: the host hardware report gains `md_arrays`. *New contract*:
`host-system-disk-replace-v1`, capability-gated, administrator session and CSRF, journaled on
the host and keyed on the operation ID like the ZFS replace; a retry adopts an array that
already has the replacement as a member. Refused with reasons when the array is not degraded,
when the device is ineligible or smaller than the surviving member, or when the Core lacks the
capability.

**Outage and recovery.** Nothing depends on the Controller. md resync runs in the kernel;
GRUB updates keep both ESPs current without an open session. A host that lost a disk keeps
running and keeps its VMs; the installer's own recipe never writes `mdadm.conf` entries for
arrays it did not create.

**Controller UI and guide.** The host page shows the system disk mirror and its rebuild
state, with **Replace disk** when degraded. The install guide gains the mirror choice; a new
"Replace a failed system disk" workflow lists the steps and the expectation that the host
stays up throughout.

Checks: `installer/check.sh` gains assertions for the RAID recipe and both-disk validation;
Go tests for the sysfs md parser and the replace operation's refusals; a disposable two-disk
ISO install in both UEFI and BIOS, a boot with each disk detached, a live replace while a VM
runs, and the existing single-disk install unchanged.

## Not in this plan

Btrfs provisioning of any kind (decision 1). ZFS root. dRAID, vdev additions, pool
expansion and pool destroy (unchanged from the ZFS plan). SMART reporting, which needs
`smartmontools` in the image and belongs to the hardware plan. md RAID for VM storage:
ZFS covers it. Encryption.

## API and documentation impact

API impact: **changed contract** (slice 1 storage status value and ZFS observation detail;
slice 3 hardware report) and **new contract** (`storage-zfs-replace-v1`,
`host-system-disk-replace-v1`). Each implementing slice updates `docs/API.md`, the
machine-readable contracts and the generated reference, and ships its contract tests. Guide
impact per slice: **updated** (`docs/guide/storage-repositories.md`, the install guide and a
host disk replacement page). State owner for every slice: **core**, as recorded above.

## Acceptance

A slice is complete only when its host-API checks, interruption and retry checks, and
Controller-replacement check pass on disposable hosts with disposable virtual disks, its UI
has been exercised in a real browser, and its guide workflow has been walked through against
the build. No real disks or existing operator hosts are targets of this work.
