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

On an amd64 Linux machine with Docker, network access and approximately 8 GB free:

```sh
./installer/build.sh
```

The builder is pinned to a Debian container digest. It verifies the base ISO
against a pinned SHA256 and Debian's signed checksum list using the documented
CD-signing fingerprint. APT authenticates additional packages. Complete package
dependencies are downloaded using an empty dpkg status so build-container packages
cannot accidentally be omitted. Installation uses only the bundled local repository.

Output: `dist/nex-core-0.1.0-alpha1-amd64.iso`, its SHA256 file, package manifest,
and build record. Cache/extracted media: `.cache/nex-iso/`. Exact Nex recipe source,
license notices and source checksum are included on the ISO and installed host.
The added-package manifest does not yet cover every original installer component;
this and matching upstream source retention are release gates, not completed claims.

This recipe is repeatable but **not yet bit-reproducible**: added packages resolve
against current `trixie` repositories and build timestamps change. Retain the output,
manifest and cache for each tested build. A dated repository snapshot/package lock
and complete source bundle are required before external release.

## Install in a disposable VM first

Boot the ISO with at least 2 virtual CPUs, 4 GB RAM and a blank 24 GB virtual disk.
Use hardware acceleration/nested virtualization when testing KVM guests. Select
**Install Nex Core**; serial installation uses `ttyS0` at 115200 baud.

Debian asks for language, networking, hostname, the root password (with confirmation),
target disk, partitioning and erase confirmation. Select only the disposable disk.
A mirror is not needed. No ordinary user account is created; Debian service accounts
remain. Blank passwords and mismatched confirmation prompt again. Use root at the local console after reboot.
Root SSH remains disabled, so a fresh installation has no SSH login account.
Eject the ISO and reboot after installation. SSH host keys are generated per installation.
No Nex Controller, enrollment agent, pool, shared storage, HA or Docker service exists
in this alpha. Debian's normal text installer follows the branded boot menu.

The installer leaves management networking as selected and does not format secondary
VM data disks. Local disk selection/partitioning remain Debian's interactive workflow.
Nex doesn't enable a management bridge or a firewall policy silently. Use this alpha
on an isolated lab network until host access policy has been validated.

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
`trixie-security` repositories. No Nex APT repository exists yet. Signed upstream
boot files are retained, but physical UEFI/Secure Boot compatibility is a separate
hardware acceptance check; do not describe it as certified from ISO inspection alone.

## References

- [Debian image verification](https://www.debian.org/CD/verify)
- [Preseed from installation media](https://www.debian.org/releases/trixie/amd64/apbs02.en.html)
- [Preseed advanced options](https://www.debian.org/releases/trixie/amd64/apbs05.en.html)
