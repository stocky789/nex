# Reinstall the system image and keep the data (Repair image)

## Status

Development build, **image edition only**. The image-edition installer and its **Repair image** mode are
implemented in the installer scripts, covered by script-level tests and run once in a virtual-machine lab from a
lab ISO (not a release ISO; see Verification), so treat this page as a procedure under validation. No real host
has been repaired. It does not apply to hosts installed with the Classic edition. Use a disposable host.

**Repair image** re-lays the partition table, system slots and boot partitions of the disks that hold a
complete Nex Core data volume, writes the image on the installation media, and keeps the data volume
(`nex-data`): the host name, settings, SSH keys, virtual machines and downloaded payloads stay as they are.
It is the way back when both system slots or the boot partitions are damaged. It is not a factory reset:
**Erase and install** is the only function that wipes the data.

## Prerequisites

- The image-edition installation media (offline or network), booted in **UEFI** mode.
- The host's own disks, all of them. A mirror needs every member disk attached; Repair has nothing to keep if
  a member is missing, and says so before it touches anything.
- Each disk at least 24 GiB.

## Steps

1. Boot the installation media in UEFI mode and choose the default image-edition entry.
2. In the setup wizard, answer **Repair image** to the question that appears only when a complete Nex Core
   data volume is found. The wizard shows the disks that hold it. They cannot be changed.
3. On the network installer, choose the management adapter and set its address, as for a new install.
4. Review the disks and answer yes. The installer rewrites the system partitions and boot files, then restarts.
5. Remove the media. The host starts the repaired image and reads its own settings from the data volume.

## Expected result

The host boots, keeps its host name, network settings, SSH host keys and virtual machines, and reports the
image written by the media. Anything you added to the system image outside `/etc` and `/var` is gone.

## Troubleshooting

- **"The image edition installs on UEFI firmware only."** Restart in UEFI mode. No disk was changed. A host
  that can only boot with legacy BIOS keeps using the Classic edition.
- **The Repair question never appears.** No complete data volume was found: a disk of a mirror is missing,
  or the data was never created by the image edition. Use **Erase and install** only if the data is expendable.
- **"Repair must name exactly the disks that hold the Nex Core data."** The disks offered differ from the
  disks that hold the data; restart the wizard and do not change the disk list.

## Verification

Not verified on a real host. **Repair image** was run in a QEMU lab on a two-disk mirror (virtual UEFI, 2026-10-02): the data
partition's bytes were identical on both disks afterwards, files written to it before read back unchanged, both boot
partitions and the system image were rewritten (they had been zeroed first), the host booted from both disks together and
from each alone, and Repair was refused, changing nothing, when one mirror member was missing. Script-level tests
(`tests/test_image_installer.py`) cover the UEFI refusal, the complete-data check and the exact-disk rule; the graphical
wizard's Repair image choice and review page were drawn and inspected under a virtual display only. See
[Phase I-6 lab 2](../qa/image/phase6-lab2.md) for what ran and what did not.
