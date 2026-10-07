# Debian builder compiler verification — 2026-09-08

## Scope and method

Inspects the Controller binary from `dist/appliance-lab6/controller.qcow2` and the local binary
from `dist/core-local/nex-core-local_0.1.0+lab6.1_amd64.deb`, then the updated compiler and final
Controller image. Disposable development artifacts; not release certification.

Scanner `govulncheck v1.7.0`, database `https://vuln.go.dev` updated `2026-09-02 19:12:04 UTC`,
from `.cache/security-tools/`, plus `go version -m`. Binary mode can match unreachable symbols
([scanner limitations](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck#hdr-Limitations)).
Host package results page removed; see git history.

## Before the change

Both binaries: **go1.24.4**, `CGO_ENABLED=1`, Linux/amd64, `GOAMD64=v1`.

| Actual extracted binary | SHA256 | Binary advisory result |
| --- | --- | --- |
| Controller from lab6 image | `a4d364b8fd3be68f2d118552bcbb3ff01b28c3e7ee689422f10c80afc77a7804` | Exit 3; 25 standard-library advisory symbol matches |
| Local service from lab6.1 package | `19989230104d4239ce2f7e4c6a4367ab6566d08cb292980e065fb53df275c0a2` | Exit 3; 28 standard-library advisory symbol matches |

Old builder: `golang-1.24-go=1.24.4-1`, `golang-1.24-src=1.24.4-1`, `golang-go=2:1.24~2`; changelog
ends at the 2025-06-16 revision with no later security backport, so patch level was not inferred
from the version string. Local-only evidence in `.cache/security-tools/`: `old-controller-buildinfo.txt`,
`old-controller-govulncheck.txt`, `old-local-buildinfo.txt`, `old-local-govulncheck.txt`,
`old-debian-go-package.txt`, `old-binary-audit.json` (ignored, not distributed).

## Build change and focused checks

Both Debian Dockerfiles download Go **1.27.1 linux/amd64**, verify SHA256
`63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445`
([official metadata](https://go.dev/dl/?mode=json)) **before extracting**, and set
`GOTOOLCHAIN=local`. Docker output: `/tmp/go.tar.gz: OK`, `go version go1.27.1 linux/amd64`.
C compiler and libc remain Debian 13.

`python3 -Werror::ResourceWarning -m unittest discover -s tests -p test_appliance_build.py`:
**10 tests passed**, including tampered-fixture checksum failure, matching-digest success, and
firstboot 16-code-point minimum, 1024-UTF-8-byte maximum and NUL/CR/LF rejection. Both Actions
configurations discover this suite; no hosted run.

Guest Go notice `/usr/local/share/nex/go-LICENSE` SHA256
`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad` matches
`third_party/go/LICENSE` in the retained source archive.

## Compiler-only comparison

Each original binary's retained source rebuilt with Go 1.27.1 (`go build -mod=vendor -trimpath`),
without later authentication changes.

| Comparison | Exact input source SHA256 | New binary SHA256 | Actual scan |
| --- | --- | --- | --- |
| Controller, original lab6 image source | `d4fc22c13c6f160f03f59fe65892b10182045de5c6ba2b6a7c3d5dc3ce8221f3` | `6203421d745c54c14095a87e19073c2f25005c6e16920aaa09d006e882f23b52` | Exit 0; no vulnerabilities found |
| Local service, source extracted from original lab6.1 package | `b4228bcd0aa3e5f8143e55ec9a8d9a5a7e0deed09c23897ca64c28387ef6a67c` | `98d39a0ce873fa140893971d393865a5349a3b1bd3027173b66100b8fc38f626` | Exit 0; no vulnerabilities found |

Local-only evidence: `.cache/security-tools/compiler-ab-build.txt`, `compiler-ab-local-exact.txt`,
`new-compiler-controller-govulncheck.txt`, `new-compiler-local-exact-govulncheck.txt`.

## Final Controller artifact

- Image: `dist/appliance-security1/controller.qcow2`.
- Image SHA256: `ae9890a20ff84e112441c30abc0a4a1dedfe4f3898208a8c1619bee9ed66202f`.
- Matching source SHA256: `958723923c1e144e517229693a7224f2524030fe79830b761bcb79e6c8aba451`.
- Actual extracted Controller binary SHA256: `93a5a381edb9636760f1b37c1fafe361829e6145cc3df6c73dfb4f5e06ff1ff9`.
- Actual binary build information: `go1.27.1`, `CGO_ENABLED=1`, Linux/amd64.
- `govulncheck -mode=binary` on that extracted binary: **exit 0, “No vulnerabilities found.”**
- `qemu-img check`: **no errors**. Detached Ed25519 manifest signature independently
  checked using OpenSSL: **Signature Verified Successfully**.

At signing, no drift between retained image source and appliance scripts, Controller code, shared
web-security code or UI. Later host-only descriptor-inheritance fixes are in the host package's
separate snapshot. Signing key is a local lab key. Local-only evidence:
`.cache/security-tools/security1-controller-buildinfo.txt`, `security1-controller-govulncheck.txt`,
`.cache/security-tools/security1-controller/`, build log `/tmp/nex-appliance-security1.log`.

## Limits

Scanner result for this database version only; not exploitability, whole-image security,
restoration, Debian source delivery or artwork redistribution clearance.
