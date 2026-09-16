# Nex Core first ISO

Build a local amd64 development ISO from Debian 13.6.0 netinst, preserving its
signed boot binaries and hybrid BIOS/UEFI layout. This is Nex Core's own
Debian-derived distribution: installed `/etc/os-release` identifies Nex Core
(`ID=nex-core`, `ID_LIKE=debian`), and the installed GRUB menu uses Nex Core.
A local dpkg diversion preserves Debian's `/etc/os-release` symlink separately;
base-files upgrades update that diverted path while leaving Nex's override intact.
`/usr/lib/os-release`, `/etc/debian_version`, package licenses and Debian ancestry
remain intact. ISO metadata and `README-NEX.txt` identify modified Nex media. Nex source is AGPL-3.0-only;
see [licensing](../docs/LICENSING.md) before distributing an image.

## Build

On an amd64 Linux machine with Docker, network access, the pinned Debian cloud
base image under `.cache/appliance/`, and the appliance signing key:

```sh
./installer/build.sh
```

Every ISO build always rebuilds from the current tree:

1. Corresponding source archive (`scripts/build-source.py`)
2. Controller appliance qcow (`appliance/build.py`) with that source
3. `nex-core-local` package from that appliance + source
4. The ISO itself

Reusing an older `CORE_LOCAL_DEB` is rejected. By default the build derives `NEX_VERSION` from the
newest numbered `CHANGELOG.md` heading. Override it only for a deliberate local build; Debian
pre-release versions use `~` so the later final version sorts above them.
Signing defaults to `~/.local/share/nex-dev/nex-appliance-signing.pem`; override with
`NEX_APPLIANCE_SIGNING_KEY`, `NEX_APPLIANCE_BASE_IMAGE`, and `NEX_APPLIANCE_BASE_SHA256`.
The build refuses to finish unless Core package source, Controller appliance source,
and appliance version all match this build.

The builder is pinned to a Debian container digest. It verifies the base ISO
against a pinned SHA256 and Debian's signed checksum list using the documented
CD-signing fingerprint. APT authenticates additional packages. Complete package
dependencies are downloaded using an empty dpkg status so build-container packages
cannot accidentally be omitted. The host-local UI package is copied into the offline
repository and installed with the hypervisor packages. Installation uses only the
bundled local repository.

Output: `dist/nex-core-<version>-amd64.iso` (version = the `nex-core-local` package it installs), its SHA256 file, package manifest,
and build record. Cache/extracted media: `.cache/nex-iso/`. Exact Nex recipe source,
license notices and source checksum are included on the ISO and installed host.
The custom graphical frontend is built from authenticated cdebconf 0.280 source;
its exact modified source/build configuration and upstream notices ship alongside Nex source.
The added-package manifest does not yet cover every original installer component;
this and matching upstream source retention are release gates, not completed claims.

This recipe is repeatable but **not yet bit-reproducible**: added packages resolve
against current `trixie` repositories and build timestamps change. Retain the output,
manifest and cache for each tested build. A dated repository snapshot/package lock
and complete source bundle are required before external release.

## Install in a disposable VM first

Boot the ISO with at least 2 virtual CPUs, 4 GB RAM and a blank 24 GB virtual disk.
Use hardware acceleration/nested virtualization when testing KVM guests. Select
**GUI install - Nex Core** for the custom Nex mouse/keyboard wizard, or
**Minimal install - Nex Core (text)** for the simple keyboard interface. The menu
waits for your selection in BIOS and UEFI. Serial installation uses `ttyS0` at 115200 baud.

The GUI runs Nex's own upfront GTK wizard: Welcome → Host → Network → Disk → Review.
A vertical step list sits beside a white form panel, with persistent Back/primary-action
navigation and scrollable page content. Hostname is editable on both Host and Network;
the fields share one value and the same validation. The native backend progress shell uses matching
typography and colors while retaining its error and recovery controls.
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

The GUI requires an explicitly selected writable, nonremovable whole sd/vd/xvd/nvme disk of
at least 16 GiB, plus an unchecked-by-default erase checkbox naming that disk at review
before whole-disk ext4 installation. Returning to Review resets the confirmation.
The validation handoff rechecks disk identity before partitioning. Minimal text installation
retains native language, account, networking, partitioning and GRUB questions. See the
[operator procedure](../docs/guide/install-nex-core.md) for exact choices and recovery.

A safe GUI seed is embedded in the initrd, avoiding the routine file-preseed loading screen;
minimal installation keeps the shared media seed. The pinned cdebconf frontend remains for
installation progress and native backend errors/recovery questions. It is not the primary
GUI configuration workflow. Hardware preparation failures remain visible and retryable.

Both modes install the same headless host with the Nex Core local HTTPS management UI
and bundled Controller appliance. A mirror is not needed. No ordinary user account is
created; Debian service accounts remain. GUI root
passwords require at least eight characters and matching confirmation; minimal rejects
blank passwords. Use root at the local console after reboot. Root SSH remains disabled,
so a fresh installation has no SSH login account. Eject the ISO after installation.
SSH host keys are generated per installation. Open the local management URL shown at
the console and follow the [Controller deployment procedure](../docs/guide/deploy-controller-appliance.md).
Controller deployment creates its VM and enrolls this host using Core's embedded agent;
the installer itself does not deploy a running Controller. Shared storage, HA and
Container Services remain later milestones.

The GUI configures the selected management adapter on `br0` for the installed host;
it does not configure a firewall policy. Use an isolated lab network and only disposable
disks. If graphics fail, choose minimal installation before
accepting disk writes. [Wizard QA](../docs/qa/iso/2026-09-08-nex-wizard.md) records actual
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

Create a disposable Linux guest with `virt-install` using a separately supplied
trusted guest ISO; verify guest networking and data persistence after guest and host
reboots. Installing the host packages or seeing a boot menu does not prove this gate.
On a nested host `/dev/kvm` must exist and permit accelerated guest operation.

After installation, updates use explicit Debian `trixie`, `trixie-updates`, and
`trixie-security` repositories. Nex product updates use the separately signed Nex APT
archive configured by `/etc/nex/updates.conf`; that public archive can lag an unpublished
local development build. Signed upstream boot files are retained, but physical UEFI/Secure
Boot compatibility is a separate hardware acceptance check; do not describe it as certified
from ISO inspection alone.

## References

- [Debian image verification](https://www.debian.org/CD/verify)
- [Preseed from installation media](https://www.debian.org/releases/trixie/amd64/apbs02.en.html)
- [Preseed advanced options](https://www.debian.org/releases/trixie/amd64/apbs05.en.html)
