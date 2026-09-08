# Phase 1 final checks and build

Date: 2026-09-08. Scope: the new Controller/agent foundation and its UI, APIs,
delivery examples, guide and tests. Installer work belongs to a separate active
workstream and was not changed by this implementation. Existing installer bug
ledger entries were preserved when the Phase 1 regression entries were appended.

## Executed checks

| Check | Actual result |
| --- | --- |
| `go test -race -count=1 ./...` | Passed: agent 1.121s, Controller 10.290s. Command/protocol packages compile and have no separate tests. |
| `python3 -m unittest discover -s tests -p 'test_*.py' -v` | All 20 API, source-bundle and documentation-tool tests passed. |
| `python3 scripts/generate_api.py --check` | Generated offline reference is current. |
| `npm --prefix web run typecheck` and `npm --prefix web run build` | Both passed. |
| Process/browser acceptance | All 14 scenario groups passed; see [exact results and inspected captures](acceptance.md). |
| Debian native libvirt smoke | All five groups passed with the final agent source; see [environment and results](debian.md). |
| Independent review | Corrections verified; no remaining implementation blockers. See [review evidence](review.md). |
| `git diff --check` | Passed. |

The guide's setup, enrollment, status, manual rotation and restart commands were
also walked using default probe/report intervals and native local observations;
the [acceptance report](acceptance.md) distinguishes that check from accelerated
failure testing and browser certificate exceptions.

## Final build and corresponding source

`make build` passed using Go 1.27.1, GCC, Node 24.20.0 and npm 12.0.2. Binaries
and UI were built from the extracted source snapshot. All 130 source-manifest
hashes passed verification; retained notices, SQLite C source and approved UI
artwork were present, and unrelated installer/runtime/cache inputs were absent.
An isolated npm cache populated only from the retained package tarballs completed
`npm ci --offline`, typecheck and build successfully.

| Artifact | SHA-256 |
| --- | --- |
| `dist/nex-controller` | `b34dd3775dff9608ec3a2d7a60ff2f8ad5452a7cad837379d3ae678cdc7d06f5` |
| `dist/nex-core-agent` | `fcb6d4e0ca5da0a7b3de7a45d13bf82b4f3d33a1ffc5c12febb972ec2c1a3e3f` |
| `dist/nex-source.tar.gz` | `005444a2263540d9021685e576c25eed33f2b24463f6fda77bf156743393ee2d` |

The final Controller started with disposable private state and a verified
localhost TLS certificate. Unauthenticated HTTPS `GET /source` returned 200
and all 124,167,896 archive bytes matched the source digest above. The test
process and private state were removed. Process-suite hashes differ because that
suite builds without the delivery build's `-trimpath` option; its report names
the binaries it actually ran.

systemd unit syntax was checked against temporary paths pointing to the built
executables. No systemd services were installed or enabled on this workstation.
Runtime service deployment remains untested; the supplied units are development
examples rather than a certified package installation flow.

## Documentation and hosted checks

The complete active working tree's `python3 scripts/check_docs.py --all` currently
fails on the installer-owned `docs/guide/install-nex-core.md` link to the still
missing `docs/qa/iso/2026-09-08-nex-wizard.md`. This unrelated in-progress evidence
was not fabricated or removed to make the checker pass.

A clean, isolated Git snapshot checked the Phase 1 change range independently:
`python3 scripts/check_docs.py --base <baseline> --head <snapshot>` passed for
**69 changed paths**, including the assessment. The snapshot started from the
repository's committed baseline and overlays only the exact paths in the Phase 1
assessment, with only the appended Phase 1 ledger section overlaid onto the
baseline ledger. This preserved the installer agent's unrelated working edits.
This scoped pass establishes Phase 1 structural and assessment coverage, not a
green check of the combined active working tree or a hosted CI result.

Both GitHub and Gitea Phase 1 workflows run Go race tests, API/source/tool checks,
UI checks, browser/process acceptance and native Debian/libvirt smoke on pushes
and pull requests. Hosted runs and branch-protection enforcement were not
executed or inspected; workflow files establish configured checks only.

## Limits and impact

API impact: **new contract** for Phase 1, implemented on both sides and documented
in the [API reference](../../API.md). Guide impact: **updated**, with actual
walkthrough evidence in the [Controller guide](../../guide/manage-controller.md).

This is development foundation acceptance. It does not establish VM creation,
console access, guest survival, physical KVM/storage correctness, full Phase 0,
installer enrollment, systemd deployment or release redistribution certification.
VM lifecycle and console work remain Phase 2; installer integration follows its
existing delivery gate.
