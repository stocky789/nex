# Publish a Nex Core release

## Status

Available. Every push to `main` on `git.stockhome.com.au` builds the ISO and both Debian
packages at the newest `CHANGELOG.md` version and publishes them as the repository release
for tag `vX.Y.Z[-preN]`, replacing that version's previous release. One version identifies a
release: the changelog heading, the tag and the Debian package version are the same number.
No tag is pushed by hand; the workflow creates or moves it.

If the website deploy secrets are set, the same run also publishes the packages to the signed
APT archive and the ISO to the download tree. [Return to the guide](README.md).

## Prerequisites

- Maintainer access to the Nex repository on `git.stockhome.com.au`. The runner needs Docker;
  it may execute jobs inside a container, since the build locates the workspace on the Docker
  host itself.
- `CHANGELOG.md` has a `## [X.Y.Z-preN]` section for the version being released and it is the
  newest version heading. Entries under `[Unreleased]` are not published: move them under the
  version heading.
- Repository secret `NEX_APPLIANCE_SIGNING_KEY`: the Ed25519 PEM private key that signs the
  Controller appliance manifest, the same key used for local builds. Without it each run signs
  with a throwaway key and warns; the release still installs, but its Controller appliance is
  trusted only by the Core package built in that same run.
- Optional website publishing. Bootstrap the website host once:

  ```sh
  sudo bash packaging/apt/bootstrap.sh
  ```

  This creates the signed APT archive, the `/srv/nex-downloads` tree owned by the publishing
  user, and the two nginx includes. Re-running it is safe. Then set three secrets; without them
  the run says so and publishes to Gitea only.

  | Secret | Value |
  | --- | --- |
  | `NEX_DEPLOY_HOST` | website host address |
  | `NEX_DEPLOY_KEY` | private SSH key for the publishing user on that host |
  | `NEX_DEPLOY_KNOWN_HOSTS` | that host's public key, so the runner pins its identity |

  Optional: `NEX_DEPLOY_USER` (default `matt`) and `NEX_PUBLIC_URL`
  (default `https://nexvirtualization.com`).

## Steps

1. Record the release in `CHANGELOG.md`: keep appending to the newest `## [X.Y.Z-preN]`
   heading while that version is in development, or add a new heading above it to start the
   next version. Its section becomes the release notes.
2. Confirm the version resolves before pushing:

   ```sh
   python3 scripts/release-version.py --current --format both
   ```

   This prints the tag version and the Debian version (`0.1.6-alpha1 0.1.6~alpha1`).
3. Push to `main`. Watch the **Release** workflow: it resolves the version, builds the
   appliance, both packages and the ISO, checks each artifact declares the released version,
   then creates the release. A second push at the same version runs again and replaces the
   release; a newer push cancels a release build still in progress.

## Expected result

The repository's **Releases** page shows `Nex Core X.Y.Z~preN` at tag `vX.Y.Z-preN`, marked
pre-release for a `-preN` version, with the changelog section as its notes and four assets:

- `nex-core-X.Y.Z~preN-amd64.iso` and its `.sha256` sidecar
- `nex-core-local_X.Y.Z~preN_amd64.deb`
- `nex-controller_X.Y.Z~preN_amd64.deb`

The tag points at the commit that built the release. Pushing the same version again deletes
the previous release, moves the tag to the new commit and uploads fresh assets, so a version
has exactly one release and it always matches the newest build.

With deploy secrets set, `https://<site>/apt/` also serves the new packages in the signed
`Release`/`Packages` index, so enrolled hosts see the update under **Updates**, and
`https://<site>/downloads/X.Y.Z~preN/` serves the ISO with `latest` pointing at the newest
release; the three oldest-beyond-three ISO releases are pruned. The workflow then fetches the
published index and the ISO checksum over HTTPS and fails if either is missing or does not
list the released version.

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

`python3 -m unittest discover -s tests -p 'test_*.py'` covers the pieces: `test_release_version.py`
resolves the newest changelog version and its notes; `test_ci_docker.py` shows the Docker wrapper
rewriting workspace bind mounts to the host path and refusing paths outside it; and
`test_gitea_release.py` drives the release script against a stub server, checking it deletes the
old release, moves the tag, creates the release as a pre-release and streams the asset bytes.
`test_release_publish.py` still covers website publishing. All run in CI on GitHub and Gitea
through the existing `test_*.py` discovery.

The workspace-location step and the wrapper were exercised in a simulated container runner
(`docker.gitea.com/runner-images:ubuntu-latest` with the workspace on a named volume and the
host Docker socket): the step reported the volume's host path, and a builder container run
through the wrapper read a file from a temporary input directory and wrote a file into `dist/`.
Raw-body asset uploads named `.iso` and `.deb` were accepted by the live Gitea instance,
including a 150 MB file, and the probe draft release was deleted afterwards.

Evidence for the first end-to-end run is recorded in the change record for this workflow
(`docs/changes/2026-09-10-push-driven-release.json`); until a run is green, treat the build
and publish steps as untested. Website publishing has still never run: no ISO has been
published to the live host, the runner's route to it is unconfirmed, and `bootstrap.sh` has
not been applied there.

The released package embeds the AGPL corresponding-source archive, and that archive was found
to be missing a packaging file ([BUG-0045](../BUG_LEDGER.md)). `tests/test_build_source.py`
cross-checks every packaging path a package builder installs against the archive's allowlist,
so a future omission fails a check rather than a release. Verify the archive contents of the
first package a release actually produces before distributing it.
