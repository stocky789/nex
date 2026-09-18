# Controller package startup gate

Date: 2026-09-18. Scope: BUG-0246 release-validation follow-up on the 0.3.2
development tree. Local Linux amd64, Go 1.27.1, Python, OpenSSL and dpkg-deb;
temporary state only, with no installed package or live infrastructure changes.

## Executed regression

`python3 tests/integration/controller_package_smoke.py` passed both cases:

- The current Controller compiled with `CGO_ENABLED=1`, packaged as a disposable
  `.deb`, extracted and started with a fresh SQLite database. Loopback HTTPS served
  the fixture UI and exact fixture source archive. Administrator login and a
  subsequent authenticated session read succeeded.
- The same Controller compiled with `CGO_ENABLED=0` was rejected. Its process exited
  with the original `go-sqlite3 requires cgo` startup diagnostic, which the gate
  included in its failure output. The regression fails if that package is accepted.

The same command also passed inside a disposable `debian:13` container with Python,
ca-certificates, gcc and libc6-dev installed, using the same Go toolchain and read-only
source/module mounts (image digest
`sha256:f324c7ff54321e8d9c588493a20244965938ce0aa50bbd1022d38010e9ffc4b1`).
Both binaries were rebuilt inside Debian. The working package
passed all checks; the CGO-disabled package was rejected for the same SQLite error.
OpenSSL is supplied by the existing ca-certificates dependency in the release builder.

These are actual Controller processes and SQLite operations. The UI, source archive
and package metadata are explicitly synthetic fixtures, not release artifacts or
corresponding-source compliance evidence. Temporary package contents, credentials,
database and subprocesses are cleaned up by the check.

## Delivery and limits

Both release workflows call the gate before creating or uploading a release, using
the exact built `.deb` in the existing Debian 13 Controller builder. The existing
Docker wrapper translates the read-only workspace mount for containerized runners;
the Controller and HTTPS client run inside the same container. Both `phase1` workflows
run the real good/bad package regression on pushes and pull requests.

No new release package/ISO build, exact release-builder run, hosted GitHub/Gitea execution,
publication, package installation/upgrade, maintainer-script or systemd walkthrough
was performed for this change. Those results are not established by the fixture run.
Phase 3 installation/recovery, Phase 4 authority and physical-hardware gates remain open.

Focused package-gate wiring (2 tests), distribution packaging (24), CI security (6),
documentation-gate (29), and repository privacy (1) checks passed. API generation,
`python3 scripts/check_docs.py --worktree`, and `git diff --check` passed.
Independent Terra 5.6 medium review found no remaining substantive defects.

API impact: **none** — verification calls existing UI, source and session routes;
no client, Controller/Core command or result contract changes. Guide impact: **none** —
release validation changes no operator workflow. State owner: **none** — the check's
temporary Controller database is discarded and adds no product-owned durable state.

## Exact current-builder follow-up

The subsequent [native lab](2026-09-18-native-placement-lab.md) built the actual
Controller package from the final source archive and ran this checker against that
exact artifact inside Debian 13. SQLite startup, packaged UI, matching source and
authenticated session checks passed. Its artifact hashes are recorded there. This
closes the local real-package gap, not installed-systemd or hosted-release acceptance.

## Subsequent artifact and installed-service sign-off

The separate [0.3.2 artifact lab](2026-09-18-release-artifact-lab.md) subsequently
passed the real build, exact-package startup and installed systemd lifecycle,
including Controller same-version reinstall, crash recovery and reboot. That
report binds results to artifact hashes and excludes later source edits. The
fixture-only limits above describe the original check, not this later lab.
Hosted acceptance remains open: inspected committed-HEAD Gitea jobs failed,
GitHub evidence was unavailable, and these uncommitted artifacts have no hosted
release result. Full Phase 3 is not signed off.
