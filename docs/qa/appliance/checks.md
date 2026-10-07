# Controller appliance development checks

Date: 2026-09-08. Host-local appliance implementation only; separate installer changes in the
working tree are not part of this slice. Walkthrough results: [lab](lab.md),
[local browser](browser.md).

## Executed checks

| Check | Result |
| --- | --- |
| `.cache/toolchains/go/bin/go test -race -count=1 ./...` | Passed (agent, Controller, localcore, vmhost; command/protocol compiled, no tests). Final vmhost/localcore race after packaged-umask fix: 1.393 s / 20.469 s |
| `python3 -m unittest discover -s tests -p 'test_*.py'` | 51 passed; appliance suite also passed with `ResourceWarning` as error on Debian 13 |
| `python3 scripts/generate_api.py --check` | Passed |
| `npm --prefix web run typecheck`, `build` | Passed |
| `tests/integration/phase1.py --browser` | 14 groups passed (libvirt failures simulated) |
| Local browser suite | Full simulated-VM and blocked-only modes passed |
| `scripts/check_docs.py --all`, `git diff --check` | Passed (structure/links only) |
| Git-range checker on isolated snapshot | Passed for declared file scope; does not certify a future combined commit with installer changes |

## Independent review

Separate read-only reviewers (runtime safety; build/recovery delivery) ran focused race/Python
suites and checked corrections for ownership after revocation, validation before mutation, seed
permissions, disk exclusivity, readiness polling, cleanup/restart, source-snapshot fidelity, TLS SAN
rotation and recovery identity validation. Final reviewer compared 53 packaged source files with
zero differences and inspected clean/restored Controller and stale local-control captures (root
also inspected). Accepted the bounded lab evidence with no unresolved runtime blocker; unexecuted
workflows were not accepted on unit-test passes.

## CI and limits

Both phase1 workflows run Go race, Python discovery, API reference, UI checks and the portable
blocked-only local browser suite on push/PR. Full simulated-VM browser mode needs KVM/bridge/storage
prerequisites and has local evidence only; nested KVM and replacement workflows need the disposable
lab. No hosted CI run or branch protection inspected. Full Phase 2/3, physical hardware and
redistribution certification remain separate.
