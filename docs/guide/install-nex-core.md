# Install Nex Core in a disposable VM

## Status

Local lab alpha of the Nex Core installer. The ISO is named after the Nex Core version it
installs (`nex-core-<version>-amd64.iso`); its boot menu offers the Nex wizard as a graphical
or a text interface (English, five keyboard layouts, IPv4). Both ask the same questions and
install the same host; only the presentation differs. Installed hosts
include the host-local web UI, from which Nex Controller can be deployed. Not certified for
production or physical hardware.

Nex Core is a distribution, not a package set added to Debian. The installer writes a signed, read-only system
image whole, to two slots on every selected disk; settings, virtual machines and downloaded payloads live on a
separate `nex-data` volume. A host is updated by replacing the image, not by installing packages; see
[software updates](software-updates.md). The installer supports both UEFI and BIOS firmware.

The full ISO carries the signed system image, the Controller appliance and the complete Windows guest-driver media
for offline installation. The development network variant, `nex-core-<version>-amd64-netinst.iso`, keeps the
bootable installer but downloads the system image, the Controller appliance and the driver media by SHA-256 from the
addresses set when the installer was built. It needs internet access throughout installation; its completed
installation provides the same Core software and driver ISO.

The installer verifies the system image and every payload against checksums pinned in the media before it writes a
disk (for netinst, the system image is checked before any disk is written; payloads are checked once `nex-data`
exists, see below). A Nex-hosted Debian mirror is not needed: the image is already assembled.

## Disk layout and boot (development, simulated install only)

- **Firmware.** The installer detects whether it was started under UEFI or BIOS and installs the matching boot
  method; the system image, slots, boot menu and update logic are the same. On UEFI, a firmware boot entry on every
  selected disk starts shim and Debian's signed GRUB from the boot partition; on BIOS, GRUB starts from the MBR of every
  selected disk. Either way GRUB reads a per-slot entry from the boot partition and starts the image. GRUB cannot
  authenticate the kernel command line, and BIOS has no Secure Boot, so the root hash is not signature-protected
  before the kernel runs (with Secure Boot on, shim still checks GRUB and the kernel); dm-verity still checks every block of the root when it is read, and
  the release signature still covers the image and every update. A 1 MiB BIOS boot partition on every disk makes the
  same layout bootable under either firmware.
- **24 GiB minimum** per disk: two slots, a boot partition, and a data volume that holds a
  staged update, the Controller appliance and VirtIO driver payloads, and the default VM store.
- **Layout.** Every selected disk gets the same partition table: a 1 MiB BIOS boot partition, a 1 GiB boot partition,
  two system slots with their integrity partitions, and `nex-data`. Two or more disks mirror only `nex-data` (md RAID1);
  each disk can boot alone. There is no swap partition.
- **Erase confirmation:** the wizard lists every disk and an explicit yes erases it. On the offline ISO
  nothing is erased until the system image and the media payloads have passed their checksum checks; a damaged image
  or payload stops the install with no disk changed. The network installer checks the downloaded system image the same
  way, but can check its payloads only after the disks are written and `nex-data` exists; a payload that fails then
  leaves an installed host without the Controller appliance or VirtIO payloads, and the installer says so. Only arrays
  on the selected disks are stopped (for Repair as well, and an array that cannot be stopped stops the run before
  any write); no other array is assembled.
- **Payloads.** The Controller appliance and the VirtIO driver ISO are installed under
  `/var/lib/nex/payload/<sha256>/` on the data volume, not inside the system slot. The offline ISO carries
  them; the network installer downloads them, and the system image, by SHA-256 from the address set when the
  installer was built.
- **Repair image** keeps the data volume: see [Reinstall the system image and keep the data](reinstall-keeping-state.md).
- **Offline whole disks only.** The disk writer rejects partitions, duplicate names for the same
  disk, mounted disks or partitions, active swap and disks held by md/device-mapper. Boot the
  installer and stop only arrays belonging to your selected targets before proceeding; never
  install or repair onto the running host's disk. Device aliases resolve to the same kernel
  disk, not a separate target. A verified image that exceeds a system slot is refused before
  wiping or formatting.

Secure Boot is supported on UEFI but not required: with it off the host works, and its image checks are not
tamper-proof against someone with disk access. Verification status: small lab ISOs of this layout were installed
and booted **in virtual machines only** (virtual UEFI, on 2026-10-02): one disk and a two-disk mirror (the host
boots with both disks and with either one alone), root's password opened a root shell on the serial console, over
SSH and through the host's local web API, a third disk that was not selected was left byte-identical, an install onto
a disk of a mounted array was refused before any disk byte changed, and an install cut off at two points ran again to
completion (twice); a virtual Secure Boot, virtual TPM and update run is recorded too. See
[Phase I-6 lab 1](../qa/image/phase6-lab1.md) and
[lab 2](../qa/image/phase6-lab2.md) for exactly what ran, what did not (a full ISO build, the net installer, the real
interactive wizards, a BIOS boot) and what is not established (any physical hardware, firmware, Secure Boot or TPM). The graphical
wizard now draws the Repair image choice (checked under a virtual display only).

## Prerequisites

- A locally built ISO and its matching SHA256 sidecar; see [build instructions](../../installer/README.md).
  External redistribution remains subject to the [release licensing gates](../LICENSING.md).
  The current working `0.4.19` tree is not a released artifact.
- An isolated disposable amd64 VM with at least 2 virtual CPUs, 4 GB RAM, and one blank 24 GB
  virtual disk. Do not attach real host disks or existing VM data disks.
- Hardware virtualization exposed to the VM if you intend to run nested KVM guests.
- For netinst: a working management NIC, gateway and DNS with HTTPS access to the
  configured Nex release host, `snapshot.debian.org` and `fedorapeople.org`, including
  their download redirects. Use a current matching ISO while its release payloads remain
  available; older netinst ISOs can stop working when hosted payloads are pruned.
- VM console access. The virtual-UEFI lab runs are recorded under Verification; installation and boot under BIOS
  firmware have not been walked through for this page.

## Steps

1. From the repository's `dist` directory, verify the image before attaching it:

   ```sh
   sha256sum -c nex-core-<version>-amd64.iso.sha256
   ```

   Replace `<version>` with the version in the built file name. For netinst, use
   `sha256sum -c nex-core-<version>-amd64-netinst.iso.sha256` instead.

   Continue only if the matching ISO reports `OK`. A checksum detects a mismatched file;
   obtain the image and checksum from a trusted build.
2. Attach that ISO to the disposable VM and boot it. Use the arrow keys and Enter in the branded menu to select
   **GUI install - Nex Core** (mouse and keyboard) or **Minimal install - Nex Core (text)**
   (keyboard only, for a machine whose graphics do not work). The menu waits for your choice.
   Both run the Nex wizard, collect the same settings and install the same headless hypervisor;
   neither installs a desktop. The serial option uses `ttyS0` at 115200 baud and runs the same text wizard on the
   serial console, for a machine you reach through a serial line or a BMC.
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
   Enter a nonempty root password and confirm it.
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
   available for the full ISO; leave every adapter disabled for a fully offline host.
   For netinst, select an enabled management NIC: offline settings are rejected before
   disk preparation. The network variant applies management networking during installer
   preparation so it can check and fetch downloads. The installed host still uses the
   saved settings on first boot; its DHCP lease may differ.
6. On **Choose the installation disks**, explicitly select the **blank disposable disk**, or
   two or more of them for a mirrored data volume.
   The GUI supports writable, nonremovable whole SATA/SCSI, virtio, Xen or NVMe disks of at
   least 24 GiB (`sd…`, `vd…`, `xvd…`, `nvme…`). Installation media and partitions are not eligible.
   Every selected disk is erased and receives the same partition table (see
   [Disk layout and boot](#disk-layout-and-boot-development-simulated-install-only)). One disk gets a single `nex-data`
   volume. Two or more disks hold `nex-data` as a Linux software RAID1 mirror (`md`): each selected disk holds an
   identical copy (sizes are not combined) and keeps its own boot partition and system slots, so any of them can
   start the host on either UEFI or BIOS firmware. Every mirror disk must be within 10% of the smallest selected disk's
   capacity, because usable size is that of the smallest member; a mismatched selection is
   refused with that reason.
   The list is a tick list in both installers: click the **Use** box, or move to a disk and press
   Space. Each row names the device, capacity, type (SSD, HDD or NVMe), model, serial and whether
   the disk already **Has partitions**, so you can tell which disk holds old data. The line under
   the list says what your ticks will produce - `What you get: one system disk, no redundancy` or
   `What you get: one 128.0 GiB system disk on all 2 drives (RAID1 copies — not pooled storage). Survives 1 disk failure` -
   and turns red with the reason if a ticked disk is too far from the smallest one. More disks
   only add more copies of that same data volume; they do not combine capacity or change the RAID
   level. Disks under 24 GiB are not listed.
   If the machine has
   only one eligible disk it is ticked for you; a mirror is never assumed.
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
   Netinst then activates the selected management network and checks download sources
   before erasing disks. If preparation fails, correct the connection/settings and retry;
   a successful preflight does not guarantee the network will stay available.
8. Let installation finish. A single Nex progress panel stays in place during and between backend steps.
   The step list on the left keeps the wizard's own steps — **Welcome**, **Host**,
   **Network**, **Disk**, **Review**, then **Install** — so the position does not restart when
   installation begins. During installation the bullet follows the setting being applied: it
   marks **Network** while the network is configured and **Disk** while the disk is prepared,
   then moves to **Install** while the system image and boot files are written. Only the steps before the bullet
   are ticked, so **Disk** and **Review** are not ticked while **Network** is current.
   **Installing system software** is the step that writes the system image; it does not ask you to choose software.
   The indicator describes the current step, not an estimated overall completion percentage.
   Under the progress bar, **Still working…** keeps moving and an **Installer activity** log
   scrolls the latest entries from `/var/log/syslog`, `/var/log/partman` and
   `/var/log/nex-installer-prepare.log`, so a long quiet step such as finishing preseed still
   shows that work is continuing. Scroll the log up to pause following new lines; leave it at
   the bottom to keep following.
   Error and recovery prompts appear when your input is needed.
   With the full ISO, the system image and payloads are on the media and a working network is not required.
   With netinst, keep networking available while the system image, Controller appliance and driver media download.
   Verified completed downloads can be reused on a retry within the current installation;
   a failed or corrupt download is discarded.
   The installation engine can still show error or recovery questions; do not accept an
   installation error as a completed host. If a required backend step returns to Debian's
   component selection, the graphical installer stops on a Nex error screen with the
   activity log visible. It does not allow later steps to be selected. Restart setup
   after correcting the cause. On other reported failures, **View log** opens
   the latest entries from the same three logs
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
   address and link state. On the full ISO, the list ends with **Offline (no management network)**,
   the same choice the graphical wizard offers; choosing it installs without a management network, lets every
   adapter be left unconfigured and asks no gateway question. A machine with no detected adapter shows
   **No network adapter was detected**, skips the management question and installs offline
   on the full ISO. Netinst omits the text Offline choice and stops if no adapter is detected.
   When more adapters are configured, each leftover adapter offers **Disabled**, **dhcp** or
   **static**; Disabled leaves that adapter unconfigured. The last screen reviews every setting, including an **Adapters:** line giving each adapter's
   mode, address, gateway and VLAN, names the disk and asks whether to erase it; it defaults to
   no and resets whenever you change the disk. The same rules apply as in the graphical wizard:
   a nonempty root password, only writable nonremovable disks of at least 24 GiB,
   never the installation media, and one VLAN per adapter with the gateway on the management
   adapter only. Answering yes checks the complete settings before anything is written: if they
   are rejected, the reason is shown, nothing is erased, and the questions return with every
   answer still filled in, so you correct the named setting and confirm the erase again. There
   are no separate partitioning or GRUB questions; accepted settings start the installation.
   Netinst requires an enabled management adapter and successful download preflight;
   the full ISO alone supports offline installation.
   The disk question is a tick list: press Space on each disk of the mirror, then Enter. Each
   choice names the device, capacity, type, model, serial and whether the disk already has partitions, in
   the same order as the graphical list, and the review names every selected disk with its
   capacity, serial and the resulting layout (a mirrored `nex-data` volume when two or more
   disks are selected, and how many disk failures that selection can survive).
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

A mirrored install keeps `nex-data` on a Linux software RAID1 array (`md`) across the selected disks, while every disk
carries its own boot partition and both system slots. The host is meant to boot from whichever member survives; when
a mirror disk fails, the host is meant to keep running on the remaining disk with its virtual machines
uninterrupted until the array is repaired. The boot partition each disk carries is not part of the mirror. With *n*
mirror disks the host can survive *n*−1 disk failures; it is not a backup, and it does not protect the data on your
virtual machines. Boot, disk-detach and leftover-array checks for the mirrored install remain outstanding — see
Verification.

The installed root file system (`/`) is the read-only, integrity-checked system image. Everything the host changes while
it runs lives under `/var`, `/etc` and `/run`: `/etc` is an overlay whose changes are kept on `nex-data`
(`/var/lib/nex/etc`), so configuration, SSH host keys and root's files survive reboots and updates.
Software is replaced by updating the image, not by installing packages; see [Software updates](software-updates.md).

Fresh installs allow root password SSH on the management network. Check the installed
host key fingerprint at its trusted console with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`,
then compare it with the key shown by your SSH client before entering the installer root password.
For example, connect with `ssh root@192.0.2.5` from the private management network or VPN.
Full-ISO installation can work offline; later updates need the host's update source (see
[software updates](software-updates.md)), which the installer does not set up.

## Troubleshooting

- **Windows driver media fails decompression or its checksum:** installation stops
  rather than exposing a partial driver ISO. Verify the installation ISO's checksum
  and retry using intact media. The full ISO restores it locally; netinst downloads
  the checksum-pinned ISO directly from Fedora.

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
  require a nonempty root password with identical confirmation and ask again on a
  mismatch. In minimal installation the rejection names its own reason and nothing is erased;
  the questions come back with your answers still filled in, so correct only the setting named
  and confirm the erase again.
- **Invalid NIC settings:** check for duplicate addresses, valid IPv4 prefixes, VLAN range,
  and a gateway only on the enabled management adapter. Return to each NIC and correct it.
- **No adapters detected, No link or Link unknown:** verify the VM NIC attachment or physical driver support.
  Use the full ISO to install offline if appropriate, then recover from the local console.
  Its graphical and text modes offer **Offline (no management network)**, and minimal
  installation reports a machine with no
  adapter at all and continues offline rather than stopping on the full ISO.
  Netinst requires an enabled management adapter. A displayed link state
  or saved address is not proof that the gateway or VLAN is reachable. Link unknown does
  not establish that the cable is unplugged; check the driver or virtual NIC attachment.
- **The system image could not be written:** the installer stops with the reason. Open
  **View log**, keep the entries with the ISO checksum, and restart setup on a disposable disk.
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
  does not offer remote rollback. Netinst download access does not prove the installed
  management bridge will be reachable after reboot.
- **No bootable media:** check the attached ISO and VM boot order, and that the VM's firmware (UEFI or BIOS) is set to
  boot it; physical hardware remains unverified.
- **Netinst download unavailable or verification fails:** check gateway, DNS, VLAN,
  HTTPS access and the installer log. Preparation checks download availability before
  disk preparation, but an upstream outage can still occur later. Downloads retry up
  to three times, resuming interrupted transfers when the source supports it; a complete
  download that fails its checksum is discarded. Use the current matching ISO
  if its release payloads were removed or replaced. Do not bypass checksum checks.
  Correct the connection and use the installer's available retry controls, or restart
  on a disposable target. A restart after disk writing is not a rollback. Use the full
  ISO when internet access or release-payload availability cannot be guaranteed.
- **Installation fails:** retain the installer error and logs with the ISO
  checksum for diagnosis. Retry on a new disposable disk after resolving the error.
- **Netinst stops before partitioning:** check that the management network still has DNS
  and can reach the release sources. The partition check stops before disk writes if a
  source is unavailable. Restart setup after correcting the connection, or use the full
  ISO when reliable internet access is unavailable.
- **Installer starts again after completion:** eject the ISO and select the installed disk.
- **Wrong disk selected before writing:** cancel and correct the VM's attached storage.
  There is no Nex undo for an accepted disk overwrite.

## Verification

Installation of the image layout was exercised only in virtual-machine labs, recorded in
[Phase I-6 lab 1](../qa/image/phase6-lab1.md) and [lab 2](../qa/image/phase6-lab2.md) (virtual UEFI; the details are
under Disk layout and boot above). BIOS firmware installs, a physical machine, and Secure Boot or TPM on
hardware are not verified.

BMC/IPMI-sized layout (Disk step wrap, no sideways page scroll, denser chrome at
≤1024×768) is covered by `tests/integration/installer-disk.py` and related installer
harnesses in the working tree; a native QEMU capture at 800×600 / 1024×768 against a
rebuilt ISO that includes this change remains outstanding.

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

Installer details are recorded in
the [Nex wizard](../qa/iso/2026-09-08-nex-wizard.md), [usability](../qa/iso/2026-09-08-installer-qol.md),
[timezone](../qa/iso/2026-09-08-timezone-dropdown.md), [hostname](../qa/iso/2026-09-08-installer-hostname.md),
[link and erase confirmation](../qa/iso/2026-09-08-installer-link.md),
[Xen disk](../qa/iso/2026-09-08-installer-xen-disk.md), [keyboard notice](../qa/iso/2026-09-08-keyboard-notice.md),
[progress](../qa/iso/2026-09-08-installer-progress.md), [continuous progress](../qa/iso/2026-09-08-continuous-progress.md),
[management bridge](../qa/iso/2026-09-08-management-bridge.md) and
[DHCP preview](../qa/iso/2026-09-09-installer-dhcp.md) reports.

[Review-fix checks](../qa/2026-10-03-immutable-review-fixes.md) exercised all-target
offline preflight with controlled block observations, actual scratch-file alias refusal, gzip
integrity rejection and text-wizard Erase/Repair navigation through a simulated debconf frontend.
These checks do not replace a new booted ISO, physical disk/network or firmware walkthrough.

[Return to the guide](README.md)
