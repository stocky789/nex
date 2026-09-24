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

The builder is pinned to a Debian container digest. It obtains guest UEFI firmware
from Debian 13 stable's `ovmf` package through the same pinned `trixie` APT closure
as other host packages; it does not use testing or sid. It verifies the base ISO
against a pinned SHA256 and Debian's signed checksum list using the documented
CD-signing fingerprint. APT authenticates additional packages. Complete package
dependencies are downloaded using an empty dpkg status so build-container packages
cannot accidentally be omitted. The host-local UI package is copied into the offline
repository and installed with the hypervisor packages. Installation uses only the
bundled local repository.

Windows guest-driver media is carried as gzip-compressed transport and restored
to its original checksum-verified ISO during installation, without network access.
The installation media retains the intact matching source RPM and notices; its
redundant extracted contents are retained in the separate release source bundle.
The Core package shares one identical Nex source archive between its Core and
appliance paths. Packaging regression checks run in both CI providers; measured
size and disposable installation evidence belong in the
[packaging QA report](../docs/qa/2026-09-18-iso-packaging-efficiency.md).

Output: `dist/nex-core-<version>-amd64.iso` (version = the `nex-core-local` package it installs), its SHA256 file, package manifest,
and build record. Cache/extracted media: `.cache/nex-iso/`. Exact Nex recipe source,
license notices and source checksum are included on the ISO and installed host.
The custom graphical frontend is built from authenticated cdebconf 0.280 source;
its exact modified source/build configuration and upstream notices ship alongside Nex source.
The added-package manifest does not yet cover every original installer component;
this and matching upstream source retention are release gates, not completed claims.

This recipe is repeatable but **not yet bit-reproducible**: added packages resolve
against the selected dated `trixie` snapshot and build timestamps change. Retain the output,
manifest and cache for each tested build. The dated snapshot is already pinned;
a complete package lock and source bundle remain release requirements.

The current working version is `0.4.11`; it is not a release merely because the build
resolves that changelog version. The recipe includes OpenZFS packages for VM storage
repositories; release publication does not treat them differently from any other ISO
component. External redistribution still follows [licensing](../docs/LICENSING.md).

## Install in a disposable VM first

Boot the ISO with at least 2 virtual CPUs, 4 GB RAM and a blank 24 GB virtual disk, or two
blank disks of the same size to exercise the mirrored system disk.
Use hardware acceleration/nested virtualization when testing KVM guests. Select
**GUI install - Nex Core** for the Nex mouse/keyboard wizard, or
**Minimal install - Nex Core (text)** for the same wizard in a keyboard-only interface.
Both entries boot the same Nex initrd; minimal adds `DEBIAN_FRONTEND=newt`, so cdebconf
renders the questions as text and no X server is started. The menu waits for your
selection in BIOS and UEFI. Serial installation uses `ttyS0` at 115200 baud and is the
only mode that still runs Debian's own installation questions.

The GUI runs Nex's own upfront GTK wizard: Welcome → Host → Network → Disk → Review.
A vertical step list sits beside a white form panel, with persistent Back/primary-action
navigation and scrollable page content. Hostname is editable on both Host and Network;
the fields share one value and the same validation. The native backend progress shell uses matching
typography and colors while retaining its error and recovery controls. While a backend
step is running it shows **Still working…** and a live **Installer activity** log that
tails the same syslog, partman and prepare logs as **View log** on failure.
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
at least 16 GiB, plus a confirmation that defaults to no and names every selected disk at review
before installation. Returning to Review resets the confirmation.
Both offer the same tick list, in the same sorted order, with the same facts about each disk:
device, capacity, SSD/HDD/NVMe, model, serial and whether it already has partitions. The graphical
step uses a checkbox column (Space or Enter ticks the focused row) and a live summary line under
the list that names the resulting layout - single disk, or a mirror with its smallest member's
capacity - and reports a selection outside the 10 % rule in red while the operator is still
choosing. Disks under 16 GiB are omitted from both lists, not offered and refused.
A lone eligible disk is ticked for them; a mirror never is, but a tip offers it when two
similar-sized disks are present. `nex-config` stays the authority: the summary repeats its rules,
it does not replace them.
One disk installs a whole-disk ext4 layout, unchanged. Two or more disks install an md RAID1
mirror (metadata 1.2) of a single ext4 root: identical copies, not combined capacity. Every
mirror disk gets the same GPT layout on either
firmware - a 1 MiB BIOS boot partition, a 512 MiB ESP, then the array member as partition 3 - so
the member index never depends on the firmware and `grub-install --target=i386-pc` has somewhere
to embed. Every member must be within 10% of the smallest selected disk's capacity.
The mirrored layout has no swap partition. GRUB is installed on every selected disk: legacy
BIOS through `grub-installer/bootdev`, UEFI by writing the mounted ESP and copying it to the
other members' ESPs, which Debian 13's `grub-efi-amd64` does not manage itself; an APT
`DPkg::Post-Invoke` hook repeats that copy whenever a package changes `/boot/efi`.
`mdadm` is installed from the offline repository, and the installed host's
`/etc/mdadm/mdadm.conf` and initramfs are regenerated from the array partman created. The ESP
mounted at `/boot/efi` is one member's own partition rather than part of the mirror, so the
mirrored host mounts it `nofail`: a replaced or failed disk must not stop the boot while the
mirror itself is healthy.
The validation handoff rechecks the identity, capacity and serial of every selected disk
before partitioning. See the
[operator procedure](../docs/guide/install-nex-core.md) for exact choices and recovery.

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

A safe seed is embedded in the initrd both wizard modes boot, avoiding the routine
file-preseed loading screen. The serial entries boot the untouched Debian initrd with the
media seed (`preseed/file=/cdrom/preseed.cfg`); the rescue entries boot it unseeded, so
rescue reaches a shell without any installation answers preloaded.
The pinned cdebconf frontend remains for installation progress and native backend
errors/recovery questions. It is not the primary configuration workflow. Hardware
preparation failures remain visible and retryable.

Both modes install the same headless host with the Nex Core local HTTPS management UI
and bundled Controller appliance. A mirror is not needed. No ordinary user account is
created; Debian service accounts remain. Root
passwords require at least eight characters and matching confirmation in both modes. Use root at the local console after reboot. Root SSH remains disabled,
so a fresh installation has no SSH login account. Eject the ISO after installation.
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
