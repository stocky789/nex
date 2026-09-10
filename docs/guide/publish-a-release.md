# Publish a Nex Core release

## Status

Available for authorized maintainers. Tagging `vX.Y.Z` on `git.stockhome.com.au` builds the
ISO and both Debian packages at the tagged version and publishes them to the website: packages
to the signed APT archive, the ISO to the download tree. One version identifies a release, and
the build refuses to start unless the tag and `CHANGELOG.md` agree.

The workflow has not yet run a real release; it is written against the existing manual steps
and the artifacts they produce. Current verification and outstanding limits are below.
[Return to the guide](README.md).

## Prerequisites

- Maintainer access to the Nex repository on `git.stockhome.com.au`, with a runner able to run
  Docker and reach the website host.
- `CHANGELOG.md` has a `## [X.Y.Z-preN]` section for the version being released, it is the
  newest version heading, and `[Unreleased]` holds no entries.
- The website host has been bootstrapped for publishing:

  ```sh
  sudo bash packaging/apt/bootstrap.sh
  ```

  This creates the signed APT archive, the `/srv/nex-downloads` tree owned by the publishing
  user, and the two nginx includes. Re-running it is safe.
- Three repository secrets. Without them the workflow still builds and uploads artifacts and
  publishes nothing, so a tag can be built and inspected before deployment is wired up.

  | Secret | Value |
  | --- | --- |
  | `NEX_DEPLOY_HOST` | website host address |
  | `NEX_DEPLOY_KEY` | private SSH key for the publishing user on that host |
  | `NEX_DEPLOY_KNOWN_HOSTS` | that host's public key, so the runner pins its identity |

  Optional: `NEX_DEPLOY_USER` (default `matt`) and `NEX_PUBLIC_URL`
  (default `https://nexvirtualization.com`).

## Steps

1. Move any `[Unreleased]` entries under a new version heading in `CHANGELOG.md`, in the four
   standard buckets, and commit.
2. Confirm the version resolves before tagging:

   ```sh
   python3 scripts/release-version.py --tag vX.Y.Z-preN --format both
   ```

   This prints the tag version and the Debian version (`0.1.6-alpha1 0.1.6~alpha1`), or the
   exact reason it refuses.
3. Tag and push:

   ```sh
   git tag vX.Y.Z-preN
   git push origin vX.Y.Z-preN
   ```

4. Watch the **Release** workflow. It resolves the version, builds the appliance, both packages
   and the ISO, checks each artifact declares the released version, uploads them, then publishes.

## Expected result

The tag builds and publishes one version everywhere:

- `https://<site>/apt/` serves the new packages in the signed `Release`/`Packages` index, so
  enrolled hosts see the update under **Updates**.
- `https://<site>/downloads/X.Y.Z~preN/` serves the ISO and its `.sha256` sidecar, and
  `https://<site>/downloads/latest/` points at the newest release.
- The three oldest-beyond-three ISO releases are pruned; packages are retained.

The workflow then fetches the published index and the ISO checksum over HTTPS and fails if
either is missing or does not list the released version. A successful publish command is not
treated as proof the artifact is served.

## Troubleshooting

- **"tag ... is not the newest CHANGELOG version"** — the tag names an older release.
  Publishing it would put a package below what hosts already have. Tag the newest version.
- **"CHANGELOG.md has no '## [X.Y.Z]' section"** — add the section, commit, re-tag.
- **"[Unreleased] still has entries"** — move them under the version heading.
- **"declares <version>, expected <version>"** — a package was built at a different version
  than the tag. Re-run the workflow; do not publish the mismatched artifact.
- **"This runner cannot bind-mount the workspace"** — the runner executes jobs inside a
  container, so the ISO build would mount an empty directory. Give `act_runner` a host
  label (`ubuntu-latest:host` in its `config.yaml`) and restart it. This currently blocks
  every tagged release; see [BUG-0043](../BUG_LEDGER.md).
- **Deploy secrets are not set** — the workflow says so and uploads artifacts without
  publishing. Add the secrets and re-run, or publish by hand from the artifacts.
- **Nothing published and no error** — check the runner can reach the website host; the host
  is on a private address and the runner must be on a network with a route to it.
- **Disk pressure on the website host** — the APT archive keeps every package version and each
  `nex-core-local` is roughly 740 MB because it embeds the Controller appliance image. Only
  ISOs are pruned. Check `du -sh /srv/nex-apt /srv/nex-downloads` before large release runs.

## Verification

`python3 -m unittest discover -s tests -p 'test_release_version.py'` and
`-p 'test_release_publish.py'` pass: version resolution refuses a malformed tag, a tag absent
from the changelog, an older-than-newest tag and a dirty `[Unreleased]`; ISO publishing writes a
verifiable `sha256sum -c` sidecar, replaces a republished version cleanly, prunes to the newest
three in dpkg order, refuses a symlinked or misnamed ISO, and leaves no partial release when a
publish fails. Both run in CI on GitHub and Gitea through the existing `test_*.py` discovery.

The nginx include insertion was exercised against a copy of the live server block shape, and the
workflow YAML was parsed and its step gating checked.

Not yet verified: no tag has been pushed, so the workflow has never run end to end; no ISO has
been published to the live host; the runner's network route to the website host is unconfirmed;
and the `bootstrap.sh` changes have not been applied to the live host.
