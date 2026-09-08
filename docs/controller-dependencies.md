# Phase 1 controller dependency review

This review covers the development controller, agent, and web build. It does not
certify ISO redistribution or resolve the separate artwork gate in
[LICENSING.md](LICENSING.md). Original Nex code remains AGPL-3.0-only.

## Exact inspected dependencies

The source bundle retains `go.mod`, `go.sum`, `web/package-lock.json`, the Go vendor
tree, and integrity-verified npm tarballs. Package versions and license files were
inspected from the actual downloaded build dependencies on 2026-09-08.

| Component | Pinned version | License and evidence |
|---|---|---|
| `github.com/mattn/go-sqlite3` | 1.14.32 | MIT; module `LICENSE`, also [upstream tagged license](https://github.com/mattn/go-sqlite3/blob/v1.14.32/LICENSE) |
| Embedded SQLite amalgamation | 3.50.4 | Public-domain statement in `sqlite3-binding.c`; [upstream dedication](https://www.sqlite.org/copyright.html) |
| `react`, `react-dom` | 19.2.8 | MIT; each npm tarball's `package/LICENSE` |
| `scheduler` (React DOM dependency) | 0.27.0 | MIT; npm tarball `package/LICENSE` |
| `@types/react` | 19.2.18 | MIT; package `LICENSE` |
| `@types/react-dom` | 19.2.7 | MIT; package `LICENSE` |
| `csstype` | 3.2.3 | MIT; npm tarball `package/LICENSE` |
| `esbuild` and locked `@esbuild/*` platform packages | 0.28.2 | MIT; esbuild tarball `package/LICENSE.md`, platform package metadata |
| `typescript` | 5.9.3 | Apache-2.0; tarball `package/LICENSE.txt` and `package/ThirdPartyNoticeText.txt` |

These licenses permit the intended combination with AGPL-covered Nex subject to
retaining their notices. Development-only typechecking/bundling packages are not
linked into the Go services. The source archive includes all locked platform npm
packages so its dependency input is independent of the build machine's architecture.
Upstream source tarballs retain their own license; the top-level Nex license does not
replace them. The UI build emits esbuild's external legal-comments file alongside
JavaScript; retain that file with the UI.

Go's runtime and standard library retain their BSD-style license. The source archive
records the actual compiler version and copies its `LICENSE` under `third_party/go/`.
The tested compiler version is recorded in build verification, rather than assumed
from the minimum `go.mod` version.

## Native libraries and deployment boundary

The default Go SQLite dependency embeds its C amalgamation; the build does not set
the `libsqlite3` tag or link a separately installed SQLite library. CGO still links
platform runtime libraries, typically libc. The agent separately executes Debian's
`virsh`; it does not link libvirt implementation code into Nex.

The Phase 1 source bundle covers Nex and its retained direct build dependencies, not
a complete Debian system. Inspect each actual binary's `ldd` output and retain the
matching platform packages' license notices and any required source/relinking materials
before redistributing a binary bundle or image. A machine's installed package license
cannot be inferred from another builder. Debian/ISO component inventory, firmware,
and source-delivery review remain the separate release gates in `LICENSING.md`.

## Matching source access

`make build` compiles from the exact extracted `dist/nex-source.tar.gz` snapshot and
keeps the archive alongside outputs. Configure the controller's `-source-archive`
with that file; `/source` serves it without requiring administrator login. A Git URL,
placeholder file, or archive of a later working tree is not a substitute for matching
source. Each file in the archive has a SHA-256 entry in `SOURCE-MANIFEST.json`, and
the archive has a SHA-256 sidecar. Rebuild instructions are in
[development.md](development.md). This establishes source availability for this build;
it is not blanket certification of unrelated redistributed components.
