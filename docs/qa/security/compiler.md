# Debian builder compiler verification — 2026-09-08

## Scope and method

This report inspects the actual Controller binary extracted from
`dist/appliance-lab6/controller.qcow2` and the actual local-service binary extracted
from `dist/core-local/nex-core-local_0.1.0+lab6.1_amd64.deb`, then checks the updated
compiler and final Controller image. These are disposable development artifacts;
this report does not certify a release or audit every Debian component.

Scanner: `govulncheck v1.7.0`, database `https://vuln.go.dev`, database update reported
as `2026-09-02 19:12:04 UTC`. Commands used the scanner in `.cache/security-tools/`
and `go version -m` to inspect binary build information. Binary mode matches known
advisories to symbols and can report unreachable functions; these results are not
proof that an attacker can exploit every finding. See the
[official scanner limitations](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck#hdr-Limitations).

## Before the change

Both shipped binaries identify their compiler as **go1.24.4**, with `CGO_ENABLED=1`,
Linux/amd64 and `GOAMD64=v1`.

| Actual extracted binary | SHA256 | Binary advisory result |
| --- | --- | --- |
| Controller from lab6 image | `a4d364b8fd3be68f2d118552bcbb3ff01b28c3e7ee689422f10c80afc77a7804` | Exit 3; 25 standard-library advisory symbol matches |
| Local service from lab6.1 package | `19989230104d4239ce2f7e4c6a4367ab6566d08cb292980e065fb53df275c0a2` | Exit 3; 28 standard-library advisory symbol matches |

The inspected old Debian builder had `golang-1.24-go=1.24.4-1`,
`golang-1.24-src=1.24.4-1`, and `golang-go=2:1.24~2`. Its installed Debian changelog
ends with the June 16, 2025 revision and names then-applied fixes. It does not show a
later Debian security backport revision. Thus an upstream version string alone was
not used to infer the installed patch level. Individual runtime exploitability and
all Debian component advisories were not established by this check.

Full local evidence remains in `.cache/security-tools/old-controller-buildinfo.txt`,
`old-controller-govulncheck.txt`, `old-local-buildinfo.txt`,
`old-local-govulncheck.txt`, `old-debian-go-package.txt`, and `old-binary-audit.json`.
These ignored diagnostic files are not distributed artifacts.

## Build change and focused checks

Both Debian Dockerfiles now download Go **1.27.1 linux/amd64**, verify the SHA256
**before extracting**, and set `GOTOOLCHAIN=local`. The pinned archive digest is
`63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445`, checked against
[official Go download metadata](https://go.dev/dl/?mode=json). Actual Docker build
output reported `/tmp/go.tar.gz: OK`, followed by `go version go1.27.1 linux/amd64`.
C compilation and libc still come from Debian 13; no workstation-linked executable
is copied into the appliance.

`python3 -Werror::ResourceWarning -m unittest discover -s tests -p test_appliance_build.py`
ran **10 tests, all passed**. This includes executing both Dockerfiles' checksum
commands against a tampered fixture and confirming failure, plus successful verification
with the fixture's matching digest. The firstboot regression also verifies the aligned
16-Unicode-codepoint minimum, 1024-UTF8-byte maximum, and rejection of NUL/CR/LF.
Both existing Actions configurations discover this Python suite; no hosted CI run is
claimed here.

The Go BSD notice is installed directly in the guest at
`/usr/local/share/nex/go-LICENSE`. Its SHA256 is
`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`, and its bytes
match `third_party/go/LICENSE` in the retained source archive. Package delivery
separately retains the Go notice and actual binary compiler version in package docs.

## Compiler-only comparison

To isolate the compiler change, each original binary's own retained source was compiled
inside Debian using Go 1.27.1, `go build -mod=vendor -trimpath`, and the same command
package. These comparison binaries do not include later Nex authentication changes.

| Comparison | Exact input source SHA256 | New binary SHA256 | Actual scan |
| --- | --- | --- | --- |
| Controller, original lab6 image source | `d4fc22c13c6f160f03f59fe65892b10182045de5c6ba2b6a7c3d5dc3ce8221f3` | `6203421d745c54c14095a87e19073c2f25005c6e16920aaa09d006e882f23b52` | Exit 0; no vulnerabilities found |
| Local service, source extracted from original lab6.1 package | `b4228bcd0aa3e5f8143e55ec9a8d9a5a7e0deed09c23897ca64c28387ef6a67c` | `98d39a0ce873fa140893971d393865a5349a3b1bd3027173b66100b8fc38f626` | Exit 0; no vulnerabilities found |

The local comparison explicitly uses the source extracted from the `.deb`; it does not
assume that package source matches the older Controller image's broader source snapshot.
Build evidence is `.cache/security-tools/compiler-ab-build.txt` and
`compiler-ab-local-exact.txt`; scanner evidence is
`new-compiler-controller-govulncheck.txt` and `new-compiler-local-exact-govulncheck.txt`
in that same cache directory. The reduction from 25/28 advisory symbol matches to zero
is a scanner result for this database version, not a whole-system security certification.

## Final Controller artifact

- Image: `dist/appliance-security1/controller.qcow2`.
- Image SHA256: `ae9890a20ff84e112441c30abc0a4a1dedfe4f3898208a8c1619bee9ed66202f`.
- Matching source SHA256: `958723923c1e144e517229693a7224f2524030fe79830b761bcb79e6c8aba451`.
- Actual extracted Controller binary SHA256: `93a5a381edb9636760f1b37c1fafe361829e6145cc3df6c73dfb4f5e06ff1ff9`.
- Actual binary build information: `go1.27.1`, `CGO_ENABLED=1`, Linux/amd64.
- `govulncheck -mode=binary` on that extracted binary: **exit 0, “No vulnerabilities found.”**
- `qemu-img check`: **no errors**. Detached Ed25519 manifest signature independently
  checked using OpenSSL: **Signature Verified Successfully**.

At signing, comparison against the retained image source found no drift in appliance
scripts, Controller code, shared web-security code, or UI source. The image retains its
own immutable source archive. Later host-only descriptor-inheritance corrections belong
to the host package's separate source snapshot; the image archive is not presented as
an archive of every latest host-source change. The signing identity is a local lab key,
not an established production release key.

Final-binary evidence remains in `.cache/security-tools/security1-controller-buildinfo.txt`
and `security1-controller-govulncheck.txt`; the extracted binary and Go notice remain in
`.cache/security-tools/security1-controller/`. The build log is
`/tmp/nex-appliance-security1.log`. Package checks and actual boot/recovery evidence are
reported separately; a clean Go binary scan does not prove the complete image secure,
prove restoration, or close Debian source-delivery and artwork redistribution gates.
