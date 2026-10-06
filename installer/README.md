# Nex Core installation media

Build a local amd64 development ISO from Debian 13.6.0 netinst, preserving its
signed boot binaries and hybrid BIOS/UEFI layout. Debian's installer environment
(kernel, initrd, signed boot components) only boots the Nex wizard and
`nex-image-install.sh`; the host itself is the signed, read-only Nex Core system image
carried on the media. ISO metadata and `README-NEX.txt` identify modified Nex media.
Nex source is AGPL-3.0-only; see [licensing](../docs/LICENSING.md) before distributing an image.

## Build

On an amd64 Linux machine with Docker and network access, a built system image
release directory and the static `nex-hostimage` that installs it:

```sh
NEX_IMAGE_DIR=<image release dir> NEX_HOSTIMAGE_BIN=<static nex-hostimage> ./installer/build.sh
```

`NEX_IMAGE_DIR` is the output of `image/build.sh` (`root.erofs`, `root.verity`, `roothash`,
the signed boot image and `manifest.json`), relative to the repository. The build runs
`nex-hostimage verify` on it before it is put on the media. Both variables are required.

Every ISO build always rebuilds from the current tree:

1. Controller system image and corresponding source archive (`appliance/build.py`, which
   regenerates `dist/nex-source.tar.gz` through `scripts/build-source.py`)
2. The ISO itself: the verified image set, the static `nex-hostimage`, `nex-image-install.sh`,
   and the payloads that live on `nex-data` rather than in the system image (the Controller
   system image, `controller-system.img`, and the gzip-compressed VirtIO driver ISO, each in
   `nex/payload/<sha256>/`)

By default the build derives `NEX_VERSION` from the newest numbered `CHANGELOG.md` heading.
Override it only for a deliberate local build; Debian pre-release versions use `~` so the later
final version sorts above them.

The builder is pinned to a Debian container digest. It verifies the base ISO against a pinned
SHA256 and Debian's signed checksum list using the documented CD-signing fingerprint, and
fetches the exact cdebconf 0.280 source for the custom frontend from immutable dated
`trixie` snapshots. The package set of the host is the system image's own and is built by
`image/build.sh`; this builder installs no packages.

Windows guest-driver media is carried as gzip-compressed transport and restored
to its original checksum-verified ISO by the installer, without network access.
The installation media retains the intact matching source RPM and notices; its
redundant extracted contents are retained in the separate release source bundle.
Packaging regression checks run in both CI providers; measured size and disposable
installation evidence belong in the
[packaging QA report](../docs/qa/2026-09-18-iso-packaging-efficiency.md).

Output: `dist/nex-core-<version>-amd64.iso`, its SHA256 file, compliance manifest, source
bundle and build record. Cache/extracted media: `.cache/nex-iso/`. Exact Nex recipe source,
license notices and source checksum are included on the ISO.
The custom graphical frontend is built from authenticated cdebconf 0.280 source;
its exact modified source/build configuration and upstream notices ship alongside Nex source.
The source bundle does not yet cover every original installer component;
this and matching upstream source retention are release gates, not completed claims.

This recipe is repeatable but **not yet bit-reproducible**: build timestamps change.
Retain the output, manifest and cache for each tested build. The dated snapshots are already
pinned; a complete source bundle remains a release requirement.

The current working version is `0.4.18`; it is not a release merely because the build
resolves that changelog version. External redistribution follows
[licensing](../docs/LICENSING.md).

## Network installer

Set `NEX_NETINST_BASE_URL` to the HTTPS directory where this release's Nex payloads
will be published, then run the same builder:

```sh
NEX_NETINST_BASE_URL=https://downloads.example.org/releases/0.4.18 \
  NEX_IMAGE_DIR=<image release dir> NEX_HOSTIMAGE_BIN=<static nex-hostimage> ./installer/build.sh
```

The build creates both the full ISO and
`dist/nex-core-<version>-amd64-netinst.iso`, with its checksum and separate
`nex-core-<version>-netinst-compliance-manifest.json`. Publish the content-addressed files
under `dist/netinst/` (the image release set and the Controller system image, each named
`<sha256>-<file>`) directly beneath the configured URL. No Debian mirror is needed. The shared
full source bundle remains required for redistribution; keep it alongside both installation
variants.

The network ISO keeps Debian's boot and installer environment. It removes the system image,
the bundled Controller system image, the VirtIO ISO and redundant retained third-party source
payloads, and downloads them by SHA-256 from `nex/payloads.tsv`: the image set and Controller
system image from the Nex release host, Windows driver media directly from Fedora. Rows are
`image/<file>` (the release set, each file keeping its path) and
`payload/<sha256>/<file>` (Controller system image, VirtIO ISO). `netinst-fetch.sh` accepts no
other destination, requires HTTPS, and installs only bytes that match the pinned SHA-256.

Both Nex frontends apply the selected management network during net installation;
serial installation uses the same text wizard. Before any disk is touched, installation
checks the system image and payload URLs (one file per Nex blob and one per upstream host).
This checks availability at that moment, not uninterrupted access for the whole installation.
A missing network or unpublished payload stops the installation before partitioning. Later
download failures stop installation with an error; each download gets three attempts
and only verified bytes are installed.

Published assets must remain available for the lifetime of their ISO. The current
release retention policy can remove old releases; use the current matching ISO if
an older release's payloads disappear. Local generation does not publish payloads
or establish an installed-host acceptance result.

## Boot menu and firmware

The media boots on BIOS and UEFI firmware from the same four entries: isolinux
(`isolinux/menu.cfg`) on BIOS and GRUB (`boot/grub/grub.cfg`) on UEFI.

| Entry | What it boots |
|---|---|
| **GUI install - Nex Core** | Nex initrd; the Nex GTK wizard |
| **Minimal install - Nex Core (text)** | The same initrd with `DEBIAN_FRONTEND=newt`: the same wizard as text, no X server |
| **Install Nex Core (serial console ttyS0)** | The text wizard on `ttyS0` at 115200 baud |
| **Debian rescue mode** | Debian's plain initrd with `rescue/enable=true`; nothing is seeded |

The menu waits for a selection in BIOS and UEFI. The target boots under the firmware the
installer was started under (`/sys/firmware/efi`): shim and systemd-boot starting a signed
boot image on UEFI, GRUB in the MBR of every selected disk reading per-slot entries from the
boot partition on BIOS. Every disk carries a 1 MiB `nex-bios` partition in front of the ESP
so the same layout boots under either firmware. The BIOS path counts as verified only after
the QEMU BIOS-mode tests in the
[host image plan](../docs/plans/2026-10-01-immutable-nex-core.md) (D2) have run; that
section also states the limits of BIOS boot. [Install Nex Core](../docs/guide/install-nex-core.md)
is the operator procedure.

Debian Installer carries no `grub-install`, so on BIOS firmware the media provides one: `nex/grub/` is a
chroot-able root fragment (`grub-install`, `grub-probe`, `grub-mkimage`, the i386-pc modules including
`grub-bios-setup`, and the full shared-library closure) extracted at build time from the system image's own
tree (`stage_grub_bios` in `installer/build.sh`; offline and network ISO alike). `nex-image-install.sh` copies it
to RAM and `nex-hostimage` runs `grub-install --target=i386-pc` inside it for every selected disk.

## Install in a disposable VM first

Boot the ISO with at least 2 virtual CPUs, 4 GB RAM and a blank 24 GB virtual disk, or two
blank disks of the same size to exercise the mirrored data volume. Use hardware
acceleration/nested virtualization when testing KVM guests. Select
**GUI install - Nex Core** for the Nex mouse/keyboard wizard, or
**Minimal install - Nex Core (text)** for the same wizard in a keyboard-only interface.
Both entries boot the same Nex initrd; minimal adds `DEBIAN_FRONTEND=newt`, so cdebconf
renders the questions as text and no X server is started.

The GUI runs Nex's own upfront GTK wizard: Welcome → Host → Network → Disk → Review.
A vertical step list sits beside a white form panel, with persistent Back/primary-action
navigation and scrollable page content. Hostname is editable on both Host and Network;
the fields share one value and the same validation. The native backend progress shell uses matching
typography and colors while retaining its error and recovery controls. While a backend
step is running it shows **Still working…** and a live **Installer activity** log that
tails the same syslog and prepare logs as **View log** on failure.
It detects adapters, shows MAC/link/driver information, and saves per-NIC disabled, DHCP
or static IPv4 settings with one optional VLAN (0 untagged, 1–4094 tagged). The selected
management adapter alone owns the default route and DNS; installed interface names are
MAC-bound `nexN` names shown in the review. IP settings are staged for first boot. During setup, inactive device NICs are enabled
only to read carrier, with IPv6 autoconfiguration suppressed and no DHCP or addresses
assigned. Selecting DHCP later requests a lease preview for the chosen NIC/VLAN,
showing searching/address/failure with Retry; it does not apply an IP, route or DNS in
the installer. A green light marks Link detected; No link / Link unknown use a neutral light. Status refreshes without replacing field edits. Selecting management creates `br0` with that NIC (or VLAN) as its port and moves the
management IP/gateway/DHCP to the bridge on first boot. Bonds, custom bridges, trunks, multiple VLANs per NIC and IPv6 settings
are outside this alpha. The English UI fills the available installer screen and adapts to
resolution changes. It offers five named keyboard layouts, area/timezone dropdowns from the bundled list, and a
password visibility toggle that resets when leaving the host page. Timezone selection
requires no typing; UTC is the default.

Both modes require explicitly selected writable, nonremovable whole sd/vd/xvd/nvme disks of
at least 24 GiB, plus a confirmation that defaults to no and names every selected disk at review
before installation. Returning to Review resets the confirmation.
Both offer the same tick list, in the same sorted order, with the same facts about each disk:
device, capacity, SSD/HDD/NVMe, model, serial and whether it already has partitions. The graphical
step uses a checkbox column (Space or Enter ticks the focused row) and a live summary line under
the list that names the resulting layout - single disk, or a mirror with its smallest member's
capacity - and reports a selection outside the 10 % rule in red while the operator is still
choosing. Disks under the floor are omitted from both lists, not offered and refused.
A lone eligible disk is ticked for them; a mirror never is, but a tip offers it when two
similar-sized disks are present. `nex-config` stays the authority: the summary repeats its rules,
it does not replace them.

Every selected disk gets the same GPT layout on either firmware: a 1 MiB BIOS boot partition
(`nex-bios`), a 1 GiB ESP, two system slots (root and integrity partitions) and `nex-data`.
One disk holds one `nex-data`; two or more disks mirror only `nex-data` (md RAID1), identical
copies rather than combined capacity. There is no swap partition. Every member must be within
10% of the smallest selected disk's capacity. The validation handoff rechecks the identity,
capacity and serial of every selected disk before anything is written.

The installer does not use Debian's partitioning, base-system or boot-loader steps.
`nex-config` writes `layout=single|raid1`, the disk list and the `/etc` seed;
`partman/init.d/00nex-recheck` hands the selected disks to `nex-image-install.sh` without
clearing them. The installer first verifies the image (`nex-hostimage verify`) and the
media payloads, then stops the md arrays that hold a selected disk and runs the static
`nex-hostimage install|repair`, which wipes and lays out the disks. It assembles only this
install's data mirror, never `mdadm --assemble --scan`. **Repair image** re-lays the GPT, slots
and boot partitions of the disks that hold a complete `nex-data` and keeps that volume.
Offline media copies the Controller system image and VirtIO payloads to `nex-data`; a net
installer downloads them.
S7 result: [docs/qa/image/phase2-installer.md](../docs/qa/image/phase2-installer.md).

Both frontends write the same private `/run/nex-installer/config.ini` and both are applied
by the same `nex-config`, so validation, the staged `/etc`, the generated debconf answers
and the installed host are identical. The text frontend is
`installer/gui/wizard-text.sh` with `installer/gui/wizard-text.templates`; it asks host,
keyboard, timezone, password, management adapter (including the same offline choice), per-adapter
IPv4/VLAN, DNS, disk and the erase confirmation, supports **Go Back**, and never partitions or
configures the running machine. It runs the shared hardware preparation before its own question
frontend exists, so the backend detection steps keep their own screen, and it has `nex-config`
validate the settings before it finishes: a rejection reports `nex-config`'s own reason, removes
the handoff and returns to the questions with every answer retained.
`tests/integration/installer-text-wizard.py` drives it against a fixture machine.

A safe seed (`installer/preseed.cfg`: root-only account policy) is embedded in the initrd all
Nex entries boot, avoiding the routine file-preseed loading screen. The rescue entry boots
Debian's plain initrd unseeded, so rescue reaches a shell without any installation answers
preloaded. The pinned cdebconf frontend remains for installation progress and native backend
errors/recovery questions. It is not the primary configuration workflow. Hardware
preparation failures remain visible and retryable.

Every mode installs the same headless host with the Nex Core local HTTPS management UI
and the Controller system image (deployed from the local web UI). No ordinary user account is created. Root
passwords must be nonempty and confirmed in both modes. Use root at the local console or over SSH after reboot. Eject the ISO after installation.
SSH host keys are generated per installation. Open the local management URL shown at
the console and follow the [Controller deployment procedure](../docs/guide/deploy-controller-appliance.md).
Controller deployment creates its VM and enrolls this host using Core's embedded agent;
the installer itself does not deploy a running Controller. Shared storage, HA and
Container Services remain later milestones.

Either mode configures the selected management adapter on `br0` for the installed host;
neither configures a firewall policy. Use an isolated lab network and only disposable
disks. If graphics fail, choose minimal installation before
accepting disk writes; it asks the same questions and installs the same host. [Wizard QA](../docs/qa/iso/2026-09-08-nex-wizard.md) records actual
checks and limits; [installer usability QA](../docs/qa/iso/2026-09-08-installer-qol.md)
tracks the fullscreen and input-control checks separately. Earlier custom-shell evidence
does not verify the new workflow.

## Check the installed host

Run these commands as root at the local console:

```sh
virt-host-validate qemu
virsh -c qemu:///system list --all
systemctl --no-pager status libvirtd ssh chrony
sshd -T | rg permitrootlogin  # use grep if rg is unavailable
```

For a first NAT guest, explicitly inspect and start libvirt's default network:

```sh
virsh net-list --all
virsh net-start default
virsh net-autostart default
```

Create a disposable Linux guest through the Core API (the host image ships no
`virt-install`) using a separately supplied trusted guest ISO; verify guest networking and data persistence after guest and host
reboots. Seeing a boot menu does not prove this gate.
On a nested host `/dev/kvm` must exist and permit accelerated guest operation.

After installation, a host is updated by staging and activating a newer signed system
image (see [software updates](../docs/guide/software-updates.md)). Signed upstream boot files
are retained, but physical UEFI/Secure Boot compatibility and BIOS boot on real hardware are
separate hardware acceptance checks; do not describe either as certified from ISO inspection alone.

## References

- [Debian image verification](https://www.debian.org/CD/verify)
- [Preseed from installation media](https://www.debian.org/releases/trixie/amd64/apbs02.en.html)
- [Preseed advanced options](https://www.debian.org/releases/trixie/amd64/apbs05.en.html)
