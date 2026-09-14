# Phase 1 final checks and build

Date: 2026-09-08. Scope: Controller/agent foundation, UI, APIs, delivery examples, guide and tests.
Installer work was not changed.

## Executed checks

| Check | Result |
| --- | --- |
| `go test -race -count=1 ./...` | Passed: agent 1.121s, Controller 10.290s; command/protocol packages have no tests |
| `python3 -m unittest discover -s tests -p 'test_*.py' -v` | 20 passed |
| `python3 scripts/generate_api.py --check` | Current |
| `npm --prefix web run typecheck`, `run build` | Passed |
| Process/browser acceptance | 14 groups passed; [acceptance](acceptance.md) (includes the default-timing guide CLI walk) |
| Debian native libvirt smoke | 5 groups passed; [debian](debian.md) |
| Independent review | No remaining blockers; [review](review.md) |
| `git diff --check` | Passed |

## Final build and corresponding source

`make build` passed (Go 1.27.1, GCC, Node 24.20.0, npm 12.0.2) from the extracted source snapshot.
All 130 source-manifest hashes verified; notices, SQLite C source and approved UI artwork present;
unrelated installer/runtime/cache inputs absent. `npm ci --offline` from retained tarballs,
typecheck and build passed.

| Artifact | SHA-256 |
| --- | --- |
| `dist/nex-controller` | `b34dd3775dff9608ec3a2d7a60ff2f8ad5452a7cad837379d3ae678cdc7d06f5` |
| `dist/nex-core-agent` | `fcb6d4e0ca5da0a7b3de7a45d13bf82b4f3d33a1ffc5c12febb972ec2c1a3e3f` |
| `dist/nex-source.tar.gz` | `005444a2263540d9021685e576c25eed33f2b24463f6fda77bf156743393ee2d` |

The built Controller (disposable state, verified localhost TLS) served unauthenticated `GET /source`
200; all 124,167,896 bytes matched the digest. Process-suite hashes differ (no `-trimpath`).
systemd unit syntax checked against temporary paths; no units installed.

## Documentation and CI

- `check_docs.py --all` on the full working tree failed on the installer-owned link to the then
  missing `docs/qa/iso/2026-09-08-nex-wizard.md` (not fabricated or removed).
- Isolated Git snapshot (baseline + exact Phase 1 paths): `check_docs.py --base … --head …` passed
  for 69 changed paths. Not a green combined tree or hosted CI.
- GitHub and Gitea Phase 1 workflows configure Go race, API/source/tool, UI, browser/process and
  Debian smoke on push/PR; hosted runs not executed.

## Limits

Development foundation acceptance only: no VM creation, console, guest survival, physical
KVM/storage, full Phase 0, installer enrollment, systemd deployment or release certification.
API impact: new contract ([API reference](../../API.md)). Guide impact: updated
([Controller guide](../../guide/manage-controller.md)).
