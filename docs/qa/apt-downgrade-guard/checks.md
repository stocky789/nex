# APT storage-downgrade guard checks

Date: 2026-09-15. BUG-0187. Reproduced and verified in a disposable `debian:13` container with
the distribution's own `apt-get` (APT 3.0), not a simulation of it. No Nex host was modified.

## Reproduction

An operator reported that upgrading Nex Core failed after the download with
`nex-core-local: refusing APT downgrade while version-2 storage state is retained`, even though
the target package is newer and does carry the marker:

    $ dpkg-deb -c dist/core-local/nex-core-local_0.2.3_amd64.deb | grep storage-format
    -rw-r--r-- root/root  2 ./usr/share/nex/core-local/storage-format-v2

The harness builds two stand-in `nex-core-local` packages (0.2.2 and 0.2.3, both carrying the
marker and the real guard script), serves them from a local file repository, installs 0.2.2,
creates the version-2 storage state the guard protects
(`/var/lib/nex-core-agent/managed-vms/storage.json`), installs the same
`DPkg::Pre-Install-Pkgs` hook the package's postinst writes, and upgrades. A second hook
captures what APT hands the guard.

Captured hook input — the cause, one line per dpkg **action**:

    nex-core-local 0.2.2 amd64 none < 0.2.3 amd64 none /work/repo/./nex-core-local_0.2.3_amd64.deb
    nex-core-local 0.2.2 amd64 none < 0.2.3 amd64 none **CONFIGURE**

Before the fix the upgrade failed exactly as reported:

    nex-core-local: refusing APT downgrade while version-2 storage state is retained; upgrade to a storage-format-v2 package instead
    E: Sub-process /usr/lib/nex/storage-downgrade-guard --core returned an error code (1)
    apt exit=100

## After the fix

Same container, same state, same hook:

- Upgrade 0.2.2 → 0.2.3: `Unpacking nex-core-local (0.2.3) over (0.2.2)`, `apt exit=0`.
- Downgrade to a 0.1.9 package built **without** the `storage-format-v2` marker: still refused
  with the same message, `downgrade exit=100`, and `dpkg-query` reports `installed=0.2.3`, so the
  protection the guard exists for is intact.
- `python3 -m unittest discover -s tests -p 'test_storage_packaging.py'`: 14 tests OK. With the
  guard reverted the suite fails one test, the new APT-payload case.

## Not covered

- No real Nex Core host was upgraded: the reproduction uses stand-in packages of the same name so
  the container needs no 716 MB download, and it exercises the hook protocol and the guard, not a
  Core service restart.
- The Controller package installs the same script (`--controller`), so it had the same defect and
  takes the same fix; that path was not separately exercised against real apt.
- The published 0.2.3 package still contains the broken guard. Hosts already carrying it must use
  the recovery in [software updates](../../guide/software-updates.md), because the guard that decides
  an upgrade is the installed one.
