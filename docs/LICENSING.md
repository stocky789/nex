# Nex licensing

Decision: project owner selected AGPL on 2026-09-08. The project applies **AGPL-3.0-only**
(GNU Affero General Public License version 3 only) to original Nex source code,
build scripts, and configuration. A file carrying another license keeps that
license. The complete, unmodified [license text](../LICENSE) was retrieved from
[GNU](https://www.gnu.org/licenses/agpl-3.0.txt). Use
`SPDX-License-Identifier: AGPL-3.0-only` in new source files where the format
supports comments. This decision does not relicense third-party material.

The Nex name, logo, and standalone artwork are outside this code-license grant.
project owner has approved their use in the Nex project and its installer; a broader
artwork redistribution license and trademark policy have not been selected.
Do not invent such permission or impose additional restrictions on AGPL-covered
code. Resolve the separate asset terms before distributing a branded release.
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

The reviewed build script includes Nex notices and a Nex source archive, but
only inventories the added `.deb` packages and does not collect their upstream
source or fully inventory the base ISO. Therefore this review supports keeping
AGPL for Nex; it does **not** establish that an ISO is ready for redistribution.
The release evidence below remains required, including firmware review and
resolution of artwork terms. No controller/agent dependency graph or built ISO
was audited in this review.

## Installer and installed system

Include `LICENSE`, `NOTICE`, and this document in the ISO's Nex documentation
directory and install them under `/usr/share/doc/nex-core/`. Include the exact
Nex source/build configuration used for that ISO in an accompanying source
archive; record its digest and the build revision or source-tree digest.
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
- A license review of any firmware or other separately licensed binaries and
  resolved redistribution terms for included Nex artwork.

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

OpenZFS is intentionally not included in the ISO or `nex-core-local` package.
It remains an optional existing-host capability pending an exact package/source
license review, DKMS and supported-kernel build evidence, offline dependency
evidence, Secure Boot signing/key-handling evidence where applicable, and
known-good boot recovery testing. This record grants no ZFS redistribution
clearance and makes no Secure Boot compatibility claim.

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
the existing complete-source, notice, firmware, and artwork gates remain open.

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
archive does not close the existing whole-ISO source, firmware or artwork release gates.

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
for all tzdata package contents. The approved raster brand asset keeps its separate terms.
The whole-ISO component/source, firmware and artwork release gates above remain open.


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
a tested delivery path. Resolve the existing Nex artwork terms independently.

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
evidence. The Debian component/source, artwork and external-distribution gates above
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
