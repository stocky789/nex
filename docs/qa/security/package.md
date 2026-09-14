# Host-package compiler hardening verification — 2026-09-08

## Status

Host package built with checksum-pinned Go 1.27.1 inside Debian 13 (Debian C compiler/libc,
service restrictions retained). Extracted binary passes binary-mode `govulncheck`; clean-container
smoke passed. Real host/VM verification: [lab](lab.md). Scanner, Go archive checksum and baseline
method: [compiler checks](compiler.md).

## Frozen inputs and artifact

| Input or artifact | SHA256 |
|---|---|
| `dist/core-local/nex-core-local_0.1.0+security1_amd64.deb` — 713,673,436 bytes | `06485e63ee2b05260601062c6307e5d48c167605d6a56bae1bb438f912dae785` |
| `dist/core-local-source-security1.tar.gz` | `a6744c8333d54506ece54629b3e24038c1da0610bea59970740fc0c8e6e43be8` |
| Embedded `dist/appliance-security1/controller.qcow2` | `ae9890a20ff84e112441c30abc0a4a1dedfe4f3898208a8c1619bee9ed66202f` |
| Separate embedded Controller source archive | `958723923c1e144e517229693a7224f2524030fe79830b761bcb79e6c8aba451` |
| Extracted `/usr/lib/nex/nex-core-local` binary | `a0fc17060c66412cbeac642402d83cc203cbf14839dae1a04b2d2a8534c04539` |

Includes close-on-exec fixes, web security and validation with matching API references. Frozen
source matched current `cmd/`, `internal/`, UI, package scripts, recovery helper and API references
(no drift). Controller appliance keeps its own source archive; lab-only signing identity.

## Baseline inspected

Old `nex-core-local_0.1.0+lab6.1_amd64.deb` SHA256
`679197247d98b5130fbdedcf415e0e24ce4235abf1150b3ccc906b7bf0a4304a`: `go1.24.4`, 28 stdlib symbol
matches, exit 3 (see [compiler checks](compiler.md#before-the-change)).

## Executed compiler and binary checks

| Check | Result |
| --- | --- |
| Go archive checksum; builder version | Pass; `go1.27.1 linux/amd64` |
| App build, frontend typecheck/build, `.deb` creation | Pass |
| 7 `test_core_local_package.py` checks | Pass, no skips |
| `dpkg-deb --fsys-tarfile` extraction, `go version -m` | `go1.27.1`, `CGO_ENABLED=1`, amd64, linux, `GOAMD64=v1`; unit keeps `UMask=0077` |
| `/usr/share/doc/nex-core-local/go-LICENSE` | `911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`, matches toolchain; `go-version.txt` records `/build/nex-core-local: go1.27.1` |
| `.cache/security-tools/govulncheck -mode=binary .cache/security-tools/security1-package/usr/lib/nex/nex-core-local` | **exit 0**, `No vulnerabilities found.` |
| Independent delivery review | Recomputed hashes, compared 55 source files against frozen archive; no drift or blocker |

## Clean Debian package smoke

```sh
tests/integration/core-local-package.sh --deb dist/core-local/nex-core-local_0.1.0+security1_amd64.deb --docker /tmp/nex-sudo-docker
```

**Exit 0**, no skips, fresh disposable Debian 13 container via `sudo -n docker` wrapper
([smoke script](../../../tests/integration/core-local-package.sh), `umask 077`, never installs on
the workstation):

- APT installed with dependencies; service enabled for first boot; recovery helper loaded.
- Setup found container address, generated credentials and unique TLS certificate; daemon started.
- Client verified TLS, authenticated, kept session, loaded GUI, source archive and API reference.
- Preflight reported 2 vCPU/4096 MiB/32 GiB defaults and missing-bridge/KVM blockers.
- No credentials in daemon logs or MOTD.
- Removal preserved configuration, authentication and a storage sentinel; container removed.

## Limits

Go vulnerability database and compiled symbols only; not exploit absence or OS/libc audit.
Container checks do not establish VM deployment, restore, booted systemd or redistribution
certification. No live host changed.
