# Release channels: stable and development

Date: 2026-10-02  
Status: implemented on the `immutable` branch, because Nex Core already carries the channel
mechanism; checked on a workstation only (see [verification](../qa/release-channels.md)), not yet on a forge run or host.

## Decision

Nex ships on two channels. **Stable** follows `main`. **Development** follows a new `dev` branch, the
staging area every feature branch opens its pull request against; `dev` is promoted to `main` by a
reviewed pull request. Operators pick the channel in Nex Core and Nex Controller settings, and each channel has its
own release and download tree.

| | Stable | Development |
|---|---|---|
| Branch | `main` | `dev` |
| Channel id (`channel=` in `/etc/nex/updates.conf`) | `stable` (the default) | `dev` |
| Version | newest numbered CHANGELOG heading, e.g. `0.4.18` | pre-release of the next release, e.g. `0.4.19-dev.231` (Debian `0.4.19~~dev.231`) |
| Forge release | `vX.Y.Z`, release | `vX.Y.Z-dev.N`, **pre-release**, newest 3 kept (as many as the download tree keeps, so every kept netinst ISO still works) |
| Downloads | `<site>/downloads/<version>/`, `latest` | `<site>/downloads/dev/<version>/`, own `latest` |
| Image update tree | `<base>/stable/channel.json` | `<base>/dev/channel.json` |
| Website pages | deployed from `main` only | never deployed from `dev` |

The download root holds the stable channel at the top and the development channel
under `dev/`: each is an independent tree, so a mirror that copies the root carries both and the
host setting only picks the subtree. The image update tree already has one directory per channel.

## Version identity of a development build

A development build is a pre-release of the release `main` will publish next, so it sorts below that release and
above everything already released (dpkg and the image publisher's `sequence`-equals-label-order check both hold):

- `next` is the numeric base of the newest CHANGELOG heading on `dev` when `main` has not published it yet
  (the owner cut it on `dev`), otherwise that heading with the patch number raised by one.
- version = `next` + `-dev.N`; Debian form `next~~dev.N`. The double tilde keeps development builds below
  alpha, beta and release-candidate versions without changing their base when an alpha/rc heading is cut.
  `N` is `git rev-list --count HEAD`, so it only grows and a rebuild of one commit reproduces the same
  version, which the release job replaces rather than duplicates.
- `main` having a newer heading than `dev` is refused ("merge `main` into `dev`"): a development build must never be
  numbered below a published stable release.
- If a newer `dev` heading has the same numeric base as a prerelease already published by `main`,
  development numbering is refused until a heading for the next numeric release is cut: `~~dev.N`
  cannot sort above that already published alpha/rc.
- Release notes are `[Unreleased]`, preceded by the cut heading's entries when that heading is ahead of `main`.

Release cutover (2026-10-03): project owner requested the changelog/version bump to **0.4.20**.
The current Unreleased entries move under that heading; Unreleased remains empty. This prepares
the source release identity only, without tagging, publishing or promoting branches.
**API impact: none** — version resolution already reads the newest changelog heading; no
Controller/Core or client-facing contract changes. **Guide impact: none** — documented publishing
steps are unchanged. Both release workflow copies continue using the same version resolver.

Forge naming correction (2026-10-03): release titles and attached ISO filenames use the
tag version (`0.4.20-dev.247`), not Debian's `0.4.20~~dev.247`. The release step hard-links
the built ISOs under those names and writes sidecars with matching filenames, reusing the
already computed hashes. Build identities, package ordering, website download paths, netinst
payload URLs and retention remain unchanged. Existing published releases are not renamed.
**API impact: none** — this is forge presentation, with no Controller/Core or client-facing
API change. **Guide impact: updated** — the publication guide distinguishes forge filenames
from Debian-form website/package names. **State owner: none** — no new product state.

Patch cutover (2026-10-03): project owner requested a version bump for the forge naming fix.
The newest changelog heading is now **0.4.21**, containing that fix; Unreleased is empty.
This changes source release numbering only, with no tag, push, publication or branch promotion.
**API impact: none** — both release workflows still resolve the version from the changelog;
no Controller/Core or client-facing contract changes. **Guide impact: none** — the documented
publication steps are unchanged. **State owner: none** — no new product state.

Switching a host from Development to Stable never downgrades it: the image engine refuses a `sequence` below the host's
`rollback_floor`. The host stays put until Stable publishes
something newer. The guide says so next to the selector.

## Pipeline changes

- `release.yml` (both forge copies): triggers on `main` and `dev`; resolves channel, version and notes with
  `scripts/release-version.py --channel`; builds the same artifacts; publishes ISOs with
  `release-publish.py --root /srv/nex-downloads[/dev]` so `latest` and the retention count are per channel; bakes the
  channel's own `/downloads[/dev]/<version>` into the netinst ISO; checks reachability of the channel's URLs; deploys
  the static website only for `main`; creates the Gitea release, as a pre-release for `dev`, and prunes older
  development pre-releases.
- The image, release, Phase 1, recovery-libvirt and demo workflows serialize their heavy **run steps**
  with a shared-host `flock`, not a forge concurrency group that can replace a pending run or cancel
  another workflow. Container jobs share `nex-ci-heavy-lock` on their Docker host; native jobs use
  a local lock directory. Use one execution mode on the shared runner: native and container locks
  are separate domains, and different Docker hosts do not share a volume. Checkout/setup actions
  and waiting job containers can coexist; this is not FIFO or whole-job serialization.
  Existing job timeouts still bound queue waiting. Workload termination precedes lock release.
- `phase1.yml`, `image.yml`, `github.yml` also run on pushes to `dev`; pull requests already run for every base.
  `website.yml` stays `main` only. `installer.yml` and `documentation.yml` already run on every branch.
- Owner actions the repository cannot perform: create `dev` from `main` and protect it like `main`; retire the stale
  local `dev` branch and the unused `dev-2` remote branch; consider making `dev` the forge's default branch so
  new pull requests target it.

## Settings

The channel is Core state on each host, and the Controller keeps nothing: it asks the host and shows what the host
reports, so a fresh Controller reads the same value from the hosts.

- `channel=` in `/etc/nex/updates.conf`, reported in `Inventory.image.channel`. `PUT /api/local/v1/updates/settings`
  and the Controller relay already exist; the Controller UI gains an **Update channel** picker on each host's update
  card. Core writes channel and base URL together, so the picker sends the host's current base URL back.
- The Controller appliance is swapped by the Core that hosts it, from that host's channel, so it has no separate
  Controller setting. Decision: project owner, 2026-10-02.
- A development build bakes `dev` as its default channel (decided from the `~dev.` in the version), so a host installed
  from a development ISO starts on Development.
- Pool rollouts keep refusing a pool whose hosts are on different channels (`channel_mismatch`).

API impact: existing contract (the host-local settings route and `Inventory.image.channel` already carry the channel).
State owner: `core`.

## Not in this slice

- Publishing the signed image update tree from CI is plan item 6 of the
  [immutable plan](2026-10-01-immutable-nex-core.md) and waits on the release-key custody decision (its question 3).
  When it lands it takes the channel from `release-version.py --channel` and publishes `stable` from `main`, `dev`
  from `dev`; the `sequence` it allocates must be the next integer of the whole tree, because order is global across
  channels (`check_order`).
- Promotion of one built release between channels without a rebuild (the immutable plan's `promote`) is unchanged and
  unused by this branch model: each branch builds its own release.
- **Update tree publishing (decision: project owner, 2026-10-07, authorizing CI to hold the release key).** The Release
  workflow publishes the signed tree to `/srv/nex-updates` on the website host, served at `<site>/updates/`, and
  `DefaultBaseURL` points every host there. The root key stays offline with the project owner; the release key is the CI
  secret `NEX_UPDATE_RELEASE_KEY_B64`, used on the website host for one command; the signed `trust.json` is the secret
  `NEX_UPDATE_TRUST_B64` and is pinned into every image. The publisher compares labels in their Debian form
  (`0.4.33-dev.294` is `0.4.33~~dev.294`), so its label-order check holds across channels. Published: the slot image, source and compliance manifest.
  Not published: optional layers (redistribution review open) and the Controller guest image, so a pool-wide
  Controller-first rollout is not yet available. `update-publish.py prune` keeps the newest five releases.
