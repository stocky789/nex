# Nex licensing

Decision: Matt selected AGPL on 2026-09-08. The project applies **AGPL-3.0-only**
(GNU Affero General Public License version 3 only) to original Nex source code,
build scripts, and configuration. A file carrying another license keeps that
license. The complete, unmodified [license text](../LICENSE) was retrieved from
[GNU](https://www.gnu.org/licenses/agpl-3.0.txt). Use
`SPDX-License-Identifier: AGPL-3.0-only` in new source files where the format
supports comments. This decision does not relicense third-party material.

The Nex name, logo, and standalone artwork are outside this code-license grant.
Matt has approved their use in the Nex project and its installer; a broader
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
Do not ship a placeholder or broken source link. No controller or agent service
is implemented by this licensing change.

This file records scope and required evidence; it does not certify a release
or substitute for checking each component's license terms.
