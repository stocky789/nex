# Publish a Nex Core release

## Status

The build and publication workflow produces the source bundle, final-ISO compliance manifest,
and publication assets required for a release. The original Nex artwork redistribution basis is
recorded in [ARTWORK-LICENSE.md](../../ARTWORK-LICENSE.md). Public distribution remains open until
the exact final ISO, its matching source bundle, notices, firmware review, source-delivery path,
and hosted publication checks have been inspected and recorded. Do not treat a successful local
build as that evidence.

Once those gates are recorded as complete, every push to `main` on the configured Git forge
builds the ISO and both Debian packages at the newest `CHANGELOG.md` version and publishes the
ISO, matching source bundle and compliance manifest as the repository release for tag
`vX.Y.Z[-preN]`, replacing that version's previous release. Standalone `.deb` files are not
release downloads: Core is installed from the ISO, and Controller is not offered as a bare
package install. Packages are still built so the ISO can install Core and so enrolled hosts can
update from APT. One version identifies a release: the changelog heading, the tag and the
Debian package version are the same number. No tag is pushed by hand; the workflow creates or
moves it.

If the website deploy secrets are set, the same Release run publishes the packages to the signed
APT archive, the ISO and source to the download tree, and the static website pages. A separate
Website workflow still exists for website-only pushes that skip the ISO build; see
[operate the website](operate-website.md).
[Return to the guide](README.md).

## Prerequisites

- The exact release has completed every external-distribution requirement in
  [Nex licensing](../LICENSING.md#release-evidence), with reviewable evidence. A successful build,
  existing website content or a configured deployment secret does not satisfy this prerequisite.
  The current working version, `0.4.8`, may publish to the private forge for internal labs.
  CI no longer blocks public publication, so leave the deploy secrets unset until that
  evidence is recorded for this exact version: the `debian/copyright` and matching source for
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
  newest version heading. Entries under `[Unreleased]` are not published: move them under the
  version heading.
- Repository secret `NEX_APPLIANCE_SIGNING_KEY`: the Ed25519 PEM private key that signs the
  Controller appliance manifest, the same key used for local builds. Without it each run signs
  with a throwaway key and warns; the release still installs, but its Controller appliance is
  trusted only by the Core package built in that same run.
- Website publishing requires a configured publishing account. Bootstrap the website host once:

  ```sh
  sudo NEX_PUBLISH_USER=<publishing-user> packaging/apt/bootstrap.sh
  ```

  This creates the signed APT archive, the `/srv/nex-downloads` tree owned by the publishing
  user, and the two nginx includes. `NEX_PUBLISH_USER` is required. Re-running it is safe. Then
  set the deployment secrets; without them the run says so and publishes to Gitea only.

  | Secret | Value |
  | --- | --- |
  | `NEX_DEPLOY_HOST` | website host address |
  | `NEX_DEPLOY_USER` | publishing account created by `NEX_PUBLISH_USER` |
  | `NEX_DEPLOY_KEY` | private SSH key for the publishing user on that host |
  | `NEX_DEPLOY_KNOWN_HOSTS` | that host's public key, so the runner pins its identity |

  `NEX_PUBLIC_URL` is optional and defaults to `https://nexvirtualization.com`.

## Steps

1. Record the release in `CHANGELOG.md`: keep appending to the newest `## [X.Y.Z]` or `## [X.Y.Z-preN]`
   heading while that version is in development, or add a new heading above it to start the
   next version. Its section becomes the release notes.
2. Confirm the version resolves before pushing:

   ```sh
   python3 scripts/release-version.py --current --format both
   ```

   This prints the tag version and Debian version. A final release prints the same value twice
   (`0.2.9 0.2.9`); a prerelease converts the tag suffix to Debian ordering
   (`0.1.6-alpha1 0.1.6~alpha1`).
3. After the exact-artifact evidence is complete, push to `main`. Watch the **Release** workflow: it resolves the version, builds the
   appliance, both packages and the ISO, checks each artifact declares the released version,
   builds the static website when deploy secrets are set, then creates the release and
   publishes packages to APT, the ISO to the download tree and those pages. A failing website
   build or a half-set deploy secret stops the run before the tag moves. A second push at the
   same version runs again and replaces the release; a newer push cancels a release build
   still in progress.
   The artifact verification step prints each output's byte size so packaging growth
   is visible before publication. Transfer compression of bundled Windows media
   changes neither the restored driver ISO nor the release's six-asset layout.

## Expected result

The repository's **Releases** page shows `Nex Core X.Y.Z` at tag `vX.Y.Z` for a final release, or
the Debian-form `Nex Core X.Y.Z~preN` at tag `vX.Y.Z-preN` for a prerelease. A `-preN` version is
marked prerelease. The changelog section supplies the notes and the release has six assets:

- `nex-core-<Debian-version>-amd64.iso` and its `.sha256` sidecar
- `nex-core-<Debian-version>-source.tar.gz` and its `.sha256` sidecar
- `nex-core-<Debian-version>-compliance-manifest.json` and its `.sha256` sidecar

Standalone `nex-core-local_*.deb` and `nex-controller_*.deb` files are not attached.

The tag points at the commit that built the release. Pushing the same version again deletes
the previous release, moves the tag to the new commit and uploads fresh assets, so a version
has exactly one release and it always matches the newest build. Each asset is checked against
its size on disk when the forge reports a stored size, and an upload the forge drops part-way through is resent
rather than failing the run. On Gitea, the replacement stays a draft until every asset passes
that check, then is published. A handled upload failure deletes the draft; the previous
release has already been removed, so rerun publication to restore that version.

With deploy secrets set, `https://<site>/apt/` also serves the new packages in the signed
`Release`/`Packages` index, so enrolled hosts see the update under **Updates**. The publisher
copies the ISO, matching source bundle, and matching compliance manifest into
`https://<site>/downloads/<Debian-version>/`, writes a SHA-256 sidecar for each, and moves
`latest` to the newest retained release; releases beyond the newest three download directories
are pruned. The publisher rejects a manifest unless its recorded ISO and source-bundle filename
and SHA-256 match the files being installed. The compliance manifest inventories ISO
components for licensing; it does not enable or block publication by package name. The workflow
then fetches the published APT indexes,
ISO checksum, source bundle, and compliance manifest over HTTPS and fails if any are missing or
the package index does not list the released version.

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
- **Deploy secrets are not set** — the workflow says so and publishes to Gitea only. Add the
  secrets and re-run, or publish the ISO and source from the release assets and the packages
  through `nex-apt-publish` on the website host.
- **Nothing published and no error** — check the runner can reach the website host; the host
  is on a private address and the runner must be on a network with a route to it.
- **Disk pressure on the website host** — the APT archive keeps every package version and each
  `nex-core-local` embeds the Controller appliance image; the packaging check records its actual size. Only
  ISOs are pruned. Check `du -sh /srv/nex-apt /srv/nex-downloads` before large release runs.

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
