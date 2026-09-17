# Publish a Nex Core release

## Status

The build and publication workflow produces the source bundle, final-ISO compliance manifest,
and publication assets required for a release. The original Nex artwork redistribution basis is
recorded in [ARTWORK-LICENSE.md](../../ARTWORK-LICENSE.md). Public distribution remains open until
the exact final ISO, its matching source bundle, notices, firmware review, source-delivery path,
and hosted publication checks have been inspected and recorded. Do not treat a successful local
build as that evidence.

Once those gates are recorded as complete, every push to `main` on the configured Git forge
builds the ISO and both Debian packages at the newest `CHANGELOG.md` version and publishes them
as the repository release for tag `vX.Y.Z[-preN]`, replacing that version's previous release.
One version identifies a release: the changelog heading, the tag and the Debian package version
are the same number. No tag is pushed by hand; the workflow creates or moves it.

If the website deploy secrets are set, the same run also publishes the packages to the signed
APT archive and the ISO to the download tree. Static website pages are a separate Website
workflow on pushes that change `website/`; see [operate the website](operate-website.md).
[Return to the guide](README.md).

## Prerequisites

- The exact release has completed every external-distribution requirement in
  [Nex licensing](../LICENSING.md#release-evidence), with reviewable evidence. A successful build,
  existing website content or a configured deployment secret does not satisfy this prerequisite.
- Maintainer access to the Nex repository on the configured Git forge. The runner needs Docker;
  it may execute jobs inside a container, since the build locates the workspace on the Docker
  host itself.
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
   then creates the release. A second push at the same version runs again and replaces the
   release; a newer push cancels a release build still in progress.

## Expected result

The repository's **Releases** page shows `Nex Core X.Y.Z` at tag `vX.Y.Z` for a final release, or
the Debian-form `Nex Core X.Y.Z~preN` at tag `vX.Y.Z-preN` for a prerelease. A `-preN` version is
marked prerelease. The changelog section supplies the notes and the release has eight assets:

- `nex-core-<Debian-version>-amd64.iso` and its `.sha256` sidecar
- `nex-core-<Debian-version>-source.tar.gz` and its `.sha256` sidecar
- `nex-core-<Debian-version>-compliance-manifest.json` and its `.sha256` sidecar
- `nex-core-local_<Debian-version>_amd64.deb`
- `nex-controller_<Debian-version>_amd64.deb`

The tag points at the commit that built the release. Pushing the same version again deletes
the previous release, moves the tag to the new commit and uploads fresh assets, so a version
has exactly one release and it always matches the newest build.

With deploy secrets set, `https://<site>/apt/` also serves the new packages in the signed
`Release`/`Packages` index, so enrolled hosts see the update under **Updates**. The publisher
copies the ISO, matching source bundle, and matching compliance manifest into
`https://<site>/downloads/<Debian-version>/`, writes a SHA-256 sidecar for each, and moves
`latest` to the newest retained release; releases beyond the newest three download directories
are pruned. The publisher rejects a manifest unless its recorded ISO and source-bundle filename
and SHA-256 match the files being installed. The workflow then fetches the published APT indexes,
ISO checksum, source bundle, and compliance manifest over HTTPS and fails if any are missing or
the package index does not list the released version.

## Troubleshooting

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
- **"gitea-release: ... HTTP 409"** — the tag is protected or another release still holds it.
  Remove the protection or the stray release and re-run.
- **A throwaway signing key warning** — set the `NEX_APPLIANCE_SIGNING_KEY` secret so every
  release is signed with the operator key.
- **Deploy secrets are not set** — the workflow says so and publishes to Gitea only. Add the
  secrets and re-run, or publish to the website by hand from the release assets.
- **Nothing published and no error** — check the runner can reach the website host; the host
  is on a private address and the runner must be on a network with a route to it.
- **Disk pressure on the website host** — the APT archive keeps every package version and each
  `nex-core-local` is roughly 740 MB because it embeds the Controller appliance image. Only
  ISOs are pruned. Check `du -sh /srv/nex-apt /srv/nex-downloads` before large release runs.

## Verification

`python3 -m unittest discover -s tests -p 'test_*.py'` covers version resolution
(`test_release_version.py`), the Docker wrapper (`test_ci_docker.py`), release publication
(`test_gitea_release.py`, `test_release_publish.py`), source-archive contents
(`test_build_source.py`), whole-media source bundling (`test_source_bundle.py`) and firmware
pruning/index regeneration (`test_prune_iso_packages.py`).

For 0.2.9, the exact final-artifact hashes, final-media inspection, boot/install results, public
website downloads, signed APT metadata and package payloads are recorded in the
[release QA evidence](../qa/2026-09-16-nex-core-0.2.9-release.md). Hosted CI remains pending.
Do not substitute an earlier image, a staging tree, or a successful unit test for those checks.
