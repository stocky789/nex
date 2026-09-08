# Host-package compiler hardening verification — 2026-09-08

## Status

The replacement host package was built with checksum-pinned Go 1.27.1 inside
Debian 13, retaining Debian's C compiler/libc and the installed service restrictions.
The exact extracted package binary passes binary-mode `govulncheck`. The final
clean-container smoke also passed; real host/VM verification is recorded separately.
The previous package and historical QA records were preserved.

## Frozen inputs and artifact

| Input or artifact | SHA256 |
|---|---|
| `dist/core-local/nex-core-local_0.1.0+security1_amd64.deb` — 713,673,436 bytes | `06485e63ee2b05260601062c6307e5d48c167605d6a56bae1bb438f912dae785` |
| `dist/core-local-source-security1.tar.gz` | `a6744c8333d54506ece54629b3e24038c1da0610bea59970740fc0c8e6e43be8` |
| Embedded `dist/appliance-security1/controller.qcow2` | `ae9890a20ff84e112441c30abc0a4a1dedfe4f3898208a8c1619bee9ed66202f` |
| Separate embedded Controller source archive | `958723923c1e144e517229693a7224f2524030fe79830b761bcb79e6c8aba451` |
| Extracted `/usr/lib/nex/nex-core-local` binary | `a0fc17060c66412cbeac642402d83cc203cbf14839dae1a04b2d2a8534c04539` |

The host package includes the final close-on-exec fixes, public-web security and
input validation, with matching generated API references. Comparison of frozen
source against current `cmd/`, `internal/`, UI source/assets, package scripts,
host-recovery helper and API references found no build-input drift. The Controller
appliance retains its separate matching source archive; the newer host snapshot
does not replace that provenance. The [compiler report](compiler.md)
records the separate scan of the actual Controller binary extracted from the signed
image. Appliance signing still uses the explicitly separate lab-only identity; this is not an externally certified release.

## Baseline inspected

The old package was `nex-core-local_0.1.0+lab6.1_amd64.deb`, SHA256
`679197247d98b5130fbdedcf415e0e24ce4235abf1150b3ccc906b7bf0a4304a`.
Its extracted local binary's build information reported `go1.24.4`, `CGO_ENABLED=1`,
`GOARCH=amd64` and `GOOS=linux`.

The inspected binary-mode report identified 28 standard-library vulnerabilities
with matching symbols in that actual old binary. Its scanner version was
`govulncheck@v1.7.0`; database `https://vuln.go.dev` reported an update at
`2026-09-02 19:12:04 UTC`. These are scanner/symbol findings, not demonstrated
exploits against Nex. The baseline scan returned exit status 3.

## Executed compiler and binary checks

The downloaded Go compiler archive passed SHA256 verification against
`63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445`.
The resulting Debian builder reported `go1.27.1 linux/amd64`. The application
build, frontend TypeScript check, frontend build and `.deb` creation all passed.
The seven focused `test_core_local_package.py` checks also passed without skips.

The actual new `.deb` was extracted through Debian's `dpkg-deb --fsys-tarfile`;
no package installation or maintainer scripts ran during extraction. The extracted
binary's `go version -m` reports `go1.27.1`, `CGO_ENABLED=1`, `GOARCH=amd64`,
`GOOS=linux` and `GOAMD64=v1`. The retained service unit still sets `UMask=0077`.

The package includes the compiler's actual BSD-style license at
`/usr/share/doc/nex-core-local/go-LICENSE`. Its SHA256
`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad` matches the
verified toolchain's notice. The package's `go-version.txt` independently records
`/build/nex-core-local: go1.27.1` from the compiled binary itself.

Executed against the extracted binary:

```sh
.cache/security-tools/govulncheck -mode=binary .cache/security-tools/security1-package/usr/lib/nex/nex-core-local
```

The command returned **exit status 0**, with exactly:

```text
No vulnerabilities found.
```

An independent delivery reviewer recomputed the package/source/image/local-binary/
license hashes, inspected the final binary metadata and scan/smoke results, and
compared 55 runtime/UI/package/API source files against the frozen host archive.
The hashes matched, no source drift was found, and no delivery blocker was reported.

## Clean Debian package smoke

The following explicit-package smoke completed in a fresh disposable Debian 13
container with **exit status 0** and no skipped assertions:

```sh
tests/integration/core-local-package.sh --deb dist/core-local/nex-core-local_0.1.0+security1_amd64.deb --docker /tmp/nex-sudo-docker
```

The wrapper executes `sudo -n docker "$@"`. The retained
[smoke script](../../../tests/integration/core-local-package.sh) never installs onto
the workstation and applies `umask 077` inside the container. Actual checks:

- APT installed the new package with its declared runtime dependencies; the native
  service was enabled for first boot and the installed recovery helper loaded.
- Setup discovered the selected container address, generated private credentials
  and a unique TLS certificate, and the installed daemon started successfully.
- The client verified TLS against that IP, authenticated the local administrator,
  retained its session, and loaded the local GUI, source archive and API reference.
- Authenticated host preflight reported the expected 2-vCPU/4096-MiB/32-GiB defaults
  and honest missing-bridge/KVM blockers in the container.
- Credentials were absent from daemon logs and the MOTD.
- Package removal preserved configuration, authentication and a disposable storage
  sentinel. The container was then removed.


## Limits

The scan covers the Go vulnerability database and symbols in the actual compiled
binary. It is not proof of exploit absence or a whole-OS/C-library security audit.
Container checks do not establish real VM deployment, restore integrity, booted
systemd behavior, or release/source-redistribution certification. Those require
separate applicable evidence. No live host or production infrastructure was changed.
