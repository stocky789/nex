# Nex Core network installer

Date: 2026-09-24  
Status: both original ISO variants passed disposable BIOS installation and reboot.
The published 0.4.12 netinst has a reproduced UEFI graphical-install failure before
partitioning. A corrected temporary overlay passed disposable UEFI installation and
first boot; rebuilt-artifact, physical-hardware and publication acceptance remain open.

## 2026-09-25 correction

The published 0.4.12 graphical netinst passes the wizard's network preflight, then
Debian `netcfg` removes the live resolver before partman's second preflight. The
second check fails DNS resolution and returns to Debian's component menu without
erasing the disk. Save the wizard's installer-time resolver and restore it before
that second check; keep the check before any disk write. If a backend component
still falls back to Debian's menu, the graphical frontend stops with a visible
log instead of offering later components that could skip required work. The
corrected overlay passed fresh UEFI installation and first boot; verify a
newly mastered ISO before closing release acceptance.

API impact: none; the affected flow is local wizard networking → Debian `netcfg` →
partman preflight → installer display, with no Controller/Core operation or client API.
Guide impact: updated for the visible failure and recovery path. State owner: none;
the resolver copy exists only in the installer runtime and adds no durable state.

## Scope and source ownership

Project owner requested a network installer alongside the full offline ISO, including
forge build workflows. Reuse Debian Installer, the Nex graphical/text wizards and the
existing host configuration path. Download prebuilt packages; the existing DKMS step
still compiles ZFS kernel modules during installation.

Use Debian infrastructure directly for unmodified Debian packages. A Nex mirror of
Debian is unnecessary. Host Nex packages and release-specific Nex artifacts on Nex
infrastructure. The existing `dlm-controld` package rebuilt for the Pacemaker ABI is a
Nex build of third-party software and belongs with those release artifacts, retaining
its upstream license, notices and matching source. Keep the Windows driver media's
upstream identity and integrity checks; avoid introducing a second package resolver.

The bootable ISO still needs the installer, kernel, storage/network drivers, firmware,
trust material and enough userspace to establish networking. Network installation
must retain explicit disk selection and erase confirmation. Networking failures must
not be presented as successful installations. Downloaded packages and payloads must
be authenticated or checked against trusted release checksums before use.

## Delivery and acceptance

1. Set `NEX_NETINST_BASE_URL` to an HTTPS release directory to build the additive
   `nex-core-<version>-amd64-netinst.iso` and checksum alongside the full ISO. Share
   the retained `nex-core-<version>-source.tar.gz` source bundle and emit a separate
   `nex-core-<version>-netinst-compliance-manifest.json` and checksum. Publish the
   content-addressed `dist/netinst/*.deb` payloads flat at that release directory.
   Preserve the full ISO's offline installation path.
2. Configure installer-time management networking from the existing wizard choices;
   reject offline settings for the network variant before disk preparation.
3. Fetch the exact Debian package URLs from the build's authenticated pinned
   `snapshot.debian.org` closure, with SHA-256 values pinned in the ISO. APT computes
   packages missing or outdated in the installed base; fetch only those. Fetch the
   pinned VirtIO ISO directly from Fedora. Only Core (which includes the appliance)
   and the Nex-rebuilt DLM package use Nex hosting. Retain the existing installation
   behavior after verified downloads.
4. Build and retain both variants in matching `.github/workflows/` and
   `.gitea/workflows/` definitions, preserving the existing forge execution policy.
5. Exercise source routing, download integrity failures, missing network input and
   full-variant isolation with runnable checks. Build actual artifacts and run
   disposable BIOS/UEFI network installation, first boot and interrupted-download
   recovery before claiming installation acceptance. Inspect actual forge runs after
   an owner-authorized push; workflow definitions alone do not prove CI success.
6. Keep the [installation guide](../guide/install-nex-core.md) and
   [QA report](../qa/iso/2026-09-24-net-installer.md) aligned with actual behavior and
   executed evidence. Outstanding hardware, artifact and publication gates remain open.

Network delivery changes where packages are fetched, not redistribution obligations.
Follow [licensing requirements](../LICENSING.md): inventory embedded components and
retain their required corresponding source and notices; distinguish packages fetched
directly from Debian during installation from binaries redistributed by Nex. Nex-hosted
modified packages and payloads need their own matching source and notices.

## Release availability limit

A netinst ISO depends on its referenced release payloads remaining hosted. The existing
release pruning policy and a replacement build of the same version can make an older
ISO's pinned payloads unavailable. Content-addressed names prevent accepting different
bytes, but do not guarantee indefinite retention. Use the current matching ISO if
preflight or checksum verification fails because a release was replaced or removed.
Permanent archival hosting is outside this slice; the full ISO remains the offline option.

## Impact

**API impact: none.** The flow is local wizard → Debian Installer/network setup →
package/payload installation → installed host. It adds no Controller operation, Core
agent command, observed-state contract or client-facing API. Existing host enrollment
and appliance deployment APIs are unchanged.

**Guide impact: updated.** Document variant choice, internet requirements, package
sources, installation-time networking and failed-download recovery.

**State owner: none.** No new pool state or Controller-owned durable data is introduced.
Installer inputs configure the existing host files and package database; a rebuilt
Controller recovers pool resources through existing host inventory/adoption.

**Roadmap impact: none.** This is an additional delivery variant; no platform milestone
is promoted or acceptance gate closed by this change.
