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
bundle, manifest, and their SHA-256 sidecars. The forge release carries those six
files plus the Core and Controller Debian packages, for eight assets total.

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

## OVMF 2023 certificate package — 2026-09-16

The installer carries Debian `ovmf-generic` `2026.05-2` from Debian sid because
the Debian 13 package does not provide the required 2023 Microsoft certificate
template. The ISO builder uses an isolated, signed sid APT source for that exact
package only; sid is not available to ordinary ISO dependency resolution or to
an installed host. It pins the binary SHA-256
`0f2d4de080ebeaf0972c5e212253c28dd2d90aa641542f42f37c6d6ecd4c67eb`, retains
the matching `edk2` `2026.05-2` `.dsc`, upstream tarball, Debian patch tarball,
the package copyright, and a checksum manifest under
`/nex/third-party/ovmf-generic/`. The installer retains that directory under
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

## virtio-win guest driver ISO — 2026-09-16

The public Core installer stages Fedora's virtio-win `0.1.302` ISO
(`virtio-win-0.1.302-1`, SHA-256
`303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d`, 877373440 bytes)
together with the matching Fedora source RPM
(`virtio-win-0.1.302-1.src.rpm`, SHA-256
`25561bba120e67bfae842429d5e25764c38e74b92f78d9ef90c20ec41d2ad75d`). The installation
media carries the complete driver ISO as gzip-compressed transport. Late-install
restores and verifies the original bytes at `/usr/share/nex/media/virtio-win.iso`
before making them available. It copies the notices and intact source RPM under
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

**External distribution of ZFS-enabled images, including alphas, remains blocked.**
The exact final component/source licence review, matching corresponding source and
notice retention, supported-kernel DKMS build, offline installation, Secure Boot
signing/key handling where applicable, kernel-upgrade and known-good boot recovery
must have inspected evidence. Until those gates close, ZFS-enabled images are for
internal disposable labs only. Configuration changes and unit tests do not establish
that an image builds, boots or can be redistributed. See the
[rollout verification](qa/2026-09-19-zfs-provisioning.md) for actual checks and limits.
That report records internal QA4 offline installation, first boot, reboot, and a
separate disposable kernel-package upgrade and retained-kernel fallback. Those
results narrow the technical unknowns; they do not close the remaining licence,
Secure Boot, exact-final-artifact, or external source-delivery gates.

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

Review exact artifacts and their retained notices in the [security package evidence](qa/security/package.md).
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
