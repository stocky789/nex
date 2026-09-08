# Controller appliance development checks

Date: 2026-09-08. This report covers the host-local appliance implementation;
separate installer changes in the working tree are not part of this slice.

## Executed checks

- `.cache/toolchains/go/bin/go test -race -count=1 ./...` passed for agent,
  Controller, localcore and vmhost. Command/protocol packages compiled without
  separate tests. Additional focused runs verified subsequent owner corrections; the final vmhost
  and localcore race run passed in 1.393s and 20.469s after the packaged-umask fix.
- `python3 -m unittest discover -s tests -p 'test_*.py'` passed 51 checks in the
  combined verification, including API/source, package, appliance and recovery checks.
  A connection-cleanup warning was corrected explicitly; the appliance suite also
  passed with `ResourceWarning` treated as an error on Debian 13.
- `python3 scripts/generate_api.py --check` passed for central and local references.
- `npm --prefix web run typecheck` and `npm --prefix web run build` passed.
- `.cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser` passed
  all 14 existing process/browser groups after the new implementation was added.
  Its libvirt failures remain explicitly simulated as documented in Phase 1.
- The [local browser suite](browser.md) passed full simulated-VM and portable
  blocked-only modes. The primary also inspected the desktop review, real-host
  overview and final clean deployment/Controller workspace captures.
- `python3 scripts/check_docs.py --all` and `git diff --check` passed. The former
  checks structure and links, not changed-file coverage or semantic completeness.
- The documented Git-range checker also passed against a clean isolated snapshot:
  unrelated workspace prerequisites were the base, and the appliance assessment paths
  plus assessment record formed the next commit. This verifies the declared file scope;
  it does not certify a future combined commit/PR containing unrelated installer changes.
  No commits were made in the shared project checkout.

## Independent review

Separate read-only reviewers inspected runtime safety and build/recovery delivery.
Owners corrected and reviewers checked ownership conflicts after host revocation,
request validation before durable mutation, private seed permissions, disk exclusivity,
readiness polling, cleanup/restart handling, source-snapshot fidelity, TLS SAN rotation,
and complete recovery identity validation. Reviewers ran focused race/Python suites.
The final reviewer compared 53 packaged runtime/reference source files with the current
implementation with zero differences and inspected clean/restored Controller and stale
local-control captures. Root also inspected these captures directly.

The final clean package reached verified readiness without manual guest corrections.
Separate-host recovery preserved login, TLS identity, source and inventory IDs, and the
existing host reconnected reporting the excluded original VM stopped. A replacement-host
reboot autostarted the restored VM and passed the same browser/identity/reconnection checks.
The independent reviewer accepted this bounded evidence with no unresolved runtime blocker.
Exact artifact and
walkthrough results are in [lab evidence](lab.md); installation/removal results are in
[package evidence](package.md).
Reviewers explicitly did not accept unexecuted workflows based on unit-test passes.

## CI and limits

Both GitHub and Gitea Phase 1 workflows run Go race tests, Python discovery,
API reference checks, UI checks and the portable local-browser blocker suite on
pushes and pull requests. The full simulated-VM browser test requires actual local
KVM/bridge/storage preflight prerequisites and has separate local execution evidence.
Real nested KVM and appliance/replacement workflows require their explicitly selected
disposable lab; the portable browser mode does not silently claim that coverage.
No hosted CI run or branch protection was inspected or configured in this task.

API impact: **new contract** for local control/deployment and central bootstrap/adoption.
Guide impact: **updated** with operator setup and actual walkthrough limits.
Full Phase 2/3, physical hardware, and redistribution certification remain separate.
