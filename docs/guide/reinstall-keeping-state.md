# Reinstall the system image and keep the data (Repair image)

## Status

Development build, **image edition only**. The image-edition installer and its **Repair image** mode are
implemented in the installer scripts and covered by script-level tests; no ISO has been built and no real
install or repair has been booted yet, so treat this page as a procedure under validation. It does not apply
to hosts installed with the Classic edition. Use a disposable host.

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

Not verified on a real or virtual host. Script-level tests (`tests/test_image_installer.py`) cover the UEFI
refusal, the complete-data check and the exact-disk rule. A QEMU repair run is outstanding; see
[installer phase 2 QA](../qa/image/phase2-installer.md).
