# Nex licensing

Decision: project owner selected AGPL on 2026-09-08. The project applies **AGPL-3.0-only**
(GNU Affero General Public License version 3 only) to original Nex source code,
build scripts, and configuration. A file carrying another license keeps that
license. The complete, unmodified [license text](../LICENSE) was retrieved from
[GNU](https://www.gnu.org/licenses/agpl-3.0.txt). Use
`SPDX-License-Identifier: AGPL-3.0-only` in new source files where the format
supports comments. This decision does not relicense third-party material.

The Nex name, logo, and standalone artwork are outside this code-license grant.
Original Nex-branded artwork listed in [ARTWORK-LICENSE.md](../ARTWORK-LICENSE.md)
is licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
The Nex, Nex Core, and Nex Controller names, logos, and related brand identifiers
remain reserved by the Nex project; CC BY 4.0 does not grant trademark rights.
This reservation does not impose additional restrictions on AGPL-covered code.
Do not add third-party icons, fonts, or artwork without retaining their actual
licenses and verifying that the intended distribution is permitted.

## Debian compatibility review — 2026-09-08

There is no single Debian software license: each component retains its own
terms. Nex's AGPL-3.0-only choice does not itself conflict with distributing
separate Debian programs alongside Nex. The current installer recipe bundles
Debian packages and configures their operation; it does not link their code into
a Nex executable. This is consistent with the aggregate boundary in section 5
of [LICENSE](../LICENSE) and [GNU's aggregation guidance](https://www.gnu.org/licenses/gpl-faq.en.html#MereAggregation).

That conclusion is not blanket dependency approval. Before copying upstream
code into Nex, linking a library, or adding a plugin, check the exact license
and how the programs are combined. GPL-2.0-only code cannot ordinarily be
combined into an AGPL-3.0-only program without additional permission; shipping
separate programs on the same ISO is different. GPLv3 combinations are expressly
permitted by section 13, with each part retaining its license. LGPL libraries
have their own notice, source, and replacement/relinking requirements. See
[GNU's compatibility guidance](https://www.gnu.org/licenses/license-list.en.html#AGPLv3).
Modifications to Debian components must comply with their upstream terms;
Nex's default license does not override them.

The 0.2.9 release tooling inventories `.deb` and `.udeb` files from the assembled
media tree, records package identity, origin, checksum and declared source, retains
matching Debian source material, and writes a final-ISO compliance manifest that
binds the finished ISO and source bundle by filename and SHA-256. It also retains
component copyright notices where available. This corrects the former
added-package-only inventory limitation. It does **not** establish that any ISO is
ready for redistribution: the exact final artifacts, complete notice/firmware
review, source delivery, hosted publication, and applicable installation checks
must still be inspected and recorded. No controller/agent dependency graph or
published ISO was audited in this review.

## Installer and installed system

Include `LICENSE`, `NOTICE`, `ARTWORK-LICENSE.md`, and this document in the
ISO's Nex documentation directory and install them under
`/usr/share/doc/nex-core/`. Include the exact Nex source/build configuration
used for that ISO in an accompanying source archive; record its digest and the
build revision or source-tree digest.
Preserve upstream notices on the ISO and installed packages' copyright files
under `/usr/share/doc/<package>/copyright`. The original ISO's notices are not
replaced by Nex's `LICENSE`.

Debian packages have individual licenses, including packages in an installer
initrd, its kernel, bootloaders, and any firmware. Packaging them together does
not make the whole ISO AGPL. See [Debian's distribution guidance](https://www.debian.org/CD/vendors/legal)
and the actual licenses of the included components.

## Release evidence

A local alpha build is not proof that distribution obligations have been met;
sharing an alpha with another party still requires the applicable obligations.
Before handing out a binary ISO, produce and inspect:

- A manifest of every embedded binary package/component, version, architecture,
  origin, checksum, and corresponding source package/version where applicable.
  Cover the base ISO as well as added packages and installer components.
- A retained copy of required corresponding source for those exact versions,
  including Debian `.dsc`, upstream tarballs and Debian patches where applicable,
  Nex modifications, and the scripts/configuration needed to build and install
  the covered software. A Nex-only source archive does not cover Debian binaries.
- The relevant notices and license texts, with a tested source-delivery path.
  Prefer hosting the matching source bundle alongside each downloadable ISO
  with clear access instructions. Merely pointing to Debian mirrors is not the
  release process: their contents change and the distributor has obligations.
- A license review of any firmware or other separately licensed binaries. The
  Nex artwork notice and CC BY 4.0 license must accompany branded releases.

Packages downloaded from a Debian mirror during installation must be recorded
in installed inventory separately from packages redistributed inside the ISO.
If Nex later mirrors or embeds those packages, include them in the release
source/notice process as well. Capture exact source versions while available;
do not assume today's binary and source package versions match.

Future Nex network services must provide the source-access mechanism required
by the AGPL where applicable, including corresponding source for modifications.
Do not ship a placeholder or broken source link.

This file records scope and required evidence; it does not certify a release
or substitute for checking each component's license terms.

## 0.2.9 distribution tooling

The 0.2.9 ISO builder removes these unreviewed firmware packages from every
media-tree package location and package index before source collection and ISO
mastering: `dahdi-firmware-nonfree`, `atmel-firmware`, `bluez-firmware`,
`firmware-sof-signed`, `firmware-b43-installer`, and
`firmware-b43legacy-installer`. It refreshes affected `Packages`, `Packages.gz`,
firmware metadata, and Debian `Release` checksums, then fails if an excluded
package remains. This is a scoped omission, not a conclusion about the terms of
all remaining firmware.

The builder produces `nex-core-<version>-source.tar.gz` from the media inventory
and retained inputs. Its compliance manifest is written only after mastering and
records the exact final ISO and source-bundle filename and SHA-256. The download
publisher verifies both bindings before atomically publishing the ISO, source
bundle, manifest, and their SHA-256 sidecars. For the historical full-ISO release, the forge carried those six files plus the
Core and Controller Debian packages. The network-installer variant adds its own
ISO/checksum and compliance manifest/checksum, together with the release download
assets it references; inspect the current workflow for the complete asset set.

The artwork prerequisite is no longer open: branded artwork covered by this
repository is identified and licensed in [ARTWORK-LICENSE.md](../ARTWORK-LICENSE.md).
That resolves the Nex artwork redistribution basis only; it does not clear any
third-party component, firmware, source, notice, build, boot, CI, or publication
gate.

**0.2.9 exact-artifact evidence:** the final ISO (`4c44133a…2f2b9`), source bundle
(`eba9a430…a9679`) and manifest (`78237830…9a25`) pass their sidecars and binding checks.
The media/source inspection covers 1,585 component records, all 573 required exact source
package/version pairs, 725 extracted package notices plus the Nex notices, and confirms the six
excluded firmware packages are absent. BIOS/UEFI menu boots and unattended offline UEFI
installation, first boot and reboot passed under TCG. The full ISO, corresponding-source bundle,
manifest and both packages were then published and independently downloaded over public HTTPS;
their hashes, manifest bindings and signed APT metadata passed. Hosted CI remains unverified.

## Historical OVMF 2023 certificate package — 2026-09-16

The earlier installer carried Debian `ovmf-generic` `2026.05-2` from Debian sid because
the Debian 13 package does not provide the required 2023 Microsoft certificate
template. That ISO builder used an isolated, signed sid APT source for that exact
package only; sid was not available to ordinary ISO dependency resolution or to
an installed host. It pinned the binary SHA-256
`0f2d4de080ebeaf0972c5e212253c28dd2d90aa641542f42f37c6d6ecd4c67eb`, retains
the matching `edk2` `2026.05-2` `.dsc`, upstream tarball, Debian patch tarball,
the package copyright, and a checksum manifest under
`/nex/third-party/ovmf-generic/`. That installer retained the directory under
`/usr/share/doc/nex-core/third-party/ovmf-generic/`.

Exact-tree final ISO `403dc489fc8fb7d948fe3d27a561c4203a54642c27ac18141dae6ede4c84267c` was
inspected for this package, retained source and parsed enrolled `db` certificates; installation,
first boot and reboot checks passed. This is a scoped
source-retention result for one firmware package. It does **not** complete the whole-ISO component
manifest, source delivery, firmware-term review or external redistribution gate above. A release
still needs a tested public source-delivery location and all remaining component work.

The later exact-tree 0.2.9 ISO
`fe49ea1d69e16b45eaf3abd25cfec0e1645096a9fbe0a2532b6b2e5b136d1f64` also passed the
parsed 2023 `db` certificate, unattended offline installation, first-boot and reboot checks. This
reconfirms the scoped OVMF package result and does not close any whole-ISO distribution gate.

Decision: project owner, 2026-09-24 — new Nex Core ISOs use Debian 13 stable's `ovmf`
through the ordinary pinned `trixie` APT closure. The sid-only staging and source copy are
removed. The general component manifest and matching-source process must retain the exact
stable `edk2` source and `ovmf` notice for each built ISO. Historical 2023-certificate and
installation evidence above applies only to the earlier artifacts; the stable package's
enrolled template lacks those 2023 certificates. A new ISO, fresh signed guests and existing
guest firmware-state compatibility require fresh acceptance evidence before release.

## virtio-win guest driver ISO — 2026-09-16

The public Core installer stages Fedora's virtio-win `0.1.302` ISO
(`virtio-win-0.1.302-1`, SHA-256
`303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d`, 877373440 bytes)
together with the matching Fedora source RPM
(`virtio-win-0.1.302-1.src.rpm`, SHA-256
`25561bba120e67bfae842429d5e25764c38e74b92f78d9ef90c20ec41d2ad75d`). The full installation
media carries the complete driver ISO as gzip-compressed transport. Late-install
restores and verifies the original bytes at `/usr/share/nex/media/virtio-win.iso`
before making them available. The network-installer variant downloads the same
unmodified ISO directly from Fedora and verifies its release-pinned SHA-256; Nex
does not need to mirror that download. Retained source and notices remain required
for copies Nex redistributes, including the full ISO and release source bundle. The full ISO copies the notices and intact source RPM under
`/usr/share/doc/nex-core/third-party/virtio-win/`; redundant extracted RPM contents
are kept outside the installation-media tree. The separately distributed release
source bundle retains the complete extracted source inputs as well as the RPM.
`scripts/build-source-bundle.py` checks compressed media too and refuses it without
the matching source inputs and notice. No drivers, source inputs or notices are
dropped from the release's retained materials.

This is the public Fedora build (attestation/test-signed), not RHEL WHQL, redistributed
unmodified. It is not only BSD driver code. A listing of that exact ISO (checksum verified) on
2026-09-16 found:

| Content on the ISO | License evidence |
| --- | --- |
| Windows VirtIO drivers and driver utilities (Balloon, NetKVM, viostor, vioscsi, vioserial, viorng, vioinput, viofs, viosock and others) | BSD-3-Clause: `virtio-win_license.txt` and `debug/LICENSE` on the ISO; source retained as `virtio-win-prewhql-0.1-302-sources.zip` from the Fedora source RPM |
| `guest-agent/qemu-ga-i386.msi`, `guest-agent/qemu-ga-x86_64.msi` (MSI version 110.2.3) | QEMU guest agent `qemu-ga`, GPL-2.0-or-later, plus bundled GLib, libintl and iconv (LGPL), PCRE2 and MinGW runtime DLLs under their own terms. Corresponding source retained as `mingw-qemu-ga-win-110.2.3-2.el10.src.rpm` |
| `virtio-win-guest-tools.exe` | Installer bundle embedding the driver MSIs, the qemu-ga MSIs, the SPICE guest agent and SPICE QXL display driver MSIs. SPICE sources retained as `spice-vdagent-win-0.10.0-5-sources.zip`, `spice-qxl-wddm-dod-0.21-2-sources.zip` and `qxl-win-unsigned-0.1-24-sources.zip`; installer as `virtio-win-guest-tools-installer-0.1.302.tar.gz`; WinFSP/MS-RL runtime as `winfsp-master-sources.zip` |
| `virtio-win-gt-x64.msi`, `virtio-win-gt-x86.msi` | Driver installer MSIs |

The staged notice records this inventory. Retain it with redistributed copies.

## VM storage client dependencies — 2026-09-11

The VM-storage repository work adds Debian's `nfs-common`, `libnfs-utils`,
`cifs-utils`, `smbclient` and `open-iscsi` as separately executed host client
programs. Nex does not copy, link, enable a storage target service, or alter
their protocol implementations. A disposable Debian 13 package inspection on
2026-09-12 identified `libnfs-utils` 5.0.2-1+b2 (source `libnfs`) as
LGPL-2.1-or-later for its primary sources, with preserved BSD and GPL notices,
and `smbclient` 2:4.22.10+dfsg-0+deb13u2 (source `samba`) as GPL-3.0-or-later
with preserved third-party notices. These are aggregate components, not code
combined with Nex. Retain each exact package's copyright notice and
corresponding source with the component manifest before external distribution,
under the release evidence gate above. This inspection does not grant external
release clearance.

Decision: project owner, 2026-09-19 — OpenZFS is built into the Core distribution
recipe ([plan](plans/2026-09-19-zfs-repository-provisioning.md)). The installer and
Core package request Debian `zfs-dkms`, `zfsutils-linux` and matching kernel headers;
installer binary and source repositories include `contrib`. These are separately
executed native programs and a native kernel module, not implementation copied or
linked into Nex. The media carries the `zfs-dkms` source package only: no prebuilt
ZFS kernel module is redistributed, and DKMS builds the module on the installed
host against that host's kernel headers. This replaces the optional existing-host
dependency policy.

The rollout's package inspection identified Debian `zfs-dkms` and `zfsutils-linux`
`2.3.9-0+deb13u1`, source `zfs-linux` at the same version. Its Debian copyright file
identifies CDDL-1.0 and separately licensed BSD-2-Clause portions. Retain the complete
exact-package notices and matching source, including all component-specific terms;
this summary is not a blanket combined-kernel licensing determination.

Release publication does not inspect the compliance manifest for OpenZFS or other
storage-related packages; forge, website downloads and APT use the same Release workflow
when deploy secrets are set. Component licence review and source retention in this
section still apply to external redistribution claims.

### Prebuilt `nex-zfs` layer — 2026-10-01 (internal lab use only)

Decision: project owner, 2026-10-01 — the Nex Core image build may build OpenZFS
kernel modules ahead of time for internal lab use. This is **not** clearance for external
redistribution, and it does not change the statement above: no prebuilt ZFS kernel module is
redistributed outside internal lab use.

What the image build does: `scripts/build-zfs-modules.sh` builds `zfs.ko` and `spl.ko` from
Debian's `zfs-dkms` (source package `zfs-linux`, currently `2.3.9-0+deb13u1`; CDDL-1.0 with
separately licensed BSD-2-Clause portions) against the exact `linux-headers` of the base
kernel inside a pinned builder container, signs them with a Nex module key and installs them
in the `nex-zfs` sysext layer together with Debian's unmodified `zfsutils-linux` and
`zfs-zed`. The compiler, headers and DKMS exist only in the container. A layer is a
separate file on `nex-data` that is merged only when an operator or Core operation enables
it; the base image, the kernel and every other layer contain no ZFS code. The layer is
a mere aggregation with the kernel at the point of use: Nex adds no code to OpenZFS and
links nothing of its own into it. Whether shipping the combination is permitted outside the
lab is a legal question for the project owner; until it is answered no release publishes
`nex-zfs` (a publisher refusal for a manifest that lists it is release-pipeline work and is not built yet).

Obligations to keep before any external release: the exact `zfs-linux` source package, the
build recipe (`scripts/build-zfs-modules.sh` and its builder Dockerfile, with the snapshot
timestamp), the base kernel source, the CDDL-1.0 text and the Debian copyright notices of the
layer's packages, listed in the compliance manifest and the source bundle. The module
signing key is generated per developer under `.cache/` (ignored by Git) or supplied through
CI secrets; the throwaway development key signs nothing that is published.

`nex-cluster` and any later layer contain only Debian packages and Nex-built `dlm-controld`
and libvirt packages already covered above; each layer carries its package list in
`/usr/share/nex/layers/<name>.packages` and Debian's copyright files under
`/usr/share/doc`.

## In-band management-controller tool (`ipmitool`) — 2026-09-22

BMC fencing adds Debian `ipmitool` to `installer/packages.txt` and to the
`nex-core-local` package's `Depends`. Nex executes it as a separate program —
`ipmitool -I open …` against this host's own controller over `/dev/ipmi0`, and
`ipmitool -I lanplus … mc guid` to read a target controller's system GUID — and
parses its standard output. No `ipmitool` code is copied into Nex and nothing
links against its libraries, so this is aggregation, not a combined program.

Upstream `ipmitool` is BSD-3-Clause (Sun Microsystems, 2003). That was verified
against an installed copy of upstream release 1.8.19's own `COPYING` file on the
workstation used for this change. **Outstanding, not verified here:** the Debian
binary package's own `debian/copyright` was not inspected, because no Debian
system or package archive was available offline for this review; Debian's
packaging may add files under other terms, and `ipmitool` links OpenSSL, whose
notice travels with its own package. Inspect the exact Debian archive's
copyright file and record the exact version before any external distribution,
exactly as the clustered-storage dependency review did for its packages.

The kernel IPMI drivers (`ipmi_si`, `ipmi_devintf`) are part of Debian's own
`linux-image-amd64`, already in `installer/packages.txt`. Nex only lists them in
`/etc/modules-load.d/nex-ipmi.conf` so the kernel loads them at boot; it ships no
kernel code and creates no combined work with them.

Because this package enters the ISO through `installer/packages.txt`, the build's
generated component manifest and compliance manifest cover it automatically.
Before any external ISO distribution, retain the exact package's copyright notice
and matching corresponding source under the release evidence gate above. No
external release clearance is granted here.

## Watchdog self-fencing daemon (`sbd`) — 2026-09-22

Watchdog fencing adds Debian `sbd` to `installer/packages.txt` and to the
`nex-core-local` package's `Depends`. Nex Core does not execute `sbd` at all: it
writes `/etc/default/sbd` — a plain shell-variable configuration file of its own
authorship — and enables the packaged `sbd.service` through `systemctl`, which
Debian's own unit then starts as a dependency of `corosync.service`. No `sbd`
code is copied into Nex and nothing links against its libraries, so this is
aggregation, not a combined program. It stands beside `corosync`, `pacemaker`
and `dlm-controld`, which the clustered-storage dependency review already
covers and which `sbd` is built against.

Upstream `sbd` (ClusterLabs) is **GPL-2.0-or-later**, per the licence headers in
its own sources. GPL-2.0-or-later may be taken at version 3, so it is compatible
with distribution alongside AGPL-3.0-only Nex code — but Nex must not combine any
of it into its own sources, and does not: the only interface is a configuration
file and a systemd unit name.

**Outstanding, not verified here:** the Debian binary package's own
`debian/copyright` was not inspected, because no Debian package archive was
available offline for this review, and `sbd` links `libqb` and Pacemaker's
libraries, whose notices travel with their own packages. Inspect the exact
Debian archive's copyright file and record the exact version before any external
distribution, exactly as the clustered-storage dependency review did.

Because this package enters the ISO through `installer/packages.txt`, the build's
generated component manifest and compliance manifest cover it automatically.
Before any external ISO distribution, retain the exact package's copyright notice
and matching corresponding source under the release evidence gate above. No
external release clearance is granted here.

## Host hardware inventory tools — 2026-09-22

Host hardware discovery adds Debian `dmidecode` and `pciutils` to
`installer/packages.txt`. The Nex agent executes `dmidecode -t memory` and
`lspci -mm -nn -D` as separate programs and parses their standard output; no
code is copied into Nex and nothing is linked against them, so this is
aggregation, not a combined program. Both are GPL-2.0-or-later, which Nex must
not combine into its AGPL-3.0-only sources. `pciutils` also ships the `pci.ids`
device-name database under its own terms. `lsblk` is part of `util-linux`,
already in the base system, and is executed the same way.

Because these packages enter the ISO through `installer/packages.txt`, the
build's generated component manifest and compliance manifest cover them
automatically. Before any external ISO distribution, retain each exact
package's copyright notice and matching corresponding source under the release
evidence gate above, including the `pci.ids` data file's own terms. No external
release clearance is granted here.

## Native clustered-GFS2 dependencies — 2026-09-12

Clustered shared iSCSI uses separately executed Debian 13 programs: `corosync`
3.1.9-2+deb13u1, `dlm-controld` 4.3.0-3 (source `dlm`), `gfs2-utils` 3.6.1-1,
`pacemaker` and `pacemaker-cli-utils` 3.0.0-2, `pcs` 0.12.0.2-1, and the selected `fence-agents-redfish`,
`fence-agents-ipmilan`, and `fence-agents-virsh` 4.16.0-3 packages (source
`fence-agents`). A package-archive inspection verified their installed copyright
files and commands/services: `corosync`/`corosync.service`,
`dlm_controld`/`dlm.service`, `tunegfs2`, `pacemaker.service`, `cibadmin`, `pcs`, and the
three typed fence commands. Their notices identify BSD-3-Clause/GPL-3+ material
in Corosync and GPL-2+ primary licensing for the DLM, GFS2 utilities,
Pacemaker, PCS, and fence-agent packages; upstream notices remain authoritative.
Debian's `pacemaker` Depends chain includes `pacemaker-resource-agents` then
`resource-agents`; the exact archives place `ocf:pacemaker:controld` in
`pacemaker` and `ocf:heartbeat:Filesystem` in `resource-agents`. The explicitly
selected `fence-agents-virsh` archive supplies `fence_virsh`. These are required
dependencies, so Debian APT resolves them even with `--no-install-recommends`;
no redundant direct resource-agent package is needed.

Nex invokes these as separate aggregate programs and does not copy or link their
implementation. The installer disables their units after installation; only an
explicit validated Core clustered-storage configuration may enable Corosync and
Pacemaker. Preserve each exact binary package's installed copyright, source
package/version, checksum, and corresponding source with the ISO component
manifest before external distribution. This review is not release certification;
the existing complete-source, notice, and firmware gates remain open.

BUG-0128 requires a separately rebuilt Debian DLM package whose fencing helper
matches the selected Pacemaker ABI. The Nex build recipe remains original AGPL
code; DLM and its modified Debian packaging retain their own licenses and remain
separate programs. Retain the untouched Debian source inputs, complete rebuilt
source package with packaging changes, exact build/runtime versions, checksums
and copyright notices alongside the binary. A successful ABI smoke check does
not establish native fencing/recovery or complete ISO source compliance; both
remain separate acceptance gates.

The installer explicitly requests Debian's `linux-image-amd64` metapackage rather
than relying on a cloud-image kernel. A Debian 13 archive inspection found
`linux-image-6.12.107+deb13-amd64` 6.12.107-1 (source `linux-signed-amd64`
6.12.107+1) contains `kernel/fs/gfs2/gfs2.ko.xz`. The release manifest must
record the exact selected kernel and retain its required corresponding source;
this one package inspection does not certify a built ISO or hardware support.

## Recovery lab libvirt backport — 2026-09-25

The S0 recovery experiment requires testing Debian libvirt `11.3.0-3+deb13u3`
with upstream commit `7b889af2d235d3fadfc172f371f02df0b74e40f2`. Its two
permission-flag changes let an independently granted AppArmor write rule take
effect during external snapshot consolidation. The inspected Debian source
package's `debian/copyright` identifies the affected helper under LGPL-2.1-or-later;
the patch retains upstream attribution. A second, local patch assigns unique temporary
overlay names during external snapshot revert. A third, local helper patch completes
supported partial backing-chain metadata. A fourth local patch serializes per-disk
external snapshot consolidation so one disk's cleanup cannot revoke another
disk's temporary access. A fifth local patch bounds QMP reply parsing to the
supported backing-chain depth while retaining the general JSON parser's limit.
The stopped-deletion recovery candidate persists the native cancellation marker
for temporarily started domains in the same libvirt snapshot source file.
The local patches are explicitly LGPL-2.1-or-later,
matching the modified libvirt source files. Libvirt remains a separate Debian program,
not copied or linked into Nex's AGPL executable.

The next candidate removes the two integrated snapshot-deletion patches: recovery
uses public block-commit operations without data deletion, followed by metadata-only
removal and reference-checked file cleanup. The four retained patches cover AppArmor
permissions, unique revert overlays, partial backing chains and bounded QMP parsing.
Historical six-patch binaries and their matching source remain separate artifacts;
their validation does not certify the new package cohort.

The subsequent `+nex4` candidate adds a local patch excluding read-only disks
and non-disk devices from external-revert overlay creation (BUG-0647). It changes
`src/qemu/qemu_snapshot.c` under LGPL-2.1-or-later and must be retained in the matched
source package and build recipe. Its build and native validation have not started;
the earlier four-patch evidence does not certify this additional change.

The original Nex build recipe is AGPL-3.0-only. Retain the untouched Debian
source inputs, complete matching patched source package, copyright notices, build
inputs and checksums with every redistributed patched binary. A lab build or
successful snapshot test does not establish installer integration, complete ISO
source retention or release certification; those gates remain separate.

## Custom installer frontend — 2026-09-08

The Nex GUI now combines original AGPL-3.0-only shell code with the pinned cdebconf
0.280 GTK frontend. Its [exact source archive](https://deb.debian.org/debian/pool/main/c/cdebconf/cdebconf_0.280.tar.xz)
and `debian/copyright` identify the C frontend as BSD-2-Clause. Preserve that notice;
Nex's source file does not replace it. The build retains the downloaded source package,
the actual modified source tree and its build configuration in `nex-gui-source.tar.gz`,
with a checksum, on the ISO and installed host. `cdebconf-copyright` accompanies them.

GTK 2 and the GTK/Pango/Cairo/GLib runtime libraries remain dynamically linked upstream
components with their own terms. The compiler's libdebian-installer headers come from
GPL-2-or-later source (0.125), not GPL-2.0-only; GPLv3-compatible use is available.
The rebuilt GTK module uses the existing installer runtime libraries. Development packages
are confined to the build container. Review retained component notices and corresponding
source for the complete combined binary before external distribution; the new frontend
archive does not close the existing whole-ISO source or firmware release gates.

## Upfront wizard and network configuration — 2026-09-08

The original Nex GTK wizard and local configuration helpers are AGPL-3.0-only. They use
existing dynamically linked GTK/GLib libraries; `nex-config` additionally links the existing
libcrypt runtime for password hashing. Inspection of the cached Debian `libcrypt1`
`1:4.4.38-1` copyright file identifies libxcrypt's overall license as LGPL-2.1-or-later,
with BSD/public-domain components and separately identified build/test files. The inspected
builder has matching `libcrypt-dev` and `libcrypt1` versions `1:4.4.38-1`. This allows
the intended dynamic combination subject to the library's notices, corresponding-source
and replacement requirements; retain the exact component evidence with release materials.
No libxcrypt implementation is copied into Nex source.

The installed network writer generates native configuration for separately executed Debian
programs. Exact cached-package notices reviewed for this slice identify `ifupdown`
`0.8.44+deb13u1` as GPL-2-or-later and `dhcpcd-base` `10.1.0-11+deb13u3` as upstream
BSD-2-Clause with BSD-2-Clause-NetBSD, BSD-3-Clause, ISC, Expat and public-domain compatibility
files; its Debian packaging uses GPL-3-or-later. These programs remain separate aggregate
components, not code linked into the AGPL wizard. Preserve their package copyright files
and exact required sources; listing them here does not satisfy source delivery.

The build derives supported timezone identifiers from the builder's tzdata `2026b-0+deb13u1`
`zone.tab`; its inspected
header explicitly places that file in the public domain. This is not a blanket license claim
for all tzdata package contents. The approved raster brand asset is covered by
[ARTWORK-LICENSE.md](../ARTWORK-LICENSE.md). The whole-ISO component/source and
firmware release gates above remain open.


## Controller virtual appliance and local-host package — 2026-09-08

The appliance aggregates a Debian 13 cloud image, cloud-init, qemu-guest-agent and
other separately executed Debian programs with original Nex services and static UI.
Original appliance/package scripts remain AGPL-3.0-only; the qcow2 and `.deb` aggregate
does not relicense its third-party contents. No new third-party application library
is introduced by this delivery mechanism. Exact binary component licenses still
require inspection before redistribution, including builder-added packages.

The builder preserves guest package copyright files and emits `components.tsv` with
installed package/source versions plus the matching Nex source archive. These are
useful inputs, not complete Debian source retention or a release certification.
Before sharing this appliance or a host package containing it, apply the release
evidence requirements above to the complete base cloud image, added binaries and
firmware as applicable; retain exact required corresponding sources, notices and
a tested delivery path. Include ARTWORK-LICENSE.md with branded delivery.

The finished Nex manifest is signed for deployment integrity/authenticity; this is
separate from license compliance. Debian's [official cloud-image guidance](https://cloud.debian.org/images/cloud/)
documents verified HTTPS plus checksums for current cloud images, with checksum
signatures on legacy OpenStack images. Do not claim a current cloud-image signature
was verified when only its HTTPS checksum was checked. The Controller and local-host
services expose their matching Nex source archive through `/source`; this does not
replace the distributor's complete Debian source-delivery obligations.


## Security-build Go toolchain — 2026-09-08

The Controller appliance and host-package builders now use checksum-pinned upstream
Go 1.27.1 while retaining Debian's C compiler/libc. The [upstream Go license](https://go.dev/LICENSE)
uses BSD-style source/binary notice-retention and non-endorsement conditions. Preserve
its actual distributed license; this compiler change does not relicense original Nex code.
The package installs that notice as `/usr/share/doc/nex-core-local/go-LICENSE` and records
the compiled binary's Go version. The appliance retains `/usr/local/share/nex/go-LICENSE`;
the corresponding source snapshot also records its toolchain notice/version.

Review exact artifacts and their retained notices for each release (the earlier host-package evidence page was removed; see git history).
A clean advisory scan is not license certification or complete appliance source-delivery
evidence. The Debian component/source and external-distribution gates above
remain applicable to the security rebuild.

## Host root authentication — 2026-09-08

The original Nex helper links Debian 13 Linux-PAM (`libpam0g`); the package depends on
`libpam-modules` and uses its `pam_unix` module. Debian PAM 1.7.0-5 copyright was
inspected: libpam offers BSD-3-clause/GPL dual terms and pam_unix includes GPL-2-or-later
and permissive/public-domain components. These permit this use with AGPL-3.0-only Nex;
no GPL-2.0-only code is copied into Nex. The package retains Debian's PAM copyright
notice. Exact Debian corresponding-source retention and delivery remain subject to
the existing external-distribution gate above.

## Installer management bridge dependency — 2026-09-08

The ISO now includes Debian's `bridge-utils` as a separate native networking program.
Its packaged copyright was inspected: GPL version 2; Nex does not copy or link that
code. The generated ifupdown configuration invokes the installed platform hooks.
Retain `/usr/share/doc/bridge-utils/copyright`, the component manifest and matching
corresponding source under the existing distribution gate. This is aggregation,
not a relicensing or combined-library compatibility claim.

## Browser console client (noVNC) — 2026-09-09

The Controller and host-local browser bundles now include the noVNC core library
(`@novnc/novnc` 1.7.0 from the npm registry, MPL-2.0; its `vendor/pako` is MIT and
`core/des.js` carries BSD-style notices) to render VM consoles. Only the JavaScript core
is bundled; noVNC's HTML, CSS, fonts and images are not used. MPL-2.0 section 3.3 permits
combining these files with AGPL-3.0-only code as a Larger Work while the noVNC files stay
under MPL-2.0, and noVNC is not marked "Incompatible With Secondary Licenses". The files
are bundled unmodified; if a noVNC file is ever changed, its source must remain available
under MPL-2.0. The bundled notice file `web/public/third-party-notices.txt` and the source
archive's retained npm package tarball carry the license texts. The Go WebSocket bridge is
original Nex code; no Go dependency was added.

## Nex Core host networking runtime dependencies — 2026-09-19

The `nex-core-local` Debian package now declares `Depends: … ifupdown, bridge-utils, vlan`,
because Nex Core's own networking code executes those programs and the ifupdown hooks they
install rather than reimplementing them: `ifdown`/`ifup` are executed directly for a staged
management network change; the `bridge_ports`, `bridge_stp` and `bridge_fd` stanza keywords
are implemented by `bridge-utils`' `/etc/network/if-pre-up.d/bridge` hook; and the
`vlan-raw-device` stanza written for a tagged management NIC needs the `vlan` package's own
ifupdown hook. `iproute2` was already a dependency, and nothing executes `brctl`.

Declaring a dependency on separate Debian programs is **aggregation on the medium**, not
linking and not a combined work, so it raises no AGPL/GPL compatibility question. The three
packages must still appear in the component manifest and the source-delivery path before any
external ISO distribution, under the existing gate above. `ifupdown` and `bridge-utils`
copyrights were inspected in the two notes above; `vlan` is new to the manifest and its
packaged copyright has **not** yet been inspected — record that as outstanding.

Not verified: the hook ownership above was reasoned from the packaging layout and from a
laboratory observation that guest VLAN materialization failed until `ifupdown bridge-utils
vlan` were installed by hand. It has **not** been confirmed against a Debian 13 archive with
`apt-cache show` or `dpkg -L`; `tests/test_core_local_package.py` ties each declared
dependency to the construct in Nex Core that needs it, which catches the dependency rotting
but is not evidence about the Debian packages themselves.

## Optional pool-fencing package staging — 2026-09-24

The Core ISO package manifest stages Debian Trixie `sanlock` and
`python3-sanlock` 3.9.5-1 (including `wdmd`), `libvirt-daemon-plugin-sanlock` 11.3.0-3+deb13u3, and
`corosync-qdevice` and `corosync-qnetd` 3.0.3-2. The Controller appliance also stages
`corosync-qnetd`. These remain separately executed Debian programs; none
is a dependency or recommendation of the Nex Core or Controller Debian package,
and no third-party code is copied into Nex. `sanlock` and its Python binding are
GPL-2-or-later with
selected library/API files under LGPL-2.1-or-later. The libvirt locking plugin
is built from the libvirt source package, whose main source is LGPL-2.1-or-later.
The qdevice/qnetd source package is BSD-3-Clause, with two build helper scripts
under GPL-3-or-later and Debian packaging under BSD-3-Clause. Exact Debian
copyright files are retained in each installed package at
`/usr/share/doc/<package>/copyright`; the ISO component/source manifest process
continues to apply before external redistribution.

Evidence: Debian Trixie package records for [`sanlock`](https://packages.debian.org/trixie/sanlock),
[`python3-sanlock`](https://packages.debian.org/trixie/python3-sanlock),
[`libvirt-daemon-plugin-sanlock`](https://packages.debian.org/trixie/libvirt-daemon-plugin-sanlock),
[`corosync-qdevice`](https://packages.debian.org/trixie/corosync-qdevice), and
[`corosync-qnetd`](https://packages.debian.org/trixie/corosync-qnetd); exact
source copyright files for [sanlock](https://metadata.ftp-master.debian.org/changelogs/main/s/sanlock/sanlock_3.9.5-1_copyright),
[libvirt](https://sources.debian.org/src/libvirt/11.3.0-3%2Bdeb13u3/debian/copyright),
and [corosync-qdevice](https://sources.debian.org/src/corosync-qdevice/3.0.3-2/debian/copyright).

## Backup destination Go modules — 2026-09-28

The Core S3 and SFTP clients add the following modules to the `go list -deps`
import graph of `cmd/nex-core-agent` and `cmd/nex-core-local` after `go mod tidy`.
The licenses and separate notices below were checked in downloaded source for
these exact versions. This is not an inventory of a finished Core binary or ISO.

| Modules and exact versions | Source license and separate notice |
|---|---|
| `github.com/aws/aws-sdk-go-v2` 1.47.1; `aws/protocol/eventstream` 1.7.20; `feature/s3/manager` 1.23.10; `internal/configsources` 1.5.4; `internal/endpoints/v2` 2.8.4; `internal/v4a` 1.5.4; `service/internal/accept-encoding` 1.13.19; `service/internal/checksum` 1.11.5; `service/internal/presigned-url` 1.14.4; `service/internal/s3shared` 1.20.4; `service/s3` 1.113.4 | Apache-2.0 in each module's `LICENSE.txt`; root module also has `NOTICE.txt`. |
| `github.com/aws/smithy-go` 1.28.1 | Apache-2.0 `LICENSE` and `NOTICE`. |
| `github.com/pkg/sftp` 1.13.10 | BSD-2-Clause `LICENSE`. |
| `github.com/kr/fs` 0.1.0; `golang.org/x/crypto` 0.56.0 | BSD-3-Clause `LICENSE` in each module. |

The compiled AWS SDK and Smithy `internal/sync/singleflight` packages each
also carry a BSD-3-Clause `LICENSE` for incorporated Go code. The Core package
build copies these files and the module licenses and notices above from the
vendored build input into `/usr/share/doc/nex-core-local/go-dependencies/`,
preserving their paths; a missing file fails the build.
An isolated disposable package build ran this copy step and produced a `.deb`.
All 19 packaged dependency license/notice files matched the vendored source
archive byte for byte; `go version -m` on the packaged Core binary listed the
15 expected modules at the versions above. Nex, Go toolchain and PAM notices
were present.
The appliance bundle in that test contained placeholders, so this does not
verify a signed release package or ISO.

The `feature/s3/manager` module's `go.mod` marks it deprecated in favor of
[`feature/s3/transfermanager`](https://github.com/aws/aws-sdk-go-v2/discussions/3306).
That changes maintenance planning, not its Apache-2.0 redistribution terms.

These permissive licenses permit combination with original AGPL-3.0-only Nex
code while their own copyright and license terms remain in force. Preserve the
exact applicable license texts, copyright statements and AWS/Smithy
notices in binary distribution materials; retain corresponding source and
recheck the actual built dependency graph and final artifact before external
distribution. This source inspection does not certify an ISO, package or
source-delivery path. The existing complete-component and matching-source
release gate still applies.

## Read-only iSCSI inspection helper — 2026-10-03

The original AGPL-3.0-only `nex-iscsi-inspect` helper dynamically links Debian's
`libiscsi7` **1.20.0-4** and `libblkid1` **2.41.5-0+deb13u1** in the pinned image package set. Development headers
are build dependencies only; neither a separate kernel initiator nor a modified
Debian library is shipped for discovery.

- [Debian's exact libiscsi copyright record](https://metadata.ftp-master.debian.org/changelogs/main/libi/libiscsi/libiscsi_1.20.0-4_copyright)
  licenses the linked library/header code under **LGPL-2.1-or-later**. The package's
  GPL-2.0-or-later example utilities are separate programs, not copied or linked
  into this helper.
- [libblkid's library license](https://github.com/util-linux/util-linux/blob/v2.41.5/libblkid/COPYING)
  is **LGPL-2.1-or-later**. The helper uses its native metadata parser; its bounded
  private-descriptor read adapter is original Nex code, not a library fork.

These library licenses permit dynamic combination with original AGPL Nex code,
subject to preserving their notices, matching corresponding library source and
the LGPL user's replacement/relinking rights. Keep the complete Debian package
copyright records with the helper's binary distribution and retain the shared
libraries as replaceable Debian components. The helper's capability check refuses
an incompatible metadata library instead of weakening read-only probing.

This license/source review is not external-release certification. The existing
complete-component manifest, matching-source retention and tested source-delivery
gate still applies to both Core editions and their final release artifacts.

## Device firmware and CPU microcode in the Core image — 2026-10-07

The Core image installs Debian's `firmware-linux-free` (main) and, from the `non-free-firmware`
component of the same pinned snapshot, `firmware-misc-nonfree`, `firmware-bnx2`,
`firmware-bnx2x`, `firmware-qlogic`, `firmware-realtek`, `firmware-intel-misc`,
`firmware-myricom`, `firmware-cavium`, `firmware-netxen`, `intel-microcode` and
`amd64-microcode`. Nex does not modify them. The kernel loads device firmware from
`/usr/lib/firmware` by name; `image/release.py` concatenates the two vendors' microcode files
into an early-microcode cpio placed first in the UKI's initrd. This is the same separate-program
aggregation as the other Debian packages above, but these are redistributable proprietary binaries
with their own licences, not free software.

Outstanding before any external ISO or release: record each package's exact version and licence
files in the component manifest (the per-package `copyright` files ship in the image), confirm that
the redistribution terms of every included blob permit shipping it in a signed read-only image, and
confirm the tested source/notice delivery path covers non-free-firmware. Graphics, wireless, audio
and Bluetooth firmware stay out (`image/denylist.txt`). The installer environment still carries no
device firmware; that is a separate decision.
