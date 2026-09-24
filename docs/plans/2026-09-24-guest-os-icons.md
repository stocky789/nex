# Guest OS icons and guest-agent identification

Scope: show a small official distro mark beside the existing Guest OS text in the
Controller VM list and Overview panel. Keep the same label and a generic monitor
icon for missing IDs, unbundled artwork or failed image loads. No additional
packages, remote image requests, name heuristics or guest commands are needed.

Core already asks QEMU Guest Agent for `guest-get-osinfo`. Carry its optional `id`
as `guest.os_id` through the existing observation, cache, inventory validation and
VM response. Linux reports its os-release identifier; Windows reports `mswindows`.
Guest reports can be absent or customized; identification is not a health check.
Keep the existing Windows 11 build-name correction and stale-host behavior.

API impact: **changed contract** — additive optional `GuestInfo.os_id` on Core
inventory and Controller `GET /api/v1/vms`, with a 200-byte limit. Existing report
identity/authentication and read authorization apply. There is no new command,
mutation, retry or job. Missing IDs from older Core versions remain supported;
upgrade Controller before Core under the existing shared upgrade rule.

State owner: **none** — transient facts are re-observed by Core; no new durable
pool setting or Controller record exists. The existing guest's OS configuration
remains the source. Controller replacement does not lose a resource setting.

Guide impact: **updated** — guest-tools guidance explains canonical IDs and the
fallback. Official source/terms research lives beside the bundled artwork in
[the asset notices](../../web/public/guest-os/README.md). The project owner approved bundling the official-source set while unresolved
usage terms remain documented for release review; a source URL is not a
redistribution grant.

Acceptance: focused Core/agent/Controller checks, UI tests, typecheck/build,
API reference generation check, repository privacy and documentation gates,
independent review, and real browser inspection with simulated inventory.
[Execution and limits](../qa/2026-09-24-guest-os-icons.md) distinguish fixtures
from live Linux/Windows guest verification. This slice closes no phase gate.

## Mainstream coverage correction

Project owner clarified that Debian plus NixOS did not meet the requested breadth.
The target is a broad mainstream set: Debian, Ubuntu and its reported flavours,
Mint, Pop!_OS, elementary, Fedora, RHEL, CentOS, Rocky, AlmaLinux, Oracle Linux,
Amazon Linux, openSUSE, SLES, Arch, Manjaro, EndeavourOS, Alpine, Gentoo, Kali,
Parrot, MX, Zorin, Windows client/Server and common BSD guests. Generic Linux
uses the upstream Tux mascot. A shared family ID does not prove an Ubuntu flavour;
no flavour-specific logo is selected without actual guest evidence.

The first pass's generic brand-policy review was too shallow: official distro
artwork packages provide additional explicit licenses, including Mint-X,
Pop icon-theme, elementary icons, kali-themes and distribution-logos-openSUSE.
These package grants and official identification-use guidance are now assessed
per asset. The source catalogue distinguishes bundled files from unresolved marks.
Coverage now includes the mainstream families above plus KDE neon, deepin, Solus,
Mageia, OpenMandriva, Slackware, Tails and the common gaming/immutable desktop
distributions. The asset catalogue is the exact coverage list; unknown IDs retain
the monitor rather than inheriting a parent distro mark.

Decision, project owner, 2026-09-24: complete the official-source set and
document unresolved terms for release review. This explicitly supersedes
withholding those assets during this implementation. Retain known notices and
licenses, use only genuine official assets for factual guest identification,
and do not claim that this decision grants third-party rights or certifies
external redistribution. No additional approval is needed to complete this set.

API impact for the coverage extension: **existing contract** — consumes the same
optional `guest.os_id` introduced above. Guide impact: **updated**. No new state.
