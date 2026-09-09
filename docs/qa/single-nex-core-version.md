# Single Nex Core version — checks

Date: 2026-09-09. Change: Nex Core reports one version (the `nex-core-local` package version);
no separate agent version. Everything below ran on the development workstation; no ISO was
rebuilt and no live host was updated, so this is **labeled simulation** of the package path and
real browser behavior of the Controller UI, not hardware or installer coverage.

## What ran

| Check | Method | Result |
| --- | --- | --- |
| Postinst identity | `tests/test_core_local_package.py::test_postinst_owns_release_identity_from_package_version`: real `dpkg-divert`/`dpkg-query` with `DPKG_ROOT` pointing at a temporary root holding Debian's `/etc/os-release` symlink; configure at `1.2.3~alpha1`, then `1.2.4` | Pass. Symlink diverted once to `/etc/os-release.debian`, `/usr/lib/os-release` untouched, `PRETTY_NAME="Nex Core <version>"` and `VERSION_ID` follow the package, `/etc/motd` and `/etc/issue` first line refreshed |
| Old postinst | Same harness against the `HEAD` postinst | Fails (`install: cannot create directory '/etc/nex'`; os-release still a symlink), so the check detects the previous behavior |
| Real dpkg path | `fakeroot dpkg --root=<tmp> --force-script-chrootless -i` of a probe package carrying this postinst at `0.1.5~alpha1`, then `0.1.6~alpha1` (dpkg 1.23.7) | Pass. dpkg printed `Adding "local diversion of /etc/os-release to /etc/os-release.debian"` once; os-release and motd read `Nex Core 0.1.5~alpha1` then `Nex Core 0.1.6~alpha1` |
| Installer contract | `./installer/check.sh` | Pass; asserts the diversion and identity now live in the postinst, `late-install.sh` requires `ID=nex-core` after package install, and no `0.1.0-alpha1` literal remains in `build.sh`/`late-install.sh` |
| Controller UI | `tests/integration/website_controller_captures.py` (real Controller binary, CSR enrollment, simulated inventory reporting `Nex Core 0.1.4`, Playwright Chromium) | Pass; General view shows `Version  Nex Core 0.1.4` and no `Agent version` row. Inspected `desktop-shell.png`; results in [controller-captures.json](single-nex-core-version/controller-captures.json) |
| Reference and docs | `python3 scripts/generate_api.py --check`; `python3 scripts/check_docs.py --all`; `npm run typecheck`, `npm run build` | Pass |
| Unit suite | `python3 -m unittest discover -s tests -p 'test_*.py'` | 84 tests OK |

## Not verified

- No ISO was rebuilt: the `nex-core-<version>-amd64.iso` name, `.disk/info`, README and
  `BUILD.txt` labels, and `iso-smoke.py`'s new `VERSION_ID`/banner checks are unexecuted.
- No installed Nex Core host took a package update, so the motd/issue refresh on a real host and
  the Controller host page reading a real agent's `os_version` after update remain untested.
- The published apt repository still serves `0.1.4`; the label changes reach hosts with the next
  published package, whose build default is now `0.1.5~alpha1` (Debian `~` pre-release ordering:
  above `0.1.4`, below a final `0.1.5`).
