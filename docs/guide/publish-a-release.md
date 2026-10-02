# Publish a Nex Core release

## Status

The build and publication workflow produces the source bundle, final-ISO compliance manifest,
and publication assets required for a release. The original Nex artwork redistribution basis is
recorded in [ARTWORK-LICENSE.md](../../ARTWORK-LICENSE.md). Public distribution remains open until
the exact final ISO, its matching source bundle, notices, firmware review, source-delivery path,
and hosted publication checks have been inspected and recorded. Do not treat a successful local
build as that evidence.

Once those gates are recorded as complete, every push to `main` on the configured Git forge
builds the full and network installer ISOs and both Debian packages at the newest `CHANGELOG.md` version and publishes the
two ISOs and their SHA-256 sidecars as the repository release for tag `vX.Y.Z[-preN]`, replacing that version's previous
release. The two `.deb` payloads supply Nex Core (including its Controller appliance) and the
custom DLM build to netinst. Core is installed through the installer; Controller is not offered
as a bare package install. Enrolled hosts continue to update through APT. One version identifies a release: the changelog heading, the tag and the
Debian package version are the same number. No tag is pushed by hand; the workflow creates or
moves it.

The same Release run publishes the packages to the signed APT archive, both ISOs, source,
compliance manifests and netinst payloads to the download tree, and the static website pages.
All four website deploy secrets are required because the netinst ISO downloads from that tree. A separate
Website workflow still exists for website-only pushes that skip the ISO build; see
[operate the website](operate-website.md).
[Return to the guide](README.md).

## Release channels

Nex publishes on two channels. **Stable** follows `main`. **Development** follows `dev`, the
branch every feature branch opens its pull request against: it is the staging area, promoted to
`main` by a reviewed pull request. Hosts choose their channel under **Settings → Updates** in
Nex Controller and in the Nex Core host settings; see [software updates](software-updates.md#update-channels).

| | Stable (`main`) | Development (`dev`) |
| --- | --- | --- |
| Version | the newest `CHANGELOG.md` heading, `0.4.18` | a pre-release of the next release, `0.4.19-dev.231` (Debian `0.4.19~dev.231`) |
| Forge release | `v0.4.18`, a release | `v0.4.19-dev.231`, a **pre-release**; the newest three stay |
| APT archive | `<site>/apt/` | `<site>/apt/dev/`: its own pool and indexes, newest three versions kept |
| Downloads | `<site>/downloads/<version>/` and `latest` | `<site>/downloads/dev/<version>/` and its own `latest` |
| Static website | deployed | never deployed |
| Release notes | the version's changelog section | `[Unreleased]`; when the changelog already has a numbered heading ahead of what `main` published (a cut made on `dev`), the notes are that heading's section followed by any entries added under `[Unreleased]` since |

A development build is numbered so it sorts above everything already released and below the
release `main` publishes next. The suffix is the commit count of `dev`, so each merge is a newer
build, and a rebuild of one commit replaces its own release. When the changelog heading for the
next release was cut on `dev` and `main` has not published it yet, builds work toward that
heading; once `main` publishes it they number the next patch release. A `main` that is ahead of
`dev` stops the run with "merge main into dev first", because such a build would sort below a
stable release.

The development archive and downloads sit under the stable ones on the same host, behind the same
signing key and web server, so the web server needs no change and the first development release
creates `/srv/nex-apt/dev` and `/srv/nex-downloads/dev`. The root-run `nex-apt-publish` is the copy
that `packaging/apt/bootstrap.sh` installed, and development publishing needs its `--keep` option:
re-run the bootstrap on the website host once before the first push to `dev` (stable publishing keeps
working with the old copy). A netinst ISO fetches its payloads from its own channel's tree.

## Prerequisites

- The exact release has completed every external-distribution requirement in
  [Nex licensing](../LICENSING.md#release-evidence), with reviewable evidence. A successful build,
  existing website content or a configured deployment secret does not satisfy this prerequisite.
  Do not configure deployment or publish a new release until evidence is recorded for the
  exact version: the `debian/copyright`
  and matching source for
  `ipmitool` and `sbd` are still marked outstanding in
  [Nex licensing](../LICENSING.md#release-evidence), which records exact-artifact evidence for
  `0.2.9` only.
- Maintainer access to the Nex repository on the configured Git forge. The runner needs Docker;
  it may execute jobs inside a container, since the build locates the workspace on the Docker
  host itself. Builder containers keep a `nex-build-cache` volume on that Docker host: the
  pinned Debian packages and source archives, the Windows driver media, the Go build cache and
  the npm cache survive between runs, so a rebuild at unchanged pins spends its time building
  rather than downloading. The volume grows by a few gigabytes each time a snapshot pin
  changes; remove it with `docker volume rm nex-build-cache` on the runner host to reclaim
  the space or to force every input to be fetched again. The pinned Debian cloud image is
  restored from the forge's cache server the same way.
- `CHANGELOG.md` has a `## [X.Y.Z]` or `## [X.Y.Z-preN]` section for the version being released and it is the
  newest version heading. Day-to-day work lives under `[Unreleased]`; before tagging, move those
  entries under the version heading and leave `[Unreleased]` empty (blockquote guidance may remain).
- Repository secret `NEX_APPLIANCE_SIGNING_KEY`: the Ed25519 PEM private key that signs the
  Controller appliance manifest, the same key used for local builds. Without it each run signs
  with a throwaway key and warns; the release still installs, but its Controller appliance is
  trusted only by the Core package built in that same run.
- Website publishing requires a configured publishing account. Bootstrap the website host once:

  ```sh
  sudo NEX_PUBLISH_USER=<publishing-user> packaging/apt/bootstrap.sh
  ```

  This creates the signed APT archive, the `/srv/nex-downloads` tree owned by the publishing
  user, and the two nginx includes. `NEX_PUBLISH_USER` is required. Re-run the updated
  bootstrap on an existing website host before publishing the short download pages;
  it installs the HTML content type and reloads nginx. Then
  set the deployment secrets; without them the run says so and publishes to Gitea only.

  | Secret | Value |
  | --- | --- |
  | `NEX_DEPLOY_HOST` | website host address |
  | `NEX_DEPLOY_USER` | publishing account created by `NEX_PUBLISH_USER` |
  | `NEX_DEPLOY_KEY` | private SSH key for the publishing user on that host |
  | `NEX_DEPLOY_KNOWN_HOSTS` | that host's public key, so the runner pins its identity |

  `NEX_PUBLIC_URL` is optional and defaults to `https://nexvirtualization.com`.

## Steps

1. Cut the release in `CHANGELOG.md`: add a new `## [X.Y.Z]` or `## [X.Y.Z-preN]` heading above the
   previous numbered section (or reuse the newest numbered heading only when deliberately
   republishing that same version), move every entry out of `[Unreleased]` under it, and leave
   `[Unreleased]` empty. That numbered section becomes the release notes. Do not leave shipping
   notes only under `[Unreleased]`.
2. Confirm the version resolves before pushing:

   ```sh
   python3 scripts/release-version.py --current --format both
   ```

   This prints the tag version and Debian version. A final release prints the same value twice
   (`0.2.9 0.2.9`); a prerelease converts the tag suffix to Debian ordering
   (`0.1.6-alpha1 0.1.6~alpha1`).
3. After the exact-artifact evidence is complete, push to `main` (stable) or merge into `dev`
   (development; promote it to stable with a pull request from `dev` to `main` that also cuts the
   changelog heading). Watch the **Release** workflow: it resolves the channel and version, builds the
   appliance, both packages and both ISOs, checks artifact versions and source/payload bindings,
   builds the static website, publishes packages to APT and both ISOs, source, manifests
   and netinst payloads to the download tree, verifies those downloads, then creates the
   forge release. A failing website
   build or a missing deploy secret stops the run before the tag moves. A second push at the
   same version runs again and replaces the release; a newer push cancels a release build
   still in progress.
   The artifact verification step prints each output's byte size so packaging growth
   is visible before publication. The netinst ISO embeds the configured website download URL.

## Expected result

The repository's **Releases** page shows `Nex Core X.Y.Z` at tag `vX.Y.Z` for a final release, or
the Debian-form `Nex Core X.Y.Z~preN` at tag `vX.Y.Z-preN` for a prerelease. A `-preN` version is
marked prerelease. The changelog section supplies the notes. The release includes:

- `nex-core-<Debian-version>-amd64.iso` and its `.sha256` sidecar
- `nex-core-<Debian-version>-amd64-netinst.iso` and its `.sha256` sidecar

The versioned website download directory holds the matching source bundle,
compliance manifests and hash-pinned netinst `.deb` files with checksums. Opening
`/downloads/latest/` or a version directory shows installer links, checksums and
source/compliance links without listing the network-installer packages. The packages
remain available at their exact URLs inside the network installer.

The tag points at the commit that built the release. Pushing the same version again deletes
the previous release, moves the tag to the new commit and uploads fresh assets, so a version
has exactly one release and it always matches the newest build. Each asset is checked against
its size on disk when the forge reports a stored size, and an upload the forge drops part-way through is resent
rather than failing the run. On Gitea, the replacement stays a draft until every asset passes
that check, then is published. A handled upload failure deletes the draft; the previous
release has already been removed, so rerun publication to restore that version.

`https://<site>/apt/` also serves the new packages in the signed
`Release`/`Packages` index, so enrolled hosts see the update under **Updates**. The publisher
copies both ISOs, their shared source bundle, separate manifests and netinst payloads into
`https://<site>/downloads/<Debian-version>/`, writes a SHA-256 sidecar for each, and moves
`latest` to the newest retained release; releases beyond the newest three download directories
are pruned. An older netinst ISO stops working if its payloads are pruned or replaced by a
same-version rebuild; use the current matching ISO. The publisher rejects mismatched ISO,
source-bundle or netinst payload bindings. The compliance manifest inventories ISO
components for licensing; it does not enable or block publication by package name. The workflow
then fetches the published APT indexes,
ISO checksums, source bundle, manifests and netinst payload availability over HTTPS and fails if any are missing or
the package index does not list the released version.

Development releases are the same files at their own locations: `https://<site>/apt/dev/` is the
APT repository (suite `nex`, component `main`, the same signing key), and
`https://<site>/downloads/dev/latest/` is the newest build. Each keeps the newest three builds,
and the final check verifies the development URLs, not the stable ones.

## Troubleshooting

- **Publication succeeds but the final check returns 401** — the configured public
  URL requires authentication from the runner. Verify `NEX_PUBLIC_URL` points to
  the intended public download/APT site, and check that site's proxy/authentication
  routing. Do not rerun the full build just to diagnose it: inspect the already
  published `/apt/dists/nex/InRelease`, package index and versioned downloads.
  The final workflow check uses HEAD for reachability, then reads the small package
  index to verify the version. It does not download the multi-gigabyte source archive
  or establish a complete downloaded-artifact integrity check.

- **The final check reports "/ returned 503"** — the site was still settling from the publish
  a moment earlier. The check retries each request for about thirty seconds before failing; a
  503 that outlasts that is a real outage on the website host.
- **"main already published X, newer than dev's Y; merge main into dev first"** — a release
  went to `main` that `dev` does not contain, typically a hotfix. Merge `main` into `dev` and
  push; the next development build numbers above that release.
- **"nex-apt-publish: error: unrecognized arguments: --keep 3"** on a development run — the helper
  installed on the website host predates channels. Re-run `packaging/apt/bootstrap.sh` there (see
  [Release channels](#release-channels)); nothing was published, so re-run the workflow afterwards.
- **A development merge cancelled a stable release still building** — both channels share one
  concurrency group on the single runner, and a newer run cancels the one in progress. Re-run
  the Release workflow on `main`.
- **"CHANGELOG.md has no version headings"** — the changelog lost its `## [X.Y.Z]` headings;
  restore the newest version's section.
- **"declares <version>, expected <version>"** — a package was built at a different version
  than the changelog. Re-run the workflow; do not publish the mismatched artifact.
- **"workspace ... is neither a Docker host path nor a Docker volume"** — the runner keeps the
  workspace somewhere its Docker daemon cannot mount. Give `act_runner` a host label
  (`ubuntu-latest:host` in its `config.yaml`) or let it mount the workspace as a volume.
- **"ci-docker: refusing to mount ..."** — a build script tried to bind-mount a path outside
  the workspace, which the Docker host cannot see. Build inputs must live under the checkout;
  the workflow points `TMPDIR` there for that reason.
- **The Release run is slow again** — the runner's Docker host lost the `nex-build-cache`
  volume (a `docker volume prune`, a rebuilt runner) or a snapshot pin changed, so the first
  run fetches everything from `snapshot.debian.org`, which is slow per connection. The next run
  at the same pins reuses it.
- **"gitea-release: ... HTTP 409"** — the tag is protected or another release still holds it.
  Remove the protection or the stray release and re-run.
- **"gitea-release: <asset> attempt N failed: ..."** — the connection to the forge dropped while
  that asset was uploading, which a multi-gigabyte ISO invites. The workflow deletes whatever the
  attempt registered and resends the whole file, up to three attempts for transport/server
  failures; rejected requests fail immediately. If it fails, the Gitea publisher attempts to delete the draft: fix the path to the
  forge and rerun the same version. If cleanup also fails or the runner is terminated, inspect
  and remove any leftover draft before retrying.
- **"<asset>: stored N of M bytes"** — the forge accepted the upload but kept a short file. The
  release is failed on purpose rather than published with a download that cannot be installed.
  Check the forge's upload size limit and any proxy in front of it.
- **Repeated "EOF occurred in violation of protocol" during ISO upload** — the TLS connection
  ended before the upload completed; this message alone does not identify which server or proxy
  closed it. Check their logs, request-body limits and timeouts. Gitea's release limit is
  `[repository.release] FILE_MAX_SIZE`, not `[attachment] MAX_SIZE`; its documented default is
  2048 MB, still below the optimized ISO and matching source bundle measured in
  [packaging verification](../qa/2026-09-18-iso-packaging-efficiency.md).
  See the [Gitea configuration reference](https://docs.gitea.com/administration/config-cheat-sheet/#repository---release-repositoryrelease).
  Confirm the actual configured limit and upload route before retrying; more retries cannot
  bypass a fixed size limit.
- **A throwaway signing key warning** — set the `NEX_APPLIANCE_SIGNING_KEY` secret so every
  release is signed with the operator key.
- **Deploy secrets are not set** — the workflow stops before creating a release because
  the netinst ISO requires the website's package downloads. Configure all four secrets
  after the exact-artifact distribution review is complete.
- **Nothing published and no error** — check the runner can reach the website host; the host
  is on a private address and the runner must be on a network with a route to it.
- **Disk pressure on the website host** — the stable APT archive keeps every package version and
  each `nex-core-local` embeds the Controller appliance image; the packaging check records its
  actual size. The development archive keeps the newest three versions, and each channel's
  download tree prunes to the newest three releases (including their netinst payloads). Check
  `du -sh /srv/nex-apt /srv/nex-downloads` before large release runs.

## Verification

The 0.3.3 Gitea draft-publication success, dropped connection, rejected upload and short-upload
paths passed against a local simulated forge; this does not establish hosted publication. See
[workflow regression evidence](../qa/2026-09-18-workflow-regressions.md).

`python3 -m unittest discover -s tests -p 'test_*.py'` covers version resolution
(`test_release_version.py`), the Docker wrapper (`test_ci_docker.py`), release publication
(`test_gitea_release.py`, `test_release_publish.py`), source-archive contents
(`test_build_source.py`), whole-media source bundling (`test_source_bundle.py`) and firmware
pruning/index regeneration (`test_prune_iso_packages.py`).

For 0.2.9, the exact final-artifact hashes, final-media inspection, boot/install results, public
website downloads, signed APT metadata and package payloads are recorded in the
[release QA evidence](../qa/2026-09-16-nex-core-0.2.9-release.md). Hosted CI remains pending.
Do not substitute an earlier image, a staging tree, or a successful unit test for those checks.

For 0.3.1, see the [exact-artifact release report](../qa/2026-09-17-nex-core-0.3.1-release.md)
for rebuilt packages, matching sources/notices, BIOS installation and reboot, both firmware
boot menus, signed APT and public delivery checks. This was direct website/APT publication;
no Git-forge release or hosted CI completion is implied.

For the development 0.4.12 netinst changes, see [network-installer verification](../qa/iso/2026-09-24-net-installer.md). Local publication fixtures do not establish a hosted release run.

The download-page regression checks cover generated installer/source
links, hidden package names, retained-version backfill, and nginx HTML delivery while
exact package URLs remain reachable. The live website and forge release require a new
owner-published run and separate inspection.
