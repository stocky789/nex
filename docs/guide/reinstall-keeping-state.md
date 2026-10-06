# Reinstall the system image and keep the data (Repair image)

## Status

Development build. The installer's **Repair image** mode is
implemented in the installer scripts, covered by script-level tests and run once in a virtual-machine lab from a
lab ISO (not a release ISO; see Verification), so treat this page as a procedure under validation. No real host
has been repaired. Use a disposable host.

**Repair image** re-lays the partition table, system slots and boot partitions of the disks that hold a
complete Nex Core data volume, writes the image on the installation media, and keeps the data volume
(`nex-data`): the host name, settings, SSH keys, virtual machines and downloaded payloads stay as they are.
It is the way back when both system slots or the boot partitions are damaged. It is not a factory reset:
**Erase and install** is the only function that wipes the data.

## Prerequisites

- The installation media (offline or network), booted in either **UEFI** or **BIOS** mode.
- The host's own disks, all of them. A mirror needs every member disk attached; Repair has nothing to keep if
  a member is missing, and says so before it touches anything.
- Each disk at least 24 GiB.
- Boot from the installer, not from a selected target. Targets must be whole disks with no
  mounted partitions, active swap or device-mapper/md holders when the disk writer begins.
  Only arrays on the explicitly selected disks may be stopped; a refusal leaves disk bytes unchanged.

## Steps

1. Boot the installation media (UEFI or BIOS) and choose the default entry.
2. In the setup wizard, answer **Repair image** to the question that appears only when a complete Nex Core
   data volume is found. The wizard shows the disks that hold it. They cannot be changed.
3. On the network installer, choose the management adapter and set its address, as for a new install.
4. Review the disks and answer yes. The installer rewrites the system partitions and boot files, then restarts.
   In the text installer, **Go Back** or **No** on the offline repair confirmation returns to
   the Erase/Repair choice. On netinst, **Go Back** revisits the network questions first.
   Selecting **Erase and install** changes to the destructive installation workflow; confirm
   it only if keeping the existing data is no longer required.
5. Remove the media. The host starts the repaired image and reads its own settings from the data volume.

## Expected result

The host boots, keeps its host name, network settings, SSH host keys and virtual machines, and reports the
image written by the media. Anything you added to the system image outside `/etc` and `/var` is gone.

## Troubleshooting

- **The Repair question never appears.** No complete data volume was found: a disk of a mirror is missing,
  or the data was never created by Nex Core's installer. Use **Erase and install** only if the data is expendable.
- **"Repair must name exactly the disks that hold the Nex Core data."** The disks offered differ from the
  disks that hold the data; restart the wizard and do not change the disk list.
- **Disk in use, partition target, duplicate disk or image too large:** do not override the
  refusal. Use the correct offline whole disks, remove only their proven active users, or obtain
  an image that fits the system slots. These checks run before any system partition is rewritten.

## Verification

Not verified on a real host. **Repair image** was run in a QEMU lab on a two-disk mirror (virtual UEFI, 2026-10-02): the data
partition's bytes were identical on both disks afterwards, files written to it before read back unchanged, both boot
partitions and the system image were rewritten (they had been zeroed first), the host booted from both disks together and
from each alone, and Repair was refused, changing nothing, when one mirror member was missing. Script-level tests
(`tests/test_image_installer.py`) cover the complete-data check and the exact-disk rule; the graphical
wizard's Repair image choice and review page were drawn and inspected under a virtual display only. See
[Phase I-6 lab 2](../qa/image/phase6-lab2.md) for what ran and what did not.

[Immutable review-fix checks](../qa/2026-10-03-immutable-review-fixes.md) cover all-target
preflight using controlled block observations, gzip rejection and actual CLI refusal of scratch
file aliases. The text wizard ran through a simulated debconf frontend. They do not establish
a new physical install/repair, firmware or real block-device observation.
