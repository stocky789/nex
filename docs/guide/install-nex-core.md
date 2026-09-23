# Install Nex Core in a disposable VM

## Status

Local lab alpha of the Nex Core installer. The ISO is named after the Nex Core version it
installs (`nex-core-<version>-amd64.iso`); its boot menu offers the Nex wizard as a graphical
or a text interface (English, five keyboard layouts, IPv4). Both ask the same questions and
install the same host; only the presentation differs. Installed hosts
include the host-local web UI, from which Nex Controller can be deployed. Not certified for
production or physical hardware.

Nex Core is a distribution, not a package set added to Debian: `nex-core-local` refuses to
install on a system the installer did not create (recorded as `/etc/nex/core-release`).
Later updates come from the signed Nex APT archive; see [software updates](software-updates.md).

The installer includes the complete Windows guest-driver media in compressed form
and restores it locally during installation. No extra download or operator step is
required, and the installed driver ISO is unchanged.

## Prerequisites

- A locally built ISO and its matching SHA256 sidecar; see [build instructions](../../installer/README.md).
  External redistribution remains subject to the [release licensing gates](../LICENSING.md).
  The current working `0.4.8` tree is not a released artifact.
- An isolated disposable amd64 VM with at least 2 virtual CPUs, 4 GB RAM, and one blank 24 GB
  virtual disk. Do not attach real host disks or existing VM data disks.
- Hardware virtualization exposed to the VM if you intend to run nested KVM guests.
- VM console access. BIOS mode matches the tested installation path; UEFI menu display alone
  does not establish a tested UEFI installation.

## Steps

1. From the repository's `dist` directory, verify the image before attaching it:

   ```sh
   sha256sum -c nex-core-<version>-amd64.iso.sha256
   ```

   Replace `<version>` with the version in the built file name.

   Continue only if the matching ISO reports `OK`. A checksum detects a mismatched file;
   obtain the image and checksum from a trusted build.
2. Attach that ISO to the disposable VM and boot it. Use the arrow keys and Enter in the branded menu to select
   **GUI install - Nex Core** (mouse and keyboard) or **Minimal install - Nex Core (text)**
   (keyboard only, for a machine whose graphics do not work). The menu waits for your choice.
   Both run the Nex wizard, collect the same settings and install the same headless hypervisor;
   neither installs a desktop. The serial option uses `ttyS0` at 115200 baud and is the one mode
   that still runs Debian's own installation questions rather than the Nex wizard.
3. For **GUI install**, wait for **Begin setup** on **Welcome to Nex Core**. The wizard
   introduces Nex Core and its local web UI; deploy Nex Controller from that UI or
   separately for central management. The wizard fills the available installer screen and follows display-resolution changes, including typical BMC/IPMI consoles at 1024×768 and 800×600. The left
   step list shows your position; the form and navigation buttons are on the right. Hardware
   detection enables inactive adapters only to check their link, without applying your
   saved IP settings, starting DHCP or erasing a disk. Use **Begin setup**,
   then **Name this host** to enter a hostname and select your keyboard layout. The five
   choices are **English (United States)**, **English (United Kingdom)**, **German**,
   **French** and **Spanish**. The layout applies
   immediately. Choose **Timezone area** from its dropdown, then choose **Timezone**
   from the shorter list for that area, for example **Australia** then **Australia/Sydney**.
   No timezone typing is required. **UTC** is the default area and timezone.
   Enter a root password of at least eight characters and matching confirmation.
   **Show passwords** lets you check both entries and switches off when you
   leave this page. Successful keyboard setup is silent; an application failure shows an error and blocks continuation.
   Changing the keyboard layout clears both passwords; enter them again
   using the selected layout. Use Tab/arrow keys or the mouse; **Back** revisits a page
   before installation starts. Long pages scroll vertically when needed while navigation stays visible;
   field tooltips provide additional input guidance.
4. On **Connect your host**, you can also edit **Hostname**. It is the same value as on
   the Host page; edits in either place appear in the other and in the final review. Use
   a short name such as `nexcore` or a qualified name such as `nexcore.example.invalid`.
   Each dot-separated label allows 1–63 ASCII letters, digits or internal hyphens;
   the complete name allows 253 characters, plus an optional final dot. Uppercase is
   accepted; the final dot is removed when saving. Internationalized names use their
   ASCII/Punycode form (`xn--…`). Spaces, underscores, empty labels and IP addresses
   are not hostnames. Select each detected NIC to see its name, MAC address, link
   status and available driver/speed information. A green light beside **Link detected** means the adapter
   reports carrier; **No link** means an enabled adapter reports no carrier; **Link unknown**
   means the installer could not check reliably. The selected adapter refreshes every two
   seconds without replacing your entries. For a VM, this reports its virtual NIC link,
   not the physical host’s switch uplink. A detected link does not prove IP/VLAN connectivity. Choose **Disabled**, **Automatic (DHCP)**
   or **Static IPv4** independently for each adapter. Choosing DHCP shows **Looking for an
   IP address…**, then the address returned for that NIC and VLAN. If no address arrives,
   check the link/VLAN and select **Retry**. The check does not apply addresses, routes or
   DNS to the installer. The installed host requests DHCP again, so its address may differ.
   Static addresses include a prefix,
   for example `192.168.1.20/24`. Set **VLAN ID** to `0` for an ordinary
   untagged connection or `1`–`4094` for one tagged connection; the switch port must carry
   that VLAN. Each physical NIC supports one untagged or one tagged interface in this alpha,
   not both, and not multiple VLANs, bonds, custom bridges, trunks or IPv6 configuration. Debian IPv6
   autoconfiguration remains enabled, including on an untagged VLAN parent; this IPv4
   editor does not provide IPv6 isolation.
5. Select **Management NIC** to identify the enabled adapter that owns the default route
   and DNS. The installer creates the management bridge `br0` on that adapter (or its
   selected VLAN), with the host IP and gateway on the bridge. This also provides the
   LAN connection for Controller deployment. Enter a gateway only on that adapter,
   in the same subnet as its static address.
   Separate IPv4 **DNS servers** with spaces to set explicit host DNS. With management
   DHCP, the DHCP server supplies the gateway and, if this field is empty, DNS; explicit
   DNS addresses override DHCP DNS. Other DHCP adapters receive addresses without
   replacing the management default route or DNS. **Offline (no management network)** is
   available; leave every adapter disabled for a fully offline host. These choices take
   effect on the installed host's first boot, not while the installer runs.
6. On **Choose the installation disks**, explicitly select the **blank disposable disk**, or
   two or more of them for a mirrored system disk.
   The GUI supports writable, nonremovable whole SATA/SCSI, virtio, Xen or NVMe disks of at
   least 16 GiB (`sd…`, `vd…`, `xvd…`, `nvme…`). Installation media and partitions are not eligible.
   One disk erases that disk and installs a whole-disk ext4 layout. Two or more disks are
   erased and installed as a Linux software RAID1 mirror (`md`) holding one ext4 root, with a
   small boot partition and a 512 MiB EFI System Partition on each disk, so any of them can start
   the host on either UEFI or legacy BIOS firmware. Every mirror
   disk must be within 10% of the smallest selected disk's capacity, because a mirror is only as
   large as its smallest member; a mismatched selection is refused with that reason.
   The list is a tick list in both installers: click the **Use** box, or move to a disk and press
   Space. Each row names the device, capacity, type (SSD, HDD or NVMe), model, serial and whether
   the disk already **Has partitions**, so you can tell which disk holds old data. The line under
   the list says what your ticks will produce - `1 disk: single disk, ext4, no redundancy` or
   `2 disks: RAID1 mirror, smallest disk 128.0 GiB, keeps running if one disk fails` - and turns red with
   the reason if a ticked disk is too far from the smallest one. Disks under 16 GiB are not listed.
   If the machine has
   only one eligible disk it is ticked for you; a mirror is never assumed. The mirrored layout has
   no swap partition; the single-disk layout is unchanged and still has one.
   A disk that held a previous installation is cleared first, including its partitions, LVM
   volumes and software RAID membership. If it was one member of a RAID array, the array is
   stopped and only the selected disk is cleared; the other member disks are not written, and
   the array they still form is degraded.
   ZFS pools left on other disks are not imported when the installed host starts; to keep one,
   import it deliberately on the host with `zpool import` (and `-f` if it last ran on
   another system).
7. On **Review and install**, check the host, every NIC, VLAN, and disk. Adapter names are
   bound to MAC addresses and become `nex0`, `nex1`, etc. on the installed host; the review
   shows each mapping. A tagged interface is named, for example, `nex0.100`.
   The selected management interface becomes a port of `br0`; its host IP is on `br0`.
   Check **Erase all data on /dev/…**, which names every selected disk, then select
   **Erase disk(s) and install Nex Core** only when the targets are certain. The checkbox starts
   unchecked and resets whenever you return to Review. This is the
   destructive confirmation; do not expect another routine erase question.
8. Let installation finish. A single Nex progress panel stays in place during and between backend steps.
   The step list on the left keeps the wizard's own steps — **Welcome**, **Host**,
   **Network**, **Disk**, **Review**, then **Install** — so the position does not restart when
   installation begins. During installation the bullet follows the setting being applied: it
   marks **Network** while the network is configured and **Disk** while the disk is prepared,
   then moves to **Install** for package and bootloader work. Only the steps before the bullet
   are ticked, so **Disk** and **Review** are not ticked while **Network** is current.
   The indicator describes the current step, not an estimated overall completion percentage.
   Error and recovery prompts appear when your input is needed.
   Packages are bundled; a mirror or working network is not required.
   The installation engine can still show error or recovery questions; do not accept an
   installation error as a completed host. On a reported failure, **View log** opens
   the latest entries from `/var/log/syslog`, `/var/log/partman` and
   `/var/log/nex-installer-prepare.log`
   (up to 128 KiB each); close it to return to the error and recovery controls.
   A missing log is reported as unavailable. Inspect diagnostics before restarting. If disk identity changes before partitioning,
   installation must stop rather than silently select another disk.
9. For **Minimal install**, the console first reports `Detecting installation media, disks and
   network adapters...` while the machine is examined, then the same questions are asked in a
   keyboard-only interface: host name, keyboard layout, timezone area and timezone (no zone
   question for UTC), root password and confirmation, management adapter, then a Yes/No
   question asking whether to configure more adapters (default no), then each detected
   adapter in turn — including the management adapter — for IPv4 mode, VLAN ID, address and
   gateway. Answering yes walks every adapter in detection order; answering no leaves the
   non-management adapters unconfigured. DNS servers and the
   installation disk follow. Use
   **Go Back** to correct an earlier answer. The keyboard layout is applied to the console as
   soon as you choose it; if this console cannot load it, **Keyboard layout not applied**
   appears and the layout question is asked again, so the root password is never typed on a
   layout that was not applied. **VLAN ID** is prefilled with `0`, so pressing Enter leaves
   that adapter untagged. The management adapter list names each detected adapter with its MAC
   address and link state and ends with **Offline (no management network)**, the same choice
   the graphical wizard offers; choosing it installs without a management network, lets every
   adapter be left unconfigured and asks no gateway question. A machine with no detected adapter shows
   **No network adapter was detected**, skips the management question and installs offline.
   When more adapters are configured, each leftover adapter offers **Disabled**, **dhcp** or
   **static**; Disabled leaves that adapter unconfigured. The last screen reviews every setting, including an **Adapters:** line giving each adapter's
   mode, address, gateway and VLAN, names the disk and asks whether to erase it; it defaults to
   no and resets whenever you change the disk. The same rules apply as in the graphical wizard:
   at least eight password characters, only writable nonremovable disks of at least 16 GiB,
   never the installation media, and one VLAN per adapter with the gateway on the management
   adapter only. Answering yes checks the complete settings before anything is written: if they
   are rejected, the reason is shown, nothing is erased, and the questions return with every
   answer still filled in, so you correct the named setting and confirm the erase again. There
   are no separate partitioning or GRUB questions; accepted settings start the installation.
   The disk question is a tick list: press Space on each disk of the mirror, then Enter. Each
   choice names the device, capacity, type, model, serial and whether the disk already has partitions, in
   the same order as the graphical list, and the review names every selected disk with its
   capacity, serial and the resulting layout, including that one disk can fail without stopping
   the host.
10. In the GUI, wait for **Nex Core installation complete**, eject the ISO and choose **Restart now**
    to boot the installed disk. Apart from the installer's **Screenshot** button, **Restart now**
    is the only action on that screen: the disk has already been written, so there is nothing to
    go back to and no back button is offered.
    Minimal mode retains its native final restart prompt. Sign in as `root` at the local console using
    your chosen password. Before login, the console should show the local web UI URL once
    `nex-core-local` has a management address. Continue with [host verification](verify-nex-core.md).
    Neither path creates an ordinary user account; Debian service accounts remain.

## Expected result

The installed boot menu identifies **Nex Core GNU/Linux** and the host identifies as
`Nex Core <version>` in `/etc/os-release` and the console banner, matching the ISO version. Management networking follows your installer choices. For a qualified hostname such as
`nexcore.example.invalid`, the installed short hostname is `nexcore`; local hostname resolution
retains the full name and short alias. This does not create a DNS record on your router
or DNS server; configure that separately if other machines should resolve the name. Selecting a management NIC in the GUI creates its bridge on first boot.
Offline mode creates no management bridge. Selecting a management adapter creates `br0` in
either installation mode, with MAC-bound `nexN` interface names. The installer does not enable a firewall policy. Keep this alpha isolated until
you have validated access controls.

A mirrored system disk appears as one `md` array holding `/`. The installer installs GRUB on
every selected disk and is meant to boot from whichever member survives; when a mirror disk
fails, the host is meant to keep running on the remaining disk with its virtual machines
uninterrupted until the array is repaired. The EFI partition each disk carries is not part of
the mirror. Installs from this build mount `/boot/efi` with `nofail` and a short device timeout
so a missing ESP does not hold up a healthy mirror; that mount behaviour has not yet been
confirmed on a booted host. Older or hand-built mirrors may still need the
[emergency-mode steps](replace-a-failed-system-disk.md#troubleshooting).
Replace a failed mirror member from Nex Controller with
[Replace a failed system disk](replace-a-failed-system-disk.md). A mirror protects against one
disk failing; it is not a backup, and it does not protect the data on your virtual machines.
Boot, disk-detach and leftover-array checks for the mirrored install remain outstanding — see
Verification.

Root SSH login is disabled, so a fresh root-only installation has no SSH login account.
Use the local console. Installation can work offline; later package updates require access
to configured Debian repositories. The installed Nex package configures its default Nex APT
source; it may have no candidate until a compatible release is actually published.

## Troubleshooting

- **Windows driver media fails decompression or its checksum:** installation stops
  rather than exposing a partial driver ISO. Verify the installation ISO's checksum
  and retry using intact media; no network download is required to restore it.

- **GUI is blank or unusable:** reboot and select **Minimal install - Nex Core (text)**.
  If disk writing has already begun, do not assume reboot undoes it; inspect the target
  and retry only on the disposable disk.
- **Checksum fails:** stop and obtain or rebuild a matching ISO/sidecar pair; do not ignore it.
- **Timezone is not listed:** choose the appropriate area first, then select a nearby city
  using the same timezone rules, or choose `UTC` if appropriate.
- **Keyboard layout not applied:** this console could not load the layout you chose. Choose
  another layout when the question returns. The installation is not affected, and the root
  password is only requested once a layout has actually been applied.
- **Host settings rejected:** check the hostname, supported timezone and keyboard. Both modes
  require at least eight password characters with identical confirmation and ask again on a
  mismatch. In minimal installation the rejection names its own reason and nothing is erased;
  the questions come back with your answers still filled in, so correct only the setting named
  and confirm the erase again.
- **Invalid NIC settings:** check for duplicate addresses, valid IPv4 prefixes, VLAN range,
  and a gateway only on the enabled management adapter. Return to each NIC and correct it.
- **No adapters detected, No link or Link unknown:** verify the VM NIC attachment or physical driver support.
  Install offline if appropriate, then recover from the local console. Both modes offer
  **Offline (no management network)**, and minimal installation reports a machine with no
  adapter at all and continues offline rather than stopping. A displayed link state
  or saved address is not proof that the gateway or VLAN is reachable. Link unknown does
  not establish that the cable is unplugged; check the driver or virtual NIC attachment.
- **No root file system:** partitioning did not create the installation layout. Open
  **View log**, keep the `/var/log/partman` entries with the image checksum, and restart setup.
- **No eligible disk or disk identity changed:** check the disposable disk attachment and size,
  then restart setup. Do not substitute another disk without reviewing its identity.
  On a server with a hardware RAID controller, only disks the controller presents are
  visible: create a virtual disk for Nex Core, or switch the controller to HBA/non-RAID mode, in
  its firmware setup. Internal SD-card boot modules appear as removable media and are not
  offered; an internal boot SSD module that presents a SATA or NVMe disk is.
- **Preparation failed:** use **Retry preparation**. If it fails again, retain the error and
  installer logs with the ISO checksum before restarting or using minimal installation.
- **Installed network is unreachable:** use the local console to inspect `ip -br address`,
  `ip route` and `/etc/network/interfaces`. Match `nexN` names to their MAC addresses and
  check switch VLAN configuration. Correct the installed settings locally; the installer
  does not offer remote rollback or prove connectivity before first boot.
- **No bootable media:** check the attached ISO and VM boot order. BIOS and UEFI menu boots
  passed on the final disposable ISO; physical hardware remains unverified.
- **Installation/package setup fails:** retain the installer error and logs with the image
  checksum for diagnosis. Do not treat the base Debian installation alone as a successful
  Nex installation. Retry on a new disposable disk after resolving the error.
- **Installer starts again after completion:** eject the ISO and select the installed disk.
- **Wrong disk selected before writing:** cancel and correct the VM's attached storage.
  There is no Nex undo for an accepted disk overwrite.

## Verification

The packaging efficiency checks and disposable offline-installation results are
tracked in [packaging verification](../qa/2026-09-18-iso-packaging-efficiency.md).

BMC/IPMI-sized layout (Disk step wrap, no sideways page scroll, denser chrome at
≤1024×768) is covered by `tests/integration/installer-disk.py` and related installer
harnesses in the working tree; a native QEMU capture at 800×600 / 1024×768 against a
rebuilt ISO that includes this change remains outstanding.

Mirrored system-disk installs (two or more disks): both wizards' Disk steps and two-disk
installs have reached the base system in the lab, but no mirrored host has been booted,
detached or checked for leftover arrays, and the current three-partition GPT layout has not
had a completed install+boot walkthrough. Outstanding checks are listed in
[installer mirror QA](../qa/iso/2026-09-22-installer-mirror.md).

The [0.3.2 local artifact lab](../qa/2026-09-18-release-artifact-lab.md) verified
its recorded ISO with BIOS/KVM and UEFI/TCG menu review, an automated offline
BIOS installation, media-free first boot and reboot. This was serial automation,
not a new walkthrough of the graphical steps. The NIC-less lab explicitly set
loopback management for Core service checks; real management networking, UEFI
installation, multiple-disk safety and failure recovery remain unverified on that
snapshot. Later storage changes were not included.

The [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md) (`0.2.10~gate5`, 2026-09-17,
disposable nested-KVM lab with SeaBIOS, screen captured every 4 s) walked the graphical steps 1–8
and 10: checksum, boot menu, host and network pages, the only target disk, the unconfirmed-erase
refusal, progress, completion and the installed console, with the installed package, version and
files checked against the build. Step 8 matched apart from one open defect: twice between backend
steps, once for about 4 s and once for about 20–24 s, the step list showed "Preparing the next
installation step…" with no current step and no ticks before returning to the right step (the
gate4 run saw 4–8 s). UEFI host installation was not exercised in that run, and the minimal
text installer predates its current wizard: that run walked the Debian questions this mode no
longer asks.

The text wizard's question order, **Go Back** navigation, device filtering and the settings
handoff it writes are covered by `tests/integration/installer-text-wizard.py`, which drives the
wizard against a fixture machine and decodes the handoff the way `nex-config` reads it. As of
2026-09-22 it also covers the default **Configure more network adapters?** skip (BUG-0444),
walking leftovers when that answer is yes, and configuring a management adapter that is not the
first detected NIC. As of 2026-09-21 it covers the untagged VLAN default, the keyboard layout
that cannot be applied and is asked again before the password, the offline choice, a machine
with no adapter, the per-adapter review line, and a rejected settings check that keeps every
answer. That is a simulated source-level check: the questions are answered by a script against a
stand-in for the installer's question system, with stand-ins for the keyboard and validation
tools, so real console rendering, real keyboard loading, real disk hardware, a booted ISO
installation through the text interface and its installed networking all remain unverified.

The [fresh local installation](../qa/fresh-install/checks.md) covers a graphical BIOS
installation onto a blank disk through Controller deployment. Installer details are recorded in
the [Nex wizard](../qa/iso/2026-09-08-nex-wizard.md), [usability](../qa/iso/2026-09-08-installer-qol.md),
[timezone](../qa/iso/2026-09-08-timezone-dropdown.md), [hostname](../qa/iso/2026-09-08-installer-hostname.md),
[link and erase confirmation](../qa/iso/2026-09-08-installer-link.md),
[Xen disk](../qa/iso/2026-09-08-installer-xen-disk.md), [keyboard notice](../qa/iso/2026-09-08-keyboard-notice.md),
[progress](../qa/iso/2026-09-08-installer-progress.md), [continuous progress](../qa/iso/2026-09-08-continuous-progress.md),
[management bridge](../qa/iso/2026-09-08-management-bridge.md) and
[DHCP preview](../qa/iso/2026-09-09-installer-dhcp.md) reports. Historical reports:
[alpha1](../qa/iso/2026-09-08-alpha1.md), [root-only accounts](../qa/iso/2026-09-08-root-account.md),
[installer modes](../qa/iso/2026-09-08-installer-modes.md) and
[previous custom shell](../qa/iso/2026-09-08-custom-installer.md).

The final ISO passed BIOS/UEFI menu boots, offline installation, installed first boot and reboot
in a disposable nested-KVM lab. Physical NICs, switch VLANs, Xen installation and physical
hardware remain unverified.

An earlier 0.2.8 build with unchanged installer code reached the final prompt, stayed open until
**Restart now**, then rebooted naturally. The final SHA's unattended install, first boot and reboot
passed; its completion screen was not visually inspected. See [final ISO and Controller acceptance](../qa/2026-09-16-final-iso-controller-acceptance.md).

[Return to the guide](README.md)
