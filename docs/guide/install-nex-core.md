# Install Nex Core in a disposable VM

## Status

Applies to the current **Nex Core 0.1.0-alpha1 installer source**. Local lab alpha; no
Controller or web GUI is installed. The earlier QA3 image has offline BIOS installation
evidence, but predates the current root-only account flow. The complete interactive procedure below
has not yet been manually rehearsed. This is not a production or physical-hardware installation
certification. [Return to the guide](README.md).

## Prerequisites

- A locally built ISO and its matching SHA256 sidecar; see [build instructions](../../installer/README.md).
  External redistribution remains subject to the [release licensing gates](../LICENSING.md).
- An isolated disposable amd64 VM with at least 2 virtual CPUs, 4 GB RAM, and one blank 24 GB
  virtual disk. Do not attach real host disks or existing VM data disks.
- Hardware virtualization exposed to the VM if you intend to run nested KVM guests.
- VM console access. BIOS mode matches the tested installation path; UEFI menu display alone
  does not establish a tested UEFI installation.

## Steps

1. From the repository's `dist` directory, verify the image before attaching it:

   ```sh
   sha256sum -c nex-core-0.1.0-alpha1-amd64.iso.sha256
   ```

   Continue only if the matching ISO reports `OK`. A checksum detects a mismatched file;
   obtain the image and checksum from a trusted build.
2. Attach that ISO to the disposable VM and boot it. Select **Install Nex Core** in the
   branded menu. The serial installation option uses `ttyS0` at 115200 baud.
3. Follow the Debian text installer for language, networking, hostname, and the root password
   with confirmation.
   Software packages are bundled; no package mirror or network connection is required for
   installation. Choose a nonblank root password and confirm it exactly; there is no default password.
   The current installer creates no ordinary user account; Debian service accounts remain.
4. Select the **blank disposable disk** for partitioning. Read the proposed changes and
   confirm only after checking that disk's identity and size. If any target is uncertain,
   stop before accepting write/erase changes.
5. Let installation finish. It installs KVM/QEMU, libvirt, SSH, time-service and supporting
   packages automatically. Do not accept an installation error as a completed host.
6. Eject the ISO and boot the installed disk. Sign in as `root` at the local console using your chosen password and
   continue with [host verification](verify-nex-core.md).

## Expected result

The installed boot menu identifies **Nex Core GNU/Linux** and the host identifies as Nex Core
0.1.0-alpha1. Management networking follows your installer choices. The installer does not
silently create a management bridge or enable a firewall policy. Keep this alpha isolated until
you have validated access controls.

Root SSH login is disabled, so a fresh root-only installation has no SSH login account.
Use the local console. Installation can work offline; later package updates require access
to configured Debian repositories. No Nex package repository is configured yet.

## Troubleshooting

- **Checksum fails:** stop and obtain or rebuild a matching ISO/sidecar pair; do not ignore it.
- **Empty password or Password input error:** continue back to the root-password prompt,
  enter a nonblank password, and repeat the same password for confirmation.
- **No bootable media:** check the attached ISO and VM boot order. Try the tested BIOS path
  on a fresh disposable disk. UEFI installation and Secure Boot remain unverified.
- **Installation/package setup fails:** retain the installer error and logs with the image
  checksum for diagnosis. Do not treat the base Debian installation alone as a successful
  Nex installation. Retry on a new disposable disk after resolving the error.
- **Installer starts again after completion:** eject the ISO and select the installed disk.
- **Wrong disk selected before writing:** cancel and correct the VM's attached storage.
  There is no Nex undo for an accepted disk overwrite.

## Verification

The [2026-09-08 alpha1 QA report](../qa/iso/2026-09-08-alpha1.md) records the exact tested image
hash, boot-menu screenshots, and reproduction command. Its automated test installs onto a
fresh disk with **no virtual NIC**, removes the ISO, and boots the installed host twice.

That QA3 image predates the current root-only account source change. Its test injects temporary
disk/account answers into a test-only initrd; it does **not** exercise
the shipping interactive prompts or validate every step above. Full manual installation walkthrough,
multiple-disk safety, physical hardware, UEFI installation, and Secure Boot remain outstanding.

The newer [root-only installer verification](../qa/iso/2026-09-08-root-account.md)
exercises the actual root-password screens, blank/mismatched retries, and successful
confirmation advancing directly to partitioning without ordinary-user questions.
It records the rebuilt image checksum and inspected screenshots separately from QA3.
