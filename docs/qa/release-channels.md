# Release channels: verification

Date: 2026-10-02  
Scope: [release channels plan](../plans/2026-10-02-release-channels.md): stable and development versioning, the
Release workflow for `main` and `dev`, the download tree and Gitea pre-release, the
host channel setting and the channel picker. Workstation checks only; nothing here ran on a forge runner, a
website host or a real Nex Core host.

## Environment

Linux workstation, Python 3.14, Go 1.27.1, Node 24 with the repository's locked web dependencies. `dpkg` was real.
The workflow steps ran under `bash -euo pipefail` with `ssh`, `scp` and `python3` replaced by recording stand-ins and a
local HTTP server standing in for the public site.

## Results

| Check | Result |
|---|---|
| `python3 -m unittest discover -s tests -p 'test_*.py'` | `test_host_tool_isolation` passes for `internal/controller`, which installs the refusing `virsh` stub now that it reaches `vmhost` through `internal/nexupdate`. Notes of a development build are the numbered heading's entries when that heading was cut on `dev` and is not yet on `main`, else `[Unreleased]`. |
| `tests/test_release_version.py` | A build after a published release numbers the next patch (`0.4.19-dev.231`, Debian `0.4.19~dev.231`); a heading cut on `dev` is the release being built; `main` ahead of `dev` is refused; the version sorts above the last release and below the next under `dpkg --compare-versions`; notes are `[Unreleased]` without the maintainer block. |
| `tests/test_ci_security.py` `ReleaseChannelTests` (both forge copies) | The version step run in a throwaway git checkout gives `v0.4.18` / `downloads` for `main`, `v0.4.19-dev.2` / `downloads/dev` for `dev`, and refuses any other branch. The publish step issues `release-publish.py --root /srv/nex-downloads/dev` for `dev` only and deploys the website only for stable. The release step passes `--prune-tags=…` and the `(development)` name for `dev` only, with a pattern that matches no stable tag, and the real `scripts/gitea-release.py` parses the arguments (an independent review found that a separate leading-dash value, `--prune-tags '-dev…'`, is rejected by argparse and would have failed every development run; the `=` form fixes it and the test now runs the real parser). |
| `tests/test_release_reachability.py` | The reachability step passes, and still fails on 401 and a missing version, against both channels' URLs. |
| `tests/test_release_publish.py` | Development builds order numerically (`~dev.999` < `~dev.1000`); the development root keeps its own `latest` and retention while nested in the stable root; the download page says it is a development channel build. |
| `tests/test_gitea_release.py` | Pruning keeps the newest three development releases across more than one listing page (the published one included), deletes release and tag, and never selects a stable release. |
| `go test ./internal/nexupdate ./internal/localcore ./internal/controller ./internal/protocol` | Pass, including the channel tests: invalid channel, a `base_url` and an environment override are refused without touching the file; the host-local route does the same end to end. |
| `web`: `node src/channel.test.mjs`, `node src/image-updates.test.mjs`, `tsc --noEmit` | Pass. The settings request carries the host's `base_url` and keeps its identity on retry; the picker preselects what the host reports, keeps an unknown channel selectable and is disabled while the host is away or running an operation. |
| `python3 scripts/generate_api.py --check`, `tests/test_api_contract.py` | Current and passing after the `docs/api/local-openapi.json` change. |

## Not exercised

- A hosted Release run on either forge, the Gitea API, the website host, nginx on it, or the live signing key.
- An installed host switching channel through the new selector or updating from it.
- A browser walkthrough of either selector, and the website build (its dependencies are not installed here).
- Signed image update tree publishing for `dev`; nothing publishes that tree yet (see the plan).
- Whether the website host's download tree accepts the first development release: nothing was published.
