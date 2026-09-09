# Install Nex Core in a disposable VM

## Status

Applies to the current **Nex Core installer source**. Local lab alpha; the ISO file is named
after the single Nex Core version it installs (`nex-core-<version>-amd64.iso`). The
boot menu offers graphical and text installation. Installed hosts include the host-local
web UI; the console login banner shows its URL after network is up. Central Controller is
still deployed from that UI or separately. The graphical path starts with Nex's own host,
network, disk and review pages; minimal installation retains Debian's text workflow. The
graphical UI is English, supports five keyboard layouts and IPv4 configuration. This is not
production or physical-hardware installation certification. Current verification and remaining
limits are recorded below.
[Return to the guide](README.md).

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
   sha256sum -c nex-core-<version>-amd64.iso.sha256
   ```

   Replace `<version>` with the version in the built file name.

   Continue only if the matching ISO reports `OK`. A checksum detects a mismatched file;
   obtain the image and checksum from a trusted build.
2. Attach that ISO to the disposable VM and boot it. Use the arrow keys and Enter in the branded menu to select
   **GUI install - Nex Core** (custom Nex wizard) or **Minimal install - Nex Core (text)**
   (simple text interface). The menu waits for your choice. Both install the same
   headless hypervisor; neither installs a desktop. The serial option uses `ttyS0` at 115200 baud.
3. For **GUI install**, wait for **Begin setup** on **Welcome to Nex Core**. The wizard
   introduces Nex Core and its local web UI; deploy Nex Controller from that UI or
   separately for central management. The wizard fills the available installer screen and follows display-resolution changes. The left
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
   before installation starts. Pages scroll when needed while navigation stays visible;
   field tooltips provide additional input guidance.
4. On **Connect your host**, you can also edit **Hostname**. It is the same value as on
   the Host page; edits in either place appear in the other and in the final review. Use
   a short name such as `nexcore` or a qualified name such as `nexcore.matthome`.
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
6. On **Choose the installation disk**, explicitly select the **blank disposable disk**.
   The GUI supports one writable, nonremovable whole SATA/SCSI, virtio, Xen or NVMe disk of at
   least 16 GiB (`sd…`, `vd…`, `xvd…`, `nvme…`). Installation media and partitions are not eligible.
   This path erases the selected disk and installs a whole-disk ext4 layout.
7. On **Review and install**, check the host, every NIC, VLAN, and disk. Adapter names are
   bound to MAC addresses and become `nex0`, `nex1`, etc. on the installed host; the review
   shows each mapping. A tagged interface is named, for example, `nex0.100`.
   The selected management interface becomes a port of `br0`; its host IP is on `br0`.
   Check **Erase all data on /dev/…**, which names the selected disk, then select
   **Erase disk and install Nex Core** only when the target is certain. The checkbox starts
   unchecked and resets whenever you return to Review. This is the
   destructive confirmation; do not expect another routine erase question.
8. Let installation finish. A single Nex progress panel stays in place during and between backend steps.
   The indicator describes the current step, not an estimated overall completion percentage.
   Error and recovery prompts appear when your input is needed.
   Packages are bundled; a mirror or working network is not required.
   The installation engine can still show error or recovery questions; do not accept an
   installation error as a completed host. On a reported failure, **View log** opens
   the latest entries from `/var/log/syslog` and `/var/log/nex-installer-prepare.log`
   (up to 128 KiB each); close it to return to the error and recovery controls.
   A missing log is reported as unavailable. Inspect diagnostics before restarting. If disk identity changes before partitioning,
   installation must stop rather than silently select another disk.
9. For **Minimal install**, follow the text questions for language, networking, hostname,
   root password and confirmation, disk partitioning, write confirmation and GRUB target.
   Select the same disposable OS disk for GRUB. This is the advanced/manual alternative;
   the graphical per-NIC editor and its whole-disk confirmation do not apply to this path.
10. In the GUI, wait for **Nex Core installation complete**, eject the ISO and choose **Restart now**
    to boot the installed disk. Minimal mode retains its native final restart prompt. Sign in as `root` at the local console using
    your chosen password. Before login, the console should show the local web UI URL once
    `nex-core-local` has a management address. Continue with [host verification](verify-nex-core.md).
    Neither path creates an ordinary user account; Debian service accounts remain.

## Expected result

The installed boot menu identifies **Nex Core GNU/Linux** and the host identifies as
`Nex Core <version>` in `/etc/os-release` and the console banner, matching the ISO version. Management networking follows your installer choices. For a qualified hostname such as
`nexcore.matthome`, the installed short hostname is `nexcore`; local hostname resolution
retains the full name and short alias. This does not create a DNS record on your router
or DNS server; configure that separately if other machines should resolve the name. Selecting a management NIC in the GUI creates its bridge on first boot.
Offline mode creates no management bridge; the minimal text installer retains its native
network configuration. The installer does not enable a firewall policy. Keep this alpha isolated until
you have validated access controls.

Root SSH login is disabled, so a fresh root-only installation has no SSH login account.
Use the local console. Installation can work offline; later package updates require access
to configured Debian repositories. No Nex package repository is configured yet.

## Troubleshooting

- **GUI is blank or unusable:** reboot and select **Minimal install - Nex Core (text)**.
  If disk writing has already begun, do not assume reboot undoes it; inspect the target
  and retry only on the disposable disk.
- **Checksum fails:** stop and obtain or rebuild a matching ISO/sidecar pair; do not ignore it.
- **Timezone is not listed:** choose the appropriate area first, then select a nearby city
  using the same timezone rules, or choose `UTC` if appropriate.
- **Host settings rejected:** check the hostname, supported timezone and keyboard. The GUI
  requires at least eight password characters with identical confirmation; minimal installation
  rejects a blank password and asks again on a mismatch.
- **Invalid NIC settings:** check for duplicate addresses, valid IPv4 prefixes, VLAN range,
  and a gateway only on the enabled management adapter. Return to each NIC and correct it.
- **No adapters detected, No link or Link unknown:** verify the VM NIC attachment or physical driver support.
  Install offline if appropriate, then recover from the local console. A displayed link state
  or saved address is not proof that the gateway or VLAN is reachable. Link unknown does
  not establish that the cable is unplugged; check the driver or virtual NIC attachment.
- **No eligible disk or disk identity changed:** check the disposable disk attachment and size,
  then restart setup. Do not substitute another disk without reviewing its identity.
- **Preparation failed:** use **Retry preparation**. If it fails again, retain the error and
  installer logs with the ISO checksum before restarting or using minimal installation.
- **Installed network is unreachable:** use the local console to inspect `ip -br address`,
  `ip route` and `/etc/network/interfaces`. Match `nexN` names to their MAC addresses and
  check switch VLAN configuration. Correct the installed settings locally; the installer
  does not offer remote rollback or prove connectivity before first boot.
- **No bootable media:** check the attached ISO and VM boot order. Try the tested BIOS path
  on a fresh disposable disk. UEFI installation and Secure Boot remain unverified.
- **Installation/package setup fails:** retain the installer error and logs with the image
  checksum for diagnosis. Do not treat the base Debian installation alone as a successful
  Nex installation. Retry on a new disposable disk after resolving the error.
- **Installer starts again after completion:** eject the ISO and select the installed disk.
- **Wrong disk selected before writing:** cancel and correct the VM's attached storage.
  There is no Nex undo for an accepted disk overwrite.

## Verification

The [fresh local installation](../qa/fresh-install/checks.md) used the ISO containing
Core/Controller `0.1.0+managed1`: actual graphical installation onto a blank disk,
explicit erase confirmation, corrected logo, disk-only BIOS boot, automatic local
HTTPS access and deployment of the bundled Controller. Artifact hashes and lab
networking are recorded there; this does not establish physical-hardware coverage.

The [Nex wizard QA report](../qa/iso/2026-09-08-nex-wizard.md) records the current
artifact, actual custom-page and network checks, inspected captures, and untested limits.
The [installer usability QA report](../qa/iso/2026-09-08-installer-qol.md) separately records
the revised layout, fullscreen behavior, the earlier timezone search and keyboard/password control checks
for the usability revision.
Do not infer physical NIC, switch VLAN, full interactive installation or UEFI/Secure Boot
coverage from a simulated or partial walkthrough.

Earlier evidence remains historical: [alpha1](../qa/iso/2026-09-08-alpha1.md),
[root-only accounts](../qa/iso/2026-09-08-root-account.md),
[GUI/minimal choices](../qa/iso/2026-09-08-installer-modes.md), and the
[previous custom shell](../qa/iso/2026-09-08-custom-installer.md).
Those reports do not verify the new upfront wizard or its per-NIC configuration.

The [timezone dropdown check](../qa/iso/2026-09-08-timezone-dropdown.md) covers the
current selection-only controls and removal of the keyboard-test field.

[Hostname validation QA](../qa/iso/2026-09-08-installer-hostname.md) records standards,
boundary checks and qualified-name installation configuration evidence.

[Link and erase-confirmation QA](../qa/iso/2026-09-08-installer-link.md) records live
carrier changes and the current checkbox confirmation workflow.

[Xen disk and link indicator QA](../qa/iso/2026-09-08-installer-xen-disk.md) records
simulated Xen target validation and native indicator review; a Xen installation remains unverified.

[Quiet keyboard startup QA](../qa/iso/2026-09-08-keyboard-notice.md) records the silent Welcome screen and retained failure handling.

[Progress, completion and failure QA](../qa/iso/2026-09-08-installer-progress.md) records
native transition/log-viewer checks and the separately identified completed-installation test.

[Continuous progress QA](../qa/iso/2026-09-08-continuous-progress.md) records the correction
from alternating layouts to one persistent progress panel.

[Management bridge QA](../qa/iso/2026-09-08-management-bridge.md) records generated
configuration and disposable native-network checks for the selected management NIC.

[Installer DHCP preview QA](../qa/iso/2026-09-09-installer-dhcp.md) records native lease,
cleanup and GUI-state checks for the selected adapter.
